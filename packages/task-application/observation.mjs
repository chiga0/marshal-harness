import {boundedPublicText, normalizedDiagnostic} from '../agent-observation/normalization.mjs';
import fs from 'node:fs';
import {createHash} from 'node:crypto';

// Optional provider observations never authorize execution or acceptance.
const activities = new Set(['starting', 'waiting', 'thinking', 'output', 'tool', 'retrying', 'compacting', 'stopping', 'terminal', 'unknown']);
const kinds = new Set(['read', 'edit', 'delete', 'move', 'search', 'execute', 'think', 'fetch', 'other']);
const statuses = new Set(['pending', 'in_progress', 'completed', 'failed']);
const text = value => typeof value === 'string' && value.length > 0 && value.isWellFormed() && !value.includes('\0') && Buffer.byteLength(value) <= 256;
const count = value => value === null || Number.isSafeInteger(value) && value >= 0;
const closed = (value, fields) => value && typeof value === 'object' && !Array.isArray(value) &&
  Object.keys(value).length === fields.length && fields.every(field => Object.hasOwn(value, field));
export function normalizedObservation(value, sequence, observedAt) {
  if (!value || !activities.has(value.activity) || !Number.isSafeInteger(sequence) || sequence < 1 || sequence > 4096 || !Number.isFinite(Date.parse(observedAt))) return null;
  const tool = value.tool ?? null, model = value.model ?? null, usage = value.usage ?? null;
  if (tool !== null && (!closed(tool, ['id', 'kind', 'status']) || !text(tool.id) || !kinds.has(tool.kind) || !statuses.has(tool.status))) return null;
  if (model !== null && (!closed(model, ['id', 'source']) || !text(model.id) || model.source !== 'provider-reported')) return null;
  if (usage !== null && (!closed(usage, ['inputTokens', 'outputTokens', 'totalTokens', 'source', 'complete']) ||
    ![usage.inputTokens, usage.outputTokens, usage.totalTokens].every(count) || usage.source !== 'provider-reported' || typeof usage.complete !== 'boolean')) return null;
  const response = value.lastResponseUsage;
  const lastResponseUsage = closed(response, ['inputTokens', 'outputTokens', 'totalTokens', 'source', 'scope', 'complete', 'zeroMayBeDefault']) &&
    [response.inputTokens, response.outputTokens, response.totalTokens].every(number => Number.isSafeInteger(number) && number >= 0) &&
    response.source === 'qwen-acp-meta' && response.scope === 'last-response' && response.complete === false && response.zeroMayBeDefault === true ? response : null;
  const diagnostic = normalizedDiagnostic(value.diagnostic);
  return structuredClone({...(diagnostic ? {diagnostic} : {}), ...(lastResponseUsage ? {lastResponseUsage} : {}), profile: 'task-observation/v1', activity: value.activity, observedAt, sequence, tool, model, usage, publicText: typeof value.publicText === 'string' && value.publicText.isWellFormed() && !value.publicText.includes('\0') && Buffer.byteLength(value.publicText) <= 65536 ? boundedPublicText(value.publicText) : ''});
}
export function settleObservation(worker) {
  if (!worker.observation) return;
  if (['completed', 'failed', 'cancelled'].includes(worker.status)) worker.observation.activity = 'terminal';
  else if (worker.status === 'unknown') worker.observation.activity = 'unknown';
  else if (worker.status === 'stopping') worker.observation.activity = 'stopping';
}

export const OBSERVATION_POLICY = Object.freeze({id: 'builtin-text-redaction', version: '1',
  sourceDigest: 'sha256:' + createHash('sha256').update(fs.readFileSync(new URL('./observation.mjs', import.meta.url))).update(fs.readFileSync(new URL('../agent-observation/normalization.mjs', import.meta.url))).update(fs.readFileSync(new URL('./input-audit.mjs', import.meta.url))).digest('hex')});
export function observationConfiguration(value) {
  if (value === undefined || value === null) return null;
  if (!closed(value, ['profile', 'retainPrompts']) || value.profile !== 'task-observation/v1' || typeof value.retainPrompts !== 'boolean') throw new TypeError('invalid_observability');
  return Object.freeze({...value, policy: OBSERVATION_POLICY});
}

export function observedUsage(records) {
  const eligible = records.filter(record => ['agent', 'leader', 'review'].includes(record.ticket.executionType));
  const observed = eligible.map(record => record.worker.observation?.usage).filter(value => Number.isSafeInteger(value?.totalTokens) && value.totalTokens >= 0);
  const tokens = observed.reduce((sum, value) => sum + value.totalTokens, 0);
  if (!observed.length || !Number.isSafeInteger(tokens)) return {tokens:null,cost:null,currency:null,source:'unavailable',coverage:0};
  return {tokens,cost:null,currency:null,source:'reported',coverage:observed.filter(value => value.complete).length / eligible.length};
}
