# ADR 0110：租户边界预留(data-root 即租户根)

- 状态：Accepted（2026-09-16；维护者要求完成企业化路线图中的租户预留项；本 ADR 仅保留语义而非新结构能力，不推出任何多租户功能）。
- 基线：`80f6fed568e21b5488b35c7048048f79c42b8adb` 之上（PR #316/#317 合并列车）。
- 关联：ADR0094（可信单用户角色团队）、此前所有存储与权限决策。

## 问题

Marshal 现行为可信单用户单节点。如果租户边界不明说在哪里，将来接企业多租或逻辑租户时，要么做昂贵的全库迁移，要么把"租户等于用户"的错误抽象硬塞进来——组织债越积越深。我们需要 today-clean 地锁定：租户物理边界落在 **data root**,**不是** 绑定在每个普通记录键上；同时把这个原则的可靠支点显式地留在 Store 的可观测面，而不是将来靠测试中意外发现。

## 决策

1. **租户 = 一个 data root**。Marshal 持久化世界的一切（事实链、预算、回执、outbox、制品 Depot、Recovery、连接文件）都以 root 为边界；"一根一 owner"的既有规则自动是"一租户一权威写者"。将来扩容的方向是"前置路由按 root 定向、root 之间互不参照",**不是**"在同一个 store 里加一个 tenant 列"。HTTP/Worker/Skill 接触面因此保持将来不需要重写的状态。
2. **显式保留名为 `tenantScope` 的语义**:Store 每个 root 从创建起就持有稳定的 `storeId`（随机 UUID，创建时生成，不再变）。在 `store.info()` 的可观测面上，`tenantScope` 以 storeId 的字符串值**可见可读**——边界名话不多说，它是根生命周期内不可更改的身份。旧库天然兼容：`storeId` 来自 2026-08 的历史遗留，没有任何格式或 schema 变化。
3. **API/域模型面上严禁出现租户信息**：当前 HTTP 路径保持 workspace-less；未来如需多租户、统一后台或界面命名空间，另行 ADR；绝不把 tenant 塞到 Task/Worker/Artifact 的域模型字段里，以免业务服务视图把租户当成普通记录键。
4. **合同测试**：conformance kit 新增一项——create→info→tenantScope 与 storeId 相等；openExisting 多次重开 tenantScope 稳定且同 storeId；两个不同 root 的 tenantScope 不相等。

## 非目标

- 不实现租户路由、多租户隔离、配额或共享后台；
- 不改事件链、owner 围栏、制品 Depot、metadata schema、API path;
- 不引入"租户相当于用户"等的组织面抽象。

## 验收

- 全部既有套件不回归；新 conformance 用例通过；`install`/`upgrade`、发行与持久格式不受影响（无 schema、no metadata 变化）。
