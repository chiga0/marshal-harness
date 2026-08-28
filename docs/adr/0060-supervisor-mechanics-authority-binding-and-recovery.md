# ADR 0060：Supervisor mechanics 权威绑定与崩溃恢复锚点

- 状态：已接受（Accepted，2026-08-29）。候选 `12996f87beb3b45b9267d4356875d9ebe257fcd2` 经独立终审确认 P0/P1/P2 均为 0；接受只冻结 mechanics authority binding 与恢复合同。`processsupervisor.Client` prepared-command API、descriptor-relative nonce/journal recovery、lost-`Close` offline recovery、生产 composition 接线与真实故障矩阵仍未完成，不升级 I186-R2–R6，也不构成发布授权。
- 关联：[ADR 0044](0044-result-ingress-and-cold-hot-paths.md)、[ADR 0052](0052-v1-release-scope-and-production-reachability.md)、[ADR 0056](0056-darwin-process-observation-and-attempt-terminalization.md)、[ADR 0057](0057-durable-local-allocation-recovery-and-production-composition.md)、[ADR 0059](0059-fixed-darwin-process-supervisor.md)、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)。

## 背景

ADR 0059 要求每个 Core 业务事实绑定产生它的精确 Supervisor mechanics receipt 与 command head，但既有 RB1 Attempt authority 只在 `process-started` 保存 Core 进程观察、在 `process-terminal` 保存一个 observation digest，在 `process-supervisor-closed` 保存部分 close digest。它不能在重放时证明 `requestDigest → receiptDigest → observationDigest → commandHead` 是同一命令链，也没有在启动 Supervisor 外部副作用前提交恢复锚点。

因此存在两个 crash gap：

1. `launch-authorized` 后、handshake/`process-supervisor-started` 前，恢复 Core 无法仅从 authority ledger 枚举预期 session、nonce digest、控制目录与固定二进制；
2. Supervisor journal 已耐久 command intent、但 receipt 无法收敛时，Attempt ledger 没有一个永久 fail-closed 的 intervention 事实，后续 cleanup/release 可能把“未知”误作可重试顺序缺口。

只把这些信息保存在 Client 内存、socket 路径或第二份 store 会重新产生双真值，不能满足 ADR 0052/0057 的单一生产 authority。

## 决策

### 1. 取代与保留关系

1. 本 ADR **补充** ADR 0059，并精确取代其 §4、§6 中 `launch-authorized → supervisor handshake → process-supervisor-started` 的局部顺序，改为：

   ```text
   launch-authorized
     → process-supervisor-bootstrap-prepared
     → supervisor bootstrap / authenticated handshake
     → process-supervisor-started
   ```

2. 本 ADR 澄清并落实 ADR 0059 §5 的“业务事实锚定 mechanics receipt”要求，不改变 Supervisor 只拥有 mechanics、Core/RB1 ledger 拥有业务 authority 的边界。
3. ADR 0056 的 terminalization 顺序、ADR 0057 的单一 `ProductionRuntime` 与单一 RB1 ledger、ADR 0044 的 ResultIngress admission authority 均保留。不得新增 Supervisor store、Client sidecar ledger 或路径/PID authority。
4. 旧 `attempt-authority/v1`/`result-ingress/v2` 事实保持逐字节 replay：新增字段使用缺省省略；旧事实缺少 mechanics binding 时只能保留历史投影，不能被合成或冒充新生产证据。

### 2. Bootstrap recovery anchor

`process-supervisor-bootstrap-prepared` 必须在启动固定 Supervisor 前，以 current owner + current Run authority 对同一 Attempt head 做 CAS。事实只保存：

- protocol revision、current owner binding、`launch-authorized` fact digest；
- 唯一 session ID、**nonce digest**（绝不保存 raw nonce）；
- descriptor-observed control-directory identity；
- 固定 Supervisor binary identity；
- secret-free bootstrap request digest。

同一 RB1 历史中的 session ID 或 control-directory device/inode 不得复用，即使旧 Attempt 已关闭。`process-supervisor-started` 必须引用 bootstrap-prepared fact digest，并逐字段匹配 session、nonce digest、owner、launch fact、control directory 与 binary。Handshake 前崩溃时，恢复只从 `PendingAttemptStates()` 枚举该锚点；不得扫描 socket、PID、argv 或目录猜测归属。

### 3. 独立 command recovery 子链

Supervisor command 使用同一 `result-ingress.jsonl` 中的独立 recovery 子链，**不得**作为 `AttemptTransition` 推进 Attempt revision/head。否则 command intent 既要引用 command 前 current authority head、其自身又会改变该 head，形成不可闭合的循环。

每个 session 同时最多一个 pending command，顺序固定为：

```text
process-supervisor-command-intent（Client.Do 前 creation-once）
  → process-supervisor-command-outcome（authenticated response 后立即落账）
  → 引用 exact outcome fact digest 的业务事实
  → process-supervisor-session-reconnected（下一 command 前，若 owner/head 已改变）
```

Intent 保存 exact command ID、sequence、previous command head、current Attempt authority head、deadline、request/payload digest、完整 mechanics pre-anchor，以及足以在 held descriptor 恢复后重建请求的非秘密 typed projection。它不保存 raw nonce、路径、argv、environment values、stdin 或 transcript bytes。

Outcome 只接受 Client 已认证的 `VerifiedCommandOutcome` 投影；`ok` 和 `rejected` 都逐条形成 checkpoint 并推进 command/journal sequence/head。特别是 child 尚存活时的 rejected `collect` 不是可忽略错误：它关闭当次 intent，后续 retry 必须从其 post-anchor 继续。每个 pre/post anchor 都绑定 session、nonce digest、authority tuple、owner epoch、current authority head、command sequence/head、journal sequence/head、UID/GID、固定 binary 与 control socket；任一静态身份或连续性漂移均 fail closed。

Intent/outcome/reconnect fact 记录 append 时 exact Attempt revision/head，但不改变它；业务 fact 或恢复时的 `control-owner-bound` 改变 owner/head 后，下一 command 必须先持久化 `process-supervisor-session-reconnected`。该 fact 的 fresh API 只接收 Client 已认证的 `SessionRecoveryEvidence`，并绑定 `Previous → Current` 完整 mechanics anchor、`ReconciliationState`、exact pending request projection 与 `MechanicsLocked`；调用者手改 owner epoch、authority head 或 command/journal head不能重锚。command ID 在 session 历史中唯一，同一 pending intent 的逐字节 replay幂等，same ID different request/head/outcome 不是 replay。

新 bootstrap-prepared Attempt 的下列业务事实必须引用 exact successful outcome fact digest：

| 业务事实 | mechanics command | 必须绑定的 typed outcome |
| --- | --- | --- |
| `process-started` | `spawn` | exact child PID/birth/session/PGID 与 `exec-stopped`，并匹配 Core 的 runtime/cwd process observation |
| WorkerResult `result-admitted` | `collect` | exact child identity、transcript/stdout/stderr digest、byte count、truncation |
| `process-terminal` | `inspect` 或 `terminate` | exact child identity与 `exited`/`absent`/`identity-conflict` closed outcome |
| `process-supervisor-closed` | `close` | exact child terminal report 与 session-closed 语义；started fact 中的 supervisor identity 另由故障域外 absence observation关闭 |

Outcome evidence 至少包含：

```text
protocolRevision, sessionId, command, commandId, sequence,
previousCommandHead, currentAuthorityHead,
requestDigest, receiptDigest, observationDigest, commandHead,
disposition, reasonCode, typedOutcome
```

`commandHead` 由 Core 按 ADR 0059 的 canonical tuple `previousCommandDigest + requestDigest + receiptDigest` 重算。ResultIngress 只接收已经由 Client 完成 authenticated response binding 的 closed projection；projection 必须足以逐字节重建 secret-free mechanics report 与 `MechanicsResult`，并重新计算 `observationDigest`、`receiptDigest`，因此调用者不能把任意 typed outcome 配到一个真实 receipt 上。它不持久化 request payload、argv、environment values、stdin、nonce 或 transcript bytes。

`bind-authority` 的 request/current head 来自 pre-anchor，verified post-anchor 唯一产生 bound authority head；调用者不能任选一个 digest。`spawn` 首次冻结 exact child PID/birth/session/PGID 与 runtime/cwd object digest；后续 `collect`、`inspect`/`terminate`、`close` 必须持续匹配。successful `spawn`、terminal command 或 `close` outcome 在对应业务 fact 落账前禁止发下一 command，避免以第二次 mechanics 副作用覆盖尚未接纳的 outcome。

### 4. ResultIngress collect 原子绑定

bootstrap-prepared Attempt 的 fresh WorkerResult admission 必须通过显式 collect-binding API，在同一 RB1 transaction 内把 ResultEnvelope admission 与 exact successful `collect` outcome fact digest 一起落账。缺失、陈旧 head、错误 session/child 或伪造 transcript projection均进入 quarantine。已提交 fact 的 lookup-only exact replay仍可在 lease 过期后返回原结论，不要求重新执行 collect。

`AdmitWithSupervisorCollectOutcome` 只声明 ResultIngress business fact 与已耐久 outcome reference 的同事实 co-commit；当前合同没有证明 transcript payload extraction 与 admission 在同一个事务内完成，因此不得据此宣称 bytes transport 已原子化。

Hot-path envelope 与非 WorkerResult cold-path 不携带 collect evidence；在这些路径注入 collect evidence必须拒绝。该决策不把 transcript bytes 当 ResultIngress authority，也不改变 DRC 字段。

### 5. Unresolved intent 与 intervention

若 RB1 只有 command intent，且原 Supervisor 无法通过 authenticated replay 唯一闭合 outcome；或 bootstrap/session/peer/binary identity 无法唯一确认，Core 必须在 current owner + current Run authority 下 append `process-supervisor-intervention-required`。该事实保存 closed reason、非敏感 evidence digest，并在 command-intent gap 时保存 exact session/command ID/sequence/previous head/current authority head/request digest，不保存 request payload。恢复时唯一允许先改变 Attempt 的动作是把新 owner epoch 以 `control-owner-bound` 接到 exact current head，随后必须把 authenticated `SessionRecoveryEvidence` 落为 reconnect fact；`exact-intent-pending` 必须保持 mechanics locked 并转 intervention，不能伪造新 post-anchor。Supervisor journal 已提交 receipt 但 RB1 尚未提交 outcome 时，恢复必须调用未来的 prepared-command replay API 取得同一 `VerifiedCommandOutcome`，不得重新生成 command。

存在 command intent 或 intervention 时，任何 allocation effect intent 与无关 Attempt mutation都必须在写 ledger 前 fail closed；只为 authenticated recovery 保留上述 owner bind + reconnect 窄例外。Intervention fact 一经提交永久阻断新的 admission、signal、terminate、close、cleanup、release 与 successor；只允许读取、exact fact replay，以及 `CleanupInspect` callback 内不改变 mechanics 的只读检查。任何其他 callback mutation 都 fail closed。Supervisor 自身崩溃后不得由新 Supervisor 重做未知副作用。恢复与 `PendingAttemptStates()` 连续枚举两次及以上必须得到相同 pending/intervention projection。

### 6. 兼容与生产启用

- 新字段与 fact type 继续写入现有 RB1 append-only ledger/projection，不升级成第二个 store。
- 历史旧事实保持原 canonical JSON 与 digest；replay 不补零值字段、不伪造 receipt。
- 新生产 composition 必须先写 bootstrap-prepared，因而自动进入完整证据门禁；没有 bootstrap-prepared 的旧路径仅用于历史 replay/迁移测试，不得作为新的 production reachability 证据。
- 当前 `processsupervisor.Client.Do` 内部生成 exact `Request`，业务层在调用前拿不到 request digest、sequence/head、deadline 与 payload digest。因此生产接线前，Client 必须增加 `PrepareCommand`/`ExecutePrepared`（或等价 deterministic prepared-intent API）；ResultIngress 本提交只冻结并测试 secret-safe prepared projection 与 `VerifiedCommandOutcome` ledger contract，不虚构当前 API 可调用性。
- descriptor-relative raw nonce recovery 及其 journal/control object device+inode 绑定、丢失 `Close` receipt 后的 offline Supervisor absence recovery仍是生产前置；raw nonce 只能由后续 held descriptor recovery取得，永不进入 RB1。
- `close` outcome 的 receipt payload 是 exact child terminal `ProcessReport`，不是 Supervisor identity。Supervisor absence 是独立 typed observation，由 `process-supervisor-closed` business fact 同时绑定；二者不可互相替代。
- 本 ADR 候选实现不接线 CLI、SandboxBridge、ProcessControl 或 Client，不改变当前产品可达性声明，也不得宣称 production ready。

## 必须通过的 hostile 矩阵

- session/control-directory ABA、owner epoch ABA、错误 Run/Attempt/head 与 callback double-call；
- request/receipt/observation/command head 任一替换，command/journal sequence/head 断链，same command ID different digest，typed child session/runtime/cwd 替换；
- crash 于 bootstrap intent 前后、handshake 前后、started CAS、spawn receipt/process CAS、collect/admission、terminal receipt/CAS、close receipt/absence/CAS 的每个边界；
- unresolved intent、Supervisor crash、response loss、两次连续 Core restart 的确定性 pending/intervention enumeration；
- caller 自报 reconnect head/epoch、缺 authenticated `SessionRecoveryEvidence`、pending/intervention 期间 allocation effect 尝试，拒绝后 ledger 必须逐字节不变；
- rejected collect、lost receipt、旧 ledger 无新增字段仍逐字节 replay，新事实缺 bootstrap/request/mechanics evidence fail closed；
- close child terminal report 与先前 terminal outcome 不一致、Supervisor absence observation 伪造或混用 Supervisor/child identity；
- `process-terminal → allocation-terminated → process-supervisor-closed → cleanup-completed → cleanup-released` 顺序不可倒置；
- RB1 ledger/event/error/log 中 raw nonce、argv、environment value、stdin、transcript bytes 为零。

## 后果

优点是启动外部副作用、命令 mechanics 与 Core 业务事实第一次共享同一耐久 predecessor/replay projection，恢复不再依赖内存或扫描。代价是每个 Supervisor command 必须由 Client 产生 closed typed projection；Client API 与生产 adapter 在该投影完成前必须 fail closed。旧事实可读取但不能成为新生产 conformance 证据。
