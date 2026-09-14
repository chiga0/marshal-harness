# 本地产品命令

本包属于 Marshal 产品，不依赖 Skill。目标用户入口为并列的 `marshal init`、`marshal serve`、`marshal status`。当前源码可用 `bash scripts/marshal.sh <命令>`；发行包中等价入口是 `node <安装根>/packages/task-local/main.mjs <命令>`。旧 Go 的 `marshal` 命令不覆盖，本变更尚未发布到 v1.0.1。

- `init`：从自身安装位置确定发行根，在 PATH 中有界查找 Qwen、Pi、OpenCode，记录入口及解析路径，并在 `~/.local/bin/marshal` 安装固定 Node 文本启动器。重复执行复用相同启动器；同名其他程序、被修改的启动器或其他版本的入口不覆盖，返回 `launcher.state=conflict`。不修改 shell 启动文件，PATH 未包含 `~/.local/bin` 时可用完整命令路径。只检测 Agent 文件，不启动 Agent、不读取凭据、不进行模型调用。
- `serve`：先验证保存的连接，成功即复用；显式参数要求改变正在运行的配置时返回 `running_configuration_conflict`，不把旧服务冒充新配置。否则以前台 Node 子进程调用服务入口。显式 `--config /absolute/config.mjs` 和已记录配置优先；没有配置时使用发行包内通用文件团队，选择保存的 Qwen 入口或当前 PATH 首个 Qwen，也可用 `--agent-executable /absolute/qwen` 指定。入口存在不等于已登录或业务能力可用，模型鉴权仍由 Agent 自身管理。
- `status`：使用原 SDK 对当前记录执行带认证的 `ready.get`，不输出 token。

`init` 未给 `--install-root` 时取当前命令自身安装根。升级不会静默覆盖旧命令：停止旧服务后，从新安装包执行 `node /absolute/new/packages/task-local/main.mjs init --replace-launcher`。显式替换仅接受与原设置中安装根、记录 Node 路径及完整生成格式一致的旧 launcher，采用临时文件同步和 inode 复查后原子替换；修改过的命令、链接或未知身份一律保留并报冲突。历史设置没有 Node 记录时仅接受当前 Node 的完整旧格式，不能猜测旧执行路径。普通冲突保留旧设置；活跃旧连接阻止配置或 launcher 升级，不自动停止旧服务。

配置位置默认为 `~/.marshal-client/local.json`（0700目录、0600文件），可用 `--settings-dir` 指定。它只是可重建的本机启动设置，不是 Task/执行/恢复的第二权威；SQLite、原服务 owner acquisition 与数据根均不变。旧连接失效不会授权关闭未知服务、清空状态或重派任务。`serve` 是前台程序，应使用宿主支持的长运行终端或服务管理器；本包不是常驻 watchdog。

```sh
bash scripts/marshal.sh init
bash scripts/marshal.sh serve
# 可选：本机 UI 与明确 Agent 入口；UI 参数指向可信发行包的 UI 目录
bash scripts/marshal.sh serve --agent-executable /absolute/qwen --ui /absolute/installed/apps/task-web/dist --port 43123
# 另一个终端或客户端：
bash scripts/marshal.sh status
```

代码客户端可 `import {connectLocal} from '<安装根>/packages/task-local/main.mjs'` 后调用 `await connectLocal()`，得到已有 TaskClient，沿用原操作合同，不需要重复传安装根和连接文件。

## 当前限制

默认通用团队数据使用 `~/.marshal-node/generic-team`，不接管旧 `~/.marshal-node/task-service`；`--data-dir` 可指定独立私有目录，原 Store 继续决定创建/打开及兼容性，不清空或迁移旧数据。重复启动保留记录的配置和数据目录。

已有业务配置时，停止服务后用 `marshal serve --generic` 明确选择内置文件团队（与 `--config` 互斥）；旧配置文件和业务数据不删除，旧 dataDir 不带入默认通用根，除非本次显式提供 `--data-dir`。如需保留两套启动设置并存，使用不同 `--settings-dir`；该选项不提供同时写同一数据目录的许可。

`--ui /absolute/ui-dist`、`--port 0..65535` 传给原服务。停止原服务后可用互斥的 `--no-ui` 清除 UI 启用记录，数据目录不变，无需手改设置文件。成功后输出实际 `address` 和 `uiUrl`，连接文件仍指向原 API 监听；`connectLocal()` 自动复用服务验证过的连接，不要求手填环境变量。UI 仍沿用正常 Bearer 认证，不把 token 放到 URL/设置输出。未启用 UI 时不虚构 UI 地址。

自动发现不等于登录或业务验收通过。默认工厂仅为通用文件交付，不承诺 ETL、外部发布或补数据能力；这些仍需明确业务配置、授权与可核查的验收。已有明确配置不会被默认工厂替换，启动失败不会关闭未知服务或创建替代 Task。

安装器已增加验签恢复后的自动 `init` 钩子，只在已验证 manifest 包含本包入口时执行；初始化失败保留已安装资产并单独提示，不误报下载/验签失败、不启动服务。固定 v1.0.1 没有该入口，继续沿用其原启动说明；新功能须经后继签名发行后才能通过一键安装获得。升级命令入口冲突需显式处理，不静默切换仍在运行的安装。
