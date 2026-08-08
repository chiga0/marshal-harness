# Marshal Harness

> English · 中文原文见 [项目首页](../index.md)

**Evidence-gated orchestration for coding agents.** The Lead Agent plans and reviews, swappable Worker Agents implement, and a deterministic Harness verifies, records evidence, and publishes under strict gates.

[![CI](https://github.com/chiga0/marshal-harness/actions/workflows/ci.yml/badge.svg)](https://github.com/chiga0/marshal-harness/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/License-MIT-yellow.svg)](https://github.com/chiga0/marshal-harness/blob/main/LICENSE)

> Status: Milestones 0–6 all passed; the Local MVP is marked `USABLE`. See [Roadmap Status](../roadmap-status.md).

## What problem it solves

Coding Agents differ in CLI, event format, permission model, and session semantics. Marshal provides all of them with a common foundation:

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

Marshal does not make agents smarter; it makes their work **verifiable, auditable, and safely delegable**. It is not a terminal runtime (see the [herdr comparison](../research/herdr-comparison.md), 中文), not a malicious-code sandbox, and provides no automatic merge. See the README for what it is and is not suited for.
