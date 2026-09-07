# 实施计划

更新：2026-09-07。当前产品设计见[服务架构](agent-team-service-architecture.md)，逐项业务退出条件见[Milestone](agent-team-service-milestones.md)，实际状态只见 [Roadmap](roadmap-status.md#业务交付当前表)。[ADR 0085](adr/0085-agent-team-service-contract-and-storage.md) 仍为 Proposed；[合同适用性](design-contract-map.md)区分当前目标、旧 profile 与启用条件。

## 唯一实施顺序

| 阶段 | 交付 | 先复用、再补齐 | 不等待/不再重复 |
| --- | --- | --- | --- |
| B1-A Workspace/接口接线 | 独立固定安装、轻量 Workspace、Task HTTP、注入应用组合 | 现有 PublicApplicationPort、fixed server、验证/Decision、process mechanics；移走 CLI/固定品牌耦合 | 不另建第二 server；不重走旧 S1′/S2′；不先铺全部 Provider 增强矩阵 |
| B1-B 原生单事务完整交付 | 直接 SQLite 跑真实零 Git 制品与 Git 单任务、取消/恢复/下载重建 | 原 reducer/事件/幂等与真实故障案例；迁移跨文件 proof 为短事务＋锁外执行＋事务重验 | 不先扩旧 file-backed HTTP 再迁库；不双写、不重签旧证据 |
| B1-U 旧库升级 | 显式静止来源、原字节/namespace 导入、旧入口禁写 | 旧真实历史与恢复证据 | 不挡新空 Workspace；未过不宣传升级支持 |
| B2 受限 Agent Team | 澄清/确认/问答、有限图、并行实现→集成→独立验收→可消费交付；三 Provider、Task/DAG/审计 API | 已有 B2 候选的预算/创建/调度/集成与 Outcome 经验，按新合同接入同一应用和 Store | 不全盘重写；不扩通用 DAG/Skill/鉴权平台；不为演示无限换模型重试 |
| API-STABLE | OpenAPI/handler/客户端一致、纯 HTTP 全链与负向检查 | B1/B2 真实路径 | 是 UI 启动检查点，不是 release 标签 |
| B3 长期运行与正式支持 | 同路径故障/升级/长历史；签名/notarization、Linux server、受保护 same-bytes stable release | 历史恢复矩阵与 RC1 发行资产；补新服务真实业务支持证据 | 不用 RC1 或文档审计代替 stable；不先拆 HA/微服务；UI 不阻 API release |
| UI-1 后置界面 | API-STABLE 后只消费公开 Task API | OpenAPI/生成客户端 | 不读内部 DB、不新建私有状态 |

ADR 接纳是边界切换的条件，不是把全部编码/学习停住的理由：旧支持路径可按原合同修复；新合同可在隔离候选实现/验证但不得进入默认支持、消耗旧权限或迁移活动数据。接纳后也须通过对应纵切验收才 enable。B1-A 是同一 B1 纵切的接线检查点；B1-B 原生 SQLite 完整交付，不要求 B1-A 先在旧存储跑实机。B1-U 只控制升级支持。精确出口以 Milestone 为准，不再造第四产品阶段。

Workspace 不做仓库或数据资源治理。Task prompt/context 提供仓库/表/平台说明，Core 保留批准输入/执行 profile/预算/归属/证据；Git 专用处理留在适配层。公开 Task=既有 Goal，节点旧 Task 对外称 WorkItem；无 Git 制品不强制 worktree/commit。首版不新增 ResourceBinding、资源注册 API、跨 Workspace claim 或通用连接器平台。

## 资产复用与测试转换

- 保留已有业务状态机、独立 Verification/Review、当前结果接纳、制品、path/secret 边界和归属终止。
- 将固定 Pi 常量/原生结果文本规则移入相应 Adapter/profile；Core 只消费中立能力和经真实观察绑定的执行事实。
- 旧物理账本、函数名/文件集合、exact AST 与一次性的切片顺序不约束新实现形状；等价的当前 owner、输入冻结、预算单次、结果/取消竞争和 crash/replay 行为必须在新接缝验证。
- 旧二进制/旧 Run 在迁移前仍遵守原合同，历史 bytes/digest 不变；未知归属继续 intervention，不借设计更新自动清理。
- 旧候选必须按新入口/支持 profile 重测才能贡献新退出条件；仅功能相似、测试绿或已有 PR 不算完成。

## 并行与效率约束

优先并行三条有边界的工作：应用/Store 主线、AgentAdapter 与 contract tests、API 客户端/业务 oracle/审计投影；API-STABLE 前不派 UI 作者。共享事务/Schema 接缝只指定一个 owner，其他以冻结接口协作。review 与长验收执行不占全局 writer lane；有 scope/资源余量不等于必须增加 Worker。

每个交付切片绑定一个 B1/B2/B3 用户可见出口，同时聚合必要 producer→consumer→恢复→验收，不按文件或每个字段拆 Run。旧 Marshal skill 完全退出运行/研发/验收依赖，不读取/加载/运行，不让 reviewer 首次发现机器可检的协议/输入问题。

派发前一次聚合输入、原生配置/能力、结果传输、业务 oracle、恢复/超时、最小 schema/path/secret 检查。代码问题一次聚合返工；结构性失败没有事实变化和预检不重复执行。判断收益使用同合同/资源的强 Lead＋SubAgents 配对实验，保留失败 Attempt、CI、人工等待与返工成本；不能以 PR 数量证明效率。

## 历史阶段如何保留

M0–M13、I186-R0→R6、S1′/S2′、T1/T2 是历史交付与证据坐标，不是当前第二套串行排期。历史 I186 的未关闭安全/发布条件映射到 B1 恢复与 B3 正式支持，既不丢弃，也不重复建设已关闭组件。完整旧表见[实施计划历史参考](implementation-plan-reference-2026-09-07.md)。

只有真实组合路径与消费方验收才能标记 `INTEGRATED`；正式资产/支持门禁通过才能标记 `RELEASED`。每次关闭 Milestone 同步 Roadmap、适用合同和遗留风险；不把 Proposed、候选代码、旧实机证据互相替代。
