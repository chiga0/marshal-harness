# ADR 0045：Strangler cutover 与单一恢复模型指向

- 状态：已接受（Accepted，2026-08-24）；接受证据：独立 reviewer 对 R0 产物审查 verdict=accept 且 P0/P1 清零；接受只冻结合同，未实现，不升级任何 milestone 状态
- 关联：ADR 0018、ADR 0019、ADR 0036、ADR 0043、ADR 0044、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)、[Issue #187](https://github.com/chiga0/marshal-harness/issues/187)、Planning Baseline v3（R4/R5）、[i186-r0-golden-trace.md](../research/i186-r0-golden-trace.md)

## 背景

Planning Baseline v3 决策迁移采用 strangler pattern：新旧路径并存、normalized trace 对比、canary cutover、最后删除 production host bypass——不做一次性重写，M0–M9 代码/测试/历史 `PASSED` 全部保留。同时 Issue #186 Final Review 指出（I186-P1-4）Provider/session/engine 失败缺统一恢复解释。本 ADR 冻结 cutover 的判定合同，并把单一恢复模型指向 R4 实施。

## 决策

### 1. Cutover 判定合同（R5）

1. **normalized business trace 对比是唯一等价性证据**：同类任务的 old/new 路径必须按 [i186-r0-golden-trace.md](../research/i186-r0-golden-trace.md) 冻结的 schema 逐字段对比——`taskId/runId/attemptId/sequence/digests` 必须相等（业务事实不变）；`commandId/allocationId/drcId` 从 null 变为非空且可 current-ledger recheck；其余语义变化必须显式列入 trace diff 报告，未解释 diff 阻断 cutover。
2. **推进顺序固定**：新路径先 feature/profile gated 且只跑 Fake Agent，再单一真实 Agent dogfood，再 canary 扩大到所有 ordinary-user Adapter，最后 production profile 默认新路径。禁止跳级。
3. **legacy 降级而非立即删除**：cutover 期间 legacy `Adapter.Run(host)` 降为 explicit local-nonproduction compatibility profile（ADR 0043 决策 7），仍可服务 Local MVP 回归；所有 production caller 完成迁移后才删除 host bypass。
4. **回滚约束**：rollback 演练必须证明不复活旧 lease/registration、不产生第二业务事实；rollback 后 authority 仍以 ledger 为准，不得回退到旧路径的内存/宿主状态。
5. **零回退门禁**：cutover 声明必须重放 [i186-r0-baseline-report.md](../research/i186-r0-baseline-report.md) 第 2 节的 Local MVP required regression 并引用 baseline commit，结果零回退。

### 2. 单一恢复模型（R4，本 ADR 只冻结方向不冻结实现）

1. 恢复决策统一为三种幂等结论：`resume`、`fence`、`new Attempt`；不能证明安全时一律 `fence + new Attempt`。
2. Provider/session/engine backend 不得自行决定业务 retry/rework/terminal state；恢复只能从统一链反推：`ledger state → pending/ambiguous command → Provider Inspect/Reconcile → active binding + lease recheck → resume | fence | new Attempt`。
3. Provider 必须接入 Inspect/Reconcile/Cancel/Terminate 与不可绕过的 current lease resolver；远程/Cloudflare provider 增加 Provider 侧 generation/fencing defense-in-depth（关闭 I186-P1-5）。
4. 提供 `marshal explain run <run-id>` 或等价 API：输出权威时间线、当前 lease/bindings、外部冲突、恢复决策与下一动作，足以让非作者复盘。
5. 故障矩阵必须覆盖：duplicate delivery、lost response、process death、provider restart、network partition、stale result、partial artifact、ambiguous side effect；每类故障有唯一幂等结论，无双写、无丢 Evidence、无重复 Publication。

### 3. 与旧 milestone 的关系

- ADR 0036（`Adapter.Run` 返回边界 fail-closed）在 legacy compatibility profile 存续期间继续有效，cutover 删除 host bypass 后其适用范围自然收缩，不提前废止。
- M10–M13 代码切片（含 Cloudflare state/intent slices）在 R6 DONE 前只作 R4/R6 fixture，不进入 production 路径；R6 后是否恢复由独立 reviewer verdict 与维护者重排决定。

## 后果与门禁

- R5 Exit Gate：新路径默认启用、回滚演练通过、旧路径不再服务 production、Local MVP required regression 零回退。
- R4 Exit Gate：故障矩阵每类有唯一幂等结论，explain 输出可独立复盘。
- 本 ADR 不新增信任边界、不改变发布权限；接受本 ADR 不升级任何 milestone 状态。
