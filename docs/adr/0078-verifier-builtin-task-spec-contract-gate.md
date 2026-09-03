# ADR 0078：Verifier 内建的 pathless TaskSpec 契约门禁

| 字段 | 值 |
| --- | --- |
| 状态 | 已接受（Accepted） |
| 日期 | 2026-09-03 |
| 提议基线 | `main@d64d6e93a26ffd55175c31bca2e526eb1bf9095c` |
| 决定者 | 维护者 |
| 取代 ADR | [ADR 0077](0077-local-dogfood-read-only-task-spec-validation.md) |
| 关联 ADR | [ADR 0004](0004-independent-verification.md)、[ADR 0019](0019-deterministic-control-plane-typed-execution-and-goal-admission.md)、[ADR 0051](0051-darwin-local-dogfood-profile.md)、[ADR 0068](0068-mac-first-cli-only-lifecycle-preview-rc1.md)、[ADR 0075](0075-rc1-dogfood-usability-barriers.md) |
| 关联范围 | independent Verification 在 candidate isolate 内对一个 required deliverable 的 exact bytes 执行 Core TaskSpec schema/semantic validation；不新增 public CLI 或 self-admission 权限 |

## 背景

M13 walking-skeleton 需要把 Worker 产出的 TaskSpec 当作 required acceptance gate 验证。ADR 0077 试图把它实现为 fixed Marshal 的 public `contract validate --schema task-spec <path>` command class，并由 local-profile self gate 放行。设计复审确认这处置错了层：

- Verification 已经拥有 candidate isolate、deliverable inventory、命令规划、结果归一化与证据绑定；把同一能力绕回 public CLI 会产生第二条执行与授权路径；
- 用户控制的 candidate pathname会进入 argv、`CommandRecord`、日志与失败证据，扩大路径泄漏和替换窗口；
- public/self admission 解决的是 fixed Marshal 自身能否执行某个宿主命令，不应成为独立 verifier 读取候选 artifact 的 authority；
- 临时 helper、`go run` 或匿名 Mach-O 会引发本机 EDR/Gatekeeper 阻断，也会制造与 Core validator 漂移的第二实现。

所需能力实际是一个 verifier 内建 command：TaskSpec 只声明**要验证哪个 deliverable**，路径只由 current Candidate 和 isolate 内的 artifact inventory解析；验证结果仍由既有 Core 唯一 validator产生，并进入既有 VerificationReport/CommandRecord 证据链。

## 决定

### 1. 唯一 exact argv 与保留命名空间

本 ADR 只接受以下精确 argv（不含任何 executable 或 path）：

```json
["marshal-builtin:contract-task-spec:v1", "deliverable:<id>"]
```

其中 `<id>` 必须是 TaskSpec identifier 的 canonical bytes，`deliverable:` 前缀只出现一次，且解析后非空。argv 必须恰有两个元素；大小写、顺序、前缀、版本或元素数量任一不同均拒绝。

`marshal-builtin:` 是 verifier 永久保留的内部命名空间。只要 `argv[0]` 以该前缀开始，规划器就必须进入 closed builtin parser：未知名称、未知版本、畸形参数或未来尚未接受的 builtin 都 fail closed，绝不能回退到 shell、`PATH`、一般 command runner 或其它 Agent/Provider。普通不带该前缀的 acceptance command保持现有逐字节行为。

### 2. planning preflight 与 exact TaskSpec 约束

在 clone、worktree mutation、command launch、baseline capture、日志文件创建或其它 execution side effect 之前，planning preflight 必须同时证明：

1. command 的 `required` 精确为 `true`；
2. `cwd` 精确为 `.`；
3. `baselinePolicy` 显式或按 schema default 归一后精确为 `none`；builtin 不执行 baseline副本；
4. `<id>` 在 TaskSpec `deliverables` 中恰好命中一个对象，该对象的 `required` 精确为 `true`，并且存在非空 `pathGlob`；
5. 同一 command 不能引用零个、多个、optional 或无路径 deliverable；同一 required deliverable不能由多个 builtin command重复声明；
6. 本 gate 只消费 current Candidate inventory 中由该 `pathGlob` 匹配的**恰好一个** artifact。零匹配、多匹配、目录或 inventory/TaskSpec 歧义都在执行前拒绝。

这些是 verifier admission 约束，不修改 TaskSpec schema。既有 schema仍允许一般 command、optional deliverable、其它 cwd/baseline策略；它们只是不能冒充本 builtin。

### 3. candidate isolate 内的 held artifact 读取

artifact authority只来自 current Verification 已持有的 candidate isolate root与 current Candidate inventory，不来自 argv、cwd、环境、日志、调用者提供路径或 repository root别名。实现应复用现有 command isolation/clone/audit闭包，不得复制另一套 clone、scope、diff或artifact发现逻辑。

解析出的唯一 candidate-relative artifact必须在 isolate 内逐段 nofollow打开并持续持有：parent和leaf均需证明 current name仍指向同一 held object；leaf必须是 regular file、`LinkCount=1`，不得是symlink、directory、FIFO、socket、device或其它特殊对象。读取严格限制为`1 MiB`，以`limit+1`读法拒绝超限，不得先无界buffer再检查。

validator只消费该次 held读取取得的exact bytes。读取前后必须重验parent/leaf identity、size、mtime/ctime与current-name；rename、replace、symlink、truncation、growth或ABA任一不确定均fail closed。读取时同步计算SHA-256；最终artifact digest必须绑定exact consumed bytes、builtin revision、deliverable id及current Candidate/Verification evidence，不能由pathname或TaskSpec声明代替。

### 4. Core 唯一 validator 与 closed result

TaskSpec验证必须调用仓库现有Core Draft 2020-12 schema validator及semantic validator。不得增加Python子集、第二份schema逻辑、外部网络`$ref`、child process、临时helper或匿名binary。

成功输出归一为固定的`contract-task-spec-valid`。失败只能归入以下closed reason：

```text
contract-builtin-denied
contract-deliverable-denied
contract-artifact-denied
contract-artifact-too-large
contract-schema-invalid
contract-semantic-invalid
contract-timeout
contract-internal-failure
```

诊断不得包含artifact path、argv原文、cwd、repository/isolate root、document value、secret、底层`os.PathError`或validator自由文本。若需要JSON pointer，只能保留由已知schema property/index组成、规范化且有界的pointer；未知或用户控制的property token必须省略pointer，而非回显。stdout/stderr与现有command evidence上限继续生效，超限固定截断为closed结果而非泄漏底层内容。

### 5. pathless CommandRecord 与证据

builtin仍产生既有Verification command evidence，但`CommandRecord`必须保持pathless：记录canonical builtin marker、revision、command id、deliverable id、artifact digest、result/reason、开始/结束时间与既有evidence binding；不得记录解析出的artifact path、宿主 executable path、伪造的shell命令或临时binary身份。

VerificationReport的成功只能来自current ledger/current Candidate下重新完成的exact读取与Core验证。artifact digest、command result或Candidate evidence任一漂移都不能重放为成功。Worker不能提供、修改或自签该权威证据。

### 6. 平台与timeout诚实边界

首个实现只授权Darwin local candidate-isolate执行。Linux及其它平台看到reserved builtin必须确定性fail closed为`contract-builtin-denied`，不能执行外部同名程序、不能回退普通command，也不能伪造skip/pass。这不影响普通acceptance command在既有支持平台的行为。

builtin在Verifier进程内执行，因此现有外部process timeout不能硬中断一个CPU hang的Core validator。首个实现必须：

- 在读取前、读取后、Core调用前与返回后检查冻结deadline；
- 依靠`1 MiB`输入上限和closed schema/semantic实现约束正常工作量；
- deadline已过时返回`contract-timeout`，不得报告成功；
- 明确不声称对Core内部无限循环提供hard preemption。

若未来真实证据表明需要硬中断，必须以fixed、可识别的隔离进程另行ADR；不得恢复匿名helper或以goroutine超时后遗留继续运行的validator伪装成取消。

### 7. 零effect与不变范围

builtin不得访问网络、写repository或`.marshal`、创建临时文件、启动child process、改变Run/Attempt/lease/ledger/worktree/review/publication状态，或修改candidate bytes。通过、拒绝、schema失败、semantic失败、timeout与I/O不确定都必须保持上述零effect；只允许既有Verification evidence持久化路径在完整结果返回后记录closed command result。

本 ADR 明确不修改：

- `TaskSpec`或`VerificationReport` schema；
- selfidentity、activation或public `contract` CLI；
- Run/Attempt lifecycle、ResultIngress、ReviewDecision或publication authority；
- AgentProvider、SandboxProvider或Worker权限。

ADR 0077 完整标记为Superseded；其`contract.validate-task-spec-file-readonly` command class、32 MiB public CLI读取与activation扩张均不再授权实现。

## 强制验收矩阵

实现至少覆盖：

1. 真实`verification.New().Verify`正路径：required command以exact argv引用exact required deliverable，held读取、Core schema/semantic、artifact digest与pathless evidence全部成立；
2. reserved namespace no-fallback：未知builtin、版本/大小写/argv畸形、optional/baseline/cwd/required/deliverable不符均在普通runner与side effect前拒绝；
3. artifact边界：零/多匹配、absolute/escape、symlink parent/leaf、directory/FIFO/socket、超过1 MiB、parent/leaf rename/replace/current-name ABA、读取期间bytes drift全部拒绝；
4. validator语义：合法TaskSpec通过，JSON syntax、Draft schema及semantic错误分别失败，证明调用Core唯一validator；未知或用户控制property不进入diagnostic pointer；
5. digest/evidence：同path换bytes、同bytes换Candidate/evidence、伪造digest、陈旧result或path-bearing record不能成为成功；
6. 零effect与保密：所有路径均无child/network/repository/lifecycle/publication mutation，result/evidence不含path、document value、secret或底层错误；
7. 平台：Darwin执行；Linux及其它平台reserved builtin固定fail closed且不fallback；普通command在各平台保持既有byte-compatible结果；
8. M13回归：acceptance改为上述pathless builtin和exact deliverable id；required gate失败仍阻止Verification通过。

本机门禁不得通过反复执行新建Mach-O对抗EDR。允许以固定路径`go test -c -o`和race compile证明构建，并由受控macOS CI执行动态矩阵；Linux必须至少compile并验证reserved builtin fail-closed路径。

## 实施边界

后继实现分一个最小代码提交：

- 在现有Verification planning/execution边界加入reserved builtin parser与side-effect前preflight；
- 在现有candidate isolate闭包内加入held bounded artifact读取，调用现有Core validator并生成pathless closed result；
- 把M13 TaskSpec acceptance改为exact builtin argv和required deliverable id；
- 加入上述定向、race、zero-effect、secret与evidence测试。

不得借此重构一般command API、复制isolate/clone/audit逻辑、改变schema或扩大其它provider/lifecycle/publication范围。

## 后果

正面结果是TaskSpec契约门禁回到独立Verification所属层：用户TaskSpec只声明deliverable identity，candidate pathname不再成为命令或证据；Core validator保持唯一，reserved namespace不可能意外执行宿主程序，普通command不受影响。本地也不再需要匿名checker或给public Marshal增加验证权限。

代价是首个builtin只在Darwin执行，且in-process Core调用只能提供有界输入与deadline前后检查，不能声称hard-preemptive timeout。Linux stable若需要同等能力，应在稳定isolate与动态证据完成后显式扩展平台合同；hard timeout则需要另行设计fixed verifier process。

接受本 ADR 只冻结该 verifier builtin，不表示M13完成、R2–R6成熟度升级、fixed server可用或stable发布门禁关闭。

## 拒绝的替代方案

### 继续实现ADR 0077的public CLI/self-admission command class

拒绝。它把内部Verifier能力错误地扩张为public executable权限，并让path进入argv/evidence，重复了candidate isolate已经拥有的authority与审计闭包。

### 使用外部脚本、`go run`、临时helper或匿名Mach-O

拒绝。它引入第二validator或不稳定executable identity，既会语义漂移，也会触发本机EDR并破坏same-bytes证据。

### reserved builtin解析失败后按普通command执行

拒绝。这会让拼写错误或未来版本通过`PATH`命中攻击者控制的同名程序。命名空间一旦保留就必须永不fallback。

### goroutine返回timeout但允许Core在后台继续运行

拒绝。这不构成取消，会在Verification已经报告失败后留下无归属执行。首版诚实采用bounded input和deadline前后检查；hard preemption另行设计。
