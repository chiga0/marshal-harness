# ADR 0101：通用文件 Leader 的显式模型输出与受信绑定

- 状态：Proposed（2026-09-14，待维护者独立审查；未实现、未启用）。
- 基线：`9348f569a0612ad2174495762645d3bd974ba146`。
- 关联：ADR0094、ADR0095 及其机器合同 §1–§2、ADR0100。

## 问题与取舍

真实通用文件任务出现了业务动作字段合法、但模型复制的 `callId` 与本次调用不符的拒收。增加复制提示不能提供确定性绑定。模型负责业务判断，由受信执行通道提供调用身份更符合现有职责边界。拒收历史保留，本提案不重新接纳旧输出，不证明作者成果或整体任务已通过。

现有 `createLeaderPort` 支持固定 `parseDecision({ticket,completion})`：先用 `parseManagedOutput` 检查原 JSON，再运行受信 mapper，最后严格校验完整 `LeaderDecision` 与动作。无需另建模型服务、状态机或放宽 Core 校验。

## 决定（接纳后才可实施）

仅为通用文件业务增加显式选择的新组合配置。模型输出为闭集 `{profile:'generic-files-leader-proposal/v1',summary,actions}`。`actions` 仍为原 `1–policy.maxActions` 项、六类动作及全部字段限制。新 wire profile 是模型建议格式，不是新 Task、Core、SQLite 或 HTTP profile；独立 Review 的原格式不变。

固定、同步、纯内存 mapper 按以下顺序处理：

1. 原输出仍限制 64 KiB，沿原严格 JSON 解析拒绝重复键、BOM、非法 Unicode/NUL 和超限；不得先宽松解析丢掉重复键再映射。
2. 精确检查新 profile 和三个顶层字段，不接受 `callId`、`inputDigest`、额外键、旧 profile 或自动探测／回退格式。旧配置仍严格接受旧格式，旧 profile 错误绑定一律拒绝，不能删除错误字段后接纳。
3. 仅复制模型 `summary/actions`；仅从此次受管回调的原 `ticket.input.leader` 取 `callId/inputDigest`，补成原 `task-managed-leader/v1` 封套。不取最新数据库快照、不访问其他 ticket、不使用全局缓存，不运行 I/O、模型或异步“修复”。
4. 原端口重新校验映射后对象总大小、闭集字段、精确身份及动作形状。动作中的节点、选果／Review／验收／repair 摘要保持模型原值，不能补造、纠正、删动作或降低必需检查。
5. 原 Provider handle、私有 receipt、正常 `end_turn`、原 cleanup、owner/generation、取消／期限／预算、readSet/currentness 和事务接纳不变。错误、未知或晚到完成不能因能映射而变成成功。

模型不再回显运输身份，不等于证明它读过输入。输入仍按原 builder 完整提供，prepared/handed-off 只证明实际交接；摘要回显本来也不能证明理解正确。业务正确性继续依靠独立 Review、客观文件核验与所需交付出口。

## 配置、证据与兼容

- 首批只允许新通用文件显式配置／新空根，`publication:null`；不改变 `serve`、`--generic` 或现有 Qwen 配置默认选择。不得借此接入 SQL、MCP、任意 shell 或外部发布。
- 新配置的稳定 port ID、mapper 实现身份和策略身份通过现有冻结机制与旧配置区分。沿现有可序列化 policy 及组合源码摘要承载，不新增动态 mapper 参数或函数序列化。启动前验证配置漂移被拒绝；不得只换函数而保持原配置身份。
- Core 仍存原完整 `LeaderDecision`、证据摘要和动作，不增加 SQLite 字段、profile 版本、公开投影或 HTTP 字段。此记录解释为“受信绑定后的决定”，不是模型逐字返回。
- 原始 `completion.outputText` 与映射结果在解析测试和实机私有验收记录中分别保存／核对，原始 bytes 与规范化决定摘要不能混用。产品当前不保证耐久保留全部原始输出；没有原始 bytes 时如实标记 `unavailable`，不能由封套反推模型输出。
- 本提案不增加 raw-output 存储标记、公共诊断正文或审计回调写入口，不让 mapper 写 Store。若后续要求产品耐久关联原始输出摘要／制品与映射后决定，须明确追加该存储与披露合同再实施；不能默许新增字段或承诺此增强已可用。
- 不改旧根、终态、失败和旧 profile 错误结果；不能用新配置重签旧结果、释放未知占用或重置预算。

## 最小验收与实施顺序

1. 新合法输出确定性映射；旧 profile 错误绑定、新 profile 夹带运输字段、未知键／重复键／BOM／超限均拒绝，不能回退。
2. 同一原 ticket 的绑定可复算；模型动作原值逐项不变。错误摘要、非重试失败 repair、无真实等待的 wait、未交付 succeeded 仍被原 Core 拒绝。
3. 原 SQLite 链覆盖计划确认→作者→独立 Review→核验→交付→总结；相关快照变化、取消、迟到／重复完成、错误 Provider/receipt 不接纳；无关进度不引起无谓重试。
4. 新配置正常重开、旧配置身份漂移拒绝；发行清单与同包消费覆盖新配置。不对旧根迁移。
5. 确定性通过后做有界真实模型任务，保存精确原输出与映射结果的私有验证记录，不以 fixture 成功宣称真实交付；失败不剔除。UI 三线按影响另验，不提升 release／可用性结论。

本 ADR 仅在上述显式配置替代 ADR0095 机器合同中“模型回显运输身份”的职责要求。Core 身份、动作含义、持久化、独立验证、权限、生命周期、恢复及发行规则不替代。接纳文档不等于启用新配置。
