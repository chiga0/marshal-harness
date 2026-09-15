# ADR0107 合同候选冻结测试

`scripts/acceptance-contract-freeze.test.mjs` 是 ADR0107 的离线候选合同测试。它保护的是正式实现前容易被悄悄改写的边界：Store 规范摘要、Task 级证据绑定、当前 Node 可定位的 `attemptRef`、`worker.reserved` 因果事件以及 `sourceArtifact:{id,digest}` 的制品身份和精确字节。

fixture 会在临时的真实 SQLite Store 中驱动 `TaskApplication` 的 `task.create → proposePlan → task.approve`，再执行 `expandDispatch → nextWork`，由真实 `TaskExecution.reserve()` 产生 Worker ticket、输入快照、`attempt` projection 和 `worker.reserved` 事件。测试只手工补充离线制品 manifest 与候选证据上下文，不接入 HTTP、Provider 启动、VerificationPort 或 Core，不登记 capability，不写入产品运行态，也不把测试结果变成 `ReviewDecision`、Verification receipt 或业务完成。因此它证明的是当前运行时事实可以被候选检查器复验，不能证明生产运行时已经支持 ADR0107 的完整验收合同。

## 保护的候选边界

- 结构化摘要统一使用当前 Store 的 `encode(value)` 后再 `digest(bytes)`；对象键顺序不改变摘要，数组顺序改变摘要。制品摘要使用原始字节摘要，不能用展示文本或 `JSON.stringify` 顺序替代。
- 证据外层是 `{profile,taskId,contractDigest,entries}`，每个条目严格闭合，并绑定 `contractDigest`、`planDigest`、`candidateDigest`、`selectionDigest` 与检查目录。
- 当前 Node 没有独立耐久 `attemptId`。测试使用 `task-attempt-ref/v1` 的复合身份：`workerId`、`commandId`、`generation`、`reservationDigest` 和 `reservationEvent`。五者必须能从同一 Task 的 `attempt` projection、输入快照和唯一 `worker.reserved` 事件重算；`Worker.worker.attempt` 不参与唯一身份。输入按真实 `TaskExecution.reserve()` 形状校验：根对象包含 `task`、`inputArtifacts`、`node`、`plan`、`upstream`，`taskId` 由 ticket 携带，不要求重复出现在输入根部。
- `sourceArtifact` 只有 `{id,digest}`。检查器通过当前 Task 的受信 manifest 解析它，并核对 `taskId`、`kind=evidence`、`status=ready`、媒体类型、字节数和精确内容摘要。只提交摘要、换制品 ID、修改字节或增加未知字段均拒绝。
- 任一合同、计划、选果、候选、Attempt、reservation event 或 source Artifact 变异都会拒绝；`reservationDigest` 会排除自身后覆盖完整 ticket 与嵌套输入，持久化 ticket 被变更而不重算摘要时也会拒绝。不能把缺证据、重复事件或未知字段降级为通过。

## 与 ADR0107 的关系

本包对应 ADR0107 的**实现前验证材料**，不表示 ADR 已 Accepted。ADR 中列明的能力身份、摘要域编码、Artifact manifest 完整来源、Core 事实生产者、Audit/UI 投影和旧客户端行为仍需在正式实现前独立冻结；任何字段或 profile 改动应先更新 ADR、测试和审计记录，不能通过兼容解析器静默放宽。

`validateAcceptanceEvidence` 是测试检查器，不是生产 API。它的 `context` 由测试夹具显式提供冻结的计划/选果摘要和临时字节读取表，不能由 HTTP 请求或候选 Agent 自行提交获得权限。当前返回值只表示离线合同候选可被复验，不携带 `authority:true`，不改变原 Task 状态。

## 复验

```sh
node --test scripts/acceptance-contract-freeze.test.mjs
```

当前测试覆盖规范摘要与原始字节摘要、真实 `TaskExecution.reserve()` 输入和 SQLite 事件/投影绑定、完整证据通过、十一类绑定变异拒绝、`reservationDigest` 重算与持久化 ticket 变异拒绝、精确输入/制品字节变异拒绝以及未知字段、重复事件和检查目录缺失拒绝。它不替代真实 Agent、业务后验、冷故障恢复、UI 三线验收或 S01→M01→C01 E2E。
