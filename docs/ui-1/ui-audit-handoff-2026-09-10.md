# UI 架构、产品体验与优化交接审计

日期：2026-09-10。状态：审计完成，优化未实施，不能作为 UI 发布验收通过证明。

## 1. 结论与范围

当前 `apps/task-web` 的方向合理：以 Task 为中心、同源 HTTP、服务端唯一事实源，适合可信单用户的 Agent 团队工作台。保留现有技术栈，不重写、不增加 Workspace、身份平台或流程编辑器。当前优先问题不是配色，而是操作一致性、连接可用性、结果语义和真实浏览器验证不足。

审计源码基线为 `a926fbaa340c1043dc5271bae8ec3124fe6d2d53`。浏览器 fixture 使用本机后继 `1b2d74da`，已检查 `apps/task-web` 与 `packages/task-service` 相对基线无差异。旧 `web/` 属于 Go Run 控制台，不与新 Task UI 混评。通用文件团队实现已推送 `feat/generic-default-team`（`b70d918817c364c036add04422f0aeb640d7759d`）并暂停；本审计没有继续该功能、合并或发布它。

方法：主审加两项独立只读代码审计、设计与验收文档对照、零模型真实 HTTP fixture、内置浏览器实看连接页。历史记录的 155 项测试不是本轮重跑结果。此次未完成已连接页面的视觉、深色、窄屏、键盘和性能矩阵，以下严格区分代码发现、实测现象与设计建议。

浏览器复现入口：使用 `apps/task-web/e2e/helpers.mjs` 的 `FIXTURE_LEADER`/`spawnService`，构建当前 UI dist 后按 `--ui` 启动隔离服务，准备一条 awaiting-answer fixture Task。此次使用 Codex 内置浏览器，地址 `http://127.0.0.1:59442/ui/`，1280×720；在连接页输入本次测试 token 后点击连接。截图已在审计会话中查看，未导出图片或采集浏览器异常栈；临时运行证据保留在本机 `/private/tmp/marshal-ui-visual-GispFj9r`，不属于可移植发布证据，不应提交其中连接凭据。服务已停止，复验必须重新启动并使用新的测试凭据。

## 2. 架构和技术框架

- 新 UI 使用 React 19、TypeScript 5、Vite 7、TanStack Query 5、React Router 7 HashRouter、Tailwind 4；按任务、成员、成果、连接分层，调用 `/v1`。
- `task-service --ui` 挂载静态资产；无需新建独立业务后端。缓存不能成为第二套权威状态机。
- 现有同源、内存 token、写请求不自动重试、下载摘要核验、批准绑定当前计划的方向应保留。
- 文档提到 shadcn 风格，但当前关键模态组件是手写实现。应统一可靠的焦点/模态原语，不必为品牌统一重写全部组件。
- 手写响应类型加 `as T` 有长期合同漂移风险；后续可从已有 OpenAPI 派生类型，并对关键写回执做运行时验证。不另建 Schema 平台。

## 3. 必修问题及验收

行号针对上述锁定基线；优化 Agent 应先核对后继 diff，避免覆盖其他作者修改。

| ID / 优先级 | 证据与问题 | 修正与验收要求 |
| --- | --- | --- |
| UI-01 / P1 | `apps/task-web/src/features/tasks/task-new-page.tsx:32,104`：所有 `ApiError`（含 500/502/503/504）均当明确拒绝并丢弃提交会话。响应失败不证明服务器未创建。 | 模拟服务器提交成功后返回 504；保留原 body、上传状态、幂等键并展示结果未知。显式重放后只存在一个 Task。不得自动新键再建。 |
| UI-02 / P1 | `detail/shared/logical-action.ts:84` 在 deps 变化时清在途锁、换键、回 idle；`question-card.tsx:41`、`leader-request-card.tsx:128,220`、`task-controls.tsx:86` 使用实时 revision。轮询可改变未决动作，旧 Promise 又可覆盖新状态。以上路径位于 `features/tasks/`。 | 冻结一次逻辑动作的输入/key；用异步代际隔离旧响应。分别在 submitting、unknown、409 时推进其他 Worker/revision，不能解锁重复提交或丢原键；用户明确核对新版后才开始新动作。 |
| UI-03 / P1 待定位 | 实际浏览器打开隔离服务 `/ui/` 成功，输入正确 fixture token 后显示“网络层不可达”；同一服务同 token 的独立 HTTP 请求返回 200。故当前浏览器接入未验收通过。 | 先用真实浏览器定位异常栈/请求，不先认定是 token 或服务未启动。检查 `transport/client.ts:76,90,134` 将原生 fetch 存入对象后作为方法调用的接收者兼容性；这只是候选原因，未证实。验收真实浏览器完成连接→列表→详情，不能只以 Node fetch 通过代替。 |
| UI-04 / P2 | `detail/overview/acceptance-panel.tsx:14,51` 只读 `leader.review`，说明写“验收由独立评审产生”。实际徽标是“评审通过”，并非直接显示“验收通过”，但遗漏真实 acceptance。 | 分开评审、独立验收、交付、后验。使用 `task.audit.acceptance` 的 pending/passed/failed/unknown 与摘要；测试 Review accept + acceptance pending/failed/passed 三组。不能以 Review 推导验收。 |
| UI-05 / P2 | `transport/client.ts:104` 不传 Worker 分页，`task-detail-layout.tsx:100` 丢 nextCursor；服务端默认 50。 | 至少 51 条 Worker 的最后一条可查看与操作；显示已加载范围或真实总数，不把首批数量当总量。 |
| UI-06 / P2 | `transport/client.ts:61` 无请求 deadline；详情 queryFn 未贯通取消 signal。 | 悬挂读请求有界结束，卸载取消读；写超时仍是 unknown、保留原键，不误称服务端取消。 |
| UI-07 / P2 | `lib/queries/client.ts:25` 收到 401 仅 clearToken，`features/connection/connection.tsx:69` 的 ready 状态与缓存清理未同步。 | 401 后连接失效、停轮询、禁写、明确重连；旧数据若保留必须标失效快照，重连不能闪现上一连接数据。 |
| UI-08 / P2 | `detail/overview/question-card.tsx:132` 不显示 Worker ACK；overview 只留 open，回答后可能消失。 | 区分已受理/已投递/已消费/未知，延迟 ACK 时仍能核对；Leader 问答不能伪造 Worker ACK。 |
| UI-09 / P2 | `features/workers/worker-drawer.tsx:26` 无 Tab trap；与嵌套取消 Dialog 的 Escape 监听并存；回调变化可能重置焦点。移动导航也需同审。 | Tab 不进入背景；一次 Escape 只关闭最上层；轮询不抢焦点；关闭返回来源控件。 |
| UI-10 / P2 | `detail/overview/leader-request-card.tsx:30,245` 到期只提示，不阻止允许按钮。 | 到期立即禁用回答/授权，确认框打开后到期也不得发请求；保留后端拒绝兜底。 |
| UI-11 / P2 | `.github/workflows/node-team.yml:4,49,81` 未包含 UI 路径触发、UI 测试/build；干净 checkout 的候选可没有 ignored dist。 | UI-only 改动触发 typecheck/test/build；声明带 UI 的候选必须含 dist/index.html，安装目录同源打开通过。API-only 仍可存在，但明确标识。 |

上述 `detail/` 简写均为 `apps/task-web/src/features/tasks/detail/`，`transport/` 为 `apps/task-web/src/lib/transport/`。真实验收合同见 `packages/task-api/openapi.json:4701`、`packages/task-application/application.mjs:333`、`execution.mjs:452`。

## 4. 产品设计、易用性与视觉建议

### 用户应一眼回答的问题

任务详情首屏应回答：要完成什么、现在到哪一步、谁在做、是否需要我、下一步如何处理、交付是否真的通过检查。技术摘要/revision/证据 ID 保留在可展开诊断中，不与下一步行动争抢主层级。

1. 待批准计划已计入待办数量，但应在顶部提供“查看并确认计划”定位入口，不能让用户向下寻找。
2. 新建任务默认自然语言目标、附件与普通上下文；JSON 放高级选项。不得改变 API 含义或把自由输入直接作为授权。
3. 团队页优先角色、当前阶段、最后有效进展、异常和详情入口；依赖先做可定位的节点/依赖视图即可。不要把可视化 DAG 升级为流程编辑器或擅自生成进度百分比。
4. 成果页突出可查看/下载的交付物及各自检查；执行完成、候选、评审、验收、发布和后验分别呈现。
5. 错误信息先给“现在能做什么”，原始 code/requestId 放诊断区。连接页当前声称“请求没有到达服务”过强：网络异常不能普遍证明这一点，应改为“未取得有效响应”。

### 风格、主题、美观性

实看连接页：浅灰背景、白色居中卡片、蓝色主按钮，视觉克制且一致；没有必要换成炫技大屏。主要问题是大段 origin/token/存储说明置于操作前，辅助字偏密，缺少可执行的首次使用指引。应采用渐进披露：简短连接目的→输入与主动作→折叠的安全/恢复说明和准确的本机指引。不建议 token 持久化或拼 URL 来换取方便。

全站继续复用现有语义色与明暗主题；明确标题、状态、正文、诊断四层字号/权重，减少全等权卡片。列表密度优先可扫描性；关键空态提供下一步，非必要字段不占整行。状态不能只靠颜色，危险动作与主动作分组，长 ID 可复制但不挤占标题。

连接页之外的美观性意见来自组件/设计系统审查，尚非全页面截图结论。不要宣称深色对比、375px 布局、200% 缩放或键盘已通过。现有设计要求主触点 44px，实际部分移动按钮 36px，应统一核验点击区域而非只看图标尺寸。

## 5. 实施分组与防返工

建议三组交付，不按每条 finding 拆小 PR：

- A：UI-01/02/03/06/07——连接、Transport、逻辑动作一致性，先定位浏览器阻塞；这些共享底层，安排同一作者。
- B：UI-04/05/08/10——任务语义、分页、问答/授权完成路径；与 A 先冻结小接口再并行。
- C：视觉/键盘及 UI-09/11——统一模态、布局与浏览器/发行测试；避免与 A/B 同时重写相同详情组件。

实现者提交精确 SHA、变更范围及验证结果，本审计方负责验收。修复代码作者不以自己的测试替代独立审查。不得借本报告恢复旧 Marshal Skill、重建 UI 架构、放宽业务授权或重新启动暂停中的通用文件团队任务。

## 6. 最终验收清单

1. 上述每条 ID 有回归用例或可复现证据；P1 清零，P2 明确处理/接受延期，不以测试数量代替路径通过。
2. 在真实浏览器与真实 HTTP 服务下完成连接、新建、确认、问答、成员详情、成果下载；fixture 与真实模型证据分开标注。
3. 覆盖 504 已提交、断网、401、409、轮询改变 revision、延迟旧响应、问题到期、至少 51 个 Worker；无重复 Task/操作与错误成功提示。
4. 列表、详情、确认、空态、错误：1440/1024/375px；浅色/深色；200% 缩放；键盘模态/焦点返回；无整页意外横向溢出。提供截图及基线 SHA。
5. 按设计目标用受控 100 Task/500 event 数据测首屏、持续轮询和长列表，记录环境与实测值，不先引入虚拟化等新复杂度。
6. 干净 checkout 验证 UI build 和发行内容；安装产物实际 `/ui/` 可开。组件 Vitest、Node HTTP 测试、浏览器验收三者不能互相冒充。

验收结论只绑定被测 SHA 与矩阵。当前结论为“架构保留，存在待修功能和体验问题，UI 尚未通过本次完整验收”，不是否定已发布 API 能力。
