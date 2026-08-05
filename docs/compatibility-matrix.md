# Compatibility Matrix

更新时间：2026-08-05。本矩阵由共享 Conformance Suite、Live Probe 与受监督 Pilot 证据生成，只记录已验证事实；未验证组合一律不承诺。

## 证据来源

- 共享 Conformance Suite：成功、Provider Failure、Permission Denial、Malformed Event、Output Limit、Cancellation、Worktree Identity 破坏与 Credential Isolation 场景；Fake 与真实 Adapter 使用同一 Lifecycle Fixture。
- Live Adapter E2E：OpenCode、Qwen Code、Pi 各自在本机临时 Git 仓库以 captured transport 完成最小修改。
- `marshal doctor` Live Probe：冻结 executable realpath、SHA-256 与精确版本；未知版本默认拒绝，只有显式 Experimental Policy 才能运行并在 Outcome 标记。
- 受监督 cmux Pilot（2026-08-05）：真实 Qwen TUI 通过 `terminal.StartPrepared`、密封 `LaunchEnvelope` 与 digest-bound 映射启动，完成屏幕观察、任务产物精确校验、Pause/Resume、InterruptStep 与 Terminate。

## 版本锁定

| Adapter | Adapter 版本 | 已验收 Provider 版本 | 配置变量 | Probe 结论 |
| --- | --- | --- | --- | --- |
| `opencode` | `0.1.0` | OpenCode `1.18.13` | `MARSHAL_OPENCODE_PATH` | `supported` |
| `qwen` | `0.1.0` | Qwen Code `0.21.5` | `MARSHAL_QWEN_PATH` | `supported` |
| `pi` | `0.1.0` | Pi `0.83.0` | `MARSHAL_PI_PATH` | `supported` |

三个 Adapter 都只接受显式绝对 executable 路径；不搜索 `PATH`，不回退同名或近似命令。Probe 后二进制身份变化会以 `binary-replaced` fail-closed。

## 能力矩阵

| 能力 | opencode 1.18.13 | qwen 0.21.5 | pi 0.83.0 |
| --- | --- | --- | --- |
| captured transport | 已验收 | 已验收 | 已验收 |
| 结构化输出 | JSONL Event | JSONL Event | JSONL Event |
| 非交互编辑 | 已验收 | 已验收 | 已验收 |
| Process Tree Cancellation | 已验收 | 已验收 | 已验收 |
| Session Policy | `ephemeral`/`persist`/`resume` | `ephemeral`/`persist`/`resume` | 仅 `ephemeral` |
| Model Selection | 支持 | 支持 | 支持 |
| 原生预算 | Marshal 实施 wall-time 与 output-bytes 上限 | Provider 提供 wall-time、tool-calls（200）与 turns（60），Marshal 叠加上限 | Marshal 实施 wall-time 与 output-bytes 上限 |
| 原生 TUI（受监督 Pilot） | 冻结 `TerminalLaunchSpec` 已就绪，Pilot 未执行 | Pilot 通过（cmux，2026-08-05） | 冻结 `TerminalLaunchSpec` 已就绪，Pilot 未执行 |
| 自动 CompletionGate | 无，仅 `supervised-confirmation` | 无，仅 `supervised-confirmation` | 无，仅 `supervised-confirmation` |

## 调用方式

| Adapter | captured 命令形态 | 权限归一化 |
| --- | --- | --- |
| opencode | `opencode run --pure --format json` | 环境 allowlist、独立 Temp/Home/Config 与 fail-closed permission 配置 |
| qwen | `qwen --safe-mode --approval-mode auto-edit --exclude-tools ...` | 按名排除 shell、sub-agent、web/network 与 computer-use 工具；`--safe-mode` 关闭 hooks、extensions、skills、MCP 与 QWEN.md |
| pi | `pi --mode json --print` 加无 shell 工具 allowlist | `--no-approve`、`--no-extensions`、`--no-skills`、`--no-prompt-templates`、`--no-themes`、`--no-context-files` 等硬化 Flag |

## 已知限制

- Local Profile 不构成恶意代码沙箱；三个 Agent 都在普通宿主机子进程中运行，不宣称抵抗同 UID 恶意进程。
- Worker 环境永远不包含 GitHub Publisher 凭据；凭据只在 publish 阶段注入独立子进程。
- Pi 不支持 `persist`/`resume`；对 Pi 伪造 Session Resume 会在启动前失败。
- 原生 TUI 模式下屏幕文本不是完成协议；WorkerResult、Git Snapshot、独立 Verification 与 Review 仍是权威门禁。
- Qwen TUI 已知问题：`send` 多行文本后立即发送 Enter 会被粘贴处理吞掉；受监督操作需要等待粘贴 settle 后单独发送 Enter（2026-08-05 Pilot 实测），详见 [Operator Runbook](operator-runbook.md) 第 8 节。
