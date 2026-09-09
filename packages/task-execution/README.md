# Task Execution

`TaskExecutionCoordinator` 承接原控制器的句柄、prepare/start/collect、定时协调、取消和 cleanup 生命周期。准入、权限、预算和结果接纳仍由同一 TaskApplication/SQLite 原事务决定；这里不新增持久化真值、独立预算或业务计划器。

v7 Service 直接使用该执行组件。原 v1–v6 的 `TaskSupervisor` 导出保持兼容别名；`TaskObserver` 只接收读观察函数，不持有 Provider 或执行命令。Leader/Review/publication/postverify 都保留原 ticket、capacity、owned handle 和 finally 释放路径，opaque receipt 不经 structuredClone 丢失能力身份。

实际 v7 HTTP/guard 检查点和尚未验收的恢复边界见 [Service 说明](../task-service/README.md)。没有付费模型或恶意代码沙箱证明。
