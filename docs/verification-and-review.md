# 验证与审查

## 两个不同问题

Marshal 将机械验证与语义审查分开：

- **Verification**：仓库和进程真实发生了什么？
- **Review**：真实变更是否正确满足工程目标？

Verifier 是确定性组件，不调用 LLM。主 Agent 基于有边界、绑定 Schema 的 ReviewPacket 做 Review。

## Verification Input

- 冻结 TaskSpec 与 `specDigest`；
- 有效 PolicySnapshot；
- 锁定的 `baseSha`；
- 当前任务 worktree；
- 已完成 WorkerResult 与 Attempt Metadata；
- 可选 Baseline Diagnostic。

验证首先记录精确的 worktree snapshot、index、untracked file 和相关 Git 配置。不得信任 Worker 声明的文件列表。

## Gate 顺序

便宜的结构化 Gate 先于昂贵命令执行。

### 1. Repository Integrity

- Worktree 属于预期 Git Common Directory；
- `baseSha` 仍存在且是预期祖先；
- 没有未完成 Merge/Rebase；
- 没有未授权 Nested Repository 或 Submodule Mutation；
- 没有 Conflict；
- Artifact Collection 不沿 Symlink 逃出 worktree。

### 2. Observed Change

- 计算相对 `baseSha` 的 staged、unstaged 与 untracked 变更；
- 记录 Binary-safe Patch 或等价 Object Reference；
- 计算文件数、diff byte 和 snapshot/diff digest；
- 识别 File Type、Rename、Mode Change 与 Submodule。

### 3. Scope

- 所有路径匹配 `allowPaths`；
- 没有路径匹配 `denyPaths`；
- Rename 两端都被允许；
- 文件数与 diff size 未超限；
- Generated/Vendor Area 满足仓库策略（由 `allowPaths`/`denyPaths` glob 判定）。

Scope Gate 的判定输入**只有**路径字符串、变更计数、diff 字节数、submodule 与 symlink 逃逸；它不解析 patch 内容。

> **当前未实现（跟踪见 [Issue #86](https://github.com/chiga0/marshal-harness/issues/86)）**：Unexpected Deletion 与 Executable-bit Change 尚未参与 Scope Gate 判定。第 2 节的 Diff 观察确实记录了每个变更的 `status`（含删除 `D`）与 Mode Change，但两者都未被任何 Gate 消费。因此测试文件删除、测试函数删除与新增 skip 类标记（`t.Skip`、`@pytest.mark.skip`、`xfail`、`.only`）**不会被机械拦截**，只要路径落在 `allowPaths` 内且未超文件数与 diff 上限；当前对这类改动的唯一防线是 Reviewer 的语义审查。本节此前声称这两项「被显式报告」，与实现不符，已更正为如实描述，不表示放弃该门禁——是否实现由 Issue #86 决定。

### 4. Deliverable

- Required Path Glob 达到最小数量；
- 文件是 Regular File 或显式支持的 URI；
- 记录 Size、Media Type 与 SHA-256；
- 拒绝重复 Logical Artifact ID；
- Publication Deliverable 在 Publisher 运行前保持 `expected`。

### 5. Acceptance Command

- 按声明顺序执行精确 argv；
- Direct Spawn、过滤环境、限制 cwd；
- 执行 Per-command 与 Verification-wide Timeout；
- 捕获 stdout/stderr，记录 Truncation；
- 记录 Exit Code、Signal、Start/End、Duration 与 Executable Resolution；
- 每个可能生成文件的命令执行后重新 Snapshot。

Verification Command 修改仓库时不得静默并入 Worker Output。Policy 只能明确选择：

- 让 Dirty-verifier Gate 失败；
- 采集已声明路径中的已知生成物；
- 执行显式 Cleanup Command 后重新 Snapshot。

默认规则是未声明的 Verifier Mutation 直接失败。

## Baseline Diagnostic

仓库可能已有失败。命令可通过 `baselinePolicy` 请求基线诊断：

- `none`：不在 Base 上运行；
- `on-failure`：Changed Worktree 失败时才在干净 Base Worktree 运行；
- `always`：Base 与 Changed Worktree 都运行。

Baseline 只用于分类，不会把失败的 `exit-zero` Gate 变成通过。若契约是“已有失败但不能回归”，必须由后续契约版本定义专门 Comparison Gate，不能根据日志相似度猜测。

## Gate Result

```json
{
  "id": "command:unit-auth",
  "category": "command",
  "required": true,
  "status": "pass",
  "summary": "退出码为 0，耗时 18.2 秒",
  "evidence": ["artifact://logs/unit-auth.stdout"]
}
```

合法 Status：`pass`、`fail`、`error`、`skipped`、`cancelled`。Required Gate 的 `skipped` 不等于通过；`error` 表示无法做可信判断，因此阻止 Accept。

## Overall Status

- `pass`：全部 Required Gate 通过。
- `fail`：至少一个 Required Gate 明确失败。
- `error`：至少一个 Required Gate 无法可靠判断。
- `cancelled`：验证被授权取消。

Optional Gate 失败仍会进入 Review，并可能导致语义拒绝。

## ReviewPacket

默认包含：

1. Objective、Non-goal、Constraint 与 Scope；
2. base 与 snapshot 身份；
3. 有边界时的完整 Text Diff，否则提供 Patch Artifact 和 Structured File Summary；
4. VerificationReport；
5. ArtifactManifest；
6. Worker 摘要、声明、Blocker 与 Risk；
7. 历史 Blocking Finding 与本轮增量 diff；
8. ReviewDecision 必须引用的 evidenceDigest。

Raw Reasoning、Partial Message Delta 和完整 Tool Transcript 默认排除。证据不足时，Reviewer 可以请求特定已保存日志。ReviewPacket 必须通过 Schema 并进行 Content Addressing，ReviewDecision 记录其 Digest，防止决策被复用到其他 Packet。

## Review 职责

主 Agent 检查：

- 相对 Objective 的行为正确性；
- 隐藏的 Scope Expansion 与意外 API Change；
- Error Handling、Concurrency、Data Integrity 与 Security；
- Test 与 Documentation 是否充分；
- Compatibility 与 Migration Impact；
- 与任务规模相称的 Maintainability；
- Optional Verification Warning 是否可接受；
- Required Artifact 是否真正有用。

Finding 应引用 File、Line、Gate 或 Artifact ID。没有实质影响的偏好只能作为 Non-blocking Finding。

## Finding Severity

| 级别 | 含义 | 默认效果 |
| --- | --- | --- |
| `P0` | 可能造成灾难性丢失、入侵或大面积不可用 | Reject 或立即 Rework |
| `P1` | 实质 Correctness、Security、Compatibility 或 Required Scope 缺陷 | 阻止 Accept |
| `P2` | 重要 Maintainability、Resilience 或 Test 弱点 | 通常 Rework；策略可设为非阻塞 |
| `P3` | 轻微改进或样式问题 | 非阻塞 |

Blocking Finding 必须描述 Required Outcome，但不必强制指定具体实现。

## Verdict Guard

### Accept

仅在以下条件同时满足时允许：

- VerificationReport 为 `pass`；
- 没有 Blocking Finding；
- Required File Artifact 存在；
- Decision 引用当前 ReviewPacket 与 evidenceDigest；
- Publication Policy 可满足或无需发布。

### Rework

需要至少一个 Blocking Finding 或失败 Gate，且预算尚存。下一 Attempt 接收 Finding，但冻结 TaskSpec 始终优先。

### Reject

用于方案不合适、范围不应继续或预算耗尽。Rejected Work 保留到显式清理。

### Blocked

只用于具体缺失能力、输入、授权或外部条件，且必须说明 Unblock Owner。

### No change

需要 `allowNoChange=true`、真实 diff 为空，并有支持结论的诊断交付物。

## Rework Review

Rework 不能只审查 Worker 解释。Marshal 重新生成完整证据，同时提供相对上次 Review Snapshot 的 Focused Diff。只有新证据证明问题关闭时，Finding 才能关闭。

## Remote CI

本地验收命令是发布前 Gate。Required Remote CI 是绑定到 pushed head SHA 的发布后 Gate。Marshal 记录 Provider、Check Name、External ID、Status、Conclusion、URL 与 Observation Time。

可通过代码修复的 CI Failure 进入 Rework；基础设施故障进入 `BLOCKED`，不得驱动猜测性改码。

## 日志处理

- 在配置字节限制内保存完整日志。
- 截断时保存有界的首尾内容、遗漏字节数和可用的 Stream Hash。
- 持久化前识别并脱敏 Secret，但承认 Redaction 只是纵深防御。
- PR Body 只包含摘要和引用，不包含原始大日志。
- Outcome Bundle 记录有效 Retention Policy。
