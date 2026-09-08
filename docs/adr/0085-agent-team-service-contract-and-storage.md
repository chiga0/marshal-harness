# ADR 0085：Task-first Agent Team、本地简启动与分阶段事务存储

- 状态：Accepted（2026-09-08 用户明确确认“ADR0085 ok，请实施”；授权按本合同实施，不等于运行时已通过验证或已生产发布）
- 日期：2026-09-07
- 决策范围：单用户、单节点、可信任务的 HTTP Agent Team；删除首版 Workspace 产品实体，先真实团队交付，后完善本地服务及正式支持。
- 方案：[服务架构](../agent-team-service-architecture.md)；验收：[Milestone](../agent-team-service-milestones.md)；审计：[复核记录](../audit-agent-team-service-design-2026-09-07.md)。

## 1. 背景与精确取代范围

上一稿把 Workspace、安装身份记录、显式初始化、SQLite 全面替换和三 Provider 矩阵放在团队交付之前，造成管理机制先于业务价值。本稿按用户要求移除这些前置：用户提交 Task，而不是先创建平台对象。一次变更集中记录于本 ADR，不再按每个字段增加 ADR 或开发 Run。

本 ADR 接纳及对应实现验证前，旧运行路径继续执行原合同；草案不授权绕过旧 activation、接管活动账本或给旧 Run 补签。下表只调整所列新服务设计，不改历史 bytes、失败记录或已接受 ADR 的历史状态。

| 原合同/上一稿 | 新目标中的精确调整 | 继续保留 |
| --- | --- | --- |
| 本 ADR 上一稿 Workspace/安装身份/显式 init | 删除 Workspace API、workspaceId、注册/切换/授权生命周期；不改名为 Project；删除新 operator-local 安装收据前置；首次空数据目录自动建立 | OS 安全机制、受管执行身份、数据根排他、损坏/旧数据不覆盖 |
| ADR 0080 §1 单任务 B1→团队 B2 的排期 | B1 变为首个受限团队 PoC；单任务是其中的内部检查步骤；B2 为本地 API 可用，B3 为正式支持 | 原 B1/B2 证据和 IN_PROGRESS 不被改名洗成完成；独立验收与有限任务 |
| ADR 0052 §1 file-backed 与本 ADR 上一稿先全面 SQLite | B1 复用当前唯一权威组合；B2 接入最小 SQLite Store，首次新数据使用 SQLite；旧历史迁移单列 U1 | 每个状态根只选一个权威 backend，不双写，不以读取较新文件选真值 |
| ADR 0062/0076 本机 AF_UNIX 客户端定位与旧 T1/T2 | 同一 Application Port 增加 Task HTTP；目标命令 marshal serve，可作为原 control-plane serve 的薄入口 | 不起第二 legacy server、不执行 child CLI 推生命周期；服务端重验当前事实 |
| ADR 0066 repository 根、CLI/Pi 固定构造；0018 authorityScope 物理映射 | 目标数据目录属于服务内部配置，不是业务资源；组合根 DI；Git 仅执行适配层绑定，B1 现有 Git 限制如实声明，B2 解除通用任务 Git 前提 | 单 owner、任务/执行 ID、输入摘要、唯一 producer；不建第二 Task/Goal 权威 |
| ADR 0058/0063 Pi 固定版本/材料，0075/0084 模型终态 envelope | 新 Adapter 按中立协议和核心能力接入，版本是兼容性/诊断维度；Adapter 根据真实终态、transcript、成果生成控制 envelope | 实际执行/config 来源及输入绑定；旧 parser 不静默放宽；不采信 Worker 自报成功 |
| ADR 0051/0068/0073 旧安装 activation | B1 可复用合法现有安装；新本机 profile 不另建安装身份平台。正式 managed/signing/notarization 留 B3 | 不复制/扩大旧 activation；不绕过 Gatekeeper/EDR；旧在途不跨版本自动 rebind |
| ADR 0065/0066/0067 双账本 proof/物理锁/AST 形状 | B1 复用其已有受控实现；B2 SQLite 同一事务接纳 intent/预算，再锁外执行、事务重验 outcome/唯一 successor | 当前 owner/lease/generation/CAS、先 intent、归属不明不重试；旧路径的有效恢复合同不提前取消 |
| ADR 0069/0070/0081 allocation/reservation 的物理 projection/lane | SQLite 接线后同 Store 保存创建义务、预算、绑定和释放；Git 特有字段留适配层 | creation-once、lookup-before-claim、停止/接纳竞争、确认终止后才能复用 |
| ADR 0019 全局等待及强制全维度 actual | B1 先有限计划确认；B2 节点级持久问答；预算区分 enforced/observed，未知用量不伪造零 | 全局 pause/cancel 优先、原总预算不刷新、人工答复不等于发布授权 |
| ADR 0083 固定三节点 Pi/RB1 候选 | 复用有界物化/集成/Outcome；首个同 Provider 两实例即可，之后再混合 Provider | 精确上游、独立 Evidence/Decision、整体成果验收；候选仍不冒充实机完成 |

ADR 0052 的正式签名、公证、Linux 与 stable gate 不删除；从关键路径后置到 B3 不是提前授予 production。后续组织/多租户/远端鉴权平台不作为本地 B1/B2 门槛。原有未知归属/permanent intervention、迟到结果、发布分权等语义不因简化消失。

## 2. 唯一服务与依赖反转

### B1 原生终态结果合同（显式候选，不改旧 Run）

内部 WorkItem 的 `worker.resultContract` 冻结于 TaskSpec 摘要：省略或 `worker-result-json/v1` 保持原模型声明协议；`native-terminal/v1` 才选择原生终态。未知值在启动前拒绝，未实现该合同的执行路径同样拒绝；不允许旧解析失败后自动回退。组合根从同一冻结字段选择 prompt 与结果解析器，Core 不识别 Provider 品牌。当前仅固定 Pi composition 实现候选，不代表所有 Adapter 已支持。

原生模式下模型只交付业务文件和真实报告，不填写 Marshal 身份、时间、控制状态或证据摘要。Adapter 必须先验证完整原生 transcript 的 session、worktree、事件闭合、重试/工具顺序和 Provider 正常终态，并要求持有的 Supervisor 收集记录确认进程 terminal、exit code 为 0、无 signal、无 transcript 截断；未知退出信息不等于成功。Task/Run/Attempt、执行身份与时间来自冻结输入和受管观察。报告是原生末条 assistant 文本，原样保留（最多 12000 字符，超限拒绝，不静默截断），其中的 JSON/控制字段不获得权威；不可将“受阻/未完成”的报告改写成成功摘要。

此合同生成的 `WorkerResult.status=completed` 仅表示本次调用正常结束、候选可进入独立 Verify，不表示业务完成。`declaredRisks` 明示此边界；未提供结构化文件/命令声明时用空声明集合，不能解释成“没有改动/测试已通过”。用量只写已观察到的数据，未知省略。原 snapshot、DRC、current-ledger recheck、停止竞争、独立 Verification/ReviewDecision 与最终集成验收不变。原 JSON 合同的 completed 语义和字节不变；旧 Run 不迁移或补签。候选测试绿不关闭 B1，必须以真实团队交付和独立业务消费证明。

### B1 首条实现：resident 自动收集与验证（候选）

沿 ADR 0083 已批准计划和原 Run 生命周期增加内部自动推进，不新增 HTTP 权限、Worker、预算或第二账本：从 current owner 下的原计划/创建/Run 事实选择 RUNNING→Collect 或 VERIFYING→Verify，按 Run 轮转，跳过忙 lease 与已 halt 计划；每步重读精确 Run/Attempt/head，陈旧选择仅跳过。与原 HTTP 共用每 Run 调度 lane；Collect 只占短 writer lane，Verify 释放全局 writer，仍持原 Run/worktree lease，独立 deadline loop 不受阻。每个 tick 最多推进一个节点，不递归完成全队或自动签 Decision。

未发布候选先以 `marshal control-plane serve --auto-team-progress` 显式开启；默认旧 serve 不变，避免仍逐阶段驱动的旧客户端被后台推进抢先。Collect 与 Verify 各一个受管、有界循环：前者沿原 30 秒 step 上限，后者上限 10 分钟且仍受原 Task 验证期限/父 context 约束；两者均由同 server 停止并 drain。后续 Task HTTP 主入口采用该模式前须完成对应客户端及真实交付验收。

明确的 attempt-still-running 只继续观察；其他执行失败先保留已有 Run 证据，再追加原 team halt，封闭 stage 增加 collect/verify。halt 同时禁止本候选自动结果推进，避免重启后重复相同错误；不等于 Run 已停止、不释放预算，原手动 Collect/合法停止/独立 deadline 与恢复仍可用。无法提交 halt 则通过 dispatch/collect/verify 共享的进程内 circuit 停止本进程团队自动化并报告未决。只有选择/锁等待阶段的取消可无副作用跳过；操作已调用后失败，即使上下文取消也尝试有界保存 halt，不自动重试。

本候选不透明恢复崩溃中断的 Verify：opt-in server 在开放 endpoint 和启动任何循环前，检查当前批准团队中既存的 VERIFYING Run，先持久 halt 对应团队；因为没有 durable verification-start 事实，无法区分尚未执行与执行中断，保守地把两者都转交人工处置。冷检查遇到忙 lease、读取或 halt 写入失败就拒绝启动，不能跳过后在后台重新验证。已经 REVIEW_PENDING 的完成验证不回滚；本次启动后由 Collect 新产生的 VERIFYING 可正常推进。此最小屏障不建立通用恢复平台，不关闭 B2/B3 的自动恢复出口。

语义 ReviewDecision、任务级 HTTP 与下载消费仍待同一 B1 链接通；自动验证到 REVIEW_PENDING 不等于 ACCEPTED/团队完成。阶段枚举扩展仅适用于未发布同版本候选，旧 reader 不因此获新记录兼容性，既有记录字节不重写。

用户只需要 Task、Worker、Artifact；计划、问题、DAG、交付与审计是 Task 的子视图，不是用户必须先创建的独立平台。公开 Task 复用既有 Goal ID/revision/预算/事实，旧内部 Task 执行规格对外称 WorkItem。B1 先做薄映射，不全仓重命名类型。

一个固定 server、多受管执行进程、一个所选 Store 和本地制品；控制/执行/存储三面逻辑分离，不先拆微服务。HTTP/CLI 共用 Application Port，Core 不导入具体 Agent 或数据库。Port 是 Go interface 一类的依赖边界，DI 是构造函数注入，不是网络端口或动态插件平台。

核心接入能力：能接收输入、绑定实际执行、观察结束/失败、收集成果、在 deadline 内停止所属执行。callback、ACP、工具事件、实时用量、问答桥接、steering 与 session resume 都是分别可选的增强。缺增强能力展示 unavailable，不假造进度；任务确需该能力时才拒绝匹配。实际版本/配置来源记录用于诊断，不要求用户每次注册精确版本。

配置由允许的 Task 选择→服务显式配置→Adapter 声明的原生配置解析，不私自 fallback 模型/Provider。原生 Agent 自管模型鉴权与 Skill；Marshal 不复制 HOME/登录 secret，也不建设统一 Skill 或鉴权系统。首个团队可用一个真实 Provider 的两个实例；Pi、Qwen Code、OpenCode 逐个验证，未测试者不列正式支持。

旧 Marshal skill 完全退出产品依赖、研发准入和验收，不读取/加载/运行；历史失败/审计保留。不把独立验证改成模型自证，不强制每次验收都多调一个 LLM：受控、独立于作者的业务测试可提供客观 Evidence；需要语义判断时才安排有界 Reviewer，Decision 仍由 Core 校验接纳。

<a id="3-http-安全与对象身份"></a>

## 3. 简启动、本地访问与最小身份

### B1 Task HTTP 与可消费交付（未发布实施候选）

截至接受时，已有未接线 DTO、纯模板预览和负例；尚未实现或启用 RB1 draft/stop/delivery、Task HTTP、自动 Decision 与交付 bundle。本合同现已接受，允许推进以下接线；实现及生产可用性仍必须逐项验证，不因合同接受自动成立。

首个入口只开放服务启动时显式安装的 `order-quote/v1` 小团队模板。模板冻结完整业务接口、原独立 oracle bytes/digest、节点范围、实际 Provider 配置、预算、publication:none 和客观验收模式；客户端只提交 intent/inline context 并选择模板，不提交 authority namespace、TaskSpec/Policy、环境或 executable。额外文本仅作需求上下文，不能修改模板的验收或权限；超出模板能力的需求须拒绝或返回明确待确认，不能自动扩 scope。既有 AF_UNIX 客户端和旧批准链保持原合同。

公开 Task ID 就是 Goal ID。首次提交在同一 RB1 追加有界 draft fact，保存规范请求摘要、幂等键摘要、完整已预检模板、创建时间与固定确认期限；不创建 reservation、Run 或 Worker。创建重放先读取原 fact，不刷新身份、输入、期限或预算。确认请求绑定该 draft 的精确摘要/版本，认证后先匹配已有原批准，再核对当前 draft、取消状态和期限；原 accepted-plan fact 仍是预算与创建义务唯一提交点。查询从同一 current-owner 下按 ID 读取 draft/plan/creation/Run/halt/outcome，不要求用户重传原内部批准包；不新增独立 Task JSON 状态机。

本地 HTTP 使用独立随机 token 的受保护 loopback 输入 adapter，并与 AF_UNIX 共用同一应用、writer lane 与 owned Run lane。token 只写受保护连接信息，不进入草案/Worker/日志；拒绝不可信 Host/Origin、任意 PID/路径、超量请求与无界输出。HTTP 请求只调用 Application Port，不直接打开账本或运行子 CLI。取消先在 RB1 记录该 Task 的 stop intent，在实际批准、物化与 Start 准入提交点重新检查并拒绝后续动作，再沿已有 CancelRun/停止与清理 receipt 收口所属执行；意图、信号或 halt 不等于已取消。已有非 RUNNING Run 不能凭 stop intent 伪报 cancelled；只能依据其真实终态或原停止/清理回执展示状态，未决与失败保留可查询事实，重放不能制造替身或重置预算。

只对显式批准上述模板、包含精确固定 oracle 的新 Task，独立客观验收可自动产生原 ReviewDecision：必须实际经过原 Verify，重读当前 Run/Attempt、完整 packet/report/manifest 和所有必需 gate，以批准的 oracle 身份及当前节点全部必需业务断言证明可接纳，再经原 DecisionImporter/current-ledger 接纳。两个上游分别经过各自冻结的节点 oracle 后独立 ACCEPTED，不依赖尚未创建的集成；集成 Run 只有在两上游接纳后才创建，并必须经过组合 oracle。下载后新目录的消费验收另行证明最终交付可使用，不作为上游接纳的循环前置。Worker 摘要、可替换 report 的 pass 标签、存在文件或进程正常退出均不够。出现额外语义/风险需求、缺失或冲突证据、未知/失败 gate 时不自动 accept；旧 Run、原 AF_UNIX 批准和其他模板不继承此模式。实现状态在独立审查与动态验收前仍为 candidate，不宣称 production。

完整交付不能直接等于集成 Run 的增量 patch。producer 在 current owner 下读取原 completed outcome、两份独立 ACCEPTED 上游及集成 ACCEPTED candidate，重验原 plan/creation/Decision/patch 摘要；复用原 `CombineAcceptedPatches` 的固定 commit 元数据、冻结的节点顺序与 `InputsDigest`，从原 base 应用两份上游 patch，同时证明重建 `TreeSHA` 与 commit 等于冻结 integration base，再应用集成 patch，形成最终允许交付文件集合。只导出普通文件和明确声明的使用说明，不导出 Git 元数据、运行证据目录、路径逃逸或未批准文件。manifest 绑定原 outcome fact、三份 candidate/patch/Decision、集成 base、文件摘要及 bundle 摘要；bytes 有界耐久保存后才提交同 RB1 引用。相同事实只复用精确对象，下载按 Task/Artifact ID 与授权查验，不接收宿主文件路径、不读取可变 Worker worktree。成果在新目录执行原整体业务 oracle 通过才关闭 B1 消费出口；bundle 生成或 HTTP 200 本身不是业务成功。

目标命令 `marshal serve` 启动 loopback HTTP 并显示端口；可选 `--data-dir` 只在本机启动时选择内部状态目录，不向 Task HTTP 开放任意根路径切换。新根不存在时自动以限制权限创建；已存在有效 Store 就打开，损坏、遗失部分状态、不兼容格式或 owner 未释放则报出具体原因，不当空库重建。初始化可重入，第二 server 不能取得同一根写权。

不新增用户注册、组织、RBAC、安装收据、Workspace ID 或单独 init 命令。内部 Store ID/generation、Task/Attempt/command ID 与进程句柄仍用来避免串任务、重复启动和误杀，它们不是需要用户管理的身份体系。B1 使用既有合法安装与真实样例环境；全新环境的一键体验在 B2 验证，不冒充已有。

所有业务 HTTP 默认使用自动生成、限制权限保存的本地随机 token；本地客户端按 OS 权限读取，通用 HTTP 客户端从本机受保护文件配置 Authorization，不通过 URL/日志打印 secret。token 不进入 Worker 环境、prompt 或账本；loopback token 不是防同 UID 恶意程序的隔离保证。认证映射到本地操作者，不按 token 随机 bytes 重新划分业务幂等域。

首版拒绝非 loopback 绑定，校验 Host/Origin，默认不开跨域；健康接口只返回最小健康状态。无需登录平台，但不能提供无保护的进程启动端口。后续远端入口首次启用前就必须具备 TLS、认证/授权与撤销等对应基线，不能“上线后补安全”。单台 VM/容器中的服务也不默认暴露公网。

业务路由以 /v1/tasks 为中心，不含 /workspaces。写操作由应用层验证本地调用者、Task、批准 profile、摘要、幂等 key 与 expected revision；先在重新认证后匹配原幂等回执，精确重放返回原结果，未命中新命令才 CAS。同 key 异内容冲突；202 是受理、不是执行结束，pending/unknown 可查询，取消不接受任意 PID。

任务文本可包含资源/仓库/表/URL，不触发 HTTP 自动抓取、宿主读文件或扩权。输入经有界上传/授权读取进入制品；默认只做本地成果交付，不提供任意 executable/env/secret 注入接口。

作者不得取得 Publisher 权限/凭据。仅模型与批准数据读取/验证工具鉴权可沿用；作者可达 Publisher 凭据或已登录发布入口的配置不进入支持范围，包括 publication:none，不能仅因产品没有发布 API 就豁免。B1 复用已有合法 profile，不为此先建统一身份平台或完整隔离矩阵；同 UID 的 ambient credential 风险不靠提示词解决，也不删除用户登录凑通过。普通宿主子进程不构成恶意代码 sandbox。

<a id="4-权威事务与-sqlite-切换"></a>

## 4. 单一权威事务与分期 SQLite

B1 复用已具备 owner、Run/Attempt、创建/预算/命令记录的现有唯一权威组合。它可物理包含 RB1 和 Run journal，但不增加新的平行 JSON Task 状态机或 SQLite 影子写库。B1 不以旧库迁移、全部数据库抽象重写或完整跨版本续跑为前置；业务状态仍只经原 Application/Core 写。

B2 的目标 Store 在一个本地 SQLite 中原子提交 events、投影、幂等回执、预算与 outbox。数据目录只是内部配置；不新建 Workspace/config 业务表或强制 repository/resource binding。短事务核对当前 owner、Task/Run revision、批准输入/profile 和执行归属，外部执行与网络不能持全局事务锁。

执行次序：事务冻结 Attempt/输入/预算及 command intent→锁外所属执行控制器启动/观察→事务重验当前 generation、真实 outcome 和唯一合法 successor。创建一次、lookup-before-claim；丢响应不消费第二次预算，不把 intent 当成已启动。取消先 stop intent/fence，确认同一执行已终止及 cleanup/disposition 成立才释放目录；未知归属禁止复用或派替身。

制品 bytes 先有界保存、核摘要并耐久写入，再提交引用；失败事务留下未引用对象供之后安全 GC，不留悬空 authority 引用。业务事实不可抽样，遥测可以有界批量并明示丢失。SQLite WAL/本地磁盘/同步与一致备份按故障测试验收，不用模式名冒充耐久证明。

最小状态持久化不能后置：批准计划、输入、任务/执行与命令身份、结果/Decision、失败原因、预算和制品必须保存。B1 正常重启可查询已有事实且不重复派发；恢复失败时停止接单、报告原因并保留原受支持只读诊断，不承诺所有未知状态仍有在线 HTTP 查询。B2 证明同版本恢复，B3 扩完整故障矩阵，不要求 B1 假装所有 crash 都能透明恢复。

### 旧数据升级 U1（独立支持项）

U1 不阻断新 Task 或团队演示。只导入显式选择、已由原受支持入口合法收口的旧历史：包括 REVIEW_PENDING 在内的全部非终态和未知效果必须先处理。逐来源持锁一致备份，保留原 IDs/bytes/digest/预算/顺序，以原 authority namespace 分开归档，同名不覆盖，不重签旧 Decision/activation。

真正接管原状态根时，先证明旧 writer 不能写，再切唯一 active Store generation；旧 binary 必须拒绝新布局，只有 marker 不足。导入失败保留原库受控恢复，不能清空/改路径冒充迁移完成。已产生新事实后不得退回旧快照丢记录，前向修复/受控迁移。旧在途跨版本自动 rebind 不在首版。

SQLite 一致快照与对应制品 manifest 一起备份；恢复先只读核对旧 owner/执行/外部效果，再开放写。历史 replay 不执行 outbox，只有当前 reconcile 可驱动未决命令。数据库恢复不回滚外部世界。PostgreSQL 以后实现同一事务接口，不先做双数据库产品。

<a id="5-节点交互生命周期与取消"></a>

## 5. 计划、交互、集成与取消

B1 先用固定小团队模板、明确需求和一次计划确认，不先建自动规划平台或通用 DAG；模板要含具体交付物、验收、依赖、范围和总预算。确认后 resident 自主调度、Collect、Verify/Review、集成和 Outcome，不依赖人逐个推动 Run。两个作者写独立目录/Git worktree，最后独立验证整套精确候选及下载后的可消费成果。

B2 补简短需求的关键澄清和持久 UserInteraction；问题绑定 Task/节点、subject/revision/type、期限及消费记录，重复同答幂等，不同/过期/陈旧答案拒绝。节点待答只阻塞依赖，全局 pause/cancel 优先；活动 Agent 只在原 deadline 内等答，终态 Run 不复活。Agent 无原生交互时由 Core 在执行边界暂停并经确认关联继续，不编造工具问答。

答案发送属于有界 outbox：发送前重查 pause/cancel/fence；Agent 收到但 ack 丢失且无原生幂等/查询能力时 unknown，不盲重发。同一个答案 Core 一次消费不等于跨进程 exactly-once。需要人工业务验收的计划必须预先声明，答复绑定精确成果与 Evidence；缺失/拒绝/过期不成功，普通答复不能授权发布。

通用成果是 ArtifactSet/DeliveryManifest，Git 节点额外绑定真实 base/worktree/patch，非 Git 不造 dummy commit；B1 首个真实 Git 样例不是 Core 永久 Git 限制，B2 必须实测零 Git SQL/文档制品及多仓库上下文。执行层保证自身分配目录排他，不承诺跨人工/服务/业务表的全局锁。

集成必须消费上游已接纳的精确成果；各节点测试绿但组合错误不得成功。局部修正只重做受影响依赖，累计预算不刷新；冻结合同变更使用新 plan revision/关联 Run。NO_CHANGE 只在批准合同、适用旧 Run 的 allowNoChange/诊断要求与独立验收均满足时复用成果，不伪造改动或改写原终态。

B1/B2 默认只交付成果，不做生产 SQL 发布/补数或自动 Git 发布。以后启用外部写必须独立授权、按实测 profile 观察回执/终态及业务结果；unknown 不自动重试，停止 Agent 不证明远端 job 已停。可选 Draft PR 使用独立 Publisher，不自动 merge；不承诺跨系统原子发布、自动回滚。

## 6. Supervisor、计量与审计

Supervisor 是同一 Core 内的确定性控制器，不是额外 Agent 或 watchdog；预算/容量/排队/超时/结果/取消按耐久事实推进，语义建议不直接改账本。B1 两个作者槽，独立验收也计资源；B2 在依赖、目录、内存/CPU、Provider 与验证队列均允许时增加并发，不为并行而拆任务。

progress/tool events 是可选观察，展示来源/最后观察时间；沉默不自动等于死锁，刷日志不延长 deadline。只终止持有明确归属的执行，信号已发不是停止完成；无法确认的写入义务保留未决。

retry/rework/replan/successor 都计原 Task 总预算，transport 重发不造新 Attempt。结构性失败识别后立即阻止原样重试；内容问题一次聚合，局部修正不重跑全队。预算耗尽保存失败 Outcome，不另开同义 Task 刷新额度。

token/cost 无原生计量时 value 为空并记录 source/coverage；先强制墙钟、Attempt、并发、输出限制，不建设计费平台。严格 token/cost 限额只能由可测/可约束 profile 满足。若既有预算维度有预留，未知 actual 不退款，可按批准 Policy 保守 debit 并结清执行义务；补到测量追加幂等修正，不重写旧数值或把 unknown 当零。相关 schema/producer/重启后准入测试须随接缝同步。

审计自 B1 就记录实际提交 prompt/context、执行/等待耗时、尝试、失败、验收和成果；B2 增加可查询聚合视图及增强事件。不可见的 Agent 内部 Skill 展开/模型输入/隐藏推理不冒充已采集，秘密不进入日志和业务事件。

## 7. 验收与非目标

B1 验收真实两 Worker 重叠、整体成果消费、独立 Decision、进度/取消、重复提交不重启及失败记录；B2 验收零 Git/多仓库、简单需求问答、SQLite、同版本恢复和按 Provider 声明能力；B3 才以同路径故障、长期运行、签名、公证、Linux 与受保护资产证明正式支持。具体出口见 Milestone。UI 仅在 B2 的核心 API-STABLE 后启动，不以所有 Provider 或全部增强能力阻挡它，也不阻挡 API 发布。

拒绝：Workspace 改名继续注册、安装身份平台阻断试用、为 SQLite 全面重写后才集成、三品牌先行、每工具调用一个 DAG 节点、每次验收强制 LLM、无保护 HTTP、双写权威、模型自证、无限返工、绕过 OS 安全或旧运行时检查。

本稿是实施方案，不是运行成功证明；文档审计不提升 B1/B2/B3 成熟度。
