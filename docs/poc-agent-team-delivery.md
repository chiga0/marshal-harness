# 多 Agent 协作交付 PoC

## 演示目标与当前状态

让观众看到一项业务需求经过两个实现 Agent 分工、第三个 Agent 集成、独立验收后，得到真正可以运行的交付物，而不是三个聊天窗口或仅有任务状态。

**当前尚未完成演示验收。** 结果定界修复 `798ea395abd97744cfc69d125ee997933dad06f9` 的 [CI 34095940005](https://github.com/chiga0/marshal-harness/actions/runs/34095940005) 五项全绿。随后两次实机仍未完成团队交付：`qwen3.8-max` 的 [34097645547](https://github.com/chiga0/marshal-harness/actions/runs/34097645547) 在模型终态失败；明确替代为已配置 `qwen3.8-flash` 的 [34098369837](https://github.com/chiga0/marshal-harness/actions/runs/34098369837) 中，服务端代码完成 Collect/Verify、33 项检查通过并生成 ReviewPacket，但客户端在同类模型终态失败。没有任何本轮独立 ACCEPTED、第三节点或 GoalOutcome，不混成同模型成功证据。

服务端归档包含 109 行真实代码，WorkerResult 报告 inputTokens=56917、cachedInputTokens=164928、outputTokens=25456；这些是该节点观察到的模型报告，不是整组总费用，也不应把 cached 与 input 任意相加解释。原始失败终态未归档，旧诊断不能区分 length/error/aborted。已停止模型轮换；后继只补闭集终态诊断，并把任务说明收敛到批准文件、简短结果及由独立 verifier 实际验收，不继续探索全仓或模拟测试。下面是冻结的演示流程，不是已经通过的业务证据。

## 一项需求，三份职责

需求：开发一个订单报价 HTTP 服务及 Python 客户端。金额使用整数分，小计不足 5000 分收取 500 分运费，否则免运费；非法订单返回 422。

| 节点 | 交付物 | 依赖与验收 |
| --- | --- | --- |
| service | `quote_api.py` | 独立 worktree；真实 HTTP、价格边界与非法输入检查 |
| client | `quote_client.py` | 独立 worktree；真实请求、响应结构、网络/协议异常检查 |
| integration | 两份最终代码与 `quote_delivery.json` | 等待两个原始 ACCEPTED，基于其真实 patch 组合；独立执行同服务 HTTP 集成验收 |

三个角色均由真实 Pi 执行。前两个节点可并行；第三个有明确依赖，不能为了显示并发提前执行。进程时间重叠须从实际执行证据确认，不能只凭 RUNNING 状态声称。

## 五分钟介绍顺序

1. 展示已批准的需求、三个节点及互斥写范围。解释为什么接口约定先冻结，而实现可以并行。
2. 展示两份不同 Run 的真实 patch、测试结果及各自独立 Decision。说明 Worker 自报成功不等于通过。
3. 展示第三节点使用的上游摘要与集成结果，证明不是另起一个 Agent 重写全部代码。
4. 启动交付代码，现场发出一笔 HTTP 报价请求；再展示一次非法订单被拒绝。
5. 查询完成后的耐久 GoalOutcome，对照三个原始 Run/Decision 和交付摘要。说明重复查询不会再次启动 Agent。

现场演示可使用已经独立验收的交付包，并明确它是一次已完成运行的结果；不要把录制/历史证据伪装成现场 Agent 正在执行。

## 交付包运行方法（待真实验收后提供包与摘要）

交付包须包含三个业务文件、冻结版本的独立 oracle、需求与证据索引。下面命令在交付目录执行，需要 Python 3；它们只运行演示业务，不操作 Marshal 的状态目录。

终端一：

```sh
python3 -c 'from quote_api import create_server; create_server("127.0.0.1", 8087).serve_forever()'
```

终端二：

```sh
python3 -c 'from quote_client import quote_order; print(quote_order("http://127.0.0.1:8087", [{"unit_price_cents":1200,"quantity":2}]))'
curl --fail-with-body -sS http://127.0.0.1:8087/quote \
  -H 'Content-Type: application/json' \
  --data '{"items":[{"unit_price_cents":1200,"quantity":2}]}'
```

预期报价：`subtotal_cents=2400`、`shipping_cents=500`、`total_cents=2900`。完整独立复验：

```sh
python3 order-quote-team-oracle.py --api quote_api.py --client quote_client.py --delivery quote_delivery.json
```

## 演示前必须填齐

- [ ] exact sourceHead、候选二进制摘要、CI 与真实运行链接。
- [ ] 两个实现及一个集成 Run 的独立 ACCEPTED 与精确 ReviewPacket 摘要。
- [ ] 原始上游成果组合与最终 GoalOutcome 账本事实；不能用清单或测试输出代替。
- [ ] 交付包下载地址、SHA-256，以及解包后上述命令的实际复验结果。
- [ ] 实际耗时、失败 Attempt、人工评审等待如实披露；未测 token/compute 不写零。

## 可以与不可以承诺

通过后可以介绍：单节点、单用户、可信仓库的受限团队协作交付、可追溯分工、依赖集成与独立验收。

仍不能承诺：任意需求全自动规划、多租户/恶意代码隔离、无人干预的任意失败恢复、已证明优于 Lead＋SubAgents，或正式 production/stable 发布。局部 replan/reuse、同路径故障矩阵与发布支持仍按 B2/B3 完成；本 PoC 不替代它们。
