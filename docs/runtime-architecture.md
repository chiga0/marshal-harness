# Runtime 架构

> 当前服务方案的实现投影，2026-09-07。ADR 0085 仍为 Proposed；本文不替代其接纳或实机门槛。历史合同适用性见[对照表](design-contract-map.md)，产品 API 与详细 matrix 见[服务架构](agent-team-service-architecture.md)。

## 单一写入与依赖方向

HTTP/CLI→Application Port→Core controllers→Store/Execution/Agent/Sandbox/Verification/Publication Port；唯一生产组合根注入实现。Core 不导入具体 Provider 品牌，不将 CLI handler 当应用层，不回落 legacy server/child CLI/通用可写账本 API。

一个 repository scope 同时只有一个 current owner。服务安装身份与业务仓库身份独立，canonical `.marshal` 仍是仓库状态根。目标 Store 是 SQLite WAL；现有 file-backed store 是旧运行路径，二者不为同 scope 双写，不从“哪个较新”选真值。

## 权威事务与执行顺序

| 时点 | 在同一 Store 事务中 | 事务外 |
| --- | --- | --- |
| 接受请求 | 授权后查幂等；新命令才做 expected revision/CAS；事件、投影、回执一起提交 | 响应可丢失，精确同请求只返回原回执 |
| 准备执行 | 重验 owner/输入/预算/scope，预留唯一 Attempt 与 execution obligation，记录 command intent/outbox | 按冻结身份启动/观察进程或调用 Provider |
| 接纳结果 | 重验 current owner/lease/generation、实际 outcome、Run head；原子记录接纳与合法 successor | 独立验证/工具/网络不能持全局数据库锁 |
| 停止执行 | 与结果接纳竞争 CAS；持久化 stop intent/fence | owned controller 有界停止/Inspect；未知不释放 writer |
| 结束与释放 | 核对 terminal/cleanup/allocation/stop 或正常完成的精确链；记 release、结算、Outcome | 回收已证明可清理的资源；用户 worktree 不擅自删除 |

数据库短事务取代新路径的跨文件 proof/shared-guard/固定 AST 形状，不取代真实副作用证明、唯一 producer 与 currentness。旧 schema/replay 不原地改写，字段级新协议在对应纵切与生产者/测试一起落地。

外部 outcome 未知时以原 commandId Inspect/Reconcile；没有查询/原生幂等证据就不盲目重发。进程已启动不等于结果已接纳，Agent 自报成功不等于 Run 成功。具体合同见 [ADR 0085 §4](adr/0085-agent-team-service-contract-and-storage.md#4-权威事务与-sqlite-切换)。

## 内置 Supervisor

一个 resident deterministic controller 从 durable obligations 与当前状态恢复调度，内存活跃索引/计时器可重建；不用用户不断调用 CLI 推进，也不新起权威 watchdog。

- 先处理已到期 deadline、未决停止/结果、review 队列，再补派 scope 互斥任务。
- 资源准入同时看可用内存、CPU、执行槽、Provider 限额、scope 与待审 WIP；不只看进程数。
- 原 deadline/预算不因重启、transport retry、局部 rework、successor 而刷新。
- Activity/tool event 是可选观察：无事件不能直接判死锁，有日志也不能延长硬 deadline 或宣称成功。
- 取消 API 接收业务 execution ID，不接收任意可 kill PID；结果先接纳则取消太晚，stop 先接纳则迟到结果隔离。

队列、日志、订阅、输出、重试与等待都有边界。取消/查询延迟必须在长 Verify 和多 Run 并发下实测；长任务不能持全局 writer lock。原 ADR 0067 的未知归属/permanent intervention 不因改存储变成自动解锁权限。

## 问答、DAG 与审计

Goal 是用户任务，有限 work node→Task→Run→Attempt；UI 阶段是投影，不另造权威生命周期。UserInteraction 冻结 subject/revision、待答节点、期限和批准范围；普通回答、计划批准、工具权限、交付验收不能互相替代。

节点待答只阻塞相关依赖，Goal `PAUSED`/cancel 优先停全图新派发。向 Agent 送答案通过 outbox，发送前重验 fence；ack 丢失无原生查询/幂等时保留 unknown，不因 Core 已消费就声称 Agent 只收一次。终态 Run 不因用户回答复活。

审计记录真实提交的 prompt/context 快照、可见事件、耗时、用量来源/覆盖、review/rework/验收与交付结果。不采集隐藏推理，不伪造 Agent 未暴露的全量上下文。强制预算维度与观察维度分开；未知 actual 用 nullable 与保守 debit，补充真实测量通过修正事件，不改旧事实、不把结算未知变成永久活 Worker。

## 存储、制品与恢复

SQLite 使用本地耐久磁盘、WAL、`synchronous=FULL` 和有界事务；事件、投影、幂等、预算、outbox 一次提交。高频遥测不与权威事件同等写放大。制品先耐久保存再提交引用，未引用对象只在证明安全后有界 GC。

首次迁移只导入已合法收口历史；包括 REVIEW_PENDING 在内的旧非终态不得自动 rebind。持原 owner 锁备份，保留 IDs/原 bytes/digest/引用/预算/幂等，验证等价后切唯一 store generation；旧入口必须机械拒绝新布局，仅写旧版本不认识的 marker 不够。切换后旧账本只读，不双写、不补签旧授权。

真正空的新仓库可先做 SQLite 原生完整业务纵切，不等待复杂历史导入；旧 `.marshal` 不能被清空或改名冒充新仓库。升级受支持前仍须通过旧库迁移与恢复验收。

备份使用一致快照和制品 manifest；恢复先只读，确认旧执行/owner 不能继续写及外部效果可对账后才开放变更。历史 replay 不执行 outbox；数据库恢复从不等于 Git/云端回滚。PostgreSQL 后续通过同一 Port/conformance 接入，不能增加第二真值。

## 验证与保留资产

复用现有状态 reducer、Evidence/Decision、process mechanics、权限和故障案例；把两账本/函数形状测试转为新 Store 接缝上的行为测试。核心矩阵覆盖 stale/forged result、启动/停止/响应丢失、预算单次消费、重复 writer、旧库拒绝与导入、问答后 cancel、交付下载重建。

旧实现的详细字段/锁序可查[历史 Runtime 参考](runtime-architecture-reference-2026-09-07.md)与相关 ADR；它们不是新服务的物理拆分或强制切片清单。验收必须穿过生产组合根，不能以文档、Fake、compile-only 或旧 profile 的证据升级新支持。
