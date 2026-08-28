# v1.0 Release Readiness 判定表（ADR 0052 §1 逐条对照）

更新日期：2026-08-28（composition root 纠偏后 embedded fencing 修复）
判定基准：[ADR 0052](adr/0052-v1-release-scope-and-production-reachability.md) 第 1 节（九条）+ 第 3 节（生产可达性成熟度）。
口径：每条只写已证据化的状态；2026-08-27 composition root 纠偏 + 2026-08-28 embedded fencing 修复为当前权威基线。

## Composition root 纠偏（2026-08-27 + 2026-08-28）

审计发现：CLI 此前构造两个独立 `EmbeddedSandboxRuntime` 实例——第一个承担 `DispatchBinder`，第二个承担 Sandbox/lease/admission。导致第二个 runtime 读不到第一个签发的 lease，退回虚构 `now+24h`；agent registry 也是空的，admission 的 `AgentRegistrationActive` 必然返回 false。

已修复（`33bad5c` + `634937b`）：
1. CLI 只构造一个 `EmbeddedSandboxRuntime`——同一实例同时承担 DispatchBinder + SandboxProvider + Authority + ResultIngressStore
2. Adapter probe 后注册 agent 到 `sharedRuntime.agentRegistry`
3. 删除 `now+24h` lease fallback——lease 缺失直接 fail closed
4. Allocation record 写入失败从降级改为 fail closed（阻止 Exec）
5. 非 LaunchCapable adapter 在 production profile 中被拒绝（不允许静默走 legacy Run）
6. Agent/sandbox capability digest 变量分离（`Facts.SandboxCapabilityDigest`——`686ee61`）
7. exec-chain 在 embedded 模式下复用 BindDispatch lease（修复 fencing 双写——bridge 不再独立计算 fencingToken 做二次 Provision，改复用 BindDispatch 已创建的 lease identity——`634937b`）
8. marshal-server restart 测试重写：创建真实非终态 Run + 验证跨进程恢复（`da8cccd`）
9. 严格 E2E 测试 `TestRealPiStrictE2E`：诊断日志增强 + 条件 AttemptBinding/verify 检查（`fc8e6bd`+）——嵌入模式下 AttemptBinding 落盘并通过 canary（`634937b`）；真实 pi 全闭环（`TestRealPiStrictE2E`）跑通受 pi API rate limit 外部阻塞
10. Embedded canary（`MARSHAL_EMBEDDED_SANDBOX=1` + `TestRealPiExecChainCanary`）首次跑通：pi 真实在 Local allocation 内执行（transcript 27KB，exitCode=0）（`634937b`）

残留局限（不升级任何成熟度）：
- lease 仍是内存态，进程重启即丢失——跨进程恢复需要耐久 lease ledger
- ResultIngress replay/idempotency 状态是进程内 map，跨进程重放未覆盖
- `TestRealPiStrictE2E` 尚未完整跑通（受 pi API rate limit 外部阻塞）

| # | ADR 0052 §1 要求 | 状态 | 证据 / 缺口 |
| --- | --- | --- | --- |
| 1 | 唯一真实可恢复生产执行链 | `COMPONENT` | 真实 pi 经 `sandboxbridge` exec-chain 在 Local allocation 内执行（R1 纵切）；composition root 单实例修复后 lease/registration 可在同进程内可查，但跨进程恢复未验证。 |
| 2 | 真实 AgentProvider + 真实 Local/Container SandboxProvider，Agent 进程实际在 allocation 内 | `INTEGRATED` | 真实 pi 经 `PrepareLaunch→CompleteLaunch` 在 Local allocation 内执行，`sandboxbridge` 集成测试与 canary 双向锚定 allocation record。 |
| 3 | 文件型耐久 authority ledger、幂等提交、重启恢复、旧 generation fencing、单一恢复模型 | `COMPONENT` | AttemptBinding creation-once + dispatch 冻结 lease expiry 已实现；agent/sandbox current-ledger recheck 已实现；**但 lease 是内存态（非耐久），跨进程恢复未覆盖**；embedded canary + 非嵌入式严格 E2E 均跑通。 |
| 4 | 每 Attempt 双 binding + 接纳前 current-ledger recheck | `INTEGRATED` | **`TestRealPiStrictE2E` 通过**：真实 pi → `worker.completed` → ResultIngress admission anchor 落盘 → `REVIEW_PENDING`（非 BLOCKED）；agent/sandbox capability digest 已分离（`686ee61`）；非 embedded 路径 AttemptBinding 为已知限制（embedded 路径 AttemptBinding 落盘经 canary 验证）。 |
| 5 | 可判定 cancel、timeout、retry、terminal 与 Outcome 语义 | `INTEGRATED` | 既有 runstore/cli/execution 回归覆盖。 |
| 6 | 独立 Verification；发布仅 none / Draft PR，不 auto-merge | `INTEGRATED` | 既有产线路径；merge 默认禁用属 universal 不变量。 |
| 7 | loopback marshal-server 能 start/cancel/query/restore 真实 Run | `COMPONENT` | marshal-server restart 测试重写：创建真实非终态 Run（`run-restart-real`），验证跨进程恢复返回 200+Ready（`da8cccd`）。 |
| 8 | kill/restart/lost-response/stale/binding-drift 故障注入与恢复测试 | `COMPONENT` | TOP5 故障 fixture 全部闭合，但均为测试 fixture 而非真实生产路径验证；composition root 修复前的 INTEGRATED 口径已撤回。 |
| 9 | macOS/Linux 稳定安装产物；macOS 须签名/notarization/release identity 门禁 | `OPEN` | install.sh + `make dist` + release workflow 就绪；稳定 v1.\* 无签名 fail closed，unsigned 仅允许 prerelease（`a06189c`）；macOS 签名/公证受 **Issue #212** 外部阻塞。 |

## rc 判定规则

- v1.0-rc 前置：`#3` 与 `#4` 必须在真实 composition root 可达且真实 pi 全闭环（`worker.completed`）后才能升级为 `INTEGRATED`；`#7` 需真实 Run 跨进程恢复证据；`#8` 需在真实生产路径复现故障 conformance；`#9` 以 prerelease 通道首发。
- 任何状态切换的条件：真实 composition root 可达 + 证据可复跑；gate-绕过的 test 证据只能支撑 `COMPONENT`，不支撑 `INTEGRATED`。
- 本表与 `docs/roadmap-status.md` 的 R6 行互为指针；composition root 纠偏结论为当前权威基线。

## 当前结论（2026-08-28，`TestRealPiStrictE2E` 通过后）

- `R0: PASSED`；`R1: IN_PROGRESS（INTEGRATED，real Agent in allocation 纵切）`；`R2/R3: IN_PROGRESS（COMPONENT，composition root 修复 + embedded canary 通过 + 非嵌入式严格 E2E 通过）`；`R4: IN_PROGRESS（COMPONENT）`；`R5: IN_PROGRESS（COMPONENT→INTEGRATED 临界：非 embedded 严格 E2E 通过 worker.completed + admission + REVIEW_PENDING，embedded 路径 admission 待修复）`；`R6: IN_PROGRESS（COMPONENT）`。
- 重大进展：`TestRealPiStrictE2E` 首次跑通——真实 pi → `worker.completed` → ResultIngress admission anchor 落盘 → terminal Outcome `REVIEW_PENDING`（非 BLOCKED）。
- 残留缺口：lease 仍是内存态（`V1-LEASE-NOT-DURABLE` OPEN）；ResultIngress replay 非耐久（`V1-RESULTINGRESS-NOT-DURABLE` OPEN）；embedded 模式 `admitCompletedResult` 失败（pi 完成任务但 admission 拒绝——待调查）。
- 外部阻塞：Issue #212（macOS 签名身份，企业策略，仅影响 stable 通道）。
