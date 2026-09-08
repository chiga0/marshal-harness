# 实施计划

更新：2026-09-08。当前方案见[Task-first 架构](agent-team-service-architecture.md)，详细出口只见[Milestone](agent-team-service-milestones.md)，实际完成状态只见 [Roadmap](roadmap-status.md#业务交付当前表)。合同调整集中于 [ADR 0085](adr/0085-agent-team-service-contract-and-storage.md)（Accepted），接受允许实施，不替代运行时与发布验证。

## 唯一实施顺序

| 阶段 | 优先实现 | 复用资产 | 不得成为前置 |
| --- | --- | --- | --- |
| B1 真实团队 PoC | Task HTTP/确认→两个真实作者并行→自主收集/独立验收→集成/下载消费→Outcome | fixed server、Application Port、现有 Store/RepositorySession、受管进程、B2 物化/集成候选 | Workspace/注册、安装身份平台、完整 SQLite 迁移、三 Provider、通用规划器、UI |
| B2 本地 API 可用 | 简启动、关键澄清/问答/暂停、SQLite、零 Git/多仓库、同版本恢复、审计与第二 Adapter | B1 同一业务入口/状态机、既有预算/恢复测试 | 历史导入 U1、PostgreSQL、全部 Provider 增强能力、远端权限平台 |
| API-STABLE | 核心 OpenAPI/handler/客户端一致、真实 HTTP 交付和已提供接口的关键反例 | B1/B2 业务验收 | 三品牌全部齐备、B3 全故障矩阵 |
| B3 正式可靠发布 | 长任务/故障/备份恢复、支持矩阵、签名/公证、Linux、same-bytes stable | 现有发行/故障资产与同路径真实业务证据 | UI、HA、多租户、自动发布或通用工作流 |
| UI-1 | 核心 API 稳定后提交/详情/DAG/问答/审计 | 公开 HTTP API | 不读私有 DB，不阻 API release |
| U1 旧数据升级 | 原来源合法收口、只读历史导入、旧 writer 禁写 | 原 ID/bytes/预算/失败证据 | 不串行阻断新数据和 B1/B2 |

旧 B1 单任务是当前 B1 内部检查，旧 B2 最小团队出口前移，历史状态/失败与证据不重新计分。首个团队用现成 Provider 的两个实例和真实 Git 业务样例；零 Git/多仓库支持在 B2 实测后才承诺。独立验收和下载消费是首出口的一部分，不能只报进程跑完。

## 具体下一步

1. 冻结一个真实小业务的接口、输入、整体 oracle/反例与下载内容；确认现有合法安装/Provider 可执行。结构性失败在付费调用前发现，不再轮换模型试运气。
2. 同一应用入口接 Task 请求与一次计划确认，连接已有 team materialization、Start、Collect、Verify/Decision、集成与 Outcome。优先补缺失接线，不重建 scheduler。
3. 两作者真实重叠执行；客户端只经 HTTP 查状态/取消/下载，独立环境消费结果，保留失败与实际耗时。通过才关闭 B1 团队 PoC。
4. 再沿同一接口完善 B2：自动简启动、最小 SQLite、零 Git/多仓库、问答与局部恢复；第二 Provider 独立接入，第三 Provider 不阻已支持主路径。
5. 核心 API-STABLE 后可开 UI；B3 在最终同路径资产上验证正式支持，不重做一套演示或绕过 OS 安全。

Workspace 不再是实体/API/初始化前提，不能改名为 Project 保留注册流程。data-dir 仅是启动配置；Task 上下文提供仓库/表/平台，权限由允许的执行配置约束。内部 Task/Goal 类型先映射而不是全仓重命名。用户不手写 lease、identity 或每个 Run。

## 复用与边界

B1 复用原 Store 权威组合，B2 一个 SQLite Store，迁移后同一状态根没有双写。既有 source/process 归属、输入绑定、独立验证、幂等、未知不重试及 Publisher 分权不变；新边界在 ADR 接纳和对应验证前不进入默认支持、不消费旧 activation 或改写旧 Run。一份 Proposed 不要求停止所有编码，但不能充当已获生产权限。

旧函数/文件/AST 与历史切片形状不约束新接口，等价的 currentness/唯一执行/结果取消竞态等行为仍须验证。旧兼容代码可保留在旧路径，不以“整洁”为由先全仓清理。B1 恢复先保证事实可查、不乱重派；同版本自动恢复与完整故障保证分 B2/B3 交付。

## 并行与效率

主链、业务 oracle/API 客户端、Adapter 三条可独立工作，按宿主和验收容量分配。共享 Application/Store 只有一个写 owner；每个作者独立 worktree；接口未定先写 fixture，不各自猜测。审查/验证必须独立，但客观验证不必每次额外调用 LLM。

一次业务切片聚合必要实现、预检、审查和修正，不按文件/Schema 字段分 Run。已知结构性失败无事实变化不重试；局部内容错只修受影响节点。每次报告退出条件、证据、blocker 与下一业务动作，不以 PR 数量/全部槽满衡量进展。

旧 Marshal skill 完全退出运行、研发和验收，不读取/执行；保留其历史失败与经验。M0–M13、I186-R0→R6、S1′/S2′、T1/T2 保留[历史参考](implementation-plan-reference-2026-09-07.md)，不形成第二套串行待办。只有同路径实机与最终消费证明 INTEGRATED，正式门禁证明 RELEASED。
