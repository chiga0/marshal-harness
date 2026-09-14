# 通用小型文件团队装配

`createGenericFilesConfig({provider})` 接通既有 Leader、独立 Review、FileBusiness、固定数据 checker 与文件交付；同一配置接受不同自然语言需求，不使用固定区域业务模板。默认 CLI 选择与发行接纳由调用方负责；此包不代表任意 Agent Skill、SQL 执行或外部发布已可用。

本 PR 首个实现是纯布局函数 `bindGenericFilesPlan`：复用原 `TaskVerification.bind`，支持 1–8 个作者、一个 verifier 汇合点、并行或依赖执行、0–16 个输入；节点名和业务描述不写死。原输入通过 `inputs/<artifactId>` 引用，上游成果通过 `upstream/<nodeId>.md` 引用，作者在独立目录生成 `result.md`，最终分支按 `results/<nodeId>.md` 交付。自然语言 scope 不是文件权限，作者不能生成 verifier 的权威证据。

## 范围与运行

上述数量是上限而非任意组合保证：布局还受原 Core 的单项可见材料与聚合字节限制。过长 ID 或高扇入导致超限时拒绝计划，不截断上下文、不放宽原门禁。

- 作者每份成果上限 8192 字节，输入合计上限 32768 字节，均须非空 UTF-8。Core 的聚合上下文限额仍适用，数量上限不是任意体积组合保证。
- 独立 Review 阅读原需求及全部实际候选，判断内容是否满足要求。固定 checker 仅核对实际字节、摘要、编码和大小；不执行候选代码，不证明任意业务事实。
- 下载 `deliverables.json` 含最终分支原文件内容、路径、字节数和摘要。未连接 verifier 的中间成果保留为候选，不重复充作最终成果。
- `service-config.mjs` 接受部署者提供的 `MARSHAL_AGENT_EXECUTABLE`，由既有可执行 ACP 工厂传入封闭 `--acp` 参数。登录仍由 Agent 原生管理，不复制凭据。
- 默认不声明未知 Agent 工具的进程组恢复资格。当前 owner 的正常清理及静止重开使用既有机制；活跃进程崩溃后无法证明归属时进入 intervention，不伪造冷恢复通过。
- 文件权限仅支持明确的 `path/content`、`path/text` 和 `file_path/content` 参数形状，未知参数、shell、MCP及外部写入拒绝；这不是 OS 沙箱。
- 受控 HTTP 测试覆盖同配置两种意图、动态节点/依赖、无输入、独立 Review、校验、下载和正常重开；不等于真实模型验证。

DataAgent 的 MCP/CLI、SQL 发布及补数另有工具归属和外部效果接缝；当前 ACP 的 `acp_tool_scope_unproven` 不会被本包移除。默认只交付成果，不授权外部写。验收语义如需改变，须先明确对应 ADR，再接入默认运行时；本次布局准备不改变既有验收或终态。
