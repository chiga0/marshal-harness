# Marshal 仓库协作指南

本仓库采用分层治理，协作规则分为三层：

- **universal**：适用于所有参与者（维护者、外部贡献者与 AI Agent），任何情况下不得豁免；
- **maintainer-only**：仅适用于通过验证的维护者，授予 canonical 仓库的直接操作与评审职责；
- **external-contributor**：外部贡献者护栏；不满足维护者验证条件时一律按此层执行。

除明确标注层级的章节外，本文档其余章节均为 universal 规则。

## 2026-09-07 当前产品路线

维护者已接受 [ADR 0080](docs/adr/0080-three-plane-business-delivery-roadmap.md) 的三面分离与 B1→B2→B3 路线。当前目标进一步按 B1 真实团队交付 PoC→B2 本地 API/SQLite/交互与审计→`API-STABLE`→B3 长期运行与正式 API 支持推进；UI-1 仅在 API-STABLE 后启动，不阻塞 API 正式发布。它不恢复通用 M13、HA、多租户作为发布前置。唯一当前完成状态见 [Roadmap 当前表](docs/roadmap-status.md#业务交付当前表)，目标退出条件见 [实施 Milestone](docs/agent-team-service-milestones.md)。独立验证、单写者、受控发布与恢复继续保留；软件签名按发行资产类别适用，Linux/stable 门禁仍属 B3，不表示能力已生产可用。

2026-09-09 用户明确收敛为可信单用户角色团队，设计接受 [ADR0094](docs/adr/0094-trusted-single-user-role-team.md)及[受管 Leader 执行机制](docs/node-leader-execution-design.md)：Leader 贯穿需求/回答、批次结果/求助、集中 Review、授权交付及后验，读 durable 上下文、输出有限行动，不以现有 Planner 冒充。Supervisor 仅观察/聚合/通知，Core 授权/预算/已批准调度及硬规则，Execution 操作所属 handle；后继 v7 Core 已按 ADR0095 集成到主线，受控完整链通过，真实模型与冷故障恢复仍待完成，不造四服务/第二状态机。`trusted-single-user` 的职责分离不等于 OS/凭据隔离，强隔离后置；历史 Go 线 2026-09-10 起随 ADR0099 退役移除，涉及历史 API/格式的表述一律以 git 历史为准，不再要求 main 内并存保护。B1 经独立七条件核验为本机 PoC PASSED，非本次改目标自动通过；B2-L 为 IN_PROGRESS（Core 已集成、正式发布前必验），B2/B3 仍 IN_PROGRESS，API-STABLE 原范围保留。业务发布不是 Marshal 软件发行，不豁免 B3；不增加 Leader/Workspace/Skill 或 Workflow 管理平台。

当前目标设计统一见 [Task-first Agent Team 服务架构](docs/agent-team-service-architecture.md)、[实施 Milestone](docs/agent-team-service-milestones.md)与 [ADR 0085](docs/adr/0085-agent-team-service-contract-and-storage.md)：删除 Workspace/Project 业务对象与注册流程，仓库/表/平台通过 Task prompt/context 提供。Node 按 ADR0088 使用自己的唯一 Application/SQLite。B1 用一个 Provider 的两个实例先交付；B2 保留本地简启动、零 Git/多仓库、问答与更多 Adapter，按0094补完整受管 Leader 与授权交付/后验。账号/安装身份平台和 U1 历史导入不作为 B1 前置。合同状态与运行时启用分别判断；[合同适用性](docs/design-contract-map.md)承载历史合同与新设计的退位边界，新设计不自动迁移历史资产。以下分权条款仅按0094明确作用域，其余不变量保留；旧 Workspace 状态布局按旧提案作用域理解，不成为新的注册前置。

旧 Marshal skill 长期完全退出产品运行依赖、研发准入和验收标准：不读取、加载、派发或执行其流程，不要求每个开发切片一个 Marshal Run。保留历史运行/失败/审计资产，不恢复旧 Skill 的微切片和轮次规范；各 Agent 自带 Skill 仍由 Agent 自行管理。这不豁免以下产品证据、权限和恢复不变量。

## 并行研发调度（2026-09-08 用户明确要求）

- 每次子 Agent 完成、退出、失败或单项任务完成后，主 Agent 立即核对真实运行状态、可用槽位、依赖、审查队列及写入 scope；不等用户提醒或定时心跳再检查。
- 有空槽且存在依赖满足、互不冲突的高价值工作时，当次补派开发、独立审查、集成测试或紧邻主线的调研/设计。已结束 Agent 必须用能触发新执行的续派调用，单纯消息不等于重新启动；派发后核实实际状态。
- 并发上限以当前工具实际容量为准（本次为主 Agent 加三个子 Agent），不得把历史已完成 Agent 算作活跃，也不得为了占满槽位制造低价值任务。不能补派时记录具体依赖/冲突/容量理由。
- 调度检查是轻量的执行动作，不新增审批、Marshal skill、微切片或独立治理平台；保留单写者、独立验证与权限边界。

## 当前阶段（历史基线，当前排期以上节为准）

Go 历史线（I186/RC1/M0–M13、ADR0001–0079 的实现载体）已于 2026-09-10 按 [ADR 0099](docs/adr/0099-go-legacy-line-retirement-and-removal.md) 退役并从 main 移除：历史接受事实、状态证据与 `v1.0.0-rc1`（tag `e99326f` → `c1407bd`）完整保留在 git 历史，本节不再承载排期。当前权威状态只看 [Roadmap 当前表](docs/roadmap-status.md#业务交付当前表)。后续变更仍须按门禁流程：信任边界/持久化契约/生命周期或发布权限的改变必须新增或替代 ADR。

（压缩保留）2026-09-01 的 `v1.0.0-rc1`（unsigned Darwin arm64 CLI-only local-dogfood prerelease）是该历史线的最后一个 prerelease：tag `e99326f` 精确指向 `c1407bd`，candidate SHA-256 为 `f9ed7fa59d05f5e71fef7164b8015240497e1d18e25ef1d3f8e199c1378a3774`；只关闭 local-dogfood prerelease distribution exit，不构成 production/RELEASED。

## 修改设计前必读

按顺序阅读：

1. `README.md`
2. `docs/vision-and-scope.md`
3. `docs/design-contract-map.md`（先区分目标、合同状态、旧 profile 与实际成熟度）
4. `docs/agent-team-service-architecture.md`（Task-first 与 API-first 目标）；`packages/task-api/openapi.json` 与 `docs/node-api-contract.md`（HTTP 合同唯一权威）
5. `docs/agent-team-service-milestones.md`、`docs/roadmap-status.md` 当前表（目标出口与实机完成状态分开）
6. `docs/task-lifecycle.md`、`docs/security-model.md`
7. [ADR 0099](docs/adr/0099-go-legacy-line-retirement-and-removal.md)（Go 历史线已于 2026-09-10 退役移除；旧 `docs/architecture.md`、`docs/implementation-plan.md`、`docs/runtime-architecture.md` 为历史资料）与命中接缝的原 ADR；`*-reference-*` 只用于历史追溯，不形成第二套强制排期

## 不可破坏的不变量（universal）

- Worker 不能为自己的工作提供权威验证证据。
- 本仓库的每个开发写任务必须使用锁定基线和独立 Git worktree；产品中的 Git 写节点同样锁定 base 并使用独立 worktree。该要求不扩展为非 Git 产品 Task 必须初始化仓库或提供 commit。
- 本仓库的本地研发运行态（任务 worktree、日志、缓存）默认位于被 Git 忽略的 `.marshal/`；运行数据均不得进入业务提交。Node 服务运行数据默认在 `$HOME/.marshal-node/`（产品侧），与仓库 `.marshal/` 互不接管。
- 每个任务 worktree 或非 Git 独立执行目录同时最多有一个写入者；归属或停止状态未知时不得复用目录。
- Worker 与 Publisher 权限必须分离：Node `trusted-single-user` 按 ADR0094 保留职责、命令准入与证据权威分离，不宣称 OS 账号/凭据强隔离。默认无业务发布授权，角色名和原生登录不能扩权。
- ReviewDecision 必须绑定到精确的证据摘要。
- 失败或阻塞任务必须保存 Outcome 证据，不得创建虚假 PR。
- 普通宿主机子进程不得被描述成恶意代码沙箱。
- 产品自动 merge 默认禁用，不属于 MVP 生命周期；此处不禁止维护者的研发 Git 合并。按用户 2026-09-08 的常驻授权，当前 v1 开发阶段，维护者研发变更经独立 review 无阻塞问题、相关本地验证通过后，可直接合入 main 并正常推送远端，无须逐次确认或强制 PR。v1 正式发布后，后续研发变更必须恢复 PR 流程。开发阶段仍保留锁定基线、单写者、独立验证、权限边界及远端历史保护，不授予产品 Worker 或 Publisher 自动合并权限。

## 文档修改要求（universal）

- 所有面向人的 Markdown 文档统一使用中文；协议字段、状态名、CLI 命令和代码标识保留英文。
- 所有文档与 Schema 中的术语和状态名必须一致。
- 打开或关闭重大架构问题时更新 `docs/audit-report.md`。
- 改变信任边界、持久化契约、生命周期或发布权限时，必须新增或替代 ADR。
- 修改合同 `packages/task-api/openapi.json` 后必须验证 JSON 语法、示例和 `git diff --check`。
- 不得为了简化 Adapter 而静默放宽强制门禁。

## 实施门禁（universal）

维护者已明确接受设计。实施期间必须：

1. 按 `docs/agent-team-service-milestones.md` 顺序实施；
2. 在真实模型接入前先完成确定性核心与受控 fixture；
3. 状态转换、崩溃恢复、幂等性与同包安装的测试通过前，不得宣称对应能力可用。

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
- **不触碰信任边界目录**：`.github/CODEOWNERS` 覆盖区（`docs/adr/`、`packages/task-api/`）内的变更需要维护者显式批准；
- **文档中文规则**：面向人的 Markdown 文档统一使用中文，协议字段、状态名、CLI 命令和代码标识保留英文；
- **ADR 触发条件**：改变信任边界、持久化契约、生命周期或发布权限时，必须先新增或替代 ADR。
