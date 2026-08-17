# ADR 0034：Qoder CLI Live Conformance Authority 与可撤销准入

- 状态：提议（Proposed）——在维护者接受、本 ADR 的负向矩阵通过独立审计且真实 credentialed probe evidence 被外部 authority 签发前，Qoder CLI 不得宣称当前部署 `supported`
- 日期：2026-08-17
- 关联：[ADR 0003](0003-separate-worker-and-publisher.md)、[ADR 0004](0004-independent-verification.md)、[ADR 0018](0018-control-plane-and-provider-ports.md)、公开 Issue #137

## 上下文

Qoder CLI Worker Adapter 已能以版本、realpath 和 executable digest 固定本地进程，但 hermetic fake、`--version` 或 Adapter 自报都不能证明真实 credential、冻结 `stream-json` 协议、权限画像和仓库写入行为可用。生产注册若只读取一个布尔开关，会让 Worker 为自己提供准入证据；若把一次校验永久缓存，又会绕过 evidence 撤销、key rotation 与 host 替换。

本 ADR 冻结的是 **AgentAdapter 本地准入证据**，不是 ADR 0018 的 Sandbox Provider `ConformanceEvidence` authority-ledger 对象。两者不得互换、复用 digest 或被描述为同一 assurance。

## 决策

### 1. 三方职责

1. 独立 verifier 在无业务仓库权限的隔离 scratch worktree 中运行真实 credentialed Qoder probe，采集 typed observation；Qoder Worker、Adapter 与 fake fixture 都不能产生权威通过结论。
2. authority signer 只签发 verifier 已裁决的完整 observation。签名者不运行 Qoder、不读取 credential、不写 Marshal 状态；私钥不进入 Marshal 进程、配置、日志或仓库。
3. Marshal consumer 只持有 Ed25519 public trust root，逐次验证完整 evidence、目标 executable 与当前 host。配置者、Worker 输出、CapabilitySnapshot 和 transcript 自报都不能替代 authority signature。

`SealConformanceEvidence` 只做 canonical validation 与签名封装，禁止替 observation 填入期望 contract。observation 必须逐项携带实际值：runner/version、observedAt/validUntil、adapter/Qoder CLI version、Qoder realpath/digest/binary version、host OS/arch/fingerprint、authority generation、suite/probe artifact/challenge digest、capability/profile/argv/environment/tool-policy digest、event/protocol/permission contract、transcript digest，以及 credential/live-protocol/scratch-worktree-write 三项 verdict。任一缺失、错误或 `false` 都拒绝签发；evidence 中这些字段必须逐项复制 observation，不得由 signer 注入常量或把 verdict 改写为 `true`。

### 2. 精确绑定

- host fingerprint 是 `sha256(JCS({hostname,os,arch}))` 的非敏感目标主机 identity；verifier observation、signed evidence 与 consumer 当前实机必须三方相等。仅 `GOOS/GOARCH` 不构成 host 绑定。
- probe profile 必须冻结真实 argv 模板、完整替换环境、空 setting sources、`accept_edits`、无 session persistence、空 named Worker tool allowlist 与隔离 scratch worktree 写入。`argvDigest` 绑定由运行时同一个 `buildArgs` 模板机械生成的四种真实组合：model 省略/存在 × `--tools` 省略/显式空值；任何未进入该矩阵的 TaskSpec argv 都必须 fail closed。probe 不拥有业务仓库权限不等于不能验证 workspace-write。
- evidence 的最大 validity window 与最大 observation age 均为 24 小时；更长窗口、未来时间、过期或陈旧记录 fail closed。
- authority config 精确绑定一个 current evidence digest、probe artifact digest、challenge digest 与递增 authority generation，并可列出 revoked evidence digests。相同 key 的新 generation 不改写旧 evidence。
- consumer 必须在独立于只读 authority root 的 consumer-owned 私有目录中耐久维护 generation high-water；安全解析配置并验证 trust root/authority root 边界后的新 generation，必须在解析 current evidence leaf 前先单调消费，即使其 evidence 已撤销、缺失或暂不可读也不得回退。记录同时绑定完整 authority config 的 canonical digest；更小 generation，或相同 generation 下替换 evidence/probe artifact/revocation/trust root 等任一 config identity，均 fail closed。
- 耐久 fence 目录必须是绝对 clean path、逐段 `openat(O_NOFOLLOW)` 打开的当前 uid/root-owned `0700` 真实目录，并与 authority signer 的只读 evidence root 分离。consumer 以 owner-only、single-link regular lock file + OS advisory exclusive lock 串行化跨线程/跨进程消费；记录通过同目录随机 `O_CREAT|O_EXCL|O_NOFOLLOW` 临时文件、file `fsync`、`renameat`、directory `fsync` 原子提交。重启只读取已提交的封闭版本记录；损坏、超限、未知字段、异常类型/权限、锁争用都 fail closed，残留临时文件不构成 authority。

### 3. 文件与路径边界

`MARSHAL_QODER_CONFORMANCE_CONFIG`、authority root 与 evidence leaf 必须是绝对 clean path。consumer 从 `/` 开始逐段使用 dirfd + `openat(O_NOFOLLOW)`，不跟随任何父级或 leaf symlink；leaf 用 `O_NONBLOCK` 打开并在读取前 `fstat`，FIFO/device/socket 均不得阻塞或伪装。config/evidence 必须是当前 uid 或 root 拥有的私有 regular file，authority root 必须是当前 uid 或 root 拥有且权限 `0700` 的真实目录。JSON 拒绝未知字段并执行严格字节上限；digest 命名 leaf 只通过已打开的 authority root dirfd 解析。

generation fence root 是 Marshal consumer 的本地耐久状态边界，不得由 evidence signer 写入、不得放在 evidence root 内，也不得携带 credential、private key、config path、evidence 正文或 transcript。ADR 为 Proposed 期间该 root 仅由同包 hermetic candidate 测试显式注入；未来生产启用必须由 Marshal 应用状态配置提供，不得从 authority config 自行选择可写路径。

### 4. 撤销、恢复与可观察性

ADR 0034 为 Proposed 时，生产 `NewFromAuthorityConfig` 必须无条件返回 typed permanent conformance-pending，应用不得注册 Qoder；候选 consumer 只能由同包 hermetic test 驱动。ADR 接受本身也不自动启用，必须由独立后续变更在矩阵与真实 evidence 均通过后显式移除硬禁用。

未来启用的生产 Adapter 不把一次成功变成永久内存授权。每次 `Probe` 和每个 Worker launch guard 都重新安全读取 current config、trust roots、generation、revocation set 与 content-addressed evidence，并重新验证签名、freshness、profile、host 和 executable identity。删除、撤销、权限变化、key rotation、generation rollback/identity 漂移立即使当前操作 fail closed；长寿命 `marshal-server` 无重启豁免。

当且仅当上述逐项复核通过时，CapabilitySnapshot/doctor 才可报告 `supported`，并公开非敏感的 evidence digest、trust-root key id、profile digest、validUntil、host fingerprint 与 authority generation，便于 ReviewPacket 和运维审计精确绑定。不得输出 config 路径、credential、private key 或 transcript 正文。

### 5. 验证与启用门禁

负向 fixture 至少覆盖：生产硬禁用、缺失配置/evidence、未知字段、过期/未来/超长 TTL、完整 observation 缺失与逐字段替换、错误 executable realpath/digest/version、同 OS/arch 不同 host、错误 suite/artifact/challenge/profile/argv/env/tool policy/event/protocol/permission、任一 verdict 为 false、未知/轮换 key、generation rollback/同代替换、已撤销 digest、config/root/evidence symlink（含父级）、FIFO、错 owner/权限、运行中撤销后 Probe 与完整 `Run` launch 拒绝，以及 supported-Qoder Schema/doctor metadata 必填。generation fence 另须覆盖进程重启与独立进程 rollback、同代 identity 冲突、并发消费、临时文件残留、损坏/超限/未知字段记录、root/lock/record symlink/FIFO/错 owner/权限以及 commit 后重开恢复。

CI 中生成的 key、fake executable 和 synthetic transcript 只能验证 canonical/signature/fail-closed 机制，必须明确标记 hermetic fixture；它不构成真实 live evidence，不改变当前部署状态。只有独立外部 probe 的真实 evidence 被运维配置并在当前 host 通过 doctor 后，该 host 的 Qoder 才可参与调度。

## 后果

- Qoder 可以在不向 Marshal 暴露 credential 的前提下获得可审计、短期且可撤销的本地准入。
- 每次 Probe/launch 会增加少量私有文件读取和签名验证成本；安全撤销优先于缓存命中。
- 本 ADR 为 Proposed 时实现只能作为候选机制，不能据此关闭 Issue #137 或宣称 Qoder 已完成 live conformance。
