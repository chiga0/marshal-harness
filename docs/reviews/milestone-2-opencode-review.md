# Milestone 2 OpenCode 独立审查

- 日期：2026-08-04
- Reviewer：OpenCode `plan` Agent（DeepSeek `deepseek-v4-pro`）
- 模式：只读；只允许读取当前仓库和运行已有测试
- 写入、提交、推送权限：无
- 最终结论：**`APPROVE`**

## 首轮结论与处理

首轮结论为 `REQUEST_CHANGES`。Reviewer 发现三个 P1：

- 可选 `maxDiffBytes` 未设置时被错误转换成 1 byte Patch 上限；
- symlink 或非常规交付物的 `invalid` 记录不满足 ArtifactManifest Schema，导致失败证据无法落盘；
- Scope Gate 未拒绝逃逸 worktree 的 symlink。

整改加入安全默认采集上限、独立的 invalid Artifact Schema 分支、symlink target 解析及回归测试。与此同时关闭 Reviewer 点名的关键 P2：文件摘要改为流式处理、Git Observe 接入 Context、相对 executable 从命令 cwd 解析并限制在 worktree、CLI 接入 SIGINT/SIGTERM、Run Lease 前移以消除状态检查 TOCTOU、Baseline worktree 纳入 Repository Lock，并隔离用户级 Git 配置与 hooks。

## 复审结论与追加加固

复审运行全量 `go test -race -count=1 ./...`、Format Check、Vet、Staticcheck 与 Build，结果全部通过。Reviewer 确认上一轮 P0/P1 均已关闭，Worktree 隔离、独立证据、Worker 不能自证、Schema 和生命周期保持一致，最终给出 `APPROVE`。

复审记录两个非阻塞 P2，也在提交前关闭：

- Repository Integrity 的所有 Git 子进程改为使用调用方 Context；
- symlink escape 检测解析完整目标链，覆盖中间目录本身为外部 symlink 的场景。

三个 P3（未使用字段、顺序调用的 capture 无并发锁、少量环境变量使用插入排序）不影响正确性，留待后续机械清理。
