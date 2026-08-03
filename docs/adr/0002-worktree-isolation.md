# ADR 0002：每个任务一个独立 Worktree

- 状态：已接受（Accepted）
- 日期：2026-08-03
- 接受日期：2026-08-03

## 背景

Coding Agent 会修改多个文件并可能中途退出。不同任务不能共享未提交状态，维护者的主 Checkout 也必须保持可用。

## 决策

锁定 Base Commit，并为每个写任务创建独立 Linked Worktree 与 Task Branch。每个 Worktree 使用独占 Writer Lease；不同任务可在不同 Worktree 中并发。

每个受管仓库的 Operational State 位于该仓库默认忽略的 `.marshal/` 中，任务工作目录位于 `.marshal/worktrees/<task-id>/`。它们是由 Git 管理的独立 linked worktree，而不是主 Checkout 中的受跟踪内容。失败后的 Dirty Worktree 必须保留，直到 Diff/Log 已归档并收到显式 Cleanup。

## 影响

- Diff 与 Cleanup 清楚归属于单一任务。
- 独立写任务可并发而不冲突。
- Worktree Metadata Operation 需要短时 Repository Lock。
- Disk Consumption 与 Stale Worktree 成为运维问题。
- Nested Repository 与 Submodule 需要专门测试。
- 嵌套于忽略目录的 linked worktree 行为必须在 macOS 与 Linux 上通过 Fixture；不兼容平台使用显式本地状态目录覆盖，但仍保持每任务独立 worktree。

## 未采用方案

- **全部 Worker 使用主 Checkout**：Ownership、Rollback 与 Concurrency 都不清晰。
- **每个 Attempt 完整 Clone**：隔离更强，但更慢、重复对象存储且难处理本地未 Push Base，可作为高隔离 Profile。
- **失败后 In-place Reset**：可能销毁证据，绝不能自动执行。
