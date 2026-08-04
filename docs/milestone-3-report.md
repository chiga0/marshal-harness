# Milestone 3 验收报告

- 验收日期：2026-08-04
- 状态：**`READY_FOR_REMOTE_CI`**
- 范围：ReviewPacket、ReviewDecision、Rework Guard、Outcome、Codex Skill
- 真实 Worker/Publication Side Effect：未启用

## 交付结果

- 新增确定性、版本化、128 KiB 上限的 Worker/Rework Prompt，包含冻结身份、范围、预算与历史 Blocking Finding；
- 从冻结 TaskSpec、当前完整 Patch、VerificationReport、ArtifactManifest 与 WorkerResult 生成 Schema-valid ReviewPacket；
- VerificationReport 与 ArtifactManifest 绑定 `verification.completed` 摘要，Review 前重新观察 worktree，Patch 绑定 verifier Artifact；
- ReviewDecision 逐字段绑定 run/task/round/spec/packet/verification/manifest/evidence，陈旧或无效输入不产生状态副作用；
- 实现 accept、rework、reject、blocked 与 no_change Guard，发布/merge 建议不能突破冻结策略；
- Rework/Attempt 预算耗尽进入 `REJECTED`，终态原因写入 RunState；
- Blocking Finding 跨轮保存完整内容，没有新 snapshot 或 verification evidence 时不能关闭；
- 每轮 Decision 与 Packet 不可变归档；终态生成 Schema-valid `outcome.json` 与中文 `outcome.md`；
- `.pending` 孤儿文件在持有 Run Lease 的重试中安全替换，Journal 写入后先提交审查记录再更新可重建 Snapshot；
- 新增 `.agents/skills/marshal/` 与中文使用说明，Codex CLI/Desktop 均通过同一 Core 契约接入。

## 自动验收

- `make check`：Format、Vet、Staticcheck、Race Test 与 Build 全部通过；
- `make ci`：额外执行 `govulncheck`，无已知漏洞；
- Contract：12 份 Draft 2020-12 Schema 的正反 Fixture 全部通过；
- CLI E2E：真实 verify/review 链路覆盖验证后 worktree 篡改、无效 prose 零副作用、终态记录；
- Verdict E2E：accept、rework、reject、blocked、no_change 全部经过 CLI 导出/导入、事件、状态与 Outcome 断言；
- Review Unit/Integration：摘要确定性、Patch 篡改/超限、WorkerResult、发布策略、预算、No-change、Finding、新旧证据与孤儿 pending 恢复均通过；
- Skill：官方 `skill-creator` 生成 `agents/openai.yaml`，`quick_validate.py` 返回 `Skill is valid!`。

独立 OpenCode 首轮审计结论为 `BLOCKED`，指出 Verdict E2E 覆盖与崩溃残留恢复缺口；整改后代码复审结论为 `APPROVE`，P0/P1/P2 代码问题全部关闭。待提交推送且远端 CI 绿色后，本报告才能更新为 `PASSED`。详见 [Milestone 3 OpenCode 独立审查](reviews/milestone-3-opencode-review.md)。
