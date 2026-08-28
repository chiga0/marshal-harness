# ADR 0061：Supervisor Close 的 transcript disposition 与无结果证明

- 状态：已接受（Accepted，2026-08-29）。候选 `742dbf0cc8c55971105710b5142f4c803e97e0f7` 经同一独立 reviewer 聚合复审确认 P0/P1/P2 均为 0；接受只冻结 `Close` 的三态封闭语义，尚未实现 wire/persisted projection，也不升级 I186-R2–R6。
- 关联：[ADR 0044](0044-result-ingress-and-cold-hot-paths.md)、[ADR 0056](0056-darwin-process-observation-and-attempt-terminalization.md)、[ADR 0059](0059-fixed-darwin-process-supervisor.md)、[ADR 0060](0060-supervisor-mechanics-authority-binding-and-recovery.md)、[Issue #186](https://github.com/chiga0/marshal-harness/issues/186)。

## 背景

ADR 0056 规定 ResultIngress admission conclusion 与 terminalization barrier 共用同一 authority CAS：WorkerResult 先被接纳时 barrier 绑定该结果；cancel、timeout 或失败先赢时，随后结果只能 quarantine。ADR 0059 的 Supervisor mechanics 当前又要求 `collect` 成功后才接受 `close`。

这两个规则在无 WorkerResult 的 cancel/timeout 路径上形成不可达序列：barrier 已提交后不能再 `collect`，但不 `collect` 又不能 `close`，Attempt 永久无法进入 `process-supervisor-closed → cleanup-completed → lease-released`。直接放宽为“未 collect 也可 close”会让正常成功路径静默跳过 transcript，破坏 ADR 0044/0060 的 ResultIngress 与 exact mechanics binding。

## 决策

### 1. 封闭的 transcript disposition

`ClosePayload`、prepared-command secret-safe projection、mechanics journal intent/receipt 与 ResultIngress command intent/outcome 增加同一个封闭枚举与一个统一的 `transcriptResolutionFactDigest`：

```text
transcriptDisposition = collected-admitted | collected-not-admitted | not-required
```

三个分支是互斥 union，且 `transcriptResolutionFactDigest` 在所有分支均必须非空并引用对应 exact RB1 business fact：

1. `collected-admitted`：Supervisor 必须已提交同 session、同 child、同 command chain 的 successful `collect`；resolution fact 必须是 exact `result-admitted` business fact，并引用该 collect outcome、transcript digest/size/truncation。
2. `collected-not-admitted`：Supervisor 同样必须已有 successful `collect`，但 empty-result terminalization barrier 随后赢得 authority CAS，ResultIngress 对该结果的唯一结论是 late/quarantined。resolution fact 必须是 creation-once `process-supervisor-transcript-not-admitted`，绑定 exact collect outcome、empty-result barrier与 exact quarantine/non-admission conclusion；它不把 transcript冒充 business admission。
3. `not-required`：Supervisor mechanics 必须尚未执行 `collect`；resolution fact 必须是 creation-once `process-supervisor-transcript-not-required`，证明 empty-result barrier已赢且该 session无 successful collect。本次 `close` 只封闭终态 child report与 held mechanics，不产生或冒充 collect/transcript admission。

未知枚举、错误 fact type、`collected-*` 但 mechanics 未 collect、`not-required` 但 mechanics 已 collect均 fail closed。不能用缺省值猜测旧请求的分支。

### 2. `TranscriptAbsenceProof` 的唯一签发条件

两类 non-admission resolution fact只能由持有 current repository owner lock 的 Core，通过 closed typed API在同一 RB1 current authority transaction 中 creation-once append。业务 fact推进 Attempt revision/head；Supervisor 必须先用 ADR 0060 的 authenticated reconnect/reanchor绑定新 head，随后才能 append `close` command intent。`transcriptResolutionFactDigest` 是 RB1 fact envelope digest；它由 ledger对不包含自身 digest字段的 canonical fact payload计算，绝不进入自身 preimage，也不能由调用者提交自由字符串。事实至少绑定：

- closed schema version、authority namespace、Task/Run/Attempt identity；
- current owner acquisition/epoch、Attempt revision/head；
- session ID、`process-supervisor-started` 与 `process-started` fact digest；
- terminalization ID/generation、barrier fact digest与 eligibility terminal kind/reason；
- barrier 当时与当前均为空的 committed WorkerResult fact digest；
- exact process terminal fact、allocation terminated fact、cleanup binding digest；
- 最后已接纳的 Supervisor observation/command outcome digest；
- fresh bootstrap/protocol revision、`process-supervisor-started`、当前 Supervisor 尚未 closed；
- cleanup 尚未 completed/released，且不存在 pending command、pending allocation effect或 intervention。

只有 barrier 先赢且没有已接纳 WorkerResult 时才可签发两个 non-admission fact。已有 successful collect时只能签发 `process-supervisor-transcript-not-admitted`，并要求 exact quarantine/non-admission conclusion；无 successful collect时只能签发 `process-supervisor-transcript-not-required`。`EligibilityTerminalCompleted`、barrier 已绑定 business result、分支与 collect 状态不符、历史无 bootstrap session、陈旧 owner/head、错误 session/child、空 terminal/allocation fact、已 closed/completed/released、pending/intervention均拒绝，且 RB1 ledger、Supervisor journal、owner objects与 mechanics state逐字节不变。Provider、Agent、Supervisor 与 CLI 参数不能自报 resolution fact。

proof 是“本 Attempt 不再需要 transcript 进入业务 admission”的 authority 事实，不声称 stdout/stderr 不存在，也不把输出丢失解释为成功。owner-only 输出对象可按既有诊断/retention 合同保留；不得进入普通 log、error、event 或 proof payload。

### 3. mechanics、ResultIngress 与恢复绑定

1. Supervisor 只验证 disposition union、同 session command continuity、`collected-*` 的本地 collected前置或 `not-required` 的未 collect前置，以及 non-empty resolution fact binding；它不自行决定业务 terminal kind或 admission结论。
2. ResultIngress 在 append non-admission resolution fact前重新验证 current owner/head、fresh bootstrap、barrier、空 business result、collect/quarantine状态、terminal/allocation/cleanup predecessor与零 pending/intervention；完成 authenticated reanchor 后，再把 exact resolution fact digest写入同一 command recovery子链的 close intent。调用者不能只传一个裸 digest绕过重建与验证。
3. close receipt与 `process-supervisor-closed` 必须回显 exact disposition/resolution fact digest。lost-`Close` offline recovery只有在 strict journal 的最终 intent/receipt、RB1 pending intent、disposition/resolution fact与 typed Supervisor absence全部一致时才能收敛。
4. 历史 `ClosePayload` 没有 disposition 时只允许历史 replay；不得作为新生产 command。协议实现若需要 wire revision升级，旧 live session必须保持原 revision并 fail closed，不得原地改写 journal。

## 必须通过的负面与崩溃矩阵

- completed/accepted-result 路径伪造任一 non-admission分支；未 collect伪造 `collected-*`；已 collect伪造 `not-required`；
- collect outcome已耐久但 admission前 barrier赢，必须唯一收敛为 `collected-not-admitted`；该窗口的每个 crash点与重放不得死锁或冒充 admission；
- barrier 后迟到结果、collect receipt与两个 non-admission resolution fact竞态，只允许 current authority CAS与 exact collect状态决定唯一分支；
- wrong owner/head/session/child/barrier/terminal/allocation/cleanup/last-observation/proof digest；
- same command ID different disposition/resolution fact、unknown enum、错误或缺失 union字段、旧 projection升级冒充；
- crash 于 absence proof 前后、close intent前后、mechanics close/receipt前后、response loss与 offline recovery；
- 任一拒绝均要求 authority ledger、journal、owner objects 与 mechanics state不变；
- raw stdout/stderr/transcript bytes、argv、environment value、nonce在 ledger/journal/event/error/log 中为零。

## 后果

无 WorkerResult 的 cancel/timeout/失败路径，以及 collect已完成但 admission输给 barrier的崩溃窗口，都可以诚实封闭 Supervisor；正常成功路径仍被强制经过 collect与 ResultIngress admission。代价是 `ClosePayload` 与恢复 projection增加三态持久化 union；实现必须先完成 hostile/replay测试，不能用默认值兼容新生产请求。
