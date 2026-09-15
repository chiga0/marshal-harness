// Deterministic checker lifetime fixture, not a model or cleanup producer.
// The normal branch executes the original independent team checker unchanged.
if (process.env.MARSHAL_CUSTODY_CHECKER_HOLD === '1') {
  process.stdin.resume();
  // The real Runtime/custodian must stop this original process. This finite
  // fallback cannot yield a valid verification frame or pretend acceptance.
  setTimeout(() => process.exit(78), 45000);
} else {
  await import('../task-team-integration/checker.fixture.mjs');
}
