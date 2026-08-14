# M9 任务拆分设计：从 embedded/local 纵切到 marshal-server

- 文档状态：设计文档（非 ADR），本身不冻结契约；供 Lead 依据本文档另行制作 M9 任务 spec 的唯一设计依据；M9 七交付已全部合入 main（2026-08-14，状态与 PR 证据见 [roadmap-status.md](roadmap-status.md) 与 §2）
- 权威基线：main HEAD `f571547a4241e8902299f54fb67193b18c703335` 与 M8 已合入现实
- 日期：2026-08-13
- 取代对象：三份已失效 M9 草案（见 §0）

## 0. 背景：为什么重写 M9 任务拆分

`.marshal/input/` 下三份 M9 草案已校验为不可派发：

1. `m9-0-pull-sandbox-runner-r1-task.json`
2. `m9-0-secure-sandbox-transport-r1-task.json`
3. `m9-2-public-api-contract-r1-task.json`

失效原因：

- 锚定不存在的 `internal/sandbox`、`internal/dispatch` 等包形态，锚定对象与 M8 合入后的实际包结构不一致；
- 引用未接受的 ADR 0020 子编号，构成未接受设计依据。

因此基于 M8 已合入现实整体重写 M9 任务拆分。本文档是 M9 任务 spec 的唯一设计依据；实际 spec 文件由 Lead 依据本文档另行制作，本文档不产生可派发任务，也不修改 `.marshal/input/**`。

## 1. M9 目标复述与 Port 拓扑对照

### 1.1 M9 目标复述（roadmap-status）

[roadmap-status.md](roadmap-status.md) 中 M9 为“**marshal-server、Public API 与 Durable Runtime**”，状态 `PASSED`（2026-08-14，设计与契约+本 milestone 交付门禁通过），证据为七交付 PR 全部合入 main 且各 PR 远端 CI 全绿（见 §2）。对应条款锚点：

- ADR 0016 §9 路线：M9 = Durable Runtime（submit API、inbox/outbox、dispatcher、heartbeat/fencing、kill/restart recovery）；
- ADR 0016 §5：耐久调度经可替换 DurableExecutionEngine Port 外包，生产参考 backend 为 Temporal，Core 保留生命周期权威，backend 不是业务权威；
- ADR 0018 §1：C/S 终态——Marshal Control Plane 运行于常驻 `marshal-server` 进程，Execution Plane 分离可远程，Core 是唯一业务权威；
- ADR 0018 §8：M9 冻结 marshal-server、Public API、Sandbox dispatch Push-Pull、远程注册、SSE 与 DurableExecutionEngine；HTTP/JSON + OpenAPI 首发；WebSocket/gRPC 推迟，引入须另行 ADR。

### 1.2 ADR 0016 §4 Port 分层对照

ADR 0016 §4 冻结两个职责不得混合的 Port：

| Port | 冻结职责 | 与 M9 的关系 |
| --- | --- | --- |
| `AgentAdapter` | Agent 协议 prepare/decode/capability，不含执行环境语义 | M9 不改动 |
| `SandboxProvider` | 执行环境全生命周期：`Probe`/`Provision`/`Stage`/`Exec`/`Inspect`/`Signal`/`Checkpoint`/`Restore`/`Terminate`/`Reconcile` 十操作 SPI | M8 已合入 `internal/sandbox`；M9-d 双拓扑 conformance 锚定该 SPI |

### 1.3 ADR 0018 §3 Port 身份矩阵对照

ADR 0018 §3 冻结身份按 Port 分流（不设 universal envelope）。矩阵覆盖六类 Port，M9 范围内涉及：

| Port | 归属 M9 任务 | 关键约束（ADR 0018 §3） |
| --- | --- | --- |
| public-api | M9-c | 禁止携带 `providerType`；携带 `workloadRole`/`allocationId`/`generation`/`fencingToken`/DispatchLease 的请求一律拒绝 fail closed |
| provider-registration/control | M9-c（远程注册） | 同样拒绝 workload lease 字段；由 Core 将获准事实写入 authority ledger |
| dispatch-bound Sandbox/Agent/Verification | M9-a / M9-b / M9-d | 唯一可绑定完整 lease 身份的 Port |
| publication | M9-b（PublicationAuthorization 签发侧） | 绑定 SideEffectIntent/ReviewDecision/evidence digest；不得宣布 gate/ReviewDecision |
| artifact / secret | M9 wire 范围外（M12 扩展） | — |

### 1.4 M9 的 Port 拓扑结果

M9 的终态拓扑一句话概括：CLI/Web/GitHub App/CI 一律作为 Public API client 经 public-api Port 接入常驻 `marshal-server`；Core 经 dispatch-bound Port 以 Push/Pull 双拓扑调度 Execution Plane，跨信任域访问只经三条 Core-only typed edge 授权，耐久调度经 DurableExecutionEngine 单一权威 seam；embedded/local 单二进制形态保留，embedded CLI 经 in-process adapter 调同一 Public application Port、不直写 store。

## 2. M8 已合入现实盘点（设计输入）

M9 全部任务锚定以下已合入包，spec 不得锚定该清单之外的包或符号：

| 已合入包 | 现实 | M9 锚点 |
| --- | --- | --- |
| `internal/sandbox` | SandboxProvider 十操作 SPI + `SandboxAllocation` + `OperationIdentity`（workloadRole 封闭枚举 worker/verifier、replay fencing）+ 内容寻址 `StageInput` + Fake Provider + conformance 套件（outcome/invariant equivalence 口径） | M9-d 在同一套件上扩展双拓扑比较 |
| `internal/sandbox/local` | LocalRunner 宿主进程 Provider（Provision/Stage/Exec/Restore/Reconcile；Local 永不 hardened） | M9-a/c/e 的回归与恢复口径载体 |
| `internal/dispatch` | `DispatchLease` + `Matcher`：Claim 六步 fail-closed、Revalidate current-ledger recheck、ValidateLeaseFencing；**唯一 claim 索引 `issuedLeases` 现为内存态，源码注释明确“Persisting this ledger is M9”** | M9-a 的直接锚点 |
| `internal/provider` | ProviderRegistration durable store + eligibility（`EvaluateProviderEligibility`/`IsHardenedEligible`）+ legacy fail-closed mapper | M9-a/b 的只读依赖 |
| `internal/authority` | 双键空间（`AuthorityNamespaceId`/`SecurityDomainId`）+ SideEffect 类型 + 三条 typed edge 记录层（`DispatchResultCapability`/`MaterialAccessGrant`/`PublicationAuthorization`，含 `Validate`/`Digest`/`ReplayKey`/`ValidAt`；**运行时未接线，源码注释明确接线留给 M9**） | M9-b 的直接锚点 |
| `internal/runstore` | 状态转换通知钩子（`MARSHAL_NOTIFY_CMD`） | M9-c SSE 只读投影的事件源 |

上述盘点中原“未合入但在途或待启动”各项的现状（截至 2026-08-14）：

- 任务 C：Provider Port 绑定 + embedded E2E——已合入 main（M9-a/M9-b 的合入前置，与 M9 的衔接点见 §4）；
- typed edge 运行时接线——即 M9-b，已合入 main（PR #107）；
- lease 持久账本——即 M9-a，已合入 main（PR #104）。

## 3. M9 任务拆分（重写）

### 3.0 依赖排序总览

```text
任务 C（在途：Provider Port 绑定 + embedded E2E）
│
├─▶ M9-a lease 持久账本与 crash recovery ──┐ 可并行，写域分离（§4）
├─▶ M9-b typed edge 运行时接线 ────────────┤
│                                          ▼
│          M9-c marshal-server 常驻 + Public API + 远程注册
│                          │
│              ┌───────────┴───────────┐
│              ▼                       ▼
│   M9-d Push/Pull 双拓扑 conformance   M9-e DurableExecutionEngine seam
└────────────（M9-d / M9-e 可并行，写域分离）
```

排序理由：M9-a/b 直接锚定两个 M8 已合入包（§2），在任务 C 合入后即可并行启动；M9-c 的服务端启动恢复依赖 M9-a 的账本、结果接纳依赖 M9-b 的 edge recheck，故排在其后；M9-c 冻结 versioned protocol family 后，M9-d（transport adapter + conformance）与 M9-e（engine seam）写域不相交，可并行。每个任务按“目标 / 写域预估 / 前置 / 验收要点 / 风险”给出。

### 3.1 M9-a：lease 持久账本与 crash recovery

**目标**

- DispatchLease 持久化：以 authority ledger 事实取代 `Matcher.issuedLeases` 内存索引；lease 的 claim/ack/cancel/expiry/generation bump 全部进入 append-only event ledger，registry/队列视图是可重建投影；
- crash recovery：恢复只基于持久事件账本，Inspect/Reconcile 比较账本、DispatchLease 与执行体状态后选择合法转换（ADR 0016 §6 口径：kill -9 Runtime 后 60 秒内完成 Inspect/Reconcile，旧 execution handle 上报被 fencing 拒绝，不得双写、不得丢证据）；
- generation bump 原子 sink：按 ADR 0018 §13 实现同事务 atomic compare-and-append/transaction，ledger transition、当前 lease generation 与 Evidence/Artifact 引用在同一原子步骤内校验提交。

**写域预估**

- `internal/dispatch`：lease 账本存储与唯一 claim 索引（claim key `(runId, attemptId)`）持久化；
- 账本原子 sink 共享模块：接口先行冻结，供 M9-b 消费（写域边界见 §4）；
- lease 事件类型与机器可读原因码：遵循 ADR 0018 §6——cancel/expiry、drain deadline 到期、kill 等事件携带机器可读原因码，与人可读审计记录分开。

**前置**

- 任务 C 合入（写域边界稳定，事件契约与 sink 接口先行冻结，见 §4）；
- M8 gate-4 durable registration store（已合入）作为只读依赖。

**验收要点**

- 重启/重建后 lease 账本从 ledger 恢复，唯一 claim 不变量在重启后保持：同一 `(runId, attemptId)` 不重发，无论原 lease 是否仍 active 或已失去资格；
- lost-response、concurrent-write、old-generation overwrite fixture 齐备（ADR 0018 §7/§13）：陈旧 generation 不得先覆盖对象 key 再被 ledger 拒绝，冲突 bytes 只进 quarantine namespace；
- claim 后失效（registration revoke/expire/incompatible、snapshot supersede/expire、evidence revoke/expire）使在途 lease 立即失去资格：cancel/expiry + generation bump/fencing，Allocation/Attempt 终止对账，晚到结果隔离为诊断材料；继续执行只能新 Attempt + 新 lease 重新 match；
- kill -9 恢复 60 秒口径在 Local/Fake 拓扑通过。

**风险**

- 原子 sink 是 M9-a/M9-b 的公共写域点，接口未先冻结会造成互相阻塞或返工（缓解：§4 gate-0 契约冻结）；
- crash recovery fixture 面广，且 Local 永不 hardened，hardened 恢复口径只能在 Fake Provider 拓扑验证，解读 conformance 结果时需注意拓扑限定。

### 3.2 M9-b：typed edge 运行时接线（签发/撤销/current-ledger recheck）

**目标**

将 `internal/authority` 三条 typed edge 记录层接入运行时（现为 record layer only）：

- 签发：Core 独占签发，issuer 恒为 Core `AuthorityNamespaceId`；DispatchResultCapability 随 claim 签发并绑定 lease（attempt/allocation/generation/fencingToken），MaterialAccessGrant 按物料申请签发（expiry 以 Attempt 边界为界），PublicationAuthorization 随发布决定签发（绑定 SideEffectIntent/ReviewDecision/evidence digest，Draft-only、merge-never）；
- 撤销：撤销是 authority ledger 事实；security-critical revoke 即时生效（立即 cancel + generation bump + kill，不留 drain 窗口）；planned/ordinary upgrade 走新 registration/snapshot + stop-new + bounded drain；
- current-ledger recheck：每次使用按当前 authority ledger 复核——edge active、digest 相符、未被撤销/过期、target actor 的 registration/snapshot/evidence 仍 eligible、绑定的 Attempt/allocation/lease 仍 active，任一项不满足 fail closed；派生 token/handle 只是指向 edge 权威记录的单向引用，不得成为第二权威。

**写域预估**

- `internal/authority`：edge 签发/撤销/recheck 运行时（不改动记录层结构与其冻结的 `Validate`/`Digest`/`ReplayKey`/`ValidAt` 语义）；
- dispatch claim 路径与结果接纳（result-ingress）路径：接入 DispatchResultCapability 的签发与 recheck；
- fixture：按 ADR 0018 §7 的 positive/negative 矩阵，明确区分三类合法 positive 与无 edge、错 edge、错绑定 negative。

**前置**

- 任务 C 合入；
- M9-a 原子 sink 接口冻结（edge 签发/撤销是 authority ledger 事实，须经同一 sink）；M9-a/b 并行方式：M9-b 先对冻结的 sink 接口开发，sink 实现合入后切换。

**验收要点**

- 伪造 issuer 或冒用签发身份、错 authority scope、错 source/target（含 target substitution）、错 operation、错 attempt/allocation、已过期、已撤销、digest 替换的 edge 一律拒绝并写入审计；
- 绕过 current-ledger recheck 使用派生 token/handle、转授、扩权、跨 Port token/schema 复用、携带 raw handle/credential 跨域复用的请求一律失败；
- 合法三条 edge 可恢复且幂等（edge 引用 + operation 请求摘要构成 canonical replay key，重复请求幂等归并）；
- Public API/SSE 客户端与 Core 内部权威对象引用不持有 Provider typed edge 的合法访问 fixture 必须通过；
- securityDomainId 相同不构成授权：同域 bearer 化请求（跳过 principal/registrationId/attempt/allocation/generation/operation 门禁）必须失败。

**风险**

- 口径差异：记录层现冻结的 operation 封闭枚举（`dispatch-result-read`/`dispatch-result-accept`、`material-read`/`material-write`、`publication-submit`/`publication-checks-read`）与 ADR 0018 §3 描述的完整 operation 枚举（result/log/checkpoint/candidate/evidence-ref/heartbeat/receipt 等）存在口径差：M9-b 不得静默改动已冻结记录层；若需完整枚举，由 Lead 在 spec 阶段决策走 Schema 版本化扩展还是以记录层现枚举为 v1 冻结，决策记录入 spec；
- edge 签发与 lease claim 处于同一写路径，事务边界未对齐 M9-a sink 会出现双写窗口（缓解：统一经原子 sink，与 M9-a 共享 gate-0 契约）。

### 3.3 M9-c：marshal-server 常驻 + Public API（versioned HTTP/JSON + SSE）

**目标**

- 常驻 `marshal-server`：Control Plane 常驻运行，Core 是唯一业务权威，不向 Provider 暴露业务状态写 API；embedded/local 单二进制形态保留——embedded CLI 经 in-process adapter 调同一 Public application Port，不直写 store；
- Public API versioned HTTP/JSON + OpenAPI 首发：Task create/get/cancel、Run approval/status、events/evidence；幂等身份 `(authorityNamespaceId, scope, idempotencyKey, requestDigest)`——三者相同幂等归并，同 key 不同 digest 冲突 fail closed；
- SSE cursor/resync：cursor 身份 `authorityNamespaceId`+`scope`+`ledgerSequence`（订阅方另绑定自身 securityDomainId 完成授权判定）；`eventId`/sequence 断线续传去重（at-least-once）；cursor 过期/gap/压缩返回 deterministic resync 起点与 snapshot digest，不得静默续推；轮询 fallback；SSE 是只读投影，不承载业务 ACK、lease heartbeat 或 command 下发；
- 远程注册（provider-registration/control Port）随 ADR 0018 §12 TLS 基线与 trust root 链校验一并 enable。

**写域预估**

- `cmd/marshal-server` 入口与 Public application Port 模块（新增）；
- `internal/runstore` 状态转换通知钩子（`MARSHAL_NOTIFY_CMD`）→ SSE 只读投影适配；
- OpenAPI 文档、版本化 error 模型、幂等/replay 权威记录（authorityNamespaceId 拥有）。

**前置**

- M9-a 合入：服务启动与 crash 恢复从 lease 持久账本重建；
- M9-b 合入：结果接纳路径具备 edge recheck。

**验收要点**

- public-api 请求携带 `providerType` 被拒绝；携带 `workloadRole`/`allocationId`/`generation`/`fencingToken`/DispatchLease 的请求 fail closed（ADR 0018 §3 矩阵）；
- 幂等三元归并与同 key 不同 digest 冲突 fixture；
- SSE 断线续传、resync 确定性、heartbeat、有界 backpressure（超限断开引导 resync、不阻塞 ledger 写入）、周期性 re-Authorization 与敏感变更即时 re-Authorization（校验失败 fail closed 关闭连接）；
- 一切非 loopback/in-process transport 首次 enable 即满足 TLS + 双向身份校验 + replay protection（ADR 0018 §12，不得推迟到 M11）；
- Local MVP 全量回归通过，embedded 模式不回归。

**风险**

- SSE 具体参数值（heartbeat 间隔、缓冲上限、resync 错误码）由 ADR 0018 §14 留给 M9 Schema 冻结：OpenAPI 未先行冻结则 resync 行为不可判定；
- SSE 投影与 runstore 通知钩子的衔接面广：必须保证投影可凭 ledger 重建、不构成第二权威；
- Public API 面宽（Task/Run/events/evidence 多端点），版本化 error 模型与幂等约定需在 spec 阶段逐端点冻结，避免实现后破坏性改版。

### 3.4 M9-d：Push/Pull 双拓扑 transport 与 outcome/invariant equivalence conformance

**目标**

- dispatch-bound Port 增加 Push HTTP 与 Pull outbound runner 两类 transport adapter：embedded/in-process、Push、Pull 是同一 versioned protocol family 内的 transport/topology adapter，运行该族统一的 conformance suite，Port 语义不随 transport 变化（ADR 0018 §2/§16）；
- conformance 口径冻结为 outcome/invariant equivalence：只冻结唯一 claim、eligibility、fencing、deadline（ack/heartbeat/expiry）、无双活、晚到结果隔离；允许拓扑特定的 offer/poll/claim/ack transition 与 timing；比较 normalized business trace 与业务不变量，不比较逐步 wire trace；
- 交付故障注入口径（ADR 0017 对 M9 的义务：两拓扑 outcome/invariant equivalence conformance 与故障注入口径），扩展 `internal/sandbox` 现有故障注入（如 drop-response）到双拓扑。

**写域预估**

- `internal/sandbox` conformance 套件扩展：normalized business trace 比较器、业务不变量断言、双拓扑故障注入；
- dispatch-bound Port 的 Push/Pull transport adapter（versioned HTTP/JSON，同一 protocol family，不派生不同协议版本或语义分支）；
- 不改动 SandboxProvider 十操作 SPI 契约与既有 Fake/Local Provider。

**前置**

- M9-c 合入：dispatch-bound Port 的 versioned protocol family 已冻结并可派发；
- M9-a 合入：lease 持久账本支撑双拓扑 claim/reconcile 与跨拓扑唯一 claim 判定。

**验收要点**

- Push/Pull 两拓扑运行同一 conformance suite，outcome/invariant 等价；normalized business trace 格式先行冻结；
- 各类 claim 后失效（revoke/expire/incompatible/supersede/evidence revoke）的 Push/Pull fixture 齐备（ADR 0018 §7）：在途 lease 失去资格并对账，不得续租或降级；
- 跨拓扑唯一 claim、晚到结果隔离、无双活不变量通过；
- 不得为 Push 定义弱化不变量的简化协议，不得为 Pull 放宽 capability match（capability match 仍只消费持久 ProviderCapabilitySnapshot）。

**风险**

- “拓扑特定 transition/timing”与不变量等价的边界易漂移：normalized business trace 格式必须在实现前冻结为 spec 附件；
- Pull 拓扑 runner outbound-only 需同时满足 §12 TLS 基线与 replay protection，credential 不入业务 JSON/事件/日志/digest；
- 双拓扑使 fixture 数量倍增，需控制与既有单拓扑 conformance 的重复维护成本（以同一 suite 参数化拓扑，不另起 suite）。

### 3.5 M9-e：DurableExecutionEngine backend seam（Temporal/Local Engine）

**目标**

- 实现 ADR 0018 §15 单一权威 seam：同事务 outbox 或 ledger-derived Core command journal 二选一，不得同时维护两套可分歧的出站事实；`commandId` 从 ledger 权威事实稳定派生；
- Local Engine（embedded/单机 in-process）作为首个完整 backend；Temporal backend 交付 profile 声明与一致性 fixture（ADR 0016 §5：Temporal 是生产参考 backend，但不得成为 Core 必选依赖，替换 backend 不改变生命周期语义）；
- 权威边界：backend 只消费 command 并回报 receipt，承担相同 commandId 的 at-least-once delivery、timer wakeup、signal transport 与 crash recovery；workflow/activity state 不是业务权威；delivery/activity retry 不创建 Attempt、不消费业务重试预算。

**写域预估**

- DurableExecutionEngine 内部 Port 模块（seam + backend 接口，新增；该 Port 是 Core 内部 Port，backend 不是 Provider 信任域成员）；
- Local Engine backend 实现；
- Temporal backend profile 声明与升级 fixture：workflow versioning/build ID、Continue-As-New、payload 外置与上限、activity heartbeat/cancel/retry。

**前置**

- M9-a 合入：outbox 与 ledger 同事务原子提交（或 journal 由 ledger 确定性派生并可重放），依赖原子 sink；
- M9-c 合入：常驻服务端载体与 command 生命周期观测。

**验收要点**

- 无双写窗口：“ledger 已提交而 command 未投递”与“command 已投递而 ledger 未提交”两态均不可达；crash/升级恢复按单一 seam 重放，未投递 command 由 outbox/journal 重新派生，不依赖 backend 内部状态；
- 同一 commandId 重复投递/消费幂等；
- backend 宣布 lifecycle/ReviewDecision/rework/终态或 safe-to-publish 的 negative fixture 一律失败；
- 升级 fixture 覆盖 workflow versioning/build ID、Continue-As-New、payload 外置与上限、activity heartbeat/cancel/retry（ADR 0018 §8/§15）；
- backend profile 声明所选 seam（outbox 或 journal）并有对应一致性 fixture 守护。

**风险**

- outbox vs journal 二选一是近乎不可逆的设计决策，选错需重做账本写路径：Lead 应在 spec 阶段结合 M9-a sink 实现产出两者比较评估并冻结入 spec；
- Local Engine 必须是完整一等 backend 而非 Temporal stub；反向地不得把 Temporal 变成 Core 必选依赖（ADR 0016 §7），两者均由一致性测试守护；
- Temporal dev server + SQLite/local blob adapter 只作为单机开发形态（ADR 0016 §5），生产 PostgreSQL/S3 存储形态属 M11，M9-e 不得提前引入生产存储依赖。

## 4. 与任务 C 的衔接点

任务 C（Provider Port 绑定 + embedded E2E）已合入 main（截至 2026-08-14，为 M9-a/M9-b 的合入前置）。衔接口径：

1. **并行启动点**：任务 C 合入后，M9-a 与 M9-b 可并行启动。
2. **写域边界**（并行不冲突口径）：
   - M9-a：`internal/dispatch` lease 账本持久化 + 原子 sink（lease 侧）；对 `internal/provider` registration store 只读消费；
   - M9-b：`internal/authority` typed edge 运行时（签发/撤销/recheck）+ 结果接纳接线；对 registration store 与 lease 记录只读消费；
   - 共享契约：事件契约与原子 sink 接口——任务 C 合入后作为两任务的 gate-0 先行冻结；任一任务不得单方面修改对方的账本事件类型；
   - 两任务均不得修改 M8 已冻结契约（§7 非目标清单）。
3. **回归基线衔接**：任务 C 的 embedded E2E 是 M9-c 的回归基线——marshal-server 常驻后，embedded/local 模式必须经 in-process adapter 运行同一 E2E，Local MVP 全量回归不回归。
4. **基线漂移处理**：若任务 C 实际合入 diff 引入新的 Port 绑定契约，Lead 制作 spec 前须以合入 commit 为准复核 M9-a/b 写域边界；本文档 §2 盘点在任务 C 合入后按其实际 merge commit 增补。

## 5. ADR 0027（Candidate 一等记录，Proposed）与 M9 的交互

ADR 0027 当前为 `Proposed`，未接受。处理口径：

- **接受前**：M9 各任务不引入任何 ADR 0027 概念。Candidate 继续按 ADR 0018 §13 既有口径处理——Candidate bytes 接纳关系归 authority ledger，对象 key 使用 `authorityNamespaceId`+run+attempt+allocation+generation scoped immutable key，digest-verified put-if-absent，陈旧/冲突 bytes 只进 quarantine namespace；
- **若在 M9 期间被接受**：Candidate 升级为一等权威记录，对 verification 归一化链的影响是——验证归一化链（worker 产物 → Candidate → Evidence/ReviewDecision）需将 Candidate 记录作为有自身生命周期的 ledger 事实，归一化断言锚定 Candidate 记录而非仅 byte 接纳。受影响的 M9 任务：
  - M9-a：原子 sink 接纳范围从 Candidate bytes 扩展到 Candidate 记录引用（ADR 0018 §13 的接纳同原子要求随之覆盖记录层）；
  - M9-b：DispatchResultCapability 的接纳 recheck 与 Candidate 记录的绑定（若 ADR 0027 改变接纳口径）；
  - M9-c：Public API events/evidence 面可能需暴露 Candidate 记录事件（受已冻结 OpenAPI 版本化约束，不破坏已冻结字段）；
- **程序约束**：ADR 0027 接受后相应变更才可经修订进入 M9 任务 spec；接受前一律按非目标处理。

## 6. 风险清单

| # | 风险 | 影响任务 | 缓解 |
| --- | --- | --- | --- |
| 1 | 原子 sink/事件契约是 M9-a/M9-b 公共写域点，接口未先冻结会互相阻塞 | M9-a/M9-b | 任务 C 合入后 gate-0 先行冻结契约（§4） |
| 2 | typed edge 记录层 operation 枚举与 ADR 0018 §3 完整枚举存在口径差 | M9-b | Lead spec 阶段决策并记录；不静默改已冻结记录层（§3.2） |
| 3 | SSE 参数值未冻结（ADR 0018 §14 留给 M9） | M9-c | OpenAPI 先冻结参数再 enable SSE（§3.3） |
| 4 | Push/Pull normalized business trace 格式漂移 | M9-d | 格式先冻结为 spec 附件再实现 adapter（§3.4） |
| 5 | outbox vs journal 二选一近乎不可逆 | M9-e | spec 阶段产出比较评估并冻结（§3.5） |
| 6 | 任务 C 在途，锚定对象可能变化 | M9-a/b | 全部 spec 以任务 C 实际合入 commit 为准（§4） |
| 7 | 远程 transport 首次 enable 安全基线义务（TLS/双向身份/replay protection） | M9-c/M9-d | §12 基线随 enable 交付，不得推迟到 M11 |
| 8 | Local MVP 回归纪律 | 全部 | 每个 M9 任务先通过 Local MVP 全量回归 |
| 9 | 提前引入未接受 ADR（ADR 0020 系列、ADR 0027）概念 | 全部 | §7 非目标 + spec 审查门禁 |

## 7. 非目标

- **不改 M8 已冻结契约**：SandboxProvider 十操作 SPI 与其请求/回执类型、`OperationIdentity` 与 workloadRole 封闭枚举、`DispatchLease` 字段绑定与 Claim 六步/Revalidate/ValidateLeaseFencing 语义、三条 typed edge 记录结构（`Validate`/`Digest`/`ReplayKey`/`ValidAt`）、ProviderRegistration 幂等身份与 eligibility 门禁、authority 双键空间（`AuthorityNamespaceId`/`SecurityDomainId`）结构、legacy mapper fail-closed 口径；
- **不提前引入未接受 ADR 的概念**：不实现 ADR 0020 系列草案或 ADR 0027 接受前的设计；
- **不实现后续 Milestone 范围**：M10 Cloudflare Provider、M11 HA/多节点/多用户 AuthN/AuthZ 与生产远程入口治理、M12 其余 Provider wire/SDK 与多语言 SDK、M13 Goal orchestration；
- **不引入 WebSocket/gRPC**：首发 versioned HTTP/JSON，新传输引入须另行 ADR；
- **不自研 workflow engine，不制造第二业务权威**：registry/queue/SSE/backend state 一律是可重建投影；
- **不使 Local hardened**：LocalRunner 是宿主进程 Provider，Local 永不 hardened；
- **本文档不产生可派发任务 spec**：实际 spec 由 Lead 另行制作；本文档不修改 `.marshal/input/**`。

## 8. 术语一致性

本文档术语与已接受 ADR 及已合入代码一致：双键空间（ADR 0018 §10）、六类 Port 与身份矩阵（ADR 0018 §3）、三条 Core-only typed edge（ADR 0018 §3）、outcome/invariant equivalence（ADR 0018 §16）、单一权威 seam（ADR 0018 §15）、原子 fencing 写入汇与 quarantine namespace（ADR 0018 §13）、SSE 恢复与再授权（ADR 0018 §14）、十操作 SPI（ADR 0016 §4）、kill -9 恢复口径（ADR 0016 §6）；transport 拓扑术语统一使用 Push/Pull/embedded。本文档措辞与已接受 ADR 不一致时以 ADR 为准。
