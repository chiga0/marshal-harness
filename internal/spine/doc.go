// Package spine 承载 I186-R1 walking skeleton 的端到端脊柱纵切：把一条
// 任务从 ApplicationCommand（internal/command durable command 投影）经
// AgentLaunchSpec → WorkerExecutor（internal/agentruntime，FakeProvider +
// FakeAgent 驱动 Sandbox Stage→Exec→Inspect→Terminate）产出的 untrusted
// WorkloadResult 组装为 Candidate/WorkerResult 投递信封与 DRC
// （internal/resultingress，绑定 Sandbox allocation 的 allocationId/
// generation），最终经 ResultIngress.Admit 完成 DRC-bound current-ledger
// recheck 接纳。Candidate 必须可机械证明来自绑定的 Sandbox allocation；
// 本包只做确定性链路打通与证据绑定，不接线生产路径，内容正确性权威仍归
// 独立 Verification/Review。
//
// 骨架由维护者先行落地，用于锚定 I186-R1-E TaskSpec 的 deliverable pathGlob
// 父目录（plan premortem qoder-deliverable-parent 门禁要求父目录在锁定
// baseRef 中已存在）；实现与表驱动测试由 Marshal Task 交付。
package spine
