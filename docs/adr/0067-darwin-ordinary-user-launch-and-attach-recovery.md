# ADR 0067：Darwin ordinary-user 启动门禁归位与只读 Attach 恢复

- 状态：已接受（Accepted，2026-08-29）
- 提议基线：`main@84d2dcd6bb78cb7fa47ed1d3040a1f3bea5a0f11`
- 接受记录：提案 `1e05fb831c04a1c87e7f4ecdc677c97beb9d88e6` 经唯一独立 reviewer 复审，`P0=0`、`P1=0`；接受只冻结本文合同，不表示 S1′/S2′、Attach/rebind、terminalization 或 RC1 已实现，也不升级 R2–R6。
- 关联：[ADR 0051](0051-darwin-local-dogfood-profile.md)（ordinary-user 边界）、[ADR 0059](0059-fixed-darwin-process-supervisor.md)（固定 Supervisor）、[ADR 0060](0060-supervisor-mechanics-authority-binding-and-recovery.md)（command recovery 子链）、[ADR 0063](0063-prepared-execution-authority-and-production-chain.md)（PreparedExecution）、[ADR 0064](0064-darwin-control-directory-phased-identity.md)（控制目录身份）、[ADR 0065](0065-sealed-run-start-proof-and-one-way-composition.md)（密封 Run-start proof）、[ADR 0066](0066-production-composition-owner-acquisition.md)（production factory）、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)。

## 背景

ADR 0063–0066 把 v1 Mac-first Run-start 的 authority 条件逐步补齐，但冻结后的首批实现候选暴露出同一类重复 P1：把 ordinary-user 能诚实做到的 current-source 检查、进程 mechanics 与 response-loss 恢复扩张成跨阶段、跨 owner、近似 hostile same-UID 的完整连续性证明。结果是同一 source closure 同时在 Core 与固定 Supervisor 中持有和重开，同一 owner/head 又同时进入 reconnect fact、Supervisor anchor 与 Run-start projection；代码面随之扩大，仍不能证明被完全控制的同 UID 主体不会在两次检查之间替换 pathname。

冻结候选的事实如下：

1. `a6a0d638f45d6902b9c453b1e600b5f798380d82` 在 19 个文件中增加 968 行、删除 102 行，把 current-path reopen、跨阶段 held FD 与 reconnect recovery 集中进 `launchidentity`、`processsupervisor` 和 `resultingress`。第二轮仍出现 source currentness 与 owner/reconnect 职责重复的 P1：旧 held FD 未变不能证明 current pathname 未替换；Core 跨 bootstrap 保存第二套 held closure 也不能替代 mutation-adjacent Supervisor 检查。
2. `506a6470767f187290df08b1060834ed59aeabdb` 为后续 runstore proof 引入 24 文件、`+2608/-1081` 的 typed mutation substrate，并改动 `execution`、`review`、`selfidentity` 等非必要调用链；`6298eaebebb9ec705e74903cdf2a32dda0b6a62c` 又在其上增加 12 文件、1167 行。该堆叠把本应由现有 descriptor-bound `runstore.Store`/`Lease` 自证的 Run currentness扩成第二套 Attempt/owner/generation substrate，偏离 ADR 0065 的单向 proof 边界。
3. 两条候选都未形成可直接合入的 bounded S1：继续逐项 rework 会同时固化过度合同与过宽代码面，且无法把 ordinary-user 提升为 hardened assurance。

因此本 ADR 冻结候选 `a6a0d63`、`506a647` 与 `6298eae`：保留其测试与失败证据用于重建，不直接合入，不以 cherry-pick 后继续补丁的方式推进 S1。新实现从当前 `main` 建立 fresh worktree，并按本文的最小合同重新切片。

## 决策

### 1. 适用范围与诚实声明

1. 本合同只适用于 `darwin-local-dogfood` 的 trusted、single-user、ordinary-user、`workspace-write`、non-production profile。它不提供 hardened sandbox、跨用户隔离、fully controlled same-UID 防护、Linux process authority 或 stable release authority。
2. ordinary-user v1 的目标是：对 Core 与 fixed Supervisor 实际观察到的 current objects、持久 authority facts 和真实 process mechanics 给出可重放、fail-closed 的结论；不宣称两次观察之间存在内核强制的 pathname 不变性。
3. 任何无法唯一证明的 pre-start、pending-command、peer、source 或 child 状态进入 typed intervention。Intervention 是诚实产品结果，不得被描述成透明恢复成功。

### 2. 必须保留的不变量

以下合同不因简化而降低：

1. `PreparedExecutionV1` 仍 creation-once、closed、secret-safe，并精确绑定 current owner、Attempt/Allocation、`launch-authorized`、`StoredClosureV1`、Pi identity 与 Run preparation；raw path、argv、environment、stdin、nonce 与 transcript bytes 不复制进 Run journal、mechanics journal、event、error 或 log。
2. ResultIngress 仍是 owner、Attempt、generation、fencing、`process-started` 与 exact successful `resume` 的唯一 current-ledger 解释者；最终重验通过后才可 mint ADR 0065 的 shared-guard `CommittedRunStartProof`。
3. runstore 仍只解释自己的 descriptor-bound `Lease`、Run state/head 与唯一 successor；generic `Append` 必须拒绝新的 `READY → RUNNING`，且不得读取或镜像 ResultIngress 的 owner/Attempt/generation。
4. `READY → RUNNING` 仍只允许在真实 runtime 已通过 exec barrier、`process-started` 已耐久，且 exact authenticated `resume(disposition=ok, reason=process-resumed, state=running)` outcome 已耐久后提交。
5. fixed `marshal internal process-supervisor` 仍是唯一 process mechanics owner。Supervisor 仍持有 child wait right、输出 pipe、runtime/cwd/material FD、PID/birth/PGID，并执行有界 `spawn/resume/inspect/terminate/collect/close`；它不拥有 lifecycle、retry、ReviewDecision、release、unlock 或 successor authority。
6. ADR 0064 的 initial-empty、final setup、phase-aware exact entry set、nonce/journal/socket/output object gate保持不变。
7. ADR 0065 的 proof API、shared guard、ResultIngress → runstore 单向依赖、固定锁序、self-only CAS 与 response-loss 各查自身账本保持不变。
8. ADR 0066 的 scope-only Phase A、锁内 Phase B、canonical `<repository>/.marshal`、唯一 Darwin arm64 factory和 fixed `cmd/marshal` 只持有 `PublicApplicationPort` 的合同保持不变。

### 3. source currentness 归位

source gate 分为两个明确阶段，不再由 Core 与 Supervisor 跨阶段共同持有第二套 closure：

1. Core 在追加 `process-supervisor-bootstrap-prepared` 前，基于 held allocation/source authority 执行一次**无副作用** current closure 验证：descriptor-relative/nofollow 打开 current Node、entrypoint、material roots、完整 material set与 working directory，重算 `Pi0843IdentityV1`、`launchMaterialsDigest`、`agentLaunchSpecDigest`，并证明 working directory 与 current `AllocationProvisionReceiptV1.LiveIdentity` 精确闭合。失败时必须零 bootstrap、零 Supervisor、零 child。
2. Core 完成上述检查后即释放这组只服务 pre-bootstrap admission 的 source FD；不得把它作为跨 bootstrap 的第二 mechanics authority，也不得向 runstore/claim复制其 identity。
3. Supervisor 的 `spawn` 是唯一 mutation-adjacent source gate。它从 authenticated、ephemeral spawn request 中的 canonical locators 出发，使用 `O_NOFOLLOW_ANY` 或逐组件 descriptor-relative/nofollow 的 parent chain独立打开对象；locator只用于定位，授权来自重新观察的exact identity/hash与已耐久source digests。Supervisor重新枚举exact role→record→file集合，按UTF-8 byte order形成keyed pair，逐对象执行open/fstat/hash/fstat、完整identity/hash/set校验，并验证cwd与allocation live identity。缺项、多项、错role、record/file错配、symlink/hardlink、pathname替换或hash/identity漂移均在child creation前拒绝。
4. Supervisor 将**同一组**已验证 FD保持到 child exec-stop/post-exec barrier完成，并把 exact set digest、runtime/cwd observation写入 secret-safe mechanics receipt。禁止验证一组对象却执行另一组 pathname。
5. 两个阶段之间的 pathname变化由 Supervisor gate检测并拒绝；本合同不声称能抵御完全控制同 UID、同时操纵 Marshal process或内核观察面的恶意主体。需要该保证时必须使用 hardened Container/VM/Remote provider并另行提供 ConformanceEvidence。

### 4. 只读 `Attach` 与 authority rebind

`processsupervisor.Client` 增加只读 `Attach`，用于连接仍存活的同一 Supervisor；它不是 reconnect authority fact，也不能在成功返回时改变任何 durable state。

新 owner 恢复已启动进程的唯一顺序冻结为：

```text
Acquire 并持续持有 repository owner
  → RB1 exact current Attempt head + no-pending 重验
  → append/fsync/replay control-owner-bound successor
     (exact previous Attempt head + exact new acquisition)
  → read-only Attach
     (previous Supervisor anchor + current acquisition + held owner)
  → append bind-authority(owner-successor) intent
     (exact control-owner-bound successor fact)
  → execute 同一 prepared command
  → append authenticated outcome
```

1. recovery process 必须先按 ADR 0066 取得并在整条恢复链内持续持有 exact repository physical owner lock 及其 acquisition-bound `CurrentOwnerLockVerifier`。释放、更换或重取 owner 会使当次链立即失效，不得继续 `Attach` 或 command。
2. 在任何 `Attach`、control object 变更或 Supervisor command 前，Core 必须从 RB1 重放 exact current Attempt revision/head，证明 `process-started` 已耐久、无 pending command、无 intervention，并以同一 held-owner verifier 对 exact predecessor head 追加、fsync 和重放唯一 `control-owner-bound` successor。该 fact 必须同时绑定 predecessor Attempt fact digest/revision/head 与 new `ControlOwnerAcquisition` fact digest/epoch；sibling successor、旧 head、旧 acquisition 或未 replay 的 append 均拒绝。
3. `Attach` 只能在 acquisition-bound borrowed callback 内构造与使用。它同时认证 held control root、nonce challenge、peer UID/PID/birth、fixed binary path/SHA-256/CDHash/sourceHead/protocol、Supervisor session/child identity、journal sequence/head、command sequence/head、exact previous Supervisor authority anchor，以及刚重放的 current acquisition/`control-owner-bound` fact 与仍在持有的 repository owner。任一边不符即拒绝。
4. `Attach` 返回的 connection/client 是同一 callback 内的同步 borrowed value；不得返回、保存到 struct/interface、跨 goroutine、跨 callback 或在 owner verifier 失活后使用。architecture test 必须机械锁定零逃逸与唯一 production callsite。
5. `Attach` 本身不追加 Supervisor mechanics journal，不改变 owner epoch、authority head、command head、pending request或 child state；失败必须保证 mechanics journal与 control objects 逐字节不变，并且除已先行耐久的唯一 `control-owner-bound` successor 外不得新增任何 RB1 fact。
6. owner/head 重锚只能通过已有 prepared-command模式完成：Core 在同一 borrowed callback 内 creation-once 追加 exact `bind-authority(owner-successor)` command intent，该 intent 必须引用第 2 项的 exact `control-owner-bound` successor fact digest、current acquisition 与 authenticated Attach observation；随后执行同一 prepared command并立即追加 authenticated outcome。session 必须已 bound，且 previous anchor/current acquisition/current Attempt head 逐项匹配；调用者不得自选 post-anchor。
7. 新生产路径不再追加 `process-supervisor-session-reconnected`。历史 reconnect facts继续逐字节 decode/replay和投影，但不得成为新 Run 的 production evidence。
8. `Attach` 与 `bind-authority(owner-successor)` 均不得重建 child、重新打开另一组 pipe、重发已完成 command或把 absence/identity conflict猜作正常退出。

### 5. response-loss、owner change 与恢复分型

恢复矩阵冻结如下：

| 当前证据 | 唯一动作 | 禁止行为 |
| --- | --- | --- |
| exact command outcome/RB1 business fact 已耐久 | 只从所属 ledger replay；无需 reconnect | 重发 command、创建第二 fact |
| 同一 owner 的短暂 transport loss，exact pending intent存在，原 Supervisor/session/anchor可 Attach | 仅以相同 command ID、request digest与pre-anchor replay一次；由 Supervisor journal判定并补同一 outcome | 新 command ID、不同 bytes、第二副作用 |
| owner已变化且存在 pending command | 耐久 permanent typed intervention | 跨 owner replay、rebind 后猜测执行结果、cleanup/release/successor |
| owner已变化、`process-started` 尚未耐久，且在任何 intervention 前能 exact 证明无 Supervisor、无 child、无 command/mechanics 副作用 | 追加封闭 `no-effect-aborted → cleanup-completed(no-effect) → lease-released` 链；全链耐久后才可依既有预算创建新 Attempt | 先写 intervention、自动 launch recovery、第二 Supervisor/child |
| owner已变化、`process-started` 尚未耐久，但上述零副作用证明缺失或失败 | 耐久 permanent typed intervention | 任何 signal/terminate/close/cleanup/release/successor或新 Attempt |
| `process-started` 已耐久、无 pending command、同一 Supervisor/child可 Attach | 持续 held owner 下严格执行「RB1 no-pending → `control-owner-bound` successor → read-only `Attach` → exact bind intent/execute/outcome」，再继续 exact inspect/collect/terminalization | 伪造 reconnect fact、先 Attach 后绑 owner、重建 child、重复 resume |
| Supervisor/session/child/source/control identity不唯一 | 耐久 permanent typed intervention | PID/path扫描、跨编排 signal、以“不存在”补 terminal fact、cleanup/release/successor |

Run-start response-loss继续遵守 ADR 0065：ResultIngress只查自己的 Attempt ledger；runstore只查自己的 Run journal。exact resume outcome已耐久时可以重新完成 final current-ledger recheck并 mint新的一次性 proof；Run successor已耐久时只从runstore replay，绝不再次调用Supervisor。

### 6. intervention 与新 Attempt

1. Core 只能在写入任何 intervention 之前，持有 current repository owner acquisition 并重放 exact Attempt head 的同一临界区内，尝试一次无副作用判定。允许走 no-effect 路径的充分且必要条件是：RB1 中无 `process-supervisor-started`、`process-started`、command intent/outcome、result admission与 intervention；ADR 0064 exact control set 证明无 Supervisor session/socket/nonce/mechanics journal；Core-held process/allocation observation 证明无 child/PID/birth/PGID/wait right/pipe；且无任何已发出或 unknown 的 Supervisor command/mechanics 副作用。“没找到”、PID/path 扫描或单一 ledger 空缺都不足以证明。
2. 上述证明成功时，Core 必须以封闭 creation-once fact 链收口：`no-effect-aborted → cleanup-completed(mode=no-effect) → lease-released`。`no-effect-aborted` 绑定 exact Attempt revision/head、current owner acquisition/fact digest、全部 negative-observation digest 与封闭 reason；后两个 fact 各自引用 exact predecessor digest、Attempt/allocation/lease/generation。如果已有 exact allocation provision receipt，只能先经既有 Provider close intent/receipt 安全收口并绑入 `cleanup-completed`；这不允许任何 Supervisor/process cleanup command。全链 fsync/replay 前不得解锁、释放 binding 或创建新 Attempt。
3. 只有第 2 项链完整耐久且既有 retry/budget policy 仍允许时，Core 才可创建一个新 Attempt；新 Attempt 必须引用 exact `lease-released` predecessor 并重走全部 admission。no-effect 结论不得在 intervention 后补造，也不得为原 Attempt 重建 Supervisor/child。
4. 任一无副作用前提缺失、不可唯一或校验失败时，Core 必须追加 permanent typed intervention。它保存 secret-safe reason code、exact session/command/source/owner evidence digest与最后可证事实，不删除、不改写历史；一经耐久，该 Attempt 永久禁止 signal、terminate、close、`cleanup-completed`、`lease-released`、unlock、successor 和新 Attempt。后续只允许读取/explain/exact replay。
5. pending command、未知 child、未知 Supervisor、无法闭合的 source/control identity或跨 owner ambiguous state 一律走第 4 项，不得用“先 intervention、再 cleanup”绕过。操作者可以另行终止旧 Run 的产品层处置并创建关联新 Run，但不能复活、改写或解锁该 intervention Attempt。

### 7. 精确取代与修订范围

本 ADR 自接受起按以下范围生效：

1. **部分取代 ADR 0059 §6**：`process-started` 前的 Core restart不再承诺自动重连并推进启动；owner变化时，只有在 intervention 前 exact 证明无 Supervisor、无 child、无 command/mechanics 副作用才可走封闭 no-effect abort/cleanup 链，否则固定 permanent intervention。`process-started` 后、无 pending command且exact session/child仍可证时保留恢复。
2. **部分取代 ADR 0060 §3、§5**：新生产路径不写 `process-supervisor-session-reconnected`；改为持续持有 repository owner/acquisition，从 exact RB1 no-pending 开始，先追加并重放绑定 predecessor Attempt head 与 new acquisition 的 `control-owner-bound` successor，再执行只读 `Attach → bind-authority(owner-successor) intent → execute → outcome`。跨 owner pending command固定 permanent intervention；同 owner exact pending replay保留。
3. **部分取代 ADR 0063 §4.4、§5.3–§5.5及§6对应 response-loss 行**：Core不跨 bootstrap持有完整 source/material FD表，也不承担第二次 mutation-adjacent source authority；pre-bootstrap只做无副作用 current closure admission，Supervisor `spawn`承担唯一 mutation-adjacent exact-set gate。owner 在 `process-started` 前变化时只允许上述 no-effect 链或 permanent intervention，不得恢复 launch。
4. **澄清 ADR 0065 §9、§10**：sealed proof、单向依赖、generic Append拒绝与Run response-loss合同不变；S1不实现通用 reconnect state machine。
5. ADR 0064、ADR 0066保持不变。ADR 0061 transcript disposition和ADR 0056 terminalization顺序保持不变，但其恢复入口使用本 ADR 的 `Attach` 分型。

上述取代只适用于本文明确的 Darwin ordinary-user 边界；其余条款与更高保证 profile 继续按原 ADR 生效。

## 必须通过的负面与崩溃矩阵

### source 与 cwd

- pre-bootstrap Node/entrypoint/root/material/cwd 任一 identity/hash/current-name漂移：零 bootstrap、零Supervisor、零child；
- material增删、role重排、record/file错配、重复role、零匹配：Supervisor `spawn`前拒绝；
- Core检查后、Supervisor spawn前发生pathname替换：Supervisor拒绝且child start计数为零；
- cwd与`AllocationProvisionReceiptV1.LiveIdentity`不等、allocation generation漂移或current receipt不唯一：拒绝；
- Supervisor打开/哈希/set校验后对象变化：post-open/post-exec barrier拒绝，不能提交`process-started`。

### Attach、owner 与 command

- `Attach`成功与失败前后mechanics journal、authority ledger、nonce/socket/control entries逐字节相等；
- peer、PID birth、binary、CDHash/sourceHead、nonce、session、child、journal/command head或previous authority anchor任一替换：Attach拒绝；
- owner successor、无pending、`process-started`已耐久：`control-owner-bound` successor 与 read-only `Attach` 完成后，必须先有引用 exact successor fact 的RB1 `bind-authority` intent，再有Supervisor receipt/outcome，之后才能执行下一command；
- owner successor 恢复必须严格为 held owner/acquisition → exact RB1 no-pending → 唯一 `control-owner-bound` successor → read-only `Attach` → 引用 exact successor fact 的 bind intent/execute/outcome；任一乱序、旧 head/acquisition、sibling successor 或 client/verifier 逃逸均拒绝；
- owner successor且存在pending intent：intervention，零bind、零command replay；
- 同owner same ID/same digest exact replay只产生一个effect与一个outcome；same ID different digest固定conflict；
- `process-started`未耐久且 owner 变化：只有在 intervention 前同时证明零 Supervisor/session/control object、零 child/process handle、零 command intent/outcome/mechanics 副作用，才允许唯一 `no-effect-aborted → cleanup-completed(no-effect) → lease-released` 链；少任一证明必须 permanent intervention；
- no-effect 链的旧 head、缺/duplicate fact、跳过 exact allocation close receipt、链未耐久即新建 Attempt、预算超限或 intervention 后补造全部拒绝；permanent intervention 后 signal/terminate/close/cleanup/release/unlock/successor 调用计数均为零；
- 历史`process-supervisor-session-reconnected`可replay，但新Run producer计数必须为零。

### proof、Run 与response-loss

- exact resume outcome已耐久时不调用Supervisor即可在fresh current-ledger recheck后mint proof；
- proof零值、复制、并发/二次消费、callback逃逸、projector保存均拒绝；
- generic`Append READY→RUNNING`、伪造event、legacy CLI/server direct append均拒绝；
- Run append/fsync后response丢失只从runstore replay，Supervisor调用计数不增加；
- ResultIngress outcome存在而Run successor缺失、或Run successor存在而调用者未收到response，两边各查自身ledger仍唯一收敛。

### intervention、平台与秘密

- ambiguous pending/identity conflict/intervention期间，allocation effect、signal、cleanup release与successor均拒绝；
- non-Darwin与hardened profile不得消费本合同作为authority；
- path、nonce、raw argv/environment/stdin/transcript/credential在新增journal/event/error/log中零出现；
- 所有拒绝分支在声明的零副作用边界前后比较tree、ledger、journal与process计数。

## 最短实施切片

### S1′：fresh-start sealed proof

从当前`main`建立新分支，只完成：

1. `internal/resultingress/prepared_execution.go`：creation-once/resolve、fresh-start-only、exact successful resume与ADR 0065 shared-guard proof；不实现跨owner reconnect；
2. `internal/launchidentity`：无副作用current closure verifier与cwd/Allocation `LiveIdentity`闭合；验证完成即释放Core临时FD；
3. `internal/processsupervisor/mechanics_darwin.go`：补齐exact root enumeration、keyed role/record/file pairing，并让同一held FD组贯穿spawn/post-exec barrier；
4. `internal/runstore/prepared_run_start_authority.go`：直接建立在当前`Store`、`Lease`和descriptor-bound authority之上，实现private projector、唯一successor和generic Append拒绝；
5. runstore 内部允许一个窄的 lease shared-guard/borrow、descriptor-bound strict journal 与read-only projection seam，仅用于在同一 held lease 下完成 self-only CAS；唯一 exported mutation seam 仍是 `WithPreparedRunStartAuthority`，borrow/projector 不得返回、保存、跨 callback 或扩展为通用 mutation API；
6. 包级hostile/crash/replay/race、单向依赖、exported API/AST 形状与borrow 零逃逸检查。

S1′明确不得引入`506a647`的通用 Attempt/Outcome/review/execution/selfidentity typed mutation substrate，不修改`internal/execution`、`internal/review`、`internal/selfidentity`或CLI，不实现通用reconnect/terminalization/factory。上述 runstore 窄 seam 是 S1 shared-guard 的封闭内部机械，不是对`506a647`的回引。

### S2′：fixed local composition

S1′后立即完成：

1. 按ADR 0066实现两阶段owner、canonical`.marshal`与唯一Darwin arm64 production factory；
2. concrete authority 必须在 bounded `PrepareRunStart` producer chain 内，使用现有 authority API 真实产生 `attempt-opened → control-owner-bound/current Attempt binding → allocation provision intent/receipt → launch-authorized/StoredClosure → PreparedExecution`；每一步绑定 exact predecessor/head并可幂等重放，不得从 component-test fixture/seed、调用者 DTO 或内存对象补造；
3. 上述 producer 只允许放在 `internal/productionruntime/authority.go` 及为调用现有 authority API 必需的窄 adapter；不得引入通用 workflow/service 层，也不得调用 legacy `execution.Run`。S1′完成后必须立即相邻进入 S2′，不允许在两者之间插入新的 substrate/component 切片；
4. 在controller唯一组合点接入S1′，fixed`cmd/marshal`本地mutation/inspect只持有`PublicApplicationPort`；
5. 真实Pi fresh Run必须从 fixed CLI 亲历第 2 项全 producer chain，证明exact resume后唯一`RUNNING`，覆盖每一 producer fact、ResultIngress outcome与Run append的response-loss；
6. `Status`诚实区分available、production-composition-incomplete、recovery-required与platform-profile-unavailable；
7. architecture/negative test 机械证明 producer 唯一 callsite、无 seed/Fake/memory-only fact 注入，且 legacy `execution.Run`、child CLI、Fake seam、第二authority root与独立`marshal-server`从S2′ production graph不可达。

### S2′后与 RC1 后继

按以下顺序单独交付，不回塞S1′/S2′：

1. 本ADR的 held-owner 恢复链：exact RB1 no-pending、`control-owner-bound` successor、只读`Attach`、`bind-authority(owner-successor)`与`process-started`后无pending恢复；同切片实现 pre-start no-effect abort/cleanup 与 permanent intervention 二分；
2. ADR 0056/0061 terminalization、transcript disposition、cleanup与successor；
3. 按 [ADR 0068](0068-mac-first-cli-only-lifecycle-preview-rc1.md) 由最终 fixed CLI 运行真实 Pi，经独立 Verification/ReviewDecision 进入 `ACCEPTED`，再以同一最终 Darwin arm64 bytes 完成显式 opt-in 的 RC1 canary 与 prerelease；
4. RC1 后再进入 stable 后继：ADR 0062 authenticated `marshal control-plane serve` 与 durable delivery ledger、managed signing/notarization，以及 Linux production/release gate。

## 后果

正面结果是v1只实现ordinary-user能够诚实证明的边界：Core负责无副作用准入，Supervisor负责mutation-adjacent source与真实process mechanics，ResultIngress负责Attempt currentness，runstore负责Run successor；未知状态明确转intervention。S1代码面回到当前Store与既有mechanics，不再为一个Run-start引入横跨execution/review/selfidentity的通用substrate。

代价是`process-started`前的Core owner变化不再自动恢复，跨owner pending command也不能透明继续；只有在任何 intervention 前 exact 证明无 Supervisor、无 child、无 command/mechanics 副作用时，才能经封闭 no-effect abort/cleanup 链收口并在预算内创建新 Attempt；其余场景永久 intervention 且禁止 cleanup/release/successor。该降级是Mac ordinary-user产品边界的诚实表达，不影响未来hardened Linux/Container/VM profile以更强内核authority另行实现自动恢复。
