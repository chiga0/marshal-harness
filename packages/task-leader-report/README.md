# 可安装的 v7 日期窗口区域报告示例

本目录是受限业务族 `leader-regional-window/v1` 的受信生产配置示例，不是任意任务入口，不是网站产品。它复用原 Service、Pi 原生工具权限桥、FileBusiness、Leader/独立 Review、原 `task-regional-window` 数据校验与固定 Node checker、本地报告发布和真实 GET 后验。配置相同，Task 的原流水、需求和日期可以不同；缺日期从原 Leader 问答取得，不在配置里预设答案。自然首轮正确不制造 rework。

代码可由现有发行库存打包。这里的无模型测试不能证明真实 Pi 完整业务验收或 stable 发行已完成；模板不授予发布权限。强 OS/凭据隔离、任意 shell、外部平台发布均不在此示例范围。同用户原生工具并非恶意代码沙箱。

## 启动同一固定配置

先按现有候选接纳流程核验安装包的精确 source、manifest 和全部 bytes。`PACKAGE` 指该已核验目录，不允许以任意下载脚本替代它。安装包无需仓库、Git 或 fixture。

准备两个现有的、当前账号所有且为 `0700` 的私有目录：一个新服务状态根的父目录、一个报告根的父目录；报告根本身也须 `0700`，与 service/Store/Depot/execution 目录互不包含。不要移动已绑定的报告根或手删锁。以下路径由操作者明确选择，不能来自 HTTP Task。Node 必须为已经安装的 `24.15.0`；保留当前账号原 HOME/Pi 登录，不复制任何凭据。

```sh
NODE=/absolute/path/to/node-v24.15.0/bin/node
PACKAGE=/absolute/path/to/verified-package
export MARSHAL_PI_ENTRY=/absolute/path/to/pi-coding-agent/dist/bundle/cli.js
export MARSHAL_PI_SDK=/absolute/path/to/pi-coding-agent/dist/index.js
export MARSHAL_REPORT_ROOT=/absolute/private/report-parent/reports
export MARSHAL_REPORT_URL=http://127.0.0.1:19091/reports/
"$NODE" "$PACKAGE/packages/task-leader-report/report-server.mjs" --root "$MARSHAL_REPORT_ROOT" --port 19091
```

另一个终端保留相同显式配置：

```sh
"$NODE" "$PACKAGE/packages/task-service/main.mjs" \
  --root /absolute/private/service-parent/data --mode create --port 19090 \
  --config "$PACKAGE/packages/task-leader-report/service-config.mjs"
```

原 CLI 输出 `connectionFile` 路径，不输出 token。该文件为 `0600`；只在受信本地客户端内读取，不粘贴、上传或写日志。启动本身不调用模型；提交 Task 后 Leader 和作者会使用原 Pi 模型并消费预算。缺少明确 Pi 路径、固定 Node 或报告根会启动失败，不回退成 Fake/旧 profile。

报告读取服务仅开放固定 loopback GET 路径，拒绝写入、任意路径/链接和过大文件。它不接收 Task token；同机其它进程可以读取已经发布的有限报告，因此本示例只用于明确可披露的业务数据。原 Task/回复和上传数据仍按私有服务存储处理，默认审计不披露完整提示词。

## 原 HTTP 客户端操作

可以在固定 Node REPL 中使用安装包的现有 `TaskClient`；下列均为用户显式操作，不是自动批准驱动。声明 `PACKAGE` 和 `CONNECTION` 为实际绝对路径后初始化：

```js
const fs = await import('node:fs');
const {pathToFileURL} = await import('node:url');
const {TaskClient} = await import(pathToFileURL(PACKAGE + '/packages/task-client/index.mjs'));
const {taskBody} = await import(pathToFileURL(PACKAGE + '/packages/task-leader-report/policy.mjs'));
const connection = JSON.parse(fs.readFileSync(CONNECTION));
const client = new TaskClient({baseURL: connection.url, token: connection.token});
```

`sales.json` 闭集为 `{rows:[{date,region,status,cents}]}`：1–512 行、总 UTF-8 不超过 64 KiB；日期为有效 `2000–2099` UTC 日历日；region 为 east/west，status 为 paid/cancelled，cents 为绝对值不超过 `10^12` 的整数。只接收 JSON 数据，不执行上传代码。例如可公开的原输入：

```json
{"rows":[{"date":"2026-09-01","region":"east","status":"paid","cents":1200},{"date":"2026-09-02","region":"west","status":"paid","cents":-50},{"date":"2026-09-02","region":"east","status":"paid","cents":0}]}
```

```js
const uploaded = await client.request('input.create', {idempotencyKey: 'my-input-1', body: {
  name: 'sales.json', mediaType: 'application/json', contentBase64: fs.readFileSync('/absolute/path/sales.json').toString('base64')}});
const created = await client.createTask(taskBody(uploaded.id), 'my-task-1');
const taskId = created.id;
await client.getTask(taskId);
await client.getLeader(taskId);
```

缺项时等待 `pendingRequest.kind === 'business'`，读取原问题后明确回答。例子中的日期须换成你的实际需求；不能改已给的日期。每次使用新 Task 的新 key，重放丢失响应时保留原 key/body/CAS，不自动刷新：

```js
const current = await client.getTask(taskId);
const view = await client.getLeader(taskId);
const answerRequest = {path: {taskId, requestId: view.pendingRequest.id}, idempotencyKey: 'my-answer-1', body: {
  expectedRevision: current.revision, requestDigest: view.pendingRequest.requestDigest,
  answer: JSON.stringify({startDate: '2026-09-01', endDate: '2026-09-02'})}};
const answerReceipt = await client.request('task.leader.reply', answerRequest);
```

也可在 `taskBody` 第二参数一次给全两个日期，或只给一个、另一个为 null；完整输入无需问答。`taskBody` 默认 10 分钟、17 Attempts、3 Workers 是原硬上限，不是强制调用次数。由操作者按原 HTTP `task.plan` 检查当前计划：仅 east/west 作者、唯一 verify sink、两个依赖、原输入映射、有限输出、独立验证及当前 Leader/发布策略都应在验收项中。只在确认后批准一次：

```js
const ready = await client.getTask(taskId);
const plan = await client.request('task.plan', {path: {taskId}});
const approval = await client.approveTask(taskId, {expectedRevision: ready.revision,
  planRevision: plan.revision, planDigest: plan.digest}, 'my-approval-1');
```

作者只能读原 sales.json/自己的报告，write/edit 只能指向自己报告，Pi `allow-once` 原参数闭集；读写别的作者、上传输入、链接、shell/MCP 等都不因角色或原生登录获准。独立 checker 在原所属进程中按原 Depot 数据和原回答重算，并核对实际候选；publicationExpected 也从原输入独立计算，不拿已输出报告当期望值。

当 Leader 公布 `pendingRequest.kind === 'publication'`，先下载 `authorization.artifactId`，独立检查报告、原 Review/验收证据，以及 `authorization` 的原 Task/plan/artifact digest/bytes/target/name/期限。目标必须是已配置的 `local-window-report`。只有核对并明确同意本次精确发布后才提交：

```js
const beforePublish = await client.getTask(taskId);
const publishView = await client.getLeader(taskId);
const report = await client.downloadArtifact(publishView.pendingRequest.authorization.artifactId);
const audit = await client.getAudit(taskId);
// 此处由用户检查 report、audit、publishView；未同意可用 decision:'deny'。
const allowRequest = {path: {taskId, requestId: publishView.pendingRequest.id}, idempotencyKey: 'my-publication-1', body: {
  expectedRevision: beforePublish.revision, requestDigest: publishView.pendingRequest.requestDigest, decision: 'allow'}};
const allowReceipt = await client.request('task.leader.reply', allowRequest);
```

等待原 `task.status === 'completed'` 且 Leader review、publication、postverify 和总结证据齐全；不能将下载成功或进程 exit0 当发布完成。GET `MARSHAL_REPORT_URL + authorization.name` 是实际消费目标。取消、超时、未知清理/外部状态按原 Core 合同处理；示例不会为了通过而重派或提升预算。

## 正常重开与证据范围

用原 CLI 的 SIGTERM 正常停服并确认最终 `clean:true`。保留整个数据根与原报告根/URL/同包配置，随后把原命令改为 `--mode open`。重新读取新 connectionFile，用原 Task ID、原批准/回答/allow key+body 查询与精确重放；不能换配置、清库、删锁或重建旧 Task。报告 reader 可在同端口/原根正常重开，但不是 Supervisor 的后台恢复替身。

本包测试仅注入外置受控 ACP 协议进程作为模型替身，仍运行安装包内真实配置工厂/CLI/HTTP/SQLite/guard/checker/发布/GET，并验证同配置两套原流水与回答、两作者交叠、原回执和成果冷开不重复。它不声称 ACP 夹具就是 Pi 模型；Pi 原权限形状另有闭集负例。本包不提供私有 receipt、Fixture 入包、自动 Task 批准或发布授权。

```sh
"$NODE" --test --test-concurrency=1 packages/task-leader-report/index.test.mjs packages/task-leader-report/report-server.test.mjs packages/task-leader-report/installed.test.mjs
```
