# v1.0 Release Readiness 判定表（ADR 0052 §1 逐条对照）

更新日期：2026-08-27（composition root 纠偏后口径重置）
判定基准：[ADR 0052](adr/0052-v1-release-scope-and-production-reachability.md) 第 1 节（九条）+ 第 3 节（生产可达性成熟度）。
口径：每条只写已证据化的状态；2026-08-27 composition root 纠偏为当前权威基线。

## Composition root 纠偏（2026-08-27）

审计发现：CLI 此前构造两个独立 `EmbeddedSandboxRuntime` 实例——第一个承担 `DispatchBinder`，第二个承担 Sandbox/lease/admission。导致第二个 runtime 读不到第一个签发的 lease，退回虚构 `now+24h`；agent registry 也是空的，admission 的 `AgentRegistrationActive` 必然返回 false。

已修复（`33bad5c`）：
1. CLI 只构造一个 `EmbeddedSandboxRuntime`——同一实例同时承担 DispatchBinder + SandboxProvider + Authority + ResultIngressStore
2. Adapter probe 后注册 agent 到 `sharedRuntime.agentRegistry`
3. 删除 `now+24h` lease fallback——lease 缺失直接 fail closed
4. Allocation record 写入失败从降级改为 fail closed（阻止 Exec）
5. 非 LaunchCapable adapter 在 production profile 中被拒绝（不允许静默走 legacy Run）
6. Agent/sandbox capability digest 变量分离（完整双 binding 需 Facts 扩展字段，标记 TODO）

残留局限（不升级任何成熟度）：
- lease 仍是内存态，进程重启即丢失——跨进程恢复需要耐久 lease ledger
- agent/sandbox capability digest 仍混用同一字段（`Facts.CapabilityDigest`），完整双 binding 需要 schema 扩展
- ResultIngress replay/idempotency 状态是进程内 map，跨进程重放未覆盖
- 严格 E2E 测试（`TestRealPiStrictE2E`）已建但未用真实 pi 验证通过

| # | ADR 0052 §1 要求 | 状态 | 证据 / 缺口 |
| --- | --- | --- | --- |
| 1 | 唯一真实可恢复生产执行链 | `COMPONENT` | 真实 pi 经 `sandboxbridge` exec-chain 在 Local allocation 内执行（R1 纵切）；composition root 单实例修复后 lease/registration 可在同进程内可查，但跨进程恢复未验证。 |
| 2 | 真实 AgentProvider + 真实 Local/Container SandboxProvider，Agent 进程实际在 allocation 内 | `INTEGRATED` | 真实 pi 经 `PrepareLaunch→CompleteLaunch` 在 Local allocation 内执行，`sandboxbridge` 集成测试与 canary 双向锚定 allocation record。 |
| 3 | 文件型耐久 authority ledger、幂等提交、重启恢复、旧 generation fencing、单一恢复模型 | `COMPONENT` | AttemptBinding creation-once + dispatch 冻结 lease expiry 已实现；agent/sandbox current-ledger recheck 已实现；**但 lease 是内存态（非耐久），跨进程恢复未覆盖**；composition root 修复后同进程内 lease 可查，但未用真实 pi 验证。 |
| 4 | 每 Attempt 双 binding + 接纳前 current-ledger recheck | `COMPONENT` | admission anchor 持久化 + 双侧 recheck 代码已实现；**但 agent/sandbox capability digest 仍混用同一字段**；ResultIngress replay 状态是进程内 map，跨进程重放未覆盖；未用真实 pi 验证 admission 成功路径。 |
| 5 | 可判定 cancel、timeout、retry、terminal 与 Outcome 语义 | `INTEGRATED` | 既有 runstore/cli/execution 回归覆盖。 |
| 6 | 独立 Verification；发布仅 none / Draft PR，不 auto-merge | `INTEGRATED` | 既有产线路径；merge 默认禁用属 universal 不变量。 |
| 7 | loopback marshal-server 能 start/cancel/query/restore 真实 Run | `COMPONENT` | API/SSE 套件绿；**跨进程 restart 测试名实不符**（只断言 404，无真实 Run 恢复）；需真实 Run + 非终态恢复证据。 |
| 8 | kill/restart/lost-response/stale/binding-drift 故障注入与恢复测试 | `COMPONENT` | TOP5 故障 fixture 全部闭合，但均为测试 fixture 而非真实生产路径验证；composition root 修复前的 INTEGRATED 口径已撤回。 |
| 9 | macOS/Linux 稳定安装产物；macOS 须签名/notarization/release identity 门禁 | `OPEN` | install.sh + `make dist` + release workflow 就绪；稳定 v1.\* 无签名 fail closed，unsigned 仅允许 prerelease（`a06189c`）；macOS 签名/公证受 **Issue #212** 外部阻塞。 |

## rc 判定规则

- v1.0-rc 前置：`#3` 与 `#4` 必须在真实 composition root 可达且真实 pi 全闭环（`worker.completed`）后才能升级为 `INTEGRATED`；`#7` 需真实 Run 跨进程恢复证据；`#8` 需在真实生产路径复现故障 conformance；`#9` 以 prerelease 通道首发。
- 任何状态切换的条件：真实 composition root 可达 + 证据可复跑；gate-绕过的 test 证据只能支撑 `COMPONENT`，不支撑 `INTEGRATED`。
- 本表与 `docs/roadmap-status.md` 的 R6 行互为指针；composition root 纠偏结论为当前权威基线。

## 当前结论（2026-08-27，composition root 纠偏后）

- `R0: PASSED`；`R1: IN_PROGRESS（INTEGRATED，real Agent in allocation 纵切）`；`R2/R3: IN_PROGRESS（COMPONENT，composition root 修复但未用真实 pi 验证）`；`R4: IN_PROGRESS（COMPONENT）`；`R5: IN_PROGRESS（COMPONENT）`；`R6: IN_PROGRESS（COMPONENT）`。
- 最短剩余路径：真实 pi 全闭环（`TestRealPiStrictE2E` 通过）→ #3/#4 升级 INTEGRATED → #7 真实 Run 跨进程恢复 → #8 真实生产路径故障 conformance → v1.0-rc prerelease。
- 外部阻塞：Issue #212（macOS 签名身份，企业策略）；不影响 prerelease 通道。
- **v1.0 当前不应进入 RC**——composition root 刚修复，真实 pi 全闭环尚未验证。
