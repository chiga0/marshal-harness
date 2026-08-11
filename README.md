# Marshal

[![CI](https://github.com/chiga0/marshal-harness/actions/workflows/ci.yml/badge.svg)](https://github.com/chiga0/marshal-harness/actions/workflows/ci.yml)
[![Pages](https://img.shields.io/badge/docs-GitHub_Pages-blue)](https://chiga0.github.io/marshal-harness/)
[![License](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**让 Agent 可以长期、可靠地完成软件工程任务。**

Marshal 是一个可自托管的任务控制系统。它持续接收新的开发任务，把复杂需求拆成有限、可检查的执行步骤，安排不同 Agent 和执行环境完成工作，并保留恢复、验证与审计所需的信息。

当前版本已经可以在本地完成 Coding Agent 的执行、独立验证、审查和 GitHub Draft PR 发布。常驻云端服务、远程 Sandbox 和跨任务 Goal 编排正在建设中。

[阅读文档](https://chiga0.github.io/marshal-harness/) · [查看当前能力](https://chiga0.github.io/marshal-harness/current-status/) · [快速开始](https://chiga0.github.io/marshal-harness/getting-started/)

## 为什么需要 Marshal

Coding Agent 很擅长修改代码，但单独使用时很难稳定回答：

- 它修改的是不是正确的代码和版本？
- 它声称运行过的测试是否真的通过？
- 任务中断后应该从哪里恢复？
- 执行代码的 Agent 是否也拿到了发布凭据？
- 几天后还能否知道结果为什么被接受或拒绝？

Marshal 把这些问题交给确定性的控制系统，而不是让 Agent 自己证明自己。

## 主要能力

- **长期运行**：Runtime 持续接受新任务；单次执行保持有限，失败后可以恢复或重新安排。
- **独立验证**：Agent 结束后重新观察真实改动并运行验收步骤。
- **安全委派**：执行与发布权限分离，默认只创建 Draft PR，不自动合并。
- **可插拔环境**：目标架构支持本地、容器和云端 Sandbox，不绑定单一供应商。
- **可审计结果**：成功、失败、中断和无需改动都会保存结果与原因。
- **复杂任务**：长期目标由多个有限任务逐步推进，而不是依赖永不退出的 Agent 会话。

## 当前可用版本

目前可用的是 macOS 与 Linux 上的本地单用户版本，支持：

- OpenCode、Qwen Code 和 Pi；
- 每个任务独立的 Git 工作区；
- 独立测试和交付物检查；
- 审查、有限返工与失败记录；
- 使用独立凭据创建 GitHub Draft PR；
- 中断后的检查、恢复和安全清理。

尚未交付：`marshal-server`、远程任务队列、Cloudflare Sandbox、生产级高可用、多用户服务、Web UI 和复杂 Goal 编排。详细状态见[当前可用能力](https://chiga0.github.io/marshal-harness/current-status/)。

## 安装

安装脚本不会请求 sudo：

```bash
curl -fsSL https://raw.githubusercontent.com/chiga0/marshal-harness/main/scripts/install.sh | bash
marshal version
```

也可以从源码构建：

```bash
git clone https://github.com/chiga0/marshal-harness.git
cd marshal-harness
make build
```

## 最小使用流程

在准备交给 Coding Agent 的 Git 仓库中：

```bash
marshal init
marshal doctor --json

marshal task plan --task TASK.json --policy POLICY.json --run RUN_ID
marshal task approve --run RUN_ID --gate plan --actor YOUR_ID
marshal task run --run RUN_ID
marshal task verify --run RUN_ID
marshal task review --run RUN_ID
```

执行结束不等于任务通过。`verify` 会独立检查改动，`review` 会准备真实代码差异和检查结果。发布 Draft PR 的完整流程见[日常使用](https://chiga0.github.io/marshal-harness/usage/)。

## 安全边界

当前本地版本可以隔离任务工作区、过滤环境变量并分离发布凭据，但普通本地子进程不是恶意代码沙箱。不要使用本地模式运行不可信仓库、依赖或构建脚本；这类任务应使用容器、虚拟机或经过验证的远程 Sandbox。

更多信息见[安全与隐私](https://chiga0.github.io/marshal-harness/security/)。安全问题请按照 [SECURITY.md](SECURITY.md) 私下报告。

## 开源与贡献

Marshal 采用 [MIT License](LICENSE)，目标是提供可二次开发、可自行部署的基础设施。

贡献前请阅读 [CONTRIBUTING.md](CONTRIBUTING.md) 和 [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)。面向实现者的架构规范、ADR、Schema、审计和 Milestone 证据保留在仓库 `docs/` 与 `schemas/` 中，但不进入用户文档站点。
