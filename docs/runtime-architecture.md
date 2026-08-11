# Runtime 架构（M7–M13 目标架构）

- 依据：[ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md)（已接受，2026-08-10）；[ADR 0017](adr/0017-provider-neutral-sandbox-contract.md)（已接受，2026-08-10；接受只关闭设计歧义，不提前升级 M8 实现/conformance 状态）冻结 provider-neutral Sandbox 安全契约：二维权限/隔离模型、ConformanceEvidence 证据拓扑、内容寻址 Stage、workloadRole/principal 身份 fencing 与无双写 Restore、DispatchLease 唯一状态机、DurableExecutionEngine 权威边界与 M9 wire contract，并澄清/部分取代 ADR 0016 的 §4/§5/§6/§7/§9；[ADR 0018](adr/0018-control-plane-and-provider-ports.md)（已接受，2026-08-11；接受只冻结设计，不升级 M8–M13 实现/conformance 状态）冻结 Marshal C/S Control Plane、按信任域分隔的 Provider Port、耐久注册/能力快照与在途 lease 撤销，澄清/部分取代 ADR 0017 §4/§6/§7/§8/§10/§12，显式取代 ADR 0016 §6 经 ADR 0017 承接的 universal 接纳口径，并冻结权威/actor 双键空间（authorityNamespaceId=(tenantNamespace, controlPlaneId, authorityScopeId) 拥有全部 Control Plane 权威对象——submission/Task/Run/Attempt/ledger/DispatchLease/Allocation/ReviewDecision/Evidence graph/Outcome/SideEffectIntent/Receipt reconcile/typed edge/idempotency/outbox/audit/SSE；controlPlaneId 是 HA/灾备中保持稳定的逻辑权威身份，不是进程实例；ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence 也是 authority ledger 事实，仅携带 actor securityDomainId/provenance/eligibility；Artifact/Checkpoint/Candidate/Evidence bytes 的接纳关系归 authority ledger；securityDomainId=(tenantNamespace, trustDomainKind, isolationDomainId) 只标识 Provider actor）、三条 Core 独占签发的 Core-only typed cross-domain edge（DispatchResultCapability/MaterialAccessGrant/PublicationAuthorization，默认拒绝；issuer/source/target（每条 edge 的 issuer 为 Core，issuer 不等于业务流的 sourceActor；sourceActor/targetActor/targetAudience 按 edge 类型绑定）/operation/expiry/digest/revocation/replay/current-ledger recheck 与各自专属绑定，派生 token/handle 不得成为第二权威）、Provider attestation 全链绑定、远程 transport 安全基线（首次 enable 即强制 TLS）、原子 fencing 写入汇、SSE 恢复与再授权、DurableExecutionEngine 单一权威 seam、按 Port 的 versioned protocol family、Push/Pull outcome/invariant equivalence 与失效处置分级（security-critical 立即处置；planned upgrade stop-new + bounded drain）（ADR 0018 §3/§10–§16）
- 补充依据：[ADR 0019](adr/0019-deterministic-control-plane-typed-execution-and-goal-admission.md)（已接受，2026-08-11）冻结确定性 Supervisor、Typed Execution 的非通用协议边界、Goal 计划接纳、Evidence 依赖适用性与 append-only 补偿语义。
- 定位：本文是 M7–M13 的冻结目标架构。Local MVP（Milestone 0–6，`USABLE`）行为不变；本文新增对象与契约随 M8–M13 逐步落地，落地前不构成实现承诺。
- 术语约定：中文叙述，协议字段、状态名、CLI 命令与代码标识保留英文，且与 `docs/task-lifecycle.md`、`schemas/` 保持一致。

## 目标重述

Marshal 的长期目标是：**长寿命 Runtime/Control Plane 持续接收、耐久排队、分发和审计大量有界 Task/Run/Attempt；环境与状态可重建、可恢复、可审计**。

- 不是让单个 Task 运行数月：长周期目标由 Project/Goal 驱动一系列有界短 Attempt；M7 只冻结 Project/Goal 的存在性、权威归属与多 Run 原则，完整计划接纳与控制器语义由 ADR 0019 冻结并在 M13 实现；
- Cloudflare Sandbox 仅作为首个可替换远程 Provider，不是 Core 必选依赖；
- 本地 CLI 单次编排保留为首个入口形态与开发模式。

## 组件分层

```mermaid
flowchart TB
    Lead["人 / API / Lead Agent"] --> Submit["Task / Goal proposal"]
    Submit --> CP["Marshal Control Plane：生命周期守卫 / 证据权威 / 发布裁决"]
    CP --> DO["DurableExecutionEngine Port（可替换）"]
    DO --> Orch["DurableExecutionEngine backend（生产参考：Temporal）：at-least-once delivery / timer / signal / crash recovery"]
    CP --> Disp["Dispatcher：claim + fencing（DispatchLease）"]
    Disp --> SP["SandboxProvider Port：Local / Docker / Kubernetes / Cloudflare"]
    SP --> SB1["Ephemeral Sandbox：Worker Attempt（经 AgentAdapter）"]
    SP --> SB2["独立 Verifier Sandbox"]
    CP --> Ledger[("事件账本与状态快照（append-only）")]
    CP --> Store[("对象存储：candidate / evidence / publication 分域写入")]
    Pub["Publisher：分权、draft-only"] --> Forge["GitHub / GitLab"]
    CP --> Pub
    CP --> Sem["Typed semantic work：Plan / Review proposal"]
    Sem --> CP
```

职责划分：

- **Marshal Control Plane**：生命周期转换守卫、冻结语义、证据权威、发布裁决、副作用意图账本。这是唯一业务权威；Core lifecycle policy/controller 独占 Attempt 创建、retry eligibility/预算、rework 与 terminal decision；
- **DurableExecutionEngine Port backend**（生产参考 Temporal，单机/embedded 为 Local Engine）：只承担相同 `commandId` 的 at-least-once delivery、timer wakeup、signal transport 与 crash recovery；其 delivery/activity retry 不创建新 Attempt、不消费业务重试预算；不持有任何业务语义（ADR 0017 §9）；command 出站经单一权威 seam（同事务 outbox 或 ledger-derived Core command journal 二选一），`commandId` 从 ledger 权威事实稳定派生，workflow/activity state 不是业务权威（ADR 0018 §15）；
- **SandboxProvider**：执行环境全生命周期；业务 workload（Worker 与 Verifier）各自在独立 sandbox 中执行；conformance probe 是例外，按 ADR 0017 §2 运行在被测 Provider 的 target allocation 内；
- **Queue**：权威状态的只读投影，用于观测与调度提示，不是第二个业务权威；
- **Provider 信任域**（ADR 0018 §2）：Agent/Sandbox/Verification workload executor 属低权限 Execution 信任域（trust domain）；SCM/Publisher transport 属独立高权限 Publication 信任域（trust domain），Publisher 永不成为 Sandbox workload；Artifact/Secret 属 Data/Capability 信任域（trust domain）；域之间不共享 credential、AuthZ、审计或 conformance profile；三类域由 securityDomainId 复合三元组的 `trustDomainKind`（execution|publication|data-capability）机械标识；Provider actor 跨 trust domain 访问默认拒绝（default deny），唯一 allowlist 例外是三条 Core-only typed edge，未经对应 active edge 授权或绑定不精确匹配的跨 trustDomainKind 引用一律 fail closed；securityDomainId 只标识 Provider actor，Control Plane 权威对象归属 `authorityNamespaceId`（ADR 0018 §10）。

### Typed execution 与中间通信

Plan、Implement、Verify、Review、Publish 是调度层的 typed work 分类，不是一个 universal Provider protocol。它们可以复用 command/lease/heartbeat/cancel/deadline/event/log/artifact/checkpoint 基座，但每个 Port 仍有独立 Schema、principal、audience、credential、接纳规则与 conformance。现有 Sandbox `workloadRole` 不扩张，仍为 `worker|verifier`。

Executor 的 phase、heartbeat、progress、log 与 checkpoint 用于观测、取消和恢复，不宣布业务完成。Core 只依据精确绑定的 Candidate、Evidence、Assessment、Receipt 与当前 Policy 物化权威转换。Provider 或 backend 报告 completed 不能直接产生 `ACCEPTED`、ReviewDecision 或 safe-to-publish。

## 核心对象与身份层次

| 对象 | 语义 | 关键约束 |
| --- | --- | --- |
| `Project`/`Goal` | 长周期目标 | 驱动多个 Run；M7 只冻结存在性、权威归属与多 Run 原则；完整 revision/admission/budget/control 语义由 ADR 0019 与 M13 承接 |
| `TaskSubmission` | 提交入口记录 | 权威对象，由 `authorityNamespaceId` 拥有（ADR 0018 §10）；绑定唯一 `(authorityNamespaceId, repository, project)` scope（authorityNamespaceId=(tenantNamespace, controlPlaneId, authorityScopeId)，controlPlaneId 是 HA/灾备中保持稳定的逻辑权威身份、不是进程实例，单实例部署 controlPlaneId 可固定 `default`，ADR 0018 §10）；携带 `idempotencyKey` 与 `requestDigest`（冻结输入的规范化摘要）；仅 authorityNamespaceId+scope+key+digest 全部相同才幂等归并（返回既有 submission/run），同 key 而 digest 不同必须冲突 fail closed（见“提交入口与幂等边界”） |
| `Task`/`Run` | 冻结工作单元 | 冻结 spec/base/policy 与最低 `SandboxRequirements`（二维要求 `accessMode` + `minimumAssuranceLevel`，见“二维权限/隔离模型”）；修改冻结项创建新 Run |
| `Attempt` | 短命执行 | 有界、可丢弃；新 Attempt 不复用旧 ID |
| `DispatchLease` | 分发租约 | `generation`、`fencingToken`、`expiresAt`、`heartbeatAt`；过期或换代即失效；Push/Pull 共用唯一状态机，只冻结 outcome/invariant equivalence（ADR 0017 §7 经 ADR 0018 §16 修订）；双绑定权威侧 `authorityNamespaceId` 与 actor 侧 `securityDomainId`，绑定 `registrationId`/`providerCapabilitySnapshotDigest`/`conformanceEvidenceDigests` 与 attestation 链（providerInstanceId/configDigest/trust root），只消费持久快照，注册/快照/证据失效时在途 lease 立即失去资格（ADR 0018 §6/§10/§11） |
| `EnvironmentSpec` | 环境定义 | 以 digest 固定镜像/工具链/mount/network/resource/credential 要求 |
| `SandboxAllocation` | 分配记录 | 只保存 provider-neutral opaque locator 与 receipt |
| `CheckpointRecord`/`WorkspaceRef`/`ArtifactRef` | 状态与产物引用 | 一律内容摘要绑定；Checkpoint 可验证、可重放或明确拒绝 |
| `SideEffectIntent`/`SideEffectReceipt`/`ReconcileRecord` | Core 内部副作用权威记录 | append-only；不是 Provider wire Schema；各 Port receipt 经版本化 fail-closed mapper 规范化；M8 冻结 Schema 并启用首批 operation，M9 扩展覆盖面；当前仅有发布专用闭环与本地 cleanup tombstone |
| `ProviderRegistration` | Provider 耐久注册 | authority ledger 事实，由 authorityNamespaceId 拥有、只允许 Core 写入，仅携带 actor securityDomainId、provenance 与 eligibility；`registrationId` canonical 幂等绑定 `(securityDomainId, principal, providerType, providerName, providerVersion, protocolVersion, scope)`（securityDomainId 为所携带的 actor 身份）与 `idempotencyKey`/`requestDigest`；create/status/revoke/expire 是 authority ledger 事实；禁止 memory-only registration；全链绑定 providerInstanceId/configDigest/trust root（ADR 0018 §5/§10/§11） |
| `ProviderCapabilitySnapshot` | Provider 能力快照 | 不可变；capture/supersede 是 ledger 事实；DispatchLease 只消费持久快照；providerInstanceId、effective configDigest 或 trust root 任一变化产生新 immutable 快照/ConformanceEvidence 并触发 eligibility 重判（ADR 0018 §5/§6/§11） |

Run 与既有身份（`taskId`/`runId`/`attemptId`/`baseSha`/`specDigest`/`evidenceDigest`）的含义不变，见[任务生命周期](task-lifecycle.md)。

## AgentAdapter 层（冻结）

`AgentAdapter` 只负责 Agent 协议的三件事：

1. **prepare**：渲染冻结的 WorkerRequest 与 Prompt，构造标准化输入；
2. **decode**：解析 WorkerResult 与标准化事件，产出进程结果记录；
3. **capability**：Probe 二进制路径、版本、结构化输出、会话与权限能力，产出 legacy `v1alpha1` CapabilitySnapshot——它只是 AgentAdapter probe 快照，仅作为 ADR 0018 §5 fail-closed mapper 的来源快照存在，不直接作为 Runtime 注册产物。

Adapter 不含执行环境语义：不创建、不复用、不恢复执行环境。既有 Worker Adapter 契约（见 [Worker Adapter](worker-adapters.md)）不因 Runtime 化放宽。

## SandboxProvider 层（冻结）

### SPI 操作集

| 操作 | 语义 |
| --- | --- |
| `Probe` | 探测 Provider 可用性、版本与能力（含可强制的隔离维度） |
| `Provision` | 按 `EnvironmentSpecDigest` 创建执行环境 |
| `Stage` | 投放冻结输入：base checkout、worktree/workspace、control input；每个输入携带或引用真实 content-addressed bytes（inline 小对象或 ArtifactStore locator），Provider 消费前后重算 sha256，禁止只回显声明 digest（ADR 0017） |
| `Exec` | 在环境内启动受控进程（Worker/Verifier），捕获有界日志与退出状态 |
| `Inspect` | 观察环境内文件/进程真实状态，供 reconcile 与证据采集 |
| `Signal` | 传递取消、超时与干预信号，可终止进程树 |
| `Checkpoint` | 产出可验证 checkpoint（digest 绑定） |
| `Restore` | 依据 checkpoint 重建环境；默认 replacement allocation（旧进程树终止并失效后以控制面 CAS 激活新 generation），失败必须可判定，不允许双写（ADR 0017） |
| `Terminate` | 终止并回收环境 |
| `Reconcile` | 对账外部真实状态与账本记录，陈旧/漂移状态 fail closed |

### 绑定规则

- Run 只冻结最低 `SandboxRequirements`：镜像/工具链/mount/network/resource/credential 要求，以及二维要求 `accessMode` + `minimumAssuranceLevel`（见下节；取代 ADR 0016 的“最低 Execution Profile”措辞）；
- 实际 Provider 与具体能力在 Attempt 分配时绑定，记录于 `SandboxAllocation`（含生效的二维组合与 `conformanceEvidenceRef`）；
- 兼容 Provider 之间重试无需改变 Run；能力不足时 fail closed，不静默降级绕过门禁；
- Provider 失败不触发 Attempt 内回退：失败的 Allocation/Attempt 必须先终止并对账；此后调度器仅可为**新 Attempt** 选择满足同一冻结 `SandboxRequirements` 与 assurance 下限的兼容 Provider；
- 没有兼容 Provider 时 Run 保持 `BLOCKED`（fail closed），绝不静默降低 AccessMode/AssuranceLevel，也不复用旧 execution handle；
- Marshal Core 不得出现 Cloudflare 专有概念（Durable Object、R2、Workers binding 等）；Provider 细节只通过 opaque locator 与 receipt 暴露。

### 二维权限/隔离模型（ADR 0017 冻结）

执行契约拆分为两个正交维度，取代单一 executionProfile 的内部表示：

- `AccessMode`（权限维度）：`read-only`（语义承继 ADR 0014）或 `workspace-write`；
- `AssuranceLevel`（隔离维度）：`workspace-write`（工作流级控制）或 `hardened`（执行体级强制 mount/network/resource/credential 隔离）；
- 四种组合均合法，包括 `read-only × hardened`（不可信代码评审）；
- 旧 `executionProfile` 按固定映射表兼容解析：`read-only` → `read-only × workspace-write`；`workspace-write` → `workspace-write × workspace-write`；`hardened` → `workspace-write × hardened`；历史持久记录不重写；
- 拒绝/降级规则：AssuranceLevel 无法满足（无有效 ConformanceEvidence）时 fail closed，Run 保持 `BLOCKED`，绝不静默降级；降级只能是操作者显式创建新 Run 的决策并记录于 Outcome；Local 普通宿主进程 Provider 永不 `hardened`；AccessMode 在 Run 内不可升级。

### Conformance 与 hardened 声明（ConformanceEvidence）

- 同一 Sandbox Port 的所有 Provider 实现（Local、Docker、Kubernetes、Cloudflare 或第三方插件）必须通过该 Port protocol family 统一的 conformance 套件；Provider 自报的 Enforcement 不能获得 `hardened`；
- `hardened` 必须绑定密封 `ConformanceEvidence`：provider identity/version、suite/probe artifact digest、mount/network/resource/credential 逐维结果、`evidenceDigest`、有效期与撤销语义；四维全部 `passed` 才允许声明 `hardened`；
- **证据拓扑（ADR 0017 §2 冻结）**：probe 定义、challenge/nonce 与 probe artifact digest、调度、out-of-band 观察、裁决与 `ConformanceEvidence` 签发由 Control Plane 与独立 Conformance Verifier 控制；**probe workload 作为敌对测试负载运行在被测 Provider 创建、身份精确绑定的 target allocation 内**——只有这样才能测量被测 Provider 自身的 mount/network/resource/credential 强制能力；Control Plane 与 Conformance Verifier 经 out-of-band 观察与对账获取结果，不依赖被测 Provider 自报；被测 Provider 的 `completed`/receipt 只是裁决输入，不能自签通过；
- 该拓扑与业务独立验证（ADR 0004）不同：业务 Verifier workload 运行在独立于 Worker 的 Verifier sandbox，而 conformance probe 恰恰必须运行在被测 Provider 的 target allocation 内，两者不可混用；
- 证据过期或被撤销即失效，对应 Provider 回落到最高 `workspace-write` AssuranceLevel；
- Local 普通宿主进程 Provider 永不 hardened；Cloudflare 与第三方 Provider 一律通过相同证据准入，无豁免；
- 安全声明绑定生效的二维组合并记录在 Outcome；缺证据时 fail closed，见[安全模型](security-model.md)。

## 权威状态与外部调度边界

- Marshal versioned event/state 是业务权威：状态转换携带预期前置状态与 sequence，拒绝陈旧写入；
- 统一使用 `DurableExecutionEngine` Port 名（ADR 0017 §9；ADR 0016 §5 的 `DurableOrchestrator` 即本 Port，自 ADR 0017 起更名）。Temporal、Local Engine（embedded/单机 in-process）与任何未来实现都只是该 Port 的 backend；
- **业务语义只在 Core lifecycle policy/controller**：只有它创建新 Attempt、决定 retry eligibility 与消费业务重试预算、决定 rework 与 terminal decision；
- **backend 只承担传输保证**：相同 `commandId` 的 at-least-once delivery、timer wakeup、signal transport、crash recovery（恢复后回报事件账本与 Control Plane，不自行决定业务转换）；backend 以 `expectedSequence`/CAS 回报，不构成第二个业务权威；
- **delivery/activity retry 不是业务 retry**：对同一 `commandId` 的投递重试（含 backend activity retry）不创建新 Attempt、不消费业务重试预算；文档中指向 backend 行为的“retry”一律指 delivery/activity retry；
- 每个 Activity 以 `commandId` + `expectedSequence` CAS 追加 Marshal 事件：backend 重试或重放不产生第二条业务事实，避免双权威；
- `DurableExecutionEngine` 是 Core 的内部 Port（ADR 0018 §4）：backend 不是 Provider 信任域成员，也不构成第二个业务权威；command 出站经单一权威 seam（同事务 outbox 或 ledger-derived Core command journal 二选一）：`commandId` 从 ledger 权威事实稳定派生，消除“ledger 已提交而 command 未投递”或反向的双写窗口（ADR 0018 §15）；
- 权威 ledger sink 使用同事务 atomic compare-and-append/transaction：ledger transition、当前 lease generation 与 Evidence/Artifact 引用同原子校验提交；Artifact/Evidence/Checkpoint/Candidate bytes 的接纳关系归 authority ledger：对象 key 使用 `authorityNamespaceId`+run+attempt+allocation+generation scoped 的 immutable key 与 digest-verified put-if-absent，actor securityDomainId 只作为 provenance 记录，已存在 key 永不覆盖；陈旧/冲突 bytes 只能进入 quarantine namespace，永不覆盖当前对象、永不进入当前 evidence graph（ADR 0018 §13）；
- 事件账本保持 append-only 审计语义与可移植格式，是唯一业务权威；账本作为 Control Plane 权威对象由 `authorityNamespaceId` 拥有、只允许 Core 写入（ADR 0018 §10）；快照（原子替换的索引）、queue、SSE 事件流与 Provider registry 都是账本的可重建投影（projection），可凭账本重建，不构成第二个权威（ADR 0018 §4）；
- 单机开发形态：Temporal dev server + SQLite/local blob adapter；生产参考形态：Temporal self-host + PostgreSQL + S3/MinIO；
- 不得演变成自研 workflow engine：替换 backend 必须通过同一生命周期一致性测试。

## 版本化 Provider Protocol、信任域与部署形态（ADR 0017 冻结，ADR 0018 修订）

Provider 注册、版本与信任域（ADR 0018 §2/§5）：

- Provider 接入必须先经认证注册：Runtime 持久化 `ProviderRegistration` 与不可变 `ProviderCapabilitySnapshot`；ProviderRegistration、ProviderCapabilitySnapshot 与 ConformanceEvidence 也是 authority ledger 事实，由 `authorityNamespaceId` 拥有、只允许 Core 写入，仅携带 actor `securityDomainId`、provenance 与 eligibility（ADR 0018 §5/§10）；至少冻结 `protocolVersion`、`providerType`、`providerName`、`providerVersion`、`capabilities`、`conformanceEvidenceRef`、`scope` 与撤销/失效语义；`registrationId` 的幂等身份 canonical 绑定 `(securityDomainId, principal, providerType, providerName, providerVersion, protocolVersion, scope)`（securityDomainId 为所携带的 actor 身份）与 `idempotencyKey`/`requestDigest`——仅 actor 域与全部字段相同才归并，同 key 而 digest 不同为 conflict fail closed；revoked/expired registration 不因普通 replay 复活；create/status/revoke/expire 与 snapshot capture/supersede 都是 authority ledger 事实，三类 expiry（registration/snapshot/evidence）互相独立；禁止 memory-only registration；
- attestation 全链绑定（ADR 0018 §11）：ProviderRegistration、ProviderCapabilitySnapshot、ConformanceEvidence 与 lease claim 全链绑定 `securityDomainId`、稳定 `providerInstanceId`、effective `configDigest` 与签发/验证 trust root（含 key id/rotation）；任一变化产生新 immutable 快照/证据并触发 eligibility 重判——相同软件版本换实例、换配置或换签发密钥不得复用 hardened evidence；同一 Run 的 Worker/Verifier 必须不同 principal 与不同 allocation；高保证策略可要求 provider/host/failure-domain diversity；
- Provider 至少分三个信任域（trust domain）：Agent/Sandbox/Verification workload executor 为低权限 Execution 信任域；SCM/Publisher transport 为独立高权限 Publication 信任域（trust domain），Publisher 永不成为 Sandbox workload；Artifact/Secret 为 Data/Capability 信任域（trust domain）；域之间不共享 credential、AuthZ、审计或 conformance profile；信任域边界由 `securityDomainId` 复合安全域键空间机械强制，trustDomainKind（execution|publication|data-capability）标识三类域；未经三条 Core-only typed edge 中对应 active edge 授权或绑定不精确匹配的跨域引用与跨 trustDomainKind 引用 fail closed，三条 typed edge 是默认拒绝规则的唯一 allowlist 例外（ADR 0018 §3/§10）；
- 六类 Provider（Agent、Sandbox、Verification workload executor、SCM/Publisher transport、Artifact、Secret）不必远程，但彼此是不同 Port、不同 protocol family，不共享 conformance suite；对每个具体 Port/protocol family（ADR 0018 §16），embedded/in-process、Push HTTP、Pull outbound runner 才是该族的 transport/topology adapter，运行该族统一的 conformance suite，Port 语义不随 transport 变化；
- 权威与 actor 分离（ADR 0018 §10/§3）：authorityNamespaceId=(tenantNamespace, controlPlaneId, authorityScopeId) 拥有全部 Control Plane 权威对象——Project/Goal、TaskSubmission、Task/Run/Attempt lifecycle、DispatchLease/Allocation、ReviewDecision、Outcome、SideEffectIntent/Receipt reconcile、Evidence graph、typed edge、事件账本、发布决定、idempotency/outbox/audit 记录与 SSE 权威序列——只允许 Core 写入；controlPlaneId 是 HA/灾备中保持稳定的逻辑权威身份，不是进程实例；ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence 也是 authority ledger 事实，仅携带 actor securityDomainId、provenance 与 eligibility；securityDomainId=(tenantNamespace, trustDomainKind, isolationDomainId) 只标识 Provider actor；authorityNamespaceId 不是 Provider 的 trustDomainKind 维度，Provider 不得写入或宣称权威对象；Provider actor 跨 trust domain 访问默认拒绝（default deny），唯一 allowlist 例外是 Core 独占签发的三条 typed cross-domain edge——DispatchResultCapability（Execution 信任域结果接纳）、MaterialAccessGrant（Data/Capability 信任域 scoped 访问）、PublicationAuthorization（Publication 信任域发布授权）——其余 Provider actor 跨域访问默认拒绝，每条 edge 是绑定 issuer/source/target（issuer 为 Core；sourceActor/targetActor/targetAudience 按 edge 类型绑定）、operation/expiry/digest/revocation/replay/current-ledger recheck 与各自专属绑定的 authority-scope-bound 权威记录，每次使用必须按当前 authority ledger 复核，派生 token/handle 不得成为第二权威，不承载 raw credential/raw secret handle，不替代 ConformanceEvidence；provider-registration/control 与 public-api 不持有三类业务 typed edge，经 transport identity、该 Port 的 AuthN/AuthZ、scope/protocol validation 与 registration protocol 校验，由 Core 将获准事实写入 authority ledger（ADR 0018 §3/§5）；securityDomainId 相同只是 actor provenance/partition 条件，不构成授权、不构成同域 bearer grant，同域请求仍须逐项匹配所请求 Port 的 principal、registrationId、providerInstanceId、scope、attempt/allocation、generation、operation 门禁；
- 未知 `protocolVersion` 或不兼容版本 fail closed：拒绝注册；不兼容与撤销分级处置（ADR 0018 §6）：security-critical revoke（credential compromise、protocol violation 等安全关键撤销）立即 cancel + generation bump + kill 在途 lease/workload，不留 drain 窗口；planned/ordinary incompatible upgrade 使用新 registration/新 snapshot，旧实例 stop-new（停止接收新 Attempt/新 lease）+ bounded drain，drain deadline 到期再 fence；事件机器可读原因码与审计记录分开；普通升级不得复活旧注册或改写旧 lease digest；
- legacy `v1alpha1` CapabilitySnapshot 仅是 AgentAdapter probe 快照：字节与 digest 保持不变，只能经显式版本化的 fail-closed mapper 转换为 Runtime 注册输入并记录 `sourceCapabilitySnapshotDigest`，不得默认补齐 scope/evidence；
- Provider registry 视图与快照索引是事件账本的可重建投影（projection）：registry 只读自 ledger，可凭 ledger 重建，不构成第二个权威（ADR 0018 §4）；
- Provider 观测到 Exec 完成只是生命周期守卫的输入，不得自行宣布 ReviewDecision 或 safe-to-publish；Verification Provider 只能执行独立验证 workload，不得决定 gate/ReviewDecision；gate、ReviewDecision 与发布权限判断只在 Marshal Control Plane；Provider 与 DurableExecutionEngine backend 只能提供输入/传输，不能宣布 approved、ReviewDecision 或 safe-to-publish（ADR 0018 §1）。

部署形态与 Public API：

- 生产终态采用 C/S：Control Plane 运行于常驻 `marshal-server` 进程，Execution Plane 与 Control Plane 分离、可远程；单二进制 embedded/local 模式长期保留，与 C/S 形态共享同一生命周期守卫与证据规则；
- CLI、Web Dashboard、GitHub App、CI 一律是 Public API client，经同一 TaskSubmission/Run Public API 接入，不得绕过 Public API 直接读写业务状态；Core 不退化成任意插件 HTTP Server，插件表面只有版本化 Provider Protocol；
- embedded CLI 经 in-process adapter 调同一 Public application Port，不直写 store；server client 经 HTTP transport；两种形态共用同一应用 Port，不允许第二条写路径（ADR 0017 §10）。

Wire Contract（M9 首版冻结，ADR 0017 §10）：

- **Public API** 采用 versioned HTTP/JSON 并提供 OpenAPI 定义；表面覆盖 Task create/get/cancel、Run approval/status、events/evidence 读取；wire contract 在 M9 首次冻结，而不是推迟到 M12 才定义；
- **事件基线采用 SSE**，支持 `eventId`/cursor 断线续传；SSE cursor 过期、gap 或被压缩时服务端返回可判定的 resync 指令（deterministic 起点 + snapshot digest），客户端据此整体重建订阅；SSE 是只读投影：cursor 身份为 `authorityNamespaceId`+scope+ledgerSequence（权威账本的权威侧身份），订阅方另绑定自身 `securityDomainId` 完成授权判定，scope 内 sequence 单调，at-least-once 交付 + eventId/sequence 客户端去重，服务端 heartbeat 与有界 backpressure，周期性 re-Authorization 与敏感变更即时 re-Authorization；SSE 不承载业务 ACK、lease heartbeat 或 command 下发；参数值（heartbeat 间隔、缓冲上限、resync 错误码）留在 M9 Schema/OpenAPI 冻结（ADR 0018 §14）；轮询作为 fallback；WebSocket/gRPC 推迟，不属于 M9–M12 承诺，引入须另行 ADR；
- **Provider remote transport 同样是 versioned HTTP/JSON**：Push 拓扑由 `marshal-server` 调用 Provider endpoint；Pull 拓扑由 Provider runner 以 outbound-only long polling 或 streaming 领取同一 DispatchLease，不得要求 inbound 监听；
- **远程 transport 安全基线（ADR 0018 §12）**：任何非 loopback/in-process transport 自首次 enable 起强制 TLS；workload-to-workload 优先 mTLS 或等价不可转移 workload identity；每次调用双向校验 server/provider 身份与 audience/scope；短期 credential 支持 rotation 与 revocation，具备 replay protection；M9/M10/M12 各远程能力首次 enable 时必须满足，M11 只扩展 HA/多节点/多用户授权策略，不补首次安全基线；
- **按 Port 的 versioned protocol family（ADR 0018 §16）**：每个具体 Port 拥有独立 audience、AuthZ scope、request/response schema、error 模型、幂等约定、撤销语义与 conformance profile；跨族只共享 transport 层、RFC 8785 JCS 与最小 base auth primitives，禁止跨 Port 复用 token、schema 或 operation；六类 Provider 分属不同 protocol family，不共享 family、audience、schema、profile、conformance suite、token 或 operation；embedded/Push/Pull 只是同一 Port protocol family 内的 adapter；
- **身份与授权分工**：M9 必须提供最小、scope-bound、可撤销的 Provider/workload 注册身份（即使入口仅 loopback/trusted boundary）；M11 只扩展生产远程入口的 HA、operator/API caller、多节点与多用户 AuthN/AuthZ 策略（transport 安全基线已按 ADR 0018 §12 自首次 enable 生效，不由 M11 补）；M12 基于 M9 冻结的 wire contract 与 OpenAPI 定义交付多语言 SDK、部署文档与多拓扑 conformance（见[实施计划](implementation-plan.md)）；
- **M9/M11/M12 分工（ADR 0018 §8）**：M9 冻结 marshal-server/Public API/Sandbox dispatch Push-Pull、远程注册、SSE 与 DurableExecutionEngine（含 §15 单一权威 seam 的 backend profile 声明与升级 fixture）；M11 只扩展 HA/多用户策略；M12 扩展其余 Provider 的 wire/SDK 与按族 conformance；HTTP/JSON + OpenAPI 首发，WebSocket/gRPC 推迟；六类 Provider 的注册产物统一为 ProviderRegistration + 不可变 ProviderCapabilitySnapshot，字段级 wire 细节在 M12 另行冻结（本层只冻结对象、身份、protocol family 边界与撤销语义）。

DispatchLease：Push/Pull 共用的唯一状态机（ADR 0017 §7，经 ADR 0018 §16 修订）：

- DispatchLease 是唯一冻结的分发状态机；**Push 与 Pull 只冻结 outcome/invariant equivalence**（Push：Control Plane 调 Provider endpoint 投递 offer；Pull：Provider runner outbound-only 领取）：唯一 claim、eligibility、fencing、deadline（ack/heartbeat/expiry）、无双活与晚到结果隔离；允许拓扑特定（topology-specific）的 offer/poll/claim/ack transition 与 timing；两拓扑 conformance 比较 normalized business trace 与业务不变量，不比较逐步 wire trace；不得给 Push 定义弱化不变量的简化协议；
- 每个 lease 只消费持久 `ProviderCapabilitySnapshot`：DispatchLease 双绑定权威侧 `authorityNamespaceId` 与 Provider actor 侧 `securityDomainId`，并绑定 `registrationId`、claim 时 active 的 status/version、`providerCapabilitySnapshotDigest` 与 `conformanceEvidenceDigests` 封闭集合、`taskId`/`runId`/`attemptId`/`allocationId`，以及 `generation`/`fencingToken`，并按 attestation 全链绑定 providerInstanceId/configDigest/trust root（ADR 0018 §6/§11）；lease 的引用与 digest 永不改写，只供审计回放；
- 每次 heartbeat、结果/副作用接纳与恢复 reconcile 都按当前 ledger 重新判定资格（eligible）；registration 被撤销（revoke）/过期（expire）/转为不兼容、snapshot 被 supersede/expire 或 evidence 被 revoke/expire 时，active lease（在途 lease）立即失去资格（不再 eligible）：lease cancel/expiry + generation bump/fencing，Allocation/Attempt 终止对账，晚到结果隔离为诊断材料；继续执行只能创建新 Attempt 并以新 lease 重新 match（ADR 0018 §6）；
- 两者都具备 offer/claim-or-delivery ack deadline、heartbeat、expiry、cancel、reconcile、generation bump 与 stale-result isolation；ack deadline 超时或响应丢失时 lease 失效并进入 reconcile，不得为同一 Run/Attempt 产生第二个 active allocation；
- Push 同样先 capability match（比对持久 ProviderCapabilitySnapshot 与 conformanceEvidenceDigests 封闭集合，不匹配不投递）；Pull 先匹配后领取，禁止匿名拉任务；
- 陈旧 generation/lease 的晚到结果一律隔离为诊断材料，不进入当前 Evidence/Review/Publication；
- M9 退出门禁必须包含 Push/Pull 两拓扑 outcome/invariant equivalence conformance 与故障注入口径（比较 normalized business trace 与业务不变量，不比较逐步 wire trace；同一用例两拓扑同状态机，ack 超时/响应丢失/heartbeat 中断/陈旧 replay 不产生双活 allocation）。

远程副作用与 Secret/Artifact 边界（按 Port 分流，ADR 0018 §3）：

- dispatch-bound Sandbox/Agent/Verification Port 绑定完整 lease 身份（task/run/attempt/allocation/workloadRole/generation/fencingToken/commandId）并经 `commandId`/`requestDigest`/receipt 对账；publication Port 绑定 SideEffectIntent、ReviewDecision 与 evidence digest；artifact/secret Port 绑定 scoped handle、content digest、scope 与 expiry，禁止伪造无关 lease；不得以 HTTP 方法的表面幂等替代业务 fencing，receipt 对账必须校验身份 + `expectedSequence`；
- Secret/Artifact Provider 只交付有界引用或 workload-scoped 短期能力；secret 明文不得写入 TaskSpec、事件、Prompt、日志或 WorkerResult，有效期以 Attempt 为界；
- `fencingToken` 是非凭据（non-credential）的 stale-write guard，不能替代 AuthN/AuthZ；credential 不进入业务 JSON、事件、日志或 digest。

Canonical JSON：

- 所有 digest、replay key、`requestDigest`、`evidenceDigest` 统一使用 RFC 8785 JCS 序列化，复用仓库既有 JCS 实现基线（Milestone 1 交付）；
- 解析 Provider 注册请求、Stage/StageReceipt、ConformanceEvidence、Public API 请求与一切 `v1alpha1` 协议对象时，重复 JSON member 一律拒绝，不得“后出现优先”或“先出现优先”。

## 提交入口与幂等边界

网络暴露与调用者授权：

- M8/M9 的提交入口默认只绑定 loopback 或受信任本地边界（本机进程与本机可信调用者），不开放远程入口；
- 任何非 loopback/in-process 提交入口自首次远程 enable 起必须满足 ADR 0018 §12 transport 安全基线（TLS 强制、双向身份校验、短期 credential rotation/revocation 与 replay protection）；生产远程入口另须具备调用者身份认证（区分人操作者与 API 调用者）、按 repository/project 的授权、提交与授权决策的审计记录，验收口径列入 [实施计划](implementation-plan.md) Milestone 11 退出门禁；M11 只扩展 HA/多用户策略，不补首次安全基线；
- TaskSubmission 是 authorityNamespaceId 拥有的权威对象（ADR 0018 §10）：必须绑定唯一的 `(authorityNamespaceId, repository, project)` scope，跨 scope 与跨 authorityNamespaceId 提交被拒绝并记录审计；多租户仍是评估项，后续引入 tenant 维度时 tenant 只能作为 authorityNamespaceId 的 `tenantNamespace` 组成参与授权判定，不得以自由文本绕过（ADR 0018 §10），不放宽授权要求；
- 操作者与 API 入口的身份属于 M11 身份分离的一部分，与 Worker/Verifier/Publisher 的 workload identity 并列，不得混用。

幂等身份：

- 幂等身份是 `(authorityNamespaceId, scope, idempotencyKey, requestDigest)`（submission 与 idempotency 权威记录均由 authorityNamespaceId 拥有，§10），其中 `requestDigest` 是该次提交冻结输入（spec/base/policy/最低环境要求）的规范化摘要；
- 相同 `authorityNamespaceId`+`scope` + 相同 `idempotencyKey` + 相同 `requestDigest`：返回既有 submission/run，不创建第二个 Run，不重复执行副作用；
- 相同 `authorityNamespaceId`+`scope` + 相同 `idempotencyKey`，但 `requestDigest` 不同：请求冲突，fail closed（返回 conflict，写入审计记录），不得创建新 Run，也不得归并进既有 Run；调用者必须改用不同 key 发起新提交；
- 重复提交不能修改冻结 Run：修改冻结项必须走新提交并产生新 Run。

## 恢复、Fencing 与 Checkpoint 语义

**可丢弃执行体原则**：Sandbox、Agent 与 Runtime 进程都可随时丢弃；权威事件、证据与副作用记录必须在其外部耐久保存。恢复结论只能凭持久事件账本得出。

恢复与 fencing 规则：

1. DispatchLease 过期或失联时，Runtime 启动 Inspect/Reconcile：比较事件账本、状态快照、lease 记录与执行体真实状态；
2. 只有持有当前 `generation`/`fencingToken` 的执行体可以追加事件；旧 execution handle 的写入必须被 fencing 拒绝并记录；
3. 权威写入接纳边界按 Port 分流（ADR 0018 §3）：dispatch-bound Port 的 Attempt 回报携带 `attemptId`、`generation`、`fencingToken`，并在权威写入边界以 `expectedSequence`/CAS 校验；publication/artifact/secret Port 按各自域绑定校验（SideEffectIntent/ReviewDecision/evidence digest；scoped handle/content digest/scope/expiry）；接纳与 ledger transition、当前 lease generation 在同一原子步骤内校验（atomic compare-and-append/transaction），Artifact/Evidence/Checkpoint/Candidate bytes 的接纳关系归 authority ledger，使用 `authorityNamespaceId`+run+attempt+allocation+generation scoped 的 immutable key 与 digest-verified put-if-absent（actor securityDomainId 只作为 provenance 记录）；携带陈旧 `generation`/`fencingToken` 到达的内容只能进入 quarantine namespace 隔离留存为诊断材料，不得进入当前 Attempt 的 Evidence、Review 或 Publication（ADR 0018 §13）；
4. 外部副作用的重试与晚到回报不绕过接纳边界，继续经 SideEffectIntent/Receipt + reconcile 对账实现幂等；
5. 恢复只选择合法生命周期转换；无法判定时保持非终态并 fail closed，交由新 Attempt 或显式操作者决策；
6. 验收口径：在 `RUNNING`/`VERIFYING` 期间 kill -9 Runtime 后，可在 60 秒内完成 Inspect/Reconcile；旧 execution handle 上报被 fencing 拒绝；携带陈旧 token 的 checkpoint/candidate/证据引用被隔离；无双写、无丢证据。

操作身份与重放边界（ADR 0017 §4 冻结，ADR 0018 §3 按 Port 分流）：

- **workloadRole 与认证 principal 拆分**：Sandbox `workloadRole` 是封闭枚举，只允许 `worker`/`verifier`（Marshal 交给 SandboxProvider 执行的全部 workload 种类），不可扩展；`control-plane`、`publisher`、operator、API caller 不是 workloadRole，而是不同语义 Port 上受 AuthZ 约束的认证 principal/actor；**Publisher 永不成为 Sandbox workload**（凭据与发布权限位于 Worker 信任边界之外，ADR 0003）；
- 身份按 Port 冻结，不设 universal envelope（ADR 0018 §3）：Provider 不得借通用 role 取得跨 Port 能力；public-api Port 禁止携带 `providerType` 并拒绝 `workloadRole`/`allocationId`/`generation`/`fencingToken`/DispatchLease；provider-registration/control Port 同样拒绝 workload lease 字段；只有 dispatch-bound Port 绑定完整 lease 身份；从某一 Port 到达的请求只能行使该 Port 声明的能力，跨 Port 能力请求一律 fail closed；
- dispatch-bound 的 Sandbox SPI 操作必须携带完整身份元组：`taskId`、`runId`、`attemptId`、`workloadRole`（仅 `worker`/`verifier`）、`allocationId`、`generation`、`fencingToken` 与操作自身的 `commandId`；replay key 由完整身份 + `commandId` 的 canonical JSON（RFC 8785 JCS）派生；
- 普通 replay（崩溃重试、at-least-once 投递）先过当前 DispatchLease fencing，再进入 `commandId` + `expectedSequence` CAS；陈旧 lease 的 replay 拒绝并隔离为诊断材料；持有完整身份但 lease 已过期的请求不原地重试，走终止 + 对账 + 新 Attempt；
- Restore 的 lost-response reconciliation 与普通 replay 分离：响应丢失时走独立 reconcile 路径（Inspect 执行体真实状态、比对账本、选择合法转换），不消费普通 replay 幂等记录，不重发同一 `generation` 的 Restore。

Restore 无双写语义（ADR 0017 冻结）：

- Restore 默认 **replacement allocation**：旧进程树终止并失效后，Control Plane 创建新一代 `SandboxAllocation`（新 `allocationId`、`generation` 单调递增），以单写者 CAS 激活；
- in-place Restore 仅在能确认旧进程树已终止或从未存在时允许；恢复后写路径只认当前 `generation` 下启动的进程；
- 任何时刻，同一 Run/Attempt 最多有一个持有当前 `generation` 的 Allocation 处于活跃状态；Restore 响应丢失、并发 Restore、恢复后陈旧 handle 写入的故障注入必须全部被拒绝并记录。

Checkpoint 语义：

- `CheckpointRecord` 是 Attempt 级加速手段，不是状态权威；必须 digest 绑定且可验证；
- 稳定环境不等于复用永生 sandbox：默认每 Attempt 独立 ephemeral sandbox；稳定性来自 `EnvironmentSpecDigest`、pinned image/toolchain、内容寻址 artifact/cache 与可验证 checkpoint；
- Warm reuse 仅限相同 `securityDomainId`（相同 tenant/repository/trust-domain），且必须有可证明的 sanitization；不满足时一律新建环境。

## 外部副作用、对账与补偿（ADR 0019）

权威历史不可 rollback。外部资源操作在 Core authority ledger 内规范化为 append-only 的 `SideEffectIntent → SideEffectReceipt → ReconcileRecord`；这些不是跨 Port wire Schema。各 Port 先按自己的 identity/AuthZ/fencing/operation 规则验证 receipt/observation，再经版本化 fail-closed mapper 写入并保留 sourcePort/sourceReceiptDigest/sourceProtocolVersion。`ambiguous` 必须先观察真实外部状态，再由 Core 依据当前 lifecycle 选择复用同一 command、安排 cleanup/compensation 或追加 blocker；不能假定任意状态都可直接进入 `BLOCKED`。

- `purpose=forward|cleanup|compensation`；补偿绑定被补偿 effect 与 receipt digest；
- `dispositionClass=ephemeral-cleanup|compensatable|irreversible`；
- 自动 cleanup/compensation 只处理 Policy 明确授权、target identity 精确可证的资源；远端对象默认保留；
- Core/authority ledger 接纳内部规范化 Receipt；Provider Receipt 本身不构成权威事实，也不能替代所属 Port 的接纳门禁；
- M8 先覆盖 Sandbox/Stage/本地 cleanup，M9 建立通用 authority-scoped ledger，M10 覆盖 Cloudflare 资源全生命周期，M11/M12 完成 HA owner 与按 Port conformance。

## Goal orchestration（M13）

```mermaid
flowchart LR
    Source["人 / Planner / API"] --> Proposal["GoalPlanProposal"]
    Proposal --> Admit["Core deterministic admission"]
    Admit -->|"reject + reason"| Audit[("authority ledger")]
    Admit -->|"atomic accept"| Plan["AcceptedGoalPlanRevision"]
    Plan --> DAG["有界 Goal DAG"]
    DAG --> Runs["既有 TaskSubmission / Run"]
    Runs --> Evidence["Candidate / Evidence / Outcome"]
    Evidence --> Replan["新 proposal / revision"]
    Replan --> Admit
```

Planner 无论由 LLM、人或代码实现，都只生成 proposal。Core 依次校验 Schema/revision CAS、scope、node/edge 完整性、allowlist、完整 effective graph 的 cycle/depth/fan-out、整个 Goal 的预算 availability/estimate 与可选 plan approval；全部通过后，在同一事务原子写 accepted plan revision、live reservation 与 materialization outbox。拒绝、stale approval、CAS conflict 或 outbox 失败不会产生 Run、副作用或 live reservation。

至少累计约束 `maxNodes`、`maxDepth`、`maxFanOut`、`maxConcurrentNodes`、`maxPlanRevisions`、`maxTotalRuns`、`maxTotalAttempts`、`maxWallTime`、`maxCompute`、`maxTokens`/成本与 `maxArtifactBytes`。预算采用 append-only `reserved → committed → settled|released|expired` 状态机，绑定 reservation/node/revision/command/idempotency identity；estimate 与 actual 差额、pause/abort/supersede/超时和 lost-response 都经 CAS/reconcile，重复 settle/release 或 `actual > reserved` 不得超卖。已完成/运行节点不可改义；pending 节点只能 append-only supersede；重规划不能改写冻结 Run。

Evidence bytes 保持不可变，其当前适用性由 `EvidenceDependencySet` 派生：subject/base/environment/Policy/Verifier capability/upstream Artifact/有效期中任一依赖变化，只使依赖它的 gate 与后继节点失去 eligibility，并以新事件记录，不做全局或原地失效。

Goal 可以进入 `PAUSED`，`pauseReason` 为 `awaiting-input|operator|policy|budget-approval`。`drain-active` 只停止新 dispatch，让 active lease 运行至 deadline；`cancel-active` 原子记录 cancel intent/`canceling` lease 后立即 generation bump 或撤销 eligibility，先 fence 再 Signal/kill，晚到输入 quarantine，然后 Inspect/Reconcile、执行合法 Run 转换并写 Outcome，最后释放资源；持久 `canceling/reconciling` + deadline 覆盖中间恢复。Goal pause 绝不直接改 Run state；resume 是带 expectedSequence 的权威事件，必须重新校验预算、Evidence、Provider eligibility 与 Policy。

## Cloudflare Sandbox：首个可替换远程 Provider 的边界

以下事实仅取自 Cloudflare 官方公开文档；未经官方记载的能力一律视为未验证。

| 事实 | 对 Marshal 的含义 |
| --- | --- |
| Sandbox 由 Worker + Durable Object + 独立 VM Container 组成 | Provider 内部结构不进入 Marshal Core，只经 `SandboxAllocation` opaque locator 表达 |
| 官方 Bridge 是自部署到用户 Cloudflare 账号的 HTTP/OpenAPI Worker，提供 Bearer 令牌认证与 create/running/exec SSE/file/persist/hydrate/destroy | M10 的 Bridge Go client 只映射到 SandboxProvider SPI；auth 凭据按凭证边界管理，不进入 TaskSpec/事件/日志 |
| 容器闲置、故障或重启时丢失文件、进程与 session；R2 backup 只作恢复优化 | 权威状态必须外置；Checkpoint/Restore 只能作为 Attempt 级加速 |
| Sandbox SDK/Bridge 为 Apache-2.0，但托管运行平台不可自部署，SDK 仍为 1.0 preview/Beta | 接入采用 live opt-in；能力探测失败或行为漂移时 fail closed：终止当前 Allocation/Attempt 并对账，新 Attempt 仅可分配满足同一冻结 `SandboxRequirements` 与 assurance 下限的兼容 Provider；无兼容 Provider 时 Run 保持 `BLOCKED`，不静默降级、不复用旧 handle |

约束：

- Cloudflare 是**可选**托管 Provider，可作为首个托管 hardened 候选；但 `hardened` 声明必须持有按 ADR 0017 §2 证据拓扑（probe 运行在被测 Provider 的 target allocation 内）独立签发的有效 `ConformanceEvidence`，证明 mount/network/resource/credential 要求均被强制；
- Local/Docker/Kubernetes 自托管 Provider 必须能通过同一 conformance/E2E；首个纵切切片验收后，仅替换为 CloudflareSandboxProvider 重跑同一套用例；
- 不把 Cloudflare（或 Temporal、任何单一基础设施）变成 Core 必选依赖。

## 纵向切片验收（M8–M10 统一口径，ADR 0017 修订）

1. **M8 embedded/local 纵切**：单二进制 embedded 模式 in-process 跑通幂等提交（loopback/受信任本地边界）→ 冻结 Run → durable `READY` → scheduler claim + fencing → Local SandboxProvider → AgentAdapter → checkpoint/log/evidence → 独立 Verifier sandbox（业务验证 workload）→ `REVIEW_PENDING`/`ACCEPTED`（暂不自动 publish）；SPI 同时实现二维要求、内容寻址 Stage（消费前后重算 sha256）、workloadRole/principal 身份 fencing 与 replacement allocation Restore；conformance probe 按 ADR 0017 §2 运行在被测 Provider 的 target allocation 内（与业务验证拓扑不同）；M8 实施顺序为硬门禁（ADR 0018 §7）：negative fixtures/event contract → ProviderRegistration/ProviderCapabilitySnapshot Schema → legacy mapper → durable embedded registration + ledger recovery → snapshot/evidence validation → 最后 enable DispatchLease match；
2. **M9 服务化**：同一套用例切换为 `marshal-server` + TaskSubmission/Run Public API 重跑（versioned HTTP/JSON + OpenAPI，SSE 断线续传，cursor 过期/gap/压缩返回可判定 resync），embedded 模式保持兼容；Push/Pull DispatchLease 按唯一状态机满足 capability matching、ack/heartbeat/deadline/generation/fencing 且两拓扑满足 outcome/invariant equivalence（比较 normalized business trace，不比较逐步 wire trace），绑定 ProviderRegistration（registrationId）、claim 时 active 的 providerCapabilitySnapshotDigest 与 conformanceEvidenceDigests；非 loopback/in-process transport 自首次 enable 起满足 TLS/双向身份/rotation/revocation/replay protection 基线（ADR 0018 §12）；lost-response、concurrent-write、old-generation overwrite、backend 升级 fixture（workflow versioning/build ID、Continue-As-New、payload 外置/上限、activity heartbeat/cancel/retry）与失效处置分级 fixture（security-critical revoke 立即 cancel + generation bump + kill；planned upgrade 旧实例 stop-new + bounded drain，deadline 到期再 fence）通过（ADR 0018 §6/§13/§15）；在 `RUNNING`/`VERIFYING` 期间 kill -9 Runtime：60 秒内 Inspect/Reconcile，旧 execution handle 被 fencing 拒绝，无双写、无丢证据；
3. **M10 Provider 替换**：仅替换为 CloudflareSandboxProvider 跑同一 conformance/E2E，用例不变；`hardened` 声明必须持有有效 ConformanceEvidence；远程 Provider transport 自首次 enable 起满足 ADR 0018 §12 transport 安全基线，不以 M11 补首次基线。

## 保留的 Local MVP 不变量

Worker 不自证；Run 冻结 spec/base/policy/最低环境要求；单 workspace/attempt 写入者；Worker/Verifier/Publisher 分权；ReviewDecision 精确绑定 evidence；失败保存 Outcome；副作用 intent-first + receipt + reconcile，补偿不回滚历史；能力不足 fail closed；普通宿主进程不宣称恶意代码隔离；Merge 默认禁用；`.marshal/` 不进入业务提交。

## 文档关系

- 本架构的决策冻结见 [ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md)；provider-neutral Sandbox 安全契约（二维权限/隔离、ConformanceEvidence 证据拓扑、内容寻址 Stage、workloadRole/principal 身份 fencing 与无双写 Restore、DispatchLease 唯一状态机、DurableExecutionEngine 权威边界、版本化 Provider Protocol 与部署形态、M9 wire contract）见 [ADR 0017](adr/0017-provider-neutral-sandbox-contract.md)（已接受，澄清/部分取代 ADR 0016 §4/§5/§6/§7/§9）；Marshal C/S Control Plane、按信任域分隔的 Provider Port、耐久 ProviderRegistration/ProviderCapabilitySnapshot 与在途 lease 撤销，以及权威/actor 双键空间（authorityNamespaceId 独占拥有权威对象，controlPlaneId 为 HA/灾备中保持稳定的逻辑权威身份而非进程实例，ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence 为 authority ledger 事实仅携带 actor securityDomainId/provenance/eligibility，Artifact/Checkpoint/Candidate/Evidence bytes 接纳关系归 authority ledger）、三条 Core-only typed cross-domain edge（issuer/source/target（issuer 固定为 Core 且不等于业务流 sourceActor，sourceActor/targetActor 按 edge 类型绑定）/operation/expiry/digest/revocation/replay/current-ledger recheck 与专属绑定，派生 token/handle 不得成为第二权威）、securityDomainId 复合安全域键空间、attestation 全链绑定、transport 安全基线、原子 fencing 写入汇、SSE 恢复与再授权、engine 单一权威 seam、按 Port protocol family、Push/Pull outcome/invariant equivalence 与失效处置分级 见 [ADR 0018](adr/0018-control-plane-and-provider-ports.md)（已接受，2026-08-11；澄清/部分取代 ADR 0017 §4/§6/§7/§8/§10/§12，显式取代 ADR 0016 §6 经 ADR 0017 承接的 universal 接纳口径）；M7–M13 路线（M7–M12 平台阶段与 M13 Goal 编排）见[实施计划](implementation-plan.md)；
- [ADR 0015](adr/0015-production-deployment.md) 已 Superseded before acceptance，不再作为实施依据；
- 能力差距与外部系统对照见[云端长程 Agent 能力审计](research/cloud-agent-readiness-2026.md)（研究文档，不构成实施承诺）。
