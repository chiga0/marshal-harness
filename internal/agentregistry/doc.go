// Package agentregistry 承载 I186-R3 的 typed AgentProvider identity 收敛
// 切片（Planning Baseline v3 R3 步骤 1–2、Issue #191）：在收敛域内建立
// durable 的 AgentProvider registration / capability snapshot / evidence
// 账本与 matcher——封闭的 typed capability schema、生命周期封闭枚举
// （active/suspended/revoked/replaced/expired）、幂等注册（idempotencyKey +
// requestDigest）、canonical digest 绑定，以及与 capability/protocol 要求
// 的 fail closed 匹配；Agent evidence 必须绑定 ProviderType=agent，不得
// 冒充 Sandbox evidence（证据边界由 I186-R3-D 负测收口）。
//
// 字段语义对齐 schemas/provider-registration.schema.json 与
// schemas/provider-capability-snapshot.schema.json 的冻结定义；本包只在
// 收敛域内以纯确定性单测证明 identity 账本语义，不接线生产路径，legacy
// mapper 迁移与真实 Provider 替换归 I186-R5 strangler cutover。
//
// 骨架由维护者先行落地，用于锚定 I186-R3-A TaskSpec 的 deliverable
// pathGlob 父目录（plan premortem qoder-deliverable-parent 门禁要求父目录
// 在锁定 baseRef 中已存在）；实现与表驱动测试由 Marshal Task 交付。
package agentregistry
