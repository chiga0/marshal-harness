# Marshal Harness

> English · 中文原文见 [项目首页](../index.md)

**A durable, self-hostable, deterministic control plane for agentic software engineering.** Marshal continuously accepts Goals and Tasks, admits complex work as bounded typed workloads, dispatches replaceable Agent and Sandbox providers, and keeps execution recoverable, auditable, and verifiable through durable state, independent Evidence, least privilege, and controlled SideEffects.

[![CI](https://github.com/chiga0/marshal-harness/actions/workflows/ci.yml/badge.svg)](https://github.com/chiga0/marshal-harness/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/License-MIT-yellow.svg)](https://github.com/chiga0/marshal-harness/blob/main/LICENSE)

> Current delivery: the embedded/local precursor (Milestones 0–6, Local MVP) is `USABLE`; M7 architecture and contracts passed, while M8–M13 remain `PLANNED`. Local MVP describes maturity, not the product boundary. See [Roadmap Status](../roadmap-status.md).

## What problem it solves

Long-running agent workloads need durable authority, bounded orchestration, provider-neutral execution, independent evidence, controlled side effects, and deterministic recovery. The current local release proves the Coding Task evidence chain; the server runtime, remote sandbox, HA, and Goal controller are not yet delivered.

- **Task contract**: frozen TaskSpec, locked baseline, dedicated worktree;
- **Evidence gates**: independent Verification, digest-bound ReviewDecision, CI-bound acceptance;
- **Controlled publication**: credential separation, Draft-only, idempotent, never auto-merge;
- **Failure semantics**: fail-closed, Outcome evidence, crash recovery.

## Quick start

See the [README](https://github.com/chiga0/marshal-harness) and the [Quick Start](quick-start.md) guide. The full operator flow is described in the [Operator Runbook](../operator-runbook.md) (中文).

## Reading order

1. [Vision & Scope](../vision-and-scope.md) (中文) → [Architecture](architecture.md) → [Task Lifecycle](task-lifecycle.md);
2. [Security Model](security-model.md) → [Verification & Review](../verification-and-review.md) (中文) → [Artifacts & Publishing](../artifact-and-publishing.md) (中文);
3. [ADR Index](adr/README.md) (0001–0014);
4. [Acceptance reports](../roadmap-status.md) (中文, Milestones 0–6);
5. [Research & audits](../research/herdr-comparison.md) (中文: herdr comparison, fan-out decision, A/B design).

## Boundaries

Marshal does not make agents smarter and does not make an LLM the system authority. Its deterministic Core is the sole supervisor and authority. The local profile is not a malicious-code sandbox, and merge is disabled by default. See the README for current delivery boundaries.
