# Architecture Decision Records (ADR Index)

> English · 中文原文见 [架构决策记录](../../adr/README.md)。ADR 正文以中文为准，下表链接指向中文原文。

This directory records the decisions that substantively constrain the implementation. ADR 0001–0005 were accepted by the maintainers on 2026-08-03; ADR 0006–0010 were accepted on 2026-08-04 together with the real Adapter, controlled publication, observability, and controlled-autonomy design implementation; ADR 0011 was accepted on 2026-08-05 based on real three-Worker delegation evidence; ADR 0012 was accepted on 2026-08-07 together with the explicit abort lifecycle extension.

| ADR | Decision | Status |
| --- | --- | --- |
| [0001](../../adr/0001-cli-first-modular-monolith.md) | CLI-first modular monolith | Accepted |
| [0002](../../adr/0002-worktree-isolation.md) | One dedicated worktree per task | Accepted |
| [0003](../../adr/0003-separate-worker-and-publisher.md) | Separation of Worker and Publisher | Accepted |
| [0004](../../adr/0004-independent-verification.md) | Independent evidence is authoritative | Accepted |
| [0005](../../adr/0005-go-runtime.md) | Go as the Core runtime | Accepted |
| [0006](../../adr/0006-attempt-control-root.md) | Attempt control root separated from the business worktree | Accepted |
| [0007](../../adr/0007-intent-first-publication.md) | Intent-first controlled publication with remote reconciliation | Accepted |
| [0008](../../adr/0008-pluggable-observer-backends.md) | Pluggable Observer backends; cmux as the first visualization implementation | Accepted |
| [0009](../../adr/0009-terminal-session-execution.md) | Native PTY Terminal Session execution transport | Accepted |
| [0010](../../adr/0010-controlled-autonomy-and-intervention.md) | Controlled autonomy, approval gates, and human intervention | Accepted |
| [0011](../../adr/0011-sealed-native-tui-transport.md) | Sealed launch and decidable native TUI transport | Accepted |
| [0012](../../adr/0012-explicit-abort.md) | Explicit abort for abandoned Runs | Accepted |
| [0013](../../adr/0013-graded-permission-denials.md) | Graded permission denials (expected vs fatal) | Accepted |
| [0014](../../adr/0014-read-only-execution-profile.md) | Read-only execution profile (least privilege for research/review) | Accepted |
