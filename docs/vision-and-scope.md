# 愿景与范围

## 当前产品投影（2026-09-07）

[ADR 0080](adr/0080-three-plane-business-delivery-roadmap.md) 将受限 Agent Team 前移：用户意图→澄清/确认→有界任务→集成候选→独立验证→授权交付，按 B1→B2→B3 验收。下文旧排期中“Goal DAG 延期”仍适用于通用/复杂编排，不再排除这个受限 profile。控制面、执行面、存储面分离不意味着每个模块独立部署。能力现状只见 [Roadmap](roadmap-status.md#业务交付当前表)。

[服务产品方案](agent-team-service-architecture.md)与 [Milestone](agent-team-service-milestones.md)明确 Task-first：用户只需 Task、Worker、Artifact；没有 Workspace/Project 实体或资源注册。data-dir 只是服务内部配置，仓库/表/平台通过 prompt/context 提供。目标一个 marshal serve 启动本地 HTTP，自动初始数据与访问保护；账号/安装身份平台/统一 Agent 登录后置。Core 的内置监督、独立验收与执行归属保留。

API-first 顺序为 B1 真实团队 PoC→B2 本地 API 可用→B3 正式可靠发布。B1 复用现有合法安装/Store，一个 Provider 两实例先交付；SQLite、零 Git/多仓库、问答和更多 Adapter 在 B2，旧库导入 U1 单列。核心 API-STABLE 后才 UI-1，UI 不阻 API 发布。旧 Marshal skill 不读取/加载/执行，各 Agent 原生 Skill 自管。ADR 0085 仍为 Proposed，边界启用和实机完成分别计证。

## 愿景

Marshal 是面向 Agent Team 真实业务交付的长寿命、可自托管、确定性 Control Plane。它持续接收用户 Task 与上下文，内部复用 Goal/计划和 WorkItem 执行模型，把需求接纳为有界的 typed workload，调度可替换的 Agent 与 Sandbox Provider，并让环境、状态、Evidence 与 SideEffect 在进程或 Provider 故障后仍可恢复、可审计、可验证。软件工程是首批业务之一，而不是所有 Task 必须有 Git 仓库的理由。

Marshal 让 Agent 工作成为受控工程执行，而不是无结构的终端对话。LLM 可以规划、实现和评估，但不能成为第二业务权威；只有确定性 Core 能接纳输入、推进生命周期并授权副作用。

当 Runtime 可以长期稳定接收新任务，且更换 Agent、Sandbox 或 durable backend 不会改变任务含义、验收标准和发布所需证据时，Marshal 才算实现目标。

长期目标已由 [ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md)（2026-08-10 接受）正式重置：从“本地单次 CLI 编排”升级为**长寿命 Runtime/Control Plane 持续接收、耐久排队、分发和审计大量有界 Task/Run/Attempt；环境与状态可重建、可恢复、可审计**。执行沙箱可插拔，Cloudflare Sandbox 只是一个可替换远程 Provider。[ADR 0019](adr/0019-deterministic-control-plane-typed-execution-and-goal-admission.md) 进一步冻结：Supervisor 是确定性 Core，不是 LLM；LLM 只执行 typed semantic workload；Goal plan 必须先 proposal、后由 Core 确定性接纳。[ADR 0052](adr/0052-v1-release-scope-and-production-reachability.md) 当时把首个正式版本收敛为单节点、单用户、可信仓库纵切，并将 Cloudflare、HA、多用户与 Goal DAG 延期到 1.x；这是历史收敛记录，当前 Task-first/受限团队/API-first 目标以上节与 ADR 0085 为准，不恢复通用复杂 DAG。

## 竞争定位与差异化

2026 年的行业形态：编码 Agent 已进入 best-of-breed 竞争，开发者忠于明显更好的独立工具，而非绑定最深的平台；入口与执行环境正在商品化，Agent 的多面存在、快速供给的远程沙箱与异步委托都已成为独立品类；厂商级产品正在收敛到“控制面 + 云端执行”架构。当模型、入口与执行环境都商品化后，可防守的资产集中在控制面：治理、审批、审计、身份与可验证证据。

Marshal 在这张地图上的差异化不是“更好的 harness”或“更全的入口”，而是三条结构性差异：

1. **开源自托管与数据不出域**：Marshal 可完整运行在自有基础设施上，事件账本、Evidence、凭据与审计不依赖任何外部托管控制面；这是企业采用的硬条件，也是托管产品无法让渡的性质；
2. **可验证且高效的交付**：Worker 不自证、独立验证、精确证据和权限边界用于降低错误交付与恢复成本；治理深度不是独立的产品产出，不能替代业务完成率、人工介入和交付耗时。尚未有数据的竞品优劣不作事实声明；
3. **Provider 中立**：核心生命周期不根据 Provider 名称分叉，更换 Agent、Sandbox 或 durable backend 不改变任务含义与验收标准；当最好的 Agent 快速换代时，中立控制面是跨周期资产。

因此 Marshal 不与通用编码 Agent 竞争编码体验，也不把入口当护城河：CLI、Web、IM bot、webhook、定时触发器等入口都是可替换的 Public API client（定位声明，非实现承诺），执行环境是可插拔 Provider；竞争发生在“治理深度 × Provider 中立 × 自托管”这一层。垂直领域的 Agent 平台可以作为 Public API client 构建在 Marshal 之上，也可以独立存在——Marshal 服务的是跨领域的治理底座本身。

## 问题定义

Agent 驱动的业务交付系统面临两类相互关联的问题。

第一类是执行差异：

1. 调用方式和会话协议不同。
2. 权限控制不同，而且可能依赖交互确认。
3. 输出事件和错误语义不同。
4. Agent 可能错误总结自己的变更或测试结果。
5. 长任务可能留下部分状态，临时脚本难以可靠检查或恢复。

第二类是 Runtime 问题：Control Plane 必须长期接收新任务，在 Agent、Sandbox、进程或机器故障后恢复，避免重复副作用和陈旧写入，并在复杂 Goal 的多次计划与执行之间维持累计预算、Evidence 适用性和完整审计链。

Marshal 必须统一这些问题，同时不能假装所有 Provider 具有相同能力，也不能让调度 backend 或 LLM 成为第二业务权威。

## 目标

### G1：契约优先的委派

公开 Task 从需求与上下文开始，经确认冻结带版本的计划、输入摘要、验收标准、必需交付物、预算和执行/发布策略。内部 Goal 保持唯一权威；WorkItem 的冻结执行规格复用原 TaskSpec，不要求用户手写它。Git base 只属于 Git 执行路径，非 Git 任务以输入和制品摘要锁定可检查的起点。

### G2：Provider 无关的 Worker

Provider 特有逻辑仅存在于 Adapter 内部，核心生命周期不得根据 Provider 名称分叉。

### G3：证据优先于声明

Marshal 独立观察真实制品与执行结果、运行验收、计算交付物摘要并记录来源；Git 任务额外核对 base、diff 与 worktree。Worker 摘要可以作为上下文，但不能独立满足门禁。生成 SQL 文件不等于 SQL 已发布或补数已完成。

### G4：职责与权威分离

Implement 产出 Candidate，Verify 产出 Evidence，Review 产出 Assessment，Publication 产出 Receipt；Marshal Core 校验并物化权威事实。发布凭据与 merge 权限位于 Worker 信任边界之外，任何执行者都不能凭自己的“完成”声明越过 Core gate。

### G5：可恢复执行

进程崩溃或机器重启后，操作者可以确定最后一个持久状态，检查执行归属、独立目录与外部效果，并明确选择恢复、重试、拒绝或中止，而不是猜测。停止 Agent 不等于远端业务作业已停止，未知效果不可盲重试。

### G6：可审计结果

每个终态 Run 都有 Outcome Bundle，包含冻结输入/执行规格、标准化事件、VerificationReport、ReviewDecision 和 ArtifactManifest；Git 类型另含真实 diff。公开 Task 聚合所有 WorkItem/Run/Attempt 的耗时、用量来源、返工和最终业务验收，缺失计量不冒充零。

### G7：耐久 Runtime 与可插拔沙箱

Runtime 长期稳定运行，持续接受新 Task 并分发；Sandbox、Agent 与 Runtime 进程可丢弃，权威事件、证据与副作用记录在其外部耐久保存；执行环境通过统一 SandboxProvider 契约接入，替换 Provider 不改变任务含义与验收标准。该目标的分层、恢复/fencing/checkpoint 语义与实施路线由 [ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md) 冻结。

### G8：确定性控制与有界复杂任务

Marshal Core 是唯一 Supervisor 与权威状态机；Plan/Implement/Verify/Review/Publish 作为 typed execution 共享基础调度机制，但不共享权限或通用协议。复杂 Goal 的计划、重规划、预算预留、证据适用性和人工暂停/恢复必须可回放且有界，Planner 不能直接创建权威 Run 或执行副作用。

## v1.0 发布范围

正式目标是单用户、单节点、可信业务任务的 Agent Team HTTP 服务，不交付终态全部平台能力。没有 Workspace ID/注册；公开 Task 复用 Goal，用户通过任务上下文提供业务信息。默认数据目录和本地 token 自动建立，损坏/不兼容旧数据不覆盖；不建设账号/组织/安装收据/统一 Agent 登录平台。

- B1 先真实团队 PoC：现有合法安装与受控 Store、一个真实 Provider 两个实例、独立目录/Git worktree、一次计划确认、并行实现、集成和独立消费验收。
- B2 完成本地 API 体验：简短需求澄清/问答、DAG/Worker 进展、暂停取消、SQLite 单写真值、同版本恢复、零 Git 制品与多仓库上下文、审计及第二真实 Adapter；第三 Provider 和增强能力按单独支持项推进。
- B3 才以同路径业务/故障/长期运行、备份恢复、Darwin 签名/notarization、Linux 实机、最终 same-bytes release gate 证明正式支持。
- Pi、Qwen Code、OpenCode 是首批适配目标；一个尚未通过者不能冒充支持，也不阻止已验证 profile 的交付。Agent 自管模型登录/Skill，Core 只消费中立接口/能力，不限定精确品牌版本。
- 默认只交付可使用成果，外部 SQL 发布/执行/补数或 Draft PR 以后按独立授权与实测能力开放；不自动 merge。生成文件不等于用户要求的生产效果。
- 核心 API-STABLE 后才开发 UI，不以三品牌全部增强或 U1 历史迁移阻挡接口稳定；UI 不阻 API release。

保留批准范围、有限预算、状态持久、单写目录、重复请求幂等、当前结果接纳、独立验证和 Publisher 分权。B1 不确定恢复可需介入，但不能宣传透明自动恢复；完整保证分 B2/B3 实测。普通宿主进程永不冒充恶意代码隔离。

HA、多租户/远端身份平台、通用工作流编辑器、动态任意 DAG、PostgreSQL、统一 Skill/资源治理、跨系统原子发布不在首版前置。U1 旧历史导入独立验证，不双写或重签，未过就只声明已验证的新格式/干净安装支持。

本节是目标，实际成熟度只见 [Roadmap](roadmap-status.md#业务交付当前表)；边界依据 [ADR 0085](adr/0085-agent-team-service-contract-and-storage.md)（Proposed）及其对应验收启用，原 profile 不自动变更。文档/Fake/组件测试不等于 INTEGRATED，版本标签不等于 RELEASED。

## 当前交付基线：Local MVP

Local MVP 是终态 Control Plane 的先行实现与回归基线，不是产品范围定义。它先在 embedded/local 拓扑证明 Coding Task 的生命周期、Evidence、权限和发布不变量。

MVP 包含：

- macOS 与 Linux 本地执行；
- 需要发布时具有 remote 的本地 Git 仓库；
- 通过结构化文件和 CLI 命令对接主 Agent，同时支持 Codex CLI 与 Codex Desktop 作为交互界面；
- Qwen Code、OpenCode、Pi 的 one-shot Adapter；
- worktree 创建与清理；
- 文件范围、dirty tree、交付物和验收命令门禁；
- Review 与有上限的 Rework；
- 通过独立 Publisher 创建 GitHub Draft PR；
- 追加式 JSONL 事件日志与原子状态快照；
- 中断后的检查与显式恢复命令。

## 当前交付基线的边界

以下既包含终态不变量（例如不信任 Worker 自证、默认不自动 merge），也包含由后续 Milestone 交付的能力。它们都不能从 Local MVP 的 `USABLE` 状态中推断出来：

- 把 Worker 输出当作可信证据。
- 让多个写 Worker 共享同一 worktree。
- 默认自动 merge。
- 第一条纵向链路支持 GitLab 发布。
- 托管式多租户控制平面。
- 对恶意仓库或恶意构建脚本提供强隔离承诺。
- 在没有评测数据时自动选择“最佳”Worker。
- Web UI、远程队列、分布式 Worker 或集群调度（不属于当前交付基线；耐久排队与分发已纳入 M7–M12 路线，见[实施计划](implementation-plan.md)）。
- 取代仓库 CI 的最终集成信号。
- 在没有 Adapter 契约时支持任意交互式 Agent。

## 用户

### 主要用户

- 自托管常驻 Runtime、通过 HTTP Task 提交代码、SQL、文档等业务 Task 的个人与团队；
- 需要在可替换 Agent/Sandbox 上执行，同时保留证据、权限边界和恢复能力的平台开发者与维护者。

### 次要用户

- 当前通过 Codex CLI、Codex Desktop 或手机端 Remote 监督本地任务的工程师；
- 在隔离 CI Runner 上执行相同流程的团队；
- 为其他 CLI 或协议编写 Adapter 的开发者；
- 审查某次 Run 为什么被接受或拒绝的 Reviewer。

## 决策权

| 决策 | 负责人 | 必需证据 |
| --- | --- | --- |
| 任务目标与范围 | 用户确认，Planner 提案，Core 接纳 | Task 需求/上下文与冻结计划/验收 |
| Worker 选择 | Core Supervisor，依据允许的配置与建议 | CapabilitySnapshot、执行 profile 与策略 |
| 业务成果实现 | Worker | 实际制品与执行记录；Git 类型另含 worktree diff |
| 验证通过或失败 | Harness | 真实命令与交付物结果 |
| 语义评估提案 | 主 Agent / Review Executor | Candidate、Diff 与 VerificationReport |
| 物化 ReviewDecision | Marshal Core | 当前 Evidence、Assessment、Policy 与 sequence 校验 |
| 发布 PR/MR | Harness Publisher（执行）/ Marshal Core（授权与接纳） | ReviewDecision、Evidence、SideEffectIntent/Receipt 与发布策略 |
| Merge | 仓库策略 / 维护者 | Accept 决策、CI 和所需审批 |

## 信任边界

首版面向单用户可信任务和已配置可信 Worker。data-dir 不是资源所有权或业务授权；Task prompt/context 中的仓库路径、表名或 URL 不触发 HTTP 自动读取、执行或扩权。独立目录、Git worktree、环境/工具权限、输入绑定与明确批准降低误操作，但不构成同 UID 敌对隔离。作者仍不得得到 Publisher 权限/凭据，原生登录不豁免该边界。

不可信仓库、不可信依赖或多用户执行必须使用容器、VM 或同等可强制执行的沙箱，才能成为受支持的安全配置。

## 成功指标

首先统计每个真实用户需求的交付时间、人工介入、等待、retry/rework/replan 原因、最终业务验收与 token 可用性；再分任务族对比单 Agent 与团队基线。失败必须进入分母，缺失 token 不记为零，PR 数量不能代表目标进度。以下不变量继续作为必要条件，而不是充分的产品成功指标：

- 100% 的 Run 具有冻结输入/执行起点和 Outcome Bundle；只有 Git 路径要求 commit 基线。
- 100% 的 Accepted Run 具有通过的独立 VerificationReport。
- 强制安全配置下，0 个 Worker 进程获得 Publisher 凭据。
- 0 个 Accepted Run 包含越界路径。
- 无需阅读原始终端记录即可判断中断 Run 的状态。
- 相同任务身份的重复发布具有幂等性。

积累足够数据后，可按 Adapter 和任务类型统计首轮通过率、返工次数、验证失败率、成本、耗时、对账与补偿率。补偿不是回滚：历史副作用与补偿结果都必须保留。

## 当前交付顺序

B1 先交付真实团队 PoC，单任务是内部步骤，不先建 Workspace/身份平台或全面迁库；B2 补本地 API、SQLite、零 Git/多仓库、问答/审计与恢复；B3 正式可靠发布。API-STABLE 后 UI 可并行但不阻 API release，U1 历史导入独立。详细出口只见[Milestone](agent-team-service-milestones.md)，完成只见[Roadmap](roadmap-status.md#业务交付当前表)。旧 Marshal skill 不作为任何阶段条件。

## 历史交付阶段（保留当时路线，不作为当前排期）

### 阶段 1：纵向链路

实现一个 Adapter、worktree 隔离、冻结输入、独立验证、主 Agent 决策导入、Outcome Bundle 和 GitHub Draft PR。

### 阶段 2：Worker 对齐

补齐其他 Adapter、安全的会话恢复、能力探测、恢复机制和 Adapter 一致性测试。

### 阶段 3：加固与路由

增加可强制执行的容器配置、CI 回调、GitLab Publisher、评测数据、策略路由、遥测和可选服务接口。

### 阶段 4：v1.0 生产纵切（I186-R0→R6）

由 ADR 0052 冻结：先接通最薄 Agent-in-Sandbox walking skeleton，再收敛 command/result authority、双 Provider binding、单一恢复模型与 strangler cutover，最后通过 conformance、跨平台打包和 release gate。M0–M9 历史资产继续复用，但不得把 `COMPONENT` 误报为 `INTEGRATED`。

### 阶段 5：1.x 平台扩展

在 v1.0 发布后，再按真实使用证据重排 Cloudflare 远程 Provider、生产存储/HA、多用户策略、SDK/生态协议与复杂 Goal orchestration。M10–M13 保留为候选池，不再按旧编号顺序阻塞 v1.0。
