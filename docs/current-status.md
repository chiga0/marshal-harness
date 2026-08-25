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

## 正在建设

Issue #186 正在把已经存在的组件收敛成唯一 Command/Result 主链。R0–R2 已完成，R3-A/R3-B 已接纳，当前推进 per-Attempt Agent/Sandbox 双 binding recheck；之后仍需恢复/explain、strangler cutover 和 conformance。下列能力虽然有设计或局部组件，当前发行版仍不能作为完整受支持能力提供：

- 常驻的 `marshal-server` 和面向客户端的网络接口；
- 远程任务队列与分布式执行；
- 可替换的远程 Sandbox，包括 Cloudflare Sandbox；
- 生产级数据库、对象存储、多节点恢复和高可用；
- 多用户身份、项目级权限和完整的服务端审计；
- 跨多个任务推进复杂目标、动态重规划和累计预算控制；
- Web 控制台和完整的 Provider SDK。

## 可以试行、但不是产品能力

- L 级或高风险任务可在编码前使用多个 `publication:none` 调研 Run，由 Lead 人工汇总后再进入 plan/admission；
- 大型或异常任务结束后可生成事实 closeout，并把因果解释和改进建议标为不可信 Assessment/Proposal；
- Worker 之间只通过已接纳计划中的不可变 Artifact ref 单向同步，不开放 mailbox 或自由 P2P chat；
- 复盘内容不会自动注入未来 Goal；跨 Goal 学习和知识快照仍待依赖与审计。

具体边界见[前期研讨、复盘与受控协作](agent-collaboration-and-learning.md)。

## 能力不会被混淆

文档中的“目标”表示已经确定的产品方向，不代表代码已经实现；“可用”只用于经过实际测试的能力。每个后续阶段都要经过实现、自动化测试、独立审查和远端 CI，才会更新本页。

如果你现在需要成熟的多用户云服务、远程 Sandbox 或 Web UI，建议等待相应版本，而不是根据设计文档自行推断支持情况。

## 接下来怎么走

近期建设顺序是：

1. 完成 Agent/Sandbox 双 Provider binding 与执行位置证据；
2. 收敛单一恢复模型和 `marshal explain`；
3. 通过 strangler cutover 删除 production host bypass；
4. 完成多拓扑 conformance、性能与 soak，再重排远程平台路线；
5. R6 后先评估 bounded Scheduler 和 Minimal Goal，再决定生态协议、复杂 Goal 与学习能力。

详细的工程 Milestone、协议和验收记录保留在 GitHub 仓库中，供贡献者和维护者使用，不属于用户站点的默认阅读内容。
