// Package agentruntime 承载 ADR 0043 的 AgentRuntime 最薄纵切：
// immutable AgentLaunchSpec（Stage 前生成、绑定 profile digest，Sandbox 只执行
// 收到的 spec，不改写、不推断 argv/environment）、DecodeEvent → FinalizeResult
// 协议归一化（产出 untrusted workload result，权威接纳归 ADR 0044 ResultIngress），
// 以及 legacy WorkerAdapter（Probe/Run）的兼容映射 Probe → PrepareLaunch →
// DecodeEvent → FinalizeResult——映射只保留 migration provenance，不形成
// production AgentProvider 声明。R1 walking skeleton 阶段以 Fake Agent 打通链路。
//
// 骨架由维护者先行落地，用于锚定 I186-R1-B TaskSpec 的 deliverable pathGlob
// 父目录（plan premortem qoder-deliverable-parent 门禁要求父目录在锁定 baseRef
// 中已存在）；实现与表驱动测试由 Marshal Task 交付。
package agentruntime
