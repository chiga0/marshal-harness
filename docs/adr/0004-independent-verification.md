# ADR 0004：独立证据具有权威性

- 状态：已接受（Accepted）
- 日期：2026-08-03
- 接受日期：2026-08-03

## 背景

Coding Agent 可能遗漏变更文件、错误总结命令输出，或声称测试通过但实际未运行。即使 Worker 诚实，Tool Output 也可能在 Context Compression 或 Protocol Conversion 时丢失。

## 决策

Worker 调用完成后，Marshal 独立计算 Diff、Changed Path、Command Result 与 Artifact Hash。强制 Gate 使用这些观察结果。Worker Report 只是保留用于对照和 Review 的声明。

主 Agent 基于有边界的 ReviewPacket 做语义决策。ReviewDecision 引用精确 Evidence，不能静默豁免失败 Gate。

## 影响

- 错误自报不能直接产生 Accepted Result。
- Verification 增加耗时，但可复现。
- Command 必须被精确定义并在受控环境执行。
- Pre-existing Failure 需要 Baseline Classification。
- Evidence Schema 与 Log Retention 是核心兼容表面。

## 未采用方案

- **信任 Worker Summary**：不可审计，且会被 Partial Transcript 破坏。
- **让第二个 Agent 判断测试是否通过**：仍是用模型证词替代进程证据。
- **只依赖 Remote CI**：反馈更晚，也无法覆盖全部 Local Artifact 与 Pre-publication Scope Gate。
