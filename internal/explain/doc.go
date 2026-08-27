// Package explain 承载 ADR 0052 §6 / ADR 0045 决策 2 的 R4 接线：
// `marshal explain run` 的账本装配层。从真实 Run journal/snapshot/attempt
// 目录把当前处置事实装配成单一恢复模型的 RecoveryInput（ledger state →
// pending/ambiguous command → Inspect/Reconcile 事实 → binding+lease
// recheck 事实），经 internal/recovery.Decide 得到唯一幂等结论并以
// recovery.Render 渲染——与 production attempt 决策同构，不是单独的
// "解释器"。离线 explain 只读，不改变任何权威状态。
package explain
