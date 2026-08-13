# ADR 0028：CI Deadline 分阶段观察与可信完成时间裁决（phased observation）

- 状态：Proposed（本 ADR 经维护者 ApprovalRecord 接受后生效；接受人与接受时间另行记录，不改写本文）
- 日期：2026-08-13
- 决策来源：公开 [Issue #30](https://github.com/chiga0/marshal-harness/issues/30) 期望 5（为 CI deadline 观察机制设计新契约）；期望 1-3（deadline 后仍读远端事实、按可信完成时间裁决、超时 fail closed）依赖本契约
- 关联：ADR 0026（SCMMergeReceipt 与 PublicationReconcileRecord，Accepted）、ADR 0027（Candidate 一等不可变记录与归一化写域，Proposed，PR #82，见「与既有 ADR 的关系」节）

## 上下文

### 问题陈述：本地 runTimeoutSeconds 与 CI 实际完成时间的语义错位

现有 `ObserveChecks`（`internal/publication/checks.go`）在观察入口处先做本地 deadline 判定：以 `state.CreatedAt + budgets.runTimeoutSeconds` 为 deadline，`now` 达到或超过该 deadline 即立即把 Run 置为终态 `BLOCKED`（错误 sentinel `ci-deadline-exceeded`），且该判定先于一切远端观察——在 publication 权威校验、PublicationRecord frozen digest 对账与 observer 调用之前执行。

该实现存在三层语义错位：

1. **锚点错位**——deadline 锚定在 Run 创建时间（`CreatedAt`），而不是 publication 时间（`publishedAt`）。CI 只能在 Draft PR 发布之后才开始，Worker/Review/Publish 消耗的时长却全额计入 CI 等待窗口；
2. **预算混用**——`runTimeoutSeconds` 是 Run 总预算，同时覆盖 Worker 执行、Review、Publish 与远端 CI 等待。Worker 耗时越长，留给 CI 的窗口越短，两者互相侵蚀；
3. **顺序倒置**——本地 deadline 检查先于一切远端观察。即使 required checks 已在 deadline 前全部通过，只要 Marshal 的观察动作落在 deadline 之后，权威的远端事实根本无法进入裁决。

### BLOCKED 复现链（checks 在 deadline 前已通过仍 BLOCKED）

1. T0：Run 创建，`state.CreatedAt = T0`，状态 `CI_PENDING`；TaskSpec `budgets.runTimeoutSeconds = R`，本地 deadline `D = T0 + R`；
2. Tp：Worker/Review/Publish 共消耗 `Tp − T0`，Publisher 记录 `publication.completed` 并写入 PublicationRecord（`publishedAt = Tp`），远端 CI 开始执行 required checks；
3. Tc：远端 required checks 全部通过，provider 记录完成时间 `Tc`，且 `Tc < D`（**checks 在 deadline 前已通过**）；
4. To：Marshal 对 `ObserveChecks` 的下一次调度/重试发生在 `To ≥ D`（诱因：attempt 超时后重排、lease 竞争、调度延迟、运维重试、Worker 耗去大部分 `R` 等）；
5. `ObserveChecks` 入口处 `now = To ≥ D` → 立即 `block()`，终态 `BLOCKED`，原因 `ci-deadline-exceeded`；observer 从未被调用，远端「checks 已于 Tc 通过」的事实从未被读取；
6. 结果：一个 published head 已在 deadline 前通过全部 required checks 的 Run 被终态 `BLOCKED`。由于 merge-never（Marshal 不合并），ADR 0026 的 accept-after-merge 路径（需 PR 已被人工合并）在多数此类场景不可用，Run 无安全恢复路径。

缺失的不是数据可得性——GitHub check run 携带 provider 侧 `completed_at`，可以证明完成时刻——而是承载「分阶段 deadline」与「可信完成时间裁决」的契约。本 ADR 冻结该契约。

## 决策

核心方向：**观察与裁决分离**——deadline 不再阻断对远端事实的读取；远端事实始终被观察并留证，RunState 裁决只依据冻结的 ciDeadline 与可信完成时间。契约分四部分冻结：分阶段预算（方案 A）、冻结裁决基准（方案 B）、可信完成时间规则、与 ADR 0026 的 reconcile 交互。

### 方案 A：phased deadline——观察阶段独立预算

在 TaskSpec `budgets` 新增可选字段 `ciObserveTimeoutSeconds`，把 CI 观察阶段的预算从 `runTimeoutSeconds` 中分离。

**TaskSpec Schema 变更点声明**（`schemas/task-spec.schema.json`，Draft 2020-12，`apiVersion=marshal.dev/v1alpha1`，`kind=Task`）：

| 变更位置 | 变更内容 |
| --- | --- |
| `$defs.budgets.properties` | 新增 `ciObserveTimeoutSeconds`：`{ "type": "integer", "minimum": 1, "maximum": 604800 }`（上下界与 `runTimeoutSeconds` 一致） |
| `$defs.budgets.required` | 不变——该字段为可选，未声明时按「向后兼容」节回退 |
| `$defs.budgets.additionalProperties` | 保持 `false`——新字段必须显式声明，不允许经 extensions 旁路 |

语义冻结：

- `ciObserveTimeoutSeconds` 只约束 **CI 观察阶段**（`publication.completed` 之后，等待远端 required checks 出结果）；
- `runTimeoutSeconds` 保持 Run 总预算语义，约束观察阶段之前的全部阶段（Worker 执行、Review、Publish）；
- 观察阶段 deadline 锚定 **publication 时间**：`ciDeadline = publishedAt + ciObserveTimeoutSeconds`，不再锚定 `CreatedAt`；
- 一旦 `publication.completed` 已记录且 ciDeadline 已冻结，CI 观察阶段的裁决只依据冻结的 ciDeadline，`runTimeoutSeconds` 不再参与观察阶段裁决（否则预算混用问题原样复归）。

### 方案 B：frozen ciDeadline——publish 时冻结裁决基准

在 publish 时把 deadline 一次性计算并冻结进 PublicationRecord，此后一切 observe/accept/reconcile 只按冻结值裁决。

**PublicationRecord Schema 变更点声明**（`schemas/publication-record.schema.json`，Draft 2020-12，`kind=PublicationRecord`）：

| 变更位置 | 变更内容 |
| --- | --- |
| 顶层 `properties` | 新增 `ciDeadline`：`{ "type": "string", "format": "date-time" }`（RFC 3339，UTC） |
| 顶层 `required` | 不变——可选字段，缺失时按「向后兼容」节回退 |
| 顶层 `additionalProperties` | 保持 `false` |

语义冻结：

- Publisher 在写入 PublicationRecord 时一次性计算 `ciDeadline = publishedAt + ciObserveTimeoutSeconds`（字段缺失时按回退规则派生），写入后不得改写；
- PublicationRecord 经 `publication.completed` lifecycle 事件的 frozen digest 锚定（现有 ObserveChecks 已做 digest 对账，不符即 BLOCKED），`ciDeadline` 随之成为不可变裁决基准；
- 任何观察、accept、reconcile 不得从可变状态（如当前时刻重算、从 TaskSpec 重新推导）再生成裁决基准；冻结值缺失才允许回退计算（见「向后兼容」）。

### 取舍理由：采用 A+B 结合

| 方案 | 解决的问题 | 单独采用的缺口 |
| --- | --- | --- |
| 只用 A | 修复锚点错位与预算混用；观察阶段独立预算 | 每次观察仍须从输入重算 deadline，裁决不可复现、不可审计；无不可变裁决基准供事后 reconcile 引用 |
| 只用 B | 裁决基准不可变、可审计、digest 绑定 | 若推导源仍是 `CreatedAt + runTimeoutSeconds`，预算混用依旧——Worker 越慢 CI 窗口越短，Issue #30 只修一半 |
| **A+B（采用）** | A 定义分阶段预算语义与锚点；B 把派生结果冻结为不可变裁决基准 | —— |

冲突裁决顺序：PublicationRecord 携带 `ciDeadline` 时，一律以冻结值为准；仅当冻结值缺失时才允许按回退规则计算，且回退计算结果必须随裁决证据留痕（见「向后兼容」）。

### 可信完成时间：来源、权威性与缺失 fail closed

**RemoteCheckRecord Schema 变更点声明**（`schemas/remote-check-record.schema.json`，Draft 2020-12，`kind=RemoteCheckRecord`）：

| 变更位置 | 变更内容 |
| --- | --- |
| `checks.items.properties` | 新增可选 `completedAt`：`{ "type": "string", "format": "date-time" }`（RFC 3339），由 observer 从 provider check run 采集 |
| 既有字段与 `required` 集合 | 不变；`observedAt` 语义不变 |

权威性判定：

- **权威来源**：GitHub check run 的 `completed_at` 为完成时间的首要裁决字段。它由 provider 写入并与 check run `conclusion` 一体，是 branch protection 判定所用的同一事实面。check run steps 时间戳只作辅助证据，不作为裁决主字段；
- **本地时间不具权威性**：Marshal 本地 wall clock 与任何 Worker 自报时间戳一律不参与完成时间裁决；RemoteCheckRecord 的 `observedAt` 只是 Marshal 观察时刻，用于留证，不用于裁决；
- **时钟偏差容忍**：provider 时钟与 Marshal 时钟存在有界偏差。裁决采用固定容差 `Δskew`，契约推荐值 **300 秒**（实现 Milestone 冻结为常量并以测试断言）。`completedAt ≤ ciDeadline + Δskew` 判为「及时完成」；
- **一致性下界**：`completedAt < publishedAt − Δskew` 属证据不一致（check run 不可能在 head 发布之前完成），一律 fail closed，不得采信。

缺失 fail closed：required check `status=pass` 但 `completedAt` 缺失或不可读时，无法证明及时完成，一律 fail closed——Run 裁决为 `BLOCKED`，原因码 `ci-completed-at-missing`。不得以「观察时已绿」推定「deadline 前已完成」。

### 裁决流程（冻结顺序）

`ObserveChecks` 在 CI 观察阶段的裁决顺序：

1. 读取 PublicationRecord；携带 `ciDeadline` 时以冻结值为准，否则按回退规则计算并留痕；
2. **先观察，后裁决**：无论 `now` 是否超过 ciDeadline，都执行远端观察并持久化 RemoteCheckRecord（含 `completedAt`）作为证据——deadline 不阻断观察（Issue #30 期望 1）；
3. 远端仍 `pending`：`now ≥ ciDeadline` → `BLOCKED`，原因码 `ci-deadline-exceeded`（fail closed）；`now < ciDeadline` → 保持 `CI_PENDING` 继续等待；
4. 远端给出结果且全部 required checks `pass`：逐项核验可信完成时间（Issue #30 期望 2）——
   - 全部 `completedAt ≤ ciDeadline + Δskew` 且通过一致性下界检查 → 判为「及时通过」：Run 尚未 BLOCKED 时走既有 `publication.checks-passed` 转换（`CI_PENDING → ACCEPTED`）；
   - 任一 `completedAt` 缺失 → `BLOCKED`，原因码 `ci-completed-at-missing`；
   - 任一 `completedAt > ciDeadline + Δskew` → `BLOCKED`，原因码 `ci-completed-at-exceeds-deadline`；
   - 任一 `completedAt < publishedAt − Δskew` → `BLOCKED`，原因码 `ci-completed-at-inconsistent`；
5. 远端 `fail`/外部失败：沿用既有语义（rework 预算内 `REWORK_REQUESTED`，耗尽或外部失败 `BLOCKED`），本 ADR 不改变。

**已 BLOCKED 的 Run 不因后续观察自动复活**：一旦终态 `BLOCKED`，后续任何观察只允许追加 RemoteCheckRecord 证据，RunState 迁移必须走 ADR 0026 定义的 typed reconciliation（append-only、证据绑定、可审计）。

### 与 ADR 0026 的交互：BLOCKED(deadline) 之后 checks 变绿

场景：Run 已因 deadline 裁决进入 `BLOCKED`，其后远端 required checks 变绿。分两种情况：

**情况一：PR 已被人工合并。** 走 ADR 0026 既有 `reconcileType=accept-after-merge` 路径，本 ADR 附加两点要求：

- `evidenceDigests` 必须包含携带 `completedAt` 的 RemoteCheckRecord 摘要；
- `reconcileReason` 必须显式区分完成时间裁决结论：及时完成用 `ci-deadline-reconciled`，缺失或迟于 deadline 的事实必须在 reconcile 前置检查中 fail closed（即：accept-after-merge 不得采信无及时完成证明的迟到绿灯，维持 ADR 0026「reconcile 不得绕过 required checks」不变量的时间维度）。

**情况二：PR 未合并（merge-never，Marshal 不合并）。** ADR 0026 现有枚举不覆盖此场景。本 ADR 提议新增 reconcileType（仅经 `schemas/publication-reconcile-record.schema.json` 的枚举扩展生效，不改 ADR 0026 本体）：

| 字段 | 扩展建议 |
| --- | --- |
| `reconcileType` | 枚举新增 `accept-after-ci-deadline` |
| `observedState` | 封闭枚举无需新增——沿用既有 `BLOCKED` |
| `decidedState` | 封闭枚举无需新增——沿用既有 `ACCEPTED` |

`accept-after-ci-deadline` 的前置条件（全部成立才可 reconcile，任一不成立 fail closed）：

1. PublicationRecord 存在（含冻结 `ciDeadline` 或可回退计算）、ReviewDecision 已接受；
2. 全部 required checks `status=pass`；
3. 每一项 required check 的可信完成时间满足 `completedAt ≤ ciDeadline + Δskew` 且通过一致性下界检查（及时完成的正向证明——这是与「绕过 deadline」的本质区别：该 reconcile 只在 deadline 实际未被违反时成立）；
4. `evidenceDigests` 绑定 PublicationRecord（含 `ciDeadline`）、携带 `completedAt` 的 RemoteCheckRecord、ReviewDecision；
5. 幂等身份 canonical 绑定 `(authorityNamespaceId, runId, scmMergeReceiptId, reconcileType)` 的规则沿用 ADR 0026；未合并场景无 SCMMergeReceipt 时，以 `(authorityNamespaceId, runId, publicationRecordId, reconcileType)` 为幂等身份（实现 Milestone 冻结具体绑定式），同身份重复提交归并，内容冲突 fail closed。

不变量保持：新 reconcile 类型同样不得绕过 required checks 与 ReviewDecision、不得改写既有 PublicationRecord / ReviewDecision、不授予 Marshal merge 权限。

### 超时 fail closed 语义与机器可读原因码

fail closed 总语义：凡是无法以权威证据证明「及时完成」的情形，Run 一律进入或保持终态 `BLOCKED`，不做任何有利于通过的推定；`BLOCKED` 之后的任何状态迁移只能经 typed reconciliation。

封闭原因码集合（用于 `ObserveChecks` 的 BLOCKED 原因与 PublicationReconcileRecord 的 `reconcileReason`，均为机器可读封闭码，非自由文本）：

| 原因码 | 触发条件 | 结果 |
| --- | --- | --- |
| `ci-deadline-exceeded` | 观察时 `now ≥ ciDeadline` 且远端仍 pending，或无及时完成证据 | BLOCKED（与既有 sentinel 同名同义，保持兼容） |
| `ci-completed-at-missing` | required check 已 pass 但可信 `completedAt` 缺失 | BLOCKED，fail closed |
| `ci-completed-at-exceeds-deadline` | 可信 `completedAt > ciDeadline + Δskew` | BLOCKED，fail closed |
| `ci-completed-at-inconsistent` | 可信 `completedAt < publishedAt − Δskew`，证据不一致 | BLOCKED，fail closed |
| `ci-deadline-reconciled` | typed reconciliation 凭及时完成证明把 BLOCKED 迁移为 ACCEPTED | reconcileReason（唯一正向码） |

新增原因码只能经本 ADR 后续修订或新 ADR 扩展该封闭集合，不得以自由文本注入。

## 向后兼容与迁移

- **既有 TaskSpec 无 `ciObserveTimeoutSeconds`**：观察阶段回退到现状语义——deadline = `CreatedAt + runTimeoutSeconds`，行为与当前版本逐字一致；新字段不进入 `required`，既有 TaskSpec 校验结果不变；
- **既有 PublicationRecord 无 `ciDeadline`**：裁决基准回退为 `CreatedAt + runTimeoutSeconds` 计算值；`accept-after-ci-deadline` reconcile 允许使用该回退值作为冻结参照，并必须把回退计算结果记入 `evidenceDigests` 留痕；
- **既有 RemoteCheckRecord 无 `completedAt`**：旧记录合法有效；在任何需要可信完成时间的裁决中按 `ci-completed-at-missing` 处理（fail closed），不做推定、不回填；
- **Schema 兼容性**：三处变更（task-spec、publication-record、remote-check-record）均为在 `additionalProperties: false` 约束下显式新增可选 property；不改动任何既有 `required` 集合、字段类型与约束；`apiVersion` 保持 `marshal.dev/v1alpha1`；
- **历史已 BLOCKED(deadline) 的 Run**：不自动回填、不批量迁移；只做只读检查与证据保留，待本 ADR 实现后经 typed reconciliation 逐个迁移（与 ADR 0026 对 legacy 已合并 Run 的处置口径一致）。

## 后果

- Issue #30 期望 1-3 获得契约基础：deadline 后仍读远端事实（观察与裁决分离）、按可信完成时间裁决（`completedAt` + `Δskew` 规则）、超时 fail closed（封闭原因码）；期望 4 方向的「发布时冻结独立 ciDeadline」即本 ADR 方案 B，已一并冻结；
- checks 在 deadline 前已通过仍 BLOCKED 的复现链被切断：观察不再被本地 deadline 先行拦截，及时完成的绿灯可以正常走 `publication.checks-passed` 转换；
- 已误入 BLOCKED 的 Run 获得可审计恢复路径（`accept-after-ci-deadline` / 增强后的 `accept-after-merge`），且恢复以「deadline 实际未被违反」的正向证明为门槛，不放宽任何终态语义；
- 成本：三份 Draft 2020-12 Schema 的变更草案（task-spec、publication-record、remote-check-record）与 publication-reconcile-record 的枚举扩展，以及后续实现（observer 采集 `completedAt`、裁决顺序重排、reconcile 类型、正反 fixture）；Schema 文件落位于 `schemas/*.json` 既有自动 embed 路径，实现属后续 Milestone 退出门禁；
- 不放宽任何既有不变量：Worker 不自证、Worker/Publisher 分权、Draft-only、merge-never、fail-closed、publication 权威校验与 frozen digest 对账全部保持有效。

## 与既有 ADR 的关系

- **ADR 0026（Accepted）**：兼容。本 ADR 不修改 0026 本体；accept-after-merge 与 deadline 裁决的交互在「与 ADR 0026 的交互」节显式声明；`accept-after-ci-deadline` 仅作为 `reconcileType` 封闭枚举的扩展提议，经配套 Schema 修订生效，0026 已冻结的记录与不变量不受影响；
- **ADR 0027（Proposed，docs/adr/0027-candidate-record-and-verification-write-scope.md，2026-08-13 维护者经 PR #82 提交）**：主题正交——0027 冻结 Candidate 一等不可变记录与归一化写域，0028 冻结 CI deadline 观察与裁决契约。交互声明：0028 采集的 RemoteCheckRecord（含 `completedAt`）与 deadline 裁决原因码属 run 级观察事实，其持久位置与不可变性沿用既有 run-store/journal 权威模型（authority ledger 归属与 producer-authority 校验规则不变）；若未来该类观察记录或裁决记录被纳入 Candidate 记录或验证写域，其持久身份与写入必须遵循 ADR 0027 的不可变性与归一化写域约束——0028 不新设写域、不改变 0027 的写域边界，也不要求 0027 为本 ADR 做任何调整。

## 非目标

- 不实现 transport/SSE/实时推送：CI 观察保持 pull 式对账；
- 不实现多 provider checks 聚合：provider 仍为 `github`（RemoteCheckRecord `provider` 为 const），不设计跨 provider 完成时间归一化；
- 不改变 merge-never：Marshal 不获得 merge 权限、不自动 merge；不改变 Draft-only、Worker/Publisher 分权与 Worker 不自证；
- 不引入任何 Worker 侧自报时间戳的信任路径；
- 不修改 ADR 0026 / ADR 0027 本体；枚举扩展仅作用于配套 Schema；
- 不回填历史已 BLOCKED Run，不做批量迁移；
- 不实现 code：本 ADR 只冻结契约，实现属后续 Milestone 退出门禁。
