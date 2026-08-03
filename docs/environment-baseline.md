# 本地环境基线

- 观察日期：2026-08-03
- 用途：仅用于实施规划；Runtime Probe 才具有权威性。

文档审计观察到以下本地工具：

| 工具 | 版本/状态 | 设计影响 |
| --- | --- | --- |
| Codex CLI | `0.145.0` | 可以进行 Non-interactive Lead/Review Integration |
| Codex Desktop | 当前工作环境可用 | 可通过项目集成终端运行 Marshal，并作为交互式 Lead/Review 界面 |
| ChatGPT 手机端 Remote | 依赖账号开放与 Desktop 配对 | 可远程继续 Desktop 上的 Codex 任务、批准操作并检查输出；执行仍发生在开发机 |
| `qwen` | `0.21.3` | Qwen Adapter 候选 Executable |
| `qwen-code` | `0.15.3` | 独立旧入口，不能作为隐式 Fallback |
| OpenCode | `1.17.0` | 候选 JSON one-shot，后续可接 ACP/Server |
| Pi | `0.83.0` | 候选 JSON one-shot，后续可接 RPC |
| Git | `2.50.1` | 支持 Worktree |
| GitHub CLI | `2.90.0`，已认证 | 可以进行 GitHub Publisher Spike |
| GitLab CLI | 未安装 | GitLab Publishing 延后 |

该表不是兼容性承诺。每次 Run 进入 `READY` 前，`marshal doctor` 必须探测精确路径、版本、Flag 与有效能力。

Codex Desktop 与 Remote 的产品要求及可用性以[主 Agent 接入界面](lead-agent-surfaces.md)中的官方文档为准。Marshal 不把 Remote 可用性作为本地 Run 的前置条件。
