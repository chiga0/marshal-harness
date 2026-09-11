# UI 内层故障关闭联动：受控回归

## 范围与边界

- 基线 `bda93e4c`，独立分支 `feat/ui-service-failure-close`。
- 按 ADR0088 的单服务与 ADR0098 的可选 UI 边界，修复内层故障已关闭而独立 UI listener 留存的问题。不改变 Task 生命周期、API、SQLite、owner/lease、恢复或发布权限，不新增 ADR。
- 原真实服务的最初内层异常仍未知。本次以独立临时根身份漂移触发确定性故障，不把这一触发器认定为原事故原因；不触碰原故障根，不运行真实模型。

## 先失败、再修复

`node --test --test-name-pattern='UI CLI exits' packages/task-service/launch.test.mjs`

补入测试后、生产代码修改前，真实 CLI 在根身份漂移后超过 4 秒仍未退出，命令退出 1（断言失败）。夹具随后仅对其自有子进程发送 TERM，失败证据保留于本机临时目录 `marshal-launch-CjmQl4`。这是测试失败，不是产品退出成功。

修复后同一测试要求：真实 `main --ui` 的静态页先返回 200，根身份漂移后不依赖外部 signal 自动退出 1，外层端口不再可达，只有一条关闭结果，原 SQLite 完整且替代根未写。另一参数在故障回调同时发送 TERM，验证共享关闭义务。通过测试的临时根按原夹具约定清理；断言先验证证据未被产品关闭过程删除。

## 修复要点

- signal 与有限终态服务诊断进入同一幂等关闭入口；启动屏障等待资源交接，避免启动中故障漏关后取得的 listener。
- edge 清理失败仍继续关闭内层；服务关闭异常不冒称 clean，失败退出码不被后来的 signal 覆盖。
- 仅输出编译期闭集 `code/stage/port`；不输出原异常、栈、Task 内容、token 或宿主路径。此信息不构成业务权威。
- 不自动重新取得 owner、不放宽过期校验、不重派任务。

## 验证命令与验收限制

```sh
node --test packages/task-service/cli-shutdown.test.mjs packages/task-service/launch.test.mjs packages/task-service/composition.test.mjs packages/task-service/leader.test.mjs packages/task-supervisor/controller.test.mjs
git diff --check
```

本机最终结果：65/65，通过，退出码 0（约 60.7 秒）；`git diff --check` 通过。首次扩大回归 64/65，唯一失败为既有诊断字段精确断言尚未列入新增的 `port`；已改为精确断言 `port=progress` 并验证原异常正文不泄漏，随后以上全命令复跑通过。

功能可靠性：以上定向自动化与真实 CLI/HTTP/SQLite 受控故障验证；不替代原事故根因定位或真实 Agent 长时运行验收。

视觉交互：没有修改页面布局或控件；本次验证服务端口失效，不声称真实浏览器视觉验收通过，适用性由独立 reviewer 确认。

产品可用性：未进行目标用户测试；不声称 UI 三线完整通过或可正式发布。
