# ADR 0054：Cutover 等价性判定与 Effect Sink fencing（R5-A/R5-B）

- 状态：已接受（Accepted，2026-08-27；原编号 0051，因与远端 ADR 0051 并行占用改为 0054）；接受依据：维护者授权的 I186 快速收敛治理（Lead 直接冻结、停用独立 reviewer 轮转）。接受冻结本合同与 `internal/cutovereq`、`internal/effectsink` 收敛域实现及负测；不把新路径默认启用、旧路径删除、canary/rollback 演练或 conformance 终态提前为完成（R5-C/D 的执行证据另行登记）。
- 关联：[ADR 0045](0045-strangler-cutover-and-single-recovery.md)（cutover 判定合同指向，本 ADR 冻结其等价性判据）、[ADR 0018](0018-control-plane-and-provider-ports.md)（§13 原子 fencing 写入汇，本 ADR 扩展为 effect sink 独立 recheck）、[i186-r0-golden-trace.md](../research/i186-r0-golden-trace.md)（normalized trace schema 与对比规则）、审计 finding `I186-ARCH-CUTOVER-EQUIVALENCE`（P1）、`I186-ARCH-EFFECT-SINK-FENCING`（P1）。

## 背景

Issue #186 Final Review 确认两个 cutover 前置缺口：

1. `I186-ARCH-CUTOVER-EQUIVALENCE`：ADR 0045 的 old/new 全 digest 相等对真实非确定 Agent 不可满足。等价性必须拆成三类判据：权威迹不变量（真实 Agent 必须满足）、deterministic Fake 的内容 digest 相等、以及真实 Agent 的资源归一化不劣化统计；authority diff 不能被人工解释掉。
2. `I186-ARCH-EFFECT-SINK-FENCING`：ResultIngress recheck 只保护 ledger，不能撤销外部效果。SCM/Artifact/Secret 等 effect sink 必须在 mutation/secret use 前独立执行 current generation、fencing、authorization 与 target recheck，并覆盖 revoke→effect 竞态。

本 ADR 冻结两者的类型化判据；实现证据为收敛域纯新增包。

## 决策

### 1. Cutover 等价性三分判据（R5-A）

1. **权威迹不变量（真实 Agent 的 cutover 判据）**：old/new normalized trace 逐步配对（Sequence 对齐，条数不等即 misaligned 错误）。业务事实逐字段相等：`taskId/runId/attemptId/command.kind/digests（双侧出现者）`；任一不等为 `business-mismatch`，阻断 cutover。
2. **authority-upgrade 可解释升级**：`commandId`、`leaseFencingToken`、`allocationId`、`sandboxProvider`、`drcId/drcBinding`、双 registration/attestation digest 由空（old）变非空（new）是唯一允许的正向 diff，每条必须记入 trace diff 报告并满足形态校验（digest 形态、DrcBinding 与 attempt/generation 对齐）；不满足形态校验即按 `business-mismatch` 阻断。任何字段语义变化、new 侧变空或值改变一律 `unexplained-drift`，阻断 cutover。
3. **不可人工覆盖**：等价性判定只由 typed 比较产出；API 不提供解释/忽略开关。`Equivalent = 无 business-mismatch 且无 unexplained-drift`。
4. **deterministic Fake 的内容 digest 相等**：Fake 路径在权威迹不变量之上追加全量 Digests map 相等；任何漂移 `ErrFakeDrift` fail closed。
5. **真实 Agent 的资源归一化不劣化**：权威面计数（AttemptCount/GateRuns/ReviewRounds）必须精确相等，不等即 `ErrAuthorityRegression`；统计面（PeakMemoryBytes/WallMillis）新侧不得超过旧侧 ×(10000+toleranceBP)/10000，toleranceBP 有界 [0,10000]，旧基线为零时新侧非零即回归。统计面不是 authority，不构成人工解释 authority diff 的通道。

### 2. Effect Sink fencing（R5-B）

1. **pre-mutation 强制独立 recheck**：任何外部效果（`scm-mutation/artifact-write/secret-use/other-effect`）执行前必须以 `VerifyBeforeEffect` 按当前 ledger 独立重判，命令式主张（intent 自带字段）不构成事实。
2. `EffectIntent`：`IntentID + Sink + TargetID + IdempotencyKey + Generation + FencingToken + AuthorizationDigest + TargetDigest + canonical IntentDigest`；digest 可重算，篡改 fail closed。
3. 固定判序（前者优先）：intent/view 结构校验（硬错误）→ `authorization-revoked` → `generation-stale` → `fencing-mismatch` → `authorization-superseded` → `target-drifted`；结构性问题与生命周期拒绝（五封闭原因码）严格分流。`authorization-revoked` 是 revoke→effect 竞态的核心防线：授权撤销后到达的意图永不执行。
4. **幂等防重**：`EffectLedger` 以 IdempotencyKey put-if-absent；同 key 同 IntentDigest 幂等，同 key 异 digest `ErrEffectConflict`，绝不双写、绝不覆盖。`ExecuteIfAdmitted` 先 recheck 后入账，撤销后二次到达既拒绝又不执行。

## 后果与门禁

- 收敛域实现与负测：`internal/cutovereq`（step 校验矩阵、golden 链 positive、business-mismatch/unexplained-drift/misaligned 阻断、Fake equality、资源矩阵含零基线）与 `internal/effectsink`（五种生命周期拒绝逐一单变量负测、revoke→effect 竞态专测、幂等/冲突、复合门禁）。全部纯新增、fail closed、无时钟/无真实进程/无网络。
- 两个 finding 的合同层面由决策 1/2 分别关闭（cutover 结论还须 R5-D 的执行证据：trace 对比、rollback 演练、Local MVP 零回退）；finding 状态以 `docs/audit-report.md` 登记为准。
- `EffectIntent` 的 SCM/Publisher 生产 sink 接线、canary/rollback 演练与 host bypass 删除归 R5-C/D 执行层；golden trace 对比 harness 归 R5-D。本 ADR 不改变任何既有生产路径行为；Local MVP 零回退。
- 本 ADR 不升级 M10–M13 或 v1.0 状态。
