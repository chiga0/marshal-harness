# 当前可用能力

Marshal 正在从本地工具演进为长寿命、可自托管的 Runtime。下面只描述用户实际可以获得的能力，不用设计阶段代替完成状态。

## 2026-09-02 ADR 0075 修复已上 main，#226 仍阻塞 sealed StartPreparedRun

[ADR 0075](adr/0075-rc1-dogfood-usability-barriers.md) 已接受并在 main 上全部落地（`516e6a3`、`9029619`、candidate-mode `0a192ec`、workflow 链 `be3183b`/`0de20c5`），三组定向回归测试（0700 worktree 不变量、launch 输入 token 契约、终态单 JSON 提取）覆盖三种原文 dogfood 确定性屏障（#224、#225）。相关 exact-head CI 已全绿。build-from-head dogfood 实证：PrepareRunStart 全路径通过（ingress 八条事实完整晋级至 `prepared-execution-created`）。

当前阻塞已定位为 [#226](https://github.com/chiga0/marshal-harness/issues/226)：main-line composition session 重构（`e691aea`、`636c80d`）在 CLI-driven existing-worktree path-B 下的 sealed `StartPreparedRun` 确定性返回 `application: authority-conflict`（承袭内层映射），supervisor 从未 spawn；RC1 sourceHead `c1407bd` 在同一条链下可过，HEAD 不可过。本方向锁定 ADR 0075 域之外（独立回归），已按门禁单独归档，需要在 composition 层 review `RehydratePreparedRunStart` 与 adapter-supplied Prepare 的字段等价性。此前一个 path-B 诊断探针因未到达对比阶段即在 ComposeRuntime 失败，已按止损恢复（`be52850`），其同源 exact-head CI 已重绿。按最新审计，v1.0 stable 的 direct 顺序仍要求：先修复 #226 并重新跑通真实 dogfood 至 VERIFYING/REVIEW_PENDING/ACCEPTED，再恢复 fixed `marshal control-plane serve`、recovery/fault matrix、managed signing/notarization（Issue #212）、Linux stable gate 与 protected stable candidate。

## 2026-09-02 M13 GoalLite 真实任务 dogfood：worker 交付完成，collect finalize 暴露第三道 RC1 缺陷

以已发布 `v1.0.0-rc1`（digest 钉死、外部下载安装、无重建）执行首个真实相对复杂任务 `m13-goal-lite-walking-skeleton-20260902`：产出 GoalLite walking skeleton 的中文设计文档与两份 schema 样例（allowPaths 限定 3 文件，3 文件均真实写出）。GH macos-14 runner（workflow `m13-e2e-dogfood`，run [33578572483](https://github.com/chiga0/marshal-harness/actions/runs/33578572483)）上真实 Pi `0.84.4`（`pai-eas/qwen3.8-max`）通过 activation v2 → render → plan → approve → sealed run.start → spawn+resume → 完整执行 ~11.5 → terminal。

**度量**：worker 会话 API usage 汇总 `input=296,176 / output=82,326 / cacheRead=4,561,792 / cacheWrite=0 / reasoning=58,735`（token）；worker 执行 11.5 分钟；run 墙钟约 12.9 分钟；collect 轮询 196 轮期间 attach 健康。

**结论**：真实复杂任务的 supply→spawn→harvest 在发布二进制上可行且效率可度量；但首个 process-terminal collect 以 `application: authority-conflict` 确定性 fail closed（[Issue #225](https://github.com/chiga0/marshal-harness/issues/225)，result-ingress 事实链落在 collect command-outcome 后的组合层），生命周期未能到达 VERIFYING/REVIEW_PENDING。另已归档 [Issue #224](https://github.com/chiga0/marshal-harness/issues/224)（0700 worktree admission 与 CJK/空白后 `/` 的 launch 输入闸门，两道 PrepareRunStart 确定性屏障）。本机 macOS 26.6.2 上 pi 子进程另被宿主策略以 trace/BPT 杀死（属 [Issue #212](https://github.com/chiga0/marshal-harness/issues/212) 签名/宿主策略族）。

上述结果不改变发布基线与成熟度：RC1 仍为 unsigned local-dogfood prerelease；#224、#225 两道确定性体验缺陷与 #212 的 signing/notarization 均为 v1.0 stable 的硬门禁，不得伪称 stable。

## 2026-09-01 `v1.0.0-rc1` 已发布

[`v1.0.0-rc1`](https://github.com/chiga0/marshal-harness/releases/tag/v1.0.0-rc1) 已发布为 unsigned、Mac-first、Darwin arm64、CLI-only 的 `darwin-local-dogfood` 生命周期预览。annotated tag object `e99326fa6b6e57a19db8d6404c56b3dcf396fdc7` 精确指向 sourceHead `c1407bd77924c97dc6784f4d81938a3f0bfa75f6`，candidate SHA-256 为 `f9ed7fa59d05f5e71fef7164b8015240497e1d18e25ef1d3f8e199c1378a3774`。

真实 Pi `0.84.4` canary run `33504020360` 与 finalize `33504247271` 已通过 fixed CLI、Local allocation、ResultIngress、独立 Verification/ReviewDecision 到达 `ACCEPTED`，receipt digest 为 `sha256:7bd5b500bbaff5c5b008922b713d9844b792a3e82ece4e4a46ccd837496b4525`。candidate exact-head CI run `33502847249` 的 Ubuntu、macOS、secret scan 全绿；release workflow run `33506656403` 只消费同一 carrier，不重建 candidate。

外部下载 release 后，二进制 SHA-256 与 canary 完全相同；临时目录安装成功，`marshal version --json` 精确返回 `1.0.0-rc1`、commit `c1407bd`、Go `1.26.6`、`darwin/arm64` 与 `darwin-local-dogfood`。安装必须显式指定 exact tag 和 `MARSHAL_LOCAL_DOGFOOD_PREVIEW=1`，安装器不会自动生成 activation，也不会修改 Gatekeeper、SIP 或 EDR。

本次结果只关闭 [ADR 0068](adr/0068-mac-first-cli-only-lifecycle-preview-rc1.md) 的 local-dogfood prerelease distribution exit。它不满足 ADR 0052 的 `RELEASED` 成熟度，也不是 production、managed、notarized、hardened、server、Linux 或 stable release。R2–R5 继续为 `COMPONENT`，R6 为 `IN_PROGRESS/COMPONENT`。

## 2026-09-01 stable server cutover 开始

独立 `cmd/marshal-server` 已按 [ADR 0062](adr/0062-fixed-marshal-production-server-mode.md) 从生产权威路径降权：删除 `--marshal-executable`、child `marshal task run`、Provider registration 写入口和相关环境转发；兼容进程只提供既有 Run/Task 查询与事件投影，所有 Task create/cancel、Run approval/start 在读取请求体或写入幂等账本前统一返回 `mutation-authority-unavailable`。启动 banner 明示 `mutationMode=disabled`，不存在通过 flag 或环境变量重新开启 mutation 的路径。

这关闭了“独立二进制仍可成为第二 authority root”的旧实现风险，但没有完成 stable server。下一相邻切片仍须在 fixed `marshal` 内实现 `marshal control-plane serve`，复用同一个 in-process `PublicApplicationPort`、`ProductionRuntime` owner 与恢复控制器，并通过 authenticated loopback、并发 owner、response-loss 和 restart recovery 矩阵。因此 fixed server finding 继续为 `INTEGRATION-OPEN`，R2–R6 成熟度不升级。

第二段相邻 cutover 已把 `internal/server` 的 Run start 从 `RunExecutor func` 旁路改为唯一注入的 `PublicApplicationPort`：transport 先以 `InspectRun` 冻结 exact sequence、Attempt 与 `authorityHead`，调用 `PrepareRunStart` 后把 exact prepared Attempt 与 `preparationDigest` 一同写入 pending intent，再调用 `StartPreparedRun`；返回值改为 path-free `RunProjection`，正常成功停在权威 `RUNNING`，不会把结果收集或 Verification 冒充为 start 的一部分。响应丢失恢复只接受同一 prepared Attempt 且 current authority 上 sequence/head 均已前进的 receipt；定向测试覆盖不重复 Prepare/Start 与“其它 Attempt 不得被旧 intent 认领”的负向场景。

该段消除了 Run start 的 direct execution seam，但 server 仍保留旧 Task create/cancel、approval、query/event 资产，fixed `marshal control-plane serve` 及 repo-wide owner/session runtime 尚未实现；因此仍不得把 `internal/server` 视为完整 ADR 0062 adapter，也不得升级 stable/R2–R6 状态。下一切片必须先给 `ProductionRuntime` 建立可服务多个 Run 的 owner-scoped session/factory，再由 fixed CLI 注入，而不是在 HTTP 包内重新装配 per-Run authority。

第三段相邻 cutover 已新增 `RepositorySession`：fixed Marshal 进程只执行一次 Phase-A scope lock、durable owner append/replay、Phase-B bind、sealed ResultIngress 与 runtime claim；后续 Run-scoped `ComposeRuntime` 只借用同一 current owner/ingress，并各自持有和释放 Run lease。Session 关闭会等待在途 Run runtime，随后关闭 ingress 并最后释放 owner；关闭单个 Run 不再释放仓库 owner，已关闭 Session 也不能继续组合新 Run。定向测试覆盖同一 owner epoch 下连续组合两个 Run runtime、并发第二 Session fail closed、关闭等待、关闭后拒绝借用以及释放后严格 successor epoch。

本段没有新增 authority fact、Schema、状态转换或发布权限，因此沿用 ADR 0062/0066/0069 的既有合同；它只是 fixed server 的资源生命周期基础，尚未把 run resolver、完整 `PublicApplicationPort` 或 AF_UNIX transport 接入 `cmd/marshal`。因此 fixed server finding 仍为 `INTEGRATION-OPEN`，下一步是把 Run-specific composition assembler 收敛为 Session 之上的应用 Port，再实现 `marshal control-plane serve`。

第四段相邻 cutover 已把 fixed CLI 原先内联在 `task run` 中的 Run-specific composition 提取为 `sealedRepositoryApplication`。该 adapter 在一个 `RepositorySession` 下复用 held owner、ResultIngress、descriptor-bound Run store、dispatch ledger 与 Provider authority；每次 `InspectRun` / `PrepareRunStart` / `StartPreparedRun` 只短暂组合并关闭精确 Run runtime。启动时它通过 held StateRoot 枚举全部 Run，先对每个 `RUNNING` Run 执行既有 attach/rebind recovery，全部成功后才让 `Status` 返回 `ready`。CLI `task run` 已改为消费这同一个 application Port，而不是保留第二套装配流程。

本段同时关闭了一个提前发现的 namespace 漂移：现有 server compatibility 默认使用 `repo:<root>`，而真实 production owner 使用 canonical `<root>`。`server.Config` 现在允许 fixed composition 显式注入 exact `AuthorityNamespaceId`，并拒绝 tenant/control-plane/root 任一漂移；独立 compatibility server 的历史默认保持不变。该 checkpoint 仍未实现 `marshal control-plane serve` 命令、descriptor-relative authenticated AF_UNIX、peer/current-owner handshake 或 transport delivery ledger，因此 stable server 仍为 `INTEGRATION-OPEN`，不得升级 R2–R6。

## 2026-08-31 fixed CLI 生命周期检查点

固定候选 `main@3819462` 已构建为 `v1.0.0-rc1` Darwin arm64 local-dogfood bytes，并以真实 Pi 执行 canary `RC1-PI-20260831-3819462`。该 Run 只经 ResultIngress 接纳结果，由独立 Verification 生成 current Evidence，再由独立 reviewer 生成精确绑定 Evidence 的 accept Decision，最终由新的 Marshal 进程重读为 `ACCEPTED`。Decision digest 为 `sha256:5d50b624e41419ef32a1d7251481d5843ab001d3affe0ef6c8a6aad5465df5e9`。

该证据已经关闭“真实 Pi 是否能穿过 fixed CLI 完整生命周期”的核心不确定性，但尚未授权发布。以下列表记录的是 2026-08-31 当日遗留状态；architecture CI 与 selector 两项后来已经关闭，当前剩余路径以上方 2026-09-01 checkpoint 为准：

- `main@3819462` 的 required CI 只剩 architecture check 红灯；其根因是 `productionruntime` 越层读取 `processsupervisor` mechanics，以及同一 invocation 重复读取 legacy executor selector。本地修复保持 fail-closed 语义，并已通过 architecture check、定向 test/race、vet、staticcheck 与 diff-check；全包 Darwin test 仍受本机匿名 Go test Mach-O 身份策略和既有 owner fixture 影响，不能冒充通过。
- ADR 0068 要求 RC1 调用链中的 environment selector、legacy/direct `Adapter.Run` fallback 计数为零。本次 selector snapshot 只修复 CI 的冻结债务和同 invocation TOCTOU，不等于完成 RC1 cutover；下一切片必须删除 production selector 与 direct fallback。
- 当前 release workflow 仍会在 tag 后重建四平台产物，并由 `publication_guard` 无条件拒绝 `v1.*`；还没有把 pre-tag immutable candidate、current-authority canary receipt、RC1 单资产 tag 校验和 GitHub prerelease 串成无重建闭环。

因此最短剩余路径是：合入架构 CI 修复 → 删除 production selector/direct fallback → 在新 final sourceHead 构建一次并重跑真实 Pi `ACCEPTED`、恢复与负向门禁 → 生成 current-authority receipt/carrier → required CI 全绿 → annotated tag 与 GitHub prerelease。不得复用 `main@3819462` 的 digest 冒充后续最终 bytes。

## 现在可以使用

当前源码版适合在 macOS 或 Linux 上，由单个用户把本地 Git 仓库任务交给 Coding Agent；已发布 RC1 的封闭支持面仅为 Darwin arm64、可信用户、可信仓库和 `publication:none`：

- 初始化独立的 Marshal 工作目录，不污染主 checkout；
- 使用 OpenCode 或 Pi 执行编码任务；Qwen Code 是否可调度以当前 `marshal doctor` 的 `supported` admission 为准；
- 为每个写任务创建独立工作区，避免直接修改用户当前工作目录；
- 在 Agent 结束后独立运行测试和交付物检查；
- 根据真实代码差异和检查结果进行审查与返工；
- 使用独立凭据创建 GitHub Draft PR；
- 在任务失败、中止或无需改动时保存结果记录；
- 对中断任务进行状态检查、恢复和安全清理。

这套本地能力已经通过真实 Agent、真实 GitHub Draft PR、Linux 与 macOS CI 验证。Darwin arm64 用户现在可以显式安装 `v1.0.0-rc1` 试用 fixed CLI local-dogfood 纵切；其它平台继续使用源码/既有 release 路径，不能把 RC1 资产外推为 Linux 或 stable 支持。

## Mac-first Adapter 现状（2026-08-30）

- Qoder 的路线要求固定 `/Users/gawain/.qoder/bin/qodercli/qodercli-1.1.23`。当前开发机虽存在该文件，但 `MARSHAL_QODER_PATH` 尚未绑定，且 PATH 中的 `1.1.27` 是不同 identity；当前没有可复用的最终 sourceHead live evidence。
- Codex 的路线要求 `0.145.0`。当前开发机实际探测到的是 `0.149.1`，不能把历史 `0.145.0` smoke 记录当作当前最终 bytes 的生产证据。
- 上述版本/路径问题与 fixed CLI production composition 是独立门禁；在 composition 闭合前，Qoder/Codex 均不得宣称为 Marshal production Worker。
- Qwen Code `0.21.11` 的本地命令可执行，但当前 Marshal admission 仍为 `unsupported/unprobed`。在 doctor 取得新鲜 `supported` 证据前，Marshal 不会直接调度它。

因此，“本地 CLI 能运行”与“Marshal 可安全调度该 Adapter”是两个不同结论；文档只采用后者作为生产可用依据。

## 历史定位记录：Pi-first Darwin 启动检查点（已被完整 canary 取代）

在历史候选分支 `feat/pi-first-architecture-fix`（`d630aa2`，基于 `5b95ed1`）上，固定 Node/Pi bundle 与空环境曾两次通过 sealed launch chain。该记录只解释早期阻塞定位；上方 `main@3819462` 的完整 fixed CLI `ACCEPTED` canary 已取代它作为当前生命周期证据。

该历史检查点修复了 live allocation 重封装、空环境 spawn payload、Darwin 工作目录 `NOTE_ATTRIB` 噪声，以及普通 CLI 的 FD3/4 inherited-child 误判。后续提交已经接通 WorkerRequest、Pi 执行、结果接纳和独立 Verification；这些旧的“尚未接线”结论不再适用于当前主线。

## v1.0 stable 正在建设

RC1 已把 RB1 existing-worktree、sealed start、Attach/terminalization、ResultIngress、fixed CLI real-Pi `ACCEPTED` 和 same-bytes distribution 串成真实纵切。下一阶段不再重复构建 RC1，而是按 ADR 0052 收敛 stable v1.0：

- 实现并验证 fixed `marshal control-plane serve` 的唯一 composition 与 loopback authenticated transport；
- 关闭 kill/restart/cancel/timeout/response-loss/retry、重复 start、stale/replayed result 与 binding drift 的完整恢复/故障矩阵；
- provision 并验证 [Issue #212](https://github.com/chiga0/marshal-harness/issues/212) 的 macOS managed signing/notarization、protected producer 和 anti-rollback authority；
- 完成 Linux stable 产物、安装、升级/回滚和同路径 conformance；
- 从受保护输入重新构建 stable candidate，不能把 unsigned RC1 原地补签或 promote。

RC1 后可以并行推进 GoalLite/Agent Team 的最薄产品纵切，但不得让横向 Provider、Cloudflare、HA、多租户或复杂 Goal DAG 抢占 stable 主链。

这些能力目前处于 `COMPONENT` 或集成中，不能因为 package、测试或 API 已存在就表述为 `INTEGRATED`。

Cloudflare 完整生产拓扑、多节点 HA、多用户/多租户、完整 Provider SDK、Web UI 与复杂 Goal DAG 已明确延期到 1.x。

## 能力不会被混淆

文档中的“目标”表示已经确定的产品方向，不代表代码已经实现；“可用”只用于经过实际测试的能力。每个后续阶段都要经过实现、自动化测试、独立审查和远端 CI，才会更新本页。

如果你现在需要成熟的多用户云服务、远程 Sandbox 或 Web UI，建议等待相应版本，而不是根据设计文档自行推断支持情况。

## 接下来怎么走

近期建设顺序为：冻结 RC1 发布证据与复盘 → fixed server/recovery fault matrix → managed signing/notarization 与 Linux stable gate → 受保护 stable candidate → `v1.0.0`。GoalLite 只以不冲突的独立纵切并行推进。

详细范围见 GitHub 上的 [ADR 0052](https://github.com/chiga0/marshal-harness/blob/main/docs/adr/0052-v1-release-scope-and-production-reachability.md)，实时工程状态见 [Roadmap](https://github.com/chiga0/marshal-harness/blob/main/docs/roadmap-status.md)。
