# Milestone 5 冻结范围

状态：`FROZEN_FOR_IMPLEMENTATION`

冻结日期：2026-08-04

## 目标

把已通过独立 Verification 且被主 Agent 接受的精确 Worktree Snapshot，以受控本地 Commit、唯一远程 Branch 和单个 GitHub Draft PR 发布；记录可恢复的 Publication Intent/Record，并只接受绑定当前 Head SHA 的 Required Check。MVP 永不 merge。

## 发布前门禁

- `task publish` 只接受 `PUBLISHING`；`task accept` 只接受 `CI_PENDING`；
- 重新验证冻结 TaskSpec/Policy、VerificationReport、ReviewPacket、ReviewDecision 的 task/run/spec/evidence/digest 绑定；
- 当前 Worktree Root/CommonDir、snapshotDigest 与 diffDigest 必须仍与被接受证据一致；
- 所有 Required Gate 必须为 `pass`，Decision 必须为 `accept/publish/do-not-merge`；
- Task 必须指定 `provider=github`、`mode=draft`、`mergePolicy=never`；
- TaskSpec 必须冻结非空 `expectedRemoteUrl`；解析后的 Remote URL、Authenticated GitHub Repository 与 Base Branch 必须匹配冻结策略，Local `url.*.insteadOf` 重写不得改变发布目标。

## Controlled Commit

- Publisher 持有 Run Lease 与 Task Worktree Lease；
- 使用 Attempt 独立临时 Index，从当前发布世代的父提交 `read-tree`；普通文件通过 `hash-object --no-filters` 写入 raw blob，符号链接按 link target 写入，再显式 `update-index` 与 `write-tree`；
- 使用 `commit-tree`，不运行 Ambient Hook、不读取 Global/System Git Config、不使用 Worker Commit；
- Commit 写入 Task/Run/spec/evidence Trailer；Author/Committer 来自固定 Marshal Policy；
- 对 Commit Tree 中每个普通文件/符号链接执行 raw blob 与 Worktree 内容比对，并确认没有 `.marshal/`；不一致时发布前 Block。

## 凭据与远端副作用

- CLI 只接受显式 absolute `MARSHAL_GH_PATH` 与 `MARSHAL_GH_CONFIG_DIR`；后者只注入 Publisher 子进程，不持久化到 Run；
- Worker 环境继续没有 `GH_CONFIG_DIR`、Token、AskPass 或 Publisher 进程句柄；
- Git push 使用临时无 Secret AskPass Wrapper，Wrapper 按 Password Prompt 调用精确 `gh auth token`；Git Global/System Config 与 Credential Helper 禁用；
- Branch 固定派生为 `marshal/<task-id>-<run-id-short>`，不接受调用者任意 ref；禁止 force push；
- Remote Branch 不存在则创建，等于当前 expected head 则复用；返工发布只允许远端等于上一发布世代 head，并以它为父提交 fast-forward；其他 SHA 一律 `BLOCKED`；
- PR Body 含唯一 Marker `<!-- marshal task=... run=... -->`；零匹配则建 Draft，一个匹配则复用/更新，多个匹配则 Block；
- Push 或 PR API 超时后必须先 Reconcile Remote，不能盲目重试创建；
- Publisher API 不实现 Merge、Ready-for-review、Release、Deploy 或 Repository Setting。

## CI 观察

- `RemoteCheckRecord` 绑定 Repository ID、PR ID 与精确 head SHA；
- Required Check 只能由当前 head commit 的 Check Run/Status 满足；旧 SHA 绿色无效；
- Pending 保持 `CI_PENDING`；全部 Required Check 成功进入 `ACCEPTED`；代码失败且返工预算可用进入 `REWORK_REQUESTED`；外部/权限/歧义失败进入 `BLOCKED`；
- Polling 单次命令有界，不在 CLI 内无限等待。

## 持久化与崩溃恢复

- 外部副作用前先原子保存 `publication-intent.json`；
- 成功后保存 Schema-valid `publication-record.json`，再追加 Lifecycle Event；
- 每个发布世代携带单调递增的 `reviewRound`；返工开始新世代前，将旧 Intent/Record/Check/Error 归档到 `publications/review-<round>-<head>/`，根目录只保留当前世代；
- 本地 Commit 后、Push 后、PR 创建后任一点崩溃，重试均通过 Commit/Branch/Marker/Provider ID 对账；
- `publication-record.json` 已落盘但 Lifecycle Event 尚未追加时，重试先校验完整发布身份；一致则复用并补齐事件，不一致则阻断；
- 任何不可安全分类的状态进入 `BLOCKED`，保留 Worktree、Intent、stderr 与远程观察。

## 测试与退出条件

- Unit：branch/marker/body、remote parsing、controlled environment、tree/raw blob、record schema、check classification；
- Integration：Fake GitHub CLI 覆盖 create/update/reconcile、ambiguous timeout、duplicate PR、unexpected remote head、stale snapshot、hook 禁用；
- E2E：在维护者授权的 GitHub 仓库创建一个真实 Draft PR，验证幂等重跑不重复创建；不 merge；测试后保留或关闭由维护者决定；
- Security：Worker 测试继续证明没有 Publisher Credential；Publisher 记录不含 Token、Config Dir 或绝对 Worktree Path；
- `make ci`、独立 OpenCode 审计、提交推送与远端 CI 全绿后才进入 M6。

## 明确不在本阶段

- Merge、Ready PR、Release、Deploy、GitLab；
- Automatic Rebase、Force Push、删除 Remote Branch；
- Webhook/Daemon；
- 完整 Worker/Publisher OS 级恶意进程隔离；
- 通用崩溃 Reconciliation/Repair CLI（M6）。
