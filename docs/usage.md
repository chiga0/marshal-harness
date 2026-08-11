# 日常使用

本页覆盖当前本地版本最常用的操作。第一次使用请先完成[快速开始](getting-started.md)。

## 标准流程

```bash
marshal task plan --task TASK.json --policy POLICY.json --run RUN_ID
marshal task approve --run RUN_ID --gate plan --actor USER_ID
marshal task run --run RUN_ID
marshal task verify --run RUN_ID
marshal task review --run RUN_ID
```

`review` 会准备真实代码差异和独立检查结果。完成审查并导入决定后，如需创建 Draft PR：

```bash
marshal task review --run RUN_ID --decision REVIEW.json
marshal task approve --run RUN_ID --gate publish --actor USER_ID
marshal task publish --run RUN_ID
marshal task accept --run RUN_ID
```

Marshal 不会自动合并 PR。

## 查看进度

```bash
marshal task status --run RUN_ID --json
marshal doctor --run RUN_ID --json
```

`status` 展示任务当前阶段；`doctor` 会对本地记录、工作区和可恢复状态做只读检查。自动化调用建议使用 `--json`。

## 任务中断怎么办

不要手工删除 `.marshal/` 或任务工作区。先运行：

```bash
marshal doctor --run RUN_ID --json
marshal task status --run RUN_ID --json
```

如果状态一致，可以重新执行当前阶段的同一命令。Marshal 会根据已有记录继续或安全拒绝重复操作。状态无法确定时，保留现场并创建新的执行，不要手工伪造完成状态。

## 如何安全清理

```bash
marshal task cleanup --run RUN_ID
```

清理默认先预览。任务仍在运行、工作区存在未保存改动或状态不完整时，Marshal 会拒绝删除。任务结果与审计信息应按团队保留策略归档。

## 使用建议

- 任务说明写清目标、允许修改的范围和可执行的验收命令；
- 小任务只运行必要测试，复杂或高风险任务再使用完整检查；
- 不要把 Token、密码、私有密钥或本机敏感路径写入任务描述；
- Agent 结束后始终执行独立验证，不只阅读它的总结；
- 发布前确认目标仓库、分支和 Draft PR 与预期一致；
- 多个任务并行时使用互不重叠的修改范围，并关注机器与 Provider 容量。

## 什么时候不该继续

出现以下情况时应停止并检查：

- Agent 修改了任务范围之外的文件；
- 验证命令没有运行或结果无法确认；
- 发布目标或远端分支与预期不一致；
- 需要把发布凭据交给 Coding Agent 才能继续；
- 本地环境不足以隔离不可信代码；
- 任务状态与实际工作区相互矛盾。

Marshal 在这些情况下默认停止推进，这是安全设计的一部分。
