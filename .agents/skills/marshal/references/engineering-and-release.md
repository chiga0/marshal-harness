# Harness 工程与发布

> **何时必须读取：** 修改 Marshal Harness、Adapter 或 Skill；选择维护者本地闭环；做 schema/docs/ADR、全仓门禁、cleanup、Web 控制台、版本/tag/release，或声称 milestone/v1.0 完成时，必须完整读取。

## 治理与修改范围

- 默认本仓库需求仍遵循 `plan → approve → run → verify → review → (publish)`，发现流程问题就修 Harness 并回写 Skill。
- 用户明确授权的 Harness 适配/Skill 修复可不走产品 Marshal Run，改用维护者最短本地闭环；这不授权绕过 universal 规则，也不改变产品 Run 生命周期。
- 本地闭环每个独立 slice 只有一个写入者和一个唯一独立 reviewer。作者不得提供权威验证；reviewer 绑定 exact sourceHead/真实 diff，P0/P1 清零后才继续。
- 写任务使用 locked base + 独立 worktree；派发前机械核对路径未被其它作者占用，scope 不重叠。保护用户现有修改、`logs/`、secret/path boundary；不清理或覆盖不属于本 slice 的文件。
- release/product slice 默认覆盖从真实 production entry 到可观察结果的最短完整纵切，并绑定 roadmap exit criterion 或当前 product blocker。禁止只提交未接入 production dependency graph 的空 package、接口壳、fake-only skeleton 或孤立 helper；`AGENTS.md`/已接受 ADR 明确要求的 deterministic Fake 前置、已批准 ADR、研究或 contract-first 前置件，仅在其本身是明确交付物且直接解锁下一纵切时例外。
- 改变 trust boundary、persistence、lifecycle 或 publication authority 必须先新增/替代 ADR，并同步 `docs/audit-report.md`。纯 Skill 信息架构重排且语义不变不需要 ADR。
- 面向人的 Markdown 使用中文；协议字段、状态、命令和代码标识保留英文。术语必须与 Schema/Core 一致。

## 最短本地闭环

作者提交 clean source 后，唯一独立 reviewer 审查 exact diff 和验证证据。只有以下适用于本变更风险的条件全满足，维护者才在 local main 创建 `--no-ff` merge commit：

- reviewer 的真实 P0/P1 为 0，P2 已处理或明确不阻塞且有理由；
- source branch clean，基线仍是权威 local main，`git merge-tree` 无冲突；
- 定向 tests、相关 race、受影响 Go package 的 `go vet`/`staticcheck` 通过；纯文档/Skill 机械变更不伪造不适用的 Go gate；
- Schema/JSON/Draft 2020-12/example gate（若涉及）通过；
- `git diff --check`、Markdown link/layout gate、secret scan 通过；
- merge 后再跑最小高风险回归。

记录 `sourceHead`、`localMergeSha`、命令/结果摘要和 `pendingRemoteSync`。Local main 是后续开发锁定基线；GitHub PR/CI 是异步补充证据。只有实际 push/remote merge 后才能声称它发生；远端 divergence 先停推审计，不改写历史。

## 验证分层与去重

验证绑定 exact `sourceHead` 或集成候选摘要；同一未变化候选已经通过的高成本 gate 不因 reviewer 轮次、heartbeat 或汇报需要而重复。先跑最小相关门禁，尽早得到可行动 failure：

1. 纯 Markdown/Skill 机械变更：layout/link、相关 Python test、`git diff --check`、secret scan；不跑不相关的全仓 Go race/vet/staticcheck。
2. 局部代码变更：受影响 package/unit/fixture、受影响 package 的 vet/staticcheck；只有并发、共享状态、lifecycle 或恢复路径相关时跑对应 `-race`。
3. production wiring、trust/persistence/lifecycle/publication、跨 package 集成或 Schema 变更：先跑定向 gate，再在合入候选的单一集成点跑一次 `make check`；若它已包含全仓 race，不再重复一条等价的 full race。
4. contract/schema/example/diff/link/secret/merge-tree 按实际触发域运行。
5. release candidate 必须在最终候选上通过 `make check` 和 release-specific gates；候选代码或依赖变化使旧证据失效时，才对新摘要重跑。

失败必须记录精确 gate、版本、命令摘要和证据 digest；禁止用“重跑一次”代替根因诊断。宿主高负载时不并发全仓测试，先完成定向门禁，容量恢复后在选定集成点补 full gate。负载敏感 test 可以用合理宽松 timeout 去 flake，不能削弱断言。公开 CLI JSON 变化必须用旧 consumer 仍可解析的 exact-shape 回归证明兼容；既有标量/数组/对象不得换形，只能追加非冲突字段，无法兼容时必须显式版本化。

新命令必须有测试；`make check` 覆盖 format/vet/staticcheck/race/build，release 前必须全绿。覆盖率基线：lifecycle 76%、cleanup 73%、CLI 53%、dashboard 53%；变化时报告真实当前值，不用基线冒充本次结果。

Schema 变化必须验证 JSON 语法、Draft 2020-12 metaschema、示例和 `git diff --check`。不得为了简化 Adapter 或测试而静默放宽 required gate。

## Skill 修改

Skill 顶层受 `references/tests/test_skill_layout.py` 的 UTF-8 12KiB、routing、relative-link 和关键 anchor 门禁约束。修改前先读 `skill-rule-migration.md` 和本次实际受影响的 reference；规则细节进入 Just-in-time reference，顶层只保留每次动作必需的边界、状态机和路由，不预读未来 lifecycle 阶段。Mac preflight 只复用固定 `bin/marshal internal *-check --attestation-ready` 中无 Core/仓库持久副作用的有界 primitive；plan checker 可以执行受限 Adapter version/capability probe。不得回退到新 worktree 或 `/tmp` 下的匿名可执行文件。

跨 Attempt/round 复用或重绑 `sourceHead`、ReviewPacket 或 evidence identity 只能登记为后续 Core 设计评估；除非 Schema/validator/Core 与所需 ADR 同步落地，Skill 不得以效率为由实现或暗示复用。

不要复制 machine contract：Schema/template/validator/Core 是字段与封闭枚举的权威；Skill 保存触发条件、命令、责任边界和 failure handling。新增 reference 首段必须写精确“何时必须读取”，并加入顶层路由和 layout test。

## Web 与遗留清理

- `marshal web`/`marshal serve` 只有显式启动才运行；默认不占内存。Web 是只读视图，控制仍在 CLI/Skill。
- 多 Workspace 可用 `--root <repo>/.marshal` 聚合；DAG UI 使用 React Flow 的缩放、平移、minimap 和节点 attempt 详情。
- 遗留终态 Run 缺 Outcome 时先：

  ```bash
  marshal task migrate-outcomes --actor ID
  ```

  该命令只补缺失 Outcome，不覆盖已有记录。之后才按策略 `cleanup --apply` 或 `cleanup --export-patch`；cleanup 不得销毁必须保留的 Outcome/evidence。

## 版本与 release

- `make check` 和 release-specific gates 全绿后才打 SemVer tag并执行 `gh release create`。
- CHANGELOG 遵循 Keep a Changelog；首个正式版目标为 `v0.1.0`，但 tag/发布事实必须以仓库和远端实际状态为准。
- Web、CLI、Schema、Provider SDK/protocol 和文档的版本/兼容声明必须一致；未完成的 roadmap 项只能标 `PLANNED`/当前文档允许的状态。
- worktree 以 `(taskId, runId)` 键控（`CreateForRun`），同 task 可 retry 而保持单写者；视图按标题折叠 retry。不要把 operational retry 误报成同一 Attempt 或新功能完成。

## 完成声明审计

在声称 milestone 或 v1.0 完成前，逐项从 `docs/implementation-plan.md`、`docs/roadmap-status.md`、相关 ADR/issue 提取退出条件，并为每一项找到当前 main 的实现、test、conformance、真实 Provider/平台证据及远端 gate。局部通过、设计已接受、ordinary-user 可用、单平台 probe 或历史 PR 都不能替代完整退出证据。

普通用户 Qoder/Codex 的 Mac 证据必须称为 ordinary-user；production authority/hardened 需要对应平台机制、真实 credentialed probe 和独立 conformance。发现 identity/version/contract drift 时撤回旧证据并回到 Adapter promotion，不继续 fan-out。
