# 开源反向审计：首次贡献者上手体验（从 clone 到第一个 PR）

- 审计日期：2026-08-07
- 审计视角：首次贡献者（无内部知识、无维护者会话历史）
- 审计方式：只读分析本仓库全部文档、Schema 与代码；离线真实执行了 `go run ./cmd/marshal version --json`、裸命令 usage 输出与 `marshal doctor --json`（均 `GOPROXY=off`，模块缓存命中，退出码 0），未使用网络、未修改审计目标以外的任何文件。

## 摘要

仓库的内部工程质量很高：状态机、契约 Schema、退出码都有文档与测试背书；非测试代码约 16.9k 行，测试约 17.7k 行（测试:代码 ≈ 1.05），130 个 Go 文件。CI 固定 Action SHA、权限最小化、带独立 Secret Scan job（`.github/workflows/ci.yml:9-10,27,30,58`）。但作为开源项目，"从零 clone 到第一个 PR"的路径目前断在三个地方：

1. **法律入口缺失**：仓库根目录没有 `LICENSE` 文件。默认版权保留下，外部贡献者既无权使用也无权提交代码，任何 PR 流程都无从谈起。
2. **5 分钟快速开始缺失**：`README.md:1-102` 没有任何一条可执行命令，也没有前置依赖、Go 版本或构建说明；首次贡献者必须自行发现两层之外的 `docs/development.md:41-52` 才知道 `make build` 的存在。
3. **贡献流程没有单一入口**：无 `CONTRIBUTING.md`；分支/测试/提交规范/CI 预期分散在 `docs/development.md`、`docs/operator-runbook.md`、`AGENTS.md`（后者实际面向 AI Agent）与 CI 配置中，且部分 CLI 文档已落后于代码。

**走查结论**：一名具备 Go 经验的首次贡献者，按当前仓库公开信息，从 clone 到跑通第一条只读命令需要自行完成"发现 development.md → 拼出 make build → 猜测 doctor 是首命令"三步无引导探索；从跑通到理解"如何提交一个合规 PR"则没有单一入口。若按本报告清单修复，该路径可压缩为"5 分钟跑通 + 一份贡献指南"。

**结论**：核心代码与门禁体系已达到可接待贡献者的质量，但接待入口（LICENSE、快速开始、贡献指南）尚未修建。

## 发现

按优先级排列。每条给出证据位置（`文件:行号` 均可直接核对）与影响分析。

### 走查模拟：当前首次贡献者的真实路径

记录本次反演路径，作为后续修复的对照基线：

1. clone 后打开 `README.md`：读到动机、模式、不变量、生命周期与 23 条文档链接（`README.md:54-78`），无一条命令。第 1–5 分钟无进展。
2. 凭经验打开 `Makefile`：确认 `build`/`test`/`check` 目标存在（`Makefile:26-37`），但无 Go 版本线索、无"先跑什么"的顺序提示。
3. 打开 `go.mod:3-5`：得知需要 `go 1.26.0`（toolchain `1.26.5`）；若本机 Go 较旧，是否自动下载、失败怎么办，无文档说明。
4. 找到 `docs/development.md:41-52`：得到 make 命令清单；`:58-84` 给出 CLI 用法与两条 `go run` 示例命令——这是全仓库唯一"可直接复制的首命令"，但已位于第二层文档。
5. 执行示例命令成功（本次实测 `version --json` 与 `doctor --json` 离线可用），此时才知道三个 Worker Adapter 都未配置（`doctor` 输出 `workerAdapters: 0`）。
6. 想跑一个真实任务：需要读 `docs/operator-runbook.md:18-29` 的标准循环，再去 `:136-143` 配置 5 个环境变量——入口分散在 260 行长文档的不同小节。
7. 想提 PR：找不到 CONTRIBUTING、CODEOWNERS、PR 模板；`make check` 全量约 15 分钟的预期也没有写在开发指南里。

### P0（阻断外部贡献）

- **F1 无 LICENSE 文件**
  - 证据：仓库根目录只有 `AGENTS.md`、`Makefile`、`README.md`、`go.mod`、`go.sum`、`cmd/`、`docs/`、`internal/`、`schemas/`、`.github/`、`.gitignore`，无 `LICENSE`/`COPYING`。
  - 影响：开源项目在无许可证声明时默认"保留所有权利"。外部贡献者无法合法 fork、修改或提交；对严肃的开源采用而言这是硬性阻断，优先级高于一切文档打磨。
  - 备注：Go 依赖经 `go.mod` 声明了各自许可证（间接依赖），但本仓库自身授权未声明。

- **F2 README 无快速开始，全文零命令**
  - 证据：`README.md:1-102` 含动机（`:7-18`）、运行模式（`:20-28`）、不变量（`:30-39`）、生命周期图（`:41-52`）、文档导航（`:54-78`）、MVP 计划与实施门禁（`:82-102`），但没有任何安装/构建/运行命令，也没有前置依赖与 Go 版本说明。
  - 影响：5 分钟上手不成立。首次贡献者必须离开 README 才能知道"这个项目能跑起来"。对比之下 `docs/development.md:82-84` 已有两条可直接上提的示例命令。
  - 影响范围：README 是克隆后唯一默认阅读入口，其缺失会放大其后所有文档的可达性问题。

### P1（显著拖慢上手）

- **F3 无 CONTRIBUTING.md，贡献流程散落多处**
  - 证据：首次贡献者需要回答的每个问题目前都无单一答案来源：本地命令与质量门禁在 `docs/development.md:41-56`；TaskSpec 纪律与实操经验在 `docs/operator-runbook.md:161-166`；CI 实际执行内容见 `.github/workflows/ci.yml`（Linux/macOS 双平台 `make check` + `make vuln` + Gitleaks）；根目录 `AGENTS.md` 面向 Marshal 自托管的 AI Agent 场景。
  - 影响："分支怎么建？测试跑到什么程度？提交信息什么格式？CI 会检查什么？审查看什么？"五个基本问题都没有入口。`AGENTS.md` 的存在还会让人类贡献者误入 Agent 协作约定。
  - 备注：仓库自身用 Marshal 流程产出变更（Draft PR、merge 默认禁用，见 `README.md:95-98` 与 ADR 0007），外部贡献者是否走同一路径，文档未回答。

- **F4 CLI 文档与代码不同步**
  - 证据：以 `internal/cli/cli.go:1443-1462` 的 usage 文本为真源，`docs/development.md:58-75` 缺失 `doctor` 的 `--run RUN_ID`/`--repair`、`task approve` 的 `--actor ID`、`task run` 的 `--through-verify`；`task abort` 完全未列入（已实现：`internal/cli/cli.go:490-504`，ADR 0012，提交 `08c8462`）。
  - 影响：文档不再是可信真源。首次贡献者若以 development.md 为准，会漏掉崩溃恢复所需的 `doctor --run --repair` 与显式终止死 Run 的 `task abort`。

- **F5 Schema 数量表述过时**
  - 证据：`docs/development.md:90` 称"15 份 Schema 全部可以编译"；`ls schemas/*.schema.json` 实为 17 份；`marshal doctor --json` 实测输出 `contractSchemas: 17`。
  - 影响：数字型断言过时是"文档腐烂"的典型信号，会连带削弱读者对 development.md 其余内容的信任。

- **F6 Operator Runbook 称 `task abort` "开发中"**
  - 证据：`docs/operator-runbook.md:106` 写"先经 `task abort`（开发中）转终态"；实现已在 `internal/cli/cli.go:490-504`（仅允许从 `RETRY_PENDING` 显式终止）。
  - 影响：照抄 runbook 的贡献者会放弃使用已存在的命令，转而手工 `git worktree remove` 之类危险操作——而 runbook 第 100 行恰恰警告不要用手工删除。

### P2（中等摩擦与隐性知识）

- **F7 Go 版本要求只在两层文档里**
  - 证据：`go.mod:3-5`（`go 1.26.0`、`toolchain go1.26.5`）与 `docs/development.md:9-15`；README 未提。
  - 影响：Go 1.26 是较新版本。旧 Go 的 toolchain 自动下载需要网络，受限环境首次构建直接失败；新人拿到的是工具链报错而非项目引导。

- **F8 usage 帮助遗漏两个写相关子命令**
  - 证据：`internal/cli/cli.go:1443-1462` 列出 plan/approve/status/run/verify/review/publish/accept，随后以泛化的 `marshal task <COMMAND>` 收尾（`:1459`）；`task cleanup`（`internal/cli/cli.go:436-443`）与 `task abort`（`:490-504`）不在清单中。
  - 影响：cleanup 是安全回收 worktree 的唯一推荐路径，abort 是死 Run 的唯一出口；两者都是新人迟早需要、却最不可能自行发现的命令。

- **F9 Live 测试环境变量只有内部人知道**
  - 证据（均为代码中唯一出处，`docs/` 全库 grep 无命中）：`MARSHAL_LIVE_GITHUB` 与 `MARSHAL_LIVE_GITHUB_FIXED_SUFFIX`（`internal/publisher/github/live_test.go:22-44`）、`MARSHAL_LIVE_OPENCODE_PATH`（`internal/adapter/opencode/live_test.go:19`）、`MARSHAL_LIVE_QWEN_PATH`（`internal/adapter/qwen/live_test.go:19`）、`MARSHAL_LIVE_PI_PATH`（`internal/adapter/pi/live_test.go:20`）。只有 `MARSHAL_LIVE_CMUX`/`MARSHAL_CMUX_PATH` 出现在 `docs/operator-runbook.md:85-86`。
  - 影响：贡献者无法复现 Live E2E；同时可能误以为"PR 里没跑 live 测试 = CI 不完整"，实际上这些测试按设计默认跳过。

- **F10 全量门禁耗时预期未写入开发指南**
  - 证据："全量 `make check` 在本仓库约 15 分钟"只在 `docs/operator-runbook.md:164`；`docs/development.md` 未提示。
  - 影响：首次贡献者跑 `make check` 要么误判卡死而中断，要么反复怀疑环境。一句"约 15 分钟，可先跑 `make test`"即可消除。

- **F11 doctor 输出与阶段表述手工维护**
  - 证据：`internal/cli/cli.go:198` 硬编码 `Milestone: "6"`；README（`README.md:5`）、`docs/development.md:5` 各自手工声明阶段。
  - 影响：三处目前恰好一致，但无测试约束；阶段推进时若漏改会给新人矛盾信号。

### P3（体验打磨）

- **F12 领域类型缺 doc 注释，godoc 友好度低**
  - 证据：`internal/domain/identity.go:10-24`、`internal/domain/review.go:5-84`、`internal/domain/run.go:7-60`、`internal/domain/task.go:3-37` 的导出类型/函数基本无 doc 注释；包级注释较好（如 `internal/cli/cli.go:1`）。
  - 影响：`internal/` 不对外导出，godoc 的对外价值有限；但 domain 是理解生命周期与证据模型的核心层，新人缺少原地注解时需反复跳转 `docs/task-lifecycle.md`。

- **F13 无 Issue/PR 模板与 community profile**
  - 证据：`.github/` 下仅 `workflows/`；无 `ISSUE_TEMPLATE/`、`PULL_REQUEST_TEMPLATE.md`、`CODEOWNERS`。
  - 影响：Issue/PR 无格式引导；审查责任对贡献者不可见。

- **F14 工具链新特性未解释**
  - 证据：`Makefile:23-24,32-33` 的 `go tool staticcheck`/`go tool govulncheck` 依赖 `go.mod:28-30` 的 `tool` 指令（Go 1.24+ 特性）；`docs/development.md:14` 只写了 staticcheck 名字。
  - 影响：按旧习惯 `go install honnef.co/go/tools/...` 的贡献者会得到与仓库固定版本不一致的 linter 结果，误报/漏报都难以归因。

### 隐性知识清单：全部 MARSHAL_* 环境变量盘点

首次贡献者迟早会撞上的环境变量如下。文档覆盖率 4/11，未覆盖的 7 个全部属于"只有读代码才知道"：

| 变量 | 代码出处 | 用途 | 文档覆盖 |
| --- | --- | --- | --- |
| `MARSHAL_STATE_DIR` | `internal/repository/state.go:29-44` | 覆盖默认 `.marshal/` 状态目录（必须绝对路径；仓库内则必须等于默认目录） | 有：`docs/architecture.md:75`、`docs/failure-and-recovery.md:7` |
| `MARSHAL_OPENCODE_PATH` | `internal/app/workers.go:44` | OpenCode Worker 可执行文件绝对路径 | 有：`docs/operator-runbook.md:137`、`docs/worker-adapters.md:100` |
| `MARSHAL_QWEN_PATH` | `internal/app/workers.go:51` | Qwen Code Worker 可执行文件绝对路径 | 有：`docs/operator-runbook.md:138` |
| `MARSHAL_PI_PATH` | `internal/app/workers.go:58` | Pi Worker 可执行文件绝对路径 | 有：`docs/operator-runbook.md:139` |
| `MARSHAL_GH_PATH` | `internal/cli/cli.go:765` | Publisher 的 `gh` 可执行文件绝对路径 | 有：`docs/operator-runbook.md:141` |
| `MARSHAL_GH_CONFIG_DIR` | `internal/cli/cli.go:765` | Publisher 独立凭据目录（不复用 ambient 配置） | 有：`docs/operator-runbook.md:142` |
| `MARSHAL_CMUX_PATH` | `internal/terminal/cmux_live_test.go:25` | cmux 可执行文件路径（受监督 Pilot） | 有：`docs/operator-runbook.md:86` |
| `MARSHAL_LIVE_CMUX` | `internal/terminal/cmux_live_test.go:22` | 启用 cmux Live E2E | 有：`docs/operator-runbook.md:85` |
| `MARSHAL_LIVE_GITHUB` / `_FIXED_SUFFIX` | `internal/publisher/github/live_test.go:22-44` | 启用 GitHub Publisher Live 测试 | 无 |
| `MARSHAL_LIVE_OPENCODE_PATH` | `internal/adapter/opencode/live_test.go:19` | 启用 OpenCode Live E2E | 无 |
| `MARSHAL_LIVE_QWEN_PATH` | `internal/adapter/qwen/live_test.go:19` | 启用 Qwen Live E2E | 无 |
| `MARSHAL_LIVE_PI_PATH` | `internal/adapter/pi/live_test.go:20` | 启用 Pi Live E2E | 无 |

注：另有若干仅测试内部使用的开关（`MARSHAL_PROCESS_TREE_HELPER`、`MARSHAL_LAUNCH_HELPER` 等，见 `internal/terminal/process_supported_test.go:18-61`、`internal/launcher/execute_test.go:13`），属于再执行辅助进程的实现细节，不构成贡献者需要知道的契约，未计入上表。

### 正面发现（保持）

- **N1 包边界文档清晰**：`docs/development.md:19-37` 一张表讲清每个 internal 包职责；入口 `cmd/marshal/main.go:12-16` 是 16 行薄壳；新人 30 秒内能定位代码层级。
- **N2 契约即测试**：17 份 Schema 各带 happy-path 与 invalid fixture（`schemas/examples/happy-path/`、`schemas/examples/invalid/`），"改 Schema = 改 fixture + 过测试"的贡献路径明确。
- **N3 退出码是稳定契约**：`internal/cli/cli.go:39-47` 明确声明 ExitOK/ExitFailure/ExitUsage/ExitUnavailable，脚本化集成门槛低。
- **N4 离线可构建**：`GOPROXY=off` 下（模块缓存命中）`go run ./cmd/marshal version --json` 与 `doctor --json` 均成功返回结构化 JSON、退出码 0，构建路径无隐藏步骤。
- **N5 Operator Runbook 第 9 节**：`docs/operator-runbook.md:128-190` 的环境准备、长命令脱离、问题上报三件套是高质量实操内容，问题只是入口太深。

## 修复建议清单

按优先级排序，均可独立成为一个小任务；每条附可执行的验收方式。

1. **（P0）新增 LICENSE**。由维护者选定许可证（建议 MIT/Apache-2.0 二选一并以 ADR 记录决策），添加 `LICENSE` 文件与 README 授权段落。注意这是法律决策不能由贡献者代为拍板；过渡期可在 README 声明贡献授权立场。验收：`test -s LICENSE && head -1 LICENSE`。
2. **（P0）README 增加"快速开始"章节**。前置依赖（Go ≥1.26.0 或依赖 toolchain 自动下载、支持 worktree 的 Git）；步骤 `git clone` → `make build` → `./bin/marshal doctor --json` → `./bin/marshal contract validate --schema task-spec schemas/examples/happy-path/task-spec.json`（后两条 `docs/development.md:82-84` 已有先例，直接上提）；链接 `docs/operator-runbook.md` 第 9 节与未来的 CONTRIBUTING。验收：按 README 步骤在干净 worktree 逐条执行成功。
3. **（P1）新增 CONTRIBUTING.md**。单一入口汇总：分支与发布策略（Draft PR、merge 默认禁用，引用 `README.md:95-98` 与 ADR 0007）、提交前 `make check`（注明约 15 分钟，可先 `make test`）、CI 预期（quality 双平台 + secrets 两 job）、审查标准（证据门禁，引用 `docs/verification-and-review.md`）、`AGENTS.md` 面向 AI Agent 的定位说明。验收：新贡献者仅凭 README + CONTRIBUTING 完成"构建→测试→提 PR→读懂 CI 结果"闭环。
4. **（P1）同步 CLI 文档与代码**。以 `internal/cli/cli.go:1443-1462` 为真源更新 `docs/development.md:58-75`（补 `doctor --run/--repair`、`approve --actor`、`run --through-verify`、`task abort`）；`:90` 的"15 份 Schema"改为 17；修正 `docs/operator-runbook.md:106` 的"（开发中）"。验收：文档与 usage 文本逐行一致；`ls schemas/*.schema.json | wc -l` 与文档数字一致。
5. **（P2）补测试分层文档**。在 `docs/development.md` 或新 TESTING 小节说明：`go test ./...` 默认不含 Live E2E；Live 需要 `MARSHAL_LIVE_GITHUB`、`MARSHAL_LIVE_OPENCODE_PATH`、`MARSHAL_LIVE_QWEN_PATH`、`MARSHAL_LIVE_PI_PATH`、`MARSHAL_LIVE_CMUX` 等显式开关（逐一标注代码出处）；Live 测试不进 PR 门禁。验收：grep 全部 live 变量，文档覆盖率 100%。
6. **（P2）补齐 usage 命令清单**。在 `internal/cli/cli.go:1443-1462` 列出 `task abort` 与 `task cleanup`，并在 `internal/cli/cli_test.go` 增加断言。验收：`marshal help` 输出覆盖全部用户可见子命令。
7. **（P3）为 `internal/domain` 导出类型补 doc 注释**。优先 `identity.go`、`state.go`、`run.go`、`review.go` 中与生命周期/证据相关的类型；措辞与 `docs/task-lifecycle.md` 术语一致。验收：`make check` 通过；状态名与文档一致。
8. **（P3）补最小 community profile**：Issue 模板（bug/文档/新功能）、PR 模板（勾选 `make check` 与影响面）、`CODEOWNERS`。验收：新建 Issue/PR 时模板自动加载。

## 未核实项

- **完整测试套件未实跑**：`make check`/`make test` 未执行（全量约 15 分钟，见 `docs/operator-runbook.md:164`；审计受时间与网络约束）。仅离线验证了编译路径与两个只读命令，不能据此声称"clone 后测试必绿"。
- **远端仓库与 CI 状态未核实**：受"不使用网络"约束，`https://github.com/chiga0/marshal-harness` 的可访问性、分支保护设置、是否已有外部 Issue/PR 均未确认。
- **新机器首次构建时长未实测**：F7 中 toolchain 自动下载与 `go mod download` 在干净机器上的耗时与失败形态未实测（需要网络）。"5 分钟跑通"是基于缺失步骤的推断，不是实测值。
- **许可证选择**：F1/建议 1 涉及的许可证类型是维护者决策，本报告不代为判断；`go.mod:1` 的公开 module path 不构成授权暗示。
- **完整任务循环未端到端实跑**：plan→run→publish 循环需要配置 Worker Adapter 可执行文件与 GitHub 凭据（`docs/operator-runbook.md:136-143`）；本审计不配置真实凭据，该路径仅按文档与代码静态核对。
- **`--through-verify` 完整语义**：usage（`internal/cli/cli.go:1454`）与提交 `76fdf40` 提及，但行为边界未逐行核对，本报告仅将其作为文档同步项引用。
