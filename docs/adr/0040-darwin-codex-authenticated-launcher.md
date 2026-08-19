# ADR 0040：Darwin Codex authenticated launcher（提案）

- 状态：提议（Proposed，2026-08-19；未经维护者接受，不启用生产准入）
- 关联：[ADR 0037](0037-codex-cli-production-authority.md)、[ADR 0038](0038-agent-production-authority-provider.md)、[ADR 0039](0039-apap-peer-exec-barrier.md)、Issue #136

## 背景

ADR 0037 与 ADR 0038 冻结了 Codex 的生产 authority 合同，并规定在没有等价强制机制前 Darwin 必须保持 `unsupported`。当前 Darwin 构建只有 pathname exec 方案，不能证明 held executable 在 digest、probe、launch 与 child barrier 之间保持同一不可变对象，因此不能把普通 macOS 子进程描述为生产隔离。

用户目标是先支持当前 macOS 宿主。该目标需要一个独立、可部署、可审计的 Darwin launcher；不能通过删除 `codex_platform_unsupported`、开启 `unsafePathExecutionForTest`、`sandbox-exec` 或仅记录 `codesign` 摘要来绕过原有门禁。

## 决策（提案）

Mac production profile 只允许在以下条件全部满足时启用：

1. Marshal consumer 通过独立的 `AgentProductionAuthorityProvider` 获取当前 signed authority bundle；Adapter、Worker 与 provider 均不能签发自身 evidence。
2. 独立 Darwin launcher 由部署者以固定 Team ID、CDHash、代码签名链和内容 digest 绑定。launcher 的签名身份、构建 digest 与版本进入 authority evidence；调用者传入的 pathname 不是 authority。
3. launcher 从 consumer 交付的 held executable fd 读取 Mach-O bytes，完成 digest、架构、代码签名和版本验证后，使用受支持的 Darwin fd/vnode 执行原语启动 Codex。若当前系统无法证明 exec 后仍是同一 vnode/签名对象，必须返回 typed permanent failure，不能降级为 pathname exec。
4. launcher 在 child workload 前建立 stopped-child barrier，并返回包含 source/sealed/child identity、argv/environment digest、worktree/controlRoot identity、challenge nonce、launch receipt 的闭集证据。exec-away、exec-back、fd replacement、barrier 丢失或 launcher crash 均 kill+wait，不能重放 receipt。
5. macOS 路径边界、只读 profile、无 secret 输出、rotation/revocation、crash recovery 和 replay negative matrix 由独立 verifier 实测；真实 credentialed live probe 未通过前 `doctor` 与 registry 继续报告 `unsupported`。

## 不采用的方案

- 不使用 `/dev/fd/N` pathname 作为唯一安全保证；
- 不以 `sandbox-exec`、普通同 UID 子进程或用户可替换 helper 作为恶意代码沙箱；
- 不把 Linux `memfd`、`execveat(AT_EMPTY_PATH)` 或 TPM 证明伪装成 Darwin 等价物；
- 不因 macOS 本地可执行 `--version` 成功而声明 `supported`。

## 实施顺序

1. 先实现独立 Darwin launcher 与 provider/verifier Port，保持未配置时 fail closed；
2. 以固定 1.1.23/0.145.0 identity 生成真实 observation/evidence，完成正向与负向矩阵；
3. 维护者接受本 ADR 后，执行一次独立 consumer enablement 和本机 doctor/live probe/conformance；
4. required checks、secret scan、merge-tree 与独立 reviewer 全绿后，才允许调度 Qoder/Codex 真实只读任务。

在上述条件完成前，本 ADR 不改变 ADR 0037/0038 的 `unsupported` 结论，也不授权任何 registry enablement。
