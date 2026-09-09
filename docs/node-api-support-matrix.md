# Node Task API 逐接口支持矩阵

核对源码：`2d178b4f96caeb2708d861b837160de5cf758f78`（2026-09-09）。本表记录这一源码的实现和已有验证范围，不把历史实机结果改绑到新提交，也不以路由存在代替功能可用。本次编写只读核对源码与已记录证据，没有重新调用模型或重跑测试。

**后继状态提示（2026-09-09）**：下方实现/测试表保留上述源码快照，不是最新 main 的完整矩阵。后继 `25ff8315` 已合入显式 v6 profile 的单 Worker 取消，旧格式仍为 501；最新完成状态和精确证据见 [Roadmap](roadmap-status.md#业务交付当前表)及[服务说明](../packages/task-service/README.md#单-worker-取消显式-v6-新根)。Leader 全程协调、授权交付和后验仍为 DESIGN，不能借已有接口检查点声明完成。

## 使用入口与适用人群

正式文档导航：[产品入口](../README.md) → [服务配置与简启动](../packages/task-service/README.md) → [目录包核验/安装](../packages/task-distribution/README.md) → [OpenAPI](../packages/task-api/openapi.json) / [HTTP 约束](../packages/task-api/README.md) → [独立客户端](../packages/task-client/README.md)。目标和当前完成状态分别看[实施 Milestone](agent-team-service-milestones.md)、[Roadmap](roadmap-status.md)与[实证记录](node-task-service-status-2026-09-08.md)。

适合需要本地 Task 提交、一次精确确认、有限团队执行、业务问答、独立验收与制品下载的部署者和客户端作者。当前是固定 Node `24.15.0`、单节点/单用户/可信任务、显式受信 Provider 与业务/验收配置；没有 Workspace 注册前置，非 Git 制品不要求初始化仓库。服务默认 loopback，自动生成私有连接凭据；`--config` 仍必须明确提供，不默认猜模型、登录或业务。

不适合把它当成任意自然语言业务的零配置服务、多租户/公网 API、敌对代码沙箱、自动发布系统或无条件崩溃续跑服务。权限按 profile 判断：[ADR0094](adr/0094-trusted-single-user-role-team.md) 对 Node `trusted-single-user` 接受 ambient credential 风险，保留职责、授权与独立证据边界，不再以前置 OS 账号/凭据不可达证明阻断本机团队目标；原生登录仍不授权外部写，也不构成强隔离。旧 Go/hardened profile 保持原合同，旧实证不重新标记。Linux 同资产部署、正式 stable 发行和生产 SLO 不由此表授予，参见 [ADR 0088](adr/0088-node-task-service-production-projection.md)。

**接口检查点与产品完成分开**：已有独立评审记录为 `API-STABLE: PASSED`，适用于 `node-task-service/v1` 的当前合同/同包客户端；OpenAPI 仍标 `0.1.0-candidate`，不等于正式 v1/stable/production。当前 25 个操作、58 个 Schema；旧 Go、九操作实验协议及未来版本不在这个兼容声明内。

新增 Leader 决定、确认、阶段结果与整体结束语义时，须随实际启用范围更新 OpenAPI、客户端和真实行为验证；正式候选重新核验新的支持面，不撤回原检查点，也不把它外推为未来语义已稳定。

## 如何读表

- “已接线”表示原 HTTP handler 确实调用 Service/Application 的实现，不是只返回 Schema 示例。它仍受状态、认证、CAS、预算、配置和恢复门禁约束。
- “条件接线”表示必须安装并冻结指定可信能力；接口本身不能代替部署配置。历史查询和新写入的适用条件可能不同。
- “合同测试”只证明形状、路由和客户端绑定；“核心测试”使用真实 SQLite、受控执行事实；“服务测试”经过真实 HTTP/文件/所属 Node 协议进程或独立 checker，仍不等于真实模型。
- “实机”仅指下方列明的真实 Provider/源码/profile。表中测试链接是可定位的现有用例，不声称本表编写时又执行了一次全部测试。

统一生产调用链是 [HTTP handler](../packages/task-api/http-handler.mjs) → [Service dispatch](../packages/task-service/composition.mjs) → [TaskApplication](../packages/task-application/application.mjs) 及其原执行/问答/制品 reducer → 同一 SQLite。Service 自己提供四个运行观察接口。**25 操作的注入式 loopback 测试不是 25 项业务实现证明**：它为未实现操作也提供示例响应；必须再看下表实际分支。

## 25 个公开操作

`操作`列使用 OpenAPI 的 `x-application-operation`（同包客户端名称）；HTTP 路径及方法逐项对应原 `operationId`，没有新增接口。

| 操作 | HTTP | 合同 / 实际实现 | 已有验证范围 | 限制或未证明部分 |
| --- | --- | --- | --- | --- |
| `health.get` | `GET /health` | 已定义 / 已接线：Service 返回当前 profile 的存活响应 | [Service loopback/私有 token 测试](../packages/task-service/composition.test.mjs)、原 CLI/安装测试 | 只说明 HTTP 活着；不证明 owner、模型登录或任务可接纳。无需 Bearer，但仍受 Host/Origin 边界约束 |
| `ready.get` | `GET /ready` | 已定义 / 已接线：当前 owner、Supervisor、旧执行义务共同决定就绪 | [Service owner/恢复测试](../packages/task-service/composition.test.mjs)、[custody 恢复](../packages/task-service/custody-recovery.test.mjs) | 未决返回 `503 not_ready`；启动阶段缺必要证明时服务可能根本不能打开，不承诺失败时 HTTP 在线；不探测模型账号 |
| `task.create` | `POST /v1/tasks` | 已定义 / 已接线：Task、输入引用、初始预算、receipt 和规划/澄清义务同库提交，返回 201 | [HTTP+SQLite 并发重放](../packages/task-application/http-integration.test.mjs)、[COMMIT 窗口](../packages/task-service/recovery-commit.test.mjs)；实机 P、Q | 创建不是批准。正常输入先规划；仅命中可信有限模板且确有缺项时问答。重复 key 不产生新 Task/预算 |
| `task.list` | `GET /v1/tasks` | 已定义 / 已接线：读取原 Task projection，按 ID 分页 | [Application 查询](../packages/task-application/application.test.mjs)、[HTTP+SQLite](../packages/task-application/http-integration.test.mjs) | 不是跨用户目录、全文搜索或排序查询；不返回另一个服务根的任务 |
| `task.get` | `GET /v1/tasks/{taskId}` | 已定义 / 已接线：读取当前 Task、revision、allowedActions、成果引用 | [Service 团队/冷重开](../packages/task-service/business-integration.test.mjs)；实机 P、Q、C | 客户端按 `allowedActions` 和精确 revision 操作；单个 Worker completed 不等于 Task 已验收 |
| `task.plan` | `GET /v1/tasks/{taskId}/plan` | 已定义 / 已接线：返回原冻结计划及 digest/revision | [HTTP 确认](../packages/task-application/http-integration.test.mjs)、[有限澄清](../packages/task-application/clarification.test.mjs)；实机 P、Q | 尚无计划返回 `409 plan_conflict`；不能从 GET 生成/更新计划。完整澄清预览另在 questions 响应中 |
| `task.approve` | `POST /v1/tasks/{taskId}/plan/approve` | 已定义 / 已接线：精确 Task revision、plan revision/digest 和原配置重验后提交批准及 outbox，返回 202 Operation | [并发确认/重放](../packages/task-application/http-integration.test.mjs)、[原 CLI 团队](../packages/task-distribution/team.test.mjs)；实机 P、Q | 未回答完不能批准；旧零问题为 awaiting-approval，澄清最终版为 awaiting-confirmation。202/批准 Operation 成功都不是业务已完成 |
| `task.graph` | `GET /v1/tasks/{taskId}/graph` | 已定义 / 已接线：原计划 edges 与当前 node/Worker 关联投影 | [DAG/分页事实测试](../packages/task-application/application.test.mjs)、[HTTP DAG](../packages/task-application/http-integration.test.mjs) | 未有计划时 409；只读图，不提供任意编辑 DAG、重绑输入或新增 Worker 接口 |
| `task.workers` | `GET /v1/tasks/{taskId}/workers` | 已定义 / 已接线：原 Task 关联 Worker/Attempt 分页 | [Execution 查询及并行](../packages/task-application/execution.test.mjs)、[服务行为测试](../packages/task-service/api-stable-behavior.test.mjs)；实机 P、C | 包含 planner/verifier 等真实执行，不是只列作者；状态/进度不代表最终验收，用量仍 unavailable |
| `worker.get` | `GET /v1/workers/{workerId}` | 已定义 / 已接线：读取原执行身份和当前 Worker 投影 | [Execution](../packages/task-application/execution.test.mjs)、[25 操作客户端传输](../packages/task-client/index.test.mjs)；服务/实机中的 Worker 观察 | 不是操作系统进程管理 API；不暴露可用于接管的 PID 权威，不提供完整原生 transcript 或计费 |
| `worker.cancel` | `POST /v1/workers/{workerId}/cancel` | **已定义 / 未实现**：合法请求到 Application 返回 `501 unsupported_operation` | [真实 HTTP 501 且零新增命令反例](../packages/task-application/http-integration.test.mjs) | 不能把 OpenAPI 的 202 示例当可用能力。当前停止出口是整个 `task.cancel`，不能用它假装取消一个 Worker |
| `task.questions` | `GET /v1/tasks/{taskId}/questions` | 已定义 / 已接线：合并原批准前澄清及运行中 business 问题，统一分页，保留历史 | [澄清 HTTP/SQLite](../packages/task-application/clarification.test.mjs)、[原 Worker 问答/恢复](../packages/task-service/runtime-question-recovery.test.mjs)；实机 Q、P | 没有事实时返回空列表/空 preview 是真实“无问题”，不是问答能力探测。不是所有 Provider 工具权限询问的通用审批 UI |
| `task.answer` | `POST /v1/tasks/{taskId}/questions/{questionId}/answers` | 已定义 / **条件接线**：澄清答案→新 preview；运行中答案→原 Worker 投递/ACK 义务，各自原 receipt，均 202 | [旧 preview/路由/取消竞争](../packages/task-application/clarification.test.mjs)、[运行中 ACK/取消/crash](../packages/task-service/runtime-question-recovery.test.mjs)、[客户端 oneOf](../packages/task-client/runtime-questions.test.mjs)；实机 Q、P | 需各自可信 port；两个请求分支不能混用。202 只表示答案接纳，不表示 Agent 已消费；看 deliveryStatus。配置漂移拒绝新写，不抹历史 |
| `operation.get` | `GET /v1/operations/{operationId}` | 已定义 / 已接线：读取原 Operation 的当前持久观察 | [原 receipt 与取消收口](../packages/task-application/execution.test.mjs)、[服务取消](../packages/task-service/composition.test.mjs)；实机 P、C | 原写回执不会被改写成最新状态。approve/pause/resume 是控制操作；repair 的最终结果绑定后续验收；不统一等同 Task completed |
| `task.cancel` | `POST /v1/tasks/{taskId}/cancel` | 已定义 / 已接线：先提交 Task fence/Operation，再由 resident 停原句柄，按真实 cleanup 收口 | [cancel/cleanup](../packages/task-service/composition.test.mjs)、[COMMIT/crash](../packages/task-service/control-commit-recovery.test.mjs)、[长 Verify 下取消](../packages/task-service/api-stable-behavior.test.mjs)；实机 C | 202 不是已停止；不得退款、接纳迟到成果或重派。未知 cleanup 保留未决；已终态/不允许的状态可 409，无“强制清除占用”接口 |
| `task.pause` | `POST /v1/tasks/{taskId}/pause` | 已定义 / 已接线：持久 paused 与控制义务，阻止新 reservation/mayStart | [SQLite pause/resume](../packages/task-application/application.test.mjs)、[执行 fence](../packages/task-application/execution.test.mjs)、[待答暂停](../packages/task-application/runtime-questions.test.mjs) | **只暂停新准入，不暂停/杀死正在运行的 Agent/Verifier**；原期限继续。现有核心测试和通用 HTTP 合同覆盖，未单独记录真实模型 pause→resume 实机 |
| `task.resume` | `POST /v1/tasks/{taskId}/resume` | 已定义 / 已接线：从原 pausedFrom 恢复准入，由同 outbox/调度继续 | [原预算/期限/无重复执行](../packages/task-application/execution.test.mjs)、[原答案暂停后投递](../packages/task-application/runtime-questions.test.mjs) | 只适用于合法 paused，受 ready/CAS/原期限约束；不是 crash attach、重启旧 Worker、延长预算或解除 intervention 的 API |
| `input.create` | `POST /v1/inputs` | 已定义 / 已接线：Depot 原字节先耐久，SQLite 原输入 manifest/receipt 原子提交，返回 201 | [上传/下载/重开](../packages/task-service/composition.test.mjs)、[Artifacts 负例](../packages/task-application/artifacts.test.mjs)；实机 P、Q | 有界 Base64 JSON 上传，原始 bytes ≤256 KiB；输入 ready 仅表示可读，不能充当最终成果。`context.inputRefs` 只引用已存在的输入 ID |
| `artifact.get` | `GET /v1/artifacts/{artifactId}` | 已定义 / 已接线：查询原 owner/task 绑定 manifest；ready 对象同时校验 Depot bytes | [Artifacts](../packages/task-application/artifacts.test.mjs)、[缺 blob 冷备份反例](../packages/task-service/backup-restore.test.mjs)；实机 P、Q | 缺失/损坏对象不补造，ready 元数据读取也可能 503；input/evidence/delivery 三种含义不能混为“已交付” |
| `artifact.content` | `GET /v1/artifacts/{artifactId}/content` | 已定义 / 已接线：按 manifest 重验长度/摘要后输出 bytes 与 Content-Digest | [独立客户端下载](../packages/task-client/index.test.mjs)、[实际 checker/完整成果](../packages/task-service/business-integration.test.mjs)；实机 P、Q | 单次响应 ≤8 MiB，不是任意路径文件下载；客户端须验证 manifest+Content-Digest。没有公开制品删除/改写或流式大文件协议 |
| `task.audit` | `GET /v1/tasks/{taskId}/audit` | 已定义 / 已接线：原 Attempts/期限计量、acceptance、Worker 输入观察及可选 repair Decision 历史 | [真实 HTTP 输入审计](../packages/task-service/input-audit.test.mjs)、[负验收/修正](../packages/task-service/same-plan-repair.test.mjs)；实机 P 的验收/Attempts | **token/cost、独立 firstReview、waitingMs 仍无可信计量**；正文仅受显式披露策略控制，见下节。最终验收不能冒充首审通过 |
| `task.events` | `GET /v1/tasks/{taskId}/events` | 已定义 / 已接线：原 append-only 事件的有界公开摘要，按 sequence cursor 轮询 | [断连/正常重开后原 cursor 续读](../packages/task-service/api-stable-behavior.test.mjs)、[事件分页](../packages/task-application/application.test.mjs) | 不是 SSE/WebSocket，也不是完整 token/tool/transcript 流；正常重开续读证据不证明所有故障恢复路径 |
| `provider.list` | `GET /v1/agent-providers` | 已定义 / 已接线：Service 冻结的可信配置事实分页 | [Service 配置/查询](../packages/task-service/composition.test.mjs)、[25 操作合同/客户端](../packages/task-client/index.test.mjs) | 默认 availability=unknown、能力数组空；不是模型登录/余量探测。没有 Provider 注册、安装或凭据上传接口 |
| `supervisor.get` | `GET /v1/supervisor` | 已定义 / 已接线：当前控制器与 SQLite 任务/outbox/容量的只读汇总 | [并行/长 Verify 查询](../packages/task-service/api-stable-behavior.test.mjs)、[多 Task 隔离](../packages/task-service/soak.test.mjs) | 不提供控制器管理/自动修库；有界扫描超限返回 unavailable，不截断计数冒充完整。性能测试不是生产 SLO |
| `task.repair` | `POST /v1/tasks/{taskId}/repair` | 已定义 / **条件接线**：同计划、原 negative Decision/报告、明确节点与反馈、剩余预算重验后原子选择保留成果及修正义务，202 | [真实 HTTP 六场景](../packages/task-service/same-plan-repair.test.mjs)、[Core 原报告/预算/旧事实](../packages/task-application/repair.test.mjs) | 仅显式 repair profile 的有限文件业务；结构失败/unknown/用户取消/过期/已成功不能修。真实模型目前只有启用该配置的首轮成功，**没有自然内容拒收后二轮修正实机** |

统计：25 项均有合同；24 项有实际读取/执行分支，其中 answer、repair 的新写入需要专用可信 port，1 项未实现（worker.cancel）。questions 可读取原历史，但空列表不代表问答写入能力已配置；以每行边界为准，不用总数掩盖差异。

## 必须保留的语义区别

### 写入、暂停与恢复

除 health/ready 外需要原私有 Bearer；所有请求仍做 Host/Origin、路径、大小和 deadline 校验。写请求要求原 `Idempotency-Key`，确认/控制携带精确版本及相应摘要。HTTP 断连或 504 结束的是本次观察，不自动取消 Task；先查原对象/Operation，必要时用原 key 与原正文重放，不能换 key 或增加预算重试。

pause/resume 已进入 `Application.control → outbox → TaskExecution.settleControl/nextWork/mayStart`，不是占位端点。pause 不会让已启动工作或绝对期限停表；resume 也不会启动新一代替身。要停止原工作使用 Task cancel，并观察原 Operation、Task/Workers、真实清理结果。

同版本正常 open、明确 custody 绑定后服务崩溃的 cleanup-only 恢复，与“继续原模型会话”不同。缺原签名观察时 open 可拒绝；签名存在但 cleanup 未证明、以及 reservation-after/binding-before 等尚未闭合窗口，仍可能 intervention/not-ready。**没有公开“强制恢复/退款/清空锁”接口**。现有故障测试证明安全拒绝的分支不能写成恢复可用；详见[当前恢复缺口](node-task-service-status-2026-09-08.md)。

### 问答不是权限自动批准

批准前使用可信有限 `ClarificationPort`：真正缺少的声明输入才问，最多一批3题；答案≤4096 UTF-8 bytes、禁 NUL，原 confirmBefore/预算/图/权限/验收策略不因回答扩大。`previewDigest` 分支产生新完整 preview，最终只确认一次；完整输入仍走零问题路径。

运行中使用 `RuntimeQuestionPort`、原 Provider bridge 和被独立验证消费的 interaction 绑定，启用新 layout3（repair 组合为 layout4）。`questionDigest` 与 `previewDigest` 是互斥请求分支；问题绑定原 Task/Worker/node/Attempt、原期限与有限策略。接纳前 deliveryStatus=null；接纳后查询其真实投递/ACK 状态。丢 ACK 或 crash 不允许客户端重派 Worker/偷偷重答。不是任意 Agent 品牌已有通用问答能力，也不把工具权限询问自动变成业务答案。

### 审计：已有的输入观察与仍缺失的费用

[input-audit](../packages/task-application/input-audit.mjs) 已替代早期固定 `prompts:[]` 的占位说明：每个有原 Worker 的记录可返回 inputDigest/reservationDigest、prepared prompt 摘要/长度，以及 prepared/handed-off 时间和 coverage。默认只存元数据，正文为空、无 snapshot；旧记录仍 unavailable，不后台补造历史。

只有可信 `auditDisclosure` 同步策略返回的文本才存为证据制品，HTTP preview 最多2048 UTF-8 bytes，完整披露结果通过原 Artifact 下载并重验。策略拒绝/抛错/异步返回不代表任务失败，也不保留未经披露的正文。handed-off 只证明有效 Provider handle 已返回，不证明模型实际消费；Agent 自带隐藏 prompt、Skill、provider context 不在“完整 prompt”承诺内。

Task 与 Worker 的 `usage` 仍固定为 `{tokens:null,cost:null,currency:null,source:'unavailable',coverage:0}`。`firstReview` 为零且 measurement 的 firstReviewSource=unavailable；最后一次独立 verification 只记 acceptance，不借作首审。Worker elapsed 是 started 到 settlement（含收口）的差值，waitingMs=null，不推算模型耗时/价格。此表不把字段存在写成费用审计已完成。

## 实机证据索引与未确定项

以下只引用已记录实证的精确源和边界，不复制长历史，也不声称本表基线重新跑过这些模型：

| 标记 | 精确实证 | 能证明 / 不能证明 |
| --- | --- | --- |
| P | `69cfacee2fbea366174744167d5a2c3a3dfeee89`，Pi 0.84.4，29.624秒；[12:59 实证段](node-task-service-status-2026-09-08.md) | 原 HTTP 规划/批准、双作者、同 Worker 业务问答/ACK、独立验收、下载/审计、正常实例重开。不是任意规划、自然 repair 或模型 crash |
| Q | `538c53494fdaf0715bd5817914c44ace46771471`，Qwen 0.22.3，21.440秒；[区域业务实证](node-task-service-status-2026-09-08.md)、[可运行窄业务说明](../packages/task-regional-window/README.md) | 两项必要日期问答→最终预览/批准→两作者/独立下载。是历史窄 profile 实机；不是把旧源写成当前候选完整验收 |
| C | `37df1e7a57d7b356ec385113f8c76e74c1f2f5a8`，Pi/custody 13.046秒；另 `69cfacee…` Qwen/layout1 14.143秒；[当前取消证据](node-task-service-status-2026-09-08.md) | 原活跃作者的早期取消、原 cleanup、Operation、零 verifier/delivery、正常重开原回执。未证明已经进入模型 token/tool 阶段，两个 profile 的恢复资格不可拼接 |
| R | `d6502386f1be840096dcf002e9bb2eb551801765`，修正启用配置下 Pi 首轮26.288秒成功；[12:04 实证段](node-task-service-status-2026-09-08.md)、[显式修正驱动](../packages/task-repair-live/README.md) | 只证明 natural-first-pass；没有执行 repair API 的模型后继，不能为补证人为制造原错误或付费循环寻找失败 |

当前精确基线 `2d178b4…` 的 [Node team CI 34316647809](https://github.com/chiga0/marshal-harness/actions/runs/34316647809) 已由维护者核对 Ubuntu/macOS 均 success；同源 API/client 独立重跑32/32、1.361秒。原[CI 测试入口](../.github/workflows/node-team.yml)包含无模型协议进程，不是两平台同资产部署或模型验收。前源 `ce55eed` 的 macOS 519/521、两个清理失败仍保留为失败记录；本基线已包含随后审查/验证的 Darwin 修正，不把旧失败说成当前仍红，也不删除原原因尚未被 CI errno 直接确认的边界。

在 `2d178b4…` 快照中尚未实现或未找到实机证据的事项：单 Worker 取消；真实 token/费用与独立首审/等待计量；自然内容拒收后的模型局部修正；pause/resume 的单独真实模型观察；尚未闭合的执行许可前恢复窗口；声明平台的同资产部署与 stable 发行。单 Worker 取消的后继进展见页首；Worker/Publisher 权限按上述 profile 区分，不将强 OS 隔离重新加入可信单用户的前置条件。未核验的 Provider 登录/能力不能说成“已缺失”，列表 unknown 也不能说成“已就绪”。本表记录限制，不新增前置门禁或撤回已记录的 API-STABLE 检查点。
