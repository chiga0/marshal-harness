// @vitest-environment node
// 与现有OpenAPI验证器逐例交叉核对，不在UI引入第二套更严格的合同。
import {expect, test} from 'vitest';
import {validate} from '../../../packages/task-api/contract.mjs';
import {parseOperation} from '../src/lib/transport/types';

const original = {id: 'op-1', taskId: 'task-1', kind: 'task.cancel', status: 'succeeded', taskRevision: 1,
  createdAt: '2026-09-11T00:00:00Z', updatedAt: '2026-09-11T00:00:00Z'};
const cases = [
  original,
  ...['accepted', 'running', 'failed', 'unknown', 'completed', '', null].map(status => ({...original, status})),
  ...['task.approve', 'task.pause', 'task.resume', 'task.answer', 'task.repair', 'wrong'].map(kind => ({...original, kind})),
  ...['', 'a'.repeat(128), 'a'.repeat(129), '-abc', 'a/b', '中文', 'abc\n'].map(id => ({...original, id})),
  {...original, taskId: 'bad/id'},
  {...original, workerId: null},
  {...original, workerId: 'worker-1'},
  {...original, kind: 'worker.cancel'},
  {...original, kind: 'worker.cancel', workerId: 'worker-1'},
  {...original, kind: 'worker.cancel', workerId: 'bad/id'},
  ...['2026-09-11', 'September 11, 2026', '2026-09-11T00:00:00', '2026-09-11T00:00:00+08:00', '2026-09-11T00:00:00.123456Z', '2026-09-11t00:00:00z', '2026-99-11T00:00:00Z'].map(updatedAt => ({...original, updatedAt})),
  ...['', ' ', '\n', '\u0000', 'a\u0000b', '\ud800', '\udc00', 'a'.repeat(128), 'a'.repeat(129), '中'.repeat(42), '中'.repeat(43), '😀'.repeat(32), '😀'.repeat(33), ' multi\nline '].map(code => ({...original, code})),
  ...[0, 1.5, Number.MAX_SAFE_INTEGER, Number.MAX_SAFE_INTEGER + 1, '1', null].map(taskRevision => ({...original, taskRevision})),
  {...original, undeclared: true}, {...original, code: undefined}, null, [],
  ...Object.keys(original).map(key => Object.fromEntries(Object.entries(original).filter(([name]) => name !== key))),
];

test.each(cases.map((value, index) => [index, value]))('Operation浏览器校验与原合同一致：%i', (_index, value) => {
  let accepted = true;
  try { parseOperation(value); } catch { accepted = false; }
  expect(accepted).toBe(validate(value, 'Operation'));
});
