# 当前可用能力

Marshal 正在从本地工具演进为长寿命、可自托管的 Runtime。下面只描述用户实际可以获得的能力，不用设计阶段代替完成状态。

## 现在可以使用

当前版本适合在 macOS 或 Linux 上，由单个用户把本地 Git 仓库任务交给 Coding Agent：

- 初始化独立的 Marshal 工作目录，不污染主 checkout；
- 使用已配置且被当前 `marshal doctor` 判为 `supported` 的 Coding Agent Adapter；本机可执行文件存在不等于 Marshal 已准入；
- 为每个写任务创建独立工作区，避免直接修改用户当前工作目录；
- 在 Agent 结束后独立运行测试和交付物检查；
- 根据真实代码差异和检查结果进行审查与返工；
- 使用独立凭据创建 GitHub Draft PR；
- 在任务失败、中止或无需改动时保存结果记录；
- 对中断任务进行状态检查、恢复和安全清理。

这套本地能力已经通过真实 Agent、真实 GitHub Draft PR、Linux 与 macOS CI 验证，可以作为早期可用版本试用。

## Mac-first Adapter 现状（2026-08-24）

- Qoder CLI `1.1.28` 配合 Adapter `0.1.8` 已完成 macOS `ordinary-user` registry/doctor 身份探测和真实只读 live probe，并报告 `supported`；它仍需 fresh live Worker smoke、transcript attestation 与独立 conformance，不是 hardened authority。
- Codex `0.145.0` 已完成两次独立 Mac ordinary-user smoke 审查并进入 `ACCEPTED`。这些 smoke 验证了 Worker、transcript、WorkerResult、路径身份和产物绑定，但没有产品代码变更，也没有远端发布或合并。
- Qwen Code `0.21.15` 命中当前 Adapter 的 `>=0.21.5 <0.22.0` 支持范围，并已由 `marshal doctor` 报告 `supported`；这只表示 ordinary-user Adapter 可调度，不表示 hardened authority。

因此，“本地 CLI 能运行”与“Marshal 可安全调度该 Adapter”是两个不同结论；文档只采用后者作为生产可用依据。

## 正在建设

以下能力已有明确设计，但当前发行版还不能提供：

- 常驻的 `marshal-server` 和面向客户端的网络接口；
- 远程任务队列与分布式执行；
- 可替换的远程 Sandbox，包括 Cloudflare Sandbox；
- 生产级数据库、对象存储、多节点恢复和高可用；
- 多用户身份、项目级权限和完整的服务端审计；
- 跨多个任务推进复杂目标、动态重规划和累计预算控制；
- Web 控制台和完整的 Provider SDK。

前期多视角调研、Worker-to-Worker 受控协调和 Goal 级自动复盘已有[设计说明](agent-collaboration-and-learning.md)，但尚未成为当前发行版的一等状态机或网络服务。现在可以按操作手册人工组织调研 Run、结构化汇总与复盘文档，不能据此宣称已交付自动协作或知识治理。

## 能力不会被混淆

文档中的“目标”表示已经确定的产品方向，不代表代码已经实现；“可用”只用于经过实际测试的能力。每个后续阶段都要经过实现、自动化测试、独立审查和远端 CI，才会更新本页。

如果你现在需要成熟的多用户云服务、远程 Sandbox 或 Web UI，建议等待相应版本，而不是根据设计文档自行推断支持情况。

## 接下来怎么走

近期建设顺序是：

1. 把现有本地能力迁移到统一的 Runtime 基座；
2. 提供常驻服务和远程任务分发；
3. 接入 Cloudflare Sandbox，并验证执行环境可以替换；
4. 完成生产存储、高可用、自托管部署和长时间稳定性测试；
5. 在稳定平台上增加复杂 Goal 编排。

详细的工程 Milestone、协议和验收记录保留在 GitHub 仓库中，供贡献者和维护者使用，不属于用户站点的默认阅读内容。
