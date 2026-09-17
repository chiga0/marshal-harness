# ADR 0111：审计锚定(export/verify)

- 状态：Accepted（2026-09-16；维护者要求企业化路线中的审计加固项；本 ADR 只新增只读证据导出与核证，不改事件链/持久格式/权限）。
- 基线：随 PR #316/#317/#318 合并列车。
- 关联：ADR0110（tenantScope 以 storeId 为值）、audit-report。

## 问题

哈希链保证"库内部一向可解释"，但监控与内控要回答的是两个问题：库的标签是什么、那一刻相较于既定锚是否被改过。"Summary digest 被写进库内"永远只是闭圈自证；锚必须能从库中带出，与库分开存放，后面任意时点可复算复核。现状没有这样的导出与核证路径。

## 决策

1. **格式 `marshal-audit-anchor/v1`**：`{format, tenantScope, storeId, generatedAt(ISO8601-UTC), heads: [{stream, sequence, digest}], headsDigest}`。canonical heads 按 stream 排序；`headsDigest = sha256 digest(encode(canonical heads))`。内容零推断，唯一来源是 Store 的只读快照（新公开 `headsList()` 方法，只读且过分列互斥队列）。
2. **CLI 原语**:`node scripts/audit-anchor.ts export <root>` 输出锚（不打扰运行，不读取语义/日志目录）;`verify <root> <anchor.json>` 重算并逐流对比，错误不自动调和——不匹配即"自锚定后有变化"的信号本身。
3. **身份保护**:verify 在有差异时报 `anchor_tenant_mismatch` 或逐流 `stream_added_since_anchor` / `head_digest_diverged` / `head_sequence_regressed` / `stream_missing_since_anchor`；自摘要损坏报 `anchor_self_digest_mismatch`。绝不报"轻微变化"、"大致等于"这种不可用结论。
4. **不负责签发**:外部存放(OSS/WORM/版本锁/公证）与签名(minisign）是部署环境义务；本文件只交付"字节可复算可比对"的锚。
5. **不动事件闭集**:actor 维度（事件 actor 字段）会触及 TaskEvent 闭集定义，另行 ADR；本 ADR 的锚不依赖它。

## 验收

- `packages/task-store/anchor.test.ts` 4 项（导出/同根一致性/篡改后共谋偏差分类/跨根拒收/自摘要与形状校验）;
- CLI export→verify 端到端 smoke；既有套件无回归。
