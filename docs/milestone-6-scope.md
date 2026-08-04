# Milestone 6 冻结范围

状态：`FROZEN_FOR_IMPLEMENTATION`

冻结日期：2026-08-04

## 目标

在不改变 M0–M5 证据门禁与 Publisher 权限边界的前提下，完成本机已配置的 OpenCode `1.18.12`、Qwen Code `0.21.5`、Pi `0.83.0` 三个真实 Worker Adapter，补齐运行态 Reconciliation、CI 停滞诊断、Archive/Cleanup Preview、Compatibility Matrix 与 Operator 文档，并以完整真实 Worker→Verification→Review→GitHub Draft PR→CI Outcome E2E 达到 Local MVP 可用状态。

## Adapter 契约

- 每个 Adapter 只接受显式 absolute executable 环境变量，不搜索近似命令或旧入口；
- Probe 冻结 executable realpath、SHA-256、精确版本、结构化输出、非交互编辑、取消、Session 与 Permission 能力；
- 未知版本默认拒绝；只有显式 Experimental Policy 才能运行并在 Outcome 标记；
- Worker 使用 direct argv、环境 allowlist、独立 Temp/Home/Config、Process Group Cancellation 与有界 stdout/stderr/transcript；
- Provider Event 归一化为既有 Normalized Event 与 WorkerResult，不把 Free-form 最终文本当作完成协议；
- Worker 无 GitHub Publisher Credential、不能调用 Marshal Publisher，不能自行扩大 Scope 或 Spawn 未授权子 Agent；
- 共享 Conformance Suite 覆盖成功、Provider Failure、Permission Denial、Malformed Event、Output Limit、Cancellation、Worktree Identity 破坏和 Credential Isolation；
- Session Resume 只对 Provider 明确返回且 Probe 证明可恢复的 Session ID 开放；`ephemeral` 始终可用，不能伪造 Resume 支持。

## Adapter 选择与 CLI

- `marshal task run` 根据冻结 TaskSpec `preferredAdapter` 选择 Adapter，并按 Policy/TaskSpec 顺序使用显式允许的 Fallback；
- 每次实际选择、Probe、Fallback 原因与版本进入 CapabilitySnapshot/Attempt Event；
- OpenCode 保持已验收行为；Qwen Code 与 Pi 不得通过复制测试假装兼容，必须各有 Fake executable 和真实 Live E2E；
- `marshal doctor` 报告三 Adapter 的配置、版本与兼容状态，不泄露认证路径或内容。

## Reconciliation 与 CI 停滞

- `marshal doctor --run RUN_ID` 只读核对 Snapshot、Journal、Worktree、Intent/Record、Remote Branch/PR/Head 与 Outcome；
- 任何修复必须使用显式 `--repair`、持有 Run Lease、生成 Repair Event/Diagnostic，并且只允许从权威 Journal/Record 重建可证明的本地 Snapshot；
- 无法唯一证明的远端或本地状态进入 Quarantine/`BLOCKED`，不猜测、不覆盖；
- `CI_PENDING` 按冻结 Run Deadline 分类；超时产生 Diagnostic 与 `BLOCKED`，`skipping`/missing 不会无限静默等待；
- Doctor 不创建 PR、不 Push、不 Merge、不删除 Remote Branch。

## Archive 与 Cleanup

- `marshal task cleanup --run RUN_ID` 默认只输出精确 Preview；真正执行要求显式 `--apply`；
- Active Lease、非终态 Run、未归档 Dirty Worktree、路径身份不一致时拒绝；
- Cleanup 只处理该 Run 的本地 worktree、Attempt 临时目录和可再生 Cache，不删除 Outcome、Journal、Publication Record、Remote Branch 或 PR；
- Dirty Worktree 必须先导出 Patch/摘要并由操作者显式授权；M6 默认安全阻断，不自动丢弃；
- 所有删除目标先解析为 State Root 下或 Git 已注册的精确 Worktree，禁止宽泛路径、glob 与 symlink escape。

## Observer 与文档兼容性

- Compatibility Matrix 由共享 Conformance 与 Live Probe 证据生成，记录三 Agent 的精确版本、能力与限制；
- Operator Runbook 覆盖安装、Codex Desktop/CLI 调用、手机端监督、失败分类、Reconcile、Cleanup 与 Draft PR 处置；
- Observer 作为独立模块，通过稳定 Port 与 Worker Adapter、Core 生命周期解耦；Backend 采用 Probe 与能力协商，不按操作系统或终端名称在 Core 中分叉；
- M6 实现默认 `captured` 与首个可视化 `cmux` Backend；cmux 只在独立 workspace/pane 中运行 Marshal 的只读日志跟随器，不能取得 Worker stdin、凭据或生命周期控制权；
- cmux Probe 区分 `not-installed`、`installed`、`reachable`、`authorized` 与 `ready`，支持 `PATH` 和 macOS App Bundle CLI 发现；未安装、未授权或运行失败时安全降级到 `captured`；
- 为后续 `iterm2`、`ghostty`、`terminal`、`tmux` Backend 保留相同接口，本阶段不承诺实现；
- 任何交互式终端接管必须生成 `manual-intervention` Diagnostic，并使该 Attempt 的自动执行证据失效，之后必须重新执行独立 Verification；
- 全部人类文档使用中文，命令、字段与 Provider 名称保留原文。

## 测试与退出条件

- Unit/Contract：三 Adapter Probe、Parser、Permission、Session、选择/Fallback、Observer 探测/降级、Doctor/Repair 与 Cleanup Guard；
- Integration：三个 Fake executable 通过同一 Conformance Suite；Journal/Snapshot、Publication、CI Deadline 与 Cleanup Crash Fixture 可恢复或安全阻断；
- Live Adapter E2E：本机真实 OpenCode、Qwen Code、Pi 各自在临时仓库受管 worktree 完成最小修改，Marshal 独立 Verification 通过；
- Full MVP E2E：至少一个真实 Worker 从冻结 TaskSpec 出发，经过真实 Adapter、Verification、Codex ReviewDecision、受控 Commit、真实 GitHub Draft PR、PR CI 与 Outcome Export；不 merge；
- Security：三个 Worker 环境均无 Publisher Credential；Cleanup 不越界；Doctor/日志不泄露 Agent/GitHub 认证内容；
- `make ci`、三个本地 Agent 的交叉审计、提交推送与远端 CI 全绿后，才能标记 Local MVP `USABLE`。

## 明确不在本阶段

- Automatic Merge、Ready for Review、Release、Deploy、删除 Remote Branch；
- GitLab Publisher、Webhook/Daemon、MCP/ACP Server Facade；
- Hardened Container/VM、Multi-user Service 与 Hostile-code Containment；
- 自动升级 Agent、宽松兼容未知版本或基于主观代码质量的路由。
