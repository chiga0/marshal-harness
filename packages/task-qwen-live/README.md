# Qwen 真实 HTTP 团队验收驱动

这是显式手工启用的验证工具，文件以 `*.fixture.mjs` 命名，不是产品默认业务、发行包依赖或 CI 模型调用。驱动复用正式 TaskService、SQLite、TaskClient、FileBusiness、ACP 和独立 VerificationCommand；不会启动 Marshal 原生二进制。

## 业务与证据

一份公开合成销售数据包含两个地区、取消记录、零额和负数退款。真实 Qwen planner 返回计划；驱动只接受 `east`、`west` 两个独立作者和一个可信 `verify` 节点，随后只发送一次新批准。不会把测试 `proposal()` 注入模型结果、改写计划、替换输出、减少断言或自动付费重试。计划格式不符立即失败保留现场。

批准检查消费 Core 确定性追加的完整 `policy`、`description`、文件布局及交付映射，并绑定实际上传的 input ID；不要求模型逐字复述中文验收句，也不把文字中出现 policy ID 当作可信策略。合理改述可通过；可信项缺失、篡改或绑定其他输入仍拒绝。业务金额与完整交付的独立 oracle 不变。

两个真实 Qwen 作者分别写各自报告，原生工具/Skill/登录仍由 Qwen 管理。版本管理的 `task-team-integration/checker.fixture.mjs` 在作者之外读取完整输出，父进程核对已冻结预期，再由 Core 接纳。最后通过正式 Artifact API 下载，独立消费者重新检查两份完整报告。并行依据原 Runtime 的 `started.startedAt` 与 `agentExit.at`，不是清理完成或 UI 时间；没有真实重叠就失败，不加人工延迟制造结果。

同版本正常 shutdown 后，以 `mode:open` 重开同一数据根，重放原 create/approve 键、请求与回执，查询原 Task 和同摘要成果；不得多起 planner/author/verifier。这里只证明**正常关闭后重开**，不冒称进程崩溃恢复、跨版本升级或 API-STABLE。

## 手工运行

先在当前候选上运行无模型测试：

```sh
/Users/gawain/.local/share/fnm/node-versions/v24.15.0/installation/bin/node --test packages/task-qwen-live/driver.test.mjs
```

维护者独立审查并确认当前 Core 修复已合入后，才显式运行以下命令。`--run-dir` 必须是不存在的新路径，父目录已存在且为真实路径；驱动创建私有 `0700` 目录，不接受复用旧根、不递归删除失败现场。

```sh
/Users/gawain/.local/share/fnm/node-versions/v24.15.0/installation/bin/node packages/task-qwen-live/driver.fixture.mjs \
  --execute-real \
  --run-dir /private/tmp/marshal-qwen-team-UNIQUE \
  --node /Users/gawain/.local/share/fnm/node-versions/v24.15.0/installation/bin/node \
  --qwen-entry /Users/gawain/.local/share/fnm/node-versions/v24.15.0/installation/lib/node_modules/@qwen-code/qwen-code/cli-entry.js \
  --timeout-ms 600000
```

不指定模型或模型 fallback；运行固定 Node 24.15.0 和实际安装的 Qwen `cli-entry.js --acp`，记录版本与入口摘要，不读鉴权文件或复制 HOME。传递环境仅为 HOME、语言、TMPDIR 和含当前 Node 的固定 PATH；不继承任意 token 环境。任务累计限额为 4 次执行、2 Worker、默认 10 分钟（可显式 1–15 分钟），不重置期限，不重试失败任务。

反向权限仅可一次允许当前作者的 `sales.json` 读取、自己报告的读取/写入/编辑；按实际已安装 Qwen 的 `kind` 和封闭 `file_path/content/old_string/new_string` 参数形状核对。shell、其他路径、未知参数、规划阶段操作和 `allow_always` 全拒绝。原生用户配置可能预先授权不再上报的工具；此回调**不是 OS 沙箱，也不证明消除了 ambient 权限**。缺少所需权限时如实失败，不自动扩大授权。

## 产物与边界

`evidence.json` 和下载的 `regional-report.json` 为 `0600`。证据只有 Task/Worker/execution ID、时间、状态、摘要、受限权限计数和重启结论；终端不输出 token、模型正文或原始日志。原服务数据根仍是私有现场，不能公开上传。失败后使用原所属句柄关闭，不依据磁盘 PID 杀进程。

本机同 UID 原生登录验收仅为 **Mac ordinary-user dogfood**；Worker/Publisher 分权未在此证明，不构成 production、正式 Provider 支持或 stable release。无模型测试只覆盖驱动参数、批准边界、权限、消费及原进程事实断言；真实 Qwen 成功与正式 HTTP 全链必须由单次实机证据另行报告。
