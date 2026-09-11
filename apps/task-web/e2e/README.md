# UI-1 浏览器接入 e2e（不使用模型/浏览器驱动）

本目录是 UI-1 第三工作包（ADR0098 同源接入、发行资产与基础验收）的服务侧 e2e。全部用例通过**真实 `packages/task-service/main.mjs` 子进程**（锁定生产入口本身，而非开发 Vite/代理）验证：

- 正路径使用 `--ui <apps/task-web/dist>` 的唯一公开 127.0.0.1 端口：同源 HTML/资产、精确 Origin、`/v1` Bearer、无 CORS/无 cookie、no-store 与 CSP、错误回显不含宿主路径或 token；
- 反例包括外站/相似后缀/错端口/`null`/双值 Origin 与错误/双值 Host（`rawRequest` 逐字节构造请求行+头，避免 fetch/浏览器的 URL 归一化掩盖遍历输入）；
- 兼容面覆盖 API-only 模式边界（开启 `--ui` 与否同一入口）与旧 Node 客户端（`TaskClient` 基于真实 stdout 公告地址与私有连接文件）。

## 运行方式

```sh
cd apps/task-web
npm ci            # 锁定 lockfile；e2e 只依赖已有 devDependencies（vitest），无新增包
npm test          # 与 src 组件测试一起运行；e2e 位于 node 环境（无 jsdom 依赖）
npx vitest run e2e/ui-boundary.test.mjs   # 只跑边界套件
npx vitest run e2e/real-http.test.mjs     # 只跑真实往返套件
```

`dist` 缺失或陈旧时套件会先执行 `npm run build`（build-if-missing），参与测试的是**构建后的同源资产**而非 `vite dev` 输出。全部进程、SQLite、临时数据根均创建在 `os.tmpdir()` 内并在用例结束后清理；运行普通 `npm test` 已自动包含本目录，无需系统服务、凭据或浏览器。

## 断言面与验收场景对应

| 场景 | 位置 | 断言内容（服务侧） |
| --- | --- | --- |
| E01/P01 | `real-http.test.mjs` / `ui-boundary.test.mjs` | 同源 `POST /v1/tasks` 201 → `awaiting-answer`；列表两次重查条目一致；`task.leader` 挂起问题与 `requestDigest` 可观察；刷新/重连 token 不落盘属 UI 客户端职责（W1/W2 层另验） |
| E02/P01 | `real-http.test.mjs` | 错 token → 401 `unauthorized`（UI 据此停轮询）；服务停止后 `fetch` 抛连接异常，不伪造就绪 |
| E22/P11 | `real-http.test.mjs` | SIGTERM 正常关闭 → 同根 `--mode open` 重开：任务/Leader 摘要/列表事实可重查，连接文件与 token 换发，不重发 mutation |
| E26/P01 | `ui-boundary.test.mjs` | 仅精确单值 Host+Origin 放行；外站/相似后缀/错端口/`null`/双值一律 403 `untrusted_request`；API-only 模式下非空 Origin（含同源值）仍拒绝；无 Origin 的旧客户端形态不受影响 |
| E27/P01 | `ui-boundary.test.mjs` | dist 全文件、错误响应、stdout/stderr 均不含 token；响应不种 cookie、无 `Access-Control-Allow-Origin` |
| E29/P13 | `ui-boundary.test.mjs` | 同包 `dist` 资产经 `/ui/` 逐字节返回；content-hash 资产 immutable、HTML no-store；未启用 `--ui` 时 `/ui/` 404 且 API 兼容 |
| E32/P01/P13 | `ui-boundary.test.mjs` | `%2e%2e`/`..%2f`/裸 `..`/双斜杠/查询串/未知文件 → 404；`POST/PUT/DELETE /ui/…` → 405；HEAD 无正文且头一致；符号链接/缺失 `index.html`/未知扩展/非法文件名在启动期拒绝（`service_start_unavailable`）且不创建数据根 |

## 固定证据与限制

- 真实往返用例使用仓库既有的零模型受管 fixture `packages/task-service/leader-recovery.fixture.mjs`（设 `MARSHAL_LEADER_RECOVERY_FIXTURE=1`，fake ACP 真实 HTTP/SQLite/托管边界，与 `packages/task-service/leader.test.mjs` 同一机制）。`managed-diagnostic.fixture.mjs` 是显式失败注入包装，不用于正路径。
- 本套件刻意不引入 Playwright/真实浏览器进程与模型；浏览器渲染/键盘/视觉类场景（E24/E25/E28 等）必须在正式验收中另行执行。是否完成以对应候选的验收记录为准，不能从本目录或 CI 中的 `e2e` 名称推断浏览器矩阵通过；本目录只锁定可由 HTTP 层判定的边界事实。
- 中断核对提示（E22 用户可见面）、token 内存持有与刷新清除（E01 客户端面）由相应客户端测试及真实浏览器验收分别负责；本套件覆盖其后端的可考核部分。
