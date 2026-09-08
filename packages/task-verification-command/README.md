# 独立命令验收适配器

`createVerificationCommand(config)` 提供 `start({ticket, prepared})`，供 Core 的私有 `createVerificationPort` 注入。它只运行一个受管命令并生成证据字节，不签发 Decision、不写 Store、不接受 Task，也不解释 HTTP/Agent 提供的验收程序。

## 可信组合

配置必须来自受信组合根：固定 Node `executable`、版本管理中的绝对 `checkerPath`、`checkerDigest`、与批准验收合同相同的 `policyDigest`、非空且完整的 `assertions: [{name, validate(actual, context)}]`、`delivery(context)`。`env` 默认空；不会自动继承 HOME、登录或发布凭证。命令参数固定为该 checker 路径，业务数据仅通过 stdin 传递。

可选 `request(context)` 生成有界 JSON 输入；默认传递冻结的 verification 和 fileLayout。所有回调都是**受信、同步、有界**业务代码，只可检查数据/文件及打包字节，不得启动额外进程、返回 Promise 或把正文当程序执行。接口不能在同进程中强制中断恶意同步回调；这不是恶意插件隔离承诺。

`context.ticket` 是完整冻结的原 ticket；`context.prepared` 只含规范化 `cwd`。delivery 还收到 `report`。输出为 `{name, mediaType, content: Uint8Array}`，最多 8 MiB；下载文件名不含路径。每个断言的校验函数必须判断该业务必需的真实结果，不能仅检查报告内 `passed: true`。

## Checker 协议

stdin 保持打开，checker 应读取**一行** canonical JSON，而非等待 EOF：

```text
{profile:"task-verification-command/v1",nonce,binding,input}\n
```

`binding` 包含原 `reservationDigest/planDigest/inputDigest` 和受信 `checkerDigest/policyDigest`。stdout 只允许一行 canonical JSON：

```text
{profile,nonce,binding,assertions:[{name,actual}]}\n
```

canonical 编码复用 Store 的纯编码函数：对象键排序、无多余空白、UTF-8，末尾一个换行。最大输入/输出各 256 KiB，stderr 64 KiB。重复键、重复/未知/缺失断言、错误 nonce/摘要、额外 frame、无效 UTF-8、非零退出、截断输出均拒绝。checker 在候选目录外，启动前及清理后核验原路径、inode/元数据与固定摘要；这只描述可信 ordinary-user 主机上的漂移检测，不是同 UID 恶意写入隔离、完整依赖封装或签名/公证保证。checker 的依赖与业务策略仍须由受信部署固定。

## 原进程归属与结果

`start` 同步返回 `{started: Promise, completion: Promise, stop()}`。实际子进程全部复用原 `launchCommand` 与活跃 guard；`stop()` 可在 bootstrap 阶段调用，等待原清理结果，绝不按磁盘 PID 杀进程。沿用 ticket 原绝对期限，不因验证或停止而刷新。

只有正常退出 0、原 guard 确认清理、完整输出、全部精确绑定及每项断言通过后，才返回 `status: 'passed'` 和 `evidence/delivery` 字节。其余返回 `status: 'failed'`、固定 reason、原 `cleanup`（尚无执行证据时为 null），制品为 null。Core 仍必须重查原 owner、ticket、manifest、取消和期限，并由自己的私有端口产生 opaque receipt 与原子最终接纳；本包返回值不是该接纳权。

## 验证边界

`checker.fixture.mjs` 是仅测试的文件求和 checker，不注册生产业务。测试运行固定已安装 Node 及原 guard，覆盖真实退出/清理、nonce/输入绑定、断言缺失、恶意正文、取消、超时、输出限额与失败不产生制品。不调用模型，不证明实际生产业务或完整 Task HTTP 交付已经完成。

```sh
node --test --test-concurrency=1 packages/task-verification-command/index.test.mjs
```
