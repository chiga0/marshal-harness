# v1.0 Release Readiness 判定表（ADR 0052 §1 逐条对照）

更新日期：2026-08-28（`main@ecee8d4` checkpoint）

判定基准：[ADR 0052](adr/0052-v1-release-scope-and-production-reachability.md) 第 1 节（九条）与第 3 节（生产可达性成熟度）。

口径：只记录已进入 `main` 或绑定精确 sourceHead 的证据；候选分支、单次 live pass 或 Accepted ADR 不会自动升级成熟度。

## 当前权威 checkpoint

1. `main@44ee8c9` 合入 durable `marshal-server` start/status/recovery controller。
2. `main@d4b9647` 合入受支持的 production selector：production profile 只放行 `LaunchCapable` Provider，ordinary workspace Adapter 不得静默降级。
3. `main@912f659` 合入 ResultIngress admission→worker-result→Run journal 的 crash-atomic 持久化和恢复；replay/idempotency 不再是进程内唯一真值。
4. `main@ecee8d4` 接受 [ADR 0056](adr/0056-darwin-process-observation-and-attempt-terminalization.md)，冻结 Darwin ordinary-user 的 Core-owned process observation 与 Attempt terminalization 合同；实现和 production composition-root 接线仍开放。
5. 前置 Pi `0.84.3` fixed-bin canary 绑定 `sourceHead=d4b9647`，单 Attempt 通过 9 项 Gate，到达 ReviewPacket/`REVIEW_PENDING`。它没有导入独立 ReviewDecision，没有进入 `ACCEPTED`，也不是当前 `main` 的最终发布证据。
6. unsigned RC 的 build/dist/install/release-contract 路径可行，但尚未发布任何 RC。稳定 `v1.*` 仍被 [Issue #212](https://github.com/chiga0/marshal-harness/issues/212) 的 macOS signing/notarization 和 Linux stable gate 阻断。

## ADR 0052 §1 对照

| # | ADR 0052 §1 要求 | 状态 | 当前证据 / 剩余缺口 |
| --- | --- | --- | --- |
| 1 | 唯一真实可恢复生产执行链 | `COMPONENT` | Pi 真实 result bytes 已进入 Core；server controller 与 crash-atomic ResultIngress 已合入。ADR 0056 process/terminalization 实现未接线，尚不能证明旧 process group 与 successor 不双活。 |
| 2 | 真实 AgentProvider + 真实 Local/Container SandboxProvider，Agent 进程实际在 allocation 内 | `INTEGRATED` | R1 证据保留：Pi `0.84.3` 由 Local allocation 承载并返回真实 result bytes。ordinary-user 不等于 hardened sandbox。 |
| 3 | 文件型耐久 authority ledger、幂等提交、重启恢复、旧 generation fencing、单一恢复模型 | `COMPONENT` | `912f659` 已闭合 ResultIngress 与 Run journal 的 crash-atomic transaction；还需 ADR 0056 terminalization CAS/cleanup 复用同一 authority transaction 并通过完整 crash matrix。 |
| 4 | 每 Attempt 双 binding + 接纳前 current-ledger recheck | `COMPONENT` | durable binding/current-ledger 实现已存在；退出前还须在当前主线覆盖 Agent/Sandbox 单侧 revoke/replace/expiry、late result 与 ABA 负测。 |
| 5 | 可判定 cancel、timeout、retry、terminal 与 Outcome 语义 | `COMPONENT` | 既有状态机与恢复决策可用；ADR 0056 实现未完成 eligibility terminal→process cleanup→unlock/successor 的唯一顺序。 |
| 6 | 独立 Verification；发布仅 none / Draft PR，不 auto-merge | `INTEGRATED` | Local MVP 的独立 Verification 和发布权限分离已有生产路径；本次 Pi canary 仅到 `REVIEW_PENDING`，不是终态 Decision 证据。 |
| 7 | loopback `marshal-server` 能 start/cancel/query/restore 真实 Run | `COMPONENT` | controller 已于 `44ee8c9` 合入且 production selector 已于 `d4b9647` fail closed；还需在 ADR 0056 实现后重跑 server crash/lost worker/failed worker 终验。 |
| 8 | kill/restart/lost-response/stale/binding-drift 故障注入与恢复测试 | `COMPONENT` | 组件级 fixture 与 `912f659` 原子恢复回归已存在；尚缺 process identity conflict、detach、cross-orchestration zero-kill 及 cleanup crash 的真实 Darwin 纵切。 |
| 9 | macOS/Linux 稳定安装产物；macOS 须 signing/notarization/release identity 门禁 | `OPEN` | unsigned prerelease 路径可验证，但 RC 尚未发布。稳定版在 Issue #212 和 Linux stable gate 全绿前 fail closed。 |

## 现行阶段与最短剩余路径

- `I186-R0: PASSED/DESIGN`。
- `I186-R1: IN_PROGRESS/INTEGRATED`。
- `I186-R2–R5: IN_PROGRESS/COMPONENT`。
- `I186-R6: PLANNED/DESIGN`。

最短路径：以 `912f659` 的 crash-atomic ResultIngress transaction 为唯一基线，实现 ADR 0056 的 Core-owned process observation/terminalization 并接入 `44ee8c9` server controller → 在最终 `main` 重跑 fixed-bin Pi canary、导入独立 ReviewDecision 并进入 `ACCEPTED` → 运行 release-contract/dist/install 终验并发布 unsigned RC → 关闭 Issue #212 与 Linux stable gate 后才发布稳定 `v1.*`。

任一步都不能由“ADR 已接受”、“单次 canary 通过”或“可以构建 RC”推导为 lifecycle 已实现、`ACCEPTED` 已达成或 RC 已发布。
