# Task Service HTTP 实现候选

此包实现 ADR0085/0088 的正式 Node profile HTTP 适配层，**不是 API-STABLE 或生产完成声明**。它不导入实验 Store/Supervisor、固定订单业务或具体 Agent，不启动 socket、模型、进程或数据库。旧九操作实验协议保持独立。

`createTaskApiHandler({application,token,expectedHost,requestTimeoutMs})` 返回 Node HTTP handler；所有 24 个操作调用同一个异步注入函数：

```js
await application(request, {principal: 'local-operator', requestId, signal});
```

`request` 包含 `operation`、路由绑定的 `taskId/workerId/questionId/operationId/artifactId`、写操作的 `key/body`，分页操作的 `page:{limit,cursor}`。应用不得把 HTTP signal 当作 Task cancel；断线/504 后命令可能已提交，客户端只能查询原对象或用原 key/正文重放。业务权限、原回执优先于 CAS、预算、计划批准、问题期限、对象归属、状态转换和持久化都由 Application/Store 唯一处理，HTTP 不新增真值。

[openapi.json](openapi.json) 是请求、响应和路由的唯一机器合同，`contract.mjs` 从它读取并执行所用 schema 子集的严格校验（不强转、补默认值、删除未知字段或解析远程引用）。未知验证关键字失败；`maxLength` 外另检查 `x-maxUtf8Bytes` 与合法 Unicode。不是通用 JSON Schema 产品。标准 Draft 2020-12 metaschema 与独立示例/真实 Application 接线验证分别执行。

## 应用接线

- `task.create` 接收 intent、可选 context/requirements/更低 limits，201 返回 Task；零问题任务通过应用规划 outbox 异步完成。显式有限模板缺少声明输入时，Application 原子返回 `awaiting-answer` 与已冻结完整预览，不产生 Planner 义务。HTTP 不调用模型等计划。
- `task.plan` 返回原计划；`task.approve` 请求绑定 expectedRevision、planRevision、planDigest。批准/取消/暂停/继续或 Worker cancel 返回原 Operation，HTTP 为 202，即使原幂等操作已完成也不增加副作用。
- `task.questions` 返回原批及 `taskRevision/previewRevision/previewDigest/confirmBefore/preview`；旧零问题 Task 为 `items:[]` 且 preview 为 null。ADR0086 的 `task.answer` 保留原四字段 `expectedRevision/previewDigest/questionRevision/answer` 和原 `AnswerReceipt`/202：历史 `task/preview/operation/acceptedRevision/acceptedPreviewDigest` 与单独 `currentTask/replayed` 明确区分。HTTP 再核对路由 Task/question 及接受 revision/preview 关联；其他操作/旧 receipt 算法不改。
- `awaiting-confirmation` 仅供 ADR0086 新问答事实的全部问题答完后使用，旧零问题仍为 `awaiting-approval`。两者只表达待确认，不批准、不执行；UI 应按 `allowedActions` 展示显式动作，不能按字符串自动批准。
- `task.list/get/graph/workers/questions/audit/events`、`worker.get`、`operation.get`、`provider.list`、`supervisor.get` 返回 schema 对应投影。列表一页最多 100，默认 50，cursor 是应用提供的 opaque ID，不能作为路径或版本权威。
- `artifact.get` 返回有界 manifest。`artifact.content` 返回 `{artifact,content:Uint8Array}`：HTTP 校验 ID、ready、长度、内容 digest 后才发送 octet-stream 与 Content-Digest；不接收宿主路径，不读磁盘。应用仍负责授权与制品所属/验收。
- `input.create` 使用有界 base64 JSON；应用必须核对 canonical base64、解码后不超过 256 KiB、原操作者临时所有权与后续 Task 引用，不能把上传等同于批准执行。Task、Operation 和 Artifact 不要求 Git 或 Workspace。
- `health.get/ready.get` 同样来自应用；HTTP 不能凭自身存活伪报应用就绪。两者免 Bearer 但仍检查实例 Host/Origin，响应只能是极小 health/readiness。未就绪抛 not_ready。

应用未接线的操作抛 `{code:'unsupported_operation',status:501}`，不能返回空成功对象；未知异常返回固定 503。没有默认假应用，也没有回退旧 Go/实验 controller。handler 存在不表示应用支持全部路由；支持矩阵必须以真实接线与验收为准。

## ADR0090 运行中业务问答合同

此差量只提供 schema/HTTP/客户端边界，不创建问题、持久化答案、投递或 ACK，也不自行报告运行能力。真实 Provider、业务、Supervisor、Store 和最终验收全部支持时，Application 才能启用；缺能力仍由原应用明确拒绝。

- 原 `Plan` 可选增加闭集 `interaction:{profile:'task-runtime-question/v1',policyDigest,maxQuestions,maxWaitMs}`，最多 3 题和 120000 毫秒；它必须来自原批准计划摘要，HTTP 不生成或更新该摘要。没有该字段的旧计划仍保留原字节与状态。
- `Questions.items` 使用 `QuestionItem` 的闭集 oneOf。原 `Question` 不变；新 `RunningQuestion.kind='business'` 必须绑定 workerId/nodeId、revision=1、questionDigest，且 subject 等于 questionDigest。空 options 表示 input，select 最多 16 个唯一原值。未接纳答案时 deliveryStatus=null（包括未答即取消/过期）；接纳后才为 pending/dispatched/acknowledged/cancelled/expired/unknown。nonce、原协议/session 句柄不属于公开字段。
- `AnswerQuestion` 为两个闭集 oneOf：旧 previewDigest 分支或新 questionDigest 分支，禁止同时带两个摘要。两分支都固定 questionRevision=1，答案非空且最多 4096 UTF-8 字节，禁止 NUL/非法 Unicode；choice 只提交原选项字符串，不提交权限 option 或对象。原题有效性、deadline、选择成员和 exact replay-before-CAS 仍由 Core 唯一判断。
- `AnswerResponse` 为原 `AnswerReceipt` 与新 `RuntimeAnswerReceipt` 的闭集 oneOf，均返回 202。新回执是 `{taskId,questionId,operation,acceptedRevision,questionDigest,deliveryStatus,task,currentTask,replayed}`，没有 preview。路由、请求族、题目摘要、原 accepted revision、Operation 和 Task 归属由 HTTP/客户端再核对；原回执不因为 ACK 改写，currentTask 单独表示当前状态。
- 202 仅表示答案被受理，不表示已经送达、Worker 继续或 Task 完成。观察原题目/Operation 才能知道投递结果；客户端不自动 approve/resume、换 CAS/key 或重试 unknown。控制版本冲突等继续使用原 409、到期 410、未支持 501，未知内部错误继续封闭为 503。

## 错误、安全与限制

应用可抛普通 DomainError `{code,status}`，不用依赖 HTTP 包；code/status 必须匹配 `contract.mjs` 的封闭映射。`TaskApiError` 是测试/组合可用的同形工具。未知 code、错误 status、异常 message/stack 均不回显。统一错误是 `{code,message,requestId,allowedActions}`，code 使用 snake_case。`same-key-replay` 只指原幂等请求，不授权自动重派 Worker 或创建新 Task。

只允许 composition 声明 `127.0.0.1:PORT`，唯一 Host/Bearer/JSON 媒体头/幂等键；拒绝非空 Origin、压缩体、未知字段、非法 UTF-8、重复 JSON 键、深层 JSON、路径编码/逃逸和多值分页参数。token 只留 HTTP 闭包，不进入 Application、正文、日志或 Worker；应用的可公开 prompt/context 必须预先脱敏。普通同 UID 不构成恶意 sandbox。

请求最多 256 KiB（input 为 384 KiB 的 base64 包络），响应/制品最多 8 MiB，body 深度最多 32；Task intent 8 KiB、context text 32 KiB。用户请求 limits 不是授权扩大服务限额：Application 必须比较实际 profile 上限。HTTP 等待默认 10 秒、最多 30 秒，不刷新 Task 原期限。请求体未结束时超时/断线会移除读取监听器并关闭该连接；可写错误响应先发出再回收连接，不继续解析剩余请求体，也不转化为 Task cancel。没有流式下载、SSE、HTTP multipart 或任意执行/发布端点。

最短验证命令：`node --test --test-concurrency=1 packages/task-api/*.test.mjs packages/task-client/*.test.mjs`。原 24 操作和新增运行问答测试使用 Request/Response 流替身、独立内存 Application fixture，以及真实 loopback HTTP；覆盖两族答复、oneOf 精确互斥、4096 字节边界、请求/回执串绑、有限选项、错误、丢回复与显式同 key 重放。标准 Draft 2020-12 metaschema/示例另用真实 jsonschema 校验器验证。无 DB/模型；不能替代实际 SQLite、同执行投递/ACK、恢复或独立业务验收。
