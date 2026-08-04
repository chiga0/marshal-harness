# Marshal

Marshal 是一个面向 Coding Agent 的证据门禁式编排框架。主 Agent 负责规划与审查，可替换的 Worker Agent 负责实现，确定性的 Harness 负责验证、留痕和发布。

> 项目状态：ADR 0001–0006 已接受；Milestone 0–3 已通过，Milestone 4 正在验收。

## 为什么需要 Marshal

不同 Coding Agent 的命令行接口、事件格式、权限模型和会话语义并不一致。直接用脚本拼接这些 Agent，往往无法可靠回答以下问题：

- Worker 收到的任务究竟是什么？
- 它修改的是哪个基线版本？
- 它声称通过的测试是否真的运行并成功？
- 要求的交付物是否真实存在？
- 谁可以 push、创建 PR/MR 或合并？
- 中断后的任务能否被安全检查和恢复？

Marshal 为不同 Worker 提供统一的任务契约、生命周期、证据模型和发布门禁。

## 运行模式

Marshal 将权限明确分成三类：

1. **主 Agent**：制定方案和验收标准，审查真实 diff 与证据，做出语义决策。
2. **Worker Agent**：在隔离的 Git worktree 中修改和测试代码。它可以声明结果，但声明不能作为验证证据。
3. **Harness**：管理状态转换、锁、独立命令执行、交付物检查、审计记录和发布凭据。

默认主 Agent 为 Codex，可从 Codex CLI 或 Codex Desktop 进入；ChatGPT 手机端 Remote 可以远程监督运行在开发机 Desktop 上的同一任务。首批 Worker Adapter 面向 Qwen Code、OpenCode 和 Pi，但核心领域模型不依赖任何单一 Agent 或交互界面。

## 核心不变量

- Worker 启动前必须锁定基线 commit。
- 每个写任务使用独立 worktree，且同一时间只有一个写入者。
- Worker 上报的测试和文件只是声明，不是证据。
- Worker 退出后，由 Marshal 独立执行并记录验收命令。
- 按策略，Worker 无法获得发布凭据。
- 成功的 Coding Task 必须产生可审查变更、验证证据、决策记录，以及按需创建的 PR/MR。
- 失败、阻塞、中止和无变更任务同样产生完整 Outcome Bundle，不创建无意义 PR。
- 实际 merge 是独立的外部副作用，默认由人工控制。

## 生命周期

```text
CREATED -> PLANNED -> READY -> RUNNING -> VERIFYING -> REVIEW_PENDING
                              |               ^                 |
                              v               |                 v
                        RETRY_PENDING         +--- REWORK_REQUESTED

REVIEW_PENDING -> PUBLISHING -> PUBLISHED -> CI_PENDING -> ACCEPTED
```

其他终态包括 `REJECTED`、`BLOCKED`、`ABORTED` 和 `NO_CHANGE`。完整转换规则见[任务生命周期](docs/task-lifecycle.md)。

## 文档导航

- [愿景与范围](docs/vision-and-scope.md)
- [架构设计](docs/architecture.md)
- [主 Agent 接入界面](docs/lead-agent-surfaces.md)
- [任务生命周期](docs/task-lifecycle.md)
- [任务契约](docs/task-contract.md)
- [Worker Adapter](docs/worker-adapters.md)
- [验证与审查](docs/verification-and-review.md)
- [交付物与发布](docs/artifact-and-publishing.md)
- [安全模型](docs/security-model.md)
- [故障与恢复](docs/failure-and-recovery.md)
- [实施计划](docs/implementation-plan.md)
- [开发指南](docs/development.md)
- [Milestone 0 验收报告](docs/milestone-0-report.md)
- [Milestone 1 验收报告](docs/milestone-1-report.md)
- [Milestone 2 验收报告](docs/milestone-2-report.md)
- [Milestone 3 验收报告](docs/milestone-3-report.md)
- [Roadmap 状态](docs/roadmap-status.md)
- [本地环境基线](docs/environment-baseline.md)
- [设计审计报告](docs/audit-report.md)
- [架构决策记录](docs/adr/README.md)

机器可读契约草案位于 [`schemas/`](schemas/)。

## MVP 计划

首个版本的 Marshal Core 是本地 CLI-first、Go 实现的模块化单体；CLI-first 不限制主 Agent 的交互界面。MVP 支持：

- 每次调用处理一个仓库；
- 每个仓库使用默认忽略的 `.marshal/` 保存本地运行态和任务 worktree；
- 多个相互隔离的任务 worktree；
- 每个任务 worktree 同时只有一个写入者；
- Qwen Code、OpenCode 和 Pi 的 one-shot Adapter；
- 标准化 JSONL 事件；
- 独立的 Git 范围与命令验证；
- 有上限的 Review/Rework 循环；
- Outcome Bundle；
- GitHub Draft PR 发布；
- 中断后的安全检查与恢复。

强恶意代码隔离、分布式调度、Web 控制台、自动 Agent 排名和无人值守 merge 不属于 MVP 承诺。

## 实施门禁

文档审计结论为 `APPROVED_FOR_IMPLEMENTATION`，维护者已接受 ADR 0001–0006 和 Local MVP 范围。Milestone 0–3 已通过；File-based Review Bridge 与 Codex Skill 已可用。Milestone 4 的 OpenCode Worker 路径正在验收，Publication 副作用仍未启用。
