import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import http from 'node:http';
import {startReportServer} from './report-server.mjs';
test('finite report reader denies non-GET/foreign origin/path/link/oversize and replaced root; normal bytes are exact', async t => {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-report-reader-'))), root = path.join(parent, 'reports');
  fs.mkdirSync(root, {mode: 0o700}); let reader;
  t.after(async () => {await reader?.close(); fs.rmSync(parent, {recursive: true, force: true});});
  const name = 'task-test-' + 'a'.repeat(64) + '.json', file = path.join(root, name), body = Buffer.from('{"public":true}');
  fs.writeFileSync(file, body, {mode: 0o600}); reader = await startReportServer({root, port: 0});
  const request = (route = name, options = {}) => fetch(new URL(route, reader.url), {...options, redirect: 'error', signal: AbortSignal.timeout(5000)});
  let result = await request(); assert.equal(result.status, 200); assert.equal(result.headers.get('content-type'), 'application/json');
  assert.equal(Number(result.headers.get('content-length')), body.length); assert.deepEqual(Buffer.from(await result.arrayBuffer()), body);
  for (const options of [{method: 'POST'}, {headers: {origin: 'http://127.0.0.1'}}, {headers: {host: 'other'}}, {headers: {authorization: 'Bearer not-a-secret'}}]) {
    // fetch normalizes Host to its URL. Use the original HTTP request API to
    // actually transmit the deliberate foreign Host instead of testing a GET
    // whose on-wire Host remained legitimate.
    const denied = await new Promise((resolve, reject) => {
      const request = http.request(new URL(name, reader.url), options, response => {let bytes = 0;
        response.on('data', chunk => {bytes += chunk.length; if (bytes > 1048576) request.destroy(Error('response bound'));});
        response.on('end', () => resolve({status: response.statusCode, bytes})); response.on('error', reject);
      }); request.setTimeout(5000, () => request.destroy(Error('request deadline'))); request.on('error', reject); request.end();
    });
    assert.equal(denied.status, 404, JSON.stringify(options)); assert.equal(denied.bytes, 0);
  }
  for (const route of ['../../anything', name + '?x=1', '%2e%2e/' + name]) assert.equal((await request(route)).status, 404);
  fs.renameSync(file, path.join(parent, 'saved.json')); fs.symlinkSync(path.join(parent, 'saved.json'), file);
  assert.equal((await request()).status, 404); fs.unlinkSync(file);
  fs.linkSync(path.join(parent, 'saved.json'), file); assert.equal((await request()).status, 404); fs.unlinkSync(file);
  fs.writeFileSync(file, Buffer.alloc(1048577), {mode: 0o600}); assert.equal((await request()).status, 404);
  fs.renameSync(root, path.join(parent, 'old')); fs.mkdirSync(root, {mode: 0o700}); fs.writeFileSync(path.join(root, name), body, {mode: 0o600});
  assert.equal((await request()).status, 404); // Do not silently adopt replacement root.
});
