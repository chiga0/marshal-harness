# v1 正式版发布与云端 Agent 托管目标

日期：2026-08-27
状态：active goal
基线：`0bf8410`（origin/main）

## 目标层级

### 第一层：v1.0-rc prerelease（当前冲刺）

**定义**：单节点、单用户、可信仓库、真实 pi Agent + Local Sandbox 的生产可达纵切，经故障 conformance 证明可恢复。

**前置条件**（ADR 0052 §1 九条）：

| # | 要求 | 当前 | 收敛条件 |
| --- | --- | --- | --- |
| 3 | 文件型耐久 authority ledger | COMPONENT→INTEGRATED | 代码已实现，待 R6 故障 conformance 证据 |
| 4 | 双 binding + current-ledger recheck | COMPONENT→INTEGRATED | 同 #3 |
| 7 | loopback server 恢复真实 Run | COMPONENT | 跨进程 restart 证据 |
| 8 | 故障注入 conformance | OPEN | TOP5 闭合 |
| 9 | 安装产物 + 签名 | OPEN | prerelease 通道（unsigned 允许） |

**R6 TOP5 闭合顺序**（按依赖与复杂度）：

1. TOP5（小）：新门 gate fault 域扩展 — recoveryDecision unavailable + anchor 缺失
2. TOP4（中）：ResultIngress 接入真链后的 stale/replay/伪造负例矩阵
3. TOP2（中）：happy-path lost response fixture
4. TOP3（中）：marshal-server 跨进程 restart recovery
5. TOP1（大）：真实进程 kill 中段注入的端到端恢复

### 第二层：v1.0 stable（外部阻塞解除后）

**定义**：v1.0-rc 经 soak + 真实使用验证后，签名 macOS 构建进入 stable 通道。

**阻塞**：Issue #212（macOS 签名身份 provision）。

**收敛条件**：
- v1.0-rc prerelease 运行 ≥ 2 周无 P0 回归
- Issue #212 解决：签名身份 provision + notarization 通过
- stable release workflow 门禁从 fail-closed 切换为 fail-open（签名身份就绪后）
- `make dist` 四平台签名产物 + SHA256SUMS + install.sh 验证

### 第三层：v1.x 云端 Agent 托管真实可用（中期目标）

**定义**：Marshal 作为云端服务托管多用户 Agent 长任务，支持远程 SandboxProvider、HA、多租户。

**依赖的架构扩展**（已有 ADR 合同，未实现）：

| 能力 | ADR 合同 | 当前状态 | 实现优先级 |
| --- | --- | --- | --- |
| 远程 SandboxProvider (Push/Pull) | ADR 0018 | DESIGN | P1 |
| 多租户 authority namespace | ADR 0018 §3 | DESIGN | P1 |
| HA (多节点 journal + lease) | ADR 0033 (Proposed) | DESIGN | P2 |
| Goal DAG / 多 Agent 协作 | ADR 0019 | DESIGN | P2 |
| A2A gateway (外部委托) | 行业跟踪 | TRACKING | P3 |
| MCP 工具互操作 | 行业跟踪 | TRACKING | P3 |

**v1.x 路线**（v1.0 stable 后启动）：

1. **Push/Pull 远程 Sandbox**：实现 ADR 0018 的远程 Provider Port transport，使 Agent 进程可在远程容器中执行
2. **多租户隔离**：authority namespace 分租户隔离，每租户独立 state root + journal
3. **HA journal**：ADR 0033 的跨节点 fence + 协调回滚
4. **Goal DAG**：ADR 0019 的确定性 GoalController + 多 Agent handoff
5. **A2A gateway**：外部 Agent 系统经 Public API 委托任务
6. **MCP 工具互操作**：标准化 Worker 工具面治理

**云端 Agent 托管"真实可用"的判定标准**：
- 远程 SandboxProvider 经 credentialed probe + conformance 全绿
- 多租户隔离经负测（跨租户访问 fail closed）
- HA 经跨节点 kill/restart conformance
- 至少一个真实 Agent（pi/qwen/codex）在远程容器中执行真实写任务
- Goal DAG 经多 Agent handoff 端到端验证
- soak ≥ 24h 无 P0 回归

## 执行节奏

- **当前**：R6 TOP5 → TOP4 → TOP2 → TOP3 → TOP1（v1.0-rc prerelease）
- **短期**：v1.0-rc prerelease → soak → stable（待 Issue #212）
- **中期**：v1.x 云端 Agent 托管（stable 后启动）
