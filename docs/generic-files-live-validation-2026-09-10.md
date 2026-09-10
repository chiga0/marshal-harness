# 通用文件团队实机验证记录（2026-09-10）

## 当前结论

实现候选 `49041af6ebcfac317d8863f4fa9a5169b5ee9b2e` 尚未通过真实完整交付，不应发行或宣称开箱即用。ADR0099 接受的是验收范围，不是实现通过。

## 已取得证据

- 默认初始化和服务启动已进入真实 HTTP Task 链路；本机 Qwen 版本为 `0.23.2`，不是目标 DataAgent 的 `0.22.3-dataworks.0`。
- Task `task-8969b7c8-7c29-4069-861f-c71d3c5b880d` 的两名作者真实并行，分别完成主持流程与讨论问题文件；独立 Review 返回 `accept`。
- 后续 Leader 调用失败，错误为 `invalid_leader_decision`；未进行最终文件验收和交付。累计约 214 秒、6 次执行，重试及 rework 均为 0，token 用量未知。
- 本机私有证据目录为 `/private/tmp/marshal-generic-files-live-49041af6`。目录不提交；原失败 Task 保留、不改写为成功。

## 尚待定位的问题

单凭当前错误码不能区分返回格式错误与动作准入失败。启动器不转发子服务 stderr，已存在的诊断没有进入本次测试证据。不得据此猜测模型输出内容，或通过放宽校验让任务通过。优先恢复冻结输入与可用结果，只复现失败调用；不能恢复的原始输出明确标为缺失。

## 机器验证与实机验证分开

独立检查中 onboarding 16 项、通用团队 11 项通过；相邻回归最初在宿主高负载期间出现 3 项超时，随后按原时限逐项串行通过，没有提高超时参数。打包验证覆盖 69 个运行文件及独立安装启动/冷开。上述结果不能代替真实模型交付。

后续出口仍是两种真实文件任务完成、下载核对，以及取消和冷恢复无重复执行；生产发布、SQL 执行与补数据不在本默认团队证明范围内。

## 第二次实测：定位收尾循环

诊断留存改进候选 `747a75de` 已经独立审查及 11 项本地接入测试，默认仍静默，显式诊断只进入私有文件。此前冻结上下文的一次非权威 Qwen 诊断返回合法 `work.verify`，没有恢复或解释第一次原始返回。

第二个 Task `task-a7dbfbb0-a800-4fde-ae1d-b5a1d0139af4` 实际通过作者、独立 Review 与固定文件核验，但在 `finalizing` 连续提出相同 `deliver`，最终为 `failed/invalid_leader_decision`，累计 16 次执行，rework 为 0。不能将文件制品已存在等同于 Task 成功；终止原因以该错误码为准，不推断为 `budget_exhausted`。

独立源码核对发现：Core 存在 stage/delivery 事实，但新 Leader snapshot 不提供它们，只提供 history 摘要；重复 deliver 又被接受并产生另一个 delivery-ready 通知。ADR0099 已补充内部输入与重复交付约束，实施修复中。私有证据为 `/private/tmp/marshal-generic-files-live-747a75de`，原 Task 不回写，修复后需新候选实测。

## 收尾修复后的真实结果

`a7a7c808` 已通过独立审查、19 项 Leader 定向测试和原发布路径回归；打包仍为 69 个运行文件。首个 Task `task-c66f4a61-d271-4c8a-994a-07b15aedee7d` 完整 `completed`，Review 接受、验收通过，约 182 秒、9 次执行、0 rework；两份文件下载核对成功，冷开后 Task/Workers 不变。

同服务第二种需求 Task `task-7b1e9611-7238-4038-ab39-0306dac2e362` 为 `intervention/cleanup_unconfirmed`。两作者记录了 `acp_tool_scope_unproven`；所属 Agent/guard 退出已被观察，但完整清理及额外执行范围无法证明，Core 因此拒绝接纳。进程退出不证明额外范围无副作用，不清除原 intervention 状态。下一步在默认 Qwen 装配处约束可选工具，不撤销范围检查。证据目录 `/private/tmp/marshal-generic-files-live-a7a7c808`；两种需求共同通过的出口仍未完成。

## 最后一次验证与当前阻塞

`bce471ba` 的默认 Qwen 参数增量经独立检查通过，仍未解决第二种需求：`task-2026147f-9c7d-4c8b-b699-50b1bbcc47cb` 完整通过，131280ms、9 次执行、0 rework，文件下载与冷开不变；`task-13c0fe62-5ff2-4cde-8c22-d9c19111b005` 再次进入 `intervention`。本轮不再整队重跑，证据位于 `/private/tmp/marshal-generic-files-live-bce471ba`。

最终只读核查：问卷作者出现 `other:completed` 与 `acp_tool_scope_unproven`，开店清单作者正常完成；共 3 次执行，未进入 Review/验收。所属 Agent/guard PID 与 PGID 已无对应进程，但具体 other 工具与额外效果尚无可用证据，不能清除 `extra_scope_unresolved`。

不能把一种任务重复成功等同于通用能力稳定，也不能把参数配置等同于 Qwen 全工具注册表的闭集控制。当前不发行候选。下一步应以零模型测试及精确的 ACP 工具标识/许可/进展关联证据定位剩余兼容问题，再决定适配；不静默接受未知执行范围，不增加重试预算掩盖问题。已修复的 Leader 收尾循环、默认启动接线和诊断留存保留，不重做作者成果。
