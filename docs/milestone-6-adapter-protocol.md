# Milestone 6 Agent Adapter 协议

状态：`FROZEN_FOR_IMPLEMENTATION`

冻结日期：2026-08-04

## 共同边界

- Adapter 只执行配置中给出的 absolute executable，并在每次 `Probe` 与 `Run` 前核对 realpath、SHA-256 和精确版本。
- Worker 进程使用 direct argv、独立进程组、受限环境变量和有界 stdout/stderr；取消或超时必须终止整个进程组。
- Worker 只能写受管 worktree 与 Attempt 输出目录，不获得 GitHub、云平台、SSH 或 Marshal Publisher 凭据。
- Provider 退出码只是必要条件，不是成功证明。成功还必须包含 Provider 的终止事件，以及通过 Schema 验证且身份与请求一致的 `WorkerResult`。
- `ephemeral` 不保存 Session；只有 Provider 明确返回 Session ID 且 Adapter 已验证 Session 能力时，才允许 `persist`/`resume`。
- Local Profile 只能降低误操作风险，不构成针对恶意仓库或恶意代码的安全沙箱。

## OpenCode 1.18.12

- 使用 `run --pure --format json`；Session、Model 与 Prompt 均通过独立 argv 传入。
- 使用受管 `OPENCODE_CONFIG_CONTENT` 冻结权限，并在启动前读取 resolved config 进行 fail-closed 校验。
- 以 JSONL 中的 Session 身份、进程成功退出和合法 `WorkerResult` 共同判定成功。

## Qwen Code 0.21.5

- 固定使用 `--safe-mode --approval-mode auto-edit --output-format stream-json`。
- 使用 0.21.5 的真实参数 `--exclude-tools` 显式禁用 `shell`、`agent`、`create_sub_session`、网络访问和 Computer Use 等非文件编辑能力；`safe-mode` 只负责关闭自定义配置、Hook、Extension、Skill 与 MCP，不能单独作为权限边界。
- 固定使用 `--max-wall-time`、`--max-tool-calls` 与 `--max-session-turns`；Marshal 仍实施外层超时和输出字节上限。
- 成功必须出现 0.21.5 实际输出的 `system/init`，其中 `qwen_code_version`、`cwd` 与 `session_id` 必须匹配受管上下文；还必须出现终止 `result`，且 `result.subtype=success`。仅退出码为 0 不算成功。
- Session 策略必须锁定到显式参数：`ephemeral` 使用 `--chat-recording=false`；`persist` 与 `resume` 使用 `--chat-recording=true`；`resume` 还必须传入任务冻结的精确 Session ID，禁止隐式 `--continue`。
- 禁止 `--yolo`。它会在没有 sandbox 时自动批准 shell 等宿主机权限，与 Local Profile 的最小权限目标冲突。
- `ephemeral` 不传 `--continue`/`--resume`；`resume` 只接受请求中冻结的精确 Session ID。

## Pi 0.83.0

- 固定使用 `--mode json --print --no-approve --no-extensions --no-skills --no-prompt-templates --no-themes --no-context-files`。
- 工具 allowlist 仅为 `read,grep,find,ls,write,edit`；不授予 `bash`，因此 Worker 不能直接执行 Git、GitHub CLI、Publisher 或任意命令。
- Marshal 在 Worker 完成后独立执行 Verification 命令；Pi 的文本声明不能替代验证证据。
- 成功必须以 version `3` 的 `session` 事件开始，事件中的 `cwd` 必须等于受管 worktree，并出现终止 `agent_end`；同时要求进程退出码为 0 和合法 `WorkerResult`。
- M6 只声明 `ephemeral`，并固定使用 `--no-session`。当前 `WorkerRequest` 没有受管 Pi Session 目录与 Session ID 映射，因而 `persist`/`resume` 必须在启动前 fail-closed；不得退化为项目或用户默认 Session 搜索。未来增加持久化契约后再单独开放。

## 审计结论处理

Qwen 只读审计曾建议使用 `--yolo`。该建议因扩大宿主机命令执行权限而被 Codex Reviewer 驳回；本文件中的 `auto-edit + explicit deny` 是冻结决策。Pi 首次实现尝试超过 11 分钟且没有输出或文件产物，已按超时失败终止，不作为实现证据。
