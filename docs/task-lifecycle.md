# 任务生命周期

## 目的

生命周期是 Planning、Worker 执行、Verification、Review、Publishing 和 Recovery 之间的持久化契约。自然语言消息不能改变状态；只有通过守卫的应用命令才能追加转换事件并原子更新状态快照。

## 身份

- **Task**：由 `taskId` 标识的稳定工程意图。
- **Run**：使用同一冻结规范与锁定基线的一次执行，由 `runId` 标识。
- **Attempt**：Run 内的一次 Worker 调用，由 `attemptId` 标识。
- **Review Round**：针对一个 evidenceDigest 的一次决策。

Retry 表示基础设施或 Provider 执行失败，因此创建新 Attempt。Rework 表示代码或语义未通过门禁，同样创建新 Attempt。二者都不得修改冻结的 TaskSpec。

## 状态

| 状态 | 含义 | 持久化前置条件 |
| --- | --- | --- |
| `CREATED` | Task 身份已创建 | 初始元数据 |
| `PLANNED` | TaskSpec 草案已存在 | 草案通过 Schema |
| `READY` | 输入已冻结，可以执行 | base SHA、spec/policy digest、CapabilitySnapshot、worktree Lease |
| `RUNNING` | 一个 Worker Attempt 持有 worktree | Attempt Record 与有效 Lease |
| `RETRY_PENDING` | Attempt 因可重试运行问题失败 | 失败分类与保存的 worktree 快照 |
| `VERIFYING` | Marshal 正在观察 Worker 结果 | 已完成 Attempt 与 snapshot 身份 |
| `REVIEW_PENDING` | 完整 ReviewPacket 等待语义判断 | VerificationReport 与 ArtifactManifest |
| `REWORK_REQUESTED` | 下一 Attempt 已有明确阻塞反馈 | ReviewDecision 或强制门禁失败，且预算尚存 |
| `PUBLISHING` | 已接受证据正在 commit 和发布 | 与当前 evidenceDigest 匹配的 Accept 决策 |
| `PUBLISHED` | PR/MR 已创建或更新 | 远程 Publication Record |
| `CI_PENDING` | 必需远程检查尚未结束 | 已发布变更与检查集合 |
| `ACCEPTED` | 所有必需门禁与语义 Review 通过 | 最终决策及所需发布/CI 证据 |
| `REJECTED` | 工作不合适且不再继续 | Reject 决策或 Rework 预算耗尽 |
| `BLOCKED` | 需要外部输入或能力 | 具体 Blocker Record |
| `ABORTED` | 授权操作者停止 Run（保留状态） | Abort 原因与保存的证据；v1 实现以 `BLOCKED` + `terminalReason=aborted-by-operator` 表达（ADR 0012） |
| `NO_CHANGE` | Review 确认无需仓库变更 | No-change 决策与诊断证据 |

`ACCEPTED`、`REJECTED`、`BLOCKED`、`ABORTED`、`NO_CHANGE` 是 Run 终态。解决 Blocker 或改变终态决策必须创建关联到旧 Run 的新 Run。

## 转换表

| From | To | 守卫条件 |
| --- | --- | --- |
| `CREATED` | `PLANNED` | TaskSpec 草案有效 |
| `PLANNED` | `READY` | 基线可解析、策略允许、Adapter Probe 通过、状态已冻结 |
| `READY` | `RUNNING` | 获得 Writer Lease 且 Attempt 预算尚存 |
| `RUNNING` | `VERIFYING` | Worker 协议完成，进程结果与文件系统快照已记录 |
| `RUNNING` | `RETRY_PENDING` | 失败可重试且预算尚存 |
| `RUNNING` | `BLOCKED` | 能力、认证或输入缺失，或失败不可安全重试 |
| `RETRY_PENDING` | `RUNNING` | Backoff 结束或操作者显式重试，分配新 Attempt ID |
| `VERIFYING` | `REVIEW_PENDING` | VerificationReport 与 Manifest 完整，即使强制门禁失败 |
| `REVIEW_PENDING` | `REWORK_REQUESTED` | Verdict 为 rework 且预算尚存 |
| `REVIEW_PENDING` | `REJECTED` | Verdict 为 reject 或返工预算耗尽 |
| `REVIEW_PENDING` | `BLOCKED` | Verdict 需要外部信息或权限 |
| `REVIEW_PENDING` | `NO_CHANGE` | Verdict 为 no_change 且 TaskSpec 允许 |
| `REVIEW_PENDING` | `PUBLISHING` | Verdict 为 accept、强制门禁通过且要求发布 |
| `REVIEW_PENDING` | `ACCEPTED` | Verdict 为 accept、强制门禁通过且无需发布 |
| `REWORK_REQUESTED` | `RUNNING` | 新 Attempt 获得阻塞问题与 Lease |
| `PUBLISHING` | `PUBLISHED` | 幂等发布成功 |
| `PUBLISHING` | `BLOCKED` | 凭据、授权或远程策略失败且不可重试 |
| `PUBLISHED` | `CI_PENDING` | TaskSpec 要求远程检查 |
| `PUBLISHED` | `ACCEPTED` | 无需远程检查 |
| `CI_PENDING` | `ACCEPTED` | 当前发布 head SHA 的必需检查通过 |
| `CI_PENDING` | `REWORK_REQUESTED` | 检查失败、预算尚存且可通过代码修复 |
| `CI_PENDING` | `BLOCKED` | 失败来自外部或需要维护者操作 |
| `RETRY_PENDING` | `BLOCKED` | 显式 abort（`run.aborted`，ADR 0012）：human actor、LeaseHeld、写终态 Outcome；v1 不启用 `ABORTED` 状态 |

意外进程退出不会自动创造转换。Recovery 必须先比较 Journal、Snapshot、Process Lease 与 worktree 状态，再选择合法转换。

## 强制不变量

### 冻结执行输入

进入 `READY` 时冻结：

- 规范化 TaskSpec 与 digest；
- 解析后的 base SHA；
- 有效配置和 PolicySnapshot；
- Adapter 可执行路径与 CapabilitySnapshot；
- 必需验收命令和交付物。

修改任何冻结项都会创建新 Run。Review 反馈只描述旧契约未满足的部分，不会修改规范。

### 证据绑定

VerificationReport 绑定 `runId`、`specDigest`、`baseSha` 和真实 snapshot/diff digest。ReviewDecision 绑定 ReviewPacket、VerificationReport 与 ArtifactManifest digest。Publisher 拒绝引用陈旧证据的决策。

### 单一写入者

Worker Attempt、Verifier 或 Publisher 在同一时间只能有一个持有 worktree Write Lease。可能生成文件的验证命令也必须持 Lease，并在执行后重新检查 dirty tree。

### 禁止静默豁免

存在失败强制门禁时，`accept` 不得进入发布。若仓库策略允许，维护者可以创建带版本的 Waiver Decision，明确 gate、原因、批准者、有效期或范围与 evidenceDigest；自然语言评论不能充当豁免。

## Retry 与 Rework 预算

- `maxAttempts`：Worker 总调用次数。
- `maxOperationalRetries`：Provider、协议或进程失败的重试次数。
- `maxReworkRounds`：Verification 或 Review 导致的实现循环次数。
- `runTimeoutSeconds`：Run 总 wall time。
- `attemptTimeoutSeconds`：单次 Worker 调用时间。

以最先耗尽的预算为准。无法满足代码契约时进入 `REJECTED`；外部容量或授权阻止正常尝试时进入 `BLOCKED`。

## 空变更与 No-change

真实 diff 为空时，即使 Worker 声称仓库原本正确，也不能视为成功 Coding Change。

- `allowNoChange=false`：空 diff 是验证失败。
- `allowNoChange=true`：主 Agent 只有在存在说明无需变更的诊断交付物时才能给出 `no_change`。
- `NO_CHANGE` 默认不创建 PR/MR。

## 发布后的 CI

CI 结果必须绑定精确的 published head SHA。旧 commit 的绿色检查不能满足门禁。Rework 更新 branch 后，旧检查失效，生命周期重新经过 Verification、Review、Publishing 与 `CI_PENDING`。

## 清理

Cleanup 不是状态转换，也不能销毁 Outcome Bundle。

- Accepted worktree 可在发布记录和 patch digest 持久化后删除。
- Rejected、Blocked 或 Aborted 的 dirty worktree 保留到显式归档或清理。
- 清理前必须重新检查 diff；存在新未归档文件时默认拒绝。
