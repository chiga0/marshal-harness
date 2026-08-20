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
  --worktree "$TASK_WORKTREE"
```

只有返回 `historyClaimed=true` 且 `action=dispatch-reviewer` 才派 reviewer；只有 `action=generate-review-packet` 才调用一次：

```bash
marshal task review --run RUN_ID --json
```

任何 `action=intervention`、非零退出、重复/并发 claim 都不派发、不生成、不重试同一 fingerprint；`reasonCode` 原样进入行动队列。History claim 是 operator 去重事实，不是 Core lifecycle authority。

Validator 的权威绑定由实现、相邻 Schema 和 Core probes 定义，不在本文复制。Operator 必须确认它复用 Core `internal/canonical`、`internal/contract`、Candidate validate/digest 与真实 worktree observer，重新观察完整 RunState、TaskSpec、Policy/Capability、plan/approval、events 中最新 verification、Packet 全部 `PacketInputs`、patch、所有 persisted WorkerResult、Candidate、当前 snapshot/diff 和原始摘要。WorkerResult 必须与 held `attempts` dirfd 枚举出的 persisted 集合逐项相等，path parent `attemptId`、body `attemptId` 和 current attempt 集合一致；遗漏/额外文件都 fail closed。动作前后所有权威输入保持同 inode/bytes。Packet 缺失时，generation claim 必须绑定 exact HEAD，并复用当前 VerificationReport、ArtifactManifest、observed patch、全部 WorkerResult 与 Candidate 链逐项证明当前 worktree observation 完全一致；Core probe 必须从 `repo.json` 绑定的 repository root 与同一 Git common dir 的 task worktree 推导 `authorityNamespaceId`，并持有 repository root、task worktree、common dir 的逐级目录身份，在 history claim 锁内、临时记录落盘前及原子替换前复核 Git membership 与全部路径 inode，任一替换都不得消费 claim。本地 verifier 生成的 worker/normalizer producer 链必须分别精确为 `worker`/`verifier:format-normalize`，worker Candidate 必须同时绑定 `worker.patch` 原始字节及其唯一 validated verifier artifact，且当前本地 verifier 不产生的 `allocationId`/`generation` 必须缺失。合法 tracked/untracked candidate diff 可以进入生成，缺少 Candidate 的 legacy/no_change 路径仍只允许空 diff。已提交 rework 后若 live packet 仍是前轮，只有它与 `review-packets/packet-NNN.json` 字节相同、被同轮 `decision-NNN.json` 全摘要绑定，并由无 `attemptId` 的权威 `review.rework` event 严格邻接到当前 Attempt 的 `worker.started → worker.completed → verification.completed` business lineage，且 completed snapshot/diff 与当前 report/worktree observation 相等，才视同缺失并生成下一轮 Packet；中间只允许逐字段验证通过的 `reconciliation.snapshot-repaired` audit。任一 archive、Decision、event、Attempt 或 Candidate 部分/伪造/错轮都必须 intervention。Candidate 新字段 all-or-none；部分迁移必须固定 `legacy-candidate-partial-requires-migration`，不能猜 legacy identity。

原子 claim 必须从首次读取起持有 history parent dirfd，使用 `O_EXCL` lock、history raw-digest/inode CAS 和同目录 atomic rename，并拒绝嵌套 parent 替换；这些实现条件缺一就不能把返回值当作一次性 claim。

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
  --run-state RUN_STATE.json
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
- 同一结构性 failure signature 或 freshness fingerprint 只裁决一次；修根因前禁止新 Attempt、原 Run retry 或重复 reviewer。
- Required Gate 已覆盖的行为不得再添加未写入 TaskSpec 的字面或实现细节要求。
- 独立 reviewer 的材料不是 Lead 结论；Lead 只对 fresh、完整、identity-bound 的证据作最终 Decision。
