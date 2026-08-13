# ADR 0029：无 Attempt Run 的显式 abort 出口（pre-attempt abort）

- 状态：提案中（Proposed；本 ADR 经维护者 ApprovalRecord 接受后生效，接受人与接受时间另行记录，不改写本文）
- 日期：2026-08-13
- 决策来源：公开 [Issue #39](https://github.com/chiga0/marshal-harness/issues/39)（`task abort` 无法终结 Worker 启动前失败的 READY Run）；当日运维中另观察到 PUBLISHING 卡死与 CI_PENDING 陈旧 Run 长期悬置无法清理——后者仅作为状态卫生问题的证据引用，其处置出口见「明确排除」节，不由本 ADR 提供
- 关联：[ADR 0007](0007-intent-first-publication.md)、[ADR 0010](0010-controlled-autonomy-and-intervention.md)、[ADR 0012](0012-explicit-abort.md)、[ADR 0019](0019-deterministic-control-plane-typed-execution-and-goal-admission.md)、[ADR 0026](0026-scm-merge-receipt-and-publication-reconcile.md)、[ADR 0028](0028-ci-deadline-phased-observation.md)、[任务生命周期](../task-lifecycle.md)

## 背景

[ADR 0012](0012-explicit-abort.md) 为被放弃的 RETRY_PENDING Run 建立了受控的、留证据的 abort 出口，但其源状态集合被刻意冻结为 RETRY_PENDING 单一状态。实践中存在第二类死 Run，ADR 0012 的出口对它完全不可用：

1. **Worker 启动前失败的 READY Run（Issue #39）**：Run 已通过 `PLANNED → READY` 冻结全部执行输入，但在 Attempt 记录产生之前失败——adapter 拉起失败、dispatch 错误、Writer Lease 获取失败等。状态停在 `READY`，从未产生任何 Attempt 记录：没有运行中子进程、没有进行中的 Verification/Review/Publish 事务、没有任何证据生产；
2. **被放弃的 PLANNED Run**：TaskSpec 草案已存在但维护者决定不再推进，Run 停在 `PLANNED`，同样无任何执行痕迹。

这类 Run 的处境与 ADR 0012 决策来源中的死 Run 完全相同：非终态 → cleanup 拒绝清理；无适用 abort 源状态 → `marshal task abort` 以固定错误拒绝；手工删除 worktree/状态目录违反 Cleanup Guard 纪律并绕过证据留存。区别只在于它们**更早死亡**——连一次 Attempt 都没有发生过。

同一时期的运维观察还包括 PUBLISHING 卡死与 CI_PENDING 陈旧 Run。它们同样悬置，但已进入发布阶段或已产生远端工件（PublicationRecord、已发布分支、远端检查集合），其退出必须处理远端事实对账，语义与「从未开始」完全不同，属于 typed reconciliation 与人工护栏的范畴（见「明确排除」节），本 ADR 明确不提供它们的出口。

缺的不是安全机制——无 Attempt Run 的终止面是生命周期中最小的：没有子进程要停止、没有证据要冲刷、没有远端事务要对账。缺的只是一个合法出口。

## 决策

核心方向：复用 `marshal task abort` 命令入口与 ADR 0012 的证据纪律，为「从未开始执行」的 Run 开放第二个 abort 出口，并以此出口首次启用生命周期状态表中早已预留的 `ABORTED` 终态。契约分五部分冻结：源状态集合与判定条件、终态与 Outcome 证据要求、明确排除、与 ADR 0012 的关系、CLI 语义。

### 1. 允许 abort 的源状态集合与判定条件（fail closed）

源状态集合（封闭）：`PLANNED`、`READY`。

当且仅当以下条件**全部成立**时允许 pre-attempt abort；任一条件不成立或无法确定，一律 fail closed 拒绝：

1. Run 当前状态属于封闭源状态集合 `{PLANNED, READY}`；
2. Run **无任何 Attempt 记录**——权威存储中该 Run 的 Attempt 记录数为零，即「从未产生过 Attempt」，而非「当前没有运行中的 Attempt」；
3. **无 publication intent**——不存在该 Run 的任何发布意图记录（ADR 0007 先记录意图的发布模型下的意图记录）；
4. **无 SideEffect**——权威 ledger 中不存在归属该 Run 的 SideEffect 记录（ADR 0019 append-only 副作用对账模型下的事实）；
5. **无已发布分支**——不存在该 Run 的 PublicationRecord，亦无由该 Run 产生的远端分支；
6. 持有 Run Lease（证明无其他进程在管理该 Run），actor 为 human（沿用 ADR 0012 / approve 的 source 校验风格）；
7. Run 尚未处于任何终态。

条件 2–5 全部是**否定性证明**：系统必须在作出终止决定前，以权威存储的记录缺失肯定地确认每一项，不得因记录不可读、存储缺失或查询失败而推定为无。判定顺序冻结为：先状态集合，再否定性条件 2–5，再 Lease；任何一步失败即终止判定并拒绝。

该条件集合的本质：同时成立即证明终止时刻不存在任何需要停止的子进程、需要冲刷的证据、需要对账的远端事务。这是本出口与活跃 Run 介入（ADR 0010）、Attempt 后废弃（ADR 0012）的边界划分依据。

### 2. 终止后的终态与 Outcome 证据要求

- 转换：`PLANNED → ABORTED` 与 `READY → ABORTED`，事件类型沿用 `run.aborted`（与 ADR 0012 同一事件家族，下游工具链按既有事件类型识别，以 payload 区分来源）；
- payload 记录固定 `terminalReason: "aborted-before-attempt"` 与独立的操作者 `reason` 字段——操作者自由文本不进入 terminalReason 值本身；`actor` 记录执行终止的人类操作者身份；
- 本出口**启用 `ABORTED` 终态**。状态表对 `ABORTED` 的持久化前置条件（Abort 原因与保存的证据）已由 [任务生命周期](../task-lifecycle.md) 定义；ADR 0012 v1 因下游工具链未经覆盖而未启用它，pre-attempt 路径没有历史 Attempt 与证据需要处置，是启用该状态负担最小的路径。ADR 0012 的 RETRY_PENDING 出口保持 `BLOCKED` + `terminalReason: "aborted-by-operator"` 不变，本 ADR 不做迁移、不做预决（见「与 ADR 0012 的关系」）；
- **Outcome 证据要求沿用 ADR 0012 的证据纪律**：abort 路径必须写入终态 Outcome（schema 合法、终态 `ABORTED`、verdict `abort`，证据摘要绑定 abort 事件 payload），并补齐 cleanup `validateOutcome` 要求的 `result.md`，使后续 cleanup 可通过。「所有进入终态的路径都必须产出 Outcome」的先例继续有效；
- 不触碰 worktree、不产生任何远端副作用；worktree 的回收仍由 cleanup 在 abort 之后按既有守卫执行（dirty worktree 拒绝删除、tombstone 两阶段记录）。

### 3. 明确排除（fail closed）

以下 Run 一律不得经本出口终止；相应拒绝必须携带出口指引（见「CLI 语义」的 sentinel 表）：

- **已进入 `PUBLISHING` 或其后阶段（`PUBLISHED`、`CI_PENDING`）的 Run**：存在进行中或已完成的发布事务与远端工件，其退出必须经 typed reconciliation（ADR 0026，及 ADR 0028 提议的 reconcileType 扩展）或人工护栏（维护者对远端分支/PR 的显式处置）。本出口一律拒绝；
- **存在 publication intent、SideEffect 记录或已发布分支的 Run**：即使其状态恰为 `PLANNED`/`READY`（正常生命周期下不应出现，作为防御性检查保留），同样一律拒绝——否定性条件任一不成立即 fail closed；
- **存在活跃子进程或进行中门禁事务的非终态**（`RUNNING`/`VERIFYING`/`REVIEW_PENDING`/`REWORK_REQUESTED`）：维持 ADR 0012 已冻结的固定拒绝；对活跃 Run 的受控 abort 仍由 ADR 0010 intervention 路径负责，与本出口不重叠；
- **`RETRY_PENDING`**：存在 Attempt 历史，天然不满足「无 Attempt 记录」条件，不在本出口源状态集合内；其出口仍是且仅是 ADR 0012；
- **已终态的 Run**（包括 `ABORTED`）：再次 abort 必须失败。

当日运维实例中的 PUBLISHING 卡死与 CI_PENDING 陈旧 Run 均落入排除集合——它们已产生发布记录或远端工件。本 ADR 有意不为它们提供出口：错误地「方便清理」这类 Run 会掩盖远端事实，正确路径是 reconcile 与人工护栏另案。

### 4. 与 ADR 0012 的关系：补充，不是替代

- ADR 0012 的全部冻结语义保持不变：`RETRY_PENDING → BLOCKED`、`terminalReason: "aborted-by-operator"`、终态 Outcome 与 `result.md` 纪律、Lease 要求、不触碰 worktree；本 ADR 不修改、不取代其中任何一条；
- 本 ADR 在**同一命令**下新增第二个出口：两个出口由互斥的源状态集合与守卫条件区分——ADR 0012 管「Attempt 已停止后被放弃」（有执行历史，目标 `BLOCKED`），本 ADR 管「从未开始」（零执行痕迹，目标 `ABORTED`）。同一 Run 不可能同时满足两个出口的条件，无重叠、无歧义；
- `terminalReason` 封闭集合由此获得第二个成员 `aborted-before-attempt`（与既有 `aborted-by-operator` 并列）。该集合的进一步扩展只能经 ADR 修订，不得注入自由文本；
- ADR 0012 遗留的「v2 是否启用独立 ABORTED 状态」问题（即 RETRY_PENDING 出口是否迁移到 ABORTED）**不在本 ADR 范围内预决**；若未来决定迁移，需另行 ADR 并处理该路径下的证据冲刷与工具链覆盖问题。

### 5. CLI 语义：`marshal task abort --run RUN_ID`

命令形态不变：`marshal task abort --run RUN_ID --actor ID --reason TEXT [--json]`。判定流程冻结为：

1. Run 不存在或已终态 → 以既有固定错误拒绝；
2. 源状态为 `RETRY_PENDING` → 走 ADR 0012 路径，行为与拒绝消息与现状完全一致；
3. 源状态为 `PLANNED`/`READY` → 按「判定条件」节顺序检查否定性条件与 Lease：
   - 全部通过 → 执行 `→ ABORTED` 转换，写入终态 Outcome 与 `result.md`，命令成功返回；
   - 任一不通过 → 以下表对应的固定 sentinel 拒绝；
4. 源状态为其他非终态 → 以固定错误拒绝，消息按状态携带出口指引。

拒绝 sentinel（封闭集合，机器可读；`--json` 输出同一 sentinel；sentinel 构成固定消息骨架，操作者自由文本不参与拼接）：

| 拒绝 sentinel | 触发条件 | 消息中的出口指引 |
| --- | --- | --- |
| `abort-denied-attempt-exists` | 状态为 `PLANNED`/`READY` 但存在 Attempt 记录 | 等待状态推进后经 ADR 0012 abort，或经 ADR 0010 intervention 路径 |
| `abort-denied-publication-intent-present` | 存在 publication intent 记录 | 先行处置发布意图（撤销/对账），或人工护栏 |
| `abort-denied-side-effect-present` | 存在 SideEffect 记录 | ADR 0019 append-only 对账/补偿，或人工护栏 |
| `abort-denied-publication-present` | 存在 PublicationRecord 或已发布分支（含 `PUBLISHED`/`CI_PENDING`） | typed reconciliation（ADR 0026 / ADR 0028），或人工护栏 |
| `abort-denied-publication-in-progress` | 状态为 `PUBLISHING` | 待发布事务落定后经 reconcile，或人工护栏 |
| `abort-denied-state-not-eligible` | 其余不满足源状态集合的非终态（`RUNNING`/`VERIFYING`/`REVIEW_PENDING`/`REWORK_REQUESTED`） | 活跃 Run 经 ADR 0010 intervention 路径 |

拒绝是**终局判定**：不写入任何转换事件、不改变 Run 状态、不产生部分 Outcome；除命令层诊断输出外不构成生命周期事实。Lease 获取失败与终态再入的拒绝沿用 ADR 0012 的既有消息与语义。

新增 sentinel 只能经本 ADR 后续修订或新 ADR 扩展该封闭集合。

## 备选方案（已否决）

- **扩展 ADR 0012 出口，允许 `PLANNED`/`READY` 但同样落到 `BLOCKED`**：复用 BLOCKED 工具链成本最低，但把「操作者终止一个从未开始的 Run」混入「需要外部输入或能力」的广义阻塞语义，使 `ABORTED` 状态继续空转；pre-attempt 路径无历史 Attempt 与证据负担，是启用 `ABORTED` 成本最小的路径，放弃该机会没有正当理由。否决；
- **让 cleanup 直接回收「疑似死亡」的非终态 Run（如 READY 超时且无 Attempt）**：绕过生命周期不变量与证据纪律、不留审计痕迹；同类方案已在 ADR 0012 否决，维持否决。且 READY Run 可能只是尚未获得 dispatch 机会，超时不等于被放弃。否决；
- **自动超时 GC**：与 ADR 0012 的否决理由一致（无证据留存审查），对本场景额外叠加上一条的「超时≠放弃」问题。否决；
- **把源状态集合同时扩大到 `RETRY_PENDING` 或其他非终态**：`RETRY_PENDING` 有 Attempt 历史与潜在证据处置问题，活跃状态有子进程与门禁事务，退出语义与 pre-attempt 完全不同；扩大源状态集合会把最小风险的终止面变成新的风险面，违背本 ADR 的边界划分依据。否决；
- **为 pre-attempt 出口新建独立命令（如 `marshal task discard`）**：操作者心智模型应当是单一 abort 入口，行为差异由守卫条件与 sentinel 表达；并列命令增加发现成本与行为分叉面，且使「死 Run 先 abort 再 cleanup」的既有 runbook 指引复杂化。否决。

## 后果

- Worker 启动前失败的 READY Run（Issue #39）与被放弃的 PLANNED Run 获得合法出口，状态卫生闭环（abort → cleanup）覆盖「从未开始」这一类死 Run；
- `ABORTED` 终态由「预留」变为实际可达，下游工具链（cleanup/doctor/Outcome 渲染）对 `ABORTED` 的覆盖成为本 ADR 的配套实现要求，属后续 Milestone 退出门禁；
- 操作者获得可判定的失败反馈：拒绝消息以 sentinel 区分「状态不适用」「存在执行痕迹」「存在发布/副作用事实」，并直接指引正确出口（ADR 0012 abort / intervention / reconcile / 人工护栏），减少试错式运维；
- 成本：reducer 两条转换与 pre-attempt 守卫检查、abort 命令判定分支与封闭 sentinel 集合、Outcome/`result.md` 写入事务对 `ABORTED` 的覆盖、表驱动与命令级正反测试；不改变信任边界与证据绑定语义；
- 明确不解决 PUBLISHING 卡死与 CI_PENDING 陈旧 Run：它们被显式排除，等待 reconcile/人工护栏另案；
- 不预决 ADR 0012 的 v2 问题（RETRY_PENDING 出口是否迁移至 `ABORTED`）。

## 与既有 ADR 的关系

- **ADR 0012（Accepted）**：补充，不是替代。两出口共享命令入口、actor/reason/Lease 纪律与 Outcome 证据纪律，由互斥源状态与守卫条件划分边界；0012 冻结语义逐条保持不变；
- **ADR 0010（Accepted）**：对活跃 Run 的受控 abort 由 intervention 路径（`abort` 类别）负责，与本出口不重叠。若未来 intervention 路径启用 `ABORTED` 目标态，应与本 ADR 共享 `ABORTED` 的持久化前置条件与 Outcome 纪律；
- **ADR 0007（Accepted）/ ADR 0019（Accepted）**：publication intent 与 SideEffect 记录是判定条件 3/4 的否定性证明输入；本 ADR 不修改两类记录的产生与对账语义；
- **ADR 0026（Accepted）/ ADR 0028（Proposed）**：排除集合（存在发布事实的 Run）的唯一状态迁移出口是 typed reconciliation；本 ADR 的拒绝消息直接指向该出口；
- **[任务生命周期](../task-lifecycle.md)**：接受并实现后需同步转换表（新增 `PLANNED → ABORTED`、`READY → ABORTED` 两行）与 `ABORTED` 状态行注释（不再整体「以 BLOCKED 表达」，ADR 0012 路径的 BLOCKED 表达保持不变）。该文档同步是接受后的配套工作，不在本提案的交付范围内。

## 非目标

- 不实现代码：本 ADR 只冻结契约，实现属后续 Milestone 退出门禁；
- 不为 PUBLISHING 卡死、CI_PENDING 陈旧等存在发布/远端事实的 Run 提供出口；
- 不修改 ADR 0012 的任何冻结语义，不迁移其 RETRY_PENDING 出口的目标态；
- 不引入自动清理、GC 或 retention 强制执行；
- 不授予 Marshal 任何远端操作权限（merge、删分支、关 PR 均不因此出口产生）。
