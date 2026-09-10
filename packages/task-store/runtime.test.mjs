import test from 'node:test';
import assert from 'node:assert/strict';
import {DatabaseSync} from 'node:sqlite';
import {supportsNode, requireNodeRuntime, inspectSQLiteRuntime} from './runtime.mjs';

test('Node >=22 admission is not an exact patch or upper-major pin', () => {
  for (const version of ['22.0.0', '22.22.1', '23.1.0', '24.15.0', '25.0.0', '30.2.1']) assert.equal(supportsNode(version), true);
  for (const version of ['20.20.0', '21.7.0', 'v22.22.1', '22', '', null]) assert.equal(supportsNode(version), false);
  assert.throws(() => requireNodeRuntime('20.20.0'), /Node >=22/);
});

test('actual SQLite supplies required primitives and reports optional defensive mode', () => {
  const result = inspectSQLiteRuntime(DatabaseSync);
  assert.equal(typeof result.defensive, 'boolean');
  assert.ok(Object.isFrozen(result));
  if (process.versions.node === '22.22.1') assert.equal(result.defensive, false);
  if (process.versions.node === '24.15.0') assert.equal(result.defensive, true);
});

test('missing SQLite BigInt, transaction, FK or timeout capability fails explicitly and closes probe', () => {
  for (const missing of ['bigint', 'transaction', 'foreignKeys', 'timeout']) {
    let closed = false;
    class IncompleteDatabase {
      isTransaction = missing === 'transaction' ? undefined : false;
      prepare(sql) { return {get: () => sql.startsWith('SELECT') ? {value: missing === 'bigint' ? 9007199254740992 : 9007199254740993n} :
        sql.includes('foreign_keys') ? {foreign_keys: missing === 'foreignKeys' ? 0n : 1n} : {timeout: missing === 'timeout' ? 0n : 100n}}; }
      exec(sql) { if (sql === 'BEGIN') this.isTransaction = true; if (sql === 'ROLLBACK') this.isTransaction = false; }
      close() { closed = true; }
    }
    assert.throws(() => inspectSQLiteRuntime(IncompleteDatabase), /unsupported_sqlite_runtime/);
    assert.equal(closed, true);
  }
});
