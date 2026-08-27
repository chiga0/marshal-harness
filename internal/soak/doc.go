// Package soak 承载 I186-R6 的 soak 形态：确定性 accelerated soak（可重复、
// 可在常规测试预算内完成大量迭代）与 wall-clock soak 的统一 harness。
//
// 两级 soak：
//
//  1. 原语 soak（本包 InvariantSoak）：对恢复决策（recovery.Decide）、双
//     binding 门禁（attemptgate）与 effect sink 门禁（effectsink）做
//     seeded 伪随机故障矩阵迭代；每次迭代断言 invariant（唯一幂等结论、
//     无第二效果、无 orphan allocation、无解释不了的拒绝）。纯内存、
//     可任意放大迭代数。
//  2. 路径 soak（execution 层，归 R6 执行）：有限次完整 execution.Run
//     bridged 循环，断言每次 Run 收敛、无第二业务事实、无残留写锁。
//
// 边界（诚实口径）：accelerated soak 是合入门禁；24h wall-clock soak
// 由外部运行器（cron/CI）以本 harness 执行并回收证据，本包不把
// wall-clock soak 伪造成完成。种子固定使失败可重放。
package soak
