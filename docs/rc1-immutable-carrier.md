# RC1 不可变 Carrier 与 Canary Receipt

本合同只服务精确的 `v1.0.0-rc1`，实现 ADR 0068 的 build-once/same-bytes 证据绑定。它不会执行候选二进制，不会创建 tag、Release、merge 或 deploy，也不会把 GitHub Artifact、runner 或 receipt 提升为新的 publication authority。

## 对象与边界

待校验目录必须是无符号链接的绝对路径，并且只包含以下四个普通文件：

- `marshal_1.0.0-rc1_darwin_arm64`：唯一候选二进制，模式 `0755`；
- `RELEASE-MANIFEST`：精确八行的单资产 manifest，模式 `0644`；
- `SHA256SUMS`：只包含该二进制的一行摘要，模式 `0644`；
- `RC1-CANARY-RECEIPT.json`：closed `RC1CanaryReceiptV1`，模式 `0644`。

前三个文件是 immutable candidate payload。receipt 是 canary 完成后产生的 **out-of-band binding**，不放进它所指向的 GitHub Artifact，因此不会出现“artifact digest 必须包含自身 receipt”的递归。发布侧可以把下载并验证后的三个 payload 文件与 receipt 装配到同一临时目录，再运行 checker；装配不改变 artifact bytes。

GitHub Artifact 只提供 content-addressed transport。Python checker 只校验 immutable carrier/envelope，不是 lifecycle authority validator。tag/release admission 仍由既有 release policy 决定；本合同的 PASS 只是 admission 的必要输入，不是充分条件。

## Payload 与 receipt 摘要

`payload.sha256` 按固定顺序 `binary → RELEASE-MANIFEST → SHA256SUMS` 计算：

```text
marshal.rc1-carrier-payload.v1\n
<name> <byte-size> <raw-sha256>\n
...
```

`payload.size` 是这三个文件的字节长度之和。`receiptDigest` 是 detached canonical digest：先把 `receiptDigest` 保留为空字符串，再对只包含整数、布尔、字符串、数组/对象的 receipt 使用 UTF-8、按 key 排序且无多余空白的 JSON 编码计算 SHA-256。任何未知字段、重复 JSON member、cross-Run/cross-profile binding、非独立 Verifier/Reviewer、非 `accept` Decision、非 `ACCEPTED` Outcome 或 `publication != none` 都 fail closed。

## 外部 authority 输入

receipt 不得自证。独立 admission 步骤必须先从同一 current durable authority 读取原始 TaskSpec/base/source、ArtifactManifest、WorkerResult 集合、activation lineage、ReviewPacket、Verification、Evidence、ReviewDecision 与 Outcome，完成各自 Schema、freshness、digest 与角色独立性语义校验，然后冻结 expected receipt digest。调用者必须从 receipt 之外提供精确 `sourceHead`、candidate workflow run ID、Artifact ID、Artifact digest、当前 durable authority head和该 expected receipt digest：

```bash
/usr/bin/python3 -I -B scripts/rc1-carrier-check.py \
  /absolute/path/to/assembled-carrier \
  40_LOWER_HEX_SOURCE_HEAD \
  WORKFLOW_RUN_ID \
  ARTIFACT_ID \
  64_LOWER_HEX_ARTIFACT_DIGEST \
  sha256:CURRENT_AUTHORITY_HEAD \
  64_LOWER_HEX_EXPECTED_RECEIPT_DIGEST
```

checker 将这些外部值逐项与 receipt 对账，同时重算 payload、manifest、sums、binary 和 detached receipt 摘要。即使攻击者整体替换 lifecycle bundle 并重算 `receiptDigest`，也会因为独立冻结的 expected receipt digest 未变化而被拒绝。所有祖先目录与四个 member 都从 held descriptor 有界读取并持有到 verdict；结束前再次对账 pathname→inode、`fstat`、目录 closed set 与完整 bytes，extra file、symlink、hardlink、路径穿越、ABA、in-place mutation、mode/size/digest 漂移都拒绝。

`authority.currentHeadDigest` 必须来自 canary 对应 durable Run authority 的 current-ledger observation，而不能由 receipt producer 随意选择。receipt 逐项封闭绑定 `specDigest`、`baseSha`、`artifactManifestDigest`、唯一 current Attempt 的 `workerResultDigests`、activation/local-self-identity lineage，以及真实 Pi Run/Attempt 后续的 ReviewPacket、Verification、Evidence、独立 ReviewDecision 和 `ACCEPTED` Outcome。checker 只验证 envelope 内部 cross-binding 与外部冻结值，不证明这些源记录真实存在；真正的 lifecycle semantic admission 必须在冻结 expected receipt digest 之前由同一 current authority 完成。后续 workflow 不得只凭 checker PASS 创建 tag 或 Release。

## 明确不提供的保证

- 不执行或探测候选 Mach-O；真实 fixed CLI canary 由 ADR 0068 的独立步骤完成；
- 不产生 `MarshalArtifactBuildAttestationV1`、`MarshalInstallReceiptV1`、签名、notarization 或 stable authority；
- 不允许 Linux、amd64、server、managed-development、hardened 或其它 profile 混入 RC1；
- 不允许以 receipt/artifact/runner 成功推导 `v1.0.0` stable、production ready 或跨主机可用。

Schema 位于 [`schemas/release/rc1-canary-receipt.schema.json`](../schemas/release/rc1-canary-receipt.schema.json)，确定性 hostile matrix 位于 [`scripts/rc1-carrier-check_test.py`](../scripts/rc1-carrier-check_test.py)。
