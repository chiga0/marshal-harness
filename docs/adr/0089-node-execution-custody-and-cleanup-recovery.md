# ADR 0089：Node 执行托管与跨代清理收口

- 状态：Accepted（2026-09-08；独立审查无阻塞意见后，维护者按用户持续实施授权接纳；不是用户逐项签署，不表示运行时已启用）。
- 解决问题：`B2-NODE-CRASH-SETTLEMENT`。真实服务进程崩溃后，SQLite 保留原执行义务，但当前服务丢失原执行句柄，无法证明并结清占用。
- 适用范围：ADR 0088 的 Node-only、单节点、可信本地任务；不迁移旧 Go 根，不授予 hardened、Publisher 或远端作业恢复权限。

## 1. 决策与最短实现

沿现有 Application、SQLite、Supervisor 与 AgentAdapter 增加内部 `ExecutionCustody` 接缝。每次执行由固定 Node 模块启动的窄职责托管进程持有原 guard 的 `ChildProcess`；它在 HTTP 服务进程之外存活。不是新业务服务、监督 Agent、定时 watchdog 或通用执行平台。

`原 SQLite reservation → custody 准备（不启动 Agent）→ 同库绑定身份/许可 → 原 Provider 执行 → 独立清理观察 → 当前 owner 同库结清`

ACP/Pi 客户端、权限回调及独立验证输出解析仍留在原服务。托管进程只转发有界 stdin/stdout、执行控制及观察，保留背压、绝对 deadline、stderr 丢弃/计数和原输出限制。协议函数、闭包、进程句柄或 WeakMap receipt 不能序列化冒充接线。三条入口 `launchAcp`、`launchProtocol`、`launchCommand` 共用该执行机制；Verifier 也必须覆盖，不能只修作者崩溃。

首批实现闭环服务单进程崩溃后的失败/取消收口及恢复接单。不恢复旧 Agent 会话、重放 prompt 或接纳旧业务输出。B2/B3 的全部退出条件不因此缩小；托管进程/宿主共同故障的可处置路径仍必须在正式支持矩阵与实测中交代。

## 2. 信任及证据来源

托管进程不访问 Task SQLite 写通道，不提供 Decision、重试或预算裁决，不拥有 Publisher 凭据。SQLite 仍是唯一业务权威；清理观察只是待 Core 接纳的外部证据。

启动前冻结的绑定至少包括 `storeId`、原 `generation`、`taskId`、`workerId`、`commandId`、`reservationDigest`、`inputDigest`、`planDigest`、原绝对 `deadline`、`custodyId`、执行作用域及 profile 摘要。托管进程在原私有创建 IPC 中生成一次执行专用签名密钥；公钥与实例身份经同库提交后才发启动许可，私钥只留原托管进程内存，不进入 Worker 环境、参数、目录、日志或业务账本。签名仅认证原观察者，不自动证明作用域已清理。

许可、重复查询和停止均绑定该实例与原义务。许可丢响应只查询原实例，不重新启动；没有记录、没有目录或新实例自报空闲均不能证明旧执行从未启动。准备阶段有有限等待上界，缺许可的实例不得启动 Agent；服务断连先封闭旧连接启动权，随后清理。旧队列和迟到许可不能在新 owner 出现后启动。无法证明准备/许可结果的窗口继续未决，不靠猜测释放占用。

清理完成后产生不可覆盖、有界、规范化的 `cleanup observation`，绑定上述完整身份、原执行 ID、原开始/退出观察、scope、清理结果、观察时间及签名。文件按受保护状态目录的已有安全写入模式执行原子创建和文件/目录耐久屏障；不得接受任意 HTTP 路径。新 owner 验证原库绑定的公钥和签名，不能从候选文件自取公钥构造自洽信任。即使托管进程随后退出，已耐久的原观察仍可验证；进程死在落盘前则不补签。

启用该持久语义的根必须有旧 reader 在 claim 前可拒绝的格式版本，不能只追加会被旧代码忽略的字段。首个候选使用新的 Node Store 格式标识/空根；现有 v1 根保留原读写和未决规则，不原地补公钥、许可或清理证明。受控升级另须证明终态历史保持、无在途义务和旧 writer 拒绝后才能支持，不借本修复清空历史或自动迁移。

观察保留到原 Store 接纳和 ACK；ACK 丢失可精确重放。清理文件不是第二任务账本，不从文件较新时间选择真值。收据数、容量和等待有界；无法保存时保留未决并报告，不静默丢弃未结义务或无限创建托管进程。删除已接纳观察属后续安全 GC，不能删除失败现场。

## 3. 原执行组清理：补实证，不升级隔离等级

沿用原 guard 保持活跃的继承进程组及其整组停止；不按磁盘 PID 重放停止信号。现有 `cleaning` 消息先于最终整组 SIGKILL，单凭它与 leader 的 SIGKILL 退出会误判“leader 提前单死、后代仍活”。

因此原 nonce/私有 IPC、原 `ChildProcess` 退出和无清理错误仍是必要条件，另在既有清理总预算内对**原句柄所绑定的进程组**做只读 signal `0` 检查：仅 `ESRCH` 允许证明该继承组已空；存在、`EPERM`、其他错误或到期全部未决。只读检查不能补发停止信号、接纳回放裸 PID 或延长业务 deadline。组号重用只产生保守拒绝，不授权停止新同号组。

该观察只覆盖 `inherited-process-group`。`setsid`、`setpgid`、工具自行 detached 或远端 job 不在覆盖范围，不把普通同 UID 进程包装成恶意代码沙箱。Provider 已存在的 `scopeUnknown`/`cleanup:null` 否决不能被新 Runtime 收据覆盖。恢复 eligibility 必须在许可前绑定可证明覆盖的执行 profile；会产生额外作用域义务的操作，必须先耐久登记对应义务再批准，不能靠服务内易失布尔量记住。没有覆盖证明的 Provider/profile 保持未决，不自动推广全部 Qwen/Pi 配置。

## 4. 唯一跨代例外：只清理，不接受旧结果

增加内部 `reconcileCleanup`，由持有当前 SQLite owner 的服务恢复循环调用，不新增公开“提交清理收据/改状态”API。原 `ticket`、`started`、`finish` 的 generation 检查不放宽。

同一短事务必须：

1. 重验当前 owner、原不可变 reservation/许可、执行及 profile 绑定、独立观察的完整签名/摘要与覆盖范围，拒绝串 Task、替换、不适用证据及未知额外作用域。原业务 deadline 已过不使真实清理观察失效：允许其后结清原执行义务，但不续期、恢复执行或接纳旧业务结果。
2. 只有尚未结清的原义务才追加清理接纳事件；精确重放零追加，异内容冲突拒绝。以原义务为幂等键，不以新请求 UUID 创造第二结算。
3. 结清相应 Worker、capacity、原 outbox 和 Operation；Attempt/重试/返工累计不退款，原批准、幂等回执和绝对期限不改写。只释放被证明停止的执行占用，未决兄弟不能被一起清除。
4. 原取消意图成立时按真实剩余义务收口取消；否则标记服务中断失败。等待所有相关义务清理后才完成 Task 收口。已完成 Task、已接纳上游和原成果不改判。
5. 对由旧未决执行产生的 `intervention`，只允许该命名例外转入失败/取消；保留原 intervention 与失败事件。这不是通用终态复活，不进入 `completed`、不生成新验收 Decision、不自动重派。

提交失败保留原义务和观察；提交成功但 ACK/响应丢失只重放同一结算。查询接口不触发恢复，正常启动恢复循环在开放 ready 前完成有限扫描；无法结清的事实仍可按原受支持方式诊断，不报告 ready。

## 5. 故障边界与操作路径

服务单死但原托管进程/证据链存活是首个自动收口范围。托管进程也死亡、原清理证明未耐久、覆盖不足或 leader 丢失且组未证空，均保留 unknown，不创建替身、不删锁、不修改数据库。已经有效落盘的原签名清理观察不因随后托管进程退出而失效。

多重故障的操作路径必须输出原 Task/Worker、未决义务、缺少的观察及允许的诊断动作。操作者一句“已停止”不是证明。Linux 可另以原受管 OS scope 提供独立终止观察；Mac 的整机重启/可信 boot 身份路径须另有明确绑定与实际验证后才支持，本 ADR 不授予重启用户电脑或改变主机策略的权限。不能把 Linux/systemd/容器部署变成所有 Mac 开发的先决条件，也不能把永久手工 intervention 当作 B3 完成。

## 6. 验收与合并顺序

一个完整 producer→consumer 纵切验收，不按字段拆多轮：

- 先修原 Runtime 清理假阳性：真实 leader 单死且后代仍活必须 unknown；正常整组停止必须最终证空；probe 错误/超时、缺原绑定与重复 stop 分别验证。
- 接通许可前后服务 SIGKILL、活跃作者/Verifier SIGKILL、取消提交后清理前 SIGKILL；原 deadline/预算保留，不重放任务。
- 观察耐久后 SQL 前崩溃、SQL 提交后 ACK 丢失：恢复接单、一次容量释放、精确回执保持；新任务能真实执行，而非只断言状态字段。
- 串身份、公钥/收据替换、旧结果迟到、旧 owner 迟到许可、Provider 额外 scope 未决、托管进程同死及写盘失败必须拒绝错误接纳。
- 使用同一正式 HTTP/SQLite/Provider/Verifier/发行包，不另建演示路径。Darwin/Linux 分别验证，Node 夹具证明故障语义，真实模型团队证明实际接线，两者不能互相代替。

原四个 create/result COMMIT 故障测试保留事务原子性证据；后继新增恢复能力应更新明确的行为期望，不删除其真实故障停点。完成后同步 Roadmap、支持矩阵和运维文档，不能仅凭本 ADR 接受关闭 B2/API-STABLE/B3。

## 7. 依据与未实现状态

当前依据：`03c7b86` 的真实 service crash 用例及 `2f6c28f` 的 create/result 提交边界候选；代码接缝位于 `agent-runtime`、`task-supervisor/controller.mjs`、`task-application/execution.mjs`、Provider 与 `task-service/composition.mjs`。本 ADR 不声称这些候选已经包含 custody 或自动恢复。

[Apple kill(2)](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/kill.2.html) 与 [Linux kill(2)](https://man7.org/linux/man-pages/man2/kill.2.html) 支持负进程组 ID、signal 0 和 ESRCH/EPERM 的区分；OS 接口语义不是 Marshal 实机测试的替代。
