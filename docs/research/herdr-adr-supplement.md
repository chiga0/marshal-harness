# ADR 0009/0011 补充：herdr 状态作为 CompletionGate 辅助信号（实验分支，不进主干）

- 状态：提案（Proposed，实验分支 `exp/herdr-terminal-backend`）
- 日期：2026-08-09
- 关联：ADR 0009（TerminalSession 执行传输）、ADR 0011（密封启动与可判定 TUI 传输）、[herdr 对照](herdr-comparison.md)、[herdr POC](herdr-backend-poc.md)

## 背景

ADR 0011 规定：缺少自动 lifecycle/idle 证据时，原生 TUI 只允许受监督模式；屏幕文本或单独
WorkerResult 不能判定自动完成。cmux 后端没有可用的自动完成信号，因此 Marshal 对 cmux 仅开放
`supervised-confirmation`。

herdr 与 cmux 的关键差异：herdr 通过 **hooks 注入 + manifest 规则引擎** 持续产出每个 pane 的
`agent_status`（`blocked/working/idle/done/unknown`），并可用 `herdr agent wait --until <state>`
事件化等待。这是一个**自动的、可解释的注意力信号**，cmux 不具备。

## 决策

1. **herdr `agent_status` 仅作 CompletionGate 的辅助（advisory）信号，不具权威性。**
   - `HerdrBackend.AuxiliaryStatus(ctx, workspace)` 读取 `workspace get` 的 `agent_status`；
   - 它只能**提示**"该去看/可以结束本轮"，**不能**单独把 Attempt 标记完成；
   - 权威完成仍须 WorkerResult + Git Snapshot + 独立 Verification + Review（ADR 0011 不变）。
2. **`agent wait --until blocked|idle` 作为 Lead 的事件源**，替代轮询（消灭空转），
   但等待结果只驱动"唤醒 Lead"，不驱动状态转换。
3. **不放宽任何信任边界**：密封 LaunchEnvelope、凭据隔离、单写者、draft-only 发布全部不变。
4. 若 herdr 版本不提供 `agent_status`/`agent wait`，后端降级为 cmux 等价行为（仅 supervised）。

## 理由

- herdr 的 `blocked` 是"需要输入/批准"的注意力信号，与 Marshal 的"证据裁决"正交；把它当权威
  会重蹈"屏幕文本当完成"的覆辙（ADR 0011 明确禁止）。
- 但把它当辅助信号可显著改善受监督体验与轮询空转，且不引入信任风险（只读、只提示）。

## 后果

- 新增 `TerminalCapability` 候选 `auxiliary-attention`（可选，不参与 fail-closed 判定）；
- Live E2E（`TestLiveHerdrTerminalSession`，opt-in `MARSHAL_HERDR_PATH`）验证
  probe/create/send/read/auxiliary/close 真实可用（herdr 0.8.0 实测通过）；
- 密封 launcher 经 herdr pane 的握手（`Start`）仍需命令面标定（`pane run` 对复合命令不稳健，
  改用 `send-text`+`enter` 更贴近终端模型），列为下一步标定，不在本补充承诺范围。

## 未决

- 将 `agent_status` 映射到受监督模式的"结束本轮"建议的精确策略（阈值/去抖）需真实 Pilot 数据；
- 生产化需独立里程碑：herdr 版本矩阵、一致性测试、远程/回魂场景的 Live E2E。
