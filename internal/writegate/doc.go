// Package writegate 承载 I186-R2 的权威写路径收口切片（ADR 0044 R2 收口、
// Planning Baseline v3 R2 步骤 6、Issue #189 R2-D）：权威状态（fact 账本、
// ledger sequence、dispatch 标记、结果接纳记录）的唯一写路径必须是
// durable command 派生 → 原子 outbox 提交 → 凭证化应用；任何绕过 outbox
// 提交凭证（Receipt/Proof）的 direct Store mutation 或 transport 层
// （CLI/API/Supervisor/Provider 语义位置）直接推进权威状态的企图，一律
// hard-fail（typed sentinel + 封闭拒绝原因），状态保持不变；合法重放幂等，
// 不重复推进、不丢事实。
//
// 本包只证明收敛域内的写边界封闭性（纯确定性单测），不接线生产路径；
// 生产侧旧路径的 strangler cutover 与 host bypass 删除归 I186-R5。
//
// 骨架由维护者先行落地，用于锚定 I186-R2-D TaskSpec 的 deliverable
// pathGlob 父目录（plan premortem qoder-deliverable-parent 门禁要求父目录
// 在锁定 baseRef 中已存在）；实现与表驱动测试由 Marshal Task 交付。
package writegate
