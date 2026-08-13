package dispatch

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/chiga0/marshal-harness/internal/provider"
)

// ledgerLease builds a sealed active DispatchLease whose identity fields
// derive from seed, so distinct seeds never collide and every
// Digest/Token-family fixture value is constructed through helpers
// (gitleaks generic-api-key publication gate).
func ledgerLease(seed string) DispatchLease {
	lease := DispatchLease{
		LeaseId:                          fixedDigest("ledger-lease-" + seed),
		AuthorityNamespaceId:             testAuthorityNamespace(),
		SecurityDomainId:                 testSecurityDomain(),
		RegistrationId:                   "ledger-registration-" + seed,
		ProviderCapabilitySnapshotDigest: fixedDigest("ledger-snapshot-" + seed),
		ConformanceEvidenceDigests:       []string{fixedDigest("ledger-evidence-" + seed)},
		Attestation:                      testAttestation(),
		TaskId:                           "ledger-task-" + seed,
		RunId:                            "ledger-run-" + seed,
		AttemptId:                        "ledger-attempt-" + seed,
		AllocationId:                     "ledger-allocation-" + seed,
		Generation:                       1,
		AckDeadlineAt:                    "2026-01-01T00:30:00Z",
		ExpiresAt:                        "2026-01-01T02:00:00Z",
		LeaseState:                       LeaseStateActive,
		CreatedAt:                        "2026-01-01T00:00:00Z",
	}
	if err := sealLease(&lease); err != nil {
		panic(err)
	}
	return lease
}

// rivalLease keeps the identical (runId, attemptId) binding as lease while
// carrying a different leaseId: the double-active probe fixture.
func rivalLease(lease DispatchLease, seed string) DispatchLease {
	rival := lease
	rival.LeaseId = fixedDigest("ledger-rival-" + seed)
	if err := sealLease(&rival); err != nil {
		panic(err)
	}
	return rival
}

// newTestLeaseLedger binds a fresh durable ledger under t.TempDir().
func newTestLeaseLedger(t *testing.T) *LeaseLedger {
	t.Helper()
	ledger, err := NewLeaseLedger(t.TempDir())
	if err != nil {
		t.Fatalf("NewLeaseLedger rejected a fresh directory: %v", err)
	}
	return ledger
}

// TestLeaseLedgerClaimCurrentActive freezes requirement (1): an accepted
// claim becomes visible as an active lease at generation 1 with the exact
// claimed snapshot.
func TestLeaseLedgerClaimCurrentActive(t *testing.T) {
	ledger := newTestLeaseLedger(t)
	lease := ledgerLease("claim-" + "1")
	if err := ledger.AppendClaim(lease); err != nil {
		t.Fatalf("AppendClaim rejected a valid lease: %v", err)
	}
	current, state, generation, err := ledger.Current(lease.LeaseId)
	if err != nil {
		t.Fatalf("Current rejected the claimed lease: %v", err)
	}
	if state != LeaseStateActive {
		t.Fatalf("Current must return the active leaseState, got %q", string(state))
	}
	if generation != 1 {
		t.Fatalf("Current must return generation 1 after the claim, got %d", generation)
	}
	if !reflect.DeepEqual(current, lease) {
		t.Fatal("Current must return the exact claimed snapshot")
	}
	if err := current.Validate(); err != nil {
		t.Fatalf("the current snapshot does not validate: %v", err)
	}
	if _, _, _, err := ledger.Current(fixedDigest("unknown-" + "lease")); !errors.Is(err, ErrUnknownLease) {
		t.Fatalf("Current must fail closed on an unknown leaseId with ErrUnknownLease, got %v", err)
	}
}

// TestLeaseLedgerClaimRejectsInvalidLease guards the claim preconditions:
// tampered snapshots and terminal initial states fail closed.
func TestLeaseLedgerClaimRejectsInvalidLease(t *testing.T) {
	ledger := newTestLeaseLedger(t)
	tampered := ledgerLease("invalid-" + "1")
	tampered.LeaseDigest = fixedDigest("forged-" + "digest")
	if err := ledger.AppendClaim(tampered); err == nil {
		t.Fatal("AppendClaim accepted a lease with a tampered leaseDigest")
	} else if !strings.Contains(err.Error(), "claim rejected") {
		t.Fatalf("expected the fail-closed claim rejection, got: %v", err)
	}
	terminal := ledgerLease("invalid-" + "2")
	terminal.LeaseState = LeaseStateExpired
	if err := sealLease(&terminal); err != nil {
		t.Fatalf("sealLease failed on the terminal fixture: %v", err)
	}
	if err := ledger.AppendClaim(terminal); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("AppendClaim must reject a terminal initial leaseState with ErrLeaseConflict, got %v", err)
	}
}

// TestLeaseLedgerDoubleActiveClaimRejected freezes requirement (2): the
// single-active invariant rejects a second live lease on the identical
// (runId, attemptId) binding and any duplicate leaseId, while a terminal
// lease releases the binding for a fresh attempt.
func TestLeaseLedgerDoubleActiveClaimRejected(t *testing.T) {
	ledger := newTestLeaseLedger(t)
	first := ledgerLease("binding-" + "1")
	if err := ledger.AppendClaim(first); err != nil {
		t.Fatalf("AppendClaim rejected the first claim: %v", err)
	}
	rival := rivalLease(first, "1")
	if err := ledger.AppendClaim(rival); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("AppendClaim must reject a second active lease on the identical (runId, attemptId) binding with ErrLeaseConflict, got %v", err)
	}
	if err := ledger.AppendClaim(first); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("AppendClaim must reject a duplicate leaseId with ErrLeaseConflict, got %v", err)
	}
	if _, _, _, err := ledger.Current(rival.LeaseId); !errors.Is(err, ErrUnknownLease) {
		t.Fatal("the rejected rival claim must never become visible in the ledger")
	}
	if err := ledger.AppendCancel(first.LeaseId, CancelReasonDeadlineExceeded, 1); err != nil {
		t.Fatalf("AppendCancel failed: %v", err)
	}
	retry := rivalLease(first, "2")
	if err := ledger.AppendClaim(retry); err != nil {
		t.Fatalf("AppendClaim must accept a fresh claim once the previous lease turned terminal: %v", err)
	}
}

// TestLeaseLedgerTerminalLeasesRejectFurtherOperations freezes requirement
// (3): cancel and expire move the lease to the terminal state at the
// bumped generation, and every further transition fails closed.
func TestLeaseLedgerTerminalLeasesRejectFurtherOperations(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		ledger := newTestLeaseLedger(t)
		lease := ledgerLease("cancel-" + "1")
		if err := ledger.AppendClaim(lease); err != nil {
			t.Fatalf("AppendClaim failed: %v", err)
		}
		if err := ledger.AppendCancel(lease.LeaseId, CancelReasonEvidenceRevoked, 1); err != nil {
			t.Fatalf("AppendCancel rejected the current generation: %v", err)
		}
		current, state, generation, err := ledger.Current(lease.LeaseId)
		if err != nil {
			t.Fatalf("Current rejected the cancelled lease: %v", err)
		}
		if state != LeaseStateCancelled {
			t.Fatalf("Current must return the cancelled leaseState, got %q", string(state))
		}
		if generation != 2 {
			t.Fatalf("cancel must bump the generation to 2, got %d", generation)
		}
		if current.CancelReason != CancelReasonEvidenceRevoked {
			t.Fatalf("the cancelled snapshot must carry the closed cancelReason, got %q", string(current.CancelReason))
		}
		if err := current.Validate(); err != nil {
			t.Fatalf("the cancelled snapshot does not validate: %v", err)
		}
		for _, op := range []struct {
			name string
			run  func() error
		}{
			{"cancel", func() error { return ledger.AppendCancel(lease.LeaseId, CancelReasonDeadlineExceeded, 2) }},
			{"expire", func() error { return ledger.AppendExpire(lease.LeaseId, 2) }},
			{"bump", func() error { _, _, err := ledger.BumpGeneration(lease.LeaseId, 2); return err }},
		} {
			if err := op.run(); !errors.Is(err, ErrLeaseConflict) {
				t.Fatalf("%s after cancel must fail closed with ErrLeaseConflict, got %v", op.name, err)
			}
		}
	})
	t.Run("expire", func(t *testing.T) {
		ledger := newTestLeaseLedger(t)
		lease := ledgerLease("expire-" + "1")
		if err := ledger.AppendClaim(lease); err != nil {
			t.Fatalf("AppendClaim failed: %v", err)
		}
		if err := ledger.AppendExpire(lease.LeaseId, 1); err != nil {
			t.Fatalf("AppendExpire rejected the current generation: %v", err)
		}
		current, state, generation, err := ledger.Current(lease.LeaseId)
		if err != nil {
			t.Fatalf("Current rejected the expired lease: %v", err)
		}
		if state != LeaseStateExpired {
			t.Fatalf("Current must return the expired leaseState, got %q", string(state))
		}
		if generation != 2 {
			t.Fatalf("expire must bump the generation to 2, got %d", generation)
		}
		if current.CancelReason != "" {
			t.Fatalf("the expired snapshot must carry no cancelReason, got %q", string(current.CancelReason))
		}
		if err := current.Validate(); err != nil {
			t.Fatalf("the expired snapshot does not validate: %v", err)
		}
		for _, op := range []struct {
			name string
			run  func() error
		}{
			{"cancel", func() error { return ledger.AppendCancel(lease.LeaseId, CancelReasonDeadlineExceeded, 2) }},
			{"expire", func() error { return ledger.AppendExpire(lease.LeaseId, 2) }},
			{"bump", func() error { _, _, err := ledger.BumpGeneration(lease.LeaseId, 2); return err }},
		} {
			if err := op.run(); !errors.Is(err, ErrLeaseConflict) {
				t.Fatalf("%s after expire must fail closed with ErrLeaseConflict, got %v", op.name, err)
			}
		}
	})
}

// TestLeaseLedgerCompareAndAppendGeneration freezes requirement (4): a
// stale expectedGeneration is rejected with ErrLeaseGenerationConflict on
// every transition without touching the state, and a matching
// expectedGeneration bumps the generation with a deterministic
// fencingToken.
func TestLeaseLedgerCompareAndAppendGeneration(t *testing.T) {
	for _, staleGeneration := range []int64{0, 2, 99} {
		t.Run("stale generation "+strconv.FormatInt(staleGeneration, 10), func(t *testing.T) {
			ledger := newTestLeaseLedger(t)
			lease := ledgerLease("cas-stale-" + strconv.FormatInt(staleGeneration, 10))
			if err := ledger.AppendClaim(lease); err != nil {
				t.Fatalf("AppendClaim failed: %v", err)
			}
			if err := ledger.AppendCancel(lease.LeaseId, CancelReasonDeadlineExceeded, staleGeneration); !errors.Is(err, ErrLeaseGenerationConflict) {
				t.Fatalf("AppendCancel must reject the stale generation with ErrLeaseGenerationConflict, got %v", err)
			}
			if err := ledger.AppendExpire(lease.LeaseId, staleGeneration); !errors.Is(err, ErrLeaseGenerationConflict) {
				t.Fatalf("AppendExpire must reject the stale generation with ErrLeaseGenerationConflict, got %v", err)
			}
			if _, _, err := ledger.BumpGeneration(lease.LeaseId, staleGeneration); !errors.Is(err, ErrLeaseGenerationConflict) {
				t.Fatalf("BumpGeneration must reject the stale generation with ErrLeaseGenerationConflict, got %v", err)
			}
			_, state, generation, err := ledger.Current(lease.LeaseId)
			if err != nil {
				t.Fatalf("Current failed: %v", err)
			}
			if state != LeaseStateActive || generation != 1 {
				t.Fatalf("rejected compare-and-append attempts must leave the lease untouched, got %q/%d", string(state), generation)
			}
		})
	}
	t.Run("matching bump", func(t *testing.T) {
		first := newTestLeaseLedger(t)
		lease := ledgerLease("cas-" + "bump")
		if err := first.AppendClaim(lease); err != nil {
			t.Fatalf("AppendClaim failed: %v", err)
		}
		newGeneration, newFencingToken, err := first.BumpGeneration(lease.LeaseId, 1)
		if err != nil {
			t.Fatalf("BumpGeneration rejected the matching generation: %v", err)
		}
		if newGeneration != 2 {
			t.Fatalf("BumpGeneration must return expectedGeneration+1, got %d", newGeneration)
		}
		if newFencingToken == lease.FencingToken {
			t.Fatal("the generation bump must derive a fresh fencingToken")
		}
		current, state, generation, err := first.Current(lease.LeaseId)
		if err != nil {
			t.Fatalf("Current failed: %v", err)
		}
		if state != LeaseStateActive || generation != 2 {
			t.Fatalf("the bumped lease must stay active at generation 2, got %q/%d", string(state), generation)
		}
		if current.FencingToken != newFencingToken {
			t.Fatal("the current snapshot must carry the bumped fencingToken")
		}
		if err := ValidateLeaseFencing(current, newGeneration, newFencingToken); err != nil {
			t.Fatalf("the fencing guard rejected the bumped snapshot: %v", err)
		}
		expectedToken, err := bumpedLeaseFencingToken(lease.LeaseId, 2)
		if err != nil {
			t.Fatalf("bumpedLeaseFencingToken failed: %v", err)
		}
		if newFencingToken != expectedToken {
			t.Fatal("the fencingToken must equal the deterministic leaseId+generation derivation")
		}
		second := newTestLeaseLedger(t)
		if err := second.AppendClaim(lease); err != nil {
			t.Fatalf("AppendClaim failed on the second ledger: %v", err)
		}
		_, secondToken, err := second.BumpGeneration(lease.LeaseId, 1)
		if err != nil {
			t.Fatalf("BumpGeneration failed on the second ledger: %v", err)
		}
		if secondToken != newFencingToken {
			t.Fatal("the identical bump on the identical lease must derive the identical fencingToken on every ledger")
		}
	})
	t.Run("matching cancel and expire", func(t *testing.T) {
		ledger := newTestLeaseLedger(t)
		cancelLease := ledgerLease("cas-" + "cancel")
		if err := ledger.AppendClaim(cancelLease); err != nil {
			t.Fatalf("AppendClaim failed: %v", err)
		}
		if err := ledger.AppendCancel(cancelLease.LeaseId, CancelReasonSnapshotExpired, 1); err != nil {
			t.Fatalf("AppendCancel rejected the matching generation: %v", err)
		}
		if _, _, generation, err := ledger.Current(cancelLease.LeaseId); err != nil || generation != 2 {
			t.Fatalf("cancel with the matching generation must bump to 2, got %d (%v)", generation, err)
		}
		expireLease := ledgerLease("cas-" + "expire")
		if err := ledger.AppendClaim(expireLease); err != nil {
			t.Fatalf("AppendClaim failed: %v", err)
		}
		if err := ledger.AppendExpire(expireLease.LeaseId, 1); err != nil {
			t.Fatalf("AppendExpire rejected the matching generation: %v", err)
		}
		if _, _, generation, err := ledger.Current(expireLease.LeaseId); err != nil || generation != 2 {
			t.Fatalf("expire with the matching generation must bump to 2, got %d (%v)", generation, err)
		}
	})
}

// TestLeaseLedgerCrashRecoveryRebuildsState freezes requirement (5): after
// a restart NewLeaseLedger replays the ledger and rebuilds the complete
// current state — snapshots, states, generations, terminal facts, the
// single-active bindings and the compare-and-append continuity.
func TestLeaseLedgerCrashRecoveryRebuildsState(t *testing.T) {
	dir := t.TempDir()
	ledger, err := NewLeaseLedger(dir)
	if err != nil {
		t.Fatalf("NewLeaseLedger failed: %v", err)
	}
	bumped := ledgerLease("recover-" + "bumped")
	cancelled := ledgerLease("recover-" + "cancelled")
	expired := ledgerLease("recover-" + "expired")
	active := ledgerLease("recover-" + "active")
	for _, lease := range []DispatchLease{bumped, cancelled, expired, active} {
		if err := ledger.AppendClaim(lease); err != nil {
			t.Fatalf("AppendClaim failed: %v", err)
		}
	}
	if _, _, err := ledger.BumpGeneration(bumped.LeaseId, 1); err != nil {
		t.Fatalf("BumpGeneration failed: %v", err)
	}
	lastToken := ""
	if _, lastToken, err = ledger.BumpGeneration(bumped.LeaseId, 2); err != nil {
		t.Fatalf("BumpGeneration failed: %v", err)
	}
	if err := ledger.AppendCancel(cancelled.LeaseId, CancelReasonSnapshotSuperseded, 1); err != nil {
		t.Fatalf("AppendCancel failed: %v", err)
	}
	if err := ledger.AppendExpire(expired.LeaseId, 1); err != nil {
		t.Fatalf("AppendExpire failed: %v", err)
	}

	recovered, err := NewLeaseLedger(dir)
	if err != nil {
		t.Fatalf("NewLeaseLedger failed to recover the existing ledger: %v", err)
	}
	expectations := []struct {
		lease      DispatchLease
		state      LeaseState
		generation int64
	}{
		{bumped, LeaseStateActive, 3},
		{cancelled, LeaseStateCancelled, 2},
		{expired, LeaseStateExpired, 2},
		{active, LeaseStateActive, 1},
	}
	for _, expected := range expectations {
		current, state, generation, err := recovered.Current(expected.lease.LeaseId)
		if err != nil {
			t.Fatalf("Current failed after recovery: %v", err)
		}
		if state != expected.state || generation != expected.generation {
			t.Fatalf("recovery must rebuild %q at generation %d, got %q/%d", string(expected.state), expected.generation, string(state), generation)
		}
		original, originalState, originalGeneration, err := ledger.Current(expected.lease.LeaseId)
		if err != nil {
			t.Fatalf("Current failed on the pre-restart ledger: %v", err)
		}
		if state != originalState || generation != originalGeneration {
			t.Fatal("recovered state must equal the pre-restart state")
		}
		if !reflect.DeepEqual(current, original) {
			t.Fatal("recovery must rebuild the exact pre-restart lease snapshot")
		}
	}
	recoveredBumped, _, _, err := recovered.Current(bumped.LeaseId)
	if err != nil {
		t.Fatalf("Current failed: %v", err)
	}
	if recoveredBumped.FencingToken != lastToken {
		t.Fatal("recovery must rebuild the fencingToken of the last generation bump")
	}
	recoveredCancelled, _, _, err := recovered.Current(cancelled.LeaseId)
	if err != nil {
		t.Fatalf("Current failed: %v", err)
	}
	if recoveredCancelled.CancelReason != CancelReasonSnapshotSuperseded {
		t.Fatalf("recovery must rebuild the terminal cancelReason, got %q", string(recoveredCancelled.CancelReason))
	}
	if err := recovered.AppendClaim(rivalLease(active, "1")); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("recovery must rebuild the single-active binding: a rival claim must fail with ErrLeaseConflict, got %v", err)
	}
	if err := recovered.AppendClaim(rivalLease(cancelled, "2")); err != nil {
		t.Fatalf("recovery must release terminal bindings for a fresh claim: %v", err)
	}
	if _, _, err := recovered.BumpGeneration(bumped.LeaseId, 2); !errors.Is(err, ErrLeaseGenerationConflict) {
		t.Fatalf("recovery must rebuild compare-and-append continuity: the stale generation must fail with ErrLeaseGenerationConflict, got %v", err)
	}
	newGeneration, _, err := recovered.BumpGeneration(bumped.LeaseId, 3)
	if err != nil {
		t.Fatalf("BumpGeneration failed after recovery: %v", err)
	}
	if newGeneration != 4 {
		t.Fatalf("the post-recovery bump must continue the sequence at generation 4, got %d", newGeneration)
	}
}

// TestLeaseLedgerCorruptLineFailClosed freezes requirement (6): any corrupt
// ledger line fails closed at construction; nothing is silently skipped.
func TestLeaseLedgerCorruptLineFailClosed(t *testing.T) {
	source := newTestLeaseLedger(t)
	lease := ledgerLease("corrupt-" + "1")
	if err := source.AppendClaim(lease); err != nil {
		t.Fatalf("AppendClaim failed: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(source.dir, leaseLedgerFileName))
	if err != nil {
		t.Fatalf("read the source ledger: %v", err)
	}
	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("the source ledger must carry exactly one line, got %d", len(lines))
	}
	valid := lines[0]

	// JCS sorts keys alphabetically, so the canonical claim line starts with
	// the digest binding: {"digest" precedes factType, lease and sequence.
	spaced := bytes.Replace(valid, []byte(`{"digest"`), []byte(`{ "digest"`), 1)
	if bytes.Equal(spaced, valid) {
		t.Fatal("the non canonical spacing mutation is ineffective: the target bytes do not exist in the canonical line")
	}

	digestMutated := append([]byte(nil), valid...)
	digestMark := []byte(`"digest":"` + provider.DigestPrefix)
	offset := bytes.Index(digestMutated, digestMark)
	if offset < 0 {
		t.Fatal("the canonical claim line must carry its digest binding")
	}
	flipPosition := offset + len(digestMark) + 3
	if digestMutated[flipPosition] == 'a' {
		digestMutated[flipPosition] = 'b'
	} else {
		digestMutated[flipPosition] = 'a'
	}
	if bytes.Equal(digestMutated, valid) {
		t.Fatal("the digest mutation is ineffective")
	}

	retyped := bytes.Replace(valid, []byte(`"factType":"lease-claimed"`), []byte(`"factType":"lease-drained"`), 1)
	if bytes.Equal(retyped, valid) {
		t.Fatal("the factType mutation is ineffective")
	}

	garbage := append(append([]byte(nil), valid...), '\n')
	garbage = append(garbage, []byte("this is not a ledger line")...)
	garbage = append(garbage, '\n')

	cases := []struct {
		name    string
		content []byte
		detail  string
	}{
		{"non canonical spacing", spaced, "canonical"},
		{"digest binding mismatch", digestMutated, "digest"},
		{"unknown factType", retyped, "factType"},
		{"garbage line", garbage, "rejected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, leaseLedgerFileName), tc.content, 0o600); err != nil {
				t.Fatalf("write the corrupt ledger: %v", err)
			}
			if _, err := NewLeaseLedger(dir); err == nil {
				t.Fatalf("NewLeaseLedger accepted a corrupt ledger (%s)", tc.name)
			} else if !strings.Contains(err.Error(), tc.detail) {
				t.Fatalf("expected the %q rejection detail, got: %v", tc.detail, err)
			}
		})
	}
}

// TestLeaseLedgerMemoryOnlyRejected freezes requirement (7): an unbound
// ledger — the empty-directory construction, the zero value or a nil
// receiver — rejects every read and write operation fail closed.
func TestLeaseLedgerMemoryOnlyRejected(t *testing.T) {
	emptyDir, err := NewLeaseLedger("")
	if err != nil {
		t.Fatalf("NewLeaseLedger must stay constructible without a directory: %v", err)
	}
	ledgers := map[string]*LeaseLedger{
		"empty directory": emptyDir,
		"zero value":      {},
		"nil receiver":    nil,
	}
	lease := ledgerLease("memory-" + "only")
	for name, ledger := range ledgers {
		t.Run(name, func(t *testing.T) {
			if err := ledger.AppendClaim(lease); !errors.Is(err, ErrMemoryOnlyLeaseLedger) {
				t.Fatalf("AppendClaim must report the memory-only lease ledger, got %v", err)
			}
			if err := ledger.AppendCancel(lease.LeaseId, CancelReasonDeadlineExceeded, 1); !errors.Is(err, ErrMemoryOnlyLeaseLedger) {
				t.Fatalf("AppendCancel must report the memory-only lease ledger, got %v", err)
			}
			if err := ledger.AppendExpire(lease.LeaseId, 1); !errors.Is(err, ErrMemoryOnlyLeaseLedger) {
				t.Fatalf("AppendExpire must report the memory-only lease ledger, got %v", err)
			}
			if _, _, err := ledger.BumpGeneration(lease.LeaseId, 1); !errors.Is(err, ErrMemoryOnlyLeaseLedger) {
				t.Fatalf("BumpGeneration must report the memory-only lease ledger, got %v", err)
			}
			if _, _, _, err := ledger.Current(lease.LeaseId); !errors.Is(err, ErrMemoryOnlyLeaseLedger) {
				t.Fatalf("Current must report the memory-only lease ledger, got %v", err)
			}
		})
	}
}

// TestLeaseLedgerReplayDeterminism freezes requirement (8): replaying the
// identical ledger bytes twice rebuilds field-identical state.
func TestLeaseLedgerReplayDeterminism(t *testing.T) {
	source := newTestLeaseLedger(t)
	bumped := ledgerLease("determinism-" + "bumped")
	cancelled := ledgerLease("determinism-" + "cancelled")
	expired := ledgerLease("determinism-" + "expired")
	active := ledgerLease("determinism-" + "active")
	for _, lease := range []DispatchLease{bumped, cancelled, expired, active} {
		if err := source.AppendClaim(lease); err != nil {
			t.Fatalf("AppendClaim failed: %v", err)
		}
	}
	if _, _, err := source.BumpGeneration(bumped.LeaseId, 1); err != nil {
		t.Fatalf("BumpGeneration failed: %v", err)
	}
	if _, _, err := source.BumpGeneration(bumped.LeaseId, 2); err != nil {
		t.Fatalf("BumpGeneration failed: %v", err)
	}
	if err := source.AppendCancel(cancelled.LeaseId, CancelReasonEvidenceExpired, 1); err != nil {
		t.Fatalf("AppendCancel failed: %v", err)
	}
	if err := source.AppendExpire(expired.LeaseId, 1); err != nil {
		t.Fatalf("AppendExpire failed: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(source.dir, leaseLedgerFileName))
	if err != nil {
		t.Fatalf("read the source ledger: %v", err)
	}
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	for _, dir := range []string{firstDir, secondDir} {
		if err := os.WriteFile(filepath.Join(dir, leaseLedgerFileName), raw, 0o600); err != nil {
			t.Fatalf("write the replay ledger: %v", err)
		}
	}
	first, err := NewLeaseLedger(firstDir)
	if err != nil {
		t.Fatalf("NewLeaseLedger failed on the first replay: %v", err)
	}
	second, err := NewLeaseLedger(secondDir)
	if err != nil {
		t.Fatalf("NewLeaseLedger failed on the second replay: %v", err)
	}
	for _, leaseId := range []string{bumped.LeaseId, cancelled.LeaseId, expired.LeaseId, active.LeaseId} {
		firstLease, firstState, firstGeneration, err := first.Current(leaseId)
		if err != nil {
			t.Fatalf("Current failed on the first replay: %v", err)
		}
		secondLease, secondState, secondGeneration, err := second.Current(leaseId)
		if err != nil {
			t.Fatalf("Current failed on the second replay: %v", err)
		}
		if firstState != secondState {
			t.Fatalf("the two replays disagree on the leaseState of %s: %q vs %q", leaseId, string(firstState), string(secondState))
		}
		if firstGeneration != secondGeneration {
			t.Fatalf("the two replays disagree on the generation of %s: %d vs %d", leaseId, firstGeneration, secondGeneration)
		}
		if !reflect.DeepEqual(firstLease, secondLease) {
			t.Fatalf("replaying the identical ledger bytes must rebuild field-identical lease snapshots for %s", leaseId)
		}
	}
}

// TestLeaseLedgerConcurrentSerialization freezes requirement (9):
// concurrent AppendClaim and BumpGeneration serialize correctly — the
// append-only line order and the compare-and-append semantics never tear.
func TestLeaseLedgerConcurrentSerialization(t *testing.T) {
	dir := t.TempDir()
	ledger, err := NewLeaseLedger(dir)
	if err != nil {
		t.Fatalf("NewLeaseLedger failed: %v", err)
	}
	anchor := ledgerLease("race-" + "anchor")
	if err := ledger.AppendClaim(anchor); err != nil {
		t.Fatalf("AppendClaim failed: %v", err)
	}

	var unexpected []error
	var unexpectedMu sync.Mutex
	record := func(err error) {
		unexpectedMu.Lock()
		unexpected = append(unexpected, err)
		unexpectedMu.Unlock()
	}

	const bumperCount = 8
	const bumpsPerBumper = 5
	var totalBumps int64
	var wg sync.WaitGroup
	for bumper := 0; bumper < bumperCount; bumper++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			completed := 0
			for completed < bumpsPerBumper {
				_, _, generation, err := ledger.Current(anchor.LeaseId)
				if err != nil {
					record(err)
					return
				}
				_, _, err = ledger.BumpGeneration(anchor.LeaseId, generation)
				if err == nil {
					completed++
					atomic.AddInt64(&totalBumps, 1)
					continue
				}
				if !errors.Is(err, ErrLeaseGenerationConflict) {
					record(err)
					return
				}
			}
		}()
	}

	const claimerCount = 8
	claimLeases := make([]DispatchLease, claimerCount)
	for index := range claimLeases {
		claimLeases[index] = ledgerLease("race-claim-" + strconv.Itoa(index))
	}
	var distinctClaims int64
	for _, candidate := range claimLeases {
		wg.Add(1)
		go func(candidate DispatchLease) {
			defer wg.Done()
			if err := ledger.AppendClaim(candidate); err != nil {
				record(err)
				return
			}
			atomic.AddInt64(&distinctClaims, 1)
		}(candidate)
	}

	baseRival := ledgerLease("race-" + "rival")
	var rivalWins int64
	for rival := 0; rival < 4; rival++ {
		wg.Add(1)
		candidate := rivalLease(baseRival, strconv.Itoa(rival))
		go func(candidate DispatchLease) {
			defer wg.Done()
			err := ledger.AppendClaim(candidate)
			if err == nil {
				atomic.AddInt64(&rivalWins, 1)
				return
			}
			if !errors.Is(err, ErrLeaseConflict) {
				record(err)
			}
		}(candidate)
	}
	wg.Wait()

	if len(unexpected) != 0 {
		t.Fatalf("unexpected concurrent errors: %v", unexpected)
	}
	if totalBumps != bumperCount*bumpsPerBumper {
		t.Fatalf("every serialized bump must succeed exactly once, got %d of %d", totalBumps, bumperCount*bumpsPerBumper)
	}
	if distinctClaims != claimerCount {
		t.Fatalf("every distinct claim must succeed, got %d of %d", distinctClaims, claimerCount)
	}
	if rivalWins != 1 {
		t.Fatalf("exactly one rival claim may win the single-active invariant, got %d", rivalWins)
	}
	if _, _, generation, err := ledger.Current(anchor.LeaseId); err != nil || generation != 1+bumperCount*bumpsPerBumper {
		t.Fatalf("the anchor lease must carry generation %d after the serialized bumps, got %d (%v)", 1+bumperCount*bumpsPerBumper, generation, err)
	}

	recovered, err := NewLeaseLedger(dir)
	if err != nil {
		t.Fatalf("recovery rejected the concurrently written ledger: the line order is torn: %v", err)
	}
	if _, _, recoveredGeneration, err := recovered.Current(anchor.LeaseId); err != nil || recoveredGeneration != 1+bumperCount*bumpsPerBumper {
		t.Fatalf("recovery must rebuild the bumped generation %d, got %d (%v)", 1+bumperCount*bumpsPerBumper, recoveredGeneration, err)
	}
	for _, candidate := range claimLeases {
		if _, state, generation, err := recovered.Current(candidate.LeaseId); err != nil || state != LeaseStateActive || generation != 1 {
			t.Fatalf("recovery must rebuild every concurrent claim as active at generation 1, got %q/%d (%v)", string(state), generation, err)
		}
	}
	if err := recovered.AppendClaim(rivalLease(baseRival, "post")); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("recovery must rebuild the rival binding as occupied: a further rival claim must fail with ErrLeaseConflict, got %v", err)
	}
}
