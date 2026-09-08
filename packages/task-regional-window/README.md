# 按日期区间汇总已付款流水

`regional-paid-window/v1` 是一个实际可接入的有限业务配置：用户给出原始流水并要求按日期区间汇总，但没有给起止日期时，两个问题都必要。它不修改原完整 sales 业务、不按文字长度猜问题、不把任意自然语言需求说成已支持。

本包复用正式 `TaskApplication / SQLite / HTTP / Supervisor / FileBusiness / Managed ACP / VerificationCommand`；没有自己的 Task reducer、SQL 账本、启动队列或恢复权限。未使用 Git。当前定向回归使用真实 Node 受管 ACP 夹具与 checker **而非真实模型**；本包没有执行真实 Qwen，不声明 production、权限隔离、完整恢复或 B2 已完成。

## 支持的业务边界

- Task intent 为“按日期区间汇总东、西两个地区的已付款流水”，仅一个上传输入；数据是 UTF-8 JSON `{ "rows": [...] }`，不超过 64 KiB、1–512 行。每行恰有 `date/region/status/cents`：日期为 2000–2099 年的严格 `YYYY-MM-DD` UTC 日历日，region 为 east/west，status 为 paid/cancelled，cents 为绝对值不超过 10¹² 的安全整数。重复行分别计作流水，不按未声明的主键去重。
- 起止日均包含、区间最多 366 日；只计 paid，退款和零额保留。缺哪个日期问哪个，已有日期不重问。已有日期可在原 `context.text` 中以 JSON 对象 `{ "startDate":"…", "endDate":"…" }` 声明；此配置不接受其他 prose 或任意新槽。起止反向或非法日期拒绝，不代猜。
- 两作者 east/west 各只读 `sales.json`、写自身 JSON；唯一终点 verify 独立验收完整两文件。固定 DAG、路径、整数算法、验收策略、源码身份与原预算不随答案变化。日期只填 Core 冻结的业务 context。初始默认总预算 600 秒、4 Attempts、并发 2；driver 可明确选择 60–900 秒，创建后不可延长。缺日期路径只消耗两作者加验收共 3 Attempts；完整输入保留原零问题 Planner 路径，不补无意义问题。
- 只按原生 Agent **当次提供**的权限请求允许本目录明确文件读写，不允许 shell、跨分支、修改输入、外部发布或永久授权。这不是恶意代码 sandbox，不能撤销 Qwen 配置中已授予的原生权限；服务须使用可信数据和适当隔离的普通用户配置，不能据此证明 Worker/Publisher 分权。

## 启动现有正式服务

使用已安装、允许执行的 Node 24.15.0 与 Qwen 入口。Qwen 自行读取原生模型配置、鉴权和 Skill；本包不读取或复制其凭据，不写入 Provider 配置，不把 Qwen 版本硬锁为某个 CLI 版本。部署者显式提供入口，配置模块仅创建原有 Adapter，不启动模型：

```sh
MARSHAL_QWEN_ENTRY=/absolute/qwen/cli-entry.js \
  /absolute/node-24.15.0 packages/task-service/main.mjs \
  --root /absolute/private-parent/window-state --mode create \
  --config /absolute/repository/packages/task-regional-window/service-config.mjs
```

父目录须事先存在、canonical、当前 UID 所有且 `0700`。之后正常启动用 `--mode open`，不是导入旧 `.marshal`。服务会给出 connection 文件路径；该文件为 `0600`、含本地 Bearer，不能公开。未知执行仍由原 Core 保留，不由本配置补派或按存储 PID kill。

其他受信组合可调用 `createRegionalWindowConfig({provider,executable,onExecution?,onPermission?})`；测试显式注入夹具 Provider，正常 Qwen 用 `createQwenWindowConfig({node,qwenEntry})`。二者共享同一业务模板和验收能力；没有 Fake fallback。

## 两个阶段，只确认最终方案一次

先“澄清”，再“执行”。下面三个 CLI 调用不等于三次人工批准；只有最后 `complete` 批准一次。所有路径均须绝对路径，session/output 新建、拒绝覆盖，session 为受保护的原输入与回执文件，不存 token、不代替服务器真值。

```sh
# 1. 先提交真实缺日期的请求。没有隐藏答案，也没有执行权限。
node packages/task-regional-window/driver.mjs intake \
  --connection /absolute/connection.json --input /absolute/sales.json \
  --session /absolute/new-session.json --key operator-window-1

# 2. 操作者看到问题后明确回答；输出完整最终 preview 和精确 approval 参数。
# 此调用不会批准，也不会启动作者。
node packages/task-regional-window/driver.mjs answer \
  --connection /absolute/connection.json --session /absolute/new-session.json \
  --start-date 2026-09-01 --end-date 2026-09-02

# 3. 看过上述最终方案后，原样填入它给出的参数；不是另建 Task。
node packages/task-regional-window/driver.mjs complete \
  --connection /absolute/connection.json --session /absolute/new-session.json \
  --expected-revision 3 --plan-revision 3 --plan-digest sha256:EXACT_RETURNED_DIGEST \
  --output /absolute/new-delivery.json
```

driver 不自动猜日期、不自动重试请求、不另造 key/Task、也不自动取消。中断或 HTTP 结果不确定时先查原 Task；intake 响应丢失可由操作者以**同 key、同原输入及原 timeout**重放，不能以新 key 自认失败重建。answer 显式重调先读当前问题，已消费答案必须完全一致；complete 的重调仍发送同批准 key/body，依赖 Core 的原回执先于 CAS。过期、取消、失败或未知清理不会被当成完成。完成后仍可使用原 TaskClient 查询/下载；driver 的等待期限沿原 Task，不负责自动恢复或延长窗口。

## 数据与独立验收链

`VerificationPort.bindPlan` 固定布局和可见完整策略；日期不拼进可变验收文字。reservation 将最终 Task input、原上传 manifest、全部作者 manifest 绑定到 ticket；独立命令只读取这个 ticket。受信业务组合用 Depot 按原 digest/bytes 读取并校验上传 bytes，通过有界 stdin frame 送原数据与最终日期，**不读取 driver 闭包里的答案或作者复制的源数据**。新增业务没有放宽验收布局去挂任意输入路径。

固定 checker 进程读取两份作者报告，按原流水和最终日期独立重算；父进程的固定断言再次使用原 ticket/Depot 重算，不能只接纳 `pass:true`。报告必须精确匹配，日期漂移、遗漏区间边界或全量汇总均失败。checker 使用原 `launchCommand` 的身份、期限、退出、完整输出及 cleanup，不执行作者代码。

通过后 delivery 从 ticket 指向的不可变作者 manifests 取回**两份完整原文件 bytes**；它们还必须匹配检查结果。原 Core 独立重查 owner、ticket、期限、cancel、全部 manifests、cleanup，再同事务接纳 Decision、引用和 Task completed。本包不签 Decision。下载 consumer 使用原上传 bytes 和从接受预览读取的日期独立重算，不复用父 oracle 的 `expected()`，不依据 HTTP 200 或 Task completed 自认业务成功。

## 定向验证

```sh
node --test --test-concurrency=1 packages/task-regional-window/window.test.mjs
```

回归包括严格 UTC/金额/输入边界、仅真正缺失槽提问、冷问答重开、非法/反向日期与旧 preview 零派发、可读预览后的精确批准、两个真实 ACP 夹具进程重叠、原受管 checker、完整下载消费与正常重开、错区间报告拒绝、原生 offered-file 权限负例，以及实际 driver CLI 的问答/批准分离。夹具模块仅测试显式配置可选，正常 `service-config.mjs` 不引用它。后续真实 Qwen 验收需另行明确授权与实证，不由这些测试自动触发。
