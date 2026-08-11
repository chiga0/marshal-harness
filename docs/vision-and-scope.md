# 愿景与范围

## 愿景

Marshal 让 Coding Agent 的工作像一套工程交付流程，而不是无结构的终端对话。主 Agent 可以委派实现，同时保留对范围、审查、证据和最终交付决策的控制。

当更换 Worker Provider 不会改变任务含义、验收标准和发布所需证据时，Marshal 才算实现目标。

长期目标已由 [ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md)（2026-08-10 接受）正式重置：从“本地单次 CLI 编排”升级为**长寿命 Runtime/Control Plane 持续接收、耐久排队、分发和审计大量有界 Task/Run/Attempt；环境与状态可重建、可恢复、可审计**。执行沙箱可插拔，Cloudflare Sandbox 仅作为首个可替换远程 Provider。[ADR 0019](adr/0019-deterministic-control-plane-typed-execution-and-goal-admission.md) 进一步冻结：Supervisor 是确定性 Core，不是 LLM；LLM 只执行 typed semantic workload；Goal plan 必须先 proposal、后由 Core 确定性接纳。目标架构与路线见 [Runtime 架构](runtime-architecture.md) 与 [实施计划](implementation-plan.md) M7–M13。

## 问题定义

本地 Coding Agent 在五个关键方面存在差异：

1. 调用方式和会话协议不同。
2. 权限控制不同，而且可能依赖交互确认。
3. 输出事件和错误语义不同。
4. Agent 可能错误总结自己的变更或测试结果。
5. 长任务可能留下部分状态，临时脚本难以可靠检查或恢复。

Harness 必须统一这些差异，同时不能假装所有 Worker 具有相同能力。

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

### G7：耐久 Runtime 与可插拔沙箱（长期）

Runtime 长期稳定运行，持续接受新 Task 并分发；Sandbox、Agent 与 Runtime 进程可丢弃，权威事件、证据与副作用记录在其外部耐久保存；执行环境通过统一 SandboxProvider 契约接入，替换 Provider 不改变任务含义与验收标准。该目标的分层、恢复/fencing/checkpoint 语义与实施路线由 [ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md) 冻结。

### G8：确定性控制与有界复杂任务（长期）

Marshal Core 是唯一 Supervisor 与权威状态机；Plan/Implement/Verify/Review/Publish 作为 typed execution 共享基础调度机制，但不共享权限或通用协议。复杂 Goal 的计划、重规划、预算预留、证据适用性和人工暂停/恢复必须可回放且有界，Planner 不能直接创建权威 Run 或执行副作用。

## MVP 范围

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

## MVP 明确不做

- 把 Worker 输出当作可信证据。
- 让多个写 Worker 共享同一 worktree。
- 默认自动 merge。
- 第一条纵向链路支持 GitLab 发布。
- 托管式多租户控制平面。
- 对恶意仓库或恶意构建脚本提供强隔离承诺。
- 在没有评测数据时自动选择“最佳”Worker。
- Web UI、远程队列、分布式 Worker 或集群调度（不属于 MVP；耐久排队与分发已纳入 M7–M12 路线，见[实施计划](implementation-plan.md)）。
- 取代仓库 CI 的最终集成信号。
- 在没有 Adapter 契约时支持任意交互式 Agent。

## 用户

### 主要用户

使用 Codex CLI、Codex Desktop 或手机端 Remote 监督 Codex 主 Agent，并把具体实现委派给本地 Coding Agent 的工程师。

### 次要用户

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

### 阶段 4：耐久 Runtime 与可插拔沙箱（M7–M13）

由 ADR 0016–0019 冻结：常驻确定性 Runtime/Control Plane、SandboxProvider 契约与 conformance、耐久调度与恢复/fencing、Cloudflare 远程 Provider、通用副作用对账与补偿、生产存储/HA、开源部署与长稳验证（M7–M12）；M13 承接复杂 Goal，并按“proposal → deterministic admission → accepted plan → 有界 Run DAG”实现计划、重规划、累计预算、依赖驱动 Evidence 适用性与 Goal `PAUSED` 人工控制。M7 只冻结 Project/Goal 的存在性和权威归属，完整控制器语义由 ADR 0019 与 M13 承接。多租户服务化仍属于评估项。
