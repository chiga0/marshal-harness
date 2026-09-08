# ADR 0088：Node Task 服务正式实现投影与 ACP 接入

- 状态：Accepted（2026-09-08；依据用户持续要求 Node-only 运行和完整 stable 正式发行，维护者在独立审查及唯一 P1 聚合修正、同 reviewer 复核无剩余 P0/P1 后接纳）。
- 目标：实现 ADR 0085 的 Task-first 产品，而不是扩大 ADR 0087 的固定样例实验。
- 非完成声明：本 ADR 接纳不等于 API-STABLE、生产启用或正式发布。

## 1. 取代范围

新增 `node-task-service/v1` 实现 profile，使用已允许的固定 Node 运行时执行受版本管理的 JavaScript 模块；不运行、编译或加载 Marshal 原生二进制及自建 native addon。不是把 Go API 包一层 Node 后继续启动 Go 子进程。

此 profile 取代 ADR 0085 中“公开 Task 必须物理映射既有 Go Goal/RB1、B1 必须沿 Go fixed server”的实现约束，保留其 Task/Worker/Artifact、批准、独立验证、预算、幂等、取消、恢复和本地访问合同。ADR 0086 的关键澄清/答案与 preview revision 语义保留，不要求复刻其 Go 存储代码。ADR 0087 保持实验事实；固定订单、禁工具、一个活跃 Task、JSON 全量文件状态和仅前端恢复不进入正式合同。

旧 Go profile 与全部历史状态保持原格式、权限与恢复规则，不清空、不双写、不后台迁移。新 profile 仅用独立空数据根及不同格式标识，误指向旧根、部分初始化或不兼容格式时拒绝打开。U1 仍是独立后继；不是把两个 profile 接到同一根形成两套权威。

## 2. 产品与模块边界

一个本地服务、内置确定性 Supervisor、受管执行进程、一个 SQLite 权威库和制品目录。HTTP 依赖 Application 接口；Application 依赖 Store、Agent、Execution 和 Verification 接口，Core 不导入 Qwen/Pi/OpenCode。接口通过构造函数 DI，不新增网络服务或动态插件平台。

复用现有 HTTP 访问保护、错误/CAS/幂等测试、受管进程经验与独立验收反例。可移植代码直接复用；Go 资产不能直接执行时复用其行为合同和故障案例，不全量翻译旧实现。实验接口仍明确 experimental，不能以原九个 operation 代表完整 API。

正式 Task 接受目标、上下文和明确的交付/验收要求，经必要问答与一次精确计划确认后执行。小任务可以一个 Worker，需协作时采用有依赖的有限节点；不强制每个任务拆成两个作者，不新增 Workspace、资源注册、角色平台或 UI。

## 3. Agent 接入与 ACP

核心能力仍为接收输入、绑定执行、观察结束/失败、采集成果和有界停止；ACP 是首选可用 transport，不是所有 Adapter 的准入硬条件。版本及协议能力记录用于追溯和兼容匹配，不按精确 CLI 版本白名单拒绝。

Qwen 优先 `--acp` stdio。原生登录、配置、工具与 Skill 沿用仍受 ADR 0085 的 Worker/Publisher 边界约束：仅模型及已批准读取/验证的鉴权可沿用；作者可达 Publisher 凭据或已登录发布入口、无法证明分权的 profile 不进入正式支持，`publication:none` 也不例外。可以使用已有的独立执行用户/环境来落实，不新增身份平台，不删除用户登录；服务不复制 HOME 或鉴权文件。`serve` 的实验性 HTTP bridge 不自动成为正式依赖。初始化协商、session ID、request ID 与 Worker/Attempt 的映射由 Adapter 持有；Prompt 的终态响应而不是长连接进程退出标识回合结束。`end_turn` 仅证明 Agent 回合结束，不证明业务交付正确。

`session/update` 转换为有界进度/工具观察，不伪造缺失用量或思考过程。反向权限请求绑定当前会话、节点、批准范围及有效期；默认拒绝未批准操作。真实用户问题进入 Task questions/answers，不自动选择答案。客户端不提供 fs/terminal capability 不等于禁掉 Agent 的本地工具；普通同 UID 执行不是恶意代码沙箱，不宣称阻止所有 ambient credential 访问。

取消先提交持久意图/fence，再发协议 cancel；收到 cancelled 只证明回合收尾，目录释放仍需所属执行停止/安全空闲的实际证明。超时按持有的执行句柄停止，不按回放裸 PID 发信号。加载 Agent 历史只恢复上下文，不证明崩溃前命令成功，更不能直接接纳旧 generation 的结果。

## 4. 唯一事务与恢复

新 profile 的 SQLite 为唯一业务权威：Task/节点/Attempt 的事件及投影、revision、批准摘要、幂等键与请求摘要、预算预留、命令/outbox、结果和审计引用在同事务提交。外部 Agent/验证/文件操作不持数据库事务。SQLite 接缝必须通过精确格式、事务回滚与重开测试后才启用；不得静默退回实验 JSON Store。

每根一个当前 owner；启动、结果与取消均核对当前 generation 和状态。进程内记忆、Agent session、socket、日志或 PID 不构成第二真值。先耐久记录执行义务，再启动；丢响应或崩溃后的未决义务先核实，不直接重复派发/退款/增加预算。旧 writer 未证明失效时不得接管；同时必须提供受支持、可验证的恢复/诊断途径，不能把手工删锁作为正常运维。

制品按任务/执行归属采集、有界、不可变且带摘要；最终集成快照在作者之外验证。Decision 绑定精确输入、结果和验证摘要；Worker 不能自签验收，Publisher 仍是独立权限。未知外部效果不自动重试。有限返工只重做受影响节点，保留其余有效成果，累计期限/预算不重置。

## 5. API 与运行支持

沿 ADR 0085 目标补齐 Task plan/approve、graph/workers、questions/answers、operations、cancel/pause/resume、Artifact 下载及 audit/events。可以先轮询，不强制 SSE；所有已开放 operation 都有认证、对象/版本绑定、错误和幂等语义，未实现接口不在支持矩阵中冒充可用。

首发仍单节点、单用户、可信本地任务；默认 loopback、自动私有 token、Host/Origin 校验、有界请求/日志，远程首次开放仍先满足相应安全合同。生产 SQL 发布/云写入等副作用不是普通提示词授权，需独立支持声明与权限。

## 6. 正式发行，不以解释器豁免验证

Node profile 交付受保护、可追溯且摘要锁定的源码包/依赖与启动入口，在声明的固定 Node 运行时安装运行，不生成新的随机 native 文件。原签名/notarization 门禁按资产类别适用：若发行原生启动器、打包 runtime 或 addon，仍需其平台签名与授权；纯脚本包不伪称已获得 Apple notarization，也不为每个脚本重造原生执行流程。运行时安装合法性与企业允许执行由实机确认，不修改安全策略。

待发布同一资产必须通过实际部署、通用业务交付、下载消费、取消、整个服务重启/故障矩阵、连续多 Task、备份/恢复及声明的升级路径。Darwin 与 Linux 分别验收，Linux 不替代 Mac。明确支持的平台、Provider/能力与限制；满足 B2/API-STABLE/B3 后才可受保护发布 stable，不能凭文档或一次握手改成熟度。

## 7. 最短实施顺序

1. 复用独立 ACP transport，先 Fake 正反例，再真实 Qwen 握手/工具/权限/取消；不承担 Task 权威。
2. 单一 Application/Store 合同下，真实业务/ACP 与 SQLite/恢复并行；统一集成者处理共享状态，不让两路各建 Task 真值。
3. 同一 Node 候选经纯 HTTP 完成可信代码与零 Git 制品两类任务及必要交互/局部修正，关闭 API-STABLE；不等待第三品牌、UI 或旧历史导入。
4. 完成 B3 同资产实机、故障、运行和发行证据。停止扩展固定实验，而不是停止正式主线。
