# 受信本机 JSON 报告端口

本包实现 ADR0095 §6 的有限发布执行与只读后验，不持有 Task Store，不生成用户授权、ReviewDecision 或独立 Verification receipt。测试里的授权对象只验证端口行为，不能冒充真实 Task 的精确 allow。只有 Core 完成原批准、当前选果/Review/acceptance、授权、取消、期限和预算重查后，才可启动此端口。

## 固定接线

```js
const publication = createLocalReportPublication({
  id: 'local-report',
  root: '/absolute/private/report-directory',
  readBaseURL: 'http://127.0.0.1:8080/reports/',
  policy: {profile: 'task-local-json-report/v1', id: 'exact-report', version: '1'},
});
publication.assertDisjoint([serviceRoot, storeRoot, depotRoot, executionParent]);
```

root 与其直接父目录必须已存在、同 UID、0700，路径不能经过符号链接。构造时持有两者 FD；不创建、迁移或扫描目录。组合根必须在任何许可前传入所有原服务/Store/Depot/Worker 根做相交校验，不能只传一个无关路径后宣称已经验证部署布局。相交包含相等和双向祖先关系。未调用 `assertDisjoint` 时不能发布或查得肯定结果。URL 只接受固定 `http://127.0.0.1` 或 `http://[::1]`，无用户信息、query、fragment 或编码路径；GET 不跟随跳转、不继承环境/凭据。

返回 `id, policyDigest, configuration, configurationDigest, start, lookup, postverify, custodyProfile, assertDisjoint, close`。`policyDigest` 为原 Store `digest(encode(policy))`。只读 `configuration={profile,id,root,readBaseURL,policy,rootIdentity}` 还绑定原目录/父目录 dev/ino/uid/mode；组合根须将其完整冻结并在重新 open 的 owner claim 前比较，不能仅凭相同 targetId/policy 静默更换目标或 URL。`configurationDigest=digest(encode(configuration))`，不构成第二 Store。`postverify` 有独立 `id/start/custodyProfile`；由 Core 的原私有 VerificationPort 包装接纳，不把 raw result 当权威 receipt。两种执行都调用固定 Node `process.execPath` 和本包 `runner.mjs`，通过原 `launchCommand`/guard，支持原 `executionContext`/custody，环境为 `{}`，不自行清除任何 `extraScopes`。

- `nameFor({taskId,artifactDigest})` 固定产生 `<taskId>-<64位摘要>.json`，无模型自选名称。
- `start({ticket,prepared,executionContext})` 同步返回原形状 `{started,completion,stop}`。ticket 的内部类型为 `publication`，`input.publication={binding,authorization}`；binding 为 `actionId,targetId,name,artifactDigest,bytes,authorizationDigest` 六字段，authorization 为 ADR0095 原完整正文。
- `prepared.cwd` 中必须已有 `publication-input.json`，内容是原已验收制品的精确 bytes，0600、普通单链接文件。准备由受信 Core/Business 完成；端口不接受源文件名、命令、argv 或模型 URL。`prepared.prompt` 不构成授权。父进程及固定 child 各持有输入 FD，核验路径/inode/权限/长度/摘要，父 FD 保留至原 completion。
- `postverify.start` 使用内部 `postverify` ticket。`input.postverify={publicationReceiptDigest,targetId,name,artifactDigest,bytes,policyDigest,expected,interactionRefs,leaderReplyRefs}`；expected 必须由受信策略依据原业务要求/答案生成，不能取作者或 Leader 的 pass。当前固定算法验证完整有限 JSON 相等，而非只检查某个金额或 exit0。若外层 ticket.input 存在两类 refs，必须与后验输入完全一致。Core 仍负责每个引用的原事实及适用性重查。
- `lookup(binding,{signal,deadline})` 是有界同步只读，返回 `absent|matched|conflict|unknown` 及原六字段绑定/证据。matched 仅证明原名称下所需 bytes 当前可观察，不证明本次进程创建或目录项已经过新的耐久同步。任何无效归属、路径漂移、读失败、超界、过期/取消都不能返回肯定的 absent/matched。

## 文件、效果与后验

报告最多 1 MiB，JSON 拒绝重复键、BOM、NUL、非法 Unicode/UTF-8、非有限数值及原解析/编码上界。原 bytes 保留，不重序列化为“发布版”。子进程原子新建 `.pending-<nonce>`，0600，完整写入并 fsync，原子 hard-link 到最终名（已存在即不覆盖），再同步目录。最终名只暴露完整文件。仅当本次 O_EXCL 临时 inode、最终 inode 及持有 FD 一致且 final 已持久同步后，可删除这一个临时名并再次同步；不删除 final，不扫描或清理旧临时物。失败和归属不明一律保留。

原进程返回 `created|matched|failed|unknown` 的 publication completion。父进程核对 nonce、完整绑定、正常原进程终态/cleanup、输出完整性和当前文件；异常或丢失输出不能猜未发布。清理证明与外部效果是两回事。发布已经发生但 SQL 尚未提交时，Core 使用原 action 的 lookup 对账，不重新创建，也不倒填本执行成功。

后验是另一原所属执行：实际 GET 固定目标，严格 200/JSON、无压缩/redirect，限制 bytes 和原绝对 deadline，核验摘要、长度、完整业务期望与答案输入绑定；成功 delivery 仍使用原输入同 bytes。证据只记录绑定、摘要和四项固定检查，不含原报告/答案正文或秘密。后验失败不能撤销已发布事实，更不能决定 Task 整体成功。

普通宿主与同 UID 路径核验不是恶意代码沙箱或 OS 凭据分权。端口不提供站点、云发布、更新 latest、删除、Git push/merge、SQL 写入、任意 checker 或跨系统 exactly-once。`close()` 只在无活动原执行时关闭本端口持有 FD，不能替代 Execution 的原 stop/cleanup。

## 有界无模型测试

```sh
/Users/gawain/.local/share/fnm/node-versions/v24.15.0/installation/bin/node --test --test-concurrency=1 packages/task-publication-report/index.test.mjs
```

测试创建自有临时目录与固定 Node child；覆盖真实完整发布、matched/冲突、私有路径/输入漂移、大小/期限、取消、link 后 unlink 前后 SIGKILL、原 fsync/unlink 错误保留，以及单独 loopback GET 后验。故障注入只在 checked-in 测试 child 内记录/抛出原调用点，不修改产品文件。它们不是模型或 Task 批准证据；真实 HTTP 监听受宿主限制时必须记录未运行，不跳过后声称完整通过。
