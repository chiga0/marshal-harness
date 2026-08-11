# ADR 0019：确定性控制平面、Typed Execution、Goal 计划接纳与补偿语义

- 状态：已接受（Accepted；维护者 2026-08-11）
- 日期：2026-08-11
- 决策来源：维护者要求基于既有审计重新整理设计与 Roadmap；三路独立只读审计分别复核 Typed Executor 边界、外部副作用补偿与 M13 Goal 编排后，确认 ADR 0016–0018 的耐久 Control Plane 方向正确，但下列契约尚未冻结。本 ADR 在不回退既有不变量的前提下补齐这些边界。
- 关联：[ADR 0004](0004-independent-verification.md)、[ADR 0007](0007-intent-first-publication.md)、[ADR 0010](0010-controlled-autonomy-and-intervention.md)、[ADR 0016](0016-durable-runtime-and-sandbox-provider.md)、[ADR 0017](0017-provider-neutral-sandbox-contract.md)、[ADR 0018](0018-control-plane-and-provider-ports.md)、[Runtime 架构](../runtime-architecture.md)、[实施计划](../implementation-plan.md)

## 背景

ADR 0016–0018 已冻结长寿命 Runtime、唯一业务权威、可插拔 SandboxProvider、耐久租约、独立 Provider Port 与 C/S 终态。这一方向无需重置，但文档仍有四组容易导致错误实现的空白：

1. “Supervisor + 多 Worker”尚未区分确定性权威与 LLM 语义执行。若把 Supervisor 实现为持有全局上下文的 LLM，会形成第二业务权威、单点上下文瓶颈和不可重放决策；
2. “Typed Executor”尚未定义输出的权威含义。Candidate、Evidence、Assessment 与 Publication Receipt 可能被错误归并成一个通用结果协议；
3. M13 只说明 Project/Goal 驱动多个 Run，未冻结 Planner proposal 的接纳边界、DAG 累计上限、重规划、证据依赖失效和人工暂停/恢复；
4. ADR 0016 已把通用 `SideEffectIntent`/`Receipt` 作为目标对象，但当前实现只有受控发布的专用闭环与本地 cleanup tombstone，尚无通用副作用、对账和补偿状态机。失败若被描述成 rollback，会掩盖外部世界已观察到的事实和补偿本身也可能失败。

## 决策

### 1. Marshal 的 Supervisor 是确定性 Control Plane

“Supervisor”只作为产品心智模型的别名，规范名称仍是 Marshal Core/Control Plane。它是确定性、可重放、由 `authorityNamespaceId` 拥有的业务权威，负责：

- 生命周期、策略、预算、租约、fencing、幂等、证据接纳、ReviewDecision 物化与发布授权；
- 从 append-only authority ledger 恢复状态；
- 依据 Schema、当前权威状态和冻结 Policy 接纳或拒绝外部 proposal/claim；
- 生成 typed command，但不依靠 LLM scratchpad 充当持久状态。

LLM、人或外部系统都只能提交输入、proposal、claim 或 assessment，不能直接写 authority ledger、创建权威 Run、宣布 gate 通过或获得发布权限。Temporal 等 `DurableExecutionEngine` backend 仍只提供传输、timer、signal 与 delivery retry，不拥有业务状态机。

### 2. Typed Execution 是内部分类，不是通用 Provider 协议

Marshal 采用下列 typed execution 心智模型：

| 类型 | 典型执行者 | 输出 | 权威含义 |
| --- | --- | --- | --- |
| Plan | Planner、人或 API client | `GoalPlanProposal` | 不可信提案，必须经 Core admission |
| Implement | Coding Agent | Candidate、diff、声明 | 候选成果，不构成验证证据 |
| Verify | 独立验证 workload / 确定性工具 | Evidence、VerificationReport | 可被 Core 接纳的事实输入，不能自行作 ReviewDecision |
| Review | 主 Agent、Reviewer 或未来 Review Port | Assessment / decision proposal | 语义判断输入；Core 绑定当前 evidence 后才物化 `ReviewDecision` |
| Publish | Publication Provider | `SideEffectReceipt` / Publication Receipt | 外部效果观察；Core 对账接纳后才成为权威事实 |

这五类工作可以共享内部执行基座的 queue、lease、heartbeat、cancel、deadline、日志、Artifact 与 checkpoint 机制，但不能共享一个无类型 `/execute`、universal envelope、credential、token、Schema 或 conformance suite。ADR 0018 的按 Port protocol family 与三信任域隔离保持不变。

- 现有 Sandbox `workloadRole` 继续保持封闭枚举 `worker|verifier`；本 ADR 不把 Planner、Reviewer 或 Publisher 塞入该枚举；
- Implement 映射到既有 Agent/Sandbox dispatch；Verify 映射到独立 Verification workload；
- 当前 Plan/Review 继续经 Public application Port、文件 Review Bridge 或受控 Lead Agent 集成提交 proposal；若未来启用远程 Planning/Review Provider，必须先以新 ADR 冻结各自的 Port、principal、protocol family、接纳规则与 conformance；
- Publisher 可以复用调度机制，但始终位于 Publication 信任域，永不成为 Sandbox workload。

Provider 是能力与传输实现；Executor 是某一次 typed workload 的执行者。二者不得混用。

### 3. 输出类型与接纳边界不可合并

Core 必须分别处理：

- `Candidate`：绑定 task/run/attempt/allocation/generation、base 与内容 digest；
- `Evidence`：绑定 subject、生成主体、环境、Policy、Verifier capability 与依赖 digest；
- `Assessment`：绑定被评估 Candidate 与 evidence set，只能提出 accept/rework/reject/block；
- `SideEffectReceipt`：绑定既有 intent、目标身份、观察摘要与 reconcile identity。

Provider 报告“完成”仅是接纳输入。Core 必须先执行所属 Port 的接纳规则，再以 atomic compare-and-append 写入 authority ledger：

- Candidate 与 dispatch-bound Evidence 校验当前 lease 的 `generation`/`fencingToken` 精确相等、`expectedSequence`、digest 与 registration/lease eligibility；不存在“较大 generation 自动有效”的规则；
- Assessment 经 Public application Port/Review Bridge 校验 actor、scope、ReviewPacket、Candidate/Evidence digest、Policy 与 sequence，并反向拒绝 workload lease 字段；
- SideEffect/Publication Receipt 校验所属 Port 的 intent、authorization、target identity、request digest、reconcile identity 与 current-ledger 状态，不伪造无关 DispatchLease；
- Artifact/Secret 等其他输入继续按 ADR 0018 各自 Port 的 scoped handle、digest、scope、expiry 与 AuthZ 接纳。

任何内部规范化记录都不能替代 Port-specific identity、AuthZ、fencing 或 current-ledger recheck。

### 4. Goal 计划必须先 proposal、后确定性接纳

M7 只冻结 `Project`/`Goal` 的存在性、权威归属和“由多个有界 Run 推进”的原则；以下字段与控制器语义由 M13 实现：

- `GoalSpecRevision`；
- `GoalPlanProposal`；
- `AcceptedGoalPlanRevision`；
- `GoalNode`/`GoalEdge`；
- `GoalBudgetLedger` 与 reservation；
- `GoalIntervention`；
- `GoalOutcome`；
- `EvidenceDependencySet` 与 eligibility/supersession event。

上述对象全部由 `authorityNamespaceId` 拥有、只允许 Core 写入。Planner 只提交 `GoalPlanProposal`，手工计划与 LLM 计划走同一 admission。Core 按固定顺序执行：

1. Schema、canonical digest 与 revision CAS；
2. Goal identity、repository/project/authority scope；
3. node/edge 完整性与 node identity 冲突；
4. executor kind、repository、路径与 side-effect class allowlist；
5. dangling/self/duplicate edge、cycle、depth、fan-out 与并发上限；
6. 对整个 Goal 累计的预算 availability 与 estimate 计算（此步不持久化 live reservation）；
7. Policy 要求时校验精确绑定的 plan approval；
8. 在同一 CAS/transaction 中原子写入 accepted revision、对应 `reserved` reservation records 与 materialization outbox；任一部分失败则三者都不落账。

任何一步失败只追加可审计的 rejection，不创建 Task/Run、不执行副作用。至少冻结并累计执行 `maxNodes`、`maxDepth`、`maxFanOut`、`maxConcurrentNodes`、`maxPlanRevisions`、`maxTotalRuns`、`maxTotalAttempts`、`maxWallTime`、`maxCompute`、`maxTokens`/成本与 `maxArtifactBytes`。不关心 token 成本不等于允许无界执行；上限首先用于故障控制、资源公平与失控防护。

预算 reservation 也是 append-only 状态机：`reserved → committed → settled`，或从 `reserved` 转为 `released|expired`。live reservation 只能与 accepted plan revision 和 materialization outbox 同事务产生；missing/stale approval、accepted-revision CAS conflict 或 outbox transaction failure 后 live reservation 数必须为 0。记录必须绑定 `reservationId`、idempotency key、Goal/node/plan revision、command 与 estimate；只有 Core 可用 CAS/current-ledger recheck 提交、结算、释放或过期。dispatch 接纳后进入 committed；终态按 actual usage settle 并记录 estimate 差额；dispatch 失败、pending node 被 supersede、pause/abort 或 deadline 到期必须走可重放 reconcile，不能直接删 reservation。`actual > reserved` 时停止新 dispatch 并按 Policy pause/block，不能以补记负余额继续超卖。重复 settle/release、stale revision release 与 lost-response replay 必须幂等或 fail closed。

### 5. Plan revision、重规划与并发

- `AcceptedGoalPlanRevision` 不可变、append-only，并绑定 proposal、Policy 与 budget snapshot digest；
- 重规划校验 overlay 后的完整 effective graph，防止跨 revision 成环或逐轮膨胀；
- 已完成或正在运行的 node 不得删除、改义或改写历史；pending node 可以 supersede，但必须保留原因、lineage 与旧 revision；
- 同一 node identity 携带不同 digest 时 conflict fail closed；
- 重规划不能修改已冻结 Run；需要改变冻结输入时创建新的 TaskSubmission/Run；
- 不同 worktree 的写节点可以并发；同一 worktree/Attempt 仍最多一个写入者。fan-out/fan-in 由依赖与预算共同约束，不允许 P2P 自由通信绕过 Core。

### 6. Evidence 保持不可变，当前适用性由依赖图派生

Evidence bytes 与历史事实永不原地改写或删除。`EvidenceDependencySet` 至少绑定：

- `subjectDigest`、`baseSha`、`environmentDigest`、`policyDigest`；
- `verifierCapabilityDigest`；
- `upstreamArtifactDigests`；
- 可选 `validUntil`。

Core 通过追加 supersession/ineligibility event 派生“该 Evidence 是否仍可用于当前 gate”：Candidate 改变使旧证据不适用于新 Candidate；base、Policy、Provider capability 或上游 Artifact 改变，只失效依赖该项的 gate 与后继节点，不做“main 一变化全部失效”的粗暴处理。强制 gate 的失败不能被 LLM assessment 或人工措辞改写为通过；Policy 允许的 waiver 必须是独立权威记录，并精确绑定 evidence、scope、actor、理由与有效期。

### 7. 外部副作用使用 append-only 对账与补偿，不使用状态 rollback

失败不会回滚权威历史。对可识别、Policy 允许的外部资源，Marshal 可以执行可审计的 cleanup/compensation，并完整保留 forward 与 compensation 两条事实链。

Core 内部 authority-ledger 的规范化记录至少包含：

- `SideEffectIntent`：effect/owner identity、Port/operation、target ref/digest、request digest、command/idempotency identity、Policy 与 authorization digest、`purpose`、`dispositionClass`、deadline；
- `SideEffectReceipt`：intent digest、`applied|not_applied|ambiguous|conflict`、Provider resource identity/observed digest、actor provenance、reconcile identity；
- `ReconcileRecord`：`absent|applied|partially_applied|conflict|unknown` 的观察，以及 accept/retry/cleanup/compensate/block 决定。

这些对象不是 Provider wire Schema 或 universal Executor protocol。每个 Port 返回自己的 versioned receipt/observation；Core 必须先执行该 Port 的 identity/AuthZ/fencing/operation 校验，再经显式、版本化、fail-closed mapper 写入内部记录，并保留 `sourcePort`、`sourceReceiptDigest` 与 `sourceProtocolVersion`。不同 Port 不共享 token、wire Schema、operation 或 conformance suite。

`purpose` 为 `forward|cleanup|compensation`；补偿 intent 必须绑定被补偿的 effect 与 receipt digest。`dispositionClass` 为：

- `ephemeral-cleanup`：Sandbox、Stage 与临时资源，默认要求有界清理；
- `compensatable`：Policy 显式允许时可关闭 Draft PR、删除专属 branch/object 等；
- `irreversible`：通知、费用、泄密、merge、release、deploy 等，不得伪装成可回滚。

补偿本身也是可能失败的副作用，必须重新走 intent → execute → receipt → reconcile，并拥有独立授权、幂等身份、deadline、重试上限和所属 Port fencing。ambiguous 结果必须先对账，不得直接创建新 intent；delivery retry 复用同一 `commandId`，不消费业务 Attempt/rework 预算。自动补偿仅限 target identity 精确可证且 Policy 已授权；高权限、不可逆或身份冲突一律 fail closed，追加 blocker/reconcile record，并仅通过当前 lifecycle 允许的转换处置；不能假定任意状态都可直接进入 Run `BLOCKED`。远端对象默认保留，除非冻结 Policy 明确授权清理。

### 8. 人工等待属于 Goal 控制，不改变 Run 终态语义

本 ADR 不新增 `WAITING_HUMAN_APPROVAL` Run 状态。`BLOCKED` 仍是 Run 终态，解除原因后通过关联的新 Run 继续。

M13 为 Goal 增加 `PAUSED` 控制状态与 typed `pauseReason`：`awaiting-input|operator|policy|budget-approval`，并冻结两种 pause mode：

- `drain-active`：只停止新 dispatch，active lease 继续到 deadline；完成后正常接纳 Outcome，再释放资源；
- `cancel-active`：Core 先原子写 cancel intent、停止新 dispatch 并把 lease 置为持久 `canceling`；随后原子 generation bump/撤销旧 lease eligibility 以 fence 旧写入，再 Signal/kill，晚到结果只进 quarantine；然后 Inspect/Reconcile 外部真实状态，按现有合法转换形成 Outcome 并决定 retry/new Attempt/关联新 Run；最后释放资源。`canceling/reconciling` 记录必须有 deadline，覆盖 fencing 后到终态对账完成前的恢复语义。

Goal `PAUSED` 本身绝不直接改 Run state，也不能制造没有 active lease 且无 Outcome 的 `RUNNING`/`VERIFYING` Run。若当前状态没有合法转换，Core 追加 blocker/reconcile record 并 fail closed。resume 必须是带 actor、scope、reason、expectedSequence 的 authority ledger event，并重新校验预算 reservation、Evidence 适用性、Provider eligibility 与 Policy。人工输入产生新的 GoalSpec/Plan revision 或新 Run，不能改写历史对象。

### 9. Roadmap 落点

- **M8**：纳入共享执行基座的类型与 negative fixture，但不改变 ADR 0018 §7 顺序；任何 claim/lease dispatch activation 仍必须等到 Schema、mapper、耐久注册/恢复与 validation 完成后才在最后一步启用。M8 同时冻结 Core 内部 SideEffect authority-record Schema，首批只启用 Sandbox allocation/terminate、Stage 与本地 cleanup operation，覆盖 orphan cleanup 与 lost-response fixture；
- **M9**：扩展 authority-scoped SideEffect ledger 的 operation 覆盖面、单一 outbox/command seam、连续进度事件的权威/观察分层与 crash recovery；不替换 M8 已冻结的内部 Schema；修复 expired cleanup 后可能遗留 orphan worktree 的恢复窗口；
- **M10**：Cloudflare Provision/Stage/Persist/Hydrate/Terminate/TTL 全部进入对账与 leak scan；不得在补偿契约前宣称资源生命周期可靠；
- **M11**：冻结 HA 下单一 side-effect/compensation owner、抢占 fencing、operator approval 与审计；
- **M12**：交付按 Port 的 effect/receipt/reconcile conformance；Planning/Review remote Port、ACP/A2A/OpenHands 等仅作为可选生态扩展，启用前另行 ADR，不阻塞核心；
- **M13**：按 M13.0 契约与 negative fixtures、M13.1 deterministic plan admission、M13.2 fan-out/fan-in + replan + evidence dependency、M13.3 pause/resume + soak 的顺序实现 Goal orchestration。

## 保留的不变量

ADR 0016–0018 与仓库治理中的全部不变量继续有效，尤其是：Core/authorityNamespaceId 唯一写权威 ledger；Worker 不自证；Worker/Verifier 不同 principal 与 allocation；单 worktree/Attempt 单写者；ReviewDecision 精确绑定当前 Evidence；强制失败 gate 不能被 Assessment 覆盖；Publisher 位于独立 Publication 信任域；按 Port 协议/credential/conformance 隔离；能力不足 fail closed；陈旧 generation 精确不匹配即拒绝并 quarantine；普通宿主进程不称 hardened；失败/阻塞保留 Outcome；Merge 默认禁用；`.marshal/` 不进入业务提交。

## 后果

- M7 原有退出门禁与 `PASSED` 状态不回滚；本 ADR 是 M7 后的设计增补，也是 M8–M13 新的实施前置条件；
- M8–M13 仍为 `PLANNED`，本 ADR 接受不表示 Typed Execution、通用 SideEffect 或 Goal 控制器已经实现；
- Local MVP 的主 Agent/Worker/Verifier/Publisher 交互保持兼容，只重新明确“外部生成内容，Core 接纳并物化权威事实”；
- 新增持久 Schema 与远程 Planning/Review Port 必须按本 ADR 的阶段落地并通过 negative fixture，不得通过一个通用 Executor 协议绕开 ADR 0018。

## 备选（已否决）

- **LLM Supervisor 维护全局状态并直接调度**：否决。不可重放、上下文膨胀，并形成第二业务权威；
- **所有角色共用 universal Executor RPC**：否决。破坏按 Port credential、Schema、接纳与 conformance 隔离；
- **把 Planner/Reviewer/Publisher 加入 Sandbox workloadRole**：否决。冲突于既有封闭枚举与信任域；
- **失败时回滚 ledger 或假装外部副作用未发生**：否决。审计事实不可删除，补偿也可能失败；
- **为等待人工输入新增可无限悬挂的 Run 状态**：否决。Run 保持有界，长等待由 Goal `PAUSED` 承担；
- **只限制单次 plan 大小**：否决。Planner 可跨 revision 逐步膨胀，必须按整个 Goal 累计预算并原子预留；
- **任意仓库变化使全部 Evidence 失效**：否决。过度重算且不能表达真实依赖，应采用依赖驱动的适用性派生。
