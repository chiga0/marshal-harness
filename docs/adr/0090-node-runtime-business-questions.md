# ADR 0090：Node 运行中业务问答与同执行答案消费

- 状态：Proposed（2026-09-08；待维护者独立审查接纳，不代表实现或实机通过）。
- 解决问题：B2 的运行中业务问答必须继续原 Worker，而非重新规划、另派 Attempt 或借权限请求放行工具。
- 范围：ADR 0088 的单节点 Node 服务，复用 ADR 0089 的执行托管与 cleanup-only 恢复。首个真实 Adapter 为 Pi 原生 RPC；其他 Adapter 没有通过同链验证时明确不支持该增强能力。

## 1. 为什么需要本决策

[ADR 0086](0086-task-preapproval-questions-and-preview-revisions.md) 只处理未批准 Task：答案原子追加 preview，再显式确认；它明确排除运行中 Worker 和跨进程投递。本决策不改变其旧请求、回执、preview 或批准含义。[ADR 0085 §5](0085-agent-team-service-contract-and-storage.md#5-节点交互生命周期与取消) 与 [ADR 0088](0088-node-task-service-production-projection.md) 已授权有界节点问答，但尚未定义当前 Node 执行的持久消费接缝。

本机 Pi 原生 `ui.input/select` 经 `extension_ui_request` 和 `extension_ui_response.value` 工作；该响应没有原生 ACK，对话自身超时只删除等待项。现有 Marshal Pi RPC 只处理权限 bridge 的 confirm，且人类等待默认 10 秒、上限 30 秒。因此“已有 UI 请求/响应”不等于可靠业务问答；不能把写入 stdin 成功当作原 Agent 已消费，也不能复用 ACP `session/request_permission` 的 `answers` 字段向 HTTP 暴露工具授权。

最短链路是：原 Worker 提问 → Core 同库登记 → HTTP 明确答复 → 原 owner 消费一次投递义务 → 原 Pi 对话返回并报告匹配 ACK → 原 Worker 继续 → 独立验收消费答案引用。只增加这一条完整链，不建设交互平台、新调度器或无限 steering。

## 2. 业务范围与原执行不变

受信业务组合通过中立接口注入 `runtimeQuestions` 策略，包含稳定 `policyId/policyDigest`、有界问题/答案验证器和支持节点；模型只提议问题，不写事实。首版支持 `input` 与有限 `select`，不接任意 confirm/editor、权限确认或人工最终验收。实际不缺业务信息就不提问，不为了演示增加“是否继续”。

策略在 Task 计划形成时冻结并进入该新计划的摘要：公开 `Plan.interaction` 为可选闭集 `{profile, policyDigest, maxQuestions, maxWaitMs}`，其中 profile=`task-runtime-question/v1`；原无交互计划仍保留原字节/摘要。受信完整策略及节点范围由同一 Task 记录保存，不能仅凭 HTTP/模型回显摘要证明策略存在。默认不启用；缺少 Provider、业务验证器或最终验收消费能力时在批准/派发前明确拒绝需要此能力的计划，不临时放宽。

- 每 Worker 同时最多一个问题，整 Task 累计最多三个运行中问题；重复协议消息不是新问题，已消耗次数不退款。策略可以更低，不能运行中增加。
- 问题文本最多 2048 UTF-8 字节，答案最多 4096 字节且非空；禁止 NUL/非法 Unicode。选择最多 16 项、值唯一，答案必须精确属于原选项。表述/答案仅作业务数据，不接受 executable、Policy、凭据、任意文件读取或发布指令。
- `deadlineAt = min(原 ticket.deadline, 登记时刻 + maxWaitMs)`；`maxWaitMs` 最多 120000 毫秒。原 Task/Worker 绝对期限、Attempt、预留预算和目录不变，等待计入实际耗时与容量，不能靠进度或答案续期。
- 问答不更换 session、prompt、原执行 ID 或 Attempt，不增加模型 fallback。问题不改原批准节点、依赖、Provider、路径/工具权限、oracle 或发布范围。超出已批准业务/验收边界的答案拒绝，不能沿旧批准偷偷改变目标。
- 节点等待不阻断无关分支；Worker 投影为 `awaiting-answer`。Task 的控制状态及 `allowedActions` 按实际未决问题投影，不能用一个节点等待隐式全局 pause。显式 pause 仍只阻止新派发及尚未授权的答案投递，不冒充暂停所有已运行工具。

## 3. 同一 SQLite 的最小追加事实

新事实族为 `task-runtime-question/v1`，复用现有 event/projection/receipt/outbox，不新增数据库或磁盘问题队列。新启用根采用 `marshal-node-task-sqlite/v3-interaction` 及相应服务格式，保留 v2-custody 的原绑定、preclaim 封闭观察与清理语义。旧 v1/v2 reader 必须在打开 SQLite/claim 前拒绝新格式；旧根不自动迁移、补写或重签。新无交互 Task 也不能让旧 reader 忽略同根已有新事实。受控升级另行验证，不以历史导入阻挡新根实施。

问题登记必须在当前 owner 短事务内核对已批准策略、原活跃 Worker/ticket/实际 started、session/native request 关联、未停止/未终态和期限，并同时追加不可变问题与 Worker 等待投影。绑定至少包括：`storeId/generation/taskId/workerId/nodeId/attempt`、`executionId/sessionId/nativeRequestId`、原 `reservationDigest/planDigest/inputDigest`、`policyDigest`、规范化问题/选项、创建时间与 `deadlineAt`。`questionDigest` 是上述冻结事实的摘要，不从问题文字或当前磁盘状态反建可信来源。

每题 revision 固定为 1，不改答、不重新开放；问题 ID 由 Core 生成，同原执行/native request 的精确重放零追加，异内容拒绝。同 session 的 request ID 重用、其他 Task/generation 的提问拒绝。问题建立、答案和控制动作按原 Task revision CAS 排序；普通 progress 不增加控制版本。

有效答复同事务保存不可变答案、答案摘要、`task.answer` Operation、原 HTTP 幂等回执及一次答案投递义务。先认证/对象授权、查精确原回执，再核新请求 CAS、题目摘要/版本、未消费/未停止/未过期及当前原执行；同 key 异内容、改答、陈旧正数 revision 返回 409，非法形状返回 400，到期返回 410。精确旧请求重放只返回原接纳事实和明确的当前投影，不再次唤醒/发送。

登记答案不是 Agent 消费：题目 `status=answered` 表示用户答案已接纳；`deliveryStatus` 单独为 `pending|dispatched|acknowledged|cancelled|expired|unknown`。Operation 初始 accepted；只有匹配 ACK 被当前 owner 接纳才 succeeded，失败/取消/unknown 按事实结清。原 HTTP 回执不随 Operation 后续改变而覆盖。

## 4. 一次投递、ACK 与失效顺序

Supervisor 的易失等待表只关联原 Worker handle，不构成业务真值。HTTP 不持锁等人，问题回调也不阻塞原 RPC 接收循环；cancel/deadline、其他任务和 API 继续前进。

1. 当前 owner 从原投递义务取得答案；短事务重验同 ticket/generation、未 cancel/terminal、非 paused、原问题期限及仍有效的原对话，追加单次 `dispatched` 消费和随机 `deliveryNonce`，绑定 `questionDigest/answerDigest`。该提交是答案投递授权的线性化点；之后才向原私有协议句柄发送，不能从磁盘 PID 或重开的 session 发送。
2. cancel/pause 先于该提交则零投递；投递先赢后 cancel/pause 不能撤回已经在途的字节，只阻止后续投递并执行原停止规则。不用“同一段 JavaScript 没有 await”宣称跨进程原子。paused 时新答复返回 409；先接纳但尚未 dispatched 的答案保持 pending，明确 resume 后仅在原期限内投递。
3. Pi 受信 native bridge 的纯业务询问工具调用原 `ui.input/select`，每题产生独立 questionNonce，原请求中绑定 session、工具调用及该 nonce。原对话返回后、该工具把答案交还模型前，发送仅用于消费确认的受信 bridge ACK 请求，携带相同 questionNonce 和实际返回答案的摘要。父 Adapter 通过原对话等待句柄将其精确归一到已登记 nativeRequestId、questionDigest、answerDigest 和 deliveryNonce，不能从 ACK 自带的对象 ID 选择目标。模型文本、普通 notify、工具自报 pass 均不替代它。该 ACK 只确认业务数据投递，永不充当 Decision、工具许可或 cleanup 证据。
4. 最小 Pi 接线复用原 SDK 的第二个 `ui.confirm` 作为内部 ACK 往返：它使用区别于 permission 的固定类型，父进程只在原 ACK 事务提交后返回 confirmed，绝不询问用户或选择工具授权。初始 `input/select` 的 value 始终是原答案，不编码命令/控制 envelope；bridge 的 ACK 未获确认时不把答案交还模型。此内部 ACK 等待最多 5000 毫秒且受原问题/Worker 期限约束；前后两个不同 native request ID 都绑定同一 questionNonce，不允许串题。缺少该能力不宣称支持运行中问答。
5. ACK 精确重复零追加；外来、陈旧或异内容 ACK 拒绝。ACK 在 cancel/到期后到达只能保留诊断，不重新开放 Worker、撤销 stop 或接纳其结果。数据库写 ACK 失败也不能用内存标记继续接纳成果。

未答到期先持久失败 fence，再取消原对话并停止原 handle；不得把 SDK 的 undefined/false 默认值当用户答案。答案已接纳但未授权投递时取消/到期，可确定标记 cancelled/expired；已 dispatched 而 ACK 丢失、协议断开或对话已结束则标记 unknown，原 Worker 失败并停止，不猜测是否已消费、不重发、不补派。实际 cleanup 被证明后允许按原规则结清失败/取消及容量；答案投递 unknown 不伪造进程仍活，也不使有清理证明的 Task 永久占用。

服务进程崩溃后，0089 仅收口旧执行清理：所有原问题保持历史，未决投递关闭，旧 ACK/答案/结果不得跨 generation 消费。新 owner 不恢复旧等待表、Agent 对话或 prompt，不用持久答案启动替身。原 ACK 已入库也不授予崩溃后的旧业务结果接纳；旧执行清理不足仍保持原未决门禁。

## 5. 原票据、下游和最终验收的绑定

运行时输入增加采用追加引用，不修改原 ticket/inputDigest/批准计划。Core 为当前 Worker 形成按题目序号稳定排序的 `interactionRefs`，每项包含问题、答案、投递/ACK 的不可变事实摘要及原 Worker/execution 绑定；仅 acknowledged 的项可供结果消费，未答、unknown 或取消项阻止正常成功。

Worker finish 在原 current-owner 事务复核其全部问题及引用，与真实候选 manifest/原结果摘要共同绑定。下游只消费已接纳上游结果所绑定的引用，不读旧 Worker 目录或自行拼接聊天记录。独立 Verification ticket 的输入含同 Task 相关 `interactionRefs` 和精确答案材料，因而其原 inputDigest/验证 receipt/最终 Decision 自然覆盖这些新增业务输入；不覆盖旧票据，也不凭 Worker 自报 refs 构造当前事实。

受信验收实现必须明确支持这组业务答案并仍检查原必需断言；不支持、引用遗漏/错绑、摘要漂移、问题超范围或验收被答案替换时拒绝。用户答案不是独立验收通过，原 oracle/权限/批准 planDigest 不因回答变化。审计保留原问题、实际答案、等待/投递时间与消费结果；敏感文本仅走已保护 Task 查询，不进入普通日志、环境或公开进度。

## 6. 最小 HTTP 差量

继续原 `task.questions` 和 `task.answer`，不新增问题创建 API、状态 PATCH、任意工具授权或 Worker 消息入口：

- GET questions 保留原分页/认证；运行中条目 `kind=business`，增加 `workerId`、`questionDigest`、`deliveryStatus`，已有 node/subject/revision/prompt/options/deadline/status 保留。此分支 `subject` 使用 questionDigest；不暴露原 nonce、协议句柄或秘密。外层原 preview 字段仍可 null，`confirmBefore` 沿原 Task 期限，题目实际期限以各 `deadlineAt` 为准。
- POST answers 的 `AnswerQuestion` 为闭集 oneOf：原 `{expectedRevision, previewDigest, questionRevision, answer}` 不变；新 `{expectedRevision, questionDigest, questionRevision, answer}`，禁止混用。choice 也提交原选项字符串，不接受 permission option/任意对象。
- 新 RuntimeAnswerReceipt 为 `{taskId, questionId, operation, acceptedRevision, questionDigest, deliveryStatus, task, currentTask, replayed}`；明确与原含 preview 的 AnswerReceipt 区分。首次成功受理返回 202，原批准前语义/状态码不变；查询 Operation/题目观察实际投递进展。HTTP 客户端不自动 approve/resume、刷新 CAS/key 或重试 unknown。
- Plan 的可选 interaction 字段按 §2 同步 schema/handler/客户端；能力只在真实 Provider、业务、Supervisor、Store/验收链都支持时报告，不因 Schema 有字段冒充运行可用。

## 7. 一个实施包与完整验收

唯一主作者覆盖 Application 新问题事实/统一旧消费者、Execution/Verification 引用、Supervisor 原等待句柄、Pi RPC/native bridge、服务与 Store 格式/发行清单；API/schema/客户端及独立组合负例可在冻结接口后并行。不另开问答控制服务、不拆每字段 ADR、不恢复旧 Marshal skill。

必须同时迁移创建/plan/批准、query/list/allowedActions、原答案 replay、pause/resume/cancel、Worker finish、最终 Verification、0089 preclaim/cleanup 格式识别和发行入口。旧 preapproval 问答保持原测试；原无交互业务不增加问题或人工等待。

验收用正式 HTTP/Application/SQLite/Pi Provider/独立 checker：原 Worker 提问、用户明确回答、同 execution/Attempt 继续，另一无关作者实际前进，最终下载成果按答案及原 oracle 独立消费。先原 Pi SDK 与真实受管进程的无模型正反例，再一次真实模型业务；fixture 不冒充真实团队。

一次聚合反例包括：跨 Task/Worker/session/request/generation、重复/改答/旧 CAS、问题/答案限额、SQL 登记/投递/ACK 失败、未答/已提交未 ACK 到期、两种 cancel/answer/dispatch 顺序、pause 保留原期限、HTTP 丢响应、ACK 丢失/重复/伪造、服务在等答/提交后/ACK 后崩溃、额外工具权限伪装、验收遗漏答案、旧 reader 拒绝新格式。清理以原句柄/custody 事实证明，不能手写 terminal 或放宽 unknown。

接纳本 ADR 只允许实施此纵切，不关闭 B2/API-STABLE/B3；实际 source、失败与实机证据另由当前状态文档记录。
