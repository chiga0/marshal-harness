import fs from 'node:fs';
import path from 'node:path';
import http from 'node:http';
import {encode} from '../task-store/store.mjs';
import {PROFILE, INPUT_NAME, MAX_REPORT, HeldRoot, readHeld, observe, binding, json, hash, check, time, same, baseURL, closed, isDigest} from './io.mjs';

// Fixed implementation. No caller-selected code/argv, shell, credentials or redirection.
export async function run(request) {
  const common = ['operation', 'nonce', 'binding', 'root', 'rootIdentity', 'inputIdentity', 'deadline'];
  check(closed(request, request.operation === 'publish' ? common : [...common, 'readBaseURL', 'expected', 'interactionRefs', 'leaderReplyRefs']) &&
    ['publish', 'postverify'].includes(request.operation) && /^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$/u.test(request.nonce));
  if (request.operation === 'publish') binding(request.binding);
  else {
    check(closed(request.binding, ['publicationReceiptDigest', 'targetId', 'name', 'artifactDigest', 'bytes', 'policyDigest']) &&
      isDigest(request.binding.publicationReceiptDigest) && isDigest(request.binding.policyDigest));
    const {publicationReceiptDigest, policyDigest: _policy, ...effect} = request.binding;
    binding({...effect, actionId: 'postverify', authorizationDigest: publicationReceiptDigest});
  }
  const root = new HeldRoot(request.root, request.rootIdentity); let held;
  try {
    time(request.deadline);
    held = readHeld(path.join(process.cwd(), INPUT_NAME), {deadline: request.deadline, expected: request.inputIdentity, oneLink: true});
    check(held.bytes.length === request.binding.bytes && hash(held.bytes) === request.binding.artifactDigest, 'publication_input_changed');
    json(held.bytes);
    if (request.operation === 'postverify') {
      const url = new URL(request.binding.name, baseURL(request.readBaseURL));
      check(url.origin === new URL(request.readBaseURL).origin);
      const bytes = await get(url, request.deadline);
      check(bytes.length === request.binding.bytes && hash(bytes) === request.binding.artifactDigest, 'publication_http_content_mismatch');
      const actual = json(bytes);
      check(encode(actual).equals(encode(request.expected)), 'publication_expectation_failed');
      held.recheck(); root.check();
      return {profile: PROFILE, nonce: request.nonce, binding: request.binding, status: 'passed', observed: {
        bytes: bytes.length, artifactDigest: hash(bytes), origin: url.origin, expectedDigest: hash(encode(request.expected)),
        interactionRefsDigest: hash(encode(request.interactionRefs)), leaderReplyRefsDigest: hash(encode(request.leaderReplyRefs)), checks: 4}};
    }
    check(request.operation === 'publish');
    const prior = observe(root, request.binding, request.deadline);
    if (prior.status !== 'absent') return {profile: PROFILE, nonce: request.nonce, binding: request.binding,
      status: prior.status === 'matched' ? 'matched' : 'failed', observed: prior};
    const temporary = path.join(root.root, '.pending-' + request.nonce), final = path.join(root.root, request.binding.name);
    let fd, created = false;
    try {
      fd = fs.openSync(temporary, fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_NOFOLLOW, 0o600);
      const original = fs.fstatSync(fd); let offset = 0;
      while (offset < held.bytes.length) {time(request.deadline); root.check(); offset += fs.writeSync(fd, held.bytes, offset, Math.min(65536, held.bytes.length - offset));}
      fs.fsyncSync(fd); held.recheck(); root.check(); time(request.deadline);
      check(same(original, fs.lstatSync(temporary)), 'publication_temp_changed');
      try {fs.linkSync(temporary, final); created = true;}
      catch (error) {if (error.code !== 'EEXIST') throw error;}
      root.sync();
      const observed = observe(root, request.binding, request.deadline);
      check(observed.status !== 'absent', 'publication_effect_unknown');
      // Only our O_EXCL inode, after final durability. Never remove a final file
      // or enumerate/delete leftovers. Failure leaves both target and temp intact.
      if (created && observed.status === 'matched') {
        const named = fs.lstatSync(temporary), target = fs.lstatSync(final);
        check(same(original, fs.fstatSync(fd)) && same(original, named) && same(original, target), 'publication_temp_changed');
        fs.unlinkSync(temporary); root.sync();
      }
      return {profile: PROFILE, nonce: request.nonce, binding: request.binding,
        status: observed.status !== 'matched' ? 'failed' : created ? 'created' : 'matched', observed};
    } finally {if (fd !== undefined) fs.closeSync(fd);}
  } finally {held?.close(); root.close();}
}
function get(url, deadline) {
  return new Promise((resolve, reject) => {
    time(deadline); let size = 0, chunks = [], done = false;
    const timer = setTimeout(() => fail(new Error('publication_stopped')), Math.max(1, deadline - Date.now()));
    const finish = (error, value) => {if (done) return; done = true; clearTimeout(timer); error ? reject(error) : resolve(value);};
    const request = http.get(url, {agent: false, headers: {accept: 'application/json', 'accept-encoding': 'identity', connection: 'close'}}, response => {
      if (response.statusCode !== 200 || response.headers['content-encoding'] && response.headers['content-encoding'] !== 'identity' ||
          !/^application\/json(?:\s*;\s*charset=utf-8)?$/iu.test(response.headers['content-type'] ?? '') ||
          response.headers['content-length'] && (!/^\d+$/u.test(response.headers['content-length']) || Number(response.headers['content-length']) > MAX_REPORT)) {
        response.destroy(); fail(new Error('publication_http_rejected')); return;
      }
      response.on('data', chunk => {size += chunk.length; if (size > MAX_REPORT) {response.destroy(); fail(new Error('publication_report_limit'));} else chunks.push(chunk);});
      response.once('error', () => fail(new Error('publication_http_unavailable')));
      response.once('end', () => finish(null, Buffer.concat(chunks)));
    });
    function fail(error) {request.destroy(); finish(error);}
    request.once('error', () => finish(new Error('publication_http_unavailable')));
  });
}
