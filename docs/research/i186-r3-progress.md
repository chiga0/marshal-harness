# I186-R3 实时进度与直接交付选择

更新时间：2026-08-27

## 当前结论

`I186-R3` 保持 `IMPLEMENTING`，不能标记为 `DONE`。R3-A/B/C 已完成；本轮在锁定基线 `a46211a80b3a04a8f27806ae7ded677a94f2c261` 上建立 `feat/i186-r3-direct`，直接重做 R3-D，不接纳历史 `REJECTED` successor 的提交，只选择性复用其测试思想。

为优先完成 v1.0，当前交付策略调整为：单 Lead 负责权威基线、实现、自审、合并与发布；多个 Sub-Agent 只承担互斥只读审计或独立代码切片；不再使用 Marshal skill 的 admission、ReviewDecision 和多轮 rework 流程。仍保留 Git 锁定基线、可复现测试、真实 diff 自审，以及改变信任边界/持久化/生命周期/发布权限前必须有 ADR 的硬约束。

## 已完成的代码 checkpoint

### R3-D evidence boundary

`internal/revokedrain/binding.go` 已实现：

- 外部输入仅为 opaque `BindingMaterialRef`，不允许调用者同时提交预期 principal、registration、snapshot、kind 或 label；
- 每次判定按 `material → registration → current snapshot → trusted target` 重新读取 authority；
- material、registration、snapshot 和 trusted target 的 Port、security domain、protocol family、audience、principal、kind、label 与引用必须完整闭合；
- resolver 使用值语义，避免返回内部指针形成校验后的 mutation/TOCTOU；
- Agent/Sandbox 两个 Port 与 evidence/credential/token 三类 material 的六种跨 Port 重放全部拒绝；
- 缺失、歧义、不可用、inactive、ref drift 和 tuple drift 均 fail closed。

### R3-D revoke / bounded drain

`internal/revokedrain/drain.go` 已实现纯确定性决策器：

- security-critical revoke 立即 `stop-new + cancel + fence + generation-bump + kill`，没有 drain 窗口；
- planned incompatible upgrade 只允许新的 registration/snapshot，并要求旧 lease、旧 registration、旧 snapshot、新 registration、新 snapshot 五个引用互不别名；
- 每次决策先重新读取并比对完整 pinned old lease，ABA 使 decider 永久 fail closed；
- deadline 前只 `stop-new`，等于或超过 deadline 后执行 cancel/fence/generation bump；
- generation overflow、无效 reason、无效 successor 和 authority lookup failure 均拒绝。

## 当前验证

已通过：

```text
go test ./internal/revokedrain
```

当前 checkpoint 尚缺 drain 的完整表驱动/race 测试，因此只作为不改变 production wiring 的在途核心合入权威 `main`，不宣称 R3-D 完成；后继提交必须补齐测试与接线后才能关闭对应 Exit Gate。

## 剩余工作

1. 补齐 drain 的 trigger/reason、deadline、alias、ABA、concurrency 与 defensive-copy 测试，并运行 package race。
2. 新增最小 ADR：`Execution Observation Authority 与单调 Failure Attribution`，冻结：
   - Provider location 只能是 claim；production 只接受故障域外 `AuthorityLocationFact`；
   - Provider failure 只能诊断或收紧；只有故障域外 resource observation 能允许 R4 按冻结 policy 做有界 retry/budget 例外；
   - 不修改 ADR 0044 已冻结的 DRC 字段，使用 ledger fact/ref 与 ResultIngress current recheck 绑定。
3. 实现 `internal/executionobservation` 的 location/resource/failure 合同、分类器与负向矩阵。
4. 将 R3-D/E/F 接入 production ResultIngress/authority ledger；完成单侧 revoke/replace、位置伪造、infra-failure 伪造和 stale generation 的端到端负测。
5. 完成 R3 自审、定向测试/race、仓库质量门禁后更新本文件与 Roadmap 为 `DONE`，再进入 R4。

## 未改变的事实

- M0–M9 历史 `PASSED` 结论保留。
- M10–M13 不恢复旧顺序；其 recovery/fencing/conformance 与 release 工具链分别重排进 R4/R6。
- 本地 ordinary-user dogfood 证据不能升级为 managed/release production assurance。
