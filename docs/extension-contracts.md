# 控制、执行、业务与存储扩展契约

更新：2026-09-15。本文把已接受架构中的跨边界义务对应到当前Node接缝。它不是动态插件SDK，不新增可import接口、HTTP字段、持久格式或权限。公共请求见[标准API](standard-api.md)，部署可用性见[支持矩阵](api-support.md)。第三方扩展是部署者审核并固定的可信代码；对象字段相似不等于通过Core私有能力、工厂身份及配置摘要准入。

## 目标Port的共同语义Schema

本节是适配实现必须满足的**目标行为规范**。名称用于精确描述输入输出的共同语义，不是当前JavaScript导出、HTTP/ACP字段或新持久格式；具体Adapter负责与现有ticket/receipt/Store字段映射。符合下列形状不自动获得私有能力身份或调用许可。本文没有授予新的外部写权限，也不要求把这些对象原样加入SQLite。

所有会产生执行或效果的请求绑定以下内容；不可用项不得用猜测、空字符串或新随机身份代替。仅某Port明确不适用的项可为null，并须在能力配置中解释：

| 语义对象 | 必需内容与约束 |
| --- | --- |
| Binding | authorityId（当前映射storeId）、taskId、commandId、attemptRef、executionId、ownerGeneration、inputDigest、configurationDigest、deadline；`attemptRef`必须引用可验证的持久事实（当前Node由`workerId`、`commandId`、`generation`、`reservationDigest`及`worker.reserved`事件引用组成），不能凭空生成`attemptId`；authorityId标识唯一权威服务根，不能由目录名或相同配置摘要推断；全部绑定原动作，generation用于拒绝过期owner接纳结果，executionId用于关联本次执行请求及其实际所属执行，不是宿主PID；若环境在启动后分配实际ID，Adapter必须保存与原请求身份的一一映射，启动前不能伪造已启动事实 |
| WorkBinding | 在Binding外包含planDigest、candidateDigest（还没有候选时null）、authorizationDigest（不需额外授权时null）；具体角色所需内容不能null |
| FrozenInput | 原需求与已确认约定、输入manifest、必要上游候选、问答引用及其摘要；bytes或读取能力来自可信准备，不能用模型自述“已读”代替 |
| Manifest | artifact身份、task归属、mediaType、bytes长度、contentDigest与可读取内容引用；位置不是授权，下载后按长度/摘要复验 |
| Capability | 能力实现/configuration身份、支持的输入/输出及上限、取消方式、cleanup保证、恢复查询范围、权限执行点和隔离范围；可选问答/usage/进度逐项声明 |
| Fault | 封闭原因类别：invalid-input、unsupported-capability、unauthorized、conflict、expired、cancelled、execution-failed、evidence-invalid、unavailable；另附effectKnowledge=none/known/unknown与绑定证据，不能只给retryable布尔值 |
| ExecutionObservation | 原Binding、实现来源、实际观察时刻、启动/退出事实、cleanup状态及其依据；Agent生成文本不能自签这些运行事实 |

Binding的deadline是原绝对截止时间，不因重连、重放、Review刷新。终止后只读恢复lookup可使用单独有界观察窗口，但该窗口不能延长原授权或重新许可执行。摘要使用冻结配置声明的规范编码和算法；不能把任意JSON序列化摘要与原Core摘要混用。authorizationDigest非空时还须获取并核对其原正文、目标、范围和有效期；摘要本身不授予权限。观察到旧generation执行仍可保存为历史/恢复依据，不能绕过当前owner原事务接纳。

每项Port操作的合法闭集为该操作表列出的业务变体，加上默认适用的共同失败包络；表中重复列出包络仅用于强调，不限制其他操作使用。共同失败包络为`rejected{binding,fault}`或`unknown{binding,fault,lastObservation,evidenceRefs}`；rejected表示本次调用未获新准入或已知失败，不证明历史执行没有副作用。unknown禁止盲目重发。每个非rejected执行结果必须保留原Binding和证据引用，错误不能返回假成功的空manifest。表内failed必须包含Fault；cancelled携带原取消观察和效果知识，不能冒充业务断言失败。

## 目标Agent与环境Port

AgentPort负责协议会话和语义输出；EnvironmentPort负责工作负载、资源、工具/数据访问的实际执行边界。两者分开，即使同一个本地Adapter同时实现。AgentPort没有Store写权限或任意进程接管权，EnvironmentPort不判断业务正确性。

| 操作 | 必需输入 | 闭集输出 | 副作用、重放与后置条件 |
| --- | --- | --- | --- |
| Environment.prepare | Binding、批准目录/资源范围、输入Manifest、Capability要求 | prepared{binding,environmentRef,inputManifestDigest,capabilities} / rejected / unknown | 只准备本次获准独立资源；不运行模型/外部发布。同command同输入返回原准备身份，异输入conflict；未知归属资源不复用 |
| Environment.start | 原prepared、Binding、受信执行规格、已批准权限/凭据引用 | accepted{binding,handleRef} / rejected / unknown | 许可先耐久，再启动原工作负载；accepted不是实际已启动。同原请求不能产生替身；跨代先恢复查询，不从deadline/网络失败猜未启动 |
| Environment.observe | 原executionId及归属证明、查询期限 | not-started / active / exited / unknown，各含ExecutionObservation | 只读；not-started必须有环境权威证明，不能从查无缓存推导。exited仍携带cleanup=confirmed/unconfirmed，退出码0不代表任务成功 |
| Environment.stop | 原Binding/handleRef、耐久停止意图引用 | stop-accepted / stopped / unknown | 只停止原所属工作负载；可重复受理但不能按新PID扩展作用域。stopped须证明声明范围的活动已停止且cleanup可确认，范围外外部效果另外对账 |
| Environment.release | 原环境身份、cleanup证据及未决写者检查 | released / rejected / unknown | 只有已确认无原活跃写者才允许释放/复用；不清除未知锁或抹去失败资产。重复释放同资源无新增删除效果 |
| Agent.begin | 原WorkBinding、prepared环境、FrozenInput、受限工具/权限回调 | accepted{binding,sessionRef,handleRef} / rejected / unknown | 仅在原环境许可中调用Agent；sessionRef不可充当环境停止证明。同command重复调用不得启动第二模型会话 |
| Agent.observe | 原sessionRef/executionId、cursor及有界查询 | running{progress,nextCursor} / completed{outputManifest,stopReason,observation} / failed{fault,observation} / cancelled{observation} / unknown | 只读观察和接收原输出；completed只表达回合结束，候选/cleanup/业务通过由其他Port核验。进度丢失不丢权威完成事实 |
| Agent.cancel | 原Binding/sessionRef、原停止意图 | cancel-accepted / cancelled / unknown | 请求协议停止不等于环境已清理；最终仍需Environment停止证明。超时后调用者继续查原执行，不能立即释放 |
| Agent.answer（可选） | 原问题身份/摘要、原答案回执引用、answerDigest和有效期 | accepted / acknowledged / expired / unknown | 仅向原会话投递原答案；同摘要重放不能重复消费，异摘要conflict。accepted与ACK分开；业务答案不提升工具权限 |

远程传输须认证执行所属服务，并让Environment.observe/stop的证据来自可查询的工作负载控制源，而非被执行Agent自报。Adapter可用HTTP、SDK、ACP或其他协议，但必须公开每个返回值能证明什么。没有原会话resume能力可以拒绝恢复会话，仍应提供原环境清理和效果查询；恢复不能偷偷从头跑一遍任务。

## 目标业务、判断与验收Port

所有语义角色都消费原WorkBinding和所需FrozenInput，并使用原Environment/Agent生命周期，不是无预算的旁路调用。候选摘要绑定实际manifest及bytes，独立性由配置与原执行身份验证，不由role字符串声明。

| Port操作 | 必需输入 | 闭集输出与接纳条件 | 副作用及重放规则 |
| --- | --- | --- | --- |
| Business.prepare | 原计划/输入/上游Manifest、写入范围、WorkBinding | prepared{environmentRef,inputManifestDigest,layoutDigest} / rejected / unknown | 只准备批准内容；各写目录单写者，Git绑定base。不默认执行外部业务写 |
| Business.collect | 原ExecutionObservation、批准layout、候选读取能力 | collected{candidateManifest,evidenceRefs} / rejected / unknown | 只采集实际允许成果，清理未知不接纳；同精确候选重复读取不新造业务完成 |
| Leader.decide | 原语义快照、需求/候选/问题/义务引用及inputDigest、批准policy、WorkBinding | proposed{summary,actions} / failed / cancelled / unknown | actions仅ask/plan/work/repair/deliver/conclude；建议本身无副作用。Core验证当前性后同事务消费原义务/保存决定/建动作；已提交决定恢复原动作，不再次问模型生成替代决定 |
| Review.evaluate | 原需求/验收约定、完整候选Manifest、当前选果摘要、独立执行身份、WorkBinding | reviewed{verdict:accept/rework/reject,findings,evidenceRefs} / failed / cancelled / unknown | 不修改候选/标准、不发布；Core只接纳精确当前候选对应意见。rework表示建议在原自治范围修正，reject表示拒收，不能折叠二者；语义意见不覆盖客观验收，也不能伪造旧repair负Decision |
| Verification.bind | 原需求/计划/输入与支持的业务验收能力 | bound{policyDigest,layoutDigest,assertionDefinitions,deliveryDefinitions} / rejected | 同步可信检查可验证范围，执行前冻结；不得把作者后来生成的任意代码变成可信checker |
| Verification.evaluate | 原WorkBinding、精确候选、已冻结断言与独立执行身份 | verified{verdict:pass或content-reject,assertionResults,evidenceManifest,deliveryManifest或null} / failed / cancelled / unknown | 只执行获准验证；每项必需断言完整、原nonce/摘要/cleanup可查。结构失败用failed，不能用content-reject申请重试；只有通过才产生正式交付候选 |

表中proposed/reviewed/verified均为Port结果，不是Task终态或发布权限。当前Core仍通过私有receipt验证来源与ticket，不接纳第三方构造的同形JSON。模型身份回显不证明理解；Review必须看到完整验收输入，不能只验证作者摘录。重复完成结果在Core按原动作/证据接纳，不能重复消费预算或重触发交付。

## 目标Publication与后验Port

Publication输入必须包含WorkBinding、目标身份、操作类型/范围、原授权正文与摘要、精确候选Manifest、外部操作关联身份和已冻结postverify期望。外部操作关联身份可以是原动作ID的可核验映射；若平台只能执行后返回jobId，须先有可对账的原请求关联，且将返回jobId与原动作耐久关联。不能因没有平台幂等键而宣称不存在重复风险。

| 操作 | 闭集输出 | 副作用与后置条件 |
| --- | --- | --- |
| Publication.execute | accepted{operationRef} / observed{effect:created或matched,receiptManifest} / rejected / unknown | 仅原精确授权目标/效果；先耐久意图，原执行由Environment托管。accepted仅受理；observed须有绑定的目标事实，不自授Task成功 |
| Publication.lookup | absent / matched / conflict / unknown，均含原操作绑定、观察来源和证据 | 有界只读，不执行写修复。matched证明当前结果满足原绑定；absent不单独证明旧执行未发生或可重发 |
| Publication.cancel（能力可选） | cancel-accepted / stopped{effectObservation} / unsupported / unknown | 只取消原远程作业，不隐含回滚。未支持则如实保留外部效果义务，不能把Agent停止称为业务停止 |
| Postverify.evaluate | passed{assertionResults,evidenceManifest} / failed{assertionResults,evidenceManifest} / unknown | 独立读取真实目标，按原业务期望而非Publisher的pass核验；仅实际完成的检查才能返回业务failed。未完成、取消或无法读取走共同rejected/unknown及Fault，不伪填断言结果。失败保留已发布效果，超时不倒写“未发布” |

重放规则是同动作、同授权、同候选、同目标，不以新key替代原未知操作。仅在Core证明原许可/清理和平台契约允许安全再次执行时继续；否则人工介入或明确非成功。新平台适配必须冻结目标操作及去重/查询/后验映射；本目标契约不自动支持删除、覆盖、生产SQL、补偿或回滚。

## 目标Store与Depot Port

| 操作 | 必需输入与闭集输出 | 原子性、错误与恢复 |
| --- | --- | --- |
| Store.open/claim | 精确格式/配置身份、原数据根身份、预期generation→opened/claimed或rejected/unknown | 单写者物理互斥加owner fence；未知格式/坏库不得初始化成新库，持锁旧owner不得因租约到期被夺写 |
| Store.read | 当前owner、查询/分页边界→原记录或absent/rejected | 无外部副作用；absent只描述该查询，不证明外部操作没有发生 |
| Store.commit | 当前owner、expected revisions/head、事件/投影/回执/outbox变更→committed{source,receipt} / conflict / rejected / unknown | 一次原子提交；同key/body返回原回执，异body冲突。COMMIT观察丢失先读原回执，不能再造预算/动作；I/O和模型不在事务中 |
| Depot.put | 原字节、长度/摘要、受信归属→stored{manifestRef} / rejected / unknown | bytes耐久后才提交Core引用；同内容可复用bytes但不混淆Task授权。失败保留未知，不为引用补造文件 |
| Depot.get | 原manifest与访问身份→bytes / absent / invalid / unavailable | 重验归属/摘要/长度；ready引用缺失或损坏不是空成功；恢复只使用可核验原资产 |

替换后端仍必须满足原事件、CAS、幂等回执、预算/outbox同事务和制品先耐久的关系。不能双写两套权威，不能从另一backend“尽量读取”后接纳成功。该语义规范不提供原库原位迁移或热插拔承诺；改变事务/格式/owner合同须先新增ADR。

## 当前实现映射与具体限制

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
