# ADR 0003：Worker 与 Publisher 分权

- 状态：已接受（Accepted）
- 日期：2026-08-03
- 接受日期：2026-08-03

## 背景

Worker 需要文件与命令权限完成实现，但不需要 Push、创建 PR、修改仓库设置或 Merge。把实现与发布权限交给同一进程会绕过 Review Gate 并扩大 Credential Exposure。

## 决策

Worker 不接收 Publisher Credential。只有 Publisher 可以从 Accepted Snapshot 创建 Commit、Push Task Branch 并创建或更新 PR/MR。Merge 是独立能力，默认禁用。

普通开发机上的凭据分离只是 Best-effort，除非 OS/Container Sandbox 能阻止访问 Ambient Credential Store 与 Network。

## 影响

- Publication 必须在独立 Verification 与语义 Accept 后发生。
- PR 创建具有幂等性和可审计性。
- Environment Filtering 不能被描述成强安全边界。
- Hardened Profile 需要 Container/VM 或同等 Host Policy。
- Publisher Failure 不会让已接受本地变更失效，可直接重试。

## 未采用方案

- **Worker 自己创建 PR**：把 Provider Prompt 与 Forge Side Effect 耦合，并绕过 Decision Gate。
- **主 Agent 直接 Push**：混合语义判断与 Credentialed Mechanic，使重试难以确定化。
