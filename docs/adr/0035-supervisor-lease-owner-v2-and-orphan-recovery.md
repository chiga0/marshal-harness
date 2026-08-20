# ADR 0035：Supervisor lease owner V2 与 orphan recovery

- 状态：Proposed
- 日期：2026-08-21
- 关联：Issue #130、ADR 0018、ADR 0019、ADR 0036

## 背景

当前本机 Run lease 已使用 `lease.lock`、`lease.lock.owner`、Run journal 和 Supervisor 扫描，但 owner record 存在两个历史形状，owner acquisition 与 Attempt recovery 也尚未由一个完整耐久事务约束。PID、heartbeat、事件年龄或进程表若被误当成 authority，可能造成 pathname takeover、ABA、错误终止进程或晚到结果进入当前 Evidence。

本 ADR 提议冻结 `LeaseOwnerRecordV2`、legacy v1 fail-closed migration、descriptor-bound owner acquisition epoch/high-water fencing，以及 `SupervisorDispatcher` orphan recovery 的 append-only durable transaction。它不实现这些机制，不接受 ADR，也不表示 Issue #130 完成或自动恢复已生产启用。

## 决策

### 1. authority、诊断与三态判断

本机活 owner 的唯一肯定证明，是对 exact `lease.lock` descriptor 持有 OS advisory exclusive lock。该证明只说明本机互斥所有权，不是 hardened authority、恶意代码隔离或跨主机 lease。

`pid`、`processStartedAt`、`heartbeatAt`、owner record、journal 事件年龄与进程表全部只作诊断。禁止 pre-lease unlock、pathname takeover、PID escape hatch 与跨编排 kill；Marshal 只能管理本次编排拥有且由当前 descriptor authority 精确绑定的进程。

共享 typed predicate 的输入固定为：current Attempt 的 journal tail 必须是 `worker.started`，其 actor、`attemptId`、Attempt generation 必须匹配；再输入冻结的 staleness threshold 和 tri-state ownership。输出字段逐字为：

- `livenessClass`：`live|not-live|unknown`；
- `ownershipBasis`：只能描述 exact descriptor lock observation，不能由 PID 或 argv 升级；
- `stalenessBasis`：绑定 journal tail、阈值及观察时间，事件年龄不能单独判 dead。

watchdog、doctor、`SupervisorDispatcher` 与 execution 必须调用同一个版本化 typed predicate。任一输入缺失、损坏、冲突、不可安全重读或谓词版本未知，统一执行 `unknown -> intervention`：zero side effect，不抢锁、不覆盖 owner、不 quarantine、不追加 `RETRY_PENDING` 或 `BLOCKED`，也不 Signal/kill。Core 只追加 typed `Outcome` 记录，保存 reason、观察摘要和所需人工动作；不得静默把 unknown 写成 `BLOCKED`。

### 2. legacy v1 的 exact closed schema

legacy v1 只承认下列两个历史真实形状；两者均为 exact closed schema：字段集合必须完全相等，拒绝 duplicate member、未知字段、缺字段、错误 JSON 类型、非规范时间、超限、symlink、非 single-link regular file 与读取中 identity/size 变化。

`legacy 5-field` 的字段恰为：

1. `token`：非空 string；
2. `pid`：非 boolean 的 integer，范围 `2..2147483647`；
3. `processStartedAt`：RFC3339 UTC timestamp；
4. `acquiredAt`：RFC3339 UTC timestamp；
5. `heartbeatAt`：RFC3339 UTC timestamp。

`legacy 7-field` 的字段恰为上述五项加：

6. `device`：大于零的 JSON integer；
7. `inode`：大于零的 JSON integer。

不得把“能解码进当前 Go struct”当成 schema validation，也不得猜测其它历史 shape。损坏、未知版本、额外字段或介于两者之间的 shape 一律进入 intervention。

### 3. LeaseOwnerRecordV2 的 exact closed schema

`LeaseOwnerRecordV2` 使用 duplicate-key-rejecting parser、RFC 8785 JCS 与 `additionalProperties=false` 的等价语义。字段恰为：

- `schemaVersion`：integer，固定为 `2`；
- `runId`、`orchestratorId`：非空、已验证的 typed ID；
- `pid`：非 boolean integer，范围 `2..2147483647`；
- `processStartedAt`、`acquiredAt`、`heartbeatAt`：RFC3339 UTC timestamp，且满足 `processStartedAt <= acquiredAt <= heartbeatAt`；
- `leaseDevice`、`leaseInode`：分别绑定 held `lease.lock` descriptor 的 device 与 inode，均为大于零的 integer；
- `currentAttemptId`：当前 Attempt 的精确 ID；尚无 Attempt 时只允许 JSON `null`，不得用空字符串；
- `acquisitionEpoch`：owner acquisition epoch，大于零且按本 Run 单调递增；
- `attemptGeneration`：Attempt generation，非负且与当前 ledger 精确一致；
- `previousOwnerDigest`：前一个已提交 owner record digest；genesis 只允许 JSON `null`；
- `recordDigest`：本记录移除自身后，对其余完整对象作 RFC 8785 JCS 并计算 `sha256:<lowercase-hex>`。

以上 JSON 名称是 wire contract；正文中的 LeaseOwnerRecordV2、currentAttemptId、previousOwnerDigest、acquisition epoch 与 attempt generation 是同一字段语义。owner acquisition epoch 与 Attempt generation 必须严格区分：前者标识每次成功取得同一 Run owner authority 的 owner 世代；后者标识 Attempt execution/fencing 世代。重新取得 owner 必须增加 acquisition epoch，但不能据此创建或增加 Attempt generation；只有 Core 的合法 Attempt transition 才能增加 attempt generation。

### 4. descriptor-bound legacy migration

v1 migration 是 fail closed 的 `legacy migration`，固定顺序为：

1. 逐段 nofollow 打开 Run directory 和 exact `lease.lock`，验证 single-link regular file，并在该 descriptor 上取得 OS advisory exclusive lock；取得前不得写 owner、解锁或修改任何 lifecycle 状态。
2. 持有同一 descriptor authority 期间读取 legacy owner 一次，按第 2 节区分 exact 5-field 或 7-field；7-field 的 `device/inode` 必须与 held descriptor 一致，5-field 不得伪造历史 descriptor binding。
3. 执行 current-ledger recheck，绑定 `runId`、当前 state、`currentAttemptId`、attempt generation、owner high-water 及 canonical Run directory/lock descriptor identity。
4. 生成 successor `LeaseOwnerRecordV2`：新 acquisition epoch 精确为 high-water 加一，`previousOwnerDigest` 绑定 legacy 原始 canonical digest；使用 crash-safe durable commit 写临时 single-link regular file、file fsync、descriptor-relative rename 和 directory fsync。
5. 重读已提交 V2 并复核 digest、epoch、previous digest、device/inode 及 held descriptor 后，才允许首个后续副作用。

legacy 5-field 缺少 descriptor identity 不构成 takeover 依据；只有已经取得同一 descriptor authority 才能迁移。任何锁竞争、path/descriptor swap、owner 读取错误、high-water 缺失或迁移 commit 不确定都进入 intervention，并保持 zero side effect。

### 5. epoch+digest high-water 与 successor chain

Core 必须在 owner record 之外维护 append-only、consumer-owned 的 `epoch+digest high-water`。每个 committed 条目绑定 `runId`、acquisition epoch、`recordDigest`、`previousOwnerDigest`、lease device/inode、事务 ID、前一 high-water digest 和提交时间。

合法 successor chain 必须同时满足：epoch 精确加一、`previousOwnerDigest` 精确指向前一 committed owner、descriptor/path identity fencing 通过、high-water CAS 成功。相同 epoch + 相同 digest 仅允许幂等 replay；`same-epoch different-digest` 必须机械拒绝。更小 epoch、旧 snapshot 或断链记录执行 rollback rejection；更大但跳号的 epoch 也拒绝，不能自行补洞。

owner 文件丢失、账本截断、high-water 回退、owner 与 high-water 冲突或 provider/本地提交结果不确定都 fail closed 进入 intervention。不得仅凭当前 pathname 上的新文件重建 high-water。

owner acquisition 的 crash-safe durable commit 使用 append/prepare 与 high-water CAS，保证崩溃后只能重放同一 transaction，不能产生两个 successor。descriptor/path identity fencing 必须在 prepare 前、high-water 消费前和副作用前分别重检 held fd 与 canonical pathname 仍指向同一 device/inode。

### 6. SupervisorDispatcher 的边界

`SupervisorDispatcher` 只做只读 scan/admission，并启动精确单 Run 的 Marshal CLI driver。driver argv 必须使用 exact-run filter，冻结 `runId` 和预期 ledger sequence；禁止扫描一个 orphan 时顺带启动其它遗留 `READY` 或 `REWORK_REQUESTED` Run。

`SupervisorDispatcher` 不决定 lifecycle、预算、fencing、quarantine、`Outcome` 或 retry。所有这些决定仍只属于持有当前 Run lease、完成 current-ledger recheck 的 Core。两个 dispatcher 并发时，最多一个能取得 exact descriptor authority；失败者只记录诊断并退出，不得等待后改走 pathname takeover。

### 7. orphan recovery 的 append-only durable transaction

每个候选 orphan 使用唯一事务身份，绑定 `runId`、currentAttemptId、attempt generation、owner acquisition epoch、owner digest、`worker.started` journal tail digest、staleness predicate digest、lease device/inode 与起始 expected sequence。事务记录只追加，不覆盖历史，状态固定为：

`prepare → fence-consumed → inspect/reconcile → resolved`

- `prepare`：在 current-ledger recheck 后追加候选、三态谓词和完整绑定；此时禁止 quarantine、retry、terminal transition 或进程动作。
- `fence-consumed`：原子消费该 Attempt generation 的 recovery fence，使 late `WorkerResult` 与旧执行 handle 不能进入当前 Evidence；重复消费同一事务幂等，不同事务冲突。
- `inspect/reconcile`：重新观察 journal、owner successor chain、descriptor/path identity、attempt outputs、预算和 typed failure authority。late WorkerResult 只进入绑定该事务、原 attempt/generation 与内容 digest 的 quarantine 记录，不能成为新 Attempt 的 WorkerResult。
- `resolved`：同一 compare-and-append 决定唯一结果并追加 typed Outcome；允许的确定结果为合法进入 `RETRY_PENDING`、预算耗尽后合法进入 `BLOCKED`，或保持非终态并进入 intervention。resolved 记录绑定全部前序 digest、quarantine manifest、Outcome digest 与最终 ledger sequence。

`Outcome` replay 必须以 transaction ID、Outcome digest 与 expected sequence 幂等；同 ID 不同内容拒绝。任何阶段崩溃都从账本恢复同一事务，禁止新建平行事务或删除 prepare。unknown enters intervention，zero side effect；intervention 的 Core Outcome 只是追加诊断事实，不伪造 terminal `BLOCKED`。

### 8. eligibility 与副作用前重检

共享 eligibility 只有在以下条件全部为肯定时返回 eligible：journal tail 是 current Attempt 的 `worker.started`；actor 精确为既定 Core worker runner；attemptId/currentAttemptId、attempt generation、owner epoch/digest、descriptor device/inode、staleness threshold 和 tri-state ownership 全匹配；事务无冲突且预算事实可判定。

所有时变判断都必须在每个副作用前做 current-ledger recheck，包括 acquisition/high-water commit、fence consume、quarantine install、`RETRY_PENDING`/`BLOCKED` transition、Outcome append、启动 successor Attempt 与任何 Signal。重检失败不沿用旧观察。

### 9. crash、ABA 与 late-result matrix

实现前必须用确定性 fixture 覆盖：

| 场景 | 必需结果 |
| --- | --- |
| descriptor/path swap，含 swap-back ABA | identity fencing 拒绝；intervention；zero side effect |
| legacy owner 损坏、未知版本或非 exact shape | 不迁移、不覆盖；intervention |
| owner record 写入或 fsync/rename 后崩溃 | 只恢复同一 transaction；无双 successor |
| takeover 前崩溃 | 无 fence 消费、无 lifecycle 副作用 |
| takeover/high-water 后、owner commit 前后崩溃 | 依据 append-only prepare/CAS 对账，不回退 epoch |
| 两个 dispatcher 竞态 | 唯一 descriptor owner；另一方零副作用 |
| late WorkerResult | attempt/generation mismatch 时 quarantine，永不接纳为当前 Evidence |
| currentAttemptId、attempt generation 或 actor mismatch | intervention，不生成 retry authority |
| budget exhausted | 仅在事实确定且事务绑定完整时追加 `BLOCKED` 与 typed Outcome |
| Outcome replay | 相同 digest 幂等；不同 digest/sequence fail closed |
| same-epoch different-digest 或 rollback | high-water 机械拒绝并审计 |
| 任一观察 unknown | intervention；不静默 `BLOCKED`，不产生其它副作用 |

测试还必须证明事件年龄单独变旧不会判 dead，PID 消失不会绕过 descriptor lock，旧 generation 的 late result 不会被 successor Attempt 消费，并证明 exact-run driver 不会启动扫描中其它 Run。

## 与既有 ADR 的关系

本提案补充 ADR 0018 的 generation/fencing/current-ledger recheck 与 ADR 0019 的 append-only reconciliation。若既有实现或 ADR 对 PID escape hatch、legacy 宽松解码、unknown 终态化或先 quarantine 后 durable prepare 的解释与本文冲突，保持 fail closed 并等待维护者裁决，不静默放宽。

ADR 0036 只冻结 `Adapter.Run` 错误的 retry authority，不为 orphan recovery 自动生成 typed retry authority；本 ADR 的事务必须独立证明 eligibility、预算和 lineage。

## 后果与实施门禁

该设计增加 owner ledger、事务记录、严格解析、fsync/CAS 与恢复测试成本，但把 owner epoch、Attempt generation、late result 和 orphan lifecycle 收敛到可回放、可审计边界。

在本 ADR 被维护者接受、Schema/negative fixtures、crash matrix、watchdog/doctor/dispatcher/execution 共享谓词、跨平台 descriptor lock 行为和独立验证全部完成前：

- ADR 状态保持 Proposed；
- legacy v1 自动迁移和自动 orphan recovery 不得生产启用；
- 不得宣称 Issue #130 完成；
- 不得据此改变 M10–M13 状态。
