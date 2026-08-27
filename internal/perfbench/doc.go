// Package perfbench 承载 I186-R6 退出门禁要求的 SLO/性能基线 harness。
//
// 目的：R6（conformance/replan）要求 v1.0 之前冻结一份可重复的 SLO/增长
// 基线。本包对 convergence 期落地的五条热路径各测单调用延迟，并以
// 门禁测试（TestBaselineConformance）对照冻结阈值断言，防止回归：
//
//   - bindingcheck.Checker.Recheck（每次 admission 的双 binding recheck）；
//   - attemptgate.Gate.AdmitAttemptResult（逐 attempt 结果接纳门禁）；
//   - jitgate.VerifyBeforeProvision（provision 前 admission 重验）；
//   - resultingress.Ingress.Admit（cold worker-result 结果接纳，含
//     eligibility recheck）；
//   - effectsink.ExecuteIfAdmitted（pre-mutation 门禁 + 账本组合）。
//
// 确定性边界：被测对象是内存内确定性域调用——夹具全部内存构造，无
// I/O、无进程、时钟一律注入定值。延迟采样（time.Now 对单次调用掐表）
// 是本包唯一的墙钟读取，仅用于测量、不参与判定语义。网络化 sink
// （真实 SCM/artifact/secret 通道）的 wall-clock SLO 显式不在本包范围：
// 那些路径含不可控外部依赖，其 SLO 须在 R6 之后以独立 soak 设施定义，
// 不得借本包混入门禁。
//
// 阈值出处：DefaultThresholds 五档一律冻结为 p99 ≤ 5000 微秒（5ms）。
// 这是对内存内实现刻意宽松的上限——量级余量用于吸收 CI 机器抖动，而非
// 逼近实测值；实测 p99 预期远低于阈值（微秒级）。该基线标记为 v1.0 临时
// 值，待 R6-C soak 校准后可收紧，收紧本身不改变接口形状（改数值即
// ADR 无关的常态变更，但须在提交说明中记录校准依据）。
//
// 测量方法（冻结）：
//
//   - 每条路径一个 Go Benchmark（flat b.N 循环，不用 RunParallel），夹具
//     在 b.ResetTimer() 之前构造完毕；基准仅提供 ns/op 观测；
//   - 门禁走普通 Test：同一批单调用闭包各采样 N=200 次，经 EstimateP99
//     （最近秩法，索引 ceil(0.99*n)-1）得 p99 微秒，再由 CheckThresholds
//     逐条对照阈值，超限即精确报告 Got/Want。刻意不依赖 test(-1)bench
//     驱动，保证 CI 执行面与判定路径确定；
//   - 会改变内部状态的调用（Ingress.Admit、ExecuteIfAdmitted）使用
//     poolSize=512 的预构造输入池（按幂等键区分），门禁测试 N=200 全程
//     落在首次接纳路径；基准回绕后进入幂等重放稳态，该稳态仍完整执行
//     全部 recheck 判定，仅跳过账本写入。
package perfbench
