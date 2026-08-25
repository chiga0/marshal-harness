# ADR 0046：Agent 生成决策输入的治理边界

- 状态：提议中（Proposed，2026-08-25）；尚未接受，不构成实现授权或当前产品能力
- 关联：[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)、ADR 0014、ADR 0018、ADR 0019、ADR 0043、ADR 0044、[设计审计报告](../audit-report.md)
- 影响范围：前期研讨、跨 Worker 信息消费、复盘记录、跨 Goal 学习

## 背景

Marshal 已经把执行结果分成 Candidate、Evidence、Assessment 和 Receipt，并要求外部结果经 ResultIngress 后才能成为 authority fact。但复杂 Goal 还有另一类输入：调研结论、方案异议、Worker 回答、复盘解释和历史经验。它们通常由 Agent 生成，会影响后续计划或执行，却不天然等于事实或授权。

若这些内容通过 transcript、共享聊天、mailbox 或实时知识检索自动进入另一个 Agent 的上下文，会产生四类问题：

1. 同一冻结输入在不同时刻得到不同上下文，无法 replay；
2. 一个受 Prompt Injection 影响的 Worker 可以污染另一个 Worker；
3. 决策只存在于会话记忆，崩溃后无法恢复，也无法解释来源；
4. 自由文本可能被误当作 Policy、指令、Evidence 或权限扩张依据。

本 ADR 提议为这类内容建立一个共同的治理边界，同时决定三项候选能力的近期取舍。它不会建立新的 `WorkerProvider`、mailbox、Knowledge Provider 或 Core 状态机。

## 决策提案

### 1. 统一的跨边界输入规则

任何 Agent 生成的语义内容，只要跨越下列任一边界并影响后续决策，就必须先成为受治理的不可变输入：

- 从 Discovery/Deliberation 进入 Plan；
- 从一个 Worker 进入另一个 Worker；
- 从 Run/Goal closeout 进入未来 Goal；
- 从非权威 observation 进入 admission、review、replan 或 execution 的冻结输入。

受治理输入至少满足：

```text
bounded typed payload
→ canonical bytes + content digest
→ producer principal + provenance
→ declared purpose + target audience
→ explicit selection / admission
→ frozen downstream reference
→ supersession without rewriting history
```

具体要求：

1. payload 有封闭类型或显式 media type、大小上限和 Schema/version；
2. canonical bytes 与 digest 可独立重算，存储使用 immutable put-if-absent；
3. provenance 至少绑定 producer、Goal/Run/Attempt、生成时间、输入摘要和适用 scope；
4. purpose 与 audience 明确，不能把“给 Lead 的调研意见”直接复用成“给 Worker 的执行指令”；
5. 下游必须显式引用并通过对应 admission；不得自动继承整个 transcript 或最近消息；
6. 引用的 digest 进入下游冻结输入集，replay 不做 live query；
7. 内容始终以 **untrusted data** 呈现，不得解释为 Policy、credential、capability、DRC、lease 或系统指令；
8. 修订产生新对象和 supersession 关系，不原地改写旧内容。

该规则是必要条件，不是充分条件。进入 Kernel authority ledger 仍需现有 typed admission；进入 ReviewDecision 仍需 Evidence 与 required gate；触发外部效果仍需 PublicationAuthorization 或对应 effect authorization。

### 2. 事实、解释和改进提案必须分开

复盘和调研不得把所有输出包装成“报告事实”。至少区分：

| 类别 | 来源 | 权威含义 |
| --- | --- | --- |
| Fact projection | ledger、Outcome、Evidence、Receipt 的机械投影 | 可追溯到既有 authority fact，但投影本身不新增因果结论 |
| Analysis assessment | Agent/人对原因、取舍和模式的解释 | 不可信语义判断，必须带 provenance、证据引用和置信度 |
| Change / lesson proposal | 流程、Policy、架构或知识改进建议 | 仅是 proposal，不能自行修改系统 |

`infra-failure`、模型能力不足、计划错误等因果归类只有在存在封闭分类规则和故障域外 observation 时才能成为机械事实；否则必须留在 Analysis assessment。

### 3. 前期研讨：接受操作试行，产品化延后

R3–R6 期间允许 Stage 0 操作流程：

- 用 `publication:none` 的独立调研 Run 产生互斥路径的报告；
- 所有调研者消费同一份 Problem Brief；
- Lead 人工汇总共识、dissent、open assumptions 与证据引用；
- 只有显式 `GoalPlanProposal` 或当前 TaskSpec/plan 流程进入 admission；调研报告本身不能创建 Run、放宽 scope 或增加预算。

Stage 0 不新增 Core 状态，不声明 Discovery 已产品化。R6 后若产品化，必须另行冻结 typed finding/option、网络与来源治理、dissent carrier、Goal controller 接线，以及新的只读 workload role/profile。当前 `sandbox.WorkloadRole=worker|verifier` 不得为了 Discovery 临时放宽，也不得让 Discovery/Retrospective 冒充 verifier。

### 4. 复盘：记录、分析、学习三段分离

近期只做两项可撤回的操作试行：

1. 从已存在的 ledger、Outcome、Evidence 与 Receipt 生成事实 closeout；
2. 由 Lead/参与者产生独立的 Analysis assessment 和 change proposal。

自动跨 Goal 学习保持 `DEFERRED`，直到同时满足：

- `ResourceEnvelope` 与 Provider-independent failure attribution 已冻结并落地；
- 出现可测量的重复任务族，证明跨 Goal 复用有正 ROI；
- 知识以 immutable、versioned snapshot 被引用，其 digest 进入 Attempt 冻结输入；
- planning/execution 路径禁止 live knowledge query；
- evidence packet 使用 allowlist + redaction 的冻结投影和独立 principal，不暴露 raw ledger、credential、宿主路径或未筛选 transcript。

任何复盘输出都不能自动修改 Policy、预算、权限、ADR 或 required gate。

### 5. Worker mailbox：现阶段拒绝实施

当前没有证据证明 Lead 转发已成为系统瓶颈，因此不新增 mailbox、自由 P2P chat 或 A2A 群聊。近期只使用 Artifact-mediated 单向协作：

1. producer 发布 immutable Artifact ref；
2. consumer 只消费已接纳计划声明的 digest；
3. Core 在 fan-in / ResultIngress 重新核对 producer、scope、依赖和 digest；
4. scope、计划、预算、权限或 acceptance 变化继续升级给 Lead/Core。

未来若重新提出 mailbox，必须先有非规范 RFC 和真实指标，证明在给定并发度下 Lead 转发等待时间已经成为瓶颈。RFC 至少审计同 Goal Prompt Injection、消息配额、deadline、循环、crash/replay、stale sender、撤销、数据脱敏和 explain 投影。

未来的 `DependencyReady` 即使存在，也只能是可丢失的唤醒提示。Core 必须在完全没有 Worker 消息的情况下，仅凭 ledger、DAG 和 Artifact refs 独立判断依赖是否满足；提示不得成为 correctness 或 liveness 的唯一条件。

## 分阶段启用条件

| 能力 | 当前判断 | 最早动作 | 产品化前置 |
| --- | --- | --- | --- |
| 前期研讨 | `ACCEPT_OPERATIONAL_PILOT` | Stage 0 立即试行 | R6 后 ADR/Schema/role/controller/negative fixtures |
| 复盘事实记录 | `ACCEPT_OPERATIONAL_PILOT` | 立即生成 closeout | 投影 allowlist、redaction、provenance 与 explain |
| 复盘因果分析 | `ACCEPT_AS_UNTRUSTED_ASSESSMENT` | 人工、有界试行 | 不得冒充机械事实 |
| 跨 Goal 学习 | `DEFER_ON_DEPENDENCY` | 暂不实现 | ResourceEnvelope、独立归因、冻结 knowledge digest、ROI |
| Artifact-mediated 协作 | `ACCEPT_CURRENT_PATTERN` | 继续使用 | 计划声明与 fan-in recheck |
| Worker mailbox | `REJECT_IMPLEMENTATION_FOR_NOW` | 只允许 RFC | 可测瓶颈 + 完整安全/恢复审计 |

这些工作不得抢占 Issue #186 的 R3–R6 主执行链。只有 Stage 0 与轻量 closeout 可在此期间作为操作约定试行。

## 验证与退出条件

本 ADR 只有在以下条件完成后才可从 Proposed 进入 Accepted：

- 独立 reviewer 对“统一输入边界”无 P0/P1；
- 文档明确区分当前能力、操作试行与未来产品合同；
- Stage 0 至少在三个 L 级任务记录成本、人工分钟数、finding 采纳率和返工变化；
- closeout pilot 证明事实投影与主观归因可机械区分；
- negative design fixtures 覆盖 transcript 自动注入、live query 漂移、错 audience、digest 替换、跨 Goal stale lesson、同 Goal Worker injection 和 capability 文本转授；
- 所有需要新增 `WorkloadRole`、Schema、persistent state、credential 或 network boundary 的阶段都拆出后续 ADR，不随本 ADR 静默实现。

## 后果

- 好处：研讨、复盘和未来协作共享同一条可解释、可重放的输入纪律；不需要为每种 Agent 文本发明独立信任模型。
- 代价：跨阶段内容不能直接“塞进上下文”，需要显式存储、引用和 admission；Lead 仍承担语义综合责任。
- 复杂度控制：近期只增加文档和操作 pilot，不增加 runtime 组件；Worker mailbox 与自动知识注入保持关闭。
- 兼容性：不改变 ADR 0018 的权威边界、ADR 0044 ResultIngress、现有 `worker|verifier` 枚举或 R0–R6 milestone 状态。

