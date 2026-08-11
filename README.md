# Marshal

[![CI](https://github.com/chiga0/marshal-harness/actions/workflows/ci.yml/badge.svg)](https://github.com/chiga0/marshal-harness/actions/workflows/ci.yml)
[![Pages](https://img.shields.io/badge/docs-GitHub_Pages-blue)](https://chiga0.github.io/marshal-harness/)
[![License](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Marshal 是一个面向 Coding Agent 的证据门禁式编排框架。当前 Local MVP 由主 Agent 提交规划与审查意见、可替换 Worker Agent 实现，确定性的 Harness 负责接纳、验证、留痕和受控发布；目标形态是长寿命的确定性 Control Plane，持续调度有界的 typed workload。

> 项目状态：Milestone 0–6 全部通过，Local MVP 已标记 `USABLE`（见 [docs/roadmap-status.md](docs/roadmap-status.md)）。文档站：[chiga0.github.io/marshal-harness](https://chiga0.github.io/marshal-harness/)。采用 [MIT](LICENSE) 许可。

> Runtime 状态：M7 设计与契约已通过；M8–M13 仍为 `PLANNED`。C/S 服务、远程 Sandbox、通用 SideEffect 对账与 Goal 控制器是已冻结的目标，不是当前已实现能力。

## 安装

两种路径均不请求 sudo，脚本不执行任何特权操作。

**一行安装脚本**——存在 `v*` tag 的 GitHub release 且含本平台匹配资产时，下载预编译二进制并按 `SHA256SUMS` 校验；否则自动源码构建（要求 Go 版本满足 `go.mod`），并安装到 `~/.local/bin`：

```bash
curl -fsSL https://raw.githubusercontent.com/chiga0/marshal-harness/main/scripts/install.sh | bash
```

仓库 checkout 内也可直接运行 `bash scripts/install.sh`（此时优先用本地源码构建）。支持环境变量覆盖：`MARSHAL_INSTALL_DIR`（安装目录）、`MARSHAL_REPO`、`MARSHAL_TAG`（固定 release tag）、`MARSHAL_FORCE_SOURCE=1`（强制源码构建）。

**手动源码构建**——没有 release 资产的平台或需要自行编译时使用：

```bash
git clone https://github.com/chiga0/marshal-harness.git
cd marshal-harness
make build        # 产出 ./bin/marshal
```

release 资产命名约定与脚本手工验证步骤见[开发指南](docs/development.md)的「安装」小节；安装完成后的下一步见下方快速开始（`marshal init` / `marshal doctor`）。

## 快速开始

前置：Go 版本以 `go.mod` 为准；macOS 或 Linux。可选：安装要委派的 Coding Agent（OpenCode / Qwen Code / Pi）并配置其环境变量（见 [docs/operator-runbook.md](docs/operator-runbook.md) §9.1）。

```bash
make build          # 产出 ./bin/marshal
bin/marshal init    # 在任意 Git 仓库初始化本地状态（.marshal/，Git 忽略）
bin/marshal doctor --json   # 只读诊断，无副作用

# 标准任务循环（完整说明见操作手册）
bin/marshal task plan   --task TASK.json --policy POLICY.json --run RUN_ID
bin/marshal task approve --run RUN_ID --gate plan --actor 你的ID
bin/marshal task run     --run RUN_ID
bin/marshal task verify  --run RUN_ID
bin/marshal task review  --run RUN_ID                 # 导出 ReviewPacket
bin/marshal task review  --run RUN_ID --decision D.json
bin/marshal task publish --run RUN_ID                # 需要独立 GH 凭据 profile
bin/marshal task accept  --run RUN_ID
```

贡献请读 [CONTRIBUTING.md](CONTRIBUTING.md)；行为准则见 [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)；安全上报见 [SECURITY.md](SECURITY.md)。

## 定位

**Marshal 不让 Agent 更聪明，而是让 Agent 的工作可验证、可审计、可安全委派。**

当前实现是包裹在 Coding Agent 进程之外的本地控制平面；目标 Runtime 是可自托管的 C/S Control Plane。Marshal 解决“**谁有资格证明什么，以及哪些输入有资格改变权威状态**”：LLM、Provider 与 durable backend 只能提交 proposal、claim、evidence、assessment 或 receipt，只有确定性 Core 能接纳它们并写入权威账本。Local MVP 完全本地、凭据分权、默认只发 Draft PR、永不自动 merge。

## 适用场景

- **把真实开发任务委派给 Coding Agent，但要求证据**：冻结任务契约、锁定基线、独立验证、摘要绑定的审查，任何一环失败都 fail-closed；
- **无人值守跑任务**：崩溃可恢复、预算有界、失败留证据不产生虚假 PR；
- **需要审计与问责的多 Agent 协作**：谁批准了什么、基于哪份证据，全部 append-only 留痕；
- **AI 代码进入仓库前的门禁**：Draft PR + 远端 CI 绑定 + 人工 merge，适合对 AI 产出持谨慎态度的团队；
- **想换 Worker 不换流程**：同一生命周期接入 OpenCode / Qwen Code / Pi，Adapter 能力 Probe 冻结版本与权限。

## 不建议使用的场景

- **一次性问答或交互式结对**：直接用 Agent 本身，协议开销不值得；
- **琐碎小改动**：无门禁或最简流程即可（见操作手册的任务分级）；
- **期望全自动交付/自动 merge**：Marshal 默认且永远只到 Draft PR，merge 是人的决定；
- **运行不可信或恶意代码**：Local Profile 不是沙箱，不宣称抵抗同 UID 恶意进程；需要时用容器/VM（Hardened Profile 在延后路线）；
- **现在就需要成熟的多用户服务、远程调度或 Web UI**：当前是本地单用户 CLI；C/S Runtime 与远程调度位于 M8–M12，尚未实现；
- **期望框架提升 Agent 的代码质量**：Marshal 门禁证据，不提升模型能力；质量仍取决于你选的 Worker 与任务契约写得好不好。

## 为什么需要 Marshal

不同 Coding Agent 的命令行接口、事件格式、权限模型和会话语义并不一致。直接用脚本拼接这些 Agent，往往无法可靠回答以下问题：

- Worker 收到的任务究竟是什么？
- 它修改的是哪个基线版本？
- 它声称通过的测试是否真的运行并成功？
- 要求的交付物是否真实存在？
- 谁可以 push、创建 PR/MR 或合并？
- 中断后的任务能否被安全检查和恢复？

Marshal 为不同 Worker 提供统一的任务契约、生命周期、证据模型和发布门禁。

## 运行模式

当前 Local MVP 的角色映射是：

1. **主 Agent / Lead**：提交计划与验收标准，基于真实 diff 与证据生成 Review assessment；它不直接写权威状态。
2. **Worker Agent**：在独立 Git worktree 中产出 Candidate。它可以声明结果，但声明不能作为验证 Evidence。
3. **Verifier**：在 Worker 会话之外生成 Evidence；它不能为 Worker 自证，也不能自行作 ReviewDecision。
4. **Marshal Core**：确定性 Supervisor/Control Plane，管理状态、策略、预算、租约、证据接纳与 ReviewDecision 物化，是唯一业务权威。
5. **Publisher**：位于独立 Publication 信任域，按 Core 的精确授权执行 Draft 发布并返回 Receipt；它没有审查权，Worker 也没有发布凭据。

目标 Runtime 可把 Plan、Implement、Verify、Review 与 Publish 视为 typed execution，但不会把它们压成一个通用 Executor RPC。它们可以共享 queue、lease、heartbeat、cancel、日志和 Artifact 基座，仍须保持各自的 Schema、principal、credential、接纳规则与 protocol family。`Candidate != Evidence != Assessment != Publication Receipt`。

默认主 Agent 可以是 pi、Codex CLI/Desktop 等任意编码 Agent（见 [docs/lead-agent-surfaces.md](docs/lead-agent-surfaces.md)）；仓库内 `.agents/skills/marshal/` 的 Skill 会被支持它的 Agent 自动加载。ChatGPT 手机端 Remote 可以远程监督运行在开发机 Desktop 上的同一任务。首批 Worker Adapter 面向 Qwen Code、OpenCode 和 Pi，但核心领域模型不依赖任何单一 Agent 或交互界面。

## 核心不变量

- Worker 启动前必须锁定基线 commit。
- 每个写任务使用独立 worktree，且同一时间只有一个写入者。
- Worker 上报的测试和文件只是声明，不是证据。
- Worker 退出后，由 Marshal 独立执行并记录验收命令。
- 按策略，Worker 无法获得发布凭据。
- 成功的 Coding Task 必须产生可审查变更、验证证据、决策记录，以及按需创建的 PR/MR。
- 失败、阻塞、中止和无变更任务同样产生完整 Outcome Bundle，不创建无意义 PR。
- 实际 merge 是独立的外部副作用，默认由人工控制。

## 生命周期

```text
CREATED -> PLANNED -> READY -> RUNNING -> VERIFYING -> REVIEW_PENDING
                              |               ^                 |
                              v               |                 v
                        RETRY_PENDING         +--- REWORK_REQUESTED

REVIEW_PENDING -> PUBLISHING -> PUBLISHED -> CI_PENDING -> ACCEPTED
```

其他终态包括 `REJECTED`、`BLOCKED`、`ABORTED` 和 `NO_CHANGE`。完整转换规则见[任务生命周期](docs/task-lifecycle.md)。

## 文档导航

- [愿景与范围](docs/vision-and-scope.md)
- [架构设计](docs/architecture.md)
- [主 Agent 接入界面](docs/lead-agent-surfaces.md)
- [任务生命周期](docs/task-lifecycle.md)
- [任务契约](docs/task-contract.md)
- [Worker Adapter](docs/worker-adapters.md)
- [验证与审查](docs/verification-and-review.md)
- [交付物与发布](docs/artifact-and-publishing.md)
- [安全模型](docs/security-model.md)
- [故障与恢复](docs/failure-and-recovery.md)
- [Operator Runbook](docs/operator-runbook.md)
- [实施计划](docs/implementation-plan.md)
- [开发指南](docs/development.md)
- [Milestone 0 验收报告](docs/milestone-0-report.md)
- [Milestone 1 验收报告](docs/milestone-1-report.md)
- [Milestone 2 验收报告](docs/milestone-2-report.md)
- [Milestone 3 验收报告](docs/milestone-3-report.md)
- [Milestone 4 验收报告](docs/milestone-4-report.md)
- [Milestone 5 验收报告](docs/milestone-5-report.md)
- [Roadmap 状态](docs/roadmap-status.md)
- [本地环境基线](docs/environment-baseline.md)
- [设计审计报告](docs/audit-report.md)
- [架构决策记录](docs/adr/README.md)

机器可读契约草案位于 [`schemas/`](schemas/)。

## MVP 计划

首个版本的 Marshal Core 是本地 CLI-first、Go 实现的模块化单体；CLI-first 不限制主 Agent 的交互界面。MVP 支持：

- 每次调用处理一个仓库；
- 每个仓库使用默认忽略的 `.marshal/` 保存本地运行态和任务 worktree；
- 多个相互隔离的任务 worktree；
- 每个任务 worktree 同时只有一个写入者；
- Qwen Code、OpenCode 和 Pi 的 one-shot Adapter；
- 标准化 JSONL 事件；
- 独立的 Git 范围与命令验证；
- 有上限的 Review/Rework 循环；
- Outcome Bundle；
- GitHub Draft PR 发布；
- 中断后的安全检查与恢复。

强恶意代码隔离、分布式调度、Web 控制台、自动 Agent 排名和无人值守 merge 不属于 MVP 承诺。

## 实施门禁

文档审计结论为 `APPROVED_FOR_IMPLEMENTATION`，维护者已接受 ADR 0001–0011 和 Local MVP 范围。Milestone 0–5 已通过；File-based Review Bridge、Codex Skill、三 Worker Adapter 路径与 GitHub Draft Publisher 已通过各自真实 E2E。原生 TUI Transport 基础与 Operator Runbook 已完成，当前继续真实受监督 cmux Pilot 与完整 MVP E2E 加固。
