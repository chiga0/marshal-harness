# 显式 Qwen 小型文件团队启动

本攻略适用于包含 `packages/task-generic-files/qwen-short-service-config.mjs` 的受信候选源码或已核验发行安装，不适用于缺少该文件的旧发行。候选 `ff468dfb` 已完成一条真实 Qwen 0.23.2 文件任务与终态正常重开；这不是默认安装体验、任意版本 Qwen 或活跃崩溃恢复通过的声明。详见[本次验收](ui-1/2026-09-14-intervention-fix.md)与 [ADR0101](adr/0101-generic-leader-model-wire-binding.md)。

## 前提

- 使用已经安装、可运行的 Node；不执行 `go run` 或临时原生二进制。
- 本机 Qwen 已登录，支持 `--acp`、`--approval-mode`、`--core-tools`、`--exclude-tools`。不需要重装 Qwen，不复制登录凭据。不支持参数时停止，不删参数降级。
- 将下列第一行改成受信源码或已核验安装的绝对路径。不要把 HTTP 输入、模型输出或不可信下载作为配置模块执行。

## 新根启动（前台）

以下 Bash 命令只定位现有程序、创建私有空父目录并启动服务；不会创建 Task 或自动批准计划。

```bash
export MARSHAL_INSTALL_ROOT='/absolute/trusted/marshal-install'
(
  set -eu
  node_entry="$(command -v node)"
  qwen_entry="$(command -v qwen)"
  test -f "$node_entry" && test -x "$node_entry"
  test -f "$qwen_entry" && test -x "$qwen_entry"
  marshal_node="$("$node_entry" -e 'process.stdout.write(require("node:fs").realpathSync(process.argv[1]))' "$node_entry")"
  agent_executable="$("$marshal_node" -e 'process.stdout.write(require("node:fs").realpathSync(process.argv[1]))' "$qwen_entry")"
  export MARSHAL_AGENT_EXECUTABLE="$agent_executable"
  marshal_install="$("$marshal_node" -e 'process.stdout.write(require("node:fs").realpathSync(process.argv[1]))' "$MARSHAL_INSTALL_ROOT")"
  test -f "$marshal_install/packages/task-service/main.mjs"
  test -f "$marshal_install/packages/task-generic-files/qwen-short-service-config.mjs"
  private_parent="$(mktemp -d "$HOME/.marshal-qwen-files-XXXXXX")"
  private_parent="$("$marshal_node" -e 'process.stdout.write(require("node:fs").realpathSync(process.argv[1]))' "$private_parent")"
  printf '本次独立数据根：%s/data\n' "$private_parent"
  exec "$marshal_node" "$marshal_install/packages/task-service/main.mjs" \
    --root "$private_parent/data" --mode create --port 0 \
    --config "$marshal_install/packages/task-generic-files/qwen-short-service-config.mjs"
)
```

保留终端中打印的数据根和启动输出的 `connectionFile` 路径。连接文件是本次服务私有凭据，由客户端在本机读取；不要将文件正文/token 粘贴到聊天、URL、截图或提交中。客户端连接成功后再创建 Task、查看真实计划并批准。前台正常停止使用此服务终端的 Ctrl-C，并确认服务正常退出；不跨编排结束其他进程。

UI 可选：若已有与候选对应的受信构建目录，在同一启动命令末尾追加 `--ui /absolute/trusted/ui-dist`。没有构建产物时不追加；不要临时猜测目录。使用本次启动输出的本地地址访问 `/ui/`，按界面连接流程使用本次私有连接信息。启用 UI 不扩大业务授权。

## 重开与明确限制

- 只有本配置创建且已正常停止的数据根，才可用原固定 Node、原安装/配置和原 `MARSHAL_AGENT_EXECUTABLE`，将上述 `--root` 换成记录的绝对根、`--mode create` 换成 `--mode open`。不要重新运行 `mktemp` 来冒充原任务恢复。配置漂移或清理不明时保留现场并停止排查。
- 默认 `marshal init`、`marshal serve`、`--generic` **没有切换**到此短协议。此攻略显式选择配置，不注册或修改默认启动器。
- 不复用旧配置根，不恢复旧 `cleanup_unconfirmed`／`extra_scope_unresolved` Task；禁止清库、改资格位或重签旧结果。终态正常重开不等于活跃崩溃恢复。
- 仅小型 UTF-8 文件交付：每位作者最多 8192 字节，独立 Review 加固定文件核验；原始交付为 JSON 容器，暂存/交付路径可能是 `.md`，内容仍可为原始 HTML。包含逐文件下载功能的候选 UI 可在成果页“查看包内文件”，核验后保存原名或显式另存为 `todo.html`；不自动猜扩展名、不执行内容、不改变原证据。不等于自动验证程序的浏览器功能。
- 专项工具配置拒绝 shell、网络、MCP 等路径，但不是 OS 沙箱，也不证明任意原生 hooks 被隔离。未知执行 scope 仍应阻断。不授权 SQL 发布、补数据或其他外部业务写入。
- 独立 Review 通过不等于真人可用性通过；`audit.firstReview` 的 0/0 是未测量，实际本次集中审查查看 `leader.review`。
