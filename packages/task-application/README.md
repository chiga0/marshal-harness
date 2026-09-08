# Task Application

ADR0088 新 Node profile 的 Task reducer，依赖 `task-store` 的同库短事务。HTTP 和执行回调不直接写投影；本组件不启动 Agent、执行 CLI、在事务中做网络/文件工作，也不接管旧 `.marshal`。

当前纵切实现：创建 Task → 持久规划义务 → 提议并冻结业务 DAG/交付与验收标准 → 原请求摘要/计划 revision/digest 精确确认 → 同事务 Task、Operation、幂等回执与 outbox → 查询/暂停/恢复/取消。计划不能提高原预算；批准不会将候选标记验收成功，取消只进入 `cancelling` 并记录停止义务，不能凭请求成功宣称进程清理完成。

```js
const application = new TaskApplication({store, owner});
const handler = createTaskApiHandler({application: application.dispatch, token, expectedHost});
```

`proposePlan(taskId, expectedRevision, proposal)` 是可信规划接纳端口，不是公开 HTTP 直写端口。方案摘要绑定规范计划和原 Task 输入摘要；依赖图先检查无环/引用/重复，再按原 limits 冻结。执行消费同一 outbox，持久 reservation 后才可启动；独立验收由下述受信能力接纳，不依据模型的角色或自报成功。

读写命令按 `task-api` 的 Application Port 接入。幂等查找先于当前 revision CAS，原回执返回原 accepted response，不重复创建任务/Operation/执行义务。Store 内部隐藏异常，Application 只在整个事务回滚后恢复自己的封闭业务错误，未知存储故障仍返回不可用。

目前支持 `task.create/list/get/plan/approve/graph/cancel/pause/resume/events/audit/workers`、`worker.get` 与 `operation.get`；未实现的操作明确 `unsupported_operation`。没有实际执行的审计使用 `tokens:null/source:unavailable`、待验收，不把未知用量或未运行验证计成成功。没有计划的图返回 `plan_conflict`，不编造计划 revision。

## 未批准 Task 的有限问答

ADR0086 的新事实族 `task-clarification/v1` 通过受信 `clarification` 配置接入同一 Application。它支持 `task.questions` 与 `task.answer`，不引入第二个 reducer，也不将任意自然语言判作“信息不足”。只有显式选择的有限模板真正缺少声明槽时才冻结一批最多 3 个问题；未选择模板或已满足全部槽的 Task 保持原 `draft → Planner → awaiting-approval` 路径、原回执和一次确认。

```js
const clarification = createClarificationPort({
  template: {id, version, digest},
  applies(input) { return matchesSupportedTemplate(input); },
  slots: [{id: slotId, prompt, validator: {id: validatorId, version, digest},
    read(input) { return declaredValueOrNull(input); },
    validate(answer) { return isValidDeclaredValue(answer); }}],
  renderer: {id: rendererId, version, digest,
    render({input, values, missing, signal, deadline}) { return completeBoundedProposal(input); }},
});
const application = new TaskApplication({store, owner, clarification});
```

这些函数来自可信部署模块，不接受 HTTP/Agent 序列化的函数、validator 或身份。`applies/read/validate` 必须同步；`read` 仅以 `null` 表示真正缺失，已有非法值、无效声明、异步 validator、renderer 失败或超范围均拒绝，不能回退 Planner 掩盖失败。模板、槽、validator 与 renderer 身份在首次 create 冻结。renderer 可以异步纯计算，但不得调用付费规划、文件/网络或创建执行；等待受请求 signal、原期限和 10 秒上限约束，回调应遵守 AbortSignal。有限预览上限 256 KiB，答案非空、合法 Unicode、禁止 NUL，最多 4096 UTF-8 字节。

初始 Task、原输入、整批问题与完整初始 preview 在同一 SQLite 事务保存，状态为 `awaiting-answer`，没有规划 outbox、reservation 或 Attempt。每个答案只填原声明的数据槽；Core 从原输入重新构造 `context.text` 的有界数据投影，不向旧 prompt 无限追加。原 intent、上传清单、limits 及其余请求字段不变；计划去除 revision/digest 后的全部 bytes 和独立 verification 策略/布局必须与初始边界一致，答案不能改 graph、scope、Provider、oracle、验收或权限。不能机械证明这种收敛的模板明确不支持。

答案先查原回执，再校验 Task/question、当前正数 revision、preview 摘要、未消费与原期限。只有 `task.answer` 的新幂等摘要加入路由 questionId，其他操作的旧算法不变。renderer 在短事务外生成下一版，提交时重新查 current owner、完整原投影、CAS、preview、期限和取消；答案、不可变下一 preview、Task revision、Operation 和 receipt 一次提交，失败没有部分消费。旧 preview 与答案保持 revision=1 的独立 interaction 记录；preview 自己的 revision 每答加 1。创建时间、confirmBefore 和预算不延长。

所有问题答完只进入新事实专用 `awaiting-confirmation`，此时可一次确认当前完整计划；旧零问题的 `awaiting-approval` 不改。旧 control predicate 与旧 Task schema 不能把新事实当作可批准旧 draft。客户端应使用 `allowedActions`，不得依据状态字符串隐式批准；确认到期仅关闭答复/批准资格，仍允许取消，不凭查询追加终态。批准绑定当前 preview digest/revision/inputsDigest，再沿原 dispatch/verification 路径运行，不让测试模板继承任何固定业务验收权威。

答案回执返回历史 `task/preview/operation/acceptedRevision/acceptedPreviewDigest` 与单独 `currentTask`；精确重放保留原历史结果并标记 `replayed:true`，即使后来取消也不复活 Task。新答案、批准、取消仍沿同一 revision CAS；cancel 先提交拒绝迟到答案，answer 先提交使旧 revision cancel 冲突，使用当前 revision 的取消仍有效。冷重开只读原事实，缺少或漂移的模板不隐藏问题/答案/预览；新的 answer/approve 拒绝，精确已提交回执继续可重放。

`clarification.test.mjs` 使用仅测试组合安装的两槽模板、真实 SQLite 和 loopback HTTP，覆盖上述原子性、回放/取消/owner 竞争、旧 reader、配置漂移和边界拒绝；Service 测试另贯通正式组合与 resident，证明确认前零 Planner、确认后双作者与原取消清理。这里没有注册新的正式业务模板，也不宣称真实用户关键问答→真实团队交付或 B2 已完成。

问答回归必须同时运行原客户端的全部操作消费者，不能只测新 handler 夹具；`AnswerReceipt` 的完整示例与 `AnswerQuestion`、路由及 accepted revision 精确对应。组合命令为 `node --test --test-concurrency=1 packages/task-application/*.test.mjs packages/task-api/*.test.mjs packages/task-service/*.test.mjs packages/task-supervisor/*.test.mjs packages/task-client/*.test.mjs packages/task-team-integration/team.test.mjs`。Schema 示例另执行 Draft 2020-12 与完整格式验证。

## 受管执行接线端口

### 输入与制品

注入 `depot` 后，另支持 `input.create`、`artifact.get`、`artifact.content`；连同有限问答共19项 Application 操作。上传上限256KiB，严格 canonical base64；SQLite 保存上传清单、单本地用户归属、原幂等回执与已提交 blob 摘要索引，Depot 先持久化 bytes，随后 SQLite 同事务提交元数据。事务失败只留下无引用孤儿，不返回可下载对象。文件 I/O 不持数据库事务。

Task 创建会校验 `context.inputRefs` 指向已提交且可读取的 input，并将精确清单绑定原输入摘要。缺文件、损坏或已提交摘要再次上传不能静默修补；原幂等回执仍表示历史受理事实，新的 GET 必须重新核对 bytes。缺失 Depot 时不声称支持这些操作。上传输入的 `ready` 不授予最终交付或独立验收。

`application.execution` 仍是同一 Application/SQLite reducer，不是第二份进程或业务状态。构造时 `execution:{maxWorkers,providerIds,defaultProvider}` 来自服务可信配置，不能由 HTTP limits 扩大服务并发上限。

- `poll(after,limit)` 分页读取待处理义务；即使过滤后 items 为空也继续 nextCursor。
- `expandDispatch(commandId,revision)` 一次展开已确认 DAG；`nextWork(commandId,revision)` 将预算、Worker、容量和命令 unknown 原子持久化后才返回唯一 ticket。Planner 同样消费预算和容量。
- `mayStart(ticket)` 在实际启动前检查当前 fence；`started(ticket,fact)` 绑定真实 executionId，返回是否应停止；`progress(ticket,sequence,progress)` 保存有界进度。兄弟节点推进不使另一 ticket 自动陈旧。
- `finish(ticket,result)` 只按原 executionId 的 cleanup 事实接受回合结果；不以 Agent end_turn 冒充业务验收。下游只获得当前确认计划中直接依赖节点的候选。清理未知保留容量并进入 intervention。
- `scan(after,limit)`、`reconcile(taskId)` 检查期限、取消和 owner 换代；返回的 stopWorkerIds 只能查找当前进程内已持有句柄，禁止由存储 PID 重建 kill 权限。所有已知执行清理后才取消/失败结案；旧代未决执行不退款、不重发。
- `settleControl(commandId,revision)` 将实际控制观察回填 Operation，原幂等回执不变。暂停仅禁止新增 dispatch，不误杀已运行 Worker；恢复复用原待执行义务，不重置预算或重复展开 DAG。

批准计划降低 timeout 时，实际 deadline 从原 Task createdAt 计算并冻结，不逐节点刷新。Worker progress 只追加观察事件/紧凑 Worker 投影，不改用户控制 CAS；真正状态/控制转换仍推进 Task revision。大输入与候选快照单独持久化，容量和历史查询只读紧凑索引，下游仅加载当前直接依赖，避免完整计划按历史 Attempt 重复读入同一事务。旧 generation 的纯控制义务可由当前 owner 根据已证明状态回填观察，但不因此重新启动旧执行。原 Store 记录/字节/期限上限不变。

## 独立验收与原子交付

`TaskApplication` 与 `TaskSupervisor` 注入**同一个** `verification` 对象；没有注入时，旧计划、普通 reviewer/verifier 角色和 Agent 的 `end_turn` 仍不具有 Task 完成权威。受信组合通过 `application.mjs` 导出的工厂创建能力：

```js
const verification = createVerificationPort({
  id: 'trusted-checker',
  policy: {id: 'business-policy', version: '1', description: '公开且完整的固定验收条件'},
  bindPlan({taskInput, proposal, inputArtifacts}) {
    return {nodeId, description, layouts, deliveries};
  },
  start({ticket, prepared}) { return {started, completion, stop}; },
});
const application = new TaskApplication({store, owner, depot, execution, verification});
```

`bindPlan` 是同步、纯数据的受信策略，不可调用模型、文件或启动执行。其 `layouts` 为每个计划节点提供 `{nodeId,inputs:[{path,source}],allowedPaths}`；source 只能是原上传 `{kind:'input',id}` 或直接依赖 `{kind:'upstream',nodeId,path}`。`deliveries` 为 `{nodeId,path,targetPath}` 数组，必须完整覆盖唯一验收终点的全部直接交付分支、每个声明输出恰好一次；终点输入与这些布局完全一致，不允许只发布最后一个 patch。验收节点复用原 DAG，必须是唯一 sink 且 role 为 verifier，不能以另一隐藏活动绕开 Attempt 和预算。

Core 同时冻结原计划、输入、策略及布局摘要；策略全文、布局与交付映射逐项加入可见 `Plan.acceptance`，原要求不删除。绑定上限 48 KiB，各路径/每节点文件清单遵循文件组件边界；原 API 的 acceptance 最多 32 项、每项 4096 字节，超出或不支持直接拒绝，不截断或自动缩需求。策略选择及版本/描述由可信服务配置提供，不能从 HTTP、模型字段或任意 provider ID 创建能力。实际 checker 的固定代码、断言和策略摘要也须由同一可信组合精确绑定。

批准时尚不存在动态 Worker ID，故冻结 nodeId 来源；reservation 在原事务中解析当前已完成依赖，得到 `ticket.input.fileLayout`，并把全计划所有作者的紧凑精确 manifest 放进验收 ticket 的 `input.verification:{binding,manifests}`。普通候选仍保存完整原报告，紧凑索引不复制报告。文件布局绑定来自原批准，而非现场 `context` 自证：

- `execution.approvedLayout(ticket)` 同步返回 `{planDigest,nodeId,layoutDigest}`；business 的 `layoutFor` 可直接读取非 planner 的 `ticket.input.fileLayout`，planner 仅空输出布局。
- `execution.observeExecution(ticket)` 同步读取原 Attempt 的 `{executionId,startedAt}`，供真实结果采集核对。
- `ticket.executionType` 明确区分 `agent` 与 `verification`。验收与作者共用容量、deadline、failure/cancel fence、original handle 和 cleanup；不能在 collect 中启动 checker。

受信 `start` 必须同步返回原受管句柄。其 completion 是 `{type:'verification',status:'passed'|'failed',cleanup,evidence?,delivery?}`，不是 ACP `end_turn`。成功 evidence/delivery 分别为 `{name,mediaType,content:Uint8Array}`、上限各 8 MiB；失败/取消/未知 cleanup 可省略输出。实际 command adapter 必须核对原退出/信号、完整未截断输出和精确固定断言，不能把模型报告的 pass 当验收。工厂只包装此受信 start 的原 completion，产生父进程内 WeakMap 绑定的 opaque receipt；没有 mint/反序列化端口，JSON、structuredClone 或其他实例 receipt 都不能通过。

Supervisor 不 clone receipt，也不将验收结果送进 Agent 专用 collect；原 finally 统一调用同步 `release(ticket)`，只关闭 business 的目录 FD，不删除目录、不退容量。准备/采集回调仍需遵守 AbortSignal，释放失败仅产生有界诊断。

通过的验收 finish 先在事务外核对原始候选/上传 bytes，并持久化 evidence 和完整 delivery bytes；随后在**同一个** SQLite 事务重查当前 owner、精确 ticket、原期限、取消 fence、所有上游 manifest、已完成作者清理及验收原 executionId/startedAt/cleanup。一次提交独立 Decision、证据/交付引用、Worker/节点终态、容量释放及 Task completed。SQL 失败只有无引用孤儿 bytes，没有 ready 引用或假完成；原 receipt 可在同 owner/同 ticket 下原样重试提交，不启动第二次验证。批准 Operation 仍表示控制受理/展开，不冒充 Task 的完成回执。cancel 先赢时只接纳原 cleanup 释放容量，绝不接纳迟到的 pass；cleanup 未知或旧 generation 未决不退款重派。

可信 checker 的 `failed` receipt 同样核对精确 capability/status/ticket、当前 owner、原 execution/cleanup 与冻结 manifest。有效且已清理的负面验收同事务记录 `rejected` Decision、`acceptance.failed` 和**已有** evidence 引用；缺 evidence 时保存无制品的明确失败事实，不制造报告，绝不接纳 delivery 或 Task completed。有界封闭 `reason` 可保存为内部 reasonCode。取消/期限先赢、cleanup 未知、旧 owner 或控制器本地停止/故障，不能冒充独立验收失败；这些路径仍保持原停止/未知语义。`acceptance` 如实呈现独立验收 pass/fail；`firstReview` 是不同维度，没有真实独立代码 review 事实仍为 `{passed:0,total:0,pending:0}`，不能借最终验收成功填为 1/1。

原受管 launcher 的失败也可能确认 `started:null/cleaned:true`：仅在精确 receipt、当前 owner/ticket 与原 Attempt 的 executionId/startedAt 均为空一致时，按已有 Worker 失败/cleanup 路径释放容量，不制造独立验收事实。`passed` 无启动、身份不匹配、缺字段均不因此获得接纳；未知 cleanup 仍保留容量。回归使用真实 checked-in Node guard 的缺 executable 失败，并验证另一 Task 可继续完成。

## 验证

固定 Node 24.15.0：

```sh
node --test packages/task-application/*.test.mjs
```

测试实际创建 SQLite 文件而非 Fake Store，覆盖冷重开原回执、owner 换代后 pending 命令保持未知效果而不自动重发、确认冲突/陈旧 revision/伪造摘要零部分写入、取消非终态、暂停期限不重置、分页与真实事件，以及调用者身份。图测试还覆盖局部修正闭包不重跑无关节点。测试进程自行生成的临时数据在结束时清除，不执行临时原生文件。

真实 loopback HTTP 已连接此 Application 与 SQLite，覆盖创建/确认/图/Operation、分页、服务重开、取消，以及并发同键创建与竞争确认。此处的规划提议由测试通过内部端口提交，没有实际 Planner/Worker，不能把存储与服务重开说成活跃执行恢复。

新增 `verification.test.mjs` 使用真实 SQLite、Depot、FileBusiness/task-files producer 与可控受信检查器，验证完整双分支布局、原候选采集、opaque receipt 反例、取消保留容量、旧 owner、缺失/漂移 bytes、原 deadline、SQL rollback、Decision/制品/Task 同事务及冷重开同 bytes。负面路径从原 Planner reservation → freeze/approve → 作者候选 → verifier failed 贯通，覆盖有/无证据、原 receipt 重放、事务回滚和冷重开，及取消/过期/未知/旧 owner 不误算独立验收失败。Supervisor 组合验证同 owned lane、跳过 Agent collect 和统一 release；夹具检查器/cleanup 不代表真实模型、进程或客观业务检查已通过。

当前为完整 Core 接纳纵切候选，不授予真实业务交付、完整进程恢复、API-STABLE 或 RELEASED。正式出口仍需同一服务实际连接受管 ACP、独立 command checker、完整下载消费，并通过取消/重启与正式发布门禁。
