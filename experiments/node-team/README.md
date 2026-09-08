# Node 本地 Agent Team 实验

对应 [ADR 0087](../../docs/adr/0087-node-local-team-feasibility-probe.md)。这是无 Marshal 原生依赖的独立可行性纵切，不是既有 Go 服务的替换或 production 发布。

## 形态

由已允许的 Node 24 解释器直接运行 `.mjs` 源码：HTTP 前端 → 内置 Node supervisor → 两个本机 Pi 作者 → 固定组合验收 → 文件交付。没有 Go 编译、Marshal 原生子进程、原生扩展或随机 Mach-O 可执行文件。

两个作者分别生成 `normalize.mjs` 与 `report.mjs`；它们的输出必须组合通过相同订单接口的正常输入、非法输入及溢出断言。Agent 禁用执行工具，以原生 JSON 事件返回代码；服务不接受模型自报的测试通过。该固定模板证明协作链路，不代表已经实现任意任务的自动规划。

## 启动

先由操作者准备一个私有 JSON 配置文件，只包含以下字段。`executable` 必须替换成本机已安装 Pi 的绝对入口路径，模型与鉴权继续由 Pi 自己管理；不要把 API key 加入配置。

```json
{"provider":"pi","executable":"/absolute/path/to/pi"}
```

使用新的、显式的实验数据目录，不指向旧 `.marshal` 状态目录。macOS 的 Unix socket 路径长度有限，数据目录宜保持短路径。

```sh
node experiments/node-team/main.mjs --data-dir /private/tmp/mnt-demo --config /absolute/path/to/provider.json
```

就绪输出只包含 URL 与私有 connection 文件路径；Bearer token 从该文件读取，不打印、不提交、不发给 Agent。服务仅绑定 loopback。通过 `POST /v1/tasks` 创建预览，核对后以精确 `previewDigest`、`expectedRevision` 和 `Idempotency-Key` 批准执行。接口列表见 ADR。

关闭 HTTP 前端不取消任务；再次启动同一数据目录会重连原 supervisor。明确停止整个实验时运行：

```sh
node experiments/node-team/main.mjs stop --data-dir /private/tmp/mnt-demo
```

## 验证

确定性测试不调用真实模型：

```sh
node --test experiments/node-team/providers.test.mjs experiments/node-team/business.test.mjs experiments/node-team/http.test.mjs
```

真实验收须显式指定本机 Pi，使用现有账号额度；不自动重试。它先启动两个真实作者，验证活跃前端重启和模块交付，再启动两个作者验证取消。结果摘要留在私有临时目录，失败证据也保留。

```sh
MARSHAL_NODE_LIVE_PI=/absolute/path/to/pi node --test experiments/node-team/live.test.mjs
```

## 不能混淆的边界

- 当前恢复目标是 HTTP 前端重启、原 supervisor 仍存活。整机/监督进程崩溃后，未知归属拒绝自动接管；不得手工删锁假装恢复成功。
- 普通同 UID 进程不是恶意代码沙箱。Node 权限模型本身不提供网络隔离；固定 VM 验收只运行纯 JS、拒绝导入，不提供宿主函数或环境给候选。
- 状态独立，不迁移旧 Run，不授予 Git/CI/云发布权限。用量无法观测时为 `null`，不估算成真实账单。
- 这里的临时文件是数据与源码，不是新编译的原生可执行文件。企业策略仍可能检查脚本或解释器；只有实机执行结果能证明当前机器允许，不承诺换语言必然免审批。
