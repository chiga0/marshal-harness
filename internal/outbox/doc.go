// Package outbox 承载 I186-R2 的原子 outbox 最薄纵切（ADR 0044 决策 5、
// Planning Baseline v3 R2）：把 durable command 派生与 authority fact/outbox
// 提交收敛为单一原子事务，并在 commit/dispatch/result 三个 crash window
// 提供确定性注入点——任一窗口崩溃恢复后，从账本反推的结论只能是
// 「已提交（含幂等重放）」或「未提交（可安全重新投递）」，不允许出现
// 未知中间权威状态；不丢事实、不重复推进。
//
// 骨架由维护者先行落地，用于锚定 I186-R2-C TaskSpec 的 deliverable
// pathGlob 父目录（plan premortem qoder-deliverable-parent 门禁要求父目录
// 在锁定 baseRef 中已存在）；实现与表驱动测试由 Marshal Task 交付。
package outbox
