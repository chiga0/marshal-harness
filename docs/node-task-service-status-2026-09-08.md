# Node Task 服务实施检查点（2026-09-08）

## 当前结论

### 22:07 CST Pi 正式 HTTP 团队实机通过

后续独立完整组合已结束：精确`74a239effa975b0ba66b9922b0543227a8a878f1` **374/374 PASS，182.405秒，零失败/取消/跳过，exit0**，运行后HEAD不变且clean。日志 `/private/tmp/marshal-node-combination-74a239e.750O4gfB/results.log`，SHA-256=`2703f3786d1e189ffef651fecf535b05dbe44480e74f87267e310b1a9fd397b9`。这是Pi整合候选的一次完整无模型组合，不与regional候选347项相加；覆盖下文“运行中”的历史时点。

Pi独立整合候选 sourceHead=`74a239effa975b0ba66b9922b0543227a8a878f1`（未合main）完成一次真实HTTP团队验收：Task=`task-93be382b-31e2-4bb8-8d47-8067a2de4847`，`14:06:36.307Z`→`14:07:13.371Z`，**37.064秒**。Pi0.84.4/Node24.15.0，原生planner→一次精确批准→两作者→独立固定Node verifier→下载消费→正常同版本服务实例重开；无自动重试。四个HTTP Worker均completed，两作者原生命周期交叠 **9.403秒**，三个Pi原执行cleanup均确认，独立verifier启动一次。

本次原生工具权限回调 **allowed=5、denied=1**，不是零回调握手。交付184 bytes，独立消费确认4笔/1825 cents，实际下载摘要 `sha256:ee6166dd4ce3e6516262414a5e032dd999083ae50b60825e85f4df5482cf56e8`；正常实例重开保留原create/approve回执和同字节成果、重复启动0。证据 `/private/tmp/marshal-pi-team-20260908-220700/evidence.json`，交付同目录 `regional-report.json`；执行时Git clean/HEAD核对绑定source，JSON本身不含sourceHead，不把它当签名收据。

Pi入口摘要 `sha256:5406c369954516fb56879d685e082ff9095cd6e06e41af406f394942377fd4bf`，SDK入口摘要 `sha256:82cb4ea864f3d8816c06bc8f2f2d9a8d82d883297af179dc69d287d042834844`。保留 **ordinaryUser=true、production=false、publisherSeparationProven=false**；不将正常实例重开当宿主崩溃恢复，不将第二Provider单次团队成功当完整API-STABLE。

整合仅含已审Pi修正/显式驱动、五项发行依赖和测试EOF格式修正，未带regional。定向57/58通过，唯一安装HTTP测试受loopback EPERM阻止；正常获准后原测试1/1通过4.145秒，未改断言。该精确候选完整Node组合由非Pi作者独立运行中，未预填通过。

下一项真实B2缺口已定位：当前open可保留旧执行intervention且不重派，但没有合法入口让新owner接纳崩溃后原清理证明；即使所属进程已清净，容量和ready仍可能阻断。开始补同一HTTP/SQLite/guard链的dispatch、取消crash组合测试；不以永久intervention宣称完整恢复，不以脚本改库结清，也不再新增Provider来回避该主线问题。

### 22:00 CST 真实必要问答团队交付

候选 sourceHead=`538c53494fdaf0715bd5817914c44ace46771471` 上，固定 Node24.15.0 + 原生 Qwen0.22.3 一次完成日期业务：HTTP 缺起止日期→两项必要问题→答案及完整预览→精确批准→双作者→独立验证→下载消费→正常服务实例重开。Task=`task-5fdad69d-2a5f-466a-8a0d-c8ec6a8193c1`，`13:57:42.906Z`→`13:58:04.346Z`，**21.440秒**，无自动重试。批准前启动0；作者生命周期交叠 **14.828秒**，两原执行 cleanup 已确认；3 Attempts 为两作者和 verifier，固定模板不额外调用模型规划。

公开8行输入的独立下载消费确认 east 3笔/1000 cents、west 2笔/550 cents，共5笔/1550 cents；日期外及 cancelled 行排除，退款负值与零值保留。下载507 bytes，摘要 `sha256:ecb05b7e173e8dc72b4d33a2192ccc8d075d637c5c2e70adcf6a8e14c2cf6dd3`。正常同宿主服务实例重开后原Task、成果及批准回执相同，重复启动0，不是崩溃恢复。证据 `/private/tmp/marshal-window-live.XW4ktd/evidence.json`、交付同目录 `delivery.json`；显式驱动摘要 `1c1542fb6bd05736ee95424a2be944d3811c9fcb26d71dbcc86f700e7d4ef77a` 来自实际执行记录，不是证据JSON内的签名收据。另一次只读计量核验确认两文件的字节摘要/耗时/汇总一致，不冒充重新执行原8行oracle。permission=0/0，不证明权限检查已触发；ordinary-user、production=false、publisherSeparationProven=false。

完整 Node 组合 **347/347 PASS，138.377秒，零跳过**；独立产品审查无剩余 P0/P1。保留失败成本：较早组合343/344通过，发行测试不应无配置导入服务配置入口；修正仍保留全部运行依赖，增加实际安装入口缺配置负例与 checker 执行测试。另一次独立业务包9/10因测试过早读取合法 cancelling 失败；tests-only 修正 `ef97907` 仅等待终态，仍严格断言 failed/验收失败/零成果，10/10通过、原reviewer复核通过，尚未整合，不改写上述实机源码范围。

Pi修正 `a5004a8` 原唯一P1已由原reviewer关闭，真实已安装Pi SDK/Agent Core无模型9/9通过；HTTP团队驱动 `b1d4c4` 已独立审查、18项无模型测试通过。正式Pi模型团队尚未运行；研发分支合并被安全审批拒绝，等待明确范围授权，未换Git操作绕过。

香港ECS已核验用户文档中的root管理通道与runuser。固定Node24.15.0安装于 `/opt/marshal-runtimes/node-v24.15.0-linux-x64/bin/node`，root拥有/0755，独立SHA-256=`d1de76d8edf2fededf6f8b30d244e2c0529ac607923a018283b77e9c74bd932c`；原系统Node24.18.1未改。专用执行用户仅完成无模型版本/SQLite检查，不是完整分权证明。后续25文件服务包及无模型脚本上传被安全审批拒绝：**没有上传、没有新部署目录、没有服务启动**，等待明确授权。

此时main/origin/main仍为已推送`61a19e9`；regional候选pendingRemoteSync=true、localMergeSha尚无，Pi整合亦未完成。B1/B2保持IN_PROGRESS、API-STABLE/B3未完成；还缺权限分离、第二真实Adapter、局部修正、完整同版本恢复及同资产部署/正式发行验证。

**随后授权及执行更新**：用户明确批准研发整合与限定ECS上传。Pi子任务正常审批通过，两个研发merge实际产生`e618814`/`b97aab3`，独立整合分支正在回归；root的regional研发merge仍被审批器按AGENTS“Merge默认禁用”拒绝，未绕过，已提出明确产品/研发作用域的澄清。ECS正常审批亦通过，已创建`/home/marshal-runner/node-package-smoke-a5c6b8f-20260908`（0700、UID/GID1000），但随后scp命令异常exit137；只读复核归档和state均不存在、服务/模型均未启动。具体终止原因未知，未盲目重传。以上覆盖前段“未获授权/没有目录”的历史时点，不表示部署完成。

### 21:21 CST 问答集成与同步

当前产品 main/origin/main=`6a2df5ef4c3fc2952a0975cc34d5d0008bcc9b3f`，已实际正常推送，产品代码 pendingRemoteSync=false；下文早期 pending 仅表示对应历史时点。批准前有限问答已进入正式 HTTP→Application→SQLite：缺失字段一次形成问题批次，答案产生新预览，最终按精确 digest/revision 批准；完整输入继续零问题，不增加模型调用。原答案回执、冷重开、过期/取消/CAS 竞争和旧客户端兼容均有覆盖。仍不是运行中 Worker 待答或任意自然语言澄清能力。

- 问答 sourceHead=`a90e1f4fe8292defe3f4653f8333a9d8848c512a`；集成 sourceHead=`a5c6b8fef0fdba432a4fed2d8e944cc46850eedb`，文件树与 localMergeSha=`6a2df5ef4c3fc2952a0975cc34d5d0008bcc9b3f` 相同。
- 独立审查后，完整 Node 组合 **337/337 PASS，142.343秒，零跳过**；41 个 Draft 2020-12 schema、41 个引用编译及25个示例独立验证通过。不是模型、Go 全仓或 Linux 部署验证。
- 实际发行包包含25文件、386782 bytes，已独立核验，manifest source 保持 `a5c6b8f`；摘要 `sha256:f0b99c8a70739741688c327c4ec22487c5cdff08c09f83f75b7cf2f886e415b5`，本机目录 `/private/tmp/marshal-node-b2-package.ozpAm5/package`。明确包含新增 clarification 运行依赖，不把包内摘要自行当作外部信任。
- 本次首审发现一项集成 P1：AnswerReceipt 缺示例，使既有 TaskClient 消费者测试失败；原123项定向检查未覆盖该消费者。已一次修正示例并增加合同示例检查，同 reviewer 复核关闭，完整337项覆盖客户端；保留成本，不记首审零问题。

**B1 剩余条件已具体化**：已列七项功能退出条件分别有真实团队交付/下载、启动阶段所属取消、正常服务实例重开及确定性负例支撑；当前还缺适用 profile 的 **Worker/Publisher 权限分离证据**。Mac ordinary-user 并不豁免该不变量，因此保持 IN_PROGRESS；不把全部 B2/B3 故障矩阵或第二 Provider 错加为 B1 前置。香港 ECS 本轮专用账号只读直连返回 `Permission denied (publickey)`，远端身份/Node/权限命令未执行，无新部署证据；未重试或切换身份。

当前两条作者线：Pi 原生 RPC 工具权限与所属子进程清理；真实日期区间业务的必要问答→精确预览→双作者→独立结果检查。共享 reviewer 同时核验问答集成和发行依赖。B2 仍缺实际问答业务、第二 Adapter、局部修正及完整同版本恢复；API-STABLE/B3 尚未完成。全程不使用 Marshal skill 或 Marshal 原生可执行文件。

### 21:02 CST 真实取消与安装包增量

正式源码 `0a1deedf704c7ccb6257fcb4389bc0eb44b3b66b` 的显式 `--scenario cancel` 一次实机通过：Task=`task-6e6e93b9-de46-4840-8d9b-165cdcfda6d3`，Qwen0.22.3/Node24.15.0，`13:01:30.489Z`→`13:01:48.720Z`，18.231秒，无自动重试。两个原作者实际启动后于 `13:01:48.257Z` 发HTTP取消，两个原Agent均于 `13:01:48.266Z` 观察退出且cleanup已确认；最终Task取消、取消Operation收口，verifierStarts=0、交付0。正常服务实例重开后原create/approve/cancel回执、完整Task保持，重复启动0。

这是**启动阶段的真实所属进程取消**，不是已证明模型生成或工具执行中取消；两个作者生命周期交叠仅36ms，不用它宣传计算并行收益。它补齐本次PoC的真实活跃取消子证据，不授予权限隔离、完整崩溃恢复或production。脱敏证据 `/private/tmp/marshal-qwen-cancel-20260908-210100/evidence.json`；源码来自实际运行记录，不把JSON当签名发行收据。验收驱动source=`100f61ae1e2a2a83b75ba44405bb6ea5cb225214`、localMergeSha=`0a1deedf704c7ccb6257fcb4389bc0eb44b3b66b`，独立17/17无模型测试包含真实HTTP/SQLite/ACP夹具，2.419秒。

已核验的原目录包（source=`3383e0e`、外置摘要保持下文所列）在本机实际从安装目录启动两个先后独立CLI进程：create→HTTP health/ready→SIGTERM/exit0/clean→open→health/ready→正常退出。状态根 `/private/tmp/marshal-installed-smoke-Cnyp1V/state`，无模型调用、不创建业务Task；不是Linux或安装包内模型交付。该行为已固化为自动回归，10/10发行测试通过33.758秒，独立审查通过，source=`69f0e16c3f9075257e425751af6b42eeb03675da`、localMergeSha=`abbcec4b2fc0b87d6bb372ac615288069798850f`。上述新增合入相对最近已推送 `33929c8` 仍为 pendingRemoteSync=true，后续推送结果另记；B1/B2未因此整体关闭。

### 20:16 CST 真实模型团队突破

已在正式 Node 源码 `42f95658071e9ce03d38d8926b376b26c82ffd50` 完成一次纯HTTP真实Qwen团队验收；该main已实际推送。驱动source=`f9a567c6feeba6bcc046c89e742bfc22ec5c79e1`，localMergeSha=`636c90d8cef0e0f25b9eb0077f7285ddeb4757a4`。下面 `82b64cf` 为其产品运行代码基线；后继仅增加验收驱动与文档。

- Task=`task-46eeda51-779e-41ce-af19-e689ebf6e0db`，2026-09-08 `12:15:40.285Z`→`12:16:12.905Z`，总计 **32.620秒**；本次无自动重试。
- 真实planner提出计划，经一次HTTP批准后两个作者分别生成east/west报告；实际执行重叠 **16.799秒**，各自原进程cleanup已确认。
- 独立检查器执行一次，Core接纳验收及最终交付；HTTP中4个Worker均completed（planner、两个author、verifier），不把模型end_turn当业务通过。
- 下载后独立消费者确认east为2笔/1275 cents、west为2笔/550 cents，共4笔/1825 cents。交付184 bytes，摘要 `sha256:ee6166dd4ce3e6516262414a5e032dd999083ae50b60825e85f4df5482cf56e8`。
- 同一Node宿主内正常shutdown→同版本open，重新创建HTTP/SQLite服务实例和token后，原create/approve回执与完整Task、成果字节不变，重复启动数0；不是宿主进程退出、崩溃恢复或跨版本升级证据。
- 实际安装身份为 **Qwen 0.22.3 / Node24.15.0**，入口摘要 `sha256:68cb29eb7ccc936d78ece5564ef55cae41a55b630e6657dc417c1f2e561cf4c9`。不混用较早0.23.0会话证据，不改原生模型/登录，不启动Marshal原生文件。

本地脱敏证据 `/private/tmp/marshal-qwen-team-20260908-201600/evidence.json`，交付 `/private/tmp/marshal-qwen-team-20260908-201600/regional-report.json`；私有运行库和原始日志不提交。源码身份由维护者实际运行记录关联，证据JSON本身不是含sourceHead/driverDigest的签名发行收据。本次permission回调allowed=0/denied=0，仅说明未收到询问，不能证明权限限制或交互路径生效。**Mac ordinary-user dogfood：production=false，publisherSeparationProven=false**。正式同链真实团队成功这一条件已推进；真实运行中取消、完整B1条件、B2/API-STABLE、权限分离及B3发行仍不自动关闭。

付费调用前修正了一项可避免的返工来源：不再要求planner逐字复述中文验收句，改为核对Core确定性追加的完整policy/description/layout/delivery及实际输入绑定；篡改仍拒绝，业务oracle不变。维护者11项无模型检查通过；同候选14项含HTTP团队夹具通过。首次受限工具沙箱中3项HTTP因服务不能启动失败，获批正常本机权限后同代码通过，不修改断言或隐瞒第一次失败。

当前并行：批准前有限问答与Pi正式RPC候选；Pi原生shell脱离继承进程组的清理边界尚待解决，不因本机已安装而宣称正式支持。以下为本次实机前的集成检查点，保留其时间范围。

以下保留实机前检查点：正式主线是 ADR0088 的 Node-only Task 服务，不再扩充固定订单实验。当时代码集成基线 main=`82b64cfa8d0daef0c188f0e296299b6e3aedcc48`，已包含正式 HTTP、唯一 SQLite、Task 控制/执行 reducer、受管 ACP、Supervisor、输入上传/制品下载、不可变文件物化、通用业务文件适配、独立验收/Decision/最终交付、独立 HTTP 客户端、正式服务入口与目录发行包。当时正式全链 Node 进程夹具已通过、真实模型团队尚未执行；后来的实机结果以上文为准。B1/B2 保持 IN_PROGRESS，B3 保持 PLANNED；实验双 Pi 成功与正式组件证据分开。

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
