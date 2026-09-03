# ADR 0075：RC1 dogfood 三道确定性屏障的收敛修复（worktree 私隐模式、launch 文本闸门、终态结果提取）

| 字段 | 值 |
| --- | --- |
| 状态 | 已接受（Accepted） |
| 日期 | 2026-09-02 |
| 接受依据 | 维护者依据 [Issue #224](https://github.com/chiga0/marshal-harness/issues/224)、[Issue #225](https://github.com/chiga0/marshal-harness/issues/225) 的双环境取证链接受；接受只冻结本 ADR 三项收敛，不改变 ADR 0068/0073 任何既有边界 |
| 决定者 | 维护者 |
| 关联 Issue | [#224](https://github.com/chiga0/marshal-harness/issues/224)、[#225](https://github.com/chiga0/marshal-harness/issues/225) |
| 关联 ADR | [ADR 0068](0068-mac-first-cli-only-lifecycle-preview-rc1.md)、[ADR 0069](0069-attempt-reservation-and-existing-worktree-allocation.md)、[ADR 0071](0071-darwin-sealed-completion-and-durable-result-capability.md)、[ADR 0072](0072-result-observation-binding-before-worktree-release.md)、[ADR 0073](0073-dogfood-activation-v2-host-portability.md) |

## 背景

`v1.0.0-rc1` 发布后，维护者用同一已发布二进制（无重建）执行首个真实相对复杂任务（M13 GoalLite walking skeleton，中文任务文本），在 `darwin-local-dogfood` sealed CLI 链上连续暴露四道确定性失败（双仓、双二进制、双宿主复现并有逐层取证）：

1. **worktree 私隐模式屏障**（PrepareRunStart，RB1 path-B bind admission）：`verifyPrivateDirectory`（`internal/allocationcontrol/store_darwin.go`）要求目标任务 worktree 根与 `.git/worktrees/<name>` admin dir 恰好 `0700` 且属 euid；`CreateForRun`（`internal/gitworktree/manager.go`）只做 `git worktree add`、不设置权限。operator umask 022 → 0755 → bind 在任何事实落盘前 fail closed，坍缩为 `application: authority-conflict`。既有 canary 通过的唯一原因是 `scripts/release-canary.sh` 首行 `umask 077`。
2. **launch 文本闸门过宽**（PrepareRunStart 的 reseal 段）：`containsAbsolutePOSIXPathToken`（`internal/adapter/pi/pi.go`）把"空白或 CJK 字符之后的 `/`"一律判为"绝对 POSIX 路径 token"。真实中文任务文本的 ` / `、`行尾/行首`、`非法/重复` 必然命中，使中文客观/约束写法的任务无法启动；canary marker 文本恰好不含此类 token。
3. **终态结果提取过硬**（ sealed `CollectRunResult`，首个 process-terminal collect）：`extractFinalWorkerResult`（`internal/adapter/pi/production_result.go`）要求终态 assistant 文本恰好是一个裸 WorkerResult JSON；只要消息体在 JSON 之前带任何散文（例如模型自报的合规说明）就以 "not one JSON object" 拒绝，并在 collect 映射层坍缩为 `application: authority-conflict`。该失败遮蔽了 harvest 全部成功的事实：worker 已按 allowPaths 真实交付、supervisor mechanics 完整、transcript 已 commit。
4. **wedge 放大器**（既已冻结，不在本 ADR 变更范围）：屏障 1–3 的失败把 attempt 留在 partial chain，同一 RunID 的 replay 门禁确定性返回 `recovery-required`，且当前没有合法的 attempt 处置入口（`task abort` 在 dogfood gate 外，`CancelAttemptReservation` 仅测试可达）。本 ADR 只阻断新 wedge 的产生，不重新定义 recovery/dispose 语义。

四者共同效果：ADR 0068 宣称的"普通用户 local dogfood 可用"对除了 canary marker 之外的任何真实任务 **不成立**。三道修复互相独立、同为确定性、都不削弱既有权威或 fail-closed 语义，收敛到一个 ADR 一并冻结。

## 决定

### 1. worktree 私隐模式在创建时即保证（不放宽 admission）

- `CreateForRun` 在 `git worktree add` 成功后、任何 admission 发生前，对 **worktree 根目录**与 **`.git/worktrees/<name>` admin 目录**显式 `chmod 0700`（属主=euid 的检查保持由 `verifyPrivateDirectory` 原样执行）。失败路径照旧 `worktree remove --force` 清理。
- 语义不放宽：`verifyPrivateDirectory` 的 `mode == 0700 && uid == euid` 判定全文保留；RB1 绑定凭证内的目录形态字段不变。admission 端不做任何"兼容 0755"的退化。
- 本决定不改变 ADR 0069 的 RB1 union immutable per-Attempt 绑定语义，只是把创建端补齐到 admission 已冻结的不变量，使"任意 operator umask 下 `task plan/run` 都工作"成为真实合同。

### 2. launch 文本闸门按注释意图收窄为"空白切词、token 前缀判定"

- `containsAbsolutePOSIXPathToken` 改为：按 Unicode 空白切词，剥掉 ASCII 前导定界符（`"`、`'`、`(`、`[`、`{`、`<`）后，token 以 `/` 起始即为 hit（含被引号/括号包裹的绝对路径）；否则一律放行。
- 明确合法：`行尾/行首`、`非法/重复`、`src/file.go`、以及任何 CJK 紧邻的斜杠对（token 首字符为 CJK，不在前导定界符集合内）。
- 明确仍拒：任何在 token 前缀位置写出的宿主绝对路径（包括命令示例里的 `/usr/bin/python3`）——"任务文本不得混入宿主绝对路径"的安全意图保留不动：绝对路径只能由代码/引擎注入，绝不能从 prompt 文本进入 argv。
- 其余 launch 输入闸门（`containsReservedControlPath`、ID/控制符检查、长度上限）一字不改。

### 3. 终态结果提取：容忍散文，要求"恰好一个完整 JSON 对象"

- `extractFinalWorkerResult` 的终态 assistant 文本要求从"裸 JSON"放宽为：**文本中恰好存在一个完整 JSON 对象且其后到 EOF 只有空白**；提取语义为：对每个 `{` 起点尝试边界解码，收集所有"完整对象 + 尾部全空白"的候选，候选数必须恰为 1，否则 fail closed；该对象照常进入 `NormaliseDeclaredWorkerResult`、schema 校验、身份绑定，再由 Marshal 覆盖 adapter/session/timing/token 权威字段（ADR 0072 的 immutable observation binding 不变）。
- 候选必须恰为 1：0 个 → 拒绝；2 个或以上（例如散文里夹了另一颗 JSON 对象）→ 拒绝。散文本体内的局部花括号（或被引号包裹的 JSON 片段）不构成候选——因为边界解码只在 top-level 完整解析成功且尾部只含空白时成立。
- 不接受"多 text item"、"非 assistant 终态"、"willRetry=true 的终态"：现有据（唯一 text item、agent role、agent_end 停机）原样保留。

### 4. 明确不属于本 ADR 的内容

- 不改变 `mapAuthorityError` 把 attempt-domain 错误坍缩为 `application: authority-conflict` 的现状（Outcome 证据仍必须承载细节；本 ADR 只移除三条必然触发的确定性路径）。
- 不引入 attempt dispose/cancel/rework CLI；recovery-required wedge 语义照旧（ADR 0069 §cancellation 未动）。
- 不改 sealed chain、ResultIngress 事实模型、provider/sandbox 权威边界、publication=none、unsigned/non-production 合同。

## 负向矩阵（必须随实现添加的回归测试）

1. F1：umask 077 *与* umask 022 下创建 worktree，两处目录保持 `0700`；admin gitdir 同样 `0700`；CreateForRun 在 chmod 失败时正确清理（worktree remove）。
2. F2：接受 CJK/CJK-slash 对与相对路径；拒绝 token-prefix 绝对路径（含引号/括号包裹形式）、字符混杂 `a/b`（token 前缀为字母，合法）。扫过既有 guard 用语料（marshal 控制路径、绝对路径注入）全部仍拒。
3. F3：散文+单 JSON → 接受；纯 JSON（canary 形状）→ 接受；无 JSON、两个 JSON、截断 JSON、尾随非空白 → 全拒；`willRetry=true`、`taskId/runId/attemptId/adapter` 漂变 → 全拒。
4. E2E 种子：`m13-e2e-dogfood` workflow 用中文 objective 原样重放到 sealed run（屏障 2 解除的关键性外部证据）。

## 后果

- 中文/CJK 真实任务首次可以在 default umask 下走完 ADR 0068 的 ordinary-user sealed 生命周期，不再依赖 operator 手动 `umask 077`。
- 任何 `--profile workspace-write` 的真实模型输出（含 models 主动递交合规说明散文后才给 JSON 的常见行为）都能到达独立 Verification。
- Issue #224（F1、F2）与 #225（F3）的 stable-阻断归因随之关闭；本 ADR 不关闭 #212（签名/notarization）与宿主 EDR（kernel SIGKILL `Killed: 9`）——那些仍是 v1.0 stable 的独立硬门禁。

## 实施顺序

1. 合并 F1/F2/F3 + 回归测试到 main（不改变任何权威语义，只是补齐创建端与闸门意图；RC1 已发布资产不回溯）。— the fixes apply to the next candidate build；已发布 `v1.0.0-rc1` 的屏障如实记录。
2. exact-head CI（Ubuntu、macOS、secret scan + architecture gate）全绿。
3. 用 `m13-e2e-dogfood` 在相同 published-asset pin 下重跑首个真实复杂任务；必须到达 `VERIFYING → REVIEW_PENDING`，随后独立 ReviewDecision `accept` 到达 `ACCEPTED`，并提取 token/时间指标。
4. 关闭 Issue #224、#225 并在 `docs/current-status.md`、`docs/roadmap-status.md` 如实记录修复与残留缺口。

## 实施证据载体勘误（2026-09-04）

上述实施顺序第 3 项的“相同 published-asset pin”与第 1 项“修复只进入 next candidate build，已发布 `v1.0.0-rc1` 不回溯”矛盾，勘误为：Issue #224/#225 的关闭证据必须使用 `candidate-mode=build-from-head`，同时冻结精确 `sourceHead` 与 candidate SHA-256；`published-rc1` 只可重放历史失败，不得声称已发布 `v1.0.0-rc1` 包含本 ADR 修复。

本勘误只纠正 evidence carrier 的归属，不改变 worktree admission、launch 文本闸门、WorkerResult 提取、ResultIngress、ReviewDecision、publication 或任何 trust boundary。
