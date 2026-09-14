# ADR 0102：通用文件 Review 的受信绑定与新安装默认配置

- 状态：Accepted（2026-09-14，root 独立审查本草案后接纳；实施与实机证据分别验收）。
- 基线：`80f6fed5203b8b6c52c3add0d3852a7ca512b6cf`。
- 关联：ADR0094、ADR0095、ADR0100、ADR0101。

## 问题与决定

真实任务的 Reviewer 给出 accept，却把 inputDigest 复制错一个字符；Core 正确拒收，用户仍无法得到交付。ADR0101 只替换 Leader 的运输身份回显，独立 Review 仍有同类接缝。默认新安装又仍选择原通用 ACP 配置，没有使用显式 Qwen 文件工具限制和 Leader 短报告。

增加独立的新通用文件组合，不改旧 `service-config.mjs`、`qwen-service-config.mjs`、`qwen-short-service-config.mjs` 的选择语义，不修补旧输出或失败任务。

### Review 模型协议

新模型协议为闭集 `{profile:'generic-files-review-proposal/v1',verdict,summary,findings}`。verdict/summary/findings 的业务含义、条数、长度、节点引用、accept 不带 findings 等全部沿用原 Core 规则。输入仍含完整冻结 Review 输入和实际候选材料，不通过省略输入制造短报告。

固定同步纯 mapper 先以原严格解析器检查原输出，拒绝重复键、BOM、非法 Unicode/NUL、超限，再检查新协议四个顶层键。不接受旧 profile、inputDigest、selectionDigest、其他键或自动探测／回退。

mapper 只从本次原 `ticket.input.review` 补入原 `task-independent-review/v1` 的 inputDigest 与 selectionDigest，仅复制模型 verdict/summary/findings。两个摘要标识本次不可变审查输入与候选集合，均由受信调用通道绑定；不从模型猜测，不查询最新数据库，不跨 ticket、不使用缓存、不执行 I/O。findings 的节点、要求、观察和修改建议不得纠正、删改或补造。

原 Review Port 对映射后结果再次执行完整封闭字段、摘要、业务形状校验。receipt、Provider 身份、end_turn、cleanup、owner/generation、预算、取消、迟到完成、currentness/readSet 与事务接纳不变。模型复制摘要不证明理解；受信绑定也不证明业务正确，业务正确继续依靠独立审查和客观验证。

新配置使用新 Port ID、策略版本与源码摘要冻结 mapper/提示词/组合身份。Core 持久化原完整 Report，不新增 SQLite/HTTP 格式；该 Report 是受信绑定的决定。原始输出与映射结果在私有验收记录区分，公开观测遵从独立披露合同，不能从映射结果反推原始 bytes。

### 新安装与既有根

新安装默认 Qwen 文件团队选新配置，同时使用新的 `generic-team-v2` 默认数据目录和持久化 `genericProfile:2` 的本地启动设置。仅该默认产品路径面向 Qwen；其他 ACP Agent 继续显式指定自己的配置。

已有 generic 设置无新标记时保持原配置语义与原 `generic-team` 默认根；再次 `--generic` 不升级旧 generic 配置。安装包升级后，generic 配置按已冻结 profile 从新 installRoot 重新定位，禁止新服务混用旧包模块；显式 `--config` 仍优先并保留部署者路径，启动器不修改既有 Store 的配置摘要、不迁移根、不重签终态。需要切换的部署者使用新 settings 目录和新不存在的数据根，或者显式新配置及新根；同一 live 连接禁止重配置。

新配置显式启用 `observability={profile:'task-observation/v1',retainPrompts:true}`，由观测合同限定真实交接输入、脱敏、存储与披露；新组合策略冻结该设置，不改变旧配置。配置已启用不等于任意旧执行都存在正文。

新配置 publication:null，仍只有文件工具，没有外部业务发布权限，也不是 OS 沙箱。原生工具实际注册必须探针验证，不能只断言 argv。

## 验收

1. 新合法短报告绑定原 ticket、业务字段逐项不变；旧配置与旧 profile 的错误摘要仍拒收。
2. 夹带摘要、未知键、重复键、BOM、超限、非法 verdict/findings、未知节点均拒收；失败／未知／取消／迟到／错误 Provider／未清理完成不能接纳。
3. 原 HTTP/SQLite 受管链覆盖计划批准→作者→新 Review→核验→交付→结论；相关 currentness 与恢复回归保持。
4. 新配置策略稳定且与旧配置不同，同包包含全部文件；新安装选新路径，既有设置保持不变，显式配置不被替换，live 配置冲突仍拒绝。
5. 确定性测试与独立审查后开展有界真实模型与浏览器任务。失败保留；接受设计或 fixture 通过不代表真实产品验收通过。

本 ADR 仅在新组合替代 ADR0101 中“独立 Review 原格式不变”的范围及新安装默认选择；不放宽独立验证、授权、持久化、恢复和 UI 三条验收规则。
