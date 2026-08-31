package execution

import "testing"

// sealedMigrationSkipReason marks suites that fabricate a RUNNING+ lineage.
// The sealed Run-start gate (ADR 0065/0069) rejects fabricated transitions at
// both runstore.Append and WriteSnapshot-vs-journal equivalence, so such a
// lineage is only producible by the darwin real composition. Until the darwin
// composition driver migration lands (sealed-migration, see
// docs/roadmap-status.md), ubuntu and driverless hosts rely on these explicit
// skips; each one must be re-enabled by that migration slice, never removed.
const sealedMigrationSkipReason = "sealed-migration: RUNNING+ lineage requires the darwin real composition driver (driver slice pending)"

func sealedMigrationSkip(t *testing.T) {
	t.Helper()
	t.Skip(sealedMigrationSkipReason)
}
