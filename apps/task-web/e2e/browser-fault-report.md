# 2026-09-11 受控浏览器异常交互记录

## 绑定与结论

- 产品基线：`50f4bc512e5132bcf890ab18a15cf39d28cb9340`，独立 `ui-browser-fault-acceptance` worktree。产品代码未改；测试脚本 SHA-256 `422c7bb519f2c3b15f187a44f96792f876f39ec582f2597e5c616b724795ef99`。
- macOS 本机、Node `v24.15.0`，1440×1000 独立 headless Chromium `152.0.7977.84` / Playwright WebKit `26.5`。WebKit 不等于 Safari 人工验收。
- `npm ci --offline --ignore-scripts --no-audit --no-fund`、`npm run build` 退出 0；显式执行 README 所列脚本，两个引擎均退出 0，原服务 SIGTERM 退出 0。
- HTML 摘要 `2102ac1d56963778d9efbee2ef87e3e640b93faa95310de89022da8b590424b1`；实际使用构建资产，不使用 Vite 开发页。

## 三线结果

| 验收线 | 此轮证据 | 结论与缺口 |
| --- | --- | --- |
| 功能与可靠性 | 每引擎真实服务/SQLite/既有 ACP Provider，两个 Task，浏览器实际提交答复、批准、取消；原请求取得真实 202 后仅丢弃浏览器响应 | 本表列明子场景通过。未调用真实模型，未核验批准后整 Task 业务交付或 publication |
| 视觉与交互 | 两引擎截图；实际点击、Enter、Escape、设置跨路由；目检未知操作栏与计划预览可读，无连接字段 | 所测桌面片段通过；未覆盖完整缩放/窄屏/主题/键盘矩阵，不替代完整视觉验收 |
| 产品可用性 | 无独立目标用户参与 | 未测，不能以 Agent 自动化替代 |

| 场景 | 实际断言 | 未覆盖 |
| --- | --- | --- |
| E20 | `leader.reply`、`task.approve`、`task.cancel` 丢回执后切设置页仍有未知提示；每动作仅原提交与一次显式重放，原键/原正文完全相同；批准/取消 Operation ID 相同 | 浏览器关闭、断线重连、其他写动作 |
| E31 / E07 | 真实 Leader 业务问题→文本答复→原计划预览→确认批准 | 经典 `task.answer`、409、新摘要竞态、发布授权 |
| E14 / E25 | 取消框 Escape 后 Task 仍 awaiting-answer；重新 Enter 明确取消；最终 cancelled；原键重放前后公开 Task+audit 完全不变 | 运行中 Worker 取消竞态、完整键盘走查 |

## 保留证据

相对本 worktree（均被 Git 忽略，不提交运行态）：

- `.marshal/evidence/browser-fault-chromium-iyq4DH/evidence.json`：Task `task-d6306c59-142e-4a11-a25a-0d95c82c8ed0` / `task-ecd7080c-16e0-428c-a631-be2a190b9bd4`。
- `.marshal/evidence/browser-fault-webkit-4p57Lq/evidence.json`：Task `task-f3aafd22-5043-4135-84f1-d7b91bda2c0c` / `task-beff7c71-25d7-44c7-a603-4fe15a5347a3`。
- 每目录含 `leader-reply-unknown.png`、`plan-preview.png`、`approve-unknown.png`、`cancel-unknown.png`；最终两引擎未知栏截图已目检，前次同产品双引擎的计划预览/取消栏也已目检。
- 私有短路径运行根由各 evidence.json 指出，SQLite 与原 fixture 日志保留；不得发布连接文件或原日志。证据摘要只记录正文/键哈希、状态、Task/Operation ID，无 token/请求头，不生成 HAR/trace。

早期试跑保留两个失败证据：深层 worktree 运行根导致服务启动失败，改用短路径；设置页无主导航导致测试定位失败，改为实际「返回工作台」。均为测试运行环境/定位修正，不计入通过，不据此修改产品。最终双引擎运行使用同一上述脚本摘要。
