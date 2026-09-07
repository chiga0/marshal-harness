# Agent Team 服务方案多轮审计

日期：2026-09-07。对象：[服务架构](agent-team-service-architecture.md)、[实施 Milestone](agent-team-service-milestones.md)、[ADR 0085](adr/0085-agent-team-service-contract-and-storage.md)。这是新产品合同的可行性审计，不是代码或生产认证。

本文按轮次保留当时结论；最新 Task-first 取舍与限定复核以[文末收缩审计](#task-first-收缩审计)为准。旧稿的 Workspace/安装管理/迁库与三 Provider 先行不再作为当前阶段前置。

## 1. 结论与证据边界

**方向可行，建议沿唯一应用/执行链收敛，而不是重建一个编排平台。** 一进程服务、可插拔 Agent/执行环境、单事务权威存储、持久问答/有限计划和独立验收能组成可实施的单用户 Agent Team。条件是：允许单 Worker、验收以实际下载成果为对象、未知执行结果不盲重试、迁移不产生双写，且产品收益由真实任务衡量。

文档审计只能发现合同矛盾和反例，不能证明三家 Agent 当前配置均兼容、恢复实现正确、旧库迁移已成功或多 Agent 比强 Lead＋SubAgents 更快。新 HTTP/SQLite/AskUser 仍是目标，不可据此将 B1/B2/B3 改成 PASSED。已确认方向不等于旧 RC1 获得 server/stable/production authority。

### 资料基线

- canonical remote：`https://github.com/chiga0/marshal-harness.git`；本轮只读核对 remote main 为 `ba2196bea33e6f007809f75f9671928c892bfa11`。
- 候选资料基线：`feat/b2-durable-materialization@ff71d7b732e42a90d20bec97f01e6c6fed8ba27f`；不冒充已合入 main。文档在独立 `feat/agent-team-service-blueprint` 工作区撰写，未修改产品工作区。
- 当前唯一完成状态与实际 canary 引用见 [Roadmap](roadmap-status.md#业务交付当前表)与 [B2 实机审计](audit-b2-first-team-2026-09-07.md)。最新记录仍无完整三节点独立 ACCEPTED/集成/GoalOutcome 实机闭环；不能把 service 的 Verify pass 和两个 start 当成团队成功。
- 阅读了 README、愿景、架构、生命周期、安全、Runtime 与相关 ADR，未使用 Marshal skill。未启动产品 Run、未迁移 `.marshal`、未执行新的付费 canary、未变更发布权限。
- 外部依据限于 [ACP 能力协商](https://agentclientprotocol.com/protocol/v1/initialization)、[SQLite WAL](https://www.sqlite.org/wal.html)/[一致备份](https://www.sqlite.org/backup.html)及 [PostgreSQL 事务](https://www.postgresql.org/docs/current/tutorial-transactions.html) 等一手资料。它们支持技术原语，不替 Marshal 提供实机证据。

## 2. 第一轮：盲审业务目标与独立实现取证

由两个不同子 Agent 在主笔写作期间并行完成，只读、不参与作者修改：

- `blind_business_audit`：只给用户产品要求，不提供推荐架构和实现，要求从零判断价值、失败前提和更简单替代方案。
- `implementation_feasibility_audit`：检查候选实际调用链/存储/协议，判断哪些可复用、哪些与新需求冲突。

盲审不可能无任何任务方向；这里的“无方向”指不提示推荐答案，不要求证明 Marshal 必然正确。随后另设反例角色主动攻击方案，而不是让作者重复确认自己的结论。

### 盲审发现与吸收

1. 并行拆分可能增加上下文丢失和集成成本：保留单 Worker/串行路径，拆分前判断收益，禁止每文件一任务。
2. Adapter 抽象不能消灭 Agent 的配置、取消、结果协议差异：最小组合能力逐个 conformance，增强能力不阻塞普通任务。
3. 确定性 Supervisor 不知道业务语义正确：原需求确认、独立 oracle/Review 和必要用户验收不可由进程状态代替。
4. DB 事务覆盖不了外部效果：命令未知先对账，取消确认前不派替身，历史 replay 不执行副作用。
5. 第二个模型可能共享错误假设：业务反例必须独立于作者，包含“测试绿但需求错误”。

### 实现取证

| 实际接缝 | 风险 | 方案处理 |
| --- | --- | --- |
| `internal/cli/sealed_application_darwin.go` 的 Pi0844 固定闭包 | 加 HTTP 壳仍被 CLI/品牌耦合 | 迁出唯一组合，注入中立 Port，CLI/HTTP 共用 |
| fixed AF_UNIX router 与 legacy `internal/server` 并存 | 误接旧 server/child CLI 可形成另一条运行链 | 只扩真实 fixed Application Port，兼容入口不进生产 |
| RB1/Run journal 与跨账本 proof | SQLite 不是附加索引或换驱动 | 明确替代物理提交/锁序，保留 Core recheck/唯一 successor |
| 旧 activation/SameSubject | 新安装路径不能直接接旧审批/在途 | 首迁移非终态先收口、历史只读，不补签/rebind |
| Pi no-skills/context、OpenCode skill/question deny | “使用本地配置”不能靠文档自动实现 | 显式 profile 迁移与原生配置/凭据边界测试 |
| 现有 Intervention 是交付后记录 | 不能充当持久运行中 AskUser | 新 Interaction/答案/消费与有界等待合同 |
| 旧 CLI child Supervisor 和 resident ticks | 双调度/双止损 | 唯一生产 resident loop |

## 3. 第二轮：对完整草案作反方向审计

上述两个 Agent 读取三份完整草案；增加 `adversarial_contract_audit`，专门用提交/崩溃/取消/重放/配置变化等事件序列攻击。三方均一次聚合意见，不逐条轮转返工。

合计发现 **6 项 P1、无 P0**，另有必要 P2/澄清；以下问题在同一批文档修订中处理。

| ID / 级别 | 具体反例 | 最小修正及位置 |
| --- | --- | --- |
| D1 / P1 | 集成工作区能跑，下载后缺忽略文件/依赖而不能启动 | 架构 §6、B1-A/B2-B：正式下载→新目录按声明依赖重建→原业务 oracle→成功 Outcome |
| D2 / P1 | 界面体验需要人工判断，但没答也被机器判成功 | 架构 §6/§7、ADR §5：批准时声明必答验收项，绑定候选/Evidence；拒绝/过期/陈旧答案不成功 |
| D3 / P1 | 新 SQLite 短事务与 ADR 0065 借用窗口内执行/双账本恢复同时有效 | ADR §1/§4：精确取代物理 proof/锁序/AST，事务 intent→锁外执行→事务重验接纳；保留权威与故障约束 |
| D4 / P1 | 旧 REVIEW_PENDING 迁入后新 binary 无权续审，却被重签放行 | ADR §3/§4：operator-local 只授权新 lineage；旧非终态先合法收口、历史只读，managed 后继 |
| D5 / P1 | 自带 Skill 调用同账户已登录 gh，绕开独立 Publisher | 架构 §4、ADR §2：执行环境不能取得 Publisher 凭据；不满足则配置支持阻塞，publication:none 不构成权限隔离 |
| D6 / P1 | 无 usage Provider 的终态只能伪造 actual=0 或永久不结算 | 架构 §10、ADR §1/§6、B1-B：enforced/observed 分离、unknown/source/coverage、保守 debit，未知不退款、不占假活 Worker |
| D7 / P2 | 历史迁移吞掉全部时间，迟迟不验证用户体验 | 全新且无旧 authority 仓库先试 SQLite 单任务/确认；旧库另验升级，禁止清空逃逸 |
| D8 / P2 | 比较前先人为写好完美合同，漏掉实际澄清成本 | 至少一组从相同简短需求起，计入规划/确认/人工准备，事前选择单/多 Worker |
| D9 / P2 | 架构图暗示 Sandbox 按 Agent 品牌驱动 | Execution 分别连接 Adapter 与 Sandbox，避免依赖方向倒置 |
| D10 / P2 | 原 allowNoChange=false 的无改动被 Goal 层洗成成功 | 同时要求原批准合同、allowNoChange/诊断与独立 no-change subject 接纳，不改写 Run ACCEPTED |
| D11 / 澄清 | Agent 收到答复、Core ack 前 crash 后重发 | 答复发送重查 fence；无原生幂等/查询则 unknown，停止/收集后才能关联继续 |
| D12 / 澄清 | 首次提交推进 revision，原请求重放被 stale CAS 拒绝 | 重新认证后先查精确幂等回执，未命中才新命令 CAS |
| D13 / 澄清 | “第二次签名才冻结”被当作允许原样再付费一次 | 结构性失败首次识别即不重试；第二次是预检/分类失效告警 |

这批修正直接进入同一个 ADR、架构和验收计划，没有增加六个协议项目或六轮代码 rework。

## 4. 第三轮：同一审计者复核修订

- 业务审计者确认原 2 项 P1、2 项 P2 已在合同层解决，未发现这些修订引入新的 P0/P1。
- 实现审计者确认原 3 项 P1、2 项 P2 已解决，限定范围内无新增 P0/P1。
- 反例审计者确认原预算 P1 及答复重放/幂等/结构性失败三项澄清已闭合，未发现新增 P0/P1；认为可进入实施，无需继续扩张协议。

第三轮是对实际改稿复核，不是增加第三个作者给自己的方案背书；所有判断只对应所读设计，不替代实现测试。

结果：6 项 P1 全部在设计层关闭，三方限定复核均无新增 P0/P1。最终保留的未决事项是下一节的真实实施/部署证明，不通过更多文档轮次假装消除。ADR 0085 仍标 Proposed，表示本分支方案待随变更接纳，而非等待再次进行无界审计。

## 5. 实施后必须证伪的假设

| 尚未证明 | 最短实证 | 失败时的选择 |
| --- | --- | --- |
| 一个用户无需手写 Run 就能拿到可用成果 | 新仓库一句需求→最小确认→单任务→下载重建验收 | 简化输入/计划，不先扩 Agent 矩阵 |
| 三家原生配置可可靠取结果/止损 | 相同核心合同 success/failure/cancel，各自独立观察 | 标明缺失能力；不以换模型/宽 parser 掩盖 |
| SQLite 迁移/恢复不双写 | 旧库静止导入、各切点 crash、旧 binary 禁写 | 保留旧库受控恢复，禁止自动清空/强迁移 |
| 有界团队有净价值 | 三任务族重复配对，完整失败/人工成本 | 串行/单 Worker，保留 Runtime 可恢复/审计价值，不承诺多 Agent 加速 |
| 用户问答不引起无限等待/越权 | 节点/全局暂停、取消竞态、ack 丢失、旧答案 | 非成功 Outcome 与明确人工出口，不复活终态 |
| 支持矩阵具备生产部署条件 | 同 bytes 安装、签名/平台、恢复与业务交付 | 只发布明示边界的 prerelease |

审计退出不是“所有理论风险消失”，而是主要合同冲突有明确解法、范围有限、每个承诺有真实路径验收，未知项不冒充已完成。下一步应进入 B1 的可运行纵切，不继续围绕文档无限细分。

## 6. 本轮文档验证

16 份新增/修改 Markdown 的新增本地链接检查通过（56 个目标/锚点），四份新文档代码围栏配对；完整 staged diff 的 `git diff --cached --check` 通过；新增内容约 81 KB 经 gitleaks stdin（redact）未发现 secret。没有修改代码、Schema 或现存运行状态，因此未运行 Go 全仓测试、实机 canary 或数据迁移；这些文档检查不构成产品验收证据。

## 后续专项复核：历史合同与当前设计

维护者要求重新检查旧 ADR/设计是否继续束缚新架构。该检查以 `abc3899` 为基线，不将前三轮“草案内部闭合”解释为整个历史文档集已一致。

两路只读审计发现：固定 Pi/55 materials、双账本 proof/AST、owner 构造形状、allocation projection、全部 UI/Goal 延期、旧 S1′/S2′顺序仍在当前正文使用强制措辞；0085 Proposed 与部分入口“冻结”冲突。问题不是需要删除安全规则，而是历史实现形状被当作长期产品合同。

一次成组修订：重写当前 architecture/runtime/implementation 三个入口、原文归档；19 份旧 ADR/候选与四份 Adapter 文档增加适用性，0085 扩展精确取代表；更新必读路线、README/vision/lifecycle/security、ADR 索引和 Roadmap。新增[合同适用性](design-contract-map.md)，把产品目标、ADR 接纳、runtime enable、实际成熟度分开；0085 保持 Proposed。

验收范围：检查旧 profile 未被放宽、新方案不会再被旧物理形状否定、历史证据未改写，检查相对链接/锚点/围栏/diff/secret；不运行产品或迁库。

两路原审计者对聚合修订复核均为 `P0=0/P1=0`：合同侧确认固定 Pi/跨账本/owner/allocation/transport 漏项已补齐，入口侧确认六类旧矛盾已关闭；未发现误删关键不变量。机械检查覆盖43份 Markdown，194 个新增/归档本地链接和15个锚点无缺失、代码围栏闭合；三份归档正文与 `abc3899` 原文逐字相同（仅标题/归档提示新增）。`git diff --check` 与 staged diff-check 通过，完整 staged diff（约424 KB，含归档）经 gitleaks stdin/redact 未发现 secret。该结果只关闭 `LEGACY-CONTRACT-AS-CURRENT-DESIGN` 的文档问题，不代表 ADR 接纳、数据库迁移或产品验收。

<a id="task-first-收缩审计"></a>

## Task-first 收缩审计（2026-09-07）

后继授权记录：用户在收到本轮最终方案后明确同意同步 AGENTS.md 导航并开始实施。现仅同步该文件顶部路线/目标说明/必读导航，其 universal 不变量及后续治理条款与修改前逐字相同；下述“本轮未改”描述此前设计审计快照，不再是待授权 blocker。

用户进一步要求删除 Workspace 的业务前置，把身份管理/显式初始化后置，先让 Agent Team 完成交付。本轮以 `68e5c8b` 文档与 `ff71d7b` 产品候选为基线收缩，不再把管理机制、全面迁库与品牌矩阵作为第一次演示的先决条件；原历史证据和失败分母保留。

### 实现依据与路线修正

只读检查确认现有 RepositorySession 已组合 owner、RB1 与 Run Store；`internal/cli/sealed_team_dispatch_darwin.go` 的初始团队物化/Start 具备两个节点的派发接缝，而完整自主 Collect/Verify/Decision/集成/成果消费仍待实机证明。`internal/planning/service.go` 的 Git 前置与 session repository 绑定不会仅因 SQLite 自动消失。

因此采用 B1 现有合法安装/唯一 Store＋一个 Provider 两实例＋真实 Git 业务交付，B2 同一应用接口后接 SQLite、零 Git/多仓库、简启动、问答和第二 Adapter，B3 正式故障/平台/发布验证。U1 历史导入独立；核心 API 稳定后才 UI。数据目录与内部 ID 保留，但不增加 Workspace/Project/资源注册对象。

### 一次聚合修订与限定复核

主笔为唯一写入者，两路独立只读审计分别检查业务/API 完整性与当前实现可接续性；未使用 Marshal skill。实际三稿的发现及处理如下：

| 发现 | 修订 | 原审计者限定复核 |
| --- | --- | --- |
| P1：将作者可达发布凭据的拒绝仅限定为启用发布时，可能弱化分权 | 即使 publication:none，作者可达 Publisher 凭据/已登录发布入口也不在支持范围；不用提示词充当权限隔离 | 已关闭；未发现新增 P0/P1 |
| P2：预上传输入尚无 Task，原所有权说明无法落地 | 本地调用者有界暂存，创建/更新 Task 时校验授权并原子绑定，孤儿到期清理；B1 可 inline context | 已关闭；未发现新增 P0/P1 |
| P2：原恢复失败可能导致 serve 退出，B1 不能承诺所有情况下在线 HTTP 查询 | 正常重启查询与恢复失败分开；后者停止新准入、保留原支持只读诊断，不伪称透明恢复 | 已关闭；未发现新增 P0/P1 |

反馈一次成组处理，由原审计者各做一次限定复核，不重启无边界文档审查。复核结论只适用于本轮文档；没有新产品测试或可用性证明。

### 保留边界与待验证项

- 三个用户对象 Task/Worker/Artifact，最小本地访问保护；身份/安装管理后置不等于开放无认证执行端口。
- 内置确定性 Supervisor、作者外独立验收、目录单写/进程归属、耐久事实与有限总预算保留；客观验收不强制额外 LLM。
- B1 是否真正并行并完成独立集成/下载消费、B2 零 Git/恢复/更多 Adapter、B3 可靠性及净效率收益仍需按 Milestone 实测。文档一致性不能替代这些出口。
- AGENTS.md 的目标导航同步被自动审批拒绝，本轮未修改该治理文件，也未通过其他路径规避；残留导航待明确授权，安全不变量不变。其他当前入口已同步，旧文档保留为历史作用域。
- 本轮没有启动/取消 Worker、改变 .marshal、接纳 ADR、合并或推送远端，也未提升任何产品成熟度。

### 本轮文档验证

检查本设计分支相对产品候选基线的 43 份 Markdown：228 个新增本地链接、25 个锚点引用与代码围栏。首次检查发现两个历史归档仍引用旧架构标题，已在当前架构对应职责节保留兼容锚点；不修改归档正文。Roadmap 历史段与本轮开始时逐字相同，三份架构归档和 AGENTS.md 本轮未改。工作区及基线完整 diff-check、gitleaks stdin/redact 均通过；只作文本检查，没有运行 Go/产品实机或宣称生产通过。
