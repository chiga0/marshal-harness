import path from 'node:path';
import {constants, openSync, closeSync, fstatSync, readSync, realpathSync} from 'node:fs';
import {randomUUID} from 'node:crypto';
import {launchCommand} from '../agent-runtime/index.mjs';
import {encode, digest} from '../task-store/store.mjs';

const PROFILE = 'task-verification-command/v1';
const MAX_FRAME = 256 * 1024, MAX_DELIVERY = 8 * 1024 * 1024;
const hash = value => typeof value === 'string' && /^sha256:[a-f0-9]{64}$/u.test(value);
const text = value => typeof value === 'string' && value.isWellFormed() && !value.includes('\0');
const object = value => value !== null && Object.getPrototypeOf(value) === Object.prototype;
const keys = (value, names) => object(value) && Object.keys(value).sort().join(',') === [...names].sort().join(',');
const fail = code => { throw new Error(code); };
function freeze(value) {
  if (value && typeof value === 'object') { for (const item of Object.values(value)) freeze(item); Object.freeze(value); }
  return value;
}
function deferred() { let resolve; const promise = new Promise(r => { resolve = r; }); return {promise, resolve}; }
function synchronous(fn, ...args) {
  const result = fn(...args);
  if (result && typeof result.then === 'function') {
    // Do not allow an accidentally async callback to create an unobserved rejection.
    Promise.resolve(result).catch(() => {}); fail('verification_async_callback');
  }
  return result;
}
function snapshotChecker(filename, expected) {
  if (realpathSync(filename) !== filename) fail('verification_checker_identity');
  const fd = openSync(filename, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const before = fstatSync(fd);
    if (!before.isFile() || before.nlink !== 1 || before.size < 1 || before.size > 1024 * 1024 || (before.mode & 0o022)) fail('verification_checker_identity');
    const buffer = Buffer.alloc(before.size + 1);
    let length = 0, count;
    while (length < buffer.length && (count = readSync(fd, buffer, length, buffer.length - length, null)) > 0) length += count;
    const bytes = buffer.subarray(0, length), after = fstatSync(fd);
    const identity = stat => [stat.dev, stat.ino, stat.size, stat.mtimeMs, stat.ctimeMs, stat.mode].join(':');
    if (length !== before.size || identity(before) !== identity(after) || digest(bytes) !== expected) fail('verification_checker_identity');
    return identity(after);
  } finally { closeSync(fd); }
}
function artifact(value) {
  if (!keys(value, ['name', 'mediaType', 'content']) || !text(value.name) || !/^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$/u.test(value.name) ||
      !text(value.mediaType) || value.mediaType.length > 128 || !/^[a-z0-9][a-z0-9.+-]*\/[a-z0-9][a-z0-9.+-]*$/u.test(value.mediaType) ||
      !(value.content instanceof Uint8Array) || value.content.byteLength < 1 || value.content.byteLength > MAX_DELIVERY) fail('verification_delivery_invalid');
  return {name: value.name, mediaType: value.mediaType, content: Buffer.from(value.content)};
}
function frame(bytes) {
  if (!(bytes instanceof Uint8Array) || bytes.byteLength < 2 || bytes.byteLength > MAX_FRAME) fail('verification_frame_invalid');
  const raw = new TextDecoder('utf-8', {fatal: true}).decode(bytes);
  if (!raw.endsWith('\n') || raw.includes('\0')) fail('verification_frame_invalid');
  const parsed = JSON.parse(raw);
  // One canonical frame rejects duplicate keys, extra whitespace/frames and
  // malformed Unicode without introducing a second permissive JSON dialect.
  if (encode(parsed).toString() + '\n' !== raw || !keys(parsed, ['profile', 'nonce', 'binding', 'assertions'])) fail('verification_frame_invalid');
  return parsed;
}

/** Trusted composition only. Neither this adapter nor a checker signs Decisions. */
export function createVerificationCommand({executable, checkerPath, checkerDigest, policyDigest, assertions,
  env = {}, request = ({ticket}) => ({verification: ticket.input.verification, fileLayout: ticket.input.fileLayout ?? null}), delivery} = {}) {
  if (!text(executable) || !path.isAbsolute(executable) || !text(checkerPath) || !path.isAbsolute(checkerPath) ||
      path.normalize(checkerPath) !== checkerPath || !hash(checkerDigest) || !hash(policyDigest) ||
      !object(env) || Object.keys(env).length > 128 || Object.entries(env).some(([key, value]) => !/^[A-Za-z_][A-Za-z0-9_]*$/u.test(key) || !text(value)) ||
      !Array.isArray(assertions) || assertions.length < 1 || assertions.length > 64 ||
      assertions.some(item => !keys(item, ['name', 'validate']) || !/^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$/u.test(item.name) || typeof item.validate !== 'function') ||
      new Set(assertions.map(item => item.name)).size !== assertions.length || typeof request !== 'function' || typeof delivery !== 'function') fail('verification_config_invalid');
  // Copy the trusted policy so later caller mutation cannot replace validators/env.
  const validators = assertions.map(({name, validate}) => ({name, validate})), environment = {...env};

  function start({ticket, prepared, executionContext} = {}) {
    const started = deferred();
    let runtime, stopped = false, cleanup = null;
    const completion = (async () => {
      try {
        if (ticket?.executionType !== 'verification' || !hash(ticket.reservationDigest) || !hash(ticket.planDigest) || !hash(ticket.inputDigest) ||
            !Number.isSafeInteger(ticket.deadline) || ticket.deadline <= Date.now() ||
            ticket.input?.verification?.binding?.profile !== 'task-verification/v1' || ticket.input.verification.binding.policyDigest !== policyDigest ||
            !text(prepared?.cwd) || !path.isAbsolute(prepared.cwd)) fail('verification_input_invalid');
        const frozenTicket = freeze(JSON.parse(encode(ticket)));
        if (digest(encode(frozenTicket.input)) !== frozenTicket.inputDigest) fail('verification_input_invalid');
        const {reservationDigest: _reservation, ...reserved} = frozenTicket;
        if (digest(encode(reserved)) !== frozenTicket.reservationDigest) fail('verification_input_invalid');
        const cwd = realpathSync(prepared.cwd), relative = path.relative(cwd, checkerPath);
        if (relative === '' || (!relative.startsWith('..' + path.sep) && !path.isAbsolute(relative))) fail('verification_checker_in_candidate');
        const identity = snapshotChecker(checkerPath, checkerDigest);
        const context = Object.freeze({ticket: frozenTicket, prepared: Object.freeze({cwd})});
        const binding = freeze({reservationDigest: frozenTicket.reservationDigest, planDigest: frozenTicket.planDigest,
          inputDigest: frozenTicket.inputDigest, checkerDigest, policyDigest});
        const nonce = randomUUID();
        const input = encode({profile: PROFILE, nonce, binding, input: synchronous(request, context)});
        if (input.length + 1 > MAX_FRAME) fail('verification_input_limit');
        if (stopped || Date.now() >= frozenTicket.deadline) fail('verification_stopped');
        runtime = await launchCommand({executable, args: [checkerPath], cwd, env: environment, deadline: frozenTicket.deadline, executionContext,
          input: Buffer.concat([input, Buffer.from('\n')]), limits: {inputBytes: MAX_FRAME, outputBytes: MAX_FRAME, stderrBytes: 64 * 1024}});
        started.resolve(runtime.started);
        if (stopped) await runtime.stop();
        const result = await runtime.completion; cleanup = result.cleanup;
        if (stopped || Date.now() >= frozenTicket.deadline) fail('verification_stopped');
        if (!cleanup?.cleaned || cleanup.reason !== 'agent_exit' || cleanup.agentExit?.observed !== true ||
            cleanup.agentExit.code !== 0 || cleanup.agentExit.signal !== null || cleanup.started?.executionId !== runtime.started.executionId ||
            cleanup.executionId !== runtime.started.executionId || !result.outputComplete) fail('verification_execution_failed');
        if (snapshotChecker(checkerPath, checkerDigest) !== identity) fail('verification_checker_identity');
        const report = frame(result.stdout);
        if (report.profile !== PROFILE || report.nonce !== nonce || !encode(report.binding).equals(encode(binding)) ||
            !Array.isArray(report.assertions) || report.assertions.length !== validators.length ||
            report.assertions.some(item => !keys(item, ['name', 'actual'])) ||
            new Set(report.assertions.map(item => item.name)).size !== validators.length) fail('verification_report_mismatch');
        freeze(report);
        for (const {name, validate} of validators) {
          const assertion = report.assertions.find(item => item.name === name);
          if (!assertion || synchronous(validate, assertion.actual, context) !== true) fail('verification_assertion_failed');
        }
        const output = artifact(synchronous(delivery, {...context, report}));
        if (stopped || Date.now() >= frozenTicket.deadline) fail('verification_stopped');
        const evidence = encode({profile: PROFILE, binding, nonce, requestDigest: digest(input), reportDigest: digest(result.stdout),
          executionId: cleanup.executionId, started: cleanup.started, agentExit: cleanup.agentExit,
          assertions: report.assertions, delivery: {name: output.name, mediaType: output.mediaType, bytes: output.content.length, digest: digest(output.content)}});
        if (evidence.length > MAX_FRAME) fail('verification_evidence_limit');
        return {type: 'verification', status: 'passed', cleanup,
          evidence: {name: 'verification.json', mediaType: 'application/json', content: evidence}, delivery: output};
      } catch (error) {
        if (runtime) { const result = await runtime.stop(); cleanup = result.cleanup; }
        else if (error?.completion) cleanup = error.completion;
        return {type: 'verification', status: 'failed', reason: stopped ? 'verification_stopped' :
          (/^verification_[a-z_]+$/u.test(error?.message ?? '') ? error.message : 'verification_failed'), cleanup, evidence: null, delivery: null};
      } finally { started.resolve(null); }
    })();
    return Object.freeze({started: started.promise, completion, stop() {
      stopped = true;
      // launchCommand owns bootstrap. Once it yields the original handle, the
      // stop flag forces owned-group cleanup before this completion settles.
      if (runtime) void runtime.stop();
      return completion;
    }});
  }
  Object.defineProperty(start, 'custodyProfile', {value: Object.freeze({id: 'managed-checker-v1', scope: 'inherited-process-group', eligible: true})});
  return Object.freeze({start, custodyProfile: start.custodyProfile});
}
