import test from 'node:test';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {execFileSync} from 'node:child_process';
import {pack, SOURCE_FILES} from './index.mjs';
import {exerciseInstalledTeam} from './installed-team.fixture.mjs';

const repository = fileURLToPath(new URL('../..', import.meta.url));
const git = (root, ...args) => execFileSync('git', ['-C', root, ...args], {encoding: 'utf8', timeout: 10000, stdio: ['ignore', 'pipe', 'pipe']}).trim();
for (const custody of [false, true]) test(`installed ${custody ? 'custody-v2' : 'legacy-v1'} package delivers a real process team through CLI/HTTP and cold-reopens exact results`, {timeout: 45000}, async t => {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-team-package-')));
  const source = path.join(parent, 'source'), installed = path.join(parent, 'package'); let passed = false;
  t.after(() => {if (passed) fs.rmSync(parent, {recursive: true, force: true}); else t.diagnostic('失败候选保留：' + parent);});
  fs.mkdirSync(source, {mode: 0o700});
  for (const file of SOURCE_FILES) {
    fs.mkdirSync(path.dirname(path.join(source, file)), {recursive: true, mode: 0o700});
    fs.copyFileSync(path.join(repository, file), path.join(source, file));
  }
  git(source, 'init', '-q'); git(source, 'add', 'packages');
  git(source, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'package team source');
  const sourceHead = git(source, 'rev-parse', 'HEAD'), packed = pack({sourceRoot: source, sourceHead, target: installed});
  await exerciseInstalledTeam(t, {installed, manifestDigest: packed.manifestDigest, sourceHead, custody});
  passed = true;
});
