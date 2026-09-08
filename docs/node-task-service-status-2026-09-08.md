# Node Task 服务实施检查点（2026-09-08）

## 当前结论

正式主线是 ADR0088 的 Node-only Task 服务，不再扩充固定订单实验。当前代码集成基线 main=`82b64cfa8d0daef0c188f0e296299b6e3aedcc48`，已包含正式 HTTP、唯一 SQLite、Task 控制/执行 reducer、受管 ACP、Supervisor、输入上传/制品下载、不可变文件物化、通用业务文件适配、独立验收/Decision/最终交付、独立 HTTP 客户端、正式服务入口与目录发行包。**正式全链 Node 进程夹具已通过；真实模型团队与 API-STABLE 尚未完成**。B1/B2 保持 IN_PROGRESS，B3 保持 PLANNED；实验双 Pi 成功与正式组件证据分开。

2026-09-08 20:03 CST 已实际将 main 从 `ba2196b` 推送至 `82b64cfa8d0daef0c188f0e296299b6e3aedcc48`；本机 main 与 origin/main 一致，以上代码 pendingRemoteSync=false。[Node team CI](https://github.com/chiga0/marshal-harness/actions/runs/34223961302)已通过，通用[CI](https://github.com/chiga0/marshal-harness/actions/runs/34223961248)最近查询仍运行中。推送/测试不等于正式发行；以下历史检查点保留原范围。

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
| 正式 Node HTTP 服务组合 | `639d3a40b0d872f868ec4b69648200af15dafc69` | `f3e32340208c9e707192c3c0ab622b2e147cd969` | 2项恢复P1一次聚合修正并由同reviewer关闭；维护者13/13真实HTTP/SQLite通过，reviewer另有17项真实SQLite/depot断言；并非模型团队或实机部署 |

ACP Provider 和 ArtifactDepot 的定向测试、diff-check、secret scan、merge-tree 均通过后本地合入。本轮不调用 Marshal 原生二进制，不生成临时原生 checker。统计保留各包/各 source 范围，不把不同快照的测试数相加宣称完整 release 回归。

较早组合 `cb151984`：维护者使用固定 Node 24.15.0 串行运行七包（含既有 `agent-acp`，不含后来客户端）的已列测试入口，**119/119 PASS，24.92秒，零跳过**。包含真实 SQLite/文件 I/O/HTTP/所属 Node 子进程夹具，不含真实模型团队和安装部署；后来合入的执行 reducer/第二客户端不在此119项内。

后续组合 `5f511abe` 的八包验证为 **140/140 PASS，29.734秒，零跳过**。最新 `71e0c1fb` 的十包组合为 **178/179 PASS，44.906秒，零跳过**，不是全绿：ACP Provider期限测试在启动前已合法截止，但测试辅助函数无条件读取不存在的 `started.guardPid`，抛出 TypeError。已保留失败，正在将断言区分实际未启动与已启动清理；不能用增加期限或重复运行把原失败抹掉。该组合不含未合入服务入口、独立命令运行或真实模型团队。

该测试错误在 `9b474bb` 修正为：只有明确观察到立即取消/绝对期限且 `handle.started=null` 才允许没有Agent PID，仍要求原guard退出/组清理事实；正常成功仍必须有原PID。修订时一次机械替换误命中相邻测试，被本地运行的ReferenceError捕获并在提交前纠正，也计为作者错误。随后组合候选 `2bca3a26c1471022a0ddd88d4e92f98a1bdb8f4d` 实跑 **184/184 PASS，39.637秒，零跳过**，含十包加独立command入口，不含后来business/service或真实模型团队；与前次失败分别保留，不跨快照相加。

服务入口合入后的 main=`f3e32340208c9e707192c3c0ab622b2e147cd969`，维护者使用固定 Node 24.15.0 执行 `node --test --test-concurrency=1 packages/*/*.test.mjs`，**208/208 PASS，41.670秒，零跳过**。本次包含真实HTTP/SQLite/文件I/O/所属Node进程夹具及business/service，仍不包含在途最终验收、真实模型团队和部署，不升级B1/B2成熟度。

## 实机 Qwen 不再只验证握手

### 最新正式同链验证（主线 82b64cf）

| 内容 | sourceHead | localMergeSha | 证据 |
| --- | --- | --- | --- |
| 独立验收、Decision、批准布局与最终交付 | `c1800fb6064cc23557746fdfdd13b0b53e91c1e4` | `a7be7624b3d56c19290051db1b4a047604f57d28` | 同 reviewer 聚合复核，71/71真实SQLite/guard定向验证；作者不能自授验收 |
| 服务业务/验证端口组合 | `bcdf105f9a820189978f0eec2462d42bc24d7be0` | `18fb563219123b83bc70d6ec4926e22bf4a76307` | 独立审查通过，维护者18项真实HTTP验证 |
| 正式 HTTP 团队全链夹具 | `95786a974056f32dd20ba0ce390b6bd59029a12c` | `e8ffa18159d5f3336c31015e1c23b0f02ebf07b7` | 3/3：双作者实际进程重叠、独立验收/下载消费、错误成果拒绝、取消与正常重启；不是模型 |
| 确定性目录发行包 | `75316b49bbf42e28147ecf02ac46d60c2ede07be` | `659e18366118b8a28b4d5d20e6df08089fa92334` | 独立9/9及额外fsync故障断言；外置摘要核验，不自授发行信任 |
| CI全包覆盖及发行依赖接线 | `3383e0ecdc6825aa58148df4bb4e1d114046cb7b` | `82b64cfa8d0daef0c188f0e296299b6e3aedcc48` | 独立审查通过；该source与merge文件树完全一致 |

维护者在 `3383e0e` 实跑正式包测试及原六组 Node 实验回归：**307/307 PASS，128.174秒，零跳过**。命令为 `node --test --test-concurrency=1 packages/*/*.test.mjs experiments/node-team/providers.test.mjs experiments/node-team/business.test.mjs experiments/node-team/runtime.test.mjs experiments/node-team/http-handler.test.mjs experiments/node-team/http.test.mjs experiments/node-team/openapi.test.mjs`。包含真实HTTP、SQLite、文件和所属Node子进程，不含真实模型、Go全仓或生产部署。

同一 source 实际打包并独立核验：24个文件、361336 bytes，manifestDigest=`sha256:735347c6aaa64197e516af78e2ae92b527afe6f284a71c78fe7bd36cd9eceb8f`，本机目录 `/private/tmp/marshal-node-candidate.vbh1cf/package`。manifest 仍准确绑定 `3383e0e`，不倒填 merge SHA；该包不是已签名/stable/实机部署验收产物。

Core 验收发生两轮实际P1修正：第一轮补足失败验收证据并纠正虚假的首审统计；第二轮修正合法未启动路径被当作缺失验收receipt而导致Supervisor失败和容量未释放。最终 `c1800fb` 用真实guard的 no-start事实覆盖该路径；此前测试遗漏和返工成本保留，不把最后71项通过写成首轮通过。

### 较早单会话工具证据

维护者使用上述受管 Runtime、固定 Node 24.15.0、本机 Qwen Code 0.23.0 的原生 `--acp`，在独立目录读取公开 `input.txt`：部门甲17、乙29、丙36以及校验标记。未禁用全部原生工具、未复制登录数据、未偷偷改模型。

一次真实 prompt 返回 `end_turn`；观察到94项更新、1次原生工具调用，独立检查返回包含正确合计82与原标记。permission 请求数为0，只表明这次读取未触发询问，不能据此宣称真实问答已通过。Runtime 执行身份=`5e17942d-c134-4f7b-a313-20bd9fc6be32`，实际开始 `2026-09-08T09:40:49.112Z`，Agent 退出观察 `09:41:00.010Z`，guard 退出观察 `09:41:00.311Z`，`cleaned:true/reason:owner_stop`，stderr 0。该调用已结束，不是仍在后台运行。

这关闭“真实会话能否调用工具并返回结果”的疑点，不关闭完整 HTTP Task、双作者、独立业务 Decision、取消问答或重启恢复。此前固定样例的真实双 Qwen 业务失败继续计入成本，详见[原失败记录](qwen-integration-status-2026-09-08.md)。

## API 与后续关键路径

机器合同为 [正式 OpenAPI](../packages/task-api/openapi.json)，不是旧 `experiments/node-team/openapi.json`。main 当前17项真实 Application 操作：Task 创建/列表/详情/计划/确认/图/取消/暂停/恢复/事件/审计/Workers、Worker详情、Operation 查询，以及输入上传、制品详情和内容下载。服务组合另提供 health/ready/providers/supervisor 4项运行观察；问答与单Worker取消仍不能因路由已定义就计作完成。输入ready只表示原始输入可读，不表示最终业务成果已经验收。

执行 reducer 原稿 `5a5079d` 的23项测试没有捕获4个实际P1：较低计划期限未落实、历史完整输入导致合法大计划事务溢出、重启后旧取消Operation无法回填、进度推进用户控制CAS。已一次聚合修正为 `dcbcd1b`，27项通过并独立复核合入；其中合法64节点、每goal 8000字节全部完成，不增加Store限额。回合完成仍不等于业务验收。

接下来的并行工作是：正式HTTP真实Qwen规划→一次批准→双作者→独立检查/下载消费；B2按ADR0086/0088实现批准前有限关键问答；准备同资产Linux服务验证。共享Application由单一作者修改，验收驱动与发行检查不写其scope。

真实Qwen团队驱动是待执行的验收工具，不是新增成功证据。仅按 Mac ordinary-user dogfood 使用已有原生登录；权限回调不是OS隔离，Worker/Publisher凭证分离仍未证明，不能宣称production。问答、单Worker取消、第二Adapter、完整恢复及声明平台发行仍有未完成项。

部署盘点已完成：旧 `scripts/install.sh` 会走 Go，旧 release workflow 面向 RC1/native，不能拿来发行 Node stable。新的 Node CI 已纳入正式包路径及全部包测试；远端执行结果另行核对，不拿本地测试替代。发行目录及外置摘要核验已实现，安装后同版本恢复和声明平台实机验证继续推进。

## 本轮效率教训

- 2026-09-08 用户再次指出：子任务结束后没有立即补派造成空槽。已将“每次完成/退出/失败都检查并补派，不能补派则记录具体原因”写入仓库 `AGENTS.md`，作为后续会话入口规则；不是新审批流程。修正后已实际并行派发 Supervisor 聚合修复、Node 服务组合根实施和制品审查，主 Agent 实现 Application 上传/下载接线。
- 有空闲槽不等于任务仍在运行；已结束子 Agent 需要明确续派。开发、独立验证、下一步部署设计可交错，不要等全部组件完成才考虑组合入口。
- Artifact 首稿再次出现此前 Store 已暴露的“首次同步失败后 Open 绕过耐久屏障”。这是重复失误，不应包装成新架构成果。已在原包一次修正 Create/Open 共用屏障，并覆盖失败→再次失败→成功重开，不新建微型治理系统。
- 执行首审4项P1也计入失败成本：只测小图/单次进度/新owner的执行拒绝，不足以证明完整合同。已将最大合法图、降低期限、进度与用户控制组合、旧控制义务重启收口加入原包；首审不是零错误，不因最后全绿删除返工事实。
- 共享状态机只有一个作者；客户端、Provider、制品和控制器以明确端口并行。下一指标是真实用户任务结束并可消费成果，不是再增加 schema 或测试包数量。
