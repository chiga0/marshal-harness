# Node v1.0.0 下载部署

> Node22兼容正在作为后继源码按 [ADR0097](adr/0097-node-capability-based-runtime-admission.md) 实施。此页面和 `install-node.sh` 目前仍下载已签名v1.0.0，内部有Node24门禁；新兼容包发布并更新摘要前，不应删除安装检查后运行旧包，也不能称一键安装已经支持22。

本入口安装 [v1.0.0 正式发行](https://github.com/chiga0/marshal-harness/releases/tag/v1.0.0) 的 Node Agent Team 文件，不安装历史 Go CLI，不编译或执行 Marshal 原生二进制，不请求 sudo，不修改系统安全设置。

## 一键安装

支持平台为 macOS Apple Silicon（`darwin-arm64`）和 Linux x64（`linux-x64`）。前置工具：可运行的 Node `24.15.0`、Python 3、`curl` 和 `minisign`。目标环境需能访问 GitHub 的发行下载及源码地址；缺工具时安装器给出错误，不自动安装系统软件。`node --version` 可先检查 Node。

```sh
curl -fsSL https://raw.githubusercontent.com/chiga0/marshal-harness/main/scripts/install-node.sh | bash
```

默认安装到 `$HOME/.local/share/marshal-node/v1.0.0`，不改 PATH。需要部署到 Sandbox 的持久化挂载盘时：

```sh
curl -fsSL https://raw.githubusercontent.com/chiga0/marshal-harness/main/scripts/install-node.sh \
  | bash -s -- --prefix /absolute/private-parent/marshal-v1.0.0
```

安装目标必须是新目录，已有安装或失败现场不会被覆盖；自定义目标的父目录必须已存在、属于当前用户且权限为 `0700`，不跟随符号链接。脚本不自动放宽或修改既有目录的权限。需要先审阅脚本时，可从仓库下载 `scripts/install-node.sh`，检查后执行 `bash install-node.sh --prefix ...`。上面 `main` 是维护者更新的安装脚本入口，发行载荷仍精确固定为 v1.0.0；需要固定安装器本身时，使用审查过的完整 commit 替换 URL 中的 `main`。

## 范围

- 当前只安装冻结的 v1.0.0，不自动选择 latest、不回退源码编译。
- Qwen/Pi、模型登录、Skill 和业务配置由部署者提供，本脚本不读取或复制凭据。
- 安装完成不等于服务已运行。当前服务仍要求显式业务配置；缺配置时不启动一个假成功服务。
- 只适用于可信单用户部署，不提供恶意代码隔离。Sandbox 中安装目录与服务数据目录应选择平台提供的持久化存储；环境销毁后不能依赖临时磁盘恢复。

## 身份与核验

脚本使用仓库固定的维护者 minisign 公钥核验 `SHA256SUMS`，并核对冻结版本的来源与资产摘要。不把随下载文件附带的任意公钥作为新的信任根。签名口径见 [ADR0096](adr/0096-node-stable-asset-signing-minisign.md)。

| 身份 | v1.0.0 |
| --- | --- |
| sourceHead | `fc2cdc9298c3e1aaf47615373bb24e6d80e4c719` |
| ZIP SHA-256 | `d46277e0c00acb4c78ae6200cebb48ae74dab52af8d1bc4a192937408ef5a766` |
| manifest SHA-256 | `828b3ada02fd7a4f857ad527a13ac31664ed56a8d01a9bc5996170c96fa4955d` |

安装器和 Node 解释器本身属于部署者信任的执行工具。企业策略仍可能限制 Node、网络或 shell；脚本不绕过此类限制。发行包不包含 Node runtime。

## 启动与数据

完成安装、准备好实际业务配置后，通过安装器输出的固定 Node 入口运行服务；启动参数及配置接口见 [服务说明](../packages/task-service/README.md)。可以指定 `--data-dir` 和 `--port`，默认仅监听 `127.0.0.1`，不自动暴露公网端口。

服务配置、数据库、日志及运行制品均放在发行包外。不要修改发行文件、把数据写进安装树、删除未知锁或用重新初始化代替恢复。停止并重开沿用同一配置和数据目录；不将本次安装能力扩大为跨版本数据迁移承诺。
