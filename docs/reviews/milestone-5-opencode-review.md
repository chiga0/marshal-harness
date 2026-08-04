# Milestone 5 OpenCode 独立审计

- 审计日期：2026-08-04
- 审计方式：本地 OpenCode 只读审查；主 Agent 复现、整改与复审
- 初审结论：`BLOCKED（REQUEST_CHANGES）`

## 初审验证

审计 Agent 阅读 M5 全部 tracked/untracked 代码、Schema、Fixture、ADR 0003/0007 与冻结范围，并执行：

- `make format-check`；
- `go vet ./...`；
- `go tool staticcheck ./...`；
- `go test -race ./...`，以及 publication、publisher、CLI、contract 的非缓存重跑；
- `go tool govulncheck ./...`；
- `git diff --check`。

上述检查均通过，但代码审查发现以下问题。

## 初审发现

### P1-1：CI Rework 后的二次发布结构性死锁

Required Check 失败后，生命周期会进入 `REWORK_REQUESTED`，同一 Run 可再次执行 Worker、Verification、Review 与 Publishing。但旧 `publication-intent.json` 仍绑定上一轮 Snapshot/Decision，固定派生分支也仍指向旧 Commit：Core 会因 Intent 陈旧而阻断；即使绕过该处，Publisher 也会因远端 Head 与新 Commit 不同而阻断。现状无法完成已声明的 Rework Loop。

整改决策：保持单一 Draft PR 与禁止 Force Push。新发布世代的受控 Commit 以前一已发布 Commit 为唯一父提交，使远端分支只发生 fast-forward；Intent/Record 增加 `reviewRound` 与 `previousHeadSha`，旧发布产物按 Commit 世代归档，重试仍复用当前世代 Intent。

### P2-1：Required Check 的 `skipping` 被计为通过

`gh pr checks` 返回 `skipping` 时，当前聚合逻辑会把 Required Check 当成成功，违反“全部 Required Check 必须实际 pass”的门禁。

整改：`skipping` 改为 `pending`；只有精确 `pass` 才满足 Required Check。

### P2-2：既有 PublicationRecord 可能被静默覆盖

若 `publication-record.json` 已落盘、但 `publication.completed` Event 尚未追加时崩溃，重试没有先校验旧 Record 身份，可能丢失旧远端 PR 的审计记录。

整改：重试发现既有 Record 时必须先校验其完整身份；一致则复用，不一致则阻断。进入新 Rework 世代时先归档上一世代 Record。

### P3

- CI 通过生成 Outcome 时应再次把 ReviewDecision Digest 与冻结 Journal/PublicationRecord 对齐；
- Publisher Git 子进程应同时禁用 `core.hooksPath`，避免执行操作者的 `pre-push`；
- 成功重试后应清除陈旧 `publication-error.json`；
- PR 列表上限 10 过小；
- `schemas/README.md` 有重复段落，`docs/audit-report.md` 尚未记录 ADR 0007；
- `pr view` 与 `pr checks` 之间仍有 TOCTOU，需要在读取 Checks 后再次复核 Head。

以上可验证项全部纳入本轮整改与回归测试。最终复审结论将在整改完成后追加。

## 最终复审

OpenCode 对最新工作树执行了 `go build`、`go vet`、`gofmt`、`git diff --check`、全仓测试，以及 publication、publisher、verification、contract 的去缓存聚焦测试，结论为 **`PASS`**，没有 P0/P1。

复审确认初审的返工死锁、`skipping` 误通过、Record 崩溃覆盖和所有 P3 可验证项均已关闭；发布世代 fast-forward、部分归档崩溃恢复、raw Git filter/hook 隔离均有回归证据。

复审另指出一个 P2 与四个 P3：

- Journal Append 成功但 Snapshot 写入前崩溃时，Publication Identity 不能由 Replay 重建；
- 首次 observe 与 controlled commit 之间存在竞态窗口；
- 未冻结 Remote URL 时，Local `url.*.insteadOf` 可能重定向发布目标；
- 永久 `skipping` 的 CI Check 需要由 M6 的运行超时/Doctor 处理；
- 超长 Task Summary 可能形成 GitHub PR 标题失败重试。

主 Agent 在接受复审结论前继续整改：`Inspect` 现在自动重放领先于 Snapshot 的 Journal 尾部，`publication.completed` Event 带齐并恢复 Publication Identity；controlled commit 后二次观察已接受快照；发布强制要求冻结 `expectedRemoteUrl`，并加入同 GitHub 域名 `insteadOf` 重定向攻击测试；PR 标题按 Unicode rune 有界截断。以上新增测试均通过。CI 停滞上限与通用 Doctor Reconciliation 属于 M6 已冻结范围，不影响 M5 的单次有界 Polling 与安全默认。

最终验收结论：**`APPROVE`**。M5 可在全量 `make ci`、真实 Draft PR 幂等 E2E 与远端 CI 通过后标记为 `PASSED`。
