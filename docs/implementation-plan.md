# 实施计划

## 门禁

本文 Local MVP 部分（Milestone 0–6）已由维护者授权实施；M7–M13 部分于 2026-08-10 随 [ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md) 接受而授权（M7–M12 为耐久 Runtime 平台阶段，M13 为 Goal 编排阶段）。M8–M13 的分工措辞随 [ADR 0017](adr/0017-provider-neutral-sandbox-contract.md)（已接受，2026-08-10）修订：M8–M13 一律按其第 12 节的分工与 provider-neutral Sandbox 安全契约执行。**ADR 0017 的接受只关闭设计歧义**：M8 实现与 conformance 状态不因此提前，首次 Sandbox SPI dogfood 的既有成果按未接纳探索证据对待，各 Milestone 状态以 [Roadmap 状态](roadmap-status.md) 为准。本文只是实施计划：信任边界、持久化契约、生命周期或发布权限的改变仍必须先新增或替代 ADR。

当前状态：Milestone 0–6 已全部通过，Local MVP 标记 `USABLE`（2026-08-07），验收证据见 [Roadmap 状态](roadmap-status.md)；M7–M12 目标架构见 [Runtime 架构](runtime-architecture.md)。

## 交付策略

在接入真实 Coding Agent 前，先用确定性 Fake Adapter 打通最小端到端链路。每个 Milestone 都必须保持仓库自身检查通过，并不得提前加入后续阶段的外部副作用。

## Milestone 0：Toolchain 与 Contract

交付：

- 锁定具体 Go 版本；
- 提交 `go.mod` 与 `go.sum`；
- 建立清晰的 Core、Port、Adapter 与 CLI Package 边界；
- `gofmt`、`go vet`、静态检查、Test 与 Build Command；
- Schema Compile 与正反 Contract Fixture；
- 生成或受契约测试保护的 Go 内部类型；
- 不执行 Worker/Publication 副作用的 CLI Skeleton。

退出条件：

- 所有 Schema 通过 Draft 2020-12 校验；
- Fixture 覆盖 Format 与 Semantic Validator；
- Clean Module Download、Format、Vet、Lint、Unit Test 与 Build 通过；
- 产出可在受支持平台运行的单一 `marshal` 可执行文件；
- Core Domain 不依赖 Provider-specific Module。

## Milestone 1：State Machine 与 Run Store

交付：

- Task/Run/Attempt ID；
- 带守卫的 Lifecycle Reducer；
- 带 Sequence 的 Append-only Event Journal；
- Atomic State Snapshot；
- File Lock 与 Lease；
- Canonical JSON 与 Digest Utility；
- 仓库本地 `.marshal/` 初始化、默认 Git 排除规则与显式 `MARSHAL_STATE_DIR` 覆盖；
- `marshal task status` 与只读 Inspection；
- 使用 Transcript Fixture 的 Fake Adapter。

退出条件：

- Table-driven Test 覆盖全部合法和非法 Transition；
- Stale Sequence Write 与 Duplicate Event 默认失败；
- Crash Fixture 能从截断 Journal 重建 State；
- Frozen Input 变化创建新 Run，而不是修改 `READY`；
- `.marshal/` 内容不会出现在业务 Diff 或 Commit Tree 中。

## Milestone 2：Git Worktree 与独立 Verification

交付：

- Repository Identity 与 base SHA Resolution；
- 在 `.marshal/worktrees/<task-id>/` 创建带 Repository/Task Lock 的 linked Worktree/Branch；
- 包含 Untracked File 与 Rename 的真实 Diff；
- Path、Glob、Symlink 与 Submodule Gate；
- 有 Time/Output Limit 的 Direct-spawn Verification Command；
- Artifact Collection 与 Hash；
- VerificationReport 与 ArtifactManifest。

退出条件：

- Fixture Run 不修改主 Checkout；
- macOS 与 Linux Fixture 验证嵌套 linked worktree 的创建、发现、锁定和清理，并覆盖 macOS `/var` 与 `/private/var` 路径别名；不支持时必须安全 Block 或使用显式状态目录覆盖；
- Path Traversal、Symlink Escape、Forbidden Rename 与 Oversized Diff 失败；
- Worker Declaration 不能改变真实 Gate；
- Cancellation 终止 Verification Process Tree；
- Verifier 产生的 Dirty Output 被发现。

## Milestone 3：Review 与 Rework Loop

交付：

- 带版本 Worker Prompt Renderer；
- Bounded ReviewPacket Export；
- Schema-valid ReviewDecision Import；
- Stale Evidence Rejection；
- Rework 与 Operational Retry Budget；
- No-change 与其他终态；
- Outcome Bundle 与人类可读摘要。
- 可安装到 `.agents/skills/marshal/` 的轻量 Codex Skill 与中文使用文档。

首个 Review Bridge 可以是 File-based：Marshal 输出 Packet，Codex CLI 或 Codex Desktop 生成并交回 Decision。后续 Native Codex Integration 必须保留相同文件契约。

退出条件：

- Accept 不能绕过 Required Failed Gate；
- Stale Decision 与 Invalid Prose 不改变 State；
- Finding 跨 Rework 保留，并只能由新 Evidence 关闭；
- Budget Exhaustion 产生正确终态。
- Skill 的显式与隐式触发 Fixture 能生成合法 TaskSpec/ReviewDecision，且不会绕过 Marshal Core 直接启动 Worker 或发布。

## Milestone 4：首个真实 Worker Adapter

选择首个 Adapter 前，对已安装 Qwen、OpenCode 与 Pi 做有界 Spike，仅评估 Structured Output、Non-interactive Edit、Cancellation、Session Identity、Permission Enforcement 与 Transcript Stability，不根据主观 Coding Quality 选择。

交付一个 Adapter 及：

- 精确 Executable Resolution 与 CapabilitySnapshot；
- Argv Spawn 与过滤环境；
- Normalized Event；
- Output Limit 与 Cancellation；
- WorkerResult Parsing；
- Adapter Conformance Fixture。

退出条件：

- Fake 与 Real Adapter 通过相同 Lifecycle Fixture；
- Unsupported Capability 在 Worker 启动前失败；
- 不回退到同名或近似 Executable；
- Worker Environment Test 中没有 Publisher Credential。

## Milestone 5：GitHub Draft Publisher

交付：

- Accepted Snapshot Recheck；
- 不使用 Ambient User Hook 的 Controlled Commit；
- Branch Derivation 与 Collision Check；
- 使用独立 Credential Profile 的 GitHub Publisher；
- 幂等创建/更新单个 Draft PR；
- Publication Artifact 与 Remote CI Observation；
- Ambiguous Timeout Reconciliation。

退出条件：

- Review 后改变 Snapshot 就不能发布；
- 模拟 Push/PR Timeout 后重试不会重复创建；
- Unexpected Remote Head 导致 Block，而不是 Force Push；
- Worker 从未获得 GitHub Credential；
- Merge 功能不可用。

## Milestone 6：其余 Adapter 与 Recovery 加固

交付：

- 其余 Qwen/OpenCode/Pi Adapter；
- 受支持的 Session Resume；
- `marshal doctor` Reconciliation；
- Archive 与 Cleanup Preview；
- 来自 Conformance Test 的 Compatibility Matrix；
- Operator Documentation。

退出条件：

- 全部受支持 Adapter 通过共享 Conformance Test；
- 文档中的 Crash Point 能幂等恢复或安全 Block；
- Dirty Worktree 未归档且未显式授权时不能删除；
- Unknown Worker Version 被拒绝或清楚标记 Experimental。

## Milestone 7–13：耐久 Runtime、可插拔 Sandbox Provider 与 Goal 编排

M7–M12 是基于 [ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md) 的唯一耐久 Runtime 平台路线，M13 实现 ADR 0016 冻结的 Project/Goal 语义；目标架构见 [Runtime 架构](runtime-architecture.md)。[ADR 0017](adr/0017-provider-neutral-sandbox-contract.md)（已接受，2026-08-10）澄清并部分取代 ADR 0016 的 §4/§5/§6/§7/§9，冻结 provider-neutral Sandbox 安全契约与修订后的 M8–M13 分工，接受后即为本路线的执行口径（接受只关闭设计歧义，不提前升级任何 Milestone 的实现状态）。每个 Milestone 必须先通过 Local MVP 全量回归，且不得提前引入后续阶段的外部副作用。

### Milestone 7：架构与契约

Goal：冻结耐久 Runtime 与可插拔 Sandbox Provider 的架构与契约——ADR 0016 与 [ADR 0017](adr/0017-provider-neutral-sandbox-contract.md)、本架构文档、核心对象模型（TaskSubmission/DispatchLease/EnvironmentSpec/SandboxAllocation/CheckpointRecord/SideEffectIntent 等，含 Project/Goal 的对象语义）与 Port 契约（`AgentAdapter` prepare/decode/capability；`SandboxProvider` Probe/Provision/Stage/Exec/Inspect/Signal/Checkpoint/Restore/Terminate/Reconcile；`DurableExecutionEngine` Port，ADR 0017 §9 统一命名）。M7 只冻结 Project/Goal 的对象语义，不实现 Goal 控制器（见 Milestone 13）。

非目标：不实现 Runtime、数据库、Temporal、SandboxProvider、Cloudflare 客户端或 Goal 控制器；不更改 Local MVP 行为。

退出门禁：

- ADR 0016 已接受，ADR 0015 标记 Superseded before acceptance；
- 治理、愿景、架构、安全模型、路线图与审计文档口径一致，术语与状态名与 Schema 对齐；
- Local MVP 回归全部通过，本仓库 CI 全绿。

Dogfooding：本 Milestone 产出本身作为一次完整 Marshal Task 经 Local MVP 闭环（Frozen TaskSpec → 独立 Verification → Review → Draft PR）交付，验证文档类任务的证据链。

### Milestone 8：Sandbox SPI、Fake/Local conformance 与 embedded/local 纵切

Goal：实现 SandboxProvider SPI 与首个 conformance 套件（Fake 与 Local Provider 通过同一套件），并同步落地 ADR 0017 冻结的 provider-neutral 契约：`AccessMode × AssuranceLevel` 二维要求与旧 `executionProfile` 兼容映射、`ConformanceEvidence` 签发链（按 §2 证据拓扑：probe 定义/challenge/artifact digest/调度/out-of-band 观察/裁决/签发由 Control Plane 与独立 Conformance Verifier 控制，probe workload 作为敌对测试负载运行在被测 Provider 创建、身份精确绑定的 target allocation 内，Provider 的 completed/receipt 不能自签通过）、内容寻址 Stage（inline/ArtifactStore locator、消费前后重算 sha256、篡改 fixture）、workloadRole/principal 拆分与完整身份元组、replay key 校验、replacement allocation Restore 故障注入。纵切交付 **embedded/local 形态**：单二进制 embedded 模式 in-process 跑通幂等提交（loopback/受信任本地边界）→ 冻结 Run → durable `READY` → scheduler claim + fencing → Local SandboxProvider → AgentAdapter → checkpoint/log/evidence → 独立 Verifier sandbox（业务验证 workload，与 conformance probe 拓扑不同）→ `REVIEW_PENDING`/`ACCEPTED`（暂不自动 publish）。`marshal-server` 与 Public API 属于 M9，不在本 Milestone。

非目标：不实现 `marshal-server` 与 TaskSubmission/Run Public API；不接入任何远程 Provider；不引入多节点；不改变既有 CLI 生命周期的外部行为。

退出门禁：

- Fake 与 Local Provider 通过同一 conformance/E2E；Local Provider 永不声明 `hardened`，请求 hardened 时 fail closed 或改由持有效 `ConformanceEvidence` 的 Provider 承接；
- conformance 证据拓扑：probe workload 运行在被测 Provider 创建、身份精确绑定的 target allocation 内；probe 定义/challenge/nonce、probe artifact digest、调度、out-of-band 观察、裁决与证据签发由 Control Plane 与独立 Conformance Verifier 控制；“Provider 凭自身 completed/receipt 自签通过”的 fixture 必须失败；
- 二维要求校验：旧 `executionProfile` 三个取值按映射表确定性解析；AssuranceLevel 无法满足时 Run 保持 `BLOCKED`，无静默降级；AccessMode 升级请求被拒绝；
- Stage 内容寻址：篡改 bytes fixture 让回显型实现失败；inline 超限对象被拒绝并要求 locator；
- 身份 fencing：workloadRole 封闭枚举仅 `worker`/`verifier`，Publisher 作为 Sandbox workload、Provider 借通用 role 跨 Port 取得能力的 fixture 全部失败；身份元组缺失/不匹配的操作被拒绝；陈旧 generation/fencingToken 的 replay 被隔离为诊断材料；Restore 响应丢失、并发 Restore 与恢复后陈旧 handle 写入的故障注入全部被拒绝，无双写；
- 纵切全链路通过，且 Worker/Verifier/Publisher 分权、Worker 不自证、单写入者不变量在 embedded 形态下有测试证明；
- 提交入口默认只绑定 loopback 或受信任本地边界；未经授权的提交与跨 scope 提交被拒绝并记录审计；
- 能力不足时 fail closed 的 Fixture 通过；
- Local MVP CLI 回归零回退。

Dogfooding：用 embedded/local 形态承接本仓库真实的文档/修复类任务，替代一次性 CLI 编排，统计重复提交的幂等归并与失败证据留存。

### Milestone 9：marshal-server、Public API 与 Durable Runtime

Goal：交付常驻服务形态与耐久 Runtime 主体——`marshal-server`（C/S 形态的 Marshal Control Plane 进程）；TaskSubmission/Run Public API 采用 versioned HTTP/JSON + OpenAPI（覆盖 Task create/get/cancel、Run approval/status、events/evidence；幂等 TaskSubmission，幂等身份为 `(scope, idempotencyKey, requestDigest)`；入口默认仅绑定 loopback 或受信任本地边界），事件基线采用支持 `eventId`/cursor 断线续传的 SSE（轮询 fallback；WebSocket/gRPC 推迟）；embedded 兼容（同一 Core 可在 in-process/service 之间切换，行为一致）；embedded CLI 经 in-process adapter 调同一 Public application Port，不直写 store，server client 经 HTTP transport；Push/Pull DispatchLease 按同一唯一状态机实现（只改变连接发起方：Push 由 `marshal-server` 调用 Provider endpoint，Pull 由 Provider runner 以 outbound-only long polling 或 streaming 领取）；两种拓扑都必须绑定认证 Provider registration、CapabilitySnapshot/ConformanceEvidence digest 与 task/run/attempt/allocation、generation/fencingToken，都具备 capability matching、offer/claim-or-delivery ack deadline、heartbeat、expiry、cancel、reconcile、generation bump 与陈旧结果隔离；Push 同样先 capability match，超时/响应丢失不得产生第二个 active allocation；禁止匿名 Pull；Provider remote transport 同样为 versioned HTTP/JSON；提供最小、scope-bound、可撤销的 Provider/workload 注册身份（即使入口仅 loopback/trusted boundary）；inbox/outbox、dispatcher、heartbeat/fencing、kill/restart recovery；`DurableExecutionEngine` Port 接入 backend（生产参考 Temporal，embedded/单机为 Local Engine；backend 只承担相同 commandId 的 at-least-once delivery、timer wakeup、signal transport 与 crash recovery，不拥有 lifecycle/retry/rework 业务语义，delivery/activity retry 不创建 Attempt、不消费业务重试预算；单机开发允许 dev server + SQLite/local blob adapter）。CLI、Web、GitHub App、CI 一律按 Public API client 对待，不得绕过 Public API 直接读写业务状态。

非目标：不接入 Cloudflare；不做多节点 HA；不自研 workflow engine；不开放远程提交入口；不把 Core 开放成任意插件 HTTP Server（插件表面只有版本化 Provider Protocol）。

退出门禁：

- M8 的 embedded/local 纵切用例在 `marshal-server` + Public API 形态下原样通过，embedded 模式同时保持兼容；embedded CLI 经 in-process adapter 调同一 Public application Port，无第二条写路径；
- Versioned HTTP/JSON + OpenAPI 的 TaskSubmission/Run Public API（Task create/get/cancel、Run approval/status、events/evidence）可用；SSE 支持 `eventId`/cursor 断线续传且轮询 fallback 可用；
- Push/Pull 两拓扑等价 conformance 与故障注入通过：同一用例在两种拓扑下满足 capability matching、ack/heartbeat/deadline/generation/fencing 与陈旧结果隔离；匿名 Pull、能力不匹配的 Push offer/claim 被拒绝并记录；offer/claim ack 超时、响应丢失、heartbeat 中断与并发竞争均不产生第二个 active allocation；
- 最小、scope-bound、可撤销的 Provider/workload 注册身份可用：未注册或已撤销身份的请求被拒绝并记录；
- 在 `RUNNING`/`VERIFYING` 期间 kill -9 Runtime 后，可在 60 秒内完成 Inspect/Reconcile；旧 execution handle 上报被 fencing 拒绝；无双写、无丢证据；
- 幂等提交语义：同 scope+key+digest 返回既有 submission/run 且不重复副作用；同 scope+key 而 digest 不同返回冲突（fail closed），不创建、不归并错误 Run，并写入审计记录；
- 权威写入接纳边界故障注入：失联旧 Attempt 携带陈旧 `generation`/`fencingToken` 晚到上传 checkpoint/candidate/日志/证据引用时，被 expectedSequence/CAS 拒绝并隔离为诊断材料，不进入当前 Attempt 的 Evidence/Review/Publication；旧 execution handle 的外部副作用回放经 SideEffectIntent/Receipt + reconcile 对账，不产生重复副作用；
- 每个 Activity 以 `commandId` + `expectedSequence` CAS 追加 Marshal 事件，账本重放不产生第二条业务事实；
- 恢复结论可仅凭持久事件账本得出；故障注入测试集全过；
- Queue 只能以状态投影实现，任何“队列权威”测试失败。

Dogfooding：Marshal 自身的回归与审计任务全部经 Durable Runtime 调度执行；故障注入成为常规测试集并在每次发布前运行。

### Milestone 10：Cloudflare Provider

Goal：实现 CloudflareSandboxProvider——官方 Bridge 的 Go 客户端（自部署到用户 Cloudflare 账号的 HTTP/OpenAPI Worker）、Bearer 令牌认证的凭据管理、exec SSE、file/persist/hydrate/destroy 到 SandboxProvider SPI 的映射，以及 live opt-in 开关。

非目标：不默认声明 `hardened`；不把 Cloudflare 变成 Core 必选依赖；不改变生命周期语义。

退出门禁：

- 仅替换 Provider 后，M8/M9 的同一 conformance/E2E 全部通过（TaskSpec 与用例不变）；
- 只有持有独立签发的有效 `ConformanceEvidence`（mount/network/resource/credential 逐维结果全部 `passed`）时才允许声明 `hardened`，否则只按实际验证到的较低 assurance 等级声明，不放宽声明；Cloudflare 与 Local 走同一证据准入，无豁免；
- Provider 失败语义故障注入通过：Cloudflare 探测失败、行为漂移或容器丢失状态时，当前 Allocation/Attempt 先终止并对账（fail closed）；调度器仅为新 Attempt 选择满足同一冻结 `SandboxRequirements` 与 assurance 下限的兼容 Provider；无兼容 Provider 时 Run 保持 `BLOCKED`，不静默降低 profile、不复用旧 execution handle、不在同一 Attempt 内透明降级；
- Bridge SDK 处于 1.0 preview/Beta 的风险被显式记录并以上述 fail closed 语义控制；
- Provider 凭据不进入 TaskSpec、事件、Prompt、日志或 Worker 可见环境。

Dogfooding：将 M8/M9 的 dogfooding 任务集原样切换到 CloudflareSandboxProvider 重跑，对比 Local 与 Cloudflare 的证据一致性。

### Milestone 11：生产级存储、多节点 HA 与身份分离

Goal：生产参考部署落地——Temporal self-host + PostgreSQL + S3/MinIO；多节点 HA；在 M9 已交付的最小 scope-bound 注册身份基础上，扩展生产级 AuthN/AuthZ——身份分离同时覆盖 Worker/Verifier/Publisher 的 workload identity 与写入域分离（candidate/evidence/publication 分域），以及操作者/API 提交入口身份（区分人操作者与 API 调用者，多节点与多用户）；生产远程提交入口（TLS、调用者身份认证、按 repository/project 授权、审计）；远程管道的 secret scan 与日志脱敏。

非目标：不承诺多租户服务化；不引入 auto-merge。

退出门禁：

- 多节点故障转移后无静默状态漂移，恢复仍满足 M9 的 fencing/60 秒口径；
- 生产远程入口验收：TLS 传输、调用者身份认证（人操作者与 API 调用者区分）、按 repository/project 的授权与提交/授权决策审计全部启用；未认证、越权、跨 scope 提交与跨 Port 能力请求全部被拒绝并记录；
- Worker 环境获取 Publisher 凭据的探测次数为 0；重试无重复副作用；
- 日志抽样无敏感值；写入域越权 Fixture 全部失败；
- 升级外部组件（Orchestrator/数据库/对象存储）不破坏生命周期一致性测试。

Dogfooding：Marshal 自身的发布、审计与长跑任务在生产形态存储上运行，产出可归因的预算与恢复统计。

### Milestone 12：开源部署、版本化 Provider SDK/协议、多语言 SDK 与长稳验证

Goal：开源部署形态与文档；基于 M9 已冻结的 versioned HTTP/JSON + OpenAPI wire contract 交付多语言 SDK 与 OpenAPI 资产（wire contract 与事件流已在 M9 首次冻结，M12 是其 SDK/文档化，不是首次定义）；版本化 Provider SDK/协议必须覆盖六类 Provider：Agent、Sandbox、Verification workload executor、SCM/Publisher transport、Artifact、Secret（子进程或独立安装包、显式信任、认证注册、同一 conformance 准入）；多拓扑 conformance（embedded、单节点 server、Push、Pull runner 跑同一 conformance 套件，含 Push/Pull DispatchLease 等价性）；全局并发与调度公平性；长稳演练：72h soak 通过后再进行 7d soak/chaos。

非目标：不承诺多租户 SaaS；不引入 auto-merge；不承诺绝对安全或绝对质量；不让 Verification/SCM/Publisher Provider 获得裁决权。

退出门禁：

- 外部贡献者仅凭开源文档可完成自托管部署；
- 六类 Provider 均经认证注册产生 CapabilitySnapshot（冻结 protocolVersion、providerType、providerName、providerVersion、capabilities、conformanceEvidenceRef、scope 与撤销/失效语义）；未知或不兼容 protocolVersion fail closed；
- 插件经同一 conformance 门禁准入，`hardened` 声明规则与 M8–M10 一致；Verification/SCM/Publisher Provider 只能执行或传输，决定 gate/ReviewDecision 与发布权限的 Fixture 全部失败，最终裁决仍在 Core；
- 72h 与 7d soak/chaos 期间无静默状态漂移，升级演练可回滚；
- 并发与公平性指标可度量并绑定事件账本。

Dogfooding：Marshal 自身仓库的 issue 分诊与文档任务经开源部署形态运行；soak/chaos 演练以 Marshal 自任务为负载。

### Milestone 13：Goal orchestration

M7–M12 完成后，平台可靠运行的是彼此独立的 Task；复杂需求目标由 M13 承接。M13 不扩大 M7–M12 的既定平台范围，只实现 ADR 0016 冻结的 Project/Goal 对象语义。

Goal：实现 Goal API 与 Goal 控制器，使长周期目标可承载复杂需求——持久 Project/Goal 对象；可审计的计划与重规划（计划的生成、变更与拒绝都是事件账本上的可回放事实）；跨 Run 记忆与 Artifact 引用（一律以内容摘要绑定）；预算与终止条件（预算耗尽、终止条件达成或判定不收敛时，Goal 必须终止并保存 Outcome）；独立质量评估（评估由不产出该成果的 Run 承担，不得自证）；人工干预与恢复（操作者可随时暂停、修正、中止 Goal，Goal 状态在 Runtime 中断后可恢复续行）。

非目标：不承诺多租户 SaaS；不引入 auto-merge；不允许超出预算与终止条件的无限自主执行；不改变 M7–M12 冻结的平台能力与生命周期语义。

退出门禁：

- Goal 的计划、重规划、预算、终止与人工干预全部可从事件账本回放，无账外状态；
- 至少一个复杂需求目标被分解为多个 Run，逐一通过独立验证与独立质量评估，端到端交付；
- 预算耗尽与终止条件触发的所有情形都保存 Outcome，失败或阻塞不静默放弃；
- Goal 在 kill/restart 后的恢复满足 M9 的 fencing 与恢复口径，续行不重复产出已接纳成果。

Dogfooding：用一个复杂 Goal 驱动本仓库自身的多步骤改进（文档 + 代码 + 审计链），在人工监督下端到端交付，并与人工分解对照评估计划与重规划质量。

### 纵向切片与 Provider 替换的统一验收口径（ADR 0017 修订）

1. **M8 embedded/local 纵切**：单二进制 embedded 模式 in-process 跑通幂等提交（loopback/受信任本地边界）→ 冻结 Run → durable `READY` → scheduler claim + fencing → Local SandboxProvider → AgentAdapter → checkpoint/log/evidence → 独立 Verifier sandbox（业务验证 workload，conformance probe 另按 §2 拓扑运行在被测 Provider 的 target allocation 内）→ `REVIEW_PENDING`/`ACCEPTED`（暂不自动 publish）；SPI 同时实现二维要求、内容寻址 Stage、workloadRole/principal 身份 fencing 与 replacement allocation Restore；
2. **M9 服务化**：同一套用例切换为 `marshal-server` + TaskSubmission/Run Public API（versioned HTTP/JSON + OpenAPI、SSE 断线续传）重跑，embedded 模式保持兼容；Push/Pull DispatchLease 按同一唯一状态机满足 capability matching、ack/heartbeat/deadline/generation/fencing 且两拓扑等价；在 `RUNNING`/`VERIFYING` 期间 kill -9 Runtime：60 秒内 Inspect/Reconcile，旧 execution handle 被 fencing 拒绝，无双写、无丢证据；
3. **M10 Provider 替换**：仅替换为 CloudflareSandboxProvider 跑同一 conformance/E2E，用例不变；`hardened` 声明必须持有有效 ConformanceEvidence。

### 与云端能力审计阶段的对应

| 研究阶段（cloud-agent-readiness-2026） | 对应 Milestone |
| --- | --- |
| Phase 1：Durable Runner PoC | M8–M9 |
| Phase 2：无人值守云端执行 | M10–M11 |
| Phase 3：复杂任务编排（Project/Goal、跨 Run 记忆） | M13 Goal orchestration（对象语义由 ADR 0016 冻结） |
| Phase 4：规模化与多租户评估 | 仍为评估项，不在 M7–M13 承诺内 |

## 延后阶段

以下事项不属于 M7–M13 承诺：

- GitLab Publisher；
- CI Webhook Receiver；
- MCP、ACP 或专用 Desktop/Remote Service Facade（常驻 Runtime 本体已纳入 M9）；
- Telemetry、Cost Accounting 与 Policy-based Routing；
- Web UI 交互面（观察能力实现为 Runtime 事件流只读投影，交互形态未冻结）；
- 任何 Automatic Merge Policy；
- 多租户服务化（属于评估项，威胁模型评审通过后才讨论）。

原“Hardened Container/VM Profile”由 M8/M10 的 conformance、独立签发的 `ConformanceEvidence` 与 `hardened` 声明规则承接（ADR 0017）；原“增加 SQLite/Index”不再是路线项（单机开发沿用文件账本，生产存储演进见 M9/M11）。

## MVP 可用定义

至少一个真实 Worker 必须从 Frozen TaskSpec 出发，经过独立 Verification、Codex Review、有界 Rework、GitHub Draft PR 与 Outcome Export，完整完成 Fixture Task；同时 `security-model.md` 中针对 Local Profile 的安全验收测试全部通过。
