# 参考索引

本页是低频技术文档的统一入口。为保持主导航简洁，这些页面默认不单独展示，但仍可访问、搜索和引用。

## 文档权威顺序

出现冲突时，按以下顺序判断：

1. 已接受的 [ADR](adr/README.md)；
2. [Runtime 架构](runtime-architecture.md)、[任务生命周期](task-lifecycle.md)、[安全模型](security-model.md)与机器可读 Schema；
3. Task、Verification、Publication 等专项契约；
4. [实施计划](implementation-plan.md)与[Roadmap](roadmap-status.md)；
5. 操作指南和开发指南；
6. 审计、研究、Milestone 报告与历史 Scope。

历史文档只能解释“为什么”，不能覆盖当前规范。

## 核心规范

- [愿景与范围](vision-and-scope.md)：产品目标、非目标与成功标准。
- [总体架构](architecture.md)：Marshal 终态产品架构及当前交付映射。
- [Runtime 架构](runtime-architecture.md)：确定性 Control Plane 的长期规范设计；v1.0 按 `I186-R0→R6` 生产纵切交付，原 M10–M13 属 1.x 候选。
- [任务生命周期](task-lifecycle.md)：Run 状态、转换和预算。
- [安全模型](security-model.md)：信任边界、威胁与验收要求。

## 契约与执行

- [任务契约](task-contract.md)
- [验证与审查](verification-and-review.md)
- [交付物与发布](artifact-and-publishing.md)
- [故障与恢复](failure-and-recovery.md)
- [Worker Adapter](worker-adapters.md)
- [主 Agent 接入界面](lead-agent-surfaces.md)
- [本地环境基线](environment-baseline.md)
- [兼容性矩阵](compatibility-matrix.md)
- [Milestone 6 Agent Adapter 协议](milestone-6-adapter-protocol.md)
- [Worker Adapter 选型 Spike](worker-adapter-spike.md)（历史能力证据）

## 建设与治理

- [Roadmap 状态](roadmap-status.md)：真实完成状态和证据。
- [实施计划](implementation-plan.md)：Milestone 顺序与退出门禁。
- [设计审计报告](audit-report.md)：已打开和关闭的架构 Finding。
- [ADR 索引](adr/README.md)：不可被普通文档静默覆盖的架构决策。
- [ADR 0052](adr/0052-v1-release-scope-and-production-reachability.md)：v1.0 支持范围、成熟度与生产可达性门禁。

## 历史资料

Milestone Scope、验收报告、独立 Review、研究和过期方案不进入主阅读路径，统一从[历史档案](archive.md)访问。

## 文档维护规则

- 每个主题只保留一个规范入口，其他页面链接过去而不复制整段定义。
- 首页先定义终态产品，再独立标注当前交付状态；快速开始只描述已实现能力，未交付能力必须显式标记 `PLANNED`。
- ADR、审计证据和验收报告不删除，只退出主导航。
- 完全重复、空白或没有审计价值的页面才允许删除。
- 新增页面默认隐藏；只有它服务于高频阅读路径时才加入 `mkdocs.yml`。
- 面向人的规范文档使用中文；协议字段、状态名、命令和代码标识保留英文。
