# 当前 API 支持与证据矩阵

更新：2026-09-15；本批真实动态验收基线为 `a0854099b9e245e51cb0d66a5b058452eeeef692`，当前文档与合同候选以 [PR #315 当前 head](https://github.com/chiga0/marshal-harness/pull/315) 为准；不将历史证据重绑到新候选。[标准API](standard-api.md)给出人类可读契约，[OpenAPI](../packages/task-api/openapi.json)为唯一机器定义，操作和 Schema 数量由[HTTP参考](api/http-reference.md)生成；[Roadmap](roadmap-status.md#业务交付当前表)保持最新完成状态和精确证据权威。[旧25操作矩阵](node-api-support-matrix.md)是9月9日历史快照，不再用其未完成项代表今天。

当前stable为v1.0.2，包含其声明的可信配置服务，不含UI和默认通用团队；v1.1.0-rc.2已公开预发布，含默认通用文件团队和可执行ACP入口。RC.2 source为`2fde5038`，安装及模型证据见[发行记录](v1.1.0-rc.2-release-dossier-2026-09-14.md)。新源码不自动进入旧资产，表内能力按所启用配置判断，不能把所有profile能力相加视为一个默认配置。

## 配置与证据读法

- 基础：唯一Application/SQLite与可信业务/验证配置；默认通用：RC.2的v7文件团队，publication为空。
- 澄清：批准前可信有限ClarificationPort；运行问答：原Provider、业务和验证都支持的runtime question配置。默认通用的Leader问答不等于所有原Worker均可问答。
- 修正：原HTTP repair需受信repair配置、原客观负Decision；Leader自治修正按其原批准策略，不能互换。
- 发布：配置固定本机报告目标、原授权和独立后验；不支持通用生产ETL。
- 观察：[ADR0103](adr/0103-execution-observability.md) 显式启用实际输入、公开活动、模型和用量。关闭配置/旧记录不因此自动补全；新源码未自动进入旧发行包。
- 格式纠错：[ADR0104](adr/0104-bounded-leader-protocol-correction.md) 在新 Qwen 配置显式启用；Task 级最多一次，`LeaderView.protocolCorrection` 为条件性只读投影，无通用重试 API。旧配置保持关闭；语法纠错不包含业务拒收、权限失败或模型空输出。
- Qwen 工具目录：新配置将同一原生 Agent 分为作者 Provider 与受管编排/评审 Provider，后者排除内置文件工具。当前 Qwen 0.23.2 的原生注册表已核对；不代表任意扩展工具、未来版本或操作系统沙箱的隔离证明。
- “已接线”说明实际Application分支存在；“条件”说明缺配置返回明确错误。每行列相关测试文件用于定位现有检查，真实证据归于下方来源，不把测试文件存在记成本次通过。

## 当前 API 支持

| 操作 | 实现与启用条件 | 验证入口与限制 |
| --- | --- | --- |
| health.get | 已接线，所有服务 | [composition测试](../packages/task-service/composition.test.mjs)；只说明HTTP存活 |
| ready.get | 已接线，owner及原恢复义务满足 | [custody恢复](../packages/task-service/custody-recovery.test.mjs)；不探测模型登录 |
| input.create | 已接线，Depot | [制品测试](../packages/task-application/artifacts.test.mjs)；原bytes≤256KiB |
| task.create | 已接线，可信业务与预算 | [HTTP/SQLite](../packages/task-application/http-integration.test.mjs)；通用文件真实Qwen见RC.2 |
| task.list | 已接线，原库分页 | [Application测试](../packages/task-application/application.test.mjs)；无全文检索/跨用户目录 |
| task.get | 已接线，真实投影 | [Leader团队](../packages/task-service/leader.test.mjs)；按allowedActions操作 |
| task.plan | 已接线，计划形成后 | [HTTP/SQLite](../packages/task-application/http-integration.test.mjs)；GET不生成计划 |
| task.approve | 已接线，精确计划版本/摘要 | [HTTP/SQLite](../packages/task-application/http-integration.test.mjs)；202不是交付 |
| task.graph | 已接线，计划形成后 | [Application测试](../packages/task-application/application.test.mjs)；只读DAG |
| task.workers | 已接线，原Worker分页 | [执行测试](../packages/task-application/execution.test.mjs)；包含受管语义执行，usage可未知 |
| worker.get | 已接线，所属执行投影 | [执行测试](../packages/task-application/execution.test.mjs)；不提供任意进程管理 |
| worker.cancel | 条件：v6或v7根；旧格式501 | [取消测试](../packages/task-application/worker-cancellation.test.mjs)；单目标停止与Task停止分开 |
| task.questions | 已接线，历史问题可读 | [澄清](../packages/task-application/clarification.test.mjs)、[运行问题](../packages/task-application/runtime-questions.test.mjs)；空列表不证明写入能力 |
| task.answer | 条件：对应澄清或运行问答Port | [运行问答恢复](../packages/task-service/runtime-question-recovery.test.mjs)；原receipt与实际ACK分开 |
| task.leader | 条件：v7 Leader配置 | [Leader测试](../packages/task-application/leader.test.mjs)；默认通用启用，旧profile501 |
| task.leader.reply | 条件：v7原有效请求与摘要 | [Leader测试](../packages/task-application/leader.test.mjs)；answer和allow/deny互斥，不模拟Worker ACK |
| operation.get | 已接线，原持久Operation | [执行测试](../packages/task-application/execution.test.mjs)；操作完成不等于业务完成 |
| task.cancel | 已接线，原状态允许 | [控制提交恢复](../packages/task-service/control-commit-recovery.test.mjs)；未知cleanup不成功 |
| task.pause | 已接线，只停止新准入 | [执行测试](../packages/task-application/execution.test.mjs)；不暂停现有进程/期限，未据此单列全配置真实模型证明 |
| task.resume | 已接线，合法paused及原期限 | [执行测试](../packages/task-application/execution.test.mjs)；不是原会话crash attach |
| task.repair | 条件：显式repair配置、客观负Decision | [同计划修正](../packages/task-service/same-plan-repair.test.mjs)；真实fog4见Roadmap，不外推默认Leader自治接口 |
| artifact.get | 已接线，原manifest/Depot | [制品测试](../packages/task-application/artifacts.test.mjs)；损坏ready对象明确失败 |
| artifact.content | 已接线，摘要长度重验 | [独立客户端](../packages/task-client/index.test.mjs)；≤8MiB，不是任意文件下载 |
| task.audit | 已接线，已有原证据 | [输入审计](../packages/task-service/input-audit.test.mjs)；显式观察可含Provider累计Token及覆盖，费用/首审计量仍未知；Qwen最近响应独立且不计累计 |
| task.events | 已接线，sequence分页 | [服务行为](../packages/task-service/api-stable-behavior.test.mjs)；轮询，无SSE/完整transcript |
| provider.list | 已接线，冻结Provider facts | [composition测试](../packages/task-service/composition.test.mjs)；默认unknown/空能力，不能当登录成功 |
| supervisor.get | 已接线，有界容量观察 | [服务行为](../packages/task-service/api-stable-behavior.test.mjs)；扫描超界unavailable，不虚报全量 |

## 已有整链证据与未覆盖范围

[Roadmap](roadmap-status.md#业务交付当前表)记录 B1/B2/B2-L/B3 在各自声明的 API、配置、恢复和发行出口内的状态；这些状态不覆盖当前 S01→M01→C01 业务正确性验收，也不代表任意业务任务结果正确。真实 Pi Leader/有限报告发布后验、真实内容失败后的 fog4 局部修正、声明面故障恢复、Linux 同资产消费与有界 soak 等证据仍按原范围记录。原源码、配置、成本和失败记录不改绑到本页基线。RC.2 真实 Qwen 用同一通用装配完成双作者→独立 Review/验证→文件交付→conclude，152.7 秒、9 Attempts、零 retry/rework；首次重复 deliver 失败仍保留。公开安装/升级及 UI 就绪不等于再跑一次模型。

真实ETL发布/补数/生产结果核验未接通，任意远程Agent冷恢复未证明，通用文件成果未执行不声称业务外部成功。UI功能可靠性及视觉交互为PARTIAL，真人可用性NOT_RUN，精确补验见[UI验收](ui-1/release-validation-2026-09-11.md#当前结论)。这里没有重跑或升级这些状态。

## 兼容承诺

历史API-STABLE检查点只覆盖当时25操作/58Schema及配套客户端；当前机器文件仍标0.1.0-candidate。本页不把旧检查点扩大到所有新增语义，也不撤回其通过事实。正式发行资产可包含经过其声明范围验收的后继接口，但消费者仍需匹配该发行的OpenAPI、客户端、数据layout与业务配置。新增严格字段/枚举、模型wire、配置身份及Store格式分别审查，不能以产品semver替代。

后继ADR0101只改变显式新通用Leader配置的模型建议格式，不改变27个HTTP操作。该配置的实现、模型验证与发行状态必须单列；不能据设计Accepted宣称默认RC.2已包含。未来扩展按[扩展契约](extension-contracts.md)提供实际能力证据后进入本表，未支持功能返回501或任务不支持错误，不用示例成功对象占位。
