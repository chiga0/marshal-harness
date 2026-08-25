// Package command 承载 ApplicationCommand → durable command 的最薄适配层：
// 把外部应用命令投影为 Core DurableExecutionEngine 的派生命令（commandId、
// requestDigest、expectedSequence 语义），幂等委托 engine.DeriveCommand，
// 全部畸形输入 fail closed。本包不维护第二命令账本，不接线生产路径。
//
// 骨架由维护者先行落地，用于锚定 I186-R1-A successor TaskSpec 的 deliverable
// pathGlob 父目录（plan premortem qoder-deliverable-parent 门禁要求父目录在
// 锁定 baseRef 中已存在）；实现与表驱动测试由 Marshal Task 交付。
package command
