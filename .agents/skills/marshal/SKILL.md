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
  1. 先执行 `MARSHAL_WATCH_NOTIFY=0 scripts/marshal-watch.sh --once --json`，读取 `memoryAvailableBytes`、`activeOwnedWorkers`、`slotsAvailable`、`recommendedMaxWorkers`、`concurrencyAction`；只有 `slotsAvailable > 0`、scope 不重叠且 Provider 无背压时才增加并发，`hold-concurrency` 时排队，不杀其他编排进程腾槽位。
  2. 首次运行或更换版本先固定绝对 executable、held identity/digest、裸 `--version`、真实 `--help` argv、permission mode 与 result transport；fake fixture 只能补充测试，不能替代真实 Mac 证据。
  3. 把 WorkerResult 传输当作协议门禁：验证普通用户模式的最终落盘路径、边界、单次写入和重命名/替换行为。若 Provider 拒绝带 `:` 的 attempt 路径、绝对路径或重复 shell 写入，适配器必须提供受控 colon-free alias 或 provider-native output channel；不得把 `result-missing` 重试当作模型偶发失败。
  4. `result-missing`、protocol/identity、path-boundary、version drift 属于结构性失败：先修 adapter/contract，再以当前 local main 创建 fresh-base successor；只有明确 provider timeout/transient transport 且结构性 preflight 已通过时才重试同一 `taskId`。
  5. 验证分层：先跑受影响包的 `go test`、`vet`、`staticcheck` 与必要的 `-race`，再跑 `make check`/全仓 race；门禁失败必须记录精确 gate、版本、命令摘要与 digest，禁止用“重跑一次”替代根因修复。
  6. 若 ReviewPacket 因旧 artifact manifest、旧 base 或缺失证据无法生成，不伪造 Decision；通过 Core 允许的 intervention/cleanup 路径标记历史阻塞并准备 fresh successor，避免陈旧 Run 长期占据 `REVIEW_PENDING` 队列。

用户明确授权的 Harness 适配修复可在 local main 直接完成，但仍必须保留独立 reviewer、精确验证摘要、`localMergeSha/sourceHead/pendingRemoteSync` 记录；产品 Run 生命周期、Worker 启动与发布权限仍只能由 Marshal Core/CLI 改变。
- TaskSpec 的 `work.context` 必须自包含（Worker 看不到对话历史）；acceptance 命令按任务裁剪；constraints 中固定“若某操作被 permission 拒绝，不得重试该路径，改用允许路径内的等价输入”。
- 新建 TaskSpec 必须先经过 `marshal task scaffold --draft DRAFT.json > TASK.json` 生成并完成 Schema admission，再把 `TASK.json` 交给 `task plan`。未收到用户的显式 Worker 顺序时，scaffold 把默认顺序冻结为 `preferredAdapter: qoder`、`fallbackAdapters: [codex, qwen, pi]`；显式顺序用 `--preferred-adapter ID --fallback-adapter ID ...` 输入并逐项保持。scaffold 对 draft 或显式参数中的 OpenCode 硬拒绝；Planning admission 也会拒绝任何直接输入且 preferred/fallback 含 OpenCode 的 TaskSpec，即使 Policy/registry 配置允许。Planning 对其他 Adapter 只消费冻结后的显式顺序，不存在隐式默认或运行时重排。
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
