# ADR 0039：APAP peer exec 单调屏障

- 状态：提议（Proposed，2026-08-18；未接受前不得实现或启用）
- 日期：2026-08-18
- 关联：[ADR 0038](0038-agent-production-authority-provider.md)、[ADR 0034](0034-qoder-cli-live-conformance-authority.md)、[ADR 0037](0037-codex-cli-production-authority.md)、Issue #136、Issue #137

## 上下文

ADR 0038 §2 要求 Linux APAP control transport 用 `SO_PEERCRED`、held `/proc/<pid>/exe` 与 pidfd 绑定 peer。实现审计证明，这只能固定 PID 生命周期和采样时 executable 对象，不能证明同一 PID 在连接存续期没有执行过其他映像。peer 可以在检查后 `exec-away`，发送 packet，再 `exec-back`；收包前后各重开 `/proc/<pid>/exe` 仍是可 swap-back 的 TOCTOU。

该缺口位于 APAP transport 信任边界。普通轮询、旧 executable fd、pathname 重开、短时间窗、同 UID helper 或 self-signed receipt 都不能关闭。

## 决策

### 1. 固定两个不同对象：launch attestation 与 connection receipt

Linux v1 的 APAP control peer 必须由 root-owned、独立 OS principal 的 trusted launcher 启动。启动证明和逐连接证明不能合并：

`APAPPeerLaunchAttestationV1` 是一次进程启动只签一次的 closed object，字段精确为：

- `schemaVersion="marshal.apap-peer-launch-attestation.v1"`；
- `providerInstanceId`、`producerPrincipalDigest`、`peerPrincipalDigest`、`role`；
- `launcherBuildDigest`、`filterDigest`、`authorityGeneration`；
- `pid`、`startTimeTicks`、`pidfdInode`、`executableIdentityDigest`；
- `mainProcessIdentityDigest`、`privateControlChannelIdentityDigest`；
- `noNewPrivs=true`、`seccompMode=2`、`notifierIdentityDigest`；
- `startedAt`、`attestedAt`；
- ADR 0038 `SignedObjectEnvelopeV1`。

其 signature domain 固定为 `marshal-apap-peer-launch-attestation-v1\0`，key usage 固定为 `apap-peer-launch-attestation`。`producerPrincipalDigest` 必须等于 current policy 中唯一的 trusted launcher principal；`SignedObjectEnvelopeV1.keyEpoch` 与 `authorityGeneration` 必须等于 current keyset/anchor。top-level 不重复 `keyId` 或 `keyEpoch`：二者只存在于 `SignedObjectEnvelopeV1`。

`APAPPeerConnectionReceiptV1` 是每个连接在线签发的另一个 closed object，字段精确为：

- `schemaVersion="marshal.apap-peer-connection-receipt.v1"`；
- `providerInstanceId`、`producerPrincipalDigest`、`peerPrincipalDigest`、`role`；
- `authorityGeneration`、`launchAttestationDigest`；
- `pid`、`startTimeTicks`、`pidfdInode`、`executableIdentityDigest`；
- `mainProcessIdentityDigest`、`privateControlChannelIdentityDigest`、`connectionIdentityDigest`；
- `challengeId`、`connectionNonce`、`issuedAt`、`expiresAt`；
- ADR 0038 `SignedObjectEnvelopeV1`。

其 signature domain 固定为 `marshal-apap-peer-connection-receipt-v1\0`，key usage 固定为 `apap-peer-connection-receipt`。receipt 必须由仍持有该 peer 的 pidfd、seccomp notifier 与 control helper supervision state 的 launcher 在线签发，不能由 peer、APAP application、Adapter 或离线 fixture签发。TTL 最多 5 秒。

### 2. USER_NOTIF 初始 exec 与永久 deny 状态机

trusted launcher 以单线程 bootstrap child 建立以下固定 Linux 状态机：

1. `created`：launcher 持有 source executable fd、child pidfd 与私有 bootstrap channel；child 尚未运行不可信映像；
2. `filter-installed`：child 在创建第二线程及执行任何不可信用户指令前设置 `PR_SET_NO_NEW_PRIVS=1`，以 `SECCOMP_FILTER_FLAG_NEW_LISTENER|SECCOMP_FILTER_FLAG_TSYNC` 安装 BPF filter，并把唯一 notifier fd 交给 launcher；交付后 child 不持 notifier；TSYNC 失败即 kill+wait；
3. `initial-exec-pending`：filter 对 native ABI 的 `execve/execveat` 发送 USER_NOTIF；对 x32 bit、非 native audit architecture 或未知 syscall encoding 固定 `KILL_PROCESS`；对 `ptrace`、`process_vm_writev`、`bpf`、`userfaultfd`、`kexec_load`、`finit_module`、`init_module`、`unshare`、`setns` 固定拒绝；legacy `clone` 只允许不含 user/pid/mount namespace bit 的线程创建，`clone3` 因 BPF 不能安全解引用参数结构而固定拒绝；
4. `initial-exec-authorized`：只允许一次 `execveat(sourceFD,"",argv,env,AT_EMPTY_PATH)`。launcher 先用 `SECCOMP_IOCTL_NOTIF_ID_VALID` 验证 notification 存活，再用 pidfd 保证 birth identity，并以 `pidfd_getfd` 复制 child 的 sourceFD；复制对象的完整 held identity/digest 必须等于 policy 与 launch request。bootstrap 保证此时只有一个线程、argv/env memory 由 launcher 创建后不可由其他线程改写、sourceFD 不可关闭/复用；任一条件不可证明即 kill+wait。只有这一次 notification 可返回 `SECCOMP_USER_NOTIF_FLAG_CONTINUE`；
5. `exec-sealed`：launcher 观察初始 exec 完成，重新验证 pidfd/birth、`/proc/<pid>/exe` held identity、`NoNewPrivs=1`、`Seccomp=2` 与 filter/notifier identity，然后签发 launch attestation。状态单调进入 sealed；此后 notifier 对任何 `execve`/`execveat`（含相同映像、FD、pathname）永久返回 `EPERM`，不存在重新授权或计数复位；
6. `terminated`：notifier、pidfd、child、helper 任一身份丢失，或 launcher crash/重启后无法从独立 supervisor 的 current held state 精确恢复时，关闭全部 APAP/private channel、kill+wait child，不得重建 attestation 或放行 request。

USER_NOTIF 的 `CONTINUE` 只用于上述单线程 bootstrap 的一次初始 held-FD exec。它不能用于连接后的 exec，也不能仅靠读取 syscall 参数后放行多线程 peer。filter 在初始 exec 后仍生效；目标 helper 的第一条业务用户指令执行前，所有线程已受同一 TSYNC filter 约束。fork/clone 派生体继承 filter；派生体同样不能 exec。

若目标 kernel 不提供 `NEW_LISTENER`、`TSYNC`、`ID_VALID`、`pidfd_getfd`、可信 procfs/pid namespace 或精确 audit architecture，Linux profile permanent fail closed。等价 ptrace 实现只有在替代 ADR 冻结 `PTRACE_SEIZE|PTRACE_O_TRACEEXEC|PTRACE_O_EXITKILL`、全线程/clone race、post-exec stop、tracer crash 与 detach 语义后才允许；本 ADR v1 不提供该 fallback。Darwin 保持 `unsupported`。

### 3. dedicated client/server helper 与不可转移通道

APAP control socket 的实际 peer 只能是两个固定、受上述 launcher 管理的 helper binary：

- `apap-control-client-helper` 独占 dialed APAP fd，代表一个已配置 consumer/verifier/authority principal；
- `apap-control-server-helper` 独占 listening/accepted APAP fd，代表 APAP service principal，并把获准 application request 转交 APAP main process。

业务 main process 不直接持有 APAP socket、launch attestation、connection receipt、notifier、launcher signing channel 或 signer handle。launcher 在初始 exec 前创建 owner-only、不可 pathname reopen 的 private `SOCK_SEQPACKET` socketpair；helper 与 main process 各持一端，其他进程不继承。helper 用 `SO_PEERCRED`、pidfd/birth、held executable identity 和预配置 principal 验证 main process；main process反向验证 helper。private channel 不接受 `SCM_RIGHTS`，任何 fd table 非空即关闭；helper 只代理 closed APAP envelope，不代理任意 bytes、credential 或 signer operation。

APAP fd、private-channel fd、notifier、attestation与receipt一律不可通过 `SCM_RIGHTS`、`pidfd_getfd` 授予其他 principal。helper 设置不可 dump，launcher/独立 supervisor 是唯一获准 ptracer；Yama/LSM/进程 capability 必须阻止其他同 UID 进程使用 `ptrace`、`process_vm_*` 或 `pidfd_getfd` 复制这些 fd。main process 身份、private channel identity 与 helper身份同时进入 launch attestation和connection receipt；任一变化关闭连接且不响应。

client/server helper 不能运行 Worker、读 probe credential、持 Publisher credential、authority private key或更改 Marshal 生命周期。server helper 在 transport bootstrap 完成前不能调用 APAP main process；client helper 在 bootstrap 完成前不能接收 main process 的 application request。

### 4. challenge、在线签发、消费与恢复

连接验证方通过与 trusted launcher 的独立 root-owned authenticated channel 创建 `PeerConnectionChallengeV1`；该 channel 不是被验证的 APAP socket，也不经过 peer helper。challenge 固定绑定 `challengeId`、验证方生成的 256-bit `connectionNonce`、期望 provider/principal/role、双方 connection identity、launch attestation digest、current authority generation、deadline 与 canonical challenge digest。

launcher journal 对每个 `challengeId` 使用单调状态：

`issued → receipt-issued → consumed`，以及终态 `expired|rejected`。

- 相同 `challengeId+challengeDigest` 在 `issued|receipt-issued` 重试返回同一 receipt bytes/digest，不重新签名；同 `challengeId` 不同 digest 固定 conflict；
- launcher 每次签发前主动复核 current keyset/revocation/generation、held notifier/pidfd/helper/main/private channel/connection identity 和 `exec-sealed` 状态；
- 验证方用独立 `InspectPeerConnectionChallenge` 恢复 lost response，只能取得已有状态和已有 receipt digest/bytes，不能触发新签名；
- transport 验证 receipt 后以 exact `challengeId+receiptDigest+connectionIdentityDigest` 调用 `ConsumePeerConnectionReceipt`。consume 是单次 compare-and-advance；同 digest replay 幂等返回 consumed，不同 digest conflict；
- receipt 未 consume、已过期、连接先关闭、generation/key epoch/revocation变化或 launcher状态不可恢复时进入 `expired|rejected` 并关闭 socket；不得把旧 receipt用于新连接；同 digest 的 consumed 重放只允许原 `connectionIdentityDigest` 的 lost-ack recovery，新 socket即使使用相同 receipt也拒绝；
- APAP application 收包必须发生在 durable consumed acknowledgement 之后。bootstrap 失败时双方静默关闭；`ProbeAuthority`、bundle、launch、signer 等 application handler 调用计数必须为零，且不得发送 APAP response。

launch attestation 不含 connection nonce，也不因每个连接重签；connection receipt 必须引用它的 digest并绑定 fresh nonce。两者的 producer principal、`SignedObjectEnvelopeV1.keyEpoch`、authority generation、helper/main identities 任一不等都 fail closed。

### 5. Key、producer 与验证规则

launch-attestation key 与 connection-receipt key是不同 usage，可属于同一 trusted launcher principal但不能跨用；二者都不能复用 APAP response、probe receipt、evidence/config、rotation、revocation或recovery key。验证方只信 current bundle 中精确 producer principal、usage、未撤销、在有效期内且 epoch 等于 `SignedObjectEnvelopeV1.keyEpoch` 的 key；object `authorityGeneration` 必须等于 current anchor。

所有签名统一使用 ADR 0038 `SignedObjectEnvelopeV1`；禁止新增第二个 signature envelope 或 top-level `launcherKeyId/keyEpoch`。错 producer、旧/未来 epoch、旧 generation、错 usage/domain、撤销 key、object digest 不等、同 key id 不同 bytes全部拒绝并静默关闭连接。

## 负向矩阵

独立测试至少覆盖：

1. admission 后清除 socket `CLOEXEC`，尝试 `exec-away → send 合法 packet → exec-back`：第一次 exec 在内核返回 `EPERM`，APAP application handler 调用数为零且无 response；`execveat` 与 fork/clone 后尝试同样失败；
2. initial exec 非 held FD、非空 path、flags 错误、FD 关闭/复用、`pidfd_getfd` identity 不符、第二次 initial allowance、notification id失效、TSYNC失败、额外线程、x32/非 native ABI 全部 kill+wait且无连接；
3. launch attestation 与 connection receipt 互换、nonce复用、同 challenge不同 digest、lost response inspect、consume crash/replay、过期、错 connection/helper/main/private channel identity全部 fail closed；
4. receipt 自签、错 producer principal、top-level key id、错 key epoch/generation/usage/domain、revoked key、launcher或supervisor crash全部 fail closed；
5. main process尝试直接连接APAP、helper转移APAP fd/receipt/notifier、private channel携带任意fd、同 UID进程 `ptrace`/`pidfd_getfd`复制均失败；
6. Qoder与Codex共用transport barrier测试，但继续分别满足ADR 0034/0037；本ADR不能让任一 Adapter 自动变为 `supported`。

## 后果与实施门禁

- APAP peer identity从“采样时相等”提升为“launcher证明一次held-FD初始exec，内核永久禁止后续映像替换”。
- 新增root launcher/supervisor、专用helper、两类签名对象、在线challenge journal与seccomp notifier的部署成本。
- 本ADR只关闭control transport peer exec swap-back，不实现APAP业务operation、真实probe或registry enablement。

实施顺序固定为：独立reviewer审查并由维护者接受本ADR；随后实现closed Schema/canonical/signature与challenge journal；再实现launcher/USER_NOTIF/helper/bootstrap；最后运行真实Linux kernel负向fixture、race、全量CI与secret scan并做独立实现审查。任何阶段失败均保持production transport hard-disabled，且不得宣称Qoder/Codex `supported`。
