# 设计审计报告

- 审计日期：2026-08-04（2026-08-10 增补 Runtime 架构重置记录、首次 Sandbox SPI dogfood reject 增补记录与 Round 2 关闭记录；2026-08-11 增补 Control Plane 与 Provider Port 边界冻结记录，含 Round 4 独立评审八项 P1 关闭记录、Round 5 复核四项残留关闭记录、Round 6 复核两项残留——Control Plane authority namespace 与 Provider actor 域分离、typed cross-domain edge——关闭记录与 Round 7 复核三项残留——双键空间残留清除（权威对象 authorityNamespaceId 独占拥有、registration/snapshot/evidence authority ledger 事实、接纳关系归 authority ledger、controlPlaneId 逻辑权威身份）、Core-only typed edge 生命周期细化（issuer/source/target/operation/expiry/digest/revocation/replay/current-ledger recheck，issuer 恒为 Core 且不等于业务流 sourceActor、sourceActor/targetActor 按 edge 类型绑定，派生 token/handle 不得成为第二权威）、Public API 幂等/SSE/对象 key 修正为 authorityNamespaceId——关闭记录与 Round 8 复核一项残留——typed edge 跨域例外与适用范围（三类 typed edge 明确为 Provider actor 跨信任域访问默认拒绝的唯一 allowlist 例外，Public API/SSE 与 Core 内部权威引用无需 Provider typed edge）——与 Round 9 复核两项残留——跨域 fail closed 表述精确化（删除会无条件拒绝 MaterialAccessGrant 等合法 typed edge 的宽泛表述）、非 edge Port 与同域不自动授权（provider-registration/control 经 transport identity/该 Port AuthN/AuthZ/registration protocol 由 Core 写 authority ledger；securityDomainId 相同只是 provenance/partition 条件，不构成授权）——关闭记录；2026-08-12 增补 Issue #25 发布合并后 head reconcile 审计记录；2026-08-13 该 finding 随 typed reconciliation 实现合入关闭；2026-08-14 增补 Issue #53 CI 失败 rework 注入设计缺口审计记录，目标契约由 [ADR 0030](adr/0030-ci-failure-rework-evidence-and-injection.md)（Proposed，草案已提出/待接受）给出（接受后方冻结），实现待后续 implementation successor）
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

上述 finding 是 Issue #136/#137 production enablement 的共同前置阻塞，不改变 M10 在途及 M11–M13 `PLANNED` 状态。只有 shared Port conformance 与对应 profile conformance、当前宿主 doctor、撤销/rollback/kill 演练、required CI 和 secret scan 全绿后，才能分别提交 Qoder 或 Codex 的独立 registry enablement 变更。

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

## Issue #25 发布合并后 head reconcile 审计增补（2026-08-12）

公开 [Issue #25](https://github.com/chiga0/marshal-harness/issues/25) 与 [PR #24](https://github.com/chiga0/marshal-harness/pull/24) 暴露：当全部 required checks 成功且 PR 已合并进入 main 后，现有 `marshal task accept` 仍要求 PR 处于 OPEN/Draft，会把 Run 永久置为 terminal `BLOCKED`。head branch 删除不是权威 head SHA 丢失——GitHub PR 节点在 merge 后仍保留原 head OID、base OID 与 merge commit。本节记录该问题的审计定位，区分 implementation bug 与 contract gap。

| ID | 级别 | 状态 | 关联 | 问题 | 定位 |
| --- | --- | --- | --- | --- | --- |
| `PUBLICATION-MERGED-HEAD-RECONCILE-P1` | P1 | `CLOSED` | [Issue #25](https://github.com/chiga0/marshal-harness/issues/25) / [PR #24](https://github.com/chiga0/marshal-harness/pull/24) | 已合并 PR 的 head 与 merge commit 缺乏权威 reconcile 路径，`accept` 无法识别 merge 完成态，Run 被永久置为 `BLOCKED` | 分两层：**nonterminal implementation bug**——`accept` 以 PR OPEN/Draft 为前置条件，未区分“merge 前需 OPEN”与“merge 后已合并”两种语义，是可修复的实现缺陷；**terminal contract gap**——当前 Schema/命令缺少不可变 `SCMMergeReceipt` 与 append-only `PublicationReconcileRecord`，无法把已合并 Run 从 `BLOCKED` 安全迁移到 `ACCEPTED`，属契约级缺口，需新 ADR 定义 |

正式处置见 [Operator Runbook §7](operator-runbook.md)。处置进展（2026-08-12）：ADR 0026 已接受并合入 main（PR #49），冻结 `SCMMergeReceipt` 与 `PublicationReconcileRecord` 契约，契约层关闭。处置进展（2026-08-13）：实现层已合入（[PR #106](https://github.com/chiga0/marshal-harness/pull/106)）——`marshal task accept` 活路径内联识别已合并且 required checks 全绿的 PR 并采集不可变 `SCMMergeReceipt`；补偿命令 `marshal task reconcile` 以 ADR 0026 冻结的 `SCMMergeReceipt` + append-only `PublicationReconcileRecord` 与 current-ledger recheck 共同门禁，把发布后的 terminal `BLOCKED` 安全迁移 `ACCEPTED`（`publication.reconciled` 事件，lifecycle 唯一命名终态例外），全程 append-only、幂等、fail closed，不绕过 required checks 与 ReviewDecision。契约层与实现层均已关闭，本 finding 状态更新为 `CLOSED`；不改变 Local MVP `APPROVED_FOR_IMPLEMENTATION` / `USABLE` 结论，也不改变任何已冻结的信任边界、持久化契约或发布权限（merge-never 不变）；`doctor --repair` 依旧只修复 snapshot 损坏，不能改变业务终态。
