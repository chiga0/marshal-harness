import {supportsNode} from '../task-store/runtime.mjs';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {createClarificationPort, createVerificationPort} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
import {PROFILE, TEMPLATE, policy, proposal, initialValues, finalValues, range, date, sourceBytes, sourceRef, expected, equal, check, fields, regions} from './policy.mjs';

const here = name => fileURLToPath(new URL(name, import.meta.url));
const checkerPath = here('./checker.mjs');
const deny = () => ({outcome: {outcome: 'cancelled'}});
/** One-time offered file permissions only; not an OS/hostile same-UID sandbox. */
export function filePermission(ticket, cwd, request) {
  if (ticket?.role !== 'author' || !regions.includes(ticket.nodeId) || typeof cwd !== 'string' || !path.isAbsolute(cwd) || !Array.isArray(request?.options)) return deny();
  const {kind, rawInput: input} = request.toolCall ?? {}; if (!input) return deny();
  let filename;
  if (kind === 'read' && Object.hasOwn(input, 'file_path') &&
    Object.keys(input).every(key => ['file_path', 'offset', 'limit'].includes(key)) &&
    ['offset', 'limit'].every(key => input[key] === undefined || Number.isSafeInteger(input[key]) && input[key] >= 0)) {
    filename = ['sales.json', ticket.nodeId + '.json'].find(name => input.file_path === name || input.file_path === path.join(cwd, name));
  } else if (kind === 'edit' && fields(input, ['file_path', 'content']) && typeof input.content === 'string' && Buffer.byteLength(input.content) <= 4096 && !input.content.includes('\0')) filename = ticket.nodeId + '.json';
  else if (kind === 'edit' && input !== null && typeof input === 'object' && Object.keys(input).every(key => ['file_path', 'old_string', 'new_string', 'replace_all'].includes(key)) &&
    ['old_string', 'new_string'].every(key => typeof input[key] === 'string' && !input[key].includes('\0') && Buffer.byteLength(input[key]) <= 4096) &&
    (input.replace_all === undefined || typeof input.replace_all === 'boolean')) filename = ticket.nodeId + '.json';
  if (!filename || input.file_path !== filename && input.file_path !== path.join(cwd, filename)) return deny();
  try {const target = path.join(cwd, filename), stat = fs.lstatSync(target);
    if (!stat.isFile() || stat.nlink !== 1 || fs.realpathSync(target) !== target) return deny();
  } catch (error) {if (kind === 'read' || error.code !== 'ENOENT') return deny();}
  const choices = request.options.filter(option => option.kind === 'allow_once' && option.optionId === 'proceed_once');
  return choices.length === 1 ? {outcome: {outcome: 'selected', optionId: 'proceed_once'}} : deny();
}

/** Deployable single-business configuration. Only this DI boundary chooses an
 * Agent; no fixture/default provider, caller-selected executable or SQL reducer. */
export function createRegionalWindowConfig({provider, executable = process.execPath, onExecution = () => {}, onPermission = () => {}}) {
  check(provider?.id && typeof provider.start === 'function' && path.isAbsolute(executable));
  const code = digest(encode(['policy.mjs', 'index.mjs', 'checker.mjs'].map(name => ({name, digest: digest(fs.readFileSync(here('./' + name)))}))));
  const identity = id => ({id, version: '1', digest: code});
  const trustedPolicy = {...policy, description: policy.description + ' 受信配置/规则/checker源码摘要：' + code};
  const clarification = createClarificationPort({template: identity(TEMPLATE), applies(input) {initialValues(input); return true;},
    slots: ['startDate', 'endDate'].map(id => ({id, prompt: id === 'startDate' ? '请给出汇总起始日（YYYY-MM-DD，UTC，含当日）。' : '请给出汇总结束日（YYYY-MM-DD，UTC，含当日；跨度最多366日）。',
      validator: identity('utc-' + id), read: input => initialValues(input)[id], validate: date})),
    renderer: {...identity('fixed-window-plan'), render({values}) {range(values, false); return proposal();}}});
  const bindPlan = ({taskInput, inputArtifacts, proposal: plan}) => {
    // Check original input without trying to fill missing dates during preview.
    check(taskInput.intent === '按日期区间汇总东、西两个地区的已付款流水' && inputArtifacts.length === 1);
    const original = proposal();
    check(equal(plan.nodes, original.nodes) && equal(plan.edges, original.edges) && equal(plan.deliverables, original.deliverables) &&
      equal(plan.assumptions, original.assumptions) && equal(plan.acceptance, original.acceptance), 'window_plan_boundary');
    return {nodeId: 'verify', description: trustedPolicy.description,
      layouts: regions.map(nodeId => ({nodeId, inputs: [{path: 'sales.json', source: {kind: 'input', id: inputArtifacts[0].id}}], allowedPaths: [nodeId + '.json']}))
        .concat({nodeId: 'verify', allowedPaths: [], inputs: regions.map(nodeId => ({path: nodeId + '.json', source: {kind: 'upstream', nodeId, path: nodeId + '.json'}}))}),
      deliveries: regions.map(nodeId => ({nodeId, path: nodeId + '.json', targetPath: nodeId + '.json'}))};
  };
  let activeDepot = null;
  const data = ticket => {check(activeDepot, 'window_business_unavailable'); return sourceBytes(activeDepot, ticket);};
  const command = createVerificationCommand({executable, checkerPath, checkerDigest: digest(fs.readFileSync(checkerPath)),
    policyDigest: digest(encode(trustedPolicy)),
    request: ({ticket}) => ({...finalValues(ticket.input.task), sourceDigest: sourceRef(ticket).digest, sourceBase64: data(ticket).toString('base64')}),
    assertions: [
      {name: 'input-window', validate: (actual, {ticket}) => equal(actual, {...finalValues(ticket.input.task), sourceDigest: sourceRef(ticket).digest})},
      {name: 'regions', validate: (actual, {ticket}) => equal(actual, expected(data(ticket), finalValues(ticket.input.task)))},
    ],
    delivery: ({ticket, report}) => {
      const actual = report.assertions.find(item => item.name === 'regions').actual;
      const files = regions.map((region, index) => {
        const manifest = ticket.input.verification.manifests.find(item => item.nodeId === region)?.manifest;
        check(manifest?.files.length === 1 && manifest.files[0].path === region + '.json', 'window_delivery_source');
        const ref = manifest.files[0], bytes = activeDepot.get({digest: ref.digest, bytes: ref.bytes});
        check(bytes.length === ref.bytes && digest(bytes) === ref.digest && equal(parseJson(bytes), actual[index]), 'window_delivery_source');
        return {path: ref.path, content: bytes.toString('utf8')}; // Preserve complete original file bytes, not only totals.
      });
      return {name: 'regional-paid-window.json', mediaType: 'application/json', content: encode({profile: PROFILE,
        window: finalValues(ticket.input.task), sourceDigest: sourceRef(ticket).digest, files})};
    }});
  const verification = createVerificationPort({id: 'trusted-window-checker', policy: trustedPolicy, bindPlan, start: command.start});
  return {providers: new Map([[provider.id, provider]]), clarification, verification,
    applicationOptions: {defaultLimits: {timeoutMs: 600000, maxAttempts: 4, maxWorkers: 2}},
    businessFactory(ports) {
      check(activeDepot === null, 'window_config_already_active'); activeDepot = ports.depot;
      const byCwd = new Map();
      const business = createFileBusiness({parent: ports.executionParent, depot: ports.depot, approvedLayout: ports.approvedLayout, observeExecution: ports.observeExecution,
        layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout,
        authorize: (ticket, request) => {const result = filePermission(ticket, byCwd.get(ticket.workerId), request); onPermission(result.outcome.outcome === 'selected'); return result;}});
      return {...business, async prepare(ticket, context) {
        if (ticket.role !== 'planner') {finalValues(ticket.input.task); sourceBytes(ports.depot, ticket);}
        const result = await business.prepare(ticket, context);
        if (ticket.role === 'planner') {
          // The exact finite policy accepted by bindPlan must be visible to the
          // actual Planner, not a private answer known only by the test peer.
          result.prompt = '本业务仅支持下列完整固定提案。请核对原输入后原样返回此 JSON 对象，不改写 goal/scope 或增删字段；不得新增预算、权限、节点或验收规则。不要写文件或自行批准。\n' +
            'REGIONAL_WINDOW_FIXED_PROPOSAL_V1\n' + encode(proposal()).toString('utf8') + '\nREGIONAL_WINDOW_FIXED_PROPOSAL_END\n' + result.prompt;
          check(Buffer.byteLength(result.prompt) <= 256 * 1024, 'window_prompt_limit');
        }
        byCwd.set(ticket.workerId, result.cwd); onExecution(ticket, result.cwd); return result;
      }, release(ticket) {byCwd.delete(ticket.workerId); business.release(ticket);}, close() {business.close(); activeDepot = null;}};
    }};
}

/** Explicit trusted Qwen deployment. Reading model credentials is Qwen's job. */
export function createQwenWindowConfig({node = process.execPath, qwenEntry}) {
  check(supportsNode() && fs.realpathSync(node) === fs.realpathSync(process.execPath) && path.isAbsolute(qwenEntry) &&
    fs.realpathSync(qwenEntry) === qwenEntry && path.basename(qwenEntry) === 'cli-entry.js', 'window_runtime_configuration');
  const pkg = JSON.parse(fs.readFileSync(path.join(path.dirname(qwenEntry), 'package.json')));
  check(pkg.name === '@qwen-code/qwen-code', 'window_qwen_identity');
  const env = {PATH: path.dirname(node) + ':/usr/bin:/bin:/usr/sbin:/sbin'};
  for (const name of ['HOME', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TMPDIR']) if (typeof process.env[name] === 'string') env[name] = process.env[name];
  check(path.isAbsolute(env.HOME ?? ''), 'window_native_home_missing');
  return createRegionalWindowConfig({executable: node,
    provider: createAcpProvider({id: 'qwen-acp', executable: node, args: [qwenEntry, '--acp'], env})});
}
