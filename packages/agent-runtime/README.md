# 受管本地执行

固定 Node 加载版本管理中的 `guard.mjs`，由仍存活的所属进程组 leader 管理子进程；不生成原生临时程序，不按数据库中的历史 PID 发信号。普通可信同用户执行，不是恶意代码沙箱；主动脱离进程组的子进程不在该 profile 保证内。

`launchAcp(options)` 返回 ACP client、原 started、exited、completion 和 stop，原接口不变。`end_turn` 是协议回合完成，仍需实际 cleanup 后才能采集候选。

`launchProtocol({...options, createClient})` 为可信组合层提供相同受管执行的协议 DI 接缝。同步 `createClient({readable,writable,onClose})` 返回有 `close()` 的协议客户端，只解析现有字节流，不获得 PID/启动权限或 Task Store。它不改变 guard 的原协议、身份与清理范围；`launchAcp` 继续使用原 `AcpClient`，不经过 Pi 实现。工厂异常、异步工厂或无效返回值在执行启动前停止已创建的 guard，并保留原失败清理事实；协议 `close()` 的异常也不能中断原 owned stop。第三方回调仍是受信代码，不是恶意插件隔离。

`launchCommand({executable,args,cwd,env,deadline,input,limits})` 用同一个 guard 执行受信组合根选择的独立验证命令，不创建 ACP client、不伪造 `end_turn`。`input` 为最多1MiB的 Uint8Array，stdin 保持打开，验证协议使用有界帧（例如一行 JSON），不能依赖 EOF。返回：

```js
const execution = await launchCommand(options);
// execution.started / execution.exited 与 ACP 路径相同。
const {cleanup, stdout, outputComplete} = await execution.completion;
// execution.stop() 幂等地等待同一 completion。
```

stdout 最多收集1MiB；`outputComplete` 只表明所收字节数和 guard 观察相等且未越界，不表示 JSON 有效或验证通过。原 stderr 仅计数不返回；env 默认空，不继承宿主凭据。期限、原句柄取消、父进程断开、继承子进程清理沿用同一机制。启动前验证输入和配置；启动失败保留原清理事实。

业务验证层必须检查原执行身份、正常退出、实际 cleanup、完整输出帧、nonce、批准策略/候选摘要与所有必要断言。Runtime 不给 stdout 签业务验收，不保存 Decision、不释放 Application 的容量，也不把退出0当 Task 完成。输出不完整或清理未知必须失败或保留未知，不重跑未知效果。

Darwin 原组退出后仍有待回收成员时，signal `0` 可能短暂返回 `EPERM`（[Apple 实现](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_sig.c#L1612)）。该结果始终是未决；Runtime 只在原有5秒清理总预算内继续只读观察，不重置预算、不补发停止信号。必须后续取得原组的真实 `ESRCH`、原清理回执和原 guard 的 `SIGKILL` 退出才可确认；持续拒绝到期仍为 `cleanup_unconfirmed`，其他错误仍立即拒绝。此项不改变业务期限、持久合同或隔离等级，也不把缺少 errno 的历史 CI 失败断言为已确诊。

定向验证：`node --test --test-concurrency=1 packages/agent-runtime/command.test.mjs packages/agent-runtime/index.test.mjs`。测试使用固定 Node 和仓库内夹具，覆盖双向 ACP、独立命令、取消/期限、非零退出和继承子进程、字节限制、父 owner 实际退出；不是实际业务 verifier 或正式部署验收。
