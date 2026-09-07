# 整体架构

> 当前目标设计，2026-09-07。产品要求与实现状态分开：完整合同提案见 [ADR 0085](adr/0085-agent-team-service-contract-and-storage.md)（Proposed），实际进度见 [Roadmap](roadmap-status.md#业务交付当前表)。本文不提前启用新边界。旧方案与本方案的关系见[合同适用性](design-contract-map.md)。

## 产品目标

Marshal 是单节点、单用户、可信仓库的 Agent Team 服务。用户提出需求，系统补充问题、给出可验收方案、取得确认，然后进行有界执行、独立验证、成果集成与交付；用户可查看任务图、Worker 实际可见进展、回答问题、取消和复盘。

运行形态是固定安装的 `marshal control-plane serve`。HTTP、最小任务页和 CLI 共用同一 Application Port；不要求用户逐个运行生命周期命令，不用第二个 Agent 或外部 watchdog 代替内置监督。Server 仍是受操作系统安全机制管理的可执行程序，不声称变为服务就无需可信安装。

完整接口、对象、Provider 与验收契约集中在[服务架构](agent-team-service-architecture.md)，此处只定义系统边界，不另复制一套字段级规范。

## 三面分离

| 面 | 职责 | 不拥有的权限 |
| --- | --- | --- |
| 控制面 | 澄清/计划接纳、预算、调度、Supervisor、当前结果接纳、Decision、effect reconcile | 不把 LLM 提案或日志当权威事实 |
| 执行面 | 注入 AgentAdapter、owned process/deadline/cancel、Sandbox allocation、独立 Verify/Review、受控 Publisher | Worker 不写权威 Store，不提供自身权威验证，不拿 Publisher 凭据 |
| 存储面 | 单一事务 Store、不可变业务事件、可重建投影、幂等/预算/outbox、制品与审计材料 | 数据库不决定业务成功；遥测不授予发布或接纳权限 |

## 逻辑职责不等于物理服务

### v1.0 物理投影

一个固定 server 进程，若干受归属管理的执行进程，一个本地 SQLite WAL 权威库，加本地内容寻址制品。Core 通过存储 Port 在短事务内提交；执行和网络在事务外进行。没有独立 scheduler、消息中间件、数据库服务、GC 服务或第二控制权威。

业务链是：认证请求→需求/方案确认→有限节点图→预算与 scope 准入→真实 Agent 执行→当前结果重验→独立 Verify/Review→精确集成候选→从下载成果独立重建/原业务 oracle→必要人工验收→GoalOutcome。可选 Draft PR 是独立 effect，不默认 merge。

进程内 DI、Application/Store/Agent/Sandbox Port 为当前依赖倒置和可测试性存在，不需为每个接口造独立生命周期。拆成独立服务只在测得故障隔离、扩缩容或信任边界需求后考虑。

## 开放 Agent 与确定性内核

首批目标 Provider 是 Pi、Qwen Code、OpenCode。核心要求是能干活、关联真实执行身份、取得终态/成果、有界取消及显式失败；实时工具流、原生交互、steering、resume、tokens 等分别为增强，不把 ACP 或完整事件流设成统一准入门槛。

Core 只认识能力和中立类型，品牌/CLI 语法/原生配置解析属于注入 Adapter。版本是兼容性与诊断维度，不是 Core 全局硬编码的名单；每次实际启动仍冻结可证明的执行、配置和输入身份。原生 Skill/模型鉴权由 Agent 管理，但不能扩大 Worker/Publisher 权限。

模型可以提议计划和评审意见。确定性 Core 负责验收计划、限额、状态转换和接纳证据；不把业务理解伪装成纯规则，也不把整个控制面交给模型。任务能用一个 Worker 时不强制拆分，多 Agent 只在 scope/依赖独立且收益可测时使用。

## 长任务的可靠性边界

- 恢复的是事实、批准范围、执行义务与制品，不保证原 Agent 会话无限存活。
- 单 owner/单写真值；输入冻结、独立 worktree 单写者；结果接纳在同 Store 中重验 current owner/lease/generation/CAS。
- 外部操作先有 intent/outbox；未知结果先 Inspect/Reconcile，不承诺跨进程或外部系统 exactly-once。
- 节点待答只阻塞依赖；Goal 全局 pause/cancel 优先。取消先 fence，再确认归属/结束与 release；不能 kill 猜测 PID。
- Worker 不自证，ReviewDecision 绑定精确证据；最后交付必须在新目录按原业务 oracle 消费成功。
- Token/上下文不可见时记录 unknown/source/coverage，不记零；rework/successor 共用原 Goal 总预算。

运行时事务、迁移与恢复映射见 [Runtime 架构](runtime-architecture.md)。

## 演进边界

B1 完整单任务服务→B2 受限团队与可观察交付→B3 长期运行与正式支持。HA、多租户、通用 Workflow DSL、动态无限图、统一 Skill/Agent 登录平台、全 Provider hardened 矩阵不作为首版前置。PostgreSQL 后续实现同 Store conformance，不先引入双写。

现有 file-backed/Pi/fixed AF_UNIX 是可复用实现和历史证据，不能冒充新 HTTP/SQLite 产品，也不能永久否定它。保留的[旧整体架构](architecture-reference-2026-09-07.md)只用于追溯；实施时先按[当前计划](implementation-plan.md)映射现有资产，不再从历史长图扩张待办。
