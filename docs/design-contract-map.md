# 当前设计与历史合同适用性

更新：2026-09-07。目的不是增加一层审批，而是避免“新方案写在页首，旧强制句留在正文”造成实现反复往返。

## 先区分三个问题

| 问题 | 唯一入口 | 当前含义 |
| --- | --- | --- |
| 用户要什么产品、接下来设计什么 | [服务架构](agent-team-service-architecture.md)、[Milestone](agent-team-service-milestones.md) | 一个服务完成需求确认、有限 Agent Team、可消费交付；轻量 Workspace/Task API/SQLite/开放 Adapter/内置监督是当前目标设计；仓库和表只作任务上下文，UI 后置 |
| 哪些合同可以启用到具体运行路径 | [ADR 0085](adr/0085-agent-team-service-contract-and-storage.md) §1 及所列原 ADR | 0085 仍为 Proposed；新边界需接纳并完成对应实现/验收后启用。旧 Accepted 合同继续约束旧 profile，不被草案或文档搬迁默默解除 |
| 哪些真的完成/可以发布 | [Roadmap 当前表](roadmap-status.md#业务交付当前表)与精确实机/发布证据 | 设计、候选代码、集成、正式发布分别记账；文档通过不提升产品成熟度 |

新方案设计不必先满足旧方案的函数、文件、物理存储与历史切片顺序。遇到冲突时把精确取代写入 **同一 ADR 0085**，不再为每个漏项连开 ADR；但 Proposed 不能直接成为生产 mutation、迁库或签名授权。ADR 接纳、代码合入、runtime enable、release 是四个不同动作，不互相冒充。

## 保留语义，替换实施形状

| 领域 | 当前目标中不再沿用的旧形状 | 必须延续的语义 | 合同载体 |
| --- | --- | --- | --- |
| 产品顺序 | M13 后才做所有团队/UI；重走 S1′→S2′→T1/T2 | 一条真实业务纵切、成熟度和故障证据 | 0052/0080→0085，当前 B1→B2→B3 |
| Agent 接入 | Core 写死 Pi 版本、55 个文件、所有 Agent 强制同一结果文本和禁 Skill | 受信 Adapter、兼容性检查、实际执行/输入身份冻结、真实 transcript、独立验收 | 0058/0063/0075/0084→0085 §2 |
| 服务与安装 | 只有固定 CLI 客户端自读 RB1、安装与业务仓库同目录 | 固定可信入口、应用层认证授权、唯一 Workspace owner；具体执行路径仍受归属控制、无 child CLI fallback | 0051/0062/0066/0068/0073/0076→0085 §3 |
| Workspace/Task | 把所有任务绑定 Git、扩通用资源目录、双 Task/Goal authority | 上下文提供业务信息；Task 映射既有 Goal；Git 特化单写/基线；输入与批准边界 | 0018/0066/0069/0080→0085 §2–§5 |
| 存储与启动 | RB1/Run 分账本、跨文件 proof/shared-guard、exact AST 与专属文件集合 | 单真值、先记 intent、真实 outcome、当前 owner/lease/generation/CAS、唯一合法 successor | 0065/0066/0067→0085 §4 |
| allocation 与停止 | projection 文件目录、Run lane/全局 lane 固定锁顺序 | 受管执行目录唯一写绑定；Git 路径追加 worktree/base；先 fence、终止归属、精确 terminal/cleanup/release 后才能复用 | 0069/0070/0081→0085 §4–5 |
| 问答与计量 | 所有待答全局 pause；缺 token 就永远不结算；固定两实现一集成 | 局部阻塞、全局 pause 优先、总预算不重置、未知用量诚实记账、人工验收绑定成果 | 0019/0083→0085 §5–6 |

这里只替代列明的物理/产品假设；例如 ADR 0067 的未知归属/permanent intervention、ADR 0079 的当前 Darwin process mechanics、旧 profile 的字段摘要与重放规则没有被顺手取消。严格/hardened profile 的签名、launcher、内核证据不会自动继承给 ordinary-user。

## 文档与实现怎样切换

1. **当前入口更新正文**：architecture/runtime/implementation 只描述当前目标与迁移，旧长文保留为 `*-reference-2026-09-07.md`。阅读历史用于复用证据或修复旧 profile，不把它当新增待办。
2. **ADR 不销毁历史**：原状态、接受证据、原字节语义保留；顶部附具体适用性与 0085 链接。Proposed 候选不因被引用而自动 Accepted。
3. **测试按用途处理**：独立验证、currentness、响应丢失、取消/结果竞争、重复副作用、数据/凭据边界等行为回归迁到新真实接缝；固定函数名、文件数或旧锁层次的形状测试只留在旧路径。不能只删会报错的测试而不补等价行为证据。
4. **新旧 scope 不混证据**：新空 Workspace/独立目录可按新 profile 验证，允许零 Git；显式旧来源先合法收口、只读导入、阻断旧 writer，再单写 cutover，不要求全机仓库注册/盘点。首次不做在途 Run 跨版本续行、不复制旧 activation 或重签旧 Decision。
5. **不重建整套框架**：能沿用的 reducer、验证器、process mechanics、制品与幂等规则继续用；去掉的是不必要的耦合，不是为干净目录重新实现全部核心。

新增进程内 Port/constructor DI 只需绑定当前退出条件、依赖方向和测试替换需求，**不要求独立生命周期**。新增独立服务/持久控制器才需要真实的扩缩容、故障隔离或信任边界理由。没有业务收益的抽象与无限微切片均不进入关键路径。

API-first 顺序为 B1 原生 SQLite 完整纵切→B2 团队/审计 API→API-STABLE→B3；UI-1 只在接口验收后启动，不阻 API 正式发布。旧 Marshal skill 永久不属于产品运行、研发准入或验收依赖，不读取/执行；各 Agent 原生 Skill 保留。术语统一使用“审计”，历史记录原措辞不改写。

## 本次明确不做

不运行/取消 Worker，不迁移 `.marshal`，不改运行时代码，不接纳未确认的 ADR，不删除历史 Run/失败分母，不推送或合并远端。当前调整是设计导航和精确替代草案的修复，不是新服务已经可用。
