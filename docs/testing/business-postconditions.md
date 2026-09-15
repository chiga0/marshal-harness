# 确定性业务后验实验

这组实验回答哪些业务结论能由有限规则独立复算。它不注册产品 Verifier、不改变原批准计划、权限、预算或恢复状态机，也不生成可提交给 Core 的验证证据。结果固定带 `authority:false`，不能代替原 Review receipt、Verification receipt 或业务交付授权。当前入口是 `scripts/business-postconditions.mjs`，测试是 `scripts/business-postconditions.test.mjs`。

## 适用范围

| 验证器 | 能确定什么 | 不能确定什么 |
|---|---|---|
| `verifyOrders` | 对传入的冻结订单，独立计算 paid/refunded 的整数分与计数，排除 cancelled，零金额仍计数；严格核对最终 JSON 形状与所有数值 | 作者未用工具、附件身份是否经过产品批准、对其他业务状态的解释 |
| `verifyGuide` | 在预先批准的有限模板、三条步骤和两条注意事项内，逐字核对事实与所有可见内容；额外文字不能通过 | 任意自然语言的事实真假、合理同义表达、该模板是否已获原用户批准 |
| `verifyRecoveryModel` | 在显式声明的四步导入模型里，执行三个初态、五个崩溃边界、重启和重复执行，核对完整唯一索引、清单完成、原笔记不变 | 原 C01 自然语言是否忠实对应模型、真实实现是否实现该模型、文件系统断电和并发行为 |

`pass` 只针对上述范围；`fail` 是该范围内已确定的矛盾；`not-verified` 表示输入或表述超出支持范围，不按通过处理。未知表述不能仅凭关键词被判失败，也不能为了提高通过率自动改写或删减。

可序列化输入的每份结果包含输入与候选的 SHA-256；`undefined` 或循环对象等无法序列化的值，其对应摘要为 `null`，返回未验证，不伪造可复核绑定。订单绑定原始 JSON 文本；模板和模型绑定传入对象的序列化值。这是实验复核标识，不冒充产品 canonical digest、批准身份或不可伪造 receipt。

## 三类实验

订单测试直接引用现有 M01 的 `orders.json`：east 两单 1200 分，west 两单 600 分，总计四单 1800 分。JSON 使用原严格边界解析器，重复键、围栏、未知字段和数值字符串拒绝；输入含未知区域/状态或不安全整数时不验；中间求和使用 BigInt，超出最终安全整数范围不舍入。测试包含原输入、乱序输入、追加订单，以及漏算零金额、漏算/反转退款、跨区域不一致等错误。候选不提供期望答案，期望值由独立规则逐单计算。

S01 夹具是额外明确约定的有限表达实验，不是对原 S01 用户需求的偷偷收窄。模板内没有签到设施、物料、收费或免费承诺；任何新增文字（包括隐藏注释）均落 `not-verified`，已识别时间/地点字段冲突则 `fail`。原任务允许的其他正常表达仍需独立语义验收；不能拿本模板拒收它们，也不能把模板选择自动设为产品默认。

C01 夹具不是从原文本自动推断出的实现。正模型依次记录待完成清单、构建临时索引、替换索引、标记完成，只有清单已完成且内容一致才跳过。负模型只因清单存在就跳过；在记录后、索引前崩溃，恢复会漏索引，实验给出实际反例状态，不替它补出不存在的续建分支。

模型使用固定原子步骤和固定只读原笔记，遍历空态、已完成态、索引损坏态，每态在开始及四个步骤之后中断。临时索引在崩溃时丢弃。此处的对象状态仅是离线模型夹具，不是产品的第二套生命周期。测试不创建服务、不调用模型、不运行候选代码。

## 如何继续接入产品

现有 `createVerificationCommand`、`createVerificationPort` 与 `TaskApplication` 的原验证完成事务是候选接缝。本实验暂不接入：生产启用需要先定义受信业务 profile、输入附件/选果绑定、固定检查器身份、支持范围及原失败到 repair 的映射，并按原同根策略与 receipt 门禁测试。不得把任意候选传入的函数作为验证器，也不得让作者自选规则生成权威证据。

M01 可优先作为新显式业务 profile 的确定性内容检查。S01 若继续允许自由文案，有限模板只能作为用户明确选择的模式；C01 若交付仍是自然语言方案，必须先解决模型与方案对应的审查，才能把模型运行结果用于业务判断。即使将来模型对应已通过，也仍需对真实实现做独立崩溃恢复测试。

## M01 受控 HTTP 垂直切片

`packages/task-service/m01-postcondition.test.mjs` 提供一个仅测试配置的 M01 垂直切片。它真实启动 `TaskApplication`、SQLite、HTTP、`ArtifactDepot` 和 `createFileBusiness`，经 `task.create → Planner → task.plan → task.approve → Worker → VerificationPort → Decision/Artifact` 完成成功与业务错误两条路径。

作者夹具只生成结构合法的 `orders-summary.json`；独立验证器从 `ticket.input.inputArtifacts` 指向的 Depot 原始 bytes 读取 `orders.json`，从受控验证目录读取作者候选，再调用 `verifyOrders` 重新计算。因此验证器没有把作者复制的输入或作者报告作为原始事实，也没有把 `verifyOrders` 返回的 `authority:false` 实验结果直接当作可序列化权限声明。只有测试配置中受信的 `createVerificationPort` 才将其映射到现有 Core Verification receipt，以检查当前接缝的实际生命周期和交付结果。

该切片不修改 OpenAPI、SQLite 合同或现有 `acceptanceEvidence` 设计，不表示 ADR0107 已 Accepted，也不表示 `verifyOrders` 已成为通用业务插件 API。它证明当前受信装配可以把一个固定 M01 后验接入现有 VerificationPort；正式业务支持仍需单独冻结 profile、能力源码摘要、证据结构、旧客户端行为及真实恢复/发布边界。错误候选路径只保存独立失败证据，不保存 delivery。

## 复验

运行 `node --test scripts/business-postconditions.test.mjs` 检查离线规则，再运行 `node --test --test-concurrency=1 packages/task-service/m01-postcondition.test.mjs` 检查受控 HTTP 垂直切片。两者都属于受控 fixture，没有真实模型或真实外部业务效果证据；UI 三线不适用的范围仅限这些测试，不能据此更新产品体验为通过。

## C01 真实文件系统恢复受控切片

`packages/task-service/c01-recovery.test.mjs` 是当前 C01 的受控纵切测试。它另外启动真实 Node 子进程，在独立临时目录对冻结的 `notes.json` 执行四个有界步骤：创建待完成清单、写临时索引、以不可覆盖的 hard-link 替换索引、创建完成标记。测试在步骤开始前以及每个步骤完成后让子进程退出，再以新进程重启；重启沿已有文件事实继续，重复运行在完整状态上返回 `skipped`。每个场景由独立 `verifyRecoveryState` 重新读取原始输入和状态文件，检查摘要、完整行集合、完成标记和临时文件清理，不读取作者报告。

同一测试还覆盖 `record-exists` 对未完成记录的拒绝、完成记录的稳定重放和冲突索引的 fail-closed（原索引字节保持不变）。另有一条真实 `TaskApplication`、SQLite、HTTP、`createFileBusiness` 和 `createVerificationPort` 链路：验证器通过 Depot 读取原始输入、读取受控候选，成功时产生独立 evidence 与 delivery，业务错误只产生 evidence；服务重启后可重新读取同一完成结果。

这组测试证明的是受控 fixture 内的真实文件写入、子进程退出和独立读取。它没有把文件恢复器接入产品 Core，也没有把旧 generation 的未决 Worker 自动接管。当前生命周期在服务进程于 Worker 已启动后崩溃时会保留 `intervention` 和未决容量，禁止自动重派；这是安全边界，不是 C01 生产恢复已完成。若要把 C01 纳入产品能力，仍需先冻结 ADR0107 所要求的业务 profile、Attempt/Reservation 来源、恢复 intent/result 事务、来源 Artifact 与证据索引、独立 verifier 身份及旧客户端行为，再做正式 Core/HTTP 接线和冷故障验收。

复验：

```sh
node --test --test-concurrency=1 packages/task-service/c01-recovery.test.mjs
```
