# 交付物与发布

## Outcome Bundle

每个终态 Run 都必须产生 Outcome Bundle，包括失败 Run。最低内容：

```text
state.json
events.jsonl
task-spec.json
policy-snapshot.json
capability-snapshot.json
attempts/*/worker-result.json
observed.patch
verification-report.json
review-packet.json
review-decision.json
artifact-manifest.json
outcome.md
```

Outcome Bundle 默认保存在当前业务仓库的 `.marshal/runs/<run-id>/`，属于本地、默认忽略的运行态，不能作为 Source Artifact 提交到业务 Git。需要长期保存时，必须通过显式 Export 或 CI Artifact/Object Store 上传；导出动作记录目标 URI、摘要与保留策略。

不适用文件通过 Manifest Status 显式表示，不能静默消失。例如在 Worker 启动前 Blocked 的 Run 没有 Patch，但 Outcome 必须解释缺失阶段。

## Artifact 类型

### Source Artifact

为满足任务而新增或修改的仓库文件，包括 Code、Test、Documentation、Migration、Generated Source 与 Configuration。

### Evidence Artifact

Patch、Command Log、Structured Report、Screenshot、Rendered Document、Coverage、Benchmark 等验证或 Review 材料。

### Publication Artifact

Commit SHA、Branch Ref、PR/MR ID 与 URL、Remote Check Record，以及可选 Release/Package ID。

### Diagnostic Artifact

Failure Report、Crash 信息、Provider Error、Blocker 与 No-change Analysis。

## ArtifactManifest

每条记录包含：

- 稳定 Artifact ID；
- Kind 与 Media Type；
- Producer：`worker`、`verifier`、`reviewer`、`publisher` 或 `system`；
- Required/Optional；
- 本地相对路径或 External URI；
- 本地文件 Byte Size 与 SHA-256；
- 创建时间、Stage 与 Attempt；
- Redaction、Truncation 与 Validation Status；
- 关联 Gate ID。

Path 必须明确相对 Run Bundle 还是 Repository Root。Portable Report 默认不包含绝对宿主机路径。

## 成功 Coding Task 的交付物

除非 TaskSpec 缩小范围，Published Coding Change 应包含：

- Source Diff；
- 相关 Test；
- Required Documentation 或 Migration Note；
- 通过的 VerificationReport；
- Accepted ReviewDecision；
- Commit 与 Branch Identity；
- Draft 或 Ready PR/MR；
- 简洁 Risk 与 Rollback Note；
- 要求时的 Remote CI Result。

失败或阻塞 Run 产生 Diagnostic Artifact，而不是人为制造 PR。

## 发布前置条件

Publisher 必须重新检查：

1. Worktree Lease Ownership；
2. 当前 snapshot/diff digest 与已接受证据一致；
3. VerificationReport 当前且通过；
4. ReviewDecision 为 `accept` 且引用当前 ReviewPacket/evidenceDigest；
5. Required Non-publication Artifact 已验证；
6. Remote URL 匹配 Repository Policy；
7. Publisher Credential Profile 可用；
8. Target Branch 与 Base Policy 仍有效。

Base Branch 前进后，Policy 只能明确选择：按锁定 Base 发布、Block 等待 Rebase，或创建新 Run。Marshal 不得静默 Rebase 已 Review 代码，因为这会改变证据。

## Commit 创建

Publisher 在 Accept 后创建首次交付 Commit，避免 Worker Commit 成为发布权限来源。

Commit 记录：

- Task/Run ID Trailer；
- specDigest 与 evidenceDigest Trailer；
- 确定性的 Author/Committer Policy；
- Accepted Snapshot Identity；
- 基于 Task Title 且符合仓库规则的 Subject。

默认禁用用户 Git Hook，因为任意 Hook 可能修改已接受快照或产生未记录副作用。仓库可以要求显式 Controlled Hook Profile，其结果必须成为验证证据。Commit Signing 是可选 Publisher 能力，Signing Material 不能暴露给 Worker。

创建 Commit 后，Marshal 必须确认 Commit Tree 精确表达已接受快照中的 File、Mode 与 Content；不一致则阻止发布。

## Branch Identity

默认逻辑 Branch：

```text
marshal/<task-id>-<slug>
```

Slug 仅用于可读性，taskId 才是权威身份。若 Remote Branch 已存在，只有 Publication Metadata 证明它属于同一任务时才能更新，否则 Block。默认禁止 Force Push。

## PR/MR 幂等性

首次发布保存 Provider、Repository ID、PR/MR ID、Head Branch、Base Branch 与 URL。后续发布使用不可变 Provider ID。

本地状态缺失时，Publisher 可以搜索 PR/MR Body 中的机器标记：

```text
<!-- marshal task=ENG-123 run=<run-id> -->
```

唯一匹配在 Remote/Head 验证后可被接管；无匹配则新建；多个匹配则 Block 等待处理。

## PR/MR Body

```markdown
## 目标

## 变更

## 验证

## 必需交付物

## Review 决策

## 风险与回滚

## 来源信息
```

Provenance 包含 Task/Run ID、锁定 Base、Head SHA、Worker Adapter/Version、specDigest 与 evidenceDigest。不得包含 Prompt、Reasoning Trace、Secret、绝对本地路径或大段日志。

Marshal 更新 PR/MR Body 时，必须保留被明确分隔的用户自定义内容。

## CI Tracking

Required Check 来自 Policy 或 TaskSpec。只有 Check 的 Repository Identity 与 Published Head SHA 均匹配时才能接受。Polling 使用有界 Backoff 并可恢复；未来可增加 Webhook，而不改变 Core Check Record。

## Merge Policy

`mergePolicy: never` 是默认值，也是 MVP 唯一强制实现。Accept 表示“可以交付”，不表示“允许合并”。

未来 Automatic Merge Profile 必须独立要求：

- 当前 Accepted Decision；
- 全部 Required CI 与 Repository Approval；
- Protected Branch Policy 兼容；
- 没有更新的 Head Commit；
- 显式 Repository Authorization；
- 可审计 Merge Method 与 Actor。

## Retention 与 Cleanup

- Worktree 清理后仍保留 Outcome Bundle。
- `.marshal/` 的本地保留不等于长期审计归档；重要 Bundle 必须显式导出到受控存储。
- 可能含 Secret 的 Raw Transcript 使用更短的可配置 Retention。
- External URI 不假设永久可用，重要远程证据同时保存 ID 与有界摘要。
- 删除重要 Outcome Bundle 是显式管理操作，绝不能成为普通 Task Cleanup 的副作用。
