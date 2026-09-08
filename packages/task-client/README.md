# 正式 Task API Node 客户端

`TaskClient` 是 `node-task-service/v1` 的 HTTP 消费者实现候选，不代表 API-STABLE 或业务验收已通过。它只导入已有 OpenAPI operation/schema 与严格 JSON 解析工具，不导入 Application、Store、Supervisor 或固定订单业务，不启动进程，也不写状态。

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
| DAG、Workers、Worker 详情/取消 | `task.graph/workers`、`worker.get/cancel` |
| 问题、回答 | `task.questions/answer`；答案携带原 questionRevision/expectedRevision |
| Operation | `operation.get` |
| 制品 metadata、下载、input 上传 | `artifact.get/content`、`input.create` |
| 事件、审计、Provider、Supervisor | `task.events/audit`、`provider.list`、`supervisor.get` |
| 存活与就绪 | `health.get/ready.get` |

分页只接受显式 limit/cursor，一次只取一页，不自动轮询。便利方法仅 `createTask/getTask/approveTask/downloadArtifact`；其余使用通用入口。不存在自动批准、生成幂等键、读取新版本重写 CAS、自动 retry 或自动 cancel。409/501/503/504 均暴露为有界错误，调用者决定查询/原请求重放或停止。

## 下载与 HTTP 限制

`downloadArtifact(artifactId,options)` 等价于 `request('artifact.content',{path:{artifactId},...options})`：先 GET 原 Artifact manifest，再 GET 内容，要求 ready、同 Artifact ID、精确 bytes、SHA-256 和响应 Content-Digest 同时成立。返回 `{artifact,content:Buffer}`；不会执行、解包或自行写入宿主目录，也不把下载摘要匹配当业务正确。

两个 GET 共用本次原超时/AbortSignal，不刷新预算。普通 request 只发一个 HTTP 请求；下载固定两次只读请求，不是隐式重试。HTTP 错误或观察超时不表示服务端命令未提交；写请求丢响应后只能由调用者查询原对象或显式复用原 key/body，不能猜新 Task。

只接收显式 `http://127.0.0.1:PORT` 根地址，无 hostname/DNS、用户信息、任意路径、查询、fragment 或第三方 URL。`fetch` 可注入，默认 Node 全局 fetch；注入方为可信 transport，必须遵守请求目标和 AbortSignal。客户端设置 manual redirect，拒绝跳转/外部 response URL；token 只放认证头，不进入日志、返回错误或应用正文。

等待默认 10 秒，调用方可设 10 毫秒至 30 秒；覆盖 fetch 与响应流读取。响应最多 8 MiB，拒绝内容编码、过大/矛盾 Content-Length、错误媒体类型、schema 或对象归属。超时/abort 终止本次传输和 reader，无 Task 取消副作用。`TaskClientError` 仅含稳定 code/status/requestId/allowedActions，不复制 peer 自由消息、原始正文、transport exception 或 caller abort reason。成功业务内容仍由调用者按其公开范围脱敏处理。

## 验证口径

`node --test packages/task-client/index.test.mjs`：7 项定向测试包含正式 HTTP handler 的真实 loopback 服务、全部 24 operations、独立消费端下载摘要、显式批准响应丢失/回执、冲突、恶意响应、超时/abort 和资源回收。Application 是清楚标记的 DI fixture，而非 SQLite 或真实 Agent；这些结果只证明客户端/HTTP 合同，不证明真实规划、执行、取消、重启恢复或业务验收。

现有 Python demo/question clients 服务旧 Go profile 的批准/固定订单制品协议，不替换它们、不声称自动兼容。后续同一 Node SDK 可直接对接正式 Application/SQLite/Supervisor 的真实服务完成独立客户端验收。
