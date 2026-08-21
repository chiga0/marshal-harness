# Milestone 6 验收报告

- 验收日期：2026-08-05
- 范围：其余 Qwen/OpenCode/Pi Adapter、受控自治与人工介入、Reconciliation、CI Deadline、Archive/Cleanup Guard、原生 TUI 受监督 Pilot、Compatibility Matrix 与完整真实 MVP E2E
- 结论：`PASSED`；Local MVP 定义达成，标记 `USABLE`

## 已交付

- 三个真实 Worker Adapter 完成冻结验收：OpenCode `1.18.13`（自 M4 的 `1.18.12` 重新验收）、Qwen Code `0.21.5`、Pi `0.83.0`；`marshal doctor` Live Probe 全部 `supported`；
- 共享 Conformance Suite 与三个 Fake executable 通过同一 Lifecycle Fixture；三个真实 Agent 各自完成 captured Live E2E；
- `balanced` 受控自治：Plan/Publish Approval 绑定精确冻结证据；Marshal-mediated Steering 生成 append-only InterventionRecord；direct PTY 输入标记 mixed provenance；
- `marshal doctor --run` 只读 Reconciliation 与显式 `--repair`；CI_PENDING 按冻结 Run Deadline 分类并产出 Diagnostic；
- `marshal task cleanup` 默认 Preview、显式 `--apply`，拒绝 Active Lease、非终态 Run、未归档 dirty worktree 与路径身份异常；
- cmux `TerminalSession`：Probe 五态判定、3 秒有界 workspace 健康检查、Process Group Pause/Resume/Terminate、密封 `LaunchEnvelope`、Provider-neutral `TerminalLaunchSpec` 与 digest-bound `terminal.StartPrepared`；
- [Operator Runbook](operator-runbook.md) 与 [Compatibility Matrix](compatibility-matrix.md)。

## 受监督 cmux Pilot（2026-08-05）

cmux workspace RPC 曾出现 `workspace-rpc-unavailable`（capability 可用而 workspace actor 卡死），Probe 在 3 秒内 fail-closed；cmux 重启后恢复。恢复后的真实 Pilot：

- `TestLiveCMUXTerminalSession` helper E2E 通过（不调用模型）；
- 真实 Qwen Code `0.21.5` TUI 经 `terminal.StartPrepared` 与密封 launcher 在 cmux workspace `workspace:67` 启动，digest 与冻结 Probe 一致；
- 完成屏幕观察、任务产物（PILOT-MARKER.md）逐字节校验、Pause/Resume、InterruptStep 与 Terminate，workspace 干净关闭；证据位于本机 `.marshal/dev/cmux-supervised-pilot/evidence.json`；
- CompletionGate 为 `supervised-confirmation`；提示词提交为一次 manual-pty 介入（原因见下），Attempt 按混合 provenance 记录，未豁免任何门禁。

## 完整真实 MVP E2E

Run `m6-mvp-e2e-r3-20260805`（任务 `M6-MVP-E2E-R3`，基线 `9589b25f`）：

1. `task plan` 冻结 TaskSpec/Policy/CapabilitySnapshot，Plan Approval（actor `gawain`）绑定全部摘要；
2. 真实 OpenCode Worker（captured，`--pure --format json`，环境 allowlist）完成文档实现；
3. 独立 Verification：全部 7 个 Required Gate 通过，包括真实 `make check`；
4. 结构化 accept ReviewDecision 绑定 specDigest/reviewPacketDigest/verificationDigest/artifactManifestDigest/evidenceDigest；
5. Publish Approval 通过后受控 Commit、派生分支 `marshal/M6-MVP-E2E-R3-3d947d34f70c`、唯一 Marker、[Draft PR #2](https://github.com/chiga0/marshal-harness/pull/2) 幂等创建；
6. PR CI run `30974239712` 全绿（Quality ubuntu/macos、Secret scan 与 requiredChecks 精确匹配）；
7. `task accept` 验证 Required Check 与发布身份，导出 Outcome（`published head passed all required checks`），终态 `ACCEPTED`。

Worker 环境无 Publisher 凭据；凭据仅经独立 `MARSHAL_GH_CONFIG_DIR` 注入 publish/accept 子进程；未 merge、未触碰 Draft PR #1。

## E2E 中发现并修复的缺陷

两个缺陷都在真实 E2E 中暴露，并各自通过独立 Marshal Run（含独立 Verification 与 Review）修复后由维护者提交：

| 缺陷 | 修复 Run | 提交 | 说明 |
| --- | --- | --- | --- |
| publish Approval Gate 读取 legacy `review-decision.json` | `m6-approval-fix-r3-20260805`（ACCEPTED） | `4538f9f` | balanced Policy 发布审批恒失败；改为读取与 reviewRound 绑定的 `decisions/decision-%03d.json`，语义校验不变 |
| `loadEvidence`/`prepareOutcome`/`loadReviewFindings` 同类残留 | `m6-decision-paths-20260805`（ACCEPTED） | `9589b25` | 发布重校验、Outcome 与 Rework 三处读取同样绑定轮次文件 |

两次失败都以 `BLOCKED` fail-closed，未产生任何远端副作用，验证了"无法证明的状态不得发布"的不变量。

## 兼容性发现（进入 Compatibility Matrix 与 Runbook）

- Qwen TUI：`send` 多行文本后立即发送 Enter 会被粘贴处理吞掉；受监督操作需等待粘贴 settle 后单独发送 Enter（Pilot 实测约 10 秒）；
- OpenCode `run`：较大 TaskSpec context 会被写入 `$TMPDIR/opencode/work-context.txt` 并引导模型读取；Marshal 的 external-directory deny 正确阻断，模型改经 control/input 完成任务；首次 E2E Attempt（Run `m6-mvp-e2e-20260805`）因此被 permission-denied 分类 fail-closed，通过 TaskSpec 路径纪律约束消除；
- Pi：`message_update` 事件携带累积全量消息，转录本近似二次方增长（本机约 2 分钟达 16MB Output Limit）；captured Live E2E 在 4MB 预算内通过，大任务需提高 `maxOutputBytes` 或后续在 Adapter 层归一化；
- cmux：capability RPC 与 workspace RPC 可能分别卡死；Probe 必须以有界只读 `workspace list --json` 独立判定，不得自动重启 cmux。

## 验证证据

- 本地 `make ci`：Format、Vet、Staticcheck、全仓 Race Test、Build 全部通过（见提交 `9589b25` 与后续文档提交的主分支 CI）；
- 三 Adapter `marshal doctor` Probe：`supported` × 3（adapter 版本均为 `0.1.0`）；
- 受监督 Pilot 与 helper E2E 证据：本机 `.marshal/dev/cmux-supervised-pilot/`；
- Full MVP E2E 证据链：Run `m6-mvp-e2e-r3-20260805` 的 Journal、VerificationReport、ArtifactManifest、PublicationRecord、RemoteCheckRecord 与 Outcome；
- Draft PR #2 CI run `30974239712` `success`；Draft PR #1 保持未 merge。

## MVP 可用定义核对

至少一个真实 Worker（OpenCode）从 Frozen TaskSpec 出发，经过独立 Verification、Lead ReviewDecision、有界 Rework 预算（未触发）、真实 GitHub Draft PR 与 PR CI、Outcome Export，完整完成任务并 `ACCEPTED`；`security-model.md` 的 Local Profile 验收项（凭据隔离、路径门禁、陈旧证据拒绝、崩溃恢复、发布幂等）在 M1–M5 与本轮 E2E 中持续覆盖。Local MVP 判定 `USABLE`。

## 遗留（进入延后阶段）

- OpenCode 与 Pi 的原生 TUI Pilot（冻结 `TerminalLaunchSpec` 已就绪，未承诺本阶段执行）；
- Pi `message_update` 转录本归一化（若后续大任务证明需要）；
- iterm2/ghostty/terminal/tmux Observer Backend（接口已保留，未承诺实现）。

## 后续兼容性证据增补（2026-08-21）

本增补不重新打开 Milestone 6，也不改变其 `PASSED` 状态；它记录 M6 之后的 Mac-first Adapter 兼容性事实：

- Qoder CLI `1.1.27` 已通过当前 macOS ordinary-user registry/doctor 探测，但尚未完成该版本 fresh live Worker smoke、transcript attestation 与独立 conformance；不得把 doctor 结果写成 production authority。
- Codex `0.145.0` 的两个 Mac ordinary-user smoke 已由同一独立 reviewer 分别审查并进入 `ACCEPTED`。它们是诊断证据，不产生产品代码、Draft PR 或远端 merge。
- Qwen Code `0.21.11` 本地命令可执行，但当前 Marshal doctor 仍为 `unsupported/unprobed`，因此不计入新的 Adapter 晋升证据。

这些事实只更新兼容性与审计记录，不提前升级 M10–M13 或 v1.0 状态。
