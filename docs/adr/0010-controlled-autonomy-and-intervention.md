# ADR 0010：受控自治、审批 Gate 与人工介入

- 状态：已接受（Accepted）
- 日期：2026-08-04
- 决策来源：用户确认采用“受控自治”，避免完全无人介入与无记录直接接管两个极端

## 背景

完全无人介入的 Coding Agent 可能长时间沿错误方向执行；允许操作者随时直接输入 Worker PTY，又会破坏任务冻结、执行归因与自动证据。两者并非只能二选一。Marshal需要把“观察”“通过 Lead Steering”“改变冻结任务”和“直接接管”建模成不同控制动作，并让每个动作具有明确后果。

结果可控主要来自冻结需求、独立 Verification 与 Review，不来自逐行遥控 Worker。人工控制应作用于目标、范围、关键授权和异常决策；Worker 在已批准边界内保留实现自治。

## 决策

Marshal采用三层职责：

1. 用户决定目标、范围、验收标准、自治级别和发布授权；
2. Lead Agent 负责计划、派发、结构化 Steering、Review、返工与最终 Harness 决策；
3. Worker 只在冻结 TaskSpec/Policy/Capability 与当前 Attempt 内实现。

所有任务必须经过独立 Git Snapshot、Verification 和 Review。无论代码由 Worker、Lead 或人类修改，终端文本和“看起来完成”都不能替代这些门禁。

## Autonomy Profile

冻结 PolicySnapshot 增加 `control`：

```json
{
  "autonomyProfile": "balanced",
  "requiredApprovals": ["plan", "publish"],
  "allowMediatedSteering": true,
  "directPtyPolicy": "record-and-reverify",
  "maxSteeringRounds": 4
}
```

支持三个 Profile：

| Profile | 行为 |
| --- | --- |
| `supervised` | Plan、Publish 和 Policy 指定的额外 Gate 均等待用户；异常立即暂停 |
| `balanced` | 默认推荐；Plan 和 Publish 需要用户批准，冻结范围内执行、Codex Review 与预算内返工自治，异常通知用户 |
| `autonomous` | 不增加人类 Gate，但仍受 Scope、Budget、Verification、Review、Draft-only、Never-merge 等安全边界约束 |

缺失或未知 Profile 不得静默获得更高自治。新建 Policy 默认 `balanced`；兼容旧 Snapshot 时必须显式迁移或标记 Legacy，不能根据运行环境猜测。

## ApprovalRecord

审批不是一个布尔值，而是绑定精确证据的不可变记录：

- `plan` Approval 绑定 Run ID、Spec/Policy/Capability Digest、Base SHA 和当时 State Sequence；
- `publish` Approval 绑定 Run ID、ReviewDecision Digest、Evidence Digest、Review Round 和当时 State Sequence；
- 输入发生变化或 Sequence 已越过适用边界时，旧 Approval 自动失效；
- Approval 以独立文件追加保存，不覆盖旧记录；
- Worker 无权创建 ApprovalRecord。

## InterventionRecord

介入分为以下类别：

| 类别 | 正常入口 | 后果 |
| --- | --- | --- |
| `observe` | 只查看 TUI/日志 | 不改变 Attempt |
| `clarification` | 用户 → Marshal → Worker | 在冻结范围内继续；记录指令与内容摘要 |
| `implementation-correction` | 用户/Lead → Marshal → Worker | 在冻结范围内继续；计入 Steering Round |
| `scope-change` | 用户 → Marshal | 当前 Attempt 停止；返回 `new-run-required`，冻结新 TaskSpec/Policy |
| `manual-pty` | 用户直接输入 Worker PTY | 标记 Attempt 为 mixed provenance/tainted；必须重新 Snapshot、Verification、Review |
| `pause` / `resume` | Marshal Control Plane | 暂停/恢复精确 Session，不等同于单次 Ctrl-C |
| `abort` | Marshal Control Plane | 走既有有证据的 Abort 流程 |

普通工作流不鼓励直接向 Worker PTY 输入。用户通过 Codex Desktop、CLI、手机端或未来 Control UI 向 Marshal发出 Steering；Marshal先分类，再决定发送、暂停或要求新 Run。

## 冻结边界

以下变化不能作为同 Run Steering：

- Allow/Deny Path、Deliverable 或 Acceptance Command；
- 目标、Non-goal、Publication Policy；
- Worker/Adapter、Execution Profile、Budget；
- Policy、Capability 或 Base SHA。

发生这些变化时，Marshal保存 InterventionRecord，停止当前 Attempt，并返回 `new-run-required`。不得修改原冻结文件后继续。

`clarification` 与 `implementation-correction` 只能解释如何满足现有 TaskSpec，不能扩大修改范围、绕过 Gate、增加凭据或改变交付物。

## PTY 与控制语义

TerminalSession 必须区分：

- `interrupt-step`：中断当前 Tool/Model Step；
- `pause-turn`：停止本轮继续派生工具调用，但保留 Session；
- `terminate-session`：有界终止整个 PTY Process Group；
- `resume-session`：从已记录 Session ID 恢复。

单次 `Ctrl-C` 不能被当作可靠 Pause。本项目的真实 OpenCode PTY 原型已经证明：`Ctrl-C` 可能只打断当前步骤，Agent随后继续并写文件。因此 Backend 必须通过能力探测实现精确语义；无法证明 `pause-turn` 时，使用进程组 `SIGSTOP/SIGCONT`（受支持平台）或安全终止 Session，不伪报已暂停。

## 持久化

新增两个 durable contract：

- `ApprovalRecord`：精确 Gate 授权；
- `InterventionRecord`：介入来源、分类、Attempt、内容摘要、影响和后续要求。

记录写入当前 Run 的 `control/` 目录，受 Run Lease、32 MiB 上限、Schema、Identity、Sequence 与 owner-only 权限保护。记录 append-only；Doctor 检查缺口、重复 Sequence、Digest 和 Run/Attempt Identity。

Worker 运行环境不获得写入 Approval/Intervention 存储的能力。TerminalSession hooks 只向 Marshal报告事件，由 Marshal Control Plane 持久化。

## Gate

- `task run` 在 `balanced/supervised` 下必须存在当前有效的 `plan` Approval；
- `task publish` 在 `balanced/supervised` 下必须存在当前有效的 `publish` Approval；
- `manual-pty` 发生后，之前的 Verification/Review 证据失效；
- `scope-change` 不允许继续当前 Run；
- Steering Round 超限进入安全暂停，由用户决定 Replan、Abort 或提高新 Run 的预算；
- Merge 在所有 Profile 中仍不可用。

## 交互入口

MVP CLI 提供：

```text
marshal task approve --run RUN_ID --gate plan|publish
marshal task intervene --run RUN_ID --kind clarification|implementation-correction|scope-change|manual-pty --message PATH|-
marshal task pause --run RUN_ID
marshal task resume --run RUN_ID
```

Codex Desktop/Skill 只封装这些结构化操作，不通过自由文本绕过分类与 Gate。

## 验证

必须覆盖：

- Approval 对 Digest、Sequence、Review Round 和 Gate 的精确绑定；
- 旧 Approval 在新证据/新 Round 后失效；
- clarification/correction 可继续且完整留痕；
- scope-change 绝不写回冻结输入并返回新 Run；
- manual-pty 使旧 Verification/Review 无效；
- pause-turn 与 interrupt-step 不混淆；
- 未授权 Worker 不能伪造 Control Record；
- cmux 原生 PTY E2E 可观察、可 Steering、可暂停、可恢复；
- 所有路径最终仍经过独立 Verification 与 Review。

## 未采用方案

- **完全无人介入直到 PR**：方向错误发现过晚；
- **允许操作者任意输入 PTY 且不留痕**：无法证明代码来源和证据新鲜度；
- **每个 Tool Call 都等待批准**：吞吐过低，且把实现责任转回人类；
- **修改原冻结 TaskSpec 后继续同 Run**：破坏审计与可重放性；
- **把 TUI Transcript 当作审批或完成证据**：不稳定且可被人工修改。
