# Roadmap 状态

更新时间：2026-08-10

| Milestone | 状态 | 证据 |
| --- | --- | --- |
| 0：Toolchain 与 Contract | `PASSED` | [验收报告](milestone-0-report.md) |
| 1：State Machine 与 Run Store | `PASSED` | [验收报告](milestone-1-report.md) |
| 2：Git Worktree 与独立 Verification | `PASSED` | [验收报告](milestone-2-report.md) |
| 3：Review 与 Rework Loop | `PASSED` | [验收报告](milestone-3-report.md) |
| 4：首个真实 Worker Adapter | `PASSED` | [验收报告](milestone-4-report.md)；GitHub Actions `30879438415` |
| 5：GitHub Draft Publisher | `PASSED` | [验收报告](milestone-5-report.md)；主分支 CI `30889069165`；[真实 Draft PR #1](https://github.com/chiga0/marshal-harness/pull/1) 与 PR CI `30889190854` |
| 6：其余 Adapter 与 Recovery 加固 | `PASSED` | [验收报告](milestone-6-report.md)；真实受监督 cmux Pilot 通过；Full MVP E2E Run `m6-mvp-e2e-r3-20260805` `ACCEPTED`，[Draft PR #2](https://github.com/chiga0/marshal-harness/pull/2) 与 PR CI `30974239712` 全绿 |

Local MVP 定义达成：标记 `USABLE`。

M7–M13（M7–M12：耐久 Runtime 与可插拔 Sandbox Provider，[ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md) 冻结；M13：Goal orchestration，承接 ADR 0016 冻结的 Project/Goal 对象语义）：

| Milestone | 状态 | 证据 |
| --- | --- | --- |
| 7：架构与契约 | `IN_PROGRESS` | [ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md) 已接受；[Runtime 架构](runtime-architecture.md) 与本文档批次随 Draft PR 交付 |
| 8：Sandbox SPI/Fake/Local conformance + 常驻单节点纵切 | `PLANNED` | 见[实施计划](implementation-plan.md) |
| 9：Durable Runtime | `PLANNED` | 见[实施计划](implementation-plan.md) |
| 10：Cloudflare Provider | `PLANNED` | 见[实施计划](implementation-plan.md) |
| 11：生产级存储、多节点 HA 与身份分离 | `PLANNED` | 见[实施计划](implementation-plan.md) |
| 12：开源部署、插件 SDK 与长稳验证 | `PLANNED` | 见[实施计划](implementation-plan.md) |
| 13：Goal orchestration（持久 Project/Goal、计划/重规划、预算与终止、独立评估、人工干预） | `PLANNED` | 见[实施计划](implementation-plan.md) |

每个 Milestone 都执行范围冻结、实现、单元/集成/E2E 测试、独立审计、提交推送和远端 CI 绿色验收。任何 P0/P1 审计问题或 CI 失败都会阻止进入下一阶段。M7–M13 还要求每个 Milestone 先通过 Local MVP 全量回归。M7 只冻结 Project/Goal 对象语义，M13 才实现 Goal 控制器；M7–M12 完成声明不涵盖复杂需求目标。
