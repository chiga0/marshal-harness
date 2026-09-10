# UI-1 独立验收运行记录

日期：2026-09-10。设计基线 commit `5bbfa0f4`；实现基线：分支 `feat/ui-1-develop` HEAD（推送后随 main 相符）。

能力范围声明：UI-1 交付为「已配置业务能力内的任务全流程」。自动化证据只覆盖 HTTP 层与组件层可判定事实；浏览器矩阵、真实 Provider 浏览器完整交付、性能目标与 Safari 人工冒烟按本表如实列入待用户实测，不预填通过。

## 环境与候选

- 执行机：darwin arm64，Node v24.15.0（本机 fnm 固定入口），仓库 worktree `.qwen/worktrees/ui-1-develop`。
- 构建产物（`npm run build` 后 dist 资产摘要，候选字节）：
  - `dist/index.html` SHA-256 `ae7a43b112e4a1d83f17d7a6280d149ece0d80e9d4153016627d700e34047278`
  - `dist/assets/index-DZfzWKrN.js` SHA-256 `14d91df7f9fbd8e9f53468680463cf88f327a4ead7da2b5995fc0e28d71cdcb4`
  - `dist/assets/index-CE401mPK.css` SHA-256 `83f544e45f3e08f51a274d1df6480dd676da05052904d78261601b48c3608a6b`

## 自动化证据（全部本机真实执行）

| 证据 | 内容 | 结果 |
| --- | --- | --- |
| `npm run typecheck`（apps/task-web，`tsc -b`，strict+exactOptionalPropertyTypes） | 全代码类型门禁 | 0 错误 |
| `npx vitest run`（apps/task-web） | 组件/transport/连接/e2e 服务侧全套 | 24 files / **155 tests 全过** |
| `node --test packages/task-service/{composition,managed-diagnostic,api-stable-behavior,launch}.test.mjs` | 既有服务套件回归 | 31/31（launch 11/11 证明未启 --ui 时行为逐字节不变） |
| `node --test packages/task-distribution/{team,v7-consumer,index}.test.mjs` | 发行 manifest/恢复语义回归 + UI 资产纳入 | 17/17（无 dist 时旧摘要不漂） |

e2e 断言面与场景对应见 `apps/task-web/e2e/README.md`（E01/E02/E22/E26/E27/E29/E32 服务侧面）。

## 独立评审

- 范围：`git diff 788d328b…HEAD`（~100 文件），六条防线逐一证据核对（合同真值/幂等/token 内存/ADR0098 边界/验收诚实性/发行兼容）。
- 结论：**P0 = 0**；P1 = 2（断开连接未清 Query 缓存与 E27 不符、ADR0098 §8 离开前提示未实现）——**均已在本落稿前修复并补回归测试**（`connection.test.tsx`、`logical-action.test.tsx`）；P2 = 7，其中 P2-1/P2-2/P2-4/P2-6 已随本次修复，P2-3（Error.allowedActions 未透出，error-codes 闭集映射覆盖恢复指引，决定保留简化）、P2-5（createTransport token 形参为占位，权威恒为模块单例，已有注释）、P2-7（distribution 打包侧 nlink 硬化口径与运行时 read 兜底不齐，非漏洞）登记为已知处置项。
- 评审可进入验收结论：**可以进入**，两条 P1 关闭后无未解决 P0/P1。

## 场景状态表

标签：AUTO_PASS=自动化证据通过（注明证据）；MANUAL=自动化不可判（浏览器/真机/真实 Provider），待用户实测，附复核路径；BLOCKED=外部动作所要求。

| ID | 需求 | 状态 | 证据与环境 | 备注 |
| --- | --- | --- | --- | --- |
| E01 | P01 连接与就绪 | AUTO_PASS（服务侧+组件） | `e2e/real-http.test.mjs`（201→awaiting-answer→列表重查一致）、`connect-page.test.tsx`；token 不落盘另见 E27 | 浏览器手测「刷新后需重新连接」画面用语由用户实测 |
| E02 | P01 凭据错误/不可达 | AUTO_PASS（服务侧） | `e2e/real-http.test.mjs`（401 unauthorized / 连接失败区分） | — |
| E03 | P02 分页与筛选 | AUTO_PASS（组件） | `task-list-page.test.tsx`（nextCursor/id 去重/局部范围声明/待处理开关） | — |
| E04 | P03 超限与草稿保留 | AUTO_PASS（组件） | `task-new-page.test.tsx`、`task-create.test.ts`（8192B/32768B/256KiB/32refs 边界 + 草稿保留） | — |
| E05 | P03 重复创建与 Idempotency-Key | AUTO_PASS（组件+服务侧） | `task-new-page.test.tsx`（防双击/同键显式重放）、`api-stable-behavior.test.mjs`（同键重放/409） | — |
| E06 | P04 零问题确认 | AUTO_PASS（组件） | `plan-approve.test.tsx`（快照冻结 + expectedRevision/planRevision/planDigest） | — |
| E07 | P04 问题-答复-确认 | AUTO_PASS（组件） | `question-card.test.tsx`（预批准分支 previewDigest 绑定）、`overview-view.test.tsx` | — |
| E08 | P04 双页版本争用 | AUTO_PASS（组件） | `plan-approve.test.tsx`（409 保留快照，不自动替换摘要） | 双标签页真人回归并入 E25/浏览器手测 |
| E09 | P05/P08 双 Worker + 单等待 | AUTO_PASS（组件） | `workers-view.test.tsx`、`overview-view.test.tsx`（真实等待项、无伪造百分比） | — |
| E10 | P06 预解答与延迟 ACK | AUTO_PASS（组件） | `question-card.test.tsx`（受理≠消费叙事）、runtime 分支 questionDigest 绑定 | — |
| E11 | P06 串题与过期 | AUTO_PASS（组件） | `question-card.test.tsx`（expired 禁答/串题拒绝） | — |
| E12 | P07 暂停-恢复 | AUTO_PASS（组件） | `task-controls.test.tsx`（暂停=止新调度文案、恢复按 allowedActions） | — |
| E13 | P07 单 Worker 取消与 501 | AUTO_PASS（组件） | `cancel-worker-flow` 测试（501 不回退取消整 Task） | — |
| E14 | P07 取消竞态 | AUTO_PASS（组件） | `task-controls.test.tsx`（受理≠已停止） | — |
| E15 | P09 候选/部分 | AUTO_PASS（组件） | `artifacts-view.test.tsx`（kind 分组、partial 徽标、「不代表完整交付」） | — |
| E16 | P09 最终成果下载 | AUTO_PASS（组件） | `downloader.test.ts`（SHA-256 复算 + bytes 复验后才保存、路径消毒） | — |
| E17 | P09 越权 / 8MiB | AUTO_PASS（组件） | `downloader.test.ts`（摘要不符拒存）；8MiB 为设计 UX 注（合同上限 8388608） | ArtifactRecord 绑定 taskId，UI 不提供跨任务访问入口 |
| E18 | P10 Leader 授权允许/拒绝 | AUTO_PASS（组件） | `leader-request-card.test.tsx`（authorization 全文、allow/deny 两分支、authorization 缺失禁过） | — |
| E19 | P10 后验失败/unknown | AUTO_PASS（组件） | `artifacts-view.test.tsx`、`acceptance-panel`（leader.postverify.status 六态如实，不重复发布） | — |
| E20 | P11 断线后重放核对 | AUTO_PASS（组件） | `logical-action.test.tsx` + 各写操作 unknown 面板（显式原键重放） | — |
| E21 | P11 轮询乱序/去重/隐藏页 | AUTO_PASS（组件） | `derive.test.ts`（numeric revision 拦截）、`task-detail-layout.test.tsx`、hidden 30s 节奏 | — |
| E22 | P11 服务重启与每次连接要求 | AUTO_PASS（服务侧） | `e2e/real-http.test.mjs`（SIGTERM 后 `--mode open` 重查、token 换发） | 客户端重连提示画面用语由用户浏览器实测 |
| E23 | P08/P11 usage=null/501 | AUTO_PASS（组件） | `workers-view`/`worker-drawer`（usage.source=unavailable 显式「不可用」） | — |
| E24 | P12 主题与窄屏/缩放 | MANUAL | 组件层 `settings-page.test.tsx` + 抽屉测试已覆盖状态切换与折叠逻辑 | 需浏览器实测 1440/1024/375px 与 200% 缩放 |
| E25 | P12 仅键盘 | MANUAL | 组件层 dialog/抽屉焦点置位、Escape、Tab 圈禁回归已加 | 需纯键盘全流程浏览器实测 |
| E26 | P01 外 Origin/Bearer 源头 | AUTO_PASS（服务侧） | `e2e/ui-boundary.test.mjs`（外站/相似后缀/错端口/null/双值 → 403；API-only 兼容反例） | — |
| E27 | P01/P12 token/存储清理 | AUTO_PASS（服务侧+组件） | `e2e/ui-boundary.test.mjs`（dist/响应/stdout 无 token）、`client.test.ts`、`connection.test.tsx`（断开清 token+查询缓存） | — |
| E28 | P09/P12 危险内容 | MANUAL | 代码事实：UI 不使用 HTML 渲染组件（无危险插值），React 文本插值不执行；证据为静态代码审计 | 浏览器层恶意内容回放实测待用户验收（XSS/Markdown/外链） |
| E29 | P13 安装与兼容 | AUTO_PASS（打包语义） | `task-distribution/index.test.mjs`（manifest/恢复）、W3 发行 smoke（同包 `--ui` 真实启动）在 e2e | 第二宿主（Linux）安装运行并入真实环境验收（历史 Linux 验收通道可用） |
| E30 | P13 回滚 | BLOCKED（可选） | — | 可选：未经用户升级操作不回滚 |
| E31 | P06 Leader business pendingRequest | AUTO_PASS（组件） | `leader-request-card.test.tsx`（business 分支 answer+requestDigest+expectedRevision） | — |
| E32 | P01/P13 静态路径与 CSP | AUTO_PASS（服务侧） | `e2e/ui-boundary.test.mjs`（遍历/符号链接/HEAD/405/启动门禁） | CSP 浏览器生效性在 E24/E25 浏览器实测中一并观察 |

## 性能与可观测性目标

NOT_RUN（诚实状态）。100 任务/500 事件的本地压测与首屏 ≤2s 等目标未造数；自动化未覆盖。现场数据在用户实测时按 `acceptance.md` 目标记录。

## 待用户实测清单（附命令）

1. 构建候选：`cd apps/task-web && npm ci && npm run build`。
2. 启动受控实例：`node packages/task-service/main.mjs --ui apps/task-web/dist`（先按 docs 配置受控 Provider/受控目标）。
3. 浏览器矩阵（Chromium/WebKit 自动化/Safari 人工）：复核 E24/E25/E28 及 E22 客户端提示画面、E29 安装路径与 E16 独立目录消费。
4. 真实 Provider 完整交付（本机已具备 Pi/Qwen 入口）：创建报告型任务 → 确认 → 双 Worker → 独立验收 → 下载后业务核对 → 后验观察；第二 Provider 补单回合实测。
5. 性能目标：按 acceptance.md 的 100/500 数据量现场记录。

## 已知卡住

无阻塞外部动作。E30 为可选回滚条目，待升级一次执行后自然关闭或继续标注。
