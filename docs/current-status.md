# 当前可用能力

Marshal 正在从本地工具演进为长寿命、可自托管的 Runtime。下面只描述用户实际可以获得的能力，不用设计阶段代替完成状态。

## 现在可以使用

当前版本适合在 macOS 或 Linux 上，由单个用户把本地 Git 仓库任务交给 Coding Agent：

- 初始化独立的 Marshal 工作目录，不污染主 checkout；
- 使用 OpenCode 或 Pi 执行编码任务；Qwen Code 是否可调度以当前 `marshal doctor` 的 `supported` admission 为准；
- 为每个写任务创建独立工作区，避免直接修改用户当前工作目录；
- 在 Agent 结束后独立运行测试和交付物检查；
- 根据真实代码差异和检查结果进行审查与返工；
- 使用独立凭据创建 GitHub Draft PR；
- 在任务失败、中止或无需改动时保存结果记录；
- 对中断任务进行状态检查、恢复和安全清理。

这套本地能力已经通过真实 Agent、真实 GitHub Draft PR、Linux 与 macOS CI 验证，可以作为早期可用版本试用。

## Mac-first Adapter 现状（2026-08-30）

- Qoder 的路线要求固定 `/Users/gawain/.qoder/bin/qodercli/qodercli-1.1.23`。当前开发机虽存在该文件，但 `MARSHAL_QODER_PATH` 尚未绑定，且 PATH 中的 `1.1.27` 是不同 identity；当前没有可复用的最终 sourceHead live evidence。
- Codex 的路线要求 `0.145.0`。当前开发机实际探测到的是 `0.149.1`，不能把历史 `0.145.0` smoke 记录当作当前最终 bytes 的生产证据。
- 上述版本/路径问题与 fixed CLI production composition 是独立门禁；在 composition 闭合前，Qoder/Codex 均不得宣称为 Marshal production Worker。
- Qwen Code `0.21.11` 的本地命令可执行，但当前 Marshal admission 仍为 `unsupported/unprobed`。在 doctor 取得新鲜 `supported` 证据前，Marshal 不会直接调度它。

因此，“本地 CLI 能运行”与“Marshal 可安全调度该 Adapter”是两个不同结论；文档只采用后者作为生产可用依据。

## Pi-first Darwin 闭环检查点（2026-08-31）

在候选分支 `feat/pi-first-architecture-fix`（`5b95ed1`）上，已用固定 Node 路径、固定 Pi bundle 路径和空环境运行真实 `TestSealedChainReachesRunningWithRealPi`，sealed launch chain 两次通过。该证据证明 Pi 可以穿过当前 Darwin ordinary-user 的启动、工作区 descriptor 绑定和 process-supervisor 路径；它仍不是 `fixed ./bin/marshal` 完整 Run→worker.completed→独立 Decision→`ACCEPTED` 的发布证据。

本检查点修复了 live allocation 重封装路径、空环境 spawn payload，以及 Darwin 工作目录访问产生的 `NOTE_ATTRIB` 元数据噪声误报；descriptor/stat/path 重验仍保留。`go vet`、`make architecture-check` 与 `git diff --check` 通过；本机未安装 `staticcheck`，没有把它记作通过。完整 `productionruntime` 包仍有 3 个既有 owner-lock fixture 在本机环境失败，CLI canary 仍在 init 阶段失败，需在最终 fixed CLI composition 上继续收敛。

当前结论：Pi 已具备可复现的真实 sealed-launch provider 证据，但尚不能宣称 v1.0 worker lifecycle 或 RC1 已发布。Codex 本轮未启用。

## v1.0 正在建设

2026-08-30 的 `main@c6debd4` checkpoint 保持 RB1-authoritative existing-worktree Bind/Receipt/Release、recovery projection、RC1 build-once distribution、exact opt-in installer guard 与 immutable carrier checker/receipt Schema；本次提交仅校正文档与当前权威 HEAD，不改变运行时语义。该 exact-head CI 的 Ubuntu/macOS quality 尚在运行，secret scan 已通过，整体仍未全绿。这些仍是 component/admission 资产：完整 S1′（S1′-A reservation/full Attempt + S1′-B held descriptor/prepared proof/sealed successor，含 item 5 borrow seam/门禁）尚未进入 `main`，`3abed5a` 仍只是未合入候选；S2′、Attach/rebind、terminalization、最终 fixed-bin Pi→独立 Decision→`ACCEPTED`、真实 same-bytes canary/carrier、tag、GitHub prerelease 与 release asset 均未完成，不能据此宣称 RC1 或 stable 可用。

当前第一优先级严格按 ADR 0068 收敛 Mac-first RC1，不再把 server 或 stable 门禁插入首发关键路径：

- 完成完整 S1′：S1′-A reservation/full Attempt 与 S1′-B held descriptor/prepared proof/sealed successor，包括 item 5 borrow seam/门禁；
- 完成 S2′ fixed CLI production composition，并让真实 producer chain 消费已合入的 RB1 existing-worktree authority；
- 依次完成 Attach/rebind 与 terminalization；
- 由最终 fixed CLI 运行真实 Pi，经独立 ReviewDecision 进入 `ACCEPTED`；
- 对同一最终 Darwin arm64 bytes 产生 canary receipt/carrier，并完成 exact opt-in 安装验证、annotated tag 与 GitHub prerelease。

fixed `marshal control-plane serve`、managed signing/notarization 与 Linux production/release/stable authority 明确属于 RC1 后继，不阻塞 unsigned CLI-only RC1，也不能由 RC1 的 component 证据提前宣称完成。

这些能力目前处于 `COMPONENT` 或集成中，不能因为 package、测试或 API 已存在就表述为 `INTEGRATED`。

Cloudflare 完整生产拓扑、多节点 HA、多用户/多租户、完整 Provider SDK、Web UI 与复杂 Goal DAG 已明确延期到 1.x。

## 能力不会被混淆

文档中的“目标”表示已经确定的产品方向，不代表代码已经实现；“可用”只用于经过实际测试的能力。每个后续阶段都要经过实现、自动化测试、独立审查和远端 CI，才会更新本页。

如果你现在需要成熟的多用户云服务、远程 Sandbox 或 Web UI，建议等待相应版本，而不是根据设计文档自行推断支持情况。

## 接下来怎么走

近期建设顺序与上文 ADR 0068 关键路径完全一致：

1. 完成尚未进入 `main` 的完整 S1′-A/S1′-B；
2. 完成 S2′ fixed CLI production composition；
3. 依次完成 Attach/rebind 与 terminalization；
4. 由最终 fixed CLI 运行真实 Pi，并以独立 ReviewDecision 进入 `ACCEPTED`；
5. 对同一最终 Darwin arm64 bytes 完成 canary/carrier、安装验证、annotated tag 与 GitHub prerelease。

RC1 发布后才推进 fixed server、managed signing/notarization、Linux authority 与 stable release gate。

详细范围见 GitHub 上的 [ADR 0052](https://github.com/chiga0/marshal-harness/blob/main/docs/adr/0052-v1-release-scope-and-production-reachability.md)，实时工程状态见 [Roadmap](https://github.com/chiga0/marshal-harness/blob/main/docs/roadmap-status.md)。
