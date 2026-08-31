# ADR 0072：Worktree 释放前绑定结果观察

- 状态：已接受（Accepted，2026-08-31）
- 提议基线：`feat/pi-first-architecture-fix@357420d8d8fb56f92a80997bd445f722b4c3c5a9`
- 关联：[ADR 0056](0056-darwin-process-observation-and-attempt-terminalization.md)、[ADR 0069](0069-attempt-reservation-and-existing-worktree-allocation.md)、[ADR 0071](0071-darwin-sealed-completion-and-durable-result-capability.md)、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)。

## 背景

ADR 0071 要求 Core 在 `worker.completed` 前完成结果接纳、terminalization 与 path-B worktree release。若进程在 release 已耐久、但 `worker.completed` 尚未追加时崩溃，worktree 已合法归还用户；恢复时重新观察 live worktree 会把用户后续修改误判为证据漂移，使仍为 `RUNNING` 的 Run 无法收敛。只信任 `.marshal` 中未绑定的 snapshot 文件同样不安全，因为文件替换可能改变 completion 证据。

## 决策

1. Core 必须在首次 WorkerResult admission 前生成规范化 `worktree-snapshot.json`。
2. governed `result-admitted` fact 同一 fsync 持久化 `ResultObservationBinding`：snapshot 文件精确 bytes 的 SHA-256、`snapshotDigest` 与 `diffDigest`。
3. Attempt authority 冷重放必须恢复该 binding；path-B release 只有在 admission 已提交后才能继续。
4. committed recovery 只从 descriptor-held attempt directory 读取 snapshot，校验规范 JSON、精确 bytes digest 与两个业务 digest；不得重新读取已释放的 live worktree，也不得重新签发 terminal lease 的 active ResultCapability。
5. 缺失、篡改或不一致的 binding 一律 fail closed。历史未携带 binding 的事实保持可重放，但不能作为本 RC1 completion 路径释放后的恢复证据。

## 顺序与恢复

```text
Collect outcome
  → parse WorkerResult
  → observe worktree + fsync snapshot file
  → result-admitted + ResultObservationBinding（同一 authority fact）
  → terminalization / release / cleanup
  → worker.completed
```

崩溃发生在 admission 前时，仍需重新观察当前受 Marshal 控制的 worktree；发生在 admission 后时，只允许重放已绑定 snapshot。worktree 在 release 后发生合法变化不影响旧 Attempt completion，但 snapshot 文件或 binding 变化必须阻断。

## 影响

本 ADR 补充 ADR 0071 的生命周期提交边界，增加一个可选持久化字段，不放宽 ResultIngress、worktree ownership 或普通用户安全边界。它只关闭 RC1 的 admission→release→run-event 崩溃窗口，不升级 milestone，也不构成发布证据。
