# 愿景与范围

## 愿景

Marshal 是面向 Agent 驱动软件工程的长寿命、可自托管、确定性 Control Plane。它持续接收 Goal 与 Task，把复杂需求接纳为有界的 typed workload，调度可替换的 Agent 与 Sandbox Provider，并让环境、状态、Evidence 与 SideEffect 在进程或 Provider 故障后仍可恢复、可审计、可验证。

Marshal 让 Agent 工作成为受控工程执行，而不是无结构的终端对话。LLM 可以规划、实现和评估，但不能成为第二业务权威；只有确定性 Core 能接纳输入、推进生命周期并授权副作用。

当 Runtime 可以长期稳定接收新任务，且更换 Agent、Sandbox 或 durable backend 不会改变任务含义、验收标准和发布所需证据时，Marshal 才算实现目标。

长期目标已由 [ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md)（2026-08-10 接受）正式重置：从“本地单次 CLI 编排”升级为**长寿命 Runtime/Control Plane 持续接收、耐久排队、分发和审计大量有界 Task/Run/Attempt；环境与状态可重建、可恢复、可审计**。执行沙箱可插拔，Cloudflare Sandbox 只是一个可替换远程 Provider。[ADR 0019](adr/0019-deterministic-control-plane-typed-execution-and-goal-admission.md) 进一步冻结：Supervisor 是确定性 Core，不是 LLM；LLM 只执行 typed semantic workload；Goal plan 必须先 proposal、后由 Core 确定性接纳。[ADR 0052](adr/0052-v1-release-scope-and-production-reachability.md) 不改变该终态，只把首个正式版本收敛为一条单节点、单用户、可信仓库的真实生产纵切；Cloudflare、HA、多用户与 Goal DAG 延期到 1.x。

## 竞争定位与差异化

2026 年的行业形态：编码 Agent 已进入 best-of-breed 竞争，开发者忠于明显更好的独立工具，而非绑定最深的平台；入口与执行环境正在商品化，Agent 的多面存在、快速供给的远程沙箱与异步委托都已成为独立品类；厂商级产品正在收敛到“控制面 + 云端执行”架构。当模型、入口与执行环境都商品化后，可防守的资产集中在控制面：治理、审批、审计、身份与可验证证据。

Marshal 在这张地图上的差异化不是“更好的 harness”或“更全的入口”，而是三条结构性差异：

1. **开源自托管与数据不出域**：Marshal 可完整运行在自有基础设施上，事件账本、Evidence、凭据与审计不依赖任何外部托管控制面；这是企业采用的硬条件，也是托管产品无法让渡的性质；
2. **证据法学级的治理深度**：Worker 不自证、独立验证、ReviewDecision 绑定精确证据摘要、ConformanceEvidence 的敌对拓扑签发、跨信任域默认拒绝（仅三条 Core-only typed cross-domain edge 例外）——托管控制面通常受自家 Agent 体验约束，难以选择这个严格度；对 Marshal 而言这正是产品本体；
3. **Provider 中立**：核心生命周期不根据 Provider 名称分叉，更换 Agent、Sandbox 或 durable backend 不改变任务含义与验收标准；当最好的 Agent 快速换代时，中立控制面是跨周期资产。

因此 Marshal 不与通用编码 Agent 竞争编码体验，也不把入口当护城河：CLI、Web、IM bot、webhook、定时触发器等入口都是可替换的 Public API client（定位声明，非实现承诺），执行环境是可插拔 Provider；竞争发生在“治理深度 × Provider 中立 × 自托管”这一层。垂直领域的 Agent 平台可以作为 Public API client 构建在 Marshal 之上，也可以独立存在——Marshal 服务的是跨领域的治理底座本身。

## 问题定义

Agent 驱动的软件工程系统面临两类相互关联的问题。

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

每个任务都有不可变、带版本的 TaskSpec，包含目标、范围、验收标准、必需交付物、预算、Worker 策略和发布策略。

### G2：Provider 无关的 Worker

Provider 特有逻辑仅存在于 Adapter 内部，核心生命周期不得根据 Provider 名称分叉。

### G3：证据优先于声明

Marshal 独立观察 Git 状态、执行验证命令、计算交付物摘要并记录退出码。Worker 摘要可以作为上下文，但不能独立满足门禁。

### G4：职责与权威分离

Implement 产出 Candidate，Verify 产出 Evidence，Review 产出 Assessment，Publication 产出 Receipt；Marshal Core 校验并物化权威事实。发布凭据与 merge 权限位于 Worker 信任边界之外，任何执行者都不能凭自己的“完成”声明越过 Core gate。

### G5：可恢复执行

进程崩溃或机器重启后，操作者可以确定最后一个持久状态，检查 worktree，并明确选择恢复、重试、拒绝或中止，而不是猜测。

### G6：可审计结果

每个终态 Run 都有 Outcome Bundle，包含冻结的 TaskSpec、标准化事件、真实 diff、VerificationReport、ReviewDecision 和 ArtifactManifest。

### G7：耐久 Runtime 与可插拔沙箱

Runtime 长期稳定运行，持续接受新 Task 并分发；Sandbox、Agent 与 Runtime 进程可丢弃，权威事件、证据与副作用记录在其外部耐久保存；执行环境通过统一 SandboxProvider 契约接入，替换 Provider 不改变任务含义与验收标准。该目标的分层、恢复/fencing/checkpoint 语义与实施路线由 [ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md) 冻结。

### G8：确定性控制与有界复杂任务

Marshal Core 是唯一 Supervisor 与权威状态机；Plan/Implement/Verify/Review/Publish 作为 typed execution 共享基础调度机制，但不共享权限或通用协议。复杂 Goal 的计划、重规划、预算预留、证据适用性和人工暂停/恢复必须可回放且有界，Planner 不能直接创建权威 Run 或执行副作用。

## v1.0 发布范围

v1.0 用最小但完整的生产纵切证明上述方向可用，而不是交付终态的全部横向能力。它支持单节点、单用户和可信仓库，并要求：

- 至少一个真实 AgentProvider 与一个真实 Local/Container SandboxProvider；
- Agent 进程实际运行在 allocation 内，真实结果只经 ResultIngress 接纳；
- CLI 或 loopback `marshal-server`、durable Run journal、WorkerExecutor、Sandbox、AgentRuntime、ResultIngress、独立 Verification 与 Outcome 组成唯一真实调用链；
- 重启恢复、幂等接纳、generation fencing、Agent/Sandbox 双 binding、cancel/timeout/retry/terminal 均在该链路上生效；
- 发布仅为 `publication:none` 或可选 GitHub Draft PR，默认不 merge；
- macOS/Linux 具有稳定发布产物，macOS 正式包通过签名与 notarization。

v1.0 不承诺多节点 HA、多用户/多租户、Cloudflare 完整生产拓扑、全部 Provider hardened 矩阵、Web UI、远程 SDK 全矩阵或复杂 Goal DAG。这些能力属于 1.x。Local ordinary-user 可以是受支持的 trusted profile，但不能宣称 `hardened` 或恶意代码隔离。

能力只有在真实 composition root 可达且真实 Agent/result bytes 穿过时才算 `INTEGRATED`；只有 release gate 通过才算 `RELEASED`。单独的 ADR、Schema、package 或 component test 不能满足 v1.0。

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

- 自托管常驻 Runtime、持续提交软件工程 Goal/Task 的个人与团队；
- 需要在可替换 Agent/Sandbox 上执行，同时保留证据、权限边界和恢复能力的平台开发者与维护者。

### 次要用户

- 当前通过 Codex CLI、Codex Desktop 或手机端 Remote 监督本地任务的工程师；
- 在隔离 CI Runner 上执行相同流程的团队；
- 为其他 CLI 或协议编写 Adapter 的开发者；
- 审查某次 Run 为什么被接受或拒绝的 Reviewer。

## 决策权

| 决策 | 负责人 | 必需证据 |
| --- | --- | --- |
| 任务目标与范围 | 主 Agent / 维护者 | TaskSpec |
| Worker 选择 | 主 Agent或配置化 Router | CapabilitySnapshot 与策略 |
| 代码实现 | Worker | Worktree diff |
| 验证通过或失败 | Harness | 真实命令与交付物结果 |
| 语义评估提案 | 主 Agent / Review Executor | Candidate、Diff 与 VerificationReport |
| 物化 ReviewDecision | Marshal Core | 当前 Evidence、Assessment、Policy 与 sequence 校验 |
| 发布 PR/MR | Harness Publisher（执行）/ Marshal Core（授权与接纳） | ReviewDecision、Evidence、SideEffectIntent/Receipt 与发布策略 |
| Merge | 仓库策略 / 维护者 | Accept 决策、CI 和所需审批 |

## 信任边界

首版面向开发者自有仓库和本地可信 Worker 二进制。它通过 worktree 隔离、环境过滤、工具策略和显式门禁降低误操作风险，但不宣称普通宿主机子进程能够隔离恶意代码。

不可信仓库、不可信依赖或多用户执行必须使用容器、VM 或同等可强制执行的沙箱，才能成为受支持的安全配置。

## 成功指标

初始指标关注可验证运行，而不是模型质量宣传：

- 100% 的 Run 具有冻结基线和 Outcome Bundle。
- 100% 的 Accepted Run 具有通过的独立 VerificationReport。
- 强制安全配置下，0 个 Worker 进程获得 Publisher 凭据。
- 0 个 Accepted Run 包含越界路径。
- 无需阅读原始终端记录即可判断中断 Run 的状态。
- 相同任务身份的重复发布具有幂等性。

积累足够数据后，可按 Adapter 和任务类型统计首轮通过率、返工次数、验证失败率、成本、耗时、对账与补偿率。补偿不是回滚：历史副作用与补偿结果都必须保留。

## 交付阶段

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
