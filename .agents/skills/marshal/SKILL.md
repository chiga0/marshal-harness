---
name: marshal
description: 使用 Marshal Harness 编排 Coding Agent、执行证据门禁审查与 Rework/发布流程。用户明确要求“使用 Marshal”，或希望由主 Agent（pi、Codex 等编码 Agent）驱动本地 Coding Agent、验收代码、管理 CI/PR、持续完成开发闭环时使用。
---

# Marshal Harness

通过 `marshal` CLI 操作 Marshal Core。Core 是生命周期、持久化、failure/retry 与发布状态的唯一权威；本 Skill 只把用户目标投影为 CLI 工作流，并基于独立证据作出 ReviewDecision。

## 强制边界

- 产品 Run 只通过 `marshal` CLI 改变；不要绕过 Core：禁止手写 `.marshal/`、直接启动 Worker、绕过 Core 推送/建 PR/调用托管 API或跨编排终止进程。
- Worker 不能为自己的工作提供权威验证；一个写任务必须绑定锁定基线和独立 worktree，一个 worktree 同时只有一个写入者，Worker 与 Publisher 权限分离。
- ReviewDecision 必须复制当前 fresh ReviewPacket 的 `taskId`、`runId`、`reviewRound`、`specDigest`、`reviewPacketDigest`、`verificationDigest`、`artifactManifestDigest` 与 `evidenceDigest`；无效或陈旧 Decision 不得产生副作用。
- `accept` 只允许全部 Required Gate 通过且无 Blocking Finding；`no_change` 还要求 TaskSpec 允许、真实 diff 为空且诊断产物已独立验证。Blocking Finding 只能由新快照或新验证证据关闭。
- 失败或阻塞必须保存 Outcome，禁止虚假 PR。普通宿主子进程不是恶意代码隔离；普通用户模式不得称为 hardened authority、APAP、sandbox 或恶意代码隔离。
- 只管理本编排拥有的进程；禁止 blanket kill。`marshal supervise --once` 可能启动 Worker，不是只读巡检。
- 信任边界、持久化、生命周期或发布权限变化必须先新增/替代 ADR。Harness 适配修复只有在用户明确授权时才可走维护者本地闭环，仍须唯一独立 reviewer、精确验证摘要和 `sourceHead/localMergeSha/pendingRemoteSync`；产品 Run 仍只走 Core/CLI。

## Reference 读取路由（强制）

完整读完本文件后，执行动作前按触发条件读取所有匹配 reference；多项命中就全部读取，不凭记忆替代。每个 reference 首段定义“何时必须读取”。

| 触发条件 | 必须读取 |
| --- | --- |
| 新建/修改 TaskSpec、plan/approve/run、零 Attempt admission、acceptance/verifier、内容型交付 | [admission-and-acceptance.md](references/admission-and-acceptance.md) |
| `REVIEW_PENDING`、生成/派发 reviewer、ReviewDecision、rework/reject/blocked/no_change、freshness 或 closure | [review-and-rework.md](references/review-and-rework.md) |
| Adapter 注册/升级/选择、doctor、真实 probe/conformance、Qoder/Codex/Qwen/Pi/OpenCode、普通用户权限或 WorkerResult | [adapter-promotion-and-mac.md](references/adapter-promotion-and-mac.md) |
| heartbeat、后台 Run、watchdog、卡住/恢复、容量/背压、fan-out 或进程归属 | [watchdog-and-capacity.md](references/watchdog-and-capacity.md) |
| `PUBLISHING`、`CI_PENDING`、checks、publish/accept/reconcile/merge、PR/CI | [publication-and-reconcile.md](references/publication-and-reconcile.md) |
| 修改 Harness/Skill、工程门禁、清理、Web、版本、release 或 milestone 完成声明 | [engineering-and-release.md](references/engineering-and-release.md) |
| 修改本 Skill 或上述路由/细则 | [skill-rule-migration.md](references/skill-rule-migration.md) 以及受影响的全部 reference |

## 每轮 fast path

1. 读取 `AGENTS.md`、本文件和命中的 reference；确认 local main、`origin/main`、工作树、active Run、进程归属与远端同步事实。
2. 先做只读巡检：

   ```bash
   MARSHAL_WATCH_NOTIFY=0 scripts/marshal-watch.sh --once --json
   marshal task status --run RUN_ID --json
   marshal doctor --run RUN_ID --json
   tail -n 3 .marshal/runs/RUN_ID/events.jsonl
   ```

3. 处理行动队列最高优先级项；每个 heartbeat 至少完成一个安全有限动作。只有所有 Run 都有 `owned-active` 进程或有明确外部 blocker 时才允许仅等待。
4. 所有写操作前机械确认单 worktree 单写入者、scope 互斥、锁定 base、容量和 Provider 背压；长命令后台化，禁止长 `sleep` 阻塞交互。
5. 每次动作后重读 Core 返回的 `targetState` 和 `task status`，按下面状态机继续；绝不从自然语言或预期 verdict 猜状态。

## 核心生命周期状态机

新任务先 `marshal task scaffold --draft DRAFT.json > TASK.json`，完成 Schema/admission，再 `plan → approve → run → verify → review`；publish 仅在 Core 状态要求时发生。

| 当前/返回状态 | 唯一允许的下一步 |
| --- | --- |
| `READY` | 命中 admission 后才 `task run`；缺有效 plan/approval 是 admission 阻断，不重试 Worker |
| `RUNNING` | 只读监控拥有的进程与事件；完成后 `task verify`，dead 先 `doctor` 对账 |
| `VERIFYING` | 等待拥有的 verifier；不得由 Worker 自证 |
| `REVIEW_PENDING` | 同一 heartbeat 生成/验证 fresh packet、派唯一独立 reviewer并导入 Decision，或留下明确 finding |
| `REWORK_REQUESTED` | 只有真实 diff 内可修复缺陷且预算足够才重新 admission 后启动下一 Attempt |
| `PUBLISHING` | 按 approval 执行 `task publish`，然后重读状态 |
| `CI_PENDING` | 观察并持久化冻结 required checks 后才 `task accept --run RUN_ID` |
| `ACCEPTED`/`NO_CHANGE`/`REJECTED`/`BLOCKED` | 读取 Outcome 后停止；不得再 publish 或 `task accept` |

`task accept` 只属于 `CI_PENDING`，不是 `verdict=accept` 的通用后续。CI/发布失败走 publication 分支，不得伪装成 Worker rework。

## 最短常用命令

```bash
marshal init
marshal task status --run RUN_ID --json
marshal doctor --run RUN_ID --json
marshal task review --run RUN_ID --json
marshal task review --run RUN_ID --decision REVIEW_DECISION.json --json
marshal task accept --run RUN_ID
```

`task run`、`task verify`、`task publish` 是长命令，使用 `nohup ... > log 2>&1 < /dev/null & disown`；调用被中断时先 `doctor` 对账，再幂等恢复同一 Core 动作。事件监控用 `tail -f` 或不超过 2 分钟短周期，禁止 8–15 分钟长轮询。

Reviewer freshness 必须通过 `references/validate-review-freshness-preflight.py` 的原子 claim：只有 `historyClaimed=true` 且 `action=dispatch-reviewer` 才派 reviewer；只有 `action=generate-review-packet` 才生成一次 packet；任何 `action=intervention`、非零退出或重复 claim 都不重试同一 fingerprint，原样保留 `reasonCode`。具体命令和证据绑定见 review reference。

## 普通用户模式的 truthful 边界

Mac Qoder/Codex 普通用户路径必须显式设置 `MARSHAL_QODER_MODE=ordinary-user` 或 `MARSHAL_CODEX_MODE=ordinary-user`，并由 doctor 证明 `configured=true`、`compatibility=supported`、`authorityMode=ordinary-user`。这仅证明普通用户能力，不证明 hardened authority、APAP、sandbox、Linux authority 或恶意代码隔离；身份/version/协议/路径任一漂移都回到 live-probe 阶段。

Adapter executable 与 publisher 必须使用绝对路径；发布还需要独立 `MARSHAL_GH_CONFIG_DIR`。当前常用 Adapter 的完整环境变量、晋升证据和失败预算见 adapter reference。

## 审查结论底线

评审完整 TaskSpec、patch、VerificationReport、ArtifactManifest、WorkerResult 与历史 finding；Worker/评审 Worker 的声明只是材料。verdict 只可为 `accept`、`rework`、`reject`、`blocked`、`no_change`，其中 `blocked` 必须给 `blockerOwner`。Finding 使用稳定 ID、严重级别、精确位置、可观察缺陷和可机械验证的封闭 required outcome。导入 Decision 后立即依据 Core `targetState` 分支。
