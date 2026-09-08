# 受信 Git 业务插件

本包提供真正的 Git 写节点：作者在锁定 base 的独立 detached Git worktree 中修改代码；原受管执行确认清理后，服务按实际字节采集 patch，再沿原 Artifact/独立 Verification/Decision 链验收。它不是把文件快照模式改名为 Git，也不注册新的 Workspace、资源实体或 HTTP 权限。

当前是有界组件与真实进程夹具验证，不代表真实模型 Git 团队、完整 B2 恢复或正式 production 已通过。原 `FileBusiness` 不作修改。

## 接入

```js
import {createGitBusiness} from './index.mjs';

const businessFactory = ({depot, executionParent, approvedLayout, observeExecution}) =>
  createGitBusiness({
    parent: executionParent, depot, approvedLayout, observeExecution,
    layoutFor: ticket => ticket.planDigest === null
      ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout,
    repositoryFor: (_ticket, request) => trustedRepositoryRoots.get(request.repositoryId),
    gitExecutable: '/usr/bin/git',
    // authorize 可选；必须只许可本节点原批准操作，默认全部拒绝。
  });
```

返回原 `prepare/collect/release/close` 接口，供 `TaskSupervisor` 的 `businessFactory` 使用。组合根提供显式、受信的本地仓库映射；不得把 HTTP 文本当作路径、命令或自动抓取指令。`repositoryFor` 只把业务描述里的标识映射到已获授权的本地目录，不自动扫描、克隆、注册或读取任意仓库。

公开输入复用现有 `Context.text`，例如其中的字符串为：

```json
{"profile":"task-git-input/v1","nodes":[{"nodeId":"library","repositoryId":"library","base":"0123456789012345678901234567890123456789","writePaths":["net.mjs"]}]}
```

上例 base 只是格式占位，必须替换为真实完整 commit ID。`gitDescription()` 解析严格、有界的业务数据；拒绝额外 root/argv、分支名、重复节点和路径逃逸。节点、repoId、base、写范围来自原 Task 输入，已包含在原 SQLite 输入/计划摘要中。组合根的 `bindPlan` 必须把该描述绑定到可见验收要求及具体节点；本包测试经过真实 HTTP schema 和精确批准，未扩展公开 Context 字段。

## 分配、采集和原制品合同

- 首个支持范围：本地 canonical、当前 UID 所有、组/其他人不可写的非 bare、非 linked-root 仓库；完整 SHA-1 commit；最多 64 个普通 tracked 文件、总计 8 MiB。拒绝 symlink、submodule、路径大小写别名、特殊文件与越界模式。
- 每个 Git 作者只能改 `writePaths` 中已有文件的内容。不支持新增、删除、重命名、修改 Git executable bit、NO_CHANGE 或任意额外文件。扩范围需要明确实现与验收，不能静默忽略未收集改动。
- Git 作者的原批准 `fileLayout` 必须为 `inputs:[]`、`allowedPaths:["changes.patch","git-context.json"]`；仓库源码来自原锁定 base，不伪造为 FileBusiness 的只读文件输入。Planner 与 Verifier 继续使用原文件模式，Verifier 输入只来自上游已接纳 manifest。
- `prepare` 创建 `git-worktrees/<workerId>` 的真实 detached、locked worktree，绝不接管既存目录。原库当前 checkout、分支 HEAD 和 index 不改变；Git 原生 worktree 元数据及新增 blob 对象会写入该受信原库的 `.git`。因此组合根必须已获准进行这种本地分配。
- 原 blob 直接有界物化，不执行 checkout filter、hooks 或凭据 helper。内部 Git 子进程不继承 HOME/登录或任意环境，不用 shell，不联网；Git 路径只由组合根配置。
- `collect` 先重验原 `observeExecution`、真实 cleanup、ticket 与截止时间，再核对原根/`.git`/lock、HEAD==base、全部 tracked 文件和未经授权内容。独立临时 index 从原 base 与实读字节生成精确 patch，不信任 Worker 修改的 index 或自报 patch。
- `git-context.json` 包含原 repo/base、node/worker、原 reservation/input/plan 摘要、patch 摘要和实际修改文件摘要；不导出本地绝对路径、Git 元数据或凭据。这是待独立验证的上下文制品，不是新 authority receipt。
- patch/context 进入单独的 FileBusiness 文件暂存目录，由原 `collect` 生成 `task-file-business/v1` manifest 并经原 Depot 持久化。原 Core 没有新增字段、跳过 layout/input/manifest 检查或放宽 Verification receipt。

## 停止、权限与恢复边界

本包不启动 Agent、不签发 Decision、不发信号、不 push/merge/commit，也不持有 Publisher 凭据。Provider/独立 checker 仍由原 Runtime 管理。Worker 与 Publisher 的实际 OS 身份、原生 Agent 登录和可达凭据必须由受信部署先满足；Git worktree lock 不是 OS 隔离，不能阻挡恶意同 UID 代码访问原库或其他路径，本包不宣称恶意代码 sandbox。

`release/close` **只关闭本包 FD**，不代表清理证明、不解锁、不删除或复用工作树。Supervisor 即使在 unknown 时调用 release，原 Git worktree、锁、私有 index、暂存与失败现场也保留，既存路径使相同 Worker 的再次分配拒绝。没有 cleanup 就不能 collect；没有业务成功就没有交付。暂停/取消/预算/容量/崩溃后的未决状态沿原 Core，不以磁盘目录或 `.git/locked` 代替 SQLite 权威，也不从旧路径恢复执行。安全回收与跨代工作树恢复不在本包范围。

## 独立验收

`checker.fixture.mjs` 是明确的受信业务夹具，不是生产默认验收器。它在每个相同 base 的**另外一个** locked worktree 应用原 patch，执行两个仓库模块的组合及负例，并检查无关文件；父 verifier 再对固定业务期望断言。Worker 不实现或调用该 oracle。最后客户端从 HTTP 下载完整 patch 包，在第三组消费 worktree 再应用与执行，并检查冷重开保持原回执和 bytes。

```sh
node --test --test-concurrency=1 packages/task-git-business/index.test.mjs
```

测试只创建自身临时仓库，运行固定 Node ACP fixture、Git 和原独立 command；不调用模型或修改用户业务仓库，不执行发布。覆盖真实双作者生命周期重叠、正确交付、适用但业务错误的 patch、越界修改、取消、unknown 保留、旧路径/HEAD 漂移、链接/额外文件和输入边界。进程全部通过原句柄停止后才删除测试私有目录；夹具的受控延时不是真实模型并行证据。
