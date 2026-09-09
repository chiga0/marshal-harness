# 正式 Task API Node 客户端

`TaskClient` 是 `node-task-service/v1` 的 HTTP 消费者实现，已纳入当前候选的 API-STABLE 核心接口检查点，兼容范围见[接口说明](../task-api/README.md)。这不表示所有业务、Provider 或正式平台验收已通过。它只导入已有 OpenAPI operation/schema 与严格 JSON 解析工具，不导入 Application、Store、Supervisor 或固定订单业务，不启动进程，也不写状态。

```js
import {TaskClient} from './index.mjs';
const client = new TaskClient({baseURL: 'http://127.0.0.1:49152', token});
const task = await client.createTask({intent: '交付一份分析说明'}, 'caller-create-key');
const plan = await client.request('task.plan', {path: {taskId: task.id}});
// 先向用户展示真实 plan，再由调用者传入明确确认的原 revision/digest。
const operation = await client.approveTask(task.id, {
  expectedRevision: confirmedTaskRevision,
  planRevision: confirmedPlanRevision,
  planDigest: confirmedPlanDigest,
}, 'caller-approve-key');
const observed = await client.request('operation.get', {path: {operationId: operation.id}});
```

## 单一合同与显式控制

通用入口：`request(operation,{path={},query={},body,idempotencyKey,signal,timeoutMs})`。operation 来自 `packages/task-api/openapi.json` 的 `x-application-operation`，不维护第二套 DTO 或路由 Schema。

| 操作 | 调用示例/说明 |
|---|---|
| Task 创建、列表、详情、计划 | `task.create/list/get/plan` |
| 批准、取消、暂停、恢复 | `task.approve/cancel/pause/resume`；body/key 必须由调用者提供 |
| 同计划局部修正 | `task.repair`；仅原独立内容拒收及已启用策略满足时受理，保留原预算/期限，不是通用重试 |
| DAG、Workers、Worker 详情/取消 | `task.graph/workers`、`worker.get/cancel` |
| 问题、回答 | `task.questions/answer`；答案携带原 questionRevision/expectedRevision 和互斥的 previewDigest 或 questionDigest |
| Operation | `operation.get` |
| 制品 metadata、下载、input 上传 | `artifact.get/content`、`input.create` |
| 事件、审计、Provider、Supervisor | `task.events/audit`、`provider.list`、`supervisor.get` |
| 存活与就绪 | `health.get/ready.get` |

分页只接受显式 limit/cursor，一次只取一页，不自动轮询。便利方法包括 `createTask/getTask/approveTask/cancelWorker/downloadArtifact`；其余使用通用入口。不存在自动批准、生成幂等键、读取新版本重写 CAS、自动 retry 或自动 cancel。409/501/503/504 均暴露为有界错误，调用者决定查询/原请求重放或停止。

`cancelWorker(workerId,{expectedRevision},idempotencyKey)` 使用所属 Task 的 revision；调用者先查询 Worker.taskId 和 Task 后作明确选择。仅显式 v6 服务实现该命令，旧根 501 不降级为 task.cancel。202 Operation 必须 kind=worker.cancel 且 workerId 与路由完全相同；查询其 succeeded 只证明该目标结清，不表示整个 Task 已取消或交付。丢响应保留原 key/body，不自动重复调用。

运行中业务问答按 ADR0090 的新闭集分支传输，原预批准问答不改：从受保护 `task.questions` 取得 `kind:'business'` 的原 questionId/questionDigest/revision，再由用户明确给出 answer 字符串。请求为 `{expectedRevision,questionDigest,questionRevision:1,answer}`；不得混入 previewDigest、工具 permission option、对象或新版本。原 4096 UTF-8 字节/NUL/Unicode 边界保持。

运行答复 202 返回 `RuntimeAnswerReceipt`，只有原接纳事实与单独 currentTask，不能据此宣布 Agent 已消费。客户端同时核对题目/Task/Operation、请求摘要族与 accepted revision；外部 transport 也不能用另一题或旧 preview 回执替代。查询原 Operation/问题可见 pending/dispatched/acknowledged/cancelled/expired/unknown；未接纳答案的问题 deliveryStatus 为 null。丢响应不会自动重发；调用者可用原 key/body 显式重放，客户端不 resume/approve、不把投递 unknown 当可重试。

可选 `Plan.interaction` 仅转发已批准的有限策略字段，不因 schema 支持而报告运行时能力。是否启用由实际服务端完整 Provider/Store/Supervisor/验证链决定，本包不访问内部账本或创建另一套问答状态机。

## 下载与 HTTP 限制

`downloadArtifact(artifactId,options)` 等价于 `request('artifact.content',{path:{artifactId},...options})`：先 GET 原 Artifact manifest，再 GET 内容，要求 ready、同 Artifact ID、精确 bytes、SHA-256 和响应 Content-Digest 同时成立。返回 `{artifact,content:Buffer}`；不会执行、解包或自行写入宿主目录，也不把下载摘要匹配当业务正确。

两个 GET 共用本次原超时/AbortSignal，不刷新预算。普通 request 只发一个 HTTP 请求；下载固定两次只读请求，不是隐式重试。HTTP 错误或观察超时不表示服务端命令未提交；写请求丢响应后只能由调用者查询原对象或显式复用原 key/body，不能猜新 Task。

只接收显式 `http://127.0.0.1:PORT` 根地址，无 hostname/DNS、用户信息、任意路径、查询、fragment 或第三方 URL。`fetch` 可注入，默认 Node 全局 fetch；注入方为可信 transport，必须遵守请求目标和 AbortSignal。客户端设置 manual redirect，拒绝跳转/外部 response URL；token 只放认证头，不进入日志、返回错误或应用正文。

等待默认 10 秒，调用方可设 10 毫秒至 30 秒；覆盖 fetch 与响应流读取。响应最多 8 MiB，拒绝内容编码、过大/矛盾 Content-Length、错误媒体类型、schema 或对象归属。超时/abort 终止本次传输和 reader，无 Task 取消副作用。`TaskClientError` 仅含稳定 code/status/requestId/allowedActions，不复制 peer 自由消息、原始正文、transport exception 或 caller abort reason。成功业务内容仍由调用者按其公开范围脱敏处理。

## 验证口径

`node --test --test-concurrency=1 packages/task-client/*.test.mjs`：测试包含正式 HTTP handler 的真实 loopback 服务、OpenAPI 全操作消费（含局部修正）、独立消费端下载摘要、显式批准响应丢失/回执、冲突、恶意响应、Unicode 转义 token 反射、超时/abort 和资源回收；专项测试覆盖运行答复的两族互斥、恶意 transport 题目串绑、丢响应后的显式原 key/body 重放及不自动继续。Application 是清楚标记的 DI fixture，而非 SQLite 或真实 Agent；这些结果只证明客户端/HTTP 合同，不证明真实规划、执行、同执行 ACK、取消、重启恢复或业务验收。

现有 Python demo/question clients 服务旧 Go profile 的批准/固定订单制品协议，不替换它们、不声称自动兼容。后续同一 Node SDK 可直接对接正式 Application/SQLite/Supervisor 的真实服务完成独立客户端验收。
