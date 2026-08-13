# ADR 0027：CI deadline 分阶段观察与冻结裁决（按可信完成时间裁决）

- 状态：Proposed（尚未生效；后续经维护者 ApprovalRecord 接受后生效，接受方式由维护者另行决定）
- 日期：2026-08-13
- 决策来源：公开 [Issue #30](https://github.com/chiga0/marshal-harness/issues/30) 期望 5「增加 phased deadline 或发布时冻结独立 ciDeadline」及其前置期望 1–3（deadline 后仍读取远端事实、按可信完成时间裁决、超时 fail closed）；与 ADR 0026（SCMMergeReceipt 与 PublicationReconcileRecord）显式兼容

## 上下文

### 问题陈述：runTimeoutSeconds 与 CI 实际完成时间的语义错位

`budgets.runTimeoutSeconds` 的语义是「Marshal 侧执行与重试的预算」，但 `ObserveChecks`（`internal/publication/checks.go`，只读参照）当前将其复用为 CI 观察 deadline：`deadline = RunState.CreatedAt + runTimeoutSeconds`，且 deadline 检查先于一切远端观察——`now ≥ deadline` 时直接 block 并把 Run 置为终态 `BLOCKED`（原因 `ci-deadline-exceeded`），不读取任何远端事实。CI 实际完成时间由 provider 侧（GitHub）决定，不受 Worker 与 Marshal 控制；把观察 deadline 锚定在 Run 创建时刻，混淆了「本地执行预算」与「远端 CI 观察窗口」两种语义。

复现链（checks 在 deadline 前已通过，Run 仍 BLOCKED）：

1. `T0`：Run 创建（`RunState.CreatedAt=T0`），TaskSpec `budgets.runTimeoutSeconds=X`；
2. `T1`（`T0 < T1 < T0+X`）：Worker 完成，Publisher 创建 Draft PR 并记录 `publication.completed`（`PublicationRecord.publishedAt=T1`），Run 进入 `CI_PENDING`；
3. `T2`（`T2 < T0+X`）：GitHub 侧全部 required checks 成功（provider 侧完成事实已成立）；
4. `T3`（`T3 ≥ T0+X`）：ObserveChecks 首次或重试被调用（轮询间隔、运维重试或 attempt 调度延迟所致）；
5. deadline 检查先于一切远端观察触发：`now(T3) ≥ CreatedAt + X` → 直接 block，未写入任何 RemoteCheckRecord；
6. 「checks 已在 deadline 前通过」的事实从未被读取；`BLOCKED` 为终态且 ObserveChecks 仅接受 `CI_PENDING`，Run 无自救路径。

错位因此复合为终态问题：BLOCKED 之后远端事实即使可得也无法进入裁决；若无 typed reconciliation 出口，此类 Run 永久停留终态 BLOCKED（对照 ADR 0026 对 Issue #25 的 terminal contract gap 定位）。

### 与 ADR 0026 的关系

ADR 0026 冻结了 SCMMergeReceipt（不可变 merge 事实证据）与 PublicationReconcileRecord（append-only reconcile 载体），为 `BLOCKED → ACCEPTED` 提供 `accept-after-merge` typed reconciliation。本 ADR 定义 deadline 裁决与该 reconcile 的交互（见「决策」第 6 节）；不修改 ADR 0026 本体。

## 决策

采用「phased deadline + 冻结 ciDeadline」组合方案：观察阶段预算与执行阶段分离（Issue #30 期望 5 的 phased deadline 形态），并在发布时把 ciDeadline 冻结进 PublicationRecord，此后观察、裁决与 reconcile 一律按冻结值执行（期望 5 的发布时冻结形态）。

### 方案取舍

| 方案 | 内容 | 优点 | 缺点 |
| --- | --- | --- | --- |
| A phased deadline | 新增 `budgets.ciObserveTimeoutSeconds`，观察阶段与 `runTimeoutSeconds` 分离 | 声明式、按任务可调、向后兼容（可选字段） | 仍是「本地超时」语义；单独无法救回「deadline 前已过、deadline 后才观察」的场景 |
| B 冻结 ciDeadline | 发布时把 ciDeadline 冻结进 PublicationRecord，accept/reconcile 按冻结值裁决 | 裁决锚点确定、可审计，与观察调用时机解耦 | 单独不定义观察阶段预算，也不回答「deadline 后是否仍读远端事实」 |
| A+B 组合（本 ADR） | 观察预算分离 + 发布时冻结 + 按可信完成时间裁决 | 同时闭合「预算语义错位、锚点漂移、终态无出口」三层问题 | 两个记录各增一个可选字段（均为向后兼容变更） |

采用理由：仅 A 只是拆分预算，deadline 仍是「观察调用时刻的本地墙钟」，而期望 1–2 的核心是裁决必须基于远端事实（可信完成时间）与确定锚点，故 B 的冻结裁决不可省略；仅 B 则缺少观察阶段的声明式预算控制。组合方案完整覆盖期望 1–3 与期望 5。

### 1. 阶段分离：观察阶段独立预算

冻结两个阶段：

- **执行阶段**：仍由 `runTimeoutSeconds` / `attemptTimeoutSeconds` 治理，语义不变；
- **观察阶段**（`CI_PENDING`）：由新字段 `budgets.ciObserveTimeoutSeconds` 治理；该字段存在时，观察 deadline 不再由 `RunState.CreatedAt + runTimeoutSeconds` 推导。

Schema 变更点 1（声明，实现归后续 Milestone）：`schemas/task-spec.schema.json` 的 `budgets` 定义新增可选字段：

| 字段 | 类型与约束 | 语义 |
| --- | --- | --- |
| `ciObserveTimeoutSeconds` | integer，`1 ≤ 值 ≤ 604800`，可选 | 观察阶段预算（秒）；缺省时维持既有推导行为（见「向后兼容与迁移」） |

### 2. 发布时冻结 ciDeadline 进 PublicationRecord

发布完成（记录 `publication.completed` 事件）时冻结 `ciDeadline = publishedAt + ciObserveTimeoutSeconds`（RFC 3339，UTC）。冻结后不可改写：ObserveChecks 既有的「PublicationRecord canonical digest 与 publication.completed 冻结 digest 对比」天然覆盖 ciDeadline（digest 覆盖整个记录），任何改写导致 digest 不符并 fail closed。

Schema 变更点 2：`schemas/publication-record.schema.json` 新增可选字段：

| 字段 | 类型与约束 | 语义 |
| --- | --- | --- |
| `ciDeadline` | string，RFC 3339（date-time），可选 | 冻结的 CI 观察与裁决 deadline；缺省时按向后兼容规则推导 |

### 3. 裁决顺序：deadline 后仍读取远端事实（期望 1）

取消「先 deadline 检查、后观察」的顺序。ObserveChecks 进入后的裁决顺序：

1. 完成既有身份/权威/digest 校验（语义不变，含 publication.completed producer-authority 与冻结 digest 对比）；
2. 执行远端观察，采集 RemoteCheckRecord（含各 check 的完成时间，见第 4 节）；
3. 按下表裁决（deadline 一律指冻结的 ciDeadline）：

| 观察事实 | now < ciDeadline | now ≥ ciDeadline |
| --- | --- | --- |
| 全部 required checks pass 且可信完成时间 ≤ ciDeadline(+容差) | ACCEPTED（既有 `publication.checks-passed` 迁移） | ACCEPTED（同左） |
| 全部 required pass 但可信完成时间 > ciDeadline(+容差) | 归入不可信分支（完成时间晚于观察时刻必破坏顺序约束） | BLOCKED，原因码 `ci-completed-after-deadline` |
| 完成时间缺失 / 顺序不可信 / conclusion 冲突 | BLOCKED，原因码 `ci-completed-at-untrusted`（fail closed） | 同左 |
| 仍 pending | 保持 `CI_PENDING` 继续等待（既有语义） | BLOCKED，原因码 `ci-deadline-exceeded` |
| fail | 既有 rework 语义（预算未用尽 → `REWORK_REQUESTED`；用尽 → BLOCKED） | BLOCKED，原因码 `ci-deadline-exceeded`（超时 fail closed，不再 rework） |
| external-failure | BLOCKED（既有语义） | BLOCKED（既有语义） |

核心变化：deadline 后唯有「可信完成时间证明全部 required checks 在 deadline 内通过」可迁移 ACCEPTED；其余观察结果一律 fail closed 置 BLOCKED（期望 3）。

### 4. 可信完成时间（期望 2）

来源与权威性判定：

- **唯一权威来源**：GitHub check run 节点的 `completed_at` 时间戳（RFC 3339），为 provider 侧服务器时钟事实；
- check run steps 的时间戳仅作辅助证据，不参与裁决（可缺失、可乱序、依赖具体 runner 实现）；
- `completed_at` 可信的前提（全部满足，任一不满足即不可信，不可信一律 fail closed）：
  1. check run 处于 completed 状态且 conclusion 映射为 RemoteCheckRecord 的 `pass`（与既有 status 枚举映射一致）；
  2. `completed_at` 存在——缺失时不得推断、不得以 observedAt 或其他时间顶替；
  3. 顺序约束：`completed_at ≤ observedAt + 时钟偏差容忍` 且 `completed_at ≤ ciDeadline + 时钟偏差容忍`；
  4. 身份绑定不变：taskId/runId/repositoryId/requestId/headSha 与 PublicationRecord 对账（既有 identity 校验，不改）。
- **时钟偏差容忍**：固定契约常量 300 秒，覆盖 provider 时钟漂移与观察链路时延；不作为 TaskSpec 字段暴露，避免按任务放宽削弱裁决；
- Schema 变更点 3：`schemas/remote-check-record.schema.json` 的 `checks` 数组项新增可选字段：

| 字段 | 类型与约束 | 语义 |
| --- | --- | --- |
| `completedAt` | string，RFC 3339（date-time），可选 | 该 check 的 provider 侧完成时间（取自 check run `completed_at`）；缺省即该 check 视为无可信完成时间 |

不含 `completedAt` 的既有 RemoteCheckRecord 保持合法，仅无法参与「deadline 内可信通过」裁决。

### 5. 超时 fail closed 与机器可读原因码（期望 3）

fail closed 语义：在/过 ciDeadline 时不能确立可信通过事实，Run 一律置终态 `BLOCKED`；不得弱化为 `REWORK_REQUESTED`、不得继续等待、不得自返；离开 BLOCKED 的唯一路径是第 6 节的 typed reconciliation。原因码为封闭集合、机器可读、非自由文本，写入 BLOCKED 事件的 `terminalReason`/reason payload，并与既有错误字面量保持一致：

| 原因码 | 触发条件 |
| --- | --- |
| `ci-deadline-exceeded` | 既有原因码：在/过 deadline 时 CI 仍 pending、fail 或不可判定 |
| `ci-completed-after-deadline` | 新增：required checks 全部成功，但可信完成时间晚于 ciDeadline(+300s) |
| `ci-completed-at-untrusted` | 新增：完成时间缺失、顺序违约或 conclusion 冲突，无法证明 deadline 内完成 |

### 6. 与 ADR 0026 的交互：BLOCKED(deadline) 之后 checks 变绿

三条出口，按优先级：

1. **主路径（无需 reconcile）**：第 3 节裁决顺序修正后，「deadline 后才观察到、但完成时间在 deadline 内」直接在观察中迁移 ACCEPTED，BLOCKED 不发生；
2. **accept-after-merge（既有）**：Run 已 BLOCKED(deadline) 后 checks 变绿、且 PR 已由人合并（merge-never 仅约束 Marshal，不约束人），走 ADR 0026 accept-after-merge 完成 `BLOCKED → ACCEPTED`。显式声明：该路径的「required checks 全部成功」前置不变量，按本 ADR 的冻结 ciDeadline 与可信完成时间裁决（而非观察墙钟时间）；deadline 裁决事实计入 `evidenceDigests`；
3. **新增 reconcileType（未合并兜底）**：BLOCKED(deadline) 后 checks 变绿但 PR 未合并时，出口 2 不适用。PublicationReconcileRecord 的 `reconcileType` 封闭枚举扩展为：

   `accept-after-merge | accept-after-ci-deadline`（新增）

   - `observedState` / `decidedState` 封闭枚举无需扩展：`BLOCKED` 与 `ACCEPTED` 已是 ADR 0026 冻结枚举成员，新类型取 `observedState=BLOCKED`、`decidedState=ACCEPTED`；
   - 前置条件：冻结 ciDeadline 存在；RemoteCheckRecord 证明全部 required checks pass 且可信完成时间 ≤ ciDeadline(+容差)；current-ledger recheck 确认 Run 仍处 BLOCKED；
   - Schema 变更点 4（建议，与本 ADR 实现一并落地，不改 ADR 0026 本体）：`schemas/publication-reconcile-record.schema.json` 中 `scmMergeReceiptId` 由恒必填改为条件必填——仅 `reconcileType=accept-after-merge` 时必填；`accept-after-ci-deadline` 以 `(authorityNamespaceId, runId, reconcileType, deadline-block 事件 digest)` 为幂等 canonical 身份绑定，`evidenceDigests` 必须包含冻结 PublicationRecord（含 ciDeadline）、deadline BLOCKED 事件与 RemoteCheckRecord 的 digest；
   - 幂等与冲突语义沿用 ADR 0026：同身份重复提交归并（幂等），同身份关键内容冲突一律 fail closed；
   - `reconcileReason` 使用封闭原因码，为该类型新增 `ci-deadline-trusted-pass`。

   出口 3 仅救济「事实上在 deadline 内通过、但因观察时机误入 BLOCKED」的 Run；deadline 后才完成的绿 checks 不满足可信完成时间前置，维持 `ci-completed-after-deadline` 终态，不得经本出口绕过 deadline。

### 7. 向后兼容与迁移

- **TaskSpec 无 `ciObserveTimeoutSeconds`**：deadline 维持既有推导 `RunState.CreatedAt + runTimeoutSeconds`；新裁决顺序（先读远端事实再裁决）同样适用，只扩大 ACCEPTED 可得性，不新增 BLOCKED 触发面；
- **PublicationRecord 无 `ciDeadline`**（既有记录与迁移窗口内的旧 Run）：裁决按 TaskSpec 规则确定性推导 deadline，不拒绝、不 fail closed；本 ADR 实现后的新发布一律写入 `ciDeadline`；
- **RemoteCheckRecord 无 `completedAt`**：记录保持合法，仅无法参与 deadline 后的可信通过裁决（回落 fail closed 分支）；
- Schema 变更点 1–3 均为新增可选字段，既有文档继续通过校验；变更点 4 是唯一触及 ADR 0026 配套 Schema 的变更，以本 ADR 名义实现并单独评审，ADR 0026 本体文档不重写；
- 不回填数据：既有 deadline BLOCKED 的 Run 只读保留，实现落地后经 typed reconciliation 救济（与 ADR 0026 的不回填原则一致）。

## 后果

- Issue #30 期望 1–3 获得契约基础：deadline 后仍读远端事实（裁决顺序，第 3 节）、按可信完成时间裁决（第 4 节）、超时 fail closed（第 5 节封闭原因码）；期望 5 以「分阶段 + 发布时冻结」组合落地（第 1、2 节）；
- 「deadline 前已过仍 BLOCKED」的复现链被双层关闭：主路径不再产生该类 BLOCKED；残留 BLOCKED 有 accept-after-merge / accept-after-ci-deadline 两条 typed reconciliation 出口；
- 裁决锚点从「观察调用墙钟」变为「冻结 ciDeadline + provider 侧完成时间」，Run 裁决可复现、可审计，与轮询时机解耦；
- 成本：四个 Schema 变更点（三个新增可选字段 + 一个 reconcile 记录演进点）及对应实现（采集、裁决、reconcile 命令与正反 fixture），归后续 Milestone；
- 不放宽任何既有不变量：Worker 不自证、Worker/Publisher 分权、Draft-only、merge never、fail-closed、ADR 0026 幂等与冲突语义全部保持有效。

## 非目标

- 不实现 SSE/Webhook 等 transport 层事件推送：CI 观察保持 pull 式轮询观察；
- 不实现多 provider checks 聚合：`provider` const `github` 语义不变，gitlab/none 路径不在本契约内；
- 不实现代码与 Schema：本 ADR 只冻结契约与变更点声明，实现属后续 Milestone 退出门禁；
- 不修改 ADR 0026 本体：对其配套 Schema 的扩展以本 ADR 变更点 4 声明并以本 ADR 名义实现；
- 不改变 merge-never：Marshal 不获得 merge 权限、不自动 merge；人合并仅作为出口 2 的前置事实；
- 不回填既有 deadline BLOCKED 的 Run：只读保留，待实现落地后经 typed reconciliation 救济。
