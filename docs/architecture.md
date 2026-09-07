# 整体架构

> 当前目标设计，2026-09-07。产品要求与实现状态分开：完整合同提案见 [ADR 0085](adr/0085-agent-team-service-contract-and-storage.md)（Proposed），实际进度见 [Roadmap](roadmap-status.md#业务交付当前表)。本文不提前启用新边界。旧方案与本方案的关系见[合同适用性](design-contract-map.md)。

## 产品目标

Marshal 是单节点、单用户、可信业务场景的 Agent Team 服务。用户提出需求，系统补充问题、给出可验收方案、取得确认，然后进行有界执行、独立验证、成果集成与交付；用户可查看任务图、Worker 实际可见进展、回答问题、取消和审计。

运行形态是固定安装的 `marshal control-plane serve`。HTTP 与 CLI 共用同一应用接口（Application Port）；UI 只在 API-STABLE 后开发，且不阻塞 API 正式发布；不要求用户逐个运行生命周期命令，不用第二个 Agent 或外部 watchdog 代替内置监督。Server 仍是受操作系统安全机制管理的可执行程序，不声称变为服务就无需可信安装。

完整接口、对象、Provider 与验收契约集中在[服务架构](agent-team-service-architecture.md)，此处只定义系统边界，不另复制一套字段级规范。

## 三面分离

| 面 | 职责 | 不拥有的权限 |
| --- | --- | --- |
| 控制面 | 澄清/计划接纳、预算、调度、Supervisor、当前结果接纳、Decision、effect reconcile | 不把 LLM 提案或日志当权威事实 |
| 执行面 | 注入 AgentAdapter、owned process/deadline/cancel、Sandbox allocation、独立 Verify/Review、受控 Publisher | Worker 不写权威 Store，不提供自身权威验证，不拿 Publisher 凭据 |
| 存储面 | 单一事务 Store、不可变业务事件、可重建投影、幂等/预算/outbox、制品与审计材料 | 数据库不决定业务成功；遥测不授予发布或接纳权限 |

## 逻辑职责不等于物理服务

### v1.0 物理投影

一个固定 server 进程打开一个轻量 Workspace，若干受归属管理的执行进程，一份 Workspace SQLite WAL 权威库，加本地内容寻址制品。Workspace 是任务/配置/状态容器，可零 Git；仓库、表和平台说明通过 Task 上下文进入，不建设 Core 资源目录、注册 API 或通用资源生命周期。Core 通过存储 Port 在短事务内提交；执行和网络在事务外进行。没有独立 scheduler、消息中间件、数据库服务、GC 服务或第二控制权威。

公开 Task 复用既有 Goal 的 ID、revision、事件和预算；内部旧 Task 对外称节点 WorkItem，不建立第二任务权威。任务 API 以 `/tasks` 为主，包含计划/批准、图、工作项、问答、暂停/恢复/取消、制品与审计，完整清单见服务架构 §5。

业务链是：认证请求→需求/方案确认→有限节点图→预算与 scope 准入→真实 Agent 执行→当前结果重验→独立 Verify/Review→精确集成候选→从下载成果独立重建/原业务 oracle→必要人工验收→GoalOutcome。可选 Draft PR 是独立 effect，不默认 merge。

Port 是依赖边界接口，Go 中通常是 interface；Adapter 实现它，DI 把实现注入使用方，不是 HTTP 监听端口。Application/Store/Agent/Sandbox 接口为当前依赖倒置和可测试性存在，不需为每个接口造独立生命周期。拆成独立服务只在测得故障隔离、扩缩容或信任边界需求后考虑。

## 开放 Agent 与确定性内核

首批目标 Provider 是 Pi、Qwen Code、OpenCode。核心要求是能干活、关联真实执行身份、取得终态/成果、有界取消及显式失败；实时工具流、原生交互、steering、resume、tokens 等分别为增强，不把 ACP 或完整事件流设成统一准入门槛。

Core 只认识能力和中立类型，品牌/CLI 语法/原生配置解析属于注入 Adapter。版本是兼容性与诊断维度，不是 Core 全局硬编码的名单；每次实际启动仍冻结可证明的执行、配置和输入身份。原生 Skill/模型鉴权由 Agent 管理，但不能扩大 Worker/Publisher 权限。

模型可以提议计划和评审意见。确定性 Core 负责验收计划、限额、状态转换和接纳证据；不把业务理解伪装成纯规则，也不把整个控制面交给模型。任务能用一个 Worker 时不强制拆分，多 Agent 只在 scope/依赖独立且收益可测时使用。

## 长任务的可靠性边界

- 恢复的是事实、批准范围、执行义务与制品，不保证原 Agent 会话无限存活。
- 每 Workspace 单 owner/单写真值；输入冻结、受管执行目录单写者；Git 任务在适配层追加锁定 base/独立 worktree；结果接纳在同 Store 中重验 current owner/lease/generation/CAS。
- 外部操作先有 intent/outbox；未知结果先 Inspect/Reconcile，不承诺跨进程或外部系统 exactly-once。
- 节点待答只阻塞依赖；Goal 全局 pause/cancel 优先。取消先 fence，再确认归属/结束与 release；不能 kill 猜测 PID。
- Worker 不自证，ReviewDecision 绑定精确证据；最后交付必须在新目录按原业务 oracle 消费成功。
- Token/上下文不可见时记录 unknown/source/coverage，不记零；rework/successor 共用原 Goal 总预算。

运行时事务、迁移与恢复映射见 [Runtime 架构](runtime-architecture.md)。

## 演进边界

B1 原生 SQLite/HTTP 完整单任务（含零 Git 制品）→B2 受限团队/多仓库与审计 API→API-STABLE→B3 正式 API 支持。UI-1 仅在 API-STABLE 后启动，不作为 API release 前置；旧库升级单列 B1-U，不拖住新空 Workspace。HA、多租户、通用 Workflow DSL、动态无限图、统一 Skill/Agent 登录平台、全 Provider hardened 矩阵不作为首版前置。PostgreSQL 后续实现同 Store conformance，不先引入双写。

原生工具可按已批准执行 profile 使用，但文本不授予权限，Agent 退出不证明外部 SQL 已完成或停止；未知外部效果不自动重试。首版不承诺全局资源锁或通用数据平台。旧 Marshal skill 不加载、不执行、不作为研发准入；原生 Agent Skill 保留。

现有 file-backed/Pi/fixed AF_UNIX 是可复用实现和历史证据，不能冒充新 HTTP/SQLite 产品，也不能永久否定它。保留的[旧整体架构](architecture-reference-2026-09-07.md)只用于追溯；实施时先按[当前计划](implementation-plan.md)映射现有资产，不再从历史长图扩张待办。
