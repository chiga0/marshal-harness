# ADR 0081：fixed server 停止意图与可恢复 Outcome

- 状态：提议（Proposed）。这是 B1 实施前的合同缺口说明；尚未接受、接线或实机验证，不能据本文放行取消/超时。
- 关联：ADR 0012、0056、0062、0069、0076、0079、0080。

## 从实际调用链发现的问题

当前 `PublicApplicationPort` 没有取消 operation；历史 `internal/server` 的 task cancel 不属于 fixed server 生产入口。`CompositionLedger.terminalizeCompletedAttempt` 只接纳已完成结果，不能直接用于用户取消。`run.aborted` 的闭集也不接受 `RUNNING`。虽然 v2 `TerminatePreparedExecution` 已有 barrier 后的安全终止实现，直接从 HTTP context cancellation 调用它仍缺业务授权和最终 Outcome。

另一个独立缺口是 `ensureAttemptLease` 初次 reserved claim 使用固定两小时 expiry。恢复保留原值是正确的，但不能把这个内部 lease expiry 宣称为用户已确认的业务 wall-timeout；HTTP deadline 更不是业务 deadline。

## 提议的最小合同

1. 沿同一个 `PublicApplicationPort` 增加 typed cancel，不复用兼容 server、不直接调用 Supervisor。请求绑定 current Run/Attempt/sequence/head、认证操作者和有界原因；客户端提交的 actor 字符串不能自行授予权限。首个范围仅覆盖已 sealed 为 `RUNNING` 且有完整 started authority 的当前 Attempt；尚未放行的 preparation/reservation 沿 ADR 0069 的既有恢复，不用取消入口猜测零副作用。
2. 在现有 Attempt authority 账本内冻结停止意图，绑定请求摘要、原 Run head、操作者/原因和停止类别；它必须与 ADR 0056 的 barrier/admission closure/eligibility generation bump 原子提交。不能仅靠 transport pending 保存“为什么取消”，也不新增旁路 JSON 状态库。新字段、canonical 编码和 replay 规则在接受本 ADR 时一起冻结。
3. 用户取消使用正常完成族 `completed/attempt-aborted`，不冒充 security-critical revoke。业务超时使用 `cancelled/deadline-exceeded`；它不等同内部 DispatchLease expiry，不能仅因业务计时到期写 `expired`。只有权威原始 lease expiry 已到期才允许该 eligibility 类别。不得从 HTTP request 超时、EOF、Worker 文本或恢复时的新时间计算业务截止点；具体来源见下节。
4. admission 与 stop barrier 竞争同一 authority CAS。结果先被接纳则保留其结论，返回取消已太晚的 typed 结果，不覆盖结果或签发新的取消成功；stop barrier 先成功则之后结果只能 quarantine。同一请求只恢复同一意图，不创建 successor 或重复消费预算。
5. barrier 之后才允许现有 v2 Attach/Terminate；随后沿 process terminal、allocation receipt、Supervisor Close 与独立 absence、cleanup completion/release 收口。身份不明保留 intervention，零跨编排 kill、零解锁。HTTP 断线只丢失响应；已提交停止意图由现有恢复路径继续，不复活执行资格。
6. 安全 cleanup 完成后，以绑定 stop intent 和全部 terminal facts 的单个 Run terminal event 收口，并通过现有 Outcome 持久化路径保留用户可见原因。业务终态的具体 event/状态/Schema 需在接受时明确；禁止借用现有 `run.aborted` 的结构例外绕过 reducer。event 已提交但 Outcome/response 丢失时从同一事实补齐，不能制造第二终点。
7. fixed delivery 继续只保存 pending/receipt-ref；响应成功必须引用上述精确 Run terminal receipt 与 Outcome，不以“TERM 已发出”回答任务已取消。冷恢复能从权威事实辨认原请求，transport 文件缺失不丢失业务意图。

## 具体实施选择（待随纵切接受，不是当前行为）

以下选择依据 `PublicApplicationPort`、`CompositionLedger.CollectRunResult`、`CompareAndAppendBarrier`、`AttemptReservationV1` 和现有 Run reducer 核对。尤其是 reservation 记录**没有时间字段**，不能假定存在 reserved-at，再在恢复时拿当前时间补造。此节把待实现方案收敛为一个纵切；ADR 仍为 Proposed，不单独放行 runtime mutation。

### 输入、身份与幂等

- fixed CLI 的封闭 activation 增加 `control-plane-cancel`，只映射 `marshal control-plane cancel`；不是绕过 self identity 的 bootstrap 命令。生成器、解码器、Schema、CLI 分类与真实入口测试必须一起更新。旧 activation 不原地扩权；其命令集合与新 binary 不匹配时继续拒绝，操作者为精确新 bytes/sourceHead 重新生成 activation。未知命令、错误 profile、缺失 activation 与身份漂移仍在连接 server 前拒绝。
- Public cancel 输入使用现有 `CurrentRunRequest` 四元组，再加有界 `requestId`。首版原因固定为 `operator-request`，不把任意自由文本、actor、PID、generation 或 deadline 当作客户端可授权字段。
- 请求者是现有 fixed server 本机受信调用边界的操作者；Core 记录从该边界取得的本机身份，不采信 JSON 自报 actor。这仍是单用户 ordinary-user 模式，不增加远程身份或多用户保证。
- 首次停止意图保存 `schemaRevision`、上述请求及其 canonical `requestDigest`、Core 观察的操作者、停止类别、精确 Attempt identity、原 Run sequence/head。所有字段进入同一 barrier fact 的摘要，零旁路状态文件。现有无 stop intent 的 completion barrier 原始字节和 replay 保持不变。
- 同 `requestId`、同原四元组与摘要恢复同一个 stop；同 ID 不同内容冲突，不能替换原因。已经存在其他 stop 时返回其只读引用，不追加第二意图、不消费 Attempt/rework 预算。Run 已终态时只允许精确已提交 stop 的只读/Outcome 补齐重放。
- fresh stop 在 held Run authority 与 ingress transaction 内再次检查当前 head、started authority 和 `CommittedResultFactDigest`。结果先接纳则返回 `stop-too-late`，不得改成取消成功；stop 先提交则原子关闭 admission 并提升 eligibility generation，后到结果 quarantine。

### 实施中的字节合同（尚未 enable）

`AttemptTransition` 的 barrier 增加可选 `stopIntent`，无此字段的历史事实保持原始编码与摘要；其他 transition 禁止携带它。`stopIntent` 使用 `schemaRevision=attempt-stop/v1`，绑定 `requestId`、原 Run `sequence/headDigest`、Core 观察的 `operatorUid`、`observedAt` 与封闭 `category`（`operator-request|attempt-deadline-exceeded|run-deadline-exceeded`）。外层 barrier 的 `AttemptIdentity` 进入意图摘要计算，不接受另一份可替换的 identity。`requestDigest` 由原 public 请求身份和 requestId 派生；`intentDigest` 则绑定全部意图字段及 AttemptIdentity。普通取消不携带 deadline witness，业务超时必须携带完整 witness，不允许混合形状。

deadline witness 保存 `specDigest`、`creationEventDigest`（首条 `planning.spec-accepted`）、`processStartedFactDigest`、`runCreatedAt`、`processStartedAt`、两个正整秒预算及两个计算后的 deadline。Core 加载原始 Task/Run/ProcessStarted 时验证来源；authority 记录重放时重算算法并验证摘要和选择规则，不能只信客户端时间或已存计算结果。上述字段只随同一 barrier CAS 一次提交。fresh stop 遇到已提交结果必须拒绝；原 stop 的精确重放不追加新事实。

此节是实现工作中的合同，取消/超时仍须完整生产接线和故障矩阵通过后，才能接受本 ADR、开启入口并更新 B1 状态。

### 业务截止点：复用不可变来源，而不是增加计时状态库

冻结算法版本为本 ADR 的 business-deadline/v1：

```text
runDeadline     = Run.createdAt + TaskSpec.budgets.runTimeoutSeconds
attemptDeadline = ProcessStarted.observedAt + TaskSpec.budgets.attemptTimeoutSeconds
effectiveDeadline = min(runDeadline, attemptDeadline)
```

- 三个来源均从精确 Run lease 下加载并验证：Task 原始 bytes 的 canonical 摘要与 `specDigest` 相同；Run 没有独立 `run.created` 事件，现有 planning producer 对 `NewRunState` 与首条 `planning.spec-accepted` 使用同一个 `now`，因此必须验证该首条 `CREATED → PLANNED` 事件的 timestamp 与快照 `CreatedAt` 精确相同，并绑定其事件摘要；ProcessStarted 是当前 Attempt 已接受的 Core/Supervisor fact。时间解析、正预算和加法溢出失败时拒绝，不从文件 mtime、HTTP deadline 或 WorkerResult 推导。历史记录若缺少该锚点只能拒绝业务 deadline 授权，不在恢复时补造时间。
- ProcessStarted 是实际 Resume 前的启动观测，因此该预算包含 exec-stopped/Resume 等待，不能推迟到第一次查询或重启。Run deadline 从创建计时；READY 阶段已耗尽预算时不得放行新的 Worker，但保持原 pre-Attempt 恢复/终态规则，不伪造 started stop。
- 同时到期时固定优先 `run-deadline-exceeded`。停止意图保存原始来源摘要、两个计算结果和选定类别；Core 在提交前重新计算，拒绝客户端提供截止点和重启延期。
- 原有两小时 lease expiry 仍是执行资格上限，不是用户 SLA。业务 deadline 通常更早，使用 `cancelled/deadline-exceeded` 屏障收口。即使尚未到内部 lease expiry，也不能继续接纳已过业务截止点的新结果；admission 必须在同一提交边界执行这一检查，不能只依赖 timer 抢先运行。
- server 恢复先扫描当前非终态 Attempt 的不可变来源与未完成 stop，再允许该 Attempt 的新推进。进程内 timer/活跃索引只作有界调度提示；每次消费仍重读权威事实，不持有第二份 deadline 权威。扫描频率/容量测试属于同一实现，不以另起 watchdog 替代 Core 恢复。

### 终态、查询与崩溃恢复

- 新增封闭事件 `worker.stopped`，仅允许 `RUNNING → BLOCKED`，actor 固定 `system/marshal-core`。`BLOCKED` 明确表示本 Run 已停止、需新 Run 才能继续，不表示任务已成功。保留既有 pre-Attempt 与 RETRY_PENDING 的 `run.aborted` 语义，不开放通用 RUNNING abort 例外。
- payload 精确绑定 stop intent/barrier、process terminal、allocation terminal、Supervisor closed/absence、cleanup released 摘要；`terminalReason` 只允许 `aborted-by-operator`、`attempt-deadline-exceeded`、`run-deadline-exceeded`。guard 同时验证原 Attempt、原 Run head 和当前 cleanup completion，不接受仅发出 TERM 的“成功”。
- barrier 后复用 v2 Terminate/Inspect、allocation release、Close/独立 absence、cleanup release。已成功的步骤只读重放；身份冲突保持 intervention，不 kill 猜测 PID、不释放可能仍有写入者的 worktree。
- Run 终态事件是提交点，Outcome 用现有可恢复 record 路径物化并绑定该事件。event 后崩溃只补同一 Outcome；Outcome 未就绪时返回 typed pending，不生成第二事件。完成响应必须包含精确 Run、stop、terminal receipt 与 Outcome 摘要。
- 状态查询在 stop 尚未完成时保留 Run 当前状态并附有界 stop/recovery 投影；投影不是 Run 转换或新授权。重启、客户端断线或 delivery 文件丢失都从 stop fact 继续，不能恢复原执行资格。fresh cancel 超时不等于取消撤销。

Collect 对已完成 stop 使用封闭 `run-stopped` 错误，不伪造 CollectedRunProjection、成功 receipt 或独立 Decision；fixed HTTP 返回 409，客户端据此停止 live polling 并查询 Run/Outcome。返回该类别前必须验证当前 Attempt cleanup、精确 `worker.stopped` 和 Outcome，单纯看到 BLOCKED、原始 deadline 到期或已经发送 TERM 均不足以授权。丢响应后的 Collect 从原 Run/Attempt/sequence/head 与当前存储意图连接，内部取原 stop requestId，再复用同一 terminal verifier；不能用这条读取/物化路径创建停止意图。部分投影、未知故障仍为 pending，不通过错误分类消除不确定性。

### 接受与 enable 门槛

常驻写调度必须覆盖整个 `delivery Begin → application → receipt reconcile/commit`，而非只锁 application。HTTP Start（包括精确重放）、Collect/Cancel/Verify/Review/Decision 与后台 deadline/Outcome 恢复共享同一个进程内 writer lane；后台 Try 不排队，公开请求在原有有界 inflight/queue 和请求 context 下等待。Status/Inspect 不经过该 lane。该调度锁不授予业务权限、不代替 durable CAS/Run lease；pending 已耐久后的原 deadline/丢响应恢复语义不变。所有资源的锁顺序为 lane → application mutex（适用时）→ 原有 Run/owner/ledger 规则，禁止在后台 callback 递归发起公开写请求。长写事务造成的停止延迟仍需单独证明，不能把互斥修复称为实时 deadline 保证。

实机候选与发布门禁必须分开：取消候选尚未满足本节实机要求时不得先合并 main，也不能被 main-only release CI gate 阻止验证。仅显式 `order-quote-cancel` 的未合入 `feat/` 分支，允许用 canonical 仓库、workflow dispatch SHA 与 expected-head 相等、同精确 SHA/分支最新手动 CI 五项成功的 candidate-only gate 做隔离实机验证；不创建 tag、release、独立 Decision 或 production 声明。main 上的任何场景及其他场景仍走原 main push CI gate，正式发布脚本和权限不变。此候选验证许可不等于接受本 ADR 或开启正式支持。

当前证据补充：`20a9999` 的 CI 34026216770 五项全绿，覆盖下述 READY 准入。后续 fixed CLI cancel 与显式 `order-quote-cancel` 驱动只补测试入口：request key 派生唯一 stop requestId，未知响应不重试，已证明停止的 Collect 返回非成功退出码。驱动必须验证精确 receipt/Outcome、同请求重放与终态查询，不生成独立 Decision；尚未运行真实取消 canary，不能用脚本测试替代 enable 门槛。

停止场景进一步覆盖成功后的冷 server：关闭 server2 后以同一固定 bytes 启动 server3，独立 evidence 目录记录原取消请求、原 deadline、精确 receipt/Outcome 的再次验证与终态 Collect。只读取本 Run 固定目录中的有界证据来重建封闭参数，证据不替代服务端 authority；不延长预算、不重启 Worker，未知响应不再次取消。24 项 Python 回归通过不等于这一场景已实机通过。共享 release 观察也已改为同时接受两类经耐久账本重读的合法终态：已接纳正常结果，或 sealed stop intent 与闭合 eligibility；停止绝不能伪造 CommittedResult 来复用正常完成分支。

READY 原始预算准入候选已在 preparation 的 ReserveAttempt 前和 bridge 进入启动链前接入，使用同一冻结 TaskSpec/首事件与当前 Run head；恰好到期即拒绝。拒绝不创建 started stop；已提交启动结果由既有 replay 先行恢复。两次检查不是整个 launch 的原子 deadline：检查后到 Resume 的窗口、长 public mutation 的调度延迟仍须与同路径停止机制一起验证。`a04d76c` 的 CI 34025131805 五项全绿覆盖此前常驻循环/Outcome 恢复，不覆盖本次新增准入；下文“仍未完成”的历史候选描述以此项和当前 Roadmap 表为准，完整 enable 门槛不变。

常驻调度候选复用 fixed server 自身生命周期：启动恢复时登记 RUNNING Run，StartRun 在可能提交启动结果前登记；索引和轮询游标只存在内存、可由账本重建。每秒最多公平处理三个 Run，每轮 30 秒 context 上限；后台不排队抢占正在执行的 public mutation。每次推进重新读取 Run、当前 owner、原业务预算和 Attempt，调用同一 stop barrier/cleanup，而不是自行构造 deadline 或 PID。server shutdown 先取消并 drain 此循环，再释放 delivery/session/owner。停止事件后 Outcome 未完成的 BLOCKED Run 在启动扫描和活跃项处理中走已存意图恢复；不能因为 Run 已非 RUNNING 就永久漏掉 Outcome。该候选尚需实机故障和容量验证；长 public mutation 的 deadline 响应上界及 READY 阶段准入仍待关闭。

实现核对发现：`ReadRunStartAuthorityUnderLease` 只在 `READY/RUNNING` 返回启动 worktree 等冻结输入，`BLOCKED` 终态不返回这些字段。因此 event 后丢响应的恢复不能再次走 `openRun`，也不能为了恢复响应补造 worktree/launch closure。已提交终态的 Outcome 补齐由 `RepositorySession.ReconcileStoppedRun` 在现有 Run lease、当前 owner 和 ingress 下直接连接原请求、当前 Attempt cleanup 与精确 `worker.stopped` 事件；它不得创建停止意图、启动/Attach Worker 或消费预算。尚未提交 terminal event 的停止仍走原 runtime cleanup 恢复，两条路径不能混用。

当前取消实现仍是未发布的纵切候选：已接入 public `CancelRun`、fixed `/v1/runs/cancel` 与原 delivery pending/receipt，贯穿 barrier 意图模型、terminal eligibility/cleanup、封闭 `worker.stopped` 和终态 Outcome 恢复；原始业务预算读取、deadline admission transaction 检查与已有 stop intent 的恢复 cleanup 已接入候选。READY 到期准入、resident timer、对外停止状态/错误闭环和完整故障矩阵仍未完成。允许在隔离开发分支保存候选并运行 hosted CI，不能合并放行或声称 B1 已完成。当前本地 Go 检查为 compile-only（不执行测试二进制），另有 vet/staticcheck、架构与 diff 检查；新增动态测试和 race 仍需对应 sourceHead 的 hosted CI 证据。f41b3aa 的 Linux contract/metaschema 检查通过不能替代最新候选动态验证；该版本 macOS session 关闭测试死锁另已定位修复，不原样重跑。不得把这些编译结果记作业务通过。

实现必须一次接通 application、Core、barrier、v2 cleanup、Run event/Outcome、fixed transport 与恢复扫描，并补齐下节故障测试，再接受本 ADR 和 enable。不得只提交新类型/handler 就把取消列为可用。与正常 Collect/admission 的竞争必须在同一生产组合路径测试；timer-only 或 mock-only 通过不关闭 B1。

## 同一纵切的验证与实施顺序

先冻结以上选择，再在一个连贯实现中接通 application→runtime→现有 barrier/Terminate/cleanup→Run event/Outcome→fixed delivery，不把孤立 handler 或新类型标为可用。测试覆盖：错误 current head/owner 零 mutation；admission/stop 两种 CAS 顺序；intent 前后、signal 前后、cleanup 后、event 后及响应丢失；同请求重放；原因替换拒绝；过期原始 deadline 与重启不延期；身份冲突零 kill/零 release；迟到结果；终态 Outcome 重建。

真实验证使用同一固定 bytes 的 server 和 cooperative Pi，保留本机签名/执行准入失败，不借用旧 image 或直接 Pi 调用。B1 仍须另外证明正常 Collect→Verify→独立 Decision→ACCEPTED；取消通过不能替代正常业务交付。
