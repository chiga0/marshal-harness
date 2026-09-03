# ADR 0076：Darwin fixed server 的 AF_UNIX pathname locator 与 held-descriptor authority

| 字段 | 值 |
| --- | --- |
| 状态 | 已接受（Accepted） |
| 日期 | 2026-09-02 |
| 提议基线 | `main@2432af523cbe7cae42724a920ceca5549bd550ed` |
| 接受基线 | `main@d9cd001389adf19698f74083124e0a6d87039bae`；接受只冻结合同，不表示 fixed server 已实现或通过真实 canary |
| 关联 ADR | [ADR 0052](0052-v1-release-scope-and-production-reachability.md)、[ADR 0062](0062-fixed-marshal-production-server-mode.md)、[ADR 0064](0064-darwin-control-directory-phased-identity.md)、[ADR 0066](0066-production-composition-owner-acquisition.md)、[ADR 0068](0068-mac-first-cli-only-lifecycle-preview-rc1.md)、[ADR 0069](0069-attempt-reservation-and-existing-worktree-allocation.md)、[ADR 0073](0073-dogfood-activation-v2-host-portability.md) |
| 关联范围 | `marshal control-plane serve` 的首个 Darwin loopback transport 切片；不实现远程、TCP 或多用户能力 |

## 背景

ADR 0062 已把 v1 stable 的生产 server 收敛为 fixed `marshal control-plane serve`，并要求 loopback AF_UNIX endpoint 位于 owner-only held control directory；ADR 0066 又把唯一生产状态根冻结为 canonical repository 的 `.marshal/runtime-v1/`。当前 CLI application assembly 已能让一次 `RepositorySession` 持有 repository-wide owner 与 `PublicApplicationPort`，但 fixed server transport 尚未实现。

Darwin 没有本切片可依赖的 `bindat` 等价接口。AF_UNIX `bind(2)` 消费 pathname；若为获得相对路径而让长寿命 Core 调用 `Fchdir`，进程级 cwd 会同时影响其它 goroutine、库调用、日志与未来 request，形成跨请求竞态和隐式第二路径上下文。若改绑 `/tmp`、用户 cache 或 symlink 短路径，又会把 endpoint 移出 canonical repository authority root。

因此需要精确澄清 ADR 0062 §3.1 中“descriptor-relative AF_UNIX”的 Darwin 投影：pathname 可以用于内核定位，但不能提供任何授权；授权必须继续来自 held canonical descriptors、current-name/object recheck、current owner、peer/process 与 fixed binary 的联合证明。

## 决定

### 1. 适用范围与取代关系

1. 本 ADR 只适用于 Darwin arm64、单节点、单用户、可信 canonical repository 中 fixed `marshal control-plane serve` 的本地 AF_UNIX endpoint。它不授权远程或非 loopback transport、TCP、独立 `marshal-server`、多用户、跨主机、Linux 或 hardened isolation。
2. 本 ADR 仅澄清 ADR 0062 §3.1 在 Darwin 无 `bindat` 时的 locator 语义：允许 server 和 client 把按本文派生的 **absolute canonical pathname** 传给 `bind(2)`/`connect(2)`；“pathname 字符串不是 authority”、owner-only root、peer/current owner/fixed binary recheck 与 durable delivery ledger合同全部保留。
3. 本 ADR 不改变 Run、Attempt、ResultIngress、ReviewDecision、owner fact、application intent/receipt 或发布权限，不增加第二 authority store。endpoint 与连接只能承载注入的 `PublicApplicationPort`；它们不能签发或替代业务 authority。

### 2. 唯一 held authority root

server 在创建 socket 前必须已经通过 ADR 0066 的唯一 production factory取得同一 `RepositorySession`，并在该 Session 生命周期内持续持有、逐级验证以下对象：

```text
canonical RepositoryRoot
  → .marshal
  → runtime-v1
  → control
```

每一级都必须由前一级 held directory descriptor 以 nofollow、owner-only 语义打开或创建，并冻结 `canonicalPath/device/inode/fileType/uid/gid/mode` 与 current-name identity。canonical repository pathname还必须按 ADR 0064 的 `O_NOFOLLOW_ANY` 或等价 held-parent chain重开，证明 current pathname仍指向同一 held对象。

以下对象或字符串都不构成 authority：absolute socket pathname、cwd、argv、环境变量、PID 数字、socket 文件名、HTTP Host、调用者提供的 repository path、`$PATH` 命中、symlink alias、`/tmp` 或用户 cache。实现不得接受调用者传入 endpoint/root override，也不得在 canonical `.marshal/runtime-v1/control` 之外创建 production socket、nonce或 transport ledger。

### 3. owner-epoch scoped 的短 pathname locator

1. endpoint leaf 由 current `ControlOwnerAcquisition.OwnerEpoch` 确定性派生为 `s-<epoch-base36>`；base36 必须是无前导零的小写 canonical 编码。客户端只能从 current durable owner fact重建该 leaf，server/CLI/API 参数不得直接提供 leaf或完整 locator。
2. locator 固定为 held `control` directory 的 current canonical pathname与该 leaf的拼接。这里的 canonical pathname只能来自入口处已经由 nofollow held-parent chain接受并冻结的 absolute lexical pathname，再以 current-name recheck证明仍指向同一 held对象；调用者提供的 symlink alias必须直接拒绝，禁止先用`EvalSymlinks`或等价 realpath解析把 alias“洗白”后再接受。locator只帮助 Darwin 内核找到 socket，不授权 endpoint，也不进入业务 ledger、Evidence、Outcome、普通 event或日志。
3. UTF-8 pathname bytes连同终止 NUL必须在任何 socket创建、transport intent、listener启动或 application mutation之前满足 Darwin `sockaddr_un.sun_path` 上限。过长、非 canonical、包含 NUL、不能从 held `control` identity唯一派生或重开不一致时，返回 typed `control-plane-locator-unavailable`；禁止回退到 cwd-relative path、`Fchdir`、symlink、`/tmp`、TCP或另一 root。
4. 每个严格递增 owner epoch使用不同 leaf。旧 server crash遗留的旧 epoch socket因而不阻塞 successor bind，也绝不能被 current客户端 adopt。current/future epoch leaf若预先存在，无论类型、owner或内容是否看似合法，都在 bind前 fail closed；不得 unlink后猜测重试。
5. 旧 epoch leaf只是不可采用的 stale transport object，不是 owner或 delivery authority。其垃圾回收不属于首个 server纵切：本切片不得为清理旧 leaf而删除、重命名或连接任何非 current endpoint；后继 GC若需要自动删除，必须另行冻结 exact stale证明与 ABA-safe处置。

owner epoch进入 locator只提供 creation-once名称空间，不提供授权。server与client仍必须逐字节绑定完整 current owner acquisition/fact digest；知道或猜到 leaf不能获得连接或 mutation authority。

这里的“完整 current owner acquisition digest”精确定义为：对 current durable owner ledger 中 `ControlOwnerState.Acquisition` 的 closed `ControlOwnerAcquisition` 值进行 JCS canonicalization 后计算的 `SHA-256`，由 owner authority 的唯一共享实现产生。其 preimage 必须包含 scope、owner epoch、UID/GID、完整 process identity、完整 fixed binary identity、observer identity 与 observed time；transport、CLI 和 delivery store 不得各自维护另一套字段选择或 JSON 编码。该 digest 与 current owner fact digest必须同时绑定，任一缺失或漂移都 fail closed。

socket object identity digest是本 owner epoch 的 host-local endpoint observation，只证明 current pathname仍指向 setup时的 exact socket object；它不是 durable owner acquisition digest，不能代替 owner fact、不能跨restart迁移、不能单独授权 replay。handshake必须同时绑定 durable owner acquisition digest与socket object identity digest；delivery `pending`必须持久化前者，后者只作为当前endpoint认证证据，不进入业务authority。

### 4. bind 前后与运行期门禁

server必须按以下顺序创建 endpoint，不得调换：

```text
持有 current RepositorySession/owner lock
  → 重验 repository/.marshal/runtime-v1/control held chain与current names
  → descriptor-relative验证 current epoch leaf不存在
  → 从 held control observation派生absolute canonical locator并检查sun_path上限
  → bind absolute locator
  → 把socket current name收敛为owner-only AF_UNIX identity
  → fsync held control parent
  → 重验listener getsockname + socket current-name identity + 完整held chain
  → O_EXCL创建并重验本epoch owner-only transport token
  → 才开始accept
```

socket current-name identity至少包含 `device/inode/fileType/uid/gid/mode/linkCount`；必须是当前用户拥有、`0600`、`LinkCount=1` 的 AF_UNIX socket。listener自身、`getsockname` 与 current name必须对应同一派生 locator。bind前/后任一级 parent rename、symlink替换、device/inode/type/owner/mode漂移，leaf提前出现、类型错误、current-name消失或替换，以及监听对象与名称不一致，均在对外 ready 前 fail closed。

每次 accept、authenticated handshake、mutation dispatch 前后以及 reconnect/recovery时，server都必须重新验证：

- held repository、`.marshal`、`runtime-v1`、`control` descriptor仍是原对象，current names仍逐级指向这些对象；
- current epoch socket name仍指向 setup时冻结的 exact identity，locator重算结果不变；
- server持续持有current epoch token descriptor，token current name仍指向setup时冻结的regular identity且content digest不变；
- repository OS owner lock与完整 `ControlOwnerAcquisition`/owner fact仍 current；
- server进程的 PID/birth、UID/GID，以及 fixed `marshal` 的 canonical path、device/inode/size/SHA-256/CDHash/sourceHead/profile仍与 owner acquisition和安装/activation policy相符；
- 已连接 client的 peer credential、PID/birth与fixed `marshal` identity满足本节客户端合同。

任一 recheck 的 I/O不确定、name缺失、rename、symlink、socket swap、PID reuse、binary/object漂移或 owner successor都返回封闭 typed失败；不得把错误归为可自动切换 endpoint，也不得执行 application mutation。pre-check与post-check之间的 rename/symlink/ABA若只在post-check被发现，已连接transport立即失效且其 reply不能成为业务成功；业务结果只能由 current RB1 exact receipt恢复。

### 5. client定位、认证与调用边界

1. fixed `marshal` client先从同一 canonical repository逐级持有并验证上述 descriptor chain，再从 current durable owner fact派生唯一 locator。client不能扫描socket、使用“最新mtime”、尝试多个epoch、采用旧endpoint，或从环境/配置读取替代地址。
2. connect前后都重验held chain与socket current-name identity；连接建立后以peer credential和进程birth证明对端是owner fact绑定的server进程，并验证对端fixed binary的canonical path/object/SHA-256/CDHash/sourceHead/profile。pathname命中但peer、owner或binary不符固定拒绝。
3. handshake必须绑定protocol revision、repository identity digest、authority namespace/root、完整current owner acquisition/epoch/fact digest、server与client process identity、fixed binary identity、endpoint identity、单次challenge nonce、request/idempotency key、application intent digest和deadline。路径字符串不能替代其中任何字段。
4. server取得owner并完成socket identity recheck后，必须在held `control`下以确定性leaf `t-<epoch-base36>`、`O_EXCL`/nofollow、owner-only `0600` regular file、`LinkCount=1`创建本epoch 32-byte random transport token，读回验证exact content digest并同步对象与parent后才可accept；预先存在、替换或identity漂移一律拒绝，不adopt、不覆盖。每条连接由server产生不可预测、单次使用且有界存活的challenge nonce，client以该token对完整handshake binding生成`HMAC-SHA-256` proof。raw token只能由同一held chain descriptor-relative读取，raw token/nonce/proof不得进入RB1、delivery projection、event、error、doctor或日志。token与nonce只认证transport，不是authority，不能替代owner、peer、process、fixed binary或RB1 recheck。
5. nonce在首次验证尝试时即消费；wrong、missing、expired或replayed nonce/token/proof固定关闭连接并返回secret-safe typed失败，且必须发生在delivery pending与`PublicApplicationPort`调用之前，保证零Port mutation。owner successor/restart必须创建新token；旧epoch token、nonce或proof不得跨epoch重放。
6. 每个连接是当前 owner epoch下的有界borrow，不是可复制 bearer capability。server只把认证后的request交给同一个in-process `PublicApplicationPort`；reply只有引用exact current RB1 application receipt/fact digest及post revision/head时才能报告业务成功。连接断开或response loss按第6节的delivery projection重放同一intent，不得创建第二Attempt、第二Supervisor command或第二authority root。
7. server持有owner时，CLI mutation只能调用该authenticated endpoint；连接不存在、locator过长或任一认证失败时返回typed unavailable/conflict，不能回退为第二个in-process writer。只读projection仍按既有合同处理，不能借只读连接签发mutation authority。

### 6. 首个纵切必须包含最小 immutable delivery projection

AF_UNIX adapter与以下最小transport delivery projection是同一个可验收纵切，不得只实现socket/handler而把response-loss窗口留给后续切片：

1. projection只能位于同一held `control` authority root下的owner-only、nofollow子目录。每个logical request只允许追加两类immutable JCS record：`pending`必须分别绑定`schema/protocol revision`、按第3节定义的exact durable `ownerAcquisitionDigest`、owner fact digest、repository/authority root digest、request/idempotency key digest、request digest、application intent digest与冻结deadline；只存owner epoch/fact而遗漏`ownerAcquisitionDigest`不构成可恢复证据。`receipt-ref`绑定exact pending digest、exact RB1 application receipt/fact digest及post revision/head。不得保存raw acquisition preimage、raw request、token、nonce、proof、secret或业务response body。
2. record以digest-derived closed leaf和`O_EXCL`/nofollow创建，写完整内容、同步对象与held parent后才可推进；已存在的same-key/same-digest record只允许exact replay，same scope/key不同request或intent digest固定conflict。禁止truncate、overwrite、rename-over、last-writer-wins或删除未知对象后重试。replay所得状态只由完整immutable chain归约为`pending|receipt-ref`。
3. 首次请求必须先完成transport认证、bounded parse与queue admission，再durable append `pending`，最后才把同一个idempotent application intent交给`PublicApplicationPort`。Port返回后仍须从current RB1重读并验证exact receipt/fact，durable append `receipt-ref`后才可发送业务success；进程内返回值、HTTP status或projection自身不能代替RB1 receipt。
4. response loss或restart看到`receipt-ref`时，只在current owner/RB1/intent全部重验后重建相同response。只看到digest-only `pending`时，server先按application intent digest查询RB1：命中exact receipt才可补同一`receipt-ref`；未命中时绝不能从digest、日志、projection、Run状态或默认值自主重建request bytes，也不能自行调用Port。只有重新通过current owner/peer/binary/token/nonce认证的fixed client，可在**原冻结deadline内**重交canonical request bytes；server必须逐字段重算request/application intent digest，证明scope/key、canonical bytes、request digest与intent digest全部精确命中old pending，才可由current owner把同一idempotent intent重交Port。任一字段不符、原deadline已到或client未重交时保持`pending/unknown`且零Port mutation。
5. old owner创建的pending只能由持有current physical owner lock的strict successor处理。successor必须从durable owner ledger证明old pending所绑owner fact到current acquisition之间存在同一repository/authority namespace、严格递增且无分叉的successor lineage，并重新执行current repository/root/RB1与request全部门禁；不能把old owner token/nonce或pending digest当作handoff capability。same scope/key但canonical bytes、request digest或intent digest不同，在任意successor epoch都固定conflict，不能新建parallel pending或以新owner覆盖旧record。
6. projection可在明确quiescent且全部pending均已由RB1 exact receipt关闭时删除并从RB1重建；unresolved pending、record缺失/截断/乱序、digest不符或I/O不确定时必须fail closed，不能以“projection可重建”为由猜测未执行。projection始终只是transport恢复索引，不能授权mutation、裁决Run或单独回答成功。

### 7. 有界transport、背压与取消

v1 Darwin single-user profile冻结以下硬上限；签名安装policy或request只能进一步收紧，不能放大，环境变量与未验证配置不能修改：

| 边界 | 硬上限 |
| --- | --- |
| handshake frame / handshake deadline | `16 KiB` / `5s` |
| HTTP header count / 单header / 总header / read-header deadline | `32` / `8 KiB` / `32 KiB` / `5s` |
| request body / read-body deadline | `1 MiB` / `15s` |
| response / write deadline | `1 MiB` / `15s` |
| idle / 每连接request数 | `30s` / `16` |
| 每repository连接 / 每连接inflight / 每repository inflight / queue | `64` / `1` / `32` / `32` |
| application deadline | request可缩短；默认`5m`，最长`10m`，restart不得延长 |

1. server分别设置handshake、read-header、read-body、idle、write与application deadline；一个总deadline不能替代阶段deadline。`ReadHeaderTimeout=5s`只覆盖header；header完成后，handler在开始读取body时单独启动`15s` read-body deadline，并在bounded body读取完成后清除该transport read deadline。不得以从accept/header开始计算的`ReadTimeout=15s`同时充当read-body窗口，因为header消耗会侵占body预算。application phase在body完成、认证与queue admission后才使用独立的冻结deadline，最长`10m`；response write deadline只在application返回、首个response byte即将写入时启动`15s`，不能从request进入时提前起算。每个在线阶段以进程monotonic elapsed计时；application deadline按既有application合同冻结为可持久重放的canonical UTC instant并写入pending/intent，restart只能恢复原值、不能重新起算。keep-alive每次只在上一request完全结束且owner/endpoint仍current时重新进入idle阶段。
2. 协议必须限制request frame、header数量、单header bytes、总header bytes、body bytes、response bytes、每连接request数、每repository连接数、每连接与每repository inflight数及application queue深度。读取严格有界，不得先无界buffer再检查；response超限不得截成success。
3. malformed、oversize、unsupported framing、header/body超时、slowloris与half-open connection在`pending`和Port之前关闭并产生secret-safe typed错误。连接/inflight/queue饱和返回typed `control-plane-overloaded`或直接在尚未认证时关闭，零delivery append、零Port mutation；server不得通过无界goroutine或队列吸收压力。
4. queue admission后创建的request context携带冻结application deadline并原样传入`PublicApplicationPort`；client断开、read/write timeout、server shutdown或owner失效必须取消该context。取消不证明mutation未发生：只要`pending`已durable，transport就不得把timeout/EOF解释为not-applied，而必须按第6节从RB1 reconcile。
5. client不读取response时，write deadline关闭connection；若exact `receipt-ref`已durable，后续exact replay返回同一结果，否则保留pending继续reconcile。任何失败response也受response-size/write deadline约束，不能泄漏path、token、request body或内部错误链。
6. graceful shutdown固定使用accept-stop、request-drain与application-cancel三个有界deadline。application在cancel后仍不返回时，server必须停止全部新borrow、保留durable pending并进入whole-process fail-stop；不得释放owner后让stuck Port goroutine在同一进程继续运行，也不得伪造clean shutdown/success。successor只可在旧进程退出、OS释放held lock后取得strict successor epoch，并按第6节reconcile。

### 8. cwd、并发与生命周期

1. fixed server production路径不得为bind/connect调用`chdir`/`Fchdir`，也不得通过锁住goroutine来把进程级cwd伪装为request-local状态。server启动前、运行中、shutdown后cwd必须保持不变；所有repository、state与endpoint访问均以held descriptor或本文唯一absolute locator完成。
2. 一个 `RepositorySession` 对应一个current owner epoch、一个listener和一个in-process application实例。accept loop可以并发读取已认证请求，但所有mutation仍由既有owner/application authority串行化或CAS；transport并发不能产生第二writer。
3. graceful shutdown顺序固定为：停止新accept → 阻止新application borrow → 按第7节有界drain/cancel，并按current receipt reconcile在途请求 → 关闭listener → 在owner lock仍持有时分别重验current socket与token identity，descriptor-relative unlink exact current socket leaf与token leaf、fsync held `control`、证明两个name absent → 关闭application/RepositorySession → 最后释放owner lock。unlink前任一identity不符时不得删除未知对象；保留typed intervention并停止提供服务。stuck application必须遵守第7节whole-process fail-stop，不能先释放owner再继续执行。
4. crash不会把socket或token授权给successor。下一fixed server必须先取得严格successor owner epoch并使用新的epoch socket/token leaf；它不adopt、不unlink、不复用旧leaf。连接到旧server、读取旧token或持有旧连接的client因owner fact不current而在mutation前失败。
5. server/client不能把raw pathname、token、nonce、proof、HTTP body、argv/env、transcript或secret写入普通日志、event、doctor或error。可观察性只输出secret-safe reason、owner epoch、endpoint identity digest、peer/binary identity digest与RB1 receipt引用。

### 9. T1/T2 实施与成熟度边界

本ADR按以下依赖顺序实施，禁止把合同、delivery、transport与真实集成重新压成一个大分支：

1. `S0 contract`：接受本文并冻结字段、阶段deadline和成熟度；只改文档。
2. `S1 durable delivery`：唯一owner authority产生`ownerAcquisitionDigest`，immutable `pending|receipt-ref`持久化并重验exact acquisition/intent/RB1 receipt；不实现socket或CLI。
3. `S2 endpoint auth`：实现held locator、AF_UNIX、peer/fixed binary、token/nonce/HMAC与socket ABA门禁；只以fake application证明`COMPONENT`。
4. `S3 bounded HTTP delivery`：实现独立read-body/write/application phase deadline及`pending → current RB1 reconcile → exact receipt`路由；仍不宣称production integration。
5. `S4 resident integration`：fixed `marshal control-plane serve`显式使用resident recovery composition，在ready前完成全repository recovery，并由同一exact candidate bytes的client和真实Pi完成RUNNING/restart replay canary。

前一切片未合入并通过其负向门禁时不得开始后一切片；不得以source string count、fake `PublicApplicationPort`、synthetic owner或compile-only测试替代`S4`动态证据。本ADR仍分T1/T2两个功能阶段：上述S0–S4共同关闭T1；T2才关闭ADR 0062要求的真实终态纵切。

#### T1：fixed transport与RUNNING可达

T1最终必须完整交付第2–8节的locator/auth/bounded transport、context取消及immutable/no-clobber delivery projection，并只路由当前`PublicApplicationPort`已有的`Status`、`StartRun`、`InspectRun`三个typed method；这些能力按S1–S4分别合入和验收，不能以任一中间切片替代T1整体出口。T2 operation在T1必须以unsupported且零pending/零Port fail closed。T1真实canary固定使用同一fixed `marshal`与真实Pi：经server `StartRun`取得exact `RUNNING` projection，再由重新认证client以`InspectRun`读到同一Run/Attempt/head，并覆盖server restart与exact pending/receipt-ref replay；不得通过CLI本地fallback补做后续阶段。

S1–S3完成时`fixed transport/T1 capability`只能标记`COMPONENT`。只有S4在exact-head macOS gate中使用固定candidate `marshal`、resident production composition与真实配置Pi，证明recovery-before-ready、真实AF_UNIX调用、同一Run到`RUNNING`、response-loss/restart后strict successor只补同一exact receipt，且没有CLI fallback、第二Run、第二Attempt或第二Supervisor command，`fixed transport/T1 capability`才可标记`INTEGRATED`。该结论不升级`ADR 0062 full fixed-server lifecycle capability`：Run到`RUNNING`仍不证明result collect、terminalization、verification、independent review、Decision或`ACCEPTED`可经server到达，也不满足ADR 0062完整T2 canary。

#### T2：PublicApplicationPort终态纵切与ACCEPTED

T2在T1通过后扩展`PublicApplicationPort`及其typed request/response schema，为以下每个阶段提供exact current-ledger operation：

- collect与attempt terminalization：接纳exact Run/Attempt/generation/head，执行`CollectRunResult`及既有terminalization/reconcile，返回绑定terminal facts的projection；
- verification：只对current collected evidence执行`VerifyRun`，返回exact VerificationReport/evidence digest；
- review：只从current verification/artifact facts构造`BuildReviewPacket`结果，不让Worker提供权威review；
- decision与终态：`ApplyReviewDecision`只接纳外部独立、fresh且digest精确绑定的Decision，返回current terminal Outcome；`InspectRun`必须可证明同一Run最终为`ACCEPTED`。

这些名称冻结transport/application operation语义；具体Go request/projection type必须在T2以versioned closed schema加入同一个Port，server handler不得直接调用CLI command、runstore、verifier、review importer或productionruntime concrete method绕开Port。每个mutation继续使用第6节同一delivery状态机与RB1 exact receipt；不得用一条“advance all”handler隐藏多个无独立receipt的阶段。

T2真实canary固定使用真实Pi与外部独立Decision，经fixed server完成collect/terminalization/verify/review/decision并到`ACCEPTED`，且覆盖server restart/response-loss后exact replay。只有该canary与全部负向门禁通过，才能把`ADR 0062 full fixed-server lifecycle capability`标记为`INTEGRATED`并满足ADR 0062；这不自动升级其它I186组件或授权stable发布。

本ADR不授权T1/T2顺带实现remote/TCP/TLS、multi-user AuthN/AuthZ、独立`marshal-server`、Provider transport、Dashboard、Linux、launchd安装、签名/notarization、stable发布、其它AgentProvider或旧epoch socket/token GC。第6节最小delivery projection是T1的必需部分，但它只能引用RB1 receipt，不能吸收socket/owner业务authority；更通用的remote delivery或跨节点ledger仍不在本ADR范围内。

验收至少证明：

1. 正常路径在canonical held chain下以absolute locator bind/connect，server与client cwd在并发请求前后逐字节不变，生产import/call graph中不存在server `chdir`/`Fchdir`、`EvalSymlinks`洗白、`/tmp`、TCP或endpoint override；
2. parent/socket/token leaf rename、symlink、current-name ABA、mode/owner/type/inode/link-count漂移、precreated current/future leaf、listener/name不一致与sun_path超限均在application mutation前fail closed；
3. peer PID/birth、credential、fixed binary path/object/SHA-256/CDHash/sourceHead/profile及owner epoch/fact任一漂移均拒绝，且零RB1/application副作用；
4. server crash后strict successor epoch使用新leaf恢复；旧endpoint、旧连接与旧owner request全部拒绝，不adopt或删除旧leaf；
5. wrong/missing/expired/replayed nonce、token或proof均在pending/Port前拒绝；raw认证材料不落RB1/projection/日志，transport认证不能替代owner/peer/binary/RB1；
6. pending创建、Port handoff、RB1 receipt、receipt-ref与response各边界的crash/response-loss只reconcile同一intent；digest-only pending在RB1未命中时不自主重建请求，只有重新认证client在原deadline内提交逐字段/摘要完全一致的canonical bytes才可由strict successor重投；same-key different-digest、owner lineage断裂、projection伪造/截断/乱序、receipt-ref错指均fail closed；
7. slowloris、half-open、malformed/oversize header/body/frame、queue/connection/inflight饱和、client不读response及每个阶段deadline都在固定上限内收敛，零重复Port mutation、零无界goroutine/buffer；
8. context取消可达Port；stuck application触发有界shutdown后的whole-process fail-stop，不能先释放owner或伪造success，strict successor只从pending/RB1恢复；
9. concurrent server启动只有owner winner可bind；current server持有owner时CLI不得装配第二writer；server restart/response loss只reconcile同一application intent与exact receipt；
10. shutdown只在exact current-name验证后删除current socket与token leaf；swap或unknown object不被删除，owner释放前parent已同步；
11. T1 exact-head Darwin测试、相关race、`go vet`、staticcheck、architecture/diff/secret/mergeability通过；S1–S3期间`fixed transport/T1 capability`保持`COMPONENT`，只有S4真实Pi canary证明同一`PublicApplicationPort`/authority chain到`RUNNING`、recovery-before-ready与restart exact replay后，`fixed transport/T1 capability`才标记`INTEGRATED`，且不得据此升级`ADR 0062 full fixed-server lifecycle capability`；
12. T2逐项证明collect/terminalization/verify/review/independent Decision的Port与delivery routing，真实Pi经fixed server到`ACCEPTED`且无CLI fallback；只有T2全绿才把`ADR 0062 full fixed-server lifecycle capability`标记为`INTEGRATED`并宣称满足ADR 0062。

## 后果

正面结果是Darwin不需要不存在的`bindat`，也不需要用进程级cwd或外部短路径交换安全性；pathname只承担内核locator职责，全部授权仍由canonical held objects、current owner、peer与fixed binary闭合。owner-epoch leaf让server crash后的successor无需删除或adopt未知旧socket即可启动。首个纵切同时关闭transport response-loss窗口，并用阶段deadline、资源上限、背压和Port取消避免fixed server被慢连接或stuck application无限占用。

代价是过长canonical repository路径在Darwin上会明确不可用，并且crash遗留的旧epoch socket/token在本切片不会自动GC。两者都优于静默回退到第二root或不安全unlink：前者由doctor给出typed迁移提示，后者保留为非authority stale对象并在未来以单独GC合同处理。

本ADR即使被接受也只授权实现该Darwin transport边界，不表示`marshal control-plane serve`已实现、真实canary已通过、I186-R2–R6成熟度升级，亦不解除managed signing/notarization、Linux stable、完整fault matrix或受保护`v1.0.0`候选门禁。

## 拒绝的替代方案

### 长寿命Core `Fchdir`到control directory后用相对socket名

拒绝。cwd是进程全局状态，不是goroutine或request局部状态；锁只能约束自有调用，不能约束运行时、库与未来代码。它会把并发server的路径解释变成隐式共享可变状态。

### 在`/tmp`、用户cache或launchd目录绑定短socket，再把路径写回repository

拒绝。该做法创建第二root；路径/记录会先于current repository owner与held object取得实际控制力，并扩大symlink、cleanup、跨repository碰撞与权限边界。

### 固定复用一个`control-plane.sock`并在启动时无条件unlink

拒绝。crash后无法仅凭pathname区分旧合法socket、仍活peer与被替换对象；`stat → unlink → bind`存在name ABA窗口。owner-epoch leaf让successor无需删除未知对象。

### 把absolute pathname或socket inode写成新的authority fact

拒绝。pathname只定位，inode只是当前host observation。业务authority仍属于现有owner/RB1 ledger；新增endpoint authority会制造第二真值与跨重启迁移问题。

### locator失败时回退CLI本地writer或TCP

拒绝。这会在server仍持有owner时创建第二composition/writer，或未经TLS/AuthN/AuthZ ADR启用新的transport。失败只能typed fail closed。
