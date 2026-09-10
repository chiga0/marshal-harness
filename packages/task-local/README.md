# 本地产品命令

本包属于 Marshal 产品，不依赖 Skill。目标用户入口为并列的 `marshal init`、`marshal serve`、`marshal status`。当前源码可用 `bash scripts/marshal.sh <命令>`；发行包中等价入口是 `node <安装根>/packages/task-local/main.mjs <命令>`。旧 Go 的 `marshal` 命令不覆盖，本变更尚未发布到 v1.0.1。

- `init`：从自身安装位置确定发行根，在 PATH 中有界查找 Qwen、Pi、OpenCode，记录入口及解析路径。只检测文件，不启动 Agent、不读取凭据、不进行模型调用。
- `serve`：先验证保存的连接，成功即复用；否则以前台 Node 子进程调用现有服务入口。首次配置可用 `--config /absolute/config.mjs`，之后复用记录；捕获启动器实际返回的连接文件，用户不必设置 `MARSHAL_CONNECTION_FILE`。
- `status`：使用原 SDK 对当前记录执行带认证的 `ready.get`，不输出 token。

配置位置默认为 `~/.marshal-client/local.json`（0700目录、0600文件），可用 `--settings-dir` 指定。它只是可重建的本机启动设置，不是 Task/执行/恢复的第二权威；SQLite、原服务 owner acquisition 与数据根均不变。旧连接失效不会授权关闭未知服务、清空状态或重派任务。`serve` 是前台程序，应使用宿主支持的长运行终端或服务管理器；本包不是常驻 watchdog。

```sh
bash scripts/marshal.sh init
bash scripts/marshal.sh serve --config /absolute/trusted/config.mjs
# 另一个终端或客户端：
bash scripts/marshal.sh status
```

代码客户端可 `import {connectLocal} from '<安装根>/packages/task-local/main.mjs'` 后调用 `await connectLocal()`，得到已有 TaskClient，沿用原操作合同，不需要重复传安装根和连接文件。

## 当前限制

自动发现不等于 Adapter 注册、协议兼容、登录或业务验收通过。当前受信业务配置仍须首次提供；检测结果没有被伪造成通用业务工厂。原生 Skill 的工具权限、通用独立验收、外部发布仍是下一步的真实运行时接缝，本命令不能消除它们。安装器自动运行 init 及稳定 `marshal` 命令安装尚未接线，不冒称已交付。
