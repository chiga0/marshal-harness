# ADR 0018：Marshal C/S Control Plane 与按信任域分隔的 Provider Port（耐久注册/能力快照与在途 lease 撤销）

- 状态：已接受（Accepted；维护者 2026-08-11，以本任务全部 Gate 通过、独立 ReviewDecision accept 且无 P0/P1（含 Round 4 独立评审八项 P1、Round 5 复核四项残留——复合安全域、Port protocol family、Push/Pull 不变量等价、计划升级 bounded drain——与 Round 6 复核两项残留——Control Plane authority namespace 与 Provider actor 域分离、typed cross-domain edge——、Round 7 复核三项残留——双键空间残留清除、Core-only typed edge 生命周期细化、Public API 幂等/SSE/对象 key 修正为 authorityNamespaceId——与 Round 8 复核一项残留——typed edge 跨域例外与适用范围——、Round 9 复核两项残留——跨域 fail closed 表述精确化（删除会无条件拒绝 MaterialAccessGrant 等合法 typed edge 的宽泛表述）、非 edge Port 与同域不自动授权（provider-registration/control 经 transport identity/该 Port AuthN/AuthZ/registration protocol 由 Core 写 authority ledger；securityDomainId 相同只是 provenance/partition 条件，不构成授权）——全部关闭）为前提。**接受只冻结设计**：不得据此把 M8–M13 实现或 conformance 状态提前标为完成，M7–M13 实施状态一律以 [Roadmap 状态](../roadmap-status.md) 为准）
- 日期：2026-08-11
- 决策来源：维护者认可的主干是 C/S + Control Plane/Execution Plane 分离 + provider-neutral 语义 Port + versioned transport，不是任意插件 HTTP Server；ADR 0017 冻结的 provider-neutral 契约在 Control Plane 与 Provider Port 边界上仍留有可歧义点（信任域未分隔、注册无幂等身份与持久化约束、universal 接纳句无法区分低权限 Execution 与高权限 Publication、注册失效后在途 lease 无统一撤销语义、registry/SSE 与账本权威关系未冻结），本 ADR 逐项关闭
- 取代关系：本 ADR 逐节澄清/部分取代 [ADR 0017](0017-provider-neutral-sandbox-contract.md) 的 §4/§6/§7/§8/§10/§12，并显式取代 ADR 0016 §6 经 ADR 0017 承接的 universal 接纳口径（对照见 §9）；历史 ADR 全文保持原样不改写，遇口径冲突以 §9 对照表为准
- 关联：[ADR 0016](0016-durable-runtime-and-sandbox-provider.md)、[ADR 0017](0017-provider-neutral-sandbox-contract.md)；[Runtime 架构](../runtime-architecture.md)、[总体架构](../architecture.md)、[安全模型](../security-model.md)、[实施计划](../implementation-plan.md)、[Roadmap 状态](../roadmap-status.md)

## 背景

ADR 0016 冻结了长寿命 Runtime/Control Plane 目标与 M7–M13 路线；ADR 0017 冻结了 provider-neutral Sandbox 安全契约。但两者在 Marshal Control Plane 与 Provider Port 的边界上仍留有可歧义点：

1. 六类 Provider（Agent、Sandbox、Verification workload executor、SCM/Publisher transport、Artifact、Secret）共用“Provider Protocol”措辞，未区分低权限 workload execution 与高权限发布传输，credential、AuthZ、审计与 conformance profile 可能跨域混用；
2. ADR 0016 §6 的权威写入接纳口径经 ADR 0017 §4 承接为 universal 句（每个 Sandbox SPI 操作与远程副作用必须携带完整身份元组、远程请求统一额外绑定 providerType），无法表达 public-api 与注册/控制面应当反向拒绝 workload lease 字段的事实；
3. Provider 注册只说“认证注册并产生 CapabilitySnapshot”，没有幂等身份、持久化约束与撤销/失效语义；legacy `v1alpha1` CapabilitySnapshot 与 Runtime 注册的映射未冻结，可能产生 memory-only registration 或默认补齐 scope/evidence 的漂移；
4. registration/快照/证据失效后，在途 lease 的命运未冻结：是继续执行、原地续租、静默降级，还是终止对账；
5. registry、queue、SSE 与 append-only event ledger 的权威关系未冻结，cursor 压缩/过期/gap 后的恢复行为不可判定；DurableExecutionEngine 的 Port 归属（Core 内部还是外部 Provider）未明确。

Round 4 独立评审在初稿之上进一步暴露八项 P1，本版本一并关闭：

6. 远程注册与 Push/Pull 已随 M9 启用，但 TLS 与调用者身份只被写成 M11 远程入口门禁——远程 Provider credential/lease 可能先于传输身份上线；
7. registration/submission/lease/artifact/secret/cache/audit 缺乏机械的安全域键空间，域间 credential/AuthZ/audit/conformance 隔离不可证明；
8. attestation 只绑定 principal/name/version/scope——相同软件版本替换实例、配置或签发密钥后可继续复用 hardened evidence；
9. expectedSequence/CAS 只是抽象请求规则，ledger transition、当前 lease generation 与 Evidence/Artifact 引用未保证同一原子校验，旧 generation 可能先覆盖对象 key 再被 ledger 拒绝；
10. SSE 只有 eventId/cursor/resync，未冻结 scope/sequence、交付与去重、压缩恢复、背压与长连接重新授权；
11. DurableExecutionEngine backend 虽声明非权威，但 ledger 已提交而 command 未投递（或反向）的双写窗口未关闭，Temporal/Local Engine 仍可能形成第二调度权威；
12. 六类 Port 仍可被理解为共用同一 operation schema、audience 与 conformance，Publication/Secret 可能借通用 Provider 能力进入 Execution 语境；
13. Push capability match 与 Push/Pull 两拓扑绑定仍残留 legacy CapabilitySnapshot/ConformanceEvidence digest 口径，与本 ADR 的持久 ProviderCapabilitySnapshot 口径冲突。

Round 5 复核在 Round 4 版本之上进一步暴露四项残留，本版本一并关闭：

14. `securityDomainId` 仍是全系统单一标识（单租户固定 `default`），与其同时宣称的 Execution/Publication/Data-Capability 三信任域隔离冲突；
15. 六类 Provider 仍被写成同一语义 Port、共享同一 conformance 套件，与按 Port 的 versioned protocol family 冲突；
16. Push/Pull 被写成“只改变连接发起方、其余语义完全等价”，不允许拓扑特定的 transition/timing，conformance 比较口径不可执行；
17. 不兼容与撤销未分级：security-critical 撤销可能保留 drain 窗口，planned upgrade 可能被立即 kill、复活旧注册或改写旧 lease digest。

Round 6 复核在 Round 5 版本之上进一步暴露两项残留，本版本一并关闭：

18. `securityDomainId` 同时承载 Control Plane 权威对象归属与 Provider actor 隔离，权威侧与 actor 侧键空间混同：事件账本、lifecycle 状态、ReviewDecision 与发布决定缺乏独立于 Provider 信任域的权威侧命名空间身份；
19. 跨信任域请求（结果接纳、物料访问、发布授权）缺乏 typed edge，可能被实现为直接跨域传递 handle/credential 或隐式信任，默认拒绝不可机械证明。

本 ADR 逐项冻结（§1–§16）。本 ADR 不改变 ADR 0016/0017 已冻结的核心对象模型、提交入口幂等边界、二维权限/隔离模型、ConformanceEvidence 证据拓扑、内容寻址 Stage、无双写 Restore、JCS 规范化与不变量集合。

## 决策

### 1. C/S Control Plane 与 Execution Plane 分层冻结

生产终态采用 C/S：Marshal Control Plane 运行于常驻 `marshal-server` 进程，Execution Plane（Agent/Sandbox/Verification workload）与 Control Plane 分离、可远程部署。

- Core 是唯一业务权威，独占 Task/Run/Attempt lifecycle、retry/rework、evidence 裁决与 ReviewDecision、Worker/Verifier/Publisher 分权、发布权限判断、审计、幂等与 fencing；Control Plane 权威对象——事件账本、submission 记录、Task/Run/Attempt lifecycle 状态、DispatchLease/Allocation、ReviewDecision、Outcome、SideEffectIntent/Receipt reconcile、Evidence graph、typed edge、发布决定、idempotency/outbox/audit 记录与 SSE 权威序列——一律由 Core 在 `authorityNamespaceId`（§10）内拥有、只允许 Core 写入，Provider 不得拥有、写入或宣称权威；
- Provider 与 DurableExecutionEngine backend 只能提供输入与传输：可以报告 completed/receipt，但不能宣布 approved、不能签发或改写 ReviewDecision、不能宣布 safe-to-publish；Provider 不得自行宣布 ReviewDecision 或 safe-to-publish；gate、ReviewDecision 与发布权限判断只在 Marshal Control Plane；
- CLI、Web Dashboard、GitHub App、CI 一律是 Public API client，经同一 TaskSubmission/Run Public API 接入，不得绕过 Public API 直接读写业务状态；embedded/local 单二进制形态经同一 application Port 保留；
- Core 不退化成任意插件 HTTP Server：对外表面只有版本化 Provider Protocol 与 Public API，不向 Provider 暴露业务状态写 API。

### 2. Provider 按信任域（trust domain）分隔

六类 Provider 按最小权限至少划分三个信任域（trust domain）：

| 信任域 | Provider | 权限定位 |
| --- | --- | --- |
| Execution trust domain | Agent、Sandbox、Verification workload executor | 低权限 workload execution；workloadRole 封闭枚举仅 worker/verifier（ADR 0017 §4） |
| Publication trust domain | SCM/Publisher transport | 独立高权限；Publisher 永不成为 Sandbox workload（ADR 0003） |
| Data/Capability trust domain | Artifact、Secret | 只交付 scoped handle 与 workload-scoped 短期能力 |

- 域之间不共享 credential、AuthZ、审计或 conformance profile；Provider actor 跨域能力请求默认拒绝（default deny），唯一 allowlist 例外是三条 Core-only typed edge（§3）：未经对应 active edge 授权，或 source/target securityDomainId、edge type、对象、operation、Attempt/Allocation、generation、expiry/deadline、digest、当前 authority ledger 状态任一不精确匹配的请求一律 fail closed；
- 信任域边界不是文档声明，而是由只标识 Provider actor 的 `securityDomainId` 复合安全域键空间机械强制（§10）；Control Plane 权威对象归属 `authorityNamespaceId`，Provider 不得写入或宣称权威（§10）；协议表面按各自独立的 versioned protocol family 隔离（§16）；
- Provider 不必远程：对每个具体 Port/protocol family（§16），embedded/in-process、Push HTTP、Pull outbound runner 只是该 protocol family 内的 transport/topology adapter，运行该族统一的 conformance suite，Port 语义不随 transport 变化；六类 Provider 分属不同 Port 与不同 protocol family，不是同一语义 Port，也不共享 conformance suite；
- M9 冻结 marshal-server/Public API/Sandbox dispatch Push-Pull、远程注册、SSE 与 DurableExecutionEngine；M12 才扩展其余 Provider 的 wire/SDK（见 §8）。

### 3. 按 Port 分流的身份与接纳矩阵（不设 universal envelope）

身份按 Port 冻结，不设 universal envelope。所有 Port 共享的最小公共认证上下文只有：`requestId`、协议 version、AuthN `principal`、`audience`、`scope`、`deadline`；`traceContext` 只作观测，不参与授权。credential 不进入业务 JSON、事件、日志或 digest。`fencingToken` 是非凭据（non-credential）的 stale-write guard，不能替代 AuthN/AuthZ，也不构成独立的授权依据。

required/forbidden 矩阵覆盖六类 Port：

| Port | 必须绑定 | 禁止/拒绝 |
| --- | --- | --- |
| public-api | requestId/version、AuthN principal、audience、scope、deadline | 禁止携带 `providerType`；拒绝携带 `workloadRole`、`allocationId`、`generation`、`fencingToken` 或 DispatchLease 的请求，一律 fail closed |
| provider-registration/control | securityDomainId、principal、providerType/providerName/providerVersion/protocolVersion、scope，以及 §11 attestation 绑定（providerInstanceId/configDigest/trust root） | 拒绝携带 `workloadRole`、`allocationId`、`generation`、`fencingToken` 或 DispatchLease 的注册/控制请求；只处理注册、查询、撤销与失效 |
| dispatch-bound Sandbox/Agent/Verification | taskId/runId/attemptId/allocationId、workloadRole（仅 worker/verifier）、generation、fencingToken、commandId、sequence | 拒绝跨 Port 能力请求；只有本 Port 可绑定完整 lease 身份 |
| publication | SideEffectIntent、ReviewDecision 引用、evidence digest | 禁止携带与本次发布无关的 lease 身份；不得宣布 gate/ReviewDecision |
| artifact | scoped handle、content digest、scope、expiry | 禁止伪造无关 lease；secret 明文不入事件/日志 |
| secret | scoped handle、scope、expiry | 禁止伪造无关 lease；明文不入 TaskSpec/事件/Prompt/日志/WorkerResult |

- 只有 dispatch-bound Port 可以绑定完整 task/run/attempt/allocation/workload/generation/fencing/command/sequence 身份；ADR 0017 §4 中“远程请求另绑定 principal/portKind/providerType/audience/scope”的 universal 句由本表取代：providerType 属于注册/dispatch 语境，public-api 与 provider-registration/control 反向拒绝 workload lease 字段；
- 普通 replay 语义不变（ADR 0017 §4）：先过当前 DispatchLease fencing，再进入 `commandId` + `expectedSequence` CAS；陈旧 lease 的 replay 拒绝并隔离为诊断材料；持有完整身份但 lease 已过期的请求不原地重试，走终止 + 对账 + 新 Attempt；
- Port 请求与记录按矩阵规则绑定 actor 侧 `securityDomainId`：dispatch-bound Port 连同完整 lease 身份绑定 actor securityDomainId，provider-registration/control Port 绑定注册 actor 的 securityDomainId，其余 Port 只绑定各自 Port 矩阵列的身份字段；权威对象一律由 `authorityNamespaceId` 拥有，不随 actor 侧键空间承载（§10）；未经三条 typed edge 中对应 active edge 授权或绑定不精确匹配的跨 securityDomainId actor 侧引用一律 fail closed（三条 typed edge 是默认拒绝规则的唯一 allowlist 例外，§10）；每个 Port 运行于独立 versioned protocol family（§16），一切非 loopback/in-process transport 自首次 enable 起满足 §12 安全基线。
- Public API 幂等、SSE 与对象 key 统一使用 `authorityNamespaceId`：public-api 幂等提交身份为 `(authorityNamespaceId, scope, idempotencyKey, requestDigest)`（submission 记录是 authorityNamespaceId 拥有的权威对象，§10）；SSE cursor 身份为 authorityNamespaceId+scope+ledgerSequence（§4/§14）；Artifact/Evidence/Checkpoint/Candidate 对象 key 为 authorityNamespaceId scoped（§13）；`securityDomainId` 只作为 actor/provenance 组成参与这些路径的授权判定，不构成其幂等、cursor 或 key 的权威归属。
- provider-registration/control 与 public-api 一样不持有三类业务 typed edge：注册/控制请求必须通过 transport identity（§12 双向身份）、该 Port 独立的 AuthN/AuthZ、scope/protocol validation（§16）与 §5 registration protocol 校验，由 Core 判定接纳并把获准事实（registration create/status/revoke/expire 等 authority ledger 事实）写入 authority ledger；transport identity 与 Port AuthN/AuthZ 不构成跨信任域业务能力，也不得派生为 typed edge 等价物。securityDomainId 相同（tenantNamespace、trustDomainKind、isolationDomainId 三元组全部一致）只是 actor provenance/partition 条件，不构成授权、不构成同域 bearer grant：同 securityDomainId 的请求仍须逐项匹配所请求 Port 的 principal、registrationId、providerInstanceId、scope、attempt/allocation、generation、operation 门禁，任一不匹配一律 fail closed 并写入审计（§5/§10）。

Provider actor 跨 trust domain 访问默认拒绝（default deny）；三条由 Core 独占签发的 Core-only typed edge 是该默认拒绝规则的唯一 allowlist 例外——除此之外 Provider actor 跨 trust domain 访问不存在其他通道，typed edge 也不是对跨域 raw handle、raw credential 或任意引用的豁免；未经对应 typed edge 授权的 Provider actor 跨域访问一律 fail closed 并写入审计。typed edge 的签发、撤销（revocation）、续期与重新授权一律 Core 独占并接受重放审计，Provider 之间不得互相签发、转授或延展 edge；三条 typed edge 的 issuer（签发者）为 Core，issuer 与业务流的 sourceActor 不是同一概念。每条 edge 冻结七项通用生命周期要素——issuer/source/target（issuer 为 Core，且只在其 authorityNamespaceId 内签发；sourceActor/targetActor/targetAudience 按 edge 类型绑定 securityDomainId 标识的 typed identity，未绑定身份不得充当 sourceActor 或 targetActor，target 不得被替换或改道）、operation（各 edge 封闭枚举的操作类型，枚举外请求拒绝）、expiry（到期即失效，不得隐式续期）、digest（规范化绑定的 canonical 内容摘要，RFC 8785 JCS）、revocation（撤销是 authorityNamespaceId 内的 authority ledger 事实，security-critical 撤销即时生效并写入审计）、replay（edge 引用 + operation 请求摘要构成 canonical replay key，重复请求幂等归并，已撤销/已过期 edge 的 replay 拒绝并隔离为诊断材料）、current-ledger recheck（每次使用都按当前 authority ledger 复核：edge active、digest 相符、未被撤销或过期，target actor 的 registration/snapshot/evidence 仍 eligible，绑定的 Attempt/allocation/lease 仍 active；任一项不满足一律 fail closed）——缺失任一要素的请求一律 fail closed。typed edge 授权必须不可伪造、不可转授、不可扩权，并同时满足：authority-scope-bound、source/target typed identity-bound、operation/least-capability-bound、attempt/allocation/lease-bound、expiry/deadline 与 payload/intent digest-bound，具备 replay/idempotency/nonce 语义；各 edge 的 issuer/sourceActor/targetActor 定义与专属绑定见下三条：

- `DispatchResultCapability`：Core 按 DispatchLease 向 dispatch-bound Port（Execution 信任域）workload 授予面向 Core result-ingress 的结果接纳能力。issuer=Core；sourceActor 为 dispatch-bound Execution workload，绑定 securityDomainId/principal/registrationId/providerInstanceId；targetAudience 为 Core result-ingress；另绑定 authorityNamespaceId、taskId/runId/attemptId/allocationId、该 DispatchLease 的 leaseId、当前 generation 与 fencingToken；operation 封闭枚举为 result、log、checkpoint、candidate、evidence-ref、heartbeat、receipt；payload/schema/content digest 规则约束每一 operation 的接纳请求；expiry/not-before 以 lease expiry 窗口为界；revocationGeneration 标记撤销代；commandId/idempotencyKey/requestDigest/nonce 与 consume 规则保证接纳幂等且不可重放；targetAudience 固定为 Core result-ingress，将结果改道至其他目标或冒充 result-ingress 的请求拒绝（target substitution 防护）；每次使用按当前 ledger 重判：DispatchLease 仍 active、generation/fencingToken 相符、registration/snapshot/evidence 仍 eligible，任一项不满足一律 fail closed，旧 generation 内容只进 quarantine namespace；edge 不可转授、不可扩权，digest 覆盖上述全部绑定字段；
- `MaterialAccessGrant`：Core 向指定 Execution workload 授予对 Artifact/Secret（Data/Capability 信任域）物料的 scoped 访问短期能力。issuer=Core；sourceActor 为 Data/Capability Provider，绑定 securityDomainId/registrationId/providerInstanceId/configDigest/trust root；targetActor 为申请物料的 Execution workload，绑定 securityDomainId/principal/attemptId/allocationId/generation；另绑定 authorityNamespaceId、typed materialRef/version/commitment；operation 封闭枚举为 read、fetch、decrypt 三类读取操作；scope/maxBytes/maxUses 约束最小能力；expiry 以 Attempt 边界为界，revocationGeneration 标记撤销代；commandId/requestDigest/nonce 保证消费幂等且不可重放；Execution 不解析 Data 域 raw handle，只经 grant 向 Data Provider 出示 target-bound grant；Data Provider 只接受 target-bound grant，替换或改道 targetActor 的请求一律拒绝（target substitution 防护）；禁止转授、bearer 化或跨 Attempt 复用；Secret 与 Artifact profile 分开；digest 覆盖上述全部绑定字段；
- `PublicationAuthorization`：Core 向 SCM/Publisher transport（Publication 信任域）授予绑定 SideEffectIntent/ReviewDecision/evidence digest 的发布授权。issuer/sourceAuthority=Core/controlPlaneId；targetActor 为 Publication 信任域的 SCM/Publisher transport actor，绑定 securityDomainId/publisher principal/registrationId/providerInstanceId；另绑定 authorityNamespaceId、repository/remote/baseRef/headRef/expectedRemoteHead、SideEffectIntent id/digest、已接受 ReviewDecision id 与精确 evidence digest；operation 封闭枚举为绑定 create/update Draft PR；Draft-only、merge-never；requestDigest/commandId/idempotencyKey/nonce 保证单次幂等重放，receiptReconcileId 完成回执对账；expiry 以发布窗口为界，revocationGeneration 标记撤销代；每次使用按当前 ledger 重判：授权仍 active、ReviewDecision 与 evidence digest 未变、Publisher registration 仍 eligible，任一项不满足一律 fail closed；发布目标不得被替换：不得更换 repository/branch/baseRef/headRef/发布策略，不得转授、扩权或 bearer 化，改道发布或变更已冻结发布策略的请求拒绝（target substitution 防护）；digest 覆盖上述全部绑定字段；

每条 typed edge 是 Core 在 `authorityNamespaceId`（§10）内签发的 authority-scope-bound 权威记录（typed edge 记录属于权威对象，由 authorityNamespaceId 拥有）；edge 只承载 scoped handle、digest 引用与授权引用，不得携带 raw credential、raw secret handle，也不得替代 ConformanceEvidence；Provider actor 跨 trustDomainKind 的访问必须持有对应 active typed edge 并通过其全部绑定校验；未经对应 edge 授权、任一绑定不符、或 edge 已过期、被撤销、digest 不符的请求一律拒绝并写入审计。edge 派生的 token/handle（scoped handle、receiptHandle、物料引用 handle 等）只是 Core 签发的指向 edge 权威记录的单向引用：自身不承载授权语义，不得在 Core 之外离线校验或缓存为授权依据；每次使用都必须通过 edge 的 current-ledger recheck，派生 token/handle 不得成为第二权威，也不得成为可替代的凭据——跨信任域访问的唯一权威依据始终是 authorityNamespaceId 内的 edge 记录。

### 4. append-only event ledger 是唯一权威；registry/queue/SSE 是可重建投影

- append-only event ledger 是唯一业务权威，作为 Control Plane 权威对象由 `authorityNamespaceId` 拥有、只允许 Core 写入（§10）；snapshot、queue、SSE 事件流、Provider registry、索引一律是 ledger 的可重建投影（projection），可凭 ledger 重建，不构成第二个权威；
- 权威 ledger sink 使用同事务 atomic compare-and-append/transaction：ledger transition、当前 lease generation 与 Evidence/Artifact 引用在同一原子步骤内校验提交；Artifact/Evidence/Checkpoint/Candidate 的接纳关系归 authority ledger：对象 key 使用 `authorityNamespaceId`+run+attempt+allocation+generation scoped 的 immutable key 与 digest-verified put-if-absent，actor securityDomainId 只作为 provenance 记录；陈旧/冲突 bytes 只能进入 quarantine namespace，永不覆盖当前对象或进入当前 evidence graph（§13）；
- SSE 是只读投影：cursor 身份为 `authorityNamespaceId`+scope+ledgerSequence（权威账本的权威侧身份），订阅方另绑定自身 `securityDomainId` 完成授权判定；SSE cursor 过期、gap 或被压缩时，服务端必须返回可判定的 resync 指令（含 deterministic 起点与 snapshot digest），客户端据此整体重建订阅；不得静默续推，也不得把压缩后的流当作完整历史；交付、去重、背压与重新授权语义见 §14；SSE 不承载业务 ACK、lease heartbeat 或 command 下发；
- `DurableExecutionEngine` 是 Core 的内部 Port：Temporal、Local Engine（embedded/单机 in-process）与任何未来实现都只是该 Port 的 backend，只承担相同 `commandId` 的 at-least-once delivery、timer wakeup、signal transport 与 crash recovery；不拥有 lifecycle/retry/rework 业务语义；delivery/activity retry 不创建 Attempt、不消费业务重试预算（ADR 0017 §9 权威边界不变）；backend 不是 Provider 信任域成员；Core command 的投递经单一权威 seam（同事务 outbox 或 ledger-derived Core command journal 二选一），`commandId` 从 ledger 权威事实稳定派生，backend workflow/activity state 不得成为业务权威（§15）。

### 5. ProviderRegistration 与不可变 ProviderCapabilitySnapshot

Runtime 必须持久化 `ProviderRegistration` 与不可变 `ProviderCapabilitySnapshot`；禁止 memory-only registration。

- 权威归属：ProviderRegistration、ProviderCapabilitySnapshot 与 ConformanceEvidence 也是 authority ledger 事实——由 `authorityNamespaceId` 拥有、只允许 Core 写入，其 create/status/revoke/expire/capture/supersede 全部记录于 authority ledger——仅携带 actor securityDomainId、provenance（来源与 attestation 链）与 eligibility（资格判定结果）；Provider actor 不得写入、改写或宣称拥有自身的注册/快照/证据记录，eligibility 判定按当前 ledger recheck（§3/§6）；
- 幂等身份：`registrationId` 的幂等身份 canonical 绑定 `(securityDomainId, principal, providerType, providerName, providerVersion, protocolVersion, scope)` 与 `idempotencyKey`/`requestDigest`，其中 securityDomainId 是该记录携带的 actor 身份（权威归属仍为 authorityNamespaceId，§10）；仅七元组（含 actor securityDomainId）、key 与 digest 全部相同才幂等归并；同 key 而 digest 不同为 conflict，fail closed（写入审计）；跨 scope/protocol 的重复注册为 conflict 或生成新 registrationId，不修改既有记录；revoked/expired registration 不因普通 replay 复活；
- 生命周期事实：create/status/revoke/expire（registration）与 capture/supersede（snapshot）都是 authority ledger 事实；registration expiry、snapshot expiry 与 evidence expiry 三类 expiry 互相独立；
- attestation 全链绑定（§11）：ProviderRegistration、ProviderCapabilitySnapshot、ConformanceEvidence 与 lease claim 全链绑定 `securityDomainId`、稳定 `providerInstanceId`、effective `configDigest` 与签发/验证 trust root（含 key id/rotation）；任一变化产生新 immutable snapshot/evidence 并触发 eligibility 重判；
- legacy 映射：旧 `v1alpha1` CapabilitySnapshot 仅是 AgentAdapter probe 快照（fail-closed mapper 的来源快照），其字节与 digest 保持不变；只能经显式版本化的 fail-closed mapper 转换为 Runtime 注册输入，并记录 `sourceCapabilitySnapshotDigest`；mapper 不得默认补齐 scope/evidence，缺失信息时 fail closed。

### 6. DispatchLease 只消费持久快照；在途 lease 撤销

- DispatchLease 的 capability match 只消费持久 ProviderCapabilitySnapshot：lease 双绑定权威侧 `authorityNamespaceId` 与 Provider actor 侧 `securityDomainId`（§10），并绑定 `registrationId`、claim 时 active 的 status/version、`providerCapabilitySnapshotDigest` 与 `conformanceEvidenceDigests` 封闭集合，以及 taskId/runId/attemptId/allocationId 与 generation/fencingToken；lease claim 同时按 §11 绑定 providerInstanceId/configDigest/trust root 的 attestation 链；
- lease 记录的引用与 digest 永不改写，只供审计回放；
- 每次 heartbeat、结果/副作用接纳与恢复 reconcile 都必须按当前 ledger 重新判定资格（eligible）；
- registration 被撤销（revoke）/过期（expire）/转为不兼容、snapshot 被 supersede/expire 或 evidence 被 revoke/expire 时，active lease（在途 lease）立即失去资格（不再 eligible）：lease cancel/expiry + generation bump/fencing，Allocation/Attempt 终止对账，晚到结果隔离为诊断材料；
- 失去资格后不得原地续租或降级复用：继续执行只能创建新 Attempt 并以新 lease 重新 match（新 Attempt + 新 lease，重新 capability matching）；
- 失效处置分级：security-critical revoke（credential compromise、protocol violation 等安全关键撤销）使 active lease 立即失去资格并立即 cancel + generation bump + kill workload，不留 drain 窗口；planned/ordinary incompatible upgrade（计划内/普通不兼容升级）一律使用新 registration/新 snapshot，旧实例 stop-new（停止接收新 Attempt/新 lease）+ bounded drain（有界时限内完成在途工作），drain deadline 到期再 fence（cancel/expiry + generation bump）；drain 窗口与 deadline 由冻结策略限定，不得无限延期；
- 事件原因码与审计分开：lease cancel/expiry、drain deadline 到期、kill 等事件携带机器可读原因码（reason code），用于 reconcile 与 fixture 判定；审计记录（audit）另含人可读原因与操作者归因；不得以审计叙述替代原因码，也不得以原因码替代审计归因；
- 普通升级不得复活 revoked/expired 的旧 registration，不得改写旧 lease 的引用与 digest（只供审计回放），不得把换配置或换 trust root key 后的实例当作旧注册延续（§11）；升级后的执行只经新 registration/snapshot + 新 Attempt + 新 lease 重新 match。

### 7. M8 实施顺序（硬门禁）

M8 实施顺序是硬门禁，前置缺失时 claim/match 一律 fail closed：

1. negative fixtures 与 event contract；
2. ProviderRegistration/ProviderCapabilitySnapshot Schema；
3. legacy `v1alpha1` CapabilitySnapshot 的 fail-closed mapper；
4. durable embedded registration + ledger recovery（重启/重建后注册可从 ledger 恢复）；
5. snapshot/evidence validation；
6. 最后 enable DispatchLease match。

negative fixtures 至少覆盖：跨 scope/protocol 重复注册、same key/different digest、revoked registration replay、restart/rebuild 后注册恢复、Provider substitution（换名/换版本冒替），以及各类 claim 后失效（revoke/expire/incompatible/supersede/evidence revoke）的 Push/Pull 场景——在途 lease 必须失去资格并对账，不得续租或降级。另按 §11/§13 补两类 fixture：attestation substitution/config/key-rotation fixture（相同软件版本换 providerInstanceId、换 configDigest 或换 trust root key 后复用旧 ProviderCapabilitySnapshot/ConformanceEvidence 必须失败）；原子 fencing fixture（lost-response、concurrent-write、old-generation overwrite——陈旧 generation 不得先覆盖对象 key 再被 ledger 拒绝，冲突 bytes 只进 quarantine namespace）。另按 §6 补失效处置分级 fixture：security-critical revoke（credential compromise、protocol violation）立即 cancel + generation bump + kill，不留 drain 窗口；planned/ordinary incompatible upgrade 走新 registration/snapshot，旧实例 stop-new + bounded drain，drain deadline 到期再 fence；普通升级复活旧注册或改写旧 lease digest 的 fixture 必须失败。另按 §3 补 Core-only typed edge fixture：伪造 issuer（签发者）或冒用签发身份、错 authority scope、错 source/target（issuer/sourceActor/targetActor 与 edge 类型不符或 target substitution）、错 operation、错 attempt/allocation、已过期、已撤销、digest 替换的 edge 必须全部拒绝；绕过 current-ledger recheck 使用派生 token/handle、转授或扩权尝试、跨 Port token/schema 复用、携带 raw handle/credential 或跨域复用 raw credential/ConformanceEvidence 的请求必须全部失败；Provider actor 未经对应 typed edge 授权、或与该 edge 绑定（source/target securityDomainId、operation、对象、Attempt/Allocation、generation、expiry、digest、当前 ledger 状态）不精确匹配的跨域访问必须失败；Public API/SSE 客户端按各自 AuthN/AuthZ 与 re-AuthZ 访问、Core 内部权威对象引用在 authority ledger 内解析、均不持有 Provider typed edge 的合法 fixture 必须通过；合法三条 typed cross-domain edge 可恢复且幂等；M8/M9 fixture 必须明确区分三类合法 typed edge 的 positive 用例与无 edge、错 edge（edge 类型与访问对象/方向不符）、错绑定的 negative 用例；provider-registration/control 经 transport identity、该 Port AuthN/AuthZ、scope/protocol validation 与 registration protocol 校验、由 Core 将获准事实写入 authority ledger 的合法注册/控制请求（positive）必须通过；跨 Port 复用该 transport identity/token、或以 securityDomainId 相同为由跳过 principal/registrationId/providerInstanceId/scope/attempt/allocation/generation/operation 门禁的同域 bearer 化请求（negative）必须失败；Public API 幂等身份、SSE cursor 与 Artifact/Evidence/Checkpoint/Candidate 对象 key 使用 authorityNamespaceId，将其归属 actor securityDomainId 的 fixture 必须失败。

### 8. M9/M11/M12 分工与 wire contract

- M9 冻结 marshal-server、Public API、Sandbox dispatch Push-Pull、远程注册、SSE 与 DurableExecutionEngine；DispatchLease 绑定必须使用 ProviderRegistration（registrationId）、claim 时 active 的 providerCapabilitySnapshotDigest 与 conformanceEvidenceDigests；HTTP/JSON + OpenAPI 首发；SSE 的恢复、去重、背压与再授权语义按 §14 冻结，具体参数值（heartbeat 间隔、缓冲上限、resync 错误码等）留在 M9 Schema/OpenAPI 冻结；DurableExecutionEngine backend profile 按 §15 声明单一权威 seam（同事务 outbox 或 ledger-derived Core command journal 二选一），升级 fixture 覆盖 workflow versioning/build ID、Continue-As-New、payload 外置与上限、activity heartbeat/cancel/retry；M9 范围内启用的一切非 loopback/in-process transport 自首次 enable 起满足 §12 安全基线；WebSocket/gRPC 推迟，引入须另行 ADR；
- M11 只扩展 HA、多节点与多用户授权策略及生产远程入口治理；M11 不能补 §12 首次安全基线——基线义务在 M9/M10/M12 各远程能力首次 enable 时即生效；
- M12 扩展其余 Provider 的 wire/SDK 与多拓扑 conformance；六类 Provider 的注册产物统一为 ProviderRegistration + 不可变 ProviderCapabilitySnapshot；每个 Port 的 versioned protocol family（§16）随其远程能力首次 enable 冻结，并在 M12 完成按族 SDK 与 conformance；M12 的字段级 wire 细节不在本 ADR 冻结（本 ADR 只冻结对象、身份、protocol family 边界与撤销语义）。

### 9. 与 ADR 0016/ADR 0017 的关系（supersede/clarify 对照）

| 历史条款 | 原表述 | 本 ADR 处置 |
| --- | --- | --- |
| ADR 0017 §4（身份拆分与完整绑定）“远程请求另须携带 principal/portKind/providerType/audience/scope” | universal 身份句，按操作统一附加 Provider 字段 | **部分取代**：身份按 Port 冻结，不设 universal envelope（§3 矩阵）；providerType 属注册/dispatch 语境，public-api 禁止 providerType，public-api 与 provider-registration/control 拒绝 workload lease 字段；公共认证上下文收敛为 requestId/version/principal/audience/scope/deadline，traceContext 只观测；credential 不入业务 JSON/事件/日志/digest |
| ADR 0017 §6（版本化 Provider Protocol 与认证注册）“认证注册并产生 CapabilitySnapshot” | 注册产物可变、无幂等身份、无持久化约束 | **部分取代**：持久化 ProviderRegistration + 不可变 ProviderCapabilitySnapshot；registrationId canonical 幂等绑定；三类 expiry 独立；禁止 memory-only registration；legacy mapper fail-closed 并记录 sourceCapabilitySnapshotDigest（§5） |
| ADR 0017 §7（DispatchLease 唯一状态机）“lease 绑定认证 Provider registration、claim 时冻结的 CapabilitySnapshot 与 conformanceEvidenceRef digest” | 快照可变，失效后 lease 命运未定义；Push/Pull 写成“只改变连接发起方、其余语义完全等价” | **澄清并部分取代**：lease 只消费持久快照，字段为 providerCapabilitySnapshotDigest/conformanceEvidenceDigests；每次 heartbeat/接纳/reconcile 按 ledger 重判资格；失效事件使在途 lease 立即失去资格；继续执行须新 Attempt + 新 lease 重新 match（§6）；Push/Pull 只冻结 outcome/invariant equivalence，允许拓扑特定 transition/timing（§16） |
| ADR 0017 §8（远程副作用业务 fencing 与 Secret/Artifact 边界）“所有远程副作用使用完整 lease 身份” | universal fencing 句覆盖全部远程写路径 | **部分取代**：fencing 与完整 lease 身份只约束 dispatch-bound Port；publication Port 绑定 SideEffectIntent/ReviewDecision/evidence digest；artifact/secret Port 绑定 scoped handle/content digest/scope/expiry 并禁止伪造无关 lease（§3） |
| ADR 0017 §10（生产形态与 Wire Contract） | M9 首版 wire contract；M11 身份扩展；M12 SDK | **澄清并部分取代**：M9 范围 = marshal-server/Public API/Sandbox dispatch Push-Pull/远程注册/SSE/DurableExecutionEngine；M12 扩展其余 Provider wire/SDK；SSE cursor 过期/gap/压缩返回可判定 resync（§4、§8） |
| ADR 0017 §12（M8–M13 分工） | M8 embedded 纵切；M12 六类 Provider 注册产生 CapabilitySnapshot | **部分取代**：M8 增加注册/快照/撤销顺序硬门禁（§7）；M9 lease 绑定字段；M12 注册产物为 ProviderRegistration + ProviderCapabilitySnapshot（§8） |
| ADR 0016 §6（恢复/fencing/checkpoint 中“所有 Attempt 回报与 Artifact/Checkpoint/Candidate/Evidence 接纳必须携带完整身份 + fencing”口径，经 ADR 0017 §4 承接） | universal 接纳句覆盖全部远程写路径 | **取代**：接纳边界按 Port 分流（§3 矩阵）；dispatch-bound Port 继续要求 attemptId/generation/fencingToken + expectedSequence/CAS；publication/artifact/secret Port 按域校验；陈旧内容隔离语义不变。ADR 0016 §6 其余条款（可丢弃执行体、恢复凭账本、Checkpoint 定位、warm reuse 边界）继续有效 |

本 ADR 生效后，上表所列条款以本 ADR 口径为准；ADR 0016/0017 全文保持历史原样，遇措辞冲突以本对照表为准。ADR 0017 未列入本表的决策（二维权限/隔离模型、ConformanceEvidence 证据拓扑、内容寻址 Stage、无双写 Restore、JCS 规范化）继续有效，不与本 ADR 冲突；其中 ADR 0017 §2 证据拓扑由 §11 在不改变签发权威分工的前提下追加 attestation 全链绑定字段，ADR 0017 §9 权威边界由 §15 在不改变 backend 传输职责的前提下冻结实现 seam。

### 10. authorityNamespaceId 与 securityDomainId 双键空间（权威与 actor 的机械边界）

冻结 Control Plane 权威与 Provider actor 的双键空间，机械强制权威与 actor 分离、信任域隔离，取代自然语言 tenant 表述与全系统单一域：

- 冻结 `authorityNamespaceId` 为复合权威命名空间，至少显式包含三元组 `(tenantNamespace, controlPlaneId, authorityScopeId)`：Control Plane 权威对象——Project/Goal、TaskSubmission、Task/Run/Attempt lifecycle 状态、DispatchLease、Allocation、ReviewDecision、Outcome、SideEffectIntent、Receipt reconcile 记录、typed edge 记录、Evidence graph、事件账本、发布决定、idempotency/replay 权威记录、outbox、audit 记录与 SSE cursor 权威序列——一律由 authorityNamespaceId 拥有，只允许 Control Plane Core 写入；`controlPlaneId` 是 HA/灾备中保持稳定的 Control Plane 逻辑权威身份，不是进程实例：进程重启、节点迁移、多副本并行或灾备切换都不改变 controlPlaneId，权威身份与权威对象归属随原 authorityNamespaceId 延续；单实例部署可固定为 `default`；`authorityScopeId` 映射 repository/project 等冻结 scope 的权威边界；
- `securityDomainId` 只标识 Provider actor，是复合 security namespace，至少显式包含三元组 `(tenantNamespace, trustDomainKind, isolationDomainId)`：`tenantNamespace` 是 tenant 边界，单租户部署可固定为 `default`，后续引入 tenant 维度时 tenant 只能作为该组成参与授权判定，不得以自由文本绕过；`trustDomainKind` 是封闭枚举 `execution|publication|data-capability`，与 §2 三信任域一一对应——不得使用全系统单一 `default` 域同时宣称隔离 Execution/Publication/Data-Capability；`isolationDomainId` 是同一 trustDomainKind 内的隔离边界，按各 Port 的冻结规则映射 repository/project、Provider 实例、run/attempt 等；
- 两键空间职责分离：`authorityNamespaceId` 不是 Provider 的 trustDomainKind 维度——不参与 execution|publication|data-capability 划分，也不属于 Provider actor 侧任何信任域；Provider 不得写入、复制或宣称拥有 authorityNamespaceId 内的任何权威对象，也不得把 securityDomainId 记录当作权威事实；反向地，`securityDomainId` 不承载 Control Plane 权威，Provider 不得以它宣称 lifecycle、ReviewDecision 或发布决定；
- ProviderRegistration、ProviderCapabilitySnapshot 与 ConformanceEvidence 也是 authority ledger 事实：由 authorityNamespaceId 拥有、只允许 Core 写入（§5），仅携带 actor securityDomainId、provenance 与 eligibility；Artifact/Checkpoint/Candidate/Evidence bytes 的接纳关系同样归 authority ledger，接纳决定、对象 key 归属与 quarantine 归属都是 authorityNamespaceId 拥有的 ledger 事实（§13）；securityDomainId 在这些记录中只是 actor/provenance 组成，不构成权威归属；
- 复合边界进入全部持久主键与引用键空间：权威侧记录——ledger 事件、submission 记录、Task/Run/Attempt lifecycle、DispatchLease/Allocation、ReviewDecision、Outcome、SideEffectIntent/Receipt reconcile、Evidence graph、typed edge、发布决定、SSE cursor/sequence、idempotency/replay 权威键、outbox 与 audit event——携带 `(tenantNamespace, controlPlaneId, authorityScopeId)`；registration/snapshot/evidence 携带项、lease/allocation actor 绑定、artifact/secret scoped handle、cache key 等 actor 侧引用字段携带 `(tenantNamespace, trustDomainKind, isolationDomainId)`，只用于 actor 身份、provenance 与授权判定，不构成权威归属；
- Provider actor 跨 trust domain 访问默认拒绝（default deny），唯一 allowlist 例外是三条由 Core 独占签发的 typed cross-domain edge（§3）；
- Public API/SSE 是 Client 到 Control Plane 的入口，使用各自的 AuthN/AuthZ、scope 约束与 re-AuthZ（§14），不需要 Provider typed edge；Core 内部权威对象引用（ledger 事件间引用、cursor、证据关系、outbox/ledger 引用）保留在 authority ledger 内，不需要 Provider typed edge；
- Provider actor 跨 trustDomainKind 访问必须持有 active typed edge，每次使用都必须精确匹配 source/target securityDomainId 与该 edge 绑定的全部对象、operation、Attempt/Allocation、generation、expiry/deadline、digest 和当前 authority ledger 状态，未经对应 edge 授权或任一绑定不符的请求一律 fail closed 并写入审计；
- 未经上述例外授权的跨 securityDomainId actor 侧引用与跨 trustDomainKind 引用一律 fail closed——即使 tenantNamespace 与 isolationDomainId 相同，execution、publication、data-capability 之间也不得在 typed edge 授权范围之外互相解析对方句柄，不得复用 credential、AuthZ 或 conformance 引用；
- securityDomainId 三元组完全相同也只是 actor provenance/partition 条件，不构成授权、不构成同域 bearer grant，同域请求仍须逐项匹配所请求 Port 的 principal、registrationId、providerInstanceId、scope、attempt/allocation、generation、operation 门禁；
- provider-registration/control 与 public-api 不持有三类业务 typed edge，经 transport identity、该 Port 的 AuthN/AuthZ、scope/protocol validation 与 registration protocol 校验，由 Core 将获准事实写入 authority ledger（§3/§5）；
- 本条现在冻结：新持久记录自始携带双键空间字段，不得推迟到 M11 再做持久主键迁移；已有 Local MVP 记录在 M8 Schema 落地时按复合边界视图接入（tenantNamespace=`default`，controlPlaneId=`default`，authorityScopeId 按仓库身份派生；trustDomainKind 按记录角色确定性映射，isolationDomainId 按既有仓库身份派生或取 `default`），不改写历史数据。

### 11. Provider attestation 全链绑定（实例/配置/trust root）

冻结 attestation 全链绑定，阻止相同软件版本替换实例、配置或签发密钥后继续复用 hardened evidence：

- ProviderRegistration、ProviderCapabilitySnapshot、ConformanceEvidence 与 lease claim 全链绑定：`securityDomainId`、稳定 `providerInstanceId`（具体运行实例的稳定标识，重新部署/重建不得复用旧实例 id）、effective `configDigest`（生效配置摘要）与签发/验证 trust root（含 signing key id 与 rotation 代）；
- 上述任一项变化必须产生新的 immutable ProviderCapabilitySnapshot 和/或 ConformanceEvidence，并触发 eligibility 重判；旧快照/证据不得被新实例、新配置或新密钥继承复用；
- 同一 Run 的 Worker/Verifier 必须使用不同 principal 与不同 allocation（ADR 0004 独立验证在 Runtime 的延伸），不得共享 registration 实例或 credential；高保证策略可进一步要求 provider/host/failure-domain diversity；
- ADR 0017 §2 证据拓扑不变：本节只在 ConformanceEvidence 之上追加绑定字段，probe 调度、out-of-band 观察、裁决与签发的权威分工保持不变；
- M8 补 substitution/config/key-rotation fixture（§7）；远程注册在 M9 enable 时按 §12 基线校验 trust root 链。

### 12. 远程 transport 安全基线（首次 enable 即生效，不可推迟）

- 任何非 loopback/in-process transport 从首次 enable 起强制 TLS（服务端身份校验 + 传输加密）；loopback/in-process 之外禁止明文 transport；
- workload-to-workload 与 workload-server 通道优先 mTLS，或等价的可证明不可转移的 workload identity；可转移的共享 secret 不得作为 workload 身份；
- 每次调用执行双向身份校验（server/provider 身份与 audience/scope）；短期 credential 支持 rotation 与 revocation；replay protection 由 ledger replay key + nonce/时间窗承担（与 ADR 0017 §4 一致）；
- M9/M10/M12 各远程能力（Push/Pull dispatch、远程注册、SSE、远程 Provider transport）首次 enable 时必须满足本基线；M11 只扩展 HA、多节点与多用户授权策略，不能补首次安全基线；
- credential 与私钥仍不进入业务 JSON、事件、日志或 digest（§3）。

### 13. 原子 fencing 写入汇与 quarantine namespace

冻结权威写入的原子 fencing sink，关闭“旧 generation 先覆盖对象 key 再被 ledger 拒绝”的窗口：

- 权威 ledger sink 使用 atomic compare-and-append/transaction：ledger transition、当前 lease generation 与 Evidence/Artifact 引用在同一原子步骤内校验并提交，不允许先写后拒或先拒后写的分裂状态；
- Artifact/Evidence/Checkpoint/Candidate bytes 的接纳关系归 authority ledger：对象 key 为 `authorityNamespaceId`+`runId`+`attemptId`+`allocationId`+`generation` scoped 的 immutable key，接纳决定、key 归属与 quarantine 归属是 `authorityNamespaceId` 拥有的 ledger 事实，actor 侧 `securityDomainId` 只作为 provenance 元数据记录、不参与 key 的权威归属；写入必须 digest-verified put-if-absent；已存在的 key 永不覆盖；
- 陈旧或冲突 bytes（旧 generation、digest 不符、并发竞争失败）只能进入 quarantine namespace 留存为诊断材料，永不覆盖当前对象，永不进入当前 evidence graph；
- `expectedSequence`/CAS 请求规则（ADR 0017 §4）仍是入口校验；本节保证它与 ledger transition、当前 lease generation、fencingToken 和 Evidence/Artifact 引用落在同一原子校验；
- M8/M9 补 lost-response、concurrent-write、old-generation overwrite fixture（§7/§8）。

### 14. SSE 恢复、去重与再授权

冻结 SSE 事件流的身份、交付、恢复与再授权语义（SSE 是只读投影，§4）：

- cursor 身份为 `authorityNamespaceId` + `scope` + `ledgerSequence`（权威账本的权威侧身份）；订阅方另绑定自身 `securityDomainId` 完成授权判定；scope 内 sequence 单调递增；
- 交付语义为 at-least-once，客户端以 `eventId`/sequence 去重；服务端不得假设 exactly-once 消费；
- cursor 过期、gap 或被压缩（compaction）时，服务端返回 deterministic resync 起点与 snapshot digest，客户端据此整体重建订阅；不得静默续推，也不得回放不完整历史；
- 连接维护：服务端 heartbeat；订阅缓冲有界并冻结 backpressure 策略——超限即断开并引导 resync，不得阻塞 ledger 写入；
- 授权：周期性 re-Authorization；敏感变更（registration 撤销、scope 变更、权限收回）即时 re-Authorization，校验失败即 fail closed 关闭连接；
- SSE 不承载业务 ACK、lease heartbeat 或 command 下发：lease heartbeat 走 dispatch Port，command 走 §15 的单一权威 seam；
- 具体参数值（heartbeat 间隔、缓冲上限、resync 错误码等）留在 M9 Schema/OpenAPI 冻结。

### 15. DurableExecutionEngine 单一权威 seam

冻结单一权威 seam，消除“ledger 已提交而 command 未投递”或反向的双写窗口：

- 同事务 outbox 与 ledger-derived Core command journal 二选一：前者 ledger 事件与 command 出站在同一事务原子提交；后者 command journal 由 ledger 事实确定性派生并可重放。M9 backend profile 必须声明所选 seam 并通过对应一致性 fixture；不得同时维护两套可分歧的出站事实；
- `commandId` 从权威事实（ledger）稳定派生；同一 commandId 的重复投递/消费必须幂等；
- backend（Temporal、Local Engine）只消费 command 并回报 receipt；workflow/activity state 不得成为业务权威，不得决定 lifecycle、retry eligibility、rework 或终态；
- crash/升级恢复按单一 seam 重放：未投递 command 由 outbox/journal 重新派生，不依赖 backend 内部状态；
- ADR 0017 §9 权威边界不变：本节冻结其实现 seam；backend 的 at-least-once delivery、timer、signal 与 crash recovery 职责不变；
- M9 backend profile 与升级 fixture 覆盖 workflow versioning/build ID、Continue-As-New、payload 外置与上限、activity heartbeat/cancel/retry（§8）。

### 16. 按 Port 的 versioned protocol family

冻结按 Port 的 versioned protocol family，禁止跨 Port 协议混用：

- 每个 Port 拥有独立 versioned protocol family：独立 `audience`、独立 AuthZ scope、独立 request/response schema、独立 error 模型、幂等约定、撤销语义与独立 conformance profile；
- 跨 protocol family 只共享：transport 层（versioned HTTP/JSON）、RFC 8785 JCS 规范化与最小 base auth primitives（§3 公共认证上下文的校验）；禁止跨 Port 复用 token、schema 或 operation；
- 六类 Provider 分属不同 protocol family，不共享 family、audience、schema、profile、conformance suite、token 或 operation；对每个具体 Port/protocol family，embedded、Push HTTP、Pull outbound runner 只是该族内的 transport/topology adapter，运行该族统一的 conformance suite，不得派生不同协议版本或语义分支；
- Push 与 Pull 拓扑只冻结 outcome/invariant equivalence：唯一 claim、eligibility、fencing、deadline（ack/heartbeat/expiry）、无双活与晚到结果隔离；允许拓扑特定（topology-specific）的 offer/poll/claim/ack transition 与 timing；两拓扑 conformance 比较 normalized business trace 与业务不变量，不比较逐步 wire trace；不得为 Push 定义弱化不变量的简化协议，也不得为 Pull 放宽 capability match；
- M9 冻结其范围内 protocol family（public-api、provider-registration/control、dispatch-bound Sandbox/Agent/Verification）；其余 Port 的 protocol family 随各自远程能力首次 enable 冻结，并在 M12 完成按族 SDK 与 conformance（§8）；protocol family 首次 enable 前必须满足 §3 矩阵与 §12 transport 安全基线。

## 保留的不变量

ADR 0016/0017 冻结的不变量集合全部保留：Worker 不自证；Run 冻结 spec/base/policy/最低环境要求；单 workspace/attempt 写入者；Worker/Verifier/Publisher 分权（Publisher 永不成为 Sandbox workload，Worker/Verifier 必须不同 principal 与不同 allocation）；ReviewDecision 精确绑定 evidence；失败或阻塞保存 Outcome；副作用 intent-first + receipt + reconcile；能力不足 fail closed；普通宿主进程永不 hardened；Merge 默认禁用；`.marshal/` 不进入业务提交。本 ADR 追加：权威与 actor 双键空间现在冻结——authorityNamespaceId=(tenantNamespace, controlPlaneId, authorityScopeId) 拥有 Project/Goal、TaskSubmission、Task/Run/Attempt lifecycle、DispatchLease/Allocation、ReviewDecision、Outcome、SideEffectIntent/Receipt reconcile、Evidence graph、typed edge、事件账本、发布决定、idempotency/outbox/audit 与 SSE 权威序列等全部 Control Plane 权威对象，只允许 Core 写入；controlPlaneId 是 HA/灾备中保持稳定的 Control Plane 逻辑权威身份，不是进程实例；ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence 也是 authority ledger 事实，仅携带 actor securityDomainId、provenance 与 eligibility；securityDomainId=(tenantNamespace, trustDomainKind, isolationDomainId) 只标识 Provider actor，权威对象不得进入 actor 侧键空间；authorityNamespaceId 不是 Provider 的 trustDomainKind 维度，Provider 不得写入或宣称权威对象；三条 Core-only typed edge（DispatchResultCapability、MaterialAccessGrant、PublicationAuthorization）是 Provider actor 跨 trust domain 访问默认拒绝（default deny）规则的唯一 allowlist 例外，其余 Provider actor 跨域访问默认拒绝，每条 edge 是绑定 issuer/source/target（issuer 为 Core，issuer 不等于业务流的 sourceActor；sourceActor/targetActor/targetAudience 按 edge 类型绑定）、operation/expiry/digest/revocation/replay/current-ledger recheck 与各自专属绑定的 authority-scope-bound 权威记录，每次使用必须按当前 authority ledger 复核，派生 token/handle 不得成为第二权威，不承载 raw credential/raw secret handle，不替代 ConformanceEvidence；信任域之间不共享 credential/AuthZ/审计/conformance profile；securityDomainId 复合安全域键空间（tenantNamespace/trustDomainKind/isolationDomainId）现在冻结，未经三条 typed edge 中对应 active edge 授权或绑定不精确匹配的跨域引用 fail closed（三条 typed edge 是默认拒绝规则的唯一 allowlist 例外；Public API/SSE 与 Core 内部权威引用不需要 Provider typed edge），不等 M11 迁移持久主键；六类 Provider 分属不同 Port/protocol family，不共享 conformance suite，对每个具体 Port 由 embedded/Push/Pull 作为该族 transport adapter 运行该族同一 suite；Push/Pull 只冻结 outcome/invariant equivalence（唯一 claim、eligibility、fencing、deadline、无双活、晚到隔离），允许拓扑特定 transition/timing，conformance 比较 normalized business trace 与业务不变量；registration/快照/evidence 失效使在途 lease 立即失去资格，继续执行须新 Attempt + 新 lease 重新 match；security-critical revoke 立即 cancel + generation bump + kill，planned/ordinary upgrade 走新 registration/snapshot + 旧实例 stop-new + bounded drain，drain deadline 到期再 fence，事件原因码与审计分开，普通升级不得复活旧注册或改写旧 lease digest；非 loopback/in-process transport 自首次 enable 起强制 TLS 与双向身份校验，M11 只扩展 HA/多用户策略、不补首次基线；attestation 全链绑定 providerInstanceId/configDigest/trust root，任一变化产生新 immutable 快照/证据并重判资格，同版本换实例/配置/密钥不得复用 hardened evidence；权威写入同原子（compare-and-append/transaction）+ Artifact/Evidence/Checkpoint/Candidate 接纳关系归 authority ledger：authorityNamespaceId+run+attempt+allocation+generation scoped immutable key + digest-verified put-if-absent（actor securityDomainId 只作 provenance 记录），陈旧/冲突 bytes 只进 quarantine namespace；SSE 是只读投影，cursor 身份为 authorityNamespaceId+scope+ledgerSequence，不承载 ACK、lease heartbeat 或 command；DurableExecutionEngine 单一权威 seam（outbox/ledger-derived journal 二选一），backend workflow/activity state 不是业务权威；按 Port 独立 protocol family，禁止跨 Port token/schema/operation；public-api 禁止 providerType 并拒绝 workload lease 字段；provider-registration/control 拒绝 workload lease；credential 不进入业务 JSON/事件/日志/digest；fencingToken 是非凭据 stale-write guard，不能替代 AuthN/AuthZ。

## 后果

- 接受后：Control Plane/Execution Plane/Provider 的信任边界获得统一口径；M8 增加注册/快照/撤销实施顺序硬门禁与 attestation/原子 fencing/失效处置分级 fixture 义务；M9/M11/M12 分工细化（M11 只扩展 HA/多用户策略，不补首次安全基线）；registry/SSE 的恢复行为可判定；权威与 actor 分离由 authorityNamespaceId/securityDomainId 双键空间机械证明，Provider actor 跨 trust domain 访问默认拒绝（default deny），唯一 allowlist 例外是 Core 独占签发的三条 typed cross-domain edge（DispatchResultCapability/MaterialAccessGrant/PublicationAuthorization），信任域隔离由复合 securityDomainId 机械证明，Push/Pull 等价性收敛为可执行的 outcome/invariant equivalence 口径；权威对象清单（submission/Task/Run/Attempt/ledger/DispatchLease/Allocation/ReviewDecision/Evidence graph/Outcome/SideEffectIntent/Receipt reconcile/typed edge/idempotency/outbox/audit/SSE）收敛为 authorityNamespaceId 独占拥有，ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence 作为 authority ledger 事实仅携带 actor securityDomainId/provenance/eligibility，Artifact/Checkpoint/Candidate/Evidence bytes 接纳关系归 authority ledger，controlPlaneId 冻结为 HA/灾备中保持稳定的逻辑权威身份而非进程实例，三条 Core-only typed edge 的 issuer/source/target（issuer 为 Core 且不等于业务流 sourceActor；sourceActor/targetActor/targetAudience 按 edge 类型绑定）/operation/expiry/digest/revocation/replay/current-ledger recheck 与专属绑定的细化及派生 token/handle 非权威口径亦一并冻结；
- 启用义务：M9/M10/M12 每次首次 enable 远程能力，必须同时交付 §12 transport 安全基线与对应 §16 protocol family，否则不得 enable；
- 兼容义务：legacy `v1alpha1` CapabilitySnapshot 字节/digest 不变，只经 fail-closed mapper 转换；Local MVP 行为不回归；已有持久记录在 M8 Schema 落地时按复合边界视图接入（tenantNamespace=`default`，trustDomainKind 按记录角色确定性映射，isolationDomainId 按既有仓库身份派生或取 `default`），不改写历史数据；
- **接受只冻结设计**：M8 实现与 conformance 状态不因此提前，M7 保持 `IN_PROGRESS`、M8–M13 保持 `PLANNED`（见 [Roadmap 状态](../roadmap-status.md)）；后续实现若偏离本 ADR，须重开本 ADR 相关章节或以新 ADR 取代。

## 备选（已否决）

- 六类 Provider 共用同一 credential/AuthZ/conformance profile：否决。Publication transport 与 workload execution 混域会让发布权限渗透进 Worker 边界，违反 ADR 0003；
- 保留 universal 身份 envelope（所有远程请求统一附加 providerType/lease 字段）：否决。public-api 与注册/控制面必须反向拒绝 workload lease 字段，universal envelope 无法表达按 Port 的 required/forbidden 差异；
- memory-only registration 或可变 CapabilitySnapshot：否决。恢复与审计必须可回放，可变快照让 lease 资格判定失去基准；
- registration/快照/证据失效后允许在途 lease 原地续租或静默降级：否决。失效即信任撤回，继续执行必须经新 Attempt/lease 重新 capability match；
- registry/SSE 作为第二个权威，或允许 cursor gap 静默续推：否决。双权威与不可判定恢复都会破坏恢复语义；SSE 承载 ACK/lease heartbeat/command 同样否决——只读投影不得拥有写语义；
- 把 TLS/调用者身份/replay protection 推迟到 M11 一次性补：否决。远程注册与 Push/Pull 在 M9/M10 即启用，远程 credential/lease 会先于传输身份上线，形成不可审计的提权窗口；基线必须随首次 enable 生效；
- securityDomainId 推迟到 M11 再做持久主键迁移：否决。迁移前域间 credential/AuthZ/audit/conformance 隔离不可机械证明，迁移过程本身会成为第二个事实来源；
- backend workflow state 与 ledger 双写，或同时维护两套可分歧的出站事实：否决。双写窗口必然出现 ledger 已提交而 command 未投递或反向情形；只能同事务 outbox 或 ledger-derived journal 二选一；
- 六类 Port 共用同一 operation schema/token/audience/conformance profile：否决。Publication/Secret 会借通用 Provider 能力进入 Execution 语境，信任域隔离失效；
- 允许陈旧 generation bytes 在 ledger 拒绝后仍覆盖对象 key：否决。对象写入必须与 ledger 校验同原子，否则当前 evidence graph 可被陈旧写入污染；
- 把 fencingToken 升级为通用凭据：否决。fencingToken 是非凭据 stale-write guard，凭据边界按 Port/AuthZ 管理，不能替代 AuthN。
