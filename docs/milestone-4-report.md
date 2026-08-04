# Milestone 4 验收报告

- 验收日期：2026-08-04
- 状态：**`PASSED`**
- 范围：首个真实 OpenCode Worker Adapter、Attempt Runner、`marshal task run`
- Publication Side Effect：未启用

## 交付结果

- 对本机 Qwen Code、OpenCode 与 Pi 做有界协议 Spike，按结构化输出、Session、取消和权限接口选择 OpenCode，而不是按主观 Coding Quality 选择；
- 首个 Adapter 固定支持 OpenCode `1.18.12`，要求唯一 absolute realpath，并冻结 executable SHA-256、版本和 CapabilitySnapshot；
- `task run` 验证冻结 TaskSpec、PolicySnapshot、CapabilitySnapshot、仓库、Base、Worktree、Adapter Capability 与预算后才进入 `RUNNING`；
- 每个 Attempt 使用独立 `controlRoot/input|output`，业务 Worktree 不含 Marshal 中间文件；信任边界见 ADR 0006；
- OpenCode 使用直接 argv、环境 allowlist、fail-closed resolved permission 校验、有界 JSONL/stderr、完整进程组取消；Worker 环境不传 Publisher/Cloud/Provider Token 环境变量；
- Adapter 双重验证并规范化 WorkerResult 身份、executable、version、session 与时间；Core 再次绑定 task/run/attempt/adapter；
- Worker 成功后重新验证 Worktree Root/CommonDir，记录真实 Git Snapshot，只进入 `VERIFYING`；独立 `task verify` 仍是唯一权威验证路径；
- Provider/协议失败按预算进入 `RETRY_PENDING` 或 `BLOCKED`，Worker 后置证据失败直接进入 `BLOCKED`，不会在未知脏状态下自动重试；
- Rework Attempt 会把当前 ReviewDecision 的 Blocking Finding 映射进 WorkerRequest 与 Prompt；Session Resume 明确延后到 M6。

## 自动验收

- Unit/Integration/Race：Adapter Probe、版本、digest、argv、权限合并、环境泄密、结构化 permission denial、result identity、无换行输出上限、取消、Run Lifecycle、Operational Retry、后置证据失败与 Worktree 身份破坏全部通过；
- 真实 E2E：`/Users/gawain/.opencode/bin/opencode` `1.18.12` 在临时 Git 仓库创建 `hello.txt`、写出 WorkerResult，Adapter 捕获真实 session/transcript；未 commit、push、发布或修改业务仓库；
- `make ci`：Vet、Staticcheck、全量 Race、Build、Govulncheck 全部通过，无已知漏洞；
- Schema：WorkerRequest 新增 required `controlRoot`，正反 Fixture 与 Draft 2020-12 校验通过；
- 独立 OpenCode 审计首轮发现 1 项 P1 与多项 P2；阻塞项和可验证 P2 整改后复审为 `APPROVE`。详见 [独立审查](reviews/milestone-4-opencode-review.md)。

## 残余风险

- Local `workspace-write` 是合作式护栏，不是同 UID 恶意代码隔离；shell deny 表不能替代容器/VM；
- 进程崩溃后长期 `RUNNING` 的 Reconciliation、Session Resume、其余 Adapter 与 Hardened Profile 属于 M6；
- GitHub 凭据与发布幂等性尚未实现，任何 Worker 路径都不具备 Publisher 能力。

提交 `8aac63d` 的 GitHub Actions run `30879438415` 已在 Linux、macOS 与 Secret Scan 全部通过，M4 正式验收完成。
