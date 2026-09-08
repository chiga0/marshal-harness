# Task Supervisor 控制器

本包是 ADR0088 下的内部自驱调度组件，只依赖 Application execution 端口和已配置的 Provider。它不直接导入 SQLite、不创建第二份 Task 状态，不执行固定业务验收，也不会把 Worker 的 `end_turn` 或候选标记为 Task completed。独立验证、最终 Artifact 与完整服务接线由可信业务组合继续完成。

```js
const supervisor = new TaskSupervisor({
  execution: application.execution,
  providers: new Map([[provider.id, provider]]),
  prepare: async (ticket, {signal, deadline}) => ({cwd, prompt}),
  collect: async (ticket, providerResult, {signal, deadline}) => ({result}),
  onError: diagnostic => reportDiagnostic(diagnostic),
});
supervisor.start();
// 关闭监听/停止接单时，等待原 Provider 的清理观察：
const shutdown = await supervisor.close();
```

## 内部接口与调用顺序

- `start()` 启动内置定时循环，无需外部 watchdog；`tick()` 可显式推进一轮，并发调用合并成同一 Promise。默认间隔 100 ms；每页 25 条，每轮每类最多 100 页，页间让出事件循环。扫描游标跨轮保留，过滤后空页仍按 `nextCursor` 继续，不以空 `items` 判断扫描结束。
- 每轮先 `scan/reconcile` 观察取消、原期限和旧 owner，再 `settleControl → expandDispatch → nextWork`。预算、全服务/每 Task 容量和依赖是否满足，只由同一 Application 事务决定。多个 ticket 独立推进，不等待一个 Provider completion 后才开始下一个。
- `nextWork` 成功持久化 reservation 后才调用 `prepare`。ticket 是深冻结的独立副本，原 deadline 不延长。准备完成后再次 `mayStart`，其返回与同步 `provider.start` 之间没有 `await`。暂停时，已预留但未启动的 ticket 等待原义务恢复，不新建 Attempt、不失败重派，也不停止其他正在运行的 Worker。
- `Provider.start({cwd,deadline,prompt,onProgress,onPermission?})` 必须同步返回 `{started,completion,stop}`；前两项为 Promise。控制器先绑定实际 started，再串行写进度，最后提交 finish。进度只映射封闭 phase、tool kind/status，不保存原始工具输入、输出或任意额外字段；每 Worker 最多 4096 次观察、最多 128 条未完成排队。
- `prepare(ticket,{signal,deadline})` 只能准备受信执行目录和输入，不得自行启动进程；仅返回 `{cwd,prompt,onPermission?}`。权限回调由可信组合绑定批准范围，默认沿 Provider 拒绝。`collect(ticket,providerResult,{signal,deadline})` 仅返回 `{plan?,result?}`，不得覆写 status、cleanup 或执行身份。Planner 的 plan 经原 Application 冻结，Worker 的 result 仍只是候选。
- 准备与采集各默认最多 30 秒，且不超过 ticket 剩余 deadline。超时或取消后不采纳迟到返回。回调必须尊重 AbortSignal；JavaScript 不提供强制终止回调私自创建活动的能力，因此这两处是受信组合，而不是 Worker 自带扩展口。

## 停止、故障隔离与恢复

取消先由 Application 持久化 fence。控制器只查找当前内存中已持有的 Worker 句柄并调用原 `stop()`，绝不从存储 PID 重建发信号权限。Provider bootstrap/started 尚未完成时也保留原句柄；协议取消或 stop 返回的通知不替代原 completion 的 cleanup。

只有确定尚未调用 `Provider.start` 的路径，才产生明确的 `scope:none-start/started:null/cleaned:true` 本地观察。已调用 start 后，缺少或身份不匹配的 cleanup 只能记为 unconfirmed，原 completion 仍保留在所属句柄上；不会凭 `null` 推定已清理、退款或重派。有效 cleanup 原样传给 Application，即使准备之后的回调失败、结果收集失败或取消抢先，也不丢弃实际清理事实。

可归属 Worker 的准备、采集、Provider/进度格式故障，只停止其原句柄并 finish failed/unknown，由 Application 对该 Task 建立失败 fence，再收口同 Task 的其他 Worker；不会因此停掉另一个 Task。只有 owner/执行端口不可用、全局扫描等不变量无法确认时才停止全局接单和所有当前句柄。`onError` 每个失败 Worker或全局失败只通知一次，字段限定为错误码、阶段和对象 ID；最近诊断最多保存 32 项，不输出异常正文、路径或模型内容。通知消费者自身失败可从 `notificationFailures` 观察，不触发执行重试。

没有 ticket 的 `unsupported_task/capacity_exceeded` 准入拒绝，不授权控制器编造 Worker 或直接修改 Task：原 pending 义务保留，由 Application 诊断/期限收口，其他 Task 继续；通知去重最多保留 128 个命令，溢出只给一次聚合提示。正式组合应在冻结计划和支持声明中拒绝不可用 Provider。

`close()` 幂等，停止新准入并等待所有原 owned completion，不用超时假造 cleanup；若 Provider 违反其有界停止合同，关闭可能仍在等待。返回 `clean` 仅说明本控制器持有的执行是否均有确认清理且结果已写回，不证明旧 generation 已停止、所有 Task 已恢复或整个服务 ready。原 owner 的未决义务由 Application 保留为 intervention/未知容量，不自动接管、重派、退款或复用目录。`snapshot()` 是诊断，不是第二份业务真值或 HTTP readiness 证据。

## 定向验证与边界

固定 Node 24.15.0：`node --test --test-concurrency=1 packages/task-supervisor/controller.test.mjs`。

测试使用真实 SQLite 与 TaskApplication、可控 Fake Provider，验证两个 Worker 重叠、直接依赖汇合、空过滤页续扫、重复 poll 不重派、准备/启动/采集中的取消、原 deadline、冷 owner 未决、进度顺序与上限、暂停恢复、自驱循环，以及一个坏 Task 不阻止另一个 Task 和后续准入。Fake cleanup 明确是夹具事实，不是 OS 进程清理或独立业务验收证明。本包测试不调用模型、不加载真实鉴权配置、不执行 Go/native 程序，不代表完整 HTTP/ACP/交付已完成或 API-STABLE。
