import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {randomUUID} from 'node:crypto';
import {launchCommand} from '../agent-runtime/index.mjs';
import {encode} from '../task-store/store.mjs';
import {PROFILE, INPUT_NAME, HeldRoot, readHeld, observe, binding, nameFor, json, hash, isDigest, id, text, closed, check, time,
  canonicalPath, baseURL, evidence} from './io.mjs';

export {nameFor, INPUT_NAME, PROFILE};
const RUNNER = fileURLToPath(new URL('./runner.mjs', import.meta.url));
const CUSTODY = Object.freeze({id: 'managed-local-report-v1', scope: 'inherited-process-group', eligible: true});
const hashValue = value => hash(encode(value));
function deferred() {let resolve; const promise = new Promise(r => {resolve = r;}); return {promise, resolve};}
function disjoint(a, b) {return a !== b && !a.startsWith(b + path.sep) && !b.startsWith(a + path.sep);}
function frozen(value) {if (value && typeof value === 'object') {for (const child of Object.values(value)) frozen(child); Object.freeze(value);} return value;}
function originalTicket(value, type) {
  const ticket = frozen(JSON.parse(encode(value)));
  const {reservationDigest, ...body} = ticket;
  check(ticket.executionType === type && id(ticket.taskId) && id(ticket.workerId) && isDigest(ticket.planDigest) &&
    hashValue(body) === reservationDigest && hashValue(ticket.input) === ticket.inputDigest, 'publication_ticket_mismatch');
  time(ticket.deadline); return ticket;
}
function authorization(value, original, targetId, policyDigest) {
  const b = binding(value.binding), a = value.authorization;
  check(closed(value, ['binding', 'authorization']) && closed(a, ['taskId', 'planDigest', 'artifactId', 'artifactDigest', 'bytes',
    'acceptanceDigest', 'reviewDigest', 'targetId', 'targetPolicyDigest', 'name', 'operation', 'expiresAt']) &&
    a.taskId === original.taskId && a.planDigest === original.planDigest && id(a.artifactId) && isDigest(a.acceptanceDigest) &&
    isDigest(a.reviewDigest) && a.targetId === targetId && a.targetPolicyDigest === policyDigest &&
    a.name === nameFor(a) && a.operation === 'create-if-absent' && text(a.expiresAt, 32) &&
    Number.isFinite(Date.parse(a.expiresAt)) && original.deadline <= Date.parse(a.expiresAt) &&
    b.targetId === targetId && b.name === a.name && b.artifactDigest === a.artifactDigest && b.bytes === a.bytes &&
    b.authorizationDigest === hashValue(a), 'publication_authorization_mismatch');
  return b;
}
function postInput(value, ticket, targetId, policyDigest) {
  check(closed(value, ['publicationReceiptDigest', 'targetId', 'name', 'artifactDigest', 'bytes', 'policyDigest', 'expected', 'interactionRefs', 'leaderReplyRefs']) &&
    isDigest(value.publicationReceiptDigest) && value.targetId === targetId && value.policyDigest === policyDigest &&
    value.name === nameFor({taskId: ticket.taskId, artifactDigest: value.artifactDigest}) &&
    Number.isSafeInteger(value.bytes) && value.bytes > 0 && value.bytes <= 1024 * 1024 &&
    Array.isArray(value.interactionRefs) && value.interactionRefs.length <= 64 && Array.isArray(value.leaderReplyRefs) && value.leaderReplyRefs.length <= 64,
    'publication_postverify_mismatch');
  for (const key of ['interactionRefs', 'leaderReplyRefs']) if (ticket.input[key] !== undefined)
    check(encode(ticket.input[key]).equals(encode(value[key])), 'publication_answers_omitted');
  json(encode(value.expected));
  const {expected: _expected, interactionRefs: _interactions, leaderReplyRefs: _replies, ...b} = value;
  return b;
}
function successful(result, started) {
  const c = result?.cleanup;
  return c?.cleaned === true && c.reason === 'agent_exit' && c.agentExit?.observed === true && c.agentExit.code === 0 && c.agentExit.signal === null &&
    c.executionId === started.executionId && c.started?.executionId === started.executionId && c.started.startedAt === started.startedAt && result.outputComplete === true;
}
function response(result, nonce, b) {
  check(result.stdout.length > 1 && result.stdout.length <= 64 * 1024 && result.stdout.at(-1) === 10, 'publication_report_invalid');
  const bytes = result.stdout.subarray(0, -1), parsed = json(bytes);
  check(encode(parsed).equals(bytes) && closed(parsed, ['profile', 'nonce', 'binding', 'status', 'observed']) &&
    parsed.profile === PROFILE && parsed.nonce === nonce && encode(parsed.binding).equals(encode(b)), 'publication_report_mismatch');
  return parsed;
}

/** Trusted composition capability, NOT Task authorization, publication authority
 * or an independent Verification receipt. Core must bind current original facts. */
export function createLocalReportPublication({id: targetId, root, readBaseURL, policy} = {}) {
  check(id(targetId) && targetId.length <= 110 && closed(policy, ['profile', 'id', 'version']) && policy.profile === PROFILE && id(policy.id) && text(policy.version),
    'publication_invalid_configuration');
  const policyDigest = hashValue(policy), url = baseURL(readBaseURL), directory = new HeldRoot(root);
  const configuration = frozen({profile: PROFILE, id: targetId, root: directory.root, readBaseURL: url,
    policy: JSON.parse(encode(policy)), rootIdentity: directory.descriptor()});
  const configurationDigest = hashValue(configuration);
  let armed = false, active = 0, isClosed = false;
  const available = () => {check(!isClosed && armed, 'publication_not_configured'); directory.check();};
  function assertDisjoint(paths) {
    check(!isClosed && Array.isArray(paths) && paths.length >= 1 && paths.length <= 16, 'publication_invalid_configuration');
    for (const name of paths) check(disjoint(directory.root, canonicalPath(name)), 'publication_root_overlap');
    directory.check(); armed = true; return true;
  }
  function lookup(value, {signal, deadline} = {}) {
    const b = binding(value); check(b.targetId === targetId, 'publication_target_mismatch');
    let observed;
    try {available(); time(deadline, signal); observed = observe(directory, b, deadline, signal);}
    catch {observed = {status: 'unknown', actual: null};}
    return {status: observed.status, binding: b, evidence: evidence('publication-lookup.json', {profile: PROFILE, binding: b,
      ...observed, observedAt: new Date().toISOString(), createdByThisExecution: false})};
  }
  function managed(type, {ticket, prepared, executionContext} = {}) {
    const started = deferred(); let runtime, stopped = false, cleanup = null, original, b, input, invoked = false;
    active++;
    const completion = (async () => {
      try {
        available(); original = originalTicket(ticket, type);
        const cwd = canonicalPath(prepared?.cwd);
        check(fs.realpathSync(cwd) === cwd && disjoint(directory.root, cwd), 'publication_invalid_path');
        const stat = fs.lstatSync(cwd); check(stat.isDirectory() && stat.uid === process.getuid() && (stat.mode & 0o7777) === 0o700, 'publication_invalid_path');
        const publishing = type === 'publication';
        const fields = publishing ? original.input.publication : original.input.postverify;
        b = publishing ? authorization(fields, original, targetId, policyDigest) : postInput(fields, original, targetId, policyDigest);
        input = readHeld(path.join(cwd, INPUT_NAME), {deadline: original.deadline, oneLink: true});
        check(input.bytes.length === b.bytes && hash(input.bytes) === b.artifactDigest, 'publication_input_changed');
        const report = json(input.bytes);
        if (!publishing) check(encode(report).equals(encode(fields.expected)), 'publication_expectation_failed');
        const nonce = randomUUID(), request = {operation: publishing ? 'publish' : 'postverify', nonce, binding: b,
          root: directory.root, rootIdentity: directory.descriptor(), inputIdentity: input.descriptor, deadline: original.deadline,
          ...(publishing ? {} : {readBaseURL: url, expected: fields.expected, interactionRefs: fields.interactionRefs, leaderReplyRefs: fields.leaderReplyRefs})};
        const bytes = encode(request); check(bytes.length < 192 * 1024, 'publication_input_limit');
        time(original.deadline); check(!stopped, 'publication_stopped'); input.recheck(); directory.check();
        invoked = true;
        runtime = await launchCommand({executable: process.execPath, args: [RUNNER], cwd, env: {}, deadline: original.deadline, executionContext,
          input: Buffer.concat([bytes, Buffer.from('\n')]), limits: {inputBytes: 192 * 1024, outputBytes: 64 * 1024, stderrBytes: 4096}});
        started.resolve(runtime.started);
        if (stopped) await runtime.stop();
        const result = await runtime.completion; cleanup = result.cleanup;
        time(original.deadline); check(!stopped && successful(result, runtime.started), 'publication_execution_unknown');
        directory.check(); input.recheck(); const observed = response(result, nonce, b);
        if (publishing) {
          check(['created', 'matched', 'failed'].includes(observed.status) && closed(observed.observed, ['status', 'actual']), 'publication_report_invalid');
          const current = observe(directory, b, original.deadline);
          check(encode(current).equals(encode(observed.observed)) && (observed.status === 'failed' ? current.status === 'conflict' : current.status === 'matched'),
            'publication_effect_unknown');
          return {type, status: observed.status, cleanup, evidence: evidence('publication.json', {profile: PROFILE, binding: b,
            status: observed.status, observed: current, executionId: cleanup.executionId, reportDigest: hash(result.stdout)})};
        }
        const expectedObservation = {bytes: b.bytes, artifactDigest: b.artifactDigest, origin: new URL(url).origin,
          expectedDigest: hashValue(fields.expected), interactionRefsDigest: hashValue(fields.interactionRefs),
          leaderReplyRefsDigest: hashValue(fields.leaderReplyRefs), checks: 4};
        check(observed.status === 'passed' && encode(observed.observed).equals(encode(expectedObservation)), 'publication_postverify_mismatch');
        return {type: 'verification', status: 'passed', cleanup,
          evidence: evidence('publication-postverify.json', {profile: PROFILE, binding: b, observed: observed.observed,
            executionId: cleanup.executionId, reportDigest: hash(result.stdout)}),
          delivery: {name: b.name, mediaType: 'application/json', content: Buffer.from(input.bytes)}};
      } catch (error) {
        if (runtime) {const result = await runtime.stop(); cleanup = result.cleanup;}
        else if (error?.completion) cleanup = error.completion;
        const result = {type: type === 'postverify' ? 'verification' : 'publication',
          status: type === 'publication' && invoked ? 'unknown' : 'failed', cleanup,
          evidence: b ? evidence(type + '-unconfirmed.json', {profile: PROFILE, binding: b, effect: 'unconfirmed', stopped}) : null};
        if (type === 'postverify') result.delivery = null;
        return result;
      } finally {input?.close(); active--; started.resolve(null);}
    })();
    return Object.freeze({started: started.promise, completion, stop() {
      stopped = true;
      try {if (executionContext?.stop) void Promise.resolve(executionContext.stop()).catch(() => {});} catch {}
      if (runtime) void runtime.stop();
      return completion;
    }});
  }
  const start = args => managed('publication', args), postStart = args => managed('postverify', args);
  Object.defineProperty(start, 'custodyProfile', {value: CUSTODY}); Object.defineProperty(postStart, 'custodyProfile', {value: CUSTODY});
  return Object.freeze({id: targetId, policyDigest, configuration, configurationDigest, start, lookup, assertDisjoint, custodyProfile: CUSTODY,
    postverify: Object.freeze({id: targetId + '-postverify', start: postStart, custodyProfile: CUSTODY}),
    close() {check(active === 0, 'publication_busy'); if (!isClosed) {isClosed = true; directory.close();}}});
}
