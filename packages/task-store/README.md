# Node Task SQLite Store

依据 Accepted ADR0088 的内部事务后端，已接同一正式 Node Application/Service 调用链；本包不独立授予发布权限。只使用 Node 内置 `node:sqlite`，不编译 Go、安装 addon 或加载 SQLite 扩展；运行时 gate 为 Node 24.x 且至少 24.15，发行包固定 Node 24.15.0；平台仅 Darwin/Linux 的本地文件系统，各平台仍须分别验证。

## 打开与 owner

```js
import { Store, encode, digest, makeEvent } from './store.mjs';

const store = Store.create('/absolute/private/new-data');
// 已有根只能 Store.openExisting('/absolute/private/new-data')。
const owner = store.claimOwner(store.info().generation, 'service-instance-id', Date.now() + 60000);
```

格式为 `marshal-node-task-sqlite/v1`，仅独立新空根；旧 `.marshal`、实验 JSON、未知/缺少文件、坏格式或不兼容 schema 拒绝，不初始化或迁移。`format` 文件只是不可变初始化标识，不保存业务/owner 状态；全部业务权威在 `authority.sqlite`。Create/Open 持有并复查路径/文件对象，返回前都执行子目录→父目录同步。同步失败不删除不确定状态；后续 Open 必须再次同步，不能绕过。这里不承诺防御同 UID 恶意程序，也不把进程中断测试称为物理断电实验。

显式新 profile 另提供 `CUSTODY_FORMAT`（v2-custody，ADR0089）与 `INTERACTION_FORMAT`（v3-interaction，ADR0090）；v3 保留 v2 的 preclaim 封闭观察/cleanup-only 语义，增加同库运行中问题、答案/投递/ACK 事实。调用者必须选择精确格式，旧 reader 在 SQLite 打开/claim 前拒绝不同 sentinel；没有自动迁移或依据磁盘输入重造旧权威。

`REPAIR_FORMAT`（v4-repair，ADR0091）与 `UNPERMITTED_FORMAT`（v5-unpermitted，ADR0092）同样只用于原精确新根。v5 的 sentinel、metadata、SQLite user_version 均为独立版本；它不是 v4 reader 可忽略的字段。原 `inspectRecovery` 保留物理排他锁下、claim 前的只读边界。Store 只提供事实查询，不从没有事件直接判断进程未运行。

`WORKER_CANCELLATION_FORMAT` 为 `marshal-node-task-sqlite/v6-worker-cancellation`（ADR0093），sentinel/metadata/user_version 精确为6；同一原 Store 承载目标 stop，不另建账本。v1–v5 reader 在 claim 前拒绝 v6，v6 不迁移旧根。格式识别不授予0092准备资格，也不把未知执行当已停止。

单个 SQLite 连接使用 EXCLUSIVE locking mode，物理锁跨短事务保持至关闭。第二进程不能因 lease 到期抢占仍持锁的进程；原连接关闭/退出后才可打开，再显式 Claim 新 generation。此锁只证明数据库 writer 互斥，不能证明旧 Worker 停止、执行目录可复用或旧结果已被接纳。`info()` 仅返回 `{format, storeId, generation}`；重新打开不继承旧 owner，`renewOwner(owner, expiresAtMs)` 不能代替首次 Claim。

Owner 为 `{storeId, generation, instanceId, expiresAt}`。序号、revision、generation 输出 BigInt；输入可为 BigInt 或安全整数，时间为安全整数 Unix 毫秒。HTTP 编码由 Application 明确处理，不隐式丢精度。

## 单一同步事务

`store.read(owner, callback)`、`store.write(owner, callback)` 都是短同步事务。callback 禁止 async/Promise、嵌套 Store 操作或事务外继续使用 Tx。任一 Tx 方法错误使整个事务不可提交，即使 callback 捕获异常。所有 SQL 参数绑定，数据库句柄不暴露；异常仅返回封闭 `StoreError.code`，不输出 SQL、路径或 payload。

没有公共 `save(nextState)`、通用 SQL 或自动业务 reducer。Core 在原事务中完成状态/批准/预算及执行证据校验；外部 Agent、协议发送、文件写入和业务验证均在事务外。

| 方法 | 参数与返回 |
| --- | --- |
| `makeEvent(stream, sequence, payload)` | 返回 `{sequence, digest, bytes}`；规范 envelope 绑定 stream 与十进制 sequence |
| `tx.head(stream)` | `{sequence, digest}`，空 head 为 `{sequence:0n,digest:''}` |
| `tx.events(stream, after=0n, limit=100)` | 原始 event 数组，bytes 为独立 Buffer |
| `tx.eventsWithField(stream,field,value,limit=100,types=null)` | 单 stream 的精确 payload 字段选择，可限原 event type；返回原 event 与 generation。完整匹配超过 limit 则拒绝，不能当截断尾页或否定证明 |
| `tx.append(stream, expectedHead, events)` | 原子追加，返回最终 Head |
| `tx.projection(kind,id)` | null 或 `{kind,id,revision,source,bytes}` |
| `tx.projections(kind,afterId='',limit=100)` | 按 ID 分页，返回上述 projection 数组 |
| `tx.putProjection(kind,id,expectedRevision,source,bytes)` | 返回新 revision；0 仅创建不存在的 projection |
| `tx.receipt(scope,operation,keyDigest,requestDigest)` | null 或 `{scope,operation,keyDigest,requestDigest,source,bytes}` |
| `tx.receiptByKey(scope,operation,keyDigest)` | 同一闭集三元键读取原 receipt；即使请求摘要不匹配仍返回原事实，恢复不能 catch conflict 后当作不存在 |
| `tx.putReceipt({scope,operation,keyDigest},requestDigest,source,responseBytes)` | 原回执不可覆盖；完全相同写入无变化 |
| `tx.enqueue(command)` | 返回完整 Command；同 ID 精确重放不重置状态 |
| `tx.command(id)` / `tx.commands(afterId='',limit=100)` | null/单 Command 或分页数组；只读取，不发送 |
| `tx.observeCommand(id,expectedRevision,status,source)` | 返回新 revision；pending→unknown/observed、unknown→observed，不自动重试 |

Source 固定为 `{stream,sequence,digest}`，引用同一数据库的已校验事件。Projection kind 为 `task/node/attempt/budget/interaction/artifact/provider/operation`；预算用 `budget` projection 加原事件，不另建平行余额真值。

Enqueue 输入为 `{id,taskId,nodeId?,attemptId?,kind,inputDigest,payload,source}`，kind 为 `start/stop/answer/verify`，payload 为规范 bytes。返回值补充 `generation/revision/status/observation`，可选 ID 缺省为空字符串；首次 revision=1n、status=pending、observation=null。新命令及新 observation 的 source 必须来自当前 generation；已有 unknown/pending 命令可读且不因重开/重放被重发。Observation 是 Core 校验后的事实引用，Store 不声称自证外部执行结果。

认证在进入 Application 时完成；在同一 write 中先 `receipt`，命中即返回原 bytes，未命中才执行 revision CAS/reducer。事件、Task/预算投影、回执和 outbox 可以在这一事务一起提交。制品 bytes 先独立耐久保存，之后仅将引用作为 Core 事件/投影写入，不能把未完成的文件写入放进 callback。

`encode(value)` 是该新格式唯一规范编码器：有限 JSON、排序对象键、拒绝不完整 Unicode/循环/undefined/非有限数等。`digest(bytes)` 只接受原 bytes；不得拿普通 JSON.stringify 顺序代替批准摘要。旧 Go 账本保持旧格式，不由本库重编码。

固定边界：单记录 1 MiB、事务累计已处理 bytes 8 MiB、512 次计费访问、分页 100、事务期限 5 秒、SQLite busy wait 100 毫秒。引用重读也计费：单条 projection/pending 命令需要 2 次，observed 命令需要 3 次；512 保证小记录的最大合法页 100 能完成这些校验，组合事务仍受累计限额约束。大 bytes 页仍可能触及 8 MiB 限额，调用者须明确报告或采用较小显式页，不能把 limit 当成末页。同步 API 不由 setTimeout 抢占；期限在每次方法及提交前检查，callback 仅限可信短存储工作。`clock/monotonic/syncDirectory` 构造选项仅作可信组合/确定性测试注入，不接受 HTTP 配置。

## 定向回归

```sh
node --test --test-concurrency=1 --test-name-pattern='^core:' packages/task-store/store.test.mjs
node --test --test-concurrency=1 packages/task-store/store.test.mjs
```

固定脚本 `fixtures/process.mjs` 只操作测试新建目录，验证持锁竞争以及提交前/后进程退出；它不是 Worker。测试涵盖全事务故障回滚、原回执/预算/outbox 重放、冷打开、新 owner、未知命令、格式/文件损坏、目录同步、精确记录数与累计读取计费、真正多事件批量写读和事务过期。累计 8 MiB 的读取计费不是 8 MiB 写入吞吐证明。完整业务、Agent 取消与遗留执行恢复仍须由正式 Application/Execution 组合独立验证。
