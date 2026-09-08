# Task 文件输入与采集

这是同步 Node 文件适配器：把已冻结的 depot 引用物化到全新执行目录，再把显式允许的输出保存回 depot。它不启动 Agent、不执行候选、不管理 Task 状态，也不产生验收或发布权限。

```js
import {createExecutionDirectory, collect} from './index.mjs';

const handle = createExecutionDirectory({
  parent: canonicalPrivateParent,
  workerId: uniqueWorkerId,
  inputs: [{path: 'inputs/source.txt', digest: 'sha256:…', bytes: 123}],
  depot, // 同步 get({digest,bytes}) 和 put(Uint8Array)
});
// 受信任 composition 将 handle.cwd 交给既有 Runtime。
// 原 Runtime 已证明所属执行清理完毕后，composition 才能调用 collect。
try {
  const candidate = collect(handle, {allowedPaths: ['result.txt']});
  // candidate = {files:[{path,digest,bytes}], inputDigest, manifestDigest}
  // 此后独立验证、SQLite Decision 与 Task 完成由上层负责。
} finally {
  handle.close(); // 仅关闭本包持有的 FD，不删除任何文件。
}
```

## 固定合同

- `parent` 必须是已存在的、canonical、当前 UID 所有的 `0700` 私有目录。`workerId` 仅允许字母、数字、下划线和短横线，最长 128 字符；目标目录只创建、不接管。重试必须使用新的执行身份，旧目录始终保留。
- `inputs` 是严格的 `{path,digest,bytes}` 数组，从 depot 读取后再次校验摘要和长度，落盘为 `0400` 文件。目录和输入文件 FD 保留到 `close()`；输入内容、身份或权限发生变化时采集拒绝。
- `allowedPaths` 由受信任 composition 根据已批准的具体交付清单提供，不能由 Worker 或自由文本 scope 扩大。它是全部必需输出文件的精确列表，不是 glob。输出不能覆盖输入；修改现有代码时，应把参考输入放在 `inputs/` 下，生成新的输出快照，而非在此目录执行 Git 原地修改。
- 路径要求 NFC、相对路径、最多 8 层和 1024 UTF-8 字节；禁止隐藏路径组件、控制字符、反斜杠、冒号、末尾空格或点、重复路径、大小写别名、文件/目录冲突。因此不会继承 `.git`、`.marshal` 或隐藏配置文件；非隐藏输入是否含凭据仍由上层负责筛选。
- 输入与输出合计最多 64 个文件、8 MiB。全部文件必须是当前 UID 所有的普通单链接文件，不允许符号链接、硬链接、特殊文件、组/其他用户可写或特殊权限。输出可以是通常的 `0644` 文件；执行根仍必须为 `0700`。
- 除文件清单所需目录外，额外文件或空目录也会拒绝。采集只读取字节，不运行程序；每个输出交给 depot 持久化，并校验返回引用。全部成功后才返回深冻结 manifest，不返回部分成功。
- `inputDigest` 是按路径排序的输入 `{path,digest,bytes}` 数组的 `JSON.stringify` 字节摘要；`manifestDigest` 对同格式的输出数组计算摘要，均使用 `sha256:` 前缀。这些摘要只标识快照，不代表业务验收。
- 成功采集仅允许一次。采集中途失败后句柄不可继续采集，原目录与已产生的孤儿 depot 字节保留；本包不 repair、不 GC、不删除故障现场。创建失败也保留已写入的部分目录。

## 信任与集成边界

本包不接收 Worker 自称的 `cleanup: true`。调用者必须先核对原 Runtime 的真实终态与所属进程清理；失败或归属未知时不能调用采集。句柄不支持重启后按路径接管；崩溃后的原执行目录应由上层保留证据并决定恢复方式。后继输入只能从已保存的 depot 引用重建，不能读取前序可变工作目录。

目录 FD、身份和摘要检查是 ordinary-user profile 的防漂移边界，不是敌对同 UID 或恶意代码沙箱。该包不提供操作系统级写入/网络隔离，也不隐藏本机凭据。Worker 与 Publisher 的权限分离仍须由 composition/Runtime 保证。

## 验证

```sh
node --test packages/task-files/index.test.mjs
```

测试使用真实文件系统和真实 `ArtifactDepot`，覆盖输入物化、正常文件写入后的采集、后继精确重建、输入漂移、链接/路径/目录身份、额外或缺失输出、大小上限及 depot 中途失败。测试不启动模型或 Worker；这些通过结果不等于真实团队业务验收通过。
