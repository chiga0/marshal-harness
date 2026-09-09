# Agent Team Milestone 历史实施日志（截至2026-09-09）

以下正文从`3f359fd0`的`docs/agent-team-service-milestones.md`原样移出，保留当时描述、SHA、失败和结论，不构成当前待办或最新同步状态。当前出口见[Milestone](agent-team-service-milestones.md)，实际状态见[Roadmap当前表](roadmap-status.md#业务交付当前表)。

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
