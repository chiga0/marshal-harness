# 正式服务的跨包团队集成测试

本包只提供测试业务策略和确定性 ACP Agent 夹具，不注册生产模板、不替代模型，也不新增 Task 权威。主线实现均从正式包导入。

`team.test.mjs` 经真实 HTTP 创建输入和 Task，由受管 ACP Planner 提议、客户端精确确认一次、两个作者并行产出，随后通过受管独立 Node 命令校验整个交付。固定检查器不导入或执行作者代码；返回的结果只是数据，可信比较器逐项对照独立期望，最终仍由 Core 接纳。

测试覆盖：

- 真实 HTTP、SQLite、Depot、FileBusiness、原 ACP/command guard 与客户端全链；作者实际进程生命周期重叠，四次原 Attempt（planner、两作者、verifier）。
- 原批准请求重复不增加启动，正常交付下载消费，整个服务关闭重开后相同 Task/制品/原回执仍可读，无替身执行。
- 两个作者的 JSON 都能解析，但其中金额错误时独立验收失败，不发布交付；不能把组件格式正确当业务正确。
- 在途 HTTP cancel 等待所属进程实际 cleanup，不启动 verifier，冷重开不重派。

```sh
node --test --test-concurrency=1 packages/task-team-integration/team.test.mjs
```

使用已允许的固定 Node 24.15.0；不调用 Marshal 原生文件，不产生临时原生 checker。私有临时目录仅是本测试拥有的数据和执行目录，测试结束由其创建者回收。不删除用户文件或历史任务。

这些是无模型集成证据，不是实际 Qwen/Pi 团队、恶意同 UID 隔离、生产 SQL 发布、活跃崩溃恢复或正式部署证据，不据此关闭 B1/B2/B3。
