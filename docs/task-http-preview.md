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

## 验证与尚未完成项

- 旧基线 `749ed23acf4f0a2b0b79f6801a5c421db1592bb2` 的 ECS 动态：`goal/planning` 的 `TestTask(Submission|Template)` 两包通过；`resultingress/productionruntime` 完整测试通过，包耗时 57.231s / 0.885s、作业 88.702s、exit 0。这不是新 HTTP 实现的证据。
- 新源码快照 SHA-256 `b41c1ac4e171d53ac4af6bd702db9c5e7b4dac035d99be19a6f33524bc577bb7`，Linux amd64 三包完整测试通过：resultingress 56.412s、taskhttp 0.015s、productionruntime 0.827s，作业 81.680s、exit 0。使用非 root、1 CPU/768 MiB/NoNewPrivileges；非 race，不含 Darwin 专属测试，不替代最终提交验证。
- 同快照的 `-race -run '^Test(Task|TeamPlanConcurrent|InspectionLease)'` 三包通过：1.653s / 1.065s / 1.089s，作业 92.357s、exit 0；不是全包 race 或 Darwin 实机证明。
- 新测试分层覆盖：同物理 RB1 的草稿/精确批准/冷重放；真实 held RepositorySession 经 HTTP 的创建/确认/重启查询；loopback/token/Host/Origin/大小/冲突/未支持能力负例。transport fixture 不冒充实际 Agent 或业务验收。
- 唯一独立审查发现两项 P1：真实 `RunContext` gate 尚未允许新启动参数，及正数但不匹配的 revision 返回码与冲突合同不符。经用户明确授权后一次聚合修正：准入与启动共用无 I/O 的封闭参数解析，保留原 activation；同一 router 的 writer lane 拒忙且无泄漏；正数陈旧 revision 返回 `409`。新增真实入口准入和共享 lane 回归。held-session 测试直接调用 Handler，listener 测试使用显式 control fixture，lane 测试使用原 router 与显式应用 fixture；三者不能合称真实 CLI→listener→队列→Agent 的整链验收。
- 本机只做静态检查，精确最终提交动态/race、Darwin fixed 启动、真实团队独立验收和下载消费仍需后续记录。无本轮模型调用、正式发布或远端合并声明。导航中的 ADR 状态冲突另行核对，本页不变更治理状态。
