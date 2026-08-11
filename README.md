# Marshal

[![CI](https://github.com/chiga0/marshal-harness/actions/workflows/ci.yml/badge.svg)](https://github.com/chiga0/marshal-harness/actions/workflows/ci.yml)
[![Pages](https://img.shields.io/badge/docs-GitHub_Pages-blue)](https://chiga0.github.io/marshal-harness/)
[![License](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Marshal 是面向 Agent 驱动软件工程的**长寿命、可自托管、确定性 Control Plane**。它持续接收 Goal 与 Task，把复杂需求接纳为有界的 typed workload，调度可替换的 Agent 与 Sandbox Provider，并通过耐久状态、独立 Evidence、最小权限和受控 SideEffect，使执行可恢复、可审计、可验证。

> 当前交付：Milestone 0–6 全部通过，embedded/local 先行实现（Local MVP）已标记 `USABLE`（见 [docs/roadmap-status.md](docs/roadmap-status.md)）。它是终态架构的可用基线，不是 Marshal 的产品边界。文档站：[chiga0.github.io/marshal-harness](https://chiga0.github.io/marshal-harness/)。采用 [MIT](LICENSE) 许可。

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

**Marshal 不替代 Agent，也不靠另一个 LLM 充当全局权威；它为 Agent 工作负载提供可长期运行的确定性控制面。**

Marshal 解决“**谁有资格证明什么，以及哪些输入有资格改变权威状态**”：LLM、Provider 与 durable backend 只能提交 proposal、claim、Evidence、Assessment 或 Receipt，只有确定性 Core 能接纳它们并写入权威账本。生产终态采用常驻 C/S Runtime；embedded/local 模式长期保留，并与服务端共享同一生命周期、Policy 和接纳规则。

当前可下载版本实现了本地 Coding Task 的完整证据链：凭据分权、独立 Verification、受控 Draft PR 和显式恢复。`marshal-server`、远程 Sandbox、HA 与 Goal controller 尚未交付，具体边界以 [Roadmap 状态](docs/roadmap-status.md)为准。

## Marshal 面向的场景

- **持续接收复杂软件工程需求**：由长寿命 Runtime 接纳 Goal/Task，再分发为有界 Run/Attempt，而不是让单任务或单 Agent 永生；
- **把真实开发任务委派给 Agent，但要求证据**：冻结任务契约、锁定基线、独立验证、摘要绑定的审查，任何一环失败都 fail-closed；
- **无人值守且可恢复地执行**：进程、机器或 Provider 故障后能够对账和恢复，预算有界，失败保留 Outcome；
- **需要审计与问责的多 Agent 协作**：谁提交、执行、验证、接纳或授权了什么，全部通过权威账本留痕；
- **AI 代码进入仓库前的门禁**：Draft PR + 远端 CI 绑定 + 人工 merge，适合对 AI 产出持谨慎态度的团队；
- **替换 Agent、Sandbox 或基础设施而不改任务语义**：Provider 通过版本化 Port 与 conformance 契约接入，不成为 Core 定义。

其中本地 Coding Task 证据链已经可用；常驻服务、远程 Provider 和复杂 Goal 编排按 M8–M13 逐步交付。

## 不建议使用的场景

- **一次性问答或交互式结对**：直接用 Agent 本身，协议开销不值得；
- **琐碎小改动**：无门禁或最简流程即可（见操作手册的任务分级）；
- **期望全自动交付/自动 merge**：Marshal 默认且永远只到 Draft PR，merge 是人的决定；
- **运行不可信或恶意代码**：Local Profile 不是沙箱，不宣称抵抗同 UID 恶意进程；需要时用容器/VM（Hardened Profile 在延后路线）；
- **现在就需要成熟的多用户服务、远程调度或 Web UI**：当前发行版仍是本地单用户 CLI；这些产品目标位于 M8–M12，尚未实现；
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

当前 embedded/local 实现中的角色映射是：

1. **主 Agent / Lead**：提交计划与验收标准，基于真实 diff 与证据生成 Review assessment；它不直接写权威状态。
2. **Worker Agent**：在独立 Git worktree 中产出 Candidate。它可以声明结果，但声明不能作为验证 Evidence。
3. **Verifier**：在 Worker 会话之外生成 Evidence；它不能为 Worker 自证，也不能自行作 ReviewDecision。
4. **Marshal Core**：确定性 Supervisor/Control Plane，管理状态、策略、预算、租约、证据接纳与 ReviewDecision 物化，是唯一业务权威。
5. **Publisher**：位于独立 Publication 信任域，按 Core 的精确授权执行 Draft 发布并返回 Receipt；它没有审查权，Worker 也没有发布凭据。

在终态架构中，Plan、Implement、Verify、Review 与 Publish 属于 typed execution，但不会被压成一个通用 Executor RPC。它们可以共享 queue、lease、heartbeat、cancel、日志和 Artifact 基座，仍须保持各自的 Schema、principal、credential、接纳规则与 protocol family。`Candidate != Evidence != Assessment != Publication Receipt`。

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

- [文档首页](https://chiga0.github.io/marshal-harness/)：按使用目标选择阅读路径。
- [快速开始](docs/getting-started.md)：安装当前版本并完成第一个本地任务。
- [核心概念](docs/concepts.md)：用最小模型理解 Marshal。
- [整体架构](docs/architecture.md)：Marshal 终态产品架构及当前实现映射。
- [Runtime 架构](docs/runtime-architecture.md)：M7–M13 的规范设计入口。
- [Roadmap 状态](docs/roadmap-status.md)：区分已实现与计划能力。
- [参考索引](docs/reference.md)：协议、生命周期、安全、ADR、审计和历史资料。

机器可读契约草案位于 [`schemas/`](schemas/)。

## 当前建设阶段

终态产品方向已经由 ADR 0016–0019 冻结。Local MVP（M0–M6）是当前可用的 embedded/local 先行实现，包括本地 CLI、三类 Worker Adapter、独立 Verification、Review/Rework、GitHub Draft PR、Outcome 与恢复工具；M7 架构设计已经通过。

长寿命 C/S Runtime、远程 Sandbox、生产 HA 和 Goal orchestration 位于 M8–M13，仍为 `PLANNED`。不要根据目标架构推断这些功能已经实现；以 [Roadmap 状态](docs/roadmap-status.md)为准。
