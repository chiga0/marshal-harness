# 开发指南

## 当前阶段

Marshal 的产品目标是[整体架构](architecture.md)定义的长寿命确定性 Control Plane；当前仓库已完成 Milestone 0–6，embedded/local 先行实现（Local MVP）标记 `USABLE`（见 [roadmap-status.md](roadmap-status.md)）。版本与发布语义见仓库根目录的 [CHANGELOG.md](https://github.com/chiga0/marshal-harness/blob/main/CHANGELOG.md)；阶段推进以 tag 为准。

## Go 基线

- Module：`github.com/chiga0/marshal-harness`；
- Language Version：Go `1.26.0`；
- Toolchain：Go `1.26.6`；
- JSON Schema：Draft 2020-12；
- Glob：`doublestar/v4`，`**` 表示跨目录递归；
- 静态检查：`go vet` 与固定版本的 `staticcheck`（经 `go.mod` 的 `tool` 指令固定，Go 1.24+ 特性；请勿自行 `go install` 其他版本，以免结果不一致）；
- 交付形式：单一 `marshal` 可执行文件；
- 工具链：Go 低于 `go.mod` 要求时 toolchain 自动下载需要网络；受限环境请预先安装匹配版本。

GitHub remote 已绑定为 `github.com/chiga0/marshal-harness`，与 Module Path 一致。

## Package 边界

```text
cmd/marshal/        CLI 进程入口
internal/cli/       参数解析与退出码
internal/cleanup/   Cleanup Preview、Guard、Crash-safe Tombstone 与显式 Apply
internal/app/       Application Service 与依赖装配
internal/domain/    Provider-neutral Domain Type
internal/contract/  Schema 编译与 Semantic Validator
internal/port/      Worker、Verifier、Lead Agent、Publisher Port
internal/adapter/   Adapter Registry、Fake 与 OpenCode Worker
internal/gitworktree/ Repository Identity、linked worktree 与锁
internal/verification/ 独立 Diff、Scope、Command 与 Artifact 验证
internal/review/   ReviewPacket、Decision Guard、Outcome 与崩溃安全记录
internal/publication/ 受控 Commit、发布证据门禁与远端 CI 状态
internal/publisher/github/ 独立凭据的 GitHub Draft Publisher
schemas/            JSON Schema、Fixture 与 Embedded FS
.agents/skills/marshal/ Codex CLI/Desktop 共用的轻量 Skill
```

Core Domain 与 Contract Package 不得导入 Provider-specific Package。CLI 只作为 Application Service 的薄入口。

## 本地命令

未受企业终端策略限制的 Linux/macOS 环境可运行完整门禁：

```bash
make format
make vet
make lint
make test
make build
make check
make vuln
make ci
```

`make check` 执行 Format Check、Vet、Staticcheck、Race Test 与 Build；`make ci` 在此基础上执行 `govulncheck`。构建结果默认位于 `bin/marshal`，该目录被 Git 忽略。

当前维护者 Mac 的企业终端策略会按新 Mach-O/CDHash 拦截未签名 Go test binary。在这类环境中禁止运行 `go test`、`make test`、`make check` 或 `make ci`，也不得移除安全属性或逐个批准临时二进制。允许的本地门禁是：

```bash
make format-check
make architecture-check
make vet
make lint
make build
make vuln
git diff --check
```

需要编译反馈时，可对命中的 package 使用 `go test -race -c -o <临时路径> <package>`；该产物只作为 compile-only 证据，不得执行，并须在验证后删除。本机固定 `./bin/marshal` 可用于 live canary，但不能把上述静态/compile-only 结果表述为 unit/race 已通过。unit/race 的权威执行证据来自相同 sourceHead 的 required GitHub macOS + Linux CI。

GitHub Actions 的主线 CI 固定展开为三个 job：`Quality (ubuntu-latest)`、`Quality (macos-latest)` 与 `Secret scan`。Linux/macOS 执行同一质量门禁和漏洞扫描；只有 Ubuntu quality runtime 在 checkout 后、任何候选 Make/script/toolchain 或 `GITHUB_ENV` 写入前，以固定 `/bin/bash`、`/usr/bin/env -i`、`/usr/bin/python3 -I -B` 和封闭 `PATH` 直接执行 release authority checker。checker 先绑定完整 workflow、Makefile、自身与五个发布/安装 fixture，再由同一受限 step 直接逐项执行五个 fixture，并在每项前后重新校验 source binding。`make release-check` 只是本地便利入口，不是 CI authority。checker 对 absolute repository root、固定 path 的 non-symlink regular object、held-fd bytes 与 Git `HEAD` tree mode/blob 做精确绑定；随后对已审查的完整 workflow source 和 `release-check` recipe 做字节级 fail-closed 校验。flow-style/重复 key、额外 matrix 维度/include、Windows、第四 job、gate 迁移/注释、`continue-on-error`、Make shell/ignore/include 改写或环境污染都必须显式更新合同并重新审查。外部 Action 固定到完整 Commit SHA，工作流默认只有 `contents: read` 权限。

## 安装

> **RC1 installer guard**：[ADR 0068](adr/0068-mac-first-cli-only-lifecycle-preview-rc1.md) 的 guard 已实现，但 `v1.0.0-rc1` 尚未发布。RC1 只能在 Darwin arm64 上通过 `MARSHAL_TAG=v1.0.0-rc1` + `MARSHAL_LOCAL_DOGFOOD_PREVIEW=1` 安装；精确资产缺失、网络失败、平台不符或 opt-in 缺失时 fail closed，禁止进入下述通用源码回退，安装器也不得自动生成或激活 `LocalDogfoodActivationV1`。

面向用户的两条安装路径（一行脚本与源码构建）见 [README](https://github.com/chiga0/marshal-harness#安装)，对应脚本为 [`scripts/install.sh`](https://github.com/chiga0/marshal-harness/blob/main/scripts/install.sh)：

- 检测平台（`darwin|linux` × `amd64|arm64`）；
- 存在 `v*` annotated tag 的 GitHub release 且含平台匹配资产时，用 `curl -fsSL` 下载预编译二进制；随后解析 tag 的唯一 tag object/peeled commit 与 canonical candidate trailers，下载 `RELEASE-MANIFEST` 与 `SHA256SUMS`，并要求 tag 冻结摘要、manifest/目标资产摘要与 sourceHead 全部精确。非 RC1 安装器继续执行 build identity 自检；RC1 的 Mach-O/buildinfo 由 release admission 预先做非执行式校验，安装器只消费同一 payload，绝不执行 candidate；
- 否则回退源码构建 `go build -trimpath ./cmd/marshal`（Go 版本须满足 `go.mod` 的 `go` 指令）；无本地 checkout 时先浅克隆 `https://github.com/chiga0/marshal-harness.git`（release tag 已知时克隆该 tag）；
- 安装到 `~/.local/bin`（默认），全程不请求 sudo，完成后输出下一步指引（`marshal init` / `marshal doctor`）。

源码回退不是弱身份旁路：源码目录必须是无未提交修改、可验证的 Git checkout；指定 `MARSHAL_TAG` 时，当前 `HEAD` 必须精确等于该 tag 的 peeled commit。构建会嵌入精确 commit，并按平台冻结 `selfProfile`（Darwin=`darwin-local-dogfood`，Linux=`unprofiled`）。源码与非 RC1 暂存/安装对象必须通过 `version --json` 身份自检；RC1 安装器不执行 candidate。

安装阶段先把 bytes 写入安装目录下固定、`0644` 且不可执行的 `.marshal-staging/marshal.candidate`；tag/manifest/checksum 闭合后才通过 no-clobber hardlink 激活固定 `.marshal-staging/marshal`，再原子替换目标并清理本次拥有的对象。RC1 在下载后、执行前（固定 staging 激活后）、安装后三次重验同一 SHA-256/size，并拒绝 symlink、超过 256 MiB 或漂移对象；路径每一段、staging 与既有 target 都必须满足 owner/mode/non-symlink/hardlink 门禁，不会在随机 `/tmp` 路径生成或执行匿名 Marshal 可执行文件。

环境变量：`MARSHAL_INSTALL_DIR`（安装目录）、`MARSHAL_TAG`（固定 release tag，跳过 latest release 查询）、`MARSHAL_FORCE_SOURCE=1`（跳过 release 直接源码构建）、`MARSHAL_LOCAL_DOGFOOD_PREVIEW=1`（仅能与 exact `v1.0.0-rc1` 同时使用的封闭 opt-in）。RC1 禁止 `MARSHAL_FORCE_SOURCE`、latest/stable/源码/其它平台 fallback，只接受精确 8 行单资产 manifest 与固定顺序、两个 ASCII 空格分隔、两个小写 digest 的 `SHA256SUMS`。release/source remote authority 固定为 canonical `chiga0/marshal-harness`，`MARSHAL_REPO` 覆盖被显式拒绝，fixture 只能通过测试进程中的 fake `git`/`curl` 模拟网络响应。

### Release 资产命名约定

`scripts/install.sh` 依赖的 release 资产约定（后续 release 工具链必须遵守）：

- `marshal_<version>_<os>_<arch>`：预编译二进制。`version` 为去掉 `v` 前缀的 release tag，`os`/`arch` 取 Go 风格 `darwin|linux` × `amd64|arm64`（如 `marshal_0.1.0_darwin_arm64`）；
- `RELEASE-MANIFEST`：canonical 11 行 closed manifest，绑定 canonical repository、tag、peeled `sourceHead`、commit UTC `buildDate`、精确 Go toolchain/build flags，以及四个平台资产的 SHA-256、size 与 profile；
- `SHA256SUMS`：四个平台资产与 `RELEASE-MANIFEST` 的校验清单，`sha256sum` 格式（`<hash>  <文件名>`）。

release workflow 只接受精确的 `vMAJOR.MINOR.PATCH` 或 `vMAJOR.MINOR.PATCH-rcN` annotated tag。tag message 必须由 `scripts/release-contract.sh candidate-tag-message` 生成并冻结 canary 的 sourceHead、manifest SHA 与 Darwin arm64 asset SHA。候选构建固定在 `macos-14` 的 `release-candidate-build` 环境中执行：read-only build job 先要求同 sourceHead 的主分支 CI 三个 job 全绿，再以 commit 的 canonical UTC timestamp、`go.mod` 精确 toolchain、`CGO_ENABLED=0`、`-trimpath -buildvcs=false -mod=readonly` 与空 build ID 只执行一次 `make dist`；manifest/asset SHA 与 tag 任一不等即 fail closed。该 job 不持有 `contents:write`，上传后同时导出 GitHub artifact ID、GitHub artifact digest 与内部 payload SHA。后续 job 必须按 artifact ID 通过 API 下载原始 ZIP，对实际 ZIP bytes 计算 SHA-256，并与 upload output 及 REST metadata digest 三方 fail closed 对账，通过后才按两个固定 member 受控解包；不得重新构建。独立 publish job 随后再核对内部 payload SHA、封闭 tar member、`SHA256SUMS`、manifest 与 exact 6-line tag message，最后才获得发布动作。`release.yml` 的完整 bytes 同时由 `scripts/release-workflow.sha256` 的版本化摘要封闭；任何 workflow 变更都必须显式更新摘要并作为一个整体重新评审，不依赖可被注释伪装的 substring 门禁。

`release-candidate-build` 环境名、GitHub-hosted runner 与 artifact digest 目前只形成**不可变候选的传输基础**，不等于 ADR 0048 的 protected-builder authority。要把该 candidate 交给 signing/install/canary 并产生权威 attestation，仍须完成 sealed source/compile/external-material、authenticated `MarshalArtifactBuildRecordV1`、外部 current builder principal、独立 artifact signer 与 deployment/install current authority；环境管理员也必须在仓库外配置 branch/tag policy 和审批，不能把“使用了 GitHub Environment”写成签名或 allowlist 证据。稳定 tag 在 Issue #212 的真实签名/notarization 链落地前保持 fail closed；RC 只能发布明确标注为 unsigned 的 prerelease。unsigned tag 只防同一 tag object 下的资产替换，不提供签名、remote-ref anti-rollback 或整体 tag retarget 防护。

`make dist` 对四个平台显式区分自身份：Darwin 资产固定为 ADR 0051 的 `darwin-local-dogfood` ordinary-user/non-production profile，Linux 资产保持 `unprofiled`。`scripts/dist-profile_test.sh` 以确定性 fake compiler 记录并断言四个 target 的 linker profile，防止 release workflow 再次产出不可启动的 Darwin `unprofiled` 资产。

### 手工验证

安装脚本不直接纳入跨平台 `make check`（shellcheck 不是本仓库的冻结依赖）；它的确定性 fixture 由 release workflow 和本地便利入口 `make release-check` 执行。修改 release/installer 后还应按以下步骤手工验证：

1. 干净 checkout 内运行 `bash scripts/install.sh`（本地源码构建路径）；
2. `MARSHAL_INSTALL_DIR=<空目录> bash scripts/install.sh` 验证自定义安装目录；
3. `MARSHAL_FORCE_SOURCE=1 bash scripts/install.sh` 验证强制源码构建路径；
4. 安装后运行 `marshal version` 与 `marshal doctor --json` 确认二进制可用。

RC1 另外必须运行 `bash scripts/install_test.sh`，该 fixture 覆盖无 opt-in、错值、未精确指定 tag、非 Darwin arm64、missing/download failure、额外平台/stable/其它 RC、非 canonical checksum 空白/大小写、manifest/SHA 漂移、symlink/oversize、三阶段 bytes/size 漂移、禁止源码 fallback/自动 activation，以及不会执行 candidate 的 exact positive asset。安装器只输出固定二进制的 `version --json`→`doctor --self`→操作者显式 activation 指引；三步均由操作者后续显式执行，安装器不修改 Gatekeeper/SIP/EDR/用户 `PATH`。

## 当前 CLI

```bash
marshal version [--json]
marshal doctor [--run RUN_ID] [--repair] [--print-env] [--json]
marshal doctor --self [--repository-root PATH] [--activation-id ID] [--valid-for DURATION]
marshal contract validate [--schema NAME] <PATH|->
marshal contract schema [--all [--out DIR]] [--schema NAME] [--json]
marshal init [--json]
marshal task status --run RUN_ID [--json]
marshal task plan --task PATH --policy PATH --run RUN_ID [--json]
marshal task approve --run RUN_ID --gate plan|publish [--actor ID] [--json]
marshal task run --run RUN_ID [--through-verify] [--json]
marshal task verify --run RUN_ID [--json]
marshal task review --run RUN_ID [--decision PATH] [--json]
marshal task publish --run RUN_ID [--json]
marshal task accept --run RUN_ID [--json]
marshal task abort --run RUN_ID --actor ID --reason TEXT [--json]
marshal task cleanup --run RUN_ID [--apply] [--json]
marshal task <COMMAND>
```

`version`、`doctor`、`contract validate`、`task status` 和不带 `--apply` 的 `task cleanup` 是只读命令；`contract schema` 除 `--all --out DIR` 外也是只读命令，该形态仅向用户显式指定的目录写入导出文件，不触碰仓库状态。`marshal init` 创建仓库绑定的状态目录并写入 Git exclude。`task run` 使用冻结选择的 Worker Adapter；`verify`、`review`、`publish`、`accept` 分别执行独立证据门禁。发布命令要求 absolute `MARSHAL_GH_PATH` 与独立 `MARSHAL_GH_CONFIG_DIR`。Cleanup 默认只列出精确 managed worktree；只有显式 `--apply` 才执行，并拒绝 Active Lease、非终态 Run、缺失 Outcome、活跃 TerminalSession、dirty/symlink/身份不明目标。它不删除 Run 证据、本地 branch、远端 branch 或 PR。

示例：

```bash
make build COMMIT="$(git rev-parse HEAD)"
./bin/marshal doctor --json
./bin/marshal contract validate --schema task-spec schemas/examples/happy-path/task-spec.json
./bin/marshal contract schema
./bin/marshal contract schema --all --out /tmp/marshal-schemas
```

开发与操作示例固定调用 `bin/marshal`；不要用 `go run ./cmd/marshal`，因为 Go 会在缓存或临时目录生成匿名可执行文件，无法复用 Marshal 的稳定路径身份。

### Darwin 本地 dogfood 自身份

LD-2 为开发机上的固定 `bin/marshal` 提供低保证、普通用户级 opt-in，不代表 managed/release authority，也不提供恶意代码 sandbox。构建时必须显式冻结 profile 与 source head；`doctor --self` 仅把 canonical activation 输出到 stdout，activation 文件由 operator 创建，Marshal 不写该文件：

```bash
make build COMMIT="$(git rev-parse HEAD)" SELF_PROFILE=darwin-local-dogfood
install -d -m 700 .marshal/bootstrap
umask 077
./bin/marshal doctor --self --repository-root "$(pwd -P)" \
  > .marshal/bootstrap/local-dogfood-activation.json
chmod 600 .marshal/bootstrap/local-dogfood-activation.json
export MARSHAL_LOCAL_DOGFOOD_ACTIVATION="$(pwd -P)/.marshal/bootstrap/local-dogfood-activation.json"
./bin/marshal doctor --json
```

activation 最长有效 24 小时（默认 8 小时），严格绑定 canonical repository root、当前固定 executable 的路径对象与 SHA-256、`sourceHead` 和 `selfProfile`；Schema 位于 `schemas/selfidentity/local-dogfood.schema.json`。LD-2 放行 `doctor`、`init`、`task scaffold`、`task plan`、`task status` 与 `task approve --gate plan`；`help`、`version`、`doctor --self` 以及带 `--attestation-ready` 握手、无 Core/仓库持久副作用的六个有界 `internal *-check` primitive 属于 bootstrap surface，其中 plan checker 会执行受限 Adapter version/capability probe。其它 internal/credentialed、Worker launch、verify/review 的 Attempt lineage 留在 LD-3；publication、publish approval与 remote surface 继续机械拒绝。

完整 `doctor --json` 会同时输出 Core-owned `selfIdentity` 和可复制的 `policyEnvironmentBinding` 投影。操作者或上层编排器把该 closed binding 放入本地 Run 的 `PolicySnapshot.environmentBinding`，并按既有规则重新封装 `policyDigest`；Marshal 不静默改写或签发 PolicySnapshot。`task plan` 在任何 repository/Adapter/持久化副作用前要求 binding 与当前 observation 精确一致，随后原样冻结 PolicySnapshot；`task status` 与 `task approve --gate plan` 会重新读取冻结 policy、核对 `PolicyDigest` 和当前身份。activation 被替换、过期或跨 profile 复用时均 fail closed，历史无 binding 的 PolicySnapshot 只能按 non-local/legacy 记录读取，不能升级成 local Run。

Darwin 上 `unprofiled` 或尚未实现自身 gate 的 profile 不能进入上述 surface；非 Darwin 构建保持既有行为。

## 契约自描述

`marshal contract schema` 从二进制内嵌的 Schema 目录（`schemas/` Embedded FS）导出契约自描述，供 Agent 与外部工具零先验消费，导出字节与内嵌内容逐字节一致：

- 无参数：逐行输出全部 Schema 名与版本（如 `task-spec v1alpha1`）；`--json` 输出同内容的 JSON 数组；
- `--schema NAME`：将单个命名 Schema 原样输出到 stdout，`NAME` 与 `contract validate --schema` 共用同一套 kebab-case 名称；
- `--all`：将完整目录（每个 Schema 的 `name`、`kind`、`version`、`$id`、文件字节 SHA256 与 examples 清单）以 JSON 输出到 stdout；
- `--all --out DIR`：在 `DIR` 写出 `<name>.schema.json`（0644）、`examples/happy-path/<name>.json`、`examples/invalid/<name>.json` 与 `catalog.json` 目录清单，全部与内嵌字节一致；重复导出幂等覆盖。

## 环境变量参考

| 变量 | 用途 | 性质 |
| --- | --- | --- |
| `MARSHAL_STATE_DIR` | 覆盖默认 `.marshal/` 状态目录（绝对路径；仓库内则必须等于默认目录） | 运行配置 |
| `MARSHAL_LOCAL_DOGFOOD_ACTIVATION` | Darwin local-dogfood operator-owned canonical activation 绝对路径；仅对 `darwin-local-dogfood` profile 生效 | 本地自身份 opt-in |
| `MARSHAL_OPENCODE_PATH` | OpenCode Worker 可执行文件绝对路径 | Adapter 注册 |
| `MARSHAL_QWEN_PATH` | Qwen Code Worker 可执行文件绝对路径 | Adapter 注册 |
| `MARSHAL_QODER_PATH` | Qoder CLI Worker 可执行文件绝对路径；仅设置此变量不会通过 live conformance 门禁 | Adapter 注册候选 |
| `MARSHAL_QODER_MODE` | 仅接受显式值 `ordinary-user`；在 Mac 上按普通用户子进程运行 Qoder，不提供 signed authority | 显式降级能力 |
| `MARSHAL_CODEX_MODE` | 仅接受显式值 `ordinary-user`；在 Mac 上按普通用户子进程运行 Codex，不提供 signed authority | 显式降级能力 |
| `MARSHAL_PI_PATH` | Pi Worker 可执行文件绝对路径 | Adapter 注册 |
| `MARSHAL_GH_PATH` | Publisher 的 `gh` 可执行文件绝对路径 | 发布凭据边界 |
| `MARSHAL_GH_CONFIG_DIR` | Publisher 独立凭据目录（不复用 ambient 配置） | 发布凭据边界 |
| `MARSHAL_CMUX_PATH` | cmux 可执行文件路径（受监督 Pilot） | 可选后端 |
| `MARSHAL_LIVE_CMUX` | 启用 cmux Live E2E | 测试开关 |
| `MARSHAL_LIVE_GITHUB` / `MARSHAL_LIVE_GITHUB_FIXED_SUFFIX` | 启用 GitHub Publisher Live 测试（后者固定 branch/PR 后缀以便幂等重跑） | 测试开关 |
| `MARSHAL_LIVE_OPENCODE_PATH` / `MARSHAL_LIVE_QWEN_PATH` / `MARSHAL_LIVE_PI_PATH` | 启用对应 Adapter 的 Live E2E | 测试开关 |
| `MARSHAL_DISCOVERY_KNOWN_LOCATIONS` | 覆盖 doctor 建议式发现的已知安装位置列表；`-` 表示禁用，仅保留 PATH 扫描 | 测试开关 |

所有 `*_PATH` 变量只接受绝对路径；注册本身不搜索 PATH、不猜近似名（未设置时 `marshal doctor` 的 workers 段显示 `not-configured`）。`marshal doctor` 的 discovery 段额外提供建议式发现，仅作建议、绝不自动注册；具体流程见下文“部署到新环境”。Live 测试默认跳过，CI 不依赖任何 Live 开关。

## 部署到新环境

新环境部署的目标是把“读文档自己猜”变成“doctor 给建议、用户一行注册”。核心原则：**discovery advisory，registration explicit**——doctor 只发现并建议，注册仍须显式环境变量；这保持了“Registry 不得静默替换二进制或版本”的不变量。

doctor 扫描 PATH 目录与常见安装位置（`~/.local/bin`、`/opt/homebrew/bin`、`~/.opencode/bin`、fnm/node 全局 bin、`npm root -g`），匹配已知二进制名 `opencode`、`qwen`、`qwen-code`、`qodercli`、`pi`。它不递归全盘、不猜近似名；对 OpenCode、Qwen Code 与 Pi 候选只执行精确的 `<bin> --version`，使用各 Adapter 的净化 probe 环境并受 10 秒 deadline 约束。Qoder 候选只执行精确的 `<bin> --config-dir <private-temp> --setting-sources "" --version`：`<private-temp>` 是本次 probe 新建的 `0700` 临时配置目录，环境只保留 `PATH`/`TMPDIR`/`LANG` 并把 `HOME` 与 XDG 目录重绑到隔离临时根，stdout/stderr 有严格 byte limit，超限、超时或子进程残留均按有界 process-group 清理后跳过。所有这些命令都只读取版本与计算 realpath/SHA256，不注册 Adapter、不启动 Worker Attempt，也不把发现结果作为 conformance evidence；任何执行失败静默跳过。已配置环境变量的 Adapter 不参与发现。

### doctor 解读

```bash
marshal doctor --json
```

报告新增 `discovery` 数组，每个未注册 Adapter 一项：

- `adapterId` / `environmentVariable`：对应 Adapter 与注册变量；
- `candidates`：每个候选含 `path`（发现位置）、`realpath`（符号链接解析后的绝对路径）、`sha256`、`version`、`source`（`path` 或 `known-location`）；
- `suggestedEnv`：建议写入注册变量的 realpath，供用户粘贴。

`discovery` 结果只出现在 doctor 输出，不进入 CapabilitySnapshot，也不改变 plan 的 fail-closed 语义：未注册的 Adapter 依旧不可用。

### 建议行粘贴

```bash
marshal doctor --print-env
```

该标志直接打印建议的 `export` 行，例如：

```bash
export MARSHAL_QWEN_PATH=/Users/me/.local/bin/qwen
```

将其粘贴到 shell 配置（或当前会话）即完成注册。doctor 不会写环境变量、不写 shell profile、不自动注册 Adapter。

### 验证 registered

粘贴导出后重新运行：

```bash
marshal doctor --json
```

确认对应 Adapter 的 `workers` 条目 `outcome=registered`，并按该 Adapter 的独立门禁检查 `compatibility`；此时该 Adapter 不再出现在 `discovery` 段。对已有 production-supported Adapter，`compatibility=unsupported` 表示版本或能力门禁未通过，doctor 会如实报告但不会因此放行。Qoder 的特殊门禁如下，发现或注册候选都不等于 `supported`。

Mac 普通用户模式（用户明确授权）可在不具备 root/signing/APAP 的主机上使用已固定的 Qoder/Codex：

```bash
export MARSHAL_QODER_PATH=/Users/me/.qoder/bin/qodercli/qodercli-1.1.23
export MARSHAL_QODER_MODE=ordinary-user
export MARSHAL_CODEX_PATH=/opt/homebrew/Caskroom/codex/0.145.0/codex-aarch64-apple-darwin
export MARSHAL_CODEX_MODE=ordinary-user
marshal doctor --json
```

doctor 的 `workers` 会标记 `authorityMode=ordinary-user`，并以 `compatibility=supported` 表示版本/协议路径可用；这不表示 signed authority、APAP 凭据、恶意代码 sandbox 或 credentialed conformance。未设置 mode 时仍走严格 authority 路径并保持 fail closed。普通用户模式不得与对应 `*_AUTHORITY_CONFIG` 同时设置。

Qoder 额外设计了独立 Conformance authority 门禁。只配置
`MARSHAL_QODER_PATH` 会完成注册，但 `compatibility` 必须保持
`unsupported`。**ADR 0034 仍为 Proposed，生产构造器当前硬禁用
`MARSHAL_QODER_CONFORMANCE_CONFIG`：配置该变量会得到
`outcome=invalid-configuration`，绝不会注册或产生 `supported`。** 下述格式只供
候选机制的 hermetic 负向矩阵与独立审计使用，不是启用说明。ADR 0034 为 Proposed 时
exported `qoder.NewCandidateLiveVerifier` 与 `qoder.SignLiveConformanceObservation` 都会 typed
hard-disabled；调用者不能以自带 keypair 建立 receipt/verifier trust root。候选同包机制强制使用
固定 authority policy 与无普通宿主 fallback 的隔离 sandbox；sandbox
只能写 verifier-owned scratch、显式拒绝全部业务仓库 root，并以完整替换环境验证 credential
与冻结的 `stream-json` 协议。verifier 只能使用
`qoder.EncodeLiveConformanceObservation` 输出非敏感 observation document（可执行文件 realpath/digest/version、稳定 host fingerprint、
suite/probe artifact/challenge、capability/profile/argv/env/tool policy（其中 argv
覆盖由运行时同一 `buildArgs` 模板生成的 model 省略/存在 × `--tools` 省略/显式空值四种组合）、
event/protocol/permission、transcript digest、有效期和三个 typed verdict）交给
独立签名者。verifier 只持独立 attestation key 并签名完整 observation，不持 receipt/evidence
authority key。签名者另行持有 evidence private key，并以自身固定 trust policy 重新严格解码、
验证 verifier signature、四份 receipt chain 与全部 candidate identity 后签名。私钥、credential、stderr 与 transcript 正文
不得进入 observation、Marshal 配置或仓库。

Qoder 的版本解析只接受裸 `X.Y.Z`，兼容范围固定为 `>=1.1.23 <1.2.0`；前缀、预发布版本、build metadata、低于 `1.1.23` 的版本及其他 minor/major 均 fail closed。这个 semver 范围只表示允许进入独立验证的候选范围，不是跨二进制授权：每个实际 patch 版本都必须以自身 realpath、SHA256 digest、版本和当前 host 重新完成真实 credentialed live probe，并取得精确绑定的新 evidence。当前没有可用于生产准入的真实 evidence，因此 Qoder 仍不是 production `supported`。

候选 verifier 与 sandbox 之间不是普通函数返回值信任：verifier 从验证到执行持续持有
executable/scratch/credential/business-root 的 fd/dirfd，sandbox 只能消费这些 handle；每轮返回由
独立 receipt authority 签名的 closed execution receipt，绑定实际 fd identity、argv、完整环境、
write/deny policy、challenge、transcript/session/model 与 marker。verifier 只持 public key 并逐字段复核。
`hermetic-fixture`、fake transcript、字段忽略/替换或跨轮 replay 均不能输出
`credentialed-live` observation。marker 仅经 held scratch dirfd 的 nofollow/nonblock `openat` 与
owner/mode/nlink/type/size 检查读取，不按路径重开。
每个 variant 的 pre/post/marker-post 都重新遍历 held dirfd ancestry，receipt 另行绑定执行时
topology digest；credential/business root 执行中 nest 到 scratch 再 swap-back 也会被拒绝。

签名者把返回的 JSON 以 `<digest-without-sha256-prefix>.json` 写入权限为
`0700` 的 authority 目录，文件权限为 `0600`。生产侧另建权限为 `0600`
且全部路径组件都不是符号链接的配置文件：

```json
{
  "evidenceRoot": "/absolute/private/qoder-authority",
  "evidenceDigest": "sha256:<64-hex>",
  "authorityGeneration": 1,
  "probeArtifactDigest": "sha256:<64-hex>",
  "challengeDigest": "sha256:<64-hex>",
  "revokedEvidenceDigests": [],
  "trustRoots": [
    {
      "keyId": "verifier-key-1",
      "algorithm": "ed25519",
      "publicKey": "<base64-public-key>"
    }
  ]
}
```

ADR 0034 接受后仍须由独立后续变更显式移除生产硬禁用；本候选提交不会自动启用。
未来启用版本的 `marshal doctor --json` 只有在 evidence 签名、内容摘要、Qoder identity、
兼容 semver、完整 probe contract、当前 host fingerprint、authority generation、
撤销集合和不超过 24 小时的有效期全部匹配时才报告
`supported`；缺失、过期、替换、未知字段、错误权限或环境不匹配均
fail closed。候选 consumer 的每次 Probe 与 Worker launch guard 都重新读取 authority 配置与 evidence，
并在独立于只读 authority root 的 consumer-owned 私有目录中耐久维护单调
`authorityGeneration` high-water；该 fence 跨进程、跨重启，以完整 authority config
canonical digest 绑定同代 identity，并在解析 evidence leaf 前原子消费。进程内缓存只作投影，
不能替代耐久 fence；generation rollback 或同 generation 替换 evidence/artifact 均 fail closed，
不会把一次成功结果写入仓库或 `.marshal` 作为第二权威。

CI 中生成的临时 key、fake executable 与 synthetic transcript 只验证签名和
fail-closed 机制，不是 credentialed live evidence。ADR 0034 未接受或当前
host 尚无外部真实 evidence 时，不得据此宣称 Qoder 已完成 live conformance。

本节与 Operator Runbook §9.1“一次性环境准备”交叉引用：§9.1 给出五个环境变量的固化方法，本节给出如何用 doctor 发现正确的绝对路径。

## 契约策略

JSON Schema 是持久化记录的权威结构定义。Go 侧只定义 Core 实际需要的强类型枚举与不透明 `Record`，避免维护第二套完整镜像结构；测试强制检查：

- 17 份 Schema 全部可以编译；
- 每份 Happy-path Fixture 通过；
- 每份 Invalid Fixture 失败；
- Format Assertion 已启用；
- `Kind` 与生命周期 `State` 常量和 Schema 对齐；
- Task Budget、ID 唯一性、路径、Verification Status、Review Finding 与 `.marshal/` Source Artifact 等 Semantic Rule 生效。

Schema 使用 ECMA-262 风格正则。Go 标准库正则不支持其中的全部转义，因此 Validator 显式使用 ECMAScript Regex Engine；不得退回默认 Go Regex 后仍宣称契约兼容。
