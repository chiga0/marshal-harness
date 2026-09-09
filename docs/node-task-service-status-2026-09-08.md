# Node Task 服务实施检查点（2026-09-08）

## 当前结论

### 2026-09-09 16:42 CST：单 Worker 取消已本地合入，原 Pi 实机一次通过

Core source=`070086e991b65b81f1765ec78ec1a9772fc01102`，整合/实机 source=`190171e0be204b680bdfadbaac5fdac48f03c6ad`，localMergeSha=`25ff8315cbf63e907aaaa47f869d76d592c89acc`；记录时尚待正常推送，`pendingRemoteSync=true`。ADR0093 新空根 `layout:6`、`task-worker-cancellation/v1` 明确启用后，原 HTTP `worker.cancel` 进入同一个 Application/SQLite/Execution 收口；旧格式仍拒绝该能力，不迁移旧根或追认未绑定执行。操作绑定原 Worker 与 Task revision，不扩大为用户提交 PID、任意 stop 或自动重试。

唯一 reviewer 在 `acbcfee` 发现一个 P1：合法28/48节点计划对每个依赖后继重读全体Worker，耗尽原 Store 读取预算，取消事务回滚。一次聚合修复只把 Worker 集合读取移到同事务循环外，保留当前周期/归属检查和512记录限额。新增原SQLite回归修前两例均 `application_unavailable`，修后作者完整组件14/14；同一 reviewer 对冻结070086独立 **43/43 PASS、3.364726458秒**，无剩余P0/P1。此前作者330/331的唯一CAS时序断言失败、随后repair7/7，以及 reviewer 执行环境中的CLI启动失败均保留；不重写成首次全绿或全仓331/331。

当前整合源固定 Node24.15.0：API/客户端/发行包/实机驱动组合 **56/56 PASS、57.526552083秒**，原CLI repair/单Worker取消组合 **15/15 PASS、89.729782208秒**，均零失败/取消/跳过。后者覆盖原repair保留兄弟、Git未绑定unknown、stop/settle四个COMMIT前后原服务SIGKILL与随后新团队交付。显式完整v6 HTTP/SQLite/custody/Pi-bridge无模型夹具也通过，不用模拟worker.cancel或降级task.cancel。独立标准Ajv Draft2020-12校验58 Schema/35示例/25操作通过，OpenAPI SHA-256=`def9cfc42e41fd6c821ce90970f556332aff2a9e7ed08d8f562be37c4ef7ab1d`；语法、diff、secret及merge-tree检查通过。本机证据不替代此新源的Linux或完整CI。

真实 Pi0.84.4、原SDK/登录、固定Node与custody配置只调用一次，Task=`task-f7ea4368-abf2-4ecb-829e-bf60a0640774`，总耗时 **45.843秒**。原east收到一次精确取消并cleaned；原west继续完成，59B `west.json`摘要=`sha256:ee65dbf5390be5d13f248f8265cd2958c866fb8ade0b3bb7dba6b62b99f87dea`，原合成输入重算为2笔/550 cents。最终Task=`failed`、code=`worker_cancelled`，原取消Operation=`operation-d62ae95e-a580-4d55-9b6a-124c9971900c`为succeeded；只有planner和两作者共3次执行，零verifier/Decision/delivery，正常停服与冷开后原回执/Task/兄弟结果保持，零替身启动。west只是保留候选，**不是已独立验收的Task交付**；两作者仅45ms进程交叠，目标east的模型/工具消费未证明，不当作计算并行证据。用量未知不写0。

原证据 `/private/tmp/marshal-v6-worker-cancel.ne34oK/run/evidence.json`，SHA-256=`657a15187a42962ae8c11ed093b647aaebe4aa566ccf7dae2f8a2119fbb9316b`；Pi入口/SDK摘要仍为前述固定5406c369…/82cb4ea8…。原记录 `production=false`、`publisherSeparationProven=false` 不改，实机是正常冷开、不是本次真实模型崩溃测试。B2的单Worker控制缺口闭合，真实模型局部修正、B2-L全程Leader、声明平台部署/长期故障与受保护stable仍未完成。

独立 reviewer 随后只读复核原SQLite、stop来源事件、创建/批准/取消三个原回执、原result/manifest/blob及公开安装Pi哈希，全部匹配：generation2、outbox均observed、容量0。source190171来自原冻结源码启动记录，证据JSON本身没有源码签名字段，不能声称已自证源码身份。审计诊断初次以普通对象与协议null-prototype做deepStrictEqual出现假红，改用原canonical encode精确比较后通过；未修改源、证据或数据库。协议/HTTP比较应复用规范字节比较，避免将对象原型差异反复误判为业务缺陷。

### 2026-09-09：有限存储写失败测试已合入，不冒充磁盘满

测试 source=`d36c1b8fdfec88657aee656a4b928af9c317a3b2`，localMergeSha=`fe304bd662129553ee042eca9a79ac35eade8905`，已随main `f27784dd`正常推送。维护者独立审查及原固定Node测试 **1/1 PASS、11.9726175秒**：仅所属CLI子进程的文件大小限额触发真实 `EFBIG` 和 SQLite COMMIT `SQLITE_IOERR_WRITE`，原请求全提交或全回滚；冷开查原回执，原4次执行不重复，新4次团队执行及下载通过。Store/制品边界和原退出事实保留，未全盘填充、修改宿主安全策略或调用模型。它不证明ENOSPC、活动Worker期间I/O故障、断电或长期SLO；B3仍开放。

### 2026-09-09 15:32 CST：原协议洪泛的有界失败与随后接单验证

仅测试 source=`303dda739967e218ca6d70de8133e88f8b05bcdc`，锁定上述产品main，唯一独立review无P0/P1；固定Node24.15.0独立 **1/1 PASS、14.608秒、0失败/取消/跳过**，542次成功公开查询最大64ms。原CLI/HTTP/SQLite/custody下，受管ACP进程分别发4100个被忽略的thought更新、超过64KiB正文、超过1MiB无换行帧，按原限额失败；紧随的end_turn不成为成功，没有Verifier/交付或伪造内容拒收。原executionId与cleanup匹配后结清，未知用量不填零；原回执不变、容量归零，随后一个新团队经原独立checker下载消费通过，最终SQL integrity/foreign-key检查通过。

这是既有custody profile的有限故障组合，不改生产Core；不证明洪泛期间独立Task继续运行、任意并发取消、RSS上界、长期SLO、真实模型或磁盘满。作者初次两次预检分别误写无验收的pending状态、忽略客户端null-prototype，修正测试断言后通过；原失败私有根 `marshal-event-flood-MSptIR`/`marshal-event-flood-oJdehu` 保留，不计首次全绿或生产缺陷修复。剩余B3矩阵不因本测试关闭。

### 2026-09-09 15:30 CST：v5 主线已同步，双平台同源回归与同资产消费通过

sourceHead=`dd3d1900c41a69fef2ad6bb5f749aa3050a72b33`，localMergeSha=`1a9dc3cb672a36bc976de52ef0ef7b9efbc83dc6`；正常推送后已按远端 refs/heads/main 核对同一SHA，`pendingRemoteSync=false`。下节独立182项及审查证据对应这次整合，不是仍待合并。

该精确main的 [Node team 34323362296](https://github.com/chiga0/marshal-harness/actions/runs/34323362296)，push/attempt1，五job均success。Ubuntu共564项、563通过、0失败、1跳过（仅Darwin瞬时EPERM专项），385.331秒；macOS **564/564**、0失败/跳过，398.094秒。原 `packages/*/*.test.mjs` 组合包含0092新v5配置/许可前恢复、原问答/repair/custody/COMMIT故障，不再使用旧8f97结果代替新v5源码回归。

原producer仅一次打包出artifact **10092972643**，47生产文件/666553 bytes，manifest=`sha256:7b1524b928e0baf3183eb963382fb2e3822f1e11231a6a0175164b51b329a70c`；归档683215 bytes、GitHub digest=`sha256:5888b4b61631ea27d05c64fb48784f1d644091fde7dcefef2183b4fd3a1b9abc`。Linux x64/UID1001与Darwin arm64/UID501分别消费同一原artifact、固定Node24.15.0；两平台各2/2、8.863秒/6.877秒，layout1/2各4个原执行、4 Attempts、冷开重复0，下载摘要均为下节3055d214…。**安装消费者仍只覆盖layout1/2**；包含新v5不等于安装后的v5故障专项，后者本轮来自同源回归。原归档有限保留期，不是永久release资产。

单Worker取消的Core/API与实机验收驱动已分别在独立worktree从该基线开工，不等待CI；冻结前不计实现通过。香港替代传输、真实受限账号模型部署、Publisher分权、长期SLO及正式发行仍未完成。本段不提升B1/B2/B3整体状态。

### 2026-09-09 15:13 CST：v5 许可前恢复独立通过，原资产接纳双平台通过

ADR0092实现 source=`dd3d1900c41a69fef2ad6bb5f749aa3050a72b33`，集成候选=`5e60f283`。维护者完整审查20文件真实diff无确认P0/P1，并在冻结源码、固定Node24.15.0独立运行17文件定向组合：**182/182 PASS、217.698秒、零失败/取消/跳过**；前后工作树clean。作者另一次同源182/182、273.400秒仅作为作者证据，不替代独立验证。syntax、diff-check、secret scan与merge-tree均通过，集成的五个产品包与原冻结源逐文件无差异。

新v5覆盖reservation、binding、cancel全部六个COMMIT前后原服务SIGKILL，以及原repair负Decision/保留兄弟、同Worker问答ACK、混合已绑定/未绑定、超过100条合法progress后的签名恢复、SQL回滚及冲突反例。每个故障停点后均跑新HTTP团队、独立验收/下载及第三次open零追加；reservation-before尚无原Worker，仍通过原显式Task取消关闭旧命令，不称无条件自动续跑。旧v2六例和原问答/repair回归一同保留；旧未绑定现场继续未决，不被新格式追认。

这关闭**新根、可信staging-only文件准备**在reservation-after/binding-before窗口的安全失败/取消收口与重新接单缺口：原ticket已冻结资格、原许可三事实全不存在时才能结算，cleanup保持null，预算不退、旧任务不重派。Git/custom prepare不继承该例外；不证明所有崩溃/模型会话可继续，也不是新v5的Linux实机或正式发行。审查前发现全量Worker事件上限会阻断超过100条合法progress后的已绑定恢复，已改为原许可类型精确查询并加入真实受管进程反例；未绑定负证明仍完整有界，不能截断当作不存在。首批构造对象/嵌套事务错误的失败记录保留。

另 [Node candidate admission 34322419104](https://github.com/chiga0/marshal-harness/actions/runs/34322419104) 在 workflow head=`10c05cea704f627a05285c88aff87347cf3ea40b` 一次dispatch、attempt1完成四job：Ubuntu/macOS边界测试分别19/19（6.840秒/8.420秒），两平台均完整接纳并消费原artifact `10090976947`。消费对象仍是下节8f97ba23/原ZIP与manifest双pin，不是新v5替身；各平台两layout各4次原执行、零冷开重复启动、相同下载摘要，模型调用0。新增原接纳工具的Linux与Darwin实证，不等于香港部署/真实模型Linux/分权或stable。

下一项是已接受ADR0093的完整Worker取消纵切；不重新开启旧skill，不等本轮CI才开发。B1/B2/B3其余出口保持开放，当前worker.cancel仍501。

### 2026-09-09 15:04 CST：原 GitHub 归档完整接纳与安装消费

只读接纳工具 source=`ec2f1e096150a8183fbc2e0554c8b78ed7a5c98c`，集成=`115719b7`，唯一独立 reviewer 完整审查五文件无确认P0/P1。维护者按相同冻结源码/固定Node24.15.0运行 **19/19 PASS、20.119秒**；原审查者受限执行为18/19，两个layout服务ready前退出，原因未定，现场保留，不能改称所有环境首次通过。diff-check、merge-tree与secret scan通过。该工具只读取GitHub run/job/artifact，不创建tag/release、不修改发布权限；原旧RC1发布通道不变。

随后在独立干净源码 `8f97ba233081e00de01a2e1e054d510ea8ec36f2` 上，实际通过新工具消费下节的原run `34318148089` / attempt1 / artifact `10090976947` ZIP。先后重读canonical仓库、精确source/run/attempt、五job成功集合及artifact元数据；验证原归档摘要、封闭文件清单和包外manifest pin后，调用原 `restoreCarrier`/`verify` 安装，再使用包内原CLI/HTTP/SQLite/客户端完成两种layout团队、独立验收、下载及冷重开。不是另打包替身或仅静态解包。

Darwin arm64、UID501、Node24.15.0，**2/2 PASS、25.328秒、零失败/取消/跳过**；layout1/layout2各4次原执行，冷开重复启动均0，下载摘要仍为 `sha256:3055d2141868fed0c8abbf628e50fa959a623fd93474a928318a802efa7b6fb0`。原ZIP仍663850 bytes / `sha256:4fba5780b2a74325d3ae17bed6e401d813a36e653210da8b2b249ec3c812342f`；47生产文件/647188 bytes，manifest pin仍 `sha256:88b5e71f31c61ffaf7cb64ae518fb6d525ca97b4dc0685e7ef42e213002bcf68`。运行前后matching-source保持clean，未调用真实模型。

证据目录 `/private/tmp/marshal-candidate-admission-independent.KT5HcO/admitted`：`result.json` SHA-256=`2a11a7096cd1663feb15f77bff8b31fbec73499be0395288c3f235b54e481a73`，`consumer.tap` SHA-256=`2ad4b7005dd457c4dc88b6533d7ee3f59e712f0b972d6a9e5bf20becabb6dfad`。这新增原GitHub资产经接纳工具完成本机消费的证据，不是香港部署、真实模型Linux验收、长期SLO或正式stable发行；B1/B2/B3仍按原出口判断。下节“尚未由新工具执行”是本次之前的历史状态。

### 2026-09-09：候选原归档与 Linux 独立账号复核

在主线 `c296226a157b89582195e102c5bc08b738756bd9` 重新执行 API/客户端合同测试，**32/32 PASS、1.379秒**，无失败/取消/跳过；生产代码仍为下节 `8f97ba23`。这不是25项业务均已实现，单Worker取消仍501。

从 canonical GitHub 原下载接口取得下节 run `34318148089`、attempt1、artifact `10090976947` 的原 ZIP：663850 bytes，归档 SHA-256=`4fba5780b2a74325d3ae17bed6e401d813a36e653210da8b2b249ec3c812342f`。已核对原 run 成功、sourceHead、artifact 归属、未过期、长度与服务端归档摘要；归档保留在 `/private/tmp/marshal-original-candidate.23FT3Q/candidate.zip`，尚未由本次新准入工具解包或执行。归档摘要与下节目录 manifest pin 是不同对象，不能互换，也不是永久发行凭证。

香港 ECS 的只读 SSH 检查确认 `marshal-runner` 为 UID/GID1000、仅其本组；home 与既有安装目录均0700。该账号对 `/root` 下检查的 SSH、GitHub、Git、AWS、Pi、Qwen 路径不可读；自身常规 GitHub/Git 凭据路径及 Pi `auth.json`、Qwen 配置目录不存在。没有读取任何凭据内容。这只证明所查路径的访问边界，不证明所有发布入口隔离，也不据此断言 Provider 必定无法鉴权。独立账号使用的固定 Node24.15.0 路径仍由 root 持有0755；尚未安装本候选、调用远端模型或证明生产部署。此前 SCP 的 SIGKILL 原因仍未确认，替代 HTTPS 传输的用户选择待答，不改安全策略、不复制本机登录。

### 2026-09-09：精确同包双平台消费通过

精确 `8f97ba233081e00de01a2e1e054d510ea8ec36f2` 的 [Node team CI 34318148089](https://github.com/chiga0/marshal-harness/actions/runs/34318148089) 五项job全部成功：Ubuntu/macOS原组合回归、单次打包、两平台同artifact消费。生产者仅生成一份47文件/647188 bytes目录包，包外 pin=`sha256:88b5e71f31c61ffaf7cb64ae518fb6d525ca97b4dc0685e7ef42e213002bcf68`，原artifact ID=`10090976947`；两个消费者没有重新打包。

Ubuntu x64/UID1001 **2/2 PASS、5.952秒**，macOS arm64/UID501 **2/2 PASS、5.648秒**，均Node24.15.0、零失败/取消/跳过。各自layout1/layout2使用原安装包CLI、HTTP、SQLite与所属无模型协议进程，完成团队、独立验收、成果下载、冷重开；每队原4次执行，旧任务重复启动0，下载摘要均为 `sha256:3055d2141868fed0c8abbf628e50fa959a623fd93474a928318a802efa7b6fb0`。原job分别为 `102360061655`、`102360061554`，生产者job=`102360005595`。

这关闭该候选在两声明平台的**同资产安装消费测试**子条件，不等于真实模型在Linux验收、香港ECS部署、升级迁移、长期SLO、Publisher分权或正式stable发行。artifact保留期7天，不是永久release资产；正式发行仍需受保护发行入口与受支持部署实证。下一节“尚未运行”的叙述保留为前一时点。

### 2026-09-09：同一安装包消费与双平台回归

已推送主线 `2d178b4f96caeb2708d861b837160de5cf758f78` 的 [CI 34316647809](https://github.com/chiga0/marshal-harness/actions/runs/34316647809) Ubuntu/macOS 均成功；它覆盖前述 Darwin 修正，不抹去 `ce55eed` 的两项失败。

安装载体实现 source=`4d0b833e90946fc131a0a45f21a2d9c67b9c0322`，集成=`413e92dd`；唯一 reviewer 无P0/P1，独立8/8通过32.594秒，维护者聚合20/20通过102.275秒。载体不是可直接执行的安装目录：先以包外固定 sourceHead/manifestDigest 核验全量内容，再排他创建0700目录/0600文件；不覆盖旧目录、不信任载体自身重算的摘要、不修改载体权限凑通过。

维护者另将精确干净源码 `d42f437af1b68c304c1267d156c92f2ccd1e9b0e` **只打包一次**（47文件、647188 bytes），模拟755/644传输权限后还原到全新私有目录。包外 pin=`sha256:aed4c43d143ace9efb75f2eb9c4a3a69e4d34d051bca6e92baeebe9173effc6a`。没有 Git 的 PATH 下，同包原CLI/HTTP客户端在 Darwin arm64、Node24.15.0、UID501 完成 layout1/layout2 团队→独立验收→下载→冷重开，**2/2 PASS、10.866秒、零跳过**；各4次原执行，冷重开旧任务重复启动0。原记录保存在 `/private/tmp/marshal-carrier-independent.GxzlZy/report.json` 与 `results.log`。这是无模型安装消费验证，不是 Pi/Qwen 模型证据或ECS部署成功。

新CI将在原双平台回归后仅生成一份目录包，再由 Ubuntu/macOS 按同一 artifact ID 和包外 pin 分别安装、消费；此段尚未记录新流程运行成功，不能用旧CI替代。ADR0092仍按其状态单独判断；安装工具不修复未绑定执行，也不关闭B1/B2/B3。

### 2026-09-09：提交窗口恢复补验与 Darwin 清理观察修正

新增恢复测试 source=`8ceea577747fce9b2b2f7ac279ad7fec880c1c91`，集成=`3b36343`；维护者独立审查无阻塞，原 CLI/HTTP/SQLite/custodian 六项 **6/6 PASS、39.179秒**。覆盖 reservation、custody binding、cancel 的 COMMIT 前后实际服务 SIGKILL。原预算、期限、回执和失败事实保持；绑定已提交但许可未发送时，由原签名 none-start 观察收口；cancel 已提交但202丢失时精确重放原回执。可结清路径均完成下一新团队、独立验收/下载和第三次 open 零追加，无真实模型调用。

**未绑定恢复仍是发布缺口**：reservation-after/binding-before 保留占用与 intervention，ready 封闭；没有安全的现成 HTTP 解除入口。测试通过仅证明没有错误释放，不代表可自动恢复。两份未决现场保留，后继必须以可信、耐久的“从未获得启动许可”协议证明解决，不能用目录为空、PID 消失或手改账本清占用。Git prepare 在绑定前可能启动子进程，不能照搬只做本进程文件准备的结论。首轮作者5/6中的唯一失败是 SQL 普通对象与客户端 null-prototype 对象的比较错误，改为规范字节比较，未修改生产行为。

精确 `ce55eed` 的 [Node team CI 34315167189](https://github.com/chiga0/marshal-harness/actions/runs/34315167189) 为 **macOS 519/521通过、2失败；Ubuntu通过**。两失败都在取消/期限清理的 cleaned 断言，不能拿前一源CI或本机通过覆盖。独立实机诊断确认 Darwin 自有退出组可出现 `EPERM → waitpid回收 → ESRCH`；[Apple 内核路径](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_sig.c#L1612)支持该机制，但原 CI 缺 errno，尚不能断言就是本次失败分支。

修正 source=`285a3516c43dee8c1b3c62b56a5bfb9a2ef75b64` 仅在 Darwin 的原5秒清理预算内重观测 EPERM，始终视作未决；必须取得后续真实 ESRCH及原回执/guard退出才成功。持续EPERM到期、其他错误、后代仍活仍拒绝；不续期、不补停止信号、不改持久合同。新瞬时错误用例修前1/1失败，修后作者定向18/18通过31.100秒；唯一独立 reviewer 无P0/P1，独立18/18通过34.668秒、零失败/跳过，合并同步另按实际记录。测试诊断仅输出有界原清理事实，无原始日志或凭证。B1/B2/B3不因本节升级，API-STABLE仍仅为当前接口检查点。

### 2026-09-09 13:25 CST：API-STABLE 检查点通过，正式发布仍未完成

已审集成 sourceHead=`5657a7d`，localMergeSha=`94795f37e37863d0fddb003874b6fac5f7f8a082`，已正常推送、核对远端相同，`pendingRemoteSync=false`。该批仅增加 Pi/custody 取消验收、连续任务隔离测试及文档导航修正，不改变生产 Core/Provider/Store；生产实现仍与 `69cface` 相同。前一 main `9700adec` 的 [Node team CI 34313835232](https://github.com/chiga0/marshal-harness/actions/runs/34313835232) Ubuntu/macOS 均通过，新合并的 CI 异步另记。本文后续时间段是历史事实，不覆盖本节。

**接口检查点不是正式发行**：原独立 reviewer 已按 Milestone 的四项条件复核，当前 `node-task-service/v1`、25操作/58 Schema与同包严格客户端记为 `API-STABLE: PASSED`。OpenAPI `info.version` 仍为 `0.1.0-candidate`；本次只将描述中的旧阶段声明改为指向 Roadmap，不改变任何请求/响应、路径、枚举或校验规则。兼容范围不包含旧 Go、九操作实验协议、任意未来版本或全 Provider；不能把候选接口检查点当 v1 stable 资产。

元数据更新后 OpenAPI SHA-256=`c8fe689c950821be3f284cfd6df8606da608bf353bc2db9e9cd965ab70f5a13d`。与原JSON机械比较仅`info.description`不同；标准 Draft2020-12 验证58 Schema及34示例通过，原API/client组合 **32/32 PASS、1.367秒、零失败/取消/跳过**。上一描述版本的`c41c0995…`摘要保留为历史，不伪装整个JSON字节未变化，也不因此重新调用模型。

| 原退出条件 | 本次证据与边界 |
| --- | --- |
| 合同/handler/示例/客户端一致，无未处置核心API P0/P1 | 原58 Schema、34示例及25操作验证；同包客户端、原revision/digest、精确receipt重放与严格拒绝规则；本批元数据说明不改变合同结构 |
| 至少一个真实Provider的纯HTTP交付与主要失败控制 | `69cface` 原Pi/custody问答→双作者→独立验收→下载/审计通过；`37df1e7` 同生产字节、同Pi/SDK/checker/policy下取消通过。不是拼接Qwen layout1，也不需要客户端手改账本 |
| 已提供功能的幂等、CAS、授权/对象/路径/迟到取消/事件续读反例 | 原API/Application/client回归及已合入事件断连/正常重开后原cursor续读通过；不宣称SSE、任意崩溃或未来接口兼容 |
| 原HTTP脚本和独立客户端；并行/长Verify查询取消有界 | 原TaskClient与raw HTTP共同消费；两个行为用例独立2/2、9.730秒，6成功查询及2取消各小于2秒。是限定回归，不是生产SLO |

**同配置真实 Pi 取消**：原 driver source=`b1badbb39d432ca58ffa1b78364cae91c2e493d7`，独立集成测试 source=`89e7da1`，20/20 PASS、5.642秒；包含真实无模型 Pi bridge/custody/layout3 HTTP取消/冷开及原Qwen默认取消。随后只执行一次真实 Pi0.84.4、Node24.15.0，运行前后冻结 source=`37df1e7a57d7b356ec385113f8c76e74c1f2f5a8`且clean：Task=`task-d2a44c9b-93fe-4942-8db8-d2d682a88546`，13.046秒，原planner加2作者共3 Attempts、作者生命周期交叠55ms，取消后39ms两原进程均退出且cleanup确认。Task cancelled、Operation succeeded、0验收器/制品/容量；正常open保留原create/approve/cancel回执、相同Task/Workers，零重复派发。

证据 `/private/tmp/marshal-pi-custody-cancel.f9LwIv/run/evidence.json`，SHA-256=`1eb946185b73f2106fd4942d3879008baee5492d4239c6d0d0948e33b56928c3`。原 reviewer 对精确SQLite作query_only核验：三个原custody descriptor、receipt bytes与bindingDigest，以及Task/Worker/reservation/input/generation对应；原4 Attempts/2 Workers/600000ms上限不变。source绑定来自冻结运行记录，JSON不是签名source收据。此为早期running进程取消；权限回调0/0、modelConsumptionProven=false、toolExecutionProven=false，不扩大成工具执行中取消、恶意隔离或crash证明。ordinary-user、production=false、Publisher分权未证。

**连续多Task与坏任务隔离**：source=`bd75a8ef652db1bde695c130f9219ae9f83005eb`，集成独立测试 source=`e7f2878dff11e8d1ca511ed3d2e526b4b5b630c1`，1/1 PASS、46.317秒。一个原CLI/custody/layout2实例，三波12个健康团队下载并独立重算；原ACP进程产生错误内容被独立checker拒收1Task，另一原挂起Task被HTTP取消。55次原执行、峰值4/单Task最多2、最终容量0，13个独立Decision为12 accepted加1 rejected，原失败摘要/通知、55累计预算不退款，幂等回执重放不产生执行；原CLI clean/exit0。无模型、不是24小时soak或生产SLO。

保留测试作者的两次夹具假红：先误要求无diagnostic，再误认为Service转发stage/Task字段；实际上composition只转发code，最终断言唯一`worker_failed`，身份由原completion及持久Decision/digest独立绑定。生产行为未改，未抹去真实拒收；另改等待原child `close`排空stdout，避免`exit`先到造成假红。以后新增观测断言先核对真实producer，再跑完整场景，不逐项猜测和重试。

**下一步按真实缺口推进**：B1部署分权、B2真实局部内容修正/未覆盖恢复、B3声明平台安装与长期故障/正式发行继续开放。局部修正工具已具备，只在真实原独立内容拒收且原预算/期限有效时使用；已成功Task不能用追加反馈重开，不重复付费寻找失败。香港现成独立runner可复用，但安装包仍未传入，SCP137原因未明；替代传输待用户确认，runner自己的Agent/模型授权范围未验证而非已确认缺失，不复制登录或绕过策略。暂无UI任务；先完成受支持部署和既有主线缺口。

### 2026-09-09 13:06 CST：当前候选取消、双平台回归及新目录恢复补验

精确产品候选仍是已推送的 `69cfacee2fbea366174744167d5a2c3a3dfeee89`。[Node team CI 34312972893](https://github.com/chiga0/marshal-harness/actions/runs/34312972893) 已通过：macOS **511/511、222.154秒**，Ubuntu **511/511、225.774秒**，均零失败/取消/跳过。不是把上一源510项移作新源证据。

同源真实 Qwen0.23.0 `--scenario cancel` 一次 **14.143秒通过**：Task=`task-6c21a613-7324-4a31-b380-72bd953c6db0`，原 planner 完成后两个作者的原进程/HTTP投影均活跃，发送一次取消，两原进程均在请求后退出并cleanup；3 Attempts、Task cancelled、Operation succeeded、verifier 0、无delivery。正常同版本实例重开保留 create/approve/cancel原回执、相同Task，重复启动0。原证据 `/private/tmp/marshal-current-qwen-cancel.ohj9Di/run/evidence.json`，SHA-256=`822ebf15539c788212b791b483cc98354b4e2563770fda5de0b1ce6728a4e744`；入口为本机已安装 `/Users/gawain/.local/lib/qwen-code/lib/cli-entry.js`，不再用旧README中的不存在路径。没有读取或复制登录文件、改变模型配置或自动重试。

该取消发生在作者启动后的早期窗口，两个原生命周期交叠45ms，不能声明模型已开始token/tool工作；权限回调0/0不证明权限隔离。此原Qwen driver未启用新custody持久化profile，因此是当前代码原layout1的真实HTTP取消/普通进程清理证据，不替代Pi运行中取消、新custody crash或Worker/Publisher分权。本机ordinary-user、production=false口径保留。执行前原取消/驱动无模型回归独立17/17通过2.426秒。

同版本冷备份测试 source=`1c3c4398796e5699448f9e89e17010606dca9940`，集成测试source=`796d40c`（生产字节为69cface；随后9edb637仅补部署README）。主Agent独立运行原CLI/HTTP/SQLite/实际受管协议进程及独立checker，**4/4 PASS、13.066秒**：正常SIGTERM且clean/exit0→完整22文件保权限/摘要/fsync复制→新目录open；原37条事件、SQL事实、回执/下载保持、旧任务零重派，再完成新4 Attempt团队。缺DB拒绝且不新建、缺blob的metadata/content均503且不补造、同物理目录第二writer拒绝。测试作者此前两次夹具假设错误（metadata也验blob、artifact ID非裸UUID）保留，未修改生产合同；这不是在线备份、断电证明、跨版本迁移或克隆副本防分叉。旧根和备份必须保持停用。

这关闭当前Qwen/layout1取消子证据与冷复制恢复测试缺口；原reviewer已确认备份测试无P0/P1。API-STABLE第2条仍待Pi/custody同一profile的取消控制，不能拼接两个不同profile宣布通过；当前两作者分别补这一实机驱动及连续多Task/坏任务隔离验证。后继main同步另按实际结果记录，不预填正式发布。B1实际Publisher分权、B2未覆盖的真实局部修正、B3持续多Task/故障/平台部署与正式资产仍开放。

### 2026-09-09 12:59 CST：当前候选真实问答交付与规划合同修正

sourceHead=`42426a2ad35e96569b356e283177a93b98ab1fcb`，localMergeSha=`69cfacee2fbea366174744167d5a2c3a3dfeee89`，已正常推送并核对远端一致，`pendingRemoteSync=false`。该候选真实 Pi0.84.4/Node24.15.0 在原 HTTP driver 上完成规划、一次批准、双作者、运行中业务问题/答案 ACK、独立命令验收、成果下载消费与正常实例重开，**29.624秒**、作者生命周期交叠 **10.189秒**、4 Attempts、1次 verifier，原执行全部确认清理。east 等待答案时 west 已完成；原 east Worker/Attempt 消费答案后继续，没有新派替代。下载338 bytes：east cancelled为1单/9000 cents，west paid为2单/550 cents。正常重开保留原回执、相同制品，重复启动0；业务字段 cancelled 不是 Task 取消。

原证据 `/private/tmp/marshal-typed-planner-pi.efuLbd/run/evidence.json`，SHA-256=`f20cf8aec82d716874d458dfee28148904027aa1126545d10737860d8a923979`；下载 SHA-256=`584a06f86d5db240769b975bfa2aafe1771c50aa63d046a9dfc51508600de50c`。source 来自运行前后冻结树检查，观察 JSON 不是签名 source receipt。原生权限回调允许5次、拒绝2次不构成同 UID 恶意隔离；ordinary-user、production=false、Publisher分权未证。本次是受限公开业务及正常重开，不证明任意需求规划、实际 repair 或活跃模型 crash 恢复。

**保留一次真实失败及原因**：前一个精确源 `0f86804140546b9e4593ba39d1a98dcb70db18bc` 的12.365秒试验在规划阶段失败，1 Attempt、无作者/验收/交付；Pi回合 completed、原cleanup=true，不等于业务成功。原提示只列 scope 字段名，模型三个节点均返回 `{read,write}`，Core 要求 `string[]`，因此 Task 为 `failed/invalid_plan`。独立只读重放原报告得到 `invalid_plan_node`；仅诊断内存副本改数组就通过，原库/报告不改。失败证据 SHA-256=`2b77d054d1ee74143f87f89c4d80128cbd4530d0a9e7dddd9f0ee95ef06642a8`，路径 `/private/tmp/marshal-current-pi-http.Mjl88N/run/evidence.json`。

修复仅补完整 Planner 输出类型、角色/ID、数组/DAG/预算说明和字段示例，不转换模型结果、不放宽 Core 或自动重试。原 reviewer 确认1项P1关闭，独立 **24/24 PASS、1.770秒**；测试从真实 prepared prompt 的示例，经原 collect→freezePlan 正向，错误对象 scope 仍拒绝。作者首次新增测试复用了已释放 ticket 而失败1项，改为独立 fixture 后24/24通过，生产 collect/release不变；该测试错误与实机失败分别保留，不把本轮说成首轮全绿。

上一源 `0f86804` 的 [Node team CI 34312082617](https://github.com/chiga0/marshal-harness/actions/runs/34312082617) 已终结：Ubuntu **510/510、230.166秒**，macOS **510/510、261.149秒**，均零失败/取消/跳过。两平台覆盖数不相加冒充1020个不同场景；新修复源的CI另行核对。已合入的API-STABLE两项行为测试独立2/2通过9.730秒：并行执行/长Verify下6次成功查询、2次202取消各自小于2秒，以及分页事件在断连和正常重开后原cursor续读无重漏。它们使用真实Node协议进程与SQLite，不是模型、SSE或生产SLO证明。

下一步收口当前候选的API证据及停服完整快照→新目录恢复；不把B3全平台/Publisher分权新增为API-STABLE前置，也不把API候选测试当正式发布。ECS替代传输方式尚待用户确认，未部署的事实不变。

### 2026-09-09 12:39 CST：输入审计与简启动合入并同步

sourceHead=`fe04a8a2f884e42afc5a7f1ad7a62e6d56a4b930`，localMergeSha=`ee3932ffc135f89ce0395f159696f93c6684da1d`；远端 main 已核对同一 SHA，`pendingRemoteSync=false`。实际 prepared prompt 的摘要/长度、有效 Provider 句柄返回后的 handed-off 观察、原 SQLite 审计引用与 Depot 快照下载已接线；默认不存正文，只有显式受信同步披露策略返回的内容可留存。handed-off 不证明模型消费，缺失用量仍 unavailable。原 CLI 增加默认私有数据目录和自动 create/open，仍必须提供受信业务/Provider/独立验证配置，不是任意任务零配置。

输入审计首审无 P0/P1；简启动首审发现多层目录 fsync 失败后重试缩短祖先同步链的一项 P1，聚合修正后由原 reviewer 确认关闭，不记首审通过。最终冻结集成源独立 **29/29 PASS、57.395365167秒、零失败/取消/跳过**，覆盖 Application/HTTP 输入审计、原 CLI 启动重开与目录拒绝/耐久失败、发行清单/安装目录冷导入。日志 SHA-256=`016f01f93c57df6b3a8cb52e2584c71b27214ed13f438ee545325b6a5e790878`。Draft2020-12 独立检查58个Schema、34个示例、25操作通过，OpenAPI SHA-256=`c41c0995f3e9030a1ca7ad237f20116d5207c1e68e789fa8f45d1da785b6b0ae`；diff、merge-tree及4提交秘密扫描通过。该组合无真实模型；前一轮8f8ff8d的远端CI不冒充本源CI。

香港 Linux 只读检查已确认既有专用用户 UID1000、固定 Node24.15.0及SQLite3.51.3可执行。旧源85fc62a的45文件目录包已在本机生成，但SCP再次exit137，远端新安装目录核对为空，具体终止原因未知；未启动产品服务或模型，未换通道重传，不记部署通过。本地包与观察保留于 `/private/tmp/marshal-linux-install-85fc62a.qGdfih/`，它不含本次新功能，不能用于证明本源部署。

下一项为 API-STABLE 的成功查询/取消响应上界及断连/正常重开后原事件cursor续读验收；已有原始HTTP与TaskClient消费者，不新增SDK。真实模型局部修正、Publisher分权、完整支持矩阵/长期故障及正式发行仍未证明，B1/B2保持IN_PROGRESS、API-STABLE未完成、B3 PLANNED。

### 2026-09-09 12:20 CST：已审代码合入并同步 main

sourceHead=`8f8ff8dfe57852e6b383fbd2bb7c70fb75a1d552`，localMergeSha=`85fc62a3338fc15c408e941e16a7c882b0be4a6d`；正常推送后远端 main 核对为同一 SHA，`pendingRemoteSync=false`。已审运行中问答、同计划局部修正及上述实机证据进入 main，未使用 force push。该 source 的 [Node team CI 34310020140](https://github.com/chiga0/marshal-harness/actions/runs/34310020140) Ubuntu/macOS 两项均 success；这是 Node 测试矩阵，不替代真实 Linux 部署、模型修正或权限分离证明。此前相关本地定向80项及真实 Pi 首轮交付的范围不扩大。

当前 OpenAPI 为3.1.0、合同版本 `0.1.0-candidate`，25个操作、55个 Schema；不是 API-STABLE。实际输入审计与简启动仍在独立工作区实施，未纳入本次 merge。B1/B2继续 IN_PROGRESS，API-STABLE 未完成、B3 PLANNED。以下带时间的“未合 main/待同步”保留为历史事实，不覆盖本检查点。

### 2026-09-09 12:04 CST：修正启用配置下真实 Pi 首轮交付成功

执行源 `d6502386f1be840096dcf002e9bb2eb551801765` 已正常推送至 `feat/node-custody-integration`；远端 main 仍为 `7250ec9`，不是 main 合并。真实 Pi 0.84.4 在 Node 24.15.0 下经 HTTP 创建、固定完整业务计划核验后一次确认、双作者、原独立 checker、下载消费与正常服务实例重开，得到 `natural-first-pass`、`validObservation=true`。总观察时间 **26.288秒**，作者执行生命周期交叠 **11.788秒**，共4 Attempts；这不等于 CPU 同时忙，也不代表任意需求自动规划。

公开合成业务先按订单取最新 revision，再按 paid 状态统计，包含取消、零额与负退款。下载182 bytes：east为3单/1175 cents，west为4单/675 cents，总计7单/1850 cents；在作者之外的原命令验证及不同算法的下载消费均通过。原清理完成，正常重开保留原回执和同字节制品，重复启动0。独立审查者核对原输入、manifest、driver/checker摘要及公开证据一致；证据 SHA-256=`a678d6ab0821a1bd1ae236cabb73b639851bffcbd38978a2b1420923eed74d90`，交付 SHA-256=`bad4d08cffbd30c44ab25d443917a78b1491cb5c57122cb53d6454dae9f34fcd`。source 归属来自执行记录，JSON不是签名 source receipt。

**没有发生真实 repair**：模型首次正确，因此不人为制造错误或追加付费尝试寻找失败样本。此前80项确定性组合及实机工具10项无模型回归覆盖修正接缝，但不替代自然内容拒收后的真实模型修正证据。此次正常重开不是 crash 测试；ordinary-user、`production=false`、Publisher分权未证，token/cost保持 unavailable。B1/B2/API-STABLE/B3不升级。

下一完整纵切并行推进实际 prompt/context 审计与原入口简启动；仍复用同一 Application、Store、Supervisor及受信业务配置，不新增控制平台或为了占满槽位扩品牌。真实修正机会出现时按原负面证据、原预算和期限处理，不重跑无关成功分支。

### 2026-09-09：同计划局部修正通过独立复审与最终组合回归

完整实现候选 `e4ee99f3de6b66809d725774a57e143410d61821` 已接线 ADR0091 的修正入口、父验证内容拒收/原负面报告、明确结果选择、原文件业务诊断输入、HTTP/client、v4-repair 状态与发行清单。后继聚合修正 `bc519e6a0d8c704424ae1f4d98ac7f22f63cb370` 由原 reviewer 复审：两项 P1 均关闭，无新增 P0/P1。尚无真实模型局部修正验收，不提升 B2/API-STABLE。

独立测试提交 `7829114498b217a95abee88ca526d6425443ee31` 仅增加三个完整服务场景文件，绑定原 e4ee 生产字节：先正向 **1/1 PASS、9.536秒**，再完整 **6/6 PASS、39.251秒、零失败/取消/跳过**，不重复计为七个独立场景。原 HTTP/SQLite/受管协议进程和独立命令检查器验证：首轮两作者及验收用4次 Attempt；原预算6只允许重做错误分支及最终验收，成功分支的 Worker/结果/bytes 不变；原负报告、第三下载消费及冷重开一致；结构失败和坏帧不取得修正资格，取消不启动新 Verifier，repair COMMIT 前/后崩溃不留下半周期或重派旧命令。日志 SHA-256=`ac3d10844f2df9491c4412f91ad9b81c560e754370db10ead80bed3e3e774472`。

主 Agent 独立补验同一冻结源的 Application repair、原 command verifier、全部 API/client 组合 **48/48 PASS、34.443秒**，日志 SHA-256=`b53eeb164b7b1ca0087723c96a2be515cf5cc76ec3bb931ab95113de9e6e0dd3`。标准 Draft2020-12 经隔离目录固定 Ajv8.17.1（纯 JavaScript、禁用安装脚本）验证55个 Schema、34个示例、25个操作通过；OpenAPI SHA-256=`39b75781bc2ae0e525a83d6cc4b93bb0c8d114ef9d162cc36853e2a0d3ad5d96`。作者的85项兼容/安装回归仅记作者检查，不冒称独立证据或与重叠测试累加。

首审发现并已关闭：`TaskRepair.commands` 同事务扫描全局历史，独立原 SQLite 复现90个无关已结任务、185条 observed 命令后，目标 Task 原状态/预算不变却 get/repair 返回503。修正采用原 Store 按 Task 选命令，不扩大事务限额或跳过原事实核验；另将 repair/Verifier 缺失或漂移配置前移到任何 Planner 启动之前拒绝。原 reviewer 重跑精确反例，确认查询/修正及原回执重放通过；真实服务配置失败在业务/Provider 启动前拒绝且无 Task/Attempt。两项合并一次 rework，不靠模型重试诊断结构问题。

最终集成 `e271f1ab5c3b8e6c2b1e6a18e89eec486b02692f` 包含上述修正、六场景服务测试及独立 retained-ACK 冷恢复测试。主 Agent 独立运行 Application repair/问答、Service repair/问答恢复、Store、原 command verifier 组合 **80/80 PASS、108.385秒、零失败/取消/跳过**；日志 SHA-256=`474181cdd8d3b9a603d9cdd02dd8a37cbf1281e3a4327948cd20511ae47ecb82`。原已 ACK 回答在冷 owner 变更及仅修正代码分支后保持原持久化身份，不重投旧命令；陈旧 owner 写入拒绝。最终 Schema 同上重新通过，16个新增非 merge 提交 secret scan 与 diff-check 通过。此80项与之前48/6项存在重叠，不累加为独立覆盖数。

本轮没有制造模型错误来获取 repair 成功记录：后继真实模型试验若首轮正确，诚实记录 first-pass；仅真实独立内容拒收且满足原预算时，才显式提供业务反馈并验证局部修正。同步检查时远端 main 仍为 `7250ec95526e237aa918f164c71e9d7c7b0f7a62`，没有分叉，不需要 force push。用户再次明确维护者合并/推送授权后，宿主仍以旧 Merge 禁用条款拒绝 main 修改；`localMergeSha=null`、main 的 `pendingRemoteSync=true`。功能分支继续正常保存，最终推送结果另行核对，不通过强推或间接命令绕过拒绝。

### 2026-09-09 11:22 CST：真实运行中问答交付通过

同步状态：当前 `main` 与已核对远端 main 仍为 `7250ec95526e237aa918f164c71e9d7c7b0f7a62`，本次 `sourceHead=1b3b7d6515b9d54bac554e392c735d0793171b96`、`localMergeSha=null`、`pendingRemoteSync=true`。维护者 main 合并被宿主自动权限审核拒绝：审核引用用户上下文中的旧 Merge 禁用条款；读取并提交当前磁盘 AGENTS 中明确维护者研发合并授权后仍被拒。未改写权限、未替换命令绕过、未合并或覆盖 main；正常功能分支保存与不受影响的局部修正开发继续，最终推送事实另行报告。

冻结源码 `1b3b7d6515b9d54bac554e392c735d0793171b96`，首次失败后完成有证据的兼容修复，再进行一次实机尝试，**50.938秒通过**。Task=`task-bcbae0f3-e649-47d7-8c65-146646dfb147`，Pi0.84.4、Node24.15.0，原规划、两个作者及一次独立 verifier；作者实际交叠18.979秒。east 在原 Worker/Attempt 中提出过滤状态问题，west 在发送答案前已独立完成；答案 `cancelled` 未预填入 Task prompt，而由工具在真实问题出现后经一次 HTTP 写入，原 east Worker 消费并继续交付。此处 cancelled 是业务订单状态，不是取消 Task。

下载338 bytes，两个地区报告经下载后再次独立核验，摘要=`sha256:9a211808c246fbb25c011c637ac352daf929820abe556b2ce3d12873f9dece4e`；所有原执行清理确认。正常服务重开保留原回执、同字节成果，重复启动0。原证据 `/private/tmp/marshal-question-live-fixed.bOvp75/run/evidence.json`，SHA-256=`e5b790ce7d7826d9f8571c9f5dbb9ed07f1957e3e8aba87316ada4c333f3ab9f`。这是 ordinary-user 的真实固定业务问答证据，`production=false`、`publisherSeparationProven=false`；不是任意规划质量、模型运行中 crash 或 stable 发布证明。首次失败记录保留如下，两次真实尝试为一失败一成功，不宣称首轮通过。

兼容修复 source=`de382cd1066df5c79e4acc307246e1499173bacb`：同一独立 reviewer 无剩余 P0/P1，Core/Provider/真实HTTP **13/13 PASS、9.646秒**；再使用原安装 Pi SDK 验证其中 **4/4 PASS、12.406秒**，不重复计为17个唯一测试。原 SDK 复合 ID 可精确登记/replay；NFC/NFD 不被规范化，旧 ACK 不能串用到下一问题。集成相对 main 的 merge-tree、diff-check 和完整差异 secret scan 通过。尚未在此段预填 main 合并/远端同步结果。

### 2026-09-09：运行中问答整链集成与独立恢复验证

冻结集成 `1b7427c98bb1270490a4dc7ee4b869277ec61112` 的完整 Node 回归 **447/447 PASS、376.515秒、零失败/取消/跳过**，运行结束 HEAD 不变且 clean。日志 `/private/tmp/marshal-question-final-20260909.CcnSHX/results.log`，SHA-256=`a45c2d37929b2e0a4726505c148d9863fec5166f1214a76a92c13edcb90b70b9`。该版本包括运行中问答实现 `9cb066e` 和独立恢复测试 `73e2249`；不把后来增加的实机工具或修复计入这次全套。

独立恢复四场景先单独 **4/4 PASS、29.111秒**：正常原 Worker 回答/ACK/交付、投递后取消、投递后服务 SIGKILL、ACK 事务提交后服务 SIGKILL。未确认消费继续显示 unknown，不重投旧答案、不新建旧 Worker；原 custody 结清后同一重开服务可交付另一任务，第三次正常重开保持五类原权威表及回执/成果，无重复派发。这是原 CLI/HTTP/SQLite/受管协议进程的故障证据，不是模型崩溃实测。单组日志 SHA-256=`1fee89ed07d20717e2198e2a0c526c99a6dc8e522dcaa209626d27caab383faf`。

首审发现一个真实 P1：中间节点完成只保存自身问题引用，丢掉已消费的祖先答案，使 A→B→C 的 C 收不到原业务信息；另有暂停期间登记问题后 resume 状态显示错误。它们由同一作者聚合修正、原 reviewer 复核，不能因为旧447项通过就忽略覆盖缺口。两次较早恢复测试失败来自测试对 HTTP null-prototype 和 Task-wide Attempt ordinal 的错误假设，已修正并保留日志；不计作生产失败或首轮成功。学习应固化为多级依赖/状态组合测试及复用真实公共值语义，不增加审批轮次。

聚合修复 `cfaba043c55529c78911e59688e33b3801b3722b` 经原 reviewer 复审，原 P1/P2 均关闭，无剩余确定 P0/P1；独立8/8通过，原三级复现引用数由1→0修为1→1，实际 prepared prompt 保留原答案。修复同事务重读直接上游 Worker/candidate/resultDigest/cleanup/原 generation/plan，继承答案再与原 question/answer/dispatch/ACK 对齐，稳定去重并合入自身答案，不吸入无关待答问题。最终集成 `f129ce20d65650252d1dee363766aed1fbe999cc` 独立定向 **108/108 PASS、67.888秒、零失败/取消/跳过**，包括 Application、独立 Verifier、真实 HTTP 问答、四恢复场景和实机工具；日志 SHA-256=`40871eccd628d0d44f8fac0d39f589bfa16739cc2269d91d12140556352e734e`。这是修复后相关组合，不将前一源码的447项改称最终全套。

显式真实 Pi 问答工具 `3bec5e3` 经非作者审查无 P0/P1；集成后的独立无模型 **9/9 PASS、0.808秒**。工具不进入生产清单，答案只在真实问题出现且另一作者完成后经 HTTP 发送，不预先填入模型 prompt。上述记录不预填实机成功、main 合并或正式可用。

下一关键路径为已接受 [ADR 0091](adr/0091-node-same-plan-local-repair.md) 的完整同计划局部修正，而非重跑整队。其后补真实 prepared/input 快照及 Pi 原生用量：现有 reservation input 可复用，但不能把重新渲染文本当历史实际 prompt，也不能把进程 started 当协议已提交。未知 token/cost 保持不可用，失败/取消/修正 Attempt 不从分母删除，Provider 成本实报不冒充已对账扣费；不新建第二审计库。

首次真实 Pi 问答试验在 `f129ce20` 上失败，不能宣称问答实机通过：Task=`task-33286d0f-9ab8-45d8-81ff-1e5d8e3c2ae5`，03:03:49.784Z→03:04:12.537Z，共22.753秒；规划完成，east 发起问题工具后失败、west 取消，原执行清理全部确认，问题列表为空，未发送业务答案。原证据 `/private/tmp/marshal-question-live.erHFtX/run/evidence.json`，SHA-256=`891d22d8390030dca987e60079618d2924598f8808b09e979f003e0cb752b0a7`。该次工具仅保存 `missing_business_question`，不足以反推丢失的原生 ID 字节。

独立诊断使用公开安装 Pi0.84.4 的原 Responses producer，在无模型条件下复现：原生产函数产生 `call_id|item_id`，原样进入 Core 后被内部主键正则误拒，原 SQLite ledger 不变；只换为简单 ID 即成功。明确修复接缝为外部 correlation ID 的有界 opaque 文本，不截断、不拆分或规范化；内部 questionId、原 nonce/摘要/Worker/ACK 绑定均不放宽。新增边界测试及完整 HTTP 问答的复合 ID 夹具；修复独立通过后才进行新的实机尝试，保留第一次失败在分母。此问题说明测试需覆盖真实 producer 的输出形态，不应靠反复模型重试暴露接缝。

### 2026-09-09 10:23 CST：真实 Pi＋Qwen 双仓库交付已推送

sourceHead=`76f9c3612ae0af1bfb8bb437e21cbbaabf0f1666`，localMergeSha=`7294878e68c59a592b4c8cd1236da34bad038fe2`；正常推送及远端 SHA 已核对，产品 pendingRemoteSync=false。新问答 API/client 与显式 Git 混合验收驱动经独立审查，无 P0/P1，最终定向组合 **48/48 PASS、20.930秒、零跳过**。API 单候选独立42/42通过2.292秒；此前一次沙箱内执行的13项 HTTP listen EPERM 保留为环境拒绝，使用合法 loopback 权限复跑后通过，没有改测试。实际 Draft2020-12 验证49个Schema/32个示例通过；只支持合同/传输，运行问答 Core/Pi 尚在实现。

真实单次 Task=`task-4710f0d9-66b2-4f89-81d2-5193cc794b7f`，02:21:38.108Z→02:22:35.022Z，**56.914秒**。Pi0.84.4 负责原规划和 library，Qwen0.22.3 负责 client；两作者实际执行交叠 **11.631秒**。固定完整业务计划、一次批准、4 Attempts、两作者、独立 command verifier；没有模型自动重试。两个新建公开合成 Git 仓库的已有文件由原生 Agent 实际修改，原锁定 base/无关文件保持，输出真实 patch/context。它证明固定完整计划的混合交付，不证明任意自然语言自动拆解质量。

原独立验收与下载后第三组真实工作区应用均通过23检查（含12负例），两笔金额合计1350；下载4285 bytes，摘要 `sha256:6b9c14916420a8d0ac6d247cf3dafa08d8114cbabac70cbaff51b39b57e1e959`。全部原执行清理被确认，正常同版本服务重开保留 create/approve 回执和同字节成果，重复启动0。原始受保护证据 `/private/tmp/marshal-git-mixed-live.kN1T7f/run/evidence.json`，SHA-256=`f8ce2bec4560fc443f1cc23e31d5fe9a469fdbb679cf1546274013f39f5fb0ba`；观察记录不是签名权威收据。

权限观察：Pi allowed=3/denied=1；**Qwen allowed=0/denied=0，仅未触发回调，不能据此宣称本次强制授权或越界拒绝通过**。沿用本机原生配置，ordinary-user、production=false、publisherSeparationProven=false；正常重开不是模型执行中 crash 恢复。B2 的真实混合 Git 业务子条件已有这次证据；运行中问答、局部修正、完整同版本故障矩阵和实际分权/平台发行仍开放，B1/B2/API-STABLE/B3整体不升级。

### 2026-09-09 10:12 CST：恢复、安装包与 Git 交付已合并推送

sourceHead=`db57d45c7b8a951eb3adddafd2f649ad165cd068`，localMergeSha=`0057dd1b7c5b259f238600bacbc3abb346d770ca`；正常推送后远端 main 已核对，产品 `pendingRemoteSync=false`。最终完整 Node 组合 **418/418 PASS、319.688秒、零失败/取消/跳过**，冻结树不变且 clean；日志 SHA-256=`47b7d7ceb5c01fa5611ab93e2fc5f3c08ea4c51d8672de6f3a8204aaecd7d63f`。 本次汇合正式执行托管、SQLite cleanup-only 接纳、ACP/Pi scope 修正、真实进程故障测试、已安装目录包团队交付和 Git 多仓库业务插件。更早记录仅保留当时结论。

独立诊断已经确认唯一失败模式：`custom` 在776ms结束为 `unknown/pi_execution_scope_unproven`，但原 `runtimeCleanup.cleaned=true`、guard SIGKILL、没有输出文件；`no-bridge` 保持原1500ms deadline，正确失败且清理完成。诊断 `/private/tmp/marshal-pi-cleanup-diagnostic.6U6mey`；不延长生产/测试期限、不把原 clean 断言改成 unknown 放行。根因是 unsupported start 的提前永久标记压过后续原 bridge 的精确 blocked 事实；修正保持 v2 在缺少安全证据前崩溃时的保守义务。全套原失败日志 SHA-256=`36520df971642c9631e4804c4ee4424e85d6b8c4be9f68a74e6bf987f0d62cb1`，原失败不删分母。最小修复 `ed8df718452762a2eafa8080a52af9ba5dacb75c` 与 Git/发行清单最终汇合到上述 `db57d45`，原失败和新增 v2 反例已在418项通过。

- **恢复生产链**：`5bec263`→`26e5677`→`96afc05`。原独立托管者持有原 guard，启动许可先绑定唯一 SQLite；新 owner 只接受原库公钥对应的签名清理观察。准备/许可未封闭或有效签名观察缺失时不 claim 新代，不凭裸 PID 杀进程。签名有效但 cleanup/scope 不足时仍保留未决、拒绝 ready。恢复只结清失败/取消，绝不接纳旧业务结果、退款或重放 prompt。旧 v1 根不迁移；v2 自动跨代结清仅适用于明确声明并验证覆盖范围的继承进程组 profile，未证明者仍为 unknown。
- **独立真实崩溃验证**：测试 source=`89e4796d6d80edf5aa812d175c378f8602423647`，针对 `5bec263` 生产树，三项 **3/3 PASS、36.382秒、零跳过**：活跃作者、原取消意图提交后、活跃 Verifier 时只 SIGKILL 原服务进程。原证据合法结清占用后，同服务可接新 Task 并交付；第三次冷开没有重复收口/启动，原回执、预算和期限保持。日志 SHA-256=`2b9440b62a0422d808f92e8581709e076f5fc209f5674150a365b4ace40baa10`。它们使用真实 HTTP/SQLite/受管进程和确定性 ACP 子进程，不是付费模型或宿主掉电证据；后继修正已在最终418项全套再次通过。
- **真实安装目录包**：source=`ac4b13704bce72948ab3d2da03aeede960dda77d`，独立 **12/12 PASS、76.929秒、零跳过**，包含 v1/v2 的安装后 CLI→HTTP→双作者→原独立 checker→Decision/下载→CLI 退出再 open；原回执和下载 bytes 不变。生产模块全部从已核验目录包加载，只有业务 Agent 与 oracle 是外置测试夹具。日志 SHA-256=`b36d0c27e61f48e6d60af55369f5a13970582b5db7965e72e64a8733b1e1cb4b`。不是香港同包部署或 stable 发行。
- **前次完整组合失败（保留分母）**：冻结 `d6833d5` 后按 Node team workflow 原命令运行，一次 serial、37个 packages 测试文件和6个指定实验测试文件，结果 **409项／408通过／1失败、345.235秒、零跳过/取消、exit1**。唯一失败是 Pi `native-bridge.test.mjs` 的 missing/foreign/bypass 组合在 line143 期望清理为 true 却未取得该值；原日志没有标注内部具体 mode，当时先做有界诊断，现已确认上述 legacy custom 原因；未归因负载或放宽断言。三项 custody 与两项已安装团队测试均在此完整组合通过，但不报全套绿。日志保留 `/private/tmp/marshal-node-full-d6833d5.jdFAXe/results.log`，不原样重跑 full。旧 `e491339` 完整 **393/393 PASS、206.934秒、零跳过**，日志 SHA-256=`c3110480e3f27cef45ed628d03a91537d65847d2aefcad0aae66c6038b9aca96`；它不代替新树。main `0ff64ff` 远端 CI `34241562015` 成功；`e491339` Node team `34240887971` 的 Linux/macOS 均成功，不代替 ECS 实机发行。
- **Git 跨仓库交付**：`8e418f4a1bb908191b0e29a1f4f8ac6debbe4744` 仅新增解耦业务插件。两个真实锁定 worktree 产生 patch，原 cleanup 后形成候选，在另外工作区 apply/组合验收后才交付；下载后第三组工作区能消费。维护者独立审查无 P0/P1，7/7定向通过、37.325秒、零跳过。原仓库/HEAD不变，错误组合/越界改动拒绝，未知执行保留现场且不释放容量。范围仅已有跟踪文件修改，不支持新增/删除/NO_CHANGE/自动回收，不是通用 Git 服务或真实模型证据；已连同发行清单合并，真实 Pi＋Qwen 混合 Git 验收仍在途。

本轮保留的失败学习：拒绝 shell 的权限请求不能先写入“已批准额外 scope”导致永远未决；反向也不能将 failed-only 工具事件当成“未执行”。原会话/原 toolCallId 的明确拒绝只允许消费一次，复用、漂移或已启动状态继续 unknown。安装验证夹具曾错误地用普通 JSON 序列化代替原 canonical `encode`，在独立审查前修正，未改产品门禁。两类问题都通过原 producer→consumer 正反例处理，不通过重试模型修复。

**当前出口**：B1 的真实 Qwen/Pi 团队证据保持，但适用 profile 的 Worker/Publisher 分权仍未证；B2 正在关闭上述限定恢复、Git/跨仓库交付与运行中问答，局部修正、混合真实 Provider、完整同版本矩阵仍未完成。API-STABLE/B3 不升级。香港传输被本机 SIGKILL、独立身份尚缺原生模型登录，未原样重试或换通道绕过。

### 以下为已保存的历史检查点

### 22:50 CST 原执行组清理修复已同步

sourceHead=`c1dd3cde974cf032dfd185754fc9f951e3b582a6`，localMergeSha/main/origin/main=`e491339d5e5eb90d52248b9a3f644aad3beab0c5`；正常推送与远端 SHA 已核对，产品 pendingRemoteSync=false。唯一独立 reviewer 无 P0/P1，固定 Node 24.15 的 Runtime 20/20 PASS、零跳过、29.166秒。leader 单死、后代仍活时不再假报清理；存在/EPERM/其他错误或清理预算到期保持未决。完整 Node 组合另行验证，不把定向通过升级为全仓通过。

ADR 0089 已经独立审查，并由维护者按持续实施授权接纳；跨代托管与 cleanup-only 收口尚未实现，仍是当前 B2 阻塞。香港独立账号已确认无 sudo，尚缺该身份原生模型登录；源码包上传被本机 SIGKILL 的原因未明，未重传或绕过策略。B1/B2/API-STABLE/B3 状态不提升。

### 22:42 CST 真实提交边界故障已合入并推送

localMergeSha/main/origin/main=`32b353f2460415fc0b92e3ad784d02ddd1e45f35`，sourceHead=`2f6c28ffef80e181043de7d8a6871429b5dd6905`；正常推送及远端 SHA 已确认，以上代码 pendingRemoteSync=false。仅新增两份原服务 CLI 的测试/配置：create 和最终验证结果各在原 COMMIT 前、后精确 SIGKILL，再经冷 open 验证。独立 reviewer 无 P0/P1，四新例与原 crash/team 组合 **9/9 PASS、零跳过、24.514秒**；作者同组合24.409秒。不把这次定向结果拼成全仓累计通过数。

提交前，任务/回执/命令整体回滚，或 verifier 的孤儿 bytes 不获得 Decision/交付引用；提交后，原幂等回执、Decision、下载 bytes、预算和原期限完整保存。测试不改生产 hook、不手工写 SQLite、不伪造 cleanup，只在服务退出后只读检查数据库。它们证明事务边界，没有解决旧执行的自动清理收口。

下一步集中修两处同一恢复链缺口：原 Runtime 的 `cleaning`＋leader SIGKILL 不足以排除活后代，补原句柄绑定的只读组存在否决；[ADR 0089](adr/0089-node-execution-custody-and-cleanup-recovery.md) 定义独立执行托管和当前 owner 的 cleanup-only 接纳。Runtime 修复在独立工作树开发，ADR 独立审查中，均未冒充完成。

B1 的实际权限分离仍未证明；B2 已有真实必要问答和第二 Pi 团队证据，剩余运行中交互、局部修正、Git/混合任务和完整同版本恢复。API-STABLE/B3 不升级。ECS 传输阻塞状态未变化，本轮未重试上传或调用模型。

### 22:22 CST 完整整合已推送，转入恢复收口

产品 localMergeSha/main/origin/main=`8d48d9452485f6c753cd074587b2307e683e3129` 已通过正常 `git push origin main` 实际同步，并由 `ls-remote` 核对；**产品代码 pendingRemoteSync=false**。sourceHead=`ce2879fc8c8ff3b0b907e8e81217049f9e63b3a2`，同树合入main。它包含已审Qwen日期问答、Pi RPC/原生工具与显式团队驱动、完整发行依赖、tests-only取消等待修正及真实服务crash组合回归。用户已明确开发阶段review无阻塞且相关本地验证通过后直接merge/push、无需逐次确认/PR；v1正式发布后恢复PR，产品自动merge仍禁用。旧审批阻塞已解除，以下“未合入/未推送”仅保留当时状态。

验证严格按候选范围计：Pi+regional整合`c576772940760122aadabbfc8c5df9199d724730`完整Node组合 **384/384 PASS，203.500秒，零跳过**；随后仅加入`03c7b86`的3个crash测试/fixture文件，最终`ce2879f`定向 **2/2 PASS，3.075秒**。不把分次执行写成同一次386项全套。crash原reviewer另独立2/2通过6.175秒，无P0/P1；diff-check、13提交范围secret scan及merge-tree通过。真实Qwen/Pi验收仍按下文各自精确source保留，不把源码合并冒充重新跑过同一发行包。

两条crash测试真实经过CLI/HTTP/SQLite/ACP guard：自有service在started后、或原cancel fence之后cleanup之前被SIGKILL；原guard/Agent/继承后代实际退出，重开保持旧Attempt/预算/回执，无替身或退款，Task为intervention，ready拒绝接单。它们确认安全保留，同时实证暴露**缺少跨代原清理证明的合法接纳/容量释放入口**。该B2缺口不能用永久intervention或手工改库关闭；下一步补create/result事务故障组合，并设计最小跨代收口合同，保留独立证据和旧结果拒绝。

ECS诊断性原路径复测确认：本机`/usr/bin/scp`原子进程收到SIGKILL，14ms、非观察器超时、输出0B；正常权限下系统签名验证通过，发送信号方仍未知，不推测成网络或安全软件结论。原目标归档/state仍不存在，无服务/模型启动，停止重试/换通道。脱敏事实 `/private/tmp/marshal-linux-package-smoke.xXYfbtC9/scp-diagnostic.json`；需解决宿主传输终止后才可继续Linux同包安装验收。B1/B2保持IN_PROGRESS，API-STABLE/B3未完成。

### 22:07 CST Pi 正式 HTTP 团队实机通过

后续独立完整组合已结束：精确`74a239effa975b0ba66b9922b0543227a8a878f1` **374/374 PASS，182.405秒，零失败/取消/跳过，exit0**，运行后HEAD不变且clean。日志 `/private/tmp/marshal-node-combination-74a239e.750O4gfB/results.log`，SHA-256=`2703f3786d1e189ffef651fecf535b05dbe44480e74f87267e310b1a9fd397b9`。这是Pi整合候选的一次完整无模型组合，不与regional候选347项相加；覆盖下文“运行中”的历史时点。

Pi独立整合候选 sourceHead=`74a239effa975b0ba66b9922b0543227a8a878f1`（未合main）完成一次真实HTTP团队验收：Task=`task-93be382b-31e2-4bb8-8d47-8067a2de4847`，`14:06:36.307Z`→`14:07:13.371Z`，**37.064秒**。Pi0.84.4/Node24.15.0，原生planner→一次精确批准→两作者→独立固定Node verifier→下载消费→正常同版本服务实例重开；无自动重试。四个HTTP Worker均completed，两作者原生命周期交叠 **9.403秒**，三个Pi原执行cleanup均确认，独立verifier启动一次。

本次原生工具权限回调 **allowed=5、denied=1**，不是零回调握手。交付184 bytes，独立消费确认4笔/1825 cents，实际下载摘要 `sha256:ee6166dd4ce3e6516262414a5e032dd999083ae50b60825e85f4df5482cf56e8`；正常实例重开保留原create/approve回执和同字节成果、重复启动0。证据 `/private/tmp/marshal-pi-team-20260908-220700/evidence.json`，交付同目录 `regional-report.json`；执行时Git clean/HEAD核对绑定source，JSON本身不含sourceHead，不把它当签名收据。

Pi入口摘要 `sha256:5406c369954516fb56879d685e082ff9095cd6e06e41af406f394942377fd4bf`，SDK入口摘要 `sha256:82cb4ea864f3d8816c06bc8f2f2d9a8d82d883297af179dc69d287d042834844`。保留 **ordinaryUser=true、production=false、publisherSeparationProven=false**；不将正常实例重开当宿主崩溃恢复，不将第二Provider单次团队成功当完整API-STABLE。

整合仅含已审Pi修正/显式驱动、五项发行依赖和测试EOF格式修正，未带regional。定向57/58通过，唯一安装HTTP测试受loopback EPERM阻止；正常获准后原测试1/1通过4.145秒，未改断言。该精确候选完整Node组合由非Pi作者独立运行中，未预填通过。

下一项真实B2缺口已定位：当前open可保留旧执行intervention且不重派，但没有合法入口让新owner接纳崩溃后原清理证明；即使所属进程已清净，容量和ready仍可能阻断。开始补同一HTTP/SQLite/guard链的dispatch、取消crash组合测试；不以永久intervention宣称完整恢复，不以脚本改库结清，也不再新增Provider来回避该主线问题。

### 22:00 CST 真实必要问答团队交付

候选 sourceHead=`538c53494fdaf0715bd5817914c44ace46771471` 上，固定 Node24.15.0 + 原生 Qwen0.22.3 一次完成日期业务：HTTP 缺起止日期→两项必要问题→答案及完整预览→精确批准→双作者→独立验证→下载消费→正常服务实例重开。Task=`task-5fdad69d-2a5f-466a-8a0d-c8ec6a8193c1`，`13:57:42.906Z`→`13:58:04.346Z`，**21.440秒**，无自动重试。批准前启动0；作者生命周期交叠 **14.828秒**，两原执行 cleanup 已确认；3 Attempts 为两作者和 verifier，固定模板不额外调用模型规划。

公开8行输入的独立下载消费确认 east 3笔/1000 cents、west 2笔/550 cents，共5笔/1550 cents；日期外及 cancelled 行排除，退款负值与零值保留。下载507 bytes，摘要 `sha256:ecb05b7e173e8dc72b4d33a2192ccc8d075d637c5c2e70adcf6a8e14c2cf6dd3`。正常同宿主服务实例重开后原Task、成果及批准回执相同，重复启动0，不是崩溃恢复。证据 `/private/tmp/marshal-window-live.XW4ktd/evidence.json`、交付同目录 `delivery.json`；显式驱动摘要 `1c1542fb6bd05736ee95424a2be944d3811c9fcb26d71dbcc86f700e7d4ef77a` 来自实际执行记录，不是证据JSON内的签名收据。另一次只读计量核验确认两文件的字节摘要/耗时/汇总一致，不冒充重新执行原8行oracle。permission=0/0，不证明权限检查已触发；ordinary-user、production=false、publisherSeparationProven=false。

完整 Node 组合 **347/347 PASS，138.377秒，零跳过**；独立产品审查无剩余 P0/P1。保留失败成本：较早组合343/344通过，发行测试不应无配置导入服务配置入口；修正仍保留全部运行依赖，增加实际安装入口缺配置负例与 checker 执行测试。另一次独立业务包9/10因测试过早读取合法 cancelling 失败；tests-only 修正 `ef97907` 仅等待终态，仍严格断言 failed/验收失败/零成果，10/10通过、原reviewer复核通过，尚未整合，不改写上述实机源码范围。

Pi修正 `a5004a8` 原唯一P1已由原reviewer关闭，真实已安装Pi SDK/Agent Core无模型9/9通过；HTTP团队驱动 `b1d4c4` 已独立审查、18项无模型测试通过。正式Pi模型团队尚未运行；研发分支合并被安全审批拒绝，等待明确范围授权，未换Git操作绕过。

香港ECS已核验用户文档中的root管理通道与runuser。固定Node24.15.0安装于 `/opt/marshal-runtimes/node-v24.15.0-linux-x64/bin/node`，root拥有/0755，独立SHA-256=`d1de76d8edf2fededf6f8b30d244e2c0529ac607923a018283b77e9c74bd932c`；原系统Node24.18.1未改。专用执行用户仅完成无模型版本/SQLite检查，不是完整分权证明。后续25文件服务包及无模型脚本上传被安全审批拒绝：**没有上传、没有新部署目录、没有服务启动**，等待明确授权。

此时main/origin/main仍为已推送`61a19e9`；regional候选pendingRemoteSync=true、localMergeSha尚无，Pi整合亦未完成。B1/B2保持IN_PROGRESS、API-STABLE/B3未完成；还缺权限分离、第二真实Adapter、局部修正、完整同版本恢复及同资产部署/正式发行验证。

**随后授权及执行更新**：用户明确批准研发整合与限定ECS上传。Pi子任务正常审批通过，两个研发merge实际产生`e618814`/`b97aab3`，独立整合分支正在回归；root的regional研发merge仍被审批器按AGENTS“Merge默认禁用”拒绝，未绕过，已提出明确产品/研发作用域的澄清。ECS正常审批亦通过，已创建`/home/marshal-runner/node-package-smoke-a5c6b8f-20260908`（0700、UID/GID1000），但随后scp命令异常exit137；只读复核归档和state均不存在、服务/模型均未启动。具体终止原因未知，未盲目重传。以上覆盖前段“未获授权/没有目录”的历史时点，不表示部署完成。

### 21:21 CST 问答集成与同步

当前产品 main/origin/main=`6a2df5ef4c3fc2952a0975cc34d5d0008bcc9b3f`，已实际正常推送，产品代码 pendingRemoteSync=false；下文早期 pending 仅表示对应历史时点。批准前有限问答已进入正式 HTTP→Application→SQLite：缺失字段一次形成问题批次，答案产生新预览，最终按精确 digest/revision 批准；完整输入继续零问题，不增加模型调用。原答案回执、冷重开、过期/取消/CAS 竞争和旧客户端兼容均有覆盖。仍不是运行中 Worker 待答或任意自然语言澄清能力。

- 问答 sourceHead=`a90e1f4fe8292defe3f4653f8333a9d8848c512a`；集成 sourceHead=`a5c6b8fef0fdba432a4fed2d8e944cc46850eedb`，文件树与 localMergeSha=`6a2df5ef4c3fc2952a0975cc34d5d0008bcc9b3f` 相同。
- 独立审查后，完整 Node 组合 **337/337 PASS，142.343秒，零跳过**；41 个 Draft 2020-12 schema、41 个引用编译及25个示例独立验证通过。不是模型、Go 全仓或 Linux 部署验证。
- 实际发行包包含25文件、386782 bytes，已独立核验，manifest source 保持 `a5c6b8f`；摘要 `sha256:f0b99c8a70739741688c327c4ec22487c5cdff08c09f83f75b7cf2f886e415b5`，本机目录 `/private/tmp/marshal-node-b2-package.ozpAm5/package`。明确包含新增 clarification 运行依赖，不把包内摘要自行当作外部信任。
- 本次首审发现一项集成 P1：AnswerReceipt 缺示例，使既有 TaskClient 消费者测试失败；原123项定向检查未覆盖该消费者。已一次修正示例并增加合同示例检查，同 reviewer 复核关闭，完整337项覆盖客户端；保留成本，不记首审零问题。

**B1 剩余条件已具体化**：已列七项功能退出条件分别有真实团队交付/下载、启动阶段所属取消、正常服务实例重开及确定性负例支撑；当前还缺适用 profile 的 **Worker/Publisher 权限分离证据**。Mac ordinary-user 并不豁免该不变量，因此保持 IN_PROGRESS；不把全部 B2/B3 故障矩阵或第二 Provider 错加为 B1 前置。香港 ECS 本轮专用账号只读直连返回 `Permission denied (publickey)`，远端身份/Node/权限命令未执行，无新部署证据；未重试或切换身份。

当前两条作者线：Pi 原生 RPC 工具权限与所属子进程清理；真实日期区间业务的必要问答→精确预览→双作者→独立结果检查。共享 reviewer 同时核验问答集成和发行依赖。B2 仍缺实际问答业务、第二 Adapter、局部修正及完整同版本恢复；API-STABLE/B3 尚未完成。全程不使用 Marshal skill 或 Marshal 原生可执行文件。

### 21:02 CST 真实取消与安装包增量

正式源码 `0a1deedf704c7ccb6257fcb4389bc0eb44b3b66b` 的显式 `--scenario cancel` 一次实机通过：Task=`task-6e6e93b9-de46-4840-8d9b-165cdcfda6d3`，Qwen0.22.3/Node24.15.0，`13:01:30.489Z`→`13:01:48.720Z`，18.231秒，无自动重试。两个原作者实际启动后于 `13:01:48.257Z` 发HTTP取消，两个原Agent均于 `13:01:48.266Z` 观察退出且cleanup已确认；最终Task取消、取消Operation收口，verifierStarts=0、交付0。正常服务实例重开后原create/approve/cancel回执、完整Task保持，重复启动0。

这是**启动阶段的真实所属进程取消**，不是已证明模型生成或工具执行中取消；两个作者生命周期交叠仅36ms，不用它宣传计算并行收益。它补齐本次PoC的真实活跃取消子证据，不授予权限隔离、完整崩溃恢复或production。脱敏证据 `/private/tmp/marshal-qwen-cancel-20260908-210100/evidence.json`；源码来自实际运行记录，不把JSON当签名发行收据。验收驱动source=`100f61ae1e2a2a83b75ba44405bb6ea5cb225214`、localMergeSha=`0a1deedf704c7ccb6257fcb4389bc0eb44b3b66b`，独立17/17无模型测试包含真实HTTP/SQLite/ACP夹具，2.419秒。

已核验的原目录包（source=`3383e0e`、外置摘要保持下文所列）在本机实际从安装目录启动两个先后独立CLI进程：create→HTTP health/ready→SIGTERM/exit0/clean→open→health/ready→正常退出。状态根 `/private/tmp/marshal-installed-smoke-Cnyp1V/state`，无模型调用、不创建业务Task；不是Linux或安装包内模型交付。该行为已固化为自动回归，10/10发行测试通过33.758秒，独立审查通过，source=`69f0e16c3f9075257e425751af6b42eeb03675da`、localMergeSha=`abbcec4b2fc0b87d6bb372ac615288069798850f`。上述新增合入相对最近已推送 `33929c8` 仍为 pendingRemoteSync=true，后续推送结果另记；B1/B2未因此整体关闭。

### 20:16 CST 真实模型团队突破

已在正式 Node 源码 `42f95658071e9ce03d38d8926b376b26c82ffd50` 完成一次纯HTTP真实Qwen团队验收；该main已实际推送。驱动source=`f9a567c6feeba6bcc046c89e742bfc22ec5c79e1`，localMergeSha=`636c90d8cef0e0f25b9eb0077f7285ddeb4757a4`。下面 `82b64cf` 为其产品运行代码基线；后继仅增加验收驱动与文档。

- Task=`task-46eeda51-779e-41ce-af19-e689ebf6e0db`，2026-09-08 `12:15:40.285Z`→`12:16:12.905Z`，总计 **32.620秒**；本次无自动重试。
- 真实planner提出计划，经一次HTTP批准后两个作者分别生成east/west报告；实际执行重叠 **16.799秒**，各自原进程cleanup已确认。
- 独立检查器执行一次，Core接纳验收及最终交付；HTTP中4个Worker均completed（planner、两个author、verifier），不把模型end_turn当业务通过。
- 下载后独立消费者确认east为2笔/1275 cents、west为2笔/550 cents，共4笔/1825 cents。交付184 bytes，摘要 `sha256:ee6166dd4ce3e6516262414a5e032dd999083ae50b60825e85f4df5482cf56e8`。
- 同一Node宿主内正常shutdown→同版本open，重新创建HTTP/SQLite服务实例和token后，原create/approve回执与完整Task、成果字节不变，重复启动数0；不是宿主进程退出、崩溃恢复或跨版本升级证据。
- 实际安装身份为 **Qwen 0.22.3 / Node24.15.0**，入口摘要 `sha256:68cb29eb7ccc936d78ece5564ef55cae41a55b630e6657dc417c1f2e561cf4c9`。不混用较早0.23.0会话证据，不改原生模型/登录，不启动Marshal原生文件。

本地脱敏证据 `/private/tmp/marshal-qwen-team-20260908-201600/evidence.json`，交付 `/private/tmp/marshal-qwen-team-20260908-201600/regional-report.json`；私有运行库和原始日志不提交。源码身份由维护者实际运行记录关联，证据JSON本身不是含sourceHead/driverDigest的签名发行收据。本次permission回调allowed=0/denied=0，仅说明未收到询问，不能证明权限限制或交互路径生效。**Mac ordinary-user dogfood：production=false，publisherSeparationProven=false**。正式同链真实团队成功这一条件已推进；真实运行中取消、完整B1条件、B2/API-STABLE、权限分离及B3发行仍不自动关闭。

付费调用前修正了一项可避免的返工来源：不再要求planner逐字复述中文验收句，改为核对Core确定性追加的完整policy/description/layout/delivery及实际输入绑定；篡改仍拒绝，业务oracle不变。维护者11项无模型检查通过；同候选14项含HTTP团队夹具通过。首次受限工具沙箱中3项HTTP因服务不能启动失败，获批正常本机权限后同代码通过，不修改断言或隐瞒第一次失败。

当前并行：批准前有限问答与Pi正式RPC候选；Pi原生shell脱离继承进程组的清理边界尚待解决，不因本机已安装而宣称正式支持。以下为本次实机前的集成检查点，保留其时间范围。

以下保留实机前检查点：正式主线是 ADR0088 的 Node-only Task 服务，不再扩充固定订单实验。当时代码集成基线 main=`82b64cfa8d0daef0c188f0e296299b6e3aedcc48`，已包含正式 HTTP、唯一 SQLite、Task 控制/执行 reducer、受管 ACP、Supervisor、输入上传/制品下载、不可变文件物化、通用业务文件适配、独立验收/Decision/最终交付、独立 HTTP 客户端、正式服务入口与目录发行包。当时正式全链 Node 进程夹具已通过、真实模型团队尚未执行；后来的实机结果以上文为准。B1/B2 保持 IN_PROGRESS，B3 保持 PLANNED；实验双 Pi 成功与正式组件证据分开。

2026-09-08 20:03 CST 已实际将 main 从 `ba2196b` 推送至 `82b64cfa8d0daef0c188f0e296299b6e3aedcc48`；本机 main 与 origin/main 一致，以上代码 pendingRemoteSync=false。[Node team CI](https://github.com/chiga0/marshal-harness/actions/runs/34223961302)已通过，通用[CI](https://github.com/chiga0/marshal-harness/actions/runs/34223961248)最近查询仍运行中。推送/测试不等于正式发行；以下历史检查点保留原范围。

## 已合入的源码与证据

| 内容 | sourceHead | localMergeSha | 验证摘要与边界 |
| --- | --- | --- | --- |
| 受管 ACP Runtime | `506a0a99df06d09263240a0c1c5737c40f478608` | `f082e37ad1533e09c5ba55ce16d78c8519beb74f` | 独立代码审查，9 项真实 Node 子进程夹具通过；不是恶意代码沙箱 |
| 正式 Task HTTP 契约 | `58bb756ff04f8828b195f1cd728f89a1d5e50223` | 已包含于 `08ca8356d92d171014a88394e2048db1093a8e04` | 24 操作、39 schemas；10 项 HTTP 测试、39 项 Draft 2020-12 metaschema 通过；不等于24操作业务均实现 |
| Node SQLite Store | `8fd6ddcc0fd6425a43c4fd52c4abc31aebf986be` | `08ca8356d92d171014a88394e2048db1093a8e04` | 同事务、分页、owner、故障与冷重开共39项独立测试通过；无 native addon |
| HTTP→Task Application→SQLite | `d895403` | `119604c77dc44e6e3aa7b2e6e66e3f024bc0cac3` | 14项定向测试通过，含真实 loopback；规划由测试内部端口提议，未自动消费执行义务 |
| ACP Provider | `3054e86c93960e54c0320641f6a2b146ee50d923` | `c7b1400d282aea4d0ed4aeec2667c646acf22414` | 独立审查无 P0/P1，7项真实 Node 夹具测试通过；即时取消句柄、权限撤销、输出有界、等待原 cleanup |
| ArtifactDepot | `a9e485f8fa09b3172c13b33cbfc50f5708960aa5` | `cb151984b1d6cd4366fce9c7342ada79df3e81f7` | 同 reviewer 复审 P1 关闭，19项真实文件 I/O 故障测试通过；只存 CAS bytes，不自授交付/验收权威 |
| 正式 HTTP 客户端 | `73aee1c149c28d7c4861e5b2758cb96ac443329f` | `dfa0f13afb878ded6e50f271fe9f0b8a54467ac5` | 独立 review及P2修正复核通过，root实跑8项含真实loopback；没有自动批准/重试/刷新CAS |
| 持久执行及控制恢复 | `dcbcd1bc68b33e5a01ff9851a939f972f484a279` | `5f511abe0bcb511a5554d7da01fda1328d9f3471` | 一次聚合修复4个P1并由原reviewer复核关闭；root同源27/27含真实HTTP通过，Worker回合不冒充最终验收 |
| 文件物化与精确采集 | `8bb6e7ffae8bc75fb8c62cac6806519416a18bde` | `076afef53b02db4de1dad179fa72add1296a92de` | 独立审查无P0/P1，独立与维护者各9/9真实FS/Depot测试通过；仅产候选，需先核实原执行cleanup |
| HTTP输入/制品接线 | `2f4ff6936e2ce5024f0b600ae6c6fde1fefa3ebd` | `3c9eed9d700f922926bef721f7b3d58d8ab54d30` | 独立审查无P0/P1，维护者33/33 Application测试含真实HTTP通过；输入manifest/原receipt/Task摘要绑定，已提交缺失bytes不修复 |
| 自驱Supervisor及失败先fence | `ee36d7a5221b8d7dacf6a6246f8dcde3edaced90` | `71e0c1fb06314a5abf73f4f4464e5dea81691605` | 原唯一P1聚合修复，同reviewer37/37通过；cleanup等待中不再派同Task下游，其他Task继续 |
| 独立验证命令运行 | `9b474bbd6c014e6756972a119488b8635589d071` | `22d536044c557f1a650410fc1627875af23f4857` | 独立review无P0/P1，21/21真实Node运行组合通过；复用原guard，不伪造ACP end_turn或业务通过 |
| 通用业务文件适配 | `f594271df2f1a1264add304391a316dfb3b4802e` | `90591d5e37e6e6eac247f6e19efae853232b9ada` | root独立逐行审查及39/39 business/files/depot组合通过；作者受git写入限制，由root核对三文件SHA后提交；仍需Core批准布局/执行观察端口 |
| 正式 Node HTTP 服务组合 | `639d3a40b0d872f868ec4b69648200af15dafc69` | `f3e32340208c9e707192c3c0ab622b2e147cd969` | 2项恢复P1一次聚合修正并由同reviewer关闭；维护者13/13真实HTTP/SQLite通过，reviewer另有17项真实SQLite/depot断言；并非模型团队或实机部署 |

ACP Provider 和 ArtifactDepot 的定向测试、diff-check、secret scan、merge-tree 均通过后本地合入。本轮不调用 Marshal 原生二进制，不生成临时原生 checker。统计保留各包/各 source 范围，不把不同快照的测试数相加宣称完整 release 回归。

较早组合 `cb151984`：维护者使用固定 Node 24.15.0 串行运行七包（含既有 `agent-acp`，不含后来客户端）的已列测试入口，**119/119 PASS，24.92秒，零跳过**。包含真实 SQLite/文件 I/O/HTTP/所属 Node 子进程夹具，不含真实模型团队和安装部署；后来合入的执行 reducer/第二客户端不在此119项内。

后续组合 `5f511abe` 的八包验证为 **140/140 PASS，29.734秒，零跳过**。最新 `71e0c1fb` 的十包组合为 **178/179 PASS，44.906秒，零跳过**，不是全绿：ACP Provider期限测试在启动前已合法截止，但测试辅助函数无条件读取不存在的 `started.guardPid`，抛出 TypeError。已保留失败，正在将断言区分实际未启动与已启动清理；不能用增加期限或重复运行把原失败抹掉。该组合不含未合入服务入口、独立命令运行或真实模型团队。

该测试错误在 `9b474bb` 修正为：只有明确观察到立即取消/绝对期限且 `handle.started=null` 才允许没有Agent PID，仍要求原guard退出/组清理事实；正常成功仍必须有原PID。修订时一次机械替换误命中相邻测试，被本地运行的ReferenceError捕获并在提交前纠正，也计为作者错误。随后组合候选 `2bca3a26c1471022a0ddd88d4e92f98a1bdb8f4d` 实跑 **184/184 PASS，39.637秒，零跳过**，含十包加独立command入口，不含后来business/service或真实模型团队；与前次失败分别保留，不跨快照相加。

服务入口合入后的 main=`f3e32340208c9e707192c3c0ab622b2e147cd969`，维护者使用固定 Node 24.15.0 执行 `node --test --test-concurrency=1 packages/*/*.test.mjs`，**208/208 PASS，41.670秒，零跳过**。本次包含真实HTTP/SQLite/文件I/O/所属Node进程夹具及business/service，仍不包含在途最终验收、真实模型团队和部署，不升级B1/B2成熟度。

## 实机 Qwen 不再只验证握手

### 最新正式同链验证（主线 82b64cf）

| 内容 | sourceHead | localMergeSha | 证据 |
| --- | --- | --- | --- |
| 独立验收、Decision、批准布局与最终交付 | `c1800fb6064cc23557746fdfdd13b0b53e91c1e4` | `a7be7624b3d56c19290051db1b4a047604f57d28` | 同 reviewer 聚合复核，71/71真实SQLite/guard定向验证；作者不能自授验收 |
| 服务业务/验证端口组合 | `bcdf105f9a820189978f0eec2462d42bc24d7be0` | `18fb563219123b83bc70d6ec4926e22bf4a76307` | 独立审查通过，维护者18项真实HTTP验证 |
| 正式 HTTP 团队全链夹具 | `95786a974056f32dd20ba0ce390b6bd59029a12c` | `e8ffa18159d5f3336c31015e1c23b0f02ebf07b7` | 3/3：双作者实际进程重叠、独立验收/下载消费、错误成果拒绝、取消与正常重启；不是模型 |
| 确定性目录发行包 | `75316b49bbf42e28147ecf02ac46d60c2ede07be` | `659e18366118b8a28b4d5d20e6df08089fa92334` | 独立9/9及额外fsync故障断言；外置摘要核验，不自授发行信任 |
| CI全包覆盖及发行依赖接线 | `3383e0ecdc6825aa58148df4bb4e1d114046cb7b` | `82b64cfa8d0daef0c188f0e296299b6e3aedcc48` | 独立审查通过；该source与merge文件树完全一致 |

维护者在 `3383e0e` 实跑正式包测试及原六组 Node 实验回归：**307/307 PASS，128.174秒，零跳过**。命令为 `node --test --test-concurrency=1 packages/*/*.test.mjs experiments/node-team/providers.test.mjs experiments/node-team/business.test.mjs experiments/node-team/runtime.test.mjs experiments/node-team/http-handler.test.mjs experiments/node-team/http.test.mjs experiments/node-team/openapi.test.mjs`。包含真实HTTP、SQLite、文件和所属Node子进程，不含真实模型、Go全仓或生产部署。

同一 source 实际打包并独立核验：24个文件、361336 bytes，manifestDigest=`sha256:735347c6aaa64197e516af78e2ae92b527afe6f284a71c78fe7bd36cd9eceb8f`，本机目录 `/private/tmp/marshal-node-candidate.vbh1cf/package`。manifest 仍准确绑定 `3383e0e`，不倒填 merge SHA；该包不是已签名/stable/实机部署验收产物。

Core 验收发生两轮实际P1修正：第一轮补足失败验收证据并纠正虚假的首审统计；第二轮修正合法未启动路径被当作缺失验收receipt而导致Supervisor失败和容量未释放。最终 `c1800fb` 用真实guard的 no-start事实覆盖该路径；此前测试遗漏和返工成本保留，不把最后71项通过写成首轮通过。

### 较早单会话工具证据

维护者使用上述受管 Runtime、固定 Node 24.15.0、本机 Qwen Code 0.23.0 的原生 `--acp`，在独立目录读取公开 `input.txt`：部门甲17、乙29、丙36以及校验标记。未禁用全部原生工具、未复制登录数据、未偷偷改模型。

一次真实 prompt 返回 `end_turn`；观察到94项更新、1次原生工具调用，独立检查返回包含正确合计82与原标记。permission 请求数为0，只表明这次读取未触发询问，不能据此宣称真实问答已通过。Runtime 执行身份=`5e17942d-c134-4f7b-a313-20bd9fc6be32`，实际开始 `2026-09-08T09:40:49.112Z`，Agent 退出观察 `09:41:00.010Z`，guard 退出观察 `09:41:00.311Z`，`cleaned:true/reason:owner_stop`，stderr 0。该调用已结束，不是仍在后台运行。

这关闭“真实会话能否调用工具并返回结果”的疑点，不关闭完整 HTTP Task、双作者、独立业务 Decision、取消问答或重启恢复。此前固定样例的真实双 Qwen 业务失败继续计入成本，详见[原失败记录](qwen-integration-status-2026-09-08.md)。

## API 与后续关键路径

机器合同为 [正式 OpenAPI](../packages/task-api/openapi.json)，不是旧 `experiments/node-team/openapi.json`。main 当前17项真实 Application 操作：Task 创建/列表/详情/计划/确认/图/取消/暂停/恢复/事件/审计/Workers、Worker详情、Operation 查询，以及输入上传、制品详情和内容下载。服务组合另提供 health/ready/providers/supervisor 4项运行观察；问答与单Worker取消仍不能因路由已定义就计作完成。输入ready只表示原始输入可读，不表示最终业务成果已经验收。

执行 reducer 原稿 `5a5079d` 的23项测试没有捕获4个实际P1：较低计划期限未落实、历史完整输入导致合法大计划事务溢出、重启后旧取消Operation无法回填、进度推进用户控制CAS。已一次聚合修正为 `dcbcd1b`，27项通过并独立复核合入；其中合法64节点、每goal 8000字节全部完成，不增加Store限额。回合完成仍不等于业务验收。

接下来的并行工作是：正式HTTP真实Qwen规划→一次批准→双作者→独立检查/下载消费；B2按ADR0086/0088实现批准前有限关键问答；准备同资产Linux服务验证。共享Application由单一作者修改，验收驱动与发行检查不写其scope。

真实Qwen团队驱动是待执行的验收工具，不是新增成功证据。仅按 Mac ordinary-user dogfood 使用已有原生登录；权限回调不是OS隔离，Worker/Publisher凭证分离仍未证明，不能宣称production。问答、单Worker取消、第二Adapter、完整恢复及声明平台发行仍有未完成项。

部署盘点已完成：旧 `scripts/install.sh` 会走 Go，旧 release workflow 面向 RC1/native，不能拿来发行 Node stable。新的 Node CI 已纳入正式包路径及全部包测试；远端执行结果另行核对，不拿本地测试替代。发行目录及外置摘要核验已实现，安装后同版本恢复和声明平台实机验证继续推进。

## 本轮效率教训

- 2026-09-08 用户再次指出：子任务结束后没有立即补派造成空槽。已将“每次完成/退出/失败都检查并补派，不能补派则记录具体原因”写入仓库 `AGENTS.md`，作为后续会话入口规则；不是新审批流程。修正后已实际并行派发 Supervisor 聚合修复、Node 服务组合根实施和制品审查，主 Agent 实现 Application 上传/下载接线。
- 有空闲槽不等于任务仍在运行；已结束子 Agent 需要明确续派。开发、独立验证、下一步部署设计可交错，不要等全部组件完成才考虑组合入口。
- Artifact 首稿再次出现此前 Store 已暴露的“首次同步失败后 Open 绕过耐久屏障”。这是重复失误，不应包装成新架构成果。已在原包一次修正 Create/Open 共用屏障，并覆盖失败→再次失败→成功重开，不新建微型治理系统。
- 执行首审4项P1也计入失败成本：只测小图/单次进度/新owner的执行拒绝，不足以证明完整合同。已将最大合法图、降低期限、进度与用户控制组合、旧控制义务重启收口加入原包；首审不是零错误，不因最后全绿删除返工事实。
- 共享状态机只有一个作者；客户端、Provider、制品和控制器以明确端口并行。下一指标是真实用户任务结束并可消费成果，不是再增加 schema 或测试包数量。
