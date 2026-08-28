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
| 3 | 文件型耐久 authority ledger、幂等提交、重启恢复、旧 generation fencing、单一恢复模型 | `COMPONENT` | AttemptBinding creation-once + dispatch 冻结 lease expiry 代码已实现；**但默认路径退回 seed admission、lease/agent-registry/ResultIngress replay 均进程内态、admission 与 journal 非同事务**——R2 持久权威纵切未闭合。 |
| 4 | 每 Attempt 双 binding + 接纳前 current-ledger recheck | `COMPONENT` | 双 digest 分离（`686ee61`）+ registration ID 前缀（`3f8638d`）+ agent registration fallback（`cad8773`）已落地；真实 pi 两路径产出 `worker.completed` + admission anchor + embedded AttemptBinding；**但严格 E2E 为假阳性（未强制生产 profile/未真实断言终态），INTEGRATED 口径撤回**，待 R2 纵切 + fail-closed E2E。 |
| 5 | 可判定 cancel、timeout、retry、terminal 与 Outcome 语义 | `INTEGRATED` | 既有 runstore/cli/execution 回归覆盖。 |
| 6 | 独立 Verification；发布仅 none / Draft PR，不 auto-merge | `INTEGRATED` | 既有产线路径；merge 默认禁用属 universal 不变量。 |
| 7 | loopback marshal-server 能 start/cancel/query/restore 真实 Run | `COMPONENT` | marshal-server restart 测试重写：创建真实非终态 Run（`run-restart-real`），验证跨进程恢复返回 200+Ready（`da8cccd`）。 |
| 8 | kill/restart/lost-response/stale/binding-drift 故障注入与恢复测试 | `COMPONENT` | TOP5 故障 fixture 全部闭合，但均为测试 fixture 而非真实生产路径验证；composition root 修复前的 INTEGRATED 口径已撤回。 |
| 9 | macOS/Linux 稳定安装产物；macOS 须签名/notarization/release identity 门禁 | `OPEN` | install.sh + `make dist` + release workflow 就绪；稳定 v1.\* 无签名 fail closed，unsigned 仅允许 prerelease（`a06189c`）；macOS 签名/公证受 **Issue #212** 外部阻塞。 |

## rc 判定规则

- v1.0-rc 前置：`#3` 与 `#4` 必须在真实 composition root 可达且真实 pi 全闭环（`worker.completed`）后才能升级为 `INTEGRATED`；`#7` 需真实 Run 跨进程恢复证据；`#8` 需在真实生产路径复现故障 conformance；`#9` 以 prerelease 通道首发。
- 任何状态切换的条件：真实 composition root 可达 + 证据可复跑；gate-绕过的 test 证据只能支撑 `COMPONENT`，不支撑 `INTEGRATED`。
- 本表与 `docs/roadmap-status.md` 的 R6 行互为指针；composition root 纠偏结论为当前权威基线。

## 当前结论（2026-08-28，维护者审计纠偏：严格 E2E 为假阳性，撤回超前口径）

> **2026-08-28 维护者审计结论（权威）**：顶层方向正确，但实施横向扩散。`TestRealPiStrictE2E` **当前是假阳性**——它未强制 embedded/production profile、`verify` 失败只记录不 fail、不断言精确 terminal Outcome、server 只启动 goroutine 未真正 stop/restart/query/restore、非 embedded 模式缺 AttemptBinding 也不失败。因此它**不能**作为 R2 durable authority / R4 recovery / R5 cutover 的证据，更不能作为 RC 门禁。此前把 #4 与 R5 标为 `INTEGRATED` 的口径**撤回**。真实偏航点：(1) 持久权威纵切未闭合——durable authority 仅 `MARSHAL_EMBEDDED_SANDBOX=1` 才注入，默认路径退回 seed admission；lease/agent registry 仍为进程内 map；ResultIngress replay/quarantine/sequence 仍在内存；admission、worker-result、Run journal 非同事务。(2) Worker 生产路径单点依赖 Pi（唯一 `LaunchCapable`）。

- `R0: PASSED`；`R1: IN_PROGRESS（INTEGRATED，real Agent in allocation 纵切——真实 pi 在 Local allocation 内执行成立）`；`R2: IN_PROGRESS（COMPONENT，durable authority 仅门控注入，默认路径退回 seed；lease/agent-registry/ResultIngress replay 均内存态；admission 与 journal 非同事务）`；`R3: IN_PROGRESS（COMPONENT）`；`R4: IN_PROGRESS（COMPONENT）`；`R5: IN_PROGRESS（COMPONENT——严格 E2E 假阳性撤回，未达 INTEGRATED）`；`R6: PLANNED（DESIGN）`。
- **最短纠偏路线（维护者定序）**：(1) 修 `gofmt` 恢复 CI 全绿（已由 `3f8638d`/`ec4c5c4` 完成，CI `33135979818` 绿）；(2) 暂停 R4–R6 横向组件，集中完成**一个 R2 纵切**：lease/generation/fencing 持久化、agent registration/snapshot 持久化、ResultIngress replay/quarantine 持久化、admission+结果引用+Run journal 同事务、生产 profile 删除 seed fallback；(3) 严格 E2E 改为真 fail-closed（真实进程重启、replay、stale/ABA、精确 Outcome）；(4) 只在 Qwen 或 Qoder 中选**一个**补 `LaunchCapable` 解除 Pi 单点；(5) R2/R3 达 `INTEGRATED` 后再恢复 R4/R5，最后 R6 prerelease。
- 真实已达成的执行链事实（不作 INTEGRATED 证据，仅记录）：真实 pi 在 embedded 与非嵌入式两路径均产出 `worker.completed` + admission anchor + allocation record + embedded AttemptBinding 落盘；但这是在**未强制生产 profile、未真实断言终态**的测试下取得，属组件级执行链证明。
- 外部阻塞：Issue #212（macOS 签名/EDR，企业策略，仅约束稳定版，不阻塞 prerelease 通道）。
