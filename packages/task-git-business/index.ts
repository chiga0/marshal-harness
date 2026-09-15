import fs from 'node:fs';
import path from 'node:path';
import {createFileBusiness} from '../task-business/index.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
import {allocateWorktree, privateDirectory, gitPath, check, GitBusinessError, MAX_BYTES} from './git.mjs';

export {GitBusinessError};
export const PATCH = 'changes.patch', CONTEXT = 'git-context.json';
const equal = (a, b) => digest(encode(a)) === digest(encode(b));
const keys = (v, names) => v && typeof v === 'object' && !Array.isArray(v) && Object.keys(v).sort().join(',') === [...names].sort().join(',');
const id = value => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);

/** Business data in the existing public Context.text, not resource registration
 * or an HTTP-controlled local path. Composition resolves repositoryId separately. */
export function gitDescription(text) {
  check(typeof text === 'string' && Buffer.byteLength(text) <= 32768, 'git_business_context');
  let value; try {value = parseJson(Buffer.from(text));} catch {throw new GitBusinessError('git_business_context');}
  check(keys(value, ['profile', 'nodes']) && value.profile === 'task-git-input/v1' && Array.isArray(value.nodes) &&
    value.nodes.length > 0 && value.nodes.length <= 32, 'git_business_context');
  const seen = new Set();
  for (const node of value.nodes) {
    check(keys(node, ['nodeId', 'repositoryId', 'base', 'writePaths']) && id(node.nodeId) && id(node.repositoryId) &&
      !seen.has(node.nodeId) && /^[a-f0-9]{40}$/.test(node.base) && Array.isArray(node.writePaths) && node.writePaths.length > 0 &&
      node.writePaths.length <= 32 && new Set(node.writePaths).size === node.writePaths.length, 'git_business_context');
    node.writePaths.forEach(gitPath); seen.add(node.nodeId);
  }
  return JSON.parse(encode(value));
}

/** A real Git allocation/collection business plugin. Original FileBusiness is
 * used for ordinary planner/verifier directories and for immutable patch-file
 * staging, NOT as the Git worktree. No Task, budget, cleanup or Decision minting. */
export function createGitBusiness({parent, depot, layoutFor, approvedLayout, observeExecution, repositoryFor,
  authorize, gitExecutable = '/usr/bin/git', clock = Date.now} = {}) {
  check(typeof repositoryFor === 'function' && typeof layoutFor === 'function' && typeof observeExecution === 'function' &&
    path.isAbsolute(parent ?? '') && path.isAbsolute(gitExecutable), 'git_business_configuration');
  const worktrees = privateDirectory(path.join(parent, 'git-worktrees'));
  const indexes = privateDirectory(path.join(parent, 'git-indexes'));
  const files = createFileBusiness({parent, depot, layoutFor, approvedLayout, observeExecution, authorize, clock});
  const entries = new Map(); let closed = false;
  function active(ticket, context) {
    check(!closed && context?.signal instanceof AbortSignal && !context.signal.aborted && context.deadline === ticket.deadline &&
      clock() < ticket.deadline, 'git_business_stopped');
  }
  function release(ticket) {
    const entry = entries.get(ticket?.workerId);
    if (entry) {
      check(entry.reservationDigest === ticket.reservationDigest, 'git_business_binding');
      entry.worktree.close(); entries.delete(ticket.workerId);
    }
    // FD closure only, even when Supervisor calls release after UNKNOWN. Every
    // worktree, Git lock, private index and failure directory remains untouched.
    files.release(ticket);
  }
  async function prepare(ticket, context) {
    active(ticket, context);
    const prepared = await files.prepare(ticket, context); // Validates exact ticket + approved layout.
    if (ticket.role === 'planner' && ticket.planDigest === null || ticket.executionType === 'verification') return prepared;
    let worktree;
    try {
      const description = gitDescription(ticket.input.task.context?.text);
      const request = description.nodes.find(node => node.nodeId === ticket.nodeId);
      check(request && ticket.role === 'author' && !entries.has(ticket.workerId), 'git_business_node');
      const layout = ticket.input.fileLayout;
      check(layout && layout.inputs.length === 0 && equal([...layout.allowedPaths].sort(), [PATCH, CONTEXT].sort()), 'git_business_layout');
      const repository = await repositoryFor(ticket, JSON.parse(encode(request)));
      active(ticket, context); check(typeof repository === 'string' && path.isAbsolute(repository), 'git_business_repository');
      worktree = await allocateWorktree({gitExecutable, repository, base: request.base, parent: worktrees, indexParent: indexes,
        workerId: ticket.workerId, writePaths: request.writePaths, deadline: ticket.deadline, signal: context.signal});
      active(ticket, context);
      const entry = {request, worktree, stage: prepared.cwd, reservationDigest: ticket.reservationDigest}; entries.set(ticket.workerId, entry);
      const prompt = '在当前真实、独立、detached 且锁定的 Git worktree 完成本节点原批准目标。只修改 git.writePaths 中已有文件；' +
        '不得创建/删除/改权限或改其它文件，不修改 .git、HEAD、index 或 refs，不 commit、push、merge 或发布。' +
        '服务将在原执行清理后自行采集 patch；你不写 patch、控制回执或验收结果。保留无关内容。\n' +
        JSON.stringify({task: ticket.input.task, plan: ticket.input.plan, node: ticket.input.node, git: request});
      check(Buffer.byteLength(prompt) <= 256 * 1024, 'git_business_prompt_limit');
      return {cwd: worktree.cwd, prompt, onPermission: prepared.onPermission};
    } catch (error) {
      worktree?.close(); files.release(ticket); entries.delete(ticket.workerId);
      if (error instanceof GitBusinessError) throw error;
      throw new GitBusinessError('git_business_prepare_failed');
    }
  }
  async function collect(ticket, result, context) {
    const entry = entries.get(ticket.workerId);
    if (!entry) return files.collect(ticket, result, context);
    try {
      active(ticket, context); check(entry.reservationDigest === ticket.reservationDigest && result?.providerId === ticket.providerId &&
        result.status === 'completed' && result.stopReason === 'end_turn' && result.cleanup?.cleaned === true &&
        result.cleanup.started, 'git_business_cleanup_required');
      const original = await observeExecution(ticket, context); active(ticket, context);
      check(original?.executionId === result.cleanup.started.executionId && original.startedAt === result.cleanup.started.startedAt,
        'git_business_execution_mismatch');
      const collected = await entry.worktree.collectPatch(context); active(ticket, context);
      const binding = {profile: 'task-git-context/v1', ...entry.request, taskId: ticket.taskId, workerId: ticket.workerId,
        worktreeId: ticket.workerId, planDigest: ticket.planDigest, reservationDigest: ticket.reservationDigest,
        inputDigest: ticket.inputDigest, contextDigest: digest(encode(entry.request)), baseTree: collected.baseTree,
        patchDigest: digest(collected.patch), patchBytes: collected.patch.length, files: collected.files};
      const content = encode(binding);
      check(content.length + collected.patch.length <= MAX_BYTES, 'git_business_limit');
      fs.writeFileSync(path.join(entry.stage, PATCH), collected.patch, {flag: 'wx', mode: 0o600});
      fs.writeFileSync(path.join(entry.stage, CONTEXT), content, {flag: 'wx', mode: 0o600});
      // Original current-ledger layout/identity recheck, Depot durability and
      // file manifest construction; upstream/Decision semantics stay unchanged.
      return await files.collect(ticket, result, context);
    } catch (error) {
      if (error instanceof GitBusinessError) throw error;
      throw new GitBusinessError('git_business_collect_failed');
    } finally {release(ticket);}
  }
  return Object.freeze({prepare, collect, release, close() {
    if (closed) return; closed = true;
    for (const entry of entries.values()) entry.worktree.close(); entries.clear(); files.close();
  }});
}
