# Node 受管 Leader：完整任务闭环的最小执行机制

状态：`DESIGN`，依据已接受的 [ADR0094](adr/0094-trusted-single-user-role-team.md) 与用户对全程 Leader、四责边界的明确要求。代码核对基线为 `31afe269`；当前一次性 Planner、Supervisor、Reviewer 标签均不代表本机制已经实现。本文冻结行为和职责，不分配新 API 字段、角色枚举、存储格式号或迁移工具。

## 1. 一个任务权威，四种职责

| 职责 | 决定与操作 | 不得承担 |
| --- | --- | --- |
| Leader | 受管、按需的业务判断：理解需求、必要澄清、分工、集中评审意见、选择有界局部调整、提出授权交付及后验、汇总成功或失败 | 自签独立证据、改预算/权限/事实、直接启动或杀进程、直接发布 |
| Supervisor | 观察进展，聚合批次完成/失败/求助及异常，整理证据引用，通知待处理业务义务 | 业务换人/重试/改计划、判定业务成败、因疑似 stuck 或无日志直接 kill |
| Core | 唯一 Application/Store 权威：接纳事实、批准与决策；检查权限/预算/依赖/当前证据；调度已批准 DAG；执行取消、硬期限及 owner fence 等确定规则 | 用业务猜测替代 Leader，接纳作者自证，借恢复重置预算或重派整队 |
| Execution | 经 Core 许可实际 start/stop/collect 原所属 handle，提供可验证的进程/输出/cleanup 观察 | 从磁盘 PID 构造停止权、把进程结束当业务通过、替 Core 写第二套真值 |

这不是四个微服务，也不是第二 scheduler/Store。当前 `TaskSupervisor` 混合观察、循环调度、调用原 handle 和失败广播；目标是在原受管 loop 中渐进委托 Core/Execution 接口，不要求全仓改历史名称。独立 Reviewer 与验收执行提供证据；Leader 读取并判断业务意义，Core 检查其来源、绑定和当前性，三者不能互相代签。

必需 result/cleanup/receipt 通过原受控结果接口直接交 Core 接纳，不以 Supervisor 的有界聚合/采样通知为唯一传输通道；通知失败不能丢弃业务事实或把它当未发生。

用户 cancel、已冻结硬期限/预算规则、owner 失去导致的停止新派发及所属执行停止、已退出事实记录立即按 Core 规则处理，不等待 LLM。已有批准 DAG 的依赖就绪由 Core 直接派发，普通节点无需再询问 Leader。停下之后是否修正/换方案才需要 Leader；未知 cleanup 不因其建议而释放容量。

失去 owner 或 Store 写失败时，Execution 仍按预批准安全规则停止其原 live handles，但不得补写虚假的停止成功/cleanup。普通内容问题或已清理的执行失败应保留 Leader 的业务决策窗口，不沿用当前 `finish` 的“一次 clean 失败即全队 cancelling”作为新 profile 通用策略；相关危险后继先由 Core 硬门禁封闭，无关安全分支可继续，不能为了等 Leader 放行不满足依赖的工作。

## 2. 何时唤起，读什么

Leader 是可重复唤起的受管语义执行，进程无需常驻，会话/聊天记忆不是恢复真值。唤起来源必须是同库已接纳的业务事实：

- 新需求或影响计划的关键回答、需要确认的范围变化；
- Worker 批次完成、失败或明确求助，足以需要业务取舍；
- 针对精确候选收齐的独立 Review/验收结果，集中处理一轮意见；
- 交付/发布前条件满足、授权回答或原发布回执/后验结果需要汇总。

每 Task 最多一个在途 Leader 调用；同一业务义务去重，进行中的新事件聚合为下一次有效上下文。raw token、逐条工具 progress、heartbeat 和一次无变化 poll 不触发 Leader，不按每个事件重规划。疑似迟缓只能成为有来源的观察，不自动成为失败或停止事实。有效变化、原剩余预算与有限调度策略决定是否调用；无变化/结构错误不循环付费，预算不足或无法决策形成可审计的等待/非成功处置。

**容量活性也是准入条件**：Core 在 Task 与全局原硬并发/资源额度内为受管业务决策保留可用 headroom，或减少作者/其他执行并发，Leader 仍计原容量和预算，不免费豁免。要求两个作者并行的代表 profile 必须在付费前证明还能容纳必要 Leader；上限不足就明确拒绝/请求用户选择，不静默提额。两个作者同时待答或长 Reviewer 占槽时仍须有界唤起 Leader，不能等占槽作者自行结束、假释放待答席位或停止未知执行凑容量。沿原 capacity 准入与优先级实现，不另建调度器。

一次调用读取受信组合生成的有界快照：原需求与可披露上下文、批准范围/原期限和剩余预算、计划与依赖、当前选中候选 manifest/摘要、相关问题/答案/ACK、集中 Review/验收证据、历史 Leader 决定/动作结果、待办外部效果及事件 cursor。大内容从原 Depot 精确引用读取，不扫描旧 Worker 目录；摘要不能替代必需全文，无权限/缺内容不能猜。输入审计沿默认 metadata-only 及当前格式的披露限制，未知 token/cost 标 unavailable，不记 0。

快照同时绑定业务语义依赖与读取 cursor。Core 接纳建议时重读**相关**计划、候选选择、答案/ACK、授权、原决定及 owner/cancel/期限；无关 heartbeat、另一分支的普通进度不使整个建议自动失效。相关事实漂移则拒绝/重新聚合该义务，不默默刷新输入摘要或用旧建议作用于新成果。公开写请求原 revision CAS 保持，不把内部语义检查偷换成外部 CAS 豁免。

## 3. 最小行动集与有界自治

以下是内部 typed actions 的行为分类，名称不是新 wire enum。每次输出有限条结构化建议及其依据，不接纳任意 shell、HTTP URL、数据库语句或解释执行的控制代码。

| 行动 | Core 的准入条件与结果 |
| --- | --- |
| 请求业务澄清/范围确认 | 只询问影响结果或授权的缺项；问题进入原 Task 的持久义务，业务答案不变成工具/发布授权 |
| 提出计划或有限调整 | 首次展示并确认；批准自治范围内可选择已允许分工/策略，目标、成果、验收、依赖合法性与权限边界仍重验；超目标/成本/高风险先确认 |
| 请求工作或独立评审/验收 | 只形成原受管执行义务；未批准节点不得启动，作者不可承担自身唯一独立审查，验收程序由可信组合决定；依赖已就绪的正常工作由 Core 自动调度 |
| 请求局部修正/替代方案 | 绑定具体意见或失败事实、原候选和受影响 closure；不清空无关有效成果，不降低验收、不抹旧 Attempt；不支持的调整先确认或明确失败 |
| 请求交付/业务发布及后验 | 精确成果和必需独立证据当前、目标/动作获授权、预算/期限有效；发布先耐久意图，后验也是受管有界执行 |
| 汇总、等待或建议结束 | 引用原动作/证据与未完成项；只有 Core 的整体出口成立才成功。等待不能延长期限，建议不能删除失败/unknown |

自治额度在原批准时有明确边界，不能解释成“Leader 认为合理即允许”。Worker 替换/重试仍用原预算，必须先满足原清理与依赖规则；无关成果只有其输入、合同与证据仍有效时才保留。越过原目标、增加成本上限或高风险副作用要新明确确认，不能用普通业务答案绕过。

现有 [ADR0091](adr/0091-node-same-plan-local-repair.md) 的 `task.repair` 只接受显式用户请求及独立验收的精确内容拒收；当前 Leader 没有内部调用权限，语义 Reviewer 意见也不能伪造成该拒收。新机制须在同一版本化实现中接入两种可区分来源：保留原 HTTP repair 回执/规则；新增批准自治范围内、绑定独立 Review 或原内容失败证据的内部修正请求。复用原 selected-results/closure 算法和当前性检查，不冒用操作者 key，不自签负 Decision，不把任意意见硬塞入旧 repair 协议。批准后拓扑/目标变化若超出现有合同，先形成精确变更确认；本轮不做任意动态工作流。

## 4. 决定、执行结果与恢复

所有新事实仍进唯一 SQLite。实现前一次明确新格式与旧 reader 拒绝/旧根处理，不回填旧记录。最小耐久含义是：业务唤起义务与消费位置、Leader 调用及原输入/预算绑定、接纳的决定及有限动作、各动作的原执行义务和结果引用。它们不是用户需管理的新资源，也不保存隐藏推理链。

1. 在短事务中选定义务、冻结输入引用、预留原任务额度和唯一 Leader Attempt；事务外通过原 Provider/Execution 运行。未知是否已调用仍保留成本，不造“免费重试”。
2. Leader 输出经结构/数量/大小限制和相关当前性检查后，**决定、消费该义务、动作及 outbox 同事务提交**。拒绝的输出保留原因；没有 committed 决定不能执行建议。
3. Core 根据 committed 动作取得执行许可；Execution 在锁外工作。每个动作身份绑定原决定、目标和输入，结果回同库按当前 owner/原命令接纳；重放查询原事实，不生成新动作 key。
4. 恢复先处理 owner/custody/未结义务。已 committed 的决定先恢复原 action/outbox，不重新询问 Leader 再做一套；未 committed 的调用只能在原义务、真实 cleanup 和剩余额度允许下有限重试，保留已消费费用。旧代输出不得补成新决定，不重规划整队。
5. 外部副作用存在“执行了但回执未提交”的窗口。恢复只能按原目标、原幂等身份及独立可查询事实 reconcile；有明确未执行/安全幂等证据才可继续原命令，否则 unknown、封闭相关后继。SQLite 的一次提交不等于跨系统 exactly-once，不能盲重发、换 key 或用新 Task 隐藏歧义。

取消/硬期限优先于未执行的 Leader 动作；暂停不投递需要运行的新动作。已发生的发布不能通过取消撤销；补偿必须另有授权并保留原效果。Leader、Reviewer、验收与发布后的检查都计原预算/期限和容量，未确认清理不能被“业务已结束”释放。支持面沿0089/0092的原证明边界，不扩大 never-permitted 资格或用 Leader 结论替代 cleanup。

## 5. 整体结束与公开接口接缝

现实现把唯一 verifier sink 的通过直接写为 Task `completed`。**新的完整 Leader profile 必须显式 opt-in，并延迟整体 terminalization**：候选验收通过只是一个阶段；还须接纳 Leader 对目标的汇总，并完成该 Task 明确要求的授权交付/后验，无待决影响成功的义务，Core 才能提交整体成功。无发布授权的任务仍只交付成果；已授权发布但后验失败/unknown必须保留原发布回执和真实非全成功结果。Leader 总结本身不是新的验收证明。

旧 Task 的 `completed` 与原字节/回执不改、不复活。当前唯一 verifier sink、`proposePlan` 仅批准前和失败收口规则不能靠配置绕过；新流程必须一次冻结 profile/持久解释及旧 reader 拒绝，再接入阶段验收与整体结束，不能把当前 v5/v6 名称直接重新解释。

公开接口继续以 [正式 OpenAPI](../packages/task-api/openapi.json) 为唯一机器合同。现有 Task/plan/questions/answer/approve/repair/cancel/operations/audit/events/artifact 查询是可复用的接缝，Leader 不另建 HTTP 控制 API 或用户可 PATCH 的状态。运行中题目当前只绑定非 planner 的既有 Worker，不能冒称已支持 Leader 确认/授权。所需的确认、授权、决定及动作状态可见性须随实现与 handler/客户端/示例一起明确；本稿不先造 JSON 字段，也不能把普通答案当发布授权。

本次文档无 breaking change、无迁移，已有 API-STABLE 证据保留。未来新状态/枚举/终态含义/副作用事实可能影响严格客户端与旧 reader，必须明确新 API/profile/格式兼容和不支持升级范围，验证旧响应/幂等 bytes 不变；不承诺整个未来实现天然向后兼容。查询只读，断线后重查原 Operation/动作，不隐式 approve/repair/publish。

## 6. 当前真实接缝与缺口

| 当前文件/方法 | 可复用内容 | 本纵切确实新增或需调整 |
| --- | --- | --- |
| [application.mjs](../packages/task-application/application.mjs) `create/proposePlan/freezePlan/mutate` | Task 输入、批准、短事务、原幂等/Operation | 反复 Leader 义务/决定接纳；当前 `proposePlan` 只允许未批准任务，不是运行中重规划入口 |
| [execution.mjs](../packages/task-application/execution.mjs) `nextWork/finish/reconcile` | Attempt、预算、依赖、当前 ticket、停止/结果 | Planner 成功一次性进入 awaiting-approval；Verifier 成功直接 completed，均需新 profile 内的阶段性处理 |
| [controller.mjs](../packages/task-supervisor/controller.mjs) `#cycle/#failEntry/#stop` | 原观察循环、异步执行与所属 handle | 当前循环混合调度/停止、失败广播；逐步委托 Core 硬规则和 Execution 操作，新增聚合通知，不加监督 LLM 或另一个 scheduler |
| [verification.mjs](../packages/task-application/verification.mjs) `bind/recheck/stage` | 私有可信验收端口、精确全候选/结果与 Depot | 当前唯一 verifier sink；新增精确 Review 消费、阶段验收与发布后证据，Reviewer role 现仅普通 Agent 标签，未有发布批准机制 |
| [repair.mjs](../packages/task-application/repair.mjs) 与 [runtime-questions.mjs](../packages/task-application/runtime-questions.mjs) | 原选果/closure、反馈、答案/ACK、原代拒绝 | Leader 授权内局部决定入口与集中意见来源、Leader 澄清/授权处理；不放松旧显式 repair 或原 ACK |
| [composition.mjs](../packages/task-service/composition.mjs)、[业务适配](../packages/task-business/index.mjs)、[Store](../packages/task-store/store.mjs) | 唯一组合/SQLite/Depot、可信业务 policy、原生 Provider 与 custody | 注入 Leader 输入/输出契约和有限发布/查询/后验适配；当前没有通用受控业务发布实现 |
| [HTTP handler](../packages/task-api/http-handler.mjs) 与 [客户端](../packages/task-client/index.mjs) | 有界认证/请求、原 CAS/回执绑定/下载 | 新行为可观察性与严格兼容测试，不能靠私有 SQL 或 UI 补步骤假称公开闭环 |

## 7. 统一实施顺序与正式发布前验收

按一个完整纵切实施，不按字段拆微功能或重新建设平台：先在上述接缝一次冻结 typed 行动、预算/语义快照、决定/outbox/结果和格式兼容；随后同链接受管 Leader、集中 Review/局部修正、有限授权发布/后验及终态；最后跑确定性故障链、原 HTTP/真实 Provider 完整验收和兼容回归。原先已通过的 B1/B2 资产复用，不重跑来填新能力证据；B3 的可靠性和软件发行并行保持。

正式发布前单列一个真实代表性 Leader 闭环：需求→必要确认→两个互补 Worker→独立 Review→至少一次基于真实意见/内容问题的局部修正并保留无关成果→独立整体验收→明确授权的有限目标交付→独立后验→Leader 汇总及 Core 整体结束。首轮无问题不能伪造拒收/污染成果凑 repair；需另一个可说明真实业务问题的有限案例，不无限付费重试。该机制 `DESIGN`，尚无这条完整实证；B1 既有本机 PoC 由独立核验另行通过，不代表本机制通过。

同一候选至少包含六类组合验收：

1. 正向全链：所有 Leader 唤起、相关快照、真实执行/Review/验收、局部修正保留分支、发布/后验回执可经 HTTP 审计；下载不冒充发布。
2. 少调用、容量活性与当前性：同 Task 并发事件合并、最多一个 Leader；两作者同时待答/长 Reviewer 占槽仍有界响应，容量不足付费前拒绝且不提额/假释；洪泛/heartbeat 不触发付费或重规划；相关候选/授权改变拒旧建议，无关进度不废弃它；原成本不清零。
3. 权威与越界：作者自证、Leader 改标准/预算、错任务/错候选 Review、用普通答案授权发布、超目标/高风险未确认均零越权执行；合法自治不多次打断用户。
4. 取消/期限/失败：Leader 或长验收在途时硬控制及时处理；stuck 观察不私自 kill，停止后无替身；发布后检查失败保留已发生效果，无虚假成功。
5. 崩溃/丢 ACK：Leader reservation、决定 COMMIT 前后、动作启动/回执提交及发布后验窗口重开；原决定/预算/无关成果不丢、不重规划整队；外部 unknown 只 reconcile，不能宣称无条件 exactly-once。
6. 兼容与后继：旧根/旧 completed/原幂等回执语义不变，不支持格式在 claim 前拒绝；新 profile 合法收口后下一 Task 可运行，未知义务不虚释容量。

只选一个受控业务发布目标，不要求所有生产 SQL/云写入/动态角色；发布软件自身仍走 B3 受保护资产门禁。成功流程重复后才抽成 Workflow Template，首发不建编辑器、市场、身份/角色管理或新 Skill 平台。角色职责分离不等于 OS/凭据隔离，旧 non-production 证据不改标。
