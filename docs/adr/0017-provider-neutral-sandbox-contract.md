# ADR 0017：Provider-neutral Sandbox 安全契约（二维权限/隔离、Conformance 证据、内容寻址、fencing 与 Restore）

- 状态：已接受（Accepted；维护者 2026-08-10，以本返工全部 P1 通过 Round 2 独立验证与 ReviewDecision accept 为前提。**接受 ADR 只关闭设计歧义**：不得据此把 M8 实现或 conformance 状态提前标为完成，M7–M13 实施状态一律以 [Roadmap 状态](../roadmap-status.md) 为准）
- 日期：2026-08-10
- 决策来源：M8 首次 Sandbox SPI dogfood 以 reject 结束，其阻塞证据（已完整内嵌于本次返工 TaskSpec）暴露出 ADR 0016 冻结的契约仍留有七类可歧义点，无法支撑 Local 与 Cloudflare 两个可替换实现按同一语义收敛；Round 2 独立评审进一步识别出首轮候选文本的六类歧义（conformance 证据拓扑、门禁状态分层、耐久引擎语义、Push/Pull 租约等价、workloadRole/principal 拆分、wire contract 与 AuthN/AuthZ 分工）；维护者据此委托本次返工冻结 provider-neutral 共同契约并逐项关闭
- 关联：ADR 0002（worktree 隔离）、ADR 0003（Worker 与 Publisher 分权）、ADR 0004（独立证据权威）、ADR 0006（Attempt 控制根）、ADR 0014（read-only 画像）、[ADR 0016](0016-durable-runtime-and-sandbox-provider.md)（本 ADR 对其决策 §4、§5、§6、§7、§9 做澄清或部分取代，见 §13 对照表）；[Runtime 架构](../runtime-architecture.md)；[安全模型](../security-model.md)；[实施计划](../implementation-plan.md)

## 背景

ADR 0016 已确定 SandboxProvider 可插拔、Cloudflare Sandbox 为首个 managed option，并以 conformance 作为 Provider 准入手段。但首次 Sandbox SPI dogfood 的 reject 证据表明，现有契约在以下七处无法给出不可歧义的共同实现：

1. 单一 `executionProfile`（`read-only`/`workspace-write`/`hardened`）把“能做什么”（权限）与“强制有多可信”（隔离保证级别）压在同一维度，无法表达 `read-only` + `hardened`（在强隔离环境里评审不可信代码）这类正交组合；
2. `hardened` 声明可以来自 Provider 自报的 Enforcement，没有独立签发、可密封、可撤销的证据形态；
3. Stage 允许只回显声明 digest，Provider 不必对真实 bytes 重算 sha256，内容寻址名存实亡；
4. 操作身份没有完整绑定 task/run/attempt/role/allocation/generation/token，重放请求可不经过当前 lease fencing；
5. Restore 的 in-place 恢复未定义双写语义，旧进程树可能在恢复后继续写；
6. M8 的“常驻单节点纵切”与 M9 的形态未切分：提交入口、调度租约、Public API 与 Provider 版本化注册的形状没有冻结，Provider 观测完成可能被误当成 ReviewDecision 或 safe-to-publish 宣布；
7. 规范化没有统一声明复用仓库既有 JCS，遇重复 JSON member 的行为未定义。

Round 2 独立评审在首轮候选文本上进一步打开了六项 P1 歧义，本 ADR 一并关闭：

1. **conformance 证据拓扑**：首轮文本要求证据采集 workload 运行在独立于被测 Provider 的 Verifier sandbox——那样只能测到 Verifier sandbox，无法测量被测 Provider 的 mount/network/resource/credential 强制能力，且错误套用了业务成果的独立验证拓扑（业务验证要隔离 Worker，conformance probe 恰恰要测试被测 Provider，见 §2）；
2. **门禁状态分层**：审计报告顶层 APPROVED 表述与增补节开放 P1 自相矛盾，可能让人或自动化提前把 M8+ 视为已获批准（由[审计报告](../audit-report.md)的分层门禁记录承接）；
3. **耐久引擎语义**：文档同时使用 DurableOrchestrator Port、DurableExecutionEngine backend 与外部引擎承担 retry，读者可能让外部引擎创建新 Attempt 或消费业务重试预算，形成双权威（见 §9）；
4. **调度租约拓扑等价**：首轮只为 Pull 列出 capability matching、ack、heartbeat、deadline、generation 与 fencing，Push 可能退化为 fire-and-forget（见 §7）；
5. **身份拆分**：首轮把 role 扩成 worker/verifier/publisher/control-plane，冲突于 Publisher 位于独立信任域且 Sandbox workload 只承载 worker/verifier 的不变量，并把调用主体身份与执行 workload 身份混成一个可扩权枚举（见 §4）；
6. **wire contract 与 AuthN/AuthZ 分工**：HTTP/JSON + OpenAPI 被推迟到 M12、M9 首版 wire contract 与可恢复事件流未冻结，且 M9 禁止匿名 Pull 却未定义注册身份来源（见 §10、§12）。

本 ADR 逐项冻结 provider-neutral 契约。本 ADR 不改变 ADR 0016 冻结的核心对象模型、提交入口幂等边界与不变量集合，也不改变 Worker/Verifier/Publisher 分权与 merge 默认禁用。

## 决策

### 1. AccessMode × AssuranceLevel 二维正交模型

执行契约拆分为两个正交维度；单一 `executionProfile` 不再作为 Runtime 阶段的内部表示（保留为兼容面，见映射表）：

- `AccessMode`（权限维度，回答“能做什么”）：
  - `read-only`：对源码只读、写域限于产物；语义承继 ADR 0014（读 worktree 全量与 `readRoots`，写仅限 `control/output` 与 `scope.allowPaths` 产物路径，只读命令白名单，不得 spawn 子 Agent）；
  - `workspace-write`：可读写任务 worktree 并执行开发命令。
- `AssuranceLevel`（隔离维度，回答“强制有多可信”）：
  - `workspace-write`：工作流级控制（独立 worktree、过滤环境、工作流 gate、拒绝分级）；普通宿主机进程最高只能达到该级别；
  - `hardened`：执行体级强制（container/VM/OS 执行体强制 mount/network/resource/credential 隔离），且必须绑定有效 `ConformanceEvidence`（见第 2 节）。

两种维度正交，四种组合均合法：`read-only × workspace-write`、`read-only × hardened`（不可信代码评审）、`workspace-write × workspace-write`、`workspace-write × hardened`。

Run 在 `SandboxRequirements` 中冻结二维要求：`accessMode`（本次请求要求的 AccessMode）与 `minimumAssuranceLevel`（最低 AssuranceLevel）。实际生效的二维组合在 Attempt 分配时记录于 `SandboxAllocation`，并绑定 Outcome 的安全声明。

旧 `executionProfile` 的兼容映射（确定性、唯一）：

| 旧 `executionProfile` | `AccessMode` | `AssuranceLevel` |
| --- | --- | --- |
| `read-only` | `read-only` | `workspace-write` |
| `workspace-write` | `workspace-write` | `workspace-write` |
| `hardened` | `workspace-write` | `hardened` |

拒绝与降级规则：

- 请求的 AssuranceLevel 无法满足（候选 Provider 无有效 ConformanceEvidence）：fail closed——失败的 Allocation/Attempt 先终止并对账，调度器仅可为新 Attempt 选择持有有效证据的兼容 Provider；无兼容 Provider 时 Run 保持 `BLOCKED`，绝不静默降级；
- 降级只允许作为操作者显式决策：必须创建新 Run（修改冻结项创建新 Run）并在 Outcome 记录理由，不得在 Attempt 内隐式降级；
- Local 普通宿主进程 Provider 永不 `hardened`：对其发起的 hardened 分配请求必须在 plan/分配阶段 fail closed，或改由持有有效证据的 Provider 承接；
- AccessMode 在 Run 内不可升级：`read-only` 的 Run 不能经 rework 变成 `workspace-write`（ADR 0014 规则不变）。

持久记录迁移：

- 既有 `v1alpha1` 持久记录保留原 `executionProfile` 字段与取值，不重写历史；
- 读取端按上表做兼容映射；二维字段随 M8 一起冻结 Schema，记录于 `SandboxRequirements`、`SandboxAllocation` 与 Outcome；
- Outcome 安全声明记录生效的二维组合与 `conformanceEvidenceRef`，同时保留原 `executionProfile` 字面以支持审计回放。

### 2. ConformanceEvidence：hardened 的唯一准入路径

Provider 自报的 Enforcement 不能获得 `hardened`。准入证据是由 conformance 套件签发的密封 `ConformanceEvidence`，签发链与证据拓扑如下：

- 身份绑定：`providerType`/`providerName`/`providerVersion`/`protocolVersion`，以及签发方（Conformance Verifier 与 conformance 套件）的身份与版本；
- artifact 摘要：套件每个 probe artifact 携带 sha256 摘要（`probeArtifactDigests`）；证据体自身的摘要为 `evidenceDigest`（canonical JSON + SHA-256，见第 11 节）；
- 逐维结果：`mount`/`network`/`resource`/`credential` 四维逐项结果（`passed`/`failed`/`not-tested`）；声明 `hardened` 要求四维全部 `passed`；
- 有效期与撤销：`validFrom`/`expiresAt`；Control Plane 可显式撤销（撤销写入事件账本）；过期或被撤销的证据立即失效，对应 Provider 回落到最高 `workspace-write` AssuranceLevel，已分配 Attempt 按失败语义终止并对账；
- **证据拓扑（冻结）**：
  - probe 定义、challenge/nonce 与 probe artifact digest、调度、out-of-band 观察、裁决与 `ConformanceEvidence` 签发，全部由 Marshal Control Plane 与独立 Conformance Verifier 控制；
  - probe workload 作为**敌对测试负载**运行在**被测 Provider 创建**、且身份精确绑定（conformance suite 身份 + `workloadRole=verifier` + 第 4 节完整身份元组）的 **target allocation 内**——被测 Provider 的 mount/network/resource/credential 强制能力只有在其自身分配内运行 probe 才测得到；
  - Control Plane 与 Conformance Verifier 通过 out-of-band 观察与对账（事件账本、Inspect、外部可达性探测）获取结果，不依赖被测 Provider 的自报；被测 Provider 的 `completed`/receipt 只是裁决输入之一，**不能自签通过**；
  - 该拓扑与 ADR 0004 的业务独立验证拓扑是两回事：业务验证 workload 运行在独立于 Worker 的 Verifier sandbox，而 conformance probe 恰恰必须运行在被测 Provider 的 target allocation 内；文档不得再把“probe 运行在独立于被测 Provider 的 sandbox”作为 conformance 拓扑口径；
- 独立性指裁决与签发方独立：Conformance Verifier 与 Control Plane 独立于被测 Provider，不采信其自报；Provider 观测到“probe workload 已完成”本身不构成证据；
- 普遍性：Local 普通宿主进程 Provider 永不 hardened；Cloudflare 与任何第三方 Provider 一律通过相同证据准入，无豁免；
- 记录位置：`conformanceEvidenceRef` 进入 Provider 注册产生的 CapabilitySnapshot，并可从 Outcome 追溯。

### 3. Stage 内容寻址：真实 bytes 与重算摘要

Stage 的每个冻结输入（base checkout、control input、workspace seed、artifact 引用）必须携带或引用真实 content-addressed bytes：

- 每个 `StageInput` 携带唯一 `inputId`、声明 `sha256` 与二选一形态：
  - `inline`：对象内联于 Stage 请求；单对象上限 `1 MiB`，单次 Stage 请求 inline 总量上限 `16 MiB`；
  - `locator`：超限对象一律走 locator，形态为 provider-neutral 的 `artifactRef = { storeId, sha256, sizeBytes }`；`storeId` 引用在 Attempt 分配时绑定的 Marshal ArtifactStore 别名；locator 不得是任意外部 URL，不得携带凭据；bytes 由 Provider 经分配时绑定的 ArtifactStore Port 拉取；
- Provider 必须对每个 input 在消费前后各重算一次 sha256：消费前重算与声明 digest 不一致即拒绝 Stage 并上报 `StageInputMismatch`（Attempt fail closed）；落地后、交付 Worker 消费前的读取回验防止 staging 过程损坏；
- 禁止只回显声明 digest：`StageReceipt` 必须记录 Provider 重算所得 digest 与计量方式；conformance 套件必须包含篡改 bytes 的 fixture——只回显不重算的 Provider 必须被判失败；
- digest 算法统一 SHA-256；Stage 元数据的规范化遵循第 11 节。

### 4. workloadRole/principal 拆分与身份完整绑定

**workloadRole 与认证 principal 拆分**：

- Sandbox `workloadRole` 是封闭枚举，只允许两个取值：`worker`、`verifier`——这是 Marshal 交给 SandboxProvider 执行的全部 workload 种类，不含第三种，不可扩展；
- `control-plane`、`publisher`、operator、API caller 不是 workloadRole，而是不同语义 Port（Public API Port、Provider Protocol Port、Publication Port、Artifact/Secret Port）上受 AuthZ 约束的认证 principal/actor；**Publisher 永不成为 Sandbox workload**，其凭据与发布权限始终位于 Worker 信任边界之外（ADR 0003 不变量）；
- Provider 不得借通用 role 取得跨 Port 能力：远程请求身份额外绑定 `principal`（认证注册主体）、`portKind`（`public-api`/`provider-protocol`/`publication`/`artifact`/`secret`）、`providerType`、`audience`、`scope`；从某一 Port 到达的请求只能行使该 Port 声明的能力，跨 Port 能力请求一律 fail closed。

**完整身份元组与重放边界**：

- 每个 Sandbox SPI 操作与远程副作用必须携带完整身份元组：`taskId`、`runId`、`attemptId`、`workloadRole`（仅 `worker`/`verifier`）、`allocationId`、`generation`、`fencingToken`，以及操作自身的 `commandId`；远程请求另须携带上述 Port 与 Provider 身份字段；
- replay key 由完整身份 + `commandId` 的 canonical JSON 规范化后统一派生；身份元组缺失或不匹配一律 fail closed；
- 普通 replay（崩溃重试、编排引擎 at-least-once 投递）先过当前 DispatchLease 的 fencing（`generation`/`fencingToken`）校验，再进入 `commandId` + `expectedSequence` CAS：持有当前 lease 的 replay 幂等归并，陈旧 lease 的 replay 拒绝并隔离为诊断材料；
- Restore 的 lost-response reconciliation 与普通 replay 分离：Control Plane 无法判定 Restore 结果（响应丢失）时走独立 reconcile 路径——先 Inspect 执行体真实状态、比对账本、选择合法转换；该路径不消费普通 replay 的幂等记录，也不得重发同一 `generation` 的 Restore；
- 持有完整身份但 lease 已过期的请求不原地重试：终止 + 对账 + 新 Attempt。

### 5. 无双写 Restore：replacement allocation 优先

- Restore 的默认语义是 **replacement allocation**：Control Plane 终止并失效旧进程树与旧 `SandboxAllocation`，创建新一代 Allocation（新 `allocationId`、`generation` 单调递增），以 Control Plane 单写者 CAS 激活；
- in-place Restore 仅在能确认旧进程树已终止（Inspect 确认）或从未存在时允许；恢复后不得允许旧进程继续写——写路径只认当前 `generation` 下启动的进程；
- 单写者不变量：任何时刻，同一 Run/Attempt 最多有一个持有当前 `generation` 的 Allocation 处于活跃状态；
- 验收口径（故障注入）：Restore 响应丢失、并发 Restore、恢复后陈旧 handle 写入，都必须被确定性拒绝并记录；无双写、无丢证据；
- Checkpoint 语义不变：`CheckpointRecord` 仍是 Attempt 级加速手段，digest 绑定，不替代持久控制平面。

### 6. 版本化 Provider Protocol 与认证注册

- Provider 接入必须先经认证注册并产生 CapabilitySnapshot：注册表校验 Provider 身份与版本，至少冻结 `protocolVersion`、`providerType`、`providerName`、`providerVersion`、`capabilities`、`conformanceEvidenceRef`、`scope` 与撤销/失效语义；
- 未知 `protocolVersion` 或不兼容版本 fail closed：拒绝注册；已注册 Provider 版本转为不兼容时，以失效事件撤销，调度器停止为其分配新 Attempt，在途 Attempt 终止并对账；
- Core 冻结 provider-neutral 语义 Port：in-process embedded、Push HTTP、Pull runner 只是同一 Port 的 transport/topology adapter，必须通过同一 conformance 套件；Port 语义不随 transport 变化；DispatchLease 的唯一状态机见第 7 节；
- 观测边界：Provider 观测到 Exec 完成只是生命周期守卫的输入；Provider 不得自行宣布 ReviewDecision 或 safe-to-publish；Verification Provider 只能执行独立验证 workload，不得决定 gate/ReviewDecision；gate、ReviewDecision 与发布权限判断只在 Marshal Control Plane（ADR 0003/0004 不变）；
- 耐久执行引擎的 Port 名与权威边界见第 9 节。

### 7. DispatchLease：Push/Pull 共用的唯一状态机

DispatchLease 是唯一冻结的分发状态机；Push 与 Pull 只改变连接发起方（Push：Control Plane 调用 Provider endpoint 投递 lease offer；Pull：Provider runner 以 outbound-only 请求领取 lease），其余语义完全等价：

- **租约绑定**：每个 lease 必须绑定认证 Provider registration（第 6 节）、claim 时冻结的 CapabilitySnapshot 与 `conformanceEvidenceRef` digest、`taskId`/`runId`/`attemptId`/`allocationId`，以及 `generation`/`fencingToken`；
- **租约生命周期**：offer（Push）或候选领取（Pull）→ claim-or-delivery ack deadline → 激活 → heartbeat → expiry/cancel/reconcile → 换代 generation bump；ack deadline 超时或响应丢失时 lease 失效并进入 reconcile；
- **单活 allocation**：超时、响应丢失或并发竞争都不得为同一 Run/Attempt 产生第二个 active allocation；新 allocation 一律经 Control Plane 单写者 CAS 激活（与第 5 节同一不变量）；
- **Push 同样先 capability match**：Push offer 前必须比对 Provider registration 的 CapabilitySnapshot 与 ConformanceEvidence，不匹配不投递；Pull 先匹配后领取，禁止匿名领取；
- **陈旧结果隔离**：lease 已过期或 generation 陈旧的晚到结果一律隔离为诊断材料，不得进入当前 Evidence/Review/Publication；
- **等价验收**：M9 退出门禁必须包含 Push/Pull 两拓扑等价 conformance 与故障注入口径——同一用例在两种拓扑下通过同一状态机，且 offer/claim ack 超时、响应丢失、heartbeat 中断与陈旧 replay 均不产生双活 allocation 或陈旧结果混入（见[实施计划](../implementation-plan.md) Milestone 9）。

### 8. 远程副作用业务 fencing 与 Secret/Artifact 边界

- 所有远程副作用使用 `commandId`/`requestDigest`/receipt 与完整 Task/Run/Attempt/allocation/lease 身份；不得以 HTTP 方法的表面幂等（如裸 PUT/DELETE 幂等）替代业务 fencing：receipt 的对账必须校验身份 + `expectedSequence`；
- Secret/Artifact Provider 只交付有界引用（reference/handle）或 workload-scoped 短期能力：secret 明文不得写入 TaskSpec、事件、Prompt、日志或 WorkerResult；有效期以 Attempt 为界，超期必须失效并可从账本追溯。

### 9. DurableExecutionEngine：唯一 Port 名与权威边界

- 统一使用 `DurableExecutionEngine` 作为 Port 名：ADR 0016 决策 §5 的“`DurableOrchestrator` Port”即本 Port，自本 ADR 起更名（见 §13 对照表）；Temporal、Local Engine（embedded/单机 in-process 引擎）与任何未来实现都只是该 Port 的 **backend**；
- **业务语义只在 Core lifecycle policy/controller**：只有它创建新 Attempt、决定 retry eligibility 与消费业务重试预算、决定 rework 与 terminal decision（终态裁决）；
- **backend 只承担四类传输保证**：相同 `commandId` 的 at-least-once delivery（以 `commandId` 幂等）、timer wakeup、signal transport、crash recovery（恢复后向事件账本与 Control Plane 回报状态，不自行决定业务转换）；backend 以 `expectedSequence`/CAS 回报结果，不构成第二个业务权威；
- **delivery/activity retry 不是业务 retry**：对同一 `commandId` 的投递重试（含 backend 的 activity retry）不创建新 Attempt、不消费业务重试预算；文档中所有指向 backend 行为的“retry”一律指 delivery/activity retry；业务预算耗尽时由 Core 裁决终态，backend 不得自行延长执行；
- 替换 backend 不改变生命周期语义：必须通过同一生命周期一致性测试；Core 不演变成自研 workflow engine。

### 10. 生产形态与 Wire Contract

生产形态（冻结）：

- 生产终态采用 C/S：Marshal Control Plane 运行于常驻 `marshal-server` 进程；Execution Plane（SandboxProvider/AgentAdapter workload）与 Control Plane 分离，可远程部署；
- 单二进制 embedded/local 模式长期保留：本地 CLI 单次编排与 M8 embedded 纵切在同一 Core 上 in-process 运行，与 C/S 形态行为一致；embedded 与 server 模式共享同一生命周期守卫与证据规则；
- CLI、Web Dashboard、GitHub App、CI 一律是 Public API client，经同一 TaskSubmission/Run Public API 接入；不得绕过 Public API 直接读写业务状态；embedded CLI 经 in-process adapter 调同一 Public application Port，不直写 store；server client 经 HTTP transport；两种形态共用同一应用 Port，不允许第二条写路径；
- Core 不退化成任意插件 HTTP Server：插件的外部表面只有版本化 Provider Protocol（认证注册 + conformance 准入）；Core 不向插件暴露业务状态写 API。

Wire contract（M9 首版冻结）：

- Public API 采用 versioned HTTP/JSON 并提供 OpenAPI 定义；表面覆盖：Task create/get/cancel、Run approval/status、events/evidence 读取；
- 事件基线采用 SSE，支持 `eventId`/cursor 断线续传；轮询作为 fallback；WebSocket/gRPC 等其他形态推迟，不属于 M9–M12 承诺，引入须另行 ADR；
- Provider remote transport 同样是 versioned HTTP/JSON：Push 拓扑由 `marshal-server` 调用 Provider endpoint；Pull 拓扑由 Provider runner 以 outbound-only long polling 或 streaming 领取同一 DispatchLease（第 7 节），不得要求 inbound 监听；
- 身份与授权分工：M9 必须提供最小、scope-bound、可撤销的 Provider/workload 注册身份（即使入口仅 loopback/trusted boundary）；M11 扩展的是生产远程入口、operator/API caller、多节点与多用户 AuthN/AuthZ；
- M12 基于本节冻结的 wire contract 与 OpenAPI 定义交付多语言 SDK、部署文档与多拓扑 conformance——wire contract 在 M9 首次冻结，而不是到 M12 才首次定义。

### 11. Canonical JSON 与重复 member 拒绝

- 所有 digest、replay key、`requestDigest`、`evidenceDigest` 与需要规范化的契约对象，统一使用 RFC 8785 JCS 序列化，复用仓库既有 JCS 实现基线（Milestone 1 交付，见 milestone-1-report）；
- 重复 JSON member 必须拒绝：解析 Provider 注册请求、Stage/StageReceipt、ConformanceEvidence、Public API 请求与一切 `v1alpha1` 协议对象时，遇到重复 member name 一律报错拒绝，不得采用“后出现优先”或“先出现优先”。

### 12. 修订后的 M8–M13 分工

取代 ADR 0016 决策 §9 的路线措辞（细节以[实施计划](../implementation-plan.md)为准）：

1. **M8** Sandbox SPI/Fake/Local conformance + **embedded/local 纵切**：单二进制 embedded 模式 in-process 跑通幂等提交（loopback/受信任本地边界）→ 冻结 Run → durable `READY` → claim + fencing → Local SandboxProvider → AgentAdapter → checkpoint/log/evidence → 独立 Verifier sandbox（业务验证 workload）→ `REVIEW_PENDING`/`ACCEPTED`（暂不自动 publish）；本 ADR 的二维模型、ConformanceEvidence、内容寻址 Stage、身份 fencing 与 replacement Restore 随 M8 的 SPI 与 conformance 套件一起实现；conformance 套件按第 2 节证据拓扑执行——probe workload 运行在被测 Provider 的 target allocation 内，裁决与证据由 Control Plane 与 Conformance Verifier 签发，Provider 的 completed/receipt 只是输入；
2. **M9** `marshal-server`、Public API 与 Durable Runtime：冻结 `marshal-server` 常驻形态；TaskSubmission/Run Public API 采用 versioned HTTP/JSON + OpenAPI（Task create/get/cancel、Run approval/status、events/evidence；SSE `eventId`/cursor 断线续传 + 轮询 fallback；WebSocket/gRPC 推迟）；embedded 兼容（同一 Core 可在 in-process/service 之间切换）；Push/Pull DispatchLease 按第 7 节唯一状态机实现，并交付两拓扑等价 conformance 与故障注入口径；提供最小、scope-bound、可撤销的 Provider/workload 注册身份；完成 inbox/outbox、dispatcher、kill/restart recovery 与 `DurableExecutionEngine` 接入（backend 只承担 at-least-once delivery/timer/signal/crash recovery，delivery/activity retry 不创建 Attempt、不消费业务预算）；
3. **M10** Cloudflare remote transport：CloudflareSandboxProvider 与 Bridge Go client（versioned HTTP/JSON），准入仍走同一 ConformanceEvidence；
4. **M11** 生产级存储、多节点 HA、AuthN/AuthZ 与身份分离：在 M9 注册身份基础上扩展生产远程入口、operator/API caller、多节点与多用户的 AuthN/AuthZ，承接生产远程入口验收；
5. **M12** 多语言 SDK、部署文档、多拓扑 conformance 与长稳验证：基于 M9 冻结的 wire contract 与 OpenAPI 定义交付多语言 SDK 与部署文档；多拓扑 conformance（embedded、单节点 server、Push、Pull runner 跑同一 conformance 套件）；版本化 Provider SDK/协议必须覆盖六类 Provider：Agent、Sandbox、Verification workload executor、SCM/Publisher transport、Artifact、Secret；Verification/SCM/Publisher Provider 只能执行或传输，最终 gate、ReviewDecision 与发布权限判断仍在 Core；
6. **M13** Goal orchestration：以 Goal API/控制器实现 ADR 0016 冻结的 Project/Goal 对象语义。

### 13. 与 ADR 0016 的关系（supersede/clarify 对照）

| ADR 0016 段落 | 原表述 | 本 ADR 处置 |
| --- | --- | --- |
| 决策 §4（分层冻结）“Run 只冻结最低 `SandboxRequirements`”及 Runtime 架构绑定规则“最低 Execution Profile” | 以单一 executionProfile 表达环境要求 | **取代**：`SandboxRequirements` 改用 `accessMode` + `minimumAssuranceLevel` 二维要求；旧 profile 按第 1 节映射表确定性映射 |
| 决策 §7（Cloudflare）“只有 conformance 证明…才允许声明 hardened” | conformance 证明未规定证据形态、证据拓扑、签发主体与时效 | **澄清**：`hardened` 必须绑定密封 ConformanceEvidence；probe workload 运行在被测 Provider 的 target allocation 内，裁决与签发由 Control Plane 与独立 Conformance Verifier 控制，含逐维结果与有效期/撤销语义（第 2 节） |
| 决策 §6（恢复/fencing/checkpoint） | Restore 未定义双写语义；重放身份未绑定 allocation/角色；DispatchLease 未冻结 Push/Pull 共用状态机 | **澄清并部分取代**：Restore 默认 replacement allocation（第 5 节）；操作身份与 replay key 完整元组绑定，workloadRole 封闭枚举为 worker/verifier，普通 replay 先过 lease fencing（第 4 节）；冻结 Push/Pull 共用的唯一 DispatchLease 状态机（第 7 节） |
| 决策 §5（权威状态与外部调度边界）Port 名为 `DurableOrchestrator`，“生产参考实现为 Temporal” | Port 命名与 backend 语义不统一，retry 归属不明，可能形成双权威 | **部分取代**：Port 统一更名 `DurableExecutionEngine`；Temporal/Local Engine 只是 backend，不拥有 lifecycle/retry/rework 业务语义；Attempt 创建、retry eligibility/预算、rework 与终态裁决只在 Core；delivery/activity retry 不创建 Attempt、不消费业务预算（第 9 节） |
| 决策 §9（路线冻结）“M8 常驻单节点纵切；M9 Durable Runtime（submit API…）” | M8 把常驻单节点与 SPI 混在一起，M9 未冻结服务形态、wire contract 与 Public API | **取代**：M8 为 embedded/local 纵切；M9 冻结 marshal-server、versioned HTTP/JSON + OpenAPI Public API、SSE 事件基线、embedded 兼容与 Push/Pull DispatchLease；M11/M12 的身份与 SDK 分工见第 10、12 节 |

本 ADR 生效后，上述对象以上表所列口径为准；ADR 0016 其余决策（长期目标、核心对象模型、提交入口幂等边界、fail closed 与不变量集合）继续有效，不与本 ADR 冲突。ADR 0016 全文保持历史原样，遇措辞冲突以本对照表为准。

## 保留的不变量

ADR 0016 冻结的不变量集合全部保留：Worker 不自证；单 workspace/attempt 写入者；Worker/Verifier/Publisher 分权；ReviewDecision 精确绑定 evidence；失败或阻塞保存 Outcome；副作用 intent-first + receipt + reconcile；能力不足 fail closed；Merge 默认禁用；`.marshal/` 不进入业务提交。本 ADR 追加并强化：普通宿主进程永不 hardened；hardened 必须持有有效 ConformanceEvidence（probe 运行在被测 Provider 的 target allocation 内，证据由 Control Plane 与 Conformance Verifier 独立签发）；Stage 不认回显 digest；Restore 不允许双写；协议解析拒绝重复 JSON member；Sandbox workloadRole 封闭枚举仅 worker/verifier，Publisher 永不成为 Sandbox workload；Push/Pull 共用唯一 DispatchLease 状态机；delivery/activity retry 不创建 Attempt、不消费业务重试预算。

## 后果

- Local 与 Cloudflare 实现获得不可歧义的共同契约；conformance 从行为探测升级为“证据准入”（逐维结果 + 证据密封 + 有效期/撤销），且证据拓扑不再混淆业务验证与 Provider 测试；
- 实施义务：M8 交付二维要求 Schema 与映射校验、ConformanceEvidence 签发链（第 2 节拓扑）、Stage 重算 fixture、workloadRole/principal 拆分与身份元组校验、replacement Restore 故障注入；M9 冻结 marshal-server、versioned wire contract 与 Push/Pull 拓扑等价；各步拆解见[审计报告](../audit-report.md)增补节；
- 兼容义务：旧 `executionProfile` 持久记录不重写；Local MVP 行为不回归；
- 本 ADR 已于 2026-08-10 经维护者接受（全部 P1 通过 Round 2 独立验证与 ReviewDecision accept）。**接受只关闭设计歧义**：M8 实现与 conformance 状态不因此提前，首次 Sandbox SPI dogfood 的既有实现成果按未接纳探索证据对待，不计为 M8 进度；后续实现若偏离本 ADR，须重开本 ADR 相关章节或以新 ADR 取代。

## 备选（已否决）

- 继续单一 executionProfile，用特例字段表达 read-only+hardened：否决。正交组合是真实需求，特例字段会组合爆炸并引入歧义；
- Provider 自报 Enforcement + 抽查测试给予 hardened：否决。自报即 Worker 自证的变体，违反 ADR 0004 精神；抽查不构成耐久证据且无撤销语义；
- conformance probe workload 运行在独立于被测 Provider 的 Verifier sandbox：否决。该拓扑只能测到 Verifier sandbox，测不到被测 Provider 的 mount/network/resource/credential 强制能力，等于没有证明任何东西；probe 必须运行在被测 Provider 的 target allocation 内，裁决与签发保持独立（第 2 节）；
- Stage 信任声明 digest（回显即可）：否决。内容寻址是 checkpoint/artifact 一致性的前提，回显无法发现篡改与损坏；
- in-place Restore 后允许旧进程继续写（乐观恢复）：否决。双写直接破坏单写者不变量与证据权威；
- role 枚举扩到 publisher/control-plane，用同一身份维度表达调用者与 workload：否决。Publisher 位于独立信任域且永不进入 Sandbox workload；混合枚举让 Provider 可借通用 role 取得跨 Port 能力（第 4 节）；
- 为 Push 单独定义简化的分发协议（fire-and-forget）：否决。任何拓扑间语义差异都会引入不同的失败模式与双活 allocation 风险；唯一状态机 + 拓扑等价 conformance 是唯一可证明等价的口径（第 7 节）；
- 保留 DurableOrchestrator 与 DurableExecutionEngine 双命名，或允许外部引擎创建 Attempt/消费业务重试预算：否决。双命名与双权威语义是同一风险的两面（第 9 节）；
- 把 versioned HTTP/JSON + OpenAPI wire contract 推迟到 M12 才首次定义：否决。M9 服务化没有 wire contract 就无法验收；SDK 是 wire contract 的消费者而不是其定义者（第 10 节）；
- 把常驻 marshal-server 提前到 M8 与 SPI 一起做：否决。SPI 契约尚未经证据稳定，再叠加服务形态会让两个验收目标互相纠缠；M8 先用 embedded/local 纵切证明契约本身；
- Core 向插件开放任意 HTTP 业务 API：否决。插件表面只应有版本化 Provider Protocol；扩大 HTTP 表面即扩大信任边界，必须另行 ADR。
