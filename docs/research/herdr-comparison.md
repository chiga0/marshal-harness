# herdr 与 Marshal 对照调研（v2 · 源码级）

- 初版：2026-08-08；v2 深挖：2026-08-08
- 对照对象：[herdr](https://github.com/herdrdev/herdr)（本机克隆 v0.8.0，79 tags，Rust）与 Marshal Harness（Go）
- 方法：v2 基于 herdr 源码逐模块分析（`src/detect/`、`src/terminal/state.rs`、`src/handoff_runtime.rs`、`src/api/`、`src/integration/`、`src/persist/`、`src/agent_resume.rs`），行号级引用

## 0. TL;DR

**herdr 是"无脑的神经系统"**：它拥有 Agent 的终端、感知 Agent 的状态、提供 agent 原生的控制与喊话原语，但**不做任务编排、不做证据裁决、不做发布治理**。"大脑"是外置的——人、一个自愿当监督者的 agent、或 Marshal 这样的外部编排系统。

**Marshal 是"大脑+司法系统"**：有显式 Lead 槽位与强制门禁，但没有身体（终端 UX/持久化）。

两者组合 = herdr 当身体与神经，Marshal 当大脑与司法。

## 1. herdr 内部机制（源码级）

### 1.1 Agent 状态感知：双通道（hooks 注入 + 屏幕规则引擎）

**通道一：向 Agent 自己的配置注入 hooks**（高保真信号）。`src/integration/claude_settings.rs` 用 jsonc AST 编辑 Claude 的 settings，`ensure_command_hook` 注入事件钩子：`PostToolUse → working`、`PostToolUseFailure → working`、`SubagentStop → working`、`PermissionRequest → blocked`、`SessionStart → …`。即 **Agent 在关键生命周期点主动上报状态**——"卡住等批准"不是猜的，是 Agent 自己说的。集成目标覆盖 16+ Agent（`integration/registry.rs`：pi/claude/codex/copilot/devin/droid/kimi/opencode/kilo/grok/…）。

**通道二：manifest 规则引擎**（兜底+无 hook 时的主信号）。`src/detect/manifests/*.toml` 每 Agent 一份规则集，如 amp.toml：

```toml
[[rules]]
id = "approval_footer"
state = "blocked"          # blocked/working/idle
priority = 300             # 优先级仲裁
region = "whole_recent"    # osc_title / whole_recent / bottom_non_empty_lines(N)
visible_blocker = true
any = [{ contains = ["waiting for approval"] }, …]
```

输入为 `DetectionInput{screen, osc_title, osc_progress}`（`detect/manifest.rs`）——屏幕快照 + OSC 标题/进度转义序列。manifest 三来源：bundled / remote（可远程更新、带版本与缓存）/ local override，`DetectionExplain` 提供完整可解释性（matched_rule、evaluated_rules、fallback_reason）。

**结论**：herdr 的 blocked/working/idle 是**注意力信号**（"该去看谁"），由 hooks（精确）+ 屏幕规则（广覆盖）融合而成，可解释、可远程更新规则——这是它"never hunt for the stuck one"的内核。

### 1.2 终端运行时与 handoff：handoff ≠ agent 间移交

`src/handoff_runtime.rs`（46 行）揭示：**handoff 是 herdr 服务器自身替换时的状态转移**——把 PTY `master_fd`、child_pid、尺寸、键盘协议、输入状态、初始历史 ANSI 序列化传给新服务器进程，"PTY、进程、agent 身份"存活，"in-flight requests、waits、subscriptions、client sockets、pane-to-pane messages"故意不保留（客户端重连重试）。这是"合盖/重启/升级后 Agent 继续跑"的机制，**不是任务移交**。

持久化在 `src/persist/`（snapshot/restore/io/plugin_registry）；**Agent 会话恢复**在 `src/agent_resume.rs`：持久化 `AgentSessionRef{Id|Path}`，恢复时按 Agent 生成 resume argv（`claude --resume <id>`、`codex resume <id>`、`copilot --resume=…`），并有注入消毒测试（`--resume=abc; rm -rf /` 用例）——**恢复命令是 herdr 拼的，所以它把这里当安全边界**。

### 1.3 控制面与通信：socket API = agent 原生原语

`src/api/`（server/schema/subscriptions/wait/event_hub）：CLI 与 socket 同面。能力清单：workspace/tab/pane 的增删查改、`read`、`send input`、`prompt`、`wait_for_output`（regex/substring + timeout，`wait.rs`，`AGENT_PROMPT_EFFECT_TIMEOUT_MS=5s`）、事件订阅。

**跨 Agent 通信 = 向对方 pane 注入终端文本 + 等对方输出/状态**（"prompt each other"、"wait until another agent is genuinely blocked"）。这是**喊话级通信**：无消息结构、无任务语义、无送达证明、无证据绑定。

### 1.4 herdr 没有的东西（源码确认）

无任务契约/TaskSpec、无生命周期状态机（只有 pane/agent 状态）、无验证/审查/发布组件（grep 无 evidence/verification 语义层）、无调度器（workspace/pane 由人或 agent 按需创建）、无凭据分权（pane 内 Agent 持环境既有凭据）。

## 2. 谁是"大脑"？跨 Agent 通信何时需要？

herdr **故意无脑**。通信原语的存在正是为了让任何想当大脑者接入：

1. **人（默认）**：状态视图 + 手动 prompt；
2. **监督者 agent（supervisor 模式）**：一个普通 agent 住在 pane 里，用 `wait-blocked` 醒来 → `read` 看屏 → `prompt` 回答被卡住的 worker → 睡去。**典型场景：worker 等批准/澄清，无人值守时由监督者代答**——把"卡到人来"变成"卡到监督者来"；
3. **外部编排系统（Marshal 槽位）**：用 spawn/prompt/wait/subscribe 当手脚，自带策略与门禁。

跨 Agent 通信的三个真实用例：**应答 blocked**（最高频）、**对等 handoff/流水线**（coder 写完喊 reviewer）、**监督循环**。但两个 agent 可以喊话喊出互信共识然后发布垃圾——herdr 不拦，因为它没有司法。

## 3. 对照表（v2 深化）

| 维度 | herdr | Marshal |
| --- | --- | --- |
| 层 | 终端/会话/注意力 | 任务/证据/治理 |
| 状态 | pane/agent 5 态（hooks+规则引擎，可解释） | Run 16 态（守卫转换，证据驱动） |
| "卡住"语义 | 注意力信号：该去看/去答 | 司法信号：该返工/该阻断/该等人 |
| 通信 | 终端喊话（prompt/wait-blocked） | 冻结契约 + Steering 记录 + digest 绑定 |
| 持久化 | PTY fd 转移 + snapshot + resume argv | Journal 重放 + 原子 Snapshot + Lease |
| 安全边界 | resume argv 注入消毒（点状） | 凭据分权/单写者/权限归一化（面状） |
| 大脑 | 外置（人/agent/编排系统） | 显式 Lead 槽位 + Core 强制门禁 |
| 扩展 | 插件市场 + 远程 manifest 更新 | Port/Adapter + 一致性测试 |
| OSS | 79 tags/brew/多语言站/sponsors | 0.1.0 待发/Pages 中文站 |

## 4. 集成设计（深化）

**Marshal Lead 住在 herdr 里**的原语映射（ADR 0009 后端边界的 herdr 实例化）：

| Marshal 需要 | herdr 提供 | 权威归属 |
| --- | --- | --- |
| 启动 Worker TUI | `pane run` + 密封 LaunchEnvelope 在 pane 内 exec | Marshal（信封一次性、owner-only） |
| 观察 | `pane read` / 订阅 | 仅观察，不裁决 |
| 注入 prompt/steering | `prompt`（=Send） | Marshal 记录 InterventionRecord |
| 完成辅助信号 | `wait blocked/idle` + hooks 上报 | **辅助**；权威仍是 WorkerResult+Snapshot+Verification（ADR 0011） |
| 崩溃/重启存活 | handoff fd 转移 + resume argv | herdr 管会话，Marshal 管 Run 证据 |

收益：受监督模式获得 ssh 远程与重启回魂；`wait-blocked` 消灭轮询空转（实测 30–50min/批）。**权威不争夺**：herdr 的状态是注意力，Marshal 的状态是裁决。

## 5. 差距清单与行动（更新）

| # | 差距 | 行动 | 状态 |
| --- | --- | --- | --- |
| 1 | 发布纪律 | 合入实现批后打 v0.1.0 | 进行中 |
| 2 | 安装体验 | install.sh | Marshal Run 执行中 |
| 3 | API 自描述 | `contract schema --all` | Marshal Run 执行中 |
| 4 | 分层治理 | AGENTS.md 三层 + MAINTAINERS | Marshal Run 执行中 |
| 5 | 多语言文档 | Pages 英文导航 | 待排 |
| 6 | 会话持久化 | 不追；集成点由 herdr 提供 | 决策已定 |
| 7 | 新 | herdr 的"规则远程更新"思路可借鉴于 Adapter 能力清单的版本化 | 记录 |

## 6. 结论

v1 说"正交两层"；v2 源码级确认：**herdr 把"感知+控制"做成了可解释、可远程演进的基础设施（manifest 规则引擎 + hooks 注入 + fd 级 handoff），把"决策"完全让渡；Marshal 把"决策+证明"做成强制门禁，把"身体"让渡**。二者组合是目前可见的最完整形态：**herdr 神经 + Marshal 司法**。
