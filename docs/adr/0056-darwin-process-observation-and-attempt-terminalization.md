# ADR 0056：Darwin ordinary-user 进程观察与 Attempt 终结事务

- 状态：已接受（Accepted，2026-08-28；唯一 aggregate rework 已关闭 launch authority、lease/cleanup 正交性、authority CAS barrier、Darwin 控制单元与 server 状态五项 P1）。接受只冻结 Darwin ordinary-user 的最小进程观察、终结与恢复合同；不表示实现已经完成，不升级 I186-R3–R5 的 `COMPONENT` 状态，也不把普通宿主进程描述为 hardened sandbox。
- 关联：[ADR 0018](0018-control-plane-and-provider-ports.md)（Core 权威账本与 DispatchLease）、[ADR 0045](0045-strangler-cutover-and-single-recovery.md)（单一恢复方向）、[ADR 0049](0049-location-attestation-and-failure-classification-authority.md)（LocationClaim/LocationFact 分权）、[ADR 0051](0051-darwin-local-dogfood-profile.md)（Darwin ordinary-user 边界）、[ADR 0052](0052-v1-release-scope-and-production-reachability.md)（v1.0 单节点纵切）、[ADR 0053](0053-pre-r4-contract-gates-and-single-recovery-model.md)（单一恢复模型）、[ADR 0055](0055-sandbox-exec-workload-envelope.md)（allocation-carried Exec）、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)。

## 背景

v1.0 的 Darwin Local 纵切已经能在 allocation 中启动真实 Agent，但现有实现仍缺少可跨 Core 重启恢复的进程权威：

- `LocalRunner` 的 allocation、lease、intent 与 `liveProcess` 只在内存中；重建 runner 后无法从耐久事实识别旧进程；
- 执行器内部持有 `exec.Cmd.Process`，Core 没有统筹 spawn 并持有 PID/进程组、工作目录与 executable 的同一启动观察；`Signal`/`Terminate` 不能可靠触达在途执行；
- 通用 orphan recovery 可能在证明旧进程树已退出前解锁 worktree 或产生 successor Attempt；
- DispatchLease 有异常的 expiry/cancel 资格终态，但没有正常完成的资格终态，也没有与资格终止正交的 `cleanup-completed` 和 cleanup binding release 事实；
- ADR 0049 已冻结 claim/fact 分权，但 Darwin Local 的 Core observer、进程 identity 与耐久 Fact 生产尚未接线。

这不是单纯的实现遗漏：进程 identity 的持久化字段、谁有权观察、何时允许 kill、Attempt 业务结果与资源清理的恢复顺序、正常 lease 终态都会改变持久化和生命周期合同，因此必须先由 ADR 冻结。

## 决策

### 1. 适用范围与权威边界

1. 本合同仅适用于 [ADR 0051](0051-darwin-local-dogfood-profile.md) 的 Darwin trusted single-user ordinary-user/workspace-write profile。它不提供恶意代码隔离、不可逃逸保证、跨用户权限或 hardened/Linux authority；稳定发布仍受 ADR 0047/0048/0052 的独立门禁约束。
2. ADR 0018 的 authority ledger、Run journal 与 DispatchLease ledger 仍是唯一权威写路径。本文新增的观察和终结事实必须由 Core append-only 持久化；`LocalRunner` map、进程对象和文件描述符只允许作为可重建的 live projection，不得成为第二真值。
3. SandboxProvider 可提供 `LocationClaim` 和诊断信息，但不得自签 `LocationFact`、进程 identity 或“已终止”结论。Core 的 Darwin observer 从自身持有的对象与内核查询产生事实，并按 ADR 0049 绑定 observer identity。
4. 本 ADR 不接受或实现 ADR 0035 的 `LeaseOwnerRecordV2`，也不改变 Run owner acquisition。只有调用者已由现有 Run authority 证明为当前合法 orchestrator 时，本文的 Attempt cleanup transaction 才可运行。
5. ResultIngress admission、Attempt terminalization barrier、DispatchLease eligibility 与 cleanup transaction 必须使用同一 authority store/transaction/CAS seam；禁止以两个 ledger 各自检查后顺序写入制造竞态窗口。

### 2. Core-owned launch coordinator 与 Darwin 进程观察

Darwin Local 的生产 composition root 必须由 Core-owned `LaunchCoordinator` 统筹 launch。它负责 nofollow 打开 cwd/executable、spawn、创建并观察独立 process group、持有 process handle/FD、读取内核 birth identity，并在放行 workload 前 append `process-started`。SandboxProvider 只能消费 coordinator 的不透明结果并产出 `LocationClaim`/诊断 receipt，不能自行 spawn 后把 PID 或 claim 提升为权威事实。

launch 顺序固定为：准备 held objects → 在 launch barrier 下 spawn 并建立 `pid == pgid` → Core 从 handle/内核读取 identity → 以 authority CAS 提交 `process-started` → 放行 workload。不能保证 workload 在事实提交前不执行的启动方式不属于 v1 支持路径；失败必须由仍持有 handle 的 coordinator 终止并 wait，不得降级为“先跑再补记”。实现不得生成匿名临时 helper executable，只能使用固定、已允许的 `marshal` 产物及 OS 原语。

`process-started` 的规范载荷至少包含：

- `schemaVersion`、`authorityNamespaceId`、`taskId`、`runId`、`attemptId`、`allocationId`；
- `leaseId`、`leaseDigest`、dispatch generation 与 fencing token 的摘要（不得持久化原始 token）；
- `commandId`、根 `pid`、根 `pgid`，且根进程必须满足 `pid == pgid`；
- 从 Darwin 内核进程查询得到的 birth identity（至少包含内核报告的启动秒与微秒，或具有同等抗 PID reuse 能力的稳定值），不得用 PID、argv 或用户态 wall clock 单独代替；
- canonical working directory 与启动前持有的 nofollow directory FD identity；identity 至少绑定 device、inode、file type、owner、mode，并绑定精确路径重验结果；
- canonical executable path 与启动前持有的 nofollow executable FD identity；identity 至少绑定 device、inode、size、file type、owner、mode、link count 与原始文件 bytes 的 SHA-256；
- observer identity、观察时间与全部字段的 canonical digest。

同一 live Core 还必须持有可向该精确进程发信号/等待的 process handle、根进程组与 cwd/executable 的 held FD，直到完成 wait 或终结事务。handle/FD 不可持久化；Core 重启后只能以 `process-started` 的 birth/PGID/path/object identity 配合新的内核观察恢复。held executable FD 只证明启动对象，不得冒充对任意时刻进程映像的 hardened attestation。

如果 Core 在 launch barrier 内、`process-started` 提交前崩溃，恢复方没有权凭扫描或 PID 猜测 kill，只能 fence 该 Attempt/worktree、禁止 successor，并写入人工介入 Outcome；受 barrier 约束的子进程不得执行 workload。该 fail-closed 窗口必须有故障注入测试，不得用临时内存记录掩盖。

### 3. v1 控制单元与跨编排保护

1. v1 在 Darwin ordinary-user 上的唯一控制单元是由 Core `LaunchCoordinator` 创建并观察的**单个 process group**，不是“所有后代进程”或宿主级 containment。受支持 workload 必须 cooperative/non-detaching：不得调用 `setsid`、迁出 PGID、daemonize/double-fork 或把工作转移到未受观察的进程。
2. 任何 group signal/terminate 前都必须重新验证根 PID、birth identity、`pid == pgid`、allocation/Attempt/lease generation、cwd 与 executable object identity。任一字段未知、变化或冲突，结论固定为 `process-identity-conflict`，**零 kill 副作用**。
3. 对已验证的控制单元，Core 可以有界地执行 group TERM → wait/observe → 必要时 group KILL → 再观察 absence；重放每一步前都重新验证 root birth 与 PGID ownership。全机 `ps`、argv 匹配、PID-only 或仅凭数字 PGID 的 kill 永不构成权威路径。
4. 检测到 descendant detach/reparent/new session 或 group ownership 不再可判定时，普通 Darwin 无法承诺终止所有后代；必须记录 `process-identity-conflict` 并 fence，禁止解锁/successor，不得扩大 kill 范围或宣称 containment。
5. 只有记录中的当前 authority tuple 与当前合法 orchestrator 完全匹配，才允许发信号。另一个 Run、Attempt、allocation、lease generation、authority namespace 或 orchestrator 的进程，即使路径/argv 相同也不得触碰。

### 4. Attempt 终结事务

每个启动过进程的 Attempt 只有以下单调事实链：

```text
process-started
  → attempt-terminalization-barrier
      （ResultIngress admission 结论 + termination-intent + lease eligibility terminal，同一 CAS）
  ├→ process-absent | process-terminated
  │   → allocation-terminated
  │   → cleanup-completed
  │   → lease-released（只释放 cleanup binding）
  └→ process-identity-conflict
      → intervention（禁止 cleanup-completed/release/unlock/successor）
```

其中：

1. 每个事实都绑定 `terminalizationId`、前一事实 digest、完整 authority/Attempt/allocation/lease/generation tuple、预期 authority sequence 与自身 canonical digest。同 ID 同内容重放幂等；同 ID 不同内容、不同终点或前序不连续一律 fail closed。
2. `attempt-terminalization-barrier` 由第 6 节的同一 authority CAS 原子提交；其中 `termination-intent` 与 DispatchLease eligibility terminal 一经可见，该 Attempt 进入 cleanup-only：禁止新 `Exec`/`Stage`/checkpoint、禁止接纳新的业务结果、禁止产生 successor；只允许持有专用 cleanup binding 的当前 Core operation 执行 `Inspect`/`Reconcile`/`Signal`/`Terminate`。
3. `process-absent` 表示内核观察证明精确 root birth identity 与其 process group 已不存在；`process-terminated` 表示精确控制组已被 group signal 终止，并再次观察为 absent。两者只证明本 ADR 的 cooperative control unit，不证明已收容或终止脱离该组的后代；检测到 detach 必须走 conflict。两者是允许后续 Provider allocation `Terminate` 的安全结论。
4. `process-identity-conflict` 是已确定的观察结论，但不是“安全清理完成”：未知不 kill，不得写 `allocation-terminated`、`cleanup-completed` 或 `lease-released`，不得解锁或启动 successor；Core 必须写入带原因码的 intervention Outcome。
5. `allocation-terminated` 绑定 Provider `Terminate` receipt 与前一进程终点。Provider lost response 只能经 `Inspect`/`Reconcile` 证明同一 allocation 已终结后幂等补记，不能由内存删除推断。
6. `cleanup-completed` 是独立于 DispatchLease eligibility 的 Core authority fact：只在安全进程终点与 `allocation-terminated` 已提交后成立，绑定同一 `terminalizationId` 与全部前序 digest；它是 unlock/successor 的必要条件。

### 5. Dispatch eligibility 与 cleanup completion 正交

1. Dispatch eligibility 与 cleanup completion 是两个正交轴。资格终态负责“立即停止新工作”；`cleanup-completed` 负责“旧控制单元已经安全收口”。任一资格终态都不得被解释为进程已退出，cleanup 未完成也不得为了释放 binding 而复活 dispatch 资格。
2. 现有 `expired|cancelled` 保持资格终态；正常 Attempt 增加 append-only `lease-completed` 事实与 `completed` 资格终态。正常原因采用独立封闭枚举：`attempt-completed`、`attempt-failed`、`attempt-aborted`、`orphan-reconciled`；不得借用安全撤销的 `CancelReason`。
3. 正常完成、cancel、expiry 或 security-critical revoke 一旦触发，都必须在 `attempt-terminalization-barrier` 中立即终止 eligibility、推进 generation 并使旧 dispatch fencing capability 永久失效。异常路径不得等待 kill/cleanup 才 fence。
4. eligibility terminal 后，Core 只签发绑定该 terminal lease generation、`terminalizationId`、operation allowlist 与当前 orchestrator 的 `cleanupBinding` authority fact；它随 barrier 耐久提交并可在重启后重建，派生 token/handle 不得成为第二权威。它不能授权 `Exec`/`Stage`/结果 admission，只能授权本事务的 Inspect/Reconcile/Signal/Terminate。cleanup binding 在 `cleanup-completed` 前保留，避免“先释放后无法合法清理”。
5. `lease-released` 是 append-only 的 **cleanup binding release fact，不是 DispatchLease state**。它只能在 `cleanup-completed` 后提交，并绑定 cleanup fact digest、Attempt/allocation/lease/generation 与 `terminalizationId`；提交后同 binding 永久不可复活。
6. 旧 ledger 可按原语义重放，禁止重写历史；新状态/事实未知、缺少绑定、资格终态冲突、cleanup/release 乱序时 fail closed。

### 6. ResultIngress admission 与 terminalization 共用 authority CAS barrier

ResultIngress admission 与 `attempt-terminalization-barrier` 必须竞争同一 Attempt authority slot/expected sequence，并在同一 authority transaction 中 compare-and-append。barrier 原子绑定：当前 Run/Attempt/allocation/lease generation；ResultIngress 已提交的 admission digest/sequence，或原子写入 `result-admission-closed`；`termination-intent`；以及第 5 节的 eligibility terminal/generation bump。两条路径只能有一种顺序：

- admission CAS 先成功：barrier 必须读取并绑定已提交结果，业务结论固定可重放；
- barrier CAS 先成功：它原子关闭 admission，之后到达的结果只能 quarantine；
- CAS conflict/authority unknown：不推断先后，进入 intervention。

禁止“先读 ResultIngress、再另写 termination intent”的分步 check-then-act，也禁止用内存 mutex 代替 authority CAS。对完成、失败、abort、timeout、Core crash、Provider lost response 与 orphan recovery，唯一顺序为：

1. 先取得当前 Run authority，并从**耐久 current ledger**复核精确 Attempt/allocation/lease generation；不是当前 owner 或 authority 不确定时零副作用；
2. 通过上述 authority CAS barrier 固定 ResultIngress 结论并立即终止 eligibility；不得从 transcript、文件或 Worker 声明推断成功；
3. 恢复 barrier/cleanup binding，按第 3/4 节 Inspect/Reconcile/Terminate，写入安全进程终点、Provider allocation terminal receipt、`cleanup-completed` 与 `lease-released`；
4. 只有 `cleanup-completed` 已提交后，才允许解锁 task worktree：已提交结果重放 `worker.completed`/后续 verification；无已提交结果才由 ADR 0053 的 generic recovery 决定失败、retry 或新 Attempt；
5. `process-identity-conflict`、CAS/ResultIngress conflict 或任一未知状态均禁止 unlock/successor。barrier 后到达的新结果进入 quarantine；只有 barrier 已绑定的先前 admission 可以幂等重放。

因此 ResultIngress 决定“业务结果是否存在”，eligibility terminal 立即阻止新工作，`cleanup-completed` 决定“旧执行是否安全退出”；三者由同一 barrier/transaction 串成一个结论，不得各自生成竞态结果。

### 7. 崩溃、ABA、伪造与重放负向矩阵

| 场景 | 唯一结论 / 必须验证的性质 |
| --- | --- |
| launch barrier 内、`process-started` 前崩溃 | workload 不得执行；无权 PID 扫描或 kill；fence Attempt/worktree、禁止 successor、intervention |
| `process-started` 后、terminalization barrier 前崩溃 | 从同一耐久观察恢复；以 admission/barrier 同一 CAS 决定先后 |
| admission CAS 与 terminalization CAS 并发 | 只有一个 expected sequence 成功；结果要么被 barrier 绑定，要么成为 late/quarantine |
| intent 后、signal 前崩溃 | cleanup-only 幂等恢复，先重验 identity |
| TERM/KILL 中途崩溃 | 每一步重新观察；只对同一 root birth/process group 继续，不盲目重复 signal |
| kill 后、进程终点事实前崩溃 | 观察 absence 后以同一 `terminalizationId` 补记，不能推测 |
| 进程终点后、Provider `Terminate` 前崩溃 | 幂等重放 `Terminate` 并绑定 receipt |
| allocation terminal 后、`cleanup-completed` 前崩溃 | 保留 cleanup binding；重验 receipt/absence 后补记 cleanup fact |
| `cleanup-completed` 后、`lease-released` 前崩溃 | 幂等释放 cleanup binding；不得恢复 dispatch eligibility |
| release 后、业务 journal 决策前崩溃 | 由 ResultIngress 已提交事实重放同一业务结论 |
| PID reuse；同 PID 不同 birth | `process-identity-conflict`，零 kill、零 release |
| PGID reuse；leader birth 不同或 root 非 group leader | conflict，零 kill |
| executable 同路径但 device/inode/digest 变化、swap-back；cwd rename/symlink/FD identity 不符 | conflict，零 kill |
| workload `setsid`/迁出 PGID/daemonize，或 descendant reparent/session escape | conflict，零扩大 kill、零 successor；普通用户不宣称全后代 containment |
| Provider 伪造 claim/fact/termination；observer 自证；tuple/digest/sequence 错 | 拒绝，Provider 声明只作诊断 |
| stale generation/token、已 terminal eligibility、已 release cleanup binding、同 terminalizationId 不同 digest | 拒绝；只有精确 cleanup binding 可继续清理 |
| intent 后 late result | 未预先提交则 quarantine；不得复活 Attempt |
| 第二 orchestrator、PID-only、argv-only、全机 `ps` 匹配 kill | 机械拒绝，零副作用 |
| Core 重启后 `LocalRunner` map 为空 | 从 authority facts 恢复 projection；不得把空 map 解释为无 allocation/无进程 |

## 后果与实施门禁

- 这是 R3–R5 的 release-critical 合同补齐，不新增服务、队列、通用 workflow、远程 scheduler 或第二状态库，也不扩大 [runtime architecture](../runtime-architecture.md) 的终态职责图。
- 实现必须沿现有 composition root 接线：Core observer/authority store → Local allocation/Exec → sandbox bridge → `execution.Service` recovery；禁止先落孤立 package 再声称集成。
- 最小实现必须同时覆盖：Core-owned launch coordinator/barrier、耐久进程观察/重放、Core-held live handle、Darwin birth/FD identity、Provider terminal receipt、eligibility terminal、`cleanup-completed`/cleanup binding release、admission/terminalization authority CAS，以及本 ADR 全部负向矩阵。
- 单元/集成测试至少包含 crash point table、CAS race、PID/PGID/process-reuse ABA、cwd/executable swap、伪造 Provider fact、跨 orchestrator kill 拒绝、detach/daemonize、late result、lost response 与两次重启重放；真实 Darwin 测试必须使用固定已允许的 `marshal` 二进制，不生成匿名临时 executable。
- 实现完成前，`LocalRunner` 纯内存 allocation/process projection、正常 eligibility terminal、cleanup completion/binding release 和 cleanup-before-successor 都是开放缺口；I186-R3–R5 继续为 `IN_PROGRESS/COMPONENT`。
