// 只读消费现行 OpenAPI；不增加端点、权限、持久状态或任意文件路径。
import contract from '../../../../../packages/task-api/openapi.json';
import {ApiError} from '@/lib/transport/types';
import type {ArtifactRecord} from '@/lib/transport/types';

type Schema = {[$key: string]: unknown; $ref?: string; type?: string | string[]; const?: unknown; enum?: unknown[];
  anyOf?: Schema[]; oneOf?: Schema[]; required?: string[]; properties?: Record<string, Schema>; additionalProperties?: boolean;
  items?: Schema; minItems?: number; maxItems?: number; minimum?: number; maximum?: number;
  minLength?: number; maxLength?: number; pattern?: string; format?: string; 'x-maxUtf8Bytes'?: number; 'x-wellFormedUnicode'?: boolean};
// 静态选择三个入口的依赖闭集，打包器可移除无关请求/响应示例与路径定义。
const {Acceptance, Artifact, Audit, AuditDecision, AuditDisclosurePolicy, ContentRejection, Digest, Id, InputObservation,
  LeaderAuthorization, LeaderRequest, LeaderReview, LeaderView, Progress, Prompt, Rates, RepairAudit, Revision, Usage, Worker, WorkerAudit} = contract.components.schemas;
const schemas: Record<string, Schema> = {Acceptance, Artifact, Audit, AuditDecision, AuditDisclosurePolicy, ContentRejection, Digest, Id, InputObservation,
  LeaderAuthorization, LeaderRequest, LeaderReview, LeaderView, Progress, Prompt, Rates, RepairAudit, Revision, Usage, Worker, WorkerAudit};
const object = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value);
const bytes = (text: string) => new TextEncoder().encode(text).length;
const wellFormed = (text: string) => !/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(text);
const keys = new Set(['$ref', 'type', 'const', 'enum', 'required', 'properties', 'additionalProperties', 'items', 'minItems', 'maxItems',
  'minimum', 'maximum', 'minLength', 'maxLength', 'pattern', 'format', 'anyOf', 'oneOf', 'description', 'examples', 'x-maxUtf8Bytes', 'x-wellFormedUnicode']);

// 与既有 task-api/contract.mjs 相同的有界合同子集，浏览器用 TextEncoder 而非 Buffer。
// 直接消费权威 Schema，未知关键字拒绝；不维护第二份审计/制品格式。
export function matchesContract(value: unknown, schema: Schema | string, depth = 0): boolean {
  const s = typeof schema === 'string' ? schemas[schema] : schema;
  if (!s || depth > 40 || Object.keys(s).some(key => !keys.has(key))) return false;
  if (s.$ref && (!s.$ref.startsWith('#/components/schemas/') || !matchesContract(value, s.$ref.slice('#/components/schemas/'.length), depth + 1))) return false;
  if (s.anyOf && !s.anyOf.some(child => matchesContract(value, child, depth + 1))) return false;
  if (s.oneOf && s.oneOf.filter(child => matchesContract(value, child, depth + 1)).length !== 1) return false;
  const type = value === null ? 'null' : Array.isArray(value) ? 'array' : typeof value;
  if (s.type && !(Array.isArray(s.type) ? s.type : [s.type]).some(t => t === type || t === 'integer' && Number.isSafeInteger(value))) return false;
  if (Object.hasOwn(s, 'const') && s.const !== value || s.enum && !s.enum.includes(value)) return false;
  if (typeof value === 'number' && (!Number.isFinite(value) || s.minimum !== undefined && value < s.minimum || s.maximum !== undefined && value > s.maximum)) return false;
  if (typeof value === 'string') {
    const length = [...value].length;
    if (s.minLength !== undefined && length < s.minLength || s.maxLength !== undefined && length > s.maxLength ||
      s.pattern && !new RegExp(s.pattern, 'u').test(value) || s['x-maxUtf8Bytes'] !== undefined && bytes(value) > s['x-maxUtf8Bytes'] ||
      s['x-wellFormedUnicode'] && !wellFormed(value)) return false;
    if (s.format === 'date-time' && (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value) || !Number.isFinite(Date.parse(value)))) return false;
  }
  if (Array.isArray(value) && (s.minItems !== undefined && value.length < s.minItems || s.maxItems !== undefined && value.length > s.maxItems ||
    s.items && !value.every(child => matchesContract(child, s.items!, depth + 1)))) return false;
  if (object(value)) {
    if (s.required?.some(key => !Object.hasOwn(value, key))) return false;
    for (const [key, child] of Object.entries(value)) {
      if (s.additionalProperties === false && !Object.hasOwn(s.properties ?? {}, key)) return false;
      if (s.properties?.[key] && !matchesContract(child, s.properties[key]!, depth + 1)) return false;
    }
  }
  return true;
}

interface Prompt {workerId: string; text: string; source: string; contextRefs: string[]; observation?: {
  stage: string; coverage: string; promptDigest: string | null; promptBytes: number | null; preparedAt: string | null;
  handedOffAt: string | null; policy: unknown; snapshot: ArtifactRecord | null; previewTruncated: boolean;
}}
interface Audit {taskId: string; workers: {id: string; taskId: string}[]; prompts: Prompt[]}
const failure = () => new ApiError(502, 'artifact_reference_mismatch', '成果关联未通过当前 Task 合同校验，已拒绝读取', null);

export function observedInputIds(value: unknown, taskId: string): string[] | null {
  if (value === null || value === undefined) return null;
  if (!matchesContract(value, 'Audit')) throw failure();
  const audit = value as Audit;
  if (audit.taskId !== taskId || new Set(audit.workers.map(w => w.id)).size !== audit.workers.length ||
    audit.workers.some(w => w.taskId !== taskId) || new Set(audit.prompts.map(p => p.workerId)).size !== audit.prompts.length) throw failure();
  for (const p of audit.prompts) {
    if (!audit.workers.some(w => w.id === p.workerId) || new Set(p.contextRefs).size !== p.contextRefs.length) throw failure();
    const o = p.observation;
    if (!o) continue; // 原合同允许无 observation 的历史 producer。
    if (o.stage === 'unavailable') {
      if (!(p.source === 'unavailable' && p.text === '' && p.contextRefs.length === 0 && o.coverage === 'unavailable' && o.promptDigest === null &&
        o.promptBytes === null && o.preparedAt === null && o.handedOffAt === null && o.policy === null && o.snapshot === null && !o.previewTruncated)) throw failure();
    } else {
      if (o.promptDigest === null || o.promptBytes === null || o.preparedAt === null || (o.stage === 'prepared' ? o.handedOffAt !== null : o.handedOffAt === null)) throw failure();
      if (o.coverage !== 'policy-redacted') {
        if (!(p.source === 'unavailable' && p.text === '' && o.snapshot === null && !o.previewTruncated && (o.coverage !== 'metadata-only' || o.policy === null))) throw failure();
      } else {
        const s = o.snapshot;
        if (!(o.policy !== null && p.source === o.stage + '-redacted' && s !== null && s.taskId === taskId && s.name === p.workerId + '.input.txt' &&
          s.kind === 'evidence' && s.status === 'ready' && s.mediaType === 'text/plain' && s.bytes <= 262144 && bytes(p.text) <= 2048 &&
          (o.previewTruncated ? bytes(p.text) < s.bytes : bytes(p.text) === s.bytes))) throw failure();
      }
    }
  }
  return [...new Set(audit.prompts.flatMap(p => p.contextRefs))];
}

export function checkArtifact(value: ArtifactRecord, id: string, taskId: string | null, input = false): ArtifactRecord {
  if (!matchesContract(value, 'Artifact') || value.id !== id || value.taskId !== taskId || input && value.kind !== 'input') throw failure();
  return value;
}
