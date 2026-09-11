# 2026-09-11 成果追溯修复自检

## 范围与绑定

产品基线 `6838ac2c553c676c956af238337d9f0e7230a1a0`，独立 worktree `ui-artifact-traceability`。只改 UI 读取、展示及其测试；不改 HTTP 合同、SQLite、发布权限或物理路径访问。独立审查发现的 P1 为成果页只消费 Task.artifactIds，遗漏公开的输入观测关联及发布/后验制品引用。

实现：Leader 回执/后验证据经完整 LeaderView Schema 与当前 Task 绑定后合入原清单，按原 ID/Task/Artifact 校验并标来源；输入单独从完整 Audit Schema 与既有 Worker/Prompt/Observation 语义验证后的 contextRefs 读取，要求原 ID、kind=input、taskId=null。source=unavailable 不抹除合法输入引用；snapshot 不当作原始输入。输入 ready 才可下载，缺观测如实显示“关联未提供”，不声称完整输入或模型已消费。每批 100、并发最多 4，超出明确显示总关联数及加载更多。没有把上传 256KiB 或内部 32 引用上限误用到公开读取合同。

## 三条线

| 线 | 作者自检证据 | 状态/剩余 |
| --- | --- | --- |
| 功能与可靠性 | 33 个新增追溯反例/正例；成果相关60测试；全UI464测试；构建/typecheck；两个真实浏览器＋原HTTP/SQLite/受控ACP | PARTIAL：所测场景通过，待独立review与原实际业务/最终安装包复测；不算作者权威验收 |
| 视觉与交互 | Chromium152/WebKit26.5、1440×1000，真实进入成果页，三次Enter下载、落盘复验；截图目检可读，来源与非完整输入说明可见 | PARTIAL：长文件名/状态有折行，未完整覆盖缩放/窄屏/主题与Safari人工 |
| 产品可用性 | 无独立真人参与 | NOT_RUN：不能以Agent或作者自测替代 |

## 定向证据

- Chromium：`.marshal/evidence/traceability-chromium-tDdUMv/evidence.json`，Task `task-896f8e12-02d2-4702-840c-373c3f7cc1bd`。
- WebKit：`.marshal/evidence/traceability-webkit-2TVA8O/evidence.json`，Task `task-6bd99e96-0a75-4c7f-8a90-8946e350816f`。
- 每次原服务 clean exit 0、脚本退出0；三个原本不在 Task.artifactIds 的制品全部由真实浏览器保存：fixture-input.json 73B、publication.json 782B、publication-postverify.json 1074B。原输入字节相等，三者大小/摘要与公开元数据相等，下载后 Task 完全不变。
- 两引擎 HTML 摘要 `sha256:2c6f1b677e69244f82d242abcdf861da1408f063455cf62e441c1a6ce00aed1e`；脚本摘要 `sha256:a47012775400379c45367de7cb96538bc28c4f431267a4683439d3c09b7bdc79`。运行时尚未提交，候选以这次补丁/构建绑定，不把基线单独冒充修复后源码。
- 新增反例覆盖跨 audit Task/Worker、未关联 Worker、非法/缺失/重复引用、旧记录、metadata-only、错 Artifact ID/Task/kind、坏长度/摘要、404、partial/unavailable禁下载、同长摘要篡改拒存、跨Task迟到/清连接缓存、128引用分批/并发、回执去重来源与串Leader。元数据校验直接消费现有OpenAPI，浏览器适配沿用原contract.mjs有界校验与审计绑定语义。
- 同长篡改测试发现 jsdom Blob→Node Response 会变成同一字符串，已用测试运行器的原生 Node Blob 修正测试数据机制；未放宽摘要断言，浏览器实下载独立通过。

命令：`npm ci --offline --ignore-scripts --no-audit --no-fund`；`npx vitest run src/features/artifacts`；`npm test`；`npm run build`；`git diff --check`；按 README 指定模块/浏览器执行两次 `browser-traceability-acceptance.mjs`。构建因合同校验引入 Schema，JS约531.5kB/gzip152.5kB，出现Vite500kB提示，未修改阈值隐藏；不是构建失败，也未测性能出口。

受控 fixture 的发布仅写新临时目标，不是正式业务发布、真实模型结果或新软件发行；原业务服务与数据没有写入。最终 UI 仍不宣称通过完整验收。
