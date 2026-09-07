# Workspace Agent Team：API-first 实施 Milestone

日期：2026-09-07。依据 [产品架构](agent-team-service-architecture.md)与 [ADR 0085](adr/0085-agent-team-service-contract-and-storage.md)。保留 B1→B2→B3，不重置失败记录或另起一套“已经完成”的表；当前状态只见 [Roadmap](roadmap-status.md#业务交付当前表)。以下编号是阶段内验收项，不要求每项一 PR、一 Run 或一个 ADR。

0085 仍为 Proposed：本表规定目标出口，不单独授予新边界的默认启用/旧库迁移权限。旧 profile 继续用原合同，新候选按[适用性对照](design-contract-map.md)验证；旧函数/文件/AST/切片顺序不作为本表之外的强制待办。

## 1. 交付顺序与停止扩张线

主路径：**B1 Workspace/唯一应用组合/SQLite 原生 HTTP 单任务 → B2 持久交互/团队交付/三个 Agent/详情与审计 API → API-STABLE → B3 同路径可靠性与正式 API 发布。** UI-1 只在 API-STABLE 后启动，可与 B3 并行，但不阻塞 API 正式发布。新路径不先扩建 file-backed HTTP 再迁库重做；旧账本只用于回归/静止导入，旧库升级单列验收。

当前 Pi 三节点候选继续作为可复用资产和迁移回归，不为了新方案丢弃；只收口已知 blocker，不开展无界新 canary/模型轮换。其在旧文件路径上的成功只能记录对应路径的证据，不能代替新服务验收。单 Agent 完整交付必须先过，三套 Provider 增强功能不得串行挡住它。

PostgreSQL、HA、统一 Skill/登录、多节点控制面、可视化工作流编辑器、通用动态 DAG、自动 merge 均在首版外。旧 Marshal skill 完全不作为运行依赖、研发流程或验收标准，不加载/运行；原生 Agent Skill 与历史失败资产保留。技术 cleanup 只有阻断当前退出条件或重复故障时进入关键路径；不按文件数量拆分任务。

## 2. B1：可安装、可调用、可恢复的单任务服务

### B1-A Workspace、唯一组合与 API 合同

- 把实际 `sealedRepositoryApplication`/Pi 固定构造从 CLI 移到唯一应用组合；CLI、受保护 Unix socket 与新增认证 HTTP facade 调用相同 Application Port。
- 固定安装与轻量 Workspace 状态身份分离；`serve --workspace` 打开一个状态根/Store，可无 Git；新库直接 SQLite，不生成随机临时可执行文件。
- Workspace 只管理任务/配置/目录/制品/审计，不建设资源目录、RepositoryBinding、仓库注册 API 或跨 Workspace claim。仓库/表/平台走 Task prompt/context；通用执行只要求已确认输入、允许 profile 和独立目录，Git 特化留在适配层。
- B1 试用使用显式 `operator-local` 安装记录和 non-production/`publication:none` profile，producer 与当前对象校验按 ADR 0085 实施；B3 的 managed receipt/签名不提前宣称。原生模型登录可用，但执行环境不得携带 Publisher 凭据。
- 首个 Pi Adapter 通过注入运行；新增 Fake Adapter 能在不改变 Core 的情况下替换它，Import/构造测试证明无 Core 品牌分支。
- 同步实现有限 OpenAPI：Workspace、Task 提交/查询/批准、Run/Worker、operations、取消、输入与制品/交付；Task 映射现有 Goal，同 ID/revision/幂等，不并列两套可写生命周期。先定义最小请求回复并接同一纵切，不建立空 API、不开发 UI。

退出：零 Git Workspace 的 SQLite/owner、Task 创建/查询/幂等与独立执行目录通过检查；HTTP 真实 handler 与生成客户端合同一致，组合可注入 Fake/Pi 而不修改 Core；普通上下文路径不触发 HTTP 文件读取或权限扩大。B1-A 只是 B1 同一纵切的接线检查点，不单独宣传可用，也不要求先在旧账本重做一轮实机。

### B1-B SQLite 原生单任务完整交付与恢复

- 定义最小事务 Store Port，实现 authority events/CAS、幂等、预算与 outbox 同事务；复用原状态机，不为数据库重写业务规则。
- 一个真实 Provider 经同一 HTTP→Core→SQLite/Execution→Collect→独立 Verify/Decision→交付链运行；先完成零 Git 的 SQL/样例/说明制品，再验证已有 Git 编码路径复用，不制造两个内核。取消/超时、重启、丢响应、unknown 对账在同入口验证，不由客户端逐阶段驱动。
- 由下载接口取得精确 manifest/成果，在新目录按已声明输入/依赖（Git 另记录 base）重建并运行原业务 oracle；无需 UI、Marshal skill 或作者未声明的工作区状态。
- 覆盖 server crash 后旧 Worker 尚活、两个执行争用同一已分配目录、输入漂移和停止未知；未确认停止不能复用。无 Git 不应触发 Git init/base/worktree 准入；不承诺任意共享目录或外部表的全局排他。
- prompt/context refs、用量来源、业务事件从这里采集，避免到审计 API 才发现历史无数据。SQL 验收标明实际引擎/目标方言，样例绿不冒充生产执行或另一引擎兼容。
- 同步实现预算维度 enforcement/计量来源/unknown 结算与保守 debit；测试“无 usage 的任务成功→重启→后继任务”不会假退款、重复结算或永久占用执行槽。

退出：HTTP 单任务真实交付成功，重启后原 Run/Attempt/预算/候选/Decision 可查询与恢复；请求丢失不额外启动 Worker，长验证期间查询/取消有界；损坏、旧 owner、unknown 停止不形成第二 writer。B1-A/B1-B 一起通过才关闭 B1 的干净 Workspace 安装能力。

### B1-U 旧数据升级出口（不串行阻塞新空工作区）

逐显式旧来源合法收口所有非终态（含 REVIEW_PENDING）与未决效果，持原锁做一致备份/原字节验证；导入到 Workspace SQLite 的 `importedArchiveNamespace + 原ID`，跨仓库同名 Run 不冲突、不重签旧 Evidence/activation。切换前机械阻断该来源旧 writer；导入失败保留原库受控恢复，不清空/改名冒充新仓库。多个来源分次、精确、幂等导入，不把部分导入当全部成功。

退出：真实旧库导入/崩溃点/旧 binary 禁写/同名记录/引用和预算等价通过。该出口单独标记 `UPGRADE_SUPPORTED`；未通过时只能支持干净安装，不宣传升级可用。B3 正式承诺旧库升级前必须完成它，不为迁移迟迟未过重复重跑新团队。

## 3. B2：用户可以交给它一个实际业务需求

### B2-A 确认合同、持久交互与有限计划

- 简短意图→澄清→展示交付物/示例反例/权限/预算→精确确认；Plan 是 proposal，不是调度权限。
- 一等 UserInteraction：重复/过期/陈旧/取消答案、节点等待和全局 pause、服务重启后待答恢复；不复活 terminal Run。
- 人工业务验收项在批准合同中声明，绑定候选与 Evidence；未答/拒绝/过期不得生成成功 GoalOutcome。答复发送覆盖“已提交答案后取消”和“Agent 已收答但确认前崩溃”，未知不可盲重发。
- 复用有限 Goal 图/物化：支持单节点、串行、最多三个并行 implement 与显式集成；原预算与创建义务同事务。
- 一次批准冻结输入摘要、节点上下文、执行 profile/权限、依赖和验收。无关上下文修改不重做已接纳成果，冻结输入/权限改变只影响相应依赖；不增加通用资源 schema。Git 节点在适配层保持精确 base/独立 worktree。
- Planner 根据依赖和集成成本选择并行，禁止“每文件一节点”。先以固定业务模板验证，再允许同一有界 schema 的 proposal；不新增 DSL。

退出：纯 HTTP 完成意图→问答→精确批准，无需人工手写每个 Run；一次批准后服务自己推进。节点 A 待答时无依赖 B 可继续；全局暂停停新派发；问题/答案/operations 重启不丢、不重复消费，错误 Workspace/Task/旧 subject 不可答复或扩权。

### B2-B 真正的团队交付与有界返工

- 两个独立 Git 仓库（API 服务与客户端）通过 Task 上下文进入计划，不预注册。作者实际重叠执行→独立检查/集成候选→只读 bundle Verifier→业务 oracle→独立 Decision→GoalOutcome/可下载整套成果；Core 不新增 repo 类型分支。
- Supervisor 自主 Collect、Verify、评审排队和集成，不依赖聊天工具/脚本逐节点推动。语义 Reviewer 仍是有界评估者，不是调度器。
- 一次局部内容 rework、依赖变化和受限 replan：保留无关已接纳成果，累计预算不重置；同时提供非成功 GoalOutcome。
- 明确 `NO_CHANGE`：只有原批准合同允许验收型交付、且旧 Run 的 `allowNoChange`/诊断要求也满足时，才可在独立验收后引用已有精确成果；不假造改动，不把旧 Run 终态当 ACCEPTED。需要新增支持时与该纵切一起更改 Schema/测试。

退出：至少一条真实跨仓库并行交付，下载完整 manifest，在新目录恢复所有精确 base/candidate/artifact 并执行真实调用。缺仓库、错 base、伪同名产物或双方单测绿但协议不兼容必须拒绝；局部返工只重做受影响工作；必需用户验收成立后才成功。逐仓库可选 Draft PR 的 partial/unknown 与 bundle 成功分开，不承诺跨 Git 原子发布/自动回滚。

### B2-C 三个 Provider、详情与审计 API

- Pi、Qwen Code、OpenCode 各通过核心接入矩阵；至少一项混合 Provider 团队证明 Core 不依赖品牌。第二 Adapter、生成客户端与审计 API 可与 B2-A 并行，UI 尚不启动。
- 支持其原生配置/Skill；Auth 失败明确交给用户修复，不进入付费循环；不同配置/版本不混证据。核心能力缺失拒绝，增强能力缺失展示限制。
- HTTP 查询/SSE 提供 Task DAG、节点分工、Worker 的真实阶段/最后工具/新鲜度、问题/取消进度、交付物与审计；无工具事件的 Adapter 仍可完成任务，不以 UI 截图代替数据合同。
- 每个 Worker 的实际提交 prompt/context refs、时间、token 来源和覆盖、重试/rework、首审/验收分母可查询；敏感材料访问与保留受控。

退出：三个 Agent 各真实完成至少一个相同核心合同任务，含失败/取消；混合团队成功；未知 token/context 不写零或冒充完整；用户能从失败节点定位原因而不是只能等待。

### API-STABLE：启动 UI 的唯一前置

这不是正式发布标签，也不新增产品阶段；它是 B2 完成后的接口稳定检查点，必须同时满足：

1. OpenAPI、真实 handler、生成客户端的请求/回复/错误/分页/async operation 合同一致；preview revision、版本与弃用规则已公布，没有未处置的对外语义 P0/P1。
2. 全程仅经 Task HTTP 完成需求澄清/批准、零 Git 制品与单/多仓库交付、pause/resume/cancel、问答/验收、下载/审计；没有 child CLI、手改账本或 UI 私有入口补步骤。
3. B2-B 真实整套 bundle 和 B2-C 三 Provider/混合团队通过；不能用单仓库 Fake 代替。
4. 同 key 重放、异摘要 conflict、旧 revision/错误 Workspace/Task、权限/路径、事件重连/gap、unknown operation、取消/迟到结果与重启恢复反例通过。
5. 两种独立客户端（脚本 HTTP 与生成客户端）消费同一 API，SDK/示例不借内部包/文件读取推断成功；关键 endpoints 的延迟/队列/大小边界经过并发验收。

之后才可开发 UI-1；API-STABLE 不替代 B3 的部署/恢复/正式发布证据。

## 4. B3：长期运行与正式支持

- 在 B2 同一应用/存储/执行路径运行 fault matrix：创建、dispatch、问题/答案、结果接纳、Decision、集成、Outcome 与 effect 的提交前后 crash；旧 owner、重复/迟到结果、磁盘满、服务关闭、Agent 无响应、长 Verify、事件洪泛。
- 多次 Goal 长期运行、长历史查询/投影恢复、GC 与一致备份；坏 Run 不拖死整个服务，CPU/内存压力不无限扩容；停止结果未知不释放写 scope。
- 同一最终 bytes 的 Darwin 稳定安装/签名/notarization 和 Linux server 实机；不能把一个平台的开发 canary 当跨平台发布证据。
- 用户按文档在干净环境启动已声明的 Local profile，使用一个 Provider 完成业务交付并重启恢复；三个 Agent 的支持矩阵分别发布。部署在一台 VM/容器不宣称分布式执行或恶意代码隔离。
- 保护的 stable release gate、升级/不兼容版本拒绝、故障处置手册；无 signing/平台证据只能 prerelease，不授予 production。
- 对外支持矩阵明确轻量 Workspace、API 版本、各 Provider/profile、零 Git/Git 实际验收、干净安装与 B1-U 升级；UI 不作为 API `v1.0.0` 发布依赖。
- 原生工具的外部 SQL 发布/执行/补数仅在对应配置实测后列支持；提交未知不自动重跑、Agent 停止不表示远端 job 停止，外部效果必需验收不满足不能用文件交付代替。首版不为此先建通用资源/连接器平台。

退出：发布清单绑定 sourceHead、最终资产摘要、平台/profile、完整业务交付及同路径恢复证据；没有未处置 P0/P1、未知写入者或未说明的数据恢复风险。版本标签本身不关闭 B3。

### UI-1：API 稳定后的可选产品界面

仅在 API-STABLE 后启动；实现 Workspace/Task、上下文提交/确认/问答、DAG/Worker 详情、制品下载和审计。只调用公开 API，不能直读 DB/持内部 authority；同一业务示例与接口客户端结果一致。可与 B3 并行，但 UI 未完成不推迟 API 正式发布；不包含通用拖拽编排器。

## 5. 可直接执行的需求—验收矩阵

| 用户要求 | 验收项 | 必须观察到的反例 |
| --- | --- | --- |
| 轻量 Workspace/非 Git 与多仓库 | B1-A/B1-B/B2-B | 零 Git 正常交付；无资源注册也可编码；同执行目录双写拒绝；跨仓库 bundle 联调失败拒绝 |
| 一键 HTTP 服务和端口 | B1-A/B1-B | 未认证/跨 Origin、错误 Workspace、第二 writer 无法写入 |
| SQLite 与恢复/升级 | B1-B/B1-U/B3 | 旧 writer、损坏导入、同名历史、丢响应、未知结果不能重复启动 |
| Task API 与需求交付 | B1/B2 | Task=既有 Goal，同幂等/revision；不能直接 PATCH 成功；测试绿但需求不满足仍拒绝 |
| 原生工具/数据任务 | B1-B/B3 支持矩阵 | SQL 文件完成不等于补数完成；错误方言不算目标引擎验证；未知外部效果不自动重试 |
| 灵活 Provider/自带 Skill | B2-C | 无进度事件仍成功；无核心止损失败；Skill 不扩大发布权限 |
| AskUser | B2-A | 旧 revision 答案拒绝；普通答复不授权 publish；全局 pause 不被节点答复绕过 |
| 内置监督与取消 | B1-B/B2-B/B3 | 日志刷屏不延长 deadline；kill 请求成功但进程尚存不算取消完成 |
| DAG/Worker 详情 | B2-A/B2-C | 重试不覆盖历史，SSE gap 明示，实时观察不伪装最终验收 |
| 审计/上下文 | B1-B/B2-C | 失败计分母，未知计量非零值声明，脱敏材料不冒充完整模型请求 |
| 最终团队正确性 | B2-B/B3 | 并行结果各自绿但组合错误不得生成成功 GoalOutcome |
| API 先于 UI | API-STABLE/UI-1 | 无 UI 即可完成全部交付/操作恢复；UI 不出现旁路状态或发布阻塞 |
| 不使用 Marshal skill | B1/B2/B3 | 缺少旧 Skill 时全流程仍可运行；原生 Agent Skill 不被误删 |

## 6. 并行实施与效率控制

最多三个不冲突工作流并行推进，具体数量按宿主/Provider 和评审容量决定，而不是永久固定 Worker 数：

1. 主路径作者：应用组合、Store、owner/recovery 的纵切；这些共享提交边界只设一个写入 owner，不拆给三个作者竞改。
2. Adapter 作者：基于已冻结 Port 实现第二/第三 Adapter 及核心 conformance；Port 未冻结先做测试 fixture，不猜接口大写实现。
3. API/验证作者：OpenAPI 生成客户端、DAG/交互/审计 API 与业务 oracle；依赖应用合同，以 fixture 开发，合入接真实 API。API-STABLE 前不派 UI 作者，不将 mock 页面算交付。

Verifier/Reviewer 独立，作者不提供自己改动的权威通过证据。采用普通工程协作/子 Agent 辅助，一次聚合预检和 review，问题聚合修复；同类问题重复时先根因纠偏，不用无限 review 代替设计。这里不继承旧 Marshal skill 的强制 round/Run/微切片流程，也不以“忽略 skill”删掉产品的证据和权限语义。

预检集中检查实际调用链、API/schema、身份/配置、当前证据、路径/secret、验收纯度、超时/取消、重放和 mergeability。只跑命中范围测试，相关 race/vet/staticcheck 与集成回归按风险补齐；不让 Reviewer 首次发现可机器检测问题。审计发现要直接修改合同/测试一次，不通过连续微型 ADR/PR 增加往返。

## 7. 衡量结果，不衡量 PR

- 每个阶段报告用户退出条件、精确候选与证据、未决 blocker 和下一项业务动作；文档完成不计业务完成。
- 三个代表任务族各至少三次配对运行，使用相同起点、确认合同、oracle、模型/工具权限与资源预算；顺序交错，禁止只保留成功。分别比较直用一个 Agent、强 Lead＋SubAgents、Marshal 的适用路径。
- 至少一组从相同的简短需求开始，计入补充合同/oracle、澄清、确认、计划失败与人工准备时间；另设冻结合同实验区分规划和执行开销。事前记录为何选单/多 Worker，不能看结果后排除“不适用”样本。
- 记录总 wall time、人工介入、总 Attempt/新 Run、rework、实际费用与计量覆盖、首审/最终验收、无关成果复用。不同模型/配置实验分层，不合成一条“效率优势”。
- 目标是首审通过率 ≥80%、同结构性签名最多一次原样复发、完整失败成本可见；这些是过程告警，不以小样本达标宣称生产可靠。
- 若 Marshal 在质量相同下未改善恢复/审计/人工等待，且明显增加交付耗时，停止扩展调度花样，先删无收益环节；强耦合任务切回单 Worker。没有对比证据不承诺提速倍数。

本轮只产出和审计上述实施合同；没有执行新的付费实验、迁移现存 `.marshal` 或发布资产。
