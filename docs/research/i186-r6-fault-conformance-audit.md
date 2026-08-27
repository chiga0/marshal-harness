# I186-R6 故障一致性测试盘点（ADR 0052 §1.8）

日期：2026-08-27（Explore 审计，Lead 整理落盘）
口径说明：「路径级」= 经 `cmd/marshal → internal/cli → execution.Run → Adapter.Run`、supervisor、runstore 这条 ADR 0052 认定的真实生产链（含 fake adapter）；「组件级」= 纯包内 fixture（recovery 决策矩阵、soak、spine、resultingress 等）。按 ADR 0052 §3，仅组件级不算生产可达性证据。分类依据 import graph + 调用链阅读，未跑运行时验证套件。

## 1. kill（进程被杀 / worker 死亡）

**已有测试**

路径级：

- `internal/cli/supervise_test.go` `TestSuperviseOnceRetriesPublishForDeadDriver` / `TestSuperviseOnceReturnsDeadRunningRunToCore` —— 真实 CLI Run → `supervise --once`，判定死 driver 后重派 `task publish` / `task run --recover-dead-driver`；但 driver「死亡」用 2h 陈旧 journal seed 模拟，非真杀进程。
- `internal/supervisor/supervisor_test.go` `TestSuperviseRecoveryGateAdmitsOrphanWithoutSideEffect`、`TestSuperviseRecoveryGateSkipsSideEffectRunNeedingReconcile` —— 新接线 `recoveryDecision`：无副作用孤儿放行（断言精确 argv 含 `--recover-dead-driver`）；声明 publication 副作用 + unreachable 判 ambiguous-side-effect，fail closed 指向 `marshal explain run`，且断言 `fake.attempts == 0`。
- `internal/cli/explain_test.go` `TestRecoverTakeoverAdmissionMatrix` —— `recoverTakeoverAdmission` 三分支（副作用-free 放行 / 副作用孤儿 fail-closed 带 explain 指针 / unknown run fail-closed）。
- `internal/execution/service_test.go` `TestLocalSelfIdentityDispatchCrashLeavesNoAttemptAuthority` —— dispatch 前崩溃 hook，无半成品 attempt 权威。
- 孤儿接管：`TestOrphanedRunningAttemptRecoveryIsFencingCapable`（stale RUNNING attempt → fence + 新 attempt 且带 fencingGeneration/supersedesAttempt 权威 payload）。

组件级：

- `internal/recovery/recovery_test.go` `TestMatrix_ProcessDeath`（unknown → fence+new attempt）。
- `internal/adapter/qwen/provider_failure_test.go` `TestRunCancellationConflictReturnsDeterministically` —— terminal + cancel + 进程组 SIGKILL，Wait 收敛窗口与冲突确定性，10x 压力；`TestRunContextCannotMaskTerminalConflict`、`TestResolveAttemptFailureCancellationConflictDoesNotDependOnWaitError`。
- `internal/adapter/qoder/fake_test.go`、`process_wait_darwin/linux_test.go`（kill 收敛 SIGKILL）；`internal/adapter/pi/pi_test.go`；`internal/terminal/process_supported_test.go`（sandbox 进程树 kill）。
- `internal/cli/detach_test.go` `TestDetachSurvivesCallerProcessGroupKill` —— 真 `kill -- -PGID`，detached driver 存活（setsid 隔离）；是真杀进程但对象是 detach fixture binary，非 marshal run。
- `internal/spine/faults_test.go` `TestFaultInjection_CrashWindows`（kill/timeout crash window typed errors + clean retry LedgerSequence=1）、`internal/agentruntime/executor_test.go` `TestExecutor_Execute_ExecFault_FailClosedAndTerminates`。
- `internal/soak/soak_test.go` `TestInvariantSoak10k`（10k 轮 unknown 观察混入 Decide invariant 断言）。

**缺口**

- 无真实 marshal worker/driver 进程中段 `kill -9` 后经 Inspect/supervise/resume 恢复的端到端 fixture；现有「死 driver」全部是陈旧 journal 场景构造，不走 runstore lease owner 死活探针（`TestLeaseOwnerProcessAliveDistinguishesExitedOwner` 只在 runstore 组件层）。
- `supervisor.recoveryDecision` 的 decision-error 分支（explain 装配失败 → “recovery decision unavailable” skip）无测试。
- 新 gate 下 binding 损伤输入不可达且未测：`explain.Assemble` 的 `deriveBindings` 只在 attempt 目录存在 `sandbox-binding-admission.json` 时才返回非全 OK，supervisor/explain 无 anchor 缺失/被替换注入。

## 2. restart（重启后由 durable ledger 恢复）

**已有测试**

路径级（全部是同进程内第二次 `Run`，不是跨 OS 进程重启）：

- `internal/execution/service_test.go` `TestStructuralFailureRestartCompensationDoesNotRelaunchWorker`（terminal append 后 crash → 重启补偿 Outcome，Worker 调用数恒 1）；`TestStructuralFailureRestartRejectsTamperedPersistentFields`（篡改 journal 持久字段后重启 fail-closed）；`TestStructuralFailureRunLeasePreventsConcurrentRelaunch`。
- 事务恢复矩阵：`TestRunReviewReworkOperationalRetryPersistsFindingsAfterRestart`、`TestRetryPendingOrphanQuarantineReconcilesAfterEventAppendCrash`、`TestOrphanBudgetTerminalTransactionRecoversAfterRestart`、`TestOrphanQuarantineTransactionRecoversBeforeTerminalAppend`、`TestOrdinaryWorkerFailureBudgetClosureAndRestartCompensation`、`TestOrdinaryTerminalQuarantineRecoversBeforeEventAppend`、`TestLocalSelfIdentityTerminalTransactionCompensatesCrashesAndQuarantineFailure`。
- `internal/execution/worker_runner_soak_test.go` `TestPathSoakBridgedRuns` —— 5 轮 bridged Run（fake adapter + Fake provider + 实 LocalRunner），断言 journal 单调、attemptId 全局唯一、runstore 重开 replay 等价、allocation record 落盘、二次业务事实为零。

组件级：

- `internal/runstore/store_test.go`：`TestRebuildIgnoresTruncatedJournalTail`、`TestInspectReplaysJournalAheadOfSnapshot`、`TestInspectReplaysPublicationIdentityAfterSnapshotCrash`、`TestInspectDetectsStateMismatchAtSameSequence`、`TestLeaseOwnerProcessAliveDistinguishesExitedOwner`、`TestAcquireMigratesLegacyLeaseOwnerAfterExclusiveLock`、`TestLeaseHeldFailsClosedWhenLockPathIsReplaced`、`TestLeaseMutationRejectsReplacedRunAuthorityDirectory`。
- `internal/runstore/control_test.go`：`TestControlTruncatedTailFailsClosed`、`TestControlReadRejectsCorruptJournal` 等。
- `internal/server/registration_test.go` `TestRegistrationLedgerRecoversAcrossReopen`（Port reopen 恢复 ledger/idempotency——server 组件层，非 server 进程重启）。
- `internal/recovery/recovery_test.go` `TestMatrix_ProviderRestartReverified`；`TestDecide_Idempotent`。

**缺口**

- 无跨 OS 进程 restart fixture：所有重启均为进程内二次 `Run`；没有「新起一个 `marshal` 二进制进程复用同一 state root」的恢复测试。
- `marshal-server` 无自身进程重启恢复测试（`cmd/marshal-server/main_test.go` 仅覆盖启动/loopback 校验/usage；ADR 0052 §1.7 的「loopback 能恢复真实 Run」无故障面证据）。
- supervisor 自身 kill→重启自愈无 fixture（它每轮从 runstore 重建扫描，理论上无状态，但无显式测试佐证该不变量）。

## 3. lost response（结果已产生、响应丢失 → Inspect 重建）

**已有测试**

路径级：

- `internal/execution/service_test.go` `TestStructuralFailureRestartCompensationDoesNotRelaunchWorker`（terminal append durable 后崩溃 → 重启补偿收敛、不重启 Worker；属「failure 终态 + 崩溃补偿」，最接近 lost-response 的路径级证据，语义不完全等同）。
- `BeforeLocalResultIngress` hostile/replay seam（`service.go`）+ `TestLocalSelfIdentityRejectsPersistedLineageReplayAndSymlink`、`TestLocalSelfIdentityRejectsPersistedIngressReplacementSymlinkReplayAndABA`、`TestLocalSelfIdentityIngressDriftIsCoreEvidenceFailure` —— ingress 前对象被替换/重放的拒绝。
- `TestPathSoakBridgedRuns` 断言 `worker.completed` 恰 1 条（journal 层无第二业务事实）。

组件级：

- `internal/recovery/recovery_test.go` `TestMatrix_LostResponse`（terminal-success → resume，Inspect 重建不重放执行）。
- `internal/resultingress/ingress_test.go` `TestAdmit_IdempotentReplay`、`TestAdmit_ReplaySameKeyDifferentDigest_IsForgery`、`TestAdmit_SequenceMonotone`；`recheck_test.go` `TestReplay_HotPathIdempotent`、`TestReplay_ColdPathIdempotent`、`TestRecheck_HotColdSharedLedger`。
- runstore journal 幂等：`TestAppendRejectsStaleSequenceAndDuplicateEvent`、`TestControlSequenceAndRecordIDConflicts`。
- soak effectsink：admit→replay idempotent no-op→post-revoke reject（`soak.go` 内嵌 invariant）。
- `internal/spine/spine_test.go` `TestRun_IdempotentReplay`（组件级）；`internal/goal/budget_test.go` lost-response replay（Goal 属 1.x，非 v1.0 链路）。

**缺口**

- 无 happy-path 级 fixture：「worker-result.json 已产出但 Run 在结果 handoff 处丢失响应 → 重进 Run 经 Inspect 直接消费既有结果并正常走完 verify 链（不 Blocked 补偿、不重启 worker、journal 无双写）」。现有路径级证据全落在「崩溃→Blocked 补偿」语义上。
- ResultIngress 未接入真实链：grep 证实 `resultingress` 仅被 `resultbinding`、`spine`、`perfbench` import；`execution`、`sandboxbridge` 均不 import。`sandboxbridge/admission.go` 只用 `resultbinding.AdmitWorkerResult` 落 admission anchor，ADR 0052 §1.4「所有结果接纳前 current-ledger recheck」在真链上无 Ingress 层重放/伪造实验。

## 4. stale / replayed result（旧 generation / 重复投递隔离）

**已有测试**

路径级：

- `internal/execution/service_test.go` `TestOrphanedRecoveryIsolatesStaleFencingWorkerResult` —— 接管 attempt 已启动后，旧 attempt 迟到结果仍被隔离；`TestOrphanedRecoveryQuarantinesStaleOutputsAndCompletesTheChain` —— stale outputs quarantine + evidence glob 仅见新 attempt；`TestCanonicalRunReplacementAfterQuarantineCannotReceiveOutcome`；`TestQuarantineIsImmutableAndRejectsUnsafeSources`。
- `TestRunDispatchAdmissionRejectsStalePresentationsBeforeProbe` —— sealed stale DispatchLease presentation 在 probe 前拒绝。
- `TestRunRejectsResultWhenEdgeExpiredOrLeaseInactive`。

组件级：

- `internal/recovery/recovery_test.go` `TestMatrix_StaleResult`（quarantined）、`TestMatrix_DuplicateDelivery`、`TestDecide_ReconcileCrossCutting`。
- `internal/resultingress` `TestAdmit_StaleGeneration`、`TestAdmit_IdempotentReplay`、`TestAdmit_ReplaySameKeyDifferentDigest_IsForgery`、`TestAdmit_StaleLease_LedgerExpired/DRCExpired`；`recheck_test.go` `TestForgery_HotPath/ColdPath`。
- `internal/resultbinding` `TestAdmitWorkerResultReplacedGeneration`、`TestAdmitWorkerResultLiveTerminatedRejected`。

**缺口**

- `sandboxbridge/execchain_integration_test.go` 只有 happy path + anchor 存在性；无「fake agent 提交旧 generation/重复 worker-result → `resultbinding.AdmitWorkerResult` 拒收 → anchor accepted=false → Run fail-closed」的负例（anchor 的拒绝分支无测试）。
- supervise 重派后旧 driver 迟到 delivery（dup-transport 经真实投递信道）无路径级 fixture。

## 5. binding / lease drift（撤销替换 / 过期 revoked）

**已有测试**

路径级：

- `internal/execution/service_test.go` `TestRunAcceptsResultWhenEdgeRecheckPasses`、`TestRunRejectsResultWhenEdgeRevoked`、`TestRunRejectsResultWhenEdgeExpiredOrLeaseInactive` —— typed-edge（DispatchResultCapability）结果接纳前 recheck，revoked 结果不得持久化、进 quarantine、进 audit trail。
- `TestRunDispatchAdmissionAcceptsFreshBinding`/`...RejectsStalePresentationsBeforeProbe` —— DispatchBinder seam 的双 binding。
- `TestStructuralFailureRunLeasePreventsConcurrentRelaunch`（run lease 互斥）。

组件级：

- `internal/bindingcheck/checker_test.go` 全套（revoke/expire/replace/generation ahead·behind/双侧替换/零值 fail-closed）、`ledger_test.go`；`internal/attemptgate`（revoke/expiry/replace/边界混淆），`internal/revokedrain`（security-critical 立即 + drain deadline fence + `TestSetLeaseDigest_Immutable`）+ `binding_test.go` 的 authority tuple drift/cross-port replay。
- `internal/resultingress` `TestAdmit_RevokedDRC`、`TestAdmit_StaleLease_*`、`TestRecheck_ColdPathRevokedBinding`。
- `internal/recovery` `TestDecide_BindingLost`、`TestDecide_ExecutingWithDeadLease`、`TestDecide_LeaseDeadQuiescedByTerminal`。
- runstore lease 组件测试（`TestLeaseIsExclusive`、`TestAcquireRejectsUnsafeOwnerWithoutMutatingTarget`、`TestLeaseCannotWriteAnotherRun` 等）。

**缺口**

- `bindingcheck.Checker`/`attemptgate`/`revokedrain` 均未接入生产路径（`execution`/`sandboxbridge` 不 import，ADR 0052 §3 亦将其标 COMPONENT）；真链上 binding drift 拒绝证据仅靠 authority edge recheck + dispatch binder lease fencing 两条 wired seam。
- security-critical revoke → generation bump + kill 在途 allocation 的演练无路径级证据（revokedrain 仅组件级状态机）。
- 新 gate 下 binding-lost 变体无测试（见 §1 缺口三）。

## 新门禁 fault injection 现状专题

- `supervisor.recoveryDecision`（`internal/supervisor/supervisor.go`）：admit / ambiguous-side-effect-skip 两分支有新测试，且断言 argv 与「绝不 spawn」。缺：decision-unavailable 分支（journal 损坏）、bindings/stale-result 输入变体、`explain.Assemble` 装配失败的端到端 skip 文案。
- `recoverTakeoverAdmission`（`internal/cli/explain.go`，wiring 在 `cli.go`）：`TestRecoverTakeoverAdmissionMatrix` 直调函数 3 分支 + `TestExplainRunRendersRecoveryDecision` 渲染。缺：经 `cli.Run` 入口的 `task run --recover-dead-driver` 在 side-effect run 上 fail-closed 的整条 CLI 链路测试。
- 两个 gate 的 fault 输入都依赖 `explain.AssembleWithStaleness` 装配（supervisor 用同 staleness 窗口保证单调）；装配层本身只有 3 个测试（`internal/explain/explain_test.go`），anchor 缺失/损坏路径只在 deriveBindings 静默 fallback，未见测试。

## 缺口 TOP5（优先级排序）

1. **真实进程 kill 中段注入的端到端恢复**：起真实 driver/worker（fake marshal binary re-exec，仿 `detach_test.go` helper 形态）中段 `kill -9`，随后 `supervise --once`/`--recover-dead-driver` 接管，且必须经 lease owner 死活探针而非纯陈旧窗口判定。建议位置：`internal/cli/supervise_test.go` 或新建 `internal/cli/fault_injection_e2e_test.go`。复杂度：大。
2. **happy-path lost response fixture**：worker-result 已 durable 但响应在 handoff 处丢失，第二次 Run 经 Inspect 直接消费既有结果完成 verify 链（不重启 worker、journal 无双写）。现有证据只覆盖「崩溃→Blocked 补偿」。建议位置：`internal/execution/service_test.go`（新增 `BeforeResultHandoff`-式 seam）或 `internal/execution/worker_runner_test.go`。复杂度：中。
3. **marshal-server 跨进程 restart recovery**：新起一个 server 进程复用同一 state root，能查询并对既有 Run 恢复/取消（ADR 0052 §1.7 直接要求 loopback「恢复真实 Run」）。建议位置：`cmd/marshal-server/main_test.go` 或 `internal/server/e2e_test.go`。复杂度：中。
4. **ResultIngress 层接入真实链后的 stale/replay/伪造负例矩阵**（R2 合同 + §1.4 recheck）：当前 resultingress 不进 execution/sandboxbridge；anchor 拒绝分支无测试。建议位置：`internal/sandboxbridge/admission.go` + `execchain_integration_test.go`（旧 generation、重复投递、revoked binding、同 key 异 digest 各一例）。复杂度：中。
5. **新门俩 gate 的 fault 域扩展**：`recoveryDecision unavailable`（journal 损坏）、admission anchor 缺失/被替换（`deriveBindings` → binding-lost skip）、CLI 入口级 `task run --recover-dead-driver` side-effect fail-closed。建议位置：`internal/supervisor/supervisor_test.go` 尾部 + `internal/cli/explain_test.go`。复杂度：小。

附注：（a）spine 包内已有较完整的 crash-window/部分输出/quarantine 审计故障矩阵（`internal/spine/faults_test.go`），但 spine 不在生产链上，属 COMPONENT 资产，可直接复用其注入思路迁移到 execution/sandboxbridge 层；（b）真实 pi canary（`TestRealPiExecChainCanary`）默认 env-gated 跳过且仅 happy path，对 §1.8 五类故障不构成证据。
