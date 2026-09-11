# 任务工作台产品审计与实施交接建议（2026-09-11）

## 文档性质与证据基线

本文是供后继 Agent 实施的审计建议，不是新合同、已接受设计或完成声明；不改变 Roadmap、Milestone、API-STABLE 或已有 UI-1 状态。代码核对锁定 `main=0f9801597d394088681d32ce24bf615bc27fc8c2`。主负责人补充：交互及成果验收修复已随 PR #290 合入 `811f0aa06e72eb391b40abd771e91f42caafb88e`；本文的新工作台、DAG、独立分组设置建议尚未实施，不能把基线缺陷当作最新主线仍未修复。

本轮只读检查了 UI、公开 OpenAPI、Application 投影与服务组合；未启动模型、业务 Task 或进行新的浏览器验收。用户截图作为设置布局意图，不作为 Marshal 具备截图中产品能力的证明。以下优先级是实施排序，不冒充实测故障严重度。

## 用户五点反馈的实施化整理

1. **成熟任务工作台**：从零散信息卡组织成任务列表、当前任务、需要处理的问题、进展与成果的连续工作流，回答“正在做什么、在等谁、我要做什么、成果是否可信”。不是新建项目/Workspace 或聊天平台。
2. **实时 DAG 与分工**：看见真实依赖、节点目标和状态、角色、实际 Worker 与执行历史；实时指有界轮询与新鲜度提示，不是模拟动画、假百分比或未实现的推送协议。
3. **独立设置与左侧分组**：主导航左下齿轮进入独立设置；设置左栏按组导航，提供明确的 Back 返回工作台。借鉴用户提供的 Codex 截图结构，不复制其身份、模型、插件等能力。
4. **统一主题与语义样式**：以 semantic tokens 统一表面、文本、边框、强调、危险/警告/成功、焦点与选中态，覆盖浅色/深色；不逐页硬编码颜色或建立第二套主题系统。
5. **Agent 与 Sandbox 配置可理解、可操作**：先分清当前已配置 Provider、任务内 Worker 实例和执行环境；已有只读能力立即呈现，缺失的配置写入能力明确作为服务端后继，不能用假表单假保存代替。

## 当前能力与证据

| 接缝 | 基线事实 | 对实施的约束 |
| --- | --- | --- |
| 工作台与详情 | `apps/task-web/src/features/tasks/detail/task-detail-layout.tsx:50` 已轮询多个真实投影；Transport 类型集中在 `apps/task-web/src/lib/transport/types.ts` | 可重排和补接公开投影，不在浏览器派生第二任务状态机；任务搜索/筛选仍只针对已加载项 |
| 动态图 | `packages/task-api/openapi.json:803` 已定义 GET graph；`:4361` 定义 GraphNode；`packages/task-application/application.mjs:318` 返回真实节点、边和 planRevision，未有计划时报 plan_conflict | 前端 Transport 尚未接 graph，但服务端无须新增图 API；不能只画静态 plan 后声称实时执行图 |
| 计划与实例 | PlanNode 有 goal/scope/providerId；GraphNode 有 status/workerIds；Worker 有 nodeId/providerId/role/attempt/progress/lastObservedAt | Worker 是一次任务执行实例，不等于 Provider 安装或配置项；一个节点可对应多个历史 Worker，不能只留下最后一个成功实例 |
| 验收与评审 | `packages/task-application/application.mjs:333` 的 audit.acceptance 独立于 leader.review；基线成果页混称问题已由上述后继合并修复 | 节点执行状态、Worker completed、Review accept、Task acceptance passed、发布/后验五者不得互相替代；新布局保留修复 |
| 设置 | `apps/task-web/src/features/settings/settings-page.tsx:1` 现为主题、连接和使用说明；`apps/task-web/src/index.css:1` 与 `lib/theme/tokens.css` 已有 token 基础 | 分组与返回导航是 UI 改造；主题工作应收敛现有 token，不声称目前完全没有主题系统 |
| Provider | `packages/task-api/openapi.json:2057` 为只读列表；`:5015` 的字段为 id/displayName/availability/coreCapabilities/enhancedCapabilities；`packages/task-service/composition.mjs:210` 返回 frozenFacts | 这是显式配置投影，不是自动登录/模型健康探测。服务说明 `packages/task-service/README.md:183` 明确默认 unknown，不发付费探测 |
| 配置写入与 Sandbox | 当前公开合同没有 Sandbox 清单/设置写入操作；CreateTask 未提供任意 Provider/profile 选择字段；`packages/task-service/main.mjs:207` 导入受信配置模块 | 不可用前端本地保存冒充服务配置，不可开放任意配置模块、可执行文件、宿主路径或 shell 编辑 |

## 建议交付一：工作台与真实轮询 DAG（优先）

复用现有任务、计划、问题、Leader、Worker、成果与 audit 查询。主区域突出当前任务和需要处理的事项；活动记录作为按需展开的证据，不默认淹没业务结论。动作仍按 allowedActions、原请求摘要与 revision 判断，409 后保留输入并要求核对，不能自动升级绑定重新提交。

新增前端 graph 读取与类型校验，使用真实节点状态画依赖图；图、可访问列表与 Worker 侧栏共用选择状态。按 nodeId 关联计划说明与 Worker 历史，只在 `graph.planRevision === plan.revision` 时拼接计划信息；不匹配时标记正在同步并重查，不把旧目标配新状态。Worker 分页未加载完时明确范围，不能宣称完整团队执行历史。

节点 completed 仅按 graph 状态显示为节点完成；不命名为“验收通过”。只有 audit.acceptance 提供 Task 独立验收读数；Leader 不可用不遮蔽 audit。等待/未知/陈旧必须可见，最后观察时间不是模型持续工作证明。保留 DAG 不可用时的列表替代，不添加拖动改依赖、任意重派或审批绕过。

### 可执行验收

- 合同夹具和真实同源服务分别验证无计划、空图、串行/并行依赖、waiting/failed/unknown、多个 Attempt 与长节点文本。
- 制造 graph/plan revision 错配与乱序响应：不混搭目标/状态；断线显示陈旧与更新时间；重连只恢复查询，不重放 mutation。
- 验证 Worker completed 但节点未完成、Review accept 但 acceptance pending/failed，以及 Leader 失败但 audit passed：不得显示虚假整体成功。
- 键盘可选节点并打开 Worker，窄屏可改为列表；轮询卸载可取消、有界刷新，无动画假进度、百分比或 ETA。
- 定向单元/组件测试后，以构建后的同源 UI 做浏览器验收；不能以静态 fixture 或 Vite 代理替代真实接入证据。

## 建议交付二：独立分组设置、Provider 只读与主题统一

主工作台左下设置齿轮须有文字/可访问名称；进入独立设置区域，左栏建议「通用」「连接与安全」「Agent」「执行环境」「关于与限制」。Back 返回合理的原任务/列表位置；直接打开设置没有历史时返回任务列表，不把浏览器后退到外站作为唯一实现。分组有稳定路由和明确选中态。

通用复用已有浅色/深色/跟随系统；连接仍只指向当前同源本机服务，token 仅内存。Agent 接已有只读 Provider 列表，展示声明能力及 unknown，不显示未经返回的模型名、登录状态或安装版本。执行环境在缺投影时明确“当前 API 未提供配置读写”，不预选 Local 或伪造隔离徽标，不放具有生效含义的保存按钮。

现有 `docs/ui-1/design-system.md` 已规定 Multica 参考、tokens、字体/间距、224 导航/320 右栏和截图验收；`tokens.css` 已存在。核心问题不是缺规范，而是规范→实现→视觉验收尚未形成用户期待的成熟体验；沿现规范完善执行，不另造同义设计系统。工作台、DAG、设置、弹层和危险操作共享表面/前景配对、状态及 focus/selected 规则。可参考 [shadcn 主题文档](https://ui.shadcn.com/docs/theming) 的 CSS 变量与背景/前景成对命名方法，不要求安装依赖或替换组件库；[Multica](https://github.com/multica-ai/multica) 仅为体验参考，不复制代码/图标/截图资产，不将其能力视为 Marshal 已支持。

### 可执行验收

- 从任务进入设置、切换分组、Back、直接访问分组、浏览器前后导航均可用；窄屏与键盘焦点不丢失。
- 浅/深/系统模式覆盖空态、错误、禁用、选中和焦点；颜色不作为唯一状态信号，文字对比度按现有 UI 可用性标准检查。
- Provider 空列表、分页、unknown、请求失败如实展示；同一 Provider 的多个 Worker 不误报多个已安装 Agent。
- token 不出现在 URL、Web Storage、日志或构建资产；断开/刷新清除；只读信息不可操作成“保存成功”。
- 查新增硬编码色值及重复 token，记录必要例外；跨页面截图验证一致性，不以测试数量替代视觉验收。

## 建议交付三：有限 Agent/Sandbox 真配置（先合同，后实现）

此组尚不能仅靠现有 UI/API 实现。先定义受限、非秘密的有效配置投影与配置版本，再决定是否提供受控更新接口。最小方向是选择已经声明且受支持的执行 profile，明确“仅影响新 Task”或“重启后生效”；不能把任意 JS 配置模块编辑器、动态插件/角色 CRUD 或统一身份平台包装成设置页。

写入合同至少需明确允许字段、验证、版本冲突、权限、持久化与失败回执、生效时间，以及在途 Task 保持原执行绑定。Provider 可用性、配置通过、原生认证成功和业务实测成功分别表达。Agent 继续管理自己的登录/模型/Skill，不复制秘密；普通 Local 子进程不是恶意代码沙箱。相关边界见现行 `docs/agent-team-service-architecture.md:42`、`:71` 与 `docs/adr/0098-local-browser-ui-boundary.md`。

新增公开操作或调整持久化/信任边界前需主负责人确认范围、更新对应合同并完成独立审查；命中 ADR 规则则先新增/替代 ADR。本文不授予该变更权限。Task 级 Provider/profile 选择若确有需求也必须补合同，不能偷偷往现有 CreateTask 添加字段。

### 可执行验收（作为后继出口，不表示已具备）

- 未保存不生效；合法配置保存后能从服务有效配置投影读回，重启保留；非法/不兼容值拒绝且不部分生效。
- 版本冲突不能覆盖较新配置；连接丢失/回执未知不自动重发；生效范围清晰，在途 Task 不切换 Provider 或执行环境。
- API、日志和制品无秘密，浏览器不能读取任意文件/SQLite、执行 shell 或修改原生登录。
- 清楚展示实际支持的执行 profile 和未实现能力；没有 OS 隔离证据就不显示强隔离承诺，不调用真实模型充当隐式“测试连接”。

## 交接与实施纪律

第一、二组可在现有公开合同内推进，第三组单列依赖，不为让页面可点击而虚构后端。按独立 worktree、锁定基线和单写者安排 scope；共享 Transport、路由与 token 由明确作者负责，其他切片等待接口冻结。每组交付记录准确提交、定向测试、真实浏览器证据与未过项；UI 成熟度不能扩大底层 Provider、恢复、发布或平台支持声明。

本次只新增本文，不修改规范、合同、产品代码或设计状态，不运行新版 UI 实现验收。主负责人已核对现有主题规范、设置页面、计划列表、Graph 合同和 Provider 投影；本文作为后继实施与验收的交接建议。
