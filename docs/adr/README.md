# 架构决策记录

本目录记录对实现有实质约束的决策。ADR 0001–0005 由维护者于 2026-08-03 接受；ADR 0006–0010 于 2026-08-04 随真实 Adapter、受控发布、可观察性与受控自治设计实施接受；ADR 0011 于 2026-08-05 根据真实三 Worker 委派证据接受；ADR 0012–0014 于 2026-08-07 随显式 abort、拒绝分级与 read-only 画像接受。2026-08-10 维护者重置长期目标并接受 ADR 0016，ADR 0015 未接受即被 ADR 0016 取代（Superseded before acceptance）。

| ADR | 决策 | 状态 |
| --- | --- | --- |
| [0001](0001-cli-first-modular-monolith.md) | CLI-first 模块化单体 | 已接受（Accepted） |
| [0002](0002-worktree-isolation.md) | 每个任务一个独立 Worktree | 已接受（Accepted） |
| [0003](0003-separate-worker-and-publisher.md) | Worker 与 Publisher 分权 | 已接受（Accepted） |
| [0004](0004-independent-verification.md) | 独立证据具有权威性 | 已接受（Accepted） |
| [0005](0005-go-runtime.md) | Go 作为 Core Runtime | 已接受（Accepted） |
| [0006](0006-attempt-control-root.md) | Attempt 控制根与业务 Worktree 分离 | 已接受（Accepted） |
| [0007](0007-intent-first-publication.md) | 先记录意图的受控发布与远端对账 | 已接受（Accepted） |
| [0008](0008-pluggable-observer-backends.md) | 可插拔 Observer Backend，cmux 作为首个可视化实现 | 已接受（Accepted） |
| [0009](0009-terminal-session-execution.md) | 原生 PTY Terminal Session 执行传输 | 已接受（Accepted） |
| [0010](0010-controlled-autonomy-and-intervention.md) | 受控自治、审批 Gate 与人工介入 | 已接受（Accepted） |
| [0011](0011-sealed-native-tui-transport.md) | 密封启动与可判定的原生 TUI 传输 | 已接受（Accepted） |
| [0012](0012-explicit-abort.md) | 废弃 Run 的显式 abort | 已接受（Accepted） |
| [0013](0013-graded-permission-denials.md) | Permission 拒绝分级（预期内 vs 致命） | 已接受（Accepted） |
| [0014](0014-read-only-execution-profile.md) | Read-only 执行画像（调研/评审最小权限） | 已接受（Accepted） |
| [0015](0015-production-deployment.md) | 生产部署：常驻服务、远程访问与 Dashboard 认证 | 未接受即被取代（Superseded before acceptance，由 0016 承接） |
| [0016](0016-durable-runtime-and-sandbox-provider.md) | 耐久 Runtime 与可插拔 Sandbox Provider | 已接受（Accepted，2026-08-10） |
