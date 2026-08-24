# ADR 0043：WorkerExecutor、WorkerRuntimeProfile 与 Agent/Sandbox 双 binding

- 状态：已接受（Accepted，2026-08-24）；接受证据：独立 reviewer 对 R0 产物审查 verdict=accept 且 P0/P1 清零；接受只冻结合同，未实现，不升级任何 milestone 状态
- 关联：ADR 0016、ADR 0017、ADR 0018、ADR 0019、ADR 0038、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)、[Issue #187](https://github.com/chiga0/marshal-harness/issues/187)、Planning Baseline v3（R0 步骤 4）

## 背景

Issue #186 Final Review 确认当前主路径的结构性缺口（I186-P0-1）：Sandbox 已 provision 但 Agent 仍可能在宿主直接经 `Adapter.Run` 执行，Candidate 无法证明来自绑定 allocation/generation 内的 Agent 进程；legacy WorkerAdapter 也不是 production AgentProvider（I186-P1-2）。Planning Baseline v3 冻结的唯一执行链要求：

```text
ApplicationCommand
→ Kernel transaction (authority fact + durable command)
→ WorkerExecutor
→ WorkerRuntimeProfile / per-Attempt binding
→ Sandbox.Stage(frozen inputs + AgentLaunchSpec)
→ Sandbox.Exec(agent)
→ AgentRuntime.Decode/Finalize
→ workload result + DRC
```

本 ADR 冻结该链中 WorkerExecutor、WorkerRuntimeProfile 与双 binding 的合同；DRC（DispatchResultCapability）沿用 ADR 0018，不重新定义。

## 决策

1. **WorkerExecutor 是 Core 内 Attempt 执行的唯一编排者**。它只消费 Kernel transaction 产出的 durable command，按 WorkerRuntimeProfile 驱动 Sandbox 与 AgentRuntime；WorkerExecutor 不是新的 Provider Port，也不是第七 Port——它是 Control Plane 内部组件，不对外暴露 Provider 协议。
2. **WorkerRuntimeProfile 是 per-Attempt 冻结对象**，包含且仅包含：AgentBinding、SandboxBinding、compatibility 断言与两者 capability/snapshot digest；不含 raw credential、不缓存 token。Profile 在 Attempt 开始时从 current ledger 解析并冻结 digest，Attempt 期间不得被就地改写；变更只能产生新 Attempt 的 profile。
3. **AgentBinding 与 SandboxBinding 是两条独立 registration 引用**：分别指向 durable AgentProvider registration 与 SandboxProvider registration。两者同 trustDomainKind 不自动互相授权；Agent evidence 不得冒充 Sandbox evidence，反之亦然（对齐 Planning Baseline v3 R3 步骤 6 对 ADR 0038/APAP 的边界：保留必要宿主 authority，但 APAP/宿主 authority 不得成为 universal WorkerProvider）。
4. **per-Attempt binding set 必须分别 current-ledger recheck**：接纳任一结果前，AgentBinding 与 SandboxBinding 各自重新核对 registration 当前状态；任一 binding 被 revoke/expire/replace，旧组合产生的后续结果一律 fail closed，不得接纳。
5. **AgentRuntime 与 Sandbox 职责分离**：Sandbox 负责 `Stage → Exec → Inspect → Terminate` 与执行位置证据（allocation/generation/PID/process-group/digest）；AgentRuntime 负责 `DecodeEvent → FinalizeResult` 的协议归一化，产出 untrusted workload result。归一化结果本身不是权威事实，权威接纳由 ADR 0044 的 ResultIngress 承担。
6. **AgentLaunchSpec 是 immutable 对象**，由 WorkerExecutor 在 Stage 前生成并绑定 profile digest；Sandbox 只执行收到的 spec，不改写、不推断 argv/environment。
7. **production profile 禁止宿主 bypass**：任何标记 production 的 WorkerRuntimeProfile 不得调用宿主 `Adapter.Run` 路径；legacy host 路径在 R5 cutover 前仅作为 explicit local-nonproduction compatibility profile 存在（见 ADR 0045）。

## 后果与门禁

- R1 walking skeleton 允许先用 Fake Agent 与既有 Local Sandbox 打通最薄链路；legacy WorkerAdapter 在此阶段以兼容映射（`Probe → PrepareLaunch → DecodeEvent → FinalizeResult`）接入，映射只保留 migration provenance，不形成 production AgentProvider 声明。
- R3 完成前不得宣称存在 production AgentProvider；Agent/Sandbox 可独立替换与撤销、跨 Port credential/token/evidence 复用 fixture 必须失败，作为 R3 Exit Gate 的负向证据。
- 本 ADR 不改变 Worker 不能自证、单 worktree 单写入者、Worker/Publisher 分权等 universal 不变量；不新增信任边界，只把既有边界落到类型化合同上。
- 接受本 ADR 不升级 M8/M9 状态，不解除 M10–M13 暂停。
