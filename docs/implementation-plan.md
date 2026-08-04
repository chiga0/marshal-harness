# 实施计划

## 门禁

本文只是实施计划，不代表已授权实施。只有维护者接受 ADR 与审计结论后，才开始运行时代码开发。

当前状态：维护者已授权实施；Milestone 0–2 已通过，Milestone 3 已完成本地实现与独立审计，等待提交、推送及远端 CI；Milestone 4 尚未开始。验收证据见 [Roadmap 状态](roadmap-status.md)。

## 交付策略

在接入真实 Coding Agent 前，先用确定性 Fake Adapter 打通最小端到端链路。每个 Milestone 都必须保持仓库自身检查通过，并不得提前加入后续阶段的外部副作用。

## Milestone 0：Toolchain 与 Contract

交付：

- 锁定具体 Go 版本；
- 提交 `go.mod` 与 `go.sum`；
- 建立清晰的 Core、Port、Adapter 与 CLI Package 边界；
- `gofmt`、`go vet`、静态检查、Test 与 Build Command；
- Schema Compile 与正反 Contract Fixture；
- 生成或受契约测试保护的 Go 内部类型；
- 不执行 Worker/Publication 副作用的 CLI Skeleton。

退出条件：

- 所有 Schema 通过 Draft 2020-12 校验；
- Fixture 覆盖 Format 与 Semantic Validator；
- Clean Module Download、Format、Vet、Lint、Unit Test 与 Build 通过；
- 产出可在受支持平台运行的单一 `marshal` 可执行文件；
- Core Domain 不依赖 Provider-specific Module。

## Milestone 1：State Machine 与 Run Store

交付：

- Task/Run/Attempt ID；
- 带守卫的 Lifecycle Reducer；
- 带 Sequence 的 Append-only Event Journal；
- Atomic State Snapshot；
- File Lock 与 Lease；
- Canonical JSON 与 Digest Utility；
- 仓库本地 `.marshal/` 初始化、默认 Git 排除规则与显式 `MARSHAL_STATE_DIR` 覆盖；
- `marshal task status` 与只读 Inspection；
- 使用 Transcript Fixture 的 Fake Adapter。

退出条件：

- Table-driven Test 覆盖全部合法和非法 Transition；
- Stale Sequence Write 与 Duplicate Event 默认失败；
- Crash Fixture 能从截断 Journal 重建 State；
- Frozen Input 变化创建新 Run，而不是修改 `READY`；
- `.marshal/` 内容不会出现在业务 Diff 或 Commit Tree 中。

## Milestone 2：Git Worktree 与独立 Verification

交付：

- Repository Identity 与 base SHA Resolution；
- 在 `.marshal/worktrees/<task-id>/` 创建带 Repository/Task Lock 的 linked Worktree/Branch；
- 包含 Untracked File 与 Rename 的真实 Diff；
- Path、Glob、Symlink 与 Submodule Gate；
- 有 Time/Output Limit 的 Direct-spawn Verification Command；
- Artifact Collection 与 Hash；
- VerificationReport 与 ArtifactManifest。

退出条件：

- Fixture Run 不修改主 Checkout；
- macOS 与 Linux Fixture 验证嵌套 linked worktree 的创建、发现、锁定和清理，并覆盖 macOS `/var` 与 `/private/var` 路径别名；不支持时必须安全 Block 或使用显式状态目录覆盖；
- Path Traversal、Symlink Escape、Forbidden Rename 与 Oversized Diff 失败；
- Worker Declaration 不能改变真实 Gate；
- Cancellation 终止 Verification Process Tree；
- Verifier 产生的 Dirty Output 被发现。

## Milestone 3：Review 与 Rework Loop

交付：

- 带版本 Worker Prompt Renderer；
- Bounded ReviewPacket Export；
- Schema-valid ReviewDecision Import；
- Stale Evidence Rejection；
- Rework 与 Operational Retry Budget；
- No-change 与其他终态；
- Outcome Bundle 与人类可读摘要。
- 可安装到 `.agents/skills/marshal/` 的轻量 Codex Skill 与中文使用文档。

首个 Review Bridge 可以是 File-based：Marshal 输出 Packet，Codex CLI 或 Codex Desktop 生成并交回 Decision。后续 Native Codex Integration 必须保留相同文件契约。

退出条件：

- Accept 不能绕过 Required Failed Gate；
- Stale Decision 与 Invalid Prose 不改变 State；
- Finding 跨 Rework 保留，并只能由新 Evidence 关闭；
- Budget Exhaustion 产生正确终态。
- Skill 的显式与隐式触发 Fixture 能生成合法 TaskSpec/ReviewDecision，且不会绕过 Marshal Core 直接启动 Worker 或发布。

## Milestone 4：首个真实 Worker Adapter

选择首个 Adapter 前，对已安装 Qwen、OpenCode 与 Pi 做有界 Spike，仅评估 Structured Output、Non-interactive Edit、Cancellation、Session Identity、Permission Enforcement 与 Transcript Stability，不根据主观 Coding Quality 选择。

交付一个 Adapter 及：

- 精确 Executable Resolution 与 CapabilitySnapshot；
- Argv Spawn 与过滤环境；
- Normalized Event；
- Output Limit 与 Cancellation；
- WorkerResult Parsing；
- Adapter Conformance Fixture。

退出条件：

- Fake 与 Real Adapter 通过相同 Lifecycle Fixture；
- Unsupported Capability 在 Worker 启动前失败；
- 不回退到同名或近似 Executable；
- Worker Environment Test 中没有 Publisher Credential。

## Milestone 5：GitHub Draft Publisher

交付：

- Accepted Snapshot Recheck；
- 不使用 Ambient User Hook 的 Controlled Commit；
- Branch Derivation 与 Collision Check；
- 使用独立 Credential Profile 的 GitHub Publisher；
- 幂等创建/更新单个 Draft PR；
- Publication Artifact 与 Remote CI Observation；
- Ambiguous Timeout Reconciliation。

退出条件：

- Review 后改变 Snapshot 就不能发布；
- 模拟 Push/PR Timeout 后重试不会重复创建；
- Unexpected Remote Head 导致 Block，而不是 Force Push；
- Worker 从未获得 GitHub Credential；
- Merge 功能不可用。

## Milestone 6：其余 Adapter 与 Recovery 加固

交付：

- 其余 Qwen/OpenCode/Pi Adapter；
- 受支持的 Session Resume；
- `marshal doctor` Reconciliation；
- Archive 与 Cleanup Preview；
- 来自 Conformance Test 的 Compatibility Matrix；
- Operator Documentation。

退出条件：

- 全部受支持 Adapter 通过共享 Conformance Test；
- 文档中的 Crash Point 能幂等恢复或安全 Block；
- Dirty Worktree 未归档且未显式授权时不能删除；
- Unknown Worker Version 被拒绝或清楚标记 Experimental。

## 延后阶段

- Hardened Container/VM Profile；
- GitLab Publisher；
- CI Webhook Receiver；
- Daemon、MCP、ACP 或专用 Desktop/Remote Service Facade；
- 有性能证据后再增加 SQLite/Index；
- Telemetry、Cost Accounting 与 Policy-based Routing；
- Web UI 与 Distributed Scheduling；
- 任何 Automatic Merge Policy。

## MVP 可用定义

至少一个真实 Worker 必须从 Frozen TaskSpec 出发，经过独立 Verification、Codex Review、有界 Rework、GitHub Draft PR 与 Outcome Export，完整完成 Fixture Task；同时 `security-model.md` 中针对 Local Profile 的安全验收测试全部通过。
