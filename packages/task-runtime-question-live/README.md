# Pi 运行中业务问答实机验收工具

这是显式、受版本管理的验证工具，不进入生产发行清单，不由普通 CI 调用模型。它复用正式 HTTP/Application/SQLite、Pi 原生 RPC/工具/登录、原进程托管、FileBusiness、独立 VerificationCommand 和 TaskClient，不提供新调度器。

业务：east 作者尚不知道统计状态，必须调用原生 `marshal_ask_user`，在 `paid` 与 `cancelled` 间向用户明确选择；west 同时独立统计 `paid`。两者从相同冻结合成销售输入产生各自文件。`--answer` 是操作者运行前明确提供的答案，仅在真实问题到达后经 HTTP 发送，不进入 Task 或模型提示。原检查器先核验 Core 的问题/答案/ACK 引用和 Worker 绑定，再从原输入重算，不相信作者的验收标签。

默认 `question` 主链：真实 planner → 一次精确批准 → 两作者并行、east 等答时 west 完成 → 一次 HTTP 答案 → 原 east Worker/Attempt 继续 → 独立验收 → 下载消费 → 正常关机及同版本冷开，原创建/批准/答案回执与成果可查且无重复启动。任何 CAS、超时、缺 ACK 或失败均保存失败证据，不重试、延长期限、换模型或额外批准工具。

```sh
/绝对路径/node-24.15.0 packages/task-runtime-question-live/driver.fixture.mjs \
  --execute-real --answer cancelled \
  --run-dir /private/tmp/新的私有运行目录 \
  --node /绝对路径/node-24.15.0 \
  --pi-entry /原安装/pi-coding-agent/dist/bundle/cli.js \
  --pi-sdk /原安装/pi-coding-agent/dist/index.js
```

`--run-dir` 必须不存在且父目录为真实路径。默认 Task 总期限 600000ms、4 Attempts（planner+2作者+checker）、2 个并发槽；题目至多 1 个、最多等待 120000ms，仍受原 Task 期限约束。只接受原 Pi 文件读写/编辑参数的一次授权，默认拒绝 shell、外部发布、越界文件和未知工具，不关闭原工具/Skill，不复制 HOME 或读取登录文件。

## 显式 Task 取消场景

同一 Pi 原生权限、业务计划、问答配置与 `node-execution-custody/v1` profile 支持显式 `--scenario cancel`，不接受 `--answer`。这与业务答案 `--answer cancelled`（统计取消订单）完全不同；没有 `--scenario` 时仍运行原问答交付链。

```sh
/绝对路径/node-24.15.0 packages/task-runtime-question-live/driver.fixture.mjs \
  --execute-real --scenario cancel \
  --run-dir /private/tmp/另一个新的私有运行目录 \
  --node /绝对路径/node-24.15.0 \
  --pi-entry /原安装/pi-coding-agent/dist/bundle/cli.js \
  --pi-sdk /原安装/pi-coding-agent/dist/index.js
```

取消链：真实 planner → 一次精确批准 → 两个原 author 进程的 started 与 HTTP `running` 投影一致 → 一次原 revision 的 `task.cancel` → 两个原句柄 `pi_provider_stopped`、cleanup、Task `cancelled` 与 Operation `succeeded` → 无 verifier/制品、容量归零 → 正常停服、同版本 open 后原创建/批准/取消回执及 Task 不变、零替身启动。冷开只精确重放原回执，不另发新 key 或 revision。

如果窗口内作者已完成、Task 已等答、期限已过或 CAS 冲突，记录失败而不是人为延迟模型、抑制原生提问、追加任务或重试。该场景只证明两个原作者进程被停止，不声称已消费 token、已调用工具或已发生业务问答；问答成功证据仍由默认场景提供。驱动复用已有取消 helper，仅显式区分 Pi 与 Qwen 的原停止原因，不能把两类原因混为兼容通过。

成功或失败只向 stdout 输出安全摘要；私有目录保存 `evidence.json`、下载成果及正式服务状态。证据包含实际 execution ID/时间、原组 cleanup、问答摘要、权限计数、独立验收和冷开结果，不保存原始模型日志、prompt 或鉴权配置。目录保留供诊断，不自动清理。

```sh
/绝对路径/node-24.15.0 --test --test-concurrency=1 packages/task-runtime-question-live/driver.test.mjs packages/task-runtime-question-live/cancel.test.mjs
```

无模型测试只证明驱动、预声明答案隔离、原计划合同、一次答案/取消请求、两族停止原因反例、实际固定 Node 检查器，以及原 Pi bridge/guard/custody/layout3 的 HTTP 取消与冷开。确定性取消 fixture 的作者停留在未批准的原生权限请求，不将这种测试等待加入实机配置；不冒充真实 Pi 模型团队或服务崩溃验收。实机由维护者在审查固定 source 后显式执行。普通用户 dogfood 不证明 Worker/Publisher 分权或恶意代码隔离，`production=false`。

## 显式单 Worker 取消场景（需已集成 ADR 0093 的 Core）

`--scenario worker-cancel` 与上述两种场景独立，不接受 `--answer`。配置是 `workerCancellation:{profile:'task-worker-cancellation/v1'}` 加原 custody/runtimeQuestions，使用新空 `layout:6` 状态根，**不开 `unpermitted`**，不把 prepare wrapper 声称成 staging-only。

```sh
/绝对路径/node-24.15.0 packages/task-runtime-question-live/driver.fixture.mjs \
  --execute-real --scenario worker-cancel \
  --run-dir /private/tmp/新的单Worker取消目录 \
  --node /绝对路径/node-24.15.0 \
  --pi-entry /原安装/pi-coding-agent/dist/bundle/cli.js \
  --pi-sdk /原安装/pi-coding-agent/dist/index.js
```

一次真实 planner/精确批准后，同时看到原 east/west 作者 started 和 HTTP Worker 身份，立即用 **Task revision** 发送一次 `worker.cancel`，目标只为 east。允许自然出现的 east `awaiting-answer`，但不回答问题。east 必须以原 `pi_provider_stopped` 和原 cleanup 收口；west 必须继续原执行并完成，不能随 east 停止。原 Operation 必须带同 Task/目标 Worker，最终 `succeeded`；最终 Task 为 `failed`/`code:worker_cancelled`，west 原结果保留，共同 verify 节点无 Worker，零 verifier/Decision/delivery、仅 3 Attempts、容量归零，原 deadline 不变。

服务原 shutdown 确认后，驱动只读自建状态根中这个 Task、west Worker、其精确 `resultRef` 的 SQLite 投影与来源事件，以及引用的原 Depot blob；核验原 reservation/plan/execution、摘要和实际 west 业务数值（2 笔、550 cents）。只输出身份/摘要及合成数据汇总，**不把未独立接纳的 west 候选称为 Task 交付**，不输出原报告/prompt/连接 token。正常 open 后精确重放原创建、批准、目标取消回执，原 Task/Operation/Workers/Graph 不变，停止后再次读回同结果和 bytes，零重复启动。这不是崩溃或权限分离证明。

仍限制 4 Attempts/2 Workers，默认且最大 10 分钟。窗口错过、原作者已完成、CAS 冲突、丢响应、非原停止原因或 unknown 均保留失败，不重试、不改发 Task.cancel、不延长模型工作。观察轮询不会阻塞 Provider；清理仅使用本次原句柄。

```sh
# 可在原基线执行：观察/身份反例、合成只读 SQLite、原 Pi bridge/guard 的无模型工具执行
/绝对路径/node-24.15.0 --test packages/task-runtime-question-live/worker-cancel.test.mjs
# 必须先集成冻结 v6 Core：完整真实 HTTP/SQLite/custody/Pi-bridge 夹具（无模型）
/绝对路径/node-24.15.0 packages/task-runtime-question-live/worker-cancel-http.fixture.mjs
```

第二条是显式无模型集成入口，未支持 v6 的基线会拒绝启动，不降级为模拟 API，也不 `skip` 后冒称通过。该夹具使用 checked-in SDK seam，原 read/write 真正生成 west 文件；仅夹具把 west 的原权限请求保持到 target HTTP 202，构成可重复的先后顺序。实机驱动不加载该 peer 或 gate。无模型结果与真实 Pi 结果分别记录，真实模型验收由维护者独立审查后执行。
