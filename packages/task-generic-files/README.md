# 通用文件成果入口（开发中）

目标是让 `marshal serve` 使用产品内置的通用团队装配，无须为每个业务提供 JavaScript 配置；用户提供需求、输入和验收要求。此包尚未默认启用，不代表任意 Agent Skill、SQL 执行或外部发布已可用。

本 PR 首个实现是纯布局函数 `bindGenericFilesPlan`：复用原 `TaskVerification.bind`，支持 1–8 个作者、一个 verifier 汇合点、并行或依赖执行、0–16 个输入；节点名和业务描述不写死。原输入通过 `inputs/<artifactId>` 引用，上游成果通过 `upstream/<nodeId>.md` 引用，作者在独立目录生成 `result.md`，最终分支按 `results/<nodeId>.md` 交付。自然语言 scope 不是文件权限，作者不能生成 verifier 的权威证据。

## 同一 PR 后续工作

上述数量是上限而非任意组合保证：布局还受原 Core 的单项可见材料与聚合字节限制。过长 ID 或高扇入导致超限时拒绝计划，不截断上下文、不放宽原门禁。

- 明确并审查通用成果验收合同：独立模型审查与确定性字节完整性检查各自证明什么，不把结构通过当成任意业务正确。
- 接通独立 Review、受管 checker 和有界 Leader，复用原 SQLite、预算、取消与恢复；不造第二状态机。
- 将检测结果绑定实际可用 Adapter，让无业务配置的 `serve` 选择内置装配；发现路径不等于协议/登录已验证。
- 两种不同业务意图走同一真实 HTTP/Agent 交付链，再启用默认入口。

DataAgent 的 MCP/CLI、SQL 发布及补数另有工具归属和外部效果接缝；当前 ACP 的 `acp_tool_scope_unproven` 不会被本包移除。默认只交付成果，不授权外部写。验收语义如需改变，须先明确对应 ADR，再接入默认运行时；本次布局准备不改变既有验收或终态。
