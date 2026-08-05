# Marshal Fan-out 编排：Lead 汇总与设计决策

- 汇总日期：2026-08-05
- 汇总者：Lead Agent（pi），按 [Operator Runbook](../operator-runbook.md) 第 10.4 节汇总纪律执行
- 输入证据：
  - **E1**：[deep-research-report.md](deep-research-report.md)（外部 Deep Research，含量化证据与来源标注）
  - **E2**：[gemini-research.md](gemini-research.md)（面向 Marshal 约束的架构设计稿）
  - **I1**：[milestone-6-report.md](../milestone-6-report.md)（M6 实战证据链）
  - **I2**：2026-08-05 fan-out 试点实测（`.marshal/dev/fanout-pilot/`，Run `fanout-design-{a,b,c}-20260805` 与 `-a2` 重跑）
  - **I3**：AGENTS.md 不可破坏的不变量

## 一、共识（多来源独立得出）

| # | 共识 | 证据 |
| --- | --- | --- |
| C1 | **并行写入负收益**：多 Worker 同时写同一任务/耦合代码，集成成本吃掉甚至超过并行收益；生产级稳定形态是"并行贡献 intelligence，写入保持 single-threaded" | E1（Cognition 实践、Co-Coder 朴素切分成本 +60%、Claude Code Teams 快但低于串行基线质量）；E2（禁止编码阶段并行写入）；I2（3 个并行 Worker 全部 fail-closed） |
| C2 | **fan-out 属于两端**：编码前的只读探索（调研）与编码后的独立评审（审计），不是编码本身 | E1（推荐拓扑三平面、引入顺序）；E2（非对称三明治架构）；与 Runbook §10 的调研队/评审团一致 |
| C3 | **信任编排是业界空白**：没有通用框架原生强制"Worker 不得自证、证据绑定决策、凭据隔离、防篡改日志、幂等恢复、fail-closed"这组不变量 | E1（六项不变量扫描结论"未发现"）；I1（Marshal 全部六项已实战验证）→ Marshal 的差异化定位坐实 |
| C4 | **协议分级是必需的**：小任务的协议成本超过收益是普遍经济边界，不是 Marshal 实现缺陷；必须按任务风险/可分解性选择协议重量 | E1（Anthropic 复杂度-Agent 数规则、AutoGen 单 Agent 优先建议、协议分级表）；E2（Tier 0/1/2 Router）；I1（M6 E2E 中 15 分钟 verify 对小任务不成比例） |
| C5 | **汇总必须结构化**：Lead 不得用自由文本"综合判断"替代裁决；findings 用统一 Schema、按证据身份去重、逐项处置、保留异议 | E1（Finding schema + 处置枚举 + ReviewDecision 字段清单）；E2（确定性裁决矩阵、反对 LLM 投票平均） |

## 二、分歧与裁决

| # | 分歧点 | E1 立场 | E2 立场 | 裁决与理由 |
| --- | --- | --- | --- | --- |
| D1 | 状态持久化基底 | 未要求更换 | SQLite WAL + append-only log | **维持现有 Journal+Snapshot 文件方案**。M1 已验证崩溃恢复与幂等重放；SQLite 在 implementation-plan 延后清单中，触发条件是性能证据，目前无此证据 |
| D2 | 评审裁决机制 | 结构化处置 + 保留异议，人工升级通道 | 硬否决矩阵（确定性测试 FAILED 直接 REJECT；安全 CRITICAL 一票否决） | **合并采纳**：确定性门禁本就有硬否决（accept 不能绕过 Required Failed Gate，I3）；LLM reviewer 的 CRITICAL/HIGH finding 默认 blocking，Lead 若否决必须写入 dissent 记录（E1 的 unresolved_dissent 字段） |
| D3 | 协议分级粒度 | 6 级（Lean→Critical）+ 5 维评分 | 3 级（Tier 0/1/2）+ 文件数启发式 | **先维持 Runbook 3 级（S/M/L）实践**，把 E1 的 6 级与评分维度作为 v0.2 参考；E2 的"文件数启发式"不可靠（改动大小 ≠ 风险），不采纳 |

## 三、采纳结论及证据

按实施顺序（每条注明证据来源）：

1. **第一优先：评审团（补丁评审平面）**——verify 通过、补丁冻结后，派 2–4 个 clean-context、只读的评审 Worker（correctness/security/test/maintainability 视角），输出结构化 findings，Lead 按 D2 裁决。
   证据：E1（Cognition clean-context reviewer、Codex 按视角并行 review 示例、PoLL 异构降偏差、漏洞检测 F1 71.4→77.2 初步证据）；E2（阶段 3 Jury）；I1（里程碑级交叉审计已实践）。
2. **第二优先：调研队（调研与计划平面）**——大任务/模糊需求在编码前派只读 Explorer（调用链/数据模型/测试面视角），产出结构化 ResearchFinding，Lead 结构化合并后冻结 TaskSpec。
   证据：E1（Anthropic Research 系统 +90.2%、Codex read-heavy 优先建议）；E2（阶段 1）。
3. **第三优先（拆分两种形态）：任务级并行**。
   - **3a 跨仓库并行（现在采纳，Lead 层约定即可，零 Core 改动）**：大型跨仓库任务按仓库拆解，每仓库一个独立 Run，各自独立 worktree/生命周期/证据；仓库边界是天然的 ownership 契约，写集合天然隔离。前置条件是跨仓接口契约先行冻结（摘要写入各子 TaskSpec），集成阶段全量重验。
   - **3b 仓库内拆解并行（继续暂缓）**：同一仓库内把大任务拆给多个 Worker，硬前置是依赖分析能力（dependency graph）；Co-Coder 证据表明只有在强耦合部分被划入同一 Worker、弱耦合边并行时才有正收益。
   证据：E1（Co-Coder 依赖感知切分的前提；集成后各分支证据不能证明集成结果；Cognition 写入集中原则）；I3（单写入者不变量在跨仓库场景按仓库自然满足）；操作者提出的跨仓库大型任务场景（仓库边界 = 免费的内聚/ownership 划分）。
4. **评审 Worker 的最小权限**：fan-out 的调研/评审角色应为只读能力画像（无编辑工具、无仓库写权限），当前三 Adapter 只有 workspace-write 画像，需要新增 read-only 执行画像支撑。
   证据：E1（前置条件"读写 capability manifest"、Codex 可配置 read-only reviewer）；E2（Explorer 剥离写入工具）；I2（试点中调研 Worker 持有不必要的写权限）。
5. **Findings Schema 与裁决纪律**：评审 Worker 输出统一 finding 结构（稳定 ID/角色/论断/严重级/位置/证据引用/置信度/处置建议）；去重按"产物+位置+证据身份"，不按语言相似度；最终 Decision 保留 accepted_finding_ids 与 unresolved_dissent。
   证据：E1（完整 schema 与处置枚举）；E2（EvidenceDigest 契约）。
6. **度量纪律**：每次 fan-out 记录基线估计、agent 数、墙钟、token、冲突数、findings、人工分钟数；没有度量不得扩大 fan-out 使用面。
   证据：E1（"否则系统只能看到 Agent 同时在工作"）；I2（试点已开始积累此类数据）。

## 四、明确不采纳

| 项 | 理由 | 证据 |
| --- | --- | --- |
| 多 Worker 协同写同一任务 | 共识 C1 | E1/E2/I2 |
| 多数投票决定 findings | 关键安全 finding 可能被多数淹没；findings 不是互斥选项 | E1 |
| Lead 自由文本综合替代裁决 | 形成新的隐式权威，绕过门禁 | E1/E2 |
| SQLite 状态引擎 | 分歧 D1 裁决 | I1（现有方案已验证） |
| 文件数/行数启发式定级 | 改动大小 ≠ 风险 | E1（风险维度评分更合理） |

## 五、Marshal 现状对照（E1 前置条件清单）

| 前置条件 | Marshal 现状 |
| --- | --- |
| TaskSpec 可哈希、不可变、版本化 | ✅ 已有（canonical digest + 冻结门禁） |
| Attempt/lease/heartbeat 模型 | ✅ 已有（Run Lease + Task Lock + Journal） |
| Evidence schema 可合并可绑定 | ✅ 已有（VerificationReport/ArtifactManifest/ReviewPacket 全 Schema 化） |
| 幂等与崩溃恢复 | ✅ 已有（M1/M5 验证；发布 marker 幂等已实战） |
| 读写 capability manifest | ❌ 缺：只有 workspace-write，无 read-only 画像（采纳项 4） |
| Fan-in policy engine | ⚠️ 部分：Review Bridge 文件契约可承载，缺 findings 汇总约定（采纳项 5） |
| Repository dependency graph | ❌ 缺：仓库内拆解并行（采纳项 3b）的硬前置；跨仓库并行（3a）不需要，仓库边界即 ownership |
| 基准任务集与度量 | ⚠️ 起步：试点开始积累（采纳项 6） |

## 六、试点实测附录（I2）

| 事件 | 数据 |
| --- | --- |
| 轮次 1：并行 3 Worker（opencode/qwen/pi，900s 预算） | 2× `context deadline exceeded` + 1× pi 输出超限（16MB）；全部 fail-closed，零远端副作用 |
| 轮次 2：串行重跑（1800s 预算，pi 32MB，机器高负载日） | opencode/qwen 仍 `context deadline exceeded`（>30 分钟）；pi 32MB 仍输出超限 |
| 交付物 | 0/3 份提案完成；本汇总因此仅基于 E1/E2/I1 与试点失败数据 |
| 结论 | 本机当前条件（多会话共存 + provider 配额）下，读密集+长文生成类任务无论串并行都不可靠；fan-out 决策必须以度量数据为准；pi 的转录本二次方增长对读密集型任务是硬约束，优先级提升到“需要解决” |

## 七、下一步

1. Runbook §10 升级到 v0.2：并入采纳项 1/2/5/6 的操作约定（评审团视角清单、finding 结构、裁决与否决规则、度量字段）与采纳项 3a 的跨仓库协作约定；
2. 首个评审团实战：下一个 M 级以上真实代码任务执行"verify 后评审团"流程，采集 findings 质量数据；
3. read-only 执行画像设计提案（Worker 能力清单、Adapter 支持面）——涉及权限契约，按仓库规则需要 ADR 讨论后再动代码；
4. 跨仓库任务族实战：下一个真实的多仓库任务按 §10.6 约定执行，验证契约冻结与集成重验流程；
5. 暂缓：仓库内拆解并行（等依赖分析能力）与 Core 级 fan-out 机制（等评审团实战数据）。
