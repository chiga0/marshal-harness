# Milestone 8 验收报告

- 验收日期：2026-08-13
- 范围：Sandbox SPI/Fake/Local conformance + embedded/local 纵切——按 [ADR 0017](adr/0017-provider-neutral-sandbox-contract.md) 与 [ADR 0018](adr/0018-control-plane-and-provider-ports.md) 冻结的 provider-neutral 契约与 authority/actor 分离，以 ADR 0018 §7 顺序硬门禁实施；`marshal-server` 与 Public API 属于 M9，不在本 Milestone
- 结论：`PASSED`；六个硬门禁 gate 全部合入 main 且各 PR 远端 CI 全绿，退出门禁通过

## 已交付

### 六个硬门禁（按 ADR 0018 §7 顺序合入）

- gate-1：authority 双键空间 AuthorityNamespaceId/SecurityDomainId + SideEffect authority-record Schema（PR #42）；
- gate-2：ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence Schema + attestation 全链绑定（PR #45）；
- gate-3：legacy v1alpha1 CapabilitySnapshot → ProviderCapabilitySnapshot 的 fail-closed mapper（PR #48）；
- gate-4：durable ProviderRegistration store + restart recovery（R2 lineage `m8-durable-registration-store-r2-20260812b`，PR #57；R1 的 PR #46 因 Secret scan 对测试 fixture 的 gitleaks 误报关闭，未合入）；
- gate-5：ProviderCapabilitySnapshot/ConformanceEvidence 的 snapshot/evidence validation（PR #47）；
- gate-6：enable DispatchLease match，按硬门禁顺序最后启用，前置缺失 claim/match fail closed（PR #60）。

### embedded 纵切

- 任务 A：internal/sandbox SPI 类型 + Fake Provider + conformance 套件（PR #75）；
- 任务 B：Local SandboxRunner 宿主进程执行 + lease 绑定 + receipt observation（PR #80）；
- typed cross-domain edge 记录类型 + fixture 矩阵残留（PR #61）。

### 同期合入的基础设施修复（简述，不属 M8 gate）

repo lock 有界重试（PR #50）、gitleaks 测试 allowlist（PR #58）、工具 allowlist 机械强制（PR #59）、verification gofmt 归一化（PR #62）、supervisor 核心（PR #72）、execution admission 身份绑定（PR #77）、状态转换通知钩子（PR #79）、adapter 版本集合门禁（PR #84）、qwen 0.21.11 支持（PR #95）。

## 验收证据

上述全部 PR 已合入 main（验收基线：main HEAD `4106d5a49aece9cd24ad08d7792d0ac44801c940`）；每个 PR 均在远端 CI（Quality、Secret scan 等全部 required checks）全绿后合入：

| 条目 | 内容 | PR | 远端 CI |
| --- | --- | --- | --- |
| gate-1 | authority 双键空间 + SideEffect authority-record Schema | [PR #42](https://github.com/chiga0/marshal-harness/pull/42) | 全绿后合入 main |
| gate-2 | ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence Schema + attestation 全链绑定 | [PR #45](https://github.com/chiga0/marshal-harness/pull/45) | 全绿后合入 main |
| gate-3 | legacy fail-closed mapper | [PR #48](https://github.com/chiga0/marshal-harness/pull/48) | 全绿后合入 main |
| gate-4 | durable ProviderRegistration store + restart recovery（R2 lineage） | [PR #57](https://github.com/chiga0/marshal-harness/pull/57) | 全绿后合入 main |
| gate-5 | snapshot/evidence validation | [PR #47](https://github.com/chiga0/marshal-harness/pull/47) | 全绿后合入 main |
| gate-6 | enable DispatchLease match（最后启用） | [PR #60](https://github.com/chiga0/marshal-harness/pull/60) | 全绿后合入 main |
| 纵切任务 A | internal/sandbox SPI + Fake Provider + conformance 套件 | [PR #75](https://github.com/chiga0/marshal-harness/pull/75) | 全绿后合入 main |
| 纵切任务 B | Local SandboxRunner + lease 绑定 + receipt observation | [PR #80](https://github.com/chiga0/marshal-harness/pull/80) | 全绿后合入 main |
| 纵切补充 | typed cross-domain edge 记录类型 + fixture 矩阵残留 | [PR #61](https://github.com/chiga0/marshal-harness/pull/61) | 全绿后合入 main |

gate-4 的 R1 实现与测试审查曾通过，但 PR #46 因 Secret scan（gitleaks generic-api-key）对测试 fixture 字面量赋值的误报关闭且未合入；改以 R2 lineage（TaskSpec 冻结 gitleaks-safe fixture 写法）重跑后经 PR #57 合入。

## 已知限制

- M8 各 gate 尚未整体接入最终 Runtime 执行路径：按 [Roadmap 状态](roadmap-status.md) 既有口径，`PASSED` 只表示退出门禁通过，不表示已落地的 gate 已接入执行路径；`marshal-server`、Public API 与 Durable Runtime 形态下的接入属于 M9。
- [Issue #25](https://github.com/chiga0/marshal-harness/issues/25) 的 typed reconciliation 实现仍在 lineage `adr0026-publication-reconcile-r4` 进行中（[ADR 0026](adr/0026-scm-merge-receipt-and-publication-reconcile.md) 契约已合入 PR #49）；在此之前临时护栏不变，误入 terminal `BLOCKED` 的 Run 保持终态，禁止手改状态或伪装修复。
