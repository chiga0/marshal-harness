# ADR 0052：v1.0 发布范围与生产可达性门禁

- 状态：已接受（Accepted）
- 日期：2026-08-27
- 决策者：Marshal 维护者
- 相关：Issue #186、ADR 0016–0019、ADR 0043–0045、ADR 0047–0048、ADR 0051

## 背景

M0–M9 产生了大量有效组件、契约、测试与历史验收证据，但生产 import graph 审计确认，真实写任务仍主要沿 `cmd/marshal → internal/cli → execution.Run → Adapter.Run` 运行。`spine`、`agentruntime`、`runtimeprofile`、`bindingcheck`、`revokedrain` 与 `resultingress` 等新组件尚未同时进入一条真实生产调用链；部分权威状态仍只存在于内存实现。把“组件存在并通过测试”表述为“端到端能力完成”，会掩盖 v1.0 的真实缺口。

同时，原 M10–M13 路线把 Cloudflare、HA、多用户、完整 Provider 矩阵与 Goal DAG 一并放进首个正式版本，扩大了范围；ADR 0045 又要求 old/new 路径对真实 Agent 的内容摘要逐字段相等，而真实 Agent 输出天然非确定，该条件无法成为可执行的 cutover 门禁。

本 ADR 收窄 v1.0，增加生产可达性成熟度，并修正真实 Agent 的 cutover 等价性。它不降低 Worker 不自证、单 worktree 单写入者、Worker/Publisher 分权、Evidence 精确绑定、路径与凭据边界、默认不 merge 等既有不变量。

## 决策

### 1. v1.0 支持范围

v1.0 是**单节点、单用户、可信仓库**的自托管正式版本，必须交付：

1. 一条唯一、真实、可恢复的生产执行链：`marshal` 或 loopback `marshal-server` → durable Run journal → Core-owned `WorkerExecutor` → Sandbox allocation → `AgentRuntime` → `ResultIngress` → independent verification/review → Outcome；
2. 至少一个真实 `AgentProvider`，以及至少一个真实 Local 或 Container `SandboxProvider`；Agent 进程必须实际运行在该 allocation 中；
3. 文件型耐久 authority ledger、幂等提交、重启恢复、旧 generation fencing 与单一恢复模型；
4. 每个 Attempt 的 Agent/Sandbox 双 binding，以及所有结果接纳前的 current-ledger recheck；
5. 可判定的 cancel、timeout、retry、terminal 与 Outcome 语义；
6. 独立 Verification；发布仅支持 `publication:none` 与可选 GitHub Draft PR，默认且自动 merge 均不属于 v1.0；
7. loopback `marshal-server` 能启动、取消、查询并恢复真实 Run，而不是只提供独立 API/transport 组件；
8. kill、restart、lost response、stale/replayed result、binding/lease drift 的故障注入与恢复测试；
9. macOS 与 Linux 的稳定安装产物；macOS release 产物还必须通过稳定路径、签名、notarization 与 release identity 门禁。

Local ordinary-user profile 可以作为 v1.0 支持的 trusted single-user 配置，但不得宣称 `hardened`、恶意代码隔离、远程 authority 或 cloud production assurance。正式发布必须明确展示该 assurance 上限。

### 2. v1.0 非目标

下列能力延期至 1.x，不阻塞 v1.0：

- 多节点 HA、多用户或多租户；
- Cloudflare 的完整生产拓扑与全部远程 Provider authority；
- 所有 Agent/Sandbox Provider 的 hardened 支持矩阵；
- Goal DAG、动态重规划与复杂长期编排；
- 完整远程 SDK/生态协议矩阵；
- 超出 v1 最小立即 revoke/fence/terminate 的通用 bounded drain；
- Web UI 与自动 merge。

M10–M13 保留为历史规划和 1.x 候选池，不再是 v1.0 发布门禁；其范围在 `I186-R6` 后重排。

### 3. 成熟度与生产可达性

Milestone 状态仍只使用 `PLANNED`、`IN_PROGRESS`、`PASSED`。另增加正交的能力成熟度：

| 成熟度 | 含义 |
| --- | --- |
| `DESIGN` | 只有 ADR、Schema 或合同，尚无实现。 |
| `COMPONENT` | Package、类型和测试存在，但真实 composition root 不可达。 |
| `INTEGRATED` | `cmd/marshal` 或 `cmd/marshal-server` 的真实 composition root 可达，且真实 Agent/结果 bytes 穿过该路径。 |
| `RELEASED` | 集成路径通过 v1.0 release gate 并进入受支持发布产物。 |

任何 R1–R6 阶段都不得仅凭 component test、Fake fixture 或独立 transport/API 测试关闭。关闭至少需要：

- 从真实 composition root 可达；
- 实际 Agent 进程由 Sandbox allocation 承载；
- 实际结果只经 `ResultIngress` 接纳；
- authority、recovery 与 failure fixture 在同一路径生效。

据此，历史 M8/M9 `PASSED` 仍保留为当时定义的退出证据，但其相关 Runtime 资产当前成熟度为 `COMPONENT`，不能证明 v1.0 生产集成完成。`I186-R0` 为 `PASSED`；`I186-R1` 为 `IN_PROGRESS`；`I186-R2` 与 `I186-R3` 为 `PLANNED`，均可复用已经存在的组件资产，但必须重新取得生产可达性证据。

### 4. 权威收敛

v1.0 复用现有 durable Run journal、lease 与 current ledger 作为唯一 authority source。不得再引入并行的 memory-only Run、lease、outbox 或 acceptance truth。内存结构只能是可重建 projection/cache；进程重启后必须由 ledger 重建。

Local v1 profile 中，Core 持有的 process handle、PID/process group、cwd 与 allocation lineage 是执行位置的权威 observation。Provider 自报的 location、resource 或 failure 只能作为诊断输入或收紧处置，不能放宽 retry、预算、Evidence、publication 或 assurance。

### 5. Cutover 等价性

本节部分取代 ADR 0045 §1 第 1 项对真实 Agent 的“全部内容摘要逐字段相等”要求：

- Deterministic Fake 路径继续要求 exact content/digest equality；
- 真实 Agent 路径比较 authority invariants：相同 TaskSpec/base/scope/policy、无路径扩张、相同 lifecycle/terminal 语义、双 binding 生效、唯一 ResultIngress 接纳、无重复副作用、故障与资源指标不劣于冻结阈值；
- 真实 Agent 的自然语言、patch 或 transcript 内容可以不同，但每次输出必须独立验证，不允许用人工解释覆盖 authority diff。

### 6. I186-R0→R6 的最短发布路径

| 阶段 | v1.0 交付 |
| --- | --- |
| R0 | 保留已完成的 rebaseline、ADR 与 baseline evidence。 |
| R1 | 在现有 `execution.Service` 唯一 seam 上接通真实 Agent-in-Local/Container allocation。 |
| R2 | 把 command/result authority 收敛到现有 durable journal；`ResultIngress` 事务化接纳真实结果，移除并行内存真值。 |
| R3 | 为每个 Attempt 落地 Agent/Sandbox 双 binding；Local profile 使用 Core-held process observation；revoke 时立即 stop-new/cancel/fence/terminate。 |
| R4 | 在真实路径上实现唯一 recovery decision 与 `marshal explain`。 |
| R5 | 运行 canary/cutover；旧 host `Adapter.Run` bypass 降级为非 production 后从 v1 支持路径删除。 |
| R6 | 故障 conformance、跨平台打包、macOS 签名/notarization、升级/回滚和正式发布。 |

## 后果

- v1.0 不再被 Cloudflare、HA、多用户、完整 Goal orchestration 阻塞；
- 已有组件不会丢弃，但“完成”必须由生产可达性而不是 package 数量证明；
- 现有真实执行链成为 strangler 的迁移起点，而不是继续平行建设第二套 Runtime；
- R1–R3 状态会回调，这属于纠正证据口径，不是否定历史实现工作；
- 1.x 仍可继续实现 ADR 0016–0019 的长期架构，不需要为 v1.0 复制一套简化状态机。

## 接受依据

维护者在审阅生产 import graph、Run/Attempt 结果、现行 Roadmap 与 Issue #186 路线后，于 2026-08-27 明确接受本次 v1.0 范围重置。接受只冻结范围、成熟度与 cutover 合同，不表示 R1–R6 已实现或 v1.0 已可发布。
