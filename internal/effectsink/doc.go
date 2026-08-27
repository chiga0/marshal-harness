// Package effectsink 承载审计发现 I186-ARCH-EFFECT-SINK-FENCING 的
// 收敛域切片。
//
// 背景：ResultIngress recheck 只能保护 ledger，不能撤销已经发生的外部
// 效果。SCM、Artifact、Secret 与其它 effect sink 一旦执行，外部世界
// 不可回滚；因此每一个 sink 在 mutation/secret use 之前必须独立执行
// current generation、fencing、authorization 与 target recheck，并覆盖
// revoke→effect 竞态。该独立 recheck 不得顺延、不得只信意图自述。
//
// 本包在收敛域内冻结契约：任何外部效果发生之前，必须经
// VerifyBeforeEffect 对签发时的 EffectIntent 做 pre-mutation 独立
// recheck。评估顺序冻结，首个失败即返回：
//
//  1. intent.Validate()——意图完整性：IntentDigest 对全部身份字段
//     canonical 重算，任一字节被改即 ErrIntentTampered（结构性，走
//     error 通道）；
//  2. view.Validate()——effect 时点当前账本事实的形态校验
//     （ErrMalformedView，结构性，走 error 通道）；
//  3. view.AuthorizationRevoked → authorization-revoked——revoke→effect
//     竞态的核心防线：意图在授权撤销后到达，永不执行；
//  4. view.CurrentGeneration != intent.Generation → generation-stale；
//  5. view.CurrentFencingToken != intent.FencingToken → fencing-mismatch；
//  6. view.CurrentAuthorizationDigest != intent.AuthorizationDigest →
//     authorization-superseded（授权已被新记录取代）；
//  7. view.CurrentTargetDigest != intent.TargetDigest → target-drifted
//     （目标状态自授权时点以来已漂移）；
//  8. 全部通过 → Verdict{OK:true}。
//
// 步骤 3–7 一律针对 view 判定，绝不只信意图自述。返回形状纪律：结构性
// 问题（意图篡改/畸形、view 形态非法）只走 error 通道且不产出 Verdict；
// 生命周期拒绝只经 Verdict.Reason 表达且 error 为 nil，两通道互不泄漏。
// RejectionReason 是封闭枚举，取值只由本包常量构造，未知值永不出现。
//
// 幂等防重：EffectLedger 按 IdempotencyKey put-if-absent 记录已执行效果。
// 同 key 同 IntentDigest 重放幂等成功（不产生第二次外部效果）；同 key
// 不同 IntentDigest 即 ErrEffectConflict，fail closed——账本永不覆盖、
// 永不双执行。ExecuteIfAdmitted 是收敛证明的组合门禁：先
// VerifyBeforeEffect，仅在 OK 时查账本——重放同摘要在 Verdict 上置
// AlreadyExecuted 幂等返回；冲突即 error；随后才 MarkExecuted。注意冻结
// 语义：VerifyBeforeEffect 先于账本重放判定，因此授权一旦撤销，即使
// 重放已执行过的意图同样被拒绝——revoke 优先于幂等重放，防止
// revocation 竞态产生双重效果。
//
// 边界：本包只冻结契约，并以纯确定性单测证明语义（无时钟、无 IO）；
// production wiring（各 sink 实现 SCM/Artifact/Secret 前强制经过
// ExecuteIfAdmitted、view 由当前账本组装）归 I186-R5 strangler cutover，
// 不在本包范围。
package effectsink
