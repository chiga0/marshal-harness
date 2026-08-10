# Roadmap 状态

更新时间：2026-08-10

| Milestone | 状态 | 证据 |
| --- | --- | --- |
| 0：Toolchain 与 Contract | `PASSED` | [验收报告](milestone-0-report.md) |
| 1：State Machine 与 Run Store | `PASSED` | [验收报告](milestone-1-report.md) |
| 2：Git Worktree 与独立 Verification | `PASSED` | [验收报告](milestone-2-report.md) |
| 3：Review 与 Rework Loop | `PASSED` | [验收报告](milestone-3-report.md) |
| 4：首个真实 Worker Adapter | `PASSED` | [验收报告](milestone-4-report.md)；GitHub Actions `30879438415` |
| 5：GitHub Draft Publisher | `PASSED` | [验收报告](milestone-5-report.md)；主分支 CI `30889069165`；[真实 Draft PR #1](https://github.com/chiga0/marshal-harness/pull/1) 与 PR CI `30889190854` |
| 6：其余 Adapter 与 Recovery 加固 | `PASSED` | [验收报告](milestone-6-report.md)；真实受监督 cmux Pilot 通过；Full MVP E2E Run `m6-mvp-e2e-r3-20260805` `ACCEPTED`，[Draft PR #2](https://github.com/chiga0/marshal-harness/pull/2) 与 PR CI `30974239712` 全绿 |

Local MVP 定义达成：标记 `USABLE`。

M7–M13（M7–M12：耐久 Runtime 与可插拔 Sandbox Provider，[ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md) 冻结；M13：Goal orchestration，承接 ADR 0016 冻结的 Project/Goal 对象语义）：

| Milestone | 状态 | 证据 |
| --- | --- | --- |
| 7：架构与契约 | `IN_PROGRESS` | [ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md) 已接受；[ADR 0017](adr/0017-provider-neutral-sandbox-contract.md) 已接受（2026-08-10，接受只关闭设计歧义）；[Runtime 架构](runtime-architecture.md) 同步 |
| 8：Sandbox SPI/Fake/Local conformance + embedded/local 纵切 | `PLANNED` | 见[实施计划](implementation-plan.md) |
| 9：marshal-server、Public API 与 Durable Runtime | `PLANNED` | 见[实施计划](implementation-plan.md) |
| 10：Cloudflare Provider（remote transport） | `PLANNED` | 见[实施计划](implementation-plan.md) |
| 11：生产级存储、多节点 HA 与身份分离 | `PLANNED` | 见[实施计划](implementation-plan.md) |
| 12：开源部署、版本化 Provider SDK/协议、多语言 SDK 与长稳验证（基于 M9 冻结的 wire contract） | `PLANNED` | 见[实施计划](implementation-plan.md) |
| 13：Goal orchestration（Goal API/控制器、持久 Project/Goal、计划/重规划、预算与终止、独立评估、人工干预） | `PLANNED` | 见[实施计划](implementation-plan.md) |

[ADR 0017](adr/0017-provider-neutral-sandbox-contract.md)（已接受，2026-08-10；全部 P1 经 Round 2 独立验证与 ReviewDecision accept 后由维护者接受）基于首次 Sandbox SPI dogfood 的 reject 证据冻结 provider-neutral Sandbox 安全契约，并修订 M8–M13 分工：

- 二维权限/隔离模型 `AccessMode × AssuranceLevel`（含旧 `executionProfile` 兼容映射、拒绝/降级规则与持久记录迁移）；
- `hardened` 必须绑定密封 `ConformanceEvidence`，证据拓扑冻结：probe 定义/challenge/nonce、artifact digest、调度、out-of-band 观察、裁决与签发由 Control Plane 与独立 Conformance Verifier 控制，probe workload 作为敌对测试负载运行在被测 Provider 创建、身份精确绑定的 target allocation 内，Provider 的 completed/receipt 只是输入、不能自签通过；Local 普通宿主进程永不 hardened，Cloudflare 无豁免；
- Stage 内容寻址（inline 小对象/ArtifactStore locator、大小上限、消费前后重算 sha256、禁止回显声明 digest）；
- workloadRole 与认证 principal 拆分：Sandbox workloadRole 封闭枚举仅 `worker`/`verifier`，control-plane/publisher/operator/API caller 是不同语义 Port 上受 AuthZ 约束的 principal，Publisher 永不成为 Sandbox workload；完整身份 fencing（task/run/attempt/workloadRole/allocation/generation/fencingToken 元组，远程请求另绑定 principal/portKind/providerType/audience/scope；普通 replay 先过当前 lease fencing；Restore lost-response reconciliation 独立路径）；
- 无双写 Restore（默认 replacement allocation，控制面 CAS 激活新 generation）；
- DispatchLease 唯一状态机：Push/Pull 只改变连接发起方，capability matching、ack/heartbeat/deadline/expiry/cancel/reconcile/generation bump 与陈旧结果隔离完全等价；M9 交付两拓扑等价 conformance 与故障注入口径；
- DurableExecutionEngine 唯一 Port 名：Temporal/Local Engine 仅是 backend，Attempt 创建/retry 预算/rework/终态裁决只在 Core，delivery/activity retry 不创建 Attempt、不消费业务预算；
- M9 wire contract 首版冻结：versioned HTTP/JSON + OpenAPI（Task create/get/cancel、Run approval/status、events/evidence），SSE `eventId`/cursor 断线续传 + 轮询 fallback，WebSocket/gRPC 推迟；Provider remote transport 同为 versioned HTTP/JSON（Push 调 Provider endpoint，Pull outbound-only）；M9 提供最小 scope-bound 可撤销注册身份，M11 扩展生产远程入口与多用户 AuthN/AuthZ，M12 基于该 wire contract 交付多语言 SDK 与部署文档；版本化 Provider Protocol 认证注册与观测边界；C/S + Control Plane/Execution Plane 分离并保留 embedded/local 模式；CLI/Web/GitHub App/CI 均为 Public API client，embedded CLI 经 in-process adapter 调同一 Public application Port、不直写 store；
- 分工修订：**M8 的纵切是 embedded/local 纵切**；`marshal-server` 与 Push/Pull Public API 属于 M9；M10 接 Cloudflare remote transport；M11 HA/AuthN/AuthZ；M12 多语言 SDK、部署文档与多拓扑 conformance；M13 实现 Goal API/控制器。

实现状态不因文档冻结或 ADR 接受而提前升级：ADR 0017 的接受只关闭设计歧义，M8 实现与 conformance 状态不因此提前；上表各 Milestone 状态保持原值（M7 `IN_PROGRESS`，M8–M13 `PLANNED`）；首次 Sandbox SPI dogfood Run 的既有实现成果按**未接纳探索证据**对待，不计为 M8 实现进度；M8 须按修订后的契约以新任务启动，并通过其退出门禁后才可标记完成。

每个 Milestone 都执行范围冻结、实现、单元/集成/E2E 测试、独立审计、提交推送和远端 CI 绿色验收。任何 P0/P1 审计问题或 CI 失败都会阻止进入下一阶段。M7–M13 还要求每个 Milestone 先通过 Local MVP 全量回归。M7 只冻结 Project/Goal 对象语义，M13 才实现 Goal 控制器；M7–M12 完成声明不涵盖复杂需求目标。
