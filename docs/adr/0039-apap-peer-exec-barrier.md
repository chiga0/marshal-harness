# ADR 0039：APAP peer exec 单调屏障

- 状态：已接受（Accepted，2026-08-18；接受只冻结合同，未实现前不得启用）
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
2. `filter-installed`：bootstrap 必须保持单线程；launcher 先逐段 nofollow 打开 `/proc/<pid>/task`，枚举结果必须精确只有 leader TID，并持有该 task dir identity。child 在执行任何不可信用户指令前设置 `PR_SET_NO_NEW_PRIVS=1`，只以 `SECCOMP_FILTER_FLAG_NEW_LISTENER` 安装 BPF filter；`NEW_LISTENER|TSYNC` 同次调用在 Linux 非法并返回 `EINVAL`，不得使用或把该失败降级为无 listener filter。child 把唯一 notifier fd 交给 launcher后不再持有 notifier；launcher 在 notifier 交付后、允许初始 exec 前分别重新从同一 held task dir 枚举，两次结果仍必须精确只有 leader TID，否则 kill+wait；
3. `initial-exec-pending`：filter 对 native ABI 的 `execve/execveat` 发送 USER_NOTIF；对 x32 bit、非 native audit architecture 或未知 syscall encoding 固定 `KILL_PROCESS`；对 `ptrace`、`process_vm_writev`、`bpf`、`userfaultfd`、`kexec_load`、`finit_module`、`init_module`、`unshare`、`setns` 固定拒绝；legacy `clone` 只允许不含 user/pid/mount namespace bit 的线程创建，`clone3` 因 BPF 不能安全解引用参数结构而固定拒绝；
4. `initial-exec-authorized`：只允许一次 `execveat(sourceFD,"",argv,env,AT_EMPTY_PATH)`。launcher 先用 `SECCOMP_IOCTL_NOTIF_ID_VALID` 验证 notification 存活，再用 pidfd 保证 birth identity，并以 `pidfd_getfd` 复制 child 的 sourceFD；复制对象的完整 held identity/digest 必须等于 policy 与 launch request。bootstrap 保证此时只有一个线程、argv/env memory 由 launcher 创建后不可由其他线程改写、sourceFD 不可关闭/复用；任一条件不可证明即 kill+wait。只有这一次 notification 可返回 `SECCOMP_USER_NOTIF_FLAG_CONTINUE`；
5. `exec-sealed`：launcher 观察初始 exec 完成，重新验证 pidfd/birth、`/proc/<pid>/exe` held identity、`NoNewPrivs=1`、`Seccomp=2` 与 filter/notifier identity，然后签发 launch attestation。状态单调进入 sealed；此后 notifier 对任何 `execve`/`execveat`（含相同映像、FD、pathname）永久返回 `EPERM`，不存在重新授权或计数复位；
6. `terminated`：notifier、pidfd、child、helper 任一身份丢失，或 launcher crash/重启后无法从独立 supervisor 的 current held state 精确恢复时，关闭全部 APAP/private channel、kill+wait child，不得重建 attestation 或放行 request。

USER_NOTIF 的 `CONTINUE` 只用于上述经三次 task 枚举证明仍为单线程的 bootstrap 的一次初始 held-FD exec。它不能用于连接后的 exec，也不能仅靠读取 syscall 参数后放行多线程 peer。filter 在初始 exec 后仍生效；目标 helper 的第一条业务用户指令执行前，唯一线程已受 filter 约束，之后由该线程创建的所有线程和 fork/clone 派生体按 Linux seccomp 继承规则取得同一 filter，且同样不能 exec。实现不得宣称使用了 TSYNC；若任一次 task 枚举出现额外 TID、task dir identity 改变或无法完整枚举，立即 kill+wait。

若目标 kernel 不提供 `NEW_LISTENER`、`TSYNC`、`ID_VALID`、`pidfd_getfd`、可信 procfs/pid namespace 或精确 audit architecture，Linux profile permanent fail closed。等价 ptrace 实现只有在替代 ADR 冻结 `PTRACE_SEIZE|PTRACE_O_TRACEEXEC|PTRACE_O_EXITKILL`、全线程/clone race、post-exec stop、tracer crash 与 detach 语义后才允许；本 ADR v1 不提供该 fallback。Darwin 保持 `unsupported`。

### 3. dedicated client/server helper 与不可转移通道

APAP control socket 的实际 peer 只能是两个固定、受上述 launcher 管理的 helper binary：

- `apap-control-client-helper` 独占 dialed APAP fd，代表一个已配置 consumer/verifier/authority principal；
- `apap-control-server-helper` 独占 listening/accepted APAP fd，代表 APAP service principal，并把获准 application request 转交 APAP main process。

业务 main process 不直接持有 APAP socket、launch attestation、connection receipt、notifier、launcher signing channel 或 signer handle。launcher/supervisor 为 helper 与 main process 建立 owner-only private `SOCK_SEQPACKET` 连接：root-owned 临时 listener 只用于双方以真实进程调用 `connect/accept`，双方完成 `SO_PEERCRED`、pidfd/birth、held executable identity 与预配置 principal 的双向验证后立即 unlink；之后 authority 只来自双方与 supervisor 持有的 endpoint fd，路径不能 reopen。禁止用启动前 `socketpair` 的创建者 credential 冒充实际 holder。其他进程不继承 endpoint。helper 只代理 closed APAP envelope 与下述 exact typed descriptor custody，不能代理任意 bytes、credential 或 signer operation。

`PrivateControlFrameV1` 是 private channel 唯一消息，closed 字段精确为 `schemaVersion="marshal.apap-private-control-frame.v1"`、`direction`、`operation`、`envelopeDigest`、`descriptorTableDigest`、`descriptors`。`direction` 枚举固定为 `client-main-to-helper|client-helper-to-main|server-helper-to-backend|server-backend-to-helper`；`descriptors` 是 0..32 项有序数组，每项字段只有 `index`（从 0 连续递增）、`role`、`objectIdentityDigest`。`descriptorTableDigest=sha256(JCS(descriptors))`。SCM_RIGHTS fd 数量、顺序、role 与逐个重新测量的 held `ObjectIdentity` digest 必须和 descriptors 精确相等；未知/重复 role、索引缺口、额外/缺失 fd、identity变化或尾随 control message立即关闭本 frame 全部 fd及连接。

允许的 descriptor table 只能逐 operation、逐方向等于 ADR 0038 §2 的固定表：client main→client helper→APAP 的 request table，以及 APAP→server helper→backend 的 request table；response 反向只允许 `ReadBundleLeafBatch` 等 ADR 0038 明确声明的 response fd table。`BeginProbe` 的 `candidateExecutable,scratchRoot,businessDenyRoot[1..16]`、`PrepareLaunch` 的八个精确 held fd、`StageBundleLeafBatch`/`ReadBundleLeafBatch` 的 ordered bundle leaf 因而可以在每一 hop 受同一 envelope/operation/role/identity/order 约束地移交。每个接收方在确认并接管 held fd 后返回绑定 frame digest 的 custody ack；发送方只有收到 ack 才关闭自己的 duplicate。lost ack 只能 Inspect 同一 frame/custody digest，不得重发为第二组 authority fd。

client 方向在 main→helper 与 helper→APAP 两个 hop 都重算并核对 exact table；server 方向在 APAP socket→server helper 与 server helper→backend 两个 hop 同样重算。backend 只暴露 digest-bound、operation-specific frontend method，不接受裸 socket、裸 fd slice、pathname或未绑定 envelope 的 descriptor。helper 不能改变 role、顺序、object identity 或 envelope digest，也不能缓存 fd 给后续 operation。

`credentialCapability`、CredentialIngress endpoint、APAP socket、private-channel fd、seccomp notifier、launcher/signer channel、private key/HSM handle、launch attestation与connection receipt carrier永远不在允许 descriptor table 中，不得通过 `SCM_RIGHTS` 或 custody frame授予其他 principal；其中 credential 继续只走 ADR 0038 独立 `CredentialIngressPort`。唯一例外是本 ADR 明确要求 launcher/独立 supervisor 用 `pidfd_getfd` 保留的 APAP/private endpoint 与 notifier 监督 duplicate；这些 duplicate 只能用于 identity/kcmp/liveness，不能读写 application packet，也不能再转移。helper 设置不可 dump，launcher/独立 supervisor 是唯一获准 ptracer；Yama/LSM/进程 capability 必须阻止其他同 UID 进程使用 `ptrace`、`process_vm_*` 或 `pidfd_getfd` 复制这些 forbidden fd。main process 身份、private channel identity 与 helper身份同时进入 launch attestation和connection receipt；任一变化关闭连接且不响应。

client/server helper 不能运行 Worker、读 probe credential、持 Publisher credential、authority private key或更改 Marshal 生命周期。server helper 在 transport bootstrap 完成前不能调用 APAP main process；client helper 在 bootstrap 完成前不能接收 main process 的 application request。

### 4. 三类 kernel identity 的唯一 canonical 投影

以下三类 digest 只能由 launcher/supervisor 对自己持续持有的 kernel fd 生成；pathname、application 提供的 fd number、application/helper 自选 nonce、请求字段或 peer 自报都不能参与 authority。所有 record 都是 closed object，经 JCS 后做 SHA-256；下列数组顺序是协议的一部分。

`KernelSocketEndpointIdentityV1` 字段精确为：

- `schemaVersion="marshal.kernel-socket-endpoint-identity.v1"`；
- `endpointRole`，枚举为 `client|server|helper|main`；
- `direction`，枚举为 `dial-out|accept-in|helper-to-main|main-to-helper`；
- `addressFamily="AF_UNIX"`、`socketType="SOCK_SEQPACKET"`、`protocol=0`；
- `device:KernelUint64`、`inode:KernelUint64`、`mode`、`socketCookie:KernelUint64`，分别来自 held fd 的 `fstat` 与 `SO_COOKIE`；
- `ownerPid`、`ownerStartTimeTicks:KernelUint64`、`ownerPidfdInode:KernelUint64`；
- `peerUid`、`peerGid`、`peerPid`、`peerStartTimeTicks:KernelUint64`，来自 `SO_PEERCRED` 与同一 held pidfd/proc birth identity。

`connectionIdentityDigest` 精确等于 `sha256(JCS(ConnectionIdentityV1))`；`ConnectionIdentityV1` 字段只有 `schemaVersion="marshal.apap-connection-identity.v1"` 与 `endpoints`。`endpoints` 必须精确为 `[clientEndpoint,serverEndpoint]`，前者 `endpointRole=client,direction=dial-out`，后者 `endpointRole=server,direction=accept-in`。launcher/supervisor 必须分别持有两个 endpoint 的原始 fd，验证二者互为 `SO_PEERCRED` 所指进程，并要求 client 与 server connection receipt 携带逐字节相同的 digest；交换顺序、只绑定本端、socketpair替换、cookie/stat/peer变化全部拒绝。

`privateControlChannelIdentityDigest` 精确等于 `sha256(JCS(PrivateControlChannelIdentityV1))`；其字段只有 `schemaVersion="marshal.apap-private-control-channel-identity.v1"` 与 `endpoints`。`endpoints` 必须精确为 `[helperEndpoint,mainEndpoint]`，方向分别为 `helper-to-main`、`main-to-helper`。launcher/supervisor 在双方真实进程完成 connect/accept 后用 pidfd 与 `pidfd_getfd` 保留两个监督 duplicate，helper/main 不能替换；每个 endpoint 的 `SO_PEERCRED` 必须指向另一个 record 的 owner PID/birth，双方各自验证本端与对端 record，launch attestation 与 connection receipt 必须绑定同一个 digest。临时 bootstrap leaf 在 identity 生成前必须已 unlink，record 中不存在 path 字段。

`notifierIdentityDigest` 精确等于 `sha256(JCS(SeccompNotifierIdentityV1))`；该 record 字段精确为：

- `schemaVersion="marshal.seccomp-notifier-identity.v1"`；
- `ownerPid`、`ownerStartTimeTicks:KernelUint64`、`ownerPidfdInode:KernelUint64`；
- `fdDevice:KernelUint64`、`fdInode:KernelUint64`、`fdMode`、`fdFlags`，来自 launcher 持有 notifier fd 的 `fstat/fcntl`；
- `filterDigest`、`listenerFlags=["NEW_LISTENER"]`；
- `openFileDescriptionProof="kcmp-file-equal"`。

launcher 与独立 supervisor 各持一个同一 notifier open-file-description 的 duplicate，并在 attestation 前及每次 connection receipt 签发前用 `kcmp(KCMP_FILE)` 证明二者相等；任一 duplicate 关闭、FD identity/flags/filter digest变化、`kcmp` 不可用或不等即终止 child。`notifierIdentityDigest` 是该 held kernel authority 的 canonical投影，不替代持续持有与 `kcmp` 复核。

`KernelUint64` 不是 JSON number，而是 canonical 十进制 JSON string：正则精确为 `^(0|[1-9][0-9]{0,19})$`，解析后必须位于 `0..18446744073709551615`，重新以 base-10 无符号格式编码必须逐字节等于输入。前导零、正负号、空白、小数、指数、超出 `uint64`、以及把该字段编码成 JSON number 全部非法。这样 JCS 实现即使内部通过 IEEE-754 `float64` 处理 JSON number，也不能把大 kernel identity 合并。

只有具有协议安全上界且该上界不超过 `2^53-1` 的值保留 JSON integer：`ownerPid/peerPid` 为 `1..2^31-1`，`peerUid/peerGid/mode/fdMode/fdFlags` 为 `0..2^32-1`，`protocol` 固定为 `0`，数组 `index` 为 `0..31`。`mode/fdMode` 记录完整 `fstat.st_mode`；`socketCookie` 使用 kernel 返回的 unsigned 64-bit值并编码为 `KernelUint64`。任何未来新增、没有机械上界证明的 kernel counter/id/size/time tick 默认必须使用 `KernelUint64`，不得使用 JSON number。

canonical vectors 必须包含 `"9007199254740991"`、`"9007199254740992"`、`"9007199254740993"` 与 `"18446744073709551615"` 的成功向量；相邻的 `"9007199254740992"`/`"9007199254740993"` 及 `"18446744073709551614"`/`"18446744073709551615"` 必须产生不同 JCS bytes 与不同 digest。另须覆盖 JSON number `9007199254740992`、`"01"`、`"+1"`、`"1e3"`、`"18446744073709551616"` 全部拒绝，证明不存在 `2^53` 舍入碰撞或 `uint64` wrap。

### 5. challenge、在线签发、消费与恢复

连接验证方通过与 trusted launcher 的独立 root-owned authenticated channel 创建 `PeerConnectionChallengeV1`；该 channel 不是被验证的 APAP socket，也不经过 peer helper。`connectionNonce` 必须由验证方的独立 launcher/verification authority 使用 kernel CSPRNG 生成 256 bit，main process、helper、Adapter、APAP application 与请求调用者均不能选择、覆盖或提供 nonce。challenge 固定绑定 `challengeId`、`connectionNonce`、期望 provider/principal/role、双方 `connectionIdentityDigest`、两侧 launch attestation digest、current authority generation、deadline 与 canonical challenge digest。

launcher journal 对每个 `challengeId` 使用单调状态：

`issued → receipt-issued → consumed`，以及终态 `expired|rejected`。

- 相同 `challengeId+challengeDigest` 在 `issued|receipt-issued` 重试返回同一 receipt bytes/digest，不重新签名；同 `challengeId` 不同 digest 固定 conflict；
- launcher 每次签发前主动复核 current keyset/revocation/generation、held notifier/pidfd/helper/main/private channel/connection identity 和 `exec-sealed` 状态；
- 验证方用独立 `InspectPeerConnectionChallenge` 恢复 lost response，只能取得已有状态和已有 receipt digest/bytes，不能触发新签名；
- transport 验证 receipt 后以 exact `challengeId+receiptDigest+connectionIdentityDigest` 调用 `ConsumePeerConnectionReceipt`。consume 是单次 compare-and-advance；同 digest replay 幂等返回 consumed，不同 digest conflict；
- receipt 未 consume、已过期、连接先关闭、generation/key epoch/revocation变化或 launcher状态不可恢复时进入 `expired|rejected` 并关闭 socket；不得把旧 receipt用于新连接；同 digest 的 consumed 重放只允许原 `connectionIdentityDigest` 的 lost-ack recovery，新 socket即使使用相同 receipt也拒绝；
- APAP application 收包必须发生在 durable consumed acknowledgement 之后。bootstrap 失败时双方静默关闭；`ProbeAuthority`、bundle、launch、signer 等 application handler 调用计数必须为零，且不得发送 APAP response。

launch attestation 不含 connection nonce，也不因每个连接重签；connection receipt 必须引用它的 digest并绑定 fresh nonce。两者的 producer principal、`SignedObjectEnvelopeV1.keyEpoch`、authority generation、helper/main identities 任一不等都 fail closed。

### 6. Key、producer 与验证规则

launch-attestation key 与 connection-receipt key是不同 usage，可属于同一 trusted launcher principal但不能跨用；二者都不能复用 APAP response、probe receipt、evidence/config、rotation、revocation或recovery key。验证方只信 current bundle 中精确 producer principal、usage、未撤销、在有效期内且 epoch 等于 `SignedObjectEnvelopeV1.keyEpoch` 的 key；object `authorityGeneration` 必须等于 current anchor。

所有签名统一使用 ADR 0038 `SignedObjectEnvelopeV1`；禁止新增第二个 signature envelope 或 top-level `launcherKeyId/keyEpoch`。错 producer、旧/未来 epoch、旧 generation、错 usage/domain、撤销 key、object digest 不等、同 key id 不同 bytes全部拒绝并静默关闭连接。

## 负向矩阵

独立测试至少覆盖：

1. admission 后清除 socket `CLOEXEC`，尝试 `exec-away → send 合法 packet → exec-back`：第一次 exec 在内核返回 `EPERM`，APAP application handler 调用数为零且无 response；`execveat` 与 fork/clone 后尝试同样失败；
2. initial exec 非 held FD、非空 path、flags 错误、FD 关闭/复用、`pidfd_getfd` identity 不符、第二次 initial allowance、notification id失效、非法 `NEW_LISTENER|TSYNC` 组合、任一阶段额外线程、x32/非 native ABI 全部 kill+wait且无连接；
3. launch attestation 与 connection receipt 互换、nonce复用、同 challenge不同 digest、lost response inspect、consume crash/replay、过期、错 connection/helper/main/private channel identity全部 fail closed；
4. receipt 自签、错 producer principal、top-level key id、错 key epoch/generation/usage/domain、revoked key、launcher或supervisor crash全部 fail closed；
5. main process尝试直接连接APAP、helper转移APAP fd/receipt/notifier/credential/signer handle、private frame 的 operation/方向/role/顺序/count/identity/envelope/custody ack 任一不符、裸 fd slice/backend pathname、同 UID进程 `ptrace`/`pidfd_getfd`复制 forbidden fd 均失败；合法 `BeginProbe`/`PrepareLaunch`/bundle leaf 双向多 hop custody则保持 exact table；connection/private endpoint 顺序交换、方向错、`SO_COOKIE`/held stat/peer birth变更、notifier `kcmp` 不等、path或application nonce参与 identity也失败；`2^53` 边界、`uint64` max 与相邻大整数 canonical/digest vectors 无碰撞；
6. Qoder与Codex共用transport barrier测试，但继续分别满足ADR 0034/0037；本ADR不能让任一 Adapter 自动变为 `supported`。

## 后果与实施门禁

- APAP peer identity从“采样时相等”提升为“launcher证明一次held-FD初始exec，内核永久禁止后续映像替换”。
- 新增root launcher/supervisor、专用helper、两类签名对象、在线challenge journal与seccomp notifier的部署成本。
- 本ADR只关闭control transport peer exec swap-back，不实现APAP业务operation、真实probe或registry enablement。

实施顺序固定为：独立reviewer审查并由维护者接受本ADR；随后实现closed Schema/canonical/signature与challenge journal；再实现launcher/USER_NOTIF/helper/bootstrap；最后运行真实Linux kernel负向fixture、race、全量CI与secret scan并做独立实现审查。任何阶段失败均保持production transport hard-disabled，且不得宣称Qoder/Codex `supported`。
