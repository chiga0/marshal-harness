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
marshal doctor [--run RUN_ID] [--repair] [--json]
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

所有 `*_PATH` 变量只接受绝对路径；未设置时 `marshal doctor` 显示 `not-configured`（不会搜索 PATH 或猜近似名）。Live 测试默认跳过，CI 不依赖任何 Live 开关。

## 契约策略

JSON Schema 是持久化记录的权威结构定义。Go 侧只定义 Core 实际需要的强类型枚举与不透明 `Record`，避免维护第二套完整镜像结构；测试强制检查：

- 17 份 Schema 全部可以编译；
- 每份 Happy-path Fixture 通过；
- 每份 Invalid Fixture 失败；
- Format Assertion 已启用；
- `Kind` 与生命周期 `State` 常量和 Schema 对齐；
- Task Budget、ID 唯一性、路径、Verification Status、Review Finding 与 `.marshal/` Source Artifact 等 Semantic Rule 生效。

Schema 使用 ECMA-262 风格正则。Go 标准库正则不支持其中的全部转义，因此 Validator 显式使用 ECMAScript Regex Engine；不得退回默认 Go Regex 后仍宣称契约兼容。
