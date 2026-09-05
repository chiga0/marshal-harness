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

- Public cancel 输入使用现有 `CurrentRunRequest` 四元组，再加有界 `requestId`。首版原因固定为 `operator-request`，不把任意自由文本、actor、PID、generation 或 deadline 当作客户端可授权字段。
- 请求者是现有 fixed server 本机受信调用边界的操作者；Core 记录从该边界取得的本机身份，不采信 JSON 自报 actor。这仍是单用户 ordinary-user 模式，不增加远程身份或多用户保证。
- 首次停止意图保存 `schemaRevision`、上述请求及其 canonical `requestDigest`、Core 观察的操作者、停止类别、精确 Attempt identity、原 Run sequence/head。所有字段进入同一 barrier fact 的摘要，零旁路状态文件。现有无 stop intent 的 completion barrier 原始字节和 replay 保持不变。
- 同 `requestId`、同原四元组与摘要恢复同一个 stop；同 ID 不同内容冲突，不能替换原因。已经存在其他 stop 时返回其只读引用，不追加第二意图、不消费 Attempt/rework 预算。Run 已终态时只允许精确已提交 stop 的只读/Outcome 补齐重放。
- fresh stop 在 held Run authority 与 ingress transaction 内再次检查当前 head、started authority 和 `CommittedResultFactDigest`。结果先接纳则返回 `stop-too-late`，不得改成取消成功；stop 先提交则原子关闭 admission 并提升 eligibility generation，后到结果 quarantine。

### 业务截止点：复用不可变来源，而不是增加计时状态库

冻结算法版本为本 ADR 的 business-deadline/v1：

```text
runDeadline     = Run.createdAt + TaskSpec.budgets.runTimeoutSeconds
attemptDeadline = ProcessStarted.observedAt + TaskSpec.budgets.attemptTimeoutSeconds
effectiveDeadline = min(runDeadline, attemptDeadline)
```

- 三个来源均从精确 Run lease 下加载并验证：Task 原始 bytes 与 `specDigest` 相同，Run 创建事件与 replay 状态一致，ProcessStarted 是当前 Attempt 已接受的 Core/Supervisor fact。时间解析、正预算和加法溢出失败时拒绝，不从文件 mtime、HTTP deadline 或 WorkerResult 推导。
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

### 接受与 enable 门槛

实现必须一次接通 application、Core、barrier、v2 cleanup、Run event/Outcome、fixed transport 与恢复扫描，并补齐下节故障测试，再接受本 ADR 和 enable。不得只提交新类型/handler 就把取消列为可用。与正常 Collect/admission 的竞争必须在同一生产组合路径测试；timer-only 或 mock-only 通过不关闭 B1。

## 同一纵切的验证与实施顺序

先冻结以上选择，再在一个连贯实现中接通 application→runtime→现有 barrier/Terminate/cleanup→Run event/Outcome→fixed delivery，不把孤立 handler 或新类型标为可用。测试覆盖：错误 current head/owner 零 mutation；admission/stop 两种 CAS 顺序；intent 前后、signal 前后、cleanup 后、event 后及响应丢失；同请求重放；原因替换拒绝；过期原始 deadline 与重启不延期；身份冲突零 kill/零 release；迟到结果；终态 Outcome 重建。

真实验证使用同一固定 bytes 的 server 和 cooperative Pi，保留本机签名/执行准入失败，不借用旧 image 或直接 Pi 调用。B1 仍须另外证明正常 Collect→Verify→独立 Decision→ACCEPTED；取消通过不能替代正常业务交付。
