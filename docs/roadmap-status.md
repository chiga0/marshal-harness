# Roadmap 状态

更新时间：2026-08-05

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

每个 Milestone 都执行范围冻结、实现、单元/集成/E2E 测试、独立审计、提交推送和远端 CI 绿色验收。任何 P0/P1 审计问题或 CI 失败都会阻止进入下一阶段。
