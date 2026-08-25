// Package revokedrain 承载 I186-R3 的撤销分级处置切片（Planning Baseline v3
// R3 步骤 5、Issue #191）：失效处置按 ADR 0018 §6 分级冻结——
// security-critical revoke（credential compromise、protocol violation 等安全
// 关键撤销）使在途 lease/allocation 立即失去资格：立即 cancel + generation
// bump + kill，不留任何 drain 窗口；planned/ordinary incompatible upgrade
// 一律使用新 registration/新 snapshot，旧实例 stop-new（不再接纳新
// Attempt/新 lease）+ bounded drain（有界时限内完成在途工作），drain
// deadline 到期再 fence（cancel + generation bump）；drain 窗口与 deadline
// 由冻结策略限定，不得无限延期。
//
// 普通升级不得复活 revoked/expired 的旧 registration，不得改写旧 lease 的
// 引用与 digest（只供审计回放）；事件携带机器可读原因码，与审计记录分开。
//
// 语义复用 internal/agentregistry（registration 生命周期）与
// internal/bindingcheck（SandboxLedger/Checker recheck）的既有冻结定义
// （均可 import，禁止修改）；本包只在收敛域内以纯确定性单测证明分级处置
// 语义（注入式确定性时钟，不依赖真实时间），不接线生产路径，真实
// Provider 接入归 I186-R5。
//
// 骨架由维护者先行落地，用于锚定 I186-R3-D1 TaskSpec 的 deliverable
// pathGlob 父目录（plan premortem qoder-deliverable-parent 门禁要求父目录
// 在锁定 baseRef 中已存在）；实现与表驱动测试由 Marshal Task 交付。
package revokedrain
