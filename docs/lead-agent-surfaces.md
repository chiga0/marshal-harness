# 主 Agent 接入界面

## 结论

Marshal 的主 Agent 不限定为 Codex CLI。MVP 同时支持以下交互界面：

1. Codex CLI：适合脚本化、CI 或无界面调用；
2. Codex Desktop：适合交互式规划、Review、终端操作和差异检查；
3. ChatGPT 手机端 Remote：远程连接运行 Codex Desktop 的开发机，继续同一个任务、批准操作并查看结果。

这三种方式使用同一套 Marshal CLI、TaskSpec、ReviewPacket 和 ReviewDecision 契约。界面变化不改变 Core 生命周期，也不改变谁有权验证和发布。

## 推荐入口：自然语言加轻量 Skill

用户入口保持自然：

```text
请使用 Marshal 帮我完成 XXX 任务。
```

同时提供名为 `marshal` 的轻量 Skill，把重复工作流固化下来。两者不是二选一：自然语言负责表达目标，Skill 负责稳定执行协议。当 Skill 的 `description` 匹配时，Codex 可以隐式启用；需要确定性调用时：

- Codex Desktop / ChatGPT：输入 `@marshal` 并从 Skill 选择器选中；
- Codex CLI / IDE：输入 `$marshal`，或使用 `/skills` 选择；
- `/marshal-harness-skill` 不作为约定入口，因为 Slash Command 与 Skill 是不同扩展面。

Skill 只承担以下职责：

1. 定位业务仓库并检查 `marshal` 版本与能力；
2. 将用户目标整理为 TaskSpec 草案并请求必要确认；
3. 通过 Marshal CLI 冻结并运行任务；
4. 在 `REVIEW_PENDING` 时读取有界 ReviewPacket，完成语义 Review；
5. 生成 Schema-valid ReviewDecision 并交回 Marshal；
6. 汇报 Outcome、产物与 PR/MR，不自行执行 Publisher 操作。

Skill 不实现状态机、不直接启动 Worker、不解释原始 transcript 为成功，也不能把失败的强制 Gate 改成通过。Marshal Core 始终是事实与副作用的唯一控制面。

团队级 Skill 可放在目标业务仓库的 `.agents/skills/marshal/SKILL.md`；跨仓库个人使用可安装到 `$HOME/.agents/skills/marshal/SKILL.md`。Marshal 仓库保留规范模板，后续可打包为 Plugin 分发。完整 Skill 在 File-based Review Bridge 可用的 Milestone 3 交付，避免发布一个只能模拟成功的空壳工作流。

## 接入边界

Core 只依赖抽象的 `LeadAgentBridge`：

```text
Codex CLI ----------\
                      > LeadAgentBridge <-> Marshal Core
Codex Desktop ------/          |
                                +-- ReviewPacket 导出
                                +-- ReviewDecision 导入
                                +-- 状态与 Outcome 查询
```

MVP 的首个实现是 File-based Bridge：

1. Marshal 在 `.marshal/runs/<run-id>/review-packet.json` 导出冻结的审查输入；
2. 主 Agent 检查真实 Diff、VerificationReport 与必需交付物；
3. 主 Agent 生成符合 Schema 的 `review-decision.json`；
4. Marshal 校验 Packet、Evidence Digest、时效性与强制 Gate，再接受 `accept`、`rework` 或 `reject`；
5. 只有有效的 `accept` 才能进入 Publisher。

主 Agent 可以通过 `marshal task review` 获得有界摘要，通过 `marshal task review --export` 获取完整 Packet，通过 `marshal task review --import <path>` 导入决策。具体参数在 CLI 实施时可调整，但文件契约必须保持稳定。

## Codex CLI 模式

Codex CLI 可以在仓库或任务 worktree 中调用同一个 `marshal` 可执行文件。需要无人值守集成时，可使用 Codex 的非交互调用生成 ReviewDecision；是否允许自动接受仍由 Repository Policy 决定。

Core 不直接解析 Codex 的自然语言终端输出。任何具有生命周期影响的决定都必须转换为 Schema-valid ReviewDecision。

## Codex Desktop 模式

Codex Desktop 是推荐的交互式主 Agent 界面。其项目级集成终端可以运行 `marshal`、Git 和仓库脚本，Codex 也可以读取终端输出、检查 Diff 和测试结果。Desktop 与 CLI 共享同一工作目录和文件契约，因此 MVP 不需要依赖未公开或界面专用的 Desktop API。

推荐流程：

1. 在 Codex Desktop 中打开业务仓库；
2. 由 Codex 与维护者制定并冻结 TaskSpec；
3. Desktop 终端调用 Marshal，Marshal 驱动本地 Worker；
4. Marshal 导出 ReviewPacket 后，由当前 Codex 任务完成 Review；
5. Codex 写入 ReviewDecision，Marshal 验证并决定返工或发布；
6. Desktop 展示 Outcome、Diff、测试与 PR/MR 链接供维护者最终确认。

未来可以增加 App Server、MCP 或专用 Desktop Bridge 来减少文件交接，但这些只是 `LeadAgentBridge` 的新适配器，不能复制状态机或绕过证据校验。

## 手机端 Remote 模式

ChatGPT 手机端的 Remote 可以连接一台正在运行 Codex Desktop 的 Mac 或 Windows 主机。手机端负责发送提示、继续任务、回答问题、批准命令并查看 Diff、测试、终端输出和截图；实际文件、凭据、工具与进程仍在已连接的开发机上运行。

这很适合“电脑持续执行，手机随时监督”的 Marshal 工作流，但有以下边界：

- 开发机必须保持开机、联网，并运行已登录的最新 Codex Desktop；
- 手机端和 Desktop 必须使用同一账号与 Workspace；
- Remote 需要先从 Desktop 完成设置，不能只靠 Codex CLI 初始化；
- 功能可用性可能受逐步开放和管理员策略影响；
- 手机批准操作不会改变 Marshal 的权限模型，Worker 仍不得获得 Publisher Credential；
- 网络断开时 Marshal 依靠本地 Journal 与状态机安全停留或继续，不能把手机连接当作持久化机制。

因此，手机不是另一个执行节点，而是 Codex Desktop 主 Agent 会话的远程控制面。

## 安全与审计

- Codex CLI、Desktop 与 Remote 都不能直接把自然语言“通过”当作发布授权；
- 每个 ReviewDecision 都绑定 `reviewPacketDigest` 与 `evidenceDigest`；
- 主 Agent 可以请求返工，但不能修改冻结的 TaskSpec；范围变化必须创建新 Run；
- Remote 批准仍受开发机上的 Codex 权限、Sandbox 和 Marshal Policy 约束；
- Publisher Credential 只在 Publisher 中按需解析，不写入 Desktop 对话、Worker 环境或 `.marshal/` 的持久文件。

## 官方能力参考

- [Codex 集成终端](https://learn.chatgpt.com/docs/integrated-terminal.md)
- [Codex Remote connections](https://learn.chatgpt.com/docs/remote-connections.md)
- [Codex 本地环境](https://learn.chatgpt.com/docs/environments/local-environment.md)
- [构建与调用 Skills](https://learn.chatgpt.com/docs/build-skills.md)
