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
- TaskSpec 的 `work.context` 必须自包含（Worker 看不到对话历史）；acceptance 命令按任务裁剪；constraints 中固定“若某操作被 permission 拒绝，不得重试该路径，改用允许路径内的等价输入”。
- pi 的大任务建议 `maxOutputBytes >= 16000000`（转录本近似二次方增长）；pi 有时会写空 `session.id` 导致 WorkerResult 被拒，调研类任务建议在 TaskSpec 内嵌 WorkerResult 逐字模板；opencode Worker 的一切读写操作必须相对路径（绝对路径即使指向 worktree 内部也会被拒）；派发 fan-out 前先勘查目标仓库已有产出。详见 Runbook §9.5 与 §10.4。
- 更完整的实操经验见 Marshal 仓库 `docs/operator-runbook.md` 第 9 节；遇到问题先采集 `task status --json`、`doctor --run --json` 与 `events.jsonl` 末尾三项只读证据。
- L 级复杂任务或探索型问题可用多 Agent fan-out（调研队/评审团/跨仓库并行）：模式、分级、裁决规则与汇总纪律见 `docs/operator-runbook.md` 第 10 节；评审 Worker 是材料不是结论，ReviewDecision 责任始终在 Lead。
