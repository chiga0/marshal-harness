# Roadmap 状态

更新时间：2026-08-04

| Milestone | 状态 | 证据 |
| --- | --- | --- |
| 0：Toolchain 与 Contract | `PASSED` | [验收报告](milestone-0-report.md) |
| 1：State Machine 与 Run Store | `PASSED` | [验收报告](milestone-1-report.md) |
| 2：Git Worktree 与独立 Verification | `PASSED` | [验收报告](milestone-2-report.md) |
| 3：Review 与 Rework Loop | `PASSED` | [验收报告](milestone-3-report.md) |
| 4：首个真实 Worker Adapter | `IN_PROGRESS` | OpenCode 已选定；Adapter、Attempt 控制根与 `task run` 正在验收 |
| 5：GitHub Draft Publisher | `PENDING` | 尚未开始 |
| 6：其余 Adapter 与 Recovery 加固 | `PENDING` | 尚未开始 |

每个 Milestone 都执行范围冻结、实现、单元/集成/E2E 测试、独立审计、提交推送和远端 CI 绿色验收。任何 P0/P1 审计问题或 CI 失败都会阻止进入下一阶段。
