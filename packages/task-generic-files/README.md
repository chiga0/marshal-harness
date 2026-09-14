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

## Qwen 文件交付专项配置（显式选择）

`qwen-service-config.mjs` 是 Qwen 专用配置，不替换通用 ACP 工厂，不适用于其他品牌。它使用原生 `--approval-mode default`、文件工具允许列表与 shell/网络/MCP 等工具拒绝列表；拒绝列表旨在避免原生自动批准绕过 Marshal 的权限回调。仅设置 `default` 并不足以覆盖用户原生 allow 规则。CLI 不支持这些参数时应报错，不能静默退回无限制启动。

通过已有 `--config /absolute/install/packages/task-generic-files/qwen-service-config.mjs` 显式选择，仍需设置 `MARSHAL_AGENT_EXECUTABLE`。不改用户 settings，不复制登录文件，不提供运行生成代码或 ETL 的权限。此配置不覆盖任意原生 hooks/动态工具，不是 OS 沙箱，也不赋予跨代清理资格；未知 scope 仍然阻断。

已有 `extra_scope_unresolved` 记录不能通过更换配置、清空数据库、修改资格位或凭 PID 消失结算。该配置属于新执行的预防措施，不是旧任务恢复接口。完整恢复机制尚缺，不得声称已经修复。

2026-09-14 实机发现 Qwen 的 `--core-tools` 仅限制其 core 集合，不限制额外注入的系统工具；旧配置仍暴露 `record_artifact`、`get_goal` 等。Qwen 文件配置共用 `qwen-file-tools.mjs`，补齐系统工具的原生拒绝列表。当前本机 Qwen 无模型 ACP 初始化验证：旧配置注册 19 项，新配置只注册 6 个文件工具。此结果不保证未来版本或任意扩展的工具全集；升级需重新核对实际注册集合，未知执行仍按原 Core 阻断。不修改原生登录、不清除旧任务的范围记录。

## 显式 Leader 短协议配置

按 ADR0101，可对新空数据根显式选择 `qwen-short-service-config.mjs`；原 `service-config.mjs`、`qwen-service-config.mjs` 保持原解释；新安装默认选择见下节。该配置保留上述 Qwen 文件工具边界，仍需 `MARSHAL_AGENT_EXECUTABLE`，不配置外部发布。

Leader 模型只返回 `generic-files-leader-proposal/v1` 的 `profile/summary/actions`，受信端口从此次原 ticket 绑定运输身份；动作与证据摘要不纠错，旧协议错误身份不接纳。Core 记录的是绑定后的决定，不是模型逐字输出。产品不保证保存全部原始输出；实机验收需分别保存原始输出与映射结果的私有证据，未保存时不能反推。独立 Review 仍用原协议。新配置 ID 与源码摘要冻结，旧根拒绝配置漂移，不迁移或复活失败任务。


## 新安装默认：Leader 与 Review 短协议

[ADR0102](../../docs/adr/0102-generic-review-wire-and-new-install-default.md) 的 `qwen-review-service-config.mjs` 是新安装的 Qwen 默认文件团队。Leader 保持 ADR0101 的短建议；Review 仅返回 `generic-files-review-proposal/v1` 的 `profile/verdict/summary/findings`。受信 mapper 从本次原 ticket 绑定 inputDigest 和 selectionDigest，业务判断原样交给原严格 Port 校验；旧格式、夹带摘要、错误字段或不完整 cleanup 均不修补。独立审查仍读取完整冻结输入及候选。

新配置 ID 和策略摘要与旧组合不同，`publication:null`。配置显式启用 `observability={profile:'task-observation/v1',retainPrompts:true}`，披露限于实际可取得、策略允许的交接输入；不记录隐藏推理，缺失或旧执行内容不能反推。

新本地启动设置使用 `genericProfile:2` 和 `generic-team-v2` 默认根。既有 generic 设置／根不自动升级；显式选择旧配置仍可用。试用新版用新的 settings 和数据根，不能把旧失败结果重新解释成成功。默认只面向 Qwen；其他 ACP Agent 继续显式部署自己的配置。
