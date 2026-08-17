# ADR 0033：Journal-bound Merge Authority Transaction 与 Delivery Anchor

- 状态：提议（Proposed）——未经维护者接受且 A–D 实现切片全部通过独立验证前，本 ADR 只是一份目标契约；即使 A–D 通过，也只能启用显式 opt-in 的 local/non-production 受限 profile，production supported 仍须等待 M11 external rollback witness 通过恢复演练；不得据此宣称受控自动合并已全面支持、ADR 0032 B2 缺口已关闭或任何 Milestone 已完成
- 日期：2026-08-17
- 决策来源：[ADR 0032](0032-controlled-policy-merge.md) B2 独立复核发现：正文记录与 sidecar monotonic head 位于同一可回滚故障域，无法检测协调回滚；authorization→intent 与 delivery attempt→result 的分步写存在 crash dead zone；恢复入口还可能重新观察时间并为不同 digest 重签授权；凭据执行时只校验可替换路径，仍有 snapshot 自身 TOCTOU
- 关联：[ADR 0018](0018-control-plane-and-provider-ports.md)、[ADR 0019](0019-deterministic-control-plane-typed-execution-and-goal-admission.md)、[ADR 0026](0026-scm-merge-receipt-and-publication-reconcile.md)、[ADR 0032](0032-controlled-policy-merge.md)、公开 Issue #160

## 上下文

ADR 0032 冻结了受控 merge 的业务门禁，但 B2 的本地实现用内容寻址文件与同目录 sidecar head 模拟 append-only authority ledger。该结构可以检测单文件篡改，却不能证明整个目录没有被一起回滚或删尾；它也不能把“授权与 prepared intent”或“delivery pending 与预算消费”变成单一原子权威事实。

因此，以下结论必须 fail closed：

1. 同一故障域内的 record+head 不能充当外部单调见证；协调回滚仍可能恢复成内部自洽的旧状态；
2. authorization 先写、intent 后写时，崩溃恢复若重新读取 `requestedAt`、check freshness 或其他可变输入，会为不同 digest 重新签发授权；
3. mutation 前只写 attempt、mutation 后再写 result 时，崩溃可能留下“副作用是否发生未知”的 pending；恢复不能把 unknown 当作 not-applied 并盲重放；
4. 只冻结 `gh` 路径或复制出的 snapshot 路径，不能排除校验后可执行对象被替换。

在本 ADR 被接受并完整实现前，`mergePolicy=policy` 不得作为 production-supported 能力启用；`mergePolicy=never` 的默认行为不变。ADR 0032 的接受与既有实现提交都不改变该门禁。

## 决策

### 1. 权威锚只来自 Run journal

受控 merge 新增两类 journal-bound authority facts；它们必须与 Run 的权威事件使用同一 `authorityNamespaceId`、同一 append-only journal 与同一 `expectedSequence` CAS。事件及其引用的 authority bytes 必须在同一 authority-store transaction 中提交；Run snapshot 随后作为 mutation 前的同步持久化 barrier，不是第二业务权威：

- `MergeAuthorityTransaction`：原子绑定 prepared `SCMMergeIntent`、当前 `PublicationAuthorization` 或 revocation successor，以及全部 admission digest；
- `MergeDeliveryAnchor`：绑定某一 ready/merge delivery 的 pending attempt、预算消费、mutation fence consumption、观察/结果与前一 anchor；`MergeMutationFenceConsumption` 不是进程内 token，而是该 authority fact 的 `status=mutation-fence-consumed` 封闭变体。

sidecar 文件、目录名、mtime、独立 head 文件或进程内 counter 都只能是可重建投影，不能成为权威。每个 authority fact 必须记录：

- `authorityNamespaceId`、`taskId`、`runId`、`journalSequence`；
- 对应局部 ledger 的连续 `ledgerSequence`；
- `previousAnchorDigest` 与 `anchorDigest`；
- 精确绑定的 intent、authorization、publication、review、verification、evidence、policy、approval、remote-check digest；
- 记录 kind、版本与封闭 payload。

`journalSequence` 是全局单调顺序；`ledgerSequence` 只表达同一 merge identity 内的局部顺序，不能替代 journal CAS。第一条局部记录的 `previousAnchorDigest=null`，其后必须逐字等于前一条 `anchorDigest`；`anchorDigest` 按 JCS canonicalize 后移除自身字段计算 `sha256`，格式固定为 `sha256:<64 lowercase hex>`。projection 缺失或落后时只从 journal 重建，绝不反向修补 journal。

### 2. Prepared intent 与 authorization 是一个原子事实

新增同状态事件；两者都只能由 Core 在完成 Schema、binding、current-ledger 与 expectedSequence 校验后追加：

| 事件 | actor | 状态 | 语义 |
| --- | --- | --- | --- |
| `publication.merge-authority-prepared` | `system/marshal-core` | `CI_PENDING → CI_PENDING` | 同一 append 原子写入 prepared intent、authorization 与冻结 admission digests |
| `publication.merge-authority-revoked` | `system/marshal-core` | `CI_PENDING → CI_PENDING` | 追加 authorization successor；旧记录保留但不再 current |

prepared 事件冻结签名输入的精确 bytes，包括 `requestedAt`、fresh check observation、expiry 与全部 canonical digest。恢复只能 hydrate 并复用同一 journal fact：

- 同 canonical identity、同 digest：幂等返回既有 transaction；
- 同 identity、不同 digest：conflict，零副作用；
- 缺失或非 current authorization：fail closed；
- 不得在恢复时重新取时间、重新观察 freshness 后补签或重签。

撤销只能通过 `publication.merge-authority-revoked` 追加 successor；删除或改写旧授权不构成撤销。每次 credentialed mutation 前必须用当前 journal snapshot 重新校验 transaction 仍为 current。

### 3. Delivery 使用 pending→fence-consumed→inspect/reconcile→resolved 状态机

新增同状态事件：

| 事件 | actor | 状态 | 语义 |
| --- | --- | --- | --- |
| `publication.merge-delivery-pending` | `system/marshal-core` | `CI_PENDING → CI_PENDING` | Core 在 mutation 前持久化 attempt、operation、预算消费与前一 anchor |
| `publication.merge-mutation-fence-consumed` | `system/marshal-core` | `CI_PENDING → CI_PENDING` | Core 原子消费唯一 mutation fence，追加 journal-bound `MergeDeliveryAnchor(status=mutation-fence-consumed)`；handoff 前必须持久化 |
| `publication.merge-delivery-observed` | `system/marshal-core` | `CI_PENDING → CI_PENDING` | Core 接纳一次 typed Inspect observation；unknown/lag 时 pending 仍 unresolved，可继续 Inspect |
| `publication.merge-delivery-resolved` | `system/marshal-core` | `CI_PENDING → CI_PENDING` | Core 只在 observation 可确定裁决时追加结果；不得覆盖 pending |

SCMMerger/Publisher 只能返回封闭的 typed observation，并作为 `sourceActor=publisher/marshal-scm-merger` provenance 被记录；它无权追加 authority journal、消费预算/fence 或宣布 resolved。Core 接纳前必须逐项验证 source Port/protocol、pending/intent/operation/attempt identity、target observation、authorization/security domain、Provider principal、observation digest 与 current journal sequence；任一不符即拒绝，零 authority append。上述四个 delivery 事件的 producer-authority 表只登记 `system/marshal-core`，same-state allowlist 只接受事件表中的精确 event type、actor 与 `CI_PENDING → CI_PENDING` 组合；其它 actor、状态或未命名同状态事件全部拒绝，replay 也执行同一校验。

`publication.merge-mutation-fence-consumed` 的 payload Schema 固定 `additionalProperties=false`，以下字段全部 required，不允许 nullable 或默认补值：

| 字段 | 封闭约束 |
| --- | --- |
| `schemaVersion` | `const: 1` |
| `recordKind`、`status` | 分别为 `const: MergeDeliveryAnchor` 与 `const: mutation-fence-consumed` |
| `authorityNamespaceId`、`taskId`、`runId` | 必须逐字等于事件 envelope 与 current Run identity |
| `journalSequence` | 正整数，必须等于本次 append 后事件 envelope 的 authority journal sequence |
| `expectedPreviousJournalSequence` | 正整数，必须等于 `journalSequence-1` 与 CAS 输入；不得跳号 |
| `ledgerSequence` | 正整数，必须等于同一 merge anchor lineage 的前一值加一 |
| `previousAnchorDigest`、`pendingAnchorDigest` | `sha256:<64 lowercase hex>`；两者必须相等并指向 current unresolved pending anchor；不得跨 pending/operation 接链 |
| `anchorDigest` | `sha256:<64 lowercase hex>`；按 §1 JCS 规则覆盖完整 payload |
| `canonicalReplayIdentity` | `sha256:<64 lowercase hex>`，值固定为 `sha256(JCS([eventType,authorityNamespaceId,runId,pendingAnchorDigest,intentDigest,operation,deliveryAttempt]))`；同 identity 只允许一个 consumption |
| `operation` | `enum: ready|merge` |
| `deliveryAttempt` | `integer: 1..3`，必须等于 pending 冻结值 |
| `intentDigest`、`authorizationDigest`、`publicationDigest`、`reviewDecisionDigest`、`verificationDigest`、`evidenceDigest`、`policyDigest`、`approvalDigest`、`remoteCheckDigest` | 均为 `sha256:<64 lowercase hex>`，逐字等于 prepared authority transaction 与 current pending 的冻结 binding |
| `expiresAt`、`consumedAt` | RFC 3339 UTC；`consumedAt < expiresAt`，恢复不得重取时间或改写 |
| `providerRequestDigest` | `sha256:<64 lowercase hex>`，绑定即将 handoff 的精确 SCMMerger operation、target 与 canonical request bytes |

`MergeDeliveryAnchor.status` 的封闭枚举因此是 `pending|mutation-fence-consumed|observed|resolved`。reducer 只在 current unresolved pending、authorization/intent/check 仍 current、expiry 未到、`expectedPreviousJournalSequence` 命中且该 `canonicalReplayIdentity` 尚未消费时接受 fence 事件；接受后不改变 Run 状态、不返还或再消费 delivery budget，只推进 journal/ledger sequence、anchor lineage，并在 snapshot 中保存 consumed identity、anchor digest 与 provider request digest。同 identity+同 digest 的重放幂等返回既有 fact，但调用方必须进入 crash hydration/Inspect，不能再次 handoff；同 identity+不同 digest、第二个 concurrent consume 或错 lineage 一律 conflict，零 append、零 mutation。

replay 与 crash hydration 必须仅从 journal 重建上述 reducer 状态：看见 pending 而未看见 fence consumption 时仍须重新执行 current/expiry recheck 后竞争唯一 fence；看见 fence consumption 时，无论有没有进程内 handoff 标记或 Provider response，都按“可能已 handoff”恢复，只能 Inspect/Reconcile，绝不重新 consume 或 replay mutation。projection、内存锁与进程日志不能改变该裁决。

执行顺序固定为：

1. 紧邻 mutation 的 `ObserveTarget`：SCMMerger 返回封闭 typed preflight observation，Core 校验 source provenance、target binding、observation digest 与 current authority；
2. 以 journal CAS 追加 `pending`，并原子消费一次 delivery budget；
3. **同步持久化包含该 sequence 的 Run snapshot**；snapshot 未成功不得 mutation；
4. snapshot 成功后、credentialed mutation 前，Core **必须同时执行** mutation-adjacent recheck **AND** single-use fence，二者不可互相替代：在 authority store 的同一 serialization key/serializable ordering 内再次读取 journal，精确核对 pending 仍 unresolved/current、journal sequence 未变化、authorization 未撤销且未过期、intent/target/check binding 仍 current；随后 CAS 追加上述 `publication.merge-mutation-fence-consumed` 与 journal-bound `MergeDeliveryAnchor(status=mutation-fence-consumed)`；任一值变化、expiry 已到或 fence identity 已消费必须零 mutation；
5. authority journal transaction 必须先 durable commit，并**同步持久化包含 fence `journalSequence`、consumed identity 与 anchor digest 的 Run snapshot**；journal commit 或该 snapshot barrier 任一未完成都不得 Provider handoff。恢复以 journal 为权威重建 snapshot，不得因 snapshot 落后重放 consumption 或 mutation；
6. `PublicationAuthorization` revoke、其它会改变 current authority 的 append、fence consumption 与 fence→Provider handoff 必须共享上述同一线性化/serializable ordering，Core 从 fence consumption、durability barrier 到把调用交给 SCMMerger 期间不释放该 serialization key。顺序裁决固定为：revoke/authority append 先线性化，则 mutation-adjacent recheck 失败并零 mutation；fence consumption+Provider handoff 先线性化，则该次 mutation 已先获得授权，后到 revoke 只能阻止后续 mutation，并把本次可能结果留给 pending Inspect/Reconcile。fence 已消费但 handoff 响应未知（含进程在 durability/handoff 边界崩溃）一律按“可能已 handoff”处理，绝不重放；
7. 执行一次 credentialed mutation；调用已进入 Provider 后才释放 serialization key，之后的进程崩溃或响应丢失由 unresolved pending 承担；M11 多节点实现必须用同一 authority store 的 fenced lease/serializable transaction 提供等价保证，进程内 mutex 不构成 HA fence；
8. Inspect 外部真实状态，把 SCMMerger typed observation 交给 Core 校验；Core 先追加 `observed`，只有结果可确定时再 CAS 追加 `resolved`；observed 的 `previousAnchorDigest` 必须接到 fence consumption anchor（其后观察继续逐条接链），之后才允许继续下一 operation 或生命周期收敛。

`resolved.outcome` 是封闭枚举：`succeeded|not-applied|permanent-failure|conflict`。`unknown|lag` 是 `observed.observationClass` 的非终态取值，永远不能伪装成 resolved outcome。Provider 原始输出不进入 journal、Outcome 或错误字符串。

恢复发现 unresolved pending 时必须先 Inspect/Reconcile：

- ready：目标已 ready 且全部 binding 匹配，Core 追加 observed+`succeeded`；仍为绑定一致的 Draft，Core 追加 observed+`not-applied` 后方可新建下一 attempt；漂移或关闭形成可确定 permanent/conflict；
- merge：先观察 receipt；匹配 receipt 时 Core 追加 observed+`succeeded`；明确未合并、仍 ready 且全部 binding 匹配时可追加 observed+`not-applied`；Provider 返回 unknown、延迟可见或观察歧义时只追加 `publication.merge-delivery-observed(observationClass=unknown|lag)`，pending 保持 unresolved，可用同一 pending 重复 Inspect，且不得 mutation replay；
- 已合并但 receipt 绑定不符：`conflict`，不得 `ACCEPTED`。

### 4. 预算不可通过投影回滚重置

delivery budget 只由 journal 中 `publication.merge-delivery-pending` 的数量派生，按 `(authorityNamespaceId, runId, intentDigest, operation)` 计数，固定最多三次。删除 projection、result 或本地 cache 不改变已消费预算；pending 即已消费，不能因为没有 result 返还。

同一 pending 的 Inspect/Reconcile 与重复 `observed(unknown|lag)` 不消费新预算。只有上一 pending 已有 `not-applied`，且 current authority、target binding 与剩余预算均重新校验通过，才能追加下一 pending。

每个 pending 还必须冻结 `reconcileDeadline` 与确定性 backoff schedule；重复 Inspect 不能无限悬挂。deadline 到期仍只有 unknown/lag 时，Core 以 actor `system/marshal-core` 追加绑定 pending 与全部 observation anchors 的既有 typed `publication.blocked`，`terminalReason=merge-delivery-reconcile-deadline-exceeded`，并原子写入 `BLOCKED` Outcome；Outcome 必须绑定 intent、authorization、pending、全部 observed anchor、最后 observation digest、deadline 与 budget digest。停止后续 mutation，但不伪造 `resolved`。

deadline 后只允许只读 late-receipt reconcile。若之后观察到精确匹配的 `SCMMergeReceipt`，Core 必须复用 [ADR 0026](0026-scm-merge-receipt-and-publication-reconcile.md) 的唯一 `BLOCKED → ACCEPTED` 终态例外，并在**同一 authority-store transaction/CAS** 中：

1. 校验 receipt、原 intent/authorization、PublicationRecord、ReviewDecision、required checks、pending、全部 observed anchors 与 deadline BLOCKED Outcome 的 digest；
2. 追加 `MergeDeliveryAnchor(status=resolved,outcome=succeeded,resolutionKind=late-receipt)`，其 `previousAnchorDigest` 指向最后 observation anchor，从而权威关闭 pending；
3. 追加 ADR 0026 `SCMMergeReceipt` 与 `PublicationReconcileRecord(reconcileType=accept-after-merge)`，其 `evidenceDigests` 必须额外包含 pending、late-receipt resolution anchor 与旧 BLOCKED Outcome digest；
4. 追加 `publication.reconciled` 并写入新的 `ACCEPTED` Outcome，旧 BLOCKED Outcome 只归档不删除。

任一步失败则全部不落账，Run 保持 `BLOCKED` 且 pending 未关闭；不得创建第二个 mutation。late observation 若明确证明 not-applied，也只能在同一 reconciliation transaction 中追加 `resolved/not-applied` anchor 并保持 `BLOCKED`，禁止续作；其它 unknown/conflict 继续保留为只读证据，不能称恢复完成。

### 5. 回滚与删尾检测边界

- projection 删除/回滚：从 journal+snapshot 重建；projection 不参与权威裁决；
- journal 尾删但 snapshot 保留更高 `journalSequence`/digest：启动与 mutation admission 必须检测并 fail closed；
- snapshot 尾删但 journal 完整：从 journal 重建 snapshot；
- journal 与 snapshot 协调回滚：这是整个 authority store 的回滚，本地同故障域文件无法诚实检测。M11 production storage 必须提供外部单调见证、WAL/备份世代或等价 rollback witness，并验证恢复演练；在此之前只能声明“检测不协调删尾”，不得声明“检测整体回滚”。

### 6. 关闭 executable snapshot TOCTOU

credentialed mutation 必须在一次打开中完成身份确认与执行：

1. 以绝对路径、无 symlink 跟随策略打开 `gh`；
2. 校验文件类型、owner/mode、digest 与 capability probe；
3. 通过继承的只读 fd、平台 immutable handle 或等价内核对象执行同一已校验对象；不得关闭后再按路径打开；
4. config root 同样使用打开后的目录 handle 约束解析，不接受 mutation 期间的路径替换；
5. stdout/stderr 有界流式消费，超限终止整个子进程组；持久化只保留 typed failure class，不保留原始输出。

平台无法提供同对象执行语义时，SCMMerger capability 必须为 unsupported；不得退化成“校验路径后执行路径”。

## 负向恢复矩阵（实施门禁）

| 场景 | 必需结果 |
| --- | --- |
| authorization 已准备但 intent 未成为同一 journal fact | Schema/reducer 不允许表达；零 mutation |
| prepared transaction 同 identity 不同 digest 重放 | conflict；零追加、零 mutation |
| Publisher 直接提交 pending/fence-consumed/observed/resolved event | producer-authority 拒绝；零 authority append |
| preflight typed observation 的 provenance/digest/target 不匹配 | Core 拒绝 pending；零 budget 消费、零 mutation |
| 恢复时 wall clock/check observation 已变化 | hydrate 原 transaction；禁止重签不同 digest |
| pending 已写、snapshot 未同步成功 | 零 mutation |
| snapshot 已同步、mutation 前 authorization/journal/expiry 变化 | mutation-adjacent recheck **AND** single-use fence 任一失败即零 mutation |
| 两个 Core 调用并发消费同一 canonical fence identity | 仅一个 CAS 追加 fence authority fact；另一个幂等 hydrate 或 conflict，但均不得第二次 handoff；mutation 总次数最多 1 |
| revoke 先于 fence→Provider handoff 线性化 | revoke 提交；recheck/fence 拒绝，零 mutation |
| fence→Provider handoff 先于 revoke 线性化 | 本次 mutation 先获授权并最多执行一次；后到 revoke 只阻止后续 mutation，结果由同一 pending 对账 |
| fence journal commit 后、fence snapshot barrier 前崩溃 | restart 从 journal hydrate consumption、重建 snapshot、Inspect/Reconcile；零 handoff replay |
| fence 已消费并 durable、handoff 边界崩溃后 restart | hydrate consumed identity/providerRequestDigest，按可能已 handoff 处理；Inspect/Reconcile，绝不重新 consume 或重放 mutation |
| pending 与 pending snapshot 已 durable、journal 明确不存在 fence consumption，进程在消费 fence 前崩溃 | restart hydrate 同一 pending，重新执行 current/expiry recheck 并竞争唯一 fence；只有 CAS 胜者完成 fence durability barrier 后可 handoff 一次，不先 Inspect，也不新建 attempt |
| mutation 已生效、响应丢失 | Inspect 得到匹配远端事实并 resolved；不得重放 |
| merge observation 为 unknown/lag | Core 追加非终态 observed anchor，pending 保持 unresolved；可重复 Inspect，不得重放 mutation |
| restart 后首次 Inspect=lag、后续 receipt 可见 | 第一次只追加 observed(lag)；重启后以同一 pending 再 Inspect，匹配 receipt 后 observed+resolved(succeeded)；mutation 总次数仍为 1 |
| reconcileDeadline 到期仍为 unknown/lag | 追加 observation-bound `publication.blocked`，pending 不伪造 resolved，mutation 总次数不增加 |
| deadline 后出现匹配 late receipt | 单一 CAS 原子追加 late-receipt resolution anchor、ADR 0026 receipt/reconcile、`publication.reconciled` 与 ACCEPTED Outcome；pending 被关闭，旧 BLOCKED Outcome 保留 |
| deadline 后 late receipt 任一 binding 不符或事务中断 | 全部不落账；保持 BLOCKED/pending，零 mutation |
| 删除 delivery projection/result | budget 仍由 pending journal facts 派生 |
| journal 尾删、snapshot 较新 | fail closed，禁止 mutation |
| journal+snapshot 协调回滚 | 明确记录为本地不可检测；production 必须由外部 rollback witness 覆盖 |
| 校验后替换 `gh` 路径目标 | 执行既有 fd/handle 或 fail closed；不得执行替换对象 |
| stdout/stderr 超限或 timeout | 终止进程组，记录 typed failure，原始输出不持久化 |

每个 fixture 必须同时断言：事件序列、snapshot digest、预算、外部 mutation 次数与 secret/path boundary。作者测试不能替代独立 reviewer 的权威验证。

## 实施切片与退出门禁

严格串行实施，前一切片未合入不得开始后一切片：

- **A：契约与 reducer**——新增六个命名事件/两类 authority record Schema（含 fence consumption closed payload）、producer authority（仅 Core）、same-state 命名 allowlist、journal/anchor sequence reducer、canonical replay identity、crash hydration 与 negative fixtures；
- **B：Authority transaction**——原子 prepared/revoked append、hydrate/re-entry、current-ledger recheck；
- **C：Delivery anchor**——pending/fence-consumed/observed/resolved、pending 与 fence 两次 durability-before-handoff barrier、concurrent consume、consume→crash→restart no-replay、Inspect/Reconcile、预算派生与 crash matrix；
- **D：Credentialed execution**——fd/immutable-handle 执行、process-group containment、GitHub fake/live conformance。

受控 merge 在 A–D 全部完成、独立审计 P0/P1 清零、required CI/secret scan 全绿并完成真实 lost-response/recovery conformance 后，最多只能注册为显式 opt-in 的 `local-nonproduction` 受限 profile。production supported 必须额外等待 M11 external rollback witness、跨节点 fenced lease 与协调回滚恢复演练全部通过；部分切片合入不得启用 `mergePolicy=policy`，不得使用无限定的 supported 表述。

## 后果

- 本 ADR 部分取代 ADR 0032 中“同故障域 monotonic head 可检测整体回滚”与“分步授权/投递记录足以关闭 crash window”的实现解释；ADR 0032 的 admission、证据绑定、独立 SCMMerger、receipt 与 fail-closed 原则继续有效；
- 当前 B2 记录仍可作为诊断 projection，但不能作为 production authority；
- authority transaction 与 delivery anchor 增加同步 journal/snapshot 写成本，换取重放安全与可审计恢复；
- 本 ADR 的提出或接受都不表示实现完成，不改变 M10 在途或 M11–M13 `PLANNED` 状态，不改变 Local MVP `USABLE`；
- Issue #160 的通用 outbox/运维投影仍可后续扩展，但不得绕过本 ADR 的 merge 专属权威事实。

## 非目标

- 不引入通用 same-state transition；仅登记本文列出的六个命名事件；
- 不允许 Worker/Verifier/普通 Agent 获得 Publisher/SCMMerger 凭据；
- 不允许 admin/force/bypass/branch delete；
- 不承诺用同一台主机的两个文件检测整机/整存储回滚；
- 不把 ADR 接受、Schema 落盘、单测通过或某个前置 PR 合入误报为 Milestone 完成。
