# ADR 0032：Policy 显式授权的受控 Task Merge（SCMMergeIntent → 独立 SCMMerger → SCMMergeReceipt 收敛 ACCEPTED）

- 状态：已接受（Accepted，2026-08-17）——接受只冻结本 ADR 的业务契约，不构成实现完整、production supported、conformance 通过或 Milestone 完成声明；journal-bound authority/delivery 的后续缺口由 [ADR 0033](0033-journal-bound-merge-authority-and-delivery.md)（Proposed）承接，关闭前 `mergePolicy=policy` 保持 unsupported
- 日期：2026-08-17
- 接受证据：维护者显式接受；对应 Marshal Run `adr0032-controlled-policy-merge-r1-20260817` 已以 reviewRound=3、state=`ACCEPTED` 收敛。真实 [ApprovalRecord Schema](../../schemas/approval-record.schema.json) 实例为 `approval:9de1bb4f138d3b33d34add75b57ca5b5`（`gate=publish`、`outcome=approved`、`source=human:user-explicit-standing-authorization`、`controlSequence=2`、`stateSequence=14`），精确绑定 `decisionDigest=sha256:7edacd64e51955bcf5b0f011ffa6ba5cec307b91bdc5abf7d9aac01fd40f1b9b` 与 `evidenceDigest=sha256:46db79cf0997dcf84085c394304a4fe8397b09b2f78d971fb085e4a4281dbfcf`；该记录是 publish gate 的耐久批准，不冒充一个不存在的独立 `adr-accept` gate。公开交付证据为 [PR #152](https://github.com/chiga0/marshal-harness/pull/152)（MERGED，merge commit `79a9e5038b0a9b51095235ab644b8a7b57257a05`），其正文绑定同一 Task/Run 与 evidence digest
- 决策来源：维护者已授权「独立审计与 required checks 全部通过之后自动合并」这一能力方向；但仓库 universal 规则（[AGENTS.md](../../AGENTS.md)）规定 Merge 默认禁用、不属于 MVP 生命周期，且不允许任何参与者以 `gh pr merge` 等 Marshal 之外的方式绕过受控流程合并 PR。发布权限的改变必须先新增或替代 ADR，本 ADR 是获得该正式能力的设计前置，并显式声明部分取代哪些旧裁决（见第九节）
- 公开背景：[Issue #25](https://github.com/chiga0/marshal-harness/issues/25) / [PR #24](https://github.com/chiga0/marshal-harness/pull/24) 暴露并推动了 PR 已合并语义下的权威 reconcile 契约（[ADR 0026](0026-scm-merge-receipt-and-publication-reconcile.md)）；当前仓库的全部合入（例如早期 Draft PR 的维护者手工合入）均由维护者在 Marshal 之外手工执行，Marshal 自身没有任何 merge 路径
- 关联：[ADR 0007](0007-intent-first-publication.md)（先记录意图的受控发布与远端对账）、[ADR 0010](0010-controlled-autonomy-and-intervention.md)（受控自治、审批 Gate 与人工介入）、[ADR 0019](0019-deterministic-control-plane-typed-execution-and-goal-admission.md)（确定性控制面、typed execution 与 append-only 补偿）、[ADR 0026](0026-scm-merge-receipt-and-publication-reconcile.md)（SCMMergeReceipt 与 PublicationReconcileRecord）、[ADR 0018](0018-control-plane-and-provider-ports.md)（Control Plane 与 Provider Port、权威/actor 双键空间）、[ADR 0028](0028-ci-deadline-phased-observation.md)（CI deadline 分阶段观察）、[ADR 0030](0030-ci-failure-rework-evidence-and-injection.md)（CI 失败证据与 rework 注入，Proposed，正交契约）、[任务生命周期](../task-lifecycle.md)、[安全模型](../security-model.md)

## 上下文

### 授权背景与问题陈述

维护者希望把「审计通过 + required checks 全绿之后的合并」从 Marshal 之外的人工操作，变成 Marshal 之内受 Policy 显式授权、绑定精确证据、可审计、可崩溃恢复的受控副作用。这不是一次放宽：当前所有相关不变量（Worker/Publisher 分权、ReviewDecision 精确证据绑定、human publish approval、intent-first、fencing、required checks、fail closed）必须全部保留，merge 只有在它们全部成立时才被允许。

问题在于当前契约没有为「Marshal 执行的受控 merge」保留任何位置：

1. TaskSpec 的 `publication.mergePolicy` 虽然已是封闭枚举 `never|manual|policy`（见 `schemas/task-spec.schema.json` 与 [任务契约](../task-contract.md)），但规划门禁机械拒绝 `never` 之外的一切取值；
2. PolicySnapshot 的 `effective.allowMerge` 字段虽然存在，但规划门禁同样机械拒绝 `allowMerge=true`；
3. Marshal task 生命周期没有任何 merge 命令；
4. ADR 0026 的 `SCMMergeReceipt` 只用于**观察远端先行合并**并做 accept-after-merge reconcile，不能被伪装成本地受控 merge 的授权或执行记录。

缺的不是远端能力（GitHub 的 merge API 存在），而是承载「Policy 授权 → 证据绑定 → intent-first 执行 → receipt 验证 → 权威收敛」的完整 typed 契约。本 ADR 给出该契约的草案（接受后冻结）。

### 当前实现的如实记录（current behavior）

本 ADR 不掩盖、不夸大当前状态。基线行为如下：

1. **TaskSpec 侧**：`publication.mergePolicy` 默认 `never`；规划门禁（`internal/planning/policy.go`，`ErrPolicyMerge`）fail closed 地拒绝 `mergePolicy != "never"` 的任务，也拒绝 `effective.allowMerge=true` 的 PolicySnapshot——Local MVP 从不 merge，任务侧与策略侧双锁；
2. **Policy 侧**：PolicySnapshot `effective.allowMerge=false` 是当前唯一可通过规划的组合；`allowMerge=true` 的快照在规划阶段即被拒绝；
3. **生命周期侧**：`marshal task` 子命令集合为 plan/run/verify/review/approve/publish/accept/reconcile（及 doctor 等），没有任何 merge 命令；状态机中不存在任何 merge 事件或 merge 触发的转换；
4. **发布侧**：`marshal task publish` 只能创建或更新带唯一 Marker 的 Draft PR（`internal/publication/service.go` 与 `internal/publisher/github/github.go` 均机械约束 `mode=draft && mergePolicy=never`），不提供 Ready for Review、Merge、Release 或 Deploy；
5. **reconcile 侧**：`SCMMergeReceipt` 与 `PublicationReconcileRecord`（ADR 0026）只在 PR 已被**远端先行合并**时采集与对账，支撑 `marshal task accept` 活路径与 `marshal task reconcile` 补偿路径的 `BLOCKED → ACCEPTED` 迁移。它是事后观察证据，不是事前授权，也不是执行记录；本 ADR 严禁把它伪装成本地受控 merge。

### 权威性分工（不变量前提）

SCM Provider（GitHub）只报告 typed facts：PR 的 head/base/state、checks 的名称与状态、merge commit 与合并者。是否允许 merge、何时收敛 `ACCEPTED`，一律由 Marshal Core 依据冻结 Policy、当前 ReviewDecision 与权威记录裁决。Provider/Publisher/SCMMerger 不产生授权、不宣布 ReviewDecision、不裁决生命周期；Worker/Verifier/普通 Adapter 不参与 merge，也永不获得 GitHub credential。本 ADR 的全部设计以该分工为前提。

## 决策

核心方向：把受控 task merge 建模为一个**独立于 publish 的 credentialed side effect**，冻结六段闭环，每一段都有封闭的字段表、绑定规则与 fail-closed 语义：

1. **最小 admission**：Policy、TaskSpec、ReviewDecision、human publish ApprovalRecord 与 required checks 的全部条件同时成立才允许进入 merge（第一节）；
2. **merge 前重新观察与绑定**：repository、PR、Draft marker、head/base、全部证据与授权记录在副作用前重新观察并逐项绑定，任一漂移即 fail closed；新鲜观察的 base 分支 head 必须逐字等于 RunState 锁定的冻结 baseSha，不等即拒绝并改走 fresh rebase/rework（第二节）；
3. **intent-first**：任何远端副作用（包括 Draft → ready）之前，先持久化不可变 `SCMMergeIntent`（第三节）；
4. **独立 SCMMerger**：新增独立 `port.SCMMerger`，只使用 Publisher 侧凭据执行 ready + merge，head OID 机械绑定到 merge 请求；禁止 admin/force/bypass/branch-delete（第四节）；
5. **receipt 验证**：merge 成功后必须观察、验证并持久化 ADR 0026 冻结的 `SCMMergeReceipt`；无 receipt 或任一绑定错配不得 `ACCEPTED`；响应丢失或崩溃先幂等对账，绝不盲重放（第五节）；
6. **权威收敛**：只有 receipt/evidence-bound 事件才能把 `CI_PENDING` 收敛到 `ACCEPTED` 并生成绑定 receipt digest 的 Outcome；远端已先合并而本地没有 `SCMMergeIntent` 的，只能走 ADR 0026 reconcile，不得声称受控自动合并（第六节）。

### 词汇与命名（封闭集合）

| 类别 | 名称 | 说明 |
| --- | --- | --- |
| 记录 kind（新增） | `SCMMergeIntent` | 不可变权威记录；merge 意图的唯一事前授权载体 |
| 记录 kind（复用） | `SCMMergeReceipt` | ADR 0026 冻结的不可变权威记录；本 ADR 不改变其字段与语义，只新增其生产路径（受控 merge 观察） |
| Port（新增） | `port.SCMMerger` | 独立 merge 端口，位于 Publication 信任域 |
| 事件类型（新增） | `publication.merged` | receipt/evidence-bound merge 完成事件，`CI_PENDING → ACCEPTED` 的唯一受控触发 |
| actor（新增登记） | `publisher/marshal-scm-merger` | `publication.merged` 的固定 actor，producer-authority 表登记，replay 强制校验 |
| 摘要名 | `intentDigest`、`receiptDigest`、`publicationDigest`、`reviewDecisionDigest`、`verificationDigest`、`evidenceDigest`、`policyDigest`、`publishApprovalDigest`、`remoteCheckRecordDigest`、`mergerCredentialIdentity` | 均为 `sha256:<64 hex>` canonical digest |
| 执行者身份（新增） | `expectedMergedBy`、`mergerSecurityDomainId` | intent 冻结的预期 merge 执行者 principal（canonical 表示 `github-login:<login>`）与 SCMMerger actor 侧安全域；receipt.`mergedBy` 归属核验的唯一权威正值来源 |
| mergeMethod（复用） | `merge \| squash \| rebase` | 与 ADR 0026 `SCMMergeReceipt.mergeMethod` 同一封闭枚举 |

除上表外本 ADR 不引入任何新事件类型、新状态或新 origin 种类；不开放通用 same-state transition；不新增 Run 状态。

## 一、最小 admission（全部成立才允许 task merge）

task merge 的准入条件冻结为以下全量合取；任一不成立即拒绝，零远端副作用：

| # | 条件 | 校验对象 |
| --- | --- | --- |
| M1 | `publication.mergePolicy == "policy"` | 冻结 TaskSpec |
| M2 | PolicySnapshot `effective.allowPublication == true` 且 `effective.allowMerge == true`，且 `policyDigest` 与 Run 冻结值精确一致 | 冻结 PolicySnapshot |
| M3 | `provider == "github"` 且 `mode == "draft"` | 冻结 TaskSpec |
| M4 | `requiredChecks` 非空，且 TaskSpec 冻结 `mergeMethod ∈ {merge, squash, rebase}` | 冻结 TaskSpec |
| M5 | 当前 ReviewDecision `verdict == accept`、`publicationRecommendation == publish`、`mergeRecommendation == eligible-after-policy` | round-bound ReviewDecision |
| M6 | 该 ReviewDecision 是产出当前发布世代 head 的 accept 决策：`reviewRound` 等于 PublicationRecord.`reviewRound`，`reviewDecisionDigest` 与 PublicationRecord 同名字段逐字一致，且其绑定的 `verificationDigest`/`evidenceDigest` 与发布世代逐字一致 | ReviewDecision ↔ PublicationRecord |
| M7 | 同一 review round 与精确 digest 的 human `publish` ApprovalRecord 成立：approval 绑定的 ReviewDecision digest、evidence digest 与当前值逐字相等，review round 一致，且未越过 ADR 0010 的适用边界（输入变化即自动失效） | ApprovalRecord |
| M8 | Run 位于 `CI_PENDING`，持有效 run lease；当前世代 PublicationRecord 经 frozen digest 对账通过 | RunState |
| M9 | ADR 0028 的 ciDeadline 门禁通过（既有语义与位置不变，本 ADR 不改变） | RemoteCheck 观察 |
| M10 | 新鲜观察的 RemoteCheckRecord schema-valid，身份字段逐字段绑定当前 Run/Publication（taskId、runId、repositoryId、requestId、headSha），冻结 requiredChecks 集合精确匹配，且全部 required check `status == pass` | RemoteCheckRecord |

补充冻结语义：

- **manual 模式不实现**：`mergePolicy=manual` 永远不调用 SCMMerger；其语义保持「Marshal 之外的人工合并 + ADR 0026 reconcile」，本 ADR 不为其新增任何执行路径；
- **never 永不调用 Merger**：`mergePolicy=never` 的任务不存在 merge 入口，既有 `CI_PENDING → ACCEPTED` 的 checks-green accept 路径逐字不变；
- **fail closed 是默认**：missing/stale/不可判定的任何输入一律拒绝；不得以环境、Profile 或操作者措辞放宽。`autonomous` Profile 不因「不增加人类 Gate」而豁免 M7——受控 merge 在任何 Profile 下都要求存在有效的 human publish ApprovalRecord；
- **预算语义**：merge admission 与执行不消费 `maxAttempts`/`maxReworkRounds`/`maxOperationalRetries`；merge 的 delivery retry 复用同一 intent（与 ADR 0019「delivery retry 复用同一 commandId，不消费业务预算」一致）。

## 二、merge 前重新观察与绑定

admission 通过后、写 `SCMMergeIntent` 之前，必须**重新观察**远端与本地权威记录，并把下表全部身份绑定进本次 merge。这里不接受任何来自旧观察的缓存值；唯一例外是冻结 baseSha——它不是观察值，而是 RunState 锁定的权威锚点，新鲜观察的 base 分支 head 必须逐字等于它：

| 观察对象 | 绑定要求 |
| --- | --- |
| repository | 仓库远程引用与冻结 TaskSpec `expectedRemoteUrl`/PublicationRecord.`repository` 精确一致（防 local Git config 或 `url.*.insteadOf` 重定向，ADR 0007 既有语义） |
| PR number | 等于 PublicationRecord 保存的不可变 publication id 对应 PR |
| Draft marker | 初次绑定：PR 仍处于 Draft 状态且携带本 Run 唯一 Marker（证明远端对象属于当前 Run）；恢复绑定：Draft 要求豁免，Marker 仍须绑定成立（见下文「初次绑定与恢复绑定」） |
| head OID | 精确等于 PublicationRecord.`headSha`（= RunState publication head） |
| base branch | PR base 分支名精确等于冻结 `baseBranch` |
| 冻结 baseSha | RunState 在 Run 进入 READY 时锁定的基线 SHA；一经锁定不可被任何后续观察改写或覆盖，同世代内是 base 绑定的唯一权威锚点（provenance 与门禁双重角色），只有 fresh rebase/rework 产生的新发布世代才随新世代重新锁定 |
| baseOid | merge 观察时刻新鲜观察的 base 分支 head OID；**必须逐字等于冻结 baseSha** 才可写入 intent 作为本次 merge 作用的确切 base；两者不等即 base 前进，fail closed，禁止仅记录前进后的 baseOid 后继续 merge |
| ReviewDecision | `reviewDecisionDigest`、`reviewRound` 精确一致（M5/M6） |
| VerificationReport | `verificationDigest` 精确一致 |
| evidence | `evidenceDigest` 精确一致 |
| PublicationRecord | `publicationDigest` frozen 对账通过 |
| PolicySnapshot | `policyDigest` 精确一致 |
| ApprovalRecord | `publishApprovalRecordId` 与 `publishApprovalDigest` 精确一致 |
| required checks identity | 新鲜 RemoteCheckRecord 的 `remoteCheckRecordDigest`（内容寻址 bytes 的 canonical digest），全绿且绑定当前 head |
| merge 执行者认证身份 | 经 Publisher 侧凭据通道（ADR 0007 冻结的同一 `MARSHAL_GH_PATH`/`MARSHAL_GH_CONFIG_DIR` 解析路径）只读观察该凭据当前认证身份，冻结为 intent.`expectedMergedBy`，canonical 表示固定为 `github-login:<login>`；观察失败、结果为空或歧义即 fail closed。它是 receipt.`mergedBy` 归属核验的唯一权威正值来源，不得由操作者手工填写，不得取 `requestedBy` 或任何人类身份顶替 |
| 凭据解析身份 | 凭据解析身份元组 `(gh binary 解析后真实路径, gh config dir 解析后真实路径, expectedMergedBy)` 的 canonical digest，冻结为 intent.`mergerCredentialIdentity`；任何凭据物料（token/secret）不得进入该元组、digest 输入或字段本身 |
| SCMMerger 安全域 | SCMMerger actor 侧复合安全域标识（ADR 0018 §10 复合安全域键空间：tenantNamespace + trustDomainKind + isolationDomainId 的 canonical 形式），冻结为 intent.`mergerSecurityDomainId`；merge 执行仅允许发生在该安全域之下 |

fail closed 触发条件（任一命中即拒绝，不产生任何远端副作用）：

- 上表任一对象缺失、schema-invalid 或 digest/身份不符（漂移）；
- required checks 出现任何非绿状态——封闭枚举中的 `pending`、`fail`、`missing`、`skipping`、`cancel` 一律不构成 merge 条件（`pending` 意味着继续等待而非放行）；
- 冻结 `requiredChecks` 集合为空；
- base 前进：观察到的 base 分支 head 与冻结 baseSha 不一致。绑定值来自 RunState 锁定锚点而非本次观察，不存在「两处皆取当前观察」的自证循环；admission、intent 写前重新观察、merge 执行前即时观察、receipt 事后核对任一时点发现不等即 fail closed，禁止仅记录前进后的 baseOid 后继续 merge。唯一恢复路径：fresh rebase/rework 产出新 head 与新发布世代，重新 Verification/Review/Approval 并重新 publish，随新世代重新锁定 baseSha 后重新走 admission。base 在 Provider merge API 上没有可机械绑定的参数，其残留 TOCTOU 窗口与 bounded 处置在「后果」节如实声明；
- PR 已被任何外部 actor 合并（无本地 intent 的已合并状态处理见第六节）。

### 初次绑定与恢复绑定（initial / recovery admission）

上表的 Draft 状态要求只约束**初次绑定**：首次持久化 `SCMMergeIntent` 之前，PR 必须仍为 Draft 且携带本 Run 唯一 Marker，全表绑定逐项成立，任一不满足即拒绝。

一旦同一 canonical 幂等身份且按 detached 规则重算 `intentDigest` 一致的 `SCMMergeIntent` 已成功持久化（典型为 `ReadyForReview` 成功后，或 ready/merge 响应丢失后的恢复），进入**恢复绑定**：

- PR 允许已处于 ready 状态（`ReadyForReview` 的合法后果），Draft 要求不再适用；
- repository、PR number、head OID、base 分支 head（仍须逐字等于冻结 baseSha）、Marker 与既有 intent 的 canonical 身份必须重新观察并逐项绑定，任一漂移即 fail closed；
- M5–M10 的证据/授权 digest 与 required checks 校验照常对当前值重新核验，不因恢复而放宽；
- 恢复绑定不得重新生成授权、ReviewDecision、ApprovalRecord 或新的 intent，只允许以同一 intent 续作未完成步骤；
- 没有匹配 intent 的 ready PR 不构成恢复条件：缺少同身份 intent 时一律按初次绑定拒绝（防止「先手动 ready、再补 intent」的绕过）。

ready 调用失败或响应丢失时，按第五节 `ObserveReady` 恢复状态机对账，不得盲重试。

## 三、SCMMergeIntent（不可变权威记录）

SCMMergeIntent 是 merge 的唯一事前授权载体，一经写入不可改写；**任何远端副作用（包括 Draft → ready）都不得先于它的持久化**。它是 authority ledger 事实，归属 `authorityNamespaceId`（ADR 0018 权威/actor 双键空间），只允许 Core 写入。字段名、类型与约束将由配套 Draft 2020-12 JSON Schema（`schemas/scm-merge-intent.schema.json`，`apiVersion=marshal.dev/v1alpha1`，`kind=SCMMergeIntent`，`additionalProperties: false`）逐字对齐——Schema 落盘与 catalog 注册属 B1 切片，本 ADR 只冻结字段集合：

| 字段 | 类型与约束 | 语义 |
| --- | --- | --- |
| `intentId` | string，非空，canonical 身份 | intent 唯一身份 |
| `authorityNamespaceId` | string，非空 | 权威记录归属 |
| `taskId` | id | 必须等于 RunState/TaskSpec 身份 |
| `runId` | id | 必须等于 RunState 身份 |
| `publicationRecordId` | string，非空，必须为 `sha256:<64 hex>` | 当前世代 PublicationRecord 的 canonical 身份。冻结（逐字复用现有 reconcile 契约）：PublicationRecord 的身份即其文档的 canonical digest（identity = digest）——现有 `internal/publication/reconcile.go` `validateReceiptBinding` 已强制 `receipt.publicationRecordId == publicationDigest`，`internal/publisher/github` 采集时即以 canonical digest 作为 `publicationRecordId`。因此本字段必须逐字等于下文 `publicationDigest`，且逐字等于对当前世代 PublicationRecord 文档重算的 canonical digest；任一空值、格式违规或互不相等即 fail closed |
| `publicationDigest` | `sha256:<64 hex>` | 当前世代 PublicationRecord 文档 bytes 的 canonical digest，可从权威文档重算，是三重等值关系的唯一重算锚点：intent.`publicationRecordId` == intent.`publicationDigest` == receipt.`publicationRecordId` == 重算值；任一不一致即 fail closed，不得产生互相不一致的双身份 |
| `reviewDecisionDigest` | `sha256:<64 hex>` | 当前 accept ReviewDecision 的 canonical digest |
| `verificationDigest` | `sha256:<64 hex>` | 发布世代 VerificationReport digest |
| `evidenceDigest` | `sha256:<64 hex>` | 发布世代 evidence identity digest |
| `policyDigest` | `sha256:<64 hex>` | 冻结 PolicySnapshot digest |
| `publishApprovalRecordId` | string，非空 | human publish ApprovalRecord 身份 |
| `publishApprovalDigest` | `sha256:<64 hex>` | 该 ApprovalRecord 的 canonical digest |
| `remoteCheckRecordDigest` | `sha256:<64 hex>` | 全绿观察的内容寻址 RemoteCheckRecord bytes 的 canonical digest |
| `repositoryRef` | string，非空 | 仓库远程引用，逐字段等于 PublicationRecord |
| `prNumber` | integer，≥1 | PR 编号 |
| `headOid` | 完整 SHA | 合并前 head，精确等于 PublicationRecord.`headSha` |
| `baseOid` | 完整 SHA | 必须逐字等于 RunState 锁定的冻结 baseSha；写入前经新鲜观察核对，禁止写入与 baseSha 不等的前进值 |
| `mergeMethod` | enum：merge \| squash \| rebase | 冻结 TaskSpec 的 mergeMethod |
| `requestedBy` | string，非空 | 触发 merge 的认证操作者身份（维护者）；不得是 Worker/Verifier/Provider actor。`requestedBy` 是唯一人类请求者字段，与 `expectedMergedBy` 角色互斥、不可互相顶替 |
| `requestedAt` | RFC 3339 | merge 请求时间 |
| `expectedMergedBy` | string，非空，canonical 表示固定为 `github-login:<login>` | 预期 merge 执行者 principal：第二节重新观察时经 Publisher 侧凭据通道只读观察并冻结的认证 GitHub login；是 receipt.`mergedBy` 归属核验的唯一权威正值来源。不得是 `requestedBy`，不得手工填写或从人类身份派生，不得是 Worker/Verifier 身份；缺失即 fail closed，intent 不允许无预期执行者身份而持久化 |
| `mergerSecurityDomainId` | string，非空 | SCMMerger actor 侧复合安全域标识（ADR 0018 §10 双键空间的 actor 侧键：tenantNamespace + trustDomainKind + isolationDomainId 的 canonical 形式）；merge 执行必须且只能发生在该安全域之下，缺失即 fail closed |
| `mergerCredentialIdentity` | `sha256:<64 hex>` | 凭据解析身份元组 `(gh binary 解析后真实路径, gh config dir 解析后真实路径, expectedMergedBy)` 的 canonical digest；单向、可重算，digest 输入禁止包含任何凭据物料（脱敏边界见第四节）；缺失即 fail closed |
| `intentDigest` | `sha256:<64 hex>` | intent 记录的 detached canonical digest：计算时把 `intentDigest` 字段自身置为空字符串后再摘要（规则见下文「detached digest 规则」） |

### canonical 幂等身份与冲突语义

- canonical 幂等身份 canonical 绑定 `(authorityNamespaceId, runId, publicationRecordId, headOid, mergeMethod)` 五元组；
- 同身份且按 detached 规则重算的 `intentDigest` 相同 → 幂等返回既有 intent（lost-response 重放安全）；
- 同身份但任一内容不同（重算 `intentDigest` 不同）→ 固定 conflict，fail closed，零副作用；`expectedMergedBy`/`mergerSecurityDomainId`/`mergerCredentialIdentity` 属于 intent 内容：同一 canonical 幂等身份下凭据轮换、认证身份或安全域漂移导致三者任一不同，即按本条冲突处理，不得以既有 intent 静默续作；
- 一个 Run 的一个发布世代的一个 head 至多一个成功 merge intent；Run 进入 `ACCEPTED` 后不再接纳任何新 merge intent。

### 持久化

- 位置：`runs/<runId>/merge-intents/<hex64>.json`，`<hex64>` 为 `intentDigest` 去掉 `sha256:` 前缀的 64 位小写十六进制；全部 digest 引用保持完整 `sha256:<hex64>` 形式；
- 写入语义：digest-verified **put-if-absent**——目标存在且按 detached 规则重算的 `intentDigest` 与本次一致则幂等成功，存在但重算 digest 不同则 fail closed；永不覆盖、永不原地改写、永不删除；
- intent 不携带任何 credential、token 或 Provider 原始输出；`requestedBy` 只记录身份标识。

### detached digest 规则（冻结）

- **intentDigest**：使用 Marshal canonical JSON（RFC 8785 JCS，即 `internal/canonical.DigestJSON`）计算；计算时先把 `intentDigest` 字段自身置为空字符串（字段保留、值为 `""`），对其余全部字段做 JCS canonicalize，再取 `sha256:<64 hex>`。producer 回填与 verifier 重算必须逐字使用同一算法（置空 → canonicalize → 摘要），使摘要可构造、可独立复核；
- **receiptDigest**：逐字复用 ADR 0026 与现有 `domain.SCMMergeReceipt` 冻结的 detached 规则（canonicalize 前移除 `receiptDigest` 字段，经同一 `internal/canonical` JCS 实现重算），本 ADR 不改变、不重新定义该规则；
- 两个 detached 变体各自冻结、不得互换：intentDigest 置空字段后摘要，receiptDigest 按既有规则移除字段后摘要；任一变更都构成契约变更，须经新 ADR；
- put-if-absent 的 digest 校验、重放幂等、冲突检测与全部负向测试都必须基于上述同一重算算法；Provider 或 runtime 自报的任何摘要一律不接受，Marshal Core 永远自行重算；
- 重算只发生在 Marshal Core 内部，重算输入是权威记录文档本身，不依赖任何远端响应字段。

## 四、独立 port.SCMMerger 与凭据边界

### Port 定义

新增独立 `port.SCMMerger`，与既有 publication Publisher 接口分离，但同处 Publication 信任域（ADR 0018 trust domain 隔离不变）。它只暴露两个操作，封闭集合，不得扩展：

| 操作 | 语义 | 约束 |
| --- | --- | --- |
| `ReadyForReview` | 将 Draft PR 置为 ready for review | 只接受 intent 绑定的 repository/prNumber；幂等（已 ready 视为成功观察） |
| `Merge` | 执行 merge | 必须把 `expectedHeadOid == intent.headOid` 机械绑定进 merge 请求（Provider API 的 expected head 参数），并按 `intent.mergeMethod` 执行 |

mutation 集合冻结为以上两个，不得扩展。`ObserveReady` 不是第三个 mutation：它是 ready 失败/响应丢失时的只读观察对账状态机（第五节），经既有 publication 观察表面执行，不给 SCMMerger 增加任何写能力。

### 凭据边界

- SCMMerger 只使用 **Publisher 侧凭据**：沿用 ADR 0007 冻结的 Publisher 凭据解析路径（显式绝对路径 `MARSHAL_GH_PATH` 与独立 `MARSHAL_GH_CONFIG_DIR`）；不新增第二凭据来源；
- **执行时身份重观察（前置门禁）**：ready 与 merge 每次调用之前（含恢复路径的每次观察），SCMMerger 必须在 intent 绑定的安全域下重新观察当前认证 principal 与凭据解析身份，并要求逐字等于 intent.`expectedMergedBy` 与 intent.`mergerCredentialIdentity`，否则 fail closed、零远端副作用；不得仅以事后 receipt 核对替代该前置门禁——前置门禁收窄「错误凭据实际执行 merge」的窗口，receipt 核对是事后兜底，两者缺一不可；
- **身份字段脱敏边界**：`expectedMergedBy` 只承载 principal 标签（`github-login:<login>`），`mergerCredentialIdentity` 是单向 digest（输入为解析路径与 principal，不含凭据物料）；两个字段以及 receipt.`mergedBy`（远端报告的执行者 login）都不得承载、编码或泄露任何 token、secret、credential 物料或 Provider 原始输出（[安全模型](../security-model.md) 既有 credential 条款继续适用）；
- **Worker、Verifier 与普通 Adapter 永远不获得 GitHub credential**：执行环境构造机械排除 `GH_TOKEN`、`GITHUB_TOKEN` 与 `MARSHAL_GH_*` 变量，负向测试断言其不存在（第八节 T9）；
- credential 不进入 TaskSpec、事件、Prompt、日志、intent、receipt 或 Outcome（[安全模型](../security-model.md) 既有条款继续适用）。

### 禁止操作集（封闭）

SCMMerger 与其凭据永远不得用于：admin API、force 操作、绕过审查/检查的 bypass（含 auto-merge queue 之类的远端托管自动化）、删除分支（含 merge 后删除 head branch）、关闭 PR、改写 base、改写 PR 内容或评论。能力越界请求一律 fail closed。

### 原子性要求

若 Provider 不能把 expected head OID 机械绑定到 merge 请求，则该 Provider/任务必须 `BLOCKED`（能力不足 fail closed），**不得以「merge 前后各观察一次」冒充原子门禁**——前后观察不构成 fencing，无法排除观察间隙内的 head 漂移。

## 五、执行顺序与 SCMMergeReceipt 验证

受控 merge 的事务顺序冻结如下；任一步失败都不改写已持久化的权威记录：

1. admission（第一节）与重新观察绑定（第二节）全部通过；
2. **持久化 SCMMergeIntent**（put-if-absent）——先于一切远端副作用；
3. `ReadyForReview`：Draft → ready for review；明确失败或响应丢失时先经 `ObserveReady` 状态机对账（见下文），不得盲重试；
4. `Merge`：携带 `expectedHeadOid` 与 `mergeMethod` 执行 merge；
5. 观察 merge 结果并构造 `SCMMergeReceipt`（ADR 0026 冻结的字段集合逐字复用：`receiptId`、`authorityNamespaceId`、`runId`、`publicationRecordId`、`repositoryRef`、`prNumber`、`headOid`、`baseOid`、`mergeCommitSha`、`mergedAt`、`mergedBy`、`mergeMethod`、`capturedAt`、`receiptDigest`），内容寻址持久化（put-if-absent）；
6. 经 journal CAS（expectedSequence）追加 receipt-bound 事件 `publication.merged`；
7. `CI_PENDING → ACCEPTED`；生成绑定 receipt/intent digest 的 Outcome（第六节）。

### ObserveReady 恢复状态机（ready 失败或响应丢失）

`ReadyForReview` 返回明确失败或响应丢失时，不得盲重试，也不得直接宣布失败；先执行只读 `ObserveReady` 对账（不产生任何远端副作用），以既有 intent 的 canonical 幂等身份为输入，重新观察 PR 当前状态并按下表收敛：

| 观察结果 | 恢复语义 |
| --- | --- |
| PR 仍为 Draft，Marker 绑定一致 | ready 未生效：以同一 intent 幂等重试 `ReadyForReview` |
| PR 已 ready，repository/PR/head OID/base（逐字等于冻结 baseSha）/Marker 与 intent 逐项绑定一致 | ready 已生效（含响应丢失但远端成功）：视为成功观察，按恢复绑定（第二节）续作 `Merge` |
| PR 已被合并 | 转入 `ObserveMergeReceipt` 对账（第七节 C4/C5） |
| PR 已关闭，或 head/base/Marker/repository 任一漂移 | fail closed → `BLOCKED`，只进对账记录 |

`ObserveReady` 可幂等重复执行任意次；恢复路径全程不重新生成授权、不新建 intent。

### receipt 绑定验证（全部通过才可 ACCEPTED）

SCMMergeReceipt 是 ADR 0026 的既有不可变权威记录（字段集合逐字不变）；本 ADR 新增的生产路径必须执行下列逐项核对：**任一字段缺失（含空值）、任一不符即 fail closed，Run 不得进入 `ACCEPTED`**。其中 `authorityNamespaceId`/`runId` 的逐字核对是 ADR 0018 双键空间不变量（所有权威记录与引用键进入双键空间：权威记录归属 `authorityNamespaceId`，actor 身份归属 `securityDomainId`，两者不混同）在 receipt 侧的必要项；`mergedBy` 归属核验的唯一权威正值来源是 intent.`expectedMergedBy`。本表适用于受控 merge 生产路径；ADR 0026 accept-after-merge reconcile 路径的核对面仍按其自身冻结文本执行，本 ADR 不扩展、不弱化：

| receipt 字段 | 必须等于/满足 |
| --- | --- |
| `authorityNamespaceId` | 逐字等于 intent.`authorityNamespaceId`；缺失、空值或跨 authority 重放（receipt 来自其它权威命名空间）即 fail closed |
| `runId` | 逐字等于 intent.`runId`；缺失、空值或跨 Run 重放即 fail closed |
| `headOid` | intent.`headOid` |
| `baseOid` | intent.`baseOid`，且逐字等于冻结 baseSha；receipt 核对时点发现 base 前进同样 fail closed |
| `mergeMethod` | intent.`mergeMethod` |
| `publicationRecordId` | 逐字等于 intent.`publicationRecordId`，并通过三重等值核验：intent.`publicationRecordId` == intent.`publicationDigest` == 当前世代 PublicationRecord 文档重算 canonical digest（identity = digest，逐字复用现有 reconcile 契约）；任一空值、非 `sha256:<64 hex>` 格式或互不相等即 fail closed |
| `repositoryRef` / `prNumber` | intent 同名字段 |
| `mergeCommitSha` | 非空完整 SHA，来自 Provider 对账 |
| `mergedBy` | 非空；`"github-login:" + receipt.mergedBy` 逐字等于 intent.`expectedMergedBy`——intent 绑定是归属的唯一权威正值来源；不得归属 Worker/Verifier 或无关 actor，不得以 `requestedBy` 或任何人类身份替代比对，不得退化为「可归属 Publisher 侧」之类的模糊判定 |
| `receiptDigest` | 按 ADR 0026 既有 detached 规则重算一致（canonicalize 前移除 `receiptDigest` 字段）；不接受 Provider 或 runtime 自报摘要 |

### 失败与重试语义

- merge 返回明确失败（如 base 不可合并）：可用**同一 intent** 有界重试（不新建 intent、不消费业务预算）；重试仍失败 → `BLOCKED` + blocker 记录；
- 响应丢失或进程崩溃：先 `ObserveMergeReceipt` 幂等对账，再决定续作或重试，**绝不盲重放**（第七节 C4）；
- receipt 与 intent 冲突（上表任一错绑）或 receipt 重放冲突（同 `receiptId` 不同 bytes）：永远 fail closed，`BLOCKED`，交由操作者处置或关联新 Run；
- 无 receipt 永远不得 `ACCEPTED`。

merge 属 ADR 0019 的 `irreversible` dispositionClass：不提供、不伪装任何「unmerge/回滚」；失败处置只能走当前生命周期允许的转换与 append-only reconcile/compensation 记录。

## 六、生命周期集成：CI_PENDING → ACCEPTED

### publish 语义不变

`marshal task publish` 继续只产生 Draft PR 与 PublicationRecord：受控 merge 不改变 publish 的任何既有门禁、事件与世代语义；Draft-only 对 publish 路径保持有效。

### task merge 是独立 credentialed side effect

merge 不属于 publish 事务，不由 `publication.completed` 或 checks 观察自动触发；它由认证操作者在 `CI_PENDING` 且全绿后显式发起（B3 切片提供 CLI 表面），经第一节 admission 与第二节绑定后才执行。

### 受控收敛事件（冻结）

| 要素 | 冻结值 |
| --- | --- |
| 事件类型 | `publication.merged`（新增） |
| 转换 | `CI_PENDING → ACCEPTED`（既有转换表行；本 ADR 为其增加 policy 条件守卫，不新增状态） |
| actor | `publisher/marshal-scm-merger`（producer-authority 表新增登记，replay 强制校验） |
| payload（封闭） | `intentId`、`intentDigest`、`receiptId`、`receiptDigest`、`headOid`、`mergeCommitSha`、`mergeMethod`、`publicationDigest`、`remoteCheckRecordDigest` |
| guard | LeaseHeld + PublicationCurrent + receipt 绑定验证（第五节）全部通过 |
| counter | 全部不变（`ACCEPTED` 不触碰任何 counter） |
| Outcome | TerminalState=`ACCEPTED`；在既有 FinalReviewDigest/FinalEvidenceDigest/publicationDigest 绑定之上，新增绑定 `intentDigest` 与 `receiptDigest`（Outcome schema 可选扩展，B2 演进点，不改变历史 Outcome）；事务沿用 PrepareOutcome no-replace 语义 |

### 按 mergePolicy 分派的收敛语义

| mergePolicy | CI_PENDING 收敛路径 |
| --- | --- |
| `never` | 既有语义逐字不变：当前 head 的 required checks 全绿即经既有 accept 路径 `CI_PENDING → ACCEPTED`；SCMMerger 永不参与 |
| `manual` | 不调用 SCMMerger（本 ADR 不实现 manual）；Marshal 内不 merge，远端人工合并后只走 ADR 0026 reconcile |
| `policy` | `CI_PENDING → ACCEPTED` **只允许**由 `publication.merged` 事件触发；checks 全绿本身不构成 ACCEPTED，只是 merge admission 的输入 |

### 外部先行合并（无本地 SCMMergeIntent）

`mergePolicy=policy` 的 Run 若观察到 PR 已被外部 actor 合并而本地不存在 `SCMMergeIntent`：

- 不得声称受控自动合并，不得追加 `publication.merged`；
- Run 经既有 typed failure 转入 `BLOCKED`（外部事实，`publication.blocked` 既有语义，原因码由 B1/B2 按 ADR 0028 封闭原因码扩展规则冻结）；
- 其后若 merged head 的 required checks 全绿，只能经 ADR 0026 的 accept-after-merge typed reconciliation（`SCMMergeReceipt` + `PublicationReconcileRecord` + current-ledger recheck）迁移 `BLOCKED → ACCEPTED`。

ADR 0030（Proposed）的 CI 失败 rework 闭环与本 ADR 正交：checks 失败仍按 ADR 0030（接受并实现后）或当前行为处理，本 ADR 不改变其证据、origin 或 counter 语义。

## 七、崩溃恢复矩阵

受控 merge 的全部崩溃窗口都只依赖 append-only 记录（intent/receipt/journal）与远端观察恢复；不盲重放、不改写历史：

| # | 崩溃点 | 远端事实 | 恢复语义 |
| --- | --- | --- | --- |
| C1 | intent 持久化前 | 无任何远端副作用 | 无需恢复；重新 admission 即可，零孤儿状态 |
| C2 | intent 后、ready 前 | PR 仍为 Draft | 按恢复绑定（第二节）重新校验后，以同一 intent（幂等身份）续作 ready + merge；不重新执行以 Draft 为要件的初次 admission，不新建 intent、不重新生成授权 |
| C3 | ready 后、merge 前 | PR 已 ready、未合并 | 恢复绑定允许 PR 为 ready：与既有 intent 逐项绑定核对（含 base 逐字等于冻结 baseSha）通过后，以同一 intent 续作 merge；ready 幂等（已 ready 视为成功观察） |
| C4 | merge 成功、响应丢失 | 未知 | 先 `ObserveMergeReceipt` 对账：已合并且 receipt 与 intent 逐项匹配 → 续作 receipt 持久化与事件追加；未合并 → 先经 `ObserveReady` 状态机确认 ready 状态，再以同一 intent 重试 merge。绝不盲重放 |
| C5 | merge 后、receipt 持久化前 | 已合并 | 重新观察构造 receipt（GitHub PR 节点保留 head/base/merge commit，ADR 0026 既有事实），put-if-absent 持久化，续作事件 |
| C6 | receipt 后、事件追加前 | 已合并且 receipt 存在 | journal CAS 追加事件；同 key 已存在且 digest 一致 → 幂等返回，不重复追加 |
| C7 | 事件后、Outcome 前 | ACCEPTED 已生效 | replay 从 journal 与 receipt 绑定重建 Outcome；final outcome 存在即拒绝重写（no-replace） |

补充冻结语义：

- **ready 成功但 merge 失败**：可重试同一 intent，但无 receipt 永不 `ACCEPTED`；永久失败走 `BLOCKED`；
- **冲突 receipt**（同身份不同内容，或绑定错配）：永远 fail closed，`BLOCKED` + reconcile 记录，操作者显式处置；
- 恢复全程不得改写 PublicationRecord、ReviewDecision、ApprovalRecord、intent 或 receipt；晚到/歧义结果只进对账记录。

## 八、负向测试矩阵（完整冻结）

实现切片必须覆盖以下全部测试用例：负向用例的预期一律为拒绝/fail closed 且零远端副作用（除标注外）；标注「正向对照」的是对应绑定的唯一确定性通过条件，实现切片必须与负向用例同批冻结为断言项，不得只测负向：

| # | 类别 | 测试用例 | 预期 |
| --- | --- | --- | --- |
| T1 | Policy admission | `allowMerge=false`；`allowPublication=false`；policyDigest 不符 | 拒绝，Merger 不被调用 |
| T2 | TaskSpec 模式 | `mergePolicy=never` → Merger 永不被调用；`mergePolicy=manual` → 不调用（未实现）；`mergePolicy=policy` 但缺 `mergeMethod` | 拒绝/无入口 |
| T3 | ReviewDecision | `mergeRecommendation=do-not-merge`；stale decision（digest 或 reviewRound 与发布世代不符）；`publicationRecommendation != publish`；`verdict != accept` | 拒绝 |
| T4 | Approval | 缺 human publish ApprovalRecord；approval digest 伪造/不符；review round 不符；已越过适用边界的旧 approval | 拒绝 |
| T5 | required checks | 冻结 `requiredChecks` 为空；任一 required check 处于 `pending`/`fail`/`missing`/`skipping`/`cancel`；check 集合与冻结 identity 不匹配 | 拒绝（pending 为等待，不放行） |
| T6 | 漂移 | repository 漂移；PR number 漂移；head OID 漂移；base 前进（任一观察时点观察 base head ≠ 冻结 baseSha，含 receipt 核对时点）；Draft marker 缺失或 Marker 不符 | fail closed |
| T7 | 外部已 merge | 无本地 SCMMergeIntent 而 PR 已被外部合并 | 不声称受控合并；`BLOCKED`，只走 ADR 0026 reconcile |
| T8 | method 不符 | receipt.`mergeMethod` ≠ intent.`mergeMethod` | 拒绝，不得 ACCEPTED |
| T9 | 凭据隔离 | Worker/Verifier 环境出现 `GH_TOKEN`/`GITHUB_TOKEN`/`MARSHAL_GH_*` | 环境构造机械排除；出现即测试失败 |
| T10 | 顺序违规 | intent 持久化前发生任何远端副作用（ready 或 merge） | 必须 fail closed；测试断言无 intent 即无副作用 |
| T11 | intent 冲突 | 同 canonical 幂等身份、不同内容（digest 不同） | 固定 conflict，fail closed，零副作用 |
| T12 | receipt 错绑 | `authorityNamespaceId`/`runId`/head/base/mergeCommitSha/method/publicationRecordId/actor（mergedBy）任一错绑 | 不得 ACCEPTED，fail closed。正向对照：仅当 receipt 全部字段逐字等于 intent 绑定（含 authorityNamespaceId、runId、publicationRecordId 与 mergedBy canonical 编码）且 receiptDigest 重算一致时，绑定验证才通过 |
| T13 | receipt 重放冲突 | 同 `receiptId` 不同 bytes | fail closed |
| T14 | crash cuts | C1–C7 每个崩溃窗口的注入测试 | 按第七节幂等恢复；无重复副作用、无 ACCEPTED 伪造 |
| T15 | 能力缺失 | Provider 无法把 expected head OID 机械绑定到 merge 请求 | `BLOCKED`；不接受前后观察冒充原子门禁 |
| T16 | base 前进处置 | base 前进后仅记录前进后的 baseOid 即试图继续 merge；未先 fresh rebase/rework 并重新 Verification/Review/Approval 即重新 admission | 拒绝；唯一恢复路径是 fresh rebase/rework 产出新发布世代后重走全链，随新世代重新锁定 baseSha |
| T17 | ready 恢复 admission | 无匹配 intent 的 ready PR 请求 merge（含「先手动 ready、再补 intent」）；有匹配 intent 但 head/base/Marker/repository 任一漂移的 ready PR | 拒绝。对照断言：仅当同身份 intent 存在且恢复绑定逐项通过时，ready PR 才可续作，且不得重新生成授权或 intent |
| T18 | detached digest | Provider/runtime 自报 `intentDigest` 或 `receiptDigest`；篡改 intent bytes 后按 detached 规则重算不符；put-if-absent 目标存在但重算 digest 不同 | 自报摘要一律不接受；重算不符即 fail closed，不得以记录内字段值冒充重算结果 |
| T19 | 跨 authority/跨 Run receipt 重放 | receipt.`authorityNamespaceId` ≠ intent.`authorityNamespaceId`（跨 authorityNamespaceId 重放）；receipt.`runId` ≠ intent.`runId`（跨 runId 重放）；receipt `authorityNamespaceId` 或 `runId` 缺失/为空（字段缺失）；把其它 Run 或其它权威命名空间的合法 receipt 对当前 intent 提交 | 全部 fail closed，不得 ACCEPTED；不得以其余绑定字段全部匹配为由接受。正向对照：仅当 receipt.authorityNamespaceId 与 receipt.runId 均逐字等于 intent 同名字段时，该两项核验通过（ADR 0018 双键空间） |
| T20 | publicationRecordId/publicationDigest 绑定 | intent.`publicationRecordId` ≠ intent.`publicationDigest`（双身份互相不一致）；receipt.`publicationRecordId` ≠ intent.`publicationRecordId`；当前世代 PublicationRecord 文档重算 canonical digest ≠ intent.`publicationDigest`；三者任一为空或非 `sha256:<64 hex>` 格式 | 全部 fail closed，不得 ACCEPTED。正向对照：仅当 intent.publicationRecordId、intent.publicationDigest、receipt.publicationRecordId 三者同值且等于对当前世代 PublicationRecord 文档重算的 canonical digest（identity = digest）时，该项核验通过 |
| T21 | mergedBy 权威身份与凭据域 | receipt.`mergedBy` 的 canonical 编码（`github-login:` + mergedBy）≠ intent.`expectedMergedBy`（错误身份）；intent 缺 `expectedMergedBy`/`mergerSecurityDomainId`/`mergerCredentialIdentity` 任一、或 receipt.`mergedBy` 为空/缺失（缺失身份）；intent.`expectedMergedBy` 与凭据通道实际观察值不符（含以 `requestedBy` 或任何人类操作者身份顶替——身份混用）；当前凭据解析身份重算 digest ≠ intent.`mergerCredentialIdentity`（credential domain 漂移：gh binary、config dir 或认证账号被更换）；merge 执行 actor 的 securityDomainId ≠ intent.`mergerSecurityDomainId` | 全部 fail closed，不得 ACCEPTED；不得退化为「可归属 Publisher 侧」的模糊判定。正向对照：receipt.mergedBy canonical 编码逐字等于 intent.expectedMergedBy，且安全域、凭据解析身份与 intent 绑定一致时，归属核验通过 |

## 九、与旧裁决的关系（部分取代声明）

本 ADR 接受后，下列旧裁决条款被**部分取代**；未被点名的条款全部继续有效：

1. **[ADR 0007](0007-intent-first-publication.md)**：「MVP 只支持……Draft PR 与 `mergePolicy=never`。不提供 Force Push、Ready for Review、Merge、Release 或 Deploy 能力」——其中「仅 mergePolicy=never、不提供 Ready for Review 与 Merge」部分被取代：接受并实现后，`mergePolicy=policy` 在第一节最小 admission 全成立时获得受控 Ready for Review + Merge；该条款的其余部分（无 Force Push、无 Release/Deploy、Draft-only 作为 publish 路径语义、凭据隔离与 `expectedRemoteUrl` 防重定向）不变；
2. **[ADR 0010](0010-controlled-autonomy-and-intervention.md)**：Gate 节「Merge 在所有 Profile 中仍不可用」——被取代为：merge 仍不是任何 Autonomy Profile 的默认能力，只在冻结 Policy（`allowMerge=true`）显式授权且 human publish ApprovalRecord 有效时作为独立 credentialed side effect 可用；Plan/Publish Gate、ApprovalRecord 绑定与失效语义不变；
3. **[ADR 0026](0026-scm-merge-receipt-and-publication-reconcile.md)**：流程不变量「merge-never 不变：Marshal 不获得 merge 权限、不自动 merge」——被取代为：Marshal 可获得严格受限于本 ADR 契约的 merge 权限；`SCMMergeReceipt`/`PublicationReconcileRecord` 的记录契约、accept-after-merge reconcile 语义与「无本地 intent 只能 reconcile」边界逐字不变；
4. **[ADR 0018](0018-control-plane-and-provider-ports.md)**：PublicationAuthorization typed edge 绑定清单中的「merge-never」——接受后由 B2 切片冻结受控 merge 的授权绑定以部分取代该条目；授权绑定必须包含预期 merge 执行者 principal（`expectedMergedBy` canonical 编码）与 SCMMerger actor 侧 `securityDomainId`，并与 intent 同名字段联动核验；trust domain 隔离、default deny、current-ledger recheck 与双键空间不变；
5. **[AGENTS.md](../../AGENTS.md)** 的 universal 不变量「Merge 默认禁用，不属于 MVP 生命周期」及 [任务生命周期](../task-lifecycle.md)、[安全模型](../security-model.md) 中的相应表述：本 ADR 接受后须在 B1–B3 切片同步修订这些文档；本 ADR 与本切片不修改它们。

上述部分取代已随本 ADR 接受生效；实现与 supported 状态仍受 ADR 0033 及其退出门禁约束。

## 十、后续切片与串行约束

本 ADR 的实现拆为三个互斥后续切片，严格串行（B1 → B2 → B3），不得交叉合入：

| 切片 | 范围 |
| --- | --- |
| **B1：Schema/domain/policy contract** | `schemas/scm-merge-intent.schema.json`（Draft 2020-12）、`internal/domain` 的 `KindSCMMergeIntent` 与 Go 类型、contract catalog 注册与跨记录语义校验、规划门禁对 `mergePolicy=policy`/`allowMerge=true` 的受控放开（替换当前 `ErrPolicyMerge` 双锁为第一节 admission 的规划侧校验）、TaskSpec `mergeMethod` 字段与 happy-path/invalid fixture |
| **B2：publication/publisher/SCMMerger 与 lifecycle core** | `port.SCMMerger` 接口与 Publication 信任域接线、Publisher 侧凭据复用、admission/重新观察绑定服务（含 `expectedMergedBy`/`mergerSecurityDomainId`/`mergerCredentialIdentity` 的观察、冻结与执行时重观察）、intent/receipt 持久化事务、`publication.merged` 事件与 reducer/allowed-map/producer-authority 登记、Outcome 绑定扩展、崩溃恢复 C1–C7、PublicationAuthorization 受控 merge 授权绑定（ADR 0018 演进） |
| **B3：marshal task merge CLI、automation、fake/live E2E 与 operator docs** | `marshal task merge` 命令表面与状态门禁、fake SCMMerger conformance、live E2E（真实 Draft PR 的受控 ready+merge）、operator runbook 与 lifecycle/security 文档同步修订 |

串行理由：B2 的 reducer/事务依赖 B1 的 Schema 与 domain 类型；B3 的 CLI/E2E 依赖 B2 的端口与事件；交叉会造成 catalog/replay 与审计链的中间态。

与 adapter patch semver fingerprint ADR（另行独立提案，不属于本 ADR）的关系：两条线若都需要修改共享的 **ADR index（`docs/adr/README.md`）、`docs/audit-report.md` 或 schemas catalog 注册表**，这些共享表面的修改必须在两条线之间串行执行，避免索引/审计/schema 注册的冲突与审计链断裂；两条线的设计内容本身互不包含（本 ADR 不含任何 fingerprint 设计）。

## 后果

- 维护者授权的「审计 + CI 全绿后自动合并」获得唯一合法落地路径：Policy 显式授权、intent-first、独立端口、receipt 验证、权威收敛，全程 append-only、幂等、fail closed；
- merge 事实获得与 publish 同级别的可审计性：intent 绑定全部证据/授权 digest 与预期执行者身份（principal、安全域、凭据解析身份），receipt 绑定 head/base/commit/method/actor 并经 authorityNamespaceId/runId 双键空间与 publication 三重等值逐字核对，Outcome 绑定 intent/receipt digest；
- 旧 reconcile 边界保持完整：无本地 intent 的远端先行合并仍只能走 ADR 0026，受控 merge 与事后 reconcile 不互相冒充；
- Worker/Verifier/普通 Adapter 的凭据边界不放宽：GitHub credential 仍只存在于 Publisher 侧（SCMMerger 复用），环境构造负向断言；
- 成本：两份新 Schema 演进点（SCMMergeIntent 新增、Outcome 可选字段扩展）、一个新 Port、一个新事件类型与三个串行实现切片；
- 已声明的残留风险：base 分支在 Provider merge API 上无机械绑定参数。本 ADR 以「intent.baseOid 必须逐字等于 RunState 冻结 baseSha + 全部观察时点不等即 fail closed + base 前进必须 fresh rebase/rework 重走 Verification/Review/Approval」消除自证循环与静默放行；残留的有界 TOCTOU 窗口仅存在于「最后一次 base 观察通过 → Provider 实际执行 merge」之间，由 receipt `baseOid` 与冻结 baseSha 的事后核对兜底（不符即 fail closed，不得 ACCEPTED）。head 由 expectedHeadOid 机械 fencing，不存在同类残留；
- 本 ADR 的提出与接受均不改变 Roadmap 里程碑 M8–M13（与第一节 admission 条件编号无关）的实现状态，不构成 conformance 声明。

## 非目标

- 不实现 task merge、SCMMerger、Schema、Policy 或 lifecycle 代码（本切片只新增本文档）；
- ADR 设计切片本身不执行 merge，也不因 ADR 接受而改变任何业务 PR 的 Draft 状态；
- 不实现 `manual` 模式；
- 不提供 branch delete、PR close、force、bypass、auto-merge queue 或任何 admin 能力；
- 不把 adapter patch semver fingerprint 设计混入本 ADR；
- 不改变 ADR 0028 的 ciDeadline/completedAt 裁决、ADR 0030 的 CI 失败 rework 契约、ADR 0026 的记录字段与 reconcile 语义；
- 不放宽 Worker 不自证、Worker/Publisher 分权、ReviewDecision 精确证据绑定、human publish approval、Draft-only（对 publish 路径）与 fail closed 中的任何一项。

## 备选（已否决）

- **维护者或 Worker 直接 `gh pr merge`**：违反 universal 治理规则与 Worker/Publisher 分权；merge 事实无法绑定证据链与审计链；
- **以「merge 前后各观察一次 head」替代 expectedHeadOid 机械绑定**：观察间隙不构成 fencing，无法排除 head 漂移，不是原子门禁；
- **复用 PublicationIntent（ADR 0007）承载 merge 意图**：publish 与 merge 的授权来源、信任域操作与生命周期阶段不同；且 ready 也是远端副作用，同样需要 intent-first，复用会造成 intent 语义混叠；
- **把 SCMMergeReceipt 扩展为事前授权或执行记录**：receipt 是 ADR 0026 的事后观察/reconcile 证据，伪装成受控 merge 会摧毁「无本地 intent 只能 reconcile」的边界；
- **让 Worker/Verifier 持有 GitHub credential 执行 merge**：摧毁凭据边界与分权不变量；
- **使用 GitHub auto-merge queue 等远端托管自动化**：在 Marshal 权威之外执行 merge，无法绑定 intent/证据/fencing，绕开 required checks 的 Marshal 裁决；
- **仅凭 checks 全绿观察（无 receipt）收敛 CI_PENDING → ACCEPTED**：不能证明 merge 事实发生且归属于本 Run 的 head/base/method；
- **为 merge 新增 Run 状态（如 MERGING）**：现有 `CI_PENDING → ACCEPTED` 转换行加 policy 条件守卫即可表达；新增状态会扩大 replay/doctor/终态语义面，收益不成立。
