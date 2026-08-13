# Changelog

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循 [SemVer](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### 新增
- herdr TerminalSession 后端 POC（实验分支，ADR 0009/0011 补充）；
- 数据层“重试同 taskId”、交互式 DAG、PTY token 统计（规划中）；
- TaskSpec `worker.tools` 声明式工具 allowlist（封闭枚举 read/edit/write/grep/find/ls/bash，可选，uniqueItems；缺省保持既有 profile 行为，全部既有 TaskSpec 向后兼容）；
- 三个 Worker Adapter 对 `worker.tools` 的机械强制：pi `--tools` 精确交集（声明 bash 或 read-only 声明 write 启动前 fail closed）、opencode 最小 permission 配置 + `debug config` 回读校验、qwen `--exclude-tools` 反向收敛；声明读取/格式非法一律启动前 fail closed；
- 三个 Adapter（含 Fake Adapter）把成功（非拒绝）工具调用的工具名规范化（ADR 0013 冻结工具分类表）后写入 `<adapter>-transcript-meta.json` 的 `toolNames`；
- Verification 新增 `tool-allowlist` required gate（denial-summary 之后、command gates 之前）：声明后任一成功调用越权判 required fail 并附越权清单证据，证据缺失/不可读/格式非法 fail closed，未声明的 Run gate skipped；
- 跨 Adapter 对账一致性 Conformance 套件（同一合规/越权事件序列在 opencode/pi/qwen 采集+对账路径下裁决逐位一致）。

### 修复
- issue #37：TaskSpec 声明的工具约束此前仅由 prompt 禁令表达；现形成 TaskSpec 声明 → Adapter 在 Provider 调用层机械强制 → transcript 采集 → Verification gate 判失败的闭环，未声明工具在 Provider 调用层被拒绝或在对账层成为 required Gate failure。
- 移除 pi/qwen Adapter 在 allowlist 重构后遗留的未使用终端 argv 构造函数（staticcheck U1000）：终端形态构造已统一并入 `buildTerminalArgsWithTools`，`--tools`/`--exclude-tools` 的 allowlist 收敛语义在 captured 与终端两条路径上均不变。

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
