# ADR 0099：Go 历史线退役与一次性移除

- 状态：Accepted（2026-09-10 维护者接受并指示执行；开发阶段快速迭代授权，接受记录锚定本提交）
- 日期：2026-09-10
- 关联：ADR0080（三面业务路线）、ADR0085（Task-first 合同与存储）、ADR0094（可信单用户）、ADR0097（Node 运行时准入）；历史线接受记录见 ADR0001–0079

## 背景

2026-09 的产品主线已切换为 Node Agent Team（`packages/task-*` + `task-api/openapi.json` 合同 + `apps/task-web` 同源 UI），并已发布 v1.0.0/v1.0.1/v1.0.2 签名发行（证据见各 release dossier）。历史 Go 实现（`cmd/`、`internal/` 77 包、`schemas/`、旧 `web/` Run 控制台、`go.mod`/`Makefile`）自 ADR0080/0094 起「不被隐式改写」，处于冻结状态：Node 侧对 Go 资产零引用（2026-09-10 独立盘点核实：`packages/` 全部 `.mjs` 无 `cmd/internal/schemas/web` 的任一文件系统引用；`task-distribution` 发行清单为显式白名单且不含 Go 资产）。

继续保留 Go 树的实际成本已经高于其证据价值：`ci.yml` 每个 push 仍对 cmd+internal 合计 1070 个 Go 文件跑完整 race 测试（双 OS），`CODEOWNERS` 为已死目录保留信任边界条款，`Makefile`/架构检查脚本持续误导新人贡献方向，且 `docs/` 与 README 需要多处并存两套口径。

## 决策

1. **Go 历史线整体退役并从 main 移除**：删除 `cmd/`、`internal/`、`schemas/`、`web/`、`sdk/`、`go.mod`、`go.sum`、`Makefile`。历史字节不删除——它们完整保留在 git 历史与已打 tag（含 `v1.0.0-rc1`）中，历史发行证据链（release dossier、ADR、audit-report）引用的文件路径通过对应 sourceHead/tag 继续可复算、可复验。本 ADR 不撤回或改写任何历史 ADR 的接受事实，只宣告其实现载体离开 main。
2. **同步移除只服务于 Go 线的自动化**：删除 workflow `ci.yml`、`release.yml`、`rc1-canary.yml`、`m10-bridge-smoke.yml`、`m13-e2e-dogfood.yml`、`fixed-server-t1-canary.yml`、`team-progress-regression.yml` 与 `.github/actions/live-review/`；删除 scripts 中 Go 专用条目（`install.sh`、`architecture_check*`、全部 `rc1-*`/`release-*`/`fixed-server-*`/`m13-*`/`candidate-ci-gate*`、`linux-candidate-conformance.sh`、`macos-authority-preflight.sh`、`marshal.sh`、`marshal-watch*`、`dist-profile_test.sh`、`e2e-m13-dogfood.py`）。保留 Node 线脚本（`install-node.sh`、`node-candidate-*`、`sync-node-oss*`、`prepare-node-mirror*`、`marshal-client-*`/`marshal-command-install` 及产品客户端测试）与 Node 线 workflow（`node-*.yml`、`marshal-client-skill.yml`、`pages.yml`）。
3. **`ci.yml` 中的 secret scan（gitleaks）不属于 Go**：提取为独立 workflow `secret-scan.yml`，门禁能力不降级。
4. **`CODEOWNERS`**：移除指向已删目录的条款；`/docs/adr/` 信任边界条款保留，并新增 `/packages/task-api/`（OpenAPI 合同目录）为信任边界——这是对合同唯一权威目录的显式收紧，替代被删目录让出的条款。`.gitleaks.toml` 的 `*_test.go` allowlist 保留（gitleaks 对完整 git 历史扫描，历史 fixture 仍会被命中）。
5. **治理文档同步**：`AGENTS.md` 删除/改写所有「旧 Go/hardened profile 按原合同继续执行」类并存条款，统一为「Go 线已退役并移除，历史证据在 git 历史」；`CONTRIBUTING.md` 移除 Go 开发环境节；README 保留「v1.0.0-rc1 为历史 Go prerelease」的最小事实注记并指向 git 历史。
6. **不影响面的显式列举**：不变更任何 Node 合同（OpenAPI、SQLite 布局、profile id、发行 manifest 白名单）；不变更 Node 发布权限与签名身份（ADR0096 与 v1.0.2 身份摘要原样）；不改变 `docs/adr/**` 既有内容与编号；不删除 Node 时代脚本与文档。

## 已知代价与边界

- 分支保护若将 `ci.yml` 的 job 列为 required check，需仓库管理员在 GitHub 设置中同步移除该 required 项——这是仓库外操作，本 ADR 无法代为完成；在执行合并前已明确告知执行者。
- 历史 Go 文档（`docs/usage.md`、`docs/how-it-works.md` 等）保留为历史资料并在 mkdocs 导航中归入「历史资料（Go 时代）」组；其中引用的 CLI 命令随代码移除不再可执行，以本 ADR 为适用边界说明。
- 此后如需追溯 Go 行为，以 tag `v1.0.0-rc1`（`e99326f`，精确指向 `c1407bd`）或删除前 main 末端 sourceHead 为入口，不在 main 树内恢复 Go 文件。

## 接受与验收

- 执行面验收：删除后 `node --test --test-concurrency=1 packages/*/*.test.mjs` 与 apps/task-web 全量（typecheck/build/test+e2e）在同 sourceHead 复跑通过；仓库内 grep 无对已删路径的活引用（历史文档的记述性引用除外）；`secret-scan` 新 workflow 语法校验通过。
- 本 ADR 只退役历史实现与自动化，不触碰任何 Node 合同，因此不需要新的 Schema/合同测试。
