// Tests may discard only these exact public runtime notices. Production keeps
// them visible; arbitrary diagnostics, secrets and other warnings are retained.
export const withoutSQLiteRuntimeNotices = text => text
  .replace(/^\(node:\d+\) ExperimentalWarning: SQLite is an experimental feature and might change at any time\r?\n(?:\(Use `node --trace-warnings \.\.\.` to show where the warning was created\)\r?\n)?/gm, '')
  .replace(/^\(node:\d+\) \[MARSHAL_SQLITE_DEFENSIVE_UNAVAILABLE\] Warning: SQLite defensive mode unavailable; trusted-single-user fixed-SQL Store only\r?\n(?:\(Use `node --trace-warnings \.\.\.` to show where the warning was created\)\r?\n)?/gm, '');
