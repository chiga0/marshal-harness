# ADR 0055：Sandbox Exec workload envelope（workdir/bounded-env/transcript sink，修订 ADR 0017）

- 状态：已接受（Accepted，2026-08-27）；接受依据：维护者授权的 I186 快速收敛治理（Lead 直接冻结、停用独立 reviewer 轮转）。接受只冻结本合同（SPI envelope 扩展）；不表示任何 Provider 已实现完整 conformance、不代表 Local/任何 Provider 获得 hardened assurance、不解除 Darwin self-identity（Issue #212，ADR 0047/0048/0051）的 release gate。
- 关联：[ADR 0016](0016-durable-runtime-and-sandbox-provider.md)（Sandbox SPI）、[ADR 0017](0017-provider-neutral-sandbox-contract.md)（二维权限/隔离模型、Stage 内容寻址、probe 敌对测试负载原则）、[ADR 0052](0052-v1-release-scope-and-production-reachability.md)（§1.2 Agent 进程必须实际运行在 allocation 中）、[ADR 0043](0043-worker-executor-profile-and-dual-binding.md)（WorkerExecutor/AgentLaunchSpec）、Issue #186。

## 背景

ADR 0052 §1.2 要求真实 Agent 进程实际运行在 Sandbox allocation 中。现状的 `ExecRequest`（`Command []string + Stdin []byte`）无法承载真实 CLI Agent：执行 cwd 固定为 allocation 内部目录、环境被 sanitized、stdout 只回传 digest（无 transcript 面）、超时为 runner 内部常量、argv[0] 不允许落在 allocation 外的 executable 上。旧 bridge（R5）因此只能 adapter.Run 自启进程 + Local 记账，不满足 ADR 0052 的 allocation-carried 判定。

本 ADR 以最小幅度冻结 Exec envelope 的三个补充维度，使**任一满足 ADR 0017 的 Provider**都能承载真实 Agent，同时不放松任何既有边界（hardened 举证、Stage 内容寻址、probe 敌对原则、ADR 0052 的非 hardened 语义上限）。

## 决策

### 1. WorkingDir binding

1. `ExecRequest` 可选携带 `WorkingDir string`。
2. 生效条件（Provider 必须逐一 fail closed 判定，否则拒绝整个 op）：
   - WorkingDir 必须由请求同一 Attempt 在 Setup/Provision 阶段声明，且 Provider 登记其与该 allocation 的绑定（绑定事实写入 allocation 记录）；
   - WorkingDir 必须为绝对路径；Provider 侧必须存在；
   - Provider 对它的任何路径改写/软链接穿越至未声明目标一律拒绝。
3. 未携带 WorkingDir 时行为与 ADR 0017 现状一致（allocation 内部目录）。
4. WorkingDir 仅声明执行根，不授权任何额外文件系统写入；写入边界仍由 AccessMode/既有 SPI 规则判定。

### 2. Bounded environment（环境白名单直通）

1. `ExecRequest` 可选携带 `Environment map[string]string`。
2. 允许直通的键集合在 **Provision 时由调用方声明且 Provider 将其写入 allocation 快照**：`EnvironmentAllowlist []string`，封闭枚举，未知键请求 fail closed、不得默认 union。
3. Provider 必须为每个这次 op 构造环境：白名单键取自调用方值；其余维持 sanitized 基线。请求环境包含白名单外键一律拒绝。
4. 键集合不允许出现凭据语义字段（密钥/token/password 字样）——这属于逃避 ADR 0018「凭据不入业务 JSON/事件/日志/digest」的通道，Provider 拒绝登记与直通。

### 3. Transcript sink（有界 transcript 面）

1. `ExecRequest` 可选携带 `TranscriptPolicy{MaxBytes int64, ArtifactId string}`；`MaxBytes>0` 且 `ArtifactId` 非空。
2. Provider 必须把该 op 的 stdout 以**追加式有界捕获**收集，超出 MaxBytes 立刻 kill 进程并按 fail closed 处理（`ExecutionKilled` + 封闭原因码，不返回部分结果）。
3. 完整捕获结束时，Provider 把 transcript 写成内容寻址 staged artifact（ArtifactId 对应 digest 由 Provider 重算并体现在 ExecReceipt）；解密和语义归纳一律不属于本 SPI（归 AgentRuntime/结果链）。
4. 未携带 TranscriptPolicy 时保持现状：只回传 digest。
5. stderr 遵循同一捕获规则（可选的另一份 artifact），未发现 transcript 请求时不得主动捕获 stdout/stderr 原文（避免persistence 偏差）。

### 4. Per-op timeout from caller

1. `ExecRequest` 可选携带 `TimeoutSeconds int64`；>0 时以 ctx 和 `min(TimeoutSeconds, Provider 上限)` 生效，违反上限 fail closed。
2. 超时即 kill，返回 `ExecutionKilled` 与封闭超时原因码；状态不入 ExecReceipt 的正常完成分支。

### 5. 边界与非目标

1. 本 ADR 不引入新的 Provider 类型、不修改 ADR 0017 的 probe 敌对职责（probe workload 仍只由被测 Provider 创建、身份明确的 target allocation 承载）；conformance 套件**必须**新增以下负测：
   - 请求未声明的 WorkingDir / 非绝对路径 / 含软链接穿越的 WorkingDir；
   - 环境白名单外键直通与凭据语义键登记；
   - TranscriptPolicy.MaxBytes 超限、ArtifactId 为空、transcript 重算 digest 不符；
   - TimeoutSeconds 有界性；
2. 本 ADR 不改变 Local/ordinary-user 的 assurance 上限：allocation-carried 执行是普通宿主进程记账语义，不是 hardened authority、不是恶意代码沙箱、不支撑一切 assurance 声明（ADR 0017/0042/0052 一致口径）。
3. Agent/Sandbox 每一项 SPI 新增负测以下一节「后果与门禁」为合入门槛；现状的 `spi.go` envelope 已冻结的判错语义（identity/fencing/resolveAllocation）全部保留。

## 后果与门禁

- 冻结合同：`ExecRequest` 增加 `WorkingDir`、`Environment`、`EnvironmentAllowlist`（Provision 申请时登记）、`TranscriptPolicy`、`TimeoutSeconds` 四个可选维度；Local 实现其语义 fail-closed；Fake 同步支持 conformance。
- 合入门槛：conformance suite 的正/负 fixture 全部通过 + 既有 SPI 判错语义不退化 + 约定文档（docstring）与实现逐字一致。
- R1（0052 §6 的「真实 Agent-in-Local/Container allocation」）在本 ADR 之上实现：bridge 通过 configurable adapter 获得 AgentLaunchSpec（argv/env/cwd/timeout），经本 envelope 在 allocation 中执行，产出 transcript artifact，再经同一 Adapter 的 Decode/Finalize 管道路由回 WorkerResult；未实现 LaunchSpec 的 Adapter 继续走 legacy compat 路径。
- 本 ADR 不升级 M8–M13 或 v1.0 状态，不声明任何 hardened/production assurance。
