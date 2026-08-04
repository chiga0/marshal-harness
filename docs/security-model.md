# 安全模型

## 安全定位

Marshal 编排能够编辑文件并执行仓库代码的进程，这天然具有高影响。MVP 面向单个开发者、可信 Worker Binary 和开发者可控仓库。它提供严格的审计与工作流控制，但只有在可强制执行的 Sandbox Profile 中才提供宿主隔离。

安全声明必须绑定到有效 Execution Profile 并记录在 Outcome Bundle。仅仅告诉模型“安全操作”，不能把 Run 描述成已沙箱化。

## 保护资产

- 源码仓库与未提交工作；
- Git 历史与远程 Branch；
- SSH Key、Forge Token、Cloud Credential 与 Signing Key；
- 私有源码、Prompt、Log 与生成交付物；
- 维护者工作站与本地服务；
- CI Minute、模型预算与网络资源；
- Review 与 Publication Decision 的完整性。

## 参与者

- 维护者与主 Agent；
- Marshal Core、Verifier 与 Publisher；
- Worker Binary 与选定 Model/Provider；
- Repository Content、Instruction、Dependency、Test 与 Hook；
- Forge 与 CI Provider；
- 第三方 Adapter 或 Plugin Author。

即使仓库由开发者控制，其中的文本仍是不可信指令输入；Dependency 与 Build Script 可以执行任意代码。

## 信任边界

### 语义边界

TaskSpec 与 Repository Policy 优先于 Worker Prompt 和自动发现的仓库指令。Prompt Injection 不能扩大路径、启用发布、暴露凭据或豁免 Gate。

### 文件系统边界

Worktree 隔离将任务变更与主 Checkout 分离，但普通文件权限无法阻止恶意宿主进程读取其他路径。因此 `workspace-write` 只是误操作防护，不是强安全边界。

仓库本地 `.marshal/` 保存 Run、Log 与 linked worktree，并由 Git 默认忽略。忽略规则只是防止误提交，不是访问控制；Verifier 与 Publisher 仍必须拒绝任何把 `.marshal/` 内容带入业务 Diff、Source Artifact 或 Commit Tree 的结果。

### 凭据边界

Worker 使用构造出来的环境，不接收 Publisher Token、`SSH_AUTH_SOCK`、Cloud Profile 或已知 Secret Variable。这能降低暴露，但当 Home Directory、Keychain、Credential Helper 或本地网络服务仍可访问时，不能视为强隔离。

强隔离要求 `hardened` Profile，使用显式 Mount、Network Policy，并移除宿主 Credential Store。

### 网络边界

TaskSpec 中的 Network Intent 只有在 Process Sandbox 能真正过滤网络时才算被执行。无法强制时必须记录为 `unenforced`，Repository Policy 可以拒绝 Run。

## Execution Profile

| Profile | 用途 | 可声明的保证 |
| --- | --- | --- |
| `read-only` | Inspection 与 Review | Marshal 不授予 Edit Tool；Host Process 隔离仍取决于 Sandbox |
| `workspace-write` | 可信本地 Coding | 独立 Worktree、过滤环境和工作流 Gate；不隔离恶意代码 |
| `hardened` | 不可信代码或无人值守 | Container/VM/OS Sandbox 强制 Mount、Network、Resource 与 Credential Isolation |

Repository Policy 选择最低 Profile。Adapter 不能满足时必须在 `READY` 前失败。

## 威胁与缓解

| 威胁 | 缓解措施 | 剩余风险 |
| --- | --- | --- |
| Worker 修改无关文件 | Allow/Deny Path 与独立 Diff | 未 Hardened 时命令仍可能影响 Worktree 外路径 |
| Worker 虚报测试通过 | Marshal 重跑精确命令 | 测试本身可能不完整或 Flaky |
| Worker Push 或开 PR | 移除凭据、禁止发布 Tool、Publisher 分权 | 未 Hardened 时仍可能访问 Ambient Credential |
| Repository Prompt Injection | 冻结 TaskSpec 优先、记录指令摘要、禁止放宽 Policy | 模型仍可能在允许范围内写出错误代码 |
| 恶意 Test/Build Script | Hardened Profile、显式命令、Network/Resource Limit | `workspace-write` 无法隔离恶意脚本 |
| Secret 写入日志 | Environment Allowlist、有界捕获、Redaction、限制文件权限 | 无法识别所有 Secret |
| Symlink/Path Traversal | Canonicalization、禁止 `..`、Root Check、禁止逃逸采集 | 平台特有 Race 需要测试 |
| Output/Resource Exhaustion | Time、Byte、Process 与 Provider Native Budget | 终止前仍可能产生模型成本 |
| 陈旧 Decision 发布新代码 | Evidence Digest 与发布前 Snapshot Recheck | Hash/Canonicalization 实现错误 |
| PR 重复或覆盖 | Provider ID、Task Marker、默认不 Force Push | 远程人工修改需要 Reconciliation |
| Git Hook 产生副作用 | Publisher 使用 Controlled/Disabled Hook | Verification Command 仍可能执行仓库脚本 |
| Adapter/Plugin 被入侵 | 显式安装信任、子进程边界、版本快照 | MVP 不能安全运行任意 In-process Plugin |

## 默认拒绝的副作用

Worker 禁止：

- `git push`、Forge API、PR/MR 创建、Merge、Release、Deployment 与 Package Publish；
- 读取 Credential Store 或主动发现 Secret；
- 修改 Git Remote、Global Git Config、Hook 或 Repository Setting；
- 修改 Task Worktree 外文件；
- 自行启用 Network 或额外 Tool；
- 未经 TaskSpec/Policy 明确授权而 Spawn 其他 Coding Agent。

Prompt 禁令必须尽可能由 Process/Tool Policy 强制。Provider 无法满足时，Capability Probe 应失败，或将 Run 明确标记为较低 Assurance。

## 环境构造

Marshal 从 Allowlist 构造环境，而不是继承环境后只删除几个已知变量。只提供执行所需的 Path、Locale、Temporary Storage、批准 Toolchain 和显式非 Secret 配置。

原生 PTY 同样不得继承 Desktop、cmux 或 login shell 的 ambient environment。Marshal使用 owner-only 的一次性 `LaunchEnvelope` 把精确环境交给受信任 launcher；可见 argv 只包含信封路径，launcher 在 `exec` Worker 前删除信封。环境值不得进入 screen、Journal 或普通日志。该机制降低意外泄露，但不隔离同 UID 恶意宿主进程；强隔离仍要求 `hardened` Profile。

Secret 仅在需要它的授权组件内 Just-in-time 解析。Publisher Credential 不得写入 TaskSpec、Event、Prompt 或 Outcome File。

## 临时文件与权限

- State 与 Log 在平台支持时使用 Owner-only Permission。
- `marshal init` 默认通过 `.git/info/exclude` 排除 `/.marshal/`；只有显式选择时才修改受跟踪的 `.gitignore`。
- Temporary File 位于 Run-owned Directory，使用不可预测名称并原子 Rename。
- Worker 使用专属 Temporary Directory。
- Unix Socket、FIFO、Device File 与 Symlink 默认不能作为普通 Artifact。
- Cleanup 不沿 Symlink 离开 Run/Worktree Root。

## Supply Chain

- 锁定 Go Toolchain 与 Marshal Dependency，并提交 `go.mod`、`go.sum`。
- 解析 Worker Executable Path 并记录版本。
- Run 期间不得自动更新 Worker。
- 初始第三方 Adapter 不得 In-process 执行。
- Marshal 自身 CI 运行 Dependency Audit、Format、Vet、静态检查、Test 与 Secret Scan。
- Adapter 文档只是参考，真实支持由 Feature Probe 与 Conformance Test 决定。

## Review 与发布完整性

- ReviewDecision 必须携带 Evidence Identity。
- Publisher 在副作用前重新计算 Snapshot 与 Evidence。
- 受控 Commit 对普通文件使用 raw blob，对符号链接使用 link target；观察与发布均屏蔽仓库 local filter、ambient hook、credential helper 以及 system/global Git config。
- 强制 Gate 失败时，没有 Policy-valid Waiver 就不能 Accept。
- Publisher 记录认证后的 Forge Identity，但不暴露 Token。
- Publisher 只接受无 Force Push 的新分支创建或经 `previousHeadSha` 证明的返工 fast-forward；CI 必须绑定同一 Repository、Draft PR 与 Head SHA。
- 实际 Merge 不属于 MVP 权限。

## 安全就绪等级

### Local MVP

适合维护者自己的可信仓库和交互式监督。CLI 必须明确显示 Host Containment 与 Network Denial 无强保证。

### Unattended Isolated Runner

要求 Ephemeral Runner/Container、最小 Forge Token、发布前 Read-only Base Checkout、显式 Network Policy 与独立 Publication Job。

### Multi-user / Hostile-code Service

不在当前范围。必须先完成专门 Threat Model 与 Hardened Isolation Review，不能把 Local MVP 宣传成满足此等级。

## 实施安全验收条件

MVP 宣称可用前必须证明：

- Worker Output 不能扩大 TaskSpec Scope；
- Worker Environment 不含 Publisher Credential；
- 陈旧 Evidence 不能发布已变化 Snapshot；
- Path Traversal 与 Symlink Escape Fixture 默认失败；
- Cancellation 能终止 Child Process Tree；
- 平台支持时，Log/State 使用限制权限；
- CLI 明确展示 Effective Assurance Profile；
- 文档持续声明普通宿主执行不是恶意代码沙箱。
