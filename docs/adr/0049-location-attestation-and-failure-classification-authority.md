# ADR 0049：执行位置 attestation 分型与失败分类 authority（R3-E/R3-F，修订 ADR 0043 §5）

- 状态：已接受（Accepted，2026-08-27）；接受依据：维护者授权的 I186 快速收敛治理（Lead 直接冻结、停用独立 reviewer 轮转）。接受冻结本合同与 `internal/locationattest`、`internal/failureclass` 收敛域实现及负测；不把 Local/kernel held handle 采集、supervisor 观测接线（归 R5/R6）、恢复决策消费（归 R4）或 production assurance 声明提前为完成。
- 关联：[ADR 0017](0017-provider-neutral-sandbox-contract.md)（hardened 证据拓扑）、[ADR 0018](0018-control-plane-and-provider-ports.md)（失效处置分级、attestation 全链绑定）、[ADR 0043](0043-worker-executor-profile-and-dual-binding.md)（本 ADR 修订其决策 5）、[ADR 0044](0044-result-ingress-and-cold-hot-paths.md)、[ADR 0045](0045-strangler-cutover-and-single-recovery.md)（R4/R5 消费方）、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)、[Issue #191](https://github.com/chiga0/marshal-harness/issues/191) R3 Exit Gate、审计 finding `I186-ARCH-LOCATION-ATTESTATION`（P0）与 `I186-ARCH-RESOURCE-CLASSIFICATION-AUTHORITY`（P1）。

## 背景

ADR 0043 决策 5 把执行位置证据的产出职责写给 SandboxProvider（allocation/generation/PID/process-group/digest）。Issue #186 Final Review 确认这仍可能由被证明方自证（`I186-ARCH-LOCATION-ATTESTATION`，P0）：Provider 自报的位置声明若无故障域外 authority 事实支撑，不能支撑 production assurance/publication。

同一批复审指出 `I186-ARCH-RESOURCE-CLASSIFICATION-AUTHORITY`（P1）：`ResourceEnvelope.observedPeak`、termination reason 与 `infra-failure` 分类权未冻结；能够放宽 retry/预算或豁免 semantic rework 的分类必须来自 workload/Provider 故障域外 observation，Provider 声明只能诊断或收紧权限。

本 ADR 冻结两个合同的类型化边界；实现证据为收敛域纯确定性单测，不接线生产路径。

## 决策

### 1. 执行位置 attestation 分型（R3-E，修订 ADR 0043 决策 5）

1. **执行位置证据二分**：`LocationClaim`（provider-asserted location claim）与 `LocationFact`（authority-verified location fact）是两个不同类型、不同签发域的对象，不得互换、不得互相冒充。
2. **LocationClaim** 是 Sandbox Provider 自报的执行位置声明：`allocationId + generation + providerRegistrationId + handleHint + CanonicalDigest`。claim 只能用于诊断、审计叙事与收紧权限；handleHint 是叙事字段，不参与裁决。claim 的 digest 必须可按身份字段 canonical 重算，篡改即 fail closed。
3. **LocationFact** 是故障域外 authority observer 观测并签发的事实：`allocationId + generation + handleKind（封闭枚举 pid/process-group/cgroup/vm-handle/independent-attestation）+ handleDigest（kernel-held handle 观测记录 digest）+ observerID + canonical FactDigest`。handleDigest 必须来自故障域外的 held handle 观测（Local：kernel-held pid/process-group/cgroup/VM handle；远程或跨故障域：independent attestation）。
4. **被证明方不得自证**：`fact.observerID == claim.providerRegistrationID` 的 fact 一律排除、视同不存在。只有排除自证后仍存在与 claim 精确绑定 `(allocationId, generation)` 的独立 fact，该执行位置才可支撑 production assurance/publication。claim-only（含全部被自证排除的情形）永不支撑 production assurance。
5. **FactLedger** 是 fact 的 immutable put-if-absent 存储：注册身份元组为 `(allocationId, generation, handleKind, observerID)`；注册时重算 FactDigest；同元组同内容幂等，同元组不同内容 fail closed，绝不静默覆盖。
6. ADR 0043 决策 5 中「Sandbox 负责……执行位置证据（allocation/generation/PID/process-group/digest）」就地细化为：Sandbox Provider 产出 `LocationClaim`（决策 2 的输入）；支撑 production assurance 的执行位置事实只来自本 ADR 决策 3 的 authority observer。该修订不解除 ADR 0017 对 hardened ConformanceEvidence 的要求；location fact 与 ConformanceEvidence 互补、不互相替代。

### 2. 失败分类 authority（R3-F）

1. **ResourceEnvelope** 冻结 infra-failure 分类输入面：`observedPeakBytes（>=0）+ termination（封闭枚举 termination:completed/exit-nonzero/oom-killed/time-limit/signal-killed/io-error/network-unreachable/unknown）+ source（封闭枚举 authority-observed/provider-asserted）+ observationDigest（sha256:<64-hex>，带外观察记录 digest）`。任一字段违例 fail closed；malformed digest 是硬错误，绝不静默降级为更保守分类。
2. **分类权方向性（本合同核心）**：只有 `source=authority-observed` 且 envelope 校验通过的 infra 候选终止原因（oom-killed/time-limit/signal-killed/io-error/network-unreachable）可分类为 `failure:infra`，并携带 `MayRelaxBudget=true`、`MayExemptSemanticRework=true`。`provider-asserted` 的同一终止原因只能分类为 `failure:provider-claimed-infra`（诊断/收紧用），放宽标志恒 false——Provider 声明只能诊断或收紧权限，永不放宽 retry/预算，永不豁免 semantic rework。
3. **抗拒洗白**：`termination:exit-nonzero` 恒分类 `failure:semantic`（authority 观察也不得把 semantic failure 升级为 infra）；`termination:completed` 恒 `failure:completed`；`termination:unknown` 恒按最保守的 `failure:semantic` 处理、放宽标志恒 false（本条为冻结规则，不得逐案重新解释）。
4. **决策绑定**：分类结论回显 `observationDigest`，任何放宽 retry/预算或豁免 rework 的下游决策必须能指回该带外观察记录。
5. **消费边界**：本合同只冻结分类 authority；放宽 retry/预算、豁免 semantic rework、失败归因驱动的恢复决策由 R4 单一恢复模型（ADR 0045 决策 2 指向）消费，本 ADR 不提前冻结恢复决策语义。

## 后果与门禁

- 收敛域实现与负测：`internal/locationattest`（claim/fact 类型、FactLedger、Verifier；digest 篡改、自证排除、跨 allocation/generation 挪用、伪造 claim、身份元组冲突不覆盖原 fact 负测）与 `internal/failureclass`（决策表 8 终止原因 × 2 来源共 16 组合、伪造 infra-failure 放宽恒 false、semantic 抗拒洗白、digest echo、envelope 全字段 fail closed）。两包纯新增、fail closed、无时钟/无真实进程/无网络、不接线生产路径。
- `I186-ARCH-LOCATION-ATTESTATION` 的合同层面由决策 1 关闭；`I186-ARCH-RESOURCE-CLASSIFICATION-AUTHORITY` 的合同层面由决策 2 冻结（R4 消费后关闭）。finding 状态以 `docs/audit-report.md` 登记为准。
- Local kernel held handle 采集、supervisor 观测与 ResultIngress/发布门禁的接线归 I186-R5 strangler cutover；location fact 对 production assurance 的实际消费纳入 R5/R6 conformance，不从本 ADR 的存在推断。
- Local MVP 零回退：本 ADR 不改变既有 ordinary-user Adapter 路径行为，M0–M6 回归基线不受影响。
- 本 ADR 不授权任何 production assurance/publication 声明，不升级 M10–M13 或 v1.0 状态。
