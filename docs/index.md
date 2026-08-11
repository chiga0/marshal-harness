# Marshal Harness

**证据门禁式 Coding Agent 编排器。** 当前 Local MVP 对 Coding Agent 的 Candidate 做独立验证、审查接纳与受控发布；目标 Runtime 是长寿命、可自托管的确定性 Control Plane，持续调度有界 typed workload。

[![CI](https://github.com/chiga0/marshal-harness/actions/workflows/ci.yml/badge.svg)](https://github.com/chiga0/marshal-harness/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/License-MIT-yellow.svg)](https://github.com/chiga0/marshal-harness/blob/main/LICENSE)

> 状态：Milestone 0–6 全部通过，Local MVP `USABLE`；M7 设计与契约通过，M8–M13 仍为 `PLANNED`。详见 [路线图状态](roadmap-status.md)。

## 它解决什么

不同 Coding Agent 的 CLI、事件格式、权限模型与会话语义各不相同。Marshal 为它们提供统一的：

- **任务契约**：冻结的 TaskSpec、锁定基线、独立 worktree；
- **证据门禁**：独立 Verification、摘要绑定的 ReviewDecision、CI 绑定的验收；
- **受控发布**：凭据分权、Draft-only、幂等、永不自动 merge；
- **失败语义**：fail-closed、Outcome 证据、崩溃恢复。

## 快速开始

见 [项目简介（README）](https://github.com/chiga0/marshal-harness#快速开始) 与 [开发指南](development.md)。操作者全流程见 [操作手册](operator-runbook.md)。

## 阅读路线

1. [愿景与范围](vision-and-scope.md) → [架构设计](architecture.md) → [任务生命周期](task-lifecycle.md)；
2. [安全模型](security-model.md) → [验证与审查](verification-and-review.md) → [产物与发布](artifact-and-publishing.md)；
3. [Runtime 架构](runtime-architecture.md) → [ADR 索引](adr/README.md)（0001–0019）；
4. [验收报告](roadmap-status.md)（Milestone 0–13 分层状态）；
5. [研究与审计](research/herdr-comparison.md)（herdr 对照、fan-out 决策、A/B 设计）。

## 定位边界

Marshal 不让 Agent 更聪明，而是让 Agent 的工作**可验证、可审计、可安全委派**。确定性 Core 是唯一 Supervisor；LLM、Provider 与 durable backend 只提交 proposal、evidence、assessment 或 receipt。Local Profile 不是恶意代码沙箱，系统不提供自动 merge。适用与不适用场景见 README。
