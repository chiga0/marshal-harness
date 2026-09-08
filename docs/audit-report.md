# 设计审计报告

## 2026-09-08：Task 取消组合验证与状态字段生命周期

取消初稿 `a59a138` 的聚合审查发现：坏 Task 的局部 Run 读取失败不推进取消游标；取消信号被 finalizer 当成全局调度错误；HTTP 测试 helper 未释放 endpoint borrow，导致冷关闭无限等待。前两项在 `387561e` 修正，helper 在 `eea6e01` 修正。主 Agent 对自己持有的挂起测试进程取 SIGQUIT 栈，确认阻塞于 Session.Close 后停止该次执行，没有重跑未修代码或终止其他 Worker。

组合 `d9cdfa82e0001a3d7a01d42cf36adae0cf257050` 的 Mac 定向测试通过：坏任务不饿死健康任务、停止任务不触发全局 finalizer、draft/approved/READY/reservation 冷恢复、缺目录与缺 cleanup 拒绝、Outcome 前停止的两个真实 Git/Verifier/Importer 场景（19.55 秒）、Outcome 先赢的 export/cancel 竞争（13.34 秒）以及原完整交付链（14.79 秒）。香港 ECS 在专用账号与资源限制下 application/taskhttp/productionruntime 整包 race 通过；resultingress 整包 race 超过 180 秒失败，随后仅取消三项的定向 race 58.114 秒通过。保留整包超时，不能以定向结果替代整包门禁。

复审发现新的明确缺口：原 RunStart projection 只在 READY/RUNNING 填充冻结摘要，取消 consumer 却在 BLOCKED/VERIFYING/REVIEW_PENDING 等状态继续读取同字段，因此真实 cleanup 后仍不能结案。实现 `f4eb0347` 已改为在同一 Run lease 下重读原冻结文件、快照与 journal，并逐项绑定原 Attempt 和合法 completed/stopped 事件；不扩改旧 projection，不接受手填 cleanup 摘要。新增 projection-only 夹具又暴露非法跳状态，`f1474f47` 按原合法状态路径修正；同 reviewer 已复审无剩余代码 P0/P1。完整 Task→真实 CancelRun→cleanup→disposition 正例继续是 B1 实机出口，不因组件通过而关闭。

实际教训：覆盖 producer 字段的**状态生命周期**，不仅比对静态字段名；缺证据负例必须证明拒绝发生在目标 gate，不能在更早的 fixture 错误处假通过。本轮不为测试共享再造协议平台，局部确定性回归与真实 Worker 验收分别留证。并行 CI/审查/后继开发已执行；#274/#275 全绿后分别合入集成分支，不是 main 或正式发布。

## 2026-09-08：自动验收生产链接缝与流水线修正

核心候选 `064b528` 的唯一 reviewer 发现 P1：objective consumer 漏掉原 Verifier 必定生成的 denial-summary、tool-audit、tool-allowlist，因此手写报告正例通过而真实报告必被拒。修正保持 frozen worker.tools 的必需/可选语义，只允许原 producer 合法 skipped，不放宽未知/缺失/重复/失败 gate。另将实际 sealed Verify 的 ToolAllowlist 接自原冻结 Task，并把 resident 的等待错误映射放回 Application 边界，没有扩架构白名单。

验收改为实际 Verifier→固定 oracle→原 Packet/Importer，以及三个真实 Git 候选→原接纳/集成→制品 producer/RB1/冷读。新增夹具的 mediaType、固定根布局、Worker transcript-meta 曾先后失败；均修正夹具且保留原门禁，不归咎模型。最终 `aa4a82d` 由同 reviewer 复核，主 Agent 独立固定路径复跑完整 Session 测试 15.80 秒通过。这里的 Worker 输入/启动/收集是明确的确定性模拟，不证明真实 Agent、HTTP 自治或 B1 完成。

可执行的作者自测应在审查前运行完整 producer 链；共享固定测试路径明确唯一写入者并交接，最终由非作者独立复跑。CI(N)、开发(N+1)、设计(N+2) 交错，而非每修一个夹具字段重新等待整轮 CI；B2 预设计不占用 B1 当前共享写入文件。具体候选、同步状态和缺口维护在 Roadmap 当前表，历史失败不清零。

## 2026-09-08：Task HTTP 候选接入与入口遗漏复盘

后续验证：实现 `70ec148` 已经原 reviewer 复核关闭两项 P1；香港 ECS 三包完整测试通过，Mac 的真实 activation 入口和 held Session/HTTP 冷重放通过。独立客户端 `f333dfd` 的 17 项解释型测试经主 Agent 复跑后合入本地候选 `736fcc9`，非 main 合并。详细命令、候选和缺口见入口文档。Mac 共享 lane 测试在测试输出前被 AMFI 以签名问题终止，未记通过、未绕过；该平台回归待合法放行或独立 Darwin CI。无模型调用、自动 Decision 或下载完成声明。

验证流程另有两次可避免的执行错误：主 Agent 初次独立编译测试未注入 Makefile 要求的 sourceHead，随后直接从仓库根启动导致包相对 fixture 路径不成立。按既有构建参数和包工作目录纠正后同测试通过，没有修改产品或测试门禁。后续固定路径测试入口应同时保留精确 linker metadata、包工作目录、测试选择与二进制摘要，不能只固定输出路径。

候选把公开 Task 的创建、精确确认和按 ID 查询接到原 RepositorySession 与同一 RB1；原 accepted plan 仍是预算和三个创建义务唯一提交点，loopback adapter 复用 resident 应用和写入队列。范围与可重复请求方式见 [Task HTTP 候选入口](task-http-preview.md)。自动 Decision、Task cancel 与完整下载尚未实现，B1 不关闭，无模型重试或发布声明。

唯一 reviewer 聚合发现两项 P1：新增 parser/HTTP 测试没有覆盖更外层真实 RunContext 的启动参数 gate；正数陈旧 revision 被过早归为无效输入，导致 Darwin 调用链测试期望冲突时必失败。修正把封闭参数解析复用到真实入口与启动 consumer，增加原 activation 准入和共享 writer lane 回归；revision 正值与当前草稿不符统一为冲突。该经验是验证完整入口，不是增加审批轮次或另建协议。

修正曾被自动工具以 ADR 授权不足拒绝，未换工具绕过。用户随后明确授权“按 ADR0085 放行 Task HTTP 的封闭 CLI 参数、复用现有写入通道，并修正 revision 冲突返回码及相应测试”，才以原工具实施。不改 ADR 历史状态。ECS 旧基线和新源码快照的非模型完整/定向 race 结果与实际边界记录在入口文档；精确最终提交与 Darwin 实机证据仍需后续验证，分层 fixture 不冒充完整业务链。

## 2026-09-08：ADR0085 接受，恢复 Task HTTP 主线实施

用户明确确认“ADR0085 ok，请实施”。据此将 ADR0085 标记 Accepted，解除 Task draft/stop/delivery、HTTP、自动独立 Decision 和完整交付的合同等待；实现复用已验证 resident 候选，不重建平行状态机。B1 仍 IN_PROGRESS，合同接受不是实机或发布证据。

用户同时明确原 ECS 为内网机器，不应接入 GitHub CI；现有 GitHub canary 在 GitHub-hosted runner 上生成 Pi 配置，不是在该 ECS 上执行。后续公网 ECS 的地址及授权尚待提供；不把内网机器注册为 GitHub runner，不把取得公网机器作为 API 编码前置，也不在聊天或日志中索取密钥。

## 2026-09-08：CI 模型配置不等于本机已配置模型元数据

停止盲目重跑后的只读核对发现，`scripts/rc1-canary-provider-config.py` 为每个模型统一写入 `contextWindow=128000`、`maxTokens=16384`，不提供 reasoning/compat；这不是读取用户本机 Pi 配置。本机 Pi 0.84.4 中 `qwen3.8-max` 的两个已配置 Provider 均声明 contextWindow 1000000、maxTokens 131072、reasoning true，其中一个还声明 Qwen thinking 格式及禁用 developer role/store。配置声明不等于服务端能力验证，不能盲目复制到未知 endpoint。

在同版本 Pi `streamSimple` 的 `onPayload` 上执行了零网络构造实验：只使用虚构 endpoint 与测试密钥，在请求发送前终止，并断言 fetch 调用数为 0。旧 CI 元数据实际构造 `max_completion_tokens=16384`，缺少 `enable_thinking`/`reasoning_effort`；本机元数据加显式 low 构造 `max_completion_tokens=131072`、`enable_thinking=true`、`reasoning_effort=low`。后者只是对照夹具，并不证明实机 Worker 当前选择 low。该实验确认配置差异会影响实际请求，不证明历次 length 的唯一根因，也不授权扩预算或改终态接纳。

下一步须先确认 CI secret endpoint 对应的已验证 Provider，再使用显式匹配的非敏感配置；无需索取或输出密钥。在确认前不更改远端 Provider 配置、不启动新的付费团队。候选 `775de21` 的 Darwin 定向 `34158437379` 已成功；现有团队交付、Task HTTP 和正式部署出口仍未完成。

后继实现增加可选 GitHub variable `PI_MODEL_PROFILE_JSON`，仅接受选定 model 的 `id/contextWindow/maxTokens/reasoning` 与受支持的 `compat/thinkingLevelMap`。限额必须为正整数、输出上限不大于 context，拒绝未知字段、重复键、错 model、超大/深层 JSON；显式配置错误在创建模型配置文件前失败，不能静默退回旧值。未设置时旧调用者行为保持，日志明确 `legacy-default`；显式时仅输出规范化配置摘要，绝不输出凭据或 endpoint。该输入不设置实际 reasoning level、不调整 Task 总预算、不证明服务端能力。当前未设置远端变量；须管理员确认 endpoint/profile 后才启用，未知配置不得作为新的实机重试理由。

首稿 `6d9306f` 的 CI `34159224060` 在三个 Linux 作业的 release contract 前置检查失败：新增独立 CI step 不符合既有锁定步骤结构，尚未执行新增测试。该错误属于本轮调用链检查遗漏，不能归咎 Provider。聚合修正撤回新增 CI step，把离线配置测试放入既有 `fixed-server-t1-canary_test.sh` 入口，保留原发布合同与完整断言；以后修改 CI 接线必须在本地先执行现有解释型 release contract gate。动态质量检查与真实业务出口继续分别计量。

## 2026-09-08：停止收口实机通过，团队失败观测仍有盲点

精确候选 `dd8e8eccdd1f2118db92e928996321272aff1fc7` 完整 CI `34156121695` 五项通过，Darwin 定向 `34155305960` 的 36 项必跑检查通过。其唯一实机 `34157213736` 仍失败，诊断 artifact `10031514061` 保留原始证据；固定二进制 SHA-256 为 `3bd2b444e62b009449b47887660f4db666161cc4d877756bb3307d2235d5378d`。没有 ReviewPacket、独立 Decision、集成或下载成功，B1 不升级。

独立核对账本：service 已有 `result-admitted`，事件到 sequence 4 `VERIFYING`；client 因 `pi-result-provider-terminal-length` 触发 sequence 54 `team-plan-halted(stage=collect)`。client 的 Collect 52/53 唯一成功，Terminate 56/57、Close 62/63、supervisor closed 64、cleanup completed/released 65/66 全部闭合，没有第二次 Collect 或结果接纳。此次实机证明上一停止收口修复生效；`VERIFYING` 不证明 Verify 已启动，团队 halt 后禁止新 Verify 是既有合同，不应自动解除。

诊断中 59 个已保存调用全部成功（一次批准、58 次 Inspect），54 次 service 进度查询最后仍成功。最终超时调用在保存前抛错，缺少预算与耗时证据，不能据此外推 HTTP/锁故障。驱动串行先等 service，也看不到 client 已 `BLOCKED`。本轮仅改诊断消费者：等待期间查询同一批准团队的另一节点，精确绑定终态失败即结束等待；保存超时操作、预算、耗时和输出摘要，不输出原文、不增加 Collect/Verify/恢复权限。Provider length 已复发，仍缺少实际输出预算原因，禁止原样付费重试或盲目增额。该改进不等于业务交付修复或生产完成。

## 2026-09-08：结果拒绝后的停止收口仍阻断团队交付

候选 `88883d9c04406fb65fe5b695f80a88bb83fbbfb0` 完整 CI `34152276913` 五项通过，Darwin 定向 `34152276380` 的 33 项必跑检查通过；精确门禁后仅派发一次实机 `34153526402`，结果 failure。诊断 artifact `10030318154` 保留原始证据，没有 Decision、集成或下载成功。本轮未到 ReviewPacket，不能据此宣称上一轮身份传递修复已实机验证。

原日志首个业务拒绝为 `pi-result-provider-terminal-length`，ledger sequence 38 记录 service transcript 已收集，sequence 39 记录 `team-plan-halted(stage=collect)`；并非团队尚未派发。期限终结后 service 已有 `process-terminal` 和 `allocation-terminated`，但后续 collect 在 sequence 48/50/66–88 反复 `process-supervisor-identity-conflict`，缺少其 supervisor closed/cleanup released，最新对外 projection 仍 sequence 3 RUNNING。client 则完成停止清理并投影 `BLOCKED/attempt-deadline-exceeded`。客户端最终 `fixed-cli-response-timeout` 是外层表现，不能替代上述具体失败链，也不能将 service 的旧 RUNNING 投影当成进程仍活跃。

根因已定位：通用 Collect 查找只看最新 checkpoint，后来的 Terminate 遮住旧成功 Collect，而物理 mechanics 明确只允许收集一次。候选仅修停止收口接缝：已有有效耐久 Collect 即继续原 Close；不重发 Collect、不通过历史 anchor 重读，不修改通用结果接纳。Close 仍使用当前 owner/head 校验真实 journal 与 transcript 对象。新增完整耐久链回归覆盖 Collect→Stop→Close、丢回复、独立 absence 与冷重开 CleanupReleased，要求停止后零 Collect、无业务结果接纳；保留原未收集停止与正常 Inspect 路径。本地 vet/staticcheck/diff 检查通过，远端 Darwin 动态结果尚待验证。

Provider length 的具体输出/预算原因仍需证据，不能猜测为用户未配置、直接增预算或修改正常终态准入。保留两次 Attempt 和失败分母，不原样付费重跑，不放宽 `native-terminal/v1` 的正向终态要求。B1/B2/B3 状态不升级。

修复候选 `a003ca7` 的快速检查 `34154916006` 首次失败于测试编排：三组耐久链共用 120 秒总限时；原 Terminate 链及 SameOwner 两分支通过（后者 65.96 秒），新 Collect→Stop 链运行约 17 秒时总限时耗尽，堆栈仍在耐久重放/摘要计算，没有业务断言失败。纠正为三个精确顶层用例各自 120 秒，保留 race、全部断言及必跑成功集合，并让新失败链先运行；不增加 Worker 预算、不以超时当测试通过。原完整 CI `34154917505` 保留运行，不为诊断编排修正取消。

## 2026-09-08：原生结果实机进入独立 Verify，团队仍未交付

`a5418f4acbb1fe3b581a4c6d079510048d82dcee` 的完整 CI `34149966062` 五项全绿，Darwin 定向 `34149938176` 的 30 项必跑检查通过。精确候选 gate 通过后只派发一次真实双 Pi 团队 `34151269983`，其失败证据保存在 diagnostic artifact `10029503062`。service Run `team-run-0189f9bd1f824517f7eb8d01a8b1d5fd5a38150a7b515f9ea39197e055743772` 的事件已到 sequence 4 `worker.completed` 和 sequence 5 `verification.completed`，报告为 pass，包含 `command:quote-team-service`。这首次为本候选原生结果→独立 Verify 接缝提供正向实机证据，但不表示整个团队成功。

随后 `call-21.json` 的 `review-packet` 请求退出 1、stdout 为空；server 仅记录 `stage=server-dispatch reasonCode=transport-failure`，尚不足以确定底层原因。没有独立 Decision、集成或下载消费；另一路的原始 state 快照不能替代 journal/current projection 判断实际状态。保留整次失败和既有预算，不原样重跑，也不把完整 CI 绿或单节点验证通过关闭 B1。下一步沿实际 ReviewPacket 接线定位，同时在独立 worktree 补 Task HTTP 用户出口。

独立接线审计进一步发现确定性 P1：服务 HTTP 请求从 `context.Background()` 建根，丢失入口已 gate 的 local identity；后台 Verify 继承入口 context，故可生成带 local binding 的报告，而 HTTP ReviewPacket 的 `prepareLocalReviewBinding` 必拒绝缺失身份。归档与此阻断吻合，但缺少原始内部错误及 manifest，不能排除更早输入失败，不能称为此次唯一首错。修正仅改为保留值的 `context.WithoutCancel(ctx)` 再建立独立 request cancellation，不跳过身份检查，也不使服务停止立即取消排空中的请求。补身份保留、无身份不伪造、父 deadline/取消隔离及显式排空取消回归，纳入 Darwin 快速必跑清单；本地 vet 通过不替代远端动态回归，更不等于 ReviewPacket 或 B1 已实机通过。

## 2026-09-08：Schema 消费链漏检与前移修正

候选 `2885dcc` 的全量 CI `34148751006` 在 Ubuntu 的 execution 包发现两项失败：新增 `/worker/resultContract` 未加入既有 prompt projection 分类目录，同时使合成未知字段反例出现额外未分类项。这是实现遗漏及定向检查选取不完整，不是模型失败；计入额外修正，不以先前 22 项 Darwin 定向通过掩盖。未启动新的付费团队 canary。

修正把该字段显式列为 Core/Adapter 使用的 hidden 字段，补独立 non-leak oracle 与渲染哨兵；不改变旧 prompt 可见字段或放宽 Schema 覆盖门禁。快速 Darwin 工作流增加 8 项既有 projection/泄漏检查并要求真实 pass。后继先运行 ECS 完整 execution race，再进入精确候选全量 CI；新增持久化字段的前置检查须覆盖既有消费者，不能仅选择新测试名称。本节记录修正范围，动态验证与 B1 实机出口仍须分别取得证据。

## 2026-09-08：从模型控制 JSON 改为显式原生结果候选

针对 `34145704791` 的真实 Collect 拒绝，本轮不再仅改提示词/诊断后重复付费 canary。按 ADR 0085 的 Adapter 责任边界，新增冻结的 `worker.resultContract=native-terminal/v1`：同一 TaskSpec 字段贯穿 Pi launch、真实 Supervisor terminal/exit/signal/truncation、严格 transcript、结果构造与原独立 Verify。模型只负责业务文件及真实报告，不能提供身份/时间/控制证据；报告中的受阻、失败和未完成内容原样保留。旧 JSON 默认语义不变，未知协议和不支持新协议的 legacy executor 在启动前拒绝，不能自动 fallback。

本次候选同时更新订单团队输入，保留原共享业务契约、独立 oracle、一次尝试/零 rework 和成果要求；没有降格验收或扩大工具权限。新增协议兼容、伪造字段、真实退出失败、缺失终态、截断、Provider 失败、报告超限和组合根传递反例。动态 Go 验证交给远端，不在受管 Mac 上执行临时编译程序。此处记录实现范围，不声称 B1、生产启用、远端合并或正式发布完成；须待独立审查、精确候选 CI 与真实交付通过。

首稿 `f9923eb` 的 ECS Pi/contract race、legacy 拒绝和 CLI 接线测试，以及 Darwin `34148180525` 的 21 项必跑用例通过；独立审查仍发现 1 项 P1：原 native 路径只排除已识别失败，缺失/null/空/未知 `stopReason` 仍会被当成正常结束。实机重跑前聚合修正为只接受实际末条 assistant 的明确 `stop`，补完整反例和旧协议兼容；停止首稿尚未完成的 CI，未启动付费 Worker。此项计入真实代码 rework，不把先前测试绿当无缺陷证明。

## 2026-09-08：公平调度候选的实机结果与 Pi 结果拒绝

`495ab02fcae086984407fb92f96fbc65c390ef2f` 完整 CI `34144298657` 全绿，Darwin 定向 `34144087380` 的 11 项检查通过；真实团队 `34145704791` 仍失败。诊断 artifact `10027742173` 中首个业务错误为 `pi-result-final-object-invalid`，当前账本记录 service 的 `team-plan-halted(stage=collect)`、两条 `process-terminal`，另一路 Outcome 为 `BLOCKED/attempt-deadline-exceeded`；driver 最终报 `fixed-cli-response-timeout`。本轮已有 Collect 进入并拒绝结果的证据，与上一轮仅 RUNNING 不同；未提交 Decision、未集成、未重试，不计 B1 通过。

旧分类把终态文本中 JSON 语法错误、canonical 拒绝，以及错误位于已识别结果前/后混为同一标签；上传包又未包含终态内容。下一候选只在原拒绝点输出封闭分类（syntax/canonical、before/after-result），不输出文本、字段名、路径或偏移，不重解码、不更改结果接纳集合。before 包含尚未识别成功的结果声明本身；canonical 也不等同于已证明重复字段。新增确定性拒绝及伪造标签反例，并纳入远端 Darwin 快速反馈。它是诊断补齐，不宣称已修好 Pi 实际输出；获得真实分类前禁止原样付费重试或盲目延长超时。后续仍须完成 Task HTTP、组合验收、下载消费与正式部署出口。

## 2026-09-08：真实团队超时与后台调度公平性

精确候选 `cd19a6d20dcce4ef9eb51ea3cf455f6fd89c76de` 的完整 CI `34140891818` 五项全绿，Darwin 定向 `34140755250` 通过；但真实双 Pi canary `34142425497` 在 360 秒观察期限内未到评审，原因 `team-resident-progress-deadline`，未提交 Decision、未自动重试。两条 `process-started` 已入账，49 次 service 查询仍为同一 RUNNING head。原始 `state.json` 的 READY 不能覆盖 fixed Inspect 的 journal 投影。上传包没有 Worker 终态输出；空 stderr、没有 Collect successor 也不能证明 Collect 从未进入，因为正向 still-running 本来不追加结果事实。因此本次唯一根因尚未确定，不归咎模型速度、不直接扩大时间预算。

独立源码诊断确认四个同周期后台 ticker 缺乏调度公平性：deadline/dispatch 持 writer 与 adapter 锁进行较重扫描，其他入口 TryLock 失败静默跳过；耗时大于周期时积压 tick 可使某循环持续获胜。候选修正为四类短入口轮转，Verify 经原 Run lane 与当前团队准入的显式握手后独立有界执行，保持单个验证槽、原精确重查和 halt/circuit。增加积压 tick、准入前不得让行、长验证期间兄弟继续、重复调度不重复验证与未准入取消测试。此处仅修正已证实的饥饿风险，不宣称真实 canary 已恢复或 B1 完成；后续须同候选动态验证及真实完整团队交付。

效率教训：纯 selector cursor 测试不能证明多个生产循环公平；应测试实际 callback 的有限服务机会。运行证据必须能区分无候选、锁忙、正向存活和结果接纳，不能持续以空日志猜测。正式 Task HTTP、集成下载消费等 B1 出口仍开放。

## 2026-09-07：Darwin 动态验证失败与前移反馈

`3f91d425` 的 CI `34138530708`：Linux 两种架构 conformance、Ubuntu quality 与 secret scan 通过，Darwin quality 失败，不得进入团队实机或标记通过。新增 cold verification/busy sibling 测试未先调用团队批准就物化，触发预期的 authority-conflict；修测试前提，不放宽产品批准。另一失败是 resultingress 全包 race 达到 Go 默认 10 分钟上限，当时栈中单测仅运行 4 秒；ECS 对 `eb480a83` 同名单测独跑 7.014 秒通过，只能排除该 Linux 定向运行的持续卡死，不能替代 Darwin 结论。

发现后取消包含相同测试代码、由本任务启动的 `34140017814`，保留日志，不继续原样全仓重试。补候选分支 push 触发的 Darwin 8 项关键路径诊断（检查实际 pass、拒绝零匹配和任意 test/package fail），先反馈当前调用链；新 workflow 不以尚不存在的默认分支 manual 入口为前提。全仓仍保留全部 race，将包级预算显式设为 20 分钟，原 CI job 30 分钟上限保留。该时间是整个测试包的累计预算，不改变产品期限或准入；若仍超限继续分析，不无限延长或删测试。诊断 workflow 不授予 canary、merge、release 权限。独立审查指出 `go test | tee` 的退出码遮蔽风险，已显式启用 pipefail 并拒绝 JSON 中任意 fail，避免只检查选定成功名而漏掉额外失败。

## 2026-09-07：自动团队推进候选与远端验证边界

后续 canary 修正客户端验真方式：`order-quote-team` 显式启动 `--auto-team-progress`，客户端只观察原 Attempt 的单调状态、获取 ReviewPacket 和传递独立 Decision；禁止客户端 Start/Collect/Verify，诊断记录三者调用为零。原主动驱动的其他场景不变。归档报告 status 仅标记诊断来源，不代替 Core canonical digest/current-ledger 重查或独立审查，也不证明进程重叠。19 项团队客户端、44 项原客户端与 canary 脚本检查通过；这是测试准备，不是实机团队成功。完整 HTTP Task、独立集成及下载消费仍开放。

该客户端独立首审发现 1 项 P1：只读 Inspect 等待 verifier 的 Run lease，却被新增 30 秒子进程上限提前终止。取消这一额外截断，保留 CLI/服务端原操作期限与场景总预算；补 40 秒模拟锁等待、90 秒剩余预算及不重试测试。学习点是读操作同样可能等待执行锁，客户端超时不能脱离服务端锁/phase 契约。

`feat/team-resident-progress` 基于 `00d3749` 复用现有 Collect/Verify/Run lane/current-ledger 路径，避免要求客户端逐节点推动。语义 Decision、任务级 HTTP 与下载消费仍待完成，不将 REVIEW_PENDING 计作团队交付。

独立 reviewer 首审发现三项 P1：新 flag 在更早的 CLI gate 被拒、Verify 硬崩溃后无开始事实而会自动重跑、旧 dispatch circuit 不覆盖新自动推进。集中修正真实 CLI 准入测试、冷启动既存 VERIFYING 团队持久 halt/忙 lease 拒启动、共享 atomic circuit；同一 reviewer 限定复核未见新增 P0/P1。冷启动屏障有意保守，可能同时暂停未真正开始验证的旧 Run，不冒充自动恢复。新增当前账本/冷重开/共享锁测试；新候选仅本地编译与静态检查通过，动态验证转 macOS CI，不挪用旧结果。

效率教训：内部函数通过不代表完整入口可达；幂等结果不等于命令不会重复执行；增加循环必须共享故障控制。这三类检查应一起纳入后续纵切，不再等 reviewer 逐项发现。继续不用 Marshal skill。

用户授权 ECS 已通过 SSH 安装校验过的 Go 工具链并实际执行旧候选基线测试，未用 root/关闭安全防护/开放公网端口。Linux 测试不能证明 Darwin 代码正确，缺测试明确记录；旧 renderer 固定 `/usr/bin/python3` 与该机器系统 Python 不兼容，两次失败保留，停止原样重试。具体配置和证据范围见[远端验证](remote-linux-validation.md)。新 Goal 保持业务出口，不因 runner 配置完成而宣称生产可用。

## 2026-09-07：Task-first，团队交付先于管理平台

按用户要求重新检查首个业务出口，发现上一稿把 Workspace/安装身份/显式初始化/全面 SQLite/三 Provider 放在团队之前。源码有现成 RepositorySession/Store、双节点物化与受控执行接缝，换库不能自动解除 Git 耦合；先补团队闭环更短。本轮删除 Workspace 产品实体，B1 先一个 Provider 两实例真实交付，B2 再简启动/SQLite/零 Git/问答/更多 Provider，B3 保留正式故障和发布门禁。不是把旧失败重新计成完成。

两路对实际三稿只读审查合计 1 项 P1、2 项必要 P2：publication:none 下作者可达发布凭据的歧义、预上传输入尚无 Task 的绑定、恢复失败却承诺在线 HTTP 查询。已一次聚合修订，限定复核见[本轮记录](audit-agent-team-service-design-2026-09-07.md#task-first-收缩审计)。仍保留独立验收、受管目录单写、已知结构性失败不原样重试、最小本地保护和持久事实；账号平台后置不等于无保护 HTTP。

ADR 0085 仍 Proposed。本轮只改方案文档，未运行 Agent、修改 .marshal 或发布产品。AGENTS.md 同步被自动审批拒绝，保持原文件并记录待授权事项，不绕过保护。原审计和失败证据全部保留。

## 2026-09-07：旧合同实施形状被误当长期架构

范围：`feat/agent-team-service-blueprint@abc3899` 后续文档审计；两路只读检查分别覆盖 ADR 适用性和当前入口一致性。发现上一轮虽新增服务方案，但旧正文仍用“当前/唯一/禁止/冻结”要求 file-backed、固定 Pi、exact AST、旧切片顺序，并把全部 UI/问答放旧阶段；0085 的 Proposed 与部分入口“冻结”又不一致。根因是只追加新方向、不撤出旧规范入口。

修订：当前[架构](architecture.md)、[Runtime](runtime-architecture.md)、[实施计划](implementation-plan.md)重写为服务目标，原长文同目录归档；[合同适用性](design-contract-map.md)区分目标/接纳/启用/成熟度；19 份 ADR 和四份 Adapter 文档标注旧 profile 范围，0085 扩展精确替代表。README、必读顺序、v1 范围、节点等待、ADR 索引同步修改。0085 保持 Proposed，不擅自追认候选、扩大旧 activation 或修改运行时。

保留独立验证、单写绑定、Worker/Publisher 分权、current owner/lease/CAS、先 intent 后副作用、未知归属不 kill/release、历史字节/失败与正式发布门禁；移除新设计对旧物理布局、函数/文件/AST 和历史排期的依赖。测试要迁移等价行为，不以删测试换通过。文档修订与补充复核见[本轮审计记录](audit-agent-team-service-design-2026-09-07.md#后续专项复核历史合同与当前设计)。这不是新服务已生产可用的结论。

## 既有审计记录

2026-09-07 服务产品方案审计已形成独立[多轮复核记录](audit-agent-team-service-design-2026-09-07.md)：从用户意图、现有实现与反方向事件序列检查，第二轮发现六项 P1（交付消费验收、人工验收出口、跨账本提交取代、旧安装身份续行、Publisher 凭据分权、unknown 用量结算），一次聚合修订到 [ADR 0085](adr/0085-agent-team-service-contract-and-storage.md)、[服务架构](agent-team-service-architecture.md)及[实施 Milestone](agent-team-service-milestones.md)，第三轮状态见该记录。方案坚持一个服务/单权威存储、有限 Agent Team 与单 Worker 回退；设计闭合不等于实机成功，B1/B2 仍 IN_PROGRESS，B3 仍 PLANNED。本文下方错误、rework 和历史证据继续保留。

2026-09-07 一次替代验证 34098369837 仍未完成团队：相同 798ea39，Pi/qwen3.8-flash；service 已 Collect/Verify pass、33 项验收、ReviewPacket，client 却在 `pi-result-provider-terminal` 退出。服务端 109 行 patch 和原 WorkerResult 已保留；报告 outputTokens=25456，并明确称没有 shell、通过静态逐项模拟 oracle 解释结果。不能把 Verify pass 写成独立 Decision/ACCEPTED；不在已结束 runner 上补签。停止模型轮换，后继一次聚合：保留原 transcript 状态机与 providerFailed 判定，仅在闭合时保留已观察的 length/error/aborted 闭集分类；失败仍拒绝，即使携带合法 WorkerResult。任务提示前移当前 no-shell 工具限制，聚焦批准文件、禁止无关全仓探索和模拟整套验收，建议简短 summary；原独立 oracle、预算、scope 与全部门禁不变。终态码只是观察，不证明上游 HTTP 根因，也不自动授权重试；旧失败分母保留。

2026-09-07 PoC 后继 34097645547：798ea39 的 CI 34095940005 五项全绿（Linux 质量 10m32s，macOS 19m28s），同宿主实机仍在独立评审前失败，闭集诊断为 `pi-result-provider-terminal`。RB1 记录两个真实 resume，service Collect 为 2283223 stdout bytes；不是“未配置”“零执行”，也不能据此认定模型仅仅超时。原始终态未归档且诊断合并了 length/error/aborted，形成可观测性缺口；不对未知具体原因造结论。为 PoC 采取一次显式模型替代：34098369837 保持相同代码/任务验收，将已配置的 qwen3.8-max 换为 qwen3.8-flash；所有失败 Attempt 保留，不能合并成同模型证据或称为零 rework。若替代仍失败，停止模型轮换并补齐终态诊断，不循环尝试 Provider 矩阵。

2026-09-07 三节点 PoC 34094155668 未完成：精确候选 cef2723 的 CI 34092330921 五项成功后单次派发；两个 Run journal 均已有 start-outcome，不能按落后的 READY/AttemptsUsed=0 快照计为零执行。Collect 最终返回非 JSON，server 的闭集诊断为 `pi-result-final-content-shape`；未到独立评审、第三节点或 GoalOutcome。原始终态文本不在归档中，具体模型输出未知。源码可复现的缺陷是“多个完整对象”错误未分类，落入 content-shape；ADR 0075 的计数还把业务示例当作第二份结果。按候选 ADR 0084 一次修正 typed framing、重复字段/损坏容器拒绝和精确诊断，并补完整 parser/生产解析反例；没有为旧 Run 补签或自动重试。候选修复未获实机证明，不关闭 B2，不宣称生产可用。

2026-09-07 B2 最终结果接线：按 ADR 0083 在原 RB1 新增一次 completed outcome，验证器在同 current-owner/三个 Run lease 回调中复用原 Decision/Outcome producer 校验；最终结果绑定全部原创建、候选/patch、独立评审与集成派生 base。resident 仅补终态 append，不补派或重跑；`team-reconcile` 在原认证与固定客户端 held ledger readback 后返回完成投影。查询不写，absence 不解释为重新执行，旧 NO_CHANGE 不冒充完成。原预算 digest 仍代表 reservation，实际只声明三个 Attempt 的计量覆盖，未宣称 token/compute 已结算或获得效率收益。store/session/HTTP/客户端回归已编写，本地编译与静态检查不等于实机通过；缺少生产 session 的完整三 ACCEPTED 正向实机、故障注入、局部 replan 仍明确开放。

同轮 CI 成本：`55a435e` 的 34089521238 四项成功，macOS 注释明确 `The job has exceeded the maximum execution time of 20m0s`；不记绿，也不归咎 Worker。只把质量作业及其精确 CI 内容契约同步调到 30 分钟，不删除断言/平台/race。补入前置的 fixedcontrolplane 团队 HTTP 回归，避免新查询调用链再次漏到最后才发现；完整 CI 仍需候选精确 head 证据。

2026-09-07 B2 交付契约前检：参考 integration Task 允许 `no_change`，但 deliverables 只有 code；既有 DecisionImporter 要求 validated diagnostic，故“两个上游本来就可正确组合”反而没有模板承诺的成功出口。这是任务设计错误，不是模型需要重试。新 proposal 把最终代码摘要、入口与示例清单作为明确交付物，保持代码可不变、正常非空交付/独立 Decision；oracle 另行实际验证 HTTP 并核对清单，拒绝漂移、伪造通过、额外/重复字段和不合规输入。不修改旧批准、不放松 no_change 门禁、不增加任务数或 Attempt。输入与 oracle 共 25 项本地回归通过，Go Core preview 的真实 renderer 回归继续由 hosted CI 覆盖；不宣称完成团队交付、生产可用或相对效率收益。

2026-09-07 集成 CI 与相邻收口：`047b170` 的 34087532524 已五项全绿；`10a5edf` 的 34088862794 在两平台目标回归中拒绝合法集成创建，根因是 `json.Marshal(TeamIntegrationBase)` 的字段顺序不是 JCS，而防御性克隆直接交给要求 canonical bytes 的 `decodeTeamRecord`。在克隆前规范化，保留严格 reader；这是作者生产接线返工，不是 Worker 失败，不应以所有负例通过掩盖正例失败。没有为该候选启动付费 Worker，修复动态结果仍待新 head CI。

同批继续业务关键路径：原团队驱动在两个正式 ACCEPTED 后观察 resident 的第三节点，经原 Collect/Verify 形成独立归档；ADR 0082 载体在同一宿主和总等待预算中传输第三份 Decision，不重新启动两个实现、不生成 accept、不刷新预算。12 项团队驱动、44 项既有驱动、17 项载体测试本地通过；仅为客户端/传输组件证据。NO_CHANGE 不伪装 ACCEPTED，团队仍缺耐久 GoalOutcome、局部 replan/reuse 与完整实机证明，B2 不升级。

2026-09-07 集成创建链：按 ADR 0083 补齐原 resident 选择、可信 Git builder、lossless Task 派生、RB1 集成创建域、原物化/恢复及首次 plan gate。只读上游值不直接授予授权：FreezeIntegrationTeamRun 要求专用 current accepted verifier，在同 current owner 和两个上游 Run lease 内重查并追加；记录复用原 reservation/Run ID，Policy 不变。前置调度仅把 ACCEPTED 投影作为候选提示，实际 Prepare/Start 仍读原归档证据；未就绪/冲突不换 ID 重跑。store fixture 正例只证明记录与模板绑定、单次追加/冷重放，不冒充实际接纳或生产集成；还需同 server 第三节点完整 Collect/Verify/Decision 与 GoalOutcome。上一候选 `047b170` 的前置回归已实际通过，旧两轮失败原因未移除。

2026-09-07 聚合回归结果与集成候选：`f97400b` 的 34087007472 暴露了第二层接缝错误：接纳读取错误地寻找根目录 `review-decision.json`，但真实 producer 写入 `decisions/decision-NNN.json`；现改为消费同 round 的归档 Decision/packet，不依赖临时传输文件或当前 packet 别名。另一个测试误以为正常 WriteSnapshot 可以伪造 ACCEPTED，而真实存储提前拒绝；现先断言该拒绝，再只在临时测试仓库注入损坏，验证读取拒绝。所有负例首先证明原正例可读，防止“原本就失败”造成负例假通过。两次 CI/作者返工均保留，B2 没有因此前进到完成。

同时实现 ADR 0083 的私有 index 组合：只对冻结 base 应用两个精确 patch，生成绑定输入摘要、固定作者/时间的 tree/commit，不修改用户 HEAD/index/worktree、不调用签名/hook/filter、不产生匿名可执行文件。测试覆盖相同输入重算、不同批准摘要、保留用户暂存变更、冲突/越界/取消和输出上限。当前仍是待接到耐久集成创建的 Git 数据操作，不能单独算业务集成；本地只有编译/静态验证，动态回归交由后继 CI。

2026-09-07 候选 CI 纠偏：`5753ca3` 的 34086700262 在 Linux/macOS 的新 review 正例构造失败，原因是本次测试把部分读取模型 `domain.TaskSpec` 重新序列化，令原本省略的 deliverable `mediaType` 变成 Schema 不允许的空串；不是 Worker 失败，也没有执行到接纳读取断言。修正为保留原完整 JSON、仅替换锁定 base，并在 fixture 构造时立即校验 Schema。该成本计一次作者/测试返工，不能把多个同源测试失败计成多个独立业务失败。前置回归改为串行运行全部目标包、聚合退出失败，避免第一个包失败掩盖后续接缝、下一轮才发现；不增加并行负载、不降低失败门禁。动态修复结果仍待后继 CI。

## 2026-09-07：集成输入从已接纳证据读取，不从工作分支取最新值

按 ADR 0083 增加 `RepositorySession.ReadAcceptedTeamInputs`：只接受原 Goal/集成节点/plan fact 选择器，在当前 owner 下同时持有两个原 Run lease，核对创建绑定、原 Task、最终 review.accept 事件、Decision/packet/report/manifest 与 Core Outcome producer，再核对 candidate detached identity、namespace/base/Attempt 及实际 patch。返回值只是只读快照，不是创建授权；后继冻结和 Start 必须重查。占用或未接纳是等待，不触发 Prepare/Worker；损坏、halt 或 owner 失效不猜测重试。

此候选尚未接入 resident 集成创建，不能算 B2 INTEGRATED。组件回归使用原 PacketBuilder、DecisionImporter、PrepareRecords 和 Outcome producer 构造正例，另检查逐个缺失/漂移输入、合法 JSON 内容漂移、真实 Run lease 占用及伪造快照标签；本地仅编译/静态检查，动态结果待 CI。将 review 包 race 提到团队前置步骤，并同步封闭 CI 内容契约，避免遗漏新的调用链接缝。

效率记录：上一候选的 CI 派发曾误用 SHA 作为 workflow ref，API 422、没有创建作业；改用已核对远端 SHA 的分支后创建 34085122738，该作业已在精确 `0f3a48e34effb2b74d2d399d3da1d39869d2f779` 上五项全绿。e4016f9 的旧 CI 34083970829 被同分支并发规则取消，不能计为全绿；本次已等当前在途完成再派同分支新 CI，避免浪费尾部作业。上述操作成本不归咎 Worker，也不从总交付成本中删除。

## 2026-09-07：补齐团队同宿主独立 Decision 传输断点

首轮 hosted 团队在 REVIEW_PENDING 后退出；离线审查包不能恢复原宿主 current-ledger 权威，故不能补签原 Run ACCEPTED。按 ADR 0082 的封闭扩展，在原 server 内为 service/client 分别传送已有 ReviewDecision，节点精确身份/packet/binary/source 绑定不变；统一等待预算，先到先处理，已证明的 reject 不阻止另一节点 Decision，未知 mutation 仍停止且不重试。验证 fail 仅允许独立 reject/rework，不允许 accept。即使两个实现都 ACCEPTED，也明确没有 integration/Goal Outcome，团队 accepted=false。

本轮没有新付费 Worker、没有手改 `.marshal` 或跨 runner 导入 authority。核对实际 DecisionImporter 调用链时同步纠正旧驱动把 rework 后状态误写为 `RETRY_PENDING` 的错误：使用真实 `REWORK_REQUESTED`，非终态无 Outcome，仍不触发 Attempt。9 项团队驱动、44 项生命周期驱动、13 项载体测试及原脚本检查通过；这是可执行的候选入口，不是团队正式接纳证据。下一步仍须局部 replan/reuse、接纳上游的集成与 server 自主推进；不以又一次全团队运行代替缺失的恢复/复用能力。

## 2026-09-07：真实双节点完成原验收，独立审查仍发现业务缺陷

`7a4f7d0` 的 CI 34081513199 五项通过；实机 34082574786 完成同 server 批准→两个真实 Pi 节点各一次 Attempt→Collect/Verify/REVIEW_PENDING，66 条 RB1 摘要/序号与两个审查包的各八份文件绑定已检查。没有逐节点外部 Start，没有 integration、正式 Decision 或 Goal Outcome，进程重叠尚未独立证明。详见[精确候选业务审查与效率复盘](audit-b2-first-team-2026-09-07.md)。

旧 oracle 均 pass，但客户端真实 HTTP 反例暴露 P1 响应结构未校验及 P2 空 userinfo 接受。已将两类问题同批前移至 oracle（组合 33 项、客户端 10 项；15 个回归测试），并澄清原客户端提示；服务候选复查通过，旧客户端被拒绝。这只是审查诊断，不改写旧报告或签发 ACCEPTED。保留服务成果，不原样重跑团队；既有预算 rework=0，后继必须走正式 Decision/局部 replan 与复用，不能靠换 Goal 清零成本。当前 adapter usage 报告很高，未审计去重/计费口径，也无同条件基线比较；一次 Attempt 不能冒充零返工或效率收益。B2 仍未完成。

## 2026-09-07：团队已启动首节点，暴露晚期文本拒绝与需求静默丢失

`5ca49bc` 的 [CI 34080540487](https://github.com/chiga0/marshal-harness/actions/runs/34080540487) 前置 planning/store/session 回归通过，但 Linux 全仓 quality 暴露新增 CLI 跨链测试的另一处夹具错误：手写了不存在的 environment-binding v1，既有 Policy Schema 要求 `marshal.local-dogfood-environment-binding.v2`。这不是 Worker 失败或生产 Schema 变更；该候选实机不派发。修正为正式 `LocalDogfoodEnvironmentBinding` 类型与版本常量，先对 renderer 的全部 Task/Policy 做逐节点 Schema 诊断，再经过原完整 Core preview/launch builder；不跳过任何检查。将整个 CLI 包的 race 回归加入前置团队步骤，避免新公共入口测试漏出前置范围、等全仓结束才发现。此第二次夹具返工计入来源修复与总耗时，前置回归通过不能再表述为完整跨调用链通过；动态新 head 证据仍待验证。

后继 `283b19b` 的 CI [34080167668](https://github.com/chiga0/marshal-harness/actions/runs/34080167668) 在两平台前置团队回归失败，实机未派发。定位到 store/session 测试夹具把 work.context 写成字符串，而已有 Schema 要求字符串数组；旧 typed Task 忽略该字段曾掩盖错误。修正所有同类夹具为数组，变更/伪造反例也保持合法形状，以继续验证真实内容绑定而不是意外依赖类型错误。生产解码与门禁不回退。此来源返工计入交付成本，新提交仍需独立动态结果。

`b8dbf3c` 的 [CI 34078286247](https://github.com/chiga0/marshal-harness/actions/runs/34078286247) 五项全绿。随后条件派发漏填必需的 expected-head，GitHub 返回 HTTP 422，未创建作业；补齐参数并确认无同版本作业后才派发 [34079333520](https://github.com/chiga0/marshal-harness/actions/runs/34079333520)。这次操作错误计入人工介入与总耗时，不归咎 Worker，也不增加虚构 Run。

实机失败诊断 artifact `10003206471`：26 条 RB1 canonical record 的摘要及全局序号均已核对，包含一个 approved plan、两个创建冻结、两个 reservation/open 和两个 worktree bind receipt。服务节点有 process-started、Resume outcome 及公开 Inspect 的 RUNNING/sequence=3；客户端停在 bind receipt 后、launch-authorized 前，原计划追加 client/start halt。磁盘 state.json 仍为 READY 不能覆盖 journal/公开 Inspect。最后 fixed-cli-response-timeout 与 sealed-run-acquire-lease 错误仍需同路径复核，不能声称查询/失败闭环已通过。

确定性根因：客户端 objective 的 `POST /quote` 命中 ADR 0075 的原绝对 POSIX token 检查，实际 reseal launch 才检查，故浪费了已创建 Run/reservation。同期发现第二处业务正确性缺口：Task Schema 已有 work.context，但 domain.TaskWork 未声明该字段；ParseTaskSpec 静默丢弃 context，Pi builder 也仅转发 objective/constraints。服务节点未收到共享的价格/HTTP 错误契约，不能把它成功启动当作可正确交付。

候选按 ADR 0083 同批处理：typed Task 保留既有 context，builder 传递完整 context/nonGoals 且沿用原路径/NUL/argv 长度检查；参考路由用完整 loopback URL 表达，不放宽路径门禁。服务器在完整 preview 后、批准 append 前，对全部三个节点调用实际纯 builder，以最大合法 Attempt ID 长度预检；实际 Start 仍绑定真实预留身份，不复用占位身份或重写历史证据。跨语言回归覆盖真实 renderer→Core preview→同一 production builder，检查完整契约出现并拒绝任一节点的绝对路径、控制路径与超长上下文；另检查空 objective 不能被 context 补成合法任务。现有 Schema 未扩张。

本次保留两次失败实机、一次派发拒绝及所有来源修复，不能报告“零返工/无失败”。本地脚本/格式/架构、双平台 compile-only、vet/staticcheck 检查不等于动态通过；后继须自己的精确 CI，再做一次团队验证。自动结果处理、独立 Decision、集成/Goal Outcome、局部 replan 与对照收益均未完成；B2 不升级。

## 2026-09-07：首次团队实机在 CLI 准入短路，补生产入口回归

`a481f0e` 的精确 CI [34076598876](https://github.com/chiga0/marshal-harness/actions/runs/34076598876) 五项通过后，首次 `order-quote-team` 实机 [34077560755](https://github.com/chiga0/marshal-harness/actions/runs/34077560755) 失败。诊断 artifact `10002598290` 显示 server ready，但第一次 `team-approve` 返回 exit=3、空 stdout；stderr SHA-256 `bb8d1e32fe9bfd6c9b829425e953f6649875f6b436c9a56893a8dec7176fa5e7` 精确匹配封闭 `self-local-command-denied`。RB1 仅一个 `control-owner-acquired`，未创建 Run/Attempt，无付费 Worker 重试；失败计入整个交付分母，不用零 rework 粉饰入口错误。

根因是新团队 handler/HTTP route 已接线，但顶层 CLI classifier 与 activation 的封闭命令集合未接通。输入生成器到 Go preview、模拟客户端以及单独 handler 测试均未经过这个真实门禁；这是生产入口覆盖缺口，不是模型配置、Pi 或 resident server 失联。沿 ADR 0083 明确的权限边界，同批修复命令分类、activation 生成/解码、Schema 与实际 `RunContext` 回归；测试使用新生成且绑定测试 executable/source 的 activation，不走 unprofiled bypass，并覆盖缺授权及未知团队命令拒绝。旧授权不得原地扩权。

本次纠偏不扩大 B2 范围、不补发原失败实验。新精确 head 必须先通过动态回归，再运行一次团队实机；自动 Collect/独立接纳/集成与 Goal Outcome 仍待完成，B1/B2 保持 IN_PROGRESS，B3 PLANNED，无新增 main merge/stable 或对照收益证明。

本地脚本回归、architecture/format、双平台 compile-only/vet/staticcheck、JSON 语法、diff 与 secret scan 已通过；本机 Python 无 jsonschema，Draft 2020-12 与实际 activation 示例验证由现有 Go Schema 测试在 hosted CI 执行。本地 `-exec /usr/bin/true` 只证明编译，不能替代这些动态检查，也没有运行匿名 Go 测试二进制。

## 2026-09-07：团队批准接入 hosted fixed-server 实机路径

在 `4867ff7` 完整输入/业务 oracle 候选之上，新增 `order-quote-team` 显式场景，复用同一个 exact-head CI gate、固定 binary 与 Pi 0.84.4 配置，不另建 server 或生产业务状态库。该模式跳过单任务 `task plan/approve`，只向认证公开入口发送一次原始 `team-approve`；resident Core 自行物化并 Start 两个 implement，客户端只有有界 Inspect 与既有 Collect/Verify/ReviewPacket。未知响应、终态或超时保留失败，不重新批准、不启动替代 Run、不自动 rework。

两条 RUNNING 投影只作为进入结果收集的前提，明确 `processOverlapProven:false`；真实重叠须后续审计原 events/RB1 的进程起止，不能从状态名推导。两节点均须业务验证通过并保留 ReviewPacket/完整 review inputs；integration 在独立接纳前不得出现，脚本不会伪造 Decision、集成成果或 Goal Outcome。成功只表示 `two-implement-review-pending`，并非 B2 完成、生产启用或收益优于 Lead＋SubAgents。

本地新客户端的 5 项确定性回归覆盖精确批准摘要绑定、仅 Inspect 等待、缺节点超时、失败不重试、未批准集成拒绝，以及一次批准复用两个既有结果驱动的完整客户端构造。既有脚本/业务 oracle 回归通过。该路径尚未真实执行；须当前精确提交 CI 通过后只派一次无故障团队 canary，再基于证据接通独立接纳与集成，不扩大 Provider 或故障矩阵。

## 2026-09-07：既有团队业务样例进入同一候选，补完整输入到 Core 的回归

B1 修复已正常整合并推送为 B2 候选 `2ecf8b1`。随后整合已有参考契约/HTTP oracle 分支 `964cba4`，新增待确认的完整三节点输入生成器，复用 B1 Task/Policy 构造，避免再维护一份不一致的 Provider 配置。生成器不批准、不创建 Run 或启动 Agent；摘要由实际 Go application/planning parser 再校验，新增跨语言回归直接运行真实 Python 生成器再执行 `Frozen/PreviewTeamInputs`，不是两套 fixture 各自自洽。

同批把 oracle 接为有界 verification command 的服务、客户端及组合入口；服务生命周期接口先冻结，客户端必须实际消费 HTTP 响应，integration 才检查两个候选组合。新增真实 loopback component 调用及候选 early-exit/异常/缺文件/symlink 拒绝；不输出异常正文、不把同进程候选导入描述为恶意代码隔离。4 项输入脚本、11 项团队 oracle 和 5 项原业务回归本地通过，Go 编译/vet/staticcheck 通过；Go 动态跨语言解析及整合 head 仍需精确 CI。尚未派真实 B2 团队，后继应直接把该输入接入 hosted fixed-server canary，不扩展第二种业务控制器。

## 2026-09-07：同 server 长验证与另一 Run 自动停止组合通过，B2 同步修复依赖

B2 `00c8351c078fc505fa578d8db590dda9a790fc90` 的 CI 34067600918 已五项通过，包含耐久 halt 与 resident 初始调度动态回归。下面的 B1 合并仍是新的候选 head，父提交成功不代替合并验证；尚无真实 B2 团队交付。

`4ace42c476a3d683f7456ac08312ea4a5cc8c944` 的精确 CI 34066636760 五项通过后，仅派发一次真实 Pi 组合验证 [34067556449](https://github.com/chiga0/marshal-harness/actions/runs/34067556449)，作业成功。小型诊断 artifact `9999466068` 的 62 条 RB1 fact 已重新核对 canonical digest；只有一个 owner acquisition、两个 Run 各一 Attempt、零 operational retry/rework。15 次 Inspect 均 exit=0；停止后的 Collect exit=1 是经认证的 `disposition:stopped/reasonCode:run-stopped`，不是传输失败。

peer 的业务 oracle 与长验证均 pass，总 Verify 104.434435 秒，最终 REVIEW_PENDING；另一个 Run 的原始 Attempt deadline 为 23:43:17.165934Z，停止意图延迟 0.180249 秒、BLOCKED 终态延迟 5.532006 秒，均处于长验证区间。已从原 Task/首条创建事件/process-started fact 复算 60 秒 Attempt 与 600 秒 Run deadline，核对 stop intent、terminal barrier、Run event 与 Outcome 引用，不把客户端 summary 中的 `deadlineWitnessVerified:false` 当证明。该样本无重启、无独立 Decision/ACCEPTED，不替代此前冷恢复证据或完整 B3 矩阵。

本次关闭候选的“长 Verify 不阻断其他 Run 自动停止、终态查询及 stopped Collect”组合子条件；不宣称旧 Pi 缺 content 故障已根治，也不删除此前失败分母。B1 尚需最终审查/主线合入与组合确认，B2 尚无团队交付。为避免下次 B2 实机仍携带已知握手等待缺陷，将 B1 `80084bb` 和 `4ace42c` 正常合入 B2 候选；无业务代码冲突，文档保留两侧历史并以当前表为准。该候选合并不是 main merge，父提交 CI 不冒充合并 head 的精确动态证据；不取消在途 `00c8351` 调度 CI，不为单纯文档或每次查询重复派 CI/Pi。

## 2026-09-07：从批准账本接到 resident 的实际 Start 路径

`aa230da` 的精确 CI 34066292363 已五项成功。本轮将耐久停派与初始调度接入 fixed server：单独 timer、同一 router mutation lane、同一应用写锁，每次选取一个原始 implement，复用原 Materialize、Inspect sequence/head 与实际 StartRun。没有 CLI 子进程协调器、内存批准或另建状态库；同时运行的 Worker 不持全局写锁。

自动派发按仓库 busy 容量 2 与原 Goal 更低上限取最小值。计入执行、重试待定、验证/审核、rework 与发布中；非团队 busy 或多个 busy Goal 时不新增，单个 busy Goal 优先继续其已批准的独立节点。该上限只约束自动派发，不宣称新增了全局人工 Start 配额或自适应内存/CPU 调度。集成依赖尚未接通，不提前执行 integration，不自动 rework。

完整调用链检查在提交前发现：Verify 合法持有 Run lease 时，调度器不能把 `ErrLeaseHeld` 当永久结构性失败。候选明确把它作为本轮容量不可判定→不派发；只读观察被取消也不触发永久停派。读取损坏/未知则停本进程派发；物化、Inspect 或 Start 失败只提交一次原计划 halt，halt 提交未决也停本进程派发。既有 deadline timer 和查询不受该本地开关禁用，shutdown 同时等待两个 timer 退出后再释放 owner。

新增 current-ledger 冷选择/READY 复用/停派、合法 lease、容量/审核队列/非团队占用/更低 Goal 上限/陈旧 READY/不提前集成的回归；纯策略输入明确标为合成投影，不作为实机证据。原 Start 的实现抽为同一持锁函数供公开入口与 controller 共用，未复制执行语义。Darwin/Linux compile-only、vet、Darwin staticcheck、架构和 diff 检查通过；halt/调度组合仍待最新精确 head 动态 CI。B1 精确分支 CI 34066636760 已五项通过，并经 candidate-ci-gate 派发一次带新诊断的真实 Pi 组合验证 34067556449，尚无结果；B2 尚缺自动 Collect/独立接纳/集成/Goal Outcome/暂停与 replan 及真实团队，因此状态不升级。

## 2026-09-07：自动调度前先接通耐久停派，不把心跳变成重试器

前驱 `fc5d479` 的精确 CI 34065300476 已五项成功；批准后无原 HTTP 请求续行的 `aa230da` 正由 34066292363 动态验证。本轮发现当前创建接口虽单次返回错误，但若直接挂入 tick，失败会被下轮再次调用，冷重开也没有停派依据。候选依 ADR 0083 在同 RB1 增加封闭 `team-plan-halted` 记录，绑定原 plan、节点、阶段与 current owner；原原因不可覆盖、exact replay 不追加、不改变原计划/预算。Prepare/Freeze 与原首次 Start gate 检查 halt；被阻止的成员不回退到普通人工批准。

新增真实 DurableStore 冷重开/幂等、未知字段值/节点/旧 plan/拒绝 verifier/取消拒绝，以及重新计算 hash 后的 stage/node/plan/owner 伪造和重复 fact 回归；session 夹具验证冷重开后两种物化入口及首次 Start 都不能绕过停派。停派不是取消或成功：原 READY 创建仍可修复，Attempt 仍为零，预算不退回。上述均不冒充真实 Worker 或最终业务验收。

本轮尚未开启自动调度。后继需把一次有容量的启动、失败停派提交与提交未决时本进程停止派发一起接通；不能仅依据新增记录就宣称无人值守已安全。halt 提交前崩溃、原 Start 丢响应/复用及显式 replan 仍需同链路验证。本地 Darwin/Linux 编译及 vet、Darwin staticcheck 通过，动态测试待后继精确 head；B1/B2 状态不升级。

效率证据：B1 `4ace42c` 的 PR CI 34065812558 已五项通过，但现有 canary gate 仅接纳 workflow_dispatch 的精确分支证据，因此又启动 34066636760，尚未重复 Pi 实机。该双重 CI 会增加总等待；优化应验证 PR 合成提交与 sourceHead 的等价范围、保留分支特有回归后统一 gate，不能直接把任何绿色检查当作放行。本轮仍优先推进业务控制链，不额外拆出 CI 清理切片。

## 2026-09-07：批准提交后无需原 HTTP 请求即可继续创建

完整调用链复盘发现，原节点 Prepare 接缝依赖 `ApproveInitialTeamRequest`，而 RB1 批准保存的是请求摘要而非可重新构造的原 HTTP request ID/deadline。若批准成功后客户端丢失请求、server 在首次冻结之前重启，仅恢复已冻结义务无法让这个节点继续。候选新增从 current owner/RB1 按 Goal/Node/精确 PlanFactDigest 定位的内部物化入口，并与原请求入口共享全部 preflight、Prepare、冻结与创建恢复逻辑；不重建 HTTP 请求、不追加另一批准或预算，不改变已有持久化格式。

冷 session 夹具覆盖批准前拒绝、批准后尚未冻结直接续行、再次冷重开不重复 Prepare、陈旧摘要/未知节点/提前集成/取消拒绝；另外覆盖 preflight、Prepare、materializer 三处失败单次返回，原计划和预算义务不变，已冻结值保留。该入口尚未接到自动调度，不宣称客户端已经可以完成团队交付；后继必须一次处理容量、失败止损、Start 事实与两实现并行，不给每秒 tick 加上无界重试。

前驱创建恢复 `2486b1c` 的 CI 34064463717 已五项通过；`fc5d479` 的 CI 34065300476 当前四项通过，macOS quality 仍运行。本候选本地只编译及静态检查，不计动态通过。B1 修复分支已推送 `4ace42c` 的缺 content 诊断，PR CI 34065812558 在途；旧 Pi 实机失败未被抹去。B1/B2 保持 IN_PROGRESS，尚无完整团队交付或对照收益。

## 2026-09-07：团队批准派生首次子 Run 执行门禁，不复制人工审批状态

后继 `71702fc` 的启动恢复接线已推送。完整 Start 调用链检查发现，原单 Run plan gate 尚不识别 RB1 团队批准；本轮把首次 implement READY 的批准从当前 owner 下的原 approved plan/creation 直接派生，核对精确 sequence/head、原准备时间、Task/Policy/Capability bytes 与摘要后，仍进入原 StartRun、reservation 和 launch CAS。没有伪造 human actor、生成额外 ApprovalRecord 或新增持久化协议。成员查找以已批准计划为准；已批准但尚未冻结的节点不能误判为普通 Run 再走人工 fallback。非团队 Run 保留原 gate，团队错误一律拒绝。

补充 session 冷重开、精确首次 READY、无创建/无冻结、head/sequence/Policy 漂移、取消和提前 integration 的回归；复用明确的 RunStore fixture，不声称真实 Pi 启动或团队交付。前驱 `2486b1c` CI 34064463717 记录时四项通过、macOS quality 在途；本轮编译/静态验证仍不代替后继精确 head 的动态 CI。B2 仍缺调度/Start 事实衔接、真实两实现加集成、独立业务验收与有界暂停/replan；B1 格式/组合验收阻塞未被此变更关闭，不能因候选变多宣称收益已经成立。

## 2026-09-07：恢复顺序由“先要求完整 Run”改为“先履行原创建义务”

创建恢复候选 `2486b1c8a44fafc048abcaded2c2eaa1c718fc7d` 已推送，[CI 34064463717](https://github.com/chiga0/marshal-harness/actions/runs/34064463717) 在途；它包含前驱 macOS canonical path 修正，不能提前宣称动态通过。本次沿完整启动调用链补接：resident 在普通 Run 扫描前，以同一 held RB1 枚举 scope 内原 plan/creation；从耐久批准恢复原创建，不依赖已丢失的原 HTTP 请求，不重新 Probe/批准/追加预算。未冻结节点仍不自动准备，已执行 Run 核对完整 authority 与原输入后交原恢复路径，不重置。

新增 store 冷重放/跨 scope 零泄漏及 session 无原请求、部分 Run、原 READY、缺失配置、占用、损坏、取消、已前进 Run 与输入漂移测试。session 使用明确的无 Pi fixture；实际 fixed server 构造已接线，但完整 server 冷启动、真实双 Worker 与业务集成仍需实机验证。该增量未增加协议、未新开付费 Run，避免为了同一启动缺口再产生后继 Run；也不能用新增测试数冲抵此前失败。B1/B2 仍 IN_PROGRESS，B3 PLANNED，不升级 INTEGRATED 或 production。下方“下一步启动接线”的叙述保留为前驱检查点。

## 2026-09-07：同一 Run 的创建恢复接入 fixed server 构造候选

前驱 `928b8ab` 的 CI 34062410465 最终五项通过。`a614acb126b35a82b213f4c1401b1cf0ba5bfba4` 的 [CI 34063169000](https://github.com/chiga0/marshal-harness/actions/runs/34063169000) 四项通过、macOS 前置 planning 失败：冷恢复正向测试返回 invalid frozen preparation。核对 Prepare/Restore 路径发现，前者冻结 canonical repository，后者却直接比较原路径字符串；macOS /var→/private/var 导致同仓库误拒绝。改用同一规范路径校验并补跨平台 symlink alias 正例，不改 frozen repository 或重选输入。该失败计入工程返工，未派付费 Worker；此前 compile-only 仍不代表动态成功。

本轮新增同一 PreparedPlan 的 ReconcileCreation，与原 Plan 共用两条 creation transition producer。恢复核对原 planning 事件、完整快照及三份冻结文件，只补缺失项；零至两条 journal、缺失/落后 snapshot、文件写入后 owner 失效均沿原 Run 接续，不删除目录、不重新 Probe、不加 Attempt。worktree 恢复持 task flock，复用原 base/branch/clean directory，并处理进程锁已释放但 Git 管理锁遗留、仅原分支已创建的中断；脏内容、foreign lock、分支或 HEAD 漂移保留并拒绝。FIFO 读取改用 nonblock 再检查普通文件，避免恢复挂死在 open。

同批把恢复接到真实 RepositorySession/固定 CLI 构造：先读批准和原创建 fact，owner/RB1 guard 包围短写入，Git/probe 不占 owner 锁；返回 READY 前从会话持有的 RunStore 复核状态与冻结输入。server 在冻结 StateRoot 前准备容器，避免首次物化自己改变 parent identity。新增真实 Git/RunStore 边界测试、held session 冷 owner 重用与伪造结果拒绝；session 的 materializer 为明确 fixture，不冒充实际 Pi/完整 HTTP 证据。

候选本地 compile-only、静态与架构检查不等于动态恢复通过，须精确新 head CI。调用链复核还发现 resident startup 先逐个要求完整 Run-start authority，不能直接跨过部分 planning Run；下一步须从 RB1 枚举原创建义务，先恢复再扫描，而不是跳过错误或要求新批准。当前测试的冷 owner 重开只覆盖 RepositorySession，不代表完整 server 冷启动已通。仍缺该启动接线、Core Goal→Run plan approval、生产调度入口、真实并行与集成验收及 B3 故障/发布门禁；不合并或升级 B2 INTEGRATED，不以这批代码关闭 B1 的格式/组合验收阻塞。

## 2026-09-07：沿原 planning 调用链恢复冻结输入，不重新探测 Provider

创建冻结候选 `928b8ab3e72e5867009e6ef0071d50ee0d751034` 的 [CI 34062410465](https://github.com/chiga0/marshal-harness/actions/runs/34062410465) 已通过 Linux quality、双架构 Linux conformance 与 secret scan；记录时 macOS quality 仍在运行，尚不能声称五项全绿。它已越过前驱 immutable-SHA fixture 的早期失败；旧失败继续计入工程返工分母。

本次沿同一 Prepare 实现新增冻结输入重建：保留原始完整 JSON、选定能力与准备时间，重新检查当前环境/仓库/remote/准入，production selector 仍执行当前 registry eligibility/admission，但不重新 Probe 或 fallback。序列化后冷重建到原 Create 的回归同时核对 READY、两个原时间事件、三份冻结文件和原 base；另覆盖输入/时间/能力/remote/adapter 漂移、取消和无配置零 Run，已有 Run 仍拒绝覆盖。测试使用显式 fixture，不冒充真实 Pi 或 RB1 批准。

接线审计发现原模板允许只声明 remote 名字：重启时该名字可能已指向另一 URL。现要求受限团队在 preview 前提供非空 expectedRemoteUrl，恢复复用原 ResolveRemote 核对；不增加持久化字段或放宽单 Run 的既有合同。此问题在 Worker 启动前确定性处理，不启动付费重试。

当前未完成项仍是：当前 owner/RB1 到实际物化入口的写入复查、已有 CREATED/PLANNED/READY 的幂等补齐、Core plan approval、真实并行与业务集成。这个函数本身不认证 receipt、不自动创建或启动 Run。B2 仍是隔离候选而非 INTEGRATED；本次本地编译/静态验证不替代新 head 的动态 CI。B1 原有实机结果格式失败和 B3 门禁不被这项进展关闭。

## 2026-09-07：实现节点冻结义务接到同账本与固定 server 准备器

`99ca8ca53742f001edef8fbfe7b6c9aebfb1473b` 的 [CI 34060885444](https://github.com/chiga0/marshal-harness/actions/runs/34060885444) 五项成功。后继 `091f2ae66b0b439489345e1dbe8bf87c1d99f068` 的 [CI 34061770534](https://github.com/chiga0/marshal-harness/actions/runs/34061770534) 失败：新 fixture 错把 HEAD 当成合法 base，原 ResolveBase 在 probe 前正确拒绝；两平台都在前置 planning/race 结束，未进入完整质量检查，也没有派 Pi。修正为真实不可变 SHA，保留仓库 HEAD 前移时使用原 SHA 的测试，另补 mutable ref 必须零 probe 拒绝。该错误计入工程返工；不能把 compile-only 当成行为验证，也不能只读调用点而遗漏既有 ResolveBase 合同。修正与同链路冻结实现一次验证，不为修 fixture 原样启动付费 Worker。

后继实现将实际 PreparedInputs 以单条创建冻结 fact 接入同一 RB1；重放重新核对已批准模板、确定性身份、base 和 Pi 选择，集成/有依赖节点拒绝提前冻结。same key 的新时间、能力快照或输入均拒绝，不追加第二份 reservation。fixed server 安装不可被请求替换的实际 planning 准备器；会话在 probe 前读取原批准/已有冻结事实，已有值冷重放零 probe，未命中在 owner 锁外准备、提交时复查 owner。没有直接启动 Worker、新增状态库或修改旧 fact。

store 回归覆盖一次追加、冷 owner successor、并发 exact 重放、未批准/错误模板与提前集成/伪造 RunID/重复 fact；真实 held session 回归覆盖批准前零准备、冷恢复不重新探测和失败零创建事实。测试中的模板/probe 为明确 fixture，不冒充真实 Pi；实际构造接线仍需整条 server 调用链验证。当前仅完成本地 compile-only/静态检查，新候选动态结果待 CI；Run 创建到 READY 恢复、Core plan approval、真实并行及集成仍待完成，B2 不升级。B1 的结果格式实机失败保留，不以冻结义务替代其修复。

## 2026-09-07：批准会话 CI 通过，单 Run 创建分离出冻结阶段

`5811572252c9fc80d714528770636bd9156d31dd` 的 [CI 34059842649](https://github.com/chiga0/marshal-harness/actions/runs/34059842649) 五项成功。条件链随后仅派发 `99ca8ca53742f001edef8fbfe7b6c9aebfb1473b` 的 [CI 34060885444](https://github.com/chiga0/marshal-harness/actions/runs/34060885444)，认证批准入口动态结果仍在验证；没有新派 Pi。

现有 Plan 将验证/probe 与 Run 写入揉在一起，无法先把实际选中输入交给同账本创建事务。候选把原生产 Plan 改为同一 `Prepare → Create`，冻结完整原始协议而非有损领域读模型；预检成功之前及冻结之后尚未 Create 时均不写 Run。创建使用原 base 和 capability，不重新探测，并重查 repository/remote/适配器身份。新增测试检验 caller buffer/返回投影篡改、可变 HEAD 前移、取消/漂移零创建和重复创建不覆盖原 journal。新增 B2 前置 planning/race 回归，尽早发现本次调用链错误，完整质量门禁保留。

本次仍仅是幂等物化的必要接缝，不提供冷恢复或 Goal 创建授权；耐久创建绑定、部分 Run 补齐、真实并行节点及集成尚未完成。静态与 compile-only 检查不冒充动态通过，B1/B2/B3 不升级、main 未合并、没有 stable 发布。

## 2026-09-07：B2 批准从固定 CLI 接到认证 HTTP 与原事实查询

在 `5811572` 的会话接缝上继续接通 `team-approve/team-reconcile → authenticated fixed HTTP → 同一 sealed application/RepositorySession → RB1`，没有单独 controller、Worker launcher 或第二批准库。批准输入增加原 canonical UTC deadline 并纳入 request digest；批准与 transport 的 key/deadline 必须相同，查询允许原 deadline 过期但不改写。服务端写后重读 exact fact，客户端在 fixed peer post-check 后用 held read-only ledger 再查 owner/原请求/原 fact；未知提交不自动执行第二次批准，伪造投影与错误“不存在”均拒绝。

回归沿实际 HTTP framing/认证后 router 覆盖读写、key/body/deadline 绑定、未知字段、能力缺失、提交未知和只读查询；实际 held owner/RB1 fixture 覆盖冷重放、客户端只读回查、伪造 fact/错误 absence/owner successor 拒绝。HTTP 的应用对象及 session 的模板预检仍是明确替身，不能把两组测试拼成已经执行真实完整 server/Schema 的证据。另覆盖 CLI 有界常规文件、符号链接/FIFO/未知/重复字段与过期批准在连接前拒绝。当前只完成本地 compile-only/静态验证，本次动态测试待精确 CI，不派付费 Pi。

剩余关键路径是 Run 创建义务到 READY 的幂等物化、完整固定 server 批准实机、实际并行节点及集成交付，B2 不升级 INTEGRATED。B1 最新失败仍按上条完整记录保留，不借批准接口实现回避其结果格式及并发验收缺口。

## 2026-09-07：B2 同账本测试通过，接入真实 owner 会话；B1 新实机未进入并发场景

耐久接纳候选 `ab43a4220e8863d8fe88c4c8c7b0c94f55945312` 的 [CI 34058800862](https://github.com/chiga0/marshal-harness/actions/runs/34058800862) 五项全部成功。后继候选将完整输入预检安装在 fixed server 构造入口，新增封闭批准请求和现有 RepositorySession 接缝：先冻结请求/校验完整输入，再持真实 owner 锁重查 held root/current RB1 owner，追加原有原子 fact。没有让账本反向依赖 planning、暴露 raw store 或新建控制进程。

新增测试覆盖请求摘要/内容冻结、构造缺少校验时拒绝、校验拒绝/篡改/取消零追加、实际 held owner 下同请求重放和冷 owner successor 复用原批准。session 测试的节点正文与 preflight 是明确替身，不冒充完整 HTTP 用户批准；完整 Schema/Policy 校验仍由实际 CLI 构造绑定。团队 route、响应丢失查询、Run 物化与真实集成交付尚未接通，B2 保持 IN_PROGRESS/未集成。这一后继仅完成本地 compile-only、vet/staticcheck/架构检查，动态测试待新 head CI。

B1 `80084bb` 的 [CI 34058120709](https://github.com/chiga0/marshal-harness/actions/runs/34058120709) 五项成功；条件链派发的单次实机 [34059061091](https://github.com/chiga0/marshal-harness/actions/runs/34059061091) 失败。peer 的第 13 次调用（Collect）exit=1、空 stdout，server 记录 `pi-result-final-content-shape`；peer Run event 仅到 RUNNING/3，主 Run 尚未 Start，未产生长 Verify/第二 Run 并发证据。小包 `9996906939` 和完整包 `9996907539` 都没有保存失败 terminal content 的结构材料，故不能据错误码推定具体字段形态，也不能评价新握手修复的实机效果。保留失败分母，不放宽解析、不原样派第三次同类付费重试；后继需把该诊断缺口纳入同一次真实调用链验证，而非另起无关清理。

本轮 main 未合并、无 stable 发布。目标继续围绕 B1→B2→B3 与业务配对收益验证，不因组件 CI 全绿改写产品完成状态。

## 2026-09-07：B2 输入预检动态通过，耐久接纳接入同一 RB1 候选

`c59c0aa97fe165cfb298fabd8c9f052be6234b7a` 的精确 CI [34057248679](https://github.com/chiga0/marshal-harness/actions/runs/34057248679) 五项全部成功；包括此前有损 Task fixture 修正和整个 Goal 预算绑定。没有因此新派 Pi 或宣称团队交付完成。

后继候选沿 ADR 0083 向既有 `result-ingress.jsonl` 接入一个 `team-plan-accepted` 原子 record，完整输入、accepted revision、预算 reservation 与确定性创建义务同时追加/fsync。所有 projection 构造和冷重放入口一起更新；接纳前重读真实 owner/Goal 历史，已存在的 Goal 不通过初始入口重置预算。exact 重放在 owner successor 后仍返回原 fact/RunID，不多 reserve 或另造 Run；错误请求、预算不足和缺少批准 verifier 均零追加。

测试覆盖一次完整 append、冷重放与 owner 更替、过期 owner 拒绝、同批准并发只提交一次、错误批准/超预算/同 Goal 异方案拒绝，以及重算 digest 后仍拒绝伪造 RunID/重复 fact。测试中的 owner/批准和 Task 模板显式为 store fixture；它们不证明真实用户确认，不取代完整 planning Schema/Policy 检查。当前本地仅编译、vet/staticcheck 和架构检查，动态结果待精确 CI。

尚未关闭：固定 API 认证批准 producer、跨 Goal scope/调度、Run 创建到 READY 的恢复、实际并行执行、集成候选、局部 replan/预算结算和独立 Goal Outcome。此提交是同一 B2 纵切的耐久基础，不是可单独启用的团队服务，未合入 main、不升级生产成熟度。B1 另在 80084bb 精确 CI 验证握手排队修正，macOS 前置真实锁争用/race 已通过，尚待整次 CI 与实机；不以 B1 验证等待为由停止 B2 接线。

## 2026-09-07：B2 批准输入的可执行绑定候选

在既有 `internal/planning` 添加完整 Task/Policy 输入束预检，复用 Task Schema 与 `ValidatePolicy`，不执行命令或写入 Run。明确封闭初始模板为两个实现节点加一个集成节点，并绑定 scope、固定 base、Pi/model、publication:none、预算与确定性 Task/Run ID；重复字段、未知字段、串接 JSON、超限输入和跨节点 Policy 均拒绝。前移这些错误可避免在付费 Worker 开始后才发现方案无法物化。

同时修正 ADR 0083 的 ID 循环：身份由批准前的 namespace/Goal/Proposal/node tuple 派生，完整输入摘要随后由 accepted fact 绑定；不从最终 fact digest 反推该 fact 内 Policy 已引用的 RunID。同 key 改内容仍须由后续 durable CAS 拒绝，不以改 ID 实现隐式 retry。候选目前仅编译、vet/staticcheck 与架构检查通过，动态测试待 CI；尚无生产 endpoint、耐久批准、物化或真实团队证据，B2 仍未集成。该工作分支仍基于未合入的 B1，不代表 main 已具备此能力。

接 RB1 前进一步发现，只有节点预算仍不足以绑定用户同意的总成本：输入束现增加整个 Goal 的 Guardrails 与 AdmissionPolicy，预算不足、超出三并发或不满足准入策略在 preview 阶段拒绝；批准摘要随这些字段变化。数据类型和 ID 派生置于既有 Goal 层，避免 planning→runstore→RB1 的导入环。初次可行性复用 Goal 的 canonical proposal 和六步检查，但空历史结果仍不算 current-ledger 接纳。此改动尚未派发 Worker，属于生产接线前发现的设计缺口；不能将预检通过作为耐久批准或团队收益证据。

首个候选 `10264d2` 的 CI [34056632311](https://github.com/chiga0/marshal-harness/actions/runs/34056632311) 在 Linux 的新增正向变更测试失败：测试把完整 Task 解码成部分 `domain.TaskSpec` 再编码，丢掉 Schema 必需的 `work.context`，因此收到正确的输入拒绝而非期待的摘要变化。产品输入束保留 RawMessage，未执行该有损转换。修正测试为保留全 envelope 只修改目标字段；同一教训约束后续物化：不能用局部领域读模型重写完整冻结协议。失败计入工程验证，不启动或重试任何 Pi；修正结果须待新的精确 CI。

## 2026-09-07：B2 计划到真实 Run 的接缝仍未实现

直接核对 `internal/goal`、`internal/outbox`、`internal/planning` 和 TaskSpec：现有计划组件不耐久落账，节点不绑定完整 Task 输入，planning 尚不是幂等 Goal 物化，Task 依赖也不传递或集成成果。不能据此把 B2 提前列为可用。[ADR 0083 提案](adr/0083-bounded-team-plan-materialization.md) 将后继限制为同一 fixed server/RB1 的批准输入束、原子创建义务、现有 Run 创建恢复和真实集成候选，不引入新 controller/DSL。该文档是 B2 的设计准备，不是实施完成；依赖的 B1 候选仍未合入。

## 2026-09-07：Pi 最终结果载体失败，先补确定性诊断而非重复实机

`80084bb8cf7c02e945a4605da51423c6d25239ee` 的精确 CI [34058120709](https://github.com/chiga0/marshal-harness/actions/runs/34058120709) 五项成功；后续唯一跨 Run 实机 [34059061091](https://github.com/chiga0/marshal-harness/actions/runs/34059061091) 整体失败。peer Collect 报 `pi-result-final-content-shape`，尚无 worker.completed、Verify 或第二个 Run 的并发验收。失败保留在总成本中，不将此前传输修复的 CI 当本次实机成功。

审查实际生产链确认 Collect 将 held transcript 原样送入 Pi 解析器；旧通用错误仍包含最终 assistant 未携带 `content` 的情况。失败归档没有这部分 transcript，故目前不能证实该次失败就是缺字段，也不能声称根因已修复。本机已安装 Pi 0.84.4 的 `toJsonEvent` 对 `agent_end` 原样透传；通过固定 Node 执行该纯函数的人工夹具确认 content 保留，无 Worker、无网络、无真实模型调用。该证据不代表云端失败实例或真实业务通过。

本次仅新增封闭 `pi-result-final-content-missing` 诊断：保持缺字段拒绝，覆盖 `stop`/`length` 和存在/不存在此前合法结果四种完整解析输入，禁止回退到旧 assistant 结果。`length` 仍优先报告原 `provider-terminal`，不让内容诊断覆盖 provider 失败；完整调用链自检已在提交前修正对应夹具预期。此前容器、元素、type/text 类型错误和未知异常仍走原封闭分类；不输出模型正文或自定义字段。未改变持久化契约、权限、重试或接纳行为。本地 compile-only、vet、staticcheck、diff 检查通过；动态回归待精确候选 CI，不冒充实机证据。

效率边界：这是当前业务阻塞的诊断补齐，不是新里程碑；不得据此再次盲目付费重试。下一步需要带可判读且不泄密的失败证据完成同一生产路径验证，同时继续 B2 实际团队启动链路；B1/B2 均未关闭，未证明相对 Lead＋SubAgents 的交付收益。

## 2026-09-07：长 Verify 通过、并行停止已发生，但查询握手仍失败

`b1e838014242c8e5b131f72d56c8a57fd1884d85` 的精确 CI [34056176966](https://github.com/chiga0/marshal-harness/actions/runs/34056176966) 五项成功，随后只派发一次真实 Pi [34056947651](https://github.com/chiga0/marshal-harness/actions/runs/34056947651)，整次失败。小诊断 artifact `9996297294` 已读取，完整 executable artifact 未下载或执行。peer 的 VerificationReport 为 `pass`，起止为 20:08:14.590559Z→20:10:01.019362Z，约 106 秒，随后 ReviewPacket 操作成功；其中部分非适用 gate 为 SKIPPED，不能描述为所有 gate 都实际执行通过。未产生独立 Decision 或 ACCEPTED。

另一 Run 的原 Attempt deadline 为 20:09:27.384502Z，停止意图在 20:09:28.405749Z 出现，`worker.stopped` 在 20:09:34.620746Z 写入，分别延迟约 1.02/7.24 秒，均处于上述 Verify 报告区间。两 Run 各一 Attempt，只有一个 owner acquisition。但第 22 次调用 Inspect exit=1、空 stdout，封闭阶段为 `client-dial`；并发线程因此未完成 stopped Collect。不能用停止事件或 peer Verify 的局部成功代替完整跨 Run 验收，也不能排除失败的两个 Attempt 成本。

代码核对确认一种确定的预算错配：client 从 connect 后统一计 5 秒；server 在 challenge 签发前和 proof 之后都要获取 current owner 锁。超过 5 秒的合法停止事务因而可能让认证排队被误算成 proof/传输超时。该结构缺陷与现场相容，但旧 stderr 仅保留摘要和大阶段，**尚不能证明本次具体失败就是这一锁等待**，更不能排除其它身份/连接失败。

候选依 ADR 0081 将本机 authority 等待与 nonce/frame 窗口分离，保持原 request/caller deadline、5 秒 nonce/proof、16 KiB frame、完整 current identity/owner/receipt 复查和零自动重试；新增真实 owner 锁争用超过 5 秒、原 deadline、父取消与部分帧回归，复用既有 hostile/replay 拒绝测试。动态测试先于任何新 Pi 实机；本地 compile-only 不是通过证据。B1 仍 IN_PROGRESS，PR #268 仍 Draft，未合入 main；B2 的冻结输入/总预算候选仍未形成 durable approved plan 或实际团队交付。

效率复盘：两轮实机都暴露阶段预算/交互问题，说明此前回归覆盖偏向单入口而没有充分覆盖端到端等待。后续同类修复必须同时检查认证排队、应用等待、字节传输、复查、关闭与原 deadline，并先用真实锁/transport 的无模型组合测试；不能把每个窗口都留给下一次付费实验发现。此次先聚合 handshake 的排队、proof、取消与截断帧，而不是只调高一个 timeout 常量。

## 2026-09-07：初始续行实机通过后暴露传输阶段预算错配

`d0be824` 精确 CI [34054261338](https://github.com/chiga0/marshal-harness/actions/runs/34054261338) 与 PR CI 34054248613 均通过；条件链仅派发一次实机 [34055217240](https://github.com/chiga0/marshal-harness/actions/runs/34055217240)，整次失败。诊断 artifact `9995784841` 已读取：peer 在唯一 owner 下从 Start 经五次 authenticated live-pending Collect 到成功 Collect，19:34:16.581183Z 写入 `worker.completed/VERIFYING/sequence=4`；对应 admission fact 为 `sha256:99c5ad1d80a3f197cebbbb5e911f0c95941acd302c3b5b518e6c8d3ea6264792`。因此前次初始 owner 故障已在本样本的真实连续链路越过，不是再次停在首次 Collect。

长验证命令已产生 rendezvous（Unix 时间 1788723263.80846），另一个 Run 19:34:31.235692Z 已写入 RUNNING。两个 Run 各一 Attempt，RB1 仅一个 `control-owner-acquired`；但 Verify 与第二次 Start 的客户端均 exit=1、空 stdout，19:34:34.287768Z 驱动报 `fixed-cli-invalid-response`；server 还记录一次 `server-half-close/transport-failure`。没有 verification report、独立 Decision、停止 Outcome 或跨 Run 组合通过，不能把上述局部推进算整次成功。原失败与两次 Attempt 均保留在交付成本中。

代码直接确认传输预算缺陷：`readClientHTTPResponse` 在等应用首个响应前就安装固定 15 秒 read deadline，必然无法支持本次 100 秒验收；server 仅等 1 秒 half-close，而客户端须先复查身份/receipt。后者是已确认的协议预算竞态，但现有 Start stderr 只有泛化文案，不能把其具体失败位置强行归因于某个 recheck。候选按 ADR 0081 分离原请求内的应用等待、字节传输和复查预算，并补 Start/Verify 的既有封闭阶段诊断；不输出原始 error、路径、secret 或放宽错误重试。

先补无模型的实际 authenticated client→HTTP router 长应用回归（超过旧 15 秒）、父取消中断、原 deadline/部分 envelope 限制，以及延迟 half-close/缺失 half-close 的有界回归，再运行一次精确候选 CI/实机。回归中的业务 application 为显式 fixture，不冒充 Pi 或独立验收。当前修正尚未动态/实机通过；B1 仍 IN_PROGRESS，停止候选未合入 main，B2/B3 不升级。

## 2026-09-07：不重启 server 的首次 Collect 暴露初始 owner 续行缺口

`593eb5d` 的精确 CI [34051652443](https://github.com/chiga0/marshal-harness/actions/runs/34051652443) 五项通过后，只派发一次跨 Run 实机 [34052534488](https://github.com/chiga0/marshal-harness/actions/runs/34052534488)。实验失败，未进入长 Verify，不能计作跨 Run 调度通过。诊断 artifact `9994993010` 已保留并读取：peer 的 Start 和 Inspect 返回 RUNNING/sequence=3，首次 Collect 已留下 delivery pending，但客户端无 JSON、退出 1；server 明确记录 `sealed-run-compose-runtime/composition-failure` 与 `recover-running-attempt/recovery-required`。RB1 只有一个 attempt-opened、17 条 fact，最后为初始 Resume 成功；另一 Run 尚未启动。没有业务 Decision、完成 Outcome 或新的业务 retry，不能把这个失败排除出实验分母。

原因不是 Pi 响应速度：初始 bind 绑定 SupervisorStarted（revision 7），Resume 后 mechanics/Attempt head 已到 ProcessStarted（revision 8），owner 仍为同一 epoch 1。`runningAttemptBoundToOwner` 却只认可 bound=head，误入 owner-successor rebind；Collect、Inspect/Terminate、Close 的 v2 gate 又只认可恢复后的 ControlOwnerBinding。这解释了旧 canary 在 Start 后重启、再 Collect 可以成功，却没有证明最普通的同 server 连续执行。

本次候选集中修复上述完整接缝：重放后的绑定分类区分初始同 owner 与已完成恢复绑定；初始分支额外核对 v2 generation、原始/current mechanics owner epoch 与 mechanics authority head，仍由调用方持有 current owner/Run/RB1、认证原 Attach 和 journal。新增同一 durable bootstrap→bind→Spawn→ProcessStarted→Resume 直接到 Collect/Close 或 stop/Terminate/Close 的连续回归，复用原丢响应、坏证据拒绝和冷重放测试；不插入 owner 升级或 server 重启。该候选尚待动态 CI 和原跨 Run 实机，未合并 main，不宣称修复已实机通过。

效率纠偏：每次恢复实验必须同时保留一个**不注入故障、不重启**的正常控制路径，不能以恢复分支覆盖代替基础调用链。先聚合相关入口与连续回归，再运行一次精确候选 CI/实机；已失败的 34052534488 不原样重跑。

后续动态 CI [34053220150](https://github.com/chiga0/marshal-harness/actions/runs/34053220150) 在 `d67e3b7` 的新增 `TestLauncherV2SameOwnerContinuesWithoutRestart` 两个分支失败：普通 Collect 未留下预期 pending，停止后的 cleanup Collect 返回 authority conflict；其余四个 job 成功。已确认底层 `validateSupervisorCommandIntentAgainstState` 仍要求 reconnect/rebind，遗漏同一初始 owner 的 v2 bind/resume 连续证据。补齐该入口，保留 legacy reconnect 限制以及现有 owner、intent、mechanics、journal 与结果校验；不通过强制重启绕过。将该连续回归加入 macOS 前置测试，避免每次等全量约 15 分钟才发现同类错误。条件派发链因 CI 失败退出，未为 `d67e3b7` 新启 Pi；修正后的动态与实机结果仍待验证。

## 2026-09-07：跨 Run 长 Verify 与自动停止组合验证接入

在同一 fixed server 的既有 Attempt-timeout canary 增加显式 `verify-peer`，不新增 Worker launcher 或业务状态库。两个 Task 在 server 启动前冻结并批准：peer 真实 Pi 完成订单报价，保留原业务 oracle，并执行 100 秒有界验证命令；命令的诊断 rendezvous 出现后，驱动才经公开 Start 启动另一个 60 秒 Attempt Run。所有 Start/Collect/Verify/ReviewPacket/Inspect 仍走 fixed control-plane，未知错误不重试，不创建 Decision。

最终判定必须绑定公开 Verify projection 的 reportDigest、冻结 Task specDigest 和精确验收 argv，并证明另一 Run 的终态查询与 stopped Collect 已完成时间严格位于真实长命令执行区间；单纯看到信号不计作通过。并发调用日志使用独立序号，避免两个响应抢写同一文件。新增回归覆盖 rendezvous/模式/身份拒绝、精确报告绑定、重叠区间、失败不重试和 hook 时机；这些是驱动测试，不代表实机跨 Run 或 B2 已通过。实验不与 crash/独立 Decision 混用，以免把多类失败混成一次 rework；中断恢复已有独立的 34050602081 证据。

## 2026-09-07：真实 Pi 在 stop barrier 后中断并恢复通过

候选 `0130465bfb65dd1b947c09113f988714c6952c00` 的 CI [34049622019](https://github.com/chiga0/marshal-harness/actions/runs/34049622019) 五项成功后，仅派发一次 [34050602081](https://github.com/chiga0/marshal-harness/actions/runs/34050602081)，实机成功。诊断 artifact `9994442912` 已核对：中断前后均停在第 22 条 `terminalization-barrier`，Run 仍 RUNNING/sequence=3；两次观察的 journal/ingress 摘要相同，并可由最终归档前缀复算。server2 是驱动自己持有的进程，SIGKILL/wait=137；同身份后继 server 完成停止，之后 server3 再执行原 Collect 请求冷恢复，两者正常退出 0。

最终 RB1 40 条 fact 的封闭摘要逐条复算通过，Run 4 条 event，只有一条 attempt-opened；原 Task 的 Attempt=60 秒、Run=600 秒与 creation event/process-start witness 一致，未延长预算。停止意图在原 Attempt deadline 后 0.458190 秒出现，包含崩溃恢复的终态在 deadline 后 4.334296 秒写入。Outcome 为 `BLOCKED/abort/attempt-deadline-exceeded`，规范摘要 `sha256:cc42602cd37872f42fa4467e88e24fac0fa992390674e29611523f6d02d59565`；同 Run/Attempt/stop intent、终态 authority head 和 Collect request key/deadline 在冷恢复中保持一致，无第二 Attempt、Cancel、retry、rework 或 ACCEPTED。

四次 binary observation 完全一致，SHA-256 `912bc611d9d77ebf480c3d2c76ce229fc220846043810bd4b7941263ea39ef69`、CDHash `7f124dadb114c2bf80078346f6cba10fd3de7c67`；本次本地审计仅下载诊断包，未另行下载完整二进制复算。结论只关闭 **barrier 已落盘、Run 尚未终态时的 server 进程中断恢复** 子条件，不代表断电、任意持久化边界或完整故障矩阵。driver 的 `deadlineWitnessVerified=false` 如实保留；上述预算检查是本次对原始材料的额外核对，不改写 driver 结果。

长 Verify 调度修复 `74e8619fa8168abbc76a7eda3f3f647f6882ed6f` 已推送，精确 CI [34050715623](https://github.com/chiga0/marshal-harness/actions/runs/34050715623) 在途。首次 dispatch 34050684925 遇到分支传播延迟选中旧 SHA，已立即取消，不计作新 source 的 CI 或业务重试；后续 dispatch 须核对解析的 headSha。B1 仍 IN_PROGRESS，跨 Run 实机、最终组合与候选合入尚未关闭；B2/B3 不升级。

## 2026-09-07：验证长事务阻塞其他 Run 的调度修复候选

调用链核对确认：fixed router 的全局 writer lane 覆盖完整 Verify，`sealedRepositoryApplication.VerifyRun` 又把 application mutex 持有到验收命令退出，后台 deadline 的两层 Try 因而只能跳过。Status/Inspect 的旧 mock 测试仅证明绕过调度锁；真实 Inspect 还竞争 Run lease，不能据此声称长验证期间查询都成功。

按 ADR 0081 的同一停止纵切补进程内 Run lane，保护完整 Begin→application→receipt；Verify 执行阶段让出全局 lane，application 在已证明当前 VERIFYING 后只保留该 Run/worktree lease 与 Close 生命周期读保护。其他 runtime mutation 的全局串行及原始 authority/CAS/receipt 规则不变；不新增 RPC、持久化状态或查询缓存。四项新增组件回归覆盖 Run waiter 取消与条目回收、Verify 期间允许后台 writer、Begin/receipt 期间仍互斥、receipt 等待不延长 deadline，以及 preflight 失败不泄漏生命周期锁。本地 Go 结果仅为 compile-only，另有 vet/staticcheck；动态/race 与真实跨 Run 故障验收尚待执行。

这只解除 Verify 对无关 Run 的阻塞：同 Run Inspect 的 lease 等待仍受原 caller deadline 限制，其他长 Start/Collect/cleanup、验证进程故障及完整业务预算/终态覆盖仍需实证，不能据本候选宣称 B1/B2 完成。中途停止 canary 仍使用已推送的 0130465，与本候选分别取证。

## 2026-09-07：停止中途崩溃验证接入（尚待实机）

在同一固定 server canary 增加显式 `stop-crash`：只允许两种业务 timeout 场景；观察到原 RB1 stop intent 且 Run 仍 RUNNING 后，仅中断驱动自己持有、尚未回收的 server 子进程。等待进程退出后再次读取同一 Run/Attempt/意图；若窗口已经错过，明确失败，不把终态冷重启冒充中途恢复，也不自动重试。后继同 bytes server 必须沿既有 owner rebind/stop reconciliation 完成终态查询与 stopped Collect，再做原请求冷恢复。观察器不提供 PID、不调用 Worker、不修改 Run/RB1；这是诊断证据，不是新的接纳 authority。

12 项无模型观察器回归、现有 canary 脚本回归和 release producer 契约回归通过；两份既有真实 RB1 的编码/摘要兼容性检查通过。这些都不代替新场景实机结果。小诊断同时加入完整 artifact 原已有的冻结 Task 与进程退出记录，避免仅为预算来源而下载 executable；仍排除 executable、transcript 与配置文件。没有新增运行时协议，B1 中途故障、长写事务响应上界与组合验收仍开放；只按实际命中的中断阶段记录覆盖，不宣称完整 crash/power-loss 矩阵通过。

## 2026-09-07：稳定容器修复后的两类自动超时和冷恢复通过

精确候选 `c61998515512f064fc4b113c229295e5df28e185` 的 [CI 34047040755](https://github.com/chiga0/marshal-harness/actions/runs/34047040755) 五项全绿，macOS 前置停止链回归先于全量质量检查通过，完整 macOS job 用时 14 分 25 秒。随后分别派发一次真实 Pi 场景，未改变 Provider/模型，未原样重跑失败版本。

| 场景 | 实机与诊断 artifact | 原始业务截止点（UTC） | 停止意图 / Run 终态延迟 | 结果 |
| --- | --- | --- | --- | --- |
| Attempt 60 秒先到期 | [34047844723](https://github.com/chiga0/marshal-harness/actions/runs/34047844723)，`9993670793`（45,991 bytes） | `2026-09-06T17:13:48.789964Z` | 0.801903 / 4.524347 秒 | 作业 2 分 11 秒；终态查询/Collect/server3 冷恢复通过 |
| Run 60 秒先到期 | [34048091298](https://github.com/chiga0/marshal-harness/actions/runs/34048091298)，`9993737275`（46,356 bytes） | `2026-09-06T17:18:23.541713Z`；Attempt 截止点为 `17:18:27.654604Z` | 0.868224 / 3.720027 秒 | 作业 2 分 13 秒；终态查询/Collect/server3 冷恢复通过 |

直接读取原始证据核对，而不只采用 workflow 绿色：两次各 36 条 RB1 fact、4 条 Run event、一次 Attempt、零 operational retry/rework；停止原因分别为 `attempt-deadline-exceeded` / `run-deadline-exceeded`。creation event canonical digest、specDigest、原始 process-started fact/时间与 deadline 计算一致；`worker.stopped` 引用 barrier、process-terminal、allocation-terminated、supervisor-closed、cleanup-released 的精确 fact digest。Outcome 为 `BLOCKED/abort`，绑定 stopped payload 的 canonical digest、原终态时间与原因，不冒充独立 ReviewDecision。两次 Outcome digest 分别为 `sha256:7606aae4cff99e449ee672328d2573a0966006fea3cbd76744d4080bc6545701`、`sha256:d5ae8eadd0dab6c0cdc84282972f97b78bedbaf43294b2bacde863238da0d1f1`。

两次实机的三代 server 均保持完整 binary identity，跨两次 artifact 的 binary SHA-256 也相同：`a55e680713258f357c3846473ba2f1ed53a685764f5d8a52cda5858545883aa3`。Attempt 场景初段 13 次 Inspect、一次 Collect；Run-first 初段 14 次 Inspect、一次 Collect；各自 server3 均两次 Inspect、一次原请求 Collect，零 Cancel。原 Collect key/参数/deadline 与终态投影跨冷重启相同，所有 Inspect 成功，Collect 返回封闭的非成功 `stopped`，没有新增 Attempt 或假 ACCEPTED。完整 artifacts `9993671390`、`9993739378` 均已下载：两份原始 Task canonical digest 与各自 witness specDigest 相同，Attempt/Run 预算分别为 60/600 秒与 60/60 秒；各自完整/小包 Outcome bytes 相同，server2/3 正常退出，实际下载的两个 binary hash 也与上述观察一致。

取证效率缺口：小诊断缺少原始 Task，预算来源复核被迫下载约 19.8 MB 的含 binary 完整包；本轮第二份下载约 7 分钟，长于两次 canary 自身。后继故障矩阵应一并归档必要的冻结业务输入到有界诊断材料；完整 artifact 仍保留，但不再把它作为读取少量预算证据的唯一入口。该改进尚未实施，不另起脱离 B1 验收的微修切片。

结论与后继：旧布局在 34044944162 的客户端打开失败没有再出现在这两次完整样本中，支持稳定容器根因修复有效；不据两个成功样本承诺竞态永不复发或普遍加速。停止中途故障矩阵、长写事务下响应上界、精确最终版本正常业务与停止组合验收、独立审查仍开放。B1 保持 `IN_PROGRESS`，B2/B3 不升级，候选未合并 main、无 stable 发布。历史失败继续计入总交付成本；下一轮应处理上述剩余验收，而非再次派这两个已通过场景。同步纠正文档“唯一当前表”仍停留早期接口阶段的陈旧内容，保留历史检查点但不让它覆盖已有业务进展。

## 2026-09-07：用稳定投影容器消除查询与业务写入的目录耦合

`487bbc243673b8e1c389e0d4e58f235767a3f188` 的 CI 34045694199 五项全绿，公共客户端 characterization 动态确认了旧布局的观察失效；它只证明接缝缺陷，不是修复或实机成功。后继候选按 ADR 0081 的同一纵切，把原子投影和 stage 移入固定容器内的 `current-v2`，transport 不再刷新 `runtime-v1` 观察，容器、runtime、control 的替换/ABA 继续拒绝。旧根部投影先按当前 RB1 合法前缀只读校验并原样保留；未知/损坏/旧中断 stage 不被覆盖或遮蔽。新布局不修改 RB1/Run journal/receipt，不新增业务 authority。

回归覆盖实际 allocation bind/release/reopen 不修改 transport 父目录、旧合法/损坏/符号链接/未知/未完成布局、容器 ABA，以及公共客户端在 stage/commit/cleanup 后原连接观察仍有效且新开身份相同。该客户端测试不冒充 HTTP+RB1 全链路，仍需新 source hosted 动态门禁和真实自动超时/冷恢复。全量发布证据保留，但这组窄回归放在 macOS 全量之前；无模型 canary 原样重试。当前仍为未合并候选，B1 IN_PROGRESS，B2/B3 不升级，无 production/stable 声明。

工程教训：把派生数据更新与 transport 根身份绑定在同一目录 mutation 上，再逐个为 Start/Collect/后台停止补“观察刷新”，会反复遗漏独立客户端。修复应隔离可变存储边界并测试完整 producer/consumer，而不是扩大错误重试集。效率是否优于 Lead+SubAgents 仍待配对业务试验，不能用本次组件通过替代。

## 2026-09-06：前置回归遗漏测试 binary 的 sourceHead

CI 34045483914 在 macOS 前置回归约 17 秒即失败：公开客户端测试的 fixture 在 `ObserveCurrentCore` 返回 identity conflict，尚未执行目录切换。原因是手写的前置 `go test` 漏掉 `Makefile:test` 已明确要求的 buildinfo.commit 注入，测试 binary 使用 `unknown` 而非精确 40-hex sourceHead。此次补齐两条前置命令与封闭 workflow producer；不跳过进程身份检查，不把 compile-only 当成已执行该检查。这是新增测试的启动配置返工，不是实机 Provider 重试，也不是目录竞态已被动态证明；B1 状态不变。后续真实身份测试必须保留同一 sourceHead 构建参数，不能只复制包名和 `-run`。

## 2026-09-06：精确诊断候选仍失败，位置前移到客户端 authority 打开

`88f9eddb856ca0c438ae574a2f8391981f8b2c23` 的 CI 34043986843 五项全绿；单次 Attempt-timeout canary [34044944162](https://github.com/chiga0/marshal-harness/actions/runs/34044944162) 失败。小诊断 artifact `9992827944` 保留全部 17 次 Inspect 摘要：前 16 次成功，第 17 次 exit=3、空 stdout、无 HTTP stage；stderr SHA-256 `f85f116f2ae11c764fec6975425fee1d413fddcc160431808a0649b216b5998f` 精确匹配固定文案“control-plane inspect 失败：resident server 不可用。”加换行，定位到 `openControlPlaneClient`，尚未发送 HTTP。不能据此断言 Provider 配置错误。

RB1 已记录 Attempt deadline barrier、process-terminal、existing-worktree-release-intent/receipt（sequence 22–27），但尚无 allocation-terminal/supervisor-closed/cleanup-completed/released；Run journal 仍为 RUNNING/3。该次不能沿用前次 BLOCKED 结论，也未完成 Collect/server3。server2 的 shutdown 不完整文案没有携带最初 authority 打开失败的内部原因，不能单凭它判断 server 先崩溃。

代码确认一个需要确定性覆盖的窗口：投影 `RENAME_SWAP` 改变 runtime-v1 目录观察；独立客户端持有的观察不会随 server 更新。新增公开 `OpenRepositorySession → OpenFixedEndpointClientAuthority → swap → old Recheck 拒绝 → fresh open` 的组件 characterization，使用真实 local/default/repository namespace、真实 held directory 与原子 swap，不使用 Provider，不冒充 HTTP 全链路或 RB1 合法 release 证明。把它加入 macOS CI 前置回归，先验证这一假设，不第三次原样派实机。没有放宽身份验证、重试集合或接纳条件；根因修复和 B1 仍开放。

反馈周期也有具体证据：前序 CI 34041798874 的 macOS quality 用时约 15 分半，其中 resultingress race 包 523.947 秒。当前需要把接缝复现放在全量之前；不能为节省等待删除全量发布证据，也不立即另起测试框架。

## 2026-09-06：停止后查询失败的诊断缺口（尚未修复根因）

对 34041730043 的原始小诊断复核后，server 仅有通用 transport-failure，客户端也只输出 authenticated request 未完成；驱动只保留 stderr 摘要。现有材料无法区分 request 读取、准入、前后身份复核、dispatch、response 和 half-close，不能断言锁或 timeout 是根因。

本候选在原有 HTTP/client 调用链添加本地封闭 stage 标签，保留原 error 分类、HTTP 结果与拒绝行为；CLI 沿已有有界错误树输出标签，driver 只收集精确 allowlist 标签，不上传原始 stderr，不把标签用于验收或重试。补充分类/脱敏单测、真实 socket router 错误阶段断言与 driver 反例；本机仅 Python 动态测试和 Go compile-only/vet/staticcheck，不执行匿名 Mach-O。前序 97e448a 的 CI 34041798874 已全绿，不覆盖本候选。尚未得到实机根因或关闭 B1，不原样重跑原失败。

## 2026-09-06：Run-first 冷恢复通过；Attempt 停止完成但查询失败

`49f745da10d33a71146afa75528d1f34a691b159` 的 [CI 34040876557](https://github.com/chiga0/marshal-harness/actions/runs/34040876557) 五项全绿，随后 [Run-first 34041702160](https://github.com/chiga0/marshal-harness/actions/runs/34041702160) 成功。独立读取诊断 artifact `9991894362`，重新计算 creation event digest，连接同 Attempt process-started、原始 60/60 秒预算、stop intent、四类 terminal/cleanup 引用及 BLOCKED 事件。Run deadline `15:16:48.357751Z` 早于 Attempt deadline `15:16:52.173736Z`；停止原因精确为 `run-deadline-exceeded`，本样本意图延迟 0.805517 秒、终态延迟 3.340678 秒。server2/server3 binary identity 相同，raw SHA-256 为 `656d53a2b8237a76566db52b469016a7fde5259de3b7556d0a0058a2893215dc`；冷恢复保留原 Collect key/head/deadline、终态投影和响应 SHA-256 `606f11c437c8af004acfcc1e766d73e63ec1b10df913949757d9147d739a6e1e`。没有 Cancel、第二 Attempt 或业务 ACCEPTED，仍是 candidate-only，原始 Outcome 文件不在此旧归档清单中。

同 source 的 [Attempt-timeout 34041730043](https://github.com/chiga0/marshal-harness/actions/runs/34041730043) 在并发组排队后执行，但整次失败：原始账本已于 `15:19:05.43834Z` 形成 `worker.stopped/BLOCKED/sequence=4/attempt-deadline-exceeded`，末端 process-supervisor-closed、cleanup-completed/released 均存在；驱动一次 Inspect 却得到空 stdout、exit=1，stderr SHA-256 为 `abf43c190890f51240c04294f209715a3c0e54b125631cc10d078e0a04d20237`，server 只保留 `reasonCode=transport-failure`，驱动于 `15:19:06.193157Z` 报 `fixed-cli-invalid-response`。未进入 Collect 或 server3，不能把停止事件当成整条恢复验证通过。诊断 `9991926037` 与完整包 `9991926396` 保留，未原样重跑。

复盘：此前只为 Inspect 的 Run lease 竞争增加等待，并没有证明连接鉴权、current owner/root recheck 到完整查询响应的并发路径都可用。本次再次出现查询 transport 类失败，应冻结同类实机重试，先补足可定位且不含路径/secret 的错误阶段证据及真实读写并发回归。现有证据不足以断言是 lease、endpoint recheck 或超时；不扩大可重试错误集合，不用客户端重试掩盖。后继 `97e448a` 的 CI 34041798874 验证 Outcome 物化回归与归档修正，不声称修复这次查询问题。B1 仍 IN_PROGRESS；下一关键动作是关闭该查询接缝，再验证两类超时完整恢复和其余故障窗口。

## 2026-09-06：停止 Outcome 部分落盘恢复回归

后续检查归档清单发现：此前完整包和小诊断都只显式包含 Run state/events，未包含 `outcome.json/result.md`。因此原始 Outcome 缺失不只是下载等待，重下同一包也无用；历史证据仍只支持已记录的 Core stopped-Collect 验证口径。候选为两份 artifact 增加精确 Run 的这两个派生文件，以便后续独立检查 bytes/摘要；不读取或上传新类别的 Worker transcript/secret，不补造历史证据。

停止事件的 current-ledger/cleanup 校验保持在原入口；只把其后既有的 Outcome 和说明文件不可变写入提取为内部函数，未新增停止权限、事件、协议或 Worker 启动路径。回归直接调用这个生产写入函数：第二个文件被测试自有空目录阻断时不得返回成功摘要，已写入的 Outcome 保留；释放并重新取得 lease 后补齐说明文件，两次重放保持原始 bytes、时间、原因和摘要。另覆盖两个目标文件的冲突内容、符号链接及已关闭 lease，均不得覆盖已有内容或报告成功。

这些是合成 Outcome 的文件物化组件测试，不是完整停止授权、实机崩溃或磁盘断电证据，不关闭 signal/cleanup 中途故障矩阵。本地 compile-only、vet、staticcheck、diff-check 通过；动态执行须由新 source 的 hosted CI 验证，不执行本机匿名 Mach-O。Run-first/两类超时冷恢复仍先验证已推送的 `49f745d`，不把后继测试代码混入它的 binary 身份。

## 2026-09-06：Run-first 与两类超时冷恢复候选

`d10cd98` 的 CI 34040123782 五项全部通过，包含 Cancel 排队 deadline/零意图及 Task renderer 的动态 schema 回归。本轮在同一个 canary 增加 `order-quote-run-timeout`，并让两类 timeout 完成后正常关闭 server2、以同 bytes server3 重启、重放原 Collect 请求和原 deadline。冷恢复若看到 RUNNING、不同终态 head、不同 binary/Run、被改写的 key/参数或过期原 deadline，即停止，不等待新一轮执行、不取消或启动 Worker。原取消场景保持。

前置核对发现 Task 语义禁止 Attempt budget 大于 Run budget；因此 Run-first 采用两者均为 60 秒，Run 创建早于 process-started，实际较早 deadline 仍须在实机 witness 中检查。这个配置问题在提交/CI/付费 Worker 前已纠正，没有修改合同门禁。Python 33 项通过；Go 本地只 compile-only/vet，新 source 动态与实机仍待完成。此前 Attempt 超时完整 artifact 的下载仍是已确认存活的同一任务，未重启下载或重复 Worker；小诊断足以推进 deadline 来源审计，但不能冒称已读取未下载的原始 Outcome。

## 2026-09-06：原始 Attempt deadline 自动停止实机通过

精确 `dd8178f096df9503fafa355f19a126105f2e7c76` 经 [CI 34039269163](https://github.com/chiga0/marshal-harness/actions/runs/34039269163) 五项全绿后，单次 [34040069400](https://github.com/chiga0/marshal-harness/actions/runs/34040069400) 成功。server2 binary SHA-256 为 `3488ade9641164f0d7a498e400d55826e10338e3d64a5c9313517e536c0772e5`。真实 Run `fixed-server-t1-34040069400` 一次 Attempt、零 operational retry/rework；驱动只有 19 次 Inspect 与终态后一次 Collect，零 Cancel，不能把它解释成 operator stop。

独立读取诊断 artifact `9991417910`（42,435 bytes），逐项核对 creation event 的 canonical digest、Task specDigest、同 Attempt 的 process-started fact/timestamp、原始 60/600 秒预算及计算结果、barrier/stopIntent 与停止事件的引用、process-terminal/allocation-terminated/supervisor-closed/cleanup-released 链。原 Attempt deadline 为 `14:45:33.642022Z`，stop intent 于 `14:45:33.752306Z` 形成，`worker.stopped/BLOCKED/sequence=4` 于 `14:45:36.212847Z` 形成：本样本意图延迟 0.110284 秒、终态延迟 2.570825 秒。此前 server1 crash/server2 rebind 没有重置 processStartedAt 或延长预算。停止原因精确为 `attempt-deadline-exceeded`，stopIntentDigest 为 `sha256:800125bc9e7bac8fef02d7a6d94c0fcf0059f4093de318740f4e9b3fcbe9a739`。

终态 Collect exit=1、stderr 为空，stdout SHA-256 为 `606f11c437c8af004acfcc1e766d73e63ec1b10df913949757d9147d739a6e1e`，对应 Core 已验证 stop/cleanup/Outcome 的 `stopped/run-stopped`。小诊断不含原始 Outcome，不能说已独立下载并重算 Outcome 摘要；完整 artifact `9991418302` 保留并单独取回。驱动自己的 `deadlineWitnessVerified=false` 保持原样，本段是驱动之外的来源/引用审计，不修改历史证据。

这关闭了隔离候选的一次原始 Attempt 自动超时实机子条件；Run budget 先到期、超时终态后的冷恢复、signal/cleanup 中途故障及长写事务延迟仍须继续验证，不推导普遍 SLA、不关闭 B1。后继 `d10cd98` 的 CI 34040123782 独立运行，只含排队回归/Task schema 枚举与过程记录；不能冒用本次 binary 身份。停止候选仍未合入 main，ADR 0081 仍 Proposed。

## 2026-09-06：等待精确超时候选 CI 时补查排队边界

`dd8178f` 已推送，精确 CI 为 34039269163。紧接 push 的第一次 workflow dispatch 34039237556 在 GitHub 解析到上一 source `86b6553`，已核对并取消该自有 CI；随后确认远端 API head 为 `dd8178f` 才重新派发，未重复 Worker、未把旧 source CI 借给新候选。以后 dispatch 后仍立即核对实际 `headSha`，不能只凭命令成功认定版本正确。

响应上界复核追到 `HTTPRouter.acquireMutation`：它已有 context-aware writer queue，Status/Inspect 绕过该队列；仅看到 application 全局互斥锁不足以断言 HTTP 会无限挂起。因此本次不新增锁或 controller，只补 Cancel 排队的请求 deadline 到期、零 delivery pending/停止意图/receipt、查询可达和不释放他人 writer lane 的定向回归，并将 timeout renderer 加入真实 Task schema 测试枚举。这是 routing/fixture 测试，不证明执行中的 verification 可以被抢占，也不关闭长事务下实际业务停止延迟。新增代码本地 compile-only、vet/staticcheck 通过，动态证据仍需其自身 source CI；不混入已派发的 `dd8178f`。

## 2026-09-06：自动业务停止观察候选，不能把观察当作 deadline 证明

显式取消与完成后冷恢复通过后，本候选在同一 fixed server/Pi/canary 增加 `order-quote-timeout`：审批前 Task 冻结 60 秒 Attempt、600 秒 Run 预算；原业务场景仍为 300/600，不改变 runtime 合同。Start 丢响应/重启后只进行有界 Inspect，直到观察 BLOCKED 才调用当前 head 的 Collect，并要求 `stopped/run-stopped`；不调用 Cancel，不在 RUNNING 时用 Collect 触发停止，不自动重试失败 transport。180 秒是观察器等待上限，不是业务 deadline。

观察成功只记 `resident-stop-observed/deadlineWitnessVerified=false`。须独立检查保留的真实 journal/ingress 的 terminalReason、冻结预算与来源、原 deadline、stop/cleanup/Outcome 链后才能记自动业务超时通过；不能以 BLOCKED 或 CI 绿色替代该检查。新增测试覆盖观察到期不派取消、异常不重试、当前 head、停止后 Collect 失败和普通 Task 预算不变。本候选尚待新 source CI/实机，不关闭 B1。

## 2026-09-06：取消、停止后 Collect 与冷 server 恢复实机通过

停止候选 `6e87f34a68f085384b8eaba09d76d2b5bd682b90` 的 [CI 34037704960](https://github.com/chiga0/marshal-harness/actions/runs/34037704960) 五项全绿后，单次 [canary 34038482097](https://github.com/chiga0/marshal-harness/actions/runs/34038482097) 全部成功。真实 Pi 经 Start 丢响应/server1 crash/server2 rebind/replay 后，取消生成 `worker.stopped/BLOCKED/sequence=4`、完整 cleanup 和 Outcome；原 Cancel 精确重放、当前终态 head 的 Collect 返回 `stopped/run-stopped`，server2 正常退出。相同固定 bytes 的 server3 冷启动后再次验证原 Cancel/receipt/Outcome、终态 Collect 和查询，未重启 Worker、未延长冻结 deadline。

诊断 artifact `9990944385` 已独立取回，完整 evidence `9990944800` 远端保留。server2 首次/重复及 server3 Cancel 的响应 SHA-256 均为 `5d8c04d2b4e57a7cd39a351d40774e5f15842abe9c3b5d84e6a04351d57ebf65`；两次终态 Collect stdout SHA-256 均为 `606f11c437c8af004acfcc1e766d73e63ec1b10df913949757d9147d739a6e1e`，exit=1、stderr 为空（这是预期的非成功业务结果）。Run 从 14:14:21.440921Z 创建，到 14:14:43.124358Z 停止；整个 canary 于 14:15:04Z 完成。不把上述整体时间当作取消请求延迟，也不以单样本推导可靠性。

关闭的是隔离候选的显式取消及完成后冷恢复子条件，不是所有 stop 故障窗口：自动业务 deadline、signal/cleanup 中途崩溃、长写事务下查询/停止响应上界仍开放，ADR 0081 继续 Proposed，B1 IN_PROGRESS。此前失败全部保留。为继续同一路径，开发分支同步 `origin/main@ba2196b` 的已合入正常 Pi 分类/ACCEPTED 文档，保留两侧审计历史；本次同步不是把停止候选合入 main，合并后的新 source 也不能冒用上述实机身份。

## 2026-09-06：真实取消与精确重放成功，停止后新 Collect 误用旧 head

`a553c445928a566874dfdb852e9f6f698ddeac88` 的 [CI 34036324414](https://github.com/chiga0/marshal-harness/actions/runs/34036324414) 五项通过。单次 [canary 34037154719](https://github.com/chiga0/marshal-harness/actions/runs/34037154719) 已越过之前的查询失败，两个 Cancel 调用均 exit=0，响应 SHA-256 同为 `e5b4181fa740ffae94677df66e2e81ff96b94561d9710137293b2f6f995619b1`；journal sequence 4 为 `worker.stopped`，含完整 barrier/process/allocation/supervisor/cleanup 引用及 `aborted-by-operator`。驱动已验证 stop/Outcome/receipt 形状后，在第四次调用 Collect 收到 transport failure。没有取消后 server3 冷恢复证据，整次 canary 仍失败。诊断 artifact 9990541132 已保留，完整包 9990541531 独立留存。

根因核对：该 Collect 是新 key，却使用取消前 RUNNING sequence/head；真实 `BeginLifecycleBound` 必须拒绝非当前 head，而原 injected-call 测试只返回预置结果，漏掉了 delivery 约束。候选使新 Collect 使用已证明的 BLOCKED head；Core 仅将精确当前终态读取连接到耐久 stop intent 的原始 head，再执行完整 terminal/Outcome 验证。旧 pending 重放、显式 Cancel 与通用 stale-head 拒绝不变，合同补入仍为 Proposed 的 ADR 0081。增加真实 delivery store 测试区分新请求与历史 pending，纯映射负例及驱动参数断言；该测试的停止引用是明确 synthetic，不冒充完整 authority 证明。新 source 的动态和实机证据仍待验证，B1 不关闭。

## 2026-09-06：停止候选完整 CI 通过，实机暴露并发查询接缝

精确候选 `e65b7aa0657dbc2d48bd7da9c29145b9d41e45b8` 的 [CI 34035050503](https://github.com/chiga0/marshal-harness/actions/runs/34035050503) 五项全部通过；前置 stop-chain race 回归也通过。随后唯一 [canary 34035979322](https://github.com/chiga0/marshal-harness/actions/runs/34035979322) 完成真实 Pi Start、server2 重启/rebind、原 Start replay，却在随后 Inspect 返回 `transport-failure`，尚未进入 cancel。因此不能把新 Collect/Close 衔接记作实机取消成功，也不原样重试。诊断 artifact 9990188334 已保存，完整 artifact 9990188687 保留。

代码核对发现 Inspect 不再等待 application 长事务后，`RepositorySession.InspectRun` 仍只执行一次 `AcquireExisting`；后台 deadline 检查可同时持有同 Run lease，原始 `ErrLeaseHeld` 会逃逸成 transport failure。现场仅有封闭分类，不能断言该次一定就是此错误。候选只对这个确定的 busy 错误做遵守原请求 context 的等待，其他错误立即返回；取得 lease 后仍重读 current owner/ledger，不读无锁快照、不加入 writer lane、不创建缺失 Run。真实 Run lease 回归验证写者释放后取得、等待取消不改 owner/不释放别人的 lease、预取消和缺失 Run。新增候选仍需动态 CI 与新的单次实机，B1 保持 IN_PROGRESS。

## 2026-09-06：动态回归发现停止后 Collect 的 report 衔接缺口

候选 `c118249` 的 CI 34033879517 结束：Linux quality、双架构 conformance 与 secret scan 通过；macOS 的 `TestLauncherV2TerminateUsesDurableBarrierAndRecoversLostReply` 在追加 SupervisorClosed 时失败。该回归已越过新增 cleanup Collect/丢响应恢复和 Close；不能据此派实机或把失败归为环境问题。旧比较要求 Close 的 report 与 Terminate 原报告完全一致，无法表达中间 Collect 封存输出及更新观察时间的合法事实链。

候选改为只对 sealed v2 stop 识别精确 process-terminal → Collect receipt → Close receipt；终态进程身份、exit/signal、runtime/workdir/source、observer 不变，Close 的完整 report 必须等于该 Collect report。普通完成/历史路径保留旧严格比较，不忽略输出字段或凭空接受新摘要。回归显式采用更新的时间/输出，并拒绝缺失 terminal/Collect 引用、身份或退出状态改变及未收集输出。新精确 CI/实机仍待验证；B1 未完成。

为缩短这类纵切反馈，`feat/b1-*` 的手动 macOS CI 在完整 quality 前先执行该精确 stop-chain race 回归；原五项检查、完整 quality/vulnerability 与实机准入不变。workflow 与 release-ci-contract 的固定白名单同步更新，不加入权限、自动发布或放行例外。前置失败仍会阻止整个 CI，不能用前置单测通过替代后续完整门禁。

## 2026-09-06：取消已终止进程，缺 cleanup transcript 导致 Close 失败

`f9c974d` 的 CI 34032441612 五项通过后，单次 canary 34033184062 已进入真实 cancel：RB1 sequence 22 为 barrier，23–25 为 Terminate intent/outcome 与 process-terminal，26–28 为 existing-worktree release intent/receipt 与 allocation-terminal，29 留下 Close intent，随后 fixed server 返回 `authority-conflict`。尚无 supervisor-closed、cleanup release、worker.stopped 或 Outcome，不能称为取消成功。小诊断包 9989307623 在数秒内返回上述事实，无须等待完整 executable 包才能定位执行阶段。

代码核对发现确定性缺口：真实 `darwinMechanics.Close` 要求 `terminal && collected`，停止 composition 在 Terminate 后直接 Close；正常结果链事先 Collect，停止链没有，而 terminal 测试替身未模拟这一前提。按 ADR 0081 的候选补充 cleanup-only Collect，沿同一 held owner/RB1 transaction 与 v2 intent/receipt/有界 transcript reader，在 Close 前保存证据；barrier 与业务接纳保持关闭，不生成 CommittedResult。测试补 sealed stop 负向矩阵、Collect 丢响应恢复一次、Close 未 Collect 即失败、无业务接纳及冷账本重放。保留旧 pending Close，不插队改写旧命令。该修复尚待新精确 source 的动态 CI/实机证据，不关闭 B1。

## 2026-09-06：查询候选消除 application 长互斥等待

后续调用链核对：`darwinRepositoryOwnerPhysicalLock.withHeld` 仍先执行不可取消的 mutex 等待，再检查 context，可能让到期查询排在长 owner transaction 后。候选采用有界间隔的 TryLock/context 等待；到期不调用 authority callback、不释放其他调用者的锁，取得锁后仍执行全部原始身份/runtime 校验。回归用明确的等待进入信号覆盖排队中取消、预取消、nil context 和锁归属；不是用 sleep 猜测并发时序。该改动仅解决进程内 owner 锁的排队取消，不能宣称正在执行的 filesystem/kernel 校验具有硬实时上界；同路径实机查询时延仍需测量。

`InspectRun` 原先与 Start/Collect/Verify/Cancel 共用 `adapter.mu`，即使 HTTP router 的只读请求不进入 writer lane，仍可能在长验证后排队。候选改为复用 `Status` 的 session lifetime 读锁；Close 仍同时取得 mutation/lifetime 写锁，Inspect 仍由 RepositorySession 获取精确 Run lease 并重新验证 owner/current ledger，不读取陈旧快照兜底。补充 mutation-held、Close 并发与无效输入测试；本地 compile-only/vet/staticcheck 通过，动态/race 尚待后继精确 CI。测试中的未 claim session 只证明不等待 application mutex，不证明有效 session 的全链路时延；底层 owner/storage 等待、同 Run 写冲突响应与实机查询延迟仍是 B1 开放项。

## 2026-09-06：取消失败定位到 fixed CLI activation 漏接线

完整 artifact 9988677757 已取回：34031227675 的 `call-1` 为 inspect/exit=0，`call-2` 为 cancel/exit=3、空 stdout。后者 stderr SHA-256 `bb8d1e32fe9bfd6c9b829425e953f6649875f6b436c9a56893a8dec7176fa5e7` 与固定诊断 `Marshal local dogfood gate 拒绝：self-local-command-denied。`（含换行）精确相等。因此请求在 CLI self gate 被拒绝，尚未进入 server；不是 Pi 未配置，也不是新的 stop runtime 失败。

根因是新增 handler/transport 未同步 CLI command classifier、activation 命令闭集和 Schema。此前 package 测试绕过了真实入口，完整 CI 绿色未覆盖这个调用链接缝。候选按 ADR 0081 补齐封闭 `control-plane-cancel`，并从 `RunContext` 使用真实生成的 activation 验证所有 fixed lifecycle 命令到达参数校验；取消缺 activation、旧命令集合仍拒绝，不增加 bootstrap 豁免。小诊断包已补调用摘要，避免再次为了几百字节诊断下载约 19 MB 完整包。动态 CI 与新的单次实机结果仍待此精确候选验证，B1 未关闭。

## 2026-09-06：取消候选越过 Start 重放，驱动响应诊断仍阻塞

`2422d14` 的 CI 34030543935 五项全绿（macOS fixedcontrolplane/cli 动态测试均通过）后，仅派发一次 34031227675。command-audit 已记录 server2 的 `received-replay` 和随后 `received-final`，越过 34029737648 的旧失败点；随后 T2 driver 报 `fixed-cli-invalid-response`。journal 保留真实 Start outcome（sequence=3），磁盘 state.json 仍是 READY/2 的旧投影，不用它覆盖 journal；无 stop intent、没有取消成功或冷恢复证据。server2.stderr 为空，不能猜测该次原始 CLI 错误。

小型诊断包 9988677224 已保留，但未包含 T2 调用元数据，必须等待完整包 9988677757（约 19 MB）才能区分初始 inspect 与 cancel 调用，这是可避免的诊断延迟。候选将既有 `driver-subject.json`、只含 operation/exitCode/stdoutSHA256/stderrSHA256 的 `call-*.json`、冻结 `cancel-request.json` 加入小型包，覆盖 t2 和 t2-recovery；不上传原始 stdout/stderr、transcript、配置或 executable，完整包保留。仅改善诊断交付，不重试旧 Run、不声称取消根因已修复。

## 2026-09-06：取消实机越过启动准入，但重启后的 Start 重放失败

候选 `5e0a8e30971e7cb89d937adf401317e945c4c5de` 的 CI 34029043931 五项全绿后，单次 [canary 34029737648](https://github.com/chiga0/marshal-harness/actions/runs/34029737648) 已启动真实 Pi，Run journal 到 `run.start-outcome/READY→RUNNING`，server2 重启/rebind 与查询成功；随后 Start 重放出现封闭 `transport-failure`，没有执行到取消、没有 stop intent/Outcome。因此既不是取消通过，也不是 Pi 未配置。诊断 artifact 9988209944 已保留；不原样重跑。

代码确认一个真实竞争窗口：resident deadline 循环在 application mutex 下取得 Run lease，而 HTTP delivery 的 Begin 在进入 application 之前先取得同一个 lease。只锁 application 无法覆盖 Begin/receipt，可能导致重放收到 lease-held/raw transport 错误；当前封闭现场日志不足以断言该次原始错误必为 lease-held。候选把完整 delivery 写事务与后台协调纳入同一 writer lane，保留 Status/Inspect 不阻塞、后台不排队、请求 context 和持久化权限检查。回归覆盖首次/重放 Start、Collect 的 Begin/receipt 阶段互斥与排队取消零意图；动态证据仍待新 source CI 和同路径实机，不用新测试替代业务可用证明。

正常主线不等待该隔离停止候选：PR #265 已合并为 `c93e31b`，main CI 34029534577 五项全绿，已单次派发正常 order-quote canary 34030199172。此时尚无新 ACCEPTED，B1 不升级。

## 2026-09-06：取消候选实机验证在 Worker 启动前被错误的发布准入阻塞

取消候选 `c1daeebb43541371d442e414ba830d59bf262ecd` 的 CI 34027878879 五项全绿。单次 canary 34028776232 却在 `Gate canonical repository and required CI` 失败：原脚本仅查 `event=push/head_branch=main`，而该候选证据来自 `workflow_dispatch`。失败在 candidate build、配置和 Worker 启动之前，零新业务 Attempt；这不是 Pi 配置或取消代码的实机失败，也不能归为实机通过。派发前未核对 gate 的事件合同，是本轮可避免的流程错误。

纠偏保持 release-ci-gate.sh 及正式发布权限原样，只给显式取消候选验证增加独立 candidate-only gate：canonical API、feat 分支、dispatch SHA=expected-head、最新同 SHA/分支手动 CI 已完成，五个必需 job 全部且唯一成功。较新的失败/运行中 CI 不能借旧绿逃逸；错误 head/branch/event、缺 job、重复 job、skip 或未知字段类型拒绝。正常业务/main 场景仍用原 main gate；不先合并未完成取消来满足测试准入。具体边界记入仍为 Proposed 的 ADR 0081。

## 2026-09-06：停止后的冷 server 验证候选

同路径故障测试补充：v2 Terminate 链原先使用普通 `attempt-failed` barrier，不能覆盖取消合同。现改为经公开 producer 提交的 sealed operator stop intent，再沿原真实 durable bootstrap/start/rebind 链验证 signal 丢回复、exact receipt 恢复、Close 丢回复与独立 absence、cleanup 冷重放；末端必须保留原 stop intent、admission closed 且没有 CommittedResult。Supervisor peer/内核观察仍为明确替身，不宣称实机取消；本地 compile-only/vet/staticcheck 通过，新 source 动态证据待 CI。

停止开发分支已推送 `984f45e`，同步 main `5bdec88` 并修正共享 release root adoption：正常完成要求已接纳结果，停止要求 sealed intent 与闭合 eligibility，二者都须在当前 owner 下重读 exact Attempt、release receipt 和 projection bytes。新增非耐久 sealed stop 拒绝反例；compile-only/vet/staticcheck 通过，动态证据待新 source，不借用 #264。

`order-quote-cancel` 场景现延伸至 server2 正常退出、同 bytes server3 冷启动、原请求/原 deadline 的精确取消重放、BLOCKED 查询和 stopped Collect。新 evidence 使用独立 `t2-recovery` 目录，拒绝二进制漂移、deadline 延长、参数注入、终态/receipt 改变；24 项 Python 回归通过。尚未派实机取消，仍缺 signal/cleanup 中途崩溃与业务超时同路径证明；本增量不能关闭 B1。

## 2026-09-06 10:23 UTC：正常 Collect receipt 修复已远端合并

PR #264 的 source `224409272eb9c30762b8b0e15a2fd730d38db0e8` 经 CI 34026422197 五项及全部附加检查通过，远端 merge SHA 为 `5bdec88d7161771caa2a556c70bbdef576375ff9`，pendingRemoteSync=false；main CI 34027276856 尚在途，尚无新 canary 或 ACCEPTED。同步到停止开发分支后，共享 cleanup 入口会调用新的投影观察校验；停止没有 CommittedResult，必须用已耐久的 stop intent/eligibility 证明其合法终态，不能跳过 root 校验。后续实现与 source 验证另记，不借用正常完成 CI。
## 2026-09-06：READY 准入动态通过，补停止场景的真实入口

`20a9999bdbe11a0eab899737651135e35c5422e5` 的 [CI 34026216770](https://github.com/chiga0/marshal-harness/actions/runs/34026216770) 五项全绿，覆盖原始预算准入及此前停止纵切。新增候选为 fixed CLI `cancel` 派生单一 request-key 绑定的停止请求，拒绝自由 PID/actor/reason；认证、当前 Run 绑定与停止权限仍由原 fixed server 校验。Collect 的已证明停止输出为 `stopped/run-stopped`，退出码 1，不冒充成功收集。

显式 `order-quote-cancel` 驱动复用真实 order-quote 的同一固定 server/启动恢复路径：要求取消后的 BLOCKED、Outcome 摘要、精确 receipt，成功后才执行一次同请求幂等重放，并验证 Collect 不再 pending、查询保持终态。未知响应立即保留证据退出，不自动重新取消；不创建 Decision、不输出 ACCEPTED。23 项注入调用 Python 测试及 shell 回归通过，不是真实 Pi 证据。取消后的重启、stop release/receipt 根绑定和完整故障矩阵仍待实现/实机验证；该选项尚未派发，ADR 0081 继续 Proposed，B1 不升级。

## 2026-09-06：停止纵切 CI 全绿，补 READY 原始预算准入

隔离候选 `a04d76c8239eb0a55822e01f7470ed9ff09a452a` 的 [CI 34025131805](https://github.com/chiga0/marshal-harness/actions/runs/34025131805) 五项全绿，包含两平台动态质量、两个 Linux conformance 和 secret scan。此前测试夹具混合历史 ProcessStarted/v2 reservation 的问题已用完整 Supervisor 启动/Collect 链修正，关闭借用的测试死锁也未再复发。这不替代真实 stop 故障矩阵，分支仍不合并。

本候选继续补 READY 预算门禁：preparation 在 ReserveAttempt 前读取冻结 TaskSpec 和首个 planning.spec-accepted；bridge 在真正进入启动链前再次检查相同 Run deadline，防止 preparation 后长时间等待消耗完预算仍启动。已提交 RUNNING outcome 保持原 exact replay，不因恢复时的新时钟拒绝历史结果。拒绝不创建 StopIntent，不伪造 ProcessStarted/Outcome；受控存储源缺失也不放宽。新增到期前 1ns/恰好到期/到期后、不同 Run head、preparation→launch 间到期的回归，更新旧 composition 夹具为同源 TaskSpec/首事件。该改动另需新 source CI；长 public mutation 的停止延迟、启动检查后到 Resume 的竞态、stop 的 release/receipt 衔接及端到端故障矩阵仍需完成。

## 2026-09-06：真实业务进入 VERIFYING，响应 receipt 尚未闭环

main `4f7311b` 的 CI 34023916927 全绿后，仅派发一次 [34024740089](https://github.com/chiga0/marshal-harness/actions/runs/34024740089)。小型诊断 artifact `9986706443` 已保留：Run event 第四条为 `worker.completed/RUNNING→VERIFYING`，authority 账本有 36 条事实并已到 `cleanup-released`。因此新 prompt 对本次真实输出有效，WorkerResult/接纳/清理已越过旧失败点；不能据此保证未来模型永不违约。随后 server 报 `commit-lifecycle-delivery/authority-conflict`，客户端报 `fixed-cli-invalid-response`。尚无 Verify、ReviewPacket、独立 Decision 或 ACCEPTED。恢复测试中 server1 的 Killed:9 是既有显式故障注入，不拿它替代本次 receipt 问题的根因。后继必须核对 receipt 的当前 Run/owner/目录绑定与提交链，不重新执行已完成业务，也不原样重跑。

取消纵切本轮接入 fixed server 常驻超时推进及 event 后 Outcome 恢复，并补有界公平批次、关闭取消、串行消费和不排队阻塞 public mutation 的测试。608ae0b 的两平台动态 CI 失败于新测试夹具混合 v2 reservation 与历史 ProcessStarted；已改用完整 Supervisor 启动/Collect 证据链。原 session Close 死锁未再出现在该次输出中，但最新常驻候选仍待动态验证；不得把 compile-only 当成故障矩阵通过。停止纵切继续隔离，不合入 main，不升级 B1。

## 2026-09-06：取消/超时后的 Collect 必须停止等待

继续 ADR 0081 纵切时发现：底层停止完成后返回 deadline sentinel，被通用 authority 映射改成 `authority-conflict`，fixed transport 又保留 pending；这会让客户端在已停止 Run 上继续等待。候选新增封闭 `run-stopped`，只在 terminal event、cleanup 和 Outcome 已验证后返回。已终态或 constructor 恢复中完成 stop 的 Collect 重放不重新打开已释放 worktree，而由 repository session 将原 current-request 连接到已存 stop intent，再执行同一终态核验。认证 HTTP 409 与客户端分类同时接通，零成功 receipt、零冒充业务 ACCEPTED。

新增认证 socket 的正常停止/部分投影测试，确保部分 projection 仍是 pending；同时覆盖无停止意图时不能从 Collect 发明 stop。compile-only、vet/staticcheck 已通过，动态证据待本候选 CI；608ae0b 的在途 CI 不覆盖这些后续改动。resident timer、READY 到期准入和全链路 fault matrix 仍未完成，不把该接口修补计作 B1 exit 关闭。

## 2026-09-06：Pi 输出修复已合入，停止纵切补业务接纳截止检查

PR #263 的 source `31b64a8` 经 Linux/macOS quality、两架构 Linux conformance、secret scan 及附加检查全部通过，已远端合并为 `4f7311b08bf59f6fad31aaae6661fc253ab0b0b4`。主线 CI 34023916927 仍在运行，尚未派发该新 head 的真实 canary；最近真实业务结果仍是 34009508838 的尾随文本拒绝，没有 ACCEPTED。该合并不改变严格结果解析门禁。

取消分支 `f41b3aa` 的 CI 34009447676 在 Linux 全部通过，但 macOS `TestReconcileStoppedRunRejectsInvalidInputAndClosedSession` 超时。堆栈显示测试在 delivery store 仍持有 session borrow 时调用 `RepositorySession.Close`，等待自己的读锁；不是 Pi 无响应。修复测试按生产资源顺序先关闭 store，再关闭 session，保留 owner 必须等待借用释放的约束，不增加超时掩盖问题。

本开发候选补充：恢复既有 stop intent 的 cleanup/Run/Outcome；从 frozen Task/首条 planning/ProcessStarted 读取业务 deadline；在 ingress 同一 durable admission transaction 内核对 Task 与 started 摘要及截止点；到期禁止 fresh admission，精确已提交结果仍允许重放。新增截止前 1ns、精确到期、到期后 1ns、冷 ingress 重放和来源漂移负例。这里只证明代码与编译检查进展，新增动态/race 证据待该候选 hosted CI。READY 到期准入、resident timer、完整故障矩阵和对外停止状态/错误闭环仍未完成，ADR 0081 保持 Proposed，禁止合并放行停止纵切或升级 B1 状态。

效率纠偏：在原开发分支保存完整纵切中间结果，CI 可提前发现平台问题；不为通过局部测试另造生产完成结论，也不重试未修复的同源实机失败。
## 2026-09-06：fixed server 真实业务首次独立 ACCEPTED

在 main `c93e31bde15d9dbcd3487dfc1db323eafc4127e1` 的 CI 34029534577 五项全绿后，单次 [业务 canary 34030199172](https://github.com/chiga0/marshal-harness/actions/runs/34030199172) 全部成功。真实 Pi 0.84.4 / `openai/qwen3.8-max` 通过 fixed server 完成订单报价纯函数；同 bytes Start 丢响应、server 重启/rebind/replay 后，沿 Collect→cleanup→delivery receipt→Verify→ReviewPacket→独立 Decision→终态查询走通。Run snapshot 为 `ACCEPTED/sequence=6`，第 6 条 event 为 `review.accept`；一次 Attempt、零 operational retry、零 rework。没有手改 `.marshal`、没有假 Decision、没有业务候选发布。

证据锚点：review artifact `9988370868`、diagnostics `9988482042`、完整 evidence `9988482580`；packet `sha256:61c35baf2a9ee1d5b1a9037482594b13a9c249e41936964113d333c782940338`，Decision `sha256:b0a13645291bdf90d6b5b0171676f5e30685ed2a8bba44c404785d7f5f2c7ffe`。[维护者提交的独立 Decision 原文](https://github.com/chiga0/marshal-harness/issues/186#issuecomment-5558942471) 由仍运行的同一 server 验证并接纳。reviewer 读取冻结 Task/完整 patch/VerificationReport/ArtifactManifest/WorkerResult，复算 capture 及 canonical 摘要，独立执行绑定候选的 28 项 oracle 和 500 组额外确定性业务断言，全部通过。Worker 明确没有运行 shell 验收，其自评未被充当权威证据。

耗时与边界：Run 创建到 ACCEPTED 约 541 秒，其中 WorkerResult 记录执行约 69 秒、verification 到独立 accept 约 458 秒。后者含 reviewer 读取/下载/审查及递交时间，说明下一阶段应及时消费 review-ready，而非增加 Worker 重试；这只是单样本，不宣称团队加速或生产成功率。旧 carrier 失败保留；#265 只细分失败原因，本次合法输出通过不代表它修好了所有模型输出。当前关闭 B1 正常交付子条件，不关闭取消/超时、B2 Agent Team、B3 长时恢复/签名/Linux/stable；不升级历史 COMPONENT 或 ordinary-user 信任等级。

停止候选另有真实反馈：`5e0a8e3` 的 CI 34029043931 五项成功后，34029737648 已启动 Pi，但 server2 Start 重放出现 transport-failure，尚未调用 cancel。代码发现后台只锁 application，delivery Begin/Commit 可在其外侧竞争 Run lease；现场封闭日志不足以断言原始错误必为 lease-held。`2422d14` 在原停止分支补整个 delivery 写事务与后台统一协调，保留只读查询、context 和 durable authority；本地 compile-only/vet/staticcheck/架构检查通过，CI 34030543935 在途，不原样再派失败候选。

## 2026-09-06：Collect receipt 修复合入，新的 Pi carrier 失败尚未定位

PR #264 的 sourceHead `224409272eb9c30762b8b0e15a2fd730d38db0e8` 已合入 `main@5bdec88d7161771caa2a556c70bbdef576375ff9`，pendingRemoteSync=false；source CI 34026422197 与 main CI 34027276856 五项全绿。单次后继真实业务 canary [34027927457](https://github.com/chiga0/marshal-harness/actions/runs/34027927457) 失败于 `pi-result-final-content-shape/authority-conflict`，未产生 worker.completed、VerificationReport、ReviewPacket 或 Decision。七次 pending 是同一请求的观察，不是七个 Attempt。journal 的 RUNNING/sequence=3 为权威，state.json 的 READY/sequence=2 只是尚未刷新投影；本次尚未走到新 Collect receipt 代码，不能宣称其实机出口通过。

小型诊断 artifact `9987677079` 与完整 artifact `9987677324` 已保存到 GitHub，但两者的上传白名单均不含 supervisor 原始 transcript。固定 Pi 0.84.4 bundle SHA-256 `5406c369954516fb56879d685e082ff9095cd6e06e41af406f394942377fd4bf` 对应 producer 的 assistant content 为数组、agent_end 原样携带 messages；这不能替代本次现场内容。现有 Go 解码错误把非数组容器、非对象元素、type 字段类型错误和 text 字段类型错误全部压成一个码，无法判断是哪一种；不得直接认定 provider 配置错误、接受字符串载体或假设 thinking 字段为根因。

本候选仅根据既有 json.Unmarshal 的失败元数据区分 container/item/type/text 四种封闭错误码，未知元数据仍回到旧码。不输出值、未知字段名、正文或凭证，不另行解码/归一化/重试，既有成功及失败集合不变。回归覆盖容器、元素、字段及 null/空数组负例和未知错误兜底；本地仅编译检查与 vet，不算动态通过。目的为解除真实业务阻塞所需的定位缺口，不计为业务交付，不升级 B1。

取消/业务超时独立分支 `feat/b1-stop-lifecycle@c1daeebb43541371d442e414ba830d59bf262ecd` 已推送，[CI 34027878879](https://github.com/chiga0/marshal-harness/actions/runs/34027878879) 五项全绿，新增真实 sealed StopIntent 的 terminal 故障链测试和取消后 server 重启查询驱动。仍未合入、ADR 0081 仍 Proposed，未运行真实取消/业务超时 canary，不用测试通过代替实机出口。

## 2026-09-06：真实 Pi 已到 VERIFYING，修复 release 与 fixed delivery 的观察衔接

候选 `c6a1609` 的 CI 34025737001 在 Linux 的既有 `TestSuperviseOnceJSONCarriesCompleteDecisionFields` 失败（exit=1，stderr 空）；该测试未输出 JSON 模式的 decision.error，故现场原因尚不能确认。代码核对发现测试子进程的一次非阻塞 Acquire 会与 readiness 的短暂 flock 探测竞争；夹具现仅对 ErrLeaseHeld 有界重试，其他错误仍立即失败，并在断言失败时输出自身生成的 decision JSON。生产获取锁/启动逻辑不变，不能把候选假设记作已证实现场根因；新 source CI 仍是放行条件，不原样重跑取巧。该次 Linux 的 productionruntime 测试通过也不能抵消整套 CI 失败。

PR #263 source `31b64a84b50e41f29f773c31762c0c29b2bc58a5` 经检查后合入 main `4f7311b08bf59f6fad31aaae6661fc253ab0b0b4`；main CI [34023916927](https://github.com/chiga0/marshal-harness/actions/runs/34023916927) 五项通过。唯一后继真实业务 canary [34024740089](https://github.com/chiga0/marshal-harness/actions/runs/34024740089) 已产生 `worker.completed`、`RUNNING→VERIFYING`，RB1 共 36 条事实且末条为 `cleanup-released`。说明该次 Pi 输出已完成解析、结果接纳和终态清理，但尚无 VerificationReport、ReviewPacket、Decision 或 ACCEPTED。

失败为 `commit-lifecycle-delivery/authority-conflict`；小型诊断 artifact `9986706443` 保留。七次 `attempt-still-running` 是同一请求的观察，不是七个新 Attempt；脚本主动注入的 server1 `Killed:9` 是既有重启测试，不是此次根因。不得混用旧的尾随 JSON 失败或声称 Pi 未配置，也不原样重跑这个 head。

调用链存在确定的不匹配：existing-worktree release 通过 `RENAME_SWAP` 原子更新 RB1 派生投影，改变 `runtime-v1` mutation observation；fixed server 只在 preparation 后采用受控更新，terminalization 后仍使用旧观察，交付层因而拒绝 receipt。候选在完整 `cleanup-released` 后、`worker.completed` 前增加同一观察衔接：当前 owner/精确完整 Attempt/当前 Run/已提交 release receipt/完整 RB1 snapshot/held graph 投影字节全部吻合，才调用既有 root adoption。未知 sibling、原 store 替换、control ABA、旧 owner 仍拒绝；不跳过 receipt，不新建 authority，不改变 ADR 0069/0076 的生命周期或信任边界。

回归覆盖投影交换后的 receipt 拒绝、受控更新后的 exact receipt/replay，以及无 durable Attempt 时伪造 terminal 字段不能改变观察；既有 allocationcontrol 的 exact-byte/损坏投影和 fixed-root ABA 测试继续约束边界。仅编译检查与 vet 通过不能关闭缺陷；需 hosted 动态/race 门禁，再进行一次 exact-main 真实验证。B1 保持 IN_PROGRESS。取消/超时另存 `feat/b1-stop-lifecycle@a04d76c`，CI [34025131805](https://github.com/chiga0/marshal-harness/actions/runs/34025131805) 为独立证据，不混入本候选，也不因 WIP 已推送宣称其出口完成。

## 2026-09-06：最终 JSON 后有非空白内容，前移输出格式约束

PR #262 source `8de9648` 全部检查通过后合入 main `5945b6854220eb86b229e19efe3e883a17556a48`；main CI [34008933865](https://github.com/chiga0/marshal-harness/actions/runs/34008933865) 五项通过。唯一后继实机 [34009508838](https://github.com/chiga0/marshal-harness/actions/runs/34009508838) 在三次同请求 `attempt-still-running` 观察后失败于 `pi-result-final-object-trailing`，小型诊断 artifact `9982033321` 已保留。启动、恢复、transcript 与最终 assistant 消息解析均已越过原屏障，但尚未进入 WorkerResult Schema/独立 Verification，没有 ReviewPacket、Decision 或 ACCEPTED。不能只读旧 `state.json` 的 READY 快照忽略实际 RB1 启动事实。

本次分类证明最终文本内一个 JSON 对象后仍有非空白内容；公开诊断不包含原始 transcript，不能断言它具体是 Markdown 围栏、结束语，或对象是否已满足 WorkerResult Schema。现有 prompt 只要求“exactly one WorkerResult JSON object”，没有明说禁止围栏/尾随报告，也没有说明所有解释必须进入 JSON 字段。本候选补齐这些 producer 约束与发出前的整条消息检查，保留完整 schema 模板；解析器和 ADR 0075 的尾随内容拒绝规则不变，新增围栏/尾随报告负例。提示词改善不等于确定性保证；新候选仍需 hosted CI 和单次真实验证，禁止把尚未发生的成功写入 B1。

取消/超时另在 `feat/b1-stop-lifecycle` 开发分支保存：`f41b3aa` 已推送并启动 [CI 34009447676](https://github.com/chiga0/marshal-harness/actions/runs/34009447676)，未建合并 PR、未放行该未完成纵切；不混用它与本次实机的 sourceHead。下一步仍优先真实业务 ACCEPTED，同时完成停止/业务 deadline 与自动恢复。

## 2026-09-06 03:01 UTC：最终消息提取拒绝，修复 Pi 消息联合类型适配

PR #261 source `01c4a54` 经 CI 34006591660 全绿后合入 main `80dc39f`，main CI 34007198729 也五项全绿。单次真实 canary [34007829215](https://github.com/chiga0/marshal-harness/actions/runs/34007829215) 运行后返回 `pi-result-final-message/authority-conflict`；此前十次 `attempt-still-running` 为同一冻结请求的允许观察，不是十个 Attempt。没有 ReviewPacket、Decision 或 ACCEPTED。诊断 artifact 9981544614 保留，未原样重试。

对照已安装 Pi 0.84.4 的公开 `pi-ai/dist/types.d.ts` 与 `core/messages.d.ts`，UserMessage/CustomMessage 的 content 合法类型为 string 或数组，而 AssistantMessage 为数组。现有 `extractFinalWorkerResult` 却把 agent_end 中所有消息都解码为 assistant 数组，合法历史 user/custom 字符串可在选取最终 assistant 之前触发拒绝。修复把历史 content 保存为 raw JSON，只对选定的最终 assistant 执行既有严格内容校验；并没有从 user/tool 文本抽取 WorkerResult，也不允许最终 assistant 字符串、toolCall、重复 text、多个 JSON 或尾随内容绕过校验。新增整条生产 parser 回归覆盖合法历史联合类型和各项反例。

现场没有公开原始 transcript，因此当前只能证明这个实现缺陷存在且与失败阶段相符，不能断言是此次现场的唯一原因。最终事件解码、空消息、角色、内容形状/类型/text 数量新增封闭分类以避免下一次仍只有笼统信息；分类只来自 Core 分支，不输出内容、路径或错误原文。新候选须独立通过 hosted 动态门禁，再跑一次真实业务路径；B1 保持 IN_PROGRESS，B2/B3 不变。

## 2026-09-06 02:09 UTC：Collect 阻塞缩小到 Pi 结果解析

main `13f42e9` 的 CI 34005015027 五项全绿后，一次性条件派发执行 canary [34005690401](https://github.com/chiga0/marshal-harness/actions/runs/34005690401)。固定产物、Pi/provider 配置及 live-review 依赖安装通过；实际 Start/重启链完成，RB1 23 条事实含 `transcript-collected`（stdout 574166 bytes）。新增阶段诊断明确为 `parse-production-worker-result/authority-conflict`：本轮已经通过 held transcript 读取，失败在 Pi parser，不是后继 observation 或 ResultIngress 接纳失败。没有 ReviewPacket、独立 Decision 或 ACCEPTED。诊断 artifact 9980863196 已本地取得，完整 artifact 9980863392 远端保留；不重跑该 head。

仍缺 parser 内部失败分类，原始 transcript 没有对外归档，也不能为了定位把潜在凭据/Worker 原文直接打印。后继补充私有来源的封闭分类（输入、协议、输出上限、provider 终态、最终对象、声明 Schema/identity/session、规范化），Schema 仅输出有限白名单字段类别；公共 composition 转为原有 `authority-conflict`，原始 Error 不进入 HTTP/CLI 日志。保留原校验、归一化和 ErrProtocol 的内部错误身份，不放宽格式、Schema 或结果接纳。回归同时覆盖成功保持、各类失败零结果、错误文本不能冒充分类、Schema 错误环/遍历预算，以及实际 CLI 日志的脱敏出口。这是定位修复，业务根因仍未确认，不能计为 B1 通过。

## 2026-09-06：同机评审载体合入，保持实机根因与发布边界

PR #260 的 source `f52e958` 经 CI 33977253462 全部五项通过（含 macOS/Ubuntu 动态质量与 Linux 双架构检查）后，远端合并为 `13f42e9`；main CI 34005015027 单独验证。旧 canary 的原始 transcript 没有进入失败 artifact，因此不能声称已离线证明 Pi 解析或 ResultIngress 的具体根因。后继只允许针对新增封闭阶段诊断的一次受控实机定位，不重启旧失败 Run、不放宽接纳。

核对本机已安装 Pi 0.84.4 的公开程序文件，其 agent_end producer 确实增加 `willRetry`，print-mode 输出 session header；当前未发现这两处生产/解析契约不符。此排查不能替代失败 Run 的原始输出。正式支持的独立外部依赖也已核对：#212 仍开放，仓库 secret 名称清单没有签名/notarization 配置；只读取名称，未读取或输出凭据。该缺口不阻止 B1/B2，但 B3 未满足前不能发布 stable 或声称企业 EDR 必然允许。

B2 复用范围核对：`internal/goal/admission.go` 的 evaluator 可复用，但 `AdmissionAudit` 当前仅为 mutex 保护的进程内事件列表；非测试 `cmd`/`internal` 源码尚无 `internal/goal` 导入。不能据组件单测宣称 approved plan 已耐久接纳或可恢复调度。后继按 ADR 0080 接入同一个 fixed server 与现有存储，以确认方案→耐久接纳→幂等 Run 物化为首条纵切，不先扩大并发或再造旁路 controller。

## 2026-09-05 15:44 UTC：真实 Start/重启已前进，Collect 接纳仍失败

`a6fe94f` 的 main CI 33974743678 五项全绿后，单次真实 Pi 订单报价 canary [33975628490](https://github.com/chiga0/marshal-harness/actions/runs/33975628490) 已实际执行。Start 丢回复、RUNNING 查询、server1 crash、server2 ready、owner rebind、原 Start replay 与恢复后查询全部完成；此前目录布局根因没有复发。RB1 23 条 fact 包含成功 `transcript-collected` receipt（stdout 788827 bytes），但随后 Collect 接纳返回 `authority-conflict`，没有 result-admitted、Verification 或 ACCEPTED。runstore 的历史 snapshot 仍为 READY seq 2，不能用它抹掉 sealed RB1 的运行事实，也不能伪造推进。

目前证据不能区分 transcript 读取、Pi 协议解析与后续 ResultIngress 接纳的具体失败点；外层 `fixed-cli-invalid-response` 不是根因。完整 artifact 9972244043 和独立诊断 artifact 9972243598 均保留，本地已取得；禁止推测为 provider 未配置，禁止原样重跑。诊断包 27496 bytes 优先上传，恢复阶段错误可在数秒内获取；完整包仅用于补充取证。

同宿主评审载体按 [ADR 0082](adr/0082-fixed-server-live-review-carrier.md) 接线：默认关闭，明确 opt-in；原 shell/server 保持存活，上传 review-only immutable artifact，读取 canonical owner 的精确未编辑评论，原文递交既有 Decision Port。9 项 Node 与 18 项 Python 测试已通过，不代表真实 ACCEPTED。执行环境不继承 GitHub/Actions token；载体没有写 Issue/发布权限。当前 Collect blocker 与取消/超时仍需关闭，B1/B2/B3 状态不提升。

针对缺失的根因定位，在 Collect 的 transcript、parser、worktree observation 和 ingress 构造边界补充封闭 operation 名；保留已有 typed reason，不输出原始 Worker 内容。这只是诊断改进，不能算作接纳根因修复。本地 Go 仅 compile-only、vet/staticcheck，动态与 race 仍由 hosted CI 验证。评审载体锁定 `@actions/artifact` 6.2.1；依赖审计当前为零已知漏洞，不沿用检查时发现漏洞的旧依赖版本。

## 2026-09-05：目录修复合入，补齐同机独立 Decision 验收入口

PR #259 的新 source `c1590a5` 经 CI 33973809127 五项全绿，15:25 UTC 已远端合并为 `a6fe94f6dd3fe8a177e50f1644f4c74ae0096a6f`。首轮 33972990414 的 Ubuntu recorder 竞态已保存并按根因修复，不归为 Pi rework，也未重复启动业务 Attempt。合并 main CI 为 33974743678；真实 Start/恢复/业务验收仍待后继。

B1 调用链核对发现现有脚本在 ReviewPacket 后正常退出 server，因此仅靠上传评审包不能完成同机 ACCEPTED。后继接线保留原 server/owner，等待独立 reviewer 发布完整 Decision，再调用既有固定 CLI `control-plane decision`：核对当前 packet/head、一次提交、receipt 与 Outcome，再 inspect 相同终态。拒绝/rework Decision 不被转换为 accept；无效或未知响应不自动重复 mutation。等待采用固定 monotonic 上限，缺文件仅表示等待，半写/重复键/链接/特殊文件立即拒绝；就绪标志在评审包和说明文件关闭后才创建，避免重演 recorder 竞态。

当前 18 项 Python 定向测试证明客户端行为，不证明实机业务、独立评审或发布。GitHub-hosted 同机评审交付通道尚未整体接线，未启用；不能借 RC1 的另一 runner finalize 或导入 review-only 包替代本条 server authority。此改动只复用既有 Decision Port，不引入新的业务 ledger 或自动签发 Decision。

## 2026-09-05：B1 生产目录布局与隔离夹具脱节

PR #259 的首轮 CI 33972990414 中 Ubuntu 动态检查暴露旧 notification recorder 夹具竞态：`TestNotifyHookGateHonoursFirstEventAndSameState` 在最终文件刚被 shell truncate、JSON 尚未写完时读取，报 `unexpected end of JSON input`。失败发生在未改动的 runstore，不能算成目录修复失效，也不凭“偶发”原样重跑。修复测试 recorder 先写独立 staging leaf、完成后 rename 发布；不放宽 JSON 断言、不增加等待时长、不改生产通知语义。未中断 macOS：其于 15:05 UTC 动态/race 全绿，Linux 双架构与 secret scan 同样通过；聚合完整结果后更新同一 PR，新 head 仍单独验证。业务 canary 未提前派发。

新 canary 33971611314（`d9d603f`）在 14:23 UTC 失败。artifact 9971098258 的精确成员经 ZIP CRC 检验读取：server 输出 `adopt-existing-worktree-projection-root/authority-conflict`；RB1 有 17 条 fact，包括 prepared execution、supervisor bootstrap/started、三组 command intent/outcome 与 seq 15 `process-started`。这证明阻塞已在启动后，不证明业务完成、ACCEPTED 或旧 137 根因。

根因是 `adoptFixedServerRuntimeMutation` 要求 runtime 只有两个目录，而实际 CLI 先创建 `result-ingress`、`dispatch-ledger`、`allocations`、`owner`、`provider-authority` 等同层目录。原公开装配夹具把 ingress 放在 repository 外，隔离 delivery 夹具只有两项，掩盖了必然失败的集成路径。修复冻结已存在的封闭 composition 目录的 held descriptor 与 current name/object；只容许其正常内部记录变化，不接纳启动后新增名称、对象替换、类型/权限漂移。control 与祖先原有精确 mutation/ABA 门禁、RB1/projection bytes join、receipt digest 格式均不变。这是恢复 ADR 0066/0076 已有布局，不增加 authority store 或降低业务证据条件。

公开装配回归改用真实 runtime ingress，delivery 回归默认七目录，新增 projection swap、晚插入已知名称、等 entry count 的 store/control 替换、control ABA 和 store append 用例。新根因修复尚待 hosted 动态/相关 race 和一次真实业务复验；本地 compile-only 不冒称执行测试。

效率教训固化：目录 producer 和消费校验必须共享真实布局测试，不能只验证 isolation fixture；失败诊断先单独上传小型 stderr/RB1/state/delivery artifact，再上传完整 binary/evidence，避免本次 19 MB 下载成为定位的串行瓶颈。完整包保留，诊断不包含配置密钥、transcript 或二进制；没有新建微切片 PR 或重复业务 Attempt。

## 2026-09-05：B1 诊断修复合入，实机根因仍开放

PR #258 的 sourceHead `dc2ed51bd61a110f8511b9959b7a2aa34eb2f777` 已通过 CI 33969959998 五项检查（含 macOS/Ubuntu quality、Linux 双架构和 secret scan），于 14:02 UTC 远端合并为 `d9d603f9353fea828e939c172faf1aa87eb488cd`。它关闭错误原因被掩盖与 artifact 路径错误，不关闭业务 Start。合并基线 CI 33970630902 与后继单次 canary 按依赖继续；旧失败 33968513566 不原样重跑、不补造其缺失 ledger。以下候选/等待描述保留为历史采样，唯一当前状态见 Roadmap 顶部表。

主线 CI 于 14:18 UTC 整体 `completed/success`，但 Secret scan job `101318382429` 的 REST 单项、jobs 列表及 GraphQL 均仍为 `in_progress/success`，同时携带 14:02:49 UTC 结束时间且全部步骤已完成。其余四个 job 已正常 `completed/success`。单次 canary 派发检查因此正确停止，未启动 Worker；不把该外部状态不一致归为 Marshal 代码失败，不修改 required-CI 门禁。保留旧 job 证据后，仅请求重跑该已结束的 Secret scan job，避免整套 quality/业务重做；后继结果另记，不覆盖首个采样。

定向 job 重跑后，33970630902 最新五个 job 均为 `completed/success`；仅在整体 run、精确 head、五项状态、current main 和该 head 零既有 canary 全部通过后，实际单次派发 order-quote run [33971611314](https://github.com/chiga0/marshal-harness/actions/runs/33971611314)。这是补齐诊断后的新版本实验，不是旧业务 Attempt 重试或已验收结果；尚未声明 Pi 启动或 ACCEPTED。

## 2026-09-05：S3 合入与 B1 业务验证开始

PR #257 已于 13:05 UTC 合并，sourceHead `05571f3174fdda4891d25a7e271a807ab0f6a38e`、mergeHead `cb4b6464fe10730f8abaa40b6bca1c60ab8537bc`。source CI 33967017702 与 mergeHead main push CI 33967908642 均五项通过；随后自动派发同 head 的 fixed server order-quote canary 33968513566。维护者定向源码检查与独立执行的 CI 不冒称独立业务 Decision；Pi 候选仍由未编写它的 reviewer 基于精确 packet 审核。该合入关闭 S3 源码/合并等待，不关闭 B1。

canary 随后失败：13:19 UTC 的外层检查报告 `fixed CLI did not reach its stdout response write boundary`。固定 Marshal 构建、Node/Pi 固定版本与 provider 配置步骤成功；现场 artifact 9970201332 已上传，未原样重跑。该提示只证明没有到达预期的 SIGPIPE/输出失败边界，不能据它认定具体 runtime 根因、Pi 已运行、或本机 137 原因复发。下一动作是核对 artifact 中的原始 Start stderr、Run 与 RB1 阶段，而非调整 timeout 或跳过 response-loss 检查。

artifact 解包后的实际证据：Run 停留 `READY/sequence=2/currentAttemptId=null`；Start stderr 表示结果未证明成功；server stderr 只有 `reconcile-start-run-delivery/authority-conflict`。源码核对确认两个诊断缺陷：reconcile 失败分支丢弃先前 `startErr`，日志又只取最外层 typed error；工作流误收集 `.marshal/result-ingress`，真实布局位于 `.marshal/runtime-v1/result-ingress`，因此本次缺少关键 RB1，不能从缺文件推断零 mutation。当前修复保留两段内部原因、最多 32 个错误节点/8 个去重结构化诊断、Run composition 的常量阶段与正确 artifact 路径。原始错误不输出；composition wrapper 不改变原有 application reason/HTTP 分类。测试覆盖双失败无成功 receipt、路径/换行/非法字段脱敏、循环链及输出预算、原有 reason 保留。脚本回归、Go compile-only、vet/staticcheck 已通过；Go 动态/race 与新证据仍待后继 CI/实机，业务根因尚未证实，B1 不升级。

取消/超时的下一纵切已在 ADR 0081 草案中补齐具体选择。调用链发现 reservation 没有时间字段，因此不能从不存在的 reserved-at 或恢复时 now 推导业务预算；拟复用已冻结的 Task budgets、Run 创建时间、当前 ProcessStarted 观测时间，deadline 检查同时进入结果接纳而非仅靠 timer。stop intent 与 admission closure/generation bump 同一 RB1 barrier 原子化；cleanup 完成才生成 Run terminal event 和可恢复 Outcome。该草案尚未接受或启用，不把文档方案算成 runtime 交付。

效率纠偏：Roadmap 的“当前表”此前累积多条互相矛盾的历史 CI 采样，容易误判还在等待早已完成的作业；现将它们明确标为历史，只保留顶部唯一当前表。历史证据未删除；不新增另一个 controller、计时状态库或微切片 PR。

## 2026-09-05：实机前补齐独立评审输入

验证更新：`6e62c8e` 的 CI 33966029736 与 `034a0f7` 的 PR CI 33966320451 五项全部通过，前述工作流契约和 Terminate fixture 失败已由新 head 关闭。等待后者真正结束后才推送本次 Python 打包增量，没有中断既有检查；本次增量单独记证。

PR #257 已建立，`034a0f7` 的 CI 33966320451 正在运行。进一步沿 ReviewPacket producer 和 artifact upload 核对发现：原 T1 artifact 只包含 Run state/events、ingress journal 和客户端响应；T2 响应内的 packet 只引用输入文件，不内嵌实际 patch/报告/WorkerResult。若直接派业务 Pi，runner 退出后独立 reviewer 将缺少原始评审输入，可能被迫重跑。这是当前 B1 验收的直接阻塞，不另起产品路线。

现有 T2 driver 增加成功验证后的有界只读打包：固定四类输入、原始 packet、最多 16 个规范 WorkerResult 路径及 packet 指定的候选记录；单文件 8 MiB、总量 64 MiB。逐层使用 no-follow directory FD，拒绝 symlink、hardlink、FIFO、超限、缺失、packet 漂移和覆盖已有包；tar 仅含生成的普通数据成员，解决 Attempt ID 的冒号不适合作为 artifact 直接文件名的问题。新增正反例覆盖实际 bytes、摘要目录、敏感无关文件排除与上述拒绝分支，驱动合计 9 个测试通过。未复制原始日志/凭据，未授予 authority import、Decision 或跨 runner 恢复权限；真实 Pi 和独立 ACCEPTED 仍开放。

## 2026-09-05：业务驱动 CI 契约遗漏

本地 `release-ci-gate_test.sh` 已通过，`6e62c8e` 提交后固定 checker 也已通过，CI 33966029736 已越过原失败步骤。准备实机时进一步核对到两个前置：现有 canary 用的是 main push CI gate，分支手动 CI 不能替代（须评审合入后再运行）；Python `isoformat` 的小数尾零会被 CLI 的 canonical RFC3339Nano 比较拒绝。当前按相同瞬间去掉小数尾零，新增整数秒/尾零/六位小数回归，7 个驱动测试通过；不延长 deadline，不放宽 CLI parser。实际 Pi 尚未启动。

`8c37fff` 的 CI 33965649054 为失败：secret scan 通过，其余四个 job 均被同一个精确工作流契约差异挡住，未得到本候选 Go 动态回归的绿色证据。新增 T2 Python 测试步骤时遗漏更新 `release-ci-contract.py` 内的封闭工作流副本，不是四项独立产品故障，也不能靠重跑解决。当前同步唯一新增步骤，绑定 T2 driver/test/task 的固定路径、模式与 HEAD bytes，更新契约夹具并增加 driver 脏字节拒绝反例；原 publication 权限和门禁不变。

效率纠偏：固定工作流改动必须同时运行本地 `release-ci-gate_test.sh`（已包含 committed fixture 的正反例），提交后再执行固定 checker 校验真实 HEAD，然后才推送/派发 CI；仅验证 YAML 语法不够。T2 驱动回归加入现有 `fixed-server-t1-check`，避免本地常规检查遗漏。真实业务 Pi、独立 Decision/ACCEPTED、本机 fixed image 退出 137、服务端业务取消/超时仍未关闭，不升级 B1。

## 2026-09-05：终止夹具权限纠偏与 T2 有界业务驱动

`c9b2d11` 的 CI 33964259197 最终四项通过、macOS quality 失败；失败为 `TestLauncherV2TerminateUsesDurableBarrierAndRecoversLostReply` 的 process terminal append 返回 cleanup unauthorized。夹具把已完成信号效果后的“追加进程终态事实”误写为 `CleanupTerminate`；既有合同只允许此处 Inspect/Reconcile，Terminate 用于 allocation 终止。当前改用 Reconcile，并增加错误权限零账本写入断言；没有放宽 production append allowlist。该修复需新 head 动态回归，不能重跑旧 head 当作修复。

为直接得到业务交付证据，现有手动 fixed server canary 增加 order-quote 分支：复用相同构建/CI/Pi/认证/启动/重启路径，再经固定 CLI 客户端驱动 Collect→Verify→ReviewPacket，不引入另一个 runtime。准备驱动时发现“仍在运行”在 application→HTTP→CLI 之间丢失，全部退化为未知 pending，无法安全决定是否等待。当前只为真实 `ErrAttemptStillRunning` 映射 closed reason，响应仍为 HTTP 202 pending、无成功 receipt；客户端必须重验 peer/owner，错 operation 或混有成功 payload 拒绝。CLI 输出明确 pending JSON 并保留非零退出码。未知错误保持未知，不能按同一循环盲目重试。

驱动记录精确请求、deadline、观察次数和阶段结果，只在明确仍在运行时等待；错误结果、摘要/Attempt/sequence 漂移、普通 pending 或 verification fail 不自动返工。首批 6 个 injected-call 回归覆盖等待、冻结请求、未知失败零重试、deadline 不延期、receipt 漂移、失败 verification 保留 packet 与最终查询漂移。另增加真实 Unix socket 的 pending 分类和伪造 envelope 拒绝测试，Go 动态执行仍由 exact-head CI 完成。本候选尚未实机验证，没有自动 Decision，也没有关闭 B1 或本机退出 137 的独立阻塞。

## 2026-09-05：fixed image 实机阻塞与业务取消合同缺口

`cb615e7` 的 CI 33963625091 五项全部通过，前次两个 fixture 失败已由该 head 的动态回归验证；最新 `c9b2d11` 另跑 CI 33964259197。没有把前一绿色结果扩展到后一提交或实机。

对精确 `c9b2d113f428f66c6f4352758dcf22f61a53665c` 在独立工作区构建固定 `bin/marshal`，使用 `darwin-local-dogfood`、`dev`、buildDate `2026-09-05T11:49:41Z`。SHA-256 为 `0e4ff2cfefe0b42fb7e67de1248fb4fae9fcf2799c08481018f693a98cc90780`，大小 17980578 bytes。执行 bootstrap 命令组（version/doctor --self）返回 137，无输出，不能证明已执行 activation 或任何 lifecycle。磁盘 codesign verify 成功，但为 linker ad-hoc、Identifier `a.out`、无 TeamIdentifier；codesigning identity 查询为 0。限定 amfid/syspolicyd 的近三分钟日志未提供匹配原因，因此只记录 SIGKILL 型退出，**不认定已查明是 EDR、Gatekeeper 或内存终止**。未改变安全设置、未重新签名/替换该候选、未直接启动 Pi；产物留在被忽略的固定 bin 路径。固定 pathname 并不自动解决执行信任，managed signing 仍为外部前置。

同时确认 B1 取消缺口是跨层合同而非单个缺失 handler：Port 没有 cancel；已有 runtime terminalization 仅正常完成；历史 reducer 明确拒绝 RUNNING 的 run.aborted；reserved lease 初始两小时并非用户确认的 wall-timeout。新增 ADR 0081 提案列明原子 stop intent、结果竞争、cleanup、Run Outcome 和 deadline 的待冻结选择，避免再次先接 handler 后返工。它尚未接受或改变运行时权限，不升级 B1 成熟度。

## 2026-09-05：取消链路与 legacy receipt-only 写旁路

逐层追踪发现 v2 Attach continuation 原先没有暴露已有 `terminate` command，ResultIngress 也只有 Inspect/Close producer；不能据 mechanics 支持信号而宣称服务端取消可用。当前在既有 ADR 0056/0067/0079 terminalization 合同下补 typed Terminate continuation，复用相同 owner 锁、RB1 barrier/intent/outcome、exact pending 分类和 post-checkpoint 认证，不新增 command schema 或生命周期状态。没有 barrier、owner binding 漂移或 intervention 时拒绝，原 command deadline 不在恢复时刷新；已有 receipt 不重复发信号。连续耐久测试增加未 Collect 的运行任务到 Terminate、丢回复恢复、终态释放和冷重放；peer/mechanics 仍显式 Fake，服务端 cancel/timeout 编排及实机故障矩阵尚未关闭。

另发现只禁用 legacy socket 客户端不够：已提交 Close 的只读 receipt 恢复仍可能追加后继 RB1 fact。生产 rebind/collect/terminal 共用的 held-directory 入口现在在首次写入及 Core/目录 IO 前拒绝 legacy started generation；显式历史解析/测试 helper 保留，不转换历史证据。这是 ADR 0079 v1 只读约束的实现补漏，不扩大权限，也不代表已部署新的 fixed image。

## 2026-09-05：S3 终态夹具纠偏与旧入口退役候选

CI 33955604098 的 macOS quality 暴露两个契约错配：Inspect 观察到 terminal 只能提供 `ProcessAbsent`，不能冒充 Terminate 的 `ProcessTerminated`；Close 恢复需要 exact `mechanics-closed`，Fake mechanics 却返回了通用测试 reasonCode。当前修复测试夹具并在 wire Close 处直接断言该原因，未放宽生产接纳。恢复错误增加无敏感值的固定阶段标签，便于一次定位身份/held journal/checkpoint/absence 失败。必须由新 head CI 确认，不能借用同次 Ubuntu 或旧 head 绿色结果。

依 ADR 0079，当前候选让固定 Supervisor 入口只接纳完整 v2，新 child 仅使用 SETEXEC；legacy Start/Reconnect/Attach 在借用 owner、连接或写文件之前拒绝，不翻译旧 session。保留历史 decoder 与显式 legacy 组件 harness，不删除旧证据。fresh producer 已在 `02d1f95` 切换源码，但尚未安装新 fixed bytes，也未完成所有 legacy 业务写入口排查，因此不宣称 rollout 完成。效率教训：贯穿终态链的 Fake 必须遵循真实状态及 reasonCode；编译和结构检查不能证明这些动态语义。保持同一 S3 分支闭环，不为两处夹具修正拆新 PR。

2026-09-05 新任务 producer 接线：删除该调用链的 v1 `Start` 与 v1 bootstrap/command evidence 构造，改为同一 fixed executable 的 `StartV2`、完整 handshake/anchor、v2 intent/outcome。source gate 与业务 `process-started → exact resume` 顺序保持；新启动前从当前 RB1 拒绝任何尚未 cleanup release 或仍 pending 的 legacy Supervisor。测试不再只手写 Spawn/Resume facts，而是借助显式假 Client 调用实际 producer，验证 intent-before-transport 并冷重放同一事实。新候选尚未部署；此检查不等于所有 v1 mutation API 已退役，也不替代零 active/pending v1 的实机 rollout 检查。取消/超时尚缺完整 production 调度链，必须继续实现，不能以 mechanics 支持 Terminate 代替业务可用性。

2026-09-05 v2 终态 producer 接线：Inspect 沿既有 exact intent/Attach/receipt 进入 RB1；Close 的 receipt 恢复还要求独立观察精确 Supervisor 已缺席，绑定固定 Core、held control files 与完整 v2 final journal checkpoint。进程仍活跃、前后 absence 不一致、破损尾部、错误 receipt/代际均拒绝；已提交 Close 不重新执行，不以 EOF 代替退出证明。业务 `SupervisorClosed` 接纳器同步检查 started 的实际代际和进程身份，以及 outcome 与 absence 的同一最终 checkpoint；历史 v1 不迁移、不重写。该变化实现 ADR 0079 已有终态合同，不新增生命周期步骤或旁路发布权限。

连续耐久测试覆盖 Inspect/Close 丢回复、缺席等待零重放、伪造 absence 写前拒绝、完整 cleanup release 与冷重放；processsupervisor 测试仍真实读取 held journal，只有 peer/内核观察采用显式替身。当前本地仅编译/静态证据，动态 CI 待验证。祖先 Collect `40bea7e` 的 CI 33950856231 五项全绿，不能替代当前候选；真实 Pi、生产 selector、取消/Terminate 与 fixed server 完整验收继续开放。

2026-09-05 v2 Collect producer 接线：既有生产 Collect 入口仍保留物理 owner/current ledger 检查，v2 不再误读 v1 handshake。exact prepared request 在任何执行前耐久；丢响应的 receipt 只作为待认证观察，Attach 必须绑定 post-checkpoint 和新 child observation 才接纳；未执行时只发送原请求，intent-only 仍 intervention。成功 outcome 已入账但输出读取失败时保留事实并只重读固定对象。新增 reader 读取 held v2 journal，不转换代际；前后检查完整 checkpoint、nonce、目录/文件/socket，并核对 exact Collect receipt、manifest、输出长度和摘要。原始 transcript bytes 不进入 RB1，后续 ResultIngress 继续引用 outcome fact。

效率复盘：上一候选 `71d53c2` 的 CI 33950429378 两个平台都在 `Makefile:27 format-check` 失败；手列 gofmt 文件时漏掉 `client_other.go`。这是本地可发现的流程失误，不是架构或业务失败，也不应原样重跑。已修正格式，后续采用仓库现有 `make format-check` 作为提交前统一检查，不为此引入新协议/工具或另拆 PR。quality 未跑到测试，不能把该次 conformance/secret scan 成功外推为恢复测试成功。本轮 Collect 与此前 pending-bind 必须在新 exact head 验证。

2026-09-05 pending-bind 丢响应恢复：实现 ADR 0067/0079 保留的同 owner 恢复规则，不新增持久化字段或 generic reconnect 权限。先读取精确 held v2 journal 分类，保留原 prepared command 的 deadline、request digest 和 A0；intent-only 不发送命令，未写 intent 仅在认证的原 checkpoint 上发送原命令，receipt 已提交则只读 Attach 认证 post-checkpoint 后追加 exact outcome。只读磁盘分类不是授权：伪造 peer 或不匹配的当前 owner 不能接纳 receipt。跨 owner pending 保持 intervention，Close 仍需独立进程缺席证明，未因 bind 恢复而开放。

新增 portable 三态/伪造 checkpoint/重复分类零写入测试、Darwin held-directory/取消/截断尾部不修复测试，以及原 RB1 业务链上的 intent-only、未执行丢回复、磁盘 receipt 加坏 peer 拒绝、认证 receipt 不重做命令和冷重放检查。动态行为由 CI 验证，本地不执行临时 Mach-O。`408f02f` 的 CI 33949630841 已五项全绿；本候选不可沿用其动态通过声明。生产 selector 未切换，未发生真实 Pi 业务验收或发布。

2026-09-05 ResultIngress v2 rebind 接线：主生产恢复入口保留原物理 owner 锁与 RB1 transaction，按 SupervisorStarted 的完整代际调用 v2 Attach；不转换为 v1 handshake，也不写 generic reconnect fact。先提交 creation-once owner-bound successor，再验证完整 Attach observation，随后只提交精确 v2 intent 并发送其 prepared command，最后接纳已验证 outcome。通用 rebind validator 与 journal fact producer 按原代际验证/写入；held session-directory lookup 从已验证的 v2 started 读取 session ID。既有 v1 历史与权限不变。

验证沿用同一个 durable bootstrap→initial bind→spawn→ProcessStarted→resume 测试账本，再续 owner acquisition/rebind：伪造观察不得发送命令或追加 intent；执行前读取最后一条账本确认 exact intent/recovery revision 已存在；成功后幂等调用不再次进入 transport，冷重放保持相同状态；模拟丢回复后 pending intent 保留，重试不得改写账本或重复执行。此为 Core 集成候选的测试证据，不是真实 fixed server/Pi/独立 Decision。当前 v2 pending 仍明确返回 intervention，避免误入 legacy replay；后继需要 descriptor-bound v2 receipt 分类与恢复，而不是把“拒绝重试”当作恢复完成。`e51dccf` 的 CI 33948930989 五项全绿；客户端 `408f02f` 的 CI 33949630841 单独运行，不混为本候选动态证明。

2026-09-05 v2 borrowed client 接线：`WithAttachedV2` 只在完整 generation/owner authority 的同步 verifier 内检查 held directory、nonce/journal/socket 与 fixed peer；借用对象仅公开一次 observation 和四个既有 continuation，不返回通用 Client/connection。prepared bind 的目标在发送前等于 owner-bound successor。回调错误不能被 verifier 吞掉，异步/重复/回调外调用失败；命令 context 同时受 Attach scope 取消约束。已尝试但未取得验证 outcome 的命令固定 intervention，不能用“journal 未保持只读”把已发生的效果误报为 no-effect。

本轮实现检查还发现 decoder 切换会丢失已预读的异常帧：v2 client 保留 Attach 的原 codec，最终 EOF 也通过同一 codec 检查；服务端的第二帧检查同样读取原 buffered reader，而非绕过其缓冲直接读连接。验证包含真实 Unix socket 的只读/同连接 prepared bind、错误 successor/method、跨 goroutine、取消的零重复 child effect，以及 portable owner 回调次数、逃逸、异常和闭集接口检查。这里的 verifier 为测试替身，不是 current-ledger 集成或独立业务 Decision 证据；ResultIngress producer 仍是下一关键接线，真实 Pi/stable gate 不变。

2026-09-05 Attach continuation 候选：复用 v2 的 intent→mechanics→receipt 提交流程，新增窄命令入口，不把 v1 Request 转换为 v2。只读观察不写状态；后续命令必须在锁内重新匹配完整 session/journal/owner/child-observation checkpoint。bind 的目标额外精确等于已认证 owner-bound fact 的 Attempt head，保留旧 mechanics owner epoch。已消费 checkpoint 不允许重放；丢响应只能走现有 exact receipt recovery。终态 Close 在已提交但回复失败时也终止监听，不留下已关闭 session 的无效常驻循环。

实现覆盖：错误 successor、owner、started fact、head、deadline、sequence、混代、Spawn replay、Resume 的零写入拒绝；普通命令入口拒绝 rebind；单次 rebind 不重复启动 child；Inspect→Collect→Close 按既有生命周期准入；真实 Unix socket 同一连接的 Attach→bind→EOF。客户端 borrowed capability 和 Core RB1 producer 尚未连接，故这些仍是候选代码，不是独立业务验收、生产恢复或 release 证据。后续必须完成客户端及 Core，再跑固定 bytes 的真实 Pi 完整闭环，不把当前服务端单测当作终点。下段保留前一只读 checkpoint 的历史状态。

2026-09-05 Attach 接线审计：不能把已有 generic `ReconnectV2` 接到生产 owner recovery。前者推进 session 内存 owner/head，并可处理其通用 pending recovery；ADR 0067 被 ADR 0079 保留的生产顺序要求只读 Attach、RB1 bind intent、相同 prepared bind、authenticated outcome，且跨 owner pending 固定 intervention。当前候选补足 ADR 0079 的只读 v2 编码与响应观察（完整 generation/anchor、current acquisition/owner-bound、peer/child、nonce challenge），服务端在 reconnect admission **之前**分流，拒绝新 owner/head 的握手伪装成 unchanged Attach。没有新增 reconnect authority fact、fallback 或生产 selector。

验证方式：portable 自洽篡改/全部 generation 字段缺失/unknown reconnect field/nonce/owner/head/child/peer 测试，以及真实 Unix socket 的无效请求后有效 Attach、EOF 收口、journal bytes/owner/head/last observation/mechanics calls 不变检查。本轮仅接通只读交换，任何 continuation 仍关闭；后继必须实现 borrowed callback 和 exact prepared-command gate，再开放 bind/collect/terminal。`a5a261e` 的 CI 33947799422 已最终五项全绿，确认上一轮业务启动链和 F_GETPATH 回归；本轮增量须单独 CI，不复用旧 head 作为动态证明。

2026-09-05 启动业务接线与动态失败复盘：ProcessStarted 原实现读取 v1 handshake 时间，v2 会读到空值；现按已验证的原代际读取时间，并绑定同一 session 的 spawn CommandID/ObservedAt/ObserverIdentity。resume 重放必须对应当前 ProcessStarted，不把 exec-stopped 当作运行成功。增加完整耐久链与错误观察零写入测试；这不是切换生产 selector 或实机闭环证据。

`78cfa06` 的 CI 33947059050：Ubuntu quality、Linux amd64/arm64 conformance、secret scan 通过，macOS 的 `TestOpenAttachedControlDirectoryAllowsPostCollectLinkCountGrowth` 在首次 `ObserveHeldControlDirectory` 失败。代码检查发现 processsupervisor、productionruntime 两处及 provider 共四处 `F_GETPATH` 缓冲区指针经 `FcntlInt` 的普通整数参数传递，丢失 Go 指针保活/移动语义。依据 [Go unsafe.Pointer 的系统调用规则](https://pkg.go.dev/unsafe#Pointer)，统一改为 syscall 表达式内直接转换，与仓库现有 Darwin identity 读取方式一致，并增加 fresh-goroutine/GC/无效 fd 测试。该模式缺陷已修正，但它是否解释本次间歇性 CI 失败仍待新 head 动态回归确认；不将一次绿灯或重复重跑当作完整根因证明，不放宽目录模式、owner、inode 或路径检查。

2026-09-05 v2 outcome 候选接线：结果不能只携带“形状合法”的 post journal head。当前由 `PreparedCommandEvidenceV2` 在传输前冻结 redacted journal request 摘要，返回结果由 process-supervisor 自己重算完整 receipt 与 journal 链，ResultIngress 只消费 typed outcome 并核对耐久 intent。无原始 argv/environment values/stdin/nonce/transcript bytes 进入新增字段；未知字段与非 canonical 请求投影拒绝。v2 的 semantic process outcome 复用代际无关业务映射，不转换为 v1 wire；外层 RB1/Attempt 协议名不变，旧 v1 optional 新字段保持缺省。该候选尚未对真实 session 启用，因此不会迁移/重写已发布 v1 历史，也不授予生产、Attach 或正式发布完成结论。后继 Attach 必须保留旧 command A0/post receipt 与新 owner recovery anchor 的区别，不能把重连后的 owner/head 回填到原 command receipt。上一 head `0a887f2` 的 CI 33946412476 已最终五项全绿，当前 outcome 候选须单独验证。

## 2026-09-05：B1 v2 恢复候选与失败复盘

S3 在同一候选分支连续实现 v2 journal、命令执行与 live-session reconnect；尚未切换 production selector。重连只按最后认证的 A0 和 exact v2 journal 分类，不以新 owner head 伪造旧命令的 journal base。未写 intent 才可执行一次，已有 receipt 只重放原结果，pending intent 保留不确定性；进入可能执行 mechanics 的阶段后，失败必须使 transport 静默关闭，不能回报“无副作用”。这些仍为候选实现及测试，不是实机恢复证据；transport 和 Core producer chain 未接通。

`485c606` 的 [CI 33939567946](https://github.com/chiga0/marshal-harness/actions/runs/33939567946) 在 `TestJournalWriterV2ValidatesBeforeTailRepair` 发现合法截断被误拒绝。Go `Decoder.Token` 对未结束的字符串返回 `io.ErrUnexpectedEOF`；共用 prefix parser 原来仅接受 `io.EOF` 或特定 `SyntaxError`，因此两者混淆。修正接纳合法 incomplete token，并新增真实 canonical 对象逐字节截断、重复 key、非法 escape/数字/分隔符及完整非法记录不修改的测试。未降低完整 frame 的 exact schema、digest、链序与语义验证；没有改写任何历史 journal。此为现有崩溃尾部合同的实现修复，不新增持久化语义。

效率纠偏：本地 compile-only/vet/staticcheck 不能替代动态恢复测试。本次定位具体失败再提交修正，不对同一 head 盲目重跑 CI，也不另拆微型 PR；下一 exact-head CI 通过前不得称失败已关闭。后继必须把同一恢复链接入 fixed server/真实 Pi，再证明业务交付，而不是继续只积累独立组件。

后继接线检查发现不能直接复用三处 v1 默认值：控制目录的 journal 文件名、busy/rejected 握手与 collect 的 observation digest。当前候选由同一 inherited server 入口解码 v2 bootstrap，代际显式传入文件身份/entry-set 检查；v2 错误连接关闭而不制造不存在的 rejected handshake；collect 分别验证 v2-wrapped observation 与实际 manifest/transcript digest，再校验 held 输出对象。测试覆盖全 wire 生命周期、receipt 恢复、效果发生后的混代文件漂移与 collect 后内容篡改。Core producer 尚未接线，Attach/正式 rollout 与实机矩阵仍开放；不是第二个 server、不同发布身份或生产成熟度升级。

客户端侧接线沿 `PrepareCommandV2 → exact evidence → DoPrepared`，没有添加自动重试或直接 `Do` 旁路。命令结果计算 v2 intent/receipt 的精确 journal head，绑定 generation 与前后 anchor；超时/EOF/伪造 receipt 只保留 pending，不提升 authority。`StartV2` 先验证当前 fixed image、初始空目录与 peer，再核对 held nonce、v2 初始 journal 和完整 handshake；非 Darwin 固定 unavailable。新的公共类型当前只是 Core 接线所需的类型边界，尚未进入 durable ResultIngress facts，不代表新持久化协议已经启用。连接级测试使用 Fake mechanics，不能替代同一 fixed bytes 的真实 Pi/重启及 rollout admission。

客户端与 journal 对照检查发现 rejected receipt 原先会把请求携带的外部 authority head 写成当前 head，而 live Session 拒绝时保留 A0；这会让后续合法命令的 journal 校验失败。现统一 rejected 保留 A0、成功 bind 使用 next head、其他成功命令采用请求 head，增加拒绝后合法 spawn 的链式回归，未改变已接受的权限语义。`5e1519a` CI 的 macOS 失败已保留：Close 测试遗漏必需终结事实摘要导致超时；本次补齐 fixture，修复是否有效以新候选动态 CI 为准，不重跑旧失败 head、不把本地编译称为测试通过。

恢复客户端继续使用原来的 owner acquisition 顺序与 live Supervisor，不创建第二个 coordinator、不重启或 adopt 进程。`ReconnectV2` 将旧命令 A0 与恢复计划的 previous/current owner 分离：丢失第一次恢复握手后，第二次计划可以前进而原命令的 journal base 不变；已提交 bind 的 outcome authority 也不得被新 owner head 覆盖。恢复前只接受 A0、exact intent 或 exact receipt 三类 held journal，恢复后再核对握手和实际 journal，保留 buffer、不用 v1 decoder/default。新增 Fake 与 Unix wire 回归证明所需检查路径，动态执行仍须 CI；这不是实机接线已经完成。下一步直接进入 ResultIngress/ProductionRuntime 的代际 subprojection 和现有调用链，避免另起独立业务模型。

`1057418` 的 CI 33941565125 已全部通过，Close fixture 和该 head 的客户端动态/race 回归有实证；后继恢复客户端尚不借用该结果。ResultIngress 的相邻接线选择既有 bootstrap fact 的显式代际 subprojection，而非重写外层协议或另一套持久化状态机：v2 必须带完整 generation、bootstrap/child/mechanics 标识及已有 owner/authority/identity/digest；v1 的新增字段为零且不序列化，混入任一 v2 字段立即拒绝。无原始 nonce 落盘，当前 authority 仍在现有投影器与 CAS 处校验，不能用自洽摘要替代 current-ledger。新鲜 Attempt 的实际 append/cold replay 测试已编写并编译，动态 CI 未确认前不提升成熟度；production producer 不做半条链切换。

started 接线增加两层不可互替的验证：process-supervisor 重算 initial journal head 并绑定 exact peer/generation；ResultIngress 继续核对当前 owner、已耐久 bootstrap fact、角色分离与历史对象复用，禁止用构造器自洽验证代替账本。初始握手上的任意合法格式摘要不再足以成为 v2 started 事实。显式 `v2` subprojection 与旧 handshake 互斥，旧非零 handshake 的序列化不变；v2 mechanics anchor 携带完整 generation 和 control directory，旧 command/reconnect 校验先拒绝该新 anchor，防止新字段被旧 consumer 静默忽略。fresh Attempt 的 bootstrap→started→cold replay 与写前拒绝负例已编译，动态执行以候选 CI 为准；尚未接通后续 command/collect/terminal，因此未切换 producer 或声称实机闭环。

后继把 v2 command intent 接入同一 RB1 recovery projector，保留 producer `PreparedCommandEvidenceV2.EvidenceDigest`，回放完整 generation/A0/参数投影后验证摘要；该投影不是可执行请求，必须经 `RebuildPreparedCommandV2` 与重新取得的精确 payload 比较才能传输。当前账本继续决定初始 bind 是否引用本 Attempt 的 started fact，不能以自洽的伪造请求替代。recovery header 必须与子投影代际一致；未接线的 v2 outcome/Attach 仍拒绝，不以 v1 body 伪装 v2。CI 33942406526 的 macOS 失败已定位为 Fake socket 位于 cwd 外、误用生产相对路径 helper；修正只作用于测试连接地址，不删除路径门禁、不改变 cwd，也不把 Fake 回归计为真实 fixed-image/Pi 证据。重复教训：测试在调用生产 helper 前须满足其路径与对象前置条件；普通协议 harness 与固定产物安装验证应明确分开。

## 2026-09-05：三面分离与真实业务交付纠偏

基线 `origin/main@0c6d9cd`。保留确定性 Core、独立验证、Provider 分层与恢复资产；当前不能把 single-task kernel 或 T2 API 存在描述成自治 Agent Team。[ADR 0080](adr/0080-three-plane-business-delivery-roadmap.md) 接受 B1→B2→B3 的业务路线，细节见 [业务交付计划](agent-team-delivery-plan.md)。

本轮打开的架构问题：Goal records/admission 尚未形成生产 controller；`sealedRepositoryApplication.VerifyRun` 在实际验证期间持仓库级 mutex，Status 等亦竞争该锁；resident recovery 任一 Run 失败阻断整体 ready；ingress transact 在独占锁内全量重放账本。前两项为代码事实，长历史性能影响尚未压测，不写成已测事故。锁/故障隔离/持久化优化保留 current-ledger 与 lease 不变量，具体语义调整先窄 ADR。

纠正检修口径：`production-owner-not-current` 在 fsync 修改后仍复现，后续诊断提交 CI 绿色不能证明根因关闭。不同日期文档把 RC1 同时称为“未发布”和“已发布”已改为 Roadmap 当前表与历史 checkpoint 分层；文档修正不改变运行成熟度。

开始执行：订单报价独立 oracle、典型错误实现反例与 T2 `--scenario order-quote`，沿既有 Task 格式接入，不新建生命周期或放宽 gate。它只是 B1 验收基础设施；尚无本场景真实 Worker/独立 Decision 证据。后续必须记录失败分母、人工介入、等待与重做成本，并以一个集成业务候选完成判定，而非各子 Run 的通过率。

B1 相邻修复：`Status` 不再取得 mutation mutex，仅与 `Close` 共享 lifetime guard，随后仍调用 `RepositorySession.OwnerProjection` 实时复核；关闭顺序为 mutation mutex→status guard→session，避免观察已释放资源。没有缓存 ready、修改 owner/Run 合同或取消 mutation 序列化。测试使用未 claim session 证明可以越过 mutation 锁并仍拒绝错误 owner，不伪造成功生产 session；健康实机延迟、其余锁及恢复隔离仍开放。

## 2026-09-04：ADR 0079 S2-B fixed-image SETEXEC canary 候选

本轮把 S2-A 的 dormant v2 contract 接到 `runLaunchChild` 的唯一调用链，但没有启用生产 selector：`NewPlatformMechanics` 继续固定返回 v1，只有隐藏且带 `--attestation-ready` 的固定 `marshal internal process-supervisor-v2-canary` 显式构造 v2 mechanics。canary 不生成、复制或执行临时 Mach-O；它从当前安装环境解析已签名 Node，立即转成 absolute real path 并冻结 device/inode/mode/size/SHA-256，子进程仍由当前 fixed Marshal 经 inherited FD 进入 `runLaunchChild`。输出只含 protocol/mechanics/observer identity、自然退出码和四个布尔/状态结论，不含 path、PID、argv、environment、nonce 或 transcript。

真实 fixed-binary 检修关闭了四个此前静态检查无法发现的问题。第一，macOS 的 `/var` 是 `/private/var` 别名，`O_NOFOLLOW_ANY` 会正确拒绝默认 `/var/folders` 临时目录，因此 canary 数据固定放在 `/private/tmp` 的 owner-only 目录，且退出后无残留。第二，sealed system volume 的 `/usr/bin/true|false` inode 超过 JSON safe-integer 合同，不能拿它们伪装普通 runtime；canary 改用 Pi 已依赖的签名 Node，而不放宽 inode 门禁。第三，fixed Marshal 必须以 canonical absolute path 启动；相对 `./bin/...` 会在 child 的 parent-binary 双端复核中 fail closed。第四，Darwin 对 `POSIX_SPAWN_START_SUSPENDED` 返回 `WIFSTOPPED=true` 且 stop signal 为 `0`，不是 `SIGSTOP`；v2 现精确接受该状态并继续拒绝 ptrace `SIGTRAP`、signal-delivered stop、exit/signaled/unknown。自然短任务还暴露了 `Inspect` 在 process exit 与 `command.Wait` channel delivery 之间的竞态；v2 只允许对同一 command 做一次 20ms 有界 wait 收敛，拿不到 exact terminal result 仍返回原身份错误。

同一 absolute fixed candidate 已真实通过：inherited FD → v2 child closed decode → SETEXEC/START_SUSPENDED → stopped runtime identity recheck → `SIGCONT` → Node 自然 `exit=1` → inspect/collect/close；另一路在 resume 前完成 exact process-group cleanup，canceled context 在任何 child effect 前拒绝，重新 seal 后的 runtime symlink 在 nofollow source gate 拒绝。Darwin arm64/amd64 与 Linux amd64 的 processsupervisor/CLI tests 均以固定输出路径 compile-only 并删除，`go vet`、staticcheck、diff-check 与 gitleaks 通过；本机未执行 Go 临时测试 Mach-O。

该结果关闭 `ADR0079-S2B-FIXED-CANARY-CANDIDATE`，但仍不把 v2 标成 production 或 `INTEGRATED`。S3 仍须证明零 active/pending v1 session 后才允许 new-session-only producer cutover，并以同一最终 fixed bytes、真实 Pi、fixed server restart/response-loss 及完整 collect/verify/review/Decision 到 `ACCEPTED`。Issue #212 的 Developer ID 签名、公证、安装 receipt 与企业白名单仍是独立 stable gate；server/canary 减少随机 executable 面，不替代主二进制的 OS 信任。

## 2026-09-04：ADR 0079 S2-A closed protocol 与只读代际路由候选

本轮在 `S1 dormant mechanics` 之后加入独立且仍不生产的 `process-supervisor/v2` 合同实现。v2 bootstrap、reconnect、handshake、request、response、inherited child spec 与 mechanics journal 均使用 ADR 0079 冻结的 exact schema/protocol/launch-child/mechanics identity；closed decoder 拒绝 unknown、v1 字段与混代 identity。`requestDigest`、`receiptDigest`、`observationDigest`、`commandHead` 与 `recordDigest` 均显式纳入 v2 代际绑定，其中 process report 只能使用 `darwin-fixed-process-supervisor/v2`，不得把 v1 observer 的结果包装成 v2 response。

新增 journal audit surface 只能按 exact leaf 把 `process-supervisor-v1.journal` 路由到现有 v1 decoder，把 `process-supervisor-v2.journal` 路由到独立 v2 decoder；它不返回 writer、不截断 partial tail，也不执行 Attach、adopt 或 append。两代 journal 同时出现、wrong genesis、first command sequence/head 不合法、intent/receipt 不配对、完整但语义非法的 torn frame、v1 leaf 出现在 v2 control-directory phase、输出对象跳序、重复或未知 entry 均 fail closed。现有 v1 producer、journal writer、`runLaunchChild` 与 ptrace mechanics 没有改动，故该切片不产生 v2 authority fact，也不把 S2 或 T2 标记为完成。

Darwin arm64/amd64 与 Linux amd64 package tests 已采用固定输出路径完成 compile-only，随后删除产物；`go vet`、staticcheck 与 diff-check 通过。本机没有执行 Go 临时测试 Mach-O。本候选只关闭 `ADR0079-S2A-CLOSED-DECODERS`；S2 仍须把 exact projection/limit 与 fixed-binary hostile/timeout/early-exit/cleanup matrix 收口，S3 才能在证明零 active/pending v1 session 后切换 new-session producer。固定 Marshal 主二进制的 Developer ID 签名、公证、安装 receipt 与企业白名单继续由 Issue #212/stable gate 负责。

## 2026-09-04：fixed server T2 实机阻塞定位与 ADR 0079 S1 bridge 候选

`feat/fixed-server-t2-lifecycle@138a49c` 已把 fixed server 的 resident application surface 扩展到 collect、verify、review 与 Decision，并以同一 canonical `bin/marshal`、同一 repository owner 和同一 server authority 启动真实 T2 Run `run:fixed-server-final-138a49c`。`control-plane start` 在 response loss 后由重启 server 以 owner epoch `3` 重读为唯一 `RUNNING` Attempt，证明 server 本身、固定二进制定位与 restart recovery 没有被 Gatekeeper/EDR 终止；后续 collect 观察到真实 runtime child 已 terminal。Darwin crash report 进一步把失败收敛到旧 Supervisor mechanics：fixed Marshal parent 仍存活，已签名 Node `24.15.0` 在 `PT_TRACE_ME → exec → PtraceDetach` 后于 dyld 边界以 `SIGTRAP` 退出。相同 Node/Pi 在普通 `env -i` 固定路径直接探测均正常，因此该失败不是 Pi 未安装、未登录或 server 匿名二进制，而是 ADR 0059 的 ptrace exec-stop 在 macOS 26.6.2 上失效。

[ADR 0079](adr/0079-darwin-posix-spawn-setexec-barrier.md) 已冻结替代合同。候选 `f71232c` 完成其 `S1 dormant mechanics`：在固定 Marshal 内加入 Darwin arm64/amd64 的 CGO-free libSystem bridge，只动态绑定 `posix_spawnattr_init`、`posix_spawnattr_setflags`、`posix_spawn` 与 destroy，flags 固定为 `SETEXEC | START_SUSPENDED`；缺 symbol、非法 argv/environment、任一 attribute/spawn/destroy 失败或 SETEXEC 意外返回均 fail closed。bridge 不创建、下载或执行第二个 helper，也不使用 PATH、CGo、shell 或 `/tmp` Mach-O。arm64/amd64 fixed `cmd/marshal` 与 package test compile-only、Linux amd64 package compile-only、`go vet`、staticcheck 和 diff-check 已通过；`nm` 证明四个 libSystem symbol 已进入 fixed image，`otool` 只增加既有系统 libSystem 依赖。

该 checkpoint 只关闭 `ADR0079-S1-DORMANT-BRIDGE-CANDIDATE`，不宣称 T2、Supervisor v2、Issue #212 或 stable gate 完成。producer 仍明确保持 disabled，现有 v1 journal 不写入新 mechanics。下一步必须先完成 `process-supervisor/v2` 的 closed wire/schema、`process-supervisor-v2.journal`、新 genesis、v1 read-only routing 与混用拒绝，再在零 active/pending v1 session 时把 `runLaunchChild` 唯一生产调用点切到 SETEXEC/START_SUSPENDED，并以同一 fixed bytes 重跑 signed Node/Pi、server restart/response-loss、collect/verify/review/Decision 到 `ACCEPTED`。即使该链完成，固定 Marshal 主二进制仍须经过 Issue #212 的 managed signing/notarization、安装 receipt 与受保护发布门禁；server 模式减少的是匿名/随机执行身份和重复启动面，不替代操作系统对主二进制的信任管理。

## 2026-09-04：fixed server T1 exact-head 集成终验

PR [#252](https://github.com/chiga0/marshal-harness/pull/252) 的 merge commit `b39c346ae2fdb856c8442bdd6b56eec361ab1f84` 修复了 S4 真实 canary 暴露的五个组合缺口：runtime-v1 rename-swap 后的 root observation、current RB1 与 Run projection 的 exact join、client 对 exact StartRun response 的采用、InspectRun 只读路径误触 attach recovery，以及 resident recovery 仍按 legacy READY `state.json` 枚举而遗漏 sealed RUNNING authority。修复没有增加 executable、authority root、持久化协议或 CLI fallback；Darwin 私有 adoption 被隔离到 Darwin build graph，Linux amd64/arm64 compile/vet 与 candidate conformance 继续 fail closed。

[main required CI 33850189142](https://github.com/chiga0/marshal-harness/actions/runs/33850189142) 在 Ubuntu、macOS、Linux amd64/arm64 与 secret scan 全绿后，[T1 run 33851302323](https://github.com/chiga0/marshal-harness/actions/runs/33851302323) 从该 exact head build-once 固定 `bin/marshal`，使用真实 Pi `0.84.4` 执行 response-loss/restart canary。第一个 server 的 `kill -9` 是脚本受控故障注入，不是 Gatekeeper/EDR 误杀；第二个 server 在 owner epoch `2` 恢复 epoch `1` 的 sealed RUNNING Attempt，并在 ready 前完成 resume/rebind。闭合 artifact `9928466621` 证明：两个 server observation 的 canonical path、device/inode、SHA-256 `sha256:180364721455b23f18aa4e72a4e2059683fb772eb39bb95b0e00f107cb35c4c5` 与 CDHash `f940ce13b175951b27105356958bf31c64bd62d7` 完全一致；Supervisor intent/outcome 成对闭合且只有 `spawn=1`、`resume=1`、`bind-authority=2`；pending/receipt、Run、Attempt、worker start、recovery bind 均唯一，`cliFallback=false`，没有第二 mutation writer。

审计结论为 `FIXED-TRANSPORT-T1-INTEGRATED / FULL-LIFECYCLE-T2-OPEN`。ADR 0076 对 fixed transport/T1 的 `INTEGRATED` 门禁已经满足，但 ADR 0062 full fixed-server lifecycle 尚未满足：当前 public Port/HTTP closed operation 只覆盖 status/start/inspect，T2 所需 collect/verify/review/Decision 仍由旧 CLI path 直接打开 state/root；在 resident server 已持有唯一 repository owner 时复用旧 CLI mutation 会形成第二 writer 或权威冲突。因此下一纵切必须把这些操作收敛到同一个 resident application authority，而不是增加另一个 executable、server 或 CLI fallback。T2 到 `ACCEPTED`、完整 recovery/fault matrix、Developer ID signing/notarization 与 managed launch、Linux stable 和 protected stable candidate 继续开放；CI ad-hoc identity 不等于企业安全软件信任或 stable production。

## 2026-09-04：main exact-head CLI 回归关闭与剩余 stable 边界审计

本轮以 `main@c2198e3628f38b126402cf7ab153120ea25e3d77` 的 build-from-head 固定 candidate 执行真实 Pi `0.84.4`，而不是复用旧 RC1 二进制或测试 seam。[run 33788766642](https://github.com/chiga0/marshal-harness/actions/runs/33788766642) 在一个 Attempt 内完成 existing-worktree path-B、sealed `PrepareRunStart → StartPreparedRun`、真实 worker terminal collect、Verification 与 `REVIEW_PENDING`。独立 reviewer 对真实 ReviewPacket/evidence 确认 `P0=0`、`P1=0`；[finalize 33790168049](https://github.com/chiga0/marshal-harness/actions/runs/33790168049) 导入精确 Decision 后到 `ACCEPTED`，`rc1-carrier-check` 通过，artifact `9907034593` 的 zip digest 为 `sha256:e212f4e817fdc774828a7eaffa6b584eeeb5b243af98e486289e2c94362143b7`。该证据满足 PR #228 对 #226 的关闭合同，因此 #226 可以关闭。

该证据不满足 ADR 0075 对 #224/#225 的完整关闭合同：`rc1-canary.yml` 使用 marker 任务与 `umask 077`，终态是裸 WorkerResult JSON；它不是 `m13-e2e-dogfood` 在相同 published-asset pin 下的真实复杂中文任务，也未覆盖 default umask、“散文 + 恰好一个完整 JSON”或提取时间/token 指标。因此 #224/#225 继续作为实现已合入、外部验证仍开放的 stable blocker；不得用 marker success 替代这些路径。

该时点的审计同时拒绝两种过度结论。第一，CLI success 当时不等于 fixed server success：S4 尚缺同一 fixed bytes 的跨进程 `StartRun → RUNNING → InspectRun`、response-loss/same-key replay、server restart strict-successor T1，以及 T2 经 server collect/verify/review/Decision 到 `ACCEPTED`；其中 T1 已由上方后继终验关闭，T2 仍开放。第二，GitHub macOS runner success 不等于当前开发机已具备 managed production launch；macOS 26.6.2 上 ADR 0059 的 `PT_TRACE_ME → exec → SIGTRAP → PtraceDetach` 已对 signed Node 在 dyld 边界复现 `SIGTRAP`。后继修复应按 ADR 0079 把固定 `marshal internal process-supervisor` inherited launch child 的最终 barrier 换为 Darwin `posix_spawn(POSIX_SPAWN_SETEXEC | POSIX_SPAWN_START_SUSPENDED)`，在 live stopped PID 上完成路径/CDHash/held-object 双端重验后 `SIGCONT`；不得新增匿名 helper、放宽身份校验或声称该机制替代 Issue #212 的 Developer ID/notarization。

结论：当前实施没有偏离 v1 主线，#226 已从阻塞清单删除；#224/#225 只差 ADR 0075 指定的复杂任务外部验证，不再扩展实现范围。该时点记录的最短发布关键路径为并行补齐该验证，同时继续 fixed server T1/T2 → recovery/fault matrix → managed signing/notarization → Linux stable → protected stable candidate；T1 现已由上方后继终验关闭。I186-R2–R6 继续保持 `COMPONENT/IN_PROGRESS` 的诚实口径。

## 2026-09-03：fixed server S4 resident integration 候选审计

S1–S3 已合入 `main@0761ad3`。S4 候选把生产入口收敛为同一个固定 `marshal` 的 `control-plane serve|status|inspect|start`，没有新增 `marshal-server`、临时 Go helper、`/tmp` executable、第二状态根或 CLI fallback。server 在 endpoint ready 前以 resident `RepositorySession` 完成全仓恢复；独立 client 只以 `O_RDONLY` held descriptor 和共享锁重放 current owner，不获取 owner 写锁、不创建缺失文件，也不借用 server 进程内 FD。AF_UNIX pathname 仍只负责定位，授权继续绑定 current owner、完整 acquisition、held root、fixed binary、peer、nonce/token proof 与 application intent。

实现自审发现并在进入独立 reviewer 前关闭两项主链缺口。第一，原 `sealedRepositoryApplication.StartRun` 依赖 CLI 外层执行 frozen local-dogfood binding 与 plan approval；直接接入 server 会绕过批准。候选已把二者下沉到共享 Application Adapter，并保留 expected sequence/head CAS，transport 不复制或降低业务门禁。第二，shutdown 最初会在 request drain 前调用 endpoint `Close`，使在途请求的收尾 authority recheck 被本进程提前删除的 socket/token 破坏；现改为 accept-stop 仅设置 listener deadline，取消并有界 drain 后才关闭 listener、精确 unlink 当前对象并最后释放 application/session。client 还在响应后重新验证 server/owner，并对 status line、closed header、canonical body 与 operation 做完整校验。

该时点的证据只到候选级：Darwin/Linux 定向 compile-only、`go vet`、staticcheck、architecture check 与 `git diff --check` 已通过；本机没有执行随机临时测试 Mach-O。唯一独立 reviewer 已在一次 aggregate rework 后确认 `P0=0`、`P1=0`；rework 关闭了显式冻结 deadline、Accept/StopAccept lost-wakeup、三阶段 shutdown 与对应确定性测试。当时 S4 仍须通过 exact-head GitHub macOS 动态门禁，以及同一固定 candidate bytes + 真实 Pi `0.84.4` 的跨进程 `StartRun→RUNNING→InspectRun`、server restart、response-loss/same-key replay canary，故只能标记 `COMPONENT`。该 T1 门禁现已由上方后继终验关闭；T2 到独立 Decision/`ACCEPTED` 仍是后继。

## 2026-09-03：fixed server S3 bounded delivery 候选审计

S2 已以 PR [#230](https://github.com/chiga0/marshal-harness/pull/230) 合入 `main@0a0c73e`。S3 候选没有新增 executable 或 authority root，而是在既有 authenticated connection 上提供 closed、单 request 的 HTTP/1.1 adapter。请求只允许 `Status`、`StartRun`、`InspectRun`，并在 Port 之前完成 canonical JSON、closed header、request/intention/deadline binding、current endpoint/owner/fixed-binary recheck、repository inflight/queue admission；header、body、application、write 使用分阶段 deadline与固定内存上限。unsupported、overload、非 canonical/未知字段、过期或未授权 deadline 均保持零 application/delivery 副作用。

`StartRun` 的业务成功只来自 S1 delivery store 对 current RB1 的 exact reconcile：pending 必须先 durable，Port handoff 前后均重读 current ledger，receipt-ref 与 current projection 再次吻合后才返回 success。Port error、disconnect 或 response loss 不能被解释为 not-applied；RB1 未命中时保留 pending，已有 exact receipt 的 replay 不调用第二次 StartRun。该语义避免了随机 helper Mach-O、CLI fallback、HTTP status 冒充 authority和重复 Attempt/Supervisor command。

独立首审聚合发现两项 P1：application 前后 recheck 未重新观察握手冻结的 peer process/binary，以及 HTTP success 未完整验证 sealed receipt 并绑定本次 pending。一次 aggregate rework 已同时关闭：server/client 每次 recheck 都重新取得并 exact compare `CoreIdentity`；delivery 导出唯一 sealed pending/receipt 校验，router 还逐项核对 authenticated request key/request/intent/deadline。same reviewer 复审结果为 `P0/P1=0`，没有进入第二轮滚动返工。

本审计不关闭 integration：当前 router 仍只由测试注入 fake `PublicApplicationPort`，没有 resident `marshal control-plane serve`、startup recovery ordering 或真实 Pi canary。S3 通过 exact-head required CI 后只可标记 `COMPONENT`；S4 必须用同一固定 candidate bytes 证明 recovery-before-ready、真实 AF_UNIX 到 `RUNNING`、restart/response-loss strict-successor exact replay，T2 再以独立 Decision 到 `ACCEPTED`，否则 `INTEGRATION-OPEN` 保持不变。

## 2026-09-03：fixed server S2 endpoint authentication 审计

对 PR [#230](https://github.com/chiga0/marshal-harness/pull/230) 的 `7ee24cc` 候选复核确认，ADR 0076 的 S2 已落在固定 `marshal` 进程内，而不是另建匿名 helper executable。endpoint 只能由 canonical repository 的 held `.marshal/runtime-v1/control` authority graph 和 current owner epoch 派生；socket/token均以owner-only、nofollow、creation-once语义建立，运行期持续重验name/object、owner acquisition/fact、peer process与fixed binary。握手采用server nonce与HMAC-SHA-256，并把proof绑定request key、request/intent digest和冻结deadline；nonce首次尝试即消费。oversize、伪造proof、replay、token ABA、short write与root/owner漂移均在application dispatch前fail closed。client不再接收裸control FD和可自洽snapshot，而是从current `FixedEndpointAuthority`原子取得close-on-exec duplicate descriptor，并在握手前后重验owner。

实现过程中两项首轮CI问题已作为跨平台门禁修正，而未降低合同：一是 `productionruntime` 错误反向依赖 `processsupervisor`，已把fixed-binary observation下沉到transport拥有者并恢复层次边界；二是Darwin私有protocol helper留在common build graph导致Linux staticcheck unused，已拆为`protocol_darwin.go`。同时修正共享token descriptor offset竞态为`Pread`、frame short write为完整循环、authority mutation/close锁递归风险为descriptor显式传递。上述问题说明S3开始前必须继续做Darwin/Linux compile+staticcheck，但不需要回退为临时二进制或扩大架构。

本切片关闭的是`S2-ENDPOINT-AUTH-COMPONENT`，不是fixed server integration。S3必须把有界parse/queue/application deadline与S1 immutable delivery接到同一认证连接；S4必须由fixed candidate bytes运行resident `marshal control-plane serve`，完成recovery-before-ready与真实Pi restart/response-loss strict-successor replay。T2独立Verification/Decision到`ACCEPTED`前，ADR 0062 full fixed-server lifecycle仍不可标记`INTEGRATED`。#226 在该时点只影响CLI-only adapter、保留为并行债务；它现已由上方 2026-09-04 checkpoint 关闭。任何server→CLI fallback仍属P0。

## 2026-09-03：fixed server S1.3 strict successor 审计

对代码候选 `62c1aed` 的审计确认，跨 owner 恢复没有把旧 token、pending digest 或路径当成 authority。ResultIngress 从同一 held RB1 ledger 重建每个 owner epoch 的历史投影；调用方必须先持有 current physical owner lock，随后同时精确命中 old owner fact digest 与完整 `ControlOwnerAcquisition` digest，并逐 epoch 验证 `PreviousFactDigest` 连续到 current owner。ledger 截断、重复 epoch、scope 漂移、fact/acquisition 伪造或 future epoch 均在 delivery 回调前 fail closed。

原 S1.1 的 session-local mutation observation 已替换为可由 cold successor 重算的 stable authority-root digest。该 digest仍绑定 canonical repository path、5 个 held directory object 的 device/inode/type/uid/gid/mode 与 4 个 closed name；每次使用前后仍执行包含 current-name、nofollow、owner-only 与 mutation/ABA 检查的完整 root validation。只从持久 identity 中排除合法 immutable append 会改变的 ctime/birthtime，未降低运行时 name/object 门禁。delivery schema/protocol 升至 v2，使旧 v1 记录不能被静默采用。

该候选关闭的是 ADR 0076 的 S1 durable delivery，不是 server integration。AF_UNIX locator、peer/fixed-binary/token/nonce/HMAC、bounded HTTP、resident `control-plane serve`、真实 Pi restart replay 和 T2 `ACCEPTED` 均仍开放；在 S4 前 fixed transport 继续标记 `COMPONENT`。本地只运行 compile-only、vet、staticcheck 与 diff-check，动态 hostile/restart 测试和 secret scan 必须由最终 exact-head required CI 通过后才允许合入。

## 2026-09-03：fixed server S1.2 receipt 对账审计

对 `a9ef62b` 的实现审计确认，S1.2 没有把 transport response、HTTP status 或当前 Run 快照冒充 application authority。新增的 `ReconcileStartRun` 是 current-owner 下的只读 RB1 查询：它重建 exact `PreparedRunStart`，并只接受同一 preparation 所产生、位于 journal head 的 sealed `run.start-outcome` successor。delivery store 随后重验同一 owner/session、held root、immutable pending、request 与 intent digest，才以 digest-derived leaf 追加或 exact-adopt `receipt-ref`。测试覆盖成功闭合、exact replay、结果漂移拒绝、RB1 未命中保持 pending，以及 Run 已从 READY 前进到 RUNNING 后仍可重放原 pending；另有 controller 测试证明 reconcile 不调用 Prepare/bridge 且只进入一次 owner authority section。

本切片没有完成 ADR 0076 S1 的跨重启部分。S1.1 的 `AuthorityRootDigest` 仍绑定一次 session 打开时观察到的 mutation/root state；strict successor 即使持有同一 physical root，也不能据此证明 old pending 到 current owner 的完整 lineage。因而 S1.2 只能判定为 `SAME-OWNER-RECEIPT-CLOSED / SUCCESSOR-OPEN`，不得开始对外宣称 server 可用，也不得以本 checkpoint 替代 S2 endpoint auth、S3 bounded HTTP、S4 exact-head real-Pi restart canary 或 T2 `ACCEPTED`。下一切片必须先让稳定 authority identity 与 owner successor lineage 可重放，并补 old-owner pending 的正负恢复矩阵；不能通过放宽 digest、adopt 未知记录或重新生成 deadline 解决。

## 2026-09-03：ADR 0078 取代 ADR 0077 的 TaskSpec 验证决定

M13 walking-skeleton dogfood Run `33709488741` 已由真实 Worker 完成交付并通过 Drive，Verification 中 8 项通过、2 项跳过，唯一 required command failure 为 `taskspec-validate`：同一 fixed candidate 执行 `contract validate` 时被 local-profile self gate 以 `self-local-command-denied` 拒绝。审计确认这不是 TaskSpec、Agent 或网络失败，而是[开发文档](development.md)已经承诺 `contract validate` 为只读命令，local dogfood closed command classes却没有对应入口。

删除 gate、手写 Python schema 子集、清空 activation 或构建临时 checker都会分别造成验证降级、语义漂移、无效绕过或匿名 Mach-O。ADR 0077 曾据此把解法定义为 public `contract validate --schema task-spec <repository-relative-path>` 与 local-profile self-admission 权限；后续设计复审确认该方案把独立 Verification 已拥有的 candidate-isolate authority 错误绕回 public CLI，使用户控制路径进入 argv、`CommandRecord` 与日志，并产生第二条 executable admission 路径。

维护者因此接受[ADR 0078](adr/0078-verifier-builtin-task-spec-contract-gate.md)并完整取代[ADR 0077](adr/0077-local-dogfood-read-only-task-spec-validation.md)。现行唯一设计是 pathless exact argv `marshal-builtin:contract-task-spec:v1 + deliverable:<id>`：它只在 current Candidate 的既有 command-isolation 闭包内解析 exact required artifact，以 held nofollow、有界读取取得 bytes，调用 Core 唯一 Draft 2020-12+semantic validator，并生成 closed、pathless evidence。reserved namespace永不回退到 PATH；失败不得泄露path、argv原文、底层错误或输入值。

ADR 0077 现仅保留为问题与被拒方案的历史记录；其 `contract.validate-task-spec-file-readonly`、public CLI 与 self-admission 决定**不再授权任何实现或激活**。ADR 0078 不新增 public contract、自身份权限、生命周期、publication或child-process effect，也不表示M13完成或任何I186成熟度升级；状态只记录替代后的现行设计与实现边界。

## 2026-09-03：ADR 0076 fixed server 合同接受与大爆炸实施纠偏

独立复审确认，先前候选`45e54b1`把ADR接受、durable delivery、AF_UNIX认证、HTTP路由和production canary压在同一分支，留下三项缺陷：两项P1分别是immutable `pending`只保存owner epoch/fact而没有exact durable `OwnerAcquisitionDigest`，无法在restart后证明完整process/binary/observer acquisition，以及所谓集成证据仍是fake `PublicApplicationPort`、synthetic owner与source string count，没有经过`control-plane serve → resident recovery → 真实Pi RUNNING → restart exact replay`；一项P2是`ReadHeaderTimeout=5s`与总`ReadTimeout=15s`共享时间窗，不满足独立15秒read-body phase。该候选保留为失败证据，不推送、不合并，也不继续滚动rework。

[ADR 0076](adr/0076-darwin-fixed-server-pathname-locator.md)现于`main@d9cd001`接受并精确冻结修正后的合同：durable owner authority对完整`ControlOwnerAcquisition`产生唯一canonical digest，delivery `pending`必须同时持久化该digest与owner fact；socket object digest仅为host-local endpoint证据，不能替代durable acquisition；read-header、read-body、application与write deadline必须分别在各自phase启动。实施固定拆为`S1 durable delivery → S2 endpoint auth → S3 bounded HTTP delivery → S4 resident production integration`，禁止整体cherry-pick旧候选或再次合成大分支。

成熟度主体保持诚实：S1–S3期间`fixed transport/T1 capability`最多为`COMPONENT`；只有exact-head macOS使用固定candidate bytes、resident recovery和真实配置Pi，证明recovery-before-ready、RUNNING、response-loss/restart strict successor exact receipt replay且零CLI fallback/重复Run/Attempt/Supervisor command，`fixed transport/T1 capability`才能标记`INTEGRATED`。`ADR 0062 full fixed-server lifecycle capability`仍必须等待T2真实Pi与外部独立Decision到`ACCEPTED`的canary后才能标记`INTEGRATED`。本次只关闭治理合同，不实现fixed server、不升级I186成熟度，也不改变managed signing/notarization、Linux stable或受保护stable candidate门禁。

## 2026-09-01：`v1.0.0-rc1` same-bytes 发布终验与失败复盘

[`v1.0.0-rc1`](https://github.com/chiga0/marshal-harness/releases/tag/v1.0.0-rc1) 已完成 ADR 0068 的 local-dogfood prerelease distribution exit。annotated tag object `e99326fa6b6e57a19db8d6404c56b3dcf396fdc7` 精确指向 sourceHead `c1407bd77924c97dc6784f4d81938a3f0bfa75f6`；candidate SHA-256 为 `f9ed7fa59d05f5e71fef7164b8015240497e1d18e25ef1d3f8e199c1378a3774`。真实 Pi `0.84.4` canary run `33504020360` 与 finalize run `33504247271` 形成单 Run/单 Attempt、9 项 Gate、独立 Verification/ReviewDecision、`ACCEPTED`、current receipt/carrier；receipt digest 为 `sha256:7bd5b500bbaff5c5b008922b713d9844b792a3e82ece4e4a46ccd837496b4525`。candidate exact-head CI run `33502847249` 的 Ubuntu、macOS 与 secret scan 全绿。

release workflow run `33506656403` 的 Admit 和 Publish 全绿，只消费 finalize carrier，不重建、重签、strip 或改写 candidate。GitHub release 外部下载的 candidate SHA-256 仍为 `f9ed7fa5…a3774`；在独立临时目录通过 exact tag + preview opt-in 安装后，`marshal version --json` 精确返回 version `1.0.0-rc1`、commit `c1407bd`、build date `2026-09-01T11:30:25Z`、Go `1.26.6`、`darwin/arm64` 与 `darwin-local-dogfood`。tag ruleset 只在创建该 exact tag 时加入临时精确 exclusion，推送后立即恢复为 active、零 exclusion；未覆盖或改写远端历史。

发布前发生三次确定性失败，candidate/tag/carrier bytes 始终未变：

1. run `33504766816`：Admit 在 carrier checker 通过后，又把四成员 carrier 交给只接受三成员 dist 的 `verify-rc1-dist`。修复为单一 `verify-candidate-tag` RC1 路由，删除重复且语义冲突的门禁。
2. run `33505577882`：Publish 把 candidate sourceHead 与 GitHub artifact workflow revision 错误地当成同一 SHA。修复为分别绑定 artifact name/candidate SHA 和 `workflow_run.head_sha`/workflow SHA，并补 cross-workflow replay 负测。
3. run `33506131715`：workflow 来自新 main，但 Publish checkout 到 RC1 tag 后实际执行了 tag 内旧 validator。修复为候选源码与 validator 双 checkout；validator 精确绑定 `${{ github.sha }}`，candidate contract 仍从 exact tag 执行。随后使用该失败 run 的真实 artifact 在本地完整预演 metadata→archive→payload→receipt→carrier→tag/binary 全链，才进行最终发布。

复盘结论：这三次往返不是 Agent 或 candidate 质量问题，而是发布测试只覆盖 helper 单体，没有在“workflow source revision 与 checked-out candidate revision 不同”的真实 GitHub workspace 语义下执行全链。后续 release 变更必须在 dispatch 前使用一个真实历史 artifact 或等价 closed fixture 完成端到端 Publish preflight，并显式列出 candidate identity、transport workflow identity、validator identity 三个不同主体；禁止重复校验器、隐式 checkout 版本和同名 `sourceHead` 混用。该学习已固化为 fixed workflow digest、双身份 hostile/replay 测试和 exact validator checkout。

本终验不改变 ADR 0068 的负向声明：RC1 不是 ADR 0052 的 `RELEASED`，也不是 production、managed、notarized、hardened、server、Linux 或 stable release。`I186-R2–R5` 保持 `IN_PROGRESS/COMPONENT`；`I186-R6` 更新为 `IN_PROGRESS/COMPONENT`，下一主线是 fixed server/recovery fault matrix、Issue #212 signing/notarization、Linux stable 与受保护 stable candidate。

## 2026-09-01：fixed server application assembly 审计

对 owner-scoped `RepositorySession` 的后继调用链复核发现，旧 `task run` 仍内联打开 owner/ingress/provider/dispatch/Run/worktree/Pi closure 并直接组合 Runtime；若直接实现 server 命令，势必复制第二套生产装配。同时 `internal/server` 的默认 authority scope 为 `repo:<root>`，真实 fixed CLI owner scope 为 canonical `<root>`，二者即使共用同一进程也无法共享 current owner/receipt。

本切片把 Run-specific producer chain提取为唯一 `sealedRepositoryApplication`，以 held StateRoot 的 descriptor-bound Run store枚举和解析 Run；repository session只打开一次，每个 application operation组合短寿命 Run runtime。启动 readiness 前先扫描全部 Run并对`RUNNING`项调用既有 `NewCompositionLedger` attach/rebind recovery，避免 resident process在未恢复旧 Attempt时对外宣称ready。worktree descriptor graph仍在pre-read Run lease持有期间冻结，防止释放lease后按字符串重开形成TOCTOU。server新增显式 namespace注入，仅接受local/default/exact repository root，compatibility默认保持不变。

该实现没有改变authority fact、持久化Schema、生命周期转换或发布权限，沿用ADR 0062/0066/0067/0069；审计结论为`APPLICATION-ASSEMBLY-CLOSED / TRANSPORT-OPEN`。剩余最短路径是fixed `marshal control-plane serve`命令、descriptor-relative AF_UNIX、peer/current-owner/fixed-binary handshake、delivery ledger及restart/response-loss真实canary。未完成这些之前不得把本切片表述为stable server或提升R2–R6成熟度。

## 2026-09-01：ADR 0073 activation V2 跨 runner 证据模型审计

GitHub RC1 canary 已把当前发布阻塞收敛为一个可复现的证据模型缺口：run phase `33477653933` 用 build-once candidate 和真实 Pi 到达 `REVIEW_PENDING`，finalize `33477984364` 在另一台 macOS runner 上失败于本地身份 binding。根因不是 Agent、ResultIngress 或 ReviewDecision，而是 V1 同时把临时文件系统的 `device/inode` 当作跨阶段稳定主体；以相同 `activationId` 重签发只能产生新的 activation digest，不能建立权威连续性。

审计后接受 [ADR 0073](adr/0073-dogfood-activation-v2-host-portability.md)，并把实现边界收窄为同 canonical 布局的 ephemeral runner：activation 与 portable `identitySubjectDigest` 绑定 repository/root/path/size/hash/sourceHead/profile，但不绑定 PID/device/inode/time；每台宿主的新 observation 仍必须以 held fd 和 pathname recheck 强制验证 device/inode、size/hash 与 ABA。activation→observation→attempt/applicability→verification→review 整条 lineage 同步升级 V2，V1/V2 混用 fail closed；RC1 finalize 必须原样消费 run artifact 的 activation，禁止以相同 ID 重签发或延长。

本地证据包括 selfidentity 正向/负向与跨 host-object subject 测试、CLI/execution/runstore/productionruntime/planning/control/verification 定向测试、Schema tests、RC1 shell contract、architecture check、`go vet`、staticcheck 与 diff-check。全仓本地 `go test ./internal/...` 仍会在本机企业终端策略下卡住 Codex/OpenCode/Pi 临时 Go 测试二进制并产生 context deadline，不能冒充全绿；双平台全仓结果必须由 exact-head required CI 提供。finding 状态为 `CONTRACT-AND-IMPLEMENTATION-CLOSED / REMOTE-CANARY-OPEN`：只有新的 V2 run/finalize 达到 `ACCEPTED` 并产出 receipt/carrier 后，才能进入 RC1 publication workflow 收口；R1–R6 暂不升级。

## 2026-08-31：生产级 Agent Team 架构终审

新增 [《Marshal 生产级 Agent Team 架构终审》](production-agent-team-architecture-audit.md)，以 `main@10f743d93cdaa71a2a3b181da3134f4a2c5dbe87` 为代码快照，交叉复核 production import graph、491 个本机 dogfood Run、Git/CI/Issue 历史，以及 Anthropic、OpenAI、Temporal、Kubernetes 与 GitHub 的一手生产资料。

终审结论：Marshal 的确定性 Kernel、唯一 authority ledger、ResultIngress、独立 Verification/Review 与 effect reconcile 方向成立，不应重写；当前欠缺的是 `Intent → Discovery → DeliveryProposal → UserApproval → WorkGraph → Integration → Outcome` 的真实产品闭环，而不是更多横向 Provider 或更细的底层合同。当前 fixed CLI real-Pi `ACCEPTED` 证据只证明 single-task kernel，不构成生产 Agent Team。路线保持先完成 ADR 0068 RC1；RC1 后第一条纵切应是 simple prompt → approved proposal → one real Task 的 GoalLite walking skeleton，随后才用最多 3 个并行节点和一个 Integration Node 证明真实加速。

审计快照的 required CI 仍为失败：Ubuntu 有一项 server recovery 测试 600 秒超时，supervisor 旧 fixture 仍绕过 sealed Run-start proof，macOS quality 因矩阵失败取消；因此该快照明确不具备 release candidate 资格。RC1 后应把既有 stable hardening 合同与 Agent Team 产品纵切拆成两个独立验收轨道，避免任一方向再次以组件数量遮蔽真实出口。

该终审同时记录了本机 Run 的效率基线：`ACCEPTED=117/491`、`BLOCKED+REJECTED=291/491`、非终态 `68/491`、多 Attempt `162/491`、`review.rework=211`，且历史 `worker.failed` 只有 `19/211` 具备可用于自动止损的 typed 信息。上述数据来自单机自举，不能外推为行业成功率，但足以说明下一阶段应把 plan/spec/environment 缺陷前移到 Worker 启动前，并用真实 outcome、first-pass、successor amplification 和并发 wall-clock speedup 取代 PR/ADR/组件数量作为进展指标。

## 2026-08-31：S2′ path B existing-worktree production-composition 切片

在 `feat/pi-s2-production-composition@d65785d` 基线上，[ADR 0069](adr/0069-attempt-reservation-and-existing-worktree-allocation.md) 冻结的 S2′-B（existing-worktree 绑定）此前只有 resultingress 层的 RB1 closed-union 与 PreparedExecution path B 投影，productionruntime 组合根与 fixed CLI 仍走 path A（staging provision），导致 closure WorkingDirectory 与 allocation receipt live 身份不匹配。本切片落地真实 path B 生产纵切，不放宽任何强制门禁：

1. 新增 [ADR 0070](adr/0070-existing-worktree-frozen-inputs-digest.md)（Accepted），冻结 `existing-worktree-binding/v1` 的 `FrozenInputsDigest`（canonical JSON closed struct {specDigest, policyDigest, capabilityDigest} 的 sha256）、`RepositoryOwnerDigest`（exact current `ControlOwnerState.FactDigest`）、`ExpectedAttemptSequence`（bind admission 前当前 `AttemptAuthorityState.Revision`）派生口径；明确 `BaseSHA`/`WorktreePath` 已在 `ExistingWorktreeBindRequestV1` 绑定不重复入 digest，且不使用 `ReservationKeyDigest`。不扩大协议。
2. `internal/productionruntime/existing_worktree_bind.go`：`CompositionInputs`/`CompositionLedger` 接收 held `ExistingWorktreeDescriptorGraphV1` + 目标 worktree `*os.File`，单边配置 fail closed；`bindExistingWorktree` 在 `BindOwnerToAttempt` 后用 `resultingress.NewExistingWorktreeAuthority` + `allocationcontrol.NewExistingWorktreeController` 完成 Bind，Run descriptor 用 `runstore.DupRunDirectory` + `allocationcontrol.NewDescriptorBoundRunV1`；binding 使用 ownerState.FactDigest、reservation.ReservationFactDigest、bound.OpenedDigest、identity lease/allocation/fencing、冻结输入 digest、bound.Revision。Core 侧 `existingWorktreeCurrentVerifier` 在打开 `RunAuthority` 前从 durable READY 投影与当前 owner/Attempt 派生 immutable expected current，在 callback 内重验 exact current owner 与 Attempt head/revision，不再在持有 `RunAuthority` RLock 时 `ReadRunStartAuthorityUnderLease`。replay gate 接受 staging provision receipt 与 existing-worktree bind receipt 的严格 closed union；path B 不把 closure 重封到 staging。
3. `internal/cli/existing_worktree_graph_darwin.go` + `sealed_ready_darwin.go`：fixed CLI 构建并持有 repository descriptor graph + exact `projection.WorktreePath` target，支持 `.git` 为目录或 linked-worktree `.git` 文件；固定 `/usr/bin/git --git-common-dir` 仅作 locator，最终由 `NewExistingWorktreeDescriptorGraph`/`NewLinkedExistingWorktreeDescriptorGraph` 与 held descriptors 校验；所有句柄生命周期覆盖 ComposeRuntime/Prepare/Start，退出关闭；改用 `AcquireExisting`（ADR 0069 §4 existing-only）。不生成或执行临时二进制。
4. 定向测试：`FrozenInputsDigest` 字段漂移、path B bind receipt 到 PreparedExecution、replay 无 sibling、held target identity drift 拒绝且无新增 bind authority、单边 inputs 拒绝、path A staging 回归不变，以及 fixed CLI 对 linked repository graph 与 Darwin symlinked target path 的覆盖。真实 Pi canary 必须等待 attempt-aware argv 与 exact-owned terminalization 接线，禁止用宽泛 `pkill -f` 代替生命周期控制。

本切片不改变信任边界（仅 ADR 0070 澄清字段口径），不暴露 raw path 到 prompt/transcript/public schema，不引入 `productionruntime → adapter/pi` 或 `processsupervisor` 新依赖，不使用 `ReservationKeyDigest` 作为 `FrozenInputsDigest`。`architecture_check.py` 通过。`I186-R2–R5` 仍为 `COMPONENT`，不据此宣称 RC1、stable 或 production 已完成；real Pi canary、terminalization、独立 Decision ACCEPTED 与 same-bytes RC1 仍是后继。

## 2026-08-30：S1′ ResultIngress Darwin 全绿切片（基线 11 失败 → 0）

在 `main@054789c` 基线上，`go test ./internal/resultingress -count=1` 于本开发机（Darwin 25/arm64）确定性失败 11 项。本切片逐项定位并修复，现该包非 race 全绿；判定依据与修复如下，未放宽任何强制门禁：

1. `TestPreparedExecutionCreationOnceResolveAndSecretBoundary`：落地即坏的断言——`DecodePreparedExecution` 是纯闭合 wire-form 校验，无法拒绝"重算过 `PreparationDigest` 的任意 Pi 身份"（文档自洽时结构校验必然通过）。改为验证 seal 覆盖 Pi 身份（篡改不重算 digest 即拒）+ 自洽文档只是纯 wire form + ungrounded digest 经 `ResolvePreparedExecution` 必返回 `ErrPreparedExecutionUnavailable`。原测试自 `6e558d7` 创建起从未通过过。
2. `TestCommittedRunStartProofIsNarrowSharedAndSynchronous`：测试竞态——`deactivateAndWait` 是否观察到 in-flight 回调取决于调度。以 `guard.active == false`（escaped 已计算后的确定性锚点）作为入场 barrier 修复。
3. `TestSupervisorReconnectFactIsRequiredBeforeBusinessHeadReanchor`：实现缺口——caller-authored Collect（rebuild 重锚业务头）在无 reconnect 事实时被允许追加。在 `validateSupervisorCommandIntentAgainstState` 的 Collect 分支补上 `SupervisorReconnectFactDigest == "" → ErrAttemptAuthorityOrder` 门禁；全部现存通过用例均已有 reconnect 在先，无行为回退。
4. `openHeldDarwinAuthorityFiles`（6 项 HeldDarwin + Seal）：Darwin 25 APFS 的目录 `st_nlink` 计入常规条目，先冻结目录身份再创建 ledger/coordination 文件导致 `verifyCurrentNames` 的 Nlink 相等性永远失败，`OpenDarwinResultIngressStore` 在本机 OS 上不可达。修复沿用 owner-lock 既有"entry stable 后再冻结"模式：创建条目并 fsync 后对同一 directory object 重冻结身份。
5. `processExecutablePath`：`kern.procargs2` 返回未解析 exec 路径（macOS `/var` → `/private/var`），而后续 `openObservedSpec` 以 `O_NOFOLLOW_ANY` 打开必然失败。自观察现以 `filepath.EvalSymlinks` 解析为规范路径——`BinaryIdentity.CanonicalPath` 语义收紧为真实磁盘位置，调用方必须传规范路径（`ObserveCurrentCore` 比较因此更严格，非放宽）。
6. 测试 fixture 修正：`TestPreparedDarwinSeal` 的 `ObservedAt` 改为从真实进程生日派生（原硬编码 `2026-08-29T00:00:00Z` 是时间炸弹，晚于该日的任何运行必失败于 "precedes process birth"）；`advancePreparedAttemptToStarted` 的进程观察从 prepared Pi closure 的 `RuntimeExecutable` 派生（`AppendProcessStarted` 的 `processMatchesRuntime` 要求绑定该精确 runtime object）；`testPreparedSupervisor` 优先复用已绑定 owner（supervisor bootstrap 属于已绑定 owner 的主流程，epoch 轮换只属于显式 reconnect 恢复场景，此前无条件轮换使 prepared 文档 owner 绑定失效）。
7. Makefile `test` 目标注入真实 git head ldflags：`BinaryIdentity.SourceHead` 的 hex40 合同使未注入 commit 的 test binary（`commit="unknown"`）无法通过自身份校验——这是本机与 CI macOS quality 失败的直接原因之一。`make test` 现与 release 构建绑定同一 source head。

残留（均经 stash 验证为 `main@054789c` 既有，非本切片引入）：`internal/processsupervisor` 8 项 Darwin mechanics 失败（`process-supervisor-intervention-required` 等，HEAD 复现）；`internal/productionruntime` 2 项 owner-lock ABA 在全包上下文 flaky（单跑 10/10 + `-race` 单跑通过，结合《v1.0 Release Readiness》"Mac 本地质量门禁边界"所述企业终端按新 Mach-O/CDHash 拦截 test binary 的策略，本地结果不作为证据层级）；`TestHeldDarwinResultIngressUnlocksAfterPanic` 在 `-race` 下 flaky（HEAD 同样复现）。`R2–R5` 成熟度不变，仍为 `COMPONENT`；组合根接线（`owner → Attempt/ResultIngress → allocation → exact runtime → PreparedRunStart/Commit → execution.Run`）与旧 CLI/execution 测试迁移仍是下一切片。

**后续切片 2 收口（`3cc88ab`）**：processsupervisor 的 8 项失败全部由测试 fixture 缺陷造成，实现未放宽任何门禁——`digest()` helper 对多字符 label 产出超长非法 digest（7 项失败 + 2 项负向测试因错误原因通过），改为按 label 哈希生成合法 64-hex；spawn source gate fixture 未解析 `/var → /private/var` symlink 祖先即以 `O_NOFOLLOW_ANY` 打开，改为先 `EvalSymlinks`。该包现含 `-race` 全绿。productionruntime 2 项 ABA 全包 flaky 与 resultingress race flake 维持环境类判定不变。S2′ 组合根的架构落点已核实：`architecture_check.py` 对 `productionruntime → resultingress` 无条件放行，组合根 authority 实现必须置于 `internal/productionruntime`；`internal/cli` 的冻结债务允许其直接 import `execution/planning/processsupervisor/sandboxbridge`，supervisor 链的 ResultIngress 追加须经理 productionruntime 暴露的窄方法，不得新增冻结债务条目。

## 2026-08-30：切片 4b 迁移的单一前置——Pi 0.84.3 字节身份（fixture 可行性结论）

组合根工程（`97e07d0`…`dcdc494`，15 提交）已使 sealed 链在 darwin/arm64 端到端可用：CLI `executeRun` READY 分支（`e408d3b`）经 `ComposeRuntime` 驱动 `PrepareRunStart` 全链与 `StartPreparedRun` 的密封机制；TestMain 继承探测（`dcdc494`）使重入的测试二进制运行真实 supervisor 循环。

切片 4b 的 RUNNING 起点 fixture 经逐层核实被确认**与 canary 同源阻塞于 Pi 0.84.3**，不是缺失代码：

1. `derivePreparedExecution` 经 `Pi0843IdentityFromClosure` 强制 closure 为结构精确的 Pi 0.84.3（55 材料、每根精确字节数、entrypoint digest 固定为 `piEntrypointDigest`）；
2. `verifyPreparedCurrentSourcesLocked` 经 `VerifyCurrentClosure` 观察真实文件——合成/临时路径必然 fail-closed；
3. `OpenPi0843` 对本机 `/opt/homebrew/bin/pi`（0.83.0）按同合同拒绝。

因此 38 项旧测试（internal/cli 18 + internal/execution 约 20）的 READY→RUNNING 段迁移到 `ComposeRuntime → PrepareRunStart → StartPreparedRun` 的执行，在维护者升级 Pi 至 0.84.3 之前无法在本机验证；迁移模板（fixture 输入、TestMain 继承探测、其余 verify/review/publish 断言原样保留）已就绪。升级完成后，执行顺序为：sealed fixture helper 落地 → 38 项迁移 → 远端 CI 绿 → 真实 Pi canary → 独立 ReviewDecision ACCEPTED → same-bytes canary/carrier/tag → v1.0.0-rc1。R2–R5 成熟度不变，仍为 COMPONENT。

## 2026-08-30：READY→RUNNING 回归证据

在 `main@2fb2d58` 上运行完整 `go test ./internal/cli -count=1` 时，多个既有 CLI E2E 在 Attempt 创建前统一失败于 `READY to RUNNING requires sealed Run-start proof`。这确认当前 sealed Run-start 门禁已正确阻止未接线的 production composition，但也暴露出旧 Local MVP 测试仍假定可直接追加 `worker.started`；`runstore.Store.Append` 已明确拒绝该路径。该结果不能通过放宽门禁或伪造 `PreparedRunStart` 修复。

处置：保留该失败作为 `I186-ARCH-PREPARED-EXECUTION-AUTHORITY` 的实现证据；下一切片必须一次性完成真实 `owner → Attempt/ResultIngress → allocation → exact process/allocation runtime → PreparedRunStart/Commit → execution.Run` 组合根，并同步迁移仅覆盖 compatibility profile 的旧测试。当前 `R2–R5` 仍为 `COMPONENT`，不产生 RC1 或 stable 发布资格。

## 2026-08-30：Run-start producer seam 架构复核

`main@a6482db` 合入了 `resultingress.PrepareMacRunStart`/`CommitMacRunStart`。该 seam 仅在当前 owner lock 下重解析 durable `PreparedExecutionV1`，逐字段校验后委托既有 proof-producing `StartPreparedExecution`；它不创建 Attempt、owner、allocation 或 process facts，也不改变 `R2–R5: COMPONENT` 判定。同期删除了未接线且违反 architecture layer gate 的 `internal/productionruntime/factory.go` 与测试，避免无效 factory 形成第二 authority 入口。定向测试、architecture check 与 vet 已通过；全包 ResultIngress Darwin owner-lock fixture 的既有失败仍保持独立 blocker，fixed CLI production composition 与真实 Pi→独立 `ReviewDecision/ACCEPTED` 尚未形成。

- 审计日期：2026-08-04（2026-08-10 增补 Runtime 架构重置记录、首次 Sandbox SPI dogfood reject 增补记录与 Round 2 关闭记录；2026-08-11 增补 Control Plane 与 Provider Port 边界冻结记录，含 Round 4 独立评审八项 P1 关闭记录、Round 5 复核四项残留关闭记录、Round 6 复核两项残留——Control Plane authority namespace 与 Provider actor 域分离、typed cross-domain edge——关闭记录与 Round 7 复核三项残留——双键空间残留清除（权威对象 authorityNamespaceId 独占拥有、registration/snapshot/evidence authority ledger 事实、接纳关系归 authority ledger、controlPlaneId 逻辑权威身份）、Core-only typed edge 生命周期细化（issuer/source/target/operation/expiry/digest/revocation/replay/current-ledger recheck，issuer 恒为 Core 且不等于业务流 sourceActor、sourceActor/targetActor 按 edge 类型绑定，派生 token/handle 不得成为第二权威）、Public API 幂等/SSE/对象 key 修正为 authorityNamespaceId——关闭记录与 Round 8 复核一项残留——typed edge 跨域例外与适用范围（三类 typed edge 明确为 Provider actor 跨信任域访问默认拒绝的唯一 allowlist 例外，Public API/SSE 与 Core 内部权威引用无需 Provider typed edge）——与 Round 9 复核两项残留——跨域 fail closed 表述精确化（删除会无条件拒绝 MaterialAccessGrant 等合法 typed edge 的宽泛表述）、非 edge Port 与同域不自动授权（provider-registration/control 经 transport identity/该 Port AuthN/AuthZ/registration protocol 由 Core 写 authority ledger；securityDomainId 相同只是 provenance/partition 条件，不构成授权）——关闭记录；2026-08-12 增补 Issue #25 发布合并后 head reconcile 审计记录；2026-08-13 该 finding 随 typed reconciliation 实现合入关闭；2026-08-14 增补 Issue #53 CI 失败 rework 注入设计缺口审计记录，目标契约由 [ADR 0030](adr/0030-ci-failure-rework-evidence-and-injection.md)（Proposed，草案已提出/待接受）给出（接受后方冻结），实现待后续 implementation successor）

## 2026-08-29：S1′/S2′ producer chain 的 Attempt 与 existing-worktree P0

对accepted S1′ mechanics与S2′ production composition做真实producer预审后，发现两项不能由fixture绕过的P0：

| Finding | 严重度 | 当前状态 | 证据、影响与关闭条件 |
| --- | --- | --- | --- |
| `I186-RUN-ATTEMPT-RESERVATION` | P0 | `CONTRACT-ACCEPTED / IMPLEMENTATION-OPEN` | 首版ADR0069把reservation与`attempt-opened`混同。接受合同改为RB1 `attempt-reserved` active→consumed|cancelled、按RunID+exact READY seq/head lookup-before-mint；dispatch claim按reservation digest+RunID+reserved AttemptID lookup-before-claim并same-bytes replay，full identity与`attempt-opened`后置，budget三次读取held Run authority且只由sealed successor消费。定向修订 sourceHead `e2af179` 已独立 `APPROVE`（`P0=0/P1=0`）。关闭仍要求schema/protocol与并发/cancel/response-loss/legacy矩阵及fixed CLI producer通过；合同接受不关闭实现缺口。 |
| `I186-EXISTING-WORKTREE-ALLOCATION` | P0 | `ADR-PROPOSED / AGGREGATE-REWORK` | 首版把sidecar描述成独立allocation ledger，会形成第二authority。修订提案把Bind/Receipt/Release全部收回RB1 closed union；repository-global target uniqueness从held-owner下RB1 replay判定。固定sidecar仅为可重建projection，缺/落后先投影、损坏/超前fail closed，绝不能覆盖RB1。关闭要求aggregate rework独立复审、existing-only Run open、锁序/sidecar/crash/replay/ABA/secret/zero-target-mutation矩阵与真实Pi纵切。 |
| `I186-RESULTINGRESS-HELD-DESCRIPTOR` | P0 | `CONTRACT-ACCEPTED / IMPLEMENTATION-OPEN` | 当前ResultIngress store仍可能按pathname reopen authority对象，且`ObserveCurrentCore`不能先于`OpenOwner`产生current结论。这不是ADR0069新增信任决策：[ADR0066](adr/0066-production-composition-owner-acquisition.md)已经要求canonical held-descriptor边界。S1′ rework必须改held descriptor backend、拆分正确时序并证明path漂移时不打开替代对象；完成前不得接受sealed proof实现。 |

[ADR 0069](adr/0069-attempt-reservation-and-existing-worktree-allocation.md)首版`ebbfd86`独立审查为`P0=3/P1=4`，`1adf20c` aggregate复审只剩`P0=1/P1=1`；定向修订 sourceHead `e2af179` 已独立 `APPROVE`（`P0=0/P1=0`）并由维护者接受。以下结论仅记录 2026-08-29 当时状态：接受只冻结合同，尚未实现；R2–R5当时保持`COMPONENT`、R6当时保持`PLANNED/DESIGN`。当前成熟度以上方 2026-09-01 RC1 终验为准。

## v1.0 候选链审计 checkpoint（2026-08-28）

本 checkpoint 取代本文较早章节对“当前状态”的描述；旧记录保留用于解释偏航与修复历史，不得用其中曾经的 `DONE` 结论升级现行 Roadmap。

| 审计项 | 当前证据 | 判定 | 剩余退出门禁 |
| --- | --- | --- | --- |
| Pi fixed-bin strict E2E | Pi `0.84.3` 前置 canary 绑定 `sourceHead=d4b9647`，单 Attempt 通过 9 项 Gate，生成正式 ReviewPacket 并进入 `REVIEW_PENDING`。 | `PARTIAL-INTEGRATION/PASS`；它没有导入独立 ReviewDecision、没有进入 `ACCEPTED`，也不是当前 `main` 终验。 | 在最终主线重跑；由独立 reviewer 产生绑定精确证据的 Decision；通过 `task review --decision` 进入 `ACCEPTED`；重跑故障矩阵。 |
| Qwen workspace live | Qwen Code `0.22.0` ordinary-user workspace live adapter 已通过。 | `COMPATIBLE-ORDINARY`；不是 `LaunchCapable`，不能替代 Pi 的 production reachability 证据。 | 若未来升级 production profile，必须另行提供 Sandbox/authority 证据；当前不阻塞 v1。 |
| durable authority | ResultIngress admission→worker-result→Run journal 的 crash-atomic 持久化/恢复已随 `main@912f659` 合入。 | R2/R3 仍保持 `COMPONENT`：ADR 0056 terminalization barrier 尚未复用同一 authority transaction，当前主线 canary 也未覆盖 cleanup/restart 全矩阵。 | 把 terminalization CAS 接到 `912f659` 的唯一 transaction；补 restart/replay/stale/revoke/replace/expiry/cleanup 真实路径负测；证明无第二真值。 |
| server/controller selector | durable start/status/recovery controller 已随 `main@44ee8c9` 合入；production selector 已随 `main@d4b9647` 收紧到 `LaunchCapable`。 | controller 层已接线，但 ADR 0056 的 launch/process observation、admission/terminalization CAS 与 cleanup transaction 未接入，R4 保持 `COMPONENT`。 | server crash/lost worker/failed worker 跨进程恢复必须在 eligibility 立即 fence 后保留 cleanup binding，直到 `cleanup-completed`，并产生唯一 durable receipt/recovery decision。 |
| RC 与 stable release | ADR0068的Darwin arm64 CLI-only RC1合同已接受，但guard、canary与产物均未实现，尚未发布任何RC。 | 只可按S1′→S2′→Attach/rebind→terminalization→fixed CLI Pi+独立Decision `ACCEPTED`→same-bytes RC1推进；不能标`RELEASED`。 | RC1必须exact opt-in、缺资产不fallback且不自动activation；其后才推进server、Issue #212 managed signing/notarization与Linux stable gate。 |

当前 Mac-first RC1 关键路径按 ADR0067/0068 固定为：R2/R3 的 S1′→S2′→Attach/rebind → R4 terminalization → R5 fixed CLI Pi + 独立 Decision/`ACCEPTED` → R6 same-bytes RC1。server recovery、managed signing/notarization与Linux stable属于RC1后的stable后继。任何单次 live pass、候选 commit 或 reviewer verdict 都只是对应门禁的输入，不等于阶段关闭。

### ProcessBridge 启动权威接缝（2026-08-29）

代码审计确认 `application.PreparedRunStart` 只保存 ID、`READY` Run sequence/head 与 `preparationDigest`；`productionruntime.DurableRunAuthority` 没有 held `WithCurrentRunAuthority` 或唯一 `CommitRunStartOutcome`，`ProcessBridge` 也无法从 held Attempt authority 解析 current owner、完整 Allocation receipt、`launch-authorized`/`StoredClosureV1` 与 Pi identity。更关键的是，`process-started` 只证明真实 Node 已处于 `exec-stopped`，不是 `RUNNING`；只有 exact successful `resume` outcome 的 `state=running` 才能授权 Run lifecycle commit。现状同时无法证明启动瞬间 Node/entrypoint/material bytes 与 configured `PiProfile.IdentityDigest()` 闭合，也无法排除把 raw closure 复制进 Run/Supervisor ledger。

| Finding | 等级 | 状态 | 处置 / 关闭条件 |
| --- | --- | --- | --- |
| `I186-ARCH-PREPARED-EXECUTION-AUTHORITY` | P0 | `ACCEPTED-CONTRACT/OPEN-IMPLEMENTATION` | [ADR 0063](adr/0063-prepared-execution-authority-and-production-chain.md) 已冻结 creation-once、secret-safe `PreparedExecutionV1`、完整 Attempt/Run/current-owner/source-fact binding、`ResolvePreparedExecution` 与 held `WithCurrentRunAuthority`。完整 Allocation receipt 只从 held allocation authority 解析，`StoredClosureV1` 只从 held Attempt authority 的 `launch-authorized` 原件解析；Run/Supervisor ledger 只存 digest/projection。关闭实现须证明 `BindOwnerToAttempt` 先于 launch/prepared，owner epoch ABA 只能 authenticated reanchor/intervention，callback zero/double/escape/goroutine/reentry 与 raw argv/env/path leak 全部在副作用前拒绝。 |
| `I186-ARCH-PI0843-IDENTITY-CLOSURE` | P0 | `ACCEPTED-CONTRACT/OPEN-IMPLEMENTATION` | ADR 0063 已冻结不含 per-Run `agentLaunchSpecDigest` 的 path-free 静态 `Pi0843IdentityV1` canonical preimage；Run spec 继续由 source closure/Prepared 单独绑定。Start 必须 mutation-adjacent、descriptor-relative/nofollow 重新打开 current Node、`pi-bundle/cli.js`、两个 roots、55 materials 与 cwd，重算后同时匹配 source、旧 held FD 和 configured `PiProfile.IdentityDigest()`，再保留真实 exec 双 barrier。关闭实现须覆盖“旧 FD 未变但 canonical path 已替换”、path/inode/hash/material-set/cwd ABA、FD close/replace 与 bootstrap 前后漂移。 |
| `I186-ARCH-RUN-START-OUTCOME-AUTHORITY` | P0 | `ACCEPTED-CONTRACT/OPEN-IMPLEMENTATION` | ADR 0063 已把 `CommitRunStartOutcome` 冻结为唯一 `READY → RUNNING` 提交点，并要求 current Attempt ledger 同时闭合 exact `process-started` 和 authenticated successful `resume(disposition=ok, reason=process-resumed, state=running)` outcome。关闭实现须覆盖 resume intent/outcome/Run commit 各 response-loss 窗口、rejected/non-running/cross-child outcome、并发 start与 Run/owner head漂移，证明零重复 resume/launch与唯一可重放 projection。 |

这些 finding 只解阻 R2/R3 的 ProcessBridge producer seam，不新增 milestone 或通用恢复框架；ADR 0063 已接受，后续顺序固定为一个 bounded authority component → 立即相邻的 fixed Marshal composition 切片。两切片之间禁止插入第二个 component 或无关工作，component 完成不升级当前成熟度。

### Run-start proof 职责分离纠偏（2026-08-29）

对 ADR 0063 implementation seam 的进一步审计确认：generic held-authority callback 与 raw-source closure 不能仅靠 callback 纪律或返回后清空变量防止副本逃逸；让 ResultIngress 与 runstore 都携带/解释 owner epoch、dispatch generation 或对方 ledger facts，也会建立第二 current 判定者、反向 callback 和 response-loss 猜测。该问题是合同边界缺口，不应通过继续给旧 API 补零散逃逸检查解决。

| Finding | 等级 | 状态 | 处置 / 关闭条件 |
| --- | --- | --- | --- |
| `I186-ARCH-RUN-START-PROOF-BOUNDARY` | P0 | `ACCEPTED-CONTRACT/OPEN-IMPLEMENTATION` | [ADR 0065](adr/0065-sealed-run-start-proof-and-one-way-composition.md) 冻结由 ResultIngress 在 current-ledger borrow 内唯一前后重验 owner/Attempt/generation 并 mint shared-guard `CommittedRunStartProof`；claim 是 non-authority，禁止 owner/generation/Run head/successor。runstore 只在自己的 outer borrow 内消费 active proof、复核自身 lease/head/state 并写唯一 successor，绝不读/镜像 ResultIngress facts。关闭仍需要 S1 proof hostile/race 矩阵与 S2 fixed composition 真实纵切通过。 |
| `I186-ARCH-RUN-START-LOCK-ORDER` | P0 | `ACCEPTED-CONTRACT/OPEN-IMPLEMENTATION` | 合同固定锁序为 repository owner → runstore outer borrow → ResultIngress borrow → Supervisor → outcome fsync → ResultIngress final recheck/mint → runstore self-only CAS → deactivate/release；禁止 reacquire、handoff gap、reverse authority callback。关闭需要 deadlock/反序负测与 response-loss 两账本各自 replay 证据。 |
| `I186-ARCH-RUN-START-COMPOSITION-BYPASS` | P1 | `ACCEPTED-CONTRACT/OPEN-IMPLEMENTATION` | 合同精确限定 `internal/productionruntime/prepared_run_start_composition.go` 中两个 exported seam 各唯一一个 typed `CallExpr`、direct `FuncLit` 与 projector 单次末参数传递；runstore 单文件四 selector/primitive 唯一 callsite；generic `Append READY→RUNNING` 必须拒绝。关闭需要全 production build-tag AST/go-types 扫描、direct append/second wrapper/存储或异步 projector 负测。 |

ADR 0065 已于 2026-08-29 接受，提案基线为 `main@40fa493d1955fd6d039169483a6501a787d3cc14`；接受只冻结合同，不撤销 ADR 0063 已冻结的 Pi identity、held source 与 exact resume 业务条件，也不表示任何旧实现候选可合入。S1/S2 实现仍未开始，实施顺序只允许 S1 proof component → S2 fixed composition；ADR 0056 terminalization 是之后的独立切片。

### S2 production composition 构造环审计（2026-08-29）

对 `main@7de2a70cec112df5fbf2b36f85ce5878f227c40c` 的构造审计确认：`internal/productionruntime` 只有 package-private `newController`/`newRuntime`，没有 fixed `./bin/marshal` 可调用的 production factory；`Runtime.Status` 固定返回 `production-composition-incomplete`；CLI/server 仍保留 legacy `execution.Run`/child CLI 路径。更直接的机械阻塞是 `openRepositoryOwnerLock` 在加锁前要求完整 `ControlOwnerAcquisition`，而下一 owner epoch、前驱 fact 与 current Core observation 只能在锁内打开 ResultIngress 并 `OpenOwner` 后安全产生，形成构造环；此外 production 若接受 `MARSHAL_STATE_DIR`，同一 repository 可形成两锁两 ledger。因此 ADR 0065 §10 的“只新增 composition 文件”边界不能原样实施。

| Finding | 等级 | 状态 | 处置 / 关闭条件 |
| --- | --- | --- | --- |
| `I186-ARCH-PRODUCTION-FACTORY-MISSING` | P0 | `CONTRACT-ACCEPTED/IMPLEMENTATION-BLOCKED` | [ADR 0066](adr/0066-production-composition-owner-acquisition.md) 冻结唯一 Darwin arm64 fixed `./bin/marshal` production factory、canonical repository `.marshal` 与完整 component graph fail-closed 构造。关闭 S2 仍需要 factory 只从 fixed `cmd/marshal` 本地 CLI mutation/inspect application adapter 可达且入口只持有 `PublicApplicationPort`，legacy/fake/第二 store/`marshal control-plane serve`/独立 `cmd/marshal-server` 不可达，`Status` 对完整/不完整/recovery 真实分型及真实 Pi E2E。fixed `marshal control-plane serve` 是其后独立 ADR 0062 transport slice，必须另证 authenticated Port adapter 与 durable delivery ledger。 |
| `I186-ARCH-OWNER-ACQUISITION-CONSTRUCTION-CYCLE` | P0 | `CONTRACT-ACCEPTED/IMPLEMENTATION-BLOCKED` | ADR 0066 冻结按 canonical `ControlOwnerScope` 先取得 descriptor-bound 物理锁，再在锁内 `OpenResultIngress → OpenOwner → ObserveCurrentCore → construct candidate → one-shot provisional verifier + AcquireOwner/fsync → exact replay → current verifier`。关闭需要 provisional verifier 不逃逸且不能用于 Attempt/operation，并通过并发单赢家、append 前后 crash/response-loss、epoch/head/entry ABA 和 root 漂移矩阵；不允许在锁前猜测 acquisition。 |
| `I186-ARCH-PRODUCTION-STATE-ROOT-SPLIT-BRAIN` | P0 | `CONTRACT-ACCEPTED/IMPLEMENTATION-BLOCKED` | production factory 不接受任意 authority root，固定从 held canonical repository 派生 `<repository>/.marshal`；非空 `MARSHAL_STATE_DIR` 在 owner/ledger 前拒绝。关闭需要同一 repository 的外部 root、环境 override、symlink/rename 与第二 ledger fixture 全部 fail closed；legacy/test 外部 root 不得成为 production 证据。 |
| `I186-ARCH-S2-FILE-BOUNDARY-INFEASIBLE` | P1 | `CONTRACT-ACCEPTED/IMPLEMENTATION-OPEN` | ADR 0066 作为 ADR 0065 的 S2 implementation successor，仅把允许修改面扩到 owner lock、controller、单一 factory/composition、fixed `cmd/marshal` 的窄 application adapter/本地 CLI mutation/inspect、architecture tests 和真实 Pi E2E；proof 方向、S1→S2 adjacency 与 terminalization/provider/server transport/release 排除保持不变。接受只解除治理 blocker，不升级 R2–R5。 |

ADR 0066 提案 `69574533fd7c7e0e91b4ef45a2c902885c2eeb4c` 经独立 reviewer 复审 `APPROVE`（P0=0、P1=0）后于 2026-08-29 接受。接受只解除 S2 的治理 blocker，不表示实现完成；S1 后仍须立即进入该 S2 边界。ADR 0066 不改变 ADR 0062 的 fixed binary、loopback authentication 或禁止 child CLI 信任模型。

### S1 mechanics 重复 P1 与 Mac-first 减法审计（2026-08-29）

对第二轮候选的 exact diff 与当前 `main@84d2dcd6bb78cb7fa47ed1d3040a1f3bea5a0f11` 重新比较后，结论不是继续补丁，而是合同过度：`a6a0d638f45d6902b9c453b1e600b5f798380d82` 在 Core 与 Supervisor 两处重复 source currentness，仍无法在 ordinary-user 边界证明两次观察之间的 same-UID pathname连续性；`6298eaebebb9ec705e74903cdf2a32dda0b6a62c` 又依赖 `506a6470767f187290df08b1060834ed59aeabdb` 的大范围 runstore substrate，把 Run currentness扩成第二套Attempt/owner/generation authority。三份候选均冻结为审计/测试输入，不直接合入，也不在原分支上进入第三轮同类rework。

| Finding | 等级 | 状态 | 处置 / 关闭条件 |
| --- | --- | --- | --- |
| `I186-ARCH-ORDINARY-SOURCE-GATE-DUPLICATION` | P1 | `CONTRACT-ACCEPTED/IMPLEMENTATION-FROZEN` | [ADR 0067](adr/0067-darwin-ordinary-user-launch-and-attach-recovery.md) 已把Core收窄为pre-bootstrap无副作用current closure admission，随后释放临时FD；fixed Supervisor的`spawn`成为唯一mutation-adjacent exact role/record/file set gate，并保持同一FD组贯穿post-exec barrier。关闭须覆盖Core检查后pathname替换、material增删/换位、cwd与Allocation `LiveIdentity`不等，且不得宣称fully controlled same-UID防护。 |
| `I186-ARCH-RECONNECT-AUTHORITY-AMPLIFICATION` | P1 | `CONTRACT-ACCEPTED/IMPLEMENTATION-FROZEN` | ADR0067已停止新`process-supervisor-session-reconnected` producer。唯一恢复序列是持续held repository owner/acquisition→exact RB1 no-pending→绑定predecessor Attempt head与new acquisition的`control-owner-bound` successor→零持久化mutation的只读`Attach`→引用exact successor fact的`bind-authority(owner-successor) intent→execute→outcome`；Attach client/verifier不得逃逸。跨owner pending command与identity不唯一固定permanent intervention；pre-`process-started`只在intervention前exact证明零Supervisor/child/command副作用时允许no-effect abort/cleanup链和预算内新Attempt，否则永久禁止cleanup/release/successor。 |
| `I186-ARCH-RUNSTORE-SUBSTRATE-OVERREACH` | P1 | `CONTRACT-ACCEPTED/IMPLEMENTATION-FROZEN` | `506a647`跨24文件修改execution/review/selfidentity/runstore，`6298eae`堆叠其上；该基线不合入。S1′必须直接建立在当前descriptor-bound `runstore.Store`/`Lease`/open authority上，只新增private projector、唯一successor和generic Append拒绝；不得复制ResultIngress owner/Attempt/generation。 |

ADR0067保留的硬门禁是current-ledger recheck、exact successful resume、ADR0065 sealed proof、唯一Run successor、fixed Supervisor mechanics、ADR0064 control-directory identity、ADR0066 canonical factory。S1′只允许runstore内部窄lease shared-guard/borrow、descriptor-bound strict journal与read-only projection，唯一exported mutation seam仍为`WithPreparedRunStartAuthority`；它原冻结的S2′ producer简写已被ADR0069预审证明缺少reservation/full Attempt与诚实existing-worktree binding，因此在ADR0069复审接受前不得按旧简写实施。候选修订顺序为`reservation→dispatch/full identity→attempt-opened→owner binding→RB1 allocation facts→Prepared→S1 start`，不允许seed/Fake/legacy`execution.Run`或sidecar authority。ADR0067的Mac ordinary-user no-effect/permanent-intervention二分保持不变；其提案`1e05fb831c04a1c87e7f4ecdc677c97beb9d88e6`已接受，但不关闭ADR0069新增P0或升级R2–R6。

ADR0068提案 `9cfa1b65275d2e23f18b958a05d027adec6af8fd` 经唯一独立 reviewer `APPROVE`（`P0=0`、`P1=0`）后接受。它仅为 `v1.0.0-rc1` 部分取代0051/0052/0062的首发前置，冻结unsigned Darwin arm64 CLI-only local-dogfood preview；真实顺序是S1′→S2′→Attach/rebind→terminalization→fixed CLI真实Pi+独立Decision `ACCEPTED`→same-bytes RC1。server、managed signing/notarization和Linux stable转为RC1后继。该段“尚未实现”的判断是 2026-08-29 历史状态，已被上方 2026-09-01 RC1 终验取代；R2–R5继续为`COMPONENT`，R6现为`IN_PROGRESS/COMPONENT`。

### Darwin 控制目录阶段化身份审计（2026-08-29）

exact-head macOS CI 证明，APFS 在 Supervisor 合法创建 nonce、journal、socket 与输出对象时可能改变目录 `st_nlink`；既有全字段 runtime equality 因此会把同一目录对象误判为 ABA，并让本应在 receipt `fsync` 后拒绝的 post-command drift 提前停在 journal sequence `1`。这不是测试断言问题，也不能通过删除 link-count hostile gate、跳过目录枚举或放宽 control object identity解决。

| Finding | 等级 | 状态 | 处置 / 关闭条件 |
| --- | --- | --- | --- |
| `I186-ARCH-DARWIN-CONTROL-DIRECTORY-PHASED-IDENTITY` | P1 | `ACCEPTED-CONTRACT/IMPLEMENTATION-HELD` | [ADR 0064](adr/0064-darwin-control-directory-phased-identity.md) 已冻结 initial empty完整精确身份、setup后final observation、runtime稳定对象字段比较与 descriptor-relative phase-aware exact entry set；提案 `7d91e9704c69dcbde987d64d6fa93e0a06d7f32a` 经独立聚合复审 P0/P1/P2=0。实现证据定位：候选 `765617c20ea3faee71af980d70a35ecd06e3462a`，测试 `TestCommandBoundaryRejectsPreAndPostReceiptDriftWithoutResponse`、`TestRuntimeControlBoundaryAllowsFrozenOutputsAndRejectsUnknownEntry`、`TestControlDirectoryObjectComparisonIgnoresOnlyLinkCount`；其中候选尚缺 final setup恰为三项与pre-collect输出absent的phase-aware exact-set gate，在补齐前保持冻结。关闭还须证明initial/final同一稳定对象、unknown/early entry与稳定字段漂移 fail closed、合法APFS `LinkCount`变化贯穿command/reconnect/transcript/close，且post-receipt drift保留sequence `3`、零response。 |
| `I186-ARCH-DARWIN-TRANSCRIPT-CROSS-READ-IDENTITY` | P2 | `OPEN-INTEGRATION/DEFERRED-HARDENED` | 当前v1 protocol/authority只持久化transcript/stdout/stderr content digest、bytes与truncation，不持久化三个data object的inode identity；现有门禁只能证明每次held-FD读取事务内identity/size稳定和content exact。ADR0051 ordinary-user不覆盖fully controlled same-UID在两次读取间以同mode/同内容新inode替换。当前v1按content等价接纳，不得宣称跨时间对象连续性；未来hardened或需要object continuity时须单独升级protocol/projection或持有跨admission生命周期FD，不得借ADR0064局部修复静默扩面。 |

该 finding 只修正 ADR 0059/0060 的 Darwin control-directory 局部语义，不插入第二套 authority或扩大v1范围；它不授权 Linux/hardened profile、stable release或任何 milestone升级。

## Darwin ordinary-user 进程生命周期合同审计（2026-08-28）

代码审计确认：crash-atomic ResultIngress transaction 已随 `main@912f659` 合入，durable DispatchLease ledger 与 server run controller 也已存在；但 Local allocation/process projection、Core-owned launch/handle、terminalization 对同一 admission CAS 的复用、eligibility terminal 与 cleanup completion 仍未形成同一耐久纵切。[ADR 0056](adr/0056-darwin-process-observation-and-attempt-terminalization.md) 已于 `main@ecee8d4` 接受；实现与生产接线保持开放，R2–R5 不升级。[ADR 0057](adr/0057-durable-local-allocation-recovery-and-production-composition.md) 已于 `main@9aff8cc` 接受，但只冻结 durable allocation recovery 与唯一 production composition 合同；RB3 尚未实现该合同，本次接受不把任何能力从 `COMPONENT` 升级为 `INTEGRATED`。[ADR 0058](adr/0058-interpreted-agent-launch-identity.md) 已于 2026-08-28 接受，冻结 Pi 0.84.3 的显式 Node runtime、两个 versioned material roots、held-FD/双 barrier 和 ResultIngress 最终接纳前的 current-authority 全量重验；在实现、故障矩阵和最终 fixed-bin 真实 Pi canary 完成前，Pi 仍不是 production reachable。

| Finding | 等级 | 状态 | 处置 / 关闭条件 |
| --- | --- | --- | --- |
| `V1-LOCAL-PROCESS-AUTHORITY` | P0 | `CLOSED-CONTRACT/OPEN-IMPLEMENTATION` | ADR 0056 冻结 Core-owned launch coordinator：它在放行 workload 前负责 spawn/process-group、PID birth、cwd/executable held-FD、process handle 与 `process-started` authority fact；Provider 只能出 claim。关闭实现须从 authority facts 恢复 projection，并通过 PID/PGID reuse、path swap、FD mismatch、伪造 claim 与 launch-barrier crash 负测。 |
| `V1-ATTEMPT-TERMINALIZATION-ORDER` | P0 | `CLOSED-CONTRACT/OPEN-IMPLEMENTATION` | ADR 0056 冻结 ResultIngress admission 与 terminalization/eligibility terminal 共用 authority transaction/CAS；CAS 固定业务结论并立即 fence，随后安全终结 process group、Provider allocation terminal receipt、`cleanup-completed`，最后才 unlock/successor。关闭实现须覆盖 CAS race、每个 crash point、late result、lost response 和两次重启重放。 |
| `V1-CROSS-ORCHESTRATION-KILL` | P0 | `CLOSED-CONTRACT/OPEN-IMPLEMENTATION` | ADR 0056 把 v1 控制单元收窄为 Core 创建/观察的 cooperative/non-detaching process group；普通 Darwin 不承诺全后代 containment。只有当前合法 orchestrator 且完整 authority/Attempt/allocation/lease/generation/root birth/PGID 匹配时才允许 group signal；detach、identity conflict、第二 orchestrator 均零扩大 kill 并 intervention。关闭实现须有真实 Darwin 负测。 |
| `V1-LEASE-NORMAL-TERMINAL-FACT` | P1 | `CLOSED-CONTRACT/OPEN-IMPLEMENTATION` | ADR 0056 将 Dispatch eligibility 与 cleanup completion 正交：normal `completed` 及既有 `cancelled|expired` 均立即 generation bump/fence；cleanup-only binding 保留到独立 `cleanup-completed`，之后 `lease-released` 只释放该 binding，不是 lease state。关闭实现须保持旧 ledger replay、终态/CAS 冲突 fail closed，并证明异常路径不会提前释放 cleanup authority。 |
| `V1-CORE-RESTART-PROCESS-MECHANICS` | P0 | `CLOSED-CONTRACT/OPEN-IMPLEMENTATION` | RB2/B2 预审证明直接 `PT_TRACE_ME` 启动者的 wait right、tracer、held FD 与 pipe 无法转移给重启 Core；PID/路径重验不能重建 mechanics。ADR 0059 已接受，冻结由固定 `marshal internal process-supervisor` 持有 per-Attempt mechanics、Core 只经 current authority 命令重连的合同。实现关闭条件仍是完成真实接线并通过两次 Core restart/lost-response；Supervisor crash 保持 intervention。 |

Mac ordinary-user 只证明在可信单用户宿主上的可恢复进程记账；它不证明恶意 workload 不逃逸，不替代 Linux/hardened authority，也不解除稳定发布的签名/notarization 门禁。

### ADR 0056 RB1：单一 Attempt authority 组件 checkpoint（2026-08-28）

RB1 在 `internal/resultingress` 的同一物理 append-only 文件中加入 per-Attempt `revision/head` CAS 链，将 `attempt-opened → launch-authorized → process-started → result-admitted|terminalization-barrier → cleanup` 收敛为单一权威序列。逻辑唯一键固定为 `AuthorityNamespaceID + taskId + runId + attemptId`；`attempt-opened` 冻结 DRC namespace ref、allocation/lease、dispatch generation、fencing token digest、当前 orchestrator 与 Run authority digest，以上任一漂移只能与既有逻辑 Attempt 冲突，不能创建 sibling chain。`attempt-opened`、`launch-authorized`、`process-started` 和 barrier 都要求 current Run authority verifier 在完整 replay/read/CAS 期间保持 authority，verifier 重复调用 callback 会 fail closed 且 callback 最多执行一次。`launch-authorized` 后、`process-started` 前的耐久投影明确为 `launch-uncertain`，不能把“未看到进程”解释成“确定未 launch”。

本组件还使所有 ResultIngress kind（包括 `checkpoint`、`heartbeat`、`log`）在 barrier 后进入 quarantine；admission 与 barrier 通过同一 store lock、同一物理日志和同一 per-Attempt CAS 判序。barrier 只把已提交的 `WorkerResult` 视为业务结果，后续辅助 admission 不会覆盖它；没有 `WorkerResult` 时则明确关闭空的业务结果槽。barrier 同一 CAS 原子绑定业务 admission（或空 closure）、terminal generation bump、封闭的 `completed|cancelled|expired` eligibility union（`security-critical-revoke` 属于 `cancelled` 闭集）与非 bearer 的 cleanup binding fact，异常终态不会被伪装成 `completed`。

governed DRC 必须逐字段匹配冻结 tuple 和 `process-started.commandId`；多候选、命令漂移或身份漂移都确定性拒绝，不能依赖 Go map 遍历选择。`ProcessObservation` 要求 cwd 为绝对规范目录、executable 为绝对规范普通文件，文件类型使用原始 POSIX `S_IFMT` 语义；`observedAt` 必须是 canonical UTC RFC3339Nano 且不早于 process birth。

cleanup 使用时必须同时经过外部 current Run authority verifier、精确 tuple、terminal generation、closed operation allowlist 与未 release 状态；单独持有 binding digest 不构成权限。真实 side effect 必须放在 `WithAuthorizedCleanup` 的 held-authority callback 内，独立 `AuthorizeCleanup` 只是无授权效力的 preflight。权限按 phase 收窄：process terminal 前只允许对已确认进程 `Signal`；process terminal 后永久关闭 `Signal`，仅在 allocation terminal 前允许独立的 Provider `Terminate`；allocation terminal 后不再授权 Provider effect，只能追加/精确重放下一合法 cleanup fact；cleanup complete 后只进入 release；release 后所有 effect 永久拒绝，只允许在 current Run authority 与精确 tuple 下无副作用地重放同一 `cleanup-released` append。`AttemptStates`/`PendingAttemptStates` 从同一日志确定性重放，供重启恢复枚举使用，不引入第二身份索引。

`LeaseLedger` 只接收带 Attempt authority head 的 `completed|cancelled|expired` 封闭投影，并保留 completion/cancel reason；它不能独立决定新 Attempt eligibility，新 Attempt terminal binding 也不能被新 lease 复活。这里必须明确：Attempt/Result 共用一个物理日志，而 `LeaseLedger` 仍是另一个物理 append-only read model，两者**没有**跨文件原子事务。既有 `AppendCancel`/`AppendExpire` 与历史事实格式仅为旧调用链兼容；RB3 必须使所有新 Attempt 只从 barrier 投影终态，并以 current authority verifier 在崩溃后幂等补投影。在该接线完成前，不能把两个 ledger 描述成物理原子或宣称 terminal eligibility 已 `INTEGRATED`。

该 checkpoint 仍是 `COMPONENT`，不关闭上表 finding：RB1 没有修改 composition root、真实 Darwin launch/process control、Local/Sandbox bridge 或 execution recovery。关闭实现仍需要后续 RB2/RB3 把本 authority API 接入 Core-owned fixed-binary launch、真实 process observation、Provider terminal receipt 和 cleanup-before-unlock/successor 全链，并在最终固定 `marshal` 产物上执行负向矩阵。RB1 author 本地只做 compile-only、`vet`/`staticcheck`/结构与 secret 门禁，不把未执行的新 Mach-O 测试产物描述为运行证据。

## v1.0 生产可达性重置（2026-08-27）

本轮综合审计不再以 package、PR 或 historical milestone 数量衡量完成度，而是从真实 composition root 反查生产路径。当前真实写任务主链仍主要是 `cmd/marshal → internal/cli → execution.Run → Adapter.Run`；`spine`、`agentruntime`、`runtimeprofile`、`bindingcheck`、`revokedrain` 与 `resultingress` 等资产分别具有类型、纯核心或测试证据，但尚未共同承载一条真实 Agent-in-Sandbox Run。`spine` 的示例执行仍依赖 FakeAgent，部分 outbox/write-gate/authority 仍为内存形态。

因此，历史 M8/M9 `PASSED` 继续证明当时定义的 gate、PR 与 CI 已通过，但不能推导出 v1.0 production integration。把 R1/R2 或 R3-A/B/C component checkpoint 标为 `DONE` 会导致后续在错误基线上继续横向扩展，属于状态口径走偏。

| Finding | 等级 | 状态 | 处置 |
| --- | --- | --- | --- |
| `V1-PRODUCTION-REACHABILITY` | P0 | `OPEN-IMPLEMENTATION` | 只有真实 `marshal`/loopback server → durable journal → WorkerExecutor → Sandbox → AgentRuntime → ResultIngress → Verification/Outcome 全链通过，R1–R3 才能逐阶段关闭。 |
| `V1-PARALLEL-AUTHORITY` | P1 | `OPEN-IMPLEMENTATION` | v1.0 复用现有 journal/lease/current ledger；内存 Run/lease/outbox 只能是可重建 projection，禁止成为第二真值。 |
| `V1-CUTOVER-NONDETERMINISM` | P1 | `CLOSED-CONTRACT` | ADR 0052 部分取代 ADR 0045 §1 第 1 项：Fake 比较 exact digest，真实 Agent 比较 authority invariants，内容仍逐次独立验证。 |
| `V1-SCOPE-UNBOUNDED` | P1 | `CLOSED-CONTRACT` | ADR 0052 把 Cloudflare 完整生产拓扑、HA、多用户、完整 Provider/SDK 矩阵与 Goal DAG 延期到 1.x。 |

该 2026-08-27 重置仅作历史纠偏记录。当时权威状态为：R0 `PASSED/DESIGN`；R1 `IN_PROGRESS/INTEGRATED`；R2–R5 `IN_PROGRESS/COMPONENT`；R6 `PLANNED/DESIGN`。当前 R6 已随 RC1 发布进入 `IN_PROGRESS/COMPONENT`；M10–M13 仍作为 1.x 候选池，不阻塞 stable v1.0。

本修订不降低任何 universal 不变量；Local ordinary-user 的 Core-held process observation 只支持 trusted single-user v1 profile，不能关闭 cloud/hardened 的 location attestation finding。生产 cutover、故障 conformance、签名/notarization 与 release identity 仍必须在 R5/R6 完成。

## 终态职责图复杂度审计（2026-08-27）

本轮复核确认：Kernel、authority ledger、Agent/Sandbox Provider、ResultIngress、独立 Verification、Decision、Effect reconcile 与 Artifact Store 分别对应不同故障或权威边界，作为长期**逻辑职责地图**没有过度设计；风险来自把每个逻辑方框直接实现为独立服务、协议或状态库。

| Finding | 等级 | 状态 | 处置 |
| --- | --- | --- | --- |
| `V1-LOGICAL-PHYSICAL-CONFLATION` | P1 | `CLOSED-DOCS` | [当时整体架构](architecture-reference-2026-09-07.md#逻辑职责不等于物理服务)已明确当时 v1.0 采用单 Control Plane 进程、唯一 file-backed authority ledger、本地内容寻址对象存储和多个有界 Worker/Verifier runtime；职责默认进程内模块化，只有独立 trust boundary、durable lifecycle 或已测量的扩缩容/故障隔离需要才能拆服务。 |
| `V1-PREMATURE-PLATFORM-GENERALIZATION` | P1 | `CLOSED-DOCS` | [当时实施计划](implementation-plan-reference-2026-09-07.md#v10-复杂度预算)禁止在当时 R1–R6 主线新建通用 `WorkflowTemplate` DSL、Goal DAG runtime、跨节点 scheduler、独立 GC service、第二 queue 或第二状态库；新增 seam 必须在同一切片接入真实 composition root。当前排期不据此拒绝0085的有限团队与单库替换。 |

该关闭只表示实现与部署口径已经明确，不升级任何 Milestone 或能力成熟度，也不表示 Goal、WorkflowTemplate、远程 Artifact/Knowledge Store 或 GC 已实现。此次修订不改变 trust boundary、持久化语义、生命周期或发布权限，因此不新增 ADR；未来若拆分引入新的权威写路径、持久对象或跨域授权，仍必须先新增或替代 ADR。

## Composition root 纠偏审计（2026-08-27）

本轮审计从真实 composition root 反查生产路径，发现 CLI 此前构造两个独立 `EmbeddedSandboxRuntime` 实例，导致 lease 和 agent registry 互不可见，admission 必然失败。已修复为单实例并补充 fail-closed 门禁。

| Finding | 等级 | 状态 | 处置 |
| --- | --- | --- | --- |
| `V1-COMPOSITION-ROOT-SPLIT` | P0 | `CLOSED-FIX` | CLI 只构造一个 `EmbeddedSandboxRuntime`——同一实例同时承担 DispatchBinder + SandboxProvider + Authority + ResultIngressStore（`33bad5c`）。 |
| `V1-AGENT-REGISTRY-EMPTY` | P0 | `CLOSED-FIX` | Adapter probe 后注册 agent 到 `sharedRuntime.agentRegistry`（`33bad5c`）。此前 agentRegistry 在生产路径始终为空，`AgentRegistrationActive` 总是返回 false。 |
| `V1-FABRICATED-LEASE-EXPIRY` | P0 | `CLOSED-FIX` | 删除 execchain.go 的 `now+24h` lease fallback——lease 缺失直接 fail closed（`33bad5c`）。此前虚构的 expiry 被冻结进 AttemptBinding 文件，污染耐久记录。 |
| `V1-ALLOCATION-RECORD-SILENT-FAIL` | P1 | `CLOSED-FIX` | Allocation record 写入失败从降级改为 fail closed——阻止 Exec（`33bad5c`）。 |
| `V1-LEGACY-ADAPTER-SILENT-FALLBACK` | P1 | `CLOSED-FIX` | RunWorker 遇到非 LaunchCapable adapter 必须 fail closed——production profile 不允许静默退回宿主 legacy Run（`33bad5c`）。 |
| `V1-CANARY-FAIL-AS-SUCCESS` | P1 | `CLOSED-FIX` | 严格 E2E 测试 `TestRealPiStrictE2E` 要求 `worker.completed`——`worker.failed` 直接 t.Fatal（`86e209a`）。canary 更新为提示运行严格 E2E。 |
| `V1-SERVER-RESTART-404-ONLY` | P1 | `CLOSED-FIX` | marshal-server restart 测试重写：创建真实非终态 Run（`run-restart-real`），验证跨进程恢复返回 200+Ready（`da8cccd`）。此前只断言 404。 |
| `V1-FENCING-DOUBLE-WRITE` | P1 | `CLOSED-FIX` | exec-chain 在 embedded 模式下（`MARSHAL_EMBEDDED_SANDBOX=1`）复用 BindDispatch 已创建的 lease（含 fencingToken/AllocationId/Generation）而非独立计算 `fencingDigestOf` 做二次 Provision；修复后 embedded canary（`TestRealPiExecChainCanary`）首次跑通：pi 真实在 Local allocation 内执行（transcript 27KB，exitCode=0）（`634937b`）。 |
| `V1-AGENT-SANDBOX-DIGEST-CONFLATED` | P1 | `CLOSED-FIX` | Facts 新增 `SandboxCapabilityDigest` 字段——agent digest 与 sandbox digest 分离（`686ee61`）。此前两者混用同一字段 `Facts.CapabilityDigest`。 |
| `V1-ATTEMPT-BINDING-MISSING-EMBEDDED` | P1 | `CLOSED-FIX` | AttemptBinding 缺失已关闭；现行前置 Pi canary 绑定 `sourceHead=d4b9647`，单 Attempt/9 Gate 到 `REVIEW_PENDING`。该证据仍不构成 R2/R5 终态，因为 ADR 0056 实现、最终主线重跑和独立 Decision/`ACCEPTED` 尚未闭环。 |
| `V1-EMBEDDED-ADMISSION-REJECTED` | P1 | `SUPERSEDED` | 原以「任意-active fallback（`cad8773`）+ 消费端补 `registration:` 前缀（`3f8638d`）」修复 embedded admission 拒绝——该两处均为门禁降级，已被第二轮审计定性并移除，改由 `V1-AGENT-REG-ANY-ACTIVE-FALLBACK` 与 `V1-SANDBOX-REG-CONSUMER-PREFIX` 的根因修复取代。 |
| `V1-LEASE-NOT-DURABLE` | P1 | `SUPERSEDED-BY-ADR0056` | DispatchLease ledger 已耐久化，原“lease 全为内存态”描述已过时；真实缺口是 Local allocation/process projection、立即 eligibility terminal、独立 `cleanup-completed` 与 cleanup binding release 未进入同一 authority transaction。由本报告顶部 ADR0056 findings 跟踪。 |
| `V1-RESULTINGRESS-NOT-DURABLE` | P1 | `CLOSED-FIX/ADR0056-INTEGRATION-PENDING` | `main@912f659` 已关闭 ResultIngress admission→worker-result→Run journal 的 crash-atomic 持久化/恢复缺口。ADR 0056 terminalization barrier 仍须复用同一 authority transaction，不能另建 check-then-act 路径；在该接线和最终主线 canary 前不升级 R2。 |

## 第二轮生产权威审计（2026-08-28）：门禁降级纠偏

第二轮维护者审计发现：最新提交为跑绿 E2E 引入了两处权威校验降级，并提前升级里程碑。均属「把测试跑通误当成生产权威闭环」的偏航，现逐项纠正。

| Finding | 等级 | 状态 | 处置 |
| --- | --- | --- | --- |
| `V1-AGENT-REG-ANY-ACTIVE-FALLBACK` | P1 | `CLOSED-FIX` | `AgentRegistrationActive` 曾降级为「精确查找失败则任意 active registration 即通过」——门禁绕过。已删除该 fallback 及配套 `LookupByProviderName`/`List`；根因改为稳定能力身份：`StableCapabilityDigest` 排除 `probedAt` 等易变诊断字段，dispatch 时冻结精确 `AgentRegistrationID` 进 AttemptBinding，ingress 只对其 exact lookup（`b7509c8`）。 |
| `V1-SANDBOX-REG-CONSUMER-PREFIX` | P1 | `CLOSED-FIX` | sandbox registrationId 曾只在消费端临时补 `registration:` 前缀，接纳不验证 binding 与真实 ledger 精确相等。已从源头统一 canonical ID（`embeddedRegistrationID` 带前缀），删除消费端 hack，并在 `AdmitWithDurableAuthority` 机械断言 `AttemptBinding.SandboxProviderRegistrationID == 当前 ledger ProviderRegistrationID`，不等即 fail closed（`0ae6640`）。 |
| `V1-STRICT-E2E-FALSE-POSITIVE` | P1 | `CLOSED-FIX/TERMINAL-PENDING` | 前置 fixed-bin canary 已绑定 `sourceHead=d4b9647`，单 Attempt 通过 9 项 Gate，到达 ReviewPacket/`REVIEW_PENDING`。最终主线尚未重跑，也未导入独立 ReviewDecision 到 `ACCEPTED`，因此不能关闭 R5。 |
| `V1-R5-INTEGRATED-PREMATURE` | P1 | `CLOSED-DOCS` | R5 曾被标 `INTEGRATED` 但同时承认 cutover 未开始；且 `MARSHAL_WORKER_EXECUTOR=legacy` 仍在、production gate 需额外环境变量、默认非 embedded 走 seed admission。已撤回为 `COMPONENT`（`82e0c9f`）。 |
| `V1-DOCS-STATE-CONFLICT` | P1 | `CLOSED-DOCS` | AGENTS/Roadmap/Readiness/Implementation Plan 的 R2–R6 状态曾互相冲突。2026-08-28 当时统一为 R0 PASSED、R1 IN_PROGRESS/INTEGRATED、R2–R5 IN_PROGRESS/COMPONENT、R6 PLANNED/DESIGN；2026-09-01 RC1 发布后又统一更新为 R6 IN_PROGRESS/COMPONENT。 |

纠正后真实进展以本报告顶部 2026-08-28 checkpoint 为准：ResultIngress crash-atomic transaction 已随 `main@912f659` 合入，durable server run controller 已随 `main@44ee8c9` 合入，production selector 已随 `main@d4b9647` 收紧，Pi canary 在 `sourceHead=d4b9647` 以单 Attempt/9 Gate 到达 `REVIEW_PENDING`；但 `main@ecee8d4` 只接受 ADR 0056 合同，实现、cleanup 矩阵和独立 Decision/`ACCEPTED` canary 尚未闭环，因此 R2–R5 继续为 `COMPONENT`。

## 行业协议收敛跟踪（2026-08-21 基线）

外部背景（公开行业资料转述，未做在线核验）：agent 相关协议正沿三条轴在 Linux Foundation 轨道收敛——MCP（agent→工具/数据轴）进入 AAIF 并成为事实标准；A2A（agent↔agent 轴）由 Google 捐赠至 Linux Foundation 并获 100+ 背书；ACP（客户端↔agent 轴，LSP 式协议）已被 Gemini CLI、Neovim、JetBrains 等客户端采用。行业判断是自研私有 agent 协议的兼容性税持续上升。

对 Marshal 的分层结论：

- Public API 与 Provider remote transport 是控制面契约，不是 agent 协议；versioned HTTP/JSON + OpenAPI + SSE 的自持契约不在收敛压力范围内，保持现状；
- 三条标准协议与 Marshal 正交：MCP 属 agent 工具层（Worker 自行使用，Marshal 经工具策略治理，不实现 MCP server）；ACP 属客户端↔agent 轴（正确形态是 ACP facade 作为 Public API client，或作为某一 AgentAdapter 的 transport）；A2A 属 agent↔agent 委托轴（正确形态是外部 gateway 作为 Public API client；Core 内多 Agent 协作仍按 ADR 0019 禁止 P2P 第二权威）；
- 既有立场（ACP 只可作为 AgentAdapter transport、A2A 只作为未来外部 gateway 候选、MCP 属延后阶段、三者不阻塞核心路线，见[实施计划](implementation-plan.md)）仍然成立；缺口不在方向，而在“何时必须接”此前缺决策记录。

跟踪机制：每季度复核一次（下一次 2026-11），更新三轴协议采用状态并检查触发条件；满足任一触发条件时先新增或替代 ADR 再实施，不在触发前抢先实现协议面：

1. ACP：客户端生态覆盖 JetBrains、Zed 与 Neovim/Gemini CLI 中至少两者，且出现真实用户请求从这些客户端驱动 Marshal 任务——实现 ACP facade 作为 Public API client，不引入任何 Core 改动；
2. A2A：出现真实外部委托场景（外部 agent 系统向 Marshal 提交任务或消费 Outcome）——评估 A2A gateway 作为 Public API client，Core 语义不变；
3. MCP：既有工具策略（tool allowlist 等）无法表达的 Worker 工具面治理需求，或 Data/Capability 域材料授予需要标准化互操作——评估 MCP 形态，仍不得承载 raw credential（ADR 0018 §3 边界不变）。

本节是跟踪记录与触发条件登记，不是架构变更：不改变任何 ADR、Milestone 状态或信任边界；接入边界的集中重申见[Runtime 架构](runtime-architecture.md)“部署形态与 Public API”节。

## Issue #130 lease owner 与 orphan recovery 审计增补（2026-08-21）

审计确认当前 owner record 存在真实 legacy 5-field/7-field shape，PID/heartbeat/事件年龄诊断与 OS descriptor lock authority 的边界需要收敛；owner acquisition epoch 与 Attempt generation 尚缺独立的 successor/high-water 合同，`SupervisorDispatcher` orphan recovery 也需要把 late `WorkerResult`、quarantine、预算裁决和 `Outcome` 绑定到同一个 crash-safe append-only transaction。

[ADR 0035](adr/0035-supervisor-lease-owner-v2-and-orphan-recovery.md)（Proposed）提出 `LeaseOwnerRecordV2` exact closed schema、同一 descriptor authority 下的 legacy v1 fail-closed migration、epoch+digest high-water rollback/ABA fencing，以及 `prepare → fence-consumed → inspect/reconcile → resolved` 的 append-only durable transaction。所有 unknown 统一进入 intervention、zero side effect，Core 只追加 typed `Outcome`，不得静默写 `BLOCKED`。

该 finding 保持 `OPEN`：ADR 尚未接受，owner v2、migration、共享 eligibility predicate、exact-run dispatcher 与 orphan transaction 均未实现或验证。此记录不表示 Issue #130 完成，不改变 M10–M13 状态，也不授权生产启用自动 orphan recovery。
- 范围：当前文档与 `v1alpha1` Schema 描述的 Local CLI MVP（Runtime/Sandbox 契约部分为分层状态，见下）
- 结论（分层）：
  - Local MVP（Milestone 0–6）：**`APPROVED_FOR_IMPLEMENTATION`** / `USABLE`，行为与门禁不变；
  - Runtime/Sandbox 契约（M7–M13）：在 [ADR 0017](adr/0017-provider-neutral-sandbox-contract.md) 接受前为 **`BLOCKED`**（首次 Sandbox SPI dogfood reject 与 Round 2 评审暴露的合同级歧义）；2026-08-10 全部 P1 经 Round 2 独立验证与 ReviewDecision accept，维护者接受 ADR 0017，设计歧义关闭；2026-08-11 维护者接受 [ADR 0018](adr/0018-control-plane-and-provider-ports.md)，冻结 Marshal C/S Control Plane、按信任域分隔的 Provider Port、耐久注册/能力快照与在途 lease 撤销，澄清/部分取代 ADR 0017 §4/§6/§7/§8/§10/§12 并显式取代 ADR 0016 §6 经 ADR 0017 承接的 universal 接纳口径，并随 Round 4 独立评审八项 P1（远程 transport 基线、securityDomainId 键空间、attestation 全链绑定、原子 fencing sink、SSE 恢复与再授权、engine 单一 seam、Port protocol family、legacy snapshot 残留）全部关闭增补 §10–§16，随 Round 6 复核两项残留（Control Plane authority namespace 与 Provider actor 域分离、typed cross-domain edge）全部关闭修订 §3/§10，随 Round 7 复核三项残留（双键空间残留清除、Core-only typed edge 生命周期细化、Public API 幂等/SSE/对象 key 修正为 authorityNamespaceId）全部关闭修订 §3/§4/§5/§7/§10/§13，随 Round 8 复核一项残留（typed edge 跨域例外与适用范围）全部关闭修订 §3/§10，随 Round 9 复核两项残留（ADR0018-UNQUALIFIED-CROSS-DOMAIN-RESIDUE、ADR0018-NONEDGE-PORT-AND-SAME-DOMAIN-AUTHZ）全部关闭修订 §2/§3/§5/§7/§10（接受只冻结设计，不升级 M8–M13 实现/conformance 状态）。
- 未关闭 Blocking Finding：4 项 P1。`ISSUE53-CI-REWORK-EVIDENCE-P1` 仍为 `OPEN`；ADR 0032 B2 独立复核另重开 `ADR0032-B2-AUTHORITY-ROLLBACK-DOMAIN`、`ADR0032-B2-DELIVERY-CRASH-WINDOW`、`ADR0032-B2-RECOVERY-RESIGN` 三项 P1。后三项的目标契约由 [ADR 0033](adr/0033-journal-bound-merge-authority-and-delivery.md)（Proposed）给出；local/non-production 受限 profile 的关闭要求 ADR 接受、A–D implementation successor 全部合入、独立验证 P0/P1 清零及 required CI/secret scan/recovery conformance 全绿；production supported 的关闭还必须等待 M11 external rollback witness、跨节点 fence 与协调回滚演练。它们不改变 Local MVP `APPROVED_FOR_IMPLEMENTATION` / `USABLE`，但在关闭前禁止把 `mergePolicy=policy` 登记为无限定 supported。
- 门禁状态（分层）：
  - 维护者已接受 ADR 0001–0011、ADR 0012–0014 与 ADR 0016（2026-08-10，含 M7–M12 路线）及 Local MVP Scope；
  - ADR 0017（provider-neutral Sandbox 安全契约）已接受（2026-08-10）；**接受只关闭设计歧义**：不得把 M8 实现或 conformance 状态提前标为完成，M7 保持 `IN_PROGRESS`、M8–M13 保持 `PLANNED`，首次 Sandbox SPI dogfood 的既有实现成果按未接纳探索证据对待（见 [Roadmap 状态](roadmap-status.md)）；
  - ADR 0018（Marshal C/S Control Plane、按信任域分隔的 Provider Port、耐久注册/能力快照与在途 lease 撤销）已接受（2026-08-11）；**接受只冻结设计**：不升级 M8–M13 实现或 conformance 状态，M7 保持 `IN_PROGRESS`、M8–M13 保持 `PLANNED`；ADR 0017 的历史 universal 口径（统一 lease 身份/统一 fencing/统一 providerType/legacy CapabilitySnapshot 注册产物）在现行规范入口就地标注已被 ADR 0018 取代；Round 4 独立评审八项 P1 已随 ADR 0018 §10–§16 关闭，Round 5 复核四项残留（复合安全域、Port protocol family、Push/Pull 不变量等价、计划升级 bounded drain）已随 ADR 0018 §2/§6/§7/§10/§16 修订关闭，Round 6 复核两项残留（Control Plane authority namespace 与 Provider actor 域分离、typed cross-domain edge）已随 ADR 0018 §3/§10 修订关闭，Round 7 复核三项残留（双键空间残留清除、Core-only typed edge 生命周期细化、Public API 幂等/SSE/对象 key 修正为 authorityNamespaceId）已随 ADR 0018 §3/§4/§5/§7/§10/§13 修订关闭，Round 8 复核一项残留（typed edge 跨域例外与适用范围）已随 ADR 0018 §3/§10 修订关闭，Round 9 复核两项残留（跨域 fail closed 表述精确化、非 edge Port 与同域不自动授权）已随 ADR 0018 §2/§3/§5/§7/§10 修订关闭，远程 transport 安全基线自各远程能力首次 enable 起生效（M11 不补基线）

## 执行结论

该设计作为本地 CLI-first Coding Agent Harness，内部一致且可以实施。CLI-first 描述 Harness 接口，不限制主 Agent 必须使用 Codex CLI；Codex Desktop 与手机端 Remote 通过相同契约接入。最重要的信任边界已明确：

- Worker 负责实现，但不能自证；
- 每个独立 Worktree 只有一个写入者；
- 确定性观察先于语义 Review；
- Decision 绑定精确 Evidence；
- Publisher 权限与凭据和 Worker 分离；
- 失败与 No-change Run 产生 Outcome Evidence，不制造虚假 PR；
- 普通宿主机执行不会被宣传成恶意代码隔离。

当前可以按[实施计划](implementation-plan.md)推进。该批准只适用于文档定义的 Local MVP，不适用于 Multi-user Service、Hostile-code Execution、Automatic Merge 或延后的 Hardened Profile。

## 审查范围

- Vision、Goal、Non-goal、Trust Boundary 与 Success Metric；
- Component Architecture、Identity、Persistence、Locking 与 Idempotency；
- 仓库本地 `.marshal/` 状态隔离、默认 Git 排除与长期归档边界；
- 全部 Lifecycle State 与 Transition Guard；
- TaskSpec、Worker、Evidence、Review、Policy、Event 与 State Contract；
- Qwen Code、OpenCode 与 Pi Adapter Boundary；
- Independent Verification 与 Lead-agent Review；
- Codex CLI、Codex Desktop 与手机端 Remote 的主 Agent 接入边界；
- Artifact、Commit、PR/MR Publication 与 CI Binding；
- Security Threat、Assurance Profile 与 Credential Separation；
- Interruption、Crash Consistency、Reconciliation 与 Cleanup；
- Implementation Milestone 与 Exit Criteria；
- ADR 0001–0011。

## 自动检查

| 检查 | 结果 |
| --- | --- |
| JSON Parsing | 12 份 Schema 与 12 份 Happy-path Record 通过 |
| Draft 2020-12 Metaschema | 12 份 Schema 通过内置 Draft 2020-12 编译器 |
| Happy-path Validation | 12 份 Record 全部通过对应 Schema |
| Local `$ref` 与 Regex | 104 项 `$ref` 与 53 项 Regex 通过 |
| Lifecycle/Schema State Alignment | 16 个 State 全部一致 |
| Markdown Local Link | 通过 |
| `.marshal/` Git Ignore | 根规则命中 |
| Nested linked worktree Probe | macOS Git `2.50.1` 可在 `.marshal/worktrees/` 创建并识别独立 worktree |
| 全文件 Whitespace/Conflict Marker | 通过 |

Schema 只承担结构校验。[`schemas/README.md`](https://github.com/chiga0/marshal-harness/blob/main/schemas/README.md) 中列出的 Semantic Validator 是 Milestone 0 强制要求。

## 审计中已关闭的问题

| ID | 级别 | 问题 | 修复 |
| --- | --- | --- | --- |
| A-001 | P1 | ArtifactManifest 无法表达发布前 `expected` 或失败后 `missing` | 增加无需伪造 Path/URI 的显式 Variant |
| A-002 | P1 | 未 Commit Worktree 被强制要求 Git Tree SHA | 改为 Canonical `snapshotDigest`，`gitTreeSha` 可选 |
| A-003 | P1 | Durable WorkerRequest 与 ReviewPacket 缺少 Schema | 增加 Schema、Example 与 `reviewPacketDigest` 绑定 |
| A-004 | P2 | Worker Artifact 可同时声明 Local Path 与 External URI | 改为 Exclusive `oneOf` |
| A-005 | P2 | Policy 可要求网络强制但不记录有效模式 | PolicySnapshot 中 `networkPolicy` 改为 Required |
| A-006 | P2 | README 生命周期漏掉 Operational Retry | 增加 `RETRY_PENDING` 并对齐 State Schema |
| A-007 | P1 | 全局状态目录示意无法直观看出不同业务仓库与任务 worktree 的归属 | 改为每仓库独立、默认忽略的 `.marshal/`，每个任务仍使用真实 linked worktree |
| A-008 | P2 | CLI-first 容易被误解为主 Agent 只能使用 Codex CLI | 增加与界面无关的 `LeadAgentBridge`，明确支持 Codex Desktop 与手机端 Remote |
| A-009 | P2 | TypeScript/Node 默认选型与本地进程编排、单二进制分发目标不完全匹配 | 新增 ADR 0005，选择 Go 并保留语言无关契约 |
| A-010 | P2 | macOS 的 `/var` 与 `/private/var` 别名会让有效 worktree 的字符串路径比较失败 | Repository/Worktree Identity 必须使用规范化真实路径，并加入平台 Fixture |
| A-011 | P1 | Worker 控制文件放入 Worktree 会污染业务 Diff，开放整个 Run Store 又会破坏冻结证据 | 新增 ADR 0006，使用 Attempt-scoped `controlRoot/input|output` |
| A-012 | P1 | Worker 可破坏 linked worktree 的 `.git` 身份，使嵌套目录向上误认主仓库 | `Open` 解析真实 `--show-toplevel`，Worker 后再次验证 Root/CommonDir，失败进入 `BLOCKED` |
| A-013 | P2 | 把 cmux 直接写入 Worker 执行路径会耦合 Provider、平台与 UI，并可能削弱进程和证据控制 | 新增 ADR 0008：独立 Observer Port，cmux 仅作为首个只读可视化 Backend，失败降级到 `captured` |
| A-014 | P1 | 直接在 cmux 启动默认 Agent TUI 会继承 ambient environment、绕过 Adapter 工具/子 Agent预算，并且没有可靠完成边界 | 新增 ADR 0011：Adapter 冻结 TUI launch，使用一次性密封启动信封；缺少可信 CompletionGate 时只允许受监督 PTY |

上述原始审计范围内没有未解决的 P0、P1 或 P2 架构问题（后续增补 finding 以下文各增补节为准，当前唯一未关闭项为 Issue #53 审计增补节的 `ISSUE53-CI-REWORK-EVIDENCE-P1`）。

Milestone 3 的实现审计曾发现 Verdict E2E 覆盖与 `.pending` 崩溃残留两个缺口，均已在复审前关闭；独立复审结论为 `APPROVE`，GitHub Actions run `30874552479` 的 Linux、macOS 与 Secret Scan 全部通过。详情见 [Milestone 3 OpenCode 独立审查](reviews/milestone-3-opencode-review.md)。Milestone 4 的真实 OpenCode Adapter 也已完成独立审计与远端 CI 验收，详见 [Milestone 4 独立审查](reviews/milestone-4-opencode-review.md)。

Milestone 5 的独立审计首轮阻止了 CI 返工发布死锁、`skipping` 误通过与 Record 崩溃覆盖；最终复审无 P0/P1，主 Agent 又关闭了 Journal/Snapshot Publication 重放、Remote `insteadOf` 重定向、首次受控 Commit 竞态和标题上限问题。主分支 CI `30889069165` 与真实 [Draft PR #1](https://github.com/chiga0/marshal-harness/pull/1) 的 CI `30889190854` 全绿，Publisher 两次复用同一 PR 且未 merge。详见 [Milestone 5 独立审计](reviews/milestone-5-opencode-review.md)。

## 红队场景

| 场景 | 控制措施 | 结果 |
| --- | --- | --- |
| Worker 未运行测试却声称通过 | 字段使用 `declared*`，Marshal 重跑 Required Command | 已覆盖 |
| Worker 修改 Forbidden Path 或隐藏 Untracked File | 独立观察 Path、Rename、Mode 与 Untracked File | 已覆盖 |
| Codex Accept 后 Worktree 被修改 | Publisher 重算 Snapshot/Evidence，拒绝 Stale Decision | 已覆盖 |
| Worker 尝试 Push 或开 PR | Publisher 分权；强凭据隔离要求 Hardened Profile | 已覆盖，并清楚限制 Local Profile |
| Repository Instruction 扩大 Scope | Frozen TaskSpec/Policy 优先并记录 Instruction Digest | 已覆盖 |
| Worker/Verifier 崩溃留下部分变更 | 终止 Process Tree、刷新 Journal、保存 Worktree、新建 Attempt | 已覆盖 |
| Push 或 PR 创建成功后进程崩溃 | 重试前 Reconcile Remote Branch 与 Task Marker | 已覆盖 |
| Required CI 来自旧 Commit | CI Evidence 绑定 Published Head SHA | 已覆盖 |
| Empty Diff 被报告为成功 | 除非允许并 Review No-change，否则 Gate 失败且不开虚假 PR | 已覆盖 |
| Review 后 Base 前进 | 禁止 Silent Rebase，由 Policy 选择 Publish、Block 或 New Run | 已覆盖 |
| Symlink 或 `..` 逃逸 | Canonical Path 与 Collection 默认失败 | 已覆盖 |
| 两个 Qwen 入口版本不同 | Exact Executable 与 CapabilitySnapshot，禁止隐式 Fallback | 已覆盖 |
| 主 Agent 返回 Prose 或 Stale Decision | 保持 `REVIEW_PENDING` | 已覆盖 |
| Git Hook 修改已接受代码 | Publisher 禁用 Ambient Hook 并验证 Commit Tree | 已覆盖 |
| Test/Build Script 恶意 | 只有 Hardened Profile 可以声称隔离 | 正确排除在 Local MVP 外 |

## 剩余限制，但不构成 Blocking Finding

### Local Profile 不是 Hostile-code Containment

`workspace-write` 提供 Worktree Isolation、Filtered Environment 与 Workflow Gate。普通 Host Process 仍可能访问 Home、Credential Helper、本地服务或网络。文档已明确该限制，并要求不可信代码使用 Hardened Profile。

### 真实 Adapter 必须 Probe

CLI Flag 与 Event Shape 会随版本变化。Implementation 不得把 Environment Baseline 当作兼容性承诺。Milestone 4 要求 Exact Executable Probe 与共享 Conformance Test。

### 初始仅支持 GitHub Publication

本机 GitHub CLI 已认证，可以进行 Publisher Spike；GitLab CLI 未安装，GitLab 延后。这不影响 Provider-neutral Boundary。

### Semantic Rule 需要代码实现

Cross-record Freshness、ID Uniqueness、Budget Relationship、Path Canonicalization、Accept/No-change Guard 与 Publication Consistency 不能全部由 JSON Schema 安全表达，已列为强制 Semantic Validator 与 Exit Criteria。

## 实施阶段关闭的问题

| ID | 级别 | 发现方式 | 问题 | 关闭 |
| --- | --- | --- | --- | --- |
| I-001 | P1 | 2026-08-05 真实 Full MVP E2E（Run `m6-mvp-e2e-20260805` / `m6-mvp-e2e-r2-20260805`） | ADR 0010 引入的 balanced publish Approval Gate 与发布重校验、Outcome、Rework 读取仍使用 legacy `review-decision.json`，而 Review 事务持久化为 `decisions/decision-%03d.json`，导致发布审批与发布恒失败 | 两个独立 Marshal Run（`m6-approval-fix-r3-20260805`、`m6-decision-paths-20260805`，均 `ACCEPTED`）修复为轮次绑定读取，语义校验不变；提交 `4538f9f`、`9589b25`；修复后真实 E2E Run `m6-mvp-e2e-r3-20260805` 全链路 `ACCEPTED` |

两次失败均以 `BLOCKED` fail-closed，未产生远端副作用；信任边界、持久化契约与发布权限未被改变，因此不新增 ADR，仅记录关闭证据。

| I-002 | P2 | 2026-08-07 hardening 批次合入 | 维护者以 worktree 拷贝合入 dx 任务时，其 cli.go 基线早于 abort 合入，覆盖了 `task abort` 实现，cli 测试失败 | 手工合回 abort dispatch 与辅助函数，骨架测试更新；提交 `f8d4e74`；教训：跨基线 worktree 合入必须先比对基线差异（已记入维护者流程） |
| I-003 | P1 | 2026-08-08 实现批合入 | 同类事故二次：ENV-DX 合入以旧基线 worktree 拷贝覆盖 opencode/pi/qwen 非测试文件，丢失 ADR 0013 分级接线，main 处于“测试在、实现缺”且零容忍回潮，三个后续 Run 因此 BLOCKED | 提交 `8d37e5d` 恢复接线；强制流程升级：跨基线合入后必须跑受影响全包测试（不仅是目标包）；该事故同时证明分级引擎缺失时失败模式与 I-002 同构 |

**hardening 批次关闭记录（2026-08-07）**：六项张力中四项已实现并合入——WorkerResult 归一化（`328ea03`）、prompt 内嵌模板（同）、显式 abort + 终态 Outcome（ADR 0012，`08c8462`）、`--through-verify` 与仓库锁重试（`76fdf40`）；均经独立 Marshal Run 的 Verification 与 Review ACCEPTED。ADR 0013（拒绝分级）与 0014（read-only 画像）保持提案状态待维护者接受。tui-research 22 个死 Run 已用新 abort 转终态并回收 7 个干净 worktree；15 个 dirty worktree 待归档机制（cleanup v1 对 dirty 硬拒绝、无归档授权路径，记为下一缺口）。

## 2026-08-10 架构重置：已关闭的架构问题与新打开的实施风险

维护者于 2026-08-10 接受 [ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md)，把长期目标从“本地单次 CLI 编排”重置为长寿命 Runtime/Control Plane。本节记录该重置关闭的架构问题（含首批文档评审识别的四个 P1 缺口，R-A5–R-A8）与新打开的实施风险；目标架构见 [Runtime 架构](runtime-architecture.md)。

已关闭的架构问题：

| ID | 级别 | 问题 | 关闭 |
| --- | --- | --- | --- |
| R-A1 | P1 | 长期目标停留在“本地单次 CLI 编排”，远程队列与分布式调度笼统延后，长期形态无冻结路线 | ADR 0016 冻结目标与 M7–M12 唯一平台路线，实施计划、愿景、Roadmap 与治理文档口径同步 |
| R-A2 | P1 | ADR 0015 的常驻形态、耐久调度与远程执行边界未闭合，且提案长期悬置 | ADR 0015 标记 Superseded before acceptance，生产部署边界由 ADR 0016 承接 |
| R-A3 | P1 | 执行环境语义混在 Worker 编排内，Provider 不可替换、`hardened` 声明无准入标准 | 冻结 `AgentAdapter`（prepare/decode/capability）与 `SandboxProvider`（Probe/Provision/Stage/Exec/Inspect/Signal/Checkpoint/Restore/Terminate/Reconcile）分层；`hardened` 以 conformance 证明 mount/network/resource/credential 强制为准入条件 |
| R-A4 | P1 | 权威状态与调度边界未定义，存在“双权威”与自研 workflow engine 风险 | 冻结耐久执行引擎 Port：Marshal versioned event/state 为业务权威，外部引擎（生产参考 Temporal）只承担传输保证，Activity 以 `commandId` + `expectedSequence` CAS 追加 Marshal 事件。该 Port 由 ADR 0017 §9 统一更名 `DurableExecutionEngine` 并冻结权威边界（Attempt 创建/retry 预算/rework/终态裁决只在 Core，delivery/activity retry 不创建 Attempt、不消费业务预算） |
| R-A5 | P1 | 提交入口（POST TaskSubmission）未界定网络暴露、调用者认证/授权与 repository/project 作用域；幂等归并可能把不同冻结输入的错误请求合并进既有 Run | ADR 0016、Runtime 架构、安全模型与实施计划冻结提交入口边界：M8/M9 默认仅 loopback/受信任本地边界；远程入口生产可用前必须 TLS、调用者身份、按 repository/project 授权与审计（M11 退出门禁验收）；幂等身份为 `(scope, idempotencyKey, requestDigest)`，同 scope+key+digest 返回既有 submission/run，同 scope+key 不同 digest 冲突 fail closed，不创建或归并错误 Run |
| R-A6 | P1 | 失联旧 Attempt 可能晚到上传 checkpoint/candidate/日志/证据引用，若接纳不校验 generation/fencingToken 会污染新 Attempt 的权威证据 | 所有 Attempt 回报与 Artifact/Checkpoint/Candidate/Evidence 接纳必须携带 attemptId/generation/fencingToken，并在权威写入边界以 expectedSequence/CAS 校验；陈旧 token 内容隔离留存为诊断材料，不进入当前 Evidence/Review/Publication；外部副作用继续经 SideEffectIntent/Receipt + reconcile 幂等；相应故障注入列入 M9 退出门禁 |
| R-A7 | P1 | Cloudflare 段落同时写 fail closed 与回退自托管 Provider，可能被实现为同一 Attempt 内透明降级，甚至从 hardened 降到 workspace-write，与 Run 冻结的最低 SandboxRequirements 冲突 | 统一为：失败的 Allocation/Attempt 先终止并对账；调度器仅可为新 Attempt 选择满足同一冻结 SandboxRequirements 与 assurance 下限的兼容 Provider；无兼容 Provider 时 Run 保持 BLOCKED（fail closed），绝不静默降低 profile 或复用旧 handle；ADR 0016、Runtime 架构、M10 退出门禁与本审计风险措辞已同步 |
| R-A8 | P1 | Project/Goal 被列为必须持久化的核心对象，但 M8–M12 无任何 Milestone 实现它，复杂任务编排被笼统推到 M12 之后，M7–M12 完成后只能运行彼此独立的 Task | 实施计划与 Roadmap 新增 M13 Goal orchestration 阶段（持久 Project/Goal、可审计计划与重规划、跨 Run 记忆/Artifact 引用、预算与终止条件、独立质量评估、人工干预与恢复，含 Goal、非目标、退出门禁与 dogfooding）；M7 只冻结对象语义、M13 实现 Goal 控制器，避免虚假完成声明 |

新打开的实施风险（不构成 Blocking Finding，由各 Milestone 退出门禁关闭）：

| ID | 风险 | 缓解与关闭条件 |
| --- | --- | --- |
| R-001 | 外部耐久引擎依赖与语义锁定 | Port 隔离 + 生命周期一致性测试；替换 Orchestrator 前一致性测试先行（M9） |
| R-002 | Cloudflare Sandbox SDK 处于 1.0 preview/Beta 且托管平台不可自部署 | live opt-in + fail-closed：探测失败或漂移时终止当前 Allocation/Attempt 并对账，新 Attempt 仅可分配满足同一冻结 SandboxRequirements 与 assurance 下限的兼容 Provider；无兼容 Provider 时 Run 保持 BLOCKED，不静默降级、不复用旧 handle；同一 conformance/E2E 通过后才可替换（M10） |
| R-003 | 恢复误判导致双写或丢证据 | DispatchLease generation/fencingToken + 故障注入测试集；kill -9 后 60 秒 Inspect/Reconcile 口径（M9） |
| R-004 | Warm reuse 引入跨任务污染 | 默认每 Attempt 独立 ephemeral sandbox；复用需相同 tenant/repository/trust-domain 且可证明 sanitization（M8 起） |
| R-005 | 事件账本膨胀拖慢恢复 | Continue-As-New 式换代与分段归档设计在 M9/M11 落地并测试 |
| R-006 | 过度承诺（把早期 PoC 宣传成多租户服务） | 文档口径绑定安全就绪等级；多租户保持评估项，威胁模型评审通过后才讨论 |
| R-007 | 多节点写入分离被绕过 | Worker/Verifier/Publisher 独立 workload identity 与写入域；越权 Fixture 必须失败（M11） |
| R-008 | 提交入口越界暴露或缺乏授权（未认证/跨 scope 提交进入调度） | M8/M9 仅绑定 loopback/受信任本地边界；远程入口必须 TLS + 调用者身份 + 按 repository/project 授权 + 审计，M11 退出门禁验收；幂等身份冲突语义防止错误归并（M9） |
| R-009 | Goal 编排自主失控、超预算运行或静默放弃 | 预算与终止条件强制并在触发时保存 Outcome；计划/重规划全部事件化可回放；独立质量评估不得自证；人工可随时干预（M13） |

## 首次 Sandbox SPI dogfood reject 增补（2026-08-10）

M8 的首次 Sandbox SPI 纵切 dogfood Run 以 reject 结束。阻塞证据（已完整内嵌于返工 TaskSpec，不再读取任何 `.marshal` Run/outcome/decision 文件）表明 ADR 0016 冻结的契约仍留有可歧义点，Local 与 Cloudflare 两个可替换实现无法按同一语义收敛。以下问题在该次 reject 中打开，由 [ADR 0017](adr/0017-provider-neutral-sandbox-contract.md) 冻结统一契约；该次 dogfood Run 的既有实现成果按**未接纳探索证据**对待，不计为 M8 实现进度。

已打开的问题（随 ADR 0017 于 2026-08-10 经 Round 2 独立验证与 ReviewDecision accept 后接受而关闭）：

| ID | 级别 | 问题 | 冻结位置（ADR 0017，已接受） |
| --- | --- | --- | --- |
| S-A1 | P1 | 单一 `executionProfile` 把权限与隔离保证级别压在同一维度，无法表达 read-only+hardened 等正交组合 | §1 `AccessMode × AssuranceLevel` 二维正交模型：兼容映射表、拒绝/降级规则、持久记录迁移 |
| S-A2 | P1 | `hardened` 可来自 Provider 自报 Enforcement，无独立签发、可密封、可撤销的证据形态 | §2 `ConformanceEvidence`：身份绑定、suite/probe artifact digest、逐维结果、evidenceDigest、有效期/撤销语义；证据拓扑见 S-B1；Local 永不 hardened，Cloudflare 无豁免 |
| S-A3 | P1 | Stage 允许只回显声明 digest，Provider 不对真实 bytes 重算 sha256，内容寻址名存实亡 | §3 inline（≤1 MiB/对象、≤16 MiB/请求）与 ArtifactStore locator 的 provider-neutral 选择；消费前后重算 sha256；篡改 fixture 必须让回显型实现失败 |
| S-A4 | P1 | 操作身份未完整绑定 task/run/attempt/role/allocation/generation/token，重放可不经过当前 lease fencing；Restore 丢响应对账与普通 replay 未分离 | §4 workloadRole/principal 拆分 + 完整身份元组 + canonical replay key；普通 replay 先过 lease fencing；Restore lost-response reconciliation 独立成路径 |
| S-A5 | P1 | Restore 的 in-place 恢复未定义双写语义，旧进程树可能在恢复后继续写 | §5 默认 replacement allocation：旧进程树终止并失效后以控制面单写者 CAS 激活新 generation；故障注入验收 |
| S-A6 | P1 | M8 常驻单节点纵切与 M9 形态未切分：提交入口、调度租约、Public API 与 Provider 版本化注册的形状未冻结；Provider 观测完成可能被误当成 ReviewDecision/safe-to-publish 宣布 | §6 版本化 Provider Protocol 与认证注册（CapabilitySnapshot 冻结字段、未知版本 fail closed、观测边界）；§7 DispatchLease 唯一状态机；§9 DurableExecutionEngine 权威边界；§10 C/S + embedded 形态与 wire contract；§12 M8 改为 embedded/local 纵切、M9 冻结 marshal-server 与 Public API |
| S-A7 | P2 | 规范化未统一声明复用仓库既有 JCS，遇重复 JSON member 的行为未定义 | §11 统一 RFC 8785 JCS；重复 JSON member 一律拒绝 |

Round 2 独立评审进一步打开并关闭的歧义（全部 P1；随 ADR 0017 接受于 2026-08-10 关闭）：

| ID | 级别 | 问题 | 关闭位置（ADR 0017） |
| --- | --- | --- | --- |
| S-B1 | P1 | 首轮文本要求证据采集 workload 运行在独立于被测 Provider 的 Verifier sandbox——只能测到 Verifier sandbox，测不到被测 Provider 的 mount/network/resource/credential 强制能力，且错误套用了业务成果独立验证拓扑 | §2 证据拓扑冻结：probe 定义、challenge/nonce、probe artifact digest、调度、out-of-band 观察、裁决与 ConformanceEvidence 签发由 Control Plane 与独立 Conformance Verifier 控制；probe workload 作为敌对测试负载运行在被测 Provider 创建、身份精确绑定的 target allocation 内；Provider 的 completed/receipt 只是输入，不能自签通过；M8 fixture 与各文档同义表述已同步修正 |
| S-B2 | P1 | 审计报告顶层 APPROVED 与增补节开放 P1 矛盾，可能让人或自动化提前把 M8+ 视为已获批准 | 本报告改为分层门禁：Local MVP M0–M6 保持 APPROVED/USABLE；Runtime/Sandbox 契约在 ADR 0017 接受前 BLOCKED；接受只关闭设计歧义，M8/conformance 状态不提前，Roadmap 保持 M7 IN_PROGRESS、M8–M13 PLANNED，dogfood 实现按未接纳探索证据对待（见报告头部与“实施门禁”节） |
| S-B3 | P1 | DurableOrchestrator/DurableExecutionEngine/backend retry 三套措辞并存，外部引擎可能被当成第二个 Attempt/retry 权威 | §9 统一 Port 名 DurableExecutionEngine；Temporal/Local Engine 仅是 backend；Core lifecycle policy/controller 独占 Attempt 创建、retry eligibility/预算、rework 与终态裁决；backend 只做相同 commandId 的 at-least-once delivery、timer、signal、crash recovery；delivery/activity retry 不创建 Attempt、不消费业务预算；Runtime/总体架构与实施计划已同步 |
| S-B4 | P1 | 只为 Pull 列出 capability matching、ack、heartbeat、deadline、generation 与 fencing，Push 可能退化为 fire-and-forget | §7 冻结 Push/Pull 共用的唯一 DispatchLease 状态机，只改变连接发起方；两者都绑定认证 registration、CapabilitySnapshot/ConformanceEvidence digest、task/run/attempt/allocation、generation/fencingToken，都具备 ack deadline、heartbeat、expiry、cancel、reconcile、generation bump 与陈旧结果隔离；Push 同样先 capability match，超时/响应丢失不产生第二个 active allocation；M9 增加两拓扑等价 conformance 与故障注入口径 |
| S-B5 | P1 | role 枚举扩成 worker/verifier/publisher/control-plane，冲突 Publisher 独立信任域不变量，把调用主体与 workload 身份混成可扩权枚举 | §4 拆分 workloadRole 与认证 principal：workloadRole 封闭枚举仅 worker/verifier；control-plane/publisher/operator/API caller 是不同语义 Port 上受 AuthZ 约束的 principal；Publisher 永不成为 Sandbox workload；远程请求另绑定 principal/portKind/providerType/audience/scope，Provider 不得借通用 role 取得跨 Port 能力；身份元组、fencing、Secret/Artifact 与安全模型表述已同步 |
| S-B6 | P1 | HTTP/JSON + OpenAPI 推迟到 M12、M9 首版 wire contract 与可恢复事件流未冻结；M9 禁止匿名 Pull 但注册身份来源与 AuthN/AuthZ 分工未定义 | §10/§12：M9 首版 Public API 采用 versioned HTTP/JSON + OpenAPI（Task create/get/cancel、Run approval/status、events/evidence），事件基线 SSE 支持 eventId/cursor 断线续传 + 轮询 fallback，WebSocket/gRPC 推迟；Provider remote transport 同样 versioned HTTP/JSON（Push 由 server 调 Provider endpoint，Pull runner 以 outbound-only long polling/streaming 领取同一 DispatchLease）；M9 提供最小 scope-bound 可撤销注册身份（入口可仅 loopback/trusted boundary），M11 扩展生产远程入口与 operator/API caller/多节点多用户 AuthN/AuthZ；M12 交付多语言 SDK、部署文档与多拓扑 conformance（不是首次定义 wire contract）；embedded CLI 经 in-process adapter 调同一 Public application Port，不直写 store |

后续实现的可执行小步（M8 内按序推进，每步先有 fixture 与失败判据）：

| 步骤 | 交付 | 完成判据 |
| --- | --- | --- |
| 1 | 二维要求 Schema 与映射校验器 | 旧 `executionProfile` 三取值确定性映射；AccessMode 升级与静默降级 fixture 全部失败 |
| 2 | ConformanceEvidence 签发链与校验 | 按 §2 证据拓扑执行：probe 定义/challenge/nonce、probe artifact digest、调度、out-of-band 观察、裁决与签发由 Control Plane 与独立 Conformance Verifier 控制；probe workload 作为敌对测试负载运行在被测 Provider 创建、身份精确绑定的 target allocation 内；被测 Provider 的 completed/receipt 只作输入，自签通过的 fixture 必须失败；逐维结果、有效期与撤销 fixture 全部按语义判定 |
| 3 | 内容寻址 Stage（inline/locator、大小上限、消费前后重算） | 篡改 bytes fixture 拒绝回显型实现；inline 超限被拒；digest 不一致上报 `StageInputMismatch` 并 fail closed |
| 4 | workloadRole/principal 拆分与身份元组、replay key 校验器 | workloadRole 封闭枚举仅 worker/verifier；Publisher 作为 Sandbox workload、借通用 role 跨 Port 取得能力的 fixture 全部失败；缺元组操作被拒；陈旧 lease replay 隔离为诊断材料；当前 lease replay 幂等归并 |
| 5 | replacement allocation Restore 与故障注入 | 响应丢失、并发 Restore、恢复后陈旧 handle 写入全部被拒；同一 Run/Attempt 单活跃 Allocation 断言通过 |
| 6 | Fake/Local Provider conformance 套件与 embedded/local 纵切 E2E | 两 Provider 通过同一套件（probe 按 §2 拓扑运行在各自 target allocation 内）；纵切全链路通过；Local 永不声明 hardened |
| 7 | Local MVP 全量回归 | 零回退 |

新增实施风险（不构成 Blocking Finding）：

| ID | 风险 | 缓解与关闭条件 |
| --- | --- | --- |
| R-010 | ADR 0017 尚在提案状态：提前启动 M8 实现或对外宣称 hardened 承诺会形成无法兑现的债务 | 已于 2026-08-10 随 ADR 0017 接受关闭；接受只关闭设计歧义，M8 实现须按修订后的实施计划重新启动，不做任何 hardened 对外承诺 |
| R-011 | 把 ADR 0017 接受误读为 M8 实现或 conformance 已完成，提前升级实施状态 | 实施状态分层记录：M7 保持 `IN_PROGRESS`、M8–M13 保持 `PLANNED`；首次 Sandbox SPI dogfood 成果按未接纳探索证据对待；conformance 通过以 M8/M10 退出门禁为准（Roadmap 状态与实施门禁同步） |

## Control Plane 与 Provider Port 边界冻结增补（2026-08-11）

维护者于 2026-08-11 在本任务全部 Gate 通过、独立 ReviewDecision accept 且无 P0/P1（含 Round 4 独立评审八项 P1、Round 5 复核四项残留与 Round 6 复核两项残留全部关闭）后接受 [ADR 0018](adr/0018-control-plane-and-provider-ports.md)，冻结 Marshal C/S Control Plane、按信任域分隔的 Provider Port、耐久注册/能力快照与在途 lease 撤销。该 ADR 澄清/部分取代 ADR 0017 §4/§6/§7/§8/§10/§12，并显式取代 ADR 0016 §6 经 ADR 0017 承接的 universal 接纳口径；Round 4 独立评审暴露的八项 P1（远程 transport 基线、securityDomainId 键空间、attestation 全链绑定、原子 fencing sink、SSE 恢复与再授权、engine 单一 seam、Port protocol family、legacy snapshot 残留）随 ADR 0018 §10–§16 一并关闭；Round 5 复核暴露的四项残留（复合安全域、Port protocol family 边界、Push/Pull 不变量等价、计划升级 bounded drain）随 ADR 0018 §2/§6/§7/§10/§16 修订关闭；Round 6 复核暴露的两项残留（Control Plane authority namespace 与 Provider actor 域分离、typed cross-domain edge）随 ADR 0018 §3/§10 修订关闭：authorityNamespaceId=(tenantNamespace, controlPlaneId, authorityScopeId) 拥有 Control Plane 权威对象、只允许 Core 写入，securityDomainId=(tenantNamespace, trustDomainKind, isolationDomainId) 只标识 Provider actor，跨信任域访问收敛为 Core 独占签发的 DispatchResultCapability/MaterialAccessGrant/PublicationAuthorization 三条 typed cross-domain edge 且默认拒绝（default deny），三条 edge 的 issuer 恒为 Core，issuer 不等于业务流的 sourceActor（DispatchResultCapability 的 sourceActor=Execution workload、targetAudience=Core result-ingress；MaterialAccessGrant 的 sourceActor=Data/Capability Provider、targetActor=Execution workload；PublicationAuthorization 的 issuer/sourceAuthority=Core、targetActor=Publication Provider）；已通过独立评审的远程 transport、attestation、原子 fencing sink、SSE、DurableExecutionEngine seam 与 legacy snapshot 修订完整保留，不回退；Runtime 架构、总体架构、安全模型、实施计划、Roadmap 与 ADR index 已同步。Round 8 复核进一步暴露并关闭一项残留（typed edge 跨域例外与适用范围）：三类 Core-authorized typed edge 明确为 Provider actor 跨 trust domain 访问默认拒绝规则的唯一 allowlist 例外，每次使用必须精确匹配 source/target securityDomainId 与该 edge 的全部对象、操作与时效绑定；Public API/SSE 使用各自的 AuthN/AuthZ、scope 约束与 re-AuthZ，Core 内部权威对象引用保留在 authority ledger，均不需要 Provider typed edge；会无条件拒绝 MaterialAccessGrant 等合法跨域访问、或把任何权威引用都强制经过 typed edge 的宽泛表述在 Round 8 当时并未全部清除（本轮更正该提前宣称）；Round 9 复核定位 ADR 0018 与 Runtime 架构、总体架构、安全模型、实施计划、Roadmap 状态的全部残留后，本轮已随 ADR 0018 §2/§3/§5/§7/§10 修订及 Runtime 架构、总体架构、安全模型、Roadmap 状态与本报告的同步清除。接受只冻结设计，不升级 M8–M13 实现/conformance 状态；M7 保持 `IN_PROGRESS`、M8–M13 保持 `PLANNED`。

已关闭的设计问题（随 ADR 0018 接受关闭）：

| ID | 级别 | 问题 | 冻结位置（ADR 0018） |
| --- | --- | --- | --- |
| C-A1 | P1 | 六类 Provider 未分信任域：低权限 Agent/Sandbox/Verification workload executor 与高权限 SCM/Publisher transport、凭据型 Artifact/Secret 共用一套 credential/AuthZ/审计/conformance profile，存在跨域提权面 | §2 三信任域（Execution / Publication / Data-Capability）+ §3 按 Port 分流的 required/forbidden 矩阵；域间不共享 credential、AuthZ、审计或 conformance profile；Publisher 永不成为 Sandbox workload |
| C-A2 | P1 | ADR 0016 §6 经 ADR 0017 §4 承接的 universal 接纳句无法区分 public-api 与注册/控制面——二者本应反向拒绝 workload lease 字段，universal 句却要求统一附加 providerType/lease 身份 | §3 身份按 Port 冻结、不设 universal envelope；public-api 禁止 providerType 并拒绝 workloadRole/allocationId/generation/fencingToken/DispatchLease；provider-registration/control 拒绝 workload lease；只有 dispatch-bound Port 绑定完整 lease 身份；§9 对照表显式取代 ADR 0016 §6 universal 口径 |
| C-A3 | P1 | Provider 注册无幂等身份与持久化约束：注册可 memory-only、CapabilitySnapshot 可变、legacy v1alpha1 快照映射未冻结，可能被静默补齐 scope/evidence | §5 ProviderRegistration 与不可变 ProviderCapabilitySnapshot；registrationId canonical 绑定 (principal, providerType, providerName, providerVersion, protocolVersion, scope) + idempotencyKey/requestDigest；同 key 不同 digest conflict；revoked/expired 不因普通 replay 复活；三类 expiry 独立；legacy mapper fail-closed 并记录 sourceCapabilitySnapshotDigest；禁止 memory-only registration |
| C-A4 | P1 | registration/快照/证据失效后 DispatchLease 命运未冻结，可能被实现为原地续租或静默降级 | §6 lease 只消费持久快照（registrationId/providerCapabilitySnapshotDigest/conformanceEvidenceDigests），引用/digest 永不改写只供审计；每次 heartbeat、接纳、reconcile 按当前 ledger 重判资格；revoke/expire/incompatible/supersede 使 active lease 立即失去资格（cancel/expiry + generation bump/fencing，终止对账，晚到结果隔离）；继续执行只能新 Attempt + 新 lease 重新 match |
| C-A5 | P1 | registry/queue/SSE 与事件账本的权威关系未冻结，cursor 压缩/过期/gap 后恢复不可判定；DurableExecutionEngine 的 Port 归属未明确 | §4 append-only event ledger 是唯一权威，snapshot/queue/SSE/registry/索引是可重建投影；SSE cursor 过期、gap 或压缩返回可判定 resync；DurableExecutionEngine 是 Core 的内部 Port |
| C-A6 | P1 | M8 注册/快照/撤销实施顺序未冻结，可能先 enable DispatchLease match 后补校验 | §7 M8 硬门禁顺序：negative fixtures/event contract → Schema → legacy mapper → durable embedded registration + ledger recovery → validation → 最后 enable DispatchLease match；前置缺失 claim/match fail closed；fixture 覆盖跨 scope/protocol、same key/different digest、revoked replay、restart/rebuild、substitution、claim 后失效的 Push/Pull |
| C-A7 | P1 | securityDomainId 同时承载 Control Plane 权威对象归属与 Provider actor 隔离，权威侧与 actor 侧键空间混同，事件账本、lifecycle 状态、ReviewDecision 与发布决定缺乏独立于 Provider 信任域的权威侧命名空间身份 | §10 冻结双键空间：authorityNamespaceId=(tenantNamespace, controlPlaneId, authorityScopeId) 拥有 Control Plane 权威对象（事件账本、lifecycle 状态、ReviewDecision、发布决定、idempotency/replay 权威记录、SSE cursor 权威序列），只允许 Core 写入；securityDomainId=(tenantNamespace, trustDomainKind, isolationDomainId) 只标识 Provider actor；authorityNamespaceId 不是 Provider 的 trustDomainKind 维度，Provider 不得写入或宣称权威对象；两键空间按职责分别进入持久主键/引用键空间；Provider actor 跨信任域访问默认拒绝，唯一 allowlist 例外是三条 Core 签发的 typed edge，未经对应 edge 授权或任一绑定不符一律 fail closed；Public API/SSE 与 Core 内部权威对象引用分别经各自 AuthN/AuthZ 与 authority ledger，不需要 Provider typed edge |
| C-A8 | P1 | 跨信任域请求（结果接纳、物料访问、发布授权）缺乏 typed edge，可能被实现为直接跨域传递 handle/credential 或隐式信任，默认拒绝不可机械证明 | §3 冻结三条 Core 独占签发的 typed cross-domain edge：DispatchResultCapability、MaterialAccessGrant、PublicationAuthorization，其余默认拒绝（default deny）；Core 是唯一签发者与唯一重新授权者；每条 edge 是 authority-scope-bound 权威记录，绑定 authority scope、issuer/source/target（issuer 为 Core，sourceActor/targetActor 按 edge 类型绑定）、attempt/allocation、expiry 与 digest；edge 不承载 raw credential/raw secret handle，不替代 ConformanceEvidence；Provider actor 跨 trustDomainKind 访问未经对应 edge 授权或任一绑定不符一律 fail closed；Provider 之间不得互相签发、转授或延展 edge |

新增实施风险（不构成 Blocking Finding，由各 Milestone 退出门禁关闭）：

| ID | 风险 | 缓解与关闭条件 |
| --- | --- | --- |
| R-012 | legacy v1alpha1 CapabilitySnapshot 被直接当作 Runtime 注册产物复用，绕过 mapper fail-closed 或静默补齐 scope/evidence | 显式版本化 mapper + sourceCapabilitySnapshotDigest 记录；缺失信息 fail closed（M8 退出门禁 fixture） |
| R-013 | registration/snapshot/evidence 失效后在途 lease 未被撤销，陈旧结果混入当前 Evidence/Review/Publication | 失效事件即时撤销 + generation bump/fencing + 晚到结果隔离；恢复 reconcile 按当前 ledger 重判资格（M8/M9 退出门禁故障注入） |
| R-014 | registry/SSE 被实现为第二个业务权威，cursor gap 静默续推导致客户端状态漂移 | registry/queue/SSE 仅作为账本投影；cursor 过期/gap/压缩返回可判定 resync（M9 退出门禁） |
| R-015 | 远程注册/Push/Pull 在 M9/M10 启用时先于传输身份上线，形成 credential/lease 可被窃听或重放的窗口 | ADR 0018 §12 transport 安全基线自首次 enable 生效：TLS 强制、mTLS/不可转移 workload identity、双向身份与 audience/scope 校验、rotation/revocation 与 replay protection；M9/M10 退出门禁明文 transport fixture 必须失败（M11 不补基线） |
| R-016 | securityDomainId 未进入持久主键/键空间，跨域 credential/AuthZ/audit/conformance 隔离不可机械证明 | ADR 0018 §10 现在冻结 securityDomainId 并进入全部键空间，未经三条 typed edge 中对应 active edge 授权或绑定不精确匹配的跨域引用 fail closed（三条 typed edge 是默认拒绝规则的唯一 allowlist 例外）；M8 Schema 落地时按 default 域接入，不等 M11 迁移；跨域引用 fixture 列入 M8/M9 退出门禁 |
| R-017 | backend workflow state 演化为第二调度权威，或双写窗口导致 command 与 ledger 分歧 | ADR 0018 §15 单一权威 seam（outbox/ledger-derived journal 二选一）+ commandId 从权威事实派生；backend 自决 lifecycle/retry/rework 的 fixture 必须失败（M9 退出门禁） |
| R-018 | SSE 被用作写通道（承载 ACK、lease heartbeat 或 command），投影获得写语义 | ADR 0018 §14 冻结 SSE 只读投影；承载 ACK/lease heartbeat/command 的 fixture 必须失败（M9 退出门禁） |

Round 4 独立评审进一步打开并关闭的 P1（全部 P1；随 ADR 0018 增补 §10–§16 于 2026-08-11 接受关闭）：

| ID | 级别 | 问题 | 关闭口径（ADR 0018） |
| --- | --- | --- | --- |
| ADR0018-REMOTE-TRANSPORT-BASELINE | P1 | ADR 0018 与 Runtime/M9 已启用远程注册和 Push/Pull，但 TLS 与调用者身份只作为 M11 远程入口门禁，远程 Provider credential/lease 可能先于传输身份上线 | §12 冻结：任何非 loopback/in-process transport 从首次 enable 起强制 TLS；workload-to-workload 优先 mTLS 或等价不可转移 workload identity；双向校验 server/provider 身份与 audience/scope，短期 credential rotation/revocation 与 replay protection；M11 只扩展 HA/多用户策略，不能补首次安全基线；同步 runtime/architecture/security/implementation-plan/roadmap 口径 |
| ADR0018-SECURITY-DOMAIN-KEYSPACE | P1 | registration/submission/lease/artifact/secret/cache/audit 缺乏机械安全域边界，无法证明域间 credential/AuthZ/audit/conformance 隔离 | §10 现在冻结 securityDomainId（单租户可固定 default，tenant 只作组成或前缀），经 Round 7 修订收敛为：actor 侧 securityDomainId 进入 registration/snapshot/evidence 携带项、lease/allocation actor 绑定、artifact/secret handle、cache、replay key 与 audit event 的引用字段，submission/run lifecycle、SSE cursor/sequence、idempotency 权威键等归权威侧 authorityNamespaceId；未经三条 typed edge 中对应 active edge 授权或绑定不精确匹配的跨域引用 fail closed；不等 M11 迁移持久主键 |
| ADR0018-PROVIDER-ATTESTATION-BINDING | P1 | attestation 仅绑定 principal/name/version/scope，相同软件版本可替换实例、配置或签发密钥后继续复用 hardened evidence | §11 冻结：ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence/lease claim 全链绑定 securityDomainId、稳定 providerInstanceId、effective configDigest、trust root（含 key id/rotation）；任一变化产生新 immutable snapshot/evidence 并触发 eligibility 重判；Worker/Verifier 不同 principal 与不同 allocation；高保证策略可要求 provider/host/failure-domain diversity；M8 补 substitution/config/key-rotation fixture（§7） |
| ADR0018-ATOMIC-FENCING-SINK | P1 | expectedSequence/CAS 只是抽象请求规则，ledger transition、当前 lease generation 与 Evidence/Artifact 引用未保证同一原子校验；旧 generation 可能先覆盖对象 key 再被 ledger 拒绝 | §13 冻结：权威 ledger sink 使用 atomic compare-and-append/transaction；Artifact/Evidence/Checkpoint/Candidate bytes 的接纳关系归 authority ledger，使用 authorityNamespaceId+run+attempt+allocation+generation scoped immutable key（actor securityDomainId 只作为 provenance 记录）、digest-verified put-if-absent；陈旧/冲突 bytes 只进 quarantine namespace；M8/M9 补 lost-response、concurrent-write、old-generation overwrite fixture |
| ADR0018-SSE-RECOVERY-AUTHZ | P1 | SSE 只有 eventId/cursor/resync，未冻结 scope/sequence、交付与去重、压缩恢复、背压或长连接重新授权 | §14 冻结：cursor 身份 authorityNamespaceId+scope+ledgerSequence（权威账本的权威侧身份；订阅方另绑定自身 securityDomainId 授权）、scope 内单调 sequence、at-least-once + eventId/sequence 去重、expiry/gap/compaction 的 deterministic resync 起点与 snapshot digest、heartbeat 与有界 backpressure、周期性与敏感变更即时 re-Authorization；SSE 是只读投影，不承载 ACK、lease heartbeat 或 command；参数值留 M9 Schema |
| ADR0018-DURABLE-ENGINE-SEAM | P1 | backend 虽声明非权威，但未关闭 ledger 已提交而 command 未投递（或反向）的双写窗口，Temporal/Local Engine 仍可能形成第二调度权威 | §15 冻结单一权威 seam：同事务 outbox 或 ledger-derived Core command journal 二选一；commandId 从权威事实稳定派生；backend 只消费/回报，workflow/activity state 不得成为业务权威；M9 backend profile 与升级 fixture 覆盖 workflow versioning/build ID、Continue-As-New、payload 外置/上限、activity heartbeat/cancel/retry |
| ADR0018-PORT-PROTOCOL-FAMILY | P1 | 现行入口仍可被理解为六类 Provider 共用 operation schema、audience 和 conformance，Publication/Secret 可能借通用 Provider 能力进入 Execution 语境 | §16 冻结 versioned protocol family：每个 Port 独立 audience、AuthZ scope、request/response schema、error/idempotency/revocation 与 conformance profile；只共享 transport、JCS 与最小 base auth primitives；禁止跨 Port token/schema/operation；embedded/Push/Pull 仅是同一 Port 内 adapter；实施计划与 Roadmap 已同步 |
| ADR0018-LEGACY-CAPABILITY-RESIDUE | P1 | Push capability match 与两拓扑绑定仍写 legacy CapabilitySnapshot/ConformanceEvidence digest，随后才要求 ProviderCapabilitySnapshot，与持久快照口径冲突 | 已直接改为比对/绑定持久 ProviderCapabilitySnapshot（providerCapabilitySnapshotDigest）+ conformanceEvidenceDigests 封闭集合（Runtime 架构 DispatchLease/Push 两拓扑、实施计划 M9 两拓扑绑定）；legacy CapabilitySnapshot 仅保留在 fail-closed mapper 来源语境（AgentAdapter probe 快照） |

Round 5 复核进一步打开并关闭的四项残留（全部 P1；随 ADR 0018 §2/§6/§7/§10/§16 修订于 2026-08-11 接受关闭）：

| ID | 级别 | 问题 | 关闭口径（ADR 0018） |
| --- | --- | --- | --- |
| ADR0018-COMPOSITE-SECURITY-DOMAIN | P1 | securityDomainId 仍是全系统单一标识（单租户固定 default），与其同时宣称的 Execution/Publication/Data-Capability 三信任域隔离冲突，域间隔离不可机械证明 | §10 改为复合 security namespace `(tenantNamespace, trustDomainKind, isolationDomainId)`：tenantNamespace 单租户可固定 default（tenant 只能作为该组成）；trustDomainKind 封闭枚举 execution/publication/data-capability；isolationDomainId 标识同 kind 内隔离边界；submission/run、registration/snapshot/evidence、lease/allocation、SSE cursor/sequence、artifact/secret handle、cache、idempotency/replay key 与 audit event 全部携带复合边界；未经三条 typed edge 中对应 active edge 授权或绑定不精确匹配的跨域引用与跨 trustDomainKind 引用 fail closed（三条 typed edge 是默认拒绝规则的唯一 allowlist 例外）；Runtime/总体架构/安全模型/实施计划/Roadmap 的单一 default 表述已同步清除 |
| ADR0018-PORT-PROTOCOL-FAMILY-BOUNDARY | P1 | 六类 Provider 仍被写成同一语义 Port、共享同一 conformance 套件，与按 Port 的 versioned protocol family 冲突 | §2/§16 澄清：六类 Provider 彼此是不同 Port、不同 protocol family，不共享 family/audience/schema/profile/suite/token/operation；对每个具体 Port/protocol family，embedded/in-process、Push HTTP、Pull outbound runner 才是该族的 transport adapter，运行该族统一的 conformance suite；runtime/plan/roadmap 中相反残留已清除 |
| ADR0018-PUSH-PULL-INVARIANT-EQUIVALENCE | P1 | Push/Pull 被写成“只改变连接发起方、其余语义完全等价”，不允许拓扑特定 transition/timing，conformance 比较口径不可执行 | §16 只冻结 outcome/invariant equivalence：唯一 claim、eligibility、fencing、deadline（ack/heartbeat/expiry）、无双活与晚到隔离；允许 topology-specific 的 offer/poll/claim/ack transition 与 timing；两拓扑 conformance 比较 normalized business trace 与业务不变量，不比较逐步 wire trace；runtime/architecture/plan/roadmap 的完全等价措辞已清除 |
| ADR0018-UPGRADE-DRAIN-SPLIT | P1 | 撤销与不兼容升级未分级：security-critical 场景可能保留 drain 窗口，普通升级可能被立即 kill、复活旧注册或改写旧 lease digest | §6/§7 分级冻结：security-critical revoke（credential compromise、protocol violation）立即 cancel + generation bump + kill，不留 drain 窗口；planned/ordinary incompatible upgrade 使用新 registration/新 snapshot，旧实例 stop-new + bounded drain，drain deadline 到期再 fence；事件机器可读原因码与审计记录分开；普通升级不得复活旧注册或改写旧 lease digest；M8/M9 补对应故障注入/退出门禁 |

Round 6 复核进一步打开并关闭的两项残留（全部 P1；随 ADR 0018 §3/§10 修订于 2026-08-11 接受关闭）：

| ID | 级别 | 问题 | 关闭口径（ADR 0018） |
| --- | --- | --- | --- |
| ADR0018-AUTHORITY-NAMESPACE-SEPARATION | P1 | securityDomainId 同时承载 Control Plane 权威对象归属与 Provider actor 隔离，权威侧与 actor 侧键空间混同，事件账本、lifecycle 状态、ReviewDecision 与发布决定缺乏独立于 Provider 信任域的权威侧命名空间身份 | §10 冻结双键空间：authorityNamespaceId=(tenantNamespace, controlPlaneId, authorityScopeId) 拥有 Control Plane 权威对象（事件账本、lifecycle 状态、ReviewDecision、发布决定、idempotency/replay 权威记录、SSE cursor 权威序列），只允许 Core 写入；securityDomainId=(tenantNamespace, trustDomainKind, isolationDomainId) 只标识 Provider actor；authorityNamespaceId 不是 Provider 的 trustDomainKind 维度，不属于 Provider actor 侧任何信任域，Provider 不得写入或宣称权威对象；SSE cursor 身份改为 authorityNamespaceId+scope+ledgerSequence（订阅方另绑定自身 securityDomainId 授权）；DispatchLease 双绑定两键空间；M8 Schema 落地时 Local MVP 记录按复合边界视图接入，不改写历史数据；Runtime/总体架构/安全模型/实施计划/Roadmap 已同步 |
| ADR0018-TYPED-CROSS-DOMAIN-EDGES | P1 | 跨信任域请求（结果接纳、物料访问、发布授权）缺乏 typed edge，可能被实现为直接跨域传递 handle/credential 或隐式信任，默认拒绝不可机械证明 | §3 冻结三条 Core 独占签发的 typed cross-domain edge——DispatchResultCapability（Execution 信任域结果/heartbeat/receipt 接纳）、MaterialAccessGrant（Data/Capability 信任域 scoped 访问短期能力）、PublicationAuthorization（Publication 信任域绑定 SideEffectIntent/ReviewDecision/evidence digest 的发布授权）——其余默认拒绝（default deny）；Core 是唯一签发者与唯一重新授权者；edge 是 Core 在 authorityNamespaceId 内签发的 authority-scope-bound 权威记录，绑定 authority scope、issuer/source/target（issuer 为 Core，sourceActor/targetActor 按 edge 类型绑定）、attempt/allocation、expiry 与 digest；edge 不承载 raw credential/raw secret handle，不替代 ConformanceEvidence；Provider actor 跨 trustDomainKind 访问未经对应 edge 授权或任一绑定不符一律 fail closed；M8 补伪造签发者/过期/撤销/raw handle/跨域 negative fixture |

ADR 0018 的接受不改变 Local MVP 的 `APPROVED_FOR_IMPLEMENTATION` / `USABLE` 结论，也不放宽任何既有不变量：Worker 不自证、单写入者、Worker/Verifier/Publisher 分权、ReviewDecision 证据绑定、fail-closed、Draft-only 与 merge never 均保持有效。

Round 7 复核进一步打开并关闭的三项残留（全部 P1；随 ADR 0018 §3/§4/§5/§7/§10/§13 修订于 2026-08-11 关闭；接受只冻结设计，不升级 M8–M13 实现/conformance 状态）：

| ID | 级别 | 问题 | 关闭口径（ADR 0018） |
| --- | --- | --- | --- |
| ADR0018-AUTHORITY-OBJECT-OWNERSHIP | P1 | 双键空间残留：仍存留“每个 Port 的请求一律携带 actor 安全域身份”的 universal 表述，submission/run 被归入 actor 侧记录，权威对象清单未收敛为 authorityNamespaceId 独占拥有，controlPlaneId 被写成具体进程实例，ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence 与 Artifact/Evidence/Checkpoint/Candidate bytes 接纳关系的权威归属未冻结 | §3/§4/§5/§10/§13 修订关闭：删除上述 universal 携带表述，Port 请求按矩阵规则绑定 actor securityDomainId；submission/Task/Run/Attempt/ledger/DispatchLease/Allocation/ReviewDecision/Evidence graph/Outcome/SideEffectIntent/Receipt reconcile/typed edge/idempotency/outbox/audit/SSE cursor 序列等权威对象一律由 authorityNamespaceId 拥有、只允许 Core 写入；ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence 也是 authority ledger 事实，仅携带 actor securityDomainId、provenance 与 eligibility，registrationId 幂等绑定中的 securityDomainId 为所携带的 actor 身份；Artifact/Checkpoint/Candidate/Evidence bytes 的接纳关系归 authority ledger；controlPlaneId 冻结为 HA/灾备中保持稳定的逻辑权威身份，不是进程实例；Runtime/总体架构/安全模型/实施计划/Roadmap 的残留表述已同步清除 |
| ADR0018-TYPED-EDGE-LIFECYCLE | P1 | 三条 typed edge 只绑定 authority scope、source/target、attempt/allocation、expiry 与 digest：operation 无封闭枚举、revocation/replay 语义缺失、每次使用无 current-ledger recheck、派生 token/handle 可能被缓存或离线校验成第二权威 | §3/§7 修订关闭：三条 edge 明确为 Core-only，冻结 issuer/source/target/operation/expiry/digest/revocation/replay/current-ledger recheck 七项生命周期要素与各自专属绑定（lease/generation、物料对象 key/content digest 封闭集合、SideEffectIntent/ReviewDecision/evidence digest）；issuer 恒为 Core 且不等于业务流的 sourceActor，sourceActor/targetActor 按 edge 类型绑定，target 是 securityDomainId 标识的 Provider actor，缺失任一要素 fail closed；每次使用都按当前 authority ledger 复核；edge 派生的 token/handle 只是指向 edge 权威记录的单向引用，自身不承载授权语义，派生 token/handle 不得成为第二权威；§7 补 Core-only typed edge fixture（伪造签发者/过期/撤销/operation 不符、绕过 recheck 使用派生 token/handle、raw handle/credential、以 edge 替代 ConformanceEvidence 必须失败） |
| ADR0018-PUBLICAPI-AUTHORITY-KEY | P1 | Public API 提交幂等身份的描述仍以 actor securityDomainId 为键空间组成，submission 幂等与对象 key 键空间未按权威对象归属收敛 | §3/§10/§13/§14 修订关闭：Public API 幂等提交身份为 `(authorityNamespaceId, scope, idempotencyKey, requestDigest)`（submission 与幂等权威记录由 authorityNamespaceId 拥有）；SSE cursor 身份维持 authorityNamespaceId+scope+ledgerSequence（各文档残留的按 actor securityDomainId 键控的 cursor 身份表述已修正）；Artifact/Evidence/Checkpoint/Candidate 对象 key 为 authorityNamespaceId+run+attempt+allocation+generation scoped，actor securityDomainId 只作为 provenance 记录；Runtime/总体架构/安全模型/实施计划/Roadmap 幂等表述已同步修正 |

Round 8 复核进一步打开并关闭的一项残留（P1；随 ADR 0018 §3/§10 修订于 2026-08-11 关闭；接受只冻结设计，不升级 M8–M13 实现/conformance 状态）：

| ID | 级别 | 问题 | 关闭口径（ADR 0018） |
| --- | --- | --- | --- |
| ADR0018-TYPED-EDGE-EXCEPTION-SCOPE | P1 | typed edge 适用范围残留两类宽泛表述：一类把跨 trustDomainKind 的访问写成无条件拒绝，可被解读为拒绝 MaterialAccessGrant 等合法跨域访问；另一类把权威对象的引用写成必须统一经过 typed edge，可被解读为 Public API/SSE 客户端访问与 Core 内部权威引用也必须持有 Provider typed edge | §3/§10 修订关闭：三类 Core-authorized typed edge 明确为 Provider actor 跨 trust domain 访问默认拒绝（default deny）规则的唯一 allowlist 例外，不是对跨域 raw handle/raw credential 或任意引用的豁免；每次使用必须精确匹配 source/target securityDomainId 与该 edge 绑定的全部对象、operation、Attempt/Allocation、generation、expiry/deadline、digest 和当前 authority ledger 状态，任一绑定不符 fail closed；Public API/SSE 是 Client 到 Control Plane 的入口，使用各自的 AuthN/AuthZ、scope 约束与 re-AuthZ，不需要 Provider typed edge；Core 内部权威对象引用（ledger 事件间引用、cursor、证据关系、outbox/ledger 引用）保留在 authority ledger 内，不需要 Provider typed edge；§7 补对应 negative/positive fixture；当时宣称“Runtime 架构、总体架构、安全模型、Roadmap 状态与本报告的残留宽泛表述已同步清除”与实际不符——ADR 0018 与 Runtime 架构、总体架构、安全模型、实施计划、Roadmap 状态仍残留无条件 fail closed 表述——该残留由 Round 9 复核（ADR0018-UNQUALIFIED-CROSS-DOMAIN-RESIDUE）定位并随本轮修订实际清除 |

Round 9 复核进一步打开并关闭的两项残留（全部 P1；随 ADR 0018 §2/§3/§5/§7/§10 修订于 2026-08-11 关闭；接受只冻结设计，不升级 M8–M13 实现/conformance 状态；本轮同时更正 Round 8 记录中在清除实际完成前提前宣称宽泛表述“已同步清除”的表述）：

| ID | 级别 | 问题 | 关闭口径（ADR 0018 及各文档） |
| --- | --- | --- | --- |
| ADR0018-UNQUALIFIED-CROSS-DOMAIN-RESIDUE | P1 | ADR 0018 与 Runtime 架构、总体架构、安全模型、实施计划、Roadmap 状态仍把跨域能力、securityDomainId 或 trustDomainKind 引用写成无条件 fail closed，与 MaterialAccessGrant 等三类合法 allowlist edge 冲突；本报告 Round 8 记录提前宣称该类宽泛表述已全部清除 | 逐处改为：仅未经三条 Core-only typed edge 中对应 active edge 授权，或 source/target securityDomainId、edge type、对象、operation、Attempt/Allocation、generation、expiry/deadline、digest、当前 authority ledger 状态任一不精确匹配的 Provider actor 跨域访问 fail closed；三条 typed edge 是默认拒绝规则的唯一 allowlist 例外，Public API/SSE 使用各自 AuthN/AuthZ 与 re-AuthZ、Core 内部权威引用保留在 authority ledger，均不需要 Provider typed edge；M8/M9 fixture 明确区分三类合法 positive 与无 edge、错 edge、错绑定 negative；本报告的提前清除宣称已更正 |
| ADR0018-NONEDGE-PORT-AND-SAME-DOMAIN-AUTHZ | P1 | provider-registration/control 的非 edge 授权路径未冻结；securityDomainId 相同只是 provenance/partition 条件的事实未冻结，可能被实现成同域 bearer grant | ADR 0018 §3/§10 冻结：provider-registration/control 不持有三类业务 typed edge，必须通过 transport identity、该 Port 的 AuthN/AuthZ、scope/protocol validation 与 registration protocol，由 Core 决定并把获准事实写入 authority ledger；securityDomainId 相同不构成授权，同域请求仍须逐项匹配具体 Port 的 principal/registrationId/providerInstanceId/scope/attempt/allocation/generation/operation 门禁；§7 与实施计划/Roadmap 补对应 positive/negative fixture，Runtime 架构、总体架构、安全模型同步 |

## ADR 建议

以下 ADR 共同构成当前 Local MVP 的架构与安全基线，建议一起接受：

1. [ADR 0001：CLI-first 模块化单体](adr/0001-cli-first-modular-monolith.md)
2. [ADR 0002：每个任务一个 Worktree](adr/0002-worktree-isolation.md)
3. [ADR 0003：Worker 与 Publisher 分权](adr/0003-separate-worker-and-publisher.md)
4. [ADR 0004：独立验证](adr/0004-independent-verification.md)
5. [ADR 0005：Go 作为 Core Runtime](adr/0005-go-runtime.md)
6. [ADR 0006：Attempt 控制根与业务 Worktree 分离](adr/0006-attempt-control-root.md)
7. [ADR 0007：先记录意图的受控发布与远端对账](adr/0007-intent-first-publication.md)
8. [ADR 0008：可插拔 Observer Backend](adr/0008-pluggable-observer-backends.md)
9. [ADR 0009：原生 PTY Terminal Session 执行传输](adr/0009-terminal-session-execution.md)
10. [ADR 0010：受控自治、审批 Gate 与人工介入](adr/0010-controlled-autonomy-and-intervention.md)
11. [ADR 0011：密封启动与可判定的原生 TUI 传输](adr/0011-sealed-native-tui-transport.md)

删除 ADR 0002–0004 中任何一个都会使本批准失效，并要求重新进行安全与生命周期审计。

[ADR 0016：耐久 Runtime 与可插拔 Sandbox Provider](adr/0016-durable-runtime-and-sandbox-provider.md) 已于 2026-08-10 被维护者接受（其决策来源为当日维护者对长期目标的明确修正与批准），并将 ADR 0015 置于 Superseded before acceptance；ADR 0016 冻结的不变量集合与上表 Local MVP 不变量一致，放宽任何一条同样要求重新审计。

[ADR 0017：Provider-neutral Sandbox 安全契约](adr/0017-provider-neutral-sandbox-contract.md) 已于 2026-08-10 在全部 P1 通过 Round 2 独立验证与 ReviewDecision accept 后被维护者接受；它澄清/部分取代 ADR 0016 的 §4/§5/§6/§7/§9，关闭首次 Sandbox SPI dogfood reject 暴露的合同级缺口（S-A1–S-A7）与 Round 2 六项歧义（S-B1–S-B6）。接受只关闭设计歧义，不构成对 M8 实现或 conformance 完成的声明；相应实现仍须逐项通过 Milestone 退出门禁。

[ADR 0018：Marshal C/S Control Plane 与按信任域分隔的 Provider Port](adr/0018-control-plane-and-provider-ports.md) 已于 2026-08-11 在本任务全部 Gate 通过、独立 ReviewDecision accept 且无 P0/P1（含 Round 4 独立评审八项 P1、Round 5 复核四项残留与 Round 6 复核两项残留全部关闭）后被维护者接受；它澄清/部分取代 ADR 0017 的 §4/§6/§7/§8/§10/§12，并显式取代 ADR 0016 §6 经 ADR 0017 承接的 universal 接纳口径，关闭本轮设计评审暴露的 C-A1–C-A8、Round 4 独立评审的八项 P1（见“Control Plane 与 Provider Port 边界冻结增补”节）、Round 5 复核的四项残留（复合安全域、Port protocol family 边界、Push/Pull 不变量等价、计划升级 bounded drain）与 Round 6 复核的两项残留（Control Plane authority namespace 与 Provider actor 域分离、typed cross-domain edge）与 Round 7 复核的三项残留（双键空间残留清除、Core-only typed edge 生命周期细化、Public API 幂等/SSE/对象 key 修正为 authorityNamespaceId）与 Round 8 复核的一项残留（typed edge 跨域例外与适用范围，见“Control Plane 与 Provider Port 边界冻结增补”节）与 Round 9 复核的两项残留（跨域 fail closed 表述精确化、非 edge Port 与同域不自动授权，见“Control Plane 与 Provider Port 边界冻结增补”节）。接受只冻结设计，不升级 M8–M13 实现或 conformance 状态；M7 保持 `IN_PROGRESS`、M8–M13 保持 `PLANNED`，实施须按修订后的实施计划与 M8 顺序硬门禁逐项通过退出门禁；各远程能力首次 enable 必须满足 ADR 0018 §12 transport 安全基线，M11 不补首次基线。

## 实施门禁（分层）

文档审计和维护者接受均已完成。实施必须：

1. 从 Milestone 0 开始，不提前执行 Worker 或 Publication Side Effect；
2. 每个 Milestone 满足 Exit Criteria 后才能进入下一阶段。

分层结论：

- **Local MVP（Milestone 0–6）**：**`APPROVED_FOR_IMPLEMENTATION`** / `USABLE`，该范围实施门禁已开启且保持不变；
- **Runtime/Sandbox 契约（M7–M13）**：ADR 0017 接受前为 `BLOCKED`；2026-08-10 接受后设计歧义关闭，实现可按修订后的[实施计划](implementation-plan.md)推进，但任何 Milestone 的完成与 conformance 通过仍须以对应退出门禁与独立证据为准，不得因 ADR 接受而提前声明；2026-08-11 ADR 0018 接受后，Control Plane 与 Provider Port 边界口径连同权威/actor 双键空间（authorityNamespaceId=(tenantNamespace, controlPlaneId, authorityScopeId) 拥有 Control Plane 权威对象；securityDomainId=(tenantNamespace, trustDomainKind, isolationDomainId) 只标识 Provider actor）、三条 Core 独占签发的 typed cross-domain edge（DispatchResultCapability/MaterialAccessGrant/PublicationAuthorization，默认拒绝）、attestation 全链绑定、原子 fencing 写入汇、SSE 恢复与再授权、engine 单一权威 seam、按 Port protocol family、Push/Pull outcome/invariant equivalence 与失效处置分级一并冻结；Round 7 复核三项残留随 ADR 0018 §3/§4/§5/§7/§10/§13 修订关闭——权威对象清单收敛为 authorityNamespaceId 独占拥有（ProviderRegistration/ProviderCapabilitySnapshot/ConformanceEvidence 为 authority ledger 事实仅携带 actor securityDomainId/provenance/eligibility，Artifact/Checkpoint/Candidate/Evidence bytes 接纳关系归 authority ledger，controlPlaneId 为 HA/灾备中保持稳定的逻辑权威身份而非进程实例）、三条 Core-only typed edge 冻结 issuer/source/target（issuer 恒为 Core 且不等于业务流 sourceActor；sourceActor/targetActor/targetAudience 按 edge 类型绑定）/operation/expiry/digest/revocation/replay/current-ledger recheck 与专属绑定（派生 token/handle 不得成为第二权威）、Public API 幂等/SSE cursor/对象 key 使用 authorityNamespaceId；Round 8 复核一项残留随 ADR 0018 §3/§10 修订关闭——三类 Core-authorized typed edge 是 Provider actor 跨 trust domain 访问默认拒绝规则的唯一 allowlist 例外（每次使用精确匹配 source/target securityDomainId 与全部对象、操作、时效绑定），Public API/SSE 使用各自 AuthN/AuthZ 与 re-AuthZ、Core 内部权威引用保留在 authority ledger，均无需 Provider typed edge；Round 9 复核两项残留随 ADR 0018 §2/§3/§5/§7/§10 修订关闭——删除会无条件拒绝 MaterialAccessGrant 等合法 typed edge、或把任何权威引用都强制经过 typed edge 的宽泛表述（跨域 fail closed 一律限定为未经对应 active typed edge 授权或绑定不精确匹配），provider-registration/control 不持有三类业务 typed edge（经 transport identity、该 Port AuthN/AuthZ、scope/protocol validation 与 registration protocol，由 Core 写 authority ledger），securityDomainId 相同只是 provenance/partition 条件、不构成授权，M8/M9 补三类 edge positive 与无 edge/错 edge/错绑定 negative fixture；实施仍按 M8 顺序硬门禁（negative fixtures → Schema → mapper → ledger recovery → validation → 最后 enable DispatchLease match）推进；任何非 loopback/in-process 远程能力首次 enable 必须满足 ADR 0018 §12 transport 安全基线，M11 不补首次基线。

因此本报告当前存在四项未关闭 P1：Issue #53 的 CI rework evidence 缺口，以及 ADR 0032 B2 独立复核重开的三项 authority/delivery recovery 缺口；详情分别见对应增补节。`APPROVED_FOR_IMPLEMENTATION` 结论只适用于 Local MVP 范围，不能被解释为受控 merge 已支持；Runtime/Sandbox 部分按上述分层状态执行。

## Milestone 7 最终关闭审计（2026-08-11）

Milestone 7（架构与契约）于 2026-08-11 通过退出门禁，Roadmap 状态更新为 `PASSED`。exact evidence 如下：

- Marshal Run `m7-control-provider-boundary-adr-r15-20260811` 完成 M7 最终架构稿，reviewRound=2，32/32 required Gates 通过，独立审查无 P0/P1，Run 进入 `ACCEPTED`；
- GitHub [PR #13](https://github.com/chiga0/marshal-harness/pull/13) 通过 Quality (ubuntu-latest)、Quality (macos-latest)、Secret scan 与 GitGuardian 检查（CI run `31449333738`），2026-08-11 由维护者手工合入 main（merge commit `4b2f3248f24ec2a67642ec77822fe6bb59730df7`，非 auto-merge）；
- M7 退出门禁逐项满足：ADR 0016/0017/0018 已接受，治理与文档口径一致，Local MVP 全量回归通过且本仓库 CI 全绿。

本次关闭只记录设计与契约阶段通过：M8–M13 保持 `PLANNED`；Runtime 实现、Sandbox SPI conformance、`marshal-server`/Public API、Cloudflare Provider 与 Goal 编排均未实现，本节不对其中任何一项作出完成声明。

本报告前文“M7 保持 `IN_PROGRESS`”的记录为 ADR 0017/0018 接受时点的状态；自 2026-08-11 起 M7 状态为 `PASSED`。本节不引入新的 Blocking Finding，不改变 Local MVP `APPROVED_FOR_IMPLEMENTATION` / `USABLE` 结论。

## 确定性控制面、补偿与 Goal Roadmap 增补审计（2026-08-11）

维护者要求以局外人视角重新审计“稳定 Runtime 持续接收和分发复杂任务”的终态。三路独立只读审计分别检查 Typed Execution、SideEffect/Compensation 与 M13 Goal 编排，结论是：ADR 0016–0018 的 Durable Control Plane 与 Provider 分层方向正确，无需改成 LLM Supervisor 或自由 P2P；但以下合同级缺口必须在实施前关闭。维护者据此接受 [ADR 0019](adr/0019-deterministic-control-plane-typed-execution-and-goal-admission.md)。

| ID | 原级别 | 问题 | ADR 0019 关闭口径 | 实现状态 |
| --- | --- | --- | --- | --- |
| D-A1 PLAN-AUTHORITY | P1 | Planner/主 Agent 可能被实现为直接创建 Run、写状态的第二 Supervisor | Supervisor 明确定义为确定性 Core；Planner 只提交 proposal，Core deterministic admission 后才 materialize | M13 `PLANNED` |
| D-A2 TYPED-RESULTS | P1 | Candidate/Evidence/Assessment/Receipt 可能被通用 Executor 结果混同 | 四类输出独立接纳；共享执行基座但按 Port 隔离 Schema/principal/credential/conformance | M8–M12 `PLANNED` |
| D-A3 GRAPH-BOUNDS | P1 | Goal DAG 缺累计规模、跨 revision cycle、预算 reservation 与重规划不变量 | 冻结 effective graph 校验、整个 Goal 累计 guardrail、先 reserve 后 dispatch、immutable revision | M13 `PLANNED` |
| D-A4 EVIDENCE-ELIGIBILITY | P1 | 跨 Run/上游 Artifact 改变后的 Evidence 适用性无法判定 | Evidence 不可变；以 dependency set 与追加 eligibility event 做局部失效 | M13 `PLANNED` |
| D-A5 HUMAN-RESUME | P1 | Run `BLOCKED` 已是终态，却缺少跨日人工等待语义 | 不改 Run 生命周期；Goal `PAUSED`/resume 负责等待并重新校验/fence | M13 `PLANNED` |
| D-A6 COMPENSATION | P1 | 通用 SideEffect 只在设计中，失败易被误称为 rollback | append-only intent/receipt/reconcile；补偿是新副作用，按 disposition class 与 Policy 控制 | M8–M12 `PLANNED` |
| D-A7 ORPHAN-CLEANUP | P1 | expired cleanup 删除 Run 目录后再移除 worktree，崩溃可能留下无法从 runs 枚举恢复的 orphan | M8/M9 建 authority-scoped cleanup ledger 与 orphan reconcile fixture | M8/M9 `PLANNED` |

设计 Finding 已由 ADR 0019 关闭，但实现风险保持开放并明确映射到 M8–M13；不得把本次文档接受描述为功能已实现。M7 保持 `PASSED`，M8–M13 保持 `PLANNED`，Local MVP `USABLE` 不变。

ADR 0019 首稿的最终独立复核另发现并关闭五项 P1：按 Port 接纳被错误泛化为 universal generation 校验；Core 内部 SideEffect 记录可能被误作跨 Port wire Schema；Goal pause 未闭合 active Run 处置；budget reservation 缺 settle/release/expire/reconcile；M8 共享执行基座措辞可能绕过 ADR 0018 §7 顺序。修订后分别冻结：dispatch-bound 才校验 lease generation/fencing；各 Port receipt 经版本化 fail-closed mapper 进入内部 authority record；`drain-active|cancel-active` 不直接改 Run state；append-only reservation 状态机；任何 claim/lease activation 仍位于 ADR 0018 §7 硬顺序最后一步。复核后无未关闭 P0/P1。

## Docs v2 信息架构审计（2026-08-11）

打开并关闭文档可发现性问题 `DOCS-IA-1`：旧 Pages 主导航同时暴露规范、实现细节、19 份 ADR、Milestone 报告、研究与英文摘要，新读者无法判断正确入口，也容易把历史材料当成当前承诺。

关闭措施：

- 主导航收敛为“开始 → 理解 Marshal → 使用 Marshal → 构建与扩展 → 更多资料”，只展示 13 个高频当前页面；
- 新增快速开始、核心概念、参考索引和历史档案四个分层入口，并把旧总体架构重写为当前 + 目标的最新整体架构；
- ADR、审计、研究、Milestone Scope/报告/Review、兼容性矩阵和英文摘要默认隐藏，但保留稳定 URL、搜索与审计可追溯性；
- 规范冲突顺序明确为 Accepted ADR → Runtime/lifecycle/security/Schema → 专项契约 → 实施/Roadmap → 指南 → 历史；
- 删除门槛收紧为“完全重复、空白且无审计价值”。本轮没有文件满足安全删除条件，因此不以 Git 历史替代仍被引用的审计材料。

该整理不改变信任边界、持久化契约、生命周期或发布权限，不触发新 ADR。

## 产品定位一致性复核（2026-08-11）

打开并关闭文档一致性问题 `DOCS-POSITIONING-1`：README 与 Pages 首页先以“证据门禁式 Coding Agent 编排器”和 Local MVP 描述 Marshal，再把长寿命 Control Plane 写成未来目标。这会让当前交付阶段反向定义产品边界，与 ADR 0016–0019 及现行整体架构不一致。

关闭措施：

- README、Pages 首页、愿景、核心概念、整体架构、Runtime 架构和站点元数据统一以“面向 Agent 驱动软件工程的长寿命、可自托管、确定性 Control Plane”定义产品；
- 明确 Runtime 持续接收 Goal/Task，将复杂需求接纳并分发为有界 typed workload；Agent、Sandbox 与 durable backend 均可替换，不能成为第二业务权威；
- 把 Local MVP 统一改写为当前可用的 embedded/local 先行实现与回归基线，并在独立状态区说明 M8–M13 尚未交付；
- Roadmap、实施计划、安全模型、操作手册、参考索引和英文入口同步区分“产品定义”与“当前成熟度”；
- 保留历史 Milestone、ADR 与审计原文的时点表述，不重写历史证据。

本次只修正文档叙事，没有改变 ADR 已冻结的信任边界、持久化契约、生命周期或发布权限，因此不触发新 ADR。后续首页不得再以 Local MVP 能力清单替代产品定位，也不得把 `PLANNED` 能力写成已经交付。

## 用户文档与工程规范分层复核（2026-08-11）

打开并关闭可读性问题 `DOCS-AUDIENCE-1`：Pages 虽已缩减导航，但仍直接发布并索引 Runtime 架构、安全模型、ADR、Schema 术语与实现门禁。普通用户必须理解内部对象名、Digest、Lease、fencing 与 ADR 修订链才能阅读截图所示页面，信息架构仍以开发者而非用户任务为中心。

关闭措施：

- Pages 只构建 8 个用户页面：首页、产品说明、当前能力、快速开始、日常使用、Codex 使用、工作原理、安全与隐私；
- Runtime/整体工程架构、ADR、Schema、审计、研究、Milestone、开发指南、实施计划与详细 Operator Runbook 继续由 Git 版本控制，但通过 `exclude_docs` 排除在 Pages HTML 和搜索索引之外；
- 用户页面不再出现 authorityNamespaceId、securityDomainId、ProviderCapabilitySnapshot、fencingToken、协议族或 ADR 修订链等实现术语；
- README 同步改为用户入口，只保留价值、当前能力、最小流程、安全边界和贡献入口；
- 当前能力页负责区分已交付与在建能力，避免简化表达演变成虚假功能承诺。

本次只改变发布信息架构与面向用户的解释层，工程规范内容及其权威顺序保持不变，不改变信任边界、持久化契约、生命周期或发布权限，不触发新 ADR。

## ADR 0032 B2 受控合并实现审计增补（2026-08-17）

ADR 0032 B2 初轮复核曾把五项实现缺口记为关闭。随后独立复核证明，其中 authorization 与 delivery 的 sidecar monotonic head 和正文记录处于同一故障域，不能检测协调回滚；分步持久化仍有 crash dead zone；恢复还可能基于变化后的时间/check observation 为不同 digest 重签授权。因此旧关闭口径被后续证据部分推翻，以下表格按最新证据修正。该修正不把受控合并误报为 M10 完成，也不改变 M11–M13 状态。

| ID | 级别 | 状态 | 关闭口径 |
| --- | --- | --- | --- |
| `ADR0032-B2-PUBLISH-DEADEND` | P1 | `CLOSED` | `Publish` 同时支持冻结的 `mergePolicy=never|policy`；`policy` 必须绑定 `eligible-after-policy` ReviewDecision，生成同策略的 PublicationIntent/PublicationRecord 并停留于 `CI_PENDING`，只有 `publication.merged` 可进入 `ACCEPTED`。 |
| `ADR0032-B2-AUTHORIZATION-BYPASS` | P1 | `REOPENED`（拆分见下） | 精确绑定与 mutation 前 recheck 仍有价值，但同故障域 sidecar 不能证明整体未回滚，authorization→intent 分步写也不是原子 authority fact，不能据此宣称 crash window 已关闭。 |
| `ADR0032-B2-BASE-BRANCH-UNBOUND` | P1 | `CLOSED` | `SCMMergeTarget` 同时携带 `baseBranch` 与 `baseOid`；fresh admission、ObserveReady recovery 以及每一次 ready/merge mutation 的紧邻 preflight 都重新观察并对照 repository、PR、head、base branch/base OID、Marker 与 Draft 状态，任一漂移在 mutation 前进入 `BLOCKED`。 |
| `ADR0032-B2-CHECK-EVIDENCE-ORPHAN` | P1 | `CLOSED` | fresh `RemoteCheckRecord` 以 canonical digest 为文件名持久化不可变 bytes；恢复与 C7 重建重新校验 Schema、重算 digest，并核对 task/run/repository/request/head/status/requiredChecks，缺失或篡改不得收敛。 |
| `ADR0032-B2-UNBOUNDED-MERGE-FAILURE` | P1 | `REOPENED`（拆分见下） | 本地 attempt/result 记录限制了正常路径重试，但同故障域整体回滚可重置计数，attempt→result 两阶段 crash 也不能安全区分 applied/not-applied；需 journal-bound pending anchor 与 Inspect/Reconcile。 |

| 新 ID | 级别 | 状态 | 问题与关闭条件 |
| --- | --- | --- | --- |
| `ADR0032-B2-AUTHORITY-ROLLBACK-DOMAIN` | P1 | `OPEN` | authorization/intent 与 sidecar head 可协调回滚；由 ADR 0033 `MergeAuthorityTransaction` 同 journal 原子事实关闭本地分步窗口，production supported 还必须等待 M11 external rollback witness 与协调回滚恢复演练。 |
| `ADR0032-B2-DELIVERY-CRASH-WINDOW` | P1 | `OPEN` | delivery attempt/result 分步写存在 unknown 副作用窗口，预算可随同域回滚重置；关闭要求 Core-only pending/fence-consumed/observed/resolved CAS append、fence closed Schema/producer-authority/same-state allowlist、journal/anchor sequence reducer、canonical replay identity 与 crash hydration，且 pending snapshot 后执行 mutation-adjacent current/journal/expiry recheck **AND** single-use fence、fence journal+snapshot durability-before-handoff、revoke/authority append 与 fence→Provider handoff 的同一线性化顺序，以及 unknown/lag 保持 unresolved 并可重复 Inspect（含 concurrent consume、consume→crash→restart no-replay 与 restart lag→receipt-visible fixture）。deadline 后匹配 late receipt 必须原子关闭 pending 并复用 ADR 0026 唯一终态例外收敛 Outcome。Publisher 只提供 typed observation/provenance，不能裁决权威结果。 |
| `ADR0032-B2-RECOVERY-RESIGN` | P1 | `OPEN` | 恢复可能重观察 `requestedAt`/check freshness 并为不同 digest 重签；由 prepared transaction 精确 bytes hydrate、同 identity 不同 digest conflict 关闭。 |
| `ADR0032-B2-EXECUTABLE-SNAPSHOT-TOCTOU` | P2 | `OPEN` | snapshot 路径本身可在校验后被替换；ADR 0033 要求通过已校验 fd/immutable handle 执行同一对象并约束 config dir handle。 |

残留 `#160` 保持 P2 `OPEN`：更通用的共享 outbox/投递观测与运维视图仍应由后续切片处理；它不能替代 ADR 0033 的 merge 专属 authority/delivery journal facts。ADR 0033 未接受且 A–D 未全部实现前，`mergePolicy=policy` 必须保持 unsupported；A–D 通过后最多启用显式 opt-in 的 local/non-production 受限 profile，production supported 仍以 M11 external witness/fence 恢复门禁为前提。

## Issue #137 Qoder live conformance authority 审计增补（2026-08-17）

Qoder production authority wiring 候选提交的独立审计确认四项 P1：生产 trust root 首次改变 Adapter admission 却无 ADR；Seal 用常量替代 verifier 实测 profile；证据只绑定 OS/arch 而可跨 host 重放；一次 Bind 后长寿命进程不再消费撤销。另有父级 symlink/owner 路径边界与无上限 TTL 两项 P2。ADR 0034 据此提出三方 authority、完整 observation、OS-attested host-key identity、24 小时 freshness、逐段 nofollow 和 Probe/launch 逐次复核契约。acceptance 复核又阻止了 hostname 碰撞重放、单一 generation JSON 被删后降级、签名 record/trust rotation 协议未冻结与同 UID 普通 subprocess 冒充 sandbox 四项 P1；目标契约现要求 OS monotonic fence anchor、完整 canonical/domain-separated signature 协议、三角色 key ledger 以及独立 OS principal 的 capability/syscall/path denial。后续复核再发现四份 receipt 无同轮链、receipt 未绑定 OS denial audit、evidence signer 缺 key epoch、operator/provider root rotation 无 anti-rollback continuity、provider advance receipt 未绑定 prepared transaction，以及所谓 exact schema 仍缺 nested type/cardinality；ADR 候选已补充 probeRun chain、IsolationAudit、OS trust-root ledger、transaction/prepared digest 绑定和封闭类型约束。clean-origin 复核又要求 root authorizer 与 record-chain 分离、activate-before-revoke/remaining replacement 续签，以及可机械解析的非敏感 argv/environment manifest 和 document/path/time/epoch 边界；随后复核修正了误拒未来 validUntil、credential digest 离线指纹风险与 generation 首链歧义，候选改为一次性 OS capability identity，并冻结 initialization/每个 prepared/committed 的精确前驱。最终独立复审 P0/P1/P2/P3 均为 0，维护者于 2026-08-18 接受 ADR 0034；接受只关闭合同缺口，不表示真实 evidence 或 production enablement 已完成。

| ID | 级别 | 状态 | 关闭条件 |
| --- | --- | --- | --- |
| `QODER-AUTHORITY-ADR-MISSING` | P1 | `CLOSED-CONTRACT` | 维护者已接受 ADR 0034；当前生产构造器仍无条件 typed fail-closed，接受 ADR 不自动启用，须独立后续变更。 |
| `QODER-LIVE-VERIFIER-PIPELINE-MISSING` | P1 | `OPEN` | 实现受限、只读且无仓库写权限的真实 executable verifier、closed typed observation schema 与独立 signer；其 evidence 必须机械导出完整 argv/environment manifest 并与精确 executable/host/challenge identity 绑定，不能只提交 opaque digest。 |
| `QODER-OBSERVATION-NOT-EXACTLY-BOUND` | P1 | `IMPLEMENTED-PENDING-REVIEW` | observation 逐项携带 suite/artifact/challenge/capability/profile/argv/env/tool/event/protocol/permission/transcript/verdict/time，Seal 不注入期望值；独立复审与真实 probe 证据分别通过。 |
| `QODER-HOST-BINDING-REPLAYABLE` | P1 | `IMPLEMENTED-PENDING-REVIEW` | verifier/evidence/consumer 三方精确 host fingerprint，跨 host fixture fail closed。 |
| `QODER-AUTHORITY-LIFECYCLE-NOT-RECHECKED` | P1 | `REWORKED-PENDING-REVIEW` | 候选 consumer 的 Probe 与 launch guard 每次重读 current config/evidence/revocation/generation；generation high-water 已扩展为 consumer-owned 私有 nofollow root 中的跨进程/重启耐久记录，以 advisory lock 串行化并按 file fsync→renameat→directory fsync 原子提交，绑定完整 config canonical digest，拒绝 rollback/同代替换，且在 missing/revoked evidence leaf 前先消费。终审 rework 进一步同时持有 fence dirfd 与实际解析 leaf 的同一 evidence dirfd，按 device/inode 与双向祖先关系拒绝同目录、路径别名及双向嵌套，并把 lock/record 收紧为精确 `0600`、single-link regular file；完整负向矩阵待再次独立复核，生产仍硬禁用。 |
| `QODER-AUTHORITY-PATH-BOUNDARY` | P2 | `IMPLEMENTED-PENDING-REVIEW` | config/root/evidence 全路径逐段 nofollow、leaf `O_NONBLOCK`+`fstat`、owner/private mode 与 FIFO 负向 fixture 通过。 |
| `QODER-CONFORMANCE-FRESHNESS` | P2 | `IMPLEMENTED-PENDING-REVIEW` | validity window 与 observation age 固定最多 24 小时；超长与陈旧 fixture fail closed。 |

该候选实现与 CI 生成的 Ed25519 key/fake executable 只验证机制，不是 credentialed live evidence；ADR 0034 虽已接受，但当前 host 尚未配置外部真实 evidence，也未完成独立 production enablement，Issue #137 保持打开，Qoder 不得被报告为已完成或当前部署 `supported`。启用序列固定为 ADR 接受后先只落地仍与 consumer 分离的 isolation/receipt/verifier/signer tooling，再产生真实 evidence 并通过负向矩阵，最后以单独变更启用 consumer/registry；调度优先级只能在 required CI、secret scan 与当前 host doctor 全绿后变更。

## Issue #136 Codex production authority 审计增补（2026-08-18）

Codex production eligibility 首次引入 Adapter 本地准入 trust root、可撤销 evidence、consumer generation fence 与 authenticated fd-exec，属于信任边界和耐久契约变更。ADR 0037 候选经多轮独立复审，最终 P0/P1/P2/P3 全部为 0；维护者于 2026-08-18 接受 [ADR 0037](adr/0037-codex-cli-production-authority.md)。合同现冻结 verifier/receipt/evidence/config/launch authority 分权、Worker 零 authority signing key、稳定 TPM-backed `hostIdentityDigest` 与逐次 fresh nonce、单一 active-root-pin/fence 原子状态及每个 fsync/rename 边界恢复、source→sealed→child 合法 topology 转换、逐次撤销复核和 ReviewPacket 精确证据绑定。

| ID | 级别 | 状态 | 关闭条件 |
| --- | --- | --- | --- |
| `CODEX-AUTHORITY-ADR-MISSING` | P1 | `CLOSED-CONTRACT` | 维护者已接受 ADR 0037；接受只冻结合同，不自动启用 production constructor 或 registry。 |

ADR 0037 接受不表示实现、真实 evidence 或 enablement 已完成。Issue #136 与相关 milestone 保持未完成；当前 Codex 部署仍须 production hard-disable，不得报告为 `supported`。Darwin 在等价 authenticated fd-exec 合同由后续 ADR 接受前继续 fail closed。

## Qoder/Codex 共享 Production Authority Provider 审计增补（2026-08-18）

Qoder 与 Codex 的 production consumer 实现复核进一步证明：两个 Adapter 虽已有封闭 evidence/config/consumer 合同，但当前仓库和宿主仍没有可独立 provision 的在线 verifier、外部 OS isolation/audit receipt authority、host attestation/monotonic anchor、stopped-child launch barrier，以及可原子交付 keyset/revocation/config/evidence 的 authority provider。把 fixture、同 UID helper 或若干普通文件直接接到 registry，会让 Adapter/Worker 所在故障域为自己的准入证据和 rollback 状态背书。

[ADR 0038](adr/0038-agent-production-authority-provider.md) 已于 2026-08-18 接受，以独立本机 `AgentProductionAuthorityProvider` Port、外部 principal、held-fd IPC、atomic bundle+monotonic fence 和 Prepare/Commit/Inspect launch barrier 补齐该层。共享仅限基础设施；Qoder/Codex 继续分别运行 ADR 0034/0037 的 exact profile 与 conformance。接受只冻结实现合同，没有把任何当前部署升级为 `supported`。

| ID | 级别 | 状态 | 问题与关闭条件 |
| --- | --- | --- | --- |
| `AGENT-AUTHORITY-PROVIDER-MISSING` | P1 | `OPEN` | 当前缺少与 Marshal/Worker 分离的在线 verifier、Secret direct-delivery、isolation/receipt/evidence/config/rotation/revocation/recovery/launch authority principal 及最小认证 IPC。关闭要求 ADR 0038 被维护者接受，shared Port/peer credential/operation AuthZ/opaque capability handoff/held-fd/role-key separation 实现和负向矩阵通过；同 UID helper、fixture、普通 subprocess 或 `sandbox-exec` 单独不能关闭。 |
| `AGENT-AUTHORITY-ATOMIC-BUNDLE-AND-BARRIER` | P1 | `OPEN` | keyset/revocation/config/evidence 与 high-water 尚无跨 crash 的原子 current bundle；Worker launch 尚无 receipt durable-before-release barrier 与 lost-response Inspect/Reconcile。关闭要求 detached manifest signature、bounded leaf batch、授权 prepare/rotate/revoke/recovery transaction、外部 monotonic anchor、prepared→anchor→committed transaction、协调回滚检测，以及精确绑定 authority namespace/fixed roots/mount namespace 的 stopped-child Prepare/Commit/Abort/Inspect、单次 release 与 kill/wait crash matrix 全部通过。 |
| `AGENT-AUTHORITY-PLATFORM-CONFORMANCE` | P1 | `OPEN` | Linux/Darwin 的强制隔离、host identity、execution identity 与 audit producer 尚未形成真实支持矩阵。关闭要求 Linux Qoder/Codex 分别通过 ADR 0034/0037 全矩阵和非作者真实 credentialed probe；Darwin 在替代强制机制与独立 ADR 通过前保持 `unsupported`，不能以 pathname execution、codesign 摘要或 `sandbox-exec` 降级。 |

## Darwin Codex Mac-first 实施提案（2026-08-19）

为支持当前 macOS 宿主，新增 ADR 0040（Proposed）冻结 Darwin authenticated launcher 的实现边界：独立签名 launcher、Mach-O held identity、child pre-workload barrier、exec-away/exec-back 负向证据与真实 credentialed conformance。该提案不放宽 ADR 0037/0038 的 fail-closed 结论；在维护者接受、真实 evidence、独立 consumer enablement 与 doctor/live probe 全绿前，Codex 仍必须报告 `unsupported`。Linux authority/runtime 延后，不作为 Mac-first 的前置条件。
| `AGENT-AUTHORITY-PEER-EXEC-SWAPBACK` | P1 | `OPEN-IMPLEMENTATION`（ADR 0039 Accepted） | held `/proc/<pid>/exe`、pidfd 与收包前后 pathname sampling 不能排除 `exec-away → send → exec-back`。ADR 0039 已于 2026-08-18 在精确独立复审 P0/P1 清零后接受并冻结合同；关闭仍要求实现 root trusted launcher、USER_NOTIF 只放行一次经 `pidfd_getfd` 验证的初始 held-FD `execveat`、随后永久 exec deny、独立 launch attestation/逐连接 nonce receipt、不可转移 client/server helper 与双向 bootstrap，且真实 Linux 负测必须证明 application handler 调用数为零且无 response。接受本身不关闭该 finding，也不启用 production transport。 |

上述 finding 是 Issue #136/#137 production enablement 的共同前置阻塞，不改变 M10 在途及 M11–M13 `PLANNED` 状态。只有 shared Port conformance 与对应 profile conformance、当前宿主 doctor、撤销/rollback/kill 演练、required CI 和 secret scan 全绿后，才能分别提交 Qoder 或 Codex 的独立 registry enablement 变更。

## Mac 普通用户模式审计（2026-08-19）

用户明确授权先按 Qwen/OpenCode 同级普通用户模式使用 Qoder 1.1.23 与 Codex 0.145.0。实现采用 `MARSHAL_QODER_MODE=ordinary-user` 与 `MARSHAL_CODEX_MODE=ordinary-user` 的显式 opt-in；未设置时严格 authority 路径仍 fail closed。普通模式继续固定 absolute path、realpath、SHA-256、版本、超时、输出、环境、worktree 边界与 WorkerResult 校验，但不宣称 signed authority、APAP credential、child barrier 或恶意代码 sandbox。doctor 输出 `authorityMode=ordinary-user`，因此该能力不会与严格 production authority 证据混淆。

## Darwin APAP transport 实机审计增补（2026-08-19）

当前 macOS 宿主对 `AF_UNIX/SOCK_SEQPACKET` 返回 `protocol not supported`，导致原 APAP client 即使 endpoint 存在也无法连接。实现已加入 Darwin 专用四字节大端长度帧 `SOCK_STREAM` 与 `SCM_RIGHTS` 累积接收，并以实机 payload+held-FD 测试、race、vet、staticcheck 与 Darwin 交叉编译验证。该变更只关闭 transport 可达性缺口；[ADR 0041](adr/0041-darwin-apap-stream-transport.md) 仍为 Proposed，root-owned APAP provider、签名 launcher、独立 verifier、credentialed live probe 与 registry enablement 继续保持 `unsupported`。

## Darwin launchd 部署投影审计增补（2026-08-19）

`internal/darwin` 新增 deterministic root-owned launchd plist 生成器，固定 `com.marshal.apap` label、service binary、signed launcher binary、APAP endpoint、`RunAtLoad`、`KeepAlive`、Background process type 与 owner-only umask，并拒绝相对路径、路径穿越、根路径和 label 注入。该生成器不执行安装、不改变 launchd 状态、不验证或生成签名；真实安装仍要求外部 root provisioning、独立 launcher signer 与 service identity evidence，当前 doctor/registry 不因此升级。

## APAP credential ingress 实现审计增补（2026-08-19）

共享 provider seam 现加入独立 `CredentialIngress` server：只接受已认证 `SecretProvider` peer、精确一个 `credentialCapability` held fd，并在 handler 返回 typed `CredentialIngressResponseV1` 后立即关闭该 fd。APAP control server 不接收 capability，response 不返回 credential bytes 或 capability fd；Mac 实机 roundtrip、race、vet、staticcheck、交叉编译和 secret scan 通过。该实现只提供 transport/protocol custody，不产生 capability、receipt signing、isolation audit 或 credential authority；因此真实 credentialed live probe 与 registry enablement 仍保持 `unsupported`。

## Darwin launchd 配置记录审计增补（2026-08-19）

`internal/darwin` 现提供严格的 `marshal.darwin.launchd-deployment.v1` 配置读取器：配置文件必须是当前用户或 root 所有、owner-only 私有普通文件、RFC 8785 canonical JSON，并通过逐级 `openat` + `O_NOFOLLOW` 拒绝路径组件替换；未知字段、尾随数据、非 root APAP endpoint policy、缺失身份字段和非法路径均 fail closed。该读取器只产生部署预检输入，不携带签名私钥、credential 或 capability，也不安装、签名、bootstrap launchd；因此不能单独改变 Qoder/Codex 的 registry admission，外部 root provisioning、signed launcher、独立 verifier 和 credential authority 仍是未关闭前置条件。

本轮 doctor 增加 `MARSHAL_DARWIN_LAUNCHD_CONFIG` 的非敏感部署状态投影（`not-configured`、`unsafe`、`unavailable`、`ready`）。该字段与 APAP endpoint 状态一样仅供诊断，不能把普通配置文件或缺少 root-owned 对象的预检结果解释为 `supported`。

本轮宿主只读核查还确认：`security find-identity -v -p codesigning` 返回 `0 valid identities found`；`/Library/PrivilegedHelperTools`、`/usr/local/libexec` 和 `/opt/homebrew/bin` 没有现成 Marshal/APAP launcher；`MARSHAL_APAP_*`、`MARSHAL_DARWIN_*`、Qoder/Codex authority 环境变量均未配置。该证据支持当前外部 signer/root provisioning 阻塞判断，但不改变任何 registry 状态。

同轮再次以固定绝对路径读取并执行版本探针：Qoder `/Users/gawain/.qoder/bin/qodercli/qodercli-1.1.23` 输出 `1.1.23`、SHA-256 为 `sha256:b09566c33df68f8ee3e82783120f6eb885fbd9aeb5bc35beb4a85a3ea2d4219a`；Codex `/opt/homebrew/Caskroom/codex/0.145.0/codex-aarch64-apple-darwin` 输出 `codex-cli 0.145.0`、SHA-256 为 `sha256:1da3f4e0e96028b8a771814293c3033dafd1971f943f6c7e79b0897fe705f590`。这只证明 held executable 可读取且版本正确，不构成 authority、隔离或生产准入证据；PATH 中其它 Qoder identity 不得混入该证据。

## APAP launch-control typed slice 审计增补（2026-08-19）

共享 `authorityprovider` 现把 ADR 0038 的 `PrepareLaunch`、`CommitLaunch`、`AbortLaunch` 与 `InspectLaunch` 纳入已注册控制面，并为请求、receipt、release identity、状态和 digest 提供封闭 typed payload 校验。`PrepareLaunch` 强制八项 held-FD 角色、完整 identity/config/fence digest、nonce 与 deadline 绑定；commit/abort/inspect 不接受 credential FD，receipt 必须是 canonical 非空对象，未知状态和成功状态的 receipt 组合均 fail closed。Fake provider 仅用于协议与重放/负向矩阵测试，不执行真实 child release，也不产生 signer、OS isolation audit 或 credential authority。

本切片通过 authorityprovider 定向、race、vet、staticcheck、Darwin/Linux 编译与 `git diff --check` 后，仅关闭 typed protocol 缺口；root-owned launchd/APAP provisioning、独立 signed launcher/verifier、真实 credentialed probe/conformance 与 Qoder/Codex registry enablement 仍保持 `unsupported`。

随后补入 `LaunchCoordinator` 作为可复用的确定性 reducer：prepare/commit/abort/inspect 统一串行化，command replay 精确绑定 request digest 与 peer identity，重复 transaction/错 receipt/陈旧 CAS fail closed，lost response 只能通过 Inspect 收敛。只有 `CommitLaunch` 的 release linearization point 前进 provider sequence；prepare/abort/inspect 不前进。实际 OS stopped-child、kill/wait、receipt signing 与平台 principal 仍由外部 `LaunchEffects` 实现，coordinator 不保存 FD、不读取 credential、不执行 pathname，也不能单独启用生产 registry。

本轮继续补入 provider-owned `DurableLaunchJournal`：记录以 canonical JSONL 追加、`sequence`/`providerSequence` 双序列、CRC + SHA-256 digest chain、严格 transition allowlist、`fsync` durable-before-return 与重启 hydration；非 canonical、尾部截断、链断裂、重复 transaction、状态回退、provider sequence 越界均 fail closed。journal 文件通过逐级 `openat(O_NOFOLLOW|O_CLOEXEC)` 固定路径组件，并校验私有目录/文件的 owner 与 mode；`NewDurableLaunchCoordinator` 在接受请求前从该 journal 恢复 pending/released/aborted 事实，副作用成功但 journal 写失败保持 `launch-outcome-ambiguous`，不能把内存状态当作已提交。

该 journal 仍只是 provider state 的持久化 seam，不能冒充 ADR 0038 所需的外部不可回滚 monotonic anchor、signed receipt authority、root-owned stopped-child launcher、kill/wait 或 credentialed isolation。故本切片关闭了 launch transaction 的本地 crash-hydration 缺口，但 Qoder/Codex 生产 registry 仍保持 `unsupported`，直至外部 authority provision 与独立 conformance 证据齐备。

Darwin stream transport 同步收紧帧边界：发送端拒绝零长度/超 64 KiB payload，接收端在 header 与 payload 两段都拒绝 `MSG_CTRUNC`/`MSG_TRUNC`，不会把丢失的 SCM_RIGHTS 或截断字节当作可验证请求。该改动只增强 ADR 0041 提案中的 framing 负向边界，不改变 `SOCK_STREAM` 的外部 peer authentication、root-owned endpoint、signed launcher、credential ingress 与独立 verifier 前置条件。

新增 [Mac-first authority 交接清单](mac-first-authority-handoff.md)，把必须由宿主管理员提供的 OS principal、签名 launcher、root-owned launchd、credential ingress、不可回滚 anchor 及 profile-specific live probe 逐项列出，并提供只读核验命令。清单不包含私钥或 credential，也不把交接材料当作 registry enablement；当前宿主缺少这些外部对象的结论保持不变。

本轮新增 `scripts/macos-authority-preflight.sh` 只读预检，将固定 Qoder/Codex 可执行文件、签名 launcher、受管 Team ID、root launchd、codesigning identity 与非交互 sudo 等外部前置条件转换为稳定的 `PASS`/`BLOCKED` 输出和非零退出码。脚本拒绝 ad-hoc 签名，且不安装、签名、bootstrap、读取 credential 或修改 `.marshal/`；本机实测两个 CLI 文件存在，但外部 authority 前置条件仍为 `BLOCKED`，因此 registry 与 doctor 继续保持 `unsupported`。

预检进一步要求 APAP service、launcher 与 endpoint socket 由 root 持有且禁止 group/other write；仅“文件存在”或“launchd label 存在”不再被视为部署就绪。该检查仍是只读输入，不改变 registry admission。

本轮收紧预检的 executable identity 门禁：固定 Qoder/Codex 候选必须分别通过精确 `--version` 与 SHA-256 比对，默认绑定当前 Mac 上已核验的 `qodercli 1.1.23` 与 `codex-cli 0.145.0` 摘要；管理员若提供同版本的不同构建，必须显式覆盖摘要并重新取得独立 verifier/conformance 证据。该检查仍不签名、不安装、不读取 credential，也不把版本/摘要通过误判为 authority 或 production support；PATH 中的同名候选继续与 held identity 隔离。

同一预检切片进一步核对 `launchctl print` 的实际 service 投影，要求其中同时出现精确 APAP service 与 signed launcher 路径；仅 label 存在、或 label 指向错误二进制时均保持 `BLOCKED`。该检查仍只读，不执行 bootstrap 或 registry enablement。

最终宿主审计（2026-08-19）仍返回 Qoder/Codex identity 两项 `PASS`，其余 13 项 `BLOCKED`：APAP service、signed launcher、两者 root/private ownership、codesigning identity、两者 signature、两者 managed Team ID、root/private endpoint socket、root launchd label、launchd exact service+launcher binding、noninteractive sudo。`security find-identity -v -p codesigning` 返回 `0 valid identities found`，三个固定系统对象均不存在。该结果满足外部阻塞判据；在管理员提供受管 signing identity、root-owned launchd/APAP、credential authority 与独立 verifier/conformance 证据前，Qoder/Codex registry 必须继续 `unsupported`。

随后将 CapabilitySnapshot 中已有的 `executableDigest` 透传到 `marshal doctor --json` 的 Worker 投影，并以 Qoder/Codex 的支持态单测锁定该字段。doctor 只输出摘要，不输出 executable 路径、环境值或 credential；该字段用于审计精确身份，不能单独改变 `unsupported`/registry admission。

本切片将 `internal/darwin` 的严格 held-executable 观察抽象为 launcher 与 Qoder/Codex candidate 共用的 `OpenHeldExecutable`/`OpenHeldCandidate`。路径逐级通过 `openat` + `O_NOFOLLOW` 固定，外部 authority 可通过 `Duplicate` 取得同一 inode 的 SCM_RIGHTS 描述符，而原始 held descriptor 继续由 owner 持有；包内不提供 pathname exec 或普通子进程 fallback。该 seam 只关闭 candidate descriptor 交付与父路径替换缺口，不产生签名 receipt、OS isolation 或 registry enablement。

## Qoder ordinary-user 结果传输与 denial 证据审计增补（2026-08-19）

Qoder 1.1.23 的真实 Mac ordinary-user smoke 暴露三项实现缺陷：旧隐藏 symlink 别名会被 Provider 的路径安全检查拒绝；stream-json parser 丢弃 `assistant/tool_use` 与 `user/tool_result`，导致权限拒绝未进入 ADR 0013 denial log；ordinary-user CapabilitySnapshot 仍沿用 strict managed HOME/config 文案。旧测试同时直接向 control result path 注入 WorkerResult，形成 transport 假阳性，因此此前的 fixture 通过不能证明真实别名可写。

本切片把 Qoder adapter 升为 `0.1.1` 并更新 event contract：WorkerResult 改为 worktree 内非隐藏 single-link regular staging file，与 control result 始终为不同 inode；adapter 通过 held worktree dirfd、`openat(O_NOFOLLOW)` 与 exact-inode consume/unlink/fsync 收取有界声明，再写入 held control leaf，Worker 从不取得 control inode 的 pathname 或 hard-link capability。prompt 只允许一次 `Bash tee`，拒绝后不得换工具重试；parser 以 tool-use ID 关联结果，并按 9 份真实 Qoder 1.1.23 transcript 冻结大小写敏感工具词表与 string content shape，完整记录 toolCalls/toolNames/permission denial；fatal denial 固定为 typed do-not-retry，协议冲突固定为 `protocol-invalid/do-not-retry`。denial log 经 held output dirfd claim/write，并与 transcript metadata 的 benign/fatal 计数双向 fail-closed 校验。ordinary-user 能力说明同步为真实的宿主 `HOME/XDG` allowlist 继承，不再声称 managed config 或禁用宿主配置源。

| ID | 级别 | 状态 | 关闭条件 |
| --- | --- | --- | --- |
| `QODER-ORDINARY-RESULT-DENIAL-P1` | P1 | `IMPLEMENTED-REVIEWED-PENDING-LIVE-SMOKE` | 独立 reviewer 已确认 P0/P1/P2 均为 0；真实 Qoder 1.1.23 仍须只经新 staging transport 完成 fresh current-main smoke，并复核 denial fixture、protocol 关联、held-fd attack matrix、metadata/log 一致性与 current-main 身份后才可置为 `CLOSED`。 |

该修复不新增 authority、credential、sandbox 或发布权限，不改变 ADR 0042 的降级边界；严格 authority 的旧 `adapterVersion/eventContract` 证据因版本变化自动失效，必须重新取得独立 conformance evidence，不能迁移旧摘要。

后续真实 ordinary-user smoke 发现：Qoder 成功读取的源码正文可能包含 permission marker；若 parser 扫描任意 `tool_result.content`，普通文件字节可伪造 denial 并中止 Attempt。修复后只接受同一事件内按 `tool_result_meta.id` 精确绑定的 `permission-rule`，或仅含一个 `tool_result` 时无歧义的 `tool_use_result.isHardFailure=true`；duplicate、orphan、unknown kind 与多结果 hard failure 均 fail-closed。由于 denial authority 语义发生变化，Qoder adapter 升为 `0.1.2`，event contract 升为 `qoder-stream-json-1.2.0-v3`，使旧 `0.1.1/v2` conformance evidence、suite digest 与 authority bundle 自动失效。该变更不扩大普通用户模式权限，也不把它描述为 hardened authority。

tee-last/held-inode 修复的后续独立复核又发现一项 P1：WorkerResult transport 已从 Worker 可寻址 staging 改为 launch 前 unlink 的 Adapter-held inode，但 conformance identity 仍停留在 `0.1.2/v3`，旧 evidence 因而可能授权未验证新 transport 的运行。修复候选把 Adapter 升为 `0.1.3`、event contract 升为 `qoder-stream-json-1.2.0-v4`，新增确定性 `workerResultTransportDigest` 并将其纳入 suite、live observation、每份 execution receipt、普通 evidence 与 ADR 0034 exact evidence consumer。digest 封闭绑定 staging basename/type/mode、`O_EXCL|O_NOFOLLOW|O_CLOEXEC`、launch 前 unlink 与 `nlink=0`、Worker 不获得 path/fd、staging/control 不同 inode、held dirfd/exact-inode commit/consume/cleanup、唯一 canonical Bash input/command、exactly-once tee-last、denial extractor 和 transcript contract；逐字段 mutation 都必须同时改变 transport/suite digest并被当前 consumer 拒绝，正确重签的旧 `0.1.2/v3` evidence 也必须 fail closed。该代码关闭候选仍待独立 reviewer；无论代码评审结果如何，Mac ordinary-user 必须为 `0.1.3/v4` 重新取得真实 conformance，旧 evidence、摘要和历史 smoke 结论均不得迁移，也不得把 ordinary-user 描述为 hardened authority 升级。

2026-08-20 的首个 `0.1.3/v4` Mac ordinary-user smoke 又发现一项 P1 compatibility 缺口：真实 Qoder 1.1.23 的 final Bash `tool_use.input` 是 canonical `{command,description}`，其中 `description` 是 Provider 自动附加的非执行 metadata；v4 fixture 只伪造 `{command}`，导致真实唯一、成功且最后的 tee 被误判成 `invalidAccess`。修复候选升级为 Adapter `0.1.4` / event contract `qoder-stream-json-1.2.0-v5`，只接受 canonical `command` 与非空、合法 UTF-8、最多 512 bytes 且无控制字符的 `description`，继续拒绝缺字段、未知第三字段、非 canonical JSON、变体 command、重复 tee 与 post-tee tool call；真实 transcript 的脱敏 shape 加入回归。transport/suite digest 与所有 consumer 因 identity bump 自动失效旧 `0.1.3/v4` evidence。该 smoke 还暴露输出父目录不存在时 Worker 会用 `ls`/`mkdir` 进行确定性环境探索；Skill 已要求受限 Bash 任务在零 Attempt preflight 机械证明每个 deliverable 父目录存在，否则先改用已有目录或修输入。本项在独立 review、定向/race/static/schema/secret/merge-tree 门禁及 fresh v5 Mac smoke 通过前不得置为 `CLOSED`，也不得把 ordinary-user 描述为 hardened authority。

同日后续真实 `0.1.4/v5` Run 又暴露一项 P1 consumer-envelope 缺口：Qoder final tee 已声明完整语义字段和精确 `taskId/runId/attemptId/adapter.id`，但 Provider 省略了由 Adapter 持有的 `adapter.executable` 与 `adapter.version`；旧 consumer 在覆盖这两个字段前先做完整 Schema 校验，因而把有效声明误归类为 `result-missing`。修复候选升级为 Adapter `0.1.5` / event contract `qoder-stream-json-1.2.0-v6`，把 consumer policy 纳入封闭 WorkerResult transport digest：只允许 Adapter 在完整 Schema 前从 held executable identity 覆盖 `adapter.executable/version`，不合成任何语义字段或 task/run/attempt/adapter ID；重复字段、未知字段、缺失语义、身份漂移和其它无效声明仍 fail closed，并固定归类 `protocol-invalid/do-not-retry`。transport/suite、普通 evidence、ADR 0034 exact evidence consumer 与 transcript attestation profile 全部随新 identity 失效旧 `0.1.4/v5` evidence/receipt，必须补 fresh v6 Mac ordinary-user conformance。该变更属于 ADR 0016、0034、0042 已冻结的版本化 Adapter compatibility 演进，不改变信任边界、持久化、生命周期或发布权限，因此不新增 ADR；在同一独立 reviewer 关闭真实 diff 的 P1 且定向/race/static/schema/secret/merge-tree 与 fresh v6 Mac evidence 通过前，不得宣称 production ready。

同日 `0.1.5/v6` 的 fresh Mac ordinary-user Run 又暴露一项 P1 stream compatibility 缺口：真实 Qoder 1.1.23 会把同一个 assistant message（相同 `message.id`）分成 thinking、text 和多个独立 `tool_use` frame；旧 parser 把每个 frame 当成互斥消息，因而在第二个合法工具调用到达时错误归类为 `protocol-invalid/do-not-retry`。修复候选升级为 Adapter `0.1.6` / event contract `qoder-stream-json-1.2.0-v7`：同一 message ID 可累计不同 tool-use ID，完全相同的 ID/name/canonical input 重放只折叠一次；ID 在关闭前变化或消失、同 ID 冲突重放、未知/非法 `stop_reason`、结果早于 `tool_use` 关闭、终态仍有未关闭 message 或 final 后追加 frame 均 fail closed。transport/suite、普通 evidence、ADR 0034 exact evidence consumer 与 transcript attestation profile 随 identity bump 自动拒绝旧 `0.1.5/v6` evidence，必须补 fresh v7 Mac ordinary-user conformance。该变更仍属于 ADR 0016、0034、0042 已冻结的版本化 compatibility 演进，不改变信任边界、持久化、生命周期或发布权限，因此不新增 ADR；独立 review 与 fresh v7 live evidence 完成前不得宣称 production ready。

## Issue #138 Verifier worktree mutation 审计增补（2026-08-18）

Python acceptance command 生成 `__pycache__/*.pyc` 的 dogfood 证明：旧 Verifier 虽能在命令后观察到 Candidate worktree 变化并把 Gate 标为失败，却让污染字节留在受管 worktree；随后 Review 的 current-observation guard 正确拒绝变化后的字节，Run 因而无法生成绑定原 Candidate 的 ReviewPacket。该问题不是新生命周期或 Schema 缺口：ADR 0027 已冻结 command 写作用域默认为 `none`、未声明写入 fail closed、Candidate 与 Evidence 不可被覆盖；缺失的是实现级 command 隔离。

| ID | 级别 | 状态 | 关闭条件 |
| --- | --- | --- | --- |
| `ISSUE138-VERIFIER-WORKTREE-MUTATION` | P1 | `IMPLEMENTED-PENDING-INDEPENDENT-REVIEW` | 每条 candidate/Baseline command 使用 fresh standalone 临时 Git 副本；原 Candidate 复制前后身份一致；缓存与临时目录定向到副本外的命令专属临时根；submodule、特殊文件、逃逸/悬空/循环 symlink 在启动前拒绝；副本 before/after digest 不同形成稳定 `verifier-worktree-mutated` fail-closed Gate，同时保留原 command exit/signal/log；命令 pass/fail/cancel 后原受管 worktree 与 Candidate/patch digest 保持不变且 ReviewPacket 可重建；定向、race、全量 quality 与独立审查 P0/P1 清零后方可置为 `CLOSED`。 |

该切片不新增 TaskSpec temporary-directory 字段，不改变 VerificationReport/ReviewPacket Schema、Run 状态、doctor/status、发布权限或 ADR 0027 normalizer 规则，也不把普通宿主临时目录描述为 hardened sandbox。

## Issue #53 CI 失败 rework 注入设计缺口审计增补（2026-08-14）

公开 [Issue #53](https://github.com/chiga0/marshal-harness/issues/53) 要求为 CI 失败 rework 闭环冻结可恢复、可审计且无双计数的契约。基线（main commit `981b53d`）行为定位：PublicationRecord 已建立且 Run 位于 `CI_PENDING`；RemoteCheckObserver 产出 `status=fail` 的 RemoteCheckRecord；`publication.checks-failed` 当前只把 `headSha` 写入事件并进入 `REWORK_REQUESTED`；execution 的 CI origin 分支返回空 findings；`task review` 仅接受 `REVIEW_PENDING`——既无法把失败检查的权威证据绑定到新 Decision，也无法把精确 `requiredOutcome` 投影给下一 Attempt；fail 分支预算守卫也只检查 rework round、不检查 attempt 余额。

| ID | 级别 | 状态 | 关联 | 问题 | 定位 |
| --- | --- | --- | --- | --- | --- |
| `ISSUE53-CI-REWORK-EVIDENCE-P1` | P1 | `OPEN`（目标契约草案已提出（ADR 0030，Proposed，待接受）；实现待后续 successor） | [Issue #53](https://github.com/chiga0/marshal-harness/issues/53) | CI 失败闭环缺少 typed evidence 身份、ReviewDecision 绑定入口、findings 投影与预算终态/重放语义，rework 在无证据、无决策留痕下进行 | **契约级缺口**：缺的不是数据可得性（RemoteCheckRecord 已携带全部失败事实），而是承载它的契约——由 [ADR 0030](adr/0030-ci-failure-rework-evidence-and-injection.md)（Proposed，草案已提出/待接受）给出目标契约（接受后冻结）：一等不可变 `CIFailureEvidence`、ReviewPacket typed CI 扩展与 ReviewDecision 绑定、`review.rework` 的 `ci-checks-failed` 命名自环、双预算守卫与 `publication.checks-rework-budget-exhausted` 封闭终态、execution lineage 精确消费、canonical digest/事务/重放规则。不放宽任何信任边界：Worker 不自证、Provider/Publisher 不宣布 ReviewDecision、Merge 仍禁用、不改变 ADR 0028 ciDeadline/completedAt 裁决 |

已否决的前置方案（反例结论由任务上下文提供，未读取任何历史 Run 内容）：上一轮实现尝试经 Review 正式 REJECTED——它只试图在 CLI/reducer 内允许 `REWORK_REQUESTED` 自环，未冻结 typed CI evidence、RemoteCheckRecord 摘要绑定、execution lineage 消费与 attempt budget 终态，且双计数/重放语义不明确。后续 implementation successor 不得复制该补丁。

关闭条件（全部满足后 `ISSUE53-CI-REWORK-EVIDENCE-P1` 方可置为 `CLOSED`）：

1. 维护者接受 ADR 0030（ApprovalRecord，接受只冻结契约，不提前升级实现状态）；
2. implementation successor 按 ADR 0030 实施切片合入（schemas/catalog、publication observation、lifecycle/replay/runstore、review importer/packet、execution prompt lineage、CLI 与 doctor）；
3. ADR 0030 测试矩阵全部通过（typed evidence happy path、每个 binding mismatch、mixed/duplicate checks、self-loop cardinality 与 lost-response、conflict、两类预算耗尽终态/Outcome、counter 两轮示例、未注入拒绝、prompt 精确消费与 operational retry、snapshot 丢失/落后、Rebuild/doctor repair、崩溃注入与旧记录兼容）；
4. 独立 Verification/Review 与全仓库 CI 无回归；确认 Merge 仍禁用、ADR 0028 裁决与 RemoteCheckRecord Provider 事实所有权未被改变。

后续 implementation successor 记录：本增补只做契约设计与审计定位，不实现代码/Schema；implementation successor 由维护者另行创建 TaskSpec，其范围以 ADR 0030 实施切片与测试矩阵为准。本增补不改变权威 [Roadmap 状态](roadmap-status.md)：M7–M9 `PASSED`、M10 在途、M11–M13 `PLANNED`；不改变 Local MVP `APPROVED_FOR_IMPLEMENTATION` / `USABLE` 结论，也不改变任何已冻结的信任边界、持久化契约或发布权限。

## Adapter 结构性失败准入审计增补（2026-08-20）

Core 曾忽略 `AdapterFailure.retryDisposition`：即使 Adapter 已把 `protocol-invalid` 或 `provider-terminal` 标为 `do-not-retry`，`execution` 仍只按剩余预算进入 `RETRY_PENDING`，导致同一 Run 启动没有信息增益的下一 Attempt。修复后，Core 统一消费 typed disposition：`retryable` 继续受双预算约束，`blocked` 与 `do-not-retry` 立即进入 `BLOCKED`，生成 Outcome，并把封闭 `adapterId`、`failureKind`、`retryDisposition` 与 `failureSignature` 记录到 append-only `worker.failed`。唯一分类表固定为 `quota-exhausted → blocked`，`rate-limited`/`dns-failure`/`connection-failure → retryable`，`protocol-invalid`/`result-missing`/`provider-terminal → do-not-retry`；Qwen、Qoder 与 Codex 的 `result-missing` 都不会启动第二个 Worker。WorkerResult Schema/身份错误由 Core 明确转换为 `protocol-invalid/do-not-retry`。按已接受的 [ADR 0036](adr/0036-adapter-run-boundary-fail-closed.md)，`Adapter.Run` 返回的普通 error 固定转换为 `protocol-invalid/do-not-retry`，legacy `port.Permanent` 固定转换为 `provider-terminal/do-not-retry`；只有合法 typed `retryable` 才能授权下一次 operational retry，Core 不解析 provider 自由文本推断终态。

Core 入场不再直接信任第一个 `errors.As` 命中：错误图必须恰有一个具体 `AdapterFailure` carrier，并重新经过闭合构造器校验 Adapter/kind/disposition、hint 正值/24h 上界/互斥与时间窗口。未知枚举、非法配对、Adapter identity 错配、自定义 `As` 投影或 joined graph 多 carrier 都固定降级为安全的 `protocol-invalid/do-not-retry`；原始 wrapper/cause 中的自由文本、路径、credential 与控制字符不进入返回错误、事件或 Outcome。崩溃恢复会从持久事件重建同一 normalized failure，并重新校验 safe summary、hints、终态 reason 与 signature，持久字段篡改一律 fail closed 且不重启 Worker。

`failureSignature` 排除 Run/Attempt/时间身份，但绑定 `baseSha`、`specDigest`、`policyDigest`、完整 `capabilityDigest`、Adapter 与 typed failure；因此相同冻结输入可得到稳定审计键，而 executable、协议/transport authority 或其它 CapabilitySnapshot 内容变化会通过完整 capability digest 使旧键失效。崩溃恢复会把该签名重新绑定到 append-only `planning.inputs-frozen` 权威、blocked snapshot、terminal reason 与 quarantine transaction；不匹配时 fail closed，且不会再次启动 Worker。

本切片不建立 Core 全局跨 Run deny-list，也不声称实现跨 Run 自动查重。跨 Run 自动拒绝需要先冻结全局索引、失效、并发、保留期与人工解除的持久化/生命周期权威，属于后续 ADR 范围。当前变更落实 [ADR 0019](adr/0019-deterministic-control-plane-typed-execution-and-goal-admission.md) 的 typed failure 映射，并由 [ADR 0036](adr/0036-adapter-run-boundary-fail-closed.md) 显式冻结 `Adapter.Run` 无类型错误的终态解释、迁移与回滚规则；状态集合、事件/Schema 字段、信任边界和发布权限不变。独立 reviewer 提出的 `ADAPTER-RUN-UNTYPED-LIFECYCLE-ADR-P1` 已通过新增 ADR 0036、删除“旧 Adapter 默认可重试”的陈旧口径并同步 Operator Runbook 关闭；代码仍限于精确 `Adapter.Run` 返回边界，Core 其它 `recordFailure` 来源保持原语义。

## Issue #25 发布合并后 head reconcile 审计增补（2026-08-12）

公开 [Issue #25](https://github.com/chiga0/marshal-harness/issues/25) 与 [PR #24](https://github.com/chiga0/marshal-harness/pull/24) 暴露：当全部 required checks 成功且 PR 已合并进入 main 后，现有 `marshal task accept` 仍要求 PR 处于 OPEN/Draft，会把 Run 永久置为 terminal `BLOCKED`。head branch 删除不是权威 head SHA 丢失——GitHub PR 节点在 merge 后仍保留原 head OID、base OID 与 merge commit。本节记录该问题的审计定位，区分 implementation bug 与 contract gap。

| ID | 级别 | 状态 | 关联 | 问题 | 定位 |
| --- | --- | --- | --- | --- | --- |
| `PUBLICATION-MERGED-HEAD-RECONCILE-P1` | P1 | `CLOSED` | [Issue #25](https://github.com/chiga0/marshal-harness/issues/25) / [PR #24](https://github.com/chiga0/marshal-harness/pull/24) | 已合并 PR 的 head 与 merge commit 缺乏权威 reconcile 路径，`accept` 无法识别 merge 完成态，Run 被永久置为 `BLOCKED` | 分两层：**nonterminal implementation bug**——`accept` 以 PR OPEN/Draft 为前置条件，未区分“merge 前需 OPEN”与“merge 后已合并”两种语义，是可修复的实现缺陷；**terminal contract gap**——当前 Schema/命令缺少不可变 `SCMMergeReceipt` 与 append-only `PublicationReconcileRecord`，无法把已合并 Run 从 `BLOCKED` 安全迁移到 `ACCEPTED`，属契约级缺口，需新 ADR 定义 |

正式处置见 [Operator Runbook §7](operator-runbook.md)。处置进展（2026-08-12）：ADR 0026 已接受并合入 main（PR #49），冻结 `SCMMergeReceipt` 与 `PublicationReconcileRecord` 契约，契约层关闭。处置进展（2026-08-13）：实现层已合入（[PR #106](https://github.com/chiga0/marshal-harness/pull/106)）——`marshal task accept` 活路径内联识别已合并且 required checks 全绿的 PR 并采集不可变 `SCMMergeReceipt`；补偿命令 `marshal task reconcile` 以 ADR 0026 冻结的 `SCMMergeReceipt` + append-only `PublicationReconcileRecord` 与 current-ledger recheck 共同门禁，把发布后的 terminal `BLOCKED` 安全迁移 `ACCEPTED`（`publication.reconciled` 事件，lifecycle 唯一命名终态例外），全程 append-only、幂等、fail closed，不绕过 required checks 与 ReviewDecision。契约层与实现层均已关闭，本 finding 状态更新为 `CLOSED`；不改变 Local MVP `APPROVED_FOR_IMPLEMENTATION` / `USABLE` 结论，也不改变任何已冻结的信任边界、持久化契约或发布权限（merge-never 不变）；`doctor --repair` 依旧只修复 snapshot 损坏，不能改变业务终态。

## 2026-08-20：Qoder transcript attestation 固定 Marshal 执行身份

Qoder v7 operator-local transcript attestation 不再构建或复制随机临时 checker。生产语义校验现由固定 `marshal` 二进制的隐藏 `internal qoder-transcript-check --attestation-ready` 子命令承载；Python validator 只接受用户显式传入的 absolute canonical Marshal 路径，并在发送 evidence 前绑定 held device/inode/raw SHA-256、Mac PID/CDHash 或 Linux `/proc/PID/exe` 身份、含握手参数的内部命令摘要、独立 Marshal build commit/version 与七项输入摘要。进程身份核验完成后才发送单字节 NUL ready token 和 canonical envelope，缺失、错序或非 NUL token fail closed。Attempt `subject.sourceHead` 和 checker build `marshal.sourceHead` 是两个独立字段，不互相替代；实际发送给内部命令的 canonical envelope raw SHA-256 由 Python 与 Go 双端精确比对。子进程采用等价 `env -i` 的封闭环境，有界 stdin/stdout/stderr、deadline 与 owned-process 回收；任何路径、build、stdin 或进程身份漂移继续 fail closed。Receipt shape 新增 Draft 2020-12 Schema/template 并把 framing 升为 `marshal-transcript-attestation-v3`，历史 v2/v5/v6 receipt 保持只读且不可迁移。

本切片不新增 ADR：它只替换既有 `mac-ordinary-user-operator-local` pre-review gate 的 executable delivery，未改变 Core 生命周期、持久化契约、发布权限、authority claim 或 Qoder transcript machine semantics。`internal` 子命令不出现在普通用户 help 中，也不写 `.marshal` 或改变 Run 状态。

## 2026-08-21：Mac-first Adapter 当前证据与未决问题

本次审计以 local main `16c18546dd771cbafc46d10a84bb447b590083e4` 为权威基线，`origin/main` 为 `91186161c734ceff4831d3f03e8734c0a24f36fd`，远端同步尚未完成（`pendingRemoteSync=true`）。本节只记录事实，不把局部 ordinary-user 证据外推为 production authority 或 Milestone 完成。

| 识别项 | 级别 | 状态 | 当前事实与关闭条件 |
| --- | --- | --- | --- |
| `QODER-MAC-1.1.27-LIVE-SMOKE` | P1 | `OPEN` | Qoder `1.1.27` 固定 executable 已通过 doctor registry/compatibility probe，digest 为 `sha256:fd36420ae0e740f7f3fb7f62e9df23aa70df400aad55fc7e7e48e0edc0ce8e2`。仍需用同一绝对路径完成 fresh live Worker、transcript attestation、WorkerResult 与独立 conformance；在此之前不得宣称 production ready。 |
| `QWEN-MAC-ADMISSION` | P1 | `OPEN` | Qwen `0.21.11` 的本地 `--version` 可执行，但当前 `marshal doctor` 为 `unsupported/unprobed`，认证命令已移除且未形成可绑定的认证选择器/凭据证据。需只读 `/doctor`/认证探针形成新鲜 `supported` capability；禁止静默降级启动。 |
| `CODEX-SMOKE-SPEC-COPY` | P2 | `NON_BLOCKING` | R19/R20 已独立复审并 `ACCEPTED`，运行证据无 P0/P1；TaskSpec 仍引用 `r15` 路径，Markdown 产物的 `mediaType` 标注不准确。后续 successor 一次性修正文案，不重做已通过运行。 |

R19/R20 的共同边界：它们证明 Codex `0.145.0` macOS `ordinary-user` Worker 的路径、digest、session、transcript、WorkerResult、verification、artifact、candidate、scope 与 base 绑定；不证明 hardened authority、Linux authority、sandbox 或远端发布。当前 watchdog 暂停新 Worker 调度；容量恢复时也必须先满足 fresh provider signal、scope 互斥、独立 worktree 与 admission receipt。

本次文档增补不改变信任边界、持久化契约、生命周期或发布权限，因此不新增 ADR；也不关闭 `AGENT-AUTHORITY-*`、Qoder live conformance、Qwen admission、Issue #53/#138 等既有开放项。

## Issue #212：Marshal Darwin 自身执行身份阻塞（2026-08-26）

[Issue #212](https://github.com/chiga0/marshal-harness/issues/212) 记录：基线 `5391b466dbb046c78411b1a491adcd81ea6d5900` 构建的固定 Marshal Mach-O 为 ad-hoc signature、`Identifier=a.out`、无 Team ID，`spctl --assess --type execute` 返回 rejected；`version --json` 与 `task scaffold` 均约 10.8 秒后以 exit 137 终止且无 stdout/stderr。故障时宿主 memory pressure 与 CPU 状态正常。exit 137 证明进程收到 `SIGKILL`，但具体发出者仍需部署者用宿主安全日志按时间、PID/CDHash 归因；本报告不把未归因信号直接等同为 Gatekeeper 或某一 EDR 产品结论。

| ID | 级别 | 状态 | 当前事实与关闭条件 |
| --- | --- | --- | --- |
| `MARSHAL-DARWIN-SELF-IDENTITY` | P1 | `CONTRACT-ACCEPTED/EXTERNAL-OPEN` | 固定 pathname 已消除随机 helper，但当前 build/install/release 没有稳定受管签名身份、安装收据/current high-water 或 CLI pre-mutation gate，所有产品 CLI 生命周期仍被阻断。ADR 0047 已在唯一 aggregate rework 后经独立 reviewer `ACCEPT` 且 P0/P1/P2=0，于 2026-08-26 [Accepted](adr/0047-marshal-darwin-self-identity-and-release-signing.md)；接受仅冻结三类 profile、外部 certificate/allowlist/current authority 前置、receipt/trust anchor 与 release/deployment signer 分权合同。实现与外部 provision 仍 OPEN，当前 Keychain 仍无有效 code-signing identity。关闭必须同时满足：部署者 provision certificate/企业 allowlist、外部不可回滚 current/high-water、新鲜 policy observation 与不同 principal/key 的 artifact/release、deployment/install signer；固定安装对象连续执行纯进程内 `version`、bootstrap `doctor --self`、完整 `doctor`、`task scaffold` 无逐次人工批准；binary/receipt/current/path/policy 漂移 fail closed；真实 R3-D scaffold/plan preflight 与独立审查通过。`spctl accepted`、ADR 接受或代码存在任一单项都不足以关闭。 |
| `MARSHAL-BUILD-INPUT-ATTESTATION` | P1 | `CONTRACT+AMENDMENT-ACCEPTED/IMPLEMENTATION-OPEN` | Issue #212 第四个候选的独立审查证明 `HEAD/status/HEAD` 只观察 Git 元数据，不能绑定 Go 实际读取的 ignored `.go`/embed，且没有关闭 provenance 后 mutation 与 linked worktree `.git`/`gitdir`/`commondir` ABA；build 后再次 `git status` 也不充分。ADR 0048 原合同在 sourceHead `5de09997f5260c672f297496290b567815162bb1` 经独立 reviewer 复审 `ACCEPT`（P0/P1=0）并随 PR #215 合入后由维护者接受；后续 amendment 在 sourceHead `b76a53007ba6a07a3bd944fb34d496c47befb289` 经同一独立 reviewer 聚合返工复审 `ACCEPT`（P0/P1/P2/P3=0）后由维护者接受，补齐 compile-root object、authenticated build-record carrier、shared code-sign identity/observation 与跨对象相等关系。接受不关闭实现缺口；关闭仍需要 production producer hostile matrix、schema/validator、protected build→sign→install 最短纵切、外部 provision 与独立 reviewer P0/P1=0。Accepted 文档、纯函数测试或 40-hex `sourceHead` 任一单项均不构成关闭，也不表示 R3-D/E/F 完成。 |
| `MARSHAL-DARWIN-LOCAL-DOGFOOD` | P1 | `CONTRACT-ACCEPTED/IMPLEMENTATION-OPEN` | ADR 0051 已基于提案 sourceHead `e38a94887352cd0ba00f7c7183209d6a6a3ef339` 的独立 reviewer `ACCEPT`（P0/P1=0）于 2026-08-27 接受，并在接受同步的唯一 aggregate rework 中关闭取代范围 P1：新增显式 `darwin-local-dogfood`，只允许 trusted single-user、固定对象、ordinary-user/workspace-write/non-production、`publication:none` 的本地生命周期；profile/identity 必须进入冻结 lineage，Publisher、Forge、credentialed SideEffect、remote Provider、production evidence 与 artifact 晋升全部拒绝。ADR 0051 仅对 local profile 部分取代 ADR 0047 §1/§2/§3.2/§3.3/§3.5/§6 的冲突条款，保留 canonical fixed regular file 原则和 managed/release 全部门禁；local-exec viability 仍是 R3 pre-CLI，ADR 0047/0048 完整 producer/sign/install/current/high-water/notarization 仍是 R6/release gate。实现关闭仍需要 versioned lineage、固定 binary 连续 canary、漂移/跨 profile/credentialed-effect 负测与独立 verdict；Accepted 文档不表示当前 binary 已可执行或 R3 已解除阻塞。 |

该 finding 与 Agent/Sandbox production authority 分离：签名 Marshal 不会把 ADR 0042 ordinary-user Adapter 升级为 hardened authority。Apple notarization 与企业 Endpoint Security/EDR allowlist 也分别判断；禁止通过删除 provenance、关闭安全软件、ad-hoc/随机 executable、`go run` 或伪造生命周期证据绕过。

## Pi 0.84.3 长任务 compaction 协议闭合（2026-08-26）

R3-D 真实 Run `i186-r3-d-shadow-s3-pi-20260826` 在 79,812 个事件、26,336,721 bytes 且未触发输出/时间预算时，以 `agent_end(willRetry=false, stopReason=length) → compaction_start(reason=overflow)` 进入 Pi session-v3 的合法自动压缩链。Pi Adapter `0.3.0` 把低层 `agent_end` 误作会话终态并主动终止进程，Core 随后正确以 `protocol-invalid/do-not-retry` 阻断原 Run；该 Run 不恢复、不接受其部分 diff，只保留 Outcome 与 raw transcript 作为根因证据。

Adapter `0.4.0` 将失败候选延迟到 `agent_settled`/EOF 提交，并显式验证 compaction reason、start/end 配对、success/aborted/failed outcome shape、usage、summarization retry、overflow 单次恢复与 continuation 顺序；未知、重复、乱序或未闭合事件继续 fail closed。该修复仅演进 Pi AgentAdapter 的版本化 decode/completion contract，不新增 Core 状态、Attempt/retry 语义、持久化字段、信任边界或发布权限，因此不新增 ADR；若未来把 compaction 暴露为 Core 生命周期或持久 authority，则必须先有新 ADR。

## 2026-08-26：post-worker abort 缺口与最小合同

真实 CAP-3 dogfood 出现多次相同结构性失败：Worker 与 Verifier 已完成，Run 已进入 `REVIEW_PENDING`，但 TaskSpec、acceptance 或 ReviewPacket 的上游缺陷使 current ReviewDecision 无法安全生成；该失败不属于 Worker 行为缺陷，继续 rework 会错误消费预算，手工修改 `.marshal` 又会绕过 authority journal。

| ID | 级别 | 状态 | 当前事实与关闭条件 |
| --- | --- | --- | --- |
| `POST-WORKER-REVIEW-PENDING-ABORT` | P1 | `CONTRACT-PROPOSED/IMPLEMENTATION-OPEN` | ADR 0050 提议仅开放 human 通过固定 CLI 发起的 `run.aborted REVIEW_PENDING → BLOCKED`，复用现有 Outcome/result/journal/snapshot 恢复，并以 current sequence/Attempt、已完成 verifier lineage、owned child 已退出、无 publication/SideEffect 与 Run Lease 组成 `PostWorkerAbortSafe`。原先新增 `ABORTED`、独立事件家族、carrier/ledger/projection Schema 和 supervisor 写权限的候选方案已因过度设计与 R3 循环依赖被否决。关闭需要 ADR 接受、实现正反/崩溃/并发矩阵、固定 Marshal 真实演练与独立 reviewer P0/P1 清零；Proposed 文档不表示命令可用，也不阻塞 R3-D/E/F。 |
## I186-R6 收口与 Roadmap replan（2026-08-27）

R6 由快速收敛治理交付；范围是 conformance/性能基线/soak/文档 replan，不新增信任边界合同（无新 ADR）。

| R6 Exit Gate 项（#186） | 证据 |
| --- | --- |
| 多拓扑 conformance | M9 双拓扑 suite 维持全绿（Push/Pull/embedded 三组合 outcome/invariant equivalence、failure injection、TLS 基线、lease 账本重开）；推进边界诚实标注：双拓扑未接生产 worker 路径，conformance 为测试套件 |
| SLO/增长基线 | `internal/perfbench`（`1f81286`）：五条热路径 p99 阈值冻结 ≤5000µs；实测基线 bindingcheck-recheck 0.764µs / attemptgate-admit 1.47µs / jitgate-verify 9.35µs / resultingress-admit 20.62µs / effectsink-execute 6.38µs（低阈值 2–3 个数量级；`TestBaselineConformance` N=200 确定性断言） |
| soak | `internal/soak`（`0208964`）：10k 迭代 seeded 原语 soak（决策/渲染幂等、unsafe 必 fence+reconcile、预算豁免只随 authority infra、effect 幂等防重+撤销后拒绝、同种子可重放）；`dc6d7ed` 路径级 accelerated soak 5 轮完整 bridged Run（journal 严格单调、attemptId 唯一、无第二业务事实、replay 等价、allocation record 完备）；**wall-clock 24h soak 未执行**（harness 就绪；归 v1.0 后首个运维窗口，不伪造成完成） |
| 无不可解释 orphan | R6 审计 Top-3 缺口处治：Gap-1 bridged SIGKILL 孤儿已关闭（`97147a1` 执行前 allocation 身份落盘 + SweepOrphans 幂等终结，新增 5 测试）；Gap-2 mid-claim Core 崩溃双拓扑 restart fixture、`I186-P1-5` 远程 fencing 归后续；Gap-3 plan 崩溃 worktree 扫描归 doctor 扩展 |
| 文档/状态同步 + replan | 成熟度矩阵 R6 快照（[i186-r0-maturity-matrix.md](research/i186-r0-maturity-matrix.md)，16 行级别重排、failure inventory 各 ID 状态化）；baseline report 补 R6 行；roadmap-status M10–M13 重排（保持 `PLANNED`，无证据不动状态枚举）与 failure inventory 同步 |
| reviewer APPROVE / replan 维护者接受 | 快速收敛治理下由 Lead 自审真实 diff 替代独立 reviewer；维护者接受以本报告与 roadmap-status 落账为准（不另行走状态机） |

R6 期间的实质修复（不是文档动作）：recovery 决策表两处副作用歧义缺口（partial-artifact/binding-lost 绕过 reconcile 横切、duplicate 幂等 resume 缺副作用歧义例外——soak iteration 69/148 驱动，含回归测试）；sandboxbridge 对真实 LocalRunner 的 allocation record + Sweep 孤儿对账。

按当时口径 I186-R6 于 2026-08-27 曾记为 `DONE`；该结论已由 ADR 0052 生产可达性纠偏撤回，现行状态为 `PLANNED/DESIGN`。当时明确保留的四项 honest gaps 为 wall-clock 24h soak、Push/Pull 生产接线、双 binding ResultIngress 接线与 CLI explain wiring。

## I186-R5 收口：strangler cutover 收敛（2026-08-27）

R5 由快速收敛治理交付，[ADR 0054](adr/0054-cutover-equivalence-and-effect-sink-fencing.md) 冻结等价性判据与 effect sink fencing：

| 交付 | 证据 |
| --- | --- |
| `internal/cutovereq` + `internal/cutovercheck`（`b3e193d`/`6285a15`） | 三分判据实现；R0 golden trace old/new 等价、business-mismatch/unexplained-drift/misaligned 阻断、资源权威计数回归 |
| `internal/effectsink`（`b3e193d`） | pre-mutation 独立 recheck 固定判序、revoke→effect 竞态专测、幂等防重复合门禁 |
| `internal/execution` `Input.WorkerRunner` seam + `internal/sandboxbridge`（`ab93174`） | Provision→Stage→Adapter.Run→Inspect→Terminate 执行链身份绑定；端到端等价：legacy 与桥路径 journal 事件序列逐条相同、WorkerResult 业务内容逐字节相同（fixture 确定性）；失败链 typed 归一化一致 |
| 默认翻向 + rollback（`20c5609`） | `MARSHAL_WORKER_EXECUTOR` 默认 sandbox、`legacy` 回退；rollback 演练：runstore setup 原样可读、无第二业务事实、零状态迁移；fencing 兼容修复（attempt 级单一 token，LocalRunner sealed-lease 精确匹配）+ 真实 LocalRunner 常驻回归 |

Exit Gate 对照：**新路径默认启用**（20c5609）；**回滚演练通过**（TestRunWorkerRunnerRollbackDrill）；**Local MVP 零回退**（全仓 `go test ./...` 唯一已知失败为 opencode live probe 宿主版本漂移，dogfood 沉淀项）；**cutover 判定**（golden 等价 + execution 端到端等价 + 白名单 diff 全入账）。范围标注：**旧路径不再服务 production** 的语义边界——本机 Local MVP 不存在 production assurance 运行（ADR 0042 ordinary-user），production profile 由 `agentruntime.ErrHostBypassDenied` fail closed 兜底，legacy 路径降级为 explicit local-nonproduction compatibility profile；real-real Agent canary 多轮对比、Cloudflare/远程 trace、host bypass 代码删除归 R6/后续治理。诚实缺口（继承自接缝测绘并经治理确认）：spine `FakeAgent` 桩、workerRuntimeProfile 的双 binding 接线、ResultIngress→runstore 证据桥三项在 R5 均未宣称完成，归 R6 或后续 milestone。

按当时 component checkpoint 口径 I186-R5 于 2026-08-27 曾记为 `DONE`；该结论已撤回，现行状态为 `IN_PROGRESS/COMPONENT`，以本报告顶部 checkpoint 为准。

**R5 真实 Agent canary 补证（2026-08-27，`3e6ed10`）**：`TestRealPiExecChainCanary`（`MARSHAL_RUN_PI_CANARY=1`/`MARSHAL_PI_PATH` 双门控、默认跳过）以标准 `scaffold→plan→approve→task run` CLI 纵切驱动真实 pi CLI 经 worker executor 默认 exec-chain 执行，断言 `worker.completed` 恰好入账一次、allocation record 锚点与尝试目录一致、`sandbox-binding-admission.json`（ADR 0052 §1.4 双 binding 接纳锚点）持久化存在、标记文件内容由 acceptance 权威校验通过；随测修复三处测试侧 policy 文档合规缺口（`generatedAt` 必填、control 块五子字段齐全、非 dogfood supervised 双批准门），生产代码零改动。范围标注不变：real-real Agent canary **多轮**对比与 Cloudflare/远程 trace 仍归 R6/后续治理，单轮 canary 不宣称生产 cutover 完成。

## I186-R4 收口：Pre-R4 四项合同 + 单一恢复模型（2026-08-27）

Pre-R4 contract gate 四项与 R4 单一恢复模型均由快速收敛治理交付，[ADR 0053](adr/0053-pre-r4-contract-gates-and-single-recovery-model.md) 冻结全部合同（并就地修订 ADR 0044 冷热路径条款）：

| 交付 | 证据 | 关闭的 finding / Exit Gate |
| --- | --- | --- |
| `internal/hotpath`（`f65cfaf`） | 入账分型、业务 kind 仅 cold、effect 门禁、Restore 门禁、洗路径冲突负测 | `I186-ARCH-HOT-PATH-AUTHORITY` |
| `internal/jitgate`（`c4c8b69`） | AdmissionToken 五要素 + provision 前五项重验、半开区间、硬错误/业务拒绝分流 | `I186-ARCH-JIT-ADMISSION-RECHECK` |
| `internal/protocolrev`（`ab7b263`） | Revision 解析、pinned 精确匹配、迁移合法性、HistoryGuard 防重写 | `I186-ARCH-PROTOCOL-REVISION-MIGRATION` |
| `internal/candidateid`（`c4c8b69`） | identity 派生与跨 Attempt 收敛、证据绑身份/换绑拒绝、legacy 迁移幂等 | `I186-ARCH-CANDIDATE-IDENTITY` |
| `internal/recovery`（`34f70d3`） | 故障矩阵八类唯一幂等结论、ambiguous side effect 强制幂等键对账、stale 仅入冲突、幂等性与 Render 复盘要素 | R4 Exit Gate（每类故障唯一结论；不能证明安全时 fence+new Attempt）；`I186-ARCH-RESOURCE-CLASSIFICATION-AUTHORITY` 消费边界 |

验收命令：`go build ./...` 干净；`go test` 12 个收敛域包（recovery/hotpath/jitgate/protocolrev/candidateid/revokedrain/attemptgate/locationattest/failureclass/agentregistry/runtimeprofile/bindingcheck）全绿。Local MVP 零回退：全部纯新增包，零既有包修改。

范围标注：ADR 0045 R4 交付清单中「Inspect/Reconcile/Cancel/Terminate 与不可绕过的 current lease resolver」的 Provider 侧接线、`marshal explain run` CLI wiring 与真实 ledger 装配归 I186-R5/R6（本收口以 ADR 0053 决策 5 「等价 API」口径冻结恢复语义与渲染模型）；`I186-ARCH-EFFECT-SINK-FENCING` 与 `I186-ARCH-CUTOVER-EQUIVALENCE` 归 R5，不随 R4 关闭。

按当时收敛域合同与决策语义口径 I186-R4 于 2026-08-27 曾记为 `DONE`；该结论已撤回，现行状态为 `IN_PROGRESS/COMPONENT`，以本报告顶部 checkpoint 为准。

**R4 真实装配与恢复路径消费补证（2026-08-27）**：范围标注中的「`marshal explain run` CLI wiring 与真实 ledger 装配」已由 `6a26012` 交付（`internal/explain` 从权威 journal/snapshot/attempt anchor 装配 `recovery.RecoveryInput`，`marshal explain run RUN_ID [--json]` 渲染恢复时间线/decision/next action，只读不改写状态）；「恢复路径消费单一恢复模型」已由 `2bf4f3e` 交付：`explain.AssembleWithStaleness` 开放 staleness 注入，supervisor 死 driver 分派以自身 driver 死亡窗口装配 `recovery.Decide`，仅 new-attempt 且免幂等键对账才派生 driver；`task run --recover-dead-driver` 逃生舱在 owner 死亡耐用记录证明后走同一 `recoverTakeoverAdmission`（staleness≈0），需对账的 ambiguous side effect 一律 fail closed 并指向 `marshal explain run`。新增 supervisor 分派矩阵与 cli admission 三分矩阵测试；既有 `TestSupervise*` 全量保持绿色。剩余 Provider 侧 Inspect/Reconcile/Cancel/Terminate 接线与 `I186-ARCH-EFFECT-SINK-FENCING`/`I186-ARCH-CUTOVER-EQUIVALENCE` 仍归 R5/R6，不随本补证关闭。

## I186-R3 收口：快速收敛治理下的 Exit Gate 证据（2026-08-27）

2026-08-27 起维护者授权 I186 快速收敛治理：单 Lead + 多 Sub-Agent 高并发，停用 Marshal skill/admission/ReviewDecision/独立 reviewer/rework 轮转，Lead 直接实现、自审真实 diff 后直接合并；防错误发布、数据破坏与 trust-boundary ADR 三项硬约束保留，dogfood 问题（含 Issue #212 签名身份）另行沉淀不阻塞主线。Issue #191 原 Exit Gate 中「独立 reviewer APPROVE / PR #192 登记前置」两条流程性条件按该授权不再适用；finding 稳定登记继续以本报告为准（不依赖未合入 PR）。

R3 Exit Gate 技术条件与证据对照：

| Exit Gate 条件 | 证据 |
| --- | --- |
| Agent/Sandbox 可独立替换与撤销；profile 不隐藏底层身份 | `internal/runtimeprofile`（AgentBinding/SandboxBinding 独立 digest、Replace* 互不影响）；R3-C `internal/bindingcheck` 双侧独立 recheck |
| 任一 binding revoke/expire/replace 后旧组合结果不可接纳 | `internal/attemptgate` 负测：仅 Agent 侧 revoke/replace/snapshot-supersede 与仅 Sandbox 侧 revoke/expire/replace 双向单侧失效均 fail closed 且另一侧不受影响 |
| 跨 Port credential/token/evidence 复用失败；Agent evidence ≠ Sandbox evidence | `internal/attemptgate/boundary_test.go`：Sandbox 签发 evidence 冒充 Agent 侧、跨 registration 借用、伪造 digest、跨 Port binding 混淆均 fail closed；`EvidenceRecord.ProviderType=sandbox` 类型级拒绝 |
| ResultIngress 从 Attempt 解析 immutable profile 并分别 current-ledger recheck | `internal/attemptgate`：AttemptProfileStore immutable put-if-absent（同 digest 幂等、冲突 fail closed）+ Gate 双侧 recheck；生产 ResultIngress 接线归 R5 |
| security-critical revoke 立即生效 / ordinary upgrade 有界排水 | `internal/revokedrain`：零 drain（cancel+bump+kill）/ stop-new + bounded drain + fence；revoke 抢占 drain、fence 后升级 fail closed、double revoke 幂等拒绝 |
| Provider 自报执行位置只能是 observation；需 authority-verified fact + 来源标注 + 伪造位置负测 | [ADR 0049](adr/0049-location-attestation-and-failure-classification-authority.md) 决策 1 + `internal/locationattest` 负测矩阵 |
| `infra-failure` 分类来自故障域外 observation；Provider 声明只能诊断/收紧 | [ADR 0049](adr/0049-location-attestation-and-failure-classification-authority.md) 决策 2 + `internal/failureclass` 决策表与伪造负测 |

落地提交：`ec13ee7`（revokedrain）、`0a9b3b6`（attemptgate）、`c47b4c2`（locationattest）、`d89c65e`（failureclass）、`8590c4e`（ADR 0049 + 0043 §5 修订标注）。验收命令：`go test ./internal/revokedrain/... ./internal/attemptgate/... ./internal/locationattest/... ./internal/failureclass/... ./internal/agentregistry/... ./internal/runtimeprofile/... ./internal/bindingcheck/... -count=1` 全绿；`go build ./...` 干净。全仓 `go test ./...` 唯一失败为 `internal/adapter/opencode` 的 live probe（宿主 opencode 1.18.20 超出 adapter 版本表 [1.18.13/1.18.16/1.18.18]），属 dogfood 环境漂移，另行沉淀不阻塞主线。Local MVP 零回退：纯新增包，未修改任何既有包或信任边界目录行为。

按当时收敛域合同与负测口径 I186-R3 于 2026-08-27 曾记为 `DONE`；该结论已撤回，现行状态为 `IN_PROGRESS/COMPONENT`，生产接线不再转嫁给已撤回的 R5 结论。Pre-R4 contract gate 四项（hot-path authority、JIT admission、protocol migration、Candidate identity）的历史实现证据继续保留。

## Issue #186：架构复审 Finding 稳定登记（2026-08-25）

[Issue #186](https://github.com/chiga0/marshal-harness/issues/186) 的多轮复审接受了 WorkerExecutor、Agent/Sandbox 双 binding、ResultIngress 与 strangler 收敛方向，同时发现若干不能只留在 Issue 评论中的合同缺口。本文只建立稳定 ID、当前证据、关闭条件和 milestone 落点；**登记不等于修复，Issue disposition 不等于 ADR 接受，代码或测试存在也不等于 finding 已关闭**。关闭任一 P0/P1 仍需相应合同/实现、正反证据和独立 reviewer verdict。

2026-08-27 direct checkpoint：为缩短 R3→R6 主线，维护者停止使用 Marshal skill 的 admission/rework 轮转，改为单 Lead + 多个互斥 Sub-Agent 并行审计、Lead 直接实现与自审。`feat/i186-r3-direct` 已重新实现 R3-D evidence boundary 与 bounded-drain 纯核心：外部只呈现 opaque material ref，权威链由新鲜 material/registration/snapshot 查询闭合，Agent/Sandbox 的 evidence、credential、token 六种跨 Port 复用方向均 fail closed；security revoke 无 drain 窗口，planned upgrade 只允许全新且无别名的 registration/snapshot 并在冻结 deadline 后 fence。定向测试 `go test ./internal/revokedrain` 通过。该 checkpoint 未接线 production authority ledger/ResultIngress，不能关闭 `I186-ARCH-DUAL-BINDING-RECHECK`；R3-E/F 的故障域外位置/资源 observation authority 会改变信任与持久化合同，仍须先新增最小 ADR。详细状态见 [i186-r3-progress.md](research/i186-r3-progress.md)。

### 主执行链 hardening

| ID | 级别 | 状态 | 当前证据与关闭条件 | 建议落点 |
| --- | --- | --- | --- | --- |
| `I186-ARCH-LOCATION-ATTESTATION` | P0 | `CLOSED-CONTRACT+CONVERGENCE`（2026-08-27） | ADR 0043 把执行位置 evidence 的产出职责写给 SandboxProvider，仍可能由被证明方自证。必须区分 `provider-asserted location claim` 与故障域外产生的 `authority-verified location fact`；只有后者可支撑 production assurance/publication。关闭证据：[ADR 0049](adr/0049-location-attestation-and-failure-classification-authority.md) 决策 1（claim/fact 分型、自证排除、FactLedger 身份元组 put-if-absent、修订 ADR 0043 决策 5）+ `internal/locationattest` 收敛域实现与负测（digest 篡改、observer 自证排除、跨 allocation/generation 挪用、伪造 claim、身份元组冲突不覆盖原 fact）。Local kernel held handle 采集与 ResultIngress/发布门禁接线归 I186-R5/R6，不从本关闭推断。 | `I186-R3` Exit Gate |
| `I186-ARCH-EFFECT-SINK-FENCING` | P1 | `CLOSED-CONTRACT+CONVERGENCE`（2026-08-27） | ResultIngress recheck 只能保护 ledger，不能撤销已经发生的外部效果。SCM、Artifact、Secret 与其它 effect sink 必须在 mutation/secret use 前独立执行 current generation、fencing、authorization 与 target recheck，并覆盖 revoke→effect 竞态。关闭证据：[ADR 0054](adr/0054-cutover-equivalence-and-effect-sink-fencing.md) 决策 2 + `internal/effectsink`：pre-mutation 固定判序独立 recheck（authorization-revoked 优先的五种生命周期拒绝逐一单变量负测、revoke→effect 竞态专测）、EffectLedger 幂等防重、ExecuteIfAdmitted 复合门禁。SCM/Publisher 生产 sink 接线归 R5/R6 持续执行，不从本关闭推断。 | R3 后、R5 cutover 前 |
| `I186-ARCH-HOT-PATH-AUTHORITY` | P1 | `CLOSED-CONTRACT+CONVERGENCE`（2026-08-27） | 当前 `internal/resultingress` 把 checkpoint/heartbeat/log 归为 hot path 并跳过 registration/snapshot/evidence eligibility recheck；checkpoint 可能在未完成冷路径校验时被 Restore 消费。关闭证据：[ADR 0053](adr/0053-pre-r4-contract-gates-and-single-recovery-model.md) 决策 1（修订 ADR 0044 冷热路径条款）+ `internal/hotpath` 收敛域实现与负测：业务 kind 只允许 cold 入账（入账即禁止而非事后解释）、authority effect（extend-lease/bump-generation/decide-fencing）仅作用 cold 接纳、Restore 门禁只接受 cold 接纳的 checkpoint、同 digest 洗路径以入账冲突 fail closed。resultingress/sandbox.Restore 生产接线归 R5，不从本关闭推断。 | #186 Pre-R4 contract gate |
| `I186-ARCH-DUAL-BINDING-RECHECK` | P1 | `CLOSED-CONVERGENCE`（2026-08-27） | R2 ResultIngress 当前只有单组 registration/snapshot/evidence binding；R3-B 已冻结 `WorkerRuntimeProfile`，但 per-Attempt profile 的 AgentBinding 与 SandboxBinding 分别 current-ledger recheck 尚未完成。关闭需要单侧 revoke/replace 的双向负向 fixture。关闭证据：R3-C `internal/bindingcheck`（双侧独立 recheck、七封闭原因）+ R3-D `internal/attemptgate`（AttemptProfileStore immutable put-if-absent 绑定、Gate 从 Attempt 解析 immutable profile 并分别 recheck AgentBinding/SandboxBinding；仅 Agent 侧 revoke/replace/supersede 与仅 Sandbox 侧 revoke/expire/replace 的双向单侧失效互不牵连负测全绿）。生产 ResultIngress 接线归 I186-R5，不从本关闭推断。 | `I186-R3-C/D` |
| `I186-ARCH-CUTOVER-EQUIVALENCE` | P1 | `CLOSED-CONTRACT+CONVERGENCE`（2026-08-27） | ADR 0045 的 old/new 全 digest 相等对真实非确定 Agent 不可满足。R5 前必须拆成真实 Agent 必须相等的 authority-trace invariants，以及只适用于 deterministic Fake 的 content digest equality；真实 Agent 使用资源归一化后的不劣化统计，不能人工解释掉 authority diff。关闭证据：[ADR 0054](adr/0054-cutover-equivalence-and-effect-sink-fencing.md) 决策 1 + `internal/cutovereq`（三分判据、白名单 upgrade 形态校验、不可人工覆盖）+ `internal/cutovercheck`（R0 golden trace old/new 等价、business-mismatch/unexplained-drift/misaligned 阻断、资源权威计数回归）+ execution 端到端等价（legacy vs 桥路径 journal 事件序列逐条相同、WorkerResult 业务内容逐字节相同，6285a15/ab93174）。真实 Agent canary 与多轮对比归 R6 conformance，不从本关闭推断。 | `I186-R5` |
| `I186-ARCH-RESOURCE-CLASSIFICATION-AUTHORITY` | P1 | `CLOSED`（2026-08-27） | `ResourceEnvelope.observedPeak`、termination reason 与 `infra-failure` 分类权未冻结。合同关闭证据：[ADR 0049](adr/0049-location-attestation-and-failure-classification-authority.md) 决策 2 + `internal/failureclass`（决策表 8×2 全组合、伪造 infra-failure 放宽恒 false、semantic 抗拒洗白、digest echo）。消费关闭证据：`internal/recovery`（34f70d3）决策表消费该分类——terminal-failure 且 authority-observed infra 分类时产生预算豁免的新 Attempt（MayRelaxBudget/MayExemptSemanticRework 输入），provider-claimed/semantic 分类恒 resume 消费失败 Outcome；R4 恢复模型已落地，消费边界关闭。 | `I186-R3` 合同，R4 恢复消费 |
| `I186-ARCH-JIT-ADMISSION-RECHECK` | P1 | `CLOSED-CONTRACT+CONVERGENCE`（2026-08-27） | JIT provision 扩大 admission→provision 时间窗。Provision 前必须重验 AdmissionDecision `validUntil`、registration/snapshot generation 与 current Policy；不得顺延到 R6。关闭证据：[ADR 0053](adr/0053-pre-r4-contract-gates-and-single-recovery-model.md) 决策 2 + `internal/jitgate`：`AdmissionToken`（五要素 + canonical digest 防篡改）与 `VerifyBeforeProvision` 五项强制重验（registration active、active snapshot digest 对齐、policy active、policy digest 对齐、半开区间 `[issue,validUntil)`）；结构性硬错误与业务拒绝（六封闭原因码）严格分流。dispatch/provision 生产强制点接线归 R5，不从本关闭推断。 | #186 Pre-R4 contract gate |
| `I186-ARCH-PROTOCOL-REVISION-MIGRATION` | P1 | `CLOSED-CONTRACT+CONVERGENCE`（2026-08-27） | `acp → acp/v1` 等协议枚举升级不得重写或重新解释历史 snapshot/digest；只能 Supersede 为新 snapshot，unversioned 历史值不能满足 pinned revision admission。关闭证据：[ADR 0053](adr/0053-pre-r4-contract-gates-and-single-recovery-model.md) 决策 3 + `internal/protocolrev`：Revision 解析冻结、AdmitPinned 精确匹配（unversioned 出示永不满足 pinned）、SupersedeMigration 合法性（digest 必新/同族/To versioned/provenance 必备）、HistoryGuard 防重写（From 须先冻结、To 须真新、判定不改写）。capability supersede 生产接线归 R5，不从本关闭推断。 | #186 Pre-R4 contract gate |
| `I186-ARCH-CANDIDATE-IDENTITY` | P1 | `CLOSED-CONTRACT+CONVERGENCE`（2026-08-27） | 当前已有独立 `candidateDigest`，但 identity slot 和链仍强约束于 Attempt，尚未以合同证明不会把 Attempt→Candidate 1:1 固化为未来破坏性约束。关闭证据：[ADR 0053](adr/0053-pre-r4-contract-gates-and-single-recovery-model.md) 决策 4 + `internal/candidateid`：CandidateID 由 (ContentDigest, RecordDigest) 派生、OriginAttemptID 仅 provenance（不同 Attempt 同内容收敛同一 ID 的构造性证明）；证据绑身份（未冻结身份不得绑定、换绑 ErrEvidenceRebound）；MigrateLegacyReference 单向幂等迁移。不启用多 Candidate fan-out；生产引用换指归 R5，不从本关闭推断。 | #186 Pre-R4 contract gate |

R2（#189）已关闭，不得把上表中原先口头指派给 R2 的 finding 视为随之关闭。为避免增加新的 milestone 和状态面，四项漏接合同统一列入 #186 的 Pre-R4 contract gate：可以与 R3 并行补齐，但 R4 启动前必须有合同、正反证据和独立 verdict。`I186-ARCH-LOCATION-ATTESTATION` 必须进入 #191 的显式 Exit Gate，避免 R3 在位置仍由 Provider 自证时被错误关闭。

### 前期研讨、复盘与 Worker 协作

三类能力的共同风险是：Agent 生成的语义内容会影响另一个阶段、另一个 Worker 或未来 Goal。复审采用统一判据：跨越该边界的内容必须先成为 immutable、content-addressed、digest-bound、带 producer provenance 的对象，明确 purpose/audience，并由下游显式选择为 **untrusted input**；禁止自动注入 transcript、自由文本消息或 live knowledge query。该判据需要 Proposed ADR 和独立审计，本文不把它提前标为已接受合同。

| ID | 级别 | 状态 | 问题与处置 / 关闭条件 |
| --- | --- | --- | --- |
| `I186-ARCH-DECISION-INPUT-BOUNDARY` | P1 | `OPEN-PROPOSED-ADR` | 需要冻结统一的跨阶段语义输入门禁：canonical digest、provenance、purpose/audience、大小/类型边界、显式 admission、supersession、冻结下游引用，以及“作为数据而非指令”呈现。现有 Artifact/Goal proposal 可复用，但不存在可绕过 admission 的通用上下文流。 |
| `I186-PRE-EXEC-DELIBERATION` | P1 | `OPERATIONAL-PILOT` | Stage 0 立即使用 `publication:none` 调研 Run、互斥报告路径、人工综合与显式 proposal，不新增 Core 状态。产品化 Discovery 推迟到 R6 后；关闭前还需 typed finding/option、只读 workload profile、网络/来源治理、dissent carrier、Goal controller 与负测。 |
| `I186-ARCH-DISSENT-CARRIER` | P1 | `OPEN-DESIGN` | dissent 与 open assumption 若只写进汇总散文，会在计划接纳时丢失。需要 versioned、content-addressed、digest-bound 的 durable handoff carrier，并在后续 ReviewPacket 中显式引用；Issue 中 `ACCEPT_P1` 只代表方向处置，不等于已有 ADR/Schema/实现。 |
| `I186-RETROSPECTIVE-RECORD` | P1 | `OPERATIONAL-PILOT` | 现在可生成 ledger/Outcome/Evidence 的事实投影与轻量 closeout；因果解释、失败归因和改进建议必须作为带 provenance 的 assessment/proposal，与机械事实分开，不能伪装成“纯投影”。 |
| `I186-ARCH-DISCOVERY-RETRO-ROLE` | P1 | `DEFERRED-R6` | `sandbox.WorkloadRole` 当前封闭为 `worker|verifier`。产品化 Discovery/Retrospective workload 不得冒充 verifier；新增 role/profile 将改变持久化合同，必须先有独立 ADR、principal、最小权限与负测。Stage 0 人工流程不声称该能力已实现。 |
| `I186-ARCH-RETRO-EVIDENCE-PROJECTION` | P1 | `DEFERRED-R6` | 未来 retrospective evidence packet 必须是 allowlisted + redacted 的冻结投影，并使用独立 principal；禁止把 raw ledger、credential、宿主路径或未筛选 transcript 原样交给复盘 Agent。 |
| `I186-ARCH-KNOWLEDGE-SNAPSHOT-REPLAY` | P1 | `DEFERRED-DEPENDENCY` | 跨 Goal 学习在 `ResourceEnvelope`、Provider-independent failure attribution 与重复任务 ROI 证据出现前不实施。未来知识只能作为 immutable versioned snapshot 被引用，其 digest 进入 Attempt 冻结输入集；planning/execution 决策路径禁止 live 查询。 |
| `I186-WORKER-COORDINATION` | P1 | `REJECT-IMPLEMENTATION-FOR-NOW` | 当前没有可测量的 Lead 转发瓶颈，不建设 mailbox/A2A 群聊。现阶段只允许 Artifact-mediated 单向协作：发布不可变 ref，下游按已接纳计划显式消费 digest，Core 在 fan-in 复核。若未来重提 mailbox，必须先提交非规范 RFC 和瓶颈数据，再审计配额、deadline、crash/replay、撤销、循环与同 Goal prompt injection。 |
| `I186-ARCH-DEPENDENCY-HINT-AUTHORITY` | P1 | `DEFERRED-WITH-MAILBOX` | 未来若存在 `DependencyReady`，它最多是可丢失的唤醒提示；Core 在完全没有 Worker 消息时也必须能从 ledger/Artifact refs 独立判断依赖满足，消息不得成为 correctness 或 liveness 的唯一条件。 |
| `I186-DOC-HUMAN-MODEL` | P1 | `OPEN-DOCS` | 需要一份人类友好的分层导读，解释“当前能力 / Accepted 目标合同 / Proposed 演进”三种状态，并说明前期研讨、复盘记录和不实施 mailbox 的原因。文档合入且链接/构建检查通过后可关闭为 `CLOSED-DOCS`，但不升级任何产品能力状态。 |

优先级冻结为：`前期研讨 Stage 0 >> 复盘记录 > 复盘学习 >> Worker mailbox`。其中只有 Stage 0 与轻量 closeout 可在 R3–R6 期间作为操作约定试行；其余不得抢占主执行链 P0/P1，不新增 required production path。所有 pilot 都应记录成本、等待时间、finding 质量、返工变化和人工分钟数，R6 后再基于证据决定保留、修改或删除。

## 2026-08-29：Supervisor mechanics receipt binding checkpoint

`I186-ARCH-SUPERVISOR-RECEIPT-BINDING` 当前状态为 `CONTRACT-ACCEPTED / INTEGRATION-OPEN`。ResultIngress 的单一 RB1 ledger/projection 已增加 `process-supervisor-bootstrap-prepared` 恢复锚点，以及不推进 Attempt head 的逐 command intent/outcome recovery 子链；每个 outcome 绑定完整 mechanics/journal pre/post anchor，business fact 只引用 exact successful outcome fact，intent-only 可耐久进入 intervention。旧 ledger 省略新字段时仍按原 digest 和状态序列回放。候选 `12996f87beb3b45b9267d4356875d9ebe257fcd2` 经独立终审确认 P0/P1/P2 均为 0 后，[ADR 0060](adr/0060-supervisor-mechanics-authority-binding-and-recovery.md) 已接受。该 checkpoint **没有**接入 production composition；`processsupervisor.Client` 的 deterministic prepared-command API、descriptor-relative nonce/journal object recovery、lost `Close` receipt 后的 offline absence recovery仍开放。在真实 spawn/collect/terminal/close 调用链与重启 reconcile 通过前，不得把它标记为 `INTEGRATED` 或关闭 R2/R3 production reachability finding。

## 2026-08-29：无结果 Close 与生产 server binary 拓扑

`I186-ARCH-CLOSE-TRANSCRIPT-DISPOSITION` 当前状态为 `CONTRACT-ACCEPTED / IMPLEMENTATION-OPEN`。真实 producer chain证明两个缺口：terminalization barrier先赢后，ResultIngress必须拒绝后续 `collect`；successful collect outcome也可能已耐久、但 admission仍输给随后 barrier。[ADR 0061](adr/0061-supervisor-close-transcript-disposition.md) 已接受 `collected-admitted|collected-not-admitted|not-required` 封闭 union；两个 non-admission分支只能引用 current RB1 authority在 empty-result barrier上签发的 exact resolution fact，不能放宽正常成功路径。wire/persisted projection、hostile/crash/replay矩阵与真实 producer接线仍未实现，完成前 cancel/timeout与 collect/admission crash window均不可宣称可恢复。

`I186-ARCH-FIXED-SERVER-COMPOSITION` 当前状态为 `CONTRACT-ACCEPTED / INTEGRATION-OPEN`。独立 `marshal-server` 进程不是 ADR 0059 要求的 fixed Marshal identity，继续 child-exec又违反 ADR 0057 的唯一 in-process Port。[ADR 0062](adr/0062-fixed-marshal-production-server-mode.md) 已接受并部分取代 ADR 0052/0057 的 executable拓扑：生产 loopback server收敛为 fixed `marshal control-plane serve`，独立 `marshal-server` 降为无生产 mutation权限的开发/兼容入口；AF_UNIX delivery projection只能引用 current owner/RB1 exact receipt，不能成为第二 authority。

2026-09-01 的第一段 cutover 已删除独立 server 的 `--marshal-executable`、child `task run`、Provider registration mutation 与 Worker selector 初始化，并通过不可配置的 `DisableMutations` 在 body parsing/idempotency 写入前拒绝 Task create/cancel 和 Run approval/start；查询、事件和跨进程只读恢复继续保留。该变更关闭“独立 executable 可被 flag/环境重新提升为 mutation root”的实现缺口，但 fixed `marshal control-plane serve`、in-process `PublicApplicationPort`、authenticated owner/session 和 server restart/response-loss recovery 尚未接线。因此本 finding 仍为 `INTEGRATION-OPEN`；只有 fixed mode 完成真实 canary，且 exact managed-development signed/allowlisted 或 notarized candidate满足 stable 门禁后，才可升级。ADR 0068 仅对 `v1.0.0-rc1` 部分取代 server/managed 前置，不能据此宣称 server/stable 已完成。

同日第二段 cutover 关闭了 Run start 的 `RunExecutor func` direct execution seam：HTTP adapter 只消费 `PublicApplicationPort`，pending intent 绑定 current-ledger sequence/authority head 与 exact prepared Attempt/`preparationDigest`，执行顺序固定为 `InspectRun → PrepareRunStart → StartPreparedRun`，receipt 从完整 legacy `RunState` 收敛为 path-free `RunProjection`。测试覆盖成功 start、幂等 replay、持久 pending 记录的 response-loss recovery及其它 Attempt 冒名恢复拒绝，并机械证明 replay 不重复 Prepare/Start。该结果只关闭 `I186-ARCH-FIXED-SERVER-COMPOSITION` 的一个子 finding；server 仍直接拥有其它 legacy lifecycle/store 分支，`ProductionRuntime` 仍按单 Run/Run Lease 组合，不能直接常驻服务多个 Run。下一实施边界是 owner-scoped runtime session/factory 与 fixed CLI 注入；若在此之前直接添加 `control-plane serve` 命令，会重新制造 owner/lease 或第二 composition 问题。

第三段 cutover 已关闭上述 owner/Run 生命周期耦合：新增的 `RepositorySession` 独占一次 repository owner acquisition、sealed ResultIngress 与 runtime claim，Run-scoped composition 只能借用且不能关闭 owner；关闭屏障保证 Session 不会与仍在执行的 Run runtime 竞态释放 held descriptors。实现复用原有 owner fact、current-ledger replay、Run lease 与 close order，没有增加新事实或绕过验证，因此不触发新 ADR。机器化架构门禁曾发现 Session 直接调用 `claimRuntime` 的越界，已通过把唯一 claim 入口保留在 `runtime.go` 修正，而非放宽检查器；自审还发现 typed nil 存入 `io.Closer` 会让旧 standalone composition 误入 borrowed-owner 分支，已改用具体指针并用旧/新组合回归覆盖。该 finding 仍为 `INTEGRATION-OPEN`：当前只有资源生命周期闭合，尚缺 Session 上的多 Run application assembler、fixed CLI server mode、authenticated transport 与真实 restart/response-loss canary。
## 2026-08-31：Result observation release-gap 修复

RC1 completion 复审发现：`result-admitted` 已提交后，terminalization 会先释放 path-B worktree，再追加 runstore `worker.completed`；若在两者之间崩溃，恢复时重新观察已归还用户的 live worktree 会因合法修改永久阻塞仍为 `RUNNING` 的 Run。该问题登记为 release-critical P1，并由 [ADR 0072](adr/0072-result-observation-binding-before-worktree-release.md) 关闭合同缺口：首次 admission 同一 authority fact 绑定规范 snapshot bytes、`snapshotDigest` 与 `diffDigest`，release 后恢复只校验 descriptor-held snapshot 对该 binding，不再读取 live worktree。实现与定向冷重放测试已落地；RC1 仍须真实 Pi 纵切与 same-bytes release canary，不因本项关闭而升级。

## 2026-08-30：RunStore 描述符补强与合入审计

`main@46e0054` 是当前本地权威基线（父提交 `054789c`，`origin/main` 尚未同步）。本次以维护者指示直接合入 `ac5fd20`，新增 `NewFromStateRootDescriptor`/`NewAt`，让 existing-only acquisition 沿 held StateRoot descriptor 打开 `runs/<runID>`，并将描述符保留到 Lease 生命周期结束。该切片通过 `go test -race ./internal/runstore`、`go vet ./internal/runstore`、`git diff --check` 与 architecture check。

独立 reviewer 随后发现 descriptor Store 的 pathname API 空根路径风险与 Close/acquisition 竞态；`main@109f35d` 已增加哨兵根路径、descriptor-only `Acquire` 拒绝和互斥保护，并通过 runstore race/vet/diff 定向门禁。本次仍未等待独立 reviewer，故记录为审计风险而非“已独立验收”；Store.root 等兼容字段仍需在 production composition 接线前完成全调用链审计，不得把该 component 合入解释为 S2′ 完成。ResultIngress/Execution/App 现有 sealed Run-start fixture 仍失败，CI 质量门禁不绿；Qoder/Codex 生产配置、真实 Pi→独立 Decision→`ACCEPTED`、RC1 同字节 canary、签名/公证和远端发布均未完成。
