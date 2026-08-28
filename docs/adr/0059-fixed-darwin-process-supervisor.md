# ADR 0059：固定 Darwin 进程 Supervisor 与 Core 重连

- 状态：提议（Proposed，2026-08-29）。本 ADR 未经独立评审与维护者接受前不得实施，不改变 I186-R2–R6 的成熟度，也不授权发布稳定 `v1.*`。
- 关联：[ADR 0018](0018-control-plane-and-provider-ports.md)（Core 业务权威）、[ADR 0051](0051-darwin-local-dogfood-profile.md)（Darwin ordinary-user 边界）、[ADR 0052](0052-v1-release-scope-and-production-reachability.md)（v1 可恢复纵切）、[ADR 0056](0056-darwin-process-observation-and-attempt-terminalization.md)（进程观察与终结事务）、[ADR 0057](0057-durable-local-allocation-recovery-and-production-composition.md)（唯一生产装配）、[ADR 0058](0058-interpreted-agent-launch-identity.md)（解释型启动身份）、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)。

## 背景

ADR 0056 与 ADR 0058 要求 Core 在真实解释器映像仍暂停时提交 `process-started`，并在 Core 重启后以耐久事实恢复进程控制。现有 Darwin 候选使用 `PT_TRACE_ME`：启动者直接持有 child 的 wait right、tracer 关系、held FD、vnode guard 与输出 pipe。该模型只能闭合首次启动；启动 Core 一旦退出，新 Core 无法重新获得这些内核对象，也不能安全恢复 resume、wait、signal 或 transcript。

只持久化 PID、PGID、路径或文件摘要不能修复这一缺口。把重启后的未知状态解释为进程已退出会允许旧 workload 与 successor 双活；按 PID 扫描或重新打开路径后 kill 则扩大了跨编排副作用。若保持现状，诚实结论只能是：Core crash 后进入 intervention，禁止 release、unlock 与 successor，能力保持 `COMPONENT`。

为满足 v1 的 Core restart 门禁，需要一个在单次 Attempt 内比 Control Core 更长寿、但不拥有业务决策权的进程 mechanics owner。它必须使用稳定、已允许的 Marshal 可执行身份，不能重新引入随机临时 Mach-O。

## 决策

### 1. 适用范围与取代关系

1. 本合同只适用于 ADR 0051 的 Darwin trusted single-user ordinary-user profile，不提供 hardened containment、跨用户隔离、Linux authority 或同 UID 恶意进程防护。
2. 本 ADR 部分取代 ADR 0056 §2/§3/§7 与 ADR 0058 §5 中“Control Core 进程直接、持续持有 wait right/held FD/pipe”的物理实现要求。逻辑所有权仍属于唯一 Core authority；物理 mechanics 委托给本 ADR 的固定 supervisor。
3. ADR 0018 的 authority ledger、Run journal 与 DispatchLease ledger 仍是唯一业务权威。Supervisor 不能 append ledger、选择 retry、生成 ReviewDecision、解锁 worktree、发布、创建 successor 或把自身 observation 提升为业务事实。
4. `process-started`、ResultIngress admission/terminalization barrier、cleanup binding、`process-terminal`、`allocation-terminated`、`process-supervisor-closed`、`cleanup-completed` 与 `lease-released` 仍由 Core authority 串行化；本 ADR 只补充 supervisor mechanics receipt 的位置，不允许 mechanics owner 跳过或倒置业务事实。
5. Supervisor crash 不属于本 ADR 自动恢复范围；无法重连并重验同一 supervisor 时固定进入 intervention，零 release、零 unlock、零 successor。

### 2. 唯一稳定可执行身份

Supervisor 只能由当前生产装配已验证的固定 Marshal 产物以隐藏入口启动：

```text
<fixed-marshal> internal process-supervisor
```

禁止 `go run`、`go test` helper、`/tmp/*checker*`、随机复制或重命名的 Marshal、脚本包装器、shebang 和 PATH 查找。Core 在启动前后都必须重验 supervisor 可执行文件的 canonical path、device、inode、type、uid、gid、mode、link count、size、raw SHA-256、Darwin CDHash、build `sourceHead` 与 self profile；任一漂移均在创建 Provider workload 前 typed unavailable。

隐藏入口只接受 Core 预先建立并继承的 bootstrap socket/held control-directory FD；控制路径、session nonce、authority payload 与凭据不得出现在 argv 或 ambient environment。直接从 shell 调用、继承 FD 身份不符、bootstrap frame 不完整或有额外输入时必须在创建 socket/child 前拒绝。

每个 Attempt 冻结一个 supervisor binary/protocol identity。升级后的新二进制不得接管旧 Attempt：旧 supervisor 仍活且协议兼容时，由新 Core 使用旧 Attempt 已冻结的身份完成重连；否则 intervention。新二进制只服务新 Attempt。

### 3. Mechanics 权限与禁止事项

Supervisor 位于 child process group 之外，只拥有以下有界 mechanics：

- runtime、cwd、material roots/materials 的 held FD 与 vnode guard；
- 固定 Marshal launch helper 的 child parent/wait right、真实解释器的 tracer/暂停状态、根 PID/PGID 与 birth observation；
- 有界 stdin/stdout/stderr、transcript bytes、exit/wait observation；
- 依据 Core 已提交 authority digest 的精确 `resume`、`inspect`、`terminate`、`collect` 与 `close` 命令。

Supervisor 不得根据超时、断连、输出内容、Provider claim 或自身策略决定业务成功、失败、重试、cancel 或 cleanup complete。`process-started` 之后 Core 断连时，它可以继续持有/观察已授权 workload 与收集有界结果，但不得自行 signal。`process-started` 之前的 child 必须保持 suspended；没有新的当前 Core authority 命令时不得 resume。

### 4. 稳定控制目录、会话与耐久事实

每个 Attempt 使用 owner-only、descriptor-relative 打开的稳定控制目录和 Unix domain socket。目录以 `O_EXCL` 等价语义创建；预先存在、身份不符或已有未知内容时不得 adopt。目录、socket、状态文件与 transcript 可以是数据文件，但不得包含可执行文件。路径只用于定位，不能单独授权。

Core 在 `launch-authorized` 之后启动并完成 supervisor handshake，再以同一 Attempt authority CAS append `process-supervisor-started`。Handshake 产生的是 **unbound passive supervisor**：它已经持有稳定 control objects，但没有 child，也不能接受 `spawn`。该 closed fact 至少绑定：

- protocol revision、完整 Attempt/allocation/lease/generation/orchestrator tuple；
- prior `launch-authorized` fact digest 与完整 supervisor binary identity；
- supervisor PID、Darwin birth identity、supervisor session/PGID；child 尚不存在，child PID/PGID/birth 只能进入后续 `spawn` receipt 与 `process-started`；
- control parent 与 socket 的 device/inode/type/uid/gid/mode/link count；
- 256-bit session nonce digest、命令 sequence 与初始 command hash head；
- observer identity、observedAt、唯一递增 `ownerEpoch` 与 canonical fact digest。`ownerEpoch` 必须由同一 authority store 在取得生产 owner lock 时 CAS append `control-owner-acquired` 分配，绑定 owner PID/birth/fixed-binary identity；内存计数、wall clock、PID 或 socket sequence 均不能充当 epoch。

原始 session nonce 只保存在 supervisor 内存、held connection 与控制目录内 owner-only `0600` 文件，不能写入 journal、日志、事件、命令行或 ReviewPacket。`process-supervisor-started` CAS 成功后，Core 必须发送同 digest 的 `bind-authority`；该命令把 fact digest、owner epoch 与 authority head 写入 mechanics journal，只有 bind receipt 耐久后才允许 `spawn`。CAS 后 Core 崩溃时，新 Core 从同一 fact 重连并幂等补同一 bind；CAS 前崩溃时，恢复方必须先以 current owner lock 证明 authority ledger 中不存在该 session fact，才可发送 `abort-unbound`。Supervisor 不得以 handshake deadline 自行退出或把「未收到 bind」解释成 CAS 未发生；无法取得唯一 ledger 结论时 intervention，release 永久禁止。

控制目录保存 append-only mechanics journal，并精确复用 ADR 0057 §2 的 framing grammar：每帧为 8 个 ASCII 小写十六进制 payload byte length（左侧补 `0`）、单字节 `:`、精确 RFC 8785 JCS payload、单字节 LF；不另加实现相关 CRC。`journalSequence` 从 `1` 开始递增；`recordDigest = sha256(JCS(recordWithoutRecordDigest))`；第一帧 `previousRecordDigest` 固定为 `sha256("marshal/process-supervisor-journal/v1\x00genesis")`，后续精确绑定上一完整帧。request intent 必须在执行 mechanics 前写入并 `fsync`，receipt/observation 必须在返回前写入并 `fsync`，新文件/rename 同步 held parent。只有 EOF 落在最后一帧合法 framing 前缀内且此前所有帧完整有效时才允许截去 partial tail并 `fsync`；完整非法帧、非 canonical JCS/digest、gap、fork、重复 sequence、同 command 不同 digest或 trailing garbage 一律 intervention。该 journal 只是 authority-bound mechanics projection，不能授权业务状态转换。

重启 Core 只能从 authority ledger 的 pending Attempt/supervisor facts 枚举重连目标，不能扫描 socket、PID 或 argv 猜测归属。它必须通过生产 owner lock、held parent/socket identity、peer credentials、supervisor PID birth、binary identity、nonce challenge、mechanics journal hash head 与 authority 中的 digest 全量重验，才能建立新连接。认证必须双向：Core 验证 supervisor；supervisor 也验证重连 peer 的 UID、PID/birth、固定 Marshal path/SHA/CDHash/sourceHead/protocol、nonce challenge、严格递增 owner epoch 与 exact authority anchor。Supervisor 同时只接受一个 Core connection；旧连接未 EOF、相同/更旧 epoch、第二 Core 或 anchor 不符均拒绝且零 mechanics。原始 nonce 文件单独不构成授权。同 UID ordinary-user 边界不宣称这些机制能抵御已完全控制该用户的恶意进程。

### 5. 有界、可重放的控制协议

协议仅允许 closed 命令集合：`bind-authority`、`abort-unbound`、`spawn`、`resume`、`inspect`、`terminate`、`collect`、`close`。每个 frame 使用 length-bounded canonical JSON，至少包含：

```text
protocolRevision, sessionId, commandId, sequence,
previousCommandDigest, currentAuthorityHead,
requestDigest, deadline, payload
```

响应绑定相同命令字段、observation/receipt digest 与新的 command hash head。相同 `commandId + requestDigest` 重放必须从 mechanics journal 返回相同结论且不重复副作用；相同 ID 不同 digest、sequence/head 不连续、未知字段、超限 frame、过期 deadline 或 authority head 漂移均拒绝。若 journal 尾部是已耐久 intent、但 response/receipt 丢失，仍存活的同一 Supervisor 必须先从其 held mechanics 观察确定该命令已执行、未执行或 identity conflict，再补同一 receipt；在该 pending command 收敛前不得接纳下一 sequence，也不得盲目重做。Supervisor 自身在 intent 与 receipt 之间崩溃时没有第二个 mechanics owner 可以完成判定，恢复结论固定为 intervention，而不是由新 supervisor 重做副作用。

Wire request 可以在认证 socket 上有界传递 spawn 所需 raw argv、environment values 与 stdin，但这些值不得进入 mechanics journal、authority ledger、event、error 或普通 log。Journal 只保存 env-key allowlist、`argvDigest`、`environmentDigest`、`stdinDigest`、runtime/cwd/material closure digests 与 secret-free reason code；不保存 raw argv、raw env values、stdin 或 auth token。Supervisor 只在内存/held FD 中保留执行所需 raw material，Core 重启只能重连仍活的同一 supervisor；supervisor crash 后不能从落盘 secret 重建，仍固定 intervention。stdout/stderr/transcript 可写 owner-only data object，业务记录只引用其 digest/size/truncation。任何 protocol/error 文本不得回显 raw request 或 transcript bytes。

- `bind-authority` 只把已提交 `process-supervisor-started` 的 exact fact digest、owner epoch 与 authority head绑定到 unbound session；同 digest重放幂等，不创建 child。`abort-unbound` 只在 current owner 提供同一 session 的 authority-absence proof 时关闭无 child session；一旦 bind 或 spawn 出现永久拒绝 abort。
- `spawn` 只在 `process-supervisor-started` 已提交后接受，并冻结 ADR 0058 的完整 runtime/material/argv/env/cwd 闭包。Supervisor 以固定 Marshal helper 创建 child，再等待真实解释器映像进入 exec-stop；helper 永远不能被报告为 `process-started`。
- `resume` 必须绑定 Core 已提交的精确 `process-started` fact digest。重复同一 resume 幂等，不同 digest 永远拒绝。
- `terminate`（含 signal/wait）与 `inspect` 必须绑定已提交 terminalization barrier digest、当前 terminalization ID、terminal generation、cleanup binding digest、`process-started` digest、最后 observation digest 与 current authority head；它们不能绑定尚未产生的 `process-terminal`。每次 TERM/KILL/wait 前均重新验证完整 root birth/PGID/runtime/cwd identity；自然退出由 `inspect`/`terminate` 产生同一类 wait receipt，但零 signal。
- `collect` 只返回有界、已封闭的 transcript/exit observation；它不能声明 ResultIngress admission。
- `close` 只在 `process-terminal` 与 `allocation-terminated` 已提交、cleanup binding 仍 current 时接受。它先耐久 intent，再封闭输出、释放 child wait/FD mechanics、耐久 final receipt并尝试返回；随后关闭 control socket并退出。Core 必须观察 exact supervisor PID/birth 已终止，才可 append `process-supervisor-closed`（绑定 final receipt/command head/absence observation），再以该 fact为 predecessor append `cleanup-completed`，最后 `lease-released`。

每个 Core business fact 必须锚定产生它的精确 mechanics receipt 与 command journal head：`process-started` 绑定 `spawn` receipt/observation；terminalization barrier 之后的 `process-terminal` 绑定 cleanup `inspect/terminate/wait` receipts；ResultIngress transcript 绑定 `collect` receipt与 bytes digest；`process-supervisor-closed` 绑定 `close` intent/receipt，`cleanup-completed` 再绑定该 closed fact。未锚定的 supervisor observation 只能作诊断。

### 6. 启动、重启与终结顺序

唯一顺序为：

```text
launch-authorized
  → supervisor handshake
  → process-supervisor-started
  → bind-authority request/receipt
  → spawn request/receipt
  → real runtime exec-stop observation
  → process-started
  → resume
  → ResultIngress admission conclusion + terminalization barrier
  → cleanup-bound terminate / wait
  → process-terminal
  → allocation terminate / allocation-terminated
  → supervisor close intent/receipt / process-supervisor-closed
  → cleanup-completed
  → lease-released
```

Handshake 后、`process-supervisor-started` CAS 前崩溃时，Supervisor 保持无 child 的 passive session，不得按超时自行推断 CAS 失败。恢复 Core 先取得唯一 owner lock并读 authority ledger：存在同 session fact则重连并补同一 `bind-authority`；确定不存在才可 `abort-unbound`；无法判定则 intervention。Supervisor 已产生 suspended child、但 `process-started` 尚未提交时，重启 Core 只能重连并重放同一 `spawn` 取得 exact observation；若 current authority sequence 仍允许同一 `process-started` CAS，才可提交并 resume。CAS conflict、身份不全或 observation 漂移均保持 suspended 并进入 intervention，禁止 successor。

`ResultIngress admission conclusion + terminalization barrier` 是同一 authority transaction 的唯一竞态结论：若结果先被接纳，barrier 固定 accepted result digest；若 cancel/timeout/失败先赢，随后到达的结果只能进入 quarantine。不存在含糊的“result or barrier”分支，也不得在 barrier 前 signal。Cleanup command只绑定已存在的 barrier/process-started/observation，不依赖未来 `process-terminal`。

`process-started` 后的 Core 重启从同一 supervisor 取回 exact running/stopped/exited observation、output hash head 与命令链，再与 current Attempt authority 重验。重连不会重建 child、重复 resume 或重新打开另一组输出 pipe。两次及以上重连必须产生同一 projection。

Supervisor crash、socket/object ABA、binary drift、wait right 丢失、pipe/transcript 不连续、root detach 或无法证明同一 child 时，固定写 intervention Outcome；不得以“进程可能已经退出”补 `process-terminal`。`close` response 丢失只能从同一完整 mechanics journal 的 exact close receipt收敛；没有 receipt不得补 `cleanup-completed`。

### 7. 输出、资源与 GC 边界

`process-supervisor/v1` 冻结以下 hard limit：wire frame `1 MiB`、journal JCS payload `256 KiB`、journal file `64 MiB`、每 session `4096` 条命令；argv 最多 `256` 项且 UTF-8 总计 `256 KiB`；environment key 最多 `128` 个，单 key `128 B`、raw values 总计 `256 KiB`；stdin `1 MiB`；stdout 与 stderr 各 `16 MiB`，transcript data object 合计 `32 MiB`；单条对外 diagnostic `64 KiB`。命令 deadline 上限分别为 `bind-authority/abort-unbound/resume/inspect=30s`、`spawn/terminate/collect/close=2m`；workload wall-time 由 Core Attempt budget决定，不借命令 deadline 偷增预算。任一上限在 protocol revision 内不可由配置放宽；需要放宽必须升级 revision，旧 session 继续使用冻结值。

输出达到上限后 Supervisor 仍须有界地 drain child pipe并只记录 overflow observation，避免阻塞 child，但不能把截断 bytes 伪装成完整 transcript。超限由 Core 通过 terminalization barrier 决定业务终点，再以 exact cleanup command 收口，Supervisor 不能自行把超限解释为失败。Transcript/stdio bytes 只写 owner-only 控制对象，不得进入 argv、普通事件、错误文本或审计日志；对外只暴露有界摘要与非敏感 reason code。

Supervisor 只能操作其 handshake 时持有的控制目录、socket、FD、child 与 process group。禁止全机 `ps`、argv 匹配、PID-only/PGID-only kill、递归删除或跨 Attempt 信号。只有 `process-supervisor-closed`（锚 exact close receipt）→`cleanup-completed`→`lease-released` 全部按顺序耐久后，生产 GC 才可按 held identity 删除本 Attempt 的非权威 socket/nonce/transcript projection；authority facts 永久 append-only。

### 8. 必须通过的故障与 hostile 矩阵

- Core crash 于 supervisor start、handshake、fact CAS、`bind-authority`、spawn、exec-stop、`process-started`、resume、运行、ResultIngress admission/barrier、TERM/KILL/wait、collect、allocation terminate、close、cleanup 与 release 的每个边界；
- 同一 Attempt 连续两次 Core 重启与 command response loss；重复 spawn/resume/terminate/collect 的 same/different digest；
- supervisor crash、拒绝启动、协议不兼容、旧 supervisor 与新 binary upgrade；
- socket/control parent symlink、hardlink、path swap、inode/mode/owner/link-count 漂移，双向 peer UID/PID birth/fixed-binary 不符，旧/并发 Core、owner epoch replay，nonce 缺失/泄露，binary SHA/CDHash/sourceHead 漂移；
- 伪造 child observation、PID/PGID reuse、runtime/cwd/material FD drift、helper 冒充 runtime、child detach/reparent/new session；
- exact v1 frame/journal/argv/env/stdin/output/command/deadline 上限逐项越界、cancel、timeout、non-zero 与 normal exit；
- journal/authority/event/error/log 对 raw argv、env value、stdin、auth token 与 transcript bytes 的零持久化/零回显扫描；
- 非当前 orchestrator、stale generation、错误 cleanup binding/authority head 的零 signal、零 release；
- barrier 先赢后的 late result quarantine、terminate 不引用未来 process-terminal、close receipt 前零 cleanup-completed、cleanup-completed 前零 lease release；
- 全链只执行固定 Marshal 与真实 runtime，不创建或执行匿名临时 Mach-O。

## 实施门禁

1. 先接受本 ADR，再修改 process lifecycle 或持久事实；不得以实现候选反向定义合同。
2. 最小实现沿现有 RB2 接线：`internal process-supervisor` hidden dispatch → supervisor protocol/server/client → process coordinator → sandbox launch → 同一 Attempt authority facts。必须先为 `process-supervisor-started`、`process-supervisor-closed` 与 `cleanup-completed` predecessor 增加 closed schema/replay/CAS，再做 mechanics 副作用；禁止先落一个生产不可达 daemon 再宣称完成。
3. Supervisor 代码完成后仍保持 production unreachable，直到 ADR 0057 的 Stage2 allocation authority 与唯一 `ProductionRuntime/PublicApplicationPort` 同时接线。
4. 单元/故障注入执行证据可由 required macOS/Linux CI 提供；企业 Mac 本机只运行固定 `bin/marshal` canary，不执行随机 Go test Mach-O。
5. 最终必须在同一 sourceHead 上证明真实 Pi Run、Core 两次重启、lost response、late result、cleanup-before-successor、独立 ReviewDecision/`ACCEPTED`；同时机器扫描 mechanics journal/authority/event/error/log，证明 raw argv/env/stdin/token/transcript bytes 未泄漏。Supervisor crash 仍必须诚实产生 intervention，不得写成自动恢复成功。
6. 本 ADR 不解除 Issue #212 的 signing/notarization、Linux stable gate 或 stable installer fail-closed 条件。

## 后果

正面后果：

- Control Core 可以在不重建 child、不丢失 wait right/FD/pipe 的情况下重启；
- 进程 mechanics 与业务 authority 分权，Supervisor 无法自行决定生命周期或发布；
- 固定 Marshal 内部子命令消除随机临时可执行文件与重复安全审批；
- 相同命令有耐久 hash chain，可判定 response loss、重连与重复请求。

代价与限制：

- 每个 live Attempt 增加一个长寿命普通用户 supervisor、owner-only socket 与协议状态；
- supervisor 自身崩溃仍不可自动恢复，只能 intervention；
- ordinary-user 同 UID 边界仍不是恶意代码隔离；
- ProductionRuntime 必须负责 owner lock、重连、升级兼容与有界 GC，不能继续由 CLI/server各自装配。

## 拒绝的替代方案

### 重启后按 PID/PGID/path 重新附着

拒绝。Darwin 不会把原 wait right、tracer、held FD 与 pipe 转移给新 Core；PID/path 重验不能恢复这些 mechanics，也不能排除 reuse 与跨编排目标。

### Core crash 后把旧进程视为已退出

拒绝。这会提前 release、unlock 或创建 successor，使旧 workload 与新 Attempt 双活。

### 为每次 Attempt 构建或复制独立 helper

拒绝。随机 Mach-O/CDHash 会触发企业安全策略，也制造不可稳定审计的执行身份。唯一 helper 是当前固定 Marshal 的内部子命令。

### 让 Supervisor 成为第二个 Control Plane

拒绝。Supervisor 只保存 mechanics，不拥有业务 authority、调度、重试、ReviewDecision、unlock、successor 或 publication 权限。

### 用 launchd/系统级服务扩大 v1 范围

拒绝。v1 只需要单用户、per-Attempt 的进程 mechanics owner；系统安装、跨用户 service、远程 HA 与通用 daemon 管理属于后续版本。
