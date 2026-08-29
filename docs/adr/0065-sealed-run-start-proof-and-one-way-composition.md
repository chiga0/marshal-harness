# ADR 0065：密封 Run-start proof 与单向生产组合

- 状态：提议（Proposed，2026-08-29）。本文档基于 `main@40fa493d1955fd6d039169483a6501a787d3cc14`；只提出合同，不表示实现、集成或发布完成，不升级 I186-R2–R6。
- 关联：[ADR 0052](0052-v1-release-scope-and-production-reachability.md)（v1.0 生产可达性）、[ADR 0056](0056-darwin-process-observation-and-attempt-terminalization.md)（Attempt terminalization）、[ADR 0057](0057-durable-local-allocation-recovery-and-production-composition.md)（Run/Allocation authority 与唯一 composition root）、[ADR 0060](0060-supervisor-mechanics-authority-binding-and-recovery.md)（Supervisor mechanics 子链）、[ADR 0063](0063-prepared-execution-authority-and-production-chain.md)（PreparedExecution 与 Run-start）、[ADR 0064](0064-darwin-control-directory-phased-identity.md)（Darwin 控制目录身份）、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)。

## 背景

ADR 0063 正确冻结了两个业务条件：只有 current Attempt authority 中 exact successful `resume(disposition=ok, reason=process-resumed, state=running)` outcome 才能授权 Run 从 `READY` 进入 `RUNNING`；Run journal 必须以 creation-once 方式提交唯一 successor。它尚未充分冻结跨 `resultingress` 与 `runstore` 的能力传递边界。

若把 `CurrentRunAuthority`、owner epoch、dispatch generation、raw source closure，或可在 callback 返回后继续执行的函数值交给通用调用者，会出现以下问题：

1. callback/closure 可以被复制、保存、放入 goroutine 或延迟调用；清空原变量不能撤销副本；
2. ResultIngress 与 runstore 都可能读取或镜像 owner/Attempt/generation，形成两个“当前”判定者；
3. runstore 若为验证 `process-started`/`resume` 再回读 ResultIngress，或 ResultIngress 为确认 Run commit 再回读 runstore，会形成反向调用、锁序反转与 response-loss 双账本猜测；
4. 通用 `Store.Append` 若仍可写入 `READY → RUNNING`，专用门禁只是可选路径；
5. 仅靠接口单测不能证明真实 controller/composition 没有缓存、二次传递或绕过 projector。

这些问题不要求建立第三个 ledger 或跨存储事务。需要的是：ResultIngress 在自己的 current-ledger borrow 内完成唯一前后重验并签发短生命周期 proof；runstore 只在自己已经持有的 Run authority 下消费 proof，并只验证自己的 lease/head/state。proof 传递“本次调用现在可以提交”的能力，claim 只传递不可授权的事实引用。

## 决策

### 1. 适用范围与取代关系

1. 本 ADR 仅覆盖 v1 Mac-first `darwin-local-dogfood` 的 `PreparedExecutionV1` 成功启动后，将 exact ResultIngress mechanics outcome 投影为唯一 `READY → RUNNING` Run journal successor 的边界。
2. 本提案在被接受后，部分取代 ADR 0063 §5 中由通用 `WithCurrentRunAuthority` callback 暴露 authority 的接口形状、§6 中由调用者提交两个 digest 的 `CommitRunStartOutcome` 形状，以及 §7 中对应的跨包 producer chain。ADR 0063 对 `Pi0843IdentityV1`、secret-safe `PreparedExecutionV1`、held source originals、双 barrier、exact successful resume 与 raw path/argv/environment 禁止复制的其余规则保持有效。
3. 本 ADR 不实现 ADR 0056 terminalization、cleanup、successor Attempt、通用 recovery、Linux/hardened authority、server transport 或发布。terminalization 必须作为本 ADR 两个切片完成后的独立后续切片，不得混入 proof/composition 返工。
4. 不新增第二 authority store，不要求 ResultIngress 与 Run journal 跨文件原子提交。两者通过 active proof 在线性化窗口内衔接，response-loss 各自只查询自己的 ledger/journal。

### 2. 职责绝对分离

ResultIngress 是以下事实的唯一读取、解释与重验者：

- current global owner 与 exact Attempt owner binding；
- exact Attempt identity、dispatch generation、fencing 与 current business head；
- `PreparedExecutionV1` 所引用的 Allocation/launch/closure 原件；
- `process-started` 与 authenticated successful `resume` outcome 的同 Attempt/child/session 关系；
- mechanics outcome 已 fsync 后、投影前的最终 owner/Attempt/generation recheck。

只有 ResultIngress 可以在上述最终 recheck 成功后 mint `CommittedRunStartProof`。它不得把 owner epoch、dispatch generation、fencing token/digest、Attempt head、Supervisor handle 或 raw source 放入 claim，也不得要求 runstore 复核这些 ResultIngress facts。

runstore 是以下事实的唯一读取、解释与变更者：

- exact held Run `Lease` 与 descriptor-bound journal；
- current Run 的 Task/Run/Attempt、`READY` state、sequence 与 authority head；
- `preparationDigest` 是否与该 current Run-start preparation 相等；
- `run-start-outcome` 是否已存在，以及唯一 successor 的 append/fsync/replay。

runstore 不得读取 ResultIngress ledger，不得保存 owner epoch/generation 的镜像，不得解析 `process-started` 或 `resume` payload，也不得把 ResultIngress fact 解释为自己的 current Run head。proof 只授权一次 self-only CAS；Run 是否仍可提交由 runstore 自己决定。

### 3. ResultIngress 的封闭、可编译 API

`internal/resultingress` 必须提供以下等价 Go API；字段、参数顺序和导出面是本合同的一部分：

```go
type CommittedRunStartClaim struct {
	TaskID                      string
	RunID                       string
	AttemptID                   string
	PreparationDigest           string
	ProcessStartedFactDigest    string
	ResumeOutcomeFactDigest     string
}

type CommittedRunStartProof struct {
	guard *committedRunStartGuard
}

func (proof CommittedRunStartProof) WithClaim(
	fn func(CommittedRunStartClaim) error,
) error

type RunStartProjector interface {
	ProjectCommittedRunStart(context.Context, CommittedRunStartProof) error
}

func (s *DurableStore) StartPreparedExecution(
	ctx context.Context,
	ownerVerifier CurrentOwnerLockVerifier,
	acquisition ControlOwnerAcquisition,
	identity AttemptIdentity,
	preparationDigest string,
	projector RunStartProjector,
) error
```

冻结规则如下：

1. `CommittedRunStartClaim` 是 non-authority value。它不得增加 `OwnerEpoch`、`Generation`、`DispatchGeneration`、`FencingTokenDigest`、Run sequence/head、successor state/sequence/head、lease、handle、path、argv、environment 或 raw outcome 字段。claim 即使被复制或持久化，也不能单独授权 mutation。
2. `CommittedRunStartProof` 的零值无效。`guard` 必须是私有、共享引用状态；proof 的所有值副本共享同一个 `active/claimed` 状态，不能因复制获得第二次消费。
3. `WithClaim` 仅在 proof active 且尚未消费时同步调用 `fn` 恰好一次。重复、并发、零 callback、已失活、callback 返回后再用或 guard 不完整均 fail closed。它不返回 claim，不暴露 guard，也不提供序列化、克隆、续期或转授 API。
4. `StartPreparedExecution` 的 `projector` 必须是最后一个参数。该方法在 ResultIngress current-ledger borrow 内完成 supervisor mechanics、outcome fsync、最终 recheck，随后 mint proof 并同步调用 `ProjectCommittedRunStart` 恰好一次；projector 返回后无条件 deactivate shared guard，再退出 ResultIngress borrow。
5. projector 返回错误时不得再次调用 projector、再次 mint proof 或重发 supervisor command。ResultIngress 保留自己已 fsync 的 facts；Run 是否已提交由下一次 runstore 本地 replay 决定。
6. `RunStartProjector` 只允许 `runstore` 的私有 borrowed projector 实现进入 production composition。Fake 可用于包内 hostile test，但不得注册为 production provider 或持久化。

### 4. runstore 的专用 authority API

`internal/runstore` 必须提供一个且仅一个导出的 Run-start authority seam：

```go
func (s *Store) WithPreparedRunStartAuthority(
	ctx context.Context,
	lease *Lease,
	prepared application.PreparedRunStart,
	fn func(resultingress.RunStartProjector) error,
) (application.RunProjection, error)
```

该方法：

1. 先持有并验证 exact `lease`，从 descriptor-bound journal 读取自己的 current Run，证明 Task/Run/Attempt、`READY` state、sequence/head 与 public `PreparedRunStart` 精确相等；
2. 若自己 journal 已存在 exact `run-start-outcome(preparationDigest)`，只从本地 journal 返回同一 `RunProjection`，不得调用 `fn` 或查询 ResultIngress；
3. 若尚未提交，构造包内私有 `borrowedRunStartProjector`，同步调用 `fn(projector)` 恰好一次，并在返回后失活 projector；
4. projector 的 `ProjectCommittedRunStart` 只能调用一次 `proof.WithClaim`；在该 callback 内调用包内私有 `appendPreparedRunStartClaim`，完成 self-only pre-CAS、append/fsync 与 post-CAS re-read；
5. `appendPreparedRunStartClaim` 只校验 claim 的 Task/Run/Attempt/preparation 引用与自己已持有的 Run 请求相符，并把两个 opaque fact digest 记录为 provenance；它不得解析或回查两个 fact，不得接收 authority namespace、owner/generation，也不得调用 ResultIngress、Supervisor、Provider 或外部 hook；
6. exact replay 零追加；same preparation 不同 fact pair、同 proof 二次消费、Run head/state/lease 漂移、不同 Attempt 或并发 winner 均 conflict。只有 append 已 fsync 且 post-CAS 重读得到 exact `RUNNING` successor 才返回成功 projection。

依赖方向固定为：

```text
internal/runstore → internal/resultingress
internal/productionruntime → internal/runstore + internal/resultingress
```

`internal/resultingress` 不得 import `internal/runstore`；两包不得经第三个“共享 authority”包形成隐式反向依赖。`CommittedRunStartClaim` 可以作为窄 DTO 被 runstore 读取，但它不属于 runstore 的持久真值。

### 5. 固定锁序与唯一线性化窗口

生产启动的锁序固定为：

```text
repository owner
  → runstore outer borrow
  → ResultIngress current-ledger borrow
  → Supervisor mechanics
  → exact outcome append + fsync
  → ResultIngress final owner/Attempt/generation recheck + mint proof
  → runstore self-only pre-CAS → append/fsync → post-CAS
  → deactivate proof
  → release ResultIngress borrow
  → release runstore outer borrow
  → release repository owner
```

约束：

1. `ownerVerifier` 必须是 repository owner 临界区内创建并同步借出的 verifier；其 `WithCurrentOwnerLock` 只证明外层 owner 仍 active，不得重新获取 repository owner lock。runstore outer borrow 在进入 ResultIngress 前也已持有；ResultIngress 调用 projector 时，runstore 只能使用已借用的 descriptor/lease，不能重新 `Acquire`、重新打开 Run authority或等待第二把 runstore 锁。
2. projector 是“向已借用 runstore 提交”的单次 continuation，不是反向 authority callback。`appendPreparedRunStartClaim` 期间禁止调用 ResultIngress、owner verifier、Supervisor、Provider 或用户 callback。
3. 不允许先释放任一 borrow 再用内存 claim/closure 提交；不允许 handoff gap、锁序反转、reacquire、递归 Start、goroutine/defer mutation 或 callback reentry。
4. Supervisor mechanics 发生在 ResultIngress borrow 内；runstore mutation 只发生在 exact successful outcome fsync 和最终 ResultIngress recheck 之后。runstore 提交失败不能倒推删除 ResultIngress facts，ResultIngress 失败不能写 Run successor。
5. context cancel/deadline 只能在上述边界产生 typed 失败；不能以 cancel 为理由缩短 fsync、跳过最终 recheck 或在 proof 失活后补提交。

### 6. response-loss 只查自己的账本

| 丢失边界 | ResultIngress 唯一动作 | runstore 唯一动作 |
| --- | --- | --- |
| mechanics intent/outcome 前 | 按 ADR 0060 从自己的 Attempt ledger 决定 replay/intervention | journal 无 successor；不得猜测 mechanics |
| outcome 已 fsync、proof 尚未 mint | 从自己的 current ledger 重放 outcome，完成新鲜最终 recheck 后可重新 mint | journal 无 successor；下次仍进入受控组合 |
| proof 已交付、Run append 前 | 不查询 Run journal、不把 proof 状态持久化 | journal 无 successor；本次/下次只在 active proof 内尝试 self-only CAS |
| Run append/fsync 后 response 丢失 | 不查询或镜像 Run successor | 仅从自己的 journal replay 同一 `RunProjection`，不再次调用 ResultIngress |
| same key different bytes / 任一 ledger 不一致 | 保留自己的 conflict/intervention evidence | 保留自己的 conflict；不得用另一 ledger 覆盖自己 |

禁止建立 cross-ledger recovery scanner、两边互抄 owner/generation/head、基于“另一边应该成功”合成记录，或把一边的缺失解释为另一边可以回滚。最终收敛依赖幂等重试经过相同 composition 顺序，而不是跨文件事务或后台猜测。

### 7. production architecture gate

真实组合只能位于：

```text
internal/productionruntime/prepared_run_start_composition.go
```

必须以 `go/ast` + `go/types` 的 repository architecture test 机械证明：

1. 该文件中 `(*runstore.Store).WithPreparedRunStartAuthority` 恰有一个 typed `CallExpr`，`(*resultingress.DurableStore).StartPreparedExecution` 也恰有一个 typed `CallExpr`；其它 production 文件零调用。
2. `WithPreparedRunStartAuthority` 的 callback 参数必须是调用点直接出现的 `FuncLit`。该 `FuncLit` 内只调用一次 `StartPreparedExecution`；borrowed `projector` 只作为该调用的最后一个实参出现一次。
3. projector 不得赋给字段、容器、全局或 interface 临时存储，不得 return，不得进入 `go`/`defer`，不得传给第二个函数、方法或 closure，不得进行类型断言后再次传递。
4. `controller.startPreparedRun` 只能调用同包私有 `composePreparedRunStart` helper；controller、`Runtime`、CLI/server、ProcessBridge 与测试外 production 代码不得直接调用两个 exported seam。
5. `internal/runstore/prepared_run_start_authority.go` 是 runstore 唯一实现文件；architecture test 对四个 selector/primitive 建立唯一性：`WithPreparedRunStartAuthority` 只有上述一个 production callsite，`borrowedRunStartProjector.ProjectCommittedRunStart` 只有一个实现，`proof.WithClaim` 只有一个 callsite，私有 `appendPreparedRunStartClaim` 只有一个 callsite。
6. `internal/resultingress` 的 proof constructor、activate/deactivate/claim guard 必须保持 private；production import graph 只有第 4 节的单向窄依赖，不得通过 type alias、wrapper、reflection helper 或 build tag 旁路。

architecture gate 必须扫描所有 production Go 文件和 build tag 组合；仅对测试文件作字符串计数不满足门禁。任一新增 callsite、间接 wrapper、无法解析类型或目标文件缺失固定 fail closed。

### 8. generic `Append` 生产旁路必须关闭

1. `runstore.Store.Append` 对任何 `current=READY` 且 `event.StateTo=RUNNING` 的调用固定返回 typed conflict，无论 event type、payload、调用者或 expected sequence 是否合法。
2. `READY → RUNNING` 只能由 `appendPreparedRunStartClaim` 写入封闭 `run-start-outcome`。该 primitive 仍复用既有 journal framing、transition validator、fsync 与 snapshot replay，不建立第二 writer。
3. import/repair/rebuild、测试 helper、legacy CLI 与 server 不得获得例外。历史合法 journal 保持可读；本门禁只禁止新 production bypass，不重写历史。
4. architecture test 必须证明 private primitive 是新 `RUNNING` event 的唯一 producer，generic `Append` hostile test 必须覆盖伪造 event type、payload 复制、合法 sequence/head 与 direct store 调用。

### 9. 最小 hostile、crash 与 replay 矩阵

实现至少一次覆盖：

- proof 零值、伪造、复制、双消费、并发消费、callback zero/double call、callback 后使用、projector 保存/return/go/defer/reentry；
- claim 字段集反射检查：无 owner epoch/generation/fencing/Run head/successor/raw source；claim 被持久化或跨 goroutine 复制仍不能授权 append；
- owner 在 mechanics 前、outcome fsync 前与 final recheck 前分别 ABA；Attempt head、dispatch generation、fencing、process/resume child/session 任一漂移均在 mint 前拒绝；
- Run lease/head/state/Task/Run/Attempt/preparation 在 outer borrow 前后、proof 前后漂移；并发 start 只有一个 journal winner；
- Supervisor intent、outcome fsync、proof mint、projector entry、Run append、Run fsync、post-CAS 与 response 各边界 crash/lost response；重复调用不重复 launch/resume 或 Run append；
- ResultIngress ledger 存在而 Run journal 缺失、Run journal 已提交而 ResultIngress 调用方未收到 response，两边只查自身记录仍收敛；
- runstore 注入 ResultIngress reader、ResultIngress 注入 runstore reader、reverse callback、reacquire 与锁序反转必须被类型/architecture gate 拒绝；
- generic `Append READY→RUNNING`、CLI/server direct append、controller direct exported seam、第二 composition 文件、projector 二次传递全部失败；
- Run journal/event/error/log/ReviewPacket 对 owner generation、raw path/argv/environment/stdin/nonce/credential/handle 做 secret 与边界扫描，除各自既有唯一 authority 原件外零新增匹配；
- `go test -race` 覆盖 shared guard 并发、proof deactivate 与 projector callback；死锁测试证明固定锁序下完成，反序 fixture 在外部副作用前失败。

### 10. 两个且仅两个实现切片

本 ADR 若被接受，实施只允许以下顺序：

1. **S1：sealed proof component**。在 `internal/resultingress` 实现 claim/proof/shared guard 与 `StartPreparedExecution`，在 `internal/runstore` 实现 `WithPreparedRunStartAuthority`、私有 borrowed projector、专用 Run-start primitive 和 generic `Append` 拒绝；完成包级 hostile/crash/replay/race 与单向依赖检查。S1 不修改 production controller，不声称 production reachable。
2. **S2：fixed composition**。只新增 `internal/productionruntime/prepared_run_start_composition.go`、私有 composition helper 与 architecture gate，把 S1 接到 fixed `marshal` / `marshal control-plane serve` 现有 controller；完成一个真实 Pi prepared-start 纵切，证明 exact resume 后唯一 `RUNNING` projection 与 response-loss 重放。S2 不得顺带实现 terminalization、Provider 扩面、release 或第二 component。

ADR 0056 的 terminalization/eligibility/cleanup producer chain 是 S2 之后的独立切片。不得为了“同一纵切”把它塞入 S1/S2，也不得在 S1 与 S2 之间插入 terminalization、Harness 微修或无关 Provider 工作。S1/S2 任一未完成时，R2–R5 继续保持 `COMPONENT`；只有真实 composition 与对应退出门禁通过后才可按 ADR 0052 升级成熟度。

## 后果

该设计把跨 ledger 的授权缩成一个不可序列化、共享失活状态的一次性 proof：ResultIngress 负责证明“这个 exact Attempt 的成功 resume 现在仍有效”，runstore 负责决定“这个 exact Run 现在仍可从 READY 提交”。两边不共享 current facts、不互相读取恢复状态，也不需要新的分布式事务。

代价是 production composition 必须长期维持严格 AST 形状，并在 Run outer borrow 内持有 ResultIngress borrow 直至 Run commit 完成。v1 的单节点、单用户边界使该固定锁序可接受；若未来需要跨节点或高并发拆分，必须先以新 ADR 定义可替代 proof 生命周期、跨故障域 commit 与 deadlock 模型，不能把本地 claim 升级为 bearer token。
