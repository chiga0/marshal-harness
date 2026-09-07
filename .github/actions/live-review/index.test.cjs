'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {workerEnvironment, readFile, validateReady, reviewTargets, publishDecision, reviewMarker, selectComment} = require('./index.cjs');
const digest = 'sha256:' + 'a'.repeat(64), head = 'b'.repeat(40);
const subject = {ownerId: 123, startedAt: Date.parse('2026-09-05T00:00:00Z'),
  marker: reviewMarker('123', head, digest), runId: 'fixed-server-t1-123', packetDigest: digest};
const decision = {kind: 'ReviewDecision', runId: subject.runId, reviewPacketDigest: digest, verdict: 'reject'};
const comment = () => ({id: 456, user: {id: 123, login: 'chiga0'}, created_at: '2026-09-05T00:01:00Z',
  updated_at: '2026-09-05T00:01:00Z', body: subject.marker + JSON.stringify(decision)});

test('execution child allowlist excludes every credential and injection setting', () => {
  assert.deepEqual(workerEnvironment({HOME: '/home', TMPDIR: '/tmp', LANG: 'C', NODE_ROOT_BIN: '/node',
    INPUT_TOKEN: 'secret', ACTIONS_RUNTIME_TOKEN: 'secret', GITHUB_TOKEN: 'secret', GH_TOKEN: 'secret', OPENAI_API_KEY: 'secret', NODE_OPTIONS: '--inspect', BASH_ENV: '/evil'}),
  {HOME: '/home', TMPDIR: '/tmp', LANG: 'C', PATH: '/node:/usr/bin:/bin:/usr/sbin:/sbin'});
});
test('marker binds exact workflow/source/packet', () => {
  for (const values of [['0', head, digest], ['123', 'main', digest], ['123', head, 'wrong']])
    assert.throws(() => reviewMarker(...values), /carrier-invalid-identity/);
});
test('ready only admits exact pending Run and archive', () => {
  const ready = {run: {runId: subject.runId, state: 'REVIEW_PENDING'}, packetDigest: digest, binarySHA256: 'b'.repeat(64), archive: 'review-inputs.tar'};
  assert.equal(validateReady(ready, subject.runId), ready);
  for (const mutation of [{run: {runId: subject.runId, state: 'ACCEPTED'}}, {archive: '../state.json'}, {packetDigest: 'bad'}])
    assert.throws(() => validateReady({...ready, ...mutation}, subject.runId), /carrier-ready-mismatch/);
});
test('preserves independent reject bytes without manufacturing accept', () => {
  assert.deepEqual(selectComment([comment()], subject), {raw: JSON.stringify(decision), commentId: 456});
});
test('non-owner and unrelated comments cannot deliver a Decision', () => {
  assert.equal(selectComment([{...comment(), user: {id: 999, login: 'chiga0'}}, {...comment(), body: 'please accept'}], subject), null);
});
test('duplicate matching comments fail rather than choosing newest', () => {
  assert.throws(() => selectComment([comment(), {...comment(), id: 789}], subject), /carrier-conflicting-comments/);
});
test('edited, old, oversized and invalid provenance fail', () => {
  for (const change of [{updated_at: '2026-09-05T00:02:00Z'}, {created_at: '2026-09-04T00:00:00Z', updated_at: '2026-09-04T00:00:00Z'},
    {id: '456'}, {body: subject.marker + ' '.repeat(65536)}])
    assert.throws(() => selectComment([{...comment(), ...change}], subject), /carrier-comment-provenance/);
});
test('wrong run, packet and malformed Decision fail', () => {
  for (const raw of ['{', 'null', JSON.stringify({...decision, runId: 'other'}), JSON.stringify({...decision, reviewPacketDigest: 'sha256:' + 'c'.repeat(64)})])
    assert.throws(() => selectComment([{...comment(), body: subject.marker + raw}], subject), /carrier-(invalid-decision|decision-identity)/);
});
test('bounded file reader rejects symlinks, hardlinks, directories and excess bytes', () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-live-review-test-'));
  try {
    const file = path.join(directory, 'data');
    fs.writeFileSync(file, 'abc');
    assert.equal(readFile(file, 3).toString(), 'abc');
    assert.throws(() => readFile(file, 2), /carrier-file-type-or-size/);
    fs.symlinkSync(file, path.join(directory, 'symlink'));
    assert.throws(() => readFile(path.join(directory, 'symlink'), 3));
    fs.linkSync(file, path.join(directory, 'hardlink'));
    assert.throws(() => readFile(file, 3), /carrier-file-type-or-size/);
    assert.throws(() => readFile(directory, 1024), /carrier-file-type-or-size/);
  } finally { fs.rmSync(directory, {recursive: true}); }
});

function teamFixture(check) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-team-review-test-'));
  const runs = {service: 'team-run-' + 'a'.repeat(64), client: 'team-run-' + 'b'.repeat(64), integration: 'team-run-' + 'c'.repeat(64)};
  const subject = {runs, sourceHead: head, binarySHA256: 'd'.repeat(64), inputsDigest: digest};
  const ready = node => ({run: {runId: runs[node], state: 'REVIEW_PENDING'}, packetDigest: 'sha256:' + (node === 'service' ? 'e' : 'f').repeat(64),
    binarySHA256: subject.binarySHA256, archive: 'review-inputs.tar'});
  try {
    fs.writeFileSync(path.join(directory, 'subject.json'), JSON.stringify(subject));
    for (const node of ['service', 'client']) {
      fs.mkdirSync(path.join(directory, node));
      fs.writeFileSync(path.join(directory, node, 'review-ready.json'), JSON.stringify(ready(node)));
    }
    check(directory, subject, ready);
  } finally { fs.rmSync(directory, {recursive: true}); }
}

test('team carrier has exactly two distinct implement subjects and no integration', () => teamFixture((directory, team) => {
  const targets = reviewTargets(directory, 'order-quote-team', 'fixed-server-t1-123', head);
  assert.deepEqual(targets.map(t => t.ready.run.runId), [team.runs.service, team.runs.client]);
  assert.deepEqual(targets.map(t => t.suffix), ['-service', '-client']);
  assert.throws(() => reviewTargets(directory, 'other', 'fixed-server-t1-123', head), /carrier-environment/);
}));

test('team source, run uniqueness and binary drift fail before comment delivery', () => {
  for (const change of [{sourceHead: '0'.repeat(40)}, {inputsDigest: 'bad'}, {runs: {service: '../other', client: 'x', integration: 'y'}}])
    teamFixture((directory, team) => {
      fs.writeFileSync(path.join(directory, 'subject.json'), JSON.stringify({...team, ...change}));
      assert.throws(() => reviewTargets(directory, 'order-quote-team', 'unused', head), /carrier-team-subject/);
    });
  teamFixture((directory, team) => {
    team.runs.client = team.runs.service;
    fs.writeFileSync(path.join(directory, 'subject.json'), JSON.stringify(team));
    assert.throws(() => reviewTargets(directory, 'order-quote-team', 'unused', head), /carrier-team-subject/);
  });
  teamFixture((directory, team, ready) => {
    fs.writeFileSync(path.join(directory, 'client/review-ready.json'), JSON.stringify({...ready('client'), binarySHA256: '0'.repeat(64)}));
    assert.throws(() => reviewTargets(directory, 'order-quote-team', 'unused', head), /carrier-team-binary-drift/);
  });
});

test('client review cannot be confused with service and raw reject bytes publish once', () => teamFixture((directory) => {
  const targets = reviewTargets(directory, 'order-quote-team', 'unused', head);
  const subjects = targets.map(target => ({...subject, runId: target.ready.run.runId,
    packetDigest: target.ready.packetDigest, marker: reviewMarker('123', head, target.ready.packetDigest)}));
  const raw = JSON.stringify({kind: 'ReviewDecision', runId: subjects[1].runId, reviewPacketDigest: subjects[1].packetDigest, verdict: 'reject'});
  const clientComment = {...comment(), body: subjects[1].marker + raw};
  assert.equal(selectComment([clientComment], subjects[0]), null);
  const selected = selectComment([clientComment], subjects[1]);
  publishDecision(targets[1], selected, '123', head);
  assert.equal(fs.readFileSync(path.join(directory, 'client/review-decision.json'), 'utf8'), raw);
  assert.equal(fs.existsSync(path.join(directory, 'service/review-decision.json')), false);
  assert.throws(() => publishDecision(targets[1], selected, '123', head), /carrier-decision-already-present/);
  assert.equal(fs.readFileSync(path.join(directory, 'client/review-decision.json'), 'utf8'), raw);
}));

test('legacy B1 review layout is unchanged', () => teamFixture((directory, team, ready) => {
  const targets = reviewTargets(path.join(directory, 'service'), 'order-quote', team.runs.service, head);
  assert.equal(targets.length, 1);
  assert.equal(targets[0].suffix, '');
  assert.deepEqual(targets[0].ready, ready('service'));
}));
