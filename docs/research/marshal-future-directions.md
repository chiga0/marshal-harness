# Marshal 发展方向调研（路线图候选）

- 日期：2026-08-09
- 性质：战略调研与方向分析，非实施承诺；任何方向落地前仍需 ADR/门禁流程
- 基线：Local MVP `USABLE`（v0.1.0），ADR 0001–0014，Milestone 0–6 通过
- 方法：以本仓库真实代码/实测为"实时依据"，辅以编排可观测性领域的外部通行实践

## 0. 现状基线（ grounded ）

| 事实 | 依据 |
| --- | --- |
| CLI-first 模块化单体，无守护进程 | ADR 0001；延后清单含 Daemon/MCP/远程调度 |
| 状态在仓库本地 `.marshal/`，`MARSHAL_STATE_DIR` 可覆盖（须绝对路径、绑定仓库身份） | `internal/repository/state.go:29-33` |
| `events.jsonl` append-only + `state.json` 原子快照 = 天然事件源 | `internal/runstore` |
| Observer / TerminalSession / Publisher 均为稳定 Port，与 Core 解耦 | ADR 0008/0009；`internal/port` |
| CI 观察为**轮询**（`ObserveChecks`），CI_PENDING 按冻结 deadline 分类 | `internal/publication/checks.go:35-97` |
| 本机已积累 47 个 Run，JSONL 全量扫描随 Run 增长变慢 | `ls .marshal/runs` = 47 |
| 多 Run 并发时"哪个卡住了"靠人肉/轮询发现 | 实测痛点（见 fanout-consolidation） |
| herdr 可提供注意力视图/远程回魂，已 POC | `exp/herdr-terminal-backend` |

## 1. 部署形态

### 1.1 独立机器/远程部署（用户举例）
**做什么**：把 Marshal 从"笔记本本地 CLI"变为"独立机器上的常驻服务"，Worker 在远端执行，Lead 在本地或手机监督。
**需要什么**：
1. **常驻服务**：当前无 daemon（ADR 0001 延后）。需要一个只读的"状态服务"+ 受控的"执行代理"，二者权限分离（沿用 ADR 0003 分权）。
2. **远程 Worker 传输**：Worker 在远端跑，需要 SSH/herdr-remote 或容器传输；herdr `--remote <ssh>` 已验证可行（herdr POC）。
3. **认证与多用户**：当前单用户；上机器即引入多用户/凭据分发，需 ADR（信任边界变化）。
4. **状态目录**：`MARSHAL_STATE_DIR` 已支持独立目录，但须绑定唯一仓库身份（已具备）。
**理由**：把重计算（worker/verify）移出笔记本；手机/多端监督。
**风险/前置**：多用户与凭据分发是最大新增信任面；建议**先做只读远程观察（见 2），再做远程执行**。优先级：中（后置）。

### 1.2 容器/VM Hardened Profile
**理由**：Local Profile 不隔离恶意代码（security-model）；要跑不可信代码必须容器/VM。
**前置**：延后清单已列；需要 mount/network/资源强制。优先级：中（安全驱动时做）。

## 2. 可观测性（用户举例：Web + 实时 DAG）

### 2.1 只读 Web 概览 + 实时 DAG/进展
**做什么**：一个**只读** Web 服务，流式呈现 Task→Run→Attempt→Review→Publish 的 DAG 与实时状态。
**可行性（高）**：`events.jsonl` append-only + `state.json` 快照是现成事件源；Observer Port 已解耦；用 SSE/WebSocket 流事件、渲染 DAG 即可，**不需要改动 Core 权威**。
**理由（外部依据）**：编排可观测性的通行做法就是 DAG+实时 UI——Temporal UI、Argo Workflows、Airflow、Buildkite、GitHub Actions 均以"可视化工作流+实时状态"为核心价值；herdr 的注意力视图亦证明"一眼看到谁卡住"的价值。我们实测的"哪个 Run 卡了靠人肉发现"正是缺这一层。
**约束**：**只读**。控制（approve/publish）仍留在 CLI/Skill，避免 Web 成为第二个权威（信任边界）。
**前置**：无信任边界变化（只读），可较早做；但属新增产品面，建议先做最小只读版。优先级：**高**（直接解决实测痛点，且不碰信任边界）。

### 2.2 CI Webhook 接收（替代轮询）
**理由**：CI 观察现为轮询（`ObserveChecks`），CI_PENDING 延迟高；webhook push 可实时化并省轮询。
**前置**：需公网入口/签名校验；延后清单已列。优先级：中。

## 3. 编排能力

### 3.1 依赖感知任务拆分（安全并行编码）
**理由**：fanout-consolidation 已证"文件互斥/scope 互斥"可安全并行；依赖感知（dependency graph）可进一步解锁耦合任务的正确切分（Co-Coder 证据）。
**前置**：需要仓库依赖图；属编排增强。优先级：中。

### 3.2 评审团/调研队产品化
**理由**：ADR 0013/0014 已铺路（分级拒绝、read-only 画像）；把评审团/调研队从"约定"变为一等命令可提升多视角审查。
**前置**：需真实 Pilot 数据。优先级：中。

### 3.3 度量/成本会计（Telemetry）
**理由**：A/B 设计与 fan-out 经济决策需要 token/墙钟/返工数据；当前无采集。
**前置**：延后清单已列；注意隐私。优先级：中（为 A/B 服务）。

## 4. 性能与规模

### 4.1 SQLite/索引查询
**理由**：47 个 Run 已使 JSONL 全量扫描变慢；随 Run 增长需要索引。延后清单"有性能证据后再加 SQLite"——现已有初步证据（47 runs）。
**前置**：保持 JSONL 为审计格式，SQLite 仅作索引（ADR 0001 已预留）。优先级：中。

## 5. 生态与接入

### 5.1 更多 Adapter / GitLab Publisher
**理由**：扩大 Worker 与 Forge 选择；每个需一致性测试。延后清单已列。优先级：低-中。

### 5.2 主 Agent 接入面（MCP/ACP/Desktop Facade）
**理由**：当前 Lead 经 CLI/Skill；MCP/ACP 可让任意 Agent 框架接入。延后清单已列。优先级：低-中。

### 5.3 i18n 与文档站增强
**理由**：中文优先已定；英文导航已加（gap #5）。继续完善检索/版本化。优先级：低。

## 6. 会话与远程回魂

### 6.1 herdr 远程/回魂生产化
**理由**：herdr POC 已证 `agent_status` 辅助信号与 ssh/回魂价值；生产化需版本矩阵+一致性测试+独立里程碑。优先级：中（实验分支继续）。

## 7. 建议排序（理由：价值/信任风险/前置）

| 序 | 方向 | 优先级 | 理由 |
| --- | --- | --- | --- |
| 1 | 只读 Web 概览+实时 DAG（2.1） | 高 | 解决实测痛点、不碰信任边界、事件源现成 |
| 2 | 评审团/调研队产品化（3.2） | 中 | ADR 已铺路，需 Pilot 数据 |
| 3 | 依赖感知拆分（3.1） | 中 | 解锁安全并行编码 |
| 4 | SQLite 索引（4.1） | 中 | 已有性能证据 |
| 5 | CI Webhook（2.2） | 中 | 实时化 CI |
| 6 | 度量/成本（3.3） | 中 | 服务 A/B 与 fan-out 经济 |
| 7 | herdr 生产化（6.1） | 中 | 实验分支继续 |
| 8 | 独立机器/远程执行（1.1） | 中-后 | 多用户信任面大，先只读观察 |
| 9 | Hardened Profile（1.2） | 中 | 安全驱动 |
| 10 | 更多 Adapter/GitLab（5.1） | 低-中 | 广度 |
| 11 | MCP/ACP（5.2）/ i18n（5.3） | 低 | 生态/体验 |

## 8. 结论

Marshal 的下一步应**先做不碰信任边界的可观测性（只读 Web+DAG）**，因为它直接解决实测痛点且事件源现成；其次是把已铺路的编排能力（评审团/依赖拆分）产品化并用数据验证；部署形态（独立机器/远程执行）与 hardened profile 涉及信任边界，应后置并走 ADR。所有方向落地前仍需 ADR/门禁流程，本文仅为候选与论证。
