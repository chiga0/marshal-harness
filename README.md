# Marshal

[![Node team](https://github.com/chiga0/marshal-harness/actions/workflows/node-team.yml/badge.svg)](https://github.com/chiga0/marshal-harness/actions/workflows/node-team.yml)
[![Pages](https://img.shields.io/badge/docs-GitHub_Pages-blue)](https://chiga0.github.io/marshal-harness/)
[![License](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**让 Agent Team 可以长期、可靠地完成真实业务任务。**

Marshal 是一个可自托管的 Agent Team HTTP 服务：接收需求和上下文，确认方案后组织有界执行、集成与独立验收，交付可使用的成果并保留审计。用户只需理解 Task、Worker、Artifact；没有 Workspace/Project 注册。Git、表结构或平台信息是任务上下文，不是 Core 资源目录。

当前目标按已接受的 [ADR0094](docs/adr/0094-trusted-single-user-role-team.md) 收敛为可信单用户角色团队：[受管 Leader](docs/node-leader-execution-design.md) 贯穿需求、批次结果、独立 Review、局部修正、授权交付与后验。Supervisor 观察，Leader 业务判断，Core 校验/硬规则，Execution 操作所属进程；当前 Planner 不代表此机制已完成。B1 本机 PoC 经独立核验通过，B2-L 仍为 DESIGN、正式发布前必验，B2/B3 未完成。角色职责不是 OS/凭据隔离；现有 API/数据与 API-STABLE 范围不变，下载不叫已发布。

设计原则是：**Leader 对业务目标负责，Agent 发挥专业能力，Core 保证执行边界与事实可靠，最终交付接受独立且贴近真实需求的检查。** 原始需求、确认的交付约定与可调整实现计划分开，避免只证明“流程跑完”，却遗漏用户真正需要的成果。

[阅读文档](https://chiga0.github.io/marshal-harness/) · [快速开始（Node 主线）](https://chiga0.github.io/marshal-harness/getting-started/)

## 为什么需要 Marshal

Coding Agent 很擅长修改代码，但单独使用时很难稳定回答：

- 它修改的是不是正确的代码和版本？
- 它声称运行过的测试是否真的通过？
- 任务中断后应该从哪里恢复？
- 执行代码的 Agent 是否也拿到了发布凭据？
- 几天后还能否知道结果为什么被接受或拒绝？

Marshal 把这些问题交给确定性的控制系统，而不是让 Agent 自己证明自己。

## 主要能力

- **长期运行**：Runtime 持续接受新任务；单次执行保持有限，失败后可以恢复或重新安排。
- **独立验证**：Agent 结束后重新观察真实改动并运行验收步骤。
- **安全委派**：执行与发布权限分离，默认 `publication:none` 只交付成果；经授权可创建 Draft PR，不自动合并。
- **可插拔环境**：目标架构支持本地、容器和云端 Sandbox，不绑定单一供应商。
- **可审计结果**：成功、失败、中断和无需改动都会保存结果与原因。
- **复杂任务**：长期目标由多个有限任务逐步推进，而不是依赖永不退出的 Agent 会话。

## 当前可用版本

**Node Agent Team 已发布 v1.0.0 / v1.0.1 / v1.0.2（2026-09-10，同一主线逐候选递进）。** 安装使用 [Node 一键部署说明](docs/node-install.md)，上手与两档配置情形见 [快速开始](https://chiga0.github.io/marshal-harness/getting-started/)。Node 安装不包含 Qwen/Pi、模型登录、业务配置或自动启动；正式发布状态以 [Roadmap 当前表](docs/roadmap-status.md#业务交付当前表)为准。

历史说明（2026-09-10）：Go 历史线已按 [ADR 0099](docs/adr/0099-go-legacy-line-retirement-and-removal.md) 退役并从 main 移除，包括 History RC1（`v1.0.0-rc1`，unsigned、Darwin arm64、CLI-only 的 `darwin-local-dogfood` prerelease）的实现载体。历史字节与发行证据完整保留在 git 历史与 tag（tag object `e99326f` → sourceHead `c1407bd`）中，文档站「历史资料」区的 Go 时代页面仅作追溯。

## v1.0 发布目标

单节点、单用户、可信任务的 Task-first Agent Team 服务。目标命令 `marshal serve` 自动准备默认数据目录和本地访问保护；可选 data-dir 仅是本机启动配置，没有 Workspace ID/初始化向导、账号/组织或资源注册。接口以 `/v1/tasks` 为中心。

- **B1 团队 PoC**：用真实样例、一个 Provider 两实例并行；服务自主收集、独立验收、集成与下载交付。先补业务闭环，不先全面换库或等三品牌。
- **B2 本地 API 可用**：简短需求问答/确认、SQLite、零 Git 与多仓库、同版本恢复、任务图/Worker 详情/审计和更多 Adapter。Pi、Qwen Code、OpenCode 逐个声明实测支持；全部增强能力不作前置。
- **B3 正式发布**：同路径长期/故障与恢复验证、Darwin 签名/notarization、Linux 实机及受保护 stable release。UI 仅核心 API-STABLE 后开发，不阻 API 发布。

控制/执行/存储三面逻辑分离，初期一个服务，Core 通过接口 DI 与 Agent/Sandbox/Store 解耦。Node profile 使用自己的唯一 Application/SQLite。Agent 使用自身已配置模型和 Skill，不建设统一登录/Skill 平台；独立验证、受管目录单写、所属进程取消、最小持久事实与本地访问保护仍保留。

默认仅交付成果，不自动生产写/发布/merge。SQL 文件生成不等于生产执行/补数；B2 按 ADR0094 补一个明确授权、可观察回执的代表性业务发布及发布后验证闭环，不先支持任意高风险外部写。Marshal 软件自身的受保护发行仍保留 B3 gate。详细目标见[架构](docs/agent-team-service-architecture.md)，真实完成情况只见 [Roadmap](docs/roadmap-status.md#业务交付当前表)。

## 当前 Node 服务与 API 候选入口

对话接入可使用 [marshal-client 薄 Skill](skills/marshal-client/SKILL.md)：将 `skills/marshal-client` 整个目录复制到宿主所支持的 Skill 发现目录，并提供已运行 Marshal 的安装根与连接文件路径。此 Skill 仅说明如何调用发行包 HTTP 客户端、转达确认和展示结果，不启动 Worker、不恢复历史 Marshal 研发治理流程，也不自动配置业务。安装 Skill 不等于服务已经运行或任意 ETL 已接线。

当前 Node 服务接纳 Node `>=22` 并检查实际 SQLite 能力；已验证 22.22.1 和 24.15.0。[一键安装](docs/node-install.md)不要求固定到单一版本。通过 [Task 服务启动说明](packages/task-service/README.md)配置原生 Agent、业务与独立验证并运行 HTTP。受信启动配置仍需提供，尚不承诺任意任务零配置。原生登录不授予业务发布权限；可信单用户下可能存在 ambient credential，不提供恶意代码强隔离。

- [OpenAPI 3.1 定义](packages/task-api/openapi.json)是 HTTP 请求/响应的唯一机器契约；[接口说明](packages/task-api/README.md)与[客户端](packages/task-client/README.md)解释使用方式。
- [逐接口支持矩阵](docs/node-api-support-matrix.md)保留其标注源码的 25 项合同、实际实现和实机范围；[Roadmap 当前表](docs/roadmap-status.md#业务交付当前表)已记录显式 v6 profile 支持单 Worker 取消，旧格式仍 501。暂停只阻止新执行，token/费用仍不可测；矩阵快照与最新增量分开，不把路由示例当作功能完成。
- [架构](docs/agent-team-service-architecture.md)、[目标用户与适用范围](docs/vision-and-scope.md)、[Milestone](docs/agent-team-service-milestones.md)与[实际进展](docs/roadmap-status.md#业务交付当前表)分别说明目标和完成情况。

## 安全边界

当前本地版本可以隔离任务工作区、过滤环境变量并分离发布凭据，但普通本地子进程不是恶意代码沙箱。不要使用本地模式运行不可信仓库、依赖或构建脚本；这类任务应使用容器、虚拟机或经过验证的远程 Sandbox。

更多信息见[安全与隐私](https://chiga0.github.io/marshal-harness/security/)。安全问题请按照 [SECURITY.md](SECURITY.md) 私下报告。

## 开源与贡献

Marshal 采用 [MIT License](LICENSE)，目标是提供可二次开发、可自行部署的基础设施。

贡献前请阅读 [CONTRIBUTING.md](CONTRIBUTING.md) 和 [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)。面向实现者的架构规范、ADR、审计和 Milestone 证据保留在仓库 `docs/` 中，但不进入用户文档站点。
