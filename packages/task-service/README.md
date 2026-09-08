# 正式 Node 服务组合根

这是 ADR0088 的 Node-only 组合入口：复用同一个 `TaskApplication`、SQLite `Store`、`ArtifactDepot`、`TaskSupervisor` 和 HTTP handler。不执行 Marshal 原生文件，不创建另一套 Task 状态或决策控制器。当前是可测试的服务组合候选，**不是完整业务交付、API-STABLE 或 RELEASED**。

## 一条命令启动

```sh
node packages/task-service/main.mjs \
  --root /absolute/private-parent/task-data \
  --mode create \
  --config /absolute/trusted/service-config.mjs
```

运行环境是已允许的固定 Node 24.15.0；父目录必须已存在、canonical、当前 UID 所有且 `0700`。`create` 只创建全新根，重复启动使用 `--mode open`。端口默认 `0`，仅监听 `127.0.0.1`，可显式 `--port`。不自动创建父目录、不把已有空目录初始化成另一格式，也不从旧 Go/实验根导入。

配置模块是受信任部署代码，不是 HTTP 插件或用户提示词。它应默认导出：

```js
export default {
  providers: new Map([[provider.id, provider]]),
  prepare: async (ticket, {signal, deadline, depot, executionParent}) => {
    // 从批准的输入引用建立唯一目录；禁止在此启动 Agent。
    return {cwd, prompt, onPermission};
  },
  collect: async (ticket, providerResult, {signal, deadline, depot, executionParent}) => {
    // 核对实际 cleanup，读取明确文件清单，保存不可变候选。
    return ticket.role === 'planner' ? {plan: proposal} : {result: candidate};
  },
  dispose: async () => { /* 关闭业务组合持有的文件 FD，不删除现场。 */ },
  applicationOptions: {
    execution: {maxWorkers: 2, defaultProvider: provider.id},
  },
};
```

示例中的变量由实际业务组合提供，不是可直接执行的假实现；`service.fixture.mjs` 只供无模型测试。Provider 可由已有 `createAcpProvider({id,executable,args,env})` 构造，CLI 不推断品牌、登录状态或默认工具权限。`prepare/collect` 缺失、没有 Provider 时启动前拒绝，不用空成功实现占位。原生登录和 Publisher 分权仍由部署方证明，不复制 HOME/凭据、不宣称同 UID 是恶意沙箱。

## JavaScript 接口

```js
const service = await startTaskService({
  root, mode: 'create', providers, prepare, collect,
  // 可选：dispose、providerFacts、applicationOptions、supervisorOptions、
  // port、leaseMs、renewIntervalMs、requestTimeoutMs、onDiagnostic。
});
// {address, connectionFile, snapshot(), shutdown()}
const result = await service.shutdown();
```

服务不返回 token 字段；启动输出只含地址、profile 和私有连接文件路径。连接文件为 `0600`，位于 `0700` 的 `connections/`，内容为 `{profile,url,token}`。每次启动生成不同文件和 token，重启客户端读取新的 connection，再对原 Task ID 使用原请求 key/正文；旧文件保留供定位，不覆盖、不续用旧 token。它不是 owner 权威或 Worker 输入，不能放进 Agent prompt/env/日志。

根布局为 `profile.json`、`store/`、`artifacts/`、`executions/`、`connections/`。profile 仅是不可变组合格式标识；owner、任务、预算和命令全在 SQLite。初始化会同步文件/目录，失败留下的部分根不自动修复。重开要求完整布局和各组件原格式验证；未知根文件拒绝。执行与历史连接文件不自动 GC。

## Owner、就绪和关闭

- SQLite 持有物理独占锁；打开当前活跃服务根会失败，不因 lease 到期抢占。每次真正重开显式 claim 新 generation。
- 默认 lease 60 秒、每 10 秒续租；**在同一同步 turn 把 `renewOwner` 返回的新 owner 更新到 `application.owner`**。不会以新 claim 替代续租。owner/根身份/全局 Supervisor 故障会停止准入并关闭原持有执行，未知清理不得伪报成功。
- `/health` 只说明 HTTP 进程活着；`/ready` 还检查当前 Store/owner、Supervisor 和旧执行义务。存在旧代未决执行时返回 `not_ready`，保留查询与取消，阻止 HTTP 新任务、批准、恢复、回答和输入提交。它不证明模型登录、外部业务验收或完整 API 支持。
- 旧 `unknown` 命令始终保留恢复阻断；旧 `pending` 只有在同库重读确认 Task 已取消/失败/完成或取消 fence 生效、命令尚未预留且没有关联 Worker 时，才从 readiness 阻断中排除。此处只作观察，不修改旧命令、不退款、不重新派发，因此未执行 Task 取消后不会永久锁死新任务。
- 认证后，写请求先通过当前 owner 执行 Application 原回执查询，再判断新请求 readiness。原 key/正文返回原响应，同 key 不同正文返回 `idempotency_conflict`；只有未命中回执的新请求才受恢复准入 gate 限制，不用 503 掩盖已提交结果或冲突。
- `provider.list` 是显式配置投影；默认 availability 为 `unknown`、能力列表为空，不把有 `start()` 方法等同于实机可用。可信 `providerFacts` 可提供已验证描述；服务不会自动发付费模型探测。
- `supervisor.get` 从当前 SQLite Task、outbox、容量与当前控制器状态计算只读观察，不把内存 Worker 数当作全库容量。观察扫描每类最多 2500 条，超出返回 unavailable，而非发布截断数字。`queuedTasks` 统计 draft/queued；`blockedTasks` 统计 intervention/awaiting-answer/awaiting-approval/paused。
- 正常 `shutdown()` 幂等：关闭接单、等原 Supervisor completion/cleanup、保持续租让结果写回、关闭 HTTP 连接，再调用 dispose 和关闭 depot/Store/目录 FD。HTTP drain 有界；Provider 违反原有界停止合同时仍可能等待，不用超时假造 cleanup。
- `shutdownClean` 只说明本控制器当前持有执行已清理并写回，不证明旧 generation 的未知执行已恢复。缺 cleanup 时为 false，持久 intervention 保留。
- **关闭在途服务不是暂停/无损续跑**：原 Supervisor 会停止当前 Worker，Application 按真实事实失败/取消收口。已终态 Task 和原幂等回执可冷重开；崩溃后的未知执行保留 intervention，不按裸 PID 杀进程、不重派、不复用目录。

四项运行观察通过明确 composition dispatch 处理；其余操作原样交给注入了 depot 的 `TaskApplication`。已接线的输入上传、manifest 与 bytes 下载使用真实 SQLite/depot；尚未接线的问答等操作仍返回原 unsupported，不冒充 24 个接口全部可用。独立 finalization、验收绑定、最终交付制品与 Task completed 必须由同一 Application/Store 后继实现，不放在此入口。

## 验证

```sh
node --test --test-concurrency=1 packages/task-service/composition.test.mjs
```

测试使用真实 loopback、真实 SQLite/depot 和受控 Fake Provider；覆盖输入上传/下载与冷重开、续租跨初始期限、计划批准双 Worker、取消、真实等待 completion、冷重开原回执、持锁竞争、根漂移、旧代未知义务、缺 cleanup，以及独立 Node CLI 的 SIGTERM。测试里的 cleanup 是明确夹具事实，既不是实机 OS 清理，也不是模型/业务通过。真实双 Qwen、独立验证后下载消费、活跃 crash 恢复、Linux 和同资产部署仍须另行验收。
