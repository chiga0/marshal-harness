# ADR 0077：Darwin local dogfood 的只读 TaskSpec 契约验证权限

| 字段 | 值 |
| --- | --- |
| 状态 | 已取代（Superseded by [ADR 0078](0078-verifier-builtin-task-spec-contract-gate.md)） |
| 日期 | 2026-09-03 |
| 接受依据 | 维护者依据 M13 dogfood Run `33709488741` 的真实 Verification 证据接受：8 项通过、`taskspec-validate` 以 `self-local-command-denied` 失败、2 项跳过；本 ADR 只修复已承诺的只读契约验证与 local-profile self gate 之间的冲突 |
| 决定者 | 维护者 |
| 关联 ADR | [ADR 0047](0047-marshal-darwin-self-identity-and-release-signing.md)、[ADR 0051](0051-darwin-local-dogfood-profile.md)、[ADR 0068](0068-mac-first-cli-only-lifecycle-preview-rc1.md)、[ADR 0073](0073-dogfood-activation-v2-host-portability.md)、[ADR 0075](0075-rc1-dogfood-usability-barriers.md) |
| 关联范围 | fixed `darwin-local-dogfood` Marshal 对 canonical repository 内一个 TaskSpec 文件执行精确、只读、无网络的契约验证；不授权其它 `contract` 形态 |

> 2026-09-03 设计复审确认：把 verifier 的 TaskSpec 契约门禁提升为 public CLI/self-admission 权限，会让候选路径进入 TaskSpec argv 与 `CommandRecord`，并把验证能力错误地绑定到 local-profile executable admission。ADR 0078 以 candidate-isolate 内的 pathless verifier builtin 完整取代本 ADR；本 ADR 保留为问题与拒绝方案的历史记录，不再授权实现。

## 背景

用户文档已经把 `marshal contract validate` 定义为只读命令，并给出以下 TaskSpec 验证用法：

```text
marshal contract validate --schema task-spec schemas/examples/happy-path/task-spec.json
```

但 `darwin-local-dogfood` 的 self gate 当前只接受封闭的生命周期 command class，没有与该只读用法对应的 command class。M13 walking-skeleton dogfood 因而出现确定性冲突：Worker、Drive 与另外 8 项 Verification 已通过，验收中的 `taskspec-validate` 调用同一 fixed candidate 时却在读取输入前返回 `self-local-command-denied`。这不是 TaskSpec 无效，也不是 Agent、网络或第三方依赖失败。

以下绕过均不可接受：

- 删除或跳过 `taskspec-validate`，会把真实契约合法性退化成 JSON syntax 检查；
- 用 Python 标准库手写 JSON Schema 子集，会与 Go Core 的 Draft 2020-12 与 semantic validation 语义漂移；
- 使用 `go run`、临时 helper 或每次新建的 checker，会生成匿名临时 Mach-O，并绕过 fixed executable 身份链；
- 清空 `MARSHAL_LOCAL_DOGFOOD_ACTIVATION` 不会关闭 built local profile 的 self gate，也不应成为授权手段；
- 把整个 `contract` family 加入 allowlist，会把 schema export、stdin、auto-detect 与未来子命令一并放入 trusted profile。

因此必须新增一个比公开 CLI 更窄的 closed command class：它只允许同一 fixed Marshal、同一 activation、同一 canonical repository 对一个明确的 repository-relative TaskSpec regular file执行现有 validator，不产生任何 effect。

## 决定

### 1. 唯一新增的 command class

新增 closed command class：

```text
contract.validate-task-spec-file-readonly
```

它只对应以下**精确 argv 形状**（不含 executable 自身）：

```text
contract validate --schema task-spec <repository-relative-path>
```

解析必须发生在打开输入文件、初始化 validator 或产生任何可观察 effect 之前，并同时满足：

1. argv 恰为 5 个元素，位置与字节值如上；
2. `--schema` 只出现一次且必须紧跟 `task-spec`；
3. 只接受一个位置参数，且不能为 `-`；
4. 不接受 flag permutation、`--schema=task-spec`、重复 flag、额外 flag、`--`、schema auto-detect 或其它 schema 名称；
5. `contract schema`、其它 `contract` 子命令以及未来新增形态继续返回 `self-local-command-denied`。

公开 CLI 在非 local-profile 环境中的兼容语法不受本 ADR 影响；上述限制只定义 `darwin-local-dogfood` self-admission 的新增权限。

### 2. repository-relative held-file 边界

`<repository-relative-path>` 必须是 canonical UTF-8 lexical relative path：非空，不以 `/` 开始，不含 NUL、空 segment、`.`、`..` 或反斜杠 segment separator。它只能相对于 activation 已绑定的 canonical repository root 解析，cwd、调用者环境、symlink alias 与其它 root 都不能参与解析。

实现必须从已验证且由 activation 精确绑定的 canonical repository root descriptor 出发，逐段以 nofollow、descriptor-relative 语义打开；最终对象必须同时满足：

- 位于同一 repository root 内；
- 是 current-name 仍指向同一 held object 的 regular file；
- 任一路径分段与最终对象都不是 symlink；
- 不是 directory、FIFO、socket、device 或其它特殊文件；
- 输入最多 `32 MiB`，在读取第 `32 MiB + 1` byte 时确定性拒绝；
- 打开前后的 held-object identity、size、modification/change time 必须一致；validator 只消费该次有界读取同时计算的 bytes，不得二次读取；path/object 或内容在打开、读取期间发生 drift 时 fail closed。

absolute path、`..` escape、symlink、FIFO 与 oversized file 均必须在 schema/semantic validation 前拒绝。validator 只能消费已接受的 held file，不得在校验阶段按 pathname 重新打开。

### 3. self identity 与 activation 绑定保持不变

该 command class 必须由现有 V2 activation 显式列入 `lifecycleCommandClasses`，并继续复用既有 self-admission 全链：fixed executable object、raw SHA-256、`sourceHead`、`selfProfile`、repository root、activation digest、current pathname/object observation 与 freshness 任一不一致都 fail closed。

本 ADR 不允许：

- 接受 `$PATH` 命中的另一颗 `marshal`；
- 从 TaskSpec、验收命令或环境提供 executable/root override；
- 用 command class 代替 executable identity 或 repository binding；
- 把本权限传播到 child process、Worker、Publisher 或其它 profile；
- 在 activation 缺失、过期、scope 不匹配或 identity drift 时降级执行。

### 4. effect 与输出边界

被授权路径只执行现有 `task-spec` Draft 2020-12 schema validation 与既有 semantic validation。它不得：

- 访问网络、解析远程 `$ref` 或加载 repository 外材料；
- 写入 `.marshal`、repository、cache、临时文件或用户配置；
- 创建/修改 Run、Attempt、owner、lease、ledger、worktree、review 或 publication 状态；
- 启动 Worker、Sandbox、Publisher 或其它 child process；
- 导出 schema、自动修复输入或改变输入 bytes。

成功 stdout 只能返回固定、无路径的 `contract-task-spec-valid`，stderr 为空，并以既有成功退出码结束。失败 stdout 为空，stderr 只能返回以下一个封闭 reason code，可附一个有界 JSON pointer；不得附底层 error string：

```text
contract-command-denied
contract-identity-denied
contract-input-denied
contract-input-too-large
contract-schema-invalid
contract-semantic-invalid
contract-internal-failure
```

stdout 上限为 `128 bytes`，stderr 上限为 `4096 bytes`。普通结果、Verification evidence、event 与日志不得包含输入 path、argv、environment、activation 内容、repository root、输入文档 bytes、schema bytes、secret 或未白名单化的底层 `os.PathError`。JSON pointer 只能由 schema property/index token构成，必须先做长度与字符集约束，且不得包含 offending value。

### 5. 失败分类与不升级声明

argv、path、object、size、identity 或 activation 不满足时，必须在 validator 执行前 fail closed；schema 或 semantic validation 失败必须保持真实非零结果，不得被包装成通过。所有失败均零持久化、零生命周期 mutation、零 publication effect。

接受本 ADR 只冻结一个 local dogfood read-only command class，不表示 M13 完成，不升级 I186 R2–R6 成熟度，不授权 stable、managed、notarized、server、Linux、remote 或 multi-user 能力。

## 强制负向矩阵

实现必须覆盖以下测试，且 reviewer 不得以静态字符串匹配代替行为证据：

1. **精确 argv**：唯一正例；顺序变化、duplicate `--schema`、`--schema=task-spec`、额外 flag/位置参数、`-`、auto-detect、其它 schema、`contract schema` 与未知子命令全部拒绝。
2. **路径与对象**：absolute、`..` escape、`.`/空 segment、symlink parent、symlink leaf、FIFO、directory、socket、device、超过 `32 MiB` 及打开后的 pathname/object drift 全部拒绝；合法 repository-relative regular file通过 path gate。
3. **契约语义**：合法 TaskSpec 通过；JSON syntax 错误、Draft schema 错误与现有 semantic rule 错误分别非零，证明不是 syntax-only 替代品。
4. **身份**：executable bytes、`sourceHead`、profile、activation、repository root、current pathname/object 与 freshness 任一漂移都拒绝；sibling repository 的同名文件不得被读取。
5. **零 effect**：通过与所有失败均不改变 repository、`.marshal`、Run ledger、worktree 与 process tree，不产生网络连接或 child process。
6. **保密与有界**：成功和各类失败的 result/evidence/diagnostic 不含 path、argv、env、activation、document value 或 secret；超长 parser/schema error被归一化并截断在固定上限内。
7. **M13 回归**：walking-skeleton acceptance 使用 direct argv，不再通过 `sh`、`cat`、pipe 或重定向；同一 fixed candidate 在 activation 下真实验证生成的 TaskSpec，非法 schema/semantic fixture仍使 required gate失败。

## 实施范围

后继实现应保持一个最小切片：

- 在 local dogfood command classifier 中加入上述 exact parser 与 closed command class；
- 在 V2 activation 生成/校验处显式加入该 class；
- 让 `contract validate` 的 local-profile 文件读取走 held repository-relative nofollow入口并输出安全诊断；
- 把 M13 acceptance 改为 direct argv；
- 添加本 ADR 负向矩阵所需的定向测试。

不得借此重构一般 contract API、扩张 M13 架构、引入新 validator、第三方依赖或临时 executable。

## 后果

- M13 与后续 dogfood 可以用同一 fixed candidate 对 Worker 生成的 TaskSpec 做真实契约验证，不再因 profile allowlist 与文档承诺冲突而确定性失败。
- 验证强度不降级：仍由 Core 的唯一 schema/semantic validator判断，且输入对象、executable 与 repository binding 比普通 CLI 更严格。
- local-profile 权限面只增加一个可枚举、可测试、无 effect 的 exact command class；任何其它 contract 形态继续 fail closed。
- 已发布 `v1.0.0-rc1` bytes 不回溯修改；该能力只属于后继 candidate。
