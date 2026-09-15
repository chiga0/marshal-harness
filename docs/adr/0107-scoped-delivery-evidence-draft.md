# ADR 0107 候选：按责任和时点接纳交付证据

- 状态：Draft，仅供独立审查；未接受、未实现，不授权部署或新增模型调用。
- 日期：2026-09-15
- 基线：`c3e9357de7343c23898abc488d8e8f7b23ee6f41`
- 关联：[ADR0094](0094-trusted-single-user-role-team.md)、[ADR0103](0103-execution-observability.md)、[ADR0104](0104-bounded-leader-protocol-correction.md)、[ADR0106](0106-bound-review-assessments.md)

## 问题和证据

ADR0106约束原文引用、条目覆盖和原receipt绑定，但合法报告仍可能误判。原42ce四例在180秒全部超时；后续各一次600秒窗口结果如下，原失败与原suite不改：

| 案例 | 实际结果 | 缺口 |
| --- | --- | --- |
| S02负例 | 373316ms，合法rework | 抓住无依据日程，却以没有运行日志证明作者未运行/部署 |
| S02修正材料 | 196320ms，输出accept但缺effects反例，无合法回执 | 不能替模型从reason补字段；内容正例不等于过程已证明 |
| C01负例 | 159447ms，合法accept | 引文真实，但自行补出“清单已记录后跳过仍会续建索引”的不存在转换 |
| C01修正材料 | 289624ms，合法accept | 恢复分项有依据；把拟议命令因尚未运行判effects不适用，且未证明原Task实际并行/依赖/后续Verify |

原件与独立判断见[验收记录](../ui-1/experience-e2e-2026-09-14.md#延长窗口揭示的合同语义与用例范围缺口)。`scripts/review-item-oracle.mjs`只比较分项期望，明确仍需语义审查。[确定性后验实验](../testing/business-postconditions.md)只证明冻结订单精算、明确有限模板或内存恢复模型，未接Core，不能证明自然语言与模型一致或真实文件系统行为。

本候选解决责任与时点错配，不宣称结构检查可证明任意业务正确。继续ADR0094可信单用户边界；ADR0103观察可能截断且来自Provider自报，不升级为权威事实。普通宿主进程不是OS沙箱。

## 决定候选：批准前划分检查责任

每个必需要求先确定负责者、方法、时点、证据能力和失败处置，再沿原计划批准。保留原要求全文；一句话混合多种要求时可映射到多个检查项，但不得遗漏、降低要求或事后改成建议。未知能力在批准前明确显示，不能靠作者保证或用户批准凭空产生证明能力。

| 方法 | 负责者及来源 | 可以证明的范围 |
| --- | --- | --- |
| `text-review` | 原独立Review；需求、附件、候选、已确认回答与公开反例 | 方案/文案是否自洽、覆盖目标；不能证明作者实际行为或软件已运行 |
| `execution-evidence` | 原Core/Execution的事实，必要时原已安装受信执行能力的回执 | 批准先于启动、依赖成果先于整合执行、原命令准入等明确事实；不以缺日志或配置存在证明没有旁路 |
| `postcondition` | 部署者审核、预装并冻结身份的原Verification；外部效果沿原Postverify | 精确候选或授权效果满足明确谓词；不把作者测试报告、模型生成代码当受信检查器 |

拟议命令仍是方案操作承诺，text-review必须检查其完成路径、反馈与反例，不因不是实测就豁免effects。相反，交付仅为方案时不能强求运行未实现的软件：C01恢复模型实验可辅助人工审查，但不能替代模型与方案对应检查，不能称为实际崩溃恢复成功。

确定性检查器从配置声明固定ID、源码/版本摘要、输入Schema、允许资源与限额。Leader只能引用已配置的能力和合法参数；参数只能引用冻结输入/候选或已批准目标，不能上传函数、shell、模块路径、网络地址或凭据。新检查器的安装审核不通过Task HTTP完成。M01精确整数计算是首个候选；S01有限模板必须用户事前明确选择，不能作为自由文案默认规则。

过程事实声明覆盖范围、完整性与身份，至少绑定Task、Worker/Attempt、原执行、配置、相关候选和事件。原Core有因果顺序不等于模型同时计算；并行口径按原要求提前明确。现有文件工具限制能证明受信入口准入范围，不能证明同UID任意旁路从未发生。不能证明的绝对要求保留unknown，若用户不改变要求且无合适能力则不支持，不静默豁免。

## 时点、aggregate和原生命周期

每项检查指定原链上执行时点：`before-delivery` 或 `after-effect`。后者仅用于原已配置且用户明确要求的授权交付/后验路径。禁止循环依赖，例如要求Review先证明尚未执行的Verify；禁止靠提前completed获得消费入口。

纯文件路径保持作者→Review→Verification→交付→conclude。独立消费者可以读受控候选字节，检查通过后交付同一候选；如原要求明确只能最终交付后检查，须在批准前确认现有后验路径支持，否则不接受该路径。不能改时点迎合测试。

Review的aggregate只总结分配给它的文本检查，不能签发整个Task业务完成。各文本项有fail/unknown仍不得accept；非文本项不伪装成text-review pass，也不因尚未到期就强迫作者返工。Core按完整检查目录决定交付及完成条件，未到期不等于已满足；after-effect条件缺失时不得conclude成功。

不新增Task生命周期枚举或独立调度器。原running/verification/delivery阶段继续运行原义务；已接纳取消、清理未知、外部结果未知仍走原机制。内容缺陷走原repair；缺用户事实走原问答；证据能力缺失无法补足时按原失败出口收口，不能把所有unknown都放进可无限等待的intervention。

## 最小标准API与存储变化候选

以下字段仅是待审设计，不是现有OpenAPI声明。实施前必须冻结闭合Schema、限额和反例；HTTP机器权威仍只有原OpenAPI。

本候选统一区分两类摘要：结构化值的摘要是`digest(encode(value))`，其中`encode`只能使用当前Store的规范编码；制品内容的摘要是`digest(exactBytes)`。因此`requirementDigest`应定义为原`Plan.acceptance[index]`字符串的结构化值摘要，而不是对展示文本或原始UTF-8字符直接另算一套未命名算法；若未来必须证明原始文本字节，应新增明确命名的字段和摘要域。`contractDigest`、`capabilityDigest`、`planDigest`、`candidateDigest`和`selectionDigest`均须在各自定义的完整输入对象上使用前述结构化值摘要，不能混用`JSON.stringify`或字段拼接。

1. Plan增加可选、仅新profile必需的 `acceptanceContract`，包含profile、原要求映射及检查列表。每项含稳定ID、原文引用与摘要、method、时点、能力身份/有界参数、允许不适用的条件。完整覆盖必需要求，最多16检查项；一项要求可对应多个检查，超限明确拒绝，不截断。该对象参与原planDigest、批准页面及原CAS。
2. 新评审范围必须显式声明 `scope=text-review` 和契约摘要；原六字段report继续由原Port验证，独立附属证据保存范围。因为0106是全部条目text-review，必须新显式profile/新outer版本，不能在旧v2上偷偷重新解释scope。确切版本号和字段需实施前独立冻结，旧客户端不得把新范围显示为全部验收通过。
3. 复用原Artifact保存契约及受信结果；Task内部增加有界 `acceptanceEvidence` 索引，字段与失效规则见下节。证据接纳与原Worker结算同事务，Core从原事实计算交付门禁，不创建第二状态真值。
4. 沿现有Plan、Audit/Leader只读投影显示条目责任、已满足/未到期/缺证据及阻塞原因。不新增强制通过、上传普通JSON即批准、或运行任意检查的新写端点。旧字段缺省仍是旧保证，不能猜新门禁启用。
5. 原deliver/conclude守卫在新profile检查所有对应时点的必需证据、当前候选、原授权与预算。`exact-files`只覆盖文件真实性，不能消费成业务或过程证明。若某检查所需执行/权限原Verification或Postverify不能承载，当前profile拒绝，不扩权限或塞入普通文件检查器。

新契约、能力与接线源码纳入新根身份；旧根不回填新批准、旧Task不换验收标准。保留原leader-ports字节、opaque receipt与never-permitted规则。公开日志和配置对象不能铸造受信证据。确有来源需求时新增的是受信事实投影，不是放宽ADR0103观察权威。

## 证据索引的闭合形状与有效性

候选索引外层闭合为 `{profile,taskId,contractDigest,entries}`，profile拟为`task-acceptance-evidence/v1`，entries最多16项且checkId唯一。`taskId`必须与当前 Task 相同，所有`attemptRef.taskId`、来源 Artifact 的`taskId`和外部动作的授权归属也必须逐项匹配。证据摘要的输入范围固定为`{profile,taskId,contractDigest,entries}`，不包含`evidenceDigest`自身；不能把脱离 Task 上下文的相同合同或相同摘要当作本 Task 的证据。每条闭合为：

```text
{contractDigest, checkId, method, capabilityId, capabilityDigest,
 owner:{kind,workerId,attemptRef,generation}, planDigest, repairId,
 candidateDigest, selectionDigest, externalAction, receiptDigest,
 sourceArtifact, applicability, result}
```

其中 `sourceArtifact` 不是单独的摘要字符串，而是可在当前 Task 的受信 Artifact manifest 中定位的闭合引用：

```json
{
  "id": "artifact-...",
  "digest": "sha256:..."
}
```

实施 Schema 不得同时引入 `sourceArtifactDigest` 这一平行字段；若需要多来源，必须由受信生产者先生成一个有界清单 Artifact，再由该对象逐项列出来源 `id`、`digest`、`taskId`、`kind`、`status`、`mediaType` 和 `bytes`，而不是让调用者提交一组摘要自行取得资格。

`attemptRef` 采用当前 Node 已有耐久事实的复合引用，不能写成尚未存在的独立 `attemptId`：

```json
{
  "profile": "task-attempt-ref/v1",
  "taskId": "task-...",
  "workerId": "worker-...",
  "commandId": "command-...",
  "generation": "1",
  "reservationDigest": "sha256:...",
  "reservationEvent": {
    "stream": "task-...",
    "sequence": "12",
    "digest": "sha256:..."
  }
}
```

`workerId`、`commandId`、`generation`、`reservationDigest` 和 `reservationEvent` 必须共同从原 `worker.reserved` 事务、ticket/command 与当前 `attempt` projection 重算并核对；任何一项不匹配都拒绝接纳。`reservationEvent` 不是展示信息，它把复合引用锚定到 Store 的耐久因果来源。当前 Node 的 `outbox.attempt_id` 通常为空，不得拿它补齐 `attemptRef`；`Worker.worker.attempt` 只可作为展示用的本地序号，不能作为唯一身份。`kind=core` 的过程事实可使用 `workerId=null`，但仍须引用产生该事实的原事务事件和 command，不能凭空构造 Worker Attempt。

- 摘要均为完整SHA-256，checkId来自原批准目录，method为本文三种之一；capabilityId为启动前登记的固定ID，capabilityDigest绑定配置及实现。Core原事实生产者也使用固定能力身份，不由Provider填入。result仅为`pass/fail/unknown`；未执行或未到期表示条目尚无证据，不能用null结果冒充通过。不适用按下述applicability分支保留，不能由空索引推断。
- applicability仅为`applicable/not-applicable`。普通检查为applicable并保留原pass/fail/unknown；not-applicable仅可映射为result=pass，表示“原契约允许的不适用条件已由受信接纳确认”，不是业务操作已执行。接纳必须同时核对原check允许NA、该项明确的不适用条件、原来源报告的NA判断/理由/引用与当前候选；缺任一项拒绝此分支，不能将未知自动改成NA。源Artifact必须保留原NA字段、理由及条件依据，索引由该来源派生；普通pass且无原NA来源不得标not-applicable。Core计为满足时仍保留applicability，API/UI须展示“不适用（条件及来源）”而非“检查执行通过”。若该条件只能由语义Reviewer判断，其来源仍标text-review；受信接纳校验身份与契约条件覆盖，不宣称程序证明自然语言判断正确，独立语义验收继续检查错误豁免。
- owner闭合为`kind/workerId/attemptRef/generation`。kind=`worker`时`workerId`与`attemptRef.workerId`必须相同，且复合引用的所有字段都来自本次原执行；kind=`core`仅用于不启动Worker的原事务事实，`workerId=null`，`attemptRef`仍须引用产生该事实的原 command/事务事件，generation来自原接纳事务。不能虚构Worker给Core事实背书，也不能拿作者身份签独立检查。
- planDigest绑定批准计划，repairId为原当前repair身份或确实无repair时null。selectionDigest绑定完整冻结选果；candidateDigest绑定按确定性顺序编码的候选文件清单（节点、路径、文件摘要、字节数），不是模型自报正文hash。任何一项不得用随机值补齐。
- externalAction为null或闭合`{actionId,authorizationDigest,targetDigest}`，只引用原已批准动作，不授新权；receiptDigest为原回执的公开规范化引用摘要或null，不能据此重建opaque receipt。外部效果必需externalAction与原可信回执，普通Verification必需原回执；Core原事实允许receiptDigest=null，但sourceArtifactDigest必须指向受信事务导出的证据。
- sourceArtifact绑定同Task、ready状态、精确字节的原证据Artifact，并同时校验其`id`、`digest`、`kind`、`mediaType`和`bytes`；需要多来源时由受信生产者生成有界证据清单Artifact，逐项校验来源ID/摘要/字节/Task身份。文本证据关联原Review envelope；Core事实包含原事件序号与覆盖范围；公开日志或仅相同摘要的普通上传不能获得生产者身份。
- 索引每条最多2048 UTF-8字节，完整索引最多32768字节；基础ID使用现有ID语法及128字节限额，generation沿原十进制非负整数表示。来源正文沿原Artifact限额，整体Review输入仍遵守原限额；超限失败不截断。实施Schema必须固定nullable分支和result所需证据，不允许未知字段。

当前可消费证据必须同时满足契约、计划、检查、能力、候选、选果和repair身份匹配。新计划/目录/能力改变不能重用；任何选果、文件字节或repair改变，首版保守使该索引整体失效，要求新证据，不从旧pass挑选拼接。即使旧文件未改，也不自动跨repair复用。原记录保留供审计，失效不改写成从未发生。

generation是接纳资格而非成果有效期：新结果必须来自当前原owner/执行资格，旧代迟到结果拒绝。冷重开可以读取原已提交证据，其原generation不改写为新代；在契约/候选/原批准未变且原恢复规则允许时继续使用。尚未提交的内存receipt、staged文件、普通JSON不能冷恢复成资格。外部目标可能变化时还须原postverify声明的时效/目标版本成立，单凭候选没变不足以复用后验。

## 原链上逐点校验

| 验证点 | 必须完成的检查 | 不成立时 |
| --- | --- | --- |
| proposal形成 | 受信契约构造器核原要求完整映射、已配置方法/能力、参数/时点合法、无循环；模型不得自行授予能力 | 计划不进入可批准状态；保留明确不支持原因 |
| 用户批准 | Plan展示原要求和检查责任；contractDigest参与planDigest；原revision/digest CAS同时确认，不能只批准旧文字 | 冲突拒绝，不补批准、不自动迁移 |
| Review预留/接纳 | 原ticket绑定当前契约和选果；只接分配文本条目的原Port/receipt及新scope；严格字段、引用、cleanup/期限/当前性 | 原失败；有效fail/unknown按缺口处理，不能变全Task通过 |
| Verification预留/接纳 | 能力固定且独立、所需源/权限/预算成立；原回执绑定本次Worker和精确候选，sourceArtifact与索引同事务 | 缺证据/失败不回退到Review意见，保持原失败或允许的repair |
| deliver | 重新核原批准、最新selection/candidate/repair和全部before-delivery必需项，不能只检查Reviewaccept或旧acceptanceDigest | 拒绝动作；不发布或交付为已验收成果 |
| conclude | 必需交付及after-effect证据成立、原外部动作/目标版本/授权绑定有效，所有到期项无unknown/fail | 不得completed；按原收口/lookup/失败路径处理 |

旧客户端可继续消费旧profile；新profile要求能展示并验证新契约的客户端。新字段导致旧闭合Schema拒绝应明确显示版本不支持，不悄悄省略字段再准许批准。若客户端沿旧请求调用批准，新profile准入必须要求显式携带contractDigest并与原planDigest共同校验；这只是原批准端点的版本化必需字段，不新增审批动作或可绕过的第二批准。服务不能仅凭客户端声称已展示而降低证据门禁。后继实现须同步OpenAPI、Transport、UI和兼容用例。

## unknown、重评预算与恢复

unknown必须说明缺的是候选规则、用户事实、执行证据还是不可达能力。不得把缺过程事实转成“请作者保证未运行”；不得将未知项目改成not-applicable。计划所需能力不可用时应尽早拒绝，不等完成后制造无法解决的返工。

本候选不启用新协议重评：默认计数为0、上限0。已有ADR0104的Leader格式纠错保持至多1/Task及原范围，不能用于Review合法JSON缺字段。若后续确需Review返回合同纠错，应另作明确的窄增量设计：新显式profile、最多一次完整新Review调用、原预算内计费、固定可信分类、原失败保存；不能通过多次调用挑选accept。该增量不是本候选集成前置，不在此擅自增加调用。

即使将来允许补合同，counterexample缺失也需要重新分析，允许新报告改unknown/rework；不自动补字段、不从reason拼对象、不锁accept。语义误收、错误引用、越权、合法rework/reject不能当格式错误重试。没有原完整完成结果、已确认cleanup或明确分类就不具资格。

当前默认900秒从Task创建开始，是整个Task预算。规划、批准等待、作者、Review、Verification、返工和后验共同消耗原Attempt、容量、期限，重评或恢复均不得重置。准入采用可执行下限：计算原剩余必需路径需要的最少Attempt和Leader call数（含尚未执行的作者、Review、Verification、交付/后验和收尾），要求`已用+最少所需<=原上限`，再检查当前时刻小于原deadline、原容量和取消/暂停准入。能力如声明硬执行上界，只可用真实配置约束，不猜模型完成时间。该下限只能排除确定无额度的路径，不能保证剩余时间足够；运行仍由原deadline停止并记失败。观测可提示剩余时间但不自动加时。代价过高时应改设计/检查范围后创建明确新实验，保留旧失败，不能偷偷续期。

证据提交按原command、契约及候选摘要幂等。提交前崩溃不能从公开报告重造receipt；提交后丢响应只读取原已提交事实。若未来新增重评，原失败Outcome、固定分类、计数和唯一后继命令必须同事务，claim/start后沿原恢复出口，不能再次调用模型假装恢复。取消、旧generation、迟到、候选变化、清理未知都不能放行旧证据；外部效果仍只沿原lookup核对，不盲重发。

## 分阶段退出门槛

| 阶段 | 必须得到的独立证据 |
| --- | --- |
| 受控合同 | 批准前完整原要求映射；缺过程证据不被文本accept覆盖；未来Verify不循环阻塞；伪造回执、错候选、重复提交、冷开/取消保持原门禁 |
| S01 | 原自由文案不得被有限模板收窄；事实/建议边界正反例；真实独立消费与原过程要求按事前责任分别验收，缺证据不假PASS |
| M01 | 原订单输入绑定、BigInt精算、零金额/退款/取消负例；原受信Verification同包接线验证，再真实浏览器从零交付；精算不证明作者未用工具 |
| C01 | 原缺失续建分支必须拒收；明确完成/一致性与失败重建的方案可按文本分项接受；effects必须适用，真实并行和依赖用独立过程证据；不得以有限模型通过冒充真实FS恢复 |
| 完整路径 | 同安装包、原Task预算下从零链；所需真实后验与最终候选一致；已有失败保留，不通过换模型/重跑累计不同候选绿色 |
| UI | 功能可靠性、真实浏览器交互、目标用户可用性分别记录；文本意见与交付条件清楚区分，未执行的真人验收继续NOT_RUN |

确定性退出单独要求：Schema/摘要/回执/事务/预算下限/取消/恢复用例与M01精算反例通过；这些不需要模型，也不证明Reviewer推论正确。独立语义退出单独要求：冻结原S01/S02事实与创作、C01失败窗口和适用性正反例逐项审查，不只比较verdict或关键词；原input/候选不变、失败保留，有限模型结果不外推真实FS。两类均满足后才进入真实从零业务路径，真实业务仍单独验收。每条结果分别计协议、分项标签、独立语义、真实业务，不以一种替另一种。独立审查必须证明源码未新增Core旁路或权限，再准入任何真实模型实验。

## 实施前必须冻结的P1接缝

本节是阻止实施准入的明确清单，不是已经存在的字段或已接受Schema。上述示意对象须按此清单完成独立复核后才能编码；仅文档中出现名称不能授予能力。

1. **两个新增协议名称及闭集**：冻结Plan验收契约的profile字面值，以及新Review范围/outer的profile字面值与版本；分别列出所有必需/可选/nullable字段、枚举、限额、未知profile拒绝规则及旧版本对应关系。目前不能把示意`scope=text-review`塞入旧v2，或把索引拟名当这两个协议均已确定。
2. **摘要算法及域**：逐一冻结contractDigest、planDigest的包含关系、candidateDigest清单排序/字段、selectionDigest复用原算法的条件及结果Artifact的字节摘要。使用原确定性encode与完整SHA-256时仍须给出精确输入对象、版本域、数组顺序、空值语义和测试向量；不得混用JSON.stringify、展示文本、短ID或文件拼接哈希。批准请求中contractDigest如何参与原planDigest/CAS须有正负例。
3. **capabilityDigest组成**：列出每种能力的固定ID/版本、实现与受信接线源码摘要、参数Schema、允许资源/权限边界、执行上限、证据输出Schema与配置值如何共同纳入确定性摘要；依赖源码与参数顺序明确。不能只哈希显示名称，不能让模型或调用者给出自称可信的digest。配置改变的同根拒绝与旧记录读取边界一起冻结。
4. **证据Artifact必须可定位**：单独的Artifact摘要不足以定位或证明归属。实施Schema必须明确`sourceArtifact.id`与`sourceArtifact.digest`，并将其与Task、kind、ready状态、mediaType、bytes联合验证。只有原受信提交路径建立的引用可接纳，不能通过同digest的任意上传复造资格。多源清单也必须保存各源Artifact ID及摘要，并拒绝跨Task/未提交/被替换来源；不得再引入与`sourceArtifact`并列的`sourceArtifactDigest`字段。
5. **Attempt持久身份来源**：当前示意owner.attemptId尚未证明有独立持久ID，不得生成UUID补位。实施前盘点原Worker.attempt序号、workerId、commandId与原reservation/执行记录，选择经验证可唯一定位的持久引用；若原系统没有独立attemptId，则修订示意字段为显式复合attemptRef并冻结唯一性/代际语义，而非宣称字段已存在。Core类型的null分支同样不可虚构Worker，需引用原事务事件。
6. **Audit与UI精确投影**：冻结Audit.acceptanceEvidence是否为新增可选字段、完整Schema/分页或有界上限、契约和Artifact读取路径、失效/未到期/unknown/NA呈现。服务端权威来源必须唯一，不能由UI根据文本猜满足；损坏新版本不得降级为旧“通过”。逐一列出OpenAPI、Transport、Reader、Plan批准和UI受影响字段及测试，不能仅写“沿原API展示”。
7. **旧客户端兼容**：冻结能力协商或可判定的版本拒绝方式、原批准端点在新profile必需contractDigest的Schema、旧请求明确错误与新客户端读旧记录的行为。不得对旧客户端隐藏新必需条件仍批准，也不将旧根强制升级；对旧profile是否完全无字段变化必须用实际字节/Schema回归确认。

退出证据至少包括机器Schema示例和拒绝变异、摘要测试向量、原持久Attempt来源的代码定位、HTTP完整读取/批准/证据消费与旧客户端拒绝测试方案。任何一项未完成，ADR保持Draft，不能将本候选视为可直接实现的封闭合同。

## 不做事项及接受前问题

不新增工作流/验证器管理平台、动态脚本市场、任意执行入口、模型投票层、第二SQLite或新Task状态机。不引入OS沙箱宣传、不恢复旧Marshal skill、不把更多Agent或更多报告当可靠性证明。不要求方案任务先实现软件，也不删除用户过程要求让oracle变绿。

接受前须明确：新profile/outer精确版本和字段；现有Verification接入首个M01固定检查器的实际身份与暂存约束；过程证明能覆盖哪些要求；16项上限是否适合原样本；只读投影能否完整表达未到期与缺证据。能力超出原权限时先缩小支持范围或另行设计，不以本草案授权扩权。

本文仅候选，不关闭现有P1、不改变原suite或失败、不授予合并/发布批准；设计接受之后仍需独立实现与上述验收。
