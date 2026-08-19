# Mac-first Qoder/Codex Authority 交接清单

本文档是宿主管理员交接材料，不是生产准入开关。它不包含私钥、credential、capability 或可直接复制的签名结果；在所有外部证据齐备前，`doctor` 与 registry 必须继续报告 `unsupported`。

## 当前仓库已经完成的部分

- Qoder 固定候选：`/Users/gawain/.qoder/bin/qodercli/qodercli-1.1.23`。
- Codex 固定候选：`/opt/homebrew/Caskroom/codex/0.145.0/codex-aarch64-apple-darwin`。
- Darwin APAP 使用长度帧 `SOCK_STREAM`，保留 SCM_RIGHTS 和封闭 envelope 校验。
- launch transaction 具备 provider-owned append-only journal、CRC/SHA 链和重启 hydration。
- `doctor` 只投影部署状态，不会因为普通配置文件或版本探针而启用 registry。

## 必须由宿主管理员完成的外部步骤

以下每一项都需要独立于 Marshal/Worker 的 OS principal 或签名 authority；不能用同 UID helper、ad-hoc `codesign`、普通用户 LaunchAgent、pathname exec 或测试 fixture 替代。

1. 为 APAP service、signed launcher、client/server helper 和独立 verifier 分配不同的受管 OS principal。
2. 使用受管 Apple signing identity 构建并签名 launcher/helper/service；记录 Team ID、CDHash、identifier、架构和内容 digest。
3. 由 root 安装并锁定 `/Library/PrivilegedHelperTools/*`、`/Library/LaunchDaemons/com.marshal.apap.plist` 与 root-owned APAP endpoint；所有路径组件拒绝 symlink、group/other write 和 pathname 替换。
4. 配置独立 credential provider 的 session-scoped `CredentialIngressPort`。APAP control socket、Marshal、verifier 和 Worker 不得收到 credential bytes、endpoint handle 或 capability fd。
5. 配置不可由 Marshal/Worker 回滚的 host/fence/receipt authority，并完成 key rotation、revocation、crash/reconcile 和 stopped-child barrier 证据。
6. 以真实 Qoder 1.1.23 和 Codex 0.145.0 分别运行 profile-specific credentialed live probe、kill/wait、lost-response、replay、read-only、secret-scan 和 conformance；两套证据不能互换。

## 管理员交接后的只读核验

交接前可先运行仓库内的只读预检；它只报告缺口，任何失败都会保持 fail-closed，不会安装、签名、bootstrap 或修改 `.marshal/`：

```sh
scripts/macos-authority-preflight.sh
```

以下命令只读，不会安装、签名、bootstrap 或修改 Marshal 状态：

```sh
security find-identity -v -p codesigning
codesign --verify --strict --verbose=2 /Library/PrivilegedHelperTools/marshal-apap
codesign --verify --strict --verbose=2 /Library/PrivilegedHelperTools/marshal-darwin-launcher
launchctl print system/com.marshal.apap

MARSHAL_QODER_PATH=/Users/gawain/.qoder/bin/qodercli/qodercli-1.1.23 \
MARSHAL_CODEX_PATH=/opt/homebrew/Caskroom/codex/0.145.0/codex-aarch64-apple-darwin \
go run ./cmd/marshal doctor --json
```

只有当独立 verifier 能证明 service/launcher/endpoint/peer/credential ingress/anchor/receipt/barrier 全部满足 ADR 0038–0041，且 Qoder 与 Codex 各自的 conformance 证据绑定到精确 executable identity 后，才允许维护者单独启用对应 registry。任一项缺失都必须保持 `platform-unsupported` 或 `profile-unsupported`，不得把“二进制存在”解释为生产可用。

## 当前本机结论

本机目前没有有效 codesigning identity、root-owned APAP launchd deployment 或 credential authority，因此这不是仓库代码可自行闭合的缺口。该事实已记录在 `docs/audit-report.md`；补齐外部交接后，可直接按上面的只读核验继续，不需要重做 APAP framing、journal 或 adapter identity 逻辑。
