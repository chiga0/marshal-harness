# Node v1.0.1 下载部署

> [v1.0.1 已发布](https://github.com/chiga0/marshal-harness/releases/tag/v1.0.1)，支持 Node22 及以上按实际 SQLite 能力运行；旧 v1.0.0 的字节和安装不变。

本入口安装 v1.0.1 的 Node Agent Team 文件，不安装历史 Go CLI，不编译或执行 Marshal 原生二进制，不请求 sudo，不修改系统安全设置。精确证据与发布状态见 [v1.0.1 发行记录](v1.0.1-release-dossier-2026-09-10.md)。

## 一键安装

支持平台为 macOS Apple Silicon（`darwin-arm64`）和 Linux x64（`linux-x64`）。前置工具：Node `>=22`、Python 3、`curl` 和 `minisign`（离线模式无需 curl）；服务启动还会检查实际 SQLite 能力。已验证 Node22.22.1/24.15.0，不声称所有未来版本已经测试。Node22 缺少 `defensive` 时明确警告，不开放任意SQL；细节见 [ADR0097](adr/0097-node-capability-based-runtime-admission.md)。默认下载走杭州 OSS；显式镜像模式只访问指定 HTTPS 目录，离线模式不联网。缺工具时安装器给出错误，不自动安装系统软件。

```sh
(
  set -eu
  setup_dir="$(mktemp -d)"
  curl -q -fL --proto '=https' --proto-redir '=https' \
    --connect-timeout 15 --max-time 120 --retry 2 --retry-all-errors \
    https://github-releases.oss-cn-hangzhou.aliyuncs.com/marshal-harness/v1.0.1/install-node.sh \
    -o "$setup_dir/install-node.sh"
  bash "$setup_dir/install-node.sh"
)
```

默认安装到 `$HOME/.local/share/marshal-node/v1.0.1`，不改 PATH。需要部署到 Sandbox 的持久化挂载盘时：

```sh
bash /absolute/downloaded/install-node.sh --prefix /absolute/private-parent/marshal-v1.0.1
```

安装目标必须是新目录，已有安装或失败现场不会被覆盖；自定义目标的父目录必须已存在、属于当前用户且权限为 `0700`，不跟随符号链接。脚本不自动放宽或修改既有目录的权限。需要先审阅脚本时，可从仓库下载 `scripts/install-node.sh`，检查后执行。本次载荷精确固定为 v1.0.1，旧安装不被改写；OSS 版本目录禁止覆盖。GitHub 继续保留原发行资产，但安装器不隐式回退 GitHub。

## 范围

GitHub 网络不稳定时可使用 [OSS 镜像或完整离线安装](node-oss-distribution.md)。发布者需先配置并完成镜像同步；不能把尚未上传的示例地址当成可用入口。

- 新入口只安装冻结的 v1.0.1，不自动选择 latest、不回退源码编译。
- Qwen/Pi、模型登录、Skill 和业务配置由部署者提供，本脚本不读取或复制凭据。
- 安装完成不等于服务已运行。当前服务仍要求显式业务配置；缺配置时不启动一个假成功服务。
- 只适用于可信单用户部署，不提供恶意代码隔离。Sandbox 中安装目录与服务数据目录应选择平台提供的持久化存储；环境销毁后不能依赖临时磁盘恢复。

## 身份与核验

脚本使用仓库固定的维护者 minisign 公钥核验 `SHA256SUMS`，并核对冻结版本的来源与资产摘要。不把随下载文件附带的任意公钥作为新的信任根。签名口径见 [ADR0096](adr/0096-node-stable-asset-signing-minisign.md)。

| 身份 | v1.0.1 |
| --- | --- |
| sourceHead | `b90d7e7247a690db2af740078c285331569aa496` |
| ZIP SHA-256 | `a94f53073c96f813a7fbd24edc15a77c32130329a3fbef877d8371e9ec17a2a1` |
| manifest SHA-256 | `10c747d2f25dce6c085a736c2ed3e55f19ed9f0517e25a3b8e8561f08f9240f7` |

安装器和 Node 解释器本身属于部署者信任的执行工具。企业策略仍可能限制 Node、网络或 shell；脚本不绕过此类限制。发行包不包含 Node runtime。

## 启动与数据

完成安装、准备好实际业务配置后，通过安装器输出的固定 Node 入口运行服务；启动参数及配置接口见 [服务说明](../packages/task-service/README.md)。可以指定 `--data-dir` 和 `--port`，默认仅监听 `127.0.0.1`，不自动暴露公网端口。

服务配置、数据库、日志及运行制品均放在发行包外。不要修改发行文件、把数据写进安装树、删除未知锁或用重新初始化代替恢复。停止并重开沿用同一配置和数据目录；不将本次安装能力扩大为跨版本数据迁移承诺。
