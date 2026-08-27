# Roadmap 状态

更新时间：2026-08-27

本 Roadmap 交付[整体架构](architecture.md)定义的长寿命、可自托管、确定性 Control Plane。Local MVP 是已经可用的 embedded/local 先行实现与持续回归基线，不是 Marshal 的最终产品范围。

> **2026-08-27 v1.0 路线重置（[ADR 0052](adr/0052-v1-release-scope-and-production-reachability.md)）**：继续使用 [Issue #186](https://github.com/chiga0/marshal-harness/issues/186) 的 `I186-R0→R6` 纵切框架，但以 production reachability 重新判定状态。M0–M9 历史 `PASSED` 结论与代码资产保留；M8/M9 的 Runtime 资产当前成熟度为 `COMPONENT`，不表示 v1.0 端到端集成。当前 `I186-R0: PASSED`、`I186-R1–R5: IN_PROGRESS`、`I186-R6: PLANNED`。R3-A/B/C/D 已有类型、纯核心与定向测试可复用，但在真实 composition root、双 binding 与 ResultIngress 接线完成前只能记为 component checkpoint。M10–M13 不再阻塞 v1.0，作为 R6 后的 1.x 候选重新排期。

## v1.0 生产纵切

Milestone 状态与能力成熟度是两个维度：

| 成熟度 | 含义 |
| --- | --- |
| `DESIGN` | ADR、Schema 或合同存在，尚无实现。 |
| `COMPONENT` | Package、类型与测试存在，但真实 composition root 不可达。 |
| `INTEGRATED` | `cmd/marshal` 或 `cmd/marshal-server` 可达，真实 Agent 与结果 bytes 穿过该路径。 |
| `RELEASED` | 集成路径通过 v1.0 release gate 并进入受支持产物。 |

| 阶段 | 状态 | 成熟度 | 当前结论 |
| --- | --- | --- | --- |
| `I186-R0` | `PASSED` | `DESIGN` | rebaseline、ADR 与 baseline evidence 已完成。 |
| `I186-R1` | `IN_PROGRESS` | `INTEGRATED` | 真实 Agent（pi）由 Local allocation 承载执行：`local.NewLocalRunner` → `sandboxbridge` exec-chain（`abf80e9`+，`544680b` 起含双 binding 接纳）；`internal/sandboxbridge` 集成证明 + cli `TestTaskRun*` 一致通过；另副侧带外的 macOS dogfood gate 激活 binary 仍受宿主企业策略阻断（固定 Mach-O 致命，go run 不可跨进程绑定 dogfood activation，属 Issue #212/ADR 0051 范畴）。 |
| `I186-R2` | `IN_PROGRESS` | `COMPONENT` | durable journal/lease 与 ResultIngress 组件可复用；真实结果经文案 `internal/resultbinding` 双 binding + resultingress DRC 接纳（R3 生命面）；尚未构成单一命令级 authority 收敛完成型态。 |
| `I186-R3` | `IN_PROGRESS` | `INTEGRATED` | 每个 Attempt 的 Agent/Sandbox 双 binding + admission 时刻 live Inspect 回读的 current-ledger recheck 已接入真实执行链（`internal/resultbinding` + bridge admission，sandbox-binding-admission.json anchor 持久化进 attempt 目录；live-terminated/replaced 拒绝面覆盖）；尚未对接 Darwin 侧可信 dogfood 全帧。 |
| `I186-R4` | `IN_PROGRESS` | `INTEGRATED` | `marshal explain run RUN_ID`（`6a26012`）+ 恢复路径唯一消费恢复模型（`2bf4f3e`：supervisor 死 driver 分派与 `task run --recover-dead-driver` 逃生舱均唯一装配 `recovery.Decide`，需幂等键对账的 ambiguous side effect 一律 fail closed 指向 explain）；回归除已知 opencode 宿主版本漂移外全绿。 |
| `I186-R5` | `IN_PROGRESS` | `INTEGRATED` | 真实 pi canary 单轮通过（`3e6ed10` `TestRealPiExecChainCanary`：gated 默认跳过，标准 CLI 纵切经默认 exec-chain + 双 binding admission anchor）；cutovereq 三分判据（ADR 0054，真实 Agent 比较 authority invariants，Fake 才要求 exact digest）随摄回归；real-real canary 多轮对比与远程 trace 归 R6 conformance。 |
| `I186-R6` | `PLANNED` | `DESIGN` | failure conformance、跨平台安装、macOS 签名/notarization、升级/回滚与 release；当前分别受 Issue #212（签名身份未 provision）与宿主策略阻断。 |

v1.0 仅支持单节点、单用户、可信仓库、至少一个真实 AgentProvider 和一个真实 Local/Container SandboxProvider。Cloudflare 完整生产拓扑、HA、多用户/多租户、全部 Provider hardened 矩阵、完整 SDK/Web UI 与 Goal DAG 延期到 1.x。

## 快速收敛线路交付记录（component checkpoint，路线重置前 2026-08-27 交付）

> 以下记录为 Lead 快速收敛治理（单 Lead + 多 Sub-Agent、停用独立 reviewer 轮转、Lead 直并，仅保留防错误发布/数据破坏/trust-boundary ADR 硬约束）下的交付证据。状态判定一率以 ADR 0052 的生产可达性口径为准：这些资产记为 component checkpoint，不另行宣称阶段 DONE。

- R3-D/E/F 快速收敛交付：R3-D `ec13ee7`（internal/revokedrain 撤销分级处置）+ `0a9b3b6`（internal/attemptgate per-Attempt 双 binding recheck + 证据边界负测）、R3-E `c47b4c2`（internal/locationattest claim/fact 分型）、R3-F `d89c65e`（internal/failureclass 失败分类 authority），[ADR 0049](adr/0049-location-attestation-and-failure-classification-authority.md) 冻结 E/F 合同并修订 ADR 0043 §5；Exit Gate 证据对照与 finding 关闭见 [audit-report.md](audit-report.md)；R3-D1/D1b 历史 Marshal Run 的 REJECTED 记录保留，其中 D1b 实现经修复公式后采用。Issue #209 与 PR #211 已闭环；Issue #210/#212 转入 dogfood 沉淀。R0 产物见 [i186-r0-baseline-report.md](research/i186-r0-baseline-report.md)；Pre-R4 contract gate 四项（hot-path authority、JIT admission、protocol migration、Candidate identity）在 R4 启动实现前随 R4 首批切片补齐。
- R4 快速收敛交付：Pre-R4 四项合同（hotpath `f65cfaf`、jitgate+candidateid `c4c8b69`、protocolrev `ab7b263`）+ 单一恢复模型（recovery `34f70d3`，故障矩阵八类唯一幂等结论 + explain 等价 API），[ADR 0053](adr/0053-pre-r4-contract-gates-and-single-recovery-model.md)（原编号 0050）冻结全部合同并修订 ADR 0044 冷热路径条款；证据与 finding 关闭见 [audit-report.md](audit-report.md)。
- R5 快速收敛交付：cutovereq/effectsink（`b3e193d`）+ worker executor seam 与 sandboxbridge 生产接线（`ab93174`）+ golden 判定 harness 与 rollback 演练（`6285a15`）+ [ADR 0054](adr/0054-cutover-equivalence-and-effect-sink-fencing.md)（`6144f24`，原编号 0051）+ 默认翻向与 fencing 修复（`20c5609`）；Exit Gate 对照与范围标注见 [audit-report.md](audit-report.md)。
- R6 快速收敛交付：perfbench 基线（`1f81286`）+ soak harness 与 recovery 两缺口修复（`0208964`）+ bridged 孤儿 allocation 对账（`97147a1`）+ 路径级 accelerated soak（`dc6d7ed`）+ 真实 Pi canary（`run-i186-r6-canary`，9 Gate 独立验证通过）；Exit Gate 对照与 honest gaps 见 [audit-report.md](audit-report.md)。

| Milestone | 状态 | 证据 |
| --- | --- | --- |
| 0：Toolchain 与 Contract | `PASSED` | [验收报告](milestone-0-report.md) |
| 1：State Machine 与 Run Store | `PASSED` | [验收报告](milestone-1-report.md) |
| 2：Git Worktree 与独立 Verification | `PASSED` | [验收报告](milestone-2-report.md) |
| 3：Review 与 Rework Loop | `PASSED` | [验收报告](milestone-3-report.md) |
| 4：首个真实 Worker Adapter | `PASSED` | [验收报告](milestone-4-report.md)；GitHub Actions `30879438415` |
| 5：GitHub Draft Publisher | `PASSED` | [验收报告](milestone-5-report.md)；主分支 CI `30889069165`；[真实 Draft PR #1](https://github.com/chiga0/marshal-harness/pull/1) 与 PR CI `30889190854` |
| 6：其余 Adapter 与 Recovery 加固 | `PASSED` | [验收报告](milestone-6-report.md)；真实受监督 cmux Pilot 通过；Full MVP E2E Run `m6-mvp-e2e-r3-20260805` `ACCEPTED`，[Draft PR #2](https://github.com/chiga0/marshal-harness/pull/2) 与 PR CI `30974239712` 全绿 |

embedded/local 先行实现的 Local MVP 定义达成：标记 `USABLE`。

## Qoder/Codex production authority 共同阻塞

[ADR 0038](adr/0038-agent-production-authority-provider.md) 已于 2026-08-18 接受：它为 ADR 0034/0037 已冻结但尚未由真实宿主提供的外部 authority 层冻结共享 `AgentProductionAuthorityProvider` Port，包括独立在线 verifier、OS isolation/audit receipt、host attestation、monotonic fence、held-fd identity、原子 authority bundle、stopped-child launch receipt/workload barrier，以及 rotation/revocation/crash reconcile。接受只允许进入实现，不表示 Qoder/Codex 或任何当前宿主已 `supported`。

接受本 ADR 不改变 Roadmap 状态，也不表示 Qoder 或 Codex 已可生产调度。Linux 只有在对应 profile 的平台机制、真实 credentialed probe 与独立 conformance 全部通过后才是候选；Darwin 的 Qoder/Codex profile 在等价强制机制与后续合同通过前保持 `unsupported`。关闭条件见[设计审计报告](audit-report.md)中的 `AGENT-AUTHORITY-*` open findings。

## Mac-first Adapter 阶段性证据（2026-08-24）

本节只记录当前宿主的 ordinary-user 证据，不改变 M6 已通过结论，也不把 M10–M13 或 v1.0 标记为完成。Qoder 与 Qwen 两行证据分别绑定 commit `9410e75`（fix(adapter/qoder)：未知 system 帧从 fail-closed 改为非语义忽略，adapterVersion bump 到 `0.1.7`，conformanceEventContract 保持 `v7`）与 commit `2c67e7e`（feat(adapter/qwen)：版本策略从精确锁改为 semver 范围 `>=0.21.5 <0.22.0`，`0.21.15` 现在 supported）。ADPT-03 行绑定 commit `b35d374`（fix(adapter/qoder)：argv 预授权 `--allowed-tools Bash`，版本下限 `1.1.23→1.1.27`，adapterVersion `0.1.7→0.1.8`，transport digest 不 bump）。

| Adapter | 当前事实 | 结论 |
| --- | --- | --- |
| Qoder CLI `1.1.27` | `marshal doctor` 在 macOS ordinary-user profile 下报告 `configured=true`、`registered=true`、`compatibility=supported`、`adapterVersion=0.1.7`；conformanceEventContract 保持 `v7`；固定 executable `/Users/gawain/.qoder/bin/qodercli/qodercli-1.1.27`，digest `sha256:fd36420ae0e740f7f3fb7f62e9df23aa70df400aad55fc7e7e48e0edc0ce8e2`。 | 该证据身份已被 ADPT-03（adapterVersion `0.1.8`）替代失效，仅作历史记录；不得复用为新派发证据。 |
| Qoder CLI `1.1.28`（ADPT-03） | 2026-08-24：adapter `0.1.8`（argv 含 `--allowed-tools Bash`、版本下限 `1.1.27`）以显式 `MARSHAL_QODER_PATH` 指向 qodercli `1.1.28`（digest `sha256:14b5aa00198986c2299084e5d87479d648db47fc4b85aaecb572e1cff3a1c4aa`）、`MARSHAL_QODER_MODE=ordinary-user`，`marshal doctor` 报告 `configured=true`、`registered=true`、`compatibility=supported`、`authorityMode=ordinary-user`，并通过了 Run `run-m10-wire-02-r2` planning selection 的真实 version/capability probe（CapabilitySnapshot digest `sha256:52c5c45b16e8e6bcc390772e869de9ede48d9ea5cd6469e86b2632fffe68fba9`）。 | 已完成晋升阶梯“真实只读 live probe”级证据并进入首个低风险写任务；仍需 fresh live Worker smoke、transcript attestation 与独立只读 conformance，未宣称 production authority。 |
| Codex `0.145.0` | `mac-codex-ordinary-smoke-r19-20260821` 与 `mac-codex-ordinary-smoke-r20-20260821` 各由唯一独立 reviewer 审查并进入 `ACCEPTED`；路径 `/opt/homebrew/Caskroom/codex/0.145.0/codex-aarch64-apple-darwin`，digest `sha256:1da3f4e0e96028b8a771814293c3033dafd1971f943f6c7e79b0897fe705f590`。 | Mac ordinary-user 运行证据已收敛；两个 smoke 仅为诊断任务，不产生产品代码 diff、发布或合并。 |
| Qwen Code `0.21.15` | `marshal doctor` 报告 `configured=true`、`registered=true`、`compatibility=supported`、`adapterVersion=0.1.0`、`binaryVersion=0.21.15`；版本策略为 semver 范围 `>=0.21.5 <0.22.0`，`0.21.15` 命中范围即 supported，minor 边界 `0.22.0` 及以上仍 fail closed。 | 范围准入证据已闭环，可调度普通 Worker；不升级为 hardened authority。 |

当前阶段的非阻断问题是两个 Codex smoke TaskSpec 文案仍引用 `r15` 路径，且 Markdown 产物声明为 `application/json`；后续 successor 应一次性修正文案，不为此逐项轮转 rework。普通用户模式不等于 hardened authority、APAP、sandbox 或 Linux authority。

## 已知阻塞与进展：Issue #25 已关闭，Issue #30 部分满足

公开 [Issue #25](https://github.com/chiga0/marshal-harness/issues/25) 与 [PR #24](https://github.com/chiga0/marshal-harness/pull/24) 暴露的「全部 required checks 成功且 PR 已合并后，`marshal task accept` 把 Run 永久置为 terminal `BLOCKED`」**已修复并关闭**（typed reconciliation 实现已合入：[PR #106](https://github.com/chiga0/marshal-harness/pull/106)，[ADR 0026](adr/0026-scm-merge-receipt-and-publication-reconcile.md)）：

- 活路径：`marshal task accept` 内联识别已合并且 required checks 全绿的 PR，采集不可变 `SCMMergeReceipt` 后经现行 checks-passed 路径进入 `ACCEPTED`；未合并 PR 仍走原 checks 观察流程；
- 补偿路径：`marshal task reconcile` 按 [ADR 0026](adr/0026-scm-merge-receipt-and-publication-reconcile.md) 冻结顺序，以 `SCMMergeReceipt` + append-only `PublicationReconcileRecord` + current-ledger recheck 共同门禁，把发布后的 terminal `BLOCKED` 安全迁移 `ACCEPTED`（`publication.reconciled` 事件是唯一命名终态例外，仅限 accept-after-merge），是发布合并后 Run 的权威恢复路径；全程 append-only、幂等、fail closed，不绕过 required checks 与 ReviewDecision，merge-never 不变；误入 terminal `BLOCKED` 的历史 Run 现可经 `marshal task reconcile` 对账恢复；
- 正式操作顺序与补偿命令见 [Operator Runbook §7](operator-runbook.md)，旧临时护栏已由 typed reconciliation 取代并废止；审计 finding `PUBLICATION-MERGED-HEAD-RECONCILE-P1` 随实现合入关闭（P1 → `CLOSED`），见[设计审计报告](audit-report.md)。ADR 0026 接受只冻结契约（PR #49），不升级 M8–M13 实现状态；本修复是 Local MVP 发布流程的实现层关闭，不改变 M0–M6 已通过状态与 Local MVP `USABLE` 结论。

公开 [Issue #30](https://github.com/chiga0/marshal-harness/issues/30)（CI deadline 观察语义）保持 `OPEN`，但已接受的 [ADR 0028](adr/0028-ci-deadline-phased-observation.md) 契约现已实现：TaskSpec 可用 `ciObserveTimeoutSeconds` 分离 CI 观察预算，publish 把 `ciDeadline` 冻结进 PublicationRecord digest；`marshal task accept` 与 controlled merge 均先观察并持久化 identity-bound RemoteCheckRecord，再以 provider `completedAt`、`publishedAt − 300s` 下界和 `ciDeadline + 300s` 上界裁决。deadline 后的及时完成证明仍可通过，pending 到期与缺失、迟到或不一致时间事实按封闭原因码 fail closed；四种 CI 时间原因导致的历史 `BLOCKED` 只有在 typed reconciliation 取得 fresh timely proof 后才可恢复。Issue 的远端关闭与补充回归证据仍独立跟踪，不影响上述当前实现语义。

M7–M13（M7–M12：耐久 Runtime 与可插拔 Sandbox Provider；M13：Goal orchestration）。[ADR 0019](adr/0019-deterministic-control-plane-typed-execution-and-goal-admission.md) 是 M7 通过后的已接受设计增补：它不回滚 M7，也不提前完成 M8–M13；确定性 Supervisor、Typed Execution、通用副作用对账/补偿与 Goal admission 均仍待实现。

### Milestone 状态取值定义

本文档全部 Milestone 状态只允许以下三种取值，其他文档引用 Milestone 状态时必须与本定义一致：

| 状态 | 含义 |
| --- | --- |
| `PLANNED` | 范围未冻结或未开始实施。ADR 接受、设计冻结与契约冻结**都不改变**该状态。 |
| `IN_PROGRESS` | 范围已冻结且已有 gate 落地 main，但该 Milestone 的退出门禁未通过。**不表示实现或 conformance 完成**，也不表示已落地的 gate 已接入执行路径。 |
| `PASSED` | 已通过该 Milestone 的退出门禁（实现、测试、独立审计、远端 CI 全绿）。对 M7 这类设计/契约阶段 Milestone，`PASSED` 只表示设计与契约阶段通过。 |

| Milestone | 状态 | 证据 |
| --- | --- | --- |
| 7：架构与契约 | `PASSED`（2026-08-11，只表示设计与契约阶段通过） | [ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md) 已接受；[ADR 0017](adr/0017-provider-neutral-sandbox-contract.md) 已接受（2026-08-10，接受只关闭设计歧义）；[ADR 0018](adr/0018-control-plane-and-provider-ports.md) 已接受（2026-08-11，接受只冻结设计）；[Runtime 架构](runtime-architecture.md) 同步；Marshal Run `m7-control-provider-boundary-adr-r15-20260811` 完成 M7 最终架构稿并 `ACCEPTED`（reviewRound=2，32/32 required Gates 通过，独立审查无 P0/P1）；[Draft PR #13](https://github.com/chiga0/marshal-harness/pull/13) 通过 Quality (ubuntu-latest)、Quality (macos-latest)、Secret scan 与 GitGuardian 检查（GitHub Actions CI run `31449333738`），2026-08-11 由维护者手工合入 main（merge commit `4b2f3248f24ec2a67642ec77822fe6bb59730df7`） |
| 8：Sandbox SPI/Fake/Local conformance + embedded/local 纵切 | `PASSED`（2026-08-13，退出门禁通过） | [验收报告](milestone-8-report.md)；六个硬门禁 gate 全部合入 main 且各 PR 远端 CI 全绿：gate-1（authority 双键空间 AuthorityNamespaceId/SecurityDomainId + SideEffect authority-record Schema，PR #42）、gate-2（ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence Schema + attestation 全链绑定，PR #45）、gate-3（legacy fail-closed mapper，PR #48）、gate-4（durable ProviderRegistration store + restart recovery，R2 lineage `m8-durable-registration-store-r2-20260812b`，PR #57）、gate-5（snapshot/evidence validation，PR #47）、gate-6（enable DispatchLease match，PR #60）；embedded 纵切：internal/sandbox SPI 类型 + Fake Provider + conformance 套件（PR #75）、Local SandboxRunner 宿主进程执行 + lease 绑定 + receipt observation（PR #80）、typed cross-domain edge 记录类型 + fixture 矩阵残留（PR #61）。各 gate 尚未整体接入最终 Runtime 执行路径；同期合入基础设施修复见[验收报告](milestone-8-report.md)。见[实施计划](implementation-plan.md) |
| 9：marshal-server、Public API 与 Durable Runtime | `PASSED`（2026-08-14，设计与契约+本 milestone 交付门禁通过） | 七交付全部合入 main 且各 PR 远端 CI 全绿：a lease 持久账本与 crash recovery（PR #104）、b typed edge 运行时接线（PR #107）、c1 marshal-server 常驻 + Public API（PR #111）、c2 SSE 只读投影（PR #115）、c3 远程注册 + TLS 基线（PR #116）、e DurableExecutionEngine seam（PR #119）、d Push/Pull 双拓扑 transport + outcome/invariant equivalence conformance（PR #120）；任务拆分见[M9 任务拆分设计](m9-vertical-to-server-design.md)。不表示 M10–M13 实现状态变化，不表示 conformance 终态；见[实施计划](implementation-plan.md) |
| 10：Cloudflare Provider（remote transport） | `PLANNED`（1.x 候选） | 不阻塞 v1.0；已合入代码保留为 fixture，R6 后按真实远程需求重排。 |
| 11：生产级存储、多节点 HA 与身份分离 | `PLANNED`（1.x 候选） | 不阻塞 v1.0；v1.0 使用单节点 durable file ledger，HA 与多用户在 R6 后重排。 |
| 12：Provider SDK/协议、多语言 SDK 与长稳平台扩展 | `PLANNED`（1.x 候选） | v1.0 只保留跨平台安装与 release conformance；完整 SDK/生态矩阵在 R6 后重排。 |
| 13：Goal orchestration | `PLANNED`（1.x 候选） | 不阻塞 v1.0；复杂 Goal DAG、动态重规划与累计预算在稳定 Runtime 发布后评估。 |

[ADR 0026](adr/0026-scm-merge-receipt-and-publication-reconcile.md)（已接受，2026-08-12；维护者合入 PR #49）冻结已合并 PR 的权威 reconcile 契约（`SCMMergeReceipt` 与 `PublicationReconcileRecord`）；accept-after-merge typed reconciliation 实现（accept 内联 MERGED 识别 + `marshal task reconcile` 补偿命令 + lifecycle 唯一终态例外）已于 2026-08-13 合入，关闭 Issue #25 与审计 finding `PUBLICATION-MERGED-HEAD-RECONCILE-P1`；该实现是 Local MVP 发布流程修复，不升级 M8–M13 实现状态。

[ADR 0017](adr/0017-provider-neutral-sandbox-contract.md)（已接受，2026-08-10；全部 P1 经 Round 2 独立验证与 ReviewDecision accept 后由维护者接受）基于首次 Sandbox SPI dogfood 的 reject 证据冻结 provider-neutral Sandbox 安全契约，并修订 M8–M13 分工：

- 二维权限/隔离模型 `AccessMode × AssuranceLevel`（含旧 `executionProfile` 兼容映射、拒绝/降级规则与持久记录迁移）；
- `hardened` 必须绑定密封 `ConformanceEvidence`，证据拓扑冻结：probe 定义/challenge/nonce、artifact digest、调度、out-of-band 观察、裁决与签发由 Control Plane 与独立 Conformance Verifier 控制，probe workload 作为敌对测试负载运行在被测 Provider 创建、身份精确绑定的 target allocation 内，Provider 的 completed/receipt 只是输入、不能自签通过；Local 普通宿主进程永不 hardened，Cloudflare 无豁免；
- Stage 内容寻址（inline 小对象/ArtifactStore locator、大小上限、消费前后重算 sha256、禁止回显声明 digest）；
- workloadRole 与认证 principal 拆分：Sandbox workloadRole 封闭枚举仅 `worker`/`verifier`，control-plane/publisher/operator/API caller 是不同语义 Port 上受 AuthZ 约束的 principal，Publisher 永不成为 Sandbox workload；完整身份 fencing（task/run/attempt/workloadRole/allocation/generation/fencingToken 元组，远程请求另绑定 principal/portKind/providerType/audience/scope——该 universal 身份口径已被 ADR 0018 §3 按 Port 分流取代；普通 replay 先过当前 lease fencing；Restore lost-response reconciliation 独立路径）；
- 无双写 Restore（默认 replacement allocation，控制面 CAS 激活新 generation）；
- DispatchLease 唯一状态机：Push/Pull 只冻结 outcome/invariant equivalence（唯一 claim、eligibility、fencing、deadline/expiry/cancel/reconcile/generation bump 与晚到隔离不变量），允许拓扑特定（topology-specific）的 offer/poll/claim/ack transition 与 timing——ADR 0017 §7 的逐步 wire 等价措辞已被 ADR 0018 §16 取代，conformance 比较 normalized business trace 与业务不变量，不比较逐步 wire trace；M9 交付两拓扑 outcome/invariant equivalence conformance 与故障注入口径；
- DurableExecutionEngine 唯一 Port 名：Temporal/Local Engine 仅是 backend，Attempt 创建/retry 预算/rework/终态裁决只在 Core，delivery/activity retry 不创建 Attempt、不消费业务预算；
- M9 wire contract 首版冻结：versioned HTTP/JSON + OpenAPI（Task create/get/cancel、Run approval/status、events/evidence），SSE `eventId`/cursor 断线续传 + 轮询 fallback，WebSocket/gRPC 推迟；Provider remote transport 同为 versioned HTTP/JSON（Push 调 Provider endpoint，Pull outbound-only）；M9 提供最小 scope-bound 可撤销注册身份，M11 扩展生产远程入口与多用户 AuthN/AuthZ，M12 基于该 wire contract 交付多语言 SDK 与部署文档；版本化 Provider Protocol 认证注册与观测边界；C/S + Control Plane/Execution Plane 分离并保留 embedded/local 模式；CLI/Web/GitHub App/CI 均为 Public API client，embedded CLI 经 in-process adapter 调同一 Public application Port、不直写 store；
- 分工修订：**M8 的纵切是 embedded/local 纵切**；`marshal-server` 与 Push/Pull Public API 属于 M9；M10 接 Cloudflare remote transport；M11 HA/AuthN/AuthZ；M12 多语言 SDK、部署文档与多拓扑 conformance；M13 实现 Goal API/控制器。

[ADR 0018](adr/0018-control-plane-and-provider-ports.md)（已接受，2026-08-11；本任务全部 Gate、独立 ReviewDecision accept 且无 P0/P1（含 Round 4 独立评审八项 P1、Round 5 复核四项残留与 Round 6 复核两项残留——Control Plane authority namespace 与 Provider actor 域分离、typed cross-domain edge——、Round 7 复核三项残留与 Round 8 复核一项残留——typed edge 跨域例外与适用范围——、Round 9 复核两项残留——跨域 fail closed 表述精确化、非 edge Port 与同域不自动授权——全部关闭）后由维护者接受）冻结 Marshal C/S Control Plane、按信任域分隔的 Provider Port、耐久注册/能力快照与在途 lease 撤销，澄清/部分取代 ADR 0017 §4/§6/§7/§8/§10/§12：

- C/S 终态：Marshal Control Plane 运行于常驻 `marshal-server`，Execution Plane 分离可远程；Core 是唯一业务权威；Provider 与 DurableExecutionEngine backend 只提供输入/传输，不能宣布 approved、ReviewDecision 或 safe-to-publish；
- 六类 Provider（Agent、Sandbox、Verification workload executor、SCM/Publisher transport、Artifact、Secret）至少分三个信任域（trust domain）：低权限 Execution、独立高权限 Publication（Publisher 永不成为 Sandbox workload）、Data/Capability；域之间不共享 credential、AuthZ、审计或 conformance profile；六类 Provider 彼此是不同 Port、不同 protocol family，不共享 conformance suite；
- Provider 不必远程：对每个具体 Port/protocol family，embedded/in-process、Push HTTP、Pull outbound runner 才是该族的 transport adapter，运行该族统一的 conformance suite；
- 身份按 Port 冻结、不设 universal envelope：required/forbidden 矩阵覆盖 public-api、provider-registration/control、dispatch-bound Sandbox/Agent/Verification、publication、artifact、secret；public-api 禁止 providerType 并拒绝 workload lease（workloadRole/allocationId/generation/fencingToken/DispatchLease）；provider-registration/control 同样拒绝 workload lease；只有 dispatch-bound Port 绑定完整 lease 身份；publication 绑定 SideEffectIntent/ReviewDecision/evidence digest；artifact/secret 绑定 scoped handle/content digest/scope/expiry；fencingToken 是非凭据 stale-write guard，credential 不入业务 JSON/事件/日志/digest；
- append-only event ledger 是唯一权威，snapshot/queue/SSE/registry/索引是可重建投影；SSE cursor 过期、gap 或压缩返回可判定 resync；DurableExecutionEngine 是 Core 的内部 Port；
- Runtime 持久化 ProviderRegistration 与不可变 ProviderCapabilitySnapshot；ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence 也是 authority ledger 事实，由 authorityNamespaceId 拥有、只允许 Core 写入，仅携带 actor securityDomainId、provenance 与 eligibility；registrationId 幂等身份 canonical 绑定 `(securityDomainId, principal, providerType, providerName, providerVersion, protocolVersion, scope)`（securityDomainId 为所携带的 actor 身份）与 `idempotencyKey`/`requestDigest`，仅 actor 域与全部字段相同才归并；同 key 不同 digest conflict；跨 scope/protocol conflict 或新 registrationId，不改旧记录；revoked/expired 不因普通 replay 复活；create/status/revoke/expire 与 capture/supersede 都是 authority ledger 事实，三类 expiry 独立；禁止 memory-only registration；legacy v1alpha1 CapabilitySnapshot 字节/digest 不变，只经显式版本化 fail-closed mapper 转换并记录 sourceCapabilitySnapshotDigest，不得默认补 scope/evidence；
- DispatchLease 只消费持久 ProviderCapabilitySnapshot，双绑定权威侧 authorityNamespaceId 与 actor 侧 securityDomainId，并绑定 registrationId、claim 时 active status/version、providerCapabilitySnapshotDigest 与 conformanceEvidenceDigests 封闭集合；lease 引用/digest 永不改写只供审计；每次 heartbeat、结果/副作用接纳与恢复 reconcile 按当前 ledger 重判资格（eligible）；registration revoke/expire/incompatible、snapshot expire/supersede、evidence revoke/expire 使 active lease（在途 lease）立即失去资格（cancel/expiry + generation bump/fencing，Allocation/Attempt 终止对账，晚到结果隔离）；继续执行只能新 Attempt + 新 lease 重新 match；
- M8 实施顺序硬门禁：negative fixtures/event contract → ProviderRegistration/ProviderCapabilitySnapshot Schema → legacy mapper → durable embedded registration + ledger recovery → snapshot/evidence validation → 最后 enable DispatchLease match；前置缺失 claim/match fail closed；fixture 覆盖跨 scope/protocol、same key/different digest、revoked replay、restart/rebuild、substitution、各类 claim 后失效的 Push/Pull，以及 typed cross-domain edge：伪造签发者（issuer）或冒用签发身份、错 authority scope、错 source/target（issuer/sourceActor/targetActor 与 edge 类型不符或 target substitution）、错 operation、错 attempt/allocation、已过期、已撤销、digest 替换的 edge 必须拒绝；绕过 current-ledger recheck 使用派生 token/handle、转授或扩权尝试、跨 Port token/schema 复用的请求必须失败；携带 raw handle/credential 或跨域复用 raw credential/ConformanceEvidence 的跨域请求必须失败；Provider actor 未经对应 typed edge 授权或绑定不精确匹配的跨域访问必须失败；Public API/SSE 客户端与 Core 内部权威对象引用不持有 Provider typed edge 的合法访问 fixture 必须通过；合法三条 typed cross-domain edge 可恢复且幂等，fixture 明确区分三类合法 positive 与无 edge、错 edge、错绑定 negative；provider-registration/control 经 transport identity、该 Port AuthN/AuthZ、scope/protocol validation 与 registration protocol 校验、由 Core 将获准事实写入 authority ledger 的合法请求必须通过，跨 Port 复用 transport identity/token 或以 securityDomainId 相同为由跳过 principal/registrationId/providerInstanceId/scope/attempt/allocation/operation 门禁的同域 bearer 化请求必须失败；Public API 幂等身份、SSE cursor 与 Artifact/Evidence/Checkpoint/Candidate 对象 key 使用 authorityNamespaceId，将其归属 actor securityDomainId 的 fixture 必须失败；
- M9 冻结 marshal-server/Public API/Sandbox dispatch Push-Pull、远程注册、SSE 与 DurableExecutionEngine；M12 扩展其余 Provider wire/SDK；HTTP/JSON+OpenAPI 首发，WebSocket/gRPC 推迟；
- securityDomainId 复合安全域键空间现在冻结（ADR 0018 §10）：复合三元组 `(tenantNamespace, trustDomainKind, isolationDomainId)`——tenantNamespace 单租户部署可固定 `default`（tenant 只能作为该组成参与授权），trustDomainKind 封闭枚举 execution|publication|data-capability，isolationDomainId 标识同 kind 内隔离边界；不得使用全系统单一 default 同时宣称隔离 Execution/Publication/Data-Capability；actor 侧 securityDomainId 进入 registration/snapshot/evidence 携带项、lease/allocation actor 绑定、artifact/secret scoped handle、cache key 等引用字段的持久键空间（只用于 actor 身份、provenance 与授权判定），submission/run lifecycle、DispatchLease/Allocation、ReviewDecision、Outcome、SideEffectIntent/Receipt reconcile、Evidence graph、typed edge、SSE cursor/sequence、idempotency/replay 权威键、outbox、audit 与事件账本归权威侧 authorityNamespaceId 拥有；未经三条 Core-only typed edge 中对应 active edge 授权或绑定不精确匹配的跨域引用与跨 trustDomainKind 引用 fail closed（三条 typed edge 是默认拒绝规则的唯一 allowlist 例外）；不得等 M11 再迁移持久主键；
- Control Plane 权威与 Provider actor 分离（ADR 0018 §10）：authorityNamespaceId=(tenantNamespace, controlPlaneId, authorityScopeId) 拥有全部 Control Plane 权威对象——Project/Goal、TaskSubmission、Task/Run/Attempt lifecycle、DispatchLease/Allocation、ReviewDecision、Outcome、SideEffectIntent/Receipt reconcile、Evidence graph、typed edge、事件账本、发布决定、idempotency/outbox/audit 记录与 SSE 权威序列——只允许 Core 写入；controlPlaneId 是 HA/灾备中保持稳定的逻辑权威身份，不是进程实例；securityDomainId=(tenantNamespace, trustDomainKind, isolationDomainId) 只标识 Provider actor；authorityNamespaceId 不是 Provider 的 trustDomainKind 维度，Provider 不得写入或宣称权威对象；
- Core-only typed cross-domain edge（ADR 0018 §3）：三条 Core-only typed edge——DispatchResultCapability、MaterialAccessGrant、PublicationAuthorization——是 Provider actor 跨 trust domain 访问默认拒绝（default deny）规则的唯一 allowlist 例外，其余 Provider actor 跨 trust domain 访问默认拒绝；
- Core 是唯一签发者、唯一撤销者与唯一重新授权者，每条 edge 的 issuer 为 Core（issuer 不等于业务流的 sourceActor：DispatchResultCapability 的 sourceActor=Execution workload、targetAudience=Core result-ingress；MaterialAccessGrant 的 sourceActor=Data/Capability Provider、targetActor=Execution workload；PublicationAuthorization 的 issuer/sourceAuthority=Core、targetActor=Publication Provider）；
- 每条 edge 是由 authorityNamespaceId 拥有的 authority-scope-bound 权威记录，冻结 issuer/source/target/operation/expiry/digest/revocation/replay/current-ledger recheck 七项生命周期要素与各自专属绑定，授权不可转授、不可扩权，每次使用必须按当前 authority ledger 复核；
- edge 只承载 scoped handle、digest 引用与授权引用，不承载 raw credential/raw secret handle，不替代 ConformanceEvidence；
- edge 派生的 token/handle 只是指向 edge 权威记录的单向引用，派生 token/handle 不得成为第二权威；
- Public API/SSE 是 Client 到 Control Plane 的入口，使用各自的 AuthN/AuthZ、scope 约束与 re-AuthZ，不需要 Provider typed edge；Core 内部权威对象引用（ledger 事件间引用、cursor、证据关系、outbox/ledger 引用）保留在 authority ledger，不需要 Provider typed edge；
- Provider actor 跨 trustDomainKind 访问必须持有 active typed edge，并精确匹配该 edge 绑定的 source/target securityDomainId 与全部对象、operation、时效绑定，未经对应 edge 授权或任一绑定不符一律 fail closed 并写入审计；
- provider-registration/control 与 public-api 不持有三类业务 typed edge，经 transport identity、该 Port 的 AuthN/AuthZ、scope/protocol validation 与 registration protocol 校验，由 Core 将获准事实写入 authority ledger；
- securityDomainId 相同只是 actor provenance/partition 条件，不构成授权、不构成同域 bearer grant，同域请求仍须逐项匹配具体 Port 的 principal/registrationId/providerInstanceId/scope/attempt/allocation/generation/operation 门禁；
- Provider attestation 全链绑定（ADR 0018 §11）：ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence/lease claim 全链绑定 securityDomainId、稳定 providerInstanceId、effective configDigest 与签发/验证 trust root（含 key id/rotation）；任一变化产生新 immutable 快照/证据并触发 eligibility 重判；相同软件版本换实例/配置/签发密钥不得复用 hardened evidence；Worker/Verifier 必须不同 principal 与不同 allocation；高保证策略可要求 provider/host/failure-domain diversity；
- 远程 transport 安全基线（ADR 0018 §12）：任何非 loopback/in-process transport 自首次 enable 起强制 TLS；workload-to-workload 优先 mTLS 或等价不可转移 workload identity；双向校验 server/provider 身份与 audience/scope；短期 credential rotation/revocation 与 replay protection；M9/M10/M12 首次 enable 即生效，M11 只扩展 HA/多节点/多用户授权策略，不补首次安全基线；
- 原子 fencing 写入汇（ADR 0018 §13）：ledger sink 同事务 atomic compare-and-append/transaction，ledger transition、当前 lease generation 与 Evidence/Artifact 引用同原子校验；Artifact/Evidence/Checkpoint/Candidate bytes 的接纳关系归 authority ledger，使用 authorityNamespaceId+run+attempt+allocation+generation scoped immutable key 与 digest-verified put-if-absent（actor securityDomainId 只作为 provenance 记录）；陈旧/冲突 bytes 只进 quarantine namespace，永不覆盖或进入当前 evidence graph；M8/M9 补 lost-response、concurrent-write、old-generation overwrite fixture；
- SSE 恢复与再授权（ADR 0018 §14）：cursor 身份 authorityNamespaceId+scope+ledgerSequence（权威账本的权威侧身份），订阅方另绑定自身 securityDomainId 完成授权判定，scope 内单调 sequence，at-least-once + eventId/sequence 去重，expiry/gap/compaction 返回 deterministic resync 起点与 snapshot digest，heartbeat 与有界 backpressure，周期性 re-Authorization 与敏感变更即时 re-Authorization；SSE 是只读投影，不承载 ACK、lease heartbeat 或 command；参数值留 M9 Schema 冻结；
- DurableExecutionEngine 单一权威 seam（ADR 0018 §15）：同事务 outbox 或 ledger-derived Core command journal 二选一；commandId 从权威事实稳定派生；backend 只消费/回报，workflow/activity state 不是业务权威；M9 backend profile 与升级 fixture 覆盖 workflow versioning/build ID、Continue-As-New、payload 外置/上限、activity heartbeat/cancel/retry；
- 按 Port 的 versioned protocol family（ADR 0018 §16）：每个具体 Port 独立 audience、AuthZ scope、request/response schema、error/idempotency/revocation 与 conformance profile；只共享 transport、JCS 与最小 base auth primitives；禁止跨 Port token/schema/operation；六类 Provider 分属不同 protocol family，不共享 conformance suite；embedded/Push/Pull 只是同一 Port 内的 adapter，运行该族统一的 conformance suite；
- Push/Pull 只冻结 outcome/invariant equivalence（唯一 claim、eligibility、fencing、deadline、无双活、晚到隔离），允许拓扑特定（topology-specific）的 offer/poll/claim/ack transition 与 timing，conformance 比较 normalized business trace 与业务不变量，不比较逐步 wire trace（ADR 0018 §16）；
- 不兼容与撤销分级处置（ADR 0018 §6/§7）：security-critical revoke（credential compromise、protocol violation）立即 cancel + generation bump + kill，不留 drain 窗口；planned/ordinary incompatible upgrade 使用新 registration/新 snapshot，旧实例 stop-new + bounded drain，drain deadline 到期再 fence；事件机器可读原因码与审计记录分开；普通升级不得复活旧注册或改写旧 lease digest。

ADR 0017 中以下历史 universal 口径就地标注**已被 ADR 0018 取代**，不再作为实施依据：所有远程副作用统一绑定完整 Task/Run/Attempt/allocation/lease 身份（现仅 dispatch-bound Port）；所有 Attempt/Artifact 接纳统一 fencing（现按 Port 分流）；所有远程请求统一额外绑定 providerType（现 public-api 禁止 providerType）；六类 Provider 注册产生 legacy CapabilitySnapshot（现为 ProviderRegistration + 不可变 ProviderCapabilitySnapshot，legacy 快照仅经 fail-closed mapper 转换）。

实现状态不因文档冻结或 ADR 接受而提前升级：ADR 0017 的接受只关闭设计歧义、ADR 0018 的接受只冻结设计，均只冻结设计不升级 M8–M13 实现/conformance 状态；上表各 Milestone 状态为：M7 于 2026-08-11 通过退出门禁后更新为已通过（只表示设计与契约阶段通过，不表示后续实现或 conformance 完成），M8 按修订后的契约（含 ADR 0018 §7 顺序硬门禁）以新任务启动，六个硬门禁 gate 全部合入 main 且各 PR 远端 CI 全绿后于 2026-08-13 通过退出门禁，更新为 `PASSED`（各 gate 尚未整体接入最终 Runtime 执行路径），M9 七交付（a lease 持久账本 PR #104、b typed edge 运行时接线 PR #107、c1 marshal-server 常驻 + Public API PR #111、c2 SSE 只读投影 PR #115、c3 远程注册 + TLS 基线 PR #116、e DurableExecutionEngine seam PR #119、d Push/Pull 双拓扑 transport + conformance PR #120）全部合入 main 且各 PR 远端 CI 全绿后于 2026-08-14 通过退出门禁，更新为 `PASSED`（只表示设计与契约+本 milestone 交付门禁通过，不表示 M10–M13 实现状态变化，不表示 conformance 终态），M10–M13 保持 `PLANNED`；首次 Sandbox SPI dogfood Run 的既有实现成果按**未接纳探索证据**对待，不计为 M8 实现进度。

ADR 0033（Proposed）只冻结受控 merge 的 journal-bound authority/delivery 目标与负向恢复矩阵。提出、接受、Schema 落盘或任一前置实现切片合入都不构成受控 merge supported 声明，也不改变 M10 在途及 M11–M13 `PLANNED` 状态；A–D 全部实现、独立审计 P0/P1 清零且 required CI/secret scan 与恢复 conformance 全绿后，最多登记显式 opt-in 的 `local-nonproduction` 受限 profile。production supported 仍须等待 M11 external rollback witness、跨节点 fenced lease 与协调回滚恢复演练通过。

每个 Milestone 都执行范围冻结、实现、单元/集成/E2E 测试、独立审计、提交推送和远端 CI 绿色验收。任何 P0/P1 审计问题或 CI 失败都会阻止进入下一阶段。M7–M13 还要求每个 Milestone 先通过 Local MVP 全量回归。M7 只冻结 Project/Goal 的存在性、authority ownership 与多 Run 原则；M13 才实现 ADR 0019 的完整 Goal 控制器，M7–M12 完成声明不涵盖复杂需求目标。
