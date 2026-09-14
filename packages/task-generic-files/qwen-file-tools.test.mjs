import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import {QWEN_FILE_ARGS, QWEN_FILE_TOOLS, QWEN_EXCLUDED_TOOLS} from './qwen-file-tools.mjs';
import {SOURCE_FILES} from '../task-distribution/index.mjs';

test('files-only Qwen denies synthetic tools outside its core allowlist', () => {
  // The observed incident called record_artifact and then get_goal despite
  // --core-tools. Qwen exempts synthetic tools from that native allowlist.
  const injected = ['record_artifact', 'get_goal', 'update_goal', 'propose_goal',
    'artifact', 'agent', 'skill', 'tool_search', 'ask_user_question', 'send_message',
    'task_create', 'task_update', 'task_list', 'task_stop', 'team_create', 'team_delete',
    'team_plan_approval', 'request_shutdown', 'list_agents', 'enter_plan_mode',
    'exit_plan_mode', 'enter_worktree', 'exit_worktree', 'workflow', 'report_findings',
    'structured_output', 'image_gen', 'display_image'];
  const deny = QWEN_FILE_ARGS[QWEN_FILE_ARGS.indexOf('--exclude-tools') + 1].split(',');
  assert.deepEqual(deny, [...QWEN_EXCLUDED_TOOLS]);
  for (const tool of injected) assert.ok(deny.includes(tool), `missing native denial: ${tool}`);
  assert.equal(new Set(deny).size, deny.length);
  assert.deepEqual([...QWEN_FILE_TOOLS].sort(), ['edit', 'glob', 'grep_search', 'list_directory', 'read_file', 'write_file']);
  for (const tool of QWEN_FILE_TOOLS) assert.ok(!deny.includes(tool));
  assert.equal(QWEN_FILE_ARGS[QWEN_FILE_ARGS.indexOf('--approval-mode') + 1], 'default');
  assert.ok(Object.isFrozen(QWEN_FILE_ARGS));
});

test('all Qwen file profiles consume the packaged common native tool policy', () => {
  for (const file of ['qwen-service-config.mjs', 'qwen-short-service-config.mjs']) {
    const source = fs.readFileSync(new URL(file, import.meta.url), 'utf8');
    assert.match(source, /import \{QWEN_FILE_ARGS\} from '\.\/qwen-file-tools\.mjs'/);
    assert.match(source, /args: QWEN_FILE_ARGS/);
    assert.doesNotMatch(source, /--exclude-tools|--core-tools/);
  }
  assert.ok(SOURCE_FILES.includes('packages/task-generic-files/qwen-file-tools.mjs'));
});
