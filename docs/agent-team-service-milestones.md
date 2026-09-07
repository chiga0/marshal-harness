# Agent Team 服务实施 Milestone

日期：2026-09-07。依据 [产品架构](agent-team-service-architecture.md)与 [ADR 0085](adr/0085-agent-team-service-contract-and-storage.md)。保留 B1→B2→B3，不重置失败记录或另起一套“已经完成”的表；当前状态只见 [Roadmap](roadmap-status.md#业务交付当前表)。以下编号是阶段内验收项，不要求每项一 PR、一 Run 或一个 ADR。

0085 仍为 Proposed：本表规定目标出口，不单独授予新边界的默认启用/旧库迁移权限。旧 profile 继续用原合同，新候选按[适用性对照](design-contract-map.md)验证；旧函数/文件/AST/切片顺序不作为本表之外的强制待办。

## 1. 交付顺序与停止扩张线

主路径：**B1 唯一应用组合 → 一个真实 Agent 的 HTTP 交付 → SQLite 单写与同路径恢复 → B2 交互/受限团队/三个 Agent/最小界面与复盘 → B3 同路径可靠性与正式发布。** 全新无旧 authority 的仓库可直接进入 SQLite 的最小需求/确认/单任务纵切；已有仓库的升级另验迁移，不让历史导入阻塞新业务试验，也不绕过它的正式升级门禁。

当前 Pi 三节点候选继续作为可复用资产和迁移回归，不为了新方案丢弃；只收口已知 blocker，不开展无界新 canary/模型轮换。其在旧文件路径上的成功只能记录对应路径的证据，不能代替新服务验收。单 Agent 完整交付必须先过，三套 Provider 增强功能不得串行挡住它。

PostgreSQL、HA、统一 Skill/登录、多节点控制面、可视化工作流编辑器、通用动态 DAG、自动 merge 均在首版外。技术 cleanup 只有阻断当前退出条件或重复故障时进入关键路径；不按文件数量拆分任务。

## 2. B1：可安装、可调用、可恢复的单任务服务

### B1-A 唯一组合与外部业务仓库

- 把实际 `sealedRepositoryApplication`/Pi 固定构造从 CLI 移到唯一应用组合；CLI、受保护 Unix socket 与新增认证 HTTP facade 调用相同 Application Port。
- 固定安装身份与外部仓库身份分离；仍 canonical `.marshal`、单 owner、锁定 base、独立 worktree；不生成随机临时可执行文件。
- B1 试用使用显式 `operator-local` 安装记录和 non-production/`publication:none` profile，producer 与当前对象校验按 ADR 0085 实施；B3 的 managed receipt/签名不提前宣称。原生模型登录可用，但执行环境不得携带 Publisher 凭据。
- 首个 Pi Adapter 通过注入运行；新增 Fake Adapter 能在不改变 Core 的情况下替换它，Import/构造测试证明无 Core 品牌分支。
- 同步实现有限 OpenAPI：提交、查询、运行详情、取消、制品/交付；严格区分异步 operation 与业务完成。先冻结请求/回复和错误码，再同一纵切接线，不建立全套空 API。

退出：在 Marshal 仓库以外的可信示例仓库，从一个 HTTP 客户端发起一次真实任务，经 Collect、独立 Verify/Decision 获取交付物；从正式下载接口取得精确成果，在新目录按声明依赖重建并运行原业务 oracle，不借作者工作区状态。请求重放无重复执行；当前路径与新 facade 共享同一权威。未迁库的这条证据只关闭 B1-A。

### B1-B SQLite 权威切换与最小恢复

- 定义最小事务 Store Port，实现 authority events/CAS、幂等、预算与 outbox 同事务；复用原状态机，不为数据库重写业务规则。
- 完成旧 RB1/Run journal 的静止导入、原始摘要验证、制品引用、单 owner/store generation、旧 writer 禁写与一致备份恢复。
- 首次旧仓库导入只接受原非终态全部合法收口的历史，不继续旧 REVIEW_PENDING/activation；新空仓库先独立试用，导入故障不导致删除旧账本或伪造收口。
- 单任务以 SQLite 为唯一真值再走 B1-A 的完整真实链；取消、超时、服务重启、丢响应和未知执行对账均在同一入口验证。
- prompt/context refs、用量来源、业务事件从这里开始采集，避免到复盘界面才发现历史无数据。
- 同步实现预算维度 enforcement/计量来源/unknown 结算与保守 debit；测试“无 usage 的任务成功→重启→后继任务”不会假退款、重复结算或永久占用执行槽。

退出：实际关闭并重启服务，原 Run/Attempt/预算/候选/Decision 可继续查询与恢复；提交/Start/取消 response loss 不额外启动 Worker；损坏、旧 owner、未知取消 fail closed；恢复没有第二 writer。只有 B1-A 与 B1-B 都通过才关闭新范围下的 B1。

## 3. B2：用户可以交给它一个实际业务需求

### B2-A 确认合同、持久交互与有限计划

- 简短意图→澄清→展示交付物/示例反例/权限/预算→精确确认；Plan 是 proposal，不是调度权限。
- 一等 UserInteraction：重复/过期/陈旧/取消答案、节点等待和全局 pause、服务重启后待答恢复；不复活 terminal Run。
- 人工业务验收项在批准合同中声明，绑定候选与 Evidence；未答/拒绝/过期不得生成成功 GoalOutcome。答复发送覆盖“已提交答案后取消”和“Agent 已收答但确认前崩溃”，未知不可盲重发。
- 复用有限 Goal 图/物化：支持单节点、串行、最多三个并行 implement 与显式集成；原预算与创建义务同事务。
- Planner 根据依赖和集成成本选择并行，禁止“每文件一节点”。先以固定业务模板验证，再允许同一有界 schema 的 proposal；不新增 DSL。

退出：无需人工手写每个 Run，一次批准后服务自己推进；节点 A 待用户回答时，无依赖的 B 可继续；全局暂停时两者都不能新派发；已提交问题与答案重启不丢、不重复消费。

### B2-B 真正的团队交付与有界返工

- 两个 scope 互斥作者实际重叠执行→各自独立检查→集成候选→端到端业务 oracle→独立 Decision→GoalOutcome/可下载交付物。
- Supervisor 自主 Collect、Verify、评审排队和集成，不依赖聊天工具/脚本逐节点推动。语义 Reviewer 仍是有界评估者，不是调度器。
- 一次局部内容 rework、依赖变化和受限 replan：保留无关已接纳成果，累计预算不重置；同时提供非成功 GoalOutcome。
- 明确 `NO_CHANGE`：只有原批准合同允许验收型交付、且旧 Run 的 `allowNoChange`/诊断要求也满足时，才可在独立验收后引用已有精确成果；不假造改动，不把旧 Run 终态当 ACCEPTED。需要新增支持时与该纵切一起更改 Schema/测试。

退出：至少一条真实并行交付，并从正式下载接口在新目录重建成果、执行真实业务 oracle；注入局部缺陷后只重做受影响工作；必需的具名用户验收成立后才成功。禁止作者修改 oracle、陈旧证据通过、集成未验收即成功、失败后全团队原样再跑。

### B2-C 三个 Provider、详情与复盘

- Pi、Qwen Code、OpenCode 各通过核心接入矩阵；至少一项混合 Provider 团队证明 Core 不依赖品牌。新增第二 Adapter 与界面可与 B2-A 并行。
- 支持其原生配置/Skill；Auth 失败明确交给用户修复，不进入付费循环；不同配置/版本不混证据。核心能力缺失拒绝，增强能力缺失展示限制。
- 最小界面显示任务 DAG、分工、Worker 的真实阶段/最后工具/新鲜度、问题与取消进度、交付物与复盘；无工具事件的 Adapter 仍可完成任务。
- 每个 Worker 的实际提交 prompt/context refs、时间、token 来源和覆盖、重试/rework、首审/验收分母可查询；敏感材料访问与保留受控。

退出：三个 Agent 各真实完成至少一个相同核心合同任务，含失败/取消；混合团队成功；未知 token/context 不写零或冒充完整；用户能从失败节点定位原因而不是只能等待。

## 4. B3：长期运行与正式支持

- 在 B2 同一应用/存储/执行路径运行 fault matrix：创建、dispatch、问题/答案、结果接纳、Decision、集成、Outcome 与 effect 的提交前后 crash；旧 owner、重复/迟到结果、磁盘满、服务关闭、Agent 无响应、长 Verify、事件洪泛。
- 多次 Goal 长期运行、长历史查询/投影恢复、GC 与一致备份；坏 Run 不拖死整个服务，CPU/内存压力不无限扩容；停止结果未知不释放写 scope。
- 同一最终 bytes 的 Darwin 稳定安装/签名/notarization 和 Linux server 实机；不能把一个平台的开发 canary 当跨平台发布证据。
- 用户按文档在干净环境启动已声明的 Local profile，使用一个 Provider 完成业务交付并重启恢复；三个 Agent 的支持矩阵分别发布。部署在一台 VM/容器不宣称分布式执行或恶意代码隔离。
- 保护的 stable release gate、升级/不兼容版本拒绝、故障处置手册；无 signing/平台证据只能 prerelease，不授予 production。

退出：发布清单绑定 sourceHead、最终资产摘要、平台/profile、完整业务交付及同路径恢复证据；没有未处置 P0/P1、未知写入者或未说明的数据恢复风险。版本标签本身不关闭 B3。

## 5. 可直接执行的需求—验收矩阵

| 用户要求 | 验收项 | 必须观察到的反例 |
| --- | --- | --- |
| 一键服务和端口 | B1-A | 未认证/跨 Origin、另一仓库 identity、第二 writer 无法写入 |
| SQLite 与恢复 | B1-B/B3 | 旧 writer、损坏导入、丢响应、外部结果未知不能重复启动 |
| 简单需求变交付 | B2-A/B2-B | 测试绿但确认需求不满足仍拒绝；交付物可实际启动 |
| 灵活 Provider/自带 Skill | B2-C | 无进度事件仍成功；无核心止损失败；Skill 不扩大发布权限 |
| AskUser | B2-A | 旧 revision 答案拒绝；普通答复不授权 publish；全局 pause 不被节点答复绕过 |
| 内置监督与取消 | B1-B/B2-B/B3 | 日志刷屏不延长 deadline；kill 请求成功但进程尚存不算取消完成 |
| DAG/Worker 详情 | B2-A/B2-C | 重试不覆盖历史，SSE gap 明示，实时观察不伪装最终验收 |
| 复盘/上下文 | B1-B/B2-C | 失败计分母，未知计量非零值声明，脱敏材料不冒充完整模型请求 |
| 最终团队正确性 | B2-B/B3 | 并行结果各自绿但组合错误不得生成成功 GoalOutcome |

## 6. 并行实施与效率控制

最多三个不冲突工作流并行推进，具体数量按宿主/Provider 和评审容量决定，而不是永久固定 Worker 数：

1. 主路径作者：应用组合、Store、owner/recovery 的纵切；这些共享提交边界只设一个写入 owner，不拆给三个作者竞改。
2. Adapter 作者：基于已冻结 Port 实现第二/第三 Adapter 及核心 conformance；Port 未冻结先做测试 fixture，不猜接口大写实现。
3. 产品投影作者：OpenAPI 客户端、DAG/交互/审计页面与业务 oracle；依赖应用合同，以 fixture 开发，合入必须接真实 API，不能将 mock 界面算交付。

Verifier/Reviewer 独立，作者不提供自己改动的权威通过证据。一次聚合预检→一次完整 review→至多一次 aggregate rework；第二次同类 P0/P1 停该切片、归档原因并改计划，不让无限 review 代替设计。此规则不是省掉必要修复，也不要求每一行变更开一个新 Run。

预检集中检查实际调用链、API/schema、身份/配置、当前证据、路径/secret、验收纯度、超时/取消、重放和 mergeability。只跑命中范围测试，相关 race/vet/staticcheck 与集成回归按风险补齐；不让 Reviewer 首次发现可机器检测问题。审计发现要直接修改合同/测试一次，不通过连续微型 ADR/PR 增加往返。

## 7. 衡量结果，不衡量 PR

- 每个阶段报告用户退出条件、精确候选与证据、未决 blocker 和下一项业务动作；文档完成不计业务完成。
- 三个代表任务族各至少三次配对运行，使用相同起点、确认合同、oracle、模型/工具权限与资源预算；顺序交错，禁止只保留成功。分别比较直用一个 Agent、强 Lead＋SubAgents、Marshal 的适用路径。
- 至少一组从相同的简短需求开始，计入补充合同/oracle、澄清、确认、计划失败与人工准备时间；另设冻结合同实验区分规划和执行开销。事前记录为何选单/多 Worker，不能看结果后排除“不适用”样本。
- 记录总 wall time、人工介入、总 Attempt/新 Run、rework、实际费用与计量覆盖、首审/最终验收、无关成果复用。不同模型/配置实验分层，不合成一条“效率优势”。
- 目标是首审通过率 ≥80%、同结构性签名最多一次原样复发、完整失败成本可见；这些是过程告警，不以小样本达标宣称生产可靠。
- 若 Marshal 在质量相同下未改善恢复/审计/人工等待，且明显增加交付耗时，停止扩展调度花样，先删无收益环节；强耦合任务切回单 Worker。没有对比证据不承诺提速倍数。

本轮只产出和审计上述实施合同；没有执行新的付费实验、迁移现存 `.marshal` 或发布资产。
