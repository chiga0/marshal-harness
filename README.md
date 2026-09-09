# Marshal

[![CI](https://github.com/chiga0/marshal-harness/actions/workflows/ci.yml/badge.svg)](https://github.com/chiga0/marshal-harness/actions/workflows/ci.yml)
[![Pages](https://img.shields.io/badge/docs-GitHub_Pages-blue)](https://chiga0.github.io/marshal-harness/)
[![License](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**让 Agent Team 可以长期、可靠地完成真实业务任务。**

Marshal 是一个可自托管的 Agent Team HTTP 服务：接收需求和上下文，确认方案后组织有界执行、集成与独立验收，交付可使用的成果并保留审计。用户只需理解 Task、Worker、Artifact；没有 Workspace/Project 注册。Git、表结构或平台信息是任务上下文，不是 Core 资源目录。

当前 Local MVP 已有执行、独立验证、审查和 Draft PR 的历史能力，RC1 支持面仍是下述 CLI-only local-dogfood。当前目标为 B1 真实团队 PoC、B2 本地 API 可用、B3 正式可靠发布；旧单任务与团队证据继续保留，尚未整体完成。

2026-09-07 方案收缩为 [Task-first Agent Team 架构](docs/agent-team-service-architecture.md)、[实施 Milestone](docs/agent-team-service-milestones.md)和[审计记录](docs/audit-agent-team-service-design-2026-09-07.md#task-first-收缩审计)：先用一个可用 Provider 的两个实例走通真实交付，不先做 Workspace、安装身份平台或全面迁库。目标一命令启动本地 HTTP，原生 Agent 自管登录/Skill，Supervisor 内置；SQLite/零 Git/问答在 B2，正式平台支持在 B3。边界由已接受的 [ADR 0085](docs/adr/0085-agent-team-service-contract-and-storage.md)承载；[ADR 0088](docs/adr/0088-node-task-service-production-projection.md)进一步接受 Node-only 正式实现路线，文档不代表功能已发布。

旧 Marshal skill 不再是产品运行依赖、研发准入或验收标准，不读取、加载或执行其流程；保留历史运行、失败和审计资产。Pi/Qwen Code/OpenCode 自带的 Skill、模型配置与登录仍由各 Agent 自行管理。

[阅读文档](https://chiga0.github.io/marshal-harness/) · [查看当前能力](https://chiga0.github.io/marshal-harness/current-status/) · [快速开始](https://chiga0.github.io/marshal-harness/getting-started/)

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

历史 Local MVP 曾在 macOS/Linux 本地单用户路径验证以下能力；这是旧实现的能力清单，不是新服务或 RC1 的统一支持矩阵：

- OpenCode、Qwen Code 和 Pi；
- 每个任务独立的 Git 工作区；
- 独立测试和交付物检查；
- 审查、有限返工与失败记录；
- 使用独立凭据创建 GitHub Draft PR；
- 中断后的检查、恢复和安全清理。

`v1.0.0-rc1` 已于 2026-09-01 发布为 unsigned、Mac-first、Darwin arm64、CLI-only 的 `darwin-local-dogfood` 预览。固定 candidate `c1407bd` 已由真实 Pi 完成单 Run/单 Attempt、ResultIngress、独立 Verification/ReviewDecision 并进入 `ACCEPTED`；GitHub prerelease、canary 与安装后二进制是同一 SHA-256。它不是 production、managed、notarized、hardened、server、Linux 或 stable release。详细状态见[当前可用能力](https://chiga0.github.io/marshal-harness/current-status/)。

## v1.0 发布目标

单节点、单用户、可信任务的 Task-first Agent Team 服务。目标命令 `marshal serve` 自动准备默认数据目录和本地访问保护；可选 data-dir 仅是本机启动配置，没有 Workspace ID/初始化向导、账号/组织或资源注册。接口以 `/v1/tasks` 为中心；这是待实现形态，不是 RC1 命令说明。

- **B1 团队 PoC**：复用现有合法固定安装/Store，用真实 Git 样例、一个 Provider 两实例并行；服务自主收集、独立验收、集成与下载交付。先补业务闭环，不先全面换库或等三品牌。
- **B2 本地 API 可用**：简短需求问答/确认、SQLite、零 Git 与多仓库、同版本恢复、任务图/Worker 详情/审计和更多 Adapter。Pi、Qwen Code、OpenCode 逐个声明实测支持；全部增强能力不作前置。
- **B3 正式发布**：同路径长期/故障与恢复验证、Darwin 签名/notarization、Linux 实机及受保护 stable release。U1 旧历史导入单独证明，不挡新任务；UI 仅核心 API-STABLE 后开发，不阻 API 发布。

控制/执行/存储三面逻辑分离，初期一个服务，Core 通过接口 DI 与 Agent/Sandbox/Store 解耦。旧 Go profile 的 Task 复用既有 Goal；当前 Node profile 使用自己的唯一 Application/SQLite，不调用 Go 或为兼容名称另造 Goal 真值。新旧数据根不混用。Agent 使用自身已配置模型和 Skill，不建设统一登录/Skill 平台；独立验证、受管目录单写、所属进程取消、最小持久事实与本地访问保护仍保留。

默认仅交付成果，不自动生产写/发布/merge。SQL 文件生成不等于生产执行/补数；可选 Draft PR 和其他外部写以后按独立权限与实测 profile 开放。旧 repository .marshal 不自动接管，不双写或清空。详细目标见[架构](docs/agent-team-service-architecture.md)，实施顺序见[计划](docs/implementation-plan.md)，真实完成情况只见 [Roadmap](docs/roadmap-status.md#业务交付当前表)。

### 2026-09-01 RC1 发布检查点

- annotated tag `v1.0.0-rc1`（tag object `e99326f`）精确指向 sourceHead `c1407bd`；candidate SHA-256 为 `f9ed7fa59d05f5e71fef7164b8015240497e1d18e25ef1d3f8e199c1378a3774`。
- 真实 Pi `0.84.4` canary run `33504020360` 与 finalize `33504247271` 完成单 Attempt、9 项 Gate、独立 Verification/ReviewDecision，并进入 `ACCEPTED`；receipt digest 为 `sha256:7bd5b500bbaff5c5b008922b713d9844b792a3e82ece4e4a46ccd837496b4525`。
- candidate exact-head CI run `33502847249` 的 Ubuntu、macOS 与 secret scan 全绿；release workflow run `33506656403` 只消费已冻结 carrier，不重建 candidate，并创建 GitHub prerelease。
- 从 GitHub release 外部下载后，二进制 SHA-256 仍与 canary 相同；在独立临时安装目录运行 `marshal version --json` 精确返回 `1.0.0-rc1`、commit `c1407bd`、Go `1.26.6`、`darwin/arm64` 与 `darwin-local-dogfood`。
- R2–R5 仍保持 `COMPONENT`；R6 进入 `IN_PROGRESS/COMPONENT`。本次发布只关闭 ADR 0068 的 local-dogfood prerelease distribution exit，不满足 ADR 0052 的 `RELEASED`、production 或 stable 门禁。
- stable `v1.0.0` 仍由 fixed server、[Issue #212](https://github.com/chiga0/marshal-harness/issues/212) 的 macOS signing/notarization、Linux stable release 与完整恢复/故障矩阵阻断。

[ADR 0067](docs/adr/0067-darwin-ordinary-user-launch-and-attach-recovery.md) 与 [ADR 0068](docs/adr/0068-mac-first-cli-only-lifecycle-preview-rc1.md) 定义的 Mac-first same-bytes RC1 路径已经走通并发布。RC1 的 tag 名不等于 stable v1.0：fixed server、managed signing/notarization、Linux stable 和 ADR 0052 的 `RELEASED` 门禁仍属于后继。

## 当前 Node 服务与 API 候选入口

当前研发主线使用固定 Node `24.15.0`，通过 [Task 服务启动说明](packages/task-service/README.md)配置原生 Agent、业务与独立验证并运行 HTTP；无需编译或执行 Marshal 原生程序。受信启动配置仍需提供，尚不承诺任意任务零配置。原生 Agent 的登录沿用与发布凭据分离是不同问题，不能因本机可运行就声称生产支持。

- [OpenAPI 3.1 定义](packages/task-api/openapi.json)是 HTTP 请求/响应的唯一机器契约；[接口说明](packages/task-api/README.md)与[客户端](packages/task-client/README.md)解释使用方式。
- [架构](docs/agent-team-service-architecture.md)、[目标用户与适用范围](docs/vision-and-scope.md)、[Milestone](docs/agent-team-service-milestones.md)与[实际进展](docs/roadmap-status.md#业务交付当前表)分别说明目标和完成情况。
- API 的 `0.1.0-candidate` 已通过当前 Node profile 与同包客户端的 API-STABLE 核心接口检查点；这不是正式 v1 或全平台支持，[精确证据与剩余出口](docs/node-task-service-status-2026-09-08.md)单独列明。[目录发行包](packages/task-distribution/README.md)也不等于 stable 安装包或正式部署。

## 历史 Go RC1 安装（不是 Node 服务入口）

Darwin arm64 用户可以显式安装已发布的 RC1；安装器不会请求 sudo、不会自动生成 activation，也不会修改 Gatekeeper、SIP 或 EDR。非 Darwin arm64、缺少精确资产、manifest/checksum/tag 漂移或缺少 preview opt-in 均 fail closed，不会回退源码或其它平台资产：

安装脚本不会请求 sudo：

```bash
curl -fsSL https://raw.githubusercontent.com/chiga0/marshal-harness/main/scripts/install.sh \
  | MARSHAL_TAG=v1.0.0-rc1 MARSHAL_LOCAL_DOGFOOD_PREVIEW=1 bash
marshal version --json
```

[查看 v1.0.0-rc1 prerelease](https://github.com/chiga0/marshal-harness/releases/tag/v1.0.0-rc1)。通用 latest/stable 安装不会自动选择 prerelease。

也可以从源码构建：

```bash
git clone https://github.com/chiga0/marshal-harness.git
cd marshal-harness
make build
```

升级、回滚（固定旧版本重装）与卸载的完整说明见 [docs/install-and-upgrade.md](docs/install-and-upgrade.md)。

## 历史 Go profile 最小使用流程

在准备交给 Coding Agent 的 Git 仓库中：

```bash
marshal init
marshal doctor --json

marshal task plan --task TASK.json --policy POLICY.json --run RUN_ID
marshal task approve --run RUN_ID --gate plan --actor YOUR_ID
marshal task run --run RUN_ID
marshal task verify --run RUN_ID
marshal task review --run RUN_ID
```

执行结束不等于任务通过。`verify` 会独立检查改动，`review` 会准备真实代码差异和检查结果。发布 Draft PR 的完整流程见[日常使用](https://chiga0.github.io/marshal-harness/usage/)。

## 发布与合入的临时顺序（Issue #25 修复合入前）

当前 `marshal task accept` 要求 PR 处于 OPEN/Draft；若先 merge 再 accept，Run 会被永久置为 `BLOCKED`（见公开 [Issue #25](https://github.com/chiga0/marshal-harness/issues/25) 与 [PR #24](https://github.com/chiga0/marshal-harness/pull/24)）。在协议修复合入前，请严格按以下唯一临时顺序操作：

1. `marshal task publish` —— 只创建或更新 Draft PR；
2. 等待冻结 TaskSpec.requiredChecks 全部明确成功；
3. 在 PR 仍为 OPEN 时运行 `marshal task accept` 并确认 Run=ACCEPTED；
4. 由维护者在 Marshal 之外 merge，之后可按仓库策略删除 head branch。

Marshal 不自动 merge，也不获得 merge 权限。已误入 `BLOCKED` 的 Run 只做只读检查与证据保留，等待后续 typed reconciliation；详细操作见[操作手册](docs/operator-runbook.md)。

## 安全边界

当前本地版本可以隔离任务工作区、过滤环境变量并分离发布凭据，但普通本地子进程不是恶意代码沙箱。不要使用本地模式运行不可信仓库、依赖或构建脚本；这类任务应使用容器、虚拟机或经过验证的远程 Sandbox。

更多信息见[安全与隐私](https://chiga0.github.io/marshal-harness/security/)。安全问题请按照 [SECURITY.md](SECURITY.md) 私下报告。

## 开源与贡献

Marshal 采用 [MIT License](LICENSE)，目标是提供可二次开发、可自行部署的基础设施。

贡献前请阅读 [CONTRIBUTING.md](CONTRIBUTING.md) 和 [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)。面向实现者的架构规范、ADR、Schema、审计和 Milestone 证据保留在仓库 `docs/` 与 `schemas/` 中，但不进入用户文档站点。
