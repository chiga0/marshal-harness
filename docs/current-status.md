# 当前可用能力

Marshal 正在从本地工具演进为长寿命、可自托管的 Runtime。下面只描述用户实际可以获得的能力，不用设计阶段代替完成状态。

## 2026-09-01 `v1.0.0-rc1` 已发布

[`v1.0.0-rc1`](https://github.com/chiga0/marshal-harness/releases/tag/v1.0.0-rc1) 已发布为 unsigned、Mac-first、Darwin arm64、CLI-only 的 `darwin-local-dogfood` 生命周期预览。annotated tag object `e99326fa6b6e57a19db8d6404c56b3dcf396fdc7` 精确指向 sourceHead `c1407bd77924c97dc6784f4d81938a3f0bfa75f6`，candidate SHA-256 为 `f9ed7fa59d05f5e71fef7164b8015240497e1d18e25ef1d3f8e199c1378a3774`。

真实 Pi `0.84.4` canary run `33504020360` 与 finalize `33504247271` 已通过 fixed CLI、Local allocation、ResultIngress、独立 Verification/ReviewDecision 到达 `ACCEPTED`，receipt digest 为 `sha256:7bd5b500bbaff5c5b008922b713d9844b792a3e82ece4e4a46ccd837496b4525`。candidate exact-head CI run `33502847249` 的 Ubuntu、macOS、secret scan 全绿；release workflow run `33506656403` 只消费同一 carrier，不重建 candidate。

外部下载 release 后，二进制 SHA-256 与 canary 完全相同；临时目录安装成功，`marshal version --json` 精确返回 `1.0.0-rc1`、commit `c1407bd`、Go `1.26.6`、`darwin/arm64` 与 `darwin-local-dogfood`。安装必须显式指定 exact tag 和 `MARSHAL_LOCAL_DOGFOOD_PREVIEW=1`，安装器不会自动生成 activation，也不会修改 Gatekeeper、SIP 或 EDR。

本次结果只关闭 [ADR 0068](adr/0068-mac-first-cli-only-lifecycle-preview-rc1.md) 的 local-dogfood prerelease distribution exit。它不满足 ADR 0052 的 `RELEASED` 成熟度，也不是 production、managed、notarized、hardened、server、Linux 或 stable release。R2–R5 继续为 `COMPONENT`，R6 为 `IN_PROGRESS/COMPONENT`。

## 2026-09-01 stable server cutover 开始

独立 `cmd/marshal-server` 已按 [ADR 0062](adr/0062-fixed-marshal-production-server-mode.md) 从生产权威路径降权：删除 `--marshal-executable`、child `marshal task run`、Provider registration 写入口和相关环境转发；兼容进程只提供既有 Run/Task 查询与事件投影，所有 Task create/cancel、Run approval/start 在读取请求体或写入幂等账本前统一返回 `mutation-authority-unavailable`。启动 banner 明示 `mutationMode=disabled`，不存在通过 flag 或环境变量重新开启 mutation 的路径。

这关闭了“独立二进制仍可成为第二 authority root”的旧实现风险，但没有完成 stable server。下一相邻切片仍须在 fixed `marshal` 内实现 `marshal control-plane serve`，复用同一个 in-process `PublicApplicationPort`、`ProductionRuntime` owner 与恢复控制器，并通过 authenticated loopback、并发 owner、response-loss 和 restart recovery 矩阵。因此 fixed server finding 继续为 `INTEGRATION-OPEN`，R2–R6 成熟度不升级。

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
