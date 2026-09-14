# 快速开始

本页带你完成一个有明确成果的小型文件任务。先区分[版本支持](api-support.md)：`v1.0.2` stable 需要受信业务配置且不含 UI；`v1.1.0-rc.2` 预发布包含通用文件团队与 UI。下面的无手写业务配置路径针对 RC.2，不把预发布功能说成 stable 已具备。

## 1. 准备 Agent 与运行环境

准备 Node ≥22（已验证具体组合见版本支持）、Python 3、curl 和 minisign。安装 Qwen 并完成其原生模型登录；Marshal 不替你复制或收集登录凭据。其他 Agent 通过已支持适配器及受信配置接入，PATH 中发现某个命令不代表它已通过真实协议验证。

通用文件模式用于小型 UTF-8 成果；不执行候选代码、不默认授权生产 SQL、补数或外部发布。先用不含秘密的材料试用。

## 2. 安装明确版本

RC.2 预发布安装：

```sh
curl -q -fL --proto '=https' --proto-redir '=https' \
  https://github-releases.oss-cn-hangzhou.aliyuncs.com/marshal-harness/v1.1.0-rc.2/install-node-preview.sh \
  -o /tmp/install-node-preview.sh
bash /tmp/install-node-preview.sh
```

安装器执行发行资产核验；以其实际输出的安装目录为准。安装、镜像、离线验证和 stable 配置路径见[下载与安装](node-install.md)，RC.2 身份与升级边界见[发行记录](v1.1.0-rc.2-release-dossier-2026-09-14.md)。安装器的旧配置提示不代表 RC.2 普通文件任务必须手写业务程序。

在安装器输出的目录中执行初始化；下面 `<安装目录>` 需要替换为真实路径：

```sh
node <安装目录>/packages/task-local/main.mjs init --install-root <安装目录>
```

如果已有旧 Marshal 启动器，先正常停止旧服务，再按发行记录使用 `init --replace-launcher`。未知或已修改启动器、活跃服务和不明状态不得强制覆盖；旧安装和数据保留。

## 3. 启动通用团队

```sh
~/.local/bin/marshal serve --generic --agent-executable /绝对路径/qwen
```

`--generic` 显式选择内置通用配置；服务记录真实 Agent 入口和连接信息，旧配置不会被普通启动静默替换。使用已安装且支持 ACP 的真实可执行路径，不根据目录名猜品牌或登录状态。

`serve` 是前台服务。保持终端运行，打开其实际输出的 UI 地址；需要核查连接时在另一个终端运行：

```sh
~/.local/bin/marshal status
```

不要把 `status` 接在前台 `serve` 后等其退出，也不要把访问 token 拼到 URL。浏览器如要求凭据，只使用本服务受保护连接记录中的 token，不能把 token 发给 Worker。

## 4. 提交并验收第一个 Task

可以输入：

> 请用两份简短中文 Markdown 分别解释加法和乘法，每份包含定义和两个正确例子。分别保留两份成果。独立检查是否覆盖要求及例子是否正确，不执行任何外部操作。

检查交付约定、分工与验收后批准，观察团队执行和独立 Review。下载实际文件并核对内容。候选通过、交付完成与总体完成是不同事实；出现拒收、待答或失败时按界面给出的实际原因处理，不新建任务来冒充原任务恢复。

完成后正常停止再启动同一服务，可以核对原 Task 和成果是否仍可查询；不要通过重新调用模型证明持久恢复。

## API 和自定义业务接入

自己的客户端通过[标准 API](standard-api.md)接入；[HTTP 参考](api/http-reference.md)与原始 [OpenAPI](../packages/task-api/openapi.json)定义准确请求、响应及错误。业务配置属于部署方受信代码，不接受 HTTP 上传可执行配置。更多业务和 Agent 按[扩展契约](extension-contracts.md)接入。

## 常见阻塞

| 现象 | 处理 |
| --- | --- |
| Agent 能启动但任务不能运行 | 查看实际握手/模型错误；命令可执行不等于协议或模型可用 |
| 旧启动器仍指向旧安装 | 按发行记录核对路径并显式升级，不覆盖未知程序 |
| 服务拒绝配置或数据根 | 保留原配置、数据和错误；不要清库或改内部状态绕过检查 |
| UI 可打开但服务未就绪 | 检查服务状态和实际错误，不把静态页面可达当业务可用 |
| 任务完成但没有生产结果 | 核对交付约定；文件模式不负责执行 SQL、发布或补数 |

完整边界见[安全模型](security-model.md)，版本与实机证据见[版本支持](api-support.md)和[Roadmap](roadmap-status.md)。
