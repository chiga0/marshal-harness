# ADR 0008：可插拔 Observer Backend

- 状态：已接受（Accepted）
- 日期：2026-08-04
- 接受日期：2026-08-04

## 背景

Marshal 需要让操作者实时观察 Worker 的实际进展，但用户可能运行在 macOS、Linux、Windows、桌面应用、普通终端或 CI 中。cmux、iTerm2、Ghostty、系统终端与无界面环境提供的控制能力不同，不能把任一终端写入 Worker Adapter 或 Core 生命周期。

可观察性也不能改变证据事实：Worker 的退出状态、stdout/stderr、结构化事件、取消与进程树仍必须由 Marshal 捕获和控制。关闭终端窗口不能导致 Worker 失控，终端显示内容也不能替代持久化证据。

## 决策

新增独立的 `Observer` 模块与稳定 Port。Observer 只消费 Marshal 已经捕获并脱敏的 Attempt 状态、日志和通知，不拥有 Worker stdin、Publisher 凭据、Run Lease 或生命周期转换权。

Core 依赖抽象能力，不根据终端品牌分叉。首批 Backend 为：

1. `captured`：必备且跨平台，只持久化 `.marshal/` 中的有界日志和状态，不要求图形终端；
2. `cmux`：M6 首个可视化 Backend，在独立 workspace/pane 中运行 Marshal 自己的只读日志跟随器，并可显示进度、状态和完成通知；
3. 后续可增加 `iterm2`、`ghostty`、`terminal`、`tmux` 或其他 Backend，不修改 Worker Adapter 契约。

Observer Backend 必须实现分阶段探测：

```text
not-installed -> installed -> reachable -> authorized -> ready
```

探测结果同时包含能力集合，例如 `workspace-create`、`pane-create`、`screen-read`、`notify`、`progress` 和 `readonly-follow`。只按名称发现可执行文件不代表可控制；授权失败必须准确报告为 `unauthorized`，不能误报为未安装。

选择顺序由配置显式给出。`auto` 只在已知 Backend 中按平台和能力探测选择；没有可用图形 Backend 时安全降级到 `captured`。Observer 初始化或运行失败生成 Diagnostic，但默认不改变 Worker Attempt 的结果。

### cmux 发现与授权

cmux Backend 依次检查显式配置、`PATH` 与 macOS App Bundle 中的控制 CLI：

```text
/Applications/cmux.app/Contents/Resources/bin/cmux
~/Applications/cmux.app/Contents/Resources/bin/cmux
```

不得把 App 主可执行文件 `Contents/MacOS/cmux` 误认为控制 CLI。Probe 至少验证 `version`、`capabilities` 或等价只读命令，以及 socket 授权状态。

Marshal 在 cmux 终端内运行时可以使用 cmux 注入的 workspace、surface 与 socket 上下文。从 Codex Desktop 等外部进程运行时，优先由 cmux 控制 CLI 读取其 Settings 中保存的 socket password；自动化环境也可以通过进程环境中的 `CMUX_SOCKET_PASSWORD` 显式覆盖。该值不得写入 TaskSpec、Event、日志或 Outcome。Marshal 不自动修改 cmux 全局设置，也不自行生成或持久化 socket password。

### 只读与人工接管

默认 `readonly` 模式只展示 Marshal 捕获后的输出。cmux pane 中运行的是 Marshal 日志跟随器，不是 Worker 的控制终端，因此关闭 pane 不会终止 Worker，也不能通过 pane 向 Worker 注入输入。

未来如果启用 `interactive` 接管，必须显式配置并产生 `manual-intervention` Diagnostic。该 Attempt 的自动执行证据随即失效，之后必须重新运行独立 Verification；Observer 自身不能静默开启交互模式。

## 影响

- Worker Adapter 保持 Provider 协议职责，不依赖 cmux、PTY UI 或操作系统终端 API；
- Codex CLI、Codex Desktop、手机端 Remote、普通 shell 和 CI 使用同一执行与证据链；
- cmux 是当前本机的优先可视化实现，但不是 Marshal 的运行前置条件；
- 终端 Backend 可以独立测试、替换和扩展；
- Observer 故障不会伪造 Worker 失败，但其 Diagnostic 会保留供 Doctor 与 Operator 检查。

## 未采用方案

- **由 cmux 直接拥有 Worker 进程**：会削弱 Marshal 对退出码、取消、进程树和结构化输出的控制；
- **把 cmux 逻辑写入每个 Worker Adapter**：导致 Provider 与 UI 组合爆炸，并使跨平台行为漂移；
- **只检测 `command -v`**：无法发现 App Bundle 内 CLI，也无法区分未授权与未安装；
- **把终端 transcript 当作权威证据**：终端内容可能截断、重排或被人工修改，不能替代 Marshal 的捕获记录。
