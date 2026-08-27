// Package recovery 承载 I186-R4 的收敛域切片：单一恢复模型（ADR 0045
// 决策 2 的实现冻结）与 explain 渲染模型（Planning Baseline v3 R4）。
//
// 冻结的唯一恢复链：
//
//	ledger state
//	→ pending/ambiguous command
//	→ Provider Inspect/Reconcile（typed Observation）
//	→ active binding + lease recheck
//	→ resume | fence + new Attempt
//
// 本包把恢复决策收敛为一个纯确定性判定函数 Decide：输入是 typed
// LedgerView（权威账本视图）、ProviderObservation（Inspect/Reconcile 归一
// 结论）、bindingcheck 双侧 recheck 结果与（可选）failureclass 分类；
// 输出是唯一幂等结论——resume，或 new Attempt（是否需要先 fence 由
// RequiresFence 表达）。冻结规则：不能证明安全时一律 fence + new
// Attempt；Provider/session/engine backend 不得自行决定业务
// retry/rework/terminal state，一切放宽只来自 failureclass 的
// authority-observed 分类输入。
//
// 故障矩阵（每类唯一幂等结论，见 decide.go 决策表）：duplicate
// delivery、lost response、process death、provider restart、network
// partition、stale result、partial artifact、ambiguous side effect。
// ambiguous side effect 结论强制 RequiresReconcile：新 Attempt 在与
// 同一 effect target 交互前必须先完成幂等键对账，杜绝重复 Publication。
//
// Explanation 是 explain 渲染的权威模型：时间线条目、当前
// lease/bindings、外部冲突、恢复决策与下一动作，决定论文本输出足以让
// 非作者复盘（`marshal explain run` 的产品接线归 R5/R6；本包只冻结
// 决策语义与渲染模型）。
//
// 本包与测试只在收敛域内以纯确定性单测证明合同（无时钟、无真实进程、
// 无网络），不接线生产路径。
package recovery
