# 正式 Node 服务组合根

## v7 受管 Leader：可运行检查点，尚非完整出口

显式配置 `leader`、`review`、原 `verification`、`custody` 和真实 `createFileBusiness` 工厂后，新根采用 layout 7 / `marshal-node-task-sqlite/v7-managed-leader`。旧配置继续沿原格式，已有根不能原位迁移；配置与发布目标身份在 claim 前比对。v7 的原句柄、定时调度和停止属于 `TaskExecutionCoordinator`，Core 保留同一 SQLite 准入/预算/结果事务；独立 Review 与客观 Verification 不是作者自签，阶段验收也不直接完成 Task。

端口由 `task-application/application.mjs` 导出 `createLeaderPort`、`createReviewPort` 及固定 `renderLeaderPrompt` / `renderReviewPrompt` / `parseManagedOutput`。端口 `prepare({ticket,input,prepared},context)` 只返回 `{prompt}`；原私有 cwd 由 FileBusiness 的受限准备创建，不由模型或自由回调指定。业务作者实际 prompt 包含原需求、批准 Plan、验收与原 Leader 回答/引用。`task.leader`、`task.leader.reply` 沿正式 HTTP 和 TaskClient，不读取私有 Ledger 扮演用户。

`leader.test.mjs` 在同一业务/验收配置下处理两个不同需求，经过真实 HTTP、SQLite、原 ACP/Node guard/custody、双作者文件、独立 Review、原受管 checker、Leader deliver/conclude、下载独立复算和正常关闭后同根重开；分别执行无发布和原本地报告发布/实际 HTTP 后验两种配置。Agent 是显式确定性协议夹具，不调用模型。运行入口：

```sh
node --test --test-concurrency=1 packages/task-service/leader.test.mjs
```

当前冻结只交付可独立复跑的集成检查点：v7 的在途 SIGKILL 恢复、混合未决义务、独立意见触发 repair、运行中 Worker 问答的交叉故障矩阵尚未验收；不得将旧格式通过或这里的正常 cold open 替代这些门禁，也不得据此宣称 B2-L 完成、真实模型或 production。首次 HTTP 夹具错误使用了非合同 Context/Requirements，客户端在发送前拒绝、零 Worker；已改用原 Schema 的 `context.text` 与 `requirements.deliverables/acceptance`，未放宽接口。

可选 `publication` 使用原本地报告端口；受信 `createVerificationPort({..., publicationExpected({ticket})})` 从冻结原输入/回答生成完整后验期望，禁止从 Leader 的 pass 字段生成。原始 `created/matched/passed` 事实保留，公开 LeaderView 映射为 `succeeded`。本检查点的发布端口由配置拥有者在 `shutdown().shutdownClean === true` 后 `close()`；冷开必须重新构造同目标身份/配置的端口，不复用失效 FD。

后验期望的私有能力在 root create/open/claim 前校验；本 Task 的具体期望在 publication reservation 和外部 create 之前同步生成，随原 ticket/action 冻结，后验不再次读取可变配置。构造失败不消耗 publication Attempt，也不启动原发布端口。取消或原期限先赢时，已产生的原绑定发布回执仍保存；只禁止后继，并不把已发生的效果改写为未发生。缺效果证明即使进程已清理也保持 unknown。

`leader-revisions.test.mjs` 是真实 SQLite/Depot 加明确受控 Provider 事实的聚合反例：等待/陈旧决定保留聚合义务、取消/期限保留原发布事实、启动前期望校验以及 ordinary/structural/nonretryable 失败分类。它不是外部发布或 OS cleanup 证明。`leader-followup.test.mjs` 另使用真实 HTTP/原 guard，覆盖答复批准后取消和独立 Review→仅错误分支修正；确定性 peer 主动写入的测试错误不是自然模型错误证据。

这是 ADR0088 的 Node-only 组合入口：复用同一个 `TaskApplication`、SQLite `Store`、`ArtifactDepot`、`TaskSupervisor` 和 HTTP handler。不执行 Marshal 原生文件，不创建另一套 Task 状态或决策控制器。API/client 检查点已 `PASSED`；逐接口实现和模型/夹具验证范围见[支持矩阵](../../docs/node-api-support-matrix.md)。这不等于整体 B2/B3、正式部署或 `RELEASED` 已完成。

## 一条命令启动

```sh
node packages/task-service/main.mjs \
  --config /absolute/trusted/service-config.mjs
```

运行环境是已允许的固定 Node 24.15.0。默认数据目录为当前用户 `HOME/.marshal-node/task-service`，独立于旧 `.marshal`。HOME 只作为 canonical、当前 UID 所有、不可由其他用户写入的路径锚点；不扫描或复制其中内容，不改变 HOME 环境、登录或现有权限。缺失的 `.marshal-node` 父目录自动建立为 `0700`，再由原 composition 创建全新服务根、SQLite 和私有 token。

可选 `--data-dir /absolute/private-parent/task-data` 选择本机内部数据位置；与旧 `--root` 互斥。显式目录的最近已存在锚点必须 canonical、当前 UID 所有且为 `0700`；从该私有锚点向下仅创建缺失父目录（最多32层）。每次启动都向上重建连续同 UID/`0700` 私有祖先链（包含新建父目录总共最多64层），仅检查路径元数据，不扫描内容；到非私有边界停止，不采用共享 `/tmp`、他人目录或宽权限父目录作为可写锚点。整个私有链在交给原组合根前完成子→父同步与身份重查；失败保留目录、拒绝启动，不删除不确定现场。重试也必须重新同步这些祖先，不以“深层目录已经存在”略过上次失败的祖先耐久屏障。

省略 `--mode` 等同 `--mode auto`：根不存在才 `create`，已经存在则只 `open`。旧 `--root ... --mode create|open` 保持可用；`create` 永不覆盖已有根，`open` 永不创建缺失目录。已存在空根、部分初始化、损坏/未知格式、符号链接、非所属或宽权限目录均拒绝，不 chmod、不重新初始化、不从旧 Go/实验根导入。并发 owner 仍由原 SQLite 独占锁拒绝，不删锁或抢占。自动模式不根据失败原因再次切换模式，不重试。

端口默认 `0`，仅监听 `127.0.0.1`，可显式 `--port`。启动输出仍只有 profile、监听地址和受保护连接文件位置，token 由原 composition 每次自动生成且不回显。`--config` 始终必需：不猜 Provider、模型、登录或默认成功业务，缺少有效配置仍返回原 `service_start_unavailable`。薄启动入口只负责路径/模式，不拥有 Task 真值或恢复权限。

配置模块是受信任部署代码，不是 HTTP 插件或用户提示词。它应默认导出：

可选顶层 `clarification` 接受 `task-application` 的 `createClarificationPort` 原对象，由组合根注入同一 Application。未安装时保持原零问题规划路径；安装后仅匹配且真正缺少声明业务槽的 Task 进入有限问答，不按长度/关键词泛猜缺失。`awaiting-answer/awaiting-confirmation` 计入 blockedTasks，但不凭此派 Planner；真实 answer/approve/cancel、原回执与冷查询都走同一 HTTP/SQLite。模板配置漂移拒绝新答复/确认，历史仍可查。有限测试模板不作为公开业务能力或 B2 完成证据。

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

示例中的变量由实际业务组合提供，不是可直接执行的假实现；`service.fixture.mjs` 只供无模型测试。Provider 可由已有 `createAcpProvider({id,executable,args,env})` 构造，CLI 不推断品牌、登录状态或默认工具权限。没有 `prepare/collect` 且没有有效 `businessFactory`、或没有 Provider 时启动前拒绝，不用空成功实现占位。Agent 原生登录由部署方配置；[ADR0094](../../docs/adr/0094-trusted-single-user-role-team.md) 的 Node 可信单用户目标保留职责、发布授权和独立证据边界，强 OS 账号/凭据隔离证明后置，明确接受 ambient credential 风险。不复制 HOME/凭据、不主动发现或输出秘密、不宣称同 UID 是恶意沙箱；登录不自动授予产品外部写权限。旧 Go/hardened profile 不适用该调整。

## 可信业务工厂与唯一 verification 实例

真实文件业务推荐使用工厂，在 App、depot 和执行目录已经存在后构造；不必预先打开另一个 Store 或 depot：

```js
import {createFileBusiness} from '../task-business/index.mjs';

export default {
  providers,
  verification: trustedVerificationPort,
  businessFactory: ({executionParent, depot, approvedLayout, observeExecution}) =>
    createFileBusiness({
      parent: executionParent, depot, approvedLayout, observeExecution,
      layoutFor: ticket => ticket.planDigest === null
        ? {inputs: [], allowedPaths: []} // 初始 Planner 无写入批准。
        : ticket.input.fileLayout,
    }),
};
```

`trustedVerificationPort` 由同 Core 导出的 `createVerificationPort({id,policy,bindPlan,start})` 在可信配置中构造。service 将**同一个对象**传给 Application 与 Supervisor，不复制、重建或反序列化原 receipt 能力。政策、完整文件布局与交付映射由 Core 在计划确认前冻结；独立执行与证据由原端口提供，service 不造 Decision。

[Leader 目标设计](../../docs/node-leader-execution-design.md#任务验收内容与可信验证机制)进一步区分可信验证机制与任务验收内容：后者来自已确认需求，独立检查其覆盖，不要求每个任务重写 Core/部署配置，也不接受 Agent 任意生成并执行验证代码。本段不改变当前 port 或启用动态配置；该目标仍随 B2-L 实现和验证。

工厂只收到 `{depot,executionParent,approvedLayout,observeExecution}`，不暴露 Store、owner 或写 reducer。后两个函数在调用时进入当前 Application `execution.approvedLayout(ticket)` 与 `execution.observeExecution(ticket)` 只读端口，不能把当前 layout 现算成批准事实。方法尚未接入时拒绝，不返回伪造绑定。

工厂必须同步返回 `{prepare,collect,release,close}`；与直接 `prepare/collect/release` 配置互斥。不能在构造时启动 Agent，工厂内部抛错之前自行清理它尚未返回的资源。已返回对象的 `close` 会在启动后续失败或正常 shutdown 中执行一次，然后再关闭 depot/Store；原可选 `dispose` 保留作额外受信清理。

`release(ticket)` 原样接入 Supervisor 的逐 Worker finally，成功、失败、取消、准备失败与验收终止都不等到全服务关闭才关文件 FD；它必须同步且只释放本业务适配器资源，不写 Task、改预算、发信号或删除目录。直接回调方式也可显式提供 `release`。`close()` 只是最终兜底，不替代逐 Worker 释放。

## JavaScript 接口

```js
const service = await startTaskService({
  root, mode: 'create', providers, prepare, collect,
  // 或：businessFactory + verification，替代直接 prepare/collect/release。
  // 可选：release、dispose、providerFacts、applicationOptions、supervisorOptions、custody、
  // port、leaseMs、renewIntervalMs、requestTimeoutMs、onDiagnostic。
});
// {address, connectionFile, snapshot(), shutdown()}
const result = await service.shutdown();
```

服务不返回 token 字段；启动输出只含地址、profile 和私有连接文件路径。连接文件为 `0600`，位于 `0700` 的 `connections/`，内容为 `{profile,url,token}`。每次启动生成不同文件和 token，重启客户端读取新的 connection，再对原 Task ID 使用原请求 key/正文；旧文件保留供定位，不覆盖、不续用旧 token。它不是 owner 权威或 Worker 输入，不能放进 Agent prompt/env/日志。

默认 v1 根布局为 `profile.json`、`store/`、`artifacts/`、`executions/`、`connections/`。profile 仅是不可变组合格式标识；owner、任务、预算和命令全在 SQLite。初始化会同步文件/目录，失败留下的部分根不自动修复。重开要求完整布局和各组件原格式验证；未知根文件拒绝。执行与历史连接文件不自动 GC。

## 可选 custody v2 与恢复边界

### 单 Worker 取消：显式 v6 新根

[ADR0093](../../docs/adr/0093-node-worker-cancellation.md) 沿原组合增加 `workerCancellation: {profile:'task-worker-cancellation/v1'}`；必须同时配置原 `custody`。只有显式新根选择 `layout:6` / `marshal-node-task-sqlite/v6-worker-cancellation`，不迁移或重标记已有根，旧 v1–v5 reader 在 claim 前拒绝。缺省配置保留原格式与 `worker.cancel` 的 501，不因客户端认识新响应字段而自动启用。

`POST /v1/workers/{workerId}/cancel` 继续使用 `{expectedRevision}`，比较所属 **Task revision**；202 返回带原 `workerId` 的 Operation，仅表示受理。原 key/body 可精确重放，客户端不自动刷新 CAS。原目标清理已证实后该 Operation 可 succeeded，不等待无关兄弟；兄弟仍按原图、预算和期限继续。未执行的后继取消，不伪造 Worker；全部剩余义务结束后 Task 为 failed/worker_cancelled，除非原全局取消/期限/未知效果等安全处置先赢。不产生独立 rejected Decision，不使用户取消自动变成 repair。

这与 `unpermitted` **正交**：普通文件与 Git 业务拥有原受管句柄时均可目标取消；不配置 `unpermitted` 的 v6 reservation 没有0092资格。若另启用 `unpermitted`，仍须原受限工厂与 metadata-only 检查，Git/任意 prepare/披露回调在打开/claim 前拒绝。冷恢复只检查原 ticket 的已绑定资格；后来打开该配置不能给旧 Git reservation 补资格，关闭它也不能删除原合格事实。Git prepare 后、custody bind 前崩溃仍保留未知容量与原目录，不凭无进程记录或三许可缺失制造 cleanup。

`worker-cancellation.test.mjs` 使用原 CLI/HTTP/SQLite、受管无模型协议进程，覆盖目标角色、两个作者隔离、stop/结算 COMMIT 前后 SIGKILL、下一团队独立验证下载和冷重放；配套 Git、问答与 repair 测试验证原分支/答案/结果保留。它们不代表模型实机或部署验收通过。目标取消只查当前 owned handle；停止失败/cleanup 未知保留占用，禁止通过存储 PID 杀进程或重派。

### 新文件业务根：v5 许可前中断结算

依据 [ADR0092](../../docs/adr/0092-node-unpermitted-reservation-settlement.md)，部署模块可以在**全新根**显式选择：

```js
import {createStagingOnlyBusinessFactory} from '../task-business/index.mjs';
export default {
  providers, verification, // 部署方原受信 Provider / 独立 Verification Port，仍须明确配置。
  custody: {profile: 'node-execution-custody/v1'},
  unpermitted: {profile: 'node-unpermitted-reservation/v1'},
  businessFactory: createStagingOnlyBusinessFactory({authorize}),
};
```

`authorize` 可以省略（默认拒绝原生权限请求），只在原许可之后调用。工厂不接收 `layoutFor`、Depot、clock、自定义 prepare；同一 composition 注入原 Depot/执行父目录及只读批准布局端口。原对象和实际 prepare 函数由私有身份登记，复制/包装/替换无法取得 v5 资格。Git/custom prepare 继续原受支持配置，不能通过 profile 字符串升级。

v5 创建 `layout:5` / `marshal-node-task-sqlite/v5-unpermitted`。任何 `auditDisclosure` 回调在打开/claim 根前被拒绝，整个 v5 只保留原 metadata-only 输入审计；并非将回调挪到许可后。旧格式已有披露不改。可同时配置原 `runtimeQuestions` / `repair`，仍按原显式策略启用，不增加问题或返工。

恢复先取得全部已绑定执行的原签名封闭观察，再 claim 新 owner。只有原 reservation 已绑定完整协议、同事务验证原输入/命令/预算/容量并证明三项许可事实均不存在，才追加 `worker.unpermitted-settled`。原 Worker 失败或原取消获胜；`cleanup` 保持 null、Attempt/返工/未知费用不退、期限不延、旧目录保留、不重派。命名例外只在所有义务结清后收口原 unknown 取消/修正 Operation 投影，原幂等回执不改；有绑定/冲突/未知兄弟仍按原签名或缺证据拒绝路径。它不证明尚未许可的辅助 custodian 已退出。

旧 v1–v4 根/reader 与 v5 互相拒绝，不改 marker、不补资格、不迁移或重新初始化。新根只有该受限文件准备路径支持此例外；旧六个 COMMIT 故障用例保留原行为。`unpermitted-recovery.test.mjs` 通过真实原 CLI/HTTP/SQLite、无模型 ACP/Pi 协议进程和独立 checker 验证新格式全部六个 COMMIT 停点（reservation、binding、cancel 各自提交前后）、修正/问答、每个停点后新 Task 下载和冷重开；不是模型或 Linux 部署实证。

固定 Node 的最小组合命令：

```sh
node --test --test-concurrency=1 packages/task-application/unpermitted.test.mjs packages/task-service/unpermitted-config.test.mjs packages/task-service/unpermitted-recovery.test.mjs packages/task-service/control-commit-recovery.test.mjs
```

ADR0089 的执行托管由受信配置显式启用，不接受 HTTP 选择 profile、提交清理证明或加载模块：

```js
import {CUSTODY_PROFILE} from '../agent-runtime/custody-contract.mjs';

export default {
  ...trustedServiceConfiguration, // 已配置的原 providers、业务工厂和同一 verification。
  custody: {profile: CUSTODY_PROFILE}, // node-execution-custody/v1
};
```

此配置在全新根创建 `profile.json` 的 `layout:2`、`marshal-node-task-sqlite/v2-custody` Store 和额外 `custody/` 私有目录。重开必须保留同一配置；省略 `custody` 仍使用原 v1。两种格式互相拒绝，不原地迁移旧 v1，不向历史未决根补签名、公钥或许可，不通过删除状态重置任务。首次启用须选新根；旧根继续保留并使用其原受支持模式。

执行前由原创建 IPC 准备托管实例；公钥、原 reservation 和执行 profile 同库提交后才允许原实例启动。服务断连时托管进程封闭许可、停止自己持有的原 guard，耐久保存签名观察；新服务不连接旧 Agent、重放 prompt 或按存储 PID 发信号。

- v2 重开先持有 SQLite 物理锁，在 claim 新 generation 前有限扫描原 live binding。最多等待 **15 秒**取得并验证全部原签名关闭观察；缺失、签名不匹配或扫描超限会拒绝启动，不开放 HTTP/ready。JavaScript 调用返回稳定错误（缺观察为 `service_custody_unresolved`），CLI 只输出 `service_start_unavailable`。失败保留原数据，不补造“未启动”或重派。
- 有效关闭观察不等于 `cleaned:true`。若原观察存在但清理/作用域不足，新 owner 仍不能结清相应占用，Task 保持 `intervention`、`/ready` 不可用；已启动的 HTTP 保留原查询和取消入口。不要把缺关闭证据的启动失败与已关闭但未证清理的 not-ready 混为一谈。
- Provider 的 `custodyProfile` 是受信构造配置 `{id,scope:'inherited-process-group',eligible}`，不是品牌能力推断或用户自报。省略它默认不 eligible；只有该部署实际执行面已证明全在继承组内，才可设 `eligible:true`。Pi 还要求原受管 bridge，`scopeUnknown`/额外作用域未决继续否决；不能靠 Runtime 签名覆盖。
- 原 command verifier 共用同一托管入口和固定 managed-checker profile。其受信 checker 配置仍须满足继承组边界；`setsid/setpgid`、detached、远端 job 不因此受覆盖，也不把任意 Qwen/Pi 原生配置或同 UID 宿主升级成恶意代码沙箱。
- 仅在当前 owner 重验原绑定和真实清理后，才在同一 SQLite 事务一次结清原 Worker、outbox、Operation 和对应容量。Attempt/预算不退款，原 deadline/批准/幂等回执不改写；只失败/取消收口，不接纳崩溃前遗留业务输出、不生成成功 Decision、不自动重派。原期限已过不妨碍接纳其后真实清理证据。

当前证据限于明确的 Node 进程夹具与 SQLite 恢复测试。服务单进程崩溃、原托管实例/已耐久观察仍存活是此接线的目标范围；托管进程同时丢失、原观察未落盘或 scope 不明继续未决，不能凭操作者一句“已停止”结清。这不声明全部 Provider 的实机 crash、Git 工作树跨代接管、完整 B2、Linux 部署或 production 已通过。

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
- **关闭在途服务不是暂停/无损续跑**：原 Supervisor 会停止当前 Worker，Application 按真实事实失败/取消收口。已终态 Task 和原幂等回执可冷重开；崩溃后的未知执行保留 intervention，只有上述 v2 原证据满足时才做 cleanup-only 收口，不按裸 PID 杀进程、不重派、不复用目录。

四项运行观察通过明确 composition dispatch 处理；其余操作原样交给注入了 depot/verification/可选 clarification 的 `TaskApplication`。已接线的输入上传、manifest 与 bytes 下载使用真实 SQLite/depot；有限问答已接线，未安装/不匹配模板的原 Task 返回零问题，回答未知问题为 not_found。尚未接线的单 Worker 取消仍返回 unsupported，不冒充 24 个接口全部可用。finalization、验收绑定、最终交付制品与 Task completed 只由同一 Application/Store 实现，不放在此入口；没有 verification 的直接回调配置也不自动获得业务完成能力。

## 验证

```sh
node --test --test-concurrency=1 packages/task-service/composition.test.mjs
node --test --test-concurrency=1 packages/task-service/business-integration.test.mjs
node --test --test-concurrency=1 packages/task-service/launch.test.mjs
```

测试使用真实 loopback、真实 SQLite/depot 和受控 Fake Provider；覆盖输入上传/下载与冷重开、续租跨初始期限、计划批准双 Worker、取消、真实等待 completion、冷重开原回执、持锁竞争、根漂移、旧代未知义务、缺 cleanup，以及独立 Node CLI 的 SIGTERM。这两组测试里的 cleanup 是明确夹具事实，不代表实机 OS 清理或模型/业务通过；不能替代真实 Provider、活跃 crash、Linux 和同资产部署的各自证据。

简启动专用测试使用独立固定 Node CLI 和仅子进程可见的临时 HOME，验证首次自动创建、SIGTERM、第二进程打开同一 SQLite、原回执/输入字节与新 token，以及并发 owner/坏根/路径拒绝和旧显式参数。它不改真实 HOME、不调用模型；正常重开不是活跃模型 crash 恢复。

业务组合测试另外使用真实 FileBusiness 与原 Core verification capability，从纯 HTTP 计划批准到完整制品下载，覆盖验收中取消的迟到结果 fence，以及失败后及时释放 FD。模型和 checker 的进程完成/cleanup 明确为受控夹具，不能用该测试代替实机原生工具或独立外部命令验收。
