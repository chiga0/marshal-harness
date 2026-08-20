# Transcript attestation preflight

本预检在派发独立 reviewer 前，机械核对 Qoder v7 原始 transcript、transcript metadata、最终 `WorkerResult` 与冻结 `TaskSpec`。它是 operator-local admission 证据，不是 Marshal Core 生命周期、重试或 ReviewDecision 权威。

使用 `templates/transcript-attestation-preflight.json` 生成 manifest，把 transcript、metadata、`WorkerRequest`、`WorkerResult`、`TaskSpec`、`CapabilitySnapshot` 与冻结的 `transcript-attestation-profile.json` 七个输入放在同一个紧凑的只读目录中，并填写各自原始字节的 `sha256` 与保守 `maxBytes`。`WorkerRequest.baseSha/specDigest/capabilityDigest` 会以 Core JCS 权威把 manifest 的 `sourceHead`、`TaskSpec` 与外部 admission evidence 绑定到实际 Attempt。先从同一 source tree 构建稳定路径的 `bin/marshal`，再以隔离 Python 启动其隐藏内部命令：

```bash
make build COMMIT="$(git rev-parse HEAD)"
env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin \
  /usr/bin/python3 -I -B .agents/skills/marshal/references/validate-transcript-attestation-preflight.py \
  --root /ABSOLUTE/COMPACT/INPUT/ROOT \
  --manifest manifest.json \
  --marshal /ABSOLUTE/REPOSITORY/bin/marshal
```

Validator 不生成、复制或执行 `/tmp` checker，也不把 pathname 或 held inode 误当成实际执行身份：它只执行用户显式传入的固定 Marshal 路径及 `internal qoder-transcript-check`，子进程使用只含固定系统 `PATH` 的等价 `env -i` 环境，并在 stdin 未发送时保持阻塞。Manifest 的独立 `marshal.sourceHead`、`marshal.executableSha256`、`marshal.version` 与 `marshal.internalCommandVersion` 必须来自同一个 clean locked source 构建；它们与 Attempt 的 `subject.sourceHead` 分别绑定，不可混用。Mac 的预期身份直接从 held Marshal raw bytes 解析 thin 64-bit Mach-O 唯一 SHA-256 CodeDirectory，实际身份再用固定系统 `codesign -dvvv +PID` 取得 full SHA-256 CDHash并精确比较；Linux CI 使用 `/proc/PID/exe` 的受限 raw SHA-256 做等价检查。身份不匹配、PID 探针失败，或固定路径/device/inode/raw digest 在任一复核点漂移时，都在发送 transcript/evidence 前终止仅由 validator 启动的 Marshal 子进程并 fail closed。Marshal 内部命令和 `codesign` 的 stdout/stderr 均由增量 reader 分别及合计施加硬上限；overflow/deadline 使用固定 `reasonCode`，仅终止、等待并回收 validator 自己创建的对应 `Popen`。输出的 `implementationDigests` 同时绑定 Marshal 原始字节摘要、build identity、内部命令摘要、实际 stdin envelope 摘要、expected held-bytes identity method、actual-process identity method 与 digest；这不是 held-fd execution 声明。

Validator 逐级使用 nofollow `dirfd` 打开文件，以硬上限分块读取，并在读取前后复核 inode、大小与时间戳。它要求：

- manifest 精确声明 `qoder-stream-json-1.2.0-v7`，其它 Adapter 或事件版本 fail closed；
- `TaskSpec`、`WorkerRequest`、`WorkerResult` 与 `CapabilitySnapshot` 全部通过 Core Draft 2020-12/语义契约，JCS digest、task/run/attempt/base/版本身份一致；
- 实际 tool 名称由冻结 profile 默认值或 `TaskSpec.worker.tools` 机械导出；file tool 的规范化路径必须留在精确 worktree、满足 `TaskSpec.scope` 且 Write/Edit 出现在 `declaredChangedFiles`，绝对越界、`..`、denyPath 与 symlink 逃逸全部 fail closed；
- 每个非 transport `Bash` 都绑定冻结 profile 的原始 command digest，并与 `declaredCommands` 的安全 `commandId/digest/status` 序列一一对应；
- final tee 使用唯一的 closed envelope，恰好成功一次且是最后一个 tool call；对应 `tool_result` 必须显式为 `kind=completed/exitCode=0/interrupted=false`，成功后没有任何事件，terminal 唯一；
- metadata 的字节数、事件数、tool 数、tool 名集合和 tee 统计与原始 transcript 一致。

成功输出使用 `marshal-transcript-attestation-v3` framing，并通过相邻的 `transcript-attestation-receipt.schema.json` 与 `templates/transcript-attestation-receipt.json` 冻结 closed receipt shape。v3 receipt 的 `implementationDigests` 绑定 Marshal raw digest、build identity、internal command digest、实际 stdin envelope digest、expected/actual process identity 与 validator/schema/profile；旧 v2 framing 和历史 v5/v6 receipt 只能作为历史材料，禁止字段改写或迁移。

典型固定 `reasonCode` 包括 `qoder-v7-transcript-invalid`、`forbidden-command-executed`、`command-binding-mismatch`、`tool-path-escape`、`tool-path-symlink-escape`、`tool-path-out-of-scope`、`write-path-not-declared`、`tee-result-not-explicit-success`、`input-digest-mismatch` 与 `transcript-meta-mismatch`。任一失败均应在 reviewer 派发前修 TaskSpec/Adapter 或建立 fresh-base successor，不应用 Worker rework 掩盖结构性问题。

当前实现只支持版本化冻结的 Qoder v7 JSONL 事件模型；Codex、Qwen 与旧 Qoder transcript 不会被猜测性兼容。v7 尚须取得 fresh Mac evidence，历史 v5/v6 evidence 与 receipt 不得迁移。

`references/fixtures/transcript-attestation/mac-qoder-v5-conformance-r3-receipt.json` 是一次历史 v5 Mac R3 输入的脱敏输出摘要：只保留身份、原始输入摘要、机械 observation、固定 `reasonCode` 与 attestation digest，不包含原始 transcript、prompt、自由文本或绝对用户路径。它只能证明所绑定的 v5 输入曾通过当时的 operator-local validator；v7 consumer/transport identity 必须取得 fresh Mac evidence，禁止迁移该 receipt 或任何 v6 evidence，也不能用它替代 Core authority、当前 Run freshness 或独立 reviewer。
