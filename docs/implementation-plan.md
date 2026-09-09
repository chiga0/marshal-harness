# 实施计划

2026-09-09 [ADR0095](adr/0095-node-managed-leader-contract.md)机器合同已独立审查接纳，B2-L 按[唯一执行合同](node-leader-execution-contract.md)进入 Core、API/客户端、有限本机报告端口三个互斥范围的同链实现。当前尚未接线，不能按文档通过升级成熟度；已确定后继与授权动作直接执行，不额外调用 Leader 重复批准。

更新：2026-09-09。当前方案见[Task-first 架构](agent-team-service-architecture.md)，详细出口只见[Milestone](agent-team-service-milestones.md)，实际完成状态只见 [Roadmap](roadmap-status.md#业务交付当前表)。合同由已接受的 ADR0085/0088/0094/0095 按 profile 承载，接受允许实施，不替代运行时与发布验证。

2026-09-09 按已接受 [ADR0094](adr/0094-trusted-single-user-role-team.md)实施可信单用户角色团队。B1本机PoC经独立核验通过，保留B2已有资产；新增[全程受管Leader](node-leader-execution-design.md)为B2-L/DESIGN，而非Planner别名。Supervisor观察、Leader业务判断、Core校验/已批准调度/硬规则、Execution原handle操作；在现loop中委托，不另造平台。强OS隔离后置，B3可靠性/受保护软件发行保留。当前HTTP/角色/格式无改动或迁移，API-STABLE保留；新机制实际机器语义一次明确兼容再启用。

## 唯一实施顺序

| 阶段 | 优先实现 | 复用资产 | 不得成为前置 |
| --- | --- | --- | --- |
| B1 真实团队 PoC | Task HTTP/确认→两个真实作者并行→自主收集/独立验收→集成/下载消费→Outcome | fixed server、Application Port、现有 Store/RepositorySession、受管进程、B2 物化/集成候选 | Workspace/注册、安装身份平台、完整 SQLite 迁移、三 Provider、通用规划器、UI |
| B2 本地 API 可用 | 保留简启动/交互/SQLite/制品/恢复/审计与第二 Adapter；B2-L完整Leader/集中Review/局部修正/授权交付与后验 | B1 同一应用/状态机、既有执行/独立验收/选果/预算/回执接缝 | 历史导入 U1、全部 Provider 增强、强 OS 隔离证明、动态角色/Workflow 平台及所有高风险发布流程 |
| API-STABLE | 核心 OpenAPI/handler/客户端一致、真实 HTTP 交付和已提供接口的关键反例 | B1/B2 业务验收 | 三品牌全部齐备、B3 全故障矩阵 |
| B3 正式可靠发布 | 长任务/故障/备份恢复、支持矩阵、签名/公证、Linux、same-bytes stable | 现有发行/故障资产与同路径真实业务证据 | UI、HA、多租户、自动发布或通用工作流 |
| UI-1 | 核心 API 稳定后提交/详情/DAG/问答/审计 | 公开 HTTP API | 不读私有 DB，不阻 API release |
| U1 旧数据升级 | 原来源合法收口、只读历史导入、旧 writer 禁写 | 原 ID/bytes/预算/失败证据 | 不串行阻断新数据和 B1/B2 |

旧 B1 单任务是当前 B1 内部检查，旧 B2 最小团队出口前移，历史状态/失败与证据不重新计分。首个团队用现成 Provider 的两个实例和真实 Git 业务样例；零 Git/多仓库支持在 B2 实测后才承诺。独立验收和下载消费是首出口的一部分，不能只报进程跑完。

## 具体下一步

**Node正式主线继续，已通过的B1与原API-STABLE不重做。** 复用现有Provider、Application/SQLite、业务与独立验证、HTTP/客户端；同一状态根永远只启用一个已声明格式的权威Store，不恢复Go原生执行或另造演示链。

1. 一次对齐B2-L最小行动、原始需求/交付约定/实现计划、需求到验收覆盖、原预算、决定/动作事实和兼容边界；可信验证机制与任务验收内容分开，结构性前提在付费前检查。
2. 在原HTTP/Application/SQLite链完成Leader需求理解与必要确认，沿既有调度执行；共享约定进入真实工作包，完成该检查点的对应故障/兼容及有界实机。
3. 接入结果驱动的集中Review与有界自主局部调整，保留无关有效成果；同时验证同一业务配置处理两个不同需求，不为每个任务改Core或人工补验收。
4. 接一个明确授权、目标有限且效果可查询的交付适配及独立后验，Leader逐项汇总/Core整体结束；下载默认保留。每步持续集成及验证，不等全部写完再首次运行。
5. 对最终同一候选核验完整Leader接口支持面和旧响应/回执兼容，再完成B3同资产平台/长期故障/备份恢复/正式发行；B3准备可以并行，最终证明不能来自另一套实现。

以上是一个B2-L纵切内的可运行检查点，不按字段/每个检查点强制PR或ADR。原`proposePlan`批准前限定、`finish`立即completed/失败全队取消、唯一verifier sink和显式用户repair仍须明确调整接缝，不能靠配置冒充。正常任务可一次通过；真实局部修正使用有限、如实标注的业务问题/缺陷修复场景证明，确定性故障另计，不无限付费等模型犯错。未实现能力不启用，状态仍按Roadmap，不新增第二控制器/Store。

Workspace 不再是实体/API/初始化前提，不能改名为 Project 保留注册流程。data-dir 仅是启动配置；Task 上下文提供仓库/表/平台，权限由允许的执行配置约束。内部 Task/Goal 类型先映射而不是全仓重命名。用户不手写 lease、identity 或每个 Run。

## 复用与边界

B1 复用原 Store 权威组合，B2 一个 SQLite Store，迁移后同一状态根没有双写。既有 source/process 归属、输入绑定、独立验证、幂等、未知不重试及 Publisher 分权不变；新边界在 ADR 接纳和对应验证前不进入默认支持、不消费旧 activation 或改写旧 Run。一份 Proposed 不要求停止所有编码，但不能充当已获生产权限。

旧函数/文件/AST 与历史切片形状不约束新接口，等价的 currentness/唯一执行/结果取消竞态等行为仍须验证。旧兼容代码可保留在旧路径，不以“整洁”为由先全仓清理。B1 恢复先保证事实可查、不乱重派；同版本自动恢复与完整故障保证分 B2/B3 交付。

## 并行与效率

主链、业务 oracle/API 客户端、Adapter 三条可独立工作，按宿主和验收容量分配。共享 Application/Store 只有一个写 owner；每个作者独立 worktree；接口未定先写 fixture，不各自猜测。审查/验证必须独立，但客观验证不必每次额外调用 LLM。

一次业务切片聚合必要实现、预检、审查和修正，不按文件/Schema 字段分 Run。已知结构性失败无事实变化不重试；局部内容错只修受影响节点。每次报告退出条件、证据、blocker 与下一业务动作，不以 PR 数量/全部槽满衡量进展。

旧 Marshal skill 完全退出运行、研发和验收，不读取/执行；保留其历史失败与经验。M0–M13、I186-R0→R6、S1′/S2′、T1/T2 保留[历史参考](implementation-plan-reference-2026-09-07.md)，不形成第二套串行待办。只有同路径实机与最终消费证明 INTEGRATED，正式门禁证明 RELEASED。
