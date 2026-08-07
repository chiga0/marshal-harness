# ADR 0013：Permission 拒绝分级（预期内 vs 致命）

- 状态：提案（Proposed）
- 日期：2026-08-07
- 决策来源：tui-research 流程审计（28 次失败中 8 次为 permission fail-closed）、data-agent-cli 与 marshal-harness hardening 实测

## 背景

当前 opencode Adapter 对"会话中任何一次被拒绝的工具调用"判整个 Attempt 失败。作为 fail-closed 默认这是正确的起点，但真实长跑任务暴露了张力：

- 被拒调用中有相当部分是**良性误触**：读 `$TMPDIR/opencode/work-context.txt`（Provider 自身引导）、读 control/ 下非 input 文件、以绝对路径读 worktree 内部文件（realpath 仍在 worktree 内）；
- 另一部分是**真正的越权信号**：写 allowPaths 之外、执行 `sh -c`/`curl`/`gh`/`git push` 等被禁命令、符号链接逃逸尝试；
- 零容忍把两类等同处理：8 次可预防失败烧掉数小时墙钟，且迫使 Lead 用 TaskSpec 文字纪律补救——纪律脆弱、Lead 依赖、不可复用。

安全意图（写边界与执行边界不可破）与成功率（长跑任务不因读误触而死）需要结构化分离。

## 决策

### 二分分类，确定性判定

拒绝事件在 Adapter 内按**确定性规则**分为两级，不使用模型判断、不学习：

- **BENIGN（预期内）**：只读探针命中以下目标——`$TMPDIR/<provider>/work-context.txt` 等 Provider 自引导产物；control/ 下非 `control/input` 路径；绝对路径但 realpath 落在当前 worktree 内的读操作。处置：记录结构化 denial event（路径、工具、时间、分级理由），**Attempt 继续**；
- **FATAL（致命）**：allowPaths 之外的写尝试；被禁命令模式（`sh -c`/`bash -c`/`xargs`/`curl`/`wget`/`gh`/`git push|commit|tag`/`sudo` 等，沿用现有 bash deny 表）；符号链接逃逸尝试；任何分类器不认识的拒绝。处置：**立即 fail-closed**，现状不变。

默认规则：未被显式列入 BENIGN 的拒绝一律 FATAL——fail-closed 是兜底，不是可选项。

### TaskSpec 只能收窄，不能放宽

TaskSpec 可声明额外 `expectedDenialPatterns`，但仅允许从内置 BENIGN 候选清单中挑选（读类模式）；**任何把写/执行类拒绝降为 BENIGN 的声明在 Schema 层拒绝**。这维持"TaskSpec 不得放宽安全 deny 规则"的既有原则。

### 拒绝日志是证据

含 BENIGN 拒绝的 Attempt：VerificationReport 标注 denial 计数与清单；ReviewPacket 携带 denial log；Lead 可将重复良性拒绝作为 rework 或换 Adapter 的信号。Outcome 记录分级统计，供经济性度量使用。

### 一致性测试

共享 Conformance Suite 增加分级用例：每个 Adapter 对同一组良性/致命拒绝序列产生相同分级结果；BENIGN 序列不终止 Attempt、FATAL 序列终止；未知拒绝默认 FATAL。

## 后果

- 长跑读密集任务成功率提升，写/执行边界零放宽；
- denial log 成为新的证据维度与 Adapter 质量度量；
- 成本：每个 Adapter 维护分类表与一致性测试；分类表变更需经审计（属安全语义变更）。

## 备选方案

- 维持零容忍 + TaskSpec 纪律（现状）：脆弱、Lead 依赖，已被三轮实测证伪；
- TaskSpec 自由重分级：违反安全 deny 不可放宽原则，否决；
- 用模型判断分级：不可审计、不可复现，否决。
