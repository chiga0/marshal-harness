# 安装、升级、回滚与卸载

本文覆盖 Marshal CLI 面向用户的生命周期操作：安装、升级、回滚与卸载。支持平台为 `darwin|linux` × `amd64|arm64`，全程不请求 sudo。安装脚本自身的契约与手工验证步骤（面向维护者）见 [docs/development.md「安装」](https://github.com/chiga0/marshal-harness/blob/main/docs/development.md#安装)。

## 安装

### 一行安装脚本（推荐）

```bash
curl -fsSL https://raw.githubusercontent.com/chiga0/marshal-harness/main/scripts/install.sh | bash
marshal version
```

脚本行为：

1. 检测平台（`darwin|linux` × `amd64|arm64`）；
2. 查询 latest release，存在平台匹配资产（`marshal_<version>_<os>_<arch>`）时用 `curl -fsSL` 下载预编译二进制；
3. 强制下载并校验 `SHA256SUMS`；清单缺失、目标条目不是唯一一项或 sha256 不匹配时均 **fail closed**，不会安装已下载资产，也不会把该失败静默降级为源码构建；
4. 无匹配资产或下载失败时回退源码构建（见下节）；
5. 安装到 `~/.local/bin`（可用 `MARSHAL_INSTALL_DIR` 覆盖），完成后自动运行 `marshal version` 自检并输出下一步指引（`marshal init` / `marshal doctor`）。

二进制先写入安装目录下固定的 `.marshal-staging/marshal` 暂存路径，校验通过后复制为 `marshal` 并清理暂存目录，不会在随机路径生成匿名可执行文件。

### 离线校验 release 资产

从 GitHub release 手动下载资产时，请同时下载 `SHA256SUMS` 并在资产目录执行：

```bash
# Linux
sha256sum -c SHA256SUMS
# macOS（无 sha256sum 时）
shasum -a 256 -c SHA256SUMS
```

校验失败即不要使用该二进制。资产命名与校验清单格式的权威约定见 docs/development.md「Release 资产命名约定」。

### 源码构建

需要本机 Go 版本满足 `go.mod` 的 `go` 指令（当前 go 1.26）：

```bash
git clone https://github.com/chiga0/marshal-harness.git
cd marshal-harness
make build      # 输出 bin/marshal
```

安装脚本在以下情况自动走源码路径：无匹配 release 资产、release 下载失败、或设置 `MARSHAL_FORCE_SOURCE=1`；无本地 checkout 时会先浅克隆仓库（`MARSHAL_TAG` 已固定时克隆对应 tag）。

## 升级

**重装即升级**：直接再次运行一行安装脚本即可，脚本会查询 latest release 并覆盖安装目录中的旧二进制。固定升级到某一版本可显式指定：

```bash
MARSHAL_TAG=v0.2.0 bash scripts/install.sh
```

GitHub 的 `latest` API 不返回 prerelease；安装 unsigned RC 必须显式固定精确 tag，且仍执行同一套 fail-closed checksum 门禁：

```bash
MARSHAL_TAG=v1.0.0-rc1 bash scripts/install.sh
```

RC 是候选/预览资产，不代表已满足 Issue #212 的 macOS 签名/notarization 或 v1.0 `RELEASED` 门禁。

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

- Gatekeeper 可能拦截未签名二进制；经浏览器下载的文件会带 `com.apple.quarantine` 属性，确认来源与校验和可信后可执行 `xattr -d com.apple.quarantine <文件>` 放行。
- 经安装脚本（curl）下载的文件一般不带 quarantine 属性；若仍无法执行，先运行 `sha256sum -c SHA256SUMS` 确认完整性，再用 `marshal doctor --json` 做只读诊断。

签名/notarization 链路（ADR 0047/0048）落地后本节会同步更新。
