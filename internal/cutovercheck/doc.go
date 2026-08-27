// Package cutovercheck 承载 I186-R5-D 的 cutover 判定 harness：以 R0
// golden business trace（run-m10-wire-r1 真实事件链）为 old 侧，投影
// 等价 new 侧 trace，经 internal/cutovereq 的三分判据做 cutover 等价性
// 判定；并承载 rollback 演练（gate 方向回拨不复活旧 lease/registration、
// 不产生第二业务事实、无状态迁移）与 Local MVP 零回退证据锚点。
//
// golden fixture 的业务事实取自 .marshal/runs/run-m10-wire-r1/events.jsonl
// 与 docs/research/i186-r0-golden-trace.md §2 的冻结表；digest 在 fixture
// 内由内容确定性派生（形态合法、双侧一致），因为文档与公开表只携带
// digest 前缀。被判定的是 old/new 等价性判据本身，不重演历史 Run。
//
// 本包只有测试与 fixture，不接线生产路径；cutover 默认翻向（新路径默认）
// 的执行决策与真实 Agent canary 归 R5 收口治理与 R6 conformance。
package cutovercheck
