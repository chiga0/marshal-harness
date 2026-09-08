# Task HTTP：当前候选入口

更新：2026-09-08。本候选按 [ADR 0085](adr/0085-agent-team-service-contract-and-storage.md) 的 Task-first 方向，把创建/查询/确认接入原 fixed server、RepositorySession 和同一 RB1。不修改 ADR 状态，不代表 API-STABLE 或 B1 团队交付完成。

## 当前范围

| HTTP | 当前语义 |
| --- | --- |
| `GET /v1/capabilities` | 返回 `task-draft/v1` 支持项和未实现项 |
| `POST /v1/tasks` | `Idempotency-Key` 加 `template/intent/context.text`，冻结不可变预览；不批准、不创建 Run |
| `GET /v1/tasks?after=ID&limit=10` | 同一数据根内分页，limit 为 1–20；cursor 是排他的 Task ID |
| `GET /v1/tasks/{id}` | 当前批准、节点、失败及已存在 Outcome 的投影 |
| `GET /v1/tasks/{id}/graph`、`/workers` | 原投影中的节点/依赖及 Run 状态；`busy` 表示暂时拿不到 Run lease，不声称进程健康 |
| `POST /v1/tasks/{id}/approve` | `Idempotency-Key` 加原 `expectedRevision/previewDigest`，批准原三个 Team 义务；既有 resident 调度推进 |

模板仅有 `order-quote/v1`：两个作者分别实现报价 API/客户端，随后集成。自由文本只补充固定契约，预览展示实际 work、scope、oracle 摘要和总限额；这不是任意需求的自动规划器。`context.text` 不解释为宿主路径或权限。模板是 operator 启动配置，HTTP 不接收 Policy、执行程序、环境或 authority 对象。

Task `cancel`、自动独立 Decision、完整成果下载尚未接线，capabilities 明列 pending，不提供空成功接口。`review-pending` 仍需原独立 Decision；已有 completed TeamOutcome 也仅显示 `verified-awaiting-delivery`，不声称成果可下载。B1 的全程自主交付与取消退出条件保持开放。

## 启动与访问

复用现有合法 fixed 安装、Pi 配置及可信仓库，不运行随机临时 Go 二进制，不创建第二个 owner：

```sh
marshal control-plane serve --task-http-address 127.0.0.1:0 --task-template /absolute/operator/team.json
```

`team.json` 来自现有 `scripts/fixed-server-team-inputs.py` 的完整 operator 输入。它是模板，不是批准：旧 requestId/deadline 不授予新 Task 权限，Core 为新 Task 绑定 ID 并冻结 30 分钟确认期。该 flag 启用原 resident Collect/Verify，但不增加自动 Decision。模型额度不足时不启动真实业务，先运行确定性测试。

ready 只输出 URL、profile、连接文件路径；随机 token 仅写入当前 owner control 目录内新建的 0600 文件，不输出日志、不交给 Worker。普通客户端读取该文件，在 HTTP `Authorization: Bearer …` 中发送 token；不要将 token 放 URL、命令参数或日志。仅允许显式 `127.0.0.1`，认证及 Host/Origin 检查先于应用调用。连接文件被替换或 owner 漂移即拒绝。这不构成对同 UID 恶意程序的隔离。

创建正文：

```json
{"template":"order-quote/v1","intent":"实现订单报价 API 和真实 HTTP 客户端","context":{"text":"按预览中的固定业务契约交付"}}
```

返回 `201` 的 `id/revision/previewDigest/confirmBefore` 只表示草稿耐久存在。确认预览后向该 ID 的 `/approve` 发送原 revision/digest；`202` 只表示义务已批准。批准正文：

```json
{"expectedRevision":1,"previewDigest":"sha256:使用创建响应中的原摘要"}
```

丢回复时保留同一 key 和正文重发；原成功重放不重新环境探测、不延期期限、不重置预算、不重复生成义务。同 key 不同正文/确认摘要返回 `409`，首次确认过期返回 `410`，缺失 Task 返回 `404`，owner/写入槽/查询暂不可用返回 `503`。不自动创建替代任务。重启后 token 可变，但 Task 身份和幂等范围来自同一 RB1，不使用 PID/token 作为 namespace；旧损坏或未恢复数据根不能通过此入口旁路接管。

## 最新验证与下一步

精确 source `8543878cc9cc9b095e94c06c0cc41987611149f2` 的[完整 CI 34184969456](https://github.com/chiga0/marshal-harness/actions/runs/34184969456)五项全部通过，包含 Darwin/Ubuntu quality；PR #271 已合入功能分支 `feat/team-resident-progress`（`be03e7015be12636b7f2af5aca644039b3558492`），不是 main。下列早期记录中的待测/运行中描述仅为历史，不覆盖此结果。

下载客户端后继 `b086d1a0d6331ce0cbacf7471d5d8ad5389ec490` 已通过独立复跑的 30 项无模型测试与复审。`download --task-id ID --output-dir NEW_DIR [--run-oracle]` 只接受已完成 Task 的有界 ZIP、固定文件及匹配摘要；明确指定 `--run-oracle` 才执行本地固定 oracle。审查发现的后代进程残留已修复为持有会话 leader、先清理所属进程组再回收，并有两类真实 fork 回归。此时服务端自动 Decision/完整下载仍在开发，不能据客户端 fixture 宣称 HTTP 团队交付已通；下一项组合验证是消费 Go 实产 ZIP，然后验证完整 Task 主链。Task cancel 与真实双 Worker 验收仍开放。

## 历史验证记录

- 旧基线 `749ed23acf4f0a2b0b79f6801a5c421db1592bb2` 的 ECS 动态：`goal/planning` 的 `TestTask(Submission|Template)` 两包通过；`resultingress/productionruntime` 完整测试通过，包耗时 57.231s / 0.885s、作业 88.702s、exit 0。这不是新 HTTP 实现的证据。
- 新源码快照 SHA-256 `b41c1ac4e171d53ac4af6bd702db9c5e7b4dac035d99be19a6f33524bc577bb7`，Linux amd64 三包完整测试通过：resultingress 56.412s、taskhttp 0.015s、productionruntime 0.827s，作业 81.680s、exit 0。使用非 root、1 CPU/768 MiB/NoNewPrivileges；非 race，不含 Darwin 专属测试，不替代最终提交验证。
- 同快照的 `-race -run '^Test(Task|TeamPlanConcurrent|InspectionLease)'` 三包通过：1.653s / 1.065s / 1.089s，作业 92.357s、exit 0；不是全包 race 或 Darwin 实机证明。
- 新测试分层覆盖：同物理 RB1 的草稿/精确批准/冷重放；真实 held RepositorySession 经 HTTP 的创建/确认/重启查询；loopback/token/Host/Origin/大小/冲突/未支持能力负例。transport fixture 不冒充实际 Agent 或业务验收。
- 唯一独立审查发现两项 P1：真实 `RunContext` gate 尚未允许新启动参数，及正数但不匹配的 revision 返回码与冲突合同不符。经用户明确授权后一次聚合修正：准入与启动共用无 I/O 的封闭参数解析，保留原 activation；同一 router 的 writer lane 拒忙且无泄漏；正数陈旧 revision 返回 `409`。新增真实入口准入和共享 lane 回归。held-session 测试直接调用 Handler，listener 测试使用显式 control fixture，lane 测试使用原 router 与显式应用 fixture；三者不能合称真实 CLI→listener→队列→Agent 的整链验收。
- 精确实现提交 `70ec14894f94b1ad0f34c8a42fdbf47c714d1496` 经原 reviewer 复核，原两项 P1 在代码层关闭，无新增 P0/P1。香港 ECS 对该提交的 `resultingress/taskhttp/productionruntime` 三包完整测试 PASS（55.965s / 0.014s / 0.748s，作业 79.761s，exit 0）；此项非 race。
- 同提交 Mac 定向动态通过 `TestRepositoryTaskHTTPHeldOwnerColdReplay`（0.64s）、`TestDarwinLocalDogfoodProductionEntry`（含真实 Task HTTP activation 入口，6.81s）及 `TestTeamProgressEntryExactAllowlist`。测试二进制使用固定缓存路径；需要身份观察的包必须按 Makefile 注入精确 sourceHead，且从包目录执行，不能把裸 `go test -c` 的 unknown metadata 或错误工作目录造成的失败算作产品缺陷。
- `TestTaskHTTPUsesExistingResidentWriterLane` 的本机测试进程在任何测试输出前 exit 137；系统日志记录对应固定测试程序的 AMFI `Unrecoverable CT signature issue`。保留未通过状态，不改签名或安全策略绕过；该测试仍须经宿主合法放行或独立 Darwin CI 执行。分层测试不等于真实 CLI→TCP→Agent 全链。
- 独立 HTTP 客户端 `f333dfda80d3530a162a86908e12ba1edf86c834` 已通过主 Agent 独立复跑的 17 项 loopback 测试、secret scan 与 diff-check；本地候选合入为 `736fcc9a949506963907bbd9b042fefb442e654e`，仅新增两份 Python 文件，未合入 main。用法见 `python3 scripts/task-http-demo-client.py --help`；默认不批准，原预览写入私有文件，明确确认原摘要后才提交批准。未提供的自动 Decision、cancel、下载仍报告 pending。
- 无本轮模型调用、正式发布或远端 main 合并声明。精确组合候选完整 CI、Darwin 共享 lane 动态和真实团队独立验收/下载消费仍开放；导航中的 ADR 状态冲突另行核对，本页不变更治理状态。
- PR #271 的 CI `34184140645` 在两个系统的 architecture-check 拒绝新增 `productionruntime → planning` 依赖；未运行到后续 vet/race，不能用先前定向测试代替完整 CI。修复把纯 `TaskTemplatePort` 放在 Application，具体模板构建留在原 CLI composition，runtime 只消费规范输入和原草稿预览，原 RB1/批准权威不变；未增加架构 allowlist。冷重放使用冻结输入，即使停用新提交模板仍可查询原 Task，oracle 漂移拒绝而非用当前常量补写。新增 Port/失败零写/停用后冷查询回归，并把 17 项无模型客户端测试接入已有 `fixed-server-t1-check`。修复工作区的 architecture-check（含 21 项自测）、17 项客户端测试与定向 vet 已通过；最终提交动态测试、release-ci-contract 和完整 CI 仍需分别核验，不在此提前宣称通过。此次纠偏将架构检查纳入本地定向验证起点，避免 reviewer/远端 CI 再成为可机械发现依赖错误的首个发现者。
