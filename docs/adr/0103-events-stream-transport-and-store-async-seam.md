# ADR 0103：任务事件 SSE 传输面与存储 async 缝线

- 状态：Accepted（2026-09-15；维护者明确 follow-up 三项（问答合同、存储可插拔基础、多传输）合单实施；合于 2026-09-15 修订后的 ADR 触发条件——本 ADR 记载实际发生的 HTTP 合同新增与存储内部接口变化，不涉及持久化格式、权限模型、生命周期或业务语义变化）。
- 基线：`80f6fed568e21b5488b35c7048048f79c42b8adb`。
- 关联：ADR0094、ADR0100、ADR0101；[扩展契约](../extension-contracts.md) Store/Depot/Agent 端口语义。

## 问题

1. `task.events` 仅有轮询：客户端要实时观察任务推进只能以 client-wrapper 轮询，长任务下既有延迟也有无效流量；Supervisor 侧终端事件（不经过任何 HTTP 变更）没有到用户的推送通道。
2. 存储生命周期方法（`read/write/claimOwner/renewOwner/inspectRecovery`）原为同步：该接口与现代异步 Port 习惯不一致，且未来后端（PostgreSQL 等）为异步 I/O 时，没有可替换的接缝；但事务回调受 `node:sqlite` 同步性与期限计算约束，不能也不应 await。
3. 有缝无规：后端可替换性的正确性只能靠 PR 评审记忆，无可执行合同。

## 决策

1. **新增 `GET /v1/tasks/{taskId}/events:stream`(SSE 传输面）**:
   - 与 `task.events` 同一权威：每一帧都实时取自对 `task.events` 应用查询的真实结果（id=sequence,event=type,data=Event JSON);**不平行推断、不缓冲平行真值、无第二派生状态**。
   - `Last-Event-ID` 续传；断档由客户端以轮询接口重对齐，本端点不承诺跨断线零丢失；GET 零副作用。
   - 认证与 `task.events` 完全一致（`Authorization: Bearer`；不接受 URL/Cookie 凭据）;`Host/Origin` 等 protect 规则同套执行。
   - 推送仅为提示：组合层事件枢纽（hub)`notify(taskId)` 只是早醒信号，醒后重查权威序列；连接发哨为惰性：无提示则按心跳周期重查。
   - 有界：单任务与全局并发连接数封顶，超界 429+Retry-After；服务关闭时 `hub.abortAll()` 明确中止长连接，不依赖超时自生自灭。
   - single-shot 适配层不得假造：经普通分发路径调用 `task.events.stream` 必然 501。
2. **存储生命周期 async 缝线 + FIFO 互斥**:
   - `read/write/claimOwner/renewOwner/inspectRecovery` 返回 Promise；事务回调**严格同步**(SQLite DatabaseSync 不能在事务中 await)，签名与失败闭集不变。
   - 同步时代由事件循环阻塞天然串行化；async 化后无关调用方（HTTP 分发、Supervisor 周期、owner 续约）会真实交错，因此 Store 内部以 **FIFO 互斥队列**串行全部生命周期操作，恢复旧的单写者顺序——这是实现语义保真，不是新并发模型。
   - 真正的冲突只剩事务回调内部的同步嵌套调用，继续 fail-closed：`nested-transaction` 且毒化当前事务（双侧如实可见）。
3. **后端无关合同测试**:`packages/task-store/conformance.test.ts` 将扩展契约中的 Store/Depot 义务（owner 围栏、CAS、幂等回执、事件仅追加与摘要链、outbox 单向生命周期、async 回调拒绝、调试恢复限界）固化为可对任意后端复跑的合同套件，SQLite 实现先行通过。
4. **问答接口现状确认**：经核对，Task 级运行问答/澄清的回答合同（receipt/ACK 分离、过期、select 校验、重放幂等）已完整在建；缺的是推送通道，由本 ADR 第 1 条补齐，Worker 侧仅保留只读投影，不把问题绑定到具体进程身份。

## 已完成（教育/后果同步）

- OpenAPI 登记：28 操作（27 single-shot + 1 SSE 传输面）;`standard-api`/`api-support` 现行节同步；`http-handler` 面审计断言更新为 28 且流面 501 作证。
- task-client 增加 `streamTaskEvents`(same-token、弹式帧解析、断线以 sequence 续传指引）。
- 错误映射不变：HTTP 401/403/400/429 与轮询面一致；事件序列/事实的持久格式、owner 围栏、预算语义不变。

## 验收与发行

- 新增 `packages/task-api/events-stream.test.ts`(7 项）与 `packages/task-service/events-stream.test.ts`（真实 service+Supervisor+ACP 推送链证明);Store 合同套件 7 项；全部既有套件不回归。
- 本 ADR 不触发任何发行；远程 Agent/存储后端为后续另行审查目标，不作本 ADR 成立条件。
