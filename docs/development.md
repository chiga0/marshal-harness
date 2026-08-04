# 开发指南

## 当前阶段

仓库已完成 Milestone 2：具备 Go 工具链、契约验证、生命周期、Run Store、`.marshal/` 初始化、隔离 Git worktree、独立 Verification、交付物摘要与证据报告。真实 Worker 与 Publisher 尚未启用。

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
internal/app/       Application Service 与依赖装配
internal/domain/    Provider-neutral Domain Type
internal/contract/  Schema 编译与 Semantic Validator
internal/port/      Worker、Verifier、Lead Agent、Publisher Port
internal/adapter/   Adapter Registry；当前不含真实 Provider
internal/gitworktree/ Repository Identity、linked worktree 与锁
internal/verification/ 独立 Diff、Scope、Command 与 Artifact 验证
schemas/            JSON Schema、Fixture 与 Embedded FS
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
marshal task verify --run RUN_ID [--json]
marshal task <COMMAND>
```

`version`、`doctor`、`contract validate` 和 `task status` 是只读命令。`marshal init` 会创建仓库绑定的状态目录，并把默认 `/.marshal/` 写入 `.git/info/exclude`。`task verify` 只在持有 Run/Task Lease 时独立观察受管 worktree、执行有界验收命令并生成 VerificationReport 与 ArtifactManifest；它不会启动 Worker 或执行发布。其余尚未实现的 `task` 子命令返回 `ExitUnavailable=3`。

示例：

```bash
go run ./cmd/marshal doctor --json
go run ./cmd/marshal contract validate --schema task-spec schemas/examples/happy-path/task-spec.json
```

## 契约策略

JSON Schema 是持久化记录的权威结构定义。Go 侧只定义 Core 实际需要的强类型枚举与不透明 `Record`，避免维护第二套完整镜像结构；测试强制检查：

- 11 份 Schema 全部可以编译；
- 每份 Happy-path Fixture 通过；
- 每份 Invalid Fixture 失败；
- Format Assertion 已启用；
- `Kind` 与生命周期 `State` 常量和 Schema 对齐；
- Task Budget、ID 唯一性、路径、Verification Status、Review Finding 与 `.marshal/` Source Artifact 等 Semantic Rule 生效。

Schema 使用 ECMA-262 风格正则。Go 标准库正则不支持其中的全部转义，因此 Validator 显式使用 ECMAScript Regex Engine；不得退回默认 Go Regex 后仍宣称契约兼容。
