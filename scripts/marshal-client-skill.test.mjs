import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawn} from 'node:child_process';
import {once} from 'node:events';
import {createServer} from 'node:http';
import {contract, validate} from '../packages/task-api/contract.mjs';
import {createTaskApiHandler} from '../packages/task-api/http-handler.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const skill = fs.readFileSync(path.join(root, 'skills/marshal-client/SKILL.md'), 'utf8');
const snippet = skill.match(/node --input-type=module <<'NODE'\n([\s\S]*?)\nNODE/)[1];
const token = 'fixture-client-skill-private-token-001';

function invoke(connectionFile) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, ['--input-type=module', '-e', snippet], {
      env: {MARSHAL_INSTALL_ROOT: root, MARSHAL_CONNECTION_FILE: connectionFile},
      stdio: ['ignore', 'pipe', 'pipe'], timeout: 15000,
    });
    let stdout = '', stderr = '';
    child.stdout.on('data', b => { stdout += b; });
    child.stderr.on('data', b => { stderr += b; });
    child.on('error', reject);
    child.on('close', code => resolve({code, stdout, stderr}));
  });
}

test('documented task body validates against shipped API, no invented ETL fields', () => {
  const examples = [...skill.matchAll(/```json\n([\s\S]*?)\n\s*```/g)];
  assert.equal(examples.length, 1);
  assert.equal(validate(JSON.parse(examples[0][1]), 'CreateTask'), true);
});

test('exact documented snippet uses real SDK/HTTP read-only and does not expose token', async t => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-skill-check-'));
  t.after(() => fs.rmSync(dir, {recursive: true, force: true}));
  const calls = [];
  const ready = contract.components.schemas.Readiness.examples[0];
  let handler;
  const server = createServer((req, res) => handler(req, res));
  t.after(() => { server.closeAllConnections(); server.close(); });
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  const url = `http://127.0.0.1:${server.address().port}`;
  handler = createTaskApiHandler({token, expectedHost: new URL(url).host, application: async request => {
    calls.push(request.operation); return structuredClone(ready);
  }});
  const connectionFile = path.join(dir, 'connection.json');
  fs.writeFileSync(connectionFile, JSON.stringify({url, token}), {mode: 0o600});
  const result = await invoke(connectionFile);
  assert.equal(result.code, 0, result.stderr);
  assert.deepEqual(JSON.parse(result.stdout), ready);
  assert.deepEqual(calls, ['ready.get']);
  assert.ok(!(result.stdout + result.stderr).includes(token));
  fs.writeFileSync(connectionFile, JSON.stringify({url: 'https://example.invalid', token}));
  const bad = await invoke(connectionFile);
  assert.equal(bad.code, 1);
  assert.equal(bad.stdout, '');
  assert.ok(!bad.stderr.includes(token));
  assert.deepEqual(calls, ['ready.get']);
});
