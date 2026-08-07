# ADR 0014：Read-only 执行画像（调研/评审角色最小权限）

- 状态：提案（Proposed）
- 日期：2026-08-07
- 决策来源：fan-out 试点与评审团设计——调研/评审 Worker 目前持 workspace-write 全写权限，违反最小权限；"评审者能修改被评审代码"是信任模型缺口

## 背景

Marshal 现有唯一执行画像是 `workspace-write`：Worker 可读写任务 worktree 并运行开发命令。这对开发角色正确，但对 fan-out 的两种新角色过宽：

- **调研 Worker**：只需读源码、写一份报告；试点中它持有对整个 worktree 的写权限与 bash；
- **评审 Worker**：只需读冻结补丁与证据、写 findings；若可写 worktree，"Reviewer 独立性"在能力层就不成立——独立性目前只靠纪律与 Lead 审查保证，不靠能力边界。

AGENTS.md 不变量要求权限分离；角色画像缺失使该不变量在 Worker 层留了口子。

## 决策

### 新增 `read-only` 执行画像

语义精确定义为"**对源码只读、对产物写域受限**"：

- 读：worktree 全量 + TaskSpec 声明的 `readRoots`（仓库内相对路径，含符号链接目标，如 `sources/<repo>/`）；
- 写：仅 `control/output/` 与 TaskSpec `scope.allowPaths` 声明的产物路径（报告/findings 文件）；
- 命令：只读命令白名单（cat/head/tail/sed -n/rg/grep/find/ls/wc/file/stat 等），禁止 shell 组合、重定向写、网络与包管理；
- 不得 spawn 子 Agent；不得访问 Run Store 其他部分（沿用 ADR 0006）。

### Adapter 能力映射

每个 Adapter 必须证明能提供该画像，映射方式各自不同但语义一致：

- opencode：permission 配置对 allowPaths/readRoots 之外 deny edit、对写模式 deny bash、external_directory 仅读放行 readRoots；
- qwen：safe-mode + 工具排除 + approval-mode 只读化，写仅限产物路径；
- pi：工具 allowlist 保留 read/grep/find/ls 与 edit（edit 由 Marshal scope 门禁兜底产物路径），移除 bash。

Probe 的 CapabilitySnapshot 增加 `executionProfiles` 声明；TaskSpec 请求的画像不在 Adapter 能力中时，plan 阶段 fail-closed。

### Schema 与门禁

- `executionProfile` 枚举增加 `read-only`；TaskSpec 增加可选 `readRoots`（相对路径模式，Schema 校验禁止 `..` 与绝对路径）；
- Verification 增加 gate：read-only Attempt 的 observed diff 只允许出现在 allowPaths；denial log（ADR 0013）中出现对 readRoots 之外**写**的尝试即失败；
- 画像在 Run 内不可升级：read-only 的 Run 不能通过 rework 变成 workspace-write（需新 Run）。

## 后果

- 调研/评审 fan-out 获得能力层最小权限，Reviewer 独立性从纪律升级为边界；
- 评审团模式（Runbook §10.3）与调研队（§10.2）的 TaskSpec 模板默认改用 read-only；
- 成本：三 Adapter 能力映射 + Schema 变更 + 一致性测试，属契约级工作，需随实现走完整门禁。

## 备选方案

- 维持 workspace-write + TaskSpec 纪律（现状）：能力层缺口持续，否决为长期态；
- 进程级沙箱（容器/VM）：属延后阶段的 Hardened Profile，与本提案正交，不互为前提。
