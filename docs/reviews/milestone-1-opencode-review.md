# Milestone 1 OpenCode 独立审查

- 日期：2026-08-03
- Reviewer：OpenCode `opencode/big-pickle`，只读 Plan 模式
- 写入、提交、推送权限：无
- 最终结论：**`APPROVE`**

## 首轮结论与处理

首轮结论为 `REQUEST_CHANGES`。Reviewer 发现 `VERIFYING → REVIEW_PENDING` 错误要求强制门禁通过，导致验证失败无法进入 Rework/Reject 流程。该 P1 已修复，并增加 `RequiredGatesPass=false` 的合法转换回归测试。

同时关闭以下 P2：

- Recovery Replay 不再重新校验瞬时 Lease/Guard；
- Journal Append 在持久化前验证结构转换；
- `task status` 在外部状态目录下验证 Repository Identity；
- Reducer 补全 READY、Attempt、Evidence、Decision、Publication、CI、Abort 与 Budget 守卫，并维护计数器。

## 复审与追加加固

复审确认上一轮问题全部关闭，无 P0/P1，结论为 `APPROVE`。Reviewer 提出的三个非阻塞 P2 也在提交前关闭：

- Inspection 同时比较 Snapshot/Journal 的 Sequence 与 State；
- 增加 Reduce/Replay 计数器一致性测试；
- 拒绝位于仓库内部但不是默认 `.marshal/` 的状态目录。

此外补充跨 Run Lease 拒绝、Event 字段校验、Repository Identity 版本校验，并升级间接依赖以清除 Module-level 漏洞提示。

复审实际运行 `gofmt`、`go vet`、`staticcheck`、Race Test、Build、Module Verify/Tidy、`govulncheck`、Diff Check 和临时仓库 CLI 冒烟测试，结果全部通过。
