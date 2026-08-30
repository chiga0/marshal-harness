# S2′ 组合根生产链配方（implementation research）

更新：2026-08-30（`main@46ff71a` 之后）。本文固化从现有 resultingress/runstore/dispatch 权威 API 逆向出的 S2′ producer chain 精确调用序列，供组合根实施切片直接使用；不改变任何合同或成熟度判定。

## 架构落点（已经 architecture_check.py 核实）

- `internal/productionruntime → resultingress` 无条件放行；`DurableRunAuthority` 实现必须落在 `internal/productionruntime`。
- productionruntime 可 import：`runstore`、`resultingress`、`dispatch`、`launchidentity`、`allocationcontrol`、`authority`、`application`、`canonical`、`domain`（均不在 FORBIDDEN 清单）。
- productionruntime 不可 import：`execution`、`planning`、`processcontrol`、`processsupervisor`、`sandboxbridge`。因此 `ProcessBridge`（supervisor 机制链）只能在 CLI 层实现（`internal/cli` 对 `processsupervisor` 有冻结债务边）。
- `internal/cli` 的冻结债务边：execution/planning/processsupervisor/sandboxbridge（直连）+ app→execution/sandboxbridge + resultbinding→resultingress + runstore→resultingress（间接）。**不得新增条目**。

## DurableRunAuthority 四方法实现要点

- `CurrentOwner`：在 controller 传入的 borrowed verifier 内 `ingress.OpenOwner(scope)`，要求 `state.Acquisition == acquisition`；`PendingRecovery` 从 Run 侧推导：`ReadRunStartAuthorityUnderLease` 处于 RUNNING（attempt 在途、无终态）记 1，否则 0（与 ADR 0056/0067 的崩溃恢复语义一致）。
- `RehydratePreparedRunStart(digest)`：`ingress.ResolvePreparedExecution` + `PrepareMacRunStart`（已存在，直接组合）。
- `RehydrateRunStartOutcome(digest)`：`runstore.ReadRunStartAuthorityUnderLease`；当 Run 处于 RUNNING 且 `PreparationDigest == digest` 时返回该投影（Sequence=journal head）。
- `InspectRun`：同一 Read 投影，校验 `RunID` 一致。

## PrepareRunStart 生产链（按序，全部在 owner lock 内）

1. `runstore.ReadRunStartAuthorityUnderLease` → 要求 READY、`AttemptID == ""`、`Sequence/AuthorityHead` 与 request 一致；构造 `resultingress.ReadyRunAuthority`（照抄 `productionruntime/zero_attempt_side_effect_darwin_test.go` fixture 的字段映射）。
2. `ingress.ReserveAttempt(ctx, runVerifier, ready)`（creation-once，内部 `domain.NewID("attempt")`，按 `reservationKey(ready)` 幂等重放）。runVerifier = `runstore.NewAttemptRunAuthorityVerifier(runs, lease, namespace, orchestratorID)`。
3. **dispatch lease 铸造（关键前置）**：`AttemptIdentity.Validate()` 强制要求 `AllocationID/LeaseID/LeaseDigest/DispatchGeneration≥1/FencingTokenDigest`；`DispatchLease` 需要完整 provider 注册链（`RegistrationId`、`ProviderCapabilitySnapshotDigest`、`provider.Attestation`）。fixed CLI 的 local-dogfood 路径必须先走 ADR 0018 的 registration→snapshot→claim 流程（`dispatch.LeaseLedger.AppendClaim`）才能取得身份字段。**这是组合根的最大未实施前置**：需要 provider 注册与 capability snapshot 的真实铸造（`internal/agentregistry` + `internal/cli` 现有 registration 代码可复用），或 ADR 层面对 local-dogfood 定义 embedded lease profile。
4. `ingress.OpenReservedAttempt(ctx, runVerifier, reservationDigest, identity)`（fresh v2 open）。
5. allocation provision：`resultingress.NewAllocationAuthority(ingress, provisionVerifier, cleanupVerifier)` → `CompareAndAppendAllocationProvisionIntent(ctx, identity, generic, typed)` → `WithCurrentAllocation(effectKey, session)` 内 `Snapshot → AppendProvisionPrepared → AppendProvisionReceipt → ProjectAndReconcile`。intent/prepared/receipt 构造配方见 `effect_authority_test.go:158`（appendTestAcceptedProvision）与 `allocation_authority_test.go:362-452`；`DeriveAllocationEffectIdentity`（resultingress）+ `allocationcontrol.DeriveRelativeNames`。provision verifier = Core 自身（`WithCurrentAllocationProvision` 校验 check 绑定当前 lease/namespace 后放行 Core SecurityDomainId）。
6. launch authorization：`ingress.CompareAndAppendAuthorized(ctx, runVerifier, revision, head, AttemptAuthorizationRequest{Identity, CurrentRunAuthority}, AttemptTransition{Kind: LaunchAuthorized, LaunchAuthorizationID, LaunchClosure})`。closure 由 CLI 层 `launchidentity.Seal` 对真实 Pi bytes 密封后传入（productionruntime 不做文件密封）。组合根需实现 `resultingress.CurrentRunAuthorityVerifier`（持 run lease，校验 READY 投影与 `runAuthorityBindingFor(identity)` 一致）。
7. `ingress.CreatePreparedExecution(ctx, ownerVerifier, acquisition, PreparedExecutionCreation{Identity, ExpectedRunSequence: ready.Sequence, ExpectedRunAuthorityHead: ready.AuthorityHead})`。
8. `PrepareMacRunStart` 投影返回 `application.PreparedRunStart`（controller 校验 RunID/Sequence/AuthorityHead 与 request 一致 + durable rehydrate 等值）。

## Commit 侧（bridge，CLI 层）

`execution.Input{PreparedRunStart, CommitPreparedRunStart}`；commit 回调内：supervisor bootstrap/start → process-started checkpoints → resume checkpoint（`AppendSupervisorCommandIntent/Outcome`，经 productionruntime 暴露的窄方法写 ResultIngress）→ `runstore.WithPreparedRunStartAuthority(lease, prepared, projector)` 内调 `ingress.CommitMacRunStart`（proof 铸造 + sealed successor 追加）。

## 已知开放问题

0. **4b-2 当前边界（实测，a106de1）**：真实 0.84.4 镜像经 `OpenPi0844` 密封成功（SHAPE 全过）、`PrepareRunStart` 全链通过、`StartPreparedRun` 进入 sealed reconcile（preparedDarwin 确认）。reconcile 的 `verifyPreparedCurrentSourcesLocked → VerifyCurrentClosure(closure, allocationLive)` 要求 closure 的 WorkingDirectory 的 live 身份 == allocation receipt 的 live 身份；staging provision 的 live 目录是 allocation store 下新建目录，与 closure 冻结的 worktree 不同。**修复方向（ADR 0069 existing-worktree 绑定）**：provisionAllocation 改用 `ExistingWorktreeController.Bind`（resultingress.NewExistingWorktreeAuthority + allocationcontrol 控制器），使 bind receipt 的 live 身份 = Run worktree；同时 `derivePreparedExecution/currentPreparedProvisionReceipt` 需接受 existing-worktree bind receipt 作为 provision 证据（或按 ADR 0069 定义等价投影）。这是 S1 冻结校验的扩展，实施前须对照 ADR 0069 文本确认投影形态。
1. lease 铸造路径（上文第 3 点）——已由 `dispatch.MintClaimedLease` + CompositionLedger.ensureAttemptLease 实现并验证。
2. 旧测试迁移在 4b-2 完成后按 compatibility profile 口径一次性执行。

1. lease 铸造路径（上文第 3 点）——唯一阻塞 PrepareRunStart 落地的缺口。
2. `Digest`/`FencingToken` 的具体铸造约定需对齐 ADR 0069 的 sealed-successor 单次预算消费语义。
3. 旧测试迁移（internal/cli 18 项 + internal/execution 约 20 项）在组合根合入后按 compatibility profile 口径一次性完成。
