# ADR 0066：生产组合的两阶段 owner acquisition 与 S2 边界纠正

- 状态：提议（Proposed）
- 日期：2026-08-29
- 提议基线：`main@7de2a70cec112df5fbf2b36f85ce5878f227c40c`
- 关联：[ADR 0052](0052-v1-release-scope-and-production-reachability.md)（v1.0 生产可达性）、[ADR 0057](0057-durable-local-allocation-recovery-and-production-composition.md)（唯一 `ProductionRuntime`）、[ADR 0062](0062-fixed-marshal-production-server-mode.md)（fixed Marshal server mode）、[ADR 0063](0063-prepared-execution-authority-and-production-chain.md)（PreparedExecution producer chain）、[ADR 0065](0065-sealed-run-start-proof-and-one-way-composition.md)（sealed proof 与 S1/S2）、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)

## 背景

ADR 0065 正确冻结了 ResultIngress → runstore 的单向 proof、锁序和 `S1 → S2` 相邻交付，但其 §10 把 S2 的实现边界收窄为只新增 `prepared_run_start_composition.go`、私有 helper 与 architecture gate。对当前生产代码的构造审计证明，这个文件边界无法落地：

1. `internal/productionruntime` 只有 package-private `newController`/`newRuntime`，没有 fixed `./bin/marshal` 可调用的 production factory；`Runtime.Status` 仍固定返回 `production-composition-incomplete`；
2. CLI 的写路径仍主要沿 legacy `execution.Run`，独立 `marshal-server` 仍保留 child CLI 兼容路径；这些路径不能冒充 ADR 0062 的 fixed Marshal production composition；
3. `openRepositoryOwnerLock` 在取得锁之前要求完整 `ControlOwnerAcquisition`，但该 acquisition 的下一 `OwnerEpoch`、前驱 fact digest 与 current owner 又只能在锁内打开 ResultIngress、执行 `OpenOwner` 并观察当前 Core 后确定；这形成“先有 acquisition 才能加锁、先加锁才能安全产生 acquisition”的构造环；
4. owner、ResultIngress、Run store 与 Supervisor control root 还没有由唯一 factory 冻结为 `StateRoot` 下的确定性布局，测试可以手工拼装组件，却不能证明真实 binary 只打开同一组 durable objects。

继续只写 composition helper 会得到不可构造的孤立接线；绕过 owner lock 或预先猜测 owner epoch 又会降低 ADR 0062/0065 的门禁。因此必须在 S2 实施前纠正构造顺序和允许修改的最小文件边界。

## 决策

### 1. 关系、范围与状态

1. 本 ADR 是 ADR 0065 的 **S2 implementation successor**，并精确部分取代 ADR 0065 §10 第 2 项及 §7 中“production 组合只需新增单一 composition 文件”的实现文件边界。ADR 0065 的 S1/S2 顺序、shared-guard proof、ResultIngress → runstore 单向依赖、固定锁序、generic `Append READY → RUNNING` 禁止和 hostile/replay 门禁全部保留。
2. 本提议不取代或放宽 ADR 0062 的信任模型：生产身份仍只有 fixed `marshal`；独立 `marshal-server` 仍非生产；owner-only AF_UNIX、peer credential、binary/path/SHA-256/CDHash/sourceHead/profile recheck、禁止 child CLI/legacy selector/匿名 Mach-O 的规则不变。
3. 本提议不新增 authority store、owner 事实类型、Run 状态、Provider、发布权限或 fallback。它只冻结现有 owner fact 的安全构造顺序、确定性目录布局、唯一 factory 与 S2 的必要接线范围。
4. 本 ADR 未接受前只记录治理修正，不能授权 S2 实现或升级成熟度。提议、独立审查和维护者接受属于 S2 的治理前置，不算在 S1/S2 之间插入第二个 component；S1 完成后仍必须立即进入经接受合同约束的 S2。

### 2. 两阶段、descriptor-bound repository owner lock

repository owner acquisition 固定分为两个阶段；两阶段由同一个 factory 同步完成，不暴露可分开调用的 production API。

#### Phase A：只按 scope 取得物理 owner lock

1. factory 先从已验证的 `StateRoot` held directory、`AuthorityNamespaceID` 与 repository identity digest 构造唯一 `ControlOwnerScope`。scope 不含 owner epoch、PID、binary observation、时间或前驱 fact。
2. owner lock 文件名只能由 canonical `ControlOwnerScope` digest 机械派生。Core 通过 held owner-directory descriptor 以 nofollow/no-clobber 语义打开该文件，验证 owner-only regular identity 后取得 non-blocking exclusive OS lock。
3. Phase A 返回的内部对象只证明“本进程当前持有该 scope 的 repository 物理锁”。它不含、也不实现针对某个 `ControlOwnerAcquisition` 的 verifier；不得在此阶段打开 Run writer、Probe/Provision、启动 Supervisor/Agent 或追加 owner/Attempt fact。
4. scope 冲突、StateRoot/owner directory/lock entry ABA、锁忙或平台不支持均在业务 authority 和外部副作用前 fail closed。

#### Phase B：在物理锁内产生并绑定下一 acquisition

持有 Phase A descriptor/OS lock 后，factory 必须在同一临界区内按以下唯一顺序执行：

```text
open deterministic ResultIngress store
  → OpenOwner(exact ControlOwnerScope)
  → ObserveCurrentCore(exact fixed ./bin/marshal + current process + control root)
  → construct next ControlOwnerAcquisition
  → AcquireOwner(expected epoch/head) + fsync
  → re-open/replay exact owner fact
  → bind physical lock verifier to that exact acquisition
```

冻结规则：

1. `OpenOwner` 的 `not found` 只允许构造 `OwnerEpoch=1` 且空前驱；已有 owner 时只能构造 `OwnerEpoch=current+1` 并逐字节引用 current fact digest。epoch/head 不连续、同 epoch 不同 acquisition 或 ledger corruption 均 fail closed。
2. `ObserveCurrentCore` 必须复用既有 current Core observation，闭合当前 PID/birth、UID/GID、fixed binary path/SHA-256/CDHash/sourceHead/profile 与本次 control root。PATH、argv[0]、环境变量、调用者 DTO 或旧 observation 不能填充 acquisition。
3. `AcquireOwner` 必须在 Phase A 锁仍持有时 append 并 fsync 到现有 ResultIngress/RB1 ledger。fsync 后必须从同一 store 重放并证明 exact acquisition/fact digest；随后 physical lock 才能成为 `CurrentOwnerLockVerifier`，且只接受这一 acquisition。
4. owner fact fsync 前崩溃不产生 acquisition；fsync 后 response 丢失由下一 owner 在同一 scope 下 `OpenOwner` 观察并生成严格 successor，绝不重用旧 epoch。无法判断 append 是否完成时不得猜测或创建 sibling acquisition。
5. 绑定后的 verifier 仍是同步 borrowed verifier：callback 返回即失活，不可复制为 bearer capability。关闭 Runtime 必须先停止新 operation，再释放该 acquisition 绑定和 held descriptors；不得删除 owner fact或重置 epoch。

### 3. `StateRoot` 的确定性内部布局

Mac-first v1 factory 只接受一个已验证的绝对 `StateRoot`，并固定使用：

```text
StateRoot/runs/                         # 既有 runstore journal/lease 布局
StateRoot/runtime-v1/owner/             # scope-derived repository owner lock
StateRoot/runtime-v1/result-ingress/    # 唯一 ResultIngress/RB1 ledger
StateRoot/runtime-v1/control/           # Supervisor/control objects 的固定 parent
```

1. 不提供环境变量、可选 path map、临时目录或调用者任意子目录来替换这些位置；测试 fixture 必须通过不同的非生产 constructor 隔离。
2. `runtime-v1` 及其子目录只能从 held `StateRoot` descriptor 逐级 nofollow 打开或 owner-only 创建；首次创建须同步新目录及其 held parent。已有对象的 type/owner/mode/device/inode/current-name 任一不符均 fail closed。
3. `runstore` 继续复用既有 `StateRoot/runs`，不得迁移或复制 Run journal。ResultIngress 只打开上述唯一目录，不创建平行 ledger。control 下的 per-Attempt 子目录继续受 ADR 0059/0064 约束。
4. Phase A 只能准备并取得 `runtime-v1/owner` 中的 scope lock；其余 writer store 只能在 Phase B 锁内打开。目录存在本身不构成 owner、Run 或 Attempt authority。

### 4. 唯一 Darwin arm64 production factory

`internal/productionruntime` 只允许一个 production-exported、语义等价于 `OpenDarwinArm64ProductionRuntime` 的 factory。它必须：

1. 只在 `darwin/arm64` 构建；其他平台的同名入口在任何 StateRoot、Run、Probe、Provision 或 process 副作用前返回 `platform-profile-unavailable`；
2. 证明当前进程执行对象就是可信 repository 中 canonical `./bin/marshal` 的 exact current object，并复用 ADR 0051/0062 的 identity/profile 门禁；替代 path、PATH 命中、symlink、匿名/临时 Mach-O 或另一份相同内容 binary 均不得进入 production factory；
3. 按第 2、3 节取得 owner、打开 ResultIngress 与既有 runstore，构造 S1 所需的 concrete authority/projector、Pi profile/ProcessBridge、controller 和 `Runtime`；缺任一 mandatory dependency 就关闭已打开对象并 fail closed，不得返回部分 Runtime；
4. factory 是 production component graph 的唯一创建者。CLI 与 `marshal control-plane serve` 只能持有它返回的 `application.PublicApplicationPort`；不得直接 new controller/store/bridge，factory 失败时不得 fallback 到 `execution.Run`、child CLI、memory-only store 或 Fake seam；
5. `Runtime.Status` 只有在 factory 已完成两阶段 owner bind、全部 mandatory store/recovery/profile 检查且无 pending recovery 时才可报告 available；继续缺失 composition 时必须保留 `production-composition-incomplete`，不得把对象构造成功误写成端到端可用。

### 5. controller 与 sealed proof 的唯一组合点

1. `controller.startPreparedRun` 是 production 中 `composePreparedRunStart` 的唯一调用者；它不得再调用旧 `ProcessBridge.StartPreparedRun` 后猜测 `RehydrateRunStartOutcome`。
2. `composePreparedRunStart` 必须继续满足 ADR 0065 §7 的 typed AST 形状：一次 `WithPreparedRunStartAuthority`，其 direct `FuncLit` 内一次 `StartPreparedExecution`，borrowed projector 只作为最后一个实参出现一次。
3. factory、`Runtime`、CLI/server adapter、ProcessBridge 和其他 production 文件对两个 S1 exported seam 均为零调用。ResultIngress 仍 mint proof，runstore 仍只做 self-only CAS；本 ADR 不改变 proof 方向、claim 字段或 response-loss 规则。
4. controller 只能在 factory 已绑定的 repository owner临界区内调用 helper。helper 不 acquire owner、不重新打开 runstore、不创建 store，也不把 projector/proof/claim 保存到 controller 或 Runtime。

### 6. S2 允许修改面与验证

经本 ADR 接受后，ADR 0065 S2 的允许修改面纠正为以下封闭类别：

- `internal/productionruntime` 的 `owner_lock*`：实现 scope-only Phase A 与 acquisition-bound Phase B；
- controller：让 `startPreparedRun` 成为 `composePreparedRunStart` 的唯一调用者；
- 单一 Darwin arm64 production factory及其 non-Darwin fail-closed stub；
- `prepared_run_start_composition.go`；
- repository architecture tests；
- 一个通过 fixed `./bin/marshal` 和同一 production factory 的真实 Pi prepared-start E2E。

S2 不得修改 S1 的 proof/claim 语义，不得顺带实现 terminalization、cleanup、Provider 扩面、server transport、Linux/hardened authority、签名/notarization或 release。若以上封闭文件类别不足以从 fixed `./bin/marshal` 到达同一 factory，S2 必须停止并回到治理审查；不得静默扩大为通用 CLI/server 重写。

验证至少证明：

1. 两个并发 factory 只有一个 scope lock/owner acquisition winner；owner fact append 前后 crash、response loss、旧 epoch、scope/root漂移与 owner lock entry ABA 全部 fail closed或产生唯一 successor；
2. StateRoot 目录 symlink/type/owner/mode/current-name 漂移、替代 ResultIngress root、第二 runstore/ledger 与环境 path override均在副作用前拒绝；
3. architecture test机械证明 factory、controller/helper和两个 S1 seam的唯一 production call graph，以及 legacy `execution.Run`/child CLI/Fake seam从该 profile不可达；
4. fixed `./bin/marshal` 的真实 Pi E2E只能在 exact successful resume后追加一个 `RUNNING` successor；Run append后 response loss只从runstore replay，不重发Supervisor command；
5. `Status` 在 composition未完整、pending recovery、owner漂移和完整可用四种情况下返回封闭、真实结论；
6. 定向测试、相关 race、`go vet`、staticcheck、architecture/diff/secret/mergeability门禁通过，且独立 reviewer对 exact sourceHead 的 P0/P1为0。

## 后果

正面结果是 owner epoch 不再被预先猜测，单一 factory 能从确定性 `StateRoot` 对象构造真实 production graph，ADR 0065 的 proof也有一个可执行且机械唯一的 controller callsite。代价是 S2 比原先“一个 composition 文件”多触及 owner lock、factory和controller，但仍局限于当前阻塞纵切。

本提议不使 S1/S2 自动完成，不改变 R2–R5 的 `COMPONENT`，也不授权真实 Pi、terminalization、Linux或发布。只有独立审查后由维护者接受，S2 才能按本边界实施。

## 拒绝的替代方案

### 在加锁前调用 `OpenOwner` 并猜测下一 epoch

拒绝。另一个进程可在观察与加锁之间提交 successor，产生 check-then-act 与 sibling acquisition 风险。

### 让物理 owner lock 继续绑定调用者预制 acquisition

拒绝。这保留构造环，并允许测试 DTO或旧进程 observation成为 owner事实输入。

### 只新增 composition helper，不提供 production factory

拒绝。孤立 helper不能从fixed binary到达，也不能关闭 `Runtime.Status=production-composition-incomplete` 或 legacy路径可达性。

### 借 S2 一次重写 CLI/server、terminalization 与发布

拒绝。它会破坏 S1→S2 的bounded adjacency并把不同生命周期/发布门禁混成一次不可审计变更；这些仍按既有ADR在S2后分切片推进。
