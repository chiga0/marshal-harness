# 架构决策记录

本目录记录对实现有实质约束的决策。ADR 0001–0005 由维护者于 2026-08-03 接受，ADR 0006–0007 于 2026-08-04 随真实 Adapter 与受控发布实施接受。

| ADR | 决策 | 状态 |
| --- | --- | --- |
| [0001](0001-cli-first-modular-monolith.md) | CLI-first 模块化单体 | 已接受（Accepted） |
| [0002](0002-worktree-isolation.md) | 每个任务一个独立 Worktree | 已接受（Accepted） |
| [0003](0003-separate-worker-and-publisher.md) | Worker 与 Publisher 分权 | 已接受（Accepted） |
| [0004](0004-independent-verification.md) | 独立证据具有权威性 | 已接受（Accepted） |
| [0005](0005-go-runtime.md) | Go 作为 Core Runtime | 已接受（Accepted） |
| [0006](0006-attempt-control-root.md) | Attempt 控制根与业务 Worktree 分离 | 已接受（Accepted） |
| [0007](0007-intent-first-publication.md) | 先记录意图的受控发布与远端对账 | 已接受（Accepted） |
