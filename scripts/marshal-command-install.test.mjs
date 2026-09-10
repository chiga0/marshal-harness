import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {spawnSync} from 'node:child_process';
import {installCommand} from '../packages/task-local/install-command.mjs';

function setup(t) {
  const home = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-launcher-')));
  t.after(() => fs.rmSync(home, {recursive: true, force: true}));
  const installRoot = path.join(home, "version with ' quote");
  fs.mkdirSync(path.join(installRoot, 'packages/task-local'), {recursive: true});
  fs.writeFileSync(path.join(installRoot, 'packages/task-local/main.mjs'), 'console.log(JSON.stringify(process.argv.slice(2)))');
  return {home, installRoot};
}
test('installed command quotes Node/entry paths, forwards arguments and is repeatable', t => {
  const options = setup(t), first = installCommand(options), second = installCommand(options);
  assert.equal(first.created, true); assert.equal(second.created, false);
  const result = spawnSync(first.commandPath, ['serve', '--config', "a ' b ; $(not-executed)"], {encoding: 'utf8'});
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(JSON.parse(result.stdout), ['serve', '--config', "a ' b ; $(not-executed)"]);
});
test('foreign commands, modified launchers, links and unsafe bin directories are preserved', t => {
  const options = setup(t), {commandPath} = installCommand(options);
  fs.writeFileSync(commandPath, '#!/bin/sh\necho existing-user-command\n');
  assert.throws(() => installCommand(options), /command_install_conflict/);
  assert.match(fs.readFileSync(commandPath, 'utf8'), /existing-user-command/);
  fs.unlinkSync(commandPath);
  fs.symlinkSync(path.join(options.home, 'elsewhere'), commandPath);
  assert.throws(() => installCommand(options));
  assert.equal(fs.lstatSync(commandPath).isSymbolicLink(), true);
  fs.chmodSync(path.dirname(commandPath), 0o777);
  assert.throws(() => installCommand(options), /command_install_conflict/);
});
test('upgrade does not silently replace command pointing at another installation', t => {
  const options = setup(t), first = installCommand(options);
  const bytes = fs.readFileSync(first.commandPath);
  const other = path.join(options.home, 'other');
  fs.mkdirSync(path.join(other, 'packages/task-local'), {recursive: true});
  fs.writeFileSync(path.join(other, 'packages/task-local/main.mjs'), '');
  assert.throws(() => installCommand({...options, installRoot: other}), /command_install_conflict/);
  assert.deepEqual(fs.readFileSync(first.commandPath), bytes);
});
