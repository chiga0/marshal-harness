# Watchdog、恢复与容量

> **何时必须读取：** 每个 heartbeat、后台 `task run`、Run 卡住/无响应/driver 中断、需要 supervise/recovery、增加或降低并发、fan-out、多编排并存或判断进程归属时，必须完整读取。

## 每轮顺序

先运行只读 watchdog：

```bash
MARSHAL_WATCH_NOTIFY=0 scripts/marshal-watch.sh --once --json
```

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

只有 `dedupeKey`、拟执行 action、doctor/admission 和外部 blocker 事实全不变时，后续 heartbeat 才只报告/等待。出现新 plan/approval、fresh base、new Attempt、Packet/control bytes、process ownership、Adapter doctor/config 或 Provider backpressure 变化时必须重新判断。

`dedupeKey` 是去重提示，不是 Core authority，不能单独阻止合法恢复。缺 plan、Adapter unconfigured、stale packet 和结构性 failure 分开分类；相同结构性 signature 不重复执行或重复派 reviewer。

## REVIEW_PENDING 与历史噪声

- fresh `REVIEW_PENDING` 在一个 heartbeat 内处理；若 packet/manifest/base/evidence 陈旧，产生 intervention finding并准备 successor，不反复调用 `task review`。
- 行动必须基于当前、非终态、identity 可验证的 Run。历史终态和不可操作旧 Run 只作为诊断/清理材料，不得挤占当前 action queue。
- 遗留终态缺 Outcome 时按 engineering reference 使用 `migrate-outcomes`；不得手写 `.marshal` 清噪。

## Fan-out

L 级复杂或探索任务可做调研队、评审团、跨仓库并行和仓库内 scope 互斥拆分。先从 `templates/research-task.json` 生成单 Attempt、零 rework、无 fallback 的自包含任务；评审 Worker 只提供材料，ReviewDecision 责任仍在 Lead。完整模式、分级和汇总纪律见 `docs/operator-runbook.md` §9、§10。
