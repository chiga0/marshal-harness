# Adapter 晋升与 Mac 实证

> **何时必须读取：** 注册、选择、升级或修复 Adapter；执行 doctor、fake/live probe、conformance 或首个真实任务；使用 Qoder/Codex/Qwen/Pi/OpenCode；处理 executable、ordinary-user、WorkerResult、transcript、protocol/path/identity/version failure 时，必须完整读取。

## 配置与 truthful authority

- Worker executable 使用绝对配置：`MARSHAL_QODER_PATH`、`MARSHAL_CODEX_PATH`、`MARSHAL_QWEN_PATH`、`MARSHAL_OPENCODE_PATH`、`MARSHAL_PI_PATH`。不得因 PATH 中有同名程序就推断 configured/eligible。
- 首次使用或换版本先固定 absolute executable、held device/inode/raw digest、裸 `--version`、真实 `--help` argv、permission mode、协议/Schema、result transport 和 host OS/arch。fake fixture 只能补测试，不能替代真实 Mac 证据。
- doctor 的 `configured=false` 是硬 admission blocker。只有重新注入并验证精确路径、held digest、version 和真实 help 后，Adapter 才能进入选择序列。
- Mac 普通用户必须显式 `MARSHAL_QODER_MODE=ordinary-user` 或 `MARSHAL_CODEX_MODE=ordinary-user`；doctor 必须证明 `configured=true`、`compatibility=supported`、`authorityMode=ordinary-user`。
- ordinary-user 只证明当前普通用户 argv/env/权限；不得称为 hardened authority、APAP、sandbox、Linux authority 或恶意代码隔离。CapabilitySnapshot、doctor 和报告必须使用 ordinary-user 真实文案，不能沿用 strict managed-config。

`references/qoder-1.1.23-event-contract.json`、`references/codex-0.145.0-result-contract.json` 和 `references/codex-0.145-provider-schema-profile.json` 是无自由文本的人工审查/预检基线，不是 Core lifecycle authority。真实漂移由 Adapter parser、typed failure 和版本化 contract fail closed；禁止用手工 shape diff 冒充 admission。

### Codex 0.145.x provider schema preflight

Codex 0.145.x 首次/升级真实 probe 前，从真实 provider 输出复制 schema 和冻结 profile 到 source worktree、`.marshal` 外的紧凑 operator root；不读取 prompt/secret。构建 production checker并运行：

```bash
go build -o "$OPERATOR_DIR/codex-provider-schema-checker" \
  .agents/skills/marshal/references/codex_provider_schema_checker.go
python3 -I -B .agents/skills/marshal/references/validate-codex-provider-schema-preflight.py \
  --root "$PREFLIGHT_ROOT" \
  --schema RELATIVE_PROVIDER_SCHEMA.json \
  --profile .agents/skills/marshal/references/codex-0.145-provider-schema-profile.json \
  --checker "$OPERATOR_DIR/codex-provider-schema-checker"
```

只接受 exit 0、`status=pass`、`reasonCode=codex-provider-schema-compatible`。Receipt 从 `templates/codex-provider-schema-preflight-receipt.json` 的字段形状生成，以相邻 receipt Schema/Core probe 验证；它是 `mac-ordinary-user-operator-local` 且 `authorityClaim=none`，不得冒充 authority。Schema/profile 必须 clean relative、nofollow、bounded；不兼容或 malformed issue 固定 fail closed，停止 Codex 新派发，先修 Adapter/profile 后重新做一次真实 probe。

## 上线阶梯

新 Adapter 或版本升级严格按以下级别晋升：

`registry/doctor → fake conformance → 真实只读 live probe → 单个低风险写任务 → 独立只读 conformance → 常规调度`

任一级失败都停在该级修 Adapter/contract，不得把产品任务当 probe。每一级只保留一份按 executable digest、平台/架构、authority mode、协议/result transport 摘要索引的证据；相同 identity 才复用。

首个真实任务前冻结 compatibility matrix：真实 argv、exit-code semantics、permission-denial semantics、output limit、session/turn budget、WorkerResult transport、path character/absolute-path 限制、network/repository write permissions。未知项先只读 probe，不在写任务中猜。

首个写任务通过后立即在同一 sourceHead 派独立只读 conformance；两份证据一致才把 Adapter 提升为默认 Worker。identity/version drift 时停止新派发，回到 live-probe；这仍是 Lead admission，不得描述成 Core 已实现的自动 promotion state machine。

## 证据复用与失败预算

只在以下 tuple 全部一致时复用最近一次已验证摘要：

`sourceHead + OS/arch + executable device/inode/digest + raw version + adapter config + authority mode + protocol/schema + result transport/path/permission identity`。

任一字段变化立即失效，重新做一次 Mac live preflight。复用证据不能替代任务特有 acceptance/gates 或独立 reviewer。

- failure/retry 分类只消费 Core 已持久化 typed failure/Outcome；watchdog/doctor 是诊断证据，不是 retry authority。Core 没有 typed failure或类别未知时 fail closed。
- 只有 Core 判断为 transient provider timeout、DNS/rate-limit 或 transport backpressure 且 preflight 仍匹配，才按 Policy 在原 taskId 做有限 operational retry；记录 attempt、预算和 backoff。
- `result-missing`、path/protocol/identity/version drift、旧 artifact/base、verification 后 worktree 变化属于结构性 `protocol-invalid/do-not-retry` 路径：同一稳定 failure signature 只消费一次裁决，修 Adapter/Core/TaskSpec 后通过 CLI 建 fresh-base successor。
- Qoder/Codex 的冻结 argv 当前不能表达非空 named `worker.tools`。plan pre-mortem 必须先以 `adapter-named-worker-tools-unsupported` 阻断；即使绕过该 operator gate，Adapter `Run` 仍返回 typed `protocol-invalid/do-not-retry` 并保留各自 `ErrUnsupportedWorkerTools` identity，禁止归为 `connection-failure/retryable`。缺省和显式空数组继续使用各 Adapter 已冻结的既有投影语义。
- successor 不重置人工预算。稳定签名绑定 `sourceHead + executable digest + authority mode + protocol/result transport digest + Core failure kind/evidence digest`；签名未变不得再次派发。签名仅供 operator admission，不能替代 Core retry/rework/Policy。

## WorkerResult transport

把 WorkerResult 当协议门禁，而非模型自由文本：

- 验证最终落盘路径/边界、唯一写入 primitive 与 argv、单次写入、staging basename/type、staging/control 不同 inode、held dirfd exact-inode consume/cleanup、permission-denial extractor、transcript event contract。
- 任一字段变化使 transport 摘要失效。fixture 只经该 transport 写入，禁止又从 control path 注入结果制造假阳性。
- Provider 拒绝带 `:` 的 attempt path、绝对路径或重复 shell 写入时，Adapter 提供受控 colon-free alias 或 provider-native output channel；不得把 `result-missing` 当偶发模型错误重试。
- exit 0、git diff 或自然语言 final 不能自动合成语义 WorkerResult，也不能为 compatibility replacement 放宽 exact identity。

Qoder 的受控 `tee` 是 Worker-side final-declaration 纪律：Worker 在内存中完成 payload，以不依赖实现符号的最短 summary，恰好一次成功 `tee` 作为最后 tool call，然后立即 `end_turn`。之后禁止任何 Read/Edit/Write/Bash、第二次 tee、检查、纠错或替换 staging，即使发现 typo 也保留原声明。真正线性化点是进程终态后 Adapter 完成 transcript、held dirfd/exact-inode、Schema/identity 验证并写入 held control leaf。

这条纪律只由 Adapter prompt 投影，TaskSpec 不得重复/改写。在 tee-last prompt、transcript“恰好一次成功 tee 且后无 tool_use”校验、regression fixture 和真实 Mac conformance 全部通过前，它是 promotion blocker；post-tee access 固定归类结构性 failure。

若 live probe 出现“deliverable 已生成、Provider exit 0，但 transport protocol-invalid”，不得复制 TaskSpec 再跑。比较 Adapter 保存的 transcript/meta 中真实 `tool_use.input` 与冻结 envelope fixture，区分执行语义和 Provider 自动元数据；仅把真实观察、类型封闭、canonical 编码且不影响执行的字段加入版本化 envelope。Envelope 变化同步 bump Adapter/event contract、transport digest 和 fresh live evidence；未知字段继续拒绝。

## Qoder v7 transcript attestation

当前只接受 `qoder-stream-json-1.2.0-v7` 与 `qoder-v7-transcript-attestation-v4`；v5/v6 evidence、receipt、profile 或摘要均为历史材料，不得迁移到当前 promotion。

Qoder 真实只读 live probe、首个低风险写任务和独立 conformance 必须分别通过 transcript attestation，并把 subject/input/attestation digest 纳入脱敏级别摘要。缺失、失败或 identity 不符不得晋升或复用。

进程终态后、独立 reviewer 前，从 `templates/transcript-attestation-preflight.json` 生成 operator-local manifest。把当前 Attempt 的以下原始文件复制到 source worktree 和 `.marshal` 外的紧凑临时目录，并填写 raw SHA-256/maxBytes 与 Attempt/Adapter/protocol/permission identity：

- `control/output/qoder-transcript.jsonl`
- `control/output/qoder-transcript-meta.json`
- `worker-request.json`
- 权威 `worker-result.json`
- Run 根 `task-spec.json`、`capability-snapshot.json`
- 当前 source tree 的 `references/transcript-attestation-profile.json`

allow/deny tool、required constraint、forbidden command、实际 tool/command、`declaredCommands` 和 command digest 只从已验证 TaskSpec/profile/Adapter transcript 机械导出；manifest 不维护人工副本。禁止读取/提交 prompt，禁止把 raw transcript、WorkerResult 自由文本或绝对用户路径带入仓库。

```bash
make build COMMIT="$(git rev-parse HEAD)"
env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin \
  /usr/bin/python3 -I -B .agents/skills/marshal/references/validate-transcript-attestation-preflight.py \
  --root "$ATTESTATION_DIR" \
  --manifest transcript-attestation-preflight.json \
  --marshal "$REPOSITORY_ROOT/bin/marshal"
```

只有 exit 0、`status=pass`、`reasonCode=transcript-attestation-pass` 且 subject 等于当前 Attempt，才能继续 freshness/reviewer。

Validator 只调用用户显式传入、已构建且稳定路径的 Marshal 二进制内部命令 `marshal internal qoder-transcript-check`，不得再构建、复制或执行随机临时 checker。它必须先让该内部命令在 stdin 未发送时保持阻塞。Mac expected identity 从 held Marshal raw bytes 解析 thin 64-bit Mach-O 唯一 SHA-256 CodeDirectory，actual 用固定 `/usr/bin/codesign -dvvv +PID` 获取 full SHA-256 CDHash；Linux CI 用 `/proc/PID/exe` raw SHA-256。只有固定路径、held device/inode/raw digest、expected/actual process identity 全部 exact match 才发送 evidence。pathname/inode 复核只是防御纵深，不得说成 held-fd execution 或实际执行 identity。

Marshal 内部命令/codesign stdout、stderr 和 combined 都增量读取并有硬上限；子进程使用等价 `env -i` 的封闭环境，overflow/deadline 固定 fail closed，只 terminate/wait/kill/reap validator 自己创建的 `Popen`。只保存脱敏 identity/digest/observation/reasonCode 摘要，并绑定 validator/schema/Marshal raw digest/internal-command digest/profile、expected/actual process identity method/digest、Qoder event contract、transport、Capability admission 与 executable digest。

Attestation 是 pre-review operator-local gate，只核对实际 tool/command、`declaredCommands`、TaskSpec 和 final tee 纪律；不是 Worker 自证，不替代 Core 生命周期/持久化/failure/retry/ReviewPacket/Decision/freshness，不写 `.marshal` 或改 Run 状态。任一非零退出或 `status=fail` 都原样保存固定 `reasonCode` 并阻断 reviewer。

失败分类：

- `input-*`、`path-boundary-invalid`、`invalid-json`、`duplicate-json-key`、`manifest-schema-invalid`、`schema-document-invalid`、`policy-invalid`：修 operator input/validator，不消费 Attempt。
- `source-head-mismatch`、`subject-mismatch`、`task-constraint-mismatch`、`task-tool-policy-mismatch`、`transcript-meta-mismatch`：重取权威输入；真实漂移则修 TaskSpec/Adapter并建 successor。
- 其它 tool/command/transcript/tee/terminal/transport/WorkerResult failure：结构性 `protocol-invalid/do-not-retry`，停止 promotion 与新派发，按稳定签名裁决一次，修 Adapter/contract 后建 successor；禁止原 Run retry/rework。

相同输入摘要和 `reasonCode` 不重复运行或派 reviewer。具体 machine fields 以 `transcript-attestation-preflight.md`、相邻 Schema/profile、validator/tests 为准。

## Adapter 特例

- Qoder 若除 final tee 外禁 Bash，所有 deliverable 父目录必须在 locked base 中由 Git tree 预检证明存在；不能让 Worker 用 `ls`/`mkdir` 探索。
- 不给无法机械限制读取的 Provider 写“只读相关小段”。列精确文件和真实工具支持的行/字节上限，或明确允许整文件；无法执行的约束在零 Attempt admission 阻断。
- Pi 大任务建议 `maxOutputBytes >= 16000000`；其 transcript 近似二次增长。Pi 可能写空 `session.id`，调研 TaskSpec 可内嵌 WorkerResult 逐字模板。
- OpenCode 一切读写必须用相对路径，worktree 内绝对路径也会被拒；本 Skill 的 scaffold 对 OpenCode 硬拒绝，除非治理规则另有明确变化。
- 派 fan-out 前勘查仓库既有产出；不要重复生产同类材料。更多历史经验见 `docs/operator-runbook.md` §9.5、§10.4。
