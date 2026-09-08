# Node 团队 HTTP 契约候选

## 范围和真值

本合同描述 [ADR0087](adr/0087-node-local-team-feasibility-probe.md) 的 `marshal-node-team-experiment/v1`，基于 `428acb36` 的实际 `main.mjs` HTTP adapter、`supervisor.mjs` 应用与 `store.mjs` 投影。机器合同为 [OpenAPI 3.1](../experiments/node-team/openapi.json)，版本 `0.1.0-experimental`。它是 **experimental contract candidate，不是 API-STABLE 或正式生产 API**；不替代 [ADR0085](adr/0085-agent-team-service-contract-and-storage.md) 的产品目标，也不是 `internal/server/openapi.json` 中旧 `v1alpha1` 协议。

没有新增运行时 endpoint、运行时校验器、权限、持久化或恢复机制。Schema 约束由当前 producer/consumer 提取，不靠空 object、补造字段或状态提升覆盖缺口。实现发生变化时须连同合同与 HTTP 契约测试更新。

## 启动与访问

按 [实验说明](../experiments/node-team/README.md) 使用本机 Node 24 启动。URL 和端口来自启动输出；Bearer token 只从该实例受保护连接文件读取，不打印、不放 URL、命令参数、Worker prompt 或业务事件。这里不提供固定端口、公开监听或登录平台。前端重启产生新连接文件/token，客户端应重新读取；原 Task 与操作 key 可继续使用。

服务仅监听 `127.0.0.1` 的随机端口。所有请求（包括 `/health`）要求 Host 精确匹配实例且只出现一次；非空 Origin 拒绝。业务请求要求唯一、精确的 `Authorization: Bearer …`，无跨域支持。仅前端 `/health` 免 Bearer，它不查询 supervisor，不能当作 `/ready`。同 UID 的 ambient 权限不属于恶意隔离保证。

POST 仅接受 `application/json` 或带 `charset=utf-8`；拒绝 Content-Encoding。JSON 请求体最多 16384 bytes，恰好一个 `Idempotency-Key`，格式为 `[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}`。路径不接受 query、百分号编码或双斜线；不提供分页参数。响应为 JSON、`Cache-Control: no-store`。调用者应私有保存 plan/intent：其中包含实际提示词，不能当成公共日志。

## 已实现的 8 条路径、9 个操作

| 方法与路由 | 请求与结果 | 注意事项 |
| --- | --- | --- |
| GET `/health` | `{status:"ok",profile}` | 仅前端存活，不保证可接单 |
| POST `/v1/tasks` | `{intent}` → 201 `Task` | 只建立草案，不启动作者；UTF-8 intent 最多 4096 bytes |
| GET `/v1/tasks` | 200 `{tasks:[Task]}` | 最多 100 个已存 Task，全量返回、无分页 |
| GET `/v1/tasks/{id}` | 200 `Task` | 计划、状态与两个 Worker 在原投影内 |
| POST `/v1/tasks/{id}/approve` | `{expectedRevision,previewDigest}` → 202 `Task` | 确认已展示的精确计划，之后自动执行；不是交付完成 |
| POST `/v1/tasks/{id}/cancel` | `{expectedRevision}` → 202 `Task` | 受理停止 fence，不是执行已终止；继续查原 Task |
| GET `/v1/tasks/{id}/workers` | 200 `{taskId,workers}` | 两个 Worker 当前观察，不是任意 Worker 管理接口 |
| GET `/v1/tasks/{id}/audit` | 200 `Audit` | Task 投影加耗时、0 retry/rework、验收结果；用量未知为 null |
| GET `/v1/tasks/{id}/delivery` | 200 `Delivery` | 仅已完成且有合法交付；JSON 内联文件，不是 Go 路径 ZIP |

响应对象、必需/可选字段、枚举、完整嵌套 plan/worker/file 与错误对象由 OpenAPI 给出。创建/控制请求拒绝未声明字段，不能传 executable、env、provider、模型、任意路径或预算。当前业务模板固定为订单 normalize/report 双作者；intent 仅作背景，不改变固定验收或工具权限。

## 状态、版本与重放

Task 状态为 `awaiting-approval / approved / running / verifying / completed / failed / cancelling / cancelled / intervention`。没有 `phase`、`allowedActions`、`cancellationRequested` 或 Go 的大写 Run 状态字段。Worker 状态为 `planned / queued / launching / running / collecting / collected / failed / cancelled`；未观察到的时间、退出码与原因字段省略，不伪造时间或零值。`pid`、`guardPid` 是可空诊断观察，不是 HTTP 取消参数，也不是授权凭据。

`revision` 是控制版本，不是每次进度的序号：草案为 1，批准/显式取消/终态更新时推进；`approved→running→verifying` 和字节进度不一定推进。客户端确认使用原 `previewDigest`，新控制动作使用最近读到的 revision；不要据 status 字符串自行计算 revision。revision 使用 JavaScript 安全整数（1 至 9007199254740991），不是任意 int64。

幂等域为 create 的整个实验状态根，以及控制动作的 `(Task, operation, key)`；approve 与 cancel 的 key 域不同。同 key 同正文先于新命令 CAS 匹配，返回**原 Task 的当前投影**，不是原响应 bytes。同 key 异正文 409；新 key 遇陈旧 revision 409。丢响应只能带同 key、原正文查询/重放，不得创建同义新 Task、刷新预算或自动再次调用模型。

取消后的 `cancelling` 可由用户取消或执行失败触发，不等于已成功取消。只有确认所属清理才可看到 `cancelled`；未知清理为 `intervention`，保留证据，不按历史 PID 杀进程。终态 Task 不接纳新的取消（409）；已受理原取消的精确重放仍可读取当前终态。已完成 Task 不因取消改写失败。

## 交付、观察与限制

Delivery 包含 `taskId, files, checks, verifiedAt`。每个文件为 `{name,content,sha256}`，允许且恰好各一份 `normalize.mjs` 与 `report.mjs`，内容为非空合法 UTF-8、无 NUL、每文件最多 65536 bytes；文件摘要是**裸 64 位小写 hex**，不同于 `previewDigest` 的 `sha256:` 前缀。客户端还须核对文件名唯一性、内容摘要和 Task 关联，并在独立目录以固定业务 oracle 消费。Schema 结构通过、HTTP 200 或 `checks` 数字不能取代业务验证；OpenAPI 示例只演示结构，不是通过原业务 oracle 的交付。

一个时刻最多一个活动 Task、两个作者，每 Task 一个 Attempt、批准起固定 300000 ms，不自动 retry/rework。stdout 原生事件流 8 MiB、stderr 1 MiB，与最终文件限制分开；溢出回调观察的 bytes 可能超过 cap，schema 不把 cap 误写成所有观测值的上限。未知 token/cost 始终 null，不当 0。`elapsedMs` 来自宿主时间，时钟回拨可能产生负值；不是单调时钟计费。没有工具事件、SSE 或推理内容承诺。

JSON Schema 的 `maxLength` 计 Unicode 字符，不能精确表达 UTF-8 byte 限额。因此同时记录 `x-maxUtf8Bytes` 与 `x-wellFormedUnicode`，契约测试显式验证；普通 OpenAPI 客户端必须自行实现这两项，不能只依赖代码生成。摘要相等、唯一文件名、状态/证据关系同样是应用语义，不由字符串 pattern 证明。

## 错误与重试边界

当前错误仅 `{error:"封闭原因码"}`，**没有**目标中的 message、requestId、operationId、retryable 或 allowedActions。OpenAPI `Error` 列出当前可达原因码；状态码包括 400（输入）、401（Bearer）、403（Host/Origin）、404（路由/Task）、405（已匹配子路由的方法）、409（版本/幂等/容量/终态/交付未就绪）、413（请求过大）、415（媒体类型）、503（内部/supervisor/存储不可用）。集合路由的不支持方法可能按未知路由返回 404，不承诺统一 405。

503、连接丢失、客户端观察超时不证明命令未落盘。保留 Task ID、原 key 与正文，并重新查询同一实例/状态根；不要把观察窗口耗尽投影成 Task 失败，也不要自动取消或新建替身。前端的 HTTP/RPC 超时不增加产品执行期限。完整 supervisor/机器崩溃恢复未验证；遗留未知 owner 拒绝接管，不声称任意重启可透明续跑。

## 与目标 API 的差异及后续顺序

| ADR0085/架构 §5 目标 | 当前 Node profile |
| --- | --- |
| `/tasks/{id}/plan`、`/plan/approve` | plan 在 Task 内；确认是 `/tasks/{id}/approve`，不可混用路由 |
| graph/DAG、Worker 详情与独立 Worker cancel | 未实现；两节点 plan/workers 不冒充通用 DAG |
| questions/answers、pause/resume | 未实现；Go ADR0086 组件不自动成为 Node 能力 |
| operations、events/SSE、allowedActions | 未实现；只有 Task 轮询与最小 audit |
| inputs、独立 artifacts/manifest/content | 未实现；当前只有 Task 内联 delivery，无任意上传和发布 |
| agent-providers、supervisor、ready | 未实现；启动配置/内部控制器不能冒充公开管理 API |
| 通用 Task、更多 Provider、SQLite 与同版本恢复 | 本实验固定订单/Pi/独立文件状态根；不复用或迁移 Go RB1 |

先保留这条能验证的 HTTP 闭环，再随真实业务纵切演进字段和路由、显式兼容策略及故障恢复；不为了完整目标清单提前放置空 endpoint。`API-STABLE` 仍须以目标 API、独立测试和真实业务/恢复出口验收，不能从此 OpenAPI 文件或 Node 实验成功直接升级。

## 契约验证

`node --test experiments/node-team/openapi.test.mjs` 使用已安装 Node、真实 HTTP adapter/supervisor 和仓库内确定性 Pi 协议替身；不调用模型、不执行 Marshal/Go。覆盖全部公开操作的代表响应、原 key 重放、CAS/未知字段/Unicode/大小/认证错误、替身交付和取消；保留有界私有夹具状态。这不是实机 Agent 证据。

测试内断言解释器只支持本文件使用的 JSON Schema 关键字，未知关键字直接失败，不自称完整 JSON Schema 实现。另用 `jsonschema` 的 `Draft202012Validator.check_schema` 对全部 component schema 做 Draft 2020-12 metaschema 检查；这只校验 schema 语法，不替代上述 producer/consumer 与业务断言。标准 refs 和所有示例均有确定性结构检查；实际 HTTP 的 preview 和文件摘要另行核对。
