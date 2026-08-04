# ADR 0009：原生 PTY Terminal Session 执行传输

- 状态：已接受（Accepted）
- 日期：2026-08-04
- 修订原因：真实 cmux 原型证明只读 stdout follower 不等同于可观察的 Agent 会话

## 背景

ADR 0008 建立了跨终端的只读 Observer，但首个 cmux 实现只在 workspace 中跟随 Marshal 已捕获的 stdout/stderr。OpenCode、Pi、Qwen Code 的非交互模式可能长时间缓冲或不输出中间过程，操作者只能看到启动命令和最终 stdio，无法看到 Agent TUI 中的状态、工具调用、审批、错误、Steering 与 Session 恢复信息。

真实 Pi 原型验证了预期体验：cmux 创建原生 PTY，启动 Pi TUI，Marshal通过 surface 注入冻结 Prompt；操作者可实时看到模型进展、工具调用和纠正消息。原型还在执行过程中暴露并纠正了错误文件名与错误 staticcheck 调用，证明其可观察性显著高于 batch `tee`。

原生 TUI 必须由 PTY 会话拥有。Marshal无法同时把同一 Agent 当作普通 pipe 子进程直接拥有，又让另一个终端完整呈现其 TUI。因此这不是 Observer 的显示增强，而是新的执行传输模式。

## 决策

保留 ADR 0008 的 `Observer` Port，并新增独立的 `TerminalSession` Port。两者职责不同：

- `Observer` 只消费已捕获日志、状态和通知；
- `TerminalSession` 创建和控制真实 PTY 会话，返回稳定的 Session Handle；
- Worker Adapter 仍负责 Provider 参数、Prompt、结构化结果和能力约束；
- Core 只选择执行传输，不按终端品牌分叉。

首批执行传输为：

1. `captured-process`：Marshal直接拥有子进程、stdout/stderr 与 Process Group，适用于 CI、无图形终端和默认降级；
2. `cmux-pty`：cmux 拥有原生 PTY，Marshal持有 workspace/surface/session 标识，通过受控 CLI/RPC 注入首轮 Prompt、读取状态、发送中断并恢复 Session；
3. 后续 `tmux`、`iterm2`、`ghostty`、`terminal` 使用相同能力协商，不修改 Adapter 或生命周期。

`TerminalSession` 至少协商以下能力：

```text
session-create
prompt-send
screen-read
lifecycle-events
tool-events
needs-input
interrupt
terminate
session-resume
input-provenance
```

能力缺失必须准确报告。只有 `session-create`、`prompt-send`、`interrupt`、`terminate` 和 Adapter 所需的结果协议都可用时，才能选择 PTY 执行；否则降级为 `captured-process`。

## 权威证据

终端屏幕和自由文本仍不是完成协议。PTY 模式成功至少要求：

1. 冻结 Prompt 指示 Worker 写入指定 `worker-result.json`；
2. Adapter 校验结构化结果、Attempt Identity 与退出/idle 边界；
3. Marshal重新观察 Git Worktree 并计算 Snapshot/Diff Digest；
4. 独立 Verification 重新执行所有必需 Gate；
5. Review 只引用冻结证据，不引用屏幕文本作通过依据。

cmux hooks 可提供 Session ID、`running`、`idle`、`needsInput`、工具事件、Feed 与恢复信息；这些属于可观察证据和控制信号，不能替代 WorkerResult、Git Snapshot 或 VerificationReport。

## 控制与人工介入

- Marshal发送的 Prompt 和 Steering 必须通过 Session Handle 记录来源；
- Backend 能区分非 Marshal 输入时，任何人工输入生成 `manual-intervention` Diagnostic；
- Backend 不具备 `input-provenance` 时，界面明确标记为“观察用途”，不宣称能够证明无人介入；
- 人工介入不会直接接受结果，之后仍必须重新生成 Git Snapshot 并执行独立 Verification；
- `Ctrl-C`、Grace Period、强制终止与 Session Resume 均由 TerminalSession Handle 实现并留存事件；关闭可视 workspace 不得被误判为 Worker 成功。

## cmux 集成

cmux Backend 使用真实 terminal surface，而不是 `tail -F`：

1. 创建带 Run/Attempt 标识的 workspace；
2. 在受管 Worktree 中启动原生 OpenCode/Pi/Qwen TUI；
3. 等待可验证的 TUI Ready 状态后发送冻结 Prompt；
4. 通过 cmux hooks/Feed 获取 Session 与生命周期事件；
5. 将 workspace/surface/session 标识写入 Attempt 观察记录；
6. 完成后保留 workspace 供操作者审阅，Cleanup 只在显式策略下关闭。

进程控制不能只记录 Agent 根 PGID。真实 OpenCode 工具执行已经证明，Agent 会为 `go test` 等子进程创建新的 PGID；只暂停根组会让工具继续运行。cmux Backend 因此先让 Worker 脱离可能由 root-owned login 进程领组的终端 PGID，再冻结根组、枚举完整后代进程树并控制所有已发现 PGID。Pause 保存该 PGID 集供 Resume 使用；Terminate 对同一集合执行 STOP → TERM → CONT → 有界 KILL。无法完成进程树枚举或精确发信号时必须报错，不得宣称成功暂停。

安装全局 hooks 会修改用户的 Agent 配置，因此 Marshal只负责 Probe 与给出精确安装命令，不静默安装。

## 影响

- 本地桌面用户可以看到原生 Agent PTY/TUI，并从 Codex Desktop 或手机端切换 cmux workspace 监督；
- CI 和无 cmux 环境继续使用确定性的 captured 模式；
- PTY 模式对终端可用性产生依赖，但不降低最终 Verification/Review 门禁；
- ADR 0008 的“cmux 只运行只读日志跟随器”不再是唯一 cmux 模式，其 Observer 安全边界仍有效；
- M6 必须增加 TerminalSession Conformance 与真实 PTY E2E，不能用 stdout follower 测试替代。

## 未采用方案

- **继续把 batch stdout 放进 cmux**：无法呈现真实 Agent 状态，已由原型证伪；
- **把屏幕文本解析成完成协议**：易受截断、布局和人工输入影响；
- **所有环境强制 PTY**：CI、Windows 或未安装终端集成时不可用；
- **静默安装全局 Agent hooks**：会越过用户配置边界。
