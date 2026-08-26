# 交付监督与吞吐止损

> **何时必须读取：** 启动或监督多个 Worker、调整交付 WIP、处理重复失败或 review 积压、判断 slice 是否推进 production dependency graph/roadmap exit criterion，或基于真实实践优化 Marshal Skill 时，必须完整读取。

## Supervisor 边界

Supervisor 是 Lead 控制面的只读观察职责，可由独立只读 Agent 承担，但不是 Core lifecycle authority、Publisher、reviewer 或作者。它只能读取已有状态和给 Lead 建议，不能：

- 调用 `task run/verify/review/publish/accept/reconcile`、`marshal supervise` 或托管平台写 API；
- 形成/导入 ReviewDecision、关闭 finding、批准 merge 或把自身观察当权威验证；
- 编辑 author worktree、手写 `.marshal`、终止/重试 Worker，或 kill/reap 任意进程。

`intervene` 只表示向 Lead 给出高优先级止损建议和证据，不授权 Supervisor 自行改变生命周期。Lead 仍须依据 Core/CLI、权限与相应 reference 执行动作。

## 每个 heartbeat 的只读快照

多 Worker 交付期间，Supervisor 对当前 cohort 一次性收集并比较上一轮：

- 每个 work item 的 `sourceHead`、worktree、唯一 writer、allowPaths/denyPaths、blocked goal item/exit criterion；
- Worker 的 Core state、process ownership、最近一次可观察 progress、当前 failure signature 和 Provider backpressure；
- slice 在 production dependency graph 中的入口、经过的真实调用链、可观察结果和仍缺失的边；
- review queue 数量/年龄、shared reviewer 是否空闲、rework round 与未关闭 P0/P1；
- baseline 是否陈旧、触及路径是否已被 main 改变、当前容量和 scope 冲突。

只读信号优先使用 `marshal task status --json`、`marshal doctor --json`、watchdog `--once --json`、有界 events tail、`git worktree list --porcelain`、`git status --short`、`git diff --stat` 和 roadmap/issue 的明确退出条件。日志、报告和提醒不得输出 secret 或不必要的绝对用户路径。

“有进程”不等于“有进展”。Progress 必须是可观察状态/证据变化、预期产物增量、测试结果或明确 blocker；固定状态和相同 failure signature 连续出现只算停滞。

Stop-loss 的时间与停滞判定遵循 [watchdog-and-capacity.md](watchdog-and-capacity.md)：只消费 numeric seconds/JSON；watchdog JSON 的 `RUNNING + processOwnership=owned-active + ownershipSource=marshal-lease + argvMatched=true` 继续 `monitor`，`ErrLeaseHeld` 路由给 owner，停滞摘要使用 `phaseProgressDigest`。Supervisor 不得人工解析 `ps etime`，也不得把 intervention 建议执行成外部 kill 或重复 abort。

## 四类只读建议

| 建议 | 触发条件 | Lead 的预期响应 |
| --- | --- | --- |
| `continue` | scope 互斥、依赖满足、纵切向 exit criterion 前进、review 容量可用且无重复 failure | 保持当前 WIP，不拆新微切片 |
| `freeze-fanout` | scope/依赖冲突、review queue 已有待审或超过一个 heartbeat、同 signature 第二次出现、容量/Provider 背压、共享 reviewer 不可用 | 停止新增 author；先清 review、统一 blocker 或背压 |
| `replan` | skeleton-only/unwired fake-only 产物、slice 不推进 production dependency graph、缺 exit criterion、base/调用链已漂移、一次 aggregate rework 后仍有 P0/P1、需要但缺少 ADR | 终止当前 slice；保留证据，重新定义一个完整纵切和 acceptance |
| `intervene` | 越权 lifecycle/publication、secret/path boundary 风险、同 worktree 多写者、非本编排进程被操作、evidence/identity 不一致、Worker 明确 dead/异常但 Lead 未对账 | 立即提醒 Lead 止损；由 Lead 按权威状态审计并决定动作 |

同一 `reasonCode + gate + adapter + spec family` 第二次出现就标记 repeated failure 并 `freeze-fanout`；修复确定性 preflight 或重做 plan 前，不允许第三次原样派发。不能用事件年龄单独判断 dead，也不能因 CPU/内存空闲就跳过 scope、依赖、review 或 Provider admission。

## Reviewer 前的 defect-owner routing

候选进入唯一 reviewer 前，Lead/Supervisor 先依据机器 preflight 与 exact diff 把缺陷归属一次；不得用 reviewer/rework 代替本可在 admission 捕获的结构性问题：

| defect owner | 典型问题 | 固定路由 |
| --- | --- | --- |
| plan/operator | TaskSpec、Policy、acceptance/verifier 不一致或不可执行 | `replan`，修正输入并从当前权威 main 建 successor；不计 Worker rework |
| Adapter/identity | executable、version、protocol、authority mode 或 evidence identity 不匹配 | 修 Adapter/admission 后建 fresh successor；不计 Worker rework |
| baseline/integration | old base、调用链漂移、依赖边尚未满足 | 更新依赖图并从 fresh main 重建 successor；不审陈旧候选，不计 Worker rework |
| architecture/governance | production seam 或架构缺口；需要但缺 ADR | `replan`；触发 trust/persistence/lifecycle/publication 变化时先 ADR，再派 successor |
| candidate-local | 锁定 TaskSpec/Policy/base 正确，缺陷仅存在于当前候选实现 diff | 才允许唯一 reviewer 一次聚合 P0/P1，并消费最多一次 aggregate rework |

同一候选同时命中多类时先关闭上游结构性 owner，禁止把它们包装成 candidate-local finding。Reviewer 若首次发现机器可检的非 candidate-local 问题，仍按上表终止 slice/replan，不开启 Worker rework；复盘时把缺失检查前移到 admission/preflight。

新增 selector/preflight 的候选在派 reviewer 前还必须通过“正向路径先行”：使用当前 production entry、固定 Marshal 二进制和可达合法输入完成一次端到端 `pass`。只验证 helper、测试 seam 或人工 manifest 的单元测试不具备该资格；production selector 不可达时直接 `replan`，不消耗 aggregate rework。

## 纵切和 WIP

- 默认四槽为 `Lead + 最多 2 authors + 1 shared reviewer`。Lead 承担 Supervisor 控制面；有额外只读槽时可以委派观察，但它不能替代 reviewer，也计入真实宿主容量。
- `first-pass` 只统计已经到达 fresh exact-diff reviewer、非 research/canary、且没有外部 Provider/平台 blocker 的 eligible release slice；首轮 reviewer `P0/P1=0` 才算通过。当前 Goal 有 eligible slice 时，按最近最多 10 个计算（不足 10 个取全部）；比率低于 50% 立即收缩为 `Lead + 1 release writer + 1 preflight/ADR lane + 1 shared reviewer`：第二条 lane 只关闭当前 release blocker 的机器 preflight、producer-chain 证据或必需 ADR，不得成为第二个 release writer 或无关 Harness 微修。
- 收缩后只有连续 3 个新的 eligible release slice 都首轮 `P0/P1=0`，才恢复最多 2 个 release writers；任一首轮失败重新从零计数。CPU/内存/Provider 容量始终只是扩容必要条件，不能覆盖 first-pass、依赖、scope 或 review queue 的止损结果；preflight/ADR lane 暂无合格任务时宁可空闲或协助只读证据，不提前恢复第二 writer。
- author 必须有互斥 worktree/scope。至少一个 author 直接推进当前最高优先级 release/product exit criterion；Harness/Skill 修复只有阻断当前主线或同 failure 已重复时插队。
- 默认禁止 skeleton-only commit。完整纵切至少从一个真实 production entry 经过本 slice 改变的实现，到一个可由 acceptance 观察的结果，并说明它关闭哪个 dependency edge。孤立 interface/package、只有 fake test、未接线 helper 或为缺目录单独建空骨架都不算纵切。
- `AGENTS.md`/已接受 ADR 明确要求的 deterministic Fake 前置、ADR、研究或 contract-first 可独立交付，仅当它本身是当前批准的 exit criterion，且写明直接解锁的下一完整纵切；不得把“以后会接线”当完成证据。
- review queue 出现待处理项时优先使用 shared reviewer；不得继续堆作者分支使候选在 review 前陈旧。

## 复盘与 Skill 演进

每次 `replan`/`intervene` 后记录：稳定 failure signature、浪费的 Attempt/rework/等待、根因属于 Core/Adapter/TaskSpec/Skill/外部依赖、已有哪个机器门禁本可提前捕获、下一次最小预防动作。

把效率损失按发生点而不是表面状态记账：

- Worker 启动前被 admission 捕获：`preventedAttempt=1`，不计 retry/rework；
- Worker 完成后才发现 TaskSpec/profile/Adapter/evidence producer 的确定性矛盾：`wastedAttempt=1`，`workerReworkAllowed=false`，停止 reviewer fan-out并先修 preflight；
- Reviewer 首次发现可由 schema、静态分析、selector probe、tool/command/profile 对比或 merge-tree 机械发现的问题：记 `machineEscape=1`，不得靠增加 reviewer 轮次补偿；
- 只有当前 diff 内的行为/设计缺陷才计 `reworkRound`。同一 aggregate rework 后仍有 P0/P1，终止切片并 replan。

Lead 每轮优先消除最高成本的重复 escape，但不得因此把主线改成无限 Harness 清理：只有阻断当前 exit criterion，或同一稳定 signature 已出现第二次，Harness 修复才插队。修复完成后必须用原失败的最小脱敏 fixture 做 forward test，证明它在 Attempt 前以同一稳定 `reasonCode` 短路；没有该证明，不得称为“已吸取教训”。

只把重复出现或能明确预防高成本安全事故的经验提升为 Skill 规则；一次性 provider 细节留在任务证据，避免为每个案例累积全局步骤。优先改确定性 preflight、routing 或默认 WIP，不以新增 reviewer 轮次代替机器检查。

### 规则预算与单一门禁

- 每个 phase 只认一个面向 operator 的机器 verdict。固定 Marshal 内部命令/Core package 拥有它已经实现的 contract、canonical、Candidate 与 observation primitive；Python preflight 可以作为该 phase 的唯一 operator verdict 入口，负责组合这些 primitive、完整 identity/lineage/path 检查、有限 I/O/timeout 和 operator-local claim，但不能另造与 Core 重叠的 Schema 或 digest 语义。
- 新 reason code、manifest 或检查器必须说明替代哪个旧入口；如果只是覆盖一次 incident，保留为 fixture/issue 证据，不进入 Skill。没有删除或收敛目标的“再加一条检查”默认拒绝。
- 现有 authority object、typed failure 和 event reason code 能表达的事实，不新增持久 receipt。只有改变 trust boundary、persistence、lifecycle 或 publication authority 时才新建 ADR；普通内部 verdict 保持非权威、纯函数和可重放。
- reviewer 发现 `machineEscape` 后，按 defect-owner 修复对应 Core/Adapter/preflight test；Skill 最多增加一条 routing 规则。不得把同一字段同时写入 Skill、wrapper、Schema 和 reviewer prompt 四处维护，也不得用新增 reviewer 代替唯一独立 reviewer。

跨 round 复用/重绑 `sourceHead`、ReviewPacket 或 evidence identity 仅登记为 Core 设计候选；Supervisor 不得建议静默复用或降低 freshness/exact identity。改变 trust boundary、persistence、lifecycle 或 publication authority 仍必须先 ADR。

Supervisor 每轮输出应简短且可行动：当前四类建议、命中证据、受阻 exit criterion、review/WIP 状态和 Lead 下一项有限动作。无变化时复用同一 observation digest，不重复制造告警或派新 Worker。
