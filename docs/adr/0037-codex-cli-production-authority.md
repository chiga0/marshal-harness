# ADR 0037：Codex CLI Production Authority、宿主绑定证据与可撤销准入

- 状态：接受（Accepted，2026-08-18）——接受只冻结本 ADR 的安全合同，不表示实现、独立负向矩阵、当前宿主真实 credentialed live evidence 或后续启用变更已经完成；Codex CLI 仍须保持 production hard-disable，不得据此报告当前部署 `supported`
- 日期：2026-08-18
- 关联：[ADR 0003](0003-separate-worker-and-publisher.md)、[ADR 0004](0004-independent-verification.md)、[ADR 0006](0006-attempt-control-root.md)、[ADR 0014](0014-read-only-execution-profile.md)、[ADR 0018](0018-control-plane-and-provider-ports.md)、[ADR 0034](0034-qoder-cli-live-conformance-authority.md)、公开 Issue #136

## 上下文

Codex CLI 的本地 Adapter 可以验证版本、解析 `exec --json` 事件并限制 argv，但这些能力本身不能形成 production authority。fake executable、单次 `--version`、Adapter 自报、作者测试或某次成功运行都不能证明当前宿主、当前二进制、当前 credential、当前权限画像及当前协议仍满足 Marshal 的准入要求。

Codex 还存在平台特有的执行身份问题。若 Adapter 先按路径计算摘要，随后再按同一路径启动，攻击者或并发安装器可以在两步之间替换文件；只比较 realpath 不能消除该 TOCTOU。任务 `worktree` 与 ADR 0006 的 `controlRoot` 若只做字符串前缀判断，也可能通过符号链接、路径别名、rename 或双向嵌套破坏写域分离。普通宿主子进程仍不是恶意代码 sandbox，这一限制不能被 Adapter 的本地路径检查重新包装为强隔离承诺。

本 ADR 冻结 Codex CLI AgentAdapter 的本地 production 准入 authority、逐次消费规则、宿主路径身份和可观察性。它不是 ADR 0018 的 Sandbox Provider `ConformanceEvidence`，两者不得互换、复用 digest 或合并 assurance 声明。ADR 0034 的 Qoder 证据也不能为 Codex 提供授权。

## 决策

### 1. 默认关闭与启用条件

Codex production Adapter 采用 deny-by-default：

1. 本 ADR 接受前，production constructor 必须稳定返回 typed `codex_conformance_pending`；接受后也只有全部实施与启用门禁完成，应用 registry、doctor 与调度器才可把 Codex 列为 `supported` 或 eligible。
2. 接受本 ADR 不自动启用 Codex。必须另有独立实施变更完成本文全部机制、负向测试与跨平台门禁，并由非作者 reviewer 审查。
3. 即使实现已合入，当前宿主没有有效的 credentialed live evidence 时仍为 hard-disabled；fake、synthetic、作者自测或其他宿主 evidence 不能降级替代。
4. 最终启用必须是显式 registry 变更，并同时要求当前 `Probe` 成功。不得用环境变量、隐藏 fallback、`force` 参数或测试 hook 绕过 authority。
5. 任一逐次复核失败立即取消该次 `Probe` 或 launch 的 eligibility，不保留“上次成功”授权，也不静默回退到路径执行、未认证执行或其他 Adapter。

因此，本 ADR 与任何候选代码都不能被表述为“Codex 已 production registered”“Codex 已 supported”或“live conformance 已完成”。

### 2. Authority 角色与独立性

准入链分为四个互不替代的角色：

1. **probe-only verifier** 在没有业务仓库权限的隔离环境中运行真实 credentialed Codex CLI，产生封闭的 typed observation。它不能签发 production evidence，不能修改 Marshal 状态，也不能向输出复制 credential、完整 transcript 或环境变量。
2. **receipt authority** 观察每个真实 probe variant 的实际执行，并对 held executable identity、实际 argv/环境、scratch 写入、事件与 challenge 结果签发执行 receipt。verifier 只持 receipt public key。
3. **evidence authority signer** 按固定 policy 重新验证 verifier attestation、全部 execution receipt、candidate identity 与 observation，之后才签发 content-addressed production evidence。它不运行 Codex、不读取 credential、不接受调用者提供的新 trust anchor。
4. **Marshal consumer** 只持只读 public trust material，逐次验证 config、keyset、revocation、evidence 与当前本机 identity。Worker、Adapter、配置写入者、CapabilitySnapshot 和 doctor 都不能为自己生成通过结论。

probe-only verifier、receipt authority、launch receipt authority、config authority signer 与 evidence authority signer 必须使用彼此不同的 signing key 和 key usage。Codex Worker 不属于 authority signing role，**持有的 authority signing private key 数量必须为零**；Worker 的进程、祖先环境、文件描述符和可读文件中也不得出现 authority signing key。私钥不得进入 Marshal 进程、仓库、TaskSpec、Prompt、事件、日志、ReviewPacket 或 evidence。hermetic fixture key 必须带 test-only identity，production consumer 必须拒绝。

### 3. 封闭对象、摘要与签名投影

本节对象全部使用 UTF-8 JSON，Schema 等价于 Draft 2020-12 `additionalProperties: false`、所有列出的字段均 `required`，除非明确标为可选。parser 拒绝重复 member、未知字段、浮点数、负整数、异常类型、非法 UTF-8、尾随内容和超限输入。共同标量如下：

- `Digest`：字符串 `sha256:` 后接 64 个小写十六进制字符；
- `Id`/`KeyId`：匹配 `[A-Za-z0-9][A-Za-z0-9._:-]{0,127}`；
- `Nonce`：无 padding 的 base64url，解码后精确 32 byte；
- 时间：UTC RFC 3339、必须带 `Z`、允许最多 9 位小数；
- generation：JSON unsigned integer，范围 `1..2^53-1`；
- 数组：保持 Schema 指定顺序；标记为集合的数组必须按 UTF-8 byte lexical order 严格递增且无重复。

每种 signed object 存为封闭 `SignedEnvelopeV1`：`{"payload":<object>,"payloadDigest":Digest,"signatures":[SignatureV1]}`；`SignatureV1` 只有 `alg="Ed25519"`、`keyId:KeyId`、`value`，其中 `value` 解码后精确 64 byte。`signatures` 按 `keyId` 严格递增且无重复；除 keyset root rotation 必须精确两份外，其余对象必须精确一份。`payloadDigest = sha256(JCS(payload))`；每份签名输入是 `JCS({"domain":Domain,"payloadDigest":payloadDigest,"schemaVersion":payload.schemaVersion})`。`payload` **不得**包含自己的 digest 或 signature，envelope 也没有 envelope digest，从而消除自引用。content-addressed leaf 名称是 `payloadDigest` 的十六进制部分。不同 Domain 的 signature 永不互认：

| 对象 | Domain | 最大 envelope bytes | 签名 key usage |
| --- | --- | ---: | --- |
| `CodexProbeObservationV1` | `marshal.codex.probe-observation.v1` | 256 KiB | `verifier-attestation` |
| `CodexProbeExecutionReceiptV1` | `marshal.codex.probe-receipt.v1` | 64 KiB | `probe-receipt` |
| `CodexWorkerLaunchReceiptV1` | `marshal.codex.launch-receipt.v1` | 64 KiB | `launch-receipt` |
| `CodexProductionEvidenceV1` | `marshal.codex.production-evidence.v1` | 256 KiB | `evidence` |
| `CodexAuthorityKeysetV1` | `marshal.codex.authority-keyset.v1` | 64 KiB | bootstrap `root` |
| `CodexAuthorityConfigV1` | `marshal.codex.authority-config.v1` | 64 KiB | `config` |

六类 payload 的精确封闭字段为：

- `CodexProbeObservationV1`：`schemaVersion="marshal.codex.probe-observation.v1"`、`authorityNamespace:Id`、`authorityGeneration`、`trustRootGeneration`、`bootstrapId:Nonce`、`observationNonce:Nonce`、`verifierKeyId:KeyId`、`verifierBuildDigest:Digest`、`observedAt`、`validUntil`、`hostAttestation:LinuxHostAttestationV1`、`binaryIdentity:ExecutableIdentityV1`、`contract:CodexContractBindingV1`、`suiteDigest:Digest`、`probeArtifactDigest:Digest`、`aggregateChallengeDigest:Digest`、`topologyDigest:Digest`、`receiptDigests:Digest[1..32]`（集合）、`verdicts`。`verdicts` 只有 `credentialedInvocation`、`jsonlContract`、`permissionProfile`、`scratchOnlyWrite`、`businessRootsDenied` 五个 boolean，签发时必须全为 `true`。
- `CodexProbeExecutionReceiptV1`：`schemaVersion="marshal.codex.probe-receipt.v1"`、`authorityNamespace:Id`、`authorityGeneration`、`trustRootGeneration`、`bootstrapId:Nonce`、`suiteDigest:Digest`、`probeArtifactDigest:Digest`、`variantId:Id`、`challengeNonce:Nonce`、`startedAt`、`endedAt`、`hostIdentityDigest:Digest`、`binaryIdentityDigest:Digest`、`argvDigest:Digest`、`environmentDigest:Digest`、`topologyDigest:Digest`、`transcriptDigest:Digest`、`markerDigest:Digest`、`receiptChallengeDigest:Digest`、`eventContractDigest:Digest`、`permissionContractDigest:Digest`、`exitCode`（`0..255`）、`receiptKeyId:KeyId`。
- `CodexWorkerLaunchReceiptV1`：字段见 §11；它与 probe receipt 是按 `schemaVersion` 区分的 tagged union，不允许混用。
- `CodexProductionEvidenceV1`：`schemaVersion="marshal.codex.production-evidence.v1"`、`authorityNamespace:Id`、`authorityGeneration`、`trustRootGeneration`、`bootstrapId:Nonce`、`evidenceKeyId:KeyId`、`observationDigest:Digest`、`receiptDigests:Digest[1..32]`（必须与 observation 相等）、`issuedAt`、`validFrom`、`validUntil`、`hostIdentityDigest:Digest`、`binaryIdentityDigest:Digest`、`contractDigest:Digest`、`profileDigest:Digest`、`suiteDigest:Digest`、`probeArtifactDigest:Digest`、`aggregateChallengeDigest:Digest`、`topologyDigest:Digest`、`verifierKeyId:KeyId`、`probeReceiptKeyId:KeyId`、`verdicts`（与 observation 逐项相等且全 true）。
- `CodexAuthorityKeysetV1`：`schemaVersion="marshal.codex.authority-keyset.v1"`、`authorityNamespace:Id`、`trustRootGeneration`、`previousKeysetDigest:Digest|null`、`validFrom`、`keys:AuthorityPublicKeyV1[1..32]`、`revokedKeyIds:KeyId[0..32]`（集合）、`rootRotation:RootRotationV1|null`。`AuthorityPublicKeyV1` 只有 `keyId`、`usage`（`verifier-attestation|probe-receipt|launch-receipt|evidence|config`）、`alg="Ed25519"`、`publicKey`（base64，解码 32 byte）、`notBefore`、`notAfter`。`RootRotationV1` 只有 `newRootKeyId`、`alg="Ed25519"`、`newRootPublicKey`（base64，解码 32 byte）、`notBefore`。首次 keyset 的 `previousKeysetDigest=null`、`rootRotation=null`；普通 leaf-key rotation 的 `rootRotation=null`；root rotation 必须令 generation 精确加一并提供非 null `rootRotation`。
- `CodexAuthorityConfigV1`：`schemaVersion="marshal.codex.authority-config.v1"`、`authorityNamespace:Id`、`authorityGeneration`、`trustRootGeneration`、`keysetDigest:Digest`、`currentEvidenceDigest:Digest`、`revokedEvidenceDigests:Digest[0..256]`、`revokedSuiteDigests:Digest[0..64]`、`revokedChallengeDigests:Digest[0..256]`（三者均为集合）、`revocationSetDigest:Digest`、`hostIdentityDigest:Digest`、`bootstrapId:Nonce`、`suiteDigest:Digest`、`probeArtifactDigest:Digest`、`aggregateChallengeDigest:Digest`、`contractDigest:Digest`、`profileDigest:Digest`、`configKeyId:KeyId`、`issuedAt`。

`revocationSetDigest = sha256(JCS({"revokedChallengeDigests":...,"revokedEvidenceDigests":...,"revokedSuiteDigests":...}))`；该投影不包含 `revocationSetDigest`。config/evidence/keyset 的 payload digest 只存在 envelope 与 leaf 名称中，不出现在各自 payload。

每个 probe receipt 的 `receiptChallengeDigest = sha256(JCS({"challengeNonce":...,"markerDigest":...,"transcriptDigest":...,"variantId":...}))`。evidence signer 将已验证 receipts 按 `variantId` UTF-8 byte lexical order 排列，计算 `aggregateChallengeDigest = sha256(JCS([{"receiptChallengeDigest":...,"receiptDigest":...,"variantId":...},...]))`。observation、evidence 与 config 必须逐字段复制同一个 aggregate digest；revocation list 中任一组成 challenge digest 命中即使整个 aggregate/evidence 失效。signer 还必须逐字段复核 `authorityNamespace`、两代 generation、bootstrapId、suite/probe artifact、HostIdentity、binary、contract、topology、receipt 集合与 verdict，不能只比较聚合摘要。

时间约束同样是 Schema 后的 mandatory semantic validation：解析 observation 后必须满足 `observation.observedAt <= evidence.validFrom <= evidence.issuedAt <= evidence.validUntil <= observation.validUntil`，`observation.validUntil-observation.observedAt <= 24h`，consumer 接纳时 `now-observation.observedAt <= 24h`；未来时间允许的最大 clock skew 为 1 分钟。probe receipt 还必须满足 `startedAt <= endedAt <= observation.observedAt`。更长窗口、未来超限、过期或乱序全部拒绝。

共同子对象同样封闭：`ExecutableIdentityV1` 只有 `canonicalRealpath`（1..4096 byte）、`deviceMajor`、`deviceMinor`、`inode`、`mountIdUnique`、`size`、`mode`、`sha256:Digest`、`version`、`versionOutputDigest:Digest`；`CodexContractBindingV1` 只有 `adapterContractDigest`、`launcherBuildDigest`、`profileDigest`、`argvMatrixDigest`、`environmentDigest`、`eventContractDigest`、`permissionContractDigest`、`toolPolicyDigest`、`resultContractDigest`、`outputLimitDigest`、`nativeBudgetsDigest`、`executionProfiles`（与 §14 相同的有序集合）。其 identity/contract digest 分别对完整子对象做 JCS+SHA-256。

consumer state 不由外部 signer 签名，避免 signer 能回滚本地状态。bootstrap 先原子持久化最大 8 KiB 的封闭 `CodexConsumerBootstrapV1`，字段只有 `schemaVersion="marshal.codex.consumer-bootstrap.v1"`、`authorityNamespace:Id`、`adapterId="codex"`、`bootstrapId:Nonce`、`hostIdentityDigest:Digest`、`machineIdDigest:Digest`、`tpmHostKeyPublic`（canonical `TPM2B_PUBLIC` 的 base64，解码后 1..1024 byte）、`tpmHostKeyPublicDigest:Digest`、`tpmHostKeyQualifiedNameDigest:Digest`、`createdAt`；`tpmHostKeyPublicDigest` 必须等于 decoded public area 的 SHA-256，`bootstrapDigest=sha256(JCS(bootstrap))`。

durable active root pin 是封闭 `CodexActiveRootPinV1`：`schemaVersion="marshal.codex.active-root-pin.v1"`、`authorityNamespace:Id`、`bootstrapDigest:Digest`、`rootKeyId:KeyId`、`rootAlg="Ed25519"`、`rootPublicKey`（base64，解码精确 32 byte）、`rootPublicKeyDigest:Digest`、`trustRootGeneration`、`keysetDigest:Digest`、`activatedAt`；`rootPublicKeyDigest` 必须等于 decoded public key 的 SHA-256。generation fence 是封闭 `CodexConsumerFenceV1`：`schemaVersion="marshal.codex.consumer-fence.v1"`、`authorityNamespace:Id`、`adapterId="codex"`、`bootstrapDigest:Digest`、`hostIdentityDigest:Digest`、`bootstrapId:Nonce`、`trustRootGeneration`、`authorityGeneration`、`keysetDigest:Digest`、`configDigest:Digest`、`revocationSetDigest:Digest`、`currentEvidenceDigest:Digest`、`committedAt`。

二者不能分文件提交，而是嵌入单一、最大 24 KiB 的 `CodexConsumerAuthorityStateV1`：`schemaVersion="marshal.codex.consumer-authority-state.v1"`、`transactionId:Nonce`、`activeRootPin:CodexActiveRootPinV1`、`fence:CodexConsumerFenceV1`、`committedAt`。`stateDigest=sha256(JCS(state))`；`fenceDigest=sha256(JCS(state.fence))` 只用于 Snapshot/doctor，不写回对象。root pin 与 fence 的 namespace/bootstrap/trust generation/keyset 必须逐项相等。bootstrap/state 使用 §7 同一 owner/mode/lock/fsync/rename 规则；bootstrap 缺失时只能创建新 identity，不能从 config 恢复旧值。

cross-object validation 同样 fail closed：除 keyset rotation 外，envelope 唯一的 `signatures[0].keyId` 必须等于 payload 对应的 `verifierKeyId`、`receiptKeyId`、`launchKeyId`、`evidenceKeyId` 或 `configKeyId`，且 current keyset 中 usage 精确匹配；observation 的每个 receipt digest 必须解析为有效 `CodexProbeExecutionReceiptV1`，variant 不重复且完整覆盖 suite；evidence authority 必须重新验证 observation envelope 与全部 receipt envelope，并逐项复制 authority namespace、两代 generation、bootstrap、suite/probe artifact、host identity、binary、contract、topology、receipt 集合、aggregate challenge 与 verdict，不得只验证 digest 存在，也不得替换字段；config 的 current evidence 必须具有相同 `authorityNamespace`、`authorityGeneration`、`trustRootGeneration`、`hostIdentityDigest`、`bootstrapId`、`suiteDigest`、`probeArtifactDigest`、`aggregateChallengeDigest`、`contractDigest` 与 `profileDigest`。任何 object 不能引用自身或形成 config/evidence/keyset cycle。

### 4. Linux host attestation 与宿主精确绑定

production v1 唯一允许的平台是 Linux，host attestation 不接受 hostname 或 `GOOS/GOARCH` 自报。固定输入是：

1. `/etc/machine-id`：从 held `/etc` dirfd 以 `openat(O_RDONLY|O_NOFOLLOW|O_NONBLOCK)` 打开，要求 root-owned、single-link、非 group/other writable regular file，大小不超过 33 byte；去掉唯一可选的末尾 LF 后必须是 32 个小写十六进制字符。规范值不直接公开，只记录 `machineIdDigest=sha256(ASCII(normalizedMachineId))`。
2. TPM 2.0 hardware-backed host key：bootstrap 固定创建 `TPM2_ALG_ECC/NIST_P256`、scheme `TPM2_ALG_ECDSA/SHA256`，attributes 精确包含 `fixedTPM|fixedParent|sensitiveDataOrigin|sign` 且不得包含 `decrypt`，private material 不可导出；authority policy 必须验证 manufacturer/EK certificate chain，并拒绝 software TPM、可迁移 key 或调用者提供的 public key。记录 canonical `TPM2B_PUBLIC`、其 SHA-256、qualified-name digest 与 `tpmEkCertificateDigest`，不公开 EK certificate 正文。
3. `bootstrapId`：consumer 首次初始化 fence 时从 OS CSPRNG 生成 32 byte，写入同一耐久 fence；该值必须进入 verifier challenge 与 authority config，不可从 request 指定。

稳定 `LinuxHostIdentityV1` 是封闭对象，只有：`schemaVersion="marshal.codex.linux-host-identity.v1"`、`os="linux"`、`arch`（`amd64|arm64`）、`machineIdDigest:Digest`、`tpmEkCertificateDigest:Digest`、`tpmHostKeyPublic`（canonical `TPM2B_PUBLIC` 的 base64，解码后 1..1024 byte）、`tpmHostKeyPublicDigest:Digest`、`tpmHostKeyQualifiedNameDigest:Digest`、`bootstrapId:Nonce`。public digest 必须等于 decoded public area 的 SHA-256；`hostIdentityDigest=sha256(JCS(LinuxHostIdentityV1))`，不写回 object。observation 携带完整 stable identity；evidence、config、bootstrap/fence 与 launch receipt 携带并逐项比较该 digest。

每次实时证明使用另一个封闭 `LinuxHostAttestationV1`：`schemaVersion="marshal.codex.linux-host-attestation.v1"`、`hostIdentity:LinuxHostIdentityV1`、`challengeNonce:Nonce`、`challengeAlg="TPM2_ECDSA_P256_SHA256"`、`challengeSignature`（base64，解码后精确 64 byte `r||s`）。signature 覆盖 `SHA256(JCS({"challengeNonce":...,"hostIdentityDigest":...}))`。每次 `Probe` 与每次 launch 必须由 consumer 独立生成新 CSPRNG nonce，并用 bootstrap 中 pin 的 `tpmHostKeyPublic` 实时验证；nonce 不得复用，也不要求不同操作的 challenge bytes/signature 相等。

bootstrap trust root/config 必须精确绑定 `hostIdentityDigest` 与 `bootstrapId`。仅复制磁盘会缺少原 hardware TPM private key，challenge 失败；复制 machine-id、fence 或 public area 不能复用 evidence。能够连同 hardware TPM identity 一起克隆的环境不属于 production v1 支持范围，必须 hard-disable。重装导致 machine-id、fence 或 TPM host key 任一改变时形成新 host identity，旧 config/evidence/fence 不可迁移；必须以新的 bootstrapId 重新完成独立 live probe 和 authority 签发。fence 丢失或 TPM unavailable/cleared 也不得“恢复”旧 bootstrapId。

observation 内 `hostIdentity` 的 digest、evidence/config/bootstrap/fence/launch receipt 的 `hostIdentityDigest` 必须相等；fresh attestation 只验证“当前 TPM 仍持有 pinned identity 的 private key”，不进入稳定 identity equality。TPM、EK validation、machine-id 格式、fresh nonce 或 CSPRNG 任一不可用时返回 permanent typed failure，不得 fallback 到 hostname、MAC、DMI UUID 或纯文件 key。

版本兼容线只说明二进制可进入 conformance 验证，不构成授权。允许兼容 patch 更新时，evidence 仍必须绑定该次实际 held executable digest 与精确版本；major、minor、pre-release、build metadata、无法严格解析的版本及 evidence 未覆盖的 patch 全部 fail closed。

### 5. 冻结的真实 probe 契约

真实 probe 必须从生产 Adapter 的同一 invocation builder 机械派生 argv matrix，不得由 verifier 手写一套“预期命令”。矩阵至少覆盖：默认/显式 model、允许的输入传递形式、JSONL 事件流、成功/模型错误/协议错误/取消/超时、输出上限，以及 `workspace-write` 与 `read-only` 两种声明画像中实际支持的组合。未进入冻结矩阵的 TaskSpec 组合不具备 eligibility。

probe 环境是完整替换环境，只包含固定 allowlist；不得继承调用者 PATH、代理、Publisher credential、Marshal authority 私钥或业务仓库变量。Codex credential 只由隔离环境的 Secret Provider 交付给该次 probe，不进入 observation。网络、审批、sandbox/permission、session persistence、tool policy 与 cwd 均须显式冻结；不允许依赖用户级隐式配置、交互确认或上一次 session。

隔离环境的唯一写域是 verifier-owned scratch。所有业务仓库 root 必须作为 deny root 传入隔离执行器，并以 held identity 证明没有同目录、别名或任一方向嵌套。probe 必须用随机 challenge 证明真实 Codex 事件与 scratch marker 属于本轮执行；预制 transcript、跨 variant receipt replay、synthetic success 或 `hermetic-fixture` receipt 都不能生成 production evidence。

### 6. Trust root、key rotation 与撤销

consumer 由部署者在只读边界外固定一个 bootstrap public trust root。authority root 中的 canonical keyset manifest 由该 root 授权，至少包含递增 `trustRootGeneration`、active evidence key、not-before/not-after、revoked key ids 与 manifest digest。普通 authority config 不能添加 trust root，也不能把调用者传入的 keypair 变成新 anchor。

key rotation 必须满足以下规则：

- evidence signer 轮换通过更高 generation 的 keyset manifest 激活；旧 key 可在明确 overlap 窗口内验证既有、未撤销且仍新鲜的 evidence，但不能签发 manifest 声明生效时间之后的新 evidence；
- root 轮换的 keyset envelope 必须精确包含两份 signature：当前已 pin root 对含 `rootRotation` 的 payload 签名，新 root 对同一 payload 签名以证明持钥；consumer 先以当前 root 验证授权、再从 payload 取得新 public key 验证第二份签名，然后按 §7 将更高 `trustRootGeneration` 的新 pin 与 fence 作为同一 `CodexConsumerAuthorityStateV1` 原子提交，不能先切 pin 或先提交 fence。也允许由独立 out-of-band 部署替换 bootstrap root，但必须清空旧 eligibility 并重新 bootstrap/live probe；单个新 key 自签不能建立信任；
- `trustRootGeneration` 与 authority generation 都由 consumer 耐久 high-water fence 单调消费；rotation、撤销或 keyset identity 变化不能通过重启回退；
- revoked key、revoked evidence digest 与 revoked suite/challenge digest 均立即失效。撤销优先于 freshness 与缓存命中。

authority config 是封闭、签名、带 generation 的 current 指针，绑定 §3 列出的全部字段。相同 generation 下任一字段改变属于 identity conflict；更小 generation 属于 rollback。更高 generation 必须先与 active root pin 一起写入单一 consumer authority state，再解析其 current evidence；即使该 evidence 缺失、损坏、已撤销或暂不可读，也不得回退到旧 generation。

### 7. Consumer-owned rollback fence

active root pin 与 rollback fence 位于 Marshal consumer 独占的耐久私有目录，与只读 authority/evidence root 是独立故障域。signer、verifier 与 Worker 均无写权限。

consumer 必须：

- 从 `/` 开始逐段 `openat(O_NOFOLLOW)` 打开 authority root 与 fence root，要求 absolute clean path、真实目录、当前 uid/root owner 与精确 `0700`；
- 持续持有两侧 dirfd，以 device/inode 和双向 ancestry 遍历拒绝同目录、路径 alias、fence-under-authority 与 authority-under-fence；
- 用当前 uid/root-owned、single-link、精确 `0600` regular lock file 加 OS advisory exclusive lock，串行化跨 goroutine/跨进程消费；
- 在锁内读取并验证 committed `state.json`，验证 current pin 后才验证更高 keyset/config；构造同时包含新 pin 与新 fence 的一个 `CodexConsumerAuthorityStateV1`，不得先单独切 root 或 generation；
- 用同目录随机 `O_CREAT|O_EXCL|O_NOFOLLOW` `state.<transactionId>.tmp` 写完整 bytes，要求精确 `0600`、single-link regular file；依次执行 temp file `fsync`、`renameat(temp,"state.json")`、state directory `fsync`。只有最后一个 directory `fsync` 成功才 commit，并且在 commit 前不得解析 current evidence、发布 Snapshot 或启动 child；
- crash 在 temp write/file fsync 前后或 rename 前：旧 `state.json` 唯一有效，temp 一律忽略；crash 在 rename 后、directory fsync 前：恢复时只接受实际存在且完整合法的 `state.json`，但在重新读取 current authority、重验 generation/pin 并完成一次 directory fsync 前不得使用它授权；crash 在 directory fsync 后：新 state 唯一有效。不存在“两文件选较大 generation”或回退旧 pin 的恢复路径；
- 重启只读取 `state.json`。同代不同 transaction/state、损坏、超限、未知字段、错 owner/mode、hardlink、symlink、FIFO/device/socket、锁异常全部 fail closed；残留 temp 不构成 authority 并在持锁后安全清理。

每次 `Probe` 及每次 Worker launch guard 都必须重新从 held authority dirfd 读取 current config/keyset/revocation/evidence，验证 signature、generation fence、freshness、host、binary 与完整 contract。长寿命 `marshal-server`、进程内 cache、之前的 CapabilitySnapshot 或之前成功的 Run 均无豁免。launch guard 必须在实际 spawn 紧邻点完成，失败后不得创建子进程。

### 8. Worktree、controlRoot 与路径身份

执行请求中的 `worktree` 与 ADR 0006 `controlRoot` 都必须是 absolute clean path。consumer 从 `/` 逐段 `openat(O_NOFOLLOW)`，拒绝任一父级或 leaf symlink，并在整个 launch 与进程建立阶段持续持有 dirfd。目录 identity 至少包含 device、inode、mount identity、owner 与 mode；字符串 realpath 只作为审计字段，不是 authority。

consumer 必须对 held `worktree`、held `controlRoot` 及 `control/input`、`control/output` 执行双向 ancestry 检查：

- 任意两者同目录、路径 alias 或非契约允许的父子关系均拒绝；
- `worktree` 不得位于 `controlRoot` 下，`controlRoot` 也不得位于 `worktree` 下；
- `control/input` 必须保持只读，`control/output` 只能属于当前 Attempt；
- pre-launch、child setup 完成后与 launch receipt 接纳前重新遍历 held ancestry；任一点 topology digest 改变均拒绝；
- child cwd、控制输入与结果落点必须从 held dirfd 派生，不得在校验后重开 request path。

rename、swap、bind mount、hardlink、symlink、`..`、重复 separator、大小写/Unicode alias、父级替换、worktree/controlRoot 双向嵌套及检查后 swap-back 都必须进入负向矩阵。路径门禁是合作式本地边界的一部分，不得描述为恶意代码隔离。

### 9. Mount namespace、`statx` 与 topology phase

production v1 要求 consumer、privilege-separated launcher 与 child pre-workload barrier 位于同一个固定 Linux mount namespace。consumer 在任何路径解析前以 `open("/proc/self/ns/mnt", O_RDONLY|O_CLOEXEC|O_NOFOLLOW)` 持有 namespace fd，记录其 `fstat` device/inode；launcher 通过继承该 fd 证明 namespace identity 相等，不允许自行 `setns`、`unshare(CLONE_NEWNS)` 或接受调用者 namespace。receipt authority 只接纳该 held namespace identity。

每个 held directory/file fd 都必须执行：

```text
statx(fd, "", AT_EMPTY_PATH|AT_NO_AUTOMOUNT,
      STATX_BASIC_STATS|STATX_MNT_ID_UNIQUE)
```

返回 mask 必须包含 `STATX_TYPE|STATX_MODE|STATX_UID|STATX_GID|STATX_INO|STATX_SIZE|STATX_MNT_ID_UNIQUE`。仅有旧 `STATX_MNT_ID`、`/proc/*/mountinfo` 字符串或 device/inode 不足以授权；kernel、filesystem、seccomp 或 libc 无法提供 `STATX_MNT_ID_UNIQUE` 时返回 permanent `codex_mount_identity_unsupported`，不允许弱化 fallback。

`MountObjectIdentityV1` 是封闭对象，只有 `role`、`deviceMajor`、`deviceMinor`、`inode`、`mountIdUnique`、`mode`、`uid`、`gid`、`size`、`sha256:Digest|null`。对目录从 held fd 反复 `openat("..", O_PATH|O_DIRECTORY|O_NOFOLLOW)` 直到 held namespace root，得到由 leaf 到 root 的 `ancestorChain:MountObjectIdentityV1[1..256]`；file role 的 chain 从校验时一并持有的 parent dirfd 开始。超过 256、loop、跨 namespace 或中途 identity 改变均拒绝。

`TopologySnapshotV1` 只有 `schemaVersion="marshal.codex.topology-snapshot.v1"`、`mountNamespaceDevice`、`mountNamespaceInode`、`phase`（`consumer-open|launcher-pre-seal|child-pre-exec|child-post-exec-barrier|consumer-receipt-accept`）、`fixedRoots`、`executables`。`fixedRoots` 永远精确按 `authorityRoot,fenceRoot,worktree,controlRoot,controlInput,controlOutput` 排列，每项只有 `identity` 与 `ancestorChain`，并要求 `T0..T4` 逐字段不变。`executables` 是按 `sourceExecutable,sealedExecutable,childExecutable` 固定顺序的 1..3 项数组：`T0` 只有 source；`T1/T2` 有 source+sealed；`T3/T4` 三者齐全。每项只有 `identity` 与 `ancestorChain`。`topologyDigest=sha256(JCS(TopologySnapshotV1))`。

同一 launch 的 phase actor 与时序固定为：

1. `consumer-open`：consumer 打开全部 root/executable 与 namespace fd 后计算 `T0`；
2. `launcher-pre-seal`：独立 launcher 接收 held fd，把 source bytes 完整复制到尚未 sealing 的 memfd 后计算 `T1`；
3. `child-pre-exec`：launcher 完成 cwd/control fd 安装、`execveat` 前计算 `T2`；
4. `child-post-exec-barrier`：child 已 exec 但 workload barrier 尚未释放时，由 launcher 通过 pidfd/proc fd 计算 `T3`；
5. `consumer-receipt-accept`：consumer 接纳 launch receipt 前从原 held fd 计算 `T4`。

阶段比较不要求三种 executable 的 device/inode 相同。固定 roots 与 mount namespace 在 `T0..T4` 必须逐字段不变；source identity 在 `T0..T4` 不变，sealed identity 在 `T1..T4` 不变，child identity 在 `T3/T4` 不变。允许的唯一转换是 source bytes 被复制为 sealed memfd、sealed memfd 被 child `execveat`：三者 `sha256/size` 必须相等，sealed 与 child 另须 `device/inode/mountIdUnique` 相等；source 与 sealed 的 device/inode **预期不同**，不能据此拒绝。launch receipt 绑定 `T0..T3` 全部 phase digest，不再声明一个跨 phase 相同的 `topologyIdentityDigest`；consumer 收到 receipt 后计算 `T4`，按上述关系比较，再把 `T4` 与 receipt digest 一起写入 eligibility binding。每个 phase 由指定 actor 生成，Worker 不能生成、修改或签名 topology。其他角色新增/缺失、bind mount/namespace swap、mount-id 变化、phase 乱序或 actor 不符全部 fail closed。

### 10. Linux authenticated fd-exec 与 Darwin fail-closed

Linux 是首个可候选 production 平台。consumer 必须在校验时打开 Codex executable，要求 owner/mode/type/link count 符合 policy；独立 launcher 用 `memfd_create(MFD_CLOEXEC|MFD_ALLOW_SEALING)` 创建目标，把 held source fd 的精确 bytes 复制进去，重新读取计算 SHA-256/size 与 source identity 相等后添加 `F_SEAL_WRITE|F_SEAL_GROW|F_SEAL_SHRINK|F_SEAL_SEAL`。摘要、版本 probe 与 Worker launch 必须针对同一 sealed memfd bytes；只允许 `execveat(memfd, "", argv, env, AT_EMPTY_PATH)`，不能在摘要后重新按 pathname 或 `/proc/self/fd/<n>` 执行。

Linux launcher 必须从 held `/` 逐段无 symlink 打开 `/proc`，要求 `fstatfs` 为 `PROC_SUPER_MAGIC`，同时持有 `/proc/self/ns/pid` 并要求 child pid namespace 相等；随后验证 held fd 未被替换、memfd seals 完整、child 实际 executable identity 与 launch receipt 一致。`/proc` 不可用/不可信、pid namespace 漂移、fd-exec 不可证明、seal 缺失、解释器/shebang 导致身份退化或 kernel 行为不满足 policy 时返回 typed permanent failure，不得 fallback 到 pathname。

Darwin 当前没有被本 ADR 接受的等价 authenticated fd-exec launcher，因此 production 必须显式返回 `codex_platform_unsupported`。即使 version probe、fake tests 或普通 pathname execution 成功，也不能标记 `supported`。未来 Darwin 启用必须由新 ADR 或替代 ADR 冻结 codesign/notarization、Mach-O identity、可信 launcher 与 TOCTOU 证明，并走独立门禁。

### 11. 独立 Worker launch receipt 与 workload barrier

launch receipt 由 privilege-separated **launch receipt authority** 产生。该进程的 binary digest 必须等于 evidence 的 `launcherBuildDigest`，只持 `usage=launch-receipt` 私钥和启动所需 held fd，不持 Codex credential、Publisher credential、业务仓库写能力或其他 authority key。Adapter/consumer 与 Worker 都不能调用 signer 对任意 payload 签名；authority 只对自己实际执行并观察到的 launch state machine 签名。

每次 launch 由 Core 生成新的 `launchNonce:Nonce`，并冻结 `requestDigest=sha256(JCS({"attemptId":...,"authorityGeneration":...,"argvDigest":...,"configDigest":...,"controlRootIdentityDigest":...,"environmentDigest":...,"evidenceDigest":...,"fenceDigest":...,"launchNonce":...,"runId":...,"taskId":...,"trustRootGeneration":...,"worktreeIdentityDigest":...}))`。nonce 不能由 Adapter、launcher 或 Worker 选择，且在 `(authorityNamespace,attemptId)` 下只能消费一次。

launcher 在 child 进入 `execveat` 前建立 pidfd，并以 `PTRACE_SEIZE`/`PTRACE_O_TRACEEXEC` 捕获该 child 的 `PTRACE_EVENT_EXEC`；child 在 exec event 上保持 stopped，尚未执行 Codex user-space 指令。launcher 随后：

1. 用 pidfd 保证 PID 未复用，读取 root-owned procfs 中该 PID 的 start-time ticks 形成 birth identity；
2. 以 `openat` 打开 `/proc/<pid>/exe` 并执行 `statx(...STATX_MNT_ID_UNIQUE)`，要求 device/inode/size 与 sealed memfd 相同，再从该 fd 重新计算 SHA-256；
3. 重算 §9 的 `child-post-exec-barrier` topology；
4. 构造并签名 receipt，保持 child stopped；
5. 把 receipt 交给 consumer；只有 consumer 完整接纳并耐久记录 receipt digest 后，才向 launcher 发送一次性 release；launcher 随后 detach/continue。验证、签名、持久记录或 release 任一步失败都 kill child 并等待退出，不运行 workload。

`CodexWorkerLaunchReceiptV1` 的精确字段是：`schemaVersion="marshal.codex.launch-receipt.v1"`、`authorityNamespace:Id`、`authorityGeneration`、`trustRootGeneration`、`taskId:Id`、`runId:Id`、`attemptId:Id`、`launchNonce:Nonce`、`requestDigest:Digest`、`launcherBuildDigest:Digest`、`launchKeyId:KeyId`、`configDigest:Digest`、`evidenceDigest:Digest`、`fenceDigest:Digest`、`hostIdentityDigest:Digest`、`sourceExecutableIdentityDigest:Digest`、`sealedMemfd:SealedMemfdIdentityV1`、`child:ChildExecIdentityV1`、`argvDigest:Digest`、`environmentDigest:Digest`、`phaseDigests:Digest[4]`、`requestedAt`、`execObservedAt`、`issuedAt`。`phaseDigests` 固定按 `T0,T1,T2,T3` 排列。

`SealedMemfdIdentityV1` 只有 `deviceMajor`、`deviceMinor`、`inode`、`mountIdUnique`、`size`、`sha256:Digest`、`seals`；`seals` 必须精确等于上述四个 seal 的 bit set。`ChildExecIdentityV1` 只有 `pid`（`1..2^31-1`）、`startTimeTicks`（正整数）、`pidfdInode`、`procExeDeviceMajor`、`procExeDeviceMinor`、`procExeInode`、`procExeMountIdUnique`、`procExeSize`、`procExeSha256:Digest`。source SHA-256、sealed memfd SHA-256 与 child proc-exe SHA-256 必须三方相等；sealed 与 child 的 device/inode/mount-id/size 必须相等。

consumer 只信任 current keyset 中在 `issuedAt` 有效、未撤销、usage 精确为 `launch-receipt` 的 key，并要求 receipt 的 authority/trust-root generation、evidence/config/fence/request/Attempt/nonce 与本次 held state 精确相等；`requestedAt <= execObservedAt <= issuedAt <= consumerNow`，总窗口不得超过 5 秒，clock skew 上限 1 秒。receipt 不得跨 Attempt、跨 nonce、跨 PID birth、跨 authority generation 或跨 host replay。ptrace、pidfd、可信 procfs、post-exec stop 或 launch receipt key 任一不可用时返回 permanent typed failure，不允许启动后补签、Worker 自签或无 receipt fallback。

### 12. Protocol、结果与撤销范围

launch 成功只说明该次 spawn 通过准入，不把 evidence 变成 Worker 自证。Adapter 仍必须：

- 只接受冻结 JSONL event schema，拒绝未知关键事件、重复 terminal result、terminal 后事件、缺失 terminal、非法 UTF-8、超限行/总输出与 stdout 非协议噪声；
- 将 stderr 视为有界诊断而非可信 evidence，并执行 credential/secret redaction；
- 区分模型/协议/权限/取消/超时/输出上限/进程退出，不用一个 generic error 吞掉原因；
- 只把 Candidate 写入当前 held worktree，WorkerResult 写入当前 held `control/output`；Worker 不能写 Verification、ReviewDecision 或 Publication；
- 在 receipt 接纳与 workload release 紧邻点完成最后 authority/revocation guard。若撤销发生在 release 前则 kill child 且不得运行。

为避免在 AgentAdapter ADR 中扩张 Run 生命周期，**production v1 的强保证只到 pre-launch/release revocation**。workload release 后发生的 config/key/revocation 变化只阻止后续 `Probe` 与 launch；本 ADR 不定义运行中自动 cancel、generation bump、late-output quarantine 或新的 journal/reconcile transition，也不得宣称“running revocation 立即生效”。如未来需要该保证，必须由单独 ADR 冻结 intent-first journal、crash recovery、fencing、receipt 与合法生命周期转换后才能启用。

### 13. Typed failure carrier 与 retry mapping

production 边界不得向 Core 暴露未分类的 `os.PathError`、字符串拼接错误或模糊 `unsupported`。统一 carrier `AdapterFailureV1` 是封闭对象，精确字段为：`schemaVersion="marshal.adapter-failure.v1"`、`adapterId="codex"`、`operation`（`constructor|probe|launch|run|doctor`）、`code`（下表封闭枚举）、`retryClass`（`permanent|transient|reconcile-required`）、`safeMessage`（1..512 UTF-8 byte）、`observedAt`、`details`。`details` 是封闭对象，只有可选 `authorityGeneration`、`trustRootGeneration`、`evidenceDigest`、`configDigest`、`phase`、`platform`；不得含 path、errno message、credential、环境或 transcript。内部 cause chain 只进受控审计，不进入该 carrier。

| 类别 | 稳定 code |
| --- | --- |
| 治理/注册 | `codex_conformance_pending`、`codex_not_registered` |
| 平台/launcher | `codex_platform_unsupported`、`codex_host_attestation_unsupported`、`codex_mount_identity_unsupported`、`codex_fd_exec_unavailable`、`codex_executable_unsafe`、`codex_launch_identity_mismatch`、`codex_launch_receipt_invalid`、`codex_launch_nonce_replay`、`codex_launch_outcome_ambiguous` |
| authority 配置 | `codex_authority_config_missing`、`codex_authority_config_invalid`、`codex_authority_signature_invalid`、`codex_trust_root_invalid`、`codex_key_revoked` |
| generation/fence | `codex_authority_rollback`、`codex_authority_identity_conflict`、`codex_authority_temporarily_unavailable`、`codex_fence_invalid`、`codex_fence_lock_busy`、`codex_fence_commit_failed` |
| evidence | `codex_evidence_missing`、`codex_evidence_invalid`、`codex_evidence_expired`、`codex_evidence_not_yet_valid`、`codex_evidence_revoked`、`codex_evidence_host_mismatch`、`codex_evidence_binary_mismatch`、`codex_evidence_contract_mismatch` |
| 路径 | `codex_path_invalid`、`codex_path_unsafe_type`、`codex_path_identity_changed`、`codex_path_topology_conflict`、`codex_path_permission_invalid` |
| 协议/运行 | `codex_protocol_invalid`、`codex_permission_denied`、`codex_output_limit_exceeded`、`codex_timeout`、`codex_canceled`、`codex_process_failed` |
| 内部 fail-closed | `codex_internal_fail_closed` |

retry mapping 是封闭的：只有 `codex_authority_temporarily_unavailable` 与 `codex_fence_lock_busy` 为 `transient`，且每次重试都从 held-root 重新读取和完整验证，受既有 Attempt deadline/次数预算约束；只有 fork/exec 后无法证明 child 已 kill+wait 的 `codex_launch_outcome_ambiguous` 为 `reconcile-required`，它不得自动重启第二个 child，必须先用 pidfd/Attempt nonce 对账。其余列出的 code 与 `codex_internal_fail_closed` 全部为 `permanent`。code 与 retryClass 不匹配的 carrier 自身为 Schema invalid 并 fail closed。

未知内部错误在 Adapter 边界转换为 `codex_internal_fail_closed`，同时保留内部审计 cause。signature、rollback、revocation、identity、path topology、protocol 与平台错误不得通过 retryClass 或调用方重试放宽。

### 14. CapabilitySnapshot、doctor 与 ReviewPacket 精确绑定

现有 CapabilitySnapshot 的唯一支持状态字段保持 `probeStatus`；**不得新增或输出 `supported=true`**，避免两套状态权威。Codex 的 Schema 条件为：

1. `adapterId="codex" && probeStatus="supported"` 时，必须有且仅有 `codexAuthority:CodexAuthorityMetadataV1`，并要求现有六个 `conformanceEvidenceDigest`、`conformanceTrustRootKeyId`、`conformanceProbeProfileDigest`、`conformanceValidUntil`、`conformanceHostFingerprint`、`conformanceAuthorityGeneration` 字段全部存在；`adapterFailure` 必须不存在，既有 `probeErrors` 必须为空；
2. `adapterId="codex" && probeStatus="unsupported"` 时，`codexAuthority` 与上述六个 conformance 字段必须不存在，`adapterFailure:AdapterFailureV1` 必须存在，`probeErrors` 只能包含 `adapterFailure.safeMessage` 的单项安全投影；
3. Codex 的 `probeStatus="experimental"` 非法；当前 Schema 的完整枚举仍是 `supported|experimental|unsupported`，但 Codex production 条件只接受前述两种。其他 Adapter 不得出现 `codexAuthority` 或 Codex `adapterFailure`。未知 `probeStatus` 或上述条件冲突使整个 Snapshot invalid。

实施时必须在现有 `capability-snapshot.schema.json` 的 `additionalProperties:false` 下显式新增 `codexAuthority` 与 `adapterFailure` property，并新增上述 Codex `if/then/else`；不得通过打开 additional properties 或复用 `notes`/`probeErrors` 承载结构化 metadata。`probeStatus` 的既有字段名和枚举保持不变。

`CodexAuthorityMetadataV1` 是封闭对象，精确字段为：`schemaVersion="marshal.codex.authority-metadata.v1"`、`codexVersion`、`binaryIdentityDigest:Digest`、`hostIdentityDigest:Digest`、`platform="linux"`、`launcherKind="linux-execveat-sealed-memfd-ptrace-v1"`、`evidenceDigest:Digest`、`configDigest:Digest`、`keysetDigest:Digest`、`fenceDigest:Digest`、`suiteDigest:Digest`、`profileDigest:Digest`、`argvMatrixDigest:Digest`、`environmentDigest:Digest`、`eventContractDigest:Digest`、`permissionContractDigest:Digest`、`toolPolicyDigest:Digest`、`resultContractDigest:Digest`、`outputLimitDigest:Digest`、`nativeBudgetsDigest:Digest`、`trustRootKeyId:KeyId`、`evidenceSignerKeyId:KeyId`、`trustRootGeneration`、`authorityGeneration`、`revocationSetDigest:Digest`、`observedAt`、`validUntil`、`executionProfiles`（`read-only|workspace-write` 的 1..2 项、按该顺序、无重复数组）、`isolationClaim="cooperative-host-process-not-malicious-code-sandbox"`。全部 metadata 纳入既有 CapabilitySnapshot JCS digest；`nativeBudgetsDigest` 对现有 `capabilities.nativeBudgets` 数组做 JCS+SHA-256，`executionProfiles` 必须与现有 `capabilities.executionProfiles` 精确相等。上述六个现有 conformance 字段必须分别等于 metadata 的 `evidenceDigest`、`trustRootKeyId`、`profileDigest`、`validUntil`、`hostIdentityDigest`、`authorityGeneration`；现有字段名 `conformanceHostFingerprint` 仅是兼容性名称，其值必须是稳定 `hostIdentityDigest`，不得定义第二种 fingerprint 投影。任一不匹配则 Snapshot invalid。

doctor 必须从当前 Probe 结果机械投影 `probeStatus`、完整 `codexAuthority` 或完整 `adapterFailure`，不能复用缓存或只打印布尔值。失败视图不得输出 config/evidence/fence 的绝对路径、machine id、credential、private key、完整 argv、环境、stderr 或 transcript 正文。

每个使用 Codex 的 ReviewPacket 必须增加封闭 `CodexEligibilityBindingV1`：`schemaVersion="marshal.codex.eligibility-binding.v1"`、`taskId`、`runId`、`attemptId`、`capabilitySnapshotDigest:Digest`、`authorityEvidenceDigest:Digest`、`configDigest:Digest`、`fenceDigest:Digest`、`launchReceiptDigest:Digest`、`launchAcceptTopologyDigest:Digest`（即 `T4`）。`eligibilityBindingDigest=sha256(JCS(binding))` 进入 ReviewPacket 的 evidence digest projection；ReviewDecision 继续绑定完整 ReviewPacket/evidence digest，不能只复制 `probeStatus`、metadata 或 Worker 声明。缺少 launch receipt 或 `T4` 的 Attempt 不能进入权威 ReviewPacket。

### 15. Crash、重启、重放与负向验证矩阵

独立验证至少覆盖：

1. **默认关闭**：Proposed 状态、无 live evidence、仅 fake evidence、仅作者 evidence、未显式 registry enable 均 hard-disable。
2. **签名与内容**：未知/错误/轮换/revoked key，任一 evidence 字段缺失或替换，重复/未知 JSON member，authority namespace/generation/bootstrap/suite/probe artifact 不匹配，receipt member 缺失/重复/乱序/replay，`receiptChallengeDigest` 或 canonical `aggregateChallengeDigest` 错误，以及 binary/host/topology/profile/contract 不匹配。
3. **时间与宿主**：未来、过期、超长 TTL、陈旧 observation，同 OS/arch 不同 host，machine-id clone、磁盘 clone 缺 TPM key、reinstall/fence 丢失/TPM clear、software TPM、bootstrapId 替换、stable `hostIdentityDigest` 漂移、fresh nonce 重放，以及不同合法 fresh nonce/signature 被误要求逐字相等。
4. **generation 与 fence**：更小 generation、同代 identity conflict、更高代但 evidence 缺失/撤销、active root pin 与 fence 不一致、进程崩溃于 temp write/file fsync/rename/directory fsync 每个边界、rename 后未完成 recovery fsync 即授权、残留临时文件、损坏/超限记录、跨进程并发消费与锁争用。
5. **rotation/revocation**：active leaf-key rotation、root 双签 rotation、单个新 root 自签、旧 key overlap 边界、运行前撤销、`Probe` 后 receipt 前撤销、receipt 后 release 前撤销；release 后撤销只验证“阻止后续 Probe/launch”，不得测试或宣称中断当前 workload。
6. **路径与 mount**：config/keyset/evidence/fence/worktree/controlRoot 及其任一父级 symlink，FIFO/device/socket、hardlink、错 owner/mode、路径 alias、同目录、双向嵌套、rename/swap、mount namespace 变化、`STATX_MNT_ID_UNIQUE` 缺失、phase actor/顺序错误、topology 变化与 swap-back。
7. **Linux launcher**：path 在 digest 后替换、fixed roots 在任一 phase 改变、source 在 `T0..T4` 改变、sealed 在 `T1..T4` 改变、child 在 `T3/T4` 改变、source/sealed/child 摘要或 size 不同、sealed/child device/inode/mount 不同，以及误把 source/sealed 的预期 device/inode 差异当成拒绝条件；另覆盖 seal 缺失、procfs/pid namespace 不可信、fd 关闭/复用、PID birth/Attempt/nonce replay、receipt 超时/错 key/错 producer、ptrace/pidfd 不可用、child identity 不符、shebang/fallback，所有失败都不得 pathname 或无 receipt fallback。
8. **Darwin**：真实二进制与 fake 均稳定 `codex_platform_unsupported`，不得出现 test hook 泄露到 production constructor。
9. **协议与结果**：argv matrix 每个 variant、未知/重复/乱序/缺失 terminal、超限、取消、超时、权限拒绝、secret redaction、非零退出及 typed mapping。
10. **恢复与重放**：consumer/Marshal kill -9 后读取 committed fence；相同 config/evidence 重放幂等；旧 config、旧 evidence、旧 Snapshot、旧 launch request/receipt 均拒绝；fork/exec 后 receipt 丢失必须先按 pidfd/Attempt nonce 对账，不能启动第二个 child。

hermetic 测试只证明 canonical、signature、路径与 fail-closed 机制。真实 live gate 必须由独立环境在当前生产候选版本和当前 host 上运行，并保存非敏感 evidence metadata；作者不能对自己的实现提供权威通过结论。

## 实施顺序与接受门禁

实施必须按以下顺序推进，且每步保持 production hard-disable：

1. 先实现封闭 Schema、typed errors、canonical/signature 与 negative fixtures；
2. 再实现 TPM-backed host bootstrap、held-dirfd、`statx` mount identity、双向 ancestry、单一 active-root-pin/fence consumer state 与每个 fsync/rename 边界的 crash/replay 测试；
3. 再实现 Linux sealed-memfd `execveat`、独立 launch receipt/workload barrier 和 Darwin 显式 unsupported；
4. 再实现独立 verifier/probe receipt/evidence authority 工具链及 credentialed live probe；
5. 再接入 `probeStatus` 条件 Schema、CapabilitySnapshot/doctor exact metadata 与 ReviewPacket eligibility binding；
6. 独立 reviewer 对真实 diff、race、跨平台、secret scan 与本文负向矩阵给出 P0/P1 清零结论；
7. 当前 host 配置真实、未撤销且新鲜的 signed evidence 后，最后以单独 registry 变更显式启用；production v1 仍不得宣称 running revocation。

本 ADR 的接受只冻结合同；任何前置或后续实施切片合入都不表示 live evidence 已存在，也不表示 Issue #136 或相关 milestone 完成。

## 后果

- Codex 的 production eligibility 成为短期、宿主绑定、可撤销且可回放审计的事实，而不是 Adapter 自报或一次成功缓存。
- Linux 通过 authenticated fd-exec 消除“校验一个文件、执行另一个文件”的路径竞态；Darwin 在证明等价机制前保持 fail closed。
- worktree/controlRoot 的 held identity 与双向 ancestry 使 ADR 0006 写域分离能够抵抗路径别名和 rename/swap，但仍不把普通宿主进程描述成恶意代码 sandbox。
- 每次 `Probe` 与 launch 增加私有文件读取、签名验证、fence 锁与摘要成本；撤销和 rollback 安全优先于缓存性能。
- typed failure 与 exact doctor metadata 允许 Supervisor、ReviewPacket 和运维区分永久配置错误、可重试 I/O 与 launch outcome ambiguous，而不泄露 secret；running revocation 明确不属于 production v1 保证。
