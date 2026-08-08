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

Worker Adapter 配置变量只接受绝对路径，注册本身不搜索 `PATH`、不回退近似名称；建议固化到 shell 配置。新环境不确定绝对路径时，先用 `marshal doctor --json` 的 discovery 段或 `marshal doctor --print-env` 获取建议式发现并粘贴注册（discovery 只建议、不自动注册），详见开发指南[部署到新环境](development.md#部署到新环境)：

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

- **pi**：`message_update` 事件携带累积全量消息，转录本近似二次方增长；大任务建议 `maxOutputBytes >= 16000000`，否则会很快触发 `pi output limit exceeded`。另：pi 有时会在 WorkerResult 里写空 `session.id`（ephemeral 会话下 Worker 无从得知）导致 schema 拒绝；在 TaskSpec context 中内嵌 WorkerResult 逐字模板（字段清单 + 占位说明）可规避；
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

- **同机单编排原则**：同一台机器同一时间原则上只由一组 Marshal 编排驱动 worker；确需并存时错峰运行且 worker 总并发 ≤ 3（资源争抢已实测）；不同编排会话的进程管理动作可能误杀对方 worker（表现为多个 Run 同时 `worker.failed`/`context canceled`）；
- **疑似被外部 kill 的排查路径**：`doctor --run` 确认状态一致 → `events.jsonl` 失败时间戳 → 比对同时段其他会话/系统动作 → 对账后幂等重跑；不要直接归因为任务本身失败；
- **事件驱动监控**：用 `tail -f .marshal/runs/RUN_ID/events.jsonl`（或 ≤ 2 分钟短周期）监听 `worker.completed` 并立即触发 verify；禁止 8–15 分钟粒度的长 sleep 轮询——实测每衔接点空转半个轮询周期，多 Run 累计可达 30–50 分钟。

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
3. **执行阶段**：N 个 Run 并行（各自独立 worktree/分支/lease）；本机并发上限 2–3 个 worker（资源争抢已实测），错峰启动避开 worktree 创建的仓库级短锁；
4. **拆分规则**：单 Run 预期超过 30 分钟才考虑拆；小任务拆分的 Run 开销与汇总成本不划算；
5. **集成阶段**：集成任务合并各子分支后**必须全量重新 verify**——文件互斥 ≠ 语义兼容，各子分支的证据不能证明集成结果；集成过审后才可发布；
6. **串行尾巴不可消除**：关键路径下限 = max(各 worker 时间) + 集成 + 全量 verify + 人工审查；并行只压缩 worker 执行段。

### 10.7 成本、适用边界与度量

- 并行 worker 只对可证明互斥的工作域有效；耦合开发的集成成本会吃掉并行收益；优先级：跨仓库并行（仓库边界免费互斥）> 仓库内 scope 互斥拆分（§10.6）> 无互斥证明的拆解（禁止）；
- 人的审批/注意力是硬瓶颈，fan-out 度不得超过 Lead 能有效审查的上限；
- 小任务禁用 fan-out：协议成本无法摊薄；
- **度量纪律**：每次 fan-out 必须记录：任务族 ID、agent 数、墙钟时间（并行 vs 串行估计）、token/工具成本、冲突数、findings 数与处置分布、人工分钟数。没有度量记录，不得扩大 fan-out 使用面。
