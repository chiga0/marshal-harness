---
name: marshal
description: 使用 Marshal Harness 编排 Coding Agent、执行证据门禁审查与 Rework/发布流程。用户明确要求“使用 Marshal”，或希望由主 Agent（pi、Codex 等编码 Agent）驱动本地 Coding Agent、验收代码、管理 CI/PR、持续完成开发闭环时使用。
---

# Marshal Harness

通过 `marshal` CLI 操作 Marshal Core。Marshal 是状态与策略的唯一权威；本 Skill 只负责把用户目标转成 CLI 工作流，并基于证据作出 ReviewDecision。

## 强制边界

- 只通过 `marshal` CLI 改变任务生命周期；不要直接改写 `.marshal/` 状态。
- 不要绕过 Core 直接启动 Worker、推送分支、调用托管平台 API 或创建 PR。
- ReviewDecision 必须复制当前 ReviewPacket 的所有身份与摘要字段。
- 无效或陈旧 Decision 不得产生状态副作用。
- `accept` 要求全部 Required Gate 通过且无 Blocking Finding。
- `no_change` 要求 TaskSpec 允许、真实 diff 为空且有已验证的诊断产物。
- Blocking Finding 只能在产生新快照或新验证证据后关闭。

## 工作流

1. 在 Git 仓库执行 `marshal init`；运行状态位于被忽略的 `.marshal/`。
2. 用 `marshal task status --run RUN_ID --json` 读取状态。
3. Run 到达 `REVIEW_PENDING` 后执行：

   ```bash
   marshal task review --run RUN_ID --json
   ```

4. 读取生成的 `review-packet.json`，审查 TaskSpec、完整 patch、VerificationReport、ArtifactManifest、WorkerResult 与历史 Blocking Finding。
5. 生成符合 `ReviewDecision` Schema 的 JSON。允许的 verdict：
   - `accept`：验证门禁全部通过，无阻塞问题。
   - `rework`：有阻塞问题或 Required Gate 失败，且预算尚未耗尽。
   - `reject`：方案不可接受，或 Rework/attempt 预算耗尽。
   - `blocked`：依赖外部输入、权限或能力，并提供 `blockerOwner`。
   - `no_change`：满足无变更守卫。
6. 导入决策：

   ```bash
   marshal task review --run RUN_ID --decision REVIEW_DECISION.json --json
   ```

7. 终态读取 `outcome.json` 与中文 `outcome.md`；非终态继续由 Core 驱动下一轮。

## 审查输出

Decision 必须绑定 `taskId`、`runId`、`reviewRound`、`specDigest`、`reviewPacketDigest`、`verificationDigest`、`artifactManifestDigest` 与 `evidenceDigest`。Blocking Finding 使用稳定 ID，包含严重级别、问题描述和可验证的 required outcome；不要接受 Worker 的自我声明代替独立证据。

## 操作要点

- Worker Adapter 通过 `MARSHAL_OPENCODE_PATH`、`MARSHAL_QWEN_PATH`、`MARSHAL_PI_PATH` 的绝对路径配置；发布需要绝对 `MARSHAL_GH_PATH` 与独立 `MARSHAL_GH_CONFIG_DIR`。
- `task run`、`task verify`、`task publish` 是长耗时命令，使用 `nohup ... > log 2>&1 < /dev/null & disown` 脱离运行；被意外中断后先用 `marshal doctor --run RUN_ID --json` 对账，再幂等重跑同一命令。
- **事件驱动监控**：用 `tail -f .marshal/runs/RUN_ID/events.jsonl`（或 ≤ 2 分钟短周期）监听 `worker.completed` 并立即触发 verify；禁止 8–15 分钟粒度的长 sleep 轮询（实测多 Run 累计空转 30–50 分钟）。
- **心跳 watchdog 与行动队列（后台必备）**：后台 `task run` 必须同时起 `nohup scripts/marshal-watch.sh 600 &`（marshal-harness runbook §11）作轮间兜底；Lead 每轮先运行 `MARSHAL_WATCH_NOTIFY=0 scripts/marshal-watch.sh --once --json` 消费行动队列（items 按 priority 升序，字段 runId/state/priority/action/ageSeconds/processOwnership），再决定本轮动作。`RUNNING` 以**进程存活**判 active/DEAD?（opencode 长 attempt 无事件是正常的，禁止用事件年龄判死）；`processOwnership=not-found`（action=doctor-dead）必须先 `marshal doctor --run RUN_ID --json` 对账，再幂等重跑。
- **每轮有限动作（反空转）**：每个 heartbeat 必须处理行动队列中最高优先级项，并至少完成一个安全有限动作——审查并导入 ReviewDecision、导入 rework、恢复 driver、publish、检查 CI、准备互斥 successor；仅当所有 Run 都有可证明的活进程（`owned-active`）或存在明确外部阻塞时才允许本轮不动作。连续多个 heartbeat 只报告状态而不推进交付属于空转事故。
- **REVIEW_PENDING 时限**：`REVIEW_PENDING` 必须在**一个 heartbeat 内**完成完整审查并导入 ReviewDecision，或产出明确的 blocking finding；不得跨轮停留在待审。Required Gate 失败且 ReviewPacket 可用时，同轮导入 rework Decision 进入 rework。
- **合并边界**：ACCEPTED、Draft PR、CI green 均不等于主干交付。mergePolicy=never 或 Core 无 merge 能力时，必须在**首次发现**即报告为外部/治理阻塞；禁止连续派发依赖该 PR 的 stale-base successor，也不得把此类 PR 计为 milestone 完成。
- **交互权优先**：绝不在工具调用里用 `sleep N && 检查` 等待长任务——Lead 会被该调用阻塞、无法响应用户纠偏（实测 500s 失联）。正确姿势：nohup 后台化驱动脚本后**立即结束本轮**交还交互权；驱动脚本收尾时发系统通知（`osascript -e 'display notification "<run> 到 REVIEW_PENDING"'`）或写完成标记文件；新轮次里用 `task status`/`events.jsonl` 快速推进。
- **并发纪律（capacity-based）**：不设固定并发上限等 magic number，也不假设同机单编排。当前阶段 Marshal main 尚无并发/公平性 Policy 字段、Provider capacity contract、自动 admission queue 与 backpressure 控制器，并发仅由 Lead 以人工 admission 决定，决策输入限于可实际取得者：写域互斥（独立 worktree 且 `scope.allowPaths` 不重叠才可并行）、宿主 CPU/内存/I/O 余量、Provider/API 明示限制与 rate limit、实际观察到的背压（backpressure，如超时率上升、事件延迟）；资源紧张时排队派发或降载，容量恢复后回升；可取得的信号缺失时不得虚构，一律默认减少新派发或排队；用户显式给定的并发策略优先于 Lead 推断。自动 queue/backpressure/升降载与可配置 Policy 字段为 M12 尚未实现的路线项，不得写成当前已可配置/已可自动化的行为；并发决策依据随 fan-out 度量人工记录，保证事后可回溯（见 Runbook §9.7 与 §10.7）。多编排并存必须保留进程所有权隔离——只管理归属本编排的进程，禁止跨编排 blanket kill（误杀表现为多个 Run 同时 `worker.failed`/`context canceled`）。
- **Mac-first 实证与 rework 最小化**：为避免 Qoder/Codex 类适配问题在每个 Run 中重复消耗 token，Lead 在每个 heartbeat 依次执行以下 admission 清单：
  1. 先执行 `MARSHAL_WATCH_NOTIFY=0 scripts/marshal-watch.sh --once --json`，读取 `memoryAvailableBytes`、`pressureFreePercent`、`pressureSource`、`swapUsedBytes`、`activeOwnedWorkers`、`slotsAvailable`、`recommendedMaxWorkers`、`concurrencyAction`；只有 `slotsAvailable > 0`、`pressure=ok`、scope 不重叠且 Provider 无背压时才增加并发。`swapUsedBytes` 只作历史/现状观测，不单独冻结并发；当前压力为 `critical`、`constrained` 或 `unknown` 时 `hold-concurrency`，恢复为 `ok` 后重新开放槽位；排队时不杀其他编排进程腾槽位。
  2. 首次运行或更换版本先固定绝对 executable、held identity/digest、裸 `--version`、真实 `--help` argv、permission mode 与 result transport；fake fixture 只能补充测试，不能替代真实 Mac 证据。
  3. 把 WorkerResult 传输当作协议门禁：验证普通用户模式的最终落盘路径、边界、单次写入和重命名/替换行为。若 Provider 拒绝带 `:` 的 attempt 路径、绝对路径或重复 shell 写入，适配器必须提供受控 colon-free alias 或 provider-native output channel；不得把 `result-missing` 重试当作模型偶发失败。
  4. `result-missing`、protocol/identity、path-boundary、version drift 属于结构性失败：先修 adapter/contract，再以当前 local main 创建 fresh-base successor；只有明确 provider timeout/transient transport 且结构性 preflight 已通过时才重试同一 `taskId`。
  5. 验证分层：先跑受影响包的 `go test`、`vet`、`staticcheck` 与必要的 `-race`，再跑 `make check`/全仓 race；门禁失败必须记录精确 gate、版本、命令摘要与 digest，禁止用“重跑一次”替代根因修复。
  6. 若 ReviewPacket 因旧 artifact manifest、旧 base、缺失证据或 `worktree evidence changed after verification` 无法生成/导入，不伪造 Decision；通过 Core 允许的 intervention/cleanup 路径标记历史阻塞并准备 fresh successor，避免陈旧 Run 长期占据 `REVIEW_PENDING` 队列。
  7. `marshal supervise --once` 不是只读巡检：它可能启动全局 `READY/REWORK_REQUESTED` Run。Heartbeat 只读检查使用 watchdog JSON、`task status`、`doctor` 和事件尾部；只有完成容量、scope、lease admission 后，才允许显式调用会启动 Worker 的 supervise/driver 路径。
  8. **预检证据复用与单次失败裁决**：为减少同一适配器在多个 Run 中重复消耗 token，Lead 应优先查找最近一次与当前 `sourceHead`、平台/架构、held executable digest、裸 `--version`、协议版本、权限模式及 WorkerResult transport **完全匹配**的已验证摘要。全部匹配时只复用该预检结论，运行任务特有的 acceptance/gates；不得把旧摘要带入新 base、换二进制、换协议或换结果路径的 Run。下列任一变化立即使摘要失效并要求一次新的 Mac live preflight：`sourceHead`、可执行文件 inode/digest、OS/架构、adapter 配置、协议/Schema、结果落盘路径或权限模式。
     - `marshal doctor` 的 `configured=false` 是硬 admission 阻断；即使 discovery 列出了候选或 PATH 中存在同名程序，也不得派发。只有注入并再次验证精确绝对路径、held digest、版本和真实 `--help` 后，才可把该 Adapter 纳入 Worker 顺序。
     - Mac 普通用户模式必须显式设置对应的 `MARSHAL_QODER_MODE=ordinary-user` 或 `MARSHAL_CODEX_MODE=ordinary-user`；再次 doctor 应看到 `configured=true`、`compatibility=supported` 和 `authorityMode=ordinary-user`。未设置 mode 时的 `unsupported` 是严格 authority 的 fail-closed 结果，不应通过重试绕过；ordinary-user 证据也不得描述为 hardened authority、APAP 或 sandbox。
     - `task run` 若返回“缺少当前有效的 plan 审批”或等价 planning-admission 错误，视为 Core admission 阻断而非 Worker/Provider 失败；先修复或重新生成当前 plan/approval 并重新核对 digest，未完成前不得重复同一 `task run`，也不得计入 operational retry 预算。
     - 同一结构性失败（`result-missing`、path/protocol/identity/version drift、旧 artifact/base、`worktree evidence changed after verification`）只允许**一次**裁决：记录失败类别和 digest，停止原 Run 的盲目重试，修复 adapter/契约后从当前 local main 创建 fresh-base successor；不得用“再跑一次”清除该 finding。
     - 仅有明确的 provider timeout、DNS/rate-limit 或短暂 transport 背压，且预检摘要仍匹配时，才允许在原 `taskId` 上做有限 operational retry；重试前记录 attempt、预算和 backoff，超过预算转 `blocked/reject`，不再派发同一失败路径。
     - `REVIEW_PENDING` 的 triage 也只做一次：有完整且身份匹配的 packet 就在本 heartbeat 导入 Decision；缺 packet/旧 manifest/旧 base/证据变更则产出 intervention finding 并准备 successor，禁止跨 heartbeat 反复执行同一 `task review`。
     - 派 reviewer 前生成一次 freshness fingerprint，至少绑定 current attempt/sourceHead/reviewRound 以及 packet/spec/verification/artifact/evidence digest；缺失或不一致使用固定 reasonCode 拦截。相同 stale fingerprint 不得重复派 reviewer 或重复导入 Decision。
     - 复用的是证据摘要而不是状态副作用；不得手写 `.marshal`、伪造 digest 或把复用摘要当作本次 Run 的独立 reviewer 结论。
  9. **零 Attempt admission 与待办去重**：`task run` 前必须先完成零 Attempt 预检：`task status` 处于 `READY`、plan approval 与当前 `specDigest/policyDigest/capabilityDigest` 匹配、`doctor` 显示 Adapter 已配置且 supported、Mac 普通用户模式显式声明、scope/worktree 不冲突、容量与 Provider 背压检查通过。任一项失败都不得启动 Worker、不得消耗 Attempt/retry 预算；只记录 admission finding 并修复输入或 plan。
     - 使用 `templates/admission-receipt.json` 生成短寿命、机器可读的 operator-local receipt；它不是 `marshal.dev` contract，不得写入 `.marshal` 或冒充 Core authority。Receipt 必须绑定当前 source/spec/policy/capability/base/state/approval、host OS/arch、Adapter config、精确 executable path/digest/device/inode、permission/result-path identity、worktree/scope，以及带摘要的 doctor/capacity/backpressure 观测；有效期最多 60 秒。执行前先重新采集所有时变证据并逐项比对，再用 `jq -e -f .agents/skills/marshal/references/validate-admission-receipt.jq RECEIPT.json` 验证；任一 tuple、sequence、digest 或时效变化即失效。
     - plan 前先做 acceptance purity lint：拒绝 shell wrapper/重定向、会在 worktree 生成 cache/profile/coverage 的命令，以及未使用 `-B` 的 Python 验收。无法证明纯只读时先在临时副本 dry-run 并比较前后 digest，禁止等到 `verifier-worktree-mutated` 才发现污染。
     - watchdog 的每个行动项提供只读 `dedupeKey`（绑定 runId、state、action/processOwnership、spec/policy/capability/base、attempt/review/rework 轮次、当前 ReviewPacket 与 control records 内容摘要）。仅当 `dedupeKey`、拟执行 action、doctor/admission 证据与外部阻塞事实都未变化时，后续 heartbeat 才只能报告或等待；新 plan/approval、fresh base、新 Attempt、Packet/control 内容、进程归属、Adapter doctor/config 或 Provider 背压任一变化，都必须重新判定。`dedupeKey` 是去重提示而非 Core authority，不能单独阻止合法恢复动作。
     - 缺 plan approval、`configured=false`、缺失/陈旧 ReviewPacket 与结构性失败分别使用不同 finding 类别；不要把 admission finding 伪装成 Worker/provider failure，也不要通过重复 Run 清除它。
     - 并发槽位是容量上限而非派发许可；即使 `concurrencyAction=increase-concurrency`，仍须同时满足零 Attempt 预检、scope 互斥与 Provider 无背压，且同一 dedupeKey 不得 fan-out。
  10. **Adapter 上线阶梯与失败预算**：新 Adapter 或版本升级必须按 `registry/doctor → fake conformance → 真实只读 live probe → 单个低风险写任务 → 常规调度` 逐级晋升；任何一级失败都停在该级修 adapter/契约，不得直接把真实产品任务当探针。每一级只保留一份按 executable digest、平台、authority mode 与协议摘要索引的证据，后续 Run 命中相同身份时复用，避免重复烧 token。
      - 首个真实任务前固定一张 compatibility matrix：真实 argv、退出码语义、权限拒绝语义、输出上限、session/turn budget、WorkerResult transport、路径字符限制、是否允许绝对路径、网络/仓库写权限。未知项先用只读 probe 补齐，不在写任务中猜测。
      - 每个独立失败签名最多消耗一次 Worker Attempt。失败分类只消费 Core 已持久化的 typed failure/Outcome；watchdog 与 doctor 仅提供诊断证据，不是 failure/retry authority。Core 尚无 typed failure 或分类未知时 fail closed，不重试。只有 Core 判定的 transient provider failure 才可按 Policy 在原 taskId 做有限 operational retry；其余类别先修 Core/Adapter/TaskSpec，再由 Core/CLI 建 fresh-base successor。
      - successor 不重置同一路径的人工失败预算：Lead 以 `sourceHead + adapter executable digest + authority mode + protocol/result transport digest + Core failure kind/evidence digest` 形成稳定 failure signature 并跨 Run 去重；签名未变化不得再次派发。该签名只用于 operator admission，不能替代 Core 的 retry/rework 状态与 Policy。
      - 写任务在派发前做 pre-mortem：检查 acceptance 是否能在 Worker 权限内执行、结果路径是否真实可写、任务上下文是否自包含、scope 是否只覆盖必要路径、验证命令是否由独立 verifier 执行。任一项未知就先补 TaskSpec 或只读 probe，避免把 reviewer 的确定性 P1 变成第二轮 Worker rework。
      - WorkerResult transport 的可复用摘要必须额外绑定 staging basename、文件类型、staging/control 不同 inode 证明、held dirfd 的 exact-inode consume/cleanup、唯一允许的写入 primitive 与 argv、权限拒绝提取器版本及 transcript event contract。任一字段变化都使旧摘要失效；fixture 必须只经该 transport 写入，禁止同时从 control path 注入结果造成假阳性。
      - 不要给无法机械限制读取范围的 Provider 写“只读相关小段”这类定性约束。TaskSpec 要么列出精确文件并给出真实工具支持的数字化行数/字节上限，要么明确允许整文件；无法在 Provider 真实工具上执行的约束必须在零 Attempt 预检时拦截。
      - acceptance 的故障注入位置必须位于其声称验证的 effect/cache/persist 边界之后，并断言相同 key、相同 outcome 与 effect exactly-once。若连接在副作用前断开，该用例只能证明普通首次执行，不得关闭幂等或 replay finding。
      - `verifier-worktree-mutated` 即使命令退出码为 0 仍是结构性 Required Gate 失败；先隔离测试 cache/temp 或修正验收，再派 fresh-base successor。普通用户 CapabilitySnapshot、doctor 和报告还必须与真实 env/argv 一致，不得沿用 strict managed-config 文案。
      - 首个真实任务通过后立即派一个独立只读 conformance 任务验证同一 sourceHead；二者证据一致才由 Lead 在后续 admission 中把 Adapter 提升为默认 Worker。发现 identity/version drift 时，Lead 停止该 Adapter 的新派发并回到 live-probe 阶段，不沿用旧证据或继续 fan-out；这不是当前 Core 已实现的自动状态机。

用户明确授权的 Harness 适配修复可在 local main 直接完成，但仍必须保留独立 reviewer、精确验证摘要、`localMergeSha/sourceHead/pendingRemoteSync` 记录；产品 Run 生命周期、Worker 启动与发布权限仍只能由 Marshal Core/CLI 改变。
- TaskSpec 的 `work.context` 必须自包含（Worker 看不到对话历史）；acceptance 命令按任务裁剪；constraints 中固定“若某操作被 permission 拒绝，不得重试该路径，改用允许路径内的等价输入”。
- 新建 TaskSpec 必须先经过 `marshal task scaffold --draft DRAFT.json > TASK.json` 生成并完成 Schema admission，再把 `TASK.json` 交给 `task plan`。通用 scaffold 的兼容默认仍是 `qoder → codex → qwen → pi`；但本 Skill 派发 `templates/research-task.json` 时必须显式传 `--preferred-adapter ID` 且默认不传任何 `--fallback-adapter`，从而覆盖兼容默认并冻结为空数组。只有当前 doctor/admission receipt 逐个证明候选 eligible，且用户明确需要 fallback 时，才按顺序显式加入。scaffold 对 draft 或显式参数中的 OpenCode 硬拒绝；Planning 只消费冻结后的显式顺序，不在运行时插入或重排 Adapter。
- `templates/research-task.json` 是单 Attempt、零 rework、无 fallback 的降耗起点。模板不得硬编码 WorkerResult 路径、shell 写入 primitive 或 Provider 私有 transport；这些只能由当前 Adapter prompt 投影。`references/qoder-1.1.23-event-contract.json` 与 `references/codex-0.145.0-result-contract.json` 只是无自由文本的人工审查基线，不是零 Attempt admission authority；真实协议漂移由 Adapter parser/typed failure 和版本化 contract fail-closed，禁止用手工 shape diff 伪装机械门禁。
- pi 的大任务建议 `maxOutputBytes >= 16000000`（转录本近似二次方增长）；pi 有时会写空 `session.id` 导致 WorkerResult 被拒，调研类任务建议在 TaskSpec 内嵌 WorkerResult 逐字模板；opencode Worker 的一切读写操作必须相对路径（绝对路径即使指向 worktree 内部也会被拒）；派发 fan-out 前先勘查目标仓库已有产出。详见 Runbook §9.5 与 §10.4。
- 更完整的实操经验见 Marshal 仓库 `docs/operator-runbook.md` 第 9 节；遇到问题先采集 `task status --json`、`doctor --run --json` 与 `events.jsonl` 末尾三项只读证据。
- L 级复杂任务或探索型问题可用多 Agent fan-out（调研队/评审团/跨仓库并行/仓库内 scope 互斥拆分）：模式、分级、裁决规则与汇总纪律见 `docs/operator-runbook.md` 第 9、10 节；调研任务优先从本 Skill `templates/research-task.json` 填空生成 TaskSpec；评审 Worker 是材料不是结论，ReviewDecision 责任始终在 Lead。

## 运维 / 发布 / 工程规范（v0.1.0 起）

- **本仓库一切需求/任务必须经 Marshal 完成**：plan→approve→run→verify→review→(publish)，禁止绕过 Core 直接改代码/推送；过程中发现问题自行修复并回写本 Skill。
- **Web 控制台 opt-in**：`marshal web`/`marshal serve` 仅显式启动才运行（不占内存）；只读，控制在 CLI/Skill。多 Workspace 用 `--root <repo>/.marshal` 聚合；DAG 用 React Flow（缩放/平移/minimap/点节点看 attempt）。
- **遗留清理**：`marshal task migrate-outcomes --actor ID` 为遗留终态 Run 补记 Outcome（不覆盖已有），再 `cleanup --apply`/`--export-patch` 清理。
- **发布**：`make check` 全绿后打 SemVer tag + `gh release create`，CHANGELOG 遵循 Keep a Changelog；首个正式版 v0.1.0。
- **工程高标准**：不降要求——新命令必须有测试；`make check`（format/vet/staticcheck/race/build）必须绿；关注覆盖率（lifecycle 76%/cleanup 73%/cli 53%/dashboard 53% 为基线）；负载敏感测试用宽松超时去 flake，不削弱断言。
- **重试同 taskId**（已落地 v0.1.0+）：worktree 按 (taskId,runId) 键控（CreateForRun），同 task 可重试、单写者不变量保持；视图层亦用标题折叠重试。
