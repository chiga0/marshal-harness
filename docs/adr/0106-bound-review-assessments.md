# ADR 0106：批准前冻结验收条目与绑定原评审的逐项证据

- 状态：Accepted（root设计接受，三方独立设计审查完成；实施与业务验收尚未通过）
- 日期：2026-09-15
- 基线：`8086b8a1972193e983cd26959322e28e2e0dc2fe`
- 关联：ADR0094、ADR0101、ADR0102、ADR0103、ADR0105

## 问题与目标

真实S02已收到固定作者指导，但批准验收未清楚展示创作与既定事实的边界；Review将未确认日程和到场方式直接接受。真实C01经过有效标签修正，Review仍接受缺少两步导入失败恢复规则的完整方案。原四字段报告能表达总体意见，却不能证明已逐项覆盖要求、引用真实来源或保留未知。

本设计约束验收过程的完整性与证据真实性，不宣称通过结构校验便能证明模型推论正确。继续使用原Review、Verification、repair、预算、清理与SQLite事务，不增加第二状态机、任意代码执行或额外模型调用。

## 批准前的验收约定

新配置显式启用`task-review-criteria/v1`。受信通用文件Leader映射器仅在生成计划提案时，将固定政策条目和条目目录加入原`plan.acceptance`；保留模型原业务验收文字及顺序，不删除、改写或截断。新增内容先进入批准页面和计划摘要，之后才允许执行；不对已批准计划补标准。

固定政策覆盖：原要求及批准范围；事实来源与创作标示；操作效果与实现边界；数据变化及失败恢复。用户要求的创作可以生成建议或拟议日程，不将所有新文案视为虚假事实；未知参与方式不能冒充已确认渠道。没有相关操作或数据变化时可以给出有依据的不适用说明。

目录采用闭合记录`{profile,items}`；每个条目包含受信分配的稳定ID与原acceptance索引。条目文字仍在可读验收列表中，不以ID替代内容。ID由索引与精确条目内容产生；保留原32项计划限额，目录和政策占用原额度，超限明确拒绝。原业务项与固定政策必须恰好覆盖，不接纳模型自行提交、重复或伪造的目录。

新配置的政策身份覆盖固定文字、目录构造、评审验证和持久化接线源码。旧配置不启用目录或逐项证据，不静默改变既有根的政策；升级继续沿用原显式支持规则。

## 逐项评审报告

新运输profile为`task-review-assessment-proposal/v1`，保留原`verdict/summary/findings`并增加`checks`。只有本次原冻结输入生成的目录有效；每个条目恰好对应一个检查，不允许遗漏、重复、外来条目或改变检查标准。

每个检查包含：`itemId`、`assessment`（`pass/fail/unknown/not-applicable`）、`method`（本配置仅`text-review`）、`reason`、`evidence`、`counterexample`与`findingIds`。引用用本次受信构造的sourceId和有界原文quote；sourceId指向原需求、原附件、已确认回答、批准计划或精确候选文件，来源类别不得混同。引用须在指定来源中逐字存在；引用计划或作者自述不因此成为用户提供的既有事实。

恢复类判断的反例记录包含初态、操作、故障点、后态与恢复依据；没有相关变化须明确说明不适用。记录是Reviewer公开的验收依据，不请求或保存隐藏推理。文本检查不能标为浏览器、数据库或故障注入实测；实际执行仍由另行安装的受信Verification能力提供证据。

`accept`要求全部条目为`pass/not-applicable`、所有引用有效且原findings为空。`fail/unknown`必须指向本报告真实finding，明确要求、观察、最小修正及原选果节点；它们不能与`accept`共存。格式或引用不合法时沿用原报告拒绝，不能将其伪装成有效业务判定。有效的`rework/reject`保持模型原意见，进入原Leader修正或失败流程；不由解析器替模型改写verdict。

结构、文本、数组与总字节均闭合且有界；原模型返回最多65536 UTF-8字节、原summary最多4096字节、findings最多16项的限制保留。不得靠截断证据形成有效报告。

## 原回执与持久化

保留原`leader-ports.mjs`字节与opaque receipt权威。新模块只登记原Review Port，登记一次，使用固定profile而非外部回调。原Provider仅调用一次，原handle的started、stop和completion保持归属；原Port先验证运输映射后的六字段报告。

逐项证据只能绑定到同次原receipt、ticket、inputDigest、selectionDigest及原report摘要。受信模块使用私有身份保存原返回中已验证的证据；Provider自报字段、序列化对象或复制receipt不能提供该能力。新配置要求附加证据存在，不能因捕获失败降级为只有总体accept。

Core沿用原finish的清理、deadline、当前性与取消检查，在同一接受事务中把原报告和逐项证据作为同一Artifact提交。新外层证据profile为`task-independent-review/v2`，内容包含`ticketDigest/report/assessment`，内部原六字段report不变；旧v1证据仍可读。事务失败只允许留下未引用字节，不得留下可用的部分评审或绕过门禁的accept。冷恢复继续读取原已提交证据，不重新判定或重复返工。

## API与界面

沿用原ReviewDecision与Artifact API，不新增评审写入端点。界面同时支持v1原报告和v2附加证据，只有绑定校验通过才展示逐项覆盖、方法、来源和未验证项。没有附加证据的历史报告显示旧范围，不能猜测全部要求已覆盖。所有检查仍明确标为文本Review，`exact-files`仍只声明其真实检查内容。

## 实施与验收出口

先完成合同与独立审查，再以三个互不冲突的工作包推进：原回执/证据提交；通用配置/批准条目/运输校验；界面读取与覆盖展示。真实模型前，必须验证缺条目、重复条目、伪引用、旧选果、unknown+accept、原失败或清理未知不能被采纳，以及原拒收→限额内返工→新选果重审与冷恢复。

业务复验保留原需求、原负例和历史失败。S02正例保留日程及报名区，明确拟议安排与未知渠道，不删用户要求；C01正例补闭合的完成判定和失败续建规则，不强制某个数据库或字段名。原反例须能拒收，合理正例须能接受，之后再运行新配置下完整真实Task。组件通过、结构通过或一次模型成功均不自动关闭整体验收。

该合同仍不能发现模型没有枚举的业务事实，或保证真实引用支持其推论。如果交付物是方案，pass表示文本设计闭合，实际软件行为仍未实测，不因本轮未实现软件便拒绝合理方案。只有用户要求实际效果而没有相应能力，或文本本身缺少关键依据，才保留unknown或请求必要澄清；不得以新增checks数组宣称通用业务确定性正确。


## 精确数据合同与补充门禁

### 目录

保留的目录记录为`{profile:"task-review-criteria/v1",items:[...]}`。每项仅含`id/index/requirementDigest/method/allowNotApplicable/policyId`：index为原acceptance中的非负整数索引，requirementDigest为对应原文UTF-8的SHA-256，id为`criterion-<index>-<16位摘要前缀>`，同时严格核验完整requirementDigest；索引保证本目录唯一，短ID不替代完整摘要绑定，method固定`text-review`。业务条目policyId为null、allowNotApplicable为false；固定政策policyId依次为`scope/facts/effects/recovery`，只有后两项允许有依据的不适用。

目录在原业务条目及四个政策文字之后添加，后续Core文件布局、交付和受管政策技术记录不递归纳入。模型提交任何保留目录profile均拒绝，不用“已有目录”绕过构造。新profile原业务条目最多12项、每项最多2048 UTF-8字节；目录最多16项，每项必须精确对应原文、方法与适用性。四项固定政策文字也分别不超过2048 UTF-8字节；单个目录仍须满足原acceptance单项4096字节上限，包含12业务项与4政策项的最大合法目录必须有实测边界用例。限制在计划提示和拒绝原因中明确，超限不能截断或静默丢项。

四项政策分别固定检查范围和创作边界、来源真实性、操作承诺、数据与失败恢复。批准页面以业务语言展示原文并区分原业务项与配置政策；目录只是编号与关联，不表示检查已经执行。

### 来源与检查

受信来源目录由本次原Review输入产生，不能由模型提供：原`task.input`为`task-input`，批准计划为`approved-plan`，已确认的`interactions.replies`分别为`confirmed-answer`；完整materials按原inputArtifacts身份区分`input-artifact`，按原selection的worker/node身份区分`candidate`，其余只标为`context`。不得把pending请求、原计划或作者内容归类为用户确认事实。结构化来源文本使用确定性`encode`后的UTF-8，文件来源使用原content字节；都核验原材料长度与摘要，先保持合法Unicode及无NUL。

存储的来源项仅含`id/kind/label/digest/locator`：id按固定输入顺序生成`source-<index>`，digest绑定精确引用文本，label为有界可读名称，locator明确本次输入路径或materials索引。正文不重复存入附加证据。最多128项来源，label最多1024字节，locator最多1024字节；不接受模型追加、重命名或重分类。引用对象仅含`sourceId/quote`，quote为无NUL的合法Unicode、最多512 UTF-8字节；除下述零字节来源例外外不得为空，须在指定来源原文中逐字存在，不先规范化替换。存在但不支持结论的引用仍是语义缺陷，必须纳入正反评测。

每个check闭合包含`itemId/assessment/method/reason/evidence/counterexample/findingIds`。reason非空、最多1024 UTF-8字节；evidence最多16条引用。pass至少有一条真实引用，not-applicable仅对允许项成立且有引用与理由。整个检查集合必须覆盖每份原selection候选文件，不能完全忽略某个最终分支。引用存在不代表引用支持推论。

counterexample为null或闭合对象`{initial,operation,failure,result,recovery}`，各字段非空且最多768 UTF-8字节；effects/recovery条目判pass时必须提供该对象，判不适用时为null，fail/unknown按实际可确定内容记录，不虚构缺少的状态。该记录是公开验收依据，不是隐藏思维链。

findingIds最多一项；fail/unknown必须恰好关联本报告一个finding，finding.requirement逐字对应原条目，所有finding均被关联，不允许挂无关反馈掩盖缺项。原必需业务项不得标not-applicable。非空理由和引用真实性不能证明不适用判断正确，仍须业务评测。

### 附加证据与版本

assessment闭合包含`profile/inputDigest/selectionDigest/planDigest/reportDigest/criteriaDigest/criteria/sources/checks`，profile为`task-review-assessment/v1`。criteria为受信目录展开后的`{id,index,requirement,requirementDigest,method,allowNotApplicable,policyId}`列表；criteriaDigest绑定该完整列表，reportDigest绑定原六字段report，其他摘要绑定同次原输入、选果与批准计划。

v2外层闭合为`{profile:"task-independent-review/v2",ticketDigest,report,assessment}`。完整外层最多131072 UTF-8字节，并继续保留原后继Leader完整输入196608字节上限；超限不截断、不退回只有v1。界面验证闭合结构、摘要和原report关联；未知或损坏v2不能按v1降级显示通过。完整引用可按来源类别和名称展开，技术ID进入详情。

原单次Provider completion仅由受信适配器捕获；原Port验证完成并通过`receipt(port,ticket,result)`后，才把与同次原report摘要一致的附加数据绑定到原result私有身份。Core需要该能力时，缺失、外来、复制或不一致均不能接受原总体accept。新证据不从诊断事件或公开JSON恢复私有资格。

崩溃矩阵必须覆盖：原receipt产生后但提交前、Artifact staging后、事务提交但响应丢失。提交前冷开不能恢复内存资格或制造接受；提交后读取原完整v2证据，不重调模型或重复返工。固定注册/目录/mapper/持久化代码与文字身份变化必须在owner claim前拒绝旧根，不靠marker改写升级。

### 未确认与返工

unknown应区分缺候选规则和缺用户事实：前者可进入原repair，后者通过既有Leader问答澄清，不能要求作者编造渠道。允许待确认占位的展示任务，可在保持原目标且清楚标示时通过；必须提供真实报名或外部效果的任务则不能用占位通过。预算不足、仍缺依据或未获授权时保留原失败/待处理，不提额、不另建Task抹去失败。

repair后必须用新selection和新原输入重新生成全部checks，不从旧候选复制通过资格。组件验收须覆盖合法方案正例、真实引用却不支持结论、必需项冒充不适用、unknown+accept、未实测冒称执行、冷恢复和批准前政策可见性；不得只围绕固定案例的关键词写规则。

空来源边界：原材料允许零字节和纯空白，仍精确核对 bytes/digest。quote 允许原样空白；仅当来源摘要等于零字节内容的 SHA-256 时才允许空字符串 quote，明确表示该来源为空，不代表存在正文。空候选仍必须有对应 sourceId 的证据覆盖；界面须显示“原材料为空（0 字节）”或可见空白说明，不能伪造引文或跳过来源。该例外仅用于引用，reason 等解释仍必须非空。
