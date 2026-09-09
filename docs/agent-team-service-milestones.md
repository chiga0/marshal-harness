# Marshal Agent Team：Task-first 实施 Milestone

2026-09-09：[ADR0095](adr/0095-node-managed-leader-contract.md)及[Leader 机器合同](node-leader-execution-contract.md)已独立审查接纳。B2-L 保持 DESIGN，接下来同链实现并验收，不以新增合同代替完整 Leader 业务交付；既有 B1/API-STABLE 的限定范围不变。

更新：2026-09-09。最终方案见[服务架构](agent-team-service-architecture.md)，合同变化集中在 [ADR 0085](adr/0085-agent-team-service-contract-and-storage.md)（Accepted）。合同接受允许实施，不表示实现或生产可用；实际事实仍只记 [Roadmap](roadmap-status.md#业务交付当前表)。

**当前检查点**：API-STABLE 四项出口已独立核验通过，范围为当前 Node profile、25操作/58 Schema和同包客户端。Pi/custody问答交付与同配置取消均有实机，原API反例/客户端/响应上界与事件续读通过；正式版本、真实局部修正及完整平台恢复/发行不随之通过。B3 冷备份恢复、多Task隔离、v5许可前恢复和事件洪泛已有有界证据，尚未完成。[精确证据与剩余出口](node-task-service-status-2026-09-08.md)。以下带时间的增量和Node形态试验保留历史，不作为当前待办。

2026-09-09 16:42 CST 增量：ADR0093新v6的单Worker取消经独立聚合审查、实际HTTP/SQLite/原CLI故障与真实Pi验收，已本地合入`25ff8315`；仅取消目标、保留原兄弟候选与冷开原回执，旧格式仍501，不算Task交付或完整Leader。一个合法大计划读取限额P1已根因修复并固化28/48节点回归；B2该控制子项闭合，B2-L/真实模型修正/正式发布仍开放。B3新增有限EFBIG/SQLite写失败与恢复后接单证据，不冒充ENOSPC或长期运行。

**2026-09-09 当前目标调整**：[ADR0094](adr/0094-trusted-single-user-role-team.md) 设计已接受。[受管 Leader](node-leader-execution-design.md) 必须全程按业务义务唤起，不是现有 Planner 组合标签；Supervisor 观察/聚合/通知，Core 校验/硬规则/已批准调度，Execution 所属 handle 操作。当前混合实现需渐进接线。B1 经本轮独立七条件复核为可信单用户本机 PoC `PASSED`，旧 non-production 不重标；B2/B3 仍 `IN_PROGRESS`，新增 `B2-L / DESIGN` 为正式发布前必验。强 OS/凭据隔离后置，不放松独立证据、授权、预算/恢复。文档无 API/角色/格式修改或迁移，API-STABLE 原范围保留；未来新终态/副作用等机器语义必须明确兼容。

**Node 形态可行性检查点已通过（不升级生产阶段）**：[ADR 0087](adr/0087-node-local-team-feasibility-probe.md) 的独立 Node-only profile 在本机完成真实双 Pi→HTTP 活跃重启→69 项独立验收→下载再验收，以及第二任务取消/重启保留。它解决本机此次必须执行 Marshal 原生文件的实验障碍，不代替下面 ADR 0085 的完整 B1/B2/B3 合同。下一步优先决定正式 Node profile 的合同与迁移边界，然后推进局部修正、第二 Provider 和完整恢复；不先全量翻译旧 Core。证据、失败与限制见[实机记录](node-team-feasibility-2026-09-08.md)。

## 1. 三个用户出口，不再把平台准备当交付

**2026-09-09 12:59 CST 主线增量**：`42426a2`规划输出类型提示修复正常合入并推送`69cfacee2fbea366174744167d5a2c3a3dfeee89`，原reviewer复审、独立24项通过；该候选真实Pi运行问答与双作者交付29.624秒通过，正常重开回执/制品不变。前次规划scope对象不符合Core数组合同的真实失败保留；不自动改写结果或重试。API响应上界/事件续读两项已通过，上一源0f86804两平台各510项全过。下一步收口API证据与同版本完整快照新目录恢复，不重做已通过功能，[精确证据](node-task-service-status-2026-09-08.md)。阶段状态仍按原退出条件判断。

**2026-09-09 12:39 CST 主线增量**：输入审计与简启动已独立审查、最终组合29/29通过，source=`fe04a8a`正常合入并推送`ee3932ffc135f89ce0395f159696f93c6684da1d`；`pendingRemoteSync=false`。原生输入正文默认不保留，显式受信策略才披露；handed-off不等于模型消费，简启动仍需显式配置。下一项为API-STABLE响应上界和事件续读，不重开接口平台或UI。真实修正/分权/完整恢复和发行仍待验收，[精确证据与失败](node-task-service-status-2026-09-08.md)。

**2026-09-09 12:20 CST 主线同步**：已审运行问答和同计划局部修正 source `8f8ff8d` 正常合入并推送 `85fc62a3338fc15c408e941e16a7c882b0be4a6d`，`pendingRemoteSync=false`；精确 source 的 Ubuntu/macOS Node team CI 均通过。实际输入审计、简启动仍在独立实施，OpenAPI 为candidate，不提升阶段状态。此前“待合入/权限阻塞”记录仅代表对应历史时点。[精确范围](node-task-service-status-2026-09-08.md)。

**2026-09-09 12:04 CST 增量**：同计划局部修正完整实现经一次聚合 rework、原独立 reviewer 复审，两项P1关闭；最终集成定向80/80通过。功能分支 `d650238` 真实Pi在修正启用配置下首次完整交付26.288秒、双作者交叠11.788秒、下载7单/1850 cents、正常重开无重复；没有实际触发repair，不把首通冒充修正实机成功。main权限仍阻塞；下一步并行补实际prompt/context审计与原服务简启动。B1/B2/API-STABLE/B3退出条件和完成状态不变，[精确证据](node-task-service-status-2026-09-08.md)。

**2026-09-09 11:22 CST 增量**：`1b3b7d6` 真实 Pi 运行中问答→原 Worker 继续→双作者完整独立验收→下载再验收→同版本正常重开通过，50.938秒、作者交叠18.979秒、重复启动0。首次实机失败后，经原 SDK 确定性复现原生复合 toolCallId 兼容缺陷，修复及独立审查后才重试；保留一失败一成功，不称首轮成功。关闭 B2 运行中问答的限定业务实机子条件；局部修正按已接受 ADR0091 实施，实际分权、完整恢复和 stable 不升级。[精确证据与限制](node-task-service-status-2026-09-08.md)。

**2026-09-09 10:23 CST 增量**：真实 Pi＋Qwen 已在两个锁定仓库并行修改、形成 patch、独立验收/下载应用并正常重开；56.914秒、交叠11.631秒、无模型重试。已正常推送 `7294878`。这关闭 B2 真实混合 Git 交付的有界子条件，不关闭通用规划、运行中问答/局部修正、完整故障矩阵或 Worker/Publisher 分权。新增问答 API/客户端仅证明合同，不冒充原 Worker 已消费答案。[证据](node-task-service-status-2026-09-08.md)。

**2026-09-09 10:12 CST 当前检查点**：sourceHead=`db57d45c7b8a951eb3adddafd2f649ad165cd068`，localMergeSha=`0057dd1b7c5b259f238600bacbc3abb346d770ca`；正常推送后远端 main 已核对为同一 SHA，产品 `pendingRemoteSync=false`。最终完整 Node 组合 **418/418 PASS、319.688秒、零失败/取消/跳过**，运行前后冻结树不变且 clean；日志 SHA-256=`47b7d7ceb5c01fa5611ab93e2fc5f3c08ea4c51d8672de6f3a8204aaecd7d63f`。 本轮关闭限定服务崩溃后 cleanup-only 恢复/再接单、安装目录包团队闭环、Git 多仓库 patch 交付的实现与确定性证据缺口；不是全平台、真实模型恢复或 Worker/Publisher 分权完成。原409项/1失败的 Pi 回归已定位修复，失败分母保留。下一完整纵切按已接纳 [ADR 0090](adr/0090-node-runtime-business-questions.md) 并行实现运行中问答 Core/Pi 与 API/client，另一方向验证真实 Pi＋Qwen Git 团队。B1/B2/API-STABLE/B3退出条件不变，见[实施记录](node-task-service-status-2026-09-08.md)。下述时间记录仅保留历史。

**22:42 CST 同版本恢复增量**：`32b353f` 已将 `2f6c28f` 的真实 create/result COMMIT 前后故障测试正常合入并推送；独立组合9/9通过。它证明提交前后全有或全无及原成果重开，不表示活跃执行已能自动恢复接单。下一项是原 Runtime 清理观察补强与 [ADR 0089](adr/0089-node-execution-custody-and-cleanup-recovery.md) 的跨代 cleanup-only 收口；不新增品牌或 UI 回避此 B2 缺口。真实必要问答与 Pi 第二团队已推进，当前表不再将它们列为“尚未实机”，完整 B1/B2/API-STABLE/B3 状态仍不提升。

**22:22 CST 整合已同步**：产品main/origin/main=`8d48d94`，source=`ce2879f`；Pi+Qwen问答组合384项通过，随后新增两项真实service crash测试通过并合入。当前只关闭对应接线/证据子条件，不提升B1/B2/API-STABLE。优先补活跃执行crash后跨代清理收口与create/result原子性故障组合；权限分离、局部修正与正式部署/发行继续保留。ECS传输被本机SIGKILL，未运行服务。详见[同步及未完成项](node-task-service-status-2026-09-08.md)。

Pi候选`74a239e`的完整Node回归已由非作者独立完成：374/374通过、182.405秒、零跳过；与真实Pi团队证据共同推进第二Adapter子条件，尚未合main，不关闭B2/API-STABLE。

**22:07 CST Pi实机增量**：候选`74a239e`真实Pi HTTP planner→双作者→独立验收→下载/正常重开一次通过，37.064秒；推进第二Adapter真实团队子条件。尚未合main，完整回归中。下一项优先是已定位的活跃执行crash后收口缺口及同链故障测试，不以第二品牌成功关闭B2/API-STABLE，详见[当前记录](node-task-service-status-2026-09-08.md)。

授权后更新：用户已批准研发整合及限定ECS上传；Pi独立分支整合正常获批，root regional合并仍因AGENTS作用域被拒绝。ECS目录创建后scp异常exit137，无归档/state或服务运行；不是部署成功，详见当前检查点。

**22:00 CST 实机增量**：候选`538c534`一次完成真实Qwen日期必要问答→精确批准→双作者→独立验收→下载消费，21.440秒、作者交叠14.828秒，正常实例重开无重复；完整Node组合347/347通过。推进B2必要问答业务子条件，不代表任意自然语言澄清或完整恢复。Pi原P1已关闭但正式模型团队尚未执行；研发合并与香港服务包上传分别遇到安全审批拒绝，未绕过。main仍为已推送`61a19e9`，候选未合入/推送；B1/B2和API-STABLE/B3完成状态不提升，详见[精确证据及失败记录](node-task-service-status-2026-09-08.md)。

**21:21 CST 实施增量**：产品 main/origin/main=`6a2df5e` 已正常推送；正式批准前有限问答贯通 HTTP/SQLite/精确预览确认，337项完整 Node 组合通过，25文件发行包核验通过。B1 七项功能条件已有范围明确的实机/负例证据，剩余适用 profile 的 Worker/Publisher 权限分离证明；不把第二 Adapter 或完整 B3 矩阵加入 B1 前置。B2 实际必要问答业务、Pi 第二 Adapter、局部修正和完整同版本恢复仍在途；API-STABLE/B3 未完成。此增量取代下文相应“正在准备/尚未问答接线”的历史状态，不更改退出条件。精确证据见[当前检查点](node-task-service-status-2026-09-08.md)。

**20:16 CST实机增量**：正式Node源码 `42f9565` 的真实Qwen纯HTTP团队已完成一次批准、双作者重叠16.799秒、独立验收、下载消费和正常重启回执/成果保持，全程32.620秒。该结果推进B1真实同链条件；运行中取消、权限分离及其余B1/B2/B3出口未被该一次成功替代。当前下一步为B2有限问答、Pi第二Adapter和实际安装恢复，B1/B2状态不自动提升。详见[精确实机证据](node-task-service-status-2026-09-08.md)。

最新实施检查点：ADR0088 已接受，正式 HTTP/SQLite/Application/ACP/Supervisor/独立验收/最终交付/服务入口已合入并推送至 `82b64cf`。全链真实Node进程夹具及307项组合测试通过，远端Node team CI通过；真实Qwen原生工具单会话通过不等于正式团队验收。当前推进真实HTTP模型团队交付，与批准前问答并行；目录发行包已准备，平台实机/权限分离/完整恢复未关闭。B1/B2仍IN_PROGRESS、B3仍PLANNED，不更改退出条件。精确事实及未完成范围见[当前状态](node-task-service-status-2026-09-08.md)。

正式实现采用 [ADR 0088 的 Node 投影](adr/0088-node-task-service-production-projection.md)。B1/B2/B3 的用户出口保留，历史 Go/RB1 物理实现和实验 JSON Store 不是新 profile 的前置。优先 Qwen ACP + 通用任务/SQLite/恢复同链集成；签名与安装按实际发行的脚本包、原生资产及运行时分别验证，不继续生成匿名 Marshal 二进制，也不以 Node 形态豁免平台与发行验证。

**2026-09-08 发布目标澄清**：用户要求的是完整、可部署的 stable 正式发行，B1 演示和 Node 可行性实验仅是中间证据，不是交付终点。不得继续把固定订单、禁用工具、一次性 JSON 候选当成通用 Agent Team 的实现主线。正式 Qwen 接入优先使用其原生 ACP：保留批准范围内的原生工具/配置/Skill，接入进度、权限/问答、取消；会话加载能力不替代 Marshal 的持久状态、归属与恢复验证。非 ACP Adapter 仍按能力匹配，并不要求所有品牌采用同一种 transport。正式 Node profile 与旧 Go/数据根的关系须明确更新合同后实施，不以实验通过默默替换权威存储或豁免 B2/B3。最新失败与路线纠偏见[Qwen 接入记录](qwen-integration-status-2026-09-08.md)。

| 阶段 | 用户实际得到什么 | 完成的硬证据 | 不等待 |
| --- | --- | --- | --- |
| **B1：真实团队 PoC** | HTTP 提交/确认明确任务，两个真实 Worker 并行，整套成果独立验收并下载 | 同一候选、真实进程重叠、精确集成成果、独立 Decision、下载后业务检查通过；取消/失败可见 | Workspace、安装身份平台、SQLite 全面切换、旧历史迁移、自动规划平台、三 Provider、UI |
| **B2：本地 API 可用** | 保留问答/计划、制品/审计/恢复与简启动；B2-L 接全程受管 Leader、局部调整、授权交付/后验 | 原业务/API 证据保留；新增闭环有独立 Review、保留无关成果、原决定/回执与整体结束，取消/崩溃不重规划整队或盲重发 | 强 OS/凭据隔离证明、动态角色/Workflow 平台、任意高风险流程、全部 Agent 增强、U1 |
| **B3：正式可靠发布** | 已声明平台/profile 上可安装、长期运行、故障可处置的正式版本 | 同路径故障/长期业务验证、签名/公证、Linux 实机、支持矩阵及受保护 same-bytes stable release | UI、HA、通用工作流、统一 Skill、未声明支持的 Provider/外部发布能力 |

**与历史编号的映射**：旧 B1 的单任务纵切成为本稿 B1 的内部步骤；旧 B2 的最小团队闭环前移到 B1，完整体验进入 B2；B3 保留正式支持。历史重排时 B1/B2 IN_PROGRESS、B3 PLANNED 的记录不改写，当前状态只见上文与 Roadmap。重排本身不是完成或另建 Goal 清零成本。

不是每个检查点一个 PR/Run/ADR。每个实现切片应穿过完整 producer→consumer→业务验收；仅在当前出口被阻断时修复底层，不把历史所有 cleanup 重新排成必做项。旧 Marshal skill 不使用；本轮文档任务也未调用它。

## 2. B1：先把一个真实 Agent Team 跑通

### 最小实现范围

1. **复用而非重建**：沿现有 fixed server、Application Port、RepositorySession/Store、受管进程与 B2 物化/集成资产，增加薄 Task HTTP。不先替换全部数据库，不新建平行 Task 权威，不调用 child CLI 推进。
2. **一个现成 Provider、一个真实业务样例**：优先实际可用配置；同一 Provider 两个实例，例为同一 Git 应用的 API 与客户端。输入直接说明真实仓库/需求，无 Workspace/资源注册 API，也不造 dummy Git。基线/独立 worktree 由执行层处理。
3. **最少确认**：可用小团队模板或已有明确计划，冻结接口、验收示例/反例、交付物、权限、依赖、总时间/尝试限额。用户确认一次，之后 server 自主调度、收集、验收和集成；不要求人为逐个编写/批准 Run。
4. **只接闭环需要的 API**：Task 创建/查询/计划确认、最小 graph/workers、取消、operations、输入与成果下载、events/最小 audit。轮询可用，不先铺 SSE/角色 CRUD/全量 Provider 管理。
5. **最低保障自动执行**：本地访问保护、输入/制品路径边界、独立目录、期限和 owned cancel、持久批准/执行/命令/结果/失败记录、重复请求不重复启动。B1 复用现有合法固定安装；不添加新的 operator-local 安装收据流程，也不绕过旧运行时权限。
6. **客观独立验收**：先把原业务 oracle 正反例跑通，再派作者。整合后的交付在作者之外重建/验证；无需为每个客观检查增加一个 LLM。原合同需要语义 Review 时保留有界独立审查和 Core Decision，不由作者自签。
7. **原生结果接缝**：新团队显式冻结 `native-terminal/v1`，Adapter 从受管进程正常退出和完整 transcript 构造控制结果，模型仅交付业务成果及真实报告。旧 JSON Run 不自动回退或换解释；当前为待验证候选，不把正常进程退出当成业务完成。

### 退出条件（全部满足才记 B1 团队 PoC 通过）

- 一条真实 HTTP 任务经过确认后，至少两个作者的实际执行时间重叠；不是仅两个计划/Run 已创建。
- 无需人工逐 Run 推进，完成自主 Collect→独立验收/Decision→集成→最终 Outcome；所有必需交付来自同一精确候选。
- 从正式下载接口取成果，在新目录按声明输入/依赖执行原业务检查。两个组件各自测试通过但组合协议错误的反例必须失败。
- 任务/Worker 状态、有限日志/最后观察、失败原因、基础耗时/尝试和成果可查询；未知 token 不填零。
- 至少一项受控取消/timeout 证明只能停止所属执行，确认停止前不复用目录；重复 HTTP 请求/丢响应不会启动替身。未知结果明确未决，不伪装成功。
- 正常重启可查询原计划/结果/失败且不重复派发；原恢复路径失败时停止接单、报告具体原因并保留原受支持只读诊断，不要求所有未知情形仍提供在线 HTTP 查询。完整自动恢复是 B2/B3 出口，不假装 B1 已覆盖。
- 没有 UI、Marshal skill、SQLite 全面迁移或三品牌齐全仍能演示。当前未支持的零 Git/复杂交互必须明示，不能以此伪造通用能力。

这是受限本机 PoC，不是正式 v1.0。没有下载消费和独立验收，不能用 PR、组件测试或 Worker 正常退出关闭此阶段。

当前可信单用户 profile 按 ADR0094 以职责与权威分离解释分权，不要求先完成 OS/凭据强隔离证明。本轮维护者独立复核七条件后，B1 本机 PoC 记 `PASSED`：原真实 Pi `69cface` 团队29.624秒、作者交叠10189ms、精确独立 Decision 与338B成果下载后再消费；同生产代码 `37df` 取消13.046秒、原 owned stops/零验收与替身/冷开原回执。原 SQLite 完整性/外键/source-event 摘要及下载复核通过，两个库前后摘要不变；当前生产等价源的独立团队3/3还覆盖两组件各自合法但组合错误拒绝/无delivery、重复请求与冷开零重派。[当前证据索引](roadmap-status.md#业务交付当前表)保留范围。此为复核旧实物，不是今天新跑模型；未知用量仍null、旧non-production不改，不覆盖B2-L或B3。

## 3. B2：把演示变成日常可用的本地 API 服务

**当前并行实施方式**：以 [Task API OpenAPI](../packages/task-api/openapi.json)与[当前接口说明](../packages/task-api/README.md)描述实际可调用形状；`node-api-contract.md` 仅保留旧实验协议，不用于当前接入。独立推进 Schema/兼容负例、HTTP Application Port 的 DI/单元测试和真实 Agent 接入，三者在组合测试处汇合，不互相串行等待。接口存在不等于 B2 全部通过；问答、DAG、operations、制品、SQLite 与恢复的当前完成状态只见 [Roadmap 当前表](roadmap-status.md#业务交付当前表)，`API-STABLE` 按下列四项独立判断。

### B2-A：Task 体验与本地简启动

当前有界切片按 [ADR 0086](adr/0086-task-preapproval-questions-and-preview-revisions.md) 先接**未批准 Task** 的关键问答：同 RB1 原子答案/新预览、原期限、精确重放、最终确认及取消 CAS。运行中 Worker 的待答、pause/resume/steering 不在该子切片；`order-quote/v1` 保持零问题。测试专用模板和组件链通过也只能关闭该子条件，不能关闭下列完整 B2-A 或 B2。

- 目标 `marshal serve` 自动处理默认数据目录和本地 token；可选 data-dir 是启动配置，不是 Workspace 实体。新根自动建库，旧根损坏/丢失部分状态/不兼容或仍有 owner 就报错，不重置。
- 简短需求→仅关键澄清→有限计划→确认→执行；Task 下 questions/answers、pause/resume、图与 allowedActions 完整可用。固定角色模板即可，不开发角色管理平台。
- 持久问答绑定节点/subject/revision/期限；局部待答不阻断无关分支，全局 pause/cancel 优先。Agent 无原生交互时明确边界，不无限等待或假装支持 steering。
- 实际 prompt/context、可见工具/阶段、观察新鲜度、等待与失败/返工/验收统计可查；计量不可见时保留 source/coverage/unavailable。

### B2-B：SQLite、通用制品与恢复

- 最小 Store 接口后接一个 SQLite 权威库，复用业务状态机。事件、投影、幂等、预算与 outbox 原子提交；执行不持全局写锁，业务 API/验收不因换库重写一套。
- 新数据根直接 SQLite；旧 file-backed profile 不双写或隐式接管。历史升级 U1 独立，不能成为新用户首次运行前置。
- 真实零 Git SQL/文档/样例制品与多仓库上下文交付；不要求 repoId、dummy commit 或 Core 资源 schema。SQL 检查明确实际引擎/方言，文件生成不冒充生产补数。
- 同版本重启恢复；创建/dispatch/结果/取消的关键故障点不产生双写、重复预算或迟到接纳。完整长期/平台矩阵留 B3。
- 一次局部内容修正只重做受影响节点，保留无关已接纳成果；预算/期限不重置，不能安全恢复时产生明确非成功 Outcome。NO_CHANGE 满足原批准、适用旧 Run 合同及独立验收，不伪造变更。
- token 不可测时不阻断执行义务结清，不假退款；相关预算 schema/producer 和重启后准入测试一起落地。

### B2-C：开放接入与可用性证明

- Pi、Qwen Code、OpenCode 分别维护核心 success/failure/cancel 兼容结果。先用第二个真实 Adapter 与至少一项混合任务证明 Core 无品牌耦合，第三个并行推进，不拖住已可用路径。
- 不要求精确版本白名单、ACP、原生 callback、实时 token 或完整工具流全部具备；缺增强能力诚实呈现，特定任务需要的能力仍须满足。
- 使用 Agent 已有登录/模型/Skill，不建设统一鉴权/Skill 平台，不偷偷模型 fallback；已知配置错误在启动前一次报告。
- 对两个业务族做实际全流程验收：无 Git 制品、Git/跨仓库协作；记录局部修正、待答与恢复成本，而非只跑 happy path。

退出：目标 Task HTTP 体验在同一应用/SQLite/执行链可用；第二真实 Adapter 的解耦证据、通用制品、问答/暂停取消、局部修正与同版本恢复通过。第三 Provider 或可选增强未过时单独列 pending/unsupported，不能宣称三家均支持，也不反向阻止核心 API 完成。

### B2-L：全程受管 Leader 与授权交付（DESIGN，正式发布前必过）

按[Leader机制](node-leader-execution-design.md)一个完整纵切实施：需求→必要确认→两个互补 Worker→独立 Review→基于真实问题的局部修正且保留无关成果→独立整体验收→授权交付→独立后验→Leader汇总/Core整体结束。Leader由业务事实重复唤起，读持久快照、提出有限动作；现有一次性Planner/reviewer标签不代表此链已实现。默认未授权只交付成果，不建Leader资源平台。

出口包含机制文档六类组合反例：聚合事件/单在途/无heartbeat调用风暴、相关当前性、独立证据与权限、硬取消/期限、决定及动作各COMMIT恢复/外部unknown对账、旧协议与后继任务。普通业务失败保留Leader决策窗口和合法无关分支；未知清理/预算/权限故障仍硬规则。新profile须延迟整体结束至所需交付/后验，不能改旧completed或复活旧Task；格式/API兼容一次冻结后显式启用。实际闭环未过不得以设计或B1 PoC替代。任意高风险业务、动态角色/Workflow平台不作首发前置，成功重复后再模板化。

### API-STABLE：核心接口稳定检查点，不是所有扩展齐备

满足以下条件后可开发 UI-1，不要求三 Provider/全部增强、旧库迁移或 B3 全故障矩阵提前完成：

1. OpenAPI、handler、示例/客户端的 Task/Worker/Artifact/问答/取消/operations/审计语义一致；preview revision 和兼容规则明确，无未处置的核心 API P0/P1。
2. 至少一个支持的真实 Provider 经纯 HTTP 完成需求确认、团队交付、下载/审计及主要失败控制；公开接口不依赖读取内部账本或 UI 私有补步骤。
3. 幂等同请求/冲突、旧 revision/错误对象、未认证/不可信 Origin、制品路径、取消迟到、事件重连/gap 等相应已提供功能的反例通过。
4. HTTP 脚本与另一个独立客户端消费相同契约；查询/取消在并行 Worker/长 Verify 下有明确且实测的响应上界。

API-STABLE 只是接口相对稳定，不授予正式平台支持。开发 UI 不能延误 B3 API 发布；不做通用拖拽编排器。

## 4. B3：在声明范围内正式发布

- 在同一已通过业务链验证 crash/丢响应/重复与迟到结果/旧 owner、问答与 cancel 竞态、磁盘满、Agent 无响应、长 Verify、事件洪泛与关闭恢复；未知副作用不自动重试。
- 多 Task 长期运行、有限容量、坏任务故障隔离、长历史查询、一致备份/恢复/安全 GC；不会无限增加 Worker 或无限保留假活执行。
- 实际待发布 bytes 完成业务交付与恢复；Darwin 稳定安装，签名/notarization 按 ADR0088 §6 的资产类别适用，Linux server 实机，受保护 same-bytes stable release。纯 JS 包不伪称 notarized。此为 Marshal 软件发行，不因 Leader 业务发布授权而豁免，不用 PoC 或旧 RC1 替代。
- 支持矩阵分别列平台、Agent/profile、可见能力、版本兼容、取消/恢复、数据格式及安装/升级范围；只承诺实测者，未过的第三 Provider 不冒充已支持。
- 首版可先承诺干净安装及已验证的新格式升级。U1 旧账本升级未过时明确不支持导入；不静默丢历史。ADR0094 的代表性业务发布按 B2 单独验证，其他生产写/高风险发布按授权与实测声明，不全部成为前置；强隔离 profile 是后继加固，不借 trusted-single-user 宣称已覆盖。
- 用代表任务族积累重复交付和配对实验，包含失败/准备/人工等待；未证明优势就不宣称多 Agent 普遍更快。

退出：精确 sourceHead/资产摘要/平台/profile/同路径业务及恢复证据齐备，无未处置 P0/P1、未知写入者或未说明恢复风险，正式发行与故障处置文档可由新用户复现。仅打 v1.0.0 标签不算完成。

### U1：旧历史导入（独立支持项）

旧来源先由原入口合法收口全部非终态及未决效果，持锁一致备份；按原 namespace/ID 原字节导入只读历史，不补签。真正切换原状态根前证明旧 writer 已机械禁止；崩溃失败保留原库受控恢复，不双写、不清空、不复用未知占用目录。只有完整导入/拒绝/崩溃/等价检查通过才声明 UPGRADE_SUPPORTED；不等待 U1 才做新 Task。

## 5. 并行实施与避免重复返工

| 并行工作 | 当前最有价值的产出 | 边界 |
| --- | --- | --- |
| 主链作者 | B1 Task HTTP→现有物化/执行→集成/Outcome | 共享 Application/Store 提交边界一个 owner，先闭环再重构 |
| 验收/API 作者 | 冻结业务接口、组合错误反例、下载消费客户端、状态查询 | 不改作者业务成果；先用 fixture，集成时必须接真实 API |
| Adapter 作者（有空余容量才开） | 当前阻断的终态/原生配置问题；主链稳定后第二 Adapter | 不为矩阵扩展占满主链与验收资源；不猜未冻结接口 |

普通主 Agent＋SubAgents 协作，每位代码作者独立 worktree，主笔/集成者统一处理共享文档与接口；独立验证与 Publisher 权限边界不变。按可用内存、CPU、Provider、scope 和验收队列确定并发，不固定凑满作者数。B1 产品执行先两个作者，之后按证据扩容；API-STABLE 前不派 UI 开发。

采用错峰流水：上一候选跑 CI/独立验证时，后继从精确候选 SHA 的独立 worktree 开发，另一路提前准备下一项的调用链与验收方案。CI 未完成不等于禁止开发，也不授予合并或发布资格。父候选失败时只冻结受影响的提交/依赖，修正后同步后继并补针对性集成验证；不让所有任务陪等，也不在 CI 运行中仅为状态文字反复推送同一候选。共享接口先对齐，设计准备完成即释放协作槽给审查或下条开发。

一个业务切片一次聚合检查和审查；机器可查的协议/配置/验收前提在付费调用前完成。结构性错误没有事实变化不重试；内容反馈集中处理，不逐文件/字段滚动 rework。连续技术修复没有推动当前出口时，收缩设计或改复用路径，不能把修治理本身当目标。

## 6. 如何报告进度与收益

- 每次报告：哪个用户出口前进、精确候选/证据、当前 blocker、下一条真实交付动作。文档、PR、代码接线、实机通过和正式发布分开计。
- B1 先保存一条完整团队结果及相同需求的单 Agent/强 Lead＋SubAgents 基线；不要求大规模实验后才演示。
- B2/B3 在零 Git 制品、Git 协作、缺陷修复等至少三个任务族各积累三次配对；需求起点、oracle、模型/工具/资源相当，保留失败与人工准备/确认/等待。任务不适合并行时选择单 Worker，不强求团队。
- 总耗时、人工介入、首次验收/首审通过、局部返工/重试、最终交付、用量来源与覆盖都可追溯；没有 token 不填零，小样本不宣传普遍效率优势。
- 若质量相当却明显增加时间、也未改善恢复/审计/人工等待，优先删无收益流程，不继续扩调度平台。

本轮交付是最终方案和实施路线调整；未运行新的产品任务、迁移现存状态或发布正式资产。
