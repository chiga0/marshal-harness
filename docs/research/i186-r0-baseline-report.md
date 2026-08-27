# I186-R0 Baseline 冻结报告

更新时间：2026-08-24

本报告按 [Issue #186](https://github.com/chiga0/marshal-harness/issues/186) Planning Baseline v3 的 R0 要求冻结当前现状事实，作为 `I186-R0 → R6` 收敛路线的可复现起点。本报告只记录事实，不改变 M0–M9 的历史结论，也不构成任何 production 状态声明。child Issue：[#187](https://github.com/chiga0/marshal-harness/issues/187)。

## 1. Baseline commit 与 toolchain

| 项 | 值 |
| --- | --- |
| baseline commit | `4d6ad29`（main；代码内容与 `26448f6` 一致，其后仅治理文档变更） |
| Go toolchain | `go1.27.0 darwin/arm64`（macOS 本地基线） |
| CI 矩阵 | GitHub Actions `32713604831`（commit `4d6ad29`，`success`，含 Linux + macOS + secret scan） |
| 治理文档 | `AGENTS.md`、`docs/roadmap-status.md` 已记录 I186 收敛路线修订（commit `4d6ad29`） |

复现方式：`git checkout 4d6ad29` 后执行 CI 同款命令（`go build ./...`、`go vet ./...`、全量 Go/Python 测试、secret scan），结果应与 `32713604831` 一致。

## 2. Local MVP 回归结果

Local MVP 保持 `USABLE`。M0–M6 的历史 `PASSED` 结论与证据链（见 [roadmap-status.md](../roadmap-status.md)）全部保留，不重写、不回滚。当前 main 的回归事实：

- Go 全量测试与 vet 在 CI 矩阵（Linux + macOS）持续 `success`；最近一次代码变更 `26448f6`（codex 测试构建根绝对路径修复）与其后文档变更 `4d6ad29` 的 CI 均绿。
- Skill 侧 Python 测试（`.agents/skills/marshal/references/tests/`）在干净 main 上全绿；历史一次性伪失败（脏工作树混合二进制导致的 codex checker-failed）已定性并记录，不改变回归基线。
- M8/M9 的 component gate `PASSED` 结论保留，但按 Planning Baseline v3 不再推导 integrated/production 状态。

## 3. 当前支持 Adapter 的 digest 与证据级别

证据级别沿用晋升阶梯口径：本节全部为 ordinary-user live probe / smoke 级证据，不构成 production authority、hardened authority 或恶意代码隔离声明。详表见 [roadmap-status.md](../roadmap-status.md) 「Mac-first Adapter 阶段性证据」章节。

| Adapter | 身份事实 | digest | 证据级别 |
| --- | --- | --- | --- |
| Qoder CLI `1.1.28` | adapter `0.1.8`（argv 含 `--allowed-tools Bash`、版本下限 `1.1.27`），`MARSHAL_QODER_MODE=ordinary-user` | executable `sha256:14b5aa00198986c2299084e5d87479d648db47fc4b85aaecb572e1cff3a1c4aa`；CapabilitySnapshot `sha256:52c5c45b16e8e6bcc390772e869de9ede48d9ea5cd6469e86b2632fffe68fba9`（Run `run-m10-wire-02-r2` planning selection 真实 probe） | live capability probe 通过；写任务在途 |
| Codex CLI `0.145.0` | ordinary-user smoke `mac-codex-ordinary-smoke-r19/r20-20260821` 均 `ACCEPTED` | executable `sha256:1da3f4e0e96028b8a771814293c3033dafd1971f943f6c7e79b0897fe705f590` | ordinary-user smoke 闭环（诊断任务，无产品 diff） |
| Qwen Code `0.21.15` | adapter `0.1.0`，semver 范围 `>=0.21.5 <0.22.0` | 见 doctor 报告 | 范围准入证据闭环，可调度普通 Worker |
| Pi / OpenCode | 未配置 | — | `not-probed` |

## 4. Child Issue index

| child Issue | 阶段 | 覆盖缺口 | 收口条件（Exit Gate 摘要） |
| --- | --- | --- | --- |
| [#187](https://github.com/chiga0/marshal-harness/issues/187) | I186-R0 | 无新增缺口；冻结现状与收敛设计 | 现状可复现；ADR accepted；M0–M9 历史未篡改；独立 reviewer 无 P0/P1 |
| [#186](https://github.com/chiga0/marshal-harness/issues/186) 路线全列与 Roadmap replan（2026-08-27） | I186-R1–R6 | 全部 failure inventory；四项 honest gaps 归后续 | 各阶段收口证据见 [audit-report.md](../audit-report.md) R3/R4/R5/R6 收口节；replan 快照见 [i186-r0-maturity-matrix.md](i186-r0-maturity-matrix.md)；R6 canary Run `run-i186-r6-canary` 独立验证 9 Gate 通过 |

R1–R6 的 child Issue 在各阶段启动时创建并补入本表；每行必须引用 [i186-r0-maturity-matrix.md](i186-r0-maturity-matrix.md) failure inventory 中的稳定缺口 ID。

## 5. 冻结语义

- 本报告冻结的是**事实快照**：R1 起的纵切实现与本 baseline 对比产生 normalized trace diff；任何声称「保持 Local MVP 零回退」的 cutover 必须重放第 2 节回归并引用本 commit。
- M10–M13 的代码切片（含 Run `run-m10-wire-02-r2` 的产物）在 `I186-R6 DONE` 前不进入 main production 路径，仅作为 R4/R6 的 recovery/conformance fixture 素材。
- 本报告不新增 ADR、不改变信任边界；增量 ADR 见 [ADR 0043](../adr/0043-worker-executor-profile-and-dual-binding.md)、[ADR 0044](../adr/0044-result-ingress-and-cold-hot-paths.md)、[ADR 0045](../adr/0045-strangler-cutover-and-single-recovery.md)（已接受，2026-08-24；接受只冻结合同，未实现，不升级 milestone 状态）。
