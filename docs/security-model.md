# 安全模型

## 安全定位

Marshal 编排能够编辑文件并执行仓库代码的进程，这天然具有高影响。MVP 面向单个开发者、可信 Worker Binary 和开发者可控仓库。它提供严格的审计与工作流控制，但只有在可强制执行的 Sandbox Profile 中才提供宿主隔离。

安全声明必须绑定到有效的执行契约并记录在 Outcome Bundle。自 [ADR 0017](adr/0017-provider-neutral-sandbox-contract.md)（已接受，2026-08-10）起，执行契约以二维组合 `AccessMode × AssuranceLevel` 表达（旧 `executionProfile` 为其兼容面）。仅仅告诉模型“安全操作”，不能把 Run 描述成已沙箱化。

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

## Execution Profile 与二维权限/隔离模型

Local MVP 的单一 Execution Profile 保留为兼容面：

| Profile | 用途 | 可声明的保证 |
| --- | --- | --- |
| `read-only` | Inspection 与 Review | Marshal 不授予 Edit Tool；Host Process 隔离仍取决于 Sandbox |
| `workspace-write` | 可信本地 Coding | 独立 Worktree、过滤环境和工作流 Gate；不隔离恶意代码 |
| `hardened` | 不可信代码或无人值守 | Container/VM/OS Sandbox 强制 Mount、Network、Resource 与 Credential Isolation |

Repository Policy 选择最低要求。Adapter 不能满足时必须在 `READY` 前失败。

自 [ADR 0017](adr/0017-provider-neutral-sandbox-contract.md)（已接受，2026-08-10）起，Runtime 阶段以两个正交维度取代单一 Profile 的内部表示：

| 维度 | 取值 | 回答的问题 |
| --- | --- | --- |
| `AccessMode`（权限） | `read-only` / `workspace-write` | 能做什么 |
| `AssuranceLevel`（隔离） | `workspace-write` / `hardened` | 强制有多可信 |

- 四种组合均合法，包括 `read-only × hardened`（不可信代码评审）；旧 Profile 按固定映射解析：`read-only` → `read-only × workspace-write`、`workspace-write` → `workspace-write × workspace-write`、`hardened` → `workspace-write × hardened`；历史持久记录不重写；
- `hardened` 必须绑定密封 `ConformanceEvidence`（provider identity/version、suite/probe artifact digest、mount/network/resource/credential 逐维结果、`evidenceDigest`、有效期/撤销语义）；Provider 自报 Enforcement 不能获得 `hardened`。证据拓扑（ADR 0017 §2）：probe 定义/challenge/nonce/artifact digest/调度/out-of-band 观察/裁决/签发由 Control Plane 与独立 Conformance Verifier 控制；probe workload 作为敌对测试负载运行在被测 Provider 创建、身份精确绑定的 target allocation 内（这样才能测到被测 Provider 自身的强制能力）；Provider 的 completed/receipt 只是裁决输入，不能自签通过。该拓扑不同于业务独立验证（业务 Verifier 运行在独立于 Worker 的 sandbox），不可混用；
- Local 普通宿主进程 Provider 永不 `hardened`；Cloudflare 与第三方 Provider 一律通过相同证据准入，无豁免；证据过期或被撤销时，Provider 回落到最高 `workspace-write` AssuranceLevel；
- AssuranceLevel 无法满足时 fail closed，Run 保持 `BLOCKED`，绝不静默降级；降级只能是操作者显式创建新 Run 的决策并记录于 Outcome；AccessMode 在 Run 内不可升级；不得为简化 Adapter 而静默放宽门禁。

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

## Runtime 阶段的安全边界（ADR 0016 与 ADR 0017）

[ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md) 冻结耐久 Runtime、[ADR 0017](adr/0017-provider-neutral-sandbox-contract.md)（已接受，2026-08-10）冻结 provider-neutral Sandbox 安全契约后，以下边界随 M8–M12 实施生效：

- 可丢弃执行体：Sandbox、Agent 与 Runtime 进程均可丢弃；权威事件、证据与副作用记录必须在其外部耐久保存，恢复结论只能凭持久事件账本得出；
- 凭据不进入执行环境：环境构造规则同样适用于远程 Sandbox；SandboxAllocation 只保存 provider-neutral 的 opaque locator 与 receipt，Provider 内部凭据不得进入 TaskSpec、事件、Prompt 或日志；
- Warm reuse 不是默认：默认每 Attempt 独立 ephemeral sandbox；复用仅限相同 tenant/repository/trust-domain 且有可证明的 sanitization；
- 提交入口边界：M8/M9 的提交入口默认只绑定 loopback 或受信任本地边界；远程入口在生产可用前必须具备 TLS、调用者身份认证、按 repository/project 的授权与审计记录（M11 退出门禁验收）；幂等身份为 `(scope, idempotencyKey, requestDigest)`，同 scope+key 而 digest 不同必须冲突 fail closed，不得归并进错误 Run；
- 权威写入接纳：Attempt 回报与 Artifact/Checkpoint/Candidate/Evidence 接纳必须携带 attemptId、generation、fencingToken 并在权威写入边界以 expectedSequence/CAS 校验；陈旧 token 内容只能隔离留存为诊断材料，不得进入当前 Evidence/Review/Publication；
- Cloudflare Sandbox 是可选托管 Provider：容器闲置、故障或重启会丢失文件、进程与 session，R2 backup 只是恢复优化，不是权威状态；`hardened` 必须持有独立签发的有效 `ConformanceEvidence`；Provider 失败不在 Attempt 内回退——失败的 Allocation/Attempt 先终止并对账，仅新 Attempt 可分配满足同一冻结要求与 assurance 下限的兼容 Provider，无兼容 Provider 时 fail closed；
- 多节点部署的身份分离：既覆盖 Worker、Verifier/Marshal、Publisher 彼此独立的 workload identity 与写入域（Worker 不得写权威证据或发布记录），也覆盖操作者与 API 入口身份；两类身份不得混用；
- workloadRole 与 principal 拆分（ADR 0017 §4）：Sandbox `workloadRole` 是封闭枚举，只允许 `worker`/`verifier`（conformance probe 以 `workloadRole=verifier` 在被测 Provider 的 target allocation 内运行为例外场景，见证据拓扑）；`control-plane`、`publisher`、operator、API caller 是不同语义 Port 上受 AuthZ 约束的认证 principal/actor，不是 workloadRole；**Publisher 永不成为 Sandbox workload**；远程请求身份额外绑定 `principal`/`portKind`/`providerType`/`audience`/`scope`，Provider 不得借通用 role 取得跨 Port 能力，跨 Port 能力请求 fail closed；
- 普通宿主进程不宣称恶意代码隔离的规则在 Runtime 形态下继续有效；
- Stage 内容寻址（ADR 0017）：每个冻结输入携带或引用真实 content-addressed bytes（inline 小对象或 ArtifactStore locator），Provider 消费前后重算 sha256，禁止只回显声明 digest；篡改 bytes 的 conformance fixture 必须让回显型 Provider 失败；
- 操作身份与重放（ADR 0017 §4）：每个 Sandbox SPI 操作与远程副作用携带 task/run/attempt/workloadRole/allocation/generation/fencingToken/commandId 完整身份元组（workloadRole 仅 worker/verifier）；远程请求另绑定 principal/portKind/providerType/audience/scope；普通 replay 先过当前 lease fencing；Restore 的 lost-response reconciliation 与普通 replay 分离，不重发同一 generation 的 Restore；不得以 HTTP 方法的表面幂等替代业务 fencing；
- Restore 无双写（ADR 0017）：默认 replacement allocation——旧进程树终止并失效后，以控制面单写者 CAS 激活新 generation；in-place 恢复后旧进程不得继续写；
- 规范化（ADR 0017）：digest/replay key/requestDigest/evidenceDigest 统一 RFC 8785 JCS；协议对象解析拒绝重复 JSON member；
- Secret/Artifact Provider（ADR 0017）：只交付有界引用或 workload-scoped 短期能力，secret 明文不得写入 TaskSpec、事件、Prompt、日志或 WorkerResult；
- Provider 观测边界（ADR 0017）：Provider 不得自行宣布 ReviewDecision 或 safe-to-publish；Verification Provider 只能执行独立验证 workload，不得决定 gate/ReviewDecision。

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
