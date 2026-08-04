# Marshal 仓库协作指南

## 当前阶段

本仓库已于 2026-08-03 通过实施门禁，ADR 0001–0008 已接受。Milestone 0–5 已通过，当前进入 Milestone 6。必须按 `docs/implementation-plan.md` 顺序实施；当前 Milestone 的退出条件满足前，不得提前执行后续阶段副作用。

## 修改设计前必读

按顺序阅读：

1. `README.md`
2. `docs/vision-and-scope.md`
3. `docs/architecture.md`
4. `docs/task-lifecycle.md`
5. `docs/security-model.md`
6. `docs/adr/` 中相关 ADR

## 不可破坏的不变量

- Worker 不能为自己的工作提供权威验证证据。
- 每个写任务都必须使用锁定基线和独立 Git worktree。
- 每个仓库的本地 Run、Log、Cache 与任务 worktree 默认位于被 Git 忽略的 `.marshal/`，不得进入业务提交。
- 一个任务 worktree 同时最多有一个写入者。
- Worker 与 Publisher 权限必须分离。
- ReviewDecision 必须绑定到精确的证据摘要。
- 失败或阻塞任务必须保存 Outcome 证据，不得创建虚假 PR。
- 普通宿主机子进程不得被描述成恶意代码沙箱。
- Merge 默认禁用，不属于 MVP 生命周期。

## 文档修改要求

- 所有面向人的 Markdown 文档统一使用中文；协议字段、状态名、CLI 命令和代码标识保留英文。
- 所有文档与 Schema 中的术语和状态名必须一致。
- 打开或关闭重大架构问题时更新 `docs/audit-report.md`。
- 改变信任边界、持久化契约、生命周期或发布权限时，必须新增或替代 ADR。
- 修改 Schema 后必须验证 JSON 语法、Draft 2020-12 metaschema、示例和 `git diff --check`。
- 不得为了简化 Adapter 而静默放宽强制门禁。

## 实施门禁

维护者已明确接受设计。实施期间必须：

1. 按 `docs/implementation-plan.md` 顺序实施；
2. 在真实 Agent 集成前先完成 Fake Adapter 与确定性核心；
3. 状态转换、路径边界、陈旧证据、崩溃恢复和发布幂等性测试通过前，不得宣称 MVP 可用。
