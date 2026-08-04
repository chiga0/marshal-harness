# 在 Codex 中使用 Marshal

Marshal 的轻量 Skill 位于 `.agents/skills/marshal/`，Codex CLI 与 Codex Desktop 使用同一套文件契约。Desktop 任务也可以由手机端 Remote 监督，不需要为 Desktop 单独实现 Harness。

## 触发方式

显式触发时可以说：

```text
请使用 Marshal 完成这个任务：修复……，完成后独立验证并审查。
```

安装或加载 Skill 的环境也可以使用 `$marshal`。当用户没有点名 Marshal，但明确要求 Codex 作为主 Agent 驱动本地 Coding Agent、执行测试、审查、CI 与 PR 闭环时，Skill 的描述允许隐式触发。

## M3 文件桥接

Run 到达 `REVIEW_PENDING` 后：

```bash
marshal task review --run RUN_ID --json
```

Codex 读取 `.marshal/runs/<run-id>/review-packet.json` 和其中引用的证据，生成 Schema-valid ReviewDecision，再执行：

```bash
marshal task review --run RUN_ID --decision REVIEW_DECISION.json --json
```

Skill 不能直接改 `.marshal/` 状态、启动 Worker、push、创建 PR 或调用 Publisher。M4 开始后，真实 Worker 也必须通过 Marshal Adapter 启动；M5 的远端发布同样只能通过 Publisher 边界完成。

## 终态结果

`ACCEPTED`、`REJECTED`、`BLOCKED` 与 `NO_CHANGE` 会生成：

- `outcome.json`：机器可读、绑定最终 Decision 与 Evidence；
- `outcome.md`：中文验收摘要。

`REWORK_REQUESTED` 不是终态，不生成 Outcome；它保存本轮 Decision 和 ReviewPacket，下一轮必须携带未关闭 Blocking Finding。
