# 人工干预空入口与 Qwen 文件交付修复

## 问题与范围

用户真实 Todo HTML Task `task-fbe8e6bb-83be-4768-abe9-c0d05f383ff7` 显示 `intervention / cleanup_unconfirmed`，但概览显示「需要你的处理（0 项）」且没有解释。该状态不是一条待回答的问题。此次修复不增加虚假的确认清理按钮，不修改历史 Task，也不把重新启动服务视为恢复成功。

原执行记录包含 `acp_tool_scope_unproven`。即使原受管进程清理回执为成功，额外执行范围仍未得到证明，因此应用保留 `extra_scope_unresolved`；不能用进程已经退出或 PID 不存在消除该事实。原配置没有跨代清理资格，不能事后伪造该资格。

## 本批修复

- UI 单独显示系统异常告警、原因和团队／活动诊断入口；异常与真实待答问题可以并存，零问题不再意味着任务健康。
- 增加显式 Qwen 文件交付配置，限制已知原生工具范围；不修改通用 ACP 默认配置，不宣称 OS 隔离，也不覆盖未知 hooks／动态工具。
- 支持 Qwen `edit` 的空 `old_string` 创建语义，但仅限不存在的 `result.md`、原作者权限和非空不超过 8192 UTF-8 字节的内容。已有文件、链接、其他路径及未知参数继续拒绝。
- Leader 提示生成器不再把 `retryable:false` 或缺少该字段的执行失败放入修复示例；完整原始证据仍然提供给 Leader。活跃兄弟执行存在时不能抢先修复，Core 原准入不变。
- 单一最终文件需求不应让多个并列作者各重做完整交付；有互补分工时经最终整合作者汇合，独立 Reviewer／Verifier 不承担作者工作。

## 证据与限制

集成基线：`12db5f908e1a15331e4edb06e9db3cd8c8c16d80`；各轮候选见下文，不把不同源码测试合并为一次完整验收。当前分支为 PR #311，尚未合入或发行。

上一轮真实专项 Task `task-32c8f09c-118c-4439-848d-aeeb9d9e10a9` 失败，不能算作成功验收：其 Leader 实际提交了对不可重试失败的 `repair`，可从保存的原始报告核实。作者曾有 `edit:failed`，但缺少具体调用参数及候选文件，**不能认定就是空旧字符串创建缺口导致**。所有四个执行的清理确认成功且无额外 scope；这只能证明该轮清理，不证明业务完成。

候选 `5046c059` 再验证使用原用户需求，不添加业务答案、不自动重试。Task `task-c7d99193-8913-417c-a87e-0c9d33d5036e` 在 87.057 秒后因 `provider_progress_limit` 失败：只发生一次规划执行，没有产生计划、作者、审查或交付；服务正常停止。因此本轮没有实测到作者编辑修复。ACP 原计数在分类之前对所有 update 累加，文本／思考碎片与工具事件共用 4096 次限额；正在补有限流量下的碎片兼容修复，不以扩大 Core 上限解决。

后继 `9348f569` 将非空 ACP 文本／思考碎片按累计 8 MiB JSON 字节控制，正文仍限 64 KiB，空更新／非文本／工具等仍受 4096 次限额约束；不会丢弃业务结果后冒称完整。定向 13 项通过。完整 CI 随后发现两个测试接缝需同步：枚举扫描把 `agent_thought_chunk` 事件名误判为错误码；旧洪泛 fixture 将现已合法的 4100 小文本片段仍当超限。`096c635d` 补事件分类、真实越界字节和空更新洪泛，独立审查及集成 10/10 通过；四条真实协议洪泛都按原原因失败并确认清理，同服务后续受控团队交付成功。该轮完整 CI 的失败仍保留，不冒称新全量已绿。

`9348f569` 的原需求实机 Task `task-95f4216b-bfee-4da0-a957-2e6a967c18df` 在 322.223 秒、3 Attempts 后失败；没有 `provider_progress_limit`，但未到独立 Review 或交付。实际作者 `write_file` 内容为 16067 UTF-8 字节，超过 8192 上限；实际冻结计划／作者输入未说明该限制，工具正确拒绝。`4eb635e7` 将限制及原始源码格式通过 layout→plan.acceptance→真实作者 prompt 传入，HTTP／layout 5 项通过且独立审查无 P0/P1；不提高大小或路径权限。

该轮 Leader 的失败结论依据合法（匹配 `snapshot.history` 单项摘要），但 `callId` 与原 ticket 不同，端口正确拒收。已独立审阅接纳 [ADR0101](../adr/0101-generic-leader-model-wire-binding.md)，仅允许显式新配置由原受信执行绑定运输身份，不纠正动作／依据、不重新接纳旧结果；后继实现及实机证据见下节。

### 候选 ff468dfb：原需求真实交付与独立后验

完整 sourceHead 为 `ff468dfb387d5ca1e65bd327af86184384ebcf42`。显式 `qwen-short-service-config.mjs` 在新根运行 Qwen 0.23.2；原始用户意图不改写，冻结计划一名作者、独立 Review 和固定文件核验。Task `task-f1b274b8-e80a-4479-9e4d-8e5da6db3a6f` 为 `completed`，耗时 303608ms；8 Attempts（包括五次 Leader 调用、作者、Reviewer、固定 verifier），retry=0、rework=0。独立 Review 为 accept，文件验收 passed，无外部发布。

独立终态审计核对 8 个执行全部完成，8 条 custody observation 均为 clean、原进程组范围，activeWorkers=0；完整 94 条公开事件未含 extra_scope。5/5 Qwen 持久化 assistant 非 thought 原文与 Core 的 summary/actions 一致，原文无运输身份字段，各冻结输入绑定匹配。这不是独立 ACP wire 字节捕获，也未另行密码学复验 custody 签名；不扩大证据等级。

交付 HTML 为 5722 字节，SHA-256 `32ad66f98034ac33f17d4e6a901aef40a19fc9756dbca679322df2cb04f4c8ea`。独立 Chromium 实际完成新增、编辑保存/取消、删除、完成/清除及刷新持久化等 9 项检查，无页面错误、无外网请求；原始文件字节未修改。375px 无整页横向溢出。保留 P2：完成切换使用不可键盘聚焦的 span、窄屏添加按钮文字换行；不把业务功能通过写成全部无障碍或视觉质量通过。

正常停止再 open：revision 41 不变、Attempts 8→8、8 个 Worker ID 与交付摘要不变，下载原制品一致；只证明终态正常重开，不证明活跃崩溃恢复。重新打开的真实 UI 概览、成果、团队均显示完成，无人工干预误报。当前封装路径仍为 `results/author.md`，Web 主下载仍为 JSON 容器；浏览器测试使用消费者提取的原始 HTML 字节，不声称已提供一键 HTML 下载。

本机私有证据根：`/Users/gawain/.marshal-qwen-html-integrated-Ow7cUd`（result.json、reopen-result.json、delivery.json、delivered.html）；仓库忽略目录 `.marshal/qwen-raw-audit-f1b274b8/final-independent-audit.json` 与 `.marshal/todo-html-browser-20260914/`（functional-result.json、marshal-ui-result.json、截图）。不提交原始运行数据；远端读者不能假定这些本机路径可访问。原验收脚本的 functionalHtmlAcceptance=not_run 保留，后续浏览器证据独立记录，避免把后验冒充运行时自动核验。

| 验收线 | 当前状态 | 证据／缺口 |
| --- | --- | --- |
| 功能与可靠性 | 本次范围 PASS；完整发行待验 | 既有定向测试、显式短 wire 正负例及 HTTP 两意图/取消/重开通过；原需求真实交付、正常重开和 HTML CRUD 通过。默认 init 未切换、旧异常未恢复、活跃崩溃与同包发行未覆盖 |
| 视觉与交互 | PARTIAL | [原异常页面验收](2026-09-14-intervention-browser.md)及完成 Task 概览/成果/团队实测通过；HTML 桌面/375px已验，保留键盘完成控件 P2；原生缩放及完整主题矩阵待验 |
| 产品可用性 | NOT_RUN | 用户报告的误导已转为回归用例；尚无修复后无指导真实用户结果，不以子 Agent 代码审查替代 |

独立审查覆盖本批集成 diff，无新增 P0/P1；不代表完整 UI 或完整产品已通过验收。保留旧 Task 的异常证据，未清库、未发布新版本、未把候选源代码测试冒充同包发行测试。
