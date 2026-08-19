# Transcript attestation preflight

本预检在派发独立 reviewer 前，机械核对 Qoder v5 原始 transcript、transcript metadata、最终 `WorkerResult` 与冻结 `TaskSpec`。它是 operator-local admission 证据，不是 Marshal Core 生命周期、重试或 ReviewDecision 权威。

使用 `templates/transcript-attestation-preflight.json` 生成 manifest，把 transcript、metadata、`WorkerRequest`、`WorkerResult` 与 `TaskSpec` 五个输入放在同一个紧凑的只读目录中，并填写各自原始字节的 `sha256` 与保守 `maxBytes`。`WorkerRequest.baseSha/specDigest` 会把 manifest 的 `sourceHead` 与 `TaskSpec` 原始字节绑定到实际 Attempt。执行：

```bash
python3 -B .agents/skills/marshal/references/validate-transcript-attestation-preflight.py \
  --root /ABSOLUTE/COMPACT/INPUT/ROOT \
  --manifest manifest.json
```

Validator 逐级使用 nofollow `dirfd` 打开文件，以硬上限分块读取，并在读取前后复核 inode、大小与时间戳。它要求：

- manifest 精确声明 `qoder-stream-json-1.2.0-v5`，其它 Adapter 或事件版本 fail closed；
- `TaskSpec`、`WorkerResult`、metadata 与 manifest 的 task/run/attempt/版本身份一致；
- 实际 tool 名称同时满足 manifest allowlist、forbidden list 与 `TaskSpec.worker.tools`；
- 每个非 transport `Bash` 都绑定原始 command digest，并与 `declaredCommands` 一一对应；
- final tee 使用唯一的 closed envelope，恰好成功一次且是最后一个 tool call；tee 成功结果后没有任何 `tool_use`，terminal event 为 `success/end_turn`；
- metadata 的字节数、事件数、tool 数、tool 名集合和 tee 统计与原始 transcript 一致。

典型固定 `reasonCode` 包括 `forbidden-tool-executed`、`forbidden-command-executed`、`undeclared-command-executed`、`declared-command-mismatch`、`result-tee-count-invalid`、`result-tee-not-last`、`post-result-tool-use`、`input-digest-mismatch` 与 `transcript-meta-mismatch`。任一失败均应在 reviewer 派发前修 TaskSpec/Adapter 或建立 fresh-base successor，不应用 Worker rework 掩盖结构性问题。

当前实现故意只支持已有真实 Mac 证据冻结的 Qoder v5 JSONL 事件模型；Codex、Qwen 与旧 Qoder transcript 不会被猜测性兼容。
