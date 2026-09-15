import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {spawnSync} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {digest} from '../task-store/store.ts';
import {utf8, expectedFiles} from './policy.ts';
import {filePermission} from './permission.ts';
const checker = fileURLToPath(new URL('./checker.ts', import.meta.url));
function fixture(t) {const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'generic-check-'))); t.after(() => fs.rmSync(root, {recursive: true, force: true})); fs.mkdirSync(path.join(root, 'results')); return root;}
test('data checker reads exact actual UTF8 file and rejects changed bytes, missing and binary content', t => {
  const root = fixture(t), content = Buffer.from('真实交付'), file = path.join(root, 'results', 'analysis.md');
  const request = {profile: 'test', nonce: 'fixed', binding: {}, input: {files: [{path: 'results/analysis.md', digest: digest(content), bytes: content.length}]}};
  const run = () => spawnSync(process.execPath, [checker], {cwd: root, input: JSON.stringify(request) + '\n', timeout: 5000});
  fs.writeFileSync(file, content); let result = run(); assert.equal(result.status, 0, result.stderr.toString());
  assert.deepEqual(JSON.parse(result.stdout).assertions[0].actual, request.input.files);
  fs.writeFileSync(file, 'changed'); assert.notEqual(run().status, 0);
  fs.unlinkSync(file); assert.notEqual(run().status, 0);
  const binary = Buffer.from([255]); fs.writeFileSync(file, binary); request.input.files[0] = {...request.input.files[0], digest: digest(binary), bytes: 1}; assert.notEqual(run().status, 0);
});
test('checker rejects traversal, links, oversize and empty output without evaluating content', t => {
  const root = fixture(t), target = path.join(root, 'outside'); fs.writeFileSync(target, 'do not execute');
  fs.symlinkSync(target, path.join(root, 'results', 'linked.md'));
  for (const ref of [{path:'../outside', bytes:14, digest:digest(Buffer.from('do not execute'))}, {path:'results/linked.md', bytes:14, digest:digest(Buffer.from('do not execute'))}]) {
    assert.notEqual(spawnSync(process.execPath, [checker], {cwd:root, input:JSON.stringify({input:{files:[ref]}})+'\n', timeout:5000}).status, 0);
  }
  assert.throws(() => utf8(Buffer.alloc(8193, 65))); assert.throws(() => utf8(Buffer.alloc(0))); assert.throws(() => utf8(Buffer.from(' \n')));
});
test('permission derives dynamic node file scope, denies inputs writes and arbitrary tools', t => {
  const root = fixture(t); fs.mkdirSync(path.join(root,'inputs')); fs.writeFileSync(path.join(root,'inputs','source'), 'data');
  const ticket = {role:'author', nodeId:'non-regional', input:{fileLayout:{inputs:[{path:'inputs/source'}],allowedPaths:['result.md']}}};
  const ask = (kind, rawInput) => filePermission(ticket, root, {toolCall:{kind,rawInput},options:[{kind:'allow_once',optionId:'yes'}]}).outcome;
  assert.equal(ask('read',{path:'inputs/source'}).optionId,'yes'); assert.equal(ask('edit',{path:'result.md',content:'answer'}).optionId,'yes');
  assert.equal(ask('read',{file_path:'inputs/source'}).optionId,'yes');
  assert.equal(ask('read',{file_path:'inputs/source',offset:0}).optionId,'yes');
  assert.equal(ask('edit',{file_path:'result.md',content:'answer'}).optionId,'yes');
  assert.equal(ask('edit',{path:'result.md',text:'answer'}).optionId,'yes');
  fs.writeFileSync(path.join(root,'result.md'),'old');
  assert.equal(ask('edit',{file_path:'result.md',old_string:'old',new_string:'new',replace_all:false}).optionId,'yes');
  assert.equal(ask('edit',{file_path:'result.md',old_string:'old',new_string:'new',replace_all:'yes'}).outcome,'cancelled');
  fs.unlinkSync(path.join(root,'result.md')); fs.symlinkSync(path.join(root,'inputs','source'),path.join(root,'result.md'));
  assert.equal(ask('edit',{path:'result.md',content:'answer'}).outcome,'cancelled');
  assert.equal(ask('edit',{path:'result.md',file_path:'result.md',content:'answer'}).outcome,'cancelled');
  assert.equal(ask('edit',{path:'result.md',text:'answer',content:'answer'}).outcome,'cancelled');
  for (const [kind,raw] of [['edit',{path:'inputs/source',content:'bad'}],['execute',{path:'result.md',content:'x'}],['edit',{path:'../result.md',content:'x'}],['edit',{path:'result.md',content:'x',command:'oops'}]]) assert.equal(ask(kind,raw).outcome,'cancelled');
});
test('Qwen empty-old-string create is confined to absent bounded result file', t => {
  const root = fixture(t), output = path.join(root, 'result.md');
  const ticket = {role:'author', input:{fileLayout:{inputs:[],allowedPaths:['result.md','other.md']}}};
  const ask = (rawInput, role = 'author') => filePermission({...ticket, role}, root, {
    toolCall:{kind:'edit',rawInput},options:[{kind:'allow_once',optionId:'yes'}],
  }).outcome;
  const create = {file_path:'result.md',old_string:'',new_string:'<!doctype html><title>Todo</title>'};
  assert.equal(ask(create).optionId, 'yes');
  assert.equal(ask({...create,file_path:output,replace_all:false}).optionId, 'yes');
  assert.equal(ask({...create,new_string:'x'.repeat(8192)}).optionId, 'yes');
  for (const raw of [
    {...create,new_string:''}, {...create,new_string:'x'.repeat(8193)},
    {...create,new_string:'中'.repeat(2731)}, {...create,new_string:'bad\0'},
    {...create,new_string:'\ud800'}, {...create,replace_all:'yes'},
    {...create,command:'anything'}, {...create,file_path:'../result.md'},
    {...create,file_path:'other.md'}, {...create,path:'result.md'},
  ]) assert.equal(ask(raw).outcome, 'cancelled');
  assert.equal(ask(create, 'reviewer').outcome, 'cancelled');
  fs.writeFileSync(output, 'keep');
  assert.equal(ask(create).outcome, 'cancelled');
  assert.equal(fs.readFileSync(output, 'utf8'), 'keep');
  fs.unlinkSync(output); fs.mkdirSync(output);
  assert.equal(ask(create).outcome, 'cancelled');
  fs.rmdirSync(output); fs.writeFileSync(path.join(root, 'outside'), 'keep');
  fs.symlinkSync(path.join(root, 'outside'), output);
  assert.equal(ask(create).outcome, 'cancelled');
});
test('expected files are bound to exact final candidate refs, not author report', () => {
  const ref={path:'result.md',bytes:2,digest:digest(Buffer.from('ok'))};
  const ticket={input:{verification:{binding:{deliveries:[{nodeId:'research',path:'result.md',targetPath:'results/research.md'}]},manifests:[{nodeId:'research',manifest:{files:[ref]}}]}}};
  assert.deepEqual(expectedFiles(ticket),[{...ref,path:'results/research.md'}]); ticket.input.verification.manifests=[]; assert.throws(()=>expectedFiles(ticket));
});
