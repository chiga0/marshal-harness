# 真实 Pi 同计划局部修正验收工具

只支持公开合成的双地区「先取订单最新版本，再统计 paid」业务。固定 Planner、两个并行作者、独立 Node checker；总预算六次 Attempt，首轮四次，最多剩两次供一个作者与完整 checker。全部规则和固定 proposal 在初始 Task 中可见，没有隐藏答案、预设错误、故意错误提示或事后改写 Worker 文件。不是通用需求规划器，也不保证模型自然犯错。

## 两次显式调用

先运行 `--phase initial`：该固定验收模式核对完整预期图、权限、规则、布局、预算、验收描述及摘要后一次批准，不代表面向任意需求的自动批准。仅在原独立内容拒收、恰一个可修分支时输出 `awaiting-explicit-repair` 和精确授权参数，保存 `negative-evidence.json`、`initial-evidence.json`、只读诊断快照 `initial.json`，正常关闭原服务。若首次正确，输出 `natural-first-pass`，完成下载消费和正常重开，但不称局部修正实机通过。两分支错误、结构/协议/执行失败、预算或期限不足均停止，不选择替身或重跑整队。

```sh
NODE=/absolute/path/to/node-24.15.0
"$NODE" packages/task-repair-live/driver.fixture.mjs \
  --execute-real --phase initial --run-dir /absolute/new/private/run \
  --node "$NODE" --pi-entry /absolute/pi/dist/bundle/cli.js \
  --pi-sdk /absolute/pi/dist/index.js --timeout-ms 900000
```

操作者先读原负报告，再在**同一原期限内**明确提供反馈与第一步输出的 Task/节点/revision/plan/Decision。不要修改输入、原快照、期限或预算。第二次只正常 open 原根，核对原 Task/计划/负报告/Worker；一次 HTTP repair，由原 Supervisor 执行新单分支和完整独立验收。

```sh
"$NODE" packages/task-repair-live/driver.fixture.mjs \
  --execute-real --phase repair --run-dir /absolute/existing/private/run \
  --node "$NODE" --pi-entry /absolute/pi/dist/bundle/cli.js \
  --pi-sdk /absolute/pi/dist/index.js \
  --task-id TASK --node-id east-or-west --expected-revision ORIGINAL_REVISION \
  --plan-digest sha256:ORIGINAL_PLAN --decision-digest sha256:ORIGINAL_NEGATIVE_DECISION \
  --feedback '根据原负报告，说明需要修正的业务内容；不改变规则'
```

第二步核对保留分支原 Worker 与精确 manifest/文件字节、总六次 Attempt、无 Planner/B 重派，下载后以独立算法再次消费，再正常重开核对原批准/修正回执和同字节成果。`repair-request.json` 是一次调用的诊断标记，不是权威 receipt；响应丢失时保持 unknown，工具不刷新 CAS/key、不自动重发，不能删除标记假装没请求过。202 只表示受理，只有最终独立验收和下载消费全部通过才输出 `natural-content-repair-completed`。初始服务退出正常，不测试模型运行中 crash。

使用固定 Node 24.15.0 和操作者已有 Pi 原生配置；不复制鉴权、不打印连接 token 或原始模型日志。最多三个首轮 Pi 进程和一个修正 Pi 进程；Node verifier 不是模型 reviewer。真实 token/cost 没有可核计量时保持 unavailable，不宣称免费或已对账。ordinary-user、`production=false`、`publisherSeparationProven=false`，不是恶意代码 sandbox、strong separation 或 stable 发行证明。失败和首次正确均保留，不靠反复付费尝试寻找通过样本。

## 无模型验证

```sh
/absolute/path/to/node-24.15.0 --test --test-concurrency=1 packages/task-repair-live/driver.test.mjs
```

定向测试覆盖显式参数、实际可见 proposal 与 Core 批准 binding、单次 HTTP repair/冲突不重试、原输入摘要、首次正确/不可修分类及独立下载消费。固定原 guard/command 用手工文件制造内容/结构反例仅用于无模型单元回归，不冒充实机模型自然错误，也不产生 Task Decision。完整 HTTP/SQLite 修正和 COMMIT 恢复由独立 `same-plan-repair` 六场景另行验证。此包为显式验收工具，不进入发行生产模块清单，不被 CI 自动调用模型。
