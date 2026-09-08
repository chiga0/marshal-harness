# B2 首轮双节点实机业务审查

日期：2026-09-07。范围：合成订单报价 API/客户端，固定 server、真实 Pi、ordinary-user；不是正式生产或最终集成交付审查。

## 结论

**运行链有实质进展，业务交付未通过。** 两个实现节点由同一 server 批准后启动，各一次 Attempt，完成 Collect、Verify、资源释放与 REVIEW_PENDING。已有 oracle 都通过，但独立审查和真实 loopback 反例发现客户端一项 P1、一项 P2；不能给予该客户端 ACCEPTED。服务候选在本轮冻结范围内未发现 P0/P1，仍未提交正式 Decision。没有执行 integration、Goal Outcome、暂停/replan 或重启后的团队恢复；没有对照收益证据。

本审查使用轻量代码审查检查异常与契约边界，并以实际候选做反例复现；不使用 Marshal skill，不修改 Run 或 authority 文件。

## 精确证据

- sourceHead：`7a4f7d0f42749ab5d2fdce5048b9aa6f83b0169b`；[CI 34081513199](https://github.com/chiga0/marshal-harness/actions/runs/34081513199) 五项成功，macOS quality 17 分 21 秒、Linux quality 8 分 57 秒。
- [实机 34082574786](https://github.com/chiga0/marshal-harness/actions/runs/34082574786) 成功，作业 6 分 26 秒；实际 canary 步骤 5 分 39 秒。固定 binary SHA-256：`900045654e40b01bf6c7c0398163cfb0aae8d9cbdaddbb72116b602d6db5e19e`。
- 完整 artifact：`10004269688 / fixed-server-t1-34082574786`；诊断 artifact：`10004269175 / fixed-server-diagnostics-34082574786`。两个 review-inputs.tar 均位于 artifact 的 `.marshal/fixed-server-t1-canary/fixed-server-t1-34082574786/team/`，只作为审查载体，不是 authority import。
- 输入摘要：`sha256:ccb0b4184b1ab1d8ed09cd4291dd8d522efba2b7eda76efcdb4682ae3978b714`。
- 66 条 RB1 record 的 canonical digest 和 sequence 逐条验证；包含一个批准、两个创建冻结、各两个 reservation/open/process-started/result-admitted/cleanup-released，没有 team halt。摘要自洽检查不代替独立内核观察或跨主机恢复。
- 服务 Run：`team-run-49d0f9cc853799cd3ffba9f389c7696fa6c0a81e7abee62405a99fba7f2f32b8`，Attempt：`attempt:f7fdcaac4f615882179ea8620a216f57`；observed.patch SHA-256：`56be262a5d3d4799f7f7580e2e4b5a8dcb88be2c288b5e446081c7a0f4fcfb1d`。
- 客户端 Run：`team-run-00f385aac52d0be7562dae50bec092c97ac8e0cb7f12772423aafc2dca4d5bef`，Attempt：`attempt:b4aa419cc76d3f316b0921529481a2e1`；observed.patch SHA-256：`c5161a61cf460ee31e2f0575326f6035399daed61f92bf46202200dfa281378b`。
- 两个 capture-manifest 各八份文件的类型、大小、唯一名字和 SHA-256 全部复查；packet 与 fixed CLI 响应相等，Task/VerificationReport/ArtifactManifest 的 canonical digest 及 observed.patch 的原始摘要与 packet 绑定相符。服务 packet digest：`sha256:06e5bf4f3a858e353fc6016732178a10b11248e2ac8909608eadcc0a1198f0df`；客户端：`sha256:3b5babcde13e7cc135ea20bc8f6645208ae2b15c8f5f566f6f3b1e491fc281ad`。这仍是已捕获证据核对，不是新 Core Decision。
- 两份 VerificationReport 的 status 都是 pass；服务验证约 3.30 秒、客户端约 2.66 秒。外部逐节点 Start 调用为零；summary 明确 `accepted:false`、`integrationExecuted:false`、`processOverlapProven:false`。

## 问题与修正

| 严重度 | 精确候选位置 | 复现与影响 | 修正 |
| --- | --- | --- | --- |
| P1 | 客户端 observed.patch 中 `quote_client.py:68` | 对 200 JSON 直接返回 `json.loads(data)`，真实 HTTP fixture 返回 `[]`、`{}`、布尔金额对象时均被作为报价返回；违反已冻结的三整数分字段与协议失败契约 | 验证对象的精确键集合及严格 int 类型，拒绝 bool/float；错误抛 ValueError，但不重新计算服务价格 |
| P2 | 同候选 `quote_client.py:29` | `parts.username`/password 的真值判断放过空 userinfo；`http://@127.0.0.1:PORT` 实际发送请求并成功返回，违反无 userinfo 要求 | 用是否为 None 判断，并在任何联网前拒绝 |

诊断使用已读取审查的 patch 在内存加载模块，由独立 loopback HTTP fixture 提供响应，共四次请求复现上述返回；未启动 Agent、未访问外部网络、未写 Run。随后新的 oracle 对服务候选通过 33 项观察，对旧客户端拒绝。该复查没有覆盖或替换原 VerificationReport，也未签发 ReviewDecision。

已前移到参考验收的同批修正：六种非法 200 响应、两种空 userinfo、以及“请求后才拒绝”的反例；保留响应 challenge，避免用本地重算掩盖协议问题。正确实现、旧式无结构校验客户端、逐项漏检变体和 URL 迟拒绝变体均有回归。客户端提示显式解释原协议结构与联网前拒绝要求，避免让模型从多处文字推断。新增反例不重新定义本次冻结输入；后继输入/验收摘要必须重新批准。

## 效率复盘与下一步

不能把两个 Run 的一次 Attempt、零 operational retry/rework 等同于零返工交付：此前存在 CLI 接线失败、晚期 launch 拒绝、需求 context 丢失、两次来源夹具错误及 CI/派发操作等待；本次仍有独立审查发现的问题。

两份规范化 WorkerResult 报告的 usage 分别为：服务 inputTokens=27518、outputTokens=30476、cachedInputTokens=570496；客户端 inputTokens=84455、outputTokens=28753、cachedInputTokens=179520。它们是当前 adapter 报告值，不是独立计费账单；缓存口径/去重尚未审计，不直接相加成总消耗或比较成本优势。对 114/70 行候选而言，用量值得专项检查，但尚无同条件 Lead＋SubAgents 配对数据，不能断言浪费来源或宣称加速。

下一步仍是业务闭环：保留服务精确候选；将客户端两项问题合成一次修正，不逐项重试、不原样重跑两个实现节点。当前模板 rework=0、replan 尚未接通，不能偷偷加预算或修改旧 Task。接通正式 Decision、server 自动收集/验证、已接纳上游集成与局部 replan/reuse，后续必须通过原 owner/current-ledger 和 Run 生命周期入口；不要以更多样例通过替代这些缺口。B1/B2 继续 IN_PROGRESS，B3 PLANNED。

补充：本次 runner 已结束，不能将 review-only 归档复制到新 runner 后给原 Run 补签权威 Decision；保留候选不等于已经获得可自动复用的 ACCEPTED。后继已实现 opt-in 同宿主双节点评审载体候选，支持分别记录接受/拒绝并保留另一成果，尚待实机。不立即派新的完整团队；局部 replan/reuse 必须先设计明确预算延续、成果来源和接纳入口，不能用换 Goal 或伪造旧 receipt 实现“复用”。
