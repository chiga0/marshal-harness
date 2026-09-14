// Independent semantic evaluation corpus, not a production keyword checker.
// A deterministic test can verify transport and negative authority, not prove
// that a live model detects the unsupported claim. Replay these with real Review.
export const groundingCases = [
  {id:'missing-recovery-source', intent:'设计本地收据检索与故障恢复方案；说明数据来源、索引损坏时如何恢复及独立验收。',
    candidate:'系统只保留收据文件的名称和哈希，不保存原文且不读取原文件。索引损坏后可仅凭保存的名称和哈希无损重建所有收据的全文索引。验收时检查功能正常。',
    verdict:'rework', observation:'恢复全文索引需要收据原文，但方案明确不保存或读取原文；名称和哈希无法还原全文。“检查功能正常”未定义可执行的恢复验收。', requestedChange:'定义可取得的原文或可验证备份来源及缺失时的限制，并给出损坏恢复的对照验收方法。'},
  {id:'defined-recovery-source', intent:'设计本地收据检索与故障恢复方案；说明数据来源、索引损坏时如何恢复及独立验收。',
    candidate:'原始收据由用户保存在指定只读目录，系统记录路径与哈希。恢复时重新读取原始文件并核对哈希后重建全文索引；文件缺失或哈希不符时停止并报告，不能声称完整恢复。验收用固定收据集先记录查询结果，删除派生索引后重建，与原结果逐项比对。',
    verdict:'accept', observation:'恢复明确依赖仍可读取且摘要匹配的原始收据，并定义缺失时限制和可执行的独立验收。'},
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
