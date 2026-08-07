# Marshal 开源社区治理反向审计报告

- 审计任务：OSS-COMMUNITY（run: `oss-audit-community-20260807`）
- 审计日期：2026-08-07
- 审计视角：开源社区治理与社区友好性
- 对照基准：GitHub Community Profile 清单（LICENSE、README、CONTRIBUTING、CODE_OF_CONDUCT、SECURITY、issue/PR 模板、CODEOWNERS）与优秀 Go OSS 项目通行实践（标签化发布、SemVer、CHANGELOG、fork 友好的 CI、可参与的治理）
- 方法约束：只读分析本 worktree；不修改任何既有文件；不使用网络；远端仓库设置与 GitHub 页面无法核实之处一律记入“未核实项”

## 摘要

Marshal 的内部工程成熟度很高：文档体系完整（README.md、30 余份 docs/ 文档、14 份 ADR），CI 可复现且权限最小化（.github/workflows/ci.yml），供应链纪律良好（Action 按 SHA 固定、go.sum 锁定、docs/compatibility-matrix.md 对 Adapter 做版本锁定与 Probe 验收）。但以“接受外部社区参与”的标准衡量，仓库仍处于**私人孵化状态**：

1. **完全没有 LICENSE（P0）**：全仓库无任何许可证文本或授权声明。法律上默认“保留所有权利”，外部用户无权使用、修改或再分发，贡献归属也未定义，社区化与企业采用均不成立。
2. **社区档案整体缺失（P1）**：CONTRIBUTING、CODE_OF_CONDUCT、SECURITY.md、issue/PR 模板、CODEOWNERS 全部不存在。现有的 AGENTS.md 与 docs/development.md 是面向 Marshal 自身 Agent 工作流的实施门禁文档，不是面向外部人类贡献者的指南。
3. **无发布语义（P1）**：无 git tag、无 CHANGELOG、无 SemVer 策略，构建版本恒为 `dev`（Makefile:3、internal/buildinfo/buildinfo.go:8），用户无法锁定任何稳定版本。
4. **CI 对 fork 总体友好（P2）**：仅依赖 `contents: read` 权限与内置 `GITHUB_TOKEN`，无自定义 secrets，外部 PR 可跑；但 Gitleaks 评论在 fork PR 下的行为未核实，且缺少 Dependabot 等依赖更新机制。
5. **治理可理解性（P2）**：决策权与门禁定义清晰但完全以维护者为中心；外部参与者没有提出 ADR、参与审查或晋升维护者的路径；文档统一中文的政策（AGENTS.md:32）对非中文贡献者构成门槛且未对外声明。

结论：在决定面向社区开放之前，必须至少关闭 P0（LICENSE）与 P1（社区档案 + 发布策略）；否则仓库在 GitHub Community Profile 与企业用户评估中将呈现“可见但不可用、可用但不敢用”的状态。

## 发现（按优先级）

### 社区档案清单对照表

| 清单条目 | 现状 | 对应发现 |
| --- | --- | --- |
| README | 存在，但缺安装/快速开始与许可声明 | F-1、F-6 |
| LICENSE | 缺失（全仓库零许可文本） | F-1 |
| CONTRIBUTING | 缺失 | F-2 |
| CODE_OF_CONDUCT | 缺失 | F-2 |
| SECURITY.md | 缺失（docs/security-model.md 是产品安全模型，非上报渠道） | F-2 |
| issue 模板 | 缺失（`.github/` 仅含 workflows/） | F-2 |
| PR 模板 | 缺失 | F-2 |
| CODEOWNERS | 缺失 | F-2 |
| 版本标签/Release | 缺失（本地 `.git/refs/tags` 为空） | F-3 |
| CHANGELOG | 缺失 | F-3 |
| SemVer/兼容性承诺 | 仅 Adapter 侧有（docs/compatibility-matrix.md），项目自身无 | F-3 |
| Fork 友好 CI | 基本满足（无自定义 secrets、最小权限、SHA 固定），个别行为未核实 | F-4 |
| 依赖更新机制 | 缺失（无 Dependabot/Renovate） | F-4 |
| 外部治理路径 | 缺失（ADR 提议、维护者晋升、审查分派均无对外流程） | F-5 |
| 贡献者语言政策声明 | 缺失（中文文档政策仅见于内部文档 AGENTS.md:32） | F-5 |

### P0

#### F-1 LICENSE 缺失：对外无任何授权

- 证据：全仓库检索 `license|License|LICENSE|许可|版权|Copyright` 零命中；仓库根无 LICENSE/LICENSE.txt/LICENSE.md（根目录仅 .gitignore、AGENTS.md、go.mod、go.sum、Makefile、README.md）；README.md 的 8 个章节（README.md:1、7、20、30、41、54、82、100）无任何许可声明；go.mod:1 声明 module `github.com/chiga0/marshal-harness` 但未随附许可。
- 法律影响：
  - 代码发布在 GitHub 上仅依 GitHub 服务条款授予平台内的查看/Fork 权利，**不构成开源许可**。按版权法默认“保留所有权利”，第三方使用、修改、再分发、商用集成均构成侵权；
  - 外部 PR 在无 CLA/DCO 的情况下提交，其代码授权链不清晰，维护者合并与再授权都存在法律不确定性；
  - 无明示专利授权。Marshal 编排 Coding Agent、驱动 forge API 与工作流自动化，属于专利密集领域，缺少专利条款的风险高于普通 CLI 工具。
- 结论：这是社区化的第一阻塞项，任何对外宣传“开源”之前必须关闭。

### P1

#### F-2 GitHub 社区档案文件几乎整体缺失

- 证据：`.github/` 目录下只有 `workflows/ci.yml`；全仓库检索确认 CONTRIBUTING、CODE_OF_CONDUCT、SECURITY.md、CODEOWNERS、`.github/ISSUE_TEMPLATE/`、PR 模板均不存在。
- 逐项影响：
  - **CONTRIBUTING**：docs/development.md:41-56 只记录本地 make 命令与质量门禁，docs/development.md:58-83 只列 CLI 与示例；全仓库没有面向外部人员的贡献流程（如何提 issue、分支与提交规范、PR 审查预期与时限、授权方式）。AGENTS.md:39-45 的“实施门禁”要求先按 docs/implementation-plan.md 顺序实施并经维护者接受 ADR，这是内部 Agent 纪律，不是外部贡献入口；
  - **CODE_OF_CONDUCT**：无行为准则与执行联系人，缺少社区运营承诺的基本信号，也是 CNCF 等基金会的硬性要求；
  - **SECURITY.md**：docs/security-model.md:1-109 是产品自身的安全模型（Execution Profile、威胁与缓解、Supply Chain），不是漏洞上报指南——没有上报渠道、联系方式、响应时限与安全披露政策。Marshal 本身编排代码执行并接触发布凭据，docs/security-model.md:3 自述“天然具有高影响”，缺少私有漏洞上报渠道尤其不合理；
  - **issue/PR 模板**：外部 issue/PR 信息无结构约束，难以满足本仓库高证据标准的审查要求（docs/verification-and-review.md、docs/artifact-and-publishing.md 对证据与 PR Body 有严格要求，但外部人无从得知）；
  - **CODEOWNERS**：无 `.github/CODEOWNERS`，审查路由完全依赖人工分派，外部 PR 没有默认责任人，也没有目录级所有权声明。

#### F-3 release/CHANGELOG/版本语义/标签策略缺失

- 证据：
  - 本地 `.git/refs/tags/` 为空目录，仓库历史中无任何版本标签；
  - 无 CHANGELOG.md 或等价发布记录；
  - Makefile:3 `VERSION ?= dev`、Makefile:4-5 `COMMIT`/`BUILD_DATE` 默认 unknown，internal/buildinfo/buildinfo.go:7-11 哨兵值同为 `dev`/`unknown`，`marshal version`（docs/development.md:61）在真实构建中只能输出 `dev`；
  - docs/artifact-and-publishing.md:38 仅把 “Release/Package ID” 列为可选 Publication Artifact 字段，docs/implementation-plan.md 中无任何 release/tag/CHANGELOG 规划，项目自身的发布流程不存在。
- 影响：用户只能从 main 构建，无法锁定版本；Go 生态 `go install` 只能解析 pseudo-version；无法用 SemVer 声明破坏性变更；schemas/ 契约（Draft 2020-12）的演进没有对外版本锚点。
- 对比：项目对 Worker Adapter 有严格版本纪律（docs/compatibility-matrix.md 的版本锁定表与 Probe 验收），对自身却没有等价纪律，形成反差。

### P2

#### F-4 CI 对 fork/外部 PR 总体友好，但存在未核实与待优化点

- 正面证据（应保持）：
  - ci.yml:3-7 在 `pull_request`（含 fork PR）与 push main 上触发；
  - ci.yml:9-10 全局 `permissions: contents: read`，符合最小权限实践；
  - ci.yml:57-60 仅依赖内置 `GITHUB_TOKEN`，全工作流无自定义 secrets，fork PR 不会因凭据缺失而不可运行；
  - 外部 Action 全部按完整 SHA 固定并注释版本号（如 `actions/checkout@3d3c42e5... # v7.0.1`、`gitleaks-action@e0c47f4f... # v3.0.0`），供应链可复现；
  - ci.yml:32 `go-version-file: go.mod` 加缓存，Go 版本由仓库自身决定，fork 环境可复现；
  - ci.yml:12-14 对同 ref 的 in-progress 任务取消，节约配额；
  - ci.yml:23-24 ubuntu/macOS 双矩阵，与 MVP 支持范围（docs/vision-and-scope.md:51）一致。
- 问题：
  - Gitleaks 步骤（ci.yml:57-60）通常借助 `GITHUB_TOKEN` 在 PR 上发表评论；fork PR 场景下该 token 为只读，评论行为是否失败、失败是否导致外部 PR 出现不可修复的红色检查，本次未核实（见未核实项）；
  - CI 不上传构建产物（Makefile:29-30 的 `bin/marshal` 只留在 runner 本地），审查者与用户无法直接下载 PR 对应二进制试用；
  - 无 Dependabot/Renovate 配置（`.github/` 下无 dependabot.yml）；docs/security-model.md:109 的 Supply Chain 一节只承诺锁定，未说明依赖更新节奏与外部上报渠道；
  - GitHub 对首次贡献者的 fork PR 需要维护者手动批准才运行 CI，这一预期未写入任何文档，外部贡献者可能误以为 CI 损坏。

#### F-5 治理文档以维护者为中心，外部参与者无路径

- 证据：
  - docs/adr/README.md:3 记录历次 ADR 均由“维护者接受”或随实施接受；没有外部人员提议、评议、表决 ADR 的流程说明；ADR 0013/0014（docs/adr/README.md:19-20）处于 Proposed 状态，但无公开征求意见渠道与关闭时限；
  - AGENTS.md:5 与 AGENTS.md:39-45 将实施门禁定义为“维护者已明确接受设计”；docs/vision-and-scope.md:87-97 的决策权表把语义决策全部归于“主 Agent / 维护者”，未给外部角色留接口；
  - docs/roadmap-status.md:3-13 的里程碑记录清晰，但是内部验收台账，没有可参与入口（无 good first issue / help wanted 约定或标签策略）；
  - AGENTS.md:32 要求“所有面向人的 Markdown 文档统一使用中文”。这与现状一致，是合理决策，但对非中文贡献者构成真实门槛，且仓库没有对外声明该语言政策、没有英文入门摘要。
- 影响：外部贡献者能读懂设计，但无法回答“谁拍板、如何提议、审查由谁负责、如何成为维护者”。叠加 F-2 的 CODEOWNERS 缺失，治理闭环对外没有接口。

#### F-6 README 缺少面向用户的安装/快速上手入口

- 证据：README.md 的章节（README.md:1、7、20、30、41、54、82、100）全部是设计说明、生命周期与文档导航，无“安装/快速开始/使用示例”；最接近可用的入口是面向开发者的 `go run ./cmd/marshal doctor` 示例（docs/development.md:82-83），且 README 未链接到它。
- 影响：从搜索、推荐或依赖图到达仓库的用户无法在数分钟内完成第一次运行；缺少 CI 状态徽章等健康度信号。这是 Go OSS README 的标准配置项。

### 正面发现（应保持）

- CI 权限最小化、Action SHA 固定、无自定义 secrets（.github/workflows/ci.yml），fork 友好度显著高于多数早期项目；
- 证据与门禁文化贯穿文档（docs/verification-and-review.md、docs/artifact-and-publishing.md），若转化为贡献指南会是非常有辨识度的社区资产；
- docs/compatibility-matrix.md 的“只记录已验证事实、未验证一律不承诺”原则，适合直接延伸为发布与兼容性承诺文本；
- docs/security-model.md 对安全保证边界的诚实表述（不做沙箱过度承诺）是优秀社区信任基础。

## 修复建议清单

按优先级排序；每项均可独立验收。

### P0（阻塞项，开放前必须完成）

1. **新增 LICENSE，建议 Apache-2.0**
   - 动作：仓库根添加 Apache-2.0 标准全文 `LICENSE`；README.md 增加 License 章节引用该文件；
   - 理由：Apache-2.0 自带明示专利授权与“贡献即授权”条款，与 Go/CNCF 生态及多数企业法务政策兼容；MIT 备选但无专利授权；GPL 系会降低企业采用。结合 Marshal 的工作流编排定位，专利条款是净收益；
   - 配套决策：初期依赖 Apache-2.0 的 inbound=outbound，不引入独立 CLA，并在 CONTRIBUTING 中写明，避免法律维护成本；
   - 验收：GitHub Community Profile 显示 License；`marshal version` 输出所在二进制的分发附带许可声明。

### P1（对外推广前应完成）

2. **新增 CONTRIBUTING.md**：环境准备（引用 docs/development.md:7-56 的 Go 基线与 make 命令）、issue/PR 流程、提交信息规范、审查预期与时限、inbound=outbound 授权声明、语言政策说明；
3. **新增 CODE_OF_CONDUCT.md**：采用 Contributor Covenant v2.1 全文，附执行联系方式；
4. **新增 SECURITY.md**：私有漏洞上报渠道（GitHub Private Vulnerability Disclosure 与/或专用邮箱）、响应时限承诺（如 7 天内确认、90 天内给出修复版本计划）、披露政策；明确引用 docs/security-model.md 并区分“产品安全模型”与“漏洞上报渠道”；
5. **新增 issue/PR 模板**：`.github/ISSUE_TEMPLATE/` 提供 bug 报告、功能请求、Adapter 兼容性问题三类模板；PR 模板可复用 docs/artifact-and-publishing.md 中 PR/MR Body 骨架（目标/变更/验证/风险与回滚/来源信息），使外部 PR 天然贴近本仓库证据要求；
6. **新增 `.github/CODEOWNERS`**：声明全局默认 owner，并按 `cmd/`、`internal/`、`schemas/`、`docs/`、`.github/` 分组；
7. **建立发布策略**
   - 新增发布流程文档（如 docs/release-process.md）：采用 SemVer 2.0.0，初始系列 `v0.x.y`，标签格式 `vX.Y.Z`，并声明 schemas/ 契约的稳定边界；
   - 新增 CHANGELOG.md（Keep a Changelog 格式），从首个发布开始记录；
   - 增加 tag 触发的发布流程（CI 或脚本），复用 Makefile:3-10 现有 `VERSION/COMMIT/BUILD_DATE` LDFLAGS 注入，使 `marshal version`（internal/buildinfo/buildinfo.go:8）输出真实版本；
   - 打出第一个 tag（如 v0.1.0）作为社区起点。

### P2（社区体验优化）

8. **验证并硬化 CI fork 场景**：确认 gitleaks-action 在 fork PR（只读 token）下的行为；若评论失败会导致红色检查，则降级为告警或跳过，避免外部 PR 被不可修复的检查卡住；在 CONTRIBUTING 中说明首次贡献者 CI 需要维护者批准；
9. **启用 Dependabot**（`.github/dependabot.yml`，覆盖 gomod 与 github-actions 生态），或在 SECURITY.md/CONTRIBUTING 中写明依赖更新节奏与外部报告渠道；
10. **README 增强**：增加安装/快速开始（`go install` 或发布二进制下载）、最小使用示例、CI 状态徽章；在 README 顶部提供英文简介段并声明“设计文档以中文为主”，降低国际参与者门槛；
11. **治理补全**：在 docs/adr/README.md 补充“如何提议新 ADR”（issue/PR 草案流程与状态机）；在 CONTRIBUTING 或独立 GOVERNANCE.md 中说明维护者名单、审查分派与晋升机制；为 Proposed 状态 ADR（docs/adr/README.md:19-20）提供公开评议渠道；
12. **CI 产物上传**：在 ci.yml 的 quality job 中将 `bin/marshal` 作为 Actions artifact 上传，方便审查者抽检。

### P3（可选增强）

13. 引入 issue 标签体系（`good first issue`、`help wanted`、`adapter/*`），为外部贡献者提供低门槛入口；
14. 如接受赞助，添加 FUNDING.yml；
15. 中长期提供 README、CONTRIBUTING、SECURITY 的双语版本。

## 未核实项

1. **远端仓库设置**：分支保护规则、GitHub Community 页面实际展示、Private Vulnerability Reporting 是否启用，均无法从本地 worktree 确认；
2. **gitleaks-action 在 fork PR 下的真实行为**：本次审计未使用网络，F-4 的风险描述基于 fork PR `GITHUB_TOKEN` 只读这一平台规则推断，未验证该 Action 的具体失败模式；
3. **`go install github.com/chiga0/marshal-harness@latest` 在无 tag 时的 pseudo-version 行为**：未实测；
4. **远端是否存在本地未拉取的 tag/release**：本审计只检查了本地 `.git/refs/tags`（为空）；若远端已有标签而本地未 fetch，F-3 需要修正；
5. **依赖许可兼容性**：go.mod/go.sum 中各依赖自带独立许可证，本次未做依赖树许可扫描；
6. **远端仓库页面（github.com/chiga0/marshal-harness）的 Insights/社区健康状态**：需要网络访问，未核实。
