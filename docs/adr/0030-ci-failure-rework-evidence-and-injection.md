# ADR 0030：CI 失败 Typed Evidence 与 Rework 注入闭环（CIFailureEvidence → ReviewDecision 绑定 → ci-checks-failed 自环 → Attempt prompt 消费）

- 状态：提议（Proposed）——本 ADR 为草案，须经维护者 ApprovalRecord 接受后生效；接受人与接受时间另行记录，不改写本文。接受前，下文全部契约内容仅为草案提议，不构成已冻结契约、已关闭缺口或任何实现/生产可用/conformance 声明
- 日期：2026-08-14
- 决策来源：公开 [Issue #53](https://github.com/chiga0/marshal-harness/issues/53)——为 CI `checks-failed` → typed evidence → rework findings 注入 → 下一 Attempt prompt 消费冻结一条可恢复、可审计、无双计数的闭环契约。本 ADR 只做契约设计，不实现代码或 Schema
- 关联：[ADR 0004](0004-independent-verification.md)（独立证据具有权威性）、[ADR 0007](0007-intent-first-publication.md)（先记录意图的受控发布与远端对账）、[ADR 0019](0019-deterministic-control-plane-typed-execution-and-goal-admission.md)（确定性控制面与 typed execution）、[ADR 0027](0027-candidate-record-and-verification-write-scope.md)（Candidate 一等不可变记录与 Verification 写作用域）、[ADR 0028](0028-ci-deadline-phased-observation.md)（CI deadline 分阶段观察与可信完成时间裁决）、[任务生命周期](../task-lifecycle.md)、[故障与恢复](../failure-and-recovery.md)

## 上下文

### 问题陈述（current behavior）

发布后 CI 失败是当前生命周期中唯一一条「进入了 `REWORK_REQUESTED` 却没有权威证据身份」的 rework 入口。复现链如下（全部为基线 commit `981b53d` 上的既有行为）：

1. `publication.completed` 已记录，PublicationRecord 经 frozen digest 锚定，Run 位于 `CI_PENDING`；
2. RemoteCheckObserver 产出 `status=fail` 的 RemoteCheckRecord（schema-valid，逐字段绑定 `taskId`、`runId`、`repositoryId`、`requestId`、`headSha`）；
3. `internal/publication/checks.go` 的 fail 分支只把 `headSha` 写入 `publication.checks-failed` 事件并转换 `CI_PENDING → REWORK_REQUESTED`；失败的 RemoteCheckRecord 仅落在可被下一次观察覆盖的 `remote-check-record.json`，没有任何 digest 进入事件；
4. execution 的 rework lineage（`resolveReworkOrigin` 的 `publication.checks-failed` 分支）对 CI origin 固定返回**空 findings**——下一 Attempt 的 `WorkerRequest.reviewFindings` 与 worker prompt 得不到任何 `requiredOutcome`；
5. `marshal task review` 只接受 `REVIEW_PENDING`（`cli.go` 状态门禁），`REWORK_REQUESTED` 的 CI origin 没有任何 ReviewPacket/ReviewDecision 入口。

结果：既无法把失败检查的权威证据绑定到一个新的 ReviewDecision，也无法把精确的 `requiredOutcome` 投影给下一 Attempt；rework 在「无证据、无 findings、无决策留痕」下进行，审计链在发布世代边界断裂。

此外，既有 fail 分支的预算守卫只检查 `ReworkRoundsUsed < budgets.maxReworkRounds`，不检查 `AttemptsUsed < budgets.maxAttempts`，可能进入一个没有任何 Attempt 余额可执行的 `REWORK_REQUESTED`。

缺失的不是数据可得性——RemoteCheckRecord 已经携带全部失败事实——而是承载这些事实的 typed 契约。本 ADR 给出该契约的草案（接受后冻结）。

### 权威性分工（不变量前提）

CI Provider（GitHub）与 Publisher 只报告 **typed facts**：哪些 required check 以什么状态结束、绑定哪个 head。最终 findings、`requiredOutcome` 与 rework admission 一律由 Marshal Core/Lead 经 ReviewDecision 决定。Provider/Publisher 不产生 finding、不宣布 ReviewDecision、不裁决 rework 是否成立；Worker 不生成自己的权威 CI findings。本 ADR 的全部设计以该分工为前提。

### 已否决的前置方案

上一轮实现尝试（经 Review 正式 REJECTED，公开背景见 Issue #53）试图只在 CLI/reducer 内允许 `REWORK_REQUESTED` 自环，但没有冻结 typed CI evidence、RemoteCheckRecord 摘要绑定、execution lineage 消费与 attempt budget 终态，且双计数/重放语义不明确。本 ADR 不复制该补丁。

## 决策

核心方向：把 CI 失败闭环拆成四个冻结阶段，每一段都有封闭的字段表、digest 公式、actor 归属与 fail-closed 语义：

1. **typed evidence**：publisher 观察事务产出内容寻址的失败 RemoteCheckRecord 不可变 bytes 与一等不可变记录 `CIFailureEvidence`，并把两者的 canonical digest 写入 origin 事件；
2. **decision binding**：`marshal task review` 新增 CI 入口——在 `REWORK_REQUESTED` 且存在唯一未消费的 CI origin 时，生成现有 ReviewPacket 的 typed CI 扩展（不伪造新的 Verification），导入绑定该 packet 的 ReviewDecision；
3. **named self-loop**：`review.rework` 事件新增 `ci-checks-failed` origin（`REWORK_REQUESTED → REWORK_REQUESTED` 的唯一命名例外），消费 origin、绑定 Decision，不触碰任何 counter；
4. **execution consumption**：`task run` 对未消费 CI origin 在 Probe 前 fail closed；自环成功后沿相邻 journal lineage 加载该 reviewRound 的 Decision，把 `blockingFindings` 精确投影到 `WorkerRequest.reviewFindings` 与 worker prompt。

预算终态独立冻结：进入 `REWORK_REQUESTED` 前同时检查两个预算守卫，任一耗尽走 `CI_PENDING → REJECTED` 的封闭终态，绝不进入不可执行的 rework 状态。

### 词汇与命名（封闭集合）

| 类别 | 名称 | 说明 |
| --- | --- | --- |
| 记录 kind | `CIFailureEvidence` | 名称固定，一等不可变权威记录 |
| 事件类型（新增） | `publication.checks-rework-budget-exhausted` | CI 失败但预算耗尽的终态事件 |
| 事件类型（复用） | `publication.checks-failed`、`review.rework` | 既有事件类型；payload 与转换语义按本 ADR 扩展 |
| origin 种类 | `normal-review`（既有，unnamed，语义不变）、`ci-checks-failed`（新增） | `review.rework` 的两类 origin；`originKind` 只在 CI 自环 payload 中显式出现 |
| terminalReason（封闭） | `ci-rework-attempt-budget-exhausted`、`ci-rework-round-budget-exhausted` | 仅用于 `publication.checks-rework-budget-exhausted` |
| 原因码（扩展 ADR 0028 封闭原因码集合） | `ci-evidence-admission-rejected` | fail 路径证据接纳被拒时，既有 `publication.blocked` 事件 `error` 字段携带的原因码（见第二节「接纳拒绝 typed 事务」）；依据 ADR 0028「新增原因码只能经后续修订或新 ADR 扩展该封闭集合」条款由本 ADR 提议扩展——不是新状态、新事件类型或新 sentinel |
| 摘要名 | `remoteCheckRecordDigest`、`publicationDigest`、`ciFailureEvidenceDigest` | 均为 `sha256:<64 hex>` canonical digest |

除上表外本 ADR 不引入任何新事件类型、新状态或新 origin 种类；不开放通用 same-state transition。

## 一、CIFailureEvidence 一等不可变记录

### 封闭字段表

`apiVersion=marshal.dev/v1alpha1`，`kind=CIFailureEvidence`，`additionalProperties: false`：

| 字段 | 类型与约束 | 语义 |
| --- | --- | --- |
| `apiVersion` | const `marshal.dev/v1alpha1` | 固定 |
| `kind` | const `CIFailureEvidence` | 固定 |
| `taskId` | id pattern（与既有 `$defs.id` 一致） | 必须等于 RunState/TaskSpec 身份 |
| `runId` | id pattern | 必须等于 RunState 身份 |
| `specDigest` | `sha256:<64 hex>` | 冻结 TaskSpec digest，必须等于 RunState.specDigest |
| `publicationDigest` | `sha256:<64 hex>` | 当前世代 PublicationRecord bytes 的 canonical digest，必须等于 `publication.completed` frozen digest |
| `remoteCheckRecordDigest` | `sha256:<64 hex>` | 失败 RemoteCheckRecord 不可变 bytes 的 canonical digest |
| `repositoryId` | string，1..256 | 逐字段等于 PublicationRecord.`repository.id` |
| `requestId` | string，1..256 | 逐字段等于 PublicationRecord.`request.id` |
| `headSha` | `^[0-9a-f]{40,64}$` | 逐字段等于 PublicationRecord.`headSha` 与 RunState publication head |
| `originEventType` | 封闭枚举：`publication.checks-failed` \| `publication.checks-rework-budget-exhausted` | 本观察事务实际追加的 CI failure origin 事件类型 |
| `originEventId` | event id | origin 事件的 `eventId`（在 journal append 之前生成并绑定） |
| `originSequence` | integer ≥ 1 | origin 事件的 `sequence`（等于观察时刻的 `state.Sequence + 1`） |
| `failedRequiredChecks` | array，1..128 项，按 `name` 升序（UTF-8 byte 序），项为 `{name: string 1..512, status: const "fail"}`——`status` 固定为 const `fail`，不允许完整状态枚举中的其他取值（该数组按定义只包含 required 且失败的集合） | 从 schema-valid checks 数组确定性筛选的 `required=true && status="fail"` 集合；不得为空 |
| `observedAt` | RFC 3339 date-time（UTC） | 逐字段复制 RemoteCheckRecord.`observedAt`（Marshal 观察时刻，仅留证，不参与任何 deadline 裁决——deadline 语义属 ADR 0028） |

记录自身不含 digest 字段；`ciFailureEvidenceDigest` 定义为记录 bytes 的 canonical digest（见「canonical digest 公式」）。

`failedRequiredChecks` 语义校验规则（semantic validator，计划演进点）：必须从 evidence 绑定的内容寻址 RemoteCheckRecord bytes 重算 `required=true && status="fail"` 集合，按 `name` 升序稳定排序后，与 `failedRequiredChecks` 逐字相等——逐项 `name` 相等、`status` 恒为 `fail`、项数一致、顺序一致。替换（`name` 或项内容被改写）、漏项（缺失任一失败 required check）、多项（混入非 required、非 fail 或重复项）的负测 fixture 必须全部失败。

### 禁止进入证据的内容

- 自由文本日志、CI 输出摘录、注解；
- check `url` 字段或任何 URL 内容（URL 不作为权威事实，也不被 Marshal 抓取）；
- Provider 自我裁决（conclusion 之外的推断、「责任归属」判断）；
- Worker 自报内容或任何 Worker 侧声明。

未知、缺失、重复或身份不一致的输入一律 fail closed（见「失败观察的接纳规则」），不做推定、不做部分接纳。

### 生产者与消费者

| 角色 | 身份 | 行为 |
| --- | --- | --- |
| 生产者（唯一） | Marshal Core 的 publication 观察路径（`ObserveChecks` fail 分支），origin 事件 actor 固定 `publisher/marshal-github-publisher` | 在同一观察事务内构造并内容寻址持久化；Provider 只提供 typed facts，不是证据生产者 |
| 消费者：CI review packet 构造 | `marshal task review` CI 入口 | 作为 typed CI 扩展的输入引用与 digest 来源 |
| 消费者：CI 自环导入 | `marshal task review` decision import | 接纳校验与 `review.rework` payload 绑定 |
| 消费者：execution lineage | `task run` admission guard | 校验 self-loop 绑定链的完整性（经 digest 链，不重读 Provider） |
| 消费者：终态 Outcome | 预算耗尽终态路径 | `outcome.json`/`result.md` 绑定 `ciFailureEvidenceDigest` |
| 消费者：doctor | `marshal doctor` | 只读校验存在性与 digest 一致性；永不修复、永不重建 |

### 持久化、大小与排序约束

- 位置：`runs/<runId>/ci-evidence/<hex64>.json`，`<hex64>` 是 `ciFailureEvidenceDigest` 去掉 `sha256:` 前缀的 64 位小写十六进制；全部 digest 引用保持完整 `sha256:<hex64>` 形式；
- 写入语义：digest-verified **put-if-absent**——目标存在且 bytes digest 相同则幂等成功，存在但 digest 不同则 fail closed；永不覆盖、永不原地改写、永不删除；
- 大小：单记录 ≤ 64 KiB；`failedRequiredChecks` ≤ 128 项；超限即拒绝构造（不截断、不降级）；
- 排序：`failedRequiredChecks` 恒按 `name` 升序，保证相同事实集合的 canonical bytes 唯一；
- 归档：发布世代归档（ADR 0007 的 `publications/` 归档）必须包含该世代引用的内容寻址 check record 与 evidence 拷贝，归档后世代自包含。

### Schema 与 catalog 演进点（计划，不在本 ADR 实施）

- 新增 `schemas/ci-failure-evidence.schema.json`（Draft 2020-12，`additionalProperties: false`，含 happy-path 与 invalid fixture）；
- `internal/domain` 新增 `KindCIFailureEvidence` 与对应 Go 类型；
- `internal/contract` catalog 新增 `Descriptor{Name: "ci-failure-evidence", Kind: KindCIFailureEvidence}`，语义校验器新增跨记录绑定规则（evidence ↔ PublicationRecord ↔ RemoteCheckRecord ↔ journal origin）；
- `apiVersion` 保持 `marshal.dev/v1alpha1`。

## 二、RemoteCheckRecord 绑定与 fail 路径接纳规则

### 逐字段绑定核对

fail 路径接纳前，必须逐字段核对当前 PublicationRecord/RunState 与 RemoteCheckRecord：

| RemoteCheckRecord 字段 | 必须等于 |
| --- | --- |
| `taskId` | RunState.taskId |
| `runId` | RunState.runId |
| `repositoryId` | PublicationRecord.`repository.id` |
| `requestId` | PublicationRecord.`request.id` |
| `headSha` | PublicationRecord.`headSha`（= RunState publication head） |

另需：PublicationRecord 自身通过 frozen digest 对账（既有语义），RemoteCheckRecord 通过 schema 校验。

### 接纳顺序（冻结）

1. Run 位于 `CI_PENDING`，持 run lease；PublicationRecord frozen digest 对账通过；
2. ADR 0028 的 ciDeadline 门禁（既有语义，本 ADR 不改变其先后位置与裁决）；
3. observer 返回 schema-valid RemoteCheckRecord；
4. 逐字段身份绑定核对（上表）；
5. `status` 必须精确为 `fail`；
6. 从 schema-valid `checks` 数组确定性筛选 `required=true && status="fail"`，按 `name` 升序排序；checks 数组内出现重复 `name`（duplicate check identity）→ 拒绝；筛选结果为空 → 拒绝；
7. 双预算守卫裁决（只读，见「预算守卫与终态」）：先裁决 origin 事件类型与终态分支——双守卫通过 → origin 事件类型为 `publication.checks-failed`；任一耗尽 → origin 事件类型为 `publication.checks-rework-budget-exhausted` 并按优先级裁决 `terminalReason`。`originEventType` 是 CIFailureEvidence 的必填字段，必须在构造 evidence 之前完成裁决；
8. 预生成 origin 事件身份：`eventId`（新 event id）与 `sequence`（= replay 派生的 `state.Sequence + 1`），供 evidence 的 `originEventId`/`originSequence` 绑定；
9. 计算 `remoteCheckRecordDigest = canonical.DigestJSON(observed RemoteCheckRecord bytes)`（先于一切引用它的对象构造），并内容寻址持久化失败观察 bytes（`runs/<runId>/remote-checks/<hex64>.json`，put-if-absent）；
10. 构造 CIFailureEvidence（`originEventType` 取步骤 7 裁决结果，`originEventId`/`originSequence` 取步骤 8 预生成身份，`failedRequiredChecks` 取步骤 6 的稳定排序结果）并内容寻址持久化（put-if-absent）；
11. 经 `runstore.Append`（expectedSequence CAS）追加 origin 事件，payload 同时携带 `headSha`、`remoteCheckRecordDigest`、`ciFailureEvidenceDigest`（终态分支另携带 `terminalReason`，且先经 PrepareOutcome、append 后 Commit，见第三节）；
12. CAS 失败处置：expectedSequence 不匹配说明 journal 已被并发推进，本次构造的 evidence 与其绑定的 `originEventId`/`originSequence`/`ciFailureEvidenceDigest` 全部作废；回到步骤 1 重读状态、重新裁决、重新预生成身份并重建 evidence。内容寻址孤儿 bytes 无害，put-if-absent 幂等，不作废任何已提交事实。

### 不得进入 fail 证据路径的情形（全部 fail closed）

| 情形 | 处置 |
| --- | --- |
| `status=pending` | 保持 `CI_PENDING`（既有语义，不产生证据） |
| `status=external-failure` | `BLOCKED`（既有语义：外部失败） |
| schema-invalid RemoteCheckRecord | `BLOCKED`（既有语义） |
| 任一身份字段不匹配 | `BLOCKED`，经下文「接纳拒绝 typed 事务」，`error` 原因码 `ci-evidence-admission-rejected` |
| 重复 check identity（同一 `name` 出现多次） | 同上 |
| `required=true && status="fail"` 集合为空（非 required 失败不构成本路径输入） | 同上 |
| digest 或 current-ledger 不匹配（frozen publication digest、specDigest、journal authority 任一不符） | fail closed（按既有 authority/frozen 检查路径） |

`BLOCKED` 后不因后续观察自动复活（与 ADR 0028 一致）；恢复只能经既有 typed reconciliation 或操作者显式 abort + 新 Run。

### 不可变 bytes 的内容寻址留存

失败观察的权威 bytes 是内容寻址的 `remote-checks/<hex64>.json`，不是 `remote-check-record.json`。`remote-check-record.json` 保留为最近一次观察的缓存语义；fail 路径、CI review、execution lineage、doctor 一律只信任内容寻址 bytes。后续观察可以产生新的内容寻址记录，但永不覆盖既有记录。

### 接纳拒绝 typed 事务（冻结）

证据接纳被拒（上表三种情形）不引入新事件类型、新转换行或新状态：复用既有已冻结的 `publication.blocked` typed failure，与基线 publication 安全门禁的 BLOCKED 路径同构。`ci-evidence-admission-rejected` 是对 ADR 0028 封闭原因码集合的后续扩展——依据 ADR 0028「新增原因码只能经本 ADR 后续修订或新 ADR 扩展该封闭集合，不得以自由文本注入」条款，由本 ADR 提议将该原因码纳入集合；ADR 0028 的 ciDeadline/completedAt 裁决与其余原因码语义不被改变。

| 要素 | 冻结值 |
| --- | --- |
| 事件类型 | `publication.blocked`（复用，不新增） |
| 转换 | `CI_PENDING → BLOCKED`（既有转换表行，不新增） |
| actor | `publisher/marshal-github-publisher`（producer-authority 表既有登记，replay 强制校验） |
| payload（封闭） | `error` = const `ci-evidence-admission-rejected`；`terminalReason` = const `publication safety gate failed`（复用既有值） |
| guard | LeaseHeld + PublicationCurrent（与既有 block 路径一致） |
| counter | 全部不变（`BLOCKED` 不触碰任何 counter） |
| Outcome | TerminalState=`BLOCKED`，Verdict=`blocked`，FinalReviewRound=replay 派生的 `state.ReviewRound`，FinalReviewDigest/FinalEvidenceDigest 继承失败发布世代绑定的 round-bound accept decision 与其 evidenceDigest（与既有 frozenEvidence 绑定语义一致），FindingCount=0，Summary 为确定性文本；PrepareOutcome no-replace |
| 事务顺序 | PrepareOutcome → `runstore.Append`（expectedSequence CAS）→ Commit → WriteSnapshot，与既有 `publication.blocked` 路径一致 |
| 崩溃恢复 | journal 未 append 则整体未发生；孤儿 pending outcome 由下一次 prepare 清理；final outcome 存在即拒绝重写；snapshot 落后由 full replay 重建 |

接纳拒绝前不产生任何内容寻址 bytes、不构造 evidence、不预留任何 counter；已 `BLOCKED` 的 Run 不因后续观察自动复活（ADR 0028 既有语义），恢复只能经既有 typed reconciliation 或操作者显式 abort + 新 Run。

## 三、预算守卫与终态

### 双守卫（追加 `publication.checks-failed` 前，原子判定）

| 守卫 | 条件 |
| --- | --- |
| rework round 守卫 | `ReworkRoundsUsed < budgets.maxReworkRounds` |
| attempt 守卫 | `AttemptsUsed < budgets.maxAttempts` |

两者同时通过 → 追加 `publication.checks-failed`（`CI_PENDING → REWORK_REQUESTED`），并在同一 reducer 步骤原子完成 counter 预留（见「counter 语义」）。任一不通过 → 走终态路径。

### 终态路径（封闭）

- 目标转换：`CI_PENDING → REJECTED`（生命周期 allowed map 新增该行，且仅能由本事件触发）；
- 事件类型固定：`publication.checks-rework-budget-exhausted`；
- actor 固定：`publisher/marshal-github-publisher`；
- payload 封闭字段：`headSha`、`remoteCheckRecordDigest`、`ciFailureEvidenceDigest`、`terminalReason`；
- `terminalReason` 封闭枚举与优先级：
  - `AttemptsUsed >= budgets.maxAttempts`（无论 rework round 是否同时耗尽）→ `ci-rework-attempt-budget-exhausted`（attempt sentinel 优先）；
  - 否则（仅 rework round 耗尽）→ `ci-rework-round-budget-exhausted`；
- 终态必须写 `outcome.json`、`outcome.md`、`result.md`（沿用 PrepareOutcome 的 no-replace 语义），并在其中绑定 `publicationDigest`、`remoteCheckRecordDigest`、`ciFailureEvidenceDigest` 三个摘要（Outcome schema 相应新增可选字段，为演进点，不改变历史 Outcome）；
- 终态判定前 evidence 与内容寻址 bytes 已经持久化——终态 Run 同样保有完整 typed 证据，供审计。

### 预算耗尽终态 Outcome 投影（冻结）

该路径不产生任何新的 ReviewDecision：Publisher 只追加 typed fact 事件与持久化 typed 证据，不生成、不签发、不导入任何 ReviewDecision。Outcome 必须是 schema-valid 投影，其需要 Decision 来源的字段全部继承自产出失败 head 的发布世代（R_pub = PublicationRecord.`reviewRound`）的既有 accept Decision 与证据，逐字段冻结如下：

| Outcome 字段 | 取值与来源 |
| --- | --- |
| `terminalState` | const `REJECTED` |
| `verdict` | const `reject`——本 ADR 定义的预算耗尽终态投影常量；它只是 Outcome 投影字段，不代表存在任何 reject ReviewDecision，Publisher 也不得据此伪造或导入 Decision |
| `finalReviewRound` | PublicationRecord.`reviewRound`（产出失败 head 的 accept 轮 R_pub；预算耗尽分支不追加 checks-failed、不预留 reviewRound，故 replay 派生的 `state.ReviewRound` 与该值相等） |
| `finalReviewDigest` | PublicationRecord.`reviewDecisionDigest`（round-bound accept decision `decision-R_pub.json` 的 canonical digest，与 `publication.completed` frozen digest 对账一致） |
| `finalEvidenceDigest` | PublicationRecord.`evidenceDigest`（R_pub 的 evidenceDigest） |
| `summary` | 封闭确定性文本：包含 `terminalReason` 与失败 head 身份，不含自由文本日志 |
| `findingCount` | 继承的 accept decision 的 findings 总数（既有公式：blockingFindings + nonBlockingFindings；publish 前置 accept decision 的 blockingFindings 必为空） |
| `publicationDigest`（新增可选字段） | 当前世代 PublicationRecord bytes 的 canonical digest |
| `remoteCheckRecordDigest`（新增可选字段） | 失败观察内容寻址 bytes 的 canonical digest |
| `ciFailureEvidenceDigest`（新增可选字段） | CIFailureEvidence 记录的 canonical digest |

三个新增字段为 Outcome schema 的可选扩展（`additionalProperties: false` 下新增 property，不改既有 `required` 集合与字段约束），使 CI 三摘要进入 canonical Outcome 与 `result.md`（renderOutcome 相应扩展列出三项摘要）；历史 Outcome 不受影响。Outcome 事务沿用 PrepareOutcome 的 no-replace/pending 语义（见第九节 CI-OBS W3/W5）；恢复路径按 journal 绑定重新 stage 并只 commit 一次，不重写既有 final。

不得进入不可执行的 `REWORK_REQUESTED`；不得以自由文本替代封闭 terminalReason；异常或不可判定时 fail closed，不伪造终态。

## 四、CI review：ReviewPacket typed 扩展与 ReviewDecision 绑定

### 状态接纳

`marshal task review` 的接纳集合扩展为：

| 状态 | 入口 |
| --- | --- |
| `REVIEW_PENDING` | normal review（既有语义，逐字不变） |
| `REWORK_REQUESTED` 且最新业务事件是**唯一未消费、target shape** 的 `publication.checks-failed` origin | CI review（本 ADR 新增） |
| 其他任何状态/origin 形状 | fail closed，零副作用 |

### ReviewPacket typed CI 扩展（不伪造新的 Verification）

CI review 不重新执行 Verification、不重新观察 worktree、不生成新的 VerificationReport/ArtifactManifest；它生成**现有 ReviewPacket 的 typed CI 扩展**：

- 以失败 head 的发布世代为锚：`PublicationRecord.reviewRound` 指向产出该 head 的 accept 轮 `R_pub`；packet 构造前必须校验世代三元组一致——`review-packets/packet-%03d.json`（R_pub）存在且 schema-valid、其 `verificationDigest`/`artifactManifestDigest` 与 PublicationRecord 同名字段逐字相等、`decisions/decision-%03d.json`（R_pub）为 `verdict=accept` 且 digest 等于 PublicationRecord.`reviewDecisionDigest`；任一不符 fail closed；
- 新 packet 的 `reviewRound` = checks-failed 预留的 CI reviewRound（= 当时 `state.ReviewRound`）；
- 新 packet 继承 R_pub packet 的 `specDigest`、`baseSha`、`snapshotDigest`、`diffDigest`、`verificationDigest`、`artifactManifestDigest`、`workerResultDigests`、`candidateDigest`、`workerCandidateDigest`（ADR 0027 Candidate 绑定语义原样继承）；
- 新增封闭扩展对象 `ciFailure`：

| 字段 | 语义 |
| --- | --- |
| `ciEvidenceInput` | CIFailureEvidence 输入引用（run 目录内相对路径 `ci-evidence/<hex64>.json`） |
| `ciFailureEvidenceDigest` | 证据记录 canonical digest |
| `remoteCheckRecordDigest` | 失败观察 bytes canonical digest |
| `originEventId` | origin 事件 id |
| `originSequence` | origin 事件 sequence |

packet 落盘沿用 `review-packets/packet-%03d.json`（round-bound，no-replace）与当前指针 `review-packet.json`。

### evidenceDigest 的 canonical identity 扩展

evidenceDigest 继续由 evidence identity 结构 canonical 化后取 digest（既有公式），identity 新增四个 CI 字段（仅 CI packet 出现；legacy packet 的序列化保持字节兼容，与 ADR 0027 的 omitempty 兼容口径一致）：

```
ciFailureEvidenceDigest   // sha256:<64 hex>
remoteCheckRecordDigest   // sha256:<64 hex>
ciOriginEventId           // origin 事件 id
ciOriginSequence          // origin 事件 sequence
```

既有字段（specDigest、patchDigest、verificationDigest、artifactManifestDigest、workerResultDigests、previousBlockingFindings、candidateDigest、workerCandidateDigest）与其顺序语义不变。CI packet 的 evidenceDigest 因此唯一绑定「同一份验证证据 + 同一份 CI 失败证据 + 同一个 origin」。

### ReviewDecision 绑定与注入门禁

ReviewDecision 字段集合不变，继续逐字绑定 `reviewRound`、`reviewPacketDigest`、`verificationDigest`、`artifactManifestDigest`、`evidenceDigest`、`specDigest`、`taskId`、`runId`。CI 导入额外门禁（全部成立才可注入，任一不成立 fail closed、零副作用）：

1. `verdict` 精确为 `rework`——`accept`/`reject`/`blocked`/`no_change` 在 CI 自环入口一律拒绝，不产生任何状态副作用；
2. `reviewRound` 等于预留的 CI reviewRound；
3. `reviewPacketDigest`/`evidenceDigest` 与构造的 CI packet 逐字一致；
4. `blockingFindings` ≥ 1，且每项 `requiredOutcome` 非空（CI 注入的 finding 必须可执行；缺失 `requiredOutcome` 的 finding 不允许进入注入）；
5. origin 未消费、唯一、相邻（见「origin 基数与幂等」）；
6. 预留与预算一致性复核：当前 `state.ReviewRound` 等于 origin 预留的 CI reviewRound（replay 派生，不得取自 snapshot 单独声明），且 `ReworkRoundsUsed <= budgets.maxReworkRounds`、`AttemptsUsed < budgets.maxAttempts` 仍然成立；异常或不可判定时 fail closed，不伪造终态、不追加事件。

## 五、review.rework 双 origin 与命名自环

### 两类 origin

| origin | 转换 | 语义 |
| --- | --- | --- |
| normal-review（既有） | `REVIEW_PENDING → REWORK_REQUESTED` | 逐字不变；payload 为 `verdict=rework`、`decisionDigest`、`evidenceDigest`；不携带 `originKind` |
| ci-checks-failed（新增） | `REWORK_REQUESTED → REWORK_REQUESTED` | 本 ADR 唯一新增的命名例外 |

除该命名例外与既有 `reconciliation.snapshot-repaired` 审计事件外，不开放任何通用 same-state transition。

### ci-checks-failed 自环事件（封闭 payload）

- 事件类型：`review.rework`（复用，不新增事件类型）；
- actor 固定：`system/marshal-review`（producer-authority 表既有登记，replay 强制校验）；
- `attemptId` 必须为空；
- payload 封闭字段：

| 字段 | 值/约束 |
| --- | --- |
| `originKind` | 固定 `ci-checks-failed` |
| `verdict` | 固定 `rework` |
| `decisionDigest` | round-bound ReviewDecision 的 canonical digest |
| `evidenceDigest` | CI packet 的 evidenceDigest |
| `originEventId` | 被消费的 `publication.checks-failed` 事件 id |
| `originSequence` | 被消费的 `publication.checks-failed` 事件 sequence |
| `ciFailureEvidenceDigest` | 与 origin payload 逐字一致 |
| `remoteCheckRecordDigest` | 与 origin payload 逐字一致 |

### typed reducer 语义

生命周期结构校验（`ValidateTransition`）、`runstore.Append`、full Replay、Rebuild/doctor repair 全部走同一 typed reducer：

- allowed map 新增 `REWORK_REQUESTED → REWORK_REQUESTED`，仅对「事件类型 `review.rework` 且 payload `originKind=ci-checks-failed` 且 actor `system/marshal-review` 且 `verdict=rework`」成立；其余 same-state 组合一律拒绝；
- counter 效果按事件类型/origin 分派（见「counter 语义」），不再只看目标状态；
- 禁止任何旁路直接写 `events.jsonl`；禁止以 snapshot 掩盖不可重放事件——snapshot 与 replay 分歧本身即 fail closed（既有 `requireSnapshotMatchesReplay` 语义保持）。

## 六、origin 基数与幂等

- **唯一消费（一对一）**：一个 `publication.checks-failed` origin 与成功的 CI `review.rework` 注入一对一：一个 origin 最多对应一个成功注入。自环成功后状态仍为 `REWORK_REQUESTED`，但最新业务事件已是自环事件；再次导入因 origin 已消费而拒绝；
- **裁决先于副作用**：导入事务先只读解析 journal 唯一键并裁决 replay/conflict（见第九节 CI-REVIEW V2），仅 fresh origin 才进入 prepare/append；同 key 同 digest 零追加返回、同 key 不同 digest conflict 且严格零副作用，由该顺序机械保证；
- **导入唯一键**：`(runId, originEventId, originSequence)`；
- **lost-response 重放**：journal 中已存在同 key 自环且 `decisionDigest` 与本次请求相同 → 返回既有结果（幂等成功），不追加第二条事件、不改写已提交记录；
- **conflict**：同 key 但 `decisionDigest` 不同 → 固定 conflict，零副作用（不追加事件、不落盘记录）；
- **新鲜性与相邻性**：导入前必须证明 origin 是当前 journal 中**最新、相邻、尚未消费**的 CI failure——即最新业务事件、`headSha` 与当前 publication generation 一致、payload digest 与内容寻址记录一致、evidence 的 origin 字段回指该事件；自环事件必须紧随 origin（其间无任何业务事件）；
- **禁止跨世代复用**：不得全局搜索旧 decision，不得跨 publication generation 复用 findings；旧世代的 decision/findings 对当前世代不构成输入（`previousBlockingFindings` 的既有语义不变，仍只取最近一份 decision）。

## 七、counter 语义（逐事件表）

### target 规则

| 事件 | 转换 | AttemptsUsed | OperationalRetriesUsed | ReworkRoundsUsed | ReviewRound |
| --- | --- | --- | --- | --- | --- |
| `worker.started` | READY/REWORK_REQUESTED/RETRY_PENDING → RUNNING | +1 | — | — | — |
| 目标为 RETRY_PENDING 的事件 | RUNNING → RETRY_PENDING | — | +1 | — | — |
| `verification.completed` | VERIFYING → REVIEW_PENDING | — | — | — | +1 |
| `review.rework`（normal-review origin） | REVIEW_PENDING → REWORK_REQUESTED | — | — | +1 | — |
| `publication.checks-failed`（target shape） | CI_PENDING → REWORK_REQUESTED | — | — | **+1** | **+1**（原子预留 CI reviewRound） |
| `review.rework`（originKind=ci-checks-failed） | REWORK_REQUESTED → REWORK_REQUESTED | — | — | — | — |
| `publication.checks-rework-budget-exhausted` | CI_PENDING → REJECTED | — | — | — | — |
| 其余事件（planning/worker.completed/verification 前后/publication 各事件等） | 各自既有转换 | — | — | — | — |

要点：

- `publication.checks-failed` 在通过双守卫后**只增加一次** `ReworkRoundsUsed` 并**原子预留一个新的 CI reviewRound**；
- 其后的自环一对一消费 origin，只绑定 Decision 与消费 origin，**绝不再次递增**（不再次递增）`ReviewRound`、`ReworkRoundsUsed`、`AttemptsUsed`、`OperationalRetriesUsed` 中的任何一个；
- 普通 REVIEW_PENDING reviewRound（`verification.completed` 时 +1）与 normal review.rework 的既有计数逐字不变；
- replay 兼容规则：legacy shape 的 `publication.checks-failed`（payload 无 `ciFailureEvidenceDigest`，仅存于切换前 journal）保持历史 counter 语义（`ReworkRoundsUsed` +1，不预留 ReviewRound），保证历史 journal replay 与其快照一致；切换后新追加的 checks-failed 一律为 target shape。

### 两轮示例（publish → CI fail → rework → republish，无双计数/跳号/覆盖）

预算：`maxAttempts=5`、`maxOperationalRetries=2`、`maxReworkRounds=2`。计数列为事件应用后的值。

| seq | 事件 | 转换 | Attempts | OpRetries | ReworkRounds | ReviewRound | 说明 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| — | CREATED 基线 | — | 0 | 0 | 0 | 0 | planning 事件不触碰 counter |
| 1 | `worker.started`（attempt:1） | READY→RUNNING | 1 | 0 | 0 | 0 | |
| 2 | `worker.completed` | RUNNING→VERIFYING | 1 | 0 | 0 | 0 | |
| 3 | `verification.completed` | VERIFYING→REVIEW_PENDING | 1 | 0 | 0 | 1 | 普通 reviewRound |
| 4 | `review.rework`（normal-review，decision-001） | REVIEW_PENDING→REWORK_REQUESTED | 1 | 0 | 1 | 1 | |
| 5 | `worker.started`（attempt:2） | REWORK_REQUESTED→RUNNING | 2 | 0 | 1 | 1 | 投影 decision-001 findings |
| 6 | `worker.completed` | RUNNING→VERIFYING | 2 | 0 | 1 | 1 | |
| 7 | `verification.completed` | VERIFYING→REVIEW_PENDING | 2 | 0 | 1 | 2 | |
| 8 | `review.accept`（decision-002） | REVIEW_PENDING→PUBLISHING | 2 | 0 | 1 | 2 | |
| 9 | `publication.completed`（g1，headSha=H1，reviewRound=2） | PUBLISHING→PUBLISHED | 2 | 0 | 1 | 2 | |
| 10 | `publication.checks-requested` | PUBLISHED→CI_PENDING | 2 | 0 | 1 | 2 | |
| 11 | `publication.checks-failed`（origin O1，target shape，绑定 D1/E1） | CI_PENDING→REWORK_REQUESTED | 2 | 0 | **2** | **3** | 双守卫通过；一次预留 CI reviewRound，一次 reworkRound |
| 12 | `review.rework`（originKind=ci-checks-failed，消费 O1，decision-003/packet-003） | REWORK_REQUESTED→REWORK_REQUESTED | 2 | 0 | 2 | 3 | **零 counter 变化** |
| 13 | `worker.started`（attempt:3） | REWORK_REQUESTED→RUNNING | 3 | 0 | 2 | 3 | 投影 decision-003 findings（精确一次） |
| 14 | `worker.failed`（可重试，进程崩溃） | RUNNING→RETRY_PENDING | 3 | 1 | 2 | 3 | |
| 15 | `worker.started`（attempt:4） | RETRY_PENDING→RUNNING | 4 | 1 | 2 | 3 | 同一 origin/decision，findings 字节等价 |
| 16 | `worker.completed` | RUNNING→VERIFYING | 4 | 1 | 2 | 3 | |
| 17 | `verification.completed` | VERIFYING→REVIEW_PENDING | 4 | 1 | 2 | 4 | |
| 18 | `review.accept`（decision-004） | REVIEW_PENDING→PUBLISHING | 4 | 1 | 2 | 4 | |
| 19 | `publication.completed`（g2，headSha=H2，previousHeadSha=H1，reviewRound=4） | PUBLISHING→PUBLISHED | 4 | 1 | 2 | 4 | fast-forward，同一 Draft PR |
| 20 | `publication.checks-requested` | PUBLISHED→CI_PENDING | 4 | 1 | 2 | 4 | |
| 21 | `publication.checks-passed` | CI_PENDING→ACCEPTED | 4 | 1 | 2 | 4 | 终态 |

decision 文件序列：`decision-001.json`（round 1，rework）、`decision-002.json`（round 2，accept）、`decision-003.json`（round 3，CI rework）、`decision-004.json`（round 4，accept）——round 号由 ReviewRound 单调派生，PrepareRecords no-replace，无跳号、无覆盖。

**变体 B**（seq 21 改为第二次 CI fail）：守卫判定 `ReworkRoundsUsed=2 == maxReworkRounds` → `publication.checks-rework-budget-exhausted`，`CI_PENDING→REJECTED`，`terminalReason=ci-rework-round-budget-exhausted`，Outcome/result.md 绑定三个 digest。

**变体 C**（fail 时 `AttemptsUsed >= maxAttempts`，或两者同时耗尽）：`terminalReason=ci-rework-attempt-budget-exhausted`（双耗尽时 attempt sentinel 优先）。

### 两次成功 CI rework 轮示例（O1/O2 双 origin，预算充足，无双计数/跳号/覆盖）

预算：`maxAttempts=8`、`maxOperationalRetries=2`、`maxReworkRounds=3`。本示例展示两个不同 origin、不同 Decision 的成功注入、两次 republish 与第三个 publication generation。计数列为事件应用后的值。

| seq | 事件 | 转换 | Attempts | OpRetries | ReworkRounds | ReviewRound | 说明 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | `worker.started`（attempt:1） | READY→RUNNING | 1 | 0 | 0 | 0 | |
| 2 | `worker.completed` | RUNNING→VERIFYING | 1 | 0 | 0 | 0 | |
| 3 | `verification.completed` | VERIFYING→REVIEW_PENDING | 1 | 0 | 0 | 1 | |
| 4 | `review.rework`（normal-review，decision-001） | REVIEW_PENDING→REWORK_REQUESTED | 1 | 0 | 1 | 1 | |
| 5 | `worker.started`（attempt:2） | REWORK_REQUESTED→RUNNING | 2 | 0 | 1 | 1 | |
| 6 | `worker.completed` | RUNNING→VERIFYING | 2 | 0 | 1 | 1 | |
| 7 | `verification.completed` | VERIFYING→REVIEW_PENDING | 2 | 0 | 1 | 2 | |
| 8 | `review.accept`（decision-002） | REVIEW_PENDING→PUBLISHING | 2 | 0 | 1 | 2 | |
| 9 | `publication.completed`（g1，headSha=H1，reviewRound=2） | PUBLISHING→PUBLISHED | 2 | 0 | 1 | 2 | |
| 10 | `publication.checks-requested` | PUBLISHED→CI_PENDING | 2 | 0 | 1 | 2 | |
| 11 | `publication.checks-failed`（origin O1，target shape） | CI_PENDING→REWORK_REQUESTED | 2 | 0 | **2** | **3** | 守卫：1<3 且 2<8 通过；只加一次 reworkRound，原子预留 CI reviewRound |
| 12 | `review.rework`（ci-checks-failed，消费 O1，decision-003/packet-003） | REWORK_REQUESTED→REWORK_REQUESTED | 2 | 0 | 2 | 3 | 一对一消费 O1；零 counter 变化，不再次递增 |
| 13 | `worker.started`（attempt:3） | REWORK_REQUESTED→RUNNING | 3 | 0 | 2 | 3 | 投影 decision-003 findings |
| 14 | `worker.completed` | RUNNING→VERIFYING | 3 | 0 | 2 | 3 | |
| 15 | `verification.completed` | VERIFYING→REVIEW_PENDING | 3 | 0 | 2 | 4 | |
| 16 | `review.accept`（decision-004） | REVIEW_PENDING→PUBLISHING | 3 | 0 | 2 | 4 | |
| 17 | `publication.completed`（g2，headSha=H2，previousHeadSha=H1，reviewRound=4） | PUBLISHING→PUBLISHED | 3 | 0 | 2 | 4 | 第一次 republish（fast-forward，同一 Draft PR） |
| 18 | `publication.checks-requested` | PUBLISHED→CI_PENDING | 3 | 0 | 2 | 4 | |
| 19 | `publication.checks-failed`（origin O2，target shape，与 O1 不同 eventId/sequence/payload digest） | CI_PENDING→REWORK_REQUESTED | 3 | 0 | **3** | **5** | 守卫：2<3 且 3<8 通过 |
| 20 | `review.rework`（ci-checks-failed，消费 O2，decision-005/packet-005） | REWORK_REQUESTED→REWORK_REQUESTED | 3 | 0 | 3 | 5 | 一对一消费 O2；零 counter 变化，不再次递增 |
| 21 | `worker.started`（attempt:4） | REWORK_REQUESTED→RUNNING | 4 | 0 | 3 | 5 | 投影 decision-005 findings |
| 22 | `worker.completed` | RUNNING→VERIFYING | 4 | 0 | 3 | 5 | |
| 23 | `verification.completed` | VERIFYING→REVIEW_PENDING | 4 | 0 | 3 | 6 | |
| 24 | `review.accept`（decision-006） | REVIEW_PENDING→PUBLISHING | 4 | 0 | 3 | 6 | |
| 25 | `publication.completed`（g3，headSha=H3，previousHeadSha=H2，reviewRound=6） | PUBLISHING→PUBLISHED | 4 | 0 | 3 | 6 | 第二次 republish，第三个 publication generation |
| 26 | `publication.checks-requested` | PUBLISHED→CI_PENDING | 4 | 0 | 3 | 6 | |
| 27 | `publication.checks-passed` | CI_PENDING→ACCEPTED | 4 | 0 | 3 | 6 | 终态 |

断言：两次成功注入分别一对一消费 O1/O2；decision 文件序列 `decision-001.json`…`decision-006.json` 由 ReviewRound 单调派生，PrepareRecords no-replace，无跳号、无覆盖；`ReworkRoundsUsed` 只在 seq 11/19 各加一次（终值 3 = maxReworkRounds，此后若再 CI fail 走预算耗尽终态）；`ReviewRound` 只由 `verification.completed`（seq 3/7/15/23）与 checks-failed 预留（seq 11/19）递增；两个自环（seq 12/20）零 counter 变化；`AttemptsUsed` 只由 `worker.started` 递增（终值 4/8）；`OperationalRetriesUsed` 全程为 0（本示例未发生可重试失败）。若 seq 27 变为第三次 CI fail，rework round 守卫失败（3 ≥ 3），进入 `publication.checks-rework-budget-exhausted` 终态（`terminalReason=ci-rework-round-budget-exhausted`），Outcome 投影见第三节。

## 八、execution prompt 消费

### 未注入拒绝（fail closed，零副作用）

`task run` 在 Probe、Attempt 创建与任何副作用之前执行 admission guard：`REWORK_REQUESTED` 且最新 CI origin（target shape）**尚无唯一匹配自环**时，一律拒绝执行；不得注入空 findings。legacy shape 的 CI origin 同样拒绝（见「兼容与迁移」）。拒绝发生在现有 admission 层位置（`loadReviewFindings` 守卫点），不产生 Attempt 记录、不触碰 worktree。

### lineage 解析与投影

自环成功后，execution 沿相邻 journal lineage（既有 `resolveRetryLineage` 的相邻行走语义，不全局搜索）加载该 reviewRound 的 Decision：

1. `REWORK_REQUESTED` 的最新业务事件必须是自环事件（`review.rework` + `originKind=ci-checks-failed`）；
2. 校验自环 payload：`decisionDigest`/`evidenceDigest` 为 canonical sha256 形式，`originEventId`/`originSequence` 回指相邻的 target shape checks-failed origin；
3. 加载 round-bound `decisions/decision-%03d.json`（round = replay 派生的预留 CI reviewRound）：schema-valid、canonical `decisionDigest` 匹配、`taskId`/`runId`/`specDigest`/`reviewRound`/`verdict` 逐项一致；
4. 校验证据绑定链：origin payload 的 `remoteCheckRecordDigest`/`ciFailureEvidenceDigest` 与内容寻址 bytes 一致，evidence 的 origin 字段回指该 origin 事件，evidence 身份字段与当前 publication generation 一致；
5. 投影：把 `blockingFindings` 按 decision bytes 中的稳定顺序，逐项精确投影 `id`、`severity`、`description`、`requiredOutcome` 四个 key 到 `WorkerRequest.reviewFindings` 与 worker prompt；不得增删 key、不得改写/概括/翻译字段值。

### 投影次数与字节等价

- 每个新 rework Attempt 投影一次；同一 Attempt 内不重复投影；
- 同一 Attempt 之后的 operational retry（RETRY_PENDING → RUNNING）必须解析到**同一 origin/Decision**，得到**字节等价**的 findings（findings 由不可变 decision bytes 确定性派生，序列化逐字一致）；
- 直到新的 review origin（新一轮 normal review.rework 或新一代 checks-failed + 自环）取代它之前，lineage 不得解析到其他 Decision。

## 九、canonical digest、事务顺序与崩溃恢复

### canonical digest 公式汇总

所有 digest 均为 `sha256:<64 hex>`，经 Marshal canonical JSON（RFC 8785/JCS 语义，`canonical.DigestJSON`）计算：

| digest | 输入 |
| --- | --- |
| `remoteCheckRecordDigest` | observer 返回的 RemoteCheckRecord bytes（canonical 化后取 digest） |
| `publicationDigest` | 当前世代 `publication-record.json` bytes |
| `ciFailureEvidenceDigest` | CIFailureEvidence 记录 bytes |
| `evidenceDigest` | evidence identity 结构的 JSON 序列化（CI packet 含四个新字段） |
| `reviewPacketDigest` | review-packet bytes |
| `decisionDigest` | 导入的 ReviewDecision bytes（持久化与引用使用同一 bytes，禁止重新序列化） |

禁止进入任何 identity 的内容：原始格式化 bytes（digest 只对 canonical bytes 计算）、map 迭代顺序、本机绝对路径。

### 事务顺序与崩溃窗口

**CI-OBS（publisher 观察事务，checks-failed 或预算耗尽终态）**：

| 步骤 | 内容 | 崩溃恢复规则 |
| --- | --- | --- |
| W1 | 校验与派生（状态/lease/绑定/筛选/双预算守卫裁决/预生成 origin eventId·sequence） | 无副作用；重试重新观察 |
| W2 | 内容寻址 put-if-absent（`remote-checks/`、`ci-evidence/`；evidence 绑定 W1 预生成的 origin 身份） | 孤儿不可变 bytes 无害；同内容重试幂等 |
| W3 | （仅终态分支）PrepareOutcome pending（no-replace） | 孤儿 pending 由下一次 prepare 清理；final 存在即拒绝重写 |
| W4 | `runstore.Append` origin 事件（expectedSequence CAS） | journal 未推进则整体未发生；CAS 失败 → 本次 evidence 的 origin 绑定作废，回到 W1 重新派生与重建（内容寻址孤儿 bytes 无害，put-if-absent 幂等） |
| W5 | （仅终态分支）Commit outcome | append 后 commit 前崩溃：journal 已权威，恢复路径按 journal 绑定重新 stage 并 commit 一次；final no-replace 保证不重写 |
| W6 | WriteSnapshot | 快照落后/缺失：full replay 重建，doctor 记 `reconciliation.snapshot-repaired` 审计 |

**CI-REVIEW（CI 自环导入事务）**：

| 步骤 | 内容 | 崩溃恢复规则 |
| --- | --- | --- |
| V1 | 接纳校验与 CI packet 构造（只读） | 无副作用 |
| V2 | journal 唯一键只读解析与裁决（先于一切写入）：解析 `(runId, originEventId, originSequence)`——journal 已存在同 key 自环且 `decisionDigest` 相同 → 幂等返回既有结果，零追加、零记录写入；`decisionDigest` 不同 → conflict，零副作用终止；仅当不存在同 key 自环（fresh origin）才进入 V3 | replay/conflict 在任何 prepare/append 之前裁决：同 digest 零追加返回、不同 digest conflict 且严格零副作用由该顺序机械保证 |
| V3 | PrepareRecords pending（decision-NNN/packet-NNN，no-replace） | 孤儿 pending 由下一次导入清理；lost-response 重放在 V2 已返回，不会在此撞 no-replace |
| V4 | `runstore.Append` 自环事件（expectedSequence CAS） | CAS 失败（并发 append 推进 sequence）→ abort pending，回到 V2 重新只读裁决（按 replay/conflict/fresh 规则继续）；未 append 则记录未生效 |
| V5 | Commit records | append 后 commit 前崩溃：journal 已权威，按 journal 绑定完成 commit（字节一致）；final no-replace |
| V6 | WriteSnapshot | replay 重建；snapshot 仅为缓存 |

### full replay 的重建义务与 fail closed 清单

full replay 从 `CREATED`/sequence=0 出发，仅凭 append-only journal 重建：State、Sequence、Publication、`ReviewRound`、`AttemptsUsed`、`OperationalRetriesUsed`、`ReworkRoundsUsed`、`TerminalReason`，以及 **origin consumed 状态**（每个 target shape checks-failed origin 是否被相邻自环消费）。snapshot 是缓存，不是权威。内容寻址记录是证据 bytes 的权威，任何消费路径（review 导入、execution admission、doctor）在其缺失或漂移时 fail closed。

replay/消费必须 fail closed 的情形（封闭清单）：

| 情形 | 处置 |
| --- | --- |
| 内容寻址记录缺失（payload 引用的 digest 无法解析） | 消费拒绝 |
| 摘要漂移（payload digest ≠ 内容寻址 bytes digest） | 消费拒绝 |
| 重复 origin 消费（journal 含两个引用同一 key 的自环） | journal 无效，replay 失败 |
| 自环先于 origin（引用不存在或更晚 sequence 的 origin） | replay 失败 |
| counter 不符（snapshot 与 replay 任一分歧） | fail closed（既有语义） |
| 旧 publication head（origin `headSha` 与其位置上的冻结 publication 不一致） | replay 失败 |
| 伪造 actor（producer-authority 表不符） | journal 无效（既有语义） |
| truncated tail（末行不完整） | 既有语义：完整行权威，截断内容留档，不完整事件 fail closed |

## 十、Rebuild/doctor 边界

- `marshal doctor` 诊断新增只读校验：每个 target shape checks-failed origin 的内容寻址 bytes 存在且 digest 一致；evidence origin 字段回指正确；origin consumed 状态与 journal 一致；CI packet/decision 的 digest 链完整；
- `doctor --repair` 语义不变：只修复 snapshot（snapshot-rebuild + `reconciliation.snapshot-repaired` 审计事件），不追加业务事件、不重建/不伪造缺失的内容寻址记录、不改变 `REWORK_REQUESTED`/终态业务事实；
- Rebuild 与 repair 走同一 typed reducer 与 replay 语义，不存在旁路。

## 兼容与迁移

- **Schema 兼容**：新增 `ci-failure-evidence.schema.json`；`review-packet.schema.json` 在 `additionalProperties: false` 下新增可选 `ciFailure` 扩展对象（不改任何既有 `required` 集合与字段约束）；review-decision、worker-request、task-spec、remote-check-record、publication-record 各 Schema 不改变（RemoteCheckRecord 字段语义归属 ADR 0028，PublicationRecord 归属 ADR 0007）；`apiVersion` 保持 `marshal.dev/v1alpha1`；
- **历史 journal**：legacy shape `publication.checks-failed`（仅 `headSha`）保持 replay-valid 与历史 counter 语义（见「counter 语义」replay 兼容规则），历史 Run 的 replay 与快照对账不受影响；legacy shape 是 replay-only 的：任何消费（CI review、execution findings、doctor digest 链校验）只接受 target shape；
- **切换时在途的 legacy CI origin**：不回填证据、不伪造自环。处于 `REWORK_REQUESTED` 且 origin 为 legacy shape 的 Run 在新契约下无法通过 CI review/execution admission（fail closed）；操作者处置路径为显式 abort（ADR 0012/0029 出口）+ 新 Run；
- 不批量迁移、不重写任何历史数据。

## 实施切片（计划，不在本 ADR 实施）

1. `schemas/ci-failure-evidence.schema.json` 与 review-packet typed CI 扩展；domain kind/Go 类型；contract catalog Descriptor 与语义校验器；happy-path/invalid fixtures；
2. publication observation：fail 分支重构（接纳顺序、内容寻址持久化、evidence 构造、双守卫、扩展 payload、接纳拒绝 typed 事务、预算耗尽终态与 Outcome 投影绑定）；
3. lifecycle/reducer/replay/runstore：allowed map 命名例外、typed counter 分派、origin consumed 派生、`CI_PENDING → REJECTED` 行、replay fail-closed 清单；
4. review importer/packet：CI packet 构造器（世代三元组校验、evidence identity 扩展）、decision 导入 CI 门禁、自环幂等/conflict；
5. execution prompt lineage：未注入拒绝、自环 lineage 解析、精确投影与字节等价；
6. CLI：`task review` 状态接纳扩展、`task run` admission guard 语义、doctor 只读校验；
7. 文档：task-lifecycle/failure-and-recovery 的 current/target 分离标注（随本 ADR 的文档同步完成契约层描述，实现合入时再更新 current 行为）。

## 测试矩阵（计划，不在本 ADR 实施）

| 类别 | 覆盖 |
| --- | --- |
| typed evidence happy path | fail 观察 → 内容寻址 bytes + evidence + checks-failed payload digest 链一致；CI review → 自环 → attempt 投影全链 |
| 每个 binding mismatch | taskId/runId/repositoryId/requestId/headSha/specDigest/publicationDigest 逐项不匹配 fixture，全部 fail closed |
| mixed/duplicate checks | required+非 required 混合（只筛选 required fail）；重复 name 拒绝；筛选为空拒绝；status=pending/external-failure 不入路径；semantic validator 从绑定 RemoteCheckRecord 重算 `failedRequiredChecks` 逐字相等，替换/漏项/多项负测全部失败 |
| 接纳拒绝 typed 事务 | 身份不匹配/重复 identity/筛选为空 → `publication.blocked` 封闭 payload（`error=ci-evidence-admission-rejected`）与 Outcome（verdict=blocked、finalReview* 继承）断言；零 evidence 持久化、零 counter 变化 |
| origin 身份预生成与 CAS 重算 | 预算/事件类型先于 evidence 构造裁决；CAS 失败后本次 evidence origin 绑定作废，重新派生后重建成功 |
| self-loop cardinality 与 lost-response | 同 key+同 decisionDigest 重放幂等返回（V2 先于 prepare，零追加、不撞 no-replace）；二次注入拒绝；origin 与成功注入一对一 |
| conflict | 同 key+不同 decisionDigest → conflict 零副作用（V2 先于 prepare，无任何 pending/append） |
| 预算终态 | attempt 耗尽/round 耗尽/双耗尽（attempt sentinel 优先）三 fixture；Outcome 逐字段断言：`verdict`/`finalReviewRound`/`finalReviewDigest`/`finalEvidenceDigest`/`findingCount` 继承来源正确、CI 三摘要绑定存在，且全程无新 ReviewDecision 产生；result.md 同步断言 |
| counter 示例 | 本文第七节单轮与双轮（O1/O2）两个示例逐事件断言：无双计数、无跳号、decision-NNN 无覆盖；自环零 counter 变化、不再次递增 |
| 未注入拒绝 | REWORK_REQUESTED 未消费 origin 时 task run 在 Probe 前拒绝、零副作用；legacy origin 同样拒绝 |
| prompt 精确消费与 operational retry | 投影四 key 逐字断言；retry 解析同 origin/decision 且 findings 字节等价；新 review origin 取代后不再解析旧 decision |
| snapshot 丢失/落后 | 删除/篡改 state.json 后 replay 重建一致；snapshot 与 replay 分歧 fail closed |
| Rebuild/doctor repair | doctor 检出 digest 漂移/缺失记录；--repair 仅 snapshot-rebuild，不改业务事实 |
| 崩溃注入 | W2–W6、V2–V6 每个窗口 kill：恢复幂等继续或安全阻断，无重复事件、无记录覆盖；含预算耗尽终态的 outcome.json/result.md 崩溃 fixtures（W3/W5 窗口） |
| 旧记录兼容 | legacy checks-failed journal replay 与其历史快照一致；legacy origin 消费拒绝；legacy packet evidenceDigest 字节不变 |

## 保留的不变量

- Worker 不能生成自己的权威 CI findings——findings 只能来自 Lead 对 typed evidence 作出的 ReviewDecision；
- Provider/Publisher 不能宣布 ReviewDecision——publisher 只追加 typed fact 事件，decision 只能经 `system/marshal-review` 导入；
- Merge 仍禁用（`mergePolicy=never`、Draft-only 不变）；
- 本 ADR 不改变 ADR 0028 的 ciDeadline 与可信 completedAt 裁决，也不改变 RemoteCheckRecord 的 Provider 事实所有权（`provider` const、字段语义归 ADR 0028）；
- Worker 不自证、Worker/Verifier/Publisher 分权、单写入者、ReviewDecision 精确证据绑定、append-only journal、幂等、fencing、fail closed 全部保持有效；
- 终态不可复活的唯一命名例外仍是 ADR 0026 typed reconciliation；本 ADR 的预算耗尽终态是 `REJECTED`，不进入该例外。

## 与既有 ADR 的关系

- **ADR 0004（Accepted）**：本 ADR 是「独立证据具有权威性」在 CI 阶段的落实——失败事实由 Marshal 独立观察并内容寻址，Worker 自报与 Provider 裁决都不是证据；
- **ADR 0007（Accepted）**：沿用发布世代模型（reviewRound 标识世代、fast-forward、同 Draft PR、世代归档）；仅把内容寻址 check 记录与 evidence 纳入归档范围；
- **ADR 0019（Accepted）**：确定性 Core 是唯一 Supervisor；自环是 Core 的 typed 转换，不是通用 workflow 事件；Typed Execution 不形成通用 Provider 协议；
- **ADR 0027（Accepted）**：CI packet 继承失败世代的 Candidate 绑定（candidateDigest/workerCandidateDigest），不新设写域、不改变归一化语义；evidence subject 语义与 0027 一致；
- **ADR 0028（Accepted）**：本 ADR 只处理 deadline 内观察到的 fail 路径；ciDeadline/completedAt 裁决与 RemoteCheckRecord 字段所有权均属 0028，不被本 ADR 修改；BLOCKED 原因码封闭集合属 0028，本 ADR 仅依其「新增原因码只能经后续修订或新 ADR 扩展」条款提议扩展一个原因码 `ci-evidence-admission-rejected`（见第二节「接纳拒绝 typed 事务」），不改变其余原因码语义；
- **ADR 0026（Accepted）**：typed reconciliation 语义不变；本 ADR 不新增 reconcile 类型。

## 非目标

- 不实现代码或 Schema，不接受本 ADR，不宣称功能已实现、M8 状态变化、生产可用或 conformance 通过；
- 不改变 ADR 0028 的 ciDeadline/completedAt 判定；不设计通用同态/same-state 事件；不开放 accept/reject/blocked/no_change 的 CI 自环；
- 不实现 Web/API/transport、自动修复 CI、自动生成业务修改建议、merge 或远端分支清理；
- 不把自由文本日志、URL 内容或 Provider 自我裁决接纳为权威 finding；
- 不回填历史 Run、不批量迁移。
