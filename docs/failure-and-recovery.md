# 故障与恢复

## 原则

Recovery 首先保存证据。Marshal 不会为了回到“干净状态”而自动 Reset Dirty Worktree、删除 Run Directory、Force Push 或重复外部副作用。

Recovery 以当前仓库的 `.marshal/` 为边界。每个仓库独立维护 `repo.json`、Lease、Run Journal 与 worktree；MVP 不依赖全局仓库索引。使用 `MARSHAL_STATE_DIR` 覆盖时也必须验证其中记录的 Repository Identity 与当前仓库一致。

## 故障分类

| 类别 | 示例 | 默认处理 |
| --- | --- | --- |
| Input | Schema 无效、仓库缺失、Base 模糊 | 保持 `PLANNED` 或进入 `BLOCKED` |
| Capability | Worker 缺失、Flag 不兼容、Profile 不满足 | 启动前 `BLOCKED` |
| Operational Retryable | Rate Limit、临时 Provider/Network 错误 | 预算内 `RETRY_PENDING` |
| Operational Non-retryable | Auth 无效、协议损坏、Model 不支持 | `BLOCKED` |
| Worker Implementation | 代码不完整、测试失败、Diff 越界 | Verify 后 `REWORK_REQUESTED` 或 `REJECTED` |
| Harness Internal | Journal 写失败、不变量冲突、Parser Bug | 停止副作用并 `BLOCKED` |
| Publisher Retryable | 本地 Accept 后 Forge 临时故障 | 保留 Accept，重试 `PUBLISHING` |
| Publisher Ambiguous | Push/Request Timeout，远端结果未知 | 重试前 Reconcile Remote |
| CI Actionable | 当前 Head 的 Required Check 失败 | 预算内 `REWORK_REQUESTED` |
| CI External | Runner 或依赖服务故障 | `BLOCKED` |
| Operator Cancellation | SIGINT 或显式 Abort | Graceful Stop 后 `ABORTED` |

分类保存为 Structured Code 与摘要，不能只靠自然语言决定 Retryability。

## 崩溃一致性

每个状态变更操作遵循：

1. 校验预期当前状态与 Lease；
2. Append Intent Event 并 Flush；
3. 执行有边界的本地或外部操作；
4. Append Result Event 与 ID；
5. 原子替换 `state.json`；
6. 释放或续期 Lease。

外部操作必须可幂等或可 Reconcile，因为进程可能在 Provider 成功但 Result Event 尚未落盘时崩溃。

## 启动时 Reconciliation

`marshal doctor` 与 Task Command 在继续前：

1. 解析 Journal 到最后一条完整 JSONL；
2. 比较 Journal Sequence/State 与 `state.json`；
3. 检查 Lease Owner PID、Start Time 与 Heartbeat；
4. 在 Adapter 支持时检查 Worker Process/Session；
5. 检查 Worktree 并重新计算 Status Digest；
6. Publication Intent 没有持久结果时检查 Remote Branch/PR；
7. 只提出合法恢复操作。

操作系统会复用 PID，因此 Stale PID 不足以判断。Lease Identity 同时包含随机 Token 与 Process Start Metadata。

## Worker 中断

Timeout 或 Cancellation 时：

1. 记录 Cancellation Intent；
2. 通过 Adapter/Process Group 发送 Graceful Termination；
3. 等待有限 Grace Period；
4. 终止 Process Tree；
5. Flush Capture Output；
6. Snapshot Git 与文件系统；
7. 分类 Attempt 并保留 Worktree。

即使 Session 可恢复，也创建新 Attempt ID 并关联旧 Session；Resume 不会抹掉中断记录。

## 部分变更

Partial Change 是证据，不会自动成为可复用工作。操作者或主 Agent 可以：

- Resume 同一 Worker Session；
- 让新 Worker 接手保留 Worktree 与 Failure Context；
- 归档 Patch 后创建干净新 Run；
- Reject 或 Abort。

Marshal 绝不自动 Reset Partial Change。新 Worker 必须被告知它继承的是未验证变更。

## Verification 中断

取消产生 `cancelled` 的不完整 VerificationReport，不能被 Review 为通过。后续验证针对新 Snapshot 生成新 Report，不得把 Passing Gate 追加到旧 Report。

若命令留下 Generated Change，重试前执行 Dirty-verifier Policy。

## Review 中断或无效决策

无效或 Schema 不匹配的 ReviewDecision 让 Run 保持 `REVIEW_PENDING`。被拒绝 Decision 作为 Diagnostic Artifact 保存，Marshal 不把 Free-form Prose 猜测成 Verdict。

引用陈旧 Evidence 的 Decision 同样保留但拒绝使用，必须重新 Review。

## Publication Reconciliation

发布拆成可观察步骤：

1. 创建并校验 Local Commit；
2. 检查 Remote Branch；
3. Push 或确认 Expected Head；
4. 通过 Provider ID 或 Task Marker 查找 Publication；
5. 创建或更新 PR/MR；
6. 记录 ID 与 URL。

Ambiguous Timeout 后先查询 Remote，不能假设失败并创建重复 PR/MR。Remote Branch 含 Unexpected Commit 时必须 Block，默认不 Force Push。

`publication-record.json` 已持久化但对应 Journal Event 尚未追加时，重试必须先核对 Intent、Repository、PR 与 Head 的完整身份；一致时复用记录并补齐事件，不一致时停止副作用。CI 返工创建新的发布世代，旧世代产物归档到 Run 内的 `publications/`，新提交仅允许从上一 Head fast-forward。

## CI Recovery

CI Polling 保存 Cursor/Last Observation 并通过有界 Backoff 恢复。Check Identity 包含 Repository、Head SHA 与 Provider Check ID，其他 SHA 的结果视为陈旧。

Provider 不可用时保持 `CI_PENDING` 到 Timeout，随后进入 `BLOCKED`。只有日志与责任归属表明是代码失败时，才进入 Actionable Rework。

## 状态损坏

`state.json` 损坏而 Journal 有效时，重建新 Snapshot 并把损坏文件作为 Diagnostic Artifact 保留。Journal 尾部截断时，完整 Record 仍具权威性，并保存截断内容。

若两者都无法建立安全状态，Run 进入 Quarantine：禁用 Worker 与 Publisher，Worktree 按策略只读，由操作者导出证据或执行有文档的 Repair。

## Cleanup 与 Garbage Collection

Cleanup 提供 Preview 与显式确认，校验精确 Target，拒绝宽泛或未解析路径。

- Active Lease 永不删除。
- Dirty Worktree 必须先归档 Patch 并显式授权。
- Local GC 不删除 Remote Branch 或 PR/MR。
- Outcome Bundle 使用独立 Retention Policy。
- Tombstone 记录已删除本地资源及其可恢复性。

M6 的首个 Cleanup Apply 只移除 Git 已注册且再次验证为 clean 的 managed worktree。本地 task branch、`state.json`、Journal、Outcome、Publication Record 与 `result.md` 永久不在该 Target Set 中。删除前追加 `planned` tombstone，删除后追加 `completed`；若进程在两者之间崩溃，重试只能在目标已经缺失且存在唯一 `planned` 记录时补写 `completed`，其他缺失路径一律按身份不明阻断。

## Recovery 验收测试

实施必须模拟以下崩溃点：

- Journal Append 前后；
- Worker stdout 解析期间；
- Worker Edit 后、Completion 前；
- Verification Command 期间；
- Local Commit 后、Push Result 记录前；
- Push 后、PR 创建前；
- PR 创建后、Provider ID 持久化前；
- 等待 CI 时；
- 原子替换 State Snapshot 时。

每个 Fixture 必须证明 Recovery 能幂等继续或以具体安全 Blocker 停止，且不会丢失 Patch 或重复发布。
