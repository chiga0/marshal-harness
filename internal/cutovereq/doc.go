// Package cutovereq 关闭审计 finding I186-ARCH-CUTOVER-EQUIVALENCE：ADR 0045
// 要求 old/new 全 digest 逐字段相等，这对真实非确定 Agent 不可满足。本包按
// 冻结文档 docs/research/i186-r0-golden-trace.md §3/§4 的 normalized trace
// schema 与对比规则，把等价判定拆成三路独立、确定性、fail-closed 的比较器：
//
//  1. authority-trace invariants（真实 Agent 必须满足的权威面）：
//     taskId/runId/attemptId/sequence/command.kind 与 digests 必须逐字段
//     相等，old 侧出现的 digest 必须原样出现在 new 侧且相等；违反即
//     business-mismatch 阻断。
//  2. deterministic Fake 的 content digest 全等
//     （CompareFakeDeterministic）：Digests map 全等 + 业务字段全等 +
//     与真实 Agent 相同的升级规则；任何非升级差异即 ErrFakeDrift。
//  3. 资源归一化不劣化统计（真实 Agent）：authority 计数
//     （attempt/gate/review）严格相等，内存与墙钟仅在 basis-point 容差内
//     不劣化；计数劣化即 ErrAuthorityRegression，统计劣化即
//     ErrResourceRegression。
//
// 升级集合是封闭的：只有 null（old 侧空）→非空（new 侧）的白名单字段
// （commandId/fencingToken/allocationId/sandboxProvider/drcId/drcBinding/
// agent.registrationId/agent.attestationDigest/sandbox.registrationId）记为
// authority-upgrade，逐条携带 Detail；升级值必须过形态校验（digest 形态、
// drcBinding 绑定本 step 的 attemptId 与 lease.generation），不满足即
// business-mismatch。白名单之外的任何差异（值改变、new 侧清空、
// origin/providerId/capabilityDigest/lease.generation 变化、new 侧多出的
// digest 键）一律 unexplained-drift 阻断 cutover。
//
// 不可覆盖性：TraceVerdict.Equivalent 只由 typed 比较派生——无
// business-mismatch 且无 unexplained-drift 才为真。本包 API 不存在任何
// ignore/override 开关，人工无法解释掉 authority diff。若 R5 需要接受
// generation 等当前判 drift 的字段，必须先修订本冻结（新增/替代 ADR 与
// normalized projection），而非人工放行。
//
// 收敛域边界：本包只冻结 normalized trace 的 schema、Validate 与三个
// 比较器语义，不实现 trace 投影采集、不接生产路径；R5 strangler cutover
// 的投影与接线不属于本切片。
package cutovereq
