'use strict';

// A canary transport, not a Run controller. Never infer or manufacture a verdict.
const fs = require('node:fs');
const path = require('node:path');
const {spawn, fork} = require('node:child_process');
const {setTimeout: pause} = require('node:timers/promises');
const {performance} = require('node:perf_hooks');
const REPO = 'chiga0/marshal-harness';
const DIGEST = /^sha256:[a-f0-9]{64}$/;
const fail = reason => { throw new Error(reason); };

function workerEnvironment(env) {
  const result = {};
  for (const key of ['HOME', 'TMPDIR', 'LANG', 'LC_ALL']) {
    if (typeof env[key] === 'string') result[key] = env[key];
  }
  result.PATH = `${env.NODE_ROOT_BIN}:/usr/bin:/bin:/usr/sbin:/sbin`;
  return result;
}

function readFile(file, limit) {
  const fd = fs.openSync(file, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW | fs.constants.O_NONBLOCK);
  try {
    const before = fs.fstatSync(fd);
    if (!before.isFile() || before.nlink !== 1 || before.size > limit) fail('carrier-file-type-or-size');
    const bytes = Buffer.alloc(before.size + 1);
    let used = 0, count;
    while (used < bytes.length && (count = fs.readSync(fd, bytes, used, bytes.length - used, null))) used += count;
    const after = fs.fstatSync(fd);
    if (used !== before.size || before.size !== after.size || before.mtimeMs !== after.mtimeMs || before.ctimeMs !== after.ctimeMs) fail('carrier-file-drift');
    return bytes.subarray(0, used);
  } finally { fs.closeSync(fd); }
}

function validateReady(value, runId) {
  if (!value || value.run?.runId !== runId || value.run.state !== 'REVIEW_PENDING' ||
      !DIGEST.test(value.packetDigest) || !/^[a-f0-9]{64}$/.test(value.binarySHA256) ||
      value.archive !== 'review-inputs.tar') fail('carrier-ready-mismatch');
  return value;
}

function reviewTargets(directory, scenario, runId, sourceHead) {
  if (scenario === 'order-quote') {
    return [{directory, suffix: '', ready: validateReady(JSON.parse(readFile(path.join(directory, 'review-ready.json'), 65536)), runId)}];
  }
  if (scenario !== 'order-quote-team') fail('carrier-environment');
  const subject = JSON.parse(readFile(path.join(directory, 'subject.json'), 65536));
  const nodes = ['service', 'client', 'integration'];
  if (subject.sourceHead !== sourceHead || !/^[a-f0-9]{64}$/.test(subject.binarySHA256) ||
      !DIGEST.test(subject.inputsDigest) || !subject.runs ||
      Object.keys(subject.runs).length !== 3 ||
      nodes.some(node => !/^team-run-[a-f0-9]{64}$/.test(subject.runs[node])) ||
      new Set(nodes.map(node => subject.runs[node])).size !== 3) fail('carrier-team-subject');
  return nodes.slice(0, 2).map(node => {
    const child = path.join(directory, node);
    const ready = validateReady(JSON.parse(readFile(path.join(child, 'review-ready.json'), 65536)), subject.runs[node]);
    if (ready.binarySHA256 !== subject.binarySHA256) fail('carrier-team-binary-drift');
    return {directory: child, suffix: `-${node}`, ready};
  });
}

function publishDecision(target, selected, workflowRun, sourceHead) {
  const stage = path.join(target.directory, 'external-decision.stage');
  const destination = path.join(target.directory, 'review-decision.json');
  fs.writeFileSync(stage, selected.raw, {flag: 'wx', mode: 0o600});
  if (fs.existsSync(destination)) fail('carrier-decision-already-present');
  fs.renameSync(stage, destination);
  fs.writeFileSync(path.join(target.directory, 'review-carrier.json'), JSON.stringify({artifact: target.artifact,
    commentId: selected.commentId, workflowRun, sourceHead, packetDigest: target.ready.packetDigest}) + '\n', {flag: 'wx', mode: 0o600});
}

function reviewMarker(workflowRun, sourceHead, digest) {
  if (!/^[1-9][0-9]*$/.test(workflowRun) || !/^[a-f0-9]{40}$/.test(sourceHead) || !DIGEST.test(digest)) fail('carrier-invalid-identity');
  return `<!-- marshal-fixed-server-review:v1:${workflowRun}:${sourceHead}:${digest} -->\n`;
}

function selectComment(comments, subject) {
  if (!Array.isArray(comments)) fail('carrier-comment-response');
  const matched = comments.filter(c => c?.user?.id === subject.ownerId && c.user.login === 'chiga0' &&
    typeof c.body === 'string' && c.body.startsWith(subject.marker));
  if (matched.length > 1) fail('carrier-conflicting-comments');
  if (!matched.length) return null;
  const comment = matched[0];
  if (!Number.isSafeInteger(comment.id) || comment.id <= 0 ||
      comment.created_at !== comment.updated_at || !Number.isFinite(Date.parse(comment.created_at)) ||
      Date.parse(comment.created_at) < subject.startedAt || Buffer.byteLength(comment.body) > 65536) fail('carrier-comment-provenance');
  // Preserve the original JSON. The driver rejects duplicate members and Core
  // owns canonical parsing, exact reviewer/evidence binding and authority.
  const raw = comment.body.slice(subject.marker.length);
  let decision;
  try { decision = JSON.parse(raw); } catch { fail('carrier-invalid-decision'); }
  if (!decision || decision.kind !== 'ReviewDecision' || decision.runId !== subject.runId ||
      decision.reviewPacketDigest !== subject.packetDigest) fail('carrier-decision-identity');
  return {raw, commentId: comment.id};
}

async function github(resource, token) {
  let response;
  try {
    response = await fetch(`https://api.github.com/repos/${REPO}${resource ? '/' + resource : ''}`, {
      redirect: 'error', signal: AbortSignal.timeout(15000),
      headers: {Authorization: `Bearer ${token}`, Accept: 'application/vnd.github+json',
        'X-GitHub-Api-Version': '2022-11-28', 'User-Agent': 'marshal-live-review'},
    });
    if (!response.ok) fail('carrier-github-unavailable');
    let size = 0;
    const chunks = [];
    for await (const chunk of response.body) {
      size += chunk.length;
      if (size > 8 << 20) fail('carrier-github-size');
      chunks.push(chunk);
    }
    return JSON.parse(Buffer.concat(chunks).toString('utf8'));
  } catch { fail('carrier-github-read-failed'); }
}

function upload(directory, name) {
  // Isolate SDK diagnostics: signed upload URLs or credentials never enter logs.
  return new Promise((resolve, reject) => {
    const child = fork(__filename, ['--upload', directory, name], {stdio: ['ignore', 'ignore', 'ignore', 'ipc']});
    let result;
    const timer = setTimeout(() => child.kill('SIGTERM'), 120000);
    child.on('message', value => { result = value; });
    child.once('error', () => { clearTimeout(timer); reject(new Error('carrier-upload-start-failed')); });
    child.once('exit', code => {
      clearTimeout(timer);
      if (code !== 0 || !Number.isSafeInteger(result?.id) || result.id <= 0 || !/^[a-f0-9]{64}$/.test(result.digest)) {
        reject(new Error('carrier-upload-failed'));
      } else resolve(result);
    });
  });
}

async function uploadChild(directory, name) {
  const {DefaultArtifactClient} = await import('@actions/artifact');
  const files = ['review-inputs.tar', 'review-ready.json', 'review-summary.json', 'review-packet.json']
    .map(leaf => path.join(directory, leaf));
  for (const file of files) readFile(file, 65 << 20);
  const result = await new DefaultArtifactClient().uploadArtifact(name, files, directory,
    {retentionDays: 30, compressionLevel: 6});
  await new Promise((resolve, reject) => process.send({id: result.id, digest: result.digest}, error => error ? reject(error) : resolve()));
}

async function main() {
  const env = process.env, root = fs.realpathSync('.');
  const startedAt = Date.now(), workflowRun = env.GITHUB_RUN_ID, sourceHead = env.EXPECTED_HEAD;
  const runId = `fixed-server-t1-${workflowRun}`;
  if (env.GITHUB_REPOSITORY !== REPO || env.GITHUB_RUN_ATTEMPT !== '1' ||
      !['order-quote', 'order-quote-team'].includes(env.CANARY_SCENARIO) || !env.INPUT_TOKEN) fail('carrier-environment');
  reviewMarker(workflowRun, sourceHead, 'sha256:' + '0'.repeat(64));
  const maintainers = readFile(path.join(root, '.github/MAINTAINERS'), 65536).toString('utf8').split(/\r?\n/);
  const repo = await github('', env.INPUT_TOKEN);
  if (repo.full_name !== REPO || repo.owner?.login !== 'chiga0' || !Number.isSafeInteger(repo.owner.id) || !maintainers.includes('chiga0')) fail('carrier-owner-not-authorized');
  const evidence = path.join(root, '.marshal/fixed-server-t1-canary', runId);
  const reviewDirectory = path.join(evidence, env.CANARY_SCENARIO === 'order-quote-team' ? 'team' : 't2');
  fs.appendFileSync(env.GITHUB_ENV, `T1_RUN_ID=${runId}\nT1_EVIDENCE_ROOT=${evidence}\n`);
  const args = ['scripts/fixed-server-t1-canary.sh', '--expected-head', sourceHead, '--pi-model', env.PI_MODEL,
    '--pi-node', env.PI_NODE, '--pi-bin', env.PI_BIN, '--pi-bundle', env.PI_BUNDLE,
    '--run-id', runId, '--evidence-root', evidence, '--scenario', env.CANARY_SCENARIO, '--await-review'];
  if (args.some(arg => typeof arg !== 'string')) fail('carrier-missing-input');
  const child = spawn('/bin/bash', args, {cwd: root, env: workerEnvironment(env), stdio: ['ignore', 'inherit', 'inherit']});
  let stopped = false, exitCode;
  const exited = new Promise(resolve => {
    child.once('error', () => { stopped = true; exitCode = -1; resolve(); });
    child.once('exit', code => { stopped = true; exitCode = code; resolve(); });
  });
  const terminate = () => { if (!stopped) child.kill('SIGTERM'); };
  process.once('SIGTERM', terminate);
  process.once('SIGINT', terminate);
  try {
    const readyDeadline = performance.now() + 15 * 60000;
    while (true) {
      if (stopped) fail('carrier-canary-exited-before-review');
      try { readFile(path.join(reviewDirectory, 'review.ready'), 0); break; }
      catch (error) { if (error.code !== 'ENOENT') throw error; }
      if (performance.now() >= readyDeadline) fail('carrier-review-not-ready');
      await pause(1000);
    }
    const targets = reviewTargets(reviewDirectory, env.CANARY_SCENARIO, runId, sourceHead);
    // Upload and both reviews consume the same wait window, not one per node.
    const deadline = performance.now() + 17 * 60000;
    for (const target of targets) {
      const marker = reviewMarker(workflowRun, sourceHead, target.ready.packetDigest);
      target.artifact = await upload(target.directory, `fixed-server-review-${workflowRun}${target.suffix}`);
      target.subject = {ownerId: repo.owner.id, startedAt, marker, runId: target.ready.run.runId, packetDigest: target.ready.packetDigest};
      console.log(`Independent review ready: artifact ${target.artifact.id}; Run ${target.ready.run.runId}; issue #186; marker ${marker.trim()}`);
    }
    const pending = new Set(targets);
    while (pending.size) {
      if (stopped) fail('carrier-canary-exited-awaiting-review');
      if (performance.now() >= deadline) fail('carrier-review-wait-expired');
      const comments = [];
      for (let page = 1; page <= 5; page++) {
        const batch = await github(`issues/186/comments?since=${encodeURIComponent(new Date(startedAt).toISOString())}&per_page=100&page=${page}`, env.INPUT_TOKEN);
        if (!Array.isArray(batch)) fail('carrier-comment-response');
        comments.push(...batch);
        if (batch.length < 100) break;
        if (page === 5) fail('carrier-comment-page-limit');
      }
      for (const target of pending) {
        const selected = selectComment(comments, target.subject);
        if (selected) {
          publishDecision(target, selected, workflowRun, sourceHead);
          pending.delete(target);
        }
      }
      if (pending.size) await pause(5000);
    }
    await Promise.race([exited, pause(6 * 60000).then(() => fail('carrier-finalize-timeout'))]);
    if (exitCode !== 0) fail('carrier-canary-not-accepted');
  } finally {
    process.removeListener('SIGTERM', terminate);
    process.removeListener('SIGINT', terminate);
    terminate();
    if (!stopped) {
      await Promise.race([exited, pause(20000)]);
      if (!stopped) { child.kill('SIGKILL'); await exited; }
    }
  }
}

module.exports = {workerEnvironment, readFile, validateReady, reviewTargets, publishDecision, reviewMarker, selectComment};
if (require.main === module) {
  if (process.argv[2] === '--upload') uploadChild(process.argv[3], process.argv[4]).then(() => process.exit(0), () => process.exit(1));
  else main().then(() => process.exit(0), error => {
    const reason = /^carrier-[a-z-]+$/.test(error.message) ? error.message : 'carrier-unavailable';
    console.error(`::error::${reason}`); process.exit(1);
  });
}
