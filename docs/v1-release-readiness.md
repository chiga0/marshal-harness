# v1.0 Release Readiness 判定表

更新日期：2026-09-01（activation V2 同布局 runner 迁移实现 checkpoint；RC1 尚未发布）

判定基准：[ADR 0052](adr/0052-v1-release-scope-and-production-reachability.md)、[ADR 0067](adr/0067-darwin-ordinary-user-launch-and-attach-recovery.md) 与 [ADR 0068](adr/0068-mac-first-cli-only-lifecycle-preview-rc1.md)。只记录已进入 `main` 或绑定精确 sourceHead 的证据；候选分支、单次 live pass、reviewer verdict 或 Accepted ADR 都不会自动升级成熟度。

## 当前权威 checkpoint

1. `main@3819462` 的 Darwin arm64 candidate bytes 已运行真实 Pi canary `RC1-PI-20260831-3819462`，由独立 Verification 与独立 reviewer 的 accept Decision 进入 `ACCEPTED`；Decision digest 为 `sha256:5d50b624e41419ef32a1d7251481d5843ab001d3affe0ef6c8a6aad5465df5e9`。该历史证据关闭 fixed CLI 完整生命周期可达性的核心风险，但不是当前 bytes 的 tag/publication authority。
2. ADR 0068 的 production environment selector/direct `Adapter.Run` fallback 已在 `b1e274f` 归零；`main@4fa1343` 的 required CI `33484291887` 已全绿。RC1 run phase `33477653933` 在 `60dda22` 到达 `REVIEW_PENDING`，证明 build-once candidate、真实 Pi、ResultIngress、Verification 与独立 Decision 输入链均可达。
3. finalize `33477984364` 没有进入 `ACCEPTED`：V1 activation 把 `device/inode` 纳入跨 runner subject；以相同 `activationId` 重签发得到的是新证据，Core 正确拒绝。ADR 0073 已接受，本切片把 portable subject 与 host-local object observation 分离、同步升级完整 V2 lineage，并删除重签发 workaround；该实现尚须 exact-head required CI 和一轮全新的 V2 run/finalize canary 验证。
4. RC1 canary workflow 已能在 finalize 成功后组装 receipt/carrier；release workflow 仍无条件拒绝 `v1.*` 且按通用四平台合同重建，尚未改为验证 current V2 canary carrier、消费同一 Darwin arm64 candidate 并发布 prerelease。
5. build-once Darwin arm64 distribution、installer exact opt-in/fail-closed guard、immutable carrier checker、receipt Schema 与 hostile matrix均已实现；仍缺当前 V2 sourceHead 的 `ACCEPTED` receipt/carrier、RC1 publication admission、annotated tag、GitHub prerelease 与 release asset。
6. 当前权威状态保持：`I186-R0: PASSED/DESIGN`、`I186-R1: IN_PROGRESS/INTEGRATED`、`I186-R2–R5: IN_PROGRESS/COMPONENT`、`I186-R6: PLANNED/DESIGN`。历史或 V2 canary 的成功都不能在当前 publication chain 关闭前单独升级成熟度。

历史候选 `d630aa2` 与前置 `d4b9647` canary 已被 `main@3819462` 的完整 `ACCEPTED` canary 取代，只保留为问题定位记录。

## ADR 0052 §1 对照

| # | ADR 0052 §1 要求 | 状态 | 当前证据 / 剩余缺口 |
| --- | --- | --- | --- |
| 1 | 唯一真实可恢复生产执行链 | `COMPONENT` | `main@3819462` 已证明 fixed CLI 主纵切可达，production selector/direct fallback 已在 `b1e274f` 归零；当前 V2 final bytes 尚未通过完整恢复/负向矩阵与 publication chain，因此保持 `COMPONENT`。 |
| 2 | 真实 AgentProvider + 真实 Local/Container SandboxProvider，Agent 进程实际在 allocation 内 | `INTEGRATED` | `main@3819462` 的真实 Pi 已由 fixed CLI 在 Local allocation 内执行并返回结果；ordinary-user 不等于 hardened sandbox，新 final bytes 仍须重跑。 |
| 3 | 文件型耐久 authority ledger、幂等提交、重启恢复、旧 generation fencing、单一恢复模型 | `COMPONENT` | `main@3819462` canary 已从 durable authority 进入 `ACCEPTED`，并由新进程重读一致终态；selector 已归零，但当前 V2 final bytes 的完整 crash/response-loss 矩阵仍开放。 |
| 4 | 每 Attempt 双 binding + 接纳前 current-ledger recheck | `COMPONENT` | `main@3819462` 的单 current Attempt 已通过 owner/Attempt/generation/source/material current-ledger recheck；完整 ABA/漂移/重放负向矩阵仍须在新 final bytes 上完成。 |
| 5 | 可判定 cancel、timeout、retry、terminal 与 Outcome 语义 | `COMPONENT` | `main@3819462` 已证明成功路径的 terminalization、cleanup 与 `ACCEPTED` Outcome；失败、超时、取消与 response-loss 的发布前矩阵仍开放。 |
| 6 | 独立 Verification；发布仅 none / Draft PR，不 auto-merge | `INTEGRATED` | `main@3819462` 已由真实 fixed CLI Pi 结果产生 current Evidence，并导入独立 ReviewDecision 到 `ACCEPTED`，全程 `publication:none`；发布 sourceHead 改变后必须重跑。 |
| 7 | loopback server 能 start/cancel/query/restore 真实 Run | `COMPONENT` | controller component 已合入，但 ADR0068 对 RC1 明确移除此首发前置。fixed `marshal control-plane serve`、authenticated transport 与 durable delivery ledger是RC1后的stable后继，不能用来升级RC1或当前成熟度。 |
| 8 | kill/restart/lost-response/stale/binding-drift 故障注入与恢复测试 | `COMPONENT` | RB1 existing-worktree crash/replay/ABA/projection fixture 已存在；仍须覆盖 S1′ source/current object漂移、Attach只读零变更、owner/pending分型、完整 response loss、process reuse、unknown child零kill与terminal cleanup replay。 |
| 9 | macOS/Linux 稳定安装产物；macOS signing/notarization/release identity | `OPEN` | RC1 的 build-once dist contract、exact opt-in installer guard 与 immutable carrier validator 已实现；历史 same-bytes canary 已 `ACCEPTED`，当前 V2 final bytes、receipt/carrier、tag、GitHub prerelease与资产尚未产生。server、managed signing/notarization、Linux production/release与stable受保护重建均为RC1后继。 |

## 最短剩余路径

唯一顺序是：

```text
合入 activation/observation/binding V2
  → exact-head required CI 全绿
  → 从 run phase 生成原始 V2 activation 与 immutable candidate
  → finalize 在新 runner 原样消费 activation，真实 Pi + 独立 Verification/ReviewDecision → ACCEPTED
  → current-authority receipt/carrier + exact opt-in/install/recovery/负向 gate
  → release workflow 验证 carrier 并消费同一 candidate
  → annotated tag + GitHub prerelease
  → unsigned CLI-only v1.0.0-rc1
```

RC1 发布后才进入 stable 后继：ADR0062 fixed `marshal control-plane serve`、authenticated transport/durable delivery ledger → Issue #212 managed signing/notarization、artifact/install signer 分权与 external current/high-water → Linux production/release gate → 新的受保护 final bytes stable canary 与 `v1.0.0`。

任一步都不能由“ADR 已接受”、“单次 canary 通过”、“server component 已存在”或“可以构建 dist”推导为 lifecycle 已实现、`ACCEPTED` 已达成、RC1 已发布或能力达到 `RELEASED`。

## Mac 本地质量门禁边界

当前开发机的企业终端策略会按新 Mach-O/CDHash 拦截未签名 Go test 二进制。固定路径复制不同 package/build 的 test binary 仍会产生不同身份；不得要求维护者反复批准、移除安全属性或绕过企业安全软件。

本机只把 `gofmt`、architecture check、`go vet`、staticcheck、compile-only、Schema/diff/secret/mergeability 与固定 `./bin/marshal` live observation 作为相应层级证据。Go unit/race 的执行证据来自相同 sourceHead 的 required GitHub macOS + Linux CI；compile-only 产物不得执行。RC1 还必须使用构建一次后冻结的同一 Darwin arm64 bytes 完成 host viability 与完整 lifecycle canary，不能用 `go run`、随机 helper、另一份“等价”binary或tag后重建替代。

## RC1 操作状态

ADR0068 的 build-once distribution contract、installer exact opt-in guard，以及 immutable carrier checker/receipt Schema 已实现；ADR0073 的 V2 迁移实现只修复同布局 runner 的证据连续性，不会替代发布授权。当前必须从新的 run phase 生成 V2 lineage，禁止复用 V1 `REVIEW_PENDING` 或重签发 activation；V2 finalize `ACCEPTED` 后还要把 release workflow 改为验证 receipt/carrier 并消费同一 candidate。因此本文仍不提供可执行的 tag、安装或发布命令。后续操作必须满足：annotated exact tag、Darwin arm64唯一支持资产、manifest/checksum/current object闭合、缺资产零fallback、零自动activation/重签发、独立Decision `ACCEPTED` 与同一最终 bytes。不得从历史 prerelease 页面复制 lightweight tag或自动发布步骤；历史资产说明见 [v1.0 RC prerelease 验证摘要](v1.0-rc-prerelease-verification.md)。
