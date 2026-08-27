// Package jitgate 承载审计发现 I186-ARCH-JIT-ADMISSION-RECHECK 的收敛域切片。
//
// 背景：JIT provision 扩大了 admission→provision 时间窗。生产路径现状
// （测量结论，不在本包复验）：admission→provision 链路没有 validUntil
// 概念，也没有强制的 provision 时点重验（dispatch.Matcher.Revalidate
// 存在但调用方可选）。该重验不得顺延到 R6。本包在收敛域内冻结契约：
// 任何 provision 发生之前，必须对签发时的 AdmissionDecision 重新验证
// 五项事实——
//
//  1. token 自身完整性：TokenDigest 对五个身份字段（DecisionID、
//     RegistrationID、SnapshotDigest、PolicyDigest、ValidUntilUnix）
//     canonical 重算，任一字节被改即 ErrTokenTampered；
//  2. registration 当前 active，否则 registration-inactive；
//  3. 当前 active snapshot digest 与 token 钉住的一致，否则
//     snapshot-superseded（snapshot generation/supersede 语义的收敛化
//     表达：当前 active digest 与 token 钉住的不一致即拒绝）；
//  4. policy 当前 active（policy-inactive），且 current policy digest
//     与 token 钉住的一致（policy-rotated）；
//  5. 注入时钟 now 仍在半开区间 [issue, validUntil) 内，否则
//     token-expired（now == validUntil 已过期）。
//
// 评估顺序冻结，首个失败即返回。返回形状纪律：结构性问题（token 篡改或
// 畸形、LedgerView 形态非法）只走 error 通道且不产出 Verdict；策略/生命
// 周期拒绝只经 Verdict.Reason 表达且 error 为 nil，两通道互不泄漏。
// RejectionReason 是封闭枚举，取值只由本包常量构造，未知值永不出现。
//
// 边界：本包只冻结契约，并以纯确定性单测证明语义（注入时钟、无 IO、无
// 并发状态）；production wiring（admission 侧签发 AdmissionToken、
// provision 调用点强制经过 VerifyBeforeProvision）归 I186-R5
// strangler cutover，不在本包范围。
package jitgate
