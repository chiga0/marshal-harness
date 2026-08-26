# Review、Freshness 与 Rework

> **何时必须读取：** Run 进入 `REVIEW_PENDING`，需要生成 ReviewPacket、派 reviewer、形成或导入 ReviewDecision、处理 Required Gate/finding、决定 rework/reject/blocked/no_change，或验证前轮 finding closure 时，必须完整读取。

## 一 heartbeat 完成 triage

`REVIEW_PENDING` 必须在一个 heartbeat 内完成完整审查并导入 Decision，或留下明确 blocking finding。只基于 freshness-validated ReviewPacket 绑定的 candidate/evidence 审查 TaskSpec、完整 patch、VerificationReport、ArtifactManifest、所有 WorkerResult 和历史 finding。

- Worker 在冻结 TaskSpec 内造成、当前 diff 可修、预算足够的行为缺陷，才允许 `rework`。
- TaskSpec/acceptance/verifier 缺陷、零测试匹配、Adapter protocol/result staging/identity/path failure、旧 artifact/base、陈旧或变化证据都不是 Worker rework。保留 Run 证据，修根因，再由 Core/CLI 从当前权威 main 建 successor。
- CI 与 publication failure 分别走 `CI_PENDING`/`PUBLISHING` 流程，不适用 Worker rework。
- 方案不可接受或预算耗尽才 `reject`；只有真实外部输入/权限/能力且有 `blockerOwner` 才 `blocked`；TaskSpec 允许、diff 真空且诊断产物已验证才 `no_change`。
- Decision 进入 `REWORK_REQUESTED` 后，启动下一 Attempt 前重新执行 Adapter doctor、WorkerResult transport、scope、capacity 和剩余预算 admission。

## ReviewDecision identity

Decision 必须原样复制当前 Packet 的：

`taskId`、`runId`、`reviewRound`、`specDigest`、`reviewPacketDigest`、`verificationDigest`、`artifactManifestDigest`、`evidenceDigest`。

Finding 使用稳定 ID、稳定 `outcomeKey`、显式 `parentFindingId`、severity、精确 location、可观察 defect、封闭枚举的 `requiredOutcome` 和 fixture refs。不得只写“完整 identity”“正确绑定”“充分覆盖”“等价处理”；必须逐项列出 identity tuple、digest、config/env key、状态转换、错误码和攻击/崩溃点。

Worker prompt 与下一轮 closure 必须从持久 ReviewDecision 投影，禁止维护未绑定人工副本。`closed-previous` 只在 exact lineage + fresh evidence 同时成立时使用；改名不得拆出“新”finding。新发现的真实 P0/P1 必须用 `newly-discovered` 或 `reviewer-omission` 阻止 accept，但不得把已经枚举的同一 outcome 拆分来制造额外 rework。

## Reviewer freshness 原子预检

从 `templates/review-freshness-preflight.json` 复制 operator-local manifest，并从 `templates/review-freshness-history.json` 初始化 history；二者不得写入 `.marshal`。Manifest 只声明预期 `REVIEW_PENDING` identity 和权威文件相对路径，不接受调用方提供 digest/fingerprint/dedupeKey。

```bash
python3 -I -B .agents/skills/marshal/references/validate-review-freshness-preflight.py \
  --run-root "$REPO/.marshal/runs/$RUN_ID" \
  --operator-root "$OPERATOR_DIR" \
  --manifest "$OPERATOR_DIR/review-freshness-preflight.json" \
  --worktree "$TASK_WORKTREE" \
  --marshal "$REPOSITORY_ROOT/bin/marshal"
```

只有返回 `historyClaimed=true` 且 `action=dispatch-reviewer` 才派 reviewer；只有 `action=generate-review-packet` 才调用一次：

```bash
marshal task review --run RUN_ID --json
```

任何 `action=intervention`、非零退出、重复/并发 claim 都不派发、不生成、不重试同一 fingerprint；`reasonCode` 原样进入行动队列。History claim 是 operator 去重事实，不是 Core lifecycle authority。

`validate-review-freshness-preflight.py` 是当前 review freshness phase 的唯一 operator verdict 入口：它复用固定 `bin/marshal internal review-freshness-check` 的 contract、canonical、Candidate 与 worktree observation primitive，并在 wrapper 内组合完整 identity/lineage/path 稳定性和原子 claim，最终只返回 closed `action/reasonCode/historyClaimed`。固定 internal command 当前只返回 `{ok,digest}` primitive，不得被描述为完整 action verdict；字段细节由相邻 Schema、Core/validator 实现及其正负测试拥有，Skill 只保留下列不变量族和路由，不人工重算 digest 或解释自由文本。

实现必须按一个原子 verdict 覆盖下列不变量族：完整 Run/Task/Policy/Capability/plan/approval/event/verification/Packet 输入；全部 persisted WorkerResult/Candidate 与 current Attempt 的 `attemptId`；repository/worktree/common-dir membership 和 nofollow inode stability；exact HEAD、raw patch、snapshot/diff；worker→normalizer producer lineage；跨 round archive/Decision/event 邻接；归一化前后路径集合与 tracked/untracked 身份不变；history parent dirfd、`O_EXCL` lock、raw digest/inode CAS 与同目录 atomic rename。具体字段和 legacy migration reason code 以实现与测试为准；缺失、额外、部分迁移、错轮、替换或漂移一律返回 `intervention`，且不得消费 claim。

相同 stale fingerprint 不重复派 reviewer或导入 Decision。若旧 manifest/base/证据变化使 packet 无法生成或导入，不伪造 Decision；通过 Core 允许的 intervention/cleanup 保存历史 finding 并准备 successor。

## Closure matrix preflight

导入 `verdict=rework` 前，把本轮所有 P0/P1 及能同域关闭的 P2 一次性映射为：

`精确位置 → 可观察缺陷 → 枚举 required outcome → 对应 verification/negative fixture`。

从 `templates/closure-matrix-preflight.json` 生成 operator-local manifest，然后在仓库根运行：

```bash
python3 -B .agents/skills/marshal/references/validate-closure-matrix-preflight.py \
  --root . \
  --run-root .marshal/runs/RUN_ID \
  --source-root WORKTREE \
  --manifest MANIFEST.json \
  --review-packet REVIEW_PACKET.json \
  --review-decision REVIEW_DECISION.json \
  --run-state RUN_STATE.json \
  --marshal "$REPOSITORY_ROOT/bin/marshal"
```

只有 `status=pass` 才导入：

```bash
marshal task review --run RUN_ID --decision REVIEW_DECISION.json --json
```

Closure manifest 不是新 Core contract；ReviewDecision 才是持久 authority。Validator 必须对所有 PacketInput 做 nofollow path 验证，调用 Core contract/JCS 验证，绑定 current attempt、sourceHead、review round、packet/spec/verification/artifact/evidence digest、raw patch、VerificationReport、ArtifactManifest、所有 WorkerResult 与 Decision finding 的 description/requiredOutcome/location 投影；closure ref 只能指向有 digest 的 required/pass gate 或 validated verifier artifact。Negative fixture receipt 必须包含实际 `argv`、input/output digest、exit code 和 `reasonCode`。

模板的 `/ABSOLUTE/PATH/TO/python3` 是必须替换的占位符。用 `command -v python3` 或 `sys.executable` 写入 `receipt.argv[0]`；相对 `python3` 必须 fail closed。

若 reviewer 当轮无法精确枚举 required outcome，同一 heartbeat 内补证或换独立 reviewer，不得先导入模糊 Decision。下一轮仍审 fresh snapshot 和完整 diff；closure matrix 只决定旧 finding 是否关闭。

## Verdict 导入后的状态分支

```bash
marshal task review --run RUN_ID --decision REVIEW_DECISION.json --json
marshal task status --run RUN_ID --json
```

- `ACCEPTED`、`NO_CHANGE`、`REJECTED`、`BLOCKED`：读取 Outcome，停止；不得 publish 或 `task accept`。
- `PUBLISHING`：读取 publication reference，完成 approval/publish 后重读状态；已 `ACCEPTED` 就停止，只有精确 `CI_PENDING` 才继续 checks。
- `CI_PENDING`：读取 publication reference；`task accept` 只验收冻结 remote checks。
- 其它非终态：仅按 Core 状态继续，不根据 verdict 或 `publication.required` 猜命令。

## 减少 rework

- reviewer 派发前一次性完成 pre-mortem：TaskSpec、scope、acceptance、permissions、result transport、negative matrix 和 verification 能否实际执行。
- 代码/内容 TaskSpec/Policy 默认 `maxReworkRounds=1`，research/canary 为 0。唯一 reviewer 首轮必须一次聚合所有 P0/P1 和可同域关闭的 P2；作者只做一次 aggregate rework，同一 reviewer 只复审一次。复审仍有 P0/P1 时终止本 slice、归档根因并回到 plan，不逐项滚动 rework。
- 同一结构性 failure signature 或 freshness fingerprint 只裁决一次；修根因前禁止新 Attempt、原 Run retry 或重复 reviewer。
- Required Gate 已覆盖的行为不得再添加未写入 TaskSpec 的字面或实现细节要求。
- 独立 reviewer 的材料不是 Lead 结论；Lead 只对 fresh、完整、identity-bound 的证据作最终 Decision。
- 为减少 packet 生成成本而跨 round 复用或重绑 `sourceHead`/evidence identity 属于 Core/Schema/validator 设计候选；现行流程不得实现静默复用，也不得放宽 freshness 或 exact identity 门禁。
