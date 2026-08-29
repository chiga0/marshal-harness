# v1.0 Release Readiness 判定表

更新日期：2026-08-29（ADR 0067/0068 acceptance checkpoint）

判定基准：[ADR 0052](adr/0052-v1-release-scope-and-production-reachability.md)、[ADR 0067](adr/0067-darwin-ordinary-user-launch-and-attach-recovery.md) 与 [ADR 0068](adr/0068-mac-first-cli-only-lifecycle-preview-rc1.md)。只记录已进入 `main` 或绑定精确 sourceHead 的证据；候选分支、单次 live pass、reviewer verdict 或 Accepted ADR 都不会自动升级成熟度。

## 当前权威 checkpoint

1. `main@44ee8c9` 的 durable server controller、`main@d4b9647` 的 production selector，以及 `main@912f659` 的 ResultIngress admission→worker-result→Run journal crash-atomic 持久化/恢复均是已合入 component 资产；它们不等于 fixed CLI production composition，也不把 server 变成 RC1 前置。
2. 前置 Pi `0.84.3` canary 绑定 `sourceHead=d4b9647`，单 Attempt 通过 9 项 Gate 到 `REVIEW_PENDING`；它没有独立 ReviewDecision、没有进入 `ACCEPTED`，也不是当前 `main` 或最终 bytes 的 RC1 证据。
3. ADR 0056、0059–0066 的 process、terminalization、Supervisor、PreparedExecution、sealed proof 与 production factory 合同继续有效；[ADR 0067](adr/0067-darwin-ordinary-user-launch-and-attach-recovery.md) 已接受其 Mac ordinary-user 减法与精确取代范围。旧候选 `a6a0d63`、`506a647`、`6298eae` 冻结且不合入，S1′/S2′ 尚未实现。
4. [ADR 0068](adr/0068-mac-first-cli-only-lifecycle-preview-rc1.md) 已接受 unsigned Darwin arm64 CLI-only local-dogfood RC1 合同，但 installer guard、same-bytes canary 和 release asset 均未实现，当前没有已发布或可执行安装的 `v1.0.0-rc1`。
5. 当前权威状态保持：`I186-R0: PASSED/DESIGN`、`I186-R1: IN_PROGRESS/INTEGRATED`、`I186-R2–R5: IN_PROGRESS/COMPONENT`、`I186-R6: PLANNED/DESIGN`。

## ADR 0052 §1 对照

| # | ADR 0052 §1 要求 | 状态 | 当前证据 / 剩余缺口 |
| --- | --- | --- | --- |
| 1 | 唯一真实可恢复生产执行链 | `COMPONENT` | durable controller、ResultIngress 与 authority components 已存在；必须按 S1′→S2′ 接入 fixed CLI 唯一 factory/`PublicApplicationPort`，legacy/Fake/child CLI/第二 authority root 均不可达。 |
| 2 | 真实 AgentProvider + 真实 Local/Container SandboxProvider，Agent 进程实际在 allocation 内 | `INTEGRATED` | R1 历史证据保留：Pi `0.84.3` 由 Local allocation 承载并返回真实 result bytes；ordinary-user 不等于 hardened sandbox，最终 RC1 仍须用同一最终 bytes 重跑。 |
| 3 | 文件型耐久 authority ledger、幂等提交、重启恢复、旧 generation fencing、单一恢复模型 | `COMPONENT` | ResultIngress/Attempt/effect component transaction 已存在；S1′ proof、自身账本 response-loss replay、Attach/rebind、terminalization CAS 与 cleanup 顺序仍未形成同一 fixed CLI 纵切。 |
| 4 | 每 Attempt 双 binding + 接纳前 current-ledger recheck | `COMPONENT` | accepted contracts 已冻结 current owner/Attempt/generation/source/material 重验；S1′/S2′真实 producer、held-owner successor与负向 ABA/漂移矩阵尚未实现。 |
| 5 | 可判定 cancel、timeout、retry、terminal 与 Outcome 语义 | `COMPONENT` | 既有状态机与恢复决策可用；仍须实现 ADR0067 pre-start no-effect/permanent-intervention 二分，以及 ADR0056/0061 terminalization/transcript/cleanup 唯一事实链。 |
| 6 | 独立 Verification；发布仅 none / Draft PR，不 auto-merge | `INTEGRATED` | Local MVP 已有独立 Verification 与权限分离；RC1 必须由真实 fixed CLI Pi 结果产生 current Evidence，再导入独立 ReviewDecision 到 `ACCEPTED`，全程 `publication:none`。 |
| 7 | loopback server 能 start/cancel/query/restore 真实 Run | `COMPONENT` | controller component 已合入，但 ADR0068 对 RC1 明确移除此首发前置。fixed `marshal control-plane serve`、authenticated transport 与 durable delivery ledger是RC1后的stable后继，不能用来升级RC1或当前成熟度。 |
| 8 | kill/restart/lost-response/stale/binding-drift 故障注入与恢复测试 | `COMPONENT` | 组件 fixture 已存在；仍须覆盖 source/current object漂移、Attach只读零变更、owner/pending分型、crash/response loss、process reuse、unknown child零kill与cleanup replay。 |
| 9 | macOS/Linux 稳定安装产物；macOS signing/notarization/release identity | `OPEN` | ADR0068只允许先发布unsigned Darwin arm64 CLI-only RC1；它要求精确opt-in、缺资产不fallback、不自动activation和same-bytes canary。server、managed signing/notarization、Linux production/release与stable受保护重建均为RC1后继。 |

## 最短剩余路径

唯一顺序是：

```text
S1′ fresh-start sealed proof
  → S2′ fixed CLI production factory + 完整 PrepareRunStart producer chain
  → held owner/acquisition + control-owner-bound successor + read-only Attach/rebind
  → terminalization/transcript/allocation close/cleanup
  → fixed CLI 真实 Pi + 独立 Verification/ReviewDecision → ACCEPTED
  → 同一最终 Darwin arm64 bytes 的 exact opt-in/canary/install/recovery gate
  → unsigned CLI-only v1.0.0-rc1
```

RC1 发布后才进入 stable 后继：ADR0062 fixed `marshal control-plane serve`、authenticated transport/durable delivery ledger → Issue #212 managed signing/notarization、artifact/install signer 分权与 external current/high-water → Linux production/release gate → 新的受保护 final bytes stable canary 与 `v1.0.0`。

任一步都不能由“ADR 已接受”、“单次 canary 通过”、“server component 已存在”或“可以构建 dist”推导为 lifecycle 已实现、`ACCEPTED` 已达成、RC1 已发布或能力达到 `RELEASED`。

## Mac 本地质量门禁边界

当前开发机的企业终端策略会按新 Mach-O/CDHash 拦截未签名 Go test 二进制。固定路径复制不同 package/build 的 test binary 仍会产生不同身份；不得要求维护者反复批准、移除安全属性或绕过企业安全软件。

本机只把 `gofmt`、architecture check、`go vet`、staticcheck、compile-only、Schema/diff/secret/mergeability 与固定 `./bin/marshal` live observation 作为相应层级证据。Go unit/race 的执行证据来自相同 sourceHead 的 required GitHub macOS + Linux CI；compile-only 产物不得执行。RC1 还必须使用构建一次后冻结的同一 Darwin arm64 bytes 完成 host viability 与完整 lifecycle canary，不能用 `go run`、随机 helper、另一份“等价”binary或tag后重建替代。

## RC1 操作状态

当前尚未实现 ADR0068 的 build-once carrier、installer exact opt-in guard、same-bytes canary receipt与release gate，因此本文不提供可执行的 tag、push、安装或发布命令。实现完成后，操作说明必须来自同一 sourceHead 的已审查 release contract，并满足：annotated exact tag、Darwin arm64唯一支持资产、manifest/checksum/current object闭合、缺资产零fallback、零自动activation、独立Decision `ACCEPTED` 与同一最终 bytes。不得从历史 prerelease 页面复制 lightweight tag或自动发布步骤；历史资产说明见 [v1.0 RC prerelease 验证摘要](v1.0-rc-prerelease-verification.md)。
