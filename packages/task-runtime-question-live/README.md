# Pi 运行中业务问答实机验收工具

这是显式、受版本管理的验证工具，不进入生产发行清单，不由普通 CI 调用模型。它复用正式 HTTP/Application/SQLite、Pi 原生 RPC/工具/登录、原进程托管、FileBusiness、独立 VerificationCommand 和 TaskClient，不提供新调度器。

业务：east 作者尚不知道统计状态，必须调用原生 `marshal_ask_user`，在 `paid` 与 `cancelled` 间向用户明确选择；west 同时独立统计 `paid`。两者从相同冻结合成销售输入产生各自文件。`--answer` 是操作者运行前明确提供的答案，仅在真实问题到达后经 HTTP 发送，不进入 Task 或模型提示。原检查器先核验 Core 的问题/答案/ACK 引用和 Worker 绑定，再从原输入重算，不相信作者的验收标签。

唯一主链：真实 planner → 一次精确批准 → 两作者并行、east 等答时 west 完成 → 一次 HTTP 答案 → 原 east Worker/Attempt 继续 → 独立验收 → 下载消费 → 正常关机及同版本冷开，原创建/批准/答案回执与成果可查且无重复启动。任何 CAS、超时、缺 ACK 或失败均保存失败证据，不重试、延长期限、换模型或额外批准工具。

```sh
/绝对路径/node-24.15.0 packages/task-runtime-question-live/driver.fixture.mjs \
  --execute-real --answer cancelled \
  --run-dir /private/tmp/新的私有运行目录 \
  --node /绝对路径/node-24.15.0 \
  --pi-entry /原安装/pi-coding-agent/dist/bundle/cli.js \
  --pi-sdk /原安装/pi-coding-agent/dist/index.js
```

`--run-dir` 必须不存在且父目录为真实路径。默认 Task 总期限 600000ms、4 Attempts（planner+2作者+checker）、2 个并发槽；题目至多 1 个、最多等待 120000ms，仍受原 Task 期限约束。只接受原 Pi 文件读写/编辑参数的一次授权，默认拒绝 shell、外部发布、越界文件和未知工具，不关闭原工具/Skill，不复制 HOME 或读取登录文件。

成功或失败只向 stdout 输出安全摘要；私有目录保存 `evidence.json`、下载成果及正式服务状态。证据包含实际 execution ID/时间、原组 cleanup、问答摘要、权限计数、独立验收和冷开结果，不保存原始模型日志、prompt 或鉴权配置。目录保留供诊断，不自动清理。

```sh
/绝对路径/node-24.15.0 --test packages/task-runtime-question-live/driver.test.mjs
```

无模型测试只证明驱动、预声明答案隔离、原计划合同、一次答案请求、反例与实际固定 Node 检查器；不冒充真实 Pi 团队或服务崩溃验收。实机由维护者在审查固定 source 后显式执行。普通用户 dogfood 不证明 Worker/Publisher 分权或恶意代码隔离，`production=false`。
