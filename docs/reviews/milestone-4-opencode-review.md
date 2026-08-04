# Milestone 4 OpenCode 独立审查

- 日期：2026-08-04
- Reviewer：opencode CLI（模型 `qwen3.8-max`）
- 模式：只读静态审查；未运行任何命令或测试，未修改任何代码
- 审计对象：`internal/adapter/opencode/opencode.go`、`internal/execution/service.go`、`internal/execution/service_test.go` 三个文件
- 口径说明：结论只基于上述三个文件的代码；`docs/milestone-4-scope.md` 与 `docs/reviews/` 既有报告仅用于对齐验收口径与文档格式。受只读范围限制，涉及 `gitworktree`、`runstore`、`lifecycle`、`verification`、`contract` 各包内部行为的判断均标注「待核实」。

## Verdict

**`BLOCKED`**（首轮结论）

存在 1 项 P1：`worker.started` 事件落盘之后的非 Adapter 失败路径不记录任何失败事件，Run 会永久滞留在 `RUNNING`。该问题与「失败或阻塞任务必须保存证据」的不变量冲突，且可由瞬时 IO 错误或 Worker 侧输入稳定触发，必须修复并复验后才能宣告 M4 通过。另有 6 项 P2、5 项 P3 与若干残余风险，见下文。

主链路总体是正确的：Worker 声明只是 declaration，身份、executable、version、session、时间字段由 Adapter 覆盖并经双重 Schema 验证（`opencode.go:288-314`、`service.go:186-205`）；验证证据来自独立的 `verification.ObserveContext`（`service.go:209-211`）；进程组隔离、环境 allowlist、控制面文件先于 `RUNNING` 落盘等设计均符合冻结范围。

## P0 Findings

无。未发现 Worker 可直接伪造验证证据、越过身份绑定、或在 M4 范围内触发 commit/push/发布副作用的路径。

## P1 Findings

### P1-1 `worker.started` 之后的非 Adapter 失败不落盘失败事件，Run 永久滞留 `RUNNING`

- 位置：`service.go:206`、`service.go:209-211`、`service.go:217-230`；`recordFailure` 自身失败 path `service.go:296-305`；触发点之一 `opencode.go:280`
- 现状：`worker.started` 落盘（`service.go:172-177`）后，Adapter 运行错误与协议错误都会走 `recordFailure`（`service.go:180`、`service.go:187-192`、`service.go:200-204`），但协议成功之后的所有错误——写 `worker-result.json`（`service.go:206`）、`verification.ObserveContext`（`service.go:209`）、写 `worktree-snapshot.json`（`service.go:217`）、`worker.completed` 的 transition/Append/WriteSnapshot（`service.go:221-230`）——均直接 `return Result{}, err`，不写 `worker.failed`、不进入任何终态。
- 后果：RunState 停在 `RUNNING`，下次执行被 `service.go:50-52` 的起始状态检查拒绝；M4 明确不含 reconciliation，Run 永久卡死，且没有保存任何失败证据。
- 次级触发（Worker 可主动构造）：`recordFailure` 将 `cause.Error()` 原样放入 RunEvent payload（`service.go:296`），而 `opencode.go:280` 将子进程 stderr（上界 64 KB）原样拼进错误串。子进程以非零退出码加非法 UTF-8 stderr 失败时，事件 JSON 序列化失败 → `recordFailure` 失败 → 同样滞留 `RUNNING`。
- 整改建议：协议成功后的任何失败都必须写终态失败事件并进入 `BLOCKED`（worktree 已被改动，重试不安全，不应再进 `RETRY_PENDING`）；对进入事件/错误文本的非受信字节做 UTF-8 净化与长度约束。

## P2 Findings

### P2-1 bash deny 表可被间接调用平凡绕过，resolved config 校验不完整

- 位置：`opencode.go:478-488`（deny 表生成）、`opencode.go:522-546`（resolved 校验）
- 现状：bash 规则为 `"*": "allow"` 加固定黑名单。黑名单按命令串前缀匹配，`sh -c 'git push ...'`、`env curl ...`、`xargs curl`、绝对路径 `/usr/bin/curl` 等间接形式均不命中。`validatePermissionMap` 只断言 10 个固定 pattern 仍是 `"deny"`，未断言顶层通配 `"*"` 仍为 `"deny"`，也未校验 `autoupdate`、`share` 在合并后的取值。
- 影响：该机制满足「显式发布命令被 deny」的字面要求，但不能阻止有意规避；文档与测试结论不得将其表述为发布控制边界或沙箱（与「普通宿主机子进程不是沙箱」的不变量一致）。
- 整改建议：至少补充 `sh *`、`bash *`、`env *`、`sudo *`、`xargs *`、`nohup *` 的 deny 并纳入 resolved 校验；显式断言顶层 `"*": "deny"`；在 `notes` 中明确该表是启发式护栏。

### P2-2 `permissionDenied` 依赖对非受信 transcript 文本的子串启发式

- 位置：`opencode.go:421-424`（判定）、`opencode.go:282-284`（据此返回 `ErrPermissionDenied`）
- 现象：对整行小写文本做 `permission` + `denied/deny` 子串匹配。Worker 会话内容（含 TaskSpec prompt 回声、代码讨论）中出现该字样即触发误报，使一次退出码为 0 的成功 attempt 失败；反过来结构化事件中的拒绝也可能漏报。
- 整改建议：改用已解析事件的结构化字段（`part.state.status` / `part.state.error`，解析结构已在 `opencode.go:400-403` 中声明但未使用）判定。

### P2-3 输出预算截断按「完整行」生效，无换行大行绕过预算

- 位置：`opencode.go:381-386`（`bufio.ReadBytes('\n')` 先缓冲整行再核对 `consumed > limit`）、`opencode.go:239`
- 现象：子进程输出一条无换行的超大行时，内存随行长无界增长，且进程组 kill（`opencode.go:236`、`opencode.go:384-386`）延迟到该行结束才触发，「输出超过预算立即取消」的实际时机被推迟。
- 整改建议：按块读取并在块边界上判断预算，或对单行长度单独设上限。

### P2-4 关键运行证据缺失：exit/signal、stderr 与截断标志未持久化

- 位置：`opencode.go:258-263`（meta 只含 session/event/tool/token/bytes/truncated/permissionDenied）、`opencode.go:240`（`captureStream` 的截断标志被 `_` 丢弃）、`opencode.go:270-272`（超时路径在检查 `waitErr` 前返回，stderr 彻底丢失）
- 现象：冻结范围要求保存 exit/signal、usage 与首尾截断信息；当前 transcript-meta 不含退出码/信号，stderr 既不落盘也不记录截断状态，超时 attempt 无法事后区分 kill 原因。
- 整改建议：将 exit code/signal、stderr（有界）与两路流的截断标志写入 meta 或独立证据文件。

### P2-5 `service_test.go` 未覆盖关键守卫分支

- 位置：`service_test.go:51-80` 仅有成功与重试两个用例；未覆盖 `service.go:50-52`（非法起始状态拒绝）、`service.go:114-116`（预算耗尽 → `BLOCKED`）、`service.go:186-205`（协议无效/身份不匹配的 WorkerResult 拒绝）
- 现象：这些分支正是「声明不作为证据」的门禁，但没有回归测试保护；`worker.started`/`worker.completed` 事件序列本身也未被断言。
- 整改建议：补表驱动测试覆盖上述分支，并断言 RunEvent 序列与终态。

### P2-6 重试可能继承上一次失败 attempt 的脏 worktree（待核实）

- 位置：`service.go:114-125`（预算检查与 `gitworktree.Acquire` 之间无任何 worktree 清理/重置）
- 现象：attempt 1 已写入文件但 Adapter 失败进入 `RETRY_PENDING` 后，attempt 2 在同一 worktree 上开始；若 `gitworktree.Acquire` 不校验或重置相对 `BaseSHA` 的工作区洁净度，重试将叠加在残留改动之上，最终 snapshot 混合多个 attempt 的产物。受只读范围限制无法核实 `gitworktree` 行为。
- 整改建议：在 service 层显式要求干净基线，或提供 `Acquire` 语义的测试证据。

## P3 Findings

### P3-1 版本探测不受 attempt 预算约束且输出无上界

- 位置：`opencode.go:118-121`（`inspect` 使用外层 ctx 与 `command.Output()`）
- 现象：`Run` 的 attempt 超时在 `opencode.go:214` 才派生，`--version` 探测不受其约束；`Output()` 对输出不做限长。另每次 `Run` 对整个二进制重算 SHA-256（`opencode.go:114`）增加开销。

### P3-2 token 统计为「最后事件覆盖」语义

- 位置：`opencode.go:417-420`
- 现象：多 part 事件流下 usage 会被低估。若 usage 仅用于观测可接受，需在 meta 中注明语义。

### P3-3 大体积非受信 stderr 进入错误串与事件 payload

- 位置：`opencode.go:280`、`service.go:296`
- 现象：最多 64 KB 的子进程 stderr 会进入 RunEvent，事件膨胀；编码问题已在 P1-1 中升级处理，此处指体积与净化本身。

### P3-4 `service.go` 的 `atomicWrite` 缺少目录 fsync，崩溃可留孤儿 attempt 目录

- 位置：`service.go:163-177`、`service.go:324-347`（对照 `opencode.go:646-680` 的 fsync 实现）
- 现象：worker-request 落盘与 `RUNNING` 事件之间崩溃会留下无状态引用的孤儿目录；目录项持久性弱于 adapter 侧实现。孤儿目录本身无害，但应有 recovery 口径。

### P3-5 细节问题

- `opencode.go:596`：错误信息 `"path escapes worktree"` 实际检查的是 control root，边界表述错误。
- `service.go:194-197`：`resultIdentity` 匿名 struct 无 JSON tag，依赖大小写不敏感匹配，建议显式 tag 防止字段改名后静默失效。
- `opencode.go:377`：`min(int(limit), 64<<10)` 在 32 位平台上 `int64→int` 溢出可得负容量 panic。

## 残余风险

1. **同 UID 边界**：Worker 与 Marshal 同用户运行，`0400`/`0700` 文件模式与控制面目录隔离不构成安全边界；Worker 可读 HOME 下的 Provider 凭据（如 `~/.config/gh`）并将内容写入 worktree diff。M5 引入发布路径之前必须有凭据隔离措施（独立用户/容器/限制凭据作用域），否则形成外带通道。
2. **Blocklist 不是沙箱**：与不变量一致，宿主进程的 bash 黑名单无法穷尽网络与发布行为，文档与测试表述必须保持「启发式护栏」定位。
3. **TOCTOU**：executable digest 与执行之间、控制面文件校验与读取之间的替换窗口是本架构固有，依赖宿主文件系统信任。
4. **崩溃恢复缺口**：M4 范围内无 reconciliation/cleanup；`RUNNING` 快照滞留、脏 worktree、孤儿 attempt 目录都要求 M5 给出显式恢复设计（P1-1 只解决非崩溃错误路径）。
5. **worktree 内 Git 局部配置**：Worker 可向 worktree 写入 `.git/config`、hooks 等，可能在后续 Publisher 阶段执行的 git 命令中被触发，M5+ 发布前必须处理。
6. **取消时延**：子孙进程持有 stdout pipe 时 `command.Wait()`（`opencode.go:251`）阻塞至 attempt 超时，取消有界但非即时。
7. **版本精确匹配脆弱性**：`--version` 输出格式或版本漂移会导致 fail-closed 的 `unsupported`，属运维风险而非安全风险。

## 复验要求

- P1-1 必须修复并补充「协议成功后观测/持久化失败进入 `BLOCKED` 且保留失败事件」的回归测试后复审。
- P2 各项可逐项修复或显式接受为残余风险并写入文档；其中 P2-1、P2-5 建议在本轮内完成。
- 复审需重新阅读三个目标文件的最新版本，并核对新增测试是否覆盖本报告列出的分支。

## 整改与复审

复审日期：2026-08-04

复审结论：**`APPROVE`**。

首轮 P1 已关闭：协议完成后的 WorkerResult 持久化、Worktree 身份复核、Git 观察和 Snapshot 持久化错误现在记录 `worker.evidence-failed` 并进入 `BLOCKED`；新增回归测试会破坏 linked worktree 的 `.git` 身份，验证不能向上误认嵌套主仓库。同时，`gitworktree.Open` 改为使用真实 `--show-toplevel`，补上了该测试暴露的潜在身份缺口。

本轮同时关闭或降低了 P2-1 至 P2-5：

- resolved config 额外校验全局 deny、`share=disabled` 和 `autoupdate=false`，并增加常见间接 shell 入口的 deny；它仍只被描述为启发式护栏；
- permission denial 改为只读取结构化 `part.state.status/error`，避免普通文本误报；
- JSONL 改为 64 KiB 分片读取，累计字节一超限即杀进程组；无换行无限流已有时延断言；
- transcript metadata 增加 exit code、signal、stderr 字节/截断和 context error，并保存有界 stderr；
- 新增后置证据失败、Worktree 身份破坏、全局 permission wildcard、permission 文本误报和无换行超限测试；完整 Race 与真实 OpenCode E2E 均通过。

P2-6 被接受为明确语义：Operational Retry 在同一任务 Worktree 上继续已有部分变更，Attempt 证据按轮分离；它不是自动回滚。Worker 完成但证据无法可靠保存时不会重试，而是 `BLOCKED`。

复审 Agent 基于上述已验证事实给出 `Verdict: APPROVE`，并保留三个非阻塞风险：M4 尚无崩溃后 `RUNNING` 心跳恢复；`BLOCKED` 需要主 Agent/维护者处理；Local Permission 依赖 Provider 正确报告且不构成宿主机沙箱。这些均进入 M6 Recovery/Hardened 范围，不阻止 M4。
