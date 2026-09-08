# Node-only 本地 Agent Team 实机记录（2026-09-08）

## 本轮问题与结论边界

用户明确要求验证：通过允许的 Node 运行方式启动 HTTP，驱动两个本机 Agent，收集结果、取消并验证重启恢复，全程不调用 Marshal 原生二进制。[ADR 0087](adr/0087-node-local-team-feasibility-probe.md) 先冻结独立实验边界；本记录不替代 ADR 0085 的 Go 主线证据，也不宣布全面迁移或正式发布。

实现与操作说明：[Node 实验入口](../experiments/node-team/README.md)。最终实机候选 `c88410b7eb8da15f5da37259c43ecb7808e9bfe5`；Node `v24.15.0`、Pi `0.84.4`，使用操作者本机原有 Pi 入口与原生配置，无模型切换或密钥复制。

## 已验证的无模型链路

- 两路独立作者分别实现 HTTP/内置 supervisor/耐久状态/所属组控制，以及 Pi 事件接入/业务契约/独立验收；集成者实现独立 HTTP 与实机客户端。
- 最终候选的 23 项确定性检查全部通过，20.16 秒（串行测试文件）。单个任务内部仍真实并行两个 Node 替身，不因测试文件串行而把产品作者串行化。
- 覆盖确认和重放、错误 revision、真实 guard 组取消、验收与取消竞态、输出超限/失败、未知清理义务拒绝新任务、状态漂移拒绝、HTTP 活跃前端重启和终态保留。
- 同一独立 reviewer 完成聚合审查及修复复核，候选未决 P0/P1 为 0。作者不能自签业务成功；服务使用固定独立 oracle，客户端再对 HTTP 下载内容独立消费。

## 真实任务状态

**最终有界实验通过**：候选 `c88410b7eb8da15f5da37259c43ecb7808e9bfe5`，运行源码摘要 `sha256:980115881c7e1ea191c0bf7d70250a8781b311390eda75760a2d474a3c0c5c49`，2026-09-08 `07:59:19Z–08:00:00Z`。下方保留前两次真实失败，不把本次通过覆盖到旧候选。

| 验收项 | 实际证据 |
| --- | --- |
| 两个真实作者与交付 | Task `task-f5bb73a1-695b-49b6-a3f4-84ee2b43c82d`；Pi PID `68815`/`68877`，实际 `startedAt→agentExitedAt` 重叠 **13,106 ms**；一次批准、一个 Attempt，最终 `completed` |
| 独立验收与下载消费 | 服务固定 oracle **69 项通过**；从 HTTP 下载的两个源码文件再由客户端固定 oracle **69 项通过**，不是仅验 hash 或相信服务状态 |
| 成果摘要 | `normalize.mjs`: `34e998ce32939c37c8845e88c6cdc1c55bece13096897c09bf23c2005ce7d26e`；`report.mjs`: `09c55cd763126ee939857402575c861cd9ae8cff3ddea7cafcfc713118ce542e` |
| 活跃前端重启 | 作者运行中关闭 HTTP 前端、重新启动；原 Worker ID/PID/startedAt 与原 deadline 不变，未再次派发，随后完成原交付 |
| 真实取消 | Task `task-d62cd1d7-9e17-40e9-a1b4-234823300860`；另一对真实 Pi PID `70288`/`70290` 启动后通过 HTTP cancel，两个 Worker 均 `cleaned=true`，Task 为 `cancelled`；再重启前端仍为 cancelled |
| 清理与成本 | 本次验收总计 **40.62 秒**；测试后显式停止 supervisor，`supervisor.lock` 已释放，无保留的测试 Worker；token 未测，仍为 null |

成功摘要与状态保留于 `/private/tmp/mnt-live-VlLlH5/`。总计三轮真实交付候选：两次失败、一次成功；成功轮另含一个真实取消任务。不宣称首轮成功、零返工或已经证明多 Agent 优于强 Lead＋SubAgents。

### 保留的前两次失败

首次实机验收失败，约 84.95 秒。Task `task-e076bcb6-a5ac-429f-b7f5-9d2f48173992`，两个 Pi PID 为 `22896`、`22899`，开始时间分别为 `07:46:50.636Z`、`07:46:50.842Z`。已通过活跃 HTTP 前端重启后原 Worker 身份和原 deadline 一致性检查；第一作者交回模块，第二作者 stdout 达 1,049,607 字节触发 `output-limit`。两位均已确认清理，状态 `failed`，没有交付成功或第二取消任务证据。

私有结果摘要在 `/private/tmp/mnt-live-1nxDKk/acceptance-summary.json`；原状态在该目录的 `state/`。连接 token、原生配置和原始 Agent 流不进入本文或 Git。

第二候选 `7f5f6c3f743b7733a3a1cfa09d5495173eaffe6a` 显式调整事件传输预算后，Task `task-0d8f5a58-4560-4618-b025-619528fa8157` 两作者均完整 collected，stdout 分别 920,866 和 1,376,473 字节，全部清理成功，但固定 oracle 在第 51 项后拒绝交付。只重放已存源码即可确认 report 漏了数量正数与价格非负的范围检查，无需再调用模型定位。私有目录 `/private/tmp/mnt-live-tcEd1y/` 保留失败，耗时约 88.25 秒。

该失败暴露角色上下文不对称：初始 normalize 提示明写范围，report 提示只有“非法字段”和 safe integer，无法依赖另一角色读过前者提示。后继把两项范围与反例放进两角色各自完整收到的共同契约，计划版本提升为 `node-orders-v2`；oracle 未改变，新增“report 删除范围判断”变异反例验证仍拒绝。没有将已失败 Task 改成成功，也没有静默重用其权限或重置 Attempt。

## 本轮失败与改进，不删成本

1. 跨模块预检发现裸文件 SHA-256 与带前缀的控制摘要混用、supervisor 白名单漏 HOME/PATH；均在付费调用前统一。后续继续先冻结调用者实际读取的字段和环境，不只对接口名称。
2. 同轮审查补正未清理 intervention 仍允许新批准、重复取消可耗尽 controls 并污染 owner、Agent 退出时间与清理完成时间混淆、下载只验摘要未消费这四处。均聚合修复，未为每个发现创建新产品 Run 或付费重试。
3. 首次 HTTP 测试把 `running` 当作两个进程均已启动，导致 PID 从 null 到真实值被误判重启换人；改为先观察实际 PID，再重启比较身份。
4. 同时启动四个测试文件时宿主 load 约 13/15 CPU，22 项中 2 项失败：短寿命替身未形成真实重叠、HTTP 固定验收失败。保持原源码与产品期限，改为串行测试文件后 22 项通过。保留并发敏感性，不能宣称高负载稳定性已验证；未用调高产品门禁或盲目付费重试掩盖。

## 后继决策

本轮已证明当前 Mac 的既有 Node 解释器可以运行无 Marshal 原生依赖的 HTTP 团队闭环，且不需要在本次链路中批准新的 Marshal 原生二进制。它仅关闭 ADR 0087 的有界可行性实验，不自动关闭 Go B1/B2 或授予 production。

下一步应先决定正式 Node profile 与原服务的合同适用范围，复用已经验证的 Task API 和业务验收；随后推进第二 Provider、局部修正/成果复用、完整 owner 恢复和存储后端，不先全面翻译 Go Core。任意任务规划、完整 supervisor/整机崩溃恢复、长期负载、SQLite 生产接线和正式安装/支持矩阵仍需后继证据。Node `vm` 与同 UID 进程不被描述成恶意代码沙箱；企业管控是否继续允许脚本/解释器须以实际环境为准。
