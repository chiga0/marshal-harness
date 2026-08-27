# v1.0 Release Readiness 判定表（ADR 0052 §1 逐条对照）

更新日期：2026-08-27（R2/R3 agent 侧 current-ledger recheck 收敛后口径）
判定基准：[ADR 0052](adr/0052-v1-release-scope-and-production-reachability.md) 第 1 节（九条）+ 第 3 节（生产可达性成熟度）。
口径：每条只写已证据化的状态；2026-08-27 维护者纠偏结论为权威基线，当日后续提交逐步收敛 R2/R3。

| # | ADR 0052 §1 要求 | 状态 | 证据 / 缺口 |
| --- | --- | --- | --- |
| 1 | 唯一真实可恢复生产执行链 | `OPEN` | 真实 pi 经 `sandboxbridge` exec-chain 在 Local allocation 内执行（R1 纵切，`INTEGRATED`）；recovery 解释面（`marshal explain run` + supervisor 消费 `recovery.Decide`）依赖 R2/R3 authority 语义——#3/#4 已接近收敛，#8 故障 conformance 未闭合。 |
| 2 | 真实 AgentProvider + 真实 Local/Container SandboxProvider，Agent 进程实际在 allocation 内 | `INTEGRATED` | 真实 pi 经 `PrepareLaunch→CompleteLaunch` 在 Local allocation 内执行，`sandboxbridge` 集成测试与 canary 双向锚定 allocation record。 |
| 3 | 文件型耐久 authority ledger、幂等提交、重启恢复、旧 generation fencing、单一恢复模型 | `COMPONENT→INTEGRATED` | runstore 快照+journal replay/lease fencing/soak 组件级全绿；**dispatch 持久化 immutable AttemptBinding 已实现**（`e75170f`）；**lease expiry 从 dispatch 冻结的 DispatchLease 读取**（`e75170f`）；**agent 侧 current-ledger recheck 已实现**（`0bf8410`：`AgentRegistrationActive` 从 durable authority 验证）；**sandbox 侧 ProviderRegistrationActive + Inspect live state 已实现**（`e75170f`）；`seedRegistry`/`seedSandboxLedger` 仍用于 bindingcheck 内部 snapshot/generation 校验，registration active 状态已从 durable authority 验证。负测矩阵覆盖 revoke/replace/terminated/replay/expired/stale-gen/revoked-agent（`a304e98`+`0bf8410`）。 |
| 4 | 每 Attempt 双 binding + 接纳前 current-ledger recheck | `COMPONENT→INTEGRATED` | `resultbinding` + bridge admission anchor 接线已接通（`sandbox-binding-admission.json` 持久化）；**双侧 current-ledger recheck 已实现**：agent 侧 `AgentRegistrationActive`，sandbox 侧 `ProviderRegistrationActive` + Inspect live state；负测矩阵覆盖 7 类拒绝场景。 |
| 5 | 可判定 cancel、timeout、retry、terminal 与 Outcome 语义 | `INTEGRATED` | 既有 runstore/cli/execution 回归覆盖。 |
| 6 | 独立 Verification；发布仅 none / Draft PR，不 auto-merge | `INTEGRATED` | 既有产线路径；merge 默认禁用属 universal 不变量。 |
| 7 | loopback marshal-server 能 start/cancel/query/restore 真实 Run | `COMPONENT` | API/SSE 套件绿；跨 OS 进程 restart 恢复证据缺口见 [i186-r6-fault-conformance-audit.md](research/i186-r6-fault-conformance-audit.md) TOP#3。 |
| 8 | kill/restart/lost-response/stale/binding-drift 故障注入与恢复测试 | `COMPONENT` | 审计盘点 TOP5 全部闭合（`a202799`+`1f5d088`+`9441f9b`+`5aac2e9`+`0cfbffe`）：gate fault 域扩展（recoveryDecision unavailable + anchor 缺失/损坏/被替换）、exec-chain admission 拒绝矩阵（revoked agent/sandbox + expired lease）、lost-response fixture（worker-result 已 durable 但 journal 未完成 → 隔离+重试）、marshal-server 跨进程 restart recovery、真实 lease 生命周期 kill 恢复。 |
| 9 | macOS/Linux 稳定安装产物；macOS 须签名/notarization/release identity 门禁 | `OPEN` | install.sh 一行安装 + SHA256SUMS 校验 + 源码回退；`make dist` 四平台资产与 release workflow 就绪（`2392f72`）；**稳定 v1.\* 无签名 secrets 一律 fail closed，unsigned 仅允许 prerelease**（`a06189c`）；macOS 签名/公证受 **Issue #212**（宿主企业策略阻断固定 Mach-O，签名身份未 provision）外部阻塞。 |

## rc 判定规则

- v1.0-rc 前置：`#3` 与 `#4` 必须收敛为 `INTEGRATED`（真实 durable authority 纵切）——**代码已实现，待故障 conformance 证据闭合后升级口径**；`#8` 六类故障 conformance 闭环；`#7` 跨进程 restart 证据补齐；`#9` 以 prerelease 通道首发，stable 通道在 Issue #212 解决前保持关闭。
- 任何状态切换的条件：真实 composition root 可达 + 证据可复跑；gate-绕过的 test 证据只能支撑 `COMPONENT`，不支撑 `INTEGRATED`（除 R1 纵切已经被维护者认定外）。
- 本表与 `docs/roadmap-status.md` 的 R6 行互为指针；主线纠偏结论为当前权威基线。

## 当前结论（2026-08-27，R6 TOP5 闭合后）

- `R0: PASSED`；`R1: IN_PROGRESS（INTEGRATED，real Agent in allocation 纵切）`；`R2/R3: IN_PROGRESS（COMPONENT→INTEGRATED，双侧 current-ledger recheck 代码已实现 + R6 TOP5 故障 conformance 闭合）`；`R4: IN_PROGRESS（COMPONENT）`；`R5: IN_PROGRESS（COMPONENT）`；`R6: IN_PROGRESS（COMPONENT→INTEGRATED，TOP5 闭合，待 #7 跨进程 restart 证据补齐）`。
- 最短剩余路径：#3/#4/#8 口径升级为 INTEGRATED → #7 跨进程 restart 补齐 → v1.0-rc prerelease。
- 外部阻塞：Issue #212（macOS 签名身份，企业策略）；不影响 prerelease 通道。
