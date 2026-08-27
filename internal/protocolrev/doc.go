// Package protocolrev 关闭审计发现 I186-ARCH-PROTOCOL-REVISION-MIGRATION：
// `acp → acp/v1` 等协议枚举升级不得重写或重新解释历史 snapshot/digest；
// 只能以新增 supersede 事实落地，unversioned 历史值不能满足 pinned
// revision admission。
//
// 现状（发现时测得）：既有 supersede 路径
// （internal/provider/capability.go）要求 protocolVersion 完全同一，
// 因此协议 bump 没有合法迁移路径；同时 unversioned 自由文本 protocol
// 值仍能通过匹配。本包在收敛域冻结迁移契约，三条冻结如下：
//
//  1. 历史不重写：HistoryGuard 以 digest 为身份记录已冻结的 snapshot；
//     AssertMigrationPreserves 要求 FromDigest 已被冻结
//     （ErrUnknownHistory——不能迁移从未冻结的历史），且 ToDigest 尚未
//     冻结（ErrDigestCollision——目标必须是真正的新事实）。校验成功
//     不改变任何既有记录，ToDigest 由调用方随后经 Record 显式冻结。
//  2. 只 Supersede 落地：SupersedeMigration 是协议 bump 的唯一合法
//     形态——FromDigest ≠ ToDigest（ErrSameDigest，迁移绝不原地重写
//     历史）、两侧同 Family（ErrFamilyChanged，协议族变更不是
//     revision migration）、ToRevision 必须 versioned 且 ≠
//     FromRevision（ErrNotAMigration，否则只是普通 supersede）、必须
//     携带迁移授权记录的 ProvenanceDigest；MigrationDigest 由
//     canonical 派生，Validate 重算复核（ErrMigrationDigestMismatch）。
//  3. pinned 精确：PinnedRevision 构造时强制 versioned
//     （ErrMalformedPin）；AdmitPinned 要求 family 与 version 精确
//     相等——unversioned 出示值即使 family 相同也永远
//     ErrPinnedMismatch；比较是全函数，垃圾出示值走同一路径拒绝。
//
// 本包与测试只在收敛域内以纯确定性单测证明上述冻结（无时钟、无网络、
// 无真实进程）；既有 supersede 代码不修改，生产接线归 I186-R5
// strangler cutover。
package protocolrev
