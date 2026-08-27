# I186-R0 Capability Maturity Matrix 与 Failure Inventory

更新时间：2026-08-27（R6 replan 快照）

按 Planning Baseline v3 R0 步骤 5，废弃单一 `PASSED` 推导生产状态的做法，改用五级 maturity：

`contract`（Schema/合同冻结）→ `component`（单元/组件门禁通过）→ `integrated`（真实端到端链路通过）→ `supported`（有运维证据与恢复路径）→ `production`（独立 reviewer 清零 P0/P1 且达到 SLO）。

只允许向上逐级举证，不允许跨级推导。本矩阵是现状快照，不改变 M0–M9 历史结论的表述，仅细化其推导范围。

## 1. Maturity matrix（R6 快照，on R0–R5 DONE）

| 能力 | contract | component | integrated | supported | production | R6 证据摘要 |
| --- | --- | --- | --- | --- | --- | --- |
| Run 生命周期状态机 | ✅ | ✅ | ✅ | ✅ | — | M1/M6 `PASSED`；R5 bridged 默认路径 5 轮路径级 soak、rollback 演练、runstore 重开 replay 等价；supported 升级证据为恢复路径与原语/路径 soak |
| Worktree 隔离与锁定基线 | ✅ | ✅ | ✅ | 部分 | — | M2 `PASSED`；桥路径下 allocation record 落盘（孤儿对账锚点）；worktree 级 24h 运维证据待 wall-clock soak |
| 独立 Verification | ✅ | ✅ | ✅ | 部分 | — | M2/M6 `PASSED`；R6 canary 真实 Pi 经 bridged 路径独立验证 9 Gate 通过 |
| Review/Rework 闭环 | ✅ | ✅ | ✅ | 部分 | — | M3 `PASSED`；闭合语义经 recovery 单一恢复模型形式化；canary Run 留 REVIEW_PENDING |
| Publication（draft PR + reconcile） | ✅ | ✅ | ✅ | 部分 | — | M5 `PASSED`；effectsink pre-mutation recheck 合同已冻结（生产 sink 接线归后续）；Merge 默认禁用不变 |
| Worker Adapter（qwen/pi ordinary-user） | ✅ | ✅ | ✅ | 部分 | — | R6 canary：Pi `0.84.3`（精确锁版本）经 sandboxbridge 默认路径端到端 real-run completed + 独立验证通过；Qwen `0.21.15` semver 准入闭环；Qoder/Codex smoke 证据维持 |
| Sandbox SPI / Local Sandbox | ✅ | ✅ | ✅ | 部分 | — | bridged 默认路径对真实 LocalRunner 常驻回归；fencing 按 sealed-lease 精确匹配；分配身份落盘 + Sweep 对账 |
| Provider registration/snapshot/evidence | ✅ | ✅ | 部分 | — | — | M8/M9 + R3-A/B/C（agentregistry/runtimeprofile/bindingcheck/attemptgate）；Agent/Sandbox 双 binding 生产接线是 R5 诚实缺口（不宣称 integrated） |
| DurableExecutionEngine seam / lease / typed edge | ✅ | ✅ | 部分 | — | — | M9 slices；typed-edge recheck 在 dispatch-bound 生产路径生效；engine seam 未成主链 |
| Public API / SSE / Push-Pull transport | ✅ | ✅ | 部分 | — | — | M9 `PASSED` slices；Push/Pull 双拓扑 conformance 为测试套件（未接生产 worker 路径） |
| ApplicationCommandPort | ✅ | ✅ | ✅ | — | — | R2 DONE（CLI/API/Supervisor 无第二写路径 exit gate） |
| ResultIngress（DRC-bound 结果接纳） | ✅ | ✅ | 部分 | — | — | resultingress + attemptgate/hotpath/effectsink 合同与门禁完备；ResultIngress→runstore 证据桥、双 binding profile 接线为 R5 诚实缺口 |
| Agent/Sandbox 双 Provider binding | ✅ | ✅ | 部分 | — | — | R3 全切片 + ADR 0049 收敛域证明；生产 ResultIngress 接线归后续 |
| 单一恢复模型 + explain | ✅ | ✅ | 部分 | — | — | recovery 决策表 + 10k 原语 soak + explain 渲染模型；`marshal explain run` CLI wiring 与真实 ledger 装配归后续（等价 API 口径见 ADR 0053 决策 5） |
| 失败分类 authority | ✅ | ✅ | ✅ | — | — | ADR 0049 决策 2 + failureclass + R4 recovery 消费闭环 |
| Cutover 判定与 worker executor 接线 | ✅ | ✅ | ✅ | 部分 | — | ADR 0045/0051 + cutovereq/cutovercheck golden 判定 + sandboxbridge 默认翻向 + rollback 演练 + cli 全套绿；wall-clock 运维证据待 soak |
| Cloudflare/远程 Provider | ✅（research） | 部分 | — | — | — | M10 暂停；切片保留为 R4/R6 fixture |

`—` 表示该级尚无证据，不是失败。`production` 列整行为空：本机不存在 production assurance 运行（ADR 0042 ordinary-user 语义边界；Darwin self-identity `MARSHAL-DARWIN-SELF-IDENTITY` 外部 provision 未完成），不随 R6 声明。

## 2. Failure inventory（R6 收口期状态）

| ID | 优先级 | 缺口 | 状态 |
| --- | --- | --- | --- |
| I186-P0-1 | P0 | Sandbox 已 provision 但 Agent 仍可能在 host `Adapter.Run`；Candidate 无法证明来自绑定 allocation/generation 内的 Agent 进程 | `CONVERGED-ORDINARY`（R5 bridged 默认路径：allocation/lease 身份绑定 + frozen 工单入账 + allocation record 落盘；hardened 证明归 production 阶段） |
| I186-P1-1 | P1 | CLI/API/Supervisor 直接编排路径 | `CLOSED`（R2） |
| I186-P1-2 | P1 | legacy WorkerAdapter 非 production AgentProvider | `CONVERGED-ORDINARY`（R3 typed registration/capability/evidence + 双 binding 收敛域；production AgentProvider 归 production 阶段） |
| I186-P1-3 | P1 | 外部结果接纳未收敛统一门禁 | `CONVERGED`（resultingress + attemptgate/hotpath/effectsink；双 binding/candidate 桥接线归后续） |
| I186-P1-4 | P1 | 缺统一恢复解释 | `CONVERGED`（recovery + explain 等价 API + soak；CLI explain wiring 归后续） |
| I186-P1-5 | P1 | 远程 Provider lease defense-in-depth | `OPEN`（Push/Pull 未接生产；归 M10 重排后范围） |

## 3. 使用规则

- R1–R6 每个 exit gate 必须引用本矩阵中至少一行能力的级别提升，并附独立证据；级别提升不得由同一故障域的自证产生。
- 任何 `production` 声明必须由独立 reviewer 清零 P0/P1 且维护者显式接受，才允许写入 [roadmap-status.md](../roadmap-status.md)。
- 本清单的缺口关闭顺序即 R1→R4 的实施顺序；新增缺口必须带稳定 ID 并归入对应 R 阶段。

## 4. 历史快照（2026-08-24，baseline `4d6ad29`）

上一版矩阵见 git 历史（commit `4d6ad29` 的本文档版本）；本行仅保留锚点，变化幅度以两版 diff 为准。
