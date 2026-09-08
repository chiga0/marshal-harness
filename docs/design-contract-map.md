# 当前设计与历史合同适用性

更新：2026-09-08。目标是少流程、少返工，不再让旧产品形状变成新团队交付的前置。

## 三个不同结论

| 问题 | 唯一入口 | 含义 |
| --- | --- | --- |
| 要实现什么 | [Task-first 架构](agent-team-service-architecture.md)、[Milestone](agent-team-service-milestones.md) | B1 先团队 PoC，B2 本地 API 可用，B3 正式支持；无 Workspace 产品实体 |
| 哪个合同可启用 | [ADR 0085](adr/0085-agent-team-service-contract-and-storage.md) §1 | 2026-09-08 已 Accepted，允许按明确范围实施；新 profile 仍须对应验证，旧 profile 仍守原规则 |
| 什么真的完成 | [Roadmap](roadmap-status.md#业务交付当前表) | 历史证据/候选/实机/发布分别计，不因文档更新提升成熟度 |

用户已明确要求按 Task-first 简化修改设计；它不等于旧 Run/activation 被重新授权。设计审计、ADR 接纳、代码合入、runtime enable 和正式 release 不互相代替。相关取代集中在同一 0085，不每个字段再写 ADR。

## 删除前置，保留必要语义

| 删除或后置的形状 | 仍要保留 | 落点 |
| --- | --- | --- |
| Workspace ID/API/注册/切换，或改名 Project | 内部数据根排他、任务/执行 ID、输入/成果归属 | 0085 §2–§3 |
| operator-local 安装收据、显式 init、账号/组织/RBAC | 合法固定安装、OS 安全、本地自动 token、空根安全建立、旧/坏数据不覆盖 | 0085 §1/§3；正式管理能力后置 |
| 全面 SQLite/旧库迁移后才团队 | B1 原唯一 Store，B2 SQLite；任何时刻每根一个权威 backend | 0085 §4；U1 独立升级 |
| 固定品牌/版本/文本 envelope/强制 ACP | 受信注入 Adapter、实际执行/输入和终态、能力符合、独立验收 | 0058/0063/0075/0084→0085 §2 |
| 旧 AF_UNIX 客户端持 RB1 证明，child CLI/多 server | HTTP/CLI 共用应用层，服务端当前事实重验 | 0062/0066/0076→0085 §3 |
| 双账本物理 proof/锁序/AST 是永久形状 | 先 intent、唯一 producer/current owner/CAS、迟到拒绝、无双写 | B1 保留旧实现，B2 依 0065/0066/0067→0085 §4 换接缝 |
| 所有任务必须 Git 或 Core 注册资源 | Task context、受管目录单写；Git 特化 base/worktree，零 Git 独立目录 | 0066/0069/0080→0085 §2/§5 |
| 完整规划/所有 Provider/全恢复先行 | 一次有限确认、两个真实作者、独立整体验收、最小事实/止损 | B1；增强体验 B2；正式矩阵 B3 |

原 ADR 0052 正式 signing/notarization/Linux/stable 门禁不删；原当前性、未知归属/permanent intervention、发布分权和历史 byte/replay 不变。普通 local profile 不继承 hardened/managed 保证。B1 重启只保证能查事实并不乱重派时，就不能宣称透明恢复。

## 文档和实现如何收敛

- 当前架构/runtime/implementation、README、愿景、Roadmap 目标列同步新顺序；历史参考文档与原事实不重写。
- B1 先补实际业务接线：当前 Store 不形成新的并行 Task 真值，HTTP 不绑定磁盘布局。B2 SQLite 替换复用相同应用与验收，不重新造一套 API。
- 回归保留独立验收、路径/secret、重复/迟到、结果取消竞争和恢复行为；旧函数名/文件数测试仅留旧 profile，不以删除行为测试换绿。
- 新空数据根与旧迁移分开；旧非终态先合法收口、只读历史导入，旧 writer 未证明禁止不接管。不得清空/重签/复用未知占用目录。
- 接口依赖反转不要求一个 Port 一个生命周期/微服务；已有 state reducer、process mechanics、制品与业务 oracle 优先复用。

B1 团队 PoC→B2 本地 API/核心 API-STABLE→B3 正式支持。UI 在核心接口稳定后开发，三品牌全部增强/U1/完整 B3 故障不作为 UI 或首演示前置。旧 Marshal skill 不读取、不执行、不作为运行/研发/验收条件；Agent 自带 Skill 保留，术语统一“审计”。

## 本轮范围

只调整方案、Milestone、合同草案、当前入口与审计。没有启动/取消 Worker、修改产品运行时、迁移 .marshal、接纳旧权限或发布正式资产；历史失败不删除。本轮文档保存与后续 Git 同步不等于产品已交付。

### 治理文件同步边界

上一轮同步根 AGENTS.md 被保护规则拦截后保留原文件。2026-09-07 用户明确批准“仅同步目标导航、保留全部安全不变量”，现已同步顶部路线、目标说明和必读导航；universal 不变量与实施/维护者/外部贡献者条款原文未改。不以文档冲突豁免单写、独立验证、凭据分权或旧运行时门禁。
