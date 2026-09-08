import {checkedGraph} from './graph.mjs';

export class TaskError extends Error {
  constructor(code, status = 409) { super(code); this.code = code; this.status = status; }
}
export const reject = (code, status) => { throw new TaskError(code, status); };
export const terminal = new Set(['completed', 'failed', 'cancelled', 'intervention']);
export const clone = value => structuredClone(value);
export const isText = (value, maximum = 8192) => typeof value === 'string' && value.trim().length > 0 &&
  value.isWellFormed() && !value.includes('\0') && Buffer.byteLength(value) <= maximum;
const positive = (value, max) => Number.isSafeInteger(value) && value >= 1 && value <= max;
export function limits(value) {
  if (!value || !positive(value.timeoutMs, 604800000) || !positive(value.maxAttempts, 100) ||
      !positive(value.maxWorkers, 64)) reject('invalid_limits', 400);
  return {timeoutMs: value.timeoutMs, maxAttempts: value.maxAttempts, maxWorkers: value.maxWorkers};
}
export function nextRevision(revision) {
  if (!positive(revision, Number.MAX_SAFE_INTEGER - 1)) reject('revision_exhausted', 503);
  return revision + 1;
}
export function actions(task) {
  if (terminal.has(task.status) || task.status === 'cancelling') return [];
  if (task.status === 'paused') return ['resume', 'cancel'];
  if (task.status === 'awaiting-approval') return ['approve', 'cancel'];
  if (task.status === 'awaiting-answer') return ['answer', 'cancel', 'pause'];
  return ['queued', 'running'].includes(task.status) ? ['pause', 'cancel'] : ['cancel'];
}
export function publicTask(record) {
  const task = clone(record.task);
  task.allowedActions = actions(task);
  return task;
}

// A planner proposes business work, never executable argv, authority, success or
// a new budget. Core bounds the DAG and exact approval binds the resulting plan.
export function freezePlan(record, proposal, hash) {
  if (!proposal || !isText(proposal.summary) || !Array.isArray(proposal.nodes) || !Array.isArray(proposal.edges)) reject('invalid_plan', 400);
  try { checkedGraph(proposal.nodes, proposal.edges); } catch { reject('invalid_plan_graph', 400); }
  const approvedLimits = limits(record.limits), budget = limits(proposal.budget ?? approvedLimits);
  if (Object.keys(budget).some(key => budget[key] > approvedLimits[key]) || budget.maxAttempts < proposal.nodes.length) reject('plan_budget_exceeded', 400);
  const roles = new Set(['planner', 'author', 'reviewer', 'integrator', 'verifier']);
  const nodes = proposal.nodes.map(node => {
    if (!roles.has(node.role) || !isText(node.goal) || !Array.isArray(node.scope) || node.scope.length > 32 ||
        node.scope.some(value => !isText(value, 4096)) || node.providerId !== null &&
        (typeof node.providerId !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(node.providerId))) reject('invalid_plan_node', 400);
    return {id: node.id, role: node.role, goal: node.goal, scope: [...node.scope], providerId: node.providerId};
  });
  const list = (key, required) => {
    const value = proposal[key] ?? [];
    if (!Array.isArray(value) || value.length > 32 || required && value.length === 0 ||
        value.some(item => !isText(item, 4096))) reject('invalid_plan_' + key, 400);
    return [...value];
  };
  const plan = {taskId: record.task.id, revision: record.plan ? nextRevision(record.plan.revision) : 1,
    summary: proposal.summary, nodes, edges: proposal.edges.map(({from, to}) => ({from, to})), budget,
    deliverables: list('deliverables', true), acceptance: list('acceptance', true), assumptions: list('assumptions', false)};
  return {...plan, digest: hash(plan)};
}
