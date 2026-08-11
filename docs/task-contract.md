# 任务契约

## 契约层级

Marshal 使用四层独立契约：

1. **TaskSpec**：一个 Run 的不可变意图和策略。
2. **WorkerRequest / WorkerResult**：Provider 无关的 Attempt 边界。
3. **VerificationReport / ArtifactManifest**：独立观察到的证据。
4. **ReviewPacket / ReviewDecision**：有边界的语义审查输入，以及绑定到该输入的决策。

JSON Schema 位于仓库的 [`schemas/`](https://github.com/chiga0/marshal-harness/tree/main/schemas)。当前版本为 `v1alpha1`：在文档阶段和首条纵向链路期间允许不兼容调整，但所有持久化记录都必须携带版本。

## TaskSpec

### 身份字段

- `apiVersion`：`marshal.dev/v1alpha1`。
- `kind`：`Task`。
- `metadata.id`：稳定且可读的任务身份。
- `metadata.title`：简短任务名称。

`taskId` 必须可安全用于日志和 branch 派生。Marshal 另行创建不透明 `runId`，调用者不得复用 Run ID。

### Repository

- `path`：调用者提供、由 Marshal 规范化的仓库路径。
- `baseRef`：待解析的 branch、tag 或 revision。
- `remote`：发布 remote，通常为 `origin`。
- `expectedRemoteUrl`：不发布时可选；要求发布时必须冻结，用于阻止仓库 Local Git Config、`url.*.insteadOf` 或 Remote 漂移把 Push 重定向到其他仓库。

进入 `READY` 前只解析一次 `baseRef`，完整 `baseSha` 写入 Run Metadata。可移动的 branch 名不具权威性，解析后的 SHA 才是事实。

### Work Definition

- `objective`：可验证结果，而不是实现猜测列表。
- `context`：Worker 所需事实或引用。
- `nonGoals`：明确排除的相邻工作。
- `constraints`：兼容性、行为、依赖或设计约束。

Secret 不能写入任务上下文。Task 可以引用 Credential Profile 名称，但 Secret Value 只能在获授权组件内部解析。

### Scope

无论宿主 OS 是什么，路径统一为仓库相对 POSIX Path，并使用 `doublestar/v4` 的确定性 Glob 语义，其中 `**` 跨目录递归匹配。规范化过程拒绝反斜杠、绝对路径、`..`、NUL 和任何解析到 worktree 外的路径。

- 写任务必须指定 `allowPaths`。
- `denyPaths` 始终优先于 allow。
- Rename 的 source 和 destination 都必须校验。
- Submodule Change 默认禁止。
- `maxChangedFiles` 与 `maxDiffBytes` 限制意外扩展。

### Acceptance Command

命令使用 argv 数组，而不是 Shell String：

```json
{
  "id": "unit-auth",
  "argv": ["npm", "test", "--", "auth"],
  "cwd": ".",
  "timeoutSeconds": 300,
  "required": true
}
```

Marshal 直接 Spawn 可执行文件，不解释 Shell 语法、命令替换、重定向或管道。需要管道的仓库应提供已签入脚本，并在 argv 中显式调用。

命令使用过滤后的环境。报告记录可执行文件解析结果、cwd、起止时间、退出状态或 Signal、截断情况和日志交付物引用。

### Deliverable

每个交付物包含：

- 稳定 `id`；
- `kind`，如 `code`、`test`、`documentation`、`report`、`binary` 或 `publication`；
- 是否 `required`；
- 文件交付物的可选仓库路径 Glob；
- 可选 Media Type 与最小数量。

Artifact Collector 记录真实文件与摘要。Worker 声明只有在采集成功后才能满足交付物要求。

### Worker Policy

- `preferredAdapter`：稳定 Adapter ID。
- `fallbackAdapters`：显式顺序，默认空。
- `executionProfile`：`read-only`、`workspace-write` 或 `hardened`。自 [ADR 0017](adr/0017-provider-neutral-sandbox-contract.md)（已接受，2026-08-10）起，该字段保留为兼容面，Runtime 阶段按固定映射解析为 `AccessMode × AssuranceLevel`：`read-only` → `read-only × workspace-write`、`workspace-write` → `workspace-write × workspace-write`、`hardened` → `workspace-write × hardened`；二维字段随 M8 落地 Schema，历史持久记录不重写。
- 可选 Model 与 Reasoning Selector，仅在 Adapter 支持时传递。
- `sessionPolicy`：`ephemeral`、`persist` 或 `resume`。

Capability Probe 必须拒绝不兼容请求，不能忽略不支持的安全或输出选项。

### Budget

预算覆盖 Run 总时间、Attempt 时间、Attempt 次数、Operational Retry、Rework Round、输出字节和可选验证日志字节。Provider Native Limit 可以更严格，但不得扩展 TaskSpec 预算。

### Publication

- `required`：验收是否必须包含 PR/MR。
- `provider`：首版 `github`，后续 `gitlab`。
- `mode`：`draft` 或 `ready`。
- `remote` 与 `baseBranch`。
- `mergePolicy`：默认 `never`。
- 可选的 Required Check 名称。

Publisher 从 taskId 派生 head branch，并在首次创建后保存 Forge 的不可变 Publication ID。

## 规范化与 Digest

进入 `READY` 前，Marshal：

1. 校验草案 Schema；
2. 应用有文档的默认值；
3. 规范化路径和无顺序语义的列表；
4. 序列化 Canonical JSON；
5. 计算 SHA-256 `specDigest`；
6. 将 Canonical Document 以只读方式保存到 Run Bundle。

作者输入可以是 YAML 或 JSON，但持久化 TaskSpec 必须是 Canonical JSON。`baseSha`、Executable Path、Policy Digest 等环境解析值属于 Run Record，不属于作者输入。

## TaskSpec 示例

```yaml
apiVersion: marshal.dev/v1alpha1
kind: Task
metadata:
  id: ENG-123
  title: 防止重复刷新 Token
repository:
  path: /work/auth-service
  baseRef: main
  remote: origin
work:
  objective: 并发调用者共享同一个进行中的 Token Refresh。
  context:
    - 问题可在 auth 单元测试中复现。
  nonGoals:
    - 不修改公开认证 API。
  constraints:
    - 不新增 Runtime Dependency。
scope:
  allowPaths:
    - src/auth/**
    - test/auth/**
  denyPaths:
    - src/auth/generated/**
  maxChangedFiles: 12
  maxDiffBytes: 50000
acceptance:
  commands:
    - id: auth-tests
      argv: [npm, test, --, auth]
      cwd: .
      timeoutSeconds: 300
      required: true
  allowNoChange: false
deliverables:
  - id: implementation
    kind: code
    required: true
    pathGlob: src/auth/**
  - id: regression-test
    kind: test
    required: true
    pathGlob: test/auth/**
  - id: pull-request
    kind: publication
    required: true
worker:
  preferredAdapter: qwen
  fallbackAdapters: [opencode, pi]
  executionProfile: workspace-write
  sessionPolicy: persist
budgets:
  runTimeoutSeconds: 3600
  attemptTimeoutSeconds: 1200
  maxAttempts: 4
  maxOperationalRetries: 1
  maxReworkRounds: 2
  maxOutputBytes: 20000000
publication:
  required: true
  provider: github
  mode: draft
  remote: origin
  baseBranch: main
  mergePolicy: never
```

## WorkerRequest

WorkerRequest 绑定 Run、Attempt、TaskSpec/Policy/Capability Digest、base SHA、worktree、Adapter、执行配置、预算、Prompt Path、Result Path 和返工问题。它是持久化跨组件记录；完整任务内容来自其引用的冻结 TaskSpec。

## WorkerResult

WorkerResult 包含：

- Run 与 Attempt 身份；
- Adapter、Binary、Session 身份；
- 完成状态与自然语言摘要；
- 声明的变更文件、交付物、已尝试命令、风险和 Blocker；
- 可选 Usage Data。

不可信事实统一使用 `declared*` 命名。报告不得提供看似权威的 `testsPassed` 或 `changedFiles` 字段。

## VerificationReport

VerificationReport 按 Repository、Scope、Diff、Command 和 Artifact 分组记录真实 Gate。每个 Gate 有稳定 ID、required、status 和 evidence。总状态机械计算：

- `pass`：全部 Required Gate 通过；
- `fail`：至少一个 Required Gate 明确失败；
- `error`：无法产生可信结果；
- `cancelled`：授权取消中断验证。

## ReviewPacket 与 ReviewDecision

ReviewPacket 对被审查的 TaskSpec、snapshot、diff、VerificationReport、ArtifactManifest 和 WorkerResult 进行 Content Addressing。ReviewDecision 必须引用精确 `reviewPacketDigest` 与 `evidenceDigest`。

Verdict 包括：`accept`、`rework`、`reject`、`blocked`、`no_change`。

Finding 使用稳定 ID 与 `P0`–`P3` 严重级别。Blocking Finding 必须包含具体 Required Outcome。Accept 决策必须没有 Blocking Finding，并引用通过的强制验证。

## 兼容性规则

- Reader 拒绝未知 Major Contract Version。
- 未知扩展字段只允许出现在预留 Namespace。
- 持久化记录不得就地“升级”；Migration 创建 Derived Record 并保留原记录。
- Adapter Native Event 可以独立演进，Normalized Event 才是持久边界。
