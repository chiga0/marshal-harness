# ADR 0056：Darwin ordinary-user 进程观察与 Attempt 终结事务

- 状态：已接受（Accepted，2026-08-28）。接受只冻结 Darwin ordinary-user 的最小进程观察、终结与恢复合同；不表示实现已经完成，不升级 I186-R3–R5 的 `COMPONENT` 状态，也不把普通宿主进程描述为 hardened sandbox。
- 关联：[ADR 0018](0018-control-plane-and-provider-ports.md)（Core 权威账本与 DispatchLease）、[ADR 0045](0045-strangler-cutover-and-single-recovery.md)（单一恢复方向）、[ADR 0049](0049-location-attestation-and-failure-classification-authority.md)（LocationClaim/LocationFact 分权）、[ADR 0051](0051-darwin-local-dogfood-profile.md)（Darwin ordinary-user 边界）、[ADR 0052](0052-v1-release-scope-and-production-reachability.md)（v1.0 单节点纵切）、[ADR 0053](0053-pre-r4-contract-gates-and-single-recovery-model.md)（单一恢复模型）、[ADR 0055](0055-sandbox-exec-workload-envelope.md)（allocation-carried Exec）、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)。

## 背景

v1.0 的 Darwin Local 纵切已经能在 allocation 中启动真实 Agent，但现有实现仍缺少可跨 Core 重启恢复的进程权威：

- `LocalRunner` 的 allocation、lease、intent 与 `liveProcess` 只在内存中；重建 runner 后无法从耐久事实识别旧进程；
- 执行器内部持有 `exec.Cmd.Process`，Core 没有持有 PID/进程组、工作目录与 executable 的同一启动观察；`Signal`/`Terminate` 不能可靠触达在途执行；
- 通用 orphan recovery 可能在证明旧进程树已退出前解锁 worktree 或产生 successor Attempt；
- DispatchLease 有异常的 expiry/cancel 终态，但没有与正常 Attempt 完成绑定的 `lease-released` 终态事实；
- ADR 0049 已冻结 claim/fact 分权，但 Darwin Local 的 Core observer、进程 identity 与耐久 Fact 生产尚未接线。

这不是单纯的实现遗漏：进程 identity 的持久化字段、谁有权观察、何时允许 kill、Attempt 业务结果与资源清理的恢复顺序、正常 lease 终态都会改变持久化和生命周期合同，因此必须先由 ADR 冻结。

## 决策

### 1. 适用范围与权威边界

1. 本合同仅适用于 [ADR 0051](0051-darwin-local-dogfood-profile.md) 的 Darwin trusted single-user ordinary-user/workspace-write profile。它不提供恶意代码隔离、不可逃逸保证、跨用户权限或 hardened/Linux authority；稳定发布仍受 ADR 0047/0048/0052 的独立门禁约束。
2. ADR 0018 的 authority ledger、Run journal 与 DispatchLease ledger 仍是唯一权威写路径。本文新增的观察和终结事实必须由 Core append-only 持久化；`LocalRunner` map、进程对象和文件描述符只允许作为可重建的 live projection，不得成为第二真值。
3. SandboxProvider 可提供 `LocationClaim` 和诊断信息，但不得自签 `LocationFact`、进程 identity 或“已终止”结论。Core 的 Darwin observer 从自身持有的对象与内核查询产生事实，并按 ADR 0049 绑定 observer identity。
4. 本 ADR 不接受或实现 ADR 0035 的 `LeaseOwnerRecordV2`，也不改变 Run owner acquisition。只有调用者已由现有 Run authority 证明为当前合法 orchestrator 时，本文的 Attempt cleanup transaction 才可运行。

### 2. Core 持有的 Darwin 进程观察

成功启动 workload 后，Core 必须立即 append `process-started`，其规范载荷至少包含：

- `schemaVersion`、`authorityNamespaceId`、`taskId`、`runId`、`attemptId`、`allocationId`；
- `leaseId`、`leaseDigest`、dispatch generation 与 fencing token 的摘要（不得持久化原始 token）；
- `commandId`、根 `pid`、根 `pgid`，且根进程必须满足 `pid == pgid`；
- 从 Darwin 内核进程查询得到的 birth identity（至少包含内核报告的启动秒与微秒，或具有同等抗 PID reuse 能力的稳定值），不得用 PID、argv 或用户态 wall clock 单独代替；
- canonical working directory 与启动前持有的 nofollow directory FD identity；identity 至少绑定 device、inode、file type、owner、mode，并绑定精确路径重验结果；
- canonical executable path 与启动前持有的 nofollow executable FD identity；identity 至少绑定 device、inode、size、file type、owner、mode、link count 与原始文件 bytes 的 SHA-256；
- observer identity、观察时间与全部字段的 canonical digest。

同一 live Core 还必须持有可向该精确进程发信号/等待的 process handle、根进程组与 cwd/executable 的 held FD，直到完成 wait 或终结事务。handle/FD 不可持久化；Core 重启后只能以 `process-started` 的 birth/PGID/path/object identity 配合新的内核观察恢复。held executable FD 只证明启动对象，不得冒充对任意时刻进程映像的 hardened attestation。

如果启动后无法写入合法 `process-started`，仍存活的父 Core 必须用其刚获得的 handle 终止并 wait；若 Core 在“进程已启动、事实尚未提交”的窗口崩溃，恢复方没有权凭扫描或 PID 猜测 kill，只能 fence 该 Attempt/worktree、禁止 successor，并写入人工介入 Outcome。该 fail-closed 窗口必须有故障注入测试，不得用临时内存记录掩盖。

### 3. 进程树归属与跨编排保护

1. 任何 signal/terminate 前都必须重新验证根 PID、birth identity、PGID leader、allocation/Attempt/lease generation、cwd 与 executable object identity。任一字段未知、变化或冲突，结论固定为 `process-identity-conflict`，**零 kill 副作用**。
2. 可以终止的集合只包含由精确 root birth identity 可归属的进程树。Darwin ordinary-user 实现必须有界地执行“停止 root → 枚举并固定可归属 descendants → TERM → 继续/等待 → 必要时 KILL → 再次内核观察”；重放每一步前都重新验证 identity。
3. descendant 已 reparent、另建 session、归属不明或无法枚举到固定点时，不得扩大为全机 `ps` 扫描、argv 匹配、PID/PGID 猜测或跨 namespace kill。必须记录冲突并 fence；不得解锁 worktree 或启动 successor。
4. 只有记录中的当前 authority tuple 与当前合法 orchestrator 完全匹配，才允许发信号。另一个 Run、Attempt、allocation、lease generation、authority namespace 或 orchestrator 的进程，即使路径/argv 相同也不得触碰。

### 4. Attempt 终结事务

每个启动过进程的 Attempt 只有以下单调事实链：

```text
process-started
  → termination-intent
  ├→ process-absent | process-terminated
  │   → allocation-terminated
  │   → lease-released
  └→ process-identity-conflict
      → intervention（禁止 allocation terminal/release/unlock/successor）
```

其中：

1. 每个事实都绑定 `terminalizationId`、前一事实 digest、完整 authority/Attempt/allocation/lease/generation tuple、预期 authority sequence 与自身 canonical digest。同 ID 同内容重放幂等；同 ID 不同内容、不同终点或前序不连续一律 fail closed。
2. `termination-intent` 一经持久化，该 Attempt 进入 cleanup-only：禁止新 `Exec`/`Stage`/checkpoint、禁止接纳新的业务结果、禁止产生 successor；只允许当前 Core cleanup operation 执行 `Inspect`/`Reconcile`/`Signal`/`Terminate`。
3. `process-absent` 表示内核观察证明精确 root birth identity 已不存在，且没有仍可归属的进程树；`process-terminated` 表示精确进程树已被信号终止，并再次观察为 absent。两者是允许后续 Provider allocation `Terminate` 的安全结论。
4. `process-identity-conflict` 是已确定的终点，但不是“安全清理完成”：未知不 kill，不得写 `allocation-terminated` 或 `lease-released`，不得解锁或启动 successor；Core 必须写入带原因码的 intervention Outcome。
5. `allocation-terminated` 绑定 Provider `Terminate` receipt 与前一进程终点。Provider lost response 只能经 `Inspect`/`Reconcile` 证明同一 allocation 已终结后幂等补记，不能由内存删除推断。

### 5. DispatchLease 的正常终态事实

1. DispatchLease 增加 append-only 的 `lease-released` 事实和 `released` 终态；它必须绑定 allocation terminal fact、Attempt、generation、前一 lease fact digest 与 `terminalizationId`，并推进 generation/使旧 fencing capability 永久失效。
2. 正常释放原因采用独立封闭枚举：`attempt-completed`、`attempt-failed`、`attempt-aborted`、`orphan-reconciled`。不得借用安全撤销的 `CancelReason` 伪装正常完成。
3. `lease-released` 只能在 `process-absent|process-terminated`、`allocation-terminated` 均已耐久提交后写入。现有 `expired|cancelled` 继续表示异常 lease 生命周期，但异常路径同样必须完成或阻断本文的安全清理，不能把 lease 终态当作进程已退出。
4. 旧 ledger 可按原语义重放，禁止重写历史；新字段/状态未知、缺少绑定或发生终态冲突时 fail closed。

### 6. ResultIngress-first、cleanup-before-unlock/successor 的唯一恢复顺序

对完成、失败、abort、timeout、Core crash、Provider lost response 与 orphan recovery，唯一顺序为：

1. 先取得当前 Run authority，并从**耐久 current ledger**复核精确 Attempt/allocation/lease generation；不是当前 owner 或 authority 不确定时零副作用；
2. **先 reconcile ResultIngress**：若 admission transaction 已提交，固定重放相同业务成功结论；若未提交，不得从 transcript、文件或 Worker 声明推断成功；若提交状态冲突/未知，进入 intervention；
3. 恢复或追加 `termination-intent`，按第 3/4 节 Inspect/Reconcile/Terminate，写入安全的进程终点、Provider allocation terminal receipt 和 `lease-released`；
4. 只有完成第 3 步后，才允许解锁 task worktree：已提交结果重放 `worker.completed`/后续 verification；无已提交结果才由 ADR 0053 的 generic recovery 决定失败、retry 或新 Attempt；
5. `process-identity-conflict`、ResultIngress conflict 或任一未知状态均禁止 unlock/successor。`termination-intent` 后到达的新结果进入 quarantine；只有 intent 前已经提交的 admission 可以幂等重放。

因此 ResultIngress 决定“业务结果是否存在”，进程终结事务决定“旧执行是否安全退出”；前者先判定，后者完成前不能释放资源和启动 successor。两者不得各自生成不同恢复结论。

### 7. 崩溃、ABA、伪造与重放负向矩阵

| 场景 | 唯一结论 / 必须验证的性质 |
| --- | --- |
| Start 后、`process-started` 前崩溃 | 无权 PID 扫描或 kill；fence Attempt/worktree、禁止 successor、intervention |
| `process-started` 后、`termination-intent` 前崩溃 | 从同一耐久观察恢复；ResultIngress-first 后追加 intent |
| intent 后、signal 前崩溃 | cleanup-only 幂等恢复，先重验 identity |
| STOP/TERM/KILL 中途崩溃 | 每一步重新观察；只对同一 birth/tree 继续，不盲目重复 signal |
| kill 后、进程终点事实前崩溃 | 观察 absence 后以同一 `terminalizationId` 补记，不能推测 |
| 进程终点后、Provider `Terminate` 前崩溃 | 幂等重放 `Terminate` 并绑定 receipt |
| allocation terminal 后、`lease-released` 前崩溃 | 重验 receipt/absence 后补记 release |
| release 后、业务 journal 决策前崩溃 | 由 ResultIngress 已提交事实重放同一业务结论 |
| PID reuse；同 PID 不同 birth | `process-identity-conflict`，零 kill、零 release |
| PGID reuse；leader birth 不同或 root 非 group leader | conflict，零 kill |
| executable 同路径但 device/inode/digest 变化、swap-back；cwd rename/symlink/FD identity 不符 | conflict，零 kill |
| descendant reparent/session escape/归属不明 | conflict，零 kill、零 successor；普通用户不宣称不可逃逸 |
| Provider 伪造 claim/fact/termination；observer 自证；tuple/digest/sequence 错 | 拒绝，Provider 声明只作诊断 |
| stale generation/token、已 release lease、同 terminalizationId 不同 digest | 拒绝且零副作用 |
| intent 后 late result | 未预先提交则 quarantine；不得复活 Attempt |
| 第二 orchestrator、PID-only、argv-only、全机 `ps` 匹配 kill | 机械拒绝，零副作用 |
| Core 重启后 `LocalRunner` map 为空 | 从 authority facts 恢复 projection；不得把空 map 解释为无 allocation/无进程 |

## 后果与实施门禁

- 这是 R3–R5 的 release-critical 合同补齐，不新增服务、队列、通用 workflow、远程 scheduler 或第二状态库，也不扩大 [runtime architecture](../runtime-architecture.md) 的终态职责图。
- 实现必须沿现有 composition root 接线：Core observer/authority store → Local allocation/Exec → sandbox bridge → `execution.Service` recovery；禁止先落孤立 package 再声称集成。
- 最小实现必须同时覆盖：耐久进程观察/重放、Core-held live handle、Darwin birth/FD identity、Provider terminal receipt、`lease-released`、ResultIngress-first 终结顺序，以及本 ADR 全部负向矩阵。
- 单元/集成测试至少包含 crash point table、PID/PGID/process-reuse ABA、cwd/executable swap、伪造 Provider fact、跨 orchestrator kill 拒绝、late result、lost response 与两次重启重放；真实 Darwin 测试必须使用固定已允许的 `marshal` 二进制，不生成匿名临时 executable。
- 实现完成前，`LocalRunner` 纯内存 allocation/process projection、正常 lease release 和 cleanup-before-successor 都是开放缺口；I186-R3–R5 继续为 `IN_PROGRESS/COMPONENT`。
