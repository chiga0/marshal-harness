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

原 `8543878` 入口中的 Task `cancel`、自动独立 Decision、完整成果下载仍为 pending。后继核心组合 `4564dfe` 已接自动客观独立 Decision 与完整成果下载，原 Task/旧 Team 的适用边界仍精确区分；固定路径五组确定性回归通过，完整 CI 与真实 HTTP Worker 验收尚待完成。使用时以实际 server 的 capabilities 与候选身份为准，不把源码接线当本机旧安装已升级。Task cancel 后端仍在独立实施；B1 的真实自主交付与取消退出条件保持开放。

## 启动与访问

复用现有合法 fixed 安装、Pi 配置及可信仓库，不运行随机临时 Go 二进制，不创建第二个 owner：

```sh
marshal control-plane serve --task-http-address 127.0.0.1:0 --task-template /absolute/operator/team.json
```

`team.json` 来自现有 `scripts/fixed-server-team-inputs.py` 的完整 operator 输入。它是模板，不是批准：旧 requestId/deadline 不授予新 Task 权限，Core 为新 Task 绑定 ID 并冻结 30 分钟确认期。该 flag 启用原 resident Collect/Verify；后继 `4564dfe` 的客观独立 Decision 只适用于经原 Task HTTP 批准的固定模板，旧 Team 不自动继承。模型额度不足时不启动真实业务，先运行确定性测试。

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

## 两阶段 HTTP 演示驱动

### 实机重叠观察（与 complete 同时运行）

当前候选新增 `scripts/task-worker-overlap-live.py`，用于在 Worker 仍活跃时自动提取原始记录，避免事后手工补造进程重叠。operator 预建独立 0700 证据目录，显式提供当前私有数据根中的 ledger 与 runs：

```sh
python3 -I -B scripts/task-worker-overlap-live.py \
  --ledger /absolute/state/runtime-v1/result-ingress/result-ingress.jsonl \
  --runs-root /absolute/state/runs --task-id ORIGINAL_TASK_ID \
  --output-dir /absolute/private-evidence --timeout-seconds 60
```

在确认 Task 后立即并行启动观察与 `complete`。工具只读原文件，从原计划派生双 Worker 身份，记录齐全即采样；不会启动、重试、停止或修改 Task。超时为 unavailable，不能在 Worker 退出后补称重叠。snapshot 文件含未脱敏原 prompt/context，保持本机私有，不上传到 PR/公开日志；report/stdout 只含脱敏观察。它证明绑定进程的生命周期重叠，不证明 CPU 同时忙，不代替 Core 接纳或完整业务验收。

### 创建和确认

`scripts/task-http-team-drive.py` 复用上述客户端，只连接已启动服务，不启动 Marshal/Worker、不调用逐 Run CLI、不读取内部 RB1，也不代签 Decision。先准备并人工查看原预览，再明确确认该摘要：

```sh
python3 scripts/task-http-team-drive.py --connection /absolute/connection.json prepare \
  --submission /absolute/submission.json --key demo-create-1 \
  --preview-out /absolute/preview.json --evidence-dir /absolute/new-prepare-evidence
python3 scripts/task-http-team-drive.py --connection /absolute/connection.json complete \
  --preview /absolute/preview.json --confirm-preview-digest sha256:原预览摘要 \
  --key demo-approve-1 --evidence-dir /absolute/new-complete-evidence \
  --output-dir /absolute/new-delivery --timeout-seconds 600
```

输入/连接文件为私有常规文件；原预览含业务正文，须在脱敏证据目录之外保存。`complete` 明确包含在新目录运行固定业务 oracle，仅适用于可信代码。退出码 `0` 为本阶段成功（prepare 只创建草稿；complete 才证明下载消费），`2` 为客户端/协议错误，`3` 为观察窗口结束，`4` 为观察到 blocked/confirmation-expired。超时不代表 Task 失败，不取消 Worker、不刷新服务端预算。连接重启可用新连接文件、原预览和同 key 接续；证据/下载目录必须新建，不覆盖已有成果。输出的 Task ID 与 recovery 提示用于恢复原操作，不能改 key 重建任务掩盖失败。

driver source `6ae76e800319636e491713979c85f3609864c327` 已独立复跑 11 项合成 HTTP 测试并审查，无 P0/P1：覆盖创建→显式确认→运行/待审→completed→下载→固定业务 oracle，以及冲突、观察超时、重连和不覆盖。`processOverlapEvidence=unavailable`：HTTP 状态不是 OS 活进程证明。真实 B1 验收仍须同候选真实 Worker、独立 Decision/最终 Outcome、执行重叠及取消/重启证据。

### 取消客户端接线（服务端能力启用后适用）

`task-http-demo-client.py --connection FILE cancel --task-id ID --expected-revision N --key KEY --watch-seconds 60` 只在 capabilities 声明 `cancel` 后发出 Task 请求，不提供 Run/PID 杀进程回退。`N` 使用当前 Task 查询的控制 revision，不替换原批准预览的 revision。冲突返回错误，不自动刷新 revision；响应丢失只用原 Task、revision 和 key 重放。`202` 与 `cancelling` 仅表示取消已受理，`cancelled` 才表示 Task 取消收口，二者均不是成功交付。观察窗口结束不改变服务端状态。完整交付 driver 若观察到外部取消的 `cancelled`，以退出码 `4` 结束，不再下载成果。

此接线及合成 HTTP 测试不是服务端取消能力或真实 Worker 清理证明；服务端接线和实机验收完成前，B1 取消出口保持开放。

### 2026-09-08 流水线集成状态

- PR #273 的 sourceHead `4d7355da154d387575ad87bb036d045af3771597` 全部检查通过后，已实际远端合并到 `feat/team-resident-progress`，remoteMergeSha `aef06e587e70ddd259644f557e8caff1297b8723`；`pendingRemoteSync=false`。这不是合入 main 或正式发布。
- 取消消费者 sourceHead `ec44741e9c27741fae475ec2ebcf9de3f0f55822` 经独立审查，无 P0/P1；37 项客户端及 12 项完整 driver 合成 HTTP 测试通过。localMergeSha `f612986e55965e76770099378298ee81b4b373f6`，本地组合时 `pendingRemoteSync=true`。响应类型与中断后取消未知状态已处理，不因 Ctrl-C 宣称取消未发生。
- 只读重叠观测 sourceHead `cdc9b54f605f1fabbb038905ddbae4855da2f61d` 经独立审查，两套本机 Python 各 20 项测试、secret scan、diff-check 与 merge-tree 通过；localMergeSha `b758d79e65e71ce0c690b7d0e0d76724252d066d`，本地组合时 `pendingRemoteSync=true`。它使用系统 libproc，不生成临时可执行文件；原事实快照须由 operator 私有提取，输出不授予 Core authority。自身探测不证明真实 Provider 并行，也不证明 CPU 同时工作。
- CI、开发与下一阶段设计准备交错进行：B1 核心真实验证链及取消后端仍在实施，B2 问答只进行可复用接缝的预设计，不插入新的平台前置。当前不能宣称真实 HTTP 团队交付、取消清理或 B1 完成。
- 动态验证不混证据：早期专项测试夹具缺少必需 mediaType 已修；后继 `074f891` 的原 gate 修复通过独立静态复审，但 Mac 生产链测试暴露固定根夹具冲突，尚在修复。固定 `review.test` 执行 exit 137 无测试输出，香港 ECS 随后 SSH 连续连接失败，均未绕过限制或计为通过；已能运行的固定生产测试继续使用原路径。

服务端开发快照 `sha256:f4baf1fe56f2387ae15e5af32cad105bfc207d4cf9d9b21db55926c7269c85f1` 已提前在受限 ECS 运行五包定向 `Test(Task|TeamOutcome)`，全部通过（非 race，非最终 commit）。其 Go 生产 ZIP 序列化器输出 495 bytes，摘要 `sha256:0010360f529e2099d66c21feb5de1996fefd7e7a18f8304609842722c1531ea8`，已由客户端原 `validate_bundle` 成功消费三个文件。此项只证明跨语言格式兼容；文件为合成内容，不是原完整 Git 导出、HTTP 服务端或真实业务验收证据，后继完整调用链仍需验证。

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
