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

7. 导入后立即读取返回的 `targetState` 与当前 `task status`，完全按状态分支：
   - `ACCEPTED`、`NO_CHANGE`、`REJECTED` 或 `BLOCKED`：读取 Outcome 后停止，**不得**再调用 publish 或 `task accept`。
   - `PUBLISHING`：完成 publish approval 与 `task publish` 后重新读取状态；若已进入 `ACCEPTED` 就停止，只有精确为 `CI_PENDING` 才继续 `task accept`。
   - `CI_PENDING`：调用 `marshal task accept --run RUN_ID` 检查冻结 required checks。
   - 其他非终态继续由 Core 驱动下一轮；绝不根据 verdict 或 `publication.required` 猜测下一条命令。

## 审查输出

Decision 必须绑定 `taskId`、`runId`、`reviewRound`、`specDigest`、`reviewPacketDigest`、`verificationDigest`、`artifactManifestDigest` 与 `evidenceDigest`。Blocking Finding 使用稳定 ID，包含严重级别、问题描述和可验证的 required outcome；不要接受 Worker 的自我声明代替独立证据。

## 操作要点

- Worker Adapter 通过 `MARSHAL_OPENCODE_PATH`、`MARSHAL_QWEN_PATH`、`MARSHAL_PI_PATH` 的绝对路径配置；发布需要绝对 `MARSHAL_GH_PATH` 与独立 `MARSHAL_GH_CONFIG_DIR`。
- `task run`、`task verify`、`task publish` 是长耗时命令，使用 `nohup ... > log 2>&1 < /dev/null & disown` 脱离运行；被意外中断后先用 `marshal doctor --run RUN_ID --json` 对账，再幂等重跑同一命令。
- `task accept` 是 `CI_PENDING` 的远端 checks 验收命令，不是 ReviewDecision `verdict=accept` 的通用后续步骤。若 `task review --decision` 已返回 `targetState=ACCEPTED`，再次调用 `task accept` 只会产生确定性的状态错误，必须停止并读取 Outcome。
- **事件驱动监控**：用 `tail -f .marshal/runs/RUN_ID/events.jsonl`（或 ≤ 2 分钟短周期）监听 `worker.completed` 并立即触发 verify；禁止 8–15 分钟粒度的长 sleep 轮询（实测多 Run 累计空转 30–50 分钟）。
- **心跳 watchdog 与行动队列（后台必备）**：后台 `task run` 必须同时起 `nohup scripts/marshal-watch.sh 600 &`（marshal-harness runbook §11）作轮间兜底；Lead 每轮先运行 `MARSHAL_WATCH_NOTIFY=0 scripts/marshal-watch.sh --once --json` 消费行动队列（items 按 priority 升序，字段 runId/state/priority/action/ageSeconds/processOwnership），再决定本轮动作。`RUNNING` 以**进程存活**判 active/DEAD?（opencode 长 attempt 无事件是正常的，禁止用事件年龄判死）；`processOwnership=not-found`（action=doctor-dead）必须先 `marshal doctor --run RUN_ID --json` 对账，再幂等重跑。
- **每轮有限动作（反空转）**：每个 heartbeat 必须处理行动队列中最高优先级项，并至少完成一个安全有限动作——审查并导入 ReviewDecision、导入 rework、恢复 driver、publish、检查 CI、准备互斥 successor；仅当所有 Run 都有可证明的活进程（`owned-active`）或存在明确外部阻塞时才允许本轮不动作。连续多个 heartbeat 只报告状态而不推进交付属于空转事故。
- **REVIEW_PENDING 时限与 rework 归因**：`REVIEW_PENDING` 必须在**一个 heartbeat 内**完成完整审查并导入 ReviewDecision，或产出明确的 blocking finding；不得跨轮停留在待审。Required Gate 失败时只依据 freshness-validated ReviewPacket 所绑定的 candidate/evidence 把 finding 归因到真实 diff：只有 Worker 在已冻结 TaskSpec 内造成的可修复行为缺陷，且剩余 rework 预算足够时，才允许同轮导入 rework Decision。TaskSpec/acceptance/verifier 缺陷、零测试匹配、Adapter protocol/result staging/identity/path 失败以及陈旧或变化后的证据都不是 Worker rework；须保留原 Run 证据，先修对应根因，再由 Core/CLI 从当前权威 main 建 fresh-base successor，禁止用 rework 消耗 Worker token 来修编排错误。Decision 合法进入 `REWORK_REQUESTED` 后，启动下一 Attempt 前再重新执行 Adapter doctor、WorkerResult transport、scope、容量与剩余 Attempt 的零 Attempt admission；CI 与发布故障不适用本条，分别严格按 `CI_PENDING`、`PUBLISHING` 的 retry/accept/reconcile 分支处理。
- **单轮 rework 的 finding 规格**：导入 `verdict=rework` 前先做一次 closure-matrix preflight，把本轮所有 P0/P1（以及能同域顺手关闭的 P2）逐项映射为“精确位置 → 可观察缺陷 → 枚举后的 required outcome → 对应验证/negative fixture”。从 `templates/closure-matrix-preflight.json` 生成 operator-local manifest，然后在仓库根执行 `python3 -B .agents/skills/marshal/references/validate-closure-matrix-preflight.py --root . --run-root .marshal/runs/RUN_ID --source-root WORKTREE --manifest MANIFEST.json --review-packet REVIEW_PACKET.json --review-decision REVIEW_DECISION.json --run-state RUN_STATE.json`；只有输出 `status=pass` 才可执行 `marshal task review --run RUN_ID --decision REVIEW_DECISION.json --json`。该 validator 对全部 `PacketInput` 做 nofollow 路径检查，调用 Core contract/JCS 实现验证并绑定 `TaskSpec`、raw patch、`VerificationReport`、`ArtifactManifest` 与每个 `WorkerResult`，并要求 closure ref 指向带 digest 验证产物的 required/pass gate 或 validated verifier artifact；negative fixture 还必须携带实际执行的 `argv`、输入/输出 digest、exit code 与 reason code 回执。它同时绑定 current attempt、worktree `sourceHead`、review round 及 packet/spec/verification/artifact/evidence digest，并要求 manifest finding 精确投影为现有 `ReviewDecision` 的 `description`/`requiredOutcome`/location 字段；manifest 不是新的 Core 契约或持久化权威。`ReviewDecision` 中 stable finding ID、稳定 `outcomeKey`、显式 `parentFindingId`、枚举后的 `requiredOutcome` 与 fixture refs 是唯一持久权威；Worker prompt 和下一轮 closure check 必须从该记录投影，禁止维护未绑定的人工副本。`closed-previous` 只能在精确 lineage 与 fresh evidence 同时成立时使用；改名不得把同一 outcome 拆成新 finding。`requiredOutcome` 不得只写“完整 identity”“正确绑定”“充分覆盖”“等价处理”等开放式词；identity tuple、digest、config/env key、封闭枚举、状态转换、错误码与攻击/崩溃点必须逐项写全。reviewer 当轮尚不能精确化时不得导入 Decision：须在同 heartbeat 内补证或换独立 reviewer；只有确有外部输入/权限/能力且给出 `blockerOwner` 时才 `blocked`，方案不可接受或预算耗尽才 `reject`，TaskSpec/ADR/scope 根因确实要求新基线时才建 fresh-base successor。下一轮仍须审查 fresh snapshot 与完整 diff；closure matrix 只裁决旧 finding 是否关闭。任何新发现的真实 P0/P1（即使前轮理论上可发现）都必须以 `newly-discovered` 或 `reviewer-omission` finding 阻止 `accept`；同时不得把已经枚举的同一 required outcome 人为拆分成多个新 finding 来制造额外 rework。
- **Negative fixture 解释器绑定**：closure-matrix 模板中的 `/ABSOLUTE/PATH/TO/python3` 是必须替换的占位符；用 `command -v python3` 或 `sys.executable` 取得当前解释器绝对路径并写入 `receipt.argv[0]`，相对 `python3` 必须由 preflight fail closed。
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
     - **Reviewer freshness 原子预检（强制）**：从 `templates/review-freshness-preflight.json` 复制 operator-local manifest，并从 `templates/review-freshness-history.json` 初始化 history（两者都不得写入 `.marshal`）。manifest 只声明期望的 `REVIEW_PENDING` identity 与各权威文件相对路径，不接受调用方提供 digest、fingerprint 或 dedupeKey；执行：

       ```bash
       python3 -I -B .agents/skills/marshal/references/validate-review-freshness-preflight.py \
         --run-root "$REPO/.marshal/runs/$RUN_ID" \
         --operator-root "$OPERATOR_DIR" \
         --manifest "$OPERATOR_DIR/review-freshness-preflight.json" \
         --worktree "$TASK_WORKTREE"
       ```

       Validator 复用 Core `internal/canonical`、`internal/contract`、`domain.Candidate.Validate/Digest` 与真实 worktree observer，逐个 nofollow 读取并重算 `RunState`、TaskSpec、Policy/Capability、control plan/approval records、`events.jsonl` 中最新有效 `verification.completed` 冻结摘要、ReviewPacket **全部** `PacketInputs`、patch、WorkerResult、Candidate 与当前 snapshot/diff；同时绑定完整 state 原始摘要、当前 attempt/review、sourceHead 及上述所有 control/evidence 摘要。WorkerResult 必须与 held `attempts` dirfd 枚举出的 persisted 集合逐项相等，path parent `attemptId`、body `attemptId` 与 current attempt 集合必须一致；遗漏或额外文件都 fail closed。packet 缺失时也必须先证明 worktree 仍为 exact HEAD 且 clean，才可消费 generation claim。所有权威输入在动作前后必须保持相同 inode 与原始字节。candidate 新字段必须 all-or-none；部分迁移固定返回 `legacy-candidate-partial-requires-migration`，禁止猜测 legacy identity。Validator 在返回 action **之前**通过从首次读取起持有的 history parent dirfd，以 O_EXCL lock + history raw-digest/inode CAS + 同目录 atomic rename 写入唯一 claim，并拒绝嵌套 parent 替换；只有返回 `historyClaimed=true` 且 `action=dispatch-reviewer` 才能派 reviewer，只有 `action=generate-review-packet` 才能调用一次 `marshal task review --run RUN_ID --json`。任何 `action=intervention`、非零退出、重复/并发 claim 都不派发、不生成、不重试同一 fingerprint；`reasonCode` 原样进入行动队列。history claim 是 operator 去重事实，不是 Core lifecycle authority，禁止据此直接改 Run 状态。
     - 复用的是证据摘要而不是状态副作用；不得手写 `.marshal`、伪造 digest 或把复用摘要当作本次 Run 的独立 reviewer 结论。
  9. **零 Attempt admission 与待办去重**：`task run` 前必须先完成零 Attempt 预检：`task status` 处于 `READY`、plan approval 与当前 `specDigest/policyDigest/capabilityDigest` 匹配、`doctor` 显示 Adapter 已配置且 supported、Mac 普通用户模式显式声明、scope/worktree 不冲突、容量与 Provider 背压检查通过。任一项失败都不得启动 Worker、不得消耗 Attempt/retry 预算；只记录 admission finding 并修复输入或 plan。
     - 使用 `templates/admission-receipt.json` 生成短寿命、机器可读的 operator-local receipt；它不是 `marshal.dev` contract，不得写入 `.marshal` 或冒充 Core authority。Receipt 必须绑定当前 source/spec/policy/capability/base/state/approval、host OS/arch、Adapter config、精确 executable path/digest/device/inode、permission/result-path identity、worktree/scope，以及带摘要的 doctor/capacity/backpressure 观测；有效期最多 60 秒。执行前先重新采集所有时变证据并逐项比对，再用 `jq -e -f .agents/skills/marshal/references/validate-admission-receipt.jq RECEIPT.json` 验证；任一 tuple、sequence、digest 或时效变化即失效。
     - plan 前先做 acceptance purity lint：operator 以保守 admission policy 拒绝 shell wrapper/重定向、会在 worktree 生成 cache/profile/coverage 的命令，以及未逐字使用 `python3 -I -B -c` 的 Python 内容验收；这不是把 wrapper 本身虚构成 Core 已证明的写入行为。无法静态证明纯只读时，必须用 verifier 的真实 argv/env/cwd 在临时副本 dry-run 并比较前后树摘要，禁止等到 `verifier-worktree-mutated` 才发现污染。
     - 对自然语言/内容型 required command，同轮完成 acceptance semantic preflight：从 verifier 提取 `required_all`、`required_any`、`forbidden`、精确路径、最小数量与大小边界，逐项映射到 `work.context` 的输出要求。代码型 `go test`、`make check` 等不要求这些字段，沿用其自身断言与 fixture。自然语言交付默认使用 `casefold` 后的稳定 token 与显式等价词组校验；只有协议字段、命令、路径等确需逐字匹配时才用单一 literal，并在 Worker prompt 中原样引用。已知错误术语、过度声明和自相矛盾句必须进入 `forbidden` 并在 prompt 中解释正确替代语义。内容型 verifier 无法建立一一映射就不得 `task plan`。
     - 抽取结果必须写入由 `templates/acceptance-semantic-manifest.json` 生成的 operator-local manifest；它不是 `marshal.dev` contract，不得写入 `.marshal` 或冒充 Core authority。先用 `./bin/marshal contract validate --schema task-spec TASK.json` 验证完整 TaskSpec。把 manifest、TaskSpec 与 fixtures 放入紧凑的 operator-local 临时目录 `FIXTURE_ROOT`，另从锁定 `SOURCE_HEAD` 创建无 `.marshal`、无未提交文件的 detached/linked clean worktree `CLEAN_WORKTREE`；再执行 `python3 -B .agents/skills/marshal/references/validate-acceptance-semantic-preflight.py --root FIXTURE_ROOT --manifest MANIFEST.json --task-spec TASK.json --protected-root CLEAN_WORKTREE --source-head SOURCE_HEAD`。禁止把 live repo root、`.marshal` 或 primary `.git` 目录作为 root/protected-root；每个显式 `--protected-root` 必须自身在顶层持有 regular nofollow `.git` linked-worktree marker，并独立满足精确 HEAD 与 clean 状态，子目录或非 Git 树不能借用其它 root 的绑定。Validator 会拒绝任意层级的 `.marshal`，对受保护树设置条目/字节硬上限，并在实际哈希时以逐级 nofollow dirfd、`fstat` 身份/大小复核和有界分块读取拒绝枚举后的替换或增长；误传大树固定 fail closed，不再重复扫描运行态。Validator 必须加载相邻 Draft 2020-12 manifest schema，并绑定 TaskSpec 原始字节摘要、required command 的完整 canonical tuple（`id/argv/cwd/timeoutSeconds/required=true/baselinePolicy/maxLogBytes`）及其摘要、命令相对路径与精确 deliverable 路径、最小交付数/行数/最大 UTF-8 字节数、封闭 normalizer、`required_all`/每组 `required_any`/`forbidden`、覆盖上述每一项的逐字 `work.context` 映射，以及每个 positive/negative fixture 的原始字节摘要。内容 gate 只接受 validator 能用 AST 完整抽取且逐字以 `python3 -I -B -c` 启动的严格只读 Python grammar；shell wrapper、optional command、未知/额外语句、绝对路径、`..`、Python 启动/import 保留名路径、symlink fixture、受保护根引用、规则遗漏/额外项、normalizer drift 或任一映射不等价均 fail closed。Fixture 必须用绑定的真实 `cwd`/timeout 在临时副本运行，并放置无外部副作用的本地模块遮蔽 canary，证明 isolated mode 未加载 canary；同时比较临时 fixture root、绑定 `SOURCE_HEAD` 的 clean worktree 与执行临时树前后摘要。这只是普通宿主进程的副作用探测，不是恶意代码 sandbox。任一失败必须使用固定 `reasonCode` 阻断 `task plan`，先修 TaskSpec/acceptance，不得启动 Worker 或归因成 rework。
     - 代码型 acceptance 必须优先断言可观察行为、错误分类、状态转换或协议输出；测试函数名、未导出 helper、局部变量、注释措辞与其他实现细节默认都不是契约，禁止仅因它们未逐字出现就让 Required Gate 失败。若确有外部工具依赖的稳定符号名，必须在 `work.objective/context` 中写明“逐字使用 `<symbol>`”并由 preflight 做一一映射；否则优先运行包级行为测试，或让静态 gate 接受语义等价的符号集合。若必须使用 `go test -run`，selector 只能覆盖派发前已存在且已枚举的测试集合，并须先用 `go test -list` 机械证明同一 selector 至少匹配一个测试，再断言实际执行数量非零；禁止把零匹配的退出码 0 当作通过。派发前须扫描 Python/grep 类代码 gate 中的源码 token：任何未映射的非契约 token 都是 TaskSpec defect，必须先修 acceptance，不得把它转化为 Worker rework。
     - 内容型 verifier 在不写入目标 worktree 的临时样本上各跑一次 positive/negative acceptance：代表性正确输出必须通过；分别缺失一个 `required_all`、缺失一个 `required_any` 组和命中一个 `forbidden` 的样本必须失败；再比较样本目录前后摘要确认 verifier 纯只读。若最近一次同类报告已证明语义等价，可复制到临时目录作为 positive fixture，禁止为验证 acceptance 再启动 Worker。代码型 verifier 使用任务自身已有的正反测试，不强制套用报告 fixture。
     - watchdog 的每个行动项提供只读 `dedupeKey`（绑定 runId、state、action/processOwnership、spec/policy/capability/base、attempt/review/rework 轮次、当前 ReviewPacket 与 control records 内容摘要）。仅当 `dedupeKey`、拟执行 action、doctor/admission 证据与外部阻塞事实都未变化时，后续 heartbeat 才只能报告或等待；新 plan/approval、fresh base、新 Attempt、Packet/control 内容、进程归属、Adapter doctor/config 或 Provider 背压任一变化，都必须重新判定。`dedupeKey` 是去重提示而非 Core authority，不能单独阻止合法恢复动作。
     - 缺 plan approval、`configured=false`、缺失/陈旧 ReviewPacket 与结构性失败分别使用不同 finding 类别；不要把 admission finding 伪装成 Worker/provider failure，也不要通过重复 Run 清除它。
     - 并发槽位是容量上限而非派发许可；即使 `concurrencyAction=increase-concurrency`，仍须同时满足零 Attempt 预检、scope 互斥与 Provider 无背压，且同一 dedupeKey 不得 fan-out。
  10. **Adapter 上线阶梯与失败预算**：新 Adapter 或版本升级必须按 `registry/doctor → fake conformance → 真实只读 live probe → 单个低风险写任务 → 常规调度` 逐级晋升；任何一级失败都停在该级修 adapter/契约，不得直接把真实产品任务当探针。每一级只保留一份按 executable digest、平台、authority mode 与协议摘要索引的证据，后续 Run 命中相同身份时复用，避免重复烧 token。
      - 首个真实任务前固定一张 compatibility matrix：真实 argv、退出码语义、权限拒绝语义、输出上限、session/turn budget、WorkerResult transport、路径字符限制、是否允许绝对路径、网络/仓库写权限。未知项先用只读 probe 补齐，不在写任务中猜测。
      - 每个独立失败签名最多消耗一次 Worker Attempt。失败分类只消费 Core 已持久化的 typed failure/Outcome；watchdog 与 doctor 仅提供诊断证据，不是 failure/retry authority。Core 尚无 typed failure 或分类未知时 fail closed，不重试。只有 Core 判定的 transient provider failure 才可按 Policy 在原 taskId 做有限 operational retry；其余类别先修 Core/Adapter/TaskSpec，再由 Core/CLI 建 fresh-base successor。
      - successor 不重置同一路径的人工失败预算：Lead 以 `sourceHead + adapter executable digest + authority mode + protocol/result transport digest + Core failure kind/evidence digest` 形成稳定 failure signature 并跨 Run 去重；签名未变化不得再次派发。该签名只用于 operator admission，不能替代 Core 的 retry/rework 状态与 Policy。
      - 写任务在派发前做 pre-mortem：检查 acceptance 是否能在 Worker 权限内执行、结果路径是否真实可写、任务上下文是否自包含、scope 是否只覆盖必要路径、验证命令是否由独立 verifier 执行。任一项未知就先补 TaskSpec 或只读 probe，避免把 reviewer 的确定性 P1 变成第二轮 Worker rework。
      - 对自然语言报告，禁止把“英文固定短语”当作唯一成功条件却只向 Worker 描述中文语义。若多个表述均可接受，用 `required_any` 显式列出等价组；若 literal 真正是契约，prompt 必须写“逐字包含 `<literal>`”，并在派发前用同一清单核对 TaskSpec。reviewer 不得在 Required Gate 已通过后另加未写入 TaskSpec 的字面要求。
      - WorkerResult transport 的可复用摘要必须额外绑定 staging basename、文件类型、staging/control 不同 inode 证明、held dirfd 的 exact-inode consume/cleanup、唯一允许的写入 primitive 与 argv、权限拒绝提取器版本及 transcript event contract。任一字段变化都使旧摘要失效；fixture 必须只经该 transport 写入，禁止同时从 control path 注入结果造成假阳性。
      - Qoder 的受控 WorkerResult `tee` 必须作为 **Worker-side final-declaration 纪律**：Worker 先在内存中完成整个 payload，使用最短且不依赖实现符号拼写的 summary，并把唯一一次成功 `tee` 作为最后一个 tool call；成功后立即 `end_turn`，禁止再用 Read/Edit/Write/Bash、第二次 `tee` 或任何工具检查、纠错或替换 staging，即使随后发现自由文本 typo 也保留原声明。这不是当前 Marshal authority commit：真正的线性化点仍是进程终态后 Adapter 完成 transcript、held dirfd/exact-inode、Schema 与 identity 全部验证，并把接纳结果写入 held control leaf。该规则只能由 Adapter prompt 投影，TaskSpec 不得重复或改写；在 prompt 的 tee-last 投影、transcript“恰好一次成功 tee 且其后无 tool_use”机械校验、回归 fixture 与真实 Mac conformance 全部落地前，本项是 Adapter promotion blocker，不得声称 post-tee access 已被机械拒绝或复用该 transport 摘要。目标实现必须把 post-tee access 归为结构性 `protocol-invalid/do-not-retry`，保持 held dirfd/exact-inode fail-closed；不得因 exit 0、git diff 或自然语言 final 自动生成语义 WorkerResult，也不得为兼容 replacement 放宽 identity。
      - 首次真实 live probe 若“交付文件已生成且 Provider exit 0，但 WorkerResult transport 被 protocol-invalid 拒绝”，不得复制同一 TaskSpec 再跑。先从 Adapter 保存的 transcript/meta 中比较真实 `tool_use.input` 与冻结 envelope fixture，区分 shell `command` 语义和 Provider 自动附加的非执行元数据；只把真实观察到、类型封闭、canonical 编码且不影响执行语义的字段加入版本化 envelope，未知字段继续拒绝。任何 envelope 变化必须同步 bump Adapter/event contract、transport digest 和 fresh live evidence；这一诊断完成前 failure signature 保持去重，避免用 Worker token 探索 parser。
      - 若 TaskSpec 限制 Qoder“除 final tee 外不得运行 Bash”，所有 deliverable 的父目录必须已存在于锁定 base，并在零 Attempt preflight 以 Git tree 机械确认；否则改用已有父目录或先修 TaskSpec/Core 准备逻辑，不得让 Worker 用 `ls`/`mkdir` 探索目录。相同规则适用于其它被禁止的准备动作：派发前已能确定的目录、输入与权限必须由 admission 关闭，不能把确定性环境缺口留给 Worker 猜测。
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
