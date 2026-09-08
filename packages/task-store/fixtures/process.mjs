// Fixed, non-Agent fixture. It only operates on the private test-created root.
import { Store, encode, digest, makeEvent } from '../store.mjs';

const [mode, root] = process.argv.slice(2);
try {
  const store = Store.openExisting(root);
  if (mode === 'probe') { store.close(); process.send?.({ code: 'opened' }); process.disconnect?.(); }
  else if (mode === 'before-commit' || mode === 'after-commit') {
    const owner = store.claimOwner(store.info().generation, 'fixture-process', Date.now() + 60000);
    store.write(owner, tx => {
      const event = makeEvent('task-1', 1, { fixtureOnly: true });
      const source = { stream: 'task-1', sequence: event.sequence, digest: event.digest };
      tx.append('task-1', { sequence: 0n, digest: '' }, [event]);
      tx.putProjection('task', 'task-1', 0, source, encode({ status: 'approved' }));
      tx.putProjection('budget', 'task-1', 0, source, encode({ attemptsReserved: 1 }));
      tx.putReceipt({ scope: 'task-1', operation: 'approve', keyDigest: digest(encode('key')) }, digest(encode({ revision: 1 })), source, encode({ acceptedRevision: 1 }));
      tx.enqueue({ id: 'start-1', taskId: 'task-1', kind: 'start', inputDigest: digest(encode('input')), payload: encode({}), source });
      if (mode === 'before-commit') process.exit(23);
    });
    process.exit(24);
  } else { store.close(); process.exitCode = 2; process.disconnect?.(); }
} catch (error) {
  process.send?.({ code: typeof error.code === 'string' ? error.code : 'unexpected-error' });
  process.exitCode = 1; process.disconnect?.();
}
