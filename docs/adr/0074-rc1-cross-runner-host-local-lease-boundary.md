# ADR 0074：RC1 跨 runner authority 传递的宿主本地 lease 边界

| 字段 | 值 |
| --- | --- |
| 状态 | 已接受（Accepted） |
| 日期 | 2026-09-01 |
| 决定者 | 维护者 |
| 接受依据 | exact-head RC1 canary `33495684252` 已真实到达 `REVIEW_PENDING`；finalize `33495913805` 在恢复后因 `lease.lock` inode 改变被 descriptor-bound owner gate 确定性拒绝，且未写入 Decision 或产生发布副作用 |
| 关联 ADR | [ADR 0035](0035-supervisor-lease-owner-v2-and-orphan-recovery.md)、[ADR 0068](0068-mac-first-cli-only-lifecycle-preview-rc1.md)、[ADR 0073](0073-dogfood-activation-v2-host-portability.md) |

## 背景

ADR 0073 允许 RC1 canary 在相同 canonical 布局的 GitHub-hosted macOS runner 间传递原始 activation、candidate 与 Run authority evidence，但没有区分 Run 的耐久 authority 与宿主本地互斥对象。

真实 finalize 恢复了 run phase 的 `lease.lock` 与 `lease.lock.owner`。owner record 绑定原 runner 上被打开 lock descriptor 的 device/inode；tar 在新 runner 创建了新的 inode。Core 随后以 `existing lease owner does not bind the opened lock descriptor` 拒绝获取 Run lease。这是正确的 fail-closed 行为，也证明这两个文件不能作为跨宿主 authority 携带。

## 决定

### 1. 精确取代范围

本文仅补充并收窄 ADR 0073 §5 的“authority evidence 一起作为不可变 artifact 传递”：`lease.lock` 与 `lease.lock.owner` 不属于可迁移集合。ADR 0073 的 activation V2、same-layout、same-bytes、current-path observation 与全部禁止 surface 保持不变。

本文不改变 runstore 的 lease 校验、owner record 格式、Run lifecycle、ReviewDecision 接纳、stable release 或一般跨主机 authority migration 合同。

### 2. 可迁移与宿主本地对象

RC1 run/finalize artifact 可以携带 append-only journal、snapshot、TaskSpec、Policy、Attempt/ResultIngress/Verification/Review evidence、原始 activation 与 candidate bytes。

以下两个精确路径族是宿主本地对象，禁止进入恢复后的 authority：

- `repository/.marshal/runs/<run-id>/lease.lock`；
- `repository/.marshal/runs/<run-id>/lease.lock.owner`。

`lease.lock` 是当前宿主的 descriptor/flock 锚点；`lease.lock.owner` 是该锚点的 owner probe 与 liveness 记录。二者的 device/inode/PID 不能由一个 runner 向另一个 runner 授权，也不能通过复制保持语义。

### 3. 传递与重建顺序

1. run phase 必须在固定 CLI 已释放 Run lease 且 workflow 成功完成后，才允许上传 evidence artifact；
2. pack 与 restore 两端都按上述两个精确路径族排除，不能扩大为删除整个 Run authority；
3. restore 后必须在首次 lifecycle mutation 前断言两个对象均不存在；只存在其中一个也拒绝；
4. workflow 不得手工创建、改写或伪造它们，也不得修改 journal、snapshot 或 Review evidence；
5. 首次 mutation 只能经 exact candidate 的固定 `bin/marshal` 进入既有 `Store.Acquire`：由 Core 创建当前宿主的 `0600 lease.lock`，在持有 flock 的 descriptor 上产生并验证新的 current-host owner record；
6. 新 lease 只授权当前进程执行本次既有 lifecycle transition，不重写历史 authority，也不继承原 runner PID/inode 身份。

### 4. 绑定与失败语义

artifact ID/digest、source workflow run、sourceHead、canary Run ID、candidate digest、activation digest、ReviewPacket 与 Decision digest 的既有绑定全部保留。排除 lease pair 不允许跳过任何 current-ledger recheck。

若恢复后的 Run authority 含任一 lease 文件、路径类型异常、符号链接、Run ID 不匹配、source run 未成功终止、candidate/sourceHead 漂移或首次 `Store.Acquire` 无法建立 current-host owner，finalize 必须在 Decision 写入和 carrier 生成前 fail closed。

### 5. 适用边界

该决策只服务于 ADR 0068 定义的 unsigned Darwin arm64 CLI-only RC1 canary，在 GitHub-hosted 相同 canonical 布局的临时 runner 间续接同一 evidence lineage。它不是通用 authority export/import、远程 Control Plane 迁移、lease takeover、恶意同 UID 防护或 stable production 恢复协议。

一般跨主机 authority 迁移仍需要显式 export manifest、接收端 transaction、fencing/high-water 与恢复协议，不能扩张本文的 canary 例外。

## 负向矩阵

至少覆盖：

1. pack 或 restore 任一端遗漏对 `lease.lock`/`lease.lock.owner` 的排除；
2. 恢复后出现两个文件之一、符号链接或非 regular current-host lock；
3. 复制/伪造原 runner owner 的 device、inode、PID 或 digest；
4. source workflow 未成功、artifact/run ID/head 不一致或 evidence 过期；
5. 绕过固定 CLI 手工创建 owner、编辑 journal/snapshot 或直接写入 Decision；
6. 新 runner 无法获得 flock、owner record 与打开 descriptor 不一致或并发 owner 存活；
7. 把该例外用于不同 canonical 布局、managed/hardened、Linux stable 或任何发布副作用。

## 后果

正面结果是已完成的真实 Pi canary evidence 可以在不重复 Worker 运行的前提下安全进入独立 Decision；descriptor-bound owner gate 保留，且新的 runner 必须重新建立自己的互斥身份。

限制是 RC1 workflow 仍不是一般耐久 authority 迁移机制；GitHub artifact 只承担同布局、同 sourceHead、同 candidate 的测试证据传递。

## 实施与验收

1. workflow 在 pack/restore 两端精确排除 lease pair，并在 restore 后断言二者不存在；
2. contract test 机械约束两个排除点与两个 absence assertion；
3. 使用既有 exact-head Pi evidence 重跑 finalize，必须由固定 CLI 从 `REVIEW_PENDING` 到 `ACCEPTED`；
4. receipt/carrier 校验通过，且失败尝试没有 Decision、carrier 或发布副作用；
5. 本 ADR 与实现合并不单独升级 R1–R6，也不构成 RC1 已发布声明。
