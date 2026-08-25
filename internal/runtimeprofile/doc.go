// Package runtimeprofile 承载 I186-R3 的 WorkerRuntimeProfile 冻结切片
// （Planning Baseline v3 R3 步骤 3、Issue #191）：WorkerRuntimeProfile 由
// AgentBinding + SandboxBinding + compatibility/profile digest 构成——两个
// binding 可独立替换与撤销（revoke/expire/replace 后旧组合不再产生可接纳
// 的 profile 语义，per-Attempt recheck 由 I186-R3-C 收口）；profile 不隐藏
// 底层身份（registration/snapshot 绑定全部可摘要、可核对）；profile 及其
// binding 结构不包含任何 raw credential/token——跨 Port credential 复用
// 与证据冒充负测由 I186-R3-D 收口。
//
// AgentBinding 绑定 internal/agentregistry 的 RegistrationID/SnapshotDigest
// 语义（可 import，禁止修改）；本包只在收敛域内以纯确定性单测证明
// profile 冻结语义，不接线生产路径，legacy Adapter 迁移归 I186-R5。
//
// 骨架由维护者先行落地，用于锚定 I186-R3-B TaskSpec 的 deliverable
// pathGlob 父目录（plan premortem qoder-deliverable-parent 门禁要求父目录
// 在锁定 baseRef 中已存在）；实现与表驱动测试由 Marshal Task 交付。
package runtimeprofile
