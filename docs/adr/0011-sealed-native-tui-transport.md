# ADR 0011：密封启动与可判定的原生 TUI 传输

- 状态：已接受（Accepted）
- 日期：2026-08-05
- 决策来源：真实 Qwen/OpenCode/Pi 委派与 Pi Live E2E 暴露的权限扩张、输出风暴和协议漂移

## 背景

ADR 0009 已决定用 `TerminalSession` 承载真实 Agent PTY/TUI，ADR 0010 已定义受控自治和人工介入。但仅把 `opencode`、`qwen` 或 `pi` 的默认交互命令放进 cmux 仍不能形成安全、可判定的 Worker Attempt。

真实委派验证了以下问题：

- 默认交互配置可能暴露 TaskSpec 未授权的工具或子 Agent；Qwen 在没有 shell 时曾尝试调用子 Agent 绕过限制；
- OpenCode/Pi 可长时间停留在推理阶段而没有产物，Pi JSON 模式还会重复输出累计思考内容，短时间产生大量 stdio；
- cmux/Desktop 的环境不是 Worker 环境，既可能缺少认证，也可能包含 Publisher 或宿主凭据；
- 把环境值拼进可见 shell command 或 argv 会泄露敏感配置；
- TUI 通常在一轮完成后继续运行，`worker-result.json` 的出现不能单独证明 Provider 已到达稳定完成边界；
- Pi `0.83.0` 的真实流同时证明 Event Shape 和终止序列会漂移，屏幕文本不适合作为协议。

因此，原生 TUI 的可观察性不能以放弃 Adapter 权限、预算和完成协议为代价。

## 决策

### 同一 Adapter 负责两种传输

每个 Worker Adapter 同时拥有 Provider 语义和权限策略。`captured-process` 与 `interactive-pty` 只能改变进程传输方式，不能改变：

- executable realpath、digest 与精确版本；
- cwd、TaskSpec、Policy、Capability 与 Attempt Identity；
- 工具/子 Agent/网络/发布权限；
- wall-time、输出、工具调用、Turn 与 Session Budget；
- WorkerResult 规范化与完成条件。

Core 不根据 Provider 名称拼接默认 TUI 命令。Adapter 必须产生冻结的 `TerminalLaunchSpec`，并声明它能提供的 `CompletionGate`。

### 密封启动信封

PTY Backend 不继承 Desktop/terminal 的 ambient environment，也不把环境值写入可见 shell command、cmux RPC、argv、标题或描述。

Marshal 在当前 Attempt 的 owner-only runtime 目录写入一次性的 `LaunchEnvelope`：

- 包含精确 executable、digest、argv、cwd 与完整 allowlisted environment；
- 文件必须是非 symlink regular file，父目录和文件均为 owner-only；
- Backend 可见命令只包含受信任 Marshal launcher 的绝对路径与 envelope 路径；
- launcher 重新校验路径、权限、identity、executable digest 与 cwd；
- launcher 在 `exec` Worker 前删除 envelope，并使用精确环境替换 ambient environment；
- envelope 的摘要和非敏感字段进入 Attempt 证据，环境值不得进入 Journal、screen 或普通日志。

Local MVP 仍不声称能防止同 UID 恶意进程在删除前读取文件；该风险属于 `workspace-write` 的既有限制。`hardened` Profile 必须用更强的 secret/mount 边界。

### 完成门禁

原生 TUI 成功必须同时满足：

1. Schema-valid 且 identity 匹配的 WorkerResult；
2. Adapter 声明并验证的 Provider completion/idle boundary；
3. Attempt 未超时、未被 abort，TerminalSession 未异常丢失；
4. Marshal随后重新观察 Git Snapshot，并运行独立 Verification 与 Review。

允许的 `CompletionGate`：

- Provider 原生 lifecycle/event hook；
- 受版本冻结的 JSON-RPC/ACP session state；
- `supervised`/显式 opt-in 模式下，由 Lead/操作者通过 Marshal Control Plane 确认本轮结束。

最后一种确认只结束传输，不接受代码，不豁免 Verification/Review，并生成 Intervention/Completion Record。屏幕文本、workspace idle 猜测、文件静默期和 Worker 的自然语言“完成”均不能单独构成自动完成门禁。

缺少自动 `CompletionGate` 时，`interactive-pty` 只能进入受监督模式；默认 `task run` 继续使用 `captured-process`。Fallback 只允许在 Attempt 启动前发生，启动后不能把同一 Attempt 从一种传输切到另一种。

### 预算与控制

- PTY Attempt 与 captured Attempt 使用同一个冻结 deadline；
- TerminalSession 必须在 deadline 到达时控制完整后代进程树；
- screen/readback 只用于显示和有界诊断，不进入成功判定；
- Prompt、Steering 与 Control Record 继续受 32 MiB 等既有上限；
- Provider 原生工具/Turn Budget 由 Adapter 保留，不能因进入 TUI 而消失；
- direct PTY 输入继续按 ADR 0010 标记 mixed provenance，并强制重新验证和审查。

## 首批实现顺序

1. `LaunchEnvelope`、受信任 launcher 与路径/权限/删除前执行测试；
2. Provider-neutral `TerminalLaunchSpec` / `CompletionGate` Port；
3. 三 Adapter 的原生 argv、环境与权限一致性测试；
4. cmux 受监督 Pilot E2E；
5. 至少一个具有自动 completion gate 的完整 Worker→Verification→Review E2E；
6. 有证据后才允许把 PTY 作为可选 `task run` transport。

## 影响

- 用户能看到真实 Agent TUI，并通过 Marshal进行可审计 Steering；
- cmux、后续 tmux/iTerm2/Ghostty 只负责 PTY，不决定 Worker 权限；
- 默认 captured 路径继续适用于 CI、无终端和自动执行；
- 原生 TUI 的实现增加一次性 launcher 与 completion gate，但避免凭据泄露和“看起来完成”的伪成功；
- Provider 默认配置不再被视为 Adapter 策略。

## 未采用方案

- **直接在 cmux 启动默认 Agent 命令**：权限、环境和预算不冻结；
- **把环境变量拼入 shell command**：会进入 screen、history、RPC 或进程信息；
- **继承 Codex Desktop/cmux 环境**：可能泄露 Publisher/宿主凭据且不可重放；
- **只轮询 WorkerResult 文件**：不能证明 TUI 已结束本轮；
- **解析屏幕上的“完成”**：受布局、截断、人工输入和 Provider 文案影响；
- **启动后降级到 captured**：会产生两个写入者或无法证明的部分执行。
