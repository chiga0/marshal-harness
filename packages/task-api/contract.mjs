import {readFileSync} from 'node:fs';

export const PROFILE = 'node-task-service/v1';
export const contract = JSON.parse(readFileSync(new URL('./openapi.json', import.meta.url), 'utf8'));
export const operations = Object.entries(contract.paths).flatMap(([path, methods]) =>
  Object.entries(methods).map(([method, definition]) => ({
    method: method.toUpperCase(), path, definition,
    operation: definition['x-application-operation'],
    status: definition['x-success-status'],
    request: definition.requestBody?.content['application/json'].schema,
    response: definition['x-response-schema'],
    authenticated: definition.security?.length !== 0,
    paged: definition.parameters.some(p => p.in === 'query'),
  })));

export function resolve(reference) {
  if (typeof reference !== 'string' || !reference.startsWith('#/')) throw new TypeError('non-local-schema');
  const found = reference.slice(2).split('/').reduce((value, part) =>
    value?.[part.replace(/~1/g, '/').replace(/~0/g, '~')], contract);
  if (!found) throw new TypeError('unknown-schema');
  return found;
}
// A bounded validator for this contract's explicit subset. No remote schema,
// eval, coercion, defaults or removal of unknown properties. Unsupported schema
// keywords are programming errors, not silently accepted validation rules.
const keywords = new Set(['$ref', 'type', 'const', 'enum', 'required', 'properties',
  'additionalProperties', 'items', 'minItems', 'maxItems', 'minimum', 'maximum',
  'minLength', 'maxLength', 'pattern', 'format', 'anyOf', 'oneOf', 'description', 'examples',
  'x-maxUtf8Bytes', 'x-wellFormedUnicode']);
export function validate(value, schema, depth = 0) {
  if (typeof schema === 'string') schema = contract.components.schemas[schema];
  if (!schema || depth > 40) return false;
  for (const key of Object.keys(schema)) if (!keywords.has(key)) throw new TypeError('unsupported-schema-keyword');
  if (schema.$ref && !validate(value, resolve(schema.$ref), depth + 1)) return false;
  if (schema.anyOf && !schema.anyOf.some(s => validate(value, s, depth + 1))) return false;
  if (schema.oneOf && schema.oneOf.filter(s => validate(value, s, depth + 1)).length !== 1) return false;
  if (schema.type) {
    const types = Array.isArray(schema.type) ? schema.type : [schema.type];
    const type = value === null ? 'null' : Array.isArray(value) ? 'array' : typeof value;
    if (!types.some(t => t === type || t === 'integer' && Number.isSafeInteger(value))) return false;
  }
  if (Object.hasOwn(schema, 'const') && schema.const !== value) return false;
  if (schema.enum && !schema.enum.includes(value)) return false;
  if (typeof value === 'number' && (!Number.isFinite(value) ||
      schema.minimum !== undefined && value < schema.minimum ||
      schema.maximum !== undefined && value > schema.maximum)) return false;
  if (typeof value === 'string') {
    const length = [...value].length;
    if (schema.minLength !== undefined && length < schema.minLength ||
        schema.maxLength !== undefined && length > schema.maxLength ||
        schema.pattern && !new RegExp(schema.pattern, 'u').test(value) ||
        schema['x-maxUtf8Bytes'] && Buffer.byteLength(value) > schema['x-maxUtf8Bytes'] ||
        schema['x-wellFormedUnicode'] && !value.isWellFormed()) return false;
    if (schema.format === 'date-time' && (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value) || !Number.isFinite(Date.parse(value)))) return false;
  }
  if (Array.isArray(value)) {
    if (schema.minItems !== undefined && value.length < schema.minItems || schema.maxItems !== undefined && value.length > schema.maxItems) return false;
    if (schema.items && !value.every(v => validate(v, schema.items, depth + 1))) return false;
  }
  if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
    if (schema.required?.some(key => !Object.hasOwn(value, key))) return false;
    for (const [key, child] of Object.entries(value)) {
      if (schema.additionalProperties === false && !Object.hasOwn(schema.properties, key)) return false;
      if (schema.properties?.[key] && !validate(child, schema.properties[key], depth + 1)) return false;
    }
  }
  return true;
}

// Public producer/consumer binding only. It does not decide whether a question
// exists, is authorized, has expired or has been consumed by the original Agent.
export function validAnswerResponse(request, value) {
  const runtime = Object.hasOwn(request.body, 'questionDigest');
  if (!validate(value, runtime ? 'RuntimeAnswerReceipt' : 'AnswerReceipt') ||
      value.taskId !== request.taskId || value.questionId !== request.questionId || value.operation.kind !== 'task.answer' ||
      value.operation.taskId !== request.taskId || value.task.id !== request.taskId || value.currentTask.id !== request.taskId ||
      value.acceptedRevision !== request.body.expectedRevision + 1 || value.task.revision !== value.acceptedRevision ||
      value.operation.taskRevision !== value.acceptedRevision || value.currentTask.revision < value.acceptedRevision ||
      value.currentTask.revision === value.acceptedRevision && (value.currentTask.plan?.digest !== value.task.plan?.digest ||
        value.currentTask.plan?.revision !== value.task.plan?.revision)) return false;
  if (runtime) return value.questionDigest === request.body.questionDigest;
  return value.preview.digest === value.acceptedPreviewDigest && value.preview.plan.taskId === request.taskId &&
    value.task.plan?.digest === value.preview.plan.digest && value.task.plan?.revision === value.preview.plan.revision;
}

export function validQuestionItems(value, taskId) {
  return value.items.every(question => question.taskId === taskId && (question.kind !== 'business' ||
    question.subject === question.questionDigest && new Set(question.options.map(option => option.value)).size === question.options.length &&
    (question.status !== 'open' || question.deliveryStatus === null) && (question.status !== 'answered' || question.deliveryStatus !== null) &&
    (question.answer === undefined || (question.deliveryStatus === null ? question.answer === null : typeof question.answer === 'string')) &&
    (question.answer === undefined || question.answer === null || question.options.length === 0 || question.options.some(option => option.value === question.answer))));
}

export function validRepairResponse(request, value) {
  return validate(value, 'RepairReceipt') && value.taskId === request.taskId && value.task.id === request.taskId &&
    value.currentTask.id === request.taskId && value.operation.taskId === request.taskId && value.operation.kind === 'task.repair' &&
    value.operation.status === 'accepted' && value.planDigest === request.body.planDigest && value.decisionDigest === request.body.decisionDigest &&
    value.acceptedRevision === request.body.expectedRevision + 1 && value.task.revision === value.acceptedRevision &&
    value.operation.taskRevision === value.acceptedRevision && value.currentTask.revision >= value.acceptedRevision &&
    value.task.status === 'queued' && value.task.plan?.digest === value.planDigest && value.currentTask.plan?.digest === value.planDigest &&
    value.task.plan.revision === value.currentTask.plan.revision &&
    new Set(value.affectedNodes).size === value.affectedNodes.length && request.body.nodeIds.every(id => value.affectedNodes.includes(id));
}

export function validAuditResponse(value, taskId) {
  if (!validate(value, 'Audit') || value.taskId !== taskId || new Set(value.workers.map(worker => worker.id)).size !== value.workers.length ||
      value.workers.some(worker => worker.taskId !== taskId) || new Set(value.prompts.map(prompt => prompt.workerId)).size !== value.prompts.length) return false;
  return value.prompts.every(prompt => {
    if (!value.workers.some(worker => worker.id === prompt.workerId) || new Set(prompt.contextRefs).size !== prompt.contextRefs.length) return false;
    const observed = prompt.observation;
    if (!observed) return true; // Original API examples/older producers remain valid.
    if (observed.stage === 'unavailable') return prompt.source === 'unavailable' && prompt.text === '' && prompt.contextRefs.length === 0 &&
      observed.coverage === 'unavailable' && observed.promptDigest === null && observed.promptBytes === null &&
      observed.preparedAt === null && observed.handedOffAt === null && observed.policy === null && observed.snapshot === null && !observed.previewTruncated;
    if (observed.promptDigest === null || observed.promptBytes === null || observed.preparedAt === null ||
      (observed.stage === 'prepared' ? observed.handedOffAt !== null : observed.handedOffAt === null)) return false;
    if (observed.coverage !== 'policy-redacted') return prompt.source === 'unavailable' && prompt.text === '' &&
      observed.snapshot === null && !observed.previewTruncated && (observed.coverage !== 'metadata-only' || observed.policy === null);
    const snapshot = observed.snapshot;
    return observed.policy !== null && prompt.source === observed.stage + '-redacted' && snapshot !== null && snapshot.taskId === taskId &&
      snapshot.name === prompt.workerId + '.input.txt' && snapshot.kind === 'evidence' && snapshot.status === 'ready' && snapshot.mediaType === 'text/plain' && snapshot.bytes <= 262144 &&
      Buffer.byteLength(prompt.text) <= 2048 && (observed.previewTruncated ? Buffer.byteLength(prompt.text) < snapshot.bytes : Buffer.byteLength(prompt.text) === snapshot.bytes);
  });
}

const descriptions = {
  'invalid_request': [400, '请求不符合接口合同。', ['correct-request']],
  'invalid_json': [400, '请求必须是合法且无重复字段的 JSON。', ['correct-request']],
  'invalid_idempotency_key': [400, '需要唯一且合法的幂等键。', ['correct-request']],
  unauthorized: [401, '本地访问凭据无效。', ['correct-request']],
  'untrusted_request': [403, '请求来源不受信任。', ['correct-request']],
  forbidden: [403, '当前操作不在批准权限内。', ['query']],
  'not_found': [404, '对象或接口不存在。', ['query']],
  'method_not_allowed': [405, '此接口不支持该方法。', ['correct-request']],
  'revision_conflict': [409, '对象版本已变化。', ['query']],
  'idempotency_conflict': [409, '幂等键已用于不同请求。', ['query']],
  'plan_conflict': [409, '计划版本或摘要不匹配。', ['query']],
  'state_conflict': [409, '当前状态不允许该操作。', ['query']],
  'artifact_not_ready': [409, '成果尚未可供下载。', ['query']],
  'recovery_required': [409, '执行归属或副作用尚未确定。', ['intervention']],
  'question_expired': [410, '问题已过期。', ['query']],
  'request_too_large': [413, '请求超过大小限制。', ['correct-request']],
  'invalid_content_type': [415, '需要不压缩的 JSON 请求。', ['correct-request']],
  'unsupported_task': [422, '任务需要当前未支持的能力。', ['correct-request']],
  'capacity_exceeded': [429, '当前容量或预算不足。', ['query']],
  'unsupported_operation': [501, '当前应用尚未实现此操作。', ['query']],
  'application_unavailable': [503, '应用当前不可用，命令效果须查询。', ['query', 'same-key-replay']],
  'invalid_application_response': [503, '应用响应不符合接口合同，命令效果须查询。', ['query']],
  'not_ready': [503, '服务尚不可接单。', ['query']],
  'request_timeout': [504, '本次 HTTP 等待结束，不代表任务已失败或停止。', ['query', 'same-key-replay']],
};
export class TaskApiError extends Error {
  constructor(code) {
    if (!Object.hasOwn(descriptions, code)) throw new TypeError('unknown-api-error');
    super(code); this.code = code; this.status = descriptions[code][0];
  }
}
export function errorPayload(error, requestId) {
  const code = typeof error?.code === 'string' && Object.hasOwn(descriptions, error.code) && error.status === descriptions[error.code][0] ? error.code : 'application_unavailable';
  const [status, message, allowedActions] = descriptions[code];
  return {status, body: {code, message, requestId, allowedActions}};
}
