// Runtime admission is separate from the Node version used to freeze an asset.
export function supportsNode(version = process.versions.node) {
  return typeof version === 'string' && /^\d+\.\d+\.\d+$/.test(version) && Number(version.split('.')[0]) >= 22;
}

export function requireNodeRuntime(version = process.versions.node) {
  if (!supportsNode(version)) throw new Error('unsupported_node_runtime: Node >=22 required');
}

// Probe a private in-memory connection, never a user database. Unsupported
// SQLite builds fail before state directories or persistent files are created.
export function inspectSQLiteRuntime(DatabaseSync) {
  let db;
  try {
    db = new DatabaseSync(':memory:', {allowExtension: false, enableForeignKeyConstraints: true, defensive: true, readBigInts: true});
    if (db.prepare('SELECT 9007199254740993 AS value').get().value !== 9007199254740993n || db.isTransaction !== false ||
        db.prepare('PRAGMA foreign_keys').get().foreign_keys !== 1n) throw new Error('required_sqlite_capability');
    db.exec('BEGIN');
    if (db.isTransaction !== true) throw new Error('required_sqlite_capability');
    db.exec('ROLLBACK');
    if (db.isTransaction !== false) throw new Error('required_sqlite_capability');
    db.exec('PRAGMA busy_timeout=100');
    if (db.prepare('PRAGMA busy_timeout').get().timeout !== 100n) throw new Error('required_sqlite_capability');
    db.exec('PRAGMA writable_schema=ON');
    return Object.freeze({defensive: db.prepare('PRAGMA writable_schema').get().writable_schema === 0n});
  } catch {
    throw new Error('unsupported_sqlite_runtime: required BigInt, transaction, foreign-key and busy-timeout APIs unavailable; upgrade Node');
  } finally { db?.close(); }
}
