# ADR 0103：受信执行观察与可读输入

- 状态：Accepted（2026-09-14，用户要求执行透明度，root独立设计审查通过）
- 关联：ADR0092、ADR0095、ADR0101

## 问题与决策

当前最后进度将模型输出与工具活动压为 running，实际提示词只留下摘要，用户无法理解团队正在做什么。增加一个显式的受信 `observability={profile:'task-observation/v1',retainPrompts:true}` 配置。它不接受回调、路径、网络目标或任意代码。默认不开启提示词留存。开启后仅由内置有界纯文本处理器在现有输入审计路径记录实际投递文本，遮盖常见凭据表达；这是有限规则而非完整秘密检测保证，部署者应只对允许留存的任务启用。Agent 自有系统提示、隐藏推理、原始工具参数和结果不采集。

保留任意 `auditDisclosure` 回调在 v7/unpermitted 下的禁止。内置观察只做当前受信文件暂存和同事务引用提交，不创建子进程或网络副作用；不扩大 never-permitted 证明。观察不能参与授权、调度、验证与 cleanup。

## 最小可选 API 扩展

`Worker.observation` 可缺失；存在时字段均固定：

- `profile:'task-observation/v1'`、`activity`：`starting|waiting|thinking|output|tool|retrying|compacting|stopping|terminal|unknown`。
- `observedAt`：Application 接收时间；`sequence`：本 Worker 单调观察序号。
- `tool`：null 或 `{id,kind,status}`，仅 Provider 已报告的信息。
- `model`：null 或 `{id,source:'provider-reported'}`；不把配置猜作实际模型。
- `usage`：null 或 `{inputTokens,outputTokens,totalTokens,source:'provider-reported',complete}`，计数为非负安全整数或 null，不推断成本。每 Worker 当前累计快照，禁止把重复快照相加。

`Audit.prompts` 继续使用既有 prepared/handed-off、policy-redacted、snapshot、previewTruncated 字段。内置策略 id/version 与摘要绑定；旧记录不回填，缺省仍 unavailable/metadata-only。新字段只对当前代码支持的读取者有保证；旧闭合 Schema 客户端需升级，不能宣传旧客户端自动兼容新字段。

## 生命周期与恢复

活动是最近一次来源明确的观察，Worker 生命周期是当前状态，两者分离。无新观察不推断正在思考；终态覆盖旧活动为 terminal，unknown 为 unknown。重新打开只读历史观察，不恢复 stream，不产生新用量或活动。终态 token 是最后报告读数，complete 只由 Provider 明确终结报告赋值；任务聚合标明覆盖范围，缺失不按零伪造完整结果。

## 验收

Provider 事件归一化；小输出/连续输出/工具结束后输出；陈旧与迟到观察不覆盖终态；重复用量不重复计数；不含推理正文；prompt 长度与脱敏、不开启不留正文、旧根不回填、任意回调依然被拒；OpenAPI 与客户端验证、重开和原恢复测试。

## 审查收敛

只在显式 `observability` 配置下新增 Worker/Event 字段；默认关闭保持旧闭合响应形状。新客户端接受旧响应缺省。`profile.json` 冻结配置、内置策略 id/version/sourceDigest；开关或代码策略变化须使用新根，不对旧根静默迁移。

`ObservationFrame` 是上述字段加 `publicText`（最大512 UTF-8字节，默认空串）。`Worker.observation` 增加 `history:ObservationFrame[]`（最新64条）与 `historyTruncated:boolean`。`Event.observation` 为可选单帧。片段只来自已完整收到的公开模型输出；不把逐token未闭合内容提前披露，以免凭据跨分片绕过规则。工具仅归一化kind/status/ID，不披露args/results。内置脱敏是有限规则，不能保证识别任意业务秘密。

输入暂存失败时保留 metadata-only/unavailable 观察，不存任何半成品引用，不改变mayStart、原budget或never-permitted判定。暂存是既有受信Depot的本地有界操作，没有回调/进程/网络；必须保持原reservation、custody许可和启动事实分别校验。崩溃遗留无引用对象不是可消费证据，也不表示执行已经启动。

历史同时限制64帧与序列化16KiB，超额丢最旧并设置historyTruncated。Audit是最多256个Worker的汇总，保留当前观察但history为空并标明截断；完整history从Worker详情/列表取得。Events沿原每页100条上限，不提高8MiB HTTP响应限制。策略sourceDigest覆盖观察实现、内置输入留存及共享纯脱敏模块。

用量明确从新观察快照读取，未修改Provider终态receipt的旧usage含义。Worker的Usage为其最新观察的reported投影；Task的Usage对已预留agent/leader/review成员取最新totalTokens求和，coverage为完整读数成员占比，其他独立执行类型不计入分母。没有任何读数或整数溢出为unavailable，成本始终null。Pi相同消息重复不相加，同timestamp不同消息碰撞降为部分读数，溢出丢弃计数；不声称部分累计为完整账单。


## 原无许可恢复边界保持

实施核验证明 ADR0092 的 unpermittedProof 明确只承认 metadata-only。当前不扩展该证明：`retainPrompts:true` 与 `unpermitted` / `execution.startProtocol` 组合在 Service claim 前及直接 Application 构造时拒绝；v5和v6可选协议均适用。`retainPrompts:false` 仍可观察公开活动元数据。新generic-v2使用普通v7 custody，不授予never-permitted例外；没有原许可事实但也无独立证明的执行继续unknown。旧原票据、cleanup规则及所有否决事实保持不变。
