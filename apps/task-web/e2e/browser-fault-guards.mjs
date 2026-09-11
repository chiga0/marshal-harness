import assert from 'node:assert/strict';

export async function bounded(run, timeoutMs) {
  const controller = new AbortController();
  let timer;
  try {
    return await Promise.race([Promise.resolve().then(() => run(controller.signal)), new Promise((_, reject) => {
      timer = setTimeout(() => { controller.abort(); reject(new Error('受控等待超时')); }, timeoutMs);
    })]);
  } finally { clearTimeout(timer); }
}

export async function readJSON(url, init, timeoutMs = 10000) {
  return bounded(async signal => {
    const response = await fetch(url, {...init, signal});
    assert.ok(response.ok, `fixture HTTP ${response.status}`);
    return response.json(); // 同一个 AbortSignal 覆盖响应体，不只覆盖响应头。
  }, timeoutMs);
}

export async function until(read, accept, timeoutMs = 60000) {
  const deadline = performance.now() + timeoutMs;
  while (performance.now() < deadline) {
    const value = await bounded(read, Math.max(1, deadline - performance.now()));
    if (accept(value)) return value;
    await new Promise(resolve => setTimeout(resolve, Math.min(250, Math.max(1, deadline - performance.now()))));
  }
  throw new Error('受控状态等待超时');
}

export function assertReplay(name, observed) {
  assert.equal(observed.length, 2);
  for (const item of observed) {
    assert.equal(item.status, 202);
    if (name === 'approve' || name === 'cancel') assert.ok(typeof item.id === 'string' && item.id.trim().length > 0, '必须取得非空 Operation ID');
  }
  assert.equal(observed[1].body, observed[0].body);
  assert.ok(observed[0].key); assert.equal(observed[1].key, observed[0].key);
  assert.equal(observed[1].id, observed[0].id);
}

export async function finish({closeBrowser, stopService, persist, evidence, timeoutMs = 15000}) {
  const fail = stage => { evidence.result = 'FAIL'; (evidence.cleanupFailures ??= []).push(stage); };
  try { if (closeBrowser) await bounded(closeBrowser, timeoutMs); } catch { fail('browser.close'); }
  try {
    if (stopService) {
      evidence.serviceExit = await bounded(stopService, timeoutMs);
      if (evidence.serviceExit.code !== 0) fail('service.exit');
    }
  } catch { fail('service.stop'); }
  try { await persist(evidence); } catch { fail('evidence.persist'); }
  return evidence.result === 'FAIL';
}
