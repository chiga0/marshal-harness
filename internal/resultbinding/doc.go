// Package resultbinding 承载 ADR 0052 §1.4 的生产接入：每个 Attempt 的
// Agent/Sandbox 双 binding current-ledger recheck 与真实结果的
// ResultIngress 接纳（命令式主张以下全部按冻结事实种子重建）。
//
// 组成（全部为确定性、无随机源）：
//
//   - AgentSide：从冻结 CapabilitySnapshot digest 与 adapter 身份种子重建
//     agentregistry（ProviderType=agent、ProtocolVersion=marshal-worker/v1alpha1、
//     SnapshotState=active、证据集合=自身 capability digest——Local
//     ordinary-user 的 admission evidence 即当前冻结能力快照）；capability
//     facts 是 Marshal scaffold 阶段冻结的持久身份。
//   - SandboxSide：以 provision receipt + admission 时刻 Inspect 读回的
//     live allocation state 种子 bindingcheck SandboxLedger——live 检查让
//     accepted result 只在 allocation 仍 active 且 generation 未漂移时接纳。
//   - WorkerRuntimeProfile（runtimeprofile）：AgentBinding+SandboxBinding+
//     compatibility digest（三方 canonical 合成），绑定 AttemptProfileStore
//     的 put-if-absent 契约。
//   - 双侧 recheck：attemptgate.Gate——agent registration/snapshot 与
//     allocation 的 current state 全绿才继续；缺失一侧 fail closed。
//   - ResultIngress：以 DRC binding（lease=facts 的 allocation/generation/
//     fencingToken、registration/snapshot/evidence digest）向 resultingress
//     接纳真实 WorkerResult bytes，获得 typed AdmissionFact（replay 幂等、
//     伪造/陈旧进 quarantine）。
//
// 边界：Local ordinary-user 语义——双 binding 的 authority 依据是冻结的
// CapabilitySnapshot 与当前 allocation ledger 回放，不宣称 hardened
// authority、不宣称远端 Provider ConformanceEvidence、不宣称 production
// assurance（云侧 hardened 归 ADR 0038 与后续 milestone）。
package resultbinding
