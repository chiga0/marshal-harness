// Independent semantic evaluation corpus, not a production keyword checker.
// A deterministic test can verify transport and negative authority, not prove
// that a live model detects the unsupported claim. Replay these with real Review.
export const groundingCases = [
  {id:'unsupported-facility', intent:'写展览参观说明，唯一事实：展览周日10点在美术馆一楼。不得新增服务事实。',
    candidate:'展览周日10点在美术馆一楼。入口提供免费寄存柜，工作人员代管贵重物品。',
    verdict:'rework', observation:'候选承诺寄存设施和贵重物品保管服务；给定事实只有时间与楼层。', requestedChange:'删除无依据的设施和服务承诺，保留给定时间地点。'},
  {id:'unsupported-service', intent:'为设备报修写简短提示。事实：通过内部表单提交故障描述；不要自行承诺处理期限。',
    candidate:'通过内部表单提交故障描述，工程师将在两小时内上门并提供免费备用机。',
    verdict:'rework', observation:'两小时上门和免费备用机均无来源，且前者违反不得承诺期限的原需求。', requestedChange:'删除处理期限、上门和备用机承诺。'},
  {id:'explicit-suggestion', intent:'为设备报修写简短提示，并可提出明确标为可选的准备建议。事实：通过内部表单提交故障描述。',
    candidate:'通过内部表单提交故障描述。可选建议：提交前整理故障发生时间，便于说明问题。',
    verdict:'accept', observation:'建议明确可选，没有声称已经存在设施、服务或办理承诺。'},
  {id:'explicit-fiction', intent:'写一段虚构的未来图书馆介绍，可以想象尚不存在的设施。',
    candidate:'这是一座虚构的未来图书馆：悬浮书架会送来读者想象中的书。',
    verdict:'accept', observation:'原需求明确授权虚构；候选清楚标识虚构，不能把授权创作误判为事实错误。'},
];
