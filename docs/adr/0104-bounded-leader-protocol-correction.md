# ADR 0104：受管 Leader 至多一次协议格式纠错

- 状态：Accepted（维护者与独立设计审查已接受；实现验收与真实模型验收分别记录）
- 日期：2026-09-14
- 设计基线：`5809eabcccd992e7677b278687447d99e8c7c133`
- 关联：ADR0094、ADR0095、ADR0101、ADR0102、ADR0103

## 问题与实际证据

真实 `M01-5809eabc-1` 的 intake Worker `worker-f2229a3a-0dc2-4a17-bcc3-273629b95b5c` 输出了 3063 UTF-8 字节公开文本，没有工具调用；最终 JSON 的括号错误导致解析失败。公开文本 SHA-256 为 `7322c4eaaed5cc9ddbf29867e8f74b398afe6d3693d6c3e322df563ac2930ffc`，末尾为 `"assumptions":[]}]}]}`，独立严格 JSON 解析在字符 1823 拒绝。没有发生作者执行、候选采集或业务动作提交。当前 Port 将解析失败映射为 `invalid_leader_decision`，Core 随后终止 Task。普通业务内容已经可以生成，但一次格式错误使用户重新提交整个任务。

第二次 `M01-5809eabc-2` 则不是相同错误：intake、作者采集和独立 Review 均完成，后续 Leader 调用了 write_file 将提案写到 `.qwen/tmp/leader-proposal.json`，收到真实拒绝后未产生公开最终文本。这属于职责及输出通道违例，不在本 ADR 纠错范围；不能开放 Leader 写权限以让测试通过。

该证据只能证明本次格式错误，不能证明所有 `invalid_leader_decision` 都可以重试。当前 Core 的动作资格、授权、当前性等拒绝也可能使用相同粗码；`onDiagnostic` 是非权威观察，不得成为调度授权来源。

## 决定范围

仅显式新配置通过 `registerLeaderJsonCorrection(原LeaderPort,{profile:"leader-json-correction/v1",maxPerTask:1})` 登记原 Port，配置对象闭合且 `maxPerTask` 固定为 1。缺省关闭。第一版仅纠正 Leader 严格 JSON 语法错误；不覆盖 Review、作者、发布、后验或 Core 业务动作拒绝，也不覆盖 JSON 已合法但形状、字段值、摘要或动作不合约的结果。后续若扩展 wire-shape，必须另有证据、明确分类和审查，不能通过增加错误码悄悄扩权。

一次纠错指向模型重新请求一份完整提案，不是自动修正括号、删除字段、补预算、重写动作或接受近似 JSON。模型仍可能第二次出错；届时按原失败出口结束并保留全部证据。

配置参与新 profile 的冻结身份与源码摘要。旧服务根不开启、不迁移计数，不通过同根改配置恢复已失败 Task。该扩展保留现有 v7 单 Application/SQLite 和唯一 Execution，不创建第二调度器、通用重试平台或隐藏 Provider 内循环。需要新 profile 时在原组合内命名，具体新默认切换由集成审查决定。

## 可信分类与权威边界

在新增受信适配器内，以原不透明 receipt 为私有 WeakMap 键保存附属分类；不修改原 receipt 数据。绑定仍为原 port、ticket 摘要、Provider 及实际 cleanup。以下阶段用于说明资格边界，持久化首版只记录实际确证的 `wire-json/invalid_json`，不得以外部对象或 Provider 自报 code 铸造：

| 阶段 | 定义 | 首版能否纠错 |
| --- | --- | --- |
| `wire-json` | 原公开输出经过既有完整 UTF-8、大小和严格 JSON 解析后，解析器确认 JSON 语法不合法 | 是，仅明确语法错误 |
| `wire-shape` | JSON 合法，但顶层或动作结构/字段不满足闭合 wire 合同 | 否 |
| `binding` | callId、inputDigest、receipt/ticket/port/Provider 等绑定不符 | 否 |
| `action` | Core 授权、状态、选果、预算、当前性或业务动作校验拒绝 | 否 |
| `provider-result` / `cleanup` | Provider 非完成结果、协议中断、取消或清理不确定 | 否 |

不能仅凭 `invalid_json` 字符串或公开 diagnostic.code 决定。新分类器先对完整原文本进行保守词法扫描，再确认原生 JSON 语法异常；混合语法错误中发现重复键（包括转义键）、深度超限、非有限数字、非法字符串或额外后缀时也拒绝资格。超限、非法编码、重复键、后缀数据、错误 wire profile 等不能因与语法错误共用粗码自动加入可纠错集合。Port 的成功值仍必须通过全部原校验；Core 的动作验证拒绝只能归入 action，不反向伪装 wire-json。

## 原失败与纠错调度的原子性

在原 `leader.finish` 的同一 owner 事务中，先完成原 Worker：保留失败状态、原 receipt 拒绝原因、清理事实、完整 ticket 绑定及 `leader.decision.rejected` 事件，不把原 Worker 改成成功。额外保存闭合的失败 Outcome 引用：原 workerId/callId/ticketDigest、可信拒绝阶段和固定码、原公开输出摘要及字节数、cleanup 摘要、时间。正文留存继续服从 ADR0103；不采集隐藏推理，不把原错误 message、宿主路径或模型自述作为代码。

只有全部成立，才在该事务内消耗唯一纠错预算并生成一个 durable successor obligation：

1. 新 profile 明确启用，Task 的 `leader.protocolCorrection.used` 为 0；原调用不是纠错 successor。
2. receipt 原 Provider 已 `completed/end_turn`，输出非空、完整、限额内，且分类确实为允许的 wire-json 语法错误；该次调用没有已观察工具调用或权限拒绝。空输出、输出到文件/工具而不是最终通道均不得加入格式纠错。工具/权限事件须由新入口的受信调用记录判定，不以可关闭的 UI diagnostic 作调度依据。
3. 原执行的 cleanup 已确认，绑定的 executionId/startedAt 与 Worker 相符；没有 extraScope 或未证明的运行范围。
4. 原 Worker、Task、原 obligation 和 readSet 都是当前的；无 stopIntent、取消、终态、期限到期或外部干预。
5. 原调用没有提交任何业务动作；原尝试/Leader call 已正常计费记数。
6. 按原预算预留规则检查仍能容纳一个新的 Attempt/Leader call及后续必要交付步骤；纠错不增加 Task 原上限，不重置 deadline。

Task 的 `leader.protocolCorrection` 保存原失败 Outcome 和 successorObligationId；原 obligation source 标记固定原因 `protocol-format-correction`，不新增独立纠错状态机。唯一 Task 计数单调递增、不能退款；每个 Task 最多一次，不是每个阶段一次。原 obligation 关闭，新 obligation 沿现有 claim/command/worker reservation 原子链调度。Task 可继续原 intake/执行阶段；UI 依据 used、原失败 Worker 与实际后继 Worker 状态呈现，未 claim 或缺少 Worker 时不能声称模型正在纠错。没有预算或任一守卫不满足时维持原失败/干预出口。

## 新调用与提示词

纠错调用使用新 Worker/Call/Attempt/隔离目录及原受管 Provider；不得复用已停止 handle，不在同一个 Provider start 内私自追加模型请求。完整原需求和合法当前 snapshot 仍来自 durable Core 输入。`prepareLeaderWithJsonCorrection` 在原 prepare 输出前加固定说明，之后由原 Controller 捕获实际输入 Audit；start 适配器绝不改写 prompt。提示只加固定说明：上一调用未形成可解析的严格 JSON，请按当前合同重新返回完整提案；业务目标、权限和验收不变。绑定新的 callId/inputDigest，不重用旧回执。

无需把损坏 JSON 全文重新拼入提示；原摘要和固定语法分类足够关联失败证据。不得从原损坏文本提取“看似有效”的动作执行。纠错输出通过全部原 Port/Core 校验后，计划仍需用户原流程批准；不能把“格式纠错”当作批准、发布或业务验收。

纠错派发前再次检查当前性和取消/期限。若其他事件已改变语义上下文，按原 obligation 当前性机制处理，不把过时输入继续重放；预算已经消耗也不回退。纠错调用再次语法失败或出现任何其他拒绝时，不产生第二个格式 successor。

## 恢复与幂等

预算消费、原失败 Outcome、successor obligation 身份和原调用关闭必须同事务提交。提交前崩溃只能按原未提交路径恢复；提交后崩溃从 durable obligation 恢复。重复 finish、重复 scan、重开、旧 generation 的迟到结果不能重复生成 successor 或增加第二个 Attempt。已 claimed 的原纠错 Worker 依原 cleanup/recovery 机制处理，不重新调用模型冒充恢复。

严格保留单写者、Worker 不自证、无自动业务 merge/publish、受控清理及不确定态不释放容量规则。不能因为本功能在新 profile 开启就让历史 never-permitted 证明接受新的异常形状。

## 设计评审后的实施边界

本次按完整 Core/Port/恢复包实施，并由独立配置/UI/验收包集成。新实现位于 `task-application/leader-protocol-correction.mjs`；旧 `leader-ports.mjs` 保持原字节，其原私有资格和已有 profile 源码身份不变。`leader.mjs` 在原事务内增加有界计数与关联，Service 只对登记的原 Port 启用 prepare/start 适配。新冻结身份覆盖登记策略、新分类器/adapter、Leader reducer 和 Service 接线的源码摘要，发行库存显式包含新模块。
旧 Port 的 `ports` / `receipts` 是模块私有 WeakMap；复制 create 到新文件或包装替换 port 对象均无法通过原 configuration/receipt 校验，不是可行兼容方案。采用以下接缝：新增受信 correction 模块登记原 createLeaderPort 返回的同一个 port 对象，登记值包括闭合策略与新模块源码身份；原 leaderConfiguration 继续识别原对象。仅新配置的 managed.start 走 adapter，adapter 调用原 port.start，并以同一 Provider 的 facade 捕获该次原 completion 和受信工具/权限观察。facade 保留原 started/completion/stop 事实与调用次数，不修改 raw 结果；adapter 在原 completion 返回后，以原不透明 result.receipt 为 WeakMap key，保存原 port、ticketDigest 与本次可信分类。Core 必须先通过原 receipt(port,ticket,result) 校验，再向新模块取得额外分类；没有登记、分类缺失或绑定不匹配均不重试。普通对象、公开 diagnostic 或 Provider 自报 code 均不能构造此关联。旧配置完全不经过 adapter。

该方案的分类边界为：独立 parser 只能判定本次完整原输出的语法类别，不能复制解析后的动作绕过原 Port。工具与权限记录只用于排除纠错资格，不产生授权。新 profile 的冻结身份需覆盖登记策略、分类器/adapter 源码和新配置；已有 opaque receipt、WeakMap 与 same-root 测试必须作为前置。未来若必须修改旧 Port，需要另行版本化合同；不能删除兼容测试或改旧 profile digest 来规避同根身份检查。

本 ADR 的设计已接受；合并代码不等于实机成功或产品可用性验收。

HTTP `LeaderView.protocolCorrection` 为可选闭合字段：profile、used（0/1）、max（固定1）、reason（null/wire-json）、original（null 或包含 stage/code/outputDigest/outputBytes/workerId/callId/ticketDigest/cleanupDigest/at 的原失败引用）、successorWorkerId、successorCallId。未登记时整个字段缺省，旧客户端原形状保持；新客户端同时支持缺省和显式字段，并校验计数、原因与关联成对一致性。该引用只关联第一次格式后继；若它随后发生崩溃，原恢复机制产生的新 Worker 不改写该关系，也不能再消费格式预算。

受控验证已包含真实 SQLite 与五处 SIGKILL：finish 前、事务内、提交后未 claim、claim 后、start 后。前两处不持久化分类或预算；未确认清理时不复铸 sidecar，不重新启动模型。另用真实 custodian 签名清理事实验证已 claim/start 的原恢复出口，格式 used 保持 1，后续格式错误不再调度。HTTP/浏览器 fixture、真实模型及目标用户可用性由集成候选另验，不能以这些受控证据替代。

## 必须独立验证的退出用例

### 2026-09-15：漏转义引号的有界词法覆盖与诚实诊断

真实 `S01-5bb31cbd-2` 最后一次 Leader 的公开输出摘要为 `sha256:9d33a259b024bfebecd828de7048e0e92ce0d67680b3892935dd01bce2753688`。它在 summary 内直接写入未转义的标题双引号，原严格解析在字符 137 拒绝；并非空输出或 Core 行动拒绝。旧保守分类器拒绝字符串外的中文词，所以 used 保持 0，Task 仍失败。

本次在原显式配置内扩展明确词法形态：两个完整、各自合法的 JSON 字符串 token 之间，允许最多 128 个 Unicode 字母、组合标记与普通空格作为漏转义引号的语法候选，不按语言特判；前一 token 必须为值，后一 token 不得是键。`NaN`、`Infinity`、数字、其他标点、超限或不明确形态仍拒绝。全文继续扫描重复键（含转义键）、深度、非有限数字、非法编码/转义与尾随数据；仍以原解析拒绝和原生语法异常为必要条件。不拼接字符串、不补引号、不读取或执行看似正确的动作；这不宣称任意 JSON 错误均可纠错。持久格式、一次预算与全部恢复/当前性门禁不变，分类源码更新进入原冻结身份。

观察层另接原 Port 已有的固定 `stage=parse` 诊断：Controller 只接收本次原受管回调、匹配 Task/Worker/Provider/执行类型且 `status=completed` 的允许码，在原 failed receipt 结算时显示 `ExecutionDiagnostic.stage=protocol`，code 为 `invalid_json`、`invalid_leader_result`、`invalid_leader_decision` 或 `invalid_review_report`，source 仍为 `controller`。它不赋予纠错资格，也不改变失败 Outcome；普通 Provider progress 不能伪造 controller 来源，停止/终态/迟到回调被忽略。旧关闭观察配置保持字段缺省。完整公开输出片段仍只记录一次，最终诊断不会把旧正文重抄为新输出；详情历史按既有边界读取，投影裁剪不表示模型没有输出。

- JSON 括号错误 → 原失败 Outcome 留存 → 恰一个新 Worker → 合法提案 → 原计划批准流程；原 Attempt 和 call各增加一次，无隐式批准。
- 第二次 JSON 错误 → 终态失败；无第三次模型请求。关闭/旧配置第一次错误仍原样失败。
- 合法 JSON 的错误 shape、错误摘要、未授权 action、业务验收失败、发布拒绝均不触发格式纠错。
- 缺少 cleanup、cleanup unknown、额外执行范围、Provider取消/截断、期限到期、剩余预算不足均不启动纠错。
- 在原失败事务前后、successor claim前后、新 Worker start前后断电/冷重开；总 successor为1，不重复模型调用，不伪造清理。
- 用户取消、Worker stop、旧 generation 结果、readSet变化与失败回调竞态保持原优先级；取消后无格式 successor派发。
- 确定性 fixture通过后，使用相同损坏公开文本夹具验证新合同，再用一次真实模型定向用例验证结果；不得故意依靠无限重跑获得绿色。
