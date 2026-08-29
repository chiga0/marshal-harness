# ADR 0069：Attempt reservation 与 existing-worktree allocation

- 状态：已接受（Accepted）
- 日期：2026-08-29
- 提议基线：`main@e1e81f8f4fe9438b54444ade8fca039964205d89`
- 返工记录：`ebbfd86d60fad748e180e900e835bd3361392cdd` 的独立审查为 `P0=3`、`P1=4`；`1adf20c6b11a3047067a0bf574a89278c0bda700` 的 aggregate 复审只剩 `P0=1`、`P1=1`，本版仅关闭这两个已知窗口，不扩展范围
- 接受依据：定向修订 sourceHead `e2af17931e123fca1c10d565cceae6dc03ef4d5a` 经独立 reviewer `APPROVE`，`P0=0`、`P1=0`，且五份变更文档的 `git diff --check` 与相对链接检查通过
- 影响范围：ResultIngress/RB1、Run start、Attempt budget、Local allocation、S1′/S2′ producer chain
- 关联：[ADR 0029](0029-pre-attempt-abort.md)、[ADR 0051](0051-darwin-local-dogfood-profile.md)、[ADR 0057](0057-durable-local-allocation-recovery-and-production-composition.md)、[ADR 0063](0063-prepared-execution-authority-and-production-chain.md)、[ADR 0065](0065-sealed-run-start-proof-and-one-way-composition.md)、[ADR 0066](0066-production-composition-owner-acquisition.md)、[ADR 0067](0067-darwin-ordinary-user-launch-and-attach-recovery.md)、[ADR 0068](0068-mac-first-cli-only-lifecycle-preview-rc1.md)、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)

## 1. 背景与边界

S1′/S2′ 的真实 producer 预审发现两个 P0：

1. `READY` Run 没有 `CurrentAttemptID`。allocation、Supervisor bootstrap 或 child side effect 之前必须有一个可重放的候选 Attempt，但不能用 Run `READY → READY` self-loop提前建立完整 Attempt authority或消费预算，否则会出现第二份 Attempt authority和第二个counter。
2. `darwin-local-dogfood` workspace-write使用已经存在的Git worktree。现有“创建新空目录”的Local receipt不能诚实描述该对象，也不能授权Marshal替换、清理或删除用户worktree。

另有一个已由[ADR 0066](0066-production-composition-owner-acquisition.md)决定的实施P0：ResultIngress不得按pathname重新打开authority对象，`ObserveCurrentCore`也不能先于`OpenOwner`产生current结论。S1′必须改为held descriptor backend并把观察放在owner打开之后；这不是本ADR新增的信任决策。

本ADR只收敛Mac ordinary-user、trusted single-user、workspace-write、`publication:none`的CLI-only RC1路径；不提供hardened sandbox、恶意仓库隔离、Linux authority或stable发布授权。

## 2. 决策一：reservation 不是完整 Attempt authority

### 2.1 `attempt-reserved` closed union

ResultIngress/RB1把physical ledger升级为`protocolRevision="attempt-authority/v2"`，新增以下稳定FactType；revision不拼进FactType：

- `attempt-reserved`：`schemaRevision="attempt-reservation/v1"`，表示`active`；仅预留候选`AttemptID`，不建立完整`AttemptIdentity`、不签发dispatch lease、不改变Run、不消费预算；
- `attempt-reservation-consumed`：引用exact reservation与sealed Run successor，表示`consumed`；
- `attempt-reservation-cancelled`：引用exact reservation与零副作用证明，表示`cancelled`。

三种FactType构成append-only closed union `active → consumed|cancelled`；同一reservation最多一个resolution，不更新旧fact。

所有历史事实append-only保留，不做物理GC。Run已经离开exact READY head、进入terminal state或产生新head后，旧reservation只能用于历史explain，逻辑上不得再current。

### 2.2 creation-once 与 replay key

Prepare的唯一replay key固定为：

```text
(RunID, exact READY RunSequence, exact READY RunAuthorityHead)
```

在同一held repository owner、held Run Lease与RB1 transaction内，producer必须先lookup该key：

1. 找到canonical bytes相同的reservation：返回同一fact、同一reserved `AttemptID`与current resolution；`active`可继续，`consumed`只返回already-consumed inspect结果，`cancelled`只返回typed cancelled，后二者均不得继续或mint sibling；
2. 找到不同bytes、不同`AttemptID`、双resolution或冲突记录：fail closed；
3. 只有确定性`not-found`才允许mint新`AttemptID`并append+fsync一条`active` reservation。

两个并发Prepare对同一key只能得到一份reservation；response loss只能exact replay，禁止sibling。reservation至少绑定`TaskID`、`RunID`、reserved `AttemptID`、`AttemptOrdinal`、exact READY sequence/head、frozen input digests、`BaseSHA`、`WorktreePath`和protocol revision。digest由完整canonical payload重算，不接受caller的digest-only echo。

### 2.3 budget 的唯一权威与三次重验

budget唯一来自同一held Run authority的`AttemptsUsed`与`MaxAttempts`；`AttemptOrdinal = AttemptsUsed + 1`。caller、RB1、projection与sidecar均不得维护或自增第二个counter。

同一exact READY sequence/head、`AttemptsUsed`、`MaxAttempts`与expected ordinal必须在三个位置重验：

1. reservation lookup-before-mint/append之前；
2. 生成dispatch lease、完整`AttemptIdentity`并append `attempt-opened`之前；
3. sealed `READY → RUNNING` successor提交之前。

前两次只确认预算可用，不消费。只有第三次的Run successor原子写入`CurrentAttemptID`并把`AttemptsUsed`增加一次；该Run fact是消费的linearization point。其后RB1以引用exact Run successor的`consumed` resolution收敛reservation。若在两次fsync之间崩溃，Run的新head已使`active` reservation逻辑不可current，恢复只能幂等append同一`consumed` resolution，不能复用reservation或二次计数；不要求Run/RB1跨ledger原子提交。任何head/budget/ordinal漂移都禁止继续副作用并进入typed cleanup/intervention。

dispatch claim 自身也必须关闭独立 ledger 的 durable-write/response-loss 窗口。在同一 held repository owner 与 held Run Lease 链内，producer 以 `(reservation fact digest, RunID, reserved AttemptID)` 先查 durable lease binding：已有 same canonical claim 时返回同一 `DispatchLease` 与同一 full `AttemptIdentity`，任一 registration/capability/generation/fencing/input bytes 冲突均 fail closed，只有确定性 `not-found` 才允许 claim 并 fsync。claim 已耐久但响应丢失或在 RB1 `attempt-opened` fsync 前崩溃时，恢复必须 lookup-before-claim 并 same-bytes replay 原 lease，然后继续幂等 append 同一 `attempt-opened`；不得 mint sibling lease、把 reservation cancel 掉或永久卡在“已有 lease 但无 opened fact”的中间态。

### 2.4 cancellation 与 ADR 0029

`active → cancelled`只允许same repository owner在held Run Lease与RB1 transaction内，对exact READY sequence/head证明以下全部为零：

- 没有`attempt-opened`或完整`AttemptIdentity`/dispatch lease；
- 没有allocation bind intent/receipt/effect；
- 没有Supervisor bootstrap、child或command；
- 没有publication或其它外部side effect。

cancel fact fsync后，才允许[ADR 0029](0029-pre-attempt-abort.md)的READY pre-attempt abort。只要任一下游fact存在，就永久禁止cancel/READY abort；系统只能在同一reservation上完成所需cleanup并进入typed intervention。关闭FD、删除projection或响应未知都不构成cancel authority。

### 2.5 完整 Attempt authority

reservation成功后，producer必须在同一current chain中生成完整`AttemptIdentity`与dispatch lease，再append FactType=`attempt-opened`、`protocolRevision="attempt-authority/v2"`、`schemaRevision="attempt-opened/v2"`。该fact精确引用reservation fact/digest、dispatch lease、full Attempt identity、exact READY head与frozen inputs。reservation本身不能授权allocation、launch、child、ResultIngress admission或Run successor。

legacy无reservation的`attempt-opened`/Run-start记录继续按原revision逐字节历史replay，但不得为fresh S1′ mint proof、启动child或进入RC1 canary。

## 3. 决策二：RB1/ResultIngress 是唯一 allocation authority

### 3.1 `bind-existing-worktree` profile

新增`bind-existing-worktree` allocation profile，protocol revision为`existing-worktree-binding/v1`。目标是Git已经登记的existing worktree，是受绑定live object而不是provider-created resource：

- Marshal不创建、替换、移动、reset、clean、prune或删除目标worktree；
- 不得伪造“新建空目录”receipt；
- bind前通过canonical locator逐段nofollow打开并观察target current-name、directory object identity、Git common-dir/worktree admin object、HEAD/base、clean state与frozen Run inputs；
- 任一路径段symlink、wrong BaseSHA、dirty/unsupported state、owner/mode/type/link/identity漂移均fail closed；
- ordinary-user只保证确定性观察与漂移拒绝，不声称阻止同UID外部进程修改worktree。

同一identity lineage不表示跨阶段永久持有同一FD。Supervisor mutation-adjacent source gate仍按ADR0067从canonical locator逐段nofollow reopen，并与RB1 receipt中的exact identity/current-name/material observations逐字段匹配；不得把旧FD存在性或pathname相等当成current证明。

### 3.2 RB1 closed-union facts

Existing-worktree的Bind/Receipt/Release全部是RB1 authority facts，形成closed union：

```text
bind-intent → bind-receipt → release-intent → release-receipt
```

每个fact绑定exact owner、Run、reserved/full Attempt、reservation、allocation/generation/fencing、frozen inputs、target current-name/object/Git identity及predecessor RB1 head。repository-global target uniqueness必须在held repository owner下从RB1 replay判定：

- 一个target current-name/object identity同时最多一个active binding；
- 一个reservation/Attempt只能绑定一个target；
- 跨Run/Attempt重复绑定fail closed；
- lost response先replay RB1：same canonical request返回同一receipt，不同bytes冲突。

不得增加第二个allocation ledger，也不得要求Run journal、RB1与projection跨ledger原子提交。

### 3.3 sidecar 只是可重建 projection

唯一layout冻结为：

```text
.marshal/runtime-v1/existing-worktree-bindings/
```

它是append-only recovery projection，位于canonical repository `.marshal`、owner-private、descriptor-bound，scope只覆盖该repository。entry以path-free target identity digest命名；locator只用于找到对象，不能授权currentness。held descriptor graph固定为repository scope→`.marshal`→`runtime-v1`→`existing-worktree-bindings`，锁序固定为：

```text
repository owner → Run Lease → RB1 transaction → projection
```

projection必须能从RB1完整重建：

- 缺失或落后：先从current RB1投影，再继续；
- 损坏、超前、内容与RB1冲突：fail closed，绝不能覆盖或修写RB1；
- lost response：先replay RB1，再幂等刷新projection；
- restart：current identity只能由RB1 current receipt加重新观察得到，不能信任projection缓存。

projection不是authority、receipt、锁或第二truth source；release只append projection frame，不物理删除历史entry，删除projection也不能释放binding。

### 3.4 release/terminate

release intent 与 receipt 必须在固定锁序内绑定并在 admission 时精确重验：current owner、Run/Attempt/RB1 head、current `terminalizationID`、`cleanupBindingDigest`、`processTerminalFactDigest`、cleanup disposition 与 allocation generation/fencing。任一字段缺失、不是 current 值或与同一 terminalization/cleanup/process-terminal 链不匹配均 fail closed。只有满足这些绑定的RB1 `release-receipt` fsync才释放逻辑binding。关闭FD不是authority；任何情况下都不得删除、reset、clean、prune或修改用户worktree与Git admin entry。

## 4. 唯一 producer 顺序

fresh S1′/S2′顺序固定为：

```text
held repository owner + existing-only Run open/Lease（未知Run不得mkdir）
  → RB1 attempt-reserved active（lookup-before-mint；Run仍READY，预算未消费）
  → dispatch lease + full AttemptIdentity
  → RB1 attempt-opened（引用reservation）
  → BindOwnerToAttempt
  → RB1 existing-worktree bind-intent
  → descriptor-bound projection/effect + target re-observation
  → RB1 bind-receipt
  → launch-authorized / StoredClosure
  → PreparedExecution / PreparedRunStart
  → S1 Supervisor bootstrap/spawn/resume + sealed proof
  → sealed Run start successor（同reservation；唯一写Attempt/消费预算）
  → RB1 reservation consumed
```

在`BindOwnerToAttempt`之前禁止allocation binding；在reservation前禁止dispatch/allocation/bootstrap/child副作用。reservation后、`attempt-opened`前失败只有满足第2.4节才可cancel；任一下游fact出现后只能cleanup/intervention。S2′不得对不可信或未知Run调用会`MkdirAll`的普通`Acquire`。

## 5. 持久化与 protocol migration

不得原地重解释或重写旧记录：

1. 新FactType固定为`attempt-reserved`、`attempt-reservation-consumed`、`attempt-reservation-cancelled`；均使用`protocolRevision="attempt-authority/v2"`和reservation payload的`schemaRevision="attempt-reservation/v1"`，不得把revision拼进event/fact名称。
2. 既有FactType=`attempt-opened`保留，fresh producer使用`protocolRevision="attempt-authority/v2"`、`schemaRevision="attempt-opened/v2"`并增加reservation、dispatch lease/full Attempt identity与exact READY head binding。
3. 既有Run-start FactType保留；fresh schema revision绑定reservation、`attempt-opened`、exact READY head和budget transition。`PreparedExecution`、`PreparedRunStart`与sealed proof的新版schema同样绑定这些对象；旧revision仅历史replay。
4. allocation FactType保留稳定名称，profile/protocol revision区分`existing-worktree-binding/v1`的intent/receipt/release closed union；legacy create-empty receipt不能转换为existing-worktree receipt。
5. ResultIngress read-only projection携带reservation与exact READY head；runstore read-only projection只携带current Run head、state、budget与frozen inputs。两者不得镜像对方ledger或暴露raw records/可写callback。
6. unknown revision、字段缺失、optional-field laundering、新旧union混用、旧fact授权fresh路径均fail closed。

raw absolute `WorktreePath`只允许存在于owner-private Run frozen inputs与RB1 authority fact；这是唯一例外。public API、Worker prompt、transcript、ReviewPacket、Outcome、日志、错误与projection entry name全部path-free，只携带opaque locator或digest。locator不构成identity或authorization。

## 6. 对既有 ADR 的部分取代与保留

- **ADR 0029**：部分取代其READY abort前置；存在active reservation时，必须先按第2.4节fsync `cancelled`，才仍算pre-attempt。其零Attempt/零side-effect原则保留。
- **ADR 0057**：对Mac ordinary-user workspace-write部分取代“allocation一定创建新空目录”的解释；existing worktree使用RB1 logical binding。provider-owned新建allocation合同、terminate幂等与descriptor-relative原则保留。
- **ADR 0063**：部分取代Prepared chain对Attempt来源不明确的部分；Prepared对象必须引用full `attempt-opened` authority和其reservation。Pi/launch held-originals与exact resume合同保留。
- **ADR 0065**：部分取代proof输入与Run successor；proof绑定reservation、full Attempt、exact READY head，Run successor才写Attempt/消费预算。ResultIngress/runstore单向边界、shared guard、自有ledger replay保留。
- **ADR 0067**：部分取代S1′/S2′ producer顺序和allocation receipt类型；source gate、Attach/rebind、no-effect/permanent-intervention二分与ordinary-user边界保留。
- **ADR 0066**：不取代。held descriptor backend、两阶段owner acquisition及`OpenOwner`后才可`ObserveCurrentCore`是S1′既有实施P0。
- **ADR 0051/0068**：不取代。ordinary-user、explicit opt-in、`publication:none`、unsigned Darwin arm64 CLI-only RC1边界保持不变。

## 7. 最短实施切片

1. **S1′-A（reservation/Attempt）**：实现RB1 `attempt-reserved` closed union、lookup-before-mint、三次budget重验、full Attempt/dispatch lease与`attempt-opened`新revision；Run保持READY直到sealed successor。
2. **S1′-B（held authority/Run start）**：ResultIngress改held descriptor backend，`ObserveCurrentCore`移到`OpenOwner`后；Prepared/proof/Run successor绑定reservation与exact READY head，successor唯一写Attempt/计数。
3. **S2′-A（existing-worktree binding）**：existing-only Run open、RB1 bind/release closed union、repository-global uniqueness、固定projection layout/锁序、held re-observation和release-only terminate。
4. **S2′-B（production composition）**：严格按第4节接fixed CLI/Pi；Fake seed、legacy `execution.Run`、create-empty receipt、第二authority root与sidecar-authoritative shortcut不可达。

四项完成后才恢复ADR0067 Attach/rebind、terminalization与RC1 E2E；本ADR提议本身不升级R2–R6。

## 8. 必须通过的负面与恢复矩阵

| 类别 | 必须证明的拒绝/恢复行为 |
| --- | --- |
| reserve/dispatch并发与响应丢失 | 同一RunID+exact READY seq/head只有一份active reservation；same bytes replay，任何不同ID/bytes或sibling拒绝；dispatch claim以reservation digest+RunID+reserved AttemptID lookup-before-claim，claim已fsync但响应丢失或`attempt-opened`未fsync时返回同一lease/full identity并继续收敛，冲突claim拒绝 |
| reserve生命周期 | active只可consumed或在零下游事实时cancelled；terminal/new head后stale；历史fact不物理GC且不能恢复current |
| budget | 三次均从held Run authority重验`AttemptsUsed/MaxAttempts`与ordinal；前两次不计数，sealed successor恰好计数一次；caller/RB1/projection mutation拒绝 |
| cancellation/ADR0029 | opened Attempt、dispatch lease、allocation、bootstrap、child、command、publication任一存在时cancel/READY abort拒绝且转cleanup/intervention |
| producer顺序 | full Attempt/dispatch缺失、owner未绑定、reservation/head不一致时allocation/launch/child调用数为零；owner绑定前bind拒绝 |
| RB1唯一authority | projection缺失/落后从RB1重建；损坏/超前fail closed；projection不能覆盖RB1或释放binding；lost response先replay RB1 |
| target唯一性 | held owner下同target跨Run/Attempt、同reservation不同target、current-name/object alias均拒绝；same request只返回同一RB1 receipt |
| worktree/path | 不存在、非Git worktree、wrong common-dir/BaseSHA、dirty、symlink、rename-away/back、目录/Git admin替换、regular leaf hardlink异常均拒绝 |
| stage identity | 后续阶段按canonical locator nofollow reopen并与RB1 receipt exact-match；旧FD存在、pathname相等或locator命中均不能单独授权 |
| crash/replay | reservation、dispatch claim、attempt-opened、bind/release intent/receipt各fsync窗口均由durable lookup与相应RB1 fact唯一收敛；特别覆盖lease已fsync而`attempt-opened`未fsync；不得由pathname/projection猜测成功 |
| release | owner/Attempt/RB1 head、current terminalizationID、cleanupBindingDigest、processTerminalFactDigest、cleanup disposition、generation/fencing任一缺失、逐字段漂移或跨链拼接均拒绝；close FD不释放authority |
| target零修改 | bind/release/terminate前后worktree、HEAD、index、untracked bytes与Git admin entry保持不变；不得调用delete/reset/clean/prune |
| descriptor时序 | `OpenOwner`前调用`ObserveCurrentCore`拒绝；authority pathname被rename/替换/ABA时不reopen新对象，held current-name不一致拒绝proof |
| secret/path | 仅owner-private Run/RB1可存raw path；public/prompt/transcript/review/outcome/log/error/projection filename泄漏时拒绝 |
| legacy/migration | 旧revision只能历史replay；不能mint reservation/proof、启动child、绑定existing worktree或进入RC1 |

## 9. 未选择方案

- Run `READY → READY` self-loop预写Attempt：形成第二份Attempt authority与第二counter，拒绝。
- reservation直接等于`attempt-opened`：在full identity/dispatch lease前扩大authority，拒绝。
- sidecar作为allocation ledger或与RB1双写原子事务：形成第二truth source和跨ledger恢复环，拒绝。
- allocation后补Attempt、caller临时AttemptID、pathname existence恢复：不能关闭crash/replay/currentness，拒绝。
- 把existing worktree冒充新建目录或在Terminate删除/clean：receipt不诚实且可能破坏用户数据，拒绝。
- 为此重写通用runtime/allocation substrate：不符合v1最短纵切，拒绝。

## 10. 接受条件

本ADR已基于 sourceHead `e2af17931e123fca1c10d565cceae6dc03ef4d5a` 的独立 `APPROVE`（`P0=0`、`P1=0`）由维护者接受。接受只冻结合同；schema、实现、hostile matrix、fixed CLI真实Pi与独立Decision完成前，不得宣称S1′/S2′、RC1、hardened或Linux authority已完成。
