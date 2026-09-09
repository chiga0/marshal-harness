# Node 受管 Leader：最小执行机器合同

状态：随 [ADR0095](adr/0095-node-managed-leader-contract.md) 独立审查接纳，**Accepted / 未实施**。行为依据是已接受的 [ADR0094](adr/0094-trusted-single-user-role-team.md) 与 [机制设计](node-leader-execution-design.md)；文档工作树锁定 `f27784dd`，取消接缝最初参考 `acbcfee9`，其聚合修复 `070086e9` 已进入当前 main `3f359fd0`。后两者仅补充实际追溯，不重新变基本工作树，也不代表 Leader 已实现。下文函数名为明确的新增/调整接缝，不声称当前已有。只选一个实现方案，不建立第二 scheduler、权限平台或 Workflow 编辑器。

## 1. 根配置、版本与固定边界

受信组合根增加 `leader: createLeaderPort({id, providerId, policy, prepare, parseDecision})`；`policy` 为闭集可序列化数据，`prepare/parseDecision` 是固定受信实现，不序列化函数、不接收 HTTP/LLM 代码。配置数据、Provider 身份、实现版本及策略摘要在建根时冻结；open 在 claim 前核对，不同配置/缺解析器拒绝。实例私有身份的校验沿 `createVerificationPort`，任意 `{capability:true}` 不构成资格。

- 启用 profile：`task-managed-leader/v1`；根格式：`marshal-node-task-sqlite/v7-managed-leader`；service layout：7。只有新空根可选，不自动修改 v1–v6 或复活旧 Task。旧 reader 在 claim 前拒绝 v7；新 reader 按原根合同读旧格式，不给它们注入 Leader。
- v7 具备 v6 目标 Worker 取消、原 custody、问题与同计划 repair 能力；never-permitted **仍由每张 ticket 的原 `startProtocol`、真实 FileBusiness/prepare/Depot 私有资格决定**，不能由 v7 或 Leader 自报。普通 Git/有外部准备效果的执行仍保留原 unknown 边界。
- 本版输入审计默认 metadata-only；若同时选择 staging-only，启动前继续拒绝任意 `auditDisclosure` 回调。Leader/Review 的准备资格必须单独证明：固定程序只读同库/Depot、纯内存组装有界 prompt；不能把任意业务回调放到许可前后就宣称无副作用。
- `policy` 必需字段为 `{profile, maxCalls, maxActions, maxRequests, repair, review, publication}`。`profile` 为上述常量；`maxCalls` 为 1–32、`maxActions` 为 1–4、`maxRequests` 为 1–16。`repair={nodeIds,maxRounds}`：最多 64 个唯一节点，轮数 0–3。`review={providerId,policyDigest}`。`publication` 为 `null` 或 `{targetId,policyDigest}`。运行时权威额度仍是 Task 原 limits；这些只是更小上限，不能增加费用、并发或 deadline。
- `policyDigest=hash(encode(policy))` 使用原 Store 的规范化 JSON 和 `sha256:` 摘要。原 Plan 结构不增字段；Core 在 `acceptance` 中追加唯一确定性 JSON 条款 `{profile,policyDigest,repair,review,publication,completion:'leader-delivery'}` 并纳入 planDigest，不能依赖模型复述。受信布局、验收标准、允许 Provider、原预算与目标共同构成批准边界。

代表性计划保留当前 DAG：两个互补作者和一个独立 verifier sink；独立语义 Review 与反复 Leader 是附属受管执行，不伪装成需要交付文件的 DAG 作者。旧 `verification.bind` 的单一 sink 不废除；新 Core 在首次 verifier 前增加“当前全选果 Review 已接纳”的阶段门槛。批准后的普通依赖由 Core 调度，不逐节点唤起 Leader。

## 2. 输入、输出和六类建议

以下对象均闭集，必需字段不能省略；不适用值显式 `null`。沿原 ID、Digest、有限 JSON、无重复键/NUL/非法 Unicode 校验。输入总量最多 192 KiB，最终 prepared prompt 仍最多 256 KiB；输出最多 64 KiB，深度/节点总量沿原 `encode` 上界。单条解释最多 4096 UTF-8 bytes。超限或必需上下文缺失拒绝，不截去验收要求后继续。

### 2.1 原输入与受信封装

`LeaderInput={profile,taskId,callId,obligationId,generation,inputDigest,snapshot,cursor}`。`inputDigest` 是除自身外其余字段规范化 bytes 的摘要；Core 先完成快照再预留 ticket，不由模型填。`snapshot` 必需字段：

| 字段 | 精确来源/上界 |
| --- | --- |
| `task` | 原 `intent/context/requirements/inputArtifacts`、原 deadline、剩余 Attempts、硬并发；只读原 Task，未知 usage 为 null |
| `plan` | 当前批准 Plan 或批准前 proposal/null；原 policyDigest 与确定性条款必须同时可读 |
| `selection` | 当前每个被消费节点的 `{nodeId,workerId,resultDigest,manifestDigest}`，最多 64 项；材料按原 Depot 引用读取，不读旧 Worker 目录 |
| `interactions` | 相关用户回复和原 Worker 的有效答案/ACK 精确引用，最多 64 项；继承关系沿原 DAG，不把所有 Task 问题灌给每个节点 |
| `evidence` | 当前 Review/Verification、执行失败、发布/后验的证据引用，最多 64 项；包括原 status/reason/digest，不把未接纳报告当 Decision |
| `history` | 最多 32 条已接纳 Leader 决定及原动作结果摘要/制品引用，不能用模型摘要替换必需当前材料 |
| `obligation` | 当前聚合业务原因、来源 event IDs、受影响节点，最多 64 个来源；不是 token/progress 的逐条副本 |
| `readSet` | 上述实际使用的 Plan/选果/答案/Review/授权/先前决定版本与摘要，Core 生成；最多 128 项，不由模型选择依赖以逃避重查 |

大内容先确认总预算并按精确引用展开给原 Provider；若必要内容无法在上界内提供，本版明确不支持该输入，不让 Leader凭摘要猜验收。摘要属于 observed 输入，不证明模型已阅读；保留原 prepared/handed-off/unavailable 审计区分，不记录隐藏推理。知识、提示、原生工具输出都不是授权。

`LeaderDecision={profile,callId,inputDigest,summary,actions}`：profile 常量，summary 最多 4096 bytes，actions 1–policy.maxActions 项。`actionId` **由 Core** 以 `(callId,decisionDigest,index)` 派生，不接受模型提供 key。模型只回显原 call/input 绑定；完整原输出经父进程解析、原 cleanup 与 currentness 检查后才可接纳。

### 2.2 六类闭集 action

| `type` 与字段 | Core 的有限语义 |
| --- | --- |
| `ask {kind,prompt,options,subject,nodeIds}` | `kind=business|publication`；prompt ≤4096 bytes；business options 为 0–16 个唯一 `{value,label}`，空表示输入，subject 为原缺项/失败/候选摘要；nodeIds 为 0–64 个唯一原计划节点，批准前空表示原目标缺项、批准后必须指定受影响节点。publication 的 subject 必须为 Core 可构造的精确发布授权摘要，nodeIds 为空、options 固定 allow/deny，不允许自带权限、路径或 URL |
| `plan {proposal}` | proposal 沿现 `proposePlan` 的有界节点/边/预算/交付结构；只在尚未批准时创建/替换 preview，Core 加入可信条款，必须原 `task.approve` 精确确认。批准后不悄改 DAG/目标；本版有限调整走 repair，超范围说明限制并等待用户明确新请求，不擅自重建 Task |
| `work {kind,nodeIds,selectionDigest}` | `kind=execute|review|verify`；nodeIds 1–64、去重、同计划。execute 仅请求推进已批准未执行节点；review 只能是当前完整可审选果；verify 必须是原 verifier sink 且当前独立 Review 满足。原相同义务存在就引用它，不创建替身 |
| `repair {nodeIds,basis,feedback}` | 原计划可修节点子集、feedback ≤8192 bytes；`basis={kind,digest}`，kind 为 `review|content-rejection|execution-failure`，来源规则见 §3；不能传新目标、预算、checker、权限或任意命令 |
| `deliver {artifactId,acceptanceDigest,reviewDigest}` | 仅精确已验收 delivery Artifact。默认只准备下载；有 publication 配置但缺精确授权时，Core 同库保存原待授权动作与完整确认请求，尚不授予启动许可；allow 回复后按原绑定继续，不再次让 Leader 批准同一动作。后验是可信策略的必需义务，不能由 Leader 选择跳过或降低 |
| `conclude {outcome,summary,basisDigests}` | outcome 为 `wait|succeeded|failed`，summary ≤4096 bytes，basisDigests ≤64；wait 只等具体未结原义务，不建定时付费循环。succeeded 必须满足整体出口；failed 保留原事实并按 Core 收口，不能覆盖已发生效果 |

同一决定最多一个 ask，ask/plan/conclude 不能与可能执行的其他 action 混合；同一选果最多一个 review/verify/repair/deliver，互斥动作或前后依赖猜测整份拒绝。work-execute 可合并节点，后继仍按原 DAG 自动调度。Core 不解释自然语言中的额外指令。批准内自治是同计划选果/修正和已允许执行；任意换 Provider、拓扑、增加预算、不可逆业务写不在本版执行集，不能把确认文本当解释器开启。

## 3. 独立 Review、修正与答案来源

新增受信 `createReviewPort({id,providerId,policy,prepare,parseReport})`，采用原 managed start 返回 `{started,completion,stop}`，内部 `executionType='review'`、公开 role=`reviewer`。本版 Reviewer 只读冻结选果，不写业务候选；相同 Provider 可复用，但不能是任何被评成果的原作者执行或参与修改该成果的执行。参与作者/Leader 的普通文本不构成 Review receipt。

原报告闭集为 `{profile:'task-independent-review/v1',inputDigest,selectionDigest,verdict,summary,findings}`；verdict 为 accept/rework/reject；summary ≤4096 bytes；findings 最多 16 条 `{id,nodeIds,requirement,observation,requestedChange}`，每段 ≤2048 bytes，nodeIds 必须属于本次选果，id 唯一。accept 必须 findings 为空。输入包含原目标/验收条款、完整被评材料及原上下游绑定，不只作者 pass 文本。父进程私有端口绑定原 ticket、inputDigest、实际报告 bytes、cleanup、Reviewer 资格，Core 重读当前选果后写 `ReviewDecision={digest,verdict,selectionDigest,policyDigest,workerId,evidenceIds}`。它证明有来源的独立意见，不保证模型理解正确，也不替代客观 Verification。

自治 repair 在同一原 `selected`/`affectedNodes` 路径增加内部来源，而不是内部 HTTP 冒用 local-operator：

- `review`：必须是上述当前、独立、verdict=rework 的原 ReviewDecision；修正 roots 覆盖其对应意见且在批准 repair.nodeIds 内。保留原意见报告给实际 FileBusiness prompt，不伪造旧 contentRejection。
- `content-rejection`：原可信 verifier 的精确负报告及必需断言，沿 ADR0091；仍重读 Depot bytes/原 Decision/选果。
- `execution-failure`：仅 Core 已确认原 cleanup、当前节点的普通非结构性执行失败；原未知、权限失败、用户 worker.cancel/Task cancel 不能被当作可重试失败。此来源最多一次替代 Attempt，不隐式换 Provider；重试费用计原预算。

三者均以原 planDigest、当前输入/ACK、精确负事实和剩余预算重查，修正 closure 的陈旧 Review/验收失效，无关选果仅在依赖未变时保留；下一 Reviewer 与 verifier 消费最终组合。旧 HTTP repair 仍只接受原显式用户合同。缺 business 消费能力启动前拒绝，不能先耗 Attempt 再丢 feedback；本版选原 FileBusiness，Git/custom 未实现该消费前不得启用本 Leader profile。

Leader 的 ask 是已结束语义调用留下的 Task 义务；答案由下一次原义务调用读取，不伪造原 Worker 的 delivery ACK。原 Pi `marshal_ask_user` 和 ACP 权限路径照旧：原 Worker 业务问题继续精确 worker/question/dispatch/ACK，权限不能通过本回复放行。普通用户答案只能填原缺项；答案改变批准范围/权限时 Core 封闭相关后继并要求新的精确批准，不能追认旧验收。原期限不延长。

v7 ticket.input 增加内部 `leaderReplyRefs`，每项 `{requestId,requestDigest,replyDigest}`：仅本节点相关已接纳 business 回复及原直接依赖继承的稳定去重并集；批准前目标缺项对该计划全体节点适用。Core 在准备与 finish/Review/Verification 同时重读原请求、答复、适用节点和上游 input/result 绑定；实际 prompt/checker 输入必须提供对应原答案 bytes，不能只带摘要。新回复使受影响旧输入不再适用时先封闭后继并形成待决修正，不给旧候选补签“已读新答案”。该数据来源与原运行 Worker 的 `interactionRefs`/ACK 分开，两者都进入原 inputDigest/验收 readSet；跨 Task、错摘要、非依赖引用拒绝，不因为当前 owner 变更而抹去已合法提交的业务数据。

## 4. 单库事实与短事务

不新增数据库/用户实体。使用原 Task event stream、`interaction`、`attempt` 投影和 outbox；v7 识别以下内部闭集记录，原 DDL 的 record/transaction 限额不涨：

| 记录 | 最少耐久内容及状态 |
| --- | --- |
| Task 内 `leader` 投影 | profile/policyDigest、stage、当前 obligationId、semantic cursor、activeCallId、已消费 calls/repairRounds、最新 decision/review/交付摘要；stage 为 intake/work/review/verification/delivery/finalizing/terminal |
| `interaction`：业务义务/请求 | obligationId、Task、原原因/source IDs、readSet 摘要、pending/claimed/consumed/closed；request 另存 kind/prompt/options/subject/requestDigest/deadline/replyRef，最多一个未答 Leader 请求 |
| `attempt`：Leader 调用 | 原 ticket/workerId、obligationId、generation、输入 Artifact/摘要、真实执行与 cleanup；保留原 Attempt reservation/预算/归属，不另有免费调用计数 |
| `attempt`：决定/动作 | 原 call/input、decisionDigest、原输出 evidenceId；每项 action 的稳定 ID/类型/payloadDigest/sourceDecision、pending/running/succeeded/failed/unknown/cancelled、原 commandId、结果引用 |

事件闭集为 `leader.obligation.created`、`leader.call.reserved`、`leader.decision.accepted`、`leader.decision.rejected`、`leader.request.replied`、`leader.action.settled`、`leader.stage.changed`；沿原 source=`application`。Review、发布、后验接纳均由 action.settled 指向原证据 Artifact，不把正文塞进高频日志；原 Worker/Attempt/cleanup 事件继续原通道。义务只从已接纳业务事实构造，taskId 范围查询有界，不扫描其他 Task 历史。

1. **冻结输入/调用**：短只读事务取得精确语义引用，锁外读/校验 Depot 并构造有界输入；短写事务重查这些引用、owner、取消/期限/预算/headroom，唯一 claim 义务、预留原 Attempt/capacity 和输入引用/outbox。任何副作用前原 custody 公钥/许可提交。输入 bytes 先耐久再 SQL，失败孤立 blob 不是权威。
2. **接纳决定**：原 managed result/cleanup 与私有解析端口通过；短写事务核对 active call、原 inputDigest、相关 readSet 和硬 fence。一次提交原决定、义务 consumed、全部 action 和其 outbox/source；部分动作校验失败整份不提交。拒绝输出留下原失败和有限重试依据，不执行其中“看起来安全”的一半。
3. **消费动作**：原 owner 在短事务重查 action、授权与硬规则，绑定原执行 reservation/command。锁外 prepare/start/collect；完成后按原 ticket 和当前事实一次接纳结果、结清原动作/容量并产生下一业务义务。同 action 精确重复只读原结果，不重新创建目标。同步无 I/O 的 plan/request/conclude 直接同事务结清，不制造空 Worker。
4. **回复**：认证→原 key 精确 replay→当前 Task revision/requestDigest/未答/期限/取消检查→原 reply+request consumed+相应后继义务及回执同事务。business 回复产生下一次需要理解答案的业务义务；publication allow 若对应已提交的精确待授权动作，则同事务解封该原动作的执行义务，不凭授权确认再造一次 Leader 批准调用。暂停可以保存合法回复，但不派新调用/动作；deny 保留拒绝并形成需业务取舍的原义务。不能把接纳回复记为模型已消费或发布已成功。
5. **整体结束**：新 profile verifier 通过写原独立 acceptance/Artifact 后，Task 继续 running、phase=delivery；保留当前 Review 和 selected 版本。交付/必需后验通过、全部影响成功义务已结、无未知执行/效果，且当前 Leader conclude=succeeded 被接纳，才同事务写 completed。Leader 无法把缺证据的失败“汇总成成功”。

语义 readSet 重查计划、用到的 selected/原结果、相关答案/ACK、Review/验收、授权和先前决定；另总是检查 owner/control/deadline。cursor 只界定读到的位置，不用整 Task revision 拒绝内部决定；另一分支 heartbeat/usage 不废弃它。剩余预算是观察值，接纳时检查实际动作仍可承担，不因合法并行执行消耗一次 Attempt 就要求整个输入快照数值完全相同。新相关业务事件合并进下一义务，不覆盖当前调用输入。公开写请求仍原 revision CAS，不因此自动刷新用户 revision。

## 5. 容量、失败与同版本恢复

Task/global 各保留一个 Leader 决策 headroom：非 Leader 新准入最多分别占各自 maxWorkers−1，Leader 使用同一原 capacity ledger 中余下真实席位。所有 Leader/Review/verifier/发布/后验仍计原 maxAttempts/deadline；总量不加一。Task leader policy 要求双作者并行时，Task 与全局 maxWorkers 均至少 3，且初次付费前证明完整最短路径的最低 Attempt 数可装入预算；默认 maxWorkers=2 不静默改为 3。等待的作者/长 Reviewer 占实际席位，不能假释放；多个 Task 的 Leader 按原待决顺序公平选唯一全局预留席位，不让普通新任务抢占该位。

本稿一次缺项/一次真实局部修正及相关故障验收的代表配置为 `maxAttempts=17`、`policy.maxCalls=9`：它们是本场景获批预算上限/可用余量，**不是必须耗满的次数、每 Task 固定九次调用或逐事件付费要求**。两个作者、两轮完整 Review、独立 verifier、精确发布与后验及总结按实际业务义务运行；approve 后已经确定的 DAG 后继由 Core 推进，批次通知合并，授权回复后已提交的精确动作直接按原门禁继续，均不为凑九次调用再问 Leader。实际少于该上限就是实际消费，不补空调用、不制造 rework；首轮正确记 firstpass。

该示例不替所有 Task 提额或承诺足以覆盖任意数量故障重试；调用者必须主动提供本次原预算，实际恢复/重试仍受剩余 Attempts/maxCalls/deadline 限制。更小的无修正/不发布任务按实际最短路径检查；未知 usage 不当零，也不对外声称预留 Attempts 等于实际 token 成本。

新格式局部可恢复失败把受影响后继封闭、原工作标失败并产生待决义务；仍合法的无关分支继续。用户 worker.cancel 保留其取消及后继禁止语义，Leader不得自动派替身；Task cancel/硬期限/权限故障立即按 Core 停止新执行并由 Execution 停原 handles，不等 Leader。疑似 stuck 仅观测，不 kill。Store/owner 丢失依旧执行预批准的原 handle 停止，不能补写虚假 cleanup。

恢复分两类，不把旧命令换 generation 即直接启动：

- 未提交决定的旧 Leader/Review 执行先沿原 custody/never-permitted 真证明结清；旧输出不接纳。只在仍有效 Task/原义务且真实 cleanup、原剩余预算允许时，写一个当前代 successor Attempt，输入重新快照，最多一次因中断的调用重试；原消费费用保留。不能在 unknown cleanup 下新派。
- 已提交决定先读取原 actions/outbox；同步已结动作只 replay。原代“未开始”的纯准备动作只有原协议证明 never-permitted 才能在当前代以相同 actionId 建有来源的 successor 执行；允许的已清理纯读 Review/后验可按同一动作有限重做并计预算。命令本身不从 unknown 重置 pending，保留原命令和 successor 关系。业务作者失败是否重做仍交 Leader 的新 repair 决定，不全队重派。
- 任何旧发布动作先结清原执行归属并只做精确 lookup；无法排除旧执行仍能写时禁止再次 create。匹配可结清效果，冲突失败，不能查询或未知原执行则保持 unknown。只有原协议明确未获启动许可且原目标确实 absent，才可继续原动作一次；业务期限过后只能只读结算，不能产生新发布/成功后验执行。
- 取消或期限后恢复只结算原 cleanup/效果/控制 Operation，不启动 Leader，不接纳旧候选或恢复旧 completed。存在 unknown 时封闭相关发布/后继且不虚释容量；是否 ready 按原未结执行义务，不因 Leader 等待本身封闭整个服务。

该恢复含义仅属于 v7。0089/0092 旧格式“旧任务失败/取消，不重派”的记录不重解释；v7 当前代 successor 有原决定/义务、原成本和清理证明，不是裸 PID 或新 owner 自签观测。

## 6. 首个 publication/postverify 端口

### 6.1 固定目标与授权

首个目标是本机受信只读 HTTP 消费的 JSON 报告目录，不是网站产品。受信 `createLocalReportPublication({id,root,readBaseURL,policy})` 持有已验证目录身份；root/URL 不能来自 Task/模型。只允许 loopback 固定 origin、无 redirect、无凭据转发；目录不能是 Store/Depot/Worker 根或相互祖先，禁止符号链接/特殊文件与不安全路径。威胁模型仍是可信单用户，不把 Node 路径检查宣传为恶意同 UID 沙箱。

Task 授权正文由 Core 从原真实事实构造：`{taskId,planDigest,artifactId,artifactDigest,bytes,acceptanceDigest,reviewDigest,targetId,targetPolicyDigest,name,operation:'create-if-absent',expiresAt}`。name 固定为由 Task ID 与 artifactDigest 派生的单层 `.json` basename，不让模型选择路径；report ≤1 MiB、mediaType=`application/json`、严格有限 JSON、正常 UTF-8 无 BOM。expiresAt 不晚于原 Task deadline。业务发布 **另需新请求中针对该完整正文的 allow 回复**；approve 原计划/普通答案都不是本次精确发布授权。默认无 publication 配置时不产生该请求。

Core 发布许可前再查原授权当前、原候选/Review/acceptance、取消/暂停/期限和 budget。首版不覆盖/删除/外部通知/追加费用/生产 SQL，也不自动补偿；不接收远程 shell、HTTP method 或任意 checker。未来高风险平台另有明确合同，不成为本版前置。

### 6.2 原所属执行与精确效果

publication adapter 提供 `start({ticket,prepared,executionContext}) -> {started,completion,stop}`，沿原 `launchCommand`/guard/custody；内部 executionType=`publication`，公开 role=`integrator`，不是赋予普通 integrator 发布权限。prepared 沿原 BusinessPort 的 prompt/目录材料结构：仅精确原 delivery bytes、授权正文及固定程序所需材料；命令/args 由受信组合固定，不能来自 Leader。

许可前同 SQL 冻结 `actionId/targetId/name/artifactDigest/bytes/authorizationDigest` 这个可查询外部效果义务；它不会因进程 cleanup 自动消失。不得把已有任意 `extraScopes` 清零或改成该已知义务；其他未知工具副作用仍 veto。原 publisher handle 和读取后验 handle 各有原 Attempt/绝对期限。

固定程序先以原 fd 核验输入 bytes/digest，再把完整 bytes 写目标私有临时普通文件（0600）、fsync；用不覆盖的原子 create-if-absent 发布名称并 fsync 父目录。最终目标始终 0600 同用户可读；同名已有先完整读取比较，绝不 rename 覆盖。新名只能看到完整文件，不把临时文件列为已发布。错误/崩溃临时物保留，不自动清理未知文件。

`lookup({actionId,targetId,name,artifactDigest,bytes,authorizationDigest},{signal,deadline})` 为有界只读；返回 `{status:'absent'|'matched'|'conflict'|'unknown',binding,evidence}`，binding 原样包含六个输入字段，evidence 用原 `{name,mediaType,content:Uint8Array}`。读取有界 bytes 并校验实际摘要/长度/普通文件，不依赖磁盘 PID/目录为空/自报 pass。匹配只证明原所需内容在原授权目标可观察，不能倒填“本执行创建”。重复相同 action 只查原事实，Task/目标/摘要任一不同不能共用结果。

`completion={type:'publication',status:'created'|'matched'|'failed'|'unknown',cleanup,evidence}`；evidence 是闭集实际观察及上述完整绑定的原始 bytes。父进程受信封装校验输出/正常终态/原 cleanup/绑定，Core 写原 action receipt。exit0 单独不通过；外部 created 但 SQL 丢提交，以 lookup 对账记录 matched，不伪造旧执行成功，也不重发。cleanup=true 不证明外部 absent，反向也不成立。

### 6.3 后验与整体交付

postverify 是独立固定 command，不由作者/Leader 传程序，仍用原 `createVerificationPort` 私有结果接纳模式。输入 `{publicationReceiptDigest,targetId,name,artifactDigest,bytes,policyDigest}` 和原批准的业务期望/必要答案引用；实际向固定只读 HTTP 目标 GET，禁止 redirect，正文 ≤1 MiB、同绝对期限，重算摘要/长度并运行每项必需业务断言。输出沿原 `{type:'verification',status,cleanup,evidence,delivery}`，失败可省 delivery；通过时复用原已验收报告 bytes，不能产一个不同的“发布版”。

Core 单独保存后验结果与原 publication receipt，结束仍需当前 Leader 总结；下载仍是原 delivery Artifact，不悄悄改成远端 URL。后验失败/未知保留已发布事实，不能把它记为未发布或 overall success。默认 publication=null 的流程不假造后验，通过原交付即可进入总结义务。Marshal tag/release/签名/分发与此端口无关，B3 gate 不变。

## 7. 公开可见性与回复合同

实际 [OpenAPI](../packages/task-api/openapi.json) 的 Task/Plan/Worker/Audit/Operation 是闭集，[TaskClient](../packages/task-client/index.mjs) 对响应逐项校验。因此本版不在旧响应塞新字段/role/Operation kind，也不要求新 header。新公开能力只用两个 Task 子资源操作，常规认证、Host/Origin、body cap、私有 token、错误/status 与原 HTTP 边界复用；不开放 typed actions 写接口。

### 7.1 `task.leader`：GET `/v1/tasks/{taskId}/leader`

200 闭集 `{taskId,taskRevision,profile,stage,policyDigest,activeWorkerId,pendingRequest,lastDecision,review,publication,postverify,summaryArtifactId}`。profile 为常量；stage 沿 §4；其余不存在时显式 null。lastDecision 为 `{digest,callId,evidenceId}`，review 为 §3 ReviewDecision；publication 为 `{actionId,status,authorizationDigest,receiptArtifactId}`，postverify 为 `{actionId,status,evidenceArtifactId}`，status 沿 action 状态。activeWorkerId 指原 Worker，可查询原进程/费用，不能表示常驻 Leader。pendingRequest 为 `{id,kind,requestDigest,subject,nodeIds,prompt,options,authorization,deadlineAt,status,replyDigest}`，status 为 pending/replied/closed；最多一个，replyDigest 未答为 null。authorization 在 business 时为 null，在 publication 时必须完整展示 §6.1 的 Core 授权正文，不能只显示模型描述或一个不可解释摘要。

`requestDigest` 精确覆盖 `{taskId,id,kind,subject,nodeIds,prompt,options,authorization,deadlineAt}` 的规范化 bytes，不包含后来变化的 status/replyDigest。请求一经创建不改正文，需新题则关闭旧请求并创建新 ID，计原 maxRequests；答复不能跨题、跨 Task 或借旧授权准许新成果。

完整历史沿原 task.events 的稳定 cursor/event IDs 和证据 Artifact 下载，不新增 SSE/分页权威或把无限历史塞进快照。本响应上界 64 KiB；GET 无副作用。旧根/未启用 Task 返回原 `unsupported_operation`/501，不构造假的空 Leader。新客户端明确知道这个入口；旧客户端不被强制升级才能读取其原 Task/Worker，但不能代替新确认 UI/显式客户端操作。

公开 Worker 角色映射：Leader=`planner`、独立 Review=`reviewer`、原 verifier/后验=`verifier`、发布=`integrator`；内部 ticket.executionType 区分 `leader/review/verification/publication/postverify`，新子投影/原事件指明真实种类。附属执行使用 Core 从 callId/actionId 派生的唯一 nodeId，不覆盖原 DAG 节点；验收收集仅包含其原绑定的 DAG 作者结果，不把附属 Reviewer/Leader 当需交付文件的作者。映射只维持旧枚举类别，不授任何普通 role 新权限。原 Worker 数/Attempt 不隐藏 Leader 成本；usage 仍未知则 null。旧 Audit.firstReview/measurement 保留旧 unavailable 含义，真正 Review 在新子投影，不改旧严格枚举。

Task 状态仍旧枚举：待业务答复用 awaiting-answer，待精确发布授权用 awaiting-confirmation，运行/交付未结束用 running，phase 分别 intake/execution/delivery；pause/cancel 仍原映射。旧 allowedActions 只列旧接口真实可执行项，不把新请求伪装成旧 task.answer；pendingRequest 给新客户端可回复的精确入口。旧 questions 只包含其原种类，不返回 Leader 请求。

### 7.2 `task.leader.reply`：POST `/v1/tasks/{taskId}/leader/requests/{requestId}/reply`

必需原 `Idempotency-Key`，body 为两个互斥闭集之一：

- 业务：`{expectedRevision,requestDigest,answer}`；answer 非空 UTF-8 ≤4096 bytes，符合原 options 或缺项验证器。
- 发布确认：`{expectedRevision,requestDigest,decision:'allow'|'deny'}`；只匹配 kind=publication 的完整精确授权正文，不能夹带 answer/更换目标/延长期限。

成功 202 回执为 `{taskId,requestId,receiptId,requestDigest,replyDigest,acceptedRevision,replayed}`；它仅证明接纳，不声称已消费、已授权执行成功或调用了模型。receiptId 是原 putReceipt 所属新操作的稳定 ID，不创建旧公开 Operation 的未知 kind。精确 key scope 为 `(task.leader.reply,taskId,requestId,key)`，requestDigest（幂等请求摘要）覆盖路由和规范化 body，与字段中的待答请求摘要分开命名为内部 `requestBodyDigest`。同 key/body 返回原 acceptedRevision/摘要，只改变 replayed；同 key 异 body 为 409。原 request 后续是否 cancelled/consumed 查询 GET/原 events，不自动刷新 CAS 或重复 approve。

接纳顺序：原认证后精确 replay 优先；新命令才查 Task revision、requestDigest、未答/未闭、原期限/取消、来源/当前选果。失败使用已有 invalid_request/400、revision_conflict或state_conflict/409、not_found/404、application_unavailable/503；不新增错误枚举。回复提交后丢 202 不再派第二个动作，查询/精确 replay 可恢复；新 key 重答拒绝。普通回答绝不授予工具或发布权限，原 Worker question/ACK 继续旧两个 response 分支、原 202 字节不变。

这些端点的字段必须在实现提交中同步 OpenAPI、DI handler、真实 HTTP、TaskClient 绑定/错路由负例；本稿没有实际改 schema。旧 response 原样回归足以，不再建全局 version/profile 握手。v7 显式 root 配置是内部持久解释，`task-managed-leader/v1` 是单一行为标识，不是每次请求第三套版本协商。

## 8. 现有文件与新增接缝

| 文件/方法 | 一个纵切内的实际修改 |
| --- | --- |
| [application.mjs](../packages/task-application/application.mjs) `create/freezePlan/mutate/dispatch/query` | v7 初始 Leader 义务、可信 approval 条款、原批准及新子资源/回复；旧路径按格式原样 |
| [execution.mjs](../packages/task-application/execution.mjs) `nextWork/finish/reconcile/expandDispatch` | 原 ticket/预算上增加内部类型、决策 headroom、阶段验收、有限失败待决和当前代安全 successor；真实结果不经观察采样 |
| 新 `task-application/leader.mjs` | `snapshot/acceptDecision/consumeAction/reply/view/recover`；同原 transaction 与 Store，不导入 Provider 品牌、持有进程或建立新 scheduler |
| [verification.mjs](../packages/task-application/verification.mjs)、[repair.mjs](../packages/task-application/repair.mjs) | 独立 Review 受信封装、当前选果/原 ACK 重查、同计划内部 repair provenance、候选验收与后验区分；不放宽旧 WeakMap/负报告门禁 |
| [controller.mjs](../packages/task-supervisor/controller.mjs) | 现混合 loop 保留为兼容组合入口：许可/已批准调度/硬规则委托 Core，原 handle 持有及 start/stop/collect 委托 Execution coordinator；Supervisor observer 只聚合观测/通知，不持有可变命令端口或执行 handle，不业务重试改计划/判成功。不要求四个服务或全仓重命名 |
| [业务适配](../packages/task-business/index.mjs)、[命令适配](../packages/task-verification-command/index.mjs)、新增 `packages/task-publication-report/` | 精确输入、集中 Review/原负反馈进入实际 prompt，固定发布/后验命令与私有 receipt；命令端口显式接纳 postverify 内部类型，不把现仅 verification 的校验当已支持；不得弃反馈或使用可变目录取上游 |
| [store.mjs](../packages/task-store/store.mjs)、[composition.mjs](../packages/task-service/composition.mjs) | v7/layout7 claim 前校验、可信 DI/原准备资格、恢复准入；复用原 outbox kind/source/预算，不涨全局事务上限 |
| [HTTP](../packages/task-api/http-handler.mjs)、[OpenAPI](../packages/task-api/openapi.json)、[客户端](../packages/task-client/index.mjs)、发行清单 | 两个真实新端点/绑定，旧 bytes 回归；所有新增生产模块进入原 same-bytes 清单，不把 live fixtures 包成生产依赖 |

## 9. 不能由本合同推导的能力

角色独立不是 OS/凭据隔离；同 UID 可达风险仍按0094公开。没有任意动态角色/目标、Workflow 平台、后台模型无限会话、跨系统 exactly-once、旧根迁移或任意副作用自动恢复。原 Pi/ACP 权限回调、原生工具 scopeUnknown、未知 cleanup/usage、Secrets 不落日志、只读验收与受保护软件发行不削减。Model 文本不能注入发布实现或覆盖 Core 硬规则。

## 10. 完整实施顺序与六类验收

先冻结并实现 v7/闭集/原事务 + typed Leader 接纳，再在同纵切接原 Provider/Review/repair/发布后验和 HTTP 观察/回复；不把 DTO 或一次 Planner 当 B2-L 完成。可并行的无冲突 scope 为：单一作者拥有 Application/Store/Supervisor/composition；第二作者只做受信本地报告端口/固定 checker；第三作者只写新独立 HTTP 故障 fixtures。API/schema/client 需消费同一冻结 shape 后由一作者一次接齐，不让多个 writer 修改共享事务文件。

1. **真实代表链**：用户缺项→Leader ask/显式回复→计划一次批准→两个互补作者→独立 Review→真实意见局部修正保留另一成果→独立 verifier→精确发布 allow→真实本机目标 GET 后验→Leader conclude；HTTP/原 Artifact/原 receipts 可独立追溯。首轮正确记 firstpass，不污染成果或无限重跑凑 repair。
2. **当前性与活性**：两个作者同时等待/长 Reviewer 占槽，Leader 在原 3 槽/预算内有界运行；2 槽付费前拒绝；洪泛/heartbeat 不多唤起；无关进度不废弃决定，相关候选/授权/答案变化拒绝；菱形继承引用去重，旧 ACK 不跨节点/代复用。
3. **权威与输入负例**：作者自 Review、错 ticket/选果/报告/目标、丢必需断言、自由 URL/路径/argv、丢反馈、任意 callback 自报资格、普通答案授发布均拒绝。Review 不能覆盖客观失败；上下文超界拒绝，不截标准。
4. **硬控制与普通失败**：长 Leader/Review/后验时 GET 与 cancel 及时；取消目标 Worker 不停合法兄弟/不重派目标；普通已清理失败留 Leader 窗口；owner/Store 错误 stop 原 handles、不伪造记录；过期/权限/unknown 不成为免费重试。
5. **COMMIT/外部窗口恢复**：Leader reservation/许可/输出未提交、决定提交后动作未派、发布创建后回执未提交、postverify 后总结前逐点 SIGKILL；原 custody/never-permitted 合法结清或明确 unknown。预算/原决定/无关成果不丢、无重复整队/覆盖发布；同名异 bytes/错误 origin/lookup 不可用不成功，丢 reply 202 精确恢复。
6. **兼容与下一任务**：旧 v1–v6 根/终态/严格 TaskClient/旧两个 AnswerReceipt/repair/WorkerOperation bytes 不变；旧 reader claim 前拒 v7；新合法收口容量归零、冷开可查原效果且下一 Task 可交付；正常停服备份沿原完整快照，不宣称防分叉。

固定 Node 确定性 Core/真实 SQLite→原受管 CLI/HTTP→原 Provider 显式单次实机，按风险递进；安装包同源码执行/同版本恢复最后验证。当前仅合同已接纳，无新模型、生产或 stable 实证。
