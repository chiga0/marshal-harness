# v1.0 Release Readiness 判定表

更新日期：2026-09-05。唯一当前 milestone 状态见 [Roadmap 当前表](roadmap-status.md#业务交付当前表)。本文列发布检查口径与证据入口，不另设平行完成状态。

## 已确认的发布事实

`v1.0.0-rc1` 已于 2026-09-01 发布；旧版本文“RC1 尚未发布”的描述已失效。annotated tag object `e99326f` 指向 sourceHead `c1407bd`；Darwin arm64 candidate SHA-256 为 `f9ed7fa59d05f5e71fef7164b8015240497e1d18e25ef1d3f8e199c1378a3774`。真实 Pi canary/finalize [33504020360](https://github.com/chiga0/marshal-harness/actions/runs/33504020360)/[33504247271](https://github.com/chiga0/marshal-harness/actions/runs/33504247271) 到独立 Decision/`ACCEPTED`；[33506656403](https://github.com/chiga0/marshal-harness/actions/runs/33506656403) 消费原 candidate 发布 prerelease，外部下载与安装同 bytes。

此事实仅关闭 ADR 0068 的 unsigned、CLI-only、Darwin arm64 local-dogfood prerelease。不是 managed/notarized/hardened/server/Linux/stable，也不是自治 Agent Team。历史 V1/V2 activation 的失败留在 Git 历史与 [当前能力的日期 checkpoint](current-status.md)，不再作为当前未发布结论。

## 正式产品发布检查表

以 ADR 0052 的技术不变量、ADR 0062 的 fixed server 入口和 [ADR 0080](adr/0080-three-plane-business-delivery-roadmap.md) 的 B1→B2→B3 产品范围共同判定：

| 检查 | 所需证据 |
| --- | --- |
| B1 完整单任务服务 | 新最终 bytes 的 fixed-server T2 真实业务任务到独立 Decision/`ACCEPTED`，取消/超时/查询/持续协调可用；T1 或 marker 不替代业务场景 |
| B2 受限团队 | approved plan、耐久物化/预算、并行任务、集成候选与独立业务验收；所有子 Run 通过不等于 Goal 完成 |
| 唯一权威与故障恢复 | 同一路径 restart、response-loss、陈旧/重复结果、绑定漂移、Worker 退出、验证中断；无双活或重复副作用，无法确认时明确阻塞 |
| B3 长期运行 | 历史规模/队列与资源测试，备份恢复和升级，待决任务与制品不丢；报告实测样本、延迟与失败，不能用 accelerated smoke 冒充长期 soak |
| macOS 与 Linux 支持面 | #212 managed signing/notarization、稳定安装与运行许可；Linux 实际 server/Agent 完整链而非仅编译；ordinary-user 保持非 hardened |
| 发布权限与产物 | 最终 sourceHead required CI、独立 Decision、current receipt/carrier、安装升级负测和受保护 same-bytes stable 发布；不得 tag 后偷偷换 bytes |

T1 已有 [33851302323](https://github.com/chiga0/marshal-harness/actions/runs/33851302323) 的真实 Pi/restart/丢响应证据；PR #254 的 `main@0c6d9cd` 接入 T2 API，required CI [33882237317](https://github.com/chiga0/marshal-harness/actions/runs/33882237317) 成功。两者不证明完整 T2、B2 或 B3 已通过。具体未关闭项目仅在 Roadmap 当前表维护。

## 最短剩余路径

B1 的 launcher production cutover与真实 server业务闭环 → B2 有限团队集成交付 → B3 同路径故障/长期运行和正式支持。签名及 Linux 外部依赖可以提前准备；不得把通用 M13/HA/Provider 矩阵重新列为前置。

任一步不能由“ADR 已接受”“实现有测试”“重复 CI 某次绿了”推导为生产完成。特别是 `production-owner-not-current` 的失败现场根因尚未闭合，不把 fsync 假设写成已证实修复。

## Mac 本地质量门禁边界

企业终端策略可能按新 Mach-O/CDHash 拦截 Go test 二进制。固定路径复制不同 bytes 仍是不同身份；不得反复要求人工批准、移除安全属性或绕过安全软件。

本机执行解释器测试、gofmt、静态分析、compile-only、Schema/diff/secret/mergeability，以及被允许的 fixed Marshal 观察；不执行匿名/临时 Go helper 或 compile-only 产物。Go unit/race 执行证据由相同 sourceHead 的 macOS/Linux CI 补齐，不能用 compile-only 代替。参考业务 oracle 不提供恶意代码隔离或权威签发。

## RC1 操作状态

现有 RC1 可按 [安装说明](install-and-upgrade.md) 显式预览；latest/stable 不能自动选 prerelease。正式发布仍需新的受保护 candidate 与完整证据；不得复用旧 receipt 授权新 bytes，也不得把重新签发 activation 称为原证据重放。
