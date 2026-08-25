// Package bindingcheck 承载 I186-R3 的 per-Attempt binding set recheck 切片
// （Planning Baseline v3 R3 步骤 4、Issue #191）：每个 Attempt 的
// WorkerRuntimeProfile binding set 在接纳时刻必须按当前账本重判——Agent
// registration/snapshot 与 Sandbox allocation 各自独立 current-ledger
// recheck；任一 binding 被 revoke/expire/replace（或 generation/fencing
// 不符、lease 过期）后，旧组合一律不可接纳（fail closed + 封闭 typed
// reason），替换后的新组合独立通过核验；两 binding 的撤销互不牵连
// （Agent 撤销不改变 Sandbox binding 的核验结论，反之亦然）。
//
// 语义复用 internal/agentregistry（registration 生命周期）与
// internal/runtimeprofile（binding/profile digest）的既有冻结定义（均可
// import，禁止修改）；本包只在收敛域内以纯确定性单测证明 recheck 语义，
// 不接线生产路径，真实 Provider 接入归 I186-R5。
//
// 骨架由维护者先行落地，用于锚定 I186-R3-C TaskSpec 的 deliverable
// pathGlob 父目录（plan premortem qoder-deliverable-parent 门禁要求父目录
// 在锁定 baseRef 中已存在）；实现与表驱动测试由 Marshal Task 交付。
package bindingcheck
