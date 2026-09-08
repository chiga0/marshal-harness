# ADR 0087：无 Marshal 原生依赖的 Node 本地团队实验

- 状态：Accepted（2026-09-08 用户明确要求按 Node HTTP→两个本机 Agent→收集/取消/重启恢复方向实施；仅授权本文实验，不批准全面迁移或 production）
- 基线：`18b7c785b22b7c66642856442a58efb091b4c21e`。

## 决策与边界

在 `experiments/node-team/` 实现独立 Node 标准库实验；不调用 Marshal 原生文件、不构建或加载本地扩展、不修改安全策略。不读取、迁移或复用旧 `.marshal` 运行状态/activation；实验数据使用显式独立私有目录及不同格式标识。ADR 0085 的 Go 生产路径保持不变，不能将实验结果冒充其 B1 或正式发布证据。

复用 Task/Worker/Artifact 的产品概念、一次精确确认、原生配置、独立验收和有限执行；本轮只判断解释器部署与最小团队纵切是否真实可行。成功后再审计是否迁移 Core，失败则保留原因，不原样付费重试。无 Workspace、身份平台、Skill 或发布能力。

## 最小形态

`node experiments/node-team/main.mjs --data-dir ABS --config ABS` 启动 loopback HTTP。配置由本地操作者提供，HTTP 不接受命令、路径、模型或环境。一个内部 Node supervisor 拥有状态和受管执行，HTTP 前端重启后认证重连同一 supervisor；这不是外部 watchdog 或监督 LLM。只允许一个 supervisor 写同一实验根，通过独占本地 socket/锁与实例握手拒绝并发接管。旧控制器不可达而归属未知时不删锁、不按旧 PID 杀进程、不自动启动替身；完整 supervisor/机器崩溃恢复仍待后继验证。

Supervisor 先耐久保存批准和启动义务再启动，取消先保存 fence；两作者独立目录、无自动 retry。HTTP 前端退出不杀活跃作者，重连继续观察原执行及原 deadline。所属组的存活 leader 保留到整组清理结束，不对回放出来的裸 PID 发信号。未知归属保留失败/未决。

首个真实样例是两个互补 Agent 生成数据处理模块：`normalize.mjs` 与 `report.mjs`，分别实现订单规范化和按 SKU 汇总。冻结接口/输入反例由 controller 提供，作者不能改变验收。先用本机已配置 Pi 两实例、禁用执行工具，通过原生 JSON 事件返回代码候选；不用模型自述作成功判定。后续 Qwen 接入不阻首条链。

候选只收允许文件名与有界 UTF-8 内容，在作者之外使用既有 Node 的权限受限子进程运行固定组合断言；通过后生成摘要清单及下载。失败、取消保留原输入/时间/原因，不把 exit 0 当验收通过。普通同 UID 不承诺恶意代码或 ambient credential 隔离；作者无 shell/读取/发布工具、不继承 Publisher 环境，不向模型提供连接 token、宿主配置或任何秘密。原生模型配置由 Agent 自行使用，不复制 HOME/鉴权文件。

## 冻结实验接口

- `GET /health` 最小健康；其余路由要求私有连接文件中的 Bearer token，拒绝 Origin 与错误 Host，仅绑定 `127.0.0.1`。
- `POST /v1/tasks`：`{intent}`，`Idempotency-Key`；返回 `{id,status:"awaiting-approval",revision,previewDigest,plan}`。
- `POST /v1/tasks/{id}/approve`：`{expectedRevision,previewDigest}` 与 key；精确重放不重复启动。
- `GET /v1/tasks`、`GET /v1/tasks/{id}`、`GET /v1/tasks/{id}/workers`、`GET /v1/tasks/{id}/audit`：真实投影，usage 不可见则 null。
- `POST /v1/tasks/{id}/cancel`：`{expectedRevision}` 与 key；202 为受理，确认所属执行结束后才 cancelled，已完成不改写。
- `GET /v1/tasks/{id}/delivery`：只下载已独立验收文件与 SHA-256 清单，不接受任意路径。

控制 revision 仅在用户控制动作/终态变化；进度不改变批准摘要。一个时刻最多一个活跃 Task、两个作者，默认每 Task 5 分钟、stdout 原生事件流 8 MiB、stderr 1 MiB、每文件 64 KiB、一个 Attempt；同 key 异正文拒绝。输入/HTTP/文件/日志均有界，数据及连接文件私有，raw Agent 输出不打印到用户终端。

2026-09-08 实机调整：初始每流 1 MiB 导致第二 Pi 的原生增量/部分消息事件在 1,049,607 字节时被截断，首位正常候选的流也达 726,044 字节。将事件传输预算显式独立为 stdout 8 MiB，不改变最终代码 64 KiB、完整终态、总期限或独立验收；仍有超过新上限的负测。保留首次真实失败，不能把这次有证据的预算调整描述成原候选成功或无界放行。

## 本轮退出证据

1. 确定性替身覆盖 HTTP/确认/重复请求/并发/失败/取消/前端重启、旧 supervisor 不可达拒绝及损坏状态拒绝。
2. 本机两个真实 Agent 的执行区间重叠，候选经过独立组合断言且下载后可消费；保存真实失败和用量缺失。
3. 真正取消所属 Agent、确认停止，不伤其他进程；活跃任务中重启 HTTP 后继续观察同一 Worker ID，不增加 Attempt。
4. 记录 Node/Agent/source/产物摘要与实际范围；代码审查无未解决 P0/P1。实验通过不升级 B2/API-STABLE/B3，不删除历史 Go 证据。
