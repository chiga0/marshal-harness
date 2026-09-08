# Task Application

ADR0088 新 Node profile 的 Task reducer，依赖 `task-store` 的同库短事务。HTTP 和执行回调不直接写投影；本组件不启动 Agent、执行 CLI、在事务中做网络/文件工作，也不接管旧 `.marshal`。

当前纵切实现：创建 Task → 持久规划义务 → 提议并冻结业务 DAG/交付与验收标准 → 原请求摘要/计划 revision/digest 精确确认 → 同事务 Task、Operation、幂等回执与 outbox → 查询/暂停/恢复/取消。计划不能提高原预算；批准不会将候选标记验收成功，取消只进入 `cancelling` 并记录停止义务，不能凭请求成功宣称进程清理完成。

```js
const application = new TaskApplication({store, owner});
const handler = createTaskApiHandler({application: application.dispatch, token, expectedHost});
```

`proposePlan(taskId, expectedRevision, proposal)` 是可信规划接纳端口，不是公开 HTTP 直写端口。方案摘要绑定规范计划和原 Task 输入摘要；依赖图先检查无环/引用/重复，再按原 limits 冻结。执行者如何消耗 outbox、持久 reservation、判定独立验收和写入制品，仍需后继受管执行接线；本组件不虚构这些事实。

读写命令按 `task-api` 的 Application Port 接入。幂等查找先于当前 revision CAS，原回执返回原 accepted response，不重复创建任务/Operation/执行义务。Store 内部隐藏异常，Application 只在整个事务回滚后恢复自己的封闭业务错误，未知存储故障仍返回不可用。

目前支持 `task.create/list/get/plan/approve/graph/cancel/pause/resume/events/audit/workers`、`worker.get` 与 `operation.get`；未实现的操作明确 `unsupported_operation`。没有实际执行的审计使用 `tokens:null/source:unavailable`、待验收，不把未知用量或未运行验证计成成功。没有计划的图返回 `plan_conflict`，不编造计划 revision。

## 受管执行接线端口

`application.execution` 仍是同一 Application/SQLite reducer，不是第二份进程或业务状态。构造时 `execution:{maxWorkers,providerIds,defaultProvider}` 来自服务可信配置，不能由 HTTP limits 扩大服务并发上限。

- `poll(after,limit)` 分页读取待处理义务；即使过滤后 items 为空也继续 nextCursor。
- `expandDispatch(commandId,revision)` 一次展开已确认 DAG；`nextWork(commandId,revision)` 将预算、Worker、容量和命令 unknown 原子持久化后才返回唯一 ticket。Planner 同样消费预算和容量。
- `mayStart(ticket)` 在实际启动前检查当前 fence；`started(ticket,fact)` 绑定真实 executionId，返回是否应停止；`progress(ticket,sequence,progress)` 保存有界进度。兄弟节点推进不使另一 ticket 自动陈旧。
- `finish(ticket,result)` 只按原 executionId 的 cleanup 事实接受回合结果；不以 Agent end_turn 冒充业务验收。下游只获得当前确认计划中直接依赖节点的候选。清理未知保留容量并进入 intervention。
- `scan(after,limit)`、`reconcile(taskId)` 检查期限、取消和 owner 换代；返回的 stopWorkerIds 只能查找当前进程内已持有句柄，禁止由存储 PID 重建 kill 权限。所有已知执行清理后才取消/失败结案；旧代未决执行不退款、不重发。
- `settleControl(commandId,revision)` 将实际控制观察回填 Operation，原幂等回执不变。暂停仅禁止新增 dispatch，不误杀已运行 Worker；恢复复用原待执行义务，不重置预算或重复展开 DAG。

批准计划降低 timeout 时，实际 deadline 从原 Task createdAt 计算并冻结，不逐节点刷新。Worker progress 只追加观察事件/紧凑 Worker 投影，不改用户控制 CAS；真正状态/控制转换仍推进 Task revision。大输入与候选快照单独持久化，容量和历史查询只读紧凑索引，下游仅加载当前直接依赖，避免完整计划按历史 Attempt 重复读入同一事务。旧 generation 的纯控制义务可由当前 owner 根据已证明状态回填观察，但不因此重新启动旧执行。原 Store 记录/字节/期限上限不变。

上述端口的 SQLite 组合测试不等于 Supervisor 已消费义务；独立验收、最终制品接纳和完整服务部署仍待接线。

## 验证

固定 Node 24.15.0：

```sh
node --test packages/task-application/*.test.mjs
```

测试实际创建 SQLite 文件而非 Fake Store，覆盖冷重开原回执、owner 换代后 pending 命令保持未知效果而不自动重发、确认冲突/陈旧 revision/伪造摘要零部分写入、取消非终态、暂停期限不重置、分页与真实事件，以及调用者身份。图测试还覆盖局部修正闭包不重跑无关节点。测试进程自行生成的临时数据在结束时清除，不执行临时原生文件。

真实 loopback HTTP 已连接此 Application 与 SQLite，覆盖创建/确认/图/Operation、分页、服务重开、取消，以及并发同键创建与竞争确认。此处的规划提议由测试通过内部端口提交，没有实际 Planner/Worker，不能把存储与服务重开说成活跃执行恢复。

当前仍为控制链集成候选，不授予团队交付完成、完整进程恢复、API-STABLE 或 RELEASED。正式出口必须同链连接受管 ACP/其他 Adapter、独立验收与制品交付后验证。
