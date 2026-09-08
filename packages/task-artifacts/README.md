# Task 制品字节仓库

`ArtifactDepot` 是 ADR0088 下的本地内容寻址 bytes 组件，不保存 Task、归属、验收、ready、Operation 或引用历史；这些仍由 SQLite Application 唯一决定。它不执行制品，不启动 Agent，也不读取或复制凭据。

```js
import {ArtifactDepot} from './depot.mjs';
const depot = ArtifactDepot.create('/absolute/canonical/private-parent/artifacts');
const reference = depot.put(new Uint8Array([1, 2, 3]));
const bytes = depot.get(reference);
depot.close();
const reopened = ArtifactDepot.openExisting('/absolute/canonical/private-parent/artifacts');
```

接口同步，最大对象固定为 8 MiB。`put` 复制输入并返回 `{digest:'sha256:<64 lowercase hex>',bytes:number}`；`get` 返回独立 Buffer，校验长度与内容摘要，不接受调用者文件路径。错误只含稳定 `ArtifactDepotError.code`，不包含制品内容或宿主路径。`close` 幂等，关闭后不可继续使用。

## 文件与恢复边界

- `create` 只创建尚不存在的独立根，已存在的空目录也不接管。父目录必须已存在、归当前用户所有且路径 canonical；根目录为 `0700`，格式与对象文件为 `0600`。旧根、缺失或不完整 format、不兼容格式拒绝打开，无自动迁移。
- 持有父目录和根目录 fd，操作前后比较 inode/dev、路径与权限；对象使用 `O_NOFOLLOW` 打开，只允许当前用户的单链接普通文件。使用 Node 路径 API，不声称提供恶意同 UID 对手下的 `openat` 沙箱或消除所有内核级 ABA 竞态。部署时由单 owner Application 管理私有根，不能暴露给不可信 Worker 写入。
- `create` 与每次 `openExisting` 返回前均持有并校验精确 format 文件，依次 `fsync(format) → fsync(root) → fsync(parent)`，每一步后重验所持 format 的字节、身份、权限及目录绑定。同步失败保留现场且不返回可用实例；可见的正确 format 不代表已持久化，重开必须重新完成整条同步链，再次同步失败仍拒绝使用，不删除或重建已有根。
- 写入 `.pending-<uuid>` 非执行数据，完成文件 `fsync` 后以硬链接原子无覆盖安装，再移除本次临时链接并 `fsync` 根目录。重复 `put` 必须重新校验原 bytes，不能覆盖损坏对象，并同步对象与目录后返回。
- 写入故障使当前实例停止操作，保留已产生的文件证据；调用者关闭并用 `openExisting` 重新核实根。合法命名、私有普通文件形式的 pending 孤儿不作为引用或成功证据，不读取、不自动采用、不删除，也不阻断其他制品。未知外来文件拒绝重开。没有 GC 或自动修复。
- 若崩溃留下 pending 与目标的两条硬链接，该目标的 `get` 继续失败；其他已提交对象仍可读。这不允许凭临时数据补签交付。SQLite 事务必须在成功 `put` 后才提交引用；先写 bytes 后 SQLite 未提交产生的孤儿不是第二真值。
- `get` 遇到缺文件、截断、摘要不符、符号链接、额外硬链接或权限漂移一律失败。Depot 没有引用历史，无法区分“从未存过的 hash”与“被外部删除的同 hash”：Application 应通过已提交引用先 `get` 核验缺失，不得把再次 `put` 当作恢复既有验收或 SQL 事实。

定向验证：`node --test --test-concurrency=1 packages/task-artifacts/depot.test.mjs`。测试使用真实临时数据文件与确定性 I/O 故障注入，覆盖三层 bootstrap 同步顺序、各层 Create 失败后 Open 再失败与成功收口、冷重开 get/put、同步末尾身份及权限漂移；不构成物理断电证明。无模型、原生编译、SQLite 替代状态或实机交付完成声明。
