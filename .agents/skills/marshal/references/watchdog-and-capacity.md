# Watchdog、恢复与容量

> **何时必须读取：** 每个 heartbeat、后台 `task run`、Run 卡住/无响应/driver 中断、需要 supervise/recovery、增加或降低并发、fan-out、多编排并存或判断进程归属时，必须完整读取。

## 每轮顺序

先运行只读 watchdog：

```bash
MARSHAL_WATCH_NOTIFY=0 scripts/marshal-watch.sh --once --json
```

review rework lineage 的 ReviewDecision 必须通过同源 Marshal Core 的
`contract validate --schema review-decision`（含 semantic validation）。脚本优先使用
`./bin/marshal`，否则使用 `PATH` 中的 `marshal`；需要锁定其它同源构建时显式设置
`MARSHAL_WATCH_MARSHAL_BIN`。Validator 缺失、执行失败或执行期间 identity 漂移时，
相关 lineage 一律 fail closed，不回退到 watchdog 自行复制的规则。

正在推进 Goal 时应显式提供 `MARSHAL_WATCH_COHORT_FILE`（`goalId + runIds`）。未提供时 watchdog 不从 `createdAt`/`updatedAt` 或“最近 24h”猜测当前工作：只有 `held-alive` Run 进入 `items` 并可产生 `topAction`，其余非终态进入 `unscopedItems`。显式 cohort 之外的 Run 仍进入 `historicalItems`；显式 cohort 无效时 fail closed 为 historical-only，禁止回退到无 cohort 或时间窗逻辑。Provider signal 也只从当前 `items` 对应 Run 推导：`historicalItems`/`unscopedItems` 中同 Adapter 的旧失败仅作诊断，不得污染当前 admission；当前 cohort 内同 Adapter 的有效背压与畸形证据仍分别保持 hold 或 fail closed。

按 `items` 的 priority 升序处理最高项，并结合：

```bash
marshal task status --run RUN_ID --json
marshal doctor --run RUN_ID --json
tail -n 3 .marshal/runs/RUN_ID/events.jsonl
```

每个 heartbeat 至少完成一个安全有限动作：导入 ReviewDecision/rework、恢复拥有的 driver、publish、检查 CI、修 admission 或准备 scope 互斥 successor。只有所有 Run 都有可证明的 `owned-active` 进程，或存在明确且归属清楚的外部 blocker 时，才允许本轮不改变状态。连续只报状态是空转事故。

后台 `task run` 必须同时有一份本编排拥有的周期 watcher，例如：

```bash
nohup scripts/marshal-watch.sh 600 > WATCH_LOG 2>&1 < /dev/null &
```

先检查已有 watcher，禁止重复启动。Watcher 是轮间兜底和诊断，不是 Core lifecycle/retry authority。

## 进程与 dead 判断

- `RUNNING` 以已证明的进程存活和 ownership 判 active；长 Attempt 没有新事件可以正常，禁止只按事件年龄判 dead。
- `processOwnership=not-found`、`action=doctor-dead` 必须先 `doctor` 对账，再幂等恢复 Core 允许的同一动作。
- 只 terminate/wait/kill/reap 本编排创建且 identity 已绑定的进程/进程组；禁止跨编排 blanket kill 或杀非自己编排的进程。误杀常表现为多个 Run 同时 `worker.failed`/`context canceled`，看到这种形状先审计 ownership，不继续 kill/retry。
- `marshal supervise --once` 会启动全局 `READY/REWORK_REQUESTED` Run，不是只读巡检。只有容量、scope、lease 与 admission 全部通过后才可显式调用 supervise/driver。
- driver/工具调用被意外中断时先 status/doctor/events 对账；不得因为控制端断连就假定 Worker failure 或另起重复 Attempt。

## 交互权与事件驱动

长 `task run`/`verify`/`publish` 用 `nohup ... & disown` 后立即交还交互权。禁止在工具调用里 `sleep N && 检查`，也禁止 8–15 分钟粒度轮询。优先 `tail -f events.jsonl` 或不超过 2 分钟短周期，看到 `worker.completed` 就及时 verify。

后台 driver 可在收尾写完成标记，或在 Mac 使用：

```bash
osascript -e 'display notification "<run> 到 REVIEW_PENDING"'
```

通知只是唤醒提示；下一轮仍以 Core 状态和证据为准。

## 容量 admission

每轮读取 watchdog 的：

`memoryAvailableBytes`、`pressureFreePercent`、`pressureSource`、`swapUsedBytes`、`activeOwnedWorkers`、`slotsAvailable`、`recommendedMaxWorkers`、`concurrencyAction`。

只有同时满足以下条件才增加并发：

- `slotsAvailable > 0` 且 pressure=`ok`；
- 每个写任务有独立 worktree，`scope.allowPaths` 互不重叠；
- Provider/API 没有明确 rate limit，实际 timeout/event latency 没显示 backpressure；
- 每个 Run 的零 Attempt admission、plan/approval 和 Adapter evidence 均有效；
- 同一 `dedupeKey` 不 fan-out。

`swapUsedBytes` 只作历史/现状观测，不单独冻结并发。pressure 为 `critical`、`constrained` 或 `unknown` 时 `hold-concurrency`；恢复 `ok` 后重新计算槽位。排队时不杀其它编排进程腾位置。

不设固定并发 magic number，也不假设同机只有一个编排。当前 main 尚没有完整的自动 fairness/capacity Policy、Provider capacity contract、admission queue 或 backpressure controller；并发是 Lead 基于可得信号的 operator decision，不能写成 Core 已自动化。信号缺失时默认少派或排队，用户明确策略优先。记录每次 fan-out 的 CPU/内存/I/O、Provider 限制、背压和 scope 依据，供事后审计。

## 行动队列去重

Watchdog action 的只读 `dedupeKey` 应绑定：

`runId + state + action/processOwnership + spec/policy/capability/base + attempt/review/rework round + current ReviewPacket/control-record content digest`。

`RETRY_PENDING` 只有在 snapshot sequence 与 append-only journal 一致，且当前 Attempt lineage 严格满足 Core 的相邻 business-event 规则时才建议 `retry-or-abort`：最新 business event 必须是 `system/marshal-worker-runner` 写入的 `worker.failed RUNNING→RETRY_PENDING`，其前一 business event 必须是同 run、同 actor、同 `attemptId` 的 `worker.started →RUNNING`；每个 retry segment 的 Attempt 不得复用，最终 failure 的 `attemptId` 必须等于 snapshot 的 `currentAttemptId`。`REWORK_REQUESTED` origin 还必须紧邻并精确绑定：review 路径只接受 `system/marshal-review` 的 `review.rework REVIEW_PENDING→REWORK_REQUESTED`；其 `reviewRound` 必须由从 `CREATED` 开始、状态连续且 producer-authority 合法的 lifecycle replay 得出，round-bound ReviewDecision 则经 nofollow/限长读取、同源 Core Schema+semantic validation、JCS digest 与 task/run/spec/round/verdict/evidence 全匹配后才可接纳；CI 路径只接受 `publisher/marshal-github-publisher` 的 `publication.checks-failed CI_PENDING→REWORK_REQUESTED`，且 `headSha` 等于 snapshot 冻结 publication。任意 `stateTo=REVIEW_PENDING` 的计数、重复 finding ID、伪造 predecessor/actor 或其它 origin 均拒绝。同时 `adapterId`/`failureKind`/`retryDisposition` 组合必须属于封闭分类表，`failureSignature` 可从当前冻结 `baseSha`/`specDigest`/`policyDigest`/`capabilityDigest` 精确重算匹配，disposition 必须为 `retryable`。legacy free-text、缺字段、非法组合、错误 actor/run/origin、缺失/不相邻 started、successor started、snapshot/journal 漂移、Attempt 复用、signature/Attempt 不匹配或 journal 不可验证时统一进入 `retry-intervention`，禁止把旧 failure 升格成新 Worker 调度建议。

watchdog 可同时投影当前 operational-retry lineage 的 `rootFailure` 与 `latestFailure`，并将两者绑定到 `dedupeKey`。`RETRY_PENDING→RUNNING` 保留 root；`READY`/`REWORK_REQUESTED` 等新 origin 的 `worker.started` 必须重置 root/latest，防止前一 lineage 的背压或失败标识污染新工作。

`worker.started` 只证明 Attempt 已启动，绝不是 Provider 成功/恢复信号；rate-limit/quota/connection failure 后即使出现 retry started，也必须保留原 backpressure 到 `notBefore`/hold 到期，或等到合法 `worker.completed`。Core 的 completed payload 不要求 `adapterId`，watchdog 只能从紧邻的、同 run/Attempt、`system/marshal-worker-runner` 写入且 `stateFrom` 属于 `READY`/`RETRY_PENDING`/`REWORK_REQUESTED`、`stateTo=RUNNING` 的 `worker.started` 继承 Adapter，并与冻结 TaskSpec 的 `preferredAdapter` 一致后才把 completed 视为 Provider success；错误 actor/run/state、Attempt 不匹配或缺合法 started 一律 fail closed 为 unknown。

只有 `dedupeKey`、拟执行 action、doctor/admission 和外部 blocker 事实全不变时，后续 heartbeat 才只报告/等待。出现新 plan/approval、fresh base、new Attempt、Packet/control bytes、process ownership、Adapter doctor/config 或 Provider backpressure 变化时必须重新判断。

`dedupeKey` 是去重提示，不是 Core authority，不能单独阻止合法恢复。缺 plan、Adapter unconfigured、stale packet 和结构性 failure 分开分类；相同结构性 signature 不重复执行或重复派 reviewer。`unscopedItems`/`historicalItems` 只是诊断投影，必须先显式归属 Goal/cohort 并重读 Core authority，不得直接消费其 action。

## REVIEW_PENDING 与历史噪声

- fresh `REVIEW_PENDING` 在一个 heartbeat 内处理；若 packet/manifest/base/evidence 陈旧，产生 intervention finding并准备 successor，不反复调用 `task review`。
- 行动必须基于当前、非终态、identity 可验证的 Run。历史终态和不可操作旧 Run 只作为诊断/清理材料，不得挤占当前 action queue。
- 遗留终态缺 Outcome 时按 engineering reference 使用 `migrate-outcomes`；不得手写 `.marshal` 清噪。

## Fan-out

L 级复杂或探索任务可做调研队、评审团、跨仓库并行和仓库内 scope 互斥拆分。先从 `templates/research-task.json` 生成单 Attempt、零 rework、无 fallback 的自包含任务；评审 Worker 只提供材料，ReviewDecision 责任仍在 Lead。完整模式、分级和汇总纪律见 `docs/operator-runbook.md` §9、§10。
