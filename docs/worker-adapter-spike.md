# Milestone 4 本地 Worker Adapter 能力 Spike

- 日期：2026-08-04
- 本机版本：Qwen Code `0.21.3`、OpenCode `1.18.12`、Pi `0.83.0`
- 比较目标：协议和运行时可控性，不评价主观 Coding Quality

## 有界探针

三种 CLI 均在 Marshal 仓库中执行一次不允许工具调用的非交互探针，只要求返回 `MARSHAL_PROBE_OK`。没有文件发生变化。

| 能力 | Qwen Code | OpenCode | Pi |
| --- | --- | --- | --- |
| 非交互入口 | `-p` | `run` | `--print` |
| 结构化输出 | `json` / `stream-json` | `run --format json` | `--mode json` / `rpc` |
| Session Identity | init/result 含 UUID | 每个事件含 `sessionID` | session 事件含 UUID |
| Ephemeral | 可新建 session | 默认新 session | `--no-session` |
| Resume | `--resume` | `--session` | `--session` / `--resume` |
| 工具限制 | `--safe-mode` 与 sandbox，但探针仍调用了内置 Goal 工具 | Permission 配置，可逐工具/命令 deny | `--tools` / `--exclude-tools`，显式 allowlist |
| Transcript 特征 | JSONL 完整但默认工具面较大、上下文成本高 | JSONL 紧凑，step/text/tool part 稳定 | JSONL/RPC 完整但 delta 较细、事件量较大 |
| 取消策略 | 需要 Harness 进程组取消 | 需要 Harness 进程组取消 | 需要 Harness 进程组取消 |

## 首个 Adapter 选择

M4 选择 OpenCode，原因只基于接口：

- 当前版本的 `run --format json` 能稳定提供 `sessionID`、step 边界、文本与工具事件；
- 已在本仓库完成多次非交互实现与只读审计；
- 支持通过 `OPENCODE_CONFIG_CONTENT` 注入最高优先级的运行时 Permission 覆盖；
- 输出比 Pi 的逐 delta JSONL 更紧凑，Qwen `--safe-mode` 探针仍暴露并调用了非任务工具。

Pi 的显式工具 allowlist 很有价值，作为 M6 的第二个 Adapter 优先实现；Qwen 同样保留到 M6，并要求先解决 safe mode 下内置工具面与 transcript 成本。

## 安全结论

OpenCode 默认权限是 permissive，用户的 ambient config 也可能允许全部操作。因此 Adapter 绝不能直接继承 ambient Permission。M4 必须：

- 使用 exact executable、版本与 binary digest；
- 使用 `--pure`，通过 `OPENCODE_CONFIG_CONTENT` 同时覆盖全局和 `build` Agent Permission；
- deny 外部目录、web、subagent、skill、question、push/commit/gh/ssh/curl/wget 等发布或网络入口；
- 使用过滤环境移除 Publisher、Cloud 与 Git 托管凭据；
- 启动前读取 resolved config 或执行等价本地校验，发现 deny 规则被 Managed Config 放宽时 fail closed；
- 明确 Local Profile 不是恶意代码容器：允许 shell 的 Worker 仍可能通过其他解释器绕过字符串级命令 deny，真正不可信代码必须使用后续 Hardened Profile。

配置优先级和 Permission 语义以 [OpenCode Config](https://opencode.ai/docs/config) 与 [OpenCode Permissions](https://opencode.ai/docs/permissions) 为准；Adapter 兼容性以本机冻结版本和 Conformance Fixture 为准，不跟随 `latest`。
