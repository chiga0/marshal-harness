# 设计审计报告

## 2026-09-01：`v1.0.0-rc1` same-bytes 发布终验与失败复盘

[`v1.0.0-rc1`](https://github.com/chiga0/marshal-harness/releases/tag/v1.0.0-rc1) 已完成 ADR 0068 的 local-dogfood prerelease distribution exit。annotated tag object `e99326fa6b6e57a19db8d6404c56b3dcf396fdc7` 精确指向 sourceHead `c1407bd77924c97dc6784f4d81938a3f0bfa75f6`；candidate SHA-256 为 `f9ed7fa59d05f5e71fef7164b8015240497e1d18e25ef1d3f8e199c1378a3774`。真实 Pi `0.84.4` canary run `33504020360` 与 finalize run `33504247271` 形成单 Run/单 Attempt、9 项 Gate、独立 Verification/ReviewDecision、`ACCEPTED`、current receipt/carrier；receipt digest 为 `sha256:7bd5b500bbaff5c5b008922b713d9844b792a3e82ece4e4a46ccd837496b4525`。candidate exact-head CI run `33502847249` 的 Ubuntu、macOS 与 secret scan 全绿。

release workflow run `33506656403` 的 Admit 和 Publish 全绿，只消费 finalize carrier，不重建、重签、strip 或改写 candidate。GitHub release 外部下载的 candidate SHA-256 仍为 `f9ed7fa5…a3774`；在独立临时目录通过 exact tag + preview opt-in 安装后，`marshal version --json` 精确返回 version `1.0.0-rc1`、commit `c1407bd`、build date `2026-09-01T11:30:25Z`、Go `1.26.6`、`darwin/arm64` 与 `darwin-local-dogfood`。tag ruleset 只在创建该 exact tag 时加入临时精确 exclusion，推送后立即恢复为 active、零 exclusion；未覆盖或改写远端历史。

发布前发生三次确定性失败，candidate/tag/carrier bytes 始终未变：

1. run `33504766816`：Admit 在 carrier checker 通过后，又把四成员 carrier 交给只接受三成员 dist 的 `verify-rc1-dist`。修复为单一 `verify-candidate-tag` RC1 路由，删除重复且语义冲突的门禁。
2. run `33505577882`：Publish 把 candidate sourceHead 与 GitHub artifact workflow revision 错误地当成同一 SHA。修复为分别绑定 artifact name/candidate SHA 和 `workflow_run.head_sha`/workflow SHA，并补 cross-workflow replay 负测。
3. run `33506131715`：workflow 来自新 main，但 Publish checkout 到 RC1 tag 后实际执行了 tag 内旧 validator。修复为候选源码与 validator 双 checkout；validator 精确绑定 `${{ github.sha }}`，candidate contract 仍从 exact tag 执行。随后使用该失败 run 的真实 artifact 在本地完整预演 metadata→archive→payload→receipt→carrier→tag/binary 全链，才进行最终发布。

复盘结论：这三次往返不是 Agent 或 candidate 质量问题，而是发布测试只覆盖 helper 单体，没有在“workflow source revision 与 checked-out candidate revision 不同”的真实 GitHub workspace 语义下执行全链。后续 release 变更必须在 dispatch 前使用一个真实历史 artifact 或等价 closed fixture 完成端到端 Publish preflight，并显式列出 candidate identity、transport workflow identity、validator identity 三个不同主体；禁止重复校验器、隐式 checkout 版本和同名 `sourceHead` 混用。该学习已固化为 fixed workflow digest、双身份 hostile/replay 测试和 exact validator checkout。

本终验不改变 ADR 0068 的负向声明：RC1 不是 ADR 0052 的 `RELEASED`，也不是 production、managed、notarized、hardened、server、Linux 或 stable release。`I186-R2–R5` 保持 `IN_PROGRESS/COMPONENT`；`I186-R6` 更新为 `IN_PROGRESS/COMPONENT`，下一主线是 fixed server/recovery fault matrix、Issue #212 signing/notarization、Linux stable 与受保护 stable candidate。

## 2026-09-01：ADR 0073 activation V2 跨 runner 证据模型审计

GitHub RC1 canary 已把当前发布阻塞收敛为一个可复现的证据模型缺口：run phase `33477653933` 用 build-once candidate 和真实 Pi 到达 `REVIEW_PENDING`，finalize `33477984364` 在另一台 macOS runner 上失败于本地身份 binding。根因不是 Agent、ResultIngress 或 ReviewDecision，而是 V1 同时把临时文件系统的 `device/inode` 当作跨阶段稳定主体；以相同 `activationId` 重签发只能产生新的 activation digest，不能建立权威连续性。

审计后接受 [ADR 0073](adr/0073-dogfood-activation-v2-host-portability.md)，并把实现边界收窄为同 canonical 布局的 ephemeral runner：activation 与 portable `identitySubjectDigest` 绑定 repository/root/path/size/hash/sourceHead/profile，但不绑定 PID/device/inode/time；每台宿主的新 observation 仍必须以 held fd 和 pathname recheck 强制验证 device/inode、size/hash 与 ABA。activation→observation→attempt/applicability→verification→review 整条 lineage 同步升级 V2，V1/V2 混用 fail closed；RC1 finalize 必须原样消费 run artifact 的 activation，禁止以相同 ID 重签发或延长。

本地证据包括 selfidentity 正向/负向与跨 host-object subject 测试、CLI/execution/runstore/productionruntime/planning/control/verification 定向测试、Schema tests、RC1 shell contract、architecture check、`go vet`、staticcheck 与 diff-check。全仓本地 `go test ./internal/...` 仍会在本机企业终端策略下卡住 Codex/OpenCode/Pi 临时 Go 测试二进制并产生 context deadline，不能冒充全绿；双平台全仓结果必须由 exact-head required CI 提供。finding 状态为 `CONTRACT-AND-IMPLEMENTATION-CLOSED / REMOTE-CANARY-OPEN`：只有新的 V2 run/finalize 达到 `ACCEPTED` 并产出 receipt/carrier 后，才能进入 RC1 publication workflow 收口；R1–R6 暂不升级。

## 2026-08-31：生产级 Agent Team 架构终审

新增 [《Marshal 生产级 Agent Team 架构终审》](production-agent-team-architecture-audit.md)，以 `main@10f743d93cdaa71a2a3b181da3134f4a2c5dbe87` 为代码快照，交叉复核 production import graph、491 个本机 dogfood Run、Git/CI/Issue 历史，以及 Anthropic、OpenAI、Temporal、Kubernetes 与 GitHub 的一手生产资料。

终审结论：Marshal 的确定性 Kernel、唯一 authority ledger、ResultIngress、独立 Verification/Review 与 effect reconcile 方向成立，不应重写；当前欠缺的是 `Intent → Discovery → DeliveryProposal → UserApproval → WorkGraph → Integration → Outcome` 的真实产品闭环，而不是更多横向 Provider 或更细的底层合同。当前 fixed CLI real-Pi `ACCEPTED` 证据只证明 single-task kernel，不构成生产 Agent Team。路线保持先完成 ADR 0068 RC1；RC1 后第一条纵切应是 simple prompt → approved proposal → one real Task 的 GoalLite walking skeleton，随后才用最多 3 个并行节点和一个 Integration Node 证明真实加速。

审计快照的 required CI 仍为失败：Ubuntu 有一项 server recovery 测试 600 秒超时，supervisor 旧 fixture 仍绕过 sealed Run-start proof，macOS quality 因矩阵失败取消；因此该快照明确不具备 release candidate 资格。RC1 后应把既有 stable hardening 合同与 Agent Team 产品纵切拆成两个独立验收轨道，避免任一方向再次以组件数量遮蔽真实出口。

该终审同时记录了本机 Run 的效率基线：`ACCEPTED=117/491`、`BLOCKED+REJECTED=291/491`、非终态 `68/491`、多 Attempt `162/491`、`review.rework=211`，且历史 `worker.failed` 只有 `19/211` 具备可用于自动止损的 typed 信息。上述数据来自单机自举，不能外推为行业成功率，但足以说明下一阶段应把 plan/spec/environment 缺陷前移到 Worker 启动前，并用真实 outcome、first-pass、successor amplification 和并发 wall-clock speedup 取代 PR/ADR/组件数量作为进展指标。

## 2026-08-31：S2′ path B existing-worktree production-composition 切片

在 `feat/pi-s2-production-composition@d65785d` 基线上，[ADR 0069](adr/0069-attempt-reservation-and-existing-worktree-allocation.md) 冻结的 S2′-B（existing-worktree 绑定）此前只有 resultingress 层的 RB1 closed-union 与 PreparedExecution path B 投影，productionruntime 组合根与 fixed CLI 仍走 path A（staging provision），导致 closure WorkingDirectory 与 allocation receipt live 身份不匹配。本切片落地真实 path B 生产纵切，不放宽任何强制门禁：

1. 新增 [ADR 0070](adr/0070-existing-worktree-frozen-inputs-digest.md)（Accepted），冻结 `existing-worktree-binding/v1` 的 `FrozenInputsDigest`（canonical JSON closed struct {specDigest, policyDigest, capabilityDigest} 的 sha256）、`RepositoryOwnerDigest`（exact current `ControlOwnerState.FactDigest`）、`ExpectedAttemptSequence`（bind admission 前当前 `AttemptAuthorityState.Revision`）派生口径；明确 `BaseSHA`/`WorktreePath` 已在 `ExistingWorktreeBindRequestV1` 绑定不重复入 digest，且不使用 `ReservationKeyDigest`。不扩大协议。
2. `internal/productionruntime/existing_worktree_bind.go`：`CompositionInputs`/`CompositionLedger` 接收 held `ExistingWorktreeDescriptorGraphV1` + 目标 worktree `*os.File`，单边配置 fail closed；`bindExistingWorktree` 在 `BindOwnerToAttempt` 后用 `resultingress.NewExistingWorktreeAuthority` + `allocationcontrol.NewExistingWorktreeController` 完成 Bind，Run descriptor 用 `runstore.DupRunDirectory` + `allocationcontrol.NewDescriptorBoundRunV1`；binding 使用 ownerState.FactDigest、reservation.ReservationFactDigest、bound.OpenedDigest、identity lease/allocation/fencing、冻结输入 digest、bound.Revision。Core 侧 `existingWorktreeCurrentVerifier` 在打开 `RunAuthority` 前从 durable READY 投影与当前 owner/Attempt 派生 immutable expected current，在 callback 内重验 exact current owner 与 Attempt head/revision，不再在持有 `RunAuthority` RLock 时 `ReadRunStartAuthorityUnderLease`。replay gate 接受 staging provision receipt 与 existing-worktree bind receipt 的严格 closed union；path B 不把 closure 重封到 staging。
3. `internal/cli/existing_worktree_graph_darwin.go` + `sealed_ready_darwin.go`：fixed CLI 构建并持有 repository descriptor graph + exact `projection.WorktreePath` target，支持 `.git` 为目录或 linked-worktree `.git` 文件；固定 `/usr/bin/git --git-common-dir` 仅作 locator，最终由 `NewExistingWorktreeDescriptorGraph`/`NewLinkedExistingWorktreeDescriptorGraph` 与 held descriptors 校验；所有句柄生命周期覆盖 ComposeRuntime/Prepare/Start，退出关闭；改用 `AcquireExisting`（ADR 0069 §4 existing-only）。不生成或执行临时二进制。
4. 定向测试：`FrozenInputsDigest` 字段漂移、path B bind receipt 到 PreparedExecution、replay 无 sibling、held target identity drift 拒绝且无新增 bind authority、单边 inputs 拒绝、path A staging 回归不变，以及 fixed CLI 对 linked repository graph 与 Darwin symlinked target path 的覆盖。真实 Pi canary 必须等待 attempt-aware argv 与 exact-owned terminalization 接线，禁止用宽泛 `pkill -f` 代替生命周期控制。

本切片不改变信任边界（仅 ADR 0070 澄清字段口径），不暴露 raw path 到 prompt/transcript/public schema，不引入 `productionruntime → adapter/pi` 或 `processsupervisor` 新依赖，不使用 `ReservationKeyDigest` 作为 `FrozenInputsDigest`。`architecture_check.py` 通过。`I186-R2–R5` 仍为 `COMPONENT`，不据此宣称 RC1、stable 或 production 已完成；real Pi canary、terminalization、独立 Decision ACCEPTED 与 same-bytes RC1 仍是后继。

## 2026-08-30：S1′ ResultIngress Darwin 全绿切片（基线 11 失败 → 0）

在 `main@054789c` 基线上，`go test ./internal/resultingress -count=1` 于本开发机（Darwin 25/arm64）确定性失败 11 项。本切片逐项定位并修复，现该包非 race 全绿；判定依据与修复如下，未放宽任何强制门禁：

1. `TestPreparedExecutionCreationOnceResolveAndSecretBoundary`：落地即坏的断言——`DecodePreparedExecution` 是纯闭合 wire-form 校验，无法拒绝"重算过 `PreparationDigest` 的任意 Pi 身份"（文档自洽时结构校验必然通过）。改为验证 seal 覆盖 Pi 身份（篡改不重算 digest 即拒）+ 自洽文档只是纯 wire form + ungrounded digest 经 `ResolvePreparedExecution` 必返回 `ErrPreparedExecutionUnavailable`。原测试自 `6e558d7` 创建起从未通过过。
2. `TestCommittedRunStartProofIsNarrowSharedAndSynchronous`：测试竞态——`deactivateAndWait` 是否观察到 in-flight 回调取决于调度。以 `guard.active == false`（escaped 已计算后的确定性锚点）作为入场 barrier 修复。
3. `TestSupervisorReconnectFactIsRequiredBeforeBusinessHeadReanchor`：实现缺口——caller-authored Collect（rebuild 重锚业务头）在无 reconnect 事实时被允许追加。在 `validateSupervisorCommandIntentAgainstState` 的 Collect 分支补上 `SupervisorReconnectFactDigest == "" → ErrAttemptAuthorityOrder` 门禁；全部现存通过用例均已有 reconnect 在先，无行为回退。
4. `openHeldDarwinAuthorityFiles`（6 项 HeldDarwin + Seal）：Darwin 25 APFS 的目录 `st_nlink` 计入常规条目，先冻结目录身份再创建 ledger/coordination 文件导致 `verifyCurrentNames` 的 Nlink 相等性永远失败，`OpenDarwinResultIngressStore` 在本机 OS 上不可达。修复沿用 owner-lock 既有"entry stable 后再冻结"模式：创建条目并 fsync 后对同一 directory object 重冻结身份。
5. `processExecutablePath`：`kern.procargs2` 返回未解析 exec 路径（macOS `/var` → `/private/var`），而后续 `openObservedSpec` 以 `O_NOFOLLOW_ANY` 打开必然失败。自观察现以 `filepath.EvalSymlinks` 解析为规范路径——`BinaryIdentity.CanonicalPath` 语义收紧为真实磁盘位置，调用方必须传规范路径（`ObserveCurrentCore` 比较因此更严格，非放宽）。
6. 测试 fixture 修正：`TestPreparedDarwinSeal` 的 `ObservedAt` 改为从真实进程生日派生（原硬编码 `2026-08-29T00:00:00Z` 是时间炸弹，晚于该日的任何运行必失败于 "precedes process birth"）；`advancePreparedAttemptToStarted` 的进程观察从 prepared Pi closure 的 `RuntimeExecutable` 派生（`AppendProcessStarted` 的 `processMatchesRuntime` 要求绑定该精确 runtime object）；`testPreparedSupervisor` 优先复用已绑定 owner（supervisor bootstrap 属于已绑定 owner 的主流程，epoch 轮换只属于显式 reconnect 恢复场景，此前无条件轮换使 prepared 文档 owner 绑定失效）。
7. Makefile `test` 目标注入真实 git head ldflags：`BinaryIdentity.SourceHead` 的 hex40 合同使未注入 commit 的 test binary（`commit="unknown"`）无法通过自身份校验——这是本机与 CI macOS quality 失败的直接原因之一。`make test` 现与 release 构建绑定同一 source head。

残留（均经 stash 验证为 `main@054789c` 既有，非本切片引入）：`internal/processsupervisor` 8 项 Darwin mechanics 失败（`process-supervisor-intervention-required` 等，HEAD 复现）；`internal/productionruntime` 2 项 owner-lock ABA 在全包上下文 flaky（单跑 10/10 + `-race` 单跑通过，结合《v1.0 Release Readiness》"Mac 本地质量门禁边界"所述企业终端按新 Mach-O/CDHash 拦截 test binary 的策略，本地结果不作为证据层级）；`TestHeldDarwinResultIngressUnlocksAfterPanic` 在 `-race` 下 flaky（HEAD 同样复现）。`R2–R5` 成熟度不变，仍为 `COMPONENT`；组合根接线（`owner → Attempt/ResultIngress → allocation → exact runtime → PreparedRunStart/Commit → execution.Run`）与旧 CLI/execution 测试迁移仍是下一切片。

**后续切片 2 收口（`3cc88ab`）**：processsupervisor 的 8 项失败全部由测试 fixture 缺陷造成，实现未放宽任何门禁——`digest()` helper 对多字符 label 产出超长非法 digest（7 项失败 + 2 项负向测试因错误原因通过），改为按 label 哈希生成合法 64-hex；spawn source gate fixture 未解析 `/var → /private/var` symlink 祖先即以 `O_NOFOLLOW_ANY` 打开，改为先 `EvalSymlinks`。该包现含 `-race` 全绿。productionruntime 2 项 ABA 全包 flaky 与 resultingress race flake 维持环境类判定不变。S2′ 组合根的架构落点已核实：`architecture_check.py` 对 `productionruntime → resultingress` 无条件放行，组合根 authority 实现必须置于 `internal/productionruntime`；`internal/cli` 的冻结债务允许其直接 import `execution/planning/processsupervisor/sandboxbridge`，supervisor 链的 ResultIngress 追加须经理 productionruntime 暴露的窄方法，不得新增冻结债务条目。

## 2026-08-30：切片 4b 迁移的单一前置——Pi 0.84.3 字节身份（fixture 可行性结论）

组合根工程（`97e07d0`…`dcdc494`，15 提交）已使 sealed 链在 darwin/arm64 端到端可用：CLI `executeRun` READY 分支（`e408d3b`）经 `ComposeRuntime` 驱动 `PrepareRunStart` 全链与 `StartPreparedRun` 的密封机制；TestMain 继承探测（`dcdc494`）使重入的测试二进制运行真实 supervisor 循环。

切片 4b 的 RUNNING 起点 fixture 经逐层核实被确认**与 canary 同源阻塞于 Pi 0.84.3**，不是缺失代码：

1. `derivePreparedExecution` 经 `Pi0843IdentityFromClosure` 强制 closure 为结构精确的 Pi 0.84.3（55 材料、每根精确字节数、entrypoint digest 固定为 `piEntrypointDigest`）；
2. `verifyPreparedCurrentSourcesLocked` 经 `VerifyCurrentClosure` 观察真实文件——合成/临时路径必然 fail-closed；
3. `OpenPi0843` 对本机 `/opt/homebrew/bin/pi`（0.83.0）按同合同拒绝。

因此 38 项旧测试（internal/cli 18 + internal/execution 约 20）的 READY→RUNNING 段迁移到 `ComposeRuntime → PrepareRunStart → StartPreparedRun` 的执行，在维护者升级 Pi 至 0.84.3 之前无法在本机验证；迁移模板（fixture 输入、TestMain 继承探测、其余 verify/review/publish 断言原样保留）已就绪。升级完成后，执行顺序为：sealed fixture helper 落地 → 38 项迁移 → 远端 CI 绿 → 真实 Pi canary → 独立 ReviewDecision ACCEPTED → same-bytes canary/carrier/tag → v1.0.0-rc1。R2–R5 成熟度不变，仍为 COMPONENT。

## 2026-08-30：READY→RUNNING 回归证据

在 `main@2fb2d58` 上运行完整 `go test ./internal/cli -count=1` 时，多个既有 CLI E2E 在 Attempt 创建前统一失败于 `READY to RUNNING requires sealed Run-start proof`。这确认当前 sealed Run-start 门禁已正确阻止未接线的 production composition，但也暴露出旧 Local MVP 测试仍假定可直接追加 `worker.started`；`runstore.Store.Append` 已明确拒绝该路径。该结果不能通过放宽门禁或伪造 `PreparedRunStart` 修复。

处置：保留该失败作为 `I186-ARCH-PREPARED-EXECUTION-AUTHORITY` 的实现证据；下一切片必须一次性完成真实 `owner → Attempt/ResultIngress → allocation → exact process/allocation runtime → PreparedRunStart/Commit → execution.Run` 组合根，并同步迁移仅覆盖 compatibility profile 的旧测试。当前 `R2–R5` 仍为 `COMPONENT`，不产生 RC1 或 stable 发布资格。

## 2026-08-30：Run-start producer seam 架构复核

`main@a6482db` 合入了 `resultingress.PrepareMacRunStart`/`CommitMacRunStart`。该 seam 仅在当前 owner lock 下重解析 durable `PreparedExecutionV1`，逐字段校验后委托既有 proof-producing `StartPreparedExecution`；它不创建 Attempt、owner、allocation 或 process facts，也不改变 `R2–R5: COMPONENT` 判定。同期删除了未接线且违反 architecture layer gate 的 `internal/productionruntime/factory.go` 与测试，避免无效 factory 形成第二 authority 入口。定向测试、architecture check 与 vet 已通过；全包 ResultIngress Darwin owner-lock fixture 的既有失败仍保持独立 blocker，fixed CLI production composition 与真实 Pi→独立 `ReviewDecision/ACCEPTED` 尚未形成。

- 审计日期：2026-08-04（2026-08-10 增补 Runtime 架构重置记录、首次 Sandbox SPI dogfood reject 增补记录与 Round 2 关闭记录；2026-08-11 增补 Control Plane 与 Provider Port 边界冻结记录，含 Round 4 独立评审八项 P1 关闭记录、Round 5 复核四项残留关闭记录、Round 6 复核两项残留——Control Plane authority namespace 与 Provider actor 域分离、typed cross-domain edge——关闭记录与 Round 7 复核三项残留——双键空间残留清除（权威对象 authorityNamespaceId 独占拥有、registration/snapshot/evidence authority ledger 事实、接纳关系归 authority ledger、controlPlaneId 逻辑权威身份）、Core-only typed edge 生命周期细化（issuer/source/target/operation/expiry/digest/revocation/replay/current-ledger recheck，issuer 恒为 Core 且不等于业务流 sourceActor、sourceActor/targetActor 按 edge 类型绑定，派生 token/handle 不得成为第二权威）、Public API 幂等/SSE/对象 key 修正为 authorityNamespaceId——关闭记录与 Round 8 复核一项残留——typed edge 跨域例外与适用范围（三类 typed edge 明确为 Provider actor 跨信任域访问默认拒绝的唯一 allowlist 例外，Public API/SSE 与 Core 内部权威引用无需 Provider typed edge）——与 Round 9 复核两项残留——跨域 fail closed 表述精确化（删除会无条件拒绝 MaterialAccessGrant 等合法 typed edge 的宽泛表述）、非 edge Port 与同域不自动授权（provider-registration/control 经 transport identity/该 Port AuthN/AuthZ/registration protocol 由 Core 写 authority ledger；securityDomainId 相同只是 provenance/partition 条件，不构成授权）——关闭记录；2026-08-12 增补 Issue #25 发布合并后 head reconcile 审计记录；2026-08-13 该 finding 随 typed reconciliation 实现合入关闭；2026-08-14 增补 Issue #53 CI 失败 rework 注入设计缺口审计记录，目标契约由 [ADR 0030](adr/0030-ci-failure-rework-evidence-and-injection.md)（Proposed，草案已提出/待接受）给出（接受后方冻结），实现待后续 implementation successor）

## 2026-08-29：S1′/S2′ producer chain 的 Attempt 与 existing-worktree P0

对accepted S1′ mechanics与S2′ production composition做真实producer预审后，发现两项不能由fixture绕过的P0：

| Finding | 严重度 | 当前状态 | 证据、影响与关闭条件 |
| --- | --- | --- | --- |
| `I186-RUN-ATTEMPT-RESERVATION` | P0 | `CONTRACT-ACCEPTED / IMPLEMENTATION-OPEN` | 首版ADR0069把reservation与`attempt-opened`混同。接受合同改为RB1 `attempt-reserved` active→consumed|cancelled、按RunID+exact READY seq/head lookup-before-mint；dispatch claim按reservation digest+RunID+reserved AttemptID lookup-before-claim并same-bytes replay，full identity与`attempt-opened`后置，budget三次读取held Run authority且只由sealed successor消费。定向修订 sourceHead `e2af179` 已独立 `APPROVE`（`P0=0/P1=0`）。关闭仍要求schema/protocol与并发/cancel/response-loss/legacy矩阵及fixed CLI producer通过；合同接受不关闭实现缺口。 |
| `I186-EXISTING-WORKTREE-ALLOCATION` | P0 | `ADR-PROPOSED / AGGREGATE-REWORK` | 首版把sidecar描述成独立allocation ledger，会形成第二authority。修订提案把Bind/Receipt/Release全部收回RB1 closed union；repository-global target uniqueness从held-owner下RB1 replay判定。固定sidecar仅为可重建projection，缺/落后先投影、损坏/超前fail closed，绝不能覆盖RB1。关闭要求aggregate rework独立复审、existing-only Run open、锁序/sidecar/crash/replay/ABA/secret/zero-target-mutation矩阵与真实Pi纵切。 |
| `I186-RESULTINGRESS-HELD-DESCRIPTOR` | P0 | `CONTRACT-ACCEPTED / IMPLEMENTATION-OPEN` | 当前ResultIngress store仍可能按pathname reopen authority对象，且`ObserveCurrentCore`不能先于`OpenOwner`产生current结论。这不是ADR0069新增信任决策：[ADR0066](adr/0066-production-composition-owner-acquisition.md)已经要求canonical held-descriptor边界。S1′ rework必须改held descriptor backend、拆分正确时序并证明path漂移时不打开替代对象；完成前不得接受sealed proof实现。 |

[ADR 0069](adr/0069-attempt-reservation-and-existing-worktree-allocation.md)首版`ebbfd86`独立审查为`P0=3/P1=4`，`1adf20c` aggregate复审只剩`P0=1/P1=1`；定向修订 sourceHead `e2af179` 已独立 `APPROVE`（`P0=0/P1=0`）并由维护者接受。以下结论仅记录 2026-08-29 当时状态：接受只冻结合同，尚未实现；R2–R5当时保持`COMPONENT`、R6当时保持`PLANNED/DESIGN`。当前成熟度以上方 2026-09-01 RC1 终验为准。

## v1.0 候选链审计 checkpoint（2026-08-28）

本 checkpoint 取代本文较早章节对“当前状态”的描述；旧记录保留用于解释偏航与修复历史，不得用其中曾经的 `DONE` 结论升级现行 Roadmap。

| 审计项 | 当前证据 | 判定 | 剩余退出门禁 |
| --- | --- | --- | --- |
| Pi fixed-bin strict E2E | Pi `0.84.3` 前置 canary 绑定 `sourceHead=d4b9647`，单 Attempt 通过 9 项 Gate，生成正式 ReviewPacket 并进入 `REVIEW_PENDING`。 | `PARTIAL-INTEGRATION/PASS`；它没有导入独立 ReviewDecision、没有进入 `ACCEPTED`，也不是当前 `main` 终验。 | 在最终主线重跑；由独立 reviewer 产生绑定精确证据的 Decision；通过 `task review --decision` 进入 `ACCEPTED`；重跑故障矩阵。 |
| Qwen workspace live | Qwen Code `0.22.0` ordinary-user workspace live adapter 已通过。 | `COMPATIBLE-ORDINARY`；不是 `LaunchCapable`，不能替代 Pi 的 production reachability 证据。 | 若未来升级 production profile，必须另行提供 Sandbox/authority 证据；当前不阻塞 v1。 |
| durable authority | ResultIngress admission→worker-result→Run journal 的 crash-atomic 持久化/恢复已随 `main@912f659` 合入。 | R2/R3 仍保持 `COMPONENT`：ADR 0056 terminalization barrier 尚未复用同一 authority transaction，当前主线 canary 也未覆盖 cleanup/restart 全矩阵。 | 把 terminalization CAS 接到 `912f659` 的唯一 transaction；补 restart/replay/stale/revoke/replace/expiry/cleanup 真实路径负测；证明无第二真值。 |
| server/controller selector | durable start/status/recovery controller 已随 `main@44ee8c9` 合入；production selector 已随 `main@d4b9647` 收紧到 `LaunchCapable`。 | controller 层已接线，但 ADR 0056 的 launch/process observation、admission/terminalization CAS 与 cleanup transaction 未接入，R4 保持 `COMPONENT`。 | server crash/lost worker/failed worker 跨进程恢复必须在 eligibility 立即 fence 后保留 cleanup binding，直到 `cleanup-completed`，并产生唯一 durable receipt/recovery decision。 |
| RC 与 stable release | ADR0068的Darwin arm64 CLI-only RC1合同已接受，但guard、canary与产物均未实现，尚未发布任何RC。 | 只可按S1′→S2′→Attach/rebind→terminalization→fixed CLI Pi+独立Decision `ACCEPTED`→same-bytes RC1推进；不能标`RELEASED`。 | RC1必须exact opt-in、缺资产不fallback且不自动activation；其后才推进server、Issue #212 managed signing/notarization与Linux stable gate。 |

当前 Mac-first RC1 关键路径按 ADR0067/0068 固定为：R2/R3 的 S1′→S2′→Attach/rebind → R4 terminalization → R5 fixed CLI Pi + 独立 Decision/`ACCEPTED` → R6 same-bytes RC1。server recovery、managed signing/notarization与Linux stable属于RC1后的stable后继。任何单次 live pass、候选 commit 或 reviewer verdict 都只是对应门禁的输入，不等于阶段关闭。

### ProcessBridge 启动权威接缝（2026-08-29）

代码审计确认 `application.PreparedRunStart` 只保存 ID、`READY` Run sequence/head 与 `preparationDigest`；`productionruntime.DurableRunAuthority` 没有 held `WithCurrentRunAuthority` 或唯一 `CommitRunStartOutcome`，`ProcessBridge` 也无法从 held Attempt authority 解析 current owner、完整 Allocation receipt、`launch-authorized`/`StoredClosureV1` 与 Pi identity。更关键的是，`process-started` 只证明真实 Node 已处于 `exec-stopped`，不是 `RUNNING`；只有 exact successful `resume` outcome 的 `state=running` 才能授权 Run lifecycle commit。现状同时无法证明启动瞬间 Node/entrypoint/material bytes 与 configured `PiProfile.IdentityDigest()` 闭合，也无法排除把 raw closure 复制进 Run/Supervisor ledger。

| Finding | 等级 | 状态 | 处置 / 关闭条件 |
| --- | --- | --- | --- |
| `I186-ARCH-PREPARED-EXECUTION-AUTHORITY` | P0 | `ACCEPTED-CONTRACT/OPEN-IMPLEMENTATION` | [ADR 0063](adr/0063-prepared-execution-authority-and-production-chain.md) 已冻结 creation-once、secret-safe `PreparedExecutionV1`、完整 Attempt/Run/current-owner/source-fact binding、`ResolvePreparedExecution` 与 held `WithCurrentRunAuthority`。完整 Allocation receipt 只从 held allocation authority 解析，`StoredClosureV1` 只从 held Attempt authority 的 `launch-authorized` 原件解析；Run/Supervisor ledger 只存 digest/projection。关闭实现须证明 `BindOwnerToAttempt` 先于 launch/prepared，owner epoch ABA 只能 authenticated reanchor/intervention，callback zero/double/escape/goroutine/reentry 与 raw argv/env/path leak 全部在副作用前拒绝。 |
| `I186-ARCH-PI0843-IDENTITY-CLOSURE` | P0 | `ACCEPTED-CONTRACT/OPEN-IMPLEMENTATION` | ADR 0063 已冻结不含 per-Run `agentLaunchSpecDigest` 的 path-free 静态 `Pi0843IdentityV1` canonical preimage；Run spec 继续由 source closure/Prepared 单独绑定。Start 必须 mutation-adjacent、descriptor-relative/nofollow 重新打开 current Node、`pi-bundle/cli.js`、两个 roots、55 materials 与 cwd，重算后同时匹配 source、旧 held FD 和 configured `PiProfile.IdentityDigest()`，再保留真实 exec 双 barrier。关闭实现须覆盖“旧 FD 未变但 canonical path 已替换”、path/inode/hash/material-set/cwd ABA、FD close/replace 与 bootstrap 前后漂移。 |
| `I186-ARCH-RUN-START-OUTCOME-AUTHORITY` | P0 | `ACCEPTED-CONTRACT/OPEN-IMPLEMENTATION` | ADR 0063 已把 `CommitRunStartOutcome` 冻结为唯一 `READY → RUNNING` 提交点，并要求 current Attempt ledger 同时闭合 exact `process-started` 和 authenticated successful `resume(disposition=ok, reason=process-resumed, state=running)` outcome。关闭实现须覆盖 resume intent/outcome/Run commit 各 response-loss 窗口、rejected/non-running/cross-child outcome、并发 start与 Run/owner head漂移，证明零重复 resume/launch与唯一可重放 projection。 |

这些 finding 只解阻 R2/R3 的 ProcessBridge producer seam，不新增 milestone 或通用恢复框架；ADR 0063 已接受，后续顺序固定为一个 bounded authority component → 立即相邻的 fixed Marshal composition 切片。两切片之间禁止插入第二个 component 或无关工作，component 完成不升级当前成熟度。

### Run-start proof 职责分离纠偏（2026-08-29）

对 ADR 0063 implementation seam 的进一步审计确认：generic held-authority callback 与 raw-source closure 不能仅靠 callback 纪律或返回后清空变量防止副本逃逸；让 ResultIngress 与 runstore 都携带/解释 owner epoch、dispatch generation 或对方 ledger facts，也会建立第二 current 判定者、反向 callback 和 response-loss 猜测。该问题是合同边界缺口，不应通过继续给旧 API 补零散逃逸检查解决。

| Finding | 等级 | 状态 | 处置 / 关闭条件 |
| --- | --- | --- | --- |
| `I186-ARCH-RUN-START-PROOF-BOUNDARY` | P0 | `ACCEPTED-CONTRACT/OPEN-IMPLEMENTATION` | [ADR 0065](adr/0065-sealed-run-start-proof-and-one-way-composition.md) 冻结由 ResultIngress 在 current-ledger borrow 内唯一前后重验 owner/Attempt/generation 并 mint shared-guard `CommittedRunStartProof`；claim 是 non-authority，禁止 owner/generation/Run head/successor。runstore 只在自己的 outer borrow 内消费 active proof、复核自身 lease/head/state 并写唯一 successor，绝不读/镜像 ResultIngress facts。关闭仍需要 S1 proof hostile/race 矩阵与 S2 fixed composition 真实纵切通过。 |
| `I186-ARCH-RUN-START-LOCK-ORDER` | P0 | `ACCEPTED-CONTRACT/OPEN-IMPLEMENTATION` | 合同固定锁序为 repository owner → runstore outer borrow → ResultIngress borrow → Supervisor → outcome fsync → ResultIngress final recheck/mint → runstore self-only CAS → deactivate/release；禁止 reacquire、handoff gap、reverse authority callback。关闭需要 deadlock/反序负测与 response-loss 两账本各自 replay 证据。 |
| `I186-ARCH-RUN-START-COMPOSITION-BYPASS` | P1 | `ACCEPTED-CONTRACT/OPEN-IMPLEMENTATION` | 合同精确限定 `internal/productionruntime/prepared_run_start_composition.go` 中两个 exported seam 各唯一一个 typed `CallExpr`、direct `FuncLit` 与 projector 单次末参数传递；runstore 单文件四 selector/primitive 唯一 callsite；generic `Append READY→RUNNING` 必须拒绝。关闭需要全 production build-tag AST/go-types 扫描、direct append/second wrapper/存储或异步 projector 负测。 |

ADR 0065 已于 2026-08-29 接受，提案基线为 `main@40fa493d1955fd6d039169483a6501a787d3cc14`；接受只冻结合同，不撤销 ADR 0063 已冻结的 Pi identity、held source 与 exact resume 业务条件，也不表示任何旧实现候选可合入。S1/S2 实现仍未开始，实施顺序只允许 S1 proof component → S2 fixed composition；ADR 0056 terminalization 是之后的独立切片。

### S2 production composition 构造环审计（2026-08-29）

对 `main@7de2a70cec112df5fbf2b36f85ce5878f227c40c` 的构造审计确认：`internal/productionruntime` 只有 package-private `newController`/`newRuntime`，没有 fixed `./bin/marshal` 可调用的 production factory；`Runtime.Status` 固定返回 `production-composition-incomplete`；CLI/server 仍保留 legacy `execution.Run`/child CLI 路径。更直接的机械阻塞是 `openRepositoryOwnerLock` 在加锁前要求完整 `ControlOwnerAcquisition`，而下一 owner epoch、前驱 fact 与 current Core observation 只能在锁内打开 ResultIngress 并 `OpenOwner` 后安全产生，形成构造环；此外 production 若接受 `MARSHAL_STATE_DIR`，同一 repository 可形成两锁两 ledger。因此 ADR 0065 §10 的“只新增 composition 文件”边界不能原样实施。

| Finding | 等级 | 状态 | 处置 / 关闭条件 |
| --- | --- | --- | --- |
| `I186-ARCH-PRODUCTION-FACTORY-MISSING` | P0 | `CONTRACT-ACCEPTED/IMPLEMENTATION-BLOCKED` | [ADR 0066](adr/0066-production-composition-owner-acquisition.md) 冻结唯一 Darwin arm64 fixed `./bin/marshal` production factory、canonical repository `.marshal` 与完整 component graph fail-closed 构造。关闭 S2 仍需要 factory 只从 fixed `cmd/marshal` 本地 CLI mutation/inspect application adapter 可达且入口只持有 `PublicApplicationPort`，legacy/fake/第二 store/`marshal control-plane serve`/独立 `cmd/marshal-server` 不可达，`Status` 对完整/不完整/recovery 真实分型及真实 Pi E2E。fixed `marshal control-plane serve` 是其后独立 ADR 0062 transport slice，必须另证 authenticated Port adapter 与 durable delivery ledger。 |
| `I186-ARCH-OWNER-ACQUISITION-CONSTRUCTION-CYCLE` | P0 | `CONTRACT-ACCEPTED/IMPLEMENTATION-BLOCKED` | ADR 0066 冻结按 canonical `ControlOwnerScope` 先取得 descriptor-bound 物理锁，再在锁内 `OpenResultIngress → OpenOwner → ObserveCurrentCore → construct candidate → one-shot provisional verifier + AcquireOwner/fsync → exact replay → current verifier`。关闭需要 provisional verifier 不逃逸且不能用于 Attempt/operation，并通过并发单赢家、append 前后 crash/response-loss、epoch/head/entry ABA 和 root 漂移矩阵；不允许在锁前猜测 acquisition。 |
| `I186-ARCH-PRODUCTION-STATE-ROOT-SPLIT-BRAIN` | P0 | `CONTRACT-ACCEPTED/IMPLEMENTATION-BLOCKED` | production factory 不接受任意 authority root，固定从 held canonical repository 派生 `<repository>/.marshal`；非空 `MARSHAL_STATE_DIR` 在 owner/ledger 前拒绝。关闭需要同一 repository 的外部 root、环境 override、symlink/rename 与第二 ledger fixture 全部 fail closed；legacy/test 外部 root 不得成为 production 证据。 |
| `I186-ARCH-S2-FILE-BOUNDARY-INFEASIBLE` | P1 | `CONTRACT-ACCEPTED/IMPLEMENTATION-OPEN` | ADR 0066 作为 ADR 0065 的 S2 implementation successor，仅把允许修改面扩到 owner lock、controller、单一 factory/composition、fixed `cmd/marshal` 的窄 application adapter/本地 CLI mutation/inspect、architecture tests 和真实 Pi E2E；proof 方向、S1→S2 adjacency 与 terminalization/provider/server transport/release 排除保持不变。接受只解除治理 blocker，不升级 R2–R5。 |

ADR 0066 提案 `69574533fd7c7e0e91b4ef45a2c902885c2eeb4c` 经独立 reviewer 复审 `APPROVE`（P0=0、P1=0）后于 2026-08-29 接受。接受只解除 S2 的治理 blocker，不表示实现完成；S1 后仍须立即进入该 S2 边界。ADR 0066 不改变 ADR 0062 的 fixed binary、loopback authentication 或禁止 child CLI 信任模型。

### S1 mechanics 重复 P1 与 Mac-first 减法审计（2026-08-29）

对第二轮候选的 exact diff 与当前 `main@84d2dcd6bb78cb7fa47ed1d3040a1f3bea5a0f11` 重新比较后，结论不是继续补丁，而是合同过度：`a6a0d638f45d6902b9c453b1e600b5f798380d82` 在 Core 与 Supervisor 两处重复 source currentness，仍无法在 ordinary-user 边界证明两次观察之间的 same-UID pathname连续性；`6298eaebebb9ec705e74903cdf2a32dda0b6a62c` 又依赖 `506a6470767f187290df08b1060834ed59aeabdb` 的大范围 runstore substrate，把 Run currentness扩成第二套Attempt/owner/generation authority。三份候选均冻结为审计/测试输入，不直接合入，也不在原分支上进入第三轮同类rework。

| Finding | 等级 | 状态 | 处置 / 关闭条件 |
| --- | --- | --- | --- |
| `I186-ARCH-ORDINARY-SOURCE-GATE-DUPLICATION` | P1 | `CONTRACT-ACCEPTED/IMPLEMENTATION-FROZEN` | [ADR 0067](adr/0067-darwin-ordinary-user-launch-and-attach-recovery.md) 已把Core收窄为pre-bootstrap无副作用current closure admission，随后释放临时FD；fixed Supervisor的`spawn`成为唯一mutation-adjacent exact role/record/file set gate，并保持同一FD组贯穿post-exec barrier。关闭须覆盖Core检查后pathname替换、material增删/换位、cwd与Allocation `LiveIdentity`不等，且不得宣称fully controlled same-UID防护。 |
| `I186-ARCH-RECONNECT-AUTHORITY-AMPLIFICATION` | P1 | `CONTRACT-ACCEPTED/IMPLEMENTATION-FROZEN` | ADR0067已停止新`process-supervisor-session-reconnected` producer。唯一恢复序列是持续held repository owner/acquisition→exact RB1 no-pending→绑定predecessor Attempt head与new acquisition的`control-owner-bound` successor→零持久化mutation的只读`Attach`→引用exact successor fact的`bind-authority(owner-successor) intent→execute→outcome`；Attach client/verifier不得逃逸。跨owner pending command与identity不唯一固定permanent intervention；pre-`process-started`只在intervention前exact证明零Supervisor/child/command副作用时允许no-effect abort/cleanup链和预算内新Attempt，否则永久禁止cleanup/release/successor。 |
| `I186-ARCH-RUNSTORE-SUBSTRATE-OVERREACH` | P1 | `CONTRACT-ACCEPTED/IMPLEMENTATION-FROZEN` | `506a647`跨24文件修改execution/review/selfidentity/runstore，`6298eae`堆叠其上；该基线不合入。S1′必须直接建立在当前descriptor-bound `runstore.Store`/`Lease`/open authority上，只新增private projector、唯一successor和generic Append拒绝；不得复制ResultIngress owner/Attempt/generation。 |

ADR0067保留的硬门禁是current-ledger recheck、exact successful resume、ADR0065 sealed proof、唯一Run successor、fixed Supervisor mechanics、ADR0064 control-directory identity、ADR0066 canonical factory。S1′只允许runstore内部窄lease shared-guard/borrow、descriptor-bound strict journal与read-only projection，唯一exported mutation seam仍为`WithPreparedRunStartAuthority`；它原冻结的S2′ producer简写已被ADR0069预审证明缺少reservation/full Attempt与诚实existing-worktree binding，因此在ADR0069复审接受前不得按旧简写实施。候选修订顺序为`reservation→dispatch/full identity→attempt-opened→owner binding→RB1 allocation facts→Prepared→S1 start`，不允许seed/Fake/legacy`execution.Run`或sidecar authority。ADR0067的Mac ordinary-user no-effect/permanent-intervention二分保持不变；其提案`1e05fb831c04a1c87e7f4ecdc677c97beb9d88e6`已接受，但不关闭ADR0069新增P0或升级R2–R6。

ADR0068提案 `9cfa1b65275d2e23f18b958a05d027adec6af8fd` 经唯一独立 reviewer `APPROVE`（`P0=0`、`P1=0`）后接受。它仅为 `v1.0.0-rc1` 部分取代0051/0052/0062的首发前置，冻结unsigned Darwin arm64 CLI-only local-dogfood preview；真实顺序是S1′→S2′→Attach/rebind→terminalization→fixed CLI真实Pi+独立Decision `ACCEPTED`→same-bytes RC1。server、managed signing/notarization和Linux stable转为RC1后继。该段“尚未实现”的判断是 2026-08-29 历史状态，已被上方 2026-09-01 RC1 终验取代；R2–R5继续为`COMPONENT`，R6现为`IN_PROGRESS/COMPONENT`。

### Darwin 控制目录阶段化身份审计（2026-08-29）

exact-head macOS CI 证明，APFS 在 Supervisor 合法创建 nonce、journal、socket 与输出对象时可能改变目录 `st_nlink`；既有全字段 runtime equality 因此会把同一目录对象误判为 ABA，并让本应在 receipt `fsync` 后拒绝的 post-command drift 提前停在 journal sequence `1`。这不是测试断言问题，也不能通过删除 link-count hostile gate、跳过目录枚举或放宽 control object identity解决。

| Finding | 等级 | 状态 | 处置 / 关闭条件 |
| --- | --- | --- | --- |
| `I186-ARCH-DARWIN-CONTROL-DIRECTORY-PHASED-IDENTITY` | P1 | `ACCEPTED-CONTRACT/IMPLEMENTATION-HELD` | [ADR 0064](adr/0064-darwin-control-directory-phased-identity.md) 已冻结 initial empty完整精确身份、setup后final observation、runtime稳定对象字段比较与 descriptor-relative phase-aware exact entry set；提案 `7d91e9704c69dcbde987d64d6fa93e0a06d7f32a` 经独立聚合复审 P0/P1/P2=0。实现证据定位：候选 `765617c20ea3faee71af980d70a35ecd06e3462a`，测试 `TestCommandBoundaryRejectsPreAndPostReceiptDriftWithoutResponse`、`TestRuntimeControlBoundaryAllowsFrozenOutputsAndRejectsUnknownEntry`、`TestControlDirectoryObjectComparisonIgnoresOnlyLinkCount`；其中候选尚缺 final setup恰为三项与pre-collect输出absent的phase-aware exact-set gate，在补齐前保持冻结。关闭还须证明initial/final同一稳定对象、unknown/early entry与稳定字段漂移 fail closed、合法APFS `LinkCount`变化贯穿command/reconnect/transcript/close，且post-receipt drift保留sequence `3`、零response。 |
| `I186-ARCH-DARWIN-TRANSCRIPT-CROSS-READ-IDENTITY` | P2 | `OPEN-INTEGRATION/DEFERRED-HARDENED` | 当前v1 protocol/authority只持久化transcript/stdout/stderr content digest、bytes与truncation，不持久化三个data object的inode identity；现有门禁只能证明每次held-FD读取事务内identity/size稳定和content exact。ADR0051 ordinary-user不覆盖fully controlled same-UID在两次读取间以同mode/同内容新inode替换。当前v1按content等价接纳，不得宣称跨时间对象连续性；未来hardened或需要object continuity时须单独升级protocol/projection或持有跨admission生命周期FD，不得借ADR0064局部修复静默扩面。 |

该 finding 只修正 ADR 0059/0060 的 Darwin control-directory 局部语义，不插入第二套 authority或扩大v1范围；它不授权 Linux/hardened profile、stable release或任何 milestone升级。

## Darwin ordinary-user 进程生命周期合同审计（2026-08-28）

代码审计确认：crash-atomic ResultIngress transaction 已随 `main@912f659` 合入，durable DispatchLease ledger 与 server run controller 也已存在；但 Local allocation/process projection、Core-owned launch/handle、terminalization 对同一 admission CAS 的复用、eligibility terminal 与 cleanup completion 仍未形成同一耐久纵切。[ADR 0056](adr/0056-darwin-process-observation-and-attempt-terminalization.md) 已于 `main@ecee8d4` 接受；实现与生产接线保持开放，R2–R5 不升级。[ADR 0057](adr/0057-durable-local-allocation-recovery-and-production-composition.md) 已于 `main@9aff8cc` 接受，但只冻结 durable allocation recovery 与唯一 production composition 合同；RB3 尚未实现该合同，本次接受不把任何能力从 `COMPONENT` 升级为 `INTEGRATED`。[ADR 0058](adr/0058-interpreted-agent-launch-identity.md) 已于 2026-08-28 接受，冻结 Pi 0.84.3 的显式 Node runtime、两个 versioned material roots、held-FD/双 barrier 和 ResultIngress 最终接纳前的 current-authority 全量重验；在实现、故障矩阵和最终 fixed-bin 真实 Pi canary 完成前，Pi 仍不是 production reachable。

| Finding | 等级 | 状态 | 处置 / 关闭条件 |
| --- | --- | --- | --- |
| `V1-LOCAL-PROCESS-AUTHORITY` | P0 | `CLOSED-CONTRACT/OPEN-IMPLEMENTATION` | ADR 0056 冻结 Core-owned launch coordinator：它在放行 workload 前负责 spawn/process-group、PID birth、cwd/executable held-FD、process handle 与 `process-started` authority fact；Provider 只能出 claim。关闭实现须从 authority facts 恢复 projection，并通过 PID/PGID reuse、path swap、FD mismatch、伪造 claim 与 launch-barrier crash 负测。 |
| `V1-ATTEMPT-TERMINALIZATION-ORDER` | P0 | `CLOSED-CONTRACT/OPEN-IMPLEMENTATION` | ADR 0056 冻结 ResultIngress admission 与 terminalization/eligibility terminal 共用 authority transaction/CAS；CAS 固定业务结论并立即 fence，随后安全终结 process group、Provider allocation terminal receipt、`cleanup-completed`，最后才 unlock/successor。关闭实现须覆盖 CAS race、每个 crash point、late result、lost response 和两次重启重放。 |
| `V1-CROSS-ORCHESTRATION-KILL` | P0 | `CLOSED-CONTRACT/OPEN-IMPLEMENTATION` | ADR 0056 把 v1 控制单元收窄为 Core 创建/观察的 cooperative/non-detaching process group；普通 Darwin 不承诺全后代 containment。只有当前合法 orchestrator 且完整 authority/Attempt/allocation/lease/generation/root birth/PGID 匹配时才允许 group signal；detach、identity conflict、第二 orchestrator 均零扩大 kill 并 intervention。关闭实现须有真实 Darwin 负测。 |
| `V1-LEASE-NORMAL-TERMINAL-FACT` | P1 | `CLOSED-CONTRACT/OPEN-IMPLEMENTATION` | ADR 0056 将 Dispatch eligibility 与 cleanup completion 正交：normal `completed` 及既有 `cancelled|expired` 均立即 generation bump/fence；cleanup-only binding 保留到独立 `cleanup-completed`，之后 `lease-released` 只释放该 binding，不是 lease state。关闭实现须保持旧 ledger replay、终态/CAS 冲突 fail closed，并证明异常路径不会提前释放 cleanup authority。 |
| `V1-CORE-RESTART-PROCESS-MECHANICS` | P0 | `CLOSED-CONTRACT/OPEN-IMPLEMENTATION` | RB2/B2 预审证明直接 `PT_TRACE_ME` 启动者的 wait right、tracer、held FD 与 pipe 无法转移给重启 Core；PID/路径重验不能重建 mechanics。ADR 0059 已接受，冻结由固定 `marshal internal process-supervisor` 持有 per-Attempt mechanics、Core 只经 current authority 命令重连的合同。实现关闭条件仍是完成真实接线并通过两次 Core restart/lost-response；Supervisor crash 保持 intervention。 |

Mac ordinary-user 只证明在可信单用户宿主上的可恢复进程记账；它不证明恶意 workload 不逃逸，不替代 Linux/hardened authority，也不解除稳定发布的签名/notarization 门禁。

### ADR 0056 RB1：单一 Attempt authority 组件 checkpoint（2026-08-28）

RB1 在 `internal/resultingress` 的同一物理 append-only 文件中加入 per-Attempt `revision/head` CAS 链，将 `attempt-opened → launch-authorized → process-started → result-admitted|terminalization-barrier → cleanup` 收敛为单一权威序列。逻辑唯一键固定为 `AuthorityNamespaceID + taskId + runId + attemptId`；`attempt-opened` 冻结 DRC namespace ref、allocation/lease、dispatch generation、fencing token digest、当前 orchestrator 与 Run authority digest，以上任一漂移只能与既有逻辑 Attempt 冲突，不能创建 sibling chain。`attempt-opened`、`launch-authorized`、`process-started` 和 barrier 都要求 current Run authority verifier 在完整 replay/read/CAS 期间保持 authority，verifier 重复调用 callback 会 fail closed 且 callback 最多执行一次。`launch-authorized` 后、`process-started` 前的耐久投影明确为 `launch-uncertain`，不能把“未看到进程”解释成“确定未 launch”。

本组件还使所有 ResultIngress kind（包括 `checkpoint`、`heartbeat`、`log`）在 barrier 后进入 quarantine；admission 与 barrier 通过同一 store lock、同一物理日志和同一 per-Attempt CAS 判序。barrier 只把已提交的 `WorkerResult` 视为业务结果，后续辅助 admission 不会覆盖它；没有 `WorkerResult` 时则明确关闭空的业务结果槽。barrier 同一 CAS 原子绑定业务 admission（或空 closure）、terminal generation bump、封闭的 `completed|cancelled|expired` eligibility union（`security-critical-revoke` 属于 `cancelled` 闭集）与非 bearer 的 cleanup binding fact，异常终态不会被伪装成 `completed`。

governed DRC 必须逐字段匹配冻结 tuple 和 `process-started.commandId`；多候选、命令漂移或身份漂移都确定性拒绝，不能依赖 Go map 遍历选择。`ProcessObservation` 要求 cwd 为绝对规范目录、executable 为绝对规范普通文件，文件类型使用原始 POSIX `S_IFMT` 语义；`observedAt` 必须是 canonical UTC RFC3339Nano 且不早于 process birth。

cleanup 使用时必须同时经过外部 current Run authority verifier、精确 tuple、terminal generation、closed operation allowlist 与未 release 状态；单独持有 binding digest 不构成权限。真实 side effect 必须放在 `WithAuthorizedCleanup` 的 held-authority callback 内，独立 `AuthorizeCleanup` 只是无授权效力的 preflight。权限按 phase 收窄：process terminal 前只允许对已确认进程 `Signal`；process terminal 后永久关闭 `Signal`，仅在 allocation terminal 前允许独立的 Provider `Terminate`；allocation terminal 后不再授权 Provider effect，只能追加/精确重放下一合法 cleanup fact；cleanup complete 后只进入 release；release 后所有 effect 永久拒绝，只允许在 current Run authority 与精确 tuple 下无副作用地重放同一 `cleanup-released` append。`AttemptStates`/`PendingAttemptStates` 从同一日志确定性重放，供重启恢复枚举使用，不引入第二身份索引。

`LeaseLedger` 只接收带 Attempt authority head 的 `completed|cancelled|expired` 封闭投影，并保留 completion/cancel reason；它不能独立决定新 Attempt eligibility，新 Attempt terminal binding 也不能被新 lease 复活。这里必须明确：Attempt/Result 共用一个物理日志，而 `LeaseLedger` 仍是另一个物理 append-only read model，两者**没有**跨文件原子事务。既有 `AppendCancel`/`AppendExpire` 与历史事实格式仅为旧调用链兼容；RB3 必须使所有新 Attempt 只从 barrier 投影终态，并以 current authority verifier 在崩溃后幂等补投影。在该接线完成前，不能把两个 ledger 描述成物理原子或宣称 terminal eligibility 已 `INTEGRATED`。

该 checkpoint 仍是 `COMPONENT`，不关闭上表 finding：RB1 没有修改 composition root、真实 Darwin launch/process control、Local/Sandbox bridge 或 execution recovery。关闭实现仍需要后续 RB2/RB3 把本 authority API 接入 Core-owned fixed-binary launch、真实 process observation、Provider terminal receipt 和 cleanup-before-unlock/successor 全链，并在最终固定 `marshal` 产物上执行负向矩阵。RB1 author 本地只做 compile-only、`vet`/`staticcheck`/结构与 secret 门禁，不把未执行的新 Mach-O 测试产物描述为运行证据。

## v1.0 生产可达性重置（2026-08-27）

本轮综合审计不再以 package、PR 或 historical milestone 数量衡量完成度，而是从真实 composition root 反查生产路径。当前真实写任务主链仍主要是 `cmd/marshal → internal/cli → execution.Run → Adapter.Run`；`spine`、`agentruntime`、`runtimeprofile`、`bindingcheck`、`revokedrain` 与 `resultingress` 等资产分别具有类型、纯核心或测试证据，但尚未共同承载一条真实 Agent-in-Sandbox Run。`spine` 的示例执行仍依赖 FakeAgent，部分 outbox/write-gate/authority 仍为内存形态。

因此，历史 M8/M9 `PASSED` 继续证明当时定义的 gate、PR 与 CI 已通过，但不能推导出 v1.0 production integration。把 R1/R2 或 R3-A/B/C component checkpoint 标为 `DONE` 会导致后续在错误基线上继续横向扩展，属于状态口径走偏。

| Finding | 等级 | 状态 | 处置 |
| --- | --- | --- | --- |
| `V1-PRODUCTION-REACHABILITY` | P0 | `OPEN-IMPLEMENTATION` | 只有真实 `marshal`/loopback server → durable journal → WorkerExecutor → Sandbox → AgentRuntime → ResultIngress → Verification/Outcome 全链通过，R1–R3 才能逐阶段关闭。 |
| `V1-PARALLEL-AUTHORITY` | P1 | `OPEN-IMPLEMENTATION` | v1.0 复用现有 journal/lease/current ledger；内存 Run/lease/outbox 只能是可重建 projection，禁止成为第二真值。 |
| `V1-CUTOVER-NONDETERMINISM` | P1 | `CLOSED-CONTRACT` | ADR 0052 部分取代 ADR 0045 §1 第 1 项：Fake 比较 exact digest，真实 Agent 比较 authority invariants，内容仍逐次独立验证。 |
| `V1-SCOPE-UNBOUNDED` | P1 | `CLOSED-CONTRACT` | ADR 0052 把 Cloudflare 完整生产拓扑、HA、多用户、完整 Provider/SDK 矩阵与 Goal DAG 延期到 1.x。 |

该 2026-08-27 重置仅作历史纠偏记录。当时权威状态为：R0 `PASSED/DESIGN`；R1 `IN_PROGRESS/INTEGRATED`；R2–R5 `IN_PROGRESS/COMPONENT`；R6 `PLANNED/DESIGN`。当前 R6 已随 RC1 发布进入 `IN_PROGRESS/COMPONENT`；M10–M13 仍作为 1.x 候选池，不阻塞 stable v1.0。

本修订不降低任何 universal 不变量；Local ordinary-user 的 Core-held process observation 只支持 trusted single-user v1 profile，不能关闭 cloud/hardened 的 location attestation finding。生产 cutover、故障 conformance、签名/notarization 与 release identity 仍必须在 R5/R6 完成。

## 终态职责图复杂度审计（2026-08-27）

本轮复核确认：Kernel、authority ledger、Agent/Sandbox Provider、ResultIngress、独立 Verification、Decision、Effect reconcile 与 Artifact Store 分别对应不同故障或权威边界，作为长期**逻辑职责地图**没有过度设计；风险来自把每个逻辑方框直接实现为独立服务、协议或状态库。

| Finding | 等级 | 状态 | 处置 |
| --- | --- | --- | --- |
| `V1-LOGICAL-PHYSICAL-CONFLATION` | P1 | `CLOSED-DOCS` | [整体架构](architecture.md#逻辑职责不等于物理服务)已明确 v1.0 采用单 Control Plane 进程、唯一 file-backed authority ledger、本地内容寻址对象存储和多个有界 Worker/Verifier runtime；职责默认进程内模块化，只有独立 trust boundary、durable lifecycle 或已测量的扩缩容/故障隔离需要才能拆服务。 |
| `V1-PREMATURE-PLATFORM-GENERALIZATION` | P1 | `CLOSED-DOCS` | [实施计划](implementation-plan.md#v10-复杂度预算)禁止在 R1–R6 主线新建通用 `WorkflowTemplate` DSL、Goal DAG runtime、跨节点 scheduler、独立 GC service、第二 queue 或第二状态库；新增 seam 必须在同一切片接入真实 composition root。 |

该关闭只表示实现与部署口径已经明确，不升级任何 Milestone 或能力成熟度，也不表示 Goal、WorkflowTemplate、远程 Artifact/Knowledge Store 或 GC 已实现。此次修订不改变 trust boundary、持久化语义、生命周期或发布权限，因此不新增 ADR；未来若拆分引入新的权威写路径、持久对象或跨域授权，仍必须先新增或替代 ADR。

## Composition root 纠偏审计（2026-08-27）

本轮审计从真实 composition root 反查生产路径，发现 CLI 此前构造两个独立 `EmbeddedSandboxRuntime` 实例，导致 lease 和 agent registry 互不可见，admission 必然失败。已修复为单实例并补充 fail-closed 门禁。

| Finding | 等级 | 状态 | 处置 |
| --- | --- | --- | --- |
| `V1-COMPOSITION-ROOT-SPLIT` | P0 | `CLOSED-FIX` | CLI 只构造一个 `EmbeddedSandboxRuntime`——同一实例同时承担 DispatchBinder + SandboxProvider + Authority + ResultIngressStore（`33bad5c`）。 |
| `V1-AGENT-REGISTRY-EMPTY` | P0 | `CLOSED-FIX` | Adapter probe 后注册 agent 到 `sharedRuntime.agentRegistry`（`33bad5c`）。此前 agentRegistry 在生产路径始终为空，`AgentRegistrationActive` 总是返回 false。 |
| `V1-FABRICATED-LEASE-EXPIRY` | P0 | `CLOSED-FIX` | 删除 execchain.go 的 `now+24h` lease fallback——lease 缺失直接 fail closed（`33bad5c`）。此前虚构的 expiry 被冻结进 AttemptBinding 文件，污染耐久记录。 |
| `V1-ALLOCATION-RECORD-SILENT-FAIL` | P1 | `CLOSED-FIX` | Allocation record 写入失败从降级改为 fail closed——阻止 Exec（`33bad5c`）。 |
| `V1-LEGACY-ADAPTER-SILENT-FALLBACK` | P1 | `CLOSED-FIX` | RunWorker 遇到非 LaunchCapable adapter 必须 fail closed——production profile 不允许静默退回宿主 legacy Run（`33bad5c`）。 |
| `V1-CANARY-FAIL-AS-SUCCESS` | P1 | `CLOSED-FIX` | 严格 E2E 测试 `TestRealPiStrictE2E` 要求 `worker.completed`——`worker.failed` 直接 t.Fatal（`86e209a`）。canary 更新为提示运行严格 E2E。 |
| `V1-SERVER-RESTART-404-ONLY` | P1 | `CLOSED-FIX` | marshal-server restart 测试重写：创建真实非终态 Run（`run-restart-real`），验证跨进程恢复返回 200+Ready（`da8cccd`）。此前只断言 404。 |
| `V1-FENCING-DOUBLE-WRITE` | P1 | `CLOSED-FIX` | exec-chain 在 embedded 模式下（`MARSHAL_EMBEDDED_SANDBOX=1`）复用 BindDispatch 已创建的 lease（含 fencingToken/AllocationId/Generation）而非独立计算 `fencingDigestOf` 做二次 Provision；修复后 embedded canary（`TestRealPiExecChainCanary`）首次跑通：pi 真实在 Local allocation 内执行（transcript 27KB，exitCode=0）（`634937b`）。 |
| `V1-AGENT-SANDBOX-DIGEST-CONFLATED` | P1 | `CLOSED-FIX` | Facts 新增 `SandboxCapabilityDigest` 字段——agent digest 与 sandbox digest 分离（`686ee61`）。此前两者混用同一字段 `Facts.CapabilityDigest`。 |
| `V1-ATTEMPT-BINDING-MISSING-EMBEDDED` | P1 | `CLOSED-FIX` | AttemptBinding 缺失已关闭；现行前置 Pi canary 绑定 `sourceHead=d4b9647`，单 Attempt/9 Gate 到 `REVIEW_PENDING`。该证据仍不构成 R2/R5 终态，因为 ADR 0056 实现、最终主线重跑和独立 Decision/`ACCEPTED` 尚未闭环。 |
| `V1-EMBEDDED-ADMISSION-REJECTED` | P1 | `SUPERSEDED` | 原以「任意-active fallback（`cad8773`）+ 消费端补 `registration:` 前缀（`3f8638d`）」修复 embedded admission 拒绝——该两处均为门禁降级，已被第二轮审计定性并移除，改由 `V1-AGENT-REG-ANY-ACTIVE-FALLBACK` 与 `V1-SANDBOX-REG-CONSUMER-PREFIX` 的根因修复取代。 |
| `V1-LEASE-NOT-DURABLE` | P1 | `SUPERSEDED-BY-ADR0056` | DispatchLease ledger 已耐久化，原“lease 全为内存态”描述已过时；真实缺口是 Local allocation/process projection、立即 eligibility terminal、独立 `cleanup-completed` 与 cleanup binding release 未进入同一 authority transaction。由本报告顶部 ADR0056 findings 跟踪。 |
| `V1-RESULTINGRESS-NOT-DURABLE` | P1 | `CLOSED-FIX/ADR0056-INTEGRATION-PENDING` | `main@912f659` 已关闭 ResultIngress admission→worker-result→Run journal 的 crash-atomic 持久化/恢复缺口。ADR 0056 terminalization barrier 仍须复用同一 authority transaction，不能另建 check-then-act 路径；在该接线和最终主线 canary 前不升级 R2。 |

## 第二轮生产权威审计（2026-08-28）：门禁降级纠偏

第二轮维护者审计发现：最新提交为跑绿 E2E 引入了两处权威校验降级，并提前升级里程碑。均属「把测试跑通误当成生产权威闭环」的偏航，现逐项纠正。

| Finding | 等级 | 状态 | 处置 |
| --- | --- | --- | --- |
| `V1-AGENT-REG-ANY-ACTIVE-FALLBACK` | P1 | `CLOSED-FIX` | `AgentRegistrationActive` 曾降级为「精确查找失败则任意 active registration 即通过」——门禁绕过。已删除该 fallback 及配套 `LookupByProviderName`/`List`；根因改为稳定能力身份：`StableCapabilityDigest` 排除 `probedAt` 等易变诊断字段，dispatch 时冻结精确 `AgentRegistrationID` 进 AttemptBinding，ingress 只对其 exact lookup（`b7509c8`）。 |
| `V1-SANDBOX-REG-CONSUMER-PREFIX` | P1 | `CLOSED-FIX` | sandbox registrationId 曾只在消费端临时补 `registration:` 前缀，接纳不验证 binding 与真实 ledger 精确相等。已从源头统一 canonical ID（`embeddedRegistrationID` 带前缀），删除消费端 hack，并在 `AdmitWithDurableAuthority` 机械断言 `AttemptBinding.SandboxProviderRegistrationID == 当前 ledger ProviderRegistrationID`，不等即 fail closed（`0ae6640`）。 |
| `V1-STRICT-E2E-FALSE-POSITIVE` | P1 | `CLOSED-FIX/TERMINAL-PENDING` | 前置 fixed-bin canary 已绑定 `sourceHead=d4b9647`，单 Attempt 通过 9 项 Gate，到达 ReviewPacket/`REVIEW_PENDING`。最终主线尚未重跑，也未导入独立 ReviewDecision 到 `ACCEPTED`，因此不能关闭 R5。 |
| `V1-R5-INTEGRATED-PREMATURE` | P1 | `CLOSED-DOCS` | R5 曾被标 `INTEGRATED` 但同时承认 cutover 未开始；且 `MARSHAL_WORKER_EXECUTOR=legacy` 仍在、production gate 需额外环境变量、默认非 embedded 走 seed admission。已撤回为 `COMPONENT`（`82e0c9f`）。 |
| `V1-DOCS-STATE-CONFLICT` | P1 | `CLOSED-DOCS` | AGENTS/Roadmap/Readiness/Implementation Plan 的 R2–R6 状态曾互相冲突。2026-08-28 当时统一为 R0 PASSED、R1 IN_PROGRESS/INTEGRATED、R2–R5 IN_PROGRESS/COMPONENT、R6 PLANNED/DESIGN；2026-09-01 RC1 发布后又统一更新为 R6 IN_PROGRESS/COMPONENT。 |

纠正后真实进展以本报告顶部 2026-08-28 checkpoint 为准：ResultIngress crash-atomic transaction 已随 `main@912f659` 合入，durable server run controller 已随 `main@44ee8c9` 合入，production selector 已随 `main@d4b9647` 收紧，Pi canary 在 `sourceHead=d4b9647` 以单 Attempt/9 Gate 到达 `REVIEW_PENDING`；但 `main@ecee8d4` 只接受 ADR 0056 合同，实现、cleanup 矩阵和独立 Decision/`ACCEPTED` canary 尚未闭环，因此 R2–R5 继续为 `COMPONENT`。

## 行业协议收敛跟踪（2026-08-21 基线）

外部背景（公开行业资料转述，未做在线核验）：agent 相关协议正沿三条轴在 Linux Foundation 轨道收敛——MCP（agent→工具/数据轴）进入 AAIF 并成为事实标准；A2A（agent↔agent 轴）由 Google 捐赠至 Linux Foundation 并获 100+ 背书；ACP（客户端↔agent 轴，LSP 式协议）已被 Gemini CLI、Neovim、JetBrains 等客户端采用。行业判断是自研私有 agent 协议的兼容性税持续上升。

对 Marshal 的分层结论：

- Public API 与 Provider remote transport 是控制面契约，不是 agent 协议；versioned HTTP/JSON + OpenAPI + SSE 的自持契约不在收敛压力范围内，保持现状；
- 三条标准协议与 Marshal 正交：MCP 属 agent 工具层（Worker 自行使用，Marshal 经工具策略治理，不实现 MCP server）；ACP 属客户端↔agent 轴（正确形态是 ACP facade 作为 Public API client，或作为某一 AgentAdapter 的 transport）；A2A 属 agent↔agent 委托轴（正确形态是外部 gateway 作为 Public API client；Core 内多 Agent 协作仍按 ADR 0019 禁止 P2P 第二权威）；
- 既有立场（ACP 只可作为 AgentAdapter transport、A2A 只作为未来外部 gateway 候选、MCP 属延后阶段、三者不阻塞核心路线，见[实施计划](implementation-plan.md)）仍然成立；缺口不在方向，而在“何时必须接”此前缺决策记录。

跟踪机制：每季度复核一次（下一次 2026-11），更新三轴协议采用状态并检查触发条件；满足任一触发条件时先新增或替代 ADR 再实施，不在触发前抢先实现协议面：

1. ACP：客户端生态覆盖 JetBrains、Zed 与 Neovim/Gemini CLI 中至少两者，且出现真实用户请求从这些客户端驱动 Marshal 任务——实现 ACP facade 作为 Public API client，不引入任何 Core 改动；
2. A2A：出现真实外部委托场景（外部 agent 系统向 Marshal 提交任务或消费 Outcome）——评估 A2A gateway 作为 Public API client，Core 语义不变；
3. MCP：既有工具策略（tool allowlist 等）无法表达的 Worker 工具面治理需求，或 Data/Capability 域材料授予需要标准化互操作——评估 MCP 形态，仍不得承载 raw credential（ADR 0018 §3 边界不变）。

本节是跟踪记录与触发条件登记，不是架构变更：不改变任何 ADR、Milestone 状态或信任边界；接入边界的集中重申见[Runtime 架构](runtime-architecture.md)“部署形态与 Public API”节。

## Issue #130 lease owner 与 orphan recovery 审计增补（2026-08-21）

审计确认当前 owner record 存在真实 legacy 5-field/7-field shape，PID/heartbeat/事件年龄诊断与 OS descriptor lock authority 的边界需要收敛；owner acquisition epoch 与 Attempt generation 尚缺独立的 successor/high-water 合同，`SupervisorDispatcher` orphan recovery 也需要把 late `WorkerResult`、quarantine、预算裁决和 `Outcome` 绑定到同一个 crash-safe append-only transaction。

[ADR 0035](adr/0035-supervisor-lease-owner-v2-and-orphan-recovery.md)（Proposed）提出 `LeaseOwnerRecordV2` exact closed schema、同一 descriptor authority 下的 legacy v1 fail-closed migration、epoch+digest high-water rollback/ABA fencing，以及 `prepare → fence-consumed → inspect/reconcile → resolved` 的 append-only durable transaction。所有 unknown 统一进入 intervention、zero side effect，Core 只追加 typed `Outcome`，不得静默写 `BLOCKED`。

该 finding 保持 `OPEN`：ADR 尚未接受，owner v2、migration、共享 eligibility predicate、exact-run dispatcher 与 orphan transaction 均未实现或验证。此记录不表示 Issue #130 完成，不改变 M10–M13 状态，也不授权生产启用自动 orphan recovery。
- 范围：当前文档与 `v1alpha1` Schema 描述的 Local CLI MVP（Runtime/Sandbox 契约部分为分层状态，见下）
- 结论（分层）：
  - Local MVP（Milestone 0–6）：**`APPROVED_FOR_IMPLEMENTATION`** / `USABLE`，行为与门禁不变；
  - Runtime/Sandbox 契约（M7–M13）：在 [ADR 0017](adr/0017-provider-neutral-sandbox-contract.md) 接受前为 **`BLOCKED`**（首次 Sandbox SPI dogfood reject 与 Round 2 评审暴露的合同级歧义）；2026-08-10 全部 P1 经 Round 2 独立验证与 ReviewDecision accept，维护者接受 ADR 0017，设计歧义关闭；2026-08-11 维护者接受 [ADR 0018](adr/0018-control-plane-and-provider-ports.md)，冻结 Marshal C/S Control Plane、按信任域分隔的 Provider Port、耐久注册/能力快照与在途 lease 撤销，澄清/部分取代 ADR 0017 §4/§6/§7/§8/§10/§12 并显式取代 ADR 0016 §6 经 ADR 0017 承接的 universal 接纳口径，并随 Round 4 独立评审八项 P1（远程 transport 基线、securityDomainId 键空间、attestation 全链绑定、原子 fencing sink、SSE 恢复与再授权、engine 单一 seam、Port protocol family、legacy snapshot 残留）全部关闭增补 §10–§16，随 Round 6 复核两项残留（Control Plane authority namespace 与 Provider actor 域分离、typed cross-domain edge）全部关闭修订 §3/§10，随 Round 7 复核三项残留（双键空间残留清除、Core-only typed edge 生命周期细化、Public API 幂等/SSE/对象 key 修正为 authorityNamespaceId）全部关闭修订 §3/§4/§5/§7/§10/§13，随 Round 8 复核一项残留（typed edge 跨域例外与适用范围）全部关闭修订 §3/§10，随 Round 9 复核两项残留（ADR0018-UNQUALIFIED-CROSS-DOMAIN-RESIDUE、ADR0018-NONEDGE-PORT-AND-SAME-DOMAIN-AUTHZ）全部关闭修订 §2/§3/§5/§7/§10（接受只冻结设计，不升级 M8–M13 实现/conformance 状态）。
- 未关闭 Blocking Finding：4 项 P1。`ISSUE53-CI-REWORK-EVIDENCE-P1` 仍为 `OPEN`；ADR 0032 B2 独立复核另重开 `ADR0032-B2-AUTHORITY-ROLLBACK-DOMAIN`、`ADR0032-B2-DELIVERY-CRASH-WINDOW`、`ADR0032-B2-RECOVERY-RESIGN` 三项 P1。后三项的目标契约由 [ADR 0033](adr/0033-journal-bound-merge-authority-and-delivery.md)（Proposed）给出；local/non-production 受限 profile 的关闭要求 ADR 接受、A–D implementation successor 全部合入、独立验证 P0/P1 清零及 required CI/secret scan/recovery conformance 全绿；production supported 的关闭还必须等待 M11 external rollback witness、跨节点 fence 与协调回滚演练。它们不改变 Local MVP `APPROVED_FOR_IMPLEMENTATION` / `USABLE`，但在关闭前禁止把 `mergePolicy=policy` 登记为无限定 supported。
- 门禁状态（分层）：
  - 维护者已接受 ADR 0001–0011、ADR 0012–0014 与 ADR 0016（2026-08-10，含 M7–M12 路线）及 Local MVP Scope；
  - ADR 0017（provider-neutral Sandbox 安全契约）已接受（2026-08-10）；**接受只关闭设计歧义**：不得把 M8 实现或 conformance 状态提前标为完成，M7 保持 `IN_PROGRESS`、M8–M13 保持 `PLANNED`，首次 Sandbox SPI dogfood 的既有实现成果按未接纳探索证据对待（见 [Roadmap 状态](roadmap-status.md)）；
  - ADR 0018（Marshal C/S Control Plane、按信任域分隔的 Provider Port、耐久注册/能力快照与在途 lease 撤销）已接受（2026-08-11）；**接受只冻结设计**：不升级 M8–M13 实现或 conformance 状态，M7 保持 `IN_PROGRESS`、M8–M13 保持 `PLANNED`；ADR 0017 的历史 universal 口径（统一 lease 身份/统一 fencing/统一 providerType/legacy CapabilitySnapshot 注册产物）在现行规范入口就地标注已被 ADR 0018 取代；Round 4 独立评审八项 P1 已随 ADR 0018 §10–§16 关闭，Round 5 复核四项残留（复合安全域、Port protocol family、Push/Pull 不变量等价、计划升级 bounded drain）已随 ADR 0018 §2/§6/§7/§10/§16 修订关闭，Round 6 复核两项残留（Control Plane authority namespace 与 Provider actor 域分离、typed cross-domain edge）已随 ADR 0018 §3/§10 修订关闭，Round 7 复核三项残留（双键空间残留清除、Core-only typed edge 生命周期细化、Public API 幂等/SSE/对象 key 修正为 authorityNamespaceId）已随 ADR 0018 §3/§4/§5/§7/§10/§13 修订关闭，Round 8 复核一项残留（typed edge 跨域例外与适用范围）已随 ADR 0018 §3/§10 修订关闭，Round 9 复核两项残留（跨域 fail closed 表述精确化、非 edge Port 与同域不自动授权）已随 ADR 0018 §2/§3/§5/§7/§10 修订关闭，远程 transport 安全基线自各远程能力首次 enable 起生效（M11 不补基线）

## 执行结论

该设计作为本地 CLI-first Coding Agent Harness，内部一致且可以实施。CLI-first 描述 Harness 接口，不限制主 Agent 必须使用 Codex CLI；Codex Desktop 与手机端 Remote 通过相同契约接入。最重要的信任边界已明确：

- Worker 负责实现，但不能自证；
- 每个独立 Worktree 只有一个写入者；
- 确定性观察先于语义 Review；
- Decision 绑定精确 Evidence；
- Publisher 权限与凭据和 Worker 分离；
- 失败与 No-change Run 产生 Outcome Evidence，不制造虚假 PR；
- 普通宿主机执行不会被宣传成恶意代码隔离。

当前可以按[实施计划](implementation-plan.md)推进。该批准只适用于文档定义的 Local MVP，不适用于 Multi-user Service、Hostile-code Execution、Automatic Merge 或延后的 Hardened Profile。

## 审查范围

- Vision、Goal、Non-goal、Trust Boundary 与 Success Metric；
- Component Architecture、Identity、Persistence、Locking 与 Idempotency；
- 仓库本地 `.marshal/` 状态隔离、默认 Git 排除与长期归档边界；
- 全部 Lifecycle State 与 Transition Guard；
- TaskSpec、Worker、Evidence、Review、Policy、Event 与 State Contract；
- Qwen Code、OpenCode 与 Pi Adapter Boundary；
- Independent Verification 与 Lead-agent Review；
- Codex CLI、Codex Desktop 与手机端 Remote 的主 Agent 接入边界；
- Artifact、Commit、PR/MR Publication 与 CI Binding；
- Security Threat、Assurance Profile 与 Credential Separation；
- Interruption、Crash Consistency、Reconciliation 与 Cleanup；
- Implementation Milestone 与 Exit Criteria；
- ADR 0001–0011。

## 自动检查

| 检查 | 结果 |
| --- | --- |
| JSON Parsing | 12 份 Schema 与 12 份 Happy-path Record 通过 |
| Draft 2020-12 Metaschema | 12 份 Schema 通过内置 Draft 2020-12 编译器 |
| Happy-path Validation | 12 份 Record 全部通过对应 Schema |
| Local `$ref` 与 Regex | 104 项 `$ref` 与 53 项 Regex 通过 |
| Lifecycle/Schema State Alignment | 16 个 State 全部一致 |
| Markdown Local Link | 通过 |
| `.marshal/` Git Ignore | 根规则命中 |
| Nested linked worktree Probe | macOS Git `2.50.1` 可在 `.marshal/worktrees/` 创建并识别独立 worktree |
| 全文件 Whitespace/Conflict Marker | 通过 |

Schema 只承担结构校验。[`schemas/README.md`](https://github.com/chiga0/marshal-harness/blob/main/schemas/README.md) 中列出的 Semantic Validator 是 Milestone 0 强制要求。

## 审计中已关闭的问题

| ID | 级别 | 问题 | 修复 |
| --- | --- | --- | --- |
| A-001 | P1 | ArtifactManifest 无法表达发布前 `expected` 或失败后 `missing` | 增加无需伪造 Path/URI 的显式 Variant |
| A-002 | P1 | 未 Commit Worktree 被强制要求 Git Tree SHA | 改为 Canonical `snapshotDigest`，`gitTreeSha` 可选 |
| A-003 | P1 | Durable WorkerRequest 与 ReviewPacket 缺少 Schema | 增加 Schema、Example 与 `reviewPacketDigest` 绑定 |
| A-004 | P2 | Worker Artifact 可同时声明 Local Path 与 External URI | 改为 Exclusive `oneOf` |
| A-005 | P2 | Policy 可要求网络强制但不记录有效模式 | PolicySnapshot 中 `networkPolicy` 改为 Required |
| A-006 | P2 | README 生命周期漏掉 Operational Retry | 增加 `RETRY_PENDING` 并对齐 State Schema |
| A-007 | P1 | 全局状态目录示意无法直观看出不同业务仓库与任务 worktree 的归属 | 改为每仓库独立、默认忽略的 `.marshal/`，每个任务仍使用真实 linked worktree |
| A-008 | P2 | CLI-first 容易被误解为主 Agent 只能使用 Codex CLI | 增加与界面无关的 `LeadAgentBridge`，明确支持 Codex Desktop 与手机端 Remote |
| A-009 | P2 | TypeScript/Node 默认选型与本地进程编排、单二进制分发目标不完全匹配 | 新增 ADR 0005，选择 Go 并保留语言无关契约 |
| A-010 | P2 | macOS 的 `/var` 与 `/private/var` 别名会让有效 worktree 的字符串路径比较失败 | Repository/Worktree Identity 必须使用规范化真实路径，并加入平台 Fixture |
| A-011 | P1 | Worker 控制文件放入 Worktree 会污染业务 Diff，开放整个 Run Store 又会破坏冻结证据 | 新增 ADR 0006，使用 Attempt-scoped `controlRoot/input|output` |
| A-012 | P1 | Worker 可破坏 linked worktree 的 `.git` 身份，使嵌套目录向上误认主仓库 | `Open` 解析真实 `--show-toplevel`，Worker 后再次验证 Root/CommonDir，失败进入 `BLOCKED` |
| A-013 | P2 | 把 cmux 直接写入 Worker 执行路径会耦合 Provider、平台与 UI，并可能削弱进程和证据控制 | 新增 ADR 0008：独立 Observer Port，cmux 仅作为首个只读可视化 Backend，失败降级到 `captured` |
| A-014 | P1 | 直接在 cmux 启动默认 Agent TUI 会继承 ambient environment、绕过 Adapter 工具/子 Agent预算，并且没有可靠完成边界 | 新增 ADR 0011：Adapter 冻结 TUI launch，使用一次性密封启动信封；缺少可信 CompletionGate 时只允许受监督 PTY |

上述原始审计范围内没有未解决的 P0、P1 或 P2 架构问题（后续增补 finding 以下文各增补节为准，当前唯一未关闭项为 Issue #53 审计增补节的 `ISSUE53-CI-REWORK-EVIDENCE-P1`）。

Milestone 3 的实现审计曾发现 Verdict E2E 覆盖与 `.pending` 崩溃残留两个缺口，均已在复审前关闭；独立复审结论为 `APPROVE`，GitHub Actions run `30874552479` 的 Linux、macOS 与 Secret Scan 全部通过。详情见 [Milestone 3 OpenCode 独立审查](reviews/milestone-3-opencode-review.md)。Milestone 4 的真实 OpenCode Adapter 也已完成独立审计与远端 CI 验收，详见 [Milestone 4 独立审查](reviews/milestone-4-opencode-review.md)。

Milestone 5 的独立审计首轮阻止了 CI 返工发布死锁、`skipping` 误通过与 Record 崩溃覆盖；最终复审无 P0/P1，主 Agent 又关闭了 Journal/Snapshot Publication 重放、Remote `insteadOf` 重定向、首次受控 Commit 竞态和标题上限问题。主分支 CI `30889069165` 与真实 [Draft PR #1](https://github.com/chiga0/marshal-harness/pull/1) 的 CI `30889190854` 全绿，Publisher 两次复用同一 PR 且未 merge。详见 [Milestone 5 独立审计](reviews/milestone-5-opencode-review.md)。

## 红队场景

| 场景 | 控制措施 | 结果 |
| --- | --- | --- |
| Worker 未运行测试却声称通过 | 字段使用 `declared*`，Marshal 重跑 Required Command | 已覆盖 |
| Worker 修改 Forbidden Path 或隐藏 Untracked File | 独立观察 Path、Rename、Mode 与 Untracked File | 已覆盖 |
| Codex Accept 后 Worktree 被修改 | Publisher 重算 Snapshot/Evidence，拒绝 Stale Decision | 已覆盖 |
| Worker 尝试 Push 或开 PR | Publisher 分权；强凭据隔离要求 Hardened Profile | 已覆盖，并清楚限制 Local Profile |
| Repository Instruction 扩大 Scope | Frozen TaskSpec/Policy 优先并记录 Instruction Digest | 已覆盖 |
| Worker/Verifier 崩溃留下部分变更 | 终止 Process Tree、刷新 Journal、保存 Worktree、新建 Attempt | 已覆盖 |
| Push 或 PR 创建成功后进程崩溃 | 重试前 Reconcile Remote Branch 与 Task Marker | 已覆盖 |
| Required CI 来自旧 Commit | CI Evidence 绑定 Published Head SHA | 已覆盖 |
| Empty Diff 被报告为成功 | 除非允许并 Review No-change，否则 Gate 失败且不开虚假 PR | 已覆盖 |
| Review 后 Base 前进 | 禁止 Silent Rebase，由 Policy 选择 Publish、Block 或 New Run | 已覆盖 |
| Symlink 或 `..` 逃逸 | Canonical Path 与 Collection 默认失败 | 已覆盖 |
| 两个 Qwen 入口版本不同 | Exact Executable 与 CapabilitySnapshot，禁止隐式 Fallback | 已覆盖 |
| 主 Agent 返回 Prose 或 Stale Decision | 保持 `REVIEW_PENDING` | 已覆盖 |
| Git Hook 修改已接受代码 | Publisher 禁用 Ambient Hook 并验证 Commit Tree | 已覆盖 |
| Test/Build Script 恶意 | 只有 Hardened Profile 可以声称隔离 | 正确排除在 Local MVP 外 |

## 剩余限制，但不构成 Blocking Finding

### Local Profile 不是 Hostile-code Containment

`workspace-write` 提供 Worktree Isolation、Filtered Environment 与 Workflow Gate。普通 Host Process 仍可能访问 Home、Credential Helper、本地服务或网络。文档已明确该限制，并要求不可信代码使用 Hardened Profile。

### 真实 Adapter 必须 Probe

CLI Flag 与 Event Shape 会随版本变化。Implementation 不得把 Environment Baseline 当作兼容性承诺。Milestone 4 要求 Exact Executable Probe 与共享 Conformance Test。

### 初始仅支持 GitHub Publication

本机 GitHub CLI 已认证，可以进行 Publisher Spike；GitLab CLI 未安装，GitLab 延后。这不影响 Provider-neutral Boundary。

### Semantic Rule 需要代码实现

Cross-record Freshness、ID Uniqueness、Budget Relationship、Path Canonicalization、Accept/No-change Guard 与 Publication Consistency 不能全部由 JSON Schema 安全表达，已列为强制 Semantic Validator 与 Exit Criteria。

## 实施阶段关闭的问题

| ID | 级别 | 发现方式 | 问题 | 关闭 |
| --- | --- | --- | --- | --- |
| I-001 | P1 | 2026-08-05 真实 Full MVP E2E（Run `m6-mvp-e2e-20260805` / `m6-mvp-e2e-r2-20260805`） | ADR 0010 引入的 balanced publish Approval Gate 与发布重校验、Outcome、Rework 读取仍使用 legacy `review-decision.json`，而 Review 事务持久化为 `decisions/decision-%03d.json`，导致发布审批与发布恒失败 | 两个独立 Marshal Run（`m6-approval-fix-r3-20260805`、`m6-decision-paths-20260805`，均 `ACCEPTED`）修复为轮次绑定读取，语义校验不变；提交 `4538f9f`、`9589b25`；修复后真实 E2E Run `m6-mvp-e2e-r3-20260805` 全链路 `ACCEPTED` |

两次失败均以 `BLOCKED` fail-closed，未产生远端副作用；信任边界、持久化契约与发布权限未被改变，因此不新增 ADR，仅记录关闭证据。

| I-002 | P2 | 2026-08-07 hardening 批次合入 | 维护者以 worktree 拷贝合入 dx 任务时，其 cli.go 基线早于 abort 合入，覆盖了 `task abort` 实现，cli 测试失败 | 手工合回 abort dispatch 与辅助函数，骨架测试更新；提交 `f8d4e74`；教训：跨基线 worktree 合入必须先比对基线差异（已记入维护者流程） |
| I-003 | P1 | 2026-08-08 实现批合入 | 同类事故二次：ENV-DX 合入以旧基线 worktree 拷贝覆盖 opencode/pi/qwen 非测试文件，丢失 ADR 0013 分级接线，main 处于“测试在、实现缺”且零容忍回潮，三个后续 Run 因此 BLOCKED | 提交 `8d37e5d` 恢复接线；强制流程升级：跨基线合入后必须跑受影响全包测试（不仅是目标包）；该事故同时证明分级引擎缺失时失败模式与 I-002 同构 |

**hardening 批次关闭记录（2026-08-07）**：六项张力中四项已实现并合入——WorkerResult 归一化（`328ea03`）、prompt 内嵌模板（同）、显式 abort + 终态 Outcome（ADR 0012，`08c8462`）、`--through-verify` 与仓库锁重试（`76fdf40`）；均经独立 Marshal Run 的 Verification 与 Review ACCEPTED。ADR 0013（拒绝分级）与 0014（read-only 画像）保持提案状态待维护者接受。tui-research 22 个死 Run 已用新 abort 转终态并回收 7 个干净 worktree；15 个 dirty worktree 待归档机制（cleanup v1 对 dirty 硬拒绝、无归档授权路径，记为下一缺口）。

## 2026-08-10 架构重置：已关闭的架构问题与新打开的实施风险

维护者于 2026-08-10 接受 [ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md)，把长期目标从“本地单次 CLI 编排”重置为长寿命 Runtime/Control Plane。本节记录该重置关闭的架构问题（含首批文档评审识别的四个 P1 缺口，R-A5–R-A8）与新打开的实施风险；目标架构见 [Runtime 架构](runtime-architecture.md)。

已关闭的架构问题：

| ID | 级别 | 问题 | 关闭 |
| --- | --- | --- | --- |
| R-A1 | P1 | 长期目标停留在“本地单次 CLI 编排”，远程队列与分布式调度笼统延后，长期形态无冻结路线 | ADR 0016 冻结目标与 M7–M12 唯一平台路线，实施计划、愿景、Roadmap 与治理文档口径同步 |
| R-A2 | P1 | ADR 0015 的常驻形态、耐久调度与远程执行边界未闭合，且提案长期悬置 | ADR 0015 标记 Superseded before acceptance，生产部署边界由 ADR 0016 承接 |
| R-A3 | P1 | 执行环境语义混在 Worker 编排内，Provider 不可替换、`hardened` 声明无准入标准 | 冻结 `AgentAdapter`（prepare/decode/capability）与 `SandboxProvider`（Probe/Provision/Stage/Exec/Inspect/Signal/Checkpoint/Restore/Terminate/Reconcile）分层；`hardened` 以 conformance 证明 mount/network/resource/credential 强制为准入条件 |
| R-A4 | P1 | 权威状态与调度边界未定义，存在“双权威”与自研 workflow engine 风险 | 冻结耐久执行引擎 Port：Marshal versioned event/state 为业务权威，外部引擎（生产参考 Temporal）只承担传输保证，Activity 以 `commandId` + `expectedSequence` CAS 追加 Marshal 事件。该 Port 由 ADR 0017 §9 统一更名 `DurableExecutionEngine` 并冻结权威边界（Attempt 创建/retry 预算/rework/终态裁决只在 Core，delivery/activity retry 不创建 Attempt、不消费业务预算） |
| R-A5 | P1 | 提交入口（POST TaskSubmission）未界定网络暴露、调用者认证/授权与 repository/project 作用域；幂等归并可能把不同冻结输入的错误请求合并进既有 Run | ADR 0016、Runtime 架构、安全模型与实施计划冻结提交入口边界：M8/M9 默认仅 loopback/受信任本地边界；远程入口生产可用前必须 TLS、调用者身份、按 repository/project 授权与审计（M11 退出门禁验收）；幂等身份为 `(scope, idempotencyKey, requestDigest)`，同 scope+key+digest 返回既有 submission/run，同 scope+key 不同 digest 冲突 fail closed，不创建或归并错误 Run |
| R-A6 | P1 | 失联旧 Attempt 可能晚到上传 checkpoint/candidate/日志/证据引用，若接纳不校验 generation/fencingToken 会污染新 Attempt 的权威证据 | 所有 Attempt 回报与 Artifact/Checkpoint/Candidate/Evidence 接纳必须携带 attemptId/generation/fencingToken，并在权威写入边界以 expectedSequence/CAS 校验；陈旧 token 内容隔离留存为诊断材料，不进入当前 Evidence/Review/Publication；外部副作用继续经 SideEffectIntent/Receipt + reconcile 幂等；相应故障注入列入 M9 退出门禁 |
| R-A7 | P1 | Cloudflare 段落同时写 fail closed 与回退自托管 Provider，可能被实现为同一 Attempt 内透明降级，甚至从 hardened 降到 workspace-write，与 Run 冻结的最低 SandboxRequirements 冲突 | 统一为：失败的 Allocation/Attempt 先终止并对账；调度器仅可为新 Attempt 选择满足同一冻结 SandboxRequirements 与 assurance 下限的兼容 Provider；无兼容 Provider 时 Run 保持 BLOCKED（fail closed），绝不静默降低 profile 或复用旧 handle；ADR 0016、Runtime 架构、M10 退出门禁与本审计风险措辞已同步 |
| R-A8 | P1 | Project/Goal 被列为必须持久化的核心对象，但 M8–M12 无任何 Milestone 实现它，复杂任务编排被笼统推到 M12 之后，M7–M12 完成后只能运行彼此独立的 Task | 实施计划与 Roadmap 新增 M13 Goal orchestration 阶段（持久 Project/Goal、可审计计划与重规划、跨 Run 记忆/Artifact 引用、预算与终止条件、独立质量评估、人工干预与恢复，含 Goal、非目标、退出门禁与 dogfooding）；M7 只冻结对象语义、M13 实现 Goal 控制器，避免虚假完成声明 |

新打开的实施风险（不构成 Blocking Finding，由各 Milestone 退出门禁关闭）：

| ID | 风险 | 缓解与关闭条件 |
| --- | --- | --- |
| R-001 | 外部耐久引擎依赖与语义锁定 | Port 隔离 + 生命周期一致性测试；替换 Orchestrator 前一致性测试先行（M9） |
| R-002 | Cloudflare Sandbox SDK 处于 1.0 preview/Beta 且托管平台不可自部署 | live opt-in + fail-closed：探测失败或漂移时终止当前 Allocation/Attempt 并对账，新 Attempt 仅可分配满足同一冻结 SandboxRequirements 与 assurance 下限的兼容 Provider；无兼容 Provider 时 Run 保持 BLOCKED，不静默降级、不复用旧 handle；同一 conformance/E2E 通过后才可替换（M10） |
| R-003 | 恢复误判导致双写或丢证据 | DispatchLease generation/fencingToken + 故障注入测试集；kill -9 后 60 秒 Inspect/Reconcile 口径（M9） |
| R-004 | Warm reuse 引入跨任务污染 | 默认每 Attempt 独立 ephemeral sandbox；复用需相同 tenant/repository/trust-domain 且可证明 sanitization（M8 起） |
| R-005 | 事件账本膨胀拖慢恢复 | Continue-As-New 式换代与分段归档设计在 M9/M11 落地并测试 |
| R-006 | 过度承诺（把早期 PoC 宣传成多租户服务） | 文档口径绑定安全就绪等级；多租户保持评估项，威胁模型评审通过后才讨论 |
| R-007 | 多节点写入分离被绕过 | Worker/Verifier/Publisher 独立 workload identity 与写入域；越权 Fixture 必须失败（M11） |
| R-008 | 提交入口越界暴露或缺乏授权（未认证/跨 scope 提交进入调度） | M8/M9 仅绑定 loopback/受信任本地边界；远程入口必须 TLS + 调用者身份 + 按 repository/project 授权 + 审计，M11 退出门禁验收；幂等身份冲突语义防止错误归并（M9） |
| R-009 | Goal 编排自主失控、超预算运行或静默放弃 | 预算与终止条件强制并在触发时保存 Outcome；计划/重规划全部事件化可回放；独立质量评估不得自证；人工可随时干预（M13） |

## 首次 Sandbox SPI dogfood reject 增补（2026-08-10）

M8 的首次 Sandbox SPI 纵切 dogfood Run 以 reject 结束。阻塞证据（已完整内嵌于返工 TaskSpec，不再读取任何 `.marshal` Run/outcome/decision 文件）表明 ADR 0016 冻结的契约仍留有可歧义点，Local 与 Cloudflare 两个可替换实现无法按同一语义收敛。以下问题在该次 reject 中打开，由 [ADR 0017](adr/0017-provider-neutral-sandbox-contract.md) 冻结统一契约；该次 dogfood Run 的既有实现成果按**未接纳探索证据**对待，不计为 M8 实现进度。

已打开的问题（随 ADR 0017 于 2026-08-10 经 Round 2 独立验证与 ReviewDecision accept 后接受而关闭）：

| ID | 级别 | 问题 | 冻结位置（ADR 0017，已接受） |
| --- | --- | --- | --- |
| S-A1 | P1 | 单一 `executionProfile` 把权限与隔离保证级别压在同一维度，无法表达 read-only+hardened 等正交组合 | §1 `AccessMode × AssuranceLevel` 二维正交模型：兼容映射表、拒绝/降级规则、持久记录迁移 |
| S-A2 | P1 | `hardened` 可来自 Provider 自报 Enforcement，无独立签发、可密封、可撤销的证据形态 | §2 `ConformanceEvidence`：身份绑定、suite/probe artifact digest、逐维结果、evidenceDigest、有效期/撤销语义；证据拓扑见 S-B1；Local 永不 hardened，Cloudflare 无豁免 |
| S-A3 | P1 | Stage 允许只回显声明 digest，Provider 不对真实 bytes 重算 sha256，内容寻址名存实亡 | §3 inline（≤1 MiB/对象、≤16 MiB/请求）与 ArtifactStore locator 的 provider-neutral 选择；消费前后重算 sha256；篡改 fixture 必须让回显型实现失败 |
| S-A4 | P1 | 操作身份未完整绑定 task/run/attempt/role/allocation/generation/token，重放可不经过当前 lease fencing；Restore 丢响应对账与普通 replay 未分离 | §4 workloadRole/principal 拆分 + 完整身份元组 + canonical replay key；普通 replay 先过 lease fencing；Restore lost-response reconciliation 独立成路径 |
| S-A5 | P1 | Restore 的 in-place 恢复未定义双写语义，旧进程树可能在恢复后继续写 | §5 默认 replacement allocation：旧进程树终止并失效后以控制面单写者 CAS 激活新 generation；故障注入验收 |
| S-A6 | P1 | M8 常驻单节点纵切与 M9 形态未切分：提交入口、调度租约、Public API 与 Provider 版本化注册的形状未冻结；Provider 观测完成可能被误当成 ReviewDecision/safe-to-publish 宣布 | §6 版本化 Provider Protocol 与认证注册（CapabilitySnapshot 冻结字段、未知版本 fail closed、观测边界）；§7 DispatchLease 唯一状态机；§9 DurableExecutionEngine 权威边界；§10 C/S + embedded 形态与 wire contract；§12 M8 改为 embedded/local 纵切、M9 冻结 marshal-server 与 Public API |
| S-A7 | P2 | 规范化未统一声明复用仓库既有 JCS，遇重复 JSON member 的行为未定义 | §11 统一 RFC 8785 JCS；重复 JSON member 一律拒绝 |

Round 2 独立评审进一步打开并关闭的歧义（全部 P1；随 ADR 0017 接受于 2026-08-10 关闭）：

| ID | 级别 | 问题 | 关闭位置（ADR 0017） |
| --- | --- | --- | --- |
| S-B1 | P1 | 首轮文本要求证据采集 workload 运行在独立于被测 Provider 的 Verifier sandbox——只能测到 Verifier sandbox，测不到被测 Provider 的 mount/network/resource/credential 强制能力，且错误套用了业务成果独立验证拓扑 | §2 证据拓扑冻结：probe 定义、challenge/nonce、probe artifact digest、调度、out-of-band 观察、裁决与 ConformanceEvidence 签发由 Control Plane 与独立 Conformance Verifier 控制；probe workload 作为敌对测试负载运行在被测 Provider 创建、身份精确绑定的 target allocation 内；Provider 的 completed/receipt 只是输入，不能自签通过；M8 fixture 与各文档同义表述已同步修正 |
| S-B2 | P1 | 审计报告顶层 APPROVED 与增补节开放 P1 矛盾，可能让人或自动化提前把 M8+ 视为已获批准 | 本报告改为分层门禁：Local MVP M0–M6 保持 APPROVED/USABLE；Runtime/Sandbox 契约在 ADR 0017 接受前 BLOCKED；接受只关闭设计歧义，M8/conformance 状态不提前，Roadmap 保持 M7 IN_PROGRESS、M8–M13 PLANNED，dogfood 实现按未接纳探索证据对待（见报告头部与“实施门禁”节） |
| S-B3 | P1 | DurableOrchestrator/DurableExecutionEngine/backend retry 三套措辞并存，外部引擎可能被当成第二个 Attempt/retry 权威 | §9 统一 Port 名 DurableExecutionEngine；Temporal/Local Engine 仅是 backend；Core lifecycle policy/controller 独占 Attempt 创建、retry eligibility/预算、rework 与终态裁决；backend 只做相同 commandId 的 at-least-once delivery、timer、signal、crash recovery；delivery/activity retry 不创建 Attempt、不消费业务预算；Runtime/总体架构与实施计划已同步 |
| S-B4 | P1 | 只为 Pull 列出 capability matching、ack、heartbeat、deadline、generation 与 fencing，Push 可能退化为 fire-and-forget | §7 冻结 Push/Pull 共用的唯一 DispatchLease 状态机，只改变连接发起方；两者都绑定认证 registration、CapabilitySnapshot/ConformanceEvidence digest、task/run/attempt/allocation、generation/fencingToken，都具备 ack deadline、heartbeat、expiry、cancel、reconcile、generation bump 与陈旧结果隔离；Push 同样先 capability match，超时/响应丢失不产生第二个 active allocation；M9 增加两拓扑等价 conformance 与故障注入口径 |
| S-B5 | P1 | role 枚举扩成 worker/verifier/publisher/control-plane，冲突 Publisher 独立信任域不变量，把调用主体与 workload 身份混成可扩权枚举 | §4 拆分 workloadRole 与认证 principal：workloadRole 封闭枚举仅 worker/verifier；control-plane/publisher/operator/API caller 是不同语义 Port 上受 AuthZ 约束的 principal；Publisher 永不成为 Sandbox workload；远程请求另绑定 principal/portKind/providerType/audience/scope，Provider 不得借通用 role 取得跨 Port 能力；身份元组、fencing、Secret/Artifact 与安全模型表述已同步 |
| S-B6 | P1 | HTTP/JSON + OpenAPI 推迟到 M12、M9 首版 wire contract 与可恢复事件流未冻结；M9 禁止匿名 Pull 但注册身份来源与 AuthN/AuthZ 分工未定义 | §10/§12：M9 首版 Public API 采用 versioned HTTP/JSON + OpenAPI（Task create/get/cancel、Run approval/status、events/evidence），事件基线 SSE 支持 eventId/cursor 断线续传 + 轮询 fallback，WebSocket/gRPC 推迟；Provider remote transport 同样 versioned HTTP/JSON（Push 由 server 调 Provider endpoint，Pull runner 以 outbound-only long polling/streaming 领取同一 DispatchLease）；M9 提供最小 scope-bound 可撤销注册身份（入口可仅 loopback/trusted boundary），M11 扩展生产远程入口与 operator/API caller/多节点多用户 AuthN/AuthZ；M12 交付多语言 SDK、部署文档与多拓扑 conformance（不是首次定义 wire contract）；embedded CLI 经 in-process adapter 调同一 Public application Port，不直写 store |

后续实现的可执行小步（M8 内按序推进，每步先有 fixture 与失败判据）：

| 步骤 | 交付 | 完成判据 |
| --- | --- | --- |
| 1 | 二维要求 Schema 与映射校验器 | 旧 `executionProfile` 三取值确定性映射；AccessMode 升级与静默降级 fixture 全部失败 |
| 2 | ConformanceEvidence 签发链与校验 | 按 §2 证据拓扑执行：probe 定义/challenge/nonce、probe artifact digest、调度、out-of-band 观察、裁决与签发由 Control Plane 与独立 Conformance Verifier 控制；probe workload 作为敌对测试负载运行在被测 Provider 创建、身份精确绑定的 target allocation 内；被测 Provider 的 completed/receipt 只作输入，自签通过的 fixture 必须失败；逐维结果、有效期与撤销 fixture 全部按语义判定 |
| 3 | 内容寻址 Stage（inline/locator、大小上限、消费前后重算） | 篡改 bytes fixture 拒绝回显型实现；inline 超限被拒；digest 不一致上报 `StageInputMismatch` 并 fail closed |
| 4 | workloadRole/principal 拆分与身份元组、replay key 校验器 | workloadRole 封闭枚举仅 worker/verifier；Publisher 作为 Sandbox workload、借通用 role 跨 Port 取得能力的 fixture 全部失败；缺元组操作被拒；陈旧 lease replay 隔离为诊断材料；当前 lease replay 幂等归并 |
| 5 | replacement allocation Restore 与故障注入 | 响应丢失、并发 Restore、恢复后陈旧 handle 写入全部被拒；同一 Run/Attempt 单活跃 Allocation 断言通过 |
| 6 | Fake/Local Provider conformance 套件与 embedded/local 纵切 E2E | 两 Provider 通过同一套件（probe 按 §2 拓扑运行在各自 target allocation 内）；纵切全链路通过；Local 永不声明 hardened |
| 7 | Local MVP 全量回归 | 零回退 |

新增实施风险（不构成 Blocking Finding）：

| ID | 风险 | 缓解与关闭条件 |
| --- | --- | --- |
| R-010 | ADR 0017 尚在提案状态：提前启动 M8 实现或对外宣称 hardened 承诺会形成无法兑现的债务 | 已于 2026-08-10 随 ADR 0017 接受关闭；接受只关闭设计歧义，M8 实现须按修订后的实施计划重新启动，不做任何 hardened 对外承诺 |
| R-011 | 把 ADR 0017 接受误读为 M8 实现或 conformance 已完成，提前升级实施状态 | 实施状态分层记录：M7 保持 `IN_PROGRESS`、M8–M13 保持 `PLANNED`；首次 Sandbox SPI dogfood 成果按未接纳探索证据对待；conformance 通过以 M8/M10 退出门禁为准（Roadmap 状态与实施门禁同步） |

## Control Plane 与 Provider Port 边界冻结增补（2026-08-11）

维护者于 2026-08-11 在本任务全部 Gate 通过、独立 ReviewDecision accept 且无 P0/P1（含 Round 4 独立评审八项 P1、Round 5 复核四项残留与 Round 6 复核两项残留全部关闭）后接受 [ADR 0018](adr/0018-control-plane-and-provider-ports.md)，冻结 Marshal C/S Control Plane、按信任域分隔的 Provider Port、耐久注册/能力快照与在途 lease 撤销。该 ADR 澄清/部分取代 ADR 0017 §4/§6/§7/§8/§10/§12，并显式取代 ADR 0016 §6 经 ADR 0017 承接的 universal 接纳口径；Round 4 独立评审暴露的八项 P1（远程 transport 基线、securityDomainId 键空间、attestation 全链绑定、原子 fencing sink、SSE 恢复与再授权、engine 单一 seam、Port protocol family、legacy snapshot 残留）随 ADR 0018 §10–§16 一并关闭；Round 5 复核暴露的四项残留（复合安全域、Port protocol family 边界、Push/Pull 不变量等价、计划升级 bounded drain）随 ADR 0018 §2/§6/§7/§10/§16 修订关闭；Round 6 复核暴露的两项残留（Control Plane authority namespace 与 Provider actor 域分离、typed cross-domain edge）随 ADR 0018 §3/§10 修订关闭：authorityNamespaceId=(tenantNamespace, controlPlaneId, authorityScopeId) 拥有 Control Plane 权威对象、只允许 Core 写入，securityDomainId=(tenantNamespace, trustDomainKind, isolationDomainId) 只标识 Provider actor，跨信任域访问收敛为 Core 独占签发的 DispatchResultCapability/MaterialAccessGrant/PublicationAuthorization 三条 typed cross-domain edge 且默认拒绝（default deny），三条 edge 的 issuer 恒为 Core，issuer 不等于业务流的 sourceActor（DispatchResultCapability 的 sourceActor=Execution workload、targetAudience=Core result-ingress；MaterialAccessGrant 的 sourceActor=Data/Capability Provider、targetActor=Execution workload；PublicationAuthorization 的 issuer/sourceAuthority=Core、targetActor=Publication Provider）；已通过独立评审的远程 transport、attestation、原子 fencing sink、SSE、DurableExecutionEngine seam 与 legacy snapshot 修订完整保留，不回退；Runtime 架构、总体架构、安全模型、实施计划、Roadmap 与 ADR index 已同步。Round 8 复核进一步暴露并关闭一项残留（typed edge 跨域例外与适用范围）：三类 Core-authorized typed edge 明确为 Provider actor 跨 trust domain 访问默认拒绝规则的唯一 allowlist 例外，每次使用必须精确匹配 source/target securityDomainId 与该 edge 的全部对象、操作与时效绑定；Public API/SSE 使用各自的 AuthN/AuthZ、scope 约束与 re-AuthZ，Core 内部权威对象引用保留在 authority ledger，均不需要 Provider typed edge；会无条件拒绝 MaterialAccessGrant 等合法跨域访问、或把任何权威引用都强制经过 typed edge 的宽泛表述在 Round 8 当时并未全部清除（本轮更正该提前宣称）；Round 9 复核定位 ADR 0018 与 Runtime 架构、总体架构、安全模型、实施计划、Roadmap 状态的全部残留后，本轮已随 ADR 0018 §2/§3/§5/§7/§10 修订及 Runtime 架构、总体架构、安全模型、Roadmap 状态与本报告的同步清除。接受只冻结设计，不升级 M8–M13 实现/conformance 状态；M7 保持 `IN_PROGRESS`、M8–M13 保持 `PLANNED`。

已关闭的设计问题（随 ADR 0018 接受关闭）：

| ID | 级别 | 问题 | 冻结位置（ADR 0018） |
| --- | --- | --- | --- |
| C-A1 | P1 | 六类 Provider 未分信任域：低权限 Agent/Sandbox/Verification workload executor 与高权限 SCM/Publisher transport、凭据型 Artifact/Secret 共用一套 credential/AuthZ/审计/conformance profile，存在跨域提权面 | §2 三信任域（Execution / Publication / Data-Capability）+ §3 按 Port 分流的 required/forbidden 矩阵；域间不共享 credential、AuthZ、审计或 conformance profile；Publisher 永不成为 Sandbox workload |
| C-A2 | P1 | ADR 0016 §6 经 ADR 0017 §4 承接的 universal 接纳句无法区分 public-api 与注册/控制面——二者本应反向拒绝 workload lease 字段，universal 句却要求统一附加 providerType/lease 身份 | §3 身份按 Port 冻结、不设 universal envelope；public-api 禁止 providerType 并拒绝 workloadRole/allocationId/generation/fencingToken/DispatchLease；provider-registration/control 拒绝 workload lease；只有 dispatch-bound Port 绑定完整 lease 身份；§9 对照表显式取代 ADR 0016 §6 universal 口径 |
| C-A3 | P1 | Provider 注册无幂等身份与持久化约束：注册可 memory-only、CapabilitySnapshot 可变、legacy v1alpha1 快照映射未冻结，可能被静默补齐 scope/evidence | §5 ProviderRegistration 与不可变 ProviderCapabilitySnapshot；registrationId canonical 绑定 (principal, providerType, providerName, providerVersion, protocolVersion, scope) + idempotencyKey/requestDigest；同 key 不同 digest conflict；revoked/expired 不因普通 replay 复活；三类 expiry 独立；legacy mapper fail-closed 并记录 sourceCapabilitySnapshotDigest；禁止 memory-only registration |
| C-A4 | P1 | registration/快照/证据失效后 DispatchLease 命运未冻结，可能被实现为原地续租或静默降级 | §6 lease 只消费持久快照（registrationId/providerCapabilitySnapshotDigest/conformanceEvidenceDigests），引用/digest 永不改写只供审计；每次 heartbeat、接纳、reconcile 按当前 ledger 重判资格；revoke/expire/incompatible/supersede 使 active lease 立即失去资格（cancel/expiry + generation bump/fencing，终止对账，晚到结果隔离）；继续执行只能新 Attempt + 新 lease 重新 match |
| C-A5 | P1 | registry/queue/SSE 与事件账本的权威关系未冻结，cursor 压缩/过期/gap 后恢复不可判定；DurableExecutionEngine 的 Port 归属未明确 | §4 append-only event ledger 是唯一权威，snapshot/queue/SSE/registry/索引是可重建投影；SSE cursor 过期、gap 或压缩返回可判定 resync；DurableExecutionEngine 是 Core 的内部 Port |
| C-A6 | P1 | M8 注册/快照/撤销实施顺序未冻结，可能先 enable DispatchLease match 后补校验 | §7 M8 硬门禁顺序：negative fixtures/event contract → Schema → legacy mapper → durable embedded registration + ledger recovery → validation → 最后 enable DispatchLease match；前置缺失 claim/match fail closed；fixture 覆盖跨 scope/protocol、same key/different digest、revoked replay、restart/rebuild、substitution、claim 后失效的 Push/Pull |
| C-A7 | P1 | securityDomainId 同时承载 Control Plane 权威对象归属与 Provider actor 隔离，权威侧与 actor 侧键空间混同，事件账本、lifecycle 状态、ReviewDecision 与发布决定缺乏独立于 Provider 信任域的权威侧命名空间身份 | §10 冻结双键空间：authorityNamespaceId=(tenantNamespace, controlPlaneId, authorityScopeId) 拥有 Control Plane 权威对象（事件账本、lifecycle 状态、ReviewDecision、发布决定、idempotency/replay 权威记录、SSE cursor 权威序列），只允许 Core 写入；securityDomainId=(tenantNamespace, trustDomainKind, isolationDomainId) 只标识 Provider actor；authorityNamespaceId 不是 Provider 的 trustDomainKind 维度，Provider 不得写入或宣称权威对象；两键空间按职责分别进入持久主键/引用键空间；Provider actor 跨信任域访问默认拒绝，唯一 allowlist 例外是三条 Core 签发的 typed edge，未经对应 edge 授权或任一绑定不符一律 fail closed；Public API/SSE 与 Core 内部权威对象引用分别经各自 AuthN/AuthZ 与 authority ledger，不需要 Provider typed edge |
| C-A8 | P1 | 跨信任域请求（结果接纳、物料访问、发布授权）缺乏 typed edge，可能被实现为直接跨域传递 handle/credential 或隐式信任，默认拒绝不可机械证明 | §3 冻结三条 Core 独占签发的 typed cross-domain edge：DispatchResultCapability、MaterialAccessGrant、PublicationAuthorization，其余默认拒绝（default deny）；Core 是唯一签发者与唯一重新授权者；每条 edge 是 authority-scope-bound 权威记录，绑定 authority scope、issuer/source/target（issuer 为 Core，sourceActor/targetActor 按 edge 类型绑定）、attempt/allocation、expiry 与 digest；edge 不承载 raw credential/raw secret handle，不替代 ConformanceEvidence；Provider actor 跨 trustDomainKind 访问未经对应 edge 授权或任一绑定不符一律 fail closed；Provider 之间不得互相签发、转授或延展 edge |

新增实施风险（不构成 Blocking Finding，由各 Milestone 退出门禁关闭）：

| ID | 风险 | 缓解与关闭条件 |
| --- | --- | --- |
| R-012 | legacy v1alpha1 CapabilitySnapshot 被直接当作 Runtime 注册产物复用，绕过 mapper fail-closed 或静默补齐 scope/evidence | 显式版本化 mapper + sourceCapabilitySnapshotDigest 记录；缺失信息 fail closed（M8 退出门禁 fixture） |
| R-013 | registration/snapshot/evidence 失效后在途 lease 未被撤销，陈旧结果混入当前 Evidence/Review/Publication | 失效事件即时撤销 + generation bump/fencing + 晚到结果隔离；恢复 reconcile 按当前 ledger 重判资格（M8/M9 退出门禁故障注入） |
| R-014 | registry/SSE 被实现为第二个业务权威，cursor gap 静默续推导致客户端状态漂移 | registry/queue/SSE 仅作为账本投影；cursor 过期/gap/压缩返回可判定 resync（M9 退出门禁） |
| R-015 | 远程注册/Push/Pull 在 M9/M10 启用时先于传输身份上线，形成 credential/lease 可被窃听或重放的窗口 | ADR 0018 §12 transport 安全基线自首次 enable 生效：TLS 强制、mTLS/不可转移 workload identity、双向身份与 audience/scope 校验、rotation/revocation 与 replay protection；M9/M10 退出门禁明文 transport fixture 必须失败（M11 不补基线） |
| R-016 | securityDomainId 未进入持久主键/键空间，跨域 credential/AuthZ/audit/conformance 隔离不可机械证明 | ADR 0018 §10 现在冻结 securityDomainId 并进入全部键空间，未经三条 typed edge 中对应 active edge 授权或绑定不精确匹配的跨域引用 fail closed（三条 typed edge 是默认拒绝规则的唯一 allowlist 例外）；M8 Schema 落地时按 default 域接入，不等 M11 迁移；跨域引用 fixture 列入 M8/M9 退出门禁 |
| R-017 | backend workflow state 演化为第二调度权威，或双写窗口导致 command 与 ledger 分歧 | ADR 0018 §15 单一权威 seam（outbox/ledger-derived journal 二选一）+ commandId 从权威事实派生；backend 自决 lifecycle/retry/rework 的 fixture 必须失败（M9 退出门禁） |
| R-018 | SSE 被用作写通道（承载 ACK、lease heartbeat 或 command），投影获得写语义 | ADR 0018 §14 冻结 SSE 只读投影；承载 ACK/lease heartbeat/command 的 fixture 必须失败（M9 退出门禁） |

Round 4 独立评审进一步打开并关闭的 P1（全部 P1；随 ADR 0018 增补 §10–§16 于 2026-08-11 接受关闭）：

| ID | 级别 | 问题 | 关闭口径（ADR 0018） |
| --- | --- | --- | --- |
| ADR0018-REMOTE-TRANSPORT-BASELINE | P1 | ADR 0018 与 Runtime/M9 已启用远程注册和 Push/Pull，但 TLS 与调用者身份只作为 M11 远程入口门禁，远程 Provider credential/lease 可能先于传输身份上线 | §12 冻结：任何非 loopback/in-process transport 从首次 enable 起强制 TLS；workload-to-workload 优先 mTLS 或等价不可转移 workload identity；双向校验 server/provider 身份与 audience/scope，短期 credential rotation/revocation 与 replay protection；M11 只扩展 HA/多用户策略，不能补首次安全基线；同步 runtime/architecture/security/implementation-plan/roadmap 口径 |
| ADR0018-SECURITY-DOMAIN-KEYSPACE | P1 | registration/submission/lease/artifact/secret/cache/audit 缺乏机械安全域边界，无法证明域间 credential/AuthZ/audit/conformance 隔离 | §10 现在冻结 securityDomainId（单租户可固定 default，tenant 只作组成或前缀），经 Round 7 修订收敛为：actor 侧 securityDomainId 进入 registration/snapshot/evidence 携带项、lease/allocation actor 绑定、artifact/secret handle、cache、replay key 与 audit event 的引用字段，submission/run lifecycle、SSE cursor/sequence、idempotency 权威键等归权威侧 authorityNamespaceId；未经三条 typed edge 中对应 active edge 授权或绑定不精确匹配的跨域引用 fail closed；不等 M11 迁移持久主键 |
| ADR0018-PROVIDER-ATTESTATION-BINDING | P1 | attestation 仅绑定 principal/name/version/scope，相同软件版本可替换实例、配置或签发密钥后继续复用 hardened evidence | §11 冻结：ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence/lease claim 全链绑定 securityDomainId、稳定 providerInstanceId、effective configDigest、trust root（含 key id/rotation）；任一变化产生新 immutable snapshot/evidence 并触发 eligibility 重判；Worker/Verifier 不同 principal 与不同 allocation；高保证策略可要求 provider/host/failure-domain diversity；M8 补 substitution/config/key-rotation fixture（§7） |
| ADR0018-ATOMIC-FENCING-SINK | P1 | expectedSequence/CAS 只是抽象请求规则，ledger transition、当前 lease generation 与 Evidence/Artifact 引用未保证同一原子校验；旧 generation 可能先覆盖对象 key 再被 ledger 拒绝 | §13 冻结：权威 ledger sink 使用 atomic compare-and-append/transaction；Artifact/Evidence/Checkpoint/Candidate bytes 的接纳关系归 authority ledger，使用 authorityNamespaceId+run+attempt+allocation+generation scoped immutable key（actor securityDomainId 只作为 provenance 记录）、digest-verified put-if-absent；陈旧/冲突 bytes 只进 quarantine namespace；M8/M9 补 lost-response、concurrent-write、old-generation overwrite fixture |
| ADR0018-SSE-RECOVERY-AUTHZ | P1 | SSE 只有 eventId/cursor/resync，未冻结 scope/sequence、交付与去重、压缩恢复、背压或长连接重新授权 | §14 冻结：cursor 身份 authorityNamespaceId+scope+ledgerSequence（权威账本的权威侧身份；订阅方另绑定自身 securityDomainId 授权）、scope 内单调 sequence、at-least-once + eventId/sequence 去重、expiry/gap/compaction 的 deterministic resync 起点与 snapshot digest、heartbeat 与有界 backpressure、周期性与敏感变更即时 re-Authorization；SSE 是只读投影，不承载 ACK、lease heartbeat 或 command；参数值留 M9 Schema |
| ADR0018-DURABLE-ENGINE-SEAM | P1 | backend 虽声明非权威，但未关闭 ledger 已提交而 command 未投递（或反向）的双写窗口，Temporal/Local Engine 仍可能形成第二调度权威 | §15 冻结单一权威 seam：同事务 outbox 或 ledger-derived Core command journal 二选一；commandId 从权威事实稳定派生；backend 只消费/回报，workflow/activity state 不得成为业务权威；M9 backend profile 与升级 fixture 覆盖 workflow versioning/build ID、Continue-As-New、payload 外置/上限、activity heartbeat/cancel/retry |
| ADR0018-PORT-PROTOCOL-FAMILY | P1 | 现行入口仍可被理解为六类 Provider 共用 operation schema、audience 和 conformance，Publication/Secret 可能借通用 Provider 能力进入 Execution 语境 | §16 冻结 versioned protocol family：每个 Port 独立 audience、AuthZ scope、request/response schema、error/idempotency/revocation 与 conformance profile；只共享 transport、JCS 与最小 base auth primitives；禁止跨 Port token/schema/operation；embedded/Push/Pull 仅是同一 Port 内 adapter；实施计划与 Roadmap 已同步 |
| ADR0018-LEGACY-CAPABILITY-RESIDUE | P1 | Push capability match 与两拓扑绑定仍写 legacy CapabilitySnapshot/ConformanceEvidence digest，随后才要求 ProviderCapabilitySnapshot，与持久快照口径冲突 | 已直接改为比对/绑定持久 ProviderCapabilitySnapshot（providerCapabilitySnapshotDigest）+ conformanceEvidenceDigests 封闭集合（Runtime 架构 DispatchLease/Push 两拓扑、实施计划 M9 两拓扑绑定）；legacy CapabilitySnapshot 仅保留在 fail-closed mapper 来源语境（AgentAdapter probe 快照） |

Round 5 复核进一步打开并关闭的四项残留（全部 P1；随 ADR 0018 §2/§6/§7/§10/§16 修订于 2026-08-11 接受关闭）：

| ID | 级别 | 问题 | 关闭口径（ADR 0018） |
| --- | --- | --- | --- |
| ADR0018-COMPOSITE-SECURITY-DOMAIN | P1 | securityDomainId 仍是全系统单一标识（单租户固定 default），与其同时宣称的 Execution/Publication/Data-Capability 三信任域隔离冲突，域间隔离不可机械证明 | §10 改为复合 security namespace `(tenantNamespace, trustDomainKind, isolationDomainId)`：tenantNamespace 单租户可固定 default（tenant 只能作为该组成）；trustDomainKind 封闭枚举 execution/publication/data-capability；isolationDomainId 标识同 kind 内隔离边界；submission/run、registration/snapshot/evidence、lease/allocation、SSE cursor/sequence、artifact/secret handle、cache、idempotency/replay key 与 audit event 全部携带复合边界；未经三条 typed edge 中对应 active edge 授权或绑定不精确匹配的跨域引用与跨 trustDomainKind 引用 fail closed（三条 typed edge 是默认拒绝规则的唯一 allowlist 例外）；Runtime/总体架构/安全模型/实施计划/Roadmap 的单一 default 表述已同步清除 |
| ADR0018-PORT-PROTOCOL-FAMILY-BOUNDARY | P1 | 六类 Provider 仍被写成同一语义 Port、共享同一 conformance 套件，与按 Port 的 versioned protocol family 冲突 | §2/§16 澄清：六类 Provider 彼此是不同 Port、不同 protocol family，不共享 family/audience/schema/profile/suite/token/operation；对每个具体 Port/protocol family，embedded/in-process、Push HTTP、Pull outbound runner 才是该族的 transport adapter，运行该族统一的 conformance suite；runtime/plan/roadmap 中相反残留已清除 |
| ADR0018-PUSH-PULL-INVARIANT-EQUIVALENCE | P1 | Push/Pull 被写成“只改变连接发起方、其余语义完全等价”，不允许拓扑特定 transition/timing，conformance 比较口径不可执行 | §16 只冻结 outcome/invariant equivalence：唯一 claim、eligibility、fencing、deadline（ack/heartbeat/expiry）、无双活与晚到隔离；允许 topology-specific 的 offer/poll/claim/ack transition 与 timing；两拓扑 conformance 比较 normalized business trace 与业务不变量，不比较逐步 wire trace；runtime/architecture/plan/roadmap 的完全等价措辞已清除 |
| ADR0018-UPGRADE-DRAIN-SPLIT | P1 | 撤销与不兼容升级未分级：security-critical 场景可能保留 drain 窗口，普通升级可能被立即 kill、复活旧注册或改写旧 lease digest | §6/§7 分级冻结：security-critical revoke（credential compromise、protocol violation）立即 cancel + generation bump + kill，不留 drain 窗口；planned/ordinary incompatible upgrade 使用新 registration/新 snapshot，旧实例 stop-new + bounded drain，drain deadline 到期再 fence；事件机器可读原因码与审计记录分开；普通升级不得复活旧注册或改写旧 lease digest；M8/M9 补对应故障注入/退出门禁 |

Round 6 复核进一步打开并关闭的两项残留（全部 P1；随 ADR 0018 §3/§10 修订于 2026-08-11 接受关闭）：

| ID | 级别 | 问题 | 关闭口径（ADR 0018） |
| --- | --- | --- | --- |
| ADR0018-AUTHORITY-NAMESPACE-SEPARATION | P1 | securityDomainId 同时承载 Control Plane 权威对象归属与 Provider actor 隔离，权威侧与 actor 侧键空间混同，事件账本、lifecycle 状态、ReviewDecision 与发布决定缺乏独立于 Provider 信任域的权威侧命名空间身份 | §10 冻结双键空间：authorityNamespaceId=(tenantNamespace, controlPlaneId, authorityScopeId) 拥有 Control Plane 权威对象（事件账本、lifecycle 状态、ReviewDecision、发布决定、idempotency/replay 权威记录、SSE cursor 权威序列），只允许 Core 写入；securityDomainId=(tenantNamespace, trustDomainKind, isolationDomainId) 只标识 Provider actor；authorityNamespaceId 不是 Provider 的 trustDomainKind 维度，不属于 Provider actor 侧任何信任域，Provider 不得写入或宣称权威对象；SSE cursor 身份改为 authorityNamespaceId+scope+ledgerSequence（订阅方另绑定自身 securityDomainId 授权）；DispatchLease 双绑定两键空间；M8 Schema 落地时 Local MVP 记录按复合边界视图接入，不改写历史数据；Runtime/总体架构/安全模型/实施计划/Roadmap 已同步 |
| ADR0018-TYPED-CROSS-DOMAIN-EDGES | P1 | 跨信任域请求（结果接纳、物料访问、发布授权）缺乏 typed edge，可能被实现为直接跨域传递 handle/credential 或隐式信任，默认拒绝不可机械证明 | §3 冻结三条 Core 独占签发的 typed cross-domain edge——DispatchResultCapability（Execution 信任域结果/heartbeat/receipt 接纳）、MaterialAccessGrant（Data/Capability 信任域 scoped 访问短期能力）、PublicationAuthorization（Publication 信任域绑定 SideEffectIntent/ReviewDecision/evidence digest 的发布授权）——其余默认拒绝（default deny）；Core 是唯一签发者与唯一重新授权者；edge 是 Core 在 authorityNamespaceId 内签发的 authority-scope-bound 权威记录，绑定 authority scope、issuer/source/target（issuer 为 Core，sourceActor/targetActor 按 edge 类型绑定）、attempt/allocation、expiry 与 digest；edge 不承载 raw credential/raw secret handle，不替代 ConformanceEvidence；Provider actor 跨 trustDomainKind 访问未经对应 edge 授权或任一绑定不符一律 fail closed；M8 补伪造签发者/过期/撤销/raw handle/跨域 negative fixture |

ADR 0018 的接受不改变 Local MVP 的 `APPROVED_FOR_IMPLEMENTATION` / `USABLE` 结论，也不放宽任何既有不变量：Worker 不自证、单写入者、Worker/Verifier/Publisher 分权、ReviewDecision 证据绑定、fail-closed、Draft-only 与 merge never 均保持有效。

Round 7 复核进一步打开并关闭的三项残留（全部 P1；随 ADR 0018 §3/§4/§5/§7/§10/§13 修订于 2026-08-11 关闭；接受只冻结设计，不升级 M8–M13 实现/conformance 状态）：

| ID | 级别 | 问题 | 关闭口径（ADR 0018） |
| --- | --- | --- | --- |
| ADR0018-AUTHORITY-OBJECT-OWNERSHIP | P1 | 双键空间残留：仍存留“每个 Port 的请求一律携带 actor 安全域身份”的 universal 表述，submission/run 被归入 actor 侧记录，权威对象清单未收敛为 authorityNamespaceId 独占拥有，controlPlaneId 被写成具体进程实例，ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence 与 Artifact/Evidence/Checkpoint/Candidate bytes 接纳关系的权威归属未冻结 | §3/§4/§5/§10/§13 修订关闭：删除上述 universal 携带表述，Port 请求按矩阵规则绑定 actor securityDomainId；submission/Task/Run/Attempt/ledger/DispatchLease/Allocation/ReviewDecision/Evidence graph/Outcome/SideEffectIntent/Receipt reconcile/typed edge/idempotency/outbox/audit/SSE cursor 序列等权威对象一律由 authorityNamespaceId 拥有、只允许 Core 写入；ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence 也是 authority ledger 事实，仅携带 actor securityDomainId、provenance 与 eligibility，registrationId 幂等绑定中的 securityDomainId 为所携带的 actor 身份；Artifact/Checkpoint/Candidate/Evidence bytes 的接纳关系归 authority ledger；controlPlaneId 冻结为 HA/灾备中保持稳定的逻辑权威身份，不是进程实例；Runtime/总体架构/安全模型/实施计划/Roadmap 的残留表述已同步清除 |
| ADR0018-TYPED-EDGE-LIFECYCLE | P1 | 三条 typed edge 只绑定 authority scope、source/target、attempt/allocation、expiry 与 digest：operation 无封闭枚举、revocation/replay 语义缺失、每次使用无 current-ledger recheck、派生 token/handle 可能被缓存或离线校验成第二权威 | §3/§7 修订关闭：三条 edge 明确为 Core-only，冻结 issuer/source/target/operation/expiry/digest/revocation/replay/current-ledger recheck 七项生命周期要素与各自专属绑定（lease/generation、物料对象 key/content digest 封闭集合、SideEffectIntent/ReviewDecision/evidence digest）；issuer 恒为 Core 且不等于业务流的 sourceActor，sourceActor/targetActor 按 edge 类型绑定，target 是 securityDomainId 标识的 Provider actor，缺失任一要素 fail closed；每次使用都按当前 authority ledger 复核；edge 派生的 token/handle 只是指向 edge 权威记录的单向引用，自身不承载授权语义，派生 token/handle 不得成为第二权威；§7 补 Core-only typed edge fixture（伪造签发者/过期/撤销/operation 不符、绕过 recheck 使用派生 token/handle、raw handle/credential、以 edge 替代 ConformanceEvidence 必须失败） |
| ADR0018-PUBLICAPI-AUTHORITY-KEY | P1 | Public API 提交幂等身份的描述仍以 actor securityDomainId 为键空间组成，submission 幂等与对象 key 键空间未按权威对象归属收敛 | §3/§10/§13/§14 修订关闭：Public API 幂等提交身份为 `(authorityNamespaceId, scope, idempotencyKey, requestDigest)`（submission 与幂等权威记录由 authorityNamespaceId 拥有）；SSE cursor 身份维持 authorityNamespaceId+scope+ledgerSequence（各文档残留的按 actor securityDomainId 键控的 cursor 身份表述已修正）；Artifact/Evidence/Checkpoint/Candidate 对象 key 为 authorityNamespaceId+run+attempt+allocation+generation scoped，actor securityDomainId 只作为 provenance 记录；Runtime/总体架构/安全模型/实施计划/Roadmap 幂等表述已同步修正 |

Round 8 复核进一步打开并关闭的一项残留（P1；随 ADR 0018 §3/§10 修订于 2026-08-11 关闭；接受只冻结设计，不升级 M8–M13 实现/conformance 状态）：

| ID | 级别 | 问题 | 关闭口径（ADR 0018） |
| --- | --- | --- | --- |
| ADR0018-TYPED-EDGE-EXCEPTION-SCOPE | P1 | typed edge 适用范围残留两类宽泛表述：一类把跨 trustDomainKind 的访问写成无条件拒绝，可被解读为拒绝 MaterialAccessGrant 等合法跨域访问；另一类把权威对象的引用写成必须统一经过 typed edge，可被解读为 Public API/SSE 客户端访问与 Core 内部权威引用也必须持有 Provider typed edge | §3/§10 修订关闭：三类 Core-authorized typed edge 明确为 Provider actor 跨 trust domain 访问默认拒绝（default deny）规则的唯一 allowlist 例外，不是对跨域 raw handle/raw credential 或任意引用的豁免；每次使用必须精确匹配 source/target securityDomainId 与该 edge 绑定的全部对象、operation、Attempt/Allocation、generation、expiry/deadline、digest 和当前 authority ledger 状态，任一绑定不符 fail closed；Public API/SSE 是 Client 到 Control Plane 的入口，使用各自的 AuthN/AuthZ、scope 约束与 re-AuthZ，不需要 Provider typed edge；Core 内部权威对象引用（ledger 事件间引用、cursor、证据关系、outbox/ledger 引用）保留在 authority ledger 内，不需要 Provider typed edge；§7 补对应 negative/positive fixture；当时宣称“Runtime 架构、总体架构、安全模型、Roadmap 状态与本报告的残留宽泛表述已同步清除”与实际不符——ADR 0018 与 Runtime 架构、总体架构、安全模型、实施计划、Roadmap 状态仍残留无条件 fail closed 表述——该残留由 Round 9 复核（ADR0018-UNQUALIFIED-CROSS-DOMAIN-RESIDUE）定位并随本轮修订实际清除 |

Round 9 复核进一步打开并关闭的两项残留（全部 P1；随 ADR 0018 §2/§3/§5/§7/§10 修订于 2026-08-11 关闭；接受只冻结设计，不升级 M8–M13 实现/conformance 状态；本轮同时更正 Round 8 记录中在清除实际完成前提前宣称宽泛表述“已同步清除”的表述）：

| ID | 级别 | 问题 | 关闭口径（ADR 0018 及各文档） |
| --- | --- | --- | --- |
| ADR0018-UNQUALIFIED-CROSS-DOMAIN-RESIDUE | P1 | ADR 0018 与 Runtime 架构、总体架构、安全模型、实施计划、Roadmap 状态仍把跨域能力、securityDomainId 或 trustDomainKind 引用写成无条件 fail closed，与 MaterialAccessGrant 等三类合法 allowlist edge 冲突；本报告 Round 8 记录提前宣称该类宽泛表述已全部清除 | 逐处改为：仅未经三条 Core-only typed edge 中对应 active edge 授权，或 source/target securityDomainId、edge type、对象、operation、Attempt/Allocation、generation、expiry/deadline、digest、当前 authority ledger 状态任一不精确匹配的 Provider actor 跨域访问 fail closed；三条 typed edge 是默认拒绝规则的唯一 allowlist 例外，Public API/SSE 使用各自 AuthN/AuthZ 与 re-AuthZ、Core 内部权威引用保留在 authority ledger，均不需要 Provider typed edge；M8/M9 fixture 明确区分三类合法 positive 与无 edge、错 edge、错绑定 negative；本报告的提前清除宣称已更正 |
| ADR0018-NONEDGE-PORT-AND-SAME-DOMAIN-AUTHZ | P1 | provider-registration/control 的非 edge 授权路径未冻结；securityDomainId 相同只是 provenance/partition 条件的事实未冻结，可能被实现成同域 bearer grant | ADR 0018 §3/§10 冻结：provider-registration/control 不持有三类业务 typed edge，必须通过 transport identity、该 Port 的 AuthN/AuthZ、scope/protocol validation 与 registration protocol，由 Core 决定并把获准事实写入 authority ledger；securityDomainId 相同不构成授权，同域请求仍须逐项匹配具体 Port 的 principal/registrationId/providerInstanceId/scope/attempt/allocation/generation/operation 门禁；§7 与实施计划/Roadmap 补对应 positive/negative fixture，Runtime 架构、总体架构、安全模型同步 |

## ADR 建议

以下 ADR 共同构成当前 Local MVP 的架构与安全基线，建议一起接受：

1. [ADR 0001：CLI-first 模块化单体](adr/0001-cli-first-modular-monolith.md)
2. [ADR 0002：每个任务一个 Worktree](adr/0002-worktree-isolation.md)
3. [ADR 0003：Worker 与 Publisher 分权](adr/0003-separate-worker-and-publisher.md)
4. [ADR 0004：独立验证](adr/0004-independent-verification.md)
5. [ADR 0005：Go 作为 Core Runtime](adr/0005-go-runtime.md)
6. [ADR 0006：Attempt 控制根与业务 Worktree 分离](adr/0006-attempt-control-root.md)
7. [ADR 0007：先记录意图的受控发布与远端对账](adr/0007-intent-first-publication.md)
8. [ADR 0008：可插拔 Observer Backend](adr/0008-pluggable-observer-backends.md)
9. [ADR 0009：原生 PTY Terminal Session 执行传输](adr/0009-terminal-session-execution.md)
10. [ADR 0010：受控自治、审批 Gate 与人工介入](adr/0010-controlled-autonomy-and-intervention.md)
11. [ADR 0011：密封启动与可判定的原生 TUI 传输](adr/0011-sealed-native-tui-transport.md)

删除 ADR 0002–0004 中任何一个都会使本批准失效，并要求重新进行安全与生命周期审计。

[ADR 0016：耐久 Runtime 与可插拔 Sandbox Provider](adr/0016-durable-runtime-and-sandbox-provider.md) 已于 2026-08-10 被维护者接受（其决策来源为当日维护者对长期目标的明确修正与批准），并将 ADR 0015 置于 Superseded before acceptance；ADR 0016 冻结的不变量集合与上表 Local MVP 不变量一致，放宽任何一条同样要求重新审计。

[ADR 0017：Provider-neutral Sandbox 安全契约](adr/0017-provider-neutral-sandbox-contract.md) 已于 2026-08-10 在全部 P1 通过 Round 2 独立验证与 ReviewDecision accept 后被维护者接受；它澄清/部分取代 ADR 0016 的 §4/§5/§6/§7/§9，关闭首次 Sandbox SPI dogfood reject 暴露的合同级缺口（S-A1–S-A7）与 Round 2 六项歧义（S-B1–S-B6）。接受只关闭设计歧义，不构成对 M8 实现或 conformance 完成的声明；相应实现仍须逐项通过 Milestone 退出门禁。

[ADR 0018：Marshal C/S Control Plane 与按信任域分隔的 Provider Port](adr/0018-control-plane-and-provider-ports.md) 已于 2026-08-11 在本任务全部 Gate 通过、独立 ReviewDecision accept 且无 P0/P1（含 Round 4 独立评审八项 P1、Round 5 复核四项残留与 Round 6 复核两项残留全部关闭）后被维护者接受；它澄清/部分取代 ADR 0017 的 §4/§6/§7/§8/§10/§12，并显式取代 ADR 0016 §6 经 ADR 0017 承接的 universal 接纳口径，关闭本轮设计评审暴露的 C-A1–C-A8、Round 4 独立评审的八项 P1（见“Control Plane 与 Provider Port 边界冻结增补”节）、Round 5 复核的四项残留（复合安全域、Port protocol family 边界、Push/Pull 不变量等价、计划升级 bounded drain）与 Round 6 复核的两项残留（Control Plane authority namespace 与 Provider actor 域分离、typed cross-domain edge）与 Round 7 复核的三项残留（双键空间残留清除、Core-only typed edge 生命周期细化、Public API 幂等/SSE/对象 key 修正为 authorityNamespaceId）与 Round 8 复核的一项残留（typed edge 跨域例外与适用范围，见“Control Plane 与 Provider Port 边界冻结增补”节）与 Round 9 复核的两项残留（跨域 fail closed 表述精确化、非 edge Port 与同域不自动授权，见“Control Plane 与 Provider Port 边界冻结增补”节）。接受只冻结设计，不升级 M8–M13 实现或 conformance 状态；M7 保持 `IN_PROGRESS`、M8–M13 保持 `PLANNED`，实施须按修订后的实施计划与 M8 顺序硬门禁逐项通过退出门禁；各远程能力首次 enable 必须满足 ADR 0018 §12 transport 安全基线，M11 不补首次基线。

## 实施门禁（分层）

文档审计和维护者接受均已完成。实施必须：

1. 从 Milestone 0 开始，不提前执行 Worker 或 Publication Side Effect；
2. 每个 Milestone 满足 Exit Criteria 后才能进入下一阶段。

分层结论：

- **Local MVP（Milestone 0–6）**：**`APPROVED_FOR_IMPLEMENTATION`** / `USABLE`，该范围实施门禁已开启且保持不变；
- **Runtime/Sandbox 契约（M7–M13）**：ADR 0017 接受前为 `BLOCKED`；2026-08-10 接受后设计歧义关闭，实现可按修订后的[实施计划](implementation-plan.md)推进，但任何 Milestone 的完成与 conformance 通过仍须以对应退出门禁与独立证据为准，不得因 ADR 接受而提前声明；2026-08-11 ADR 0018 接受后，Control Plane 与 Provider Port 边界口径连同权威/actor 双键空间（authorityNamespaceId=(tenantNamespace, controlPlaneId, authorityScopeId) 拥有 Control Plane 权威对象；securityDomainId=(tenantNamespace, trustDomainKind, isolationDomainId) 只标识 Provider actor）、三条 Core 独占签发的 typed cross-domain edge（DispatchResultCapability/MaterialAccessGrant/PublicationAuthorization，默认拒绝）、attestation 全链绑定、原子 fencing 写入汇、SSE 恢复与再授权、engine 单一权威 seam、按 Port protocol family、Push/Pull outcome/invariant equivalence 与失效处置分级一并冻结；Round 7 复核三项残留随 ADR 0018 §3/§4/§5/§7/§10/§13 修订关闭——权威对象清单收敛为 authorityNamespaceId 独占拥有（ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence 为 authority ledger 事实仅携带 actor securityDomainId/provenance/eligibility，Artifact/Checkpoint/Candidate/Evidence bytes 接纳关系归 authority ledger，controlPlaneId 为 HA/灾备中保持稳定的逻辑权威身份而非进程实例）、三条 Core-only typed edge 冻结 issuer/source/target（issuer 恒为 Core 且不等于业务流 sourceActor；sourceActor/targetActor/targetAudience 按 edge 类型绑定）/operation/expiry/digest/revocation/replay/current-ledger recheck 与专属绑定（派生 token/handle 不得成为第二权威）、Public API 幂等/SSE cursor/对象 key 使用 authorityNamespaceId；Round 8 复核一项残留随 ADR 0018 §3/§10 修订关闭——三类 Core-authorized typed edge 是 Provider actor 跨 trust domain 访问默认拒绝规则的唯一 allowlist 例外（每次使用精确匹配 source/target securityDomainId 与全部对象、操作、时效绑定），Public API/SSE 使用各自 AuthN/AuthZ 与 re-AuthZ、Core 内部权威引用保留在 authority ledger，均无需 Provider typed edge；Round 9 复核两项残留随 ADR 0018 §2/§3/§5/§7/§10 修订关闭——删除会无条件拒绝 MaterialAccessGrant 等合法 typed edge、或把任何权威引用都强制经过 typed edge 的宽泛表述（跨域 fail closed 一律限定为未经对应 active typed edge 授权或绑定不精确匹配），provider-registration/control 不持有三类业务 typed edge（经 transport identity、该 Port AuthN/AuthZ、scope/protocol validation 与 registration protocol，由 Core 写 authority ledger），securityDomainId 相同只是 provenance/partition 条件、不构成授权，M8/M9 补三类 edge positive 与无 edge/错 edge/错绑定 negative fixture；实施仍按 M8 顺序硬门禁（negative fixtures → Schema → mapper → ledger recovery → validation → 最后 enable DispatchLease match）推进；任何非 loopback/in-process 远程能力首次 enable 必须满足 ADR 0018 §12 transport 安全基线，M11 不补首次基线。

因此本报告当前存在四项未关闭 P1：Issue #53 的 CI rework evidence 缺口，以及 ADR 0032 B2 独立复核重开的三项 authority/delivery recovery 缺口；详情分别见对应增补节。`APPROVED_FOR_IMPLEMENTATION` 结论只适用于 Local MVP 范围，不能被解释为受控 merge 已支持；Runtime/Sandbox 部分按上述分层状态执行。

## Milestone 7 最终关闭审计（2026-08-11）

Milestone 7（架构与契约）于 2026-08-11 通过退出门禁，Roadmap 状态更新为 `PASSED`。exact evidence 如下：

- Marshal Run `m7-control-provider-boundary-adr-r15-20260811` 完成 M7 最终架构稿，reviewRound=2，32/32 required Gates 通过，独立审查无 P0/P1，Run 进入 `ACCEPTED`；
- GitHub [PR #13](https://github.com/chiga0/marshal-harness/pull/13) 通过 Quality (ubuntu-latest)、Quality (macos-latest)、Secret scan 与 GitGuardian 检查（CI run `31449333738`），2026-08-11 由维护者手工合入 main（merge commit `4b2f3248f24ec2a67642ec77822fe6bb59730df7`，非 auto-merge）；
- M7 退出门禁逐项满足：ADR 0016/0017/0018 已接受，治理与文档口径一致，Local MVP 全量回归通过且本仓库 CI 全绿。

本次关闭只记录设计与契约阶段通过：M8–M13 保持 `PLANNED`；Runtime 实现、Sandbox SPI conformance、`marshal-server`/Public API、Cloudflare Provider 与 Goal 编排均未实现，本节不对其中任何一项作出完成声明。

本报告前文“M7 保持 `IN_PROGRESS`”的记录为 ADR 0017/0018 接受时点的状态；自 2026-08-11 起 M7 状态为 `PASSED`。本节不引入新的 Blocking Finding，不改变 Local MVP `APPROVED_FOR_IMPLEMENTATION` / `USABLE` 结论。

## 确定性控制面、补偿与 Goal Roadmap 增补审计（2026-08-11）

维护者要求以局外人视角重新审计“稳定 Runtime 持续接收和分发复杂任务”的终态。三路独立只读审计分别检查 Typed Execution、SideEffect/Compensation 与 M13 Goal 编排，结论是：ADR 0016–0018 的 Durable Control Plane 与 Provider 分层方向正确，无需改成 LLM Supervisor 或自由 P2P；但以下合同级缺口必须在实施前关闭。维护者据此接受 [ADR 0019](adr/0019-deterministic-control-plane-typed-execution-and-goal-admission.md)。

| ID | 原级别 | 问题 | ADR 0019 关闭口径 | 实现状态 |
| --- | --- | --- | --- | --- |
| D-A1 PLAN-AUTHORITY | P1 | Planner/主 Agent 可能被实现为直接创建 Run、写状态的第二 Supervisor | Supervisor 明确定义为确定性 Core；Planner 只提交 proposal，Core deterministic admission 后才 materialize | M13 `PLANNED` |
| D-A2 TYPED-RESULTS | P1 | Candidate/Evidence/Assessment/Receipt 可能被通用 Executor 结果混同 | 四类输出独立接纳；共享执行基座但按 Port 隔离 Schema/principal/credential/conformance | M8–M12 `PLANNED` |
| D-A3 GRAPH-BOUNDS | P1 | Goal DAG 缺累计规模、跨 revision cycle、预算 reservation 与重规划不变量 | 冻结 effective graph 校验、整个 Goal 累计 guardrail、先 reserve 后 dispatch、immutable revision | M13 `PLANNED` |
| D-A4 EVIDENCE-ELIGIBILITY | P1 | 跨 Run/上游 Artifact 改变后的 Evidence 适用性无法判定 | Evidence 不可变；以 dependency set 与追加 eligibility event 做局部失效 | M13 `PLANNED` |
| D-A5 HUMAN-RESUME | P1 | Run `BLOCKED` 已是终态，却缺少跨日人工等待语义 | 不改 Run 生命周期；Goal `PAUSED`/resume 负责等待并重新校验/fence | M13 `PLANNED` |
| D-A6 COMPENSATION | P1 | 通用 SideEffect 只在设计中，失败易被误称为 rollback | append-only intent/receipt/reconcile；补偿是新副作用，按 disposition class 与 Policy 控制 | M8–M12 `PLANNED` |
| D-A7 ORPHAN-CLEANUP | P1 | expired cleanup 删除 Run 目录后再移除 worktree，崩溃可能留下无法从 runs 枚举恢复的 orphan | M8/M9 建 authority-scoped cleanup ledger 与 orphan reconcile fixture | M8/M9 `PLANNED` |

设计 Finding 已由 ADR 0019 关闭，但实现风险保持开放并明确映射到 M8–M13；不得把本次文档接受描述为功能已实现。M7 保持 `PASSED`，M8–M13 保持 `PLANNED`，Local MVP `USABLE` 不变。

ADR 0019 首稿的最终独立复核另发现并关闭五项 P1：按 Port 接纳被错误泛化为 universal generation 校验；Core 内部 SideEffect 记录可能被误作跨 Port wire Schema；Goal pause 未闭合 active Run 处置；budget reservation 缺 settle/release/expire/reconcile；M8 共享执行基座措辞可能绕过 ADR 0018 §7 顺序。修订后分别冻结：dispatch-bound 才校验 lease generation/fencing；各 Port receipt 经版本化 fail-closed mapper 进入内部 authority record；`drain-active|cancel-active` 不直接改 Run state；append-only reservation 状态机；任何 claim/lease activation 仍位于 ADR 0018 §7 硬顺序最后一步。复核后无未关闭 P0/P1。

## Docs v2 信息架构审计（2026-08-11）

打开并关闭文档可发现性问题 `DOCS-IA-1`：旧 Pages 主导航同时暴露规范、实现细节、19 份 ADR、Milestone 报告、研究与英文摘要，新读者无法判断正确入口，也容易把历史材料当成当前承诺。

关闭措施：

- 主导航收敛为“开始 → 理解 Marshal → 使用 Marshal → 构建与扩展 → 更多资料”，只展示 13 个高频当前页面；
- 新增快速开始、核心概念、参考索引和历史档案四个分层入口，并把旧总体架构重写为当前 + 目标的最新整体架构；
- ADR、审计、研究、Milestone Scope/报告/Review、兼容性矩阵和英文摘要默认隐藏，但保留稳定 URL、搜索与审计可追溯性；
- 规范冲突顺序明确为 Accepted ADR → Runtime/lifecycle/security/Schema → 专项契约 → 实施/Roadmap → 指南 → 历史；
- 删除门槛收紧为“完全重复、空白且无审计价值”。本轮没有文件满足安全删除条件，因此不以 Git 历史替代仍被引用的审计材料。

该整理不改变信任边界、持久化契约、生命周期或发布权限，不触发新 ADR。

## 产品定位一致性复核（2026-08-11）

打开并关闭文档一致性问题 `DOCS-POSITIONING-1`：README 与 Pages 首页先以“证据门禁式 Coding Agent 编排器”和 Local MVP 描述 Marshal，再把长寿命 Control Plane 写成未来目标。这会让当前交付阶段反向定义产品边界，与 ADR 0016–0019 及现行整体架构不一致。

关闭措施：

- README、Pages 首页、愿景、核心概念、整体架构、Runtime 架构和站点元数据统一以“面向 Agent 驱动软件工程的长寿命、可自托管、确定性 Control Plane”定义产品；
- 明确 Runtime 持续接收 Goal/Task，将复杂需求接纳并分发为有界 typed workload；Agent、Sandbox 与 durable backend 均可替换，不能成为第二业务权威；
- 把 Local MVP 统一改写为当前可用的 embedded/local 先行实现与回归基线，并在独立状态区说明 M8–M13 尚未交付；
- Roadmap、实施计划、安全模型、操作手册、参考索引和英文入口同步区分“产品定义”与“当前成熟度”；
- 保留历史 Milestone、ADR 与审计原文的时点表述，不重写历史证据。

本次只修正文档叙事，没有改变 ADR 已冻结的信任边界、持久化契约、生命周期或发布权限，因此不触发新 ADR。后续首页不得再以 Local MVP 能力清单替代产品定位，也不得把 `PLANNED` 能力写成已经交付。

## 用户文档与工程规范分层复核（2026-08-11）

打开并关闭可读性问题 `DOCS-AUDIENCE-1`：Pages 虽已缩减导航，但仍直接发布并索引 Runtime 架构、安全模型、ADR、Schema 术语与实现门禁。普通用户必须理解内部对象名、Digest、Lease、fencing 与 ADR 修订链才能阅读截图所示页面，信息架构仍以开发者而非用户任务为中心。

关闭措施：

- Pages 只构建 8 个用户页面：首页、产品说明、当前能力、快速开始、日常使用、Codex 使用、工作原理、安全与隐私；
- Runtime/整体工程架构、ADR、Schema、审计、研究、Milestone、开发指南、实施计划与详细 Operator Runbook 继续由 Git 版本控制，但通过 `exclude_docs` 排除在 Pages HTML 和搜索索引之外；
- 用户页面不再出现 authorityNamespaceId、securityDomainId、ProviderCapabilitySnapshot、fencingToken、协议族或 ADR 修订链等实现术语；
- README 同步改为用户入口，只保留价值、当前能力、最小流程、安全边界和贡献入口；
- 当前能力页负责区分已交付与在建能力，避免简化表达演变成虚假功能承诺。

本次只改变发布信息架构与面向用户的解释层，工程规范内容及其权威顺序保持不变，不改变信任边界、持久化契约、生命周期或发布权限，不触发新 ADR。

## ADR 0032 B2 受控合并实现审计增补（2026-08-17）

ADR 0032 B2 初轮复核曾把五项实现缺口记为关闭。随后独立复核证明，其中 authorization 与 delivery 的 sidecar monotonic head 和正文记录处于同一故障域，不能检测协调回滚；分步持久化仍有 crash dead zone；恢复还可能基于变化后的时间/check observation 为不同 digest 重签授权。因此旧关闭口径被后续证据部分推翻，以下表格按最新证据修正。该修正不把受控合并误报为 M10 完成，也不改变 M11–M13 状态。

| ID | 级别 | 状态 | 关闭口径 |
| --- | --- | --- | --- |
| `ADR0032-B2-PUBLISH-DEADEND` | P1 | `CLOSED` | `Publish` 同时支持冻结的 `mergePolicy=never|policy`；`policy` 必须绑定 `eligible-after-policy` ReviewDecision，生成同策略的 PublicationIntent/PublicationRecord 并停留于 `CI_PENDING`，只有 `publication.merged` 可进入 `ACCEPTED`。 |
| `ADR0032-B2-AUTHORIZATION-BYPASS` | P1 | `REOPENED`（拆分见下） | 精确绑定与 mutation 前 recheck 仍有价值，但同故障域 sidecar 不能证明整体未回滚，authorization→intent 分步写也不是原子 authority fact，不能据此宣称 crash window 已关闭。 |
| `ADR0032-B2-BASE-BRANCH-UNBOUND` | P1 | `CLOSED` | `SCMMergeTarget` 同时携带 `baseBranch` 与 `baseOid`；fresh admission、ObserveReady recovery 以及每一次 ready/merge mutation 的紧邻 preflight 都重新观察并对照 repository、PR、head、base branch/base OID、Marker 与 Draft 状态，任一漂移在 mutation 前进入 `BLOCKED`。 |
| `ADR0032-B2-CHECK-EVIDENCE-ORPHAN` | P1 | `CLOSED` | fresh `RemoteCheckRecord` 以 canonical digest 为文件名持久化不可变 bytes；恢复与 C7 重建重新校验 Schema、重算 digest，并核对 task/run/repository/request/head/status/requiredChecks，缺失或篡改不得收敛。 |
| `ADR0032-B2-UNBOUNDED-MERGE-FAILURE` | P1 | `REOPENED`（拆分见下） | 本地 attempt/result 记录限制了正常路径重试，但同故障域整体回滚可重置计数，attempt→result 两阶段 crash 也不能安全区分 applied/not-applied；需 journal-bound pending anchor 与 Inspect/Reconcile。 |

| 新 ID | 级别 | 状态 | 问题与关闭条件 |
| --- | --- | --- | --- |
| `ADR0032-B2-AUTHORITY-ROLLBACK-DOMAIN` | P1 | `OPEN` | authorization/intent 与 sidecar head 可协调回滚；由 ADR 0033 `MergeAuthorityTransaction` 同 journal 原子事实关闭本地分步窗口，production supported 还必须等待 M11 external rollback witness 与协调回滚恢复演练。 |
| `ADR0032-B2-DELIVERY-CRASH-WINDOW` | P1 | `OPEN` | delivery attempt/result 分步写存在 unknown 副作用窗口，预算可随同域回滚重置；关闭要求 Core-only pending/fence-consumed/observed/resolved CAS append、fence closed Schema/producer-authority/same-state allowlist、journal/anchor sequence reducer、canonical replay identity 与 crash hydration，且 pending snapshot 后执行 mutation-adjacent current/journal/expiry recheck **AND** single-use fence、fence journal+snapshot durability-before-handoff、revoke/authority append 与 fence→Provider handoff 的同一线性化顺序，以及 unknown/lag 保持 unresolved 并可重复 Inspect（含 concurrent consume、consume→crash→restart no-replay 与 restart lag→receipt-visible fixture）。deadline 后匹配 late receipt 必须原子关闭 pending 并复用 ADR 0026 唯一终态例外收敛 Outcome。Publisher 只提供 typed observation/provenance，不能裁决权威结果。 |
| `ADR0032-B2-RECOVERY-RESIGN` | P1 | `OPEN` | 恢复可能重观察 `requestedAt`/check freshness 并为不同 digest 重签；由 prepared transaction 精确 bytes hydrate、同 identity 不同 digest conflict 关闭。 |
| `ADR0032-B2-EXECUTABLE-SNAPSHOT-TOCTOU` | P2 | `OPEN` | snapshot 路径本身可在校验后被替换；ADR 0033 要求通过已校验 fd/immutable handle 执行同一对象并约束 config dir handle。 |

残留 `#160` 保持 P2 `OPEN`：更通用的共享 outbox/投递观测与运维视图仍应由后续切片处理；它不能替代 ADR 0033 的 merge 专属 authority/delivery journal facts。ADR 0033 未接受且 A–D 未全部实现前，`mergePolicy=policy` 必须保持 unsupported；A–D 通过后最多启用显式 opt-in 的 local/non-production 受限 profile，production supported 仍以 M11 external witness/fence 恢复门禁为前提。

## Issue #137 Qoder live conformance authority 审计增补（2026-08-17）

Qoder production authority wiring 候选提交的独立审计确认四项 P1：生产 trust root 首次改变 Adapter admission 却无 ADR；Seal 用常量替代 verifier 实测 profile；证据只绑定 OS/arch 而可跨 host 重放；一次 Bind 后长寿命进程不再消费撤销。另有父级 symlink/owner 路径边界与无上限 TTL 两项 P2。ADR 0034 据此提出三方 authority、完整 observation、OS-attested host-key identity、24 小时 freshness、逐段 nofollow 和 Probe/launch 逐次复核契约。acceptance 复核又阻止了 hostname 碰撞重放、单一 generation JSON 被删后降级、签名 record/trust rotation 协议未冻结与同 UID 普通 subprocess 冒充 sandbox 四项 P1；目标契约现要求 OS monotonic fence anchor、完整 canonical/domain-separated signature 协议、三角色 key ledger 以及独立 OS principal 的 capability/syscall/path denial。后续复核再发现四份 receipt 无同轮链、receipt 未绑定 OS denial audit、evidence signer 缺 key epoch、operator/provider root rotation 无 anti-rollback continuity、provider advance receipt 未绑定 prepared transaction，以及所谓 exact schema 仍缺 nested type/cardinality；ADR 候选已补充 probeRun chain、IsolationAudit、OS trust-root ledger、transaction/prepared digest 绑定和封闭类型约束。clean-origin 复核又要求 root authorizer 与 record-chain 分离、activate-before-revoke/remaining replacement 续签，以及可机械解析的非敏感 argv/environment manifest 和 document/path/time/epoch 边界；随后复核修正了误拒未来 validUntil、credential digest 离线指纹风险与 generation 首链歧义，候选改为一次性 OS capability identity，并冻结 initialization/每个 prepared/committed 的精确前驱。最终独立复审 P0/P1/P2/P3 均为 0，维护者于 2026-08-18 接受 ADR 0034；接受只关闭合同缺口，不表示真实 evidence 或 production enablement 已完成。

| ID | 级别 | 状态 | 关闭条件 |
| --- | --- | --- | --- |
| `QODER-AUTHORITY-ADR-MISSING` | P1 | `CLOSED-CONTRACT` | 维护者已接受 ADR 0034；当前生产构造器仍无条件 typed fail-closed，接受 ADR 不自动启用，须独立后续变更。 |
| `QODER-LIVE-VERIFIER-PIPELINE-MISSING` | P1 | `OPEN` | 实现受限、只读且无仓库写权限的真实 executable verifier、closed typed observation schema 与独立 signer；其 evidence 必须机械导出完整 argv/environment manifest 并与精确 executable/host/challenge identity 绑定，不能只提交 opaque digest。 |
| `QODER-OBSERVATION-NOT-EXACTLY-BOUND` | P1 | `IMPLEMENTED-PENDING-REVIEW` | observation 逐项携带 suite/artifact/challenge/capability/profile/argv/env/tool/event/protocol/permission/transcript/verdict/time，Seal 不注入期望值；独立复审与真实 probe 证据分别通过。 |
| `QODER-HOST-BINDING-REPLAYABLE` | P1 | `IMPLEMENTED-PENDING-REVIEW` | verifier/evidence/consumer 三方精确 host fingerprint，跨 host fixture fail closed。 |
| `QODER-AUTHORITY-LIFECYCLE-NOT-RECHECKED` | P1 | `REWORKED-PENDING-REVIEW` | 候选 consumer 的 Probe 与 launch guard 每次重读 current config/evidence/revocation/generation；generation high-water 已扩展为 consumer-owned 私有 nofollow root 中的跨进程/重启耐久记录，以 advisory lock 串行化并按 file fsync→renameat→directory fsync 原子提交，绑定完整 config canonical digest，拒绝 rollback/同代替换，且在 missing/revoked evidence leaf 前先消费。终审 rework 进一步同时持有 fence dirfd 与实际解析 leaf 的同一 evidence dirfd，按 device/inode 与双向祖先关系拒绝同目录、路径别名及双向嵌套，并把 lock/record 收紧为精确 `0600`、single-link regular file；完整负向矩阵待再次独立复核，生产仍硬禁用。 |
| `QODER-AUTHORITY-PATH-BOUNDARY` | P2 | `IMPLEMENTED-PENDING-REVIEW` | config/root/evidence 全路径逐段 nofollow、leaf `O_NONBLOCK`+`fstat`、owner/private mode 与 FIFO 负向 fixture 通过。 |
| `QODER-CONFORMANCE-FRESHNESS` | P2 | `IMPLEMENTED-PENDING-REVIEW` | validity window 与 observation age 固定最多 24 小时；超长与陈旧 fixture fail closed。 |

该候选实现与 CI 生成的 Ed25519 key/fake executable 只验证机制，不是 credentialed live evidence；ADR 0034 虽已接受，但当前 host 尚未配置外部真实 evidence，也未完成独立 production enablement，Issue #137 保持打开，Qoder 不得被报告为已完成或当前部署 `supported`。启用序列固定为 ADR 接受后先只落地仍与 consumer 分离的 isolation/receipt/verifier/signer tooling，再产生真实 evidence 并通过负向矩阵，最后以单独变更启用 consumer/registry；调度优先级只能在 required CI、secret scan 与当前 host doctor 全绿后变更。

## Issue #136 Codex production authority 审计增补（2026-08-18）

Codex production eligibility 首次引入 Adapter 本地准入 trust root、可撤销 evidence、consumer generation fence 与 authenticated fd-exec，属于信任边界和耐久契约变更。ADR 0037 候选经多轮独立复审，最终 P0/P1/P2/P3 全部为 0；维护者于 2026-08-18 接受 [ADR 0037](adr/0037-codex-cli-production-authority.md)。合同现冻结 verifier/receipt/evidence/config/launch authority 分权、Worker 零 authority signing key、稳定 TPM-backed `hostIdentityDigest` 与逐次 fresh nonce、单一 active-root-pin/fence 原子状态及每个 fsync/rename 边界恢复、source→sealed→child 合法 topology 转换、逐次撤销复核和 ReviewPacket 精确证据绑定。

| ID | 级别 | 状态 | 关闭条件 |
| --- | --- | --- | --- |
| `CODEX-AUTHORITY-ADR-MISSING` | P1 | `CLOSED-CONTRACT` | 维护者已接受 ADR 0037；接受只冻结合同，不自动启用 production constructor 或 registry。 |

ADR 0037 接受不表示实现、真实 evidence 或 enablement 已完成。Issue #136 与相关 milestone 保持未完成；当前 Codex 部署仍须 production hard-disable，不得报告为 `supported`。Darwin 在等价 authenticated fd-exec 合同由后续 ADR 接受前继续 fail closed。

## Qoder/Codex 共享 Production Authority Provider 审计增补（2026-08-18）

Qoder 与 Codex 的 production consumer 实现复核进一步证明：两个 Adapter 虽已有封闭 evidence/config/consumer 合同，但当前仓库和宿主仍没有可独立 provision 的在线 verifier、外部 OS isolation/audit receipt authority、host attestation/monotonic anchor、stopped-child launch barrier，以及可原子交付 keyset/revocation/config/evidence 的 authority provider。把 fixture、同 UID helper 或若干普通文件直接接到 registry，会让 Adapter/Worker 所在故障域为自己的准入证据和 rollback 状态背书。

[ADR 0038](adr/0038-agent-production-authority-provider.md) 已于 2026-08-18 接受，以独立本机 `AgentProductionAuthorityProvider` Port、外部 principal、held-fd IPC、atomic bundle+monotonic fence 和 Prepare/Commit/Inspect launch barrier 补齐该层。共享仅限基础设施；Qoder/Codex 继续分别运行 ADR 0034/0037 的 exact profile 与 conformance。接受只冻结实现合同，没有把任何当前部署升级为 `supported`。

| ID | 级别 | 状态 | 问题与关闭条件 |
| --- | --- | --- | --- |
| `AGENT-AUTHORITY-PROVIDER-MISSING` | P1 | `OPEN` | 当前缺少与 Marshal/Worker 分离的在线 verifier、Secret direct-delivery、isolation/receipt/evidence/config/rotation/revocation/recovery/launch authority principal 及最小认证 IPC。关闭要求 ADR 0038 被维护者接受，shared Port/peer credential/operation AuthZ/opaque capability handoff/held-fd/role-key separation 实现和负向矩阵通过；同 UID helper、fixture、普通 subprocess 或 `sandbox-exec` 单独不能关闭。 |
| `AGENT-AUTHORITY-ATOMIC-BUNDLE-AND-BARRIER` | P1 | `OPEN` | keyset/revocation/config/evidence 与 high-water 尚无跨 crash 的原子 current bundle；Worker launch 尚无 receipt durable-before-release barrier 与 lost-response Inspect/Reconcile。关闭要求 detached manifest signature、bounded leaf batch、授权 prepare/rotate/revoke/recovery transaction、外部 monotonic anchor、prepared→anchor→committed transaction、协调回滚检测，以及精确绑定 authority namespace/fixed roots/mount namespace 的 stopped-child Prepare/Commit/Abort/Inspect、单次 release 与 kill/wait crash matrix 全部通过。 |
| `AGENT-AUTHORITY-PLATFORM-CONFORMANCE` | P1 | `OPEN` | Linux/Darwin 的强制隔离、host identity、execution identity 与 audit producer 尚未形成真实支持矩阵。关闭要求 Linux Qoder/Codex 分别通过 ADR 0034/0037 全矩阵和非作者真实 credentialed probe；Darwin 在替代强制机制与独立 ADR 通过前保持 `unsupported`，不能以 pathname execution、codesign 摘要或 `sandbox-exec` 降级。 |

## Darwin Codex Mac-first 实施提案（2026-08-19）

为支持当前 macOS 宿主，新增 ADR 0040（Proposed）冻结 Darwin authenticated launcher 的实现边界：独立签名 launcher、Mach-O held identity、child pre-workload barrier、exec-away/exec-back 负向证据与真实 credentialed conformance。该提案不放宽 ADR 0037/0038 的 fail-closed 结论；在维护者接受、真实 evidence、独立 consumer enablement 与 doctor/live probe 全绿前，Codex 仍必须报告 `unsupported`。Linux authority/runtime 延后，不作为 Mac-first 的前置条件。
| `AGENT-AUTHORITY-PEER-EXEC-SWAPBACK` | P1 | `OPEN-IMPLEMENTATION`（ADR 0039 Accepted） | held `/proc/<pid>/exe`、pidfd 与收包前后 pathname sampling 不能排除 `exec-away → send → exec-back`。ADR 0039 已于 2026-08-18 在精确独立复审 P0/P1 清零后接受并冻结合同；关闭仍要求实现 root trusted launcher、USER_NOTIF 只放行一次经 `pidfd_getfd` 验证的初始 held-FD `execveat`、随后永久 exec deny、独立 launch attestation/逐连接 nonce receipt、不可转移 client/server helper 与双向 bootstrap，且真实 Linux 负测必须证明 application handler 调用数为零且无 response。接受本身不关闭该 finding，也不启用 production transport。 |

上述 finding 是 Issue #136/#137 production enablement 的共同前置阻塞，不改变 M10 在途及 M11–M13 `PLANNED` 状态。只有 shared Port conformance 与对应 profile conformance、当前宿主 doctor、撤销/rollback/kill 演练、required CI 和 secret scan 全绿后，才能分别提交 Qoder 或 Codex 的独立 registry enablement 变更。

## Mac 普通用户模式审计（2026-08-19）

用户明确授权先按 Qwen/OpenCode 同级普通用户模式使用 Qoder 1.1.23 与 Codex 0.145.0。实现采用 `MARSHAL_QODER_MODE=ordinary-user` 与 `MARSHAL_CODEX_MODE=ordinary-user` 的显式 opt-in；未设置时严格 authority 路径仍 fail closed。普通模式继续固定 absolute path、realpath、SHA-256、版本、超时、输出、环境、worktree 边界与 WorkerResult 校验，但不宣称 signed authority、APAP credential、child barrier 或恶意代码 sandbox。doctor 输出 `authorityMode=ordinary-user`，因此该能力不会与严格 production authority 证据混淆。

## Darwin APAP transport 实机审计增补（2026-08-19）

当前 macOS 宿主对 `AF_UNIX/SOCK_SEQPACKET` 返回 `protocol not supported`，导致原 APAP client 即使 endpoint 存在也无法连接。实现已加入 Darwin 专用四字节大端长度帧 `SOCK_STREAM` 与 `SCM_RIGHTS` 累积接收，并以实机 payload+held-FD 测试、race、vet、staticcheck 与 Darwin 交叉编译验证。该变更只关闭 transport 可达性缺口；[ADR 0041](adr/0041-darwin-apap-stream-transport.md) 仍为 Proposed，root-owned APAP provider、签名 launcher、独立 verifier、credentialed live probe 与 registry enablement 继续保持 `unsupported`。

## Darwin launchd 部署投影审计增补（2026-08-19）

`internal/darwin` 新增 deterministic root-owned launchd plist 生成器，固定 `com.marshal.apap` label、service binary、signed launcher binary、APAP endpoint、`RunAtLoad`、`KeepAlive`、Background process type 与 owner-only umask，并拒绝相对路径、路径穿越、根路径和 label 注入。该生成器不执行安装、不改变 launchd 状态、不验证或生成签名；真实安装仍要求外部 root provisioning、独立 launcher signer 与 service identity evidence，当前 doctor/registry 不因此升级。

## APAP credential ingress 实现审计增补（2026-08-19）

共享 provider seam 现加入独立 `CredentialIngress` server：只接受已认证 `SecretProvider` peer、精确一个 `credentialCapability` held fd，并在 handler 返回 typed `CredentialIngressResponseV1` 后立即关闭该 fd。APAP control server 不接收 capability，response 不返回 credential bytes 或 capability fd；Mac 实机 roundtrip、race、vet、staticcheck、交叉编译和 secret scan 通过。该实现只提供 transport/protocol custody，不产生 capability、receipt signing、isolation audit 或 credential authority；因此真实 credentialed live probe 与 registry enablement 仍保持 `unsupported`。

## Darwin launchd 配置记录审计增补（2026-08-19）

`internal/darwin` 现提供严格的 `marshal.darwin.launchd-deployment.v1` 配置读取器：配置文件必须是当前用户或 root 所有、owner-only 私有普通文件、RFC 8785 canonical JSON，并通过逐级 `openat` + `O_NOFOLLOW` 拒绝路径组件替换；未知字段、尾随数据、非 root APAP endpoint policy、缺失身份字段和非法路径均 fail closed。该读取器只产生部署预检输入，不携带签名私钥、credential 或 capability，也不安装、签名、bootstrap launchd；因此不能单独改变 Qoder/Codex 的 registry admission，外部 root provisioning、signed launcher、独立 verifier 和 credential authority 仍是未关闭前置条件。

本轮 doctor 增加 `MARSHAL_DARWIN_LAUNCHD_CONFIG` 的非敏感部署状态投影（`not-configured`、`unsafe`、`unavailable`、`ready`）。该字段与 APAP endpoint 状态一样仅供诊断，不能把普通配置文件或缺少 root-owned 对象的预检结果解释为 `supported`。

本轮宿主只读核查还确认：`security find-identity -v -p codesigning` 返回 `0 valid identities found`；`/Library/PrivilegedHelperTools`、`/usr/local/libexec` 和 `/opt/homebrew/bin` 没有现成 Marshal/APAP launcher；`MARSHAL_APAP_*`、`MARSHAL_DARWIN_*`、Qoder/Codex authority 环境变量均未配置。该证据支持当前外部 signer/root provisioning 阻塞判断，但不改变任何 registry 状态。

同轮再次以固定绝对路径读取并执行版本探针：Qoder `/Users/gawain/.qoder/bin/qodercli/qodercli-1.1.23` 输出 `1.1.23`、SHA-256 为 `sha256:b09566c33df68f8ee3e82783120f6eb885fbd9aeb5bc35beb4a85a3ea2d4219a`；Codex `/opt/homebrew/Caskroom/codex/0.145.0/codex-aarch64-apple-darwin` 输出 `codex-cli 0.145.0`、SHA-256 为 `sha256:1da3f4e0e96028b8a771814293c3033dafd1971f943f6c7e79b0897fe705f590`。这只证明 held executable 可读取且版本正确，不构成 authority、隔离或生产准入证据；PATH 中其它 Qoder identity 不得混入该证据。

## APAP launch-control typed slice 审计增补（2026-08-19）

共享 `authorityprovider` 现把 ADR 0038 的 `PrepareLaunch`、`CommitLaunch`、`AbortLaunch` 与 `InspectLaunch` 纳入已注册控制面，并为请求、receipt、release identity、状态和 digest 提供封闭 typed payload 校验。`PrepareLaunch` 强制八项 held-FD 角色、完整 identity/config/fence digest、nonce 与 deadline 绑定；commit/abort/inspect 不接受 credential FD，receipt 必须是 canonical 非空对象，未知状态和成功状态的 receipt 组合均 fail closed。Fake provider 仅用于协议与重放/负向矩阵测试，不执行真实 child release，也不产生 signer、OS isolation audit 或 credential authority。

本切片通过 authorityprovider 定向、race、vet、staticcheck、Darwin/Linux 编译与 `git diff --check` 后，仅关闭 typed protocol 缺口；root-owned launchd/APAP provisioning、独立 signed launcher/verifier、真实 credentialed probe/conformance 与 Qoder/Codex registry enablement 仍保持 `unsupported`。

随后补入 `LaunchCoordinator` 作为可复用的确定性 reducer：prepare/commit/abort/inspect 统一串行化，command replay 精确绑定 request digest 与 peer identity，重复 transaction/错 receipt/陈旧 CAS fail closed，lost response 只能通过 Inspect 收敛。只有 `CommitLaunch` 的 release linearization point 前进 provider sequence；prepare/abort/inspect 不前进。实际 OS stopped-child、kill/wait、receipt signing 与平台 principal 仍由外部 `LaunchEffects` 实现，coordinator 不保存 FD、不读取 credential、不执行 pathname，也不能单独启用生产 registry。

本轮继续补入 provider-owned `DurableLaunchJournal`：记录以 canonical JSONL 追加、`sequence`/`providerSequence` 双序列、CRC + SHA-256 digest chain、严格 transition allowlist、`fsync` durable-before-return 与重启 hydration；非 canonical、尾部截断、链断裂、重复 transaction、状态回退、provider sequence 越界均 fail closed。journal 文件通过逐级 `openat(O_NOFOLLOW|O_CLOEXEC)` 固定路径组件，并校验私有目录/文件的 owner 与 mode；`NewDurableLaunchCoordinator` 在接受请求前从该 journal 恢复 pending/released/aborted 事实，副作用成功但 journal 写失败保持 `launch-outcome-ambiguous`，不能把内存状态当作已提交。

该 journal 仍只是 provider state 的持久化 seam，不能冒充 ADR 0038 所需的外部不可回滚 monotonic anchor、signed receipt authority、root-owned stopped-child launcher、kill/wait 或 credentialed isolation。故本切片关闭了 launch transaction 的本地 crash-hydration 缺口，但 Qoder/Codex 生产 registry 仍保持 `unsupported`，直至外部 authority provision 与独立 conformance 证据齐备。

Darwin stream transport 同步收紧帧边界：发送端拒绝零长度/超 64 KiB payload，接收端在 header 与 payload 两段都拒绝 `MSG_CTRUNC`/`MSG_TRUNC`，不会把丢失的 SCM_RIGHTS 或截断字节当作可验证请求。该改动只增强 ADR 0041 提案中的 framing 负向边界，不改变 `SOCK_STREAM` 的外部 peer authentication、root-owned endpoint、signed launcher、credential ingress 与独立 verifier 前置条件。

新增 [Mac-first authority 交接清单](mac-first-authority-handoff.md)，把必须由宿主管理员提供的 OS principal、签名 launcher、root-owned launchd、credential ingress、不可回滚 anchor 及 profile-specific live probe 逐项列出，并提供只读核验命令。清单不包含私钥或 credential，也不把交接材料当作 registry enablement；当前宿主缺少这些外部对象的结论保持不变。

本轮新增 `scripts/macos-authority-preflight.sh` 只读预检，将固定 Qoder/Codex 可执行文件、签名 launcher、受管 Team ID、root launchd、codesigning identity 与非交互 sudo 等外部前置条件转换为稳定的 `PASS`/`BLOCKED` 输出和非零退出码。脚本拒绝 ad-hoc 签名，且不安装、签名、bootstrap、读取 credential 或修改 `.marshal/`；本机实测两个 CLI 文件存在，但外部 authority 前置条件仍为 `BLOCKED`，因此 registry 与 doctor 继续保持 `unsupported`。

预检进一步要求 APAP service、launcher 与 endpoint socket 由 root 持有且禁止 group/other write；仅“文件存在”或“launchd label 存在”不再被视为部署就绪。该检查仍是只读输入，不改变 registry admission。

本轮收紧预检的 executable identity 门禁：固定 Qoder/Codex 候选必须分别通过精确 `--version` 与 SHA-256 比对，默认绑定当前 Mac 上已核验的 `qodercli 1.1.23` 与 `codex-cli 0.145.0` 摘要；管理员若提供同版本的不同构建，必须显式覆盖摘要并重新取得独立 verifier/conformance 证据。该检查仍不签名、不安装、不读取 credential，也不把版本/摘要通过误判为 authority 或 production support；PATH 中的同名候选继续与 held identity 隔离。

同一预检切片进一步核对 `launchctl print` 的实际 service 投影，要求其中同时出现精确 APAP service 与 signed launcher 路径；仅 label 存在、或 label 指向错误二进制时均保持 `BLOCKED`。该检查仍只读，不执行 bootstrap 或 registry enablement。

最终宿主审计（2026-08-19）仍返回 Qoder/Codex identity 两项 `PASS`，其余 13 项 `BLOCKED`：APAP service、signed launcher、两者 root/private ownership、codesigning identity、两者 signature、两者 managed Team ID、root/private endpoint socket、root launchd label、launchd exact service+launcher binding、noninteractive sudo。`security find-identity -v -p codesigning` 返回 `0 valid identities found`，三个固定系统对象均不存在。该结果满足外部阻塞判据；在管理员提供受管 signing identity、root-owned launchd/APAP、credential authority 与独立 verifier/conformance 证据前，Qoder/Codex registry 必须继续 `unsupported`。

随后将 CapabilitySnapshot 中已有的 `executableDigest` 透传到 `marshal doctor --json` 的 Worker 投影，并以 Qoder/Codex 的支持态单测锁定该字段。doctor 只输出摘要，不输出 executable 路径、环境值或 credential；该字段用于审计精确身份，不能单独改变 `unsupported`/registry admission。

本切片将 `internal/darwin` 的严格 held-executable 观察抽象为 launcher 与 Qoder/Codex candidate 共用的 `OpenHeldExecutable`/`OpenHeldCandidate`。路径逐级通过 `openat` + `O_NOFOLLOW` 固定，外部 authority 可通过 `Duplicate` 取得同一 inode 的 SCM_RIGHTS 描述符，而原始 held descriptor 继续由 owner 持有；包内不提供 pathname exec 或普通子进程 fallback。该 seam 只关闭 candidate descriptor 交付与父路径替换缺口，不产生签名 receipt、OS isolation 或 registry enablement。

## Qoder ordinary-user 结果传输与 denial 证据审计增补（2026-08-19）

Qoder 1.1.23 的真实 Mac ordinary-user smoke 暴露三项实现缺陷：旧隐藏 symlink 别名会被 Provider 的路径安全检查拒绝；stream-json parser 丢弃 `assistant/tool_use` 与 `user/tool_result`，导致权限拒绝未进入 ADR 0013 denial log；ordinary-user CapabilitySnapshot 仍沿用 strict managed HOME/config 文案。旧测试同时直接向 control result path 注入 WorkerResult，形成 transport 假阳性，因此此前的 fixture 通过不能证明真实别名可写。

本切片把 Qoder adapter 升为 `0.1.1` 并更新 event contract：WorkerResult 改为 worktree 内非隐藏 single-link regular staging file，与 control result 始终为不同 inode；adapter 通过 held worktree dirfd、`openat(O_NOFOLLOW)` 与 exact-inode consume/unlink/fsync 收取有界声明，再写入 held control leaf，Worker 从不取得 control inode 的 pathname 或 hard-link capability。prompt 只允许一次 `Bash tee`，拒绝后不得换工具重试；parser 以 tool-use ID 关联结果，并按 9 份真实 Qoder 1.1.23 transcript 冻结大小写敏感工具词表与 string content shape，完整记录 toolCalls/toolNames/permission denial；fatal denial 固定为 typed do-not-retry，协议冲突固定为 `protocol-invalid/do-not-retry`。denial log 经 held output dirfd claim/write，并与 transcript metadata 的 benign/fatal 计数双向 fail-closed 校验。ordinary-user 能力说明同步为真实的宿主 `HOME/XDG` allowlist 继承，不再声称 managed config 或禁用宿主配置源。

| ID | 级别 | 状态 | 关闭条件 |
| --- | --- | --- | --- |
| `QODER-ORDINARY-RESULT-DENIAL-P1` | P1 | `IMPLEMENTED-REVIEWED-PENDING-LIVE-SMOKE` | 独立 reviewer 已确认 P0/P1/P2 均为 0；真实 Qoder 1.1.23 仍须只经新 staging transport 完成 fresh current-main smoke，并复核 denial fixture、protocol 关联、held-fd attack matrix、metadata/log 一致性与 current-main 身份后才可置为 `CLOSED`。 |

该修复不新增 authority、credential、sandbox 或发布权限，不改变 ADR 0042 的降级边界；严格 authority 的旧 `adapterVersion/eventContract` 证据因版本变化自动失效，必须重新取得独立 conformance evidence，不能迁移旧摘要。

后续真实 ordinary-user smoke 发现：Qoder 成功读取的源码正文可能包含 permission marker；若 parser 扫描任意 `tool_result.content`，普通文件字节可伪造 denial 并中止 Attempt。修复后只接受同一事件内按 `tool_result_meta.id` 精确绑定的 `permission-rule`，或仅含一个 `tool_result` 时无歧义的 `tool_use_result.isHardFailure=true`；duplicate、orphan、unknown kind 与多结果 hard failure 均 fail-closed。由于 denial authority 语义发生变化，Qoder adapter 升为 `0.1.2`，event contract 升为 `qoder-stream-json-1.2.0-v3`，使旧 `0.1.1/v2` conformance evidence、suite digest 与 authority bundle 自动失效。该变更不扩大普通用户模式权限，也不把它描述为 hardened authority。

tee-last/held-inode 修复的后续独立复核又发现一项 P1：WorkerResult transport 已从 Worker 可寻址 staging 改为 launch 前 unlink 的 Adapter-held inode，但 conformance identity 仍停留在 `0.1.2/v3`，旧 evidence 因而可能授权未验证新 transport 的运行。修复候选把 Adapter 升为 `0.1.3`、event contract 升为 `qoder-stream-json-1.2.0-v4`，新增确定性 `workerResultTransportDigest` 并将其纳入 suite、live observation、每份 execution receipt、普通 evidence 与 ADR 0034 exact evidence consumer。digest 封闭绑定 staging basename/type/mode、`O_EXCL|O_NOFOLLOW|O_CLOEXEC`、launch 前 unlink 与 `nlink=0`、Worker 不获得 path/fd、staging/control 不同 inode、held dirfd/exact-inode commit/consume/cleanup、唯一 canonical Bash input/command、exactly-once tee-last、denial extractor 和 transcript contract；逐字段 mutation 都必须同时改变 transport/suite digest并被当前 consumer 拒绝，正确重签的旧 `0.1.2/v3` evidence 也必须 fail closed。该代码关闭候选仍待独立 reviewer；无论代码评审结果如何，Mac ordinary-user 必须为 `0.1.3/v4` 重新取得真实 conformance，旧 evidence、摘要和历史 smoke 结论均不得迁移，也不得把 ordinary-user 描述为 hardened authority 升级。

2026-08-20 的首个 `0.1.3/v4` Mac ordinary-user smoke 又发现一项 P1 compatibility 缺口：真实 Qoder 1.1.23 的 final Bash `tool_use.input` 是 canonical `{command,description}`，其中 `description` 是 Provider 自动附加的非执行 metadata；v4 fixture 只伪造 `{command}`，导致真实唯一、成功且最后的 tee 被误判成 `invalidAccess`。修复候选升级为 Adapter `0.1.4` / event contract `qoder-stream-json-1.2.0-v5`，只接受 canonical `command` 与非空、合法 UTF-8、最多 512 bytes 且无控制字符的 `description`，继续拒绝缺字段、未知第三字段、非 canonical JSON、变体 command、重复 tee 与 post-tee tool call；真实 transcript 的脱敏 shape 加入回归。transport/suite digest 与所有 consumer 因 identity bump 自动失效旧 `0.1.3/v4` evidence。该 smoke 还暴露输出父目录不存在时 Worker 会用 `ls`/`mkdir` 进行确定性环境探索；Skill 已要求受限 Bash 任务在零 Attempt preflight 机械证明每个 deliverable 父目录存在，否则先改用已有目录或修输入。本项在独立 review、定向/race/static/schema/secret/merge-tree 门禁及 fresh v5 Mac smoke 通过前不得置为 `CLOSED`，也不得把 ordinary-user 描述为 hardened authority。

同日后续真实 `0.1.4/v5` Run 又暴露一项 P1 consumer-envelope 缺口：Qoder final tee 已声明完整语义字段和精确 `taskId/runId/attemptId/adapter.id`，但 Provider 省略了由 Adapter 持有的 `adapter.executable` 与 `adapter.version`；旧 consumer 在覆盖这两个字段前先做完整 Schema 校验，因而把有效声明误归类为 `result-missing`。修复候选升级为 Adapter `0.1.5` / event contract `qoder-stream-json-1.2.0-v6`，把 consumer policy 纳入封闭 WorkerResult transport digest：只允许 Adapter 在完整 Schema 前从 held executable identity 覆盖 `adapter.executable/version`，不合成任何语义字段或 task/run/attempt/adapter ID；重复字段、未知字段、缺失语义、身份漂移和其它无效声明仍 fail closed，并固定归类 `protocol-invalid/do-not-retry`。transport/suite、普通 evidence、ADR 0034 exact evidence consumer 与 transcript attestation profile 全部随新 identity 失效旧 `0.1.4/v5` evidence/receipt，必须补 fresh v6 Mac ordinary-user conformance。该变更属于 ADR 0016、0034、0042 已冻结的版本化 Adapter compatibility 演进，不改变信任边界、持久化、生命周期或发布权限，因此不新增 ADR；在同一独立 reviewer 关闭真实 diff 的 P1 且定向/race/static/schema/secret/merge-tree 与 fresh v6 Mac evidence 通过前，不得宣称 production ready。

同日 `0.1.5/v6` 的 fresh Mac ordinary-user Run 又暴露一项 P1 stream compatibility 缺口：真实 Qoder 1.1.23 会把同一个 assistant message（相同 `message.id`）分成 thinking、text 和多个独立 `tool_use` frame；旧 parser 把每个 frame 当成互斥消息，因而在第二个合法工具调用到达时错误归类为 `protocol-invalid/do-not-retry`。修复候选升级为 Adapter `0.1.6` / event contract `qoder-stream-json-1.2.0-v7`：同一 message ID 可累计不同 tool-use ID，完全相同的 ID/name/canonical input 重放只折叠一次；ID 在关闭前变化或消失、同 ID 冲突重放、未知/非法 `stop_reason`、结果早于 `tool_use` 关闭、终态仍有未关闭 message 或 final 后追加 frame 均 fail closed。transport/suite、普通 evidence、ADR 0034 exact evidence consumer 与 transcript attestation profile 随 identity bump 自动拒绝旧 `0.1.5/v6` evidence，必须补 fresh v7 Mac ordinary-user conformance。该变更仍属于 ADR 0016、0034、0042 已冻结的版本化 compatibility 演进，不改变信任边界、持久化、生命周期或发布权限，因此不新增 ADR；独立 review 与 fresh v7 live evidence 完成前不得宣称 production ready。

## Issue #138 Verifier worktree mutation 审计增补（2026-08-18）

Python acceptance command 生成 `__pycache__/*.pyc` 的 dogfood 证明：旧 Verifier 虽能在命令后观察到 Candidate worktree 变化并把 Gate 标为失败，却让污染字节留在受管 worktree；随后 Review 的 current-observation guard 正确拒绝变化后的字节，Run 因而无法生成绑定原 Candidate 的 ReviewPacket。该问题不是新生命周期或 Schema 缺口：ADR 0027 已冻结 command 写作用域默认为 `none`、未声明写入 fail closed、Candidate 与 Evidence 不可被覆盖；缺失的是实现级 command 隔离。

| ID | 级别 | 状态 | 关闭条件 |
| --- | --- | --- | --- |
| `ISSUE138-VERIFIER-WORKTREE-MUTATION` | P1 | `IMPLEMENTED-PENDING-INDEPENDENT-REVIEW` | 每条 candidate/Baseline command 使用 fresh standalone 临时 Git 副本；原 Candidate 复制前后身份一致；缓存与临时目录定向到副本外的命令专属临时根；submodule、特殊文件、逃逸/悬空/循环 symlink 在启动前拒绝；副本 before/after digest 不同形成稳定 `verifier-worktree-mutated` fail-closed Gate，同时保留原 command exit/signal/log；命令 pass/fail/cancel 后原受管 worktree 与 Candidate/patch digest 保持不变且 ReviewPacket 可重建；定向、race、全量 quality 与独立审查 P0/P1 清零后方可置为 `CLOSED`。 |

该切片不新增 TaskSpec temporary-directory 字段，不改变 VerificationReport/ReviewPacket Schema、Run 状态、doctor/status、发布权限或 ADR 0027 normalizer 规则，也不把普通宿主临时目录描述为 hardened sandbox。

## Issue #53 CI 失败 rework 注入设计缺口审计增补（2026-08-14）

公开 [Issue #53](https://github.com/chiga0/marshal-harness/issues/53) 要求为 CI 失败 rework 闭环冻结可恢复、可审计且无双计数的契约。基线（main commit `981b53d`）行为定位：PublicationRecord 已建立且 Run 位于 `CI_PENDING`；RemoteCheckObserver 产出 `status=fail` 的 RemoteCheckRecord；`publication.checks-failed` 当前只把 `headSha` 写入事件并进入 `REWORK_REQUESTED`；execution 的 CI origin 分支返回空 findings；`task review` 仅接受 `REVIEW_PENDING`——既无法把失败检查的权威证据绑定到新 Decision，也无法把精确 `requiredOutcome` 投影给下一 Attempt；fail 分支预算守卫也只检查 rework round、不检查 attempt 余额。

| ID | 级别 | 状态 | 关联 | 问题 | 定位 |
| --- | --- | --- | --- | --- | --- |
| `ISSUE53-CI-REWORK-EVIDENCE-P1` | P1 | `OPEN`（目标契约草案已提出（ADR 0030，Proposed，待接受）；实现待后续 successor） | [Issue #53](https://github.com/chiga0/marshal-harness/issues/53) | CI 失败闭环缺少 typed evidence 身份、ReviewDecision 绑定入口、findings 投影与预算终态/重放语义，rework 在无证据、无决策留痕下进行 | **契约级缺口**：缺的不是数据可得性（RemoteCheckRecord 已携带全部失败事实），而是承载它的契约——由 [ADR 0030](adr/0030-ci-failure-rework-evidence-and-injection.md)（Proposed，草案已提出/待接受）给出目标契约（接受后冻结）：一等不可变 `CIFailureEvidence`、ReviewPacket typed CI 扩展与 ReviewDecision 绑定、`review.rework` 的 `ci-checks-failed` 命名自环、双预算守卫与 `publication.checks-rework-budget-exhausted` 封闭终态、execution lineage 精确消费、canonical digest/事务/重放规则。不放宽任何信任边界：Worker 不自证、Provider/Publisher 不宣布 ReviewDecision、Merge 仍禁用、不改变 ADR 0028 ciDeadline/completedAt 裁决 |

已否决的前置方案（反例结论由任务上下文提供，未读取任何历史 Run 内容）：上一轮实现尝试经 Review 正式 REJECTED——它只试图在 CLI/reducer 内允许 `REWORK_REQUESTED` 自环，未冻结 typed CI evidence、RemoteCheckRecord 摘要绑定、execution lineage 消费与 attempt budget 终态，且双计数/重放语义不明确。后续 implementation successor 不得复制该补丁。

关闭条件（全部满足后 `ISSUE53-CI-REWORK-EVIDENCE-P1` 方可置为 `CLOSED`）：

1. 维护者接受 ADR 0030（ApprovalRecord，接受只冻结契约，不提前升级实现状态）；
2. implementation successor 按 ADR 0030 实施切片合入（schemas/catalog、publication observation、lifecycle/replay/runstore、review importer/packet、execution prompt lineage、CLI 与 doctor）；
3. ADR 0030 测试矩阵全部通过（typed evidence happy path、每个 binding mismatch、mixed/duplicate checks、self-loop cardinality 与 lost-response、conflict、两类预算耗尽终态/Outcome、counter 两轮示例、未注入拒绝、prompt 精确消费与 operational retry、snapshot 丢失/落后、Rebuild/doctor repair、崩溃注入与旧记录兼容）；
4. 独立 Verification/Review 与全仓库 CI 无回归；确认 Merge 仍禁用、ADR 0028 裁决与 RemoteCheckRecord Provider 事实所有权未被改变。

后续 implementation successor 记录：本增补只做契约设计与审计定位，不实现代码/Schema；implementation successor 由维护者另行创建 TaskSpec，其范围以 ADR 0030 实施切片与测试矩阵为准。本增补不改变权威 [Roadmap 状态](roadmap-status.md)：M7–M9 `PASSED`、M10 在途、M11–M13 `PLANNED`；不改变 Local MVP `APPROVED_FOR_IMPLEMENTATION` / `USABLE` 结论，也不改变任何已冻结的信任边界、持久化契约或发布权限。

## Adapter 结构性失败准入审计增补（2026-08-20）

Core 曾忽略 `AdapterFailure.retryDisposition`：即使 Adapter 已把 `protocol-invalid` 或 `provider-terminal` 标为 `do-not-retry`，`execution` 仍只按剩余预算进入 `RETRY_PENDING`，导致同一 Run 启动没有信息增益的下一 Attempt。修复后，Core 统一消费 typed disposition：`retryable` 继续受双预算约束，`blocked` 与 `do-not-retry` 立即进入 `BLOCKED`，生成 Outcome，并把封闭 `adapterId`、`failureKind`、`retryDisposition` 与 `failureSignature` 记录到 append-only `worker.failed`。唯一分类表固定为 `quota-exhausted → blocked`，`rate-limited`/`dns-failure`/`connection-failure → retryable`，`protocol-invalid`/`result-missing`/`provider-terminal → do-not-retry`；Qwen、Qoder 与 Codex 的 `result-missing` 都不会启动第二个 Worker。WorkerResult Schema/身份错误由 Core 明确转换为 `protocol-invalid/do-not-retry`。按已接受的 [ADR 0036](adr/0036-adapter-run-boundary-fail-closed.md)，`Adapter.Run` 返回的普通 error 固定转换为 `protocol-invalid/do-not-retry`，legacy `port.Permanent` 固定转换为 `provider-terminal/do-not-retry`；只有合法 typed `retryable` 才能授权下一次 operational retry，Core 不解析 provider 自由文本推断终态。

Core 入场不再直接信任第一个 `errors.As` 命中：错误图必须恰有一个具体 `AdapterFailure` carrier，并重新经过闭合构造器校验 Adapter/kind/disposition、hint 正值/24h 上界/互斥与时间窗口。未知枚举、非法配对、Adapter identity 错配、自定义 `As` 投影或 joined graph 多 carrier 都固定降级为安全的 `protocol-invalid/do-not-retry`；原始 wrapper/cause 中的自由文本、路径、credential 与控制字符不进入返回错误、事件或 Outcome。崩溃恢复会从持久事件重建同一 normalized failure，并重新校验 safe summary、hints、终态 reason 与 signature，持久字段篡改一律 fail closed 且不重启 Worker。

`failureSignature` 排除 Run/Attempt/时间身份，但绑定 `baseSha`、`specDigest`、`policyDigest`、完整 `capabilityDigest`、Adapter 与 typed failure；因此相同冻结输入可得到稳定审计键，而 executable、协议/transport authority 或其它 CapabilitySnapshot 内容变化会通过完整 capability digest 使旧键失效。崩溃恢复会把该签名重新绑定到 append-only `planning.inputs-frozen` 权威、blocked snapshot、terminal reason 与 quarantine transaction；不匹配时 fail closed，且不会再次启动 Worker。

本切片不建立 Core 全局跨 Run deny-list，也不声称实现跨 Run 自动查重。跨 Run 自动拒绝需要先冻结全局索引、失效、并发、保留期与人工解除的持久化/生命周期权威，属于后续 ADR 范围。当前变更落实 [ADR 0019](adr/0019-deterministic-control-plane-typed-execution-and-goal-admission.md) 的 typed failure 映射，并由 [ADR 0036](adr/0036-adapter-run-boundary-fail-closed.md) 显式冻结 `Adapter.Run` 无类型错误的终态解释、迁移与回滚规则；状态集合、事件/Schema 字段、信任边界和发布权限不变。独立 reviewer 提出的 `ADAPTER-RUN-UNTYPED-LIFECYCLE-ADR-P1` 已通过新增 ADR 0036、删除“旧 Adapter 默认可重试”的陈旧口径并同步 Operator Runbook 关闭；代码仍限于精确 `Adapter.Run` 返回边界，Core 其它 `recordFailure` 来源保持原语义。

## Issue #25 发布合并后 head reconcile 审计增补（2026-08-12）

公开 [Issue #25](https://github.com/chiga0/marshal-harness/issues/25) 与 [PR #24](https://github.com/chiga0/marshal-harness/pull/24) 暴露：当全部 required checks 成功且 PR 已合并进入 main 后，现有 `marshal task accept` 仍要求 PR 处于 OPEN/Draft，会把 Run 永久置为 terminal `BLOCKED`。head branch 删除不是权威 head SHA 丢失——GitHub PR 节点在 merge 后仍保留原 head OID、base OID 与 merge commit。本节记录该问题的审计定位，区分 implementation bug 与 contract gap。

| ID | 级别 | 状态 | 关联 | 问题 | 定位 |
| --- | --- | --- | --- | --- | --- |
| `PUBLICATION-MERGED-HEAD-RECONCILE-P1` | P1 | `CLOSED` | [Issue #25](https://github.com/chiga0/marshal-harness/issues/25) / [PR #24](https://github.com/chiga0/marshal-harness/pull/24) | 已合并 PR 的 head 与 merge commit 缺乏权威 reconcile 路径，`accept` 无法识别 merge 完成态，Run 被永久置为 `BLOCKED` | 分两层：**nonterminal implementation bug**——`accept` 以 PR OPEN/Draft 为前置条件，未区分“merge 前需 OPEN”与“merge 后已合并”两种语义，是可修复的实现缺陷；**terminal contract gap**——当前 Schema/命令缺少不可变 `SCMMergeReceipt` 与 append-only `PublicationReconcileRecord`，无法把已合并 Run 从 `BLOCKED` 安全迁移到 `ACCEPTED`，属契约级缺口，需新 ADR 定义 |

正式处置见 [Operator Runbook §7](operator-runbook.md)。处置进展（2026-08-12）：ADR 0026 已接受并合入 main（PR #49），冻结 `SCMMergeReceipt` 与 `PublicationReconcileRecord` 契约，契约层关闭。处置进展（2026-08-13）：实现层已合入（[PR #106](https://github.com/chiga0/marshal-harness/pull/106)）——`marshal task accept` 活路径内联识别已合并且 required checks 全绿的 PR 并采集不可变 `SCMMergeReceipt`；补偿命令 `marshal task reconcile` 以 ADR 0026 冻结的 `SCMMergeReceipt` + append-only `PublicationReconcileRecord` 与 current-ledger recheck 共同门禁，把发布后的 terminal `BLOCKED` 安全迁移 `ACCEPTED`（`publication.reconciled` 事件，lifecycle 唯一命名终态例外），全程 append-only、幂等、fail closed，不绕过 required checks 与 ReviewDecision。契约层与实现层均已关闭，本 finding 状态更新为 `CLOSED`；不改变 Local MVP `APPROVED_FOR_IMPLEMENTATION` / `USABLE` 结论，也不改变任何已冻结的信任边界、持久化契约或发布权限（merge-never 不变）；`doctor --repair` 依旧只修复 snapshot 损坏，不能改变业务终态。

## 2026-08-20：Qoder transcript attestation 固定 Marshal 执行身份

Qoder v7 operator-local transcript attestation 不再构建或复制随机临时 checker。生产语义校验现由固定 `marshal` 二进制的隐藏 `internal qoder-transcript-check --attestation-ready` 子命令承载；Python validator 只接受用户显式传入的 absolute canonical Marshal 路径，并在发送 evidence 前绑定 held device/inode/raw SHA-256、Mac PID/CDHash 或 Linux `/proc/PID/exe` 身份、含握手参数的内部命令摘要、独立 Marshal build commit/version 与七项输入摘要。进程身份核验完成后才发送单字节 NUL ready token 和 canonical envelope，缺失、错序或非 NUL token fail closed。Attempt `subject.sourceHead` 和 checker build `marshal.sourceHead` 是两个独立字段，不互相替代；实际发送给内部命令的 canonical envelope raw SHA-256 由 Python 与 Go 双端精确比对。子进程采用等价 `env -i` 的封闭环境，有界 stdin/stdout/stderr、deadline 与 owned-process 回收；任何路径、build、stdin 或进程身份漂移继续 fail closed。Receipt shape 新增 Draft 2020-12 Schema/template 并把 framing 升为 `marshal-transcript-attestation-v3`，历史 v2/v5/v6 receipt 保持只读且不可迁移。

本切片不新增 ADR：它只替换既有 `mac-ordinary-user-operator-local` pre-review gate 的 executable delivery，未改变 Core 生命周期、持久化契约、发布权限、authority claim 或 Qoder transcript machine semantics。`internal` 子命令不出现在普通用户 help 中，也不写 `.marshal` 或改变 Run 状态。

## 2026-08-21：Mac-first Adapter 当前证据与未决问题

本次审计以 local main `16c18546dd771cbafc46d10a84bb447b590083e4` 为权威基线，`origin/main` 为 `91186161c734ceff4831d3f03e8734c0a24f36fd`，远端同步尚未完成（`pendingRemoteSync=true`）。本节只记录事实，不把局部 ordinary-user 证据外推为 production authority 或 Milestone 完成。

| 识别项 | 级别 | 状态 | 当前事实与关闭条件 |
| --- | --- | --- | --- |
| `QODER-MAC-1.1.27-LIVE-SMOKE` | P1 | `OPEN` | Qoder `1.1.27` 固定 executable 已通过 doctor registry/compatibility probe，digest 为 `sha256:fd36420ae0e740f7f3fb7f62e9df23aa70df400aad55fc7e7e48e0edc0ce8e2`。仍需用同一绝对路径完成 fresh live Worker、transcript attestation、WorkerResult 与独立 conformance；在此之前不得宣称 production ready。 |
| `QWEN-MAC-ADMISSION` | P1 | `OPEN` | Qwen `0.21.11` 的本地 `--version` 可执行，但当前 `marshal doctor` 为 `unsupported/unprobed`，认证命令已移除且未形成可绑定的认证选择器/凭据证据。需只读 `/doctor`/认证探针形成新鲜 `supported` capability；禁止静默降级启动。 |
| `CODEX-SMOKE-SPEC-COPY` | P2 | `NON_BLOCKING` | R19/R20 已独立复审并 `ACCEPTED`，运行证据无 P0/P1；TaskSpec 仍引用 `r15` 路径，Markdown 产物的 `mediaType` 标注不准确。后续 successor 一次性修正文案，不重做已通过运行。 |

R19/R20 的共同边界：它们证明 Codex `0.145.0` macOS `ordinary-user` Worker 的路径、digest、session、transcript、WorkerResult、verification、artifact、candidate、scope 与 base 绑定；不证明 hardened authority、Linux authority、sandbox 或远端发布。当前 watchdog 暂停新 Worker 调度；容量恢复时也必须先满足 fresh provider signal、scope 互斥、独立 worktree 与 admission receipt。

本次文档增补不改变信任边界、持久化契约、生命周期或发布权限，因此不新增 ADR；也不关闭 `AGENT-AUTHORITY-*`、Qoder live conformance、Qwen admission、Issue #53/#138 等既有开放项。

## Issue #212：Marshal Darwin 自身执行身份阻塞（2026-08-26）

[Issue #212](https://github.com/chiga0/marshal-harness/issues/212) 记录：基线 `5391b466dbb046c78411b1a491adcd81ea6d5900` 构建的固定 Marshal Mach-O 为 ad-hoc signature、`Identifier=a.out`、无 Team ID，`spctl --assess --type execute` 返回 rejected；`version --json` 与 `task scaffold` 均约 10.8 秒后以 exit 137 终止且无 stdout/stderr。故障时宿主 memory pressure 与 CPU 状态正常。exit 137 证明进程收到 `SIGKILL`，但具体发出者仍需部署者用宿主安全日志按时间、PID/CDHash 归因；本报告不把未归因信号直接等同为 Gatekeeper 或某一 EDR 产品结论。

| ID | 级别 | 状态 | 当前事实与关闭条件 |
| --- | --- | --- | --- |
| `MARSHAL-DARWIN-SELF-IDENTITY` | P1 | `CONTRACT-ACCEPTED/EXTERNAL-OPEN` | 固定 pathname 已消除随机 helper，但当前 build/install/release 没有稳定受管签名身份、安装收据/current high-water 或 CLI pre-mutation gate，所有产品 CLI 生命周期仍被阻断。ADR 0047 已在唯一 aggregate rework 后经独立 reviewer `ACCEPT` 且 P0/P1/P2=0，于 2026-08-26 [Accepted](adr/0047-marshal-darwin-self-identity-and-release-signing.md)；接受仅冻结三类 profile、外部 certificate/allowlist/current authority 前置、receipt/trust anchor 与 release/deployment signer 分权合同。实现与外部 provision 仍 OPEN，当前 Keychain 仍无有效 code-signing identity。关闭必须同时满足：部署者 provision certificate/企业 allowlist、外部不可回滚 current/high-water、新鲜 policy observation 与不同 principal/key 的 artifact/release、deployment/install signer；固定安装对象连续执行纯进程内 `version`、bootstrap `doctor --self`、完整 `doctor`、`task scaffold` 无逐次人工批准；binary/receipt/current/path/policy 漂移 fail closed；真实 R3-D scaffold/plan preflight 与独立审查通过。`spctl accepted`、ADR 接受或代码存在任一单项都不足以关闭。 |
| `MARSHAL-BUILD-INPUT-ATTESTATION` | P1 | `CONTRACT+AMENDMENT-ACCEPTED/IMPLEMENTATION-OPEN` | Issue #212 第四个候选的独立审查证明 `HEAD/status/HEAD` 只观察 Git 元数据，不能绑定 Go 实际读取的 ignored `.go`/embed，且没有关闭 provenance 后 mutation 与 linked worktree `.git`/`gitdir`/`commondir` ABA；build 后再次 `git status` 也不充分。ADR 0048 原合同在 sourceHead `5de09997f5260c672f297496290b567815162bb1` 经独立 reviewer 复审 `ACCEPT`（P0/P1=0）并随 PR #215 合入后由维护者接受；后续 amendment 在 sourceHead `b76a53007ba6a07a3bd944fb34d496c47befb289` 经同一独立 reviewer 聚合返工复审 `ACCEPT`（P0/P1/P2/P3=0）后由维护者接受，补齐 compile-root object、authenticated build-record carrier、shared code-sign identity/observation 与跨对象相等关系。接受不关闭实现缺口；关闭仍需要 production producer hostile matrix、schema/validator、protected build→sign→install 最短纵切、外部 provision 与独立 reviewer P0/P1=0。Accepted 文档、纯函数测试或 40-hex `sourceHead` 任一单项均不构成关闭，也不表示 R3-D/E/F 完成。 |
| `MARSHAL-DARWIN-LOCAL-DOGFOOD` | P1 | `CONTRACT-ACCEPTED/IMPLEMENTATION-OPEN` | ADR 0051 已基于提案 sourceHead `e38a94887352cd0ba00f7c7183209d6a6a3ef339` 的独立 reviewer `ACCEPT`（P0/P1=0）于 2026-08-27 接受，并在接受同步的唯一 aggregate rework 中关闭取代范围 P1：新增显式 `darwin-local-dogfood`，只允许 trusted single-user、固定对象、ordinary-user/workspace-write/non-production、`publication:none` 的本地生命周期；profile/identity 必须进入冻结 lineage，Publisher、Forge、credentialed SideEffect、remote Provider、production evidence 与 artifact 晋升全部拒绝。ADR 0051 仅对 local profile 部分取代 ADR 0047 §1/§2/§3.2/§3.3/§3.5/§6 的冲突条款，保留 canonical fixed regular file 原则和 managed/release 全部门禁；local-exec viability 仍是 R3 pre-CLI，ADR 0047/0048 完整 producer/sign/install/current/high-water/notarization 仍是 R6/release gate。实现关闭仍需要 versioned lineage、固定 binary 连续 canary、漂移/跨 profile/credentialed-effect 负测与独立 verdict；Accepted 文档不表示当前 binary 已可执行或 R3 已解除阻塞。 |

该 finding 与 Agent/Sandbox production authority 分离：签名 Marshal 不会把 ADR 0042 ordinary-user Adapter 升级为 hardened authority。Apple notarization 与企业 Endpoint Security/EDR allowlist 也分别判断；禁止通过删除 provenance、关闭安全软件、ad-hoc/随机 executable、`go run` 或伪造生命周期证据绕过。

## Pi 0.84.3 长任务 compaction 协议闭合（2026-08-26）

R3-D 真实 Run `i186-r3-d-shadow-s3-pi-20260826` 在 79,812 个事件、26,336,721 bytes 且未触发输出/时间预算时，以 `agent_end(willRetry=false, stopReason=length) → compaction_start(reason=overflow)` 进入 Pi session-v3 的合法自动压缩链。Pi Adapter `0.3.0` 把低层 `agent_end` 误作会话终态并主动终止进程，Core 随后正确以 `protocol-invalid/do-not-retry` 阻断原 Run；该 Run 不恢复、不接受其部分 diff，只保留 Outcome 与 raw transcript 作为根因证据。

Adapter `0.4.0` 将失败候选延迟到 `agent_settled`/EOF 提交，并显式验证 compaction reason、start/end 配对、success/aborted/failed outcome shape、usage、summarization retry、overflow 单次恢复与 continuation 顺序；未知、重复、乱序或未闭合事件继续 fail closed。该修复仅演进 Pi AgentAdapter 的版本化 decode/completion contract，不新增 Core 状态、Attempt/retry 语义、持久化字段、信任边界或发布权限，因此不新增 ADR；若未来把 compaction 暴露为 Core 生命周期或持久 authority，则必须先有新 ADR。

## 2026-08-26：post-worker abort 缺口与最小合同

真实 CAP-3 dogfood 出现多次相同结构性失败：Worker 与 Verifier 已完成，Run 已进入 `REVIEW_PENDING`，但 TaskSpec、acceptance 或 ReviewPacket 的上游缺陷使 current ReviewDecision 无法安全生成；该失败不属于 Worker 行为缺陷，继续 rework 会错误消费预算，手工修改 `.marshal` 又会绕过 authority journal。

| ID | 级别 | 状态 | 当前事实与关闭条件 |
| --- | --- | --- | --- |
| `POST-WORKER-REVIEW-PENDING-ABORT` | P1 | `CONTRACT-PROPOSED/IMPLEMENTATION-OPEN` | ADR 0050 提议仅开放 human 通过固定 CLI 发起的 `run.aborted REVIEW_PENDING → BLOCKED`，复用现有 Outcome/result/journal/snapshot 恢复，并以 current sequence/Attempt、已完成 verifier lineage、owned child 已退出、无 publication/SideEffect 与 Run Lease 组成 `PostWorkerAbortSafe`。原先新增 `ABORTED`、独立事件家族、carrier/ledger/projection Schema 和 supervisor 写权限的候选方案已因过度设计与 R3 循环依赖被否决。关闭需要 ADR 接受、实现正反/崩溃/并发矩阵、固定 Marshal 真实演练与独立 reviewer P0/P1 清零；Proposed 文档不表示命令可用，也不阻塞 R3-D/E/F。 |
## I186-R6 收口与 Roadmap replan（2026-08-27）

R6 由快速收敛治理交付；范围是 conformance/性能基线/soak/文档 replan，不新增信任边界合同（无新 ADR）。

| R6 Exit Gate 项（#186） | 证据 |
| --- | --- |
| 多拓扑 conformance | M9 双拓扑 suite 维持全绿（Push/Pull/embedded 三组合 outcome/invariant equivalence、failure injection、TLS 基线、lease 账本重开）；推进边界诚实标注：双拓扑未接生产 worker 路径，conformance 为测试套件 |
| SLO/增长基线 | `internal/perfbench`（`1f81286`）：五条热路径 p99 阈值冻结 ≤5000µs；实测基线 bindingcheck-recheck 0.764µs / attemptgate-admit 1.47µs / jitgate-verify 9.35µs / resultingress-admit 20.62µs / effectsink-execute 6.38µs（低阈值 2–3 个数量级；`TestBaselineConformance` N=200 确定性断言） |
| soak | `internal/soak`（`0208964`）：10k 迭代 seeded 原语 soak（决策/渲染幂等、unsafe 必 fence+reconcile、预算豁免只随 authority infra、effect 幂等防重+撤销后拒绝、同种子可重放）；`dc6d7ed` 路径级 accelerated soak 5 轮完整 bridged Run（journal 严格单调、attemptId 唯一、无第二业务事实、replay 等价、allocation record 完备）；**wall-clock 24h soak 未执行**（harness 就绪；归 v1.0 后首个运维窗口，不伪造成完成） |
| 无不可解释 orphan | R6 审计 Top-3 缺口处治：Gap-1 bridged SIGKILL 孤儿已关闭（`97147a1` 执行前 allocation 身份落盘 + SweepOrphans 幂等终结，新增 5 测试）；Gap-2 mid-claim Core 崩溃双拓扑 restart fixture、`I186-P1-5` 远程 fencing 归后续；Gap-3 plan 崩溃 worktree 扫描归 doctor 扩展 |
| 文档/状态同步 + replan | 成熟度矩阵 R6 快照（[i186-r0-maturity-matrix.md](research/i186-r0-maturity-matrix.md)，16 行级别重排、failure inventory 各 ID 状态化）；baseline report 补 R6 行；roadmap-status M10–M13 重排（保持 `PLANNED`，无证据不动状态枚举）与 failure inventory 同步 |
| reviewer APPROVE / replan 维护者接受 | 快速收敛治理下由 Lead 自审真实 diff 替代独立 reviewer；维护者接受以本报告与 roadmap-status 落账为准（不另行走状态机） |

R6 期间的实质修复（不是文档动作）：recovery 决策表两处副作用歧义缺口（partial-artifact/binding-lost 绕过 reconcile 横切、duplicate 幂等 resume 缺副作用歧义例外——soak iteration 69/148 驱动，含回归测试）；sandboxbridge 对真实 LocalRunner 的 allocation record + Sweep 孤儿对账。

按当时口径 I186-R6 于 2026-08-27 曾记为 `DONE`；该结论已由 ADR 0052 生产可达性纠偏撤回，现行状态为 `PLANNED/DESIGN`。当时明确保留的四项 honest gaps 为 wall-clock 24h soak、Push/Pull 生产接线、双 binding ResultIngress 接线与 CLI explain wiring。

## I186-R5 收口：strangler cutover 收敛（2026-08-27）

R5 由快速收敛治理交付，[ADR 0054](adr/0054-cutover-equivalence-and-effect-sink-fencing.md) 冻结等价性判据与 effect sink fencing：

| 交付 | 证据 |
| --- | --- |
| `internal/cutovereq` + `internal/cutovercheck`（`b3e193d`/`6285a15`） | 三分判据实现；R0 golden trace old/new 等价、business-mismatch/unexplained-drift/misaligned 阻断、资源权威计数回归 |
| `internal/effectsink`（`b3e193d`） | pre-mutation 独立 recheck 固定判序、revoke→effect 竞态专测、幂等防重复合门禁 |
| `internal/execution` `Input.WorkerRunner` seam + `internal/sandboxbridge`（`ab93174`） | Provision→Stage→Adapter.Run→Inspect→Terminate 执行链身份绑定；端到端等价：legacy 与桥路径 journal 事件序列逐条相同、WorkerResult 业务内容逐字节相同（fixture 确定性）；失败链 typed 归一化一致 |
| 默认翻向 + rollback（`20c5609`） | `MARSHAL_WORKER_EXECUTOR` 默认 sandbox、`legacy` 回退；rollback 演练：runstore setup 原样可读、无第二业务事实、零状态迁移；fencing 兼容修复（attempt 级单一 token，LocalRunner sealed-lease 精确匹配）+ 真实 LocalRunner 常驻回归 |

Exit Gate 对照：**新路径默认启用**（20c5609）；**回滚演练通过**（TestRunWorkerRunnerRollbackDrill）；**Local MVP 零回退**（全仓 `go test ./...` 唯一已知失败为 opencode live probe 宿主版本漂移，dogfood 沉淀项）；**cutover 判定**（golden 等价 + execution 端到端等价 + 白名单 diff 全入账）。范围标注：**旧路径不再服务 production** 的语义边界——本机 Local MVP 不存在 production assurance 运行（ADR 0042 ordinary-user），production profile 由 `agentruntime.ErrHostBypassDenied` fail closed 兜底，legacy 路径降级为 explicit local-nonproduction compatibility profile；real-real Agent canary 多轮对比、Cloudflare/远程 trace、host bypass 代码删除归 R6/后续治理。诚实缺口（继承自接缝测绘并经治理确认）：spine `FakeAgent` 桩、workerRuntimeProfile 的双 binding 接线、ResultIngress→runstore 证据桥三项在 R5 均未宣称完成，归 R6 或后续 milestone。

按当时 component checkpoint 口径 I186-R5 于 2026-08-27 曾记为 `DONE`；该结论已撤回，现行状态为 `IN_PROGRESS/COMPONENT`，以本报告顶部 checkpoint 为准。

**R5 真实 Agent canary 补证（2026-08-27，`3e6ed10`）**：`TestRealPiExecChainCanary`（`MARSHAL_RUN_PI_CANARY=1`/`MARSHAL_PI_PATH` 双门控、默认跳过）以标准 `scaffold→plan→approve→task run` CLI 纵切驱动真实 pi CLI 经 worker executor 默认 exec-chain 执行，断言 `worker.completed` 恰好入账一次、allocation record 锚点与尝试目录一致、`sandbox-binding-admission.json`（ADR 0052 §1.4 双 binding 接纳锚点）持久化存在、标记文件内容由 acceptance 权威校验通过；随测修复三处测试侧 policy 文档合规缺口（`generatedAt` 必填、control 块五子字段齐全、非 dogfood supervised 双批准门），生产代码零改动。范围标注不变：real-real Agent canary **多轮**对比与 Cloudflare/远程 trace 仍归 R6/后续治理，单轮 canary 不宣称生产 cutover 完成。

## I186-R4 收口：Pre-R4 四项合同 + 单一恢复模型（2026-08-27）

Pre-R4 contract gate 四项与 R4 单一恢复模型均由快速收敛治理交付，[ADR 0053](adr/0053-pre-r4-contract-gates-and-single-recovery-model.md) 冻结全部合同（并就地修订 ADR 0044 冷热路径条款）：

| 交付 | 证据 | 关闭的 finding / Exit Gate |
| --- | --- | --- |
| `internal/hotpath`（`f65cfaf`） | 入账分型、业务 kind 仅 cold、effect 门禁、Restore 门禁、洗路径冲突负测 | `I186-ARCH-HOT-PATH-AUTHORITY` |
| `internal/jitgate`（`c4c8b69`） | AdmissionToken 五要素 + provision 前五项重验、半开区间、硬错误/业务拒绝分流 | `I186-ARCH-JIT-ADMISSION-RECHECK` |
| `internal/protocolrev`（`ab7b263`） | Revision 解析、pinned 精确匹配、迁移合法性、HistoryGuard 防重写 | `I186-ARCH-PROTOCOL-REVISION-MIGRATION` |
| `internal/candidateid`（`c4c8b69`） | identity 派生与跨 Attempt 收敛、证据绑身份/换绑拒绝、legacy 迁移幂等 | `I186-ARCH-CANDIDATE-IDENTITY` |
| `internal/recovery`（`34f70d3`） | 故障矩阵八类唯一幂等结论、ambiguous side effect 强制幂等键对账、stale 仅入冲突、幂等性与 Render 复盘要素 | R4 Exit Gate（每类故障唯一结论；不能证明安全时 fence+new Attempt）；`I186-ARCH-RESOURCE-CLASSIFICATION-AUTHORITY` 消费边界 |

验收命令：`go build ./...` 干净；`go test` 12 个收敛域包（recovery/hotpath/jitgate/protocolrev/candidateid/revokedrain/attemptgate/locationattest/failureclass/agentregistry/runtimeprofile/bindingcheck）全绿。Local MVP 零回退：全部纯新增包，零既有包修改。

范围标注：ADR 0045 R4 交付清单中「Inspect/Reconcile/Cancel/Terminate 与不可绕过的 current lease resolver」的 Provider 侧接线、`marshal explain run` CLI wiring 与真实 ledger 装配归 I186-R5/R6（本收口以 ADR 0053 决策 5 「等价 API」口径冻结恢复语义与渲染模型）；`I186-ARCH-EFFECT-SINK-FENCING` 与 `I186-ARCH-CUTOVER-EQUIVALENCE` 归 R5，不随 R4 关闭。

按当时收敛域合同与决策语义口径 I186-R4 于 2026-08-27 曾记为 `DONE`；该结论已撤回，现行状态为 `IN_PROGRESS/COMPONENT`，以本报告顶部 checkpoint 为准。

**R4 真实装配与恢复路径消费补证（2026-08-27）**：范围标注中的「`marshal explain run` CLI wiring 与真实 ledger 装配」已由 `6a26012` 交付（`internal/explain` 从权威 journal/snapshot/attempt anchor 装配 `recovery.RecoveryInput`，`marshal explain run RUN_ID [--json]` 渲染恢复时间线/decision/next action，只读不改写状态）；「恢复路径消费单一恢复模型」已由 `2bf4f3e` 交付：`explain.AssembleWithStaleness` 开放 staleness 注入，supervisor 死 driver 分派以自身 driver 死亡窗口装配 `recovery.Decide`，仅 new-attempt 且免幂等键对账才派生 driver；`task run --recover-dead-driver` 逃生舱在 owner 死亡耐用记录证明后走同一 `recoverTakeoverAdmission`（staleness≈0），需对账的 ambiguous side effect 一律 fail closed 并指向 `marshal explain run`。新增 supervisor 分派矩阵与 cli admission 三分矩阵测试；既有 `TestSupervise*` 全量保持绿色。剩余 Provider 侧 Inspect/Reconcile/Cancel/Terminate 接线与 `I186-ARCH-EFFECT-SINK-FENCING`/`I186-ARCH-CUTOVER-EQUIVALENCE` 仍归 R5/R6，不随本补证关闭。

## I186-R3 收口：快速收敛治理下的 Exit Gate 证据（2026-08-27）

2026-08-27 起维护者授权 I186 快速收敛治理：单 Lead + 多 Sub-Agent 高并发，停用 Marshal skill/admission/ReviewDecision/独立 reviewer/rework 轮转，Lead 直接实现、自审真实 diff 后直接合并；防错误发布、数据破坏与 trust-boundary ADR 三项硬约束保留，dogfood 问题（含 Issue #212 签名身份）另行沉淀不阻塞主线。Issue #191 原 Exit Gate 中「独立 reviewer APPROVE / PR #192 登记前置」两条流程性条件按该授权不再适用；finding 稳定登记继续以本报告为准（不依赖未合入 PR）。

R3 Exit Gate 技术条件与证据对照：

| Exit Gate 条件 | 证据 |
| --- | --- |
| Agent/Sandbox 可独立替换与撤销；profile 不隐藏底层身份 | `internal/runtimeprofile`（AgentBinding/SandboxBinding 独立 digest、Replace* 互不影响）；R3-C `internal/bindingcheck` 双侧独立 recheck |
| 任一 binding revoke/expire/replace 后旧组合结果不可接纳 | `internal/attemptgate` 负测：仅 Agent 侧 revoke/replace/snapshot-supersede 与仅 Sandbox 侧 revoke/expire/replace 双向单侧失效均 fail closed 且另一侧不受影响 |
| 跨 Port credential/token/evidence 复用失败；Agent evidence ≠ Sandbox evidence | `internal/attemptgate/boundary_test.go`：Sandbox 签发 evidence 冒充 Agent 侧、跨 registration 借用、伪造 digest、跨 Port binding 混淆均 fail closed；`EvidenceRecord.ProviderType=sandbox` 类型级拒绝 |
| ResultIngress 从 Attempt 解析 immutable profile 并分别 current-ledger recheck | `internal/attemptgate`：AttemptProfileStore immutable put-if-absent（同 digest 幂等、冲突 fail closed）+ Gate 双侧 recheck；生产 ResultIngress 接线归 R5 |
| security-critical revoke 立即生效 / ordinary upgrade 有界排水 | `internal/revokedrain`：零 drain（cancel+bump+kill）/ stop-new + bounded drain + fence；revoke 抢占 drain、fence 后升级 fail closed、double revoke 幂等拒绝 |
| Provider 自报执行位置只能是 observation；需 authority-verified fact + 来源标注 + 伪造位置负测 | [ADR 0049](adr/0049-location-attestation-and-failure-classification-authority.md) 决策 1 + `internal/locationattest` 负测矩阵 |
| `infra-failure` 分类来自故障域外 observation；Provider 声明只能诊断/收紧 | [ADR 0049](adr/0049-location-attestation-and-failure-classification-authority.md) 决策 2 + `internal/failureclass` 决策表与伪造负测 |

落地提交：`ec13ee7`（revokedrain）、`0a9b3b6`（attemptgate）、`c47b4c2`（locationattest）、`d89c65e`（failureclass）、`8590c4e`（ADR 0049 + 0043 §5 修订标注）。验收命令：`go test ./internal/revokedrain/... ./internal/attemptgate/... ./internal/locationattest/... ./internal/failureclass/... ./internal/agentregistry/... ./internal/runtimeprofile/... ./internal/bindingcheck/... -count=1` 全绿；`go build ./...` 干净。全仓 `go test ./...` 唯一失败为 `internal/adapter/opencode` 的 live probe（宿主 opencode 1.18.20 超出 adapter 版本表 [1.18.13/1.18.16/1.18.18]），属 dogfood 环境漂移，另行沉淀不阻塞主线。Local MVP 零回退：纯新增包，未修改任何既有包或信任边界目录行为。

按当时收敛域合同与负测口径 I186-R3 于 2026-08-27 曾记为 `DONE`；该结论已撤回，现行状态为 `IN_PROGRESS/COMPONENT`，生产接线不再转嫁给已撤回的 R5 结论。Pre-R4 contract gate 四项（hot-path authority、JIT admission、protocol migration、Candidate identity）的历史实现证据继续保留。

## Issue #186：架构复审 Finding 稳定登记（2026-08-25）

[Issue #186](https://github.com/chiga0/marshal-harness/issues/186) 的多轮复审接受了 WorkerExecutor、Agent/Sandbox 双 binding、ResultIngress 与 strangler 收敛方向，同时发现若干不能只留在 Issue 评论中的合同缺口。本文只建立稳定 ID、当前证据、关闭条件和 milestone 落点；**登记不等于修复，Issue disposition 不等于 ADR 接受，代码或测试存在也不等于 finding 已关闭**。关闭任一 P0/P1 仍需相应合同/实现、正反证据和独立 reviewer verdict。

2026-08-27 direct checkpoint：为缩短 R3→R6 主线，维护者停止使用 Marshal skill 的 admission/rework 轮转，改为单 Lead + 多个互斥 Sub-Agent 并行审计、Lead 直接实现与自审。`feat/i186-r3-direct` 已重新实现 R3-D evidence boundary 与 bounded-drain 纯核心：外部只呈现 opaque material ref，权威链由新鲜 material/registration/snapshot 查询闭合，Agent/Sandbox 的 evidence、credential、token 六种跨 Port 复用方向均 fail closed；security revoke 无 drain 窗口，planned upgrade 只允许全新且无别名的 registration/snapshot 并在冻结 deadline 后 fence。定向测试 `go test ./internal/revokedrain` 通过。该 checkpoint 未接线 production authority ledger/ResultIngress，不能关闭 `I186-ARCH-DUAL-BINDING-RECHECK`；R3-E/F 的故障域外位置/资源 observation authority 会改变信任与持久化合同，仍须先新增最小 ADR。详细状态见 [i186-r3-progress.md](research/i186-r3-progress.md)。

### 主执行链 hardening

| ID | 级别 | 状态 | 当前证据与关闭条件 | 建议落点 |
| --- | --- | --- | --- | --- |
| `I186-ARCH-LOCATION-ATTESTATION` | P0 | `CLOSED-CONTRACT+CONVERGENCE`（2026-08-27） | ADR 0043 把执行位置 evidence 的产出职责写给 SandboxProvider，仍可能由被证明方自证。必须区分 `provider-asserted location claim` 与故障域外产生的 `authority-verified location fact`；只有后者可支撑 production assurance/publication。关闭证据：[ADR 0049](adr/0049-location-attestation-and-failure-classification-authority.md) 决策 1（claim/fact 分型、自证排除、FactLedger 身份元组 put-if-absent、修订 ADR 0043 决策 5）+ `internal/locationattest` 收敛域实现与负测（digest 篡改、observer 自证排除、跨 allocation/generation 挪用、伪造 claim、身份元组冲突不覆盖原 fact）。Local kernel held handle 采集与 ResultIngress/发布门禁接线归 I186-R5/R6，不从本关闭推断。 | `I186-R3` Exit Gate |
| `I186-ARCH-EFFECT-SINK-FENCING` | P1 | `CLOSED-CONTRACT+CONVERGENCE`（2026-08-27） | ResultIngress recheck 只能保护 ledger，不能撤销已经发生的外部效果。SCM、Artifact、Secret 与其它 effect sink 必须在 mutation/secret use 前独立执行 current generation、fencing、authorization 与 target recheck，并覆盖 revoke→effect 竞态。关闭证据：[ADR 0054](adr/0054-cutover-equivalence-and-effect-sink-fencing.md) 决策 2 + `internal/effectsink`：pre-mutation 固定判序独立 recheck（authorization-revoked 优先的五种生命周期拒绝逐一单变量负测、revoke→effect 竞态专测）、EffectLedger 幂等防重、ExecuteIfAdmitted 复合门禁。SCM/Publisher 生产 sink 接线归 R5/R6 持续执行，不从本关闭推断。 | R3 后、R5 cutover 前 |
| `I186-ARCH-HOT-PATH-AUTHORITY` | P1 | `CLOSED-CONTRACT+CONVERGENCE`（2026-08-27） | 当前 `internal/resultingress` 把 checkpoint/heartbeat/log 归为 hot path 并跳过 registration/snapshot/evidence eligibility recheck；checkpoint 可能在未完成冷路径校验时被 Restore 消费。关闭证据：[ADR 0053](adr/0053-pre-r4-contract-gates-and-single-recovery-model.md) 决策 1（修订 ADR 0044 冷热路径条款）+ `internal/hotpath` 收敛域实现与负测：业务 kind 只允许 cold 入账（入账即禁止而非事后解释）、authority effect（extend-lease/bump-generation/decide-fencing）仅作用 cold 接纳、Restore 门禁只接受 cold 接纳的 checkpoint、同 digest 洗路径以入账冲突 fail closed。resultingress/sandbox.Restore 生产接线归 R5，不从本关闭推断。 | #186 Pre-R4 contract gate |
| `I186-ARCH-DUAL-BINDING-RECHECK` | P1 | `CLOSED-CONVERGENCE`（2026-08-27） | R2 ResultIngress 当前只有单组 registration/snapshot/evidence binding；R3-B 已冻结 `WorkerRuntimeProfile`，但 per-Attempt profile 的 AgentBinding 与 SandboxBinding 分别 current-ledger recheck 尚未完成。关闭需要单侧 revoke/replace 的双向负向 fixture。关闭证据：R3-C `internal/bindingcheck`（双侧独立 recheck、七封闭原因）+ R3-D `internal/attemptgate`（AttemptProfileStore immutable put-if-absent 绑定、Gate 从 Attempt 解析 immutable profile 并分别 recheck AgentBinding/SandboxBinding；仅 Agent 侧 revoke/replace/supersede 与仅 Sandbox 侧 revoke/expire/replace 的双向单侧失效互不牵连负测全绿）。生产 ResultIngress 接线归 I186-R5，不从本关闭推断。 | `I186-R3-C/D` |
| `I186-ARCH-CUTOVER-EQUIVALENCE` | P1 | `CLOSED-CONTRACT+CONVERGENCE`（2026-08-27） | ADR 0045 的 old/new 全 digest 相等对真实非确定 Agent 不可满足。R5 前必须拆成真实 Agent 必须相等的 authority-trace invariants，以及只适用于 deterministic Fake 的 content digest equality；真实 Agent 使用资源归一化后的不劣化统计，不能人工解释掉 authority diff。关闭证据：[ADR 0054](adr/0054-cutover-equivalence-and-effect-sink-fencing.md) 决策 1 + `internal/cutovereq`（三分判据、白名单 upgrade 形态校验、不可人工覆盖）+ `internal/cutovercheck`（R0 golden trace old/new 等价、business-mismatch/unexplained-drift/misaligned 阻断、资源权威计数回归）+ execution 端到端等价（legacy vs 桥路径 journal 事件序列逐条相同、WorkerResult 业务内容逐字节相同，6285a15/ab93174）。真实 Agent canary 与多轮对比归 R6 conformance，不从本关闭推断。 | `I186-R5` |
| `I186-ARCH-RESOURCE-CLASSIFICATION-AUTHORITY` | P1 | `CLOSED`（2026-08-27） | `ResourceEnvelope.observedPeak`、termination reason 与 `infra-failure` 分类权未冻结。合同关闭证据：[ADR 0049](adr/0049-location-attestation-and-failure-classification-authority.md) 决策 2 + `internal/failureclass`（决策表 8×2 全组合、伪造 infra-failure 放宽恒 false、semantic 抗拒洗白、digest echo）。消费关闭证据：`internal/recovery`（34f70d3）决策表消费该分类——terminal-failure 且 authority-observed infra 分类时产生预算豁免的新 Attempt（MayRelaxBudget/MayExemptSemanticRework 输入），provider-claimed/semantic 分类恒 resume 消费失败 Outcome；R4 恢复模型已落地，消费边界关闭。 | `I186-R3` 合同，R4 恢复消费 |
| `I186-ARCH-JIT-ADMISSION-RECHECK` | P1 | `CLOSED-CONTRACT+CONVERGENCE`（2026-08-27） | JIT provision 扩大 admission→provision 时间窗。Provision 前必须重验 AdmissionDecision `validUntil`、registration/snapshot generation 与 current Policy；不得顺延到 R6。关闭证据：[ADR 0053](adr/0053-pre-r4-contract-gates-and-single-recovery-model.md) 决策 2 + `internal/jitgate`：`AdmissionToken`（五要素 + canonical digest 防篡改）与 `VerifyBeforeProvision` 五项强制重验（registration active、active snapshot digest 对齐、policy active、policy digest 对齐、半开区间 `[issue,validUntil)`）；结构性硬错误与业务拒绝（六封闭原因码）严格分流。dispatch/provision 生产强制点接线归 R5，不从本关闭推断。 | #186 Pre-R4 contract gate |
| `I186-ARCH-PROTOCOL-REVISION-MIGRATION` | P1 | `CLOSED-CONTRACT+CONVERGENCE`（2026-08-27） | `acp → acp/v1` 等协议枚举升级不得重写或重新解释历史 snapshot/digest；只能 Supersede 为新 snapshot，unversioned 历史值不能满足 pinned revision admission。关闭证据：[ADR 0053](adr/0053-pre-r4-contract-gates-and-single-recovery-model.md) 决策 3 + `internal/protocolrev`：Revision 解析冻结、AdmitPinned 精确匹配（unversioned 出示永不满足 pinned）、SupersedeMigration 合法性（digest 必新/同族/To versioned/provenance 必备）、HistoryGuard 防重写（From 须先冻结、To 须真新、判定不改写）。capability supersede 生产接线归 R5，不从本关闭推断。 | #186 Pre-R4 contract gate |
| `I186-ARCH-CANDIDATE-IDENTITY` | P1 | `CLOSED-CONTRACT+CONVERGENCE`（2026-08-27） | 当前已有独立 `candidateDigest`，但 identity slot 和链仍强约束于 Attempt，尚未以合同证明不会把 Attempt→Candidate 1:1 固化为未来破坏性约束。关闭证据：[ADR 0053](adr/0053-pre-r4-contract-gates-and-single-recovery-model.md) 决策 4 + `internal/candidateid`：CandidateID 由 (ContentDigest, RecordDigest) 派生、OriginAttemptID 仅 provenance（不同 Attempt 同内容收敛同一 ID 的构造性证明）；证据绑身份（未冻结身份不得绑定、换绑 ErrEvidenceRebound）；MigrateLegacyReference 单向幂等迁移。不启用多 Candidate fan-out；生产引用换指归 R5，不从本关闭推断。 | #186 Pre-R4 contract gate |

R2（#189）已关闭，不得把上表中原先口头指派给 R2 的 finding 视为随之关闭。为避免增加新的 milestone 和状态面，四项漏接合同统一列入 #186 的 Pre-R4 contract gate：可以与 R3 并行补齐，但 R4 启动前必须有合同、正反证据和独立 verdict。`I186-ARCH-LOCATION-ATTESTATION` 必须进入 #191 的显式 Exit Gate，避免 R3 在位置仍由 Provider 自证时被错误关闭。

### 前期研讨、复盘与 Worker 协作

三类能力的共同风险是：Agent 生成的语义内容会影响另一个阶段、另一个 Worker 或未来 Goal。复审采用统一判据：跨越该边界的内容必须先成为 immutable、content-addressed、digest-bound、带 producer provenance 的对象，明确 purpose/audience，并由下游显式选择为 **untrusted input**；禁止自动注入 transcript、自由文本消息或 live knowledge query。该判据需要 Proposed ADR 和独立审计，本文不把它提前标为已接受合同。

| ID | 级别 | 状态 | 问题与处置 / 关闭条件 |
| --- | --- | --- | --- |
| `I186-ARCH-DECISION-INPUT-BOUNDARY` | P1 | `OPEN-PROPOSED-ADR` | 需要冻结统一的跨阶段语义输入门禁：canonical digest、provenance、purpose/audience、大小/类型边界、显式 admission、supersession、冻结下游引用，以及“作为数据而非指令”呈现。现有 Artifact/Goal proposal 可复用，但不存在可绕过 admission 的通用上下文流。 |
| `I186-PRE-EXEC-DELIBERATION` | P1 | `OPERATIONAL-PILOT` | Stage 0 立即使用 `publication:none` 调研 Run、互斥报告路径、人工综合与显式 proposal，不新增 Core 状态。产品化 Discovery 推迟到 R6 后；关闭前还需 typed finding/option、只读 workload profile、网络/来源治理、dissent carrier、Goal controller 与负测。 |
| `I186-ARCH-DISSENT-CARRIER` | P1 | `OPEN-DESIGN` | dissent 与 open assumption 若只写进汇总散文，会在计划接纳时丢失。需要 versioned、content-addressed、digest-bound 的 durable handoff carrier，并在后续 ReviewPacket 中显式引用；Issue 中 `ACCEPT_P1` 只代表方向处置，不等于已有 ADR/Schema/实现。 |
| `I186-RETROSPECTIVE-RECORD` | P1 | `OPERATIONAL-PILOT` | 现在可生成 ledger/Outcome/Evidence 的事实投影与轻量 closeout；因果解释、失败归因和改进建议必须作为带 provenance 的 assessment/proposal，与机械事实分开，不能伪装成“纯投影”。 |
| `I186-ARCH-DISCOVERY-RETRO-ROLE` | P1 | `DEFERRED-R6` | `sandbox.WorkloadRole` 当前封闭为 `worker|verifier`。产品化 Discovery/Retrospective workload 不得冒充 verifier；新增 role/profile 将改变持久化合同，必须先有独立 ADR、principal、最小权限与负测。Stage 0 人工流程不声称该能力已实现。 |
| `I186-ARCH-RETRO-EVIDENCE-PROJECTION` | P1 | `DEFERRED-R6` | 未来 retrospective evidence packet 必须是 allowlisted + redacted 的冻结投影，并使用独立 principal；禁止把 raw ledger、credential、宿主路径或未筛选 transcript 原样交给复盘 Agent。 |
| `I186-ARCH-KNOWLEDGE-SNAPSHOT-REPLAY` | P1 | `DEFERRED-DEPENDENCY` | 跨 Goal 学习在 `ResourceEnvelope`、Provider-independent failure attribution 与重复任务 ROI 证据出现前不实施。未来知识只能作为 immutable versioned snapshot 被引用，其 digest 进入 Attempt 冻结输入集；planning/execution 决策路径禁止 live 查询。 |
| `I186-WORKER-COORDINATION` | P1 | `REJECT-IMPLEMENTATION-FOR-NOW` | 当前没有可测量的 Lead 转发瓶颈，不建设 mailbox/A2A 群聊。现阶段只允许 Artifact-mediated 单向协作：发布不可变 ref，下游按已接纳计划显式消费 digest，Core 在 fan-in 复核。若未来重提 mailbox，必须先提交非规范 RFC 和瓶颈数据，再审计配额、deadline、crash/replay、撤销、循环与同 Goal prompt injection。 |
| `I186-ARCH-DEPENDENCY-HINT-AUTHORITY` | P1 | `DEFERRED-WITH-MAILBOX` | 未来若存在 `DependencyReady`，它最多是可丢失的唤醒提示；Core 在完全没有 Worker 消息时也必须能从 ledger/Artifact refs 独立判断依赖满足，消息不得成为 correctness 或 liveness 的唯一条件。 |
| `I186-DOC-HUMAN-MODEL` | P1 | `OPEN-DOCS` | 需要一份人类友好的分层导读，解释“当前能力 / Accepted 目标合同 / Proposed 演进”三种状态，并说明前期研讨、复盘记录和不实施 mailbox 的原因。文档合入且链接/构建检查通过后可关闭为 `CLOSED-DOCS`，但不升级任何产品能力状态。 |

优先级冻结为：`前期研讨 Stage 0 >> 复盘记录 > 复盘学习 >> Worker mailbox`。其中只有 Stage 0 与轻量 closeout 可在 R3–R6 期间作为操作约定试行；其余不得抢占主执行链 P0/P1，不新增 required production path。所有 pilot 都应记录成本、等待时间、finding 质量、返工变化和人工分钟数，R6 后再基于证据决定保留、修改或删除。

## 2026-08-29：Supervisor mechanics receipt binding checkpoint

`I186-ARCH-SUPERVISOR-RECEIPT-BINDING` 当前状态为 `CONTRACT-ACCEPTED / INTEGRATION-OPEN`。ResultIngress 的单一 RB1 ledger/projection 已增加 `process-supervisor-bootstrap-prepared` 恢复锚点，以及不推进 Attempt head 的逐 command intent/outcome recovery 子链；每个 outcome 绑定完整 mechanics/journal pre/post anchor，business fact 只引用 exact successful outcome fact，intent-only 可耐久进入 intervention。旧 ledger 省略新字段时仍按原 digest 和状态序列回放。候选 `12996f87beb3b45b9267d4356875d9ebe257fcd2` 经独立终审确认 P0/P1/P2 均为 0 后，[ADR 0060](adr/0060-supervisor-mechanics-authority-binding-and-recovery.md) 已接受。该 checkpoint **没有**接入 production composition；`processsupervisor.Client` 的 deterministic prepared-command API、descriptor-relative nonce/journal object recovery、lost `Close` receipt 后的 offline absence recovery仍开放。在真实 spawn/collect/terminal/close 调用链与重启 reconcile 通过前，不得把它标记为 `INTEGRATED` 或关闭 R2/R3 production reachability finding。

## 2026-08-29：无结果 Close 与生产 server binary 拓扑

`I186-ARCH-CLOSE-TRANSCRIPT-DISPOSITION` 当前状态为 `CONTRACT-ACCEPTED / IMPLEMENTATION-OPEN`。真实 producer chain证明两个缺口：terminalization barrier先赢后，ResultIngress必须拒绝后续 `collect`；successful collect outcome也可能已耐久、但 admission仍输给随后 barrier。[ADR 0061](adr/0061-supervisor-close-transcript-disposition.md) 已接受 `collected-admitted|collected-not-admitted|not-required` 封闭 union；两个 non-admission分支只能引用 current RB1 authority在 empty-result barrier上签发的 exact resolution fact，不能放宽正常成功路径。wire/persisted projection、hostile/crash/replay矩阵与真实 producer接线仍未实现，完成前 cancel/timeout与 collect/admission crash window均不可宣称可恢复。

`I186-ARCH-FIXED-SERVER-COMPOSITION` 当前状态为 `CONTRACT-ACCEPTED / INTEGRATION-OPEN`。独立 `marshal-server` 进程不是 ADR 0059 要求的 fixed Marshal identity，继续 child-exec又违反 ADR 0057 的唯一 in-process Port。[ADR 0062](adr/0062-fixed-marshal-production-server-mode.md) 已接受并部分取代 ADR 0052/0057 的 executable拓扑：生产 loopback server收敛为 fixed `marshal control-plane serve`，独立 `marshal-server` 降为无生产 mutation权限的开发/兼容入口；AF_UNIX delivery projection只能引用 current owner/RB1 exact receipt，不能成为第二 authority。

2026-09-01 的第一段 cutover 已删除独立 server 的 `--marshal-executable`、child `task run`、Provider registration mutation 与 Worker selector 初始化，并通过不可配置的 `DisableMutations` 在 body parsing/idempotency 写入前拒绝 Task create/cancel 和 Run approval/start；查询、事件和跨进程只读恢复继续保留。该变更关闭“独立 executable 可被 flag/环境重新提升为 mutation root”的实现缺口，但 fixed `marshal control-plane serve`、in-process `PublicApplicationPort`、authenticated owner/session 和 server restart/response-loss recovery 尚未接线。因此本 finding 仍为 `INTEGRATION-OPEN`；只有 fixed mode 完成真实 canary，且 exact managed-development signed/allowlisted 或 notarized candidate满足 stable 门禁后，才可升级。ADR 0068 仅对 `v1.0.0-rc1` 部分取代 server/managed 前置，不能据此宣称 server/stable 已完成。

同日第二段 cutover 关闭了 Run start 的 `RunExecutor func` direct execution seam：HTTP adapter 只消费 `PublicApplicationPort`，pending intent 绑定 current-ledger sequence/authority head 与 exact prepared Attempt/`preparationDigest`，执行顺序固定为 `InspectRun → PrepareRunStart → StartPreparedRun`，receipt 从完整 legacy `RunState` 收敛为 path-free `RunProjection`。测试覆盖成功 start、幂等 replay、持久 pending 记录的 response-loss recovery及其它 Attempt 冒名恢复拒绝，并机械证明 replay 不重复 Prepare/Start。该结果只关闭 `I186-ARCH-FIXED-SERVER-COMPOSITION` 的一个子 finding；server 仍直接拥有其它 legacy lifecycle/store 分支，`ProductionRuntime` 仍按单 Run/Run Lease 组合，不能直接常驻服务多个 Run。下一实施边界是 owner-scoped runtime session/factory 与 fixed CLI 注入；若在此之前直接添加 `control-plane serve` 命令，会重新制造 owner/lease 或第二 composition 问题。

第三段 cutover 已关闭上述 owner/Run 生命周期耦合：新增的 `RepositorySession` 独占一次 repository owner acquisition、sealed ResultIngress 与 runtime claim，Run-scoped composition 只能借用且不能关闭 owner；关闭屏障保证 Session 不会与仍在执行的 Run runtime 竞态释放 held descriptors。实现复用原有 owner fact、current-ledger replay、Run lease 与 close order，没有增加新事实或绕过验证，因此不触发新 ADR。机器化架构门禁曾发现 Session 直接调用 `claimRuntime` 的越界，已通过把唯一 claim 入口保留在 `runtime.go` 修正，而非放宽检查器；自审还发现 typed nil 存入 `io.Closer` 会让旧 standalone composition 误入 borrowed-owner 分支，已改用具体指针并用旧/新组合回归覆盖。该 finding 仍为 `INTEGRATION-OPEN`：当前只有资源生命周期闭合，尚缺 Session 上的多 Run application assembler、fixed CLI server mode、authenticated transport 与真实 restart/response-loss canary。
## 2026-08-31：Result observation release-gap 修复

RC1 completion 复审发现：`result-admitted` 已提交后，terminalization 会先释放 path-B worktree，再追加 runstore `worker.completed`；若在两者之间崩溃，恢复时重新观察已归还用户的 live worktree 会因合法修改永久阻塞仍为 `RUNNING` 的 Run。该问题登记为 release-critical P1，并由 [ADR 0072](adr/0072-result-observation-binding-before-worktree-release.md) 关闭合同缺口：首次 admission 同一 authority fact 绑定规范 snapshot bytes、`snapshotDigest` 与 `diffDigest`，release 后恢复只校验 descriptor-held snapshot 对该 binding，不再读取 live worktree。实现与定向冷重放测试已落地；RC1 仍须真实 Pi 纵切与 same-bytes release canary，不因本项关闭而升级。

## 2026-08-30：RunStore 描述符补强与合入审计

`main@46e0054` 是当前本地权威基线（父提交 `054789c`，`origin/main` 尚未同步）。本次以维护者指示直接合入 `ac5fd20`，新增 `NewFromStateRootDescriptor`/`NewAt`，让 existing-only acquisition 沿 held StateRoot descriptor 打开 `runs/<runID>`，并将描述符保留到 Lease 生命周期结束。该切片通过 `go test -race ./internal/runstore`、`go vet ./internal/runstore`、`git diff --check` 与 architecture check。

独立 reviewer 随后发现 descriptor Store 的 pathname API 空根路径风险与 Close/acquisition 竞态；`main@109f35d` 已增加哨兵根路径、descriptor-only `Acquire` 拒绝和互斥保护，并通过 runstore race/vet/diff 定向门禁。本次仍未等待独立 reviewer，故记录为审计风险而非“已独立验收”；Store.root 等兼容字段仍需在 production composition 接线前完成全调用链审计，不得把该 component 合入解释为 S2′ 完成。ResultIngress/Execution/App 现有 sealed Run-start fixture 仍失败，CI 质量门禁不绿；Qoder/Codex 生产配置、真实 Pi→独立 Decision→`ACCEPTED`、RC1 同字节 canary、签名/公证和远端发布均未完成。
