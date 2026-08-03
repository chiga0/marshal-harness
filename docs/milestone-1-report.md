# Milestone 1 验收报告

- 验收日期：2026-08-03
- 状态：**`PASSED`**
- 范围：State Machine、Run Store、Lease、Canonical Digest、Repository State 与 Fake Adapter
- 真实 Worker/Publication Side Effect：未启用

## 交付结果

- 提供合法格式的 Task、Run、Attempt 与 Event ID 生成及校验；
- 实现完整生命周期转换表与运行期守卫，失败的强制门禁仍可进入 Review，但不能进入发布或接受；
- 实现带期望 Sequence、重复 Event ID 和非法转换防护的追加式 JSONL Journal；
- 实现 `fsync → rename → directory fsync` 的原子 `state.json`；
- 使用 OS File Lock 提供互斥 Run Lease，并记录随机 Token、PID、进程启动时间和 Heartbeat；
- 使用 RFC 8785 JCS 与 SHA-256 生成稳定摘要；
- 实现默认 `.marshal/`、`.git/info/exclude`、仓库身份绑定和绝对 `MARSHAL_STATE_DIR`；
- 实现 `marshal init` 与只读 `marshal task status --run`；
- 实现由 Transcript Fixture 驱动、结果可重复且支持取消的 Fake Adapter。

## 故障与不变量验证

| 验收项 | 结果 |
| --- | --- |
| 全部合法和非法状态组合 | 256 组表驱动测试通过 |
| Failed required gate 仍进入 Review | 通过 |
| Stale Sequence、重复 Event 与非法转换 | 默认拒绝 |
| 截断 Journal 最后一条记录 | 从最后一条完整记录重建 |
| Recovery Replay | 不重新依赖瞬时运行期守卫 |
| Snapshot/Journal Sequence 或 State 漂移 | Inspection 显式失败 |
| Frozen Input 在 `READY` 后改变 | 返回冲突并要求新 Run |
| 同一 Run 的并发 Lease | 第二个持有者失败 |
| 跨 Run 使用 Lease | 失败 |
| 外部状态目录被不同仓库复用 | 失败 |
| 仓库内非默认状态目录 | 失败，避免 Git 泄漏 |
| `.marshal/` 出现在 Status 或 Commit Tree | 未出现 |

## 自动验收

`make ci` 已通过 Format、Vet、Staticcheck、Race Test、Build 与 `govulncheck`。CLI E2E 在临时真实 Git 仓库中覆盖 `init → Snapshot → task status`。独立审查先发现一个 P1 与若干 P2，整改后复审结果为 `APPROVE`，且后续 P2 加固项也已关闭。

独立审查记录见 [Milestone 1 OpenCode Review](reviews/milestone-1-opencode-review.md)。
