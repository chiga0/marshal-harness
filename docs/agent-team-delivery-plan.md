# Marshal Agent Team 业务交付计划

更新日期：2026-09-05。依据 [ADR 0080](adr/0080-three-plane-business-delivery-roadmap.md)。**当前唯一阶段状态见 [Roadmap 当前表](roadmap-status.md#业务交付当前表)**；本文保存目标、验收与操作方法，不另设一份完成状态。

## 用户承诺与终态

用户给出需求，Marshal 澄清影响行为/范围/权限的关键选择并确认方案，随后持续驱动多 Agent 执行、集成、验证和授权交付。自治是授权与预算内的自动前进，不是取消独立验证或无人负责的自动发布。默认 publication:none，可选 Draft PR；不自动 merge。

控制面管理事实与决策，执行面产生候选和独立验证观察，存储面保留状态与制品。首个实现维持一个固定 server、多个有界执行进程、现有账本和本地对象存储；不要先建存储微服务、HA 或通用 DSL。

## 三个业务 milestone

| 阶段 | 用户结果 | 必须在同一支持路径证明的退出条件 |
| --- | --- | --- |
| B1：完整单任务服务 | 交给 server 一个真实功能，得到可运行结果 | T2 真实 Agent→Collect→Verify→独立 Decision→ACCEPTED；重启/丢响应不重复 Attempt；取消/超时有明确 Outcome；验证期间状态查询有界；不靠用户逐步 continue |
| B2：受限 Agent Team | 确认方案后，两个到三个独立实现任务形成一个集成交付 | approved plan 耐久接纳与幂等物化；依赖和资源调度；每写节点独立 worktree；集成候选接受业务测试；精确候选独立 Decision；等待用户/局部 replan 不重做无关成果；重启后继续 |
| B3：长期运行与正式支持 | 在明确平台和信任范围内持续交付 | 在 B2 同一路径注入 Worker 退出、server restart、断网、重复提交、陈旧结果及中断验证；故障隔离；状态/历史规模测试；备份恢复和升级；managed signing/notarization、Linux 实机 server gate、受保护 same-bytes stable release |

B1 优先关闭当前 launcher 与 T2 真实链路阻塞。B2 的业务样例/验收可同时准备，但不得先实现脱离生产调用链的另一个 controller。B3 的安装信任外部依赖可提前准备，不把签名成功等同企业 EDR 必然允许。修改持久化/生命周期/授权边界先补窄 ADR。

## 本轮开始执行的参考场景：订单报价

不是 marker，而是一个可重复的小业务能力：输入商品单价（整数分）、数量与运费政策，输出商品小计、运费和总价。冻结规则：商品非空；单价为非负整数分；数量为正整数；布尔值不能冒充整数；小计达到 5000 分免运费，否则运费 500 分；不改写输入。

- B1：真实 Agent 在现有 T2 Task 路径实现 `quote_order.py`，提供 `quote_order(items)`；固定参考 oracle 检查正常、边界、非法输入、输入不变和 JSON 类型。
- B2：扩展成订单报价 API 与客户端。先确认共享输入/错误契约，然后分开实现服务与客户端，集成任务验证从客户端到服务的完整请求；不把两个不相交文件的提交当作团队完成。
- 独立 oracle 放在控制仓库的固定脚本，不在 Worker 可修改范围内；Oracle 本身用正确实现和典型错误实现做回归。测试素材全部为合成数据，无客户数据/真实订单。
- 参考工作区与 Marshal 业务代码隔离。当前 Task renderer 仍绑定 canonical Marshal repository，因此 B1 的文件范围隔离只用于首轮，**不冒充外部参考仓库集成已支持**；后继应用入口允许可信外部仓库后，迁移同一场景到独立小仓库。
- 保留现有 marker 作为传输诊断模式；marker 通过不能替代本业务验收。

调用现有 `scripts/fixed-server-t2-task.py` 时增加 `--scenario order-quote` 即生成业务 Task；其余 repository/base-ref/model/doctor/task-out/policy-out 参数不变。oracle 的 SHA-256 随 Task 冻结，验收执行已校验的同一份内存 bytes；oracle 变化必须重新生成/接纳 Task，不可修改已冻结 Run。生成不代表 dispatch：先闭合固定 server 的 profile/launcher 与准入，再走既有 server start/collect/verify/Decision；不可直接启动 Pi 补造这条证据。

业务 canary 复用 `.github/workflows/fixed-server-t1-canary.yml` 的 exact-head/required-CI、固定产物、Pi 与 provider 配置，显式选择 `scenario=order-quote`。原 `t1-marker` 默认路径保持。业务路径在同一 server 重启/请求重放后运行 `fixed-server-t2-drive.py`，自动 Collect→Verify→ReviewPacket，保留每阶段冻结请求和响应摘要；它只调用固定 `bin/marshal control-plane`，不启动 Worker 或直接写业务账本。只有已认证的 `attempt-still-running` 才按原 key/head/deadline 有界等待；普通 delivery-pending、身份冲突、CLI 超时/无效响应停止自动重试。该观察不证明无副作用，也不授权创建新 Attempt。

驱动成功只表示 `REVIEW_PENDING` 且业务 verification pass；不会生成 ReviewDecision、签发 ACCEPTED 或宣称整个 B1 通过。独立 reviewer 仍须检查精确候选与证据，并通过同一 fixed server 的 Decision operation 收口。驱动 deadline 到期只停止客户端，不代表业务取消；自动 wall-timeout/Outcome 仍按 ADR 0081 的合同缺口推进。新增驱动的 injected-call 测试不是实机 Pi 证据，canary 必须等待本候选 exact-head CI。

## 每轮最小记录

任务类型/难度、冻结需求/输入/候选/运行 binary 身份、开始结束时间、各阶段等待/运行时间、Attempt/rework 数、失败分类、人工介入、业务验收和 evidence refs。token 缺失记为 unavailable，不能计为零。样例测试通过仅是测试基础设施证据，不是实机 Agent 成功。

先对至少三个不同业务案例记录单 Agent 基线，再在可并行的同类案例比较团队 wall time、人工介入与最终质量；样本少时不宣称普遍加速。历史失败率不能冒充当前测量，不能剔除失败 Run 美化分母。

## 监督与止损

1. 每项工作绑定 B1/B2/B3 的具体退出条件。结构性修复只在阻断当前条件或同故障复发时插队。
2. 先做一个最小实验检验整轮最危险假设；已知非法环境/Schema/版本不启动付费 Worker。
3. Reviewer 一次返回完整问题，局部实现缺陷定向 rework；接口/授权/范围错误回到 plan。预算用尽保存 Outcome，不带错放行。
4. 对相同原因+阶段+Provider+输入族的复发先诊断，不进行第三次原样调用；transient 故障必须确认可重试且有退避、deadline 和预算。
5. 并发取依赖就绪、scope、宿主资源、Provider 限额与验证/集成容量的交集；初始最多三个实现节点，评审/集成积压时不盲目补作者。
6. 每次监督报告本轮推进的用户验收条件、关键路径、等待原因与纠偏；不是只报 PID 和 CPU。语义监督建议经 Core 接纳，不直接写账本或杀进程。

## 不做什么

不使用仓库 Marshal skill 驱动本次开发；不强制为每个文件创建 Run；不扩大 AgentProvider 矩阵；不重写整个存储；不增加通用角色/RPC/DSL；不抹掉旧 Run 和失败记录；不把同 UID 当成强沙箱。每个可交付功能闭环后更新 Roadmap，而非累计协议微切片后统一集成。
