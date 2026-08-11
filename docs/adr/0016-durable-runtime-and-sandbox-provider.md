# ADR 0016：耐久 Runtime 与可插拔 Sandbox Provider

- 状态：已接受（维护者 2026-08-10）
- 日期：2026-08-10
- 决策来源：维护者于 2026-08-10 明确修正并批准长期目标——目标不是让单个 Task 由一个进程运行数月，而是 Runtime 长期稳定运行、持续接受新 Task 并分发；环境与状态可重建、可恢复、可审计；Sandbox 必须可插拔，初期候选为 Cloudflare Sandbox。本 ADR 记录该修正并冻结相应架构与实施路线。
- 取代关系：[ADR 0015](0015-production-deployment.md) 为 Superseded before acceptance（未接受即被取代），其生产部署边界由本 ADR 承接
- 关联：ADR 0001（CLI-first 模块化单体）、ADR 0003（Worker 与 Publisher 分权）、ADR 0004（独立证据权威）、ADR 0007（意图先行发布）；[Runtime 架构](../runtime-architecture.md)；[实施计划](../implementation-plan.md) M7–M13（M7–M12 平台阶段与 M13 Goal 编排）；[云端长程 Agent 能力审计](../research/cloud-agent-readiness-2026.md)

## 背景

Local MVP 已于 2026-08-07 标记 `USABLE`，本地 CLI 单次编排闭环全部通过真实任务验收。但文档体系对长期目标的表述仍停留在“本地单次 CLI 编排”，存在三处已打开的架构问题：

1. `docs/vision-and-scope.md` 的交付阶段止于“加固与路由”，Daemon、远程队列与分布式调度被笼统列入“延后阶段”，长期形态没有冻结的路线；
2. ADR 0015 以提案形式给出“只读观察服务先行、执行代理后置”的生产部署路径，但常驻形态、耐久调度与远程执行的信任边界始终未闭合，提案未被接受；
3. [云端长程 Agent 能力审计](../research/cloud-agent-readiness-2026.md) 已确认：缺口不在生命周期语义，而在耐久控制平面与远程强化执行；Local MVP 的不变量集合是必须原样保留的资产，而不是障碍。

维护者据此在 2026-08-10 修正目标：Marshal 的长期形态是长寿命 Runtime/Control Plane，持续接收、耐久排队、分发和审计大量有界 Task/Run/Attempt；执行沙箱可插拔，Cloudflare Sandbox 仅作为首个可替换远程 Provider。

## 决策

### 1. 长期目标正式重置

Marshal 的长期目标重置为：**长寿命 Runtime/Control Plane 持续接收、耐久排队、分发和审计大量有界 Task/Run/Attempt；环境与状态可重建、可恢复、可审计**。

- 这不是让单个 Task 运行数月：Task/Run 仍是有界冻结工作单元，长周期目标由 Project/Goal 长期对象驱动一系列短 Attempt；
- 本地 CLI 单次编排保留为首个入口形态与开发模式，Local MVP 行为不变、不回归；
- 普通宿主机子进程仍然最多声明 `workspace-write`，不得被描述成恶意代码沙箱。

### 2. 核心对象模型冻结

Runtime 必须区分并持久化以下核心对象（字段级契约随 M7 冻结、随实施落地 Schema）：

- `Project`/`Goal`：长周期目标对象，驱动多个 Run；M7 只冻结对象语义，Goal 控制器在 M13 实现；
- `TaskSubmission`：提交入口对象，绑定唯一 `(repository, project)` scope，携带 `idempotencyKey` 与 `requestDigest`（冻结输入的规范化摘要）；仅 scope+key+digest 三者相同才幂等归并，同 scope+key 而 digest 不同必须冲突 fail closed（见下节）；
- `Task`/`Run`：冻结工作单元，冻结 spec/base/policy/最低环境要求；
- `Attempt`：短命执行，可丢弃；
- `DispatchLease`：携带 `generation`、`fencingToken`、`expiresAt`、`heartbeatAt`；
- `EnvironmentSpec`：以 digest 固定镜像/工具链/mount/network/resource/credential 要求；
- `SandboxAllocation`：只保存 provider-neutral 的 opaque locator 与 receipt，不泄露 Provider 内部结构；
- `CheckpointRecord`/`WorkspaceRef`/`ArtifactRef`：一律以内容摘要绑定；
- `SideEffectIntent`/`Receipt`：统一所有外部副作用，先意图、后执行、重试先对账、歧义 fail closed；
- Queue 是权威状态的投影，用于观测与调度提示，不是第二个业务权威。

### 3. 提交入口与幂等边界冻结

- M8/M9 的提交入口默认只绑定 loopback 或受信任本地边界（本机进程与本机可信调用者），不开放远程入口；
- 远程入口在生产可用前必须具备：TLS 传输、调用者身份认证、按 repository/project 的授权、提交与授权决策的审计记录；该验收口径列入 Milestone 11 退出门禁；
- M11 的身份分离既覆盖 Worker/Verifier/Publisher 的 workload identity，也覆盖操作者与 API 入口身份，两者不得混用；
- 幂等身份是三元组 `(scope, idempotencyKey, requestDigest)`：三者相同返回既有 submission/run，不重复副作用；同 scope+key 而 digest 不同为冲突，fail closed（返回 conflict，写入审计），不得创建或归并错误 Run。

### 4. AgentAdapter 与 SandboxProvider 分层冻结

两个 Port 职责不得混合：

- `AgentAdapter` 只负责 Agent 协议的 prepare/decode/capability：冻结 WorkerRequest、解析 WorkerResult、探测能力；不含执行环境语义；
- `SandboxProvider` 负责执行环境全生命周期：`Probe`/`Provision`/`Stage`/`Exec`/`Inspect`/`Signal`/`Checkpoint`/`Restore`/`Terminate`/`Reconcile`；
- Marshal Core 不得出现 Durable Object、R2、Workers binding 等 Cloudflare 专有概念；Provider 内部实现细节只能通过 `SandboxAllocation` 的 opaque locator 与 receipt 表达；
- Run 只冻结最低 `SandboxRequirements`；实际 Provider 与能力在 Attempt 分配时绑定；兼容 Provider 之间重试无需改变 Run。

### 5. 权威状态与外部调度边界冻结

- Marshal 的 versioned event/state 是业务权威；生命周期转换守卫、冻结语义、证据裁决与发布裁决留在 Marshal Control Plane；
- 耐久调度经由可替换的 `DurableOrchestrator` Port 外包：dispatch/timer/signal/retry 由外部引擎承担，生产参考实现为 Temporal；
- 每个 Activity 以 `commandId` + `expectedSequence` CAS 追加 Marshal 事件；外部引擎的执行记录不直接充当业务证据，避免双权威；
- 单机开发允许 Temporal dev server + SQLite/local blob adapter；生产参考部署为 Temporal self-host + PostgreSQL + S3/MinIO；
- 不得演变成自研 workflow engine；替换 Orchestrator 不得改变生命周期语义，由一致性测试守护。

### 6. 恢复/fencing/checkpoint 语义冻结

- Sandbox、Agent 与 Runtime 进程都是可丢弃的；权威事件、证据与副作用记录必须在其外部耐久保存；
- 恢复只能基于持久事件账本：Inspect/Reconcile 比较 Journal、Snapshot、DispatchLease 与执行体状态后选择合法转换；陈旧 execution handle 必须被 fencing 拒绝，不得双写、不得丢证据；
- 权威写入接纳边界：所有 Attempt 回报与 Artifact/Checkpoint/Candidate/Evidence 接纳必须携带 `attemptId`、`generation`、`fencingToken`，并在权威写入边界以 `expectedSequence`/CAS 校验；携带陈旧 token 的内容隔离留存为诊断材料，不得进入当前 Evidence、Review 或 Publication；外部副作用继续经 SideEffectIntent/Receipt + reconcile 保证幂等；
- 验收口径：在 `RUNNING`/`VERIFYING` 期间 kill -9 Runtime 后，可在 60 秒内完成 Inspect/Reconcile，旧 execution handle 上报被 fencing 拒绝；
- Checkpoint 只是 Attempt 级加速手段：`CheckpointRecord` 必须可验证且 digest 绑定，不能替代持久控制平面；
- 稳定环境不等于复用永生 sandbox。默认每个 Attempt 独立 ephemeral sandbox；稳定性来自 `EnvironmentSpecDigest`、pinned image/toolchain、内容寻址 artifact/cache 与可验证 checkpoint；warm reuse 仅限相同 tenant/repository/trust-domain 且有可证明的 sanitization。

### 7. Cloudflare Sandbox 仅为首个可替换远程 Provider

Cloudflare 事实只用于设计边界（来源为官方公开文档，未经官方记载的能力一律视为未验证）：

- Cloudflare Sandbox 由 Worker + Durable Object + 独立 VM Container 组成；
- 官方 Bridge 是自部署到用户 Cloudflare 账号的 HTTP/OpenAPI Worker，提供 Bearer 令牌认证与 create/running/exec SSE/file/persist/hydrate/destroy 操作；
- 容器在闲置、故障或重启时丢失文件、进程与 session；R2 backup 只作为恢复优化，不是权威状态；
- Sandbox SDK/Bridge 为 Apache-2.0，但托管运行平台不可自部署，且 SDK 仍处于 1.0 preview/Beta。

由此：

- Cloudflare 可以作为首个托管 hardened 候选，但只有 conformance 证明 mount/network/resource/credential 要求均被强制时，才允许声明 `hardened`；证据不足时 fail closed，只按实际验证到的较低 assurance 等级声明，不得放宽声明；
- Provider 失败不触发 Attempt 内回退：失败的 Allocation/Attempt 先终止并对账；调度器仅可为新 Attempt 选择满足同一冻结 `SandboxRequirements` 与 assurance 下限的兼容 Provider；没有兼容 Provider 时 Run 保持 `BLOCKED`（fail closed），绝不静默降低 profile 或复用旧 execution handle；该规则同样适用于 Local/Docker/Kubernetes 等自托管 Provider；
- Local/Docker/Kubernetes 自托管 Provider 必须通过同一 conformance 套件；Provider 可替换性由一致性测试守护，替换 Provider 不改变任务含义、验收标准与证据要求；
- 不把 Cloudflare、Temporal 或任何单一基础设施变成 Core 必选依赖。

### 8. ADR 0015 的处置

ADR 0015（生产部署：常驻服务、远程访问与 Dashboard 认证）标记为 **Superseded before acceptance**：

- 其“生产部署边界”由本 ADR 与 M7–M12 路线承接：常驻形态、提交入口、远程执行与凭证边界在 Runtime 架构中统一定义；
- 其“只读观察先行”不再构成独立前置里程碑：观察能力实现为 Runtime 事件流的只读投影，不先于执行体引入新的信任边界；
- 本 ADR 接受后，ADR 0015 不再作为实施依据。

### 9. M7–M13 实施路线冻结

[实施计划](../implementation-plan.md) 中 M7–M12 是唯一的耐久 Runtime 平台路线，M13 实现本 ADR 冻结的 Project/Goal 语义；每个 Milestone 都有 Goal、明确非目标、退出门禁和 dogfooding 任务：

1. **M7** 架构与契约（本 ADR 与 runtime-architecture.md 即其产出）；
2. **M8** Sandbox SPI/Fake/Local conformance + 常驻单节点纵切；
3. **M9** Durable Runtime（submit API、inbox/outbox、dispatcher、heartbeat/fencing、kill/restart recovery）；
4. **M10** Cloudflare Provider（Bridge Go client、SSE、hydrate/persist/destroy、live opt-in）；
5. **M11** PostgreSQL/S3/MinIO 与多节点 HA、安全/质量身份分离，并承接生产远程入口（TLS/调用者认证/按 repository/project 授权/审计）验收；
6. **M12** 开源部署、插件 SDK、并发/公平、72h 后 7d soak/chaos；
7. **M13** Goal orchestration：持久 Project/Goal、可审计计划与重规划、跨 Run 记忆/Artifact 引用、预算与终止条件、独立质量评估、人工干预与恢复；M7 只冻结对象语义，M13 实现 Goal 控制器。

## 保留的不变量

以下不变量不因 Runtime 化放宽，且在云端形态下继续作为验收标准：

- Worker 不自证：Worker 的任何声明不构成权威证据；
- Run 冻结 spec/base/policy/最低环境要求，修改冻结项创建新 Run；
- 单 workspace/attempt 写入者：一个任务工作区同时最多一个写入者；
- Worker/Verifier/Publisher 分权，凭据与 merge 权限位于 Worker 信任边界之外；
- ReviewDecision 精确绑定 evidence；陈旧证据不能驱动发布；
- 失败或阻塞必须保存 Outcome，不创建虚假 PR；
- 副作用 intent-first + receipt + reconcile，重试先对账，歧义 fail closed；
- 能力不足时 fail closed，不静默放宽门禁；
- 普通宿主进程不宣称恶意代码隔离；
- Merge 默认禁用；
- `.marshal/` 不进入业务提交。

## 后果

- 接受后：M8–M13 获得实施路线（M8–M12 为平台阶段，M13 为 Goal 编排）；新增 Schema、Port 与状态扩展随各 Milestone 落地并同步契约测试；
- 文档口径统一：长期目标、状态与术语以本 ADR、`docs/runtime-architecture.md` 与实施计划为唯一来源；
- 义务：每个 Milestone 必须先通过 Local MVP 回归；conformance 套件成为 Provider/Adapter 的唯一准入手段；
- Cloudflare 依赖风险（SDK Beta、托管平台不可自部署）以 live opt-in + fail-closed 控制，不构成 Core 必选依赖。

## 备选（已否决）

- **沿 ADR 0015 演进（只读观察先行，远程执行另议）**：否决。观察与执行的信任边界无法分开闭合，且与“Runtime 长期稳定运行”的修正目标不一致；
- **自研 workflow engine 或自研分布式调度器**：否决。事件溯源调度器自建成本高，且违反“不引入第二个业务权威”原则；
- **Cloudflare 单一绑定（Core 直接依赖 Durable Object/R2）**：否决。破坏 Provider 可替换性，违反 ADR 0002/0004 式的 Port 纪律；
- **复用永生 sandbox 换取环境稳定**：否决。永生会话是状态载体幻觉；稳定性必须由 digest、pinned 环境与内容寻址缓存提供，warm reuse 只能作为有 sanitization 证明的优化；
- **让单 Task 进程运行数月**：否决。Attempt 必须有界可丢弃，长周期由 Project/Goal 驱动多个短 Attempt。
