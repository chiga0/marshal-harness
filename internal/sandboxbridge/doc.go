// Package sandboxbridge 承载 I186-R5 的生产接线桥（strangler cutover，
// ADR 0045）：把 legacy WorkerAdapter 的包在一条绑定 allocation/lease
// 身份的执行链中运行——Provision（allocation+generation 身份绑定）→
// Stage（冻结工单的 content-addressed 入账，消费前后重算 digest）→
// WorkerAdapter.Run（真实 agent 协议栈保持不变）→ Inspect → Terminate。
//
// 本包经 internal/execution 的 Input.WorkerRunner seam 接入生产路径；
// seam 为 nil 时行为与旧路径完全一致（legacy `Adapter.Run(host)` 降级为
// ADR 0043 决策 7 的 explicit local-nonproduction compatibility profile）。
//
// 边界（诚实口径）：
//   - 本桥不改变 Local MVP 的 assurance 语义：Local sandbox 的 allocation
//     是普通宿主进程记账（workspace-write assurance），不是 hardened
//     authority；生产 assurance 运行在本机依旧不可能（与既有状态一致）。
//   - remote provider（Cloudflare 等）应消费 Stage 的 content-addressed
//     输入在远端执行；Local profile 下 adapter 仍读取宿主 controlRoot
//     文件（同一台机器，执行等价）。
//   - 桥不做业务裁决：WorkerResult 的 schema 校验、身份校验、typed-edge
//     recheck、持久化与下游 verification/review/publication 语义全部由
//     execution 既有链路承担，本包只负责执行链身份绑定与资源生命周期。
package sandboxbridge
