# Qwen 接入事实与正式发布纠偏（2026-09-08）

## 已验证与未完成

- 本机 Qwen Code `0.23.0` 的帮助与已安装实现包含 `--acp`。`serve` 当前帮助标注 Stage 1 experimental HTTP bridge；不能因为支持 ACP 就把这个 daemon 宣称为稳定依赖。
- 另做一次真实 `--acp` initialize-only 探测（`protocolVersion:1`、空 clientCapabilities，无 session/new/prompt，保留原生登录入口）：进程在合法 initialize 响应前退出，尚未完成握手。该探测未保留 stderr，不能判断退出根因，不能据此归咎登录/配置或否定 ACP 支持。所属进程组已确认消失，不原样重复；后继需有界私有诊断并只报告脱敏原因。
- 后继维护者独立诊断已 **真实 initialize PASS**：同一本机入口与原生配置，发送后保持 stdin 打开，收到合法响应才关闭；返回 `protocolVersion:1`、`agentInfo.name:qwen-code`、`loadSession:true`，stdout 623 bytes、stderr 0、exit 0。未发送 session/new/prompt、未启动模型任务。它证明本机能进入 ACP，不推断上次退出的唯一原因，也不宣称会话/工具/恢复已经验收。
- 可复用组件 `packages/agent-acp/client.mjs` sourceHead=`90e64841a03d5b0afe35220866bc2f3139d73645` 已独立审查无 P0/P1；维护者定向测试 **21/21 PASS（约 0.34 秒）**、secret scan 和 merge-tree 通过，localMergeSha=`f3d1ab32d17ace2954059124baddebd2b4e1251e`。组件通过注入 streams 提供关联请求、会话绑定、进度、显式权限回调与 cancel；不 spawn/kill、不持久化或接纳业务结果。能力声明中的 load 不等于该组件已经实现恢复。
- 该组件的真实消费者也完成 initialize；精确 source 上进一步完成 Qwen `session/new`，sessionId=`9268f4c1-c56b-4a6b-8e39-3d2c97f0f2ab`，独立目录 `/private/tmp/marshal-acp-session-kFib0E`，exit 0、stderr 0。未发 prompt，未验证模型/工具/取消/恢复，不升级 B1/B2/B3；下一步必须接实际 Task，而非继续添加握手样例。
- Node 实验组合候选 `81a83e61fa6cb9fb427ee3ece4e2a40107007f03` 的六文件确定性测试为 **46/46 PASS**。它们覆盖有限 HTTP/DI/Schema/Provider/状态测试，不是正式发布验收。
- 同候选真实双 Qwen 测试耗时约 38.55 秒，两个作者都启动、返回候选并被收集；Task `task-4f4d5c8a-b175-42a0-b2f6-b92d0041baa7` 最终为 `failed`，原因 `independent-verification-failed`，Attempt 为 1。故障出在已收到的业务候选验收，不应误报成“未配置”或“Qwen 不能启动”。
- 重新执行本地固定 oracle 仍失败（`checks:6`、`verification_failed`）；没有再次调用模型。该次未走到成功下载及第二任务的真实取消测试，不能复用 Pi 的成功记录填补。
- 私有证据保留于 `/private/tmp/mnt-live-7L6Ne8/acceptance-summary.json` 与同根 `state/state.json`；摘要为 `incomplete`，sourceDigest 为 `sha256:b28d47936e32087146aa6b98f5c44c39600d333fd747e5f08bd88166cc9de940`。原始状态/输出不提交，可能含提示词和业务内容。持有的两个 worker/guard 均记录已清理，本轮检查四个实际 PID 已不存活；不按历史 PID 杀进程。

## 原因与纠偏

为检验 macOS 允许的 Node 启动方式而选择无工具、单次代码返回实验是有界检查，但继续扩展它不能交付用户所需的通用、长期运行服务。这里必须区分“Agent 启动/传输成功”“成果验收成功”和“正式产品稳定”，不能互相替代。

1. 不原样重试该固定样例，不把提升它的通过率当成正式版本关键路径。
2. Qwen 正式 Adapter 优先走原生 ACP 能力协商，而不是把它限制为无工具 JSON 生成器。版本记录用于可追溯和兼容诊断，不是精确版本白名单；不盲目承诺未来任意版本。
3. 保留原生配置/Skill，但权限请求不能扩大用户批准范围；问答、进度与取消通过应用接口接入。ACP session ID/日志不拥有 Task 的验收、预算或生命周期权威。
4. B2 的通用 Task/Artifact、问答/DAG/operations、SQLite 原子状态与恢复仍要实际实现；B3 长期运行、故障矩阵、安装部署、支持矩阵与正式发行仍要验收。46 个实验测试和任何一次握手均不能关闭这些出口。
5. API/DI/确定性测试与 ACP 接入可并行；存储/生命周期接口由一个集成者统一，避免多路各造一套 Task 真值。正式 Node 投影先明确对 ADR 0085/0087 的取代范围，不全量翻译旧 Core，也不隐式接管旧 `.marshal`。

最终目标仍为 **stable 正式发行**，不是再新增一个 Demo 目标。实验资产保留用于回归与失败学习，未获得生产资格。
