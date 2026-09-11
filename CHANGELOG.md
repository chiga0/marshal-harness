# Changelog

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循 [SemVer](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### 移除（2026-09-11，ADR 0099）
- Go 历史线整体退役并从 main 移除：`cmd/`、`internal/`、`schemas/`、`web/`、`sdk/`、`go.mod`/`go.sum`/`Makefile`、7 个 Go 专属 workflow（ci/release/rc1-canary/m10-bridge-smoke/m13-e2e-dogfood/fixed-server-t1-canary/team-progress-regression）、`.github/actions/live-review/` 与 Go 专用 scripts；历史字节与 `v1.0.0-rc1` 经 git 历史与 tag 完整追溯，见 [ADR 0099](docs/adr/0099-go-legacy-line-retirement-and-removal.md)。
- 同步变化：`ci.yml` 内 secret scan（gitleaks）提取为独立 workflow `secret-scan.yml`；`CODEOWNERS` 信任边界收敛为 `docs/adr/` 与 `packages/task-api/`；`README`/`CONTRIBUTING`/`AGENTS.md`/mkdocs 导航按 ADR 0099 更新。

### 修复（Node 主线，2026-09-10）
- UI 工作台（apps/task-web）按 2026-09-10 交接审计完成 11 项必修修复（UI-01–UI-11，提交 5b21e11c/a837902f/2574b0cf/e4053f92）：创建任务 5xx 不再误判为拒绝（结果未知 + 保留幂等键显式同键重放）；逻辑动作输入冻结与异步代际隔离（轮询推进 revision 不解锁/换键/覆盖未决动作）；默认 fetch 闭包包装修复严格 receiver 浏览器「网络层不可达」误报；评审/独立验收/交付/后验分开呈现（新增 `GET /v1/tasks/{taskId}/audit` 消费，`acceptance=passed` 才是验收通过）；Worker 列表无限分页与已加载范围标注；请求默认 deadline 与 AbortSignal 贯通；服务端 401 即时连接失效并清缓存；运行中 Worker 问题展示 deliveryStatus（受理≠投递≠消费）且已答未 ACK 保留待核对；Leader 请求到期即时禁用（确认框打开后到期不发请求）；统一模态层原语（一次 Escape 只关最上层、Tab 圈禁、焦点不被轮询重置）；
- CI：node-team workflow 纳入 `apps/task-web` 路径触发，新增 `ui` job（typecheck/build/test+e2e）；pack 前先构建 UI dist 并断言候选与安装树含 `apps/task-web/dist/index.html`，干净 checkout 不再静默产出无 UI 候选。

## [v1.0.2] - 2026-09-10（Node 主线）

Node Agent Team 正式发行（macOS arm64 / Linux x64）：task-service 本地服务 + v7 受管 Leader 可运行检查点 + `task-local` 的 `marshal init/serve/status` 统一命令 + minisign 验签的一键安装（OSS 镜像 / HTTPS 镜像 / 离线三模式）。证据与身份摘要见 `docs/v1.0.2-release-dossier-2026-09-10.md`，使用入口见 `docs/node-install.md`。旧 v1.0.0/v1.0.1 为同一主线的前序候选；历史 Go RC1（`v1.0.0-rc1`，unsigned darwin-arm64 CLI-only）是另一条已冻结的历史线，与本条目无关。

## [Unreleased v1.0-candidate] - 2026-08-27（历史 Go 线）

注意：本节不是 v1.0 发布声明。v1.0 范围与生产可达性门禁已由 [ADR 0052](docs/adr/0052-v1-release-scope-and-production-reachability.md)（2026-08-27 接受）冻结为单节点生产纵切；当前主表状态为 `I186-R0: PASSED`、`I186-R1: IN_PROGRESS`、`I186-R2–R6: PLANNED`，v1.0 未发布。下列为 I186 快速收敛线路（单 Lead + 多 Sub-Agent 高并发）已合入 main 的交付资产（component checkpoint），同时增收 0.1.0 之后已合入交付。

### 新增
- Worker executor strangler cutover：`MARSHAL_WORKER_EXECUTOR` 默认走 sandboxbridge 执行链（Provision→Stage→WorkerAdapter.Run→Inspect→Terminate，allocation/lease 身份绑定、frozen 工单 content-addressed 入账）；`=legacy` 显式回到 `Adapter.Run(host)` compatibility profile；rollback 无状态迁移（ADR 0043/0045/0054；等价证据：legacy 与桥路径 journal 事件序列逐条相同、WorkerResult 业务内容逐字节相同）；
- 执行前 allocation 身份落盘 + `SweepOrphans` 孤儿 allocation 幂等终结（bridged 路径 SIGKILL 崩溃窗口对账）；
- R3 收敛域合同与门禁：per-Attempt 双 Provider binding recheck（attemptgate）、分级撤销处置（revokedrain：security-critical 零 drain / ordinary bounded drain）、执行位置 claim/fact 分型（locationattest）、失败分类 authority（failureclass）、ADR 0049；
- R4 单一恢复模型（recovery：故障矩阵八类唯一幂等结论 + explain 渲染模型）与 Pre-R4 四项合同（hotpath/jitgate/protocolrev/candidateid）、ADR 0053；
- R5 cutover 判定（cutovereq 三分判据 + cutovercheck golden trace）与 effect sink pre-mutation fencing（effectsink）、ADR 0054；
- R6 SLO/性能基线（perfbench：五条热路径 p99 ≤5000µs 冻结阈值与实测基线）、确定性 accelerated soak（soak 10k 迭代 + 5 轮路径级 bridged Run invariant）；
- herdr TerminalSession 后端 POC（实验分支，ADR 0009/0011 补充）；
- TaskSpec `worker.tools` 声明式工具 allowlist（封闭枚举 read/edit/write/grep/find/ls/bash，可选，uniqueItems；缺省保持既有 profile 行为，全部既有 TaskSpec 向后兼容）；
- 三个 Worker Adapter 对 `worker.tools` 的机械强制：pi `--tools` 精确交集（声明 bash 或 read-only 声明 write 启动前 fail closed）、opencode 最小 permission 配置 + `debug config` 回读校验、qwen `--exclude-tools` 反向收敛；声明读取/格式非法一律启动前 fail closed；
- 三个 Adapter（含 Fake Adapter）把成功（非拒绝）工具调用的工具名规范化（ADR 0013 冻结工具分类表）后写入 `<adapter>-transcript-meta.json` 的 `toolNames`；
- Verification 新增 `tool-allowlist` required gate（denial-summary 之后、command gates 之前）：声明后任一成功调用越权判 required fail 并附越权清单证据，证据缺失/不可读/格式非法 fail closed，未声明的 Run gate skipped；
- 跨 Adapter 对账一致性 Conformance 套件（同一合规/越权事件序列在 opencode/pi/qwen 采集+对账路径下裁决逐位一致）。

### 修复
- recovery 决策表两处副作用歧义缺口：具名分支绕过 reconcile 横切规则、duplicate delivery 幂等 resume 缺副作用歧义例外（soak iteration 69/148 驱动）；
- issue #37：TaskSpec 声明的工具约束此前仅由 prompt 禁令表达；现形成 TaskSpec 声明 → Adapter 在 Provider 调用层机械强制 → transcript 采集 → Verification gate 判失败的闭环，未声明工具在 Provider 调用层被拒绝或在对账层成为 required Gate failure；
- 移除 pi/qwen Adapter 在 allowlist 重构后遗留的未使用终端 argv 构造函数（staticcheck U1000）：终端形态构造已统一并入 `buildTerminalArgsWithTools`，`--tools`/`--exclude-tools` 的 allowlist 收敛语义在 captured 与终端两条路径上均不变；
- Issue #209（Qoder 随机临时 executable）与 Issue #211 闭环；Pi 0.84.3 + session-v3 compaction 协议闭合（audit-report，2026-08-26 节）。

### 数据层规划（未承诺版本）
- “重试同 taskId”、交互式 DAG、PTY token 统计（规划中）。

### 边界与已知缺口（honest scope）
- 本机不存在 production assurance 运行（ADR 0042 ordinary-user 语义；Darwin self-identity 外部 provision 未完成，`MARSHAL-DARWIN-SELF-IDENTITY` OPEN）；
- Push/Pull 未接生产 worker 路径（conformance 为测试套件）；双 binding/ResultIngress→runstore 证据桥接线、`marshal explain run` CLI wiring、wall-clock 24h soak 归 v1.0 后首批运维工作（I186-R6 收口第四节）；
- opencode 宿主版本 1.18.20 超出 adapter 版本表属 dogfood 环境漂移（另行沉淀）。

## [0.1.0] - 2026-08-10

首个正式版本：Local MVP `USABLE`（Milestone 0–6 全通过）。

### 新增
- `marshal web` / `marshal serve`：只读 Web 控制台（opt-in，三级视角+hash 路由+实时 SSE+检索+亮色）；
- `marshal task migrate-outcomes`：遗留终态 Run 补记 Outcome（不覆盖已有）；
- `marshal task abort`：废弃 Run 的显式生命周期出口，写终态 Outcome（ADR 0012）；
- `marshal task run --through-verify`：worker 成功后同调用内自动独立 verify；
- WorkerResult 归一化：三 Adapter 在校验前删除无效可选 `session` 字段；
- Worker prompt 内嵌 WorkerResult 逐字模板；
- worktree 创建仓库级短锁退避重试（5×800ms）；
- GitHub Pages 文档站（mkdocs-material，中文 + mermaid）；
- 开源基建：MIT LICENSE、CONTRIBUTING、CODE_OF_CONDUCT、SECURITY、issue/PR 模板；
- 三 Worker Adapter：OpenCode 1.18.13 / Qwen Code 0.21.5 / Pi 0.83.0；GitHub Draft Publisher；受监督 cmux TerminalSession。

### 修复
- publish Approval 与发布重校验读取 legacy `review-decision.json` 导致恒失败（轮次绑定修复）；
- ADR 0013/0014 接受并实施（拒绝分级、read-only 画像）。

## [0.1.0] - 2026-08-08

Local MVP：Milestone 0–6 全通过。证据门禁式生命周期（plan→run→verify→review→publish→accept）、三 Worker Adapter（OpenCode 1.18.13 / Qwen Code 0.21.5 / Pi 0.83.0）、GitHub Draft Publisher、受监督 cmux TerminalSession、崩溃恢复与发布幂等。
