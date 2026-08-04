# ADR 0007：先记录意图的受控发布与远端对账

- 状态：已接受（Accepted）
- 日期：2026-08-04

## 背景

Git Push、创建 PR 与查询 CI 都是可能超时但实际已经生效的远端副作用。若 Marshal 在失败后直接重放命令，可能产生重复 PR、覆盖他人分支，或把旧提交的绿色检查误认为当前结果。业务 Worktree 还可能包含 Worker 写入的 Git 配置或 Hook，不能直接用普通 `git commit` 发布。

## 决策

Marshal 在任何远端副作用之前完成以下步骤：

1. 重新观察 Worktree，并要求 `snapshotDigest` 与 `diffDigest` 精确匹配已接受的 Verification 与 ReviewDecision；
2. 使用临时 Git Index，从锁定父提交构造 Tree；普通文件以 `hash-object --no-filters`、符号链接以 raw target 写入，避免执行仓库 filter，再通过 `git commit-tree` 创建 Commit；禁用 System/Global Git Config、Credential Helper 与 Hook；
3. 将完整身份、证据摘要、派生分支、受控 Commit SHA 和唯一 Marker 原子写入 `publication-intent.json`；
4. Publisher 只接受该 Intent，只能无 Force Push 到派生分支，并且只能创建或更新一个带相同 Marker 的 Draft PR；
5. Push 或 PR 创建返回歧义错误时，先读取远端 Branch、PR Marker 与 Head SHA 对账，再决定成功、可重试或永久阻断；
6. 将 Provider Repository ID、PR ID、Head SHA 和发布结果写入 `publication-record.json`。CI 观察必须同时绑定这三个身份，并只按 TaskSpec 中冻结的 Required Check 名称判定。

同一 Run 在 CI 代码失败后可以进入新 Review/Publication 世代。每个世代以 `reviewRound` 标识；新受控 Commit 以前一已发布 Head 为唯一父提交，并且 Publisher 只允许远端分支从 `previousHeadSha` 无 Force Push 地 fast-forward。旧世代 Intent、Record 与 Check 记录在创建新世代前归档，当前世代仍使用稳定根路径，保证重试语义不变且始终复用同一个 Draft PR。

Publisher 的 GitHub 凭据只能通过显式绝对路径 `MARSHAL_GH_PATH` 与独立 `MARSHAL_GH_CONFIG_DIR` 获得。Worker 环境不继承 Publisher Credential。要求发布的 TaskSpec 必须冻结 `expectedRemoteUrl`，解析出的远端必须与其精确一致，防止 Local Git Config 或 `url.*.insteadOf` 重定向。Provider 子进程输出视为不可信，失败时不得原样进入错误或持久化记录。

MVP 只支持规范的 `https://github.com/<owner>/<repo>[.git]` Remote、Draft PR 与 `mergePolicy=never`。不提供 Force Push、Ready for Review、Merge、Release 或 Deploy 能力。

## 后果

- 重试复用同一个 Intent、Commit、Branch 与 PR，不会盲目重复副作用；
- CI 返工产生可追溯的新发布世代，并以 fast-forward 更新同一分支与 Draft PR；
- 同名远端分支指向其他 Commit 时安全阻断，不进行 Force Push；
- Worker 写入的 Hook、Local Git Config 和 Filter 不参与发布 Commit；
- CI 绿灯不能跨 PR 或 Head SHA 复用；
- Intent 与远端记录不一致时停止发布，通用修复与清理交由 M6 Reconciliation；
- Local Profile 的同 UID 进程隔离仍不是恶意代码安全边界，Hardened Profile 继续延后。

## 未采用方案

- **直接 `git commit && git push`**：会执行工作区 Git 配置与 Hook，且难以精确绑定已验收 Tree；
- **失败后无条件重试 Push/PR Create**：无法区分“未执行”与“已执行但响应丢失”；
- **复用任意同名 PR 或 CI 状态**：不能证明远端对象属于当前 Run 和当前 Commit；
- **由 Worker 或主 Agent 直接调用 `gh`**：破坏权限分离与可审计的发布端口。
