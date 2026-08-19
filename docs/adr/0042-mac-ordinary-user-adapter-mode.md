# ADR 0042：Mac 普通用户 Adapter 模式

- 状态：用户明确授权实现（2026-08-19）；严格 authority 模式仍保持默认关闭
- 关联：ADR 0003、ADR 0014、ADR 0034、ADR 0037、ADR 0040、Issue #136、Issue #137

## 背景

当前 Darwin 没有 Linux `memfd`/`execveat`/pidfd 等 authenticated fd-exec 机制，且宿主未提供受管 Apple signing identity、root-owned APAP launcher、credential authority 与独立 verifier。严格 production authority 因此必须继续 `unsupported`。

用户明确选择先让 Qoder CLI 1.1.23 与 Codex CLI 0.145.0 以 Qwen/OpenCode 同级的普通用户子进程可用。该选择降低的是隔离与 authority 保证，不得被描述为 hardened sandbox、credentialed production authority 或恶意代码边界。

## 决策

新增显式 opt-in 的普通用户模式：

- `MARSHAL_QODER_MODE=ordinary-user`
- `MARSHAL_CODEX_MODE=ordinary-user`

未设置时，Qoder/Codex 继续走严格 authority 路径并保持 `unsupported`；不存在隐式降级或 PATH 回退。普通用户模式要求显式 absolute executable，并继续执行版本、realpath、SHA-256、超时、输出限制、完整替换环境、工作区边界和 WorkerResult/协议校验。

普通用户模式允许 Darwin 使用普通用户 pathname exec，不能接收 credential bytes、不能签发 receipt/evidence、不能启用 APAP、不能声称 child barrier、撤销 authority 或恶意代码 sandbox。doctor/CapabilitySnapshot 必须投影 `authorityMode=ordinary-user`，让使用者明确知道安全保证已降级。

Qoder/Codex 的严格 authority 配置、signed launcher、独立 verifier 与 credentialed conformance 不因该模式被删除；未来 provision 完整 authority 后，移除对应 mode 变量即可回到严格路径。

## 后果与门禁

- Mac 上可在不需要 root/signing 的前提下运行两个 CLI，行为口径与 Qwen/OpenCode 一致。
- 普通用户模式不能用于宣称强隔离、secret custody 或生产 authority；文档、doctor 和审计必须显式区分。
- 普通用户模式仍需独立协议/结果/路径边界测试、race、vet、staticcheck、secret scan、diff-check 与 merge-tree。
- Linux authenticated runtime 继续延后，不影响该 Mac-first 模式。
