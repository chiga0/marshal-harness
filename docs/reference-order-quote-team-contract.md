# 订单报价 Agent Team 参考契约（候选）

这是 B2 的合成业务样例与待确认契约，不是 B2 已实现或已接纳的 GoalPlan。执行前仍须把精确契约、oracle 摘要、基线和预算纳入 approved plan。不得用本文件或脚本直接创建业务 Run、独立 Decision 或发布权限。

## 交付物与并行范围

- 服务节点实现 `quote_api.py`：只在 `127.0.0.1` 指定端口提供 HTTP API，不访问外部网络或保存订单；复用已冻结的 B1 报价规则。
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

`scripts/order-quote-team-oracle.py` 先直接通过 HTTP 检查服务，再调用客户端检查同一服务的报价。覆盖运费阈值、多商品、零价、大整数、错误输入、错误 JSON、路由与输入不变。后继集成验证还必须独立观察客户端真实请求，并以不同响应 challenge 排除硬编码或客户端私自计算；本 oracle 当前不单独证明这一点。

测试使用本地合成 HTTP fixture，测试通过只说明验收基础设施可运行，不是实际 Agent Team 交付。真实 B2 仍缺 approved plan 耐久接纳/物化、调度、集成候选、独立 Decision、replan 和恢复；不新增第二个 controller 或账本。最终支持面保持可信单用户，普通 Python/HTTP 进程不是恶意代码沙箱。
