# ADR 0006：Attempt 控制根与业务 Worktree 分离

- 状态：已接受（Accepted）
- 日期：2026-08-04

## 背景

Worker 同时需要两类文件：业务代码，以及 Marshal 生成的 TaskSpec、Prompt、WorkerRequest、Transcript 和 WorkerResult。如果把控制文件放进任务 Worktree，它们会污染待验证 diff；如果直接开放整个 `.marshal/runs/`，Worker 又能改写冻结输入、事件、验证证据或其他 Attempt。

## 决策

每次 Attempt 使用独立控制根：

```text
.marshal/runs/<run-id>/attempts/<attempt-id>/
├── worker-request.json
├── worker-result.json
├── worktree-snapshot.json
└── control/
    ├── input/       # Marshal 写入，Adapter 策略只读
    └── output/      # 当前 Worker 可写
```

`WorkerRequest.controlRoot` 是必填的绝对路径。`taskSpecPath`、`promptPath` 和 `resultPath` 只能是相对该根目录的非逃逸路径。Worker 进程仍以业务 Worktree 为 cwd；Adapter 只允许访问当前 Attempt 的 `control/input/**` 与 `control/output/**`，并拒绝其他外部目录。Marshal 将 WorkerResult 规范化后复制到 Attempt 顶层，后续 Review 不信任控制输出中的声明。

`.marshal/` 继续由 `marshal init` 写入 Git 本地排除规则，任何控制面文件不得进入业务提交。

## 后果

- 业务 diff 不包含 Harness 中间文件；
- 一个 Worker 无权修改 Run 状态、冻结输入、Verification 或其他 Attempt；
- Local `workspace-write` 仍是合作式权限边界，不是抵抗同 UID 恶意进程的 OS Sandbox；Hardened Profile 需在后续使用容器或 VM；
- Schema 和所有 Adapter 必须显式支持 `controlRoot`，旧请求不再兼容。
