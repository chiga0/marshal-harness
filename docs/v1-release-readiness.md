# v1.0 Release Readiness 判定表（ADR 0052 §1 逐条对照）

更新日期：2026-08-28（`main@64ec359` checkpoint）

判定基准：[ADR 0052](adr/0052-v1-release-scope-and-production-reachability.md) 第 1 节（九条）与第 3 节（生产可达性成熟度）。

口径：只记录已进入 `main` 或绑定精确 sourceHead 的证据；候选分支、单次 live pass 或 Accepted ADR 不会自动升级成熟度。

## 当前权威 checkpoint

1. `main@44ee8c9` 合入 durable `marshal-server` start/status/recovery controller。
2. `main@d4b9647` 合入 production selector 的组件门禁：production profile 只放行 `LaunchCapable` Provider，ordinary workspace Adapter 不得静默降级；但当前 release canary 仍通过 `MARSHAL_EMBEDDED_SANDBOX=1` 选择路径，ADR 0057 要求的唯一 `ProductionRuntime/PublicApplicationPort` composition 尚未收敛。
3. `main@912f659` 合入 ResultIngress admission→worker-result→Run journal 的 crash-atomic 持久化和恢复；replay/idempotency 不再是进程内唯一真值。
4. `main@ecee8d4` 接受 [ADR 0056](adr/0056-darwin-process-observation-and-attempt-terminalization.md)，冻结 Darwin ordinary-user 的 Core-owned process observation 与 Attempt terminalization 合同；RB2/B2 预审随后确认直接 `PT_TRACE_ME` 启动者无法把 wait right、held FD 与 pipe 转移给重启 Core，[ADR 0059](adr/0059-fixed-darwin-process-supervisor.md) 已接受并冻结固定 per-Attempt supervisor 合同，但尚未实现，恢复门禁仍开放。
5. `main@7651bc4` 合入 durable Attempt authority 与 ResultIngress eligibility barrier；`main@64ec359` 合入 durable effect intent/receipt/recovery authority。它们关闭了 component 级持久化缺口，但没有自动完成真实 process lifecycle/composition cutover。
6. `main@94ba223` 接受 [ADR 0058](adr/0058-interpreted-agent-launch-identity.md)，冻结解释器真实 process image 与 provider materials closure 合同；Accepted 只表示设计冻结，`ExecutableGID`、`LaunchMaterialsDigest` 等字段和生产接线尚未实现。
7. 前置 Pi `0.84.3` fixed-bin canary 绑定 `sourceHead=d4b9647`，单 Attempt 通过 9 项 Gate，到达 ReviewPacket/`REVIEW_PENDING`。它没有导入独立 ReviewDecision，没有进入 `ACCEPTED`，也不是当前 `main` 的最终发布证据。
8. unsigned RC 的 build/dist/install/release-contract 路径可行，但尚未发布任何 RC。稳定 `v1.*` 仍被 [Issue #212](https://github.com/chiga0/marshal-harness/issues/212) 的 macOS signing/notarization 和 Linux stable gate 阻断。

## ADR 0052 §1 对照

| # | ADR 0052 §1 要求 | 状态 | 当前证据 / 剩余缺口 |
| --- | --- | --- | --- |
| 1 | 唯一真实可恢复生产执行链 | `COMPONENT` | Pi 真实 result bytes 已进入 Core；server controller、crash-atomic ResultIngress、durable Attempt/effect authority 已合入。ADR 0056/0057/0058 的 process identity、terminalization 与唯一 production composition 仍未完成接线，尚不能证明旧 process group 与 successor 不双活。 |
| 2 | 真实 AgentProvider + 真实 Local/Container SandboxProvider，Agent 进程实际在 allocation 内 | `INTEGRATED` | R1 证据保留：Pi `0.84.3` 由 Local allocation 承载并返回真实 result bytes。ordinary-user 不等于 hardened sandbox。 |
| 3 | 文件型耐久 authority ledger、幂等提交、重启恢复、旧 generation fencing、单一恢复模型 | `COMPONENT` | `912f659`、`7651bc4`、`64ec359` 已依次闭合 ResultIngress、Attempt 与 effect 的 component 级 durable transaction；还需 ADR 0056 terminalization CAS/cleanup 复用同一 authority transaction 并通过完整 crash matrix。 |
| 4 | 每 Attempt 双 binding + 接纳前 current-ledger recheck | `COMPONENT` | durable binding/current-ledger 与 Attempt authority 实现已存在；ADR 0058 的真实解释器 process image/material closure 尚未实现，退出前还须覆盖单侧 revoke/replace/expiry、late result 与 ABA 负测。 |
| 5 | 可判定 cancel、timeout、retry、terminal 与 Outcome 语义 | `COMPONENT` | 既有状态机与恢复决策可用；ADR 0056 实现未完成 eligibility terminal→process cleanup→unlock/successor 的唯一顺序。 |
| 6 | 独立 Verification；发布仅 none / Draft PR，不 auto-merge | `INTEGRATED` | Local MVP 的独立 Verification 和发布权限分离已有生产路径；本次 Pi canary 仅到 `REVIEW_PENDING`，不是终态 Decision 证据。 |
| 7 | loopback `marshal-server` 能 start/cancel/query/restore 真实 Run | `COMPONENT` | controller 已于 `44ee8c9` 合入且 production selector 已于 `d4b9647` fail closed；还需在 ADR 0056 实现后重跑 server crash/lost worker/failed worker 终验。 |
| 8 | kill/restart/lost-response/stale/binding-drift 故障注入与恢复测试 | `COMPONENT` | 组件级 fixture 与 `912f659` 原子恢复回归已存在；直接 `PT_TRACE_ME` 不能跨 Core 转移 wait right/FD/pipe，ADR 0059 合同已接受但尚须实现，并补 process identity conflict、supervisor crash、detach、cross-orchestration zero-kill 及 cleanup crash 的真实 Darwin 纵切。 |
| 9 | macOS/Linux 稳定安装产物；macOS 须 signing/notarization/release identity 门禁 | `OPEN` | unsigned prerelease 路径可验证，但 RC 尚未发布。稳定版在 Issue #212 和 Linux stable gate 全绿前 fail closed。 |

## 现行阶段与最短剩余路径

- `I186-R0: PASSED/DESIGN`。
- `I186-R1: IN_PROGRESS/INTEGRATED`。
- `I186-R2–R5: IN_PROGRESS/COMPONENT`。
- `I186-R6: PLANNED/DESIGN`。

最短路径：以 durable Attempt/effect/allocation authority 为当前基线，实现已接受 ADR 0059 的固定 supervisor 与 Core 重连，再完成 ADR 0056/0057/0058 的 process observation、terminalization、解释器 materials closure 与唯一 production composition，并接入 `44ee8c9` server controller → 从最终 `main` 生成确定性 `dist`，让固定 `bin/marshal` 使用待发布 Darwin arm64 资产的 exact bytes，重跑 Pi canary并导入独立 ReviewDecision到 `ACCEPTED` → 以 annotated tag 冻结 candidate manifest/asset SHA，由 release runner 对同 sourceHead 跨主机重建并逐 SHA fail-closed 对比 → 发布 unsigned RC → 关闭 Issue #212 与 Linux stable gate 后才发布稳定 `v1.*`。

任一步都不能由“ADR 已接受”、“单次 canary 通过”或“可以构建 RC”推导为 lifecycle 已实现、`ACCEPTED` 已达成或 RC 已发布。

## Mac 本地质量门禁边界

当前开发机的企业终端策略会按新 Mach-O/CDHash 拦截未签名 Go test 二进制。即使把每个 package 的 test binary 复制到同一个固定路径，不同 package 和不同构建仍产生不同的 ad-hoc code identity；2026-08-28 的真实试跑在固定路径被 `SIGKILL`（退出码 137）。因此固定路径复制器不是可用的 Mac-first 方案，也不得要求维护者反复批准、移除安全属性或绕过企业安全软件。

在 Issue #212 的 Developer ID/signing/notarization 能力具备前，本机只提供 `gofmt`、architecture check、`go vet`、`staticcheck`、compile-only、Schema/diff/secret/mergeability 和固定 `./bin/marshal` live canary 证据；Go unit/race 的执行证据来自 required GitHub macOS + Linux CI。compile-only 产物不得执行，并应在验证后删除。最终 RC 必须绑定同一 sourceHead 的 required CI 全绿；本地静态门禁不能被表述成 unit/race 已通过。

## Tag 前 Mac-first Pi release canary

最终实现合入且 `main` clean、`HEAD == origin/main` 后，先以 peeled commit 的 UTC commit timestamp、`go.mod` 精确 toolchain、`CGO_ENABLED=0`、`-trimpath -buildvcs=false -mod=readonly` 与空 build ID 生成封闭 `dist`。随后只把待发布 Darwin arm64 资产复制到固定路径；禁止为 canary 另行 rebuild，也禁止用 `go run`、`go test` 临时二进制或其他 Marshal 副本充当权威身份：

```bash
FINAL_HEAD="$(git rev-parse HEAD)"
test "$FINAL_HEAD" = "$(git rev-parse origin/main)"
test -z "$(git status --porcelain --untracked-files=all)"

BUILD_DATE="$(scripts/release-contract.sh build-date . "$FINAL_HEAD")"
make dist \
  VERSION=1.0.0-rc1 \
  COMMIT="$FINAL_HEAD" \
  BUILD_DATE="$BUILD_DATE"
scripts/release-contract.sh verify-dist dist v1.0.0-rc1 "$FINAL_HEAD"
install -m 0755 dist/marshal_1.0.0-rc1_darwin_arm64 "$PWD/bin/marshal"
```

用持久 Run ID 启动真实 Pi `0.84.3` canary：

```bash
RUN_ID="v1.0.0-rc1-final-${FINAL_HEAD:0:12}"
scripts/release-canary.sh run \
  --run-id "$RUN_ID" \
  --expected-head "$FINAL_HEAD" \
  --expected-version 1.0.0-rc1

scripts/release-canary.sh status \
  --run-id "$RUN_ID" \
  --expected-head "$FINAL_HEAD" \
  --expected-version 1.0.0-rc1 \
  --expect REVIEW_PENDING
```

脚本只调用绝对路径 `$PWD/bin/marshal`，先验证其 SHA-256 精确等于 `dist/marshal_1.0.0-rc1_darwin_arm64`，再冻结 `RELEASE-MANIFEST` 摘要、本机 Pi 入口、Pi `0.84.3` bundle 路径及 SHA-256，以及已在 `main@d4b9647` fixed-bin canary 完成 WorkerResult 的 `qwen-token-plan-cn/qwen3.6-flash` 模型。Task draft 从仓库 happy-path example 派生，再由当前固定 Marshal 的 `task scaffold` 生成并验证 TaskSpec；PolicySnapshot 同样从仓库 example 派生，按 Core 的 detached `policyDigest` 规则封口，并由 `task plan` 再做 Schema、digest 和 capability admission。

状态边界是固定且分离的：operator control root 为源仓库的 `.marshal/release-canary/$RUN_ID/control/`；disposable Git repository 为 `.marshal/release-canary/$RUN_ID/repository/`；Core 的权威 Run/Attempt journal 位于该 disposable repository 自己的 `.marshal/runs/$RUN_ID/`。三者都落在源仓库已忽略的 `.marshal/` 下，但 control 文件不是 Core authority，不得用来改写 nested repository 的 `.marshal`。脚本退出或启动新 shell 后仍可用 `status` 从 Core journal 恢复同一 Run。脚本不隐式 fetch，要求本地 `refs/remotes/origin/main` 精确等于 `--expected-head`；非空 `MARSHAL_WORKER_EXECUTOR` 也会在任何 Marshal 调用及状态创建前被拒绝，随后显式 unset，production composition 仍只由固定 final binary 决定。任一 source HEAD、branch、canonical remote、dirty tree、Marshal version/profile/bytes、Pi path/version/bundle 摘要、WorkerResult model 或 Run 身份漂移都会 fail closed。

`run` 必须停在 `REVIEW_PENDING`。Lead 在另一个独立审查上下文中读取 `.marshal/release-canary/$RUN_ID/control/review-packet-output.json`，生成逐字段绑定当前 packet、Verification、ArtifactManifest、evidence 与 local self identity binding 的 `verdict=accept` ReviewDecision；canary driver 不生成 Decision，也不允许 Worker 自评。随后单独导入：

```bash
scripts/release-canary.sh finalize \
  --run-id "$RUN_ID" \
  --expected-head "$FINAL_HEAD" \
  --expected-version 1.0.0-rc1 \
  --decision "/absolute/path/to/lead-review-decision.json"

scripts/release-canary.sh status \
  --run-id "$RUN_ID" \
  --expected-head "$FINAL_HEAD" \
  --expected-version 1.0.0-rc1 \
  --expect ACCEPTED
```

只有 `finalize` 导入独立 Decision 后两次全新 Marshal 进程都恢复为 `ACCEPTED`，才允许创建 annotated tag。先用 `scripts/release-contract.sh candidate-tag-message dist v1.0.0-rc1 "$FINAL_HEAD"` 生成封闭 tag message，再以 `git tag -a v1.0.0-rc1 "$FINAL_HEAD" -F <message>` 创建 tag；lightweight tag、重复/未知 trailer 或摘要漂移均拒绝。Release workflow 的 read-only candidate job 会先要求同 sourceHead 的最新 `main` push CI 中 `Quality (ubuntu-latest)`、`Quality (macos-latest)` 与 `Secret scan` 精确全绿，再在 `macos-14` / `release-candidate-build` 中以同一 commit timestamp/toolchain/flags 执行唯一一次 `make dist`；`RELEASE-MANIFEST` 与 Darwin arm64 SHA 必须同 tag 中的 canary 摘要完全相等。冻结后输出 GitHub artifact ID、GitHub artifact digest 与内部 payload SHA，后续 job 只能按该 ID 消费，不能在 Linux publish runner 上重建另一份 Darwin bytes。独立 `contents:write` publish job 直接下载实际 artifact ZIP，将其 bytes 的 SHA-256 与 upload output 及同 workflow run/sourceHead 的 REST metadata digest 三方对账，受控解包后再校验 payload SHA、封闭 tar member/内部摘要/manifest/tag message，最后才调用 GitHub Release。整份 release workflow 另由版本化 exact-byte digest 封闭，不使用 substring/blacklist 作为权威合同。

这一步关闭的是 candidate→publish 的 rebuild/substitution 缺口，不把 GitHub-hosted runner 或 artifact digest 冒充 ADR 0048 authority。当前本地 canary 仍发生在 tag 前；要让 signing/install/canary 也消费 workflow 产生的同一 immutable candidate，后续必须把这些步骤加入同一 digest 链，并完成 sealed build input、authenticated build record、受保护 signer、安装 receipt/current/high-water 与外部 allowlist。annotated tag 仍未签名：它不能防 canonical remote ref 与 tag object 被整体重指，也不提供 anti-rollback；稳定 `v1.0.0` 继续被 Issue #212 阻断。

对同一 Decision 重跑 `finalize` 会再次核对 source/Pi/Run/Decision/candidate 身份与两次 Core `ACCEPTED`，但不会重复导入；传入不同 Decision 必须失败。任何失败均停止发布；不得改写 `.marshal`、复用旧 packet、调用 `task accept` 或把 `REVIEW_PENDING` 记作通过。脚本契约回归使用 `bash scripts/release-canary_test.sh`，该测试只运行固定路径 fake Marshal，不启动真实 Pi。
