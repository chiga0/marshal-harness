# v1.0 Release Readiness 判定表

更新日期：2026-08-31（`main@3819462` fixed CLI real-Pi `ACCEPTED` checkpoint；RC1 尚未发布）

判定基准：[ADR 0052](adr/0052-v1-release-scope-and-production-reachability.md)、[ADR 0067](adr/0067-darwin-ordinary-user-launch-and-attach-recovery.md) 与 [ADR 0068](adr/0068-mac-first-cli-only-lifecycle-preview-rc1.md)。只记录已进入 `main` 或绑定精确 sourceHead 的证据；候选分支、单次 live pass、reviewer verdict 或 Accepted ADR 都不会自动升级成熟度。

## 当前权威 checkpoint

1. `main@3819462` 的 Darwin arm64 candidate bytes 已运行真实 Pi canary `RC1-PI-20260831-3819462`，由独立 Verification 与独立 reviewer 的 accept Decision 进入 `ACCEPTED`；Decision digest 为 `sha256:5d50b624e41419ef32a1d7251481d5843ab001d3affe0ef6c8a6aad5465df5e9`。该证据关闭 fixed CLI 完整生命周期可达性的核心风险，但不是 tag/publication authority，也不能复用于后续不同 sourceHead/bytes。
2. `main@3819462` 的 required CI 已把失败收敛到 architecture check，Secret scan 通过。后续本地修复把 `productionruntime` 对 supervisor mechanics 的越层调用收回 ResultIngress，并把重复 legacy selector 读取收敛为 invocation snapshot；architecture check、定向 test/race、vet、staticcheck 与 diff-check 已通过。该修复合入后必须重新构建 final bytes 和重跑 canary。
3. ADR 0068 的 environment selector/direct `Adapter.Run` fallback 必须从 production path 归零。本次 invocation snapshot 只消除重复读取和 CI 冻结债务，不能冒充 RC1 cutover 完成。
4. release workflow 必须改为 pre-tag build-once candidate → current-authority receipt/carrier → annotated tag → consume-existing-artifact prerelease；当前 tag 后四平台重建、无条件拒绝 `v1.*` 的 `publication_guard`、四资产 tag 校验与单资产 RC1 合同不一致。
5. build-once Darwin arm64 distribution、installer exact opt-in/fail-closed guard、immutable carrier checker、receipt Schema 与 hostile matrix均已实现；仍缺真实 receipt producer/admission、carrier assembly、annotated tag、GitHub prerelease 与 release asset。
6. 当前权威状态保持：`I186-R0: PASSED/DESIGN`、`I186-R1: IN_PROGRESS/INTEGRATED`、`I186-R2–R5: IN_PROGRESS/COMPONENT`、`I186-R6: PLANNED/DESIGN`。`ACCEPTED` canary 是实质进展，但在最终 sourceHead 的 bypass、恢复/负向和 publication chain 全部关闭前不升级成熟度。

历史候选 `d630aa2` 与前置 `d4b9647` canary 已被 `main@3819462` 的完整 `ACCEPTED` canary 取代，只保留为问题定位记录。

## ADR 0052 §1 对照

| # | ADR 0052 §1 要求 | 状态 | 当前证据 / 剩余缺口 |
| --- | --- | --- | --- |
| 1 | 唯一真实可恢复生产执行链 | `COMPONENT` | `main@3819462` 已证明 fixed CLI 主纵切可达；environment selector/direct fallback 仍可达，因此尚未满足“唯一”并保持 `COMPONENT`。 |
| 2 | 真实 AgentProvider + 真实 Local/Container SandboxProvider，Agent 进程实际在 allocation 内 | `INTEGRATED` | `main@3819462` 的真实 Pi 已由 fixed CLI 在 Local allocation 内执行并返回结果；ordinary-user 不等于 hardened sandbox，新 final bytes 仍须重跑。 |
| 3 | 文件型耐久 authority ledger、幂等提交、重启恢复、旧 generation fencing、单一恢复模型 | `COMPONENT` | `main@3819462` canary 已从 durable authority 进入 `ACCEPTED`，并由新进程重读一致终态；完整 crash/response-loss 矩阵与 selector 归零仍开放。 |
| 4 | 每 Attempt 双 binding + 接纳前 current-ledger recheck | `COMPONENT` | `main@3819462` 的单 current Attempt 已通过 owner/Attempt/generation/source/material current-ledger recheck；完整 ABA/漂移/重放负向矩阵仍须在新 final bytes 上完成。 |
| 5 | 可判定 cancel、timeout、retry、terminal 与 Outcome 语义 | `COMPONENT` | `main@3819462` 已证明成功路径的 terminalization、cleanup 与 `ACCEPTED` Outcome；失败、超时、取消与 response-loss 的发布前矩阵仍开放。 |
| 6 | 独立 Verification；发布仅 none / Draft PR，不 auto-merge | `INTEGRATED` | `main@3819462` 已由真实 fixed CLI Pi 结果产生 current Evidence，并导入独立 ReviewDecision 到 `ACCEPTED`，全程 `publication:none`；发布 sourceHead 改变后必须重跑。 |
| 7 | loopback server 能 start/cancel/query/restore 真实 Run | `COMPONENT` | controller component 已合入，但 ADR0068 对 RC1 明确移除此首发前置。fixed `marshal control-plane serve`、authenticated transport 与 durable delivery ledger是RC1后的stable后继，不能用来升级RC1或当前成熟度。 |
| 8 | kill/restart/lost-response/stale/binding-drift 故障注入与恢复测试 | `COMPONENT` | RB1 existing-worktree crash/replay/ABA/projection fixture 已存在；仍须覆盖 S1′ source/current object漂移、Attach只读零变更、owner/pending分型、完整 response loss、process reuse、unknown child零kill与terminal cleanup replay。 |
| 9 | macOS/Linux 稳定安装产物；macOS signing/notarization/release identity | `OPEN` | RC1 的 build-once dist contract、exact opt-in installer guard 与 immutable carrier validator 已实现；`main@3819462` same-bytes canary 已 `ACCEPTED`，但新 final bytes、receipt/carrier、tag、GitHub prerelease与资产尚未产生。server、managed signing/notarization、Linux production/release与stable受保护重建均为RC1后继。 |

## 最短剩余路径

唯一顺序是：

```text
合入 architecture CI 修复
  → 删除 production environment selector 与 direct Adapter.Run fallback
  → pre-tag build-once immutable candidate
  → 新 final bytes 的真实 Pi + 独立 Verification/ReviewDecision → ACCEPTED
  → current-authority receipt/carrier + exact opt-in/install/recovery/负向 gate
  → required CI 全绿 + annotated tag + consume-existing-artifact prerelease
  → unsigned CLI-only v1.0.0-rc1
```

RC1 发布后才进入 stable 后继：ADR0062 fixed `marshal control-plane serve`、authenticated transport/durable delivery ledger → Issue #212 managed signing/notarization、artifact/install signer 分权与 external current/high-water → Linux production/release gate → 新的受保护 final bytes stable canary 与 `v1.0.0`。

任一步都不能由“ADR 已接受”、“单次 canary 通过”、“server component 已存在”或“可以构建 dist”推导为 lifecycle 已实现、`ACCEPTED` 已达成、RC1 已发布或能力达到 `RELEASED`。

## Mac 本地质量门禁边界

当前开发机的企业终端策略会按新 Mach-O/CDHash 拦截未签名 Go test 二进制。固定路径复制不同 package/build 的 test binary 仍会产生不同身份；不得要求维护者反复批准、移除安全属性或绕过企业安全软件。

本机只把 `gofmt`、architecture check、`go vet`、staticcheck、compile-only、Schema/diff/secret/mergeability 与固定 `./bin/marshal` live observation 作为相应层级证据。Go unit/race 的执行证据来自相同 sourceHead 的 required GitHub macOS + Linux CI；compile-only 产物不得执行。RC1 还必须使用构建一次后冻结的同一 Darwin arm64 bytes 完成 host viability 与完整 lifecycle canary，不能用 `go run`、随机 helper、另一份“等价”binary或tag后重建替代。

## RC1 操作状态

ADR0068 的 build-once distribution contract、installer exact opt-in guard，以及 immutable carrier checker/receipt Schema 已实现并独立审查；这些组件只验证输入和封装，不会替代发布授权。虽然 `main@3819462` 已完成 fixed-bin Pi→独立 Decision→`ACCEPTED`，但后续代码变更要求新的 final bytes canary，且 receipt/carrier assembly、tag、GitHub prerelease 与 release asset 尚未完成，因此本文仍不提供可执行的 tag、安装或发布命令。后续操作必须来自同一 sourceHead 的已审查 release contract，并满足：annotated exact tag、Darwin arm64唯一支持资产、manifest/checksum/current object闭合、缺资产零fallback、零自动activation、独立Decision `ACCEPTED` 与同一最终 bytes。不得从历史 prerelease 页面复制 lightweight tag或自动发布步骤；历史资产说明见 [v1.0 RC prerelease 验证摘要](v1.0-rc-prerelease-verification.md)。
