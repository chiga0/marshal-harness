# Pi AgentProvider：原生 RPC 与工具权限桥候选

本包复用 `agent-runtime.launchProtocol` 的原 guard，并通过 Pi 自带 extension/工具 operations 接缝对接正式 FileBusiness。它没有注册到默认 TaskService 配置或发行支持矩阵；确定性组合回归不等于真实模型交付、B2 关闭或 production。实验目录中禁工具 Pi 的成功不作为本包正式接入证据。

```js
const provider = createPiProvider({id, executable, args, env,
  bridge: {sdkEntry: '/absolute/pi-package/dist/index.js'}});
const handle = provider.start({cwd, deadline, prompt, onProgress, onPermission});
const started = await handle.started;
const result = await handle.completion;
// 调用方先持久化取消 fence，再调用 await handle.stop()。
```

`executable/args/env/bridge` 只由可信 composition 明确注入、复制冻结，HTTP/模型不能指定。实际入口可为固定 Node 加已安装 `pi/dist/bundle/cli.js --mode rpc --no-session`。不读取鉴权、不复制 HOME、不切换模型、不 fallback、不添加禁工具/禁 Skill/禁扩展参数。本机研究的 SDK 为 0.84.4；按实际 RPC/SDK 能力匹配，不按精确版本白名单。

## 原生权限及工具

- 组合显式选择 `bridge` 时，Provider 添加同包受版本管理的 `native-bridge.mjs` 扩展。每个执行的随机 nonce、cwd、原始期限与 SDK 入口只用于父进程/扩展关联；扩展读完移除配置环境变量。nonce 不是恶意代码沙箱或加密 attestation。
- 启动须收到当前扩展 ready，再核对原生 session 空闲、无历史消息/待执行队列，之后才发送唯一 prompt。ready 缺失/绑定失配不继续；客户端不能从模型文字中猜测能力。
- 扩展保留原 active tools 清单，使用原 SDK 的 `read/write/edit/grep/find/ls/bash` definition：参数 schema、prompt metadata、文件操作和原输出格式不重写。权限检查位于最终 `execute` 包装层，晚于可能改写参数的原生 `tool_call` hooks。复制实际参数，交现有 `onPermission`，只接受当前 `allow_once`；取消、期限、拒绝或迟到回答都不执行。
- 回调保持已有请求形状：`sessionId/toolCall/options`；`toolCall.rawInput` 是 Pi 原参数，`toolCall._meta` 给出 `provider:pi/toolName`。业务授权器须理解原生 `path/content/command`，不把自由文本 scope 或一种品牌的 `file_path` 字面量当所有工具权限。默认拒绝；不会吞掉 FileBusiness 已要求的回调。
- bash 仍由原生工具解析/格式化，通过官方 operations 注入 `detached:false` 子进程；使用同 SDK shell 解析与环境。自定义 shell 可显式传 `bridge.shellPath`。单工具最多 4 MiB 输出，执行内最多 4 个并行 shell，超时不超过原 deadline。保留退出后输出 idle grace；最终继承后代仍由原 Runtime guard 整组清理。
- 当前 POSIX 适配不支持 Windows powershell。未接入的 custom 工具保留可见，但执行前明确 capability 拒绝；不会无声启用/禁用工具。其他扩展后续替换执行实现、绕过包装而没有当前授权关联时，返回 unknown，不能根据同名工具冒充权限/清理。需要 custom 工具时应增加明确受管实现，而非 allow-all。

## 结果、停止及边界

同步返回 `started/completion/stop/snapshot`；`get_state→prompt→agent_settled` 与原 Runtime 实际清理共同形成结果。prompt ack/agent_end 可能仍有重试，不作为终态；`stopReason:stop` 才映射 `end_turn`。原生自动重试仍受原期限约束；不会给 Core 新增 Attempt 或重置预算。Agent 终态不是 Decision；业务仍独立验收。usage 不猜测计费。

公开进度只有 phase、观察时间、工具 id/kind/status，不带参数/结果、thinking、session 文件、配置或原始错误。原生真实用户问题不会自动回答，当前未接 Task question writer 时明确失败，不把工具权限当用户业务答案。

`stop()` 原句柄幂等；bootstrap 期间可停止。运行中先给 `clear_queue→abort` 最多 250ms，再调用原 owned stop。取消期间仍保留工具安全义务观察；没有明确包装/拒绝关联的工具返回 `status:unknown/cleanup:null`，原继承组观测可保存为 `runtimeCleanup`，不能释放整个目录。所有清理事实来自原 guard，不按磁盘 PID 杀进程，不提供 `cleanupProven:true` 配置。

不选择 bridge 的 transport 模式只供底层兼容诊断：传入 `onPermission` 立即拒绝，观察到未证明的 shell/custom 工具返回 unknown，不能注册为正式文件业务。选择 bridge 后也仅是可信同 UID、继承进程组；故意自行脱离组、具有外部效果的扩展或 ambient Publisher 凭据不在清理/分权证明内，正式支持仍须部署验收。未改 Core/Store/Decision authority，没有新增插件平台。

## 验证

```sh
node --test --test-concurrency=1 packages/agent-pi-rpc/client.test.mjs packages/agent-provider-pi/*.test.mjs packages/agent-runtime/*.test.mjs packages/agent-provider-acp/index.test.mjs packages/agent-acp/client.test.mjs
```

默认测试使用受版本管理的无模型 SDK seam；实际加载同一个 extension，并执行原 guard、真实文件读写与继承 shell 子进程。`native-bridge.test.mjs` 另以正式 FileBusiness+Depot 验证原输入不变、实际 Runtime 身份与制品采集。可显式设置 `MARSHAL_PI_TEST_SDK=/absolute/pi-package/dist/index.js`，重复该测试来使用已安装 Pi 的原 SDK definition；只加载工具模块，不启动 Pi/登录/模型。原生 CLI 加载扩展、真实模型工具与取消/整个服务恢复仍要独立实机验收，不能由模拟 peer 代替。
