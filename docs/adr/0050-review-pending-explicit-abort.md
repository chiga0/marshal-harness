# ADR 0050：`REVIEW_PENDING` Run 的最小显式 abort 出口

- 状态：提议（Proposed）
- 日期：2026-08-26
- 决策来源：真实 dogfood 中，Worker 与 Verifier 已完成，但 TaskSpec、验收门禁或 ReviewPacket 发生结构性缺陷，Run 停留在 `REVIEW_PENDING`；继续 rework 会把上游缺陷错误归因给 Worker，手工修改 `.marshal` 又会绕过 authority journal
- 关联：[ADR 0012](0012-explicit-abort.md)、[ADR 0029](0029-pre-attempt-abort.md)、[任务生命周期](../task-lifecycle.md)、[安全模型](../security-model.md)

## 背景

ADR 0012 已用 `task abort → run.aborted → BLOCKED` 解决 `RETRY_PENDING` 死 Run，ADR 0029 解决零 Attempt 的 `PLANNED`/`READY`。两者都刻意排除了 post-worker 状态。

当前反复出现的真实缺口只在 `REVIEW_PENDING`：Worker 和 Verifier 已退出，Run 不再产生执行证据，但结构性上游缺陷又使 ReviewDecision 无法安全生成或导入。此时启动 successor 是正确修复方向，旧 Run 却没有保留证据后进入终态的合法出口。

本 ADR 只补这个已复发缺口，不设计通用 intervention 子系统。

## 决策

### 1. 复用现有命令、事件和终态

现有命令增加一条封闭转换：

```text
marshal task abort --run RUN_ID --actor ID --reason TEXT

REVIEW_PENDING -- run.aborted / human --> BLOCKED
```

- 复用 `run.aborted`、human actor、Run Lease、`terminalReason=aborted-by-operator`、Outcome `verdict=abort`、`result.md` 和现有幂等恢复；
- 不新增 `ABORTED` 路径、event family、request carrier、projection Schema、独立 ledger 或新命令；
- abort 不是 review verdict，不创建、替换或伪造 ReviewDecision。

### 2. `PostWorkerAbortSafe` 守卫

首版只接纳 `legacy-local` Run。仅当以下条件全部可由 current authority 肯定证明时允许转换；未知、不可读或不一致一律 fail closed，且除既有 Run Lease owner 操作记录外，不产生 lifecycle/control/publication/Outcome/`result.md`/snapshot authority 写入：

1. current RunState 与 journal 都是同一 `REVIEW_PENDING` sequence；
2. 在同一 held Run Lease 下重放得到唯一且闭合的 legacy-local execution lineage：exact `currentAttemptId` 的 `worker.started → worker.completed → verification.completed` 相邻业务事实分别具有固定 actor、状态与 payload binding，之后不存在任何 execution event；
3. Core 在 append 前、与 mutation 相邻地再次重放并要求第 2 项完全不变。该闭合 lineage 是首版唯一 quiescence authority；禁止用 OS-wide、PID-only 或 `ps` 负扫描证明子进程不存在，也不得 Signal 或 kill 任何非本编排进程；
4. 不存在 PublicationIntent、PublicationRecord、SCMMergeIntent、SideEffect 或未决 publication transaction；
5. 不存在未决的 lifecycle-mutating `InterventionRecord`、effectful control request 或 control transaction；查询失败同样拒绝，避免两个 terminal intent；
6. caller 持有 Run Lease，并以 expected sequence 追加事件。

Candidate、ReviewPacket 或验收材料可以缺失、陈旧或结构性无效，因为 abort 不接纳其内容；已有字节必须原样保留。registration、AgentBinding、SandboxBinding 或 ResultIngress 不要求继续 current，它们不是 legacy-local 安全终止已完成 Attempt 的必要条件，也不得被 abort 升级为 production/hardened 证据。production profile 不在首版源集合；未来若开放，必须先冻结故障域外 quiescence authority，不得复用本节的 legacy-local 结论。

### 3. reducer 与恢复边界

- `DecisionCurrent` 只对 exact `run.aborted REVIEW_PENDING → BLOCKED` 例外；其它 `REVIEW_PENDING` 出边仍必须绑定 current ReviewDecision；
- 全部 guard 通过后可以按现有实现预制不具 authority 的 pending Outcome/`result.md`；`run.aborted` 必须是首个 committed business fact，随后再幂等 commit Outcome、`result.md` 与 snapshot。event 前 crash 的 pending 文件必须可忽略或清理，event 后任一阶段 crash 必须从 exact event 恢复；
- 相同 actor/reason 的 lost-response replay 复用现有 abort authority；并发请求只有一个 expected-sequence 赢家；
- 不删除或修改 worktree、Candidate、Evidence、transcript、branch、PR 或历史 ReviewPacket；终态不可复活，后续工作必须创建 successor Run。

### 4. 明确排除

- `RUNNING` 继续走受控 active intervention；
- `VERIFYING` 可能仍有 verifier 事务，不在首版范围；
- `REWORK_REQUESTED` 可能来自 review、CI 或 publication，不在首版范围；
- `PUBLISHING`、`PUBLISHED`、`CI_PENDING` 继续走 reconcile/人工护栏；
- supervisor/watchdog 只可建议 abort，不获得 lifecycle 写权限；首版只允许 human 调用固定 CLI。

若以上状态出现独立、重复且有证据的死 Run，再以其准确 producer lineage 扩展，不预先合并不同事务语义。

## 实现门禁

接受本 ADR 只冻结合同，不表示功能已完成。实现至少证明：

1. 正向 `REVIEW_PENDING → BLOCKED` 产生唯一 `run.aborted`、Outcome、`result.md` 与一致 snapshot；
2. forged producer、stale sequence、Lease 未持有、legacy-local lineage 非唯一/不相邻/追加了 execution event、publication/SideEffect 存在时全部拒绝，且除 Lease owner 操作记录外零 authority/business 写入；
3. `RUNNING`、`VERIFYING`、`REWORK_REQUESTED`、publication 状态和全部终态固定拒绝；
4. `DecisionCurrent` 例外仅命中本 ADR 的 exact event/transition；
5. 未决 effectful control/InterventionRecord 固定拒绝；覆盖 control record 已追加但 terminal event 未追加的 crash window，不产生第二 terminal intent；
6. event 前 pending projection crash 可安全忽略/清理，event 后各 commit crash window可幂等恢复，并发请求只有一个赢家；
7. abort 前后原 worktree 与 Evidence 字节不变；
8. 定向测试、相关 race、staticcheck/vet、Schema/diff/secret scan 与 merge-tree 全绿，独立 reviewer 对 exact sourceHead 的 P0/P1 清零；
9. 固定 Marshal 二进制对一个真实 legacy-local `REVIEW_PENDING` Run 完成演练，且没有 Worker、Publisher或远端副作用。

## 后果

- 结构性上游缺陷不再被迫伪装成 Worker rework，旧 Run 可以留证据后终止并创建 successor；
- 实现复用已验证工具链，改动面保持为一个 reducer 出边、一个 guard 和现有 CLI 分支；
- 本 ADR 不阻塞 R3-D/E/F，不要求 production 双 binding 先完成，也不改变发布权限。

## 未采用方案

- 新增 `ABORTED`、`control.post-worker-aborted` 与独立持久化协议：问题规模不支持新的 lifecycle 子系统，否决；
- 要求 Candidate/ReviewPacket 健康后才允许 abort：会使结构性门禁故障再次无出口，否决；
- 要求 registration/binding/ResultIngress 继续 current：它们可能正是终止旧 Run 的原因，否决；
- 允许 supervisor 自动终止：扩大 lifecycle 写权限，否决。
