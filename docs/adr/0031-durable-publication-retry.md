# ADR 0031：Typed Durable Publication Retry（Core-owned PublicationRetryLedger、approval lineage 与 CI_PENDING re-observation）

- 状态：提议（Proposed）——本 ADR 为草案，须经维护者 ApprovalRecord 接受后生效；接受人与接受时间另行记录，不改写本文。接受前，下文全部契约内容仅为草案提议，不构成已冻结契约、已实现能力、已关闭缺口或任何实现完成、可上线、conformance 通过的声明：publication retry 尚未耐久化，CI_PENDING 死角尚未修复，M10/M11/M12/M13 状态不发生任何变化
- 日期：2026-08-17
- 决策来源：公开 [Issue #54](https://github.com/chiga0/marshal-harness/issues/54)——为 publication 路径反复出现的 git/gh 瞬态失败、ambiguous/lost response，以及首次 publication 后 CI_PENDING 无法重新签发 publish approval、因而不能合法再观察 checks 的死角，冻结一条 Core-owned、append-only、可 crash replay 的 typed durable publication retry 契约。本 ADR 只做契约设计，不实现代码或 Schema
- 关联：[ADR 0003](0003-separate-worker-and-publisher.md)（Worker 与 Publisher 分权）、[ADR 0007](0007-intent-first-publication.md)（先记录意图的受控发布与远端对账）、[ADR 0010](0010-controlled-autonomy-and-intervention.md)（受控自治、审批 Gate 与人工介入）、[ADR 0018](0018-control-plane-and-provider-ports.md)（Control Plane 权威/actor 双键空间、PublicationAuthorization 与 DurableExecutionEngine 单一权威 seam）、[ADR 0019](0019-deterministic-control-plane-typed-execution-and-goal-admission.md)（确定性控制面、typed execution 与 append-only SideEffect 对账）、[ADR 0026](0026-scm-merge-receipt-and-publication-reconcile.md)（SCMMergeReceipt 与 PublicationReconcileRecord）、[ADR 0028](0028-ci-deadline-phased-observation.md)（CI deadline 分阶段观察与可信完成时间裁决）、[ADR 0029](0029-pre-attempt-abort.md)（无 Attempt Run 的显式 abort 出口）、[任务生命周期](../task-lifecycle.md)、[故障与恢复](../failure-and-recovery.md)

## 上下文

### 问题陈述（Issue #54 公开事实）

Issue #54 与维护者 2026-08-14 comment 记录了三类反复出现的原始错误：

1. **git/gh 瞬态失败**：`git ls-remote failed: signal: killed` 等进程级/传输级失败反复出现。这类失败的语义是ambiguous 或 transient 取决于远端是否已应用效果，但当前实现只有自由文本错误，没有任何 typed 分类可供恢复路径消费；
2. **malformed remote branch observation**：远端分支观察结果畸形。它既可能是瞬态观察故障（可有界重试），也可能是身份/协议冲突（必须 fail closed），当前实现不区分二者；
3. **publish driver 静默死亡**：publication 执行进程无声退出，远端效果可能已发生、可能未发生、可能部分发生。当前实现以本地进程退出码推断结果，无法表达「response lost → 必须对账」这一语义。

第四类问题是本 ADR 的另一半动因——**CI_PENDING 死角**，公开复现链如下：

1. Draft PR 已成功建立，`publication.completed` 已记录，Run 进入 `CI_PENDING`；
2. 远端 required checks 随后全部通过；
3. 原 publish 进程已退出（或从未进入 CI 观察即失败），需要在新进程中重新观察 checks；
4. 幂等重跑 `marshal task publish` 被判「缺少当前有效 publish approval」——`controlplane.Require` 在 `publication.Publish` 之前无条件校验 publish approval，而 approval binding 精确包含当前 `stateSequence`，journal 早已推进；
5. `CI_PENDING` 又不是可签发 publish approval 的状态，新 approval 无法签发；
6. 结果：公开 CLI 不存在任何合法路径能重新调度这次本应只读的 checks 观察——checks 已全绿，Run 却永久困在 `CI_PENDING`。

缺失的不是数据可得性——PublicationRecord、RemoteCheckRecord、ApprovalRecord、ReviewDecision 均已携带恢复所需的全部身份事实——而是承载「耐久 retry 调度、typed 失败分类、授权 lineage 与只读 continuation」的契约。本 ADR 给出该契约的草案（接受后冻结）。

### current implementation（基线 commit `981b53d` 上的既有行为，逐字准确）

以下内容是现状记录，不是本 ADR 的目标能力；不得把现状误读为已具备 durable retry：

- `internal/cli/cli.go` 的 `task publish` 在调用 `publication.Publish` 之前无条件执行 `controlplane.Require`（Gate 为 `ApprovalGatePublish`）：缺少当前有效 publish approval 即拒绝，不区分「首次 publication effect」与「同世代只读 continuation」；
- `internal/control/approval.go` 的 `currentBinding` 只允许 `PUBLISHING` 状态签发 publish approval，且 ApprovalBinding 精确包含当前 `stateSequence`、`reviewRound`、`decisionDigest`、`evidenceDigest`；`approvalMatches` 要求 ApprovalRecord 与该 binding 逐字一致。journal 推进后 `stateSequence` 漂移，旧 approval 机械失效；`CI_PENDING` 不在可签发状态集合内；
- `publication.Publish`（`internal/publication/service.go`）在 `PUBLISHING` 创建或复用 PublicationIntent，Publisher 对 remote head、branch push、Draft PR 做部分即时 reconcile；成功后写 PublicationRecord 与 `publication.completed`（`PUBLISHING → PUBLISHED`），随后 `publication.checks-requested`（`PUBLISHED → CI_PENDING`）；
- `task accept`/`ObserveChecks`（`internal/publication/checks.go`）是独立的只读观察入口；checks pass 时经 `CI_PENDING → ACCEPTED` 既有转换收敛；
- supervisor 对 dead `PUBLISHING` driver 只有 `retry-publish`（`ActionRetryPublish`）一种动作；`PUBLISHED`/`CI_PENDING` 不产生动作；
- 当前不存在 Core-owned 的 publication retry ledger、持久 `notBefore`、typed retry budget 或跨进程 timer；`publication-error.json` 只是可被下一次运行覆盖/删除的诊断文件；git/gh 错误多为自由文本，不进入任何 typed 事件身份。

### 权威性分工（不变量前提）

SCM Provider（git/gh）与 Publisher 只报告 **typed observations/receipts**：某条 commandId 的执行观察、远端身份事实、receipt。retry 的准入（admission）、预算消费、退避调度、终局裁决一律是 Marshal Core 的独占职责。Provider/Publisher/DurableExecutionEngine/supervisor/timer queue/snapshot/doctor/进程状态都不是 retry authority，不决定重试、不延长预算、不改 lifecycle、不签发 approval、不覆盖历史。Worker 不参与 publication retry。本 ADR 的全部设计以该分工为前提，且不弱化 ADR 0003/0007/0010/0018/0019 已冻结的分权、human approval、精确证据绑定、append-only journal、intent-first、Draft-only、merge-never、fencing、current-ledger recheck 与 fail-closed 不变量。

## 决策（target contract 概览）

核心方向：publication 路径的每一次重试都由 **Core-owned、append-only 的一等权威账本** 承载；失败是 **typed** 的；授权按 **publication generation** 继承而非按 `stateSequence` 机械重签；CI_PENDING 的 checks 再观察是**只读、幂等的 typed continuation**，不需要也不允许重新签发 human publish approval。契约分十个部分冻结：

1. 一等 Core-owned append-only `PublicationRetryLedger`（名称固定）及其不可变条目 `PublicationOperationAttempt`（名称固定）；
2. `operation` 与 `operationClass` 封闭集合，ambiguous effect 必须先对账后重发；
3. typed `PublicationFailure`（名称固定）：封闭 category/retryDisposition/reasonCode 与脱敏规则;
4. 单一 retry authority、publication 专用预算与确定性退避；
5. publish approval 与 PublicationAuthorization 的代际语义（inheritance ≠ bearer grant）；
6. CI_PENDING checks re-observation 的 typed continuation 契约；
7. lost response、idempotency 与 reconcile 顺序；
8. crash/replay 矩阵与 full replay 不变量；
9. doctor 与 observability（typed events、诊断码、安全 repair 边界）；
10. future implementation slices 与测试矩阵（只设计，不实现）。

除下文封闭集合外，本 ADR 不引入新的 lifecycle 状态，不开放通用 same-state transition，不新增 merge/ready/release/deploy 权限。

### 词汇与命名（封闭集合）

| 类别 | 名称 | 说明 |
| --- | --- | --- |
| 权威账本 kind | `PublicationRetryLedger` | 名称固定；一等 Core-owned append-only 权威对象，属 `authorityNamespaceId` |
| 账本条目 kind | `PublicationOperationAttempt` | 名称固定；不可变条目，一次 admission 与一条条目一对一 |
| 失败记录 kind | `PublicationFailure` | 名称固定；typed 失败分类记录，内容寻址 |
| 事件/条目类型（新增） | `publication.retry.scheduled`、`publication.retry.started`、`publication.retry.failed`、`publication.retry.reconciled`、`publication.retry.completed`、`publication.retry.terminal` | retry ledger 的 typed 条目类型；全部携带封闭 payload（见第十节） |
| 复用对象（不新增） | `PublicationIntent`、`PublicationRecord`、`ApprovalRecord`、`ReviewDecision`、`SideEffectIntent`、`PublicationAuthorization`、`SCMMergeReceipt`、`PublicationReconcileRecord`、`RemoteCheckRecord` | 既有对象；本 ADR 只新增对它们的 digest 引用 |
| 世代身份 | `publicationGenerationId` | 确定性派生（见「publicationGenerationId 派生公式」），是 retry、授权继承与 continuation 的锚 |
| 摘要名 | `requestDigest`、`intentDigest`、`publicationAuthorizationDigest`、`failureDigest`、`receiptDigest`、`reconcileDigest`、`sanitizedDiagnosticDigest`、`attemptDigest` | 均为 `sha256:<64 hex>` canonical digest（Marshal canonical JSON/JCS 语义，与 `canonical.DigestJSON` 一致） |

## 一、PublicationRetryLedger 与 PublicationOperationAttempt

### 权威边界

- 每个 `(authorityNamespaceId, taskId, runId)` 拥有唯一一个 `PublicationRetryLedger`；它是 ADR 0018 权威键空间中 `authorityNamespaceId` 独占拥有的权威对象，与 ledger/SideEffectIntent/Receipt reconcile/idempotency/outbox 同级；
- Core 独占：条目 append、retry admission、timer（`notBefore` 到期判定）、预算消费、终局裁决。Publisher 与 SCM transport 只执行指定 `commandId` 并返回 typed observation/receipt；
- snapshot、timer queue、supervisor view、doctor 输出全部是 ledger 的**可重建投影**，不得成为第二权威；投影丢失、落后或漂移不得改变任何 replay 结论；
- DurableExecutionEngine 只对同一 `commandId` 做 at-least-once delivery：其 activity 级重投递必须携带同一 `idempotencyKey`/`requestDigest`，不追加 ledger 条目、不消费 Core 预算。transport/activity retry 与 Core publication retry 不相乘：总效果尝试次数恒等于 ledger 中 `scheduled` 条目数。

### PublicationRetryLedger 封闭字段表

`apiVersion=marshal.dev/v1alpha1`，`kind=PublicationRetryLedger`，`additionalProperties: false`：

| 字段 | 类型与约束 | 语义 |
| --- | --- | --- |
| `apiVersion` | const `marshal.dev/v1alpha1` | 固定 |
| `kind` | const `PublicationRetryLedger` | 固定 |
| `authorityNamespaceId` | ADR 0018 权威键空间身份 | 账本所属权威命名空间 |
| `taskId` | id pattern（与既有 `$defs.id` 一致） | 必须等于 RunState/TaskSpec 身份 |
| `runId` | id pattern | 必须等于 RunState 身份 |

账本内容是其 append-only 条目序列本身；条目顺序即 append 顺序，每条携带自己的 `originEventId`/`originSequence` 绑定。账本不设可变头字段（无计数器、无指针）：`budgetUsed`、next `notBefore`、in-flight 集合全部由 replay 派生。

### PublicationOperationAttempt 封闭字段表

一个 `PublicationOperationAttempt` 是一次 retry admission 的不可变记录，`additionalProperties: false`：

| 字段 | 类型与约束 | 语义 |
| --- | --- | --- |
| `authorityNamespaceId` | 权威键空间身份 | 必须等于所属账本 |
| `taskId` / `runId` | id pattern | 必须等于所属账本 |
| `publicationGenerationId` | `sha256:<64 hex>` | 本尝试所属 publication generation（派生公式见下节） |
| `reviewRound` | integer ≥ 0 | 产出该 generation 的 accept 轮；必须等于该 generation 的 PublicationRecord/ReviewDecision 绑定轮次 |
| `operation` | 封闭枚举（见第二节） | 本尝试执行的操作 |
| `operationClass` | 封闭枚举：`read-only` \| `idempotency-keyed-effect` \| `ambiguous-effect` | 必须与 operation 的固定分类一致（第二节封闭表） |
| `commandId` | typed execution command id | Core 派发、engine at-least-once delivery 的命令身份；同一 `commandId` 与唯一 `(publicationGenerationId, operation, idempotencyKey)` 组合一对一绑定，重试恒重发同一 `commandId` |
| `idempotencyKey` | string，1..256 | 效果去重键；同 key 语义见「canonical identity 与幂等归并」 |
| `requestDigest` | `sha256:<64 hex>` | 本次请求内容的 canonical digest（封闭输入，见下节） |
| `intentDigest` | `sha256:<64 hex>` | 该 generation 的 PublicationIntent/SideEffectIntent canonical digest；effect 尝试必填 |
| `publicationAuthorizationDigest` | `sha256:<64 hex>` | 该 generation 的 PublicationAuthorization（ADR 0018 Core-only edge）canonical digest；effect 尝试必填 |
| `attemptOrdinal` | integer ≥ 1 | 同 `(publicationGenerationId, operation, idempotencyKey)` 内的第几次 admission；replay 派生，不得跳号或重复 |
| `budgetLimit` | integer ≥ 1 | admission 时刻该 operation 的生效上限（快照留证） |
| `budgetUsed` | integer ≥ 1 | admission 后的已用预算（含本条），replay 派生值的留证 |
| `scheduledAt` | RFC 3339（UTC） | admission 事实时刻（事实字段，不参与身份 digest） |
| `notBefore` | RFC 3339（UTC） | Core 计算的最早派发时刻；到期且 recheck 通过才可派发 |
| `startedAt` | RFC 3339（UTC），可空 | 首次派发时刻；未派发即崩溃时为空，由 replay 恢复语义处置 |
| `completedAt` | RFC 3339（UTC），可空 | 终局时刻 |
| `outcome` | 封闭枚举：`applied` \| `not-applied` \| `ambiguous` \| `conflict` \| `unknown`；`scheduled`/`started` 状态时为空 | 终局裁决（见状态机） |
| `failureDigest` | `sha256:<64 hex>`，可空 | 关联 PublicationFailure 的 canonical digest |
| `receiptDigest` | `sha256:<64 hex>`，可空 | 采纳的 receipt（SCMMergeReceipt/PR receipt/push observation）canonical digest；`outcome=applied` 必填 |
| `reconcileDigest` | `sha256:<64 hex>`，可空 | 采纳的 reconcile 记录（PublicationReconcileRecord/checks reconcile binding）canonical digest |
| `originEventId` | event id | 本条目对应 ledger 事件的 `eventId`（append 前预生成并绑定） |
| `originSequence` | integer ≥ 1 | 本条目对应 ledger 事件的 `sequence` |

### publicationGenerationId 派生公式（封闭输入）

`publicationGenerationId` 必须由同一 generation 的既有身份事实确定性派生，封闭输入集合为：

```
taskId, runId, reviewRound,
intentDigest,            // 该 generation 的 PublicationIntent canonical digest
decisionDigest,          // 该 generation 的 accept ReviewDecision canonical digest
evidenceDigest,          // 该 generation 的 evidenceDigest
repositoryId, headSha    // 该 generation 的远端仓库与 head 身份
```

`publicationGenerationId = canonical.DigestJSON(上述字段按固定顺序组成的 canonical object)`。输入中不含任何 wall-clock、绝对路径、token 或可变展示字段。同一 generation 的 PublicationIntent/ReviewDecision/evidence/head identity 任一变化都会产生新的 `publicationGenerationId`；反之，同世代内 retry journal 的增长、`stateSequence` 的推进不改变它。

### canonical identity 与幂等归并

- `attemptDigest = canonical.DigestJSON({authorityNamespaceId, taskId, runId, publicationGenerationId, operation, idempotencyKey, requestDigest, attemptOrdinal})`；
- 所有 digest 使用 Marshal canonical JSON/JCS 语义；digest 输入与事件身份中禁止出现：凭证/token、绝对本机路径、原始 stderr、任意 Provider 自由文本输出、可变 wall-clock 展示字段（时刻字段只作为事实留证，不参与身份）；
- **同 key 同 digest replay**：同一 `(authorityNamespaceId, publicationGenerationId, operation, idempotencyKey)` 且 `requestDigest` 相同的重放请求 → 幂等归并：返回既有尝试的结果，不追加第二条条目、不再次消费预算、不产生任何远端重复效果；
- **同 key 异 digest conflict**：同一 key 但 `requestDigest`（或 generation/intent/authorization 绑定）不同 → 固定 `conflict`，严格零副作用（不追加条目、不执行远端调用、不改 lifecycle），由人工经 typed reconcile/新 gate 处置。

### PublicationOperationAttempt 状态机（封闭转换）

```
                       ┌────────────────────────────────────────┐
 admission ──► scheduled ──► started ──► closed(outcome=applied)
                  │             │  └───► closed(outcome=not-applied)
                  │             ├──────► ambiguous ──► closed(outcome=applied|not-applied|conflict|unknown)
                  │             └──────► closed(outcome=conflict)
                  └────────────────────► closed(outcome=conflict)
```

| 状态 | 语义 | 允许的后继 |
| --- | --- | --- |
| `scheduled` | admission 通过，预算原子消费，`notBefore` 已写入；尚未派发 | `started`；发现身份冲突时直接 `closed(conflict)` |
| `started` | 同一 `commandId` 已派发（`startedAt` 记录）；engine 对同一 `commandId` 的重复投递不改变状态、不新增条目 | `closed(applied)`、`closed(not-applied)`、`ambiguous`、`closed(conflict)` |
| `ambiguous` | timeout、process kill 或 response lost；禁止以本地进程退出码推定 `not-applied` | 只能经对应 observe/reconcile 进入 `closed`；reconcile 结果 `unknown` → `closed(unknown)` fail closed |
| `closed` | 终局；`outcome` 封闭五值：`applied`（receipt/reconcile 证明已应用）、`not-applied`（typed 证明未应用）、`conflict`（身份冲突 fail closed）、`unknown`（reconcile 无法判定 fail closed）、`ambiguous` 不是终局值 | 无 |

- `outcome=applied` 必须绑定 `receiptDigest` 或 `reconcileDigest` 之一；`outcome=ambiguous` 期间，任何重发请求一律拒绝（必须先对账）；
- retry 链的账本级终局由 `publication.retry.terminal` 条目表达（见第四节终局裁决），不把单个 attempt 状态与 lifecycle 状态混用。

## 二、operation 与 operationClass（封闭集合）

| operation | operationClass | 对账身份（reconcile identity） |
| --- | --- | --- |
| `repository-observe` | `read-only` | repository 身份与可见性事实 |
| `actor-observe` | `read-only` | actor/credential 有效性事实（不携带 credential 本体） |
| `branch-observe` | `read-only` | 远端分支 ref/head 身份 |
| `commit-observe` | `read-only` | commit 身份 |
| `pr-observe` | `read-only` | repository/marker/PR id/head/base/draft 身份 |
| `checks-observe` | `read-only` | PublicationRecord/RemoteCheckRecord 身份与 canonical digest |
| `branch-push` | `ambiguous-effect` | exact remote head 与 fast-forward 身份（previousHeadSha → headSha、branch ref） |
| `pr-create` | `idempotency-keyed-effect` | repository/marker（idempotencyKey）/PR id/head/base/draft 身份 |
| `pr-update` | `idempotency-keyed-effect` | repository/marker/PR id/head/base/draft 身份 |

上表封闭：本 ADR 不引入其他 operation；新增 operation 必须经后续修订或新 ADR。分类语义：

- **`read-only`**：可按 policy 有界 retry；失败不产生远端副作用，重试只需重发同一 `commandId`；
- **`idempotency-keyed-effect`**：效果操作，provider 调用携带稳定 `idempotencyKey`/marker；必须**先有持久 SideEffectIntent/PublicationAuthorization**，重放恒为同一 `commandId`/`idempotencyKey`/`requestDigest`；
- **`ambiguous-effect`**：git transport 无 provider 侧幂等键，timeout/kill/response lost 必然 ambiguous；任何不确定结果都必须先对账 exact remote head/fast-forward 身份。

effect 操作的强制规则（对两个 effect class 均成立）：发生 timeout、process kill 或 response lost 时，attempt outcome 为 `ambiguous`；必须先执行对应 observe/reconcile——证明 `not-applied` 才允许重发同一 `commandId`；证明 `applied` 则采纳同一 receipt，不重复执行业务效果；身份冲突或 reconcile 结果 `unknown` 一律 fail closed。**禁止盲重放 `branch-push`/`pr-create`/`pr-update`。**

## 三、PublicationFailure typed 契约

### 封闭字段表

`apiVersion=marshal.dev/v1alpha1`，`kind=PublicationFailure`，`additionalProperties: false`，内容寻址持久化（digest-verified put-if-absent，永不覆盖）：

| 字段 | 类型与约束 | 语义 |
| --- | --- | --- |
| `apiVersion` / `kind` | const | 固定 |
| `authorityNamespaceId` / `taskId` / `runId` | 身份字段 | 与账本一致 |
| `publicationGenerationId` | `sha256:<64 hex>` | 失败所属 generation |
| `reviewRound` | integer ≥ 0 | 同 attempt |
| `operation` / `operationClass` | 封闭枚举 | 与 attempt 一致 |
| `commandId` / `idempotencyKey` / `attemptOrdinal` | 同 attempt 绑定 | 失败必须可回指唯一 attempt |
| `category` | 封闭枚举（见下） | 失败大类 |
| `retryDisposition` | 封闭枚举（见下） | Core 裁决的处置方式 |
| `reasonCode` | 封闭枚举（见下） | 具体原因码 |
| `providerExitClass` | 封闭枚举：`signal-killed` \| `non-zero-exit` \| `transport-error` \| `response-lost` \| `provider-rejected` \| `schema-invalid` \| `unknown` | provider 退出的 typed 分类；取代自由文本退出描述 |
| `sanitizedDiagnosticDigest` | `sha256:<64 hex>` | 脱敏诊断对象的 canonical digest |
| `observedAt` | RFC 3339（UTC） | Marshal 观察到失败的时刻（事实字段，不参与身份） |
| `originEventId` / `originSequence` | 事件身份 | 绑定产生本记录的 `publication.retry.failed` 事件 |

`failureDigest = canonical.DigestJSON(记录 bytes)`。

### category（封闭枚举）

| category | 语义 |
| --- | --- |
| `transient` | 明确的瞬态故障（含可重新观察的 read-only 观察故障）；未应用语义明确或无远端副作用 |
| `ambiguous` | effect 操作的 timeout/kill/response lost；远端是否已应用未知，必须先对账 |
| `permanent` | provider 确定性拒绝该操作本身；重试无意义 |
| `conflict` | 身份冲突（同 key 异 digest、receipt 身份不符、generation 漂移） |
| `authorization` | approval/authorization stale、revoked、expired 或 digest 不符 |
| `budget` | publication 专用 retry 预算耗尽 |
| `deadline` | publication 专用 retry deadline 超限 |
| `protocol` | 请求/响应 schema 或协议非法；身份或协议冲突永不 retry |

### retryDisposition（封闭枚举）与映射

| retryDisposition | 语义 |
| --- | --- |
| `retry-after-notBefore` | Core 在 ledger 事务中写入下一次 `notBefore`；到期且 recheck 通过后重发同一 `commandId` |
| `reconcile-before-retry` | 必须先执行对应 observe/reconcile；证明 `not-applied` 后才允许重发 |
| `do-not-retry` | 终局；写 `publication.retry.terminal`，不自动重试 |

category → retryDisposition 封闭映射：`transient → retry-after-notBefore`；`ambiguous → reconcile-before-retry`；`permanent`/`conflict`/`authorization`/`budget`/`deadline`/`protocol → do-not-retry`。

### reasonCode（封闭枚举）

至少且仅限：`process-killed`、`transport-timeout`、`rate-limited`、`provider-unavailable`、`malformed-observation`、`identity-conflict`、`authorization-stale`、`retry-budget-exhausted`、`retry-deadline-exceeded`、`protocol-invalid`。该集合由本 ADR 新立，属于 `PublicationFailure` 自有封闭集合：它不修改、不扩展 ADR 0028 的封闭原因码集合，也不进入 `publication.blocked` 既有 `error` 字段语义；新增原因码只能经后续修订或新 ADR。

reasonCode 与 `providerExitClass` 的组合必须与 category 一致（如 `process-killed` + effect 操作 → `ambiguous`；`process-killed` + read-only → `transient`）；不一致的组合在 semantic validation 层拒绝。

### 脱敏规则（冻结）

`sanitizedDiagnosticDigest` 的输入对象字段封闭：`providerExitClass`、`reasonCode`、`operation`、`attemptOrdinal` 以及 policy 显式枚举的结构化信号存在性标记。**以下内容永不进入事件身份、digest 输入或任何持久 retry 记录**：credential/token、绝对本机路径、原始 stderr、任意 Provider 自由文本输出。错误示例与诊断文本只能引用 Issue #54 公开文本级别的信息。

### malformed observation 的有界 retry 规则

`malformed-observation` 只有在同时满足以下条件时才可有界 retry（category=`transient`，disposition=`retry-after-notBefore`）：无身份冲突、Schema/current-ledger 可重新观察、operation 为 `read-only`、policy 明确分类。effect 操作的 malformed observation 按 `ambiguous` 处置（先对账）；凡属身份或协议冲突（`identity-conflict`/`protocol-invalid`）一律 `do-not-retry`，永不重试。

## 四、单一 retry authority、预算与退避

### 预算字段（future schema，本 ADR 不实现）

Policy/TaskSpec 的 future schema 应提供 publication 专用预算块（字段名为提议，Schema 实现属后续 slice），封闭语义如下：

| 字段 | 语义 |
| --- | --- |
| `publicationRetry.totalBudget` | 单个 `publicationGenerationId` 内全部 operation 的总尝试上限 |
| `publicationRetry.perOperationLimits` | 按 operation 的尝试上限（每个 operation 一个） |
| `publicationRetry.deadlineSeconds` | 单个 generation 的 retry 总 deadline（锚定 generation admission 时刻，不锚定 Run 创建时刻） |
| `publicationRetry.backoff.baseDelaySeconds` / `maxDelaySeconds` | 指数退避基值与上界 |
| `publicationRetry.backoff.jitterPolicy` | jitter 策略标识；jitter 值只能由稳定 seed 确定 |

**禁止复用** `maxAttempts`、`maxOperationalRetries`、`maxReworkRounds`——它们是 Run/Attempt/rework 预算，与 publication retry 语义不同；混用会使 Worker 耗时侵蚀 retry 窗口并使计数互相污染。**禁止** SDK、git、gh、DurableExecutionEngine 与 Core 各自叠加隐藏 retry：transport/activity retry 与 Core publication retry 不相乘——engine activity 重投递只重发同一 `commandId`（同一 `idempotencyKey`/`requestDigest`），不消费本预算；任何一层新增重试都必须先经 Core admission。

### 原子消费与派发条件（冻结顺序）

1. Core 在**同一个 ledger 事务**中：校验 admission（预算、deadline、授权 current-ledger recheck）→ 原子消费一次预算（`budgetUsed` +1 的留证写入条目）→ 计算并写入 `notBefore` → append `publication.retry.scheduled`；
2. 派发只发生在：`notBefore` 到期，**且** current-ledger recheck 全部通过——generation 未漂移、intent/target/`requestDigest` 未变、PublicationAuthorization 未 expired/revoked 且 digest 一致、lifecycle 状态仍接纳该 operation、Run lease 仍持有；
3. 派发恒为同一 `commandId`；recheck 任一不通过 → 不派发，按对应 category 写 typed failure/terminal decision；
4. 预算与 deadline 的权威值是 replay 派生值；任何投影（snapshot/supervisor view/doctor 输出）与之冲突时以 replay 为准，投影重建。

### 确定性退避（可重放）

`notBefore = completedAt(前序尝试) + delay(n)`，其中 `delay(n) = min(baseDelaySeconds × 2^(n-1), maxDelaySeconds) + jitter`，`n = attemptOrdinal`；`jitter` 由稳定 seed 确定性派生（seed 输入封闭：`idempotencyKey`、`attemptOrdinal`、`publicationGenerationId` 的 canonical digest），**不得**从进程随机状态、wall-clock 或环境变量重算。同一 ledger 在任何进程重放必须得到逐字相同的 `notBefore` 序列。首次 admission（无失败前序）的 `notBefore` 等于 admission 时刻（立即可派发）。

### 终局裁决（budget exhausted / deadline exceeded / do-not-retry）

预算耗尽或 deadline 超限（以及任何 `do-not-retry` category）时，Core 写 typed terminal decision（`publication.retry.terminal`，携带 reasonCode、generation、operation、digest 绑定）与 Outcome/diagnostic evidence 留证。终局之后：

- **不制造虚假 PR**：不得以「看起来已发布」的远端状态伪造 PublicationRecord 或 `publication.completed`；
- **不自动把任意状态转为 `BLOCKED`**：目标状态必须经过当前 lifecycle 的合法转换（如既有 `PUBLISHING → BLOCKED` 的 typed failure 路径）或 typed reconcile；无合法转换时保持现状态并留证，由操作者经既有显式出口（含 ADR 0029 abort 语义与既有 reconciliation）处置；
- **不得创建新 intent 绕过 exhausted budget**：同 generation 内以新 PublicationIntent/新 idempotencyKey 重新入队一律拒绝（`conflict`/`retry-budget-exhausted`）。

## 五、publish approval 与 PublicationAuthorization 的代际语义

### 首次签发（既有语义，逐字保持）

首次 `PUBLISHING` 的 human publish ApprovalRecord 仍精确绑定：`reviewRound`、ReviewDecision/evidence（`decisionDigest`/`evidenceDigest`）、policy/capability/base 与当时 gate facts（含当时 `stateSequence`）。本 ADR 不放宽该精确绑定，不弱化 human approval。

### PublicationAuthorization 物化（新增契约）

Core 据该 ApprovalRecord 为**一个** `publicationGenerationId` 物化 PublicationAuthorization（ADR 0018 Core-only typed edge：默认拒绝、issuer 固定为 Core、携带 expiry/revocation/digest 与专属绑定）：一个 `publicationGenerationId` 与一个 PublicationAuthorization 一对一绑定，授权仅在该 generation 内有效。`publicationAuthorizationDigest` 是授权的唯一引用形式；派生 token/handle 不得成为第二权威。

### 世代内继承（关闭 stateSequence 机械漂移）

随后同 generation 的 retry journal sequence 增长**不得**让该授权因 `stateSequence` 机械漂移而失效：

- 重试只**继承**既有 `publicationAuthorizationDigest`，不要求按新 `stateSequence` 重签 approval；
- 每次派发前执行 current-ledger recheck：generation 一致、intent 一致、target 一致、operation 在授权范围内、expiry/revocation 有效、evidence digest 一致；任一不符即拒绝派发；
- 继承只作用于同一 `publicationGenerationId`。

### 继承不是 bearer grant（反例冻结）

以下情形一律拒绝（reasonCode `authorization-stale`，category `authorization`，disposition `do-not-retry`）：

- 新的 `reviewRound`（rework 后新一轮 accept）；
- 新的 head/intent/`requestDigest` 或目标（target）变化——它们派生新的 `publicationGenerationId`；
- 新的 publication generation 使用旧 authorization；
- stale、revoked、expired 或与当前 binding digest 不符的 approval/authorization。

新 gate/新 approval/新 authorization 只能经既有审批路径重新签发；Core 永不自动补签。

## 六、CI_PENDING checks re-observation（typed continuation）

### 定义与绑定

PublicationRecord 成功并由 `publication.completed` 冻结后，同一 `publicationGenerationId` 的 `checks-observe` 是**首次已批准 publication effect 的 read-only、idempotent continuation**。它绑定（封闭集合）：PublicationRecord digest、ReviewDecision digest、`evidenceDigest`、`repositoryId`、`requestId`、`headSha`、`requiredChecks` 与 `publicationAuthorizationDigest`。

### 授权 lineage（关闭 Issue #54 死角）

该 continuation 继承首次 publication 的 authorization lineage：同一 `publicationGenerationId` 的只读 checks continuation 不再次审批——**不要求、也不允许**在 `CI_PENDING` 重新签发 human publish approval。`CI_PENDING` 不可签发 publish approval 的既有语义逐字保持——死角不是靠「在 CI_PENDING 补签 approval」关闭，而是靠「只读 continuation 不需要 approval」关闭。

### 路由要求（target contract）

公开 CLI/driver 无论经 `task accept` 还是同一 publish workflow continuation 调度，都必须**在路由到 `checks-observe` 之前**识别该 typed continuation（识别输入：当前状态为 `CI_PENDING`、存在同 generation 的 frozen PublicationRecord 与有效 authorization lineage）。识别为 continuation 的请求不得先走只适用于 `PUBLISHING` 首次 effect admission 的 `Require(current stateSequence)` approval 门禁；识别失败（任一绑定漂移）则 fail closed，不降级为「重新要求 approval」。

### 禁止与漂移处置

continuation 绝不能执行 `branch-push`、`pr-create`、`pr-update` 或扩大 target；其 operationClass 恒为 `read-only`。head/PR/generation/requiredChecks/record digest 任一漂移必须拒绝（`identity-conflict`），并进入 typed reconcile（不自动重试、不自动改判）。continuation 的调度与预算仍受本 ADR 的 ledger/预算/notBefore 契约约束（operation=`checks-observe`），但预算耗尽的终局只影响观察调度，不追溯否定已冻结的 PublicationRecord。

## 七、CI_PENDING 死角正反例矩阵

| # | 场景 | 期望结果 |
| --- | --- | --- |
| 1 | 首次 approval → publication effect 成功 → 进程退出 → 远端 CI 完成 → 新进程重观察 | `ACCEPTED`：typed continuation 继承 authorization lineage，不再次审批；经既有 `CI_PENDING → ACCEPTED` 转换收敛 |
| 2 | checks pending | 保持 `CI_PENDING`；Core 按 `notBefore` 调度下一次 `checks-observe`（同 generation、新 attemptOrdinal） |
| 3 | checks observation response lost | attempt → `ambiguous`；对账 PublicationRecord/RemoteCheckRecord 身份与 canonical digest 后 closed；`unknown` 则 fail closed |
| 4 | 相同 generation/record/head 重放 | 幂等归并：同 key 同 digest 返回既有结果，不追加重复条目、不产生重复业务效果 |
| 5 | `CI_PENDING` 尝试签发新 publish approval | 保持拒绝（既有语义逐字不变），且不妨碍只读 continuation 的调度与接纳 |
| 6 | 旧 approval 用于新 reviewRound / 新 head | 拒绝（`authorization-stale`，`do-not-retry`）；新世代必须经新 gate/新 approval/新 authorization |
| 7 | PublicationRecord/head/request/requiredChecks 任一漂移 | 拒绝（`identity-conflict`），进入 typed reconcile |
| 8 | authorization revoked/expired | 拒绝（fail closed）；不自动补签 |
| 9 | continuation 尝试 `branch-push`/`pr-create`/`pr-update` 或扩大 target | 拒绝（operationClass 违规），零副作用 |

边界划分（冻结）：ADR 0028 的 ciDeadline/completedAt 裁决（含可信完成时间与 deadline fail-closed）保持独立语义，本 ADR 不改变其裁决顺序与结果；ADR 0030（Proposed）的 CI failure typed evidence 与 rework 注入闭环同样独立——checks `fail` 时按 ADR 0030 路径处理，本 ADR 不替它定义 findings。本 ADR 只提供耐久调度与授权 lineage：谁可以在什么时候以什么授权再观察一次 checks，以及这次观察的失败如何被 typed 地承载。

## 八、lost response、idempotency 与 reconcile 顺序

### 冻结顺序（effect 操作）

1. **先账本后远端**：每次 effect 必须先 append intent/attempt/scheduled facts（SideEffectIntent/PublicationIntent、`publication.retry.scheduled`），再执行远端调用；
2. **先 receipt 后 lifecycle**：远端返回 success 时，先内容寻址持久化 receipt/reconcile binding（digest-verified put-if-absent），再追加 lifecycle 事件；
3. **ambiguous 从账本派生**：response lost、driver death 或超时一律从 ledger 状态得出 ambiguous（`started` 且无 receipt/completion 记录），**不以本地进程退出码推定 `not-applied`**。

### 按 operation 的对账身份（冻结）

| operation | 对账身份 |
| --- | --- |
| `branch-push` | exact remote head 与 fast-forward 身份：branch ref、previousHeadSha → headSha 谱系 |
| `pr-create` / `pr-update` | repository、marker/idempotencyKey、PR id、head、base、draft identity |
| `checks-observe` | PublicationRecord/RemoteCheckRecord 身份与 canonical digest |

### 对账结果处置（封闭）

- `applied` → 归并：采纳同一 receipt（`receiptDigest`/`reconcileDigest` 绑定到 attempt），不重复执行业务效果；
- `not-applied` → 允许重发**同一** `commandId`（预算与 recheck 通过时），attemptOrdinal +1；
- `conflict` / `unknown` → fail closed（`closed(conflict)`/`closed(unknown)`），写 typed failure，`do-not-retry`；
- 任何情况下不得创建新 intent/新 idempotencyKey 绕过 exhausted budget。

## 九、crash/replay 矩阵

| # | 崩溃点 | 恢复语义（full replay 结论不变） |
| --- | --- | --- |
| 1 | ledger schedule 前崩溃 | 无任何已提交事实；admission 可整体重来，预算未消费 |
| 2 | schedule 后、远端调用前崩溃 | replay 重建 `scheduled` 条目与 `notBefore`；到期且 recheck 通过后派发同一 `commandId` |
| 3 | 远端 effect 后、receipt 持久化前崩溃 | attempt 为 `started`（无 receipt）→ 按 ambiguous 处置：先对账；远端已应用则采纳 receipt，未应用则重发同一 `commandId`（idempotencyKey/requestDigest 保证不产生第二个业务效果） |
| 4 | receipt 后、lifecycle 事件 append 前崩溃 | receipt bytes 已内容寻址存在；lifecycle 事件未提交则重放确定性 reducer 重新 append（预生成 eventId + expectedSequence CAS 保证不重复提交），receipt 不被重写 |
| 5 | `publication.completed` 后、snapshot 前崩溃 | snapshot 只是投影；full replay 重建，既有语义不变 |
| 6 | `CI_PENDING` observe 后、checks 事件 append 前崩溃 | 观察 bytes 内容寻址存在；事件未提交则按 replay 重新驱动 append；未提交前不构成任何 lifecycle 结论 |
| 7 | `notBefore` timer 丢失/重复触发 | timer queue 是投影：丢失 → 从 ledger `notBefore` 事实重建；重复触发 → 派发受 attempt 状态 + current-ledger recheck + 同 `commandId` 幂等三重守卫，不产生第二次消费或第二个效果 |
| 8 | snapshot 缺失/落后 | full replay 重建；snapshot 与 replay 分歧本身 fail closed（既有 `requireSnapshotMatchesReplay` 语义保持） |
| 9 | journal tail truncated | 未提交后缀整体作废；以最后已提交序列为准；`started` 无 completion 的 attempt 按 ambiguous 对账收敛 |
| 10 | 双 driver/lease 竞争 | Run lease + expectedSequence/CAS + command idempotency 收敛：CAS 败者不追加任何事实；旧 owner/旧 generation 的远端结果只作诊断或 quarantine，永不作为权威 |

**full replay 不变量**：仅凭 append-only journal（含 retry ledger 条目）与内容寻址的 intent/receipt/reconcile records，必须重建——retry `budgetUsed`、next `notBefore`、in-flight ambiguous 集合、authorization lineage 与最终 lifecycle。snapshot、timer、backend 运行态的丢失或漂移不得改变上述任何结论。

## 十、doctor 与 observability

### typed events（封闭集合）

retry 的 schedule/start/failure/reconcile/completed/budget-exhausted 必须写 typed 条目，不得只写 stderr 或覆盖 `publication-error.json`（后者保持为非权威的可覆盖诊断缓存）：

| 条目类型 | 必带 payload（封闭最小集） |
| --- | --- |
| `publication.retry.scheduled` | generation、operation、operationClass、attemptOrdinal、commandId、idempotencyKey、requestDigest、notBefore、budgetLimit、budgetUsed |
| `publication.retry.started` | generation、operation、attemptOrdinal、commandId、startedAt 事实 |
| `publication.retry.failed` | generation、operation、attemptOrdinal、commandId、category、retryDisposition、reasonCode、providerExitClass、failureDigest |
| `publication.retry.reconciled` | generation、operation、attemptOrdinal、commandId、reconcile 结果（applied/not-applied/conflict/unknown）、reconcileDigest |
| `publication.retry.completed` | generation、operation、attemptOrdinal、commandId、outcome、receiptDigest/reconcileDigest |
| `publication.retry.terminal` | generation、operation、reasonCode、终局 digest 绑定（含 Outcome/diagnostic evidence 引用） |

### doctor 检查面（只从 ledger 只读推导）

orphan in-flight（`started` 无 completion 且未被标记对账中）、逾期 `notBefore`（到期未派发且超出宽限）、dead driver ownership、预算计数一致性（replay 派生 vs 投影）、authorization lineage 完整性、`applied` attempt 缺 receipt/reconcile、snapshot/timer 投影漂移。

### 安全 repair 边界（冻结）

doctor 的安全 repair 只能：重建投影（snapshot/timer queue/supervisor view）、对**已提交**的同一 `commandId` 重新投递。ambiguous effect 必须先 reconcile；**不得**伪造 receipt、approval、PublicationRecord，不得直接改写权威状态文件，不得追加任何本应由 reducer 派生的事实。

### 诊断码与 operator action（封闭表）

| 诊断码 | 含义 | operator action |
| --- | --- | --- |
| `PUBRETRY_ORPHAN_INFLIGHT` | `started` 尝试无终局记录 | 触发对应 operation 的 observe/reconcile；禁止不经对账直接重发 effect |
| `PUBRETRY_NOTBEFORE_OVERDUE` | `scheduled` 尝试逾期未派发 | 检查 timer 投影；重建投影后由 Core 正常派发；ledger 事实不改 |
| `PUBRETRY_DEAD_DRIVER_OWNERSHIP` | driver 死亡但仍持有 ownership | 按 Run lease 语义接管；旧 owner 结果仅诊断/quarantine |
| `PUBRETRY_BUDGET_DRIFT` | 预算投影与 replay 派生不一致 | 丢弃投影，以 replay 派生值为准 |
| `PUBRETRY_AUTHORIZATION_LINEAGE_BROKEN` | authorization 绑定链断裂/过期/被撤销 | fail closed；如业务需要，经既有审批路径人工签发新 gate/approval/authorization |
| `PUBRETRY_MISSING_RECEIPT` | `applied` 尝试缺 receipt 绑定 | 按对账身份重新 reconcile；禁止伪造 receipt |
| `PUBRETRY_MISSING_RECONCILE` | ambiguous 尝试缺 reconcile 记录 | 执行 typed reconcile 后按结果收敛 |
| `PUBRETRY_PROJECTION_DRIFT` | snapshot/timer 投影与 ledger 分歧 | full replay 重建投影；分歧本身 fail closed 留证 |

## 十一、不变量汇总（不得弱化）

本 ADR 的实现与后续修订必须保持：Worker/Verifier/Publisher 分权（ADR 0003）；human publish approval 的精确绑定与不可绕过（ADR 0010）；ReviewDecision 精确证据绑定；append-only journal 与 intent-first（ADR 0007/0019）；Draft-only、merge-never；fencing 与 expectedSequence/CAS（ADR 0018）；current-ledger recheck 先于一切派发；fail-closed 语义。retry authority 唯一且属于 Core：Publisher、DurableExecutionEngine、supervisor、timer queue、snapshot、doctor 与进程状态都不得被实现或描述为 retry authority；transport/activity retry 与 Core publication retry 不得相乘。

## 十二、Consequences

- publication 路径的瞬态失败、ambiguous/lost response 与 driver 死亡获得可 replay 的 typed 承载：恢复不再依赖进程存活、退出码或自由文本；
- CI_PENDING 死角在契约层关闭：checks 再观察成为继承 authorization lineage 的只读 continuation，既不破坏「`CI_PENDING` 不签发 publish approval」的既有门禁，也不要求操作者绕过公开 CLI；
- 代价：新增一等权威账本与其 Schema/catalog/semantic validation、Core timer/outbox 集成、Publisher typed 分类映射与 CLI 路由改造；全部属后续 implementation slices；
- 本 ADR 不取代 ADR 0019 的通用 SideEffect append-only 对账与补偿语义——它只是 publication Port/profile 的 typed durable retry specialization；不改变 ADR 0026 的 receipt/reconcile 记录结构、ADR 0028 的 ciDeadline/completedAt 裁决、ADR 0029 的 abort 出口，也不替 ADR 0030 定义 CI failure findings/rework 注入；不新增 merge/ready/release/deploy 权限。

## 十三、future implementation slices（只提议，不实现）

建议顺序（每个 slice 独立验收，前序未接受不开后序）：

1. **Schema/catalog/semantic validation**：`PublicationRetryLedger`/`PublicationOperationAttempt`/`PublicationFailure` Schema（Draft 2020-12，`additionalProperties: false`，含 happy-path 与 invalid fixture）、`internal/domain` kind 与 catalog descriptor、跨记录绑定 semantic validator、publication 专用预算的 TaskSpec/Policy schema 扩展；
2. **Core ledger/reducer/outbox/timer**：账本事务（原子预算消费 + `notBefore`）、attempt 状态机 reducer、确定性退避、current-ledger recheck、outbox 派发守卫；
3. **Publisher typed mapper 与 git/gh classification**：git/gh 退出与错误到 `providerExitClass`/`reasonCode`/`category` 的 typed 映射、receipt/reconcile binding 产出、`publication-error.json` 降级为非权威诊断缓存；
4. **CLI approval routing 与 CI continuation**：`task publish`/`task accept` 对 typed continuation 的路由识别（先识别，后门禁）、PublicationAuthorization 物化与世代继承；
5. **supervisor/doctor/replay 集成**：dead driver ownership 的 ledger 视图、诊断码、安全 repair 边界、full replay 不变量校验；
6. **fake/live conformance 与 crash injection**：fake SCM 全矩阵 conformance、crash injection（第九节十场景）、双 driver fencing 演练。

首切片（本 ADR 所在切片）只新增本文件：不改 ADR 索引、生命周期、恢复文档、代码或 Schema；不接受本 ADR；不宣称 retry 已耐久化或死角已修复。

## 十四、测试矩阵（正反例，后续 slice 实现时必须覆盖）

- **Issue #54 三类原始错误**：`git ls-remote failed: signal: killed`（→ effect 操作为 ambiguous，先对账；read-only 为 transient 有界 retry）；malformed remote branch observation（read-only 有界 retry 与身份冲突 fail-closed 各一例）；publish driver 静默死亡（dead driver ownership + ledger 派生 ambiguous，退出码不得作为 `not-applied` 证据）；
- **typed classification**：category/retryDisposition/reasonCode/providerExitClass 全映射正例 + 不一致组合拒绝负例；
- **single retry authority**：Publisher/engine/supervisor 各自不得产生第二次预算消费或第二个 attempt 条目（乘法 retry 负例必须失败）；
- **预算/退避/notBefore**：原子消费留证、`notBefore` 确定性重放（两进程逐字一致）、budget exhausted 与 deadline exceeded 终局（不制造 PR、不自动 BLOCKED）；
- **lost response 四分支**：applied/not_applied/conflict/unknown 各一例；
- **幂等归并与冲突**：同 key 同 digest replay 归并（零重复效果）；同 key 异 digest conflict（严格零副作用）；
- **approval lineage**：同 generation 继承（`stateSequence` 漂移不失效）；新 reviewRound/新 head/新 intent/新 target 拒绝；stale/revoked/expired 拒绝；
- **CI_PENDING 只读 continuation**：正例矩阵（第七节 #1–#4）与负例矩阵（#5–#9）全部覆盖；`Require(current stateSequence)` 不得先于 continuation 识别执行；
- **跨 generation stale approval**：旧 authorization 用于新 generation 拒绝；
- **双 driver fencing**：CAS 败者零追加；旧 owner 结果 quarantine；
- **full replay/doctor repair**：第九节十场景逐一覆盖；repair 伪造 receipt/approval/PublicationRecord 的负例必须失败；
- **敏感输出脱敏**：credential/绝对路径/原始 stderr 出现在 digest 输入或事件身份的负例必须失败。

## 十五、后续文档同步义务（本切片不执行）

本首切片只新增 `docs/adr/0031-durable-publication-retry.md`，刻意不同步任何共享文档。本 ADR 通过 Review/接受决策后，必须另开互斥 successor 完成以下同步，再开 implementation successor：

1. `docs/adr/README.md`：新增 ADR 0031 索引行与状态记录段落；
2. `docs/task-lifecycle.md`：补充 CI_PENDING 只读 continuation 与 retry ledger 对生命周期观察路径的影响；
3. `docs/failure-and-recovery.md`：补充 typed PublicationFailure、诊断码表与 doctor 安全 repair 边界的操作者视图；
4. `docs/audit-report.md` 及其他受影响的共享文档：按接受后的最终契约文本同步。

在上述同步完成且 implementation slices 逐项接受之前，本 ADR 的状态保持提议（Proposed）。
