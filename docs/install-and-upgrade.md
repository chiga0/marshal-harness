# 安装、升级、回滚与卸载

本文覆盖 Marshal CLI 面向用户的生命周期操作：安装、升级、回滚与卸载。支持平台为 `darwin|linux` × `amd64|arm64`，全程不请求 sudo。安装脚本自身的契约与手工验证步骤（面向维护者）见 [docs/development.md「安装」](https://github.com/chiga0/marshal-harness/blob/main/docs/development.md#安装)。

> **RC1 状态（2026-08-29）**：[ADR 0068](adr/0068-mac-first-cli-only-lifecycle-preview-rc1.md) 的 installer guard 已实现，但 `v1.0.0-rc1` 仍尚未发布。它只允许 Darwin arm64、精确 tag 和显式 `MARSHAL_LOCAL_DOGFOOD_PREVIEW=1`；缺少精确 RC1 资产时必须 fail closed，不得回退源码、其它平台资产或 stable/latest，也不得由安装器自动生成或激活 `LocalDogfoodActivationV1`。安装命令只在 release 真实存在后才能成功。

## 安装

### 一行安装脚本（推荐）

```bash
curl -fsSL https://raw.githubusercontent.com/chiga0/marshal-harness/main/scripts/install.sh | bash
marshal version
```

脚本行为：

1. 检测平台（`darwin|linux` × `amd64|arm64`）；
2. 查询 latest release，存在平台匹配资产（`marshal_<version>_<os>_<arch>`）时用 `curl -fsSL` 下载预编译二进制；
3. release tag 必须是 annotated tag；脚本从 canonical Git remote 解析唯一 tag object/peeled commit 并获取 canonical tag message，再下载 `RELEASE-MANIFEST` 与 `SHA256SUMS`；tag message 冻结的 sourceHead、manifest SHA 与 Darwin arm64 candidate SHA 必须和下载内容对账，manifest 的 repository/tag/sourceHead/buildDate/toolchain/flags/四平台资产集合也必须精确；任一缺失、重复、尾随字段、资产整组替换或漂移均 **fail closed**；
4. 非 RC1 安装在无匹配资产或下载失败时回退源码构建（见下节）；若指定了 `MARSHAL_TAG`，源码 checkout 的 `HEAD` 必须精确等于该 tag 的 peeled commit，否则 fail closed，禁止把任意源码标记成请求版本。`v1.0.0-rc1` 不得进入这条回退路径；
5. 安装到 `~/.local/bin`（可用 `MARSHAL_INSTALL_DIR` 覆盖）；首次写入前逐段验证安装路径、staging 和既有 target 的 owner/mode/non-symlink/hardlink 边界。下载对象保持 `0644` 且不可执行，只有 tag/manifest/checksum 全部闭合后才通过 no-clobber hardlink 激活固定 `.marshal-staging/marshal`，再运行 `marshal version --json`；release 路径精确核对 `version`、peeled `commit`、manifest `buildDate/goVersion` 与 `selfProfile`，源码路径核对自身 `HEAD`；任一执行失败、字段缺失或身份漂移都 fail closed；
6. Darwin 安装资产与源码回退固定 `selfProfile=darwin-local-dogfood`，Linux 固定 `selfProfile=unprofiled`。前者只是 ADR 0051 的 Mac ordinary-user/non-production 能力，不得描述成 hardened 或正式 production authority。

二进制 bytes 先写入安装目录下固定且不可执行的 `.marshal-staging/marshal.candidate`；校验通过后以 no-clobber hardlink 激活固定 `.marshal-staging/marshal`，再原子替换目标 `marshal` 并清理本次拥有的暂存对象。脚本不会在随机路径生成或执行匿名可执行文件，也不会跟随安装路径、staging 或目标 symlink；不安全 owner、group/world 可写路径、stale staging 与 hardlink target 都会被拒绝。

### `v1.0.0-rc1` 显式预览安装

RC1 发布后，只能在 Darwin arm64 上使用以下精确命令：

```bash
MARSHAL_TAG=v1.0.0-rc1 \
MARSHAL_LOCAL_DOGFOOD_PREVIEW=1 \
bash scripts/install.sh
```

`MARSHAL_LOCAL_DOGFOOD_PREVIEW` 只接受精确值 `1`，且只能与 `MARSHAL_TAG=v1.0.0-rc1` 同时使用。缺少 opt-in、任意其它值、未显式指定 tag、非 Darwin arm64、`MARSHAL_FORCE_SOURCE`、资产或网络失败以及 manifest/identity 漂移全部 fail closed。安装器只下载该 tag 的精确 Darwin arm64 binary、`RELEASE-MANIFEST` 和 `SHA256SUMS`，并在执行前与安装后重验同一 SHA-256、size 和 build identity。

安装器不创建 activation，不修改 Gatekeeper、SIP、EDR、`PATH` 或符号链接。成功后它会输出固定绝对路径的 `version --json`、`doctor --self` 与操作者显式写入/selection activation 的指引；必须在当前受信任 canonical repository 内按顺序执行。`$PATH` 命中和 convenience symlink 都不是 trust anchor。

### 离线校验 release 资产

从 GitHub release 手动下载资产时，请同时下载 `RELEASE-MANIFEST` 与 `SHA256SUMS` 并在资产目录执行：

```bash
# Linux
sha256sum -c SHA256SUMS
# macOS（无 sha256sum 时）
shasum -a 256 -c SHA256SUMS
```

该命令只验证 bytes；安装脚本还会验证 annotated tag peeled commit 与二进制内嵌身份。任一校验失败即不要使用该二进制。资产命名与 manifest 约定见 docs/development.md「Release 资产命名约定」。

### 源码构建

需要本机 Go 版本满足 `go.mod` 的 `go` 指令（当前 go 1.26）：

```bash
git clone https://github.com/chiga0/marshal-harness.git
cd marshal-harness
make build      # 输出 bin/marshal
```

非 RC1 安装脚本在以下情况自动走源码路径：无匹配 release 资产、release 下载失败、或设置 `MARSHAL_FORCE_SOURCE=1`；无本地 checkout 时会先浅克隆仓库（`MARSHAL_TAG` 已固定时克隆对应 tag）。源码路径要求无未提交修改、可验证的 Git checkout，并把精确 `HEAD`、构建时间和平台 profile 写入 build info；指定 `MARSHAL_TAG` 时还会要求 `HEAD == refs/tags/<tag>^{commit}`。`v1.0.0-rc1` 明确禁止源码构建和这条 fallback。

## 升级

**重装即升级**：直接再次运行一行安装脚本即可，脚本会查询 latest release 并覆盖安装目录中的旧二进制。固定升级到某一版本可显式指定：

```bash
MARSHAL_TAG=v0.2.0 bash scripts/install.sh
```

GitHub 的 `latest` API 不返回 prerelease。`v1.0.0-rc1` 当前尚未发布；发布后必须使用上文的精确 tag + preview opt-in 命令，不会由 latest/stable channel 自动获得。RC 是候选/预览资产，不代表已满足 Issue #212 的 macOS 签名/notarization 或 v1.0 `RELEASED` 门禁。

RC 的 annotated tag 目前是 unsigned。封闭 tag message 能在 **同一 tag object 未变化** 时发现 release assets、manifest 或候选摘要被替换，但它不是签名、透明日志或 anti-rollback authority；若 canonical remote ref 连同 tag object 被整体重指，普通安装器没有外部高水位可据此识别降级或历史替换。使用者必须显式固定期望 tag，并在需要强 anti-rollback 时等待 Issue #212 的受保护签名/发布链，不能把当前 RC 门禁表述为稳定分发保证。

升级后用 `marshal version` 确认实际生效的版本号。升级前建议确认没有进行中的 Run（见「`.marshal/` 状态目录与版本兼容」）。

## 回滚

Marshal 采用单一静态二进制分发，回滚就是固定安装上一版本的 release：

```bash
# 固定到上一个可用版本重新安装
MARSHAL_TAG=v0.1.0 bash scripts/install.sh
marshal version   # 确认已回到期望版本
```

### `.marshal/` 状态目录与版本兼容

如实说明当前版本策略，不做超出实现的承诺：

- 每个仓库的本地 Run、journal/control 记录、日志与缓存位于被 Git 忽略的 `.marshal/`，**不进入业务提交**；升级或回滚二进制不会修改、迁移这些状态。
- runstore 快照与 journal/control 记录均携带 `apiVersion`（当前为 `marshal.dev/v1alpha1`）；读取到无法识别的 `apiVersion` 时 Marshal 按 **fail closed** 报冲突，而不会猜测解释。
- 当前版本**没有状态迁移/降级工具**：由更新版本写入的状态能否被旧版本读取**未覆盖、未承诺**。回滚前请确认没有进行中的 Run；如需保留证据，先备份（或另存）`.marshal/` 再回滚。
- 若旧版本因状态不兼容 fail closed，中止的 Run 与证据仍会保留在 `.marshal/` 中；恢复指引见 [操作手册](https://github.com/chiga0/marshal-harness/blob/main/docs/operator-runbook.md)。

## 卸载

1. 删除二进制：

   ```bash
   rm ~/.local/bin/marshal   # 或 $MARSHAL_INSTALL_DIR/marshal
   ```

2. 清理安装脚本可能遗留的暂存目录：`<安装目录>/.marshal-staging`（脚本正常结束时已自动清理）。
3. 按需删除各仓库 checkout 内的 `.marshal/` 状态目录。删除前请确认不再需要其中的 Run 证据；该目录保存 Outcome 与审计所需信息，删除不可逆。

Marshal 不写其他系统路径，无后台常驻进程需要停止（`marshal-server` 尚不属于本地 MVP 调用链）。

## macOS 未签名资产说明（Issue #212）

在 [Issue #212](https://github.com/chiga0/marshal-harness/issues/212) 解决、签名身份与 notarization 凭据完成 provision 之前，release 的 darwin 资产为未签名构建（release notes 会明确标注 unsigned build）：

- Gatekeeper 或企业 EDR 可能拦截未签名二进制；经浏览器下载的文件还可能带 quarantine 属性。先核对 canonical tag、manifest 与 checksum，再通过 macOS「系统设置」中的显式批准流程或企业管理员针对稳定路径/身份配置 allowlist；若策略仍拒绝，安装保持 blocked。
- 不要关闭 Gatekeeper/SIP/企业安全软件，也不要用删除 quarantine/xattr 等命令绕过策略。经安装脚本（curl）下载的文件若仍无法执行，保留失败、固定路径和摘要证据，由系统/企业安全策略明确批准后再重试；批准前不能用 `marshal doctor` 的执行失败冒充可用。

签名/notarization 链路（ADR 0047/0048）落地后本节会同步更新。
