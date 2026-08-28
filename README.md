# Marshal

[![CI](https://github.com/chiga0/marshal-harness/actions/workflows/ci.yml/badge.svg)](https://github.com/chiga0/marshal-harness/actions/workflows/ci.yml)
[![Pages](https://img.shields.io/badge/docs-GitHub_Pages-blue)](https://chiga0.github.io/marshal-harness/)
[![License](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**让 Agent 可以长期、可靠地完成软件工程任务。**

Marshal 是一个可自托管的任务控制系统。它持续接收新的开发任务，把复杂需求拆成有限、可检查的执行步骤，安排不同 Agent 和执行环境完成工作，并保留恢复、验证与审计所需的信息。

当前版本已经可以在本地完成 Coding Agent 的执行、独立验证、审查和 GitHub Draft PR 发布。v1.0 正在把这些组件收敛为一条可恢复的 Agent-in-Sandbox 生产链；常驻云端、多节点 HA 与跨任务 Goal 编排属于后续 1.x。

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
- **安全委派**：执行与发布权限分离，默认只创建 Draft PR，不自动合并。
- **可插拔环境**：目标架构支持本地、容器和云端 Sandbox，不绑定单一供应商。
- **可审计结果**：成功、失败、中断和无需改动都会保存结果与原因。
- **复杂任务**：长期目标由多个有限任务逐步推进，而不是依赖永不退出的 Agent 会话。

## 当前可用版本

目前可用的是 macOS 与 Linux 上的本地单用户版本，支持：

- OpenCode、Qwen Code 和 Pi；
- 每个任务独立的 Git 工作区；
- 独立测试和交付物检查；
- 审查、有限返工与失败记录；
- 使用独立凭据创建 GitHub Draft PR；
- 中断后的检查、恢复和安全清理。

`marshal-server`、Sandbox SPI、ResultIngress 和恢复组件已经存在。2026-08-28 的候选链已用固定 Pi `0.84.3` 二进制在锁定 sourceHead `8df8b88` 上走到 ResultIngress、独立 `verify`、跨进程恢复和正式 ReviewPacket/`REVIEW_PENDING`；但它还没有导入独立 ReviewDecision 并进入 `ACCEPTED`，当前主线也仍待重跑同一 live canary。因此这是一条接近闭环的候选证据，不是 v1.0 集成或发布完成。详细状态见[当前可用能力](https://chiga0.github.io/marshal-harness/current-status/)。

## v1.0 发布目标

v1.0 只承诺单节点、单用户、可信仓库：至少一个真实 AgentProvider 在真实 Local/Container Sandbox allocation 中运行，命令和结果由同一 durable authority ledger 管理，结果只经 ResultIngress 接纳，并通过重启恢复、双 binding、独立验证和故障注入。发布支持 `publication:none` 与可选 GitHub Draft PR，默认不 merge。

Cloudflare 完整生产拓扑、多节点 HA、多用户/多租户、全部 Provider hardened 矩阵、Web UI 与复杂 Goal DAG 延期到 1.x，不阻塞首个正式版本。完整范围见 [ADR 0052](docs/adr/0052-v1-release-scope-and-production-reachability.md) 与 [Roadmap](docs/roadmap-status.md)。

### 2026-08-28 发布检查点

- Pi `0.84.3` fixed-bin strict E2E 的最新 live 证据绑定锁定 sourceHead `8df8b88`，已通过到 `REVIEW_PENDING`，证明真实 Agent result bytes 可经过 ResultIngress、独立验证、ReviewPacket 和跨进程恢复；相关实现已合入 `main@5d5c426`，R5 退出前须在当前主线重跑，并用现有 `task review --decision` 导入独立 ReviewDecision 到 `ACCEPTED`。
- Qwen Code `0.22.0` 已通过 macOS ordinary-user workspace live adapter 验证，可作为本地兼容 Worker；它不是 `LaunchCapable`，不能满足 production profile 或 hardened authority 门禁。
- durable agent authority、fixed-bin strict E2E 与 RC identity/install 聚合已由独立 reviewer 判定 P0/P1 为零，并于 `main@5d5c426` 合入、推送；`marshal-server` start/status/recovery controller 尚在聚合返工，ResultIngress 到 Run journal 的崩溃原子性仍在收口，因此 R2–R4 保持 `COMPONENT`。
- RC artifact identity/install 切片已获独立 `APPROVED`，但尚未发布任何 RC。稳定 `v1.*` 仍由 [Issue #212](https://github.com/chiga0/marshal-harness/issues/212) 的 macOS signing/notarization 和 Linux stable release gate 阻断；当前只允许证据完整的 unsigned prerelease。

## 安装

安装脚本不会请求 sudo：

```bash
curl -fsSL https://raw.githubusercontent.com/chiga0/marshal-harness/main/scripts/install.sh | bash
marshal version
```

也可以从源码构建：

```bash
git clone https://github.com/chiga0/marshal-harness.git
cd marshal-harness
make build
```

升级、回滚（固定旧版本重装）与卸载的完整说明见 [docs/install-and-upgrade.md](docs/install-and-upgrade.md)。

## 最小使用流程

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
