# 受管本地执行

固定 Node 加载版本管理中的 `guard.mjs`，由仍存活的所属进程组 leader 管理子进程；不生成原生临时程序，不按数据库中的历史 PID 发信号。普通可信同用户执行，不是恶意代码沙箱；主动脱离进程组的子进程不在该 profile 保证内。

`launchAcp(options)` 返回 ACP client、原 started、exited、completion 和 stop，原接口不变。`end_turn` 是协议回合完成，仍需实际 cleanup 后才能采集候选。

`launchCommand({executable,args,cwd,env,deadline,input,limits})` 用同一个 guard 执行受信组合根选择的独立验证命令，不创建 ACP client、不伪造 `end_turn`。`input` 为最多1MiB的 Uint8Array，stdin 保持打开，验证协议使用有界帧（例如一行 JSON），不能依赖 EOF。返回：

```js
const execution = await launchCommand(options);
// execution.started / execution.exited 与 ACP 路径相同。
const {cleanup, stdout, outputComplete} = await execution.completion;
// execution.stop() 幂等地等待同一 completion。
```

stdout 最多收集1MiB；`outputComplete` 只表明所收字节数和 guard 观察相等且未越界，不表示 JSON 有效或验证通过。原 stderr 仅计数不返回；env 默认空，不继承宿主凭据。期限、原句柄取消、父进程断开、继承子进程清理沿用同一机制。启动前验证输入和配置；启动失败保留原清理事实。

业务验证层必须检查原执行身份、正常退出、实际 cleanup、完整输出帧、nonce、批准策略/候选摘要与所有必要断言。Runtime 不给 stdout 签业务验收，不保存 Decision、不释放 Application 的容量，也不把退出0当 Task 完成。输出不完整或清理未知必须失败或保留未知，不重跑未知效果。

定向验证：`node --test --test-concurrency=1 packages/agent-runtime/command.test.mjs packages/agent-runtime/index.test.mjs`。测试使用固定 Node 和仓库内夹具，覆盖双向 ACP、独立命令、取消/期限、非零退出和继承子进程、字节限制、父 owner 实际退出；不是实际业务 verifier 或正式部署验收。
