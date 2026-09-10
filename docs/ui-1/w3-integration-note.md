# UI-1 W3 工程边界说明（浏览器接入、发行资产与基础 e2e）

> 本说明随 W3（ADR0098 同源接入、`task-service --ui`、`task-distribution` 静态资产、`apps/task-web/e2e`）交付，记录实现级边界与已验证事实。它不是新的需求或授权来源：合同/安全边界以 ADR0098 与设计包为准，验收结论以独立验收记录为准。

## 接入形态（一次实际决定）

ADR0098 要求“同进程静态托管、精确 Origin、原 Bearer”。在不改动 `task-api` 冻结边界（`protect`：非空 Origin 一律拒绝）前提下，`packages/task-service/main.mjs` 以显式 `--ui <buildDir>` 增加一个**同进程第二监听**：

- 组合根 API 继续监听一个仅 loopback 的内部端口（连接文件 `{url,token}` 指向它，旧 Node 客户端与既有 `launch.test` 事实不变，token 仍不回显）。
- `--ui` 启用时，启动输出中的 `address` 指向新的公开 127.0.0.1 端口。该端口是唯一浏览器入口：先执行 ADR0098 §2 边界（单值精确 Host、单值精确 Origin 或旧客户端形态无 Origin），然后 `/ui/` 走冻结静态清单，其余请求经**无会话、无状态、逐请求中继**进入内部 API 监听。中继不转发 `Origin`/`Cookie`/逐跳头，不附加任何 CORS 头，不改写 Host 期望之外的内容；内层 `protect` 的 API-only 规则保持不变，同源放行不是对 `protect` 的绕过而是前置的 UI 模式裁决。
- 拒绝面：非精确/双值 Host、非空但非精确 Origin（含 `null`、外站、相似后缀、不同端口）→ 403 `untrusted_request`；未知文件/遍历/查询串/双斜杠 → 404 `not_found`；`/ui/` 非 GET/HEAD → 405 `method_not_allowed`；错误体均为原 API 错误信封（`code/message/requestId/allowedActions`），不回显宿主路径。中继超时/内层不可达 → 503 `application_unavailable`，语义同原“命令效果须查询”。
- 静态清单在启动时一次性冻结：逐文件 `lstat`/`O_NOFOLLOW`/类型/归属/名称/扩展/数量与总量锁定，字节读入内存后按 manifest key 精确匹配服务；运行期不再读盘，升级或替换产物必须重启服务。符号链接、非常规文件、缺 `index.html`、未知扩展、URL 编码（`%` 一律拒绝解码）等全部启动期/请求期拒绝。
- 响应头：HTML `no-store`；`assets/` 下带 content-hash 名（`-[A-Za-z0-9_-]{8}.`）的文件 `public, max-age=31536000, immutable`；其余静态 `no-store`。全部 UI/API 响应带 `X-Content-Type-Options: nosniff` 与 `Content-Security-Policy: default-src 'self'; script-src 'self'; connect-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; object-src 'none'; frame-ancestors 'self'; base-uri 'self'; form-action 'self'`；UI 静态另带 `Cross-Origin-Resource-Policy: same-origin`、`Referrer-Policy: no-referrer`。无 `Set-Cookie`、无 `Access-Control-Allow-*`。
- 未启用 `--ui` 时行为逐字节保持原样：stdout 仍只有 `profile/address/connectionFile`，无第二监听，`/ui/` 为 API 404，非空 Origin 仍 403。

仍属 ADR0098 已声明的限制：浏览器 XSS、同 UID 恶意代码不在隔离承诺内；token 全权访问风险沿用可信单用户边界；第二监听与内部监听同处 loopback，不绑定其他地址，也没有把连接文件改写为 UI 地址（它是旧 Node 客户端的稳定入口）。

## 发行资产

`task-distribution` 的 `pack/verify/restore-carrier` 现识别源码树中本地构建的 `apps/task-web/dist`：严格名称/扩展/数量/符号链接规则与运行时一致，文件按字典序追加在锁定 `SOURCE_FILES` 之后，共用同一 manifest 摘要与 `0700/0600` 安装树。dist 缺失或为空时打包结果与原清单逐字节一致（旧消费者/CI 零漂移）；UI 文件不在 Git 中，不经 `inventory` 覆盖，也不携带任何 `node_modules`。`index.html` 是绝对 entrypoint；升级不混版（旧 HTML 与新包 hash 资产不共存），因为替换产物必须重启服务。

## e2e 与未覆盖项

`apps/task-web/e2e/`（vitest、node 环境、零新增依赖）两套件：`ui-boundary.test.mjs`（边界/静态/反例/启动门禁/API-only 兼容）、`real-http.test.mjs`（E01 最小同源往返 + E02 401/不可达 + E22 正常重启重查），真实进程使用仓库既有零模型 `leader-recovery.fixture.mjs`。断言面映射见 `e2e/README.md`。

本包未覆盖：真实浏览器渲染/键盘/视觉（E24/E25/E28）、Playwright 级矩阵、token 内存持有与中断核对提示的客户端面（W1/W2 浏览器层）、正式发布验收。作者自检只是线索，不能替代独立验收者在冻结候选上的执行（验收文档 E01–E32 的完成方式不变）。
