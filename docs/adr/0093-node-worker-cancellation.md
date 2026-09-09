# ADR 0093：Node 单 Worker 取消与依赖收口

- 状态：Accepted（2026-09-09；维护者独立审查原提案及格式差量 `6517f850` 无阻塞问题后接受实施。先整合并验证0092纵切，再实施本合同；接受不表示接口已启用或正式可用）。
- 基线：原提案 `c296226a157b89582195e102c5bc08b738756bd9`；本格式决定差量锁定 `dd35332a51abf531a70dc6b2655a8487f7e827fd`。0092 的 v5 在途实现仅用于核对配置接缝，不作为已通过证据。
- 目标：补齐正式 v1 的单 Worker 取消，不以现有 501 永久后置该能力；复用同一 Application、SQLite、Supervisor 与所属执行句柄，不新增平台或控制器。

## 1. 差量与既有合同

[ADR 0085](0085-agent-team-service-contract-and-storage.md)、[ADR 0088](0088-node-task-service-production-projection.md)及服务架构已要求 Worker 可取消、取消不隐式重试，也不等于整个 Task 已取消。当前 `POST /v1/workers/{workerId}/cancel` 已定义为 `worker.cancel`，202 返回 Operation，但 Application 实际仍返回 501。OpenAPI 没有单独的 CancelWorker Schema，请求实际复用 `ControlTask{expectedRevision}`。

缺少的是单 Worker stop 的耐久归属、依赖处理和聚合规则。现有 `execution.fail/finish` 将失败变为全 Task cancelling，Supervisor 随之停止全部兄弟；直接调用它不是单 Worker 取消。本决策只增加下述命名路径，不改变普通执行故障的原 fail-fast 安全处理。

## 2. 公开入口、CAS 与精确接纳

请求继续闭集 `{expectedRevision}`，使用原认证、Host/Origin、Idempotency-Key 和大小限制，不接受 PID、路径、理由脚本、预算、Provider 或执行参数。Worker 没有控制 revision；此字段明确比较**所属 Task 的当前 revision**，客户端先从 Worker 查询 taskId，再读取 Task，不自动刷新 CAS 或重试。

在同一当前 owner 短事务中，按原 Worker 记录解析 Task/节点/Attempt/周期；幂等 scope 使用目标 workerId，请求摘要同时绑定 operation、workerId、原 taskId 和 body。现有 `receiptKey()` 未含 workerId，必须只为本新操作新增分支，不能改变旧操作的键或摘要。先匹配原回执再检查新命令 CAS；精确重放返回原 Operation 字节，异内容冲突。不同 Worker 不得复用错误回执。

新请求仅接受当前周期、未终态的 queued/running/awaiting-answer Worker；已有同 Worker stop 意图的新 key、已 completed/failed/cancelled 的 Worker、旧 repair 周期、旧 generation 未决执行均拒绝，不能凭请求补清理。Task 全局取消已先赢或已终态同样拒绝；精确原请求仍可重放。原父 Task deadline 已到则由原超时处置，不开新的停止周期。

事务一次追加目标 stop 事实，绑定原 reservation/plan/input、generation、Worker/节点和原周期；同时更新 Task revision、保存 `worker.cancel` Operation、幂等回执及一个原 stop outbox。202 只表示受理。Operation 沿原字段，仅 kind=worker.cancel 分支增加必需 `workerId`，供 handler/client 核对目标；旧 Operation 请求/回执字节不变。后续状态由原 Operation 查询，不覆盖初始回执。

## 3. 只停止目标，保留无关分支

stop 事实是目标执行与成果接纳的 fence，不修改批准计划、原 ticket 摘要或总预算。`mayStart`、custody 绑定、实际 launch 准入、答案投递/ACK 和 `finish` 都重读该目标 fence。当前 Supervisor 从原 `#owned` 取得同 Worker handle，复用原 stop/guard/custody；不重建 PID，不向所有同 Task handle 广播取消。bootstrap/准备中同样有界停止，但信号、stop 意图或空目录不代表清理成功。

按原冻结 DAG 计算该节点的后继闭包：未执行的后继不再准入，其原命令据实关闭，Graph 标记 cancelled 并在事件保留“依赖已取消”的原因，不伪造后继 Worker 或 cleanup。正常依赖规则下，目标尚未完成时其后继不应已启动；若观察到冲突的活动后继/错周期，不用本操作掩盖不一致。已完成的历史节点与结果不重写。

无关分支按原计划、期限、容量继续，已接纳的成果和修正周期外保留结果不丢弃。目标的真实 stop 结果必须走单 Worker 分支，不能被原普通失败入口升级为全局 cancelling。全局 task.cancel、期限、真实其他故障或 cleanup unknown 的原安全规则仍优先，不能承诺未知归属时其他执行无限继续。

目标清理成立后 Worker 为 cancelled；只有该目标及所有剩余相关义务均结清、无关可执行分支结束后，Task 才因无法完成原交付进入 `failed`，原因明确为 worker_cancelled，不伪称整个 Task 被用户取消。Planner 取消后不能生成计划；Verifier 取消后不能提交 Decision/delivery。共同依赖目标的最终 Verifier 不启动，原 acceptance 不伪造独立拒收。未完成汇总前仍允许用户显式 task.cancel 停止所有剩余执行；该操作获胜后最终按原 Task 取消收口。

## 4. Operation、问答与恢复

Worker Operation 的完成条件只针对目标：原真实 cleanup，或原 ticket 符合0092全部条件的合法 never-permitted 结算成立，即可 succeeded，不等无关兄弟全部结束。**单 Worker 取消能力与 staging-only 准备资格正交**：Git 与普通 custody 业务持有原执行句柄时也可取消；新格式、取消配置或没有 started 观察本身不授予无许可结算资格。证明不足为 unknown，保留占用并按原规则封闭 ready；不因 Task failed/进程退出标签而伪报成功。预算、Attempt、返工次数及原 deadline 不退款、不刷新，停止失败不重派替身。

只关闭目标的运行中业务问题/待投递义务，不取消无关问题。stop 先于答案投递或成果提交则拒绝后继；答案 ACK/成果先赢时保留原事实，不能撤回已在途数据。ACK 不代替 cleanup，取消不能让旧答案跨执行消费。[ADR 0091](0091-node-same-plan-local-repair.md)的修正仍只允许真实内容拒收：本操作不制造 contentRejection，不让 cancelled Worker 或该 Task 自动进入 repair；已有修正周期被中断时原 repair Operation 据实失败，无关成果保留。

复用 [ADR 0089](0089-node-execution-custody-and-cleanup-recovery.md)的原公钥观察，以及 [ADR 0092](0092-node-unpermitted-reservation-settlement.md)严格适用时的 never-permitted 结算。服务崩溃后不恢复原 prompt、等待表或旧执行；所有旧执行仍按原恢复合同处置，不把“无关分支继续”解释成跨代续跑。目标可证明已结清时，仅本命名路径允许其原 unknown WorkerOperation 变为 succeeded，原回执不变；不能一起清除未知兄弟或放宽所有 Operation 终态。

stop COMMIT 前死亡没有受理事实；COMMIT 后丢 202 则原 key 精确重放。结算 COMMIT 前后/ACK 丢失均按原 Worker/reservation 一次收口，容量最多释放一次。GET 不执行停止或恢复；没有剩余未决执行/命令且原恢复扫描完成才恢复 ready。新用户 Task 可以接单，不等于重试旧 Task。

## 5. 格式与唯一实施包

冻结后继格式为 `marshal-node-task-sqlite/v6-worker-cancellation`，Store 常量 `WORKER_CANCELLATION_FORMAT`、SQLite metadata/version 为6，服务 sentinel 为原 `node-task-service/v1` 的 `layout:6`。新 stop 事实不能被旧 reader 忽略后继续启动/接纳：服务根及 Store 文件格式都必须让旧 v1–v5 reader **在 claim 前拒绝**。只 opt-in 创建新空根；不扩大0092的 `v5-unpermitted/layout:5`，不合并首发格式，不改旧 marker、不追认旧意图/资格/回执，不清空、双写或迁移旧根。U1 保持独立。

沿当前 `startTaskService({custody, unpermitted, runtimeQuestions, repair, ...})` 接缝，只新增受信闭集配置 `workerCancellation={profile:'task-worker-cancellation/v1'}`，默认缺省。它要求原显式 `custody={profile:'node-execution-custody/v1'}`，但**不要求** `unpermitted` 或 FileBusiness。配置选中 v6 后，原问答/修正仍各自显式启用，原批准/预算及独立验证要求不变；HTTP/Task/Provider 不可填写这些启动配置。

| 受信组合 | 唯一根格式/启动资格 |
| --- | --- |
| 未配置 `workerCancellation` | 原 v1–v5 选择规则与行为保持，不启用单 Worker 取消，不将新字段写回旧格式。 |
| `custody`＋`workerCancellation`，不配置 `unpermitted` | `layout:6`/v6；允许原 Git 或普通业务组合，reservation 不附加0092的 `startProtocol`。已有原 handle/有效 custody 观察可结清；无绑定且缺独立证明仍 unknown。 |
| 再显式配置 `unpermitted={profile:'node-unpermitted-reservation/v1'}` | 仍是同一个 `layout:6`/v6；先执行0092的原 factory/prepare 私有身份、完整 staging-only 与 metadata-only 检查，通过才给新 reservation 写入原 exact `startProtocol`。Git、任意 prepare 或任意 `auditDisclosure` 回调必须在打开/claim前拒绝这一组合，不能自动降级或伪装合格。 |

具体落点是原组合根的格式选择让 `workerCancellation` 优先选择6，否则保持原5→4→3→2→1；Store 复用原 sentinel、metadata 和排他打开，仅增加精确版本分支。v6 的 Execution 允许无 `startProtocol` 或0092的完整闭集原值；只有已核验的 composition 可注入后者，仍拒绝调用者从 `applicationOptions.execution.startProtocol` 自报。问答、修正与 custody 的 reader 接纳 v6 是同一事务实现的格式能力扩展，不复制其 reducer，也不把格式识别当业务资格。

恢复解析与当前是否启用 `unpermitted` 分开：v6 必须能严格读取两类**原 ticket**。只有原 `reservationDigest` 已覆盖0092协议、完整原 reservation/命令/预算一致，且同事务证明原 custody 绑定、许可事件、许可回执三者全不存在，才可调用原 never-permitted 结算。任一许可事实存在就要求原签名观察，矛盾/缺损/冲突事实拒绝；Git unbound 或缺原协议的记录继续 unknown。后来切到 staging-only 配置不能给旧记录补资格，后来关闭该可选配置也不能抹掉原有合格记录的绑定；不按当前配置重造历史。

同一个目标 stop reducer 消费原 handle cleanup、原 signed custody cleanup 或上述严格 never-permitted 结算结果，统一核验原 worker stop、Operation、问题关闭、依赖与容量。后者仍保留 `cleanup:null` 和独立结算事件，不伪装 Runtime cleanup。普通失败及全局 Task 控制保留原路径；不要分别为 Git/FileBusiness、v5/v6 或问答/修正另建停止状态机。

一个作者聚合 Application 新入口/目标幂等、Execution 目标 fence/依赖聚合、原 TaskCleanup 命名结算、问答/repair 消费者、Supervisor 原句柄、HTTP Schema/client 及服务/发行接线。复用 `affectedNodes`、原 stop outbox、`settleOperation` 与 capacity；不把单 Worker 控制转发为 task.cancel，不另建队列。主要文件为 `task-application/{application,execution,cleanup,runtime-questions,repair}.mjs`、`task-supervisor/controller.mjs`、`task-api`、`task-client`、Store/服务格式接缝及对应测试。

## 6. 六类完整验收与边界

1. 原 HTTP/SQLite/两个受管作者：只取消 A，A 原句柄停止，B 原执行完成且成果保留；A 后继/共同 Verifier 不启动，最终 Task failed。增加真实临时 Git 库/锁 base 独立 worktree 的 owned 取消：不配置 `unpermitted`，A 原 cleanup 确认前不 release/复用，B 的合法 patch 保留，原仓库 HEAD 不变。Planner/Verifier 目标分别覆盖，不靠固定两个作者伪装所有角色可用。
2. 目标 bootstrap、running、awaiting-answer；cancel 对 bind/launch/finish/答案 dispatch/ACK 的两种提交顺序；迟到成果不接纳，无关答案仍可消费。
3. 原 Task CAS、同 key 精确重放/异内容、跨 Worker/Task、终态/旧周期、第二 key 和全局 task.cancel 竞争；响应必须绑定实际 workerId，零额外 Attempt。
4. cleanup unknown/额外 scope、错误执行身份、停止失败；只操作原句柄，Operation 不虚报成功，不释放未知容量；目标已清理而兄弟未决时只结清目标。
5. stop COMMIT 前后丢响应、清理/结算 COMMIT 前后服务崩溃、原 unknown Operation 合法收口；无旧重派/预算刷新，原回执/events 保留，再开新 Task 能交付并下载，正常冷开一致。增加 v6 Git reservation/prepare 后、custody 绑定前崩溃反例：即使三许可事实均缺失，也因原 ticket 无0092资格保持 unknown/占用/现场；换 staging-only 配置不补资格，不能假报目标 Operation succeeded。
6. 旧 reader/旧记录拒绝；v6两种配置及原v5回归，Git＋`unpermitted`、伪造协议或披露回调须启动早拒；metadata/sentinel错配不 claim/重建。v6重开撤掉 `unpermitted` 后原合格 ticket 仍按原证明结算，新 reservation 不获得资格；反向开启只影响通过当前受信检查的新 reservation，旧 Git ticket 保持不合格。新格式与问答/repair/原 Task cancel 回归，原修正无关结果不变，不把用户停止或结构故障伪造为可修正内容失败。

先无模型原受管协议/独立 checker 整链，再按声明 Provider/profile 实机验证；组件、HTTP 202 或本草案均不等于正式支持。原权限分离、未知外部效果和单节点恢复边界保留；正式 v1 仍需完成该用户能力，不以 501 或缩小目标代替实现。
