# ADR 0063：PreparedExecution 启动权威合同与唯一生产者链

- 状态：提议（Proposed，2026-08-29）。本 ADR 只冻结 ProcessBridge 前后的最小耐久合同；不表示实现已完成，不升级 I186-R2–R6，也不授权真实进程启动或发布。
- 关联：[ADR 0057](0057-durable-local-allocation-recovery-and-production-composition.md)（唯一 `ProductionRuntime` 与 Allocation receipt）、[ADR 0058](0058-interpreted-agent-launch-identity.md)（`launch-authorized` 与 `StoredClosureV1`）、[ADR 0059](0059-fixed-darwin-process-supervisor.md)（固定 Supervisor）、[ADR 0060](0060-supervisor-mechanics-authority-binding-and-recovery.md)（Supervisor mechanics 子链）、[ADR 0062](0062-fixed-marshal-production-server-mode.md)（固定 Marshal server mode）、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)。

## 背景

当前 `PublicApplicationPort` 已把 `PrepareRunStart` 与 `StartPreparedRun` 分开，但 `application.PreparedRunStart` 只携带 Task/Run/Attempt、Run sequence/head 与 `preparationDigest`。`productionruntime.ProcessBridge` 因而不能从权威存储唯一恢复完整 `AttemptIdentity`、current `RunAuthorityBinding`、current owner binding、Allocation provision receipt、精确 `launch-authorized` fact、`StoredClosureV1` 与 Pi `0.84.3` identity。

同时，现有 `DurableRunAuthority` 没有显式 `WithCurrentRunAuthority`、`ResolvePreparedExecution` 与 `CommitRunStartOutcome`。bridge 返回后由 controller 猜测 outcome 会形成 check-then-act；把 `process-started` 当成功点又会在 child 仍为 `exec-stopped`、`resume` 尚未得到 exact successful outcome 时错误提交 `RUNNING`。ADR 0057–0062 已冻结事实来源和 Supervisor mechanics，但尚未冻结这段 producer chain、creation-once 记录、Pi identity canonical preimage 与 resume response-loss 语义。它们改变持久化合同和 Run 生命周期提交点，必须先由 ADR 冻结。

## 决策

### 1. 范围与取代关系

1. 本合同只覆盖 v1 Mac-first `darwin-local-dogfood` 中从 `READY` Run 准备真实 Pi `0.84.3` Attempt，到 exact successful `resume` outcome 后提交唯一 `RUNNING` Run outcome 的接缝。它不定义 terminalization、通用 restart/fence/new Attempt 策略、Supervisor crash 处置、Linux/hardened authority 或发布策略；这些继续由 ADR 0056、0059–0062、ADR 0047/0048 与既有恢复/发布合同约束。
2. 本 ADR 补充 ADR 0057 §7，并在 `launch-authorized → process-supervisor-bootstrap-prepared` 之间增加不推进 Attempt head 的 creation-once `PreparedExecutionV1` Run authority fact。ADR 0058 的启动双 barrier和 ADR 0060 的 bootstrap/command 子链仍然有效。
3. `PreparedExecutionV1` 不是 bearer capability、第二份 Run/Attempt 真值或 API Client 可构造的执行计划。它是 Core 从既有 current durable facts 产生的 closed/versioned、**secret-safe** 索引。包含 path、argv、environment 与完整 object identity 的唯一原件仍是 held Attempt authority 内的 `launch-authorized`/`StoredClosureV1`；不得复制进 Run authority、Supervisor mechanics 子链、公共 DTO、事件、错误或日志。

### 2. `Pi0843IdentityV1` 的 closed canonical preimage

新增 path-free、secret-safe 的 closed `Pi0843IdentityV1`，字段与顺序固定为：

```text
schemaVersion="Pi0843IdentityV1",
protocolRevision="pi-0843-identity/v1",
agentProvider="pi",
agentVersion="0.84.3",
closureProfileId="pi/0.84.3/darwin-arm64/v1",
nodeRuntimeObjectDigest,
entrypointMaterialDigest,
materialRootsDigest,
launchMaterialsDigest,
identityDigest
```

canonical 计算固定为：

```text
nodeRuntimeObjectDigest = sha256(JCS(exact RuntimeExecutableV1))
entrypointMaterialDigest = sha256(JCS(exact LaunchMaterialV1(role="pi-bundle/cli.js")))
materialRootsDigest = sha256(JCS(exact MaterialRootV1[] sorted by name UTF-8 bytes))
identityDigest = sha256(JCS(Pi0843IdentityV1 without identityDigest))
```

`launchMaterialsDigest` 使用 ADR 0058/`launchidentity` 已冻结的算法；entrypoint 必须在同一 sorted material manifest 中恰好出现一次。`agentLaunchSpecDigest` 绑定每个 Run 的 argv/environment/cwd，**不得**进入静态 `Pi0843IdentityV1` preimage 或 `identityDigest`；它只在 source closure 与第 3 节 `PreparedExecutionV1` 中单独闭合。任何未知字段、空集合替代、排序差异、digest-only echo、PATH/版本输出或 Adapter 自报都不能产生该 identity。composition root 配置的 `PiProfile.IdentityDigest()` 必须与从 held Node、entrypoint、两个 roots 和全部 55 个 materials 重算出的 `Pi0843IdentityV1.identityDigest` **逐字节精确相等**，否则 profile 在任何 authority mutation 或外部副作用前 `typed-unavailable`。

### 3. secret-safe `PreparedExecutionV1`

字段固定为：

```text
schemaVersion="PreparedExecutionV1", protocolRevision="prepared-execution/v1",
AttemptIdentity,
RunAuthorityBinding,
expectedRunSequence, expectedRunAuthorityHead,
CurrentOwnerBinding, controlOwnerBoundFactDigest,
attemptAuthorityHeadAtPreparation,
allocationProvisionReceiptFactDigest, allocationProvisionReceiptDigest,
launchAuthorizationId, launchAuthorizedFactDigest,
storedClosureDigest,
launchMaterialsDigest, agentLaunchSpecDigest,
pi0843IdentityDigest,
preparationDigest
```

冻结规则：

1. `AttemptIdentity` 必须保存完整 authority namespace/ref、Task/Run/Attempt、Allocation/Lease、dispatch generation、fencing token digest、orchestrator 与 Run authority digest；`RunAuthorityBinding` 必须精确由它派生。`expectedRunSequence/head` 必须是 held current Run authority 下复核的 `READY` head。
2. current global owner 必须先经 `BindOwnerToAttempt` 把 exact `CurrentOwnerBinding` append 到该 Attempt，随后才允许 allocation launch preparation、`launch-authorized` 或 prepared creation。`controlOwnerBoundFactDigest` 必须是 `launch-authorized` 的同链 ancestor，owner scope/epoch/acquired fact 均须 current；`launch-authorized` 在首次 prepared creation 时必须是 exact current Attempt head，因而不存在“owner bind 与 launch head 同时 current”的双头假设。
3. `allocationProvisionReceiptFactDigest` 与 `allocationProvisionReceiptDigest` 只引用 ADR 0057 allocation authority 中已 fsync 且 reconcile 为 current/applied 的完整 receipt；`launchAuthorizedFactDigest`、`storedClosureDigest` 与两个 launch digest 只引用同一 Attempt authority 中的 exact `launch-authorized` 原件。resolver 必须在同一 held current-authority callback 内从 allocation authority 读取完整 receipt、从 Attempt authority 读取完整 `StoredClosureV1`，再逐项闭合 Attempt/Allocation/generation/request/intent/prepared/receipt/launch；调用者不得提交另一份 closure。
4. `storedClosureDigest = sha256(JCS(exact StoredClosureV1))`；`pi0843IdentityDigest` 必须等于第 2 节从该 closure 所引用 held objects 重算的 identity。`PreparedExecutionV1` 不含 `AllocationProvisionReceiptV1`、`StoredClosureV1`、`Pi0843IdentityV1` 的完整副本，也不含 raw path/argv/environment。
5. `preparationDigest = sha256(JCS(preparedWithoutPreparationDigest))`。缺字段、未知字段、非 canonical、跨 Attempt/Allocation/generation、owner/receipt/launch/closure/Pi identity 不闭合或 digest 不可重算均 fail closed。

### 4. creation-once 与 owner epoch

1. 每个 exact `(authorityNamespaceId, runId, attemptId, launchAuthorizedFactDigest, controlOwnerBoundFactDigest)` 最多存在一个 `PreparedExecutionV1`。它写入现有 durable Run authority 的 append-only creation-once 子链；可物理复用同一 authority 文件，但不得创建第二 store，也不推进 Attempt revision/head。
2. 生产者只能在 owner lock 与 `WithCurrentRunAuthority` 的同一个 borrowed callback 内，从 current durable ledger 读取并验证第 3 节全部原件，再 compare-and-append、fsync。禁止先返回内存 plan 后补记；exact key + exact bytes 重放零追加、零 Probe/Provision/Supervisor/child 副作用，same key different bytes 固定 conflict。
3. prepared fsync 前崩溃只能从相同 current facts 重新创建；fsync 后 response 丢失只能 resolve 已有对象。历史 Run 不补写；没有 prepared fact 的新 Run 禁止进入 ProcessBridge。
4. prepared 创建后 owner epoch 变化会使它失去 fresh-start 资格。若 Supervisor mechanics session 已存在，只能按 ADR 0060 先 `BindOwnerToAttempt`，再持久化 authenticated `process-supervisor-session-reconnected` 重锚同一 session；pending intent、identity/head 不能唯一闭合时进入 intervention。若 epoch 在 bootstrap 前变化，因为尚无 session 可重连，固定进入 intervention，不创建第二份 prepared、不重新追加 `launch-authorized`、不启动 Supervisor。该窄规则只确定当前切片的 fail-closed 分支，不扩展通用恢复策略。
5. `PrepareRunStart` 创建 prepared 时不得启动 Supervisor/child。`StartPreparedRun` 必须先按 ADR 0060 creation-once append exact `process-supervisor-bootstrap-prepared`；只有 fresh bootstrap append 允许启动 fixed Supervisor。bootstrap exact replay只能 authenticated reconnect/recovery，禁止第二个 Supervisor。

### 5. held authority、Pi ABA 与 callback 纪律

`DurableRunAuthority` 必须提供语义等价的内部接口：

```text
WithCurrentRunAuthority(ctx, RunAuthorityBinding, func(CurrentRunAuthority) error) error
ResolvePreparedExecution(ctx, CurrentRunAuthority, preparationDigest) (PreparedExecutionV1, error)
CommitRunStartOutcome(ctx, CurrentRunAuthority, preparationDigest,
  processStartedFactDigest, resumeOutcomeFactDigest) (RunProjection, error)
```

1. `WithCurrentRunAuthority` callback 必须恰好调用一次；不能逃逸、保存、跨 goroutine、异步继续、重入或在返回后使用。外层 owner lock、Run head、Attempt owner/generation 任何漂移均 fail closed。ProcessBridge 的 resolve、必要 mechanics 调用与 Run outcome CAS 全部发生在这一个 borrowed callback 中。
2. `ResolvePreparedExecution` 只能以 `preparationDigest` 索引 durable fact，并从 held allocation authority 解析唯一完整 receipt、从 held Attempt authority 解析唯一 `launch-authorized` 与 `StoredClosureV1` 原件；fresh Start 时 current Attempt business head 必须仍精确等于该 `launchAuthorizedFactDigest`，且 current owner 必须仍等于 prepared 中的 binding。public projection、配置、路径扫描、Provider map 与调用者 bytes 都不能补齐字段。
3. 进入任何 bootstrap/command mutation 前，ProcessBridge 必须从 source closure 对 Node、`pi-bundle/cli.js` entrypoint、两个 roots 与全部 55 materials 建立 held FD 表。每个 regular object 都要执行 `fstat-before → held-FD raw hash → fstat-after`，验证完整 device/inode/type/mode/uid/gid/size/link-count/hash；目录同样验证 held identity。由此重算 `Pi0843IdentityV1`，并与 source facts、prepared digests 和 configured `PiProfile.IdentityDigest()` exact equal；每个 Run 的 `agentLaunchSpecDigest` 另与 source closure/Prepared exact equal。
4. 在 append/execute prepared `spawn` 前，不能只对旧 held FD 做 `fstat`。Core 必须从已验证的 Node canonical parent、allocation parent 与 material-root parent descriptors 出发，逐组件 descriptor-relative、nofollow 重新打开 **current** Node、entrypoint、两个 roots 与全部 materials 的 canonical path；重新枚举 roots 下 current material role/set，逐项重算 reopened identity/raw hash，并同时与 source closure 和旧 held FD identity exact equal。任一 current path swap、symlink/hardlink、inode/hash变化、材料增删/换位、FD close/replace 或集合 ABA 均拒绝。完成比较后以 reopened current FDs 替换旧表并保持打开，直到 post-exec barrier 完成；禁止用 pathname `stat`、只验旧 FD 或重开后不比较来通过门禁。
5. `StoredClosureV1.workingDirectory` 必须沿 ADR 0056/0059 的 held allocation/cwd chain descriptor-relative、nofollow 打开并验证为 expected current directory；spawn intent、Supervisor runtime/cwd object digest 与后续 `ProcessObservation` 必须引用同一个 held cwd identity。Node、entrypoint、materials 与 cwd 的最终 current-path reopen/`fstat` 必须 mutation-adjacent 地发生在 bootstrap 前以及 spawn 前；中间不得运行 Provider callback、逃逸 goroutine、可重入 hook 或无关 I/O。bootstrap 前漂移必须 authority/外部零 mutation；bootstrap 后、spawn 前漂移不得 spawn，并按既有 pending/intervention 事实收敛。
6. 实际执行仍必须经过 ADR 0058/0059 的双 barrier：只用已验证 stable Node pathname创建 suspended child，post-exec 观察必须证明实际 image 是同一 Node、cwd/runtime identity 相等且 Provider entrypoint 尚未执行；`process-started` 落账后再走第 6 节 exact resume。current-path reopen 不能替代真实 exec barrier，held FD 相等也不能冒充已执行 image。
7. public `PreparedRunStart` 只作索引和 stale-input guard。已有 exact committed Run-start outcome 时直接 replay；没有 outcome且 fresh-start 不再成立时返回 recovery/intervention，禁止重新 launch。

### 6. exact `resume` 后才可提交 `RUNNING`

唯一成功顺序为：

```text
fresh supervisor bootstrap
  → bind-authority outcome
  → spawn outcome(state=exec-stopped)
  → process-started Attempt fact
  → resume intent
  → resume outcome(disposition=ok, reason=process-resumed, state=running)
  → CommitRunStartOutcome
  → run-start-outcome(state=RUNNING)
```

1. ProcessBridge 不能以 `nil`、PID、path、RunProjection 或 `process-started` 单独表示成功。它最多返回 secret-safe exact `(processStartedFactDigest, resumeOutcomeFactDigest)` reference；两项都必须已在同一 RB1 authority 中耐久。
2. `CommitRunStartOutcome` 必须重新解析 exact `process-started` fact和 exact `resume` outcome，证明二者属于 prepared 的完整 Attempt、同一 launch/child/session，resume intent 引用该 `processStartedFactDigest`，outcome 为 authenticated `VerifiedCommandOutcome`，且 `disposition=ok`、`reason=process-resumed`、`ProcessReport.state=running`。rejected/unknown/identity-conflict/非 running outcome 永不授权 `RUNNING`。
3. commit 还须证明 current Attempt business head 是该 exact `process-started`（resume recovery 子链不推进 Attempt head），current owner/generation 仍相等，Run 仍为 prepared 绑定的 `READY` head。随后在 durable Run journal creation-once 提交 `run-start-outcome`，原子绑定两个 fact digest、`preparationDigest`、successor sequence/head 与 `state=RUNNING`；fsync 前不得返回成功。
4. exact 三元组 replay 返回同一 `RunProjection`，零追加、零副作用。same preparation 指向不同 process/resume fact、owner/head 漂移或 cross-child/session 固定 conflict。`CommitRunStartOutcome` 是唯一 `READY → RUNNING` 生产提交点。

response-loss 判定固定为：

| 丢失边界 | 唯一下一步 | 禁止行为 |
| --- | --- | --- |
| `process-started` 后、resume intent 前 | 从 exact current fact创建一次 resume prepared command与 intent | 新 child、新 `process-started` |
| resume intent 已 fsync、Supervisor 未收到 | 以同一 command ID/request/head 执行 prepared command | 生成第二 intent或新 command ID |
| Supervisor 已处理但 response/outcome 未入 RB1 | authenticated prepared-command replay取回同一 `VerifiedCommandOutcome` 后 append exact outcome | 第二次 resume、猜测 running |
| resume outcome 已 fsync、Run commit 前 | 以 exact 两个 fact digest调用 `CommitRunStartOutcome` | 再发 Supervisor command |
| Run commit 已 fsync、response 丢失 | replay同一 committed `RunProjection` | 追加第二 outcome |
| rejected/unknown/conflict 或 owner/session 无法重锚 | 保留证据并进入既有 recovery/intervention | 提交 `RUNNING` 或盲目 retry |

### 7. 唯一真实 producer chain

```text
marshal / marshal control-plane serve
  → PublicApplicationPort.PrepareRunStart
  → owner lock + DurableRunAuthority.WithCurrentRunAuthority
  → current Attempt/lease admission
  → BindOwnerToAttempt
  → Allocation Controller provision/reconcile → exact receipt
  → Core launch preparation → exact launch-authorized + StoredClosureV1
  → compare-and-append secret-safe PreparedExecutionV1 + fsync
  → return secret-safe PreparedRunStart
  → PublicApplicationPort.StartPreparedRun
  → owner lock + WithCurrentRunAuthority
  → ResolvePreparedExecution + mutation-adjacent Pi held-object recheck
  → ProcessBridge（fresh bootstrap → spawn → process-started → resume outcome running）
  → CommitRunStartOutcome
  → durable RUNNING RunProjection
```

禁止 CLI/server 直接调用 `execution.Run`、ProcessBridge 构造 Attempt/receipt/closure、根据配置/PATH拼装 prepared、child `task run`、environment selector、memory-only prepared map，以及 bridge 返回后由另一事务猜测 Run 已启动。ADR 0059 的 RB1 no-raw 规则适用于 prepared、Supervisor intent/outcome、Run outcome及其 projections：不得再次持久化 raw request payload、path、argv、environment values、stdin、nonce 或 transcript bytes；held Attempt authority 中既有 `launch-authorized` 是完整 closure 的唯一原件。

## 最小实现与验证边界

本 ADR 接受后，首个实现切片只能：

1. 在现有 durable authority 中加入 `Pi0843IdentityV1`、secret-safe `PreparedExecutionV1` strict codec、creation-once append/resolve，与 `BindOwnerToAttempt`/source fact lookup 的 component contract；
2. 加入 `WithCurrentRunAuthority`、`ResolvePreparedExecution`、`CommitRunStartOutcome`，让 ProcessBridge 只消费 resolver 给出的 held originals并返回 exact process/resume references；
3. 接通 Pi `0.84.3` held-object verifier，完成一个 producer-chain component test；不得以该 component test升级 `INTEGRATED`；
4. 一次覆盖下列 hostile matrix：
   - owner bind/rebind、owner epoch ABA、stale Run/Attempt/head/generation、错误 receipt/launch source；
   - entrypoint/Node/root/material/cwd 的 symlink/hardlink/current-path/inode/hash/集合 ABA，旧 held FD 未变但 canonical path 已替换，configured Pi identity 不等；
   - crash/response loss发生在 bootstrap、spawn、process-started、resume intent/outcome、Run commit每个边界；
   - callback zero/double call、escape/goroutine/reentry、bridge 只回 process fact、并发 start；
   - same key different bytes、cross-child/session resume、rejected/non-running resume；
   - 对 Prepared/Run journal/Supervisor ledger/event/error/log 做 raw argv/environment/path 泄漏扫描，除 held Attempt `launch-authorized` 唯一原件外零匹配。

交付顺序固定为：**先接受本 ADR → 只落一个 bounded authority component → 立即以相邻下一切片接入 fixed `marshal` / `marshal control-plane serve` composition root**。component 与 composition 之间不得插入第二个 component、Provider 扩面或无关 Harness 工作。该切片不得顺带实现 terminalization/通用恢复策略、server transport、Linux authority、发布签名、通用 AgentProvider prepared schema或第二存储层；在相邻 composition 完成前始终标记 `COMPONENT / INTEGRATION-OPEN`。

## 后果

ProcessBridge 第一次只能消费由 Core 从 current durable facts 解析出的 held originals，Pi configured identity 与启动瞬间真实 bytes 闭合，`READY → RUNNING` 只在真实 child 成功 resume 后提交。代价是增加一份 secret-safe Run authority索引和严格 held callback；它不会复制 raw closure，也不会建立第二 authority。该代价只关闭当前 production seam，不引入通用 workflow 或恢复框架。
