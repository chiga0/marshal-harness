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

## 从这里开始

| 你的目标 | 阅读路径 |
| --- | --- |
| 第一次使用 | [快速开始](getting-started.md) → [操作手册](operator-runbook.md) |
| 理解项目 | [核心概念](concepts.md) → [整体架构](architecture.md) → [Runtime 架构](runtime-architecture.md) |
| 查看完成度 | [Roadmap 状态](roadmap-status.md) |
| 参与开发 | [开发指南](development.md) → [实施计划](implementation-plan.md) |
| 查字段、状态或历史决策 | [参考索引](reference.md) |

主导航只展示高频、当前相关的内容。ADR、审计、研究、Milestone 报告和兼容性细节默认隐藏，统一通过[参考索引](reference.md)访问。

## 定位边界

Marshal 不让 Agent 更聪明，而是让 Agent 的工作**可验证、可审计、可安全委派**。确定性 Core 是唯一 Supervisor；LLM、Provider 与 durable backend 只提交 proposal、Evidence、Assessment 或 Receipt。Local Profile 不是恶意代码沙箱，系统不提供自动 merge。

当前可直接使用的是 Local MVP。`marshal-server`、远程 Sandbox、HA 与 Goal orchestration 均属于 M8–M13 的目标能力，尚未实现。
