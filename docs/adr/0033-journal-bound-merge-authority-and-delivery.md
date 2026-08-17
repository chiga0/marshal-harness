# ADR 0033：Journal-bound Merge Authority Transaction 与 Delivery Anchor

- 状态：提议（Proposed）——未经维护者接受且 A–D 实现切片全部通过独立验证前，本 ADR 只是一份目标契约；不得据此宣称受控自动合并已支持、ADR 0032 B2 缺口已关闭或任何 Milestone 已完成
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
- `MergeDeliveryAnchor`：绑定某一 ready/merge delivery 的 pending attempt、预算消费、观察/结果与前一 anchor。

sidecar 文件、目录名、mtime、独立 head 文件或进程内 counter 都只能是可重建投影，不能成为权威。每个 authority fact 必须记录：

- `authorityNamespaceId`、`taskId`、`runId`、`journalSequence`；
- 对应局部 ledger 的连续 `ledgerSequence`；
- `previousAnchorDigest` 与 `anchorDigest`；
- 精确绑定的 intent、authorization、publication、review、verification、evidence、policy、approval、remote-check digest；
- 记录 kind、版本与封闭 payload。

`journalSequence` 是全局单调顺序；`ledgerSequence` 只表达同一 merge identity 内的局部顺序，不能替代 journal CAS。第一条局部记录的 `previousAnchorDigest=null`，其后必须逐字等于前一条 `anchorDigest`；`anchorDigest` 按 JCS canonicalize 后移除自身字段计算 `sha256`，格式固定为 `sha256:<64 lowercase hex>`。projection 缺失或落后时只从 journal 重建，绝不反向修补 journal。

### 2. Prepared intent 与 authorization 是一个原子事实

新增同状态事件：

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

### 3. Delivery 使用 pending→inspect/reconcile→resolved 状态机

新增同状态事件：

| 事件 | actor | 状态 | 语义 |
| --- | --- | --- | --- |
| `publication.merge-delivery-pending` | `publisher/marshal-scm-merger` | `CI_PENDING → CI_PENDING` | 在 mutation 前持久化 attempt、operation、预算消费与前一 anchor |
| `publication.merge-delivery-resolved` | `publisher/marshal-scm-merger` | `CI_PENDING → CI_PENDING` | 追加 observation-bound 结果；不得覆盖 pending |

执行顺序固定为：

1. 紧邻 mutation 的 `ObserveTarget` 与 current authority recheck；
2. 以 journal CAS 追加 `pending`，并原子消费一次 delivery budget；
3. **同步持久化包含该 sequence 的 Run snapshot**；snapshot 未成功不得 mutation；
4. 执行一次 credentialed mutation；
5. Inspect 外部真实状态并追加 `resolved`；之后才允许继续下一 operation 或生命周期收敛。

`resolved.outcome` 是封闭枚举：`succeeded|not-applied|permanent-failure|ambiguous|conflict`。Provider 原始输出不进入 journal、Outcome 或错误字符串。

恢复发现 unresolved pending 时必须先 Inspect/Reconcile：

- ready：目标已 ready 且全部 binding 匹配，追加 `succeeded`；仍为绑定一致的 Draft，追加 `not-applied` 后方可新建下一 attempt；漂移或关闭为 permanent/conflict；
- merge：先观察 receipt；匹配 receipt 追加 `succeeded`；明确未合并、仍 ready 且全部 binding 匹配时可追加 `not-applied`；Provider 返回 unknown、延迟可见或观察歧义时保持 pending/追加 `ambiguous`，不得 mutation replay；
- 已合并但 receipt 绑定不符：`conflict`，不得 `ACCEPTED`。

### 4. 预算不可通过投影回滚重置

delivery budget 只由 journal 中 `publication.merge-delivery-pending` 的数量派生，按 `(authorityNamespaceId, runId, intentDigest, operation)` 计数，固定最多三次。删除 projection、result 或本地 cache 不改变已消费预算；pending 即已消费，不能因为没有 result 返还。

同一 pending 的 Inspect/Reconcile 不消费新预算。只有上一 pending 已有 `not-applied`，且 current authority、target binding 与剩余预算均重新校验通过，才能追加下一 pending。

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
| 恢复时 wall clock/check observation 已变化 | hydrate 原 transaction；禁止重签不同 digest |
| pending 已写、snapshot 未同步成功 | 零 mutation |
| pending 已写、mutation 前崩溃 | Inspect；只有明确 `not-applied` 才可新 attempt |
| mutation 已生效、响应丢失 | Inspect 得到匹配远端事实并 resolved；不得重放 |
| merge observation 为 unknown/lag | 保持 pending/ambiguous；不得重放 |
| 删除 delivery projection/result | budget 仍由 pending journal facts 派生 |
| journal 尾删、snapshot 较新 | fail closed，禁止 mutation |
| journal+snapshot 协调回滚 | 明确记录为本地不可检测；production 必须由外部 rollback witness 覆盖 |
| 校验后替换 `gh` 路径目标 | 执行既有 fd/handle 或 fail closed；不得执行替换对象 |
| stdout/stderr 超限或 timeout | 终止进程组，记录 typed failure，原始输出不持久化 |

每个 fixture 必须同时断言：事件序列、snapshot digest、预算、外部 mutation 次数与 secret/path boundary。作者测试不能替代独立 reviewer 的权威验证。

## 实施切片与退出门禁

严格串行实施，前一切片未合入不得开始后一切片：

- **A：契约与 reducer**——新增事件/记录 Schema、closed payload、producer authority、same-state 命名 allowlist、journal replay 与 negative fixtures；
- **B：Authority transaction**——原子 prepared/revoked append、hydrate/re-entry、current-ledger recheck；
- **C：Delivery anchor**——pending/resolved、snapshot-before-mutation、Inspect/Reconcile、预算派生与 crash matrix；
- **D：Credentialed execution**——fd/immutable-handle 执行、process-group containment、GitHub fake/live conformance。

受控 merge 只有在 A–D 全部完成、独立审计 P0/P1 清零、required CI/secret scan 全绿并完成真实 lost-response/recovery conformance 后才可注册为 supported。部分切片合入不得启用 `mergePolicy=policy`。

## 后果

- 本 ADR 部分取代 ADR 0032 中“同故障域 monotonic head 可检测整体回滚”与“分步授权/投递记录足以关闭 crash window”的实现解释；ADR 0032 的 admission、证据绑定、独立 SCMMerger、receipt 与 fail-closed 原则继续有效；
- 当前 B2 记录仍可作为诊断 projection，但不能作为 production authority；
- authority transaction 与 delivery anchor 增加同步 journal/snapshot 写成本，换取重放安全与可审计恢复；
- 本 ADR 的提出或接受都不表示实现完成，不改变 M10 在途或 M11–M13 `PLANNED` 状态，不改变 Local MVP `USABLE`；
- Issue #160 的通用 outbox/运维投影仍可后续扩展，但不得绕过本 ADR 的 merge 专属权威事实。

## 非目标

- 不引入通用 same-state transition；仅登记本文列出的四个命名事件；
- 不允许 Worker/Verifier/普通 Agent 获得 Publisher/SCMMerger 凭据；
- 不允许 admin/force/bypass/branch delete；
- 不承诺用同一台主机的两个文件检测整机/整存储回滚；
- 不把 ADR 接受、Schema 落盘、单测通过或某个前置 PR 合入误报为 Milestone 完成。
