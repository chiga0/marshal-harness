# Milestone 2 验收报告

- 验收日期：2026-08-04
- 状态：**`PASSED`**
- 范围：Git Worktree、Repository Identity、独立 Verification、Scope Gate、Artifact 与 Command Evidence
- 真实 Worker/Publication Side Effect：未启用

## 交付结果

- 以规范化 Git Common Directory 标识仓库，并解析、锁定完整 Base SHA；
- 在 `.marshal/worktrees/<task-id>/` 创建独立 linked worktree 与 `marshal/<task-id>` branch；
- 使用短时 Repository Lock 和持续 Task Lock，允许不同任务隔离并行、同一任务单写者；
- 独立观察 tracked、untracked、rename、binary、mode、symlink 与 submodule 变更，流式计算完整摘要并有界保存 Patch；
- 对 rename 双端、allow/deny glob、`.marshal/`、symlink escape、submodule、文件数和 diff 大小执行确定性 Gate；
- 以 direct argv、过滤环境、精确 executable、受限 cwd、全局/单命令超时和有界日志运行验收命令；
- SIGINT、SIGTERM、Context Cancellation 会终止完整 Verification Process Group；
- 独立收集常规交付物并流式计算 SHA-256；无效、缺失或逃逸交付物仍生成 Schema-valid 失败证据；
- 生成并校验 VerificationReport、ArtifactManifest、observed.patch 与命令日志；
- `marshal task verify --run` 在持有 Run/Task Lease 后验证冻结 TaskSpec 与仓库身份，并持久化 `verification.completed`。

## 故障与不变量验证

| 验收项 | 结果 |
| --- | --- |
| Fixture Run 修改主 Checkout | 未发生 |
| macOS `/var` 与 `/private/var` identity | 规范化后相等 |
| 同一 Task 并发写入 | 第二个 Lease 失败 |
| 不同 Task 隔离 worktree | 可并行创建 |
| Path Traversal、Forbidden Rename | Gate 失败 |
| Symlink 直接或中间组件逃逸 | Gate 失败且证据落盘 |
| Submodule 未授权变更 | Gate 失败 |
| Oversized Diff / Patch 截断 | Gate 失败 |
| 未设置可选 `maxDiffBytes` | 使用 64 MiB 安全采集默认值 |
| Worker Declaration 放宽范围 | 无效 |
| Verifier 命令写脏 worktree | 被前后 Snapshot 检出 |
| 相对 executable | 从命令 cwd 解析且不得逃逸 worktree |
| Cancellation | Observe 与完整子进程树终止 |
| 用户 Git hooks/config | 与 Verifier/Worktree 管理隔离 |
| 未完成 merge/rebase/cherry-pick/revert | Repository Gate 失败 |

## 自动验收

`make check` 已通过 Format、Vet、Staticcheck、Race Test 与 Build；完整 `make ci` 还包括 `govulncheck`。测试在真实临时 Git 仓库中覆盖 worktree、diff、artifact、command、baseline 和 CLI `VERIFYING → REVIEW_PENDING` 路径。

独立审查首轮给出 `REQUEST_CHANGES`，三个 P1 与关键 P2 全部整改；复审给出 `APPROVE`。复审留下的两个非阻塞 P2 也在里程碑结束前关闭。详见 [Milestone 2 OpenCode Review](reviews/milestone-2-opencode-review.md)。
