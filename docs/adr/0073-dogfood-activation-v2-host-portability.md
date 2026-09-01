# ADR 0073：darwin-local-dogfood activation v2 的同布局 runner 可迁移性

| 字段 | 值 |
| --- | --- |
| 状态 | 已接受（Accepted） |
| 日期 | 2026-09-01 |
| 决定者 | 维护者 |
| 接受依据 | GitHub-hosted RC1 canary 的跨 runner finalize 实证，以及对 activation、observation、binding 与既有 ADR 的逐项审计；接受只冻结本文合同，不改变 R1–R6 状态 |
| 关联 ADR | [ADR 0051](0051-darwin-local-dogfood-profile.md)、[ADR 0067](0067-darwin-ordinary-user-launch-and-attach-recovery.md)、[ADR 0068](0068-mac-first-cli-only-lifecycle-preview-rc1.md) |

## 背景

`darwin-local-dogfood` 首发路径此前只在单台开发机中完整闭合。`LocalDogfoodActivationV1` 同时把 activation 绑定到当前文件系统的 `device/inode`。这对单进程、单主机的 pathname 对象漂移检查有效，但把一次 activation 的稳定主体错误地绑定到了临时 VM 的文件系统对象编号。

GitHub-hosted RC1 canary 已提供确定性实证：`run` 与 `finalize` job 使用逐字节相同的 candidate、相同 sourceHead 和相同目录布局，但落在不同 runner VM；finalize job 重新签发相同 `activationId` 后得到不同的 activation digest 与 identity subject，Core 正确以“本地 Run 身份绑定无效”拒绝续接。相同 `activationId` 不是权威连续性，重签发也不能把 V1 证据变成跨主机证据。

RC1 需要的不是一般意义上的跨机器 bearer grant，而是更窄的能力：在 GitHub-hosted macOS runner 的**相同 canonical 目录布局**中，携带完全相同的 activation v2 canonical bytes，以同一 candidate bytes 完成 run/finalize。每台 runner 仍须独立完成 current-path object 观察和 TOCTOU 检查。

## 决定

### 1. 精确取代范围

本文仅对 `darwin-local-dogfood` 与 RC1 canary 部分取代：

1. ADR 0051 §2 中把 `expectedDevice/expectedInode` 放入 operator activation 和跨阶段 `identitySubjectDigest` 的部分；current-path fd object 的本机检查、same-UID trusted、`publication:none`、固定 canonical regular file 与全部禁止 surface 保持不变。
2. ADR 0068 §2.3 中要求 activation 本身携带 device/inode 的部分，以及 §2.4–§2.5 中“activation 绝不跨主机使用”的绝对表述；替换为本文的同布局 runner 合同。每台新 runner 仍须独立执行 bootstrap diagnosis、host viability 与 host-local observation，且不得由一台 Mac 推断另一台 Mac 的 Gatekeeper/EDR 可执行性。
3. ADR 0068 的 same-bytes、CLI-only、unsigned、non-production、显式 opt-in、release asset 与禁止 promotion 合同全部保持有效。

本文不改变 managed/release、签名/notarization、Linux stable、ResultIngress current-ledger recheck、sealed chain authorship或 Issue #212 的 stable 门禁。

### 2. `LocalDogfoodActivationV2` 完整闭集

新 activation 的 `schemaVersion` 固定为 `marshal.local-dogfood-activation.v2`，closed canonical JSON 字段为：

- `schemaVersion`、`activationId`、`issuedAt`、`validUntil`；
- `repositoryIdentity`、`canonicalRepositoryRoot`；
- `canonicalExecutablePath`、`expectedSize`、`expectedRawSHA256`；
- `expectedSourceHead`、`expectedSelfProfile=darwin-local-dogfood`；
- `scope`：仍精确绑定 `local-loopback`、`publication=none`、`ordinary-user` 与封闭 lifecycle command classes；
- `activationDigest=sha256(JCS(activationWithoutActivationDigest))`。

V2 不含 `expectedDevice` 与 `expectedInode`。activation 是 same-UID trusted 模型内的显式 opt-in 与不可变意图，不是签名、MAC、安装收据、anti-rollback、deployment current 或跨任意路径/任意主机的 bearer grant。

`now < issuedAt` 或 `now >= validUntil` 时拒绝；时间必须是 canonical UTC RFC3339 秒精度，且 `validUntil-issuedAt` 不超过既有最大 freshness。

### 3. 可迁移主体与主机本地观察分离

`identitySubjectDigest` 只覆盖以下跨同布局 runner 必须相等的字段：

- `activationDigest`；
- `repositoryIdentity`、`canonicalRepositoryRoot`；
- `canonicalExecutablePath`、`size`、`rawSHA256`；
- `sourceHead`、`selfProfile`。

它不覆盖 `processId`、`device`、`inode`、`observedAt` 或 observation digest。

每次 lifecycle mutation、Worker 启动、ResultIngress、Verification、Review 与 finalize 前仍产生新的 host-local `LocalSelfIdentityObservationV2`。其 `CurrentPathObjectV2` 必须保留并强制判定：

- canonical path、device、inode、size、raw SHA-256；
- descriptor 打开前后 `fstat` 等价与完整有界读取；
- pathname 在读取后仍命名同一 device/inode 对象；
- `pathRechecked=true` 与固定 observation kind。

device/inode 只是不进入**跨 runner 稳定主体**；它们继续是每次本机 observation 的强制判定字段，不得降为自由文本或可选诊断备注。

### 4. V2 lineage 与迁移

以下新对象必须同步使用 V2 Schema，并且 fresh producer 只能产生 V2：

- `LocalSelfIdentityObservationV2`；
- `LocalSelfIdentityBindingV2`；
- `LocalDogfoodEnvironmentBindingV2`；
- Verification identity binding V2；
- Review identity binding V2。

这些对象继续沿用既有字段形状，但 schemaVersion 和 digest 均按 V2 重算。任何 V1/V2 混合、未知版本、字段缺失、optional-field laundering 或使用 V1 物化 fresh Evidence/Review/Outcome 都 fail closed。

既有 V1 bytes 只允许保留为不可变历史档案；当前 V2 producer/decoder 不接纳它们，不得用于新的 canary/finalize，也不得原地改写。已经到达 `REVIEW_PENDING` 的 V1 GitHub canary 必须归档；V2 启用后从 run phase 重新产生完整 activation→dispatch→ingress→verification→review→decision lineage，不能只重跑 finalize。

### 5. RC1 workflow 合同

run job 生成一次 activation v2 canonical bytes，并将其与 candidate 和 authority evidence 一起作为不可变 artifact 传递。finalize job 必须：

1. 验证 artifact identity、candidate bytes、sourceHead 与相同 canonical 目录布局；
2. 把逐字节相同的 candidate 安装回 activation 绑定的 canonical executable path；
3. 原样消费 run job 的 activation v2，禁止按相同 `activationId` 重签发、替换或延长有效期；
4. 在本 runner 产生新的 host-local observation，再按相同 activation digest 与 portable identity subject 完成 current-ledger recheck。

若绝对 canonical repository root 或 executable path 不同，V2 直接拒绝。本 ADR 因此只声明 **same-layout ephemeral runner portability**，不声明一般安装路径迁移。需要任意布局迁移时，必须另行定义 logical repository/artifact identity 与显式 Core rebind transition，不能继续扩张本文合同。

### 6. 负向与恢复矩阵

至少覆盖：

1. activation scope、repositoryIdentity、root/path、size/hash、sourceHead/profile、ID、时间或 digest 任一漂移；
2. V1 activation/observation/binding 与 V2 lineage 任意混用；
3. 同 `activationId` 但不同 canonical activation bytes/digest；
4. 相同 bytes 但 runner 目录布局不同；
5. 同一 runner 中 pathname rename/replace/ABA、symlink、device/inode 漂移、读取中增长或截断；
6. run artifact activation 被重签发、改写、延长或替换；
7. V1 `REVIEW_PENDING` 证据直接进入 V2 finalize；
8. publication、credentialed effect、remote/non-loopback、managed/hardened 或 stable authority 请求。

所有拒绝必须发生在新的 Attempt、Worker、ReviewDecision 或外部副作用之前，并保留不含 secret/path payload 的 typed reason。

## 后果

正面结果是：同一 immutable candidate 与 activation 可以在 GitHub-hosted 相同布局 runner 间延续，而每台 runner 仍独立验证当前 pathname 对象；不会再把临时文件系统编号当作跨机器 artifact 身份。

限制是：该合同仍然依赖相同绝对目录布局和 same-UID trusted threat model，不提供签名真实性、一般跨主机安装迁移或 hostile same-UID 防护。RC1 的 non-production/unsigned 声明不变。

## 实施顺序

1. 同步升级 activation、observation、attempt/applicability/verification/review binding Schema 与 hostile fixtures；
2. fresh producer/decoder 切换为 V2，V1 仅历史保留且不得进入 fresh path；
3. 删除 RC1 finalize 的 activation 重签发 workaround，改为传递原始 V2 bytes；
4. 从新的 run phase 重跑 GitHub-hosted same-bytes canary；
5. exact-head CI、Schema/example/diff/secret scan 全绿且 V2 canary `ACCEPTED` 后，才更新 RC1 readiness 或发布 prerelease。
