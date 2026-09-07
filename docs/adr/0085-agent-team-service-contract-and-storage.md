# ADR 0085：Agent Team 服务、开放接入与单一事务存储

- 状态：Proposed（当前产品目标设计；初稿完成三轮审计，本轮补齐旧合同适用性。尚未记录本 ADR 的正式接纳，未启用新运行时）
- 日期：2026-09-07
- 决策范围：单用户、单节点、可信仓库的 B1→B2→B3；不启用 HA、多租户、恶意代码执行或自动发布。
- 方案：[服务架构](../agent-team-service-architecture.md)；验收：[Milestone](../agent-team-service-milestones.md)；审计：[复核记录](../audit-agent-team-service-design-2026-09-07.md)。

## 1. 背景与精确取代范围

维护者需要的产品是一个可独立安装的 HTTP 服务：用户提交简单需求，经澄清/确认后由本机不同 Agent 协作，能够查询 DAG、内部可见进展、答疑、取消及复盘。当前代码的 fixed AF_UNIX/Pi 组合、文件 RB1、禁止部分原生 Skill 的 profile 和人工驱动团队脚本不能直接等同这个产品。

本 ADR 接受后，仅对下列新服务 profile 的部分取代/澄清。当前设计可按此目标消除旧实现耦合，但提案不授权默认 enable、旧库迁移或复用旧 activation。旧 profile 在实际 cutover 前仍执行原合同，未列出的证据、授权、fencing 与故障不变量保留；文档适用性见[对照表](../design-contract-map.md)。

| 原合同 | 本次替代 |
| --- | --- |
| ADR 0052 §1 文件型存储、§2 全部 Web UI/Goal DAG 延期 | SQLite 为首版目标权威存储；前移有限任务详情 UI 与受限 Goal 图，不前移通用编辑器/动态 DSL |
| ADR 0058 §1/§3/§7/后果、0063 §1–§4 的 Pi0843IdentityV1、固定根/55 materials/版本常量 | 只保留为旧 Pi profile 的证据合同；新 Agent 身份描述/协议解码由受信注入 Adapter 提供，Core 使用中立 schema，执行层核对实际观测；开放版本不取消逐 Attempt identity/input/fencing，不采信 Worker 自报兼容性 |
| ADR 0062 §1/§3 仅本机 AF_UNIX 身份客户端、0076 §1–§6/§9 的本机定位/客户端与固定 T1/T2 范围 | 保留 fixed `marshal control-plane serve` 与唯一 Application Port；新增认证 TCP HTTP facade，外部客户端不要求读本机 RB1/验证进程 peer。旧 AF_UNIX 仍守原认证合同；新 HTTP 不是 locator 失败时的隐式降级，服务端仍重查当前 authority |
| ADR 0066 §2–§6 的仓库内 `./bin/marshal`、固定物理布局/Pi 组合、direct-call/文件集合/AST 及 S1→S2 顺序 | 稳定安装与 repository identity 分离，唯一组合根 DI、业务提交迁 SQLite；保留先拿 scope 单 owner 锁、再验证/提交 owner successor，持锁前无业务副作用；不要求旧 provisional verifier 函数形状或旧两切片排期 |
| ADR 0065 §1–§7、§10 与后果的双账本解释器、跨账本 proof、borrow/锁序/exact AST、封闭文件与阶段顺序；0067 §2.7/§7 及 S1′/S2′段的继承条款 | SQLite 路径改为事务写 intent/outbox→锁外有界执行→同 Store 事务重验/接纳 outcome 与 Run successor；保留唯一 Core producer、当前 owner/lease/CAS 与无通用 append 旁路。旧路径形状测试保留，新路径以等价行为门禁替代；0067 的 source/process 观测及未知归属/permanent intervention 不变 |
| ADR 0069 §2–§4 的跨账本 reservation/budget 与 allocation projection/固定锁序、0070 §2/§4 的 RB1 字段派生、0081 候选中的 projection/lane 物理形状 | 新 Store 一次事务保存对应 reservation、预算、binding、release 与投影；保留 creation-once/lookup-before-claim、全仓 target 唯一性、输入摘要、stop/admission 竞争、terminal/cleanup/release 后复用。旧 revision 字节/派生语义不改写，0081 不因引用而被追认 |
| ADR 0051/0068/0073 的旧安装路径 activation 续行约束 | 新服务引入下述 operator-local 安装记录，只授权新 lineage 的 non-production 试用；旧 activation/旧 Run 不自动 rebind，首次导入仅已收口历史；managed/stable 仍保留原门禁 |
| ADR 0080 的首部署 file-backed、暂不扩 Provider/UI | 明确目标为 SQLite + 三个首批 Provider + 最小任务页；按一个 Provider 单纵切先完成，不等待完整增强矩阵 |
| ADR 0019 §8 人工等待全局 Goal pause 的解释 | 新增有界节点级 UserInteraction，局部待答不隐式全局停派；显式 Goal PAUSED 始终停全图新派发，terminal Run 不复活 |
| ADR 0019 §4 的全维度强制 actual settlement | 按批准 Policy 分 enforced budget 与 observed usage，支持逐维度 unknown/source/coverage 与保守 debit；不将缺失数据伪装为零或自动退款 |
| ADR 0083 的候选物理 RB1 事务/固定三节点 Pi 模板 | 保留批准＋预算＋创建义务原子语义与成果精确接纳；映射到 Store 事务、有限单/串/并行模板与注入 Provider。0083 仍是原候选状态，本 ADR 不追认其全部实现 |
| ADR 0075/0084 的 Pi 模型终态控制 envelope | 允许经迁移的 Adapter 从真实执行/制品构造控制 envelope，保留 transcript、错误终态、身份、候选和独立验证；旧 parser 在新合同测试通过前不静默放宽 |

不得把文档取代解释成当前 unsigned binary 已获新生产授权、历史组件已经集成、旧 Run 可以改写或旧审批扩大。ADR 0052 stable、签名/Linux 门禁不变。

历史实施阶段与函数/文件/物理锁结构不是长期不变量。当前排期统一为 B1→B2→B3；0058/0063 的“新版本必须修改 Core 常量”、0065/0066/0067 的封闭切片/AST 条款不再作为新 profile 的设计准入。这里只集中替代同一服务重构所需的旧假设，不删除其它 ADR，也不为每个实现拆分追加新 ADR。

## 2. 唯一服务与依赖反转

一个 fixed binary、一套应用组合、同仓库一个 owner、一份权威 Store。HTTP/CLI 只注入 Application Port；生产调用链不能回落到 `execution.Run`、独立 legacy server 或 child CLI。

Core 只依赖中立 Port、能力快照与领域类型。Adapter 准备/解码 Agent 协议，执行层管理启动/句柄/deadline/归属，SandboxProvider 管理 allocation。Plan/Review 可使用本地 Agent 生成 proposal/Assessment，但必须使用与 Implement 不同的角色输入、权限与接纳规则；不创建通用 Provider 权威 RPC，不授予 Agent 写账本能力。

核心能力是可驱动、可追踪执行身份、可判终态/取成果、有界止损、显式失败；callback、工具事件、原生交互、中途 steering、resume、tokens/context 都是分别可选的增强。版本按兼容协议与 conformance 管理，实际 binary/config/能力仍逐 Attempt 冻结，禁止中途替换或复用其他身份的旧证据。

Agent launch descriptor 是注册的受信 Adapter 按版本化中立 schema 生成的配置，不是 Worker 提供的权威结果。Core 检查允许 profile、能力和冻结输入；执行层按该 profile 核对真实 executable/runtime/material 观测并绑定 command/outcome。未知协议或不可证明的强制能力拒绝，增强能力未知诚实降级展示。更改支持版本通常更新 Adapter/conformance 而非 Core；只有身份/信任语义改变才需要新决策。

配置选择优先级在同一 Port 明确为：已批准的任务 profile 引用（仅选择允许项）→服务启动时的显式 profile 配置→该 Adapter 声明采用的 Agent 原生配置。Marshal 不私自插入模型 fallback；环境和原生加载规则由 Adapter 记录可见来源。不可见配置内容标 unknown，不能宣称完整可复现或满足需要其证明的任务。

原生 Skill 可用，但不自动授予额外宿主/发布权限。普通本机同 UID 加载用户配置存在 ambient credential 风险，不得声称仅靠提示词实现 Publisher secret 隔离。启用原生配置的 profile 仅需所选 Agent 的模型鉴权；Worker 执行账户/环境不能含 Publisher 凭据，也不能可达已登录发布工具/keychain 的受权入口。无法证明分权时只能报告该配置支持受阻，`publication:none` 不是其绕过条件。Draft PR 由独立受控凭据路径的 Publisher 执行；不建设统一 Agent 登录平台，不擅自删除用户凭据。

## 3. HTTP 安全与对象身份

TCP 默认绑定 loopback，认证仍必需；非 loopback 启用要求 TLS 和显式授权，禁止无保护旁路端口。限制 Host/Origin/CORS、CSRF、输入大小、队列、订阅与 deadline。认证 secret 不进入 URL、业务账本、prompt 或日志。Unix socket 管理路径可以保留。

每条变更绑定调用者、repository/scope、操作类型、请求摘要、幂等 key、deadline 和预期 revision；服务端取得当前 owner/Policy/lease/Evidence，不能接受客户端提交的 current-ledger “证明”。同请求幂等，异内容冲突；异步操作回执不等于业务完成。所有权限检查在应用层复用，不只在 HTTP handler。

重新认证/授权后先查 scope 内的幂等记录：同 key 同摘要返回原回执，不能因第一次操作已推进 revision 而拒绝精确重放；只有未命中才对新命令做 revision CAS。远程入口还须遵守 ADR 0018 §12 中适用的双向身份、撤销与 replay/time-window 约束，不能把任意 bearer token 加 TLS 解释为完整 authority。

启动路径从受信安装元数据验证实际 fixed binary，而不是要求位于业务仓库。运行身份与 repository identity 分别绑定每个 owner/session；canonical `.marshal` 仍是该仓库唯一业务状态根。外部 state root、多仓库服务和多 owner 尚未启用，不能用任意 `--state-dir` 绕开锁。

B1 新 lineage 使用 `operator-local` 安装记录：由操作员显式调用受信固定安装命令，生成 owner-only、版本化不可变记录，绑定安装 ID、canonical executable path、实际 bytes SHA-256/size/sourceHead/profile、repository identity、有效期和 `publication:none`。其 producer 必须从 held 当前文件对象观察，不接受 Worker/API 伪造 identity；server 每次启动及 mutation 重验对象与批准 scope。该记录是 same-user opt-in，不是数字签名、抗同 UID 篡改、managed receipt 或生产授权。B3 以正式签名/安装收据闭环提升支持。当前系统安全不允许运行时仍停止并报告，不能用匿名 helper 绕过。

首次迁移不提供跨路径/版本的旧 Run rebind：包括 REVIEW_PENDING 在内的全部旧非终态必须先由原受支持入口合法收口；新服务仅导入只读历史。复制旧 bytes/digest 不授予新 binary 消费旧 activation 的权限，不重签旧 Evidence。确需在途跨版本续行属于未来单独支持，不阻断新空仓库的业务验证。

## 4. 权威事务与 SQLite 切换

Store 最小语义是 `expected owner/revision + validated command → append immutable events + update projections + idempotency result + budget transition + outbox obligations` 一次提交；任一部分失败全部不成立。数据库 row 是实现，业务摘要和事件规则不是由数据库重新定义。

启动具体顺序：事务冻结当前 Attempt/输入、预算与 command intent/outbox→释放事务锁后由所属执行控制器执行→事务中重验当前 owner/lease/generation、真实 command outcome 与 Run CAS，并原子接纳 outcome/唯一合法 Run successor。命令落账不等于进程已启动；缺失、未知或被 fence 的结果不得合成 RUNNING。跨步骤 crash 用同 commandId Inspect/Reconcile，不能用旧跨文件 proof 或新通用 `Append` 旁路。ADR 0065 中仍有意义的 hostile/replay 断言迁到该事务接缝，旧路径在该 scope 切换后不可写。

SQLite WAL 使用本地磁盘，权威提交使用 `synchronous=FULL` 及受支持的耐久文件系统，短事务串行写入；Worker/Verifier 执行和网络调用不能持事务锁。查询可并发，长 Verify 不占全局 writer lane。高频遥测与权威事件分流，不承诺所有 token 都落业务日志。

reservation、dispatch claim 和 budget 使用同一 Store 的 creation-once/幂等键与事务，不再要求双账本两次 fsync 补 consumed。任何真正可能启动的 execution obligation 先有冻结输入、授权和保留额度；合法 Run start successor 消费一次，response loss 不 mint sibling。allocation 以 repository-global target identity 保证一个 worktree 一个活跃写绑定；release 必须核对同一 current owner/Attempt/generation、terminalization、process-terminal、cleanup/disposition 的精确链。新绑定只能在旧 release 耐久成立后取得，不因投影缺失、关闭 FD、server 重启或换库而释放。0070 的旧字段定义用于旧记录逐字节解释，新 schema 不把 reservation key、Run revision 与 Attempt revision 混用。

制品先持久化再提交引用；失败事务留下未引用对象供有界 GC，不留下悬空 authority ref。原始不可变 bytes 和 digest 保留，导入/恢复不能因重序列化重新定义旧审批的 subject。PostgreSQL 以后实现同一事务与 conformance，不默认引入事件中间件。

### 一次性迁移协议

1. 新版本先以只读方式识别旧 store，执行预检，报告未决 Run/command/effect/owner 与导入范围。不得一启动就自动把活动账本迁走。
2. 在原 writer 路径 stop-new，排空或通过已有合法取消/对账收口全部非终态（不只活动进程）；有 REVIEW_PENDING、未知写入者/外部操作或无法确认的 ownership 时拒绝 cutover，保留原服务的受控恢复，不强杀无归属进程。
3. 持同一 repository owner 锁，生成一致原账本/制品备份；在未激活位置导入全部被接纳的历史 ID、原 bytes/digest、引用、预算和幂等记录，建立可验证的导入 manifest。原事实只迁移，不补签新证据或刷新 lease。
4. 检查完整事件/引用/Outcome/预算相等、孤儿与损坏诊断、新旧只读投影等价。再持久提交唯一 active store generation；新 Store 激活前没有新派发。
5. 切换必须机械禁止旧 reader/writer 误当新 store：安装版本 gate 和旧入口拒绝测试。单独写一个旧版本不识别的 marker **不够**；若旧 binary 不会拒绝，必须先发布识别 gate 的过渡版本，或采用受权且可恢复的旧 writer 不可打开布局并证明所有旧入口已阻断。缺证明不激活。
6. 切换后旧 ledger 保留只读归档，SQLite 为唯一业务真值；没有双写过渡期。任意崩溃点重启只选择经验证的单一已激活 generation，不能从“哪个文件较新”猜测。
7. 尚无新事务时可在原锁和一致校验下退回原备份；有新事实之后禁止直接换回旧 snapshot，必须前向修复/显式迁移，避免丢失授权、预算和外部效果记录。

全新仓库在机械证明没有任何旧 authority/store/活动 owner 后，可直接初始化 SQLite 并先完成最小需求确认/单任务交付；不得通过改路径、重命名或清空旧 `.marshal` 将已有仓库冒充全新。新安装试用与旧库升级的退出条件分别记录，正式支持仍须完成导入与恢复。

备份由 SQLite 一致快照机制与对应不可变制品 manifest 产生，不在服务运行时只复制主 `.db` 文件。恢复默认 read-only；确认旧 owner/执行不能再写及外部效果可对账后取得新的 ownership 才开放变更。历史 replay 不执行 outbox 副作用；只有当前 reconcile 判定并经授权的未决命令可继续。DB rollback 从不等于外部世界 rollback。

## 5. 节点交互、生命周期与取消

UserInteraction 是 Goal 权威对象，持久记录 subject/revision、节点阻塞范围、类型、期限、答案与消费引用。问题创建、答复接纳、预算/继续义务各自走幂等事务；同一答案只消费一次。普通回答不能转成 plan approval、permission 或 publication authorization。

节点待答只阻塞自身依赖；全局 pause 总是优先。待答未运行节点不占执行槽；其 reservation 是否保留到期限必须由批准 Policy 冻结。活动 Agent 只在原 deadline 内有界等待，期间仍占槽/锁。无法继续或过期走合法终态与 Outcome，再由批准范围内新 Run 继续；不增加无限等待 Run 状态，不复活 BLOCKED。

需用户判断的验收项在计划批准时冻结为必需的 `delivery-acceptance` Interaction，绑定精确候选/制品/Evidence/合同与具名 actor；未答不成功，拒绝触发预算内返工或非成功 Outcome，过期按冻结 Policy 收口。候选改变不能消费旧回答；人工通过不豁免强制独立检查，不形成 publish 授权。

向活动 Agent 发送答案是 outbox 命令，发送前再次核对 Interaction/Run 与 Goal pause/cancel fence。Agent 已收到但确认丢失时，只有原生幂等/查询能证明结果才可重放，否则 unknown；先停止并确认旧执行/收集成果，才能在原预算下由关联新 Run 消费答案。Core 一次性消费不能宣称远端 exactly-once。测试必须包含“answer commit→cancel→发送”和“Agent 收答→ack 前 crash”。

取消通过 owned execution identity，不通过任意 PID API。先持久 stop intent/fence，再有界停止并 Inspect，确认结束才释放 writer/scope；未知结果保留未决，绝不以 request accepted 伪装已终止。Run 的实际状态转换沿原生命周期；新 UI 的 waiting/canceling 是投影，不旁路转换守卫。

`NO_CHANGE` 的交付扩展只允许原批准合同明确许可且“现有精确成果已经满足批准需求”的验收型节点：兼容旧 Run 时仍检查 `allowNoChange` 和诊断交付物；独立 Evidence 和具名验收决定绑定现有成果，GoalOutcome 显式记录无新 patch，不把 NO_CHANGE Run 改写为 ACCEPTED。字段级 Schema 与 producer 在 B2-B 一起实施，缺失时保留原诊断，不提前宣布团队完成。

## 6. 监督、计量与审计

监督归唯一 resident controller；LLM 只产出 proposal/Assessment。progress 是观察，不改变权威成功状态；增强能力缺失的 worker 仍受硬 deadline、cancel、结果收集和独立验收约束。队列等待、实际执行槽、Review WIP、Provider 额度分别计算，review 堆积不能靠盲目加作者解决。

所有 retry、rework、successor 与 plan revision 计入原 Goal 总预算；transport 重发复用 commandId，不制造业务尝试。结构性失败只在事实变化并通过预检后重试。缺实时 token 的 profile 不承诺硬性成本限额，需明确使用可执行的墙钟/Attempts/并发/输出预算；要求严格费用上限时不匹配该 profile。

批准 Policy 按维度冻结 `enforced` 和 `observed` 模式。结算版本化记录包含逐维度 `value`（未知可空）、`source`、`coverage`、`debit` 及其依据：可测量维度按真实消费结清；未知 token/compute 的 actual 保持未知，执行义务可以终结但不能视作零花费或退款。若保留该维度的准入额度，只能按批准的保守 debit 留账，不释放未知预留以扩容；无可信消费上界的 profile 不能声称严格 token/cost enforcement。补到真实数值后用幂等修正事件，不重写原结算。旧 `Actual`/非 nullable token Schema、生产者和重启后后继准入测试在 B1-B 一起更改，不将预算未决错误地保留为活 Worker。已识别结构性失败立即禁止原样重试，第二次同 signature 仅是分类/预检失效告警。

prompt/context 实际提交快照、用量来源和计量覆盖在执行时记录。隐藏推理与 Agent 未暴露输入不要求采集；未知不写零。业务证据与脱敏审计材料区别存储/展示，保留期限与权限控制不能依赖 Agent 自律。日志、事件和摘要都不能授予接纳/发布权限。

## 7. 验收与被拒绝的简化

必须在同一生产路径证明：认证请求/幂等与旧 revision 拒绝；单 owner/Store 切换恢复；真实 Agent 单任务与两个并行节点/集成；无工具事件可执行；原生 Skill 不扩大权限；活动等待/取消/未知结果；错需求但测试绿被拒绝；prompt/用量缺失诚实呈现；独立验证与最后交付。详细 matrix 与次序以本 ADR 引用的 Milestone 为准。

拒绝：双账本写权威、HTTP 包装 legacy child CLI、所有 Agent 强制 ACP/工具流/精确版本、模型自写成功证明、无限 rework、每个工具调用变 DAG 节点、将 DB 恢复称外部回滚、为观察功能先建多服务、把普通主机子进程描述为恶意代码 sandbox。

这是可实施的目标合同，生产成立仍依赖真实交付、故障测试和正式资产支持证据。不能用通过文档审计代替实机验收。
