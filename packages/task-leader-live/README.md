# 真实 Pi 受管 Leader 验收工具

这是手工显式执行的实机验收工具，不是产品依赖，不进入默认发行清单，CI 不调用真实模型。它使用原 `startTaskService`、SQLite、Pi native bridge、FileBusiness、Core Leader/Review 模板及私有接纳端口；不会模拟私有 receipt、修改 Store、篡改候选或自行推进 Core 状态。

当前无模型测试只证明驱动边界、原回复/输入绑定、固定 Node 文件 oracle 和只读 HTTP 消费器。Core/Publication 候选未冻结并集成之前，不运行真实模型，不声称本目录已证明完整 Leader。

## 显式运行

主 Agent 完成独立审查并集成精确候选后，使用已核对的本机 Node/Pi 入口：

```sh
/Users/gawain/.local/share/fnm/node-versions/v24.15.0/installation/bin/node \
  packages/task-leader-live/driver.fixture.mjs \
  --execute-real --answers paid,cancelled --allow-local-publication \
  --run-dir /private/tmp/PRIVATE_PARENT/NEW_RUN \
  --node /Users/gawain/.local/share/fnm/node-versions/v24.15.0/installation/bin/node \
  --pi-entry /ABS_PI_PACKAGE/dist/bundle/cli.js \
  --pi-sdk /ABS_PI_PACKAGE/dist/index.js \
  --timeout-ms 600000
```

Pi 两个绝对入口必须来自同一个原安装包，真实路径、包名和版本在任何模型启动前检查。保留原 HOME、登录、默认原生工具/Skill；不复制 HOME、不读取登录文件、不替换模型、不传 `--no-tools`。权限回调仅允许当前作者明确的 `sales.json` 读取及所属地区 JSON 的读写，Leader/Reviewer 不借模型工具写发布目录；未知工具默认拒绝。此业务授权不等于全局禁用原生工具。

运行父目录必须已存在且路径 canonical；新 `NEW_RUN` 不得存在。新目录为0700，包含：

- `data/`：原 v7 Service/Store/Depot/custody 数据，保持原格式和单写者。
- `reports/`：另一个固定私有本地报告目标，与所有服务目录不相交，由正式 Publication 端口发布；只读 loopback 服务仅 GET，不写入或生成报告。
- `leader-0-authorization.json`、`leader-1-authorization.json`：在 allow 前保存本次完整精确授权；不会包含 token。
- `leader-0-report.json`、`leader-1-report.json`：正式下载及原 HTTP GET 逐 bytes 相同的合成业务报告。
- `evidence.json`：有限状态、原执行/时间、摘要、权限计数、实际 Attempts/Leader 次数及失败阶段；不保存原生 stdout/stderr、token 或隐藏推理。

`--allow-local-publication` 只授权该固定验收场景在独立检查原 Task/Plan/Review/acceptance、原 delivery bytes 和派生报告名称之后，对展示的精确正文发送一次 allow。普通业务答案/plan approve 不代替此授权。目标不能由 Task 或模型传入路径、URL 或命令；报告只新增，不覆盖/删除。它是受信本机业务服务，不是 Marshal 软件发行、网站产品或第三方部署。

## 同配置的两种需求与真实证据

完整模式只创建一次业务/验证/Leader 配置，同一服务根依次执行 `paid` 和 `cancelled` 两个不同 Task 需求；只通过原 Task 输入、真实 Leader 提问/用户回复和计划批准改变需求。每个 Task 原 `timeoutMs/maxAttempts=17/maxWorkers=3` 不重置；9次 Leader 是 policy cap，不要求耗满。每个 Task 仅一次 plan approve、业务回复及精确发布 allow，无自动 CAS 刷新、模型 retry 或失败后换配置。

模型提示没有用户选择的答案或预期合计。原销售数据包含零额和负数；父进程从原 ticket 的 `inputArtifacts` 取 Depot bytes，验证原输入摘要后交固定 checker。答案从 Core `leaderReplies/leaderReplyRefs` 取，校验原 request/replyDigest；正式验收报告再与最初 HTTP 回复回执绑定。`publicationExpected({ticket})` 从同一原答案独立重算，绝不从 delivery 内容反推 expected。既有 oracle 只读 JSON，不执行作者代码。

两作者实际 `Provider.start` 前观察最终 prepared prompt：完整 Task、共享 Plan、本节点和原 Leader 回答必须都在工作包，Leader/Review 使用 Core 导出的完整输入模板。只存摘要和 coverage 布尔，称为 `handed-off` 观察，不证明模型确实理解/消费全部内容，也不宣称正则可证明自然语言正确性。独立 Review 仍从原需求和材料判断。

最终同时检查原独立 Review、固定 verifier、精确授权、原 publication/postverify Artifact、最后 Leader conclude、Task completed；发布端口自己实际 GET 后验，驱动还另一次 GET 与正式下载逐 bytes 比较。原进程开始/Agent exit（不是清理结束）证明两作者交叠，角色标签不替代独立执行身份。正常停服后重建同配置 Publication FD，校验同 configurationDigest/rootIdentity，再以原根 `open`；原 Task/Worker/Leader 视图、批准/回复回执和下载不变且没有新启动。两个 Task 共享该配置，不为第二任务改 oracle。

首轮正确记 `firstpass`，不制造修正。真实 repair 仍需实际独立拒收/意见、同计划局部重做与无关成果保持的另一次有界实证，本工具未触发则不记通过。职责独立不是 OS/凭证强隔离；证据始终 `production=false`、`publisherSeparationProven=false`，未知 usage 不填零。

## 首个可运行检查点

可显式增加 `--checkpoint first-workers`：只创建首个 Task，检查真实 Leader 缺项→原 HTTP 回复→Leader 再规划→一次 approve→两个作者原启动和完整工作包，然后一次原 HTTP Task cancel，等待原 cleanup/Operation 收口。报告 `checkpoint.passed=true`、`fullDelivery=false`、顶层 `passed=false`；退出0仅表示这个检查点通过，不表示 Review、发布、后验或整体交付已通过。不靠延时扣留作者或扩大期限凑窗口；窗口错过/409保留失败，不重试。

## 无模型测试

```sh
/Users/gawain/.local/share/fnm/node-versions/v24.15.0/installation/bin/node \
  --test --test-concurrency=1 \
  packages/task-leader-live/scenario.test.mjs \
  packages/task-leader-live/driver.test.mjs
```

测试使用明确标注的边界对象；不冒充真实 Application/SQLite 接纳。固定 checker 负例运行 checked-in Node 脚本和自有文件，不调用 Agent；原 HTTP 消费测试只监听 loopback。沙箱若拒绝监听，应保留错误并走正常授权，不改断言或宣称通过。实机模型调用由主 Agent 在最终冻结/独立审查后单次执行，失败代码与阶段保留，不原样反复付费。
