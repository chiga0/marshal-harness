# ADR 0105：原文件业务对象的一次性固定作者指导

- 状态：Accepted（root 与 observability_audit 已独立接受设计；实现验收见冻结后独立审查，实机结果另记）
- 日期：2026-09-15
- 基线：`389125e7a93590df9e215ed1eecd30e3e5a46917`
- 关联：ADR0094、ADR0101、ADR0102、ADR0103

## 问题与证据

真实 S02-a0854099-1 作者输入为 6562 字节，SHA-256 为 `e46475e06e034be673ac016a2b39f74656e149572912daaec536353fcc41ef6e`。原 Audit 保存的实际输入只有文件权限说明与完整 Task/plan/node，没有新配置的事实来源、操作效果、数据来源指导。Leader 的自由文本计划也没有转述这些规则。相反，S01 的计划曾把事实来源要求收紧为禁止任何建议暗示。两者说明固定配置指导不能依赖模型转述。

这不解释所有业务失败：C01 Reviewer 已经收到完整指导仍错误接受恢复方案。固定作者指导降低交接遗漏，不证明模型一定服从，也不替代独立业务验收。

## 决定

新增 `registerFileAuthorInstructions(business, {profile:"file-author-instructions/v1", text})`。只接受本模块创建的原业务对象，拒绝副本、包装对象、staging-only 对象、已关闭或已尝试任何 `prepare`/`prepareManaged` 的对象，以及重复注册。对象身份和状态保存在私有 WeakMap，注册只复制不可变字符串，不保留选项引用、不接受回调或动态模板。参数必须闭合，文本为非空、合法 Unicode、无 NUL、最多 16384 UTF-8 字节。

任何准备尝试，包括失败尝试，立即封闭注册。staging factory 在对象交给调用方之前已完成原身份登记，没有外部注册窗口。旧 staging factory 仍只接受原 `authorize` 参数；本接口不成为未获执行许可阶段的新扩展点。

仅已批准且真实 role 为 `author` 的原 `prepare` 拼入固定指导；planner、reviewer、integrator、verifier 与受管准备均不拼入。原票据和业务字段不改写，最终完整提示仍经原 MAX_PROMPT 限额校验，超限失败，不截断。原对象、prepare 函数及权限、采集、清理逻辑保持原身份。

新 review-wire 配置调用原 businessFactory 初始化 depot 与原闭包，获得对象后登记指导并原样返回。旧 generic index 文件字节不变。指导包含事实来源、操作效果、数据来源及允许用户所需创作的边界；Leader 同步明确不能把合理建议一刀切为新禁令，但不通过 parser 修写模型提案。

注册实现源码与固定指导文本均进入新配置 policy 摘要。旧未注册配置提示逐字保持；已有根不自动接入新指导，新配置身份改变须按原新根/显式升级规则处理，不把拒绝旧根当升级成功。

## 证据与权威

Coordinator 在原 prepare 完成后执行原 Audit 捕获，再把同一提示交给 Provider；Provider 不二次追加或改写。本变更不改变生命周期、预算、调度、权限、no-permit 门禁或作者不能自证验收的不变量。

验收包含未注册提示逐字基线、非法/外来/包装/重复/晚绑定/staging 拒绝、失败准备也锁定、author-only、超限不截断，以及受控 HTTP→实际 Provider 输入与 Audit 字节一致。真实模型与真人可用性在后继记录，本 ADR 不预宣称通过。
