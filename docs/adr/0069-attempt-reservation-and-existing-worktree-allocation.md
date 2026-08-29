# ADR 0069：Attempt 预留与 existing-worktree allocation

- 状态：提议（Proposed）
- 日期：2026-08-29
- 提议基线：`main@e1e81f8f4fe9438b54444ade8fca039964205d89`
- 影响范围：Run journal、Attempt budget、Local allocation、ResultIngress/Run-start producer chain
- 关联：[ADR 0051](0051-darwin-local-dogfood-profile.md)、[ADR 0057](0057-durable-local-allocation-recovery-and-production-composition.md)、[ADR 0063](0063-prepared-execution-authority-and-production-chain.md)、[ADR 0065](0065-sealed-run-start-proof-and-one-way-composition.md)、[ADR 0066](0066-production-composition-owner-acquisition.md)、[ADR 0067](0067-darwin-ordinary-user-launch-and-attach-recovery.md)、[ADR 0068](0068-mac-first-cli-only-lifecycle-preview-rc1.md)、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)

## 1. 背景

S1′/S2′ 的真实 producer 预审发现两个会令 fresh production chain 不可达的 P0：

1. 当前 `READY` Run 没有耐久 `AttemptID`。若在 allocation、Supervisor bootstrap 或 child side effect 之后才创建 reservation，崩溃重放可能产生第二个候选 Attempt；若提前把 Attempt 写入 Run self-loop，又会形成第二份 Attempt authority并提前/重复消费 Run budget。
2. `darwin-local-dogfood` 的 workspace-write 场景使用已经存在的 Git worktree。现有 Local allocation 的“创建新空目录”receipt 不能诚实描述该对象，也不能授权 Marshal 替换、清理或删除用户 worktree。

另有一个已确认的实现P0：当前ResultIngress store仍可能按pathname重新打开authority对象，且`ObserveCurrentCore`不能先于`OpenOwner`产生current结论。该问题已经由[ADR 0066](0066-production-composition-owner-acquisition.md)的canonical held-descriptor与两阶段owner边界决定；本ADR不建立新的信任决策，只要求S1′ rework改为held descriptor backend、把观察放在owner打开之后，并补齐rename/ABA负测。

本 ADR 只收敛 Mac ordinary-user、trusted single-user、workspace-write、`publication:none` 的 CLI-only RC1 路径；不提供 hardened sandbox、恶意仓库隔离、Linux authority 或 stable 发布授权。

## 2. 决策一：ResultIngress `attempt-opened` 是 fresh Attempt 的唯一预留

### 2.1 Run 在 sealed successor 前保持 READY

不新增 Run `READY → READY` self-loop。ResultIngress 在自己的 append-only ledger 中 creation-once 写入 `attempt-opened/v2` reservation；Run 在此后仍保持 `READY`、`CurrentAttemptID=""`、attempt counter不变。reservation只有在以下条件全部成立时才能提交：

- held Run projection为current `READY`且`CurrentAttemptID=""`，ResultIngress ledger不存在该READY head的current reservation；
- exact READY Run sequence/head与frozen Run inputs精确匹配；
- attempt budget尚有一个可用额度，但此时不得消费；
- 尚无 allocation binding、Supervisor bootstrap、child、command 或 publication side effect；
- `AttemptID`、ordinal 与 reservation canonical bytes 尚未被其他事实占用。

`AttemptOpenedReservationV2`至少绑定：`TaskID`、`RunID`、reserved `AttemptID`、expected ordinal、exact READY Run sequence/head、`SpecDigest`、`PolicyDigest`、`CapabilityDigest`、`BaseSHA`、`WorktreePath`与protocol revision。reservation digest由完整canonical payload确定性重算，不接受caller提供的digest-only echo。

### 2.2 creation-once、预算与 response loss

- ResultIngress `attempt-opened/v2` append+fsync是reservation creation-once的linearization point；它不改变Run，也不消费attempt budget。
- 相同 request canonical bytes 的响应丢失通过 current journal exact replay 返回同一 reservation；不重复 append、不重复计数。
- 相同 `AttemptID`/ordinal 但不同 bytes、相同 Run 的第二个 current reservation、或不同 Run/Attempt 对同一 reservation 的借用均 fail closed。
- 后续唯一sealed `READY → RUNNING` 的 `run.start-outcome/v2` 必须同时引用exact reservation、exact READY head与同一`AttemptID`；只有该successor原子写入Run `CurrentAttemptID`并恰好消费一次attempt budget。
- 旧 journal 中没有 reservation 的 `READY → RUNNING` 继续按旧协议逐字节重放和旧计数语义解释，但只用于历史 inspection/explain，不能 mint 新 S1′ proof、不能授权新的 child 或 RC1 canary。

对同一exact READY head，两个并发Prepare只能产生一份reservation；相同bytes响应丢失时replay同一fact，任何不同AttemptID/bytes均拒绝。崩溃发生在reservation fsync前时没有reservation、预算消费或下游副作用；发生在fsync后时只能恢复同一reservation。Run-start successor fsync前Run仍为READY且预算未消费；fsync后Run唯一进入RUNNING并只计数一次。不得用“响应未知”创建sibling reservation或Attempt。

## 3. 决策二：bind-existing-worktree 是独立 allocation profile

### 3.1 受绑定 live object，而非新建资源

新增 `bind-existing-worktree/v1` profile。目标必须是已经由 Git 登记的 existing worktree；Marshal 将其作为受绑定 live object 使用：

- 不创建、替换、移动、reset、clean、prune 或删除目标 worktree；
- 不伪造“新建空目录”或 provider-owned directory receipt；
- bind 前通过 held descriptors 观察 target current-name、目录 object identity、Git common-dir/worktree admin object、HEAD/base 与 frozen Run inputs；
- fresh bind 要求 exact `BaseSHA`、干净的受支持 worktree 状态、absolute clean `WorktreePath`，并拒绝 symlink traversal、对象类型/owner/mode/identity 漂移；
- launch 与 ResultIngress current-source 检查继续复用同一 held object lineage，不按 pathname 重新获得权威。

ordinary-user profile 只保证对可信用户工作区的确定性绑定与漂移拒绝，不声称能够阻止同 UID 外部进程修改 worktree。

### 3.2 sidecar append-only binding ledger

canonical repository `.marshal` 下新增 owner-private、descriptor-bound 的 allocation binding sidecar ledger。它以 intent/outcome/release 链记录：

- provider/profile revision、AllocationID/generation/fencing token；
- exact reservation fact/digest、Task/Run/Attempt 与 frozen input digests；
- target current-name observation、directory object identity、Git administrative identity 与 base HEAD；
- bind intent/outcome receipt、release intent/outcome 及各自 predecessor head。

`ExistingWorktreeBindingReceiptV1` 是逻辑绑定 receipt，不得与“目录创建成功”的 legacy provision receipt互换。一个 target current-name/object identity 同时最多有一个 active binding；一个 reservation 也只能绑定一个 target。跨 Run/Attempt 的重复绑定一律 fail closed；同一 canonical request 的 lost response 只 exact replay 同一 outcome。

`Terminate` 只 append 逻辑 release 并释放 Marshal 持有的 descriptor/sidecar binding；不得删除、重置或清理用户 worktree与Git admin entry。intent-only 或 outcome response loss 只能由同一 sidecar ledger reconcile，不能通过检查“目录是否存在”推断成功。

## 4. 持久化与协议迁移

不得原地重解释或重写旧记录：

1. ResultIngress schema新增`attempt-opened/v2` reservation及其exact READY head/frozen-input projection；Run schema在READY时仍无Attempt，只由sealed `run-start-outcome/v2` successor原子写入`CurrentAttemptID`与budget counter。
2. fresh Run-start 使用 `run-start-outcome/v2`，精确引用 reservation；相应 `PreparedExecutionV2`、`PreparedRunStartV2` 与 sealed claim/proof 绑定 reservation fact/digest及 existing-worktree binding receipt。V1 仅供历史 replay。
3. allocation schema 增加 `bind-existing-worktree/v1` 的 intent/outcome/release closed union；legacy create-empty receipt 不能迁移或转换成该 receipt。
4. decoder 必须按 protocol revision 选择旧/新语义；未知 revision、字段缺失、optional-field laundering 与新旧 union 混用均 fail closed。
5. ResultIngress read-only projection携带reservation及其exact READY head；runstore read-only closed projection只携带current READY head与frozen inputs。两者不得镜像对方ledger，也不得暴露raw journal、owner、generation或可写callback。

协议接受后需同步 schema、fixture、canonical digest 与 replay migration 测试；在此之前不得把实现状态升级为 `INTEGRATED`。

## 5. 固定 producer 顺序

fresh S1′/S2′ 的最短顺序冻结为：

```text
existing-only open Run/held Lease（未知 Run 不得 mkdir）
  → ResultIngress append+fsync attempt-opened/v2 reservation（Run仍READY，预算未消费）
  → bind-existing-worktree intent/outcome（目标零创建/替换）
  → owner / reserved-Attempt binding
  → allocation binding receipt admission
  → launch-authorized / StoredClosure
  → PreparedExecutionV2
  → Supervisor bootstrap/spawn/resume
  → sealed proof
  → run-start-outcome/v2（同一reservation，唯一写Attempt并消费预算）
```

reservation之前禁止allocation/bootstrap/child副作用。reservation之后若bind validation拒绝，唯一耐久变化是ResultIngress reservation；Run仍为READY、预算未消费，目标worktree保持逐字节不变，不创建sibling reservation。S2′不得对不可信/未知Run调用会`MkdirAll`的普通`Acquire`，必须使用existing-only open/acquire语义。

## 6. S1′/S2′ 最短实施切片

1. **S1′-A（ResultIngress reservation）**：实现`attempt-opened/v2` creation-once、同READY head唯一性与response-loss replay；Run READY projection保持空Attempt/不计预算。
2. **S1′-B（held authority + successor）**：把path reopen改为ADR0066已要求的held descriptor backend，并把`ObserveCurrentCore`拆到`OpenOwner`之后、由同一held owner/current-name检查驱动；`PreparedExecutionV2`绑定reservation、exact READY head、binding receipt与held originals；`run-start-outcome/v2`才唯一写Attempt/消费预算。
3. **S2′-A（allocation）**：实现 existing-only Run open/acquire和 `bind-existing-worktree/v1` sidecar ledger、held target observation、唯一 active binding及 release-only terminate。
4. **S2′-B（production composition）**：严格按第5节 producer顺序接入 fixed CLI/Pi；不得使用 Fake seed、legacy `execution.Run`、create-empty receipt或第二 authority root。

四项完成后才恢复 ADR0067 Attach/rebind、terminalization与RC1 E2E；本 ADR 提议本身不升级 R2–R6。

## 7. 必须通过的负面与恢复矩阵

| 类别 | 必须证明的拒绝/恢复行为 |
| --- | --- |
| reservation 并发 | 同一exact READY head的两个并发Prepare只有一个reservation成功；另一个exact replay或conflict；Run仍READY且预算为零消费 |
| reservation 响应丢失 | outcome丢失后同 bytes返回同 fact；不同 bytes、ID、ordinal、frozen input均拒绝 |
| budget | reserve admission先证明预算可用但不消费；budget在sealed READY→RUNNING successor恰好消费一次；successor响应丢失replay不二次计数 |
| legacy | 无 reservation 的旧记录只能历史 replay；不能 mint proof、启动 child或进入新 RC1 |
| producer 顺序 | reservation缺失/不current、binding receipt缺失/跨Attempt、PreparedExecution reservation不一致时所有下游调用数为零 |
| worktree 类型 | 不存在、非Git worktree、wrong common-dir、wrong BaseSHA、dirty、非目录、任一路径段symlink均在bind前拒绝 |
| identity/ABA | current-name rename、rename-away/back、目录替换、inode/device/owner/mode变化、Git admin object替换均拒绝；受保护regular leaf hardlink数异常拒绝 |
| 重复绑定 | 同一target跨Run/Attempt或同一reservation绑定另一target拒绝；同一请求响应丢失只重放一份receipt |
| crash/replay | bind/release intent前后、outcome fsync前后分别崩溃时，reconcile只查sidecar并得到唯一结论；不得由pathname存在性猜测 |
| terminate | release成功后worktree、HEAD、index、untracked bytes与Git admin entry保持不变；不得调用delete/reset/clean/prune |
| sidecar | 缺失、截断、乱序、伪造receipt、generation/fencing漂移、跨repository复制均fail closed |
| ResultIngress descriptor | `OpenOwner`前不得`ObserveCurrentCore`；owner打开后只在同held descriptor lineage观察。authority root/ledger pathname被rename/替换/ABA时不reopen新对象；held object与current-name不一致时拒绝proof |
| secret/path | raw绝对路径仅保存在owner-private本地Run/sidecar；不得进入Worker prompt、transcript、ReviewPacket、Outcome、日志或错误文本 |
| 零副作用 | reserve前拒绝为零预算/零side effect；reserve后bind validation拒绝只保留ResultIngress reservation，Run仍READY/预算未消费，目标零修改、零bootstrap/child；不确定intent只reconcile同一reservation |

## 8. 被取代与保留范围

- 对 fresh `darwin-local-dogfood` workspace-write 路径，本 ADR提议以 reservation + `bind-existing-worktree/v1` 取代 ADR0057 中“Local allocation 必须创建新空目录”的冲突解释；ADR0057 对新建 provider-owned allocation 的原合同仍保留。
- 本 ADR细化 ADR0063/0065/0067 的 producer chain与sealed proof输入，不改变 Worker不自证、ResultIngress current-ledger recheck、Worker/Publisher分权或independent Decision。
- ADR0066 的canonical held-descriptor与两阶段owner acquisition保持不变；ResultIngress path reopen和`OpenOwner`后才可`ObserveCurrentCore`的时序修复是其既有实施P0，不是本ADR新增authority。
- ADR0068 的 unsigned Darwin arm64 CLI-only、explicit opt-in、non-production边界保持不变。

## 9. 未选择方案

- **在 allocation 后补写 Attempt**：崩溃窗口会产生无主副作用或 sibling Attempt，拒绝。
- **用 Run READY self-loop预写Attempt**：会形成第二份Attempt authority并把预算消费拆成两个ledger，拒绝。
- **让 caller 提供临时 AttemptID**：不能证明 current、budget与response-loss唯一性，拒绝。
- **把 existing worktree冒充新建目录**：receipt不诚实且可能授权误删，拒绝。
- **复制/移动现有worktree到 Marshal目录**：改变用户对象且扩大RC1范围，拒绝。
- **按 pathname existence恢复**：不能关闭rename/ABA/symlink，拒绝。
- **为此重写通用 allocation/runtime substrate**：不符合v1最短纵切，拒绝。

## 10. 接受条件

本 ADR 当前仅为 Proposed。只有唯一独立 reviewer 对 exact sourceHead 确认 `P0=0`、`P1=0`，维护者完成接受同步后才能标记 Accepted。接受仍只冻结合同；上述 schema、implementation、hostile matrix、fixed CLI真实Pi与独立Decision证据完成前，不得宣称S1′/S2′已实现、RC1可发布或Marshal具备hardened/Linux authority。
