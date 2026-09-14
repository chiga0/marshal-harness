# 控制、执行、业务与存储扩展契约

更新：2026-09-14。本文把已接受架构中的跨边界义务对应到当前Node接缝。它不是动态插件SDK，不新增可import接口、HTTP字段、持久格式或权限。公共请求见[标准API](standard-api.md)，部署可用性见[支持矩阵](api-support.md)。第三方扩展是部署者审核并固定的可信代码；对象字段相似不等于通过Core私有能力、工厂身份及配置摘要准入。

## 三面与契约归属

控制面保持唯一Application/Core。Leader提出有限业务建议；Core检查原需求、批准、预算、事实和当前性，提交动作义务；Execution执行所属handle；Supervisor只观察、聚合、通知。存储面保存原事实、回执、outbox和制品。执行成功不是业务成功，观察回调不是唯一结果通道，存储成功也不能决定验收通过。

| 边界 | 当前实现入口 | 输入、输出与权限归属 |
| --- | --- | --- |
| 客户端→应用 | [HTTP handler](../packages/task-api/http-handler.mjs)、[Service dispatch](../packages/task-service/composition.mjs) | 校验后的operation/路由ID/key/body/page及独立请求context→原投影或回执；Core独占业务准入 |
| Core→Leader/Review | [createLeaderPort/createReviewPort](../packages/task-application/leader-ports.mjs) | 冻结ticket/input及准备上下文→受管执行与有限建议/独立意见；只有私有绑定receipt可进入原接纳路径 |
| Core→Execution | [TaskExecutionCoordinator](../packages/task-execution/controller.mjs) | 原outbox/ticket/许可→prepare/start/collect/release及原结果；不建立第二预算/调度真值 |
| Execution→Agent | [ACP Provider](../packages/agent-provider-acp/index.mjs)、[Pi Provider](../packages/agent-provider-pi/index.mjs) | 原cwd/prompt/deadline、许可回调与执行上下文→started/completion/stop及可选观察 |
| Execution→环境 | [agent-runtime](../packages/agent-runtime/README.md) | 受信可执行文件/环境/期限→所属运行身份、退出和cleanup；当前本机guard，不能声称恶意沙箱 |
| Execution→业务 | [createFileBusiness](../packages/task-business/index.mjs) | 冻结输入、上游manifest、布局与原执行→准备目录和候选字节；不自签通过 |
| Core→验证 | [createVerificationPort](../packages/task-application/verification.mjs)、[独立命令](../packages/task-verification-command/README.md) | 原需求/计划/精确候选/政策→独立断言、证据、交付bytes；Core签发权威接纳 |
| Core→交付/后验 | [本机报告端口](../packages/task-publication-report/README.md) | 已验收成果、原目标与精确授权→执行回执/lookup/独立实际结果 |
| Core→Store/Depot | [SQLite Store](../packages/task-store/README.md)、[制品实现](../packages/task-application/artifacts.mjs) | owner绑定短事务/原bytes→原子事件、projection、receipt、outbox及不可混淆制品引用 |

## Agent与执行环境

当前ACP的实际调用形状为：

```js
const handle = provider.start({cwd, deadline, prompt, onProgress, onPermission});
await handle.started;
const result = await handle.completion;
// 取消使用原handle；不能用数据库PID替代。
await handle.stop();
```

可信执行组合还传递自身支持的executionContext。Provider结果包含providerId、status、stopReason、reason、sessionId、outputText、usage、cleanup；具体字段由该Provider合同定义。completed/end_turn只表示Agent回合完成，必须确认原cleanup、收集实际候选并独立验收。stop必须幂等指向原completion；启动前取消也应最终收口原启动，不伪报已停止。unknown不能转换为failed后自动释放目录。stdout、进度、权限请求和业务结果分开，秘密不得进入公开观察。

下面是**目标行为记法，不是当前SDK函数名或远程wire定义**：

```text
准备(原任务输入、已批准范围、环境能力) → 本次独立执行环境
启动(原执行身份、原期限、准备结果) → 所属执行句柄
观察(原句柄或经验证执行ID) → 有界进度/终态/证据来源
停止(原身份、停止意图) → 停止受理及最终清理证明
收集(原身份、精确候选约定) → manifest与可复验bytes
恢复查询(原身份、原操作绑定) → 已知事实或明确unknown
```

本地CLI、远程HTTP、SDK或ACP都可以实现这些义务，传输方式不改变权限。环境与Agent协议是两个接缝：Agent解析/输出不会自动提供环境隔离；环境可停止工作负载，也不一定能撤销它已发出的外部作业。当前没有完整远程执行环境实现或通用远程握手协议。实现远程Adapter必须给出执行ID归属、认证、重连/取消和证据可信来源；远端Agent自报receipt不能代替独立可查询的执行事实。

## 能力与部署准入

准入依据本任务所需能力与部署实际保证，不用品牌、CLI/远程标签或版本目录名推断。至少逐项声明并验证：输入/制品边界、所用协议、原身份关联、终态观察、停止/cleanup、权限通道、外部效果查询、可用的恢复范围。运行问答、tool events、usage、steering、会话resume都是可选能力；缺失时不得伪造值或让用户误以为能操作。

当前实现通过受信工厂、私有Port登记、冻结配置与profile校验落实准入，Provider列表只是只读事实投影；没有公共capability注册或万能插件加载API。新增实现必须接入原装配和私有接纳机制，不能仅返回相同JSON就成为验证器。custody资格还依赖精确runtime/prepare来源，任意ACP可执行入口默认不取得跨代恢复资格。缺必要能力应在启动模型或外部副作用前拒绝；停止证明不足时保留intervention，不通过换Agent绕过。

Task可以请求更低预算，不能扩大部署上限。Skill是能力说明与Agent上下文，不是执行授权；凭据由原Agent/受信部署管理。工具回调白名单不等于封住原生shell/MCP旁路。当前trusted-single-user接受同UID ambient风险，权限职责与强OS隔离分别声明。额外隔离实现必须独立验证，不能从现有普通子进程合同推导。

## Leader、Review与业务验证

createLeaderPort配置提供id/providerId/policy/prepare/parseDecision；createReviewPort提供parseReport。prepare读取原ticket.input中冻结内容并返回有界prompt；start复用指定Provider，只有原end_turn、cleanup与严格解析全部满足后，才形成绑定该ticket的私有receipt。Leader六类动作仍为ask/plan/work/repair/deliver/conclude，没有任意命令或状态PATCH。Review读取原需求与完整候选、形成独立意见，作者与Leader不能为自己的成果生成权威通过。

ADR0101只为显式新通用文件配置接受模型建议wire→原LeaderDecision的受信映射：运输身份来自原ticket，summary/actions来自严格模型输出；映射不能修正业务摘要、补造动作或绕过Core当前性检查。旧配置保持旧协议，新配置/新空根/政策身份独立；该决定不改变HTTP，不意味着所有原始模型输出已耐久保存。实现与发行进度以Roadmap为准。

业务prepare把原需求、批准布局、输入和上游摘要实际交给相关Worker；collect在原执行清理后检查允许文件并采集bytes，release处理自身资源。Git写任务使用锁定base及独立worktree，非Git任务使用独立目录；同目录最多一名写入者。业务代码不能藏外部写或自行改变Store。

验证配置的bindPlan接收taskInput/proposal/inputArtifacts，返回nodeId/description/layouts/deliveries，把可验证范围绑定进批准计划。start接收ticket/prepared/executionContext，返回started/completion/stop；原raw结果由私有VerificationPort绑定接纳。独立命令以原nonce、reservation/plan/input/checker/policy摘要和完整断言验证输出，不能只认passed:true。结构/身份/输出错误与真实内容拒收分开，只有原政策允许的客观失败才能启用相应修正。通用文件默认不执行作者代码，跨业务族增加验收需要明确受信适配，不能由HTTP上传checker。

## 授权外部操作、对账和后验

适配义务沿ADR0095/0100：每次外部写在副作用前绑定原Task、计划、已验收成果、独立证据、固定目标、有效期及用户精确授权；先持久化意图与动作，再由原Execution启动。内部目标由可信配置选择，Task只引用允许身份，不能提供任意shell、URL或凭据。下面定义完整行为义务，不把任意ETL目标纳入已有权限：

1. 准备时验证目标/操作范围和独立后验能力；后验期望来自原业务需求/答案，执行前冻结，不从Leader的pass生成。
2. start返回原started/completion/stop，执行输出携带原操作身份、目标、精确成果绑定和实际效果证据。接纳回执不等于业务完成。
3. lookup只能按原身份有界只读对账，区分absent、matched、conflict、unknown；matched证明观察到所需结果，不必然证明本次执行创建。
4. 超时/丢回执先lookup，不能换幂等键重发。absent本身不足以证明安全重发，还需原执行确无副作用及Core恢复条件。不能查证则保留unknown，不声称跨系统exactly-once。
5. postverify是独立执行，读取真实目标并检查业务断言与精确绑定。停止本地Agent不代表远端作业已停；取消或后验失败不抹去已发生效果，不暗自补偿、删除或回滚。

当前本机报告实现提供start/lookup/postverify.start，固定新建JSON文件和只读loopback结果验证；lookup为同步有界接口，远程实现不能直接返回Promise绕过它。网络异步对账需在执行层适配并审查超时/恢复契约，若改变现有持久或执行语义须新增ADR。真实ETL、云部署、生产补数的具体目标、操作语义、去重与结果oracle仍需单独冻结和验收；上面义务完整并不等于这些平台可用。

## Store、制品与兼容验证

Store.create/openExisting、claimOwner、read/write及Tx API见[实际存储合同](../packages/task-store/README.md)。read/write为短同步事务，事件、projection、幂等receipt、预算与outbox可原子提交；外部I/O在事务外。Agent和插件没有原始SQL或通用save(nextState)入口。制品bytes先耐久保存，再同库提交摘要引用；读取复验归属、长度及摘要，缺bytes不能补造成功。

替换存储后端的目标义务是保持原子性、单写者/owner fence、CAS、不可改写回执、顺序证据和未决动作恢复；它不表示当前提供可热插拔数据库插件。不得双写真值或失败时切换旧库，未知格式拒绝，旧根迁移必须显式合同。哈希链用于检测违反已知绑定的漂移，不单独证明同UID恶意攻击下不可篡改。

每个扩展交付至少验证一条成功闭环和相关负例：能力不足零执行；错误ticket/Provider/摘要拒收；重复请求无重复预算/副作用；取消与迟到结果竞争；启动/完成/提交窗口中断；缺制品/冲突/unknown不成功；独立业务反例被拒；旧配置和客户端兼容。记录源码、配置、运行平台、原资产及证据摘要，fixture、真实Agent、真实外部业务分别标记。涉及UI还需原三条独立验收，不用代码审查替代真人可用性。
