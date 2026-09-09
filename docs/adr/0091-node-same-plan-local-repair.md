# ADR 0091：Node 同计划局部修正与精确成果选择

- 状态：Proposed（2026-09-09；待维护者独立审查，不授予实施或完成声明）。
- 范围：ADR 0088 的单节点 Node 服务；在原批准计划、预算、期限内，由用户显式要求修正内容错误。不增加第二控制器、Workspace、发布权限或通用重规划平台。
- 基线：`7250ec95526e237aa918f164c71e9d7c7b0f7a62`。运行中问答按 ADR 0090 的独立实现接线后再合流；本决策不修改其在途候选。

## 1. 现有依据与精确差量

[ADR 0085 §5–6](0085-agent-team-service-contract-and-storage.md#5-节点交互生命周期与取消) 与 [ADR 0088 §4](0088-node-task-service-production-projection.md#4-唯一事务与恢复) 已要求有限返工、只重做受影响依赖、原预算不刷新。现有 `task-application/graph.mjs` 的 `affectedNodes` 可计算依赖闭包，但尚无 `task.repair`；`execution.mjs` 与 `verification.mjs` 仍按同 plan 的全部历史 Worker 选择输入，不能直接重置节点后追加第二次执行。

当前确有独立 `rejected` Decision：Core 在原 opaque receipt、当前 owner、真实 cleanup 与输入绑定成立时落库。不过 `task-verification-command` 把业务断言 false、检查器退出/协议错误、回调异常等都归为 failed，失败 evidence 通常为 null。`reasonCode`、失败字符串、Worker 总结或一个 rejected 标签均不足以判定可修正内容。

本决策只新增以下命名例外：**Node Task 聚合在明确的同计划修正接纳后从 `failed` 进入新 `queued` 周期**。原 Worker/Attempt/Decision/失败事件均不可变，旧 Go Run 的终态规则不变；`completed`、`cancelled`、`intervention` 不因此获得重开入口。改目标、依赖、Provider、权限、oracle 或预算的 replan 不在本切片。

## 2. 可修正失败必须有真实父验证判据

受信业务组合显式提供 `task-local-repair/v1` 策略，默认不启用。新 Plan 可选闭集 `repair={profile,policyDigest}`；完整有界策略由同一 Task 保存，含允许的业务断言名称及可选择的业务写节点。策略、父断言验证器与 checker 的稳定摘要随原计划/验收绑定冻结；函数只从受信 DI 按摘要解析，不序列化函数，不由 HTTP/模型指定。配置缺失、摘要漂移或业务插件不能消费修正输入时拒绝启用，不在失败后补配。无 repair 的旧计划保持原 bytes/digest，不因新增 Schema 获得此能力。

只有原可信命令验证 Adapter 的父验证分支可以产生 `contentRejection`，且必须同时成立：

1. 原 checker 身份前后未变；所属命令正常退出 0、输出完整、原 cleanup 有效；规范报告的 profile、nonce、ticket binding、必需断言名称/数量/形状全部通过。
2. 每个必需父验证函数实际执行完毕，严格返回 boolean；不允许 Promise、throw、缺值或以错误名称伪装。至少一项明确返回 false，且**全部失败项**属于冻结策略允许的业务内容断言；其他结构/授权断言失败不允许修正。
3. 原 Task 未取消、未过期，当前 owner/执行仍可接纳。取消、deadline、unknown、no-start、非零退出、坏帧、错绑定、checker/环境错误或 delivery 构造错误均不转成内容失败；结构性失败立即禁止原样重试。

此分支一次汇总失败项，不遇首项 false 就丢弃后续验证。结果携带 `contentRejection={policyDigest,failedAssertions,reportDigest}` 和原有界负面报告 evidence：保留规范报告原 bytes、请求/报告摘要、原执行及输入绑定、父断言结论；不重签剪裁报告冒充原 digest。报告沿原 256 KiB 命令输出上限、最多 64 项断言及现有 evidence 上限，超限拒绝分类，不静默截断。内容失败不调用成功 delivery 构造器，不产生交付物。

沿原不可序列化 Verification receipt 传给 Core；Core 重验当前事实，先将负面 evidence bytes 经原 Depot 耐久保存，再同事务记录其引用、`rejected` Decision 和 nullable `contentRejection`。此字段不是 HTTP 可提交的证明；原负面 Decision 仍保留，已有无此判据/报告的历史失败不能追认成可修正。无原始报告、摘要不匹配、伪造 receipt 均拒绝修正。

## 3. 唯一用户入口与接纳事务

新增 `POST /v1/tasks/{taskId}/repair`，应用 operation 为 `task.repair`；复用本地认证、对象授权、Host/Origin、Idempotency-Key 和有界 JSON。闭集请求为 `{expectedRevision,planDigest,decisionDigest,nodeIds,feedback}`：`nodeIds` 是 1–64 个唯一、已批准且策略允许的业务写节点，不含 Planner/Verifier；`feedback` 是最多 4096 UTF-8 字节的非空业务说明，拒绝 NUL/非法 Unicode。内容不能改变冻结合同，也不是任意 Agent 消息或发布许可。

当前 owner 先认证并查原 scope/key 回执：精确重放返回原接纳事实，另附当前 Task 投影，零追加/零启动；同 key 异内容冲突。新请求才做 Task revision CAS，并在同一 SQLite 事务核对：

- Task 当前为 `failed`，所指是当前周期最后一份、尚未被修正消费的精确内容拒收 Decision；plan、策略和原负面 evidence 匹配。Task 不曾由用户取消，原 deadline 未过，所有原执行/命令均已合法结清，无 active/pending/unknown 占用；意图或 PID 不替代原 cleanup。
- Core 用原 DAG 计算 `affectedNodes(nodeIds)`，包含全部后继和最终独立 Verifier，不能由请求裁剪闭包。闭包外每个需供结果的节点都有仍适用的精确已接纳候选；缺失、被取消或未知的无关分支导致拒绝，不能顺便重跑全队。
- 原剩余 Attempt 预算至少覆盖闭包所需的新执行数（含 Verifier），总 `maxWorkers`、输入/输出上限和绝对期限仍适用。接纳不退款、不加预算，也不提前重复扣 Attempt；实际 `nextWork` reservation 仍是唯一扣费/占容量点。

事务原子追加修正事实、控制事件、Task revision/reworkCount、当前结果选择的失效、Operation、原幂等回执及仅闭包节点的 execute outbox。成功返回 202 的 `RepairReceipt={taskId,repairId,operation,acceptedRevision,planDigest,decisionDigest,affectedNodes,task,currentTask,replayed}`，不是修正完成。新事实至少冻结 repairId/周期序号、原 Decision 引用、根节点/闭包、feedback 及摘要、保留结果摘要、原 plan/策略与绝对期限。失败事务不留下部分周期或命令；锁内不执行 Agent、文件操作或外部验证。

这一次显式请求授权原合同内的修正，不重跑 Planner、不另建同义 Task、不要求逐 Worker 再批准。`allowedActions` 仅在上述资格可满足时展示 repair；API/schema/客户端/审计同步暴露当前拒收和闭包，不自动刷新 CAS/key、自动 repair 或把用户选根当作错误已被权威定位。

## 4. 每节点明确选择，复用原执行与验收

同一 Store 增加有界 `selectedResults`：按原 nodeId 明确指向 Core 已接纳的 `workerId/resultDigest/candidateManifestDigest`，未选中或已失效为 null。Planner 仍只提供原冻结计划；Verifier 的 Decision 单独绑定本次完整选择，不充当上游业务 Candidate。旧 Attempt、结果和 bytes 全部保留；“选中候选”不冒充该分支已单独通过整体业务验收。

修正事务只清空闭包的当前选择并将这些节点置 pending；闭包外引用与已适用的独立证据原样保留。当前 Task 转 queued、acceptance 转 pending，清除仅属于旧周期的当前失败标志，原失败/Decision 留在不可变历史和修正来源中；原 Task ID/createdAt、批准回执、plan revision/digest、累计 Attempt 与 deadline 不变。统一改造 `nextWork`、直接依赖输入、`fileLayoutDigest`/manifest、Verification ticket、最终 current-ledger recheck：只消费明确选择，不从全部历史 Worker、mtime 或“最新一次成功”猜选。每个业务产出节点恰有一个当前结果；重复/陈旧/不匹配引用拒绝。

原 Supervisor 继续调度，reservation/command/ticket 的摘要绑定 `repairId`（初始周期为 null）、该节点的新输入和所消费的 selected-result 引用。事务中检查当前周期、owner、节点 pending 与原预算后才取得新 Attempt。旧周期未消费命令必须已结清或被原子封闭，不能因节点重新 pending 而复活；旧 finish 可保留原合法清理，但不得改新周期选择、节点状态或 Decision。

业务 `prepare` 从冻结 `ticket.input.repair` 取得原负面报告引用/失败断言、用户说明及本节点对应范围；这些只是诊断输入，不替代原 prompt/context、直接依赖或 oracle。受信插件按原 Depot 摘要读取，不能到旧可变 Worker 目录拼结果。每个新写节点使用新独立目录；Git 使用原锁定 base 的新 worktree，单 writer。未确认原 cleanup 的目录不释放、不复用；不让 Worker commit/push/merge。

最终独立 Verifier 必须实际再执行，消费“保留的无关结果 + 新闭包结果”的完整精确集合和适用交互引用。Core 沿原 stage 在锁外核对真实输入/候选 bytes 并耐久保存输出；结果接纳事务重查当前 repairId、全部 selected-result/manifest 及已核 bytes 的不可变引用、原 plan/policy/owner/期限与取消状态，再同事务提交 Decision、制品引用、Task completed 和修正 Operation succeeded。再次内容失败记录新负面报告/Decision，Operation failed；取消/其他执行失败/unknown 按原真实义务收口 Operation，不能只实现成功分支。只有新失败重新满足 §2–3 才可显式再次修正。原失败周期不改写，新进度不冒充旧验收通过。

## 5. 竞争、问答与同版本恢复

修正只对已结清的失败周期准入，不另加对所有 failed Task 的通用 cancel 入口。原周期的取消先赢则不产生可修正内容判据；已有取消意图一律拒绝 repair。repair 接纳后的新 queued 周期仍可正常 cancel，按原 Task CAS 排序并先持久 fence，再停止所属执行；cancel 与新 dispatch/finish 的两种顺序均不能产生迟到接纳。pause 只阻止新 dispatch/未授权答复投递，不刷新期限；到期停止新准入，原 deadline 之后仍允许凭真实证据结清 cleanup，不接受迟到业务成果。预算耗尽保留失败 Outcome，不隐式新建 Task。

[ADR 0090](0090-node-runtime-business-questions.md) 的问题始终属于原 Worker/execution。保留候选只能携带其原已接纳 `interactionRefs`；失效节点的问题/答案/ACK 保持历史，不投递给新 Attempt、不伪造新 ACK、不恢复旧等待表。确需新问题时使用新执行绑定和新 question ID，仍计整 Task 原问题限额。修正输入可按冻结业务策略引用历史答案作为数据，但不把它当作新执行的原生消费事实；最终验收不能遗漏保留及新成果对应的答案引用。

沿 [ADR 0089](0089-node-execution-custody-and-cleanup-recovery.md) 恢复原义务，不新增恢复控制器：修正事务前崩溃是原失败周期，提交后是完整新周期；均无半张选择表。旧 generation 的 prompt/命令/答案不重放，旧业务输出不接受。新周期执行中崩溃只凭原 custody 证据结清失败/取消；覆盖不足继续 unknown，不派替身、不退款。已提交但经原命令/Worker 事实证明从未 reservation 的旧代修正命令可结清为未启动的中断失败，不能捏造 worker.stopped 或自动重派。修正不中断后的正常完成可冷重开查询、原回执精确重放及相同 bytes 下载。

新持久语义使用 `marshal-node-task-sqlite/v4-repair` 及相应新服务格式，完整继承 v2-custody/v3-interaction；所有旧 v1/v2/v3 reader 在打开/claim 前必须拒绝。首批仅新空根，不在旧根补 selectedResults、推导内容失败、补签或双写；同名字段被旧 reader 忽略不是兼容。旧根历史及其原规则保留，受控升级另行验证，不借本切片扩大 U1。

## 6. 一个实施包与一次整链验收

待 ADR 0090 共享实现冻结后，由一个生产作者聚合完成：Application repair/selected-result 与原 Execution/Verification/问答消费者；原 command verifier 的父内容判据和负面 evidence；原 Supervisor/业务输入、Store/服务格式；HTTP/schema/客户端及发行清单。测试可按冻结接口并行，不增加第二 Store、Agent 控制服务或每字段 ADR。

用正式 HTTP→SQLite→真实受管 Agent 协议夹具/文件或 Git 写节点→独立 command checker 跑完整场景：初次两作者完成，其中 A 的内容断言失败，原负面报告耐久可查；用户只选 A，Core 重做 A 的闭包及 Verifier，B 的 Worker ID/结果/bytes 不变；新 Decision 与第三独立下载消费通过，冷重开保持全部原/新回执与历史。初始批准须预留实际所需 Attempt，验收不能临时提高预算。随后同路径真实模型验证不由此 fixture 冒充。

同一测试包一次覆盖：

- 严格业务 false 正例；同名异常、Promise/非 boolean、结构断言 false、坏帧/nonce、非零退出、checker 漂移、no-start/旧负面记录/伪 receipt 均不能准入修正；负面报告原 bytes 与 Decision 引用冷重开一致。
- 丢响应精确 replay、异内容 key、旧 CAS/Decision/plan、闭包外缺成果、SQL 回滚、结果重复/字节漂移；新 selectedResults 不混历史，原预算/期限/问题次数不刷新。
- cancel 两种顺序、pause/期限、旧 finish/旧 owner/旧答案迟到、cleanup unknown；修正前后 COMMIT 与 reservation 边界崩溃不重派，最终提交后冷下载相同，旧 reader 拒绝新格式。

本 ADR 只定义可实施合同；接受、组件测试、真实模型及 API-STABLE/B3 是不同事实。现有失败证据与未关闭的分权/恢复/发行边界不因局部修正而消失。
