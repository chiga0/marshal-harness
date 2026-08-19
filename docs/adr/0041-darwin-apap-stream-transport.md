# ADR 0041：Darwin APAP 长度帧传输（提案）

- 状态：提议（Proposed，2026-08-19；未经维护者接受，不启用生产准入）
- 关联：[ADR 0038](0038-agent-production-authority-provider.md)、[ADR 0040](0040-darwin-codex-authenticated-launcher.md)、Issue #136、Issue #137

## 上下文

当前 macOS 宿主对 `AF_UNIX/SOCK_SEQPACKET` 返回 `protocol not supported`。因此即使 APAP socket 已由 root-owned launchd 部署，原有 client 也无法建立连接。Linux 继续使用 ADR 0038 规定的 `SOCK_SEQPACKET`。

## 决策（提案）

Darwin APAP control transport 使用 `AF_UNIX/SOCK_STREAM`，但保留同一 authority contract：

1. 每个消息是四字节大端长度加 payload 的单帧；长度必须为 `1..64KiB`，超限、零长度、EOF 或额外字节全部失败关闭。
2. `SCM_RIGHTS` 只随该帧发送；接收方必须累计整个帧期间的 control message，并执行原有 0..32 FD、顺序、role 与 held identity 校验。
3. transport 不解释签名、credential 或 authority；peer authentication、root-owned endpoint、launcher barrier、credential ingress 与独立 verifier 仍由 ADR 0038/0039/0040 负责。
4. 不允许把普通 stream、pathname exec、同 UID helper 或自签 fixture 作为生产等价物；Darwin registry 继续 `unsupported`，直到完整 authority bundle、签名 launcher、负向矩阵与 credentialed live probe 通过。

## 负向测试与退出条件

实现必须覆盖截断帧、长度伪造、FD control message 丢失/重复/超量、帧间串接、路径替换、peer 错误与服务缺失。当前实现仅关闭 transport 可达性缺口，不关闭任何 production authority finding；维护者接受本 ADR、独立 provider/verifier、root-owned launchd provisioning 与真实 conformance 仍是后续门禁。
