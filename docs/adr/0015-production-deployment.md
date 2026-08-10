# ADR 0015：生产部署——常驻服务、远程访问与 Dashboard 认证

- 状态：未接受即被取代（Superseded before acceptance，2026-08-10 由 [ADR 0016](0016-durable-runtime-and-sandbox-provider.md) 取代其生产部署边界）
- 日期：2026-08-10
- 关联：ADR 0001（CLI-first/daemon 延后）、ADR 0003（分权）、ADR 0008（Observer）、[方向调研](../research/marshal-future-directions.md) §1/§2、[dashboard POC](../research/web-dashboard-poc.md)

## 取代说明

本提案从未被接受。维护者于 2026-08-10 重置长期目标后，其生产部署边界由 [ADR 0016](0016-durable-runtime-and-sandbox-provider.md) 承接：常驻形态、提交入口、远程执行与凭证边界在耐久 Runtime 架构中统一定义。本 ADR 中“只读观察先行”的路径不再构成独立前置里程碑，观察能力实现为 Runtime 事件流的只读投影。本文保留仅作决策历史，不再作为实施依据。

## 背景

Local MVP 是本地单用户 CLI：状态在 `.marshal/`，Worker 跑在本机，dashboard POC 仅 loopback 只读。
要"部署到独立机器 + Web 实时概览"达到生产可用，需要回答三件事：**常驻形态、远程传输、访问控制**。
这三者都改变信任边界，故以 ADR 提案，未接受前不实施。

## 决策（提案）

### 1. 常驻形态：只读观察服务先行，执行代理后置
- 第一阶段把 `marshal serve`（只读 dashboard）以 launchd/systemd 常驻，**只读**、loopback 或经反向代理；
- **执行**（Worker/verify/publish）仍留在受控 CLI/Skill，不暴露为网络服务；远程执行（第二阶段）需独立 ADR（见 §3）。
- 理由：观察不改变权威，风险最低，先获得生产可观测性。

### 2. Dashboard 认证/传输
- 生产不得裸奔 loopback 之外：认证（基本认证/反向代理 SSO）+ TLS 由**反向代理**承担，Marshal 本身保持只读、无会话状态；
- Dashboard 永不输出凭据/环境；多用户只读共享可接受（只读不改变权威）。
- 理由：把认证/TLS 交给成熟反向代理，避免在 Marshal 内自建会话/凭据管理（新信任面）。

### 3. 远程执行（后置，另立 ADR）
- 远程 Worker 执行需要：执行代理（只接受受控指令）、凭据分发、多用户授权、审计；信任面大，**不在本 ADR 承诺**，另行提案。
- 传输候选：SSH / herdr-remote（已 POC）/ 容器；均须保持 ADR 0003 分权与 draft-only 发布。

## 后果

- 接受后：dashboard 可生产常驻（只读+反代认证），不碰执行权威；
- 远程执行仍延后，避免一次性引入多用户/凭据分发的大信任面；
- 不改变 CLI/Skill 为唯一控制面。

## 备选

- 自建 Marshal 会话/认证：否决（新信任面，重复造轮子）；
- 直接开放执行 API：否决（破坏 ADR 0003 分权与 draft-only）。
