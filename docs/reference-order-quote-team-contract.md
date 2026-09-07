# 订单报价 Agent Team 参考契约（候选）

这是 B2 的合成业务样例与待确认契约，不是 B2 已实现或已接纳的 GoalPlan。执行前仍须把精确契约、oracle 摘要、基线和预算纳入 approved plan。不得用本文件或脚本直接创建业务 Run、独立 Decision 或发布权限。

## 交付物与并行范围

- 服务节点实现 `quote_api.py`：导出 `create_server(host, port)`，仅接受 `host=127.0.0.1`、支持 `port=0`，返回已绑定但尚未调用 `serve_forever` 的标准库 `HTTPServer`，导入无副作用。调用者负责启动与关闭，不访问外部网络或保存订单；复用已冻结的 B1 报价规则。
- 客户端节点实现 `quote_client.py`，导出 `quote_order(base_url, items)`：调用该服务、返回精确报价对象；无效订单抛出 `ValueError`，连接失败不返回伪造报价。不能在本地重算报价冒充调用成功。
- 两个节点各自独立 worktree；共享契约先冻结，互不改对方文件。集成节点消费精确候选后，启动同一个服务并运行客户端到服务的验收；子任务 ACCEPTED 不能替代集成交付。

## HTTP 契约

`POST /quote`，`Content-Type: application/json`，body 只有 `items` 一项。商品规则沿用 [B1 业务计划](agent-team-delivery-plan.md)：非空商品列表、非负整数分单价、正整数数量、拒绝布尔/浮点/额外字段；小计达到 5000 分免运费，否则运费 500 分；无输入改写。

| 情况 | HTTP 状态 | JSON |
| --- | --- | --- |
| 有效订单 | `200` | 精确 `subtotal_cents`、`shipping_cents`、`total_cents` 三个整数分字段 |
| JSON 语法错误 | `400` | `{"error":"invalid-json"}` |
| JSON 合法但订单结构/值错误 | `422` | `{"error":"invalid-order"}` |
| 未知路径 | `404` | `{"error":"not-found"}` |

全部响应使用 `application/json`。服务启动、端口分配、整体 deadline、停止与制品观察由后继 B2 集成执行器管理，不由 Worker 或本 oracle 授权。客户端不得跟随重定向访问任意地址；当前样例只允许明确的 loopback endpoint。

## 独立验收与诚实边界

`scripts/order-quote-team-oracle.py` 先直接通过 HTTP 检查服务，再调用客户端检查同一服务的报价。覆盖运费阈值、多商品、零价、大整数、错误输入、错误 JSON、路由与输入不变。随后使用验证者自己的 loopback HTTP fixture 观察精确 POST body，并提供不同的 200 报价与 422 响应，排除纯本地重算、发请求但忽略响应、未发请求的硬编码与错误请求体。客户端消费服务响应，不重复实现服务端定价；fixture 的不同报价只检验响应传递，不作为服务定价正确的证据。首轮实机审查后补齐既有契约的反例：200 非对象、缺字段、额外字段、布尔/浮点金额必须拒绝；含空 userinfo 的 URL 必须在请求前拒绝。当前组合共 33 项观察（客户端独立模式 10 项），不替代真实团队候选/独立 Decision，外层执行器仍必须提供整体 deadline；同 UID 下的 HTTP observer 不是对恶意客户端的隔离证明。

这些反例来自 [首轮真实候选审查](audit-b2-first-team-2026-09-07.md)，不是追加新的定价规则。新 oracle 摘要及澄清后的客户端提示必须进入新的明确批准，不能改写旧 frozen Task、VerificationReport 或将复查结果冒充原 Run 的权威证据；不自动重跑整个团队。

测试使用本地合成 HTTP fixture，测试通过只说明验收基础设施可运行，不是实际 Agent Team 交付。真实 B2 仍缺 approved plan 耐久接纳/物化、调度、集成候选、独立 Decision、replan 和恢复；不新增第二个 controller 或账本。最终支持面保持可信单用户，普通 Python/HTTP 进程不是恶意代码沙箱。

上述缺口列表为原始样例检查点，最新实现状态以 [Roadmap](roadmap-status.md#业务交付当前表) 为准。候选现提供 `fixed-server-team-inputs.py` 生成完整三节点请求；每个节点使用固定 oracle 摘要，`--api quote_api.py` 只验证服务、`--client quote_client.py` 只做客户端请求/响应 challenge，两参数同时提供才验证实际组合。外层 verification command 限时 30 秒；oracle 不在 server 进程内执行，也不创建业务 Run。候选代码 `SystemExit(0)` 不能被当成验收通过，异常正文不写入诊断。该 Python 进程仍不是恶意代码隔离环境。
