# 多 Agent 编排架构深度调研：面向 Marshal Harness 的证据门禁式外部扫描

## 执行摘要与核心判断

截至 **2026 年 8 月 5 日**，多 Agent 系统已从早期的自由对话式“agent society”逐步收敛到几种较务实的工程形态：**Orchestrator–Worker fan-out/fan-in、显式状态图、独立 workspace、任务控制平面，以及依托 Temporal/Durable Task 的可恢复执行**。LangGraph、CrewAI、AutoGen/AG2、OpenAI Agents SDK 等主要解决“如何安排 Agent”；Claude Code、Codex、Devin 更进一步解决“如何让多个 Coding Agent 在隔离环境中工作”；Temporal 与 Microsoft Durable Extension 则解决“如何让长流程在崩溃、重启和重试后继续”。但这些系统通常没有把**谁有资格证明什么、决策必须绑定哪份证据、什么凭据可以执行发布动作**建模为一等公民。citeturn1search1turn13view4turn0search11turn12search23turn11search1

本次扫描得到的最重要结论是：

**第一，多 Agent 的收益主要来自可分解性，而不是 Agent 数量本身。** Anthropic 的内部 Research eval 中，Opus 4 Lead 加 Sonnet 4 Subagents 相比单 Opus 4 提升 90.2%，复杂调研的时间最多缩短 90%；但其多 Agent 系统约消耗普通 Chat 的 15 倍 token，并明确指出大多数高依赖 Coding 任务不适合当前多 Agent 协作。citeturn14view0

**第二，Coding fan-out 的关键不是按文件平均切分，而是按依赖内聚度切分。** 2026 年 Co-Coder 预印本在 28 个 repository-level 任务上报告：依赖感知切分可同时提升 pass rate、降低墙钟时间和成本；而朴素 file-based parallel 在一个基准中成本增加 60%，质量只提高 3.2 个百分点，Claude Code Agent Teams 虽然最快，却低于串行基线质量。该研究样本较小、集中于 Python repository benchmark，仍需独立复现，但它为 Marshal 的“单 worktree 单写入者”和任务依赖分析提供了直接支持。citeturn14view1turn15view1turn15view2

**第三，目前生产实践最稳定的形态是“并行贡献 intelligence，写入保持 single-threaded”。** Cognition 对 Devin 多 Agent 实践的总结是：unstructured swarm 多数没有实际采用价值，实用形态更接近 “map-reduce-and-manage”；干净上下文的 Reviewer、专项调查 Agent 和 Manager 能提供额外信息，但写入和最终决策应保持集中。OpenAI Codex 的当前文档同样建议先把 subagents 用于 exploration、tests、triage、summarization，对并行 write-heavy workflow 保持谨慎。citeturn15view3turn15view4turn15view5

**第四，多评审者确有潜力改善判断，但代码缺陷发现的强证据仍有限。** PoLL 在六个数据集上发现异构小模型 jury 优于单个大 Judge，且成本低七倍以上；2026 年一个漏洞检测研究在 262 个 Juliet 样本上将 F1 从单专家的 71.4% 提升至 77.2%，独立 adversarial verifier 使 precision 提高 10.3 个百分点。但前者是通用 LLM evaluation，后者是 workshop preprint 和合成式安全数据，不能直接等价为真实 PR review 的缺陷发现率。citeturn15view9turn15view10turn15view11

**第五，Marshal 不宜首先建设“多个 Worker 协同修改同一任务”。** 最值得引入的顺序是：

1. **只读调研队**：并行探索代码、依赖、历史缺陷和测试面；
2. **补丁冻结后的独立评审团**：从 correctness、security、test adequacy、maintainability 等不同角度审查同一不可变 patch；
3. **真正独立 TaskSpec 之间的任务级并行**：每个任务独占 worktree 和写入者；
4. **证据驱动的确定性 fan-in**：Lead 只能合并 finding，不能凭语言共识替代 Verification，也不能自行产生发布授权。

换言之，Marshal 应采用 **fan-out intelligence, serialize mutation, independently verify, deterministically adjudicate**，而不是复刻自由通信式 Agent Team。

## 架构格局与代表系统

主流系统可以归纳为六种编排模式：

| 模式 | 代表系统 | 核心特点 | 主要风险 |
|---|---|---|---|
| State graph | LangGraph、Google ADK Graph Workflow | 状态、节点、边和并发路径显式化 | 图能表达流程，但不自动表达信任资格 |
| Conversational team | AutoGen/AG2、CrewAI Crew、Group Chat | Agent 通过消息、角色和选择器协作 | 上下文膨胀、重复工作、隐式状态 |
| Manager-as-tools | OpenAI Agents SDK、Anthropic Research | Lead 将 Worker 当作工具调用并汇总 | Lead 成为信息和信任集中点 |
| Handoff/swarm | OpenAI Handoffs、AG2 Swarm、Claude Agent Teams | 控制权在 Agent 间转移或共享任务列表 | 责任归属与最终权威容易模糊 |
| Workspace/task parallelism | Codex、Devin、Symphony | 每个任务或 Agent 使用隔离 workspace/VM | 集成冲突、并发 PR 和人工审阅瓶颈 |
| Durable workflow | Temporal、Microsoft Durable Extension、LangGraph persistence | Event history、checkpoint、retry、resume | 可恢复不等于可验证或防篡改 |

### LangGraph

LangGraph 的核心是显式 `StateGraph`：节点读取和更新共享 state，边定义控制流，同一 super-step 内的多个节点可并行执行。它采用 Pregel 风格执行模型，并通过 checkpointer 在每个 super-step 保存 checkpoint，从而支持 thread-scoped persistence、time travel、human-in-the-loop 和从最后成功步骤恢复。citeturn1search1turn1search3turn13view6

中间产物通常进入 graph state，或由 subgraph 返回父图；subgraph 可配置为 per-invocation、per-thread 或 stateless。若不配置 checkpoint，则 subgraph 不具备相同的崩溃恢复能力。Interrupt 机制要求开发者注意副作用位置，并明确建议在可能重放的操作中使用幂等设计。citeturn1search0turn1search4

适合场景是：流程边界清晰、需要分支/并行、人工审批以及长任务恢复的 Agent workflow。局限是它提供的是**状态和执行控制原语**，而不是安全策略：公开文档未显示其原生强制“作者不能成为权威 Reviewer”、Reviewer 决策必须绑定 evidence digest，或 Worker 与 Publisher 使用不同 credential class。上述约束需由应用状态机、节点身份和外部授权层自行实现；截至本次扫描，完整内建支持**未能验证**。

### CrewAI

CrewAI 将 **Flow** 定位为结构化、事件驱动的流程控制层，将 **Crew** 定位为执行某项工作的 Agent 团队。官方生产建议是 Flow-first：用 Pydantic state 管理结构化状态，显式传递上下文，用 structured output 和 task guardrail 限制输出；`@persist` 可保存流程状态，以便崩溃或 human-in-the-loop 后恢复。Crew 支持 sequential、hierarchical 等 process，也可通过异步任务实现并行。citeturn0search7turn0search12turn13view4turn10search3turn10search15

其交接单位主要是 task output、Pydantic object 和 Flow state，适合业务自动化、角色明确的研究/内容/运营流程。Guardrail 可以检查输出格式或内容，但通常由当前流程中的函数或 Agent 执行，并不自动建立独立信任域。因此 CrewAI 可以实现 Marshal 的部分流程，但“Reviewer 独立性”和“证据绑定授权”仍然是应用层策略，而非框架不变量。

### AutoGen、AG2 与 Microsoft Agent Framework

AutoGen 的 AgentChat 提供 RoundRobinGroupChat、SelectorGroupChat、MagenticOneGroupChat 和 Swarm 等 team preset。团队通常共享 message context，由固定轮询、模型选择器或 handoff 决定下一发言者；官方文档也提醒多 Agent team 需要更多 scaffolding，能够由单 Agent 解决时应优先单 Agent。citeturn0search11

AG2 延续并扩展了 GroupChat、nested chat、context variables 和 workflow control。GroupChat Manager 可以使用 auto、round-robin、random、manual 或自定义 speaker selection，并支持 start/resume；nested chat 常通过 summary 将子对话结果传回主会话。citeturn10search25turn10search12turn10search19turn10search0turn10search22

这类架构适合 brainstorm、debate、专家路由和 conversational workflow，但中间状态高度依赖消息历史和 summary。即使支持序列化或恢复，对外部副作用的一致性、精确 artifact identity 和 credential boundary 仍需应用自行管理。

微软在 2026 年推进 **Microsoft Agent Framework**，将其描述为 AutoGen 与 Semantic Kernel 的后继统一框架，提供 sequential、concurrent、handoff、group chat 和 magentic orchestration。其 Durable Extension 将 Agent session 和 workflow checkpoint 持久化到底层 Durable Task infrastructure，支持跨 host 恢复、长期暂停和确定性 workflow replay。citeturn5search5turn5search10turn14view7

相较 AutoGen，Microsoft Agent Framework 在 durable state 方面更接近 Marshal 需要，但它仍然主要保证“流程能够继续”，并不天然保证“继续后的决策仍绑定原 patch、原证据和原授权主体”。

### OpenAI Agents SDK 与 Responses Multi-agent

OpenAI Agents SDK 支持两类主要编排方式：

- **Manager-as-tools**：中央 Agent 将专业 Agent 作为工具调用，最终答案仍由 Manager 控制；
- **Handoffs**：当前 Agent 将会话和控制权交给另一个 Agent。

官方同时支持 code-driven orchestration，例如通过 `asyncio` 并行调用 Agent。OpenAI 文档指出，代码编排通常在速度、成本和行为上更可预测，而完全由 LLM 决定 orchestration 更灵活但更不确定。citeturn0search6turn12search23

SDK 的 Sessions 保存对话记忆，Tracing 记录模型生成、tool call、handoff 和 guardrail，序列化的 `RunState` 可支持 human approval 后 resume。需要注意的是，input/output guardrail 默认只覆盖 run 的初始输入和最终输出；在 handoff chain 中，中间 Agent 未必经过相同 guardrail，除非逐个使用 tool guardrail 或额外检查。citeturn12search2turn12search0turn12search13turn12search1turn12search21

2026 年的 Responses Multi-agent beta 提供 root agent 创建并发 subagent、发消息、追问、等待和中断等 hosted action。官方建议其用于可独立、边界明确的工作，不适合严格顺序链和共享可变状态；默认并发数为 3。一个对 Marshal 很重要的细节是：同一 multi-agent request tree 中的 Agent 默认可访问 request 配置的模型和工具，因此它并不自动提供 Worker/Reviewer/Publisher 级别的 credential isolation。citeturn6view2

### Anthropic Research、Claude Code Subagents 与 Agent Teams

Anthropic 的 Research 系统是典型的 Orchestrator–Worker：LeadResearcher 制定计划并保存至 Memory，创建多个有独立 context window 的 Subagents 并行搜索，Subagents 返回压缩后的 findings，Lead 合成结果，最后由单独的 CitationAgent 将报告主张映射到来源位置。Anthropic 还建议对大 artifact 采用外部文件系统交接，让 Subagent 保存产物并只返回引用，以减少“传话游戏”和 token 复制。citeturn14view0

该架构特别适合开放式、breadth-first 调研、跨大量来源搜索和上下文压缩。局限包括 token 消耗高、Lead 同步等待造成 straggler bottleneck、Subagent 之间无法实时协调，以及共享上下文或依赖密集任务表现不佳。CitationAgent 提供的是**引用归属检查**，并不等价于独立 Verification authority：它可以确认“报告引用了什么”，但不能证明“Worker 的代码通过了可信测试”。

Claude Code 的 **subagents** 使用独立 context，可拥有特定 system prompt、模型、工具和权限，并向主 Agent 返回 summary；这使只读 Explorer、Security Reviewer 或 Test Reviewer 可以配置成比 Worker 更小的权限集合。citeturn13view1

Claude Code **agent teams** 则是多个独立 Claude Code session，共享 task list，并可相互直接通信。官方将其定位于 competing hypotheses、并行 code review 和相对独立的 feature pieces，同时明确说明其 token 成本更高且仍属实验性功能。citeturn13view0

这些权限配置为 Marshal 提供了可借鉴的 capability separation 原语，但尚未看到官方机制可以证明 Reviewer 与 Worker 没有共享底层 credential，或把 ReviewDecision 强制绑定到特定 commit/evidence digest；完整满足 Marshal 信任模型的能力**未能验证**。

### OpenAI Codex 与 Symphony

Codex 的云任务将每项工作放入独立 sandbox，允许多个任务并行运行、执行测试并形成 PR；Codex app 使用独立 thread 和内建 worktree，让不同 Agent 在隔离 copy 上修改，用户最后检查 diff。citeturn3search0turn3search3turn3search1

当前 Codex subagent 文档建议从 read-heavy exploration、tests、triage 和 summarization 开始；并行 write-heavy workflow 会产生冲突和协调开销。Subagents 消耗额外 token，可以为不同角色选择不同 model/reasoning effort，也可将专项 Agent 配置为 read-only，但 child 默认可能继承 parent 的 sandbox 和 approval mode，因此权限隔离需要显式配置而不能假设存在。citeturn15view5

Symphony 将 issue tracker 转化为 Agent control plane：每个开放任务映射到独立 Agent/workspace，任务状态驱动调度，系统可以设置最大并发、重试和 backoff。OpenAI 报告部分内部团队在最初三周出现 landed PR 数增长 500%，但这是生产案例观察，不是对照实验，不能推出单任务质量提高 500%，也没有公开相应 token、rework 或 defect escape 数据。citeturn15view7turn15view8

Symphony 与 Marshal 的共同点是把任务状态机置于 Agent 之上，而不是让 Agent 对话成为唯一状态。但 Symphony 主要以 issue 和 workspace 为编排单位；公开规范中尚未看到 Marshal 式 evidence-bound review authority 与 Worker/Publisher credential split。

### Devin 与 Managed Devins

Devin 的 Managed Devins 使用 Coordinator 拆分任务，子 Devin 在独立 VM 中执行，Coordinator 监控状态、处理冲突并汇总。子 session 可以有独立 ACU 限额，并可分别产生 PR；早期 MultiDevin 也采用 Manager 加多个 Worker、合并成功分支的方式。citeturn13view3turn4search3turn4search10

Cognition 在 2026 年对实践的总结比产品能力本身更值得关注：平行写入 swarm 的实际采用有限，效果更好的模式是 Manager 分解、子 Agent 提供调查或评审结果、写入保持 single-threaded。其识别的核心开放问题不是 Agent 数量，而是 context transfer、升级条件、跨 Agent 发现传播和责任碎片化。citeturn15view3turn15view4

Devin 的 VM 隔离是强执行隔离，但 VM 隔离本身不能建立“谁可签署验收”的组织信任边界；是否有不可篡改证据 ledger、Reviewer/Publisher 独立身份和 digest-bound approval，公开资料中**未能验证**。

### Google ADK 与其他工作流框架

Google ADK 2.0 在 2026 年引入 graph workflow、dynamic workflow 和 collaborative workflow；其 `ParallelAgent` 可并发执行独立子 Agent，结果通常通过 shared session state 或 `output_key` 交接。citeturn10search13turn10search9turn10search5turn10search32

ADK 的设计与 LangGraph 类似，适合确定性流程和 Agent 分支混合，但 shared session state 仍是协作状态，不是证据授权模型。MetaGPT、ChatDev、Magentic-One 等系统对角色分工和软件组织模拟有研究影响，但在 2026 年工程实践中，越来越多平台转向显式 workflow、isolated workspace 与 durable state，而不是仅依赖角色对话。Co-Coder 的 related-work 分析也把通信开销和任务切分视为当前多 Agent Coding 的主要瓶颈。citeturn14view1

### Temporal 与 durable execution

Temporal Workflow 通过持久化 Event History 和 deterministic replay，使进程、机器或网络故障后能够重建 workflow state；外部副作用由 Activity 执行，Activity 通常具有 at-least-once 语义，因此应使用 idempotency key 或其他幂等设计。Temporal 已发布与 OpenAI Agents SDK 的集成，用于持久执行 Agent、重试 rate limit 和网络错误。citeturn11search1turn11search4turn11search0turn11search5turn5search1

这正是 Agent 框架普遍较弱、而 Marshal 特别需要的部分：

- Agent 推理可以重试；
- 工具副作用必须建模为 Activity；
- workflow crash 后从 event history 恢复；
- 等待远端 CI 时不占用持续计算资源；
- publish、comment、merge 等操作用业务 idempotency key 去重。

但必须区分三种属性：

| 属性 | Temporal 等 durable engine |
|---|---|
| 可恢复 | 强：通过 history、checkpoint、replay |
| 可审计 | 中到强：可看到事件和状态变迁 |
| 防篡改、密码学可验证 | 公开文档未声明 event history 是 append-only cryptographically tamper-evident ledger，**未能验证** |

因此，Temporal 可以成为 Marshal 的执行 substrate，但不能替代 Marshal 自己的 evidence hash、签名/身份、credential separation 和 decision policy。

## 并行化的实证边界

### 可量化证据

| 来源与时间 | 场景 | 对比结果 | 墙钟时间 | 成本 | 证据限制 |
|---|---|---|---|---|---|
| Anthropic Research，2025-06-13 | 开放式多来源调研 | Opus 4 Lead + Sonnet 4 Subagents 比单 Opus 4 高 90.2% | 复杂调研最多缩短 90% | Multi-agent 约为普通 Chat 的 15× token | 内部 eval；非 Coding；未公开完整题集与统计分布 citeturn14view0 |
| Co-Coder，2026-05-31 | 28 个 repository-level Coding 任务，DevEval | pass rate 56.8% → 68.1% | 800s → 442s，约 1.81× | $0.25 → $0.18，下降 28% | arXiv v1；样本小；主要为 Python citeturn15view1 |
| Co-Coder，2026-05-31 | CodeProjectEval | pass rate 20.1% → 34.1% | 2756s → 1315s，约 2.10× | $1.03 → $0.67，下降 35% | 同上，需独立复现 citeturn15view2 |
| File-based Parallel，同一研究 | 朴素按文件并行 | DevEval 仅 56.8% → 57.7%；CodeProjectEval 20.1% → 23.3% | 有一定加速或几乎不变 | 分别增加 44% 和 60% | 显示错误切分可抵消并行收益 citeturn15view1turn15view2 |
| Claude Code Agent Teams，同一研究 | 自协调并行 Coding | DevEval 54.1% < 串行 56.8%；CodeProjectEval 16.3% < 20.1% | 两组中都明显更快 | 更低 | 只测试特定版本、prompt 和 benchmark，不能泛化到全部 Claude Code 使用方式 citeturn15view1turn15view2 |
| Symphony，2026 | Issue-level 并行 Coding | 某些团队 landed PR 数三周内增长 500% | 未公开 | 未公开 | 内部案例；不是控制实验；质量和返工数据未披露 citeturn15view7 |

Co-Coder 最值得关注的不是某个绝对数字，而是其机制：它通过 static analysis 建立 repository dependency graph，将共享 symbol 密集的文件放在同一 partition，隔离 structural hub，并使用 dependency-aware scheduler。研究显示，朴素并行容易产生不一致 interface 和重复生成，而 cohesion-aware partition 能减少跨 Agent context transfer。citeturn14view1turn15view2

不过，该论文称“在依赖最密集的项目上收益最大”不能被简单解释为“耦合越高越应该多 Agent”。其前提是系统能够识别强耦合部分并把它们**放在同一个 Worker 内**，只将 partition 之间的弱耦合部分并行化；若把高度耦合文件分给不同 Agent，实验恰恰显示结果更差。citeturn15view1turn15view2

### 哪些任务通常正收益

| 任务特征 | 原因 | 适合的 fan-out |
|---|---|---|
| 多方向探索、资料检索 | 搜索空间可独立覆盖，结果可压缩后合并 | Research squad、Explorer agents |
| 独立假设验证 | 每个 Agent 可用不同路径寻找反例，减少单一路径依赖 | Competing-hypothesis reviewers |
| 代码审查与安全扫描 | 多数为 read-heavy，不需要同时改变代码 | Correctness、Security、Test jury |
| 测试执行与故障归因 | 测试 shard 可独立运行，结果可结构化合并 | Test fan-out |
| 低耦合模块或不同 repository | workspace 和接口边界明确 | Task-level parallel workers |
| 大上下文压缩 | 每个 Agent 读取一部分 corpus，返回 distilled artifact | Map-reduce research |

Anthropic 明确把 breadth-first、跨大量信息源和多工具任务视为 multi-agent 的优势场景；Codex 当前文档建议优先 parallelize exploration、tests、triage 和 summarization；Cognition 则强调 clean-context Reviewer 和 intelligence helpers。citeturn14view0turn15view5turn15view4

### 哪些任务通常负收益

| 任务特征 | 主要失败方式 |
|---|---|
| 多 Agent 同时编辑同一 worktree 或同一核心模块 | 覆盖修改、冲突、接口漂移、状态不可归因 |
| 高频交互才能确定接口 | communication token 和等待时间超过有效工作 |
| 单一短小局部修复 | 启动、传递上下文、汇总和评审成本超过编码本身 |
| 必须共享完整隐含上下文 | Subagent summary 丢失细节，Lead 出现“information bottleneck” |
| 严格顺序迁移或单一外部副作用链 | 并发无缩短 critical path，反而增加恢复复杂度 |
| 模糊拆分、无 ownership contract | 重复调查、遗漏范围、不同 Agent 对目标理解不一致 |
| 写入和最终判断由同一 Agent 群体循环完成 | 容易形成自洽但未独立验证的共识 |

Anthropic 的早期系统曾为简单查询启动约 50 个 Subagent，产生重复搜索和无边界消耗，因此后来为任务复杂度设置明确 scaling rules；其建议简单事实只用一个 Agent，普通比较使用 2–4 个，复杂研究才使用更多。citeturn14view0

这也解释了 Marshal 在小任务上观察到“协调协议成本超过收益”：这不是实现异常，而是当前多 Agent 系统的普遍经济边界。并行只有在可并行工作量大于 decomposition、context transfer、fan-in、冲突处理和额外验证成本时才有意义。

## 多视角评审、审计与结果汇总

### 通用 jury 与 debate 证据

2024 年的 **Replacing Judges with Juries / PoLL** 使用来自不同 model family 的多个较小 evaluator 代替单一大 Judge。在三个 Judge 场景、六个数据集上，PoLL 优于单个大型 Judge，减少 intra-model bias，并降低七倍以上成本。关键不是简单复制同一模型，而是利用**异构性**降低相关错误。citeturn15view9

**ChatEval** 在文本生成评估中让多个角色通过 debate 形成评价，报告其在两个 benchmark 上相比单 Evaluator 有更好的准确性和人类相关性。该证据支持多视角评估，但目标是自然语言质量，不是代码执行正确性。citeturn9search1turn9search21

**More Agents Is All You Need** 表明对同一问题独立采样并多数投票，可随 Agent 数量增加提升若干推理任务表现，且困难任务的收益更明显。不过这种方法要求答案能够归一化并投票；代码评审中的 finding 通常不是互斥选项，因此不能直接套用 majority vote。citeturn9search3turn9search15

### 代码审查与安全审计证据

2026 年 **Strategic Heterogeneous Multi-Agent Architecture for Cost-Effective Code Vulnerability Detection** 使用三个 cloud expert 加本地 adversarial verifier，在 262 个 NIST Juliet 样本、14 类 CWE 上取得 F1 77.2%，高于单 expert 的 71.4%；Verifier 将 precision 提高 10.3 个百分点，并通过并行执行实现 3× 加速。论文为 AAMAS 2026 Software Engineering Workshop 接收的 preprint，因此比纯未审稿稿件更强，但数据仍以 Juliet 样本为主，无法直接代表真实 repository PR。citeturn15view10turn15view11

2024 年 **CodeAgent: Autonomous Communicative Agents for Code Review** 使用多个角色处理 commit-message inconsistency、vulnerability、style 和 revision，并加入 supervisory QA-checker。其摘要宣称取得 state-of-the-art 结果，但本次可访问资料未提供足以复核“相对单 Reviewer 的统一缺陷发现率提升”数据，因此具体 uplift **未能验证**。citeturn7academia30

2025 年一项面向工业 C++ 的 defect-focused automated code review 研究宣称，通过多角色 LLM、program slicing 和过滤，在历史 fault-report merge request 上达到标准 LLM 的约两倍表现和早期 baseline 的约十倍。但公开摘要不足以确认统一 recall、precision、false-positive 和样本选择细节，且涉及内部数据；具体缺陷率改善应视为初步证据，完整复核**未能验证**。citeturn7academia32

所以，可以得出较窄的结论：

> 异构、多视角、带独立过滤者的审查，较单一 Judge 更可能发现互补问题并减少偏差；但目前没有足够公开、跨 repository、跨语言、生产级的控制实验，可以证明任意多 Agent code review 都会稳定提高真实缺陷发现率。

### Lead 应如何汇总

业内常见的 fan-in 方法包括：

| 方法 | 适用场景 | 主要问题 |
|---|---|---|
| Majority vote | 单一正确答案、分类、是否通过某项规则 | 少数但关键的安全 finding 可能被多数淹没 |
| Weighted vote | Reviewer 历史可靠性可测、任务类型稳定 | 权重可能过拟合旧 benchmark |
| Judge synthesis | 开放式研究、多个报告合成 | Judge 可能删掉自己不理解的证据 |
| Structured merge | Code review、审计、测试 findings | 需要统一 schema 和去重规则 |
| Adversarial verification | 安全、合规、高风险变更 | 成本和 latency 较高 |
| Escalation to human | 冲突无法由客观证据解决 | 吞吐下降，但可保留责任主体 |

对 Marshal，**不建议用一个 Lead 读完自然语言报告后“综合判断”**。Lead 应当执行受限的 adjudication，而不是自由生成 verdict。建议所有 Reviewer 输出同一 `Finding` schema：

```text
finding_id
reviewer_role
scope
claim
category
severity
affected_artifact
location
reproduction_or_check
evidence_refs
evidence_digest
confidence
recommended_disposition
```

Fan-in 首先按 `affected_artifact + location + evidence identity` 去重，而不是按自然语言相似度去重。每个 finding 应产生独立处置：

```text
accepted
rejected_with_reason
superseded
duplicate
needs_new_verification
human_escalation
```

最终 `ReviewDecision` 应保存：

```text
task_spec_hash
base_commit_sha
candidate_commit_sha
patch_digest
verification_bundle_digest
review_findings_digest
accepted_finding_ids
unresolved_dissent
policy_version
decision_actor
decision_credential_class
timestamp
```

这种模式可避免“平均数的平庸”：安全 Reviewer 的一个高置信严重 finding 不会因为另外两个 Reviewer 没看到而被 2:1 否决。PoLL 支持使用异构 Reviewer 减少相关偏差，Cognition 支持 clean-context Reviewer，Anthropic 则证明结构化 artifact 和轻量引用优于反复通过 Lead 转述；将这些原则组合成 evidence-first merge 是面向 Marshal 的工程推断。citeturn15view9turn15view4turn14view0

## 信任、验证与 durable execution 的空白

### Trust orchestration 扫描结论

在本次检查的官方文档与公开论文中，**未发现一个通用多 Agent 框架把以下六项同时作为原生、强制、不可绕过的不变量**：

1. Worker 不能为自己的产物提供权威验证；
2. ReviewDecision 必须绑定精确 TaskSpec、commit、patch 和 evidence digest；
3. Reviewer、Worker、Publisher 使用相互隔离的 credential class；
4. Worker 无权修改或替换 Verification evidence；
5. 日志具有密码学防篡改或外部 append-only 保证；
6. crash/retry 后所有外部副作用保持幂等，且授权不漂移。

因此，“主流框架主要处理 orchestration，而非 trust orchestration”这一判断基本成立。这里的“未发现”限定于截至 2026-08-05 的公开文档；私有企业内部系统可能实现了类似约束，但公开信息不足，**未能验证**。

### 各类系统覆盖了什么

| 信任或可靠性要求 | 已有部分能力 | 尚缺部分 |
|---|---|---|
| 禁止 Worker 自证 | 可通过独立 Agent role、clean context、独立 session 实现 | 多数框架不校验 Reviewer 与 Worker 是否为同一 authority |
| Evidence binding | Anthropic CitationAgent、structured outputs、traces、test artifacts | 通常未强制 verdict 引用特定不可变 evidence digest |
| Credential isolation | Claude/Codex 可限制 tool/sandbox；Devin 独立 VM | Publisher credential 独立、不可由 Worker 继承通常不是默认规则 |
| Artifact isolation | Codex worktree、Devin VM、独立 cloud sandbox | 多 workspace 最终集成仍可能由同一 Agent 兼任作者和裁决者 |
| Crash recovery | Temporal、LangGraph checkpoint、CrewAI persist、Microsoft Durable | 恢复后 artifact/version/authorization 是否一致需应用自行验证 |
| Idempotency | Temporal 明确要求 Activity 幂等 | 多数 Agent SDK 只做 retry，不自动保证 GitHub/CI 操作 exactly-once |
| Audit trail | OpenAI tracing、Temporal Event History、LangGraph checkpoint | 密码学 tamper evidence、WORM storage 和外部签名普遍未声明 |
| Fail-closed | 可通过 graph branch、guardrail、policy node 实现 | 很多默认 demo 在异常时重试、继续或让 Agent自行决定，非强制 fail-closed |

OpenAI Tracing 可以记录 handoff、tool call 和 guardrail，但 tracing 是 observability，不等于 authorization ledger。LangGraph checkpoint 可以证明 graph 曾处于某个 state，但若 state 中的 evidence reference 可由 Worker 写入，checkpoint 并不能证明证据来自独立 Verifier。Temporal Event History 能可靠恢复执行，却不会自动检查某个 Activity 的业务操作者是否越权。citeturn12search1turn13view6turn11search1

### 对 Marshal 最关键的设计区分

Marshal 应把以下实体严格分开：

| 实体 | 作用 | 允许写入 |
|---|---|---|
| WorkerOutput | 候选代码和作者声明 | Worker workspace、candidate branch |
| VerificationEvidence | 从冻结候选版本重新生成的测试/扫描结果 | 仅 Verifier evidence store |
| ReviewFinding | Reviewer 对候选和证据的解释 | Review namespace |
| ReviewDecision | 根据 policy 采纳或拒绝 findings | Decision authority |
| PublicationRecord | Draft PR、远端 CI 和发布操作 | Publisher authority |
| Outcome | 最终外部状态与验收结果 | Outcome controller |

尤其不能把 `Worker says tests passed` 当作 VerificationEvidence。即使 Worker 提供了日志，也只能视为 WorkerOutput 的一部分；权威 Verifier 应从不可变 `candidate_commit_sha` 和可信 toolchain/environment 重新运行命令，再生成自己的 evidence manifest。

### Durable execution 应怎样接入

Marshal 的 crash recovery 可以采用 Temporal 风格的分层：

- **Workflow state** 只保存确定性引用和状态转换；
- **非确定性推理**作为 Activity 执行并持久化结果；
- **Git、GitHub、CI、filesystem side effect** 均使用显式 idempotency key；
- **Agent attempt** 与逻辑 task 分离，一个 task 可以有多个 attempt；
- **恢复时重新验证 lease、commit SHA、policy version 和 credential class**；
- **任何 digest 不匹配都转为 quarantined/blocked，而不是自动继续**。

例如发布 Draft PR 的 idempotency key 可以是：

```text
repository_id
task_spec_hash
candidate_commit_sha
publication_policy_version
```

若 crash 后重试，Publisher 应先查询是否已存在匹配 marker 的 PR，而不是直接再创建一个。Temporal 对 Activity 重试和幂等性的要求直接支持这种设计，但 digest、policy 和 authority 检查仍应由 Marshal 实现。citeturn11search0turn11search5turn11search20

## 面向 Marshal 的 fan-out 方案与成本分级

### 推荐拓扑

最适合 Marshal 的不是单一 fan-out，而是三个受不同信任边界约束的 fan-out plane。

#### 调研与计划平面

多个只读 Agent 并行执行：

- repository map；
- dependency analysis；
- 历史 issue/commit 检索；
- test surface 识别；
- security/privacy impact 扫描；
- implementation alternatives；
- TaskSpec ambiguity challenge。

它们不得拥有 worktree 写权限，不得修改冻结 TaskSpec，只输出 `ResearchFinding`。Lead 根据 findings 形成建议；TaskSpec 的重新冻结必须由 CLI policy authority 显式执行。

该形态最接近 Anthropic Research，并符合 Codex 对 read-heavy parallelism 的建议。citeturn14view0turn15view5

#### 补丁评审平面

Worker 完成后先冻结候选：

```text
base_sha
candidate_sha
patch_digest
workspace_manifest
```

然后启动 2–4 个 clean-context、read-only Reviewer，例如：

| Reviewer | 关注范围 |
|---|---|
| Correctness Reviewer | TaskSpec 一致性、边界条件、状态转换 |
| Test Reviewer | 测试覆盖、遗漏路径、flaky 风险 |
| Security Reviewer | 输入边界、secret、权限、供应链与注入 |
| Maintainability Reviewer | 接口、复杂度、迁移和回滚风险 |

Reviewer 不应共用 Worker 的对话历史，只读取冻结 TaskSpec、不可变 diff、必要 repository context 和由 Verifier 生成的 evidence。Cognition 的实践认为 clean-context reviewer 能发现作者上下文盲区，Codex 也直接给出了按 security、test gaps、maintainability 并行 review 的示例。citeturn15view4turn15view5

#### 任务执行平面

只有在拆分后每个 TaskSpec 具有清晰 ownership 和低跨边依赖时，才启动多个写入 Worker。每个 Worker 必须：

- 使用独立 worktree；
- 独占自己的写集合；
- 针对一个冻结子 TaskSpec；
- 输出独立 commit；
- 不能直接合并其他 Worker 的 branch；
- 不能拥有 Publisher credential。

跨任务集成由单一 Integrator 或确定性 merge stage 执行，并在合并后重新运行 Verification；此前各分支证据不能直接证明集成结果。该设计既遵循 Marshal 现有“每个 worktree 最多一个写入者”，也符合 Co-Coder 的依赖感知切分证据和 Cognition 的 single-threaded writes 经验。citeturn15view2turn15view4

### 前置条件

引入 fan-out 前，建议完成以下基础设施：

| 前置条件 | 原因 |
|---|---|
| TaskSpec 可哈希、不可变、版本化 | 所有 Agent 必须围绕相同目标工作 |
| Repository dependency graph | 判断哪些工作可以安全并行 |
| 读写 capability manifest | 防止 Reviewer 或 Explorer 获得写权限 |
| Artifact store 与 content-addressing | 避免通过 conversation 传递大产物和丢失精度 |
| Evidence schema | 让不同 Verifier 的结果可合并、可摘要、可绑定 |
| Attempt、lease、heartbeat 模型 | 支持 crash recovery 和防止双写 |
| Model/tool/prompt provenance | 能解释不同 attempt 为何产生不同结果 |
| Fan-in policy engine | 防止 Lead 用自由文本绕过 gate |
| 基准任务集 | 比较串行与 fan-out 的质量、时间和成本 |
| 预算与终止条件 | 防止 Agent 数量、tool call 和 token 失控 |

Anthropic 的经验表明，若 Lead 没有明确目标、边界、output format 和 effort budget，Subagents 会重复工作或遗漏范围；其生产系统为不同复杂度设置显式 Agent 数和 tool-call 预算。citeturn14view0

### 协议分级

业界已经出现“从单 Agent 开始、按任务复杂度增加 Agent 和模型强度”的实践：Anthropic 使用明确的复杂度/Agent 数规则，AutoGen 文档建议能用单 Agent 时不使用 team，Codex 允许按角色选择更低成本模型和 reasoning effort。citeturn14view0turn0search11turn15view5

但截至本次调研，未发现一个跨厂商、成熟标准化的“Coding task risk tier → 固定 multi-agent protocol”规范；所谓成熟统一实践**未能验证**。Marshal 可以建立自己的分级：

| 协议级别 | 触发条件 | Worker | Review/Verification | 建议并发 |
|---|---|---|---|---:|
| Lean | 小改动、低风险、局部且可确定测试 | 单 Worker | 确定性 Verification；必要时单 Reviewer | 1 |
| Guarded | 中等改动、单模块、有清晰测试 | 单 Worker | 独立 Verifier + 1 个 clean-context Reviewer | 1–2 |
| Jury | 安全、认证、数据、并发、迁移等高风险变更 | 单 Worker | 独立 Verifier + 2–4 个异构 Reviewer | 2–4 read-only |
| Parallel-read | repository 大、需要广泛探索或测试 triage | 单 Worker | 多 Explorer/Test Agent，最终单 Writer | 2–5 read-only |
| Partitioned | 可证明低耦合的多个子任务 | 每 TaskSpec 单 Worker | 分支级验证 + 集成后全量重验 | 受 dependency graph 限制 |
| Critical | 发布、权限、secret、基础设施或不可逆迁移 | 单 Writer/Integrator | Jury + deterministic gate + human approval | 有界 |

是否升级协议可由五个维度评分：

```text
risk
decomposability
cross_partition_coupling
verification_observability
economic_value_of_latency
```

一个实用决策原则是：

```text
预期质量收益
+ 墙钟时间价值
>
额外模型与工具成本
+ 协调成本
+ 集成失败风险
+ 人工审阅负担
```

不需要真的把所有项精确货币化，但必须在每次 fan-out 后记录：

```text
single_agent_baseline_estimate
agent_count
wall_clock
model_tokens
tool_cost
verification_cost
merge_conflicts
review_findings
escaped_defects
human_minutes
```

否则系统只能看到“Agent 同时在工作”，无法判断多 Agent 是否真正提高了经济效率。

### 主要风险与缓解

| 风险 | 表现 | 缓解 |
|---|---|---|
| Correlated failure | 多 Reviewer 使用同一模型、相同 prompt，集体漏报 | 使用不同 role、prompt、model family 和工具 |
| Evidence poisoning | Worker 提交伪造日志或诱导 Reviewer | Verifier 从冻结 checkout 重新生成证据 |
| Lead capture | Lead 删掉或弱化不利 finding | Findings append-only；处置逐项记录；保留 dissent |
| Context dilution | Lead 被大量低质量 summary 淹没 | 结构化 schema、scope partition、artifact references |
| Integration invalidation | 各 branch 各自通过，但合并失败 | 集成后重新生成完整 evidence bundle |
| Token runaway | Lead 过度创建 Subagent 或无限追问 | 并发上限、token/tool budget、deadline 和 stopping rule |
| Credential inheritance | Child 继承 Parent 的写入/发布权限 | Capability 默认 deny；每角色显式最小权限 |
| Retry double effect | crash 后重复发 PR、评论、标签或部署 | 幂等 key、查询后写入、outbox/transaction pattern |
| Jury complacency | 多数一致被误认为事实 | 以可复现证据裁决，不以人数替代证明 |
| Human attention saturation | PR 数量增长快于审阅能力 | Work-in-progress limit、风险优先级和 review queue |

## 外部方案与 Marshal 模型对照

表中“部分”表示框架提供实现原语，但未强制满足 Marshal 的信任不变量；“未能验证”表示公开资料中没有确认该能力，而不是断言厂商内部绝对不存在。

| 方案 | 编排单位 | 状态持久化 | 证据机制 | 信任边界 | 失败处理 | 成本模型 |
|---|---|---|---|---|---|---|
| **LangGraph** | Node、edge、subgraph、shared state | Checkpoint per super-step；thread persistence；resume/time travel | State、trace、checkpoint；可保存 evidence reference，但不强制真实性 | Role/credential independence 需应用实现；禁止 Worker 自证未内建 | Checkpoint recovery；interrupt；副作用要求开发者幂等 | Agent/node/tool call 驱动；框架不自动做风险分级 citeturn1search1turn13view6turn1search4 |
| **CrewAI** | Flow、Crew、Task、Agent | Flow state；`@persist`；HITL resume | Structured output、guardrail、task result | Guardrail 不等于独立 Verifier；credential split 需外部实现 | Persist/restart；具体副作用幂等由应用负责 | 支持按 Flow/Crew 选择重量，但无统一风险协议 citeturn13view4turn10search3 |
| **AutoGen / AG2** | Message、GroupChat、Swarm、nested chat | 对话历史、component serialization、resume | Message log、summary、tool result | Agent role 是逻辑角色，非强制 authority boundary | 可 resume；durable side-effect 语义相对弱 | Agent 轮次和对话长度易膨胀；官方建议能单 Agent 则单 Agent citeturn0search11turn10search25 |
| **Microsoft Agent Framework + Durable** | Agent、workflow、concurrent/handoff/group chat | Durable Task checkpoint、session persistence、跨 host 恢复 | Workflow history、checkpoint | 独立验证与 evidence digest binding 仍需应用 policy | 强 durable execution、暂停、replay 和恢复 | 可配置并发和 workflow；无公开统一 risk tier citeturn5search5turn14view7 |
| **OpenAI Agents SDK** | Agent、tool、handoff、Run | Session、RunState serialization | Tracing、guardrail、tool result | 中间 handoff 默认不全受首尾 guardrail；credential split 非默认 | Pause/resume；与 Temporal 集成后可增强 durability | Code orchestration 更可预测；LLM orchestration 更灵活但成本不确定 citeturn12search23turn12search0turn12search1 |
| **OpenAI Responses Multi-agent** | Root、subagent、hosted action | Hosted run/session state | Agent messages、tool results、trace | Request tree 默认共享配置工具，不等价于隔离 Worker/Publisher credential | 支持 wait、interrupt 等；业务幂等仍需实现 | 默认并发 3；适合独立 bounded work citeturn6view2 |
| **Anthropic Research** | LeadResearcher、Subagent、CitationAgent | Memory、checkpoint、外部 artifacts | CitationAgent 做主张—来源映射；artifact reference | Citation authority 不等于代码 Verification authority | Retry、regular checkpoint、resume；同步 fan-in 有 straggler 问题 | 明确按复杂度配置 Agent 数；multi-agent 约 15× Chat token citeturn14view0 |
| **Claude Code Subagents / Teams** | Subagent session、team member、shared task list | Session/context；团队任务状态 | Summary、message、代码和测试产物 | 可限制 tools/permissions，但完整 authority separation 未能验证 | 独立 session 可隔离故障；精确幂等语义未公开 | 可按角色选模型；官方提示团队 token 成本较高 citeturn13view0turn13view1 |
| **OpenAI Codex** | Task、thread、subagent、worktree | Hosted task/session、workspace | Diff、test output、thread trace | Worktree/sandbox 隔离较强；child 权限可能继承 parent，需显式限制 | 任务隔离、retry；外部发布幂等需 orchestrator 实现 | 建议 read-heavy fan-out；可按 Agent 选择低成本模型/effort citeturn3search3turn15view5 |
| **Symphony** | Issue、Agent、workspace、PR | Issue tracker 作为 control plane；运行状态和 retry | PR、workspace result、task state | 人工 review 是主要外部边界；证据门禁未内建 | Retry/backoff、任务重派和并发控制 | Issue-level throughput；报告部分团队 PR +500%，缺少 per-task cost 数据 citeturn15view7turn15view8 |
| **Devin / Managed Devins** | Coordinator、child session、独立 VM | Managed session state | Child output、branch、PR、Coordinator summary | VM 隔离强于纯对话 Agent；Reviewer/Publisher authority 分离未能验证 | Managed restart/monitoring；业务幂等细节未公开 | ACU budget、child session limits；并行写入仍受协调限制 citeturn13view3turn4search10turn15view4 |
| **Google ADK** | Graph node、ParallelAgent、session | Session state、graph workflow state | `output_key`、shared state、tool artifact | Shared state 不建立独立 evidence authority | Workflow-level error handling；durability 取决于部署和后端 | 可确定性并行；统一风险分级未能验证 citeturn10search13turn10search9 |
| **Temporal** | Workflow、Activity、Signal、Event | Durable Event History、deterministic replay | Event History 是执行记录，不自动成为业务证据 | Worker/Reviewer/Publisher credential 可由 Activity worker 隔离，但需应用配置 | 本组最强；retry、resume、timer；Activity 应幂等 | 按 workflow/activity/resource 付出基础设施成本；不决定 Agent 数 citeturn11search1turn11search0turn5search1 |
| **Marshal Harness 目标模型** | Frozen TaskSpec、单写入 Worker、独立 Verification、ReviewDecision、Publisher、Outcome | CLI/state machine 为唯一权威；每阶段持久化；crash 后幂等恢复 | EvidenceBundle 与 ReviewDecision 绑定精确 digest；集成后重新验证 | Worker 不得自证；每 worktree 单写入者；Worker、Verifier、Reviewer、Publisher 分权 | 默认 fail-closed；lease、attempt、idempotency key、远端 CI observation | 按任务风险和可分解性升级：串行 → read fan-out → jury → 独立 TaskSpec 并行 |

综合比较，Marshal 当前不是在重复发明另一个 Agent conversation framework，而是在填补主流框架尚未系统覆盖的层次：**把 Agent orchestration 转化为可审计、可恢复、证据约束且权限分离的 software delivery protocol**。

最稳妥的演进路线是保留现有串行闭环作为可信基线，在其外围增加只读 fan-out；随后增加 patch 冻结后的独立 Reviewer jury；最后才对经过依赖分析、拥有明确 ownership 的 TaskSpec 做任务级并行。不要让 Lead 的语言综合成为新的隐式权威，也不要为了获得“多 Agent”标签而放松单写入者、独立验证、证据绑定和 credential separation。现有实证与生产经验均表明，Marshal 最可能获得净收益的路径不是“更多 Agent 一起写”，而是**更多独立视角发现信息，由更少、更受约束的主体执行写入和授权**。