# Compatibility Matrix

更新时间：2026-08-18。本矩阵由共享 Conformance Suite、Live Probe 与受监督 Pilot 证据生成，只记录已验证事实；未验证组合一律不承诺。

## 证据来源

- 共享 Conformance Suite：成功、Provider Failure、Permission Denial、Malformed Event、Output Limit、Cancellation、Worktree Identity 破坏与 Credential Isolation 场景；Fake 与真实 Adapter 使用同一 Lifecycle Fixture。
- Live Adapter E2E：OpenCode、Qwen Code 与 Pi `0.83.0` 各自在本机临时 Git 仓库以 captured transport 完成最小修改；该历史 Pi Live E2E 的证据只覆盖 Pi `0.83.0`，不得外推到 Pi `0.84.1`。
- `marshal doctor` Live Probe：冻结 executable realpath、SHA-256 与精确版本；未知版本默认拒绝，只有显式 Experimental Policy 才能运行并在 Outcome 标记。
- 受监督 cmux Pilot（2026-08-05）：真实 Qwen TUI 通过 `terminal.StartPrepared`、密封 `LaunchEnvelope` 与 digest-bound 映射启动，完成屏幕观察、任务产物精确校验、Pause/Resume、InterruptStep 与 Terminate。
- Pi 0.84.1 精确兼容升级（2026-08-11，从锁定 main 独立实现）：Adapter `0.2.0` 保持 session protocol v3 与 raw JSONL 审计语义；上游真实最小 wire（`assistantMessageEvent` 内的 `text_delta`/`contentIndex`/`delta`）由具名协议 fixture 锁定并经独立 Verification。该证据是协议级 Verification，不是真实 Pi 0.84.1 captured Live E2E。
- Qoder CLI 候选机制：代码只允许裸 semver `>=1.1.23 <1.2.0` 进入独立 conformance 判定，但每个实际二进制仍须以自身 realpath、SHA256 digest、精确版本和当前 host 取得独立 signer 签发的真实 credentialed live evidence。hermetic fixture、版本范围命中和建议式 discovery 都不是 production 准入证据。

## 版本锁定

| Adapter | Adapter 版本 | 锁定 Provider 版本 | 配置变量 | Probe 结论 |
| --- | --- | --- | --- | --- |
| `opencode` | `0.1.0` | OpenCode `1.18.13` | `MARSHAL_OPENCODE_PATH` | `supported` |
| `qwen` | `0.1.0` | Qwen Code `0.21.5` | `MARSHAL_QWEN_PATH` | `supported` |
| `qoder` | `0.1.0` | Qoder CLI `>=1.1.23 <1.2.0`（逐 binary evidence） | `MARSHAL_QODER_PATH` | `pending live evidence`；当前 production 不支持 |
| `pi` | `0.2.0` | Pi `0.84.1` | `MARSHAL_PI_PATH` | 代码锁定 `supported`；Live Probe 未执行 |

四个 Adapter 都只接受显式绝对 executable 路径；注册不搜索 `PATH`，不回退同名或近似命令。Probe 后二进制身份变化会 fail closed。OpenCode、Qwen Code 与 Pi 继续使用精确版本锁；Qoder 是唯一采用兼容 semver 范围的候选 Adapter，范围固定为 `>=1.1.23 <1.2.0`，但命中范围不会继承其他 patch 的证据。Qoder 的每个实际二进制必须以自身 realpath、SHA256 digest、精确版本和当前 host 重新完成真实 credentialed live probe 并取得新 evidence；当前尚无 production live evidence，不能报告 `supported`。所有 Adapter 都拒绝前缀匹配与隐式 fallback，并在门禁不满足时于 Worker 进程启动前 fail closed。

Pi Adapter `0.2.0` 与 Pi `0.84.1` 的升级保持 session protocol 版本精确为 3 且 raw JSONL 审计语义不变：Marshal 不删除、不压缩、不改写 Provider transcript 字段，compact LF JSONL 按原始字节保留。0.84.1 的真实最小 `message_update` wire 为 `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"…"}}`：事件仍携带 `assistantMessageEvent`，其 `text_delta` 只含 `contentIndex` 与线性增量 `delta`，不含消息标识字段、不含 `partial`，也没有顶层累积 `message` 快照。该上游 wire 合同由具名协议 fixture（如 `TestCaptureJSONLAcceptsPi0841NormalizedMessageUpdate`）锁定并经独立 Verification；本轮升级不是真实 Pi 0.84.1 captured Live E2E。历史 Pi `0.83.0` Live E2E 证据不适用于 0.84.1；0.84.1 的 Live Probe、captured transport 与非交互编辑 Live E2E 均未执行，其兼容性目前只由代码精确版本锁定与协议级单元测试支撑。

## 能力矩阵

| 能力 | opencode 1.18.13 | qwen 0.21.5 | qoder >=1.1.23 <1.2.0 | pi 0.84.1 |
| --- | --- | --- | --- | --- |
| captured transport | 已验收 | 已验收 | hermetic fixture 已验证；真实 credentialed Live E2E 待完成 | 协议 fixture 与单元测试已验证；0.84.1 Live E2E 未执行 |
| 结构化输出 | JSONL Event | JSONL Event | `stream-json` 候选合同；production evidence 待完成 | JSONL Event |
| 非交互编辑 | 已验收 | 已验收 | hermetic fixture 已验证；production evidence 待完成 | 协议 fixture 与单元测试已验证；0.84.1 Live E2E 未执行 |
| Process Tree Cancellation | 已验收 | 已验收 | hermetic fixture 已验证；production evidence 待完成 | 已验收 |
| Session Policy | `ephemeral`/`persist`/`resume` | `ephemeral`/`persist`/`resume` | 仅 `ephemeral`；当前未支持 | 仅 `ephemeral` |
| Model Selection | 支持 | 支持 | 候选合同支持；当前未支持 | 支持 |
| 原生预算 | Marshal 实施 wall-time 与 output-bytes 上限 | Provider 提供 wall-time、tool-calls（200）与 turns（60），Marshal 叠加上限 | Marshal 实施 wall-time 与 output-bytes 上限；当前未支持 | Marshal 实施 wall-time 与 output-bytes 上限 |
| 声明式工具 Allowlist（`worker.tools`） | 支持：按声明生成最小 permission 配置，`debug config` 回读校验；未声明保持 profile 缺省 | 支持：`--exclude-tools` 反向收敛，声明 `bash` 不解除 shell 排除，对账由 Verification gate 兑底；未声明保持 profile 缺省 | named tools 不支持；显式空值冻结为 `--tools ""`；当前未支持 | 支持：`--tools` 精确交集，声明 `bash` 启动前 fail closed；未声明保持 profile 缺省 |
| 工具名单采集与对账 | `toolNames` 写入 transcript-meta，Verification `tool-allowlist` required gate 对账 | 同左 | production evidence 待完成 | 同左 |
| 原生 TUI（受监督 Pilot） | 冻结 `TerminalLaunchSpec` 已就绪，Pilot 未执行 | Pilot 通过（cmux，2026-08-05） | 不在当前候选准入范围 | 冻结 `TerminalLaunchSpec` 已就绪，Pilot 未执行 |
| 自动 CompletionGate | 无，仅 `supervised-confirmation` | 无，仅 `supervised-confirmation` | 无；当前未支持 | 无，仅 `supervised-confirmation` |

## 调用方式

| Adapter | captured 命令形态 | 权限归一化 |
| --- | --- | --- |
| opencode | `opencode run --pure --format json` | 环境 allowlist、独立 Temp/Home/Config 与 fail-closed permission 配置；声明 `worker.tools` 时为最小 permission 配置并经 `debug config` 回读校验 |
| qwen | `qwen --safe-mode --approval-mode auto-edit --exclude-tools ...` | 按名排除 shell、sub-agent、web/network 与 computer-use 工具；`--safe-mode` 关闭 hooks、extensions、skills、MCP 与 QWEN.md；声明 `worker.tools` 时反向排除未声明工具 |
| qoder | `qodercli --print --output-format stream-json --permission-mode accept_edits --no-session-persistence --config-dir ... --setting-sources "" --cwd ...` | 候选合同使用完整替换环境、独立 config 与隔离 scratch；真实 credentialed live evidence 尚未完成，当前 production 不支持 |
| pi | `pi --mode json --print` 加无 shell 工具 allowlist | `--no-approve`、`--no-extensions`、`--no-skills`、`--no-prompt-templates`、`--no-themes`、`--no-context-files` 等硬化 Flag；声明 `worker.tools` 时 `--tools` 收敛为声明集与工具面的精确交集 |

## 已知限制

- Local Profile 不构成恶意代码沙箱；已启用 Agent 都在普通宿主机子进程中运行，不宣称抵抗同 UID 恶意进程。Qoder 的候选 live verifier 另要求隔离 sandbox，但尚未完成 production evidence 与启用。
- 声明式工具 Allowlist 依赖 Provider 遵守自身配置：opencode/pi 由 Provider 调用层拒绝未声明工具，qwen 无法正向穷举时由 transcript 采集与 Verification `tool-allowlist` gate 对账兑底；任一成功越权调用判 required fail。
- Worker 环境永远不包含 GitHub Publisher 凭据；凭据只在 publish 阶段注入独立子进程。
- Pi 不支持 `persist`/`resume`；对 Pi 伪造 Session Resume 会在启动前失败。
- 原生 TUI 模式下屏幕文本不是完成协议；WorkerResult、Git Snapshot、独立 Verification 与 Review 仍是权威门禁。
- Qwen TUI 已知问题：`send` 多行文本后立即发送 Enter 会被粘贴处理吞掉；受监督操作需要等待粘贴 settle 后单独发送 Enter（2026-08-05 Pilot 实测），详见 [Operator Runbook](operator-runbook.md) 第 8 节。
