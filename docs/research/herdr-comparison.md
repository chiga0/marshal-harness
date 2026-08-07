# herdr 与 Marshal 对照调研：两个正交层的互补分析

- 调研日期：2026-08-08
- 对照对象：[herdr](https://github.com/herdrdev/herdr)（本机克隆，v0.8.0，79 个 release tag）与 Marshal Harness（main）
- 方法：只读分析 herdr 的 README/AGENTS.md/文档站源码/src 结构/release 历史，与 Marshal 的架构文档、ADR、实现逐项对照

## 0. TL;DR

**herdr 与 Marshal 不是竞品，是同一问题栈的两个正交层。**

- herdr 回答："Agent 在哪里活、现在在干嘛、卡住了没、掉线了怎么回来"——**终端与会话层**（runtime your coding agents live on）；
- Marshal 回答："Agent 的工作凭什么算数、谁批准、怎么安全落地"——**证据与治理层**（evidence-gated orchestration）。

herdr 的 `blocked/working/idle` 是**注意力调度**信号（该去看哪个 pane）；Marshal 的 `REVIEW_PENDING/BLOCKED/ACCEPTED` 是**证据裁决**状态（能否改变仓库）。前者不做发布治理，后者不做终端持久化——**把 herdr 当 Marshal 的 TerminalSession 后端、把 Marshal 当 herdr 之上的治理层，是两者最自然的组合**。

## 1. 定位与心智模型

| 维度 | herdr | Marshal |
| --- | --- | --- |
| 一句话 | 编码 Agent 的常驻终端运行时 | 编码 Agent 的证据门禁式控制平面 |
| 隐喻 | 身体与神经：Agent 住在里面，状态可见、可回魂 | 流程与审计：什么被允许、什么被证明 |
| 与 Agent 的关系 | 拥有 Agent 的**终端**，不包装不替换（claude/codex/cursor/opencode/grok 皆可） | 通过 **Adapter** 规范化 Agent 的权限/协议/产物，Worker 可替换 |
| 核心承诺 | always running、卡住可见、ssh 可回附 | 冻结契约、独立验证、摘要绑定审查、draft-only 发布、永不 merge |

## 2. 实现对照

| 维度 | herdr | Marshal |
| --- | --- | --- |
| 语言/形态 | Rust 单二进制；**常驻后台 server** + TUI 客户端 | Go 单二进制；**CLI 一次性调用**（无守护进程，ADR 0001） |
| 状态模型 | pane 级 5 态（working/blocked/done/idle/unknown），屏幕 manifest + 集成上报 | Run 级 16 态生命周期 + 转换守卫；事实优先级五层（进程/Git/FS > 冻结输入 > Verification > Review > Worker 自述） |
| 持久化/恢复 | server 状态持久化，重启/断网/合盖后会话回魂；命名 session；ssh reattach | append-only Journal + 原子 Snapshot + Lease；崩溃后重放恢复；发布幂等 |
| Agent 接口 | socket API + CLI 同面（`herdr api schema` 自描述 JSON Schema）；agent 间可互 prompt、可 wait-until-blocked | CLI + Skill 驱动 Lead；Worker 经 Adapter 的 argv/权限/预算规范化；密封 LaunchEnvelope（ADR 0011） |
| 信任模型 | 人在环里实时观察；无证据门禁、无发布治理 | Worker 不得自证；凭据分权；fail-closed；审计留痕 |
| 扩展 | 插件市场（marketplace）、集成安装 | Port/Adapter 一致性测试；第三方默认不进进程 |
| UX | 键盘+鼠标双一等、分屏、插件 | CLI + 文档 + Skill；受监督 PTY（cmux 后端） |
| OSS 成熟度 | 79 tags、brew/mise/install.sh、版本化文档站（en/ja/…）、sponsors、X 运营 | 0.1.0 待发布；Pages 初版（中文）；社区文件刚齐 |

## 3. 各自优势（诚实版）

### herdr 强于

1. **安装与分发**：`curl install.sh` / brew / mise / 二进制 release，79 个 tag 的发布纪律；
2. **会话持久化与远程**：合盖/断网/重启不丢 Agent，ssh 回附——Marshal 的 captured/PTY 模式都没有这层；
3. **注意力产品化**：blocked/working/idle 一眼可见，"never hunt for the stuck one" 直接命中多 Agent 监督痛点；
4. **agent-native 自描述 API**：`api schema` 导出完整 JSON Schema，Agent 零先验即可驱动；
5. **社区与治理文档**：AGENTS.md 分层（universal / maintainer-only / external-contributor guardrail），维护者身份需可验证（MAINTAINERS + remote + write 权限三条件）；
6. **多语言文档站**与版本化文档（docs/next、preview、versions）。

### Marshal 强于

1. **证据门禁**：冻结 TaskSpec、独立 Verification、digest 绑定 Review、CI 绑定 accept——herdr 完全没有这层，Agent 在 herdr 里"说完成了"就是完成了；
2. **信任边界物理化**：单写者 worktree、control root 分离、Worker/Publisher 凭据隔离、权限归一化；herdr 的 pane 里 Agent 持有什么凭据由环境决定；
3. **失败语义**：fail-closed、Outcome 证据、崩溃恢复与发布幂等均有实测；herdr 的恢复是会话级，不是任务证据级；
4. **可审计性**：append-only Journal + 审批/介入记录绑定摘要；herdr 是实时观察，事后审计弱；
5. **发布治理**：draft-only、幂等 PR、永不 merge；herdr 不管发布。

## 4. 互补与集成点（核心结论）

1. **herdr 作为 Marshal 的 TerminalSession 后端**（ADR 0008/0009 可插拔边界的自然延伸）：
   - herdr socket API 的 `pane run / read / send input / wait on state / subscribe events` 与 Marshal cmux 后端所需原语同构，且额外提供 ssh 远程与重启回魂；
   - 密封 LaunchEnvelope 可在 herdr pane 内执行；herdr 的 `blocked/working` 作为 CompletionGate 的**辅助**信号（按 ADR 0011，仍非权威证据）；
   - 收益：受监督模式从"本机 cmux"扩展到"任意终端 + 远程"。
2. **wait-until-blocked 解决 Marshal 的轮询空转**：Lead Agent 住在 herdr 里，用事件订阅替代 sleep 轮询——我们实测的 30–50 分钟衔接空转直接归零。
3. **权威不争夺**：herdr 不做发布/裁决，Marshal 不做终端/持久化——集成时 Marshal 仍是状态与策略唯一权威，herdr 是呈现与传输层，与 cmux 同地位。
4. **反向互补（可供 herdr 借鉴）**：证据门禁、冻结契约、凭据分权、draft-only 发布——herdr 用户若要把 Agent 产出安全落地仓库，正缺 Marshal 这层。

## 5. 差距清单与行动（向 herdr 学）

| # | 差距 | 行动 | 优先级 |
| --- | --- | --- | --- |
| 1 | 发布纪律（79 tags vs 0） | 合入当前实现批后打 `v0.1.0`，CHANGELOG 同步；此后每个 milestone 收口打 tag | 高 |
| 2 | 安装体验 | 提供 `install.sh` 或 brew formula（Go 单二进制友好） | 中 |
| 3 | API 自描述 | `marshal contract schema --all` 导出全部 Schema（部分已有 validate；补导出） | 中 |
| 4 | 分层治理 AGENTS.md | 借鉴 maintainer 可验证 + external-contributor guardrail 两层 | 中 |
| 5 | 多语言文档 | 中文优先不变，README 已有英文定位句；Pages 增加英文导航为下一步 | 低 |
| 6 | 会话持久化 | 不追（定位不同）；经集成点 1 由 herdr 提供 | — |

## 6. 结论

"实现和定位一样"是表层印象（都管多个 coding agent）；深挖后是**会话层 vs 治理层**的分工。 Marshal 的下一步不是复刻 herdr 的终端能力，而是：

1. 守住治理层独特性（证据门禁、信任边界）；
2. 把 herdr 纳入 TerminalSession 后端候选（设计提案，扩展 ADR 0008/0009 的后端清单）；
3. 在发布纪律、安装体验、自描述 API、分层治理四项上对齐优秀开源基线。
