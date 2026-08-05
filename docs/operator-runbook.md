# Operator Runbook

本文档面向操作者与 Lead Agent（pi、Codex Desktop/CLI 或其他编码 Agent），描述当前 Local MVP 已实现的安全操作路径，也明确尚未开放的功能。首次在真实任务中使用 Marshal 前，先读[第 9 节：日常使用最佳实践](#9-日常使用最佳实践)。

## 1. 选择运行方式

- 日常任务使用默认 `captured-process`。它具有确定性的 stdout/stderr、退出码、WorkerResult 和自动 deadline，适用于无人值守阶段与 CI。
- 需要观察真实 Agent TUI 或中途纠偏时，使用 `interactive-pty` 受监督 Pilot。当前仅提供 Go API `terminal.StartPrepared` 与 cmux Backend，尚未接入公开 `marshal task run` CLI。
- PTY Backend 不可用时安全降级到新的 captured Attempt；已经启动的 Attempt 不得在两种 transport 之间切换。
- 无论 transport 如何，Worker 都不能 push、发布、merge，也不能豁免 Verification、Review 或 Approval。

Codex Desktop 与 Codex CLI 都可以作为 Lead。手机端 Remote 只是在同一台开发机上监督 Codex Desktop 任务，不会把 Worker 移到手机执行。

## 2. 标准任务循环

在目标 Git 仓库执行：

```bash
marshal init
marshal doctor
marshal task plan --task TASK.json --policy POLICY.json --run RUN_ID
marshal task approve --run RUN_ID --gate plan --actor USER_ID
marshal task run --run RUN_ID
marshal task verify --run RUN_ID
marshal task review --run RUN_ID --decision REVIEW.json
marshal task approve --run RUN_ID --gate publish --actor USER_ID
marshal task publish --run RUN_ID
marshal task accept --run RUN_ID
```

每一步都应先检查退出码；自动化调用建议同时使用 `--json`。`review` 要审查真实 diff 与独立验证证据，不能只复述 Worker 的总结。`publish` 只创建或更新 Draft PR；Marshal不自动 merge。

## 3. 观察与介入决策

操作者发现方向可能错误时，按影响从小到大选择：

1. **只观察**：读取 screen 或状态，不向 Worker 输入；不改变证据来源。
2. **Lead Steering**：通过 Marshal Control Plane 发送不改变冻结 Scope/Acceptance/Policy 的澄清，生成 `InterventionRecord`。
3. **Interrupt Step**：终止当前模型/工具步骤，不等于暂停整个进程树。
4. **Pause/Resume**：冻结或恢复 Agent 及已发现的所有后代进程组；无法精确枚举时必须失败。
5. **Terminate**：停止当前 Attempt，保留现场证据，再决定 rework、abort 或新 Run。
6. **直接在 PTY 输入**：只用于紧急人工接管；Attempt 标记 mixed provenance，之后必须重新生成 Git Snapshot、Verification 与 Review。

改变 Scope、Acceptance、Budget、Policy、Capability 或 base SHA 时，不发送 Steering，必须终止当前 Attempt 并创建新 Run。

## 4. “本轮结束”与“代码通过”

当前三种原生 TUI Adapter 的 `CompletionGate` 都是 `supervised-confirmation`。操作者确认只表示“不再向本轮 TUI 等待更多输出”，不代表：

- WorkerResult 可信；
- 代码正确；
- 验证通过；
- Review 接受；
- 可以发布或 merge。

结束 PTY 后仍需 identity 匹配的 WorkerResult、重新观察 Git Snapshot、独立 Verification、Lead Review 和相应 Approval。屏幕上的“完成”、终端空闲或文件静默期都不能替代这些门禁。

## 5. cmux 准备与诊断

本机优先使用：

```bash
/Applications/cmux.app/Contents/Resources/bin/cmux ping
/Applications/cmux.app/Contents/Resources/bin/cmux capabilities --json
/Applications/cmux.app/Contents/Resources/bin/cmux workspace list --json
```

不要在日志、TaskSpec 或命令历史中打印 `CMUX_SOCKET_PASSWORD`。Marshal不会修改 cmux 全局设置，也不会自行保存 socket password。

状态判定：

| Diagnostic | 含义 | 处置 |
| --- | --- | --- |
| `binary-replaced` | Probe 后 cmux executable identity 变化 | 停止使用，重新选择并 Probe 精确路径 |
| `probe-failed` | socket、授权或 capability RPC 不可用 | 检查 cmux 是否运行及 socket password 配置 |
| `missing-required-method` | 当前 cmux 缺少必要 RPC | 使用 captured；不要猜测兼容 |
| `workspace-rpc-unavailable` | capability 可用，但只读 workspace RPC 在 3 秒内无响应 | 使用 captured；保存诊断；由用户自行决定是否重启 cmux |
| `trusted launcher did not consume envelope` | workspace 已创建但 launcher 未消费一次性信封 | 终止 Pilot，检查 workspace/terminal 启动链，不复用信封 |

Marshal不得自动重启 cmux、关闭用户已有 workspace 或杀死无法证明归属的 login process group。若用户选择重启 cmux，应先保存正在使用的终端工作，再重新运行 Probe；不要把“重启后可用”写成自动恢复假设。

安全的 helper Live E2E（会创建并清理一个可见测试 workspace，不调用模型）：

```bash
MARSHAL_LIVE_CMUX=1 \
MARSHAL_CMUX_PATH=/Applications/cmux.app/Contents/Resources/bin/cmux \
go test ./internal/terminal -run '^TestLiveCMUXTerminalSession$' -v -count=1
```

只有 helper E2E 全部通过后，才允许尝试真实 Agent TUI Pilot。

## 6. 中断恢复与清理

```bash
marshal doctor --run RUN_ID --json
marshal task status --run RUN_ID --json
marshal task cleanup --run RUN_ID
```

`doctor` 默认只读。仅当权威 Journal/Record 能唯一证明修复结果时使用 `--repair`；不确定状态进入 quarantine/`BLOCKED`。`cleanup` 默认只预览，活动 lease、非终态 Run、dirty worktree 或路径身份异常都会阻止删除。不要用手工递归删除代替 Cleanup Guard。

## 7. 发布和远端处置

- Publisher 凭据只在 publish 阶段注入，不能进入 Worker/TUI 环境。
- Push 后等待远端 CI 明确成功；missing、skipping 或超时不是成功。
- Draft PR 由人工决定后续处置。当前 Marshal不执行 Ready for Review、merge、release、deploy、删除远端分支。
- 远端与本地 head/PR identity 不一致时停止发布并运行 `doctor --run`，不得覆盖猜测。

## 8. 当前已知本机状态

2026-08-05 上午本机 cmux `workspace list --json` 曾无响应，安全 helper Pilot 以 `workspace-rpc-unavailable` 在约 3 秒内 fail-closed；同日 cmux 重启后该 RPC 恢复。恢复后的实测证据：

- `TestLiveCMUXTerminalSession` helper E2E 通过：进程组创建、Pause/Resume、Terminate 全部验证；
- 真实受监督 Pilot 通过：Qwen Code `0.21.5` TUI 经 `terminal.StartPrepared`、密封 `LaunchEnvelope` 与 digest-bound 映射在 cmux workspace 启动，完成屏幕观察、任务产物精确校验、Pause/Resume、InterruptStep 与 Terminate，workspace 干净关闭；
- 该 Pilot 的提示词提交动作为 manual-pty 介入（原因见下），Attempt 按混合 provenance 处置；这不影响 Pilot 证据本身，但任何据此产生的代码改动仍必须重新经过独立 Verification 与 Review。

已知原生 TUI 问题（受监督模式限制）：Qwen TUI 在 `send` 多行文本后立即接收 Enter 时，Enter 会被粘贴处理吞掉；操作者必须等待粘贴 settle（本机实测约 10 秒）后再单独发送 Enter。OpenCode 与 Pi 的原生 TUI Pilot 尚未执行，不得假设行为相同。

默认 captured 模式不受上述问题影响。Marshal 仍不会自动重启 cmux、不关闭非本次创建的 workspace、不杀死无法证明归属的 login process group。

## 9. 日常使用最佳实践

2026-08-05 真实 M6 验收与 Full MVP E2E 沉淀的实操经验。首次在新仓库使用 Marshal 时建议通读。

### 9.1 一次性环境准备

Worker Adapter 配置变量只接受绝对路径，不搜索 `PATH`、不回退近似名称；建议固化到 shell 配置：

```bash
export MARSHAL_OPENCODE_PATH=<opencode 绝对路径>
export MARSHAL_QWEN_PATH=<qwen 绝对路径>
export MARSHAL_PI_PATH=<pi 绝对路径>
# 仅发布需要：gh 绝对路径与独立凭据目录，不复用 ambient ~/.config/gh
export MARSHAL_GH_PATH=<gh 绝对路径>
export MARSHAL_GH_CONFIG_DIR=<含 hosts.yml 的独立目录，目录 0700、文件 0600>
```

配置后用 `marshal doctor --json` 确认对应 Adapter `outcome=registered`、`compatibility=supported`；未知版本会被拒绝。目标仓库先执行 `marshal init`，运行状态写入被 Git 排除的 `.marshal/`。

### 9.2 Skill 分发

`.agents/skills/marshal/` 目录是 Lead Agent 的 Skill（SKILL.md 与 Codex 界面元数据）。包含该目录的仓库中 pi/Codex 会自动发现；在其他仓库使用 Marshal 时，把该目录复制到目标仓库，或让 Lead Agent 先读取 SKILL.md 的绝对路径再操作。Skill 只描述工作流与强制边界，不替代本文档。

### 9.3 长命令脱离运行

`task run`、`task verify`、`task publish` 可能耗时数分钟至数十分钟。在脚本或 Agent 会话中运行时使用脱离形式，避免进程被终端或会话清理回收：

```bash
nohup marshal task run --run RUN_ID --json > run.log 2>&1 < /dev/null & disown
```

进程被意外回收是安全的：Journal 与 Snapshot 支持幂等续跑。先用 `marshal doctor --run RUN_ID --json` 确认状态一致，再重新执行同一命令即可。

### 9.4 TaskSpec 编写

- `work.context` 必须自包含：Worker 看不到对话历史，关键内容、精确路径与边界约束都写入 context；逐字复制类任务写明“逐字写入，不得增删改写”；
- acceptance 命令按任务裁剪：全量 `make check` 在本仓库约 15 分钟，小任务挂一到两个精准命令即可；
- 写明路径纪律：captured Worker 对业务 worktree 与 control 目录之外的路径有强制 permission 限制，constraints 中应固定“若某个操作被 permission 拒绝，不得重试该路径，改用允许路径内的等价输入”，否则模型的探索行为会让 Attempt 以 permission denied fail-closed；
- TaskSpec 是进入 Review Packet 与远端 PR 的冻结产物，不得写入凭据、token 或敏感本机路径。

### 9.5 Adapter 预算建议

- **pi**：`message_update` 事件携带累积全量消息，转录本近似二次方增长；大任务建议 `maxOutputBytes >= 16000000`，否则会很快触发 `pi output limit exceeded`；
- **opencode**：较大 TaskSpec context 会被写入 `$TMPDIR/opencode/work-context.txt` 并引导模型读取；该路径被外部路径策略正确拒绝，不影响任务完成（模型可改读 control/input/task-spec.json），不要把这类拒绝当作故障；
- **qwen**：captured 模式无特殊预算要求；原生 TUI 受监督模式注意第 8 节的 Enter 粘贴竞态。

### 9.6 问题上报三件套

遇到问题时先采集三项只读信息：

```bash
marshal task status --run RUN_ID --json          # 当前状态与身份
marshal doctor --run RUN_ID --json               # 只读对账诊断
tail -5 .marshal/runs/RUN_ID/events.jsonl        # 最近的生命周期事件
```

Attempt 级 Worker transcript、stderr、VerificationReport、ReviewPacket 与 PublicationRecord 均已自动存档在对应 run 目录，无需手工复现现场。上报时附一句“我在做什么、期望什么、实际发生什么”。
