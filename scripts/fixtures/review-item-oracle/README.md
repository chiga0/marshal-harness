# 后继逐项验收 oracle

这是针对 42ce 冻结诊断输入的离线测试工具，不修改生产合同、原 suite、原报告或模型。只接受四个已核对输入摘要，并重新校验完整输入与报告来源绑定；期望按政策身份及精确过程条款定位，不依赖行号或报告关键词。

原 suite 的 `expected: ["accept"]` 只标记候选内容修正，未考虑过程证据缺失，不能作为整体正确性标准。本工具保留该标签供追溯，不用它计分。

- S02 正例：facts 与 effects 应通过；未运行/部署没有可信证据，过程条款应 unknown，scope 不能全通过，整体不能 accept。
- S02 负例：facts 应 fail，过程缺口仍保留。
- C01 负例：恢复条款应 fail，不能自行补出清单已有记录后的续建分支。
- C01 正例：恢复文本分项可通过；effects 适用于拟议命令且需要反例；未证明实际并行、依赖执行和后续验证，scope 不得代表完整过程通过。

`ITEM_EXPECTATIONS_MET` 仅表示分项标签、必需反例存在及整体拒收符合预设。它不判断反例推理是否成立，不证明完整行为、真实恢复或 Core 权威；所有结果继续标记 `semanticReview: REQUIRED`。无合法报告为 `NOT_EVALUABLE`，不能从原始文本补字段制造报告。

调用（不启动模型或服务，不写输入文件）：

```sh
node scripts/review-item-oracle.mjs S02-positive /绝对路径/result.json /绝对路径/S02-positive-input.json
node --test scripts/review-item-oracle.test.mjs
```

输入是组件原冻结输入文件（可含外层 input/provenance）。新候选输入或可信过程来源变化时须新版本独立审查，不跳过摘要检查沿用旧 oracle。测试中最小报告仅验证比较函数；生产入口仍必须经过原完整来源合同校验。

## 首次零模型消费结果

基线 `46de2a65` 上，6 项最小报告测试通过；只读消费既有长窗口诊断（没有新模型调用）：S02-negative 捕获过程项误判；S02-positive 保留 `invalid_assessment_receipt` 为无法分项验收；C01-negative 捕获 recovery、scope 与 aggregate 错判；C01-positive 捕获 effects 错误豁免、缺反例、scope 与 aggregate 错判。原 180 秒四次失败及长窗口结果均未修改。这里的工具命中缺陷不把原业务执行算作通过，源码作者测试也不替代独立审查。
