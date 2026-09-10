# 通用文件团队（实机验收中）

按 ADR0099 提供 `createGenericFileTeamConfig({executable, env?})`，供 `marshal serve` 无显式业务配置时接入。`executable` 是本机发现的 Qwen 绝对入口，使用原 `--acp` 与原登录；构造配置不启动模型，不代表协议或登录已通过。Provider `availability` 保持 `unknown`，首个真实请求才检验原生认证与模型能力。独立测试不代表开箱即用验收已完成，更不代表任意 Agent Skill、SQL 执行或外部发布已可用。

纯布局函数 `bindGenericFilesPlan` 复用原 `TaskVerification.bind`，支持 1–8 个作者、一个 verifier 汇合点、并行或依赖执行、0–16 个输入；节点名和业务描述不写死。原输入通过 `inputs/<artifactId>` 引用，上游成果通过 `upstream/<nodeId>.md` 引用，作者在独立目录生成 `result.md`，最终分支按 `results/<nodeId>.md` 交付。自然语言 scope 不是文件权限，作者不能生成 verifier 的权威证据。

## 装配与证据范围

上述数量是上限而非任意组合保证：布局还受原 Core 的单项可见材料与聚合字节限制。每份输出必须非空、合法 UTF-8、无 NUL，最多 16384 bytes；Leader/Review 真实展开材料总量仍受原 196608 bytes 限制。过长 ID、高扇入、二进制输入或上下文超限时明确失败，不截断上下文、不放宽原门禁。

- 同一 Provider 的不同原执行分别承担 Leader、作者与独立 Review。Review 消费摘要核验后的真实原输入及全部当前选果正文，检查原需求覆盖；不能用作者或 Leader 的自评替代。
- 固定 Node checker 读取验收目录的实际文件、拒绝额外文件/链接，并报告实际字节摘要。父进程按冻结 manifest 独立比对，交付 bytes 再从原 Depot 重验；checker 不执行候选代码、不证明语义或外部效果。
- 最终交付 `results.json`，媒体类型 `application/json`，包含 `profile:task-generic-files-delivery/v1`、`taskId`、`planDigest` 与 `files:[{nodeId,path,digest,bytes,content}]`，其中 `content` 是完整原 UTF-8 文本。既有 HTTP artifact 下载可直接消费。
- 默认无 publication、自动 repair 为零；内容拒收不自动重试。默认原限额为 10 分钟、24 Attempts、3 Workers，Leader 最多 12 次语义调用和 4 次必要问题，不要求耗满。
- Qwen 权限仅接原生 `file_path` 读、写及编辑闭集，并只选择 `proceed_once/allow_once`。未知参数、路径、工具类别、shell/网络/MCP 均不授予权限；文件准入和同 UID 进程不是恶意代码沙箱。

DataAgent 的 MCP/CLI、SQL 发布及补数另有工具归属和外部效果接缝；当前 ACP 的 `acp_tool_scope_unproven` 不会被本包移除。默认只交付成果，不授权外部写。要求真实外部效果的原目标应澄清或拒绝，不得把文件交付说成 SQL 已执行或数据已发布。

`runtime.test.mjs` 使用显式无模型 ACP 进程，经实际 HTTP、SQLite、原 owned runtime、独立 Review 和固定 checker，测试两种意图/不同作者数、内容拒收、多余文件、权限边界、取消及冷开同 bytes。`createGenericConfig({provider})` 是受信组合 DI，不是 HTTP 可配置端口；夹具不进入发行库存。真实 Qwen 两种需求及全部正式门禁仍需单列证据。
