// Package locationattest 承载 I186-R3-E 的收敛域切片（Issue #191 Exit
// Gate 与审计 finding I186-ARCH-LOCATION-ATTESTATION）：执行位置
// attestation 合同与纵切。
//
// 冻结分型（本包的核心合同）：
//
//   - LocationClaim：provider-asserted location claim。Sandbox Provider
//     自报的执行位置声明（allocation/generation/handle 提示 + digest）。
//     claim 只能用于诊断、审计叙事与收紧权限；单独永远不能支撑
//     production assurance 或 publication。被证明方不得自证。
//   - LocationFact：authority-verified location fact。由故障域外
//     authority observer（supervisor/kernel held handle 或独立
//     attestation）观测并签发的事实：allocationId + generation +
//     handleKind（封闭枚举 pid/process-group/cgroup/vm-handle/
//     independent-attestation）+ handleDigest + observerID +
//     canonical FactDigest。只有 fact 能支撑 production assurance。
//
// 门卫规则（Verifier.Evaluate）：
//
//   - claim 必须结构合法（digest 可重算）；
//   - production assurance 要求存在至少一条与 claim 同
//     (allocationId, generation) 的 fact，且 fact.observerID ≠
//     claim.providerRegistrationID（自证 fact 被排除，视同无 fact）；
//   - 只有 claim 没有匹配 fact → claim-only，不得支撑 production
//     assurance；伪造 claim（digest 篡改）、伪造 fact（observer 即
//     claimant 本人、handle kind 越界、digest 不可重算、跨
//     allocation/generation 挪用）一律 fail closed。
//
// FactLedger 是 put-if-absent 的 immutable fact 存储；注册时重算
// FactDigest，篡改即拒绝；重复注册同一 fact 幂等。
//
// 本包与测试只在收敛域内以纯确定性单测证明合同（无时钟、无真实进程、
// 无网络）；真实 kernel held handle 采集与 supervisor 观测接线归
// I186-R5/R6，production assurance 消费归发布门禁，不在本包。
package locationattest
