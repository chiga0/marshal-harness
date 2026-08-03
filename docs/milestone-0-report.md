# Milestone 0 验收报告

- 验收日期：2026-08-03
- 状态：**`PASSED`**
- 范围：Toolchain、Contract、Package Boundary 与无副作用 CLI Skeleton
- 真实 Worker/Publication Side Effect：未启用

## 交付结果

### Go Toolchain

- Module：`github.com/chiga0/marshal-harness`；
- Language：Go `1.26.0`；
- Toolchain：Go `1.26.5`；
- Dependency Lock：`go.mod` 与 `go.sum`；
- Build Artifact：单一 `bin/marshal`；
- Quality Gate：Format、`go vet`、`staticcheck`、Race Test 与 Build。
- Security Gate：`govulncheck` 与 GitHub Gitleaks Secret Scan；
- CI Platform：GitHub Actions，Linux 与 macOS Matrix。

Module Path 当前根据本机已认证 GitHub 用户与目录名推导。仓库尚无 Git remote；首次发布前必须校验最终 Remote Identity。

### 模块边界

- `internal/domain`：Provider-neutral `Kind`、`State` 与 opaque `Record`；
- `internal/contract`：Schema Catalog、Draft 2020-12 Compiler 与 Semantic Validator；
- `internal/port`：Worker、Verifier、Lead Agent 与 Publisher Port；
- `internal/adapter`：Exact-ID Registry，不做隐式 Fallback；
- `internal/app`：Application Service 与 Composition Root；
- `internal/cli`：薄 CLI Boundary 与稳定 Exit Code；
- `cmd/marshal`：单一 Process Entry。

Core Domain 不依赖 Qwen、OpenCode、Pi、GitHub 或 Codex 的 Provider-specific 实现。

### Contract

- 11 份 JSON Schema 编译成功；
- 11 份 Happy-path Fixture 通过；
- 11 份 Invalid Fixture 被拒绝；
- Draft 2020-12 Format Assertion 已启用；
- `Kind` Catalog 与 16 个 Lifecycle `State` 受漂移测试保护；
- 首批 Semantic Validator 覆盖：
  - Repository/Artifact Relative Path；
  - Path Pattern 与 ID 唯一性；
  - Attempt、Retry 与 Rework Budget Relationship；
  - Publication Deliverable Consistency；
  - Verification Overall Status 与 Required Gate 一致性；
  - Changed File Count 与时间顺序；
  - Review Finding、Blocking Verdict 与 Blocker Owner；
  - 禁止把 `.marshal/` 内容声明为 Repository Source Artifact；
  - 使用 `doublestar/v4` 固定 `**` 的递归 Glob 语义；
  - 所有 `relativePath` Schema 拒绝反斜杠。

完整 Go Record Mirror 没有在尚未被 Core 使用时提前生成。Schema 是持久化契约的唯一权威；Go 只强类型化当前 Domain 实际使用的字段，并由 Catalog/State/Fixture Test 防止漂移。

### CLI Skeleton

当前可用的只读命令：

```text
marshal version [--json]
marshal doctor [--json]
marshal contract validate [--schema NAME] <PATH|->
```

全部预期 `marshal task` 子命令已占位，返回稳定的 `ExitUnavailable=3`。回归测试证明这些占位命令不会创建 `.marshal/`、启动 Worker 或执行发布。

## 实施中发现并关闭的问题

| ID | 级别 | 问题 | 处理 |
| --- | --- | --- | --- |
| M0-001 | P1 | Go 默认 Regex Engine 不接受 JSON Schema/ECMAScript 合法的 `\\u0000` Pattern | 显式使用 ECMAScript Regex Engine，并让 Schema Compile Test 覆盖该差异 |
| M0-002 | P2 | 仓库没有 Remote，Go Module Path 缺少最终权威来源 | 暂用 `github.com/chiga0/marshal-harness`，首次发布前增加 Remote Identity Gate |
| M0-003 | P2 | CLI Skeleton 容易被误认为已能执行任务 | 所有 `task` 命令返回独立 Unavailable Exit Code，并明确声明无副作用 |
| M0-004 | P2 | Go `path.Match` 不提供文档所需的递归 `**` 语义 | 锁定 `doublestar/v4`，Semantic Validator 与未来 Scope Gate 共用同一 Glob 语义 |
| M0-005 | P2 | 部分 `relativePath` Schema 可接受反斜杠路径 | 六份相关 Schema 统一拒绝反斜杠，并加入无 Go Semantic Layer 的 ReviewPacket 回归测试 |

## 自动验收

```text
make check
  format-check     passed
  go vet ./...     passed
  staticcheck      passed
  go test -race    passed
  go build         passed

make vuln
  govulncheck      passed
```

Binary Smoke Test：

- `marshal version --json`：通过；
- `marshal doctor --json`：报告 11 份 Schema、0 个 Worker Adapter、Milestone 0；
- Happy-path TaskSpec：Exit `0`；
- Invalid TaskSpec：Exit `1`；
- `marshal task status`：Exit `3`；
- 仓库根目录没有创建 `.marshal/`。

## 退出条件对照

| Milestone 0 退出条件 | 结果 |
| --- | --- |
| 全部 Schema 通过 Draft 2020-12 编译 | 通过 |
| Fixture 覆盖 Format 与 Semantic Validator | 通过 |
| Clean Module Download、Format、Vet、Lint、Unit Test 与 Build | 通过 |
| 产出单一 `marshal` 可执行文件 | 通过 |
| Core Domain 不依赖 Provider-specific Module | 通过 |

Milestone 0 已满足退出条件，可以进入 Milestone 1。Milestone 1 才开始实现仓库本地 `.marshal/`、Lifecycle Reducer、Journal、Atomic Snapshot、Lease、Digest 与 Fake Adapter。

独立审查记录见 [Milestone 0 OpenCode Review](reviews/milestone-0-opencode-review.md)。
