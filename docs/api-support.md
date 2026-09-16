# 当前 API 支持与证据矩阵

更新：2026-09-15；只读核对基线 `12db5f908e1a15331e4edb06e9db3cd8c8c16d80`；增量：事件流 SSE(task.events.stream)已接线并有单元+服务级测试,未重新执行模型或全部历史测试。[标准API](standard-api.md)给出契约，[OpenAPI](../packages/task-api/openapi.json)为28操作/67Schema唯一机器定义(27项 single-shot + 1项 SSE 传输面,openAPI 版本自 66Schema 起计);[Roadmap](roadmap-status.md#业务交付当前表)保持最新完成状态和精确证据权威。[旧25操作矩阵](node-api-support-matrix.md)是9月9日历史快照，不再用其未完成项代表今天。

当前stable为v1.0.2，包含其声明的可信配置服务，不含UI和默认通用团队；v1.1.0-rc.2已公开预发布，含默认通用文件团队和可执行ACP入口。RC.2 source为`2fde5038`，安装及模型证据见[发行记录](v1.1.0-rc.2-release-dossier-2026-09-14.md)。新源码不自动进入旧资产，表内能力按所启用配置判断，不能把所有profile能力相加视为一个默认配置。

## 配置与证据读法

- 基础：唯一Application/SQLite与可信业务/验证配置；默认通用：RC.2的v7文件团队，publication为空。
- 澄清：批准前可信有限ClarificationPort；运行问答：原Provider、业务和验证都支持的runtime question配置。默认通用的Leader问答不等于所有原Worker均可问答。
- 修正：原HTTP repair需受信repair配置、原客观负Decision；Leader自治修正按其原批准策略，不能互换。
- 发布：配置固定本机报告目标、原授权和独立后验；不支持通用生产ETL。
- “已接线”说明实际Application分支存在；“条件”说明缺配置返回明确错误。每行列相关测试文件用于定位现有检查，真实证据归于下方来源，不把测试文件存在记成本次通过。

## 28项支持

| 操作 | 实现与启用条件 | 验证入口与限制 |
| --- | --- | --- |
| health.get | 已接线，所有服务 | [composition测试](../packages/task-service/composition.test.ts)；只说明HTTP存活 |
| ready.get | 已接线，owner及原恢复义务满足 | [custody恢复](../packages/task-service/custody-recovery.test.ts)；不探测模型登录 |
| input.create | 已接线，Depot | [制品测试](../packages/task-application/artifacts.test.ts)；原bytes≤256KiB |
| task.create | 已接线，可信业务与预算 | [HTTP/SQLite](../packages/task-application/http-integration.test.ts)；通用文件真实Qwen见RC.2 |
| task.list | 已接线，原库分页 | [Application测试](../packages/task-application/application.test.ts)；无全文检索/跨用户目录 |
| task.get | 已接线，真实投影 | [Leader团队](../packages/task-service/leader.test.ts)；按allowedActions操作 |
| task.plan | 已接线，计划形成后 | [HTTP/SQLite](../packages/task-application/http-integration.test.ts)；GET不生成计划 |
| task.approve | 已接线，精确计划版本/摘要 | [HTTP/SQLite](../packages/task-application/http-integration.test.ts)；202不是交付 |
| task.graph | 已接线，计划形成后 | [Application测试](../packages/task-application/application.test.ts)；只读DAG |
| task.workers | 已接线，原Worker分页 | [执行测试](../packages/task-application/execution.test.ts)；包含受管语义执行，usage可未知 |
| worker.get | 已接线，所属执行投影 | [执行测试](../packages/task-application/execution.test.ts)；不提供任意进程管理 |
| worker.cancel | 条件：v6或v7根；旧格式501 | [取消测试](../packages/task-application/worker-cancellation.test.ts)；单目标停止与Task停止分开 |
| task.questions | 已接线，历史问题可读 | [澄清](../packages/task-application/clarification.test.ts)、[运行问题](../packages/task-application/runtime-questions.test.ts)；空列表不证明写入能力 |
| task.answer | 条件：对应澄清或运行问答Port | [运行问答恢复](../packages/task-service/runtime-question-recovery.test.ts)；原receipt与实际ACK分开 |
| task.leader | 条件：v7 Leader配置 | [Leader测试](../packages/task-application/leader.test.ts)；默认通用启用，旧profile501 |
| task.leader.reply | 条件：v7原有效请求与摘要 | [Leader测试](../packages/task-application/leader.test.ts)；answer和allow/deny互斥，不模拟Worker ACK |
| operation.get | 已接线，原持久Operation | [执行测试](../packages/task-application/execution.test.ts)；操作完成不等于业务完成 |
| task.cancel | 已接线，原状态允许 | [控制提交恢复](../packages/task-service/control-commit-recovery.test.ts)；未知cleanup不成功 |
| task.pause | 已接线，只停止新准入 | [执行测试](../packages/task-application/execution.test.ts)；不暂停现有进程/期限，未据此单列全配置真实模型证明 |
| task.resume | 已接线，合法paused及原期限 | [执行测试](../packages/task-application/execution.test.ts)；不是原会话crash attach |
| task.repair | 条件：显式repair配置、客观负Decision | [同计划修正](../packages/task-service/same-plan-repair.test.ts)；真实fog4见Roadmap，不外推默认Leader自治接口 |
| artifact.get | 已接线，原manifest/Depot | [制品测试](../packages/task-application/artifacts.test.ts)；损坏ready对象明确失败 |
| artifact.content | 已接线，摘要长度重验 | [独立客户端](../packages/task-client/index.test.ts)；≤8MiB，不是任意文件下载 |
| task.audit | 已接线，已有原证据 | [输入审计](../packages/task-service/input-audit.test.ts)；token/cost/首审计量仍unavailable |
| task.events | 已接线，sequence分页 | [服务行为](../packages/task-service/api-stable-behavior.test.ts)；轮询同一权威序列,完整transcript仍不承诺 |
| task.events.stream | 已接线,SSE 推送同一权威序列 | [单元](../packages/task-api/events-stream.test.ts)、[服务级](../packages/task-service/events-stream.test.ts)；Last-Event-ID 续传,断档须以轮询重对齐,凭据仅 Authorization 头,不平行推断 |
| provider.list | 已接线，冻结Provider facts | [composition测试](../packages/task-service/composition.test.ts)；默认unknown/空能力，不能当登录成功 |
| supervisor.get | 已接线，有界容量观察 | [服务行为](../packages/task-service/api-stable-behavior.test.ts)；扫描超界unavailable，不虚报全量 |

## 已有整链证据与未覆盖范围

[Roadmap](roadmap-status.md#业务交付当前表)记录B1/B2/B2-L/B3在声明支持范围内PASSED：包括真实Pi Leader/有限报告发布后验、真实内容失败后的fog4局部修正、声明面故障恢复、Linux同资产消费与有界soak。原源码、配置、成本和失败记录不改绑到本页基线。RC.2真实Qwen用同一通用装配完成双作者→独立Review/验证→文件交付→conclude，152.7秒、9 Attempts、零retry/rework；首次重复deliver失败仍保留。公开安装/升级及UI就绪不等于再跑一次模型。

真实ETL发布/补数/生产结果核验未接通，任意远程Agent冷恢复未证明，通用文件成果未执行不声称业务外部成功。UI功能可靠性及视觉交互为PARTIAL，真人可用性NOT_RUN，精确补验见[UI验收](ui-1/release-validation-2026-09-11.md#当前结论)。这里没有重跑或升级这些状态。

## 兼容承诺

历史API-STABLE检查点只覆盖当时25操作/58Schema及配套客户端；当前机器文件仍标0.1.0-candidate。本页不把旧检查点扩大到所有新增语义，也不撤回其通过事实。正式发行资产可包含经过其声明范围验收的后继接口，但消费者仍需匹配该发行的OpenAPI、客户端、数据layout与业务配置。新增严格字段/枚举、模型wire、配置身份及Store格式分别审查，不能以产品semver替代。

后继ADR0101只改变显式新通用Leader配置的模型建议格式，不改变27个HTTP操作。该配置的实现、模型验证与发行状态必须单列；不能据设计Accepted宣称默认RC.2已包含。未来扩展按[扩展契约](extension-contracts.md)提供实际能力证据后进入本表，未支持功能返回501或任务不支持错误，不用示例成功对象占位。
