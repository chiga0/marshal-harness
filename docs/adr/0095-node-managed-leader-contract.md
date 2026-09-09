# ADR 0095：Node 受管 Leader 的最小机器合同

- 状态：Accepted（2026-09-09）。维护者独立审查源 `66e98c1147887dbc8c692686806b5ce97ae40591` 后接纳；ADR0094 行为目标与本机器合同据此进入实施，不代表实现、验收或业务发布授权。
- 文档工作树基线：`f27784ddea738c3d095ad11c184d60fbf67ad162`；生产接缝最初核对 Worker 取消候选 `acbcfee9960557215328590f7b97bc1af3884d43`，其聚合修复 `070086e991b65b81f1765ec78ec1a9772fc01102` 已进入当前 main `3f359fd0fd88987b8ea2f3d3fbb36058834b003b`。这是追溯补充，不重新变基，也不把 Leader 设计记为已有实现。
- 依据：[ADR0094](0094-trusted-single-user-role-team.md)、[Leader 行为设计](../node-leader-execution-design.md)。唯一实施字段、边界和验收清单见 [机器合同](../node-leader-execution-contract.md)。本稿不修改现有导航、代码或 OpenAPI。

## 1. 决定

采用一个显式启用的 `task-managed-leader/v1` 内部执行 profile，使用新空根格式 `marshal-node-task-sqlite/v7-managed-leader`、service layout 7。已有 v1–v6 根、终态、回执及默认启动选择不变，不自动迁移或追认。新 reader 必须按根格式选择 reducer；旧 reader 在 owner claim 前拒绝 v7。新格式的必要性是阶段验收不再直接整体结束、反复 Leader 决定/动作及外部效果有恢复义务，而不是为角色名称换一个版本。

仍用唯一 Application/SQLite、原短事务、Depot、预算/Attempt、outbox、Provider、guard/custody 和受控执行。Leader 的内部 `executionType` 不增加公开 role；Supervisor observer 只聚合观测，不持有可变命令端口或执行 handle；Core 决定许可/调度/硬规则，Execution coordinator 持有原 handle 并承担 start/stop/collect。现混合 loop 只作为兼容组合入口逐步委托，不要求四个服务或全仓重命名。必需结果直接回 Core。普通可恢复失败封闭受影响后继并唤起 Leader，不全队无差别取消；未知清理/权限和硬期限不降级成业务重试。

六类闭集建议为 `ask`、`plan`、`work`、`repair`、`deliver`、`conclude`，没有通用 shell、URL、SQL 或状态 PATCH。调用原预算内单 Task 串行，业务事件合并；相关语义快照决定结果是否仍适用，不因无关 heartbeat 废弃。Task 和全局原并发内保留一个决策席位，不免费、不提额；代表双作者流程在任何付费前校验至少能容纳两个作者和 Leader。机器合同中的 17 Attempts/9 次调用仅是一次修正及相关故障验收场景的预算配置，不是必须消费的固定流程；既定 DAG 后继、合并通知和精确授权动作继续不额外索要 Leader 批准，不制造返工或空调用凑次数。

## 2. 证据与事务

Leader 输出只是建议。受信父进程绑定原输入/执行/cleanup；独立 Review 由另一受管执行产生、绑定精确选果和原始报告，不能由作者或 Leader 自签。Review 可成为自治修正来源，但不能伪造 ADR0091 的客观内容拒收，不能代替独立最终验收。自治修正只在已批准同计划、指定可修节点和轮数内；原 HTTP `task.repair` 的显式用户、负 Decision 和回执不变。

原义务、Leader Attempt/输入、接纳决定、有限动作及各动作结果均在同库。接纳决定、消费原义务、建立动作/outbox 同事务；I/O 在锁外，结果再按当前 owner、取消、期限、相关证据接纳。已提交决定恢复原动作，不重新规划整队；未提交调用只在原清理/原剩余预算内有界重试。外部效果 unknown 先查询，不凭重放 key 宣称跨系统 exactly-once。

新 profile 的 verifier 通过仅形成阶段验收。要求的独立 Review、精确交付、发布后验和 Leader 总结全部满足，且无未结成功义务，Core 才提交整体 `completed`。失败后已发生的发布不消失；取消不能撤销外部效果。旧 `completed` 不复活。

## 3. 最小公开面，不建立第三套版本管理

保持现有 `/v1` Task、Plan、Worker、Operation、Audit 字段和角色枚举；公开 Leader 执行仍用兼容的 `planner` 类别，新只读投影明确它是受管 Leader，而非一次 Planner 已实现全流程。新 profile 使用现有 `running/delivery` 等状态表达尚未整体完成，冻结的原计划 acceptance 明示阶段验收/自治范围/交付条款。

仅增加 Task 下的 `GET /v1/tasks/{taskId}/leader` 和 `POST /v1/tasks/{taskId}/leader/requests/{requestId}/reply`，不开放执行任意 Leader action 的 HTTP 入口。新回复是“答复已耐久接纳”，不是给已退出 Leader 冒造 Worker ACK；旧 questions/answer 及其 202 回执原样保留。新字段只出现在新响应，旧严格客户端可继续原读写但不具备新交互能力；不用全局 header 握手，也不要求所有旧 v1 请求识别新 profile。新根仍需显式配置和完整受支持客户端验收，不能以旧客户端不会报错推导其已能操作全程 Leader。

## 4. 首个业务发布边界

只实现受信本机 JSON 报告目标：操作者在配置中指定固定私有目录、固定只读 loopback 消费入口和允许的报告策略；Task 只引用目标 ID，不能传路径、URL、凭据或命令。一次动作仅新增一个已验收报告，原名字/摘要存在则查询比较，绝不覆盖、删除或更新“latest”。明确授权绑定 Task/计划/原成果/独立证据/目标/期限。

Publication Port 复用原受管 start/stop/cleanup，提供 create-if-absent、lookup 和独立 postverify。执行后丢回执先查原目标；匹配只是证明当前所需 bytes 已可观察，不凭它声称本进程创建。冲突或无法查询保持非成功；不可证明无执行不得盲重发。原进程清理和业务效果是两类事实，不相互替代。

这是业务测试服务，不是网站 UI、第三方部署平台或 Marshal 软件发行。默认不发布，高风险/超范围在付费或副作用前明确确认或拒绝；本版不实现任意动态拓扑、生产数据写入/删除、补偿或增加原预算的功能。相同计划的真实意见修正、一次精确授权报告和后验必须整链可用，不能以“不支持任意流程”为由只交付 Planner 组件。

## 5. 精确变更与不变范围

本稿仅在新 profile 内具体化 ADR0094 对 ADR0085 §6、ADR0088 §2 和 ADR0091 §1/§3 的职责、阶段结束及自治来源调整；以机器合同规定的 v7 和新增子资源实施。旧格式、旧 Go/hardened、旧签名/发行权限均不改。ADR0089 的原 custody 证明、ADR0092 的真实 factory/prepare 资格和 metadata-only 限制、ADR0093 的目标 Worker 取消及预算规则继续适用，不能因开启 Leader 追认 never-permitted 或抹去 unknown。

当前两文档不造成运行时 breaking change；实施 v7 是明确的新持久化支持面，不提供升级迁移。新增子资源是增量 API；旧响应字节、CAS、幂等键与错误语义须回归。若实现需要再给旧响应增加未知字段/枚举、修改旧终态或回执，超出本合同，必须先重新审查兼容差量，不能临时加万能 profile header。

## 6. 实施与接纳条件

按 [机器合同实施顺序](../node-leader-execution-contract.md#10-完整实施顺序与六类验收) 聚合一个纵切：格式/事务与真实 Core 接线→原执行上的 Leader/Review/同计划修正→有限发布/后验→HTTP、恢复、兼容及原 Provider 完整验收。生产作者不可为自身实现提供唯一权威 review。

未满足六类组合验收和真实代表性业务闭环，状态保持 `DESIGN` 或实际组件层级；B1 的原本机 PoC 通过、API-STABLE 旧范围、B3 软件发行门禁不因此改变。不得人为污染作者文件、伪造业务拒收或无限模型重跑凑修正证据。
