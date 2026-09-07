# ADR 0082：fixed server 实机验证的同宿主独立评审载体

- 状态：已接受（Accepted，2026-09-05，维护者代理在持续实施授权内采纳；不代表实机通过）。
- 范围：ADR 0080 B1 及 ADR 0083 首批两个 implement 节点的 canonical 仓库 hosted macOS canary；不改变产品 Public API、Run Schema 或发布权限。

## 问题与决策

现有 canary 生成 ReviewPacket 后关闭 server。把归档复制到另一个 runner 不能证明原宿主 current-ledger 接纳；自动生成 accept 又违反 Worker 不自证。因此在同一个 job、原 canary shell 和 server 存活期间，增加明确 opt-in 的外部评审输入载体。

协调器仅启动现有 canary 驱动，不直接启动 Worker。驱动关闭 review-only 归档后发布 ready 标记；协调器通过固定依赖的 Actions Artifact 客户端上传精确输入。独立维护者读取真实 diff、Task、VerificationReport 和摘要后，向 canonical Issue #186 发布一次 ReviewDecision 原文。评论必须来自已验证 canonical 仓库 owner、列名于 MAINTAINERS，且精确绑定 workflow run ID、sourceHead、ReviewPacket digest；旧评论、编辑评论、重复匹配评论和非 owner 评论均不能授权递交。

评论只是现有 ReviewDecision 的传输，不能修改 Policy、充当 waiver 或宣布 ACCEPTED。协调器只读取 GitHub，不持有发布/评论写权限；传给 canary 的环境使用 allowlist，移除 Actions/GitHub credential。普通同 UID runner 不提供恶意代码隔离，不宣称 hardened。

## 接纳与失败边界

- 原始评论 bytes 有界，写入 Run 的 canary evidence 目录，不写 authority ledger；Decision 文件完成后原子发布。文件解析拒绝重复 member、symlink、hardlink、特殊文件、截断和漂移。
- 固定 CLI 在同一 server 上先重新 Inspect 精确 Run/Attempt/sequence/head，再通过既有 DecisionPort 接纳。Core 独占 JCS、当前 Evidence、ReviewDecision 与 Outcome 的校验和持久化。
- 不构造默认 accept；只有独立 Decision、成功 receipt、终态 Outcome 和最终 Inspect 全部一致才报告 ACCEPTED。reject/rework 不启动新 Attempt。
- review 等待最多 20 分钟；协调器和网络请求有独立上限。超时、上传/鉴权失败、冲突或不确定 mutation 均停止并保留证据，不自动重试业务 mutation、不把客户端失败冒充终态 Outcome。
- 协调器只终止自己持有的 canary child；原驱动负责其拥有的 server。禁止 broad kill、跨 runner 恢复 authority 或临时 Mach-O helper。

## 退出与后继

### 2026-09-07：团队同宿主评审扩展

首轮团队实机暴露出「两个节点到 REVIEW_PENDING 后 runner 结束，独立评审只能离线发现缺陷，不能原地提交 Decision」的交付断点。本次在既有 opt-in 载体中增加封闭 `order-quote-team` 场景：只接收原批准中 `service`、`client` 两个 Run，不通用化 Issue 审批协议，不创建 integration、重试或 successor。

驱动先为两个节点完成 Collect/Verify/ReviewPacket 和各自关闭的 review-only 归档，再发布一个团队 ready 标记。协调器固定读取两个子目录；校验 sourceHead、两个不同的确定性 RunID、同一 binarySHA256，每个评论仍精确绑定 workflow/sourceHead/packet digest，并由原 fixed server 重查当前 Run/Attempt/sequence/head。两个 Decision 可独立到达；统一最多 20 分钟等待预算，不按节点延长等待。一个节点拒绝不能阻止另一个已就绪节点接收独立 Decision；未知 mutation/损坏证据仍停止，绝不猜测成功。

验证失败可以进入独立 reject/rework 评审，但不能被驱动当成 accept；Core 的原始门禁不变。驱动记录每个真实 Decision receipt/Outcome 和最终 Inspect；即使两个 implement 都 ACCEPTED，团队 summary 仍为 `accepted:false`、`integrationExecuted:false`，不能代替集成 GoalOutcome。拒绝只记录终态；有预算的 rework 记录 Core 的 `REWORK_REQUESTED`（非 `RETRY_PENDING`），不伪造终态 Outcome，不启动 Attempt；本团队零 rework 预算下仍由 Core 返回 REJECTED。该扩展没有实现局部 replan、成果接纳后的集成复用或 server 自主 Collect/Verify，关闭这些后继前不允许再用全团队重跑掩盖失败成本。

静态/单元测试不关闭 B1。必须实际观察真实 Pi 业务、独立评审原文、同 server Decision receipt 和 ACCEPTED Outcome。载体是测试设施，不是长期生产审批数据库；正式交互仍通过产品 API。后续通用团队审批不得直接沿用 Issue 评论作为业务权威。
