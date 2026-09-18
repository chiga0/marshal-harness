# ADR 0112：Agent IAM 与执行策略

- 状态：Proposed（设计草案，供维护者审查；未接受、未实现，不授权部署或新增模型调用）
- 基线：ADR0111 合并基线之上
- 关联：ADR0108（TS 迁移）、ADR0109（SSE+async 缝线）、ADR0110（tenantScope 以 storeId 为值）、ADR0111（审计锚定）、[扩展契约](../extension-contracts.md) Binding/WorkBinding/Fault 闭集与 AgentPort/EnvironmentPort 语义、ADR0094（可信单用户角色团队）、ADR0014（read-only 执行画像）、ADR0017（二维权限/隔离模型与 ConformanceEvidence）、ADR0018（Control Plane 与 Provider Port 信任域分离）

## 问题

Marshal 当前行为是可信单用户（ADR0094）：Agent 以操作者 ambient 身份运行，持有同 UID 的文件、环境变量与原生登录可达性。这在单用户开发场景成立，但"数字员工在生产受控执行"需要回答一个 Marshal 和 Multica 都未闭合的问题：**每个任务的 Agent 到底能访问什么**。

两个项目的现状对比：

| 维度 | Multica 现状 | Marshal 现状 | 缺口 |
| --- | --- | --- | --- |
| 技能可见性 | `agent_skill` junction + per-agent `enabled` + SHA-256 content hash（migration 008/161, `skill.sql`） | 无 skill 概念，Agent 可调用任意工具 | Marshal 无 per-agent 技能准入 |
| 凭据范围 | `mat_` task-scoped token 绑定 (agent,task,workspace,owner)，plugin secret 连接时解析不落 task record | ambient 凭据可达，`publication:none` 默认阻止发布但不约束凭据注入 | Marshal 无 per-task 凭据注入合同 |
| 连接器范围 | plugin scope sets 闭集（`issues:read/write`, `net:<domain>` 等，`manifest.go` `ValidateScope`），per-route 强制 | `publication:none` 默认；Publication Port 有目标绑定但无 scope 粒度 | Marshal 无 per-task 连接器 scope |
| 执行旁路 | 硬编码 `--permission-mode bypassPermissions`（Claude/CodeBuddy）和 `danger-full-access`（Codex），用户不可覆盖 | 拒绝 Agent 自授权（ADR0013 分级拒绝），但无正向声明"能做什么" | 两项目均缺冻结的执行策略包 |

核心缺口是**缺少一个在计划批准时冻结、Agent 不可协商、harness 强制的 per-task 访问信封**。Multica 有技能/凭据/scope 的部分实现但主动开放旁路；Marshal 有 publication:none / custody / SandboxProvider 接缝但无技能可见性、无 scope 凭据、无连接器 scope。本 ADR 定义填补这一空白的**契约**——不是平台。

## 设计约束

- 必须 fit Marshal 现有 Port/DI 架构（[扩展契约](../extension-contracts.md)）：复用 Environment.prepare/start 与 Agent.begin 已有的"批准范围""凭据引用""受限工具/权限回调"输入位，不新增执行路径
- 必须保持零运行时依赖：类型用可擦除 TypeScript 语法（`type`/`interface`/`readonly`，不用 `enum`/`namespace`/decorator），默认实现只用 Node.js 内置
- 必须向后兼容：默认策略 = 当前行为，旧根/记录无 schema 变化
- 必须有可对任意实现复跑的一致性测试规范（参照 `packages/task-store/conformance.test.ts` 模式）
- 必须保持"Agent 不可自授权"不变量（ADR0013）：策略在 Agent 运行前冻结，Agent 不可修改

## 决策

### 1. ExecutionPolicy 值对象

定义一个在计划批准时冻结的值对象，声明本次任务 Agent **可以**访问什么。策略一旦冻结，只能通过新 Run（新计划批准）改变，不在 Attempt 内协商。

```typescript
/** 冻结于计划批准时；摘要进入 Binding.configurationDigest。 */
type ExecutionPolicy = {
  readonly profile: 'execution-policy/v1'

  /** null = 不限制（向后兼容）；数组 = 仅列出的环境变量对 Agent 可见 */
  readonly envAllowlist: readonly string[] | null

  /** null = 依赖 executionProfile 既有范围；对象 = 显式路径边界 */
  readonly fsPaths: {
    readonly read: readonly string[] | null
    readonly write: readonly string[] | null
  } | null

  /** null = 不限制；数组 = 仅列出的出站域名可达 */
  readonly networkDomains: readonly string[] | null

  /** null = 所有技能可见（向后兼容）；数组 = 仅列出的技能可用 */
  readonly skillAllowlist: readonly SkillEntry[] | null

  /** 空数组 = 无外部写（publication:none 默认） */
  readonly connectorScopes: readonly ConnectorGrant[]

  /** 空数组 = 无注入凭据 */
  readonly credentialRefs: readonly CredentialRef[]
}

type SkillEntry = {
  readonly skillId: string
  readonly contentDigest: string  // sha256 hex
}

type ConnectorGrant = {
  readonly connector: string   // 如 'issues'、'storage'、'net'
  readonly scopes: readonly string[]  // 如 ['read']、['read','write']
}

type CredentialRef = {
  readonly ref: string     // 不透明引用，永远不是值本身
  readonly purpose: string // 用途说明
  readonly scope: string   // 绑定的 scope
}
```

`null` 语义统一为"不限制（向后兼容）"，非 null 数组语义为"仅这些允许"。`connectorScopes` 和 `credentialRefs` 不使用 null——空数组即为默认值（无外部写、无注入凭据），与 `publication:none` 默认一致。

策略的规范编码使用 RFC 8785 JCS 序列化后取 SHA-256，复用仓库既有 JCS 基线（ADR0017 §11）。`policyDigest = sha256(jcs(ExecutionPolicy))`。

### 2. PolicyResolverPort

在计划准备阶段产生 ExecutionPolicy。这是 Core 在计划批准事务中调用的准备性 Port，不是 Agent 可调用的运行时接口。

```typescript
interface PolicyResolverPort {
  readonly profile: 'policy-resolver/v1'
  resolve(input: PolicyResolverInput): ExecutionPolicy
}

type PolicyResolverInput = {
  readonly planDigest: string
  readonly agentIdentity: string
  readonly taskScope: string       // tenantScope = storeId（ADR0110）
  readonly executionProfile: string  // 'read-only' | 'workspace-write'
}
```

默认实现 `createDefaultPolicyResolver()` 返回全 null + 空数组的策略，即当前 ambient 行为。受限策略由部署者审核并冻结的显式配置产生，配置身份进入 profile 摘要。

PolicyResolverPort 是**封闭的受信工厂**——不是公共插件加载 API。新增实现必须接入原装配和私有接纳机制（同 extension-contracts.md "能力与部署准入"节），不能仅返回相同 JSON 就获得准入。

### 3. CredentialVaultPort

在 claim 时将 `credentialRefs` 解析为短生命周期 handle，注入进程环境；在 release 时撤销。凭据值**永远不进入** task 记录、事件、日志、Prompt 或制品。

```typescript
interface CredentialVaultPort {
  readonly profile: 'credential-vault/v1'
  resolve(ref: string, binding: Binding): CredentialResolution
  revoke(handle: CredentialHandle): void
}

type CredentialResolution =
  | { readonly ok: true; readonly handle: CredentialHandle }
  | { readonly ok: false; readonly fault: Fault }

type CredentialHandle = {
  readonly handleId: string   // 不透明，不是值
  readonly envKey: string     // 注入的环境变量名
  readonly expiresAt: string  // ISO8601-UTC
}
```

关键不变量：

- **claim 时解析，不预存**：Vault 在 Environment.start 前（claim 阶段）被调用，不在计划批准或 Task 提交时解析。这对应 Multica 的 "broker asks for credential at connection time rather than receiving it in the claim payload"（`plugin_mcp.go`）与 `mat_` token 在 claim 时铸造（`daemon.go`）的设计——但作为 Port 合同，不是服务端功能。
- **值不落盘**：Vault 实现不得将凭据值写入 Store、Depot、事件、Audit 或日志。Audit 只记录 `ref` 和 `purpose`，不记录值。`resolve` 失败不重试到 ambient——fail closed。
- **release 时撤销**：Environment.release 前必须调用 `revoke(handle)`。撤销后 handle 失效，进程环境中的值由调用者负责清理（Vault 不修改进程环境，只提供值或句柄）。
- **不复制 ambient**：Vault 不从 `HOME`、`~/.config`、`~/.ssh` 或进程环境复制凭据。它只解析策略中显式声明的 `credentialRefs`。这阻止 "为 Agent 准备凭据" 变成 "复制操作者全部凭据"。

默认实现 `createNoopCredentialVault()` 对空 `credentialRefs` 无操作，对非空 ref 返回 `rejected{fault: unauthorized}`——即当前行为（不注入任何凭据）。

### 4. SkillGatePort

在 Agent.begin 前验证技能可见性。给定冻结的策略和技能请求，检查技能是否在 allowlist 中且内容摘要匹配。

```typescript
interface SkillGatePort {
  readonly profile: 'skill-gate/v1'
  check(
    policy: ExecutionPolicy,
    skillId: string,
    contentDigest: string
  ): SkillGateResult
  list(
    policy: ExecutionPolicy,
    agentIdentity: string
  ): readonly SkillEntry[]
}

type SkillGateResult =
  | { readonly allowed: true }
  | { readonly allowed: false; readonly reason: 'not-in-allowlist' | 'digest-mismatch' | 'no-policy' }
```

语义：

- `policy.skillAllowlist === null`：`check` 返回 `{allowed: true}`（不限制，向后兼容），`list` 返回空数组（无法枚举全部可用技能，只声明不限制）。
- `policy.skillAllowlist` 非 null：`check` 查找匹配 `skillId` 的条目；找到后比对 `contentDigest`，不匹配返回 `digest-mismatch`（fail closed，不降级为 not-in-allowlist）。`list` 返回 allowlist 副本。
- 内容摘要验证对应 Multica 的 `skillbundle/hash.go` SHA-256 manifest 与 `skill.sql` 的 `content_hash`——但作为 Port 合同，不要求技能注册表平台。技能内容来源由部署者审核并固定，SkillGatePort 只验证"本次 Agent 看到的技能与冻结策略声明一致"。

默认实现 `createPassthroughSkillGate()` 对 `null` allowlist 全部放行，对非 null allowlist 严格检查。

### 5. ConnectorFencePort

在 Publication Port 执行外部操作前验证连接器 scope。给定冻结的策略和操作请求，检查连接器是否被授权且 scope 是否被授予。

```typescript
interface ConnectorFencePort {
  readonly profile: 'connector-fence/v1'
  authorize(
    policy: ExecutionPolicy,
    connector: string,
    operation: string
  ): ConnectorFenceResult
}

type ConnectorFenceResult =
  | { readonly allowed: true }
  | { readonly allowed: false; readonly reason: 'connector-not-granted' | 'scope-not-granted' | 'no-policy' }
```

语义：

- `operation` 形如 `'issues:read'`、`'issues:write'`、`'net:api.example.com'`——scope 词汇是**部署者冻结的闭集**，不是 Agent 可扩展的。这对应 Multica 的 `plugincontract/manifest.go` `ValidateScope` 闭集校验与 `publicapi/v1/routes.go` per-route scope 强制——但由 harness 在执行边界强制，不是 Agent CLI 自律。
- `connector` 标识操作目标族（如 `'issues'`、`'storage'`、`'net'`），`operation` 是具体 scope。`authorize` 先匹配 connector，再检查 operation 是否在该 connector 的 scopes 列表中。
- `connectorScopes` 为空数组（默认）时所有操作返回 `connector-not-granted`——与 `publication:none` 默认一致。
- ConnectorFencePort 是 Publication Port 执行前的**验证合同**，不是新的执行路径。Publication Port 仍由 Core 授权、Environment 托管（extension-contracts.md "目标Publication与后验Port"节）。Fence 只增加一层"本次任务的连接器 scope 是否被冻结策略授予"的检查。

默认实现 `createDenyAllConnectorFence()` 对空 `connectorScopes` 全部拒绝，对非空 grants 严格检查。

### 6. 冻结点与 configurationDigest

ExecutionPolicy 在 Core 创建 Run 时冻结，与计划一起进入冻结输入。冻结流程：

1. Leader 提出计划 → PolicyResolverPort.resolve(planDigest, agentIdentity, ...) 产生 ExecutionPolicy
2. 用户批准计划 → ExecutionPolicy 随计划一起冻结，`policyDigest` 进入 Store
3. Core 创建 Run → `configurationDigest = sha256(jcs({planDigest, policyDigest, ...其他配置摘要}))`
4. Attempt 分配 → Binding 携带 `configurationDigest`（已含 policyDigest）
5. Environment.prepare → 接收冻结的 fsPaths/networkDomains 作为"批准目录/资源范围"
6. Environment.start → CredentialVaultPort.resolve 解析 credentialRefs
7. Agent.begin → SkillGatePort.check 验证技能；ConnectorFencePort.authorize 验证连接器操作
8. Environment.release → CredentialVaultPort.revoke 撤销凭据 handle

`policyDigest` 不是 Binding 的新字段——它是 `configurationDigest` 的组成部分。Binding 的闭集定义（extension-contracts.md）不变。策略变更需要新 Run（新计划批准），不在 Attempt 内修改。

### 7. 默认策略与向后兼容

默认策略保持当前行为：

```typescript
const defaultPolicy: ExecutionPolicy = {
  profile: 'execution-policy/v1',
  envAllowlist: null,        // 不限制
  fsPaths: null,             // 依赖 executionProfile
  networkDomains: null,      // 不限制
  skillAllowlist: null,      // 全部可见
  connectorScopes: [],       // 无外部写（publication:none）
  credentialRefs: [],        // 无注入
}
```

兼容规则：

- 旧根/记录无 schema 变化：ExecutionPolicy 存储为计划冻结输入的一部分（已有 Depot 制品），不新增数据库表或字段。
- 旧配置不自动启用受限策略：`defaultPolicy` 是全 null + 空数组。新配置身份改变须按原新根/显式升级规则处理，不对旧根静默迁移。
- `policyDigest` 不改变旧记录的 `configurationDigest`：旧记录的 configurationDigest 计算方式不变，新记录在计算中纳入 policyDigest。旧 reader 读旧记录不受影响。
- 默认 Port 实现保持当前行为：NoopCredentialVault、PassthroughSkillGate、DenyAllConnectorFence、DefaultPolicyResolver——全部可被默认构造，不引入新依赖。

### 8. 一致性测试规范

参照 `packages/task-store/conformance.test.ts` 模式，新增 `packages/task-execution/policy-conformance.test.ts`。任意 PolicyResolverPort / CredentialVaultPort / SkillGatePort / ConnectorFencePort 实现必须通过以下用例：

| # | 用例 | 断言 |
| --- | --- | --- |
| P1 | 默认策略保持 ambient | `defaultPolicy` 全 null + 空数组；`configurationDigest` 在纳入前后对旧记录无变化 |
| P2 | 策略冻结不可变 | Agent 运行中修改 ExecutionPolicy 对象不改变 `policyDigest`；Core 不接纳与冻结策略不一致的 Binding |
| P3 | policyDigest 进入 configurationDigest | 同策略产生同 digest；异策略产生异 digest；JCS 规范化后 SHA-256 确定性 |
| P4 | envAllowlist 排除非列 | 非 null allowlist 时，未列出的环境变量对 Agent 不可见；null 时不限制 |
| P5 | fsPaths 强制边界 | 非 null 时，未列出的读/写路径被拒绝；null 时依赖 executionProfile |
| P6 | networkDomains 强制边界 | 非 null 时，未列出的域名出站被拒绝；null 时不限制 |
| P7 | skillAllowlist 准入 | 非 null 时，未列出技能返回 `not-in-allowlist`；列出但摘要不匹配返回 `digest-mismatch` |
| P8 | 技能内容摘要验证 | 篡改技能 bytes 后 contentDigest 不匹配；通过需重算 SHA-256 |
| P9 | connectorScopes 强制 | 空数组时全部拒绝；非空时仅授予的 connector+scope 通过；越界操作拒绝 |
| P10 | 凭据 claim 时解析 | `resolve` 在 Environment.start 前调用；不在计划批准或 Task 提交时解析 |
| P11 | 凭据不落盘 | `resolve` 返回的 handle 和 envKey 不出现在 Store、Depot、事件、Audit、Prompt 或日志中；Audit 只记录 ref 和 purpose |
| P12 | 凭据 release 时撤销 | `revoke` 在 Environment.release 前调用；撤销后 handle 不可用 |
| P13 | 凭据不复制 ambient | Vault 不从 HOME、~/.config、~/.ssh 或进程环境读取；只解析显式 credentialRefs |
| P14 | Agent 不可自授权 | Agent 请求修改策略不产生新 policyDigest；Agent 请求未授予的 scope/技能/连接器返回拒绝 |
| P15 | 向后兼容 | 不配置 ExecutionPolicy 的旧根行为不变；默认 Port 实现可通过全部用例 |
| P16 | 拒绝重复 JSON member | ExecutionPolicy 的 JCS 解析遇重复 member name 一律拒绝（ADR0017 §11） |

P1-P3 验证冻结与摘要；P4-P9 验证各维度强制；P10-P13 验证凭据非持久化；P14 验证自授权不变量；P15-P16 验证兼容与格式。

## 与现有 Port 契约的接缝

本 ADR 不修改 extension-contracts.md 中任何 Port 操作的闭集定义。四个 Port 是已有操作输入位的**正式化**：

| extension-contracts.md 已有输入位 | 本 ADR 正式化为 | 原语义 |
| --- | --- | --- |
| Environment.prepare "批准目录/资源范围" | ExecutionPolicy.fsPaths + networkDomains | 已批准范围 |
| Environment.start "已批准权限/凭据引用" | CredentialVaultPort.resolve(credentialRefs) | 已批准凭据引用 |
| Agent.begin "受限工具/权限回调" | SkillGatePort + ConnectorFencePort | 受限工具/权限 |
| Binding.configurationDigest | 纳入 policyDigest | 冻结配置摘要 |
| WorkBinding.authorizationDigest | 纳入 credentialRefs 的 scope 声明 | 授权摘要 |

Binding/WorkBinding/Fault 的闭集语义不变。Environment.prepare/start、Agent.begin 的闭集输出不变。新 Port 只在已有输入位上增加可冻结、可摘要、可验证的值对象和验证合同。

## 非目标

- **不是技能注册表平台**：不提供技能 CRUD API、技能市场、技能发现服务。技能来源由部署者审核固定，SkillGatePort 只验证可见性与摘要。Multica 的 `agent_skill` junction 是数据库平台功能；本 ADR 只定义 Port 合同。
- **不是凭据管理平台**：不提供凭据存储、轮转、分发服务。Vault 实现是部署特定的（本地 secrets 文件、KMS 等），Port 合同只保证非持久化与 claim 时解析。
- **不是连接器市场**：不提供插件安装 API、连接器注册表。连接器 scope 词汇由部署者冻结，ConnectorFencePort 只验证授予。
- **不是 OS 级沙箱**：AccessMode × AssuranceLevel 二维模型（ADR0017）已定义隔离维度。本 ADR 定义的是权限维度（能访问什么），与隔离维度正交。
- **不是多租户 IAM**：tenantScope（ADR0110）定义租户物理边界；本 ADR 的策略在租户边界内 per-task 生效。多租户路由、配额、共享后台不在本 ADR 范围。
- **不是策略引擎/OPA 替代**：ExecutionPolicy 是冻结值对象，不是可求值的规则集。策略由 PolicyResolverPort 在计划准备时产生，不是运行时动态求值。
- **不改变 trusted-single-user 信任假设**：ADR0094 的 ambient 风险保留。本 ADR 提供的是"在 trusted-single-user 内收紧 per-task 访问"的合同，不是强 OS 隔离。
- **不引入 bypass 机制**：与 Multica 硬编码 `bypassPermissions`/`danger-full-access` 不同，本 ADR 明确禁止 Agent 自授权或旁路策略。策略只能通过新 Run（新计划批准）改变。
- **不改变发布授权链**：Publication Port 的 Core 授权、Environment 托管、intent-first + receipt + reconcile（ADR0007）不变。ConnectorFencePort 是 Publication Port 执行前的验证合同，不替代 Core 授权。
- **不改变 custody/guard 流程**：ADR0089 的 custody 证明、guard 进程管理不变。CredentialVaultPort 在 claim 阶段解析凭据，与 custody 流程正交。

## 实施路径建议

本 ADR 是设计草案，不规定实施顺序。以下仅为维护者审查时的参考切分：

1. **类型与 Port 接口定义**：`ExecutionPolicy` 类型 + 四个 Port interface + 默认实现（全 noop/passthrough），纳入 TS 迁移（ADR0108）的包结构
2. **冻结点接线**：PolicyResolverPort 接入计划批准事务；`policyDigest` 进入 `configurationDigest` 计算
3. **CredentialVaultPort 实现与非持久化验证**：claim 时解析 + release 时撤销 + Audit 只记录 ref
4. **SkillGatePort 实现与内容摘要验证**：allowlist 检查 + SHA-256 比对
5. **ConnectorFencePort 实现与 scope 闭集**：连接器操作验证
6. **一致性测试套件**：P1-P16 全部通过
7. **受限策略配置**：部署者审核并冻结的显式配置，配置身份进入 profile 摘要

每步可独立验证，不要求全部完成后才首次集成。

## 验收

- 一致性测试 P1-P16 全部通过；默认 Port 实现可通过全部用例
- 旧根/记录行为不回归：不配置 ExecutionPolicy 的根行为与当前一致
- `policyDigest` 的 JCS + SHA-256 确定性：同策略同 digest，异策略异 digest
- 凭据非持久化：grep 测试确认 handle/envKey 不出现在 Store/Depot/事件/Audit/Prompt/日志中
- Agent 自授权拒绝：Agent 请求修改策略或访问未授予资源时 fail closed
- 重复 JSON member 拒绝：JCS 解析遇重复 member name 报错
- 本 ADR 接受只冻结设计；实现验收、真实模型验收与生产部署另行记录
