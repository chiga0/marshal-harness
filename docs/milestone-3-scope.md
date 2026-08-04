# Milestone 3 冻结范围

状态：`FROZEN_FOR_IMPLEMENTATION`
冻结日期：2026-08-04

## 目标

在不启动真实 Worker、不发布远端分支的前提下，打通 `REVIEW_PENDING` 后的 File-based Review Bridge、证据绑定决策、受预算约束的 Rework 与终态 Outcome。

## CLI 契约

```text
marshal task review --run RUN_ID [--json]
marshal task review --run RUN_ID --decision PATH [--json]
```

- 不带 `--decision`：从冻结 TaskSpec、observed.patch、VerificationReport、ArtifactManifest 与已保存 WorkerResult 生成有界、Schema-valid、内容寻址的 `review-packet.json`；不改变生命周期状态。
- 带 `--decision`：先验证 ReviewDecision Schema，再验证 run/task/round/spec/packet/verification/manifest/evidence 全部绑定当前证据；无效或陈旧输入不得追加 Event、不得改写 State。
- `accept`：只允许 Required Gate 全部通过且无 Blocking Finding；需要发布则进入 `PUBLISHING`，否则进入 `ACCEPTED`。
- `rework`：必须有 Blocking Finding 或失败 Required Gate，且预算可用；进入 `REWORK_REQUESTED`。预算耗尽进入 `REJECTED` 并保留原因。
- `reject`、`blocked`、`no_change` 分别进入对应终态；`blocked` 必须有 owner，`no_change` 必须由 TaskSpec 允许、真实 diff 为空并有诊断交付物。
- 每份被接受的 Decision 按 review round 不可变保存，并生成 Event；终态额外生成机器可读 Outcome Bundle 和中文 `outcome.md`。

## Core 交付

- 版本化、确定性、有大小上限的 Worker Prompt Renderer；
- ReviewPacket Builder 与内容摘要；
- ReviewDecision Importer、语义 Guard 与 Stale Evidence Rejection；
- Finding 历史：Blocking Finding 跨 Rework 保留，只有引用新 Evidence 的后续 Decision 才能关闭；
- `maxAttempts`、`maxOperationalRetries`、`maxReworkRounds` 与 No-change Guard；
- Outcome Bundle 与人类可读摘要；
- `.agents/skills/marshal/SKILL.md`、`agents/openai.yaml` 和中文使用说明；Skill 只能调用 Marshal Core，不得直接启动 Worker、改写 State 或发布。

## 测试与退出条件

- Unit：摘要稳定性、包大小上限、Decision 绑定、Finding 历史、预算与 Verdict Guard；
- Integration：文件导出/导入、无效 prose、陈旧 Decision 与 Crash-safe 写入；
- E2E：`REVIEW_PENDING → review packet → rework/accept/reject/blocked/no_change → outcome`；
- Skill：显式请求“使用 Marshal”与隐式 Harness 意图均能形成合法 TaskSpec/ReviewDecision，且不会绕过 Core；使用 `skill-creator` 的 `quick_validate.py` 校验；
- Accept 不能绕过 Required Failed Gate；无效或陈旧 Decision 不改变 State；Finding 只有新证据能关闭；预算耗尽产生正确终态；
- `make ci`、独立只读审计、提交推送与远端 CI 全绿后才进入 Milestone 4。

## 明确不在本阶段

- Qwen/OpenCode/Pi 真实 Worker 进程；
- Git commit、push、PR/MR 或远端 CI；
- 自动 merge；
- Codex 专属 Daemon、MCP 或 Desktop API。Codex CLI 与 Desktop 均通过同一文件契约接入。
