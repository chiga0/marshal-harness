# Node 服务目录包：核验、启动与支持边界

这是 [ADR 0088 §6](../../docs/adr/0088-node-task-service-production-projection.md) 的 **same-bytes 发行前置工具**，不是 stable 发布或平台支持凭证。产物是确定性目录树，包含清单内的服务模块及显式业务配置入口，不包含 Node runtime、新原生启动器、native addon、第三方 npm 依赖、Agent 本体、用户登录或用户部署配置。目标机须已有合法可执行的固定 Node `24.15.0`，以及所选 Provider 的原生安装和授权；工具不会安装它们。

本文核对基线为 `0f86804140546b9e4593ba39d1a98dcb70db18bc`，该版本 [SOURCE_FILES](index.mjs) 为 **47 个运行文件**。具体候选始终以其配套清单及核验输出为准，不再使用旧 23 文件清单。manifest 中的 `darwin-arm64 / linux-x64` 是声明的验收目标，不代表两平台同资产部署已通过；当前完成状态见[主线实证](../../docs/node-task-service-status-2026-09-08.md)。

## 1. 从受信任源码生成目录包

以下路径占位符须换成实际绝对、canonical 路径；使用已独立审查的固定源码和配套核验工具。先检查 Node 输出必须为 `v24.15.0`，不匹配则停止，不以系统 PATH 中的另一版本代替。

```sh
MARSHAL_NODE=/absolute/node-24.15.0/bin/node
MARSHAL_SOURCE=/absolute/trusted/source
MARSHAL_PACKAGE=/absolute/canonical/new-package
MARSHAL_TOOLS="$MARSHAL_SOURCE/packages/task-distribution/main.mjs"
"$MARSHAL_NODE" --version

"$MARSHAL_NODE" "$MARSHAL_TOOLS" pack \
  --source "$MARSHAL_SOURCE" \
  --source-head FULL_40_HEX_REVIEWED_COMMIT \
  --target "$MARSHAL_PACKAGE"
```

`MARSHAL_PACKAGE` 必须尚不存在，父目录已存在且 canonical，目标在源码目录之外。`pack` 要求 Git 可用，仅执行只读 `rev-parse / ls-tree / show`；每个清单文件必须与给定 commit 精确一致，清单内未提交的内容修改会被拒绝。输出包含 `sourceHead / manifestDigest / files / bytes`，应作为此次候选记录保存在包外。

显式清单覆盖 HTTP/OpenAPI、Application/SQLite、Supervisor、ACP/Pi/受管执行、制品、文件/Git 业务、区域汇总业务、客户端、独立命令验证和简启动。测试、fixture、README、发行工具自身不打入包；不会复制任意用户配置或目录。`source_inventory_changed` 表示运行依赖清单不匹配，必须审查真实 import/静态资源并更新配套工具，不能忽略或改成通配符。旧核验工具不会自动信任新清单。

目录权限为 `0700`，文件为 `0600`；单文件上限 2 MiB、总计 16 MiB。manifest 固定记录源码摘要/长度、顺序、sourceHead、Node 版本和入口，时间戳不进入内容合同。创建或同步失败保留现场，不能覆盖失败目标重来；另选新目标前先保存失败证据。

## 2. 安装后先核验，再运行包内入口

把目录包交付到目标机后，保留原树、权限及独立保护的 `manifestDigest`，以目标机上受信任、与该候选匹配的工具核验。工具不在目录包内；目标端的 `MARSHAL_TOOLS` 不能指向未经核验下载中的任意程序。`verify` 本身不依赖 Git，也不执行包内代码。

```sh
MARSHAL_NODE=/absolute/target/node-24.15.0/bin/node
MARSHAL_TOOLS=/absolute/trusted/matching-source/packages/task-distribution/main.mjs
MARSHAL_PACKAGE=/absolute/canonical/installed-package
MARSHAL_PACKAGE_DIGEST=sha256:TRUSTED_DIGEST_FROM_RELEASE_EVIDENCE
"$MARSHAL_NODE" --version
"$MARSHAL_NODE" "$MARSHAL_TOOLS" verify \
  --root "$MARSHAL_PACKAGE" \
  --manifest-digest "$MARSHAL_PACKAGE_DIGEST"
```

必须核对成功输出的 `sourceHead / manifestDigest / node / entrypoint / files / bytes` 与候选记录一致。**不能从下载目录重算 manifest 摘要后自行信任**；摘要只绑定 bytes，不代替受保护来源、签名、业务正确性或发布授权。核验拒绝缺失、多余文件或空目录、链接、路径逃逸、重复别名、截断、权限及摘要漂移。核验失败停止，不修补原包、不放宽权限凑通过。

核验后的包保持只读使用；状态、日志、配置及临时输出放在包外，否则多余文件会破坏下一次核验。这里没有敌对同 UID 沙箱或逐次加载 attestation，部署者仍须保证核验后的代码不被不可信者替换。

服务仍需显式受信任 `--config`，不会猜 Provider、模型、登录或业务。配置必须从此次安装包引用运行模块，接入真实 Provider、业务 `prepare/collect` 或 factory、独立验证等能力；不要混入另一个源码 checkout 的 Core。通用配置接口见[服务 README](../task-service/README.md)。

```sh
"$MARSHAL_NODE" "$MARSHAL_PACKAGE/packages/task-service/main.mjs" \
  --config /absolute/trusted/service-config.mjs
```

已有可直接选择的窄业务配置是 `regional-paid-window/v1`，不是通用自然语言业务默认值。它要求当前执行用户已有 Qwen Code 原生安装/模型授权，`MARSHAL_QWEN_ENTRY` 指向其真实 `cli-entry.js`；不复制 HOME/登录、不提供默认账号或模型：

```sh
MARSHAL_QWEN_ENTRY=/absolute/canonical/qwen-code/cli-entry.js \
"$MARSHAL_NODE" "$MARSHAL_PACKAGE/packages/task-service/main.mjs" \
  --config "$MARSHAL_PACKAGE/packages/task-regional-window/service-config.mjs"
```

该配置接入原 Qwen `--acp`、必要日期问答、有限计划和独立汇总 oracle，具体输入与确认/下载消费步骤见[区域汇总业务说明](../task-regional-window/README.md)。仅启动服务不代表已调用模型或已完成业务；没有准备好该业务/授权时不要使用无模型 fixture 冒充正式配置。Worker 可达 Publisher 凭据或已登录发布入口的配置不在支持范围，`publication:none` 和删环境变量不等于已证明分权。

## 3. 数据根、停止与正常重开

默认数据根是当前用户 `HOME/.marshal-node/task-service`，与安装包及旧 `.marshal` 分开。缺失的私有父目录由简启动按边界建立，服务根由原 composition 创建；HOME 只作路径元数据锚点，不扫描或修改登录/权限。省略 `--mode` 为 `auto`：根不存在才创建，根已存在只尝试原格式打开，不因打开失败切换为创建。

需要固定另一位置时，追加 `--data-dir /absolute/private-parent/task-data`；旧 `--root ... --mode create|open --port 0` 保持兼容，`--root` 与 `--data-dir` 互斥。显式路径要求当前 UID/canonical/`0700` 私有锚点、受限祖先检查与同步，详见[简启动边界](../task-service/README.md#一条命令启动)。空根、部分初始化、未知或损坏格式、链接、宽权限/非所属目录以及已有 owner 都拒绝，不重置、不 chmod、不删锁、不导入旧 Go/实验根。不要预先创建一个空服务根供 `auto` 初始化。

默认只监听 `127.0.0.1` 的动态端口；启动输出为 `profile / address / connectionFile`，不是公开 token。客户端在同一受控身份下读取 `0600` 连接文件，保留认证与 Host/Origin 校验，不把 token 放到命令历史、日志或 Agent prompt。

前台原进程使用 Ctrl-C，或由持有原进程句柄的启动者发送 SIGTERM；等待其退出和 shutdown 的 `clean=true`，不是只观察 HTTP 消失。随后以同一包、配置、数据目录再次运行上述启动命令，即打开原根。每次生成新连接文件/token；客户端使用新连接，按原 Task ID、原请求 key/正文精确重放，不能重新创建任务或刷新预算冒充恢复。未知清理或启动恢复拒绝时保留现场，不自动重试接管。

**冷备份/恢复与升级尚待同资产验收**：正常停止后原地 `open` 的证据不等于备份副本可恢复，不提供已验证的热备份、跨机复制根或就地升级承诺。不要只复制运行中的 `authority.sqlite`、遗漏制品/执行/托管事实，或修改格式标记让新配置接管旧根。声明备份或升级支持前，须独立验证完整一致数据、原回执/制品/预算及失败处置；不把手工删锁、重建库作为恢复方法。

## 4. 工具测试、安装证据与正式发布分开

从源码工作区使用固定 Node 执行相关无模型测试：

```sh
"$MARSHAL_NODE" --test --test-concurrency=1 \
  packages/task-distribution/index.test.mjs \
  packages/task-distribution/carrier.test.mjs \
  packages/task-distribution/team.test.mjs
```

`index.test.mjs` 包含10项：same-bytes、冷导入、缺配置拒绝、源码/清单/权限/链接漂移及真实安装 CLI 的 HTTP health/ready→正常退出→新进程打开原 SQLite。`team.test.mjs` 包含 v1/v2-custody 两例：生产模块与客户端均来自核验后的目录包，真实 CLI/HTTP/SQLite/受管 ACP 进程、两作者交叠、独立 checker、下载消费和冷重开原回执/bytes；业务 Agent/checker 是明确外置夹具，不调用模型。它们不是香港同包部署、真实模型验收或所有数据 layout 的安装验证，精确通过记录仍以对应 source 的实证为准。

### 同一 CI 候选的两平台消费

`node-team.yml` 在原回归成功后，由一个 producer 对精确 workflow source **只 pack 一次**。同一 workflow 的 Linux/macOS 普通 runner 按 producer 输出的唯一 artifact ID 下载，使用包外的 `sourceHead / manifestDigest` 核验；消费者不会自行生成摘要后信任，也不重新 pack。核验工具与外置夹具从同一精确 source checkout 获得，运行用的 Core、CLI 和客户端只来自恢复后的包。

目录 artifact 可能把权限变成 `0755/0644`，因此下载目录只作 carrier，不直接运行、不原地 chmod。候选辅助命令在完整原清单、无链接/硬链接、类型、大小及全部摘要检查成功后，才向当前用户所属且 `0700` 的已有父目录下独占创建新安装树，写 `0700/0600`、逐层同步并调用原严格 `verify`。不覆盖既有目标；同步失败保留现场并拒绝成功。没有新增包格式或发布权威。

```sh
"$MARSHAL_NODE" "$MARSHAL_TOOLS" restore-carrier \
  --carrier /absolute/downloaded-carrier \
  --target /absolute/private-parent/new-package \
  --manifest-digest "$MARSHAL_PACKAGE_DIGEST" \
  --source-head FULL_40_HEX_PRODUCER_COMMIT
```

共享 `installed-team.fixture.mjs` 保留原两种 layout 的完整团队消费者；显式 `candidate-consumer.mjs` 必须收到 `MARSHAL_CANDIDATE_ROOT / MARSHAL_CANDIDATE_MANIFEST / MARSHAL_CANDIDATE_SOURCE`，CI 另记录原 artifact ID。缺参失败，无 pack fallback。两例须实际 pass、零跳过，并在原进程 pipe 排空后确认 `clean=true / exit=0`。`carrier.test.mjs` 另覆盖传输权限、精确摘要、无 Git 恢复、非法载荷（含非阻塞拒绝 FIFO manifest）、既有目标、同步失败及父目录替换。

此 workflow 仍只有 `contents:read`：不创建 tag/release，不申请 OIDC 或发布写权限，不接触模型登录。CI artifact 是有限保留期的测试候选，不是受保护 stable 资产；两平台实际结果、香港部署和真实模型验收仍分别记录，不能由新增 workflow 文本预填通过。

[API-STABLE 四条件](../../docs/agent-team-service-milestones.md#api-stable核心接口稳定检查点不是所有扩展齐备)是：合同/handler/示例/客户端一致且无未处置核心 API P0/P1；至少一个支持的真实 Provider 完成纯 HTTP 确认/交付/下载审计和主要失败控制；已提供功能的幂等、旧版本、认证/Origin、路径、取消迟到、事件续读/gap 等反例通过；HTTP 脚本及独立客户端共用契约，查询/取消在并行与长 Verify 下有实测响应上界。**不要求 B3 全平台部署与完整长期故障矩阵提前完成**，但本工具或安装测试也不自动授予 API-STABLE。

B3 另要求声明平台/profile 的同一待发布资产完成实际业务、取消/故障恢复、长期多 Task、备份与升级验收、受保护来源及 same-bytes stable release。Darwin 与 Linux 分别取证；原生资产才按类别适用签名/notarization，纯脚本不伪称 Apple 公证，也不豁免运行时合法性与安装验证。当前不声明 Linux 部署、B3 完成、production 或 stable；不以标签、包核验或一次安装测试替代这些出口。
