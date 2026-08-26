# ADR 0051：Darwin 本地 Dogfood 执行 Profile 与受管发布权威分流

- 状态：提议（Proposed，2026-08-26）；仅冻结候选合同，未实现，不表示 Issue #212、I186-R3 或 Marshal v1.0 已关闭
- 关联：[ADR 0003](0003-separate-worker-and-publisher.md)、[ADR 0014](0014-read-only-execution-profile.md)、[ADR 0042](0042-mac-ordinary-user-adapter-mode.md)、[ADR 0047](0047-marshal-darwin-self-identity-and-release-signing.md)、[ADR 0048](0048-protected-build-input-and-artifact-attestation.md)、[Issue #191](https://github.com/chiga0/marshal-harness/issues/191)、[Issue #212](https://github.com/chiga0/marshal-harness/issues/212)

## 背景

ADR 0047 把 Darwin profile 封闭为 `darwin-adhoc-build`、`darwin-managed-development` 与 `darwin-notarized-release`，并要求任何改变生命周期或启动 Worker 的 profile 先获得外部签名、部署 policy、current/high-water 与安装收据。ADR 0048 又为 managed/release artifact 冻结受保护构建输入、签名与 authenticated build record。

这些要求适合受管部署和最终发布，但当前把两类不同风险绑成了一个前置：

1. 当前 Mac 上一个固定 Marshal 对象能否被宿主安全策略连续执行；
2. 该对象能否获得跨主体、不可回滚、可发布的 managed/release authority。

Issue #212 的真实阻塞首先属于第 1 类；R3-D/E/F 的本地开发与 `publication:none` dogfood 不需要第 2 类的发布权威。若强制先实现完整 protected build→sign→install→current/high-water 链，R3 会被发布基础设施反向阻塞；若直接绕过 ADR 0047，又会让同 UID 可替换的本地对象冒充 production authority。

因此需要增加一个显式低保证 profile，恢复 Local MVP 的可信单用户开发边界，同时把 managed/release 的强门禁完整保留到发布路径。

## 决策

### 0. 对既有 ADR 的取代范围

若本 ADR 被接受，则仅部分取代 ADR 0047 §1、§2、§6 中“所有 Darwin lifecycle profile 都必须先获得 external managed/release provision”以及“三个封闭 profile”的冲突条款，使 `darwin-local-dogfood` 可以按本文的低保证合同运行。ADR 0047 对 `darwin-managed-development`、`darwin-notarized-release`、installer/release、current/high-water、signer 分权、notarization、部署 policy 与禁止绕过宿主安全软件的其余条款全部保持有效；ADR 0048 不被取代。

本 ADR 不回写或改写 ADR 0047 的历史文本。接受状态、取代关系与当前权威解释只在 ADR 索引和路线文档中追加记录。

### 1. 增加第四个封闭 profile

ADR 0047 的 Darwin profile 集合由三个扩展为四个，新增：

| profile | 信任模型 | 允许用途 | 保证上限 |
| --- | --- | --- | --- |
| `darwin-local-dogfood` | 操作者信任当前本地用户、当前仓库与固定 Marshal 对象；同 UID 不是对抗边界 | 本地、loopback、`publication:none` 的开发与 dogfood | `ordinary-user`、`workspace-write`、`non-production` |

该 profile 不是 `darwin-managed-development` 的弱校验模式，也不是 managed/release 失败后的 fallback。只有显式 opt-in 才能启用；profile 缺失、漂移或无法确定时 fail closed。

ADR 0047 与 ADR 0048 对 `darwin-managed-development`、`darwin-notarized-release` 的签名、收据、current/high-water、受保护 producer chain、signer 分权、notarization 与部署 policy 要求保持不变。

### 2. 本地 activation 与对象身份门禁

显式 opt-in 必须由一个 operator-owned、closed/versioned、canonical JSON `LocalDogfoodActivationV1`（名称固定）表达，不能只靠环境变量或自由文本。最小字段为：

- `schemaVersion`、`activationId`、`issuedAt`、`validUntil`；
- `repositoryIdentity` 与 canonical repository root；
- `canonicalExecutablePath`、`expectedDevice`、`expectedInode`、`expectedSize`、`expectedRawSHA256`；
- `expectedSourceHead`、`expectedSelfProfile=darwin-local-dogfood`；
- `scope`：固定为 local/loopback、`publication=none`、ordinary-user Adapter，以及允许的 lifecycle command class；
- `activationDigest=sha256(JCS(activationWithoutActivationDigest))`。

实现必须冻结 activation 的最大 freshness，`validUntil` 不得由 Marshal 在运行中延长。文件 owner/mode、sequence 或本地存储位置只提供 same-UID trusted 模型内的完整性诊断，不得宣称 anti-rollback、external current 或 deployment authority。每次生命周期 mutation 和 Worker 启动都显式读取、canonical decode 并绑定同一 raw activation digest；缺失、过期、重复/未知字段、尾随 bytes、repository/profile/scope 不符全部拒绝。

`darwin-local-dogfood` 只能执行固定 canonical regular file，禁止 `$PATH` fallback、`go run`、自动下载、随机临时 Mach-O 或随机 helper。每次会改变本地生命周期或启动 Worker 前，至少绑定并比较：

- 当前进程报告的 executable path 与启动后以 `O_NOFOLLOW` 打开的 current-path object；
- canonical path、device、inode、size 与 raw SHA-256；
- build `sourceHead` 与 `selfProfile=darwin-local-dogfood`；
- `LocalDogfoodActivationV1.activationDigest`。

Darwin 普通进程不能仅凭启动后重新打开 pathname 证明该 fd 就是内核已加载的 vnode，因此本 profile 如实把它称为 **current-path object observation**，不称 held-executable attestation。实现必须在同一次有界读取中对 fd 做前后 `fstat`，拒绝 symlink、非 regular file、size/device/inode 漂移、短读/增长与 digest 不匹配；观察完成后在 mutation 前再次比较 pathname identity。该检查只在“same UID 与当前操作者可信”的 profile 威胁模型内成立，不产生对抗同 UID 替换的保证。

Core 在 Worker/Provider 启动前产生 immutable、content-addressed `LocalSelfIdentityObservationV1`（名称固定）。它至少绑定 `activationDigest`、当前 process identity、current-path object identity、raw digest、sourceHead、selfProfile、observation time 与 closed status/reason，并产生：

- `identitySubjectDigest`：只覆盖 activation 与必须跨 Run/Attempt 保持相等的 executable/repository/profile 字段；
- `observationDigest`：覆盖完整 observation，包括 process/时间与 `identitySubjectDigest`。

这两个 digest 是 Core-owned operator-local fact，不是 Worker、Adapter 或 binary build metadata 自报的 external authority。只检查 pathname、只相信编译时 `sourceHead/profile` 或只相信 activation 任一单项都不足以授权。对象被替换、路径重定向、digest/sourceHead/profile 不符、观察失败或 opt-in 缺失时一律拒绝。上述事实不得命名为 install receipt、artifact authority、deployment current 或 high-water。

固定 path/digest 也不保证 AMFI、Gatekeeper 或企业 EDR 会允许执行。因此，Issue #212 的最小 local-exec viability 仍必须证明：同一固定对象在当前宿主上连续运行 `version`、bootstrap self doctor 与一个无外部副作用的本地 lifecycle canary，且不需要为随机新路径逐次人工批准。若对象仍被 exit 137 终止，必须保留宿主归因证据并继续阻塞，不得循环重建或降级门禁。

### 3. 允许的本地 surface

在 identity gate、显式 opt-in 与既有 Core 生命周期门禁全部通过时，`darwin-local-dogfood` 只允许：

- trusted developer-owned repository 与 loopback/in-process embedded CLI；
- 本地 `.marshal`、锁定基线、独立 worktree、单写入者；
- `init/scaffold/plan/approve/run/verify/review` 及其只读 status/doctor/events；
- ADR 0042 的 ordinary-user Agent Adapter；
- Worker、独立 Verifier 与 Reviewer 分权；
- `publication:none` 时由 Core 收敛本地 `ACCEPTED`。

该 profile 不改变以下不变量：Worker 不能为自己的工作提供权威验证；ReviewDecision 必须绑定精确证据摘要；失败必须保存 Outcome；所有状态转换仍只由 Core/固定 Marshal CLI 执行；普通宿主子进程不得描述成恶意代码沙箱。

### 4. 禁止的 surface

`darwin-local-dogfood` 必须机械拒绝：

- `task publish`、Publisher、merge、push、Forge API、release、deploy 与 reconcile；
- `PublicationAuthorization`，以及 Core/Publisher/Forge/Cloud/Secret 等 typed credentialed external SideEffect；
- Marshal、Core、Publisher 或 Provider 发起的 Secret/Cloud credential、SSH agent、Keychain credential discovery 或注入；
- remote/non-loopback Public API、远程 Provider、Cloudflare execution 与 privileged Provider；
- APAP/strict production authority 与 hardened Sandbox 声明；
- 签发 production ConformanceEvidence、artifact attestation、install receipt、current/high-water 或 deployment policy observation；
- 将本地 artifact 补签、补 receipt 或修改 metadata 后晋升为 managed/release artifact。

doctor、Run、Evidence 与 Outcome 必须明示 `ordinary-user`、`workspace-write`、`non-production`；不得声明 `managed`、`notarized`、`production`、`enterprise-allowed`、`hardened` 或 Linux authority。

ordinary-user Qoder/Codex 使用操作者已经配置的自身模型登录，仅按 ADR 0042 的低保证推理通道处理；本 ADR 不授权 Marshal 发现、读取、复制或注入该凭据，也不把模型登录升级为 production credential authority。若 Agent 通过自身既有登录执行模型调用，其结果仍受本 profile 的 non-production evidence 适用性限制。

### 5. 跨 profile 污染隔离

`LocalDogfoodActivationV1.activationDigest` 与 Core-owned `identitySubjectDigest` 必须冻结在 Run Policy/environment binding，而不是写进业务意图型 TaskSpec。每次 Attempt dispatch 前，Core 生成新的 `LocalSelfIdentityObservationV1`，要求其 `identitySubjectDigest` 与 Run binding 相等，并把 `observationDigest` 冻结进 dispatch/evidence lineage。

WorkerResult 最多回显 dispatch-bound `identitySubjectDigest/observationDigest`；它不能生产、补齐、改变或升级 Marshal 自身身份。Core 必须用 current observation、Attempt dispatch 与 immutable evidence lineage重新验证该回显。Verification、ReviewDecision 与 Outcome 只引用 Core-owned digest，并对精确 Attempt/Run/Policy 适用性做检查；不得从 WorkerResult、Adapter transcript、doctor 自由文本或 build metadata 推导缺失身份。任何缺失、跨 profile replay 或 profile/digest 不一致都必须在 Attempt 或 acceptance 前拒绝。

本地 Run/Evidence/Outcome 只能满足同 profile、`publication:none` 的本地适用性，不能作为 managed/release admission、production conformance、publication 或 release exit evidence。managed/release artifact 必须从受保护 source material 重新构建并走 ADR 0048 producer chain；禁止把 local artifact 原地晋升。

若现有持久化对象无法无歧义表达该 lineage，实施前必须先用 versioned Schema/迁移补齐；不得把 profile 藏在自由文本、环境变量或仅 doctor 输出中。

### 6. Issue #212 与路线分流

Issue #212 拆成两个独立关闭条件：

1. **local execution viability（R3 pre-CLI）**：固定本地对象能连续执行，完成当前对象/路径/digest/sourceHead/profile/opt-in gate，并以无外部副作用 canary 证明 lifecycle 可达；
2. **managed/release authority（R6/release gate）**：完成 ADR 0047 的外部 certificate/allowlist/current/high-water、安装收据与 signer 分权，以及 ADR 0048 的 protected build/sign/install、authenticated build record、notarization/release 验证。

第 1 项通过后可以推进 R3-D/E/F 的本地实现和负向 fixture；第 2 项未通过时不得发布 v1.0 managed/release artifact，也不得声称 production authority。路线调整不是关闭或整体后移 #212，而是避免发布权威阻塞本地纵切。

### 7. 与 I186-R3 的关系

本 ADR 只解除本地 CLI/dogfood 的执行阻塞，不替代 Issue #191 的 Exit Gate：

- R3-D 仍须从真实 ResultIngress/execution 边界形成 typed failure 与 quarantine/decision；
- R3-E 的 location fact 仍须来自 Provider 故障域外的 Core/kernel-held PID、handle 或独立 attestation，不能由 ordinary-user Agent/Sandbox 自报；
- R3-F 中可放宽 retry/预算的资源与 infra-failure 分类仍须来自 workload/Provider 故障域外 observation；
- 本地 evidence 不支持 publication 或 production assurance。

### 8. 最小封闭 reason code

实现不得解析自由文本决定授权，至少提供：

- `self-local-opt-in-missing`；
- `self-local-object-mismatch`；
- `self-local-source-mismatch`；
- `self-local-profile-mismatch`；
- `self-local-publication-denied`；
- `self-local-credentialed-effect-denied`；
- `self-local-remote-surface-denied`；
- `self-local-cross-profile-evidence`；
- `self-local-exec-killed-before-diagnostic`。

unknown、观察超时、对象漂移、Schema 不可表达或 profile 不一致一律 fail closed。

## 必须通过的负向矩阵

实现至少覆盖：

1. pathname 相同但 inode/digest/sourceHead 改变；
2. symlink、rename、held object 与 pathname 对象不一致；
3. managed/release gate 失败后尝试自动降级 local；
4. local Evidence replay 到 managed/release 或 publication；
5. local artifact 补签或补 receipt 后尝试晋升；
6. local profile 请求 Publisher、Forge、Secret、Cloud、remote API 或 credentialed effect；
7. ordinary-user Adapter 伪造 hardened/location/infra-failure claim；
8. opt-in、profile lineage 或 identity observation 缺失/陈旧；
9. 固定对象被宿主安全策略终止；
10. crash/replay 后 profile 与 identity 适用性保持一致。

## 后果

正面后果：

- R3 的本地纵切不再等待完整企业签名与发布基础设施；
- 本地 dogfood 与 managed/release authority 在类型和证据适用性上分离，减少“为了可运行而放宽 production gate”的诱因；
- #212 的最小执行阻塞与最终发布门禁都有独立、可验证的退出条件。

代价与限制：

- 增加一个 profile、跨对象 lineage 与负向矩阵；
- 同 UID 恶意替换不在该 profile 的对抗边界内，因此它不能支持 production publication；
- 宿主若拒绝当前固定对象，架构分流本身不会使其自动可执行；
- 最终 v1.0 release 仍须完成 ADR 0047/0048 的 managed/release 链。

## 拒绝的替代方案

### 保持 ADR 0047 三 profile 并先完成全部 #212

拒绝。它把发布基础设施变成 R3 本地纵切的串行前置，扩大等待与 rework，但没有提高 R3 本地代码的验证强度。

### 直接允许 `darwin-adhoc-build` 改变生命周期

拒绝。隐式放宽既有 profile 会污染历史证据，且无法机械阻止 managed/release fallback 与 publication。

### 关闭 Gatekeeper/EDR、反复批准随机对象或使用随机 helper

拒绝。这既不稳定，也降低宿主安全；仍违反固定身份与 fail-closed 要求。

### 本地 artifact 后补签后晋升

拒绝。它绕过 ADR 0048 的 protected source/compile/external-material producer chain。

## 实施顺序

1. 接受本 ADR，并在 ADR 索引与路线文档追加部分取代关系；不改写 ADR 0047 历史文本；
2. 定义 profile/identity lineage 的 versioned Schema 与适用性检查；
3. 实现固定对象 self gate、explicit opt-in、doctor 投影和禁止 surface；
4. 运行 local-exec viability canary 与上述负向矩阵；
5. 恢复 R3-D/E/F 的单条真实 failure-edge 薄纵切；
6. 在 R6/release gate 完成 ADR 0047/0048 的 managed/release producer、签名、安装、current/high-water 与外部 provision。
