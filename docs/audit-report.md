# 设计审计报告

- 审计日期：2026-08-03
- 范围：当前文档与 `v1alpha1` Schema 描述的 Local CLI MVP
- 结论：**`APPROVED_FOR_IMPLEMENTATION`**
- 未关闭 Blocking Finding：无
- 门禁状态：维护者已于 2026-08-03 接受 ADR 0001–0005 与 Local MVP Scope

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
- ADR 0001–0005。

## 自动检查

| 检查 | 结果 |
| --- | --- |
| JSON Parsing | 11 份 Schema 与 11 份 Happy-path Record 通过 |
| Draft 2020-12 Metaschema | 11 份 Schema 通过 `check-jsonschema` |
| Happy-path Validation | 11 份 Record 全部通过对应 Schema |
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

没有未解决的 P0、P1 或 P2 架构问题。

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

## ADR 建议

以下 ADR 共同构成当前 Local MVP 的架构与安全基线，建议一起接受：

1. [ADR 0001：CLI-first 模块化单体](adr/0001-cli-first-modular-monolith.md)
2. [ADR 0002：每个任务一个 Worktree](adr/0002-worktree-isolation.md)
3. [ADR 0003：Worker 与 Publisher 分权](adr/0003-separate-worker-and-publisher.md)
4. [ADR 0004：独立验证](adr/0004-independent-verification.md)
5. [ADR 0005：Go 作为 Core Runtime](adr/0005-go-runtime.md)

删除 ADR 0002–0004 中任何一个都会使本批准失效，并要求重新进行安全与生命周期审计。

## 实施门禁

文档审计和维护者接受均已完成。实施必须：

1. 从 Milestone 0 开始，不提前执行 Worker 或 Publication Side Effect；
2. 每个 Milestone 满足 Exit Criteria 后才能进入下一阶段。

因此最终结论为 **`APPROVED_FOR_IMPLEMENTATION`**，实施门禁已开启。
