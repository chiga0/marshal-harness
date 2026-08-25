// Package resultingress 承载 ADR 0044 的 DRC-bound ResultIngress 最薄纵切：
// 外部结果对象（本 R1 阶段仅单一纵切的 Candidate/WorkerResult）必须经
// ResultIngress 校验后才能成为 authority ledger 事实——接纳前做 DRC-bound
// current-ledger recheck（actor/target、attempt、allocation、lease、
// generation、fencing、digest、replay 子集），合法 replay 幂等返回既有接纳
// 事实，伪造/已撤销/晚到/digest 不符一律 fail closed 并进入 quarantine
// namespace 留档（附 typed 拒绝原因，不参与业务推导）。ResultIngress 接纳
// 只证明结果来源与授权合法，不证明结果内容正确（内容权威归独立
// Verification/Review），也不做业务 retry/rework/terminal 决策（归 R4
// 单一恢复模型）。R1 walking skeleton 阶段以 Fake ledger/DRC 打通链路。
//
// 骨架由维护者先行落地，用于锚定 I186-R1-D TaskSpec 的 deliverable pathGlob
// 父目录（plan premortem qoder-deliverable-parent 门禁要求父目录在锁定
// baseRef 中已存在）；实现与表驱动测试由 Marshal Task 交付。
package resultingress
