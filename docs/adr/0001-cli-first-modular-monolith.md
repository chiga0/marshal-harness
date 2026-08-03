# ADR 0001：CLI-first 模块化单体

- 状态：已接受（Accepted）
- 日期：2026-08-03
- 接受日期：2026-08-03

## 背景

Marshal 需要编排本地 CLI Agent、接受来自 Codex CLI 或 Codex Desktop 的调用、在普通 Git 仓库中工作并产生可检查文件。未来可能需要 Daemon、MCP Interface 或 Remote Scheduler，但验证核心生命周期不依赖这些接口。

## 决策

MVP 实现为具有显式 Domain Port 的 CLI-first 模块化单体。这里的 CLI-first 描述 Marshal 的产品接口，而不是限制主 Agent 必须运行在终端。Worker 与 Verification Command 作为 Child Process。Durable Contract 使用 JSON Schema 与 JSONL Event。

Core Runtime 由独立的 [ADR 0005](0005-go-runtime.md) 决定，避免把接口形态与实现语言绑在同一决策中。

## 影响

- Codex CLI 可以直接调用 Marshal；Codex Desktop 可通过项目集成终端和相同的文件契约调用与检查 Marshal。
- 本地安装与调试比强制服务更简单。
- Core Module 不得依赖 CLI Parsing，以便未来增加 Service Interface。
- Long-lived RPC 与 Remote Scheduling 延后。
- 主 Agent 入口必须通过 `LeadAgentBridge` 适配，不得把 Codex CLI 的进程模型写入 Core。

## 未采用方案

- **MCP-first**：适合后续 Facade，但会在领域模型验证前增加协议和生命周期表面积。
- **Distributed Service First**：与 Local-first Trust Model 冲突，延迟首条纵向链路。
- **Shell Script Harness**：无法满足 State Machine、Recovery、Event Normalization 与 Testability 要求。
