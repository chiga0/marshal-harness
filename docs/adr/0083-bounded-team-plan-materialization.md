# ADR 0083：受限团队计划接纳与 Run 幂等物化

- 状态：提议（Proposed），仅未发布候选含接线，未在 main/正式产品启用。初始设计基线为 d67e3b7，B1 依赖现整合到 4ace42c；该 B1 候选的同 server 长 Verify/并行停止组合实机已通过，但尚未合入 main，不能作为 main 的能力声明。
- 依据：ADR 0019、0052、0069、0080、0082；目标为 B2，不恢复通用 M13/HA/多租户/DSL。

## 实现事实与要解决的缺口

2026-09-07 候选当前接线：批准→耐久输入/创建→首次 Start gate→resident tick 的两个 implement 调度已编写，失败沿同 RB1 停派；调度与 halt 的组合动态验证及真实并行尚未完成。Collect/独立验收/集成/Goal Outcome/暂停与 replan 仍待接通。以下实现段同时保留阶段演进，不应把较早的“尚无接线”或当前存在的代码当成正式启用、INTEGRATED 或完成证明。

`internal/goal.Evaluate` 能检查图、scope 和累计预算，但输入的 AuthorityState 由调用方提供，输出 reservation plan 不落盘；`internal/outbox` 为内存实现。把二者串起来不构成生产 Goal 接纳。`GoalNode` 也没有 TaskSpec/Policy 输入，无法从 node title 安全创建真实 Run。

`internal/planning.Plan` 接收完整 TaskSpec、Policy 和指定 RunID，完成已有单 Run 的准入、worktree 与 READY 创建，但并非可重放的 Goal 物化事务。不能在丢响应后换一个 RunID 再调用它。现有 Task `dependsOn` 只能指定 Run/Task 终态，既不传递成果，也不创建集成候选。

本合同只补这个真实缺口：同一个 fixed server 接纳用户批准的有界方案，耐久记录创建义务，复用单 Run 生产入口并完成业务集成交付。不得另起 Python 业务状态库、内存 controller 或 CLI 子进程协调器。

## 1. 确认的是完整可执行方案，不是 node 标题

2026-09-07 实机纠偏：Schema 已允许的 `work.context` 必须保留到 typed Task 并与 objective、constraints、nonGoals 一起进入固定 Pi 提示，不得在反序列化或启动构造时静默丢弃。所有文本仍通过 ADR 0075 原路径/控制路径/长度检查；HTTP 示例采用完整 loopback URL，不为样例放宽宿主绝对路径规则。固定 server 在原完整输入 preview 后、批准落账前，对所有节点（包括后继 integration）调用同一个纯 Pi launch builder，以确定性 Task/Run ID 和最大合法长度的占位 Attempt ID 检查提示与 argv 上限；不 Probe、预留或启动。这只前移确定性文本错误，不授予 Attempt 权威；真实 Start 仍用已预留身份重新构造并核验。旧 frozen Task/已封装 closure 不改写，新 binary 无法复用的旧 closure 仍拒绝漂移。

先通过只读 preview 返回：用户需求/非目标、确认后不得自动改变的业务验收、锁定 repository identity/base SHA、节点角色/scope/依赖、真实 Pi 配置、每节点和整个 Goal 的预算、集成策略与 publication:none。Planner 只能提交提案；服务器的确定性检查不能由 Planner 自称通过。

首个支持模板为两个独立实现节点加一个集成节点，初始三个节点、同时活跃 Implement Run 不超过三个，禁止 nested fan-out。后继节点/Run 必须计入整个 Goal 预先批准的累计预算，不能以这个并发上限代替累计上限。集成节点不是增加可插拔 Executor 类型，而是同一 Task/Run 机制中的明确业务角色。模板上限只限定首个受支持 profile，不把三节点样例通过泛化为任意 Agent Team。

新增封闭版本的计划输入束，绑定既有 GoalSpecRevision、GoalPlanProposal、每个节点的完整 TaskSpec/Policy 模板及其 canonical digest。请求总量和落账记录均有确定上限（初始总束不超过 512 KiB、节点模板各不超过 128 KiB），不接受任意路径引用、远端可变文档或 shell 模板插值。预算同时覆盖所有节点、后继 revision 和失败，不仅当前 fan-out。

输入束还必须包含整个 Goal 的 Guardrails 与 AdmissionPolicy，任何预算/准入配置变动都改变待批准摘要，不能等接纳时再由默认值或环境补齐。纯数据及确定性 ID 放在已有 `internal/goal`，planning 复用它完成 Task/Policy 预检，RB1 复用它落账；禁止让账本反向导入 planning（后者已通过 runstore 引用 RB1）。preview 可用空历史做初次可行性检查，但该结果没有 authority；接纳仍须以真实账本的历史和累计消耗重新执行 `goal.Evaluate`。

批准 operation 必须来自现有已认证 Public API 操作者，精确绑定 preview/输入束/Policy digest、expected Goal revision/head；请求中的 actor 文本、GitHub 评论、Worker 文本都不构成批准。ADR 0082 的 Issue 评论载体只用于 hosted B1 验证，不能复用为生产 Goal 审批数据库。批准不能授权绕过子 Run 独立验证或 publication 边界。

## 2. 接纳、预算与创建义务只有一个提交点

在现有 held owner 与 RB1 `result-ingress.jsonl` 追加封闭的 Goal 事务 fact；不增加第二权威文件。该 fact 持有完整输入束、accepted revision、同批 reserved reservation 与确定性 materialization command 集。它们由一次 compare-and-append/fsync 提交，并纳入原 replay/digest/unknown-outcome 规则；不借用“依次写三份 JSON”假装事务。

Goal 投影由同一物理账本 replay 得到，`goal.Evaluate` 只接收该投影和冻结 Policy，不接收客户端构造的 AuthorityState。调用顺序沿用 ADR 0019 的六项纯检查，随后验证批准并进行事务 CAS。拒绝只记录有界原因，不创建 live reservation、worktree、Run 或 Worker。

传输请求丢失或 fsync 结果不确定时，用原 request digest、Goal key 和 expected revision 在同一 held ledger 查询；命中精确提交则返回原结果，未决则保留未决，冲突则拒绝。不产生另一个 accepted revision 或另一个 materialization key。历史 fact 不改写；旧程序遇到未知 Goal fact 必须 fail closed，因此部署/回退必须按新 reader 支持范围管理，不能启动旧 binary 擦除新记录。

首个候选追加 `bounded-team-plan/v1` 的 `team-plan-accepted` fact：同一 record 保存 owner scope/fact 引用、完整 canonical 输入、认证批准 request/input digest、accepted revision 及每节点 TaskID/RunID、reservation 和模板摘要。`expectedHead` 为空仅表示初次创建；事务必须先从当前 RB1 replay 证明 Goal key 不存在，才可使用零历史预算。已有同 key 只允许 exact approval/input 重放；包括换 ProposalID 在内的异输入均拒绝，绝不能把累计消耗重新置零。后继 replan/settlement 未接通前，不开放更新操作。

生产批准 verifier 必须同时持有 current owner，并绑定已通过完整 Task/Policy preview 的不可变 bytes、真实认证操作者和当前请求；输入摘要自身不是批准。store 内再次检查 ledger 的 current owner，接纳和重放重新计算 revision/reservation/确定性物化义务，不信任已存派生字段。完整 record 的大小在 append 前检查，避免成功写入却无法重放；旧 reader 的未知 fact 拒绝规则保留。当前 store-only 测试中的显式 verifier/模板 fixture 不提供生产批准证据。fixed API producer、Run 创建恢复和集成接线未完成时，本候选不启用、不单独算 B2 INTEGRATED。

## 3. 物化是可恢复工作，不是接纳事务中的长操作

固定 CLI 的 ordinary-user activation 显式增加 `control-plane-team-approve` 与 `control-plane-team-reconcile`，分别只映射上述两个命令，不属于绕过 self identity 的 bootstrap。沿用 ADR 0081 的封闭集合升级规则：生成器、解码器、Schema、CLI 分类与顶层入口回归一同变更；旧 activation 不原地扩权，操作者须为精确新 binary/sourceHead 重新生成。未知命令、错误 profile、缺失授权与身份漂移仍在连接前拒绝。此进程准入不代替 fixed peer 认证、完整方案确认或 current-ledger 检查，也不授予发布权。

初始批准使用 `/v1/teams/approve`，只读原请求查询使用 `/v1/teams/reconcile-approval`，二者均在现有 fixed-peer 认证之后调用同一应用对象。批准 request ID 与 transport request key 相同；原 UTC 截止时间作为请求字段进入 request digest，批准时与认证 transport deadline 完全相同，过期后不得重新写入。查询可使用新的查询 deadline，但携带原批准完整请求，不改变其 ID/截止时间/输入。结果丢失时不在 router 自动重放写入；读取原账本返回 exact fact 或明确不存在，未知/损坏不解释为不存在。

该操作只有单个原子 RB1 提交点，批准事实本身就是 durable receipt，不再复制 StartRun 的跨步骤 pending 文件。服务端返回前从当前 owner 下重读 exact fact；客户端在固定 peer/owner post-check 后，从 held read-only RB1 重放并比较相同请求/投影，缺失、伪造或 owner 漂移均拒绝。查询“不存在”同样须读账本确认，不能仅相信空响应。此入口只批准创建义务，不自动物化/Start；普通用户批准亦不授予子 Run 自我验证或发布权。

固定 server 的构造入口安装纯 `TeamInputPreflight`：复用 planning 的完整 Schema/Policy 预检，并绑定该进程的 canonical repository 与 authority namespace。请求不能提供或替换这个函数；缺失配置时团队批准关闭，既有单 Run 接口不受影响。批准请求封闭绑定协议版本、request ID、完整 canonical 输入和用户确认摘要；session 在自己的 lifetime guard 内先验证不可变副本，再用私有 verifier 持有真实 owner 锁、重查 held root 和 RB1 owner，调用同一原子接纳事务。不把可反序列化的批准标志或外部传入 verifier 当作认证。

该 session 方法本身是特权应用接缝，不提供 transport 身份认证；候选现由上述 authenticated route 调用，固定 CLI 提供 `team-approve` / `team-reconcile`。生产版本启用仍须本候选的实际链路验证，不能以存在 route 宣称团队可用。返回投影只表示计划已批准及创建义务数，不表示 Run 已创建、Worker 已执行或业务 ACCEPTED。session 冷重开后 exact request 返回原 fact，非 exact request 冲突；拒绝与取消不能追加批准。

接纳返回后，现有 fixed server reconcile 循环按依赖、scope、宿主/Provider 和验证容量取就绪节点。创建和 Start 不占用 Goal/RB1 锁执行长命令。每个节点的 materialization key、TaskID、RunID 在 preview 前从版本域、authority namespace、GoalID、ProposalID 和 NodeID 的 canonical tuple 确定性派生，随后由 accepted fact 绑定；不能从 accepted fact digest 再推导输入中已有的 ID，否则形成 `fact→RunID→Policy digest→bundle digest→fact` 循环。相同 key 的不同输入是冲突，不自动产生另一 Run；新方案必须换 ProposalID 并重新批准。换 server/丢响应/暂停恢复不能换 key。

首个 resident dispatch tick 每次最多物化并 Start 一个 dependency-free implement，顺序启动允许两个真实 Worker 在执行阶段重叠。初始 profile 的自动调度按仓库 busy 上限 2 判断容量，同时遵守原 Goal 更低上限；这不新增人工 Start 接口的全局配额。RUNNING、重试待定、VERIFYING/REVIEW_PENDING、rework 和发布中均占容量，不能把审核排队当作空槽。现有非团队 busy Run 或同时存在多个 busy Goal 时不增加派发，避免跨方案 scope 冲突；单个 busy Goal 优先续行其已批准的无冲突节点。该固定上限不宣称具备自适应内存/CPU 扩容能力。

候选列表来自同 owner 下的 RB1 计划/停派投影与 descriptor-bound Run authority，不来自目录存在或进程 PID。只接受未创建或原首次 READY；不自动重试/rework，不提前创建 integration。选中后仍由原物化入口重查计划，并用实际 Inspect 的 sequence/head 调用同一 StartRun；不以调度输入结构授予执行权。调度与公开 mutation 共用 writer lane，运行中的 Worker 不持该锁。截止处理使用独立 timer，团队控制失败不得停止已有 Run 的 deadline 处理。单次失败落原 halt；查询/停派提交未决时本进程停派且报告，保留现有 Run/Outcome，不循环猜测重试。

先耐久绑定该命令的最终 TaskSpec/Policy digest、repository/base、选定 Provider profile 和目标 RunID，再调用同一生产 planning seam。开始前和返回后均重查已有 Run 的冻结输入与事实：精确 READY 复用；CREATED/PLANNED 只沿同一创建义务补齐；冲突/无法判定则保留明确阻塞，不删除旧目录、重选新 ID 或绕过准入。需将现有 `planning.Plan` 的创建步骤补为可恢复入口，而不是宣称它目前已经幂等。

候选将单 Run 入口分成 `planning.Prepare → PreparedPlan.Create`，原 `Plan` 也使用同一路径。Prepare 完成完整校验/准入/锁定 base/能力选择，不创建 Run 或 worktree；但原有 precondition、解释器预检和 probe 仍可能执行，不冒充纯函数。其私有进程内句柄保留完整 canonical Task/Policy/Capability，提供独立副本供 Core 绑定创建义务；Create 不再次解析可变 base ref 或 probe，创建前重查 repository/remote 与适配器身份。该句柄不是可反序列化的批准或冷恢复凭据，现阶段仍拒绝已有 Run。耐久创建绑定与 CREATED/PLANNED/READY 重放尚待接通，不把这个阶段分离算作幂等物化完成。

实现节点的首次冻结沿同一 RB1 追加 `bounded-team-run-creation/v1 / team-run-inputs-frozen`，引用当前 owner fact、原 approved plan fact、Goal/Node/Run ID，并保存完整 canonical PreparedInputs 及摘要。事务从已接纳计划重算绑定：只接受无上游的 implement 节点，Task/Policy 必须与已批准模板逐字 canonical 相同，repository/base/确定性 ID 相同，仅一次 Pi selected（无 fallback）；Capability 仍须由实际 Prepare 完成完整 Schema/环境与 probe 校验。集成节点不能借此直接使用原 base 创建；须待上游成果接纳及集成派生算法另行接通。第一次冻结原子提交；exact 输入重放返回原 fact，不增加 reservation；同 key 异 capability、时间或正文均冲突。冷恢复先读原冻结值而不是再 probe/刷新时间；冻结事实不表示 Run 已创建、READY 或 Start。此 record 不新增第二状态库或放宽旧 reader 的未知 fact 拒绝行为。

fixed server 构造时安装不可由请求替换的 `TeamRunPreparer`，使用该 server 已冻结的 Pi runtime/entrypoint 构造既有 production selector，调用实际 Prepare；不在 reconcile 时从 PATH/环境重新发现 Provider。RepositorySession 先持 current owner 核对原批准及已有冻结事实；命中即原值返回，未命中才在 owner/RB1 锁外执行 Prepare，提交时再次验证 current owner 和原计划。此特权应用接缝目前不暴露新 HTTP 操作，也不 Create/Start；下一阶段须用原冻结值恢复创建并补 Goal→Run approval，不能依靠重新 probe 来“恢复”。

批准已提交但创建输入尚未冻结的窗口，由 resident controller 使用 `MaterializeApprovedInitialTeamRun(GoalID, NodeID, PlanFactDigest)` 继续；这些参数只用于定位，不能充当批准。session 在 current owner 下读取原完整计划与批准 digest，再复用同一个 preflight/Prepare/Freeze/创建恢复入口；不要求客户端持有原 HTTP 请求，不伪造其 request ID 或延长批准 deadline。首次未冻结节点允许执行原 Prepare，已冻结节点只读原结果，不再 Probe。该单次内部操作不负责调度、不自动重试、不创建上游尚未满足的集成节点；resident 的容量/失败止损与自动 Start 尚待接通。既有启动恢复仍只修补已冻结义务，不借启动扫描无条件扩容。

冷读取的 PreparedInputs 必须通过 `planning.RestorePrepared` 重新执行同一完整 Task/Policy/环境、precondition、解释器、repository/base/remote 和能力 Schema 校验；唯一不同是 production selector 重新核对 registry eligibility/admission 后复用原单候选能力快照，不 Probe、不 fallback、不刷新时间。受限团队模板必须有非空 expectedRemoteUrl，否则冷恢复无法证明原 remote 名字仍指向原目标；该缺口在批准 preview 前拒绝，不等付费 Worker 开始。恢复函数自身不验证账本或授予批准，controller 必须从当前 owner/RB1 取原事实并在写入前复查；不能把客户端 PreparedInputs 当作 receipt。候选现通过 `RepositorySession.MaterializeInitialTeamRun` 读取原事实并安装 current-owner/fact 写入 guard；固定构造器恢复原 PreparedPlan 后调用 ReconcileCreation，返回前重读会话持有的 RunStore、精确状态及三份冻结文件。尚无团队 HTTP 物化/Start、Core plan approval 或完整实机团队证据。

计划批准向子 Run 的 plan approval 映射是显式 Core producer：必须绑定 accepted Goal fact、节点最终输入和当前 Policy，只授权该一个 Run 的执行。不能生成通用 actor 批准文件或扩大用户确认范围。保留原 Run/Attempt reservation 与 dispatch lookup-before-claim；Goal reservation 记录预算归属，不替代它们或重复扣费。每条物化事实引用精确 Run 创建/Start 事实，恢复先核对再提交 committed；失败/终态的 release/settle 沿 ADR 0019，不凭本地进程状态释放预算。

初始 implement 的 plan gate 直接由 Core 在 current owner 下读取同账本批准与创建 fact，并核对原 READY sequence/head、原准备时间、Task/Policy/Capability bytes 与摘要。它是仅适用于原首次 READY 的批准派生检查，不新增 ApprovalRecord、human actor 或第二批准状态库；客户端布尔值不能代替它。团队成员发生任何冲突均拒绝，不回退到普通人工审批；非团队 Run 继续既有单 Run plan gate。实际 Start 仍使用同一个携带 expected sequence/head 的生产入口，后续 reservation/launch 再查 current owner 与 Run CAS。旧团队批准不授权后继 READY/rework、发布或扩大预算；后继必须由已批准的新计划规则接纳。

## 4. 成果集成是冻结方案的一部分

### 创建恢复的受限写入规则（未启用候选）

物化通过原 PreparedPlan 的专用恢复入口处理，要求 controller 在 Run lease 创建及每批文件/journal/snapshot 写入时持 current owner 并重查同一 RB1 创建 fact。Git 操作在 owner 锁外、Run lease 与 task flock 内执行；owner 漂移后只能留下待诊断的原工作区，不能提交 READY。恢复不 Probe、不启动 Worker，也不生成新 Run/Attempt。

fixed server 必须在冻结 StateRoot identity 前准备 runs/locks/worktrees 容器，后续仅创建其子项，避免合法物化改变已冻结父目录身份。Run 恢复读取采用有界 descriptor-relative nofollow/nonblock 普通文件检查；FIFO 不得在文件类型检查前阻塞。原事件与输入文件不重写；快照只是对已验证 journal 的完整投影补齐。

resident 启动先从 held RB1 重放当前 repository scope 的既有冻结创建义务，再进入普通 Run-start authority 扫描。恢复只使用账本中原 approved plan/creation，不能伪造原 HTTP request ID/deadline、重新批准或给未冻结节点重新 Probe。每项重新检查 current owner 和原两份 fact；不完整的原创建沿同一物化入口补齐。已超过两条创建事件的 Run 只有在完整 Run authority、原创建身份和冻结文件均相符时才留给既有执行恢复，不回滚或重新物化；冲突/占用/损坏不得静默跳过。该枚举不增加持久化协议或第二状态库，也不在 startup 自动 Start。

只接纳零至两条精确的既有 planning 事件；每条除原随机 event ID 外，类型、状态边、actor、原时间与完整 payload 都须与原准备一致。快照必须等于这些事件对应的完整投影，不允许用“重建快照”掩盖冲突；缺失或合法落后才补齐，截断 journal 和已运行状态拒绝。三份冻结文件使用现有 descriptor-relative immutable writer，存在值必须逐字相等、单链接普通文件；缺失才安装，不覆盖现有异内容。

创建 worktree 使用原 task/run 派生路径与分支。恢复前必须拿到 task flock，验证同 Git common directory、精确分支/原 HEAD、干净工作区；只允许接管已释放进程锁留下的 `managed by Marshal` Git 管理锁，不处理其他原因的锁。只有分支已创建而目录尚未出现时，允许精确原 base 的未占用分支继续 `worktree add`；任何冲突保留，不强制删除或 reset。该恢复入口是内部 composition 接缝，不能直接接收 HTTP 请求宣称它自带批准。

两个实现节点共享锁定原始 base、使用独立 worktree。每个成果仍经过既有 Collect、独立 Verify 与精确 Decision；集成只消费已接纳的精确 candidate/base/patch digest，不读“最新分支”、未审 scratchpad 或其他 worktree 的可变文件。不得用子 Run 数量或单元测试通过替代最终验收。

集成准备的只读入口从 current owner 的原计划/创建事实定位两个实现 Run，同时持有它们的 descriptor-bound lease，重放 ACCEPTED 的最终 review.accept 事件并核对其 Decision digest。沿原 DecisionImporter 校验 Task、packet、report、manifest、Decision 和 local applicability；Outcome 必须等于原 Core producer 以该终态事件生成的内容。候选记录的 detached digest、Run/Attempt/namespace/base 及 patch content digest 必须全部相等。返回的 bytes 是这个时点的已接纳输入快照，不是可反序列化批准、跨宿主导入凭据或集成创建授权；在后继耐久冻结/Start 前仍须重查原事实和输入。未接纳返回未就绪；缺失/冲突不解释为可重跑。此入口不创建 Run、预算或第二审批状态库。

依赖就绪后，Core 在尚未交给 Worker 的专属集成 worktree 按固定节点顺序应用这两个精确 patch，生成可复算的候选 tree/commit，并落账绑定上游候选及原 base。冲突是 integration-blocked，不隐式改需求、选 theirs/ours 或启动无预算修复。此操作只产生本地候选，不 push/merge，不属于 Publisher。

集成 Task 的业务要求、scope、model、预算、oracle 和权限来自已批准模板；唯一允许派生的字段为已批准算法生成的输入 base/上游绑定及确定性身份。最终 Task bytes 在 Run 创建前耐久冻结。其他字段变化必须新 proposal/批准。集成 Agent 在这个 base 上检查/修复组合行为，独立 oracle 从实际客户端发起 HTTP 请求并验证服务响应，而不是重新计算本地答案。

最终交付绑定集成 candidate、全部上游 candidate 与独立证据/Decision。首个 publication:none 返回可获取的候选与说明，不自动 merge，也不把 Goal 完成等同部署或正式版本发布。

## 5. 有界暂停、局部重规划与失败

初始自动调度前增加同 RB1 的 `bounded-team-halt/v1 / team-plan-halted` 事实：精确引用原计划、出错节点与封闭阶段（`prepare`、`materialize`、`start`、`inspect`），由持 current owner 的 Core 追加。首次调度失败后原计划停止派发；后续 tick、冷重开和手动内部物化均先读该事实，不再次启动相同节点或继续扩散。它只撤销后续派发资格，不宣称 Run 已终止、不释放预算、不替代 Run Outcome，也不杀已有 Worker；现有 deadline/Collect/恢复仍须履行。重复同事实只返回原摘要，不重复追加；不同原因不能覆盖首个失败。恢复派发必须走后续显式 replan/重新确认，不能删除 halt。若 halt 无法耐久提交，controller 必须停止本进程团队派发并报告未决，不能继续循环调用。

用户等待/预算/依赖阻塞在 Goal 上有 typed reason；初始暂停采用 drain-active，不发新节点，既有 Run 仍受其原 deadline 限制。需要取消时只调用 B1 的合法 cancel/terminal reconciliation，不直接 kill。恢复重查 current owner、输入适用性与预算，不延长已冻结 Run 的 deadline。

重规划仅 supersede 未运行节点；已经运行或完成的节点保持不可变，修改其成果通过有预算的新节点/Run 表达。只有依赖变化的后继失效，无关已接纳成果继续复用。不能偷偷增加 Run/rework 预算。一次局部失败保留 Outcome、原因和消耗；结构性原因先修 preflight，不按原输入反复 fan-out。

## 6. 一条业务链的实施与退出

实现以“两个实现任务→一个真实集成候选”为一个纵切，一次接通：输入 preview/批准→RB1 原子接纳→Run 创建恢复→单 Run Start/Collect/Verify/Decision→集成→Goal Outcome。纯 Goal 类型、独立 store 或 mock controller 不能单独标 B2 INTEGRATED。

同一调用链必须覆盖：无批准零创建；同输入重放/同 key 异输入拒绝；accepted fact 前后丢响应；Run 创建和 READY 之间中断；Start 丢响应不多 Attempt；两个独立写节点真正重叠执行；上游漂移或集成冲突拒绝；局部 replan 不重做无关成果；Goal 暂停/重启后继续；独立业务验收与最终 Decision。测试先无故障、无重启走通，然后再注入故障，不能只验证恢复分支。

当前实现候选首先在既有 `internal/planning` 增加完整输入预检：封闭版本与大小、重复/未知 JSON 字段、Spec/Proposal digest、两实现加一集成图、确定性身份、Task schema、既有 Policy 验证、scope/锁定 base、显式 Pi/model、publication:none、禁止子 Worker fan-out 和声明预算不低于 Task。输出仅是 canonical preview，不是批准、预留、current-ledger verdict 或 Run。全局 scope 冲突、累计预算、批准及 CAS 必须在耐久接纳时依据账本再判定；生产 endpoint、物化与真实团队链尚未接通，不将这段预检单独计为 B2 INTEGRATED。

先在订单报价 API/客户端样例上验证机制，再在至少另外两个业务任务族进行重复配对比较。统计所有失败与修复、总交付时间、人工介入及实际成果复用；若不优于强 Lead＋SubAgents，简化策略或保留 Runtime-only 价值，不以增加协议来解释失败。B3 的同路径长期故障、安装信任和 stable 门禁仍须完成，本 ADR 不授予 production。
