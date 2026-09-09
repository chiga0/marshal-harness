# 通用文件业务 Adapter

本包为 Task Supervisor 提供文件业务的 `prepare/collect` 组合，支持代码文件、说明、分析数据等有明确文件交付范围的任务，不固定订单业务或作者数量。不创建进程、Task 真值、审批、验收 Decision、Workspace 或 Skill 平台；保持 Node-only。

```js
const business = createFileBusiness({
  parent: canonicalPrivateExecutionParent,
  depot,
  layoutFor,        // 可信的逐节点文件布局，不解析自然语言 scope。
  approvedLayout,   // 从原批准合同独立查询绑定；不得临时自签。
  observeExecution,// 原 Task/Attempt 的实际 started 观察。
  authorize,       // 可选：业务授权；省略时权限请求一律取消。
});
const supervisor = new TaskSupervisor({
  execution: app.execution, providers,
  prepare: business.prepare, collect: business.collect,
});
```

`parent` 和 `depot` 沿用 `task-files` 的已有私有目录、只创建新执行目录、普通文件/路径/链接/大小与失败保留约束。`prepare` 只返回 `{cwd,prompt,onPermission}`，不额外改变 Supervisor 的准备 DTO。

ADR0092 的 v5 服务使用 `createStagingOnlyBusinessFactory({authorize?})`，而非上述任意布局工厂。它复用同一文件准备主体，但布局固定来自原 ticket（Planner 明确空布局），Depot、父目录与批准/执行查询由 composition 注入，时钟使用原实现。`isStagingOnlyBusiness(factory,business?)` 只读检查私有 WeakMap 内的原工厂、原对象及实际 prepare 身份，不能登记任意回调；属性复制/包装均不合格。`authorize` 仍只在许可后的原生权限请求中执行。原宽接口不删除，也不被追认为 v5；这不是同 UID 恶意 JavaScript 的安全沙箱。

## 必须由可信组合提供的两个事实

`layoutFor(ticket,{signal,deadline})` 返回显式布局：

```js
{
  inputs: [
    {path: 'inputs/source.csv', source: {kind: 'input', id: inputArtifactId}},
    {path: 'inputs/upstream.json', source: {kind: 'upstream', workerId, path: 'summary.json'}},
  ],
  allowedPaths: ['src/report.mjs', 'README.md'],
}
```

`fileLayoutDigest(layout)` 规范化路径排序后产生摘要。可信组合必须在原计划确认前冻结、显示并绑定该布局；`approvedLayout(ticket,context)` 在准备和采集时独立返回 `{planDigest,nodeId,layoutDigest}`。缺失或不匹配即拒绝。**把当前 layout 现算摘要返回、从 Worker 文本/自由 scope 推导允许路径，都不构成原批准事实。** 本包不补造它，也不会修改已批准 plan。

初始 Planner 尚未有批准计划，只能按已有 Task 输入准备只读文件，`allowedPaths` 必须为空；其提案交 Core 冻结和等待确认。非 Planner 的每次执行都要求原 `planDigest` 匹配。

`observeExecution(ticket,context)` 返回原 Attempt 已记录的 `{executionId,startedAt}`，应由 Application 的当前 owner/ticket 检查后读取。它必须匹配 Supervisor 交来的**原受管 Provider completion** 中 `cleanup.started`，且原结果是 `completed/end_turn/cleaned:true`。本包既不接受 Worker 正文里的 cleanup，也无法凭普通 JSON 证明进程确实已停止；Supervisor/Runtime 的原句柄观察仍是前置。Provider 必须保持同 ticket 的绑定。

当前切片只提供这些 DI 端口，不宣称已替服务端实现布局批准持久化、started 查询或完整自主交付接线。缺少真实查询实现时必须拒绝，不用常量通过替代。

## 输入、提示词与候选

- 原 `ticket.input.task` 的完整目标、context、requirements、批准 plan、节点目标及上游候选原样进入提示词，整体最多 256 KiB，不静默截断。提示词不禁用 Agent 原生工具/Skill，也不把工具可用性当成额外授权；用户/源数据里的指令不改变控制合同。
- 上传输入只从 `ticket.input.inputArtifacts` 精确引用解析；上游只从 `ticket.input.upstream` 已冻结候选解析，并核对同 Task/plan、直接依赖、原 Worker 和 manifest 摘要。随后仅从 Depot 物化字节，绝不读旧 Worker 目录。缺少 `inputArtifacts` 而 Task 声明了 inputRefs 时拒绝。
- `scope` 是业务描述，不是 glob 或文件权限。实际输入/输出路径只来自已批准布局；文件输入不可修改。Git 原地修改不属于本适配器的首个模式，可以交付独立文件快照，不冒充 Git patch 集成。
- Planner 的报告最多 64 KiB，只解析一个 JSON 对象（允许单个 JSON code fence），拒绝重复字段、尾随文字、顶层控制字段及超限。返回 `{plan,result}`；Graph、预算、具体类型与确认由原 Core 决定，不因能解析 JSON 就通过计划。
- 普通节点只返回 `{result}`，包含 `profile:task-file-business/v1`、Task/Worker/node、原 reservation/plan、布局/输入/manifest 摘要、精确 `files:[{path,digest,bytes}]` 和有界原报告。报告中的 pass/Decision 不获得权威。候选可供后继独立验收，并不表示 Task completed 或 delivery ready。
- 文件 manifest 摘要沿用 `task-files` 的固定字段顺序；读取经 SQLite 规范化 JSON 保存过的候选时先恢复该顺序，避免同一字节在冷读取后失配。

## 取消、错误与释放

所有异步 DI 前后检查原 signal 和 deadline；不延长预算，不自动重试准备、采集或模型。权限缺省拒绝；可选 `authorize(ticket,request,{signal,deadline})` 是可信授权策略，必须自行核对当前权限/停止 fence，迟到返回在取消、到期、释放后不再交给 Provider。

成功采集或已进入采集后的失败会关闭该 handle 的 FD；源目录和孤儿 Depot 字节始终保留。`release(ticket)` 仅关闭 FD、放弃后续采集，不删除文件、不发信号、不释放 Execution 容量。**失败/取消的 Worker 不走成功 collect，服务应在原终止路径调用 release**；逐 Worker release 钩子未接好前不能声称长时间服务资源回收已完成。`close()` 幂等关闭所有剩余 FD，可映射服务的 dispose，但不代替逐 Worker 收尾。

只保留最多 64 个本进程准备 handle；目录和停止归属未知时不接管。普通同 UID 文件权限不是恶意代码沙箱；Publisher 分权仍由服务/执行环境落实。

## 验证范围

`node --test --test-concurrency=1 packages/task-business/index.test.mjs`

测试使用真实文件系统、ArtifactDepot、规范化候选序列化及一条真实 TaskApplication Planner reservation→collect→Core 等待确认链；执行 started/cleanup 来自明确的测试夹具，没有模型或真实 Worker。覆盖布局漂移、输入与身份、取消/迟到权限、模糊提案、额外/缺失文件及失败保留；不据此关闭 B1、API-STABLE 或业务验收。
