# ADR 0012：废弃 Run 的显式 abort

- 状态：提案（Proposed）
- 日期：2026-08-07
- 决策来源：三个真实项目累积的 20+ 个死 Run（RETRY_PENDING 被放弃后无出口），cleanup 对非终态与无 Outcome 的 Run 双重拒绝，状态卫生无解

## 背景

生命周期定义了终态集合（ACCEPTED/REJECTED/BLOCKED/ABORTED/NO_CHANGE），但**没有任何命令能到达 `ABORTED`**：

- 被放弃的 RETRY_PENDING Run 永远悬置：非终态 → cleanup 拒绝；无 Outcome → 即使终态也拒绝；
- 真实使用中 Lead 换策略重开新 Run 是常态（任务拆解迭代、adapter 选型试错），死 Run 与其 worktree 持续堆积；
- 手工删除 worktree/状态目录违反 Cleanup Guard 纪律，且绕过证据留存要求。

需要一个受控的、留证据的生命周期出口。

## 决策

### 新命令与转换

`marshal task abort --run RUN_ID --actor ID --reason TEXT [--json]`：

- 仅允许从 `RETRY_PENDING` 发起（v1 范围）；其他状态一律固定错误拒绝；
- 转换 `RETRY_PENDING → BLOCKED`，事件类型 `run.aborted`，actor 为 human（沿用 approve 的 source 校验风格），payload 含 `terminalReason: "aborted-by-operator: <reason>"`；
- 必须持有 Run Lease（证明无其他进程在管理该 Run）；已终态 Run 再次 abort 失败；
- 不触碰 worktree、不产生任何远端副作用——worktree 的回收仍由 cleanup 在 abort 之后按既有守卫执行。

### Outcome 证据要求

abort 路径必须写入终态 Outcome（schema 合法、终态 BLOCKED），使 cleanup 的 `validateOutcome` 通过。这同时确立先例：**所有进入终态的路径都必须产出 Outcome**——worker-failure 路径当前缺失 Outcome（导致 BLOCKED 死 Run 连 cleanup 都不可清理），列为本 ADR 的伴随修复项。

### 为何目标是 BLOCKED 而非 ABORTED

状态表已定义 `ABORTED`，但其持久化前置条件与下游工具链（cleanup/doctor/Outcome 渲染）对 ABORTED 的覆盖未经实现与测试；v1 复用 BLOCKED 的完整工具链，以 `terminalReason` 区分来源，成本最低且语义自洽（"需要外部输入或能力"的广义阻塞）。是否在 v2 启用独立 ABORTED 状态，视 v1 使用数据决定。

### 不引入自动清理

abort 只转状态，不自动删除任何内容；清理仍需显式 cleanup 与既有守卫（dirty worktree 需导出与显式授权）。自动 GC/retention 强制执行另案（retentionDays 落地）。

## 后果

- 死 Run 获得合法出口，状态卫生闭环（abort → cleanup）；
- Outcome 全覆盖修复后，cleanup 对所有终态 Run 可用；
- 成本：reducer 一条转换 + CLI 一个命令 + Outcome 写入事务 + 测试；不改变信任边界。

## 备选方案

- 允许 cleanup 直接处理非终态 Run：绕过生命周期不变量，否决；
- 自动超时 GC：与"冻结 Run Deadline 由 CI 分类使用"的语义冲突，且无证据留存审查，否决为 v1。
