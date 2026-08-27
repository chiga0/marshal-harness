// Package hotpath 关闭审计 finding I186-ARCH-HOT-PATH-AUTHORITY 的合同：
// internal/resultingress 把 checkpoint/heartbeat/log 归为 hot path 并跳过
// registration/snapshot/evidence eligibility recheck，checkpoint 可能在未完成
// 冷路径校验时被 Restore 消费。本包以 deterministic、fail-closed 的接纳账本
// 冻结三条规则：
//
//  1. 热路径永不延长 lease / bump generation：extend-lease 与
//     bump-generation 只允许落在 ChannelCold 接纳之上；
//  2. 热路径永不决定 fencing：decide-fencing 只允许落在 ChannelCold
//     接纳之上；
//  3. 热路径永不产生冷路径不可复现的 authority：业务 kind
//     （worker-result/candidate/evidence-ref/receipt/assessment）必须经
//     ChannelCold 记录；只有冷路径完整接纳的 checkpoint 才可被 Restore
//     消费；同 digest 的 hot→cold 洗白在账本层即产生
//     ErrAdmissionConflict。
//
// 横切规则：record-observation 是唯一允许落在任意 channel 上的
// authority effect（热路径的存在意义就是低成本观察事实）。
//
// 收敛域边界：本包只冻结合同与判定逻辑——put-if-absent 接纳账本、
// authority-effect 门禁与 Restore 门禁；不实现 lease/generation/fencing
// 本身，也不感知时钟。resultingress 等生产路径向本账本语义的接线归
// I186-R5（strangler cutover / conformance）处理，不属于本收敛切片。
package hotpath
