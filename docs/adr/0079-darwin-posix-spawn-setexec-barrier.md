# ADR 0079：Darwin `posix_spawn` SETEXEC 启动屏障与 Supervisor v2

| 字段 | 值 |
| --- | --- |
| 状态 | 提议（Proposed） |
| 日期 | 2026-09-04 |
| 提议基线 | `origin/main@c2198e3628f38b126402cf7ab153120ea25e3d77` |
| 关联 ADR | [ADR 0051](0051-darwin-local-dogfood-profile.md)、[ADR 0056](0056-darwin-process-observation-and-attempt-terminalization.md)、[ADR 0058](0058-interpreted-agent-launch-identity.md)、[ADR 0059](0059-fixed-darwin-process-supervisor.md)、[ADR 0060](0060-supervisor-mechanics-authority-binding-and-recovery.md)、[ADR 0062](0062-fixed-marshal-production-server-mode.md)、[ADR 0067](0067-darwin-ordinary-user-launch-and-attach-recovery.md)、[ADR 0071](0071-darwin-sealed-completion-and-durable-result-capability.md)、[Issue #212](https://github.com/chiga0/marshal-harness/issues/212) |
| 关联范围 | Darwin ordinary-user 的最终 runtime exec barrier；不改变上层 lifecycle、ResultIngress、terminalization、server 或发布权限 |

## 背景

ADR 0058/0059 当前在 fixed Marshal child 中使用以下 Darwin 启动屏障：`PT_TRACE_ME → exec(runtime) → SIGTRAP exec-stop → PtraceDetach`。Supervisor 借此在真实 Node 映像已经装载、Provider 用户态入口尚未运行时观察 PID、birth、PGID、runtime/cwd identity，提交 `process-started` 后再放行。

2026-09-04 的 macOS 26.6.2 实机回归证明，这个 mechanics 对 signed/hardened Node 不再可用：`PT_TRACE_ME` 后的 exec 在 dyld 边界产生 `SIGTRAP`，随后 `PtraceDetach` 不能把该事件安全转换为普通继续执行，真实 Node 以 `SIGTRAP` 终止。该失败发生在既有 authority、ResultIngress 与业务生命周期之外；继续修改 retry、terminalization 或 server 都不能修复它。

同机原型还证明一条更窄的替代路径可行：`CGO_ENABLED=0` 的固定 Marshal 可以通过编译进同一二进制的 libSystem ABI bridge 调用 `posix_spawn`，组合 `POSIX_SPAWN_SETEXEC | POSIX_SPAWN_START_SUSPENDED` 后，helper 在**同一 PID**中被 runtime 映像替换，runtime 在进入 Provider 用户态前保持 stopped；父 Supervisor 完成身份观察后发送 `SIGCONT`，signed/hardened Node 正常继续执行。

该原型只证明机制可行，不构成仓库实现、生产集成、conformance、签名或发布证据。尤其是 `posix_spawn` 仍按 pathname 选择 runtime：它没有把 Darwin ordinary-user 启动变成 fd-exec，也没有消除 pathname TOCTOU。

## 决定

### 1. 适用范围与精确取代

1. 若本 ADR 被接受，它只适用于 `darwin-local-dogfood` 的 trusted、single-user、ordinary-user、`workspace-write` 边界，以及未来使用同一 mechanics 的受管 Darwin profile。它不提供 hostile same-UID 防护、hardened Sandbox assurance、跨用户隔离、Linux process authority 或发布 authority。
2. 本 ADR 只取代 ADR 0058 §5 与 ADR 0059 §3/§5/§6/§8 中依赖 `PT_TRACE_ME`、`SIGTRAP exec-stop` 与 `PtraceDetach` 的最终 exec barrier、await 与 resume mechanics。新生产路径禁止这三个 primitive；历史说明与旧 journal bytes 保留。
3. 功能代码面的变更限于 `internal/sandboxlaunch` 的最终 runtime 替换屏障、`internal/processcontrol` 的 stopped observation/await/resume mechanics，以及为隔离新旧 mechanics 所必需的 `process-supervisor/v2` protocol/observer identity、projection 与测试接线。不得借此改写 Core authority、Run/Attempt 状态机、ResultIngress、terminalization、cleanup、review、publication 或 fixed server transport。
4. 以下既有合同逐字保留：fixed `bin/marshal __sandbox-launch`；ready/release pre-exec handshake；held FD、vnode guard 与 mutation-adjacent source precheck；`launch-authorized → process-started → exact resume outcome`；suspended live PID/path/CDHash/birth/PGID/current-authority recheck；ResultIngress 与 terminalization 的同一 CAS barrier；`process-terminal → allocation-terminated → process-supervisor-closed → cleanup-completed → lease-released`；未知状态 fail closed。
5. 上层状态名和业务语义保持不变。v2 `spawn` 成功仍投影为 `exec-stopped`，v2 `resume` 成功仍投影为 `running`；`READY → RUNNING` 仍只允许在 exact authenticated resume outcome 耐久后发生。本文不新增 Run 状态、Attempt 终点或自动 retry 类型。

### 2. 唯一允许调用 SETEXEC 的进程

1. `POSIX_SPAWN_SETEXEC` 只能由 per-Attempt、短寿命、已经完成 ready/release 握手的 fixed `bin/marshal __sandbox-launch` 调用。成功调用会替换调用进程本身，因此该 helper 的 PID、birth 与 process-group leader identity在映像替换前后保持同一条观察链。
2. 长寿命 `marshal control-plane serve`、CLI owner、per-Attempt fixed Supervisor 与任何通用库初始化路径都不得调用 SETEXEC。否则会把 Control Plane 或 mechanics owner 自身替换成 Agent runtime，丢失 owner、socket、journal、wait right 与多 Attempt 服务能力。architecture/call-graph test 必须证明 SETEXEC bridge 只有 `__sandbox-launch` 一个 production callsite。
3. bridge 必须是 fixed Marshal 内、Darwin arm64/amd64 分架构审核的最小 libSystem ABI bridge；构建保持 `CGO_ENABLED=0`。禁止 CGo、`go run`、临时/匿名 Mach-O、脚本/shebang wrapper、额外 helper executable、可下载 dylib、PATH 查找或第二 trusted binary。
4. bridge 只调用宿主 libSystem 的 `posix_spawnattr_init`、`posix_spawnattr_setflags`、`posix_spawn` 与必需的销毁/错误处理表面。symbol、ABI、flag 或调用约定不能精确满足时，在 workload 用户态执行前返回 closed typed unavailable；不得回退到 ptrace、`syscall.Exec`、fork/exec、shell 或匿名 helper。

### 3. 保留 pre-exec 握手并替换最终 barrier

`__sandbox-launch` 的顺序固定为：

```text
继承并验证 spec/ready/release/cwd/runtime/marshal/material held FD
  → descriptor/path/CDHash/closure/current-name precheck
  → 写 ready 并封闭 ready pipe
  → 等待 exact release byte 并封闭 release pipe
  → 再次验证同一 held FD、vnode guard、runtime pathname、cwd 与完整材料集合
  → 初始化 posix_spawn attributes
  → flags = POSIX_SPAWN_SETEXEC | POSIX_SPAWN_START_SUSPENDED
  → 以冻结 absolute runtime pathname、argv、environment 调用 posix_spawn
  → 成功时不返回；同一 PID 的 runtime image 处于 start-suspended
```

ready 只表示 helper 已验证 inherited protocol 并等待 release，不表示 runtime 已装载。release 只授权执行最后一次 current-object recheck 与一次 SETEXEC 调用，不授权 workload 运行。`posix_spawn` 返回非零、意外从 success path 返回、attribute 初始化/设置/销毁不一致或 helper 在 runtime stopped observation 前退出，均固定为 launch failure/intervention；不得伪造 `exec-stopped`。

不得把 ready/release 合并成一个信号，也不得把 `posix_spawn` 提前到 release 之前。raw argv、environment、stdin、nonce 与 transcript 继续只经既有 inherited FD/认证协议有界传输，不能进入 argv 诊断、authority journal、mechanics journal、event、error 或普通 log。

### 4. v2 stopped observation、双端身份核验与 resume

Supervisor/processcontrol 必须对初始 helper 与 start-suspended runtime 执行双端核验：

1. **调用前一端**：以现有 held FD、nofollow current-path reopen、hash/CDHash、vnode guard、cwd/allocation live identity、完整 materials set 与 `launch-authorized`/`StoredClosureV1` 重验，证明本次请求只能尝试冻结的 absolute runtime pathname、argv 与 environment。
2. **装载后一端**：父 Supervisor 对原 helper 的 exact PID 做有界 wait/inspect，必须观察到 live stopped state；PID、Darwin birth identity、`pid == pgid`、session/process-group ownership必须与 SETEXEC 前完全一致。随后从该 suspended live PID 观察 actual executable path/CDHash，并 nofollow 重开 current runtime path，对 device/inode/type/uid/gid/mode/link-count/size/raw SHA-256/CDHash 与冻结 identity逐项重验；cwd、held closure、vnode guard与 exact set digest也必须仍 current。
3. 只有上述核验全部通过，Core 才能把带 `darwin-posix-spawn-setexec/v1` observer/mechanics identity 的 observation锚到 exact v2 `spawn` outcome并提交既有 `process-started`。stopped但 actual image不是冻结 runtime、同 PID 不同 birth、PGID/session漂移、CDHash/path/object不符、SIGTRAP/exit/signaled/unknown observation或无法证明用户态尚未放行，均零 `process-started`、零 `SIGCONT`。
4. v2 `resume` 必须绑定 current `process-started` fact、exact successful v2 spawn outcome、相同 suspended PID/birth/PGID/path/CDHash与 current authority head；相邻重验后只发送一次 `SIGCONT`。禁止 `PtraceDetach`。same command ID/same digest 重放只返回同一 outcome；response loss由 Supervisor journal观察同一 child state收敛，不能再次产生业务事实。
5. `SIGCONT` 成功发送不等于业务 `RUNNING`。Supervisor 必须观察同一 runtime 已离开 suspended barrier、身份未漂移，才返回 `disposition=ok, reason=process-resumed, state=running`；Core 耐久接纳该 exact outcome 后，既有 sealed proof 才能授权 `READY → RUNNING`。
6. `process-started` 前 live Core/Supervisor 仍持有 exact child/wait mechanics时，可以按既有 launch-error合同有界 terminate+wait；Core/Supervisor crash、identity unknown或跨 owner pending仍按 ADR 0059/0067进入 intervention或严格 no-effect分支。本文不扩大 PID/PGID kill authority。

### 5. pathname TOCTOU 的诚实边界

`POSIX_SPAWN_SETEXEC` 仍消费 `RuntimeExecutableV1.canonicalPath`。held runtime FD、调用前 current-path重验、START_SUSPENDED与装载后 live-process重验共同保证“只有观察到冻结 image 的 suspended process才可被 Marshal放行”，但不能证明 pathname从最后一次检查到内核加载之间从未被同 UID主体替换，也不能把 pathname load描述为 fd-exec或 immutable handle execution。

因此：

- 两端核验缺一不可；只比较路径、只比较 CDHash、只比较 held FD或只比较 post-load PID均不得接纳；
- 任一 drift/ABA/不确定性必须在 `SIGCONT` 前 fail closed，并保持停止、终结或 intervention的既有安全顺序；
- 该机制不提高 `darwin-local-dogfood` 的 assurance，普通宿主进程仍不是恶意代码沙箱；需要 hostile same-UID保证时仍必须转向 Container/VM/Remote hardened Provider及独立 `ConformanceEvidence`；
- 本 ADR 不得被引用为 Issue #212 signing/notarization、anti-rollback、安装 receipt或 stable release的关闭证据。

### 6. protocol/observer identity 与 v1 只读迁移

新 mechanics 不得复用旧 identity。新 producer 固定使用：

```text
marshal-sandbox-launch/v2
process-supervisor/v2
darwin-posix-spawn-setexec/v1
```

`process-supervisor/v2` 保留 ADR 0059/0060/0061/0067/0071 的 command集合、上限、hash-chain、prepared intent/outcome、Attach与 transcript disposition，但 v2 bootstrap、spawn outcome、`process-started`、resume outcome、Attach observation与 close chain都必须绑定上述 sandbox-launch和observer/mechanics identity。任一 v1/v2 identity、request、receipt、journal head或业务 fact交叉引用均 fail closed。

`process-supervisor/v1` journal、facts与 historical projections只允许 decode、verify、status/explain和审计读取：

- 新 binary不得向 v1 journal追加 command、Attach/rebind、resume、terminate、collect或close；
- 不得把 v1 journal原地升级、重写为v2，或在一个session/hash chain内混写v1/v2 frame；
- rollout前必须证明没有 active/pending v1 session。仍活的v1 Attempt只能由其冻结的旧fixed bytes在切换前安全drain，或进入typed intervention；新v2 binary不能adopt；
- historical Run、RC1 receipt、journal与Outcome逐字节保留，不回写“已使用v2”或重算旧digest。

### 7. fail-closed、timeout 与 terminal cleanup

所有等待必须服从现有 command/Attempt budget并具备独立有界阶段：ready、release、SETEXEC后stopped observation、post-load identity recheck、resume observation、wait/terminal cleanup。任一 deadline到期都不能把“未观察到”解释为成功或absence。

- release前 timeout：helper未获 workload授权；按既有 held child mechanics收口；
- release后、stopped observation前 timeout或early exit：不得提交`process-started`；exact mechanics能证明时terminate+wait，否则intervention；
- stopped后、`process-started`前 drift：零`SIGCONT`，按精确身份与既有authority决定cleanup/intervention；
- `process-started`后、resume前 timeout/crash：只允许exact v2 replay/Attach后一次resume，或走既有terminalization；
- resume后 early exit、non-zero、signal、cancel或wall-timeout：沿既有collect/admission/barrier/process-terminal/allocation/close/cleanup链，不新增快捷终点；
- cleanup信号前继续要求exact PID/birth/PGID/runtime/cwd/current terminalization barrier与cleanup binding。unknown identity固定零kill、零release、零successor。

## 必须通过的实现与验证矩阵

| 类别 | 必须证明 |
| --- | --- |
| Darwin build | `CGO_ENABLED=0` 下 `darwin/arm64` 与 `darwin/amd64` 都能构建 fixed `cmd/marshal`；产物不含CGo依赖、临时helper或第二trusted binary；libSystem bridge的架构ABI与错误映射有静态/单元门禁 |
| 真实fixed binary | 每个宣称受支持的Darwin架构都必须以最终fixed `bin/marshal`执行`__sandbox-launch`集成；不得用`go test`临时Mach-O、复制/重命名binary或脚本替代；至少在macOS 26.6.2以signed/hardened Node复现旧SIGTRAP失败并证明v2正常继续 |
| stop barrier | SETEXEC前后PID/birth/PGID保持；runtime actual path/CDHash/object精确相等；Provider sentinel在`SIGCONT`前不存在、`SIGCONT`后才可出现；long-lived server/Supervisor SETEXEC call count固定为零 |
| hostile source | runtime/material/cwd symlink、hardlink、rename、swap/ABA、mode/owner/link-count/size/SHA/CDHash漂移，以及precheck后到kernel pathname load窗口的并发替换；结论只能是正确exact image被放行或在放行前拒绝，不能宣称消除TOCTOU |
| protocol migration | v1 bytes exact只读replay；v1 append/Attach/adopt拒绝；v1/v2 frame、observer、outcome、fact混用拒绝；active/pending v1阻断cutover |
| timeout/early exit | ready、release、stopped await、identity recheck、resume与terminal wait各阶段timeout；`posix_spawn`返回错误/意外返回、helper/runtime early exit、signaled、unexpected SIGTRAP；零伪造`process-started`/`running` |
| identity drift | PID reuse、同PID不同birth、PGID/session变化、actual runtime path/CDHash/object变化、cwd/allocation/held-set/vnode guard漂移；全部在`SIGCONT`前fail closed，未知目标零signal |
| response loss/restart | spawn/stopped observation、`process-started`、SIGCONT、resume outcome各边界crash/response-loss与连续两次Core restart；same request exactly once、different digest conflict、无第二child/第二resume |
| stop/cleanup | stopped pre-resume cancel、running TERM→wait→KILL、natural exit、collect、close、Supervisor absence、cleanup-completed/release顺序；所有signal绑定exact terminalization/cleanup authority，unknown保持intervention |
| 上层不变 | Run/Attempt状态名、ResultIngress admission、sealed proof、terminalization、worktree release、Verification/Review/Outcome normalized trace除新增mechanics identity外保持等价；legacy selector/direct Adapter/processcontrol bypass为零 |
| 秘密与发布 | journal/event/error/log中raw argv、environment、stdin、nonce、token、transcript为零；#212 signing/notarization、Linux stable与受保护stable candidate门禁仍开放 |

Darwin arm64/amd64 compile-only不能替代对应架构的真实fixed-binary integration；某架构缺少真实host证据时只能保持unsupported/`COMPONENT`。包级Fake、匿名原型或单独bridge测试不能升级`INTEGRATED`。

## 实施、切换与回滚策略

1. `S0 contract`：只接受本文并冻结取代范围、identity、矩阵与回滚；不改代码、不升级成熟度。
2. `S1 dormant mechanics`：实现双架构、`CGO_ENABLED=0` bridge及纯mechanics负测，但producer保持disabled；不得生成v2 authority fact或启用production selector。
3. `S2 v2 closed protocol`：加入v2 schema/projection/read-only v1 decoder和混用拒绝；以fixed binary完成hostile/timeout/early-exit/cleanup测试，仍保持`COMPONENT`。
4. `S3 exact production integration`：只有在零active/pending v1 session、真实fixed binary、signed/hardened Node、server/restart/response-loss与上表矩阵通过后，新Attempt producer才切到v2。切换是new-session-only，不迁移既有session。
5. 切换前回滚只需移除disabled candidate。切换后若v2出现问题，rollback固定为**停止接纳新的Darwin launch并typed fail closed**，不是回退ptrace。active v2 session必须由其冻结的exact v2 fixed bytes安全drain；不能drain时进入intervention，禁止旧binary/v1 adopt。
6. 只有证明active/pending v2 session为零、全部terminal/cleanup facts已耐久且owner无pending command时，才允许部署不生产v2的新版本。不得把v2 journal改写为v1、不得以旧binary继续v2、不得为rollback复活`PT_TRACE_ME`路径。受影响的signed/hardened Node在v1下保持unavailable。
7. 回滚不删除authority/history，不回写RC1，不改变已发布tag/receipt，也不关闭Issue #212。重新启用必须创建新的fixed candidate，重跑完整矩阵并产生new-session v2证据。

## 后果

正面结果是保留 fixed Marshal、per-Attempt Supervisor、双阶段业务authority与全部恢复/终结链，只替换在macOS 26.6.2上已证明不兼容signed/hardened Node的ptrace mechanics。SETEXEC让runtime沿同一PID/birth/PGID链被观察，START_SUSPENDED让Core可以在Provider用户态前完成第二端身份门禁，SIGCONT不再承担ptrace detach语义。

代价是增加Darwin arm64/amd64的libSystem ABI bridge、一次明确的Supervisor protocol/journal代际切换，以及切换期间必须清空v1 active session。pathname TOCTOU、ordinary-user同UID边界、Supervisor自身崩溃intervention与完整release gate均保持；本ADR即使被接受，也不表示代码已实现、fixed server T1/T2已完成、I186成熟度升级、Issue #212关闭或stable发布获得授权。

## 拒绝的替代方案

### 捕获或忽略 dyld `SIGTRAP` 后继续 PtraceDetach

拒绝。实机失败说明该事件不再是Marshal可稳定消费的普通exec-stop；把signal disposition调成“碰巧继续”会把签名runtime、dyld与内核版本差异变成未版本化authority。

### 让 server 或 Supervisor 自己 SETEXEC

拒绝。SETEXEC会替换调用进程；长寿命owner或mechanics owner一旦被替换，现有authority、socket、journal、wait与多Attempt控制都会消失。

### 用普通 posix_spawn 创建新的 runtime PID

拒绝。它会把helper与runtime分成两个birth/PGID链，扩大wait right、pipe、cleanup与recovery合同。本决策只接受SETEXEC保持同PID的窄替换。

### 把 START_SUSPENDED 当作消除pathname TOCTOU或hardened证明

拒绝。它提供的是放行前post-load observation，不是fd-exec、不可变pathname或hostile same-UID containment。

### 继续向process-supervisor/v1 journal写入新mechanics

拒绝。相同protocol identity出现两种stop/resume语义会使response-loss、Attach、hash-chain与历史审计无法判定。
