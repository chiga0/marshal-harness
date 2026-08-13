# ADR 0027：Candidate 一等不可变记录与 Verification 写作用域

- 状态：提案（Proposed，待维护者接受）
- 日期：2026-08-13
- 决策来源：公开 [Issue #63](https://github.com/chiga0/marshal-harness/issues/63)（D-1，设计审计轮次 [#71](https://github.com/chiga0/marshal-harness/issues/71)）定位到 Verification 阶段写入被验证对象且缺少 Candidate 持久身份；维护者选定方案 A（允许声明式确定性归一化，但必须产出新 Candidate）。量级依据见效率审计 [#73](https://github.com/chiga0/marshal-harness/issues/73) 与 [#76](https://github.com/chiga0/marshal-harness/issues/76)
- 关联：[ADR 0002](0002-worktree-isolation.md)、[ADR 0004](0004-independent-verification.md)、[ADR 0013](0013-graded-permission-denials.md)、[ADR 0017](0017-provider-neutral-sandbox-contract.md)、[ADR 0018](0018-control-plane-and-provider-ports.md)、[ADR 0019](0019-deterministic-control-plane-typed-execution-and-goal-admission.md)、[验证与评审](../verification-and-review.md)

## 背景

ADR 0019 §2/§3 已把 `Candidate` 列为 Implement 类 typed execution 的输出，并要求它「绑定 task/run/attempt/allocation/generation、base 与内容 digest」。但 `Candidate` 从未被定义为持久记录：既无 Schema，无 `domain.Kind`，也无 Go 类型。

今天 Evidence 实际绑定的是 `SnapshotDigest` + `DiffDigest` + `BaseSHA`（`internal/review/packet.go:60-98`）。这描述的是「某时刻 worktree 长什么样」，不是「Worker 交付了什么」。缺少这层身份直接造成三个后果：

1. **Verifier 写入被验证对象且原始产物不可追溯。** 为消除 [Issue #55](https://github.com/chiga0/marshal-harness/issues/55)（Worker 无 shell 权限、跑不了 `gofmt`，format 门禁首轮必红）的返工浪费，提交 `643bd20` 让 Verification 阶段执行 `gofmt -w` 就地改写被验证 worktree（`internal/verification/formatnormalize.go:93`），随后重新观察并**覆盖同一 artifact 的 digest**（`internal/verification/formatnormalize.go:162-165`）与同路径 patch 字节（`internal/verification/verifier.go:91`）。Worker 原始产物的字节与 digest 因此不复存在。该变更未经 ADR，`docs/verification-and-review.md` 亦未记载。
2. **ADR 0019 §6 的 `EvidenceDependencySet.subjectDigest` 没有 subject 可指。**
3. **M13 的下游证据失效派生缺少依赖图节点身份。**

三者同源：系统缺少一个可以承载「Worker 交付的那一份不可变字节」的名字。因此本 ADR 不把问题 1 当作单点缺陷修复，而是补上缺失的抽象，使该类问题在契约层不可表达。

关键事实：归一化本身有真实价值。效率审计实测 213 个 Run 累计 **92 轮 rework**（每轮 ≈ 19 分钟），其中确定性首轮红是可完全消除的一类；`REJECTED` 记录中存在「14 个 Gate 中 12 pass、2 fail；唯一剩余阻塞是 `runtime_identity.go` 的四行未达到 gofmt 结果」——一个完整 Run 因四行格式被否决。所以缺的不是「禁止归一化」，而是**让归一化成为声明式、可审计、不销毁前序事实的独立步骤**。

## 决策

### 1. `Candidate` 是一等不可变权威记录

新增权威记录 `Candidate`（`apiVersion=marshal.dev/v1alpha1`，`kind=Candidate`）。它是 authority ledger 事实，按 ADR 0018 §10 归属 `authorityNamespaceId`，只允许 Core 写入，append-only，永不原地改写或删除。

至少绑定：

- `taskId`、`runId`、`attemptId`；
- `baseSha`——冻结基线 commit；
- `contentDigest`——候选内容 canonical 字节的 sha256（当前实现为 observed patch 字节）；
- `producerKind`——封闭枚举 `worker | normalizer`；
- `producer`——`adapterId` + `adapterVersion`（`normalizer` 时为归一化工具标识与版本）；
- `predecessorCandidateDigest`——`producerKind=normalizer` 时必填，指向被归一化的前序 Candidate；`producerKind=worker` 时必须缺省；
- `createdAt`。

远程执行启用后按 ADR 0019 §3 追加 `allocationId` 与 `generation` 绑定；本 ADR 不提前引入 embedded/local 尚不存在的 allocation 身份。

`contentDigest` 是内容寻址身份：相同 `(baseSha, contentDigest)` 即同一 Candidate。Candidate 字节按 ADR 0018 §13 使用 digest-verified put-if-absent 写入，冲突字节只进 quarantine namespace（见 [Issue #70](https://github.com/chiga0/marshal-harness/issues/70)），永不覆盖。

### 2. Verification 允许声明式确定性归一化，且必须产出新 Candidate

Verification 阶段可以执行归一化并写入 task worktree，当且仅当同时满足：

1. **声明**：该归一化器在冻结 `PolicySnapshot` 中显式声明，含工具标识、版本与允许作用的路径集合；未声明的写入一律 fail closed；
2. **确定性**：同输入必须产生同输出，且不改变程序语义；
3. **有界**：作用范围不得超出声明的路径集合，且不得超出该 Attempt 的 diff 作用域。

满足条件的归一化**产出一个新的 Candidate**（`producerKind=normalizer`），并绑定 `predecessorCandidateDigest` 指向 Worker 的 Candidate。两条事实都必须留档：

- Worker 原始 Candidate 的字节与 `contentDigest` 作为独立 artifact 永久保留，**不得被同路径覆盖或原地替换 digest**；
- 归一化产生的 diff 作为独立证据 artifact 记录；
- `VerificationReport` 同时记录 `candidateDigest`（被验证的主体）与 `sourceCandidateDigest`（其前序），使 lineage 可从 Evidence 一路回溯到 Worker 产物。

Evidence 的 subject 是**归一化后**的 Candidate——这与 ADR 0004 不冲突：Worker 仍未对自己的工作提供权威证据，归一化是 Control Plane 授权的确定性工具步骤，不是 Worker 的自我声明。

归一化不转移正确性责任。若归一化器需要的改动超出声明作用域，或归一化后强制 gate 仍失败，那是 rework，不是归一化；不得以「已归一化」为由放宽任何强制 gate。

### 3. 写作用域必须声明，并由 Core 机械断言

每个 verification 阶段声明自身写作用域：`none`（默认）或声明的归一化路径集合。

Verification 结束时，Core 重新观察 worktree 并断言：**实际变更集合 ⊆ 已声明作用域**。任何未声明写入 fail closed，追加可审计记录，不产出 Evidence。

该断言不依赖 Verifier 自报，成本约等于一次 `ObserveContext`（已有实现）。默认作用域为 `none` 意味着：未来任何 gate 若开始写入，会在门禁期失败，而不是靠代码评审察觉。

### 4. Gate 的 `required` 是定义属性

Gate 的 `required` 由冻结的 gate 定义（`PolicySnapshot` / 冻结 spec）决定，**不得随运行结果变化**。

当前 `format:normalize` 在成功路径为 `required=false`（`internal/verification/formatnormalize.go:142`）、在失败路径为 `required=true`（`internal/verification/verifier.go:80,86`），使 gate 集合随结果漂移；而 `evidenceDigest` 是对 gate 的复合（`internal/review/packet.go:93-98`），因此该漂移会污染证据身份。本 ADR 要求 gate 的 `required` 在定义处固定。

### 5. 不放宽的部分

- Worker 不能为自己的工作提供权威验证证据；
- 一个 task worktree 同时最多一个写入者——归一化发生在 Worker Attempt 结束、Verification 持有排他 lease 期间，不引入并发写入者；
- Evidence bytes 与历史事实永不原地改写或删除；
- 强制 gate 的失败不能被 assessment、归一化或人工措辞改写为通过；
- `ReviewDecision` 仍精确绑定当前 Evidence digest。

## 保留的不变量

ADR 0002/0004/0017/0018/0019 与仓库治理中的全部不变量继续有效，尤其是：Core/`authorityNamespaceId` 唯一写权威 ledger；Worker 不自证；Worker/Verifier 不同 principal 与 allocation（远程执行启用后）；单 worktree/Attempt 单写者；ReviewDecision 精确绑定当前 Evidence；强制失败 gate 不可被覆盖；Evidence 与历史事实永不原地改写；陈旧/冲突字节只进 quarantine；普通宿主进程不称 hardened；失败/阻塞保留 Outcome；`.marshal/` 不进入业务提交。

## 后果

- 需要新增 `schemas/candidate.schema.json`、`domain.KindCandidate` 与 `contract.Descriptor` 条目及 happy-path/invalid fixture（与 [Issue #65](https://github.com/chiga0/marshal-harness/issues/65) 的 catalog 补齐一并处理）；
- `VerificationReport` 与 `ArtifactManifest` 需增加 `candidateDigest` / `sourceCandidateDigest` 字段；对历史记录为可选，**不改写既有 Run 数据**；
- 本 ADR 的接受只冻结契约，不实现代码，也**不升级 M8–M13 的实现/conformance 状态**；实现属 M8 之外的独立工作项，不改变 ADR 0018 §7 的 M8 gate 顺序；
- Issue #55 的收益保留（format 首轮不再必红），Issue #63 在实现合入后关闭；
- 本 ADR 为 ADR 0019 §6 的 `EvidenceDependencySet.subjectDigest` 与 M13 的下游失效派生提供了所需的节点身份，是二者的实现前置。

## 备选（已否决）

- **方案 B：Verifier 恢复严格只读，`gofmt` 移回 Worker 侧。** 否决。Worker 无 shell 权限（`docs/implementation-plan.md:186` 教训 (3)），且实测 92 轮 rework 中确定性首轮红是可完全消除的一类（含一个 Run 因四行未 gofmt 被整体否决）。恢复只读会回退这部分收益，而方案 A 在保留收益的同时通过 Candidate lineage 与写作用域断言恢复独立性。若日后 Worker 侧能可靠自跑格式化，可另起 ADR 收回归一化授权。
- **保持现状（就地改写并覆盖同一 artifact 的 digest）。** 否决。Worker 产物不可追溯，且使 ADR 0019 §3/§6 与 M13 无法实现。
- **把 `Candidate` 只作为内部 Go 类型、不落权威记录。** 否决。ADR 0019 §6 要求 `EvidenceDependencySet` 绑定 `subjectDigest`，需要持久且可被 ledger 引用的身份。
- **归一化后沿用同一 Candidate 身份（覆盖内容 digest）。** 否决。违反内容寻址与不可变原则，且会让「验证的是哪一份字节」不可判定。
- **允许任意 Verifier 写入，仅靠代码评审约束。** 否决。`643bd20` 已证明这条约束在实践中失效——一次未经 ADR 的信任边界变更通过了评审并进入 main。
