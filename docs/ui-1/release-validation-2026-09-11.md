# UI 正式发行候选验收记录（2026-09-11）

## 当前结论

**尚不可声明完整验收通过，尚未发布新版。** 本记录补充三条验收，不以组件测试替代实际交付、视觉操作或目标用户测试。

| 验收线 | 当前状态 | 证据与缺口 |
| --- | --- | --- |
| 功能与可靠性 | PARTIAL | `9e5e49e0` 关闭答复迟到 P1，41 文件 425 项测试及构建通过；第三轮真实 Pi 团队完成独立审查、验收、授权发布和后验，见下文。取消、故障及同发行包全范围验收仍待补齐。 |
| 视觉与交互 | PARTIAL | 实际浏览器完成创建、附件上传、Leader 答复、计划批准、DAG 与团队查看；窄屏、缩放、键盘、完整错误/恢复矩阵尚未全部完成。 |
| 产品可用性 | NOT_RUN（真人）；Agent 截图观察已完成 | 用户选择子 Agent 模拟；实际观察者参与过 UI 实现且无法访问主侧浏览器，只有截图观察，主侧负责操作。不能称为独立可用性测试、盲测或真人验收；其独立验证范围仅为未参与实现的报告内容与后端交付 GET。 |

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

后续新增一位未继承开发历史、未参与 UI 实现的 Agent，约三分钟完成限定只读目标：从列表找到已完成任务，理解日期/两作者及校验分工，区分 review、acceptance、发布和后验状态，下载最终 JSON/验收 JSON，并从设置返回原成果页。未读源码、API 或 SQLite，也未收到点击指导；两次各约 30 秒的等待来自把团队误当按钮、假定设置页仍有工作台任务链接，不能归为业务失败。证据 `/private/tmp/marshal-observer-enS74O/observation.md` 及同目录页面截图/下载文件。这是按用户选择执行的无点击指导 Agent 模拟，仍不替代真人测试。

该观察者仍难理解多个 managed-leader ID 与尝试编号，且在页面未找到原输入或实际发布目标回执，不能自行重算或验证目标物理落盘。上述为后续核对的可用性线索，不从一次未找到推断服务丢数据；之前独立原输入/实际目标核验事实继续保留。

参与过 UI 实现的 Agent 在截图观察中认为业务问题、答案与提交按钮的排列可发现，但手写 JSON 日期负担高，技术标识与“投影/合同档案”等术语增加理解成本。这不是独立用户测试。通用 UI 不应为单个报告场景硬编码日期字段；后续应结合已有输入合同评估通用结构化输入，不能从任意模型文本猜测并执行新协议。

追加观察曾发现候选页同时出现“答复已受理”和“等待你的答复／需要你的处理”，用户难判断该等待还是重复提交。此项及迟到响应反例已由 `9e5e49e0` 修复，第三轮真实答复截图显示一致等待状态。另外，失败页的多个历史“规划”成员与 `agent.running` 观察摘要易混淆当前工作，且仅凭成员完成无法判断结果是否已交付。后续验收须要求观察者明确指出当前责任人、失败影响，并区分产生文件、独立验收和实际交付。

## 数据密度与有界性补充

候选 `b887cecd` 增加真实 React 组件的隔离 fixture 回归：100 条任务分页、卡片切换与筛选；事件持续读到 600 条，DOM 保留最近 500 条，后继合并输入不超过 550 条、两页。独立代码审查无阻塞问题。子 Agent 四轮 jsdom 观测中，100 任务载入为 141–145ms，500 事件载入为 332–364ms；这些只包含隔离组件执行，不含真实网络、浏览器布局/绘制或模型耗时，不作为性能 SLO 或视觉通过证据。

## 发布前仍须完成

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

### 可复用浏览器异常交互

已整合测试源 `9a51a098` / `42b19128` 为 `d9c4fa07` / `80d20e10`，不改产品代码。Chromium 152、WebKit 26.5 对真实 HTTP/SQLite 与既有受控 ACP 实测：Leader 答复、批准、取消已取得真实 202 后丢弃浏览器响应；跨设置页不自动重发，显式原键/原正文重放保持批准与取消的非空 Operation ID；取消框 Escape 不提交，取消重放前后 Task/audit 不变。原 reviewer 一次提出三个测试级 P2，聚合修复超时、清理兜底及缺 ID 断言后复审关闭；主侧四项负测亦通过。独立 Agent 另运行修前同场景 Chromium 一次通过；修后双引擎证据与脚本摘要由 reviewer 核对。详细范围与命令见 [受控浏览器记录](../../apps/task-web/e2e/browser-fault-report.md)。

这关闭 E20/E31/E07/E14/E25 的已列子场景，不外推经典 task.answer、409、运行中取消竞态、发布拒绝/后验失败、纯键盘完整流程或真人可用性。未启动真实模型，也不把该受控 Task 当第三轮真实模型任务。

主侧在整合 `80d20e10` 另实际运行修后 Chromium 脚本，三项受控丢回执场景通过、服务退出 0；原取消回执重放后 Task/audit 未变。证据为本 worktree 的 `.marshal/evidence/browser-fault-chromium-RnW2sP/evidence.json`；脚本/保护函数摘要与上述冻结记录一致，无真实模型或业务发布。

- 候选所有变更独立审查及全量回归，关闭跨连接和 Operation 结果追踪缺口。
- 第三轮真实模型团队链及正常终态重启已通过；继续验证取消、故障中途恢复与最终冻结发行资产的同包消费，不重复不确定的写入。
- 实际浏览器补齐桌面/窄屏/缩放/键盘/错误状态/大量任务与事件；截图必须绑定候选代码。
- 模拟可用性按能力边界记录；需要真实用户结果时保持 NOT_RUN，不以 Agent 截图观察替代。
- 冻结同包候选，验证安装、独立消费和恢复/回滚后，再执行签名及 GitHub/OSS 发布流程。

执行顺序：按增量研发规则，在独立代码审查及相关本地/CI 检查通过后合并 PR，并继续保留上面的 UI 验收缺口；再取得该 main push 的原始 CI 包进行最终同包验收。不能要求先取得尚未产生的 main 包才允许研发合并，也不能用研发合并关闭 UI-1 或直接发版。Safari 工具探测本轮返回 `Browser is not available: Safari`，未创建标签或改变权限；这是当前浏览器控制入口的限制，不表示 Safari 产品兼容性失败，实际 Safari 仍待验。
