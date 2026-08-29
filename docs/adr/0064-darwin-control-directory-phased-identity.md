# ADR 0064：Darwin 控制目录阶段化身份与 APFS `LinkCount` 语义

- 状态：提议（Proposed，2026-08-29；未经维护者接受不得启用对应生产实现）
- 关联：[ADR 0051](0051-darwin-local-dogfood-profile.md)（Darwin ordinary-user 边界）、[ADR 0059](0059-fixed-darwin-process-supervisor.md)（固定 Supervisor 与控制目录）、[ADR 0060](0060-supervisor-mechanics-authority-binding-and-recovery.md)（bootstrap recovery anchor 与 started fact）、[ADR 0063](0063-prepared-execution-authority-and-production-chain.md)（唯一生产者链）、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)。

## 背景

ADR 0059 要求每个 Attempt 使用 owner-only、descriptor-relative 打开的稳定控制目录，并把目录 `link count` 列入 handshake 与 hostile matrix。当前实现据此把 bootstrap 前观察到的完整 `ControlDirectoryIdentity` 用于所有后续比较。

真实 macOS/APFS 验证发现：Supervisor 合法创建 `session.nonce`、mechanics journal、`control.sock`，以及稍后创建 stdout、stderr、transcript data object 时，目录对象自身未替换，但目录 `st_nlink` 可以变化。若运行期继续逐字段比较 bootstrap 的 `LinkCount`，合法 setup 会在 handshake 或首条 command 前被误判为目录 ABA；例如本应在 mechanics receipt 已 `fsync` 后发现 journal mode drift 的路径，会提前停在 pre-command，journal sequence 仍为 `1`，无法证明 post-receipt fail-closed 语义。

直接忽略整个目录身份、放宽未知 entry、只看 pathname，或取消 nonce/journal/socket 精确身份都会降低 ADR 0059 的门禁。需要区分三件事：

1. bootstrap 前尚未产生副作用的 **initial empty identity**；
2. Supervisor 创建固定控制对象并同步父目录后的 **final setup identity**；
3. runtime 中稳定目录对象与可变 entry 集合的分别验证。

## 决策

### 1. 适用范围与取代关系

1. 本合同只适用于 ADR 0051 的 Mac-first `darwin-local-dogfood` ordinary-user profile，不提供 Linux authority、hardened containment、跨用户或同 UID 恶意进程隔离。
2. 本 ADR 部分修订 ADR 0059 §4、§6、§8 中“目录 link-count 漂移一律等同对象漂移”的局部语义：目录 `LinkCount` 是阶段观察值，不是 runtime authorization field；nonce、journal、socket 和输出对象的各自 `LinkCount` 仍按本 ADR 第 4 节精确验证。
3. 本 ADR 精确修订 ADR 0060 §2 中 bootstrap-prepared 与 `process-supervisor-started` 的 control-directory 逐字段相等要求：bootstrap-prepared 保存 initial identity；started 保存 final setup identity；二者必须满足第 3 节的同一稳定对象关系，不能要求 `LinkCount` 相等。
4. 既有 authority、mechanics journal、protocol revision 与 JSON 字段不变，不新增第二 store 或 pathname authority。历史事实保持逐字节 replay，不能把历史 initial observation 合成为新的 final observation或反向升级为生产证据。

### 2. bootstrap initial identity 必须完整且为空

Core 在任何 Supervisor 控制对象产生前，通过 held control-directory FD 观察完整 initial `ControlDirectoryIdentity`：

```text
canonicalPath, device, inode, fileType, uid, gid, mode, linkCount
```

该完整值进入 bootstrap-prepared 与 `BootstrapRequest`。Supervisor 从继承的 held FD 重新观察时必须与 request **全部字段精确相等**，包括 initial `LinkCount`；随后必须从该 held FD 以 descriptor-relative、独立 directory stream 枚举，并证明目录为空。预先存在 entry、共享 stream offset 隐藏 entry、symlink/path replacement、mode/owner/object 不符或枚举失败均在创建 nonce、journal、socket、child 前 fail closed。

initial empty 检查不能被“entry 名称属于未来 allowlist”替代。bootstrap 时即使只存在一个未来合法名称，也不得 adopt。

### 3. setup final identity 与稳定对象关系

Supervisor 只能按固定顺序，以 descriptor-relative `O_EXCL`/nofollow 等价语义创建 owner-only nonce、mechanics journal 与 socket，并在每个新对象及 held parent 上完成既有同步要求。socket identity、nonce identity 与 journal identity全部验证成功后，Supervisor 与 Core 必须分别从各自持有的同一 directory FD 重新观察 final setup `ControlDirectoryIdentity`。

initial、final 与后续 current observation 必须在以下稳定对象字段上精确相等：

```text
canonicalPath, device, inode, fileType, uid, gid, mode
```

只有目录自身的 `LinkCount` 允许因当前阶段已授权的 entry 创建而不同。任何 path、device、inode、type、owner、group 或 mode 漂移仍是对象 ABA/security-boundary drift，固定 fail closed。current canonical pathname 还必须重开并证明仍指向同一 device/inode；held FD 不能把已被 pathname 替换的目录继续冒充 current control root。

Darwin 的 current pathname重开必须使用 `O_NOFOLLOW_ANY` 保护整个 canonical path，或从已验证的 held canonical parent 开始逐组件 descriptor-relative/no-follow 打开。只保护末级的 `O_NOFOLLOW`、裸 absolute `lstat`/`stat`/`open` 或先解析再打开均不满足本合同。Unix socket pathname仍可作 locator，但授权继续来自 descriptor-relative socket identity、peer credential/process birth与fixed Marshal binary，而不是该 pathname。

`ConnectionEvidence.ControlDirectory` 与新写入的 `process-supervisor-started.ControlDirectory` 必须保存 **final setup observation**。started 同时引用 bootstrap-prepared digest；admission 必须证明 initial 与 final 是上述同一稳定对象。禁止用 bootstrap initial identity填充 final、在 Core 内预测 APFS `LinkCount`，或在 final observation 之后再创建未被下节覆盖的 setup object。

### 4. descriptor-relative closed entry 集与对象级精确门禁

每次 command 前后、reconnect、transcript read 与 committed-close recovery 的 control-directory recheck 都必须通过 held FD 重新打开独立 directory stream并枚举全部名称。名称集合不是任意 allowlist 子集，而由已耐久的 mechanics 阶段精确决定：

| 阶段 | 允许的精确 entry 集 |
| --- | --- |
| bootstrap initial | 空集 |
| final setup、bind/spawn/resume/inspect/terminate，以及 exact `collect` intent 前 | `session.nonce` + journal + `control.sock`，三项必须恰好存在 |
| exact `collect` intent 已耐久、receipt 尚未闭合 | 基础三项，加输出创建顺序的单调前缀：空、仅 stdout、stdout+stderr、stdout+stderr+transcript；只能由同一 pending collect mechanics解释 |
| successful `collect` receipt 后 | 基础三项 + stdout + stderr + transcript，六项必须恰好存在 |
| rejected/unknown collect留下部分输出 | 只允许按 exact pending/outcome恢复并进入 intervention；不能把部分集合提升为 collected transcript |
| `close`/offline recovery | 按 ADR 0061 的 exact transcript disposition 与已耐久 collect outcome选择上述基础三项或完整六项；不得猜测 |

冻结名称为：

```text
session.nonce
process-supervisor-v1.journal
control.sock
stdout.bin
stderr.bin
transcript.jcs
```

未知 entry、提前出现的输出名、与当前 durable mechanics阶段不符的缺项/多项、枚举失败或名称编码异常固定 fail closed。名称属于冻结集合只说明它可能合法，不能单独授权或 adopt 该对象。尤其 final setup admission 必须证明 **恰好** 只有 nonce、journal、socket；当前实现候选若只检查“六项 allowlist 的任意子集”，仍未满足本 ADR，必须在 acceptance 后补 phase-aware exact-set gate。

对象门禁为：

- `session.nonce` 与 mechanics journal 必须持续比较 held descriptor 和 descriptor-relative current pathname 的完整 file identity；两者均为 owner-only `0600` regular file、`LinkCount=1`。nonce 的固定长度、字符集与 digest仍精确验证；journal framing、sequence、head、size与内容规则仍由 ADR 0059/0060约束。
- `control.sock` 必须持续按 descriptor-relative current pathname 比较完整 socket identity，包含 device/inode/type/uid/gid/mode/`LinkCount=1`，并继续绑定 peer credential、process birth 与 fixed Marshal binary。
- `stdout.bin`、`stderr.bin` 与 `transcript.jcs` 只能在 exact collect intent 之后由 Supervisor 以 descriptor-relative `O_EXCL`/nofollow 创建；预先存在即拒绝。每次读取事务都必须持有该次 descriptor，验证 owner-only regular identity、`LinkCount=1`、有界 size、读前/读后同一 identity/size、事务结束前 current relative name仍指向该 identity，以及 command outcome 中的 exact content digest/byte count/truncation binding。

v1 protocol/authority fact不持久化三个输出文件的 inode identity，因此上述门禁只证明 **单次读取事务** 与已耐久 content digest/bytes闭合，不构成跨两次读取的文件对象 authority。ADR 0051 已明确 fully controlled same-UID attacker不在 ordinary-user profile assurance：其在两次读取之间以相同 mode与相同内容换成新 inode，下一次仍得到相同 content digest的场景不由本 ADR声称可检测。若未来 hardened profile或业务语义要求跨时间对象连续性，必须另行升级 protocol/persisted projection或让authority持有生命周期覆盖完整 admission 的FD；本局部目录 `LinkCount` 修复不得擅自扩 protocol。

允许目录 `LinkCount` 变化不得放宽以上任何对象门禁，也不得允许可执行文件、临时文件、备份文件、lock file或未来未版本化名称进入控制目录。增加名称必须先升级本合同或 protocol revision，并补完整 crash/hostile matrix。

### 5. command、重连、transcript 与 close 语义

1. `sessionControlBoundary` 冻结 final setup observation，并在每条 command mechanics 前后按当前 durable mechanics阶段执行第 3、4 节 exact-set recheck。合法输出对象造成的目录 `LinkCount` 变化不构成冲突。
2. 若 command 前发现未知 entry、稳定字段漂移或 nonce/journal/socket exact drift，必须零 mechanics、零 response 并进入 intervention。
3. 若 command intent与receipt已经按 ADR 0059 `fsync`，随后 post-command recheck发现漂移，receipt保持耐久（首条 command对应 journal sequence `3`），但 Core不得收到 success/rejected response；Supervisor进入 intervention。不得通过提前失败、截去合法 receipt或返回空壳成功掩盖该边界。
4. Reconnect 接受的 durable control-directory observation可以来自 final setup或较早合法 runtime observation；它与 current只比较稳定对象字段，并同时根据 exact pending command/outcome执行阶段化 entry set、nonce/journal/socket exact gate。不同 `LinkCount` 本身不能阻止同一 Supervisor重连。
5. Transcript read与 committed-close recovery使用相同阶段规则。合法 stdout/stderr/transcript 已创建后仍可读取/恢复；未知/提前 entry、单次读取事务内的输出对象 ABA/content drift、稳定目录对象漂移或 control object drift仍 fail closed。跨读取事务只授权相同 content digest，不声明相同 inode。

### 6. 最小 hostile 与恢复矩阵

实现至少覆盖：

- initial 目录非空，即使 entry 名属于 allowlist也在副作用前拒绝；initial完整 identity任一字段漂移拒绝；
- APFS setup创建 nonce/journal/socket 后目录 `LinkCount` 增长，final exact set恰为三项、handshake成功且 ConnectionEvidence保存final observation；任一输出名提前出现均拒绝；
- collect依次创建三个合法输出对象造成 `LinkCount` 变化，command boundary、reconnect、transcript read与committed-close recovery仍通过；
- 每个稳定字段 path/device/inode/type/uid/gid/mode单独漂移、current pathname ABA、symlink replacement均拒绝；
- 六个冻结名之外任意 entry在每个阶段都拒绝；冻结名与当前 mechanics阶段不符也拒绝；任一名称预创建、symlink、hardlink、弱 mode与对象替换仍拒绝；
- nonce、journal、socket各自 inode/mode/owner/link-count ABA拒绝；输出 identity、size或content digest漂移拒绝；
- post-receipt drift保留 exact receipt/journal sequence但返回零 response；response loss后重连不能把 conflict解释为可重试成功；
- `O_NOFOLLOW_ANY`/held-parent-chain覆盖中间路径 symlink；仅末级 nofollow与裸 absolute reopen负测拒绝；
- 单次 transcript read中的 pathname swap、identity/size/content drift拒绝；两次read之间same-UID相同内容新inode只验证content等价，不误称跨时间对象检测；
- 不把 control path、nonce、raw output或目录枚举内容写入普通 log/event/ReviewPacket。

## 后果与实施约束

该决策保留 bootstrap 的完整空目录门禁和每个控制/输出对象的精确身份，只把 APFS 目录级 `LinkCount` 从 runtime authorization降为阶段 observation。代价是 initial/final 两份 secret-free directory observation与每次边界一次 descriptor-relative有界枚举；v1最多六个 entry，成本固定且不需要新服务、Schema或store。

本 ADR 处于 Proposed。对应实现候选必须保持冻结，直到维护者接受本合同并由独立 reviewer确认真实 diff 的 P0/P1清零。候选 `765617c20ea3faee71af980d70a35ecd06e3462a` 已实现稳定字段比较、final observation、closed name拒绝与post-receipt sequence测试，但其通用六名称子集检查尚未实现本 ADR要求的 phase-aware exact set，因此不是可直接接纳的完整实现。接受也只授权 Darwin control-directory 局部语义，不表示 ADR 0059/0060/0063 全链已实现，不升级 I186-R2–R6，不授权 Linux/hardened profile，也不解除 macOS签名/notarization或稳定 `v1.*` 发布门禁。
