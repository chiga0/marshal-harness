# ADR 0070：existing-worktree-binding/v1 的 FrozenInputsDigest 澄清

- 状态：已接受（Accepted）
- 日期：2026-08-31
- 提议基线：`feat/pi-s2-production-composition@d65785d`
- 影响范围：existing-worktree-binding/v1 的 `ExistingWorktreeBindingV1.FrozenInputsDigest` 字段含义与派生口径
- 关联：[ADR 0069](0069-attempt-reservation-and-existing-worktree-allocation.md)、[ADR 0066](0066-production-composition-owner-acquisition.md)、[ADR 0067](0067-darwin-ordinary-user-launch-and-attach-recovery.md)

## 1. 背景与边界

[ADR 0069](0069-attempt-reservation-and-existing-worktree-allocation.md) 冻结了 `existing-worktree-binding/v1` 的 closed-union（`bind-intent → bind-receipt → release-intent → release-receipt`）与 `ExistingWorktreeBindingV1` 字段集合，但未逐一规定 `FrozenInputsDigest`、`RepositoryOwnerDigest`、`ExpectedAttemptSequence` 的精确派生口径。S2′ 生产组合在落地 path B 时发现这三处存在“字段已存在、派生口径未冻结”的窗口：若不同实现把它们各自解释成 `ReservationKeyDigest`、pathname 派生摘要或 admission 后的 Attempt head，bind receipt 会变成可漂移对象，从而无法支撑 ADR 0069 §3.2 的 repository-global target uniqueness 与 response-loss 同 bytes 收敛。

本 ADR 仅澄清既有字段的派生口径，不新增 FactType、不新增协议 revision、不扩大 schema、不改变信任边界或持久化契约。它不取代 ADR 0069 的任何决策，只关闭其遗留的“字段含义未冻结”窗口。S2′ 的 path A（staging AllocationProvision）合同完全不变。

## 2. 决策

### 2.1 FrozenInputsDigest

`ExistingWorktreeBindingV1.FrozenInputsDigest` 固定为 canonical JSON closed struct 的 sha256：

```json
{ "specDigest": "<sha256>", "policyDigest": "<sha256>", "capabilityDigest": "<sha256>" }
```

冻结规则：

1. 该 struct 只含这三个字段，顺序无关（canonical JSON 按字段名排序），不得追加、省略或重命名任何字段。
2. 三个 digest 均来自 READY Run 的冻结输入投影（`RunStartAuthorityProjection.SpecDigest/PolicyDigest/CapabilityDigest`），与 reservation payload 绑定的 frozen inputs 同源；不接受 caller 单独传入的 digest-only echo。
3. `BaseSHA` 与 `WorktreePath` 已经在 `ExistingWorktreeBindRequestV1`（`ExpectedBaseSHA`、`WorktreePath`）分别绑定，不重复放入 `FrozenInputsDigest`。把任一者并入该 digest 会与 request 自身的 `RequestDigest` 形成重复绑定并破坏 closed-union 的逐字段 recheck。
4. 不得使用 `ReservationKeyDigest` 作为 `FrozenInputsDigest`。reservation key 绑定的是 `(RunID, exact READY sequence/head)` 的 replay key；existing-worktree binding 的 frozen inputs 是 Run 的冻结 launch inputs，二者语义不同，混用会让 reservation replay 与 worktree bind 的 recheck 互相牵连。
5. 该 digest 在 bind-intent 与 bind-receipt 中逐字节相等；任何漂移均 fail closed，且不更新旧 fact。

### 2.2 RepositoryOwnerDigest

`ExistingWorktreeBindingV1.RepositoryOwnerDigest` 固定为 bind admission 时 current `ControlOwnerState.FactDigest`（即 `OpenOwner(scope)` 返回的 exact current owner fact digest）。

冻结规则：

1. 它是 owner-only、repository-scope 的 current owner fact 摘要，不是 acquisition 候选对象、不是 scope digest、不是 pathname 派生摘要。
2. bind-intent 追加前由持有 repository owner lock 的 producer 从 current ledger 读取一次并冻结进 binding；bind-receipt 与 release 全链路逐字段重验同一值。
3. owner epoch 轮换、owner fact 漂移或 ledger 重写后，旧 binding 的 `RepositoryOwnerDigest` 不得被重新解释为 current；release 仍按 ADR 0069 §3.4 在固定锁序内重验 current owner。

### 2.3 ExpectedAttemptSequence

`ExistingWorktreeBindingV1.ExpectedAttemptSequence` 固定为 bind admission 前当前 `AttemptAuthorityState.Revision`（即 `BindOwnerToAttempt` 完成后、existing-worktree bind-intent 追加前的 Attempt revision）。

冻结规则：

1. 它是 Attempt authority 的线性 revision，不是 allocation generation、不是 dispatch lease sequence、不是 Run sequence。
2. bind-intent 追加使 Attempt revision 前进一次；bind-receipt 的 `PredecessorRB1HeadDigest` 引用 bind-intent 后的 head，release 链继续线性推进。`ExpectedAttemptSequence` 只锚定 admission 前的 revision，用于与 `ExistingWorktreeCurrentAuthorityV1.validateBinding` 的 `current.ExpectedAttemptSequence == binding.ExpectedAttemptSequence` recheck 对齐。
3. 任何把 admission 后的 revision、Run `AttemptsUsed` 或 allocation generation 填入该字段的行为均 fail closed。

### 2.4 派生时序

path B 的 producer 在 `BindOwnerToAttempt` 之后、`ExistingWorktreeController.Bind` 之前，于同一 repository owner lock 临界区内按以下顺序冻结上述三字段：

```text
current ControlOwnerState.FactDigest        → RepositoryOwnerDigest
current AttemptAuthorityState.Revision       → ExpectedAttemptSequence
READY SpecDigest/PolicyDigest/CapabilityDigest → FrozenInputsDigest (closed struct sha256)
```

三字段一旦冻结进 `ExistingWorktreeBindRequestV1.Binding` 并 `Seal()`，即随 bind-intent 持久化；后续 bind-receipt、release-intent、release-receipt 与 PreparedExecution 的 path B recheck 全部逐字段重验同一冻结值，不接受任何中间态重算。

## 3. 不变与边界

- 本 ADR 不改变 `existing-worktree-binding/v1` 的协议 revision、FactType 集合或 schema 字段名；`ExistingWorktreeBindingV1` 的 `Validate()` 仍只校验 digest 形状与 generation/sequence 范围。
- 本 ADR 不放宽 ADR 0069 §8 的任何负面矩阵：target 唯一性、response-loss 同 bytes 收敛、path A/B 互斥、release 逐字段 recheck 全部保留。
- raw `WorktreePath` 仍只允许存在于 owner-private Run 冻结输入与 RB1 authority fact（`ExistingWorktreeBindRequestV1.WorktreePath`）；public API、prompt、transcript、projection 文件名仍 path-free（ADR 0069 §5）。
- 本 ADR 不升级 R2–R5 的 `COMPONENT` 成熟度，不授权真实 Pi canary、terminalization、Linux 或发布。

## 4. 接受条件

本 ADR 仅澄清字段派生口径，接受即冻结该口径。实现侧须证明：bind-intent 与 bind-receipt 的三字段逐字节相等；admission 前/后 revision 混用、`ReservationKeyDigest` 冒充 `FrozenInputsDigest`、owner fact 漂移后旧 binding 重解释为 current 均被 fail-closed 拒绝。完成 path B 生产接线与定向负测前，不得据此宣称 S2′、RC1 或 stable 已完成。
