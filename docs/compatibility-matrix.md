# Compatibility Matrix

更新时间：2026-08-24。本矩阵由共享 Conformance Suite、Live Probe 与受监督 Pilot 证据生成，只记录已验证事实；未验证组合一律不承诺。

## 证据来源

- 共享 Conformance Suite：成功、Provider Failure、Permission Denial、Malformed Event、Output Limit、Cancellation、Worktree Identity 破坏与 Credential Isolation 场景；Fake 与真实 Adapter 使用同一 Lifecycle Fixture。
- Live Adapter E2E：OpenCode、Qwen Code 与 Pi `0.83.0` 各自在本机临时 Git 仓库以 captured transport 完成最小修改；该历史 Pi Live E2E 的证据只覆盖 Pi `0.83.0`，不得外推到 Pi `0.84.1`。
- `marshal doctor` Live Probe：冻结 executable realpath、SHA-256 与精确版本；未知版本默认拒绝，只有显式 Experimental Policy 才能运行并在 Outcome 标记。
- 受监督 cmux Pilot（2026-08-05）：真实 Qwen TUI 通过 `terminal.StartPrepared`、密封 `LaunchEnvelope` 与 digest-bound 映射启动，完成屏幕观察、任务产物精确校验、Pause/Resume、InterruptStep 与 Terminate。
- Pi 0.84.1 精确兼容升级（2026-08-11，从锁定 main 独立实现）：Adapter `0.2.0` 保持 session protocol v3 与 raw JSONL 审计语义；上游真实最小 wire（`assistantMessageEvent` 内的 `text_delta`/`contentIndex`/`delta`）由具名协议 fixture 锁定并经独立 Verification。该证据是协议级 Verification，不是真实 Pi 0.84.1 captured Live E2E。
- Qoder CLI 候选机制：代码只允许裸 semver `>=1.1.27 <1.2.0` 进入独立 conformance 判定，但每个实际二进制仍须以自身 realpath、SHA256 digest、精确版本和当前 host 取得独立 signer 签发的真实 credentialed live evidence。hermetic fixture、版本范围命中和建议式 discovery 都不是严格 authority 准入证据。Adapter `0.1.8` / event contract `qoder-stream-json-1.2.0-v7` 的 WorkerResult transport digest 精确绑定 staging basename/type/mode、`O_EXCL|O_NOFOLLOW|O_CLOEXEC`、launch 前 unlink 与 `nlink=0`、Worker 不获知 path/fd、staging/control 不同 inode、held dirfd/exact-inode commit/consume/cleanup、唯一 canonical Bash `tee`、Qoder 真实 `command` + 有界非权威 `description` envelope、exactly-once tee-last、denial extractor、transcript contract与声明 consumer policy。consumer 在完整 Schema 验证前只用 held executable identity 覆盖 `adapter.executable` 与 `adapter.version`；不得合成任何语义字段，`taskId/runId/attemptId/adapter.id` 仍须来自声明并精确匹配，缺失、未知、重复或无效声明固定为 `protocol-invalid/do-not-retry`。任何字段变化都会改变 transport 与 suite digest。Mac 可显式设置 `MARSHAL_QODER_MODE=ordinary-user`（Codex 对应 `MARSHAL_CODEX_MODE=ordinary-user`）按普通用户模式配置；该模式不提供 signed authority、APAP 或恶意代码 sandbox，doctor 必须标记 `authorityMode=ordinary-user`，且 `0.1.8/v7` 仍须取得新的真实 Mac conformance 才能晋升为默认 production Worker。旧 `0.1.2/v3`、`0.1.3/v4`、`0.1.4/v5`、`0.1.5/v6` evidence、摘要、receipt 或历史普通用户任务结论不得迁移到新 transport，也不构成 hardened authority 升级。
- Qoder 1.1.28 ordinary-user live capability probe（2026-08-24）：ADPT-03 Adapter `0.1.8`（argv 预授权 `--allowed-tools Bash`、版本下限 `1.1.27`）在 Mac 以显式 `MARSHAL_QODER_PATH` 指向真实 qodercli `1.1.28`、`MARSHAL_QODER_MODE=ordinary-user` 通过 planning selection 的真实 version/capability probe（`probeStatus=supported`，CapabilitySnapshot digest `sha256:52c5c45b16e8e6bcc390772e869de9ede48d9ea5cd6469e86b2632fffe68fba9`）。这是晋升阶梯“真实只读 live probe”级证据，不构成独立只读 conformance、WorkerResult tee-last 纪律验证或 production 晋升；该证据只证明普通用户兼容性，不升级为 hardened authority。

## 版本锁定

| Adapter | Adapter 版本 | 锁定 Provider 版本 | 配置变量 | Probe 结论 |
| --- | --- | --- | --- | --- |
| `opencode` | `0.1.0` | OpenCode `1.18.13` | `MARSHAL_OPENCODE_PATH` | `supported` |
| `qwen` | `0.1.0` | Qwen Code `>=0.21.5 <0.22.0` | `MARSHAL_QWEN_PATH` | `supported` |
| `qoder` | `0.1.8` | Qoder CLI `>=1.1.27 <1.2.0`（逐 binary evidence） | `MARSHAL_QODER_PATH` | 严格模式 `pending live evidence`；显式 Mac `ordinary-user` 已配置，qodercli `1.1.28` 于 2026-08-24 通过真实 live capability probe 并进入首个低风险写任务阶段；新 `v7` transport 尚待真实 conformance 与独立只读 conformance，不能迁移 `0.1.2/v3`、`0.1.3/v4`、`0.1.4/v5`、`0.1.5/v6`、`0.1.6/v7` 或 `0.1.7/v7` 摘要，也不提供 hardened authority |
| `pi` | `0.2.0` | Pi `0.84.1` | `MARSHAL_PI_PATH` | 代码锁定 `supported`；Live Probe 未执行 |
| `codex` | `0.1.0` | Codex CLI `0.145.x` | `MARSHAL_CODEX_PATH` | 严格模式待 authenticated fd-exec；显式 Mac `ordinary-user` 可用但不提供 hardened authority |

已注册的三个 Adapter 都只接受显式绝对 executable 路径；注册不搜索 `PATH`，不回退同名或近似命令。Probe 后二进制身份变化会以 `binary-replaced` fail-closed。OpenCode 与 Pi 继续使用精确版本锁；Qwen Code 与 Qoder 均已改为兼容 semver 范围准入（Qwen 为 `>=0.21.5 <0.22.0`，与 Qoder 同模式，范围命中即 supported，minor 边界 0.22.0 及以上仍 fail closed）。Qoder 是唯一额外要求逐 binary credentialed live evidence 的候选 Adapter，命中范围不会继承其他 patch 的证据。Qoder 的每个实际二进制必须以自身 realpath、SHA256 digest、精确版本、当前 host、authority mode、event contract 与 WorkerResult transport digest 重新完成真实 live probe 并取得新 evidence；当前 `0.1.8/v7`（ADPT-03：argv 预授权 Bash 工具并把版本下限升至 `1.1.27`）已于 2026-08-24 以 qodercli `1.1.28` 取得 Mac `ordinary-user` 真实 live capability probe 证据（planning selection probe，CapabilitySnapshot digest `sha256:52c5c45b16e8e6bcc390772e869de9ede48d9ea5cd6469e86b2632fffe68fba9`），但 signed credentialed live conformance 与独立只读 conformance 尚未完成，production 晋升未闭环，仍不能从旧 `0.1.2/v3`、`0.1.3/v4`、`0.1.4/v5`、`0.1.5/v6`、`0.1.6/v7`、`0.1.7/v7` evidence、receipt 或人工摘要推导 `supported`。Mac ordinary-user 也必须为新 transport 补做真实 conformance 才能晋升为默认 Worker；这项验证只证明普通用户兼容性，不升级为 hardened authority。所有 Adapter 都拒绝前缀匹配与隐式 fallback，并在门禁不满足时于 Worker 进程启动前 fail closed。

Codex Adapter #136 仍处于开放状态，不属于上述已注册集合。其 patch 版本门禁为 `0.145.x`，但版本兼容不等于平台执行边界通过：Linux 实现把当前 launcher 与 Codex 源 inode 复制到加入 write/grow/shrink/seal 封印的匿名 `memfd`，digest/version probe 与 Worker exec 全部使用同一持有 FD；Darwin 缺少 `fexecve`/`execveat`，且 `/dev/fd/N` 不能执行，因此 Probe 返回带稳定原因的 `unsupported`，BindConformance 与 Run 均永久拒绝。不得通过同 UID 可 `chmod`/replace 的私有 pathname 快照规避该门禁。Darwin 后续支持需要独立的 signed/privileged launcher 设计及 ADR；在该工作完成并取得独立 conformance 证据前，不关闭 #136、不宣称 Codex Worker ready。

Pi Adapter `0.2.0` 与 Pi `0.84.1` 的升级保持 session protocol 版本精确为 3 且 raw JSONL 审计语义不变：Marshal 不删除、不压缩、不改写 Provider transcript 字段，compact LF JSONL 按原始字节保留。0.84.1 的真实最小 `message_update` wire 为 `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"…"}}`：事件仍携带 `assistantMessageEvent`，其 `text_delta` 只含 `contentIndex` 与线性增量 `delta`，不含消息标识字段、不含 `partial`，也没有顶层累积 `message` 快照。该上游 wire 合同由具名协议 fixture（如 `TestCaptureJSONLAcceptsPi0841NormalizedMessageUpdate`）锁定并经独立 Verification；本轮升级不是真实 Pi 0.84.1 captured Live E2E。历史 Pi `0.83.0` Live E2E 证据不适用于 0.84.1；0.84.1 的 Live Probe、captured transport 与非交互编辑 Live E2E 均未执行，其兼容性目前只由代码精确版本锁定与协议级单元测试支撑。

## 能力矩阵

| 能力 | opencode 1.18.13 | qwen >=0.21.5 <0.22.0 | qoder >=1.1.27 <1.2.0 | pi 0.84.1 | codex 0.145.x |
| --- | --- | --- | --- | --- | --- |
| captured transport | 已验收 | 已验收 | hermetic fixture 已验证；真实 credentialed Live E2E 待完成 | 协议 fixture 与单元测试已验证；0.84.1 Live E2E 未执行 | Linux fd-exec 代码路径待独立 Live Conformance；Darwin 不支持 |
| 结构化输出 | JSONL Event | JSONL Event | `stream-json` 候选合同；production evidence 待完成 | JSONL Event | JSONL Event 契约 fixture 已验证，尚未注册 |
| 非交互编辑 | 已验收 | 已验收 | hermetic fixture 已验证；production evidence 待完成 | 协议 fixture 与单元测试已验证；0.84.1 Live E2E 未执行 | 尚未通过独立 Live Conformance |
| Process Tree Cancellation | 已验收 | 已验收 | hermetic fixture 已验证；production evidence 待完成 | 已验收 | 单元测试已覆盖，尚未注册 |
| Session Policy | `ephemeral`/`persist`/`resume` | `ephemeral`/`persist`/`resume` | 仅 `ephemeral`；当前未支持 | 仅 `ephemeral` | 仅 `ephemeral` |
| Model Selection | 支持 | 支持 | 候选合同支持；当前未支持 | 支持 | 契约支持，尚未注册 |
| 原生预算 | Marshal 实施 wall-time 与 output-bytes 上限 | Provider 提供 wall-time、tool-calls（200）与 turns（60），Marshal 叠加上限 | Marshal 实施 wall-time 与 output-bytes 上限；当前未支持 | Marshal 实施 wall-time 与 output-bytes 上限 | Marshal 实施 wall-time 与 output-bytes 上限 |
| 声明式工具 Allowlist（`worker.tools`） | 支持：按声明生成最小 permission 配置，`debug config` 回读校验；未声明保持 profile 缺省 | 支持：`--exclude-tools` 反向收敛，声明 `bash` 不解除 shell 排除，对账由 Verification gate 兑底；未声明保持 profile 缺省 | named tools 不支持；显式空值冻结为 `--tools ""`；当前未支持 | 支持：`--tools` 精确交集，声明 `bash` 启动前 fail closed；未声明保持 profile 缺省 | 当前冻结 argv 无法表达逐工具 allowlist，声明非空时启动前拒绝 |
| 工具名单采集与对账 | `toolNames` 写入 transcript-meta，Verification `tool-allowlist` required gate 对账 | 同左 | production evidence 待完成 | 同左 | 尚未注册 |
| 原生 TUI（受监督 Pilot） | 冻结 `TerminalLaunchSpec` 已就绪，Pilot 未执行 | Pilot 通过（cmux，2026-08-05） | 不在当前候选准入范围 | 冻结 `TerminalLaunchSpec` 已就绪，Pilot 未执行 | 不在 #136 首切片范围 |
| 自动 CompletionGate | 无，仅 `supervised-confirmation` | 无，仅 `supervised-confirmation` | 无；当前未支持 | 无，仅 `supervised-confirmation` | 不支持 |

## 调用方式

| Adapter | captured 命令形态 | 权限归一化 |
| --- | --- | --- |
| opencode | `opencode run --pure --format json` | 环境 allowlist、独立 Temp/Home/Config 与 fail-closed permission 配置；声明 `worker.tools` 时为最小 permission 配置并经 `debug config` 回读校验 |
| qwen | `qwen --safe-mode --approval-mode auto-edit --exclude-tools ...` | 按名排除 shell、sub-agent、web/network 与 computer-use 工具；`--safe-mode` 关闭 hooks、extensions、skills、MCP 与 QWEN.md；声明 `worker.tools` 时反向排除未声明工具 |
| qoder | `qodercli --print --output-format stream-json --permission-mode accept_edits --no-session-persistence --allowed-tools Bash --config-dir ... --setting-sources "" --cwd ...` | 候选合同使用完整替换环境、独立 config 与隔离 scratch；Qoder 1.1.28 起 accept_edits 下对 Bash 调用新增 permission 询问（非交互无 handler 会拒发 WorkerResult tee），argv 以 `--allowed-tools Bash` 预授权恢复 1.1.23-1.1.27 既有放行语义，版本下限随之升至 1.1.27；真实 credentialed live evidence 尚未完成，当前 production 不支持 |
| pi | `pi --mode json --print` 加无 shell 工具 allowlist | `--no-approve`、`--no-extensions`、`--no-skills`、`--no-prompt-templates`、`--no-themes`、`--no-context-files` 等硬化 Flag；声明 `worker.tools` 时 `--tools` 收敛为声明集与工具面的精确交集 |
| codex | Linux：经 authenticated fd launcher 执行 `codex exec --json --ephemeral`；Darwin：不启动 | `approval=never`、`workspace-write`、显式关闭网络、忽略用户配置/rules；尚未完成独立 Live Conformance 与正式注册 |

## 已知限制

- Local Profile 不构成恶意代码沙箱；已启用 Agent 都在普通宿主机子进程中运行，不宣称抵抗同 UID 恶意进程。Qoder 的候选 live verifier 另要求隔离 sandbox，但尚未完成 production evidence 与启用。
- 声明式工具 Allowlist 依赖 Provider 遵守自身配置：opencode/pi 由 Provider 调用层拒绝未声明工具，qwen 无法正向穷举时由 transcript 采集与 Verification `tool-allowlist` gate 对账兑底；任一成功越权调用判 required fail。
- Worker 环境永远不包含 GitHub Publisher 凭据；凭据只在 publish 阶段注入独立子进程。
- Pi 不支持 `persist`/`resume`；对 Pi 伪造 Session Resume 会在启动前失败。
- 原生 TUI 模式下屏幕文本不是完成协议；WorkerResult、Git Snapshot、独立 Verification 与 Review 仍是权威门禁。
- Qwen TUI 已知问题：`send` 多行文本后立即发送 Enter 会被粘贴处理吞掉；受监督操作需要等待粘贴 settle 后单独发送 Enter（2026-08-05 Pilot 实测），详见 [Operator Runbook](operator-runbook.md) 第 8 节。
