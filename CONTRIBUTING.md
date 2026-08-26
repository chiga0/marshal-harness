# 贡献指南

欢迎！Marshal 是一个证据门禁式的 Coding Agent 编排器。本指南帮助你从环境搭建到第一个 PR。

## 行为准则

参与即表示你同意遵守 [行为准则](CODE_OF_CONDUCT.md)。

## 治理

本仓库采用 universal / maintainer-only / external-contributor 三层治理，规则权威在 [AGENTS.md](AGENTS.md)：

- 所有参与者必须遵守 universal 规则（不可破坏的不变量与门禁）；
- 维护者以三条件验证：列名于 [.github/MAINTAINERS](.github/MAINTAINERS)、remote 指向 canonical 仓库、账号有写权限（见 AGENTS.md『维护者工作流（maintainer-only）』）；
- 外部贡献者一律 fork + PR，遵守 AGENTS.md『外部贡献者护栏（external-contributor）』。

## 开发环境

- Go 版本以 `go.mod` 为准；`make check` 还需要 Python 3 运行仓库内的确定性架构检查（均不需要额外安装第三方包）；
- 常用目标：
  - `make check`：format-check + package-layer architecture-check + vet + staticcheck + 全仓 race 测试 + build（提交前必须通过）；
  - `make vuln`：govulncheck；
  - `make test` / `make build`：单独执行。
- 仓库的本地运行态位于被 Git 忽略的 `.marshal/`，不会进入你的提交。

## 贡献流程

1. **先开 Issue**（bug 或 feature），大改动先对齐方案；涉及信任边界、持久化契约、生命周期或发布权限的变更需要 ADR（见 `docs/adr/`）；
2. Fork 并创建分支；
3. 实现 + 测试。表驱动测试优先；失败路径与成功路径同等重要；
4. 本地 `make check` 与 `make vuln` 通过；
5. 提交 PR，填写模板。CI（Linux + macOS + secret scan）必须全绿；
6. 审查标准：不变量不被破坏（见 [AGENTS.md](AGENTS.md)）、证据链完整、文档同步更新。

## 不可破坏的不变量（摘要）

- Worker 不能为自己的工作提供权威验证证据；
- 每个写任务使用锁定基线与独立 worktree，一个 worktree 同时最多一个写入者；
- ReviewDecision 必须绑定精确证据摘要；
- Worker 与 Publisher 凭据分离；失败 fail-closed，不猜测、不覆盖。

完整清单见 [AGENTS.md](AGENTS.md)。

## 文档与代码风格

- 面向人的 Markdown 使用中文；协议字段、状态名、CLI 命令与代码标识保留英文；
- 术语与状态名全仓一致（`docs/task-lifecycle.md` 为状态权威）；
- Go 代码遵循现有包边界：Core（domain/lifecycle/runstore）不依赖 Provider 细节。

## Good First Issue

见带 `good-first-issue` 标签的 Issue；若无开放项，可在 Issue 中认领审计报告中列出的候选（`docs/research/oss-audit-*.md`）。

## 许可证

本项目采用 MIT 许可（见 [LICENSE](LICENSE)）。你的贡献将按同一许可证发布。
