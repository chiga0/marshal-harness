# UI 正式发行候选验收记录（2026-09-11）

## 当前结论

**已合并更新**：PR #295 于 2026-09-11 21:37:33（Asia/Shanghai）合并为 `61a0048ee40a24d5b44568312a280e4e24bbc94a`，本地 main 正常快进并与 origin/main 一致。精确源 `9cdfc074` 的 Node run `34604147735` 全13项与安全检查均通过，合并采用精确 head 校验；以下候选在途表述保留历史时点。最终 main 包由 run `34605383372` 生成，本次查询仍在运行，不借用 PR 包或旧 main 包宣布新版验收通过。

**最新后继**：[PR #295](https://github.com/chiga0/marshal-harness/pull/295) 精确源 `9cdfc074b60149c81ad73f2cb878d5d7971155b5` 已完成独立产品/安全审查，无阻塞问题；ACK 源与整合差异的 stable patch-id 均为 `09e5f34de5b21e9028be6f0af8b8689880eb0cc2`。整合全量496项测试、4项护栏及构建通过。远端 Node run `34604147735` 尚在运行，未合并或发行。下列“后继修复/待补”段落是历史过程，最新 ACK、Git、8 MiB 等限定结论见末节，不能把已验证项重新排为完整重跑任务。

后继 [PR #294](https://github.com/chiga0/marshal-harness/pull/294) 已于 2026-09-11 21:06:16（Asia/Shanghai）合并：精确 source `3ed0d3ed992f88583f232acdb798e85186f7e800`，merge/main=`0549aa82efc0858fac0c8c07e887a9c696e9a647`。Node run `34601260031` 的13项全部成功，准入 `34601259985` 双平台成功（PR不执行手动原包接纳job），secret `34601259904` 及外部安全检查成功。已正常快进本地主分支并保留用户未跟踪文件；main 原包生产 run `34602433748` 另行验证，不把 PR 包冒充 main 包，不因代码合并宣称 UI 正式发行。

2026-09-11 20:21:16（Asia/Shanghai），PR #293 的精确 head `93ed5468` 经独立审查和 CI `34597445153` 全13项、准入 `34597445154` 双平台与 secret scan 通过后，实际合并为 main `9852e4da487c0c0f0a36c3ec99de62d5d71b55a9`；本地主分支已正常快进，用户未跟踪文件保留。main push `34598484218` 已13项通过，原始候选包独立安装消费通过，精确绑定见下文；不复用 PR 包冒充该 main 包。本条仅为研发合并，不是 UI 完整验收或软件发行。

后继运行控制补验确认 P2：暂停 Operation 已 succeeded，公开 Task paused 且允许 resume/cancel，但页面 `pendingAction` 仍锁住后续操作，必须先点含义不明确的“关闭”。这不是单纯脚本遗漏；存在可见绕行路径、未证数据/权限损害，但 E12 自然操作仍未通过，后继独立分支修复。unknown/submitting 的冻结锁必须保留，不按202自动解锁。原失败及聚合复验报告 `/private/tmp/ui-controls-fixed.diiOfo/report.md` 保留；E13单Worker取消后兄弟继续和E14真实运行取消的限定子场景已通过，不重复整批。

该控制 P2 的功能修复已整合至 `5c417aa2`，随 [PR #294](https://github.com/chiga0/marshal-harness/pull/294) 推送。独立 reviewer 对源 `53884e40` 发现陈旧 GET 与显示缓存混淆的 P2，由 `569ab716` 集中修复；50项定向及1项独立反例通过，主侧整合479项前端测试、4项护栏及构建通过。独立 Chrome 实操 E12 原 pause/resume Operation 均 succeeded，注入 unknown 时仍禁用，恢复原响应后无需关闭回执即可继续，两原作者恢复完成；Task 后续 verify 失败仍如实显示。报告 `/private/tmp/ui-e12.8o0cz6/report.md`。控制区处于应用滚动容器下方，截图没有完整覆盖双回执，视觉范围仍待补验，DOM/点击不替代截图。

新增 E04 P2：33个 JSON inputRefs 在零 POST 时被误分为未知请求并冻结草稿；原失败 `/private/tmp/ui-create-boundary.BXRPia/followup/report.md` 保留。修复源 `60fef546` 经独立审查、57项定向测试及 Chrome 复验通过，整合为 `13f04cf8`；33引用及32引用加1附件均零POST、本地错误可见、草稿可编辑，原根唯一Task前后完全一致。合法32提交由组件测试覆盖，浏览器未重复创建。报告 `/private/tmp/ui-e04-fixed.3tsf5g/report.md`；真实网络未知冻结及原键重放不放宽。整合后42文件483项前端测试、4项护栏及构建通过，实际 JS `index-BNLSYH6U.js`；截图仅桌面浅色，不替代完整主题/缩放或真人验收。

原 main 包独立消费结果 `/private/tmp/marshal-main-9852.RiUeEa/independent-admission/result.json`：两种布局均通过，每种原执行4次、Attempts4、冷恢复重复启动0、modelCalls0。升级预检另于 `independent-upgrade-preflight.json` 返回 `invalid_manifest`：测试消费者用当前67文件库存校验旧版65文件，运行尚未开始、未创建业务根；四个共同 profile 摘要相同。不把工具兼容缺口当作运行时数据迁移失败，也不声称升级通过。后继仅修测试消费者的固定旧验证器选择，不放宽生产库存验证；此前报告 profile 身份迁移的独立缺口仍保留。

上述升级工具缺口后续修复源 `44913c3a`，整合为 `3ed0d3ed`。仅测试消费者按固定 v1.0.2 source、18240字节及 SHA-256 `60ed1edfb01bd4dee8bc3304e142dc6080c88611362599c260065a51e97d534d` 选择包外旧验证器；验证文件身份与摘要后执行冻结字节，不从待测包导入验证器，生产 verify/库存不变。主侧使用 code-review-helper 独立审查，8项独立定向测试通过（session7896），作者完整发行回归33项通过（session28445）。原旧版签名清单只签 ZIP/manifest，不称 helper 被直接 minisign 签名。

固定旧 v1.0.2 → 原 main `9852e4da` CI 安装资产 → 旧 API-only 同根实测通过：`/private/tmp/marshal-main-9852.RiUeEa/independent-upgrade-validated/evidence.json`，Task `task-9d012e40-0062-44f4-9f96-ba87f6d0ca1b`，原 starts2/Attempts3、升级及回滚新增 starts0、三次退出0/stderr0，原快照完全一致、root dev/ino不变；east2/1200、west1/-50。执行者为测试消费者修复作者，主侧独立检查源码、定向回归与原结果，未独立重跑该固定资产完整链。此限定 regional-window profile、modelCalls0、publication=false、migrationClaim=false；不覆盖报告 profile 迁移，不冒充含后继UI修复的最终签名资产或发行批准。原 BLOCKED 证据不覆盖。

E24/E25 问题／方案／授权页面限定补验 `/private/tmp/ui-question-keyboard.2kJlxA/followup/report.md`：独立冻结 `8598f1f4`、自有 `CByOpyRS` 构建，Chrome152，375px及原生200%（outer1440/inner720/DPR2/CSS zoom1）。真实HTTP/SQLite受控Task，六组合均用Tab发现入口、Enter打开确认框、Escape返回原入口；十二张可见窗口截图前先让实际SECTION滚入目标，无整页横溢。仅业务答复及方案批准各202，发布授权仅打开退出、目标目录为空，服务与两浏览器均退出。浅色Leader自由文本范围，不覆盖经典选项、深色、Shift+Tab/首尾环绕、实际Safari或从启动起完整纯键盘；原0755测试目录启动错误在Task前发生、修0700后同根继续，FAIL保留，不记产品缺陷。

只读异常聚合 `/private/tmp/ui-read-anomaly.ZsfpWv/report.md`：独立冻结 `13f04cf8`／`BNLSYH6U`，原 completed Task 的61版本响应未覆盖已见62及正文；原782字节制品等长翻转一字节被拒存，串Task metadata被拒展示和读取内容；375px深色错误换行，无整页横溢。断开后排空再观察2.3秒无新增请求，重连原成果恢复。157个浏览器API请求、POST0、download0，原Task/Workers/Leader/audit完全不变，所属服务退出0。主侧另直接查看错误摘要375深色截图，制品表有局部横向滚动，不声称所有列同时可见。切换另一独立页面时原页仍visible，没有真正触发隐藏生命周期，因此隐藏退避未通过也未证明产品失败；8MiB及全部分页竞态未在本批覆盖。

组合性能 `/private/tmp/ui-combined-perf.5ps0VT/report.md`：冻结 `8598f1f4`／`CByOpyRS` 的单一受控服务正式创建100Task，仅批准1个产生实际事件，无真实模型或外部发布。首轮定位断言失败保留；“卡片链接名包含状态”的具体归因经查源码撤回，原错误缺栈，异步路由即时count仅为候选原因。同根后继只读复验先按实际href等待路由，载入100Task后在同会话进入原详情，实际显示500条事件（公开545条），真实SECTION预检及16次滚轮有位移、长任务0；不称两页面同时在DOM。冷导航至连接字段可填76.4ms、连接至首批可用112.6ms、卡片状态反馈1.4ms均为原有限测量，不冒充完整冷TTI/绘制结束。原Task均终态，未取消或新建替代，新事实≤3秒仍未测。所属服务和浏览器关闭，原FAIL不覆盖。

E09/E11/E12 部分证据 `/private/tmp/ui-active.ieXUTd/report.md`：冻结 `13f04cf8`／`BNLSYH6U`。east等待问题、west完成有公开事实，概览问题可见但兄弟区未展开，不能称完整双分支视觉；串另一真实Task的questionDigest由服务409拒绝，前后两Task/问题/Workers/Events严格不变。原pause/resume均succeeded且二次确认截图取得，但双回执截图未取得。脚本重复把resume后的状态假定为running，实际既有成功脚本与Core均恢复awaiting-answer，超时属于已知前提未复用，原FAIL保留；后续仅补未发生的E10，不重造已有通过子场景。

E10 单Task限定补验 `/private/tmp/ui-active-fixed.j4F9Ru/report.md`：冻结 `13f04cf8`／`BNLSYH6U`，原45秒Task/30秒问题预算，私有driver仅在原业务ACK请求前加入最长20秒gate，放行后沿原Pi adapter→Runtime/Core→Store路径确认，未自行写ACK或伪造浏览器响应。浏览器实际看到202受理、dispatched未消费；原问题随后acknowledged，活动页自动出现对应event-37。公开ACK读数到DOM1786.70ms，gate到DOM2044.77ms为本次真实ACK持久提交到显示的保守上界，小于3秒；不覆盖100Task/500事件负载，也不是包含gate等待的用户答复POST到显示时间。主侧另直接查看ACK事件截图。概览ACK后问题消失、活动只有英文原事件名，另有过时“此视图不显示ACK”文案，确认中文消费状态可追溯性的P2，正在修复。所属服务退出0；不把受控Task终态当业务成果消费通过。

受支持Git patch样例 `/private/tmp/ui-git-patch.NTj21t/report.md`：冻结 `13f04cf8`／`BNLSYH6U`，原受控Pi桥/ACP、真实HTTP/SQLite、自有两个各2文件仓库，未扩大Git支持面或调用模型。Task `task-f0df4afe-0ef4-472e-9290-ec07889f3671` 由浏览器批准202后completed/revision19，公开4Workers/4Attempts均完成。实际下载 `git-mixed-patches.json` 3586字节，SHA-256 `6cee4892b6c5444e2c6b387f0b8c354994584116058ad6da7e7ae3872fb58eaa` 与metadata一致；原consumer在第三组独立worktree应用浏览器下载包并通过23检查（含12负例），desk900/lamp450/total1350，两原仓HEAD/工作树不变、无remote，Task下载前后不变。交付是双patch与上下文的JSON包，不是独立.patch文件，不是远端发布。团队截图仍为中间帧，完整终态来自API，下载截图显示完成及文件保存；不外推完整视觉矩阵。脚本启动前独立预检纠正接口/定位/等待与非空完整性断言，唯一实际运行退出0，服务/浏览器/consumer均关闭。

**尚不可声明完整验收通过，尚未发布新版。** 本记录补充三条验收，不以组件测试替代实际交付、视觉操作或目标用户测试。

| 验收线 | 当前状态 | 证据与缺口 |
| --- | --- | --- |
| 功能与可靠性 | PARTIAL | 第三轮真实 Pi 团队完成独立审查、验收、授权发布和后验；原输入/成果/发布回执/后验证据四项浏览器下载复验通过。整合 `13f04cf8` 的 UI 483 项及 4 项测试护栏通过；`9852e4da` 原 CI 包独立安装消费及同根旧→新→旧验证通过，后继精确资产仍须验证。取消/故障全范围与持续稳定性仍待补齐。 |
| 视觉与交互 | PARTIAL | 已实测创建、问答、批准、DAG/团队、桌面/375px、浅深主题、Chrome 原生 200% 缩放、抽屉键盘及受控异常回执。成果列宽/错误换行已修复复验；完整必需矩阵与实际 Safari 仍未全部完成，具体版本和限定范围见下文。 |
| 产品可用性 | NOT_RUN（真人）；无指导 Agent 模拟已完成 | 早期只有参与开发者的截图观察；后续新增未继承开发历史的独立 Agent，完成无点击指导的限定只读任务并发现追溯缺口，修复后原业务四下载已独立复验。模拟不替代真人，也未覆盖全部新建与授权用户路径。 |

候选锁定起点：`976ddc00c74018601a1b8e14c52d7a532f7c029d`。当前已发布安装器版本仍为 `v1.0.2`；研发候选合入或构建成功不等于安装发行资产已更新。

最新增量：`9e5e49e0` 已关闭上述答复迟到 P1，原 reviewer 复审无新增 P0/P1、39 项定向通过；主侧整合构建及 41 文件 425 项测试通过，JS `index-BESzNDyL.js`。第三轮真实团队交付已完成；`a2171284` 增加精确 13 CI jobs 与摘要锁定 UI ZIP 接纳，本地发行接纳 30 项通过。当前 PARTIAL 不变，仍须浏览器全矩阵、同包发行和升级出口。

证据版本分别记录，不能混用：第一轮实机与 `intake.png`、`team-review.png`、`failed-team.png` 使用基线 `976ddc00`；第二轮使用代码等价于 `c41e8732` 的冻结构建（CSS `index-DnldHR2s.css`、JS `index-CskjiGYd.js`），包含信封示例和主要 UI 修复，但不含后续 Operation 展示与观察 fallback。`candidate-intake.png` 属于第二轮。后续候选均须重新绑定构建验收，不能复用为通过证明。

## 真实业务链：第一轮

- Task：`task-52f54383-5c82-4167-b503-7dc73346d06e`。
- 创建时间：2026-09-11 16:40:21（Asia/Shanghai）。
- 环境：固定 Node 24.15.0、真实 Pi Provider、报告服务本地回环地址；隔离的临时测试输入及本地报告目标，不涉及生产业务资源。
- 输入：五条测试流水，覆盖 paid、cancelled、负数退款、零金额、日期窗口外数据。窗口为 2026-09-01 至 2026-09-02，起止包含。
- 独立预期：east `count=2, netCents=1200`；west `count=1, netCents=-50`。
- 真实页面操作：上传文件并创建 Task → 回答 Leader 日期问题 → 查看并批准 east/west 两作者与独立 verify 计划 → 查看团队与图状态。
- 已发生：两作者产生结果，Review `accept`，独立 acceptance `passed`。
- 未发生：publication 授权请求、报告发布及交付后验证。
- 最终状态：`failed`，revision `47`，`invalid_leader_decision`，event `102`，16:47:36。不得用局部验收通过冒充整个 Task 成功。

终端诊断确认模型正常结束，但返回对象顶层为 `deliver`，其内部才是完整决定信封。严格解析器在 parse 阶段拒绝。提示词原先将完整示例放在动作名键下，可能诱导模型照抄包装；这是从代码与失败形状作出的根因推断，不是模型内部意图证据。

修复将示例改为中文标题下各自独立的完整 JSON 信封，并明确顶层五字段。保持原解析器、授权、预算及生命周期不变，不自动去包装、不复活失败 Task。确定性回归已覆盖错误包装拒绝与合法信封继续原交付链；仍需新候选实机验证。

## 真实业务链：第二轮

已确认第一轮服务正常关闭（`clean=true`），从原数据根 `open` 重启；旧 Task 仍为原失败状态，未丢记录、未自动重复执行。浏览器明确重新连接新会话后创建独立 Task `task-9013bc63-8a31-443c-9e97-7ae70ce93e36`，不复活第一轮。

第二轮创建时间 16:58:25，使用相同输入和业务日期。真实 Leader 再次提问并收到答复，但 16:59:23 转为 `failed`，revision `13`，代码 `leader_result_rejected`。尚无接受的计划、作者交付或发布。

正常停服后独立只读核对：失败 Worker 的 inputRef、扩展 ticket 摘要、候选 ticketDigest/callId/inputDigest 全部绑定，候选摘要为 `sha256:a24b3ce9fad08e6446a4ab87438f19d7549e372ba303a8decd1dd7069a52f293`。计划额外增加 `review` 节点，形成 4 节点/5 条边；本报告 profile 只接受 east/west/verify 的 3 节点/2 条边，首个拒绝条件是 `report_plan_boundary`。`verify.providerId=null` 正确，排除了 Provider 指定错误假说。另把尝试预算 17 自减为 10，即便修正图结构仍不足原完整链最低容量。

该 profile 的独立 Review 是 Core 受管阶段，不应由 Leader 重复放入业务 DAG。提示需明确这些已存在的边界及省略预算沿用原 limits；不修改通用 DAG 合同、不自动删节点或扩大预算。原错误仅存在 `Error.message`，通用捕获只读取 `.code`，因此公开结果折叠为 `leader_result_rejected`；后续诊断改进应只允许已知 reasonCode，不能泄漏任意异常正文。

已整合最小提示修复 `acd49fae`：明确三个节点、两条依赖、Core 受管 Review 及默认沿用预算。主侧独立运行 `leader-prompt.test.mjs` 与报告 `index.test.mjs`，23 项全部通过；其中真实 Verification.bind 对 4/5 拓扑继续拒绝、3/2 通过，原 Core 对预算 10 继续拒绝。未放宽 parser、业务门禁或自动修改模型计划，当时第三轮真实模型尚未运行（后续结果见第三轮）。

独立消费第一轮交付候选也已完成：其两个 Task 成果引用实际是 verification evidence 与合并的 `regional-window.json`，不是两个独立发布文件。按原流水重算两地区报告、input/sourceDigest/evidence/delivery 的摘要和字节数全部一致；本地发布目录仍空，publication/postverify 为 null。这证明部分结果正确，不证明完整交付。

原始运行证据保留在本机私有临时目录，不入发行包或业务提交。不得把连接令牌、原始诊断输出或编码后的完整模型输出粘贴到公开记录。

### 恢复与升级边界的新增核对

`8ff4c386` 使用原数据根 `open` 失败。独立只读比对确认：报告 `index.mjs` 将 `policy.mjs` 全文摘要纳入 review/Leader 策略，因此本次 GUIDANCE 修订使配置身份变化，持久 `profile.json` 与新配置字节不一致，原门禁拒绝打开。这不是端口或 UI dist 缺失；禁止改写 profile、清库或放宽摘要校验绕过。旧现场保留，新版新根验证不代表跨版本迁移或回滚通过，该兼容性限制仍须在发行处置中明确。

新私有数据根已用固定 Node 与候选 UI 成功启动，真实浏览器完成连接、空任务列表、设置导航及浅/深主题切换（1280×720，JS `index-CLhrYL2x.js`）；尚未创建第三轮模型 Task，不能把空态检查代替有数据矩阵。设置页发现旧“不能识别未决请求”措辞，已改为当前连接提示、刷新/断开丢内存的精确边界，经独立审查无阻塞及三项设置测试通过，后继构建须再验。

主侧独立执行报告安装态测试通过（31.0 秒）：受控 Provider、两个原 HTTP Task 各 12 Attempts、实际发布 GET 与同版冷恢复，重复启动/发布均为 0、modelCalls=0。测试自建包 sourceHead=`b54ef46922f757136132d7df0e752191667dd21e`，manifest=`sha256:e450d61be6679bb3e3434b52b54627435c91c98ba7ef1999bdbd1851cf20647c`；不是最终 GitHub 发行资产，不能替代真实模型或 UI 同包验收。

## 本轮修复与审计发现

1. 活动 API 按 sequence 升序读取，旧 UI 把首页当最新页；超过 50 条事件后遗漏后续进展。改为正向追赶、尾页继续读取与有界去重。
2. 取消遇到 revision 冲突后缺少重新核对入口；确认期间轮询可替换 CAS。冻结确认版本，明确拒绝可重新核对，未知结果只原键重放。
3. Leader 答复/授权确认期间请求发生变化时关闭原确认，要求重新核对，不能确认替换后的目标。
4. SPA 切页会卸载未决动作。增加连接内存作用域、稳定动作身份、顶部未决提示与显式原键重放；断开清理，不增加持久化或自动重试。
5. 新建任务另有附件上传链；须同时保留原草稿与上传会话，并在连接消失后阻止后续上传/创建。只禁止旧回调更新 UI 不足以防止跨连接写入，独立审查发现后已补逐次请求前检查；单/双附件及 FileReader 等待中切换连接的独立反例已通过。
6. Worker 抽屉路由卸载列表导致键盘焦点丢失；深链成员不在当前页时缺少解释。保持列表实例并提供明确刷新/返回入口。
7. 待批准计划应置顶，但不能通过改变父节点卸载原动作。固定父节点，非待批准状态折叠而不丢回执实例；待答问题按期限排序。
8. 对话框长内容和窄屏按钮溢出、选择按钮错误声明 radiogroup 已修复，真实浏览器矩阵待补。
9. 原始 JSON、摘要和协议名压过业务内容。计划改为目标/角色/范围/预算/验收优先，完整技术原文可展开；不能丢弃授权范围和审计事实。
10. 202 Operation 回执需可追踪到终态，不能仅显示“已受理”或依赖 Task 轮询。已整合原 ID 查询、终态显示与本连接最近 20 条活动回执；独立审查补测 unknown 轮询、终态停止和淘汰缓存通过，仍在补完整合同反例。
11. 成果元数据读取增加原 Artifact ID 与 Task 双重绑定，错误归属不展示或下载；下载前核对清单上限、读后核对实际 8 MiB 上限及原 SHA-256。原 HTTP handler 与 UI DOM fixture 九场景通过，包括错绑、404、摘要错误及大小边界。读后限制保证拒存，不意味着下载网络/内存流式有界。独立审查无 P0/P1。
12. 答复受理提示修复初版的 422 项组合回归通过，但独立审查另发现同 request ID 在 flight 期间仅更换摘要时，旧成功显示可能遮住新问题。`9e5e49e0` 补齐通用动作 fallback 反例并关闭该 P1：仅明确 accepted 且绑定陈旧时重置；unknown/submitting 仍冻结原请求，不误重放到新请求。

## 真实业务链：第三轮已完成

- Task：`task-04ba8301-3721-43c5-872a-fb1b73a63d97`，新私有数据根、相同原始输入，运行代码等价于 `9e5e49e0`，后续提交仅改变文档/发行接纳，不改变该运行文件集。
- 从真实 UI 上传、创建、回答日期、读取完整计划并批准，随后明确批准限定本地报告目标的 `create-if-absent` 发布；没有通过隐藏 API 代写这些操作。
- 生命周期 2026-09-11 17:49:31.859 至 17:52:47.042（Asia/Shanghai），`completed/terminal`、revision `62`，耗时 `195183ms`（含用户确认等待）。
- east/west 两个真实 Pi 作者分别在 17:50:51.234 与 17:50:51.437 启动；east 于 17:51:03.781 完成，二者存在真实执行重叠。
- 独立 Reviewer `worker-8200978f-43de-4137-88a3-017c30b4c005` 返回 `accept`，reviewDigest=`sha256:65b35cabb7dff9639a5999a6bdbe2d4d6420d20189ba560d75fb3fe34bd88706`；独立 acceptance 为 `passed`。
- 最终成果 `artifact-e1efb904-17c7-4ba3-acbb-65b2c8ff2fef`，`regional-window.json`，382 字节，SHA-256=`b652504bda6eda554da2d00a3f1d66b3fbc87512de3236b4dbfb5baa906d2534`。
- publication 与 postverify 均 `succeeded`，回执分别为 `artifact-e347485c-4c45-4a45-bb38-ddcdd3970e94`、`artifact-1a515ac0-1304-409f-b3d1-76c1fd6340fd`。这是隔离本地目标的业务发布，不是 Marshal 软件发行。
- 独立观察 Agent 单次 HTTP GET 返回 200，核对 382 字节、SHA、原输入 sourceDigest 以及 east 2/1200、west 1/-50 一致；零金额和退款保留，取消和窗口外记录排除。浏览器实际触发下载并显示 SHA 复验通过，但未另行查找浏览器落盘文件，不冒充该层验证。
- 后续独立 Agent 已补浏览器落盘消费：Chrome 152、`index-CpLIqAu5.js`，在成果页点击下载，捕获真实 download 事件并保存到新私有目录；`download.failure=null`，落盘 382 字节及 SHA 与上述最终成果一致，JSON 的日期窗口、east 2/1200、west 1/-50 全部核对通过。证据 `/private/tmp/marshal-ui-download.NKPC5b/evidence.json`、原文件 `regional-window.json` 和 `download-ui.png`；不是用 API GET 代替 UI 下载。此项覆盖原真实任务成果，不等同最终发行包消费者。
- Audit 返回 12 Attempts、retryCount=0、reworkCount=0，12 Workers 全部 completed；这是该任务内统计，不隐去前两轮失败及后续修复。tokens/cost/coverage 分别为 null/null/0，不把未知计为零消耗。`firstReviewSource=unavailable`，不能从该字段声称首审通过率；本轮 accept 来自 Leader 的精确 Review 证据。
- 当前截图 `third-reply.png`、`third-dag.png`、`third-publication-request.png`、`third-delivery.png` 保留于私有验收目录，绑定 JS `index-BESzNDyL.js`。已操作移动导航，但 375 截图尺寸与渲染比例仍需重新核对，不记为窄屏视觉通过；键盘和 200% 缩放尚待完成。
- 同版本正常恢复已实测：确认 12 Workers 全终态后，对本轮所属服务执行 TERM，得到 `closed/clean=true`；同一 root、配置和 UI 以 `open` 重启。独立 Agent 经新连接执行 5 个只读 GET，Task revision/updatedAt、12 Attempts、全部 Worker、原 publication/postverify action 与回执、最终 Artifact 及报告字节/SHA 均保持。未发现新增执行或替换回执；仅覆盖正常终态重启，不证明崩溃中途恢复。

前两轮的失败和旧根完整保留。第三轮成功只关闭这个真实场景的交付链，不说明任意业务、配置升级、故障中途恢复或完整 UI 可用性均通过。

CI `34583578954` 的 Ubuntu/Node22 密度测试超过默认 5 秒（5119ms），未出现断言失败；macOS/Node22 与 Ubuntu/Node24 同测试通过。`8ff4c386` 只将这一个容量用例设为 15 秒有界时限，保留全部断言；UI job 名补 OS 消歧。独立核对当前无旧名称 required-check 绑定，后续 CI 结果仍须按精确 head 检查，不能用跨平台成功覆盖失败。

## 模拟观察意见

### 剩余验收按用户路径聚合

独立核对 `93ed5468` 后，后续不逐个场景重复已有测试，而按以下批次补齐：

1. 运行控制/问答：E09 单分支等待、E10 延迟 ACK 中间展示、E12 暂停恢复、E13 单 Worker 取消及旧 profile 501、E14 运行中取消/完成竞态；复用既有受控真实 HTTP fixture，不必付费等待模型产生竞态。
2. 浏览器边界/会话安全：E04/E05 草稿边界与创建丢响应，E11 到期/串绑，E21 乱序/隐藏页/重连，E27/E28 会话隔离与恶意内容；服务和组件反例已有，缺的是实际浏览器行为，不能重复后端测试冒充补齐。
3. 最终原始 CI 包的安装/旧 API/回滚、实际 Safari 与未参与开发的目标用户验收。由主侧负责最终资产，真人缺口仍需用户或代表参与，不用 Agent 模拟替代。
4. 把剩余问答/批准/运行中页面的尺寸/主题/纯键盘检查并入上面操作；另补 500 条已载入事件的浏览器性能。100 Task 分页/筛选已有，600→500 的 jsdom 留存测试不能代替浏览器性能。

E18 历史拒绝已证明安全事实，不把它重新列为完全未测。后续补验见下文；E19 发布后验失败及刷新不重复也已有限定证据。上述是待验排期，不表示已发现同等数量的产品缺陷，不撤销已发布 API/B3。

后续新增一位未继承开发历史、未参与 UI 实现的 Agent，约三分钟完成限定只读目标：从列表找到已完成任务，理解日期/两作者及校验分工，区分 review、acceptance、发布和后验状态，下载最终 JSON/验收 JSON，并从设置返回原成果页。未读源码、API 或 SQLite，也未收到点击指导；两次各约 30 秒的等待来自把团队误当按钮、假定设置页仍有工作台任务链接，不能归为业务失败。证据 `/private/tmp/marshal-observer-enS74O/observation.md` 及同目录页面截图/下载文件。这是按用户选择执行的无点击指导 Agent 模拟，仍不替代真人测试。

该观察者仍难理解多个 managed-leader ID 与尝试编号，且在页面未找到原输入或实际发布目标回执，不能自行重算或验证目标物理落盘。上述为后续核对的可用性线索，不从一次未找到推断服务丢数据；之前独立原输入/实际目标核验事实继续保留。

后续只读诊断已确认成果追溯遗漏，列为完整 UI 验收 P1（非数据丢失）：实际公开 API 有 `sales.json` 347 字节（input、taskId=null，当前 Task 审计存在 contextRefs），以及同 Task 的 publication.json 796 字节、publication-postverify.json 1088 字节；独立读取三项内容并核对 SHA 一致。UI 仅聚合 Task.artifactIds，未展示审计输入关联，Leader 回执 ID 也未成为下载入口。正在统一修复：同 Task 回执沿用原归属门禁；输入只从当前 Task 的合法审计关联独立展示，不放宽任意 ID 访问；无观测改为“关联未提供”。实际目标绝对路径不是通用合同字段，不增加任意宿主文件读取。该 P1 关闭前不得宣布完整 UI 可用或发版。

后继已将完整修复 `feed3243` 独立审查后整合为 `d9989547`，review 无 P0/P1、独立成果测试 60/60；作者全 UI 464 项通过。主侧在该整合候选构建后，以 Chrome 152 实际下载受控输入 73 字节、发布回执 782 字节和后验证据 1074 字节，均与公开元数据大小/摘要一致，下载前后 Task 不变，所属服务退出 0。证据 `.marshal/evidence/traceability-chromium-7QdKnd/evidence.json`，HTML 摘要 `2c6f1b677e69244f82d242abcdf861da1408f063455cf62e441c1a6ce00aed1e`、JS `index-Bvyy6j-n.js`。主侧第一次误用浏览器环境变量导致浏览器未找到，原 FAIL 保留于 `traceability-chromium-wDbMID`；改用脚本声明的 `BROWSER_EXECUTABLE` 后在新隔离目录测试，未重试真实业务效果。此为独立受控下载通过，原 sales Task 在旧服务故障后尚未重开复验，最终安装包与完整三线仍待验；不从代码修复直接关闭完整产品出口。

参与过 UI 实现的 Agent 在截图观察中认为业务问题、答案与提交按钮的排列可发现，但手写 JSON 日期负担高，技术标识与“投影/合同档案”等术语增加理解成本。这不是独立用户测试。通用 UI 不应为单个报告场景硬编码日期字段；后续应结合已有输入合同评估通用结构化输入，不能从任意模型文本猜测并执行新协议。

追加观察曾发现候选页同时出现“答复已受理”和“等待你的答复／需要你的处理”，用户难判断该等待还是重复提交。此项及迟到响应反例已由 `9e5e49e0` 修复，第三轮真实答复截图显示一致等待状态。另外，失败页的多个历史“规划”成员与 `agent.running` 观察摘要易混淆当前工作，且仅凭成员完成无法判断结果是否已交付。后续验收须要求观察者明确指出当前责任人、失败影响，并区分产生文件、独立验收和实际交付。

## 数据密度与有界性补充

独立 Agent 后续用真实 Chrome 152、HTTP/SQLite 与专用受控 Provider 完成 100 Task 密度检查，未启动模型，未修改原真实业务服务。构建 JS 为 `index-CpLIqAu5.js`，逐资产清单摘要 `67942c314fa92181cedf44d278b49d9d4c05769de15f6744c3cc012fa15e463b`。首页 24 项，四次分页到 100 个唯一任务，分页均 HTTP 200；列表/卡片切换、已加载范围内搜索 50 项、清空恢复、末项滚动与进入精确 Task 路由通过。证据 `/private/tmp/marshal-density.6J1nP1/evidence.json`，所属浏览器和服务已关闭、服务退出 0。单轮首批 94ms、分页 60–64ms 只含该本机自动化样本，不是 SLO；100 个短标题、同质 fixture 失败状态不替代真实业务、多状态、长标题或持续事件压力验收。

候选 `b887cecd` 增加真实 React 组件的隔离 fixture 回归：100 条任务分页、卡片切换与筛选；事件持续读到 600 条，DOM 保留最近 500 条，后继合并输入不超过 550 条、两页。独立代码审查无阻塞问题。子 Agent 四轮 jsdom 观测中，100 任务载入为 141–145ms，500 事件载入为 332–364ms；这些只包含隔离组件执行，不含真实网络、浏览器布局/绘制或模型耗时，不作为性能 SLO 或视觉通过证据。

## 发布前仍须完成

最新整合：`ee4b03a5` 修复成果页短状态/下载按钮竖排、成功提示挤压相邻列；独立在同一已完成受控 Task 上冷启动复验 375/1024 浅深四组合，局部横滚和 Tab/Enter 三类下载可达，Task 不变。证据 `/private/tmp/ui-fixed.Kr2Yg7/report.md` 绑定 JS `index-B8fsO8o0.js`；代价是局部表宽增加，未造成整页横滚。最后普通错误包装 `min-w-0 break-all` 单独复验：375 深色、合同允许的 503 与 256 字节无空格 message 注入，展开详情后错误子元素均未超出操作单元格，Task 不变、服务退出 0。证据 `/private/tmp/ui-error.wNkVNI/report.md` 绑定最终 JS `index-CZg1Rmd8.js`、HTML `3b3b2c93476fa674b21fea1463b1fd5ac3fa790f87a2fba921202c2397899aa2`；不是自然服务错误或真人验收。原滚动未复位导致的测试 FAIL 与原视觉发现保留，不把修后结果覆盖旧记录。

服务外层滞留修复已独立审查整合为 `db8aa08c`，主侧 18 项 CLI/关闭定向回归通过。它证明受控内部故障后所属监听和进程有界退出、数据保留；不代替原事故根因和长期运行验证。完整三线仍 PARTIAL/PARTIAL/NOT_RUN（真人），当前不能因两个修复已整合便直接发版。

最新长期服务观察出现阻塞：原真实任务服务的静态页仍可加载，但 API 503，原所属 PID 仅监听外层 UI 端口。主侧保留截图并正常停止该自有服务，得到 `closed/clean=true/service_supervisor_failed`、退出码 1，未重启或改数据。原交付、正常重启及下载证明不被抹除，但不能据它们证明长期运行无故障。独立诊断进行中，详见 [审计 OPEN 项](../audit-report.md#2026-09-11ui-外层仍存活内部服务已失败open)。该状态也解释本次按钮切换检查为何未进入列表；未将其计为切换通过或按钮缺陷。

### Chrome 响应式增量（运行文件集 `9e5e49e0`）

在真实服务原第三轮任务上补测，不新增模拟任务：Chrome 375×812 的列表与详情、1440×900 的十二成员团队表格、1024×768 的设置深色页，浏览器读取的 `innerWidth` 与 `documentElement.scrollWidth` 分别相等，所测页面无整页横滚。截图为 `chrome-list-375.png`、`chrome-detail-375.png`、`chrome-team-1440.png`、`chrome-settings-dark-1024.png`，保留于同一私有验收目录；已实际点击列表/详情/团队/设置并切换主题，临时 viewport 已恢复。此前 IAB 375 尺寸证据不用于此结论。

这只补齐列出的页面/主题/尺寸组合，不能代表 E24 全矩阵。自动化 locator 的 Enter 仅聚焦而未导航；鼠标对照成功，原生键盘注入接口受限，因此键盘验收仍待验证，暂不将原因归于产品，也不记 PASS。200% 浏览器缩放、WebKit 与实际 Safari 仍待补齐。

后继 `e21353aa` 关闭终态 Worker 历史观察文案 P2：保留原 `agent.running`，明确“执行已结束；以下为历史观察”，不改状态或 Provider 事实。独立 reviewer 和主侧分别跑 19 项定向测试通过，主侧构建通过（JS `index-CpLIqAu5.js`）。真实服务正常关闭后加载新 UI，Chrome 实际重新连接、从设置返回原团队页，在 1440×900 深色视图复验新文案，截图 `chrome-team-history-e21353aa.png`；临时 viewport 已恢复。此证据只覆盖新文案的团队展示，不冒充抽屉、全部键盘或其他主题/尺寸复验。

主侧全量 UI 回归随后为 41 文件 431 项通过（4.70 秒）。另使用已安装 Chrome `152.0.7977.84` 的独立 headless 进程，通过 Playwright 原生键盘事件验证真实终态任务：任务链接 Enter、团队导航 Enter、成员抽屉内各一步 Tab/Shift+Tab、Escape 关闭后焦点返回原明细入口均通过。页面实际 script src 为 `/ui/assets/index-CpLIqAu5.js`；证据 `keyboard-team-e21353aa.png`。独立 reviewer 审核了脚本断言范围，未亲自执行浏览器。入口由程序先聚焦，尚不证明仅 Tab 可发现入口或首尾焦点环绕；不覆盖问答、批准、嵌套确认或 WebKit/Safari。此前 locator Enter 未导航不能据此判定产品键盘缺陷。

早期 200% 浏览器缩放尝试未成功：独立 headless Chrome 内发送五次 Meta+= 后宽度/DPR 未变化（1440/1），不把该操作或 viewport 缩小冒充浏览器缩放通过。后续有效结果如下。

独立 Agent 在私有 Chrome 152 profile 使用原生外观设置 `zoomLevel=2` 完成 200% 实测，加载 `index-CpLIqAu5.js`。窗口固定 1440×1000，页面 innerWidth=720、DPR=2、visualViewport.scale=1、CSS zoom=1，不是 CSS/viewport/设备缩放模拟。列表、概览、团队、抽屉、设置浅深主题十处 document/body 宽度均 720，无页面级横溢；团队表格局部 670/720 横滚，明细入口经聚焦滚入后 Enter 可达，单控件双向环绕、Escape 焦点归还及设置返回原团队通过。十张截图使用原始 CDP 截图避免默认截图裁切，证据 `/private/tmp/marshal-ui-zoom200.2N5VfD/evidence.json`。主侧读取测量并查看深色团队截图；长标题需要纵向滚动，内部内容未全部逐屏核验，不外推问答批准、纯 Tab 发现性或完整 E24/E25。所属浏览器已关闭，真实 Safari 仍待验。

### 独立浏览器与跨版消费增量（2026-09-11）

独立 Agent 实际操作 Chrome `152.0.7977.84`，加载 `/ui/assets/index-CpLIqAu5.js`：终态成员抽屉 Tab/Shift+Tab 首尾环绕、Escape 归还原入口焦点通过；此抽屉仅有一个可聚焦控件，不外推多控件环绕。375×812 导航打开、Escape/关闭按钮关闭、设置返回精确原团队 URL 通过。证据位于 `/private/tmp/marshal-ui-independent-browser.V5T5ax/evidence.json` 及同目录四张截图。这是有指导的独立 Agent 测试，不是真人或无指导测试。

主侧另实际执行 WebKit `26.5` 的任务链接 Enter、团队链接 Enter、成员抽屉各一步 Tab/Shift+Tab、Escape 归还焦点，均通过；实际 script src 同为 `index-CpLIqAu5.js`，截图 `/private/tmp/marshal-ui-final.MUfhL2/webkit-keyboard-team-e21353aa.png`。初始入口由程序聚焦，不证明纯 Tab 可发现性；WebKit 不替代真实 Safari，且不覆盖问答/批准与缩放。

独立 Agent 再补 WebKit 26.5 的 375×812 列表/详情概览/设置浅深主题六张截图，document/body 宽度均为 375，无页面级横溢。证据 `/private/tmp/marshal-ui-webkit.Nutphr/evidence.json`；主侧另查看浅深详情截图确认主题实际变化。详情有 291/560 像素的局部横滚容器，未逐屏核验内部内容，不据此声称完整响应式矩阵通过。长需求作为标题在窄屏占据较大首屏面积，列为后续产品可用性观察点，不从“无横溢”推导布局已最优。

主侧截图初见团队内容但概览仍有蓝线；独立查码确认概览已有精确路由匹配。随后等待导航颜色动画结束再取证，只有团队 `aria-current=page` 且边框为蓝色，其余四项无 current 且透明。未复现稳定状态双激活，不进行无依据的路由修改；截图需等过渡完成，避免把中间帧误判为缺陷。

新增双安装包消费器已独立审查并整合于 `374ca333`，分页空页上限补丁 `50f4bc51` 同样通过独立审查：最多 100 页，禁止唯一 cursor 空页无限循环。实测旧 OSS 签名 v1.0.2 → 本地固定候选 → 旧版，在同一数据根完成：

- 旧 sourceHead=`f9a93cd678cac40bcd04ff9d0c1672612f184701`，manifest=`sha256:94b2a036ea4b61e869ee0fd02e9b5a257db88b820c8677f5f09573c7320ee8d3`。
- 新 sourceHead=`1f455e35933c8a7ce1a5b25c3c6bc2ea2098d1cc`，manifest=`sha256:abf1f3d07b7708a6027c4cc828acdd55a57dddb09fc680e4c99bec9b808e7fc6`。这是本地未签名、非 CI 发行包，不是最终发布资产。
- Task=`task-c4fa72e8-5a0a-490b-8814-180bb7d3bca6`；旧版 API-only 完成任务，升级包提供 UI，回滚后恢复 API-only。三次退出均 code=0、signal=null、stderrBytes=0。
- 原 Agent 启动 2 次、Attempts=3；升级及回滚新增启动 0。三阶段完整快照 SHA-256 均为 `b655ef329cf8c964653e62a9a1706441c18e5787b0039604338a39ba8c1600d1`，由独立 reviewer 另行核对。
- 结果 476 字节，SHA-256=`baa6814d88c7b7fa1dd6b93ee9737cae53a0056f94c0e908b4a0eef2c5e89840`，east 2/1200、west 1/-50。证据 `/private/tmp/marshal-ui-upgrade-assets.4Xopg0/upgrade-v102-to-local-1f455e35/evidence.json`。

该测试使用受控 ACP 与原固定 checker，modelCalls=0、publication=false，仅证明配置身份不变的 regional-window profile 同根升级/回滚。它不解决前述报告 GUIDANCE 改动导致的配置身份迁移，不代表真实模型跨版恢复，也不代替最终签名 CI 包消费。UI 三线整体结论仍为 PARTIAL / PARTIAL / NOT_RUN（真人）。

整合 `374ca333` 的 distribution 全套回归由子 Agent 重跑：29/29、退出码 0、97232.973ms；后继 `50f4bc51` 分页补丁在主侧定向回归 4/4、退出码 0、12965.817ms。后者包括原受控双包消费及空页新 cursor 上限反例，不把同源码 fixture 记为上述跨版证明。

### 原真实任务修后追溯复验

独立浏览器观察者在实际服务加载 `index-CZg1Rmd8.js` 后，对原 `task-04ba8301-3721-43c5-872a-fb1b73a63d97` 完成四项下载：原输入 sales.json 347 字节、发布回执 796 字节、后验证据 1088 字节、交付 regional-window.json 382 字节。全部匹配公开 metadata 的大小、SHA-256 和归属；原输入按日期/paid 条件独立重算 east 2/1200 cents、west 1/-50 cents，与交付一致，输入→交付→发布回执→后验摘要链一致。修复前“原输入/回执不可从页面找到”的 P1 在此真实任务范围内复验关闭。

全过程仅公开 GET/HEAD，浏览器没有尝试 POST；前后 ready、Task、Workers、Leader、audit 完整对象严格相等，Task completed/revision62、12 Workers、12 Attempts，没有重新调用模型或重复发布。证据 `/private/tmp/ui-original.SfJrLH/report.md`、`evidence.json` 及输入/回执截图。服务启动源码 `db8aa08c`，采集时 HEAD `aa93f1be` 仅多文档；实际 HTML SHA-256 为 `3b3b2c93476fa674b21fea1463b1fd5ac3fa790f87a2fba921202c2397899aa2`。不把该只读复验记为新增真实模型任务、长期运行或真人验收通过。

主侧在 `aa93f1be` 的 UI 默认测试通过：42 文件、464 项 Vitest 加 4 项浏览器测试护栏，退出 0。随后 CI `34596493181` 两平台发行准入失败：新增服务模块未同步进显式发行清单，严格库存检查返回 `source_inventory_changed`；独立机械对照确认只缺 `cli-shutdown.mjs`、`service-diagnostic.mjs`。修复只补入这两个运行依赖，保持拒绝额外/遗漏库存的规则；最终同包检查与持续观察另行记录，不用 UI 测试代替发行验证。学习：任何新增运行模块都必须在同次本地验证中覆盖真实仓库打包/安装消费，不能只测从清单构造的 fixture。

### 可复用浏览器异常交互

main `9852e4da` 的原 Node team run `34598484218`、attempt1、push 事件已13项全部通过。原 artifact=`10262833820`，ZIP大小1515081字节、SHA-256=`a7e76d1a6bfa49c14c74a473f914d6f7ffc7be2600d4e16fcf7efaaf30ed4cc6`，下载后实际匹配GitHub API摘要；原producer日志给定 manifest=`sha256:025c4ea96b076ded7154d59792f455ba85791530e65abe806e3cd20fcf8344a2`，本地原manifest字节摘要一致（70文件/1490309字节）。原包保留 `/private/tmp/marshal-main-9852.RiUeEa/candidate.zip`，独立准入/安装消费另记结果；未签名、未发布，不包含后继暂停恢复修复。

500事件限定性能补验 `/private/tmp/ui-events-perf.QX14y6/report.md`：独立受控ACP driver按原协议产生合法工具通知，经原Execution/SQLite持久553条事件（530条progress），冷开只读浏览器实际保留500条。Darwin25.6/Node24.15/Chrome152、M5 Pro、1440×1000浅色，原 `CZg1Rmd8` 资产；实际section滚动先验证位移，再采样16次滚轮，长任务0、最大帧间隔16.8ms；活动路由可见92.4ms不是冷TTI，点击至第二帧31.4ms仅响应代理。历史尾页7.6ms缺“目标行原先不存在”的单独落证，不关闭新生成事件显示延迟；不代替持续负载、100Task＋500事件组合或最终包。Task/Workers/553事件前后不变、POST0、自有服务退出0。三次测试脚本前提错误（批准路径、分页参数、滚动容器）均保留FAIL；最终复用原数据根，不再次生成负载，不把测试错误归为产品缺陷。

浏览器安全限定补验 `/private/tmp/ui-security.MAIG8n/report.md`：原真实 HTTP/SQLite fixture 上传 HTML115B、SVG122B、Markdown130B，含脚本/事件处理器/远程图/危险链接测试字符串；列表/详情按文本呈现，危险类型只显式下载，摘要匹配，未打开文件。观测无脚本执行标记、弹窗、远程节点或外部请求尝试；网络护栏阻止任何实际外传。断开后旧内容消失、请求停止，URL/localStorage/sessionStorage/cookie 未见 token；同 origin 重连等待600ms不闪旧内容，第二空服务无原 Task。原脚本误等“返回列表”而实际回原详情，FAIL 保留；正常冷开两个原根补测通过，无新任务/模型，服务均退出0。不证明同 origin 后端替换、IndexedDB、堆内存擦除或全面渗透测试。

创建/认证限定补验 `/private/tmp/ui-create401.vmbfxe/report.md`：创建真实201被服务受理后丢弃浏览器响应，UI明确未知、不自动重发；显式原请求重放仍201、同key/body/Task ID，仅1 Task。原脚本误比较原始JSON属性顺序导致raw摘要不同，FAIL保留；结构字段严格相等，原根冷开核对仍1 Task，不声称raw字节相同。精确详情GET注入一次合法401后退回连接页、旧内容消失；排空300ms后6秒请求计数23→23，无轮询风暴。属于受控401，不冒充自然鉴权失效，服务均退出0。两组实际资产均 `CZg1Rmd8`／`--K1WoSHw`，不外推Safari、真人、全部竞态或附件上传恢复。

本批测试驱动复盘：手拼API路径/分页参数、凭印象等待状态/路由、比较语义回执的raw JSON顺序造成额外失败；并非都属产品rework。后继脚本改用既有TaskClient与OpenAPI，先校验请求序列和返回字段、以实际URL/revision及稳定渲染作定位；优先冷开原隔离根继续读验证，不重造任务或覆盖FAIL。运行控制中的已成功回执仍锁按钮另经查码确认为真实P2，不能以“脚本错误”一概抹去。

本地测试安全摘要（来源为主侧工具执行记录，独立 reviewer 未重跑）：`node --test packages/task-distribution/index.test.mjs`，原 session56683，11/11、0失败、退出0、62044.79325ms；`python3 -I -B scripts/node-candidate-admit_test.py` 在提交 `93ed5468` 后运行，原 session34478，31项、20.247秒、OK、退出0；`node --test packages/task-distribution/*.test.mjs`，原 session30594，29/29、0失败、退出0、75498.05975ms。已结束句柄后续不可重读，不据此声称另外一次独立执行；下文 CI 为独立远端证据。

后继 E18 限定补验 `/private/tmp/ui-deny.IusDKj/report.md`、`evidence.json` 使用 `93ed5468`／`CZg1Rmd8` 与新隔离原受控 fixture：deny 202、原摘要匹配、请求 replied；Task 实际 failed/invalid_leader_decision，成果页明确整体失败，publication pending、无回执。刷新再观察2.2秒后 publication-start 仍0、浏览器 POST 仍3、目录为空，服务正常退出0。补齐原脚本错误等待导致中断的成果页/刷新范围，不覆盖历史 FAIL、不将受控 Leader 的失败改为成功；“拒绝后仍 pending”的解释性仍有限，保留体验改进项。

服务修复后原业务数据根的单次只读观察：2026-09-11 20:00:05.899–20:15:05.910（Asia/Shanghai），单调时长900001ms，60轮/240次公开 GET 全部200，ready/supervisor ready、activeWorkers0、原 Task completed/revision62/12 Workers及完整事实摘要均不变，异常0；原观察进程自然退出0，主侧服务未停止。证据位于独立 worktree `ui-service-failure-close/.marshal/soak-readonly.9oMhbH/summary.json`、`observations.jsonl`，脚本 SHA-256=`21b0465c36e98447adfde8db27edd54d7df282b47caecb60000d22f658c2ce4e`。只证明15分钟只读窗口，不覆盖原约1.5小时故障窗口、活跃模型或长期生产稳定，原未知触发原因仍 OPEN。

`93ed5468` 修复后的本地完整发行回归为 29/29、退出 0、75498ms，含安装包 HTTP 启动、冷打开不重复执行、受控同源码双包回滚与拒绝不完整证据；不冒充最终 CI/签名资产。CI `34597445154` 的 macOS 与 Ubuntu Admission boundaries 均通过，后续独立接纳作业按 PR 事件预期跳过，尚不构成 main 原包接纳。

列表/卡片稳定态补验 `/private/tmp/ui-toggle.TkVxDZ/report.md`：Chrome152、1440/375、浅深主题，八个模式状态均在移开 hover 并等待500ms后截图（实际过渡150ms）；ARIA、选中背景与真实 grid/list 布局一致，无整页横溢。使用原完成 Task，仅 GET 与本地展示切换，前后 Task 不变；实际资产 `CZg1Rmd8`／`--K1WoSHw`。关闭旧截图对应的稳定态未核验缺口，不把历史两帧截图武断解释为唯一原因，不替代真人、Safari或长期稳定性。

E18/E19 单次受控 Chrome 152 实测记录于 `/private/tmp/ui-pub.PV5WYZ/report.md`，UI 资产清单摘要同为 `67942c314fa92181cedf44d278b49d9d4c05769de15f6744c3cc012fa15e463b`，两个隔离服务均正常退出 0，无真实模型或外部业务发布。E19 在授权前关闭自有 reader 后，真实 publisher 写出 93 字节文件，publication=succeeded、postverify=failed、Task=failed；原回执与文件保留，刷新前后发布启动仍 1 次、文件摘要不变，成果页不冒充整体成功，限定子场景通过。E18 拒绝得到真实 202，但脚本误等 `answered` 而合同实际是 `replied`，原自动化 FAIL 保留；同次停止后只读 SQLite 核对原 deny/digest 与回执一致，零发布启动、目录空。未执行 E18 显式刷新检查，且受控 Leader 随后为 `invalid_leader_decision`，不能写完整拒绝流程通过。学习：浏览器等待状态直接取既有合同，避免凭印象另造状态名；后核对不覆盖失败原记录。

另一独立单次 Chrome 152 测试补充经典问答与陈旧批准：第二公开 HTTP 客户端先批准成功后，原浏览器旧正文收到真实 `409 revision_conflict`，错误可见且没有自动刷新摘要重提；经典 `/questions/{id}/answers` 首次真实 202 丢响应后，显式重放保持原键、正文及 Operation ID，最终问题 `deliveryStatus=acknowledged`。使用既有受控 question fixture，原 30 秒问题期限未改，无真实模型或业务发布，服务退出 0。证据 `/private/tmp/marshal-classic-browser.EO7Scq/evidence.json`，脚本摘要 `f404de6483b31ebb1b20313e5a70d2e69e5ec065fabde88ae6daa0d4d89445b1`，UI 同为 `index-CpLIqAu5.js`。本次点击设置后未等路由稳定，截图仍为概览，故不计跨设置保留通过、不推断导航故障；只关闭明确断言的子场景，不代表全 Task 完成或完整验收矩阵。

浏览器测试的四项超时、清理与回执保护负测纳入默认 `npm test`（Vitest 后执行 `test:browser-guards`），使已有 Node team 的 UI 作业自动覆盖，不需安装浏览器。实际浏览器异常交互仍为单独执行，两类证据不可替代。

已整合测试源 `9a51a098` / `42b19128` 为 `d9c4fa07` / `80d20e10`，不改产品代码。Chromium 152、WebKit 26.5 对真实 HTTP/SQLite 与既有受控 ACP 实测：Leader 答复、批准、取消已取得真实 202 后丢弃浏览器响应；跨设置页不自动重发，显式原键/原正文重放保持批准与取消的非空 Operation ID；取消框 Escape 不提交，取消重放前后 Task/audit 不变。原 reviewer 一次提出三个测试级 P2，聚合修复超时、清理兜底及缺 ID 断言后复审关闭；主侧四项负测亦通过。独立 Agent 另运行修前同场景 Chromium 一次通过；修后双引擎证据与脚本摘要由 reviewer 核对。详细范围与命令见 [受控浏览器记录](../../apps/task-web/e2e/browser-fault-report.md)。

这关闭 E20/E31/E07/E14/E25 的已列子场景，不外推经典 task.answer、409、运行中取消竞态、发布拒绝/后验失败、纯键盘完整流程或真人可用性。未启动真实模型，也不把该受控 Task 当第三轮真实模型任务。

主侧在整合 `80d20e10` 另实际运行修后 Chromium 脚本，三项受控丢回执场景通过、服务退出 0；原取消回执重放后 Task/audit 未变。证据为本 worktree 的 `.marshal/evidence/browser-fault-chromium-RnW2sP/evidence.json`；脚本/保护函数摘要与上述冻结记录一致，无真实模型或业务发布。

- 候选所有变更独立审查及全量回归，关闭跨连接和 Operation 结果追踪缺口。
- 第三轮真实模型团队链及正常终态重启已通过；继续验证取消、故障中途恢复与最终冻结发行资产的同包消费，不重复不确定的写入。
- 实际浏览器补齐桌面/窄屏/缩放/键盘/错误状态/大量任务与事件；截图必须绑定候选代码。
- 模拟可用性按能力边界记录；需要真实用户结果时保持 NOT_RUN，不以 Agent 截图观察替代。
- 冻结同包候选，验证安装、独立消费和恢复/回滚后，再执行签名及 GitHub/OSS 发布流程。

### 最后修复与制品边界补验（2026-09-11）

- main `0549aa82efc0858fac0c8c07e887a9c696e9a647` 的 CI `34602433748` 已完成：13 项均 SUCCESS，包含四种平台/Node 组合的同包消费；不等于新版 UI 已发行。
- 已消费记录修复源 `93ae96a48e14c09d3bc655acf29d32f231b6118b`，整合提交 `2ca440fc`。唯一独立 reviewer 完整审查 8 文件，无阻塞问题，独立 42 项测试及构建通过。复用原 E10 根冷开，在 Chrome 1440/375 下核对只读历史与 event-37 中文说明；POST=0、新执行=0、原 Task/Questions/Workers/Events/audit 不变。报告 `/private/tmp/ui-ack-review.ktAYdF/report.md`。仅关闭 ACK 后无可见核对入口的 P2，不重计实时延迟，也不代表真人可用性通过。
- 整合后主侧全量回归实际通过 43 个文件、496 项 Vitest 测试及 4 项 browser guards（session1840）；不以 reviewer 的 42 项替代全量。
- E17 制品边界：冻结 `13f04cf8afea1aa5e5cb3a89ff8990f44d2e96ea`、实际 UI `index-BNLSYH6U.js`。原受控验证器/Application/SQLite/depot/HTTP 路径产生 8,388,608 字节 ready 制品，Chrome 下载 SHA-256 为 `8915466a89b7008e77f6094e4d6abde067cf87f4e06a9c095e3d8c55ac33ecac`，与公开元数据一致；Task `task-a8227cf4-9cbf-402e-b39c-b3c7dd01d6d7` completed rev19。超出 1 字节的 Task `task-088173f2-4c0b-46bf-b16f-210eaae3741c` failed rev20，唯一 verifier 明确拒绝 `verification_delivery_invalid`，无制品/下载入口。报告 `/private/tmp/ui-8mib.gKVlsM/report.md`，最终 session26179 exit0，自有服务关闭。
- 上项使用同步 delivery 回调的确定性测试字节，不冒称真实模型/业务交付、8 MiB 输入上传或流式内存上限。Chrome 超限 wire 注入仍未测；原 handler/jsdom 证据与本次真实浏览器证据分开。
- 两次私有脚本前置失败（幂等键含空格、空列表错误使用搜索框 locator）均保留，发生时尚未创建 Task；修正后同根复用输入，两个实际 Task 各一次。今后键值先按客户端合同规范化，连接断言必须同时覆盖空态与有数据态。不得将脚本重试隐去或算作产品失败。

三线结论仍分别记录：功能/视觉为限定范围补充证据，目标用户可用性 NOT_RUN；Agent 模拟不能替代真人。最终发行资产仍需在新 main 冻结后复验，未发布新版本。

E14 限定补证（`9cdfc074`，`/private/tmp/ui-terminal-cancel.7Badza/report.md`）：原 E10 Task 已 completed/rev23，Chrome 实际滚入控制区后无取消入口。唯一一次正式客户端使用此前真实观察的 rev15 取消，返回409 `revision_conflict`；原 Task/Questions/Workers/Events/audit及两项制品元数据、字节与摘要不变，新增执行0，客户端POST1/浏览器POST0。session6647及所属服务均退出0。由于 Core 先检查 revision，此证据仅证明终态展示与陈旧 CAS 拒绝，不关闭同时竞争或同revision终态guard，不追加重复请求凑覆盖。

执行顺序：按增量研发规则，在独立代码审查及相关本地/CI 检查通过后合并 PR，并继续保留上面的 UI 验收缺口；再取得该 main push 的原始 CI 包进行最终同包验收。不能要求先取得尚未产生的 main 包才允许研发合并，也不能用研发合并关闭 UI-1 或直接发版。Safari 工具探测本轮返回 `Browser is not available: Safari`，未创建标签或改变权限；这是当前浏览器控制入口的限制，不表示 Safari 产品兼容性失败，实际 Safari 仍待验。
