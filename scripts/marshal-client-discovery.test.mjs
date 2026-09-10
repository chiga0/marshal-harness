import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, chmod, symlink, rm, access, realpath } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { discoverAgents } from '../skills/marshal-client/scripts/discovery.mjs';

async function fixture(t) {
  const root = await realpath(await mkdtemp(join(tmpdir(), 'marshal-discovery-')));
  t.after(() => rm(root, { recursive: true, force: true }));
  return root;
}
async function executable(file, content = '#!/bin/sh\nexit 99\n') {
  await writeFile(file, content);
  await chmod(file, 0o755);
}

test('发现固定三个命令，保留 PATH 优先级和 symlink 的入口/真实路径', async (t) => {
  const root = await fixture(t);
  const first = join(root, 'first with spaces');
  const second = join(root, 'second');
  await mkdir(first); await mkdir(second);
  const actual = join(root, 'cli-entry.js');
  await executable(actual);
  await symlink(actual, join(first, 'qwen'));
  await executable(join(second, 'qwen'));
  await executable(join(second, 'pi'));
  await executable(join(first, 'opencode'));
  await executable(join(first, 'codex'));
  assert.deepEqual(await discoverAgents({ pathValue: `${first}:${second}:${first}` }), [
    { id: 'qwen', command: 'qwen', executable: join(first, 'qwen'), resolvedPath: actual },
    { id: 'pi', command: 'pi', executable: join(second, 'pi'), resolvedPath: join(second, 'pi') },
    { id: 'opencode', command: 'opencode', executable: join(first, 'opencode'), resolvedPath: join(first, 'opencode') },
  ]);
});

test('空/相对/控制字符路径不触发搜索，支持没有 PATH', async () => {
  assert.deepEqual(await discoverAgents({ pathValue: ':.:./bin:bin:/invalid\u0000path:/invalid\npath:' }), []);
  assert.deepEqual(await discoverAgents({ pathValue: '' }), []);
});

test('目录、非执行文件、断链和循环 symlink 不作为候选，继续下一 PATH', async (t) => {
  const root = await fixture(t);
  const bad = join(root, 'bad'); const next = join(root, 'next'); const good = join(root, 'good');
  await mkdir(bad); await mkdir(next); await mkdir(good);
  await mkdir(join(bad, 'qwen'));
  await writeFile(join(bad, 'pi'), 'not executable');
  await chmod(join(bad, 'pi'), 0o644);
  await symlink(join(root, 'missing'), join(bad, 'opencode'));
  await symlink('qwen', join(next, 'qwen'));
  await executable(join(good, 'qwen'));
  const result = await discoverAgents({ pathValue: `${bad}:${next}:${good}` });
  assert.deepEqual(result.map((entry) => entry.executable), [join(good, 'qwen')]);
});

test('发现不会运行入口或读取 Agent 输出', async (t) => {
  const root = await fixture(t);
  const marker = join(root, 'was-executed');
  await executable(join(root, 'qwen'), `#!/bin/sh\ntouch '${marker}'\n`);
  assert.equal((await discoverAgents({ pathValue: root })).length, 1);
  await assert.rejects(access(marker), { code: 'ENOENT' });
});

test('拒绝超大或非字符串输入，限制文件系统探测次数', async () => {
  for (const pathValue of [null, {}, 42, 'x'.repeat(65537), Array(257).fill('/bin').join(':')]) {
    await assert.rejects(discoverAgents({ pathValue }), TypeError);
  }
});
