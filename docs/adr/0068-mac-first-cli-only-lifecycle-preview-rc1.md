# ADR 0068：Mac-first CLI-only 生命周期预览 RC1

- 状态：已接受（Accepted）
- 日期：2026-08-29
- 提议基线：`main@84d2dcd6bb78cb7fa47ed1d3040a1f3bea5a0f11`
- 接受记录：提案 `9cfa1b65275d2e23f18b958a05d027adec6af8fd` 经唯一独立 reviewer 审查，结论 `APPROVE`，`P0=0`、`P1=0`；接受只冻结 RC1 合同，不表示 RC1 已实现、已发布或取得 production/managed/stable authority，也不升级 R2–R6。
- 关联：[ADR 0047](0047-marshal-darwin-self-identity-and-release-signing.md)、[ADR 0048](0048-protected-build-input-and-artifact-attestation.md)、[ADR 0051](0051-darwin-local-dogfood-profile.md)、[ADR 0052](0052-v1-release-scope-and-production-reachability.md)、[ADR 0062](0062-fixed-marshal-production-server-mode.md)、[ADR 0065](0065-sealed-run-start-proof-and-one-way-composition.md)、[ADR 0066](0066-production-composition-owner-acquisition.md)、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)、[Issue #212](https://github.com/chiga0/marshal-harness/issues/212)

## 背景

当前最短生产纵切已经收敛到 fixed `marshal`、唯一 `ProductionRuntime` factory、durable authority、真实 Agent-in-Local allocation、`ResultIngress`、独立 Verification/Review 与 Outcome。本文所称 S1′/S2′ 分别是 ADR 0065 的 S1 与 ADR 0066 的 S2 在 ADR 0067 减法后的实现切片；S1′/S2′、Attempt terminalization 和同一最终对象 canary 仍未完成，fixed `marshal control-plane serve` transport 也尚未实现。

另一方面，ADR 0047/0048 的 managed/release 路径还要求外部 code-signing certificate、allowlist/deployment policy、protected producer、不同的 artifact/deployment signer、不可回滚 current/high-water 与 notarization。仓库可以实现这些合同的 producer、validator 和 workflow，但不能自行 provision Apple 或企业信任，也不能保证任意目标 Mac 的 EDR 接受新 bytes。若把这组外部条件继续作为首个 prerelease 的串行前置，真实 CLI 生命周期无法尽早交给操作者 dogfood；若把 unsigned/ad-hoc 产物直接描述成 production RC，则会违反既有身份与发布合同。

ADR 0051 已经为当前 Mac、当前可信用户、当前可信仓库与固定 Marshal 对象定义了 `darwin-local-dogfood`。本 ADR 复用这一低保证 profile，为 `v1.0.0-rc1` 定义一个有界、可分发但明确 non-production 的 CLI-only 生命周期预览。该 tag 名只是 prerelease 序列标识，不表示 v1.0 stable 的 server、Linux、managed identity、签名或 notarization 门禁已经通过。

## 决策

### 0. 精确取代范围

1. 本 ADR 只对 `v1.0.0-rc1` 及其逐字节相同的候选产物生效。它不改变 `v1.0.0` stable，也不自动适用于后续 RC；后续 prerelease 必须由 release policy 显式选择相同合同或另行决策。
2. 对 RC1，本 ADR精确部分取代 ADR 0051 §5、§6 中“local Run/Evidence/Outcome 不能成为 release exit evidence”的冲突部分：这些事实可以满足本文定义的 **local-dogfood prerelease distribution exit**，但继续不能满足 managed/release admission、production conformance、`RELEASED` 成熟度、stable promotion 或 publication authority。
3. 对 RC1，本 ADR精确部分取代 ADR 0052 §1 第 1、7、9 项和 §6 `R6` 中把 fixed server transport、Linux 稳定产物、macOS managed signing/notarization列为同一首发前置的部分。RC1只要求本文的 Darwin arm64 CLI纵切；ADR 0052 对 v1.0 stable 的完整范围、生产可达性与 `RELEASED` 定义保持有效。
4. 对 RC1，本 ADR精确部分取代 ADR 0062 §4 中“真实 RC canary 必须先取得 `darwin-managed-development` 或 notarized identity，且必须经 server mode完成”的部分。RC1改用 ADR 0051 的 `darwin-local-dogfood` activation，并只经 fixed Marshal CLI的 in-process `PublicApplicationPort` 完成 canary。ADR 0062 的唯一 binary、唯一 factory、owner、禁止 legacy/child-exec/selector旁路、fixed server mode合同与 stable发布门禁全部保留。
5. 本 ADR不取代 ADR 0047/0048 的 managed-development、notarized-release、protected build、artifact/install signer分权、current/high-water、deployment policy与 stable release合同；也不允许把 local artifact后补签、补 receipt或改 metadata后晋升。stable必须从受保护输入重新构建并走完整 producer chain。
6. 本 ADR不改变 Worker不能自证、单 worktree单写入者、Worker/Verifier/Reviewer/Publisher分权、ReviewDecision精确绑定Evidence、失败保存Outcome、普通宿主进程非恶意代码沙箱与默认不merge等通用不变量。

### 1. RC1 的唯一支持画像

RC1 只支持以下封闭画像：

| 维度 | RC1 支持值 |
| --- | --- |
| OS / architecture | `darwin/arm64` |
| Marshal profile | `darwin-local-dogfood` |
| 入口 | canonical fixed `./bin/marshal` CLI，经同一 in-process `PublicApplicationPort` |
| 操作者与仓库 | 当前受信任的单一普通用户、单一可信 canonical repository |
| 执行 | Local allocation、ordinary-user Agent、`workspace-write`、non-hardened |
| 生命周期 | `init/scaffold/plan/approve/run/verify/review/status/doctor/events` 与 `publication:none` 的本地 `ACCEPTED` |
| 发布副作用 | Marshal Run 的 `publication:none`；不得获得 Publisher、Forge、merge、deploy 或其它 credentialed effect |
| 保证 | 本机 local-dogfood 可用性与可恢复性；不提供 production、managed、notarized、enterprise-allowed、hardened 或跨主机保证 |

`marshal control-plane serve`、独立 `marshal-server`、remote/non-loopback API、远程 Provider、Cloudflare、Linux 生命周期和其它 OS/architecture 均不属于 RC1 支持面，不能进入 RC1 canary 或发布声明。若二进制仍包含这些开发/兼容命令或其它平台构建产物，其存在不构成支持；release contract、安装文档与 release notes 必须把它们标为 unavailable/unsupported 或 build-only diagnostic，不能把 component availability 冒充用户能力。

GitHub prerelease asset 的发布是仓库维护者的 distribution 操作，不是 Marshal Run 的 `publication`。它不能据此签发 `PublicationAuthorization`，也不能为任何业务 Run 开启 Publisher 或 credentialed effect。

### 2. 同一最终 bytes 与 local activation

1. RC1 candidate 必须 build-once 并形成 immutable、content-addressed final Darwin arm64 object。dist manifest、checksum、release asset、canary object 与安装后 canonical `./bin/marshal` 的 raw SHA-256、长度、sourceHead、version、build date、Go version、architecture 与 `selfProfile=darwin-local-dogfood` 必须逐项相等；canary 后不得重建、重签、strip、改写 metadata 或替换 bytes。
2. candidate只获得诊断 provenance和本文的 local-dogfood eligibility，不产生 `MarshalArtifactBuildAttestationV1`、`MarshalInstallReceiptV1`、deployment current/high-water或 managed/release authority。release notes必须明确它不是 ADR 0047/0048的受保护或已签 artifact。
3. 操作者必须为最终 canonical object生成并显式选择 ADR 0051的 closed `LocalDogfoodActivationV1`。每次 lifecycle mutation和Worker启动前继续重验 activation、canonical path、device/inode/size/raw SHA-256、sourceHead、profile与current-path object observation；`$PATH`命中、symlink trust anchor、另一份内容相同的 binary、`go run`和随机/匿名Mach-O helper均不得替代该对象。
4. activation只在same-UID trusted threat model内有效，受冻结的最大 freshness约束，并且只能绑定local/loopback、ordinary-user与`publication:none`。它不是安装收据、签名、allowlist、anti-rollback或跨主机 bearer grant。
5. 每次在新主机或新canonical repository安装，都必须重新完成bootstrap self diagnosis、host viability与该主机自己的activation；一台Mac成功不能推出其它Mac会被Gatekeeper或EDR接受。

### 3. RC1 退出门禁

RC1只有在以下门禁全部由同一 sourceHead和同一最终 bytes满足后才能发布为 GitHub prerelease：

1. **调用链门禁**：ADR 0065 S1经ADR0067收窄后的S1′与ADR 0066 S2经同一减法约束的S2′全部实现；fixed `./bin/marshal` CLI只经唯一production factory和`PublicApplicationPort`到达durable Run authority、Sandbox allocation、真实Agent、`ResultIngress`、Verification/Review与Outcome。legacy `execution.Run`/`Adapter.Run`、child CLI、environment selector、memory-only/Fake authority与独立server旁路计数为零。
2. **终态门禁**：真实Attempt的process observation、cancel/timeout/exit、ResultIngress admission、Run terminalization、allocation close与cleanup intent/receipt形成可恢复的唯一事实链；成功、失败、超时、取消与response loss均收敛到可判定Outcome，pending authority/effect/intervention为空。
3. **身份门禁**：final object在目标canonical path通过`version`、bootstrap `doctor --self`、完整`doctor`与local activation/current-object recheck。任何path/object/digest/sourceHead/profile/activation漂移均在Run/Attempt/Probe/Worker副作用前fail closed。
4. **宿主可执行性门禁**：同一最终对象在目标Mac上连续通过bootstrap与完整生命周期canary，不需要为随机新路径逐次批准。若对象被`SIGKILL`、Gatekeeper/EDR拒绝或无法执行，保留non-secret PID/CDHash/时间与安全产品观察，RC1保持blocked；不得循环重建、关闭宿主安全策略或把`spctl`/notarization缺失描述成已接受。
5. **真实canary门禁**：至少一个真实Pi ordinary-user Agent在Local allocation内完成单Run/单current Attempt；实际结果只经`ResultIngress`接纳，独立Verifier产生current Evidence，独立Reviewer的ReviewDecision精确绑定该Evidence并最终进入`ACCEPTED`，Run全程`publication:none`。
6. **恢复门禁**：至少两个fresh Marshal进程从同一durable authority重读一致的最终`ACCEPTED`与Outcome；在worker exit、result admission、terminal append、allocation close和cleanup边界注入crash/response loss时，不产生第二Attempt、第二Supervisor command、重复接纳、重复effect或不可判定终态。
7. **负向门禁**：publication/Publisher/Forge/merge/deploy/credentialed effect、remote/non-loopback、server证据、Linux证据、cross-profile evidence replay、activation expiry、binary/path drift、stale generation/result与ordinary-user Agent伪造hardened/location/infra-failure claim全部拒绝或不具RC1 eligibility。
8. **release门禁**：exact-head Linux与macOS CI、定向/相关race、`go vet`、staticcheck、Schema/example/diff/secret scan、dist manifest/checksum/install/release-contract测试全部通过；release workflow只消费已通过canary且digest冻结的同一Darwin arm64 bytes，不在tag job中重建另一份“等价”binary。

任一门禁缺失时，只能保留本地未发布candidate或发布纯诊断artifact；不得称“可用RC1”、production ready或v1.0完成。

### 4. 发布、安装与声明

RC1的release title和首段说明必须逐字表达以下事实：

> `v1.0.0-rc1` 是 unsigned、Mac-first、Darwin arm64、CLI-only 的 `darwin-local-dogfood` 生命周期预览，仅支持当前可信用户与可信仓库中的 `publication:none` 本地运行；它不是 production、managed、notarized、hardened、server 或 Linux release。

安装流程必须：

1. 要求用户显式选择精确tag与local-dogfood preview，不能由`latest`、stable channel或静默fallback安装；
2. 在写入canonical `./bin/marshal`前验证release manifest与SHA-256，写入后重新观察同一对象；
3. 不自动生成activation，不修改Gatekeeper/SIP/EDR，不删除provenance，不请求用户批准随机helper；
4. 引导操作者先运行`version`与`doctor --self`，再显式生成/检查activation；host viability或identity gate失败时保持blocked并给出safe reason；
5. 不把系统级convenience symlink或`$PATH`解析对象作为S1′/S2′ trust anchor。

RC1不得修改或覆盖`v1.0.0` stable channel，不得被原地promote。稳定版必须重新从受保护source/material producer chain构建，取得Developer ID签名、notarization、artifact/install分权、外部current/high-water与deployment policy，并满足fixed server、Linux与ADR 0052/0062剩余release gate。

### 5. 成熟度与后续顺序

RC1通过只证明这条固定CLI纵切在`darwin-local-dogfood`支持画像下达到`INTEGRATED`并可作为non-production prerelease分发。它不把能力升级为ADR 0052的`RELEASED`，也不关闭Issue #212的managed/release authority条件。

RC1后的固定顺序是：

1. 实现ADR 0062的`marshal control-plane serve`、authenticated loopback transport与durable delivery ledger，并用同一application Port/authority重跑真实canary；
2. 完成ADR 0047/0048的protected build、签名/notarization、artifact/install signer分权、receipt与external current/high-water/deployment policy；
3. 完成Linux production factory、真实Agent lifecycle、恢复与发布产物门禁；
4. 以新的受保护final bytes完成stable canary并发布`v1.0.0`。

## 后果

### 正向

- 不等待仓库无法自行provision的Apple/企业identity，即可尽早把真实fixed CLI生命周期交给当前Mac操作者dogfood；
- 复用S1′/S2′、durable authority、ResultIngress和终态链，不建设第二套“简化RC”状态机；
- release声明与证据适用性机械区分local preview和stable production，避免unsigned artifact冒充正式版本；
- server、Linux与managed identity仍有明确后继门禁，而不是从路线中删除。

### 代价与限制

- `rc1`名称容易被外部理解为接近stable，因此release title、安装opt-in、doctor和文档必须重复显示non-production边界；
- 产物只对完成本机activation与host viability的当前操作者可用，不能承诺任意Mac直接执行；
- 同一实现需要在server、managed identity和Linux完成后再次取得stable证据；但代码调用链复用，重复的是更高保证的证据而不是平行实现；
- 若S1′/S2′或terminalization未完成，本文不会把已有Local MVP/legacy path包装成RC1。

## 拒绝的替代方案

### 继续把server与managed-development identity作为RC1 blocker

拒绝用于首个prerelease。它保持最强保证，但把fixed server、受保护producer、两类signer、Apple certificate/notarization、企业allowlist与external current/high-water串成一条关键路径，其中多项不能由仓库独立完成；在等待期间不能获得真实CLI用户反馈。该路径继续作为stable合同。

### 只发布version/help/doctor可运行的unsigned诊断candidate

拒绝作为“可用RC1”。它实现快，但用户不能运行Agent或完成生命周期，对验证v1真实目标没有价值。若本文任一生命周期门禁失败，可以发布明确命名的diagnostic artifact，但不能使用本ADR的RC1可用性声明。

### 让local artifact后补签并promote stable

拒绝。它绕过ADR 0048的sealed source/material、authenticated build record与producer分权，也无法补造canary前不存在的authority。stable必须fresh protected rebuild。

## 实施顺序

1. 独立审查并接受本ADR；接受只冻结RC1合同，不表示实现或发布完成；
2. 严格按 S1′→S2′→ADR0067 Attach/rebind→terminalization 的顺序完成 authority 与恢复链，再由 fixed CLI 运行真实 Pi，并以独立 Verification/ReviewDecision 使真实纵切到达`ACCEPTED`；
3. 实现RC1 release-contract、build-once/dist manifest、explicit install opt-in、local activation与canary receipt；
4. 用同一final bytes在目标Mac执行host viability、真实Pi、独立Verification/Review、crash/recovery与负向矩阵；
5. required CI与release gate全绿后发布带固定negative claims的GitHub prerelease；
6. 恢复server、managed/notarized identity与Linux stable关键路径。
