# Admission 与 Acceptance

> **何时必须读取：** 新建或修改 TaskSpec、执行 `task plan`/`approve`/`run`、准备 successor、设计 acceptance/verifier、处理 planning/admission/`verifier-worktree-mutated`/零测试匹配，或交付自然语言内容时，必须在动作前完整读取。

## TaskSpec 与 plan

- `work.context` 必须自包含，Worker 看不到对话历史；scope 仅列必要路径，acceptance 命令按任务裁剪。constraints 固定写明：“若某操作被 permission 拒绝，不得重试该路径，改用允许路径内的等价输入”。
- 新 TaskSpec 必须先执行 `marshal task scaffold --draft DRAFT.json > TASK.json`，再完成 Schema admission，之后才能交给 `task plan`。Planning 只消费冻结后的显式 Adapter 顺序，不在运行时插入或重排。
- 通用 scaffold 的兼容默认是 `qoder → codex → qwen → pi`。使用 `templates/research-task.json` 时必须显式传 `--preferred-adapter ID`，默认不传 `--fallback-adapter`，把 fallback 冻结为空；只有当前 doctor/admission receipt 逐一证明候选 eligible 且用户明确需要 fallback 时才显式加入。scaffold 对 OpenCode 硬拒绝。
- `templates/research-task.json` 是单 Attempt、零 rework、无 fallback 的降耗起点，不得硬编码 WorkerResult 路径、shell 写入 primitive 或 Provider 私有 transport；这些只由 Adapter prompt 投影。
- `task run` 报“缺少当前有效 plan 审批”或等价错误是 Core admission 阻断，不是 Worker/Provider failure。先重建当前 plan/approval 并核对 digest，未修复前不得重复 `task run` 或计入 retry。

## 统一 plan phase preflight

正常 operator 工作流只使用 `marshal-fastpath-preflight.py --phase plan` 这一入口，不分别手工拼接 semantic acceptance 与 plan pre-mortem。先显式声明 `--task-kind content` 或 `--task-kind non-content`；content 必须提供 semantic manifest、正反 fixtures 与 clean linked worktree，non-content 必须省略 semantic manifest，并在 receipt 中留下 `status=not-applicable`、`reasonCode=non-content-task-declared`。Required `documentation`/`report` deliverable、`other`/`diagnostic` 的明确文本 `mediaType` 或 canonical content gate 被声明成 non-content 时固定以 `content-task-semantic-manifest-required` fail closed，不能静默跳过。

```bash
python3 -I -B .agents/skills/marshal/references/marshal-fastpath-preflight.py \
  --phase plan \
  --task-kind content \
  --root "$OPERATOR_ROOT" \
  --plan-manifest plan-premortem-preflight.json \
  --acceptance-manifest acceptance-semantic-manifest.json \
  --protected-root "$CLEAN_WORKTREE" \
  --marshal "$REPOSITORY_ROOT/bin/marshal"
```

统一入口对 content 先执行 semantic acceptance，再执行 plan pre-mortem；只有两者都 pass，且两份证据绑定同一 raw `taskSpecDigest` 与 `sourceHead`，才输出 `reasonCode=combined-plan-preflight-pass` 和 `combinedDigest`。semantic child 对每个 fixture 只读取一次，同一份 held bytes 同时用于语义判断、临时命令与 receipt 的 `semanticManifestDigest`/`fixtureAggregateDigest`；wrapper 复核 child 与外部前后证据，拒绝 ABA。任一 fixture bytes 变化都会产生不同 receipt。两个 child phase 分别使用有上限的 timeout；超时只终止本入口创建的进程组，grace 后检查并以 `SIGKILL` 清理仍存活成员，复核进程组消失，再稳定返回 `acceptance-semantic-timeout` 或 `plan-premortem-timeout`。任一 component failure 原样保留固定 `reasonCode` 与 `stage`，在 `task plan` 前止损。non-content 分支仍执行 plan pre-mortem，并把显式不适用裁决纳入同一个 `combinedDigest`。

此 combined receipt 仍是 operator-local 证据，不是 plan approval、Run admission 或 Core authority。本切片不修改 admission receipt、production validator 或 Schema；下一独立切片再评估 admission receipt 是否需要绑定 `combinedDigest`，如需扩大生产 validator/Schema，必须重新做治理与兼容门禁。

## 零 Attempt admission

`task run` 前逐项关闭以下条件；任何未知或失败都只记录 admission finding，不启动 Worker、不消费 Attempt/retry：

1. `task status` 精确为 `READY`，plan/approval 与当前 `specDigest`、`policyDigest`、`capabilityDigest` 相符。
2. doctor 证明所选 Adapter `configured=true`、`compatibility=supported`；普通用户 mode 显式且与真实 env/argv 一致。
3. source/base、worktree、scope 与单写入者绑定准确；并行 scope 不重叠。
4. watchdog 表明有容量、压力 `ok`，Provider 没有已观察到的背压。
5. acceptance 在 Worker 权限内可运行，结果路径确实可写，父目录/输入已存在，context 自包含，独立 verifier 可以执行。
6. required command 不写工作树，selector 至少匹配一个既有测试，内容型规则与 prompt 一一映射。

缺 plan approval、`configured=false`、缺失/陈旧 ReviewPacket、结构性 Adapter failure 必须使用不同 finding 类别；不得把 admission finding 伪装成 Worker/provider failure，也不得靠新 Run 清除。

## Operator-local admission observation（可选诊断）

Core 的 `task run` 是唯一 admission authority。普通 Mac fast path 不要求 operator receipt 才能运行；scope 独占、acceptance purity 与 result-path 可写性仍必须由本章前述独立 preflight 关闭。需要定位路径、版本或容量漂移时，可以使用固定 Python 源文件生成并即时验证短寿命 operator-local observation，禁止手填动态字段：

```bash
python3 -I -B .agents/skills/marshal/references/create-admission-receipt.py \
  --operator-root OPERATOR_ROOT \
  --receipt admission-receipt.json \
  --run-root RUN_ROOT \
  --workspace-root WORKSPACE_ROOT \
  --adapter-id ADAPTER_ID \
  --adapter-mode ADAPTER_MODE
```

生成命令本身必须由 `env -i` 启动，只注入 `HOME`、`PATH`、`TMPDIR`、`LANG`、`LC_ALL`、`MARSHAL_WATCH_NOTIFY=0`、`MARSHAL_WATCH_COHORT_FILE`、所选 Adapter path，以及 Qoder/Codex 各自所需的 mode/config；不得带入 OpenCode、未选 Adapter 的 path/mode 或跨 Adapter config。Pi/Qwen 不得携带 authority config，Qoder/Codex 只能携带本 Adapter allowlist 内的 config。`PATH` 必须包含所选 launcher 的精确 runtime 目录（例如 Pi 的 Node 目录），否则 doctor probe 会在 Worker 启动前确定性失败。生成器自动收集所选 Adapter 的精确 allowlist，缺任一必需键或存在任何 governed env 污染都会 fail closed，不能由调用者漏报 `--launch-key`。真正执行 `doctor` 与 `task run` 时必须显式复用同一个 `env -i` 集合和值。

生成器与 validator 复用同一份采样和投影实现，写入 observation 后立即再次完整验证；exit 0、`status=pass` 且 `reasonCode=operator-receipt-created-and-valid` 只表示同一次普通宿主采样内部一致，不能授权、阻止或替代 Core `task run`。输出必须是 `.marshal`、run root、state/control、任务 worktree、workspace、固定 `bin/marshal`、watch script、Marshal Skill 与输入之外的全新 0600 regular file；生成器在任何动态 probe 前拒绝既有目标，并持有 nofollow parent identity，以 no-clobber 方式发布，绝不替换 symlink 或既有文件。生成器不改变 Core state、不启动 Worker、不记录 launch env value。`pi`/`qwen` 没有 authority mode env，必须使用 `--adapter-mode host-user`；Qoder/Codex Mac 普通用户 profile 使用 `ordinary-user`。`host-user` 只陈述普通宿主进程事实，不得解释为 delegated authority、hardened sandbox 或 Linux authority；v3 不接受含混的 `strict` 自报模式。

Observation 不是 `marshal.dev` contract，不得写入 `.marshal` 或冒充 Core authority。它仅记录：

- 当前 source/spec/policy/capability/base/state/approval；
- host OS/arch、精确 Adapter executable path/digest/device/inode；
- worktree HEAD/clean status；
- doctor、capacity、backpressure 的稳定 admission 投影摘要；
- 精确 plan `ApprovalRecord`、Core state/control 相对路径、机械 validator 使用的 Marshal/Watch 工具 identity；
- 显式 launch env 的排序 key allowlist 和 canonical key/value digest。0600 observation 为了复核 identity 必然包含 canonical Adapter/worktree 路径；stdout、validator 输出和日志不得回显这些路径、env value 或 secret。

时间字段统一使用严格 RFC3339 UTC `Z`（秒后只允许 1–9 位小数），有效期最多 60 秒。生成器在实际写入前重检 TTL；validator 在初读、doctor/watch 动态 probe 后、最终 fd/bytes/identity 复核后各重检一次。device/inode 在 Schema、Python 与 `jq` 中统一为非负整数。`jq` 只做低成本形状 lint，不提供 admission：

```bash
jq -e -f .agents/skills/marshal/references/validate-admission-receipt.jq RECEIPT.json
```

需要诊断动态漂移时，相邻 Python validator 在同一显式 env 下重新执行 fresh `doctor --run` 和一次 watchdog 采样，并在命令前后复核 state/control/observation identity：

```bash
python3 -I -B .agents/skills/marshal/references/validate-admission-receipt.py \
  --operator-root OPERATOR_ROOT \
  --receipt admission-receipt.json \
  --run-root RUN_ROOT \
  --workspace-root WORKSPACE_ROOT
```

exit 0、`status=pass` 且 `reasonCode=operator-receipt-valid` 仍只是诊断结论。Validator 逐级 nofollow、有界读取并重算 Adapter executable device/inode/raw digest、worktree HEAD/clean/status、READY state、plan approval binding、host identity；Adapter/Marshal/Watch regular file 与 worktree directory 的 exact fd 持有到动态命令结束，再比较 device/inode/size/mtime/raw digest，worktree还会重采 HEAD 与 porcelain-z status。fresh doctor 必须观察到 `configured=true`、`registered=true`、`compatibility=supported`、精确 `authorityMode`/binary identity，watchdog 必须观察到 `pressure/cpu/provider/queueSignalStatus=ok`、`slotsAvailable>=1` 且所选 Adapter `status=available`。`dynamicEvidence` 的 digest 只绑定稳定分类投影，不绑定自然波动的原始内存/load 数值。

普通用户进程无法在不引入 OS/Core authority 的前提下证明 hostile pathname ABA 永远没有发生，因此该 observation 不得被升级成最终 gate。正常 fast path 在独立 preflight 通过后，直接以同一显式 `env -i` 依次运行固定稳定路径的 `bin/marshal doctor --run ... --json` 与一次 `bin/marshal task run ...`；Core 在 Attempt admission 时重新校验当前 state、approval 与 Adapter。任何 observation failure 只用于修复输入，不产生 Core 状态，也不得把结构性错误伪装成 Provider failure。

任一 tuple、sequence、digest、工具 identity、时效、容量、背压或 state/control identity 改变会使 observation fail closed；固定 `reasonCode` 原样保存。复用的是诊断摘要，不是 Core 状态副作用。scope 独占、acceptance purity 等门禁必须独立完成；Python validator 不接收这些无法自行机械证明的自报字段。旧 v2 receipt 因包含自报 evidence 且容易形成伪 authority 已退役，不得恢复。

## 单次 plan pre-mortem

在 `task plan` 前，对拟使用的 TaskSpec、PolicySnapshot、锁定 `sourceHead` 和所选 Adapter 只运行一次 operator-local pre-mortem。它直接复用 Core 的 TaskSpec/PolicySnapshot/CapabilitySnapshot Schema、`ValidateTaskSpecAcceptanceFloor`、`ValidatePolicy`、`WorkerRuntime.Selector.Probe` 与 `ValidateCapability`；只执行 Adapter 的 version/capability probe，不调用 `Worker.Run`、不创建 Attempt、不读写 `.marshal`，也不产生 Core authority。

从 `templates/plan-premortem-preflight.json` 复制 manifest，把 TaskSpec 与 PolicySnapshot 放入同一紧凑 operator root，填入两份文件的原始 `sha256:` 摘要、锁定的 40 位 commit、`runId` 和本次唯一所选 Adapter。operator root 必须在 `.marshal` 外，所有绑定路径必须是无 symlink 的相对 regular file。正常 plan 由上面的统一入口调用本 component；仅开发或诊断 validator 时才单独运行：

```bash
OPERATOR_ROOT="$(cd "$(mktemp -d)" && pwd -P)"
python3 -I -B .agents/skills/marshal/references/validate-plan-premortem-preflight.py \
  --root "$OPERATOR_ROOT" \
  --manifest plan-premortem-preflight.json \
  --marshal "$REPOSITORY_ROOT/bin/marshal"
```

只有 exit 0、`status=pass` 且 `reasonCode=plan-premortem-pass` 才继续 plan。pass receipt 绑定 TaskSpec/PolicySnapshot 原始摘要、`sourceHead`、所选 Adapter、`authorityMode` 与 capability JCS 摘要；它仍不是 plan approval 或 Run admission receipt。任一失败必须在启动 Worker 前止损，原样保留固定 `reasonCode`，修正输入后才允许对新摘要再执行一次：

- `acceptance-required-command-missing`：TaskSpec 或 Policy 的 required acceptance floor 为空、没有 required command 或 argv 无效；
- `policy-approval-gates-conflict`：Policy 的 approval gates 与控制语义冲突；
- `policy-publication-merge-conflict`：publication/merge 开关、provider、method 或 required checks 不一致；
- `adapter-ordinary-user-execution-profile-unsupported`：所选普通用户 Adapter 不支持 TaskSpec 的 `executionProfile`；不得把 ordinary-user 能力升级描述成 delegated authority；
- `adapter-named-worker-tools-unsupported`：所选 Qoder/Codex 的已验证 argv 无法表达非空 `worker.tools`；缺省或显式空数组可继续，named allowlist 必须先从 TaskSpec 移除或改选具备已验证映射的 Adapter；
- `qoder-deliverable-parent-missing`：Qoder required path deliverable 的父目录在锁定 Git tree 中不存在；不得为此单独提交空目录或骨架。改选已经实证能创建父目录的 Adapter 完成当前纵切；若必须先改变基线，则该前置提交必须把父目录与同一条完整纵切的真实文件/接线一起交付，后续 Qoder 再锁定新 base。preflight 在输入改变前继续 fail closed，不把结构性错误转成 Worker rework。

其它 contract、路径、摘要、Adapter 配置/选择或 capability 失败也以稳定 `reasonCode` fail closed。wrapper 逐级 nofollow、有界读取并复核输入 fd identity，把已持有的精确字节复制到私有临时目录后调用固定 `bin/marshal internal plan-premortem-check`；临时目录只承载输入文件，不承载或执行任何二进制。输出不包含 executable、仓库、输入文件或临时目录路径。该工具是减少确定性 rework 的前置过滤器，不能替代后续 doctor、admission、独立 reviewer 或 Core 生命周期命令。

## Acceptance purity

- plan 前做 purity lint：保守拒绝 shell wrapper/重定向、会在 worktree 生成 cache/profile/coverage 的命令，以及没有逐字使用 `python3 -I -B -c` 的 Python 内容验收。
- 无法静态证明纯只读时，用 verifier 的真实 argv/env/cwd 在临时副本 dry-run，比较前后树摘要；普通宿主副作用探测不是恶意代码 sandbox。
- `verifier-worktree-mutated` 即使命令退出 0 也是结构性 Required Gate failure。先隔离 cache/temp 或修 acceptance，再由 Core/CLI 建 fresh-base successor；不得归因成 Worker rework。
- acceptance 故障注入必须位于它声称验证的 effect/cache/persist 边界之后，并断言相同 key、相同 outcome、effect exactly-once。副作用前断开只能证明普通首次执行，不能关闭 replay/idempotency finding。

## 内容型 acceptance semantic preflight

从 verifier 机械抽取 `required_all`、`required_any`、`forbidden`、精确路径、最小数量/行数、最大 UTF-8 字节数和 normalizer，并与 `work.context` 的输出要求逐项一一映射。

- 自然语言默认使用 `casefold` 后的稳定 token 和显式等价词组；只有协议字段、命令、路径等确需逐字时才用单一 literal，并在 Worker prompt 明写“逐字包含 `<literal>`”。
- 多种表述可接受时用 `required_any`；已知错误术语、过度声明和自相矛盾句进入 `forbidden`，prompt 同时解释正确替代语义。
- Required Gate 已通过后 reviewer 不得追加 TaskSpec 中不存在的字面要求。无法建立 verifier ↔ context 一一映射时不得 plan。

从 `templates/acceptance-semantic-manifest.json` 生成 operator-local manifest；它不是 Core contract。先验证完整 TaskSpec：

```bash
./bin/marshal contract validate --schema task-spec TASK.json
```

把 manifest、TaskSpec、fixtures 放入紧凑的 `FIXTURE_ROOT`；从锁定 `SOURCE_HEAD` 创建无 `.marshal`、无未提交文件的 detached/linked `CLEAN_WORKTREE`。正常 plan 由统一入口调用本 component；仅开发或诊断 validator 时才单独执行：

```bash
python3 -B .agents/skills/marshal/references/validate-acceptance-semantic-preflight.py \
  --root FIXTURE_ROOT \
  --manifest MANIFEST.json \
  --task-spec TASK.json \
  --protected-root CLEAN_WORKTREE \
  --source-head SOURCE_HEAD
```

禁止把 live repo root、`.marshal` 或 primary `.git` 作为 root/protected-root。每个 protected root 顶层必须有 regular nofollow `.git` linked-worktree marker，并独立满足 exact HEAD/clean；子目录不能借用其它 root 的绑定。

Validator 的 machine truth 由相邻 Draft 2020-12 Schema 和实现定义，不在本文复制。Operator 必须确认其绑定 TaskSpec 原始摘要、required command 完整 canonical tuple（`id/argv/cwd/timeoutSeconds/required=true/baselinePolicy/maxLogBytes`）、相对 cwd/精确 deliverable、最小数量/行数/最大字节数、封闭 normalizer、所有内容规则、逐字 context 映射及每个 fixture 原始摘要。

它必须拒绝 `.marshal`、symlink、路径逃逸、绝对路径、`..`、Python startup/import 保留名、未知/额外 AST 语句、normalizer drift、规则遗漏/额外项和受保护树替换/增长；对受保护树有 entry/byte hard limit，并以逐级 nofollow dirfd、`fstat` identity/size 复核和有界分块读取拒绝枚举后替换/增长。Fixture 使用绑定的真实 cwd/timeout 在临时副本运行。误传大树固定 fail closed，不重复扫描运行态。任一失败原样保留固定 `reasonCode`，先修 TaskSpec/acceptance，不启动 Worker。

## 正反 fixture

内容型 verifier 必须在不写目标 worktree 的临时样本中证明：

- 代表性正确输出通过；
- 分别缺少一个 `required_all`、缺少一个 `required_any` 组、命中一个 `forbidden`、低于最小值或超过大小边界时失败；
- 执行前后 fixture root 和 protected clean worktree 摘要相同；isolated Python 不加载本地模块遮蔽 canary。

最近的语义等价正确报告可复制到临时目录作为 positive fixture，禁止为验证 acceptance 再启动 Worker。代码型 verifier 使用任务自己的正反测试，不强套报告 fixture。

## 代码型 acceptance

- 优先断言可观察行为、错误分类、状态转换或协议输出。测试函数名、未导出 helper、局部变量、注释措辞默认不是契约。
- 稳定符号确是外部契约时，在 `work.objective/context` 明写“逐字使用 `<symbol>`”并由 preflight 一一映射；否则用包级行为测试或接受语义等价符号集合。
- 使用 `go test -run` 前先 `go test -list` 枚举并证明 selector 至少匹配一个派发前已存在的测试，再证明实际执行数非零；退出 0 的零匹配不算通过。
- 扫描 Python/grep 类源码 gate 中的 token；未映射的非契约 token 是 TaskSpec defect，先修 acceptance，不得转为 Worker rework。

## Admission 失败的止损

TaskSpec/acceptance/verifier、路径、identity、protocol、version、旧 base/artifact 或证据变化属于结构性问题。同一稳定输入摘要和 `reasonCode` 只裁决一次：保留证据、修 operator/Core/Adapter 输入，然后从当前权威 main 通过 Core/CLI 建 successor。只有 Core 持久化的 typed transient provider failure 且 admission 仍匹配时，才按 Policy 做有限 operational retry。
