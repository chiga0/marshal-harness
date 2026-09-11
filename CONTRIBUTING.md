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

### Node 主线（packages/ + apps/task-web）

当前产品主线是 Node：`packages/`（30 个纯 ESM `.mjs` 包，无 npm workspace、无第三方运行时依赖）+ `apps/task-web`（React 19 + Vite 浏览器 UI）。

- Node ≥ 22（CI 实测 22.22.1 / 24.15.0 双版本矩阵）；
- packages 测试命令与 CI 完全一致：`node --test --test-concurrency=1 packages/*/*.test.mjs`；
- apps/task-web：`npm ci` 后在其目录下执行 `npm run typecheck`、`npm run build`、`npx vitest run`（组件/单测 + e2e；e2e 起真实 task-service 子进程并依赖已构建的 `dist/`，先 build 再跑）；
- CI：`.github/workflows/node-team.yml`（packages 回归 + `ui` job[typecheck/build/test+e2e] + pack 冻结 + 候选消费，ubuntu/macOS × Node 22/24 矩阵）。

### 历史 Go 已退役移除

Go 历史线（`cmd/`、`internal/`、`schemas/`、旧 `web/` 控制台）已于 2026-09-10 按 [ADR 0099](docs/adr/0099-go-legacy-line-retirement-and-removal.md) 退役并从 main 移除；历史字节保留在 git 历史与 tag `v1.0.0-rc1` 中追溯。新功能开发一律在 Node 主线进行。

### 本地运行态

仓库的本地运行态位于被 Git 忽略的 `.marshal/`，不会进入你的提交。

## 贡献流程

1. **先开 Issue**（bug 或 feature），大改动先对齐方案；涉及信任边界、持久化契约、生命周期或发布权限的变更需要 ADR（见 `docs/adr/`）；
2. Fork 并创建分支；
3. 实现 + 测试。表驱动测试优先；失败路径与成功路径同等重要；
4. 本地验证按改动面执行：`node --test --test-concurrency=1 packages/*/*.test.mjs`；涉及 apps/task-web 时再跑 `npm run typecheck && npm run build && npx vitest run`（在 apps/task-web 内）；
5. 提交 PR，填写模板。相同 sourceHead 的 CI（node-team 回归 + ui job + secret scan）必须全绿；
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
- Node 代码遵循现有包分层：Agent 运行（`agent-*`）不依赖 Task 认知；存储/应用（`task-store`/`task-application`）不依赖 Provider 细节；HTTP 合同唯一权威在 `packages/task-api/openapi.json`。

## Good First Issue

见带 `good-first-issue` 标签的 Issue；若无开放项，可在 Issue 中认领审计报告中列出的候选（`docs/research/oss-audit-*.md`）。

## 许可证

本项目采用 MIT 许可（见 [LICENSE](LICENSE)）。你的贡献将按同一许可证发布。
