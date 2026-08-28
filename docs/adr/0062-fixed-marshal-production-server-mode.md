# ADR 0062：固定 Marshal 的生产 server mode 与唯一 composition root

- 状态：提议（Proposed，2026-08-29）。本 ADR 只冻结 v1 Mac-first 的生产二进制与入口拓扑；未经独立复审和维护者接受不得把独立 `marshal-server` 加入 trusted binary set，也不得据此宣称 production reachable。
- 关联：[ADR 0047](0047-marshal-darwin-self-identity-and-release-signing.md)、[ADR 0052](0052-v1-release-scope-and-production-reachability.md)、[ADR 0057](0057-durable-local-allocation-recovery-and-production-composition.md)、[ADR 0059](0059-fixed-darwin-process-supervisor.md)、[ADR 0060](0060-supervisor-mechanics-authority-binding-and-recovery.md)、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)。

## 背景

ADR 0057 要求 CLI 与 loopback server 都只调用唯一 `PublicApplicationPort`，并禁止 server 通过子 `marshal task run` 重建业务流程。ADR 0059 又要求启动 per-Attempt Supervisor 的当前进程自身必须是 fixed Marshal binary。

现有独立 `cmd/marshal-server` 不是 fixed `bin/marshal`，却仍以 child-exec 和环境 selector驱动 CLI。若让它直接启动 Supervisor，就必须扩大 trusted binary set、安装收据与代码签名对象；若继续 child-exec，则保留第二套 composition与 response-loss gap。两者都偏离 v1 最短纵切。

## 决策

### 0. 取代与术语

1. 本 ADR精确取代 ADR 0052 §1 第 1、7 项和 §3 中把独立 `cmd/marshal-server`/`marshal-server` executable列为 production composition root的部分；“loopback server”产品角色与启动、取消、查询、恢复真实 Run的要求保留，但该角色改由 fixed `marshal` 的 server mode承载。
2. 本 ADR精确取代 ADR 0057 §7 第 2、3 项及相关后果中把“loopback `marshal-server`”解释为独立 executable的部分；唯一 `ProductionRuntime`、同一 in-process `PublicApplicationPort`、禁止 direct execution/child CLI/env selector的合同全部保留。
3. 本文后续“server mode”固定指 `marshal control-plane serve` 进程内 adapter；“独立 `marshal-server`”固定指 `cmd/marshal-server` 构建出的兼容 executable。两者不得混用生产证据。

### 1. 唯一生产二进制与 server mode

1. v1 Mac-first 的唯一生产可执行身份是最终发布资产中的 fixed `marshal`。生产 loopback server 必须作为该二进制的固定 server mode运行，命令面冻结为 `marshal control-plane serve`；它与 CLI mutation使用同一个 `ProductionRuntime` factory、`PublicApplicationPort`、authority store与恢复控制器。
2. `cmd/marshal-server`/独立 `marshal-server` 不进入 v1 trusted binary set。它只能保留为开发/兼容 transport入口，默认不得打开生产 authority store、创建 `ProductionRuntime`、启动 Supervisor、执行 mutation或 child-exec `marshal task run`，也不能作为 R1–R6/RC的 production reachability证据。若未来要恢复独立 production binary，必须另立 ADR并扩展安装、签名、receipt、upgrade与 revocation authority。
3. server HTTP handler只持有注入的 `PublicApplicationPort`。它可以保留 transport-scoped request/idempotency delivery ledger，但不能拥有 Run/Attempt/lease/result/review业务真值，也不能读取 composition selector选择另一条执行路径。

### 2. 单实例 ownership 与 CLI 行为

1. 每个可信 repository scope 同时最多一个持有 durable owner acquisition/epoch 的 `ProductionRuntime`。owner lock在打开 RB1、Provider/Agent/lease/allocation stores与任何 probe/provision/process副作用前取得。
2. fixed Marshal server mode持有 owner lock时，另一个 CLI进程不得再本地装配第二个 writer。CLI mutation必须经第 3 节冻结的 authenticated loopback API交给该 server，或返回 typed owner-busy/unavailable；read-only status可以读取受支持的只读 projection，但不能据此签发 mutation authority。
3. server未运行且 owner lock可取得时，fixed Marshal CLI可以临时创建同一个 `ProductionRuntime`并直接调用其 in-process Port，完成有界 operation后释放。两种入口调用同一 application method，不允许 CLI/server分别维护 planning/execution/lifecycle分支。
4. owner epoch、current Run/Attempt head与 request idempotency在每次 mutation前重验。CLI到 server的 delivery retry只能重放同一 application intent，不能创建第二 Attempt或第二 Supervisor command。

### 3. Authenticated loopback、delivery ledger 与禁止旁路

1. production loopback使用 owner-only held control directory中的 descriptor-relative AF_UNIX socket承载HTTP；parent/socket的 device/inode/type/UID/GID/mode/link-count在 bind前后与每次重连时重验。路径字符串、TCP端口、PID或 argv不能成为 authority。
2. server handshake/response绑定 protocol revision、repository identity digest、authority namespace/control-plane/root digest、current owner acquisition/epoch/fact digest、fixed server process PID/birth与 binary path/SHA-256/CDHash/sourceHead/profile、endpoint identity。Client使用 peer credential与 current RB1 owner fact重验同一进程/二进制/owner；旧 endpoint、PID reuse、owner或 binary漂移均 fail closed。
3. 每个 mutation request绑定 request/idempotency key、expected owner acquisition/epoch、repository/authority root、application intent digest、deadline与调用者身份；业务 reply必须引用 exact RB1 application authority receipt/fact digest及 post revision/head。缺 exact receipt时只能返回 pending/unknown，不能返回业务成功。
4. transport delivery ledger是可删除、可从 RB1重建的 `pending|receipt-ref` projection，只保存 secret-safe request digest与 exact RB1 receipt reference，不创建 Run/Attempt/lease/result/review authority。response loss/restart后必须按 application intent digest从 RB1 reconcile：exact receipt存在才补 `receipt-ref`；不存在则保持 pending并只重放同一 intent；同 scope/key不同 digest固定 conflict。transport record本身永远不能授权 mutation或回答成功。
5. endpoint nonce/token只存在owner-only control object与有界内存，不进入RB1、transport ledger、event/error/log；它只能认证 transport，不能替代 owner/RB1 recheck。

禁止旁路与平台边界：

- 生产 source/import graph禁止 `MARSHAL_EMBEDDED_SANDBOX`、`MARSHAL_WORKER_EXECUTOR`、`MARSHAL_PRODUCTION_GATE`改变 composition；这些变量出现时不得选择 legacy path。
- CLI/server不得直接调用 `execution.Run`、`Adapter.Run`、Sandbox SPI、`processcontrol.Coordinator`、`processsupervisor.Client`或 ResultIngress mutation；只有 `ProductionRuntime` 装配具体实现。
- server不得 child-exec `task run`，不得通过 argv/env把权限传给第二进程。
- non-Darwin profile在 owner store、Probe、Provision、Attempt 或 Supervisor mutation前返回 typed `platform-profile-unavailable`。这不宣称 Linux production support。
- 固定二进制 path/SHA-256/CDHash/sourceHead/profile与安装 receipt任一漂移均 fail closed；禁止临时/匿名 Mach-O helper。

### 4. 发布与可观察性门禁

纯 unsigned/adhoc `v1.0.0-rc1` candidate只能执行离线 build、release-contract、checksum以及 ADR 0047允许的 `version/help/doctor --self`，不得启动 Pi、改变 lifecycle或作为真实 canary。真实 RC canary必须执行最终 dist中**同一份 bytes**的 fixed `bin/marshal`，且该 candidate已按 ADR 0047/0051取得 current `darwin-managed-development`签名、allowlist、安装 receipt与 high-water；或取得完整 notarized release identity。canary identity与最终发布资产SHA-256/CDHash/sourceHead必须逐项相同，不得用另一份开发 binary替代，并证明：

1. server mode取得唯一 owner并启动真实 Pi Attempt；
2. server重启后从 RB1恢复同一 Attempt，随后 CLI经同一 Port完成 status/verify/review；
3. exact Supervisor bootstrap/command/journal、ResultIngress admission、terminal/allocation/close/cleanup事实链可观察，pending intent/effect/intervention为空；
4. legacy selector、independent server child-exec与 `processcontrol` production seam调用计数为零；
5. 两次新进程读取一致并最终到独立 ReviewDecision/`ACCEPTED`。

`doctor --json`/`status --json` 只能输出 secret-safe composition/authority digest与状态；不得输出 raw nonce、控制路径、argv/env或 transcript bytes。managed-development签名/allowlist只授权该 exact RC在声明的Mac-first ordinary-user范围内运行，不等于 Developer ID/notarization或 stable release；RC通过不解除稳定版 signing/notarization与 Linux authority gate。若当前主机无法 provision该 identity，只能生成未验证 candidate，不能发布为“可用 RC”。

## 必须通过的负面与崩溃矩阵

- fixed server与 CLI并发争 owner、旧 owner epoch/port重放、server response丢失后的相同/不同 request；
- socket parent/entry ABA、peer credential不符、PID reuse、旧 endpoint/owner/binary、transport pending伪造success、receipt-ref指向错误RB1 fact；
- 独立 `marshal-server` mutation、child `task run`、legacy selector任意值、legacy Adapter/processcontrol panic sentinel；
- server crash于 application intent、Supervisor command intent/receipt、ResultIngress admission、terminal/close/cleanup每个边界；
- fixed binary/path/CDHash/sourceHead/profile漂移与 PID reuse；
- unsigned/adhoc candidate尝试 lifecycle/Worker必须在任何Run/Attempt/Supervisor副作用前拒绝；canary bytes与dist/manifest漂移必须拒绝；
- non-Darwin mutation零 ledger/Run/Attempt/Probe/Provision副作用；
- server与 CLI读取不同 authority root、重复 Attempt、重复 Supervisor或重复 effect必须机械失败。

## 后果

生产信任集合保持一个固定 Marshal binary，避免为独立 server增加第二套签名/收据/升级 authority；CLI与 server也不再通过子进程和环境变量复制业务装配。代价是独立 `marshal-server` 在 v1降为非生产兼容入口，生产 server用户必须使用 fixed Marshal 的 server mode。
