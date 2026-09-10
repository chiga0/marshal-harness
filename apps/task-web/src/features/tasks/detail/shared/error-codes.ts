// 已知错误码 → 用户可执行恢复动作的映射。码表对齐 packages/task-api/openapi.json 的 Error.code 闭集；
// 未知码不猜测含义，原样显示 code/requestId。任何展示都不得包含 token。

export type RecoveryAction = 'refresh' | 'reconnect' | 'retry-same-key' | 'fix-input' | 'wait' | 'none';

export interface ErrorGuidance {
  title: string;
  guidance: string;
  action: RecoveryAction;
}

const GUIDANCE: Record<string, ErrorGuidance> = {
  unauthorized: {
    title: '凭据无效',
    guidance: '服务端拒绝了当前凭据（401）。请断开并重新连接；轮询已停止，不会用旧凭据继续请求。',
    action: 'reconnect',
  },
  forbidden: {
    title: '没有权限',
    guidance: '服务端判定你没有执行该操作的权限（403）。请先刷新核对任务与回执，不要换个入口重试同一动作。',
    action: 'refresh',
  },
  untrusted_request: {
    title: '请求不受信任',
    guidance: '服务端拒绝了不受信任的请求来源。请刷新页面后重试；持续出现请核对本机服务配置。',
    action: 'refresh',
  },
  revision_conflict: {
    title: '版本已过期',
    guidance: '对象版本已变化（409）。你的输入已保留；请先查看最新内容，再基于新的 revision 决定是否重试。不会自动替换摘要再提交。',
    action: 'refresh',
  },
  plan_conflict: {
    title: '计划已更新',
    guidance: '计划在你查看期间已变化（409）。已查看的摘要草稿保留，请查看最新计划内容后再决定是否确认。',
    action: 'refresh',
  },
  state_conflict: {
    title: '状态已变化',
    guidance: '任务当前状态不允许该操作（409）。请先刷新核对任务最新状态与回执。',
    action: 'refresh',
  },
  question_expired: {
    title: '问题已过期',
    guidance: '该问题已超过答复期限，无法作答。请刷新任务；如有新问题会在「需要处理」中出现。',
    action: 'refresh',
  },
  idempotency_conflict: {
    title: '同键请求已受理',
    guidance: '相同 Idempotency-Key 但内容不同的请求已被受理。请勿再次提交，先刷新核对任务状态与最近回执。',
    action: 'refresh',
  },
  invalid_idempotency_key: {
    title: '请求标识无效',
    guidance: '本次请求的幂等标识未被接受。这是客户端问题；可以重新发起一次该操作（会生成新的标识）。',
    action: 'none',
  },
  request_timeout: {
    title: '请求超时',
    guidance: '结果未知：请求可能已被服务端受理。可以显式原键重放一次（不重新执行），或先刷新核对任务状态。',
    action: 'retry-same-key',
  },
  application_unavailable: {
    title: '应用暂不可用',
    guidance: '服务端应用暂时不可用（503）。请稍后刷新；结果未知时不要重复提交写操作。',
    action: 'wait',
  },
  not_ready: {
    title: '服务未就绪',
    guidance: '服务尚未就绪。请稍后刷新重试；不要重复提交写操作。',
    action: 'wait',
  },
  artifact_not_ready: {
    title: '成果未就绪',
    guidance: '该成果尚未就绪，暂不能下载。请稍后刷新再试。',
    action: 'wait',
  },
  not_found: {
    title: '对象不存在',
    guidance: '请求的对象不存在或已被清理。请刷新列表核对；不要基于旧链接重复操作。',
    action: 'refresh',
  },
  request_too_large: {
    title: '内容超出限制',
    guidance: '请求内容超出服务端限制（413）。请减小内容后再提交。',
    action: 'fix-input',
  },
  capacity_exceeded: {
    title: '容量已满',
    guidance: '服务端容量已满（429）。请稍后再试，不要连续重复提交。',
    action: 'wait',
  },
  recovery_required: {
    title: '需要人工干预',
    guidance: '服务端要求先完成恢复核对。请按本机服务输出指引处理后再刷新。',
    action: 'refresh',
  },
  unsupported_operation: {
    title: '此服务不支持该操作',
    guidance: '当前服务版本未提供该能力（501）。界面不会用其他操作代替执行；请按服务实际支持面处理。',
    action: 'none',
  },
  unsupported_task: {
    title: '任务类型不受支持',
    guidance: '该任务不在当前服务支持面内（501）。不会回退到其他操作代替执行。',
    action: 'none',
  },
  invalid_json: {
    title: '请求格式错误',
    guidance: '请求正文不是有效 JSON（400）。这是客户端问题，请联系作者修复；服务端未执行任何动作。',
    action: 'none',
  },
  invalid_request: {
    title: '请求无效',
    guidance: '请求未通过服务端校验（400/422）。请核对输入；服务端未执行任何动作。',
    action: 'fix-input',
  },
  invalid_content_type: {
    title: '内容类型不支持',
    guidance: '服务端不接受该内容类型（415）。服务端未执行任何动作。',
    action: 'none',
  },
  invalid_application_response: {
    title: '应用响应无效',
    guidance: '服务端应用返回了无效响应。请先刷新核对任务状态；结果未知时不要重复提交。',
    action: 'refresh',
  },
  method_not_allowed: {
    title: '方法不允许',
    guidance: '该接口不接受此请求方法（405）。这是客户端问题，请联系作者修复。',
    action: 'none',
  },
};

const FALLBACK: ErrorGuidance = {
  title: '操作失败',
  guidance: '服务端返回了未识别的错误。请展开技术详情查看错误码与 requestId；结果未知时不要重复提交写操作。',
  action: 'refresh',
};

export function guidanceFor(code: string | null | undefined): ErrorGuidance {
  if (!code) return FALLBACK;
  return GUIDANCE[code] ?? FALLBACK;
}

export const RECOVERY_LABELS: Record<RecoveryAction, string | null> = {
  'refresh': '刷新查看最新状态',
  'reconnect': '断开并重新连接',
  'retry-same-key': '原键重放（不重新执行）',
  'fix-input': '修正后再提交',
  'wait': '稍后重试',
  'none': null,
};

export interface RenderedError {
  code: string;
  status: number | null;
  message: string;
  requestId: string | null;
}

export function toRenderedError(error: unknown): RenderedError {
  if (error instanceof Error) {
    const status = (error as {status?: unknown}).status;
    const code = (error as {code?: unknown}).code;
    const requestId = (error as {requestId?: unknown}).requestId;
    return {
      code: typeof code === 'string' && code !== '' ? code : (error.name || 'unknown_error'),
      status: typeof status === 'number' ? status : null,
      message: error.message !== '' ? error.message : '未知错误',
      requestId: typeof requestId === 'string' && requestId !== '' ? requestId : null,
    };
  }
  return {code: 'unknown_error', status: null, message: String(error), requestId: null};
}
