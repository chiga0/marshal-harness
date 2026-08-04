# 开发指南

## 当前阶段

仓库已完成 Milestone 0–5：具备契约、生命周期、Run Store、隔离 worktree、独立 Verification、Review/Rework、Outcome、OpenCode Worker 与 GitHub Draft Publisher。当前进入 Qwen Code/Pi Adapter、Recovery/Cleanup 与完整 MVP E2E。

## Go 基线

- Module：`github.com/chiga0/marshal-harness`；
- Language Version：Go `1.26.0`；
- Toolchain：Go `1.26.5`；
- JSON Schema：Draft 2020-12；
- Glob：`doublestar/v4`，`**` 表示跨目录递归；
- 静态检查：`go vet` 与固定版本的 `staticcheck`；
- 交付形式：单一 `marshal` 可执行文件。

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

`make check` 执行 Format Check、Vet、Staticcheck、Race Test 与 Build；`make ci` 在此基础上执行 `govulncheck`。构建结果默认位于 `bin/marshal`，该目录被 Git 忽略。

GitHub Actions 在 Linux 与 macOS 上执行同一质量门禁和漏洞扫描，并使用独立 Job 执行 Secret Scan。外部 Action 固定到完整 Commit SHA，工作流默认只有 `contents: read` 权限。

## 当前 CLI

```bash
marshal version [--json]
marshal doctor [--json]
marshal contract validate [--schema NAME] <PATH|->
marshal init [--json]
marshal task status --run RUN_ID [--json]
marshal task plan --task PATH --policy PATH --run RUN_ID [--json]
marshal task approve --run RUN_ID --gate plan|publish [--json]
marshal task run --run RUN_ID [--json]
marshal task verify --run RUN_ID [--json]
marshal task review --run RUN_ID [--decision PATH] [--json]
marshal task publish --run RUN_ID [--json]
marshal task accept --run RUN_ID [--json]
marshal task cleanup --run RUN_ID [--apply] [--json]
marshal task <COMMAND>
```

`version`、`doctor`、`contract validate`、`task status` 和不带 `--apply` 的 `task cleanup` 是只读命令。`marshal init` 创建仓库绑定的状态目录并写入 Git exclude。`task run` 使用冻结选择的 Worker Adapter；`verify`、`review`、`publish`、`accept` 分别执行独立证据门禁。发布命令要求 absolute `MARSHAL_GH_PATH` 与独立 `MARSHAL_GH_CONFIG_DIR`。Cleanup 默认只列出精确 managed worktree；只有显式 `--apply` 才执行，并拒绝 Active Lease、非终态 Run、缺失 Outcome、活跃 TerminalSession、dirty/symlink/身份不明目标。它不删除 Run 证据、本地 branch、远端 branch 或 PR。

示例：

```bash
go run ./cmd/marshal doctor --json
go run ./cmd/marshal contract validate --schema task-spec schemas/examples/happy-path/task-spec.json
```

## 契约策略

JSON Schema 是持久化记录的权威结构定义。Go 侧只定义 Core 实际需要的强类型枚举与不透明 `Record`，避免维护第二套完整镜像结构；测试强制检查：

- 15 份 Schema 全部可以编译；
- 每份 Happy-path Fixture 通过；
- 每份 Invalid Fixture 失败；
- Format Assertion 已启用；
- `Kind` 与生命周期 `State` 常量和 Schema 对齐；
- Task Budget、ID 唯一性、路径、Verification Status、Review Finding 与 `.marshal/` Source Artifact 等 Semantic Rule 生效。

Schema 使用 ECMA-262 风格正则。Go 标准库正则不支持其中的全部转义，因此 Validator 显式使用 ECMAScript Regex Engine；不得退回默认 Go Regex 后仍宣称契约兼容。
