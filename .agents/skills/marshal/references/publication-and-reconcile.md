# Publication、CI 与 Reconcile

> **何时必须读取：** ReviewDecision 导入后返回 `PUBLISHING`/`CI_PENDING`，需要 publish approval、Draft PR、remote checks、`task accept`、controlled merge、远端 merge 或 `task reconcile`，以及判断 local/remote merge 和 milestone 完成事实时，必须完整读取。

## 状态优先

导入 Decision 后立即读取返回的 `targetState` 和最新 `task status`：

- terminal `ACCEPTED`、`NO_CHANGE`、`REJECTED`、`BLOCKED`：读取 Outcome，停止。不得 publish 或 `task accept`。
- `PUBLISHING`：完成当前 publication approval，再执行 `task publish`；重读状态。若直接 `ACCEPTED` 就停止，只有精确 `CI_PENDING` 才进入 checks。
- `CI_PENDING`：`marshal task accept --run RUN_ID` 是冻结 required checks 的验收，不是 review verdict accept 的通用后续。
- 其它状态只按 Core 的合法命令继续；禁止根据 `publication.required`、PR 页面或预期结果猜状态。

Publisher 与 Worker credential/权限必须分离。GitHub publisher 使用绝对 `MARSHAL_GH_PATH` 和独立 `MARSHAL_GH_CONFIG_DIR`；禁止绕过 Core 直接调用托管 API/创建 PR。

## Publish

`task publish` 是长命令，后台化并保存 stdout/stderr；控制端中断后先 status/doctor/events 对账，再幂等恢复同一命令。发布失败保存 typed Outcome/failure，不能用 Worker rework 修。

Draft PR、CI green 或 Review `ACCEPTED` 都不等于主干交付。`mergePolicy=never` 或 Core 无 merge 能力时，在第一次识别就报告治理/外部 blocker；不得继续派依赖该 PR 的 stale-base successor，也不得计作 milestone 完成。

## Observe before judge

`task accept` 和 controlled merge 必须先通过 Publisher 的 `ObserveChecks` 观察冻结 required checks，并在任何 status/deadline/merge adjudication 前持久化 identity-bound `RemoteCheckRecord`。不得用网页绿灯、旧 check 摘要或本地观察时间替代 Provider 的可信事实。

Operator 需要确认：

- check record 绑定当前 task/run、repository、request/PR 和 published `headSha`；
- observed check identity 精确等于冻结 required set，没有缺失、额外或 duplicate identity；
- status 与每个可信 `completedAt` 都通过 Schema/Core validation；
- 不论最后 pass、pending、fail 或时间证据拒绝，观察到的可信 record 都先成为审计证据；失败不伪造 merge receipt、reconcile record、Outcome 或终态事件。

`ciDeadline` 在 publish 时冻结并纳入 PublicationRecord digest。及时完成证明使用 Provider `completedAt`，每个 required passing check 必须位于：

`[publishedAt − 300s, ciDeadline + 300s]`。

deadline 后才观察到、但 Provider 证明在窗口内完成，仍可通过；缺 completedAt、duplicate、时钟不一致、晚完成或到期仍 pending 都按 Core 封闭 `reasonCode` fail closed。本地 observation time 不能替代缺失的 Provider completion time。具体枚举和兼容语义以 ADR 0028、`docs/task-lifecycle.md`、Schema 与 `internal/publication` 测试为准，不在 Skill 复制机器 contract。

## `CI_PENDING` 处理

1. 读取 fresh status、PublicationRecord 和 frozen required checks。
2. 通过 Core/Publisher 观察并持久化 RemoteCheckRecord。
3. `pass` 且 identity/timeliness 全部成立时，Core 才可进入 `ACCEPTED` 或受控 merge。
4. `pending` 未过 deadline 时保持 `CI_PENDING`，按短周期事件/Provider节奏观察；不盲目 rerun CI。
5. required check failure 按已接受的 typed CI failure/rework contract处理，保留 `CIFailureEvidence`，受 attempt/rework 双预算约束；不要把它归因成原 Worker diff，除非 Core 形成对应 round-bound rework origin。
6. evidence admission、time fact 或 identity 拒绝时保持 fail closed，原样报告 reason；修观察器/contract 后再走合法动作，不写 `.marshal`。

## Merge 边界

远端 merge 只有 required checks 全绿且 Core/Policy/approval 允许时才执行；禁止 `--auto` 提前合并。远端差异或 history divergence 时停止推送并审计，禁止 force 覆盖。

用户明确授权的 Harness 本地闭环是独立 maintainer path：在唯一 reviewer P0/P1 清零、clean/mergeable、定向 test/race（相关时）/vet/staticcheck/schema/diff-check/secret scan/merge-tree 通过后，维护者可在 local main 创建 merge commit并作为后继权威基线。必须记录：

- `sourceHead`
- `localMergeSha`
- 精确验证摘要
- `pendingRemoteSync`

本地 merge 不等于 GitHub PR/远端 merge；只能报告实际发生的事实。远端同步和 CI 可异步补证，但 remote merge 仍等 required checks。

## Accept-after-merge reconcile

若已合并 PR 的 Run 因 publication/CI 路径进入 terminal `BLOCKED`，只使用 typed：

```bash
marshal task reconcile --run RUN_ID
```

Reconcile 只适用于 post-publication `BLOCKED`，不用于未合并 PR；未合并回 `task accept`。它必须：

1. 重验 current ledger、frozen accepting ReviewDecision、verification 和 PublicationRecord；
2. 观察 immutable `SCMMergeReceipt`；
3. fresh re-observe、validate 并先持久化 merged head 的 RemoteCheckRecord；
4. 对 CI time-block 的四类封闭原因取得 fresh timely proof；非 time-block 保持 ADR 0026 语义；
5. 之后才持久化 receipt、append-only `PublicationReconcileRecord` 与唯一 `publication.reconciled` 终态例外。

缺 prerequisite、identity/digest mismatch、checks 非全绿、timeliness 不成立或 idempotency conflict 都保持 `BLOCKED`，不得写 receipt/event/outcome 来“修”状态。重复 reconcile 必须幂等；它不修改原 PublicationRecord/ReviewDecision，也不绕过 required checks 或 merge-never。

## 事实汇报

每次分别说明 local main HEAD、origin/main、source branch/head、PR 状态、required checks、remote merge 是否真实发生和 `pendingRemoteSync`。Milestone 完成必须有当前权威文档规定的所有交付/门禁证据；单个 PR、局部 test 或 local merge 不能外推为整个 milestone/v1.0 完成。
