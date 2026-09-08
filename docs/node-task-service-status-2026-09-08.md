# Node Task 服务实施检查点（2026-09-08）

## 当前结论

正式主线是 ADR0088 的 Node-only Task 服务，不再扩充固定订单实验。当前代码集成基线 main=`90591d5e37e6e6eac247f6e19efae853232b9ada`，已包含正式 HTTP、唯一 SQLite、Task 控制/执行 reducer、受管 ACP、Supervisor、输入上传/制品下载、不可变文件物化、通用业务文件适配、独立验证命令运行和独立 HTTP 客户端。**正式服务入口仍为审查候选，完整自主交付和 API-STABLE 尚未完成**。B1/B2 保持 IN_PROGRESS，B3 保持 PLANNED；实验双 Pi 成功与正式组件证据分开。

最新已知 origin/main=`ba2196bea33e6f007809f75f9671928c892bfa11`（此前 fetch 结果，本检查点未重新查询远端），pendingRemoteSync=true。本表记录的是本地 merge，不表示远端 main 已同步、CI 已运行或已发行。

## 已合入的源码与证据

| 内容 | sourceHead | localMergeSha | 验证摘要与边界 |
| --- | --- | --- | --- |
| 受管 ACP Runtime | `506a0a99df06d09263240a0c1c5737c40f478608` | `f082e37ad1533e09c5ba55ce16d78c8519beb74f` | 独立代码审查，9 项真实 Node 子进程夹具通过；不是恶意代码沙箱 |
| 正式 Task HTTP 契约 | `58bb756ff04f8828b195f1cd728f89a1d5e50223` | 已包含于 `08ca8356d92d171014a88394e2048db1093a8e04` | 24 操作、39 schemas；10 项 HTTP 测试、39 项 Draft 2020-12 metaschema 通过；不等于24操作业务均实现 |
| Node SQLite Store | `8fd6ddcc0fd6425a43c4fd52c4abc31aebf986be` | `08ca8356d92d171014a88394e2048db1093a8e04` | 同事务、分页、owner、故障与冷重开共39项独立测试通过；无 native addon |
| HTTP→Task Application→SQLite | `d895403` | `119604c77dc44e6e3aa7b2e6e66e3f024bc0cac3` | 14项定向测试通过，含真实 loopback；规划由测试内部端口提议，未自动消费执行义务 |
| ACP Provider | `3054e86c93960e54c0320641f6a2b146ee50d923` | `c7b1400d282aea4d0ed4aeec2667c646acf22414` | 独立审查无 P0/P1，7项真实 Node 夹具测试通过；即时取消句柄、权限撤销、输出有界、等待原 cleanup |
| ArtifactDepot | `a9e485f8fa09b3172c13b33cbfc50f5708960aa5` | `cb151984b1d6cd4366fce9c7342ada79df3e81f7` | 同 reviewer 复审 P1 关闭，19项真实文件 I/O 故障测试通过；只存 CAS bytes，不自授交付/验收权威 |
| 正式 HTTP 客户端 | `73aee1c149c28d7c4861e5b2758cb96ac443329f` | `dfa0f13afb878ded6e50f271fe9f0b8a54467ac5` | 独立 review及P2修正复核通过，root实跑8项含真实loopback；没有自动批准/重试/刷新CAS |
| 持久执行及控制恢复 | `dcbcd1bc68b33e5a01ff9851a939f972f484a279` | `5f511abe0bcb511a5554d7da01fda1328d9f3471` | 一次聚合修复4个P1并由原reviewer复核关闭；root同源27/27含真实HTTP通过，Worker回合不冒充最终验收 |
| 文件物化与精确采集 | `8bb6e7ffae8bc75fb8c62cac6806519416a18bde` | `076afef53b02db4de1dad179fa72add1296a92de` | 独立审查无P0/P1，独立与维护者各9/9真实FS/Depot测试通过；仅产候选，需先核实原执行cleanup |
| HTTP输入/制品接线 | `2f4ff6936e2ce5024f0b600ae6c6fde1fefa3ebd` | `3c9eed9d700f922926bef721f7b3d58d8ab54d30` | 独立审查无P0/P1，维护者33/33 Application测试含真实HTTP通过；输入manifest/原receipt/Task摘要绑定，已提交缺失bytes不修复 |
| 自驱Supervisor及失败先fence | `ee36d7a5221b8d7dacf6a6246f8dcde3edaced90` | `71e0c1fb06314a5abf73f4f4464e5dea81691605` | 原唯一P1聚合修复，同reviewer37/37通过；cleanup等待中不再派同Task下游，其他Task继续 |
| 独立验证命令运行 | `9b474bbd6c014e6756972a119488b8635589d071` | `22d536044c557f1a650410fc1627875af23f4857` | 独立review无P0/P1，21/21真实Node运行组合通过；复用原guard，不伪造ACP end_turn或业务通过 |
| 通用业务文件适配 | `f594271df2f1a1264add304391a316dfb3b4802e` | `90591d5e37e6e6eac247f6e19efae853232b9ada` | root独立逐行审查及39/39 business/files/depot组合通过；作者受git写入限制，由root核对三文件SHA后提交；仍需Core批准布局/执行观察端口 |

ACP Provider 和 ArtifactDepot 的定向测试、diff-check、secret scan、merge-tree 均通过后本地合入。本轮不调用 Marshal 原生二进制，不生成临时原生 checker。统计保留各包/各 source 范围，不把不同快照的测试数相加宣称完整 release 回归。

较早组合 `cb151984`：维护者使用固定 Node 24.15.0 串行运行七包（含既有 `agent-acp`，不含后来客户端）的已列测试入口，**119/119 PASS，24.92秒，零跳过**。包含真实 SQLite/文件 I/O/HTTP/所属 Node 子进程夹具，不含真实模型团队和安装部署；后来合入的执行 reducer/第二客户端不在此119项内。

后续组合 `5f511abe` 的八包验证为 **140/140 PASS，29.734秒，零跳过**。最新 `71e0c1fb` 的十包组合为 **178/179 PASS，44.906秒，零跳过**，不是全绿：ACP Provider期限测试在启动前已合法截止，但测试辅助函数无条件读取不存在的 `started.guardPid`，抛出 TypeError。已保留失败，正在将断言区分实际未启动与已启动清理；不能用增加期限或重复运行把原失败抹掉。该组合不含未合入服务入口、独立命令运行或真实模型团队。

该测试错误在 `9b474bb` 修正为：只有明确观察到立即取消/绝对期限且 `handle.started=null` 才允许没有Agent PID，仍要求原guard退出/组清理事实；正常成功仍必须有原PID。修订时一次机械替换误命中相邻测试，被本地运行的ReferenceError捕获并在提交前纠正，也计为作者错误。随后组合候选 `2bca3a26c1471022a0ddd88d4e92f98a1bdb8f4d` 实跑 **184/184 PASS，39.637秒，零跳过**，含十包加独立command入口，不含后来business/service或真实模型团队；与前次失败分别保留，不跨快照相加。

## 实机 Qwen 不再只验证握手

维护者使用上述受管 Runtime、固定 Node 24.15.0、本机 Qwen Code 0.23.0 的原生 `--acp`，在独立目录读取公开 `input.txt`：部门甲17、乙29、丙36以及校验标记。未禁用全部原生工具、未复制登录数据、未偷偷改模型。

一次真实 prompt 返回 `end_turn`；观察到94项更新、1次原生工具调用，独立检查返回包含正确合计82与原标记。permission 请求数为0，只表明这次读取未触发询问，不能据此宣称真实问答已通过。Runtime 执行身份=`5e17942d-c134-4f7b-a313-20bd9fc6be32`，实际开始 `2026-09-08T09:40:49.112Z`，Agent 退出观察 `09:41:00.010Z`，guard 退出观察 `09:41:00.311Z`，`cleaned:true/reason:owner_stop`，stderr 0。该调用已结束，不是仍在后台运行。

这关闭“真实会话能否调用工具并返回结果”的疑点，不关闭完整 HTTP Task、双作者、独立业务 Decision、取消问答或重启恢复。此前固定样例的真实双 Qwen 业务失败继续计入成本，详见[原失败记录](qwen-integration-status-2026-09-08.md)。

## API 与后续关键路径

机器合同为 [正式 OpenAPI](../packages/task-api/openapi.json)，不是旧 `experiments/node-team/openapi.json`。main 当前17项真实 Application 操作：Task 创建/列表/详情/计划/确认/图/取消/暂停/恢复/事件/审计/Workers、Worker详情、Operation 查询，以及输入上传、制品详情和内容下载。其余7项不能因路由已定义就计作完成。输入ready只表示原始输入可读，不表示最终业务成果已经验收。

执行 reducer 原稿 `5a5079d` 的23项测试没有捕获4个实际P1：较低计划期限未落实、历史完整输入导致合法大计划事务溢出、重启后旧取消Operation无法回填、进度推进用户控制CAS。已一次聚合修正为 `dcbcd1b`，27项通过并独立复核合入；其中合法64节点、每goal 8000字节全部完成，不增加Store限额。回合完成仍不等于业务验收。

当前并行工作按实际依赖分工：

1. 正式 Node 服务候选原稿 `4e6726d7` 独立审查发现2个P1：旧未派发pending在取消后仍永久阻止ready、ready门禁先于原幂等回放。已一次聚合修正为 `639d3a40b0d872f868ec4b69648200af15dafc69`，作者与维护者各13/13真实HTTP/SQLite通过，原reviewer复核中，不是已合入或已部署。
2. 通用业务 prepare/collect：使用批准的明确布局、当前ticket输入与上游Depot快照；不从模型自由文本推导文件权限，不采用旧Worker目录。
3. 独立验收作为批准DAG内受管终点，复用预算/取消/清理；非ACP验证命令复用现guard，随后将精确receipt、Decision和交付引用在同SQLite事务接纳。禁止把Agent end_turn或候选manifest当Task completed。

部署盘点已完成：旧 `scripts/install.sh` 会走 Go，旧 release workflow 面向 RC1/native，不能拿来发行 Node stable；现有 Node CI 仍只覆盖部分实验/ACP，正式包路径与测试须随部署纵切补齐。复用既有 loopback/私有连接文件经验，不另造身份平台；以版本化 JS＋OpenAPI 资产清单、已允许的 Node 启动和安装后同版本恢复验证交付。

## 本轮效率教训

- 2026-09-08 用户再次指出：子任务结束后没有立即补派造成空槽。已将“每次完成/退出/失败都检查并补派，不能补派则记录具体原因”写入仓库 `AGENTS.md`，作为后续会话入口规则；不是新审批流程。修正后已实际并行派发 Supervisor 聚合修复、Node 服务组合根实施和制品审查，主 Agent 实现 Application 上传/下载接线。
- 有空闲槽不等于任务仍在运行；已结束子 Agent 需要明确续派。开发、独立验证、下一步部署设计可交错，不要等全部组件完成才考虑组合入口。
- Artifact 首稿再次出现此前 Store 已暴露的“首次同步失败后 Open 绕过耐久屏障”。这是重复失误，不应包装成新架构成果。已在原包一次修正 Create/Open 共用屏障，并覆盖失败→再次失败→成功重开，不新建微型治理系统。
- 执行首审4项P1也计入失败成本：只测小图/单次进度/新owner的执行拒绝，不足以证明完整合同。已将最大合法图、降低期限、进度与用户控制组合、旧控制义务重启收口加入原包；首审不是零错误，不因最后全绿删除返工事实。
- 共享状态机只有一个作者；客户端、Provider、制品和控制器以明确端口并行。下一指标是真实用户任务结束并可消费成果，不是再增加 schema 或测试包数量。
