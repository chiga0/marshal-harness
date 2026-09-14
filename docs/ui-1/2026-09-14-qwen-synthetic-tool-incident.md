# Qwen 系统工具导致人工验收再次失败

## 现场与根因

PR #311 后，用户真实验收 Task `task-a806fcd9-af3d-4636-9e30-94bcf00e3243` 再次进入 `intervention/cleanup_unconfirmed`。首次额外范围发生在 `2026-09-14T11:44:17.895Z`，对应 Qwen 两毫秒前调用 `record_artifact`；之后还调用 `get_goal`。原进程组已有停止证据，但不能因此证明额外范围清理，Core 保留 unknown 正确。旧任务不恢复、不重新标记通过。

问题是显式 Qwen 文件配置误把 `--core-tools` 当成全部工具白名单。本机 Qwen 对 synthetic 工具豁免该白名单；单次真实成功只说明那一次未选择额外工具，不足以证明配置稳定。

## 修复与证据边界

- 两个显式 Qwen 配置共享原生允许与拒绝参数，补齐 artifact、goal、plan、team 等非文件工具拒绝；不改 Core、HTTP、SQLite、清理资格或权限回调。
- 独立无模型实机探针，只执行 ACP initialize/session/new：旧配置最终 19 项，新配置恰好 6 项（list_directory/read_file/grep_search/glob/edit/write_file），两个实例均正常停止。没有调用模型或把 argv 字面断言当成实际注册证明。
- 自动化覆盖关键 synthetic 拒绝、允许/拒绝互斥、两个配置共用策略与发行库存；完整文件任务实测另行记录，不能用初始化探针替代交付。
- 范围仅显式 Qwen 文件 profile；不声明任意 Agent、未来工具或外部效果已支持。旧服务根冻结配置仍不迁移。

## 三条验收

| 验收线 | 状态与范围 |
| --- | --- |
| 功能与可靠性 | 注册与定向回归通过；完整新任务失败，详见下节，不宣称交付可用 |
| 视觉与交互 | 本次无 UI 代码修改；既有异常提示准确反映此次失败，不代表流程已可用 |
| 产品可用性 | 用户真实验收失败；修复后待重新验收，不以独立 Agent 检查替代 |

## 候选 644a2e27 的完整实测与下一阻塞

- Node 24 定向及两种 HTTP 场景 14/14、Node 22 定向 11/11、Node 22/24 同包发行各 11/11 通过；独立未知范围负向回归 3 组通过。独立审查无 P0/P1，secret scan 未发现泄漏。
- 新 Task `task-bd476aba-26fe-403e-81e5-0cf48ba07f16` 实际运行 318511ms、4 次执行、retry/rework 均 0。作者只调用文件工具，成功生成 6629 字节原始 HTML，无 synthetic 工具或权限循环；SHA-256 `b7d71514a8944e0ef6534bab3fc7192ea5b27dbc5f6756947dc7efe2e4ae69a5`。
- 后续独立 Reviewer 输出 accept，但把冻结 inputDigest 的 `…92be5937d3dd…` 抄成 `…92be6937d3dd…`，单字符错误。其余字段形状、selectionDigest、summary 长度均合法。Core 正确拒收为 `invalid_review_report`；最终 failed/revision24，未交付、验收仍 pending。没有修改该报告、重启重试整队或把临时 HTML 算作最终交付。
- 本机受限证据根 `/Users/gawain/.marshal-qwen-html-integrated-tmjz7r`；不提交原始运行数据或私有模型内容。新服务已正常停止，旧用户服务未修改。
- 后继应按 ADR0101 的运输身份与业务判断分离原则，为独立 Review 单独设计显式短报告及可信本次 ticket 绑定，先明确 ADR 与负向测试。不能修补现有长报告、静默回退或仅堆叠“复制准确”提示后宣称确定性问题解决。当前 PR 仅关闭工具配置根因，不关闭完整产品可靠性问题。
