# Marshal Skill 规则迁移映射

> **何时必须读取：** 修改 `SKILL.md`、任一 workflow reference、路由表、布局门禁或试图删除/合并规则时必须完整读取。日常 Run 不需要读取。

本表证明顶层默认读取面的缩小没有删除规则。机器契约仍由现有 Schema、template、validator 和 Core 实现定义；reference 只保存 operator workflow。

| 原规则簇 | 新权威位置 |
| --- | --- |
| frontmatter、显式/隐式触发、Core authority、不可绕过边界 | `SKILL.md` |
| plan→approve→run→verify→review、terminal/PUBLISHING/CI_PENDING 分支、最短命令 | `SKILL.md`；发布细节在 `publication-and-reconcile.md` |
| Decision 身份摘要、verdict、Required Gate、Blocking Finding | `SKILL.md`；完整审查在 `review-and-rework.md` |
| TaskSpec scaffold、自包含 context、统一 `--phase plan` 入口、acceptance purity/semantic preflight、TaskSpec/Policy/Capability/执行面/证据面闭合交叉矩阵、正反 fixture、零匹配 selector | `admission-and-acceptance.md` |
| admission receipt、plan approval/digest、scope/worktree/pre-mortem、结构性 finding 分类 | `admission-and-acceptance.md` |
| REVIEW_PENDING 一 heartbeat、rework 归因、closure matrix、negative receipt、freshness 原子 claim | `review-and-rework.md` |
| executable/config、Mac 实证复用、ordinary-user 诊断与 strict/exact Qoder transcript attestation 分流、Adapter 晋升阶梯、failure signature/预算 | `adapter-promotion-and-mac.md` |
| WorkerResult transport、Qoder final tee、envelope、路径/目录/读取约束、Codex/Qwen/Pi/OpenCode 特例 | `adapter-promotion-and-mac.md` |
| event/watchdog、有限动作、交互权、容量/背压、dedupeKey、supervise 风险、fan-out/进程所有权 | `watchdog-and-capacity.md` |
| 多 Worker 交付监督、完整纵切/WIP、Attempt/rework/machine escape 记账、重复 failure、review 队列、production dependency graph/exit criterion 对齐、continue/freeze/replan/intervene | `delivery-supervision.md` |
| 单一 operator verdict、规则替换预算、defect-owner routing、Review freshness 的字段级 identity/digest/lineage/原子 claim 细节 | workflow/routing 在 `SKILL.md`、`delivery-supervision.md` 与 `review-and-rework.md`；当前 review freshness operator verdict 位于 Python preflight，固定 internal command 只提供 contract/canonical/Candidate/observation primitive，字段合同由相邻 Schema、实现及其测试拥有 |
| publish approval、CI checks、accept/reconcile、merge 与远端同步事实 | `publication-and-reconcile.md` |
| 用户授权的 Harness 本地闭环、纵切交付、按风险去重的工程测试、公开 CLI JSON shape 兼容、Web、cleanup、SemVer/release、覆盖率、milestone 声明 | `engineering-and-release.md` |

## 维护纪律

- 新规则先判定触发域；只进入一个主要 reference，跨域处用链接或短摘要，避免重复维护。
- 顶层只增加“每次动作都必须知道”的边界、状态分支或路由；细节必须进入按需 reference。
- reference 按下一项具体动作 Just-in-time 读取；只读审计和维护者本地机械小修不得预读未来 lifecycle/review/publication 阶段。
- 删除规则前必须在本表标出替代的机器契约或新位置，并让 layout test 覆盖相应路由/锚点。
- 改变语义时按 `AGENTS.md` 判断 ADR；仅重排文字且语义不变不新增 ADR。
