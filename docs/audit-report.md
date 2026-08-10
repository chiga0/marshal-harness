# 设计审计报告

- 审计日期：2026-08-04（2026-08-10 增补 Runtime 架构重置记录、首次 Sandbox SPI dogfood reject 增补记录与 Round 2 关闭记录）
- 范围：当前文档与 `v1alpha1` Schema 描述的 Local CLI MVP（Runtime/Sandbox 契约部分为分层状态，见下）
- 结论（分层）：
  - Local MVP（Milestone 0–6）：**`APPROVED_FOR_IMPLEMENTATION`** / `USABLE`，行为与门禁不变；
  - Runtime/Sandbox 契约（M7–M13）：在 [ADR 0017](adr/0017-provider-neutral-sandbox-contract.md) 接受前为 **`BLOCKED`**（首次 Sandbox SPI dogfood reject 与 Round 2 评审暴露的合同级歧义）；2026-08-10 全部 P1 经 Round 2 独立验证与 ReviewDecision accept，维护者接受 ADR 0017，设计歧义关闭。
- 未关闭 Blocking Finding：无（S-A1–S-A7 与 Round 2 六项歧义的关闭记录见下文增补节）
- 门禁状态（分层）：
  - 维护者已接受 ADR 0001–0011、ADR 0012–0014 与 ADR 0016（2026-08-10，含 M7–M12 路线）及 Local MVP Scope；
  - ADR 0017（provider-neutral Sandbox 安全契约）已接受（2026-08-10）；**接受只关闭设计歧义**：不得把 M8 实现或 conformance 状态提前标为完成，M7 保持 `IN_PROGRESS`、M8–M13 保持 `PLANNED`，首次 Sandbox SPI dogfood 的既有实现成果按未接纳探索证据对待（见 [Roadmap 状态](roadmap-status.md)）

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

Schema 只承担结构校验。[`schemas/README.md`](../schemas/README.md) 中列出的 Semantic Validator 是 Milestone 0 强制要求。

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

没有未解决的 P0、P1 或 P2 架构问题。

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

## 实施门禁（分层）

文档审计和维护者接受均已完成。实施必须：

1. 从 Milestone 0 开始，不提前执行 Worker 或 Publication Side Effect；
2. 每个 Milestone 满足 Exit Criteria 后才能进入下一阶段。

分层结论：

- **Local MVP（Milestone 0–6）**：**`APPROVED_FOR_IMPLEMENTATION`** / `USABLE`，该范围实施门禁已开启且保持不变；
- **Runtime/Sandbox 契约（M7–M13）**：ADR 0017 接受前为 `BLOCKED`；2026-08-10 接受后设计歧义关闭，实现可按修订后的[实施计划](implementation-plan.md)推进，但任何 Milestone 的完成与 conformance 通过仍须以对应退出门禁与独立证据为准，不得因 ADR 接受而提前声明。

因此本报告不存在未关闭 Blocking Finding；`APPROVED_FOR_IMPLEMENTATION` 结论适用于 Local MVP 范围，Runtime/Sandbox 部分按上述分层状态执行。
