# Marshal 契约 Schema

这些 JSON Schema 使用 Draft 2020-12，描述当前设计提出的持久化 `v1alpha1` 记录。

- [`task-spec.schema.json`](task-spec.schema.json)
- [`capability-snapshot.schema.json`](capability-snapshot.schema.json)
- [`policy-snapshot.schema.json`](policy-snapshot.schema.json)
- [`run-event.schema.json`](run-event.schema.json)
- [`run-state.schema.json`](run-state.schema.json)
- [`worker-request.schema.json`](worker-request.schema.json)
- [`worker-result.schema.json`](worker-result.schema.json)
- [`verification-report.schema.json`](verification-report.schema.json)
- [`artifact-manifest.schema.json`](artifact-manifest.schema.json)
- [`review-packet.schema.json`](review-packet.schema.json)
- [`review-decision.schema.json`](review-decision.schema.json)
- [`publication-intent.schema.json`](publication-intent.schema.json)
- [`publication-record.schema.json`](publication-record.schema.json)
- [`remote-check-record.schema.json`](remote-check-record.schema.json)
- [`scm-merge-receipt.schema.json`](scm-merge-receipt.schema.json)
- [`publication-reconcile-record.schema.json`](publication-reconcile-record.schema.json)
- [`scm-merge-intent.schema.json`](scm-merge-intent.schema.json)
- [`approval-record.schema.json`](approval-record.schema.json)
- [`intervention-record.schema.json`](intervention-record.schema.json)
- [`sandbox-requirements.schema.json`](sandbox-requirements.schema.json)
- [`release/rc1-canary-receipt.schema.json`](release/rc1-canary-receipt.schema.json)：精确 `v1.0.0-rc1` 的 out-of-band same-bytes canary binding；它不是 publication authority。

Schema Validation 是必要条件，但还不充分。Implementation 必须增加以下 Semantic Validation：

- Canonical Repository Path 与 Glob Behavior；
- Task Command、Deliverable、Artifact 与 Finding ID 唯一性；
- Budget Relationship；
- Adapter Capability Compatibility；
- TaskSpec、Evidence 与 Artifact Digest；
- ReviewDecision Freshness；
- `accept` 要求强制 Gate 通过且没有 Blocking Finding；
- `no_change` 要求 Empty Diff 与 `allowNoChange=true`；
- Publication Identity 与 Remote Validation。

Milestone 0 已实现单记录即可判定的第一批规则，包括 Path、Glob Syntax、ID Uniqueness、Budget Relationship、Verification Overall Status、Review Finding 与 `.marshal/` Source Artifact 禁止规则。ReviewDecision Freshness、跨记录 Digest Binding、Accept/No-change Gate、Capability Compatibility 与 Publication Identity 依赖后续运行态上下文，分别在 Milestone 1–5 实现；在此之前不得宣称这些跨记录规则已被 Runtime 强制。

Schema 是已接受但仍处于 `v1alpha1` 的契约。Implementation 必须为每个 Schema 与 Semantic Rule 提供正反 Fixture。

[`examples/happy-path/`](examples/happy-path/) 中的文件构成一条跨 Record 示例链路，使用合成 Identity 与 Digest，不代表 Runtime Output 或兼容性承诺。

[`examples/invalid/`](examples/invalid/) 为每份 Schema 提供最小反例。Go Contract Package 会把 Schema 与 Fixture 嵌入单一二进制，使用 Draft 2020-12、ECMAScript Regex 与 Format Assertion 编译，并在 Schema 通过后继续执行 Semantic Validator。全部 `relativePath` Schema 同时拒绝反斜杠，避免其他语言消费者绕过 Go Semantic Layer。

Publication 记录（PublicationIntent、PublicationRecord、RemoteCheckRecord）只包含发布世代、Provider/Repository/PR 身份、Branch、SHA、Digest 与 Marker；不得包含 Token、GH Config Dir 或绝对本地 Worktree Path。

ADR 0026 权威 reconcile 记录：SCMMergeReceipt 是已合并 PR 的不可变 merge 事实证据（headOid/baseOid/mergeCommitSha 与 GitHub PR 节点对账，`receiptDigest` 为去除自身后的 canonical digest）；PublicationReconcileRecord 是 append-only 对账载体，幂等身份 canonical 绑定 `(authorityNamespaceId, runId, scmMergeReceiptId, reconcileType)`，同身份重复提交归并、关键内容不同冲突 fail closed。两者均为 authority ledger 事实，只允许 Core 写入；`BLOCKED → ACCEPTED` 迁移是由这两个记录与 current-ledger recheck 共同门禁的 typed reconciliation，不放宽任何其他终态语义。

ADR 0032 B1 权威 merge 意图记录：SCMMergeIntent 是受控 Task Merge 的唯一事前授权载体，先于一切远端副作用持久化且永不在原地改写；它逐字段绑定当前发布世代、ReviewDecision/Verification/Evidence/Policy/Approval/RemoteCheck 的 canonical digest 与预期 merge 执行者身份（`expectedMergedBy` canonical 表示 `github-login:<login>`）。`intentDigest` 是 detached canonical digest：计算时把 `intentDigest` 自身置为空字符串（字段保留）后经 RFC 8785 JCS 重算，权威校验永远自行重算、不信任记录内自报摘要；`publicationRecordId` 必须逐字等于 `publicationDigest`。TaskSpec 的 `publication.mergeMethod` 为 `merge|squash|rebase` 封闭枚举，仅当 `mergePolicy=policy` 时强制存在，且此时 `requiredChecks` 必须非空去重；`never` 保持兼容，`manual` 仍 fail closed。

Control 记录（ApprovalRecord、InterventionRecord）是 Run Lease 保护下的追加式授权与介入证据。Approval 必须绑定当时的冻结输入或发布证据；Intervention 的分类决定是否可在当前 Attempt 继续、必须重新验证或必须创建新 Run。Worker 无权创建这两类记录。

SandboxRequirements 按 ADR 0017 冻结 Run 的二维沙箱要求：`accessMode`（权限维度，封闭枚举 `read-only`/`workspace-write`）与 `minimumAssuranceLevel`（隔离维度，封闭枚举 `workspace-write`/`hardened`），四种正交组合全部合法。旧 `executionProfile` 只存在确定性单向映射：`read-only` → `read-only` × `workspace-write`、`workspace-write` → `workspace-write` × `workspace-write`、`hardened` → `workspace-write` × `hardened`；不存在反向映射，`read-only` × `hardened` 没有旧 `executionProfile` 表达。本契约切片只冻结 Schema 与 catalog 注册，不改变 TaskSpec、WorkerRequest、PolicySnapshot、CapabilitySnapshot、Outcome 或 Local CLI/Adapter 行为，也不回写历史持久记录。
