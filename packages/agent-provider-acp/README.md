# ACP AgentProvider 组合层

本包复用 `agent-runtime.launchAcp` 和 `agent-acp.AcpClient`，把一个 Worker 的启动、初始化、会话、回合和所属进程清理组合起来。不导入 Task/SQLite/固定业务，不创建验收结论，不执行发布，不恢复旧 PID。profile 为 `ordinary-user`，不是恶意代码 sandbox 或正式产品完成声明。

```js
const provider = createAcpProvider({id, executable, args, env});
const handle = provider.start({cwd, deadline, prompt, onProgress, onPermission});
await handle.started;
const result = await handle.completion;
// 需要停止时：await handle.stop();
```

`executable/args/env` 只由可信 composition 注入并复制配置值，无品牌分支、HTTP argv 或自动继承 `process.env`。本包不复制 HOME/配置文件；需要原生模型鉴权时由 composition 明确提供其环境，并在实际部署确保 Worker 不可达 Publisher 权限。无客户端 fs/terminal 能力不等于禁用 Agent 原生工具。

## 句柄与完成边界

`start` 同步返回 `{started,completion,stop,snapshot}`，不等待初始化或模型回合：

- `started` 是原 Runtime 启动身份或 `null` 的 Promise。初始化前便可 `stop`。若仍在 Runtime bootstrap，取消标记立即生效，取得受管句柄后立刻停止，不发送初始化/Prompt；bootstrap 自身受 Runtime 原期限约束，不伪称取消调用时进程已消失。
- `stop()` 幂等返回同一 completion Promise；已有 session 时尝试原 ACP cancel，但不把写出通知当作停止证明，仍执行原 owned stop。调用者必须先持久化取消/失败 fence，本包不替它修改 Task。
- `completion` 返回 `{providerId,status,stopReason,reason,sessionId,outputText,usage,cleanup}`。`completed` 只表示 `end_turn`；`max_tokens/max_turn_requests/refusal` 为 `failed`，明确取消为 `cancelled`。任何未确认清理为 `unknown`，不能释放/复用目录或接纳成果。
- `cleanup` 是原 Runtime 的执行 ID、启动、退出、guard 清理和 bytes 观察，保持原 `inherited-process-group` 能力范围，不扩大为进程组外后代的停止证明。回合完成后也停止所属执行，当前不池化长连接。
- 无假 `business accepted` 或制品验收；实际文件采集、独立验证、Task 归属和 current generation recheck 由上层负责。返回文本可能包含业务信息，上层公开/持久化前仍须执行其脱敏策略。

## 有界观察与权限

`snapshot()` 返回 `{phase,observedAt,tool}` 的独立副本；phase 为 starting/initializing/session/running/stopping/terminal。进度回调只收到该规范投影和 `{signal}`，tool 为 `{id,kind,status}`，不转发 title、rawInput/rawOutput、思考、stderr 或 `_meta`。终态可由 completion/snapshot 查询，不能要求一定收到 terminal 回调。

Prompt 最多 256 KiB，`outputText` 仅累计 `agent_message_chunk` 的文本，最多 64 KiB，超限失败而非截断后伪成功。单回合最多 4096 条 ACP update；进度持久化回调最多等待 1 秒，超时/异常转失败并清理，取消撤销其 signal。回调必须尊重 signal；JavaScript 无法强制终止第三方回调内部自建活动。

权限默认拒绝。显式 `onPermission(request,{signal})` 是可信授权策略通道，不是公开进度：保留实际 session ID、toolCallId、tool 输入与选项，去掉 `_meta`；回调可返回原 ACP `{outcome:{outcome:'selected',optionId}}` 或 cancelled。AgentClient 校验原选项、当前 session、有效回合并在取消/结束时撤销未决权限。允许原生工具不等于自动允许请求，真实用户澄清由上层 Task questions/answers 接线。

usage 当前 `{tokens:null,cost:null,currency:null,source:'unavailable',coverage:0}`。没有把 `usage_update.used`（上下文占用）当成本轮计费 token，也不从 `_meta` 猜测账单。后续只有明确定义的供应商账单字段才能增加准确映射。

验证：`node --test packages/agent-provider-acp/index.test.mjs`，使用 checked-in Node ACP fixture 与原 Runtime/guard 的真实进程，不访问网络或模型。覆盖立即取消、初始化/运行取消、默认和显式权限、文本上限、工具进度、回合拒绝、期限、阻塞回调与真实 cleanup；不代替正式平台/Provider 或业务实机验收。
