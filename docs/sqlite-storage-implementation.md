# SQLite 存储后端：当前实现与接线边界

更新：2026-09-08。依据 ADR 0085；本页描述 `internal/sqlitestore`，当前为 **COMPONENT**，没有开启生产 SQLite profile、迁移旧 `.marshal` 或关闭 B2。

## 已实现

- `Create/Open` 区分新库与已有库；损坏、不兼容、错误 namespace、缺少文件或已有 writer 时拒绝，不自动重置。
- 同一个 SQLite `Update` 事务可跨 RB1、Run、Dispatch、Provider 四类原始记录，同时写投影、幂等回执与 command outbox。原字节及各账本原摘要算法保留；业务 reducer/producer 准入仍属于原 Core。
- `View` 提供一致读取；有限事务、分页和数据大小，CAS 冲突不覆盖，回调忽略错误也不能提交前缀。
- 每次重新打开必须重新 `ClaimOwner`，持久 generation 与当前实例授权共同检查；续期不能替代首次取得 owner。数据库 owner 不是 Worker 进程或执行目录所有权证明。
- 回执绑定 scope/operation/key/request；精确重放不生成新命令。outbox 保存 pending/unknown/observed，不负责发送，不把 unknown 重置成 pending。
- 私有本地目录持锁，检查路径/文件身份与权限。Create/Open 返回前均按子目录→父目录同步；同步失败保留不确定状态，后续 Open 也必须重新完成同步，不能绕过失败。

不承诺同 UID 恶意代码隔离、完整掉电硬件保证、业务 authority 自动成立或 Worker 自动恢复。

## 独立验证

最终修复快照的增量源码 tar（`go.mod/go.sum/internal/sqlitestore`）SHA-256：`27db4f71076c7a33d111059a2d78edd9b6f15344959bb45412490271b94d4177`，基线 `97c6a9b873340108fb192cd19348f826ac8bd1b4`。

维护者独立编译 Linux amd64、Go 1.26.6、CGO=0 测试包，SHA-256 `9b8148d24647da0049d9a202871c3b46b36e014634894b4a1aa1fc8f612545db`。本地与香港 ECS 接收摘要一致；专用 `marshal-runner` uid 1000、1 CPU、768 MiB、NoNewPrivileges、120 秒测试期限下，22 个顶层测试组通过，作业 2.136 秒、exit 0。一个 helper 顶层跳过，但提交前/后退出与第二 writer 子用例确实执行它。

覆盖四账本原子提交、七处事务回滚、进程中断、CAS、实例/代际、原记录格式、outbox 重开、读取/写入并发、缺文件/损坏及目录同步失败→Open→Claim→重启。进程中断和同步注入测试不是物理断电实验；此批非 race，也不替代 Darwin、最终提交 CI 或真实业务验收。模块校验、架构检查、vet/staticcheck、diff/secret 扫描分别通过。

独立审查的一个 P1（父目录同步）已在同一候选修复，并覆盖失败后重新打开的路径；同 reviewer 复核无剩余 P0/P1。旧快照 19 组通过仅作历史，不挪给修复后的快照。

## 下一步：同一生产事务，而非只替换连接对象

1. 将现有 RB1/Run/Dispatch/Provider concrete store 接缝接入该后端；原 producer/current-ledger/reducer 检查必须在同一 Session 事务边界内生效。
2. 实际提交中原子写入原记录、投影、预算/回执及 outbox；外部执行在事务外，重放不重复启动或消费预算。
3. 接通新数据根简启动、同版本 owner/recovery，再通过真实 Task HTTP 创建/问答/批准/取消与执行链验证。旧 file-backed 根继续明确隔离，不双写、不隐式导入。
4. 补同路径 crash/迟到结果/重复命令/目录归属及业务交付验收后，才能升级成熟度。SQLite 后端单测通过不意味着 B2/API-STABLE 已完成。
