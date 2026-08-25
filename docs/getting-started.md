# 快速开始

本页帮助你安装当前本地版本，并确认 Marshal 和 Coding Agent 可以正常工作。当前版本支持 macOS 与 Linux。

## 1. 安装

使用安装脚本：

```bash
curl -fsSL https://raw.githubusercontent.com/chiga0/marshal-harness/main/scripts/install.sh | bash
marshal version
```

或者从源码构建：

```bash
git clone https://github.com/chiga0/marshal-harness.git
cd marshal-harness
make build
```

源码构建后的命令位于 `bin/marshal`。

## 2. 初始化目标仓库

进入准备交给 Coding Agent 的 Git 仓库：

```bash
marshal init
marshal doctor --json
```

Marshal 会把任务状态、日志、缓存和独立工作区放在被 Git 忽略的 `.marshal/` 中，不会直接修改你的主 checkout。

## 3. 连接 Coding Agent

当前支持 OpenCode、Qwen Code 和 Pi。先让 Marshal 检查本机环境并给出配置建议：

```bash
marshal doctor --print-env
```

按照输出设置要使用的 Agent 路径，例如：

```bash
export MARSHAL_OPENCODE_PATH=/absolute/path/to/opencode
export MARSHAL_QWEN_PATH=/absolute/path/to/qwen
export MARSHAL_PI_PATH=/absolute/path/to/pi
```

你不需要同时安装三个 Agent。再次运行 `marshal doctor --json`，确认所选 Agent 已注册且版本受支持。

## 4. 准备任务

当前 CLI 使用两个 JSON 文件：

- `TASK.json`：要完成什么、允许修改哪些文件、怎样判断完成；
- `POLICY.json`：执行次数、时间、发布方式等限制。

可以从仓库中的[有效示例](https://github.com/chiga0/marshal-harness/tree/main/schemas/examples/happy-path)开始修改。不要把密码、Token 或其他敏感信息写入任务文件。

## 5. 运行任务

```bash
marshal task plan --task TASK.json --policy POLICY.json --run my-first-run
marshal task approve --run my-first-run --gate plan --actor YOUR_ID
marshal task run --run my-first-run
marshal task verify --run my-first-run
marshal task review --run my-first-run
```

执行结束不等于任务通过。`verify` 会独立检查真实改动和测试，`review` 会准备供人或 Lead Agent 审查的材料。

需要发布 Draft PR 时，再完成审查决定和发布授权。完整命令见[日常使用](usage.md)。

## 6. 查看和排错

```bash
marshal task status --run my-first-run --json
marshal doctor --run my-first-run --json
```

发生中断时先运行这两个只读命令，不要手工删除 `.marshal/` 或任务工作区。更多恢复和清理方法见[日常使用](usage.md)。

第一次接触完整 Control Plane 时，建议先读[十分钟理解 Marshal 架构](architecture-in-10-minutes.md)。大型、模糊或高风险任务在直接编码前，可按[前期研讨、复盘与受控协作](agent-collaboration-and-learning.md)和操作手册执行 Stage 0 调研 Pilot；这仍是人工操作约定，不代表 Goal 编排或 Worker mailbox 已实现。

## 当前限制

这个安装得到的是本地单用户版本，不包含常驻 `marshal-server`、远程 Sandbox、Web UI 或复杂 Goal 编排。最新进展见[当前可用能力](current-status.md)。
