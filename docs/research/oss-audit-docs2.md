# 开源反向审计报告：docs（OSS-DOCS-2）

- 审计日期：2026-08-07
- 审计视角：文档工程师（文档 vs 代码漂移、结构、可部署性）
- 审计基线：`marshal/OSS-DOCS-2` worktree，HEAD `da644e7`（main 同 SHA）
- 交付物：本报告；未修改任何其他文件

审计方法与覆盖：

- 逐条比对 docs/ 全部 31 份文档与 `internal/`、`cmd/` 实现（CLI 命令/flag、生命周期 reducer、domain 常量、contract catalog、adapter 版本锁、环境变量读取点）；
- 逐条比对 `schemas/` 17 份 Schema 与 `docs/task-contract.md`、`docs/verification-and-review.md`、`schemas/README.md` 的字段与枚举描述；
- 脚本化检查全仓 Markdown 相对链接（docs/**、README/CONTRIBUTING/SECURITY/CODE_OF_CONDUCT/AGENTS、schemas/README.md）；
- 静态审查 mkdocs.yml、.github/workflows/pages.yml 与 ci.yml 的部署链路；
- 未执行网络请求，未修改任何已有文件。

## 摘要

docs/ 与代码在核心契约层（生命周期状态、Verdict、Verification 状态、Schema 字段、Adapter 版本锁、环境变量主集）总体一致；17 份 Schema 与 Go contract catalog 完全对齐。但存在三类系统性漂移：

1. **abort 生命周期（ADR 0012）落地后未回写文档**：`docs/task-lifecycle.md` 仍描述“任意非终态 → ABORTED”的未实现转换，且缺少实际实现的 `RETRY_PENDING → BLOCKED`（run.aborted）转换行；`docs/development.md`、`docs/operator-runbook.md`、`docs/failure-and-recovery.md` 对 abort 的描述分别为缺失、“开发中”、错误终态。
2. **阶段状态（Milestone 6 已 PASSED、ADR 0013/0014 已接受）未同步**：README、AGENTS.md、implementation-plan、development、audit-report 五处仍停留在“进入 Milestone 6 / ADR 0001–0011 / ADR 0013-0014 提案状态”。
3. **GitHub Pages 部署不可达**：`mkdocs.yml` nav 引用不存在的 `docs/README.md`（高置信构建失败），nav 漏收 8 个已存在页面，中文搜索与依赖固定存在缺口。

共 22 条发现：P1 × 6、P2 × 9、P3 × 7。修复建议清单按优先级给出，全部可在不改代码（仅改文档/配置）的前提下关闭，其中 F-05、F-10 涉及 mkdocs 配置与 CI，F-01 涉及 ADR 与生命周期文档的语义对齐。

## 发现

### P1（必须修复：错误描述或未实现语义）

- **F-01 生命周期文档与 abort 实现不一致（ADR 0012 未回写）**
  - `docs/task-lifecycle.md:65` 定义转换“任意非终态 → `ABORTED`，授权中止，子进程停止且证据已刷新”。代码中不存在任何到达 `ABORTED` 的转换：`internal/lifecycle/reducer.go:26-39` 的 allowed 表没有指向 `StateAborted` 的边；`internal/cli/cli.go:523-530` 限定 abort 仅可从 `RETRY_PENDING` 发起，`cli.go:537-543` 事件目标态为 `BLOCKED`（事件类型 `run.aborted`，`internal/lifecycle/reducer.go:16-24`）。
  - ADR 0012 本身与代码一致（`docs/adr/0012-explicit-abort.md:23-24` v1 仅 RETRY_PENDING；`:36-38` 明确目标态是 BLOCKED 而非 ABORTED）。因此是生命周期文档滞后，而非代码违规。
  - 同时 `docs/task-lifecycle.md:41-65` 转换表缺少 ADR 0012 新增的 `RETRY_PENDING → BLOCKED（显式 abort）` 行；`:34` 的 `ABORTED` 状态定义在 MVP 代码中不可达，应标注“v1 未启用”。
- **F-02 `docs/development.md` 当前 CLI 清单缺失 abort 与 --through-verify**
  - `docs/development.md:60-75` 的命令清单没有 `marshal task abort --run RUN_ID --actor ID --reason TEXT`，而代码已实现（`internal/cli/cli.go:56` 命令表、`cli.go:490-500` flag 定义）。
  - 同清单 `task run` 未记录 `--through-verify` 标志（`internal/cli/cli.go:870`；该功能随 `76fdf40` 合入，见 `docs/audit-report.md:128`）。
- **F-03 阶段状态漂移：Milestone 6 已 PASSED，多文档仍称“进入 Milestone 6”**
  - `docs/roadmap-status.md:13-15`：M6 `PASSED`、Local MVP `USABLE`；`docs/milestone-6-report.md:5` 结论 `PASSED`。
  - 过时表述：`README.md:5`、`README.md:102`（“Milestone 0–5 已通过，当前进入 Milestone 6”）、`docs/implementation-plan.md:7`、`AGENTS.md:5`（同句）、`docs/development.md:5`（“当前进入 Qwen Code/Pi Adapter、Recovery/Cleanup 与完整 MVP E2E”——这些正是 M6 已完成交付，见 `docs/milestone-6-report.md:7-15`）。
  - 代码侧 `marshal doctor` 报告 `milestone=6`（`internal/cli/cli.go:198`），与 roadmap 一致；文档头部陈述落后一个阶段。
- **F-04 ADR 接受范围不一致：0013/0014 已接受但三处文档未更新**
  - 已接受证据：`docs/adr/0013-graded-permission-denials.md:3`、`docs/adr/0014-read-only-execution-profile.md:3`（“已接受，2026-08-07 维护者接受，进入实施”）、`docs/adr/README.md:19-20`；提交 `da644e7` 信息明确“ADR 0013/0014 维护者接受，进入实施”。
  - 未同步：`README.md:5` 与 `AGENTS.md:5` 仍称“ADR 0001–0011 已接受”；`docs/audit-report.md:128` 称 0013/0014 “保持提案状态待维护者接受”。按 AGENTS.md 文档规则（重大架构问题开关须更新 audit-report），该记录应补关闭说明。
- **F-05 mkdocs nav 引用不存在的 `docs/README.md`（Pages 部署阻断，高置信）**
  - `mkdocs.yml:42`（`docs_dir: docs` 下的 `README.md`）；`docs/README.md` 不存在（`ls docs/` 无此文件，main 分支 git tree 同样没有）。
  - mkdocs 对 nav 指向缺失文件以配置错误中止构建；本机未安装 mkdocs，无法离线复现（见未核实项）。pages.yml 在 push 到 main 时执行 `mkdocs gh-deploy --force`（`.github/workflows/pages.yml:24-25`），预期该工作流自 `da644e7` 起失败。
- **F-06 `docs/operator-runbook.md` 称 abort“开发中”，实际已实现**
  - `docs/operator-runbook.md:106`：“死 Run 先经 `task abort`（开发中）转终态”。abort 已随 `08c8462` 实现并曾丢失后由 `f8d4e74` 恢复（`docs/audit-report.md:126-128`），且 22 个死 Run 已用它处置（`docs/audit-report.md:128`）。

### P2（应修复：漂移、缺漏、误导）

- **F-07 `docs/failure-and-recovery.md` 错误分类表与 SIGINT/abort 实现不符**
  - `docs/failure-and-recovery.md:23`：“Operator Cancellation | SIGINT 或显式 Abort | Graceful Stop 后 `ABORTED`”。实现上 SIGINT/SIGTERM 仅触发 context 取消（`cmd/marshal/main.go:13`），不产生 ABORTED 转换；显式 abort 的终态是 BLOCKED（F-01）。
- **F-08 Schema 文档漏收 Outcome；Schema 计数过时**
  - `schemas/outcome.schema.json` 实际存在且被运行时注册、生成与校验（`internal/contract/catalog.go:26`、`internal/review/outcome.go:26`、`internal/cleanup/service.go:152`），但 `schemas/README.md:5-20` 的 16 项清单未列出它。
  - `docs/development.md:90` 称“15 份 Schema 全部可以编译”；`internal/contract/catalog.go:22-38` 实际为 17 份。
- **F-09 `docs/lead-agent-surfaces.md` 描述不存在的 CLI 标志**
  - `docs/lead-agent-surfaces.md:61` 声称 `marshal task review --export` 与 `--import <path>`；代码中 `task review` 仅有 `--run`、`--decision`、`--json`（`internal/cli/cli.go:1142-1148`）。实际文件桥接（review-packet.json + `--decision` 导入）在 `docs/marshal-skill.md:19-27` 描述正确。
- **F-10 mkdocs nav 漏收 8 类已存在页面**
  - 对照 `docs/` 目录与 `mkdocs.yml:40-90`，nav 缺失：`compatibility-matrix.md`、`milestone-3-scope.md`、`milestone-4-scope.md`、`milestone-5-scope.md`、`milestone-6-scope.md`、`milestone-6-adapter-protocol.md`、`research/deep-research-report.md`、`research/gemini-research.md`；`docs/reviews/` 全部 6 份独立审查报告也未入 nav。构建仅告警不入 nav，但站点信息不完整且与 F-14 的 strict 检查冲突。
- **F-11 README 文档导航漏收 M6 与兼容性文档**
  - `README.md:54-78` 导航列出 Milestone 0–5 报告，但缺 `docs/milestone-6-report.md`、`docs/compatibility-matrix.md`、`docs/marshal-skill.md`、`docs/worker-adapter-spike.md`。
- **F-12 cmux 环境变量文档位置误导**
  - `docs/operator-runbook.md:85-87` 把 `MARSHAL_LIVE_CMUX`/`MARSHAL_CMUX_PATH` 写成操作者可用变量，但二者仅存在于测试 helper（`internal/terminal/cmux_live_test.go:22-25`），不是运行时配置；运行时 cmux 路径经 Go API 参数传入（`internal/terminal/cmux.go:60`）。反向地，代码协议哨兵 `MARSHAL_LAUNCH_READY`（`internal/terminal/cmux.go:245`、`:577`）在 docs 中无任何解释。
- **F-13 中文文档搜索无分词支持**
  - `mkdocs.yml:27-30` 启用 `search.lang: zh`，但 MkDocs/Material 内置 lunr 搜索对中文无分词能力，中文术语检索会大面积失效；需要 jieba 类分词插件（Material 官方建议）。
- **F-14 pages 工作流依赖不固定、缺构建前置检查**
  - `.github/workflows/pages.yml:21-24`：`actions/setup-python@v5` 未按 SHA 固定、`pip install mkdocs-material` 无版本锁，与 `ci.yml` 全 SHA 固定（如 `ci.yml:27`）实践不一致，构建不可复现；且没有独立 `mkdocs build --strict` 前置 job，使 F-05 这类 nav 错误只能在部署阶段暴露。
- **F-15 `docs/architecture.md` 命令组列表过时**
  - `docs/architecture.md:38-51`：“预期命令组”缺 `marshal init`、`marshal task approve`、`marshal task cleanup`（均已实现，见 `docs/operator-runbook.md:19-28`、`internal/cli/cli.go:363`、`cli.go:722`、`cli.go:436`）；列出的 `marshal task rework`（`docs/architecture.md:47`）无实现分支——`internal/cli/cli.go:428-431` 对其输出“尚未实现”并以 exit 3 返回，实际 rework 语义由 `task review` verdict=`rework` 表达（`internal/cli/cli.go:1304-1305`）。

### P3（建议修复：一致性/卫生）

- **F-16 SECURITY.md 本地相对链接失效**：`SECURITY.md:13` 的 `../../security/advisories/new` 在本仓库文件系统不存在（全仓链接检查中唯一的失效相对链接）；该链接依赖 GitHub 仓库级 UI 路径，建议改写为完整 URL 或说明仅 GitHub UI 有效。
- **F-17 SECURITY.md 支持矩阵引用 release tag**：`SECURITY.md:8` 承诺“最近一个 release tag”受支持；仓库是否存在 release tag 未核实（见未核实项），若无 tag 该承诺不可操作。
- **F-18 `.gitignore` 未忽略 mkdocs 产物 `site/`**：本地执行 `mkdocs build/serve` 会留下未跟踪构建噪声（`.gitignore:1-8` 仅含 `.marshal/`、`/bin/` 等）。
- **F-19 中英文间距不一致**：`docs/operator-runbook.md:31`、`:68`、`:80`、`:113` 出现“Marshal不/不会/不得”（中英文间无空格），与全仓其余文档的中英混排空格风格不一致。
- **F-20 `docs/audit-report.md` 自动检查计数停留历史**：`docs/audit-report.md:43-47` 记录“12 份 Schema / 12 份 Happy-path”，为 2026-08-04 审计时点数据；现为 17 份 Schema、17 份 happy-path fixture（`schemas/examples/happy-path/`）。作为活文档被 README 引用，建议重跑或标注“截至审计日期”。
- **F-21 `docs/lead-agent-surfaces.md` 时态过时**：`docs/lead-agent-surfaces.md:38` 称 Skill “在 Milestone 3 交付”为将来时；M3 已通过且 Skill 实存（`.agents/skills/marshal/SKILL.md`、`templates/research-task.json`）。
- **F-22 `docs/marshal-skill.md` 终态集与生命周期不一致**：`docs/marshal-skill.md:33` 列举终态为 ACCEPTED/REJECTED/BLOCKED/NO_CHANGE，缺 `ABORTED`（`docs/task-lifecycle.md:37` 与 `internal/domain/state.go:27`、`:64-70` 的终态集均含 ABORTED）。即使 abort v1 目标态为 BLOCKED，两处终态枚举也应一致。

### 信息架构评估

- **新读者路线（概念→快速开始→参考→设计）部分成立**：README（为什么/不变量/生命周期）→ `docs/vision-and-scope.md` → `docs/architecture.md` 概念链完整；`docs/operator-runbook.md:3` 提供“首次真实使用前先读第 9 节”的实操入口；`docs/development.md` 是唯一 CLI 参考面（但见 F-02）。缺口在“参考→设计”衔接：兼容性事实（compatibility-matrix.md）未从 README 可达（F-11），Milestone 6 证据链对“当前状态是什么”的提问断链（F-03）。
- **内容分层基本合理**：ADR（决策）、milestone report（证据）、runbook（操作）、contract/schema（机器契约）四类职责清晰，未见职责性重复。`docs/marshal-skill.md` 与 `.agents/skills/marshal/SKILL.md` 职责边界（前者给人读、后者给 Lead Agent 读）在 runbook 9.2 有说明。
- **重排建议**：mkdocs 站点宜增设“运行与兼容性”一级组（operator-runbook、compatibility-matrix、environment-baseline、failure-and-recovery），把“快速开始”组聚焦到 development + runbook 第 9 节路线；scope/protocol 类历史文档归入“实施与验收”组并标注状态（见修复建议 6）。

### GitHub Pages 可部署性评估

- **选型评估**：mkdocs-material 对目标（中文 + mermaid）适配——主题 `language: zh`（`mkdocs.yml:9`）、superfences mermaid custom fence（`mkdocs.yml:34-38`）与仓库唯一 mermaid 代码块（`docs/architecture.md:7`）匹配，Material ≥9 内置 mermaid 渲染；中文检索是选型短板（F-13）。选型本身可接受，问题集中在配置与 CI（F-05/F-10/F-14）。
- **部署链路现状**：`pages.yml` 触发条件（docs/**、mkdocs.yml、workflow 自身的 push 到 main）合理（`.github/workflows/pages.yml:4-10`）；deploy 使用 `mkdocs gh-deploy --force`（`:25`）写入 gh-pages 分支，权限 `contents: write`（`:13`）为该方式所需。阻断项：nav 缺失文件（F-05）。
- **站点 URL 与仓库身份**：`site_url: https://chiga0.github.io/marshal-harness/`（`mkdocs.yml:3`）与 TaskSpec `expectedRemoteUrl` 的仓库一致；是否已在 GitHub 启用 Pages 未核实。
- **结论**：**不可部署（blocked by F-05）**；完成修复建议 1、6、8 后可达“就绪”，其中 F-14 的 `--strict` 前置检查可将此类配置漂移挡在合并前。

### 一致性抽查通过项（证据）

文档 vs 代码逐项对照表：

| # | 项目 | 文档位置 | 代码/Schema 位置 | 结论 |
| --- | --- | --- | --- | --- |
| C-01 | 生命周期 16 状态 | task-lifecycle.md:18-35 | internal/domain/state.go:11-28 | 一致 |
| C-02 | Run 终态集（含 ABORTED） | task-lifecycle.md:37 | state.go:64-70 | 一致（ABORTED 可达性见 F-01） |
| C-03 | ReviewDecision verdict 枚举 | task-contract.md:219 | review-decision.schema.json verdict enum | 一致 |
| C-04 | VerificationReport 总状态 | verification-and-review.md:104-107 | verification-report.schema.json:54 | 一致 |
| C-05 | Verification Gate 状态（含 skipped） | verification-and-review.md:100 | verification-report.schema.json:107 | 一致 |
| C-06 | WorkerResult declared* 命名与 status 枚举 | task-contract.md:196-204 | worker-result.schema.json:49-52 | 一致 |
| C-07 | Adapter 版本锁（1.18.13/0.21.5/0.83.0） | compatibility-matrix.md:16-18 | opencode.go:30、qwen.go:33、pi.go:32 | 一致 |
| C-08 | Adapter 配置变量 | operator-runbook.md:137-142 | internal/app/workers.go:41-63 | 一致 |
| C-09 | MARSHAL_STATE_DIR 语义 | architecture.md:66、failure-and-recovery.md:7 | internal/repository/state.go:29 | 一致 |
| C-10 | Publisher gh 环境变量 | operator-runbook.md:140-142 | internal/cli/cli.go:765 | 一致 |
| C-11 | Approval gate（plan/publish） | development.md:67、operator-runbook.md:22,26 | internal/domain/control.go:6-7 | 一致 |
| C-12 | doctor --repair 需 --run | operator-runbook.md:100 | internal/cli/cli.go:164,178-179 | 一致 |
| C-13 | Go 基线 1.26.0/toolchain 1.26.5 | development.md:10-11 | go.mod | 一致 |
| C-14 | CI 门禁描述（双 OS+Secret Scan+SHA 固定） | development.md:56 | .github/workflows/ci.yml:10-58 | 一致 |
| C-15 | Makefile 目标与描述 | development.md:43-54、CONTRIBUTING.md | Makefile:14-37 | 一致 |
| C-16 | cleanup --apply 守卫描述 | development.md:77、operator-runbook.md:100 | internal/cli/cli.go:436-489 | 一致 |
| C-17 | doctor 只读、milestone 字段 | operator-runbook.md:95,145 | internal/cli/cli.go:153,198 | 一致 |
| C-18 | `--pure --format json` 调用形态 | compatibility-matrix.md:40 | internal/adapter/opencode/opencode.go:562 | 一致 |
| C-19 | `.marshal/` Git 忽略 | README.md:87、CONTRIBUTING.md | .gitignore:1-2 | 一致 |
| C-20 | 全仓相对链接 | docs/** 与根级文档 | 文件系统 | 仅 F-16 一处失效 |

补充说明：

- ADR 索引完整性：`docs/adr/README.md:5-20` 列出 0001–0014 全部 14 份且链接均可解析；与 `docs/audit-report.md:134-144` 的建议清单（至 0011）相比，后者未列 0012–0014，属可接受的“建议接受范围”历史口径，但建议随 F-04 一并更新。
- Schema 示例链路：`schemas/examples/happy-path/` 17 份 fixture 与 catalog 17 个 Kind 一一对应；`schemas/README.md:38-40` 对 happy-path/invalid 用途的描述与目录实际一致。
- TaskSpec 示例有效性：`docs/task-contract.md:123-188` 的 YAML 示例字段集（含 publication/budgets）与 `schemas/task-spec.schema.json` 结构兼容；acceptance 命令示例（task-contract.md:57-65）省略了可选 `baselinePolicy`/`maxLogBytes`，后者已在 `docs/verification-and-review.md:79` 单独说明，不构成漂移。

## 修复建议清单

按优先级排序；每条标注对应发现编号。除注明外均为纯文档/配置修改，无需改代码。

1. **[P1, F-05] 修复 mkdocs 首页引用**：将 `mkdocs.yml:42` 改为指向真实文件（选项 a：增加 `docs/index.md` 作为站点首页，内容可与 README 差异化；选项 b：nav 直接引用现有 `docs/` 页面，如 `vision-and-scope.md`，并在站点说明 README 位于仓库根）。这是 Pages 部署的阻断项。
2. **[P1, F-01/F-07] 生命周期文档对齐 ADR 0012**：在 `docs/task-lifecycle.md` 转换表增加 `RETRY_PENDING → BLOCKED`（守卫：显式 abort，Lease 持有，事件 `run.aborted`，terminalReason `aborted-by-operator`）；将 `:65` “任意非终态 → ABORTED” 行改为标注 v1 未启用并指向 ADR 0012 的决策理由；`ABORTED` 状态行（`:34`）加注“v1 不由 CLI 到达”。同步修正 `docs/failure-and-recovery.md:23` 的行为描述（SIGINT=取消、显式 abort 仅 RETRY_PENDING→BLOCKED）。
3. **[P1, F-02/F-15/F-09] 更新 CLI 参考面**：`docs/development.md:60-75` 增加 `marshal task abort` 与 `task run --through-verify`；`docs/architecture.md:38-51` 补 `init`/`task approve`/`task cleanup`，将 `task rework` 改为说明由 review verdict 表达；`docs/lead-agent-surfaces.md:61` 删除 `--export/--import` 或改写为文件契约描述。
4. **[P1, F-03/F-04] 全仓阶段与 ADR 状态同步**：README.md:5/:102、AGENTS.md:5、implementation-plan.md:7、development.md:5 更新为“Milestone 6 已通过（USABLE）”，后续阶段表述引用 roadmap-status.md；README/AGENTS.md 的 ADR 范围改为 0001–0014；按文档规则在 `docs/audit-report.md` 补记 0013/0014 接受事件。
5. **[P1, F-06] 删除 operator-runbook.md:106 的“（开发中）”标注**，并按 ADR 0012 补充 abort 的适用范围（仅 RETRY_PENDING、目标态 BLOCKED、必须带 --actor/--reason）。
6. **[P2, F-10/F-11] 补齐导航**：mkdocs nav 增加“运行与兼容性”组（compatibility-matrix.md）、scope/protocol 文档归入“实施与验收”组、research 组补齐两份研究文档、reviews 组独立；README 导航补 milestone-6-report、compatibility-matrix、marshal-skill。
7. **[P2, F-13] 中文检索**：pages.yml 增加中文分词插件（如 `mkdocs-material-jieba` 或等价方案）并在 mkdocs.yml plugins 启用；若暂不启用，删除误导性 `lang: zh` 声明并注明检索限制。
8. **[P2, F-14] 构建硬化**：pages.yml 将 setup-python 固定到 commit SHA、`pip install mkdocs-material==<锁版本>`；在 ci.yml（或 pages.yml 前置 job）增加 `mkdocs build --strict`，使 nav/链接错误在合并前暴露。
9. **[P2, F-08] Schema 文档对齐**：schemas/README.md 补 outcome.schema.json 条目；development.md:90 计数更新为 17；audit-report.md:43-47 重跑或标注审计日期口径（F-20）。
10. **[P2, F-12] 环境变量定位**：runbook 第 5 节明确 MARSHAL_LIVE_CMUX/MARSHAL_CMUX_PATH 仅测试 helper 使用；在 worker-adapters.md 或 runbook 增补 `MARSHAL_LAUNCH_READY` 协议哨兵的说明。
11. **[P3, F-16~F-19/F-21/F-22] 卫生批量**：SECURITY.md 链接改完整 URL 并核实 release tag 策略（F-16/F-17）；.gitignore 增加 `site/`（F-18）；runbook 四处中英空格（F-19）；lead-agent-surfaces.md 时态（F-21）；marshal-skill.md 终态集补 ABORTED（F-22）。

## 未核实项

- **mkdocs 构建失败的实证**：本机未安装 mkdocs 且任务禁用网络，F-05 的“构建中止”是基于 mkdocs 已知行为的推断，置信度高但未实际执行 `mkdocs build`。
- **GitHub Pages 实际状态**：pages.yml 自 `da644e7` 后的执行结果、Pages 是否已启用、`site_url`（https://chiga0.github.io/marshal-harness/）是否可达，均需远端核验（禁用网络）。
- **release tag 是否存在**：`git tag` 被本任务 permission 策略拒绝，F-17 无法核实。
- **外部链接有效性**：本次仅检查本地相对链接；docs 中全部 https 外链（含 `docs/lead-agent-surfaces.md:109-112`、`docs/worker-adapters.md:92-110`）未验证。
- **Milestone 6 后续工作清单权威出处**：audit-report.md:128 提到 15 个 dirty worktree 待归档机制，属运行时缺口而非文档缺口，未在本报告展开。
