# Node v1.0.2 下载部署

> [v1.0.2 已发布](https://github.com/chiga0/marshal-harness/releases/tag/v1.0.2)，OSS 六项资产已同步并公开下载验签通过。实际证据见 [v1.0.2 发行记录](v1.0.2-release-dossier-2026-09-10.md)。旧 v1.0.0/v1.0.1 的字节和安装不变。

本入口安装 v1.0.2 的 Node Agent Team 文件，不安装历史 Go CLI，不编译或执行 Marshal 原生二进制，不请求 sudo，不修改系统安全设置。精确证据与发布状态见 [v1.0.2 发行记录](v1.0.2-release-dossier-2026-09-10.md)。

## 一键安装

支持平台为 macOS Apple Silicon（`darwin-arm64`）和 Linux x64（`linux-x64`）。前置工具：Node `>=22`、Python 3、`curl` 和 `minisign`（离线模式无需 curl）；服务启动还会检查实际 SQLite 能力。已验证 Node22.22.1/24.15.0，不声称所有未来版本已经测试。Node22 缺少 `defensive` 时明确警告，不开放任意SQL；细节见 [ADR0097](adr/0097-node-capability-based-runtime-admission.md)。默认下载走杭州 OSS；显式镜像模式只访问指定 HTTPS 目录，离线模式不联网。缺工具时安装器给出错误，不自动安装系统软件。

```sh
(
  set -eu
  setup_dir="$(mktemp -d)"
  curl -q -fL --proto '=https' --proto-redir '=https' \
    --connect-timeout 15 --max-time 120 --retry 2 --retry-all-errors \
    https://github-releases.oss-cn-hangzhou.aliyuncs.com/marshal-harness/v1.0.2/install-node.sh \
    -o "$setup_dir/install-node.sh"
  bash "$setup_dir/install-node.sh"
)
```

默认安装到 `$HOME/.local/share/marshal-node/v1.0.2`，不改 PATH。需要部署到 Sandbox 的持久化挂载盘时：

```sh
bash /absolute/downloaded/install-node.sh --prefix /absolute/private-parent/marshal-v1.0.2
```

安装目标必须是新目录，已有安装或失败现场不会被覆盖；自定义目标的父目录必须已存在、属于当前用户且权限为 `0700`，不跟随符号链接。脚本不自动放宽或修改既有目录的权限。需要先审阅脚本时，可从仓库下载 `scripts/install-node.sh`，检查后执行。本次载荷精确固定为 v1.0.2，旧安装不被改写；OSS 版本目录禁止覆盖。GitHub 继续保留原发行资产，但安装器不隐式回退 GitHub。

## 范围

GitHub 网络不稳定时可使用 [OSS 镜像或完整离线安装](node-oss-distribution.md)。发布者需先配置并完成镜像同步；不能把尚未上传的示例地址当成可用入口。

- 新入口只安装冻结的 v1.0.2，不自动选择 latest、不回退源码编译。
- Qwen/Pi、模型登录、Skill 和业务配置由部署者提供，本脚本不读取或复制凭据。
- 安装完成不等于服务已运行。当前服务仍要求显式业务配置；缺配置时不启动一个假成功服务。
- 只适用于可信单用户部署，不提供恶意代码隔离。Sandbox 中安装目录与服务数据目录应选择平台提供的持久化存储；环境销毁后不能依赖临时磁盘恢复。

## 身份与核验

脚本使用仓库固定的维护者 minisign 公钥核验 `SHA256SUMS`，并核对冻结版本的来源与资产摘要。不把随下载文件附带的任意公钥作为新的信任根。签名口径见 [ADR0096](adr/0096-node-stable-asset-signing-minisign.md)。

| 身份 | v1.0.2 |
| --- | --- |
| sourceHead | `f9a93cd678cac40bcd04ff9d0c1672612f184701` |
| ZIP SHA-256 | `be4a8b6198769f1199b09d69faed91301ca99bc1632e2742fb174f764dc43e12` |
| manifest SHA-256 | `94b2a036ea4b61e869ee0fd02e9b5a257db88b820c8677f5f09573c7320ee8d3` |

安装器和 Node 解释器本身属于部署者信任的执行工具。企业策略仍可能限制 Node、网络或 shell；脚本不绕过此类限制。发行包不包含 Node runtime。

## 启动与数据

### 产品内初始化与统一命令

v1.0.2 包含 `packages/task-local/main.mjs`，在验签恢复成功后自动执行 `init`：从安装位置识别自身，发现 PATH 中的 Qwen/Pi/OpenCode，保存本机设置并安装 `~/.local/bin/marshal` 文本启动器。它调用已用来安装的 Node，不创建或执行临时原生二进制；不修改 Agent 登录、Shell 配置或现有同名命令。旧 v1.0.1 不包含此模块。

```sh
~/.local/bin/marshal init
~/.local/bin/marshal serve --config /absolute/trusted/config.mjs
# 之后可复用已记录配置，无需再次填写连接文件或环境变量：
~/.local/bin/marshal status
~/.local/bin/marshal serve
```

`init` 可重复执行；同名命令冲突时保留原程序，并使用安装根下的 `node packages/task-local/main.mjs` 等价入口。安装成功但初始化失败时会单独提示，已验签安装资产不会删除。`serve` 前台运行，连接已就绪时直接复用；不自动关闭旧进程或清空状态。具体行为见[本地命令说明](../packages/task-local/README.md)。

**尚未完成的接缝**：检测到可执行文件还不等于完成 Adapter/通用业务装配，目前首次服务启动仍须受信配置。通用独立验收及原生 Skill 的工具/外部效果支持不能靠放宽文件权限或复用登录替代。本候选没有证明 DataWorks 发布与补数已经可运行。

完成安装、准备好实际业务配置后，通过安装器输出的固定 Node 入口运行服务；启动参数及配置接口见 [服务说明](../packages/task-service/README.md)。可以指定 `--data-dir` 和 `--port`，默认仅监听 `127.0.0.1`，不自动暴露公网端口。

服务配置、数据库、日志及运行制品均放在发行包外。不要修改发行文件、把数据写进安装树、删除未知锁或用重新初始化代替恢复。停止并重开沿用同一配置和数据目录；不将本次安装能力扩大为跨版本数据迁移承诺。

### 可选本机浏览器 UI（随包预览，默认关闭）

> 本节描述可选 UI 装配能力（[ADR0098](adr/0098-local-browser-ui-boundary.md) 与 [UI-1 设计包](ui-1/README.md)）。本次 v1.0.2 发行包没有构建后的 UI 资产，不能据此使用以下 UI 示例；只有明确携带并核验 UI 资产的后继包才适用。

发行包如包含 `apps/task-web/dist`（与运行文件同一 manifest 核验、同一安装树、同一权限检查），可显式开启同源浏览器 UI：

```sh
node <安装目录>/packages/task-service/main.mjs \
  --config /absolute/trusted/service-config.mjs \
  --data-dir /absolute/private-parent/task-data \
  --ui <安装目录>/apps/task-web/dist
```

- `--ui` 不放在安装树内数据目录外的其他位置；目录缺失、含符号链接、缺 `index.html`、含未知扩展名或非常规文件时启动失败且不留数据现场。启动输出中的地址（`http://127.0.0.1:<端口>`）即唯一浏览器入口，打开 `http://127.0.0.1:<端口>/ui/` 使用；token 仍从本次启动的私有连接文件读取，只输入页面内存，不写 URL/存储/日志。
- 静态全部为只读 GET/HEAD；HTML 不缓存、content-hash 资产可长期缓存；不种 cookie、不启用 CORS、不绑定 `127.0.0.1` 之外的地址。升级后旧浏览器标签页必须整体刷新再连接，不混用旧 HTML 与新包资产。
- **关闭**：去掉 `--ui` 参数重新启动同一配置与数据目录即回到 API-only；`/ui/` 恢复 404，非空 Origin 一律拒绝，旧 Node 客户端行为不变。未启用时私有连接文件中的 `{url,token}` 及全部 CLI 用法与v1.0.2 完全一致。
- 服务数据根（`store`/SQLite）与 `--ui` 无关：开/关 UI 不改变任务、回执与恢复事实，也不会向浏览器暴露 SQLite 或宿主任意文件。
