# 设计与合同适用性

本页回答“以什么定义目标、什么约束机器行为、什么证明已经可用”。不在这里复制当前发布进度；唯一事实入口是 [Roadmap 当前表](roadmap-status.md#业务交付当前表)。历史接受事实不因文档归档撤回，当前 Node 设计也不隐式继承已退役 Go 的运行能力。

## 权威分工

| 问题 | 权威入口 | 边界 |
| --- | --- | --- |
| 产品最终要解决什么 | [愿景与范围](vision-and-scope.md) | 用户、适用条件、长期目标与非目标 |
| 系统如何完整工作 | [服务架构](agent-team-service-architecture.md)、[生命周期](task-lifecycle.md) | 三面职责、需求到业务交付、失败和恢复；不是完成状态 |
| HTTP 的精确请求响应 | [OpenAPI](../packages/task-api/openapi.json)、[标准 API](standard-api.md) | OpenAPI 是唯一机器 Schema；说明文档不创造额外字段 |
| 内部 Leader 的机器语义 | [Leader 合同](node-leader-execution-contract.md) | 闭集 action、输入/决定绑定、事务、授权及恢复 |
| 能力如何扩展 | [扩展合同](extension-contracts.md) | 受信 DI 与接口义务，不等于动态插件或远端能力已经启用 |
| 谁能执行什么 | [安全模型](security-model.md)及相应 ADR | 职责、权限、事实权威和实际隔离分别判断 |
| 如何验收、什么完成 | [Milestone](agent-team-service-milestones.md)、[Roadmap](roadmap-status.md#业务交付当前表)、[接口支持矩阵](api-support.md) | 出口、版本/profile、实机证据、发行状态分别记录 |

设计接受、代码存在、配置启用、独立验证、实机业务完成和软件发行是不同结论。任何一项不能自动代替其余项；`API-STABLE` 只覆盖其证据明确列出的操作与 Schema，不代表终态全部扩展永远冻结。

## 当前 Node 决策链

| 决策 | 对现行设计的约束 | 与历史合同的关系 |
| --- | --- | --- |
| [ADR0080](adr/0080-three-plane-business-delivery-roadmap.md) | 三面分离，业务交付优先 | 不把通用复杂编排、HA或多租户作为受限团队前置 |
| [ADR0085](adr/0085-agent-team-service-contract-and-storage.md) | Task-first、API-first，无 Workspace/Project 注册；独立验证、交互及持久职责 | 产品合同保留；其 Go/RB1 物理投影由0088取代 |
| [ADR0086](adr/0086-task-preapproval-questions-and-preview-revisions.md) | 批准前问答、精确 preview/revision 与幂等 | 不把旧批准请求解释为运行中授权 |
| [ADR0088](adr/0088-node-task-service-production-projection.md) | Node 唯一 Application/SQLite、发行资产按类别处理 | 0087为实验；不运行或导入旧Go权威根 |
| [ADR0089](adr/0089-node-execution-custody-and-cleanup-recovery.md) | 所属执行托管、跨代清理证明及恢复准入 | 不将旧输出变成新代可接纳成果；后继Leader恢复按0095显式定义 |
| [ADR0090](adr/0090-node-runtime-business-questions.md) | 原Worker问答、答案投递与ACK、验收引用 | 业务回答不是工具授权或Leader发布确认 |
| [ADR0091](adr/0091-node-same-plan-local-repair.md) | 同计划内容拒收、影响闭包、保留无关成果和原预算 | 旧HTTP repair保留；内部Leader来源由0094/0095限定 |
| [ADR0094](adr/0094-trusted-single-user-role-team.md) | 可信单用户角色职责；Leader业务判断、Supervisor观察、Core硬规则、Execution操作handle | 精确调整0085/0088强凭据隔离前置；不撤销独立证据、默认拒绝发布、秘密保护 |
| [ADR0095](adr/0095-node-managed-leader-contract.md) | 显式v7、受管Leader闭集决定、独立Review、授权交付/后验及恢复 | 不重解释v1–v6、旧completed和回执；不另建状态机 |
| [ADR0096](adr/0096-node-stable-asset-signing-minisign.md) | Node发行资产签名及同字节证据 | 软件发行与Task业务发布不共用授权 |
| [ADR0099](adr/0099-go-legacy-line-retirement-and-removal.md) | Go线退役，代码及证据在git历史追溯 | 不要求main并存旧实现，也不删除旧ADR接受事实 |
| [ADR0100](adr/0100-generic-team-default-and-agent-entry.md) | 默认通用文件团队、受信可执行Agent入口、显式配置/启动器升级 | 专用业务样例不限制所有任务；普通文件验收不证明真实ETL完成 |
| [ADR0101](adr/0101-generic-leader-model-wire-binding.md) | 显式通用文件模型proposal由受信ticket mapper补齐控制封套 | 不默认切换旧配置，不改变Core持久化或HTTP，不把映射bytes称为原始模型输出 |
| [ADR0102](adr/0102-generic-review-wire-and-new-install-default.md) | 新安装的通用文件配置分别映射 Leader/Review 运输封套，保留原完整输入与独立验收 | 旧设置不静默切换；配置语义与当前安装路径分开；新配置不扩展外部执行权限 |
| [ADR0103](adr/0103-execution-observability.md) | 显式受信策略留存 Marshal 实际输入与 Provider 公开活动、模型、用量 | 观察不构成权威结果；缺失不估算，隐藏推理不留存；输入留存与 startProtocol 组合不支持，旧关闭配置保持原形状 |
| [ADR0104](adr/0104-bounded-leader-protocol-correction.md) | 显式配置下，Leader 至多一次严格 JSON 格式重提，原失败与后继执行分别持久化 | 原预算、期限、清理和当前性不放宽；不自动改写输出，不纠正业务、权限或动作拒绝；旧配置缺省关闭 |
| [ADR0105](adr/0105-fixed-author-instructions.md) | 新通用文件配置在原业务对象准备前一次性登记固定作者指导，原 Audit 与 Provider 使用同一输入 | 旧未登记提示逐字不变；不开放 staging 或权限；不依赖 Leader 转述，不保证模型服从或业务正确 |
| [ADR0106](adr/0106-bound-review-assessments.md) | 批准前业务验收目录、逐项来源与状态、原 receipt 绑定的评审证据 v2 | 旧 v1 可读但不补造覆盖；不新增 HTTP 写入口，不以文本评审冒充实际效果验收 |

其他精确接缝仍以对应 ADR 原文为准，不因本表未逐一列出而失效。继承旧机器语义的版本必须保留其行为与回执；提出替代不能靠重写说明文档完成。

## 需要保留的边界

- 每个服务数据根一个权威 owner；事件、投影、回执、预算和 outbox 在唯一应用事务中接纳。
- 每个执行目录一个写入者；Git写节点锁定base并使用独立worktree，非Git任务不伪造仓库或commit。
- 作者不提供自身唯一权威验收；ReviewDecision绑定精确输入、候选和证据，变化后重查适用性。
- 默认无业务发布授权；角色和原生登录不扩权，普通答案不替代精确目标确认。
- 未知执行或外部效果不盲重发、不虚释容量、不靠新Task清零预算；失败保留真实Outcome。
- 普通同UID进程不是恶意代码沙箱；现行可信单用户职责分离不宣称强凭据隔离。
- 旧根不隐式迁移、覆盖或重签；旧终态不复活，不支持格式在接管前拒绝。

这些不变量约束扩展实现，不能为了适配新Agent、简化客户端或通过演示而放宽。

## 文档与协议演进

变更信任边界、持久化契约、生命周期或发布权限，必须新增或替代 ADR，再同步相应机器合同、实现与兼容验收。仅澄清已接受决定、移动历史叙述、删除过期进度不构成新协议。OpenAPI变更要同时验证Schema、示例、客户端和请求回执兼容。

当前说明只写稳定目标与适用边界；版本状态进入Roadmap/支持矩阵，失败和签名证据保留原来源。历史参考显式标记时间，不在“当前基线”标题下混用旧Go状态或旧部署命令。归档前的本页见[历史参考](design-contract-map-reference-2026-09-14.md)。
