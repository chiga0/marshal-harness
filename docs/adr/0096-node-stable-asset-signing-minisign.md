# ADR 0096：Node stable 发行资产的 minisign 签名口径

- 状态：Accepted（2026-09-10；依据维护者本轮对 Node Agent-Team stable 资产签名方案的明确决议（minisign），不是实现审查完成或运行许可回执）。
- 适用：首个受保护 stable 发行（`v1.0.0`）及其后继同资产类别的签名材料；不改变运行合同、持久化格式、生命周期语义或既有 release gate。
- 基线：`fc2cdc9298c3e1aaf47615373bb24e6d80e4c719`。实际成熟度见 [Roadmap](../roadmap-status.md#业务交付当前表)。

## 1. 决策

自 `v1.0.0` 起，Node Agent-Team 的受保护 stable 发行资产必须由维护者 minisign 签名；无签名资产只可为评审候选，不得作为 stable 发布。

| 面 | 决定 |
| --- | --- |
| 算法/工具 | minisign（Ed25519 分离签名，RFC 8032 `Edwards-Curve Digital Signature Algorithm (EdDSA)`）；签名对象以字节内容为准 |
| 签名对象 | 签名清单文件（`SHA256SUMS`，行内同时包含候选 sourceHead、annotated tag、独立 Decision 摘要、包 `manifestDigest` 与运输 ZIP SHA-256）；清单自封闭三级绑定，不依赖带外传递 |
| 身份三级互证 | sourceHead（annotated tag → commit）、包 `manifestDigest`、运输 ZIP SHA-256；任一漂移即拒绝，不以 tag 或仓库最新分支为最终身份 |
| 私钥 | 维护者个人密钥，仅存于其持有的受控位置；不进入仓库、CI、共享运行平台或任何发行资产 |
| 公钥 | 以只读文本列入仓库发行文档，并在 release notes 随附原文；公钥轮换/撤销另立新 ADR |
| 验签公式 | `minisign -V -P <pubkey原文> -m <SHA256SUMS形式清单>` 且第二身份以 `sha256sum -c`（GNU coreutils；macOS 维护环境用 `shasum -a 256 -c` 等价）行级比对；默认值/别名或单文件签名不被接受 |
| 签名时点 | 仅在该候选的完整证据（CI 五 job、同字节跨环境、声明范围真实验收）齐备、独立 Decision 完成之后执行；签名不对未验收资产背书 |

## 2. 范围、精确取代与不变量

- 仅覆盖签名口径：不新增托管签名平台、远程身份平台、密钥托管服务或第二发布通道；不修改 HTTP 请求/响应、枚举、持久化数据或发行 check 本体的判断结果。
- minisign 发行资产签名与 macOS 代码签名（Developer ID/Mach-O）/ notarization 是并存且互不替代的身份域；本稿不声称任一域可直接替代另一域的历史门禁（ADR 0052/0068 的相关范围不变：Node 资产不是 Mach-O）。
- `SHA256SUMS` 的格式约定：身份信息（sourceHead、annotated tag、独立 Decision 摘要）以 `#` 起始的行级注释写明，`sha256sum -c`/`shasum -a 256 -c` 按惯例忽略；其余行保持国际标准摘要格式 `<digest>␣␣<文件名>`，保证验签公式可直接执行。
- 旧 `v1.0.0-rc1`（unsigned、CLI-only、Darwin local-dogfood prerelease）的记录与边界保持不变，不重签名、不回标；本稿不为其扩展任何授权。
- 发行 check 继续以 CI required gate、独立 Decision、current receipt/carrier、安装升级负测为条件；签名指标是放行要件之一，不替代任一门禁。
- 私钥泄露的处置仅按：`撤销公钥 → 通知书立即作废该公钥下全部签名 → 另立新 ADR 公钥轮换`，不修订历史 release 资产字节。撤销通知书固定落点在仓库发行文档与交流渠道，并按日期与操作者记录在公钥权威清单的对应行同步标注作废标记，供验证者按公钥追认。
- 维护者签名是对"指定字节的这次验收证据链"的担保，不是对模型输出、第三方运行或未来行为的担保；验证侧仍须执行各已有验收。

## 3. 兼容性

非协议破坏性变更。不迁移数据，不要求现有安装升级，不影响旧 profile 正在执行的运行；无签名或未经验签的资产在被支持流程中的身份只能是候选/非生产。新增 stable 发行检查项见 [发布就绪文档](../v1-release-readiness.md)；同资产实际完成状态见 [Roadmap 当前表](../roadmap-status.md#业务交付当前表)。
