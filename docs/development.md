# 开发指南

## 当前阶段

仓库已完成 Milestone 0–6，Local MVP 标记 `USABLE`（见 [roadmap-status.md](roadmap-status.md)）。版本与发布语义见根目录 [CHANGELOG.md](../CHANGELOG.md)；阶段推进以 tag 为准。

## Go 基线

- Module：`github.com/chiga0/marshal-harness`；
- Language Version：Go `1.26.0`；
- Toolchain：Go `1.26.5`；
- JSON Schema：Draft 2020-12；
- Glob：`doublestar/v4`，`**` 表示跨目录递归；
- 静态检查：`go vet` 与固定版本的 `staticcheck`（经 `go.mod` 的 `tool` 指令固定，Go 1.24+ 特性；请勿自行 `go install` 其他版本，以免结果不一致）；
- 交付形式：单一 `marshal` 可执行文件；
- 工具链：Go 低于 `go.mod` 要求时 toolchain 自动下载需要网络；受限环境请预先安装匹配版本。

GitHub remote 已绑定为 `github.com/chiga0/marshal-harness`，与 Module Path 一致。

## Package 边界

```text
cmd/marshal/        CLI 进程入口
internal/cli/       参数解析与退出码
internal/cleanup/   Cleanup Preview、Guard、Crash-safe Tombstone 与显式 Apply
internal/app/       Application Service 与依赖装配
internal/domain/    Provider-neutral Domain Type
internal/contract/  Schema 编译与 Semantic Validator
internal/port/      Worker、Verifier、Lead Agent、Publisher Port
internal/adapter/   Adapter Registry、Fake 与 OpenCode Worker
internal/gitworktree/ Repository Identity、linked worktree 与锁
internal/verification/ 独立 Diff、Scope、Command 与 Artifact 验证
internal/review/   ReviewPacket、Decision Guard、Outcome 与崩溃安全记录
internal/publication/ 受控 Commit、发布证据门禁与远端 CI 状态
internal/publisher/github/ 独立凭据的 GitHub Draft Publisher
schemas/            JSON Schema、Fixture 与 Embedded FS
.agents/skills/marshal/ Codex CLI/Desktop 共用的轻量 Skill
```

Core Domain 与 Contract Package 不得导入 Provider-specific Package。CLI 只作为 Application Service 的薄入口。

## 本地命令

```bash
make format
make vet
make lint
make test
make build
make check
make vuln
make ci
```

`make check` 执行 Format Check、Vet、Staticcheck、Race Test 与 Build；`make ci` 在此基础上执行 `govulncheck`。**全量 `make check` 在本仓库约 15 分钟**（race 全量测试为主）；日常可先跑 `go test ./internal/<包>/...` 快速反馈。构建结果默认位于 `bin/marshal`，该目录被 Git 忽略。

GitHub Actions 在 Linux 与 macOS 上执行同一质量门禁和漏洞扫描，并使用独立 Job 执行 Secret Scan。外部 Action 固定到完整 Commit SHA，工作流默认只有 `contents: read` 权限。

## 当前 CLI

```bash
marshal version [--json]
marshal doctor [--run RUN_ID] [--repair] [--print-env] [--json]
marshal contract validate [--schema NAME] <PATH|->
marshal init [--json]
marshal task status --run RUN_ID [--json]
marshal task plan --task PATH --policy PATH --run RUN_ID [--json]
marshal task approve --run RUN_ID --gate plan|publish [--actor ID] [--json]
marshal task run --run RUN_ID [--through-verify] [--json]
marshal task verify --run RUN_ID [--json]
marshal task review --run RUN_ID [--decision PATH] [--json]
marshal task publish --run RUN_ID [--json]
marshal task accept --run RUN_ID [--json]
marshal task abort --run RUN_ID --actor ID --reason TEXT [--json]
marshal task cleanup --run RUN_ID [--apply] [--json]
marshal task <COMMAND>
```

`version`、`doctor`、`contract validate`、`task status` 和不带 `--apply` 的 `task cleanup` 是只读命令。`marshal init` 创建仓库绑定的状态目录并写入 Git exclude。`task run` 使用冻结选择的 Worker Adapter；`verify`、`review`、`publish`、`accept` 分别执行独立证据门禁。发布命令要求 absolute `MARSHAL_GH_PATH` 与独立 `MARSHAL_GH_CONFIG_DIR`。Cleanup 默认只列出精确 managed worktree；只有显式 `--apply` 才执行，并拒绝 Active Lease、非终态 Run、缺失 Outcome、活跃 TerminalSession、dirty/symlink/身份不明目标。它不删除 Run 证据、本地 branch、远端 branch 或 PR。

示例：

```bash
go run ./cmd/marshal doctor --json
go run ./cmd/marshal contract validate --schema task-spec schemas/examples/happy-path/task-spec.json
```

## 环境变量参考

| 变量 | 用途 | 性质 |
| --- | --- | --- |
| `MARSHAL_STATE_DIR` | 覆盖默认 `.marshal/` 状态目录（绝对路径；仓库内则必须等于默认目录） | 运行配置 |
| `MARSHAL_OPENCODE_PATH` | OpenCode Worker 可执行文件绝对路径 | Adapter 注册 |
| `MARSHAL_QWEN_PATH` | Qwen Code Worker 可执行文件绝对路径 | Adapter 注册 |
| `MARSHAL_PI_PATH` | Pi Worker 可执行文件绝对路径 | Adapter 注册 |
| `MARSHAL_GH_PATH` | Publisher 的 `gh` 可执行文件绝对路径 | 发布凭据边界 |
| `MARSHAL_GH_CONFIG_DIR` | Publisher 独立凭据目录（不复用 ambient 配置） | 发布凭据边界 |
| `MARSHAL_CMUX_PATH` | cmux 可执行文件路径（受监督 Pilot） | 可选后端 |
| `MARSHAL_LIVE_CMUX` | 启用 cmux Live E2E | 测试开关 |
| `MARSHAL_LIVE_GITHUB` / `MARSHAL_LIVE_GITHUB_FIXED_SUFFIX` | 启用 GitHub Publisher Live 测试（后者固定 branch/PR 后缀以便幂等重跑） | 测试开关 |
| `MARSHAL_LIVE_OPENCODE_PATH` / `MARSHAL_LIVE_QWEN_PATH` / `MARSHAL_LIVE_PI_PATH` | 启用对应 Adapter 的 Live E2E | 测试开关 |
| `MARSHAL_DISCOVERY_KNOWN_LOCATIONS` | 覆盖 doctor 建议式发现的已知安装位置列表；`-` 表示禁用，仅保留 PATH 扫描 | 测试开关 |

所有 `*_PATH` 变量只接受绝对路径；注册本身不搜索 PATH、不猜近似名（未设置时 `marshal doctor` 的 workers 段显示 `not-configured`）。`marshal doctor` 的 discovery 段额外提供建议式发现，仅作建议、绝不自动注册（见下文[部署到新环境](#部署到新环境)）。Live 测试默认跳过，CI 不依赖任何 Live 开关。

## 部署到新环境

新环境部署的目标是把“读文档自己猜”变成“doctor 给建议、用户一行注册”。核心原则：**discovery advisory，registration explicit**——doctor 只发现并建议，注册仍须显式环境变量；这保持了“Registry 不得静默替换二进制或版本”的不变量。

doctor 扫描 PATH 目录与常见安装位置（`~/.local/bin`、`/opt/homebrew/bin`、`~/.opencode/bin`、fnm/node 全局 bin、`npm root -g`），匹配已知二进制名 `opencode`、`qwen`、`qwen-code`、`pi`。它不递归全盘、不猜近似名；对每个候选只执行 `<bin> --version` 读取版本并计算 realpath 与 SHA256，任何执行失败静默跳过。已配置环境变量的 Adapter 不参与发现。

### doctor 解读

```bash
marshal doctor --json
```

报告新增 `discovery` 数组，每个未注册 Adapter 一项：

- `adapterId` / `environmentVariable`：对应 Adapter 与注册变量；
- `candidates`：每个候选含 `path`（发现位置）、`realpath`（符号链接解析后的绝对路径）、`sha256`、`version`、`source`（`path` 或 `known-location`）；
- `suggestedEnv`：建议写入注册变量的 realpath，供用户粘贴。

`discovery` 结果只出现在 doctor 输出，不进入 CapabilitySnapshot，也不改变 plan 的 fail-closed 语义：未注册的 Adapter 依旧不可用。

### 建议行粘贴

```bash
marshal doctor --print-env
```

该标志直接打印建议的 `export` 行，例如：

```bash
export MARSHAL_QWEN_PATH=/Users/me/.local/bin/qwen
```

将其粘贴到 shell 配置（或当前会话）即完成注册。doctor 不会写环境变量、不写 shell profile、不自动注册 Adapter。

### 验证 registered

粘贴导出后重新运行：

```bash
marshal doctor --json
```

确认对应 Adapter 的 `workers` 条目 `outcome=registered`、`compatibility=supported`；此时该 Adapter 不再出现在 `discovery` 段。若 `compatibility=unsupported`，说明二进制版本与冻结的 supported 版本不符，doctor 会如实报告但不会因此放行。

本节与 Operator Runbook §9.1“一次性环境准备”交叉引用：§9.1 给出五个环境变量的固化方法，本节给出如何用 doctor 发现正确的绝对路径。

## 契约策略

JSON Schema 是持久化记录的权威结构定义。Go 侧只定义 Core 实际需要的强类型枚举与不透明 `Record`，避免维护第二套完整镜像结构；测试强制检查：

- 17 份 Schema 全部可以编译；
- 每份 Happy-path Fixture 通过；
- 每份 Invalid Fixture 失败；
- Format Assertion 已启用；
- `Kind` 与生命周期 `State` 常量和 Schema 对齐；
- Task Budget、ID 唯一性、路径、Verification Status、Review Finding 与 `.marshal/` Source Artifact 等 Semantic Rule 生效。

Schema 使用 ECMA-262 风格正则。Go 标准库正则不支持其中的全部转义，因此 Validator 显式使用 ECMAScript Regex Engine；不得退回默认 Go Regex 后仍宣称契约兼容。
