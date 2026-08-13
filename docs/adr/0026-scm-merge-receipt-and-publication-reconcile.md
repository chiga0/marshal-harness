# ADR 0026：SCMMergeReceipt 与 PublicationReconcileRecord（已合并 PR 的权威 reconcile 契约）

- 状态：Accepted（2026-08-12，维护者合入 PR #49）
- 日期：2026-08-12
- 决策来源：公开 [Issue #25](https://github.com/chiga0/marshal-harness/issues/25) / [PR #24](https://github.com/chiga0/marshal-harness/pull/24) 暴露「全部 required checks 成功且 PR 已合并后，Run 被 `marshal task accept` 永久置为 terminal `BLOCKED`」；审计 finding `PUBLICATION-MERGED-HEAD-RECONCILE-P1`（见 docs/audit-report.md「Issue #25 发布合并后 head reconcile 审计增补（2026-08-12）」节）

## 上下文

公开 [Issue #25](https://github.com/chiga0/marshal-harness/issues/25) 与 [PR #24](https://github.com/chiga0/marshal-harness/pull/24) 暴露：当全部 required checks 成功且 PR 已合并进入 main 后，现有 `marshal task accept` 仍要求 PR 处于 OPEN/Draft，会把 Run 永久置为 terminal `BLOCKED`。审计对该问题分两层定位：

1. **nonterminal implementation bug**——`accept` 以 PR OPEN/Draft 为前置条件，未区分「merge 前需 OPEN」与「merge 后已合并」两种语义，是可修复的实现缺陷；本 ADR 不实现代码，只冻结契约，该缺陷的修复与本契约的实现一并归后续 Milestone；
2. **terminal contract gap**（finding `PUBLICATION-MERGED-HEAD-RECONCILE-P1`）——当前 Schema/命令缺少不可变 `SCMMergeReceipt` 与 append-only `PublicationReconcileRecord`，无法把已合并 Run 从 `BLOCKED` 安全迁移到 `ACCEPTED`；这是契约级缺口，由本 ADR 定义。

关键事实：GitHub PR 节点在 merge 后仍保留原 head OID、base OID 与 merge commit；head branch 删除不是权威 head SHA 丢失，merge 事实可以在事后被权威对账。因此缺的不是数据可得性，而是承载该事实的不可变权威记录与安全迁移契约。

## 决策

冻结两个权威记录与一条 reconcile 流程。两个记录的字段名、类型与约束与配套 Draft 2020-12 JSON Schema（`schemas/scm-merge-receipt.schema.json`、`schemas/publication-reconcile-record.schema.json`，`apiVersion=marshal.dev/v1alpha1`，`kind` 分别为 `SCMMergeReceipt` 与 `PublicationReconcileRecord`）逐字对齐。两个记录均为 authority ledger 事实，归属 `authorityNamespaceId`（见 ADR 0018 权威/actor 双键空间），只允许 Core 写入。本 ADR 定义的 `BLOCKED → ACCEPTED` 迁移不是普通生命周期转换，而是由这两个权威记录与 current-ledger recheck 共同门禁的 typed reconciliation，不放宽任何其他终态语义。

### SCMMergeReceipt（不可变权威记录）

SCMMergeReceipt 是 merge 事实的权威证据，一经写入不可改写；accept reconcile 必须消费它。

| 字段 | 类型与约束 | 语义 |
| --- | --- | --- |
| `receiptId` | string，非空，canonical 身份 | receipt 唯一身份 |
| `authorityNamespaceId` | string，非空 | 权威记录归属 |
| `runId` | string，非空 | 所属 Run |
| `publicationRecordId` | string，非空 | 关联的既有 PublicationRecord |
| `repositoryRef` | string，非空 | 仓库远程引用 |
| `prNumber` | integer，≥1 | PR 编号 |
| `headOid` | string，非空 SHA | 合并前 head SHA |
| `baseOid` | string，非空 SHA | 合并前 base SHA |
| `mergeCommitSha` | string，非空 SHA | merge commit SHA |
| `mergedAt` | string，RFC 3339 | merge 时间 |
| `mergedBy` | string，非空 | 合并者身份 |
| `mergeMethod` | enum：merge \| squash \| rebase | merge 方式 |
| `capturedAt` | string，RFC 3339 | receipt 采集时间 |
| `receiptDigest` | string，canonical digest | receipt 内容的 canonical digest |

### PublicationReconcileRecord（append-only 权威记录）

PublicationReconcileRecord 是 append-only：只能新增，不能改写或删除。

| 字段 | 类型与约束 | 语义 |
| --- | --- | --- |
| `reconcileId` | string，非空 | reconcile 记录唯一身份 |
| `authorityNamespaceId` | string，非空 | 权威记录归属 |
| `runId` | string，非空 | 被 reconcile 的 Run |
| `scmMergeReceiptId` | string，非空 | 关联的 SCMMergeReceipt `receiptId` |
| `reconcileType` | enum：accept-after-merge | reconcile 类型 |
| `observedState` | 封闭枚举：BLOCKED | reconcile 时观测到的 Run 状态 |
| `decidedState` | 封闭枚举：ACCEPTED | reconcile 后的目标状态 |
| `evidenceDigests` | digest 集合，非空 | 关联证据摘要集合，含原 PublicationRecord、ReviewDecision、required-check 摘要 |
| `reconcileReason` | string，非空，机器可读原因码 | 封闭原因码，非自由文本 |
| `reconciledBy` | string，非空 | reconcile 执行者身份 |
| `reconciledAt` | string，RFC 3339 | reconcile 时间 |
| `reconcileRecordDigest` | string，canonical digest | 记录内容的 canonical digest |

幂等身份 canonical 绑定 `(authorityNamespaceId, runId, scmMergeReceiptId, reconcileType)`：同身份重复提交归并（幂等）；同身份但 `evidenceDigests` 等关键内容不同视为冲突，一律 fail closed。

### accept-after-merge reconcile 流程（冻结顺序）

1. 前置不变量成立：已有 PublicationRecord、已接受的 ReviewDecision、required checks 全部成功（既有不变量不变）；
2. PR 已合并——采集不可变 SCMMergeReceipt：`headOid`/`baseOid`/`mergeCommitSha` 与 GitHub PR 节点对账，OID 不符 fail closed；
3. 写入 append-only PublicationReconcileRecord：`reconcileType=accept-after-merge`、`observedState=BLOCKED`、`decidedState=ACCEPTED`，`evidenceDigests` 绑定全部前置证据；
4. reconcile 通过 current-ledger recheck 后，Run 从 `BLOCKED` 安全迁移到 `ACCEPTED`。

流程不变量：

- reconcile 不得绕过 required checks 或 ReviewDecision；
- reconcile 不得改写既有 PublicationRecord 或 ReviewDecision；
- merge-never 不变：Marshal 不获得 merge 权限、不自动 merge；
- 任一前置缺失、OID 对账不符或幂等身份冲突，一律 fail closed，Run 保持 `BLOCKED`。

## 后果

- 已合并 PR 的 Run 获得可审计的安全终态迁移路径（`BLOCKED → ACCEPTED`），关闭 `PUBLICATION-MERGED-HEAD-RECONCILE-P1` 的 terminal contract gap（本 ADR 关闭契约层；实现层关闭以对应 Milestone 退出门禁为准）；
- SCMMergeReceipt 使 merge 事实获得不可变权威证据：head branch 删除后仍可凭 GitHub PR 节点恢复权威 OID 并对账；
- PublicationReconcileRecord 提供通用 append-only 对账载体：未来新增 reconcile 类型只扩展枚举，不改写历史；
- 成本：两份 Draft 2020-12 Schema 草案，以及后续实现（采集/reconcile 命令、语义校验与正反 fixture）；Schema 文件落位于 `schemas/*.json` 既有自动 embed 路径，catalog 注册与语义校验属实现 Milestone；
- 不放宽任何既有不变量：Worker 不自证、Worker/Publisher 分权、Draft-only、merge never、fail-closed 保持有效。

## 非目标

- 不改变 merge-never 策略：Marshal 不获得 merge 权限、不自动 merge；
- 不回填或修复 Issue #25 之前的 legacy 已合并 Run：已误入 `BLOCKED` 的 Run 只做只读检查与证据保留，等待本 ADR 实现后的 typed reconciliation 才迁移；
- 不改变 Worker/Publisher 分权、不改变发布凭据隔离、不改变 Draft-only；
- 不实现 code：本 ADR 只冻结契约，实现属后续 Milestone 退出门禁。
