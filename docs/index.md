# Marshal Harness

**让 Agent 可以长期、可靠地完成软件工程任务。**

Marshal 是一个可自托管的任务控制系统。它持续接收新的开发任务，把复杂需求拆成有限、可检查的执行步骤，安排不同 Agent 和执行环境完成工作，并保留恢复、验证与审计所需的信息。

[![CI](https://github.com/chiga0/marshal-harness/actions/workflows/ci.yml/badge.svg)](https://github.com/chiga0/marshal-harness/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/License-MIT-yellow.svg)](https://github.com/chiga0/marshal-harness/blob/main/LICENSE)

> 当前版本已经可以在本地完成 Coding Agent 的执行、独立验证、审查和 Draft PR 发布。常驻云端服务、远程 Sandbox 和跨任务 Goal 编排正在建设中。详见[当前可用能力](current-status.md)。

## 为什么需要 Marshal

直接运行 Coding Agent，通常很难稳定回答这些问题：

- Agent 修改的是不是正确的代码和版本？
- 它声称运行过的测试是否真的通过？
- 任务中断后应该从哪里恢复？
- 哪个 Agent 可以执行代码，哪个组件可以发布变更？
- 几天后还能否知道当时为什么接受或拒绝了结果？

Marshal 把这些问题交给确定性的控制系统，而不是让 Agent 自己证明自己。

## 你能得到什么

- **持续运行**：Runtime 可以长期在线，任务本身保持短小、有界，失败后可以恢复或重新执行。
- **结果可信**：实现完成后由独立步骤重新检查代码、测试和交付物。
- **权限隔离**：执行代码的 Agent 默认拿不到发布凭据；发布需要单独授权。
- **环境可替换**：本地进程、容器、云端 Sandbox 都可以作为执行环境，不把系统绑定到单一供应商。
- **过程可追溯**：成功、失败和中断都会留下结果与原因，便于复盘和审计。
- **适合复杂任务**：长期目标由多个有限任务逐步推进，而不是依赖一个永不退出的 Agent 会话。

## 从这里开始

| 你想了解什么 | 推荐阅读 |
| --- | --- |
| Marshal 适不适合我 | [Marshal 是什么](concepts.md) |
| 今天已经能做什么 | [当前可用能力](current-status.md) |
| 马上在本地试用 | [快速开始](getting-started.md) |
| 日常执行和排错 | [日常使用](usage.md) |
| 系统大体怎样工作 | [工作原理](how-it-works.md) |
| 用人类语言理解完整分层 | [十分钟理解 Marshal 架构](architecture-in-10-minutes.md) |
| 大任务如何先研讨、后复盘 | [前期研讨、复盘与受控协作](agent-collaboration-and-learning.md) |
| 安全边界和数据处理 | [安全与隐私](security.md) |

## 当前边界

Marshal 不会让模型本身变得更聪明，也不是用于运行恶意代码的安全沙箱。当前版本仍以本地单用户使用为主；尚未交付的能力会明确标注，不会当作现成功能宣传。
