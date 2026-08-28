# ADR 0058：解释型 Agent 启动身份与材料闭包

- 状态：已接受（Accepted，2026-08-28；candidate sourceHead `2e338273c1fad796e418b089617608829a846e37` 经独立 reviewer 审查，P0/P1/P2 均为 0）；接受只冻结合同，不表示实现完成，不升级 Pi 或任何能力的生产可达性
- 关联：[ADR 0043](0043-worker-executor-profile-and-dual-binding.md)（`AgentLaunchSpec`）、[ADR 0051](0051-darwin-local-dogfood-profile.md)（Darwin ordinary-user 边界）、[ADR 0052](0052-v1-release-scope-and-production-reachability.md)（生产可达性）、[ADR 0055](0055-sandbox-exec-workload-envelope.md)（allocation-carried Exec）、[ADR 0056](0056-darwin-process-observation-and-attempt-terminalization.md)（真实进程观察）、[ADR 0057](0057-durable-local-allocation-recovery-and-production-composition.md)（唯一生产装配）、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)

## 背景

Pi 的配置入口可以是指向 JavaScript 的 symlink，文件又以 `#!/usr/bin/env node` 启动。若 Marshal 只摘要该入口再把它当作 executable，就会把真实进程映像（Node）、Provider 代码材料和 PATH 选择混为一个身份；`ProcessObservation` 也可能错误地把脚本记成内核实际执行的 executable。

ADR 0056 要求 `process-started` 描述真实进程映像，并要求固定、已允许的启动对象。解释型 Provider 因此必须同时冻结解释器和 Provider 材料，且不能依赖 shebang、`env` 或 ambient PATH 补齐身份。本 ADR 只补齐这一合同，不建立包管理、安装收据、签名或 notarization 体系。

## 决策

### 1. Core-owned 版本闭包与两层身份

每个 production `AgentLaunchSpec` 必须冻结：

1. `RuntimeExecutableV1`：内核实际执行的 absolute canonical regular file；Pi 必须是显式固定的 Node，不能是 `pi` symlink、`cli.js`、`/usr/bin/env` 或 PATH 中解析出的 `node`；
2. `ClosureProfileID`、`MaterialRoots[]` 与 `LaunchMaterials[]`：Core 按精确 Provider 版本拥有并执行闭包 profile；Adapter 只能提交待核对 manifest，不能定义闭包；
3. `LaunchMaterialsDigest`：第 3 节定义的闭包摘要；
4. `AgentLaunchSpecDigest`：对包含 runtime、profile、roots、完整 materials、argv、environment 和 cwd 的 closed `AgentLaunchSpec` 作 RFC 8785 JCS 后计算 `sha256:<lowercase-hex>`。

Pi 0.84.3 的候选 profile 固定为 `pi/0.84.3/darwin-arm64/v1`。runtime 是配置中显式给出的 canonical Node。Core-owned profile 只声明两个 versioned named roots：

| root name | Pi 安装根内的固定 relative path | 确定性闭包（2026-08-28 实测） |
| --- | --- | --- |
| `pi-bundle` | `dist/bundle` | 全部 descendant regular files，48 个、7,422,432 bytes |
| `photon-node` | `node_modules/@silvia-odwyer/photon-node` | 全部 descendant regular files，7 个、2,265,687 bytes |

`MaterialRootV1` 必须保存 root name、absolute canonical path、相对受支持 Pi package root 的上述固定 path，以及 nofollow 打开的 held directory identity。Core 从这两个 roots 自行生成 manifest：逐段 nofollow 打开；目录项按 raw UTF-8 bytes 升序；只接纳未经过 symlink 的 regular file；role 唯一等于 `<root-name>/<normalized-relative-path>`。入口 role 固定为 `pi-bundle/cli.js`，当前入口为 629 bytes，SHA-256 为 `1c3a5094b54aae9ae98c66516ce8c6578140363d081471ca7e91f9cb8c23dc8a`。

该 profile 不枚举整个 12,528-file Pi package，不把 cache、log、auth、session、config、extension 或其它用户数据纳入闭包。literal/dynamic import、native addon、WASM、package `exports`/`main` 的目标只有在上述 roots 的保守全量 regular-file superset 内才可达；未知目标、解析逃出 root、symlink、socket、device、FIFO 或不能证明仍在声明 root 内的 dynamic/native 目标一律 `typed-unavailable`，不得静默扩 root 或缩减闭包。Core manifest 必须与 Adapter manifest 排序后逐字段精确相等；省略、多报、换 role 或未知字段都拒绝。

配置入口 symlink 只作诊断，不进入 authority identity，也不得用于 exec。原生 Qoder 使用 canonical native executable 作为 runtime，采用 Core-owned native profile，`MaterialRoots=[]`、`LaunchMaterials=[]`；空集合 digest 仍不能省略。

### 2. Held object identity、hardlink 与 FD admission

`RuntimeExecutableV1` 和每个 `LaunchMaterialV1` 都必须由 Core-owned launch coordinator nofollow 打开并持有只读 FD。每项身份至少绑定：

```text
canonicalPath, device, inode, fileType, mode, uid, gid,
size, linkCount, rawSHA256
```

只接受 regular file；`rawSHA256` 从 held FD 的原始 bytes 计算，hash 前后 `fstat` 的上述字段必须相同。runtime 必须至少有一个 executable bit、不得有 setuid/setgid bit，且 `linkCount == 1`。v1 对 materials 同样固定 `linkCount == 1`；任何 material hardlink、同一 `(device,inode)` 对应多个 path/role 或运行中 link count 增加都 `typed-unavailable`。这是初始保守结论，不宣称通用 hardlink provenance。

root name、role 和 relative path 只允许 printable ASCII bytes `0x20..0x7e`，禁止反斜杠、控制字符、DEL、绝对路径、空段、`.`、`..`、前后空格及未声明 root；分隔符仅为 `/`。路径字符串、`stat` 后再打开、版本输出、argv、shebang、mtime 或 Provider 自报均不能替代 held identity。

coordinator 维护只存在于 live process 的 `LaunchFDTableV1`：`runtime`、每个 material root 和每个 material role 精确映射一个 held FD；role、FD 或 object 一对多/多对一、FD 被关闭/替换、以及表与 Core manifest 不一致均拒绝。OS FD 数字不进入耐久 digest；FD 在相邻重验和 post-exec barrier 完成前不得释放。

profile admission 必须先读取当前 soft `RLIMIT_NOFILE`。固定 `CoreFDReserve=32`，`RequiredHeldFDs = 1 + len(MaterialRoots) + len(LaunchMaterials)`，且只有 `RequiredHeldFDs + CoreFDReserve < RLIMIT_NOFILE` 时才允许启用；否则在启动前 `typed-unavailable`，不得少持有、lazy hash 或关闭部分材料 FD。当前 Pi profile 为 `1 + 2 + 55 = 58` 个 held FDs，连同 reserve 为 90；2026-08-28 实测 soft limit 为 1,048,575。该实测不是未来 host 的 admission 证据，每次启动必须重读当前 limit。

### 3. 材料集合的 canonical digest

`LaunchMaterialV1` 是 closed object，字段固定为 `role` 加第 2 节全部 identity 字段。按 role 的 UTF-8 bytes 严格升序排列完整 records，对数组作 RFC 8785 JCS，再计算 `sha256:<lowercase-hex>`。空集合按 JCS `[]` 计算，固定为 `sha256:4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945`。

任何未知字段、非 canonical path、排序差异、role 冲突、manifest 增删或 identity 漂移都会改变或拒绝 digest。profile 固定材料数量与总 bytes；实测之外的增删或 bytes 变化只能由新 versioned profile 接纳，不能运行时自适应。

### 4. 同一 authority 的完整可恢复事实

在创建任何子进程前，Core 必须以同一 Attempt authority store 的 compare-and-swap 持久化 closed `launch-authorized` fact。该 fact 不得只存不可恢复 digest，必须完整包含：

```text
RuntimeExecutableV1
ClosureProfileID
MaterialRoots[]
LaunchMaterials[]
LaunchMaterialsDigest
AgentLaunchSpecDigest
```

fact 还绑定既有 Goal/Run/Attempt/Allocation/Command lineage、authority sequence 和预期前序 fact。restart/replay 只能读取这份完整事实重开对象，不得从 Adapter、配置 symlink 或当前安装重新发现并改写旧 manifest。

`process-started` 必须引用精确的 prior `launch-authorized` fact digest，并持久化描述真实 process image 的 `ProcessObservation`；它新增并持久化 `LaunchMaterialsDigest` 与 `AgentLaunchSpecDigest`，不能把 helper 或脚本当作 executable。完整 executable identity（包括 `ExecutableGID`）必须与 prior `RuntimeExecutableV1` 逐字段精确相等，两个 launch digest 也必须与 prior fact 精确相等；只比较一个不可恢复 digest 或由结果自报重建事实不构成门禁。

普通 ResultIngress 在最终接纳 Candidate/Result 前，必须持有 current Attempt authority 的锁定读取/CAS 视图，从 prior `launch-authorized` 取出完整事实，重新 nofollow 打开 runtime、roots 和全部 materials，重新枚举并计算完整 manifest、每项 identity、`LaunchMaterialsDigest` 与 `AgentLaunchSpecDigest`，再与 current `process-started` 精确比较。任何缺失、漂移、集合变化、authority sequence 变化或无法重开均为零接纳并 fence/intervention；不能使用 Result 携带的 Facts、旧 FD 表、Adapter 回显或仅 digest echo 代替 current-authority 重验。

### 5. Stable pathname exec、双阶段 barrier 与 crash 三分

Darwin ordinary-user 使用 `RuntimeExecutableV1.canonicalPath` 做 stable pathname exec，不使用 `/dev/fd/*`、随机临时 executable 或 shebang。`launch-authorized` CAS 成功后，Core 重新 `fstat` 全部 held FDs、逐项 nofollow 重开 current paths、重算 raw SHA 和 canonical manifest，并与该 fact 精确比较；随后只允许同一 transaction 执行已冻结 argv。

启动具备两个 fail-closed 阶段：

1. **pre-exec barrier**：`launch-authorized` 已耐久提交且相邻重验全等，才允许通过 stable canonical path 创建真实 Node；
2. **post-exec barrier**：真实 Node image exec 成功后，Provider entrypoint 必须仍处于可由 Darwin 内核状态和 Core-held launch control token 证明的 suspended 状态。Core 观察的必须是 Node，不是 Marshal helper、`pi` symlink 或脚本；只有 `process-started` CAS 成功并满足第 4 节精确相等，才可释放暂停，让 `cli.js` 获得第一次执行机会。

固定 Marshal helper 可以参与 pre-exec 协调，但 helper observation 永远不能写成 `process-started`。若当前 Darwin primitive 无法证明“真实 Node image 已装载且 Provider entrypoint 尚未运行”，该 profile `typed-unavailable`。

crash/error 处置固定三分，不得把三者混成 blind retry：

1. **`process-started` CAS 前、Core 仍存活且启动返回错误**：entrypoint 必须仍 suspended；Core 只通过现有 held child handle 确定性 terminate 并 wait，同一 Attempt 零 workload side effect；
2. **`process-started` CAS 前 Core crash**：entrypoint 必须仍 suspended；authority 中没有足够 kill 事实，恢复方只能 fence/intervention，禁止按 PID/path/进程扫描猜测 kill，禁止 resume 或创建 successor；
3. **`process-started` CAS 成功后、resume 前 Core crash**：authority 已完整保存 prior fact 与 exact `ProcessObservation`；恢复方先按 ADR 0056 精确重验同一 process birth/unit/runtime identity，再对同一 suspended child只执行一次 resume 或 cleanup。身份不全或重验不等时 fence/intervention，不能扫描或扩大 kill authority。

实现必须在 exec 后/观察前、观察后/CAS 前、CAS 失败、CAS 成功后/resume 前分别注入 crash 或错误，并断言：CAS 前 live error 会 terminate+wait；CAS 前 Core crash 只 fence；CAS 失败时 entrypoint sentinel 不存在；CAS 后 crash 恢复只产生一次 resume/cleanup 且不重复 `process-started`。

stable pathname exec 加 held-FD/path 相邻重验只适用于 trusted single-user Darwin ordinary-user，能检测已发生的 path swap/ABA，但不能证明同 UID 恶意替换者在最后一次重验与解释器读取 material 之间不存在 TOCTOU，也不提供 hardened containment。

### 6. argv、shebang、环境与恢复

Pi 的 argv 固定为 `[RuntimeExecutableV1.canonicalPath, entrypoint.canonicalPath, ...frozenArgs]`；shebang 只作为 material bytes 的一部分，不参与解释器或 PATH 选择。Qoder 的 argv 从其 native executable 开始，materials 为空。

启动环境从空环境构造，只加入 `AgentLaunchSpec.Environment` 的完整白名单值；不存在隐式继承。RuntimeExecutable 使用绝对路径，PATH 永不参与 runtime identity。Provider 若需要 PATH 搜索后续工具，PATH 必须作为冻结环境显式提供并受 ADR 0055 allowlist 约束；PATH 缺失时不能静默继承。

replay 或 Core restart 后，恢复方必须从 prior `launch-authorized` 读取完整 runtime、profile、roots、materials 和 digests，再 nofollow 重开并逐项重算。任何路径缺失、symlink、type/owner/mode/link-count/size/digest 变化、材料集合变化或 authority lineage 不一致均拒绝继续 launch、dispatch 或结果 admission。cleanup 是否可以 signal 仍只由 ADR 0056 的真实 PID birth/process-group/runtime image identity 决定，material drift 不产生新的 kill authority。

### 7. 必须通过的 hostile matrix

- 配置入口、runtime、root 或 material 的 symlink/path escape；swap/ABA、hardlink/link-count、type/mode/uid/gid/size/raw SHA 漂移；runtime 无 executable bit或带 setuid/setgid；
- Adapter manifest 省略/增加/换 role，未声明 root、非 printable ASCII、反斜杠/控制字符/路径歧义、role 重复、FD 关闭/替换/别名、FD budget 不足；
- Pi 0.84.3 manifest 不是 `pi-bundle` 48 files/7,422,432 bytes 加 `photon-node` 7 files/2,265,687 bytes，或 dynamic/native/WASM 目标逃出 roots；
- shebang/`/usr/bin/env`/PATH 注入、PATH 中另一 Node、argv 把 `cli.js` 放在首位、`ProcessObservation` 把 helper/脚本冒充 Node；
- `launch-authorized` 缺失完整 runtime/materials、prior `ExecutableGID`/materials/spec digest 与 `process-started` 不符、ResultIngress 只信旧 digest/Result Facts 或接纳前文件漂移；
- post-exec 暂停不可证明、CAS 前 live error 未 terminate+wait、CAS 前 Core crash 扫描/猜 kill、CAS 前 entrypoint 产生 sentinel、CAS 后 crash 导致重复 resume/cleanup/fact；
- replay/restart 后配置入口指向新版本、旧 manifest 被重写、Qoder 伪造非空 materials 或省略空集合 digest；
- 上述任一失败均零 Provider workload 放行、零结果接纳；cleanup/kill 仍须独立通过 ADR 0056 精确进程身份。

## 后果与退出门禁

- 最小实现边界是：Core 增加 Pi 0.84.3 two-root closure profile 和 deterministic enumerator；扩展 `LaunchPlan`/`AgentLaunchSpec` 与同一 authority fact 承载完整 runtime/roots/materials/digests；扩展 `ProcessObservation`；RB2 coordinator 实现 held FD table、FD admission、stable-path exec、双阶段 barrier 和故障 fixture；Pi adapter 只选择显式 Node 和提交待核对 manifest；Qoder 保持 `Materials=[]`；ResultIngress 在最终接纳前执行 current-authority 全量重验。
- 不新增 Provider 类型、包管理器、代码签名、安装收据或第二 authority store；ADR 0047/0048 的稳定发布门禁保持不变。
- 只有最终固定 `marshal` 产物从唯一 `ProductionRuntime` 运行真实 Pi，并证明真实 Node image、完整 materials digest、ResultIngress、独立 Verification/Decision、restart/CAS/ABA/PATH hostile matrix 全部通过，Pi 才能升级为 production reachable。ADR 被接受、组件测试或旧 fixed-bin canary 均不能单独升级成熟度。

## 拒绝的替代方案

### 继续直接执行 `pi` symlink 或 `cli.js`

拒绝。它依赖 shebang/PATH 选择未冻结的 Node，并把脚本身份误当成真实进程映像。

### 通过 `/dev/fd/*` 执行解释器或脚本

拒绝。它会产生不稳定 executable path，并在企业 Mac 环境触发陌生/匿名执行身份；held FD 应用于相邻重验，不替代 stable pathname exec。

### 枚举整个 package 或只 hash 入口

拒绝。前者把无关安装材料和潜在用户数据扩大为 12,528-file 权威闭包并耗尽可预期 FD 成本，后者漏掉 Provider 可达代码。Pi 0.84.3 只采用两个 versioned roots 的保守全量 regular-file superset。

### 把本合同扩展为包签名或 hardened 证明

拒绝。v1 当前只需关闭 trusted single-user Darwin 的误绑定与恢复歧义；签名/notarization 与 hostile same-user containment 有独立门禁。
