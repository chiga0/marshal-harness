# ADR 0039：APAP peer exec 单调屏障

- 状态：提议（Proposed，2026-08-18；未接受前不得实现或启用）
- 日期：2026-08-18
- 关联：[ADR 0038](0038-agent-production-authority-provider.md)、[ADR 0034](0034-qoder-cli-live-conformance-authority.md)、[ADR 0037](0037-codex-cli-production-authority.md)、Issue #136、Issue #137

## 上下文

ADR 0038 §2 要求 Linux APAP control transport 用 `SO_PEERCRED`、held `/proc/<pid>/exe` 与 pidfd 绑定 peer。实现审计证明，这只能固定 PID 生命周期和准入时打开的 executable 对象，不能证明同一 PID 在连接存续期没有执行过其他映像。恶意 peer 可以在一次 `/proc/<pid>/exe` 检查后 `exec` 到未授权映像，发送 packet，再 `exec` 回允许映像；在收包前后各检查一次仍存在 swap-back TOCTOU。

该缺口位于 APAP transport 信任边界。普通轮询、旧 executable fd、pathname 重开、短时间窗或测试 helper 自报都不能关闭。

## 决策

### 1. 生产连接必须由受信 launcher 建立永久 exec-deny

Linux v1 的每个 APAP control peer 必须由独立、root-owned trusted launcher 启动。launcher 从 held executable fd 启动精确映像，并在允许 peer 连接 control socket 前完成以下原子前置：

1. 设置 `PR_SET_NO_NEW_PRIVS=1`；
2. 安装不可移除、不可放宽的 seccomp filter；filter 对 native ABI 的 `execve` 与 `execveat` 永久返回 `EPERM`，遇到错误 architecture 固定 kill；
3. filter 安装后再次核对 child 的 pidfd、PID birth identity、实际 executable identity、`NoNewPrivs=1` 与 `Seccomp=2`；
4. 使用仅属于 launcher principal、usage 固定为 `apap-peer-launch` 的 Ed25519 key 签发 closed `APAPPeerLaunchReceiptV1`；
5. 只有 receipt 已签发且 child 仍为同一 pidfd/birth identity 时才释放连接动作。

seccomp filter 只能叠加，不能被 peer 移除；`no_new_privs` 防止 peer 通过 exec 获得新的 privilege。filter 还必须拒绝会建立未受审新执行域的 `ptrace`、`process_vm_writev`、`bpf`、`userfaultfd`、`kexec_load`、`finit_module`、`init_module` 与 namespace 创建操作。该列表是 APAP control peer 的最小 profile，不把普通宿主进程描述成恶意代码 sandbox。

### 2. receipt 与 transport bootstrap 精确绑定

`APAPPeerLaunchReceiptV1` 是 closed object，字段精确为：

- `schemaVersion="marshal.apap-peer-launch-receipt.v1"`；
- `providerInstanceId`、`principalDigest`、`role`；
- `launcherKeyId`、`launcherBuildDigest`、`filterDigest`；
- `pid`、`startTimeTicks`、`pidfdInode`；
- `executableIdentityDigest`；
- `noNewPrivs=true`、`seccompMode=2`；
- `connectionNonce`、`issuedAt`、`expiresAt`；
- ADR 0038 `SignedObjectEnvelopeV1`，domain 固定为 `marshal-apap-peer-launch-receipt-v1\0`，key usage 固定为 `apap-peer-launch`。

receipt 的 TTL 最多 5 秒，只能消费一次；`connectionNonce` 由连接的验证方生成，不能由 peer 或 launcher选择。transport 在向 application 暴露首个 APAP request 或 response 前完成 bootstrap：核对 `SO_PEERCRED`、held pidfd、可信 procfs 的 start-time、held/current executable identity、receipt 全部字段、current keyset/revocation/generation、签名、nonce 与时间。任何不匹配立即关闭 socket，不发送 APAP application response，也不调用任何 `ProbeAuthority`、bundle、launch 或 signer operation。

双方都必须验证对端：listener 验证 consumer/verifier/authority peer receipt；dialer 验证 APAP service receipt。不能把 server 签名的普通 APAP response 当作 server launch receipt，也不能把 receipt 配置成可选项。launcher key 与 APAP response key、evidence/config/receipt/rotation/revocation/recovery key互斥。

### 3. 单调性与失败语义

一旦 receipt 对应的 filter 安装，任何 `execve`/`execveat` 在内核执行映像替换前返回 `EPERM`。因此不存在“exec-away 后运行新映像并发送，再 exec-back”的可观察窗口。连接存续期仍逐包复核 `SO_PEERCRED`、pidfd、birth identity 和 current executable；这些检查用于检测退出、PID/identity 异常，不能替代 seccomp barrier。

缺少 trusted launcher、receipt、current key、可信 procfs、seccomp/no-new-privs 证明，或平台不支持时，production transport 返回 typed permanent peer rejection。禁止 fallback 到 `/proc` sandwich、pathname、同 UID helper、自签 receipt 或无 barrier transport。Darwin 保持 `unsupported`，直到替代 ADR 冻结等价的 kernel/code-signing barrier。

### 4. 负向矩阵

独立测试至少覆盖：

1. peer 在 admission 后依次尝试 `exec-away → send → exec-back`：第一次 exec 必须在内核返回 `EPERM`；APAP application handler 调用计数为零，且无 response packet；
2. `execveat`、错误 architecture、filter 缺失/替换、`NoNewPrivs=0`、receipt 自签/错 key/错 usage/错 domain、过期/未来/replay nonce、错 PID/birth/pidfd/executable/principal/provider 全部 fail closed；
3. launcher 在 filter 前签名、receipt 后 child swap、launcher crash、peer exit、socket retained fd、fork/clone 后子进程尝试 exec 全部不能产生获准 request；
4. Qoder 与 Codex 共用 transport barrier 测试，但继续分别满足 ADR 0034/0037 的 profile-specific evidence 与 launch barrier，不能因本 ADR 自动变为 `supported`。

## 后果

- APAP control peer code identity 从“采样时相等”提升为“launcher 证明初始映像，内核永久禁止后续映像替换”。
- 新增 root launcher、专用 signing key、双向 bootstrap 与 seccomp profile 的部署成本。
- 本 ADR 只关闭 control transport peer identity 的 exec swap-back 缺口，不实现 APAP 业务 operation、真实 probe 或 registry enablement。

## 实施门禁

1. 先由独立 reviewer 审查并由维护者接受本 ADR；
2. 再实现 closed receipt Schema/canonical/signature、trusted launcher、双向 bootstrap 与 typed failures；
3. Linux 真实 kernel 负向 fixture 必须证明 `ProbeAuthority=0` 且无 response；Darwin 编译与运行均稳定 fail closed；
4. 全量 CI、race、secret scan、独立实现审查 P0/P1 清零后，仍只能作为 APAP service seam；真实 host provision 与 profile conformance 完成前不得宣称 Qoder/Codex `supported`。
