# ADR 0037：Codex CLI Production Authority、宿主绑定证据与可撤销准入

- 状态：提议（Proposed）——本 ADR 尚未被维护者接受；在本 ADR 被接受、独立负向矩阵通过、当前宿主取得真实 credentialed live evidence 且后续启用变更通过门禁前，Codex CLI 必须保持 production hard-disable
- 日期：2026-08-18
- 关联：[ADR 0003](0003-separate-worker-and-publisher.md)、[ADR 0004](0004-independent-verification.md)、[ADR 0006](0006-attempt-control-root.md)、[ADR 0014](0014-read-only-execution-profile.md)、[ADR 0018](0018-control-plane-and-provider-ports.md)、[ADR 0034](0034-qoder-cli-live-conformance-authority.md)、公开 Issue #136

## 上下文

Codex CLI 的本地 Adapter 可以验证版本、解析 `exec --json` 事件并限制 argv，但这些能力本身不能形成 production authority。fake executable、单次 `--version`、Adapter 自报、作者测试或某次成功运行都不能证明当前宿主、当前二进制、当前 credential、当前权限画像及当前协议仍满足 Marshal 的准入要求。

Codex 还存在平台特有的执行身份问题。若 Adapter 先按路径计算摘要，随后再按同一路径启动，攻击者或并发安装器可以在两步之间替换文件；只比较 realpath 不能消除该 TOCTOU。任务 `worktree` 与 ADR 0006 的 `controlRoot` 若只做字符串前缀判断，也可能通过符号链接、路径别名、rename 或双向嵌套破坏写域分离。普通宿主子进程仍不是恶意代码 sandbox，这一限制不能被 Adapter 的本地路径检查重新包装为强隔离承诺。

本 ADR 冻结 Codex CLI AgentAdapter 的本地 production 准入 authority、逐次消费规则、宿主路径身份和可观察性。它不是 ADR 0018 的 Sandbox Provider `ConformanceEvidence`，两者不得互换、复用 digest 或合并 assurance 声明。ADR 0034 的 Qoder 证据也不能为 Codex 提供授权。

## 决策

### 1. 默认关闭与启用条件

Codex production Adapter 采用 deny-by-default：

1. 本 ADR 为 `Proposed` 时，production constructor 必须稳定返回 typed `codex_conformance_pending`；应用 registry、doctor 与调度器不得把 Codex 列为 `supported` 或 eligible。
2. 接受本 ADR 不自动启用 Codex。必须另有独立实施变更完成本文全部机制、负向测试与跨平台门禁，并由非作者 reviewer 审查。
3. 即使实现已合入，当前宿主没有有效的 credentialed live evidence 时仍为 hard-disabled；fake、synthetic、作者自测或其他宿主 evidence 不能降级替代。
4. 最终启用必须是显式 registry 变更，并同时要求当前 `Probe` 成功。不得用环境变量、隐藏 fallback、`force` 参数或测试 hook 绕过 authority。
5. 任一逐次复核失败立即取消该次 `Probe` 或 launch 的 eligibility，不保留“上次成功”授权，也不静默回退到路径执行、未认证执行或其他 Adapter。

因此，本 ADR 与任何候选代码都不能被表述为“Codex 已 production registered”“Codex 已 supported”或“live conformance 已完成”。

### 2. Authority 角色与独立性

准入链分为四个互不替代的角色：

1. **probe-only verifier** 在没有业务仓库权限的隔离环境中运行真实 credentialed Codex CLI，产生封闭的 typed observation。它不能签发 production evidence，不能修改 Marshal 状态，也不能向输出复制 credential、完整 transcript 或环境变量。
2. **receipt authority** 观察每个真实 probe variant 的实际执行，并对 held executable identity、实际 argv/环境、scratch 写入、事件与 challenge 结果签发执行 receipt。verifier 只持 receipt public key。
3. **evidence authority signer** 按固定 policy 重新验证 verifier attestation、全部 execution receipt、candidate identity 与 observation，之后才签发 content-addressed production evidence。它不运行 Codex、不读取 credential、不接受调用者提供的新 trust anchor。
4. **Marshal consumer** 只持只读 public trust material，逐次验证 config、keyset、revocation、evidence 与当前本机 identity。Worker、Adapter、配置写入者、CapabilitySnapshot 和 doctor 都不能为自己生成通过结论。

probe-only verifier、receipt authority、evidence authority signer 与 Codex Worker 必须使用不同的 authority key。私钥不得进入 Marshal 进程、仓库、TaskSpec、Prompt、事件、日志、ReviewPacket 或 evidence。hermetic fixture key 必须带 test-only identity，production consumer 必须拒绝。

### 3. Canonical evidence 与宿主精确绑定

所有签名对象与 digest 使用 RFC 8785 JCS canonical JSON；解析时拒绝重复 member、未知字段、异常类型、超限输入与尾随内容。生产 evidence 至少精确绑定：

- `schemaVersion`、`evidenceDigest`、`suiteId`、`suiteDigest`、`challengeDigest`；
- verifier、receipt authority、evidence signer 的 key id 与 attestation digest；
- `observedAt`、`validFrom`、`validUntil`，最大 observation age 与最大 validity window 均为 24 小时；
- Codex executable 的 canonical realpath、device、inode、size、mode、SHA-256、稳定三段 semver 与版本输出 digest；
- 当前宿主的 OS、arch 与非敏感 `hostFingerprint`；
- authority generation、trust-root generation 与签发时 revocation-set digest；
- Adapter contract、argv matrix、完整替换 environment、event protocol、permission profile、tool policy、result contract 与 output-limit digest；
- 隔离 scratch root、credential root、业务仓库 deny roots 及其执行时 topology digest；
- credentialed invocation、JSONL event contract、权限画像、scratch-only 写入和无业务仓库访问的逐项 verdict；
- 每个 probe variant 的 execution receipt digest、transcript digest、marker digest 与随机 challenge digest。

`hostFingerprint` 必须由固定 host-attestation policy 对稳定机器 identity、OS 与 arch 生成，只公开摘要，不公开原始 machine id。仅 `GOOS/GOARCH`、hostname、用户名、路径或可由调用者自由提供的字符串都不足以构成宿主绑定。evidence 的 host fingerprint、authority config 声明与 consumer 对当前实机的重新计算必须三方相等。

版本兼容线只说明二进制可进入 conformance 验证，不构成授权。允许兼容 patch 更新时，evidence 仍必须绑定该次实际 held executable digest 与精确版本；major、minor、pre-release、build metadata、无法严格解析的版本及 evidence 未覆盖的 patch 全部 fail closed。

### 4. 冻结的真实 probe 契约

真实 probe 必须从生产 Adapter 的同一 invocation builder 机械派生 argv matrix，不得由 verifier 手写一套“预期命令”。矩阵至少覆盖：默认/显式 model、允许的输入传递形式、JSONL 事件流、成功/模型错误/协议错误/取消/超时、输出上限，以及 `workspace-write` 与 `read-only` 两种声明画像中实际支持的组合。未进入冻结矩阵的 TaskSpec 组合不具备 eligibility。

probe 环境是完整替换环境，只包含固定 allowlist；不得继承调用者 PATH、代理、Publisher credential、Marshal authority 私钥或业务仓库变量。Codex credential 只由隔离环境的 Secret Provider 交付给该次 probe，不进入 observation。网络、审批、sandbox/permission、session persistence、tool policy 与 cwd 均须显式冻结；不允许依赖用户级隐式配置、交互确认或上一次 session。

隔离环境的唯一写域是 verifier-owned scratch。所有业务仓库 root 必须作为 deny root 传入隔离执行器，并以 held identity 证明没有同目录、别名或任一方向嵌套。probe 必须用随机 challenge 证明真实 Codex 事件与 scratch marker 属于本轮执行；预制 transcript、跨 variant receipt replay、synthetic success 或 `hermetic-fixture` receipt 都不能生成 production evidence。

### 5. Trust root、key rotation 与撤销

consumer 由部署者在只读边界外固定一个 bootstrap public trust root。authority root 中的 canonical keyset manifest 由该 root 授权，至少包含递增 `trustRootGeneration`、active evidence key、not-before/not-after、revoked key ids 与 manifest digest。普通 authority config 不能添加 trust root，也不能把调用者传入的 keypair 变成新 anchor。

key rotation 必须满足以下规则：

- evidence signer 轮换通过更高 generation 的 keyset manifest 激活；旧 key 可在明确 overlap 窗口内验证既有、未撤销且仍新鲜的 evidence，但不能签发 manifest 声明生效时间之后的新 evidence；
- root 轮换必须由当前 root 与新 root 的显式双重授权，或由独立 out-of-band 部署替换 bootstrap root；单个新 key 自签不能建立信任；
- `trustRootGeneration` 与 authority generation 都由 consumer 耐久 high-water fence 单调消费；rotation、撤销或 keyset identity 变化不能通过重启回退；
- revoked key、revoked evidence digest 与 revoked suite/challenge digest 均立即失效。撤销优先于 freshness 与缓存命中。

authority config 是封闭、签名、带 generation 的 current 指针，至少绑定：`authorityGeneration`、`trustRootGeneration`、keyset digest、current evidence digest、suite/probe artifact/challenge/profile digest 与完整 revocation-set digest。相同 generation 下任一字段改变属于 identity conflict；更小 generation 属于 rollback。更高 generation 必须先写入 consumer fence，再解析其 current evidence；即使该 evidence 缺失、损坏、已撤销或暂不可读，也不得回退到旧 generation。

### 6. Consumer-owned rollback fence

rollback fence 位于 Marshal consumer 独占的耐久私有目录，与只读 authority/evidence root 是独立故障域。signer、verifier 与 Worker 均无写权限。fence 记录至少绑定 authority namespace、Adapter id、host fingerprint、trust-root generation、authority generation、keyset/config/revocation/current evidence digest。

consumer 必须：

- 从 `/` 开始逐段 `openat(O_NOFOLLOW)` 打开 authority root 与 fence root，要求 absolute clean path、真实目录、当前 uid/root owner 与精确 `0700`；
- 持续持有两侧 dirfd，以 device/inode 和双向 ancestry 遍历拒绝同目录、路径 alias、fence-under-authority 与 authority-under-fence；
- 用当前 uid/root-owned、single-link、精确 `0600` regular lock file 加 OS advisory exclusive lock，串行化跨 goroutine/跨进程消费；
- 以同目录随机 `O_CREAT|O_EXCL|O_NOFOLLOW` 临时文件写入封闭记录，执行 file `fsync`、`renameat`、directory `fsync` 后才视为 committed；
- 重启只读取 committed 记录。损坏、超限、未知字段、错 owner/mode、hardlink、symlink、FIFO/device/socket、锁异常或同代 identity 冲突全部 fail closed；残留临时文件不构成 authority。

每次 `Probe` 及每次 Worker launch guard 都必须重新从 held authority dirfd 读取 current config/keyset/revocation/evidence，验证 signature、generation fence、freshness、host、binary 与完整 contract。长寿命 `marshal-server`、进程内 cache、之前的 CapabilitySnapshot 或之前成功的 Run 均无豁免。launch guard 必须在实际 spawn 紧邻点完成，失败后不得创建子进程。

### 7. Worktree、controlRoot 与路径身份

执行请求中的 `worktree` 与 ADR 0006 `controlRoot` 都必须是 absolute clean path。consumer 从 `/` 逐段 `openat(O_NOFOLLOW)`，拒绝任一父级或 leaf symlink，并在整个 launch 与进程建立阶段持续持有 dirfd。目录 identity 至少包含 device、inode、mount identity、owner 与 mode；字符串 realpath 只作为审计字段，不是 authority。

consumer 必须对 held `worktree`、held `controlRoot` 及 `control/input`、`control/output` 执行双向 ancestry 检查：

- 任意两者同目录、路径 alias 或非契约允许的父子关系均拒绝；
- `worktree` 不得位于 `controlRoot` 下，`controlRoot` 也不得位于 `worktree` 下；
- `control/input` 必须保持只读，`control/output` 只能属于当前 Attempt；
- pre-launch、child setup 完成后与 launch receipt 接纳前重新遍历 held ancestry；任一点 topology digest 改变均拒绝；
- child cwd、控制输入与结果落点必须从 held dirfd 派生，不得在校验后重开 request path。

rename、swap、bind mount、hardlink、symlink、`..`、重复 separator、大小写/Unicode alias、父级替换、worktree/controlRoot 双向嵌套及检查后 swap-back 都必须进入负向矩阵。路径门禁是合作式本地边界的一部分，不得描述为恶意代码隔离。

### 8. Linux authenticated fd-exec 与 Darwin fail-closed

Linux 是首个可候选 production 平台。consumer 必须在校验时打开 Codex executable，要求 owner/mode/type/link count 符合 policy，并把精确 bytes 复制到 sealed executable memfd；seal 至少禁止 write、grow、shrink 与再次改变 seal。摘要、版本 probe 与 Worker launch 必须针对同一认证 bytes：版本与执行均从 held/sealed fd 派生，不能在摘要后重新按 pathname 执行。

Linux launcher 必须验证 procfs/fd 语义可用、held fd 未被替换、memfd seals 完整、child 实际 executable identity 与 launch receipt 一致。`/proc` 不可用、fd-exec 不可证明、seal 缺失、解释器/shebang 导致身份退化或 kernel 行为不满足 policy 时返回 typed permanent failure，不得 fallback 到 pathname。

Darwin 当前没有被本 ADR 接受的等价 authenticated fd-exec launcher，因此 production 必须显式返回 `codex_platform_unsupported`。即使 version probe、fake tests 或普通 pathname execution 成功，也不能标记 `supported`。未来 Darwin 启用必须由新 ADR 或替代 ADR 冻结 codesign/notarization、Mach-O identity、可信 launcher 与 TOCTOU 证明，并走独立门禁。

### 9. Protocol、结果与启动后的撤销边界

launch 成功只说明该次 spawn 通过准入，不把 evidence 变成 Worker 自证。Adapter 仍必须：

- 只接受冻结 JSONL event schema，拒绝未知关键事件、重复 terminal result、terminal 后事件、缺失 terminal、非法 UTF-8、超限行/总输出与 stdout 非协议噪声；
- 将 stderr 视为有界诊断而非可信 evidence，并执行 credential/secret redaction；
- 区分模型/协议/权限/取消/超时/输出上限/进程退出，不用一个 generic error 吞掉原因；
- 只把 Candidate 写入当前 held worktree，WorkerResult 写入当前 held `control/output`；Worker 不能写 Verification、ReviewDecision 或 Publication；
- 在 child setup 后、正式 workload 前完成最后 launch guard。若撤销发生在该点之前则不得运行；发生在运行中时，Supervisor 记录 typed revocation observation，撤销 eligibility、fence 当前 launch 并按现有生命周期执行 cancel/Inspect/Reconcile，不把晚到输出接纳为当前 evidence。

### 10. Typed failure 契约

production 边界不得向 Core 暴露未分类的 `os.PathError`、字符串拼接错误或模糊 `unsupported`。每个失败必须返回稳定 code、`retryClass=permanent|transient|reconcile-required`、安全的非敏感 detail 与 cause chain；至少覆盖：

| 类别 | 稳定 code |
| --- | --- |
| 治理/注册 | `codex_conformance_pending`、`codex_not_registered` |
| 平台/launcher | `codex_platform_unsupported`、`codex_fd_exec_unavailable`、`codex_executable_unsafe`、`codex_launch_identity_mismatch` |
| authority 配置 | `codex_authority_config_missing`、`codex_authority_config_invalid`、`codex_authority_signature_invalid`、`codex_trust_root_invalid`、`codex_key_revoked` |
| generation/fence | `codex_authority_rollback`、`codex_authority_identity_conflict`、`codex_fence_invalid`、`codex_fence_lock_failed`、`codex_fence_commit_failed` |
| evidence | `codex_evidence_missing`、`codex_evidence_invalid`、`codex_evidence_expired`、`codex_evidence_not_yet_valid`、`codex_evidence_revoked`、`codex_evidence_host_mismatch`、`codex_evidence_binary_mismatch`、`codex_evidence_contract_mismatch` |
| 路径 | `codex_path_invalid`、`codex_path_unsafe_type`、`codex_path_identity_changed`、`codex_path_topology_conflict`、`codex_path_permission_invalid` |
| 协议/运行 | `codex_protocol_invalid`、`codex_permission_denied`、`codex_output_limit_exceeded`、`codex_timeout`、`codex_canceled`、`codex_process_failed`、`codex_reconcile_required` |

未知内部错误在 Adapter 边界转换为 `codex_internal_fail_closed`，同时保留内部审计 cause，但不得把路径、credential、环境或 transcript 正文泄露给用户输出。只有 policy 明确列出的 transient I/O/lock contention 可重试；signature、rollback、revocation、identity、path topology、protocol 与平台错误不得通过重试放宽。

### 11. CapabilitySnapshot 与 doctor

只有本次 `Probe` 完整通过上述逐项复核时，CapabilitySnapshot 才能报告 `supported=true`。Snapshot 与 doctor 必须精确公开以下非敏感 metadata，并纳入 snapshot digest：

- Adapter id/version、Codex exact version、executable digest 与安全的 binary identity digest；
- host fingerprint、platform、authenticated launcher kind；
- evidence digest、suite/profile/argv/environment/event/protocol/permission/result contract digest；
- evidence signer key id、keyset digest、trust-root generation、authority generation、revocation-set digest；
- `observedAt`、`validUntil`、freshness 状态与 last-consumed fence digest；
- 支持的 execution profile、native budgets 与明确的非 sandbox 限制。

任一字段缺失或不匹配时不得输出部分 `supported`。doctor 的失败视图输出 stable code、generation、公开 digest 与到期时间即可，不得输出 config/evidence/fence 的绝对路径、machine id、credential、private key、完整 argv 中的敏感值、环境、stderr 或 transcript 正文。ReviewPacket 若依赖 Codex eligibility，必须绑定该次完整 CapabilitySnapshot digest，而不是复制 `supported` 布尔值。

### 12. Crash、重启、重放与负向验证矩阵

独立验证至少覆盖：

1. **默认关闭**：Proposed 状态、无 live evidence、仅 fake evidence、仅作者 evidence、未显式 registry enable 均 hard-disable。
2. **签名与内容**：未知/错误/轮换/revoked key，任一 evidence 字段缺失或替换，重复/未知 JSON member，digest、receipt、challenge、suite/profile/contract 不匹配。
3. **时间与宿主**：未来、过期、超长 TTL、陈旧 observation，同 OS/arch 不同 host，重启后 host/fingerprint 漂移。
4. **generation 与 fence**：更小 generation、同代 identity conflict、更高代但 evidence 缺失/撤销、进程崩溃于 file fsync/rename/directory fsync 各点、残留临时文件、损坏/超限记录、跨进程并发消费与锁争用。
5. **rotation/revocation**：active key rotation、root 双授权 rotation、旧 key overlap 边界、运行前撤销、`Probe` 后 launch 前撤销、运行中撤销、server 不重启重新验证。
6. **路径**：config/keyset/evidence/fence/worktree/controlRoot 及其任一父级 symlink，FIFO/device/socket、hardlink、错 owner/mode、路径 alias、同目录、双向嵌套、rename/swap、mount/topology 变化与 swap-back。
7. **Linux launcher**：path 在 digest 后替换、held fd 与 memfd 不同、seal 缺失、procfs 不可信、fd 关闭/复用、child identity 不符、shebang/fallback；所有失败都不得 pathname fallback。
8. **Darwin**：真实二进制与 fake 均稳定 `codex_platform_unsupported`，不得出现 test hook 泄露到 production constructor。
9. **协议与结果**：argv matrix 每个 variant、未知/重复/乱序/缺失 terminal、超限、取消、超时、权限拒绝、secret redaction、非零退出及 typed mapping。
10. **恢复与重放**：consumer/Marshal kill -9 后读取 committed fence；相同 config/evidence 重放幂等；旧 config、旧 evidence、旧 Snapshot 与旧 launch request 均拒绝；撤销后的晚到 Worker output 隔离且不进入当前 Evidence。

hermetic 测试只证明 canonical、signature、路径与 fail-closed 机制。真实 live gate 必须由独立环境在当前生产候选版本和当前 host 上运行，并保存非敏感 evidence metadata；作者不能对自己的实现提供权威通过结论。

## 实施顺序与接受门禁

实施必须按以下顺序推进，且每步保持 production hard-disable：

1. 先实现封闭 Schema、typed errors、canonical/signature 与 negative fixtures；
2. 再实现 held-dirfd 路径 identity、双向 ancestry、consumer fence 与 crash/replay 测试；
3. 再实现 Linux authenticated fd-exec 和 Darwin 显式 unsupported；
4. 再实现独立 verifier/receipt/evidence authority 工具链及 credentialed live probe；
5. 再接入 CapabilitySnapshot/doctor exact metadata；
6. 独立 reviewer 对真实 diff、race、跨平台、secret scan 与本文负向矩阵给出 P0/P1 清零结论；
7. 当前 host 配置真实、未撤销且新鲜的 signed evidence 后，最后以单独 registry 变更显式启用。

任何前置切片合入都不表示本 ADR 被接受、不表示 live evidence 已存在，也不表示 Issue #136 或相关 milestone 完成。

## 后果

- Codex 的 production eligibility 成为短期、宿主绑定、可撤销且可回放审计的事实，而不是 Adapter 自报或一次成功缓存。
- Linux 通过 authenticated fd-exec 消除“校验一个文件、执行另一个文件”的路径竞态；Darwin 在证明等价机制前保持 fail closed。
- worktree/controlRoot 的 held identity 与双向 ancestry 使 ADR 0006 写域分离能够抵抗路径别名和 rename/swap，但仍不把普通宿主进程描述成恶意代码 sandbox。
- 每次 `Probe` 与 launch 增加私有文件读取、签名验证、fence 锁与摘要成本；撤销和 rollback 安全优先于缓存性能。
- typed failure 与 exact doctor metadata 允许 Supervisor、ReviewPacket 和运维区分永久配置错误、可重试 I/O 与需要 reconcile 的运行中撤销，而不泄露 secret。
