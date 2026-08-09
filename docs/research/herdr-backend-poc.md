# herdr TerminalSession 后端 POC（实验分支，不进主干）

- 分支：`exp/herdr-terminal-backend`（独立 worktree，仅深度调研 + POC）
- 日期：2026-08-09
- 关联：[herdr 对照调研](herdr-comparison.md)、ADR 0008（可插拔 Observer）、ADR 0009（TerminalSession）、ADR 0011（密封 TUI 传输）

## 0. 结论

herdr 可以作为 Marshal 的第二个真实 TerminalSession 后端（`herdr-pty`），与 cmux 同构。
本 POC 证明：**在不改动 Marshal 信任模型的前提下，把 herdr 接入 TerminalSession Port 是可行的**——
Probe/Start/Send/ReadScreen/Interrupt/Pause/Resume/Terminate 全部可由 herdr CLI 承载，
密封 LaunchEnvelope、PID 握手、输入溯源、进程组控制均复用现有原语。

herdr 只提供"身体/神经"（终端、注意力、喊话通信），**不提供任务/证据/治理**；Marshal 仍是
状态与策略唯一权威。herdr 的 `blocked/working/idle` 信号仅作辅助（注意力），不替代
WorkerResult/Git Snapshot/Verification/Review（ADR 0011）。

## 1. 接口映射

| Marshal Port（internal/port/terminal.go） | herdr CLI（POC 命令面） |
| --- | --- |
| `Probe` | `herdr workspace list --json`（控制面可达）+ 二进制 realpath/SHA-256 钉扎 |
| `Start` | `herdr workspace create --name --description --cwd --command <sealed-launcher> --focus false` → 返回 `workspace:N` |
| 启动握手 | `read-screen` 轮询 `MARSHAL_LAUNCH_READY` → `send-key enter` → PID 文件握手 → 信封消费 |
| `Send` | `send --workspace` + `send-key enter`（记录 input-provenance） |
| `ReadScreen` | `read-screen --workspace --lines N` |
| `InterruptStep` | `send-key escape` |
| `Pause/Resume` | 进程组控制器（SIGSTOP/SIGCONT），与 cmux 一致 |
| `Terminate` | 进程组终止 + `herdr workspace close` |

命令面以 herdr 实际 CLI 为准；若 herdr 子命令名不同，仅需替换命令字符串，接口不变。

## 2. 信任边界（与 cmux 一致，未放宽）

- 不继承 herdr/terminal ambient environment；环境值不进可见 argv（密封信封一次性、owner-only）；
- `ExpectedExecutableDigest` 漂移即拒绝（Start 校验）；
- 屏幕文本不替代权威证据；无自动 CompletionGate 时仅受监督模式；
- Pause/Resume/Terminate 依赖进程组控制器，不支持则返回 ErrUnsupported（不假装成功）。

## 3. 相对 cmux 的增量价值（POC 验证目标）

- **ssh 远程 / 重启回魂**：herdr 的 fd 级 handoff 使受监督会话可跨重启/断网存活（cmux 无）；
- **wait-until-blocked**：herdr 的 hooks+manifest 状态可作 Lead 的事件源，消灭轮询空转；
- **注意力视图**：blocked/working/idle 侧栏辅助多 Agent 监督。

## 4. 未决与后续（不进主干的原因）

- herdr CLI 命令面与 socket schema 需以 herdr 实际版本校准（本 POC 以文档推断）；
- 需真实 herdr 环境的 Live E2E（`MARSHAL_HERDR_PATH`）验证 Start/Send/回魂；
- 若要把 herdr 状态纳入 CompletionGate 辅助信号，需 ADR 0009/0011 的补充（辅助信号边界）；
- 因此本分支仅作 POC 与接口验证；生产化需独立 ADR + Live E2E + 一致性测试后另开里程碑。

## 5. 测试

- `TestHerdrProbeUnavailableWithoutBinary`：无 herdr 时 fail-closed、相对路径拒绝；
- `TestHerdrProbeLive`（opt-in，`MARSHAL_HERDR_PATH`）：真实控制面 Probe。
