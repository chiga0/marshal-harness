# 整体架构

> 当前目标设计。完整方案见[Task-first 服务架构](agent-team-service-architecture.md)，合同为已接受的 [ADR 0085](adr/0085-agent-team-service-contract-and-storage.md)与 [ADR 0088](adr/0088-node-task-service-production-projection.md)，实际完成状态见 [Roadmap](roadmap-status.md#业务交付当前表)。合同接受不等于生产完成，旧合同适用性见[对照表](design-contract-map.md)。

## 产品目标与最少概念

Marshal 是单节点、单用户、可信任务的 Agent Team HTTP 服务：需求与上下文→必要方案确认→有界执行→集成与独立验收→可下载成果。用户可查看 DAG/Worker 进展、回答问题、取消并审计。[ADR0094](adr/0094-trusted-single-user-role-team.md) 接受当前 `trusted-single-user` 目标：[受管 Leader](node-leader-execution-design.md) 全程按业务义务唤起、读 durable 上下文并输出有限行动；Supervisor 仅观察/聚合/通知，Core 授权/预算/已批准调度/硬规则，Execution 原 handle 操作。现混合实现需渐进委托，不是四服务或第二状态机。B2-L 仍 DESIGN，包含集中 Review、局部修正、授权交付/后验和延迟整体结束；当前 Planner/下载不能冒充。现协议和格式不因文档改变，新机制须显式兼容，旧 completed 不复活。

用户只需理解 Task、Worker、Artifact。删除 Workspace 产品实体、ID、注册/切换及管理 API，不改名为 Project；数据目录是服务内部配置，Git/表/平台作为 Task 上下文，不进入通用资源目录。计划、问题、执行尝试和异步回执是任务的子记录，不要求用户先创建一串平台对象。

目标启动入口是 `marshal serve`；当前 Node profile 由固定 Node 执行脚本入口、组合唯一 Application/SQLite，不启动旧 Go server 或子 CLI。默认本地数据目录/访问 token 自动准备，无用户注册、安装身份收据或单独 init 向导；简启动的实际进度以 Roadmap 为准。服务依然受 OS/企业安全机制管理，不能借 server 绕过匿名二进制拦截。

<a id="v10-物理投影"></a>
<a id="逻辑职责不等于物理服务"></a>

## 三面分离，初期一个部署单元

| 面 | 职责 | 边界 |
| --- | --- | --- |
| 控制面 | Task/计划接纳、预算、内置 Supervisor、结果接纳、Decision/Outcome | LLM 只提议内容/计划/语义意见，不改权威事实 |
| 执行面 | AgentAdapter、受管进程/deadline/cancel、SandboxProvider、独立验证、后续受控发布执行 | 作者不自证、不自授发布权；可信单用户的职责分离不声称 OS/凭据隔离 |
| 存储面 | 唯一 Store、事件/投影/幂等/预算/命令、内容寻址制品和审计 | 不以缓存/日志/第二数据库决定业务成功 |

一个 server、多个受管执行进程、一个选定 Store 与本地制品；不先拆调度/GC/数据库微服务。当前 Node profile 从独立新根使用唯一 SQLite，旧 Go profile 不迁移或双写。Core 只依赖接口/领域类型，组合根用普通构造函数 DI 注入 Agent/执行/存储实现；Port 是 interface 契约，不是网络端口或独立生命周期。

每个 profile 内公开 Task 只有一套 ID/revision/预算/事件；旧 Go profile 复用 Goal，Node profile 不为沿用旧类型名再建立平行权威。HTTP 与启动入口使用同一 Application，不从 legacy server/child CLI 回落。旧行为合同和回归案例可复用，但没有实际消费验收不能记为完成。

## 最短交付链

HTTP 提交并确认有限计划→两个 scope 不冲突的真实作者→自动 Collect/独立验收→精确集成候选→下载并在独立目录运行业务验收→成功 Outcome。需要语义判断才增加有界 Reviewer，不能用另一个模型的赞同代替独立测试；Worker 不能为自己签发权威证据。

首个样例使用一个已可用 Provider 的两实例，不以三品牌、跨仓库或自动规划平台为前置。B2 才完整接入简短需求的持久问答、零 Git 与多仓库业务。紧耦合任务可以单 Worker，不强迫拆分。

## 保留的最小正确性

- 批准输入、执行身份、计划、命令与失败/结果从 B1 持久化，公开状态只读。
- 受管目录单写，Git 特化锁 base/worktree；停止未知不能复用或派替身。
- 重复请求幂等，结果按当前执行与证据重验，Worker 退出不等于验收/交付成功。
- 本地 token/Host/Origin 保护；账号、组织、RBAC 后置，首版本地 profile 不开放公网。
- deadline、Attempt/并发/输出有限；重试和局部返工不刷新总预算；未知外部效果不盲重跑。
- 作者与发布职责/Core 授权分离；原生登录/Skill 不等于权限扩大，默认只交付成果。ADR0094 将强 OS/凭据隔离证明转后继加固，不改变旧 Go/hardened 合同，也不改旧实机 non-production 声明。
- B1 正常重启能查事实且不乱重派；恢复失败停止接单、报告原因并保留原受支持只读诊断，不承诺该时在线 HTTP。B2 同版本恢复，B3 完整故障/正式支持。

## 唯一实施顺序

**B1 真实团队 PoC → B2 本地 API 可用 → B3 正式可靠发布。** 旧 B1 单任务成为内部步骤，不改历史状态和失败分母。B2 的核心 API-STABLE 后才启动 UI-1，UI 不阻 API 正式发布；U1 旧历史迁移不阻新任务。Pi/Qwen Code/OpenCode 逐个按实测声明支持，不以全部增强能力拖住已支持路径。

PostgreSQL、HA、多租户/远端身份平台、统一 Skill、动态任意角色/DAG、通用 Workflow 平台与任意高风险自动发布都不作为初版前置。当前只增加一个有界授权业务发布闭环，成功流程重复后再固化 Workflow Template。Node 的 SQLite 已有实现，不等于完整恢复验收。Marshal 软件发行与产品业务发布分开：签名/公证按 ADR0088 的资产类别适用，Linux 与受保护 same-bytes stable gate 保留在 B3。详细实现与验收只维护在[Milestone](agent-team-service-milestones.md)。

[历史整体架构](architecture-reference-2026-09-07.md)保留原证据和长期设计参考，不构成第二套当前待办。旧 Marshal skill 完全退出运行/研发/验收，不加载或执行。
