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

## Mac-first Adapter 现状（2026-08-21）

- Qoder CLI `1.1.27` 已在固定路径完成 registry/doctor 身份探测，并报告 macOS `ordinary-user`、`supported`；这不是 hardened authority，也不是 production conformance 的替代品。首次使用该版本仍需 fresh live Worker smoke、transcript attestation 与独立 conformance。
- Codex `0.145.0` 已完成两次独立 Mac ordinary-user smoke 审查并进入 `ACCEPTED`。这些 smoke 验证了 Worker、transcript、WorkerResult、路径身份和产物绑定，但没有产品代码变更，也没有远端发布或合并。
- Qwen Code `0.21.11` 的本地命令可执行，但当前 Marshal admission 仍为 `unsupported/unprobed`。在 doctor 取得新鲜 `supported` 证据前，Marshal 不会直接调度它。

因此，“本地 CLI 能运行”与“Marshal 可安全调度该 Adapter”是两个不同结论；文档只采用后者作为生产可用依据。

## v1.0 正在建设

当前第一优先级不是继续扩展组件数量，而是把已有资产收敛为一条真实可达的生产链：

- `marshal` 或 loopback `marshal-server` 进入同一个 durable Run journal；
- Core-owned `WorkerExecutor` 把真实 Agent 放进 Local/Container allocation；
- 真实结果只经 `ResultIngress` 接纳，并执行 Agent/Sandbox 双 binding current-ledger recheck；
- restart、lost response、stale/replayed result、lease/binding drift 都能确定性恢复或拒绝；
- macOS 与 Linux 产出稳定安装物，macOS 正式包通过签名与 notarization。

这些能力目前处于 `COMPONENT` 或集成中，不能因为 package、测试或 API 已存在就表述为 `INTEGRATED`。

Cloudflare 完整生产拓扑、多节点 HA、多用户/多租户、完整 Provider SDK、Web UI 与复杂 Goal DAG 已明确延期到 1.x。

## 能力不会被混淆

文档中的“目标”表示已经确定的产品方向，不代表代码已经实现；“可用”只用于经过实际测试的能力。每个后续阶段都要经过实现、自动化测试、独立审查和远端 CI，才会更新本页。

如果你现在需要成熟的多用户云服务、远程 Sandbox 或 Web UI，建议等待相应版本，而不是根据设计文档自行推断支持情况。

## 接下来怎么走

近期建设顺序以 v1.0 生产可达性为唯一主线：

1. 接通真实 Agent-in-Local/Container walking skeleton；
2. 收敛 command/result authority 与 durable recovery；
3. 落地 Agent/Sandbox 双 binding 与 Core-held local process observation；
4. 完成单一恢复模型、strangler cutover 和旧 host bypass 移除；
5. 通过跨平台故障 conformance、签名/notarization 和 release gate。

详细范围见 GitHub 上的 [ADR 0052](https://github.com/chiga0/marshal-harness/blob/main/docs/adr/0052-v1-release-scope-and-production-reachability.md)，实时工程状态见 [Roadmap](https://github.com/chiga0/marshal-harness/blob/main/docs/roadmap-status.md)。
