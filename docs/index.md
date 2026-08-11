# Marshal Harness

**面向 Agent 驱动软件工程的长寿命、可自托管、确定性 Control Plane。** Marshal 持续接收 Goal 与 Task，把复杂需求接纳为有界的 typed workload，调度可替换 Agent 与 Sandbox Provider，并通过耐久状态、独立 Evidence、最小权限和受控 SideEffect，使执行可恢复、可审计、可验证。

[![CI](https://github.com/chiga0/marshal-harness/actions/workflows/ci.yml/badge.svg)](https://github.com/chiga0/marshal-harness/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/License-MIT-yellow.svg)](https://github.com/chiga0/marshal-harness/blob/main/LICENSE)

> 当前交付：Milestone 0–6 的 embedded/local 先行实现（Local MVP）已 `USABLE`；M7 终态设计与契约通过，M8–M13 仍为 `PLANNED`。Local MVP 是当前成熟度，不是产品定位。详见[路线图状态](roadmap-status.md)。

## 它解决什么

复杂 Agent 工作负载不仅要“能执行”，还必须在长期运行、故障恢复和跨 Provider 调度时保持权威状态一致。Marshal 提供统一的：

- **耐久控制**：常驻 Control Plane、append-only authority ledger、可重建投影与幂等提交；
- **有界编排**：Goal proposal 经确定性接纳后物化为有限 Task/Run/Attempt，预算与并发可控；
- **可插拔执行**：Agent、Sandbox、Verification、Publication、Artifact 与 Secret 通过各自版本化 Port 接入；
- **证据门禁**：独立 Verification、摘要绑定的 ReviewDecision、CI 绑定的验收；
- **安全副作用**：最小权限、凭据分域、intent/receipt/reconcile、默认禁止自动 merge；
- **恢复与审计**：lease、heartbeat、fencing、Outcome 与可回放历史。

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

Marshal 不让 Agent 更聪明，也不让 LLM 充当系统权威。确定性 Core 是唯一 Supervisor；LLM、Provider 与 durable backend 只提交 proposal、Candidate、Evidence、Assessment 或 Receipt。Local Profile 不是恶意代码沙箱，Merge 默认禁用。

当前可直接使用的是 embedded/local 先行实现，用于本地 Coding Task 的证据门禁与受控发布。`marshal-server`、远程 Sandbox、HA 与 Goal orchestration 均属于 M8–M13 的目标能力，尚未实现。
