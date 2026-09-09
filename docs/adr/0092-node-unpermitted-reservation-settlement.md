# ADR 0092：Node 从未获执行许可的预留中断结算

- 状态：Accepted（2026-09-09；维护者依据持续发布授权，在独立反向审查及唯一P1聚合修正、同reviewer复核无剩余P0/P1后接纳实施。已审正文SHA-256=`a8b8958ce3bdbd965e6fe9258933278df4084f43064678f17eeb58e9f6bf2713`，指本状态更新前的完整草案；不表示运行时已实现或旧根可恢复）。
- 解决问题：B2 同版本恢复中，reservation 已提交而 custody 许可未提交的两个窗口会保留容量并封闭 ready；本决策只为可证明从未获准执行的新记录增加中断结算。
- 基线：`ce55eed6fc01ae7b533e73d8fefc1bd14754dfbd`；继承 [ADR 0088](0088-node-task-service-production-projection.md)、[ADR 0089](0089-node-execution-custody-and-cleanup-recovery.md)，不改变单节点、单用户、可信任务的正式发布目标。

## 1. 问题与精确取代范围

当前实际顺序为 `nextWork` 同库预留 Attempt/容量 → FileBusiness `prepare` → custody 准备 → `bindCustody` 同库提交原公钥/许可 → 原句柄 `permit` → Provider/Verifier 启动。服务在 reservation 提交后或绑定事务提交前死亡，原 Worker 仍 queued，原预算已消费，但数据库没有可验证的 custody 公钥。`TaskCleanup.inspectBeforeClaim/pending` 仅选择已有绑定，普通 `reconcile` 则把跨代活动义务标为 intervention。现有 HTTP cancel 不能重开该终态，重启或时间流逝也不构成停止证明。

不能对现有记录直接推导“没有许可所以未执行”：业务准备早于许可，`task-git-business` 的准备实际执行 Git 子进程，自定义 prepare、布局/审计回调也可能产生外部效果。目录为空、PID 不存在、Agent 没有上报 started、新配置自称安全都不是证据。

本决策仅对**新格式、原 reservation 已冻结完整启动协议且准备路径合格**的记录，补充 ADR 0089 §2/§4 的一个命名例外。已有许可仍必须走原公钥绑定的独立 cleanup observation；未知工具 scope、旧结果接纳、普通终态、原取消/问答/修正规则均不放宽。新例外不是 cleanup 观察，不补签原 custodian，不恢复旧 Task，不退款或重新派发。

## 2. 原始资格必须来自实际组合，不能自报

首批只支持原受信 FileBusiness 的受限准备实现：规范输入与布局校验、从原 Depot 读取精确不可变引用、在新 Worker 专属目录内有界复制/写入业务输入与生成 prompt。准备不运行子进程、Agent、检查器、Git、网络请求、外部业务写入或用户脚本。允许的本地暂存不是已停止进程的证明；崩溃现场保留，不复用旧目录。

可信 service composition 在开放 HTTP、创建 Task 或预留首个 Planner 前，选择并验证这一实际准备路径，而非读取业务对象的 `eligible:true` 或同名 profile 字符串：

1. 为现有 FileBusiness 提供受版本管理的窄构造入口，仍复用其文件/引用/提示词实现；合格对象及**实际 prepare 函数身份**由模块私有 WeakMap/不可伪造句柄登记。只读资格解析只认可该原对象和函数，不提供“认证任意回调”的公开 setter；复制属性、包装/替换 prepare、HTTP/模型/Provider 自报均不能取得资格。该内存身份只用于原始组合核验，不序列化为恢复证据。
2. 合格构造的准备阶段不接受任意 `layoutFor`、Depot、时钟或权限读取回调。布局直接来自原 Core 冻结的 `ticket.input.fileLayout` 并经同一 Application 的原 `approvedLayout` 核对；Planner 使用原明确空执行布局。Depot、执行父目录和只读 Application 包装由同一 composition 注入原实例，不从业务工厂返回值取信。不把自由文本 scope 解析成权限。
3. 全部 reservation→许可之间的调用都在这项约束内，不只检查 FileBusiness 名称。首批 v5 仅使用原输入审计默认 metadata-only 路径，必须在打开/接管数据根或首个 Attempt 前拒绝配置任意 `auditDisclosure` 回调。把回调移到原许可提交之后仍不足够：回调在服务进程中产生的子进程/网络效果不一定受原 custody 管理，不能据原 none-start/cleaned 结清。不得以同步函数、无 await 或正则脱敏宣称没有外部效果。旧格式已有披露功能不改，不追认为新恢复例外；原 prepared/handed-off 观察强度、实际时间和秘密披露规则不改变。
4. `authorize`、原生工具/Skill/登录和业务 checker 不因此被禁止；它们仍在许可之后执行，继续使用原权限、scope 登记、custody 和独立验收规则。合格 FileBusiness 的 collect/release 不提前执行。自定义 prepare、Git 准备及不能证明上述实际接线的包装默认不具备新例外；它们可以继续使用原受支持格式，不能被本决策自动升级。

这不是防恶意宿主配置的沙箱：受信组合代码仍属于部署信任边界，私有句柄只防误接线/数据自报，不能认证任意恶意 JavaScript。若未来需要另一准备实现，必须提供其完整无外部执行 producer 证据及同链测试，不能仅添加一个 capability 字符串。

## 3. 新格式与最小不可变绑定

新增 `marshal-node-task-sqlite/v5-unpermitted` 和对应服务 `layout:5`；沿原文件 sentinel、SQLite metadata/version 及服务根校验，使旧 v1–v4 reader **在 claim 前拒绝**，不能只添加旧 reader 会忽略的字段。v5 完整保留 v2 custody、v3 问答、v4 修正的语义；问答/修正仍按原显式业务配置启用，不因 v5 强制增加问题或返工。

首批只创建新空根。旧 v1–v4 根、已存在无标记 reservation、旧备份、旧失败与在途记录不能补资格、补许可、公钥或结算证明；不能改 marker 将其视为 v5。旧数据迁移仍属于 ADR 0085 的 U1，不由本决策授权。持久格式改变不能靠重建空数据库解决。

复用原 ticket/Worker 记录、`worker.reserved`、命令、容量和同库回执，不新建另一账本。原 `nextWork` 事务把闭集数据描述 `startProtocol={profile:'node-unpermitted-reservation/v1',preparation:'file-staging-only/v1'}` 纳入 ticket 的原规范化 `reservationDigest`；该描述由已核验组合的内部 DI 提供，HTTP/Planner 不可填写，不能在恢复时由当前配置推导。原 `storeId/generation/taskId/workerId/commandId/inputDigest/planDigest/deadline` 和已有 `repairId` 继续绑定，函数不序列化。

v5 启动时缺少该受信组合、数据描述不支持或实际 prepare 被替换，必须在任何 Task/Planner 前拒绝。新服务和离线 reader 必须能严格解析该完整格式；旧格式继续原 bytes 与解释。原业务输入/计划摘要不因恢复改写，原无 repair/interaction 的字节规则保持。

## 4. 从许可 producer 到否定证明

原启动许可边界不变：`TaskCleanup.bind` 在一个短事务内重验当前 owner、原 ticket/期限/stop，原子提交 Worker 的 custody 绑定、`worker.custody-permitted` 事件和 `execution.custody-binding` 不可变回执。事务成功返回后才调用原私有句柄 `permit`，再调用 Provider/Verifier。ACP、Pi、command 三条入口必须消费同一 ticket-bound `executionContext.launch`，不能在调用前另起执行或回退直接 Runtime。

恢复中的“许可不存在”仅在下列条件**同时成立**时成为 Core 对原启动协议的否定证明：

- 已持有原完整 Store 的物理排他 writer，格式、完整性、受保护文件与原事件/投影引用检查通过；不能从锁外分页快照、缺表/缺文件的残库、旧备份或候选目录取信。
- 沿原 `inspectBeforeClaim` 先取得全部**已绑定**旧执行的原封闭观察，不能跳过旧 custodian 的迟到许可窗口；随后 claim 新 generation。原 owner 的后继 SQL bind/start/finish 无法提交，未提交许可不能通过原私有启动协议发送；不以 ppid、过期时间或 JavaScript 同步段声称跨进程原子。
- 在当前 owner 的短事务中，重读原 reservation、完整 ticket/input 摘要、对应原命令/预算/容量和任务归属，证明该记录原来就采用 §2–3 的协议；它是旧 generation 的 queued、无实际 execution ID/start/result 的义务，且尚未结算。
- 对**同一原 Worker/命令**同时证明没有 custody 绑定、没有原许可回执、没有许可事件；三者任一存在则回到原签名观察路径，任一矛盾、缺损或不支持则拒绝。不能只测一个 nullable 字段；按原 Task/Worker 的有界事实查询，不扫全局历史后取第一页当全集。
- 没有与该结论冲突的 started、结果、原执行观察或额外 scope 义务。已有 `input-prepared` 仅是有界本地准备观察，不表示许可；handed-off 或其他与未启动冲突的事实必须拒绝。过去曾执行而记录被删除不在此信任模型内，损坏/不一致必须 fail closed。

准备中的原 custodian 可能尚存，但其未收到已提交许可就不能启动 Agent；它仍执行原有限准备等待/断连封闭规则。这个结论**不声称该辅助进程已经退出**，不接受它自签无绑定文件、不按磁盘 PID 发信号，也不删除未归属目录。若部署无法保证准备期原协议封闭，不能启用此例外。

## 5. 当前 owner 一次中断结算

在现有 `TaskCleanup` 增加内部 `settleUnpermitted(workerId)`，复用原 Application 短事务和恢复循环；不增加 HTTP“已停止”按钮、状态 PATCH、cleanup 上传或另一个 controller。

同事务完成 §4 全部重验后，追加 `worker.unpermitted-settled`，内容绑定原 reservation 摘要、协议、当前结算 generation 和 `disposition:'never-permitted'`。这是一项 Core 结算事实，不是 Runtime receipt；Worker 的 `cleanup` 保持 null，不造 `cleaned:true`、started/exit/guard/进程 ID，也不把实际 token/工具使用量写成零。

只结清该原命令与占用：Worker 中断失败；若原取消意图依原规则获胜，则按原取消状态收口。没有剩余未决义务时 Task 才进入 `failed/service_interrupted` 或原 `cancelled`，原 Operation 据实失败/成功，未知兄弟仍 intervention。复用原问答关闭与修正 Operation 收口，不投递旧答案，不接纳旧业务结果，不生成 Decision；已完成节点/已接纳成果及旧失败历史不改写。已经因该义务进入 intervention 的 Task 仅允许这一命名例外转失败/取消，不复活为 queued/completed。若对应原 Operation 已因该义务成为 unknown，只允许本命名例外在全部义务结清后精确收口其投影；原幂等回执保持不变，不放宽通用 Operation 终态转换，也不清除未知兄弟。

原 execute outbox 由 unknown 一次变 observed，原 capacity 精确移除对应 Worker/generation；Attempt/重试/返工及任何未知费用不退款，deadline 不刷新。未 reservation 的旧代命令仍沿原中断处置，不重放 prompt，不派替身，不隐式新建 Task。暂存目录保留且不得复用，本例外不授予 GC 权限。

幂等键是原 Worker/reservation，不是新请求 UUID；原结算摘要与引用已存在时精确重放零追加/零再次释放，异内容拒绝。SQL 提交失败保留原义务；提交成功但调用响应丢失只读取原结算。启动恢复在开放 ready 前有界扫描并结清，GET 仍只读；存在任何未覆盖活动义务则不报告 ready。

## 6. 一个实现包与真实接缝

| 原文件/接缝 | 最小变更 |
| --- | --- |
| `task-business/index.mjs`、`task-files/index.mjs` | 复用文件准备主体，提供实际受限构造/私有身份核验；不把任意 DI 函数默认为无外部效果。 |
| `task-service/composition.mjs` | 在接单/首个 reservation 前核验实际组合，选择新格式；原 preclaim 不变，claim 后同恢复循环结算新义务。 |
| `task-application/execution.mjs`、`cleanup.mjs` | 原 reservation 冻结协议；严格按 Task/Worker 查询原许可与冲突事实；一次中断结算、Operation/容量/问答/修正统一收口。 |
| `task-supervisor/controller.mjs`、原 Runtime/Provider 入口 | 保留 SQL 许可先于原句柄 start；覆盖准备与审计回调顺序，验证三个真实启动入口不旁路。 |
| `task-store/store.mjs`、服务/发行清单 | 新格式与旧 reader 拒绝；仅在现有事务 API 确实缺少精确归属查询时补有界查询，不扩大全局读取限额或增加第二权威。 |

新事实的 HTTP audit/events 如实展示中断结算，沿已有 Task/Worker/Operation 形状与 nullable cleanup，不将它冒充进程清理。实现若发现必须改变公开 Schema 的字段解释，应在同一草案审查中聚合明确，不以兼容名义静默重写旧回执。

## 7. 验收：复用六个真实停点，不伪造 terminal

已有独立候选 `8ceea577747fce9b2b2f7ac279ad7fec880c1c91` 的 `control-commit-recovery.test.mjs/.fixture.mjs` 在原 Store.write 同步 COMMIT barrier、原 CLI/SQLite/custody 下覆盖六个停点；作者定向 6/6 通过是**旧规则与两个 intervention 窗口的证据**，不是本草案实现成功。保留该旧格式行为，在新格式完整链中验证：

| 原服务 SIGKILL 停点 | 新格式必须证明 |
| --- | --- |
| reservation COMMIT 前 | 回滚无 Worker/Attempt/容量；不重派旧命令，原合法控制可结案。 |
| reservation COMMIT 后、custody 准备前 | 原 protocol 已耐久、无许可；新 owner 一次中断结算，原预算不退。 |
| 绑定 COMMIT 前 | 绑定回滚与原 reservation 同时可见；只走 never-permitted，不能接受无库公钥的观察。 |
| 绑定 COMMIT 后、原 permit/launch 前 | 必须走原公钥验证的 none-start observation，不用新的否定例外替代。 |
| cancel COMMIT 前、202 丢失 | 不凭丢响应伪造取消意图；原执行按真实证据失败收口，旧 CAS 不偷偷刷新。 |
| cancel COMMIT 后、202 丢失 | 原取消回执精确重放，所属义务真实结清后 Operation succeeded；不重复预算/启动。 |

每个可收口场景还必须通过原 HTTP 开新 Task、Planner/两个受管作者/独立 checker、最终下载消费及再次正常重开，原 Task/回执/预算不变、零旧 redispatch；不是只断言 ready 或容量字段。取消与有许可执行仍使用原句柄和独立观察，不能写假 cleanup；无模型夹具证明故障语义，不冒充 Pi/Qwen 模型实测。

同一包聚合负例：伪造/复制资格、包装 prepare、任意预许可回调/Git 准备、新格式组合缺失须在首个 Attempt 前拒绝；旧 reader/旧根/旧记录不追认；许可三种事实任一存在或相互矛盾、摘要/命令/容量错绑、handed-off/started/额外 scope 均不结算。加入旧 owner 迟到 bind/start/finish、bound/unbound 混合兄弟、原期限已过、问答/修正原周期、结算 COMMIT 前后和重复冷开反例；容量恰一次释放，未知兄弟不被清除。

## 8. 支持边界、替代与状态

本方案解决单节点可信文件业务的**两种许可前服务崩溃**，不证明旧根、Git 准备、任意业务插件、已许可但原 custodian/证明共同丢失、detached/远端效果或损坏数据库可自动恢复。既有未决旧根目前没有可凭本 ADR 新增的安全 HTTP 解锁；诊断应明确缺证据，不建议删锁、手改库或把新根冒充原任务恢复。多重故障与历史升级仍按原独立支持项，不把 Linux/容器/整机重启变成本 Mac 修复的前置。

可替代方案是先准备原 custodian，再将 reservation 与公钥/许可一次提交，减少未来无绑定窗口；但需重新划分临时 ticket 与原预算 CAS，仍不能证明任意业务准备无外部效果或修复旧记录。本轮选择上述窄否定证明，不重排整个调度生命周期。

草案接受后仍须完成上述一个生产纵切、独立审查、同源故障链及对应支持文档；不能以 ADR、六个旧规则测试或新格式名字关闭 B2/B3 或宣布 stable。现有失败/未决证据保留，不修改总发布目标。
