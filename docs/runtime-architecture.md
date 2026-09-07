# Runtime 架构

> 2026-09-07 Task-first 目标投影。完整行为见[服务架构](agent-team-service-architecture.md)，边界由 [ADR 0085](adr/0085-agent-team-service-contract-and-storage.md)（Proposed）承载；本文不提前启用新权限、数据迁移或生产支持。

## 唯一组合与内部数据根

HTTP/CLI→Application Port→Core→Store/Execution/Agent/Sandbox/Verification 接口。组合根注入具体实现，Core 不导入品牌或数据库，HTTP 不启动 child CLI，也不回落 legacy server。

没有 Workspace 实体/ID/API。服务启动时选择一个内部 data-dir，任务不能通过请求切根；内部 Store ID/generation 和执行身份用于互斥、当前性与恢复，不是用户身份系统。受管目录单写、旧根不自动接管；不创建 Project/资源注册替代 Workspace。

B1 复用已有 RepositorySession、RB1/Run Store 的唯一权威组合，先在真实 Git 样例闭环。它物理上可以是多文件，但不额外加入平行 JSON Task/SQLite 写权威。B2 切最小 SQLite Store 并解除通用任务的仓库前提；Git baseline/worktree 留适配层，非 Git 使用独立目录与制品，不造假 commit。HTTP/application 不绑定存储物理形状，避免换库再写一次业务。

## 最小事务语义

| 时点 | 权威提交 | 执行/网络 |
| --- | --- | --- |
| 请求接纳 | 认证后先查精确幂等，未命中新命令才 revision CAS，保存回执 | 丢响应不重建 Task |
| 准备执行 | 输入/计划/预算、唯一 Attempt/创建义务和 command intent | 事务/受保护提交之外启动所属执行 |
| 收取结果 | 重验当前 owner/lease/generation、真实 outcome 和 Run head，唯一 successor | 独立验收不长持全局 writer lane |
| 停止 | stop intent/fence 与结果接纳竞争 | 有界停止/Inspect；未知不释放占用 |
| 结束 | 精确 terminal/cleanup/disposition 后释放、结算与 Outcome | 仅回收已确认归属且可安全清理的对象 |

B1 保留原受控提交接缝；B2 SQLite 将相关事件/投影/幂等/预算/outbox 一次事务化，不重写业务含义。真实副作用不能因数据库事务成功而伪造。未知命令先 Inspect/Reconcile，无查询/幂等证据不盲重发，不靠通用 Append 绕过 Core。

制品先耐久保存并验证摘要，再提交引用；业务记录不可抽样，高频观察可以有界批量且明示丢失。SQLite 使用本地耐久磁盘与短写事务，WAL/同步设置和恢复以故障测试证明。无数据库全量平台建设前置。

## 内置 Supervisor

同一 resident controller 自动推进接纳后计划、Start、Collect、Verify/Review 队列、集成和 Outcome；不需要人逐 Run 调 CLI，不起外部 watchdog 或监督 LLM。

B1 两作者先跑通；B2 根据目录/依赖、内存/CPU、Provider 限额及验收队列扩容。长 Verify/坏 Run 不拖住查询、取消及其他 deadline。工具事件只是观察，无事件不判定死锁，日志不延长 deadline。取消使用 owned execution ID，不接受任意 PID，stop 未确认不派替身。

重试/返工/后继计原任务预算，结构性错误无事实变化不原样重试。局部修正复用仍有效的无关成果，不能安全恢复就显式非成功，不重跑整队洗掉成本。

## 计划与交互

公开 Task 映射既有 Goal；WorkItem 是内部执行规格的节点投影，Run/Attempt 用作历史诊断，不增加可写 Task 状态机。B1 明确需求/模板一次确认；B2 questions/answers 处理关键澄清、节点阻塞和必要人工验收。

答案绑定 subject/revision/类型/期限，发送前重验 pause/cancel/fence；确认丢失无原生查询/幂等则 unknown，不能宣称 exactly-once。全局 pause/cancel 优先，等待不延长执行 deadline；终态 Run 不复活。

## 恢复分期与迁移

- B1：计划、命令、执行/结果/失败持久；正常重启能查事实且不乱重派。恢复失败停止接单、报告原因并保留原受支持只读诊断，不承诺此时在线 HTTP，不宣称透明自动恢复所有崩溃。
- B2：同版本恢复、节点级等待/取消/结果竞争、SQLite 单 owner/单写真值、无 Git/多仓库成果；新空数据根自动建立，损坏/不兼容/丢失部分状态不自动清空。
- B3：长历史、一致备份恢复、磁盘满/旧 owner/迟到结果/洪泛、长期任务与正式平台支持。恢复单位是任务事实和成果，不保证 Agent 原会话永存。
- U1：显式旧来源合法收口后持锁导入只读历史，保留原 ID/bytes/digest/预算/namespace；切原根前机械禁止旧 writer，不重签/双写/强杀/复用未知目录。未过不宣称升级支持，也不阻新任务。

备份包含一致 Store 快照和制品 manifest；恢复先只读核对旧 owner/执行/外部效果再开放写。历史 replay 不执行 outbox，DB rollback 不回滚外部 SQL/Git/云。外部写能力默认不启用；以后按单独实测 profile 处理授权/回执/unknown，不提前造通用资源锁。

## 审计与复用

自 B1 保存真实提交 prompt/context、阶段时间、尝试/失败、独立 Evidence/Decision 和精确交付；B2 提供 Task 聚合/可选事件。隐藏推理或 Agent 未暴露数据标不可见，未知用量不当零；如果保留额度预留，unknown 只能按批准规则保守 debit，不假退款或占用假活 Worker。

复用 reducer、制品/Decision、process mechanics 与现有故障案例；旧文件/AST/两账本测试在原 profile 留用，SQLite 路径改测等价行为。核心目标是同一真实业务调用链，不是重写所有历史模块。[历史 Runtime 参考](runtime-architecture-reference-2026-09-07.md)仅用于追溯。
