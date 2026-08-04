# Worker Adapter

## 目的

Adapter 将某个 Provider 的调用方式、Event Stream、Permission 和 Session Behavior 转换为 Marshal Worker Contract。Adapter 不管理任务状态、验证、Review、Git 发布或 Policy Decision。

## Adapter Interface

概念接口：

```text
probe(executable) -> CapabilitySnapshot
start(WorkerRequest, AbortSignal) -> WorkerEvent stream + WorkerResult
resume(WorkerRequest, SessionRef, AbortSignal) -> WorkerEvent stream + WorkerResult
cancel(AttemptRef) -> CancellationResult
```

`resume` 可选。TaskSpec 要求 Resume，而 Adapter 不支持时，必须在 Capability Validation 阶段失败。

## Capability Probe

进入 `READY` 前执行 Probe，并记录：

- 稳定 Adapter ID；
- 精确 Executable Path 与可用的 File Identity；
- Version Output；
- Structured Output Mode；
- Non-interactive Edit 能力；
- Session Create/Resume 语义；
- Model Selector；
- Native Sandbox 与 Permission Flag；
- Native Time/Tool/Turn Budget；
- 已知不兼容或 Deprecated Option；
- Probe 时间与 Adapter Implementation Version。

Probe Result 是事实，不能根据文档 URL 猜测。已安装二进制缺少 TaskSpec 所需能力时，Marshal 必须拒绝继续。

## WorkerRequest

Request 包含冻结的任务与 Run Identity、worktree 与 base SHA、TaskSpec/Policy/Capability Digest、Attempt Budget、Review Finding、Session Policy、独立 `controlRoot` 和最终 WorkerResult Path。控制根只开放当前 Attempt 的只读 `input/` 与可写 `output/`，不能开放整个 `.marshal`。详见 [ADR 0006](adr/0006-attempt-control-root.md)。

Prompt 由带版本 Template 渲染并保存到 Attempt。Adapter 可以增加 Provider 专属格式，但不能删除约束。

原生 TUI 仍由同一 Adapter 生成冻结的 executable、argv、allowlisted environment、Provider Budget 与 `CompletionGate`，不能调用 Provider 默认配置作为隐式策略。PTY Backend 只消费 `TerminalLaunchSpec`，不根据 Adapter ID 拼命令。缺少可信自动 completion/idle 信号时，Adapter 只能声明受监督 PTY，不得把 WorkerResult 文件或屏幕文本单独当作完成协议。启动边界见 [ADR 0011](adr/0011-sealed-native-tui-transport.md)。

当前 Qwen、OpenCode 与 Pi Adapter 均实现 `PrepareTerminal`：先校验 WorkerRequest、精确二进制版本与 realpath/digest，再返回原生 TUI argv、完整替换式环境、worktree、独立初始 Prompt 和 `supervised-confirmation` 门禁。原生环境移除 captured 专用的 `CI=1`，显式冻结 `TERM=xterm-256color` 与 `COLORTERM=truecolor`，不依赖 Desktop/cmux ambient environment。Qwen 保留 safe-mode、工具排除和原生 wall/tool/turn/session 预算；OpenCode 保留 `--pure` 与经过 `debug config` 反向验证的权限配置；Pi 保留无 bash 工具白名单、关闭扩展/Skill/上下文和 ephemeral session。三者均移除 captured 模式的 JSON/print/位置 Prompt 参数，且目前不接入默认 `task run`。

`terminal.StartPrepared` 是 Adapter 与 PTY Backend 之间唯一的 provider-neutral 映射：它校验 Adapter identity 与 completion gate，复制 argv/environment，并把 Adapter 冻结的 executable digest 传入密封 launcher。Backend 与 `LaunchEnvelope.Seal` 都会重新计算并比对该 digest，防止在 Adapter probe 与启动之间替换 Worker 二进制。该映射仍是显式受监督 Pilot API，不会改变默认 captured transport。

cmux Probe 除 capability discovery 外，还执行 3 秒上限的只读 `workspace list --json` 健康检查。若 socket 能响应 capability 但 workspace RPC actor 卡死，Probe 返回 `workspace-rpc-unavailable`，不得创建 Pilot workspace，也不得自动重启 cmux 或关闭用户已有 workspace。

## Normalized Event

每个 Event 包含 `sequence`、`timestamp`、`runId`、`attemptId`、`adapter` 和 `kind`。初始 Event Kind：

```text
attempt.started
session.started
message.completed
tool.started
tool.completed
file.declared
artifact.declared
usage.updated
warning
error
attempt.completed
```

Partial Token/Message Delta 可选，默认关闭以控制日志量。Native Raw Output 单独保留，受字节上限和脱敏控制，不能替代 Normalized Event。

## 进程规则

- 使用 argv 直接 Spawn，不经过 Shell。
- Cwd 设置为任务 worktree。
- 进入 `READY` 前解析 Executable。
- 使用环境 Allowlist 并显式构造 `PATH`。
- 分别捕获 stdout 和 stderr。
- 限制输出并标记 Truncation，不能丢失最终状态。
- 超时后先 Graceful Termination，再在有限时间后终止整个 Process Tree。
- 独立记录 Exit Code、Signal、Duration 和 Protocol Parse Error。
- Structured Completion 缺失时，不能仅根据 Exit Code 判断成功。

## 首批 Adapter

### Qwen Code

预期能力：Headless 调用、JSON/stream-JSON、Project-scoped Session Resume，以及版本支持时的 Sandbox 和 Native Budget。

Adapter 必须显式解析 `qwen`。不得静默回退到独立安装的 `qwen-code`，因为二者可能具有不同版本和 Option Set。

参考：[Qwen Code Headless Mode](https://qwenlm.github.io/qwen-code-docs/zh/users/features/headless/)。

### OpenCode

预期能力：`opencode run`、Raw JSON Event、显式 cwd、Agent/Model Selector、Session Continuation、Permission Configuration，以及后续 ACP/Headless Server。

MVP 使用 one-shot `run`。只有 Core Lifecycle 和 Cancellation 通过一致性测试后，才考虑 ACP/Server。

M4 冻结并实测 OpenCode `1.18.12`，M6 在本机自动更新到 `1.18.13` 后先 fail-closed，再以完整 Conformance 与 Live E2E 重新验收并更新精确 pin。CLI 通过 `MARSHAL_OPENCODE_PATH` 接受唯一绝对 executable，Probe 冻结 realpath、SHA-256 与精确版本；运行使用 `--pure --format json`、环境 allowlist 和 fail-closed `permission`。未知版本在 Worker 启动前失败。Local Profile 不宣称抵抗同 UID 恶意进程。

参考：[OpenCode CLI](https://opencode.ai/docs/cli/)与[权限](https://opencode.ai/docs/permissions)。

### Pi

预期能力：Non-interactive Print、JSON Event、显式 Session ID/Directory、Tool Allow/Deny List，以及后续 JSON-RPC。

MVP 使用 one-shot JSON。只有测量证明需要 Session Steering 或降低启动耗时后，才增加 RPC。

参考：[Pi Coding Agent README](https://github.com/badlogic/pi-mono/blob/main/packages/coding-agent/README.md)。

## 权限归一化

Provider Flag 的安全强度并不相同。Marshal 定义期望 Profile，由 Adapter 证明能否满足。

### `read-only`

Worker 可读仓库，并只能运行显式只读命令。禁止 Edit 与 Publish。

### `workspace-write`

Worker 可读写任务 worktree 并运行开发命令。禁止 Publish。在普通开发机上它只降低误操作风险，不隔离恶意代码。

### `hardened`

Worker 在可强制执行的 Container、VM 或同等 Sandbox 中运行，使用显式 Mount、Network Policy、Resource Limit，且不能访问宿主机凭据。

仅给模型传 Tool Allowlist 不能让 Adapter 把 Run 标记为 `hardened`；进程执行层必须真正强制边界。

## Prompt Contract

Worker Prompt 必须说明：

1. Objective 与 Non-goal；
2. Allow/Deny Path；
3. Required Deliverable；
4. Worker 可运行的反馈命令；
5. Marshal 会独立验证全部声明；
6. 禁止 Push、创建 PR/MR、Merge 和发现凭据；
7. WorkerResult Path 与 Schema；
8. 被阻塞时报告 Blocker，不扩大范围；
9. Rework 时的 Finding 与保持不变的约束。

`AGENTS.md` 等仓库指令可以增加本地约定，但不能扩大 TaskSpec 范围或削弱 Marshal Policy。Attempt 必须记录有效指令集合和所发现指令文件的摘要。

## Adapter 一致性测试

每个 Adapter 必须覆盖：

- Version 与 Capability Probe；
- Structured Event 正常完成；
- Edit 与 Untracked File；
- Invalid JSON 与 stdout 噪声；
- 有无部分变更时的非零退出；
- Timeout 与 Process Tree Cancellation；
- Output Truncation；
- 支持时的 Session Resume 与 Missing Session；
- Denied Operation；
- Unicode、空格和长路径；
- WorkerResult 缺失或与真实状态不一致。

所有 Adapter 运行同一个端到端 Fixture。Provider Native Transcript 是测试输入，Core Expected Outcome 必须共享。

## 版本漂移

Adapter 支持范围是 Adapter Implementation、Binary Version 与 CapabilitySnapshot 的受测组合。未知版本只能在显式 Experimental Mode 下使用，并在 Outcome Bundle 中标记。安全相关能力不得静默回退或删除 Flag。
