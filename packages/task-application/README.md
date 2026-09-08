# Task Application

ADR0088 新 Node profile 的 Task reducer，依赖 `task-store` 的同库短事务。HTTP 和执行回调不直接写投影；本组件不启动 Agent、执行 CLI、在事务中做网络/文件工作，也不接管旧 `.marshal`。

当前纵切实现：创建 Task → 持久规划义务 → 提议并冻结业务 DAG/交付与验收标准 → 原请求摘要/计划 revision/digest 精确确认 → 同事务 Task、Operation、幂等回执与 outbox → 查询/暂停/恢复/取消。计划不能提高原预算；批准不会将候选标记验收成功，取消只进入 `cancelling` 并记录停止义务，不能凭请求成功宣称进程清理完成。

```js
const application = new TaskApplication({store, owner});
const handler = createTaskApiHandler({application: application.dispatch, token, expectedHost});
```

`proposePlan(taskId, expectedRevision, proposal)` 是可信规划接纳端口，不是公开 HTTP 直写端口。方案摘要绑定规范计划和原 Task 输入摘要；依赖图先检查无环/引用/重复，再按原 limits 冻结。执行者如何消耗 outbox、持久 reservation、判定独立验收和写入制品，仍需后继受管执行接线；本组件不虚构这些事实。

读写命令按 `task-api` 的 Application Port 接入。幂等查找先于当前 revision CAS，原回执返回原 accepted response，不重复创建任务/Operation/执行义务。Store 内部隐藏异常，Application 只在整个事务回滚后恢复自己的封闭业务错误，未知存储故障仍返回不可用。

目前支持 `task.create/list/get/plan/approve/graph/cancel/pause/resume/events/audit` 与 `operation.get`；未实现的操作明确 `unsupported_operation`。没有实际执行的审计使用 `tokens:null/source:unavailable`、待验收，不把未知用量或未运行验证计成成功。没有计划的图返回 `plan_conflict`，不编造计划 revision。

## 验证

固定 Node 24.15.0：

```sh
node --test packages/task-application/*.test.mjs
```

测试实际创建 SQLite 文件而非 Fake Store，覆盖冷重开原回执、owner 换代后 pending 命令保持未知效果而不自动重发、确认冲突/陈旧 revision/伪造摘要零部分写入、取消非终态、暂停期限不重置、分页与真实事件，以及调用者身份。图测试还覆盖局部修正闭包不重跑无关节点。测试进程自行生成的临时数据在结束时清除，不执行临时原生文件。

真实 loopback HTTP 已连接此 Application 与 SQLite，覆盖创建/确认/图/Operation、分页、服务重开、取消，以及并发同键创建与竞争确认。此处的规划提议由测试通过内部端口提交，没有实际 Planner/Worker，不能把存储与服务重开说成活跃执行恢复。

当前仍为控制链集成候选，不授予团队交付完成、完整进程恢复、API-STABLE 或 RELEASED。正式出口必须同链连接受管 ACP/其他 Adapter、独立验收与制品交付后验证。
