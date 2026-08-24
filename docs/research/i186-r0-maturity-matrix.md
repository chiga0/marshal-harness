# I186-R0 Capability Maturity Matrix 与 Failure Inventory

更新时间：2026-08-24

按 Planning Baseline v3 R0 步骤 5，废弃单一 `PASSED` 推导生产状态的做法，改用五级 maturity：

`contract`（Schema/合同冻结）→ `component`（单元/组件门禁通过）→ `integrated`（真实端到端链路通过）→ `supported`（有运维证据与恢复路径）→ `production`（独立 reviewer 清零 P0/P1 且达到 SLO）。

只允许向上逐级举证，不允许跨级推导。本矩阵是现状快照，不改变 M0–M9 历史结论的表述，仅细化其推导范围。

## 1. Maturity matrix（baseline `4d6ad29`）

| 能力 | contract | component | integrated | supported | production | 当前证据摘要 |
| --- | --- | --- | --- | --- | --- | --- |
| Run 生命周期状态机 | ✅ | ✅ | ✅ | — | — | M1/M6 `PASSED`；embedded/local 真实 Run 多轮闭环 |
| Worktree 隔离与锁定基线 | ✅ | ✅ | ✅ | — | — | M2 `PASSED`；单写入者由 Core fence |
| 独立 Verification | ✅ | ✅ | ✅ | — | — | M2/M6 `PASSED`；verifier 与 Worker 分域 |
| Review/Rework 闭环 | ✅ | ✅ | ✅ | — | — | M3 `PASSED`；freshness claim + closure matrix |
| Publication（draft PR + reconcile） | ✅ | ✅ | ✅ | — | — | M5 `PASSED`；Merge 默认禁用不变 |
| Worker Adapter（qoder/codex/qwen ordinary-user） | ✅ | ✅ | 部分 | — | — | live probe/smoke 级证据，见 baseline report 第 3 节；非 production authority |
| Sandbox SPI / Local Sandbox | ✅ | ✅ | — | — | — | M8 component gate `PASSED`；未与 Agent 执行绑定（R1 目标） |
| Provider registration/snapshot/evidence | ✅ | ✅ | — | — | — | M8/M9 slice；legacy Adapter 尚未迁移为 production AgentProvider |
| DurableExecutionEngine seam / lease / typed edge | ✅ | ✅ | — | — | — | M9 delivered slices；未证明唯一主链闭环 |
| Public API / SSE / Push-Pull transport | ✅ | ✅ | — | — | — | M9 `PASSED` slices；conformance 非终态 |
| ApplicationCommandPort（单一业务写路径） | — | — | — | — | — | 尚未建设（R2 目标） |
| ResultIngress（DRC-bound 结果接纳） | 部分（ADR 0018 DRC） | — | — | — | — | DRC 合同已冻结，ingress 未实现（R1/R2 目标） |
| Agent/Sandbox 双 Provider binding | — | — | — | — | — | 未建设（R3 目标） |
| 单一恢复模型 + `marshal explain` | — | — | — | — | — | 未建设（R4 目标） |
| Cloudflare/远程 Provider | ✅（research） | 部分 | — | — | — | M10 暂停；切片转 R4/R6 fixture |

`—` 表示该级尚无证据，不是失败。M10–M13 的旧 milestone 状态按 Planning Baseline v3 冻结为暂停，不参与本矩阵的 production 推导。

## 2. Failure inventory（现状缺口，沿用 Issue #186 分级）

| ID | 优先级 | 缺口 | 收敛阶段 |
| --- | --- | --- | --- |
| I186-P0-1 | P0 | Sandbox 已 provision 但 Agent 仍可能在 host `Adapter.Run`；Candidate 无法证明来自绑定 allocation/generation 内的 Agent 进程 | R1 |
| I186-P1-1 | P1 | CLI/API/Supervisor 存在直接编排 Store/lifecycle 的路径，缺单一 ApplicationCommandPort 与 authority fact/outbox 原子提交 | R2 |
| I186-P1-2 | P1 | 当前主要是 legacy WorkerAdapter，不是 production AgentProvider（缺独立 durable registration/typed capability/attestation/conformance/binding） | R3 |
| I186-P1-3 | P1 | 外部结果接纳未全部收敛到统一门禁，缺 DRC-bound ResultIngress + current-ledger recheck + quarantine/replay | R1/R2 |
| I186-P1-4 | P1 | Provider/session/engine 失败缺统一恢复解释，缺 ledger-driven reconcile/fence/new Attempt 与 `marshal explain run` | R4 |
| I186-P1-5 | P1 | Cloudflare/远程 Provider current-lease defense-in-depth 不完整，缺不可绕过的 lease resolver/fencing validation | R4 |

## 3. 使用规则

- R1–R6 每个 exit gate 必须引用本矩阵中至少一行能力的级别提升，并附独立证据；级别提升不得由同一故障域的自证产生。
- 任何 `production` 声明必须由独立 reviewer 清零 P0/P1 且维护者显式接受，才允许写入 [roadmap-status.md](../roadmap-status.md)。
- 本清单的缺口关闭顺序即 R1→R4 的实施顺序；新增缺口必须带稳定 ID 并归入对应 R 阶段。
