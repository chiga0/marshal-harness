# v1.0 Release Readiness 判定表（ADR 0052 §1 逐条对照）

更新日期：2026-08-27（主线纠偏后口径）
判定基准：[ADR 0052](adr/0052-v1-release-scope-and-production-reachability.md) 第 1 节（九条）+ 第 3 节（生产可达性成熟度）。
口径：每条只写已证据化的状态；2026-08-27 维护者纠偏结论为权威基线——R3–R5 的 INTEGRATED 宣称已撤回（生产 authority 语义未收敛），唯一主线是先收敛 R2/R3 的真实耐久纵切。

| # | ADR 0052 §1 要求 | 状态 | 证据 / 缺口 |
| --- | --- | --- | --- |
| 1 | 唯一真实可恢复生产执行链 | `OPEN` | 真实 pi 经 `sandboxbridge` exec-chain 在 Local allocation 内执行（R1 纵切，`INTEGRATED`）；但链上 authority 收敛未完成（见 #3/#4），recovery 解释面（`marshal explain run` + supervisor 消费 `recovery.Decide`）依赖 R2/R3 authority 语义。 |
| 2 | 真实 AgentProvider + 真实 Local/Container SandboxProvider，Agent 进程实际在 allocation 内 | `INTEGRATED` | 真实 pi 经 `PrepareLaunch→CompleteLaunch` 在 Local allocation 内执行，`sandboxbridge` 集成测试与 canary 双向锚定 allocation record。 |
| 3 | 文件型耐久 authority ledger、幂等提交、重启恢复、旧 generation fencing、单一恢复模型 | `COMPONENT` | runstore 快照+journal replay/lease fencing/soak 组件级全绿；**结果接纳未重开真实 durable authority**：当前是 `seedRegistry`/`seedSandboxLedger` 临时构造 + 输入 Facts 自验；lease expiry 接纳时 `now+24h` 生成，非 dispatch 冻结值。R2 收口条件：dispatch 持久化 immutable Attempt binding、ingress 打开真 ledger 逐项 recheck、fact 与 Run journal 原子提交。 |
| 4 | 每 Attempt 双 binding + 接纳前 current-ledger recheck | `COMPONENT` | `resultbinding` + bridge admission anchor 接线已接通（`sandbox-binding-admission.json` 持久化），但其 recheck 语义同 #3：**不是真实 ledger recheck**；R3 production admission 重做中。 |
| 5 | 可判定 cancel、timeout、retry、terminal 与 Outcome 语义 | `INTEGRATED` | 既有 runstore/cli/execution 回归覆盖。 |
| 6 | 独立 Verification；发布仅 none / Draft PR，不 auto-merge | `INTEGRATED` | 既有产线路径；merge 默认禁用属 universal 不变量。 |
| 7 | loopback marshal-server 能 start/cancel/query/restore 真实 Run | `COMPONENT` | API/SSE 套件绿；跨 OS 进程 restart 恢复证据缺口见 [i186-r6-fault-conformance-audit.md](research/i186-r6-fault-conformance-audit.md) TOP#3。 |
| 8 | kill/restart/lost-response/stale/binding-drift 故障注入与恢复测试 | `OPEN` | 审计盘点已落盘（TOP#1–#5）：真进程中段 kill 经 lease owner 探针恢复、happy-path lost response、server 进程重启、ResultIngress 接入真链后的 stale/replay/伪造负例、新 gate 故障域扩展均未闭合。 |
| 9 | macOS/Linux 稳定安装产物；macOS 须签名/notarization/release identity 门禁 | `OPEN` | install.sh 一行安装 + SHA256SUMS 校验 + 源码回退；`make dist` 四平台资产与 release workflow 就绪（`2392f72`）；**稳定 v1.\* 无签名 secrets 一律 fail closed，unsigned 仅允许 prerelease**（`a06189c`）；macOS 签名/公证受 **Issue #212**（宿主企业策略阻断固定 Mach-O，签名身份未 provision）外部阻塞。 |

## rc 判定规则

- v1.0-rc 前置：`#3` 与 `#4` 必须收敛为 `INTEGRATED`（真实 durable authority 纵切），`#8` 六类故障 conformance 闭环；`#7` 跨进程 restart 证据补齐；`#9` 以 prerelease 通道首发，stable 通道在 Issue #212 解决前保持关闭。
- 任何状态切换的条件：真实 composition root 可达 + 证据可复跑；gate-绕过的 test 证据只能支撑 `COMPONENT`，不支撑 `INTEGRATED`（除 R1 纵切已经被维护者认定外）。
- 本表与 `docs/roadmap-status.md` 的 R6 行互为指针；主线纠偏结论为当前权威基线。

## 当前结论（2026-08-27，纠偏后）

- `R0: PASSED`；`R1: IN_PROGRESS（INTEGRATED，real Agent in allocation 纵切）`；`R2–R5: IN_PROGRESS（COMPONENT）`；`R6: PLANNED`。
- 最短剩余路径：R2（immutable Attempt binding 在 dispatch 持久化 + ingress 打开真 ledger）→ R3 production admission（seed* 删除、原子提交）→ provider-neutral launch contract → 不经 bypass 的真实 pi 全闭环 → 故障 conformance → v1.0-rc。
- 外部阻塞：Issue #212（macOS 签名身份，企业策略）；不影响 prerelease 通道。
