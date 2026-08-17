# Operator Runbook

> **Issue #25 已修复：正式发布与合入顺序见 §7.1 / §7.2（旧临时护栏已废止）**
>
> [Issue #25](https://github.com/chiga0/marshal-harness/issues/25) 与 [PR #24](https://github.com/chiga0/marshal-harness/pull/24) 暴露的「全部 required checks 成功且 PR 已合并后，`marshal task accept` 把 Run 永久置为 `BLOCKED`」已由 [ADR 0026](adr/0026-scm-merge-receipt-and-publication-reconcile.md) 实现修复并合入。推荐顺序不变：
>
> 1. 运行 `marshal task publish`，只创建或更新 Draft PR，不触发合入；
> 2. 等待冻结 TaskSpec 的 required checks 全部明确成功（`missing`、`skipping` 或超时一律不视为成功）；
> 3. 运行 `marshal task accept`，确认 Run 进入 `ACCEPTED` 且 Outcome 存在；PR 已被维护者先行合并且 merged head checks 全绿时，accept 会内联识别并同样进入 `ACCEPTED`；
> 4. accept 成功后，由维护者在 Marshal 之外按仓库策略 merge，并可按仓库策略 delete head（删除 head branch）。
>
> Marshal 本身没有 merge 权限（merge-never）。发布后因先 merge 后 accept 误入 terminal `BLOCKED` 的 Run，在 PR 已合并且 merged head 的 required checks 全绿时，用正式补偿命令 `marshal task reconcile` 迁移到 `ACCEPTED`（§7.2）；不满足条件时命令会 fail closed。禁止手改状态、伪造证据或绕过 required checks 与 ReviewDecision；正式顺序的详细步骤与边界见 §7.1。

本文档面向操作者与 Lead Agent（pi、Codex Desktop/CLI 或其他编码 Agent），只描述当前 embedded/local 发行版已经实现的安全操作路径，并明确尚未开放的功能。它是当前版本的操作说明，不定义 Marshal 的终态产品边界；整体方向见[整体架构](architecture.md)。首次在真实任务中使用 Marshal 前，建议先读下文第 9 节“日常使用最佳实践”。

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

**状态卫生规范**（防止 `.marshal/` 与业务仓库膨胀）：

- Lead 编排产物（TaskSpec 输入、驱动日志、Decision 草稿）一律放 `.marshal/` 下（如 `.marshal/tasks/`、`.marshal/logs/`），不得提交进业务仓库；注意 `.gitignore` 对已跟踪文件无效，误提交后必须 `git rm --cached` 解除跟踪；
- 证据保留：终态 Run 的 Journal/Outcome/Record 按 Policy 的 `retentionDays`（默认 30 天）保留，期间不得删除；`retentionDays` 当前为声明式、代码未强制执行，执行落地前以本规范人工遵守；
- 死 Run（RETRY_PENDING/被放弃的 BLOCKED）先经 `task abort`（ADR 0012，已实现）转终态，再 `cleanup` 回收 worktree 与临时缓存；证据按上一条保留；
- 旧版 TaskSpec/草稿等活性已失的文件，定期归档到 `.marshal/archive/` 或删除，不留在工作面上。

## 7. 发布和远端处置

- Publisher 凭据只在 publish 阶段注入，不能进入 Worker/TUI 环境。
- Push 后等待远端 CI 明确成功；missing、skipping 或超时不是成功。
- Draft PR 由人工决定后续处置。当前 Marshal不执行 Ready for Review、merge、release、deploy、删除远端分支。
- 远端与本地 head/PR identity 不一致时停止发布并运行 `doctor --run`，不得覆盖猜测。

### 7.1 正式发布与合入顺序（ADR 0026 实现合入后适用）

公开 [Issue #25](https://github.com/chiga0/marshal-harness/issues/25) 与 [PR #24](https://github.com/chiga0/marshal-harness/pull/24) 暴露的「全部 required checks 成功且 PR 已合并后，`marshal task accept` 把 Run 永久置为 `BLOCKED`」已由 [ADR 0026](adr/0026-scm-merge-receipt-and-publication-reconcile.md) 的实现修复：活路径 accept 内联识别已合并的绿色 PR，补偿路径 `marshal task reconcile` 把发布后的 `BLOCKED` 安全迁移到 `ACCEPTED`。推荐顺序仍为：

1. **publish**：运行 `marshal task publish --run RUN_ID`，只创建或更新 Draft PR，不触发合入；
2. **等待 required checks 全绿**：冻结 TaskSpec.requiredChecks 中声明的全部检查必须明确成功；`missing`、`skipping` 或超时一律不视为成功；
3. **accept**：运行 `marshal task accept --run RUN_ID`。PR 尚为 OPEN 时按原 checks 观察路径进入 `ACCEPTED`；若维护者已先行 merge 且 merged head 的 required checks 全绿，accept 会内联采集不可变 `SCMMergeReceipt` 并经同一 checks-passed 路径进入 `ACCEPTED`（不写 PublicationReconcileRecord）；
4. **维护者在 Marshal 外 merge**：accept 成功后，由维护者在 Marshal 之外按仓库策略合入 PR；merge 完成后可按仓库策略 delete head（删除 head branch）。head branch 删除不是权威 head SHA 丢失：GitHub PR 节点在 merge 后仍保留原 head OID、base OID 与 merge commit。

关键边界不变：Marshal 不获得 merge 权限（merge-never），`publish` 与 reconcile 都只观察不操作 PR；merge 与删除 head branch 由维护者在 Marshal 外完成。

### 7.2 已合并后误入 BLOCKED 的 Run：task reconcile 补偿命令

若 Run 在发布后因先 merge 后 accept（或其他发布安全门）进入 terminal `BLOCKED`，在 PR 已被合并且 merged head 的 required checks 全绿时，用正式补偿命令迁移：

```sh
marshal task reconcile --run RUN_ID --actor OPERATOR_ID [--json]
```

命令语义（ADR 0026 冻结顺序）：

- 仅当 Run 恰为发布后的 `BLOCKED` 时执行；其他状态拒绝，重复调用幂等归并；
- current-ledger recheck 复核冻结证据：ReviewDecision accept 且无 blocking findings、冻结 PublicationRecord digest 一致——不绕过 required checks 与 ReviewDecision；
- 采集不可变 `SCMMergeReceipt`（head/base OID 与冻结 PublicationRecord 对账，不符 fail closed）并复验 merged head 的 required checks 全绿；
- 写 append-only `PublicationReconcileRecord`，追加 `publication.reconciled` 事件（actor `system/marshal-reconciliation`），旧 BLOCKED Outcome 只归档不删除，写入新的 ACCEPTED Outcome；
- 任一前置缺失、OID 对账不符、digest 重算不符或幂等身份冲突，一律 fail closed，Run 保持 `BLOCKED`。

**仍然禁止的操作：**

- 不得运行 `marshal task cleanup` 删除证据；
- 不得手工修改 `.marshal/runs`、`events.jsonl`、`state.json`、`outcome.json` 等任何状态/事件/结果文件；
- 不得用 `marshal doctor --repair` 改变业务终态——`--repair` 只修复权威 Journal 能唯一证明的 snapshot 损坏，**不能把 `BLOCKED` 改成 `ACCEPTED`**；
- 不得通过新建同 branch、伪造 OPEN PR 或覆盖远端 head 制造新副作用来“修复”；
- PR 未合并的 `BLOCKED` 不适用 reconcile（命令会拒绝）：先解决阻塞原因或在维护者 merge 后重试；无冻结 PublicationRecord 的发布前 `BLOCKED`（如 PUBLISHING 阶段失败）也不适用，只能创建新 Run。

## 8. 已知限制

- `interactive-pty` 仍是受监督 Pilot，不属于默认公开 CLI 路径；日常和无人值守任务使用 `captured-process`。
- Qwen TUI 在 `send` 多行文本后立即接收 Enter 时，Enter 可能被粘贴处理吞掉；需要等待粘贴 settle 后单独发送 Enter。OpenCode 与 Pi 尚无等价 Pilot 证据，不得推断行为相同。
- cmux RPC 不可用或超时时必须 fail closed 并改用新的 captured Attempt；Marshal 不自动重启 cmux、不关闭用户已有 workspace、不杀死无法证明归属的进程组。
- TUI 屏幕、终端空闲或人工确认不能替代 WorkerResult、Git Snapshot、Verification 和 Review。

具体 Pilot 证据保存在[历史档案](archive.md)的 Milestone 报告与独立 Review 中，不在操作手册维护机器时间线。

## 9. 日常使用最佳实践

2026-08-05 真实 M6 验收与 Full MVP E2E 沉淀的实操经验。首次在新仓库使用 Marshal 时建议通读。

### 9.1 一次性环境准备

Worker Adapter 配置变量只接受绝对路径，注册本身不搜索 `PATH`、不回退近似名称；建议固化到 shell 配置。新环境不确定绝对路径时，先用 `marshal doctor --json` 的 discovery 段或 `marshal doctor --print-env` 获取建议式发现并粘贴注册（discovery 只建议、不自动注册）；补充说明见[开发指南](development.md)的“部署到新环境”：

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

`.agents/skills/marshal/` 目录是 Lead Agent 的 Skill（SKILL.md 与 Codex 界面元数据）。包含该目录的仓库中 pi/Codex 会自动发现；在其他仓库使用时任选其一：把目录复制到目标仓库；把该目录软链接到 `~/.agents/skills/marshal`（pi 全局发现位置，单一来源随仓库演进，已验证 pi 扫描器跟随符号链接并按 realpath 去重）；或让 Lead Agent 先读取 SKILL.md 的绝对路径再操作。Skill 只描述工作流与强制边界，不替代本文档。

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

- **pi**（Pi Adapter `0.2.0` / Pi `0.84.1`）：0.84.1 的 `message_update` 事件仍携带 `assistantMessageEvent`，但其 `text_delta` 只含 `contentIndex` 与线性增量 `delta`（不含消息标识字段），顶层不再有累积 `message` 快照，也没有 `partial`，转录本随增量近似线性增长；历史版本 0.83.0 的累积全量消息与近似二次方增长仅作历史说明，当前不受 Marshal 支持。Marshal 实施的 wall-time 与 output-bytes 上限仍然生效，大任务仍建议 `maxOutputBytes >= 16000000`，否则可能触发 `pi output limit exceeded`。另：pi 有时会在 WorkerResult 里写空 `session.id`（ephemeral 会话下 Worker 无从得知）导致 schema 拒绝；在 TaskSpec context 中内嵌 WorkerResult 逐字模板（字段清单 + 占位说明）可规避；
- **opencode**：较大 TaskSpec context 会被写入 `$TMPDIR/opencode/work-context.txt` 并引导模型读取；该路径被外部路径策略正确拒绝，不影响任务完成（模型可改读 control/input/task-spec.json），不要把这类拒绝当作故障。**绝对路径零容忍**：opencode 对任何绝对路径的工具调用都会拒绝——即使路径在 worktree 内部；而 Worker 会模仿 Marshal prompt 中出现的绝对路径。因此读密集/调研类任务的 constraints 必须强制“所有读写操作一律相对路径，提示词中的绝对路径仅供理解”；读符号链接外部源码用 `sources/<repo>/...` 相对路径 + bash 简单命令；
- **qwen**：captured 模式无特殊预算要求；原生 TUI 受监督模式注意第 8 节的 Enter 粘贴竞态。

### 9.6 问题上报三件套

遇到问题时先采集三项只读信息：

```bash
marshal task status --run RUN_ID --json          # 当前状态与身份
marshal doctor --run RUN_ID --json               # 只读对账诊断
tail -5 .marshal/runs/RUN_ID/events.jsonl        # 最近的生命周期事件
```

Attempt 级 Worker transcript、stderr、VerificationReport、ReviewPacket 与 PublicationRecord 均已自动存档在对应 run 目录，无需手工复现现场。上报时附一句“我在做什么、期望什么、实际发生什么”。

### 9.7 多编排共存与监控纪律

- **capacity-based 并发（不设 magic number）**：同机允许多组 Marshal 编排并存，取消"原则上单编排"假设与任何固定数字的并发上限。当前 Marshal main 尚无并发/公平性 Policy 字段、Provider capacity contract、自动 admission queue 与 backpressure 控制器，因此并发仅由 Lead 以人工 admission 决定，决策输入限于可实际取得者：写域互斥（独立 worktree 且互斥的 `scope.allowPaths`，见 §10.6）、可实际取得的宿主 CPU/内存/I/O 余量（load average、可用内存、磁盘 I/O）、Provider/API 明示的限制与 rate limit、实际观察到的背压（backpressure：超时率上升、事件延迟增长、整波变慢；资源争抢已实测会把分钟级任务放大到整波超时 fail-closed）。可取得的信号缺失时不得虚构，一律默认减少新派发或排队；资源紧张时排队新派发或降低并发（降载），容量恢复后再回升；用户显式给定的并发策略优先级最高，覆盖 Lead 推断。自动 queue/backpressure/升降载与可配置 Policy 字段是 M12 尚未实现的路线项，不得写成当前已可配置/已可自动化的行为；并发决策依据与降载动作按 §10.7 度量纪律人工记录，保证事后可回溯；
- **进程所有权隔离**：每个编排只允许对归属自己的进程树执行 pause/resume/terminate；不同编排会话的进程管理动作可能误杀对方 worker（表现为多个 Run 同时 `worker.failed`/`context canceled`），禁止对其他编排或无法证明归属的进程做 blanket kill；
- **疑似被外部 kill 的排查路径**：`doctor --run` 确认状态一致 → `events.jsonl` 失败时间戳 → 比对同时段其他会话/系统动作 → 对账后幂等重跑；不要直接归因为任务本身失败；
- **事件驱动监控**：用 `tail -f .marshal/runs/RUN_ID/events.jsonl`（或 ≤ 2 分钟短周期）监听 `worker.completed` 并立即触发 verify；禁止 8–15 分钟粒度的长 sleep 轮询——实测每衔接点空转半个轮询周期，多 Run 累计可达 30–50 分钟。
- **心跳行动队列**：每个 heartbeat 先运行 `scripts/marshal-watch.sh --once --json` 并处理最高优先级项，反空转纪律、REVIEW_PENDING 时限与合并边界见 §11。

## 10. 多 Agent Fan-out 协作模式（v0.2）

Fan-out 的价值不在“更快写代码”，而在“更多视角做决策”与“把主 Agent 注意力花在更少、更好的决策点上”。业界（orchestrator-workers、角色 fan-out、debate/jury）已验证并行化模式，但均无 Marshal 的证据门禁；本节定义在 Marshal 信任模型内可用的 fan-out 形态与纪律。v0.2 依据 [fan-out 汇总决策](research/fanout-consolidation.md) 更新：补充评审团操作约定、findings 裁决纪律、跨仓库任务平面与度量要求。

### 10.1 任务分级（先决条件）

- **S 级**（琐碎改动）：不走 Marshal 或最简流程；
- **M 级**（常规开发）：标准流程 + 裁剪 acceptance；
- **L 级**（复杂/高风险/探索型）：标准流程 + 本节 fan-out 模式。

分级由 Lead 在起草 TaskSpec 时判定；fan-out 成本随 N 线性增长，只对 L 级或探索型问题使用。

### 10.2 调研队（Sectioning）

探索型问题拆为 N 个独立调研 Run：

- 每个调研 Run 为 `publication: none`，deliverable 是调研报告（documentation），共享同一份问题简报，各自带不同的评估视角（如：信任模型优先 / 效率优先 / 可操作性优先）；
- acceptance 裁剪为轻量命令（产物存在性与基本完整性即可），质量由 Lead review 判定；
- 各 Run 的产物路径必须互斥；报告写入各自 worktree，ACCEPTED 后由 Lead 直接读取；
- **调研任务模板**：优先从 Skill 目录 `templates/research-task.json` 填空生成 TaskSpec（内含相对路径纪律、bash 命令白名单、预算档位与精准 acceptance），不要从空白起草；
- **只读纪律**：当前尚无 read-only 执行画像（设计中），调研/评审 Worker 的 TaskSpec scope 必须收紧到仅允许报告/评审产物文件（`allowPaths` 单文件、`maxChangedFiles: 1`），并在 constraints 中禁止修改其他内容；
- **网络限制**：Marshal Worker 无网络权限（设计使然）。需要外部信息的调研在 harness 外的联网会话完成，或等待后续“调研权限画像”（read-only + 显式网络放行）设计落地。

### 10.3 评审团（Voting/Jury）

verify 通过后、写 ReviewDecision 前：

1. 派发 2–4 个评审 Worker：优先不同 Adapter，输入同一份 ReviewPacket；视角建议从 correctness（TaskSpec 一致与边界条件）、security（输入边界、凭据、注入）、test adequacy（覆盖、遗漏路径、flaky 风险）、maintainability（接口、复杂度、回滚风险）中选择，每个 Worker 只持一个视角；
2. 评审产物为结构化 findings，每条包含：稳定 ID、视角角色、论断、严重级（P0–P3）、位置（文件/行）、证据引用、置信度、处置建议；去重按“产物 + 位置 + 证据身份”，不按自然语言相似度；
3. Lead 汇总：逐条处置（accepted / rejected_with_reason / duplicate / needs_new_verification / human_escalation），裁决冲突，抽查关键断言；
4. Lead 写最终 ReviewDecision，全部摘要绑定机制不变。

裁决规则：确定性门禁失败是硬否决（accept 不能绕过 Required Failed Gate）；P0/P1 finding 默认 blocking，Lead 若否决必须在 Decision 中写明 dissent 理由；不得以多数观点淹没少数但关键的 finding，裁决以可复现证据为准。

红线：评审 Worker 的意见是材料不是结论；决策责任与证据绑定始终在 Lead。评审 Worker 遵守 10.2 的只读纪律。

### 10.4 汇总纪律

**派发前勘查**：Lead 派发任何 fan-out 前必须先检查目标仓库已有产出（`reports/`、`git log`、`.marshal/runs` 状态），确认工作未被其他会话完成或进行中，避免重复劳动。

Lead 的汇总必须产出三份清单并可回溯：**共识**（多 Agent 独立得出的一致结论）、**分歧**（冲突点与 Lead 的裁决理由）、**采纳结论及其证据**（每条结论必须能指回具体 Run 的证据）。没有证据支撑的“综合意见”不得进入结论。findings 的逐条处置记录与 dissent 理由同样属于汇总产物，不得省略。

### 10.5 跨仓库任务平面（Multi-repo 并行）

适用于跨多个代码仓库的大型任务。仓库边界是天然的 ownership 契约，写集合按仓库隔离，这是当前可落地的第一种任务级并行形态，不需要 Core 改动（Marshal 状态本来就按仓库独立）：

1. **契约先行**：拆解前先冻结跨仓接口契约（API/Schema/版本约定），计算其摘要，写入每个子 TaskSpec 的 `work.context`；契约变更 = 重新拆解，子任务不得自行漂移契约；
2. **每仓一个 Run**：各仓库独立 `marshal init` 与状态目录，各自走完整生命周期与门禁；每个子 TaskSpec 的 context 记录任务族 ID、兄弟任务（仓库 + task-id + run-id）与契约摘要，保证可追溯；
3. **独立门禁不可省**：每个子 Run 各自 verify/review，不得因“只是其中一个仓库”而降级；
4. **集成阶段**：全部子 Run `ACCEPTED` 后，在指定仓库（或专门的集成仓库）创建集成任务，其 acceptance 运行跨仓集成验证（指向兄弟分支/本地路径）；各子分支的证据不能证明集成结果，集成必须重新全量验证；
5. **审批预算**：N 个子 Run = N 组审批；Lead 按操作者注意力上限控制并行度。

禁止：子 Worker 直接修改兄弟仓库；跳过集成验证直接合并兄弟分支。

### 10.6 仓库内任务平面（scope 互斥拆分，v1）

适用于同一大任务拆分为多个子任务并行。v1 不依赖依赖分析，以 `scope.allowPaths` 互斥作为 ownership 契约，把“希望不冲突”变成“证明不可能冲突”：

1. **拆分阶段（Lead）**：按内聚度拆，共享符号密集的文件必须划入同一子任务（外部证据：朴素按文件拆成本 +60%）；冻结接口契约（共享符号、签名、跨子任务假设）写入每个子 TaskSpec 的 `work.context`；契约变更 = 重新拆解；
2. **写域互斥**：每个子任务分配互斥的 `allowPaths` 写域，越界写入由 scope 门禁直接判失败——合并冲突在结构上不可能发生；若集成时仍出现文本冲突，先检查 scope 划分是否有遗漏；
3. **执行阶段**：N 个 Run 并行（各自独立 worktree/分支/lease）；写域互斥已由 `scope.allowPaths` 结构性保证，实际同时并行的 Run 数由 Lead 按 §9.7 的 capacity-based 纪律人工 admission——依据可实际取得的宿主 CPU/内存/I/O 余量、Provider/API 明示限制与观察到的背压动态增减，资源紧张或容量信号缺失时排队派发而不是硬撑；错峰启动避开 worktree 创建的仓库级短锁；
4. **拆分规则**：单 Run 预期超过 30 分钟才考虑拆；小任务拆分的 Run 开销与汇总成本不划算；
5. **集成阶段**：集成任务合并各子分支后**必须全量重新 verify**——文件互斥 ≠ 语义兼容，各子分支的证据不能证明集成结果；集成过审后才可发布；
6. **串行尾巴不可消除**：关键路径下限 = max(各 worker 时间) + 集成 + 全量 verify + 人工审查；并行只压缩 worker 执行段。

### 10.7 成本、适用边界与度量

- 并行 worker 只对可证明互斥的工作域有效；耦合开发的集成成本会吃掉并行收益；优先级：跨仓库并行（仓库边界免费互斥）> 仓库内 scope 互斥拆分（§10.6）> 无互斥证明的拆解（禁止）；
- 人的审批/注意力是硬瓶颈，fan-out 度不得超过 Lead 能有效审查的上限；
- 小任务禁用 fan-out：协议成本无法摊薄；
- **度量纪律**：每次 fan-out 必须记录：任务族 ID、agent 数、墙钟时间（并行 vs 串行估计）、token/工具成本、冲突数、findings 数与处置分布、人工分钟数。没有度量记录，不得扩大 fan-out 使用面；
- **并发 admission 记录**：每次并发决策（首次 fan-out、加派、排队、降载、回升）都必须随 fan-out 度量人工记录，至少包含：**策略来源**——用户显式并发策略（最高优先）还是 Lead 推断，还是因信号缺失而默认保守；**决策时并发数**与本次动作对象（taskId/runId/attemptId）；**容量样本与观察**——可实际取得的宿主 CPU（load average）/可用内存/磁盘 I/O 读数、Provider/API 明示的 rate limit 与容量限制观察、背压观察（超时率、事件延迟），取得不到的项如实标注"未取得"，不得虚构；**动作与时间**——排队/降载/回升动作及发生时间（RFC3339）；**编排与进程所有权标识**——归属编排会话及进程归属判据，确保降载/回升只触碰本编排进程。M12 的自动 admission queue 与 backpressure 控制器尚未实现，当前这些人工记录是并发决策可回溯的唯一手段；没有 admission 记录，不得扩大并发度。

## 11. 心跳 watchdog 与行动队列（防"挂了毫无感知"与"持续报告无交付动作"）

后台 `task run` 期间 Lead 与操作者都可能失去可见性（opencode 长 attempt 很少发事件，`events.jsonl`
长时间不增长是**正常**的，不能据此判死），必须用**进程存活**判定。2026-08-16 夜间 dogfood
进一步暴露吞吐事故：多个 Run 已 ACCEPTED 且 Draft PR CI 全绿，但主干未前移；失败或
REVIEW_PENDING 状态跨多个心跳无人采取下一动作。因此 watchdog 从"周期性拼接状态字符串"
升级为**一次性、机器可读的行动队列**：不仅报告，还给出优先级与下一动作。

### 11.1 两种调用模式

循环模式（向后兼容旧入口，长期后台兜底）：

```bash
nohup scripts/marshal-watch.sh 600 > /tmp/marshal-watch.log 2>&1 < /dev/null & disown
```

一次性模式（每个 heartbeat 的第一步；执行一次立即退出，不 sleep）：

```bash
MARSHAL_WATCH_NOTIFY=0 scripts/marshal-watch.sh --once --json
```

- `--once` 单次执行后立即退出；`--json` 输出行动队列；两者组合供 Lead 机器消费，处理 `items[0]`（最高优先级项）。
- 循环模式保留旧行为：每 interval（默认 600 秒）输出一行文本状态，tee 到 `$MARSHAL_WATCH_LOG` 并 osascript 通知。
- 确定性测试用 `bash scripts/marshal-watch_test.sh`，只使用临时 fixture，不触碰真实 `.marshal`。

### 11.2 JSON 行动队列契约

输出包含 `generatedAt` 与按 priority 升序的 `items`；每个 item 含
`runId`、`state`、`priority`、`action`、`ageSeconds`、`processOwnership`。
不写入 secret、环境变量值、完整命令行参数、绝对路径。

动作映射（priority 越小越优先）：

| state | 条件 | action | priority |
| --- | --- | --- | --- |
| REVIEW_PENDING | — | review-now | 10 |
| REWORK_REQUESTED | — | run-rework-now | 20 |
| RUNNING | 无可证明归属进程 | doctor-dead | 30 |
| RETRY_PENDING | — | retry-or-abort | 40 |
| VERIFYING | — | verify-or-doctor | 50 |
| PUBLISHING | — | publish-or-doctor | 60 |
| READY / APPROVED | — | run-now | 70 |
| CI_PENDING | — | check-ci | 80 |
| RUNNING | 有可证明归属活进程 | monitor | 90 |

终态（ACCEPTED/REJECTED/BLOCKED/ABORTED/NO_CHANGE）默认不进入行动队列。

`processOwnership` 只有三种取值：`owned-active`（有可证明归属的活进程）、`not-found`
（RUNNING 但找不到归属进程，需 doctor-dead）、`not-applicable`（非 RUNNING 状态）。
进程匹配覆盖 `marshal task run/verify/publish/supervise` 与 qwen/codex/qoder/opencode/pi，
且必须绑定**精确 runId**——同名进程不等于本 Run 所有者。不得以事件年龄单独判定 RUNNING
死亡（opencode 单 attempt 可能 20–40 分钟无事件）；无法证明归属时输出 doctor-dead，
由操作者用 `marshal doctor --run RUN_ID --json` 对账后幂等重跑。

### 11.3 环境变量

- `MARSHAL_WATCH_ROOT`：含 runs 目录的根，默认 `.marshal`；测试可指向 fixture 根目录。
- `MARSHAL_WATCH_NOTIFY=0`：禁用 osascript 通知（heartbeat 消费 JSON 时必设）。
- `MARSHAL_WATCH_LOG`：循环模式日志，默认 `/tmp/marshal-watch.log`。
- `MARSHAL_WATCH_PROCESS_FILE`：进程 fixture 文件（每行 `<pid> <command>`），设置后归属判定只按文件内容，不触碰真实进程。

### 11.4 每轮有限动作（反空转纪律）

- 每个 heartbeat 先运行 `scripts/marshal-watch.sh --once --json`，处理最高优先级项。
- 除所有 Run 均有真实活进程（`owned-active`）或明确外部阻塞外，每轮至少完成一个安全有限动作：review、导入 rework、恢复 driver、publish、检查 CI、准备互斥 successor。
- REVIEW_PENDING 必须在**一个 heartbeat 内**完成完整审查并导入 ReviewDecision，或给出明确 blocking finding；Required Gate 失败且 ReviewPacket 可用时，同轮进入 rework。
- 连续多个 heartbeat 只报告状态而不推进交付，按空转事故处理。

### 11.5 合并边界（ACCEPTED ≠ 主干交付）

- ACCEPTED、Draft PR、CI green 都不等于主干交付；Marshal 无 merge 权限（merge-never），merge 由维护者按仓库策略在 Marshal 之外完成（见 §7.1）。
- mergePolicy=never 或 Core 不支持 merge 时，必须在**首次发现**即报告为外部/治理阻塞，并按 §10.7 记入当轮记录，不得静默视为"已完成"。
- 不得连续派发依赖该 PR 的 stale-base successor：后续任务的 base 依赖未合并变更时，先等待合并或显式声明阻塞，避免 base 漂移造成连锁 rework。
- 不得把此类 PR 计为 milestone 完成；milestone 以实际进入主干的交付计数。
