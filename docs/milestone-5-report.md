# Milestone 5 验收报告

- 验收日期：2026-08-04
- 范围：受控 Commit、GitHub Draft Publisher、远端 CI 观察与发布世代恢复
- 结论：`PASSED`

## 已交付

- `PublicationIntent`、`PublicationRecord`、`RemoteCheckRecord` 三份 Schema、正反 Fixture 与 Runtime Semantic Binding；
- 发布前重验冻结 TaskSpec、Policy、Verification、Review 与 Worktree Snapshot；
- 使用临时 Index、raw blob、`commit-tree` 和固定身份创建受控 Commit，不执行 Worker filter、hook、credential helper 或 ambient system/global Git config；
- 强制冻结 `expectedRemoteUrl`，阻断 Remote 漂移与 Local `url.*.insteadOf` 重定向；
- 独立 GitHub Credential Profile、无 Force Push 的派生分支、唯一 Marker 与单 Draft PR 幂等创建/更新；
- Push/PR 歧义结果先查询远端对账，并兼容 GitHub 创建后的短暂可见性延迟；
- Required Check 严格绑定 Repository、PR 与 Head SHA，只有显式 `pass` 满足门禁；
- CI 失败返工使用 `reviewRound` 新世代，以前一 Head 为父提交 fast-forward 同一 Branch/PR，旧记录归档；
- Journal 领先 Snapshot 时自动重放，包括 Publication Identity；部分发布世代归档中断可幂等恢复；
- `marshal task publish` 与 `marshal task accept` CLI，以及显式 opt-in 的真实 GitHub E2E。

## 验证证据

- 本地 `make ci`：Format、Vet、Staticcheck、全仓 Race Test、Build 与 `govulncheck` 全部通过；
- Fake Publisher/gh/git：创建、编辑、幂等、超时歧义、重复 PR、Fork Identity、Unexpected Head、Hook/Filter、Credential 与 Secret Redaction 全覆盖；
- Publication Core：真实临时 Git Repository/Worktree，覆盖 Snapshot 漂移、受控 Tree、Symlink、Record/Journal 崩溃窗口、返工 fast-forward 与 CI 分类；
- 独立 OpenCode 复审：无 P0/P1，最终结论 `APPROVE`，详见[审计记录](reviews/milestone-5-opencode-review.md)；
- 主分支提交 `70e1af7` 的 GitHub Actions run `30889069165`：Linux、macOS 与 Secret Scan 全绿；
- 真实 Publisher E2E 创建并两次复用同一 [Draft PR #1](https://github.com/chiga0/marshal-harness/pull/1)，没有重复 PR；PR CI run `30889190854` 全绿，未 merge。

## 遗留到 M6

- CI_PENDING 的运行期限、停滞诊断与 Operator Reconciliation；
- Archive/Cleanup Preview 与显式确认；
- Qwen Code、Pi Adapter 和三个真实 Agent 的共享 Compatibility Matrix；
- 完整 Harness 层的真实 Worker→Verification→Review→Publisher Fixture E2E。
