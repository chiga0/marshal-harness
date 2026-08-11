# 快速开始

本页只覆盖当前已经可用的 Local MVP。C/S Runtime、远程 Sandbox 和 Goal 控制器仍在 Roadmap 中，不属于当前安装后的默认能力。

## 1. 安装

要求 macOS 或 Linux，Go 版本以仓库 `go.mod` 为准。

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

## 2. 初始化仓库

进入准备交给 Coding Agent 的 Git 仓库：

```bash
marshal init
marshal doctor --json
```

Marshal 默认把运行状态、日志、缓存和任务 worktree 放在被 Git 忽略的 `.marshal/` 中，不修改主 checkout。

## 3. 配置 Worker

Worker 必须使用显式的可执行文件绝对路径。先让 `doctor` 给出只读建议：

```bash
marshal doctor --print-env
```

按输出设置一个或多个 Adapter，例如：

```bash
export MARSHAL_OPENCODE_PATH=/absolute/path/to/opencode
export MARSHAL_QWEN_PATH=/absolute/path/to/qwen
export MARSHAL_PI_PATH=/absolute/path/to/pi
```

再次运行 `marshal doctor --json`，确认目标 Adapter 为 `registered` 且 `supported`。

## 4. 执行一个任务

准备符合 [TaskSpec 契约](task-contract.md)的 `TASK.json` 和 `POLICY.json`：

```bash
marshal task plan --task TASK.json --policy POLICY.json --run RUN_ID
marshal task approve --run RUN_ID --gate plan --actor USER_ID
marshal task run --run RUN_ID
marshal task verify --run RUN_ID
marshal task review --run RUN_ID
```

`review` 首次调用会导出 ReviewPacket。生成结构化 ReviewDecision 后导入：

```bash
marshal task review --run RUN_ID --decision REVIEW.json
```

需要发布 Draft PR 时，先按[操作手册](operator-runbook.md)配置独立 GitHub Publisher 凭据，再执行：

```bash
marshal task approve --run RUN_ID --gate publish --actor USER_ID
marshal task publish --run RUN_ID
marshal task accept --run RUN_ID
```

Marshal 不自动 merge。

## 5. 出错时

先做只读检查，不手工删除 `.marshal/` 或任务 worktree：

```bash
marshal task status --run RUN_ID --json
marshal doctor --run RUN_ID --json
marshal task cleanup --run RUN_ID
```

更完整的恢复、介入和发布操作见[操作手册](operator-runbook.md)。想先理解系统为什么这样工作，继续阅读[核心概念](concepts.md)。
