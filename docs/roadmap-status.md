# Roadmap 状态

<a id="业务交付当前表"></a>

## 当前唯一状态与关键路径（2026-09-09）

本段与下表是当前状态；之后的“早期集成记录/历史过程记录”保留原 SHA、失败成本和当时结论，不再作为待办。最终目标仍是 B1 真实团队交付→B2 日常 API 可用→API-STABLE→B3 正式可靠发布，不以新增协议或 PR 数量替代用户出口。

**2026-09-09 18:03 检查点**：main/origin/main 为 `3beafe17c1874c42d0fed8e25a3f0c9a811c9bc0`，已推送的 API、有限报告端口和实机驱动批次 `pendingRemoteSync=false`；该 SHA 的 Node team CI `34336243004` 五项通过，资产 `10098074531` 不含后继 v7 Core。后继整合 `da2e9bd2719929e94c25fbed553d0a1819b0e9ad` 保持候选，未合 main：API/客户端/驱动 57/57、真实 HTTP/SQLite/所属进程正向 2/2（46.249 秒）通过；发行清单 55 文件、独立安装冷导入通过，不等于安装后 v7 执行通过。以上使用受控 ACP，不是新真实模型验收。

Core 冻结源 `236ce0173dc797daa753e734ee9d31083078d5d3` 首审确认四个 P1：等待/陈旧 Leader 的业务义务续接、取消后的原发布收据保留、发布后验能力前置、结构失败不可自动重试。现交同一作者聚合修正，未放行。后继测试源 `0e9886bc` 在维护者普通权限运行：HTTP 双作者取消 1/1（3.616 秒）通过，独立 Review→仅 east 修正→第二次 Review 接纳已走到原 verify，但 verify 失败导致 Task failed，完整修正用例未通过；原状态与失败成本保留，不能据局部成功关闭 B2-L。当前同时推进独立安装包 v7 消费；B2-L 整链仍 DESIGN，B2/B3 不升级。[当前实施 Goal](agent-team-service-milestones.md#当前持续实施-goal)取代 ECS-first 的执行前置，不重置历史成本。

**2026-09-09 17:24 增量**：Leader API 源 `b3910e95a3dcd038d1b2c9ae7b4a19551724d771` 经独立审查无 P0/P1、API/客户端 45/45 与发行包回归 20/20 后，已合入并正常推送 `07e3afc29ed992f17dba857c7c16e4419f43766f`；该批次远端同 SHA，`pendingRemoteSync=false`。新增两项 Leader 子资源，机器合同当前共 27 操作/66 Schema，Ajv2020 的 45 示例通过；原 58 Schema 和 24 路径对象（25 操作）逐对象保持不变。新增接口尚不等于 v7 Core 已接线，既有 API-STABLE 结论仍限原 25 操作。有限本机报告端口源 `47f10417e93ca4bbfc8d3bcdc178a1636cf0b831` 已独立通过 11/11（含真实 HTTP GET 后验与五个所属子进程故障窗口），当前只证明端口，不证明 Task 原授权、完整 Leader 或正式发布。Core 与真实 Leader 驱动仍在实施；B2-L 的整链成熟度保持 DESIGN，不因并行组件通过升级。

**2026-09-09 16:57 后继**：上一代码/证据批次已正常推送并核对 main/origin/main=`3f359fd0fd88987b8ea2f3d3fbb36058834b003b`，该批次 `pendingRemoteSync=false`，覆盖下段记录时的未推送状态。[ADR0095](adr/0095-node-managed-leader-contract.md)/[Leader 机器合同](node-leader-execution-contract.md)已独立审查接纳，按 Core、API/客户端、本机报告端口三个无冲突范围实施；B2-L 仍 DESIGN，未因合同接纳升级。

**当前研发同步**：单Worker取消Core=`070086e9`、整合/实机source=`190171e0`已本地合入main `25ff8315cbf63e907aaaa47f869d76d592c89acc`，记录时`pendingRemoteSync=true`待正常推送。新v6显式profile已支持原HTTP取消，旧格式仍501；一次聚合P1修复经原reviewer独立43/43通过，整合API/客户端/发行包/driver56/56、真实CLI repair/取消15/15与58Schema/35示例通过。真实Pi一次45.843秒仅停止east，west原550候选保留，冷开原回执/零替身，不是独立Task交付。存储有界写失败测试已随f27784dd推送。最近已记录的双平台完整CI仍为旧1a9dc3c/run34323362296，不能替代本源；[精确证据与失败范围](node-task-service-status-2026-09-08.md)。

**当前目标/profile**：[ADR0094](adr/0094-trusted-single-user-role-team.md)及[受管Leader机制](node-leader-execution-design.md)按用户本轮要求设计Accepted，机制为`DESIGN`未实现：Supervisor观察聚合、Leader业务判断、Core校验/硬规则和已批准调度、Execution原handle操作，现混合实现尚待接线。全程Leader/集中Review/局部调整保留成果/授权交付后验单列B2-L正式发布前出口，不以Planner或下载替代。强OS/凭据隔离后置，独立证据/授权/恢复与B3软件发行不变；文档不改HTTP/枚举/格式或数据，不撤回API-STABLE，未来新终态/副作用需显式兼容，旧non-production不重标。

**B1 独立结论（2026-09-09）**：维护者按七条件复核原实物后记`PASSED`，仅`trusted-single-user`本机PoC。原真实Pi `69cface` Task `task-70cb4ab2…`29.624秒、作者交叠10189ms；原独立Decision `decision-5b561070…`绑定计划`2ea310…`，338B下载`584a06…`重新独立消费仍2 reports。相同生产代码`37df`取消Task `task-d2a44c9b…`13.046秒，owned停止/零verifier与替身/冷开原回执。两个原SQLite的完整性、外键、source-event哈希检查通过，复核前后库SHA不变。当前`31afe269`生产等价源独立团队3/3通过10.140948375秒，含组件各自合法而组合wrong-total拒收/无delivery及重复approve/cancel/冷开零重派，原driver23/23通过。此为独立复核旧模型实物和无模型组合，不是今天重跑模型，不含v6/Leader/B3，usage仍null；原完整记录见[状态证据](node-task-service-status-2026-09-08.md)。不是仅因放宽隔离而自动通过。

**2026-09-09 13:25 CST 当前出口**：API-STABLE 核心接口检查点通过，范围仅当前 Node profile 的25操作/58 Schema与同包客户端，不授予正式 v1/生产/全平台支持。已审源码 `5657a7d` 正常合并推送至 `94795f37e37863d0fddb003874b6fac5f7f8a082`，远端一致、`pendingRemoteSync=false`。同 Pi/custody 配置的真实取消一次13.046秒通过；原20项定向与三波14Task/55执行的独立隔离测试通过。旧 main `9700ade` 双平台CI全绿，新提交CI另记，不冒充本源通过。[完整四项证据、失败成本与下一步](node-task-service-status-2026-09-08.md)。

**恢复与平台范围**：0092 新 v5 仅对已冻结资格的可信 staging-only 文件准备，结清 reservation-after/binding-before 两个无许可窗口；六个原 CLI/SQLite COMMIT 故障窗口后均验证新团队下载与再次冷开，无旧任务重派。旧根、Git/custom prepare 未绑定义务不被追认，原 unknown 仍保留。事件洪泛的独立1/1证明有界失败、原cleanup及随后新团队可用，不证明洪泛期间并行健康、SLO或磁盘满。已有旧8f97安装消费和失败成本保留，不再用它替代新v5；[当前实施检查点](node-task-service-status-2026-09-08.md)区分平台/模型/故障范围，正式发布仍未完成。

| Milestone | 当前状态 | 尚缺用户出口 |
| --- | --- | --- |
| B1 真实团队交付 | `PASSED`（可信单用户本机 PoC） | 上述七条件独立复核通过；旧non-production不改，不外推全程Leader、生产或stable |
| B2 日常本地 API | `IN_PROGRESS` | 问答/团队/审计/同Pi取消及新v6单Worker取消已有实机；真实模型局部修正、用量缺失仍明示；B2-L尚待完整接线 |
| B2-L 全程受管 Leader | `DESIGN`（正式发布前必过） | 需求/确认→2互补Worker→独立Review→真实局部调整保留无关成果→授权交付→后验；含取消/失败/重开原决定与动作不重复、无虚假成功；当前Planner不等价 |
| API-STABLE 核心接口检查点 | `PASSED` | 当前25操作/58Schema/同包客户端与四条出口通过；候选版本不等于正式发行，不承诺旧协议/任意未来版本或全Provider兼容 |
| B3 正式可靠发布 | `IN_PROGRESS` | 冷备份、多Task、v5/v6故障、洪泛、有限EFBIG/SQLite写失败和旧同资产安装消费已有有界证据；声明平台部署/长期故障、ENOSPC及受保护同资产发行仍未完成；强隔离单列后继加固 |

### 历史 Node 集成检查点（不覆盖上表）

**2026-09-09 12:59 CST 已合入并推送**：规划提示合同修正source=`42426a2`，localMergeSha=`69cfacee2fbea366174744167d5a2c3a3dfeee89`，远端一致、`pendingRemoteSync=false`。原reviewer复审与独立24项通过；当前候选真实Pi HTTP运行问答→双作者交付→独立验收/下载→正常重开29.624秒通过、交叠10.189秒、零重复派发。前一候选一次真实规划失败保留：未说明scope类型导致对象/数组不匹配；补提示而非放宽Core。上一源0f86804 Ubuntu/macOS各510项全过，API响应上界和事件续读测试已合入，不再待开发。当前补同候选失败控制/接口证据与完整快照新目录恢复；仍非生产发布，[精确证据及剩余边界](node-task-service-status-2026-09-08.md)。

**2026-09-09 12:39 CST 已合入并推送**：输入审计/简启动source=`fe04a8a`，localMergeSha=`ee3932ffc135f89ce0395f159696f93c6684da1d`，远端一致、`pendingRemoteSync=false`。独立最终29/29通过，58Schema/34示例/25操作通过；简启动一次P1聚合修正后复审关闭。下一项为API-STABLE响应上界及事件续读。香港runner/固定Node可执行，SCP137后未收到安装包、不记部署成功。B1/B2/API-STABLE/B3不升级，[精确证据与边界](node-task-service-status-2026-09-08.md)。

**2026-09-09 12:20 CST 已合入并推送**：sourceHead=`8f8ff8dfe57852e6b383fbd2bb7c70fb75a1d552`，localMergeSha=`85fc62a3338fc15c408e941e16a7c882b0be4a6d`，远端 main 已核对一致、`pendingRemoteSync=false`；精确 source 的 Ubuntu/macOS Node team CI 均通过。运行中问答和局部修正进入主线；实际输入审计、简启动仍在途，OpenAPI仍为candidate，B1/B2/API-STABLE/B3不升级。[验证范围及未完成项](node-task-service-status-2026-09-08.md)。以下未合 main 记录是历史时点。

**2026-09-09 12:04 CST 增量**：已审局部修正及证据正常推送至功能分支 `d650238`，不是main合并；原两P1聚合关闭，最终定向80/80通过。真实Pi首轮交付26.288秒、双作者交叠11.788秒，下载7单/1850 cents和正常重开通过，无实际repair、不造错寻找失败。局部修正实现/确定性证据前进，真实模型修正仍未证明；实际prompt/context审计和简启动开始并行实施。下表状态不升级，[精确证据与边界](node-task-service-status-2026-09-08.md)。

**2026-09-09 10:23 CST**：真实 Pi＋Qwen 双仓库 patch 团队一次通过，56.914秒、作者真实重叠11.631秒、4 Attempts、独立验收与下载消费23检查/12负例通过、正常重开零再启动。source=`76f9c36` 已合并并推送 `7294878e68c59a592b4c8cd1236da34bad038fe2`，pendingRemoteSync=false；新增API/驱动独立组合48/48通过。只关闭固定业务混合交付子条件，不把 Qwen 零权限回调或普通宿主执行当分权证明；运行问答合同已接线，Core实际投递/ACK、局部修正和正式 profile 仍在途。[精确证据与边界](node-task-service-status-2026-09-08.md)。

**2026-09-09 10:12 CST 已合并并推送**：sourceHead=`db57d45c7b8a951eb3adddafd2f649ad165cd068`，localMergeSha=`0057dd1b7c5b259f238600bacbc3abb346d770ca`；正常推送后远端 main 已核对为同一 SHA，产品 `pendingRemoteSync=false`。最终完整 Node 组合 **418/418 PASS、319.688秒、零失败/取消/跳过**，运行前后冻结树不变且 clean；日志 SHA-256=`47b7d7ceb5c01fa5611ab93e2fc5f3c08ea4c51d8672de6f3a8204aaecd7d63f`。 限定执行托管/cleanup-only 恢复、安装包 v1/v2 团队闭环及 Git 双仓库 patch 业务插件已进入主线，均经独立审查。此前 `d6833d5` 的409项中1项 Pi legacy 回归保留为失败记录，由 `ed8df71` 最小修复并通过最终全套，不放宽 v2 未知 scope 门禁。后继三个并行方向为运行中业务问答、其 API/schema/客户端、Git/Pi＋Qwen 混合实机驱动；[ADR 0090](adr/0090-node-runtime-business-questions.md) 已接纳但不等于运行时已完成。B1/B2/API-STABLE/B3 不升级，[精确范围与剩余出口](node-task-service-status-2026-09-08.md)。

以下时间检查点为历史事实，不覆盖上述同步状态。

**22:50 CST 当前代码**：Runtime source=`c1dd3c` 已正常合入并推送 `e491339d5e5eb90d52248b9a3f644aad3beab0c5`，产品 pendingRemoteSync=false。独立20/20测试与审查通过，关闭所测原继承组清理假阳性；完整组合另行验证。ADR 0089 已接纳，尚待执行托管与跨代清理的实际整链实现，不关闭 B2 或正式发布。[当前精确证据](node-task-service-status-2026-09-08.md)。

**22:42 CST 提交边界已同步**：localMergeSha/main/origin/main=`32b353f2460415fc0b92e3ad784d02ddd1e45f35`，source=`2f6c28f`，正常推送已核对、pendingRemoteSync=false。新增原 CLI/HTTP 的 create/result COMMIT 前后四项真实 crash 用例，独立与原 crash/team 组合9/9通过、24.514秒、无P0/P1。未改变生产恢复行为；下一步 Runtime 清理假阳性修复和 [ADR 0089 cleanup-only 恢复](adr/0089-node-execution-custody-and-cleanup-recovery.md)，详见[检查点](node-task-service-status-2026-09-08.md)。

**22:22 CST 当前同步与下一步**：localMergeSha/main/origin/main=`8d48d9452485f6c753cd074587b2307e683e3129`已实际正常推送，产品pendingRemoteSync=false；集成source=`ce2879f`。Qwen日期问答、Pi真实团队候选和crash首批已合入；整合`c576772`完整384/384通过，最终仅新增crash测试的`ce2879f`定向2/2通过，分次范围不合并计数。当前主线转入B2跨代清理收口与create/result故障组合；B1权限分离仍未证，API-STABLE/B3未完成。ECS原scp被SIGKILL且未上传，部署未通过。开发阶段已按用户常驻授权直接review后merge/push，v1正式发布后恢复PR；以下旧阻塞/未推送记录不覆盖本段。[精确证据](node-task-service-status-2026-09-08.md)。

Pi候选完整回归后续确认：`74a239e`独立一次 **374/374 PASS、182.405秒、exit0、零跳过**，HEAD及工作区保持clean；不把它与regional候选347项拼成一个组合，不改变未合main/未发布的状态。

**22:07 CST Pi实机增量**：独立候选`74a239e`真实Pi0.84.4 HTTP团队一次通过，37.064秒、双作者交叠9.403秒、工具许可5/拒绝1、独立验收及下载4笔/1825 cents、正常实例重开零重复。尚未合main/推送，完整组合回归中。Qwen与Pi均已有明确范围的真实团队成功，但权限分离、活跃执行崩溃收口、局部修正及正式同资产部署仍未完成。B1/B2继续IN_PROGRESS，开始补真实进程crash组合测试，详见[精确证据](node-task-service-status-2026-09-08.md)。

授权后更新：用户已明确批准研发整合和限定ECS上传；Pi独立分支正常获批完成整合、回归中，root的regional研发merge仍被AGENTS作用域解释阻止。ECS已建专用目录但scp异常exit137，复核无归档/state、未启动服务，终止原因未知，未重传。以下“等待授权”保留审批前时点，当前待规则澄清及传输根因诊断，不是用户未授权。

**22:00 CST 当前增量**：候选`538c534`真实Qwen必要日期问答团队一次通过，21.440秒、双作者交叠14.828秒、独立下载验收5笔/1550 cents，正常服务实例重开无重复；完整Node组合347/347通过。Pi原P1关闭，但正式模型团队尚未执行。main/origin/main仍为已推送`61a19e9`，候选pendingRemoteSync=true、尚无localMergeSha。研发合并及香港源码包上传分别被安全审批拒绝，等待明确授权，未绕过；香港固定Node安装不等于服务部署完成。B1/B2继续IN_PROGRESS，权限分离、局部修正、完整恢复、第二真实Adapter和正式部署/发行仍待完成。下列检查点保留历史范围，精确事实见[当前记录](node-task-service-status-2026-09-08.md)。

**21:21 CST 当前增量**：main/origin/main=`6a2df5ef4c3fc2952a0975cc34d5d0008bcc9b3f`，产品代码已正常推送、pendingRemoteSync=false。批准前问题/答案/新预览/精确确认已贯通 HTTP→SQLite；完整 Node 组合337/337通过，41 schemas/25示例独立核验，25文件目录包已打包核验。一次消费者示例遗漏 P1 已聚合修复并由同 reviewer 关闭，不记首审全绿。真实日期区间问答业务与 Pi 正式原生工具适配并行；尚未形成新模型验收。B1 七项功能条件已有有界证据，明确尚缺适用 profile 的 Worker/Publisher 权限分离证明；不把 B2/B3 全矩阵加成 B1 前置。香港专用账号 SSH 本次被公钥鉴权拒绝，未执行远端检查。精确 source/merge、包摘要与验证范围见[当前检查点](node-task-service-status-2026-09-08.md)。下列时间记录保留历史范围，不覆盖本段。

**21:02 CST增量**：`0a1deed` 真实Qwen启动阶段HTTP取消一次通过，两个原作者清理、Task/Operation收口、零verifier/交付及正常服务实例重开回执保持已验证；未声称模型生成/工具中取消。安装目录包另已通过本机独立CLI进程create→正常退出→open的零模型smoke，并固化10/10回归。最新合入source/merge、范围和本地同步状态见[当前检查点](node-task-service-status-2026-09-08.md)。B2问答候选在独立审查，Pi RPC候选继续处理原生权限/工具清理接缝；B1/B2未整体关闭。

**20:16 CST新增真实团队证据**：正式源码 `42f95658071e9ce03d38d8926b376b26c82ffd50` 已推送；纯HTTP真实Qwen 0.22.3完成planner→一次批准→双作者（重叠16.799秒）→独立验收→下载消费→正常重启原回执/成果不变，总计32.620秒。本次无自动重试，交付为两地区报告、4笔/1825 cents，精确Task/摘要见[实机检查点](node-task-service-status-2026-09-08.md)。这是正式同链的Mac ordinary-user dogfood成功，不是仅实验或进程夹具；仍不证明Worker/Publisher分权、真实运行中取消、崩溃恢复或stable，B1/B2继续IN_PROGRESS。下面82b64cf是此次运行代码基线，不覆盖本段新增实机事实。

当前代码集成基线 main/origin/main=`82b64cfa8d0daef0c188f0e296299b6e3aedcc48`，已实际推送，以上代码 pendingRemoteSync=false。正式HTTP→SQLite→Supervisor→双作者→独立验收/Decision→交付下载及取消/正常重启已有真实Node进程夹具证明；不是模型团队通过。维护者在文件树相同的source `3383e0e` 完成307/307组合测试；[远端 Node team CI](https://github.com/chiga0/marshal-harness/actions/runs/34223961302)通过，通用CI最近查询仍运行中。目录发行包24文件已核验，不代表部署或stable。真实Qwen原生工具单会话证据保留，当前正在准备正式同链团队实机验收，B2问答并行开发；API-STABLE和正式发行尚未完成。精确source/merge、Core两轮P1修正及旧失败记录见[当前实施检查点](node-task-service-status-2026-09-08.md)。

### 以下为较早本地集成检查点（不覆盖上表）

**最新正式路线**：[ADR 0088](adr/0088-node-task-service-production-projection.md) 明确 Node-only 产品投影；不继续以固定无工具样例作为开发主线。Qwen 实验在 `81a83e6` 上确定性组合 46/46 通过，但真实双作者的业务验收失败；后继独立真实 ACP initialize 成功（协议 1、qwen-code、loadSession=true、exit 0、stderr 0），尚不证明工具/交互/恢复。已审实验与目标纠偏 sourceHead=`c8923cebe6124abb4e4a296ac1717ed0529d3387`，localMergeSha=`97c4558`；Git 空锁经两次无持有者核对后保留改名备份，再完成合并。最新 fetch 确认 origin/main 仍为 `ba2196b`，pendingRemoteSync=true。正式 ACP/Application/SQLite 接线继续推进，B1/B2/B3 不升级。以下较早的“Qwen 未新增证据/下一步决定 Node”等语句保留检查点上下文，以本段为准。

**API/DI 并行检查点**：产品能力设计已有 ADR0085，但不能把旧 `/v1alpha1` OpenAPI 当作当前 Task-first 稳定协议。Node 实际接口候选见 [API 契约与差距](node-api-contract.md)及 `experiments/node-team/openapi.json`，覆盖 8 个路径、9 个操作；它只描述 ADR0087 实验，尚非 `API-STABLE`。HTTP 已抽为注入 Application 的薄适配器，sourceHead=`fb08495`，localMergeSha=`64c28cb5986c0ec424fc0f3aefd7d1565ba3e6c8`；维护者独立审查及 34/34 Node 组合回归通过。Schema/负例与真实无模型 HTTP 响应验证、DI 单测、第二 Provider 接入分别在独立工作树推进，不等待真实模型才写接口或测试。Qwen 接入仍在实施，未新增该 Adapter 的实机通过证据。

本地 main 已通过 `428acb36a2af69bff0b1f94d6f77ada078a8321c` 集成此前开发栈，并继续合入上述 DI；远端 main 最近核对仍为 `ba2196b`，本地增量 `pendingRemoteSync=true`。以下各 PR 的远端功能分支记录是各自历史同步事实，不表示本地仍未集成，也不表示远端 main 已更新。

Schema source=`960292a` 的 18 个 component 已经 Draft 2020-12 metaschema 独立校验；与 DI 合并后六个 Node 测试文件 **37/37 通过，26.69 秒**，包含真实 HTTP 的契约响应、重放、取消和交付（Agent 为确定性替身）。同组新增测试已加入后续集中 CI 清单，本轮没有发新 PR、触发远端 CI 或调用真实模型；这关闭确定性接口接缝，不关闭真实第二 Provider/生产发布。

2026-09-08 用户明确调整：快速推进期间不再逐切片创建 PR 或等待 CI/E2E；独立本地 review 无阻断后直接合并，继续下一项开发，集中补跑验证。保留单写者、精确 source、未验证项和发布前实际验收，不把本地 merge 当成正式 release。已有远端 CI 可异步完成，不反向阻塞后继开发。

Node 与 SQLite 已在本地候选 `5b50439f79fe55082785b60cea11f09c99c94166` 集成；SQLite 修复 source=`f51ff9a744a84da0e73d6dd358aa1f8110dd5bd2` 经维护者独立代码 review，无生产期限或行为变更，新的动态 race 待集中验证。Node source=`0e1250c` 的 [Node team 34203735967](https://github.com/chiga0/marshal-harness/actions/runs/34203735967) 在 Ubuntu/macOS 均通过确定性回归，本机同组 23/23 通过；不调用真实模型或 Marshal 原生文件。后续直接基于集成代码推进，不继续维护平行功能岛。

当前待补验证：SQLite 新边界/事务回滚的 race、合并候选的全仓回归与真实第二 Provider、完整 supervisor/主机中断恢复；B1/B2/B3 不因本次合并自动升级。香港执行机专用 `marshal-runner`（uid 1000）本轮 SSH 只读核对存在，随后两次连接出现 `Bad file descriptor`，未改宿主策略或循环重试；旧执行机文档的“缺专用用户”不适用于香港此账号，本轮未取得新的远端测试通过证据。

- **Node-only 可行性实验已通过**：`c88410b` 在本机 Node 24.15.0 / Pi 0.84.4 经纯 HTTP 完成真实双 Pi（重叠 13.106 秒）、69 项独立验收、下载后 69 项消费、活跃前端重启及另一真实任务取消/重启保留，全程不调用 Marshal 原生文件；本次约 40.62 秒。前两次真实失败分别为原生事件流预算不足、角色提示未各自完整列明数值范围，均保留并修正，不倒填首轮通过。见 [ADR 0087](adr/0087-node-local-team-feasibility-probe.md) 与[实机记录](node-team-feasibility-2026-09-08.md)。该独立 profile 不修改旧 `.marshal`，不自动授予 Go B1/B2 或 production；下一决策是正式 Node profile 边界，而非继续等待本机原生执行才允许做实验。

- 远端 main 最近核对 `ba2196b`；PR #275 已在完整 CI 全绿后合入功能分支 `feat/team-resident-progress`，merge=`a2f41c97c95a5dafb9b84c44a2a0c79fbb60f1b1`，pendingRemoteSync=false，**不是 main 合并**。Task HTTP、独立客观 Decision、完整制品链已有原 Store/Verifier 的确定性组合证据，尚无该完整团队的真实下载消费。
- PR #276 的 `272aa4c` 在 [CI 34195113168](https://github.com/chiga0/marshal-harness/actions/runs/34195113168) 六项通过、macOS quality 失败：输出超限负例的 150ms 预算先触发解释器启动超时。后继 `a97b0f8` 只分离负例预算，超时负例仍 150ms，输出/JSON 负例沿用原默认 30 秒；运行时代码和错误断言不变。独立审查 P0/P1=0，37 项回归及 [CI 34198059195](https://github.com/chiga0/marshal-harness/actions/runs/34198059195) 全部检查通过，2026-09-08 08:07 UTC 已远端合入 `feat/team-resident-progress`：sourceHead=`a97b0f88413f177d2cf5a5cf09dacf91d2916fe9`，remoteMergeSha=`0cc5881e35fe86887cc9b98d7871f7cf8d556440`，pendingRemoteSync=false；不是 main 合并。此前 context 夹具和本次失败成本都保留。
- **Pi/Qwen 本地可用配置已由用户确认**。Mac 当前实机阻碍是固定 Marshal 二进制启动被 AMFI 拒绝，不是未配置 Agent 或待补额度；不得通过随机换路径、重签或跳过安全策略假报运行。下一条 B1 证据必须来自合法固定候选的真实 Task HTTP 双 Worker→自主验收/集成→下载消费，并验证所属进程重叠、取消/cleanup 及重启。
- B2-A 问答在接受的 [ADR 0086](adr/0086-task-preapproval-questions-and-preview-revisions.md) 下接通候选代码；`4e8925d` 的三组真实 Session/HTTP 冷恢复及 resultingress/taskhttp 问答 race 通过。独立审查发现 sealed server 漏转发接口，`75af587` 已修复并复审 P0/P1=0。最终 sourceHead=`18b7c785b22b7c66642856442a58efb091b4c21e` 的 [CI 34198304816](https://github.com/chiga0/marshal-harness/actions/runs/34198304816) 包括 Darwin 在内全部检查通过，PR #278 于 2026-09-08 08:11 UTC 远端合入 `feat/task-cancel-integration`，remoteMergeSha=`f17cb1093e5a11260d880be0d642e4e60bd87a07`，pendingRemoteSync=false；尚未进入 main，也未通过此次合并进入 `feat/team-resident-progress`。只处理批准前问题，生产 `order-quote/v1` 保持零问题；不冒充零 Git 业务或运行中交互。客户端 `91ee1935` 已整合，11 项问答及原 37+12 项客户端回归通过。
- SQLite 候选已推送 PR #277：原子后端和目录恢复修正经独立审查无剩余 P0/P1，最终 Linux 测试包 `9b8148d` 在香港 ECS 专用用户 22 组通过、2.136 秒、非 race。但 `4dc849b` 的 [CI 34198134136](https://github.com/chiga0/marshal-harness/actions/runs/34198134136) 两平台 quality 均在 `TestRecordTransactionAndPageBounds` 失败：期望 `ErrLimit`，实际分别得到 compare-and-append conflict / context deadline exceeded；不得用此前非 race 结果放行或原样重跑。该夹具在 5 秒事务内构造大型规范化记录，需先隔离测试构造成本与事务限制验证，不能放宽生产期限。当前仅 COMPONENT，未接入生产 Session、不双写旧 RB1，不关闭 B2；旧 19 组证据保留不替代新快照。
- 流水并行保持 CI(N)/问答实现(N+1)/SQLite 独立准备与审查；共享事务一个作者。后继机器门禁前移到真实 producer/consumer，历史失败、CI、人工等待不删分母。目前没有重复业务配对实验，不能宣称相对强 Lead＋SubAgents 的效率优势。

| Milestone | 当前状态 | 当前尚缺的用户出口 |
| --- | --- | --- |
| B1 真实团队 PoC | `IN_PROGRESS` | 同一合法固定候选的真实 HTTP 双 Worker 重叠、自主接纳/集成、下载消费、取消与重启证据 |
| B2 本地 API 可用 | `IN_PROGRESS` | 实际业务关键问答/运行中控制、简启动、SQLite 单写真值、零 Git/多仓库、通用制品、局部修正/恢复与第二真实 Provider |
| B3 正式可靠发布 | `PLANNED` | B2 同链故障/长时运行、受支持平台实机、签名/公证和受保护 same-bytes stable release |

## 2026-09-08 早期集成记录（历史，不覆盖当前表）

本节保存当时的集成与失败记录；其中“当前”“下一步”“在途”不覆盖顶部唯一状态，也不构成重复实施已接线功能的理由。产品要求见[服务架构](agent-team-service-architecture.md)、[实施 Milestone](agent-team-service-milestones.md)、[ADR 0085](adr/0085-agent-team-service-contract-and-storage.md)与[合同适用性](design-contract-map.md)；原候选证据不转移为后继 release 资格。

- main 最近核对为 `ba2196bea33e6f007809f75f9671928c892bfa11`；远端集成分支 `feat/team-resident-progress` 为 `a2f41c97c95a5dafb9b84c44a2a0c79fbb60f1b1`（PR #275 全部检查通过后合并）。取消组合在独立 `feat/task-cancel-integration` 验证。早期 B2/设计分支、旧 CI 与 canary 只保留精确证据，不能挪给新候选；分支合并不等于 main 合并或 stable。
- 798ea39 的精确 [CI 34095940005](https://github.com/chiga0/marshal-harness/actions/runs/34095940005) 五项全绿；先前 34094155668 的结果计数阻断由 [ADR 0084](adr/0084-pi-typed-terminal-result-framing.md) 候选修正。后继 max 实机 34097645547、一次显式 flash 替代 34098369837 均未完成团队；后者 service 通过真实 Collect/Verify 的 33 项检查并形成 ReviewPacket，client 在模型终态失败。没有本轮独立 Decision/ACCEPTED、第三节点或 GoalOutcome。停止模型轮换，后继聚合终态分类与任务上下文/输出收敛，不删失败分母。演示范围和实际证据见 [PoC 交付页](poc-agent-team-delivery.md)。
- 最终目标不改为“完成更多协议/PR”：交付 fixed server 的完整业务任务与受限团队，并用至少三个代表任务族的重复配对实验，与相同冻结契约、oracle、模型、工具及资源的强 Lead＋SubAgents 比较。源代码返工、失败 CI、失败 Attempt、人工等待全部计入；目前没有效率优势证据。
- 最新实施候选 `dd8e8ec` 的完整 CI `34156121695` 五项与 Darwin 定向 `34155305960` 的 36 项通过；真实团队 `34157213736` 证明已收集结果→Stop→Close 收口、零重复 Collect 及 cleanup released。但 client 的 Provider `length` 仍触发团队 halt，service 虽已接纳结果进入 VERIFYING，没有 ReviewPacket/Decision/集成/下载。当前补驱动的跨节点失败观测与超时诊断，不自动解除 halt，不原样付费重跑。`a5418f4` 的历史单节点 Verify pass 不替代本候选团队出口；B1/B2/B3 状态不升级，详细证据见[审计报告](audit-report.md)。
- 用户已创建以 B1→B2→API-STABLE→B3 正式部署为出口的持续 Goal。[Linux 远端验证执行机](remote-linux-validation.md)已安装并校验 Go 1.26.6，实际开始执行旧远端基线 `68e5c8b` 的测试；它不替代 Darwin 测试或授予 Linux server production 能力。系统绝对路径 Python 不兼容导致 CLI renderer 基线失败，保留失败，不通过改 PATH/跳过后假报全绿。
- 2026-09-08 Task HTTP sourceHead=`8543878cc9cc9b095e94c06c0cc41987611149f2` 的[完整 CI 34184969456](https://github.com/chiga0/marshal-harness/actions/runs/34184969456)五项全部通过，包含 Ubuntu/macOS quality。[PR #271](https://github.com/chiga0/marshal-harness/pull/271) 已远端合入 `feat/team-resident-progress`，remoteMergeSha=`be03e7015be12636b7f2af5aca644039b3558492`，tree 与 source 相同；该 PR pendingRemoteSync=false，**不是 main 合并**。草稿/确认/查询、原 writer lane、CLI 封闭参数及 revision 400/409 已接线。首次 CI 发现的跨层依赖已通过组合根 DI 修正，未扩大白名单。ECS 四包定向 race、Mac held Session 冷重放另有通过证据；本机固定测试受安全检查终止的历史保留，Darwin CI 通过不表示本机策略解除。内网机器不接 GitHub CI，额度不足期间不发起真实 Agent 重试。
- 下载客户端与纯 HTTP 团队 driver 已分别通过 PR #272/#273 全部 CI 并远端合入 `feat/team-resident-progress`，最新 remoteMergeSha=`aef06e587e70ddd259644f557e8caff1297b8723`；两 PR pendingRemoteSync=false，main 不变。取消客户端 `ec44741` 与只读重叠观测 `cdc9b54` 已独立审查，组合候选 `175cca43cd3d1ac5abb54c5e56c245b5116a76e5` 的 37+12+20 项无模型测试通过，PR #274/CI `34189439463` 在途；已推送但尚未远端合并。
- 自动客观独立 Decision/完整制品候选 sourceHead=`aa4a82da4a24c8c7e62434752d4d1d7d2f72d398` 经同一 reviewer 聚合修复/复审，原 Verifier 固有安全 gate 不匹配 P1 已关闭。主 Agent 在精确 linker sourceHead、固定二进制及包目录独立执行真实三节点 Verifier→原 Packet/Importer→接纳上游/集成→BuildTaskDelivery→同 RB1→下载→冷恢复，15.80 秒通过，树漂移零追加、重复生成零追加和存储 blob 损坏拒绝也通过；准备/启动/收集及 Worker 输入仍为确定性夹具，无模型。localMergeSha=`ed34ed9b03fa25ca3eb120b46158f654f83255b7`；组合后继 `4564dfe49d916d89291fa99e3c490b21c8b19df4` 已补 CLI tool allowlist 实际接线并独立审查，五组定向测试通过（HTTP held-owner 冷重放、完整交付链、新 Task 批准、旧 Team 不继承、ZIP 边界），其中交付链 14.95 秒。当前 pendingRemoteSync=true，精确组合完整 CI 待提交；这些证据不替代真实 HTTP 双 Worker 重叠及业务交付。
- Task cancel 后端在独立工作树实现同 RB1 stop/disposition、原进程取消/cleanup、冷启动与提交竞争，不因客户端完成而声明能力可用。早期测试夹具曾依次暴露必需 mediaType、固定根初始化顺序及 transcript-meta 缺失；已补完整生产形状，保留成本。固定 review.test exit 137 与 ECS SSH 连接失败未计通过，未绕过宿主限制；可执行的生产链测试已完成上述复跑。没有新真实团队交付、取消完成或正式发布证据。

| Milestone | 当前状态 | 已有证据 / 实现 | 尚待退出条件 |
| --- | --- | --- | --- |
| B1 真实团队交付 PoC（原 B1 单任务为内部步骤，原 B2 团队最小闭环前移） | `IN_PROGRESS` | main 正常业务独立 ACCEPTED；已整合的 `4ace42c` 停止候选在 34067556449 证明长 Verify 期间另一 Run 的 deadline、查询与 Collect 可前进；34082574786 两个真实 Pi 节点到 REVIEW_PENDING，未完成团队交付 | 复用现有合法安装/Store：Task HTTP→计划批准→两个真实 Worker 并行→自主 Collect/Verify/Decision/集成→下载消费；取消/失败有事实，正常重启可查询。不以 Workspace、安装管理或全面迁库为前置 |
| B2 本地 API 可用版 | `IN_PROGRESS` | 候选已接独立 Decision、上游组合、第三节点及 completed GoalOutcome 查询，完整实机未过；原 B2 实现与失败证据保留，不因重排改成完成 | 简启动、SQLite、零 Git/多仓库、持久 AskUser/答案/确认/验收、详情/审计、同版本恢复与局部 rework/reuse；第二真实 Provider 验证解耦。第三品牌可单列待支持，不阻塞核心 API-STABLE |
| B3 长期运行与正式支持 | `PLANNED` | 历史故障/恢复组件与 RC1 prerelease 证据保留，不升级成熟度 | B2 同路径故障矩阵、长历史/升级恢复、managed signing/notarization、Linux server 实机、受保护 same-bytes stable release |

本轮流水状态：PR #274 sourceHead=`175cca43cd3d1ac5abb54c5e56c245b5116a76e5`，remoteMergeSha=`019cc08228126a6304725f5a36990b8981c42567`，tree 一致，pendingRemoteSync=false。PR #275 sourceHead=`5c13033e3610371e538e3a067266a83c8287d308` 已推送，CI `34190493630` 在途，尚未合并；此前段落的“待提交”已由此记录取代。取消 sourceHead=`a59a1382989235ef9888ad2436ebe8f9f269b5f0` 的本地组合 merge=`66cf5d10d0b51121cf43aed31536d13a5eea36a6`，接线 `767aaee`、测试资源修正 `eea6e01` 均尚未推送。精确 `eea6e01` 的 draft/approved/READY 冷恢复、缺 cleanup 不假结案、原完整交付链（14.73 秒）通过；reservation 两个夹具因创建目录改变 held root 身份失败，整组测试仍为失败。独立审查发现取消误触发全局调度停止、坏 Task 饿死取消队列及 endpoint 测试借用泄漏，共三项 P1，后者已修，前两项聚合修正中。实时原始 facts 提取与取消/交付组合回归在独立 worktree 并行；不让 CI 等待占住开发槽。

后继验证（取代上段对应在途状态）：PR #275 sourceHead=`5c13033e3610371e538e3a067266a83c8287d308` 的 CI `34190493630` 全绿，remoteMergeSha=`a2f41c97c95a5dafb9b84c44a2a0c79fbb60f1b1`，tree 一致，pendingRemoteSync=false。取消组合代码 `c7ffc519f4bcee0da1e4e1ddff2bb77210468eb2` 已关闭首轮调度问题及复审发现的终态冻结来源错误；同 reviewer 复审包含合法状态夹具修正 `f1474f47`，无剩余代码 P0/P1。Mac 通过取消 HTTP/冷恢复/公平调度、两类 Outcome 前停止及 Outcome 先赢竞争、原完整交付链；终态六状态来源和事件形状属组件验证，不是实机 cleanup。Linux `d9cdfa8` 的 application/taskhttp/productionruntime race 与 resultingress 取消定向 race 通过，后者整包超时保留；`c7ffc51` 的新 ECS 执行连接失败未启动，不能挪用旧 SHA 结果。实时原始记录 collector `8683c94` 已独立审查，18 项新测试两套 Python 与原 20 项回归通过，并接入既有 CI target。取消组合尚待完整 CI 与真实 Worker 验收。

下一步顺序：提交取消/交付/实时观测的组合 CI，同时准备同一固定候选的实机验收。模型配置与额度可用后，执行真实 HTTP 双 Worker 订单团队并下载消费、观测所属进程重叠及完整 Task→CancelRun→cleanup→disposition、重启；不把无模型 fixture 当业务完成。B2 关键问答的可复用接缝已预设计，不提前修改当前共享事务；SQLite、零 Git/多仓库、简启动、问答和第二 Adapter 在 B2。U1 旧历史导入不阻新任务，B3 保留正式故障/平台/发布门禁。没有 Workspace/安装身份平台/三品牌矩阵前置，也不绕过旧 activation 或伪造 Git。结构性失败无事实变化不重复付费，历史失败分母不清零。尚未完成团队交付或正式发布。

并发边界：B1 先两个 scope 互斥的作者，验收/集成也计容量；现有候选仍只证明两个派发接缝，不把新方案写成已完成实机。开发可并行主应用闭环、业务 oracle/API 客户端、当前阻断的 Adapter；共享事务一个 owner，每个作者独立 worktree，不用旧 Marshal skill。UI 只在核心 API-STABLE 后启动。更多并发须依赖、目录、内存/CPU、Provider 与验收队列均允许；不是所有槽满才叫有效率。

本轮设计纠偏：按用户明确指示删除首版 Workspace 实体/API/注册，将账号/安装身份管理、全面迁库和全 Provider 矩阵移出 B1；旧 B1 单任务是新 B1 团队 PoC 的内部步骤，原状态、SHA/CI 与失败事实全部保留。ADR0085 已于 2026-09-08 接受，当前导航据正文同步，不另行扩大权限。AGENTS.md 的 universal 不变量/门禁保留；设计调整不代表产品出口、远端合并或正式发布完成。

## 历史过程记录（不作为当前待办）

2026-09-07 团队终态纵切候选：新增同 RB1 的 completed outcome，current owner 与三 Run lease 内复查原独立接纳/上游/集成 base；resident 自动一次收口、重放不增加 Attempt。已接原 `team-reconcile` 查询、固定客户端独立账本 readback、客户端只读等待，最终 summary 只有看到该事实才置业务 accepted。Outcome 的 budgetDigest 是原 reservation snapshot，实测仅 Attempt 数，token/compute 未测不写零。已添加 store 正常/冷恢复/幂等/拒绝、session 未就绪/越权、HTTP 只读绑定与客户端等待回归；本地编译、vet/staticcheck/架构及脚本回归通过，真实 Go 动态与三节点实机仍待候选 CI/验证，不能把候选实现记为 B2 完成。`55a435e` 的 CI 34089521238 最终四项通过，macOS 超过 20 分钟上限被取消；质量作业改为 30 分钟且保留全部检查。局部 replan/reuse、失败 Goal Outcome、计量与配对收益仍开放。

2026-09-07 当前关键路径修正：在新 Worker 派发前发现参考集成模板的正常成功路径矛盾——允许 `no_change`，却未声明其必需诊断交付物。新 proposal 明确交付绑定最终代码摘要、接口与 HTTP 示例的 `quote_delivery.json`；代码正确时不造无意义修改，固定 oracle 仍执行原真实 HTTP 验收，清单不充当通过证据。本地 5 项输入/20 项 oracle 回归通过（包括真实 loopback、代码不变交付、摘要漂移、重复字段、FIFO 与伪造通过拒绝），没有新增付费 Attempt。当前集成创建/同宿主评审候选 `55a435e` 的完整 CI 仍待终结；GoalOutcome 耐久收口、局部 replan/reuse 和真实第三节点仍未完成，B2 不升级。下一步是把三节点结果耐久聚合并读取，再执行同路径真实验证；不得把本次模板修正或清单当作 GoalOutcome。

2026-09-07 当前增量：`047b170` CI 34087532524 五项已全绿；`10a5edf` CI 34088862794 在两平台前置回归发现集成克隆未先 JCS 规范化，合法输入被拒绝，现已补齐且保留失败成本。继续接通第三节点客户端 Collect/Verify/独立评审载体，共用原等待预算，不重跑整个团队；本地 12+44+17 项客户端/载体测试通过，Go 修复动态结果待后继精确 CI。B1/B2 IN_PROGRESS、B3 PLANNED；没有新真实 integration、GoalOutcome、main merge 或 stable，也没有效率优于基线的证据。

2026-09-07 集成创建接线候选：实现两个 ACCEPTED 上游→原 integrate 调度→精确 Git 组合→仅变 base 的完整 Task→原 Prepare→同 RB1 创建冻结→原物化/首次 Start。新创建域绑定两个原 creation/Run/Attempt/authority head/candidate/patch/Decision/packet/Outcome，冻结和 Start 均重查；不增加预算或刷新已有创建。尚无真实 integration Run/最终 GoalOutcome，当前仍 B2 IN_PROGRESS。`047b170` 的 Git/review/团队前置动态回归已通过，完整 CI 当时仍在运行；本接线的新动态结果待提交后 CI。

2026-09-07 后续校正：接纳读取改为实际 Core producer 的同 round 归档 Decision/packet；已保留两轮 CI 失败的根因与成本。集成 base 的精确 patch 组合已编写，使用私有 index 生成确定性 commit，不触碰用户 HEAD/index 或工作分支。仍未接通 durable integration creation/Start 和 GoalOutcome，B2 继续 IN_PROGRESS；未新增 Worker 或整队重跑，未宣称效率获益。后继回归增加 gitworktree 并聚合全部目标包失败。

2026-09-07 集成准备候选：新增 current-owner/双 Run lease 的已接纳输入读取，复用原 Decision 校验与 Outcome producer，绑定精确 candidate/patch，不消费可变分支或仅凭 ACCEPTED 快照标签。已完成 Darwin/Linux 编译检查、vet/staticcheck；Go 动态测试待候选 CI，不把 `-exec /usr/bin/true` 计为测试通过。该读取尚未接入 resident 的集成创建，局部 replan、集成 Task/派生 base 耐久冻结、最终 GoalOutcome 仍开放；B2 不升级，本轮不再派整队重跑。

2026-09-07 后继实现：按 ADR 0082 扩展同宿主团队评审载体，两个 implement 的审查包全部关闭后才 ready，各自接收独立 Decision；先到先处理，共用有界等待，拒绝不丢弃另一节点成果，不启动新 Attempt。已通过 9 项团队驱动、44 项生命周期驱动、13 项载体测试及原脚本回归；新候选仍待精确 CI/实机，不倒填 34082574786 的 Decision。当前 Goal 保持业务交付及对照收益口径，不重建 Goal 清零失败成本；暂不再派整个团队，先闭环局部 replan/reuse 和集成。B1/B2 IN_PROGRESS、B3 PLANNED，无新 main merge/stable。

2026-09-07 实质进展与限制：`7a4f7d0` CI 34081513199 五项通过，真实团队 34082574786 成功完成两个节点各一次 Attempt、Collect/Verify 和 REVIEW_PENDING；66 条 RB1 记录及审查包绑定已核对。但独立审查以真实 HTTP 反例确认客户端 P1/P2，尚无 Decision/ACCEPTED、integration、Goal Outcome 或效率优势。已补齐原契约的 oracle 盲区与提示澄清，不修改旧 Run、不重跑已完成服务；下一关键路径为正式接纳/聚合客户端修正、局部 replan/reuse 和成果集成。详见[业务审查](audit-b2-first-team-2026-09-07.md)。

2026-09-07 验证纠偏：`5ca49bc` 的 CI 34080540487 前置团队回归通过，但 macOS/Linux quality 均在新 CLI 跨链测试中发现手写 environment-binding 版本错误；该测试此前未包含在前置步骤。现改用正式类型/版本常量及逐节点 Task/Policy Schema 诊断，并前移整个 CLI 包的 race 回归，同步 CI 精确内容契约。实机未派发，不把前置绿灯冒充完整链路通过；此第二次夹具来源返工计入成本。B1/B2 IN_PROGRESS、B3 PLANNED，仍须新 head CI→真实双节点→独立验收与集成交付。

2026-09-07 验证更新：提示/准入修复 `283b19b` 的 CI 34080167668 被前置团队回归拦截，原因是旧 store/session 夹具使用不符合既有 Schema 的字符串 context。已统一修正数组形状及对应反例，不修改生产门禁；没有派发该候选实机。B1/B2/B3 状态不变，下一动作仍是新 head 动态验证。

2026-09-07 最新：`b8dbf3c` 精确 CI 34078286247 五项通过；团队实机 [34079333520](https://github.com/chiga0/marshal-harness/actions/runs/34079333520) 完成批准、两个 Run 创建及一个节点启动，但第二节点在 launch 文本检查失败后耐久停派，最终客户端查询超时，整次失败。26 条 RB1 记录逐条摘要与顺序检查通过，含 2 个 reservation/open、1 个 process-started、1 个 team-plan-halted；没有团队 ACCEPTED/集成。已修候选中的 typed Task context 丢失、完整 Pi 提示传递，并将实际 launch builder 全节点预检移到批准前；HTTP 路由使用完整 URL，保留原路径检查。后继动态 CI/实机尚待完成，失败后的查询超时仍未证明关闭。B1/B2 IN_PROGRESS、B3 PLANNED；无新增 main merge/stable 或效率优势证明。

2026-09-07 最新：`a481f0e` 精确 CI 34076598876 五项通过，但首次团队实机 [34077560755](https://github.com/chiga0/marshal-harness/actions/runs/34077560755) 在顶层 CLI 的 `self-local-command-denied` 短路，未创建 Run/Attempt。已定位 handler 与 closed activation/classifier 接线遗漏，后继同批补 Schema、权限及真实入口回归，不原样重试。B1/B2 IN_PROGRESS、B3 PLANNED，无新增 main merge/stable。当前目标保持 B1→B2→B3 及至少三个任务族的强 Lead＋SubAgents 配对验证；收益尚未证明。以下“尚未执行/在途”为历史时点。

2026-09-07 团队验证入口：在 `4867ff7` 上接入 `order-quote-team` hosted 场景：一次团队批准→resident 自动物化/启动两个实现节点→既有 Collect/Verify/ReviewPacket；不逐个外部 Start，不提前创建 integration。新客户端与既有脚本回归通过，真实团队执行、进程重叠证据、独立接纳及集成交付仍待完成。`4867ff7` 的跨语言输入回归已在 macOS/Linux CI 通过，记录时全量质量作业仍在途；新入口仍需自己的精确 CI。B1/B2 仍 IN_PROGRESS、B3 PLANNED，无新 main merge/stable。

2026-09-07 当前：B1 `4ace42c` 精确 CI 与真实 Pi 跨 Run 验证 [34067556449](https://github.com/chiga0/marshal-harness/actions/runs/34067556449) 通过；同一 server 内约 104 秒 Verify 期间，另一 Run 在原 deadline 后约 5.53 秒到 BLOCKED，15 次 Inspect 和 stopped Collect 均完成。只关闭该候选组合子条件，无新 ACCEPTED/main merge/stable。B2 `00c8351` 的调度组合 CI 34067600918 已五项通过，同时正常整合上述 B1 修复，避免旧依赖进入下一次真实团队实验；合并 head 仍需自己的精确验证。B1/B2 仍 IN_PROGRESS，B3 PLANNED；以下为历史检查点。

2026-09-07 最新：创建恢复 2486b1c、首次 plan gate fc5d479、批准后冷续行 aa230da 的精确 CI 均已五项通过。耐久停派 1f61c2c 已推送；后继将其与 fixed server 初始调度、实际 Start 共用路径接通，自动 busy 上限 2，合法 Verify lease 仅使本轮不派发。代码已编写、本地编译/静态检查通过，halt/调度组合动态 CI 和真实并行仍待验证；自动 Collect、独立接纳、集成、Goal Outcome/暂停/replan 未完成。B1 的 4ace42c PR CI 与精确分支 CI 34066636760 均已五项通过；一次带新诊断的真实 Pi 组合验证 34067556449 已派发，旧实机失败未关闭。B1/B2 仍 IN_PROGRESS、B3 PLANNED，无新 main merge/stable 发布。下方保留历史检查点。

2026-09-07 最新检查点：B1 `80084bb` 的 CI 五项通过，但实机 34059061091 在 peer Collect 的 `pi-result-final-content-shape` 失败，尚未进入跨 Run 组合验收，禁止原样重试。B2 `928b8ab` 的 CI 34062410465 已通过 Linux quality、双架构 conformance 和 secret scan，记录时 macOS quality 在途；后继正接通原冻结输入的无 Probe planning 重建，尚未完成部分 Run 补齐和真实团队。Goal 保持 B1→B2→B3 与至少三个业务族的强 Lead＋SubAgents 重复配对收益验证。main 仍 ba2196b、两候选均未合并；下方旧“当前/在途”段落是历史检查点，不代表最新状态。

2026-09-07 当前：`d0be824` 的精确 CI 34054261338 与 PR CI 均通过；单次跨 Run 实机 34055217240 已在同一 owner、无重启条件下完成 peer Collect 到 VERIFYING，并启动第二个 Run，但整体因客户端响应失败而结束。已确认 15 秒固定响应读取窗口与 100 秒 Verify 不兼容，另有 1 秒 half-close 等待与身份复查预算冲突；后继候选集中修正传输阶段预算、补实际认证客户端回归和缺失的阶段诊断。**B1 仍 IN_PROGRESS，main 仍 ba2196b；PR #268 为 Draft，无停止候选合并、无 stable 发布。** 后续历史段落中的“在途”不代表作业仍活跃。

2026-09-07 最新：`0130465` 的 CI 五项全绿，真实 Pi 中断恢复 [34050602081](https://github.com/chiga0/marshal-harness/actions/runs/34050602081) 通过。已证明 stop barrier 落盘后、Run 仍 RUNNING 时中断 server，原 Attempt/stop intent 在同身份后继恢复为 BLOCKED，并通过原 Collect 请求冷恢复；一 Attempt、零 retry/rework，包含恢复的终态延迟为原 deadline 后 4.334296 秒。仅关闭该进程中断边界，不代表完整故障矩阵。长 Verify 让出全局写锁的后继 `74e8619` 已推送，CI 34050715623 在途，跨 Run 实机尚待验证。**B1 IN_PROGRESS，B2/B3 不升级；main 仍 ba2196b，停止候选未合并、无 stable 发布。** 下列段落保留为历史检查点。

2026-09-07 当前候选：`c61998515512f064fc4b113c229295e5df28e185` 的 CI 34047040755 五项全绿；真实 Pi 的 Attempt-timeout 34047844723 和 Run-first 34048091298 均通过终态查询、stopped Collect、同 bytes server3 冷恢复。稳定投影容器修复首次在两类预算顺序的完整查询路径得到实机正向证据；两次各一 Attempt、零 Cancel/retry/rework，不删除此前失败分母。**B1 IN_PROGRESS：尚缺停止中途故障矩阵、长写事务响应上界与最终组合验收；B2/B3 不升级。候选未合并 main，无 stable 发布。** 证据范围见 [审计报告](audit-report.md#2026-09-07稳定容器修复后的两类自动超时和冷恢复通过)。以下是历史检查点。

2026-09-06 16:19 UTC：诊断候选 `88f9edd` 的 CI 34043986843 全绿，但 Attempt-timeout 34044944162 在第 17 次 Inspect 的客户端 authority 打开阶段失败，未进入 HTTP；RB1 到 worktree release receipt，尚无 stopped Run 终态/Collect/server3。本轮新增公开客户端目录观察切换的确定性回归，冻结同类实机重试，先定位并修复完整接缝。**B1 仍 IN_PROGRESS，B2/B3 未升级；停止候选未合入 main、无 stable 发布。** 前文在途 CI/待派发语句为历史时点，不代表当前仍运行。

2026-09-06 15:19 UTC：`49f745d` 的 Run-first 实机 34041702160 通过，已独立核对原始预算来源和 server3 同字节/原请求/原 deadline 冷恢复；新增关闭一个候选子条件。Attempt-timeout 34041730043 虽已停止到 BLOCKED，但一次 Inspect 出现 `transport-failure`，未进入 Collect/冷恢复，整次失败并保留证据，不原样重跑。B1 当前关键阻塞是停止期间查询的完整并发路径，另有中途故障、长事务响应上界和最终组合验收；B2/B3 状态不变。后继 Outcome/归档候选 `97e448a` 已推送，CI 34041798874 在途。main 未合并停止候选、未发布 stable。

2026-09-06 15:15 UTC：`49f745d` 的 CI 34040876557 五项全绿；已通过精确候选 gate 派发 Run-first/冷恢复 34041702160，以及 Attempt-timeout/冷恢复 34041730043，后者按同 source 并发组排队，不是失联重试。后继 `3c5e734` 补停止 Outcome 部分落盘/冲突/冷 lease 重放组件回归，`7229b30` 修复 canary 未归档原始 Outcome 的清单缺口；本地静态与脚本检查通过，后继动态证据尚待新 source CI。上述实机未完成，B1 仍 IN_PROGRESS，B2/B3 不升级，未合并停止候选或发布 stable。

2026-09-06 14:45 UTC：候选 `dd8178f` 的五项 CI 与自动超时实机 34040069400 通过，原始 60 秒 Attempt deadline 未因 server 重启延长；零 Cancel，停止原因与原始预算来源已独立核对。显式取消/冷恢复和 main 正常业务 ACCEPTED 证据继续保留。B1 尚缺 Run budget 先到期、超时后冷恢复、中途故障与长事务响应上界，仍 IN_PROGRESS；以下为历史检查点，当前汇总以下表为准。

### 2026-09-06 早期检查点（历史）

2026-09-06 10:23 UTC：PR #264 的 source `2244092` 经 CI 34026422197 五项及全部 PR 附加检查通过，已远端合并为 `5bdec88d7161771caa2a556c70bbdef576375ff9`，pendingRemoteSync=false。main CI 34027276856 在途，尚未派新 canary。停止候选 `213e271` 已推送，CI 34027223715 独立在途；本次同步主线修复到停止开发分支，不将其放行。
2026-09-06 当前增量：停止分支 `20a9999` 的 CI 34026216770 五项全绿，覆盖 preparation 前与 launch 前 READY 原始预算检查。最新未发布候选新增 fixed CLI cancel、明确非成功的 stopped Collect 输出与显式 `order-quote-cancel` canary 驱动；23 项 Python 测试通过，但尚未实机派发。stop 端到端故障矩阵、release/receipt 衔接和截止延迟仍未关闭，不合并放行。主线实机仍是 `4f7311b` 到 VERIFYING，PR #264 待 macOS 检查；B1 IN_PROGRESS，B2/B3 PLANNED。下方为此前检查点。

2026-09-06 最新状态：main `4f7311b` 的 CI 全绿，唯一 canary 34024740089 已到 `VERIFYING/cleanup-released`，尚无 Verify/ReviewPacket/ACCEPTED。receipt 阻塞修复见 PR #264，其精确 source CI 独立推进，不原样重跑旧 canary。停止分支 `a04d76c` 的 CI 34025131805 五项全绿；最新候选补 preparation 前与 launch 前的 READY 原始预算检查，需新 source 动态证据。stop 端到端故障矩阵、release/receipt 衔接和截止延迟仍未关闭，不合并放行。B1 IN_PROGRESS，B2/B3 PLANNED；以下为历史检查点。

2026-09-06 停止纵切后续候选：已补 `run-stopped` 的 Core→fixed HTTP→客户端返回以及原 Collect 请求的终态重放，避免完成取消/超时后仍显示 live pending。只在 stop/cleanup/Outcome 验证后返回；部分结果仍 pending。当前仍在隔离开发分支，尚缺 resident timer、READY 到期准入和全链路 fault matrix；B1 未完成、B2/B3 未升级。主线与最新实机事实见下段；新候选动态证据独立记录，不借用旧 CI。

2026-09-06 09:15 UTC 当前增量：PR #263 已在全部 source 检查通过后远端合并，main 为 `4f7311b08bf59f6fad31aaae6661fc253ab0b0b4`，该 PR 的 pendingRemoteSync=false。main CI 34023916927 在途，尚无新 canary/ACCEPTED。取消分支 macOS CI 的 session 借用关闭死锁已定位并修复测试；继续实现原始业务 deadline 的原子接纳检查与既有 stop 恢复，尚缺 timer/READY 到期准入及完整故障证据，不可合并放行。B1 IN_PROGRESS，B2/B3 PLANNED。以下保留前一检查点，不覆盖本段最新事实。

2026-09-06 最新实机结果：main `5945b68` 的 CI 34008933865 五项全绿；单次 canary 34009508838 已越过历史消息解析并提取最终 assistant 文本，但以 `pi-result-final-object-trailing/authority-conflict` 停止，尚无 Verify/ReviewPacket/ACCEPTED。本候选将机器输出约束明确前移到 Pi prompt，禁止代码围栏/尾随报告，解析器门禁不变；不原样重试、不宣称格式问题已实机解决。取消纵切候选 `feat/b1-stop-lifecycle@f41b3aa` 已推送并运行独立 CI，仍不允许合并放行。B1 为 IN_PROGRESS，B2/B3 仍 PLANNED，详见 [审计记录](audit-report.md)。
2026-09-06 最新实机结果：main `4f7311b` 的 CI 34023916927 五项全绿；单次 canary 34024740089 已完成真实 Pi 结果接纳与 cleanup-released，并到 `VERIFYING`，随后 fixed delivery receipt 提交失败。候选修复 release 投影交换后 root mutation observation 未衔接的问题，不放宽门禁；尚无 Verify/ReviewPacket/ACCEPTED，不原样重试。取消/业务超时另在 `feat/b1-stop-lifecycle@a04d76c` 运行独立 CI，未放行。B1 为 IN_PROGRESS，B2/B3 仍 PLANNED，详见 [审计记录](audit-report.md)。
2026-09-06 11:35 UTC：同一 fixed server 的真实 Pi 订单报价链已到 **ACCEPTED**。单次 canary 34030199172 成功：一次 Attempt、零 operational retry、零 rework，经过 Start 丢响应/重启/rebind/replay、Collect、业务 Verify、独立 Decision 和终态查询。B1 的正常业务交付子条件已关闭；取消/业务超时及恢复尚未通过，B1 仍 IN_PROGRESS，B2/B3 仍 PLANNED。详见 [本次验收证据](audit-report.md#2026-09-06fixed-server-真实业务首次独立-accepted)。

更新时间：2026-09-06（ADR 0080 三面分离与业务交付路线；不升级历史成熟度）

### 2026-09-06 业务交付状态表（历史）

远端 main 为 `ba2196bea33e6f007809f75f9671928c892bfa11`（含 #266 的正常业务证据文档）。停止候选最新已验证代码 `d0be824` 的精确 CI/PR CI 全绿；其 34055217240 实机证明初始同 owner Collect 已越过 34052534488 的故障，但暴露后续传输阶段预算错配，未证明跨 Run 组合完成。最近完整成功的中断恢复属于前驱 `0130465`。后继修正及回归仍在同一 Draft PR #268，尚未合入 main，不存在 localMergeSha 或 remote merge；分支已推送不等于 main 已启用取消。

当前实机验证的 main 基线为 `c93e31bde15d9dbcd3487dfc1db323eafc4127e1`（[PR #265](https://github.com/chiga0/marshal-harness/pull/265)，sourceHead `e805129fd8b684824f25c6dffbfb9267642bdf65`），远端已合并，pendingRemoteSync=false。main CI 34029534577 五项全绿后，仅派发一次 [34030199172](https://github.com/chiga0/marshal-harness/actions/runs/34030199172)，全部成功。Run `fixed-server-t1-34030199172` 的第 6 条 event 为 `review.accept`，快照 `ACCEPTED/sequence=6`。此前 34027927457 的不合法 Pi content 失败仍保留，分类修复不是放宽解析或保证模型永不违约；本次通过不能删除失败分母。

独立 Decision 载体默认关闭、仅显式 `live-review=true` 启用。本次 reviewer 检查完整候选/冻结 Task/验证报告并复算证据摘要，复跑 28 项 oracle 与额外 500 组确定性业务断言；[原始 Decision](https://github.com/chiga0/marshal-harness/issues/186#issuecomment-5558942471) 由同一 server 接纳，不是 Worker 自评或人工改 Run。Task publication=none，因此业务候选没有发布或合并；这仍是可信仓库、Darwin ordinary-user 的合成参考场景，不等同外部业务仓库、team 或正式生产支持。

| Milestone | 状态 | 当前事实 | 未关闭的退出条件 |
| --- | --- | --- | --- |
| B1 完整单任务服务 | `IN_PROGRESS` | main 正常业务独立 ACCEPTED；候选取消、两种 deadline 与 barrier 中断恢复通过；4ace42c 精确 CI 及同 server 长 Verify/并行停止/查询/Collect 34067556449 通过 | 最终审查/候选主线合入与组合确认；旧 peer Collect 失败仍计入分母；完整 B2 同路径故障矩阵在 B3 |
| B2 受限 Agent Team | `IN_PROGRESS`（未完成集成交付） | 7a4f7d0 精确 CI 与 34082574786 双节点实机通过：server 自动物化/Start、外部客户端 Collect/Verify→REVIEW_PENDING；独立审查确认客户端 P1/P2并补 oracle 盲区 | 正式 Decision/客户端聚合修正与服务成果复用、进程重叠证据、server 自动 Collect/Verify、成果集成、Goal Outcome/有界 replan/暂停及恢复；尚无对照收益 |
| B3 长期运行与正式支持 | `PLANNED` | 历史 I186 组件证据保留，不升级 | B2 同路径故障/历史规模/升级恢复、#212 managed signing/notarization、Linux server 实机、受保护 same-bytes stable release |

[ADR 0081](adr/0081-fixed-server-stop-intent-and-outcome.md) 仍为 Proposed，main 尚未开启 cancel/timeout。下一步验证长 Verify 期间无关 Run 的 deadline 能继续推进，完成停止纵切的组合验收与独立审查，随后进入 B2；不重跑已通过的旧 source，不扩大 Provider 或另起 controller。完整失败样本及证据边界见审计记录。本机 fixed binary 退出 137/缺 Developer ID 身份是独立平台问题，不混为 CI canary 原因。

旧现场 33968513566 的 Run 为 `READY/sequence=2` 且漏收 RB1；#258 已修复诊断和收集路径。新现场 33971611314 的 artifact 9971098258 提供 17 条 RB1 fact：启动链已推进，随后 adoption 错把生产 namespace 限为 `control` 与 `existing-worktree-bindings`。候选改为冻结已存在的五类 composition store descriptor/name/object，保留未知插入、对象替换及 control ABA 拒绝；同步将公开装配测试的 ingress 放回真实 runtime 布局。增设先上传的小型诊断 artifact，避免以后等待整包 candidate 下载才定位失败；原完整 evidence 包继续保留。

### 合入前过程记录（历史采样，不是当前状态）

以下保留原始失败、修复和 CI 采样；其中“当前候选”“尚未派发”“正在运行”、旧基线及旧 milestone 表均指记录当时，当前结论仅以上表为准。

`6e62c8e` 的 [CI 33966029736](https://github.com/chiga0/marshal-harness/actions/runs/33966029736) 与 PR #257 的 `034a0f7` [CI 33966320451](https://github.com/chiga0/marshal-harness/actions/runs/33966320451) 均已五项全绿，包含 launcher v2、Terminate fixture 修复及 typed live-pending 的动态/质量回归。当前评审输入打包增量仅有本地定向证据，不借用前一 head 绿色，仍须新 head CI。下方运行中记录为历史采样。

当前候选为 [PR #257](https://github.com/chiga0/marshal-harness/pull/257)，仍待完整 CI 和独立评审。业务 canary 实机前发现 artifact 未包含 packet 引用的实际评审文件，当前在已有 T2 driver/上传目录补有界只读 `review-inputs.tar`，9 个定向测试通过；不增加另一个 controller、不重跑 Worker、不宣称可导入 authority。真实业务到独立 ACCEPTED 仍是下一条集成证据，B1/B2/B3 状态不变。

`6e62c8e` 已修复下述 CI 契约遗漏并推送，本地 committed-fixture 正反例和提交后 fixed checker 均通过，CI 33966029736 已越过原失败步骤。实机调度仍要求候选评审合入后的同 head main push CI；分支手动 CI 不满足这个现有 gate。驱动另修正 Python/Go 的 canonical deadline 小数尾零不一致，7 个定向测试通过。尚未派实机 Pi、没有 Decision/ACCEPTED，不借此更新 milestone 成熟度。

最新 CI：`8c37fff` 的 [33965649054](https://github.com/chiga0/marshal-harness/actions/runs/33965649054) 失败，四个 job 同因新增 T2 测试步骤未同步封闭工作流契约而停止，secret scan 通过。当前修复契约及固定文件绑定，补本地 committed-fixture 正反例和常规 T2 测试入口；新 head 的完整 CI 与真实 Pi 业务 canary 待执行。B1 仍 `IN_PROGRESS`，B2/B3 仍 `PLANNED`。下方“等待新 head”均为对应提交当时状态，不代表当前仍运行。

当前候选新增可直接运行的 T2 业务验收路径：手动 fixed-server canary 的 `scenario=order-quote` 复用同一 fixed binary/Pi/启动重启，随后自动 Collect→Verify→ReviewPacket；只有已认证 `attempt-still-running` 按冻结请求有界等待，其他失败停止自动重试。不生成 Decision、不声明 ACCEPTED。本地 6 个驱动回归与原 T1 shell 回归通过，真实业务 canary 尚未运行。`c9b2d11` 的 CI 33964259197 已结束：四项通过，macOS 因 Terminate 夹具使用错误 cleanup append operation 失败；当前改为 Reconcile 并保留错误权限零写入反例，等待新 head CI。B1 状态不变，下方运行中状态为先前检查时记录。

最新检查：`cb615e7` 的 [CI 33963625091](https://github.com/chiga0/marshal-harness/actions/runs/33963625091) 已五项全绿；`c9b2d11` 的 Terminate/legacy recovery 增量已推送，正在单独运行 [CI 33964259197](https://github.com/chiga0/marshal-harness/actions/runs/33964259197)，不能混用两者证据。固定工作区首次构建 `bin/marshal` 后，bootstrap 命令组返回 137 且没有版本/activation 输出；磁盘 codesign verify 通过，身份为 linker ad-hoc、`Identifier=a.out`、无 Team ID，本机可用 codesigning identity 数为 0。尚无日志证明具体终止来源，未重签、绕过保护、反复执行或启动 Pi。该固定路径候选仍不能计为实机可用。

取消入口的合同缺口收敛至 [ADR 0081 提案](adr/0081-fixed-server-stop-intent-and-outcome.md)：当前 Port 无 cancel、Run reducer 不接受 RUNNING 的 `run.aborted`，内部两小时 lease expiry 不能冒充业务 wall-timeout。先明确 stop intent/barrier 原子性、Outcome 和已确认 deadline，再完整接线；不使用旧 server 或 HTTP context cancellation 绕过。B1 仍 IN_PROGRESS，B2/B3 不变。

当前取消链路接线候选：`TerminatePreparedExecution` 已沿现有 owner/RB1 guard 接入 v2 Attach prepared continuation，要求 terminalization barrier 已耐久，不能从 context cancellation 直接推导发信号权限。与 Inspect 共用 exact intent/receipt 恢复；已提交结果先认证 post-checkpoint，已有成功终态重复调用不再执行或追加。新增连续 bootstrap→running→owner rebind→Terminate 丢回复→receipt 恢复→cleanup 冷重放测试及真实 Unix socket/Fake mechanics 的 Terminate continuation 测试。本地 compile-only/静态检查不等于动态通过；尚未把服务端 cancel/timeout 调度器接入该入口。共同生产目录入口同时拒绝 legacy generation，覆盖无需 socket 的 Close receipt 恢复写旁路。前一提交 `cb615e7` 的 CI 33963625091 与本候选分别计证，B1 仍未关闭。

最新验证纠偏：终态候选 `c921e1b` 的 [CI 33955604098](https://github.com/chiga0/marshal-harness/actions/runs/33955604098) 已结束，Ubuntu quality、Linux 双架构 conformance、secret scan 通过，macOS quality 失败。两处失败的夹具分别把 Inspect 的不存在事实写成 `ProcessTerminated`、把 Close 返回原因写成通用测试值而非 `mechanics-closed`；当前修正夹具并保留生产校验，增加固定阶段诊断，等待新 head 动态回归。新 fixed entry 同时退役 v1 Start/Reconnect/Attach 与旧 child 启动分支，在启动副作用前拒绝 legacy bootstrap；v1 解码/历史组件测试仍保留。此候选未部署，取消/超时、完整 legacy mutation 排查与真实 Pi 独立 ACCEPTED 仍是 B1 阻塞，不能把源码接线当成生产完成。下方 CI“仍在运行”为提交当时的历史状态。

2026-09-05 新任务启动候选：原 `reconcilePreparedExecutionLocked` 调用链中的 bootstrap、started、Bind/Spawn/Resume producer 已改为完整 v2；不增加第二 coordinator 或环境 selector，仍依赖相同 owner/Run/RB1 guard、source precheck 与 sealed resume proof。新启动前扫描当前耐久投影，存在未释放或 pending 的 v1 Supervisor 时拒绝启动，不 adopt 或改写历史。连续耐久测试的 Spawn/Resume 已改为调用实际命令 producer，并在假 transport 内检查 exact intent 已落盘；本地 compile-only、vet/staticcheck、format 与 architecture 检查通过。该候选尚未安装/启用，终态候选 `c921e1b` 的 CI 33955604098 仍在运行；v1 mutation 入口退役、取消/超时生产链及 fixed server 实机矩阵仍开放，不能宣称 rollout/B1 已完成。

2026-09-05 当前终态接线候选：v2 `InspectPreparedExecution` 在既有 owner/RB1 guard 内记录 exact intent，经 Attach 收取或认证已提交 receipt；v2 Close 必须同时取得 exact receipt 与独立内核 Supervisor absence，EOF 或磁盘 receipt 单独不能授权关闭。已提交但尚未证明退出的 Close 不重复发送；最终 `SupervisorClosed → CleanupCompleted → CleanupReleased` 沿原业务账本接纳完整 v2 身份，保持 v1 历史兼容。连续 RB1 测试从 bootstrap/start/rebind/Collect 延伸到该终态和冷重放，另覆盖 absence 漂移、伪造 checkpoint 与破损 journal 不修复；显式假 peer/内核观察不是真实 Pi。候选本地 compile-only、format、architecture、双平台 vet/staticcheck 通过，动态 CI 待提交后验证。下一步为 Terminate/恢复调用链检查、完整 selector 与固定 server 真实 Pi 独立 ACCEPTED；B1 仍未关闭。

验证更新：Collect 候选 `40bea7e` 的 [CI 33950856231](https://github.com/chiga0/marshal-harness/actions/runs/33950856231) 已五项全绿，包括 macOS/Ubuntu quality、Linux 双架构 conformance 与 secret scan；覆盖其祖先 pending-bind 修复，但不覆盖上方终态候选。下面保留各次提交时的原始状态。

2026-09-05 最新 Collect 接线候选：生产 `CollectPreparedExecution` 按完整 Supervisor generation 进入 v2 分支；原 owner/RB1 锁内持久化 exact intent、借用 Attach 收取结果并接纳 outcome。pending receipt 先认证 post-checkpoint；输出读取失败后复用已耐久 receipt，仅重读 held 输出，不再执行 Collect。新增固定 Core/held v2 journal/transcript reader，绑定 receipt、manifest、长度和内容摘要，不把 v2 转成 v1。连续 RB1 测试已延伸到 Collect 丢回复、receipt 恢复、读取失败重读和结果 fact 引用；仍使用显式假 peer，不是真实 Pi 验收。

验证纠偏：`71d53c2` 的 [CI 33950429378](https://github.com/chiga0/marshal-harness/actions/runs/33950429378) 双平台 quality 在 `format-check` 失败，未运行动态测试；原因是非 Darwin stub 未 gofmt，现已修正并补跑仓库统一 `make format-check`。Linux 双架构 conformance 与 secret scan 通过不代表 quality 通过。本候选仍待新 head CI；下一关键路径是终态 Inspect/Close/absence、selector 与固定 server 真实 Pi 独立 ACCEPTED，B1 状态不变。

2026-09-05 当前增量：接通同 owner 的 v2 pending-bind 恢复。held journal 只读分类为未执行、intent-only 或 exact receipt；未执行保留原 deadline/request 才能在认证 Attach 内发送，已提交 receipt 必须先认证其 post-checkpoint 才能进入 RB1，不能重做 bind。跨 owner pending、部分尾部或身份漂移继续 intervention。补充只读文件边界、零重放、连续 RB1 丢响应/伪造 peer/receipt 恢复和冷重放测试。本地仅 compile-only 与静态检查；本增量动态 CI 待验证。客户端 `408f02f` 的 [CI 33949630841](https://github.com/chiga0/marshal-harness/actions/runs/33949630841) 已五项全绿，不代替本增量测试。**下一关键路径：Collect/terminal producer、完整 selector 与固定 server 真实 Pi 到独立 ACCEPTED；B1 仍未关闭，B2/B3 尚未完成。**下面保留先前 checkpoint。

2026-09-05 最新 Core 候选：`RebindOwnerSuccessorForAttachedRecovery` 按耐久 Supervisor generation 选择 v2 transport；无 pending 时在原 owner/ledger 锁内完成 owner-successor→只读 Attach→v2 intent fsync→同连接 bind→v2 outcome，并保留幂等短路。rebind validator、recovery fact revision 和 held session-directory lookup 不再误读 v1 字段。连续 RB1 测试从已有真实账本 bootstrap/start/resume 延伸至 rebind、冷重放及丢回复 pending 保留，peer 仍为替身。本地 compile-only/vet/staticcheck/架构检查通过；动态 CI 待 exact head。**v2 pending receipt 分类恢复、Collect/terminal producer 和最终 selector/canary 尚未接完，不能将本候选称为重启闭环或生产可用。**B1 保持 IN_PROGRESS。

2026-09-05 最新客户端候选：新增 `WithAttachedV2`，以完整 v2 authority 调用 held-owner verifier，借用原 Unix socket 完成 observation→单次 prepared continuation→EOF。禁止重复/跨 goroutine/回调外消费，拒绝错误 successor、错误 method 和取消请求；持久化命令可能已执行但丢回复时保持 intervention，不声称无副作用。新增真实 socket 的客户端与服务端互通测试；Darwin/Linux 本地只做 compile-only、vet/staticcheck/架构检查。当前生产 selector 未切换；**后继必须接通 ResultIngress 的 v2 rebind/collect/terminal producer，并以固定 server 的真实 Pi 独立验收到 ACCEPTED；B1 仍为 IN_PROGRESS。**以下是先前 checkpoint，不表示当前可生产使用。

2026-09-05 后续接线候选：v2 Attach 服务端现在接受同一认证连接上的至多一个 `bind-authority/Inspect/Collect/Close`，不进入通用命令循环。检查点在命令锁内再次核对；bind 只允许精确 owner-bound successor，旧 checkpoint、错误 successor、Spawn/Resume 和跨代请求在 journal intent 前拒绝。已提交命令的丢响应仍由既有 exact receipt recovery 处理，不重用已消费的 Attach。Close 即使丢响应也退出服务循环。新增 portable 生命周期/拒绝矩阵与同一 Unix socket bind 测试；本地仅 compile-only、vet/staticcheck 与架构检查，不冒充动态测试。**callback-scoped 客户端与 Core producer 接线仍待完成，生产 selector 保持未切换，B1 未关闭。**下段为上一 checkpoint 的历史记录。

2026-09-05 候选增量：`a5a261e` 的 [CI 33947799422](https://github.com/chiga0/marshal-harness/actions/runs/33947799422) 五项全绿，验证 v2 bootstrap→started→bind/spawn→ProcessStarted→resume→cold replay 与 F_GETPATH 修正回归。继续 S3 接线时发现 generic `ReconnectV2` 会推进内存 owner/head，不能替代 ADR 0067 保留的只读 Attach；本轮在 ADR 0079 补足 v2 Attach 编码，新增显式 v2 authority/observation、只读服务端入口和 Unix socket 零副作用测试。尚未接通 callback-scoped prepared continuation，入口不放行任何 command，生产 selector 未切换。本轮动态证据待新 head CI；后继是 borrowed Attach→已耐久 bind/collect/terminal 命令以及固定 bytes 真实 Pi 独立验收，B1 仍为 IN_PROGRESS。

以下表格是早期 checkpoint，**不是当前状态**；唯一当前汇总为上方[业务交付当前表](#业务交付当前表)。保留历史以追踪判断变化。目标/验收见 [业务交付计划](agent-team-delivery-plan.md)，范围变化见 [ADR 0080](adr/0080-three-plane-business-delivery-roadmap.md)。

| Milestone | 状态 | 已有事实 | 未关闭的退出条件 |
| --- | --- | --- | --- |
| B1 完整单任务服务 | `IN_PROGRESS` | main `c93e31b` 的真实 Pi 订单报价已独立 ACCEPTED（34030199172）；候选显式取消/冷恢复通过；最新 `c619985` 的两类自动超时、终态查询/Collect、冷恢复通过 | 停止中途故障矩阵；长写事务下查询/停止响应上界；独立审查与最终版本正常业务/停止组合验收、主线合入。候选子条件通过不等于 B1 完成 |
| B2 受限 Agent Team | `PLANNED` | ADR 0019 已有计划接纳组件，ADR 0080 已确认受限产品目标 | approved plan 耐久物化/调度、两个到三个独立实现任务、集成候选业务验收、局部 replan、暂停恢复；不能以子 Run 全绿替代 |
| B3 长期运行与正式支持 | `PLANNED` | I186-R2–R6 组件和历史测试可复用 | B2 同路径故障与长历史测试、升级/恢复、#212 signing/notarization、Linux server 实机 gate、受保护 stable release |

2026-09-07 核对远端 `main@ba2196bea33e6f007809f75f9671928c892bfa11`，包含 #266 的正常业务证据；正常业务实机 source 为 `c93e31b`。停止候选 `c619985` 已推送 `feat/b1-stop-lifecycle`，没有 localMergeSha 或远端 merge，不得把其能力说成 main 已启用。两类超时均使用其同一 candidate binary SHA-256 `a55e680713258f357c3846473ba2f1ed53a685764f5d8a52cda5858545883aa3`；历史 source 的成功不替代最终组合验收。

### 历史实施检查点（不覆盖上方当前表）

本轮不改变 Run/Goal 持久化、授权或生产 selector。最先执行参考 oracle 正反例，再沿 B1 的 fixed server 路径收集真实业务证据；不新起另一套 controller。I186-R0 PASSED、R1 IN_PROGRESS/INTEGRATED、R2–R6 IN_PROGRESS/COMPONENT 保持。

B1 候选实现同时把 `Status` 与长 mutation mutex 解耦，使用单独 lifetime 读写锁保护 session 关闭，并保留实时 owner 复核。新增 mutation-held、Close 并发和无效 receiver 测试；这不解锁其它 mutation，也不保证底层存储无等待。定向动态/race 与健康 session 实机延迟证据仍须 CI/后继 canary，不提前标记 B1 完成。

本轮本地验证：订单报价 oracle 的 5 个测试通过（含 28 条业务用例、典型错误实现、生成验收命令实执行与 oracle 摘要漂移）；T1 shell 回归与 9 个 evidence 回归通过；Darwin/Linux 定向 vet、staticcheck、architecture check 与 Darwin CLI test compile-only 通过。另新增 Go 合同测试，直接验证 marker/order-quote renderer 的真实 Task 输出；它与 Status 动态/race 测试交由 CI 执行。现有固定二进制在新工作区报告 `self-local-profile-mismatch`，没有绕过或冒用历史身份；本轮未启动实机 Worker，也未签发 Decision/发布。

当前在途为同一 `feat/launcher-v2-production` 分支上的 S3 连贯实现，不按内部文件拆 PR：已编写 v2 journal writer、command session 与 live-session reconnect 分类，复用代际无关命令语义，保留 v2 exact decode/digest/receipt。未有 intent 才允许一次恢复执行；pending intent 保留 intervention；committed receipt 原样返回。新增连续两次 Core restart/丢握手、过期 receipt replay、不同 digest 冲突、伪造 A0/peer/旧代拒绝测试；重连仅复制 checkpoint 与目标 receipt，避免每次扫描全部历史。

候选 `485c606` 的 [CI 33939567946](https://github.com/chiga0/marshal-harness/actions/runs/33939567946) 暴露合法 journal 字符串中途截断被拒绝；根因是共用 parser 未接纳 Go `io.ErrUnexpectedEOF`。本轮修复并增加逐字节截断与损坏输入回归，完整非法记录仍在修复前拒绝且不修改文件。候选未全绿，不重跑同一失败 head、不合并、不升级 B1。修正及 reconnect 动态测试须由后继 exact-head CI 验证；transport、Core v2 subprojection、零 active/pending v1 admission 与实机链仍未完成，生产 selector 保持 v1。

后继 `b0780bd` 的 [CI 33940177093](https://github.com/chiga0/marshal-harness/actions/runs/33940177093) 已通过 Ubuntu quality、Linux 双架构与 secret scan，记录时 macOS quality 仍在运行。本轮进一步把 exact v2 bootstrap 接入已有 inherited server 入口，使用 v2 journal leaf、v2 mechanics、v2 handshake/command/reconnect；共用 held file/directory/socket 检查显式选择代际，不翻译 v2 为 v1。新增 Unix socket 全生命周期/receipt 恢复与混代 journal、输出篡改回归，bootstrap 读取也增加有界等待及 context 取消。v2 闭合握手合同没有 rejected 变体，因此错误/busy 连接只关闭，不生成非法 v2 或 v1 响应。此候选仍未完成 Core 的 v2 producer/subprojection、Attach 与 rollout admission，尚无真实 Pi 业务链证据，不能生产切换或升级 B1。

`b0780bd` 的上述 CI 后续已全部通过。server 候选 `5e1519a` 的 [CI 33940718334](https://github.com/chiga0/marshal-harness/actions/runs/33940718334) 通过 Ubuntu、Linux 双架构与 secret scan，macOS 完整生命周期测试在 Close 处超时，整轮为失败。本轮新增 `StartV2` 与 `ClientV2`：固定同一 Marshal image、empty env、inherited FD；准备/重建证据绑定完整 generation、两个 genesis、control directory 与命令前后 anchor，响应丢失或篡改保留 pending 并停止连接内重试。新测试通过客户端 API 连接同一 inherited server 的完整 bind→spawn→resume→inspect→collect→close 路径，但 mechanics 明确为 Fake，不冒充实机。已修正上轮 wire 测试 Close 输入遗漏的两项必需终结事实摘要，没有放宽关闭校验；同时修正 rejected receipt 不应消费外部 authority head 的 journal/client 一致性问题，并增加拒绝后继续执行的回归。当前本地验证为 compile-only/vet/staticcheck/architecture，动态结果待后继 CI；ResultIngress/ProductionRuntime v2 事实接线、client reconnect/Attach、rollout admission 和真实 Pi 仍开放。

客户端候选 `1057418` 的 [CI 33941565125](https://github.com/chiga0/marshal-harness/actions/runs/33941565125) 已通过 Linux 双架构和 secret scan，记录时 macOS/Ubuntu quality 正在运行，不记为通过。本轮继续同一 S3 分支：新增 `ReconnectV2`，从 held v2 nonce/journal/socket 和当前 fixed Core 构造请求，连接前后校验文件边界及精确 journal 位置；恢复返回完整 generation、原始命令 A0、新 owner anchor、pending 和 typed replayed outcome。连续丢失两次恢复握手时 A0 不变，已提交 receipt 不重做；intent-only 的客户端锁定，不准继续发命令。测试新增三种 journal 分类、过期 receipt、已提交 bind 的旧/新 authority 分离、伪造恢复和 Unix wire 恢复后 successor resume；本地 compile-only、Darwin/Linux vet、staticcheck/architecture 通过，动态证据待新候选 CI。剩余主阻塞是 ResultIngress/ProductionRuntime 的完整 v2 subprojection 与调用链、Attach、rollout admission 和同一 fixed bytes 的真实 Pi 业务验收，B1 不升级。

`1057418` 的上述 CI 已最终全绿（含 macOS/Ubuntu quality），确认该 head 的 Close fixture 修正与 prepared client 回归通过；不延伸为后继 `aa0dd00` 恢复客户端的动态证据。本轮开始接入 ResultIngress：`NewSupervisorBootstrapPreparedV2` 直接从 exact v2 请求产生无原始 nonce 的完整代际投影，并进入现有 `appendPreparedAttemptTransitionLocked`/owner/Run/current-head/CAS 与冷重放路径；外层 `attempt-authority` 不改名、不新建账本。新增 fresh Attempt 上 v2 bootstrap append/reopen、重算摘要的错误 current head 写前拒绝、逐字段缺失/混代拒绝与旧 v1 原字节/摘要不变测试。当前本地 compile-only/vet/staticcheck/architecture 已通过，后继动态 CI 待执行；started、command intent/outcome、Attach 和业务结果链仍须完成后才能切换生产入口。

本轮进一步把 exact v2 `process-supervisor-started` 接入原有 Attempt transition、owner/current-ledger 校验和冷重放：started 的显式 `v2` subprojection 保留完整握手与 anchor，禁止与 legacy handshake 同时出现，并重算唯一初始 journal record digest。新 started 向当前 projection 传递完整 generation/control-directory mechanics anchor，跨 v1/v2 历史仍执行 session/process/directory/socket ABA 检查；旧 command/reconnect 消费者明确拒绝 v2 anchor，不能默默丢字段后当 v1 使用。已补 bootstrap→started 的 fresh Attempt 耐久链、冷重放、伪造初始 head、自洽但错误 bootstrap 引用/Core 冒充 Supervisor 的写前拒绝测试。本地 compile-only/vet/staticcheck/architecture 通过；`cfa0e1b` 的 CI 33942406526 记录时 Linux 双架构/secret scan 通过，macOS/Ubuntu quality 仍运行，新 started 动态验证待后继 CI。command intent/outcome、Attach/terminal/collect 和生产 cutover 仍开放，B1 不升级。

后继进展：`2d45f5f` 已推送 started 接线。CI 33942406526 已结束：Linux 双架构、Ubuntu quality、secret scan 通过，macOS 在新增重连测试的 socket address 准备阶段失败；Fake 目录位于 `/private/tmp`，却调用要求 cwd 内相对地址的生产 helper，尚未发生 reconnect 握手。候选改用该 Fake harness 已持有的短绝对 socket 地址，保留生产路径边界，不改变全局 cwd。当前继续接入 v2 command intent：原 RB1 recovery 子链按 exact v2 revision 写入完整准备证据摘要、generation 与 A0；fresh Attempt 的 bootstrap→started→bind intent→cold replay 可恢复同一准备证据并要求 exact payload 重建，错误 started 引用及混代 recovery header 写前拒绝。该新增链已完成本地 compile-only、vet/staticcheck 与 architecture 检查，动态回归待新 CI；outcome、Attach/terminal/collect 与实机生产切换仍未完成，B1 保持 `IN_PROGRESS`。

## 历史 checkpoint 与技术证据映射

本 Roadmap 交付[整体架构](architecture.md)定义的长寿命、可自托管、确定性 Control Plane。Local MVP 是已经可用的 embedded/local 先行实现与持续回归基线，不是 Marshal 的最终产品范围。

> **2026-09-04 fixed server T1 integration closure**：PR [#252](https://github.com/chiga0/marshal-harness/pull/252) 已合入 `main@b39c346`，该 head 的 [required CI](https://github.com/chiga0/marshal-harness/actions/runs/33850189142) 全绿；随后 [run 33851302323](https://github.com/chiga0/marshal-harness/actions/runs/33851302323) 从同一 head build-once 固定 `bin/marshal`，用真实 Pi `0.84.4` 完成 authenticated server start 到 `RUNNING`、response-loss、受控 server crash、strict-successor recovery-before-ready 与 same-key exact receipt replay。artifact `9928466621` 绑定 candidate SHA-256 `sha256:180364721455b23f18aa4e72a4e2059683fb772eb39bb95b0e00f107cb35c4c5`、CDHash `f940ce13b175951b27105356958bf31c64bd62d7`、owner epochs `[1,2]`；`spawn=1`、`resume=1`、`bind-authority=2`、pending/receipt 各一、`cliFallback=false`。因此 ADR 0076 的 fixed transport/T1 capability 升级为 `INTEGRATED`；ADR 0062 full lifecycle、I186-R2–R6 与 stable 成熟度不升级。下一相邻纵切是让 collect/verify/review/Decision 通过同一 resident Port 到 `ACCEPTED` 的 T2，不能回退到另一个 CLI writer。

> **2026-09-04 exact-head CLI closure**：`main@c2198e3` 的 build-from-head 固定 candidate 已在 [run 33788766642](https://github.com/chiga0/marshal-harness/actions/runs/33788766642) 用真实 Pi `0.84.4` 单 Attempt 穿过 existing-worktree path-B、sealed start、terminal collect 与 Verification到 `REVIEW_PENDING`；独立 reviewer `P0=0/P1=0` 后，[finalize 33790168049](https://github.com/chiga0/marshal-harness/actions/runs/33790168049) 持久到 `ACCEPTED`，carrier checker 通过，artifact `9907034593` 的 zip digest 为 `sha256:e212f4e817fdc774828a7eaffa6b584eeeb5b243af98e486289e2c94362143b7`。该 marker canary 满足 #226 的 PR #228 关闭合同，#226 已关闭；它不满足 ADR 0075 对 #224/#225 的 `m13-e2e-dogfood` 复杂任务、default umask、“散文 + 单 JSON”与时间/token 指标要求，因此 #224/#225 继续开放。该 CLI 证据本身不是 fixed server T1/T2、managed signing/notarization、Linux stable 或 `RELEASED` 证据；fixed server T1 已由上方后继 canary 关闭，当前下一纵切是 T2 与 recovery/fault matrix。

> **2026-09-03 fixed server S3 candidate**：`main@0a0c73e` 已包含 S2 authenticated AF_UNIX endpoint；当前相邻切片只增加 `Status`/`StartRun`/`InspectRun` 的 bounded HTTP adapter，并把 authenticated request binding 精确传给 S1 immutable delivery。`StartRun` 固定执行 `pending → current RB1 reconcile → 至多一次 Port handoff → current RB1 reconcile → receipt-ref → response`；只有 exact receipt 与 current projection 同时匹配才报告 success，错误/断连后的未知结果保持 pending，receipt replay 不产生第二次 mutation。canonical JSON、closed headers/operations、独立 header/body/application/write deadline、1 MiB request/response、32 inflight/32 queue 和 disconnect cancellation 已进入组件测试。生产路径不创建匿名 Mach-O；S3 仍为 `COMPONENT`，S4 resident `marshal control-plane serve` + 真实 Pi restart canary 与 T2 `ACCEPTED` 仍是升级门禁。

> **2026-09-03 fixed server S2 candidate**：PR [#230](https://github.com/chiga0/marshal-harness/pull/230) 的实现 sourceHead `7ee24cc` 已建立 canonical `.marshal/runtime-v1/control` 下 owner-epoch scoped AF_UNIX socket/token、held descriptor/current-name recheck、peer/current-owner/fixed-binary联合身份验证、单次nonce+HMAC握手，以及16 KiB/5s/64连接硬边界。client只接受current `FixedEndpointAuthority`并在握手前后重验owner；生产路径不生成随机Mach-O、不使用`/tmp`/TCP/cwd切换，也不允许调用者覆盖locator。S2仍为`COMPONENT`：S3 bounded request+delivery、S4 resident `marshal control-plane serve`、真实Pi restart/response-loss与T2 `ACCEPTED`尚未完成。当时 #226 降为CLI-only并行债务、不再串行阻塞fixed server；它现已由 2026-09-04 exact-head checkpoint 关闭。server不得回退到CLI mutation writer。

> **2026-09-03 fixed server S1.3 candidate**：S1 durable delivery 已形成可跨 cold owner successor 重证的 `pending → current RB1 read-only reconcile → receipt-ref`。稳定 authority-root identity 排除 append 引起的 mutation timestamp，但仍绑定 canonical repository、held object、closed name、owner/mode/type；current physical owner 必须从同一 RB1 证明旧 pending 的 exact owner fact/acquisition 到当前 epoch 的连续无分叉 lineage，并重新核对 request/intent/root，伪造 fact/acquisition/epoch 固定 fail closed。代码 sourceHead `62c1aed` 的本地双平台 compile-only、vet、staticcheck 与 diff-check 已绿，最终分支仍以 required CI 全绿为合入条件。S1 关闭后立即进入 S2 endpoint auth → S3 bounded HTTP → S4 resident integration；AF_UNIX、用户入口与真实 Pi canary 尚未完成，能力保持 `COMPONENT`。

> **2026-09-01 stable server cutover checkpoint**：独立 `cmd/marshal-server` 已按 ADR 0062 删除 child `marshal task run`、`--marshal-executable`、Provider registration 写入口和 Worker selector 初始化，并在读取 mutation body 或写入幂等账本前统一 fail closed；查询与事件投影保留为非生产兼容面。该 checkpoint 只关闭第二 authority root 的旧实现风险，不等于 fixed server 已完成：`marshal control-plane serve`、in-process `PublicApplicationPort`、authenticated loopback owner/session 与 restart/response-loss recovery 仍是下一相邻 stable 切片，R2–R6 状态不升级。

> **2026-09-01 Run start Port cutover checkpoint**：`internal/server` 已删除生产形状中的 `RunExecutor func`，Run start 现在只通过注入的 `PublicApplicationPort.InspectRun → PrepareRunStart → StartPreparedRun`，transport pending intent 绑定 exact sequence/authority head 以及 prepared Attempt/`preparationDigest`，receipt 只携带 path-free current-ledger `RunProjection`；正常 start 返回 `RUNNING`，结果收集、Verification 与 Decision 不再混入该端点。response-loss replay 的定向测试证明不重复 Prepare/Start，且拒绝把其它 Attempt 认领为本 intent 的结果。该 checkpoint 仍是 `COMPONENT`：server 其余 mutation 尚未迁移到 Port，owner-scoped multi-Run `ProductionRuntime` session、fixed CLI server mode、authenticated AF_UNIX 与真实 restart canary 仍未完成。

> **2026-09-01 owner-scoped multi-Run session checkpoint**：`internal/productionruntime` 已把 repository-wide owner/ResultIngress 生命周期与 Run-scoped lease/runtime 生命周期拆开。`RepositorySession` 一次取得并 claim exact durable owner，多个 Run runtime 在同一 owner epoch 下顺序或并发借用；Run 关闭只释放自身 lease/borrow，Session 关闭等待借用归还后才关闭 ingress 并释放 owner。测试覆盖连续两个 Run runtime、第二 Session 竞争、关闭屏障、关闭后拒绝与 successor epoch。该变化不新增 authority/persistence/lifecycle 合同，仍是 ADR 0062/0066 的 `COMPONENT` 实现；Run resolver/application multiplexer、fixed `marshal control-plane serve`、authenticated AF_UNIX 与 restart canary 仍是升级 `INTEGRATED` 的必要条件。

> **2026-09-01 session-backed application checkpoint**：fixed CLI 的一次性 production assembly 已收敛为 `sealedRepositoryApplication`：repository-wide owner/ingress/Provider/dispatch/held StateRoot 只打开一次，每个 application operation 通过 descriptor-bound Run resolver 组合短寿命 Runtime；进程启动先枚举 durable Run 并对全部 `RUNNING` Run完成既有 attach/rebind，成功后才报告 application `ready`。`task run` 与后续 fixed server 因而共享同一个 `PublicApplicationPort` adapter。同步新增 fixed composition exact namespace 注入，机械拒绝 compatibility server 的 `repo:<root>` 与 production `<root>` 混证据。该 checkpoint 仍为 `COMPONENT`：`marshal control-plane serve`、authenticated descriptor-relative AF_UNIX、peer/owner handshake、delivery ledger 与 restart/response-loss canary 尚未完成。

> **2026-09-01 RC1 publication checkpoint**：[`v1.0.0-rc1`](https://github.com/chiga0/marshal-harness/releases/tag/v1.0.0-rc1) 已按 ADR 0068 发布。annotated tag object `e99326f` 精确指向 `c1407bd`，candidate SHA-256 为 `f9ed7fa59d05f5e71fef7164b8015240497e1d18e25ef1d3f8e199c1378a3774`。真实 Pi `0.84.4` canary/finalize runs `33504020360`/`33504247271` 已到达 `ACCEPTED` 并生成 current receipt/carrier；exact-head CI run `33502847249` 三项 required jobs 全绿；release run `33506656403` 只消费同一 carrier 并创建 prerelease。外部下载与临时安装后的二进制 bytes 仍与 canary 相同。该 checkpoint 关闭 ADR 0068 的 local-dogfood prerelease distribution exit，但 ADR 0068 明确不授予 ADR 0052 的 `RELEASED`、production、managed、notarized、hardened、server、Linux 或 stable authority；R2–R5 保持 `COMPONENT`，R6 更新为 `IN_PROGRESS/COMPONENT`。

> **2026-09-01 activation V2 checkpoint**：`main@4fa1343` 的 required CI 已全绿；RC1 run phase `33477653933` 已用 same-bytes candidate 到达 `REVIEW_PENDING`，但 finalize `33477984364` 在另一台 GitHub macOS runner 上因 V1 activation/identity subject 绑定临时 `device/inode` 而正确 fail closed。维护者接受 [ADR 0073](adr/0073-dogfood-activation-v2-host-portability.md)，本切片同步升级 activation→observation→attempt/applicability→verification→review 的 V2 lineage，保留每台宿主 current-path fd object 的 device/inode/ABA 强校验，并删除 finalize 以相同 `activationId` 重签发 activation 的 workaround。该 checkpoint 只关闭跨 runner 证据模型缺口；必须在 exact-head required CI 全绿后从新的 run phase 重跑 V2 canary，并由 finalize 达到 `ACCEPTED`、产出 receipt/carrier，才可进入 RC1 publication workflow 收口。R1–R6 成熟度暂不升级。

> **2026-09-01 sealed-migration skip 标记 checkpoint**：ADR 0068 zero-selector cutover 已落地（`b1e274f`）——`MARSHAL_WORKER_EXECUTOR`/`MARSHAL_EMBEDDED_SANDBOX`/`MARSHAL_PRODUCTION_GATE` 三个 env selector 及其 direct `Adapter.Run` fallback 从 production 链移除，`FROZEN_SELECTOR_DEBT` 归零为零容忍扫描；compat 生命周期套件（qwen fallback、ThroughVerify、autoflow）退役，dogfood 套件转真实 Pi + sealed fail-closed。同期发现 main 测试套件自 sealed Run-start 门禁落地起即红（被 lint 失败掩盖）：旧 fixture 直写 `READY→RUNNING`，被 `runstore.Append` 门禁与 `WriteSnapshot`↔journal 等价验证双层 fail-closed 拒绝，唯一合法产生路径是 darwin real composition + 真实 Pi 0.84.4。迁移路线已由维护者确定为 **darwin 组合驱动 + 双端 skip**：gate 命中的 184 项测试统一以 `sealedMigrationSkip`（per-package helper，含 1f520c8 半落地的 darwin production admission 执法）显式标记并保持可见，连同 processsupervisor `/private/tmp` 环境修复与 qoder checker darwin-identity 平台门禁。darwin real-composition 驱动是恢复这些套件为真实通过的下一个纵切前置；skip 只标记债务，不授予任何运行时路径豁免，R2–R6 状态不变。

> **2026-08-31 fixed CLI `ACCEPTED` checkpoint**：`main@3819462` 的同一 Darwin arm64 candidate bytes 已通过真实 Pi canary `RC1-PI-20260831-3819462`，由独立 Verification 与独立 ReviewDecision 进入 `ACCEPTED`；Decision digest 为 `sha256:5d50b624e41419ef32a1d7251481d5843ab001d3affe0ef6c8a6aad5465df5e9`。该结果证明 fixed CLI 的主生命周期可达，但不升级 R2–R6，也不授权 tag：exact-head CI 仍有 architecture red；ADR 0068 要求 production environment selector/direct fallback 为零；release workflow 仍缺 pre-tag immutable candidate、current-authority receipt producer/admission、RC1 单资产 tag 校验与 no-rebuild prerelease consumption。当前最短路径只处理这三项并在新 final bytes 上重跑 same-bytes canary，禁止回到横向组件扩张。

> **历史 checkpoint（2026-08-28）**：`main@44ee8c9` 已合入 durable server run controller；`main@d4b9647` 已收紧受支持的 production selector；`main@912f659` 已合入 ResultIngress admission→worker-result→Run journal crash-atomic 持久化/恢复。前置 Pi `0.84.3` canary 绑定 `sourceHead=d4b9647`，单 Attempt 通过 9 项 Gate 到 ReviewPacket/`REVIEW_PENDING`，但在该时点尚未导入独立 ReviewDecision、未进入 `ACCEPTED`。本段只保留当时证据，不覆盖上方 2026-09-01 当前状态。
>
> **2026-08-28 Darwin 进程生命周期合同 checkpoint**：[ADR 0056](adr/0056-darwin-process-observation-and-attempt-terminalization.md) 已于 `main@ecee8d4` 接受，冻结 Core-owned launch coordinator、admission/terminalization authority CAS、立即终止 dispatch eligibility、独立 `cleanup-completed` 与 cleanup binding release，以及 cooperative/non-detaching process-group 控制边界。实现与 production 接线仍开放，因此本 checkpoint 不升级 R3–R5。
>
> **2026-08-29 Run-start proof 纠偏**：[ADR 0065](adr/0065-sealed-run-start-proof-and-one-way-composition.md) 已接受，基于 `main@40fa493` 冻结 ResultIngress 的 owner/Attempt/generation 重验与 runstore 的 Run lease/head/state CAS 绝对分离，并以 shared-guard proof 和精确 composition AST gate 衔接。接受只冻结合同，S1/S2 尚未实现，不升级 R2–R5；旧实现候选不构成当前进展。
>
> **2026-08-29 S2 production factory 合同**：[ADR 0066](adr/0066-production-composition-owner-acquisition.md) 已接受。当前没有 fixed `./bin/marshal` production factory，`Runtime.Status` 仍为 `production-composition-incomplete`，owner lock 存在“acquisition 先于锁、successor acquisition 又必须锁内产生”的构造环，任意 `MARSHAL_STATE_DIR` 还允许同 repository 两锁两 ledger。接受合同只纠正 S2 为 scope-only lock → one-shot provisional `AcquireOwner` → exact replay 后 current verifier、canonical repository `.marshal`、唯一 Darwin arm64 factory/controller composition，以及 fixed `cmd/marshal` 本地 CLI mutation/inspect 只持有 `PublicApplicationPort`；`marshal control-plane serve` 后移为 S2 之后、release 之前的独立 ADR 0062 transport slice。接受不表示实现完成，不改变 R2–R6 状态或 ADR 0062 信任模型。
>
> **历史 Mac-first 合同接受（2026-08-29）**：[ADR 0067](adr/0067-darwin-ordinary-user-launch-and-attach-recovery.md) 与 [ADR 0068](adr/0068-mac-first-cli-only-lifecycle-preview-rc1.md) 已接受；提案 sourceHead 分别为 `1e05fb831c04a1c87e7f4ecdc677c97beb9d88e6`、`9cfa1b65275d2e23f18b958a05d027adec6af8fd`，唯一独立 reviewer 均确认 `P0=0`、`P1=0`。旧候选`a6a0d63`/`506a647`/`6298eae`继续冻结、不直接合入；当时冻结的S1′→S2′→Attach/rebind→terminalization→fixed CLI真实Pi+独立Decision `ACCEPTED`→same-bytes RC1顺序已由上方发布 checkpoint 完成，fixed server、managed signing/notarization与Linux stable仍为后继。

> **2026-08-29 producer P0 合同**：[ADR 0069](adr/0069-attempt-reservation-and-existing-worktree-allocation.md)已在定向修订 sourceHead `e2af179` 经独立 reviewer `APPROVE`（`P0=0/P1=0`）后由维护者接受。`attempt-reserved`只是RB1 creation-once reservation；dispatch lookup-before-claim、full Attempt与`attempt-opened`随后产生，sealed Run successor才写Attempt/计budget。Existing-worktree Bind/Receipt/Release的唯一authority同样是RB1；固定sidecar仅为可重建projection，锁序owner→Run→RB1→projection，release绑定current terminalization/cleanup/process-terminal链。ResultIngress pathname reopen和`OpenOwner`后才可`ObserveCurrentCore`仍是ADR0066实施P0。接受只冻结合同，实现尚未完成，不升级阶段。

> **2026-08-30 implementation checkpoint**：RB1-authoritative existing-worktree Bind/Receipt/Release 与 descriptor-bound recovery projection 已于 `main@259edd3` 实现；Linux staticcheck U1000 的跨平台 build-graph 修复已于 `main@60291e8` 推送，ResultIngress 缺失 verifier/stale-owner 顺序修复已于 `main@04c8fa9` 推送。该 exact-head CI 的 secret scan 已通过，Ubuntu/macOS quality 仍在运行，整体尚未宣称全绿。RC1 build-once distribution contract（`main@2d7da6a`）、installer exact opt-in/fail-closed guard（`main@e6a78a3`）以及 immutable carrier checker/receipt Schema/hostile matrix（`main@66523d9`）均已实现并独立审查。这些都是 component/admission 资产：真实 RC1 canary 在 `task plan` 阶段确定性暴露 CLI production selector 未装配 per-Attempt `ExactProcessRuntime`/`ExactAllocationRuntime`（`launch identity unavailable`）；完整 S1′（S1′-A reservation/full Attempt + S1′-B held descriptor/prepared proof/sealed successor，含 item 5 borrow seam/门禁）尚未进入 `main`，`3abed5a` 仍只是未合入候选；S2′、Attach/rebind、terminalization、最终 fixed-bin Pi→独立 Decision→`ACCEPTED`、真实 same-bytes canary/carrier、tag、GitHub prerelease 与 release asset 仍未完成，因此 R2–R6 状态不变。
>
> **2026-08-30 producer seam checkpoint**：`main@a6482db` 合入了 ResultIngress 当前账本重解析的 `PrepareMacRunStart`/`CommitMacRunStart` seam，并删除了未接线且违反 architecture gate 的 `productionruntime` factory。定向测试、architecture check 与 vet 通过；该 seam 只投影并提交已存在的 durable PreparedExecution，不产生 owner/allocation/launch/process facts，因此不升级 R2–R5。全包 ResultIngress Darwin owner-lock fixture 仍有既有失败，fixed CLI 真实生产接线、Pi→独立 Decision→`ACCEPTED` 与 RC1 发布仍未完成。

> **2026-08-30 RunStore descriptor checkpoint**：`main@46e0054` 以 `--no-ff` 合入 `ac5fd20`，新增 `NewFromStateRootDescriptor`/`NewAt`，使 existing-only RunStore acquisition 可以沿 held StateRoot descriptor 打开 `runs/<runID>`，并保留 Lease 的描述符，降低 StateRoot pathname TOCTOU/ABA 风险。随后 `main@109f35d` 对 descriptor-bound Store 的 pathname API 增加 fail-closed 哨兵根路径、禁止误用 `Acquire`，并以 Store mutex 保护 descriptor close/acquisition。两次切片均通过 `go test -race ./internal/runstore`、`go vet ./internal/runstore` 与 `git diff --check`；ResultIngress/Execution/App 的既有 sealed Run-start fixture 仍失败，故仅为 component 补强，不升级 R2–R5，也不代表 production composition 已接通。第二次切片按维护者指示未等待独立 reviewer，需在后续 production 接线前补充审计。

补充：`ceb8b39` 修复 runstore canonical journal 写入，`822dcd3` 对齐 sealed Run-start 测试 fixture，`a7e9f93` 增加带 authoritative time 与 lease expiry/ack-deadline 校验的 `CurrentByAttempt` dispatch lookup。上述提交均已同步 `origin/main`，只补强组件门禁，不改变 R2–R5 的 `COMPONENT` 状态。

> **2026-08-31 Pi-first Darwin checkpoint**：候选分支 `feat/pi-first-architecture-fix@d630aa2`（基于 `5b95ed1`）已用固定 Node/Pi bundle 和空环境通过真实 `TestSealedChainReachesRunningWithRealPi`。该候选修复 live allocation 重封装、空环境 spawn payload、Darwin 工作目录 `NOTE_ATTRIB` 误报、普通 Go 进程 FD3/4 误判，并移除临时调试输出；证明 sealed launch chain 可达；尚未接入最终 fixed `./bin/marshal` 的完整 Run→worker.completed→独立 Decision→`ACCEPTED`，不升级 `I186-R2–R5`，不宣称 RC1/stable。`go vet`、architecture-check、diff-check 通过；`staticcheck` 未安装，productionruntime 的 3 个既有 owner-lock fixture 失败及严格 CLI E2E 的 `worker.completed` 接线仍待单独收敛。Codex 本轮未启用。

> **2026-08-31 固定 CLI 复测**：候选分支在 `5b95ed1` 之后修复了 Darwin 普通进程 FD3/4 误判 inherited child 的问题，并删除了临时调试输出；严格 fixed `./bin/marshal` E2E 已通过 `plan→approve→run.start-outcome`，但 `worker.completed` 尚未出现。原因已收敛为 sealed READY 分支当前只启动 Run-start/supervisor，尚未把带真实 WorkerRequest 的 Pi 执行、结果接纳和独立 Verification 接入同一次 `task run`。该候选仍不升级 `I186-R2–R5`，不宣称 RC1/stable；Codex 未启用。

## v1.0 生产纵切

Milestone 状态与能力成熟度是两个维度：

| 成熟度 | 含义 |
| --- | --- |
| `DESIGN` | ADR、Schema 或合同存在，尚无实现。 |
| `COMPONENT` | Package、类型与测试存在，但真实 composition root 不可达。 |
| `INTEGRATED` | fixed `cmd/marshal` 的 CLI mutation 或 `marshal control-plane serve` 经唯一 production factory/`PublicApplicationPort` 可达，真实 Agent 与结果 bytes 穿过该路径；独立 `cmd/marshal-server` 不计。 |
| `RELEASED` | 集成路径通过 v1.0 release gate 并进入受支持产物。 |

| 阶段 | 状态 | 成熟度 | 当前结论 |
| --- | --- | --- | --- |
| `I186-R0` | `PASSED` | `DESIGN` | rebaseline、ADR 与 baseline evidence 已完成。 |
| `I186-R1` | `IN_PROGRESS` | `INTEGRATED` | final candidate `c1407bd` 的真实 Pi 已由 fixed CLI 在 Local allocation 中执行，并把真实 result bytes 送入 ResultIngress。 |
| `I186-R2` | `IN_PROGRESS` | `COMPONENT` | final candidate 的真实 fixed-CLI 纵切已穿过 reservation/full Attempt、sealed successor、current ResultIngress，并在 fresh process 中重读为 `ACCEPTED`。完整 crash/response-loss/ABA/forgery/replay 负向矩阵仍未关闭，因此不升级。 |
| `I186-R3` | `IN_PROGRESS` | `COMPONENT` | RB1-authoritative existing-worktree、PreparedExecution、exact resume、fixed Supervisor、Attach/rebind 与 terminalization 已由 final candidate 真实穿过；production environment selector/direct `Adapter.Run` fallback 已归零。source/cwd/process/binding drift 的 stable 矩阵仍开放；ordinary-user 不得描述为 hardened。 |
| `I186-R4` | `IN_PROGRESS` | `COMPONENT` | success path 的 authority CAS、terminalization 与 fresh-process reread已由 final candidate 证明。退出前仍须覆盖 kill/restart/cancel/timeout/response-loss/retry、重复 start、未知或复用 PID 的唯一可回放结论；fixed server 为当前 stable 后继。 |
| `I186-R5` | `IN_PROGRESS` | `COMPONENT` | final candidate 的 fixed-bin real-Pi canary 已由独立 Verification/ReviewDecision 进入 `ACCEPTED`，同一 receipt/carrier 已用于发布；旧 production bypass 已归零。完整 recovery/cutover fault matrix 未关闭，因此不升级。 |
| `I186-R6` | `IN_PROGRESS` | `COMPONENT` | ADR 0068 local-dogfood prerelease exit 已关闭：same-bytes candidate、receipt/carrier、required CI、annotated tag、GitHub prerelease、外部下载与安装验证均完成。ADR 0052 的 stable server、Issue #212 managed signing/notarization、Linux gate、完整 conformance 与 `RELEASED` 成熟度仍开放。 |

v1.0 仅支持单节点、单用户、可信仓库、至少一个真实 AgentProvider 和一个真实 Local/Container SandboxProvider。Cloudflare 完整生产拓扑、HA、多用户/多租户、全部 Provider hardened 矩阵、完整 SDK/Web UI 与 Goal DAG 延期到 1.x。

上述 RC1 最短路径已形成历史发布证据；当前顺序由页首 B1→B2→B3 表定义。component checker 或历史 canary 不等于当前 final bytes publication authority，不能提前升级阶段。

## 快速收敛线路交付记录（component checkpoint，路线重置前 2026-08-27 交付）

> 以下记录为 Lead 快速收敛治理（单 Lead + 多 Sub-Agent、停用独立 reviewer 轮转、Lead 直并，仅保留防错误发布/数据破坏/trust-boundary ADR 硬约束）下的交付证据。状态判定一率以 ADR 0052 的生产可达性口径为准：这些资产记为 component checkpoint，不另行宣称阶段 DONE。

- R3-D/E/F 快速收敛交付：R3-D `ec13ee7`（internal/revokedrain 撤销分级处置）+ `0a9b3b6`（internal/attemptgate per-Attempt 双 binding recheck + 证据边界负测）、R3-E `c47b4c2`（internal/locationattest claim/fact 分型）、R3-F `d89c65e`（internal/failureclass 失败分类 authority），[ADR 0049](adr/0049-location-attestation-and-failure-classification-authority.md) 冻结 E/F 合同并修订 ADR 0043 §5；Exit Gate 证据对照与 finding 关闭见 [audit-report.md](audit-report.md)；R3-D1/D1b 历史 Marshal Run 的 REJECTED 记录保留，其中 D1b 实现经修复公式后采用。Issue #209 与 PR #211 已闭环；Issue #210/#212 转入 dogfood 沉淀。R0 产物见 [i186-r0-baseline-report.md](research/i186-r0-baseline-report.md)；Pre-R4 contract gate 四项（hot-path authority、JIT admission、protocol migration、Candidate identity）在 R4 启动实现前随 R4 首批切片补齐。
- R4 快速收敛交付：Pre-R4 四项合同（hotpath `f65cfaf`、jitgate+candidateid `c4c8b69`、protocolrev `ab7b263`）+ 单一恢复模型（recovery `34f70d3`，故障矩阵八类唯一幂等结论 + explain 等价 API），[ADR 0053](adr/0053-pre-r4-contract-gates-and-single-recovery-model.md)（原编号 0050）冻结全部合同并修订 ADR 0044 冷热路径条款；证据与 finding 关闭见 [audit-report.md](audit-report.md)。
- R5 快速收敛交付：cutovereq/effectsink（`b3e193d`）+ worker executor seam 与 sandboxbridge 生产接线（`ab93174`）+ golden 判定 harness 与 rollback 演练（`6285a15`）+ [ADR 0054](adr/0054-cutover-equivalence-and-effect-sink-fencing.md)（`6144f24`，原编号 0051）+ 默认翻向与 fencing 修复（`20c5609`）；Exit Gate 对照与范围标注见 [audit-report.md](audit-report.md)。
- R6 快速收敛交付：perfbench 基线（`1f81286`）+ soak harness 与 recovery 两缺口修复（`0208964`）+ bridged 孤儿 allocation 对账（`97147a1`）+ 路径级 accelerated soak（`dc6d7ed`）+ 真实 Pi canary（`run-i186-r6-canary`，9 Gate 独立验证通过）；Exit Gate 对照与 honest gaps 见 [audit-report.md](audit-report.md)。

| Milestone | 状态 | 证据 |
| --- | --- | --- |
| 0：Toolchain 与 Contract | `PASSED` | [验收报告](milestone-0-report.md) |
| 1：State Machine 与 Run Store | `PASSED` | [验收报告](milestone-1-report.md) |
| 2：Git Worktree 与独立 Verification | `PASSED` | [验收报告](milestone-2-report.md) |
| 3：Review 与 Rework Loop | `PASSED` | [验收报告](milestone-3-report.md) |
| 4：首个真实 Worker Adapter | `PASSED` | [验收报告](milestone-4-report.md)；GitHub Actions `30879438415` |
| 5：GitHub Draft Publisher | `PASSED` | [验收报告](milestone-5-report.md)；主分支 CI `30889069165`；[真实 Draft PR #1](https://github.com/chiga0/marshal-harness/pull/1) 与 PR CI `30889190854` |
| 6：其余 Adapter 与 Recovery 加固 | `PASSED` | [验收报告](milestone-6-report.md)；真实受监督 cmux Pilot 通过；Full MVP E2E Run `m6-mvp-e2e-r3-20260805` `ACCEPTED`，[Draft PR #2](https://github.com/chiga0/marshal-harness/pull/2) 与 PR CI `30974239712` 全绿 |

embedded/local 先行实现的 Local MVP 定义达成：标记 `USABLE`。

## Qoder/Codex production authority 共同阻塞

[ADR 0038](adr/0038-agent-production-authority-provider.md) 已于 2026-08-18 接受：它为 ADR 0034/0037 已冻结但尚未由真实宿主提供的外部 authority 层冻结共享 `AgentProductionAuthorityProvider` Port，包括独立在线 verifier、OS isolation/audit receipt、host attestation、monotonic fence、held-fd identity、原子 authority bundle、stopped-child launch receipt/workload barrier，以及 rotation/revocation/crash reconcile。接受只允许进入实现，不表示 Qoder/Codex 或任何当前宿主已 `supported`。

接受本 ADR 不改变 Roadmap 状态，也不表示 Qoder 或 Codex 已可生产调度。Linux 只有在对应 profile 的平台机制、真实 credentialed probe 与独立 conformance 全部通过后才是候选；Darwin 的 Qoder/Codex profile 在等价强制机制与后续合同通过前保持 `unsupported`。关闭条件见[设计审计报告](audit-report.md)中的 `AGENT-AUTHORITY-*` open findings。

## Mac-first Adapter 阶段性证据（2026-08-24）

本节只记录当前宿主的 ordinary-user 证据，不改变 M6 已通过结论，也不把 M10–M13 或 v1.0 标记为完成。Qoder 与 Qwen 两行证据分别绑定 commit `9410e75`（fix(adapter/qoder)：未知 system 帧从 fail-closed 改为非语义忽略，adapterVersion bump 到 `0.1.7`，conformanceEventContract 保持 `v7`）与 commit `2c67e7e`（feat(adapter/qwen)：版本策略从精确锁改为 semver 范围 `>=0.21.5 <0.22.0`，`0.21.15` 现在 supported）。ADPT-03 行绑定 commit `b35d374`（fix(adapter/qoder)：argv 预授权 `--allowed-tools Bash`，版本下限 `1.1.23→1.1.27`，adapterVersion `0.1.7→0.1.8`，transport digest 不 bump）。

| Adapter | 当前事实 | 结论 |
| --- | --- | --- |
| Qoder CLI `1.1.27` | `marshal doctor` 在 macOS ordinary-user profile 下报告 `configured=true`、`registered=true`、`compatibility=supported`、`adapterVersion=0.1.7`；conformanceEventContract 保持 `v7`；固定 executable `/Users/gawain/.qoder/bin/qodercli/qodercli-1.1.27`，digest `sha256:fd36420ae0e740f7f3fb7f62e9df23aa70df400aad55fc7e7e48e0edc0ce8e2`。 | 该证据身份已被 ADPT-03（adapterVersion `0.1.8`）替代失效，仅作历史记录；不得复用为新派发证据。 |
| Qoder CLI `1.1.28`（ADPT-03） | 2026-08-24：adapter `0.1.8`（argv 含 `--allowed-tools Bash`、版本下限 `1.1.27`）以显式 `MARSHAL_QODER_PATH` 指向 qodercli `1.1.28`（digest `sha256:14b5aa00198986c2299084e5d87479d648db47fc4b85aaecb572e1cff3a1c4aa`）、`MARSHAL_QODER_MODE=ordinary-user`，`marshal doctor` 报告 `configured=true`、`registered=true`、`compatibility=supported`、`authorityMode=ordinary-user`，并通过了 Run `run-m10-wire-02-r2` planning selection 的真实 version/capability probe（CapabilitySnapshot digest `sha256:52c5c45b16e8e6bcc390772e869de9ede48d9ea5cd6469e86b2632fffe68fba9`）。 | 已完成晋升阶梯“真实只读 live probe”级证据并进入首个低风险写任务；仍需 fresh live Worker smoke、transcript attestation 与独立只读 conformance，未宣称 production authority。 |
| Codex `0.145.0` | `mac-codex-ordinary-smoke-r19-20260821` 与 `mac-codex-ordinary-smoke-r20-20260821` 各由唯一独立 reviewer 审查并进入 `ACCEPTED`；路径 `/opt/homebrew/Caskroom/codex/0.145.0/codex-aarch64-apple-darwin`，digest `sha256:1da3f4e0e96028b8a771814293c3033dafd1971f943f6c7e79b0897fe705f590`。 | Mac ordinary-user 运行证据已收敛；两个 smoke 仅为诊断任务，不产生产品代码 diff、发布或合并。 |
| Qwen Code `0.21.15` | `marshal doctor` 报告 `configured=true`、`registered=true`、`compatibility=supported`、`adapterVersion=0.1.0`、`binaryVersion=0.21.15`；当时版本策略为 semver 范围 `>=0.21.5 <0.22.0`。 | 原范围准入证据已闭环，可调度普通 Worker；0.22 边界现由下一行候选替代验证，二者都不升级为 hardened authority。 |
| Qwen Code `0.22.0`（2026-08-28 候选） | macOS ordinary-user workspace live adapter 已通过，输出与 workspace 写入受有界协议校验。 | 可作为 ordinary workspace 兼容 Worker；不是 `LaunchCapable`，不得用于 production profile 或 hardened authority，也不改变 R1–R6 状态。 |

当前阶段的非阻断问题是两个 Codex smoke TaskSpec 文案仍引用 `r15` 路径，且 Markdown 产物声明为 `application/json`；后续 successor 应一次性修正文案，不为此逐项轮转 rework。普通用户模式不等于 hardened authority、APAP、sandbox 或 Linux authority。

## 已知阻塞与进展：Issue #25 已关闭，Issue #30 部分满足

公开 [Issue #25](https://github.com/chiga0/marshal-harness/issues/25) 与 [PR #24](https://github.com/chiga0/marshal-harness/pull/24) 暴露的「全部 required checks 成功且 PR 已合并后，`marshal task accept` 把 Run 永久置为 terminal `BLOCKED`」**已修复并关闭**（typed reconciliation 实现已合入：[PR #106](https://github.com/chiga0/marshal-harness/pull/106)，[ADR 0026](adr/0026-scm-merge-receipt-and-publication-reconcile.md)）：

- 活路径：`marshal task accept` 内联识别已合并且 required checks 全绿的 PR，采集不可变 `SCMMergeReceipt` 后经现行 checks-passed 路径进入 `ACCEPTED`；未合并 PR 仍走原 checks 观察流程；
- 补偿路径：`marshal task reconcile` 按 [ADR 0026](adr/0026-scm-merge-receipt-and-publication-reconcile.md) 冻结顺序，以 `SCMMergeReceipt` + append-only `PublicationReconcileRecord` + current-ledger recheck 共同门禁，把发布后的 terminal `BLOCKED` 安全迁移 `ACCEPTED`（`publication.reconciled` 事件是唯一命名终态例外，仅限 accept-after-merge），是发布合并后 Run 的权威恢复路径；全程 append-only、幂等、fail closed，不绕过 required checks 与 ReviewDecision，merge-never 不变；误入 terminal `BLOCKED` 的历史 Run 现可经 `marshal task reconcile` 对账恢复；
- 正式操作顺序与补偿命令见 [Operator Runbook §7](operator-runbook.md)，旧临时护栏已由 typed reconciliation 取代并废止；审计 finding `PUBLICATION-MERGED-HEAD-RECONCILE-P1` 随实现合入关闭（P1 → `CLOSED`），见[设计审计报告](audit-report.md)。ADR 0026 接受只冻结契约（PR #49），不升级 M8–M13 实现状态；本修复是 Local MVP 发布流程的实现层关闭，不改变 M0–M6 已通过状态与 Local MVP `USABLE` 结论。

公开 [Issue #30](https://github.com/chiga0/marshal-harness/issues/30)（CI deadline 观察语义）保持 `OPEN`，但已接受的 [ADR 0028](adr/0028-ci-deadline-phased-observation.md) 契约现已实现：TaskSpec 可用 `ciObserveTimeoutSeconds` 分离 CI 观察预算，publish 把 `ciDeadline` 冻结进 PublicationRecord digest；`marshal task accept` 与 controlled merge 均先观察并持久化 identity-bound RemoteCheckRecord，再以 provider `completedAt`、`publishedAt − 300s` 下界和 `ciDeadline + 300s` 上界裁决。deadline 后的及时完成证明仍可通过，pending 到期与缺失、迟到或不一致时间事实按封闭原因码 fail closed；四种 CI 时间原因导致的历史 `BLOCKED` 只有在 typed reconciliation 取得 fresh timely proof 后才可恢复。Issue 的远端关闭与补充回归证据仍独立跟踪，不影响上述当前实现语义。

M7–M13（M7–M12：耐久 Runtime 与可插拔 Sandbox Provider；M13：Goal orchestration）。[ADR 0019](adr/0019-deterministic-control-plane-typed-execution-and-goal-admission.md) 是 M7 通过后的已接受设计增补：它不回滚 M7，也不提前完成 M8–M13；确定性 Supervisor、Typed Execution、通用副作用对账/补偿与 Goal admission 均仍待实现。

### Milestone 状态取值定义

本文档全部 Milestone 状态只允许以下三种取值，其他文档引用 Milestone 状态时必须与本定义一致：

| 状态 | 含义 |
| --- | --- |
| `PLANNED` | 范围未冻结或未开始实施。ADR 接受、设计冻结与契约冻结**都不改变**该状态。 |
| `IN_PROGRESS` | 范围已冻结且已有 gate 落地 main，但该 Milestone 的退出门禁未通过。**不表示实现或 conformance 完成**，也不表示已落地的 gate 已接入执行路径。 |
| `PASSED` | 已通过该 Milestone 的退出门禁（实现、测试、独立审计、远端 CI 全绿）。对 M7 这类设计/契约阶段 Milestone，`PASSED` 只表示设计与契约阶段通过。 |

| Milestone | 状态 | 证据 |
| --- | --- | --- |
| 7：架构与契约 | `PASSED`（2026-08-11，只表示设计与契约阶段通过） | [ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md) 已接受；[ADR 0017](adr/0017-provider-neutral-sandbox-contract.md) 已接受（2026-08-10，接受只关闭设计歧义）；[ADR 0018](adr/0018-control-plane-and-provider-ports.md) 已接受（2026-08-11，接受只冻结设计）；[Runtime 架构](runtime-architecture.md) 同步；Marshal Run `m7-control-provider-boundary-adr-r15-20260811` 完成 M7 最终架构稿并 `ACCEPTED`（reviewRound=2，32/32 required Gates 通过，独立审查无 P0/P1）；[Draft PR #13](https://github.com/chiga0/marshal-harness/pull/13) 通过 Quality (ubuntu-latest)、Quality (macos-latest)、Secret scan 与 GitGuardian 检查（GitHub Actions CI run `31449333738`），2026-08-11 由维护者手工合入 main（merge commit `4b2f3248f24ec2a67642ec77822fe6bb59730df7`） |
| 8：Sandbox SPI/Fake/Local conformance + embedded/local 纵切 | `PASSED`（2026-08-13，退出门禁通过） | [验收报告](milestone-8-report.md)；六个硬门禁 gate 全部合入 main 且各 PR 远端 CI 全绿：gate-1（authority 双键空间 AuthorityNamespaceId/SecurityDomainId + SideEffect authority-record Schema，PR #42）、gate-2（ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence Schema + attestation 全链绑定，PR #45）、gate-3（legacy fail-closed mapper，PR #48）、gate-4（durable ProviderRegistration store + restart recovery，R2 lineage `m8-durable-registration-store-r2-20260812b`，PR #57）、gate-5（snapshot/evidence validation，PR #47）、gate-6（enable DispatchLease match，PR #60）；embedded 纵切：internal/sandbox SPI 类型 + Fake Provider + conformance 套件（PR #75）、Local SandboxRunner 宿主进程执行 + lease 绑定 + receipt observation（PR #80）、typed cross-domain edge 记录类型 + fixture 矩阵残留（PR #61）。各 gate 尚未整体接入最终 Runtime 执行路径；同期合入基础设施修复见[验收报告](milestone-8-report.md)。见[实施计划](implementation-plan.md) |
| 9：marshal-server、Public API 与 Durable Runtime | `PASSED`（2026-08-14，设计与契约+本 milestone 交付门禁通过） | 七交付全部合入 main 且各 PR 远端 CI 全绿：a lease 持久账本与 crash recovery（PR #104）、b typed edge 运行时接线（PR #107）、c1 当时以 `marshal-server` 命名的常驻入口 + Public API（PR #111）、c2 SSE 只读投影（PR #115）、c3 远程注册 + TLS 基线（PR #116）、e DurableExecutionEngine seam（PR #119）、d Push/Pull 双拓扑 transport + outcome/invariant equivalence conformance（PR #120）；该 executable 拓扑已由 ADR 0062 取代，现行 production server 只能是 fixed `marshal control-plane serve`。任务拆分见[M9 任务拆分设计](m9-vertical-to-server-design.md)。不表示现行 server transport 已实现，不表示 M10–M13 实现状态变化或 conformance 终态；见[实施计划](implementation-plan.md) |
| 10：Cloudflare Provider（remote transport） | `PLANNED`（1.x 候选） | 不阻塞 v1.0；已合入代码保留为 fixture，R6 后按真实远程需求重排。 |
| 11：生产级存储、多节点 HA 与身份分离 | `PLANNED`（1.x 候选） | 不阻塞 v1.0；v1.0 使用单节点 durable file ledger，HA 与多用户在 R6 后重排。 |
| 12：Provider SDK/协议、多语言 SDK 与长稳平台扩展 | `PLANNED`（1.x 候选） | v1.0 只保留跨平台安装与 release conformance；完整 SDK/生态矩阵在 R6 后重排。 |
| 13：Goal orchestration | `PLANNED`（1.x 候选） | 不阻塞 v1.0；复杂 Goal DAG、动态重规划与累计预算在稳定 Runtime 发布后评估。 |

[ADR 0026](adr/0026-scm-merge-receipt-and-publication-reconcile.md)（已接受，2026-08-12；维护者合入 PR #49）冻结已合并 PR 的权威 reconcile 契约（`SCMMergeReceipt` 与 `PublicationReconcileRecord`）；accept-after-merge typed reconciliation 实现（accept 内联 MERGED 识别 + `marshal task reconcile` 补偿命令 + lifecycle 唯一终态例外）已于 2026-08-13 合入，关闭 Issue #25 与审计 finding `PUBLICATION-MERGED-HEAD-RECONCILE-P1`；该实现是 Local MVP 发布流程修复，不升级 M8–M13 实现状态。

[ADR 0017](adr/0017-provider-neutral-sandbox-contract.md)（已接受，2026-08-10；全部 P1 经 Round 2 独立验证与 ReviewDecision accept 后由维护者接受）基于首次 Sandbox SPI dogfood 的 reject 证据冻结 provider-neutral Sandbox 安全契约，并修订 M8–M13 分工：

- 二维权限/隔离模型 `AccessMode × AssuranceLevel`（含旧 `executionProfile` 兼容映射、拒绝/降级规则与持久记录迁移）；
- `hardened` 必须绑定密封 `ConformanceEvidence`，证据拓扑冻结：probe 定义/challenge/nonce、artifact digest、调度、out-of-band 观察、裁决与签发由 Control Plane 与独立 Conformance Verifier 控制，probe workload 作为敌对测试负载运行在被测 Provider 创建、身份精确绑定的 target allocation 内，Provider 的 completed/receipt 只是输入、不能自签通过；Local 普通宿主进程永不 hardened，Cloudflare 无豁免；
- Stage 内容寻址（inline 小对象/ArtifactStore locator、大小上限、消费前后重算 sha256、禁止回显声明 digest）；
- workloadRole 与认证 principal 拆分：Sandbox workloadRole 封闭枚举仅 `worker`/`verifier`，control-plane/publisher/operator/API caller 是不同语义 Port 上受 AuthZ 约束的 principal，Publisher 永不成为 Sandbox workload；完整身份 fencing（task/run/attempt/workloadRole/allocation/generation/fencingToken 元组，远程请求另绑定 principal/portKind/providerType/audience/scope——该 universal 身份口径已被 ADR 0018 §3 按 Port 分流取代；普通 replay 先过当前 lease fencing；Restore lost-response reconciliation 独立路径）；
- 无双写 Restore（默认 replacement allocation，控制面 CAS 激活新 generation）；
- DispatchLease 唯一状态机：Push/Pull 只冻结 outcome/invariant equivalence（唯一 claim、eligibility、fencing、deadline/expiry/cancel/reconcile/generation bump 与晚到隔离不变量），允许拓扑特定（topology-specific）的 offer/poll/claim/ack transition 与 timing——ADR 0017 §7 的逐步 wire 等价措辞已被 ADR 0018 §16 取代，conformance 比较 normalized business trace 与业务不变量，不比较逐步 wire trace；M9 交付两拓扑 outcome/invariant equivalence conformance 与故障注入口径；
- DurableExecutionEngine 唯一 Port 名：Temporal/Local Engine 仅是 backend，Attempt 创建/retry 预算/rework/终态裁决只在 Core，delivery/activity retry 不创建 Attempt、不消费业务预算；
- M9 wire contract 首版冻结：versioned HTTP/JSON + OpenAPI（Task create/get/cancel、Run approval/status、events/evidence），SSE `eventId`/cursor 断线续传 + 轮询 fallback，WebSocket/gRPC 推迟；Provider remote transport 同为 versioned HTTP/JSON（Push 调 Provider endpoint，Pull outbound-only）；M9 提供最小 scope-bound 可撤销注册身份，M11 扩展生产远程入口与多用户 AuthN/AuthZ，M12 基于该 wire contract 交付多语言 SDK 与部署文档；版本化 Provider Protocol 认证注册与观测边界；C/S + Control Plane/Execution Plane 分离并保留 embedded/local 模式；CLI/Web/GitHub App/CI 均为 Public API client，embedded CLI 经 in-process adapter 调同一 Public application Port、不直写 store；
- 分工修订：**M8 的纵切是 embedded/local 纵切**；M9 当时以 `marshal-server` 命名的常驻入口与 Push/Pull Public API 资产保留为历史交付，其 executable 拓扑已由 ADR 0062 取代；M10 接 Cloudflare remote transport；M11 HA/AuthN/AuthZ；M12 多语言 SDK、部署文档与多拓扑 conformance；M13 实现 Goal API/控制器。

[ADR 0018](adr/0018-control-plane-and-provider-ports.md)（已接受，2026-08-11；本任务全部 Gate、独立 ReviewDecision accept 且无 P0/P1（含 Round 4 独立评审八项 P1、Round 5 复核四项残留与 Round 6 复核两项残留——Control Plane authority namespace 与 Provider actor 域分离、typed cross-domain edge——、Round 7 复核三项残留与 Round 8 复核一项残留——typed edge 跨域例外与适用范围——、Round 9 复核两项残留——跨域 fail closed 表述精确化、非 edge Port 与同域不自动授权——全部关闭）后由维护者接受）冻结 Marshal C/S Control Plane、按信任域分隔的 Provider Port、耐久注册/能力快照与在途 lease 撤销，澄清/部分取代 ADR 0017 §4/§6/§7/§8/§10/§12：

- C/S 终态：Marshal Control Plane 运行于 fixed `marshal control-plane serve`，Execution Plane 分离可远程；Core 是唯一业务权威；Provider 与 DurableExecutionEngine backend 只提供输入/传输，不能宣布 approved、ReviewDecision 或 safe-to-publish；
- 六类 Provider（Agent、Sandbox、Verification workload executor、SCM/Publisher transport、Artifact、Secret）至少分三个信任域（trust domain）：低权限 Execution、独立高权限 Publication（Publisher 永不成为 Sandbox workload）、Data/Capability；域之间不共享 credential、AuthZ、审计或 conformance profile；六类 Provider 彼此是不同 Port、不同 protocol family，不共享 conformance suite；
- Provider 不必远程：对每个具体 Port/protocol family，embedded/in-process、Push HTTP、Pull outbound runner 才是该族的 transport adapter，运行该族统一的 conformance suite；
- 身份按 Port 冻结、不设 universal envelope：required/forbidden 矩阵覆盖 public-api、provider-registration/control、dispatch-bound Sandbox/Agent/Verification、publication、artifact、secret；public-api 禁止 providerType 并拒绝 workload lease（workloadRole/allocationId/generation/fencingToken/DispatchLease）；provider-registration/control 同样拒绝 workload lease；只有 dispatch-bound Port 绑定完整 lease 身份；publication 绑定 SideEffectIntent/ReviewDecision/evidence digest；artifact/secret 绑定 scoped handle/content digest/scope/expiry；fencingToken 是非凭据 stale-write guard，credential 不入业务 JSON/事件/日志/digest；
- append-only event ledger 是唯一权威，snapshot/queue/SSE/registry/索引是可重建投影；SSE cursor 过期、gap 或压缩返回可判定 resync；DurableExecutionEngine 是 Core 的内部 Port；
- Runtime 持久化 ProviderRegistration 与不可变 ProviderCapabilitySnapshot；ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence 也是 authority ledger 事实，由 authorityNamespaceId 拥有、只允许 Core 写入，仅携带 actor securityDomainId、provenance 与 eligibility；registrationId 幂等身份 canonical 绑定 `(securityDomainId, principal, providerType, providerName, providerVersion, protocolVersion, scope)`（securityDomainId 为所携带的 actor 身份）与 `idempotencyKey`/`requestDigest`，仅 actor 域与全部字段相同才归并；同 key 不同 digest conflict；跨 scope/protocol conflict 或新 registrationId，不改旧记录；revoked/expired 不因普通 replay 复活；create/status/revoke/expire 与 capture/supersede 都是 authority ledger 事实，三类 expiry 独立；禁止 memory-only registration；legacy v1alpha1 CapabilitySnapshot 字节/digest 不变，只经显式版本化 fail-closed mapper 转换并记录 sourceCapabilitySnapshotDigest，不得默认补 scope/evidence；
- DispatchLease 只消费持久 ProviderCapabilitySnapshot，双绑定权威侧 authorityNamespaceId 与 actor 侧 securityDomainId，并绑定 registrationId、claim 时 active status/version、providerCapabilitySnapshotDigest 与 conformanceEvidenceDigests 封闭集合；lease 引用/digest 永不改写只供审计；每次 heartbeat、结果/副作用接纳与恢复 reconcile 按当前 ledger 重判资格（eligible）；registration revoke/expire/incompatible、snapshot expire/supersede、evidence revoke/expire 使 active lease（在途 lease）立即失去资格（cancel/expiry + generation bump/fencing，Allocation/Attempt 终止对账，晚到结果隔离）；继续执行只能新 Attempt + 新 lease 重新 match；
- M8 实施顺序硬门禁：negative fixtures/event contract → ProviderRegistration/ProviderCapabilitySnapshot Schema → legacy mapper → durable embedded registration + ledger recovery → snapshot/evidence validation → 最后 enable DispatchLease match；前置缺失 claim/match fail closed；fixture 覆盖跨 scope/protocol、same key/different digest、revoked replay、restart/rebuild、substitution、各类 claim 后失效的 Push/Pull，以及 typed cross-domain edge：伪造签发者（issuer）或冒用签发身份、错 authority scope、错 source/target（issuer/sourceActor/targetActor 与 edge 类型不符或 target substitution）、错 operation、错 attempt/allocation、已过期、已撤销、digest 替换的 edge 必须拒绝；绕过 current-ledger recheck 使用派生 token/handle、转授或扩权尝试、跨 Port token/schema 复用的请求必须失败；携带 raw handle/credential 或跨域复用 raw credential/ConformanceEvidence 的跨域请求必须失败；Provider actor 未经对应 typed edge 授权或绑定不精确匹配的跨域访问必须失败；Public API/SSE 客户端与 Core 内部权威对象引用不持有 Provider typed edge 的合法访问 fixture 必须通过；合法三条 typed cross-domain edge 可恢复且幂等，fixture 明确区分三类合法 positive 与无 edge、错 edge、错绑定 negative；provider-registration/control 经 transport identity、该 Port AuthN/AuthZ、scope/protocol validation 与 registration protocol 校验、由 Core 将获准事实写入 authority ledger 的合法请求必须通过，跨 Port 复用 transport identity/token 或以 securityDomainId 相同为由跳过 principal/registrationId/providerInstanceId/scope/attempt/allocation/operation 门禁的同域 bearer 化请求必须失败；Public API 幂等身份、SSE cursor 与 Artifact/Evidence/Checkpoint/Candidate 对象 key 使用 authorityNamespaceId，将其归属 actor securityDomainId 的 fixture 必须失败；
- M9 当时冻结以 `marshal-server` 命名的 Public API/Sandbox dispatch Push-Pull、远程注册、SSE 与 DurableExecutionEngine 合同；现行 production executable 拓扑由 ADR 0062 的 fixed `marshal control-plane serve` 取代；M12 扩展其余 Provider wire/SDK；HTTP/JSON+OpenAPI 首发，WebSocket/gRPC 推迟；
- securityDomainId 复合安全域键空间现在冻结（ADR 0018 §10）：复合三元组 `(tenantNamespace, trustDomainKind, isolationDomainId)`——tenantNamespace 单租户部署可固定 `default`（tenant 只能作为该组成参与授权），trustDomainKind 封闭枚举 execution|publication|data-capability，isolationDomainId 标识同 kind 内隔离边界；不得使用全系统单一 default 同时宣称隔离 Execution/Publication/Data-Capability；actor 侧 securityDomainId 进入 registration/snapshot/evidence 携带项、lease/allocation actor 绑定、artifact/secret scoped handle、cache key 等引用字段的持久键空间（只用于 actor 身份、provenance 与授权判定），submission/run lifecycle、DispatchLease/Allocation、ReviewDecision、Outcome、SideEffectIntent/Receipt reconcile、Evidence graph、typed edge、SSE cursor/sequence、idempotency/replay 权威键、outbox、audit 与事件账本归权威侧 authorityNamespaceId 拥有；未经三条 Core-only typed edge 中对应 active edge 授权或绑定不精确匹配的跨域引用与跨 trustDomainKind 引用 fail closed（三条 typed edge 是默认拒绝规则的唯一 allowlist 例外）；不得等 M11 再迁移持久主键；
- Control Plane 权威与 Provider actor 分离（ADR 0018 §10）：authorityNamespaceId=(tenantNamespace, controlPlaneId, authorityScopeId) 拥有全部 Control Plane 权威对象——Project/Goal、TaskSubmission、Task/Run/Attempt lifecycle、DispatchLease/Allocation、ReviewDecision、Outcome、SideEffectIntent/Receipt reconcile、Evidence graph、typed edge、事件账本、发布决定、idempotency/outbox/audit 记录与 SSE 权威序列——只允许 Core 写入；controlPlaneId 是 HA/灾备中保持稳定的逻辑权威身份，不是进程实例；securityDomainId=(tenantNamespace, trustDomainKind, isolationDomainId) 只标识 Provider actor；authorityNamespaceId 不是 Provider 的 trustDomainKind 维度，Provider 不得写入或宣称权威对象；
- Core-only typed cross-domain edge（ADR 0018 §3）：三条 Core-only typed edge——DispatchResultCapability、MaterialAccessGrant、PublicationAuthorization——是 Provider actor 跨 trust domain 访问默认拒绝（default deny）规则的唯一 allowlist 例外，其余 Provider actor 跨 trust domain 访问默认拒绝；
- Core 是唯一签发者、唯一撤销者与唯一重新授权者，每条 edge 的 issuer 为 Core（issuer 不等于业务流的 sourceActor：DispatchResultCapability 的 sourceActor=Execution workload、targetAudience=Core result-ingress；MaterialAccessGrant 的 sourceActor=Data/Capability Provider、targetActor=Execution workload；PublicationAuthorization 的 issuer/sourceAuthority=Core、targetActor=Publication Provider）；
- 每条 edge 是由 authorityNamespaceId 拥有的 authority-scope-bound 权威记录，冻结 issuer/source/target/operation/expiry/digest/revocation/replay/current-ledger recheck 七项生命周期要素与各自专属绑定，授权不可转授、不可扩权，每次使用必须按当前 authority ledger 复核；
- edge 只承载 scoped handle、digest 引用与授权引用，不承载 raw credential/raw secret handle，不替代 ConformanceEvidence；
- edge 派生的 token/handle 只是指向 edge 权威记录的单向引用，派生 token/handle 不得成为第二权威；
- Public API/SSE 是 Client 到 Control Plane 的入口，使用各自的 AuthN/AuthZ、scope 约束与 re-AuthZ，不需要 Provider typed edge；Core 内部权威对象引用（ledger 事件间引用、cursor、证据关系、outbox/ledger 引用）保留在 authority ledger，不需要 Provider typed edge；
- Provider actor 跨 trustDomainKind 访问必须持有 active typed edge，并精确匹配该 edge 绑定的 source/target securityDomainId 与全部对象、operation、时效绑定，未经对应 edge 授权或任一绑定不符一律 fail closed 并写入审计；
- provider-registration/control 与 public-api 不持有三类业务 typed edge，经 transport identity、该 Port 的 AuthN/AuthZ、scope/protocol validation 与 registration protocol 校验，由 Core 将获准事实写入 authority ledger；
- securityDomainId 相同只是 actor provenance/partition 条件，不构成授权、不构成同域 bearer grant，同域请求仍须逐项匹配具体 Port 的 principal/registrationId/providerInstanceId/scope/attempt/allocation/generation/operation 门禁；
- Provider attestation 全链绑定（ADR 0018 §11）：ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence/lease claim 全链绑定 securityDomainId、稳定 providerInstanceId、effective configDigest 与签发/验证 trust root（含 key id/rotation）；任一变化产生新 immutable 快照/证据并触发 eligibility 重判；相同软件版本换实例/配置/签发密钥不得复用 hardened evidence；Worker/Verifier 必须不同 principal 与不同 allocation；高保证策略可要求 provider/host/failure-domain diversity；
- 远程 transport 安全基线（ADR 0018 §12）：任何非 loopback/in-process transport 自首次 enable 起强制 TLS；workload-to-workload 优先 mTLS 或等价不可转移 workload identity；双向校验 server/provider 身份与 audience/scope；短期 credential rotation/revocation 与 replay protection；M9/M10/M12 首次 enable 即生效，M11 只扩展 HA/多节点/多用户授权策略，不补首次安全基线；
- 原子 fencing 写入汇（ADR 0018 §13）：ledger sink 同事务 atomic compare-and-append/transaction，ledger transition、当前 lease generation 与 Evidence/Artifact 引用同原子校验；Artifact/Evidence/Checkpoint/Candidate bytes 的接纳关系归 authority ledger，使用 authorityNamespaceId+run+attempt+allocation+generation scoped immutable key 与 digest-verified put-if-absent（actor securityDomainId 只作为 provenance 记录）；陈旧/冲突 bytes 只进 quarantine namespace，永不覆盖或进入当前 evidence graph；M8/M9 补 lost-response、concurrent-write、old-generation overwrite fixture；
- SSE 恢复与再授权（ADR 0018 §14）：cursor 身份 authorityNamespaceId+scope+ledgerSequence（权威账本的权威侧身份），订阅方另绑定自身 securityDomainId 完成授权判定，scope 内单调 sequence，at-least-once + eventId/sequence 去重，expiry/gap/compaction 返回 deterministic resync 起点与 snapshot digest，heartbeat 与有界 backpressure，周期性 re-Authorization 与敏感变更即时 re-Authorization；SSE 是只读投影，不承载 ACK、lease heartbeat 或 command；参数值留 M9 Schema 冻结；
- DurableExecutionEngine 单一权威 seam（ADR 0018 §15）：同事务 outbox 或 ledger-derived Core command journal 二选一；commandId 从权威事实稳定派生；backend 只消费/回报，workflow/activity state 不是业务权威；M9 backend profile 与升级 fixture 覆盖 workflow versioning/build ID、Continue-As-New、payload 外置/上限、activity heartbeat/cancel/retry；
- 按 Port 的 versioned protocol family（ADR 0018 §16）：每个具体 Port 独立 audience、AuthZ scope、request/response schema、error/idempotency/revocation 与 conformance profile；只共享 transport、JCS 与最小 base auth primitives；禁止跨 Port token/schema/operation；六类 Provider 分属不同 protocol family，不共享 conformance suite；embedded/Push/Pull 只是同一 Port 内的 adapter，运行该族统一的 conformance suite；
- Push/Pull 只冻结 outcome/invariant equivalence（唯一 claim、eligibility、fencing、deadline、无双活、晚到隔离），允许拓扑特定（topology-specific）的 offer/poll/claim/ack transition 与 timing，conformance 比较 normalized business trace 与业务不变量，不比较逐步 wire trace（ADR 0018 §16）；
- 不兼容与撤销分级处置（ADR 0018 §6/§7）：security-critical revoke（credential compromise、protocol violation）立即 cancel + generation bump + kill，不留 drain 窗口；planned/ordinary incompatible upgrade 使用新 registration/新 snapshot，旧实例 stop-new + bounded drain，drain deadline 到期再 fence；事件机器可读原因码与审计记录分开；普通升级不得复活旧注册或改写旧 lease digest。

ADR 0017 中以下历史 universal 口径就地标注**已被 ADR 0018 取代**，不再作为实施依据：所有远程副作用统一绑定完整 Task/Run/Attempt/allocation/lease 身份（现仅 dispatch-bound Port）；所有 Attempt/Artifact 接纳统一 fencing（现按 Port 分流）；所有远程请求统一额外绑定 providerType（现 public-api 禁止 providerType）；六类 Provider 注册产生 legacy CapabilitySnapshot（现为 ProviderRegistration + 不可变 ProviderCapabilitySnapshot，legacy 快照仅经 fail-closed mapper 转换）。

实现状态不因文档冻结或 ADR 接受而提前升级：ADR 0017 的接受只关闭设计歧义、ADR 0018 的接受只冻结设计，均只冻结设计不升级 M8–M13 实现/conformance 状态；上表各 Milestone 状态为：M7 于 2026-08-11 通过退出门禁后更新为已通过（只表示设计与契约阶段通过，不表示后续实现或 conformance 完成），M8 按修订后的契约（含 ADR 0018 §7 顺序硬门禁）以新任务启动，六个硬门禁 gate 全部合入 main 且各 PR 远端 CI 全绿后于 2026-08-13 通过退出门禁，更新为 `PASSED`（各 gate 尚未整体接入最终 Runtime 执行路径），M9 七交付（a lease 持久账本 PR #104、b typed edge 运行时接线 PR #107、c1 当时以 `marshal-server` 命名的常驻入口 + Public API PR #111、c2 SSE 只读投影 PR #115、c3 远程注册 + TLS 基线 PR #116、e DurableExecutionEngine seam PR #119、d Push/Pull 双拓扑 transport + conformance PR #120）全部合入 main 且各 PR 远端 CI 全绿后于 2026-08-14 通过退出门禁；该历史 executable 拓扑现已由 ADR 0062 的 fixed `marshal control-plane serve` 取代。M9 `PASSED` 只表示当时设计与契约+本 milestone 交付门禁通过，不表示现行 server transport、M10–M13 或 conformance 终态完成；M10–M13 保持 `PLANNED`。首次 Sandbox SPI dogfood Run 的既有实现成果按**未接纳探索证据**对待，不计为 M8 实现进度。

ADR 0033（Proposed）只冻结受控 merge 的 journal-bound authority/delivery 目标与负向恢复矩阵。提出、接受、Schema 落盘或任一前置实现切片合入都不构成受控 merge supported 声明，也不改变 M10 在途及 M11–M13 `PLANNED` 状态；A–D 全部实现、独立审计 P0/P1 清零且 required CI/secret scan 与恢复 conformance 全绿后，最多登记显式 opt-in 的 `local-nonproduction` 受限 profile。production supported 仍须等待 M11 external rollback witness、跨节点 fenced lease 与协调回滚恢复演练通过。

每个 Milestone 都执行范围冻结、实现、单元/集成/E2E 测试、独立审计、提交推送和远端 CI 绿色验收。任何 P0/P1 审计问题或 CI 失败都会阻止进入下一阶段。M7–M13 还要求每个 Milestone 先通过 Local MVP 全量回归。M7 只冻结 Project/Goal 的存在性、authority ownership 与多 Run 原则；M13 才实现 ADR 0019 的完整 Goal 控制器，M7–M12 完成声明不涵盖复杂需求目标。
