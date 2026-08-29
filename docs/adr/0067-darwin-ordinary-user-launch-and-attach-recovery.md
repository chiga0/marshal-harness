# ADR 0067：Darwin ordinary-user 启动门禁归位与只读 Attach 恢复

- 状态：提议（Proposed，2026-08-29）
- 提议基线：`main@84d2dcd6bb78cb7fa47ed1d3040a1f3bea5a0f11`
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

1. `Attach` 必须从 RB1 pending state定位 exact session，验证 held control root、nonce challenge、peer UID/PID/birth、fixed binary path/SHA-256/CDHash/sourceHead/protocol、Supervisor session/child identity、journal sequence/head、command sequence/head与调用者提供的 exact previous authority anchor。
2. `Attach` 不追加 Supervisor mechanics journal，不改变 owner epoch、authority head、command head、pending request或 child state；失败必须保证 authority ledger、mechanics journal与control objects逐字节不变。
3. owner/head 重锚只能通过已有 prepared-command模式完成：Core 先在 RB1 creation-once追加 exact `bind-authority` command intent，再通过已 Attach 的连接执行该命令，最后立即追加 authenticated outcome。`bind-authority` 增加封闭的 `initial|owner-successor` mode；`owner-successor` 只允许 session 已 bound、无 pending command、exact previous anchor匹配且 new owner acquisition已在 RB1耐久时执行。
4. 新生产路径不再追加 `process-supervisor-session-reconnected`。历史 reconnect facts继续逐字节 decode/replay和投影，但不得成为新 Run 的 production evidence。
5. `Attach` 与 `bind-authority(owner-successor)` 均不得重建 child、重新打开另一组 pipe、重发已完成 command或把 absence/identity conflict猜作正常退出。

### 5. response-loss、owner change 与恢复分型

恢复矩阵冻结如下：

| 当前证据 | 唯一动作 | 禁止行为 |
| --- | --- | --- |
| exact command outcome/RB1 business fact 已耐久 | 只从所属 ledger replay；无需 reconnect | 重发 command、创建第二 fact |
| 同一 owner 的短暂 transport loss，exact pending intent存在，原 Supervisor/session/anchor可 Attach | 仅以相同 command ID、request digest与pre-anchor replay一次；由 Supervisor journal判定并补同一 outcome | 新 command ID、不同 bytes、第二副作用 |
| owner已变化且存在 pending command | typed intervention | 跨 owner replay、rebind 后猜测执行结果 |
| owner已变化、`process-started` 尚未耐久 | typed intervention；保持 child suspended或按已证实的无 child/cleanup路径收口 | 自动 launch recovery、第二 Supervisor、第二 child |
| `process-started` 已耐久、无 pending command、同一 Supervisor/child可 Attach | `Attach → bind-authority(owner-successor) intent/outcome →` 继续 exact inspect/collect/terminalization | 伪造 reconnect fact、重建 child、重复 resume |
| Supervisor/session/child/source/control identity不唯一 | typed intervention | PID/path扫描、跨编排 signal、以“不存在”补 terminal fact |

Run-start response-loss继续遵守 ADR 0065：ResultIngress只查自己的 Attempt ledger；runstore只查自己的 Run journal。exact resume outcome已耐久时可以重新完成 final current-ledger recheck并 mint新的一次性 proof；Run successor已耐久时只从runstore replay，绝不再次调用Supervisor。

### 6. intervention 与新 Attempt

1. intervention 必须保存 secret-safe reason code、exact session/command/source/owner evidence digest与最后可证事实；不删除、不改写历史。
2. 只有能够从 current authority 与真实 mechanics证明“外部副作用从未发生”，或已按 exact identity完成 terminalization/cleanup并耐久 `cleanup-completed → lease-released`，Core才可依既有预算创建新 Attempt。
3. pending command、未知 child、未知 Supervisor、无法闭合的 source/control identity或跨 owner ambiguous state不得直接创建 successor Attempt。操作者可以选择终止旧 Run并创建关联新 Run，但不能复活或改写旧 Run。

### 7. 精确取代与修订范围

本 ADR 若被接受，将按以下范围生效：

1. **部分取代 ADR 0059 §6**：`process-started` 前的 Core restart不再承诺自动重连并推进启动；owner变化固定 intervention。`process-started` 后、无 pending command且exact session/child仍可证时保留恢复。
2. **部分取代 ADR 0060 §3、§5**：新生产路径不写 `process-supervisor-session-reconnected`；改为只读 `Attach` 后通过已耐久 `bind-authority(owner-successor) intent → execute → outcome`重锚。跨 owner pending command固定 intervention；同 owner exact pending replay保留。
3. **部分取代 ADR 0063 §4.4、§5.3–§5.5及§6对应 response-loss 行**：Core不跨 bootstrap持有完整 source/material FD表，也不承担第二次 mutation-adjacent source authority；pre-bootstrap只做无副作用 current closure admission，Supervisor `spawn`承担唯一 mutation-adjacent exact-set gate。owner在`process-started`前变化固定 intervention。
4. **澄清 ADR 0065 §9、§10**：sealed proof、单向依赖、generic Append拒绝与Run response-loss合同不变；S1不实现通用 reconnect state machine。
5. ADR 0064、ADR 0066保持不变。ADR 0061 transcript disposition和ADR 0056 terminalization顺序保持不变，但其恢复入口使用本 ADR 的 `Attach` 分型。

在本 ADR仍为 Proposed 时，上述条款只作为 implementation replan候选，不修改已接受 ADR 的现行状态。

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
- owner successor、无pending、`process-started`已耐久：必须先有RB1 `bind-authority` intent，再有Supervisor receipt/outcome，之后才能执行下一command；
- owner successor且存在pending intent：intervention，零bind、零command replay；
- 同owner same ID/same digest exact replay只产生一个effect与一个outcome；same ID different digest固定conflict；
- bootstrap已发生但`process-started`未耐久时owner变化：intervention，零第二Supervisor/child；
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
5. 包级hostile/crash/replay/race与单向依赖检查。

S1′明确不得引入`506a647`的typed mutation substrate，不修改`internal/execution`、`internal/review`、`internal/selfidentity`或CLI，不实现通用reconnect/terminalization/factory。

### S2′：fixed local composition

S1′后立即完成：

1. 按ADR 0066实现两阶段owner、canonical`.marshal`与唯一Darwin arm64 production factory；
2. 在controller唯一组合点接入S1′，fixed`cmd/marshal`本地mutation/inspect只持有`PublicApplicationPort`；
3. 真实Pi fresh Run证明exact resume后唯一`RUNNING`，覆盖ResultIngress outcome与Run append两类response-loss；
4. `Status`诚实区分available、production-composition-incomplete、recovery-required与platform-profile-unavailable；
5. legacy `execution.Run`、child CLI、Fake seam、第二authority root与独立`marshal-server`从S2′ production graph不可达。

### S2′后、release前

按以下顺序单独交付，不回塞S1′/S2′：

1. 本ADR的只读`Attach`、`bind-authority(owner-successor)`和`process-started`后无pending恢复；
2. ADR 0056/0061 terminalization、transcript disposition、cleanup与successor；
3. ADR 0062 authenticated`marshal control-plane serve` transport与durable delivery ledger；
4. 最终fixed-bin真实Run、故障矩阵、独立Decision与RC gate。

## 后果

正面结果是v1只实现ordinary-user能够诚实证明的边界：Core负责无副作用准入，Supervisor负责mutation-adjacent source与真实process mechanics，ResultIngress负责Attempt currentness，runstore负责Run successor；未知状态明确转intervention。S1代码面回到当前Store与既有mechanics，不再为一个Run-start引入横跨execution/review/selfidentity的通用substrate。

代价是`process-started`前的Core owner变化不再自动恢复，跨owner pending command也不能透明继续；这些场景需要人工处置或在已证明无副作用/已完成cleanup后创建新Attempt。该降级是Mac ordinary-user产品边界的诚实表达，不影响未来hardened Linux/Container/VM profile以更强内核authority另行实现自动恢复。
