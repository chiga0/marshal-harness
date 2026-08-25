# ADR 0047：Marshal Darwin 自身执行身份、安装收据与发布签名

- 状态：提议（Proposed，2026-08-26；未经维护者接受，不实施、不启用生命周期门禁）
- 关联：[ADR 0003](0003-separate-worker-and-publisher.md)、[ADR 0007](0007-intent-first-publication.md)、[ADR 0014](0014-read-only-execution-profile.md)、[ADR 0038](0038-agent-production-authority-provider.md)、[ADR 0042](0042-mac-ordinary-user-adapter-mode.md)、[Issue #212](https://github.com/chiga0/marshal-harness/issues/212)

## 背景

Issue #212 记录的固定 Marshal Mach-O 只有 linker ad-hoc signature，`Identifier=a.out`、无 Team ID，且 `spctl --assess --type execute` 拒绝。该对象执行 `version --json` 与 `task scaffold` 时都以 exit 137 终止且无输出，已经阻断产品 CLI 生命周期。固定 pathname 只消除了随机临时 executable；它本身不构成稳定、可允许列表化的代码身份。

`spctl rejected` 能证明当前对象未被 macOS 分发策略接受。exit 137 能证明进程收到 `SIGKILL`，但不能单独证明信号由 Gatekeeper、企业 Endpoint Security/EDR 还是其它主体发出；精确归因仍须部署者按时间、PID、CDHash 或 audit event 查询宿主安全日志。反向地，Developer ID 签名或 notarization 也不自动代表企业 EDR 已允许该对象。两条判定必须分开记录、分别 fail closed。

当前构建、安装与发布流程没有稳定 binary identifier、受管 signing identity、notarization、安装收据或 CLI 自身身份门禁；当前 Keychain 也没有有效 code-signing identity。仓库代码无法凭空创建部署者或 Apple 信任的身份。因此，本 ADR 同时冻结仓库内合同和仓库外 provision 前置，但不把外部证书、私钥或企业策略变成仓库资产。

本 ADR 约束的是 **Marshal 自身 executable**，不是 Agent、Sandbox 或 APAP 的 production authority。一个已签名的 Marshal 仍不能把 ADR 0042 的 ordinary-user Qoder/Codex 子进程升级为 hardened sandbox 或 credentialed production authority。

## 决策（提案）

### 1. 仓库外前置条件

Darwin 上任何可改变 Run/Task/Attempt/Publication 状态或启动 Worker 的 Marshal profile，必须先由部署者完成以下 provision：

1. 提供一个持久、可验证的 code-signing 证书身份，并由 deployment/receipt policy 把可接受的 designated requirement 限定到 Marshal identifier；开发 profile 可使用企业/组织管理的开发证书，公开 release profile 必须使用有效的 Developer ID Application 身份；
2. 由企业安全管理员按稳定 binary identifier、签名 designated requirement、证书/Team ID 与实际产品策略建立 allowlist 或一次性批准；若策略仍拒绝，仓库实现不得把 notarization 冒充 allowlist；
3. 以宿主安全日志确认当前 exit 137 的实际执行者与拒绝依据，保留非敏感 event identity，避免把未归因的 `SIGKILL` 固化成错误策略；
4. provision 一个由 deployment authority 拥有、Marshal 运行用户不可回滚的 current receipt/high-water authority，并签发有 issuer、digest、freshness 与 sequence 的 deployment policy observation；
5. 分离 immutable artifact/release attestation signer 与 deployment/install receipt signer；两者可使用同一 HSM/signing service 基础设施，但必须是不同 producer principal、不同 signing identity 和不同 private key；
6. 为公开 release 提供受保护的 notarization 凭据与发布环境；开发证书不能替代 Developer ID/notarization。

在以上 identity/allowlist 缺失时，仓库能实现的只有确定性诊断与提前拒绝，不能令二进制自行获得宿主信任。仓库、安装脚本与 CI：

- 不自动生成自签名证书或 ad-hoc 身份；
- 不导入、导出、提交或打印私钥、证书口令、notarization credential；
- 不修改或关闭 Gatekeeper、SIP、企业 Endpoint Security/EDR；
- 不删除、清空或规避 `com.apple.provenance` 及其它 provenance；
- 不通过 `go run`、随机临时路径或反复改变 identity 的 helper executable 绕过拒绝。

### 2. Darwin profile

Marshal Darwin 构建只允许下列三个封闭 profile；profile 必须进入 build metadata、安装收据和 doctor 投影，不得静默推断或降级：

| profile | 用途 | 必须满足 | 允许的 CLI surface | 禁止声明 |
| --- | --- | --- | --- | --- |
| `darwin-adhoc-build` | 本地编译、单元测试与离线检查 | raw digest 与 sourceHead 可追踪；允许 ad-hoc/no Team ID | 仅纯进程内 `version`、`help`、bootstrap `doctor --self`；不能改变仓库或 `.marshal`，不能启动 Worker/Provider | installed、managed、notarized、production、enterprise-allowed |
| `darwin-managed-development` | 当前受管 Mac 上的开发/dogfood | 外部 provision 的持久签名证书；稳定 identifier；部署 policy 接受；精确安装收据；外部 current/high-water 与每次 current identity recheck | 本 ADR 接受并实现后，可在收据与 policy 均有效时运行本地生命周期及 ordinary-user Adapter | Developer ID、notarized、hardened Agent/Sandbox、可跨主机分发 |
| `darwin-notarized-release` | 面向最终用户或企业分发的 release | Developer ID Application、稳定 Team ID/identifier/designated requirement、hardened runtime、secure timestamp、Apple notarization、部署 policy 接受、release receipt、外部 current/high-water | 可在目标部署 policy 允许且收据有效时运行完整已实现 CLI surface | EDR 已自动 allowlist、Agent/Sandbox 已 hardened、未实现能力已 production-ready |

`darwin-managed-development` 没有 Team ID 时，只能由显式 deployment policy 以证书链/leaf digest 与 designated requirement 锚定，且必须投影 `teamIdentifier=null`；它不能被自动提升为 `darwin-notarized-release`。`darwin-adhoc-build` 即使某台主机偶然允许执行，也不能获得生命周期权限。

### 3. 稳定 executable 身份与信任锚

1. Marshal binary identifier 固定为 `com.github.chiga0.marshal`。改变 identifier 是身份迁移，不能作为普通 rebuild；需要新 ADR 或本 ADR 的显式替代、双身份迁移窗口和独立审查。
2. lifecycle authority 绑定的是 canonical fixed regular file，不是 `$PATH` 命中项或 convenience symlink。安装根由部署配置冻结；symlink 只可转发到收据绑定的 canonical object，不能成为 trust anchor。
3. 每次门禁至少同时验证：运行中 executable object、canonical path、device/inode、长度、raw SHA-256、Mach-O architecture、CDHash、identifier、Team ID 或显式 null、designated requirement、certificate chain/leaf digest、签名 kind、hardened runtime/secure timestamp 状态、build sourceHead/profile，以及部署 policy 结果。
4. `darwin-notarized-release` 还必须验证 notarization 状态；`darwin-managed-development` 必须明确验证 deployment policy 中的 managed anchor。只匹配 pathname、raw digest、CDHash、Team ID 或 receipt 中任意单项都不足以授权。
5. 对运行中对象的观察必须绑定当前进程/held object；先校验 pathname、再按 pathname 重新打开不是 TOCTOU 关闭。便捷命令输出与 receipt 声明都不能替代运行时重新观察。
6. CDHash 与 raw digest 会随重建变化，因此它们绑定具体 artifact；稳定 identifier、designated requirement 与受管证书锚定发行身份。升级不得为了稳定 allowlist 而省略 artifact 级绑定。

### 4. `MarshalInstallReceiptV1` 合同

安装收据是 versioned、closed、canonical JSON 对象；未知或重复成员、尾随 bytes、非 canonical 编码、digest/signature 不匹配全部拒绝。最小字段集合固定为：

- `schemaVersion`、`profile`、`deploymentId`、`receiptId`、`installGeneration`、`authoritySequence`、`issuedAt`；
- `artifact`：`repository`、`releaseTag|null`、`sourceHead`、`version`、`buildDate`、`goVersion`、`os=darwin`、`arch`、`rawSHA256`、`fileSize`；
- `artifactAttestation`：`producerPrincipalId`、`keyId`、`attestationDigest`，精确绑定 immutable artifact/release record；
- `codeSignature`：`signatureKind`、`identifier`、`teamIdentifier|null`、`cdHash`、`designatedRequirement`、`leafCertificateSHA256|null`、`certificateChainSHA256|null`、`hardenedRuntime`、`secureTimestamp`；
- `assessment`：`spctlStatus`、`notarizationStatus`、`assessedAt`，以及 closed `policyObservation`：`issuerPrincipalId`、`policySequence`、`policyDigest`、`observationDigest`、`status`、`issuedAt`、`validUntil`；
- `installation`：`canonicalPath`、`device`、`inode`、`fileMode`、`installedAt`；
- `publisher`：`buildWorkflowRef|null`、`signingWorkflowRef|null`、`releaseRecordDigest|null`，不得包含 credential；
- `previousReceiptDigest|null`、`receiptDigest` 与 `signedObjectEnvelope`。

`receiptDigest=sha256(JCS(receiptWithoutReceiptDigestAndSignedObjectEnvelope))`。`signedObjectEnvelope` 由 **deployment authority** 对固定 domain `marshal.install-receipt.v1` 与 `receiptDigest` 签名，并包含 `producerPrincipalId/keyId/keyEpoch/signatureAlgorithm/signatureEncoding/signature`；该 authority 必须实际观察最终 executable、policy observation 并控制对应 install transaction，不能只消费 installer 自报字段。它与 `artifactAttestation.producerPrincipalId/keyId` 必须都不同；artifact/release signer 不得签 install receipt，deployment signer 也不得重签、替换或发布 artifact。两类 signer 可共用 HSM/signing service 的基础设施，但不可共用 signing identity/private key。binary 自签收据、普通 Worker 签收据或只把 receipt 放在同 UID 可写目录都不产生 authority。

收据只是一项输入，不是“签过即可信”的 bearer grant。每次 lifecycle gate 都必须重新观察运行中 executable 和 current deployment policy，并逐字段与 current receipt 比较。receipt 文件缺失、损坏、回滚、指向旧 artifact 或与当前对象不一致时一律 fail closed；只读诊断可报告原因，但不得自动修复、重签或删除旧 provenance。

#### Current receipt/high-water authority

`darwin-managed-development` 与 `darwin-notarized-release` 必须配置外部 `MarshalInstallCurrentV1` authority。该 authority 由 deployment identity 唯一命名，存储/服务 principal 与 Marshal 运行用户、Worker、artifact/release signer 分离，且不能被这些主体删除或回滚。current fact 至少绑定 `deploymentId/currentReceiptDigest/installGeneration/receiptHighWater/policySequence/policyDigest/authoritySequence` 与 authority signature。

policy observation 只能由 deployment policy issuer 产生；其 `issuerPrincipalId/policySequence/policyDigest/observationDigest/status/issuedAt/validUntil` 必须由 current authority 精确接纳。`validUntil` 必须是由部署 policy 冻结的有界 freshness，不能由 installer 或 Marshal 延长；过期、sequence 落后、issuer/key 撤销、digest 不符或 status 非 `accepted` 都拒绝 lifecycle。宿主 EDR 不提供可查询 observation 时，只能由企业 deployment authority 签发对应 allowlist/policy observation；Marshal 不得把“进程此刻仍存活”推断为 current policy accepted。

current 推进固定为 compare-and-advance：请求绑定 `commandId/expectedAuthoritySequence/expectedCurrentReceiptDigest/nextReceiptDigest/nextInstallGeneration/nextPolicySequence/requestDigest`；deployment authority 必须先 durable prepare，再原子推进 current/high-water，最后提交 receipt。相同 `commandId+requestDigest` 的 lost-response replay 幂等返回同一结果；同 commandId 异 digest 固定 conflict。调用者遇到 timeout/crash/unknown 必须 `Inspect` 原 transaction；recovery 只能补齐原 prepared transaction，不能选择另一 receipt。看到 high-water 已推进但 receipt commit 响应丢失时返回原 commit；看到 current 未推进时可安全 abort；状态无法证明时保持 pending/unsupported，禁止猜测、重签或回退。

每次 `SelfIdentityDecision` 都必须读取并绑定同一 deployment identity 的 current fact、current receipt、receipt high-water 与仍新鲜的 policy observation；本地 receipt 的 generation/sequence 低于 high-water，或 current authority 不可用/不一致时 fail closed。没有该外部不可回滚锚点时，managed-development/release lifecycle 一律 `unsupported`，即使签名、notarization、pathname、digest 与本地 receipt 全部匹配也不例外。

### 5. 安装、升级与回滚

1. unsigned/ad-hoc build job 只产出待签 artifact、build metadata 与 raw digest，不能持有 signing/notarization/release credential，也不能发布最终 Darwin asset。
2. 签名与 assessment 在固定、部署者管理的 staging root 完成。staging artifact 在成为收据绑定的最终对象前不得作为 Marshal/helper 执行；不创建、复制或执行随机 `/tmp` Mach-O。
3. installer 在写 current 前依次验证 checksum、build metadata、签名、profile、notarization（release profile）、deployment policy 与 receipt signature；任一失败不替换 current。
4. current binary、deployment-signed receipt 与外部 `MarshalInstallCurrentV1` 作为一个可 reconcile 的 install transaction 提交；本地 generation directory/current pointer 只是物料投影，不能替代外部 current/high-water authority。任何 crash-visible 半完成状态都按上一节 `Inspect`/recovery 处置为原 transaction 的 `pending|committed|aborted|unknown`，不能把新 binary 配旧 receipt。
5. convenience symlink 只在 current transaction 完成后更新；最终运行路径解析到收据中的 canonical regular file。固定 identity 不要求每次升级保持相同 CDHash，但要求新 generation 有新 receipt、同一稳定 identifier/trust anchor 与显式 lineage。
6. rollback 是新的 compare-and-advance install transaction：只能选择仍满足当前签名、notarization、deployment policy 和 anti-downgrade policy 的既有 artifact，产生更高的 `installGeneration/authoritySequence/receiptHighWater`，并以 `previousReceiptDigest` 连接当前收据。禁止直接复制旧 binary、回写旧 receipt、降低任何 high-water 或删除当前/历史 provenance 来“恢复”。
7. 发布撤回、证书 revoke、allowlist revoke 或 current policy 变化必须使后续 lifecycle gate 失败；已启动的操作仍按既有生命周期、lease/fencing 与恢复合同处置，不能由安装器跨编排杀进程。

### 6. CLI 自身身份门禁

Darwin 上，CLI 在下列任一动作发生前必须完成一次 current `SelfIdentityDecision`：创建/修改仓库状态、写 `.marshal`、改变 Run/Task/Attempt/Publication 生命周期、启动 Worker/Verifier/Provider/Publisher，或调用能产生这些副作用的隐藏内部命令。决定绑定本次进程观察、current receipt digest、部署 policy observation 和调用分类。

唯一 bootstrap 诊断例外是：

- 纯进程内 `version`；
- 纯进程内 `help` 及等价 usage 输出；
- 纯进程内 self-identity doctor surface `doctor --self`（或协议等价的显式 bootstrap mode）。

`version`/`help` 只能读取编译时常量与当前进程内安全数据；bootstrap `doctor --self` 只能观察 Marshal 自身的运行中 executable、receipt/current/high-water/policy facts并计算 `SelfIdentityDecision`。三者均不得初始化 repository、Control Plane、Worker runtime、Adapter/Provider registry，不得执行 Probe/discovery，不得启动任何外部 executable，也不得写文件、网络访问或改变状态。receipt/current 无效时 `doctor --self` 必须显示 `selfIdentity.status=unsupported|blocked`、profile 与 reason code，不能返回“健康”。

完整 `doctor`（即使表面只读）会初始化 registry、执行 capability Probe/discovery 或观察外部 executable，因此必须先通过 `SelfIdentityDecision`；`doctor --repair` 还须沿用其 mutation 门禁。任何 scaffold/init、所有 `task`/`run` 生命周期命令以及 hidden/internal helper 都不因命名而自动豁免；需要副作用或外部进程的命令必须先通过门禁。

该门禁不能解决“进程在进入 `main` 前已被 EDR kill”的引导问题。因此 installer/release verifier 必须先做外部 assessment 与最终路径 canary；若 canary 被 exit 137 终止，安装事务不激活，报告 `self-exec-killed-before-diagnostic` 并要求部署者查看安全日志/allowlist，不能循环重建 binary。

### 7. 封闭 failure reason

实现必须使用封闭 reason code，不解析自由文本决定授权。最小集合为：

- `self-receipt-missing`、`self-receipt-invalid`、`self-receipt-signature-invalid`、`self-receipt-rollback`；
- `self-current-authority-missing`、`self-current-authority-conflict`、`self-current-authority-rollback`、`self-current-authority-unknown`；
- `self-policy-observation-stale`、`self-policy-observation-invalid`；
- `self-profile-adhoc-lifecycle-denied`、`self-profile-mismatch`；
- `self-path-mismatch`、`self-object-mismatch`、`self-digest-mismatch`、`self-build-mismatch`；
- `self-code-signature-invalid`、`self-identifier-mismatch`、`self-team-mismatch`、`self-requirement-mismatch`、`self-cdhash-mismatch`；
- `self-spctl-rejected`、`self-notarization-required`、`self-deployment-policy-rejected`、`self-deployment-policy-unknown`；
- installer-only `self-exec-killed-before-diagnostic`；
- `self-observation-failed`、`self-identity-unsupported-platform`。

reason 的 safe message 不得包含用户名、Keychain 内容、证书私钥路径、credential、企业规则正文或未筛选安全日志。unknown、observer timeout、工具输出超限、字段漂移或无法确定当前 policy 一律 fail closed。

### 8. Key custody 与 release authority

1. artifact/release authority 的 code-signing/release-attestation key material、deployment/install authority 的 receipt key 与 notarization credential 由部署者/发布基础设施持有，不进入仓库、Worker worktree、`.marshal`、普通 build job 或 Agent context。artifact/release principal 与 deployment/install principal 必须使用不同 identity/private key；可共用不可导出 Keychain/HSM/受管 signing service 基础设施。轮换与 revoke 由各自外部 authority 执行，不能由一方代替另一方。
2. CI 至少分离 unsigned build、protected artifact signing/notarization、immutable release attestation、deployment/install transaction 与 release publication 职责。PR job 与普通 Worker 不得获得任何上述 credential；artifact/release signer 不观察或控制目标安装事务，不能签 install receipt；只有观察最终对象并控制 compare-and-advance 的 deployment authority 能签 receipt。deployment authority 不能重签 artifact 或发布 release。
3. 最终 release record 必须绑定 tag、sourceHead、workflow identity、artifact raw digest、CDHash、Team ID、identifier、notarization result 与 artifact attestation digest；deployment receipt 另行绑定该 immutable attestation 与目标安装 observation。Publisher 不能重签未知 sourceHead，也不能用同 tag 覆盖既有 asset。
4. 本 ADR 不授权现有 CI 或维护者直接发布。具体 secret store、审批者、GitHub Environment、tag/release mutation 与撤回流程必须在实施前接受；若实现改变 ADR 0007/既有 publication authority，须以新 ADR 或本 ADR replacement 再冻结。

### 9. Linux 边界

本 ADR 的签名、Team ID、CDHash、designated requirement、`spctl` 与 notarization 合同只适用于 Darwin。Linux 构建必须投影显式非 Darwin profile，不得填充伪造的 Mac identity 字段，也不得因为没有 Mac receipt 而被自动标为不可信。

本提案不替 Linux 定义包签名、fs-verity、IMA、发行版 repository trust 或 production host authority。Linux 当前行为保持不变；未来若要求等价 self identity，必须按 Linux 的 trust anchor、安装与发布机制另立 ADR，不能复制 Darwin 字段冒充等价保证。

### 10. 非目标

- 不用 Marshal 自身签名替代 Agent/Sandbox/APAP authority、Worker 独立验证、lease/fencing 或 publication authorization；
- 不把 ordinary-user Adapter 改称 hardened 或 production authority；
- 不承诺 notarization 能通过任意企业 EDR，也不把 EDR allowlist 当作 Apple notarization；
- 不定义 Apple Developer Program 账号申请、证书采购或企业安全审批的自动化；
- 不允许运行时自动更新、自签更新、PATH 回退或网络下载后直接执行；
- 不删除 provenance、历史 receipt 或被拒绝证据；
- 不在本 ADR 中实现代码、Schema、installer 或 release workflow，也不宣称 Issue #212 已关闭。

## 实施与迁移顺序

1. **外部 provision（可与 ADR 审查并行，但不能由后续代码替代）**：部署者准备 managed-development certificate、彼此分离的 artifact/release 与 deployment/install signer、外部不可回滚 current/high-water authority、policy observation issuer 与企业 allowlist，并用宿主日志归因 exit 137；release owner 另行准备 Developer ID/notarization authority。
2. 维护者独立审查并接受或拒绝本 ADR。Proposed 状态不授权改变 trust boundary、持久化或 publication authority。
3. 冻结 `MarshalInstallReceiptV1`、`MarshalInstallCurrentV1`、policy observation、profile policy 与 build metadata；实现 held self-observation、纯进程内 bootstrap `doctor --self`、pre-mutation gate 和 hostile/ABA/path/signature/receipt/high-water negative matrix。此步保持 ad-hoc lifecycle fail closed。
4. 改造 build/install：固定 identifier、受管 staging、签名/assessment、install transaction、upgrade/rollback 与不执行随机 Mach-O 的测试；在收据和最终对象逐字段一致后才激活。
5. 以 `darwin-managed-development` 完成本机 canary：同一稳定安装连续运行纯进程内 `version`、bootstrap `doctor --self`、通过门禁后的完整 `doctor` 与 `task scaffold`，无需逐次人工批准；替换 binary/receipt/current high-water/path/policy observation 后必须 fail closed。完成独立 reviewer 之前不恢复被 #212 阻断的产品 Run。
6. 独立实现 protected Developer ID signing/notarization/release publication，完成 clean install、upgrade、rollback、revoke 与企业 canary；只有全部 release gate 通过才发布 `darwin-notarized-release`。
7. Issue #212 只在固定安装 identity 连续可用、receipt 漂移负测通过、真实 R3-D scaffold/plan preflight 成功且有独立审查证据后关闭。ADR 被接受、代码存在或 `spctl accepted` 任一单项都不等于关闭。

## 后果与门禁

- 该提案新增 Marshal 自身的 Darwin trust boundary、版本化安装收据和 lifecycle pre-mutation gate，并改变 signing/release authority，因此在 ADR 接受前不得实现或启用。
- managed-development 能缩短当前 Mac dogfood 闭环，但安全声明严格受限于该主机的外部 certificate/allowlist；它不是公开生产 release。
- notarized-release 提供稳定 Apple 分发身份，但企业 policy 仍是独立门禁；两者都必须在 receipt 和 doctor 中可观察。
- 当外部证书/allowlist、分权 signer 或不可回滚 current/high-water authority 任一尚未 provision 时，Issue #212 保持 blocker；实现不得用 ad-hoc、随机 executable 或静默降级换取表面可用性。
