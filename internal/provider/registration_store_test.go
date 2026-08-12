package provider

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ledgerLineCount counts the non-empty append-only fact lines currently in
// the store ledger under dir.
func ledgerLineCount(t *testing.T, dir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ledgerFileName))
	if err != nil {
		t.Fatalf("read ledger %s: %v", ledgerFileName, err)
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// mustPut admits registration into store, failing the test on any error.
func mustPut(t *testing.T, store *RegistrationStore, registration ProviderRegistration) ProviderRegistration {
	t.Helper()
	admitted, err := store.Put(registration)
	if err != nil {
		t.Fatalf("Put rejected a valid registration: %v", err)
	}
	return admitted
}

// newTestStore opens a durable registration store rooted in a fresh
// t.TempDir() ledger directory, failing the test on any error.
func newTestStore(t *testing.T) (*RegistrationStore, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewRegistrationStore(dir)
	if err != nil {
		t.Fatalf("NewRegistrationStore: %v", err)
	}
	return store, dir
}

// TestRegistrationStoreRejectsMemoryOnly freezes negative fixture (1): a
// store that is not bound to a durable ledger directory never accepts a
// registration, and construction rejects a blank directory fail closed.
func TestRegistrationStoreRejectsMemoryOnly(t *testing.T) {
	registration := validRegistration()

	unbound := &RegistrationStore{}
	if _, err := unbound.Put(registration); err == nil {
		t.Fatal("Put accepted a registration into a memory-only store")
	} else if !strings.Contains(err.Error(), "memory-only registration not allowed") {
		t.Fatalf("expected the memory-only rejection, got: %v", err)
	}
	if _, err := unbound.Get(registration.RegistrationId); err == nil {
		t.Fatal("Get served an unbound memory-only store")
	}
	if err := unbound.Revoke(registration.RegistrationId); err == nil {
		t.Fatal("Revoke served an unbound memory-only store")
	}
	if err := unbound.Expire(registration.RegistrationId); err == nil {
		t.Fatal("Expire served an unbound memory-only store")
	}

	var nilStore *RegistrationStore
	if _, err := nilStore.Put(registration); err == nil || !strings.Contains(err.Error(), "memory-only registration not allowed") {
		t.Fatalf("Put accepted a registration into a nil store, got: %v", err)
	}

	for _, dir := range []string{"", "   "} {
		if _, err := NewRegistrationStore(dir); err == nil {
			t.Fatalf("NewRegistrationStore accepted the blank ledger directory %q", dir)
		}
	}
}

// TestRegistrationStoreRestartRecovery freezes negative fixture (2): a store
// reconstructed from the same ledger directory rebuilds every admitted
// registration, its terminal lifecycle state and its replay protection.
func TestRegistrationStoreRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	store, err := NewRegistrationStore(dir)
	if err != nil {
		t.Fatalf("NewRegistrationStore: %v", err)
	}

	active := validRegistration()
	revoked := validRegistration()
	revoked.RegistrationId = "registration-2"
	revoked.IdempotencyKey = "idempotency-key-2"
	revoked.RequestDigest = fixedDigest("registration-request-2")
	setRegistrationDigest(&revoked)

	mustPut(t, store, active)
	mustPut(t, store, revoked)
	if err := store.Revoke(revoked.RegistrationId); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	recovered, err := NewRegistrationStore(dir)
	if err != nil {
		t.Fatalf("NewRegistrationStore after restart: %v", err)
	}

	gotActive, err := recovered.Get(active.RegistrationId)
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if !reflect.DeepEqual(gotActive, active) {
		t.Fatalf("recovered registration differs from the admitted record:\n got %+v\nwant %+v", gotActive, active)
	}

	gotRevoked, err := recovered.Get(revoked.RegistrationId)
	if err != nil {
		t.Fatalf("Get revoked after restart: %v", err)
	}
	if gotRevoked.LifecycleState != LifecycleStateRevoked {
		t.Fatalf("expected lifecycleState revoked after restart, got %q", string(gotRevoked.LifecycleState))
	}

	// Replay protection survives the restart: the identical replay merges
	// without appending, the revoked replay stays rejected and a reused
	// registrationId with a different requestDigest still conflicts.
	merged, err := recovered.Put(active)
	if err != nil {
		t.Fatalf("idempotent replay after restart rejected: %v", err)
	}
	if !reflect.DeepEqual(merged, gotActive) {
		t.Fatal("idempotent replay after restart returned a different record")
	}
	if _, err := recovered.Put(revoked); err == nil {
		t.Fatal("ordinary replay resurrected a revoked registration after restart")
	}
	conflicting := validRegistration()
	conflicting.RequestDigest = fixedDigest("registration-request-conflicting")
	setRegistrationDigest(&conflicting)
	if _, err := recovered.Put(conflicting); err == nil {
		t.Fatal("same identity with a different requestDigest did not conflict after restart")
	}
	if count := ledgerLineCount(t, dir); count != 3 {
		t.Fatalf("restart replays must not append new facts; ledger has %d lines", count)
	}
	if _, err := recovered.Get("registration-unknown"); err == nil {
		t.Fatal("Get accepted an unknown registrationId after restart")
	}
}

// TestRegistrationStoreMergesIdenticalReplay freezes negative fixture (3):
// the identical registration merges idempotently and never appends a second
// copy of the same fact.
func TestRegistrationStoreMergesIdenticalReplay(t *testing.T) {
	store, dir := newTestStore(t)
	registration := validRegistration()

	first := mustPut(t, store, registration)
	if count := ledgerLineCount(t, dir); count != 1 {
		t.Fatalf("first admission must append exactly one fact, got %d lines", count)
	}
	second := mustPut(t, store, registration)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("idempotent merge returned a different record:\nfirst %+v\nsecond %+v", first, second)
	}
	if !reflect.DeepEqual(first, registration) {
		t.Fatal("idempotent merge must return the existing admitted record")
	}
	if count := ledgerLineCount(t, dir); count != 1 {
		t.Fatalf("idempotent merge must not append a second fact; ledger has %d lines", count)
	}
}

// TestRegistrationStoreRejectsSameKeyDifferentDigest freezes negative
// fixture (4): the identical septuple identity and idempotencyKey with a
// different requestDigest is a conflict; it never merges and never
// overwrites the existing record.
func TestRegistrationStoreRejectsSameKeyDifferentDigest(t *testing.T) {
	store, dir := newTestStore(t)
	registration := validRegistration()
	mustPut(t, store, registration)

	conflicting := validRegistration()
	conflicting.RequestDigest = fixedDigest("registration-request-conflicting")
	setRegistrationDigest(&conflicting)
	if _, err := store.Put(conflicting); err == nil {
		t.Fatal("Put merged identical identity and idempotencyKey with a different requestDigest")
	} else if !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("expected an idempotency conflict rejection, got: %v", err)
	}

	got, err := store.Get(registration.RegistrationId)
	if err != nil {
		t.Fatalf("Get after the conflict: %v", err)
	}
	if !reflect.DeepEqual(got, registration) {
		t.Fatal("the conflicting replay modified the existing registration")
	}
	if count := ledgerLineCount(t, dir); count != 1 {
		t.Fatalf("the conflicting replay must not append; ledger has %d lines", count)
	}
}

// TestRegistrationStoreTerminalStatesSurviveReplay freezes negative fixtures
// (5) and (6): revoked and expired registrations are terminal; no ordinary
// replay resurrects them, before or after a restart.
func TestRegistrationStoreTerminalStatesSurviveReplay(t *testing.T) {
	for _, terminal := range []struct {
		state      LifecycleState
		transition func(*RegistrationStore, string) error
	}{
		{LifecycleStateRevoked, (*RegistrationStore).Revoke},
		{LifecycleStateExpired, (*RegistrationStore).Expire},
	} {
		t.Run(string(terminal.state), func(t *testing.T) {
			store, dir := newTestStore(t)
			registration := validRegistration()
			mustPut(t, store, registration)

			if err := terminal.transition(store, registration.RegistrationId); err != nil {
				t.Fatalf("%s: %v", string(terminal.state), err)
			}
			got, err := store.Get(registration.RegistrationId)
			if err != nil {
				t.Fatalf("Get after %s: %v", string(terminal.state), err)
			}
			if got.LifecycleState != terminal.state {
				t.Fatalf("expected lifecycleState %s, got %q", string(terminal.state), string(got.LifecycleState))
			}

			if _, err := store.Put(registration); err == nil {
				t.Fatalf("ordinary replay resurrected a %s registration", string(terminal.state))
			}
			createReplay := validRegistration()
			createReplay.LifecycleState = LifecycleStateCreate
			setRegistrationDigest(&createReplay)
			if _, err := store.Put(createReplay); err == nil {
				t.Fatalf("a create replay was accepted against a %s registration", string(terminal.state))
			}
			stillTerminal, err := store.Get(registration.RegistrationId)
			if err != nil {
				t.Fatalf("Get after the rejected replays: %v", err)
			}
			if stillTerminal.LifecycleState != terminal.state {
				t.Fatalf("replays changed the terminal state to %q", string(stillTerminal.LifecycleState))
			}

			recovered, err := NewRegistrationStore(dir)
			if err != nil {
				t.Fatalf("NewRegistrationStore after restart: %v", err)
			}
			if _, err := recovered.Put(registration); err == nil {
				t.Fatalf("replay resurrected a %s registration after restart", string(terminal.state))
			}
			recoveredRecord, err := recovered.Get(registration.RegistrationId)
			if err != nil {
				t.Fatalf("Get after restart: %v", err)
			}
			if recoveredRecord.LifecycleState != terminal.state {
				t.Fatalf("restart lost the terminal state, got %q", string(recoveredRecord.LifecycleState))
			}

			if count := ledgerLineCount(t, dir); count != 2 {
				t.Fatalf("expected one registration fact plus one lifecycle fact, got %d lines", count)
			}
		})
	}
}

// TestRegistrationStoreRejectsDirectTerminalAdmission freezes negative
// fixture (7): a fresh registration can only be admitted in create or
// active; revoked and expired are never initial states.
func TestRegistrationStoreRejectsDirectTerminalAdmission(t *testing.T) {
	store, dir := newTestStore(t)
	for _, state := range []LifecycleState{LifecycleStateRevoked, LifecycleStateExpired} {
		registration := validRegistration()
		registration.RegistrationId = "registration-direct-" + string(state)
		registration.LifecycleState = state
		setRegistrationDigest(&registration)
		if _, err := store.Put(registration); err == nil {
			t.Fatalf("Put admitted a registration directly in %s", string(state))
		}
	}
	if count := ledgerLineCount(t, dir); count != 0 {
		t.Fatalf("rejected admissions must not append; ledger has %d lines", count)
	}
}

// TestRegistrationStoreRejectsUnknownTransitions freezes negative fixture
// (8): revoke and expire fail closed on unknown registrationIds, and a
// terminal registration never crosses into the other terminal state.
func TestRegistrationStoreRejectsUnknownTransitions(t *testing.T) {
	store, dir := newTestStore(t)
	if err := store.Revoke("registration-missing"); err == nil {
		t.Fatal("Revoke accepted an unknown registrationId")
	}
	if err := store.Expire("registration-missing"); err == nil {
		t.Fatal("Expire accepted an unknown registrationId")
	}

	registration := validRegistration()
	mustPut(t, store, registration)
	if err := store.Revoke("registration-missing"); err == nil {
		t.Fatal("Revoke accepted an unknown registrationId on a populated store")
	}
	if count := ledgerLineCount(t, dir); count != 1 {
		t.Fatalf("rejected transitions must not append; ledger has %d lines", count)
	}

	if err := store.Revoke(registration.RegistrationId); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := store.Revoke(registration.RegistrationId); err != nil {
		t.Fatalf("repeating the identical terminal transition must be idempotent: %v", err)
	}
	if err := store.Expire(registration.RegistrationId); err == nil {
		t.Fatal("a revoked registration transitioned to expired")
	}
	if count := ledgerLineCount(t, dir); count != 2 {
		t.Fatalf("expected one registration fact plus one lifecycle fact, got %d lines", count)
	}
}

// TestRegistrationStoreKeepsDistinctIdentitiesSeparate freezes negative
// fixture (9): any change to the idempotency identity yields a different
// IdempotencyDigest and a separate registration; records never overwrite
// each other.
func TestRegistrationStoreKeepsDistinctIdentitiesSeparate(t *testing.T) {
	store, dir := newTestStore(t)
	base := validRegistration()
	mustPut(t, store, base)
	baseIdentity, err := base.IdempotencyDigest()
	if err != nil {
		t.Fatalf("IdempotencyDigest: %v", err)
	}

	mutations := []struct {
		name   string
		change func(*ProviderRegistration)
	}{
		{"scope", func(registration *ProviderRegistration) { registration.Scope = "repository:other" }},
		{"protocolVersion", func(registration *ProviderRegistration) { registration.ProtocolVersion = "v2alpha1" }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			variant := validRegistration()
			variant.RegistrationId = "registration-variant-" + mutation.name
			mutation.change(&variant)
			setRegistrationDigest(&variant)

			variantIdentity, err := variant.IdempotencyDigest()
			if err != nil {
				t.Fatalf("IdempotencyDigest: %v", err)
			}
			if variantIdentity == baseIdentity {
				t.Fatalf("changing %s did not change the idempotency digest", mutation.name)
			}
			admitted := mustPut(t, store, variant)
			if admitted.RegistrationId != variant.RegistrationId {
				t.Fatal("the distinct registration was merged into the existing record")
			}
		})
	}

	gotBase, err := store.Get(base.RegistrationId)
	if err != nil {
		t.Fatalf("Get base after variants: %v", err)
	}
	if !reflect.DeepEqual(gotBase, base) {
		t.Fatal("a distinct identity overwrote the existing registration")
	}
	for _, mutation := range mutations {
		if _, err := store.Get("registration-variant-" + mutation.name); err != nil {
			t.Fatalf("Get variant %s: %v", mutation.name, err)
		}
	}
	if count := ledgerLineCount(t, dir); count != 3 {
		t.Fatalf("three distinct registrations require three ledger facts, got %d lines", count)
	}
}

// TestRegistrationStoreFailsClosedOnInvalidInput freezes the fail-closed
// admission boundary: invalid or tampered registrations never reach the
// ledger, and a corrupted ledger is rejected during recovery.
func TestRegistrationStoreFailsClosedOnInvalidInput(t *testing.T) {
	store, dir := newTestStore(t)

	tampered := validRegistration()
	tampered.RegistrationDigest = fixedDigest("tampered-registration-binding")
	if _, err := store.Put(tampered); err == nil {
		t.Fatal("Put accepted a registration whose digest does not match its content")
	}
	if _, err := store.Put(ProviderRegistration{}); err == nil {
		t.Fatal("Put accepted a zero registration")
	}
	if count := ledgerLineCount(t, dir); count != 0 {
		t.Fatalf("rejected admissions must not append; ledger has %d lines", count)
	}

	corruptDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(corruptDir, ledgerFileName), []byte("not-a-ledger-fact\n"), 0o644); err != nil {
		t.Fatalf("seed corrupted ledger: %v", err)
	}
	if _, err := NewRegistrationStore(corruptDir); err == nil {
		t.Fatal("NewRegistrationStore recovered from a corrupted ledger")
	}
}
