# ADR 0053：Pre-R4 合同门禁四项与单一恢复模型实现（R4，修订 ADR 0044 冷热路径条款）

- 状态：已接受（Accepted，2026-08-27；原编号 0050，因与远端 ADR 0050 并行占用改为 0053）；接受依据：维护者授权的 I186 快速收敛治理（Lead 直接冻结、停用独立 reviewer 轮转）。接受冻结本合同与 `internal/hotpath`、`internal/jitgate`、`internal/protocolrev`、`internal/candidateid`、`internal/recovery` 收敛域实现及负测；不把任何生产接线（R5）、`marshal explain run` CLI wiring（等价 API 已由 `internal/recovery` 渲染模型冻结，CLI 归 R5/R6）或 conformance 终态提前为完成。
- 关联：[ADR 0018](0018-control-plane-and-provider-ports.md)、[ADR 0027](0027-candidate-record-and-verification-write-scope.md)、[ADR 0044](0044-result-ingress-and-cold-hot-paths.md)（本 ADR 修订其冷热路径条款）、[ADR 0045](0045-strangler-cutover-and-single-recovery.md)（决策 2 的实现冻结，本 ADR 不重复其语义）、[ADR 0049](0049-location-attestation-and-failure-classification-authority.md)（本 ADR 决策 5 消费其失败分类合同）、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)「进入 R4 前置合同门禁」四项、审计 finding `I186-ARCH-HOT-PATH-AUTHORITY`、`I186-ARCH-JIT-ADMISSION-RECHECK`、`I186-ARCH-PROTOCOL-REVISION-MIGRATION`、`I186-ARCH-CANDIDATE-IDENTITY`。

## 背景

Issue #186 的 Pre-R4 contract gate 要求：R4 启动前，hot-path authority、JIT admission recheck、protocol revision migration、Candidate identity 四项必须「有合同、正反证据」。同一批复审确认：R4 的单一恢复模型（ADR 0045 决策 2 只冻结方向）需要实现层冻结才能作为 R5 cutover 的恢复语义基线。本 ADR 一次性冻结五项合同；实现证据全部为收敛域纯新增包（零既有包修改，与 R3 切片同纪律）。

## 决策

### 1. 热路径 authority（修订 ADR 0044 冷热路径条款）

1. 接纳路径分型为一等事实：`Admission{Digest, Kind, Channel(hot|cold), LedgerSequence}` 经 `AdmissionLedger` put-if-absent 入账；同 digest 内容幂等，同 digest 不同内容 fail closed（绝不改写）。
2. **业务 kind（worker-result/candidate/evidence-ref/receipt/assessment）只允许 cold**：把业务 kind 记入 hot channel 在入账时即 fail closed（`ErrHotPathForbidden`）——热路径不产生冷路径不可复现的 authority，从「事后不可解释」前移为「入账即禁止」。checkpoint/heartbeat/log 可按意图选择 hot（轻便、不可 Restore）或 cold（可 Restore）。
3. **热路径永不续权、永不定 fencing**：authority effect（extend-lease/bump-generation/decide-fencing）只允许作用于 cold 接纳；record-observation 双路均可。
4. **Restore 门禁**：`ConsumeForRestore` 只接受 cold 通道接纳的 checkpoint；未知 digest、非 checkpoint kind、hot 接纳的 checkpoint 一律 fail closed。同 digest 先 hot 后 cold 的「洗路径」尝试以入账冲突 fail closed。
5. ADR 0044「热路径只做最小 fencing/replay 校验、不承担完整 evidence 校验成本、最终必须可被冷路径核对吸收」与本条并存：本 ADR 把「可吸收」细化为可判定的 Restore 谓词与 effect 门禁，不改变 ADR 0044 的 DRC 冻结定义与双路径共享 ledger 语义。

### 2. JIT admission provision 前重验

1. provision admission 显式化为 `AdmissionToken`：`DecisionID + RegistrationID + SnapshotDigest + PolicyDigest + ValidUntilUnix + canonical TokenDigest`；token digest 可重算，篡改 fail closed。
2. Provision 前强制重验五项（缺一拒绝）：registration 当前 active；当前 active snapshot digest 与 token 钉住的一致（generation/supersede 的收敛化表达）；current Policy active；current policy digest 与 token 钉住的一致；`now < ValidUntilUnix`（半开区间 `[issue, validUntil)` 为冻结规则）。
3. 结构性问题（token 篡改、view 畸形）是硬错误；生命周期/Policy 失效是带封闭原因码的业务拒绝（`registration-inactive/snapshot-superseded/policy-rotated/policy-inactive/token-expired`），两类不得混流。

### 3. Protocol revision migration

1. `Revision` 解析冻结：`family/version`（末位 `/` 分割）；`acp` → 无版本（legacy/unversioned）；`acp/v1` → 版本 `v1`；空 family、空版本、多 `/`、空串一律 fail closed。
2. **Pinned revision admission 精确匹配**：presented 必须 family+version 全等于 pin；unversioned 历史值（如 `acp`）永不满足 pinned revision（含 family 相同的情形）；pin 自身必须 versioned。
3. **历史永不重写**：迁移只以 `SupersedeMigration` 新增事实落地——`FromDigest ≠ ToDigest`（不允许原地改写）；两侧同协议族（`acp→acp/v1` 合法，`acp→other/v1` 拒绝）；To 必须 versioned 且与 From 不同；携带迁移授权 provenance digest；MigrationDigest 可重算。
4. `HistoryGuard` 防重写：FromDigest 须已冻结在册（未冻结不可迁移），ToDigest 须全局未出现（须真新）；迁移成功不改写任何记录，ToDigest 由调用方随后入账。

### 4. Candidate 独立 identity

1. `CandidateID = "candidate:<64-hex>"`，hex 由 `(ContentDigest, RecordDigest)` canonical 派生；`OriginAttemptID` 只作 provenance，不进入 identity——同一内容+记录 digest 来自不同 Attempt 收敛为同一 CandidateID（Attempt→Candidate 1:1 不固化的构造性证明）。
2. `IdentityLedger` put-if-absent：同 ID 同内容幂等，同 ID 不同内容 fail closed。
3. **Evidence 绑定 identity 而非 attempt**：`BindEvidence` 要求 identity 先行冻结（证据不得绑定未冻结身份）；同一 evidence digest 换绑不同 CandidateID 一律 `ErrEvidenceRebound` fail closed。
4. 兼容迁移：`MigrateLegacyReference` 单向生成 identity（legacy task/run/attempt 三元组只作 provenance 记录），幂等可回放。
5. **不启用多 Candidate fan-out**：本合同只冻结独立 identity 与证据绑定；fan-out 需要另行 ADR。

### 5. 单一恢复模型实现冻结

ADR 0045 决策 2 的方向由 `internal/recovery` 实现冻结：纯函数 `Decide(RecoveryInput) (Decision, Explanation, error)` 覆盖故障矩阵八类（duplicate delivery、lost response、process death、provider restart、network partition、stale result、partial artifact、ambiguous side effect），每类唯一幂等结论；不能证明安全一律 fence + new Attempt；ambiguous side effect 强制幂等键对账（杜绝重复 Publication）；infra 分类的放宽只来自 ADR 0049 决策 2 的 authority-observed 输入（本 ADR 不重新打开分类权）。`Explanation` 权威模型与 `Render` 文本出口即「等价 API」；`marshal explain run` CLI wiring 与真实 ledger 装配归 R5/R6。本条款不重述决策表全文，实现即冻结语义，`internal/recovery/decide.go` 的决策表注释为唯一权威解释。

## 后果与门禁

- 收敛域实现与负测：`internal/hotpath`（入账分型、effect 门禁、Restore 门禁、洗路径冲突）、`internal/jitgate`（token 五要素重验、半开区间边界、错误/拒绝分流）、`internal/protocolrev`（解析矩阵、pinned 精确、迁移合法性、防重写守卫）、`internal/candidateid`（identity 派生与收敛、证据绑定/换绑拒绝、legacy 迁移幂等）、`internal/recovery`（故障矩阵 8 类唯一结论、幂等性、malformed fail closed、Render 复盘要素）。全部纯新增、fail closed、无真实进程/网络；时钟以注入参数提供（仅 jitgate/recovery 需要）。
- 四项 Pre-R4 finding 的合同层面由决策 1–4 分别关闭；`I186-ARCH-RESOURCE-CLASSIFICATION-AUTHORITY` 随决策 5 对 ADR 0049 分类的消费转全关闭。finding 状态以 `docs/audit-report.md` 登记为准。
- 生产接线（resultingress/sandbox.Restore、dispatch/provision、capability supersede、Candidate 引用换指、CLI explain）统一归 I186-R5 strangler cutover；本 ADR 不改变任何既有生产路径行为。
- R4 Exit Gate（ADR 0045）：故障矩阵每类唯一幂等结论、explain 可独立复盘——由 `internal/recovery` 测试与渲染模型满足；「Inspect/Reconcile/Cancel/Terminate 与 current lease resolver」的 Provider 侧接线归 R5。
- Local MVP 零回退：零既有包修改，M0–M6 回归基线不受影响；本 ADR 不升级 M10–M13 或 v1.0 状态。
