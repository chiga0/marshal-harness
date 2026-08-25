# ADR 0048：受保护构建输入与 Marshal Artifact Attestation

- 状态：已接受（Accepted，2026-08-26）；接受证据绑定 sourceHead `5de09997f5260c672f297496290b567815162bb1` 的同一独立 reviewer 复审结论 `ACCEPT`（一次聚合返工后 P0/P1=0）与已合入 PR #215；接受只冻结合同，不构成实现、签名授权、外部 provision、Issue #212 关闭或 R3-D/E/F 完成证据
- 修订状态：提议（Amendment Proposed，2026-08-26）；下述 `CompileRootManifestV1`、authenticated `MarshalArtifactBuildRecordV1`、共享 `CodeSignatureIdentityV1` 与跨对象相等关系尚未被原 sourceHead/PR #215 审查或接受，须经本轮独立复审和维护者单独接受后才冻结
- 关联：[ADR 0003](0003-separate-worker-and-publisher.md)、[ADR 0004](0004-independent-verification.md)、[ADR 0038](0038-agent-production-authority-provider.md)、[ADR 0047](0047-marshal-darwin-self-identity-and-release-signing.md)、[Issue #212](https://github.com/chiga0/marshal-harness/issues/212)

## 背景

ADR 0047 已冻结 Marshal Darwin executable 的 profile、安装收据、current/high-water、CLI 自身门禁和 artifact/release 与 deployment/install 分权，但没有冻结 artifact signer 如何证明 `sourceHead` 对应实际编译输入。

Issue #212 的第四个实现候选暴露了该缺口：构建脚本在 Make 解析阶段读取 `HEAD/status/HEAD`，随后才执行 `go build`。这不能排除被 Git 忽略但会参与编译的 `.go` 或 `go:embed` 输入，不能关闭观察后源码修改，也不能关闭 linked worktree 的 `.git`、`gitdir`、`commondir` swap-back ABA。仅在 build 后再次运行 `git status` 仍看不到 ignored 输入。若把这样的 `sourceHead` 写进 binary，下游只能证明字段格式正确，不能证明该 commit 产生了该 artifact。

本 ADR 只补充 **Marshal 自身 artifact 的构建输入 authority**。它不改变 R3–R6 的 Agent/Sandbox authority、ResultIngress、恢复或 cutover 合同；也不替代 ADR 0047 要求的外部 certificate、企业 allowlist、current/high-water 与部署 policy observation。

## 决策

### 1. Profile 与 authority 边界

1. `darwin-adhoc-build` 可携带非权威的诊断 provenance claim，但该 claim 只能用于本地定位；不得成为 `MarshalArtifactBuildAttestationV1`、安装收据、lifecycle authorization、release 或 publication 的输入。
2. `darwin-managed-development` 与 `darwin-notarized-release` 的可安装 artifact 必须来自受保护 builder；普通可变 worktree、同 UID 临时目录或 Agent worktree 不能成为 artifact authority。
3. 受保护 builder 必须从 canonical repository 的不可变、内容寻址 source bundle/materialization 构建。调用者提供的 `sourceHead`、clean 状态或 manifest 都只是不可信请求，不能由 signer 原样背书。
4. 接受本 ADR 不会让当前 ad-hoc binary 获得执行权限，也不会授权任何已有 CI secret、签名 identity 或发布流程。

### 2. Sealed build input

构建输入固定为两个连续的 sealed stage：

1. **source stage**：受保护 producer 从 canonical repository 解析精确 repository identity 与 commit，导出内容寻址 source bundle；解析 submodule、Git LFS 和生成步骤 policy 后，产出 canonical source manifest；
2. **compile stage**：若有 code generation，generator 只能读取 sealed source stage，并把输出写入独立目录；其输出经重新 manifest 和 sealing 后成为 compile root。没有生成步骤时，source stage 直接成为 compile root。

compile root 在 manifest 观察后必须只读且不可替换。compiler 只能从该 sealed root 读取项目源码和 embed 内容；module/cache/toolchain 与输出位于显式分离的受控位置。若平台不能机械阻止 root mutation、symlink 越界或 administrative graph 替换，该构建不能产生权威 attestation。

本合同不要求对 Go compiler 做通用 syscall trace，也不声称枚举其内部所有实现依赖。输入完整性的 authority 来自：compiler 被约束只能读取 sealed manifest root 与下节 sealed external material roots，加上显式绑定的 toolchain、构建 invocation 和 machine-observed content manifest。任何 ambient workspace、`GOWORK`、replace target、module cache、CGO/tool 或环境输入，只有先物化为内容寻址材料、进入 closed content manifest 并 sealing 后才可使用；policy allowlist 只决定某类材料能否出现，不能替代实际材料摘要。无法物化并 sealing 的输入必须 fail closed。

### 3. Canonical source manifest

canonical `SourceManifestV1` 至少绑定：

- repository identity、`sourceHead`、Git object format、source bundle digest；
- 每个 source-root entry 的规范相对路径、类型、mode、长度和 SHA-256；
- symlink target 及其边界判定；禁止解析后逃出 sealed root；
- submodule path、pinned commit、materialized tree digest，或显式 `none`；
- Git LFS pointer digest、materialized object digest，或显式 `none`；
- generated-source stage identity、generator executable/toolchain content digest、generator invocation/input/output manifest digest，或显式 `none`；
- `go.mod`、`go.sum`、vendor/workspace policy、resolved module graph digest；
- build invocation、环境 allowlist、toolchain distribution digest、Go version、target OS/arch/profile；
- manifest schema version、canonical digest 与 producer observation identity。

`SourceManifestV1` 必须显式携带 `entries` 与 `rootDigest=sha256(JCS(entries))`，并采用 versioned closed schema、canonical encoding 和 domain-separated `manifestDigest`。未知或重复成员、尾随 bytes、绝对路径、重复路径、大小写/Unicode 归一化冲突、越界 symlink、无法物化的 submodule/LFS、未声明 generator 或 ambient module/workspace 输入全部拒绝。

生成步骤完成后的 compile root 必须由独立的 `CompileRootManifestV1` 绑定，不能只传一个调用者自报摘要。该对象是 exact closed object，最小字段固定为：

- `schemaVersion`、`manifestId`、`repository`、`sourceHead`、`sourceManifestDigest`、`generatedSourceStageDigest|null`；
- `entries`：每项统一包含 `path/entryType/mode/length/sha256/symlinkTarget/symlinkBoundary`。`regular` 要求非负 length 与 lowercase SHA-256，两个 symlink 字段均为 null；`directory` 固定 length=0、sha256=null 且两个 symlink 字段为 null；`symlink` 的 length 与 sha256 分别绑定 canonical UTF-8 target bytes 的长度和摘要，`symlinkTarget` 必须为规范相对 target，`symlinkBoundary=within-sealed-root`；其它组合拒绝；
- `rootDigest`、`producerObservationIdentity`、`manifestDigest`。

`rootDigest=sha256(JCS(entries))`，`manifestDigest=sha256(JCS(manifestWithoutManifestDigest))`。entries 必须按规范路径稳定排序；路径、Unicode/case-fold、重复成员、尾随 bytes 与 symlink 边界规则和 `SourceManifestV1` 相同。没有 generator 时仍须产生该对象，`generatedSourceStageDigest=null`，且 compile-root `entries/rootDigest` 必须与 `SourceManifestV1.entries/rootDigest` 逐项相等；只声明 null 而内容不同固定拒绝。有 generator 时，`generatedSourceStageDigest` 必须解析到 exact closed `GeneratedSourceStageV1`，该对象绑定 `sourceManifestDigest`、generator material/invocation/input digest、规范输出 `entries/rootDigest` 与自身 digest；compile-root `entries/rootDigest` 必须与该输出逐项相等。artifact signer 必须重新计算 manifest/root digest 并观察对应 sealed root；只有 digest、没有可复核对象或 sealed root 时固定拒绝。

#### External build material manifest

所有允许从 compile root 外读取的 bytes 必须先物化到 sealed external material roots，并由一个或多个 exact closed `ExternalBuildMaterialManifestV1` 绑定。最小字段为：

- `schemaVersion`、`materialSetId`、`materialKind`、`producerObservationIdentity`、`policyDigest`；
- `entries`：每项的规范逻辑 identity、sealed root 内相对路径、类型、mode、长度、SHA-256 与来源 identity；
- `manifestDigest`。

`materialKind` 为封闭枚举，至少覆盖 `go-module-source`、`workspace-module`、`local-replace`、`vendor-tree`、`dependency-embed`、`generator-tool`、`cgo-header`、`cgo-library`、`pkg-config-data`、`external-build-tool` 和 `go-toolchain`。同一 bytes 可由不同逻辑 identity 引用，但 manifest 必须显式记录引用关系；同一逻辑 identity 指向不同 bytes 固定拒绝。

`manifestDigest=sha256(JCS(manifestWithoutManifestDigest))`。manifest 自身不授予 authority；artifact/release signer 必须重新计算该 digest、观察对应 sealed objects，并由最终 artifact attestation 精确接纳。

module source 不能只记录 module path/version 或 resolved graph。producer 必须物化实际 dependency source tree（包括 dependency 的 embed、assembly、C/C++/header 与 platform-specific source），重新计算 content manifest，再让 module graph entry 精确引用对应 material digest。local replace、workspace module 与 vendor 三种决策必须互斥并显式记录；ambient module cache 不能作为 compile input。

generator executable、CGO header/library、`pkg-config` data/tool 和其它外部 build tool 都按实际 bytes 进入 material manifest。动态或系统库若不能锁定精确对象与内容，当前 profile 不得产生权威 attestation。policy allowlist 与 observed material manifest 使用不同字段和 digest；任一方缺失、错配或 observation 后替换均拒绝。

### 4. `MarshalArtifactBuildRecordV1`

builder 到 artifact/release signer 之间只能传 immutable、content-addressed 的 `MarshalArtifactBuildRecordV1`，不得传自由 JSON、可变 pathname 或调用者自报摘要。该 record 是 exact closed object，最小字段固定为：

- `schemaVersion`、`recordId`、`createdAt`、`buildProfile`、`repository`、`sourceHead`；
- `sourceBundleDigest`、`sourceManifestDigest`、`compileRootManifestDigest`、`externalMaterialManifestDigests`；
- `buildInvocationDigest`、`environmentPolicyDigest`、`toolchainMaterialDigest`、`moduleGraphDigest`；
- `builderPrincipalId`、`builderWorkflowIdentity`、`builderIsolationProfile`；
- closed `unsignedArtifact`：`rawSHA256`、`fileSize`、`goBuildId`、`os`、`arch`、`version`、`buildDate`；
- `recordDigest`。

`recordDigest=sha256(JCS(recordWithoutRecordDigestAndSignedObjectEnvelope))`。`externalMaterialManifestDigests` 必须非空、去重并按 lowercase digest 排序，且至少包含 `toolchainMaterialDigest` 对应的 `materialKind=go-toolchain` manifest；所有 module、generator、CGO/tool 或其它 compile-root 外 bytes 均须由同一集合精确接纳。authoritative profile 不允许空数组、null 或省略字段。

record 必须携带 exact closed `signedObjectEnvelope`，精确复用 ADR 0038 `SignedObjectEnvelopeV1`，并固定 `signatureDomain=marshal-artifact-build-record-v1\0`、key usage=`marshal-artifact-build-record`；envelope `objectDigest` 必须等于 `recordDigest`。verifier 必须从外部 current、未撤销 builder principal keyset 解析 `builderPrincipalId/keyId/keyEpoch/usage/domain/algorithm/encoding/createdAt` 并验签，不能从候选 record 选择 producer、trust root 或 current epoch。该签名把 exact `recordDigest`、builder principal/workflow/isolation 与 unsigned artifact digest 绑定为受保护 workflow fact，但不授予 artifact/release verdict；artifact/release signer 仍须从按 digest 解析的 immutable record 重新验证 source/compile/external manifests、builder workflow receipt 与 unsigned artifact bytes。builder 可持有仅限 build-record usage 的专用 key，但不能持有 Apple code-signing、artifact-attestation 或 deployment/install key。

### 5. `MarshalArtifactBuildAttestationV1`

`MarshalArtifactBuildAttestationV1` 是 immutable、content-addressed、signed object，最小字段为：

- `schemaVersion`、`attestationId`、`issuedAt`、`buildProfile`；
- `repository`、`sourceHead`、`sourceBundleDigest`、`sourceManifestDigest`、`compileRootManifestDigest`、`buildRecordDigest`；
- `submodulePolicyDigest`、`lfsPolicyDigest`、`generatedSourceStageDigest|null`；
- `buildInvocationDigest`、`environmentPolicyDigest`、`externalMaterialManifestDigests`、`toolchainMaterialDigest`、`moduleGraphDigest`；
- `builderPrincipalId`、`builderWorkflowIdentity`、`builderIsolationProfile`；
- `artifactAttestationProducerPrincipalId`、`codeSigningWorkflowIdentity`、`artifactAttestationWorkflowIdentity`；
- unsigned build output 的 raw SHA-256 与 file size，以及完成 code signing 后 final artifact 的 raw SHA-256、file size、Go build ID、OS、arch、version、build date；
- closed `codeSignatureIdentity`（共享 `CodeSignatureIdentityV1`）：`signatureKind`、`identifier`、`teamIdentifier|null`、`cdHash`、`designatedRequirement`、`leafCertificateSHA256|null`、`certificateChainSHA256|null`、`hardenedRuntime`、`secureTimestamp`；
- closed `codeSignatureObservation`：`observedFinalRawSHA256`、`observedFileSize`、`observedAt`、`observerWorkflowIdentity`；
- `attestationDigest` 与 `signedObjectEnvelope`。

`attestationDigest=sha256(JCS(attestationWithoutAttestationDigestAndSignedObjectEnvelope))`，并且必须等于 `signedObjectEnvelope.objectDigest`。

`signedObjectEnvelope` 精确复用 ADR 0038 `SignedObjectEnvelopeV1`，是 exact closed object，字段只有 `objectDigest/signatureAlgorithm/signatureEncoding/keyId/keyEpoch/signatureDomain/signature`。本对象固定 `signatureAlgorithm=Ed25519`、`signatureEncoding=base64url-unpadded`、`signatureDomain=marshal-artifact-build-attestation-v1\0`、key usage=`marshal-artifact-build-attestation`；signature 解码后精确 64 bytes，签名消息为该 UTF-8 domain 后连接 lowercase ASCII `objectDigest`。

parent object 的 `builderPrincipalId` 只记录 builder，不是签名 producer。`artifactAttestationProducerPrincipalId` 必须等于 release policy 按 `(buildProfile, repository, artifact/release authority)` 唯一解析的 producer；verifier 必须从该 principal 的外部 current、未撤销 keyset 验证 `keyId/keyEpoch/usage/domain/algorithm/encoding/issuedAt/signature`。待验证对象不能自带或选择 trust root/current keyset。错 producer、错 usage/domain、旧或未来 epoch、issuedAt 不在 key validity、revoke 后签名、未知 key/算法/编码、parent digest 不等或同 key 跨 artifact/install role 全部拒绝。

机器观察事实与 policy verdict 必须分离：manifest/digest/builder/output 字段记录可复核事实；是否允许进入 managed-development、release、signing 或 install 由调用方 policy 根据 profile 与 current ledger 决定，不能把 `allowed=true` 写成 artifact 自证字段。

attestation 中与 resolved build record 重复的 repository/sourceHead/source bundle/source/compile/external manifest、invocation/environment/toolchain/module graph、builder principal/workflow/isolation 与 unsigned artifact 字段必须逐项相等；parent final artifact digest/size 必须与 `codeSignatureObservation` 逐项相等。任一合法 record 配错 parent sourceHead、builder identity、unsigned artifact 或 manifest digest 均拒绝。

`codeSignatureObservation` 必须由 `artifactAttestationWorkflowIdentity` 对同一 immutable final object 产生；`codeSignatureIdentity.identifier` 固定为 ADR 0047 的 `com.github.chiga0.marshal`，其它身份字段按 profile policy 验证。ADR 0047 `MarshalInstallReceiptV1.codeSignature` 的九个身份字段精确定义同一个 closed `CodeSignatureIdentityV1`，只能与 attestation 的 `codeSignatureIdentity` 对该共享对象逐字段相等；installer 另以 receipt `artifact.rawSHA256/fileSize`、`installation.canonicalPath/device/inode/installedAt` 和 deployment/install signature 绑定自身 held-object 重观察，不与 attestation observation literal 等形比较。`codeSigningWorkflowIdentity` 记录 Apple code-sign operation，必须等于 ADR 0047 immutable release record（以及 receipt `publisher.signingWorkflowRef`）按 digest 接纳的 signing workflow；`artifactAttestationWorkflowIdentity` 只记录 post-sign observation/attestation operation，二者不得互相冒充。缺 observation、receipt 后补 identity、错 CDHash/Team ID/requirement/certificate chain、签名后 bytes swap 或 workflow 不匹配均拒绝。该 attestation 的职责止于 code-signing 后 artifact identity；notarization 与目标 deployment policy observation 继续由 ADR 0047 的 immutable release record 和 `MarshalInstallReceiptV1` 独立接纳，不能静默塞入本对象或由本对象冒充。

### 6. Producer 与 signer 分权

1. source/materialization producer 解析 canonical repository 并产出 sealed bundle；普通调用者不能自报 bundle digest。
2. builder 在受保护环境中消费 sealed bundle，并产出 unsigned artifact 与 build record；builder 不能持有 code-signing key，也不能签最终 artifact attestation。
3. artifact/release authority 必须按 `buildRecordDigest` 解析并验证 authenticated immutable `MarshalArtifactBuildRecordV1`，独立验证 source/compile/external-material manifest、外部 current builder workflow fact、record digest 与 unsigned artifact bytes，再对精确对象执行 Apple code signing；随后重新观察签名后 final artifact bytes 与 closed `CodeSignatureIdentityV1`，最后使用专属 attestation key usage 签 `MarshalArtifactBuildAttestationV1`。它不得只消费调用者提供的 `sourceHead`、record 字段或摘要。Apple code-signing operation 与 artifact-attestation operation 可以由同一受保护 artifact/release authority 控制，但必须使用不同 operation identity/key usage 并分别记录 `codeSigningWorkflowIdentity/artifactAttestationWorkflowIdentity`；二者都不能与 builder 或 deployment/install signer 合并。
4. deployment/install signer 只能消费已签 artifact attestation，独立观察目标安装对象、policy/current transaction 后签 `MarshalInstallReceiptV1`；不得修改、补签或重新解释 artifact attestation。
5. artifact/release signer 与 deployment/install signer 沿用 ADR 0047 的不同 principal、identity 和 private key 要求。任何角色合并、调用者自签或同一 key 复用均 fail closed。

### 7. 签名、安装与 replay

- code signing 只能作用于精确匹配 builder record 中 unsigned digest 的 immutable artifact；签名会改变 Mach-O bytes，因此 attestation 必须同时绑定 unsigned digest 与签名后 final digest，且只允许安装 final digest 对应对象。签名前后都须重新观察 raw bytes 与 identity，出现替换即拒绝。
- `MarshalInstallReceiptV1.artifactAttestation` 必须精确绑定本合同的 attestation digest、producer principal、key identity 与 current key epoch；receipt 的 `codeSignature`（即 shared `CodeSignatureIdentityV1`）必须与 attestation 的 `codeSignatureIdentity` 逐字段一致，并由 installer 对目标 held object 重新观察自己的 digest/size/time/observer metadata，不能只复制 attestation observation。
- artifact attestation verifier 每次消费时都必须重新读取 producer principal 的 current keyset/revocation fact；Apple code-signing identity 不能代替 artifact-attestation key usage，artifact attestation 也不能代替 ADR 0047 的 code-signature 与 deployment policy observation。
- 相同 source manifest 不保证相同 binary；每个 artifact 必须有独立 output digest 与 attestation。相同 tag/version 不能覆盖既有 attestation。
- 旧 attestation、旧 key epoch、已撤销 builder/signer、降级 sourceHead/profile/toolchain 或与 current release policy 不匹配的 replay 均拒绝。
- build、artifact signing、code signing/notarization、install/current 推进和 publication 之间不得以可变 pathname 或自由文本传递 authority；只传 immutable object identity、digest 与独立 observation。

### 8. 必须通过的 hostile matrix

实现进入 managed-development 或 release 前，至少验证：

1. `.gitignore`、`.git/info/exclude` 或 global excludes 中的 `.go` 文件不能无记录参与编译；
2. ignored 或生成的 `go:embed` 输入不能绕过 manifest；
3. manifest/provenance 观察后修改、增加、删除或替换源码，构建或接纳必须失败；
4. linked worktree `.git` file、`gitdir`、`commondir`、refs/object store swap-back ABA 不能改变已 sealed 输入；
5. submodule pinned commit 与 materialized tree 不一致必须失败；
6. Git LFS pointer 与 materialized object 不一致或对象缺失必须失败；
7. generator binary、invocation、输入或输出漂移必须失败；
8. `go.mod`、`go.sum`、vendor/workspace、module graph、toolchain、CGO/tool 或环境 allowlist 漂移必须失败；
9. symlink、case-fold/Unicode 冲突、绝对路径或 root 外读取必须失败；
10. build 后 artifact swap、code-sign 前后 swap、attestation 与 raw bytes 不一致必须失败；
11. builder 自签、artifact signer 代签 install receipt、deployment signer 重签 artifact、同 key 跨角色必须失败；
12. revoked key、旧 epoch、旧 policy、旧 attestation、profile/source/toolchain downgrade 与 tag overwrite 必须失败。
13. module graph 相同但 extracted module/dependency embed bytes 不同、同 generator argv 但 executable bytes 不同、同 CGO allowlist 但 header/library/`pkg-config`/tool bytes 不同必须失败；
14. artifact attestation 的错 producer、错 key usage/domain、旧或未来 epoch、revoke 后签名、Apple code-signing key 冒充 attestation key，以及 artifact/install 两角色复用同 key 必须失败。
15. 缺失或伪造 `MarshalArtifactBuildRecordV1`、record 与 sealed manifest/unsigned artifact 摘要不一致、调用者替换 builder workflow fact 或 signer 只信 record 字段必须失败；
16. 缺失或伪造 `CompileRootManifestV1`、无 generator 时合法 source manifest 配被替换 compile root、生成输出未重新枚举、source/compile manifest 交叉拼接或 compile-root sealed object 不可得必须失败；
17. attestation 缺少 shared `CodeSignatureIdentityV1`/closed observation、final digest/size 与 observation 不一致、错 identifier/CDHash/Team ID/designated requirement/certificate chain、signing/attestation workflow 漂移，以及 receipt 后补或改写 signature identity 必须失败；
18. 合法 build-record digest 配错 sourceHead、builder principal/workflow/isolation、manifest digest 或 unsigned artifact，未知/撤销/旧 epoch builder record key，以及候选 record 自选 producer/current keyset 必须失败。

这些测试必须覆盖 production producer chain，不得只测试摘要纯函数或测试 seam。hostile fixture 不执行随机临时 Mach-O，也不要求关闭宿主安全策略。

## 后果

### 正向

- `sourceHead` 从格式字段升级为受保护构建输入的可验证结论；
- 关闭 ignored input、观察后 mutation 与 linked worktree ABA 三类已复发缺口；
- 把 artifact provenance、code signing 与 deployment receipt 的 authority 分开，避免一张签名同时自证源码、产物和安装；
- 为 #212 后续 schema/validator/protected build/install 纵切提供有限边界，避免继续在可变 Make/worktree producer 上滚动 rework。

### 代价

- managed-development 与 release 需要受保护 builder、sealed materialization、独立 signer 和额外 attestation store；
- 本地 ad-hoc build 仍可用于纯诊断和单元测试，但不能直接升级为可运行产品 CLI；
- 外部 certificate、allowlist、current/high-water、policy observation 仍是不可由仓库实现替代的 blockers。

## 实施顺序与退出条件

1. 接受本 ADR；在此之前不得把候选实现描述为 authority；
2. 定义 source manifest、`CompileRootManifestV1`、`ExternalBuildMaterialManifestV1`、`MarshalArtifactBuildRecordV1` 与 `MarshalArtifactBuildAttestationV1` schema、canonical encoder/validator 和 hostile fixtures；
3. 建立 source materializer → sealed compile root → closed build record → artifact signer/code-sign observation 的最短纵切；
4. 让 code-sign/release 与 `MarshalInstallReceiptV1` 精确消费 attestation；
5. 外部 provision certificate/allowlist/current authority 后，在固定安装路径完成 canary；
6. 连续通过 `version`、`doctor --self`、完整 `doctor`、`task scaffold`，再恢复真实 R3-D scaffold/plan preflight。

关闭本 ADR 实施项不等于关闭 Issue #212。Issue #212 仍须同时满足 ADR 0047 的外部 provision、安装/current/policy、固定对象执行与漂移负测；R3-D/E/F 仍按 Issue #191 的独立 exit gate 判断。

## 不做什么

- 不从可变 worktree 直接签 managed/release artifact；
- 不用 build 后 `git status`、tracked-files-only tree digest 或 40-hex `sourceHead` 代替 sealed input；
- 不要求通用 compiler syscall trace，也不以未实现的“完整依赖发现”作安全声明；
- 不自签证书、不修改 Gatekeeper/SIP/EDR、不删除 provenance、不执行随机临时 Mach-O；
- 不借本 ADR 重构 R3–R6、Adapter/Sandbox authority、能力 schema 或生命周期；
- 不把 Accepted ADR、测试存在、CI 通过或 `spctl accepted` 任一单项描述为 production ready。
