# ADR 0038：共享 Agent Production Authority Provider 与原子准入交付

- 状态：提议（Proposed，2026-08-18）——仅冻结候选合同；未经维护者接受、实现、独立 conformance 与真实宿主 provision，不得把 Qoder CLI 或 Codex CLI 报告为 `supported`
- 日期：2026-08-18
- 关联：[ADR 0003](0003-separate-worker-and-publisher.md)、[ADR 0004](0004-independent-verification.md)、[ADR 0014](0014-read-only-execution-profile.md)、[ADR 0017](0017-provider-neutral-sandbox-contract.md)、[ADR 0018](0018-control-plane-and-provider-ports.md)、[ADR 0034](0034-qoder-cli-live-conformance-authority.md)、[ADR 0037](0037-codex-cli-production-authority.md)、Issue #136、Issue #137

## 上下文

ADR 0034 与 ADR 0037 分别冻结了 Qoder CLI、Codex CLI 的 production authority 合同。随后的真实实现审计证明，共同缺口不在 JSON parser 或 Adapter registry，而在宿主上仍缺少一个可 provision、可轮换、可恢复的**外部 authority provider**：

- 独立在线 verifier 与 Worker/Adapter 仍需真正不同的 OS principal；
- probe 的隔离、launch/deny/exit audit 与 execution receipt 不能由被测 CLI、自身 Adapter 或同 UID helper 自报；
- host attestation、monotonic fence、receipt/evidence/config key 必须有 Marshal 与 Worker 无法回滚或导出的宿主锚点；
- probe evidence 生成与 Worker launch 之间仍需一次性 workload barrier 和独立 launch receipt；
- authority bundle 的 keyset、revocation、config、evidence 与 monotonic high-water 必须作为一个 current identity 原子交付，不能由若干可协调回滚的普通文件拼装；
- key rotation、security-critical revoke、provider crash 与 lost response 必须有 Inspect/Reconcile 路径，不能依赖重启后猜测。

在 Marshal 进程里增加更多签名代码不能解决这些问题。同 UID subprocess、test fixture、普通容器标签、`chdir`/`chmod`、`sandbox-exec` 单独使用或 Adapter 自己生成 receipt，都不形成独立 authority，也不能宣称恶意代码隔离。

本 ADR 提议一个共享宿主基础设施合同。共享只覆盖 authority 的 Port、外部 principal、持久化、IPC、barrier 与 conformance 骨架；Qoder/Codex 的 profile、argv/environment、receipt/evidence Schema、host 约束和支持结论仍由 ADR 0034/0037 分别定义，不得合并成宽松的通用证据。

本 ADR 不把 AgentAdapter 本地准入 evidence 改称 ADR 0018 的 Sandbox Provider `ConformanceEvidence`。二者属于不同 authority 对象，digest、registry 字段和 assurance 不能互换。

## 决策

### 1. 新增独立的本机 Authority Port

定义 `AgentProductionAuthorityProvider`（简称 APAP）作为独立本机安全服务。它不是 Worker、AgentAdapter、SandboxProvider、Verification workload executor、Publisher 或 Marshal Core，也不写 Task/Run 生命周期。

APAP 部署只提供下列封闭 surface：除明确标注的 `AttachProbeCredential` 外均位于 APAP control socket；`AttachProbeCredential` 只位于 target child 自有的 session-scoped `CredentialIngressPort`，control socket 收到同名 operation 必须拒绝：

| operation | 调用者 | 结果 | 副作用 |
| --- | --- | --- | --- |
| `Describe` | consumer/verifier controller | provider identity、protocol/profile/platform capability | 无 |
| `BeginProbe` | verifier controller | 一次性 `probeSessionId` 与非敏感 target/endpoint identity digest；不返回 challenge、endpoint handle 或 credential handle | 创建有期限 probe session 与 stopped credential-loader child |
| `AttachProbeCredential` | Secret provider，经 session-scoped target isolation `CredentialIngressPort`；APAP control socket 不注册此 operation | 非敏感 content-addressed credential handoff receipt ref | Secret provider 与目标 child 直连，移交 opaque 一次性 capability；APAP 不接收 endpoint/fd/bytes |
| `RunProbeVariant` | verifier controller | 外部观察的 isolation/execution receipt | 启动并监督一个被测 variant |
| `FinalizeProbe` | verifier controller | observation candidate 或 typed rejection | 关闭 session，撤销 capability；不签 evidence/config 或更新 bundle |
| `ReadCurrentBundle` | Marshal consumer/verifier controller | current manifest、detached signature 与 monotonic receipt | 无 |
| `ReadBundleLeafBatch` | Marshal consumer/verifier controller | 指定 immutable bundle 的 held leaf fd batch | 无 |
| `StageBundleLeafBatch` | evidence/config、rotation 或 revocation authority | transaction-scoped staged leaf receipt | 暂存 immutable leaf；不改变 current |
| `PrepareEvidenceUpdate` | evidence/config authority | prepared bundle transaction receipt | 验证 observation 与签名，durable prepare；不改变 current |
| `PrepareRotation` | rotation authority | prepared bundle transaction receipt | 验证 rotation 与 bounded overlap，durable prepare；不改变 current |
| `PrepareRevocation` | revocation authority | prepared bundle transaction receipt | 验证 security revoke，durable prepare；不改变 current |
| `CommitBundleUpdate` | 对应 prepare authority | committed bundle/anchor receipt | 单次 compare-and-advance 并切换 current |
| `InspectBundleTransaction` | 对应 authority/recovery authority/consumer | staged/prepared/committed/aborted/unknown | 无；用于 lost-response reconcile |
| `RecoverBundleTransaction` | recovery authority | 已存在 transaction 的确定性恢复 receipt | 只补齐同一 prepared/anchor-advanced transaction；不能选择新 bundle |
| `PrepareLaunch` | Marshal consumer | stopped child、launch receipt、一次性 release identity | 创建 launch pending；不运行 workload |
| `CommitLaunch` | Marshal consumer | release receipt | 单次释放已验 receipt 的 child |
| `AbortLaunch` | Marshal consumer | kill/wait receipt | 终止 pending child |
| `InspectLaunch` | Marshal consumer | `pending|released|aborted|exited|unknown` observation | 无；用于 lost-response reconcile |
| `WatchEpoch` | Marshal consumer | monotonic epoch/revocation notification | 只读提示；每次使用仍须主动复核 |

`Describe` 不能授予 eligibility。只有 `ReadCurrentBundle` 的 current、有效 bundle，加上对应 profile 的逐次验证与必要 launch receipt，才能让 Adapter 产生候选 `supported` Snapshot。`WatchEpoch` 只是低延迟提示，不替代逐次 current recheck。

### 2. Port 身份与最小 IPC

v1 transport 固定为本机 Unix domain `SOCK_SEQPACKET`（平台不支持时可用保留消息边界的等价本机 transport，但必须经替代 ADR）：

- socket 位于 root/系统服务拥有、非 group/other writable 的真实目录；从 `/` 逐段 nofollow 打开并 pin identity；
- 双方以 OS peer credential 验证 `uid/gid/pid` 与 code/binary identity，不能只信请求字段；Linux 使用 `SO_PEERCRED` 加 held `/proc/<pid>/exe`/pidfd identity，Darwin 使用 kernel peer credential 与 code-signing designated requirement；
- executable、authority root、worktree、control root、scratch 等非 secret 对象只通过 APAP control socket 的 `SCM_RIGHTS` 或平台等价不可转移 handle 传递；request 中的 pathname 仅供审计且不能重新打开。credential capability 明确排除在 APAP fd table 外，只能走 §5 的 target-isolation `CredentialIngressPort`；
- 每个 packet 是 length-delimited canonical JSON envelope 加可枚举 fd table。最大 envelope 64 KiB、每包最大 fd 32；未知 operation、member、fd role、重复 JSON member、超限与尾随 bytes 全部拒绝；bundle leaf 必须走 batch，不能靠提高单包 fd 上限；
- APAP control request（明确排除 `AttachProbeCredential`）固定为 closed `APAPRequestEnvelopeV1`：`schemaVersion`、`protocolFamily=marshal.agent-production-authority`、`protocolVersion=1`、`audience`、`requestId`、`commandId`、`callerPrincipalDigest`、`providerInstanceId`、`authorityProfile`、`operation`、`issuedAt`、`expiresAt`、`nonce`、`expectedProviderSequence`、`payload`、`requestEnvelopeDigest`。`requestEnvelopeDigest=sha256(JCS(envelopeWithoutRequestEnvelopeDigest))`；payload 中禁止同名 envelope 字段，业务对象摘要使用 `probeRequestDigest`、`apapLaunchRequestDigest`、`profileRequestDigest` 等精确名称；
- `expectedProviderSequence` 对所有有副作用的 operation 必须是非空 `uint64`，等于服务端 current sequence 才能执行；只读 operation 固定为 `null`。同 `commandId+requestEnvelopeDigest` 的 lost-response replay 幂等返回同一 receipt；同 commandId 不同 digest 固定 conflict；nonce、expiry 与 peer principal 每次都复核；
- response 固定为 closed `APAPResponseEnvelopeV1`：对应 request 的 protocol/audience/request/command/provider/profile/operation identity、唯一的 `observedProviderSequence`、`safeCode`、`safeMessage`、operation-specific `payload` 与 `responseEnvelopeDigest`。`responseEnvelopeDigest=sha256(JCS(envelopeWithoutResponseEnvelopeDigest))`；success payload 禁止重复这些字段；
- envelope 与 profile leaf/receipt 中因签名域必须重复的 identity 必须逐字节相等；不相等返回 `identity-mismatch`。`providerSequence` 在公开 framing 中只由 `observedProviderSequence` 表示，profile-specific signed object 内的同义字段仍须等值，禁止再在 success payload 复制；
- socket credential 只证明 caller 身份，不是跨 operation bearer grant。verifier controller 不能调用 launch，consumer 不能请求 credential capability，Worker 不能连接 socket；每个 operation 都按 peer principal 和 active policy 单独授权。

本 ADR **新定义的 shared** signed closed object 统一内嵌同一个 exact closed `SignedObjectEnvelopeV1`，字段只有 `objectDigest/signatureAlgorithm/signatureEncoding/keyId/keyEpoch/signatureDomain/signature`。`signatureAlgorithm=Ed25519`、`signatureEncoding=base64url-unpadded`、signature 解码后精确 64 bytes；签名消息是 envelope 内 UTF-8 `signatureDomain` 后连接 lowercase ASCII `objectDigest`。除 detached bundle signature 由 §7 明确以 `bundleDigest` 为 `objectDigest` 外，`objectDigest=sha256(JCS(parentObjectWithoutSignedObjectEnvelope))`。embedding Schema 必须固定唯一 domain 与 key usage；verifier 必须用对应 producer principal 的 current、未撤销 key 验证 key id/epoch/usage/domain/signature。envelope 缺失、额外字段、错 domain/usage/epoch/producer 或 parent digest 不等全部拒绝。ADR 0034/0037 已冻结的 profile-specific object 继续使用各自签名表示，不得被这个 shared envelope 改写；shared object 只以 digest 精确绑定它们。

非本机 transport 不属于 v1。未来远程化必须单独冻结 mTLS/audience/scope/replay、fd/capability 等价物和故障域，不能把该本机 socket 直接暴露为网络 API。

`CredentialIngressPort` 使用另一 protocol family `marshal.agent-credential-ingress`，只接受唯一 operation `AttachProbeCredential`。closed `CredentialIngressRequestV1` 精确包含 `schemaVersion/protocolFamily/protocolVersion/audience/requestId/commandId/secretProviderPrincipalDigest/providerInstanceId/authorityProfile/probeSessionId/targetIsolationIdentityDigest/credentialIngressEndpointIdentityDigest/credentialIngressTicketDigest/issuedAt/expiresAt/nonce/payload/requestDigest`；`requestDigest=sha256(JCS(objectWithoutRequestDigest))`。target child 从 signed ticket 得到 session 创建时的 provider sequence，不接受 APAP `expectedProviderSequence`，也不能执行任何 control operation；同 command+digest replay 返回同一 receipt ref，同 command异digest拒绝。

operation payload 的最小 v1 形状如下；表中未列字段一律非法，fd role 必须与表中集合精确相等：

| operation | request payload | request fd role | success payload |
| --- | --- | --- | --- |
| `Describe` | `{}` | 无 | `providerBuildDigest/platform/profiles[]` |
| `BeginProbe` | `candidateIdentityDigest/suiteDigest/probeArtifactDigest/policyDigest/challengeDigest/deadline` | `candidateExecutable,scratchRoot,credentialRoot,businessDenyRoot[1..16]` | `probeSessionId/targetIsolationIdentityDigest/credentialIngressEndpointIdentityDigest/expiresAt`；challenge 只来自 request，不在 response 重复，endpoint handle 不返回 |
| `AttachProbeCredential`（只存在于 `CredentialIngressPort`） | `probeSessionId/capabilityIdentityDigest/capabilityPolicyDigest/serviceIdentityDigest/capabilityExpiresAt/deliveryNonce/targetIsolationIdentityDigest` | Secret provider 与 target child 的直连 socket 上精确一个 `credentialCapability`；APAP fd table 中出现即拒绝 | `credentialHandoffReceiptRef`；两份 signed receipt 存入独立只读 receipt store，APAP/verifier 只持非敏感 digest ref，不返回或回显 handle |
| `RunProbeVariant` | `probeSessionId/variantId/invocationManifestDigest/credentialHandoffReceiptRef/previousReceiptDigest/deadline` | 无；只复用隔离边界内 pinned handle | `receiptDigest/receipt/nextVariantSet` |
| `FinalizeProbe` | `probeSessionId/orderedReceiptDigests/observationDigest` | 无 | `observationCandidateDigest/observationCandidate/credentialRevocationReceiptDigest` |
| `ReadCurrentBundle` | `minProviderSequence` | 无 | `bundleDigest/manifest/detachedSignature/anchorReceipt`；不返回 leaf fd |
| `ReadBundleLeafBatch` | `bundleDigest/orderedLeafIndexes[1..24]` | 无 | `bundleDigest/orderedLeafIndexes/leafIdentityDigests`，并按相同顺序返回 `bundleLeaf[1..24]` held fd |
| `StageBundleLeafBatch` | `bundleTransactionId/updateKind/orderedLeafDescriptors[1..24]` | 与 descriptor 顺序一致的 `bundleLeaf[1..24]` | `bundleTransactionId/stagedLeafDigests/stagingReceiptDigest` |
| `PrepareEvidenceUpdate` | `bundleTransactionId/manifest/detachedSignature/updateAuthorization/observationCandidateDigest/previousBundleDigest` | 无 | `bundleTransactionId/bundleDigest/anchoredNextProviderSequence/preparedReceiptDigest` |
| `PrepareRotation` | `bundleTransactionId/manifest/detachedSignature/updateAuthorization/previousBundleDigest` | 无 | 同上 |
| `PrepareRevocation` | `bundleTransactionId/manifest/detachedSignature/updateAuthorization/previousBundleDigest` | 无 | 同上 |
| `CommitBundleUpdate` | `bundleTransactionId/bundleDigest/originalExpectedProviderSequence/anchoredNextProviderSequence/preparedReceiptDigest` | 无 | `bundleDigest/anchorReceipt/commitReceiptDigest` |
| `InspectBundleTransaction` | `bundleTransactionId/bundleDigest` | 无 | `status=staged|prepared-no-anchor|anchor-advanced-not-committed|committed|aborted|unknown`、`originalExpectedProviderSequence/observedCurrentProviderSequence/anchoredNextProviderSequence` 与已有 receipt digest；`unknown` 不携伪造 receipt |
| `RecoverBundleTransaction` | `bundleTransactionId/bundleDigest/originalExpectedProviderSequence/observedCurrentProviderSequence/anchoredNextProviderSequence/preparedReceiptDigest/anchorReceiptDigest|null` | 无 | `status=committed/anchorReceiptDigest/commitReceiptDigest/recoveryReceiptDigest`；unknown/aborted 返回 typed failure，不伪造 success receipt |
| `PrepareLaunch` | `taskId/runId/attemptId/authorityNamespaceId/launchNonce/apapLaunchRequestDigest/profileRequestDigest/bundleDigest/evidenceDigest/configDigest/fenceDigest/candidateExecutableIdentityDigest/authorityRootIdentityDigest/fenceRootIdentityDigest/worktreeIdentityDigest/controlRootIdentityDigest/controlInputIdentityDigest/controlOutputIdentityDigest/mountNamespaceIdentityDigest/argvDigest/environmentDigest/deadline` | `candidateExecutable,authorityRoot,fenceRoot,worktree,controlRoot,controlInput,controlOutput,mountNamespace` | `launchTransactionId/apapLaunchRequestDigest/profileRequestDigest/launchReceiptDigest/launchReceipt/releaseIdentity/deadline`；child 保持 stopped |
| `CommitLaunch` | `launchTransactionId/launchReceiptDigest/releaseIdentity/durableAcceptDigest` | 无 | `status=released/releaseReceiptDigest/releaseReceipt` |
| `AbortLaunch` | `launchTransactionId/reasonCode` | 无 | `status=aborted|exited/abortReceiptDigest/abortReceipt` |
| `InspectLaunch` | `attemptId/launchNonce/apapLaunchRequestDigest/profileRequestDigest` | 无 | `status=pending|released|aborted|exited|unknown` 与该状态已有 receipt digest；`unknown` 不携伪造 receipt |
| `WatchEpoch` | `afterProviderSequence` | 无 | `bundleDigest/revocationSetDigest/observedAt` |

所有 `*Digest`、ID、时间、数组 cardinality 与 tagged object 都必须由 Draft 2020-12 closed Schema 固定。成功的 `safeCode=ok`、`safeMessage=""`；失败没有 success payload。任何 operation 的 request/response Schema 未落地并通过 canonical vectors 前，该 operation 不得注册。

### 3. 外部 principal 与部署所有权

生产部署至少分离以下 principal；一个进程可承担多个无私钥的只读角色，但不能合并下表互斥的 key/权限：

| principal | 持有/权限 | 明确禁止 |
| --- | --- | --- |
| OS provisioner | 安装服务、首次 pin provider/root identity、离线恢复授权 | 运行 Worker、日常签 evidence |
| verifier controller | probe policy、challenge、仅调用 Begin/Run/Finalize | 业务仓库写、CredentialIngress endpoint handle、credential handle、evidence/config/receipt/launch 私钥 |
| Secret provider | 验证 target child kernel peer identity 后经 session-scoped CredentialIngress 直连移交 opaque capability、credential-delivery signing key | 连接 APAP credential fd table、把 capability 交给 verifier/Marshal/APAP、读业务仓库、签 probe/evidence/config |
| isolation/receipt authority | 强制启动与外部 audit、仅转送 opaque capability、probe receipt key | 映射/读取 credential、evidence/config key、Marshal 状态写入 |
| evidence/config authority | 验证完整 observation/receipt 后签 bundle leaf 并提交 evidence update | 运行 CLI、读 credential、轮换 root、执行 security revoke |
| rotation authority | 授权 key/root rotation 与 bounded overlap | 签 probe observation、执行 security revoke、离线 recovery |
| revocation authority | 授权 security-critical revoke | 签 probe observation、降低 generation、离线 recovery |
| recovery authority | 恢复已存在 prepared/anchor-advanced transaction | 选择新 bundle、改 leaf、降低 sequence/generation、签日常 update |
| launch authority | held fd launch、stopped-child barrier、launch receipt key | evidence/config key、Publisher credential、业务仓库任意写 |
| APAP state/anchor service | monotonic counter、current bundle pointer、revocation、transaction journal | 解释 Task 生命周期或生成 ReviewDecision |
| Marshal consumer | public trust material、bundle/launch receipt 验证 | authority private key、probe credential、越过 barrier 释放 child |
| Worker | 仅当次 workload capability | 任意 authority private key、APAP socket、Publisher credential |

credential-delivery、receipt、verifier-attestation、evidence、config、bundle-manifest、rotation、revocation、recovery、launch、root/operator 的 private key 必须是不同 key，usage 封闭且不可跨用。优先存于 TPM/Secure Enclave/HSM 或系统 keystore 的不可导出对象；软件 key 只有在对应 profile 明确允许且其文件由独立 OS principal 独占、Marshal/Worker 不可读时才可成为较低 assurance，绝不能冒充 hardware-backed profile。

APAP 以 system service 部署：Linux 使用 root-owned system service 与独立 service users；Darwin 使用 root-owned launch daemon，涉及强制隔离或进程审计时必须另有已签名、已批准的系统扩展/entitlement。用户 session 内同 UID helper 不满足 production principal 分离。

### 4. Profile 分离与支持矩阵

`authorityProfile` 是封闭枚举，v1 只允许 `qoder-cli-adr0034-v1` 与 `codex-cli-adr0037-v1`。共享 provider 不定义通用 argv、环境、credential、receipt 或 evidence payload：

- Qoder 继续使用 ADR 0034 的四变体、一次性 credential capability、OS isolation audit、host/fence/trust ledger 与 exact manifest；
- Codex 继续使用 ADR 0037 的 contract matrix、TPM-backed stable host identity/fresh nonce、mount topology、sealed memfd fd-exec 与 launch receipt；
- 一个 profile 的 bundle、key usage、receipt、capability、evidence 或 conformance 不能映射到另一个 profile；
- provider `Describe` 可以报告 `available|unsupported|misconfigured`，但 Adapter 只有完整门禁通过才可报告 `supported`。

平台支持矩阵如下；“候选”只表示允许进入实现与真实 conformance，不表示当前仓库或宿主已经支持：

| 平台/profile | v1 状态 | 成为候选的必要机制 |
| --- | --- | --- |
| Linux/Qoder | 条件候选 | 独立 UID/namespace/cgroup、seccomp/Landlock 或等价 pathname/syscall deny、只读 credential capability、锁定网络目的、外部 launch/deny/exit audit、host attestation 与 OS monotonic anchor |
| Linux/Codex | 条件候选 | ADR 0037 的 hardware TPM、`STATX_MNT_ID_UNIQUE`、可信 procfs/pid namespace、pidfd+ptrace exec stop、sealed memfd+`execveat(AT_EMPTY_PATH)` 与 launch barrier |
| Darwin/Qoder | `unsupported` | 需后续实现并独立证明 signed privileged provider、kernel-enforced file/network/process policy、外部 denial audit、不可导出 host key 与 monotonic anchor；`sandbox-exec` 单独使用不满足 |
| Darwin/Codex | `unsupported` | ADR 0037 已要求替代 ADR 冻结 codesign/notarization、Mach-O held identity、可信 fd/immutable execution 与 post-exec barrier；pathname exec 或 `sandbox-exec` 不满足 |

缺少任一必需 kernel/OS 能力时必须返回稳定 permanent unsupported。不得用容器标签、普通 subprocess、同 UID supervisor、测试 fixture、可写 audit log 或用户确认降级。

### 5. Probe-only 与 Run 权限隔离

Probe session 与 Worker launch 是两条不可互换的权限路径：

- `BeginProbe` 只接受 verifier controller principal，绑定 profile、provider instance、host identity、candidate held executable、suite/artifact/policy digest、随机 challenge、scratch/deny roots 与 deadline；
- verifier controller 只创建 session 和 challenge，永远收不到 CredentialIngress endpoint handle、credential fd、bearer token 或可转交 capability。isolation authority 创建 stopped target child 后，只把非敏感 `probeSessionId/targetIsolationIdentityDigest/credentialIngressEndpointIdentityDigest` 写入 APAP session；可连接的 endpoint handle 只通过独立 OS-authorized channel 交给 Secret provider；
- `CredentialIngressPort` 是 target child 在其隔离 namespace 内持有的 session-scoped Unix `SOCK_SEQPACKET` listener，不是 APAP socket、broker 或 fd table。Secret provider 必须同时验证 kernel peer credential、held child executable/pid birth/namespace identity、APAP 签发的单次 session ticket 与 endpoint identity；target child 反向验证 Secret provider peer principal。任一不等即关闭连接且无 receipt；
- 单次 session ticket 是 exact closed `CredentialIngressTicketV1`：`schemaVersion/providerInstanceId/authorityProfile/providerSequence/producerPrincipalDigest/probeSessionId/targetIsolationIdentityDigest/credentialIngressEndpointIdentityDigest/secretProviderPrincipalDigest/ticketNonce/issuedAt/expiresAt/signedObjectEnvelope`，producer只能是APAP state service，domain=`marshal-credential-ingress-ticket-v1\0`、key usage=`credential-ingress-ticket`。可连接 endpoint handle 与 ticket 只由 isolation authority 通过 Secret provider 专属 OS-authorized channel 交付，controller/APAP public response 只有 endpoint identity digest；ticket 到期、peer 不符或重放即永久关闭该 endpoint；
- Secret provider 与 target child 建立直连后调用 `AttachProbeCredential`，把精确 profile、service identity、policy、expiry 与 delivery nonce 绑定的一次性 capability 直接交给 child 的固定只读 fd。APAP、controller、Marshal 与 isolation/receipt authority 从不持有该连接或 capability fd，也不代理 bytes；若平台不能从 OS audit 证明连接两端正是 Secret provider 与 target child，或不能阻止 listener/fd 被父进程继承、复制、redeem，则该 profile permanent `isolation-unavailable`；
- target child安装后由外部isolation/receipt authority只根据kernel connection/fd-install audit签install receipt，不读取capability。两份receipt写入该authority拥有的content-addressed只读receipt store；APAP/verifier只得到exact closed `CredentialHandoffReceiptRefV1={deliveryReceiptDigest,installReceiptDigest}`，不能取得CredentialIngress endpoint或capability。它们可按digest请求独立receipt verifier返回pass/fail，但不在APAP/session持久化receipt正文。session关闭、失败或expiry时必须由Secret provider/target child撤销并关闭；target child不能把capability转交父进程或其他child；
- `CredentialDeliveryReceiptV1` 是 closed object：`schemaVersion/providerInstanceId/authorityProfile/probeSessionId/capabilityIdentityDigest/capabilityPolicyDigest/serviceIdentityDigest/capabilityExpiresAt/deliveryNonce/targetIsolationIdentityDigest/credentialIngressEndpointIdentityDigest/secretProviderPrincipalDigest/targetChildPrincipalDigest/issuedAt/signedObjectEnvelope`，其 envelope domain 固定 `marshal-credential-delivery-receipt-v1\0`、key usage 固定 `credential-delivery`；
- `CredentialInstallReceiptV1` 是 closed object：`schemaVersion/providerInstanceId/authorityProfile/receiptAuthorityPrincipalDigest/probeSessionId/deliveryReceiptDigest/capabilityIdentityDigest/targetIsolationIdentityDigest/credentialIngressEndpointIdentityDigest/targetChildPrincipalDigest/installedFdRole/installedCapabilityIdentityDigest/kernelAuditDigest/installedAt/signedObjectEnvelope`，domain固定`marshal-credential-install-receipt-v1\0`、key usage固定`credential-install-receipt`。两份receipt均不含raw handle、credential bytes或credential-derived digest；`RunProbeVariant`只有从receipt store按精确digest读取并逐项验证签名、current key/epoch、session/profile/service/target/capability相等后才能启动；
- probe 的唯一写域是 session scratch，业务 repository roots 必须显式作为 deny roots；每个 variant 的 held argv/environment 与实际 launch audit 逐 token/name/value representation 对账；
- `PrepareLaunch` 只接受 Marshal consumer principal，并绑定 Task/Run/Attempt、current bundle/evidence/config/fence、`authorityNamespaceId`、held executable/worktree，以及 ADR 0037 T1–T3 所需的 `authorityRoot/fenceRoot/controlRoot/mountNamespace` handle 与 control input/output、argv/environment digest、Core 生成的一次性 launch nonce；它不接受 probe capability；
- `apapLaunchRequestDigest` 固定为 `sha256(JCS(APAPLaunchAuthorityRequestV1))`；该 closed object 精确包含 envelope 的 provider/profile identity、`expectedProviderSequence`、Task/Run/Attempt、authority namespace、nonce、`profileRequestDigest`、bundle/evidence/config/fence digest、八个 request fd 的 held identity digest、argv/environment digest 与 deadline。八项按 `candidateExecutable,authorityRoot,fenceRoot,worktree,controlRoot,controlInput,controlOutput,mountNamespace` 固定顺序且逐名存在；fd table identity、payload identity 与 receipt 中的同名值必须全部相等；
- Codex profile 的 `profileRequestDigest` 精确等于 ADR 0037 已冻结公式的 `CodexWorkerLaunchReceiptV1.requestDigest`，不与 `apapLaunchRequestDigest` 相等；其中 `authorityNamespace` 逐字节等于本 Port 的 `authorityNamespaceId`。APAP request/receipt 同时绑定两个不同 domain 的 digest；`WorkloadLaunchReceiptV1` 还绑定实际 child exec identity、stopped-child/release identity，并把 `authorityRoot,fenceRoot,worktree,controlRoot,controlInput,controlOutput` 精确映射到 ADR 0037 `TopologySnapshotV1.fixedRoots` 的 T1/T2/T3 同名项，`mountNamespaceIdentityDigest` 精确映射到三阶段的 mount namespace device/inode identity。Qoder profile 也保留 ADR 0034 自己的 profile request digest，再由 `apapLaunchRequestDigest` 外层绑定，禁止把两个 domain 摘要视为相等或互相替代；
- shared launch binding 使用 exact closed `APAPLaunchBindingReceiptV1`：`schemaVersion/providerInstanceId/authorityProfile/launchAuthorityPrincipalDigest/expectedProviderSequence/taskId/runId/attemptId/authorityNamespaceId/launchNonce/apapLaunchRequestDigest/profileRequestDigest/profileLaunchReceiptDigest/candidateExecutableIdentityDigest/authorityRootIdentityDigest/fenceRootIdentityDigest/worktreeIdentityDigest/controlRootIdentityDigest/controlInputIdentityDigest/controlOutputIdentityDigest/mountNamespaceIdentityDigest/t1TopologyDigest/t2TopologyDigest/t3TopologyDigest/stoppedChildIdentityDigest/releaseIdentity/issuedAt/signedObjectEnvelope`，domain=`marshal-apap-launch-binding-receipt-v1\0`、key usage=`launch`。profile launch receipt 仍完全按 ADR 0034/0037 验证；shared receipt 只增加外层精确绑定，不能替代或重算 profile digest；
- launch authority 必须先创建并保持 child stopped，外部验证 exec identity/topology 后签 receipt。Marshal durable 接纳精确 receipt digest 后才能调用 `CommitLaunch`；commit 只消费一次；失败或超时必须 kill+wait；
- Worker child 不继承 APAP socket、authority fd、receipt pipe、private key、verifier handle、ambient session/agent socket 或 Publisher credential；显式 allowlist 以外 fd 全部关闭；
- release 后的运行期撤销语义仍分别受 ADR 0034/0037 限制。本 ADR 不偷偷增加新的 Run 状态；要实现运行中立即 revoke，必须再冻结 Core journal/generation/quarantine 生命周期合同。

### 6. Held identity 与外部 audit receipt

所有安全关键对象在检查到使用期间保持 held handle：executable、authority bundle root/leaf、scratch、credential root/capability、business deny roots、worktree、control input/output、mount/pid namespace 与 stopped child。identity 至少绑定平台可用的 device/inode/mount-or-volume generation/owner/mode/type/link count/size/content digest；路径只是非权威审计投影。

probe `IsolationExecutionReceiptV1` 和 launch `WorkloadLaunchReceiptV1` 使用 profile-specific closed payload，但共同 envelope 必须绑定：

- `providerInstanceId`、`providerBuildDigest`、`authorityProfile`、host identity、provider monotonic sequence；
- session/Task/Run/Attempt/variant/nonce/request digest（不适用项必须由 tagged schema 排除，不能置空冒充）；
- source executable held identity、实际 child exec identity、全部 fixed root identity、topology/audit phase digests；
- 实际 argv/environment manifest digest、credential capability identity（仅 probe）、network policy digest、started/exec/ended/issued time；
- launch、deny、exit audit digest与 producer principal/key id/epoch；
- `previousReceiptDigest` 或 profile 冻结的 chain/aggregate identity。

receipt authority 只对其实际 launch 和外部 audit 签名；请求方不能提供待签 payload。audit source 必须是 Worker/被测 CLI 不可写、不可伪造的 OS observation channel。布尔自报、stdout marker、容器标签、同 UID log file 不能单独满足 denial 或 launch evidence。

### 7. 原子 Authority Bundle 与 monotonic fence

共享 `AuthorityBundleManifestV1` 只定义跨 profile 的交付外壳，profile leaf 仍保持 ADR 0034/0037 精确 Schema。manifest 是 closed object：

```text
schemaVersion = marshal.agent-authority-bundle.v1
providerInstanceId
authorityProfile
hostIdentityDigest
providerSequence
authorityGeneration
trustRootGeneration
keysetDigest
revocationSetDigest
configDigest
evidenceDigest
profileLeaves[]
createdAt
validUntil
previousBundleDigest|null
transactionId
```

`profileLeaves` 是 `AuthorityBundleLeafV1[1..64]`；每项只有 `leafKind`（profile Schema 冻结的枚举）、`digest`、`size`（`1..1 MiB`）与 `mediaType=application/json`，按 `(leafKind,digest)` UTF-8 bytes 严格递增且无重复。它精确覆盖该 profile 要求的 keyset/trust ledger、host attestation、config、evidence、receipt aggregate、policy 与 revocation leaf；不允许缺失、额外、跨 profile 或循环引用。`bundleDigest=sha256(JCS(manifest))`，manifest 由 usage=`bundle-manifest` key 签名；该 key 不能替代 profile 的 evidence/config/root key。

大小与传输边界固定为：canonical manifest 不超过 64 KiB；单 leaf `1..1 MiB`；全部 leaf 的 declared size 与实际 size 总和不超过 8 MiB；leaf 数不超过 64。`ReadCurrentBundle` 只在一个无 fd packet 返回 manifest、detached signature 与 anchor receipt。consumer 先冻结 `(providerInstanceId,authorityProfile,providerSequence,bundleDigest)`，再按 manifest 顺序以最多 24 个 leaf 的 `ReadBundleLeafBatch` 取完 held fd，逐项重算 size/digest/type；完成后再次 `ReadCurrentBundle`，只有四元组与 detached signature/anchor receipt 完全相同才可使用。leaf 以 bundle digest+index content-addressed 且提交后永不变；缺失或批次错序全部 fail closed。

detached signature 固定为 closed `AuthorityBundleSignatureV1`：

```text
schemaVersion = marshal.agent-authority-bundle-signature.v1
signedObjectEnvelope.objectDigest = bundleDigest
signedObjectEnvelope.signatureAlgorithm = Ed25519
signedObjectEnvelope.signatureEncoding = base64url-unpadded
signedObjectEnvelope.keyId
signedObjectEnvelope.keyEpoch
signedObjectEnvelope.signatureDomain = marshal-agent-authority-bundle-v1\0
signedObjectEnvelope.signature
```

签名不进入 manifest，也不作为 leaf fd 传输；它与 manifest 在同一个 `ReadCurrentBundle` response payload 中交付。consumer 必须用 OS provision 时 pin 的初始 trust root，或上一个已接受 bundle 的 trust root/keyset 验证 envelope 的 `keyId/keyEpoch` 在 `createdAt` 有效、usage 精确为 `bundle-manifest`、未撤销且算法/编码/domain 精确匹配；不能先信任待验证 bundle 自带的新 keyset。rotation bundle 必须同时满足旧 current root 的 authorization/cross-sign continuity 与新 root ledger；未知算法、key epoch、签名或 manifest 字段全部拒绝。bundle-manifest key 不能替代 profile 的 evidence/config/root/credential-delivery key。

更新只能由授权 principal 通过下列事务完成，verifier controller、Marshal consumer 与 Worker 不能提交 bundle：

1. evidence/config authority、rotation authority 或 revocation authority 先用 `StageBundleLeafBatch` 在 transaction-scoped、content-addressed、immutable staging namespace 写全部 leaf，逐项重算 digest、size/type 并 fsync；各 authority 只能选择自己的 `updateKind=evidence-update|planned-rotation|security-revocation`；
2. 对应 authority 调用唯一匹配的 `PrepareEvidenceUpdate|PrepareRotation|PrepareRevocation`；request envelope 的 `expectedProviderSequence` 成为 transaction 的 `originalExpectedProviderSequence` 且必须等于 current，`anchoredNextProviderSequence=originalExpectedProviderSequence+1`；manifest 的 `previousBundleDigest/providerSequence` 必须分别等于 current digest 与 anchored next；provider 验证 profile-specific signature、generation、freshness、host、revocation、authorization 与全部 staged leaf；
3. 追加 `prepared(transactionId,updateKind,callerPrincipalDigest,bundleDigest,originalExpectedProviderSequence,anchoredNextProviderSequence,authorizationDigest)` 到 APAP 私有 journal并durable；prepare不推进provider sequence；
4. 同一个prepare authority调用`CommitBundleUpdate`，request envelope仍携带original expected。独立monotonic anchor对`(providerInstanceId,authorityProfile,transactionId,bundleDigest,originalExpectedProviderSequence,anchoredNextProviderSequence)` compare-and-advance，返回绑定previous anchor receipt的signed receipt；
5. anchor advance 立即使权威 current sequence 成为 anchored next；随后追加 `committed`、原子切换 current bundle pointer并完成file/directory fsync；response envelope 的 `observedProviderSequence=anchoredNextProviderSequence`；
6. security revoke 的失效线性化点是 anchor advance；planned rotation 的 overlap 必须由 profile policy 给出封闭起止 sequence。evidence update 不得改变 root/key generation，rotation 不得删除未授权 key，revocation 不得降低任一 generation；
7. response丢失时调用者必须以同一transaction/bundle调用`InspectBundleTransaction`。只有recovery authority能调用`RecoverBundleTransaction`，且只可依据journal与anchor receipt补齐已存在prepared或anchor-advanced transaction；request中的`originalExpectedProviderSequence`永远来自prepared记录，`observedCurrentProviderSequence`永远等于调用时anchor权威current且必须等于envelope `expectedProviderSequence`，两者不能混用。它不能stage leaf、改变manifest/authorization、选择另一个bundle、跳过CAS或降低sequence/generation。

三个 prepare operation 必须携带各自 authority 签名的 closed `BundleUpdateAuthorizationV1`，精确字段为：`schemaVersion/providerInstanceId/authorityProfile/updateKind/callerPrincipalDigest/transactionId/previousBundleDigest/bundleDigest/originalExpectedProviderSequence/anchoredNextProviderSequence/authorityGeneration/trustRootGeneration/issuedAt/expiresAt/signedObjectEnvelope`；`planned-rotation` 另要求 `overlapStartSequence/overlapEndSequence`，`security-revocation` 另要求 `revokedObjectDigests/reasonCode/effectiveSequence`，其他 kind 出现这些字段非法。envelope domain 按 kind 固定为 `marshal-evidence-update-authorization-v1\0|marshal-rotation-authorization-v1\0|marshal-revocation-authorization-v1\0`，key usage 分别固定为 `evidence-update-authorizer|rotation-authorizer|revocation-authorizer`。

四类 receipt 不共享一个可扩展 envelope，而各自是下列 exact closed object：

- `PreparedBundleReceiptV1`：`schemaVersion/providerInstanceId/authorityProfile/updateKind/producerPrincipalDigest/callerPrincipalDigest/transactionId/previousBundleDigest/bundleDigest/originalExpectedProviderSequence/anchoredNextProviderSequence/authorizationDigest/stagedLeafSetDigest/preparedAt/signedObjectEnvelope`；producer只能是APAP state service，domain=`marshal-bundle-prepared-receipt-v1\0`，key usage=`bundle-prepare`；
- `AnchorAdvanceReceiptV1`：`schemaVersion/providerInstanceId/authorityProfile/updateKind/producerPrincipalDigest/transactionId/previousBundleDigest/bundleDigest/originalExpectedProviderSequence/anchorObservedPreviousSequence/anchoredNextProviderSequence/authorizationDigest/preparedReceiptDigest/monotonicCounterIdentityDigest/previousAnchorReceiptDigest/advancedAt/signedObjectEnvelope`；producer只能是独立anchor service，domain=`marshal-anchor-advance-receipt-v1\0`，key usage=`anchor-advance`；
- `BundleCommitReceiptV1`：`schemaVersion/providerInstanceId/authorityProfile/updateKind/producerPrincipalDigest/callerPrincipalDigest/transactionId/previousBundleDigest/bundleDigest/originalExpectedProviderSequence/anchoredNextProviderSequence/authorizationDigest/preparedReceiptDigest/anchorReceiptDigest/currentPointerIdentityDigest/committedAt/signedObjectEnvelope`；producer只能是APAP state service，domain=`marshal-bundle-commit-receipt-v1\0`，key usage=`bundle-commit`；
- `BundleRecoveryReceiptV1`：`schemaVersion/providerInstanceId/authorityProfile/updateKind/producerPrincipalDigest/recoveryPrincipalDigest/transactionId/bundleDigest/originalExpectedProviderSequence/observedCurrentProviderSequence/anchoredNextProviderSequence/observedRecoveryState/recoveryAction/authorizationDigest/preparedReceiptDigest/anchorReceiptDigest/commitReceiptDigest/recoveredAt/signedObjectEnvelope`；producer只能是APAP state service，`observedRecoveryState=prepared-no-anchor|anchor-advanced-not-committed|committed`，`recoveryAction=retry-anchor-and-commit|commit-only|return-committed`，成功后anchor/commit digest都必须存在，domain=`marshal-bundle-recovery-receipt-v1\0`，key usage=`bundle-recovery`。

每个 receipt 对外引用的 `*ReceiptDigest` 都等于其 `signedObjectEnvelope.objectDigest`。verifier 必须从 operation 对应 principal 的 current、未撤销 keyset 验证 producer principal、key usage、key id/epoch、issued time 与 signature；未知/重复字段、错 domain/usage/epoch/producer 或 authorization/receipt chain 不等全部拒绝。

current pointer、journal 与普通文件同域，不能单独证明 rollback；支持 profile 必须具有 Marshal/Worker 不可回滚的外部 monotonic anchor。Linux 可使用 TPM NV counter/受保护系统服务 counter；Darwin 候选可使用 Secure Enclave/系统服务的不可回滚记录，但在真实 rollback conformance 前仍 unsupported。provider 与 anchor 同时回滚、counter 删除、同 sequence 异 digest、generation 降低或 committed leaf 缺失全部 fail closed。

更高 generation 即使 bundle 缺 leaf、损坏或 evidence 已撤销，也必须先消费 high-water 并保持 unavailable，不能回退旧 bundle。security-critical revoke 是新 bundle/anchor transaction：先使旧 evidence/config/key current 失效，再拒绝新 Probe/launch；planned rotation 允许 policy 明确的 bounded overlap，但旧 key 不能签发生效点之后的新对象。

### 8. Crash、恢复与对账

APAP journal 是 append-only、CRC/digest chained、单写者且由 provider principal 私有。snapshot、current symlink/filename、watch stream 与 cache 都是投影。

- crash 在 staging/prepared 前：没有 authority 变化，孤立 immutable leaf 可回收；
- `prepared-no-anchor`：anchor current仍为`originalExpectedProviderSequence`；recovery request的envelope `expectedProviderSequence`与payload `observedCurrentProviderSequence`都必须等于original expected，`anchoredNextProviderSequence=original+1`。验证prepared/authorization后只可用原transaction重试一次compare-and-advance，再补commit；
- `anchor-advanced-not-committed`：anchor current已经是`anchoredNextProviderSequence`；recovery request的envelope expected与payload observed current都必须等于anchored next，同时保留不同字段`originalExpectedProviderSequence`。只可验证既有anchor receipt并补journal/current pointer commit，绝不再次advance；
- `committed`：anchor current与envelope expected/payload observed current均为anchored next；相同transaction/bundle返回既有commit receipt及`recoveryAction=return-committed`，不写第二个事实；
- committed 后、response 前：同 `commandId+requestEnvelopeDigest` 在CAS之前先命中幂等记录并返回既有结果；不同commandId不得用original expected重试commit，必须先Inspect再按上述state recovery；
- current projection 丢失/落后：从 journal+anchor 重建；任何领先、分叉或协调回滚保持 unavailable；
- `PrepareLaunch` 后 response 丢失：consumer 必须 `InspectLaunch`；provider 通过 attempt+nonce+pid birth+launch request digest 找到唯一 pending，不能启动第二 child；
- `CommitLaunch` response 丢失：Inspect 返回 released 并携带同一 release receipt；重复 commit 不再次释放；
- provider restart 发现 pending child：在能证明完整 receipt、durable accept token 与未过 deadline前保持 stopped；否则 kill+wait 并记录 aborted，不猜测 release；
- `unknown` 不是可重试启动许可。consumer 返回 reconcile-required，保存非敏感诊断并等待 Inspect/人工处置。

所有有副作用 operation 都使用 request envelope 中非空的 `expectedProviderSequence` 做 CAS；只读 operation 必须为 `null`。只有 bundle commit 的 anchor advance 推进 provider sequence；session、staging、prepare 与 launch transaction 不推进 sequence，但仍在同一 provider serialization lock 下验证 CAS。普通请求的 expected 等于 current；recovery 请求的 expected 精确等于 `observedCurrentProviderSequence`，而 original/anchored next 仅作为被恢复 transaction 的不可变字段。bundle commit 的 anchor advance与 launch release分别是唯一线性化点，response envelope只报告该点之后的 `observedProviderSequence`。CAS mismatch固定返回 conflict且无副作用；同 commandId+digest重放在CAS前返回原receipt。并发prepare、rotation、revoke和launch release有唯一线性化顺序。revoke先提交则旧bundle不能release；release先提交则该child已按当时current authority获准，后到revoke只影响profile已冻结的后续行为。

### 9. 无 secret evidence 与可观察性

业务 JSON、bundle、receipt、observation、evidence、CapabilitySnapshot、ReviewPacket、doctor、事件和公开日志禁止包含：

- credential/private key/raw capability handle；
- credential bytes 或 credential-derived plain digest；
- 完整 environment、prompt、stdout/stderr、transcript、machine id、EK certificate 正文；
- authority/config/evidence/fence/socket 的绝对路径；
- raw errno/path error 或 provider 内部 cause。

允许字段只包括封闭 ID、generation/sequence、key id/epoch、public-key digest、profile/contract/policy/content digest、host identity digest、时间、固定枚举、字节计数与稳定 safe error。调试 cause 进入 provider 私有、访问受控且有期限的审计存储；其导出需再次 redaction。

doctor 只机械投影：provider status/instance/build/protocol、profile support reason、current bundle/evidence/config/fence/host digest、generation/sequence、validUntil、last reconcile state。watch 断开不把旧 Snapshot继续视为 current；每次 Probe/launch仍调用 `ReadCurrentBundle`。

### 10. Fail-closed 与错误模型

APAP response 使用封闭 `safeCode`：

- permanent：`platform-unsupported`、`profile-unsupported`、`principal-unauthorized`、`identity-mismatch`、`bundle-invalid`、`bundle-rollback`、`evidence-invalid`、`evidence-revoked`、`evidence-expired`、`host-attestation-invalid`、`isolation-unavailable`、`launch-receipt-invalid`、`secret-boundary-violation`；
- transient：仅 `provider-busy`、`anchor-temporarily-unavailable`；重试仍受 request deadline/预算并重新读取 current；
- reconcile-required：仅 `bundle-commit-ambiguous`、`launch-outcome-ambiguous`。

未知错误映射为 `internal-fail-closed` permanent；签名、rollback、revocation、identity、path/topology、secret、profile 和平台错误不得标成 transient。provider unavailable、watch lag、bundle不完整、peer credential不确定、fd identity无法证明、clock异常与 audit gap 均不产生 eligibility。

### 11. Conformance 与验收矩阵

共享 Port conformance 与 profile conformance 分层运行；两层都通过才可 enable。最小矩阵如下：

1. **principal/IPC**：错误 uid/pid/code identity、Worker直连、跨 operation token、packet截断/拼接/重复 member/未知字段/fd role、SCM_RIGHTS替换/关闭/复用、command replay/conflict、nonce/expiry/audience错误全部拒绝；envelope/payload重复identity、`requestEnvelopeDigest`与业务digest混用、response重复provider sequence、CAS空值/陈旧值也必须失败。
2. **key ownership**：Worker/Marshal/verifier可读任一 authority private key、角色复用 key、调用者自带 trust root/test key、revoked/错 usage/错 epoch key全部拒绝；进程 fd/env/filesystem 扫描证明 Worker authority key count=0。
3. **bundle/fence**：leaf缺失/额外/篡改/跨 profile、同代异 digest、更小代、旧 snapshot、counter跳跃/删除/回滚、prepared/anchor/commit每个 crash 点、projection丢失、provider+文件协调回滚、rotation overlap/revoke线性化全部覆盖；64-leaf bundle必须通过24-fd batch完成，25-fd packet、8 MiB aggregate越界、batch错序、批次间current变化、detached signature错key/usage/epoch/algorithm/domain均失败。update authorization 与 prepare/anchor/commit/recovery 任一 `SignedObjectEnvelopeV1` 缺失、digest排除规则错误、错producer/domain/usage/key epoch/signature也失败。
4. **probe isolation**：Secret provider必须以独立peer连接target child的session-scoped `CredentialIngressPort`；APAP control socket接受`AttachProbeCredential`、APAP/controller/verifier/Marshal/isolation parent收到endpoint handle、连接、credential fd或bytes全部失败。ticket错peer/child/endpoint/expiry/nonce、delivery/install receipt错session/profile/service/target/capability/audit/key epoch/signature、receipt ref替换也失败。credential只读、scratch唯一写域、business root对open/openat/rename/link/unlink/mount/chdir等等价路径拒绝、ambient fd/env/session/agent socket拒绝、network只到锁定service identity；audit由外部principal产生。预制transcript、self-signed receipt、同UID helper、普通subprocess、容器标签或`sandbox-exec`单独通过的fixture必须失败。
5. **profile exactness**：Qoder完整运行 ADR 0034 四变体、capability/manifest/receipt/evidence/fence矩阵；Codex完整运行 ADR 0037 contract、TPM/topology/sealed-fd/receipt矩阵。任一 profile证据不能供另一个profile使用。
6. **launch barrier**：child在receipt durable accept前执行任何user-space workload、重复release、错nonce/Attempt/authority namespace/bundle/evidence/config/fence、缺失或替换八个held handle任一项、ADR 0037 T1–T3 fixedRoots/mount namespace映射不等、把`apapLaunchRequestDigest`与`profileRequestDigest`要求相等或互相替代、shared binding receipt与profile receipt不等、revoke-before-release、receipt丢失后第二spawn、provider restart误释放、kill未wait全部失败。
7. **held identity**：父/leaf symlink、rename/swap/swap-back、alias、hardlink、FIFO/device/socket、owner/mode/link count、mount/namespace/volume generation变化与 pathname reopen全部失败。
8. **secret scan**：IPC capture、bundle/evidence/receipt、public error、doctor/Snapshot/ReviewPacket、provider journal export与crash dump的 canary credential扫描零命中；摘要字典攻击 fixture证明没有 credential-derived digest。
9. **平台**：Linux在每个声明机制缺失时稳定 unsupported；Darwin Qoder/Codex在各自替代合同未通过前稳定 unsupported。fake、test hook或用户确认不能改变结果。
10. **恢复/并发**：kill -9 provider/consumer/verifier/launcher、lost request/response、并发rotate/revoke/probe/launch、anchor lag、clock jump、disk-full/fsync error；未授权principal的evidence/rotation/revoke/recovery、recovery改manifest/leaf/authorization、混用original expected与observed current、anchor-advanced状态二次advance或绕过envelope CAS全部失败；最终只有一个current bundle和一个launch outcome，unknown保持reconcile-required。
11. **跨平台/质量**：Linux amd64/arm64与Darwin arm64 build/test/race/static analysis、Schema/JCS vectors、secret scan全绿；真实宿主probe由非作者验证，作者不能提供权威通过结论。

启用顺序不可压缩：ADR 接受 → shared Port/anchor/IPC fake与负向矩阵 → Linux外部principal与真实isolation/receipt实现 → profile-specific真实credentialed probe/evidence → 当前宿主doctor与撤销/rollback/kill演练 → 单独Adapter registry enablement。任何一步失败保持 hard-disabled。

## 与既有 ADR 的关系

- 本 ADR 补充 ADR 0034/0037 的“外部 provider 如何最小落地”，不放宽或取代其 profile-specific exact Schema、平台限制与启用顺序；冲突时更严格者优先。
- 本 ADR 不改变 ADR 0017/0018 的 Sandbox/Provider Port、authorityNamespaceId/securityDomainId、typed cross-domain edge 或 lifecycle；APAP receipt/evidence 仍由 Marshal consumer验证后映射为 AgentAdapter 本地eligibility，不直接写 Core authority ledger。
- APAP 不能宣布 ReviewDecision、safe-to-publish、Run终态或 Sandbox `hardened`。它只提供可验证 observation/receipt/bundle。
- 本 ADR 不改变 Publisher分权、Merge默认禁用、单worktree单写入者或“普通宿主进程不是恶意代码sandbox”。

## 后果

- Qoder/Codex可复用同一经过审计的OS principal、IPC、bundle/fence和launch barrier基座，减少两套不可互操作的守护进程与恢复实现。
- 共享层不会把两个Agent的证据语义合并；每个profile仍需独立真实probe与conformance。
- 部署成本上升：需要系统级provisioning、独立service users、不可导出key/monotonic anchor与平台特有内核能力。
- Linux可按机制逐项实现；Darwin在替代强制机制被新ADR接受并实证前保持unsupported。
- 提议本身不改变Issue #136/#137、M10–M13或任何当前registry状态。

## 备选方案（已否决）

- **在Marshal进程内签receipt/evidence**：否决。consumer可为自己造准入证据并读取key。
- **每个Adapter各写一套daemon**：否决。principal、anchor、bundle事务与barrier恢复会分叉；共享基础设施但保留封闭profile更可审计。
- **同UID helper+普通文件key**：否决。Worker可冒充authority、回滚state或读取key。
- **只签最终evidence JSON**：否决。无法证明实际isolation、held launch与child barrier，也无法安全恢复ambiguous launch。
- **bundle用多个current文件依次rename**：否决。config/keyset/revocation/evidence可形成撕裂current identity；必须单manifest+monotonic anchor+原子current transaction。
- **watch事件替代逐次recheck**：否决。丢事件或长寿命cache会绕过撤销。
- **Darwin用`sandbox-exec`或codesign摘要直接标supported**：否决。前者单独不构成外部强制audit，后者不解决pathname TOCTOU和post-exec barrier。
