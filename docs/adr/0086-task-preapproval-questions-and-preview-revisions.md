# ADR 0086：未批准 Task 的关键问答与追加式预览版本

- 状态：Accepted（2026-09-08，由维护者在已授权 B2 实施目标内接纳；独立审查 `4f76a2e` 无 P0/P1，并补齐消费者迁移及取消 CAS 澄清。不是用户逐条确认记录，不表示实现或实机已通过）
- 日期：2026-09-08
- 范围：B2-A 的提交前关键澄清；只处理尚未批准、未创建 Run 的公开 Task。
- 依据：[ADR 0085 §3、§5](0085-agent-team-service-contract-and-storage.md)、[服务 Milestone](../agent-team-service-milestones.md)。

## 1. 缺口与最小调整

0085 已接受问题必须绑定 Task、subject、revision、期限和消费记录，但当前 `bounded-task-draft/v1` 只保存一个不可变 draft，确认绑定该 draft 的精确摘要和 revision=1。它没有定义回答后如何原子保存新 preview、阻止旧 preview 获批，也没有定义谁可以生产问题。

本决策只补上述持久化接缝，不修改旧 draft 的字节、摘要、版本或既有批准含义。新 Task 仍是同一个 Goal，不增加独立 Task 真值、数据库或通用交互平台。已批准 Task、运行中的 Worker steering、人工成果验收和跨进程答案投递不在本次范围。

当前 `order-quote/v1` 的业务接口与 oracle 已完整冻结，没有需要用户补充的必填槽，因此必须返回零问题，保持原 B1 提交→一次确认流程。不能为演示问答而增加“是否继续”、按文本长度猜测歧义，或让用户回答本已确定的选项。

## 2. 唯一问题生产者与输入边界

问题只能来自组合根显式安装、通过中立接口注入的受信模板/规划实现。该实现声明有限的必填业务输入槽、缺失判定、问题文字、答案验证器和预览 renderer；模板/槽定义、验证器及 renderer 的版本与摘要在首次提交时冻结。Core 校验这些声明，不信任 HTTP、Worker 文本或可替换报告声明某项是必填。

- 每 Task 最多一批、三个问题；一次展示全部缺失项，不根据每个答案再扩出新一轮问题。
- 首版答案是有界文本：每项最多 4096 UTF-8 字节，禁止 NUL；问题/槽 ID 唯一，问题文字最多 2048 字节。具体格式及业务约束由该槽的冻结验证器检查，不把任意 JSON、宿主路径或命令当控制输入。
- 已有输入满足槽时不提问；所有槽已满足则继续原零问题路径。缺少生产者、无法冻结验证器、无法构造有界可校验预览或要求超出模板范围时明确拒绝，不降级为“全部已满足”。
- 答案仅填充原模板已声明的业务输入/context 槽，不授予文件读取、网络、工具、凭据、发布或任何新副作用权限。答案和 prompt 一样是数据，不是 Policy 或验收授权；不得包含凭据，沿既有受保护 Task 数据保存，不回显于普通日志。
- 冷重开不重新调用可漂移的规划模型生成问题。问题及原输入从账本读取；继续答复时必须能解析同一冻结验证器/renderer，缺失或身份不符则保持可查询、拒绝新写，不替换为当前安装的新版本。

这里的“受信”指由维护者安装的实现和 Core 的机械边界，不意味着任意 Planner 文本可直接决定权威状态。可选语义规划以后可提案，但必须归一到该封闭合同；本片不增加模型调用。

## 3. 同 RB1 的原子记录与兼容性

只有确有问题的新 Task 才选用显式版本化的 `task-clarification/v1` 事实族；零问题 Task 继续原 `bounded-task-draft/v1`，既有 AF_UNIX 批准链不变。新旧记录可在同一 RB1 回放，但旧 reader 遇到不支持的新事实必须明确拒绝，不能跳过后按旧 draft 批准；不重签、改写或迁移历史记录。

首次问题记录原子保存 Task/Goal ID、原规范请求与幂等键摘要、创建时间、固定 `confirmBefore`、冻结模板/输入与初始预览，以及整批问题。问题绑定原 Task、槽、初始 preview 的 subject 摘要、question revision=1 和期限。问题不是后来补到一个已经可以批准的旧 draft 上：崩溃不能暴露“已保存 draft 但问题未保存”的可批准中间态。

初始预览必须由原模板给出完整、可验证的计划边界；缺失业务槽独立列明，不伪造已获得的答案，也不创建 Run、reservation 或创建义务。只有所有必填槽都有合格答案后，当前预览才具备确认资格；无法在该范围内表示的模板不接入本片。

每个有效答案通过同一 current-owner/writer 的一次 compare-and-append，**同时**保存不可变答案/消费回执和下一 preview，绑定前一 preview fact digest、原问题/槽及累积答案摘要。不能先写“已消费”再靠下一次 HTTP 请求补 preview。失败时两者都不推进；冷回放只投影原事实，不重跑 renderer 或执行副作用。

- `Task.revision` 是公开控制 CAS：新 Task 从 1 开始，每个成功答案加 1；后续批准、stop、disposition 沿原规则分别增加。Worker 进展不改变它。
- `previewRevision` 从 1 开始，每个成功答案加 1；每一版保存自己的完整输入 bytes/digest 与 preview fact digest，不修改上一版。批准不生成新 preview。
- 问题批与每个问题的 subject/revision 不变；答案另外绑定提交时的 `expectedRevision` 和当前 `previewDigest`，避免剩余问题被回答到陈旧预览。已消费问题不重新开放。
- `createdAt`、`confirmBefore`、原 Task 身份和所有预算不变。回答、重新读取、断线或冷重开都不获得新确认窗口。

现有 accepted-plan 仍是批准和预算/创建义务的唯一权威提交点。对新事实族，它绑定当前 preview 的精确 fact digest 和 inputs digest；原 v1 Task 仍绑定原 draft，不把原 `TaskDraft.Revision == 1` 校验悄悄改成“任意版本”。

## 4. 预览生成、确认与取消的唯一顺序

预览 renderer 是纯计算，可在短 writer lane 外执行；真正追加答案/预览以及批准时，必须在原 owner 事务内重新读取事实并复核。不得以计算前的状态快照作最终授权。

1. 先通过现有本地认证与 Task 所属授权，再查该 Task/operation 下的精确幂等回执。同 key、同原规范请求（包括原 revision、preview、问题和答案）只返回原操作结果及明确标注的当前 Task 投影，零追加；同 key 异内容冲突。
2. 未命中回执的新答复必须满足：当前 Task 尚未批准、没有 stop intent、未超过原确认/问题期限、正数 expectedRevision 等于当前控制版本、previewDigest 等于当前 preview、问题属于原批且尚未消费。非法形状/非正数版本返回 400；陈旧正数版本、已消费或不同答案返回 409；已过期返回原确认过期错误。
3. 冻结槽验证器验证答案；renderer 只根据冻结原输入及已接纳答案构造下一版，不在上次生成的 prompt 后无界重复追加，也不从可变 worktree/外部路径读取新材料。
4. Core 比较冻结权限与验收边界：oracle bytes/digest 与必需断言、Provider/environment、路径 scope、publication、工具/凭据策略、预算及节点依赖不得因答复变化。只允许模板预先声明的业务输入投影及其必需摘要变化；不能机械证明的变化拒绝，不默认相信 renderer。
5. 提交前再做第 2 项当前性检查，以一条事实追加答案与 preview。全部问题已消费后展示新的完整方案；用户只确认最终一版，不要求每答一题再批准一次。
6. 确认先查精确原批准回执；新批准须重验原期限、零未答问题、当前 preview/revision 和 stop fence，再沿原 accepted-plan 提交。旧 preview 不得通过；确认不隐式消费未答问题。

取消与新答复/确认沿同一 RB1 线性化：stop 先提交，后续新答复和批准拒绝；答案先提交，后到 stop 仍停止该 Task；批准先提交，后续新答复拒绝且不能改已冻结 Run 输入。已经生效操作的精确 lost-response 重放仍是只读回执，不撤销 stop、不复活 Task。确认到期本身不凭空追加成功/取消事实，也不重置身份重新创建同义任务。

以上取消仍执行原显式 revision CAS：若答案已先提交，持旧 `expectedRevision` 的新 cancel 返回 409，服务不得隐式刷新其 revision 或幂等键。客户端重新查询后，以新的明确取消请求和匹配当前 revision 提交，才可与后续动作竞争；“取消优先”不是绕过 CAS 或覆盖已经提交的答案。

## 5. 最小 HTTP 和实现接缝

- `GET /v1/tasks/{id}/questions`：只读返回原批、各题 pending/answered 状态、当前 Task revision/previewDigest 和原期限；有授权才返回问题/答案，不触发规划、修复或写入。
- `POST /v1/tasks/{id}/questions/{qid}/answers`：沿现有保护接受 `Idempotency-Key` 与 `expectedRevision`、`previewDigest`、`questionRevision`、`answer`；不接受 TaskSpec/Policy、任意 questions 创建或宿主路径操作。
- Task 详情从同一事实投影 `awaiting-answer` 或 `awaiting-confirmation`，`allowedActions` 与实际准入一致；取消及过期如实可查。答案成功返回新 Task/preview，客户端取得最新版后可继续答其他题或一次最终确认。
- 中立 Application question Port→原 RepositorySession→原 RB1；规划实现从组合根注入，Core 不 import planning 或具体 Agent。新记录不创建独立状态文件、SQLite 双写或通用问答 outbox。

原 v1 Task 查询 questions 返回空，answer 返回无该问题；不开启额外问答，不改变其原确认请求、取消、自动 Decision 或交付行为。

实施必须一次覆盖原事实消费者，不另建平行验证路径：

- 按实际事实族解析同一 Task 的根请求、当前 preview 和已批准的精确 preview，统一供创建重放、按 ID 查询/list、accepted-plan 与原 deadline 检查、cancel revision/scope/fence 使用。原 `validateTaskDraftApproval` 的“无旧 draft 即 AF_UNIX”分支必须先排除 clarification Task；未答问题或遗漏新记录不得借旧入口获批。
- objective Decision、ReviewPacket 选择及 delivery 继续要求原 `order-quote/v1` 资格与精确原授权。新的测试模板或其他问答模板不因具备 preview/accepted-plan 而继承 `marshal-order-quote-v1` 的 system Decision、固定 ZIP 交付或任何旧业务验收保证。

## 6. 一次性验收与诚实出口

确定性测试使用明确标注、仅测试组合根安装的模板：缺两项真实必填输入→同批提问→逐项答案验证→两版追加 preview→最终确认→原计划接纳；断线精确重放/冷重开保持同 Task、同原期限、同摘要和零重复事实。它不对外注册为业务模板，也不冒充已支持零 Git 业务。

聚合负例覆盖：问题/Task 串绑、重复槽、非法/超量答案、同 key 异内容、旧正数 revision、旧 preview、改答、过期、批准后新答复、cancel/answer/approve 两种提交顺序、renderer/validator 漂移或失败、越界修改 oracle/权限/预算、写入中断、旧 reader 不跳过新事实。现有 `order-quote/v1` 原生产创建/确认/取消/交付回归须保持零问题，不增加模型请求或人工等待。

上述代码及组件测试只关闭 B2-A 的协议/调用链子条件。真正 B2 仍须为后续零 Git 业务模板或受限需求规划接入明确业务槽，在实际用户任务中证明必要问答→确认→真实团队→独立可消费交付，并完成其余 B2 出口。纯 DTO、手写状态、测试模板或多一次确认都不能关闭 B2，也不改善未测得的业务成功率。

本 ADR 接受后允许实现新事实族与对应 HTTP 写入；运行时支持仍须通过本节验收，不自动启用旧实例或授予 B2 完成。它不重开 Workspace/身份/Skill 平台，不扩已运行 Agent 的交互或生产发布权限。
