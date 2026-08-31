# ADR 0071：Darwin sealed completion 与耐久 ResultCapability

- 状态：已接受（Accepted，2026-08-31）
- 提议基线：`main@0f5aca0d0c1e85db2505293bb9b9bb5851ebd187`
- 关联：[ADR 0044](0044-result-ingress-and-cold-hot-paths.md)、[ADR 0056](0056-darwin-process-observation-and-attempt-terminalization.md)、[ADR 0067](0067-darwin-ordinary-user-launch-and-attach-recovery.md)、[ADR 0068](0068-mac-first-cli-only-lifecycle-preview-rc1.md)、[ADR 0069](0069-attempt-reservation-and-existing-worktree-allocation.md)、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)。

## 背景

fixed CLI 的 sealed READY 路径已经能以真实 Pi 0.84.4 完成 `PrepareRunStart → StartPreparedRun`，但只把 Run 推进到 `RUNNING`。当前实现存在两个必须同时关闭的生产断点：

1. fresh start 在 `resume` outcome 耐久后断开 Supervisor client；ADR 0067 的 borrowed `Attach` 只允许 `Observation` 与 `bind-authority(owner-successor)`，没有 terminal collection/cleanup 闭集，因此 Core 无法收集真实 transcript；
2. sealed dispatch 直接 mint claimed lease，没有使用 ADR 0069 的 `ClaimReserved`，因而没有在同一耐久 claim fact 中签发 `DispatchResultCapability`。CLI 若临时构造 DRC，会退回由调用者拼装自洽 facts 的旧错误，不能构成 current-ledger recheck。

Pi production argv 已冻结为 path-free result transport：final assistant output 是唯一 WorkerResult JSON object，真实结果只存在于 Supervisor 持有的 JSONL stdout 中。不能重新引入 worker-controlled result path，也不能绕过 ResultIngress 直接追加 `worker.completed`。

## 决策

### 1. 一个 release-critical completion 纵切

fixed CLI 只能通过 `PublicApplicationPort` 调用一个高层 completion 操作。其唯一顺序为：

```text
RUNNING current authority
  → bounded Collect retry（每次 creation-once intent/outcome）
  → descriptor-bound transcript read
  → Pi JSONL final WorkerResult extraction
  → durable DispatchResultCapability current-ledger recheck
  → ResultIngress admission（绑定 exact successful Collect outcome）
  → terminalization barrier
  → process terminal observation
  → allocation release
  → Supervisor close
  → cleanup-completed
  → lease-released
  → runstore worker.completed（RUNNING → VERIFYING）
```

任一步失败都保留已耐久前缀并返回 typed recovery-required；不得跳步、伪造后继或使用 legacy execution fallback。

### 2. borrowed Attach 的闭集 continuation

本 ADR 部分修订 ADR 0067 §4：`AttachedSession` 可在同一 callback、同一 goroutine、同一已认证 connection 上执行**一个**已耐久 prepared command，但 exported surface 仍是命令语义闭集，而不是 generic client：

- `ExecutePreparedBindAuthority`；
- `ExecutePreparedCollect`；
- 后继 terminalization 实现可增加 `ExecutePreparedInspect`、`ExecutePreparedTerminate`、`ExecutePreparedClose`，每项都必须在本 ADR 的 architecture test 白名单中显式列出。

每个 borrowed callback 最多执行一个 command；command 前必须先消费 `Observation`，prepared evidence 的 pre-anchor、command kind、sequence/head、authority head 与 durable intent 必须精确一致。callback 返回后 session 失效。不得暴露 connection、codec、nonce、raw Request、generic `DoPrepared` 或可跨 callback 复用的 client。

仍在运行时的 `Collect` 返回 rejected outcome；该 outcome 也必须先耐久落账并推进 command chain。controller 只可用新 command ID/sequence/head 做有界重试，不得重放一个已经得到明确 rejected receipt 的 request。successful `Collect` 自带 terminal ProcessReport；admission 前不得抢先写 terminalization barrier。

### 3. path-free Pi result transport

Core 必须对完整 bounded JSONL 运行既有 Pi protocol state machine，再从最终 `willRetry=false` 的 `agent_end` 最后一条 assistant message 中提取结果：

- 允许任意个 `thinking` content；
- 必须且只能有一个非空 `text` content；
- text 必须只包含一个 JSON object，禁止 markdown fence、前后说明、第二个 JSON 或 tool call；
- WorkerResult schema、task/run/attempt、adapter id 与 transcript session 必须精确匹配；
- executable/version/model、session、started/completed 与 usage 由 Marshal 观察值覆盖，worker 声明不是 authority。

Supervisor transcript manifest、stdout/stderr 对象、长度与 digest 仍由 descriptor-bound `ReadCollectedTranscript` 重验；解析器不得读取 worker-controlled path。

### 4. reserved claim 与 ResultCapability

fresh sealed attempt 必须改用 `dispatch.Matcher.ClaimReserved`。同一 `attempt-reserved` fact、canonical provider registration/snapshot/evidence、requirements、allocation、deadline 与 target actor 产生一个 creation-once `reserved-claimed` fact；该 fact 同时持久保存 exact `DispatchLease` 与 `DispatchResultCapability`。

composition 必须持有并读取真实 provider registration authority。不得从 WorkerResult、task capability digest 或 CLI 常量临时伪造 registration/snapshot/evidence。exact replay 必须从 lease ledger 恢复 same-bytes claim/result capability，并把 capability 重建进当前 `EdgeRuntime`。

admission 时 Core 必须先通过当前 `EdgeRuntime` 重验：edge active、lease generation/fencing active、target registration/snapshot/evidence eligible、attempt/allocation/operation 精确匹配；随后才能从同一 durable claim 投影 ResultIngress 的兼容 DRC/LedgerBinding。DRC 的 per-delivery command/idempotency/request digest/nonce 只能绑定此次 WorkerResult digest 与 sequence，不得改变 claim authority。

### 5. 生命周期提交边界

successful admission 不是 `worker.completed`。只有 ADR 0056 cleanup 链全部耐久且 worktree snapshot 已由 Core 观察后，runstore 的专用 completion seam 才能幂等追加 `worker.completed` 并把 `RUNNING → VERIFYING`。generic journal append 与 CLI 私有拼接继续拒绝该转换。

## 负面与恢复矩阵

- running `Collect`：耐久 rejected receipt，有界新 command 重试；不得 barrier-first；
- transcript object/content/digest/length/session 漂移：拒绝 admission，保留 supervisor/attempt authority；
- final assistant 零文本、多文本、tool call、尾随 JSON、identity/session 漂移：拒绝；
- claim 缺失、registration/snapshot/evidence 漂移、edge 未签发/已撤销、lease generation/fencing 变化：拒绝并 quarantine；
- command response loss：只重建 exact pending prepared command，由同一 Supervisor journal判定 replay；
- admission response loss：按 idempotency key 与 result digest replay唯一 AdmissionFact；
- barrier 与 admission 竞态：共享 CAS，barrier-first 永久关闭 admission；
- cleanup 或 run completion response loss：各自只查所属 durable ledger，禁止重复 side effect；
- callback 逃逸、第二 command、错误 command method、跨 goroutine：fail closed 且 session poisoned。

## 影响

该决策增加一个受限的 Attach trust-boundary surface 和一个真实 provider/edge authority producer chain，但没有放宽 ordinary-user profile，也不提供 hostile same-UID、hardened、Linux 或 stable release 保证。完成本 ADR 只关闭 RC1 的 terminalization/result 纵切；RC1 仍需真实 Pi Run、独立 ReviewDecision `ACCEPTED`、same-bytes canary 与 release-contract 门禁。
