# ADR 0084：Pi 终态按结果类型定界，保留唯一声明与独立验收

- 状态：候选（Proposed；仅随当前 B2 PoC 候选验证，不宣称 main 已接受）
- 日期：2026-09-07
- 关联：ADR 0075 §3、ADR 0080；B2 三节点业务交付

## 证据与边界

真实 canary 34094155668 在 Collect 阶段失败，两个 Run 的 journal 已记录启动。固定 server 报 `pi-result-final-content-shape`；原始终态文本未被归档，不能断言模型具体输出。源码可确定复现：终态中两个完整 JSON 对象会由未分类错误落到这个误导分类，即使第一个只是业务示例。此次失败不计成功，不原样重试，不为旧 Run 补签 Decision。

## 候选决定（替代 ADR 0075 §3 的对象计数部分）

1. 仍只选择完整 Pi 协议中最后一个非重试 `agent_end` 的最后 assistant，且必须是唯一非空 text item；不从历史、thinking 或 tool 借用结果。
2. 按完整 JSON object/array 边界扫描文本；已解码容器内部不再搜索。完整非结果对象与数组可以作为前置说明，但不能包装待接纳结果。遇到无法解码的容器起点立即拒绝，不在损坏容器内猜测结果，也避免反复解码嵌套前缀。
3. 恰好一个顶层 `kind=WorkerResult` 对象才是声明。计数不以 schema、版本或身份是否正确筛选，两个声明即拒绝，不能挑一个“正确”的。声明后必须只有空白；代码围栏、额外 JSON、报告文本均拒绝。
4. 每个解码容器在读取 `kind` 前走 Core JCS，递归拒绝重复字段（含转义同名），避免 map 覆盖隐藏声明。返回原声明的规范化字节，不把其他业务 JSON 拼入结果。
5. 声明仍必须经过原 Normalize、完整 WorkerResult schema、冻结 Task/Run/Attempt/adapter/session 校验及 Marshal 观察字段覆盖；原始 transcript 摘要绑定、current-ledger recheck、独立 Verification/Decision、发布权限均不变。解析成功从不等于业务正确或 ACCEPTED。
6. `final-object-missing/multiple/invalid/trailing` 为固定诊断码，不输出模型内容、字段值或原始解析错误；诊断不授予重试权限。

## 验证与退出

正例覆盖裸结果、散文、业务 JSON/数组前缀；反例覆盖重复声明（含错误身份声明）、完整与损坏 wrapper、截断、重复/转义/嵌套字段、尾随内容、schema/身份漂移和多 text item。经 exact-head CI 后再做一次三节点实机，原独立评审、可重建业务交付物和耐久 GoalOutcome 全部成立才可称该 PoC 通过；B2/B3 全量退出条件不缩减。
