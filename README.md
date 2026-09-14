# Marshal

[![Node team](https://github.com/chiga0/marshal-harness/actions/workflows/node-team.yml/badge.svg)](https://github.com/chiga0/marshal-harness/actions/workflows/node-team.yml)
[![文档](https://img.shields.io/badge/docs-GitHub_Pages-blue)](https://chiga0.github.io/marshal-harness/)
[![License](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**让 Agent Team 持续、可靠地交付真实业务成果。**

Marshal 是本机优先、可自托管的 Agent Team 服务。用户提交需求和上下文，确认必要的交付约定；Marshal 组织 Agent 执行、独立审查与验收，完成获准的交付，并保存结果、失败和决策依据。它面向希望通过 API、浏览器或自己的 Agent 客户端使用团队能力的个人开发者和系统集成者。

[使用文档](https://chiga0.github.io/marshal-harness/) · [快速开始](docs/getting-started.md) · [架构设计](docs/agent-team-service-architecture.md) · [标准 API](docs/standard-api.md)

## 为什么使用 Marshal

Agent 擅长理解和执行开放任务，但“执行结束”并不等于“业务完成”。多人或多 Agent 协作还需要明确输入、分工、验收、授权，以及中断后如何继续。

Marshal 将这些责任连接起来：

- **围绕成果组织工作**：先明确需求、交付物、验收和预算；一个 Worker 足够时不强制拆分，有互补职责时才并行。
- **独立检查结果**：作者不能为自己的成果签发权威通过；检查绑定实际候选，测试全绿也不能掩盖需求遗漏。
- **控制执行与外部操作**：Core 管批准范围、预算和事实；Agent 的建议或原生登录不自动授予发布权限。
- **保留连续工作依据**：任务状态、成果、决定和回执独立于 Agent 会话保存；恢复基于事实，未知外部结果先查询核对。
- **接入可替换的执行者**：通过明确的契约连接 Agent、执行环境和业务能力，客户端不依赖特定 Agent 品牌。

## 适合怎样的工作

适合输入与成果可明确、能够安排独立验收的任务，例如文档和代码成果协作、分析结果整理，以及通过已配置适配器完成的业务流程。用户主要面对 Task、Worker 和 Artifact；仓库、表和平台信息作为任务上下文提供，无须先注册 Workspace 或 Project。

产品目标和版本能力分别说明：请在[版本支持](docs/api-support.md)核对可运行的业务、驱动与环境，在[愿景与范围](docs/vision-and-scope.md)了解设计边界。生成 SQL 文件与生产执行、交付代码与部署上线，是不同的业务结果。

Marshal 不保证模型对任意业务的理解必然正确，也不保证团队总比单 Agent 更快。它不提供组织管理或统一 Skill 市场；Agent 管理自己的模型登录与原生 Skill。可信单用户模式保留职责、命令授权和证据权威分离，但普通宿主子进程不是恶意代码沙箱，同 UID 的凭据也不构成强隔离。

## 开始使用与集成

按照[快速开始](docs/getting-started.md)安装并连接一个已配置的 Agent，提交一个有明确成果和验收条件的 Task。版本选择、安装前置和运行参数在安装文档中维护。

| 想了解什么 | 入口 |
| --- | --- |
| 产品目标、适用场景与非目标 | [愿景与范围](docs/vision-and-scope.md) |
| 系统怎样协作，为什么这样设计 | [目标架构](docs/agent-team-service-architecture.md) |
| 对外 API 与组件间协作契约 | [标准 API](docs/standard-api.md)、[OpenAPI](packages/task-api/openapi.json) |
| 接入其他 Agent、环境和业务能力 | [扩展契约](docs/extension-contracts.md) |
| 权限、数据与恢复边界 | [安全模型](docs/security-model.md)、[生命周期](docs/task-lifecycle.md) |
| 实施出口和实际进展 | [Milestone](docs/agent-team-service-milestones.md)、[Roadmap](docs/roadmap-status.md) |

## 开源与贡献

Marshal 采用 [MIT License](LICENSE)，支持自行部署与二次开发。贡献请阅读 [CONTRIBUTING.md](CONTRIBUTING.md) 和 [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)。安全问题请按 [SECURITY.md](SECURITY.md) 私下报告。历史设计通过[合同地图](docs/design-contract-map.md)追溯，不作为现行产品的安装步骤。
