# ADR 0081：fixed server 停止意图与可恢复 Outcome

- 状态：提议（Proposed）。这是 B1 实施前的合同缺口说明；尚未接受、接线或实机验证，不能据本文放行取消/超时。
- 关联：ADR 0012、0056、0062、0069、0076、0079、0080。

## 从实际调用链发现的问题

当前 `PublicApplicationPort` 没有取消 operation；历史 `internal/server` 的 task cancel 不属于 fixed server 生产入口。`CompositionLedger.terminalizeCompletedAttempt` 只接纳已完成结果，不能直接用于用户取消。`run.aborted` 的闭集也不接受 `RUNNING`。虽然 v2 `TerminatePreparedExecution` 已有 barrier 后的安全终止实现，直接从 HTTP context cancellation 调用它仍缺业务授权和最终 Outcome。

另一个独立缺口是 `ensureAttemptLease` 初次 reserved claim 使用固定两小时 expiry。恢复保留原值是正确的，但不能把这个内部 lease expiry 宣称为用户已确认的业务 wall-timeout；HTTP deadline 更不是业务 deadline。

## 提议的最小合同

1. 沿同一个 `PublicApplicationPort` 增加 typed cancel，不复用兼容 server、不直接调用 Supervisor。请求绑定 current Run/Attempt/sequence/head、认证操作者和有界原因；客户端提交的 actor 字符串不能自行授予权限。首个范围仅覆盖已 sealed 为 `RUNNING` 且有完整 started authority 的当前 Attempt；尚未放行的 preparation/reservation 沿 ADR 0069 的既有恢复，不用取消入口猜测零副作用。
2. 在现有 Attempt authority 账本内冻结停止意图，绑定请求摘要、原 Run head、操作者/原因和停止类别；它必须与 ADR 0056 的 barrier/admission closure/eligibility generation bump 原子提交。不能仅靠 transport pending 保存“为什么取消”，也不新增旁路 JSON 状态库。新字段、canonical 编码和 replay 规则在接受本 ADR 时一起冻结。
3. 用户取消使用正常完成族 `completed/attempt-aborted`，不冒充 security-critical revoke。只有权威原始 expiry 已到期才允许 `expired`；不得从 request 超时、EOF、Worker 文本或恢复时的新时间计算截止点。业务 deadline 的确认、首次持久化时点与 lease expiry 的关系必须在开放自动 wall-timeout 前冻结。
4. admission 与 stop barrier 竞争同一 authority CAS。结果先被接纳则保留其结论，返回取消已太晚的 typed 结果，不覆盖结果或签发新的取消成功；stop barrier 先成功则之后结果只能 quarantine。同一请求只恢复同一意图，不创建 successor 或重复消费预算。
5. barrier 之后才允许现有 v2 Attach/Terminate；随后沿 process terminal、allocation receipt、Supervisor Close 与独立 absence、cleanup completion/release 收口。身份不明保留 intervention，零跨编排 kill、零解锁。HTTP 断线只丢失响应；已提交停止意图由现有恢复路径继续，不复活执行资格。
6. 安全 cleanup 完成后，以绑定 stop intent 和全部 terminal facts 的单个 Run terminal event 收口，并通过现有 Outcome 持久化路径保留用户可见原因。业务终态的具体 event/状态/Schema 需在接受时明确；禁止借用现有 `run.aborted` 的结构例外绕过 reducer。event 已提交但 Outcome/response 丢失时从同一事实补齐，不能制造第二终点。
7. fixed delivery 继续只保存 pending/receipt-ref；响应成功必须引用上述精确 Run terminal receipt 与 Outcome，不以“TERM 已发出”回答任务已取消。冷恢复能从权威事实辨认原请求，transport 文件缺失不丢失业务意图。

## 接受前必须关闭的具体选择

- stop intent 的 closed Schema、barrier 原子载荷与同 request/不同 request 的幂等规则；不得先放宽既有 barrier 再补关联。
- 取消与超时各自的 Run event、状态、Outcome reason 与 reducer guard；保留现有 pre-Attempt/RETRY_PENDING abort 语义。
- 业务 deadline 从哪份已确认输入冻结，以及自动停止调度如何在 server 重启后消费原截止点；不得静默采用固定两小时作为用户 SLA。
- integration 层对 outcome 尚未落盘、cleanup-only 重入和终态 Run 冷重放的查询/响应约定。

## 同一纵切的验证与实施顺序

先冻结以上选择，再在一个连贯实现中接通 application→runtime→现有 barrier/Terminate/cleanup→Run event/Outcome→fixed delivery，不把孤立 handler 或新类型标为可用。测试覆盖：错误 current head/owner 零 mutation；admission/stop 两种 CAS 顺序；intent 前后、signal 前后、cleanup 后、event 后及响应丢失；同请求重放；原因替换拒绝；过期原始 deadline 与重启不延期；身份冲突零 kill/零 release；迟到结果；终态 Outcome 重建。

真实验证使用同一固定 bytes 的 server 和 cooperative Pi，保留本机签名/执行准入失败，不借用旧 image 或直接 Pi 调用。B1 仍须另外证明正常 Collect→Verify→独立 Decision→ACCEPTED；取消通过不能替代正常业务交付。
