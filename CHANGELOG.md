# Changelog

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循 [SemVer](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### 新增
- `marshal task abort`：废弃 Run 的显式生命周期出口，写终态 Outcome（ADR 0012）；
- `marshal task run --through-verify`：worker 成功后同调用内自动独立 verify；
- WorkerResult 归一化：三 Adapter 在校验前删除无效可选 `session` 字段；
- Worker prompt 内嵌 WorkerResult 逐字模板；
- worktree 创建仓库级短锁退避重试（5×800ms）；
- GitHub Pages 文档站（mkdocs-material，中文 + mermaid）；
- 开源基建：MIT LICENSE、CONTRIBUTING、CODE_OF_CONDUCT、SECURITY、issue/PR 模板；
- ADR 0013（Permission 拒绝分级）、ADR 0014（Read-only 执行画像）已接受，实施中。

### 修复
- publish Approval 与发布重校验读取 legacy `review-decision.json` 导致恒失败（轮次绑定修复）；
- Skill 触发一致性测试与泛化描述对齐。

## [0.1.0] - 待发布

Local MVP：Milestone 0–6 全通过。证据门禁式生命周期（plan→run→verify→review→publish→accept）、三 Worker Adapter（OpenCode 1.18.13 / Qwen Code 0.21.5 / Pi 0.83.0）、GitHub Draft Publisher、受监督 cmux TerminalSession、崩溃恢复与发布幂等。
