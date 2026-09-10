---
name: marshal-client
description: 通过已有 Marshal Node HTTP 服务提交和跟踪团队任务、转达待确认问题、取消任务并获取交付物与审计。适用于用户要求使用 Marshal 协作，或继续已有 Marshal task；不是通用编码流程或本机 Worker 启动器。
---

# Marshal 对话接入

作为用户与 Marshal 的交互入口：理解需求、传递上下文和确认、展示进展与结果。任务内的分工与业务决策交给 Marshal Leader，调度、取消和恢复交给 Core；不要自行启动多个 Qwen/Pi、另建 DAG、盲目重试或直接操作 Marshal 数据库。

这是独立的产品客户端 Skill，不是历史 Marshal 研发治理 Skill，不要求开发切片、ADR、PR 或固定 review 轮次。可由 DataAgent 等支持 Skill 的宿主使用。

## 连接与实际能力

新版发行若包含 `packages/task-local/main.mjs`，优先调用产品的 `init`、`serve`、`status`，不在 Skill 中实现探测或进程管理。`init` 自动识别自身安装根和本机 Agent 路径；`serve` 保存本次启动连接，后续通过产品导出的 `connectLocal()` 获取 TaskClient。服务配置首次提供后复用。这些命令属于后继源码，v1.0.1 不包含它们；旧发行使用下述兼容方式，不伪造新命令可用。没有受信业务配置时，不把检测到的 Agent 当成已启用的通用执行服务。

需要已安装的 Marshal Node（建议 v1.0.1+）、Node22+、已配置并运行的服务。连接初始化只做一次，复用以下两项：

- `MARSHAL_INSTALL_ROOT`：可信发行安装根，含 `packages/task-client/index.mjs`。
- `MARSHAL_CONNECTION_FILE`：本次服务启动输出的 `connectionFile` 路径，内容为连接信息；仅在客户端进程内读取，不打印、不放进 Task prompt、Worker 环境或聊天记录。

**环境变量是可选的传参方式，不是准入条件。** 用户已在对话中提供安装位置，或安装器/启动器已返回可信路径时，直接通过下面的位置参数使用，不再要求用户 export。不要因环境变量为空而忽略已有信息。连接文件属于一个 Marshal 服务，不是每个业务系统或每个 Worker 都要配置；多个任务复用同一服务。它保存地址和认证信息，不是 Agent 登录配置。

不扫描 HOME 寻找连接文件或 Agent 凭据，不按修改时间猜历史连接。重启后使用本次启动输出的 `connectionFile`。缺少信息时只列真正缺少的一项：安装成功但没有启动记录，应说明“尚缺服务启动”，不要让用户反复设置空变量。未配置的通用业务能力不能由 Skill 伪造；不擅自启动另一份服务或修改 Provider。

优先使用安装包内的 `TaskClient`，它校验请求/响应、对象绑定、下载摘要并拒绝重定向；只支持显式 `http://127.0.0.1:PORT`。远端 Sandbox 应在同一个 Sandbox 内调用，不能直接把 token 发给任意公网 URL。

先检查 `ready.get`，再按需查询 `provider.list`。就绪或 Provider 存在不证明某个业务工厂、Leader、运行问答或发布操作已启用。当前服务需要受信业务配置；Skill 本身不能把固定报告业务变成任意 SQL 发布/补数据平台。收到501或能力缺失时明确报告，不能降级为直接执行 Worker。

## 最小调用方式

下面代码兼容已有发行 SDK，不依赖新增服务器接口。已有环境变量时原样运行；否则在 `node --input-type=module -` 后追加两个带引号的位置参数：可信安装根、启动器返回的连接文件绝对路径（不要把占位符当真实路径）。路径来自已有上下文时由客户端 Agent 填入，无需用户再操作。其余调用复用初始化得到的 `client`。

```sh
node --input-type=module - <<'NODE'
import fs from 'node:fs';
import path from 'node:path';
import {pathToFileURL} from 'node:url';
try {
  const root = process.argv[2] ?? process.env.MARSHAL_INSTALL_ROOT;
  const connectionFile = process.argv[3] ?? process.env.MARSHAL_CONNECTION_FILE;
  if (process.argv.length > 4) throw Error();
  if (!root || !connectionFile || !path.isAbsolute(root) || !path.isAbsolute(connectionFile)) throw Error();
  const {TaskClient} = await import(pathToFileURL(path.join(root, 'packages/task-client/index.mjs')));
  const fd = fs.openSync(connectionFile, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW | fs.constants.O_NONBLOCK);
  let connection;
  try {
    const stat = fs.fstatSync(fd);
    if (!stat.isFile() || stat.size > 16384 || stat.uid !== process.getuid() || (stat.mode & 0o077) !== 0) throw Error();
    const bytes = Buffer.alloc(16385);
    const length = fs.readSync(fd, bytes, 0, bytes.length, 0);
    if (length > 16384) throw Error();
    connection = JSON.parse(bytes.subarray(0, length).toString('utf8'));
  } finally { fs.closeSync(fd); }
  const client = new TaskClient({baseURL: connection.url, token: connection.token});
  console.log(JSON.stringify(await client.request('ready.get')));
} catch {
  console.error('Marshal 连接或就绪检查失败；检查受信路径、当前连接文件和服务状态。');
  process.exitCode = 1;
}
NODE
```

成功业务响应可能含业务敏感信息，只向用户展示需要的摘要；不要原样转储连接对象或配置。处理 `TaskClientError` 时可显示其 `code/status/requestId/allowedActions`，不显示原始异常、HTTP头或凭据。完整机器合同在安装包 `packages/task-api/openapi.json`；仅在需要具体字段时读取对应 schema/operation，不凭空添加参数。

## 从需求到交付

1. 整理用户目标、必要资源说明、交付物和验收要求。已有信息足够就提交，不要求每个任务重新问一遍。只有不明确的目标、资源范围或高风险操作才补充确认。仓库、表、时间范围放在 `context.text`，不要创建 Workspace 对象或把秘密放入正文。
2. 用 `client.createTask(body, key)` 提交；保存返回的 `task.id`。body 示例：

   ```json
   {"intent":"完成用户已确认的任务","context":{"text":"在此补充相关资源、输入与操作边界；只授权本次约定的工作，不隐含外部发布权限。"},"requirements":{"deliverables":["约定的交付物","独立检查结果"],"acceptance":["满足用户确认的验收要求"]}}
   ```

   这是请求格式示例，不是该业务已接线的保证。已有 taskId 时先查询原任务，不为“继续”重复创建。
3. 查询 Task、plan 和需要的 questions/leader 投影。向用户展示真实计划、验收和将发生的操作；批准应对应用户已授权且看到的精确计划。调用 `approveTask` 时保留确认过的 Task revision、Plan revision/digest，不猜值、不自动批准扩大范围的新计划。
   调用形状：`client.approveTask(taskId, {expectedRevision, planRevision, planDigest}, key)`；三个版本/摘要值来自本次真实查询及用户确认，不使用示例常量。
4. 执行中查询进展，转达问题或授权请求；用户无需关注低层日志。按用户关注程度短时观察，例如每5–15秒查询、遇到变化再展示，单轮观察到约60秒或需要用户输入即返回当前状态。不要把观察超时当失败，不无限空转；没有宿主后台机制时不要声称仍在后台监控。
5. 完成后读取 audit、制品及可用的 Leader 发布/后验状态。Task completed、Worker退出、202受理都不是“数据已发布/补齐”的替代证据；按实际验收与发布/后验结果说明已完成、未完成和未知项。

## 常用 SDK 操作

以下是 `client.request(operation, options)`，除创建示例外的写入都需按当前合同填写 body 和 `idempotencyKey`。所有 ID 从真实响应取得。

| 目的 | operation / options |
| --- | --- |
| 就绪、可用 Provider | `ready.get`、`provider.list` |
| 查询任务、计划、DAG、Workers | `task.get/plan/graph/workers`，`{path:{taskId}}` |
| Worker 进展 | `worker.get`，`{path:{workerId}}`；仅展示实际暴露的进度，不编造内部思考或工具轨迹 |
| 问题、Leader 状态 | `task.questions`、`task.leader`，`{path:{taskId}}`；Leader 未启用时501不是整个任务失败 |
| 事件、审计 | `task.events`、`task.audit`，`{path:{taskId}}`；事件是分页查询，不是 SSE |
| 任务取消 | `task.cancel`，`{path:{taskId},body:{expectedRevision},idempotencyKey}` |
| Worker 取消 | `worker.cancel`，`{path:{workerId},body:{expectedRevision},idempotencyKey}`；revision 属于该 Worker 的 Task，且需服务启用此能力 |
| 控制回执 | `operation.get`，`{path:{operationId}}`；取消返回202后继续观察回执，不直接声称进程已停止 |
| 制品 | `artifact.get`，`{path:{artifactId}}`；下载用 `client.downloadArtifact(artifactId)`，得到 `{artifact,content}` 后再写用户指定的新文件，不执行下载内容 |

仅支持分页的操作可加 `query:{limit:50,cursor}`，cursor 沿用响应值；不要把未翻页当完整审计。暂停、继续、局部修正也有契约，但仅在用户意图和实际支持吻合时使用，不替代 Leader 自行决定业务重试。

## 问答与授权不是同一件事

- 规划问答：`task.answer` 的 body 为 `{expectedRevision,previewDigest,questionRevision,answer}`；题目来自 `task.questions`。
- 运行问答：同一 operation 使用 `{expectedRevision,questionDigest,questionRevision,answer}`。两类摘要不可混用；options 非空时提交用户选择的原选项值。path 为 `{taskId,questionId}`。
- Leader 请求：读取 `task.leader` 的 `pendingRequest`，使用 `task.leader.reply`，path 为 `{taskId,requestId}`。业务答复 body 为 `{expectedRevision,requestDigest,answer}`；发布授权 body 为 `{expectedRevision,requestDigest,decision:'allow'|'deny'}`。不能混用；原样展示待授权目标、动作和制品范围，只有用户授权覆盖该精确动作才发送 allow。
- 问题答复的202只表示接纳，不等于 Worker 已收到。Leader reply 不返回旧 Operation，不能把它的 receiptId 当 operationId；应查询原 Leader/Task 状态。规划答复也不会自动 approve。
- Agent 已登录、用户说“开发 ETL”或允许制定方案，都不自动授权生产发布、SQL执行或补数据。

## 丢响应和冲突

每个写命令先生成一次 key（例如 `cmd-` 加 UUID），在发送前将 key、精确 body 和任务关联保存到宿主可恢复的私有操作记录，绝不存 token。这只是请求重放信息，不建立另一套任务状态机。SDK不会自动生成 key 或重试。

写入丢响应/504时，先查询已知 Task/Operation；仍需取回写入回执时，可在原授权范围内用同一 operation/path/key 和完全相同 body 显式重放，包括批准与取消。创建尚无 taskId 时同样用原请求取回原 Task，不换 key 重新创建。若原 key/body 已丢失，不猜一个新批准请求。409时重新观察并解释冲突，不能自动改 revision/digest 后重做批准或发布。401/403停止写入并确认当前连接/授权；501报告未启用；失败、unknown 或内容反馈本身不授权重派整个团队。
