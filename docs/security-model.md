# 安全模型

## 安全定位

Marshal 是长期运行并持续调度 Agent 工作负载的确定性 Control Plane。它编排能够编辑文件、执行仓库代码和触发外部副作用的进程，因此安全边界必须覆盖权威状态、Provider 身份、执行隔离、Evidence 接纳、凭据与 SideEffect，而不能只依赖 Prompt 或 Worker 自我约束。

当前 Local MVP 面向单个开发者、可信 Worker Binary 和开发者可控仓库。它已经提供严格的审计与工作流控制，但只有在可强制执行的 Sandbox Profile 中才提供宿主隔离；Runtime 阶段的完整边界随 M8–M13 逐步实现。

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

TaskSpec 与 Repository Policy 优先于 Worker Prompt 和自动发现的仓库指令。Prompt Injection 不能扩大路径、启用发布、暴露凭据或豁免 Gate。另一个 Agent 的调研报告、回答、复盘或历史经验仍是不可信语义输入；“由 Agent 生成”不构成可信来源。

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
| 跨 Worker / 跨 Goal 语义注入 | 当前禁用自由 mailbox 与自动 transcript/知识注入；使用 immutable Artifact ref、producer provenance、显式下游引用 | 内容本身仍可能错误，需要独立 Evidence 与 Review |
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

受控 merge 的目标安全门禁见 [ADR 0033](adr/0033-journal-bound-merge-authority-and-delivery.md)（Proposed）：授权与 prepared intent 必须是同一 Run journal 原子事实；每次 mutation 先追加 pending、同步 snapshot，再强制执行 mutation-adjacent journal/current/expiry recheck **AND** single-use fence，二者不可替代。fence consumption 必须由 Core 以 closed `publication.merge-mutation-fence-consumed` payload 写为 journal-bound authority fact，绑定 canonical replay identity 与 anchor lineage；journal 与 fence snapshot 均 durable 后才可 handoff，restart 看到 consumption 时不得重放。authorization revoke/authority append 与 fence→Provider handoff 使用同一 serializable ordering：revoke 先则零 mutation，handoff 先则本次调用先获授权、后到 revoke 只阻止后续调用。Publisher 只提供 typed observation/provenance，只有 Core 可校验后 CAS append；unknown/lag 保持 pending unresolved 并重复 Inspect。deadline 后 late receipt 只能通过 ADR 0026 唯一终态例外，在同一 transaction 关闭 pending、写 reconcile 与 Outcome。delivery budget 只从 journal pending facts 派生。credentialed `gh` 必须通过已打开且已校验的只读 fd/immutable handle 执行同一对象，并对输出设界、超限时终止进程组。只校验路径后再次按路径打开不构成 TOCTOU 关闭。A–D 完整实现与 conformance 通过后也只允许 local/non-production 受限 profile；production supported 还要求 M11 external rollback witness 与跨节点 fence 恢复演练。

## 安全就绪等级

### Local MVP

适合维护者自己的可信仓库和交互式监督。CLI 必须明确显示 Host Containment 与 Network Denial 无强保证。

### Unattended Isolated Runner

要求 Ephemeral Runner/Container、最小 Forge Token、发布前 Read-only Base Checkout、显式 Network Policy 与独立 Publication Job。

### Multi-user / Hostile-code Service

不在当前范围。必须先完成专门 Threat Model 与 Hardened Isolation Review，不能把 Local MVP 宣传成满足此等级。

## Runtime 阶段的安全边界（ADR 0016–0019）

[ADR 0016](adr/0016-durable-runtime-and-sandbox-provider.md) 冻结耐久 Runtime，[ADR 0017](adr/0017-provider-neutral-sandbox-contract.md) 冻结 Sandbox 安全契约，[ADR 0018](adr/0018-control-plane-and-provider-ports.md) 冻结 C/S Control Plane、按信任域分隔的 Provider Port、权威/actor 双键空间与 typed cross-domain edge；[ADR 0019](adr/0019-deterministic-control-plane-typed-execution-and-goal-admission.md) 进一步冻结确定性 Supervisor、Typed Execution 接纳边界、Goal admission、Evidence 依赖适用性与 append-only 补偿。以下边界随 M8–M13 实施生效：

- 可丢弃执行体：Sandbox、Agent 与 Runtime 进程均可丢弃；权威事件、证据与副作用记录必须在其外部耐久保存，恢复结论只能凭持久事件账本得出；
- 凭据不进入执行环境：环境构造规则同样适用于远程 Sandbox；SandboxAllocation 只保存 provider-neutral 的 opaque locator 与 receipt，Provider 内部凭据不得进入 TaskSpec、事件、Prompt 或日志；
- Warm reuse 不是默认：默认每 Attempt 独立 ephemeral sandbox；复用仅限相同 securityDomainId（相同 tenant/repository/trust-domain）且有可证明的 sanitization；
- 提交入口边界：M8/M9 的提交入口默认只绑定 loopback 或受信任本地边界；任何非 loopback/in-process 入口自首次远程 enable 起必须满足 ADR 0018 §12 transport 安全基线（TLS 强制、双向身份校验、credential rotation/revocation 与 replay protection）；生产远程入口另须具备调用者身份认证、按 repository/project 的授权与审计记录（M11 退出门禁验收；M11 只扩展 HA/多用户策略，不补首次安全基线）；TaskSubmission 与幂等权威记录均由 authorityNamespaceId 拥有（ADR 0018 §10），幂等身份为 `(authorityNamespaceId, scope, idempotencyKey, requestDigest)`，同 key 而 digest 不同必须冲突 fail closed，不得归并进错误 Run；
- 远程 transport 安全基线（ADR 0018 §12）：任何非 loopback/in-process transport 从首次 enable 起强制 TLS，loopback/in-process 之外禁止明文 transport；workload-to-workload 优先 mTLS 或等价不可转移 workload identity，可转移共享 secret 不得作为 workload 身份；每次调用双向校验 server/provider 身份与 audience/scope，短期 credential 支持 rotation/revocation 并具备 replay protection；M9/M10/M12 各远程能力首次 enable 时必须满足，M11 只扩展 HA/多节点/多用户授权策略，不能补首次安全基线；
- 权威写入接纳按 Port 分流（ADR 0018 §3）：dispatch-bound Port 的 Attempt 回报必须携带 attemptId、generation、fencingToken 并在权威写入边界以 expectedSequence/CAS 校验；publication/artifact/secret Port 按各自域绑定校验（SideEffectIntent/ReviewDecision/evidence digest；scoped handle/content digest/scope/expiry）；接纳与 ledger transition、当前 lease generation 同原子校验（atomic compare-and-append/transaction），陈旧 token 内容只能进入 quarantine namespace 隔离留存为诊断材料，不得进入当前 Evidence/Review/Publication（ADR 0018 §13）；
- Cloudflare Sandbox 是可选托管 Provider：容器闲置、故障或重启会丢失文件、进程与 session，R2 backup 只是恢复优化，不是权威状态；`hardened` 必须持有独立签发的有效 `ConformanceEvidence`；Provider 失败不在 Attempt 内回退——失败的 Allocation/Attempt 先终止并对账，仅新 Attempt 可分配满足同一冻结要求与 assurance 下限的兼容 Provider，无兼容 Provider 时 fail closed；
- 多节点部署的身份分离：既覆盖 Worker、Verifier/Marshal、Publisher 彼此独立的 workload identity 与写入域（Worker 不得写权威证据或发布记录），也覆盖操作者与 API 入口身份；两类身份不得混用；
- workloadRole 与 principal 拆分（ADR 0017 §4，ADR 0018 §3 按 Port 分流）：Sandbox `workloadRole` 是封闭枚举，只允许 `worker`/`verifier`（conformance probe 以 `workloadRole=verifier` 在被测 Provider 的 target allocation 内运行为例外场景，见证据拓扑）；`control-plane`、`publisher`、operator、API caller 是不同语义 Port 上受 AuthZ 约束的认证 principal/actor，不是 workloadRole；**Publisher 永不成为 Sandbox workload**；身份按 Port 冻结、不设 universal envelope，Provider 不得借通用 role 取得跨 Port 能力，跨 Port 能力请求 fail closed；
- Provider 信任域隔离（ADR 0018 §2）：Agent/Sandbox/Verification workload executor 属低权限 Execution 信任域（trust domain）；SCM/Publisher transport 属独立高权限 Publication 信任域（trust domain）；Artifact/Secret 属 Data/Capability 信任域（trust domain）；域之间不共享 credential、AuthZ、审计或 conformance profile；三类域由 securityDomainId 复合三元组的 trustDomainKind（execution|publication|data-capability）机械标识；Provider actor 跨域能力请求与跨 trustDomainKind 引用默认拒绝（default deny），唯一 allowlist 例外是三条 Core-only typed edge——未经对应 active edge 授权，或 source/target securityDomainId 与该 edge 的对象、operation、Attempt/Allocation、generation、expiry/deadline、digest、当前 authority ledger 状态任一不精确匹配的访问一律 fail closed 并写入审计（ADR 0018 §3/§10）；securityDomainId 只标识 Provider actor，Control Plane 权威对象归属 authorityNamespaceId（ADR 0018 §10）；
- securityDomainId 复合安全域键空间（ADR 0018 §10）：securityDomainId 是复合 security namespace `(tenantNamespace, trustDomainKind, isolationDomainId)`——tenantNamespace 单租户部署可固定 `default`（tenant 只能作为该组成参与授权，不得以自由文本绕过），trustDomainKind 封闭枚举 execution|publication|data-capability，isolationDomainId 标识同一 trustDomainKind 内的隔离边界；不得使用全系统单一 default 同时宣称隔离 Execution/Publication/Data-Capability；actor 侧 securityDomainId 进入 registration/snapshot/evidence 携带项、lease/allocation actor 绑定、artifact/secret scoped handle、cache key 等引用字段的持久键空间，只用于 actor 身份、provenance 与授权判定；submission/run lifecycle、DispatchLease/Allocation、ReviewDecision、Outcome、SideEffectIntent/Receipt reconcile、Evidence graph、typed edge、SSE cursor/sequence、idempotency/replay 权威键、outbox、audit 与事件账本归权威侧 authorityNamespaceId 拥有，不进入 actor 侧键空间；actor 侧跨域引用默认拒绝并写入审计，唯一 allowlist 例外是三条 Core-only typed edge：未经对应 active edge 授权或绑定不精确匹配的跨 securityDomainId/跨 trustDomainKind 引用一律 fail closed，无论 tenantNamespace/isolationDomainId 是否相同，execution、publication、data-capability 之间都不得在 edge 授权范围之外互相解析句柄；securityDomainId 三元组完全相同也只是 actor provenance/partition 条件，不构成授权、不构成同域 bearer grant，同域请求仍须逐项匹配具体 Port 的 principal、registrationId、providerInstanceId、scope、attempt/allocation、generation、operation 门禁；provider-registration/control 与 public-api 不持有三类业务 typed edge，经 transport identity、该 Port 的 AuthN/AuthZ、scope/protocol validation 与 registration protocol 校验，由 Core 将获准事实写入 authority ledger（ADR 0018 §3/§5）；现在冻结，不等 M11 再迁移持久主键；
- Control Plane 权威与 Provider actor 分离（ADR 0018 §10）：authorityNamespaceId=(tenantNamespace, controlPlaneId, authorityScopeId) 拥有全部 Control Plane 权威对象——Project/Goal、TaskSubmission、Task/Run/Attempt lifecycle 状态、DispatchLease、Allocation、ReviewDecision、Outcome、SideEffectIntent、Receipt reconcile 记录、typed edge 记录、Evidence graph、事件账本、发布决定、idempotency/replay 权威记录、outbox、audit 记录与 SSE cursor 权威序列——只允许 Core 写入（controlPlaneId 是 HA/灾备中保持稳定的逻辑权威身份，不是进程实例；单实例部署 controlPlaneId 可固定 `default`，authorityScopeId 映射 repository/project 等冻结 scope）；ProviderRegistration、ProviderCapabilitySnapshot 与 ConformanceEvidence 也是 authority ledger 事实，仅携带 actor securityDomainId、provenance 与 eligibility；Artifact/Checkpoint/Candidate/Evidence bytes 的接纳关系归 authority ledger（ADR 0018 §13）；securityDomainId=(tenantNamespace, trustDomainKind, isolationDomainId) 只标识 Provider actor；authorityNamespaceId 不是 Provider 的 trustDomainKind 维度，不属于 Provider actor 侧任何信任域，Provider 不得写入、复制、宣称拥有或宣称任何权威对象；反向地，securityDomainId 不承载 Control Plane 权威，Provider 不得以它宣称 lifecycle、ReviewDecision 或发布决定；Core 内部权威对象引用（ledger 事件间引用、cursor、证据关系、outbox/ledger 引用）保留在 authority ledger 内，不需要 Provider typed edge；权威侧记录携带 `(tenantNamespace, controlPlaneId, authorityScopeId)`，actor 侧引用字段携带 `(tenantNamespace, trustDomainKind, isolationDomainId)`，各自进入持久主键/引用键空间；
- Core-only typed cross-domain edge（ADR 0018 §3）：三条 Core-only typed edge 是 Provider actor 跨 trust domain 访问默认拒绝（default deny）规则的唯一 allowlist 例外——DispatchResultCapability（issuer=Core；sourceActor=Execution workload，绑定 securityDomainId/principal/registrationId/providerInstanceId；targetAudience=Core result-ingress；Execution 信任域 dispatch-bound Port 的 result/log/checkpoint/candidate/evidence-ref/heartbeat/receipt 接纳能力，绑定 leaseId/generation/fencingToken、commandId/idempotencyKey/requestDigest/nonce 与 expiry/revocationGeneration）、MaterialAccessGrant（issuer=Core；sourceActor=Data/Capability Provider，绑定 securityDomainId/registrationId/providerInstanceId/configDigest/trust root；targetActor=Execution workload，绑定 securityDomainId/principal/attemptId/allocationId/generation；对 Data/Capability 信任域 Artifact/Secret 物料的 scoped 访问短期能力，operation 封闭 read/fetch/decrypt，绑定 typed materialRef/version/commitment、scope/maxBytes/maxUses、expiry/revocationGeneration 与 commandId/requestDigest/nonce；Execution 不解析 Data 域 raw handle，Data Provider 只接受 target-bound grant，禁止转授/bearer 化/跨 Attempt 复用）、PublicationAuthorization（issuer/sourceAuthority=Core/controlPlaneId；targetActor=Publication Provider，绑定 securityDomainId/publisher principal/registrationId/providerInstanceId；Publication 信任域绑定 SideEffectIntent/ReviewDecision/evidence digest 的发布授权，绑定 repository/remote/baseRef/headRef/expectedRemoteHead、Draft-only、merge-never、requestDigest/commandId/idempotencyKey/nonce、expiry/revocationGeneration 与 receiptReconcileId）——其余默认拒绝（default deny）；Core 是唯一签发者、唯一撤销者与唯一重新授权者，每条 edge 的 issuer 为 Core，issuer 与业务流的 sourceActor 不是同一概念；每条 edge 是 Core 在 authorityNamespaceId 内签发的 authority-scope-bound 权威记录（typed edge 记录由 authorityNamespaceId 拥有），冻结 issuer/source/target（sourceActor/targetActor/targetAudience 按 edge 类型绑定 securityDomainId 标识的 typed identity，未绑定身份不得充当 sourceActor 或 targetActor，target 不得被替换或改道）、operation（各 edge 封闭枚举，枚举外请求拒绝）、expiry、digest、revocation、replay 与 current-ledger recheck 七项生命周期要素；授权不可转授、不可扩权；每次使用都按当前 authority ledger 复核（重判 edge 仍 active、lease、registration/snapshot/evidence eligibility 与 generation，旧 generation 只进 quarantine）；edge 只承载 scoped handle、digest 引用与授权引用，不得携带 raw credential、raw secret handle，也不得替代 ConformanceEvidence；edge 派生的 token/handle 只是指向 edge 权威记录的单向引用，自身不承载授权语义，派生 token/handle 不得成为第二权威；Provider actor 跨 trustDomainKind 访问未经对应 edge 授权或任一绑定不符一律 fail closed，过期、被撤销或 digest 不符的 edge 拒绝并写入审计；Provider 之间不得互相签发、转授或延展 edge；
- Provider attestation 全链绑定（ADR 0018 §11）：ProviderRegistration、ProviderCapabilitySnapshot、ConformanceEvidence 与 lease claim 全链绑定 securityDomainId、稳定 providerInstanceId、effective configDigest 与签发/验证 trust root（含 key id/rotation）；任一变化产生新 immutable 快照/证据并触发 eligibility 重判，相同软件版本换实例、换配置或换签发密钥不得复用 hardened evidence；同一 Run 的 Worker/Verifier 必须不同 principal 与不同 allocation，高保证策略可要求 provider/host/failure-domain diversity；
- 普通宿主进程不宣称恶意代码隔离的规则在 Runtime 形态下继续有效；
- Stage 内容寻址（ADR 0017）：每个冻结输入携带或引用真实 content-addressed bytes（inline 小对象或 ArtifactStore locator），Provider 消费前后重算 sha256，禁止只回显声明 digest；篡改 bytes 的 conformance fixture 必须让回显型 Provider 失败；
- 操作身份与重放（ADR 0017 §4，ADR 0018 §3 按 Port 分流）：dispatch-bound 的 Sandbox SPI 操作携带 task/run/attempt/workloadRole/allocation/generation/fencingToken/commandId 完整身份元组（workloadRole 仅 worker/verifier）；普通 replay 先过当前 lease fencing；Restore 的 lost-response reconciliation 与普通 replay 分离，不重发同一 generation 的 Restore；不得以 HTTP 方法的表面幂等替代业务 fencing；
- Port 分流矩阵（ADR 0018 §3）：身份/接纳/凭据/fencing 按 Port 分流；public-api Port 禁止 providerType 并拒绝携带 workloadRole/allocationId/generation/fencingToken/DispatchLease 的请求，一律 fail closed；provider-registration/control Port 拒绝 workload lease 字段，只处理注册/查询/撤销/失效；publication Port 绑定 SideEffectIntent、ReviewDecision 与 evidence digest；artifact/secret Port 绑定 scoped handle/content digest/scope/expiry，禁止伪造无关 lease。`fencingToken` 是非凭据（non-credential）的 stale-write guard，不能替代 AuthN/AuthZ；credential 不进入业务 JSON、事件、日志或 digest；
- 耐久注册与在途 lease 撤销（ADR 0018 §5/§6）：Runtime 持久化 ProviderRegistration 与不可变 ProviderCapabilitySnapshot，禁止 memory-only registration；ProviderRegistration、ProviderCapabilitySnapshot 与 ConformanceEvidence 也是 authority ledger 事实，由 authorityNamespaceId 拥有、只允许 Core 写入，仅携带 actor securityDomainId、provenance 与 eligibility；registrationId 幂等身份 canonical 绑定 `(securityDomainId, principal, providerType, providerName, providerVersion, protocolVersion, scope)`（securityDomainId 为所携带的 actor 身份）与 idempotencyKey/requestDigest，revoked/expired 不因普通 replay 复活；legacy v1alpha1 CapabilitySnapshot 字节/digest 不变，只经 fail-closed mapper 转换并记录 sourceCapabilitySnapshotDigest；DispatchLease 只消费持久 ProviderCapabilitySnapshot（双绑定权威侧 authorityNamespaceId 与 Provider actor 侧 securityDomainId，并绑定 registrationId、providerCapabilitySnapshotDigest、conformanceEvidenceDigests 与 attestation 链）；registration revoke/expire/incompatible、snapshot supersede/expire、evidence revoke/expire 使 active lease（在途 lease）立即失去资格，Allocation/Attempt 终止对账、晚到结果隔离，继续执行只能新 Attempt + 新 lease 重新 match；失效处置分级：security-critical revoke（credential compromise、protocol violation）立即 cancel + generation bump + kill，不留 drain 窗口；planned/ordinary incompatible upgrade 使用新 registration/新 snapshot，旧实例 stop-new + bounded drain，drain deadline 到期再 fence；事件机器可读原因码与审计记录分开；普通升级不得复活旧注册或改写旧 lease digest；
- 原子 fencing 写入汇（ADR 0018 §13）：权威 ledger sink 使用同事务 atomic compare-and-append/transaction，ledger transition、当前 lease generation 与 Evidence/Artifact 引用同原子校验提交；Artifact/Evidence/Checkpoint/Candidate bytes 的接纳关系归 authority ledger，使用 authorityNamespaceId+run+attempt+allocation+generation scoped 的 immutable key 与 digest-verified put-if-absent（actor securityDomainId 只作为 provenance 记录），已存在 key 永不覆盖；陈旧/冲突 bytes 只能进入 quarantine namespace，永不覆盖当前对象、永不进入当前 evidence graph；
- SSE 恢复与再授权（ADR 0018 §14）：SSE 是只读投影，cursor 身份为 authorityNamespaceId+scope+ledgerSequence（权威账本的权威侧身份），订阅方另绑定自身 securityDomainId 完成授权判定，scope 内 sequence 单调，at-least-once 交付 + eventId/sequence 客户端去重；cursor 过期、gap 或被压缩时返回 deterministic resync 起点与 snapshot digest；服务端 heartbeat 与有界 backpressure（超限断开引导 resync，不阻塞 ledger）；周期性 re-Authorization 与敏感变更（registration 撤销、scope 变更、权限收回）即时 re-Authorization；SSE 不承载业务 ACK、lease heartbeat 或 command 下发；Public API/SSE 是 Client 到 Control Plane 的入口，使用各自的 AuthN/AuthZ、scope 约束与 re-AuthZ，不需要 Provider typed edge；参数值留 M9 Schema 冻结；
- DurableExecutionEngine 单一权威 seam（ADR 0018 §15）：同事务 outbox 或 ledger-derived Core command journal 二选一，backend profile 声明所选 seam；commandId 从 ledger 权威事实稳定派生；backend（Temporal、Local Engine）只消费 command 并回报，workflow/activity state 不得成为业务权威，不得决定 lifecycle/retry/rework/终态；
- 按 Port 的 versioned protocol family（ADR 0018 §16）：每个具体 Port 独立 audience、AuthZ scope、request/response schema、error 模型、幂等约定、撤销语义与 conformance profile；跨族只共享 transport 层、JCS 与最小 base auth primitives；禁止跨 Port 复用 token、schema 或 operation；六类 Provider 分属不同 protocol family，不共享 family、audience、schema、profile、conformance suite、token 或 operation；embedded/Push/Pull 只是同一 Port protocol family 内的 adapter，运行该族统一的 conformance suite；
- Push/Pull 不变量等价（ADR 0018 §16）：Push 与 Pull 拓扑只冻结 outcome/invariant equivalence——唯一 claim、eligibility、fencing、deadline（ack/heartbeat/expiry）、无双活与晚到结果隔离；允许拓扑特定（topology-specific）的 offer/poll/claim/ack transition 与 timing；conformance 比较 normalized business trace 与业务不变量，不比较逐步 wire trace；不得为 Push 定义弱化不变量的简化协议；
- 账本与投影（ADR 0018 §4）：append-only event ledger 是唯一业务权威；snapshot/queue/SSE/Provider registry/索引是可重建投影（projection）；SSE cursor 过期、gap 或被压缩时返回可判定的 resync；DurableExecutionEngine 是 Core 的内部 Port，backend 只承担 at-least-once delivery/timer/signal/crash recovery，不构成第二个权威；
- Restore 无双写（ADR 0017）：默认 replacement allocation——旧进程树终止并失效后，以控制面单写者 CAS 激活新 generation；in-place 恢复后旧进程不得继续写；
- 规范化（ADR 0017）：digest/replay key/requestDigest/evidenceDigest 统一 RFC 8785 JCS；协议对象解析拒绝重复 JSON member；
- Secret/Artifact Provider（ADR 0017）：只交付有界引用或 workload-scoped 短期能力，secret 明文不得写入 TaskSpec、事件、Prompt、日志或 WorkerResult；
- Provider 观测边界（ADR 0017）：Provider 不得自行宣布 ReviewDecision 或 safe-to-publish；Verification Provider 只能执行独立验证 workload，不得决定 gate/ReviewDecision。
- Planner 越权与任务爆炸（ADR 0019）：Planner 输出是不可信 `GoalPlanProposal`，不能直接写 ledger、创建 Run 或执行 side effect；Core 必须校验 scope/allowlist、完整 effective DAG、跨 revision cycle 与整个 Goal 的累计节点、并发、Attempt、wall time、compute、token/成本和 Artifact 预算，并在 dispatch 前原子 reservation；
- Prompt injection 不能扩权：仓库内容、上游 Artifact、Agent transcript 与 Review 文本都不是 Policy 或授权；它们不能改变 repository/scope、executor kind、side-effect class、credential、budget 或 gate；
- Agent 生成决策输入治理（[ADR 0046](adr/0046-governed-agent-decision-inputs.md)，Proposed）：跨阶段、Worker 或 Goal 的语义内容应先成为 immutable、content-addressed、digest-bound、带 provenance 与 purpose/audience 的有界对象，由下游显式 admission 并冻结引用，始终作为数据而非指令消费；禁止自动 transcript 注入、capability 文本转授和 planning/execution live knowledge query。该条在 ADR 0046 接受前只是演进门槛，不表示 mailbox、Discovery/Retrospective role 或跨 Goal learning 已实现；
- Evidence 适用性：Evidence bytes 不可变，当前 eligibility 从 subject/base/environment/Policy/Verifier capability/upstream Artifact/有效期依赖派生；依赖变化追加 ineligibility/supersession event，强制 gate 失败不能被 LLM Assessment 覆盖；
- 副作用与补偿：失败不回滚 authority ledger；cleanup/compensation 是新副作用并重新走 intent/receipt/reconcile。自动执行只限精确 target identity 与冻结 Policy 明确授权；ambiguous、不可逆、高权限或身份冲突均 fail closed；
- Goal 人工控制：Goal `PAUSED` 停止新 dispatch，resume 以 expectedSequence 写 ledger 并重新校验预算、Evidence、Provider eligibility 与 Policy；暂停期间按 Policy 释放 lease/sandbox，取消 active workload 必须 generation bump/fence。Run 不新增无限等待状态。

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
