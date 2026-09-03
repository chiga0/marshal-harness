# ADR 0079：Darwin `posix_spawn` SETEXEC 启动屏障与 Supervisor v2

| 字段 | 值 |
| --- | --- |
| 状态 | 已接受（Accepted） |
| 日期 | 2026-09-04 |
| 提议基线 | `origin/main@c2198e3628f38b126402cf7ab153120ea25e3d77` |
| 接受基线 | `main@9f5f16688a4c2f24f0611bd5de6e68b8914a5610`；接受只冻结合同，不表示实现、真实 canary、Issue #212 或 stable gate 已完成 |
| 决定者 | 维护者 |
| 关联 ADR | [ADR 0051](0051-darwin-local-dogfood-profile.md)、[ADR 0056](0056-darwin-process-observation-and-attempt-terminalization.md)、[ADR 0058](0058-interpreted-agent-launch-identity.md)、[ADR 0059](0059-fixed-darwin-process-supervisor.md)、[ADR 0060](0060-supervisor-mechanics-authority-binding-and-recovery.md)、[ADR 0062](0062-fixed-marshal-production-server-mode.md)、[ADR 0064](0064-darwin-control-directory-phased-identity.md)、[ADR 0067](0067-darwin-ordinary-user-launch-and-attach-recovery.md)、[ADR 0071](0071-darwin-sealed-completion-and-durable-result-capability.md)、[Issue #212](https://github.com/chiga0/marshal-harness/issues/212) |
| 关联范围 | Darwin ordinary-user 的最终 runtime exec barrier；不改变上层 lifecycle、ResultIngress、terminalization、server 或发布权限 |

## 背景

ADR 0058/0059 当前在 fixed Marshal 的 `internal process-supervisor` inherited launch-child mode 中使用以下 Darwin 启动屏障：`PT_TRACE_ME → exec(runtime) → SIGTRAP exec-stop → PtraceDetach`。该 mode 由 `internal/processsupervisor/runLaunchChild` 执行；父 Supervisor 借此在真实 Node 映像已经装载、Provider 用户态入口尚未运行时观察 PID、birth、PGID、runtime/cwd identity，提交 `process-started` 后再放行。

2026-09-04 的 macOS 26.6.2 实机回归证明，这个 mechanics 对 signed/hardened Node 不再可用：`PT_TRACE_ME` 后的 exec 在 dyld 边界产生 `SIGTRAP`，随后 `PtraceDetach` 不能把该事件安全转换为普通继续执行，真实 Node 以 `SIGTRAP` 终止。该失败发生在既有 authority、ResultIngress 与业务生命周期之外；继续修改 retry、terminalization 或 server 都不能修复它。

同机原型还证明一条更窄的替代路径可行：`CGO_ENABLED=0` 的固定 Marshal 可以通过编译进同一二进制的 libSystem ABI bridge 调用 `posix_spawn`，组合 `POSIX_SPAWN_SETEXEC | POSIX_SPAWN_START_SUSPENDED` 后，inherited launch child 在**同一 PID**中被 runtime 映像替换，runtime 在进入 Provider 用户态前保持 stopped；父 Supervisor 完成身份观察后发送 `SIGCONT`，signed/hardened Node 正常继续执行。

该原型只证明机制可行，不构成仓库实现、生产集成、conformance、签名或发布证据。尤其是 `posix_spawn` 仍按 pathname 选择 runtime：它没有把 Darwin ordinary-user 启动变成 fd-exec，也没有消除 pathname TOCTOU。

## 决定

### 1. 适用范围与精确取代

1. 若本 ADR 被接受，它只适用于 `darwin-local-dogfood` 的 trusted、single-user、ordinary-user、`workspace-write` 边界，以及未来使用同一 mechanics 的受管 Darwin profile。它不提供 hostile same-UID 防护、hardened Sandbox assurance、跨用户隔离、Linux process authority 或发布 authority。
2. 本 ADR 部分取代 ADR 0058 §5 以及 ADR 0059 §3/§6/§8 中依赖 `PT_TRACE_ME`、`SIGTRAP exec-stop` 与 `PtraceDetach` 的最终 exec barrier、stopped await 与 resume mechanics；新生产路径禁止这三个 primitive。同时，对新 v2 session，本 ADR 部分取代 ADR 0059 §4/§5/§7 的 v1 wire、journal identity、genesis、hard limit 与 projection 合同，以本文第 6–8 节为准；ADR 0059 的历史说明和 v1 bytes 不改写。
3. 本 ADR 对 ADR 0060 §2/§3/§5/§6 及 ADR 0067 §2 第 6 项、§4/§5/§7 中受 Supervisor protocol/journal 代际影响的 bootstrap、command recovery、`Attach`、intervention 和 authority projection 合同作部分取代：新 session 只能生产本文冻结的 v2 证据，v1 只读。ADR 0060/0067 的 owner、replay、pending-command、no-effect/intervention 二分与业务 authority 顺序不变。
4. 本 ADR 对 ADR 0064 §1 第 4 项、§4–§6 及「最多六个 entry」的新生产 session 合同作部分取代：v2 必须使用本文第 7 节的新 journal 名与阶段闭集。ADR 0064 的 initial-empty、stable directory fields、object identity、descriptor-relative enumeration、collect 单调前缀和 close disposition 逐项保留；v1 目录仍按旧合同只读验证。
5. 功能代码面的变更限于 `internal/processsupervisor/runLaunchChild` 的最终 runtime 替换屏障、同一 `internal/processsupervisor` Darwin mechanics 的 stopped observation/await/resume，以及隔离新旧 mechanics 必需的 v2 wire/journal/projection/decoder 与测试接线。禁止把 production path 迁到 `internal/sandboxlaunch`、`internal/processcontrol`、`__sandbox-launch` 或第二个 process coordinator；不得借此改写 Core authority、Run/Attempt 状态机、ResultIngress、terminalization、cleanup、review、publication 或 fixed server transport。
6. 以下既有合同逐项保留：fixed `cmd/marshal` 以 `internal process-supervisor` hidden dispatch 运行；inherited launch-child mode 的 spec/ready/release pre-exec handshake；held FD、vnode guard 与 mutation-adjacent source precheck；`launch-authorized → process-started → exact resume outcome`；suspended live PID/path/CDHash/birth/PGID/current-authority recheck；ResultIngress 与 terminalization 的同一 CAS barrier；`process-terminal → allocation-terminated → process-supervisor-closed → cleanup-completed → lease-released`；未知状态 fail closed。
7. 上层状态名和业务语义保持不变。v2 `spawn` 成功仍投影为 `exec-stopped`，v2 `resume` 成功仍投影为 `running`；`READY → RUNNING` 仍只允许在 exact authenticated resume outcome 耐久后发生。本文不新增 Run 状态、Attempt 终点或自动 retry 类型。

### 2. 唯一允许调用 SETEXEC 的进程

1. 当前 accepted production topology 冻结为：fixed `cmd/marshal` 以 argv `internal process-supervisor` 启动 per-Attempt Supervisor；该 Supervisor 再以同一 canonical fixed Marshal path、同一 argv 启动继承 child spec/ready/release/cwd/runtime/marshal/material FD 的短寿命 child；inherited invocation discriminator 选中 child mode 并进入 `internal/processsupervisor/runLaunchChild`。
2. `POSIX_SPAWN_SETEXEC` 的唯一 production caller 必须是这个 fixed Marshal inherited child mode 内的 `runLaunchChild`。成功调用会替换调用进程本身，因此该 launch child 的 PID、birth 与 process-group leader identity 在映像替换前后保持同一条观察链。
3. 长寿命 `marshal control-plane serve`、CLI owner、per-Attempt Supervisor server loop、`internal/sandboxlaunch`、`internal/processcontrol`、`__sandbox-launch` 与任何通用库初始化路径都不得调用 SETEXEC。否则会把 Control Plane 或 mechanics owner 自身替换成 Agent runtime，丢失 owner、socket、journal、wait right 与多 Attempt 服务能力。architecture/call-graph test 必须证明 bridge 只有 `runLaunchChild` 一个 production callsite，且 production graph 中 `__sandbox-launch`/`processcontrol` 调用计数为零。
4. bridge 必须是 fixed Marshal 内、Darwin arm64/amd64 分架构审核的最小 libSystem ABI bridge；构建保持 `CGO_ENABLED=0`。禁止 CGo、`go run`、临时/匿名 Mach-O、脚本/shebang wrapper、额外 helper executable、可下载 dylib、PATH 查找或第二 trusted binary。
5. bridge 只调用宿主 libSystem 的 `posix_spawnattr_init`、`posix_spawnattr_setflags`、`posix_spawn` 与必需的销毁/错误处理表面。symbol、ABI、flag 或调用约定不能精确满足时，在 workload 用户态执行前返回 closed typed unavailable；不得回退到 ptrace、`syscall.Exec`、fork/exec、shell 或匿名 helper。

### 3. 保留 pre-exec 握手并替换最终 barrier

`internal/processsupervisor/runLaunchChild` 的顺序固定为：

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

ready 只表示 inherited launch child 已验证 inherited protocol 并等待 release，不表示 runtime 已装载。release 只授权执行最后一次 current-object recheck 与一次 SETEXEC 调用，不授权 workload 运行。`posix_spawn` 返回非零、意外从 success path 返回、attribute 初始化/设置/销毁不一致或 child 在 runtime stopped observation 前退出，均固定为 launch failure/intervention；不得伪造 `exec-stopped`。

不得把 ready/release 合并成一个信号，也不得把 `posix_spawn` 提前到 release 之前。raw argv、environment、stdin、nonce 与 transcript 继续只经既有 inherited FD/认证协议有界传输，不能进入 argv 诊断、authority journal、mechanics journal、event、error 或普通 log。

### 4. v2 stopped observation、双端身份核验与 resume

父 `internal process-supervisor` 必须对初始 inherited launch child 与 start-suspended runtime 执行双端核验：

1. **调用前一端**：以现有 held FD、nofollow current-path reopen、hash/CDHash、vnode guard、cwd/allocation live identity、完整 materials set 与 `launch-authorized`/`StoredClosureV1` 重验，证明本次请求只能尝试冻结的 absolute runtime pathname、argv 与 environment。
2. **装载后一端**：父 Supervisor 对原 inherited launch child 的 exact PID 做有界 wait/inspect，必须观察到 live stopped state；PID、Darwin birth identity、`pid == pgid`、session/process-group ownership必须与 SETEXEC 前完全一致。随后从该 suspended live PID 观察 actual executable path/CDHash，并 nofollow 重开 current runtime path，对 device/inode/type/uid/gid/mode/link-count/size/raw SHA-256/CDHash 与冻结 identity逐项重验；cwd、held closure、vnode guard与 exact set digest也必须仍 current。
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

### 6. v2 identity、wire 与 canonical JSON 闭集

新 mechanics 不得复用任何 v1 identity。新 producer 冻结以下 exact ID：

| 层次 | v2 exact ID |
| --- | --- |
| Supervisor protocol | `process-supervisor/v2` |
| bootstrap schema | `marshal.process-supervisor-bootstrap.v2` |
| reconnect schema | `marshal.process-supervisor-reconnect.v2` |
| handshake schema | `marshal.process-supervisor-handshake.v2` |
| command request schema | `marshal.process-supervisor-request.v2` |
| command response schema | `marshal.process-supervisor-response.v2` |
| mechanics journal schema | `marshal.process-supervisor-journal.v2` |
| inherited launch-child protocol | `process-supervisor-launch-child/v2` |
| inherited child-spec schema | `marshal.process-supervisor-launch-child.v2` |
| Supervisor observer | `darwin-fixed-process-supervisor/v2` |
| exec mechanics | `darwin-posix-spawn-setexec/v1` |
| RB1 command recovery subchain | `process-supervisor-command-recovery/v2` |

v2 command 闭集逐项沿用且只允许 `bind-authority, abort-unbound, spawn, resume, inspect, terminate, collect, close`；`ReconciliationState` 只允许 `unchanged, exact-intent-pending, exact-receipt-committed`。增加 command、state 或 reason 语义必须再升级 protocol，不能在 v2 中通过宽松 decoder 引入。

v2 wire 仍使用 strict RFC 8785 JCS、I-JSON safe integer、UTF-8、closed decoder 和 exact digest；unknown、duplicate、缺失必需字段、非 canonical bytes、wrong type 或 `null` 冒充缺省均拒绝。以下列表冻结顶层字段闭集；canonical bytes 仍由 JCS 对 object key 排序：

| v2 object | exact canonical JSON fields |
| --- | --- |
| bootstrap | `schemaVersion, protocolRevision, launchChildProtocolRevision, mechanicsIdentity, sessionId, sessionNonce, ownerEpoch, authority, launchAuthorizedFactDigest, currentAuthorityHead, controlDirectoryIdentity, core` |
| reconnect | `schemaVersion, protocolRevision, launchChildProtocolRevision, mechanicsIdentity, sessionId, sessionNonce, previousOwnerEpoch, ownerEpoch, previousAuthorityHead, currentAuthorityHead, controlOwnerAcquiredFactDigest, core, lastOwnerEpoch, lastAuthorityHead, lastCommandSequence, lastCommandHead, lastJournalSequence, lastJournalHead, pendingRequest`；无 pending 时只能省略 `pendingRequest` |
| handshake | `schemaVersion, protocolRevision, launchChildProtocolRevision, mechanicsIdentity, status, reasonCode, sessionId, sessionNonceDigest, ownerEpoch, currentAuthorityHead, commandSequence, commandHead, journalSequence, journalHead, observerIdentity, observedAt, supervisorProcess, supervisorBinary, controlSocket, controlFiles, reconciliation, replayedResponse`；v2 必须携带 `controlFiles`；无 recovery 时省略 `reconciliation`，无 durable replay receipt 时省略 `replayedResponse` |
| request | `schemaVersion, protocolRevision, launchChildProtocolRevision, mechanicsIdentity, sessionId, command, commandId, sequence, previousCommandDigest, currentAuthorityHead, requestDigest, deadline, payload` |
| response | `schemaVersion, protocolRevision, launchChildProtocolRevision, mechanicsIdentity, sessionId, command, commandId, sequence, requestDigest, status, reasonCode, receiptDigest, observationDigest, commandHead, payload` |
| inherited child spec | `schemaVersion, protocolRevision, launchChildProtocolRevision, mechanicsIdentity, parentPid, runtime, workingDirectory, marshal, materialRoots, launchMaterials, argv, environment` |
| journal record | `schemaVersion, protocolRevision, launchChildProtocolRevision, mechanicsIdentity, journalSequence, kind, sessionId, sessionNonceDigest, authority, ownerEpoch, currentAuthorityHead, request, response, previousRecordDigest, recordDigest`；`request/response` 只按下节 kind 矩阵省略 |

v2 明确逐项沿用下列 v1 nested object/payload 字段，不得以「结构大致相同」补充未列字段：

- `authority = authorityNamespaceId, taskId, runId, attemptId, allocationId, leaseId, leaseDigest, generation, fencingTokenDigest, orchestratorId`；`core = uid, gid, process, binary`；`process = pid, birthSeconds, birthMicroseconds, sessionId, processGroupId`。
- `binary = canonicalPath, device, inode, fileType, uid, gid, mode, linkCount, size, rawSHA256, cdHash, sourceHead, selfProfile`；`controlDirectoryIdentity = canonicalPath, device, inode, fileType, uid, gid, mode, linkCount`；control socket/file 都为 `device, inode, fileType, uid, gid, mode, linkCount`；`controlFiles = nonce, journal`。
- `childObject = fd, object`；`heldObject = role, canonicalPath, device, inode, fileType, uid, gid, mode, linkCount, size, rawSHA256`；`allocationLiveIdentity = device, inode, fileType, uid, gid, mode, linkCount, size`。
- `bind-authority = supervisorStartedFactDigest, ownerEpoch, previousAuthorityHead, authorityHead`；`abort-unbound = ownerEpoch, previousAuthorityHead, authorityAbsenceProofDigest`；`resume = processStartedFactDigest`；`collect = processStartedFactDigest, lastObservationDigest`；`close = processTerminalFactDigest, allocationTerminatedFactDigest, cleanupBindingDigest`；`inspect/terminate = terminalizationBarrierDigest, terminalizationId, terminalGeneration, cleanupBindingDigest, processStartedFactDigest, lastObservationDigest`。
- `spawn = launchAuthorizedFactDigest, supervisorStartedFactDigest, runtime, workingDirectory, sourceGateRevision, allocationLiveIdentity, closureProfileId, materialRoots, launchMaterials, launchMaterialsDigest, agentLaunchSpecDigest, argvDigest, environmentDigest, stdinDigest, environmentKeys, argv, environment, stdin`。`sourceGateRevision` 只允许既有 `darwin-source-gate/v1`；`materialRoot = name, canonicalPath, packageRelative, object`；`launchMaterial = role, object`；其 `object = canonicalPath, device, inode, fileType, mode, uid, gid, size, linkCount, rawSha256`。
- journal `request = command, commandId, sequence, requestDigest, previousCommandDigest, currentAuthorityHead, nextAuthorityHead, supervisorStartedFactDigest, authorityAbsenceProofDigest, deadline, launchMaterialsDigest, agentLaunchSpecDigest, sourceGateRevision, closureProfileId, argvDigest, environmentDigest, stdinDigest, environmentKeys, processStartedFactDigest, terminalizationBarrierDigest, terminalizationId, terminalGeneration, cleanupBindingDigest, lastObservationDigest, processTerminalFactDigest, allocationTerminatedFactDigest`；optional 字段只能按 command 闭集出现。
- `mechanicsResult = disposition, reasonCode, observationDigest, transcriptDigest, stdoutBytes, stderrBytes, truncated, payload`；`processReport = state, observerIdentity, observedAt, process, runtimeObjectDigest, workingObjectDigest, sourceGateRevision, exactSetDigest, exitCode, signal, stdoutDigest, stderrDigest, stdoutBytes, stderrBytes, transcriptTruncated`。新 v2 report 中 `observerIdentity` 必须是上表 exact v2 值，`sourceGateRevision` 和 `exactSetDigest` 必须存在。

`requestDigest` 的 preimage 是 request 除 `requestDigest` 外的全部 exact v2 字段；`receiptDigest`、`observationDigest`、`commandHead` 和 `recordDigest` 必须把 v2 schema/protocol/launch-child/mechanics identity 一并纳入 canonical preimage。任一绑定不能从上下文默认推导，也不得在验证后丢弃再生产业务投影。

### 7. v2 journal、genesis 与 control-directory 阶段

v2 mechanics journal 只能命名为 `process-supervisor-v2.journal`。它逐项沿用 ADR 0059 的 `8 lowercase hex length + ':' + exact JCS payload + LF`、append-only、intent-before-effect `fsync`、receipt-before-response `fsync`、held-parent sync、partial-tail 唯一可截断条件，以及 gap/fork/duplicate/trailing-garbage intervention 规则。v2 冻结：

```text
CommandGenesisDigest = sha256("marshal/process-supervisor-command/v2\x00genesis")
                     = sha256:d2b74e69e8f7dc7d2f7718a9a1e3691dd2c32e295cd0a3a3f73daee769306ee9
JournalGenesisDigest = sha256("marshal/process-supervisor-journal/v2\x00genesis")
                     = sha256:24d02077bdcae6909a74214a4c722b0512c26ad001a610823e336fb592459dee
```

`journalSequence` 从 `1` 开始，第一帧必须是 `kind=session-created`，`previousRecordDigest` 必须为 v2 journal genesis，`request`/`response` 均缺省；`command-intent` 必须只有 request，`command-receipt` 必须同时有同一 request 与 exact response。首个 command 的 `sequence=1`、`previousCommandDigest` 必须为 v2 command genesis。

v2 逐项沿用 ADR 0064 的目录稳定字段 `canonicalPath, device, inode, fileType, uid, gid, mode`、initial `linkCount` 精确相等、runtime 阶段 `linkCount` 只作观察、descriptor-relative independent enumeration 与全部对象级门禁。阶段 exact-entry 闭集冻结为：

| v2 阶段 | exact entry set |
| --- | --- |
| bootstrap initial | 空集 |
| final setup、bind/spawn/resume/inspect/terminate、collect intent 前 | `session.nonce`, `process-supervisor-v2.journal`, `control.sock` |
| collect intent 已耐久、receipt 未闭合 | 基础三项加单调前缀：空、`stdout.bin`、`stdout.bin + stderr.bin`、`stdout.bin + stderr.bin + transcript.jcs` |
| successful collect receipt 后 | 基础三项加 `stdout.bin`, `stderr.bin`, `transcript.jcs`，恰好六项 |
| rejected/unknown collect | 只能依 exact pending/outcome 进入 intervention，不得提升为 collected |
| close/offline recovery | 依 ADR 0061 耐久 disposition 精确选择基础三项或完整六项 |

v2 目录中出现 `process-supervisor-v1.journal`、v1 目录中出现 `process-supervisor-v2.journal`、同时出现两者、任一未冻结名或与耐久阶段不符均在 mechanics 前 fail closed。nonce、journal、socket 和输出对象的 type/uid/gid/mode/link-count/current-name/held-identity/content 门禁逐项沿用 ADR 0064；不增加 lock、migration marker、backup 或 sidecar entry。

### 8. limits、decoder routing 与 authority/projection 绑定

v2 不放宽 v1 上限，而是逐项冻结为：wire frame `1 MiB`；journal JCS payload `256 KiB`；journal file `64 MiB`；每 session `4096` 条 command；argv `256` 项且 UTF-8 总计 `256 KiB`；environment key `128` 个、单 key `128 B`、raw values 总计 `256 KiB`；stdin `1 MiB`；stdout/stderr 各 `16 MiB`；transcript data object `32 MiB`；单条 diagnostic `64 KiB`；I-JSON integer 最大 `2^53-1`。deadline 上限仍为 `bind-authority/abort-unbound/resume/inspect=30s`、`spawn/terminate/collect/close=2m`。任一上限均包含 v2 新增字段，不得通过配置、分片 frame 或外部 object 绕过。

decoder routing 先依 held control-directory exact-entry phase 选择唯一 journal leaf，再用首帧 schema/protocol/genesis 与已耐久 authority binding 交叉验证：

1. `process-supervisor-v1.journal` 只能进入 exact v1 decoder；它只允许 decode、verify、status/explain 和审计读取，新 binary 对 append、`Attach`、rebind、resume、terminate、collect、close 与 adopt 均拒绝。
2. `process-supervisor-v2.journal` 只能进入 exact v2 decoder/writer；首帧或任一后续帧出现 v1 schema、genesis、observer、recovery fact 或缺失 v2 mechanics binding 均 intervention。
3. 禁止根据「能解析的字段」猜代际、v2 失败后 fallback v1、一次连接中切换 decoder、内存 translate 后追加、同 session/hash chain 混写或原地 rename/rewrite。两个 journal leaf同时存在必须 intervention，不以 mtime/head 选胜。

v2 `process-supervisor-bootstrap-prepared`、`process-supervisor-started`、`process-supervisor-command-intent/outcome`、`control-owner-bound` 后的 `Attach` evidence、`process-started`、collect admission、resume sealed proof、`process-terminal`、`process-supervisor-closed` 与 transcript projection 都必须显式绑定：

```text
processSupervisorProtocolRevision = process-supervisor/v2
journalSchema = marshal.process-supervisor-journal.v2
journalFileName = process-supervisor-v2.journal
commandGenesisDigest = sha256:d2b74e69e8f7dc7d2f7718a9a1e3691dd2c32e295cd0a3a3f73daee769306ee9
journalGenesisDigest = sha256:24d02077bdcae6909a74214a4c722b0512c26ad001a610823e336fb592459dee
launchChildProtocolRevision = process-supervisor-launch-child/v2
mechanicsIdentity = darwin-posix-spawn-setexec/v1
observerIdentity = darwin-fixed-process-supervisor/v2
supervisorCommandRecoveryProtocolRevision = process-supervisor-command-recovery/v2
```

每个 pre/post mechanics anchor 还必须继续绑定 exact session/nonce digest/authority tuple/owner epoch/current authority head/command sequence+head/journal sequence+head/UID+GID/fixed binary/control directory+socket+files。外层 `attempt-authority/v1` 与 `result-ingress/v2` 不因本 ADR 改名，但它们对新 session 携带的 Supervisor subprojection 只能是上述完整 v2 binding；不得用 v1 default/缺省值合成 v2 authority。

版本绑定必须落在产生它的 exact projection 中：bootstrap-prepared 绑定 bootstrap 与 child-spec schema；started 再绑定 handshake 与 journal schema/head；command intent/outcome 分别绑定 request/response schema 与 `process-supervisor-command-recovery/v2`；`Attach` 绑定 reconnect+handshake schema、同一 v2 leaf 与 pre/post anchor；`process-started`/resume/terminal/collect/close 继续引用 exact outcome fact digest 和上述 mechanics/observer identity。任一业务 projection 只保存 journal head 而省略其 schema/genesis，或只保存 observer 而省略 launch-child/mechanics identity，都不构成 v2 authority。

rollout 前必须证明没有 active/pending v1 session。仍活的 v1 Attempt 只能由其冻结的旧 fixed bytes 在切换前安全 drain，或进入 typed intervention；启用 v2 后所有 v1 bytes 均只读，新 v2 binary 不能 adopt。historical Run、RC1 receipt、journal、Outcome 与 v1 recovery facts 逐字节保留，不回写「已使用 v2」、不重算 digest、不往 v1 chain 追加。

### 9. fail-closed、timeout 与 terminal cleanup

所有等待必须服从现有 command/Attempt budget并具备独立有界阶段：ready、release、SETEXEC后stopped observation、post-load identity recheck、resume observation、wait/terminal cleanup。任一 deadline到期都不能把“未观察到”解释为成功或absence。

- release前 timeout：inherited launch child未获 workload授权；按既有 held child mechanics收口；
- release后、stopped observation前 timeout或early exit：不得提交`process-started`；exact mechanics能证明时terminate+wait，否则intervention；
- stopped后、`process-started`前 drift：零`SIGCONT`，按精确身份与既有authority决定cleanup/intervention；
- `process-started`后、resume前 timeout/crash：只允许exact v2 replay/Attach后一次resume，或走既有terminalization；
- resume后 early exit、non-zero、signal、cancel或wall-timeout：沿既有collect/admission/barrier/process-terminal/allocation/close/cleanup链，不新增快捷终点；
- cleanup信号前继续要求exact PID/birth/PGID/runtime/cwd/current terminalization barrier与cleanup binding。unknown identity固定零kill、零release、零successor。

## 必须通过的实现与验证矩阵

| 类别 | 必须证明 |
| --- | --- |
| Darwin build | `CGO_ENABLED=0` 下 `darwin/arm64` 与 `darwin/amd64` 都能构建 fixed `cmd/marshal`；产物不含CGo依赖、临时helper或第二trusted binary；libSystem bridge的架构ABI与错误映射有静态/单元门禁 |
| 真实fixed binary | 每个宣称受支持的Darwin架构都必须以最终fixed `cmd/marshal`执行`internal process-supervisor`集成，并由inherited FD真实进入`runLaunchChild`；不得用`go test`临时Mach-O、复制/重命名binary、`__sandbox-launch`或脚本替代；至少在macOS 26.6.2以signed/hardened Node复现旧SIGTRAP失败并证明v2正常继续 |
| topology/call graph | SETEXEC bridge在 production 中唯一 caller 是 `internal/processsupervisor/runLaunchChild`；`marshal internal process-supervisor → inherited child mode → runLaunchChild` 路径只有一条；long-lived server、Supervisor server loop、`__sandbox-launch`、`internal/sandboxlaunch`、`internal/processcontrol` call count 均为零 |
| stop barrier | SETEXEC前后PID/birth/PGID保持；runtime actual path/CDHash/object精确相等；Provider sentinel在`SIGCONT`前不存在、`SIGCONT`后才可出现；long-lived server/Supervisor SETEXEC call count固定为零 |
| hostile source | runtime/material/cwd symlink、hardlink、rename、swap/ABA、mode/owner/link-count/size/SHA/CDHash漂移，以及precheck后到kernel pathname load窗口的并发替换；结论只能是正确exact image被放行或在放行前拒绝，不能宣称消除TOCTOU |
| protocol migration | v1/v2 exact fixture覆盖全部wire/schema ID、两个genesis、journal filename、阶段entry set、decoder routing、limits与authority binding；v1 bytes只读，v1 append/Attach/adopt拒绝；dual leaf、wrong genesis、schema/observer/recovery fact混用、fallback/translate、active/pending v1 cutover全部拒绝 |
| timeout/early exit | ready、release、stopped await、identity recheck、resume与terminal wait各阶段timeout；`posix_spawn`返回错误/意外返回、launch child/runtime early exit、signaled、unexpected SIGTRAP；零伪造`process-started`/`running` |
| identity drift | PID reuse、同PID不同birth、PGID/session变化、actual runtime path/CDHash/object变化、cwd/allocation/held-set/vnode guard漂移；全部在`SIGCONT`前fail closed，未知目标零signal |
| response loss/restart | spawn/stopped observation、`process-started`、SIGCONT、resume outcome各边界crash/response-loss与连续两次Core restart；same request exactly once、different digest conflict、无第二child/第二resume |
| stop/cleanup | stopped pre-resume cancel、running TERM→wait→KILL、natural exit、collect、close、Supervisor absence、cleanup-completed/release顺序；所有signal绑定exact terminalization/cleanup authority，unknown保持intervention |
| 上层不变 | Run/Attempt状态名、ResultIngress admission、sealed proof、terminalization、worktree release、Verification/Review/Outcome normalized trace除新增mechanics identity外保持等价；legacy selector/direct Adapter、`__sandbox-launch`、`processcontrol` bypass为零 |
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

### 让 server 或 Supervisor server loop 自己 SETEXEC

拒绝。SETEXEC会替换调用进程；长寿命 owner 或 Supervisor mechanics owner 一旦被替换，现有 authority、socket、journal、wait 与多 Attempt 控制都会消失。本文允许的是同一 executable 的 inherited launch-child mode，不是 Supervisor server loop。

### 用普通 posix_spawn 创建新的 runtime PID

拒绝。它会把launch child与runtime分成两个birth/PGID链，扩大wait right、pipe、cleanup与recovery合同。本决策只接受SETEXEC保持同PID的窄替换。

### 把 START_SUSPENDED 当作消除pathname TOCTOU或hardened证明

拒绝。它提供的是放行前post-load observation，不是fd-exec、不可变pathname或hostile same-UID containment。

### 继续向process-supervisor/v1 journal写入新mechanics

拒绝。相同protocol identity出现两种stop/resume语义会使response-loss、Attach、hash-chain与历史审计无法判定。
