# ADR 0057：本地 Allocation 耐久恢复与唯一生产装配

- 状态：已接受（Accepted，2026-08-28；candidate sourceHead `4fe46c96134e5e72f72fb60c1682753bd7a72a41` 经同一独立 reviewer aggregate re-review，P0/P1/P2 均为 0）；接受只冻结合同，不表示实现完成，不升级 I186-R3–R6 的成熟度，也不授权稳定版发布
- 关联：[ADR 0018](0018-control-plane-and-provider-ports.md)（唯一 authority ledger 与 Provider Port）、[ADR 0045](0045-strangler-cutover-and-single-recovery.md)（单一恢复模型与 cutover）、[ADR 0049](0049-location-attestation-and-failure-classification-authority.md)（位置事实分权）、[ADR 0051](0051-darwin-local-dogfood-profile.md)（Darwin ordinary-user 边界）、[ADR 0052](0052-v1-release-scope-and-production-reachability.md)（v1.0 生产可达性）、[ADR 0053](0053-pre-r4-contract-gates-and-single-recovery-model.md)（恢复决策）、[ADR 0055](0055-sandbox-exec-workload-envelope.md)（allocation-carried Exec）、[ADR 0056](0056-darwin-process-observation-and-attempt-terminalization.md)（单 Attempt authority 与精确进程控制）、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)

## 背景

ADR 0056 冻结了 Attempt admission、terminalization、DispatchLease eligibility 与 cleanup binding 共用一个 authority CAS 的合同，也要求 Darwin Local 由 Core 持有精确进程控制单元。当前本地 Sandbox allocation 仍有两个相邻缺口：

1. `LocalRunner` 的 allocation、lease、SideEffectIntent 和 reconcile 记录主要存在内存中；`Terminate` 直接删除目录，进程重启后无法区分「从未执行」「副作用已执行但响应丢失」「目录被外部删除」；
2. CLI、`marshal-server` 与 embedded runtime 仍可各自装配执行路径，server 通过启动子 `marshal task run` 复用实现，环境变量还可以选择 embedded、production gate 或 legacy `Adapter.Run(host)`。这不是一个可证明唯一的 production composition root。

这两个缺口不能只用实现约定修补。Allocation 生命周期的耐久字段、删除前后的 crash 语义、请求重放身份，以及谁可以创建生产 Runtime，都会改变持久化、生命周期与信任边界，必须先冻结 ADR。

## 决策

### 1. 适用范围与权威分层

1. 本合同只覆盖 v1.0 的单节点、单用户、可信仓库与 Darwin `darwin-local-dogfood` ordinary-user profile。它不提供恶意代码隔离、跨用户 containment、hardened assurance、远程 Sandbox authority 或稳定发布身份。
2. ADR 0056 的单一 Attempt authority store/CAS 仍是业务权威。ResultIngress admission、launch/terminalization、DispatchLease eligibility、current Run authority、cleanup binding、unlock 与 successor 的判定都只能来自该 store。
3. Provision/Terminate 的 `SideEffectIntentV1`、`SideEffectReceiptV1` 与 `ReconcileDecisionV1` 必须是该单一 Attempt authority store 内 closed/versioned 的 authority subledger facts，并沿既有 `intent → receipt → reconcile` 状态机提交。只有已耐久提交且仍 current 的 exact intent 才授权一次对应外部副作用；receipt 是 Core 接纳的外部观察，reconcile decision 只能由 Core 依据当前 authority 与 receipt 产生。
4. 本文新增 `AllocationRecoveryJournalV1` 只是 Core-owned、authority-scoped 的 **Provider 文件系统恢复 projection**。它只能从已提交的 authority subledger facts 重建，不能创建 intent、授权副作用、创建 Attempt、终止 eligibility、签发 cleanup binding、宣布 Run terminal 或推进 lifecycle。projection 缺失或落后时，Core 必须先从 authority facts 重建并重验文件系统；无法重建、损坏或与 authority 冲突时 fail closed，不得反向用 projection 修补或覆盖 authority。
5. authority intent 的 admission 必须在同一 authority transaction/CAS 中完成 current Run、exact Attempt sequence、request digest、command/idempotency conflict 与生命周期条件检查，并 compare-and-append 后 fsync authority subledger；禁止 `check → 解锁 → append`。Provision 还要求 DispatchLease eligibility 为 active、无 terminalization/recovery barrier；Terminate 要求 ADR 0056 的 terminal cleanup phase、仍 current 的 `cleanupBinding` 与已提交的安全进程终点。transaction 只有在 intent 已耐久且为该 scope 建立排斥冲突 mutation 的 pending-effect barrier 后才释放。
6. Provider 每次 mutation 前必须重新取得上述已提交 intent 并匹配当前 Run authority、exact tuple 与 pending-effect barrier；journal record、Provider map、路径存在性、Run snapshot、PID、环境变量或调用者声明均不能授权副作用。receipt/reconcile 提交或 intervention 关闭该 barrier；不得在同一 scope 并行 admission 第二个冲突 effect。

### 2. Allocation recovery journal

`AllocationRecoveryJournalV1` 使用 append-only RFC 8785 JCS 记录，并形成单调 canonical hash-chain。每个 frame 固定为：8 个 ASCII 小写十六进制数字（payload byte length，左侧补 `0`）、单字节 `:`、精确的 JCS payload bytes、单字节 LF；长度受固定上限约束，payload 内不得包含 JCS 对象之外的尾随内容。每条记录至少包含：

- `schemaVersion`、`journalSequence`、`recordKind`、`recordId` 与 `recordedAt`；
- `authorityNamespaceId`、`taskId`、`runId`、`attemptId`、`allocationId`；
- `leaseId`、`generation`、`fencingTokenDigest`、`commandId`、`idempotencyKey`；
- `expectedAttemptSequence`、`attemptAuthorityFactDigest`；
- Terminate 路径上的 `terminalizationId`、`cleanupBindingDigest`、`processTerminalFactDigest`；
- `requestDigest`、前一条记录的 `previousRecordDigest` 与自身 `recordDigest`。

冻结规则：

1. `journalSequence` 从 `1` 开始且每个完整 frame 精确递增 `1`。`recordDigest = sha256(JCS(recordWithoutRecordDigest))`；第一条记录使用封闭 genesis digest，此后 `previousRecordDigest` 必须精确等于上一条完整记录的 `recordDigest`。
2. 解析器先读满 8 个小写十六进制长度 bytes 与 `:`，再读满声明长度的 payload 和唯一终止 `\n`，之后才解析 strict JCS。重复/未知字段、payload 尾随 bytes、非法时间、超长 frame、sequence 非连续、非 canonical digest、断链、同 `recordId` 不同内容或任何完整 frame 内容错误都固定为 corruption。
3. 只有 EOF 落在最后一个 frame 的长度 header、payload 或终止 LF 内、已读取 header bytes 都是上述 framing grammar 的合法前缀，且此前所有 frame 完整有效时，该 EOF suffix 才是可截去的 partial tail；截到上一完整 frame 后必须 fsync journal。完整 frame 后出现不构成下一个合法 frame 前缀的 bytes 是 trailing garbage，不能按 partial tail 忽略；中间截断、完整但非法的末帧、断链与 trailing garbage 都阻断该 authority scope 的新副作用并进入 intervention。
4. 同 `(authorityNamespaceId, commandId, requestDigest)` 同内容重放返回既有结果，零追加、零文件系统副作用；同 command 或 idempotency key 携带不同 request digest 固定 conflict。
5. journal 文件、allocation parent directory 与 tombstone parent directory 必须由 Core 以 owner-only 权限、nofollow held directory descriptor 打开。journal 首次创建后必须先 fsync 文件并 fsync 其 held parent directory，才可追加第一个 frame。路径字符串只作诊断；所有创建、stat、rename 与 fsync 都相对已验证 descriptor 执行。
6. journal 与 authority store 可以物理分文件，但 journal frame 必须逐项投影已提交 authority fact，并回显该 fact digest/sequence；journal 自身不能使未提交 intent 生效。只有一个 `ProductionRuntime` 可以打开和串行化两者，任何实现若无法证明 authority fact → projection 的顺序与可重建性，必须拒绝运行，不能退回内存锁。

### 3. Provision：intent-first 与耐久可见性

Provision 的唯一成功序列为：

```text
authority CAS append provision-intent + authority fsync
  → project intent frame + journal fsync
  → 在 held parent 下 no-clobber 创建私有 staging directory 和 identity marker
  → marker file、staging directory、parent directory fsync
  → authority append staging-prepared observation + authority fsync
  → project observation frame + journal fsync
  → descriptor-relative no-replace rename staging → live allocation name
  → parent directory fsync
  → authority append provision-receipt + authority fsync
  → project receipt frame + journal fsync
  → 返回 receipt
```

1. `AllocationProvisionIntentV1` authority fact 精确绑定完整 authority/Attempt/allocation/lease/generation tuple、冻结 requirements、workdir/env allowlist、staged/live/marker 相对名、对象 identity 预期、request digest、expected authority sequence 与 admission fact digest。
2. staging、live 与 marker 名只能由 Core 从 allocation identity 机械派生，必须位于 held parent 下；创建 staging 必须使用 nofollow/no-clobber primitive，已存在即 conflict。禁止绝对删除目标、`..`、symlink、mount/device 漂移、调用 shell 或以路径再解析替代 held descriptor。
3. staging 创建后，Core 必须用 descriptor-relative `O_CREAT|O_EXCL|O_NOFOLLOW` 或等价 no-clobber primitive 在其中创建 owner-only、closed JCS identity marker；marker target 已存在即 conflict。marker 绑定 intent/request digest、完整 tuple、staging relative name 与 nonce。marker file fsync、staging directory fsync、parent directory fsync 后，Core 重新以 held descriptors 读取 marker，并把 marker digest 与 staging 的 device/inode/type/owner/mode 作为 `AllocationStagingPreparedV1` authority observation 耐久提交；该 identity 未进入 authority subledger 和 recovery projection 前不得 rename。
4. staging → live 必须使用 descriptor-relative no-replace primitive；Darwin 使用 `renameatx_np(..., RENAME_EXCL)` 或语义等价的 no-clobber 实现。发起 rename 时 target 已存在固定 conflict，不得覆盖、交换或删除；同 intent crash recovery 若观察到 staging 缺失且 live 已是 prepared fact 的 exact inode/marker，只能把它作为「rename 可能已完成」进入 parent fsync/reconcile，不能再次 rename。rename 前 staging identity/marker 必须与 prepared fact 相同，rename 后 live 必须仍是同一 device/inode/type 与 marker。
5. `AllocationProvisionReceiptV1` authority fact 只在 live rename 已完成且 parent directory fsync 成功后提交；它回显 intent/prepared digest、request digest、精确 tuple、live directory device/inode/type/owner/mode、marker digest 与 receipt digest。projection receipt frame journal-fsync 完成前不得向调用者返回成功。
6. `ProvisionReceipt` 是被 Core 接纳的 observation，不签发后续 Exec authority。只有 Attempt authority 中相邻的 launch authorization/process fact 才能允许 Exec。
7. 如果 intent 已提交而 receipt 未提交，调用方不得创建第二个 allocation；必须按第 6 节 Inspect/Reconcile 收敛同一 command。

### 4. Terminate：进程终点、tombstone 与 receipt

Terminate 的唯一成功序列为：

```text
authority CAS recheck current Run + cleanupBinding + exact process terminal fact
  → append terminate-intent（绑定 live inode/marker）+ authority fsync
  → project intent frame + journal fsync
  → descriptor-relative no-replace rename live allocation → tombstone
  → parent directory fsync
  → authority append terminate-receipt + authority fsync
  → project receipt frame + journal fsync
  → 返回 receipt
```

1. `TerminateRequestV1` 必须是 closed/versioned 对象，精确包含：`authorityNamespaceId`、`taskId/runId/attemptId`、`allocationId`、`leaseId/generation/fencingTokenDigest`、`terminalizationId`、`cleanupBindingDigest`、`processTerminalFactDigest`、`orchestratorId`、`commandId/idempotencyKey`、`expectedAttemptSequence/attemptAuthorityFactDigest`、`liveRelativeName/tombstoneRelativeName` 与 `requestDigest`。
2. `requestDigest = sha256(JCS(requestWithoutRequestDigest))`。任一字段缺失、未知、跨 scope、跨 generation、非 current、digest 不可重算或与 intent 不同都必须在 mutation 前拒绝。
3. `AllocationTerminateIntentV1` authority fact 在任何 rename/delete 前追加并 fsync，精确绑定 request digest、当时 current 的 authority/cleanup/process-terminal facts，以及由 held descriptor 观察的 live device/inode/type/owner/mode 和 identity marker digest。intent 不等于已终止，projection intent frame 也不授权终止。
4. 只有 ADR 0056 的精确控制单元已由 Core 证明 absent/terminated 后才可 rename。PID/PGID/path 不确定、detached descendant、process identity conflict 或 `not found` 均不能替代 process terminal fact。
5. 终止必须使用同一 held parent descriptor 下的 descriptor-relative no-replace primitive；Darwin 使用 `renameatx_np(..., RENAME_EXCL)` 或语义等价实现，把 live 原子移到 deterministic tombstone。发起 rename 前必须证明 live 与 intent 的 device/inode/type/marker 相同且 tombstone 不存在；target 已存在固定 conflict，不得覆盖、交换或删除。同 intent crash recovery 若观察到 live 缺失且 tombstone 已是 intent 的 exact inode/marker，只能把它作为「rename 可能已完成」进入 parent fsync/reconcile，不能再次 rename。rename 后 tombstone 必须仍是同一 inode/type/marker，live 必须缺失。禁止直接 `RemoveAll(livePath)`、跟随 symlink 或在未知对象上扩大删除范围。
6. `AllocationTerminateReceiptV1` authority fact 只在 no-replace tombstone rename 与 parent fsync 完成后提交；它精确回显 request/intent digest、完整 tuple、live-absent+tombstone-present 的 descriptor-relative observation、与 intent 相同的 tombstone device/inode/type/marker identity、`disposition=applied` 与 `receiptDigest`。projection receipt frame journal-fsync 完成前不得返回成功。
7. receipt 本身仍是 Provider effect observation。Core 只有在重新校验 receipt digest、当前 terminalization/cleanup binding 与 Attempt authority 后，才可 append ADR 0056 的 `allocation-terminated`；`cleanup-completed` 和 `lease-released` 继续按 ADR 0056 的顺序分别提交。
8. 当前 v1 未实现 allocation tombstone GC，因此 tombstone 必须永久保留，任何 production path 都不得删除它。未来若引入物理回收，必须先用独立 ADR 冻结 closed GC intent/receipt、authority、retention 与恢复合同；它不是本 ADR 的 Terminate 成功条件，也不得删除 journal、Outcome 或 Evidence。

### 5. `not found` 与终止判定

1. Provider/文件系统返回 `not found` 只表示当前一次查找未观察到对象，**不等于** `terminated`、`not_applied`、`cleanup-completed` 或可解锁。
2. 只有以下条件同时满足时，Core 才可把 Local allocation 判为已终止：同一 request 的 valid terminate intent；live 名缺失；exact tombstone object 存在且 identity 与 intent/receipt 一致；parent fsync 已完成；valid terminate receipt 已 journal-fsync；当前 Attempt authority 仍绑定该 terminalization/cleanup tuple。
3. 当前合同不接受 tombstone 缺失作为成功证明。即使 receipt 已耐久提交，tombstone 缺失或与 intent inode/marker 不同也必须得到 conflict/unknown；孤立的「live 与 tombstone 都不存在」永远只能得到 `unknown`。
4. live 与 tombstone 同时存在、二者都不存在、对象 identity 漂移、路径交换或 projection/authority 不一致时，结论固定为 conflict/unknown：零删除、零 release、零 unlock、零 successor，并进入 intervention。

### 6. Lost response、restart 与 crash 矩阵

| crash / replay 点 | 唯一恢复结论 |
| --- | --- |
| Provision authority intent 前 | 零副作用；fresh request 可重新 admission。 |
| Provision intent 后、staging 创建前 | 从 authority intent 重建 projection 后续作同一 command；不得生成新 intent。 |
| staging/marker 创建后、prepared fact 前 | 只有 marker/dir/parent 可重验并补 fsync、exact identity 可耐久提交时才补同一 prepared fact；未知对象、target 已存在或漂移则 conflict。 |
| prepared fact 后、staging→live rename 前 | 重验 current intent/barrier、exact staging inode/marker 且 live 缺失后，才可发起同一 no-replace rename。 |
| Provision rename 后、parent fsync 前 | staging 缺失且 exact live inode/marker 匹配时补 parent fsync，再提交 receipt；不得再次 rename或创建第二个 allocation。 |
| Provision parent fsync 后、receipt 前 | exact live 匹配时提交同一 authority receipt 并投影；不匹配则 conflict。 |
| Provision receipt 后响应丢失 | 同 request digest 返回既有 authority receipt；零追加 authority fact、零新目录。 |
| `process-started` 前 launch 不确定 | 沿 ADR 0056 进入 fence/intervention；不得凭 allocation journal 猜测 kill 或终止。 |
| Terminate authority intent 前 | 零 allocation mutation；必须重新取得 current authority、cleanup binding 与 process terminal fact。 |
| Terminate intent 后、rename 前 | 重验 current intent/barrier、exact process terminal fact、live inode/marker 与 cleanup binding；tombstone 缺失才可发起同一 no-replace rename。 |
| Terminate rename 后、parent fsync 前 | live 缺失且 exact tombstone inode/marker 与 intent 相同时补 parent fsync；不得再次 rename；两者同时或 identity 不确定则 conflict。 |
| Terminate parent fsync 后、receipt 前 | exact tombstone 匹配时提交同一 authority receipt 并投影；不得把 `not found` 推断为成功。 |
| Terminate receipt 后响应丢失 | 同 request digest 返回既有 authority receipt；零追加 authority fact、零 signal、零 rename。 |
| receipt 后、`allocation-terminated` 前 | Core 重验 receipt/current tuple 后补 Attempt authority fact。 |
| `allocation-terminated` 后、`cleanup-completed` 前 | 按 ADR 0056 补 cleanup fact；journal 不得自行解锁。 |
| Runtime restart | 先验证 Attempt authority subledger，再从 authority facts 验证/重建 projection，并重验 parent/live/tombstone identity；任何不可收敛缺口 fail closed。 |
| journal partial tail | 仅按第 2 节截到上一完整 frame 并 fsync，再从 authority facts 重建缺失 projection；不得从 partial bytes 推断 effect。 |
| journal trailing garbage/断链、同 command 不同 digest | 固定 corruption/conflict，阻断副作用并 intervention；不得静默 truncate 或重试。 |

所有恢复都复用原 `commandId/idempotencyKey/requestDigest`，不得以 delivery retry 创建新业务 intent。无法取得当前 Run authority、cleanup binding 或精确对象 identity 时，唯一结论是等待/人工介入，不是 blind retry。

### 7. 唯一 `ProductionRuntime` 与 Public application Port

1. v1.0 进程内只允许一个 `ProductionRuntime` composition root。它一次性拥有并装配：Run/Attempt authority、durable provider/agent registry、DispatchLease ledger、`AllocationRecoveryJournalV1`、SandboxProvider、Core `LaunchCoordinator`/exact process unit、ResultIngress、`execution.Service`、Verification/Review 与 Outcome 服务。
2. `ProductionRuntime` 暴露唯一 `PublicApplicationPort`。embedded `marshal` CLI 与 loopback `marshal-server` 只是该 Port 的不同输入 adapter；两者调用相同 in-process application method、同一 authority store 与同一恢复控制器。
3. CLI 和 server 不得直接调用 `execution.Run`/`Adapter.Run`/Sandbox SPI 来重建业务流程；只有 `ProductionRuntime` 内部可以沿冻结 command 调用 `execution.Service`。server 不得通过启动子 `marshal task run` 复用业务逻辑，CLI 也不得递归启动自身充当控制面。
4. production path 禁止环境变量选择 embedded、production gate 或 legacy path。`MARSHAL_EMBEDDED_SANDBOX`、`MARSHAL_WORKER_EXECUTOR=legacy`、`MARSHAL_PRODUCTION_GATE` 或等价环境开关不得改变 supported composition；Provider executable path 等普通配置可以进入冻结 typed configuration，但不能开启 bypass。
5. v1 production selector 只接受 `LaunchCapable` AgentProvider，经 allocation-carried Exec 与 ResultIngress。direct host `Adapter.Run`、child CLI、memory-only LocalRunner authority、seed/fake authority、可选 admission reconciler与 legacy fallback 都不能从 `ProductionRuntime` 到达。
6. compatibility/测试装配必须以不同 constructor/package-private test seam 明确隔离，不注册为 production profile，不得在 production constructor 失败后自动 fallback。生产依赖缺失、重复 Runtime、authority lock 已占用或恢复不确定时启动即 fail closed。
7. 同一 repository/authority scope 的 `marshal` 与 `marshal-server` 竞争同一 owner lock；不能同时成为写 Control Plane。只读 status/events 可以通过当前 owner 的 Public Port 或只读 ledger projection 提供，不能打开第二写路径。

### 8. 平台可用性

1. 完成本 ADR 的全部实施门禁后，Darwin `darwin-local-dogfood` 才是本文唯一允许启用的 production-shaped profile；ADR 被接受或组件单测通过本身都不启用该 profile。启动时必须证明：固定 Marshal 对象通过 ADR 0051；RB1 Attempt authority store 可打开；RB2 exact process coordinator/observer 可用；allocation journal 可恢复；支持的 AgentProvider 为 `LaunchCapable`。任一条件失败时 `task run` 在 Agent/Provider/文件系统副作用前返回 typed unavailable，不得回退 legacy。
2. Darwin profile 的 assurance 上限仍是 trusted single-user、ordinary-user、`workspace-write`、non-hardened。process group、held FD、tombstone 与 journal 只提供精确恢复和误杀防护，不提供恶意 workload containment。
3. Linux 在本 ADR 接受时明确为 `unprofiled`：CLI/server 可以提供 version、doctor、只读状态、规划与构建产物，但 production `run/start` 必须返回 typed `platform-profile-unavailable`，且在 Probe/Provision/Attempt side effect 前停止。实现并通过独立 Linux process/allocation authority ADR + conformance 后才能启用；不得复用 Darwin 结论或静默称普通 Linux host process 为 production authority。
4. 其他平台同样 fail closed；未知平台/profile 不允许自动选择 LocalRunner。

## 实施门禁

本 ADR 接受后，最小实现必须作为一个纵切完成，不得只新增孤立 package 后声称 `INTEGRATED`：

1. 先在 RB1 单一 authority store 内实现 closed SideEffectIntent/Receipt/ReconcileDecision subledger、pending-effect barrier 与原子 admission；覆盖 stale sequence、check-then-append、重复 command 和冲突 effect 负测；
2. 实现 `AllocationRecoveryJournalV1` exact framing/sequence/canonical hash-chain、authority-fact projection/rebuild 与 request/receipt closed types，覆盖 partial-tail、trailing-garbage、corrupt/replay/conflict 负测；
3. 在 Local Provider 中实现 marker-bound identity、descriptor-relative no-replace Provision/Terminate、file/dir/parent/journal fsync 与 tombstone 永不删除；移除直接 `RemoveAll(livePath)` 成功路径；
4. 将 authority subledger/projection 接到 RB2 process unit；覆盖第 6 节全部 crash points、两次 restart、target-exists、ABA/path swap、inode/marker 漂移、symlink、lost response、not-found、跨 orchestrator 与 cleanup binding 漂移；
5. 建立唯一 `ProductionRuntime`/`PublicApplicationPort`，让 CLI 与 server 同进程调用；删除 server child CLI 和 production 环境选择器，确保 legacy/direct execution 从 production import graph 不可达；
6. 在最终 composition root 上用真实 Pi 完成：durable Run → allocation-carried Agent → ResultIngress → independent Verification/ReviewDecision → `ACCEPTED`，并执行 restart/late result/terminate lost-response canary；
7. Darwin required conformance、静态检查、跨平台编译、release contract 与安装验证通过后，能力才可从 `COMPONENT` 升为 `INTEGRATED`；稳定版签名/notarization 与 Linux authority 仍按各自独立门禁，不得由本 ADR 冒充关闭。

## 后果

正面后果：

- Allocation 副作用在 crash/lost-response 后有唯一、可重放且不误删的结论；
- `not found` 不再被误当成 cleanup 证明；
- CLI 与 server 共用一个生产装配，移除 child CLI、环境 gate 与 direct host execution 的多路径歧义；
- RB1 的业务 authority、RB2 的精确进程控制与 Provider 文件系统恢复组成一条可验证纵切。

代价与限制：

- 增加一个耐久 recovery journal、tombstone 保留与 owner lock；
- crash fixture 和 descriptor-relative 文件系统实现比直接删除复杂；
- 当前只有 Darwin ordinary-user production-shaped run 可用，Linux 明确 fail closed；
- 本设计提高恢复确定性，但不提高 Local host 的隔离等级，也不关闭签名/notarization、Linux authority 或 stable release blocker。

## 拒绝的替代方案

### 继续依赖 `LocalRunner` 内存 map 与 best-effort allocation record

拒绝。空 map、缺文件或进程退出都不能证明副作用状态；best-effort 记录会在最需要恢复时消失。

### `Terminate` 直接 `RemoveAll`，错误时重试

拒绝。直接删除没有 intent/receipt crash 边界，路径交换与 response loss 会让恢复方无法区分完成、未执行和误删风险。

### server 继续启动固定子 `marshal`

拒绝。即使二进制 identity 稳定，child CLI 仍产生第二次装配、第二 owner 生命周期与取消/恢复窗口；共享代码必须通过同一 application Port，而不是共享可执行文件。

### 仅在 Darwin 启用严格路径，Linux 继续 best-effort LocalRunner

拒绝。跨平台静默降低 authority 会让同一 v1 标签表达不同安全语义。Linux 未实现时应明确 unavailable，而不是伪装成 production 可用。
