# ADR 0036：`Adapter.Run` 边界无类型失败确定性 Fail-Closed

- 状态：已接受（Accepted，2026-08-20）
- 日期：2026-08-20
- 关联：[ADR 0019](0019-deterministic-control-plane-typed-execution-and-goal-admission.md)、[ADR 0042](0042-mac-ordinary-user-adapter-mode.md)

## 背景

Core 已用封闭 `AdapterFailure` 表达 Provider 的失败类别与重试权限，但历史 Adapter 仍可能从 `Adapter.Run` 返回普通 Go error 或 legacy `port.Permanent`。若 Core 把普通 error 默认解释为 transient，协议漂移、结果缺失或 Adapter 实现错误会消费下一次 Attempt，重复启动 Worker，却没有新的可恢复信息。若 Core 解析 provider 自由文本猜测类别，又会让日志、路径或 credential 内容影响生命周期权威，并可能把敏感原始 cause 持久化。

这项选择会改变失败后的持久状态与后续启动行为，因此必须显式冻结，而不能继续作为向后兼容细节。

## 决策

### 1. 唯一适用边界

本决策只适用于 `Adapter.Run` 返回错误的精确边界。Core 在错误进入通用 `recordFailure` 前执行一次确定性归一化：

- 普通、非 `port.Permanent`、且不能归一化为唯一合法 `AdapterFailure` 的错误，转换为 `protocol-invalid/do-not-retry`；
- legacy `port.Permanent` 且不能归一化为唯一合法 `AdapterFailure` 的错误，转换为 `provider-terminal/do-not-retry`；
- 唯一且合法的 typed `AdapterFailure` 原样进入既有分类器；
- 非法、歧义或多 carrier 的 typed error graph 继续按既有闭合分类器归一化为 `protocol-invalid/do-not-retry`。

Core 内部其它复用 `recordFailure` 的来源不受本 ADR 影响。例如 typed-edge 结果复核、Core-owned operational failure 等仍按各自现行分类和预算语义处理；不得把本 ADR 扩展成所有无类型 Core error 的全局终态规则。

### 2. 生命周期与持久化

上述两个兼容归一化结果都在首个失败 Attempt 进入 `BLOCKED`，不增加 `operationalRetriesUsed`，并继续使用现有 append-only `worker.failed`、terminal Outcome、quarantine transaction 与 `failureSignature` 契约。终态 Run 重启时必须在调用 Adapter `Probe` 或 `Run` 前返回已持久化的 `BLOCKED` 状态；不得以进程重启重新取得一次 Worker 执行机会。

本 ADR 不增加状态、事件或 Schema 字段。它冻结的是既有字段的权威解释：只有合法 typed `retryable` 能授权 operational retry；缺少 typed carrier 不是 retry authority。

### 3. 信息边界

Core 不解析原始 error 文本来决定 kind，也不得把 Adapter 原始 cause、路径、credential、secret 或控制字符写入返回错误、事件、Outcome 或安全摘要。持久化与调用方只能观察 Core 构造的封闭 `AdapterFailure`、终态 reason 和既有 `failureSignature`。

### 4. 迁移与兼容

- 需要重试语义的 Adapter 必须显式返回构造器校验通过的 typed `AdapterFailure`；普通 error 不再获得历史默认重试。
- legacy `port.Permanent` 仍保持“不可重试”含义，但以 `provider-terminal/do-not-retry` 进入统一持久证据。
- 历史 journal、Outcome 与终态不重写、不重新解释；新行为只作用于升级后新发生的 `Adapter.Run` 失败。
- 已处于 `BLOCKED` 的 Run 不因 Adapter 后续升级而复活。修复 Adapter 后应从当前权威基线创建新 Run。

这是一项有意的 fail-closed 兼容性收紧：旧 Adapter 若依赖普通 error 自动重试，必须先升级为 typed contract，不能静默降级门禁。

## 回滚

不得通过重新解释历史事件或重新打开既有 `BLOCKED` Run 回滚。若实际运行证据证明该决策需要改变，必须由后续 ADR 明确新的重试 authority、迁移与恢复规则；代码回退也只能影响新的 Run。紧急止损应禁用有缺陷的 Adapter，或修复其 typed failure 映射后创建 fresh-base successor。

## 被拒绝的方案

- **继续让普通 error 默认 retryable**：会让结构性失败重复消费 Attempt 与 token，且缺少可审计的重试授权。
- **按错误文本猜测 transient/permanent**：自由文本不稳定，也可能包含路径或 secret，不能成为生命周期 authority。
- **把所有进入 `recordFailure` 的无类型错误一律终态化**：超出已证明的 Adapter 边界，会改变 Core 内部既有 operational retry 语义。
- **静默把无类型错误包装成 `connection-failure/retryable`**：只是保留旧歧义，无法关闭无信息增益的重复执行。

## 验收门禁

- 普通 error 与 legacy `port.Permanent` 分别固定为 `protocol-invalid` 和 `provider-terminal`，两者均为 `do-not-retry`；
- 合法 typed failure 保持不变，非法/歧义 typed graph 继续 fail closed；
- 首次失败、append-only 证据、Outcome、restart-before-Probe/Run 与不泄露原始 cause 均有定向测试；
- Core 的其它 `recordFailure` 来源保留现有 retry 行为；
- 定向测试、相关 race、vet/staticcheck、文档与 diff 检查、secret scan 和 merge-tree 全部通过，且真实 diff 经独立 reviewer 确认 P0/P1 清零。
