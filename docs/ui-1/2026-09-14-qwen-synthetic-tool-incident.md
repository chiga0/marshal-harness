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
| 功能与可靠性 | 无模型真实注册检查通过；定向回归及完整新任务结果待补 |
| 视觉与交互 | 本次无 UI 代码修改；既有异常提示准确反映此次失败，不代表流程已可用 |
| 产品可用性 | 用户真实验收失败；修复后待重新验收，不以独立 Agent 检查替代 |
