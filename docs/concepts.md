# 核心概念

本页提供理解 Marshal 所需的最小模型。协议字段和完整不变量以[Runtime 架构](runtime-architecture.md)及[参考索引](reference.md)中的规范文档为准。

## 当前能力与目标能力

| 层次 | 状态 | 含义 |
| --- | --- | --- |
| Local MVP（M0–M6） | `USABLE` | 本地 CLI、独立 worktree、真实验证、Review/Rework、GitHub Draft PR、恢复与审计 |
| Runtime 架构（M7） | `PASSED` | 长寿命 Control Plane、Provider 边界与 Goal 方向已经完成设计 |
| Runtime 实现（M8–M12） | `PLANNED` | Sandbox SPI、`marshal-server`、Cloudflare Provider、HA、SDK 与长稳验证尚未实现 |
| Goal 编排（M13） | `PLANNED` | 有界 DAG、计划接纳、重规划、预算与人工暂停尚未实现 |

不要把目标架构当成当前产品能力。实时状态以 [Roadmap](roadmap-status.md) 为准。

## Control Plane 与 Executor

Marshal Core 是确定性 Control Plane，也是唯一业务权威。它负责生命周期、Policy、预算、租约、fencing、Evidence 接纳、ReviewDecision 和发布授权。

LLM、人、Provider 和 durable backend 都不是第二个 Supervisor。它们只能提交 proposal、Candidate、Evidence、Assessment 或 Receipt，Core 校验后才决定是否写入权威账本。

Plan、Implement、Verify、Review 和 Publish 可以共享调度、heartbeat、cancel、日志与 Artifact 基座，但不共享一个通用 Executor 协议。每类 Port 仍有独立的 Schema、身份、权限、凭据和 conformance。

## 一次任务的对象层次

```text
Project / Goal       长周期目标；由多个有界 Run 推进（M13）
└── TaskSubmission   幂等提交入口
    └── Task / Run   冻结 spec、base、Policy 与最低环境要求
        └── Attempt  一次短命、可丢弃的执行
            └── Allocation / Lease 具体执行环境与 fencing 身份
```

Run 是有界工作单元，不应持续数月。长期稳定性来自可恢复的 Runtime、事件账本和多个短 Attempt，而不是永生进程或永生 Sandbox。

## 四种不能混淆的输出

| 输出 | 谁产生 | 能否独立改变状态 |
| --- | --- | --- |
| Candidate | Implement Worker | 不能，只是候选成果 |
| Evidence | 独立 Verifier | 不能自行作 ReviewDecision，但可满足事实门禁 |
| Assessment | Reviewer / 主 Agent | 不能，Core 必须绑定当前 Evidence 后接纳 |
| Receipt | Publication/SideEffect Provider | 不能，Core 必须对账后接纳 |

因此，Worker 不能为自己的代码或测试提供权威证明，Provider 的 `completed` 也不等于任务完成。

## 安全与质量不变量

- 写任务锁定 base SHA，并使用独立 worktree。
- 同一任务 worktree 同时最多一个写入者。
- Worker 与 Verifier 使用不同 principal 和 allocation。
- Publisher 位于独立 Publication 信任域，Worker 不获得发布凭据。
- ReviewDecision 精确绑定当前 Evidence。
- 陈旧 generation、Evidence 或远端 head 均 fail closed。
- 失败和阻塞同样保存 Outcome，不创建虚假 PR。
- Local 普通宿主进程不是恶意代码 Sandbox。
- Merge 默认禁用。

## 下一步阅读

- 想使用当前版本：阅读[快速开始](getting-started.md)和[操作手册](operator-runbook.md)。
- 想理解完整系统：先读[整体架构](architecture.md)，再深入[Runtime 架构](runtime-architecture.md)。
- 想贡献代码：阅读[开发指南](development.md)。
- 想查协议、生命周期或历史决策：使用[参考索引](reference.md)。
