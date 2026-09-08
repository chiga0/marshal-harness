# Node 服务脚本包：打包与核验候选

这是 ADR 0088 §6 的 **same-bytes 发行前置工具**，不是 stable 发布、签名、部署或业务验收。产物是可搬运的确定性目录树，不包含新的原生启动器、Node runtime、native addon、npm 依赖、Agent 登录或服务配置。当前固定运行时为 Node `24.15.0`；manifest 中的 Darwin arm64 / Linux x64 是后续验收目标，不代表两平台已经通过。

## 使用

从受信任、已提交且独立审查的源码使用固定 Node 调用：

```sh
node packages/task-distribution/main.mjs pack \
  --source /absolute/canonical/source \
  --source-head FULL_40_HEX_COMMIT \
  --target /absolute/canonical/new-package

node packages/task-distribution/main.mjs verify \
  --root /absolute/canonical/new-package \
  --manifest-digest sha256:TRUSTED_DIGEST_FROM_PACK_OUTPUT
```

`pack` 要求 Git 可用，仅用只读 `rev-parse / ls-tree / show`；源文件必须与给定 commit 精确一致。它只复制 `SOURCE_FILES` 显式清单，包括 HTTP/OpenAPI、Application/Store、Supervisor、ACP/受管执行、制品/文件业务、客户端、命令验证与服务入口。新增正式运行文件会导致 `source_inventory_changed`，必须复核后显式补清单；不扫描并发布任意本地文件。测试、fixture、README、发行工具自身不属于服务运行依赖，均不打入包。源内用户配置或未提交内容不会被复制。

`verify` 不依赖 Git，不执行包内代码；工具本身应来自受信任发行端。**必须在包外保存和传递 `manifestDigest`**，不得从下载目录计算摘要后自行信任，否则任何人重写源码和 manifest 都能自证。摘要证明精确 bytes，不代替签名、受保护源码来源、业务正确性或发布授权。

本清单锁定 `bdbd1fdd3b58593fca3d9d055d068fb024fec2de` 的 23 个运行文件；不提前接纳未合并的后继实现。更新时先在独立工作区锁定已接纳的新 sourceHead，读取新增运行文件及其 import/静态资源链，显式修改 `SOURCE_FILES` 并运行本包测试及新目录完整冷导入，再经独立审查。不要忽略 `source_inventory_changed` 或用目录通配符绕过它。核验端的工具版本和清单必须匹配发行候选；旧核验端不会自动信任新清单。

目录包不依赖归档时间戳、压缩器或平台排序：manifest 固定 UTF-8 JSON 序列化、文件顺序、精确源码摘要/长度、sourceHead、Node 版本和入口。源码和 manifest 同样输入产生同样 bytes；权限为目录 `0700`、文件 `0600`，时间戳不进入内容合同。单文件上限 2 MiB、总计 16 MiB。打包创建新目录，不覆盖已有目标；写入及目录同步失败保留现场，不自动清理/修复。核验拒绝缺失、多余（含空目录）、链接、路径逃逸、重复别名、截断与摘要漂移。

核验完成后，在目标目录通过已允许的 Node 运行：

```sh
node packages/task-service/main.mjs \
  --config /absolute/trusted/service-config.mjs \
  --root /absolute/new-service-state --mode create --port 0
```

配置是部署者单独提供的受信任模块，不来自 HTTP 或 Worker；必须接入正式服务要求的 Provider、业务 prepare/collect 或 factory、独立验证等能力。工具不生成空配置假装服务可用，不复制 HOME/凭据，不启动 Agent。安装路径不能在核验后被不可信者改写：这里没有敌对同 UID 沙箱，也没有运行时逐次加载 attestation。

## 定向证据与剩余出口

```sh
node --test packages/task-distribution/index.test.mjs
```

测试使用真实私有文件目录、独立 Git fixture 和固定 Node：两次确定性打包、冷目录导入全部正式模块、服务入口缺配置诚实拒绝、源码/manifest/文件漂移与新增依赖、缺失/多余/链接、目标覆盖拒绝、CLI 错误脱敏。不运行模型或原生 Marshal。

本测试不是完整启动部署：待主线精确候选收口后，维护者须在 Darwin/Linux 对同一包完成真实安装、HTTP 业务交付与下载、取消、整个服务重启/恢复、连续多 Task、备份与升级验证，并另外提供受保护发行来源。通过这些出口前不称 `API-STABLE`、B3 完成或 production/stable。
