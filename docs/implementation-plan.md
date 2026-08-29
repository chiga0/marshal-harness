# 实施计划

本计划用于把当前 embedded/local 先行实现逐步收敛到[整体架构](architecture.md)定义的长寿命、可自托管、确定性 Control Plane。Milestone 是交付顺序，不是产品定位。

> **当前权威路线（2026-08-28）**：实施顺序以仓库根目录 `AGENTS.md`、[Roadmap 状态](roadmap-status.md)、[ADR 0052](adr/0052-v1-release-scope-and-production-reachability.md) 与 [Issue #186](https://github.com/chiga0/marshal-harness/issues/186) 为准，按纵切优先的 `I186-R0→R6` 收敛。Milestone 0–9 的历史结论与代码资产保留，但历史 `PASSED` 不自动等于 v1.0 production integration。当前 `I186-R0: PASSED`、`I186-R1: IN_PROGRESS（INTEGRATED）`、`I186-R2–R5: IN_PROGRESS（COMPONENT，production 语义未收敛）`、`I186-R6: PLANNED（DESIGN）`。M10–M13 不再阻塞 v1.0，作为 1.x 候选在 R6 后重新排期。本文后续 M0–M13 章节保留历史目标，不得据此提前升级实现状态。

> **2026-08-28 执行 checkpoint**：durable server run controller 已于 `main@44ee8c9` 合入；受支持的 production selector 已于 `main@d4b9647` 收紧；ResultIngress admission→worker-result→Run journal crash-atomic 持久化/恢复已于 `main@912f659` 合入。前置 Pi `0.84.3` fixed-bin canary 绑定 `sourceHead=d4b9647`，单 Attempt 通过 9 项 Gate 到 ReviewPacket/`REVIEW_PENDING`；它尚未导入独立 ReviewDecision、未进入 `ACCEPTED`。unsigned RC 路径可行但产物尚未发布。上述事实不升级 R2–R6。
>
> **2026-08-28 生命周期合同 checkpoint**：[ADR 0056](adr/0056-darwin-process-observation-and-attempt-terminalization.md) 已于 `main@ecee8d4` 接受，冻结 Darwin ordinary-user 的 Core-owned launch coordinator、admission/terminalization authority CAS、dispatch eligibility 与 `cleanup-completed` 正交、cleanup binding release 及 cooperative/non-detaching process-group 边界；实现仍待沿现有 Local/sandbox bridge/`execution.Service`/server controller composition root 接线，R3–R5 不升级。
>
> **2026-08-29 ProcessBridge authority checkpoint**：[ADR 0063](adr/0063-prepared-execution-authority-and-production-chain.md)（Proposed）登记了当前最小阻塞：现有 `PreparedRunStart` 无法从 held Attempt authority 恢复完整 owner/Attempt/Allocation/launch/Pi 原件，且没有 held current Run authority 与 exact successful `resume(state=running)` 后的唯一 Run-start outcome commit。交付顺序固定为：ADR 0063 先被接受 → 只落一个 secret-safe `PreparedExecutionV1` bounded authority component → 立即以相邻下一切片接入 fixed `marshal` composition root；不得在两切片之间插入第二个 component 或扩大到通用恢复策略。

## v1.0 权威实施表

能力成熟度独立于 Milestone 状态：`DESIGN` 表示只有合同，`COMPONENT` 表示实现/测试存在但 production root 不可达，`INTEGRATED` 表示 fixed `marshal` CLI 或 `marshal control-plane serve` 的真实 composition root 与真实 Agent/result bytes 穿过该路径，`RELEASED` 表示 release gate 通过。任何阶段都不得只凭 Fake、package test 或独立 API/transport 测试关闭。

| 阶段 | 状态 | 当前成熟度 | 必须交付的最短纵切 | 退出条件 |
| --- | --- | --- | --- | --- |
| `I186-R0` | `PASSED` | `DESIGN` | rebaseline、ADR 0043–0045、baseline report 与 golden trace | 历史证据保留，不重复实施 |
| `I186-R1` | `IN_PROGRESS` | `INTEGRATED` | 在现有 `execution.Service` 唯一 seam 接通真实 Agent-in-Local/Container allocation | `cmd/marshal` 或 loopback server 可达；Agent 实际在 allocation；真实 result bytes 返回 Core |
| `I186-R2` | `IN_PROGRESS` | `COMPONENT` | command/result authority 收敛到现有 durable journal；ResultIngress 事务化接纳；补齐 Proposed ADR 0063 的 creation-once Run-start preparation 合同 | `main@912f659` 已合入 crash-atomic admission/worker-result/Run journal；继续让 ADR 0056 terminalization barrier 复用同一 authority CAS，并在 ADR 0063 接受后证明 prepared/outcome response-loss 重放无第二真值 |
| `I186-R3` | `IN_PROGRESS` | `COMPONENT` | `main@d4b9647` 已将 production selector 收紧到 `LaunchCapable`；`main@ecee8d4` 已接受 ADR 0056 的 Core-owned Darwin launch/process-group observation合同；ProcessBridge 前完整 authority closure 仍由 Proposed ADR 0063 阻塞 | hardened evidence 独立产生；ordinary-user 明确 N/A；任一 binding/进程 identity/PreparedExecution 漂移与 detach 均拒绝；真实路径负测通过 |
| `I186-R4` | `IN_PROGRESS` | `COMPONENT` | 单一 recovery decision 与 `marshal explain`；已合入的 loopback server controller 复用固定 CLI authority；admission/terminalization authority CAS | `main@44ee8c9` controller 接入 cleanup transaction；kill/restart/cancel/timeout/retry 和重复 start 只有一个可回放结论；未知进程 identity 零 kill 并 fence |
| `I186-R5` | `IN_PROGRESS` | `COMPONENT` | `sourceHead=d4b9647` 的前置 canary 已单 Attempt/9 Gate 到 `REVIEW_PENDING`；继续接入独立 ReviewDecision、eligibility terminal、allocation terminal receipt 与 `cleanup-completed` | 在最终 `main` 将同一 fixed-bin E2E 推进到 `ACCEPTED`；cleanup-before-unlock/successor；无重复副作用；旧 bypass 机械拒绝 |
| `I186-R6` | `PLANNED` | `DESIGN` | failure conformance、稳定安装、签名/notarization、升级/回滚、release | 先发布身份可验证的 unsigned RC；再关闭 Issue #212 并通过 macOS/Linux stable gate，能力成熟度才升级为 `RELEASED` |

v1.0 的唯一支持链是：

```text
marshal / marshal control-plane serve
  → durable Run journal
  → Core-owned WorkerExecutor
  → Local/Container Sandbox allocation
  → real AgentRuntime
  → ResultIngress
  → independent Verification / Review
  → Outcome
```

Cloudflare 完整生产拓扑、多节点 HA、多用户/多租户、完整 Provider/SDK 矩阵、Web UI 与复杂 Goal DAG 均延期到 1.x。

当前交付顺序固定如下，禁止再横向铺组件：

1. 锁定 `main@912f659` 的 crash-atomic ResultIngress transaction 为唯一 admission 基线，不再建立另一条 ResultIngress/worker-result 真值；
2. 完成 ADR 0063 独立审查并先接受合同；未接受时不得实现该持久化格式；
3. 接受后只落一个 ADR 0063 bounded authority component，严格限于 codec/creation-once/held resolver/outcome commit 与 hostile component matrix，成熟度保持 `COMPONENT`；
4. **立即相邻**的下一切片把该 component 接到 fixed `marshal` / `marshal control-plane serve` composition root，并同时按 ADR 0056 在唯一 transaction 上接入 Core-owned Darwin launch、terminalization CAS、eligibility terminal 与 `cleanup-completed`；第 3、4 步之间禁止第二个 component、Provider 扩面或无关 Harness 工作；
5. 由独立 reviewer 为当前主线 live canary 生成 ReviewDecision，通过现有 `task review --decision` 从 `REVIEW_PENDING` 到 `ACCEPTED`，并证明旧 cooperative process group 已安全退出或被 fence、不会与 successor 双活；
6. 发布 unsigned RC 收集安装/升级证据；stable release 仅在 Issue #212 signing/notarization 和 Linux stable gate 全绿后执行。

### Darwin ordinary-user 进程生命周期实现顺序

[ADR 0056](adr/0056-darwin-process-observation-and-attempt-terminalization.md) 的实现必须作为一个 production vertical slice，不拆成不可达的孤立 package：

1. 在现有 authority storage 中 append-only 持久化 `process-started`、terminalization barrier 与 cleanup facts；ResultIngress admission 和 barrier 使用同一 transaction/CAS；
2. Core-owned launch coordinator 在 barrier 下完成 spawn/process-group、PID birth、cwd/executable held-FD 与 live handle 观察，Provider 只返回 claim；内存 handle/map 仅作 projection；
3. eligibility 正常/异常终止时立即 generation bump/fence；仅保留 cleanup-only binding。Sandbox bridge 与 Provider `Terminate` receipt 接入同一 transaction，在安全 process-group 终点后写 `allocation-terminated`、`cleanup-completed`，最后释放 cleanup binding；
4. 已合入的 server controller 与 execution recovery 先以同一 barrier 固定 ResultIngress 结论，再清理旧控制单元，只有 `cleanup-completed` 后才 unlock/retry/successor；
5. 一次完成 crash/CAS-race/ABA/forgery/replay/process-reuse/detach 矩阵和真实 Darwin fixed-bin E2E。禁止用匿名临时 executable、全机 `ps` 匹配或 PID-only kill 代替权威观察。

该顺序不新建服务、queue 或状态库，也不接受 ADR 0035 的 Run owner V2；缺少当前合法 Run authority 时，清理方只能 fence/intervention，不能跨编排 kill。

### v1.0 复杂度预算

I186-R1→R6 按[整体架构的 v1.0 物理投影](architecture.md#v10-物理投影)实施，终态职责图不直接映射为服务清单：

- Control Plane 默认保持一个固定 `marshal` binary（CLI 或 `marshal control-plane serve` 进程）和一条 authority write path；独立 `marshal-server` executable 仅是历史/测试兼容，不得进入 production topology；Kernel、admission、schedule/allocation、ResultIngress、Decision 与 effect reconcile 优先实现为进程内模块；
- v1.0 不新建通用 `WorkflowTemplate` DSL、Goal DAG runtime、跨节点 scheduler、独立 GC service、第二 queue 或第二状态库；
- 新增 seam 必须直接关闭当前 R1–R6 exit criterion，或关闭已经在同一生产链复现的确定性故障；仅有未来复用价值不足以插入主线；
- 默认每个切片必须在同一变更中接到 `cmd/marshal` 或 `marshal control-plane serve` 的真实 composition root 并取得真实路径证据，禁止无界铺 package/schema 后延期集成。唯一窄例外是 ADR 0063：**ADR 接受 → 一个 bounded authority `COMPONENT` → 立即相邻的 fixed composition 切片**；前置切片不得升级成熟度，第二区段不得插入第二个 component、Provider 扩面或无关工作，紧邻 composition 未完成就必须回退前置资产；
- Provider 扩面必须晚于至少一条真实 Agent-in-Sandbox 纵切闭环；新增 Agent 复用既有 Sandbox，新增 Sandbox 复用既有 Agent，不复制 Core 生命周期；
- 只有独立 trust boundary、durable lifecycle、已测量的扩缩容/故障隔离需要，才能把模块拆成独立进程；拆分不得产生第二业务权威。

该预算不放宽 Worker 不自证、Worker/Publisher 分权、ResultIngress current-ledger recheck、Evidence 精确绑定、路径/凭据边界或默认不 merge。它只限制实现面的横向铺开，确保 v1.0 用最短纵切验证长期架构。

## 门禁

本文 Local MVP 部分（Milestone 0–6）已由维护者授权实施；M7–M13 部分于 2026-08-10 随 [ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md) 接受而授权（M7–M12 为耐久 Runtime 平台阶段，M13 为 Goal 编排阶段）。M8–M13 还必须遵守 [ADR 0017](adr/0017-provider-neutral-sandbox-contract.md)、[ADR 0018](adr/0018-control-plane-and-provider-ports.md) 与 [ADR 0019](adr/0019-deterministic-control-plane-typed-execution-and-goal-admission.md)：确定性 Core 是唯一 Supervisor；Typed Execution 不形成通用 Provider 协议；副作用采用 append-only 对账/补偿；Goal plan 先 proposal、后 deterministic admission。ADR 的接受只冻结设计，不提前升级实现与 conformance 状态。本文只是实施计划：信任边界、持久化契约、生命周期或发布权限的改变仍必须先新增或替代 ADR。

当前状态：Milestone 0–6 已全部通过，Local MVP 标记 `USABLE`（2026-08-07）；M7 的设计与契约阶段已通过。M8/M9 保留当时定义下的 `PASSED` 和 CI 证据，但审计确认其 Runtime 资产当前为 `COMPONENT`：尚未共同进入真实 Agent-in-Sandbox 生产调用链。M10–M13 保持 `PLANNED`，并改为 R6 后的 1.x 候选池。状态取值与证据见 [Roadmap 状态](roadmap-status.md)，目标架构见 [Runtime 架构](runtime-architecture.md)。

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

M7–M12 是耐久 Runtime 平台路线，M13 是 Goal 编排路线；目标架构见 [Runtime 架构](runtime-architecture.md)。M7 的 `PASSED` 只覆盖当时的 ADR 0016–0018 设计门禁；ADR 0019 是不回滚 M7 状态的设计增补，也是 M8–M13 的实施前置契约。每个 Milestone 必须先通过 Local MVP 全量回归，且不得提前引入后续阶段的外部副作用。

### Milestone 7：架构与契约

Goal：冻结耐久 Runtime 与可插拔 Sandbox Provider 的架构与契约——ADR 0016、[ADR 0017](adr/0017-provider-neutral-sandbox-contract.md) 与 [ADR 0018](adr/0018-control-plane-and-provider-ports.md)、核心对象和 Port 边界。对 Project/Goal，M7 只冻结其存在性、authority ownership 与“驱动多个有界 Run”的原则；revision、plan admission、budget 与 controller 语义由 ADR 0019/M13 承接。

非目标：不实现 Runtime、数据库、Temporal、SandboxProvider、Cloudflare 客户端或 Goal 控制器；不更改 Local MVP 行为。

退出门禁：

- ADR 0016 已接受，ADR 0015 标记 Superseded before acceptance；ADR 0017 与 ADR 0018 已接受（接受只关闭设计歧义/冻结设计，不升级 M8–M13 实现/conformance 状态）；
- 治理、愿景、架构、安全模型、路线图与审计文档口径一致，术语与状态名与 Schema 对齐；ADR 0017 的历史 universal 口径在现行规范入口就地标注已被 ADR 0018 取代，不残留为活跃依据；
- Local MVP 回归全部通过，本仓库 CI 全绿。

Dogfooding：本 Milestone 产出本身作为一次完整 Marshal Task 经 Local MVP 闭环（Frozen TaskSpec → 独立 Verification → Review → Draft PR）交付，验证文档类任务的证据链。

### M8–M13 共同实施门禁（ADR 0019）

- 共享执行基座只复用 command/lease/heartbeat/cancel/deadline/event/log/artifact/checkpoint；每个 Port 的 Schema、principal、credential、acceptance 与 conformance 保持独立；
- `Candidate`、`Evidence`、`Assessment`、`SideEffectReceipt` 分别按所属 Port 接纳：只有 dispatch-bound Candidate/Evidence 校验 current lease generation/fencing/sequence 精确相等；Assessment 校验 actor/scope/packet/evidence/sequence 并拒绝 lease 字段；Receipt 校验 intent/authorization/target/request/reconcile/current-ledger；
- Core 内部规范化的 `SideEffectIntent`/`SideEffectReceipt`/`ReconcileRecord` 尚未实现，它们不是 Provider wire Schema；M8 冻结内部 Schema 并启用首批 operation，M9 扩展覆盖面，M10 Cloudflare，M11 HA owner，M12 按 Port mapper/wire/conformance；
- M13 启用 Planner-generated dispatch 前，必须先完成 Goal Schema、deterministic admission 与全部 negative fixture；Planner 不得直接创建 Run、写 ledger、发布或补偿。

### Milestone 8：Sandbox SPI、Fake/Local conformance 与 embedded/local 纵切

**实施进展（2026-08-13 更新，退出门禁通过）**：M8 六个硬门禁全部合入 main 且各 PR 远端 CI 全绿，退出门禁于 2026-08-13 通过，M8 状态更新为 `PASSED`（见 [Roadmap 状态](roadmap-status.md) 与[验收报告](milestone-8-report.md)）。已合入：gate-1（authority 双键空间 AuthorityNamespaceId/SecurityDomainId + SideEffect authority-record Schema，`internal/authority/`，PR #42）、gate-2（ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence 类型 + Draft 2020-12 Schema + attestation 全链绑定，`internal/provider/`，PR #45）、gate-3（legacy v1alpha1 CapabilitySnapshot → ProviderCapabilitySnapshot 的 fail-closed mapper，`internal/provider/legacy_mapper.go`，PR #48）、gate-5（ProviderCapabilitySnapshot/ConformanceEvidence eligibility 判定层，`internal/provider/validation.go`，PR #47）、gate-4（durable embedded registration + ledger recovery，R2 lineage `m8-durable-registration-store-r2-20260812b`；R1 的 PR #46 因 Secret scan 对测试 fixture 的 gitleaks 误报关闭未合入，PR #57）与 gate-6（enable DispatchLease match，按硬门禁顺序最后启用，PR #60）。embedded 纵切：internal/sandbox SPI 类型 + Fake Provider + conformance 套件（PR #75）、Local SandboxRunner 宿主进程执行 + lease 绑定 + receipt observation（PR #80）、typed cross-domain edge 记录类型 + fixture 矩阵残留（PR #61）。同期合入基础设施修复：#50 repo lock 有界重试、#58 gitleaks 测试 allowlist、#59 工具 allowlist 机械强制、#62 verification gofmt 归一化、#72 supervisor 核心、#77 execution admission 身份绑定、#79 状态转换通知钩子、#84 adapter 版本集合门禁、#95 qwen 0.21.11 支持。**已知限制**：M8 各 gate 尚未整体接入最终 Runtime 执行路径（接入属 M9）。**acceptance 设计教训**：(1) 不要要求精确测试函数名（诱发 worker rename 震荡），用功能性验收（查实现符号 + 测试通过）；(2) git 密集包的回归测试用 `-count=3` 而非 `-count=10`（后者撞 go 默认 10m 超时）；(3) worker 无 shell 权限跑不了 gofmt，格式问题需给精确 diff；(4) opencode 二进制自动升级会变 unsupported，备用 pi；(5) CI Secret scan（gitleaks generic-api-key）会命中测试 fixture 的 `XxxKey = "字面量"` 直接赋值，fixture 必须用字面量相加形式规避，仓库已新增 `.gitleaks.toml`（仅放行 `*_test.go`）兜底；(6) ReviewDecision 的 `mergeRecommendation` 在 `allowMerge:false` policy 下必须为 `do-not-merge`，实际 merge 由维护者在 Marshal 外执行。

ADR 0019 增量：在 M8 纳入按 Port 隔离的共享执行基座类型与 negative fixture，但不得改变 ADR 0018 §7 硬顺序；任何 claim/lease dispatch activation 仍位于 Schema、mapper、耐久注册/恢复与 validation 之后的最后一步。M8 冻结 Core 内部 SideEffect authority-record Schema，首批只启用 Sandbox `Provision`/`Stage`/`Terminate` 与本地 cleanup operation，覆盖 lost-response、重复投递和 orphan cleanup。

Goal：实现 SandboxProvider SPI 与首个 conformance 套件（Fake 与 Local Provider 通过同一套件），并同步落地 ADR 0017 冻结的 provider-neutral 契约：`AccessMode × AssuranceLevel` 二维要求与旧 `executionProfile` 兼容映射、`ConformanceEvidence` 签发链（按 §2 证据拓扑：probe 定义/challenge/artifact digest/调度/out-of-band 观察/裁决/签发由 Control Plane 与独立 Conformance Verifier 控制，probe workload 作为敌对测试负载运行在被测 Provider 创建、身份精确绑定的 target allocation 内，Provider 的 completed/receipt 不能自签通过）、内容寻址 Stage（inline/ArtifactStore locator、消费前后重算 sha256、篡改 fixture）、workloadRole/principal 拆分与完整身份元组、replay key 校验、replacement allocation Restore 故障注入。纵切交付 **embedded/local 形态**：单二进制 embedded 模式 in-process 跑通幂等提交（loopback/受信任本地边界）→ 冻结 Run → durable `READY` → scheduler claim + fencing → Local SandboxProvider → AgentAdapter → checkpoint/log/evidence → 独立 Verifier sandbox（业务验证 workload，与 conformance probe 拓扑不同）→ `REVIEW_PENDING`/`ACCEPTED`（暂不自动 publish）。`marshal-server` 与 Public API 属于 M9，不在本 Milestone。M8 实施顺序为硬门禁（ADR 0018 §7）：negative fixtures/event contract → ProviderRegistration/ProviderCapabilitySnapshot Schema → legacy capability mapper → durable embedded registration + ledger recovery → snapshot/evidence validation → 最后 enable DispatchLease match；前置缺失时 claim/match 一律 fail closed；negative fixtures 覆盖跨 scope/protocol、same key/different digest、revoked replay、restart/rebuild、Provider substitution 与各类 claim 后失效的 Push/Pull；另按 ADR 0018 §11/§13 覆盖 attestation substitution/config/key-rotation fixture（相同软件版本换 providerInstanceId、换 configDigest 或换 trust root key 后复用旧 ProviderCapabilitySnapshot/ConformanceEvidence 必须失败）与原子 fencing fixture（lost-response、concurrent-write、old-generation overwrite，冲突 bytes 只进 quarantine namespace）；另按 ADR 0018 §6 覆盖失效处置分级 fixture（security-critical revoke 立即 cancel + generation bump + kill，不留 drain 窗口；planned/ordinary incompatible upgrade 走新 registration/snapshot，旧实例 stop-new + bounded drain，deadline 到期再 fence；普通升级复活旧注册或改写旧 lease digest 必须失败）；另按 ADR 0018 §3/§10 覆盖 typed cross-domain edge fixture：伪造签发者（issuer）或冒用签发身份、错 authority scope、错 source/target（issuer/sourceActor/targetActor 与 edge 类型不符或 target substitution）、错 operation、错 attempt/allocation、已过期、已撤销、digest 替换的 edge 必须全部拒绝；绕过 current-ledger recheck 使用 edge 派生 token/handle、转授或扩权尝试、跨 Port token/schema 复用、携带 raw handle/credential 或跨域复用 raw credential/ConformanceEvidence 的请求必须失败；合法三条 typed cross-domain edge（DispatchResultCapability/MaterialAccessGrant/PublicationAuthorization）可恢复且幂等；未经 Core 签发的跨域访问默认拒绝（default deny）。

非目标：不实现 `marshal-server` 与 TaskSubmission/Run Public API；不接入任何远程 Provider；不引入多节点；不改变既有 CLI 生命周期的外部行为。

退出门禁：

- Fake 与 Local Provider 通过同一 conformance/E2E；Local Provider 永不声明 `hardened`，请求 hardened 时 fail closed 或改由持有效 `ConformanceEvidence` 的 Provider 承接；
- conformance 证据拓扑：probe workload 运行在被测 Provider 创建、身份精确绑定的 target allocation 内；probe 定义/challenge/nonce、probe artifact digest、调度、out-of-band 观察、裁决与证据签发由 Control Plane 与独立 Conformance Verifier 控制；“Provider 凭自身 completed/receipt 自签通过”的 fixture 必须失败；
- 二维要求校验：旧 `executionProfile` 三个取值按映射表确定性解析；AssuranceLevel 无法满足时 Run 保持 `BLOCKED`，无静默降级；AccessMode 升级请求被拒绝；
- Stage 内容寻址：篡改 bytes fixture 让回显型实现失败；inline 超限对象被拒绝并要求 locator；
- 身份 fencing：workloadRole 封闭枚举仅 `worker`/`verifier`，Publisher 作为 Sandbox workload、Provider 借通用 role 跨 Port 取得能力的 fixture 全部失败；身份元组缺失/不匹配的操作被拒绝；陈旧 generation/fencingToken 的 replay 被隔离为诊断材料；Restore 响应丢失、并发 Restore 与恢复后陈旧 handle 写入的故障注入全部被拒绝，无双写；
- 耐久注册与在途 lease 撤销（ADR 0018 §5/§6/§7）：按硬门禁顺序完成 negative fixtures/event contract、ProviderRegistration/ProviderCapabilitySnapshot Schema、legacy capability mapper、durable embedded registration + ledger recovery、snapshot/evidence validation，最后才 enable DispatchLease match，前置缺失时 claim/match fail closed；memory-only registration、默认补 scope/evidence、revoked/expired 经普通 replay 复活、跨 scope/protocol 归并的 fixture 全部失败；registration revoke/expire/incompatible、snapshot expire/supersede、evidence revoke/expire 使 active lease（在途 lease）立即失去资格，Allocation/Attempt 终止对账、晚到结果隔离，继续执行经新 Attempt + 新 lease 重新 match；
- attestation 全链绑定与跨域隔离（ADR 0018 §10/§11）：registration/snapshot/evidence/lease 持久记录（registration/snapshot/evidence 为 authorityNamespaceId 拥有的 authority ledger 事实）携带复合 securityDomainId（tenantNamespace/trustDomainKind/isolationDomainId）作为 actor 身份与 provenance，未经三条 typed edge 中对应 active edge 授权或绑定不精确匹配的跨 securityDomainId/跨 trustDomainKind 引用 fail closed、合法三条 typed edge 精确匹配后可恢复且幂等的 fixture 通过；相同软件版本换 providerInstanceId、换 configDigest 或换 trust root key 后复用旧 ProviderCapabilitySnapshot/ConformanceEvidence 的 fixture 全部失败，任一绑定项变化产生新 immutable 快照/证据并触发 eligibility 重判；同一 Run 的 Worker/Verifier 共享 principal 或 allocation 的 fixture 全部失败；
- 权威与 actor 分离、typed cross-domain edge（ADR 0018 §3/§10）：权威侧持久记录（submission/Task/Run/Attempt/ledger/DispatchLease/Allocation/ReviewDecision/Evidence graph/Outcome/SideEffectIntent/Receipt reconcile/typed edge/idempotency/outbox/audit/SSE）携带 authorityNamespaceId=(tenantNamespace, controlPlaneId, authorityScopeId)，registration/snapshot/evidence 作为 authority ledger 事实仅携带 actor securityDomainId/provenance/eligibility，其余 actor 引用字段携带 securityDomainId=(tenantNamespace, trustDomainKind, isolationDomainId)；伪造签发者（issuer）或冒用签发身份、错 authority scope、错 source/target（issuer/sourceActor/targetActor 与 edge 类型不符或 target substitution）、错 operation、错 attempt/allocation，或已过期、已撤销、digest 替换的 typed edge（DispatchResultCapability/MaterialAccessGrant/PublicationAuthorization）fixture 全部失败；合法三条 typed cross-domain edge 可恢复且幂等，M8/M9 fixture 明确区分三类合法 typed edge 的 positive 用例与无 edge、错 edge（edge 类型与访问对象/方向不符）、错绑定的 negative 用例；provider-registration/control 经 transport identity、该 Port AuthN/AuthZ、scope/protocol validation 与 registration protocol 校验、由 Core 将获准事实写入 authority ledger 的合法注册/控制请求（positive）通过，跨 Port 复用该 transport identity/token 或以 securityDomainId 相同为由跳过 principal/registrationId/providerInstanceId/scope/attempt/allocation/generation/operation 门禁的同域 bearer 化请求（negative）失败；绕过 current-ledger recheck 使用 edge 派生 token/handle（派生 token/handle 不得成为第二权威）、转授或扩权尝试、跨 Port token/schema 复用、携带 raw handle/credential、以 edge 替代 ConformanceEvidence 或未经 Core 签发直接跨域、写入权威对象的请求必须失败；Provider actor 借 securityDomainId 宣称 lifecycle、ReviewDecision 或发布决定的 fixture 全部失败；controlPlaneId 在进程重启/灾备切换后保持不变、权威对象归属随原 authorityNamespaceId 延续的 fixture 通过；
- 失效处置分级（ADR 0018 §6）：security-critical revoke（credential compromise、protocol violation）立即 cancel + generation bump + kill、不留 drain 窗口的故障注入通过；planned/ordinary incompatible upgrade 使用新 registration/新 snapshot，旧实例 stop-new + bounded drain，drain deadline 到期再 fence；事件携带机器可读原因码、与审计记录分开；普通升级复活旧注册或改写旧 lease digest 的 fixture 全部失败；
- 原子 fencing 写入汇（ADR 0018 §13）：ledger sink 同事务 atomic compare-and-append/transaction 验收通过；Artifact/Evidence/Checkpoint/Candidate bytes 的接纳关系归 authority ledger，以 authorityNamespaceId+run+attempt+allocation+generation scoped immutable key（actor securityDomainId 只作为 provenance 记录）、digest-verified put-if-absent 写入；lost-response、concurrent-write、old-generation overwrite 故障注入全部通过——陈旧 generation 不可能先覆盖对象 key 再被 ledger 拒绝，冲突 bytes 只进 quarantine namespace，不进入当前 evidence graph；
- 纵切全链路通过，且 Worker/Verifier/Publisher 分权、Worker 不自证、单写入者不变量在 embedded 形态下有测试证明；
- 提交入口默认只绑定 loopback 或受信任本地边界；未经授权的提交与跨 scope 提交被拒绝并记录审计；
- 能力不足时 fail closed 的 Fixture 通过；
- Local MVP CLI 回归零回退。

Dogfooding：用 embedded/local 形态承接本仓库真实的文档/修复类任务，替代一次性 CLI 编排，统计重复提交的幂等归并与失败证据留存。

### Milestone 9：固定 Marshal 服务模式、Public API 与 Durable Runtime

> **现行拓扑修订（ADR 0062）**：本节以下出现的 `marshal-server` 均是 M9 当时的历史命名，不授权独立 production executable；所有现行 Goal、退出门禁和纵切验收中的 production server 一律解释为固定 `marshal control-plane serve`。legacy `marshal-server` 只能保留无 production mutation 权限的开发/兼容入口，不得成为第二 composition root。

ADR 0019 增量：扩展 M8 已冻结的 authority-scoped SideEffect ledger operation 覆盖面，不替换内部 Schema；各 Port receipt 经 versioned fail-closed mapper 规范化并保留 sourcePort/sourceReceiptDigest/sourceProtocolVersion。进度/heartbeat/log 是观察事件；crash recovery 必须对账 ambiguous effect，不能创建第二个 intent。

Goal：交付常驻服务形态与耐久 Runtime 主体——`marshal-server`（C/S 形态的 Marshal Control Plane 进程）；TaskSubmission/Run Public API 采用 versioned HTTP/JSON + OpenAPI（覆盖 Task create/get/cancel、Run approval/status、events/evidence；幂等 TaskSubmission（submission 与幂等权威记录均由 authorityNamespaceId 拥有），幂等身份为 `(authorityNamespaceId, scope, idempotencyKey, requestDigest)`，authorityNamespaceId=(tenantNamespace, controlPlaneId, authorityScopeId)，controlPlaneId 是 HA/灾备中保持稳定的逻辑权威身份、不是进程实例（单实例部署 controlPlaneId 可固定 default），ADR 0018 §10；入口默认仅绑定 loopback 或受信任本地边界），事件基线采用支持 `eventId`/cursor 断线续传的 SSE（轮询 fallback；WebSocket/gRPC 推迟；SSE 恢复/去重/背压/再授权语义按 ADR 0018 §14 冻结，参数值在 M9 Schema/OpenAPI 冻结）；embedded 兼容（同一 Core 可在 in-process/service 之间切换，行为一致）；embedded CLI 经 in-process adapter 调同一 Public application Port，不直写 store，server client 经 HTTP transport；Push/Pull DispatchLease 按同一唯一状态机实现，只冻结 outcome/invariant equivalence（唯一 claim、eligibility、fencing、deadline、无双活、晚到隔离；允许拓扑特定的 offer/poll/claim/ack transition 与 timing：Push 由 `marshal-server` 调用 Provider endpoint，Pull 由 Provider runner 以 outbound-only long polling 或 streaming 领取）；两种拓扑都必须绑定认证 ProviderRegistration（registrationId）、claim 时 active 的持久 ProviderCapabilitySnapshot（providerCapabilitySnapshotDigest）与 conformanceEvidenceDigests 封闭集合、task/run/attempt/allocation、generation/fencingToken，都具备 capability matching、offer/claim-or-delivery ack deadline、heartbeat、expiry、cancel、reconcile、generation bump 与陈旧结果隔离；Push 同样先 capability match（比对持久 ProviderCapabilitySnapshot 与 conformanceEvidenceDigests，不匹配不投递），超时/响应丢失不得产生第二个 active allocation；禁止匿名 Pull；DispatchLease 只消费持久 ProviderCapabilitySnapshot，注册/快照/证据失效使在途 lease 立即失去资格（ADR 0018 §6）；Provider remote transport 同样为 versioned HTTP/JSON；提供最小、scope-bound、可撤销的 Provider/workload 注册身份（即使入口仅 loopback/trusted boundary）；inbox/outbox、dispatcher、heartbeat/fencing、kill/restart recovery；`DurableExecutionEngine` Port 接入 backend（生产参考 Temporal，embedded/单机为 Local Engine；backend 只承担相同 commandId 的 at-least-once delivery、timer wakeup、signal transport 与 crash recovery，不拥有 lifecycle/retry/rework 业务语义，delivery/activity retry 不创建 Attempt、不消费业务重试预算；单机开发允许 dev server + SQLite/local blob adapter）。CLI、Web、GitHub App、CI 一律按 Public API client 对待，不得绕过 Public API 直接读写业务状态。M9 冻结 marshal-server/Public API/Sandbox dispatch Push-Pull、远程注册、SSE 与 DurableExecutionEngine；M12 扩展其余 Provider 的 wire/SDK（ADR 0018 §8）；HTTP/JSON + OpenAPI 首发，WebSocket/gRPC 推迟。M9 范围内启用的一切非 loopback/in-process transport 自首次 enable 起满足 ADR 0018 §12 transport 安全基线（TLS 强制、双向身份与 audience/scope 校验、短期 credential rotation/revocation、replay protection）；DurableExecutionEngine backend profile 按 ADR 0018 §15 声明单一权威 seam（同事务 outbox 或 ledger-derived Core command journal 二选一），commandId 从 ledger 权威事实稳定派生，workflow/activity state 不是业务权威；M9 范围内 Port（public-api、provider-registration/control、dispatch-bound）按 ADR 0018 §16 冻结各自 versioned protocol family（独立 audience、AuthZ scope、schema、error/幂等/撤销与 conformance profile）；六类 Provider 与按 Port 的 conformance suite 边界按退出门禁的两条独立条款验收（不共享 suite 与每族统一 suite 分开表述）；security-critical revoke 立即 cancel + generation bump + kill，planned/ordinary incompatible upgrade 走新 registration/新 snapshot，旧实例 stop-new + bounded drain，deadline 到期再 fence，事件原因码与审计分开（ADR 0018 §6）。

非目标：不接入 Cloudflare；不做多节点 HA；不自研 workflow engine；不开放远程提交入口；不把 Core 开放成任意插件 HTTP Server（插件表面只有版本化 Provider Protocol）。

退出门禁：

- M8 的 embedded/local 纵切用例在 `marshal-server` + Public API 形态下原样通过，embedded 模式同时保持兼容；embedded CLI 经 in-process adapter 调同一 Public application Port，无第二条写路径；
- Versioned HTTP/JSON + OpenAPI 的 TaskSubmission/Run Public API（Task create/get/cancel、Run approval/status、events/evidence）可用；SSE 支持 `eventId`/cursor 断线续传且轮询 fallback 可用；SSE 按 ADR 0018 §14 通过验收：cursor 身份为 authorityNamespaceId+scope+ledgerSequence（权威账本的权威侧身份），订阅方另绑定自身 securityDomainId 完成授权判定，scope 内 sequence 单调，at-least-once 交付 + eventId/sequence 去重，cursor 过期/gap/压缩（compaction）返回 deterministic resync 起点与 snapshot digest，heartbeat 与有界 backpressure（超限断开引导 resync、不阻塞 ledger），周期性 re-Authorization 与敏感变更（registration 撤销、scope 变更、权限收回）即时 re-Authorization；SSE 承载业务 ACK、lease heartbeat 或 command 下发的 fixture 全部失败（SSE 是只读投影）；
- Push/Pull 两拓扑 outcome/invariant equivalence conformance 与故障注入通过：比较 normalized business trace 与业务不变量（唯一 claim、eligibility、fencing、deadline、无双活、晚到隔离），不比较逐步 wire trace；同一用例在两种拓扑下满足 capability matching、ack/heartbeat/deadline/generation/fencing 与陈旧结果隔离；匿名 Pull、能力不匹配的 Push offer/claim 被拒绝并记录；offer/claim ack 超时、响应丢失、heartbeat 中断与并发竞争均不产生第二个 active allocation；
- 最小、scope-bound、可撤销的 Provider/workload 注册身份可用：未注册或已撤销身份的请求被拒绝并记录；ProviderRegistration/ProviderCapabilitySnapshot 持久化于 ledger，memory-only registration、revoked/expired 经普通 replay 复活的 fixture 全部失败；
- 在途 lease 撤销（ADR 0018 §6）：registration revoke/expire/incompatible、snapshot expire/supersede、evidence revoke/expire 使 active lease 立即失去资格，Allocation/Attempt 终止对账、晚到结果隔离，继续执行经新 Attempt + 新 lease 重新 match 的故障注入全部通过；
- 不兼容与撤销分级处置（ADR 0018 §6）：security-critical revoke（credential compromise、protocol violation）立即 cancel + generation bump + kill、不留 drain 窗口的故障注入通过；planned/ordinary incompatible upgrade 使用新 registration/新 snapshot，旧实例 stop-new + bounded drain，drain deadline 到期再 fence；事件携带机器可读原因码、与审计记录分开；普通升级复活旧注册或改写旧 lease digest 的 fixture 全部失败；
- 在 `RUNNING`/`VERIFYING` 期间 kill -9 Runtime 后，可在 60 秒内完成 Inspect/Reconcile；旧 execution handle 上报被 fencing 拒绝；无双写、无丢证据；
- 幂等提交语义（幂等身份为 authorityNamespaceId+scope+idempotencyKey+requestDigest，幂等权威记录由 authorityNamespaceId 拥有）：同 scope+key+digest 返回既有 submission/run 且不重复副作用；同 scope+key 而 digest 不同返回冲突（fail closed），不创建、不归并错误 Run，并写入审计记录；
- 权威写入接纳边界故障注入：失联旧 Attempt 携带陈旧 `generation`/`fencingToken` 晚到上传 checkpoint/candidate/日志/证据引用时，被 expectedSequence/CAS 拒绝并隔离为诊断材料，不进入当前 Attempt 的 Evidence/Review/Publication；旧 execution handle 的外部副作用回放经 SideEffectIntent/Receipt + reconcile 对账，不产生重复副作用；
- 原子 fencing 写入汇（ADR 0018 §13）：ledger sink 同事务 atomic compare-and-append/transaction 验收通过；Artifact/Evidence/Checkpoint/Candidate bytes 的接纳关系归 authority ledger，以 authorityNamespaceId+run+attempt+allocation+generation scoped immutable key（actor securityDomainId 只作为 provenance 记录）、digest-verified put-if-absent 写入，已存在 key 永不覆盖；lost-response、concurrent-write、old-generation overwrite 故障注入全部通过，陈旧/冲突 bytes 只进 quarantine namespace，永不覆盖当前对象、永不进入当前 evidence graph；
- transport 安全基线（ADR 0018 §12）：M9 启用的一切非 loopback/in-process transport 自首次 enable 起强制 TLS、双向校验 server/provider 身份与 audience/scope、短期 credential rotation/revocation 与 replay protection 的验收通过；明文 transport、可转移共享 secret 充当 workload identity、未校验对端身份的 fixture 全部失败；
- DurableExecutionEngine 单一权威 seam（ADR 0018 §15）：backend profile 声明同事务 outbox 或 ledger-derived Core command journal 之一并通过对应一致性 fixture；commandId 从 ledger 权威事实稳定派生且重复投递幂等；backend workflow/activity state 自行决定 lifecycle/retry/rework/终态的 fixture 全部失败；升级 fixture 覆盖 workflow versioning/build ID、Continue-As-New、payload 外置与上限、activity heartbeat/cancel/retry；“ledger 已提交而 command 未投递”与反向故障注入均能按单一 seam 恢复，无第二调度权威；
- 按 Port 的 versioned protocol family（ADR 0018 §16）：M9 范围内 public-api、provider-registration/control、dispatch-bound Port 各自冻结独立 audience、AuthZ scope、request/response schema、error 模型、幂等约定、撤销语义与 conformance profile；跨 Port 复用 token、schema 或 operation 的 fixture 全部失败；embedded/Push/Pull 未派生不同协议版本或语义分支；
- 六类 Provider 分属不同 Port 与不同 protocol family，彼此不共享 conformance suite，不复用跨族 suite（ADR 0018 §2/§16）；
- 对每个具体 Port/protocol family，embedded/Push/Pull 是该族的 transport/topology adapter，运行该族统一的 conformance suite，Port 语义不随 transport 变化（ADR 0018 §16）；
- 每个 Activity 以 `commandId` + `expectedSequence` CAS 追加 Marshal 事件，账本重放不产生第二条业务事实；
- 恢复结论可仅凭持久事件账本得出；故障注入测试集全过；
- Queue 只能以状态投影实现，任何“队列权威”测试失败。

Dogfooding：Marshal 自身的回归与审计任务全部经 Durable Runtime 调度执行；故障注入成为常规测试集并在每次发布前运行。

### 跨 Milestone 前置门禁：Agent Production Authority Provider（ADR 0038，Accepted）

Qoder/Codex 的 AgentAdapter production enablement 在 M10–M12 平台路线中共享一个宿主前置，但不新增或提前完成 Milestone：[ADR 0038](adr/0038-agent-production-authority-provider.md) 已接受独立本机 `AgentProductionAuthorityProvider` Port 的实现合同。共享范围只包括外部 principal、认证 IPC、held-fd identity、OS isolation/audit receipt、host attestation、monotonic fence、atomic authority bundle、launch receipt/workload barrier 与 crash reconcile；Qoder/Codex 仍分别遵守 ADR 0034/0037 的封闭 profile、证据 Schema 与 conformance，AgentAdapter evidence 不得冒充 ADR 0018 Sandbox Provider `ConformanceEvidence`。

实施顺序为硬门禁：

1. 维护者接受 ADR 0038；在此之前只允许 fake Port、Schema/IPC parser 与负向 fixture，production registry 保持 hard-disabled；
2. 实现本机 Unix `SOCK_SEQPACKET` protocol v1、OS peer credential/operation AuthZ、closed request/response envelope、shared `SignedObjectEnvelopeV1`、`expectedProviderSequence` CAS、`SCM_RIGHTS` held handle、command replay/conflict 与 safe error；非本机 transport 留给后续 ADR；
3. 实现外部 service principal 与互斥 key usage，证明 Worker/Marshal/verifier 无 authority private key；Secret provider 通过 target child 的 session-scoped `CredentialIngressPort` 直连把 opaque、不可读的一次性 capability 交给 probe isolation principal，controller/APAP 永远不接收连接、fd 或 bytes，只持非敏感 content-addressed receipt ref；
4. 实现 content-addressed leaf batch、精确 detached manifest signature 与 prepared→external monotonic anchor→committed 原子交付；evidence/rotation/revocation/recovery authority 分权，recovery CAS 分离 original expected、observed current 与 anchored next，覆盖每个 fsync/crash/协调回滚、lost response 与 rotation/revocation 线性化点；
5. 实现 probe isolation receipt 与 stopped-child `PrepareLaunch/CommitLaunch/AbortLaunch/InspectLaunch` barrier；launch request/receipt 精确绑定 `authorityNamespaceId`、ADR 0037 T1–T3 的全部 held fixed root/mount namespace、current authority CAS，receipt 被 Marshal durable 接纳前 child 不得执行 workload，lost response 不得产生第二 child；
6. Linux 分别通过 Qoder ADR 0034、Codex ADR 0037 的真实 credentialed profile conformance；Darwin 在等价强制机制与替代 ADR 被接受前稳定返回 `unsupported`；
7. 非作者 reviewer 对真实 diff、当前宿主 evidence、race/static analysis、secret scan、rollback/revoke/kill 演练给出 P0/P1 清零结论后，Qoder/Codex 各自以独立 registry 变更启用。

该前置门禁不改变 M10 Cloudflare Sandbox Provider 的范围，也不把 APAP 计为 ADR 0018 六类 Provider 的新通用协议。M12 只在 APAP 已实现后交付其 system-service provisioning、运维文档与 profile conformance 工具；不能用 SDK/文档任务补做首次安全基线。

### Milestone 10：Cloudflare Provider

ADR 0019 增量：Cloudflare `Provision`/`Stage`/`Persist`/`Hydrate`/`Terminate`/TTL 全部进入副作用对账与 leak scan；补偿契约和资源身份无法证明时不得宣称资源生命周期可靠。

Goal：实现 CloudflareSandboxProvider——官方 Bridge 的 Go 客户端（自部署到用户 Cloudflare 账号的 HTTP/OpenAPI Worker）、Bearer 令牌认证的凭据管理、exec SSE、file/persist/hydrate/destroy 到 SandboxProvider SPI 的映射，以及 live opt-in 开关。

非目标：不默认声明 `hardened`；不把 Cloudflare 变成 Core 必选依赖；不改变生命周期语义。

退出门禁：

- 仅替换 Provider 后，M8/M9 的同一 conformance/E2E 全部通过（TaskSpec 与用例不变）；
- 只有持有独立签发的有效 `ConformanceEvidence`（mount/network/resource/credential 逐维结果全部 `passed`）时才允许声明 `hardened`，否则只按实际验证到的较低 assurance 等级声明，不放宽声明；Cloudflare 与 Local 走同一证据准入，无豁免；
- Provider 失败语义故障注入通过：Cloudflare 探测失败、行为漂移或容器丢失状态时，当前 Allocation/Attempt 先终止并对账（fail closed）；调度器仅为新 Attempt 选择满足同一冻结 `SandboxRequirements` 与 assurance 下限的兼容 Provider；无兼容 Provider 时 Run 保持 `BLOCKED`，不静默降低 profile、不复用旧 execution handle、不在同一 Attempt 内透明降级；
- Bridge SDK 处于 1.0 preview/Beta 的风险被显式记录并以上述 fail closed 语义控制；
- Provider 凭据不进入 TaskSpec、事件、Prompt、日志或 Worker 可见环境；
- 远程 Provider transport 自首次 enable 起满足 ADR 0018 §12 transport 安全基线（TLS 强制、双向身份与 audience/scope 校验、短期 credential rotation/revocation、replay protection），不以 M11 补首次安全基线；Push/Pull 两拓扑按同一 protocol family 验收，未注册/已撤销/跨 securityDomainId 的 Provider 请求被拒绝并记录。

Dogfooding：将 M8/M9 的 dogfooding 任务集原样切换到 CloudflareSandboxProvider 重跑，对比 Local 与 Cloudflare 的证据一致性。

### Milestone 11：生产级存储、多节点 HA 与身份分离

ADR 0019 增量：HA 下同一 effect 只能有一个当前 reconcile/compensation owner；抢占必须 fencing，operator approval、补偿授权与残留外部效果全部进入审计。

Goal：生产参考部署落地——Temporal self-host + PostgreSQL + S3/MinIO；多节点 HA；在 M9 已交付的最小 scope-bound 注册身份基础上，扩展生产级 AuthN/AuthZ——身份分离同时覆盖 Worker/Verifier/Publisher 的 workload identity 与写入域分离（candidate/evidence/publication 分域），以及操作者/API 提交入口身份（区分人操作者与 API 调用者，多节点与多用户）；生产远程提交入口（调用者身份认证、按 repository/project 授权、审计）；远程管道的 secret scan 与日志脱敏。**M11 只扩展 HA、多节点与多用户授权策略**：TLS、双向身份校验、credential rotation/revocation 与 replay protection 等 transport 安全基线已按 ADR 0018 §12 自 M9/M10 各远程能力首次 enable 起生效，M11 不补、也不得推迟首次安全基线。

非目标：不承诺多租户服务化；不引入 auto-merge。

退出门禁：

- 多节点故障转移后无静默状态漂移，恢复仍满足 M9 的 fencing/60 秒口径；故障转移与灾备切换不改变 controlPlaneId（HA/灾备中保持稳定的逻辑权威身份，不是进程实例），权威对象归属随原 authorityNamespaceId 延续；
- 生产远程入口验收：在 ADR 0018 §12 transport 安全基线（TLS、双向身份、rotation/revocation、replay protection，自首次远程 enable 已生效）之上，调用者身份认证（人操作者与 API 调用者区分）、按 repository/project 的授权、提交/授权决策审计与多节点/多用户策略全部启用；未认证、越权、跨 scope/跨 authorityNamespaceId 提交与跨 Port 能力请求全部被拒绝并记录；
- Worker 环境获取 Publisher 凭据的探测次数为 0；重试无重复副作用；
- 日志抽样无敏感值；写入域越权 Fixture 全部失败；
- 升级外部组件（Orchestrator/数据库/对象存储）不破坏生命周期一致性测试。

Dogfooding：Marshal 自身的发布、审计与长跑任务在生产形态存储上运行，产出可归因的预算与恢复统计。

### Milestone 12：开源部署、版本化 Provider SDK/协议、多语言 SDK 与长稳验证

ADR 0019 增量：各 Port 独立交付 effect/receipt/reconcile conformance。OpenHands 只可作为可选 Agent Provider；ACP 只可作为某一 AgentAdapter/Facade 的 transport；A2A 只作为未来外部 gateway 候选。Planning/Review remote Port 启用前必须另行 ADR，三者均不阻塞核心路线。

Goal：开源部署形态与文档；基于 M9 已冻结的 versioned HTTP/JSON + OpenAPI wire contract 交付多语言 SDK 与 OpenAPI 资产（wire contract 与事件流已在 M9 首次冻结，M12 是其 SDK/文档化，不是首次定义）；版本化 Provider SDK/协议必须覆盖六类 Provider：Agent、Sandbox、Verification workload executor、SCM/Publisher transport、Artifact、Secret（子进程或独立安装包、显式信任、认证注册；六类 Provider 分属不同 protocol family，各有各自的 conformance suite，不共享家族间 suite）；多拓扑 conformance（对每个具体 Port/protocol family，embedded、单节点 server、Push、Pull runner 运行该族统一的 conformance 套件，含 Push/Pull DispatchLease outcome/invariant equivalence——比较 normalized business trace 与业务不变量，不比较逐步 wire trace）；全局并发与调度公平性；长稳演练：72h soak 通过后再进行 7d soak/chaos。

非目标：不承诺多租户 SaaS；不引入 auto-merge；不承诺绝对安全或绝对质量；不让 Verification/SCM/Publisher Provider 获得裁决权。

退出门禁：

- 外部贡献者仅凭开源文档可完成自托管部署；
- 六类 Provider 均经认证注册，注册产物为 ProviderRegistration + 不可变 ProviderCapabilitySnapshot（冻结 protocolVersion、providerType、providerName、providerVersion、capabilities、conformanceEvidenceRef、scope 与撤销/失效语义），legacy v1alpha1 CapabilitySnapshot 只经 fail-closed mapper 转换并记录 sourceCapabilitySnapshotDigest；未知或不兼容 protocolVersion fail closed；注册产物含 attestation 全链绑定（securityDomainId、providerInstanceId、effective configDigest、trust root 含 key id/rotation），任一绑定项变化触发新 immutable 快照/证据与 eligibility 重判，key rotation/config 变更后复用旧证据的 fixture 全部失败（ADR 0018 §11）；
- 每个 Port 的 versioned protocol family 完成冻结并通过按族 conformance（独立 audience、AuthZ scope、request/response schema、error 模型、幂等约定、撤销语义与 conformance profile）；六类 Provider 分属不同 protocol family，各族运行各自 conformance suite，不共享 suite；跨 Port 复用 token、schema 或 operation 的 fixture 全部失败（ADR 0018 §16）；各远程能力首次 enable 均满足 ADR 0018 §12 transport 安全基线；
- 插件按其所属 Port 的 protocol family 经该族 conformance 门禁准入，`hardened` 声明规则与 M8–M10 一致；Verification/SCM/Publisher Provider 只能执行或传输，决定 gate/ReviewDecision 与发布权限的 Fixture 全部失败，最终裁决仍在 Core；
- 72h 与 7d soak/chaos 期间无静默状态漂移，升级演练可回滚；
- 并发与公平性指标可度量并绑定事件账本。

Dogfooding：Marshal 自身仓库的 issue 分诊与文档任务经开源部署形态运行；soak/chaos 演练以 Marshal 自任务为负载。

### Milestone 13：Goal orchestration

M7–M12 完成后，平台可靠运行的是彼此独立的 Task；复杂需求由 M13 按 [ADR 0019](adr/0019-deterministic-control-plane-typed-execution-and-goal-admission.md) 承接。M7 只冻结 Project/Goal 的存在性、authority ownership 与多 Run 原则，完整对象与控制器语义不追溯写成 M7 已实现。

Goal：实现 Goal API 与确定性 Goal 控制器。持久对象包括 `GoalSpecRevision`、`GoalPlanProposal`、`AcceptedGoalPlanRevision`、`GoalNode`/`GoalEdge`、`GoalBudgetLedger`/reservation、`GoalIntervention`、`GoalOutcome` 与 `EvidenceDependencySet`/eligibility event，全部由 `authorityNamespaceId` 拥有、Core-only write。

实施顺序：

1. **M13.0 契约与 negative fixtures**：冻结 Schema、digest、revision CAS、Goal 生命周期、budget reservation/settle/release/expire/reconcile 状态机与 Evidence 依赖模型；未通过不得启用 Planner dispatch；
2. **M13.1 deterministic plan admission**：`GoalPlanProposal → Core admission → AcceptedGoalPlanRevision`；依次校验 scope、node/edge、allowlist、完整 effective graph、累计预算 availability/estimate 与 plan approval；最后同事务写 accepted revision + live reservations + materialization outbox；
3. **M13.2 执行、fan-out/fan-in 与重规划**：materialize 既有 Task/Run；完成跨 revision cycle 检查、immutable revision、pending-node supersession、依赖驱动 Evidence eligibility 与独立质量评估；
4. **M13.3 人工控制与长稳**：Goal `PAUSED`/resume/abort、`pauseReason`、`drain-active|cancel-active`；cancel 必须先写 intent 并 fence，再 Signal/Inspect/Reconcile/转换/释放；完成 kill/restart 与 soak。

累计 guardrail 至少包含 `maxNodes`、`maxDepth`、`maxFanOut`、`maxConcurrentNodes`、`maxPlanRevisions`、`maxTotalRuns`、`maxTotalAttempts`、`maxWallTime`、`maxCompute`、`maxTokens`/成本、`maxArtifactBytes`，以及 executor kind、repository、路径与 side-effect class allowlist。限制按整个 Goal 记账并先 reserve 后 dispatch，不能靠多次 replan 绕过。

非目标：不承诺多租户 SaaS；不引入 auto-merge；Planner 不直接写 ledger、创建 Run、发布或补偿；不以 P2P Agent 自由通信形成第二权威；不把 transcript 当作状态；不允许跨 Goal 隐式依赖；不新增 Run 的 `WAITING_HUMAN_APPROVAL`，Run `BLOCKED` 仍为终态。

退出门禁：

- oversize/depth/fan-out/cycle（含跨 revision）、dangling/self/duplicate edge、node identity digest substitution、越权 executor/repository/side effect 全部拒绝，且不创建 Run；
- 并发 budget reservation 不超卖；missing/stale approval、accepted-revision CAS conflict 或 outbox transaction failure 后 live reservation 数为 0；replay 不产生第二 reservation；concurrent reserve/release、dispatch lost-response、double settle/release、stale revision release、reservation expiry 与 `actual > reserved` fixture 均幂等或 fail closed；已完成/运行节点不能被 replan 删除、改义或重复 dispatch；
- 上游 Artifact、Candidate、base、Policy 或 Verifier capability 改变后，旧 descendant Evidence 不能驱动 accept/publish；无关依赖不被全局失效；
- `drain-active` 与 `cancel-active` 在 `RUNNING`/`VERIFYING`/`PUBLISHING`/terminal-cleanup 四类场景不制造无恢复记录的非终态 Run；旧 handle 在 cancel intent 后、Run transition 前后晚到写入均不能进入当前 graph；pause/restart/resume 不重复 dispatch；resume 重新校验 budget/evidence/provider eligibility/policy；
- Goal 的计划、重规划、预算、终止、人工干预与补偿提案全部可从 ledger 回放；Planner 生成补偿只能是 proposal，Core/Policy 决定是否接纳；
- 预算耗尽、终止或不收敛均保存 GoalOutcome；失败或阻塞不静默放弃。

Dogfooding：用一个复杂 Goal 驱动本仓库的并行 fan-out/fan-in，至少包含一次合法 replan、一次 proposal reject、一次人工 pause/resume、一次上游 Evidence 失效与重新验证、一次 kill/restart；最终已接纳成果 exactly once，并与人工分解对照评估。

### 纵向切片与 Provider 替换的统一验收口径（ADR 0017 修订）

1. **M8 embedded/local 纵切**：单二进制 embedded 模式 in-process 跑通幂等提交（loopback/受信任本地边界）→ 冻结 Run → durable `READY` → scheduler claim + fencing → Local SandboxProvider → AgentAdapter → checkpoint/log/evidence → 独立 Verifier sandbox（业务验证 workload，conformance probe 另按 §2 拓扑运行在被测 Provider 的 target allocation 内）→ `REVIEW_PENDING`/`ACCEPTED`（暂不自动 publish）；SPI 同时实现二维要求、内容寻址 Stage、workloadRole/principal 身份 fencing 与 replacement allocation Restore；
2. **M9 服务化**：同一套用例切换为固定 `marshal control-plane serve` + TaskSubmission/Run Public API（versioned HTTP/JSON + OpenAPI、SSE 断线续传）重跑，embedded 模式保持兼容；Push/Pull DispatchLease 按同一唯一状态机满足 capability matching、ack/heartbeat/deadline/generation/fencing 且两拓扑满足 outcome/invariant equivalence（比较 normalized business trace，不比较逐步 wire trace）；非 loopback/in-process transport 自首次 enable 起满足 ADR 0018 §12 transport 安全基线；在 `RUNNING`/`VERIFYING` 期间 kill -9 Runtime：60 秒内 Inspect/Reconcile，旧 execution handle 被 fencing 拒绝，无双写、无丢证据；
3. **M10 Provider 替换**：仅替换为 CloudflareSandboxProvider 跑同一 conformance/E2E，用例不变；`hardened` 声明必须持有有效 ConformanceEvidence。

### 与云端能力审计阶段的对应

| 研究阶段（cloud-agent-readiness-2026） | 对应 Milestone |
| --- | --- |
| Phase 1：Durable Runner PoC | M8–M9 |
| Phase 2：无人值守云端执行 | M10–M11 |
| Phase 3：复杂任务编排（Project/Goal、跨 Run 记忆） | M13 Goal orchestration（存在性/归属由 ADR 0016 冻结，计划接纳与控制器语义由 ADR 0019 冻结） |
| Phase 4：规模化与多租户评估 | 仍为评估项，不在 M7–M13 承诺内 |

## 延后阶段

以下事项不属于 M7–M13 承诺：

- GitLab Publisher；
- CI Webhook Receiver；
- MCP 或专用 Desktop/Remote Service Facade；ACP 可作为可选 AgentAdapter transport，A2A 可作为未来外部 gateway，均不作为内部权威协议；
- Telemetry、Cost Accounting 与 Policy-based Routing；
- Web UI 交互面（观察能力实现为 Runtime 事件流只读投影，交互形态未冻结）；
- 任何 Automatic Merge Policy；
- 多租户服务化（属于评估项，威胁模型评审通过后才讨论）。

原“Hardened Container/VM Profile”由 M8/M10 的 conformance、独立签发的 `ConformanceEvidence` 与 `hardened` 声明规则承接（ADR 0017）；原“增加 SQLite/Index”不再是路线项（单机开发沿用文件账本，生产存储演进见 M9/M11）。

## MVP 可用定义

至少一个真实 Worker 必须从 Frozen TaskSpec 出发，经过独立 Verification、Codex Review、有界 Rework、GitHub Draft PR 与 Outcome Export，完整完成 Fixture Task；同时 `security-model.md` 中针对 Local Profile 的安全验收测试全部通过。
