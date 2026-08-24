# I186-R0 Golden Business Trace 与 Normalized Trace Schema

更新时间：2026-08-24

本文按 Planning Baseline v3 的 R0 步骤 2–3，采集当前主路径的 golden business trace，并冻结 old/new normalized trace schema，供 R1 纵切与 R5 strangler cutover 做逐字段对比。Baseline 事实见 [i186-r0-baseline-report.md](i186-r0-baseline-report.md)。

## 1. 当前主路径（old path）

当前唯一业务写路径为 embedded/local CLI 链：

```text
CLI `marshal task run --run RUN_ID`
→ internal/cli/cli.go runTaskWorker（核验冻结 CapabilitySnapshot、runstore.Inspect、plan approval、embedded sandbox/provider 选择）
→ internal/execution/service.go execution.Run（Attempt 控制根、lease、fencing、worktree observer）
→ port.WorkerAdapter.Run(host subprocess)（Adapter 在宿主直接启动 Agent 进程）
→ verification（独立 verifier：gate 重放、diff/snapshot observe、artifact 校验）
→ review（fresh ReviewPacket + 独立 reviewer + ReviewDecision）
→ publication（仅在 Policy 要求时；维护者直接 push main 的本地闭环记录 sourceHead/localMergeSha/pendingRemoteSync）
```

结构性事实（R1 纵切必须覆盖的 authority 触点）：

- 状态权威在 Run journal/snapshot（`.marshal/runs/<run-id>/`），由 `execution.Run` 与 CLI lifecycle 命令写入；Supervisor/API 存在直接编排 Store 的历史路径（Issue #186 P1 缺口）。
- Worker 结果由 `Adapter.Run` 返回边界直接转换为权威事实（ADR 0036 只对返回边界做 failure 归一化），没有独立的 untrusted-observation → admission 门禁。
- Sandbox provision 与 Agent 执行尚未绑定：embedded local profile 下 Agent 进程运行在宿主，无 allocation/generation 与 Sandbox 关联证据。

## 2. Golden trace 样本

样本 Run：`run-m10-wire-r1`（Task `M10-WIRE-01`，`ACCEPTED`，含 rework→accept 全链，adapter qoder，baseSha `5895122`）。事件序列（`.marshal/runs/run-m10-wire-r1/events.jsonl`，10 条 RunEvent）：

| sequence | type | attempt（尾 8 位） | 关键 payload |
| --- | --- | --- | --- |
| 1 | `planning.spec-accepted` | — | specDigest `sha256:2492e622…` |
| 2 | `planning.inputs-frozen` | — | adapterId qoder、baseSha、branch、policyDigest、capabilityDigest、worktreePath |
| 3 | `worker.started` | `9a9fa650` | adapterId qoder |
| 4 | `worker.completed` | `9a9fa650` | diffDigest `sha256:96fb83ed…` |
| 5 | `verification.completed` | — | gate 全量重放 |
| 6 | `review.rework` | — | verdict rework、decisionDigest `sha256:3a04d10e…` |
| 7 | `worker.started` | `9e09229f` | rework attempt |
| 8 | `worker.completed` | `9e09229f` | diffDigest `sha256:b0a003b1…` |
| 9 | `verification.completed` | — | — |
| 10 | `review.accept` | — | verdict accept、decisionDigest `sha256:5b3afa9c…` |

补充样本：`run-docs-compat-r3`、`run-m13-neg-r3`（ACCEPTED）；`run-m10-wire-02-r2`（qodercli `1.1.28` 首个写任务，含 verify 中断→幂等恢复→review rework 链，作为 R4 恢复模型 fixture）。

## 3. Old normalized trace schema

对 old path 的每条 business step 归一化为：

```json
{
  "authorityNamespaceId": "local-embedded/<repo-root>",
  "taskId": "M10-WIRE-01",
  "runId": "run-m10-wire-r1",
  "attemptId": "attempt:…",
  "command": {"kind": "attempt.start|attempt.result|verify|review|publish", "origin": "cli|api|supervisor", "commandId": null},
  "lease": {"owner": "execution.Run", "generation": 1, "fencingToken": null},
  "allocation": {"sandboxProvider": "none", "allocationId": null},
  "agentRegistration": {"adapterId": "qoder", "registrationId": null, "capabilityDigest": "sha256:…"},
  "sandboxRegistration": {"providerId": null, "registrationId": null},
  "resultCapability": {"drcId": null, "correlationId": null},
  "digests": {"spec": "…", "diff": "…", "verification": "…", "decision": "…"},
  "sequence": 3,
  "timestamp": "2026-08-22T…Z"
}
```

old path 的固定取值：`command.origin=cli`、`command.commandId=null`（无 durable command 账本）、`allocation.sandboxProvider=none`、`resultCapability.drcId=null`（结果不经 DRC-bound ingress）。

## 4. New normalized trace schema（R1 目标）

R1 纵切完成后，同一条 business chain 必须投影为：

```json
{
  "authorityNamespaceId": "(tenantNamespace, controlPlaneId, authorityScopeId)",
  "taskId": "…", "runId": "…", "attemptId": "…",
  "command": {"kind": "attempt.start", "origin": "cli-transport-adapter", "commandId": "cmd:…", "requestDigest": "sha256:…", "expectedSequence": 7},
  "lease": {"owner": "kernel", "generation": 2, "fencingToken": "…"},
  "allocation": {"sandboxProvider": "local", "allocationId": "alloc:…", "stageDigest": "sha256:…"},
  "agentRegistration": {"providerId": "agent:qoder", "registrationId": "reg:…", "capabilityDigest": "sha256:…", "attestationDigest": "sha256:…"},
  "sandboxRegistration": {"providerId": "sandbox:local", "registrationId": "reg:…", "snapshotDigest": "sha256:…"},
  "resultCapability": {"drcId": "drc:…", "correlationId": "attemptId+allocationId+generation"},
  "digests": {"spec": "…", "diff": "…", "verification": "…", "decision": "…"},
  "sequence": 3,
  "timestamp": "…Z"
}
```

对比规则（R5 cutover 使用）：

1. `taskId/runId/attemptId/sequence/digests` 在 old/new 必须逐字段相等（业务事实不变）；
2. `command.commandId`、`allocation.allocationId`、`resultCapability.drcId` 从 null 变为非空且可 current-ledger recheck；
3. `agentRegistration` 与 `sandboxRegistration` 必须是两条独立 registration，相同 trustDomainKind 不自动授权；
4. old/new 任一字段语义变化必须显式列入 trace diff 报告，未解释 diff 阻断 cutover。

## 5. 非目标

本文不设计 CommandBus 平台、不迁移 command type、不实现 ResultIngress；这些分别属于 R2 与增量 ADR 0043–0045 的实现阶段。
