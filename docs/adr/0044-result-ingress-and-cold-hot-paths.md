# ADR 0044：DRC-bound ResultIngress 与冷热双路径

- 状态：已接受（Accepted，2026-08-24）；接受证据：独立 reviewer 对 R0 产物审查 verdict=accept 且 P0/P1 清零（仅 P2/P3 finding，已随接受同批修复）；接受只冻结合同，未实现，不升级任何 milestone 状态
- 关联：ADR 0018（DRC/current-ledger recheck/quarantine）、ADR 0019（append-only 补偿）、ADR 0027（Candidate 一等记录）、ADR 0043、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)、[Issue #187](https://github.com/chiga0/marshal-harness/issues/187)、Planning Baseline v3（R1/R2）

## 背景

Issue #186 Final Review 指出（I186-P1-3）：当前外部结果接纳未全部收敛到统一门禁——Worker 结果由 `Adapter.Run` 返回边界直接转换为权威事实（ADR 0036 只对返回边界做 failure 归一化），没有独立的 untrusted-observation → admission 门禁。Planning Baseline v3 要求所有外部结果经 ResultIngress，并在 commit/dispatch/result 各 crash window 不丢事实、不重复推进。

## 决策

1. **ResultIngress 是唯一外部结果接纳路径**。WorkerResult、Candidate、Evidence ref、checkpoint/heartbeat、Assessment、Receipt 等一切来自 Control Plane 外部的结果对象，必须经 ResultIngress 校验后才能成为 authority ledger 事实；任何组件直接写 Store/ledger 接纳外部结果的路径在 R2 收口时删除或 hard-fail。
2. **接纳前必须 DRC-bound current-ledger recheck**，核验集合对齐 ADR 0018：actor/target、attempt、allocation、lease、generation、fencing、registration、snapshot、evidence、operation、digest、expiry、replay。DRC 沿用 ADR 0018 的 DispatchResultCapability 冻结定义：issuer 固定为 Core，sourceActor 为 dispatch-bound Execution workload，targetAudience 固定为 Core result-ingress，绑定 authorityNamespaceId、taskId/runId/attemptId/allocationId、leaseId、generation、fencingToken、封闭 operation 枚举、expiry/revocation 与 commandId/idempotencyKey/requestDigest/nonce 幂等字段，每次使用按当前 ledger 重判；本 ADR 不新增任何 DRC 字段，不引入第二种结果授权。
3. **失败分级与 quarantine**：
   - 合法 replay（digest/sequence 一致的重复投递）幂等返回既有接纳事实，不重复推进；
   - 伪造、已撤销、晚到（lease/generation 已过期或被替代）、digest 不符的结果一律 fail closed，进入 quarantine namespace 留档，附 typed 拒绝原因；quarantine 对象不进入业务推导，只供恢复与审计消费。
4. **冷热双路径**：
   - **热路径**：Attempt 在途的 checkpoint/heartbeat/progress 类高频小对象走 append-only 快速入账，只做最小 fencing/replay 校验，不承担完整 evidence 校验成本；
   - **冷路径**：WorkerResult/Candidate/Evidence ref 等终态或审计对象走完整 current-ledger recheck 与 digest 绑定后入账；
   - 两路共享同一 authority ledger 与 sequence 语义，热路径条目最终必须可被冷路径核对吸收；热路径不得产生冷路径无法解释的权威事实。
5. **崩溃语义**：ResultIngress 的接纳事务与 authority fact/outbox 提交保持原子；任一 crash window 恢复后，从 ledger 反推的结论只能是「已接纳（含幂等重放）」或「未接纳（可安全重新投递）」，不允许出现未知中间权威状态。
6. **恢复链对齐**：结果歧义按 Planning Baseline v3 统一恢复链处理：`ledger state → pending/ambiguous command → Provider Inspect/Reconcile → active binding + lease recheck → resume | fence | new Attempt`；ResultIngress 自身不做业务 retry/rework/terminal 决策（该决策权归 R4 单一恢复模型）。

## 实施顺序（对应 milestone）

- R1 只实现最薄 ResultIngress：接纳单一纵切的 Candidate/WorkerResult + DRC；
- R2 扩展到全部结果类型并覆盖完整 recheck 集合，删除 direct Store mutation 与 Gateway→Kernel 状态推进。

## 后果与门禁

- R2 Exit Gate 以 crash window 注入为证据：commit/dispatch/result 各窗口不丢事实、不重复推进；伪造/撤销/晚到结果 fail closed 的负向 fixture 必须失败。
- 本 ADR 不改变「Worker 不能自证」与 ReviewDecision 绑定精确证据摘要的不变量；ResultIngress 接纳只证明结果来源与授权合法，不证明结果内容正确——内容权威仍归独立 Verification/Review。
- 接受本 ADR 不升级任何 milestone 状态，不解除 M10–M13 暂停。
