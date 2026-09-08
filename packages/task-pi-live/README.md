# Pi 原生 RPC 的正式 HTTP 团队验收驱动

这是显式手工启用的验证工具，不是产品默认模板、默认发行依赖或自动 CI 模型调用。入口以 `*.fixture.mjs` 命名，复用正式 TaskService、SQLite、TaskClient、FileBusiness、Pi Provider/原生权限桥及独立 VerificationCommand；不启动 Marshal 原生二进制。

## 一条完整业务链

使用 `task-qwen-live` 已导出的业务输入、精确计划检查、独立下载消费和原进程重叠断言；复用的是 Provider 无关合同，不复用 Qwen 的 `file_path` 参数或 `proceed_once` 权限选项。公开合成数据含两个地区、取消记录、零额和负数退款。这是有界验收场景，不声称任意任务都需要两个作者。

1. 原生 Pi planner 接收完整 Task 要求，产出计划。驱动检查 Core 确定性绑定的 oracle、布局、原输入和最终交付，只发送一次新批准；不改写模型结果，不依赖模型逐字复述中文，不将预期数字提供给作者。
2. 两个真实 Pi author 使用本机原工具分别生成地区报告。FileBusiness 的实际 `onPermission` 仅允许本次批准的文件操作；原生 bridge 再绑定当前调用和所属执行。
3. 一个独立固定 Node checker 读取两份完整成果。父进程比较已冻结的全部业务断言，由 Core 生成接纳记录；不是作者自行返回 `passed`。
4. 使用正式 Artifact API 下载成果，独立消费者重新核对完整报告。依据原 Runtime `startedAt` 与 `agentExit.at` 证明两作者真实重叠，不用清理结束时间、不插入人为延迟。
5. 正常 shutdown 后重开同一数据根，查询原 Task、重放完全相同的 create/approve 请求和键、核对原回执与同摘要下载，不得增加执行。这里只证明同版本正常关闭后恢复，不能替代崩溃恢复、备份、升级或 API-STABLE。

## 启动与边界

维护者先合入通过复审的 Pi 候选并完成以下无模型测试，再决定是否进行付费实测：

```sh
/Users/gawain/.local/share/fnm/node-versions/v24.15.0/installation/bin/node --test --test-concurrency=1 packages/task-pi-live/driver.test.mjs packages/task-qwen-live/driver.test.mjs
```

仅以下显式命令会请求真实模型；`--run-dir` 必须为不存在的新目录，父目录须已存在且为真实路径。已有目录不会被接管或清除，失败现场保留。

```sh
/Users/gawain/.local/share/fnm/node-versions/v24.15.0/installation/bin/node packages/task-pi-live/driver.fixture.mjs \
  --execute-real \
  --run-dir /private/tmp/marshal-pi-team-UNIQUE \
  --node /Users/gawain/.local/share/fnm/node-versions/v24.15.0/installation/bin/node \
  --pi-entry /Users/gawain/.local/share/fnm/node-versions/v24.15.0/installation/lib/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js \
  --pi-sdk /Users/gawain/.local/share/fnm/node-versions/v24.15.0/installation/lib/node_modules/@earendil-works/pi-coding-agent/dist/index.js \
  --timeout-ms 600000
```

Pi 使用原生 `--mode rpc --no-session`，显式加载同源 native bridge；沿用原登录、配置、Skill 和 active tools。只传 HOME、PATH、语言、TMPDIR 白名单，不复制 HOME/鉴权文件，不换模型、不自动重试或 fallback，不添加禁工具/禁扩展标志。记录实际安装版本和入口摘要，不以品牌版本白名单代替协议能力检查。

此销售业务的权限闭集为：当前作者读取 `sales.json` 和自身报告；写入自身报告；按 Pi 的 `path`、`content`、`edits:[{oldText,newText}]`（或其有界旧形状）编辑已有自身报告。只选当前 `allow_once/allow-once`，拒绝 shell、来源覆盖、另一作者成果、未知参数、链接及广泛授权。它是业务的明确范围，不是全局修改 Pi 工具配置或恶意代码沙箱。

总预算四次执行、两个 Worker、默认十分钟，不重置截止时间。不新增取消实现；失败/退出使用原所属服务和执行句柄停止，未知清理不伪称成功，也不按磁盘 PID 发信号。

`evidence.json` 与下载的 `regional-report.json` 为私有 `0600`。证据仅含 Task/Worker/execution ID、状态、时间、摘要、权限计数、原清理及正常重开结果；不输出 token、模型正文、原始配置或日志。私有服务数据根不可公开上传。本机同 UID 原生登录仅为 ordinary-user dogfood，始终标记 `production:false` 和 `publisherSeparationProven:false`；无模型回归不代表真实 Pi 团队已成功、第二 Provider 已正式支持或 stable 已发布。
