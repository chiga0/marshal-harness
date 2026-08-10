# Changelog

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循 [SemVer](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### 新增
- herdr TerminalSession 后端 POC（实验分支，ADR 0009/0011 补充）；
- 数据层“重试同 taskId”、交互式 DAG、PTY token 统计（规划中）。

## [0.1.0] - 2026-08-10

首个正式版本：Local MVP `USABLE`（Milestone 0–6 全通过）。

### 新增
- `marshal web` / `marshal serve`：只读 Web 控制台（opt-in，三级视角+hash 路由+实时 SSE+检索+亮色）；
- `marshal task migrate-outcomes`：遗留终态 Run 补记 Outcome（不覆盖已有）；
- `marshal task abort`：废弃 Run 的显式生命周期出口，写终态 Outcome（ADR 0012）；
- `marshal task run --through-verify`：worker 成功后同调用内自动独立 verify；
- WorkerResult 归一化：三 Adapter 在校验前删除无效可选 `session` 字段；
- Worker prompt 内嵌 WorkerResult 逐字模板；
- worktree 创建仓库级短锁退避重试（5×800ms）；
- GitHub Pages 文档站（mkdocs-material，中文 + mermaid）；
- 开源基建：MIT LICENSE、CONTRIBUTING、CODE_OF_CONDUCT、SECURITY、issue/PR 模板；
- 三 Worker Adapter：OpenCode 1.18.13 / Qwen Code 0.21.5 / Pi 0.83.0；GitHub Draft Publisher；受监督 cmux TerminalSession。

### 修复
- publish Approval 与发布重校验读取 legacy `review-decision.json` 导致恒失败（轮次绑定修复）；
- ADR 0013/0014 接受并实施（拒绝分级、read-only 画像）。

## [0.1.0] - 2026-08-08

Local MVP：Milestone 0–6 全通过。证据门禁式生命周期（plan→run→verify→review→publish→accept）、三 Worker Adapter（OpenCode 1.18.13 / Qwen Code 0.21.5 / Pi 0.83.0）、GitHub Draft Publisher、受监督 cmux TerminalSession、崩溃恢复与发布幂等。
