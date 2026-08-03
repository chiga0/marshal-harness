# ADR 0005：Go 作为 Core Runtime

- 状态：已接受（Accepted）
- 日期：2026-08-03
- 接受日期：2026-08-03

## 背景

Marshal 的核心职责是驱动外部进程、处理信号与取消、管理 Git worktree 和文件锁、执行原子持久化，并以可重复方式分发到开发机与 CI。主 Agent 和 Worker 通过 CLI、文件与版本化 JSON 契约接入，因此 Core 不需要依赖某一家 Agent 的进程内 SDK。

## 决策

MVP 使用 Go 实现 Core、CLI 和内置 Adapter，并交付单一 `marshal` 可执行文件。实施开始时锁定具体 Go 版本，提交 `go.mod` 与 `go.sum`，使用 `gofmt`、`go vet`、静态检查、单元测试和构建命令作为仓库门禁。

JSON Schema 仍是跨语言的持久化契约。Go 类型必须由 Schema 生成，或通过双向 Fixture 与漂移检查证明一致。未来的 Desktop Bridge、Web UI 或特定 SDK 集成可以使用其他语言，但不得复制或绕过 Core 生命周期。

## 理由

- 单一二进制降低本地 Agent、CI 和不同仓库中的安装与 Runtime 管理成本。
- `context.Context`、goroutine、标准库进程与信号机制适合并发 Worker、取消和超时控制。
- 标准库覆盖文件系统、JSON、HTTP、Hash 与大部分控制平面需求，初始依赖面较小。
- 静态类型与显式错误处理适合实现状态机、持久化边界和 Publisher 门禁。

## 影响

- 团队需要为 Draft 2020-12 JSON Schema 选择并验证 Go 工具链，不能假定 Format 与 Semantic Rule 自动得到完整支持。
- 某些 Agent SDK、MCP 或 Web 生态在 TypeScript/Python 中更成熟；这些能力优先作为进程外 Bridge，而不是迫使 Core 改用对应语言。
- Adapter 应优先执行外部 CLI 或标准协议，不直接耦合仅存在于某语言 SDK 的实现。
- 若 Spike 发现 Go 无法满足必需协议，可新增 ADR 调整边界；不得在实现中静默引入第二套生命周期。

## 未采用方案

- **TypeScript/Node.js**：Agent SDK 与 Web 生态强，但 MVP 主要是本地进程编排与可分发 CLI，Node Runtime 和依赖树没有带来足以抵消部署成本的收益。若首版必须深度嵌入某个 JS-only SDK，应重新评估。
- **Python**：原型与脚本集成快，但单文件分发、环境一致性和长期类型约束弱于当前目标；更适合作为实验 Adapter 或验证脚本。
- **多语言 Core**：会扩大构建、发布和故障边界，不适合首个纵向链路。
