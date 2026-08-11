# Marshal 仓库协作指南

本仓库采用分层治理，协作规则分为三层：

- **universal**：适用于所有参与者（维护者、外部贡献者与 AI Agent），任何情况下不得豁免；
- **maintainer-only**：仅适用于通过验证的维护者，授予 canonical 仓库的直接操作与评审职责；
- **external-contributor**：外部贡献者护栏；不满足维护者验证条件时一律按此层执行。

除明确标注层级的章节外，本文档其余章节均为 universal 规则。

## 当前阶段

本仓库已于 2026-08-03 通过实施门禁，ADR 0001–0011 已接受；2026-08-07 增补接受 ADR 0012–0014；2026-08-10 接受 ADR 0016，把长期目标重置为长寿命 Runtime/Control Plane，并冻结 AgentAdapter 与 SandboxProvider 分层及 M7–M13 路线（M7–M12 为耐久 Runtime 平台阶段，M13 为 Goal 编排；见 `docs/runtime-architecture.md` 与 `docs/implementation-plan.md`），ADR 0015 未接受即被 ADR 0016 取代。Milestone 0–6 已全部通过，Local MVP 标记 `USABLE`。后续变更仍须按门禁流程：信任边界/持久化契约/生命周期/发布权限的改变必须新增或替代 ADR。

## 修改设计前必读

按顺序阅读：

1. `README.md`
2. `docs/vision-and-scope.md`
3. `docs/architecture.md`
4. `docs/task-lifecycle.md`
5. `docs/security-model.md`
6. `docs/runtime-architecture.md`（M7–M12 目标架构）
7. `docs/adr/` 中相关 ADR

## 不可破坏的不变量（universal）

- Worker 不能为自己的工作提供权威验证证据。
- 每个写任务都必须使用锁定基线和独立 Git worktree。
- 每个仓库的本地 Run、Log、Cache 与任务 worktree 默认位于被 Git 忽略的 `.marshal/`，不得进入业务提交。
- 一个任务 worktree 同时最多有一个写入者。
- Worker 与 Publisher 权限必须分离。
- ReviewDecision 必须绑定到精确的证据摘要。
- 失败或阻塞任务必须保存 Outcome 证据，不得创建虚假 PR。
- 普通宿主机子进程不得被描述成恶意代码沙箱。
- Merge 默认禁用，不属于 MVP 生命周期。

## 文档修改要求（universal）

- 所有面向人的 Markdown 文档统一使用中文；协议字段、状态名、CLI 命令和代码标识保留英文。
- 所有文档与 Schema 中的术语和状态名必须一致。
- 打开或关闭重大架构问题时更新 `docs/audit-report.md`。
- 改变信任边界、持久化契约、生命周期或发布权限时，必须新增或替代 ADR。
- 修改 Schema 后必须验证 JSON 语法、Draft 2020-12 metaschema、示例和 `git diff --check`。
- 不得为了简化 Adapter 而静默放宽强制门禁。

## 实施门禁（universal）

维护者已明确接受设计。实施期间必须：

1. 按 `docs/implementation-plan.md` 顺序实施；
2. 在真实 Agent 集成前先完成 Fake Adapter 与确定性核心；
3. 状态转换、路径边界、陈旧证据、崩溃恢复和发布幂等性测试通过前，不得宣称 MVP 可用。

## 维护者工作流（maintainer-only）

维护者必须**同时**满足以下三个条件，任一条件不满足则按外部贡献者护栏执行：

1. 账号列于 [.github/MAINTAINERS](.github/MAINTAINERS) 维护者清单；
2. 当前仓库的 remote 指向 canonical 仓库 `https://github.com/chiga0/marshal-harness.git`，而非 fork；
3. 该账号在 canonical 仓库拥有写权限。

通过验证的维护者可以：

- 在 canonical 仓库直接创建分支、推送变更并运行 Marshal 任务；
- 评审 PR、做出 ReviewDecision，并守护本文档与 `docs/` 中的不变量和门禁。

维护者身份不豁免任何 universal 规则：改变信任边界、持久化契约、生命周期或发布权限仍必须新增或替代 ADR。

## 外部贡献者护栏（external-contributor）

- **fork 工作**：从 canonical 仓库 fork，在自己的 fork 中创建分支，一律通过 PR 回流 canonical 仓库；
- **PR 必须 CI 绿**：CI（Linux + macOS + secret scan）全绿后才进入评审，见 [CONTRIBUTING.md](CONTRIBUTING.md)；
- **不触碰信任边界目录**：`.github/CODEOWNERS` 覆盖区（`internal/lifecycle/`、`internal/publication/`、`internal/publisher/`、`internal/adapter/`、`schemas/`、`docs/adr/`）内的变更需要维护者显式批准；
- **文档中文规则**：面向人的 Markdown 文档统一使用中文，协议字段、状态名、CLI 命令和代码标识保留英文；
- **ADR 触发条件**：改变信任边界、持久化契约、生命周期或发布权限时，必须先新增或替代 ADR。
