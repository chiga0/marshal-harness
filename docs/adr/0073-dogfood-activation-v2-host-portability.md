# ADR 0073：darwin-local-dogfood activation v2 的机器可迁移性

| 字段 | 值 |
| --- | --- |
| 状态 | Proposed |
| 日期 | 2026-09-01 |
| 决定者 | 维护者（pending） |
| 惯例约束 | 接受只冻结合同；接受前不得开始实现；不改变 R1–R6 状态 |
| 关联 ADR | [ADR 0051](0051-darwin-local-dogfood-boundary.md)、[ADR 0067](0067-darwin-ordinary-user-launch-and-attach-recovery.md)、[ADR 0068](0068-mac-first-cli-only-lifecycle-preview-rc1.md) |

## 背景

`darwin-local-dogfood` 首发路径（`S1′ → S2′ → Attach/rebind → terminalization → fixed CLI 真实 Pi → 独立 Decision ACCEPTED → same-bytes RC1`）此前全部在单台开发机上完成。`marshal.local-dogfood-activation.v1` 同时把运行 activation 的二进制文件的 `expectedDevice` 与 `expectedInode` 纳入身份主体绑定。

这个设计在单机上成立：workflow 的 `run` 与 `finalize` 两个 job 落在不同 runner VM 上时，即使二进制 bytes 完全一致，文件系统在 VM 间也是不同的 device/inode 集合。GH 流水线实证：`scripts/release-canary.sh` 的 finalize 在「本地 Run 身份绑定无效」处被拒。GH-hosted canary 因此成为单机演出，RC1 分发渠道不可用。

## 决定

将 `marshal.local-dogfood-activation.v1` 升级为 `marshal.local-dogfood-activation.v2`：从 identity 中移除 device/inode，改为八个字段闭集。

activation v2 的身份只由下列字段界定：

- `canonicalRepositoryRoot`：EvalSymlinks 已解析的 canonical 仓库根路径；
- `canonicalExecutablePath`：canonical 可执行路径；
- `expectedRawSHA256`：二进制正文 sha256 digest；
- `expectedSize`：二进制大小；
- `expectedSourceHead`：40-hex sourceHead；
- `expectedSelfProfile`：`darwin-local-dogfood`；
- `activationId`：activation 标识符；
- `issuedAt`、`validUntil`：有效性窗口。

device/inode 只保留在新 observation 的 `diagnosticFirstObservation` 备注字段中，不再参与判定。

## 负向 contract（保持 fail-closed）

以下任何一项改变均拒绝：

- `canonicalRepositoryRoot` 错误；
- `canonicalExecutablePath` 错误或缺失；
- `expectedRawSHA256` 不匹配；
- `expectedSize` 不匹配；
- `expectedSourceHead` 不匹配或过期；
- `expectedSelfProfile` 不匹配；
- `activationId` 不匹配或 scope 无效；
- `issuedAt` 之后生效或 `validUntil` 之前失效（超出窗口）。

## 信任边界影响

- 伪造 activation 仍然无法在新机器上成立：canonical path、bytes digest、size、sourceHead、selfProfile 必须全部对上；
- 两个不同 runner VM（如 GH Actions 的不同 runner）只要对同一 activationId 交付完全一致的 bytes、path、sourceHead，仍可在同一有效性窗口内完成 cross-host 的 finalize；
- 已产生的 v1 证据保留为只参考档案；新 runs 一律按 v2 schema 发出，不接受以 v1 digest 作为依据。

## Scope

- 本合约只修 activation/observation 的机器维 binding 语义；
- zero-selector、ResultIngress current-ledger recheck、sealed chain authorship、Issue #212 签名公证与 Linux stable gate 不改动；
- v1 证据不支持任何新的 canary/finalize 调用。

*仅 proposal；接受前不得开始实现。*
