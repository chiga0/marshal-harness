// Package attemptgate 承载 I186-R3-D 的收敛域切片（Issue #191 Exit Gate：
// ResultIngress 从 Attempt 解析 immutable profile，并分别 current-ledger
// recheck AgentBinding 与 SandboxBinding；仅一侧 revoke/replace 的双向负测
// 均 fail closed；Agent evidence ≠ Sandbox evidence，跨 Port
// credential/token/evidence 复用失败）。
//
// 本包提供三个收敛域原语：
//
//  1. AttemptProfileStore：Attempt → WorkerRuntimeProfile 的 immutable
//     绑定存储。put-if-absent；同 attemptID 同 ProfileDigest 幂等，同
//     attemptID 不同 ProfileDigest fail closed（冲突）。绑定只增不改。
//  2. Gate：结果接纳门禁。AdmitAttemptResult 从 Attempt 解析 immutable
//     profile，经 bindingcheck.Checker 对 AgentBinding 与 SandboxBinding
//     分别做 current-ledger recheck（两侧始终独立完整评估、互不短路），
//     并校验出示的 evidence digest 属于该 Agent 当前 active snapshot 的
//     封闭证据集合。任一侧失效或证据未绑定即拒绝。
//  3. 证据边界负测：Sandbox conformance evidence 与 Agent EvidenceRecord
//     分属不同 Port 的类型系统（前者由 internal/sandbox 独立 verifier
//     签发，后者强制 ProviderType=agent）；跨 Port 出示、跨 registration
//     借用、伪造 digest 均 fail closed。
//
// 语义复用 internal/agentregistry（registration/snapshot 生命周期与证据
// 集合）、internal/runtimeprofile（WorkerRuntimeProfile 冻结类型）与
// internal/bindingcheck（双侧独立 recheck）的既有冻结定义（均可 import，
// 禁止修改）；本包与测试只在收敛域内以纯确定性单测证明门禁语义（无
// 时钟依赖、无真实进程、无网络），不接线生产路径——ResultIngress 生产
// 接线归 I186-R5 strangler cutover。
package attemptgate
