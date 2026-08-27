// Package failureclass 承载 I186-R3-F 的资源/故障分类 authority 收敛域切片，
// 关闭审计发现 I186-ARCH-RESOURCE-CLASSIFICATION-AUTHORITY：
// ResourceEnvelope.observedPeak、termination reason 与 infra-failure 分类权
// 之前未冻结，本切片冻结其合同。
//
// 冻结的合同分三段：
//
//  1. 来源（source）：ObservationSource 封闭枚举区分
//     authority-observed（来自 workload/Provider 故障域外的带外观察，如
//     supervisor / kernel held handle）与 provider-asserted（Provider
//     自报）；未知来源 fail closed。
//  2. digest：ResourceEnvelope 必须携带形态合法的 ObservationDigest
//     （"sha256:" + 64 位小写 hex），把分类结论绑定到具体的带外观察记录；
//     形态非法一律返回带 "failureclass: " 前缀的类型化错误，绝不静默降级。
//  3. 方向性（directionality）：只有 authority-observed 的 infra 候选
//     termination（oom-killed / time-limit / signal-killed / io-error /
//     network-unreachable）才可分类为 failure:infra 并放宽 retry/预算、
//     豁免 semantic rework；provider-asserted 声明只能诊断或收紧，一律
//     分类为 failure:provider-claimed-infra 且两个放宽标志恒为 false；
//     semantic failure（exit-nonzero）无论来源都保持 failure:semantic，
//     authority 观察也不能把 semantic 洗白成 infra；unknown termination
//     按冻结规则归为 failure:semantic（unknown 永不放宽）。
//
// 消费边界：本包只冻结分类 authority，输出 Classification 供 R4 的恢复
// 决策消费；是否真正放宽预算、是否豁免 semantic rework 由 R4 恢复模型决定，
// 不在本包范围内。本包不接线生产路径，真实 Provider 接入归 I186-R5。
package failureclass
