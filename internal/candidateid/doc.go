// Package candidateid 承载审计 finding I186-ARCH-CANDIDATE-IDENTITY 的
// 收敛域切片：Candidate 独立身份合同。
//
// 背景：internal/domain.Candidate 已携带独立 record digest
// （CandidateDigest），但所有权威引用路径（store index、review
// packet、spine DRC）仍经 attemptId 派生身份。若不加约束，这一现状
// 会把 Attempt→Candidate 1:1 固化为未来的破坏性约束。本包在收敛域
// 冻结 Candidate 作为一等身份的合同，证明该 1:1 关系不会被固化。
//
// 冻结的三条合同：
//
//  1. Candidate 独立身份：CandidateIdentity 的 CandidateID
//     （candidate:<64-hex>）与 IdentityDigest 仅由 ContentDigest +
//     RecordDigest canonical 派生。OriginAttemptID 只作 provenance，
//     不进入 identity：content+record digest 相同而 OriginAttemptID
//     不同的两条身份记录是同一个 Candidate。
//  2. 证据绑定身份而非 Attempt：BindEvidence 把证据绑定到已注册的
//     CandidateID；证据不得先行绑定未冻结身份，证据换绑（同一
//     evidenceDigest 改绑另一 CandidateID）永不允许。
//  3. 单向兼容迁移：MigrateLegacyReference 把 legacy
//     task/run/attempt 三元组引用迁入身份账本。三元组只作 provenance
//     （LegacyProvenance 条目不进入 identity）；两个不同 Attempt 携带
//     相同 content+record digest 时收敛到同一 CandidateID——这就是
//     「Attempt→Candidate 1:1 不固化」的核心证明。
//
// 明确排除：本包不启用多 Candidate fan-out；多 Candidate fan-out
// 需要另行 ADR。
//
// 接线边界：生产引用路径（store index、review packet、spine DRC）
// 的 re-pointing 归 I186-R5，不在本包。本包与测试只在收敛域内以纯
// 确定性单测证明合同（无时钟、无网络、无文件系统）。
package candidateid
