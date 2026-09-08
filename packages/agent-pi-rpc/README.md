# Pi 原生 RPC transport 候选

本包只解析注入的双向字节流，不启动或关闭系统进程，不访问配置/登录，不负责 Task 权威。它不是 ACP 或 Pi 自带会额外启动进程的 RpcClient 的别名。依据本机安装的 `@earendil-works/pi-coding-agent@0.84.4` 的 `docs/rpc.md` 与原实现编写；版本作为调研依据，不是运行版本白名单。

接口：`new PiRpcClient({readable,writable,onEvent,onClose,onInteraction,...limits})`，提供 `getState()`、`prompt(text,{timeoutMs})`、`cancel()`、`close()`。仅一个原生会话/一个活跃 prompt；没有任意 RPC command、模型切换、Steer、直接 bash、历史 session 导入或自动重试 API。

- LF 是唯一分帧符；UTF-8 可跨 chunk，CRLF 可接受，字符串中的 U+2028/U+2029 不分帧。不使用 Node readline。
- `get_state` 精确关联 id/command，只返回 sessionId 和忙碌/队列/消息计数，不转发模型配置或 session 文件路径。
- `prompt` 的 success 只表示被接收；`agent_end` 不是最终完成。必须同时收到精确 prompt 接收响应、`agent_settled` 与本次最终 assistant 消息。原生自动 retry/compaction 可以在原累计 deadline 内继续，客户端不另派 prompt。
- 只有最终 `stopReason=stop` 且有完整文本才返回候选；error/aborted/length/toolUse/deferred 不算成功。最多64KiB文本，不能截断后返回成功。
- 取消先 `clear_queue` 的原响应，再 `abort`；返回只证明协议响应，不是进程/目录清理。清理属于原 Runtime。
- 原生 extension 对话默认 cancelled，且本次不能因此声称问题已经解答。受信 Provider 可注入 `onInteraction(message,{signal})`，自行验证其固定原生扩展/当前执行绑定；只有 `{handled:true,confirmed:boolean}` 可回应 confirm。通知只供受信关联，不能自动成为批准/进度/证据；此 transport 自身不实现 ACP 或 Task question authority。
- 对话处理不占收包循环，clear_queue/abort 在等待权限时仍可执行；超时、关闭和取消撤销 signal，迟到回答失效。pending 对话不能与成功终态并存。其他真实用户问题仍须上层 Task question producer，不能伪装工具授权或默认答案。
- 帧、队列、请求、事件数和回调均有界；错误只含固定 reason code，不反射原生错误文字。`onEvent` 为受信临时数据消费者，必须有界；公开进度需由 Provider 单独投影，不能公开原始事件。

测试 `node --test packages/agent-pi-rpc/client.test.mjs` 仅使用字节流，覆盖关联、分帧、终态/重试、取消顺序、问题拒绝、错误/EOF/超限/回调超时，不访问网络或模型。
