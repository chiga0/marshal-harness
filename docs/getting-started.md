# 快速开始

本页带你从零到在本机跑通第一个 Task。当前产品是 **Node Agent Team**：`packages/task-service` 本地服务 + 可选同源浏览器 UI（ADR0098）。历史 Go CLI 已退出产品运行依赖，仅作为历史资料保留（适用边界见 [设计合同地图](design-contract-map.md)）；Node 下载部署的完整身份核验、镜像与离线说明见 [node-install.md](node-install.md)，本页是其上手视角的压缩与延伸。

> 版本说明：v1.0.2 发行包**不包含**构建后的 UI 资产；从下一候选起发行包含 `apps/task-web/dist`（与运行文件同一 manifest 摘要核验）。`--ui` 用法两种包一致，UI 资产缺失时启动如实失败。下文标注「需含 UI 的包」的位置注意区分。

## 两种前置情形

### 情形 A：Agent 与配置均已就绪

适用条件：Coding Agent（Qwen Code / Pi / OpenCode 之一）已安装并完成登录，且已有受信业务配置 `service-config.mjs`（部署方提供）。

1. **一键安装**（支持 macOS arm64 / Linux x64；前置 Node ≥22、Python 3、curl、minisign）：

   ```sh
   curl -q -fL --proto '=https' --proto-redir '=https' \
     https://github-releases.oss-cn-hangzhou.aliyuncs.com/marshal-harness/v1.0.2/install-node.sh \
     -o /tmp/install-node.sh && bash /tmp/install-node.sh
   ```

   安装到 `$HOME/.local/share/marshal-node/v1.0.2`，验签恢复后自动执行 `init` 并安装 `~/.local/bin/marshal` 启动器；不请求 sudo、不改 PATH、不覆盖旧安装。

2. **启动服务**：

   ```sh
   ~/.local/bin/marshal serve --config /absolute/trusted/service-config.mjs
   ```

   默认只监听 `127.0.0.1`，数据目录默认 `$HOME/.marshal-node/task-service`。连接文件在 `<数据目录>/connections/service-*.json`，内容为 `{profile,url,token}`（监听地址在文件的 `url` 字段）。注意：`marshal serve` 启动器只接受 `--config` 等少量参数，不转发 `--ui/--data-dir/--port`；需要这些参数时改用裸入口：

   ```sh
   node <安装目录>/packages/task-service/main.mjs \
     --config /absolute/trusted/service-config.mjs \
     --data-dir /absolute/private-parent/task-data
   ```

   裸入口第一行输出 launch announcement（含地址与连接文件路径）。

3. **连接使用**（三选一）：

   - 浏览器（需含 UI 的包）：用裸入口启动并追加 `--ui <安装目录>/apps/task-web/dist`，打开 `http://127.0.0.1:<端口>/ui/`，把连接文件里的 `token` 粘进连接页。token 只进页面内存，不要拼进 URL。
   - Node 客户端：见 `packages/task-client/README.md`，读取连接文件 `{url,token}` 调用 `/v1`。
   - Agent 客户端 Skill：`skills/marshal-client/SKILL.md`（产品客户端，不是历史研发治理 Skill）。

4. **第一个 Task**：自然语言描述需求 → 计划确认视图核对计划 → 批准后执行 → 在概览/团队/成果页跟踪；执行完成、候选、评审、独立验收、发布与后验分别呈现，执行结束不代表验收通过。

### 情形 B：只装了 Agent，完全没有配置

适用条件：机器上有一个 Coding Agent，但没装 Marshal、没登录模型、没有任何业务配置。按由浅入深的顺序补齐：

1. **完成 Agent 自身安装与模型登录**。登录属 Agent 官方流程（Qwen Code / Pi / OpenCode 各自文档）；Marshal 不代为登录、不读取或复制凭据，「能执行 Agent CLI」不等于已接入产品。
2. **安装并让 init 发现 Agent**：按情形 A 第 1 步安装。v1.0.2 的 `init` 会从安装位置识别自身并发现 PATH 中的 Qwen/Pi/OpenCode；可重复执行 `~/.local/bin/marshal init` 复查。发现不了时先确认 Agent 的可执行入口在 `PATH`。
3. **编写受信 `service-config.mjs`**。配置是部署方的可信 JS 模块，不是 HTTP 插件或提示词，服务启动前会真实校验；缺有效配置时启动失败（`service_start_unavailable`），不会起一个假成功服务。合同要点：

   - `providers`：`Map<provider.id, provider>`，如 `createAcpProvider({id, executable, args, env})`（见 `packages/agent-provider-acp`）；CLI 不推断品牌/登录状态。
   - 业务能力二选一：直接 `prepare/collect` 回调，或 `businessFactory + verification`（推荐，见 `packages/task-service/README.md`「可信业务工厂与唯一 verification 实例」）；两者皆无将被拒绝。
   - `applicationOptions.execution`：`{maxWorkers, defaultProvider}`。

   逐字段合同与示例骨架见 [task-service 组合根说明](../packages/task-service/README.md)。本仓库不附带可直接运行的默认业务配置：当前没有默认启用的通用业务，首个业务装配需要你（或你的部署方）明确选择与验收。
4. **启动与第一个 Task**：同情形 A 第 2–4 步。

## 最短路径检查单（从零到第一个任务）

- [ ] `node --version` ≥ 22，`python3 --version`、`curl`、`minisign` 可用；
- [ ] 安装脚本验签通过，安装目录为 `0700` 私有；
- [ ] `~/.local/bin/marshal init` 发现目标 Agent（情形 B 先完成登录）；
- [ ] `service-config.mjs` 通过启动校验（Provider + 业务能力齐全）；
- [ ] `marshal serve --config …`（或裸入口）输出连接文件路径，文件含 `{url,token}`；
- [ ] 用连接文件 `token` 完成一次连接，创建一个小型真实需求，确认计划 → 批准 → 观察到首个 Worker 事件。

## 常见阻塞定位

| 现象 | 优先检查 |
| --- | --- |
| `service_start_unavailable` | config 是否含 providers 与业务能力；提供方登录状态；错误详情以启动原始输出为准 |
| `--data-dir` 被拒绝 | 私有锚点要求：canonical、当前 UID 所有且 `0700`；共享 `/tmp`、他人目录或宽权限父目录不可用 |
| 连接页报「未取得有效响应」 | 服务是否在监听本站 origin；token 是否取自本次启动的连接文件；不要拼 `?token=` 到 URL |
| 任务创建了但没有 Worker | provider 未配置/未登录；`provider.list` 投影 availability 默认 `unknown`，不会自动发付费探测 |
| `--ui` 启动失败 | dist 缺失/含符号链接/缺 `index.html`/含未知扩展名；v1.0.2 包本就不含 UI 资产 |

## 之后读什么

- 架构与设计目标：[agent-team-service-architecture.md](agent-team-service-architecture.md)、[agent-team-service-milestones.md](agent-team-service-milestones.md)；
- API 合同：`packages/task-api/openapi.json` 与 [node-api-contract.md](node-api-contract.md)、[node-api-support-matrix.md](node-api-support-matrix.md)；
- 发行/核验/镜像：[node-install.md](node-install.md)、[node-oss-distribution.md](node-oss-distribution.md)；
- 历史 Go 时代资料均已移入 `*-reference-*` 或由 [设计合同地图](design-contract-map.md) 统一说明适用边界，不作为当前产品的使用文档。
