# UI 审计修复续验（2026-09-11）

基线：`0f9801597d394088681d32ce24bf615bc27fc8c2`。本批只处理 UI 调用链遗漏，不改变 API、持久化、授权或业务验收标准；通用文件团队分支继续暂停。

## 修正范围

- Leader 业务回答及发布授权：409 后不自动丢弃原状态；用户核对新版后显式重新开始，保留草稿并使用新 key/CAS。结果未知仍保留原键重放，不以刷新重发。
- 成果页：接入与概览相同的真实 `task.audit.acceptance`，评审、验收、交付及后验各自独立展示；Leader 不可用时也不隐藏已有验收事实。

## 基线真实浏览器与 HTTP 验证

在独立工作区构建 dist，使用 `leader-recovery.fixture.mjs` 与 `MARSHAL_LEADER_RECOVERY_FIXTURE=1` 启动真实 Node HTTP 服务；模型调用为零，没有业务发布或真实凭据使用。

- Codex 内置浏览器：正确 fixture token 连接成功→空任务列表→新建任务表单→合法上下文提交→服务端 Task 回执→详情显示原需求、运行状态、Worker 与验收 pending。此前 UI-03 的连接阻塞在本基线上未复现。
- 浏览器 Task：`task-8cef2da0-c23d-4f77-a249-6db2fd5ba2ad`。这只证明上述短路径，不等于完成团队交付、Leader 409 重提或发布后验。
- 浏览器团队页→Worker 明细→打开取消确认→按一次 Escape：仅取消确认关闭，Worker 抽屉保留，焦点回到“取消该 Worker”。没有提交取消；这是嵌套模态的实际浏览器冒烟，不代表全部键盘矩阵。
- 定向 `vitest run e2e/ui-boundary.test.mjs e2e/real-http.test.mjs --reporter=verbose`：2 文件、13 项通过（5.44 秒），包括真实 HTTP 创建至 awaiting-answer、正常重启、Origin/Host、静态资源和 API-only 边界。这些测试不是浏览器驱动。
- 验证前显式 `npm run build` 成功；不能依赖 `ensureDist` 自动发现陈旧资产（它只判断文件是否存在）。

## 尚未关闭的完整 UI 出口

整合候选的回归与独立 review 结果在本文件后续记录。深色/窄屏/200% 缩放、完整键盘流程、100 Task/500 event 性能、真实 Provider 的浏览器交付以及新版本同包安装发行仍未由上述短路径证明。v1.0.2 不含 UI，不能把主线源代码与已发布安装包混为一谈。
