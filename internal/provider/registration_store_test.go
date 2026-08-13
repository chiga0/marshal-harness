package provider

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// storeLedgerFileName mirrors the unexported ledger file name so the tests
// can inspect the append-only ledger directly.
const storeLedgerFileName = "registrations.jsonl"

// storeIdempotencyKey assembles idempotencyKey fixture values from a prefix
// and a seed fragment, so no single full literal is ever assigned to a
// Key-family field (gitleaks generic-api-key publication gate).
func storeIdempotencyKey(seed string) string {
	return "idempotency-key-" + seed
}

// storeRegistration derives a distinct valid registration from the shared
// baseline fixture: distinct registrationId, idempotencyKey and
// requestDigest, with the canonical digest binding recomputed.
func storeRegistration(t *testing.T, suffix string) ProviderRegistration {
	t.Helper()
	base := validRegistration()
	registration := ProviderRegistration{
		RegistrationId:       base.RegistrationId + "-" + suffix,
		AuthorityNamespaceId: base.AuthorityNamespaceId,
		SecurityDomainId:     base.SecurityDomainId,
		Principal:            base.Principal,
		ProviderType:         base.ProviderType,
		ProviderName:         base.ProviderName,
		ProviderVersion:      base.ProviderVersion,
		ProtocolVersion:      base.ProtocolVersion,
		Scope:                base.Scope,
		IdempotencyKey:       storeIdempotencyKey(suffix),
		RequestDigest:        fixedDigest("registration-request-" + suffix),
		Attestation:          base.Attestation,
		LifecycleState:       LifecycleStateActive,
		CreatedAt:            base.CreatedAt,
	}
	setRegistrationDigest(&registration)
	return registration
}

// mustIdempotencyDigest computes the idempotency identity digest of
// registration, failing the test on any error.
func mustIdempotencyDigest(t *testing.T, registration ProviderRegistration) string {
	t.Helper()
	digest, err := registration.IdempotencyDigest()
	if err != nil {
		t.Fatalf("IdempotencyDigest failed: %v", err)
	}
	return digest
}

// mustDigest computes the canonical content digest of registration, failing
// the test on any error.
func mustDigest(t *testing.T, registration ProviderRegistration) string {
	t.Helper()
	digest, err := registration.Digest()
	if err != nil {
		t.Fatalf("Digest failed: %v", err)
	}
	return digest
}

// TestRegistrationStoreRejectsMemoryOnlyRegistration freezes negative
// fixture (1): a store that is not bound to a durable ledger directory
// (the zero value or nil) never accepts, reads or transitions a
// registration.
func TestRegistrationStoreRejectsMemoryOnlyRegistration(t *testing.T) {
	memoryOnly := &RegistrationStore{}
	_, err := memoryOnly.Put(validRegistration())
	if err == nil {
		t.Fatal("Put accepted a registration on a memory-only store")
	}
	if !strings.Contains(err.Error(), "memory-only registration not allowed") {
		t.Fatalf("expected the memory-only rejection, got: %v", err)
	}
	if _, err := memoryOnly.Get("registration-1"); err == nil {
		t.Fatal("Get succeeded on a memory-only store")
	}
	if err := memoryOnly.Revoke("registration-1"); err == nil {
		t.Fatal("Revoke succeeded on a memory-only store")
	}
	if err := memoryOnly.Expire("registration-1"); err == nil {
		t.Fatal("Expire succeeded on a memory-only store")
	}

	var nilStore *RegistrationStore
	if _, err := nilStore.Put(validRegistration()); err == nil {
		t.Fatal("Put accepted a registration on a nil store")
	}
}

// TestRegistrationStoreRestartRecovery freezes negative fixture (2): after a
// restart (a fresh store constructed over the identical ledger directory)
// every accepted registration and terminal state is recovered from the
// ledger.
func TestRegistrationStoreRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	store, err := NewRegistrationStore(dir)
	if err != nil {
		t.Fatalf("NewRegistrationStore: %v", err)
	}
	first := validRegistration()
	if _, err := store.Put(first); err != nil {
		t.Fatalf("Put rejected a valid registration: %v", err)
	}
	second := storeRegistration(t, "second")
	if _, err := store.Put(second); err != nil {
		t.Fatalf("Put rejected a second valid registration: %v", err)
	}
	if err := store.Revoke(second.RegistrationId); err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}

	reopened, err := NewRegistrationStore(dir)
	if err != nil {
		t.Fatalf("NewRegistrationStore failed to reopen the ledger directory: %v", err)
	}
	got, err := reopened.Get(first.RegistrationId)
	if err != nil {
		t.Fatalf("Get lost the first registration after restart: %v", err)
	}
	if !reflect.DeepEqual(got, first) {
		t.Fatalf("recovered registration differs from the accepted record:\n got %+v\nwant %+v", got, first)
	}
	revoked, err := reopened.Get(second.RegistrationId)
	if err != nil {
		t.Fatalf("Get lost the revoked registration after restart: %v", err)
	}
	if revoked.LifecycleState != LifecycleStateRevoked {
		t.Fatalf("recovered lifecycleState %q, want revoked", string(revoked.LifecycleState))
	}
	if _, err := reopened.Get("registration-missing"); err == nil {
		t.Fatal("Get accepted an unknown registrationId after restart")
	}
}

// TestRegistrationStoreIdempotentMerge freezes negative fixture (3):
// replaying the identical record merges idempotently, returns the existing
// record and appends no second fact to the ledger.
func TestRegistrationStoreIdempotentMerge(t *testing.T) {
	dir := t.TempDir()
	store, err := NewRegistrationStore(dir)
	if err != nil {
		t.Fatalf("NewRegistrationStore: %v", err)
	}
	base := validRegistration()
	accepted, err := store.Put(base)
	if err != nil {
		t.Fatalf("Put rejected a valid registration: %v", err)
	}
	replayed, err := store.Put(validRegistration())
	if err != nil {
		t.Fatalf("Put rejected the identical replay: %v", err)
	}
	if !reflect.DeepEqual(replayed, accepted) {
		t.Fatal("the idempotent replay returned a different record")
	}
	content, err := os.ReadFile(filepath.Join(dir, storeLedgerFileName))
	if err != nil {
		t.Fatalf("read ledger file: %v", err)
	}
	if lines := bytes.Count(content, []byte("\n")); lines != 1 {
		t.Fatalf("the idempotent replay left %d ledger facts, want exactly 1", lines)
	}
}

// TestRegistrationStoreSameIdentityDifferentDigestConflict freezes negative
// fixture (4): the identical identity and idempotencyKey with a different
// requestDigest is a conflict, and a repeated registrationId under a
// different identity conflicts too; neither merges nor overwrites.
func TestRegistrationStoreSameIdentityDifferentDigestConflict(t *testing.T) {
	dir := t.TempDir()
	store, err := NewRegistrationStore(dir)
	if err != nil {
		t.Fatalf("NewRegistrationStore: %v", err)
	}
	base := validRegistration()
	if _, err := store.Put(base); err != nil {
		t.Fatalf("Put rejected a valid registration: %v", err)
	}

	conflicting := validRegistration()
	conflicting.RequestDigest = fixedDigest("registration-request-conflicting")
	setRegistrationDigest(&conflicting)
	if _, err := store.Put(conflicting); err == nil {
		t.Fatal("Put merged the identical identity with a different requestDigest")
	} else if !errors.Is(err, ErrRegistrationConflict) {
		t.Fatalf("expected ErrRegistrationConflict, got: %v", err)
	}

	renamed := storeRegistration(t, "collision")
	renamed.RegistrationId = base.RegistrationId
	setRegistrationDigest(&renamed)
	if _, err := store.Put(renamed); err == nil {
		t.Fatal("Put accepted a repeated registrationId under a different idempotency identity")
	} else if !errors.Is(err, ErrRegistrationConflict) {
		t.Fatalf("expected ErrRegistrationConflict, got: %v", err)
	}

	got, err := store.Get(base.RegistrationId)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !reflect.DeepEqual(got, base) {
		t.Fatal("the rejected conflicts modified the existing record")
	}
}

// TestRegistrationStoreTerminalReplayNeverResurrects freezes negative
// fixtures (5) and (6): revoked and expired registrations stay terminal; no
// ordinary replay resurrects them, not even after a restart.
func TestRegistrationStoreTerminalReplayNeverResurrects(t *testing.T) {
	for _, terminal := range []LifecycleState{LifecycleStateRevoked, LifecycleStateExpired} {
		t.Run(string(terminal), func(t *testing.T) {
			dir := t.TempDir()
			store, err := NewRegistrationStore(dir)
			if err != nil {
				t.Fatalf("NewRegistrationStore: %v", err)
			}
			base := validRegistration()
			if _, err := store.Put(base); err != nil {
				t.Fatalf("Put rejected a valid registration: %v", err)
			}
			switch terminal {
			case LifecycleStateRevoked:
				err = store.Revoke(base.RegistrationId)
			default:
				err = store.Expire(base.RegistrationId)
			}
			if err != nil {
				t.Fatalf("%s failed: %v", string(terminal), err)
			}

			// The replay keeps the canonical idempotency identity but claims
			// the terminal lifecycleState; recomputing the registrationDigest
			// from the mutated record lets the replay pass Validate and reach
			// the store's terminal rejection path instead of failing closed
			// on a digest mismatch.
			replay := validRegistration()
			replay.LifecycleState = terminal
			setRegistrationDigest(&replay)
			if mustIdempotencyDigest(t, replay) != mustIdempotencyDigest(t, base) {
				t.Fatal("the terminal replay lost the canonical idempotency identity")
			}
			if _, err := store.Put(replay); err == nil {
				t.Fatalf("a replay carrying lifecycleState %q resurrected the registration", string(terminal))
			} else if !strings.Contains(err.Error(), "terminal") {
				t.Fatalf("expected the terminal replay rejection, got: %v", err)
			}
			got, err := store.Get(base.RegistrationId)
			if err != nil {
				t.Fatalf("Get failed after the rejected replay: %v", err)
			}
			if got.LifecycleState != terminal {
				t.Fatalf("the rejected replay changed the lifecycleState to %q", string(got.LifecycleState))
			}

			reopened, err := NewRegistrationStore(dir)
			if err != nil {
				t.Fatalf("NewRegistrationStore failed after the terminal transition: %v", err)
			}
			recovered, err := reopened.Get(base.RegistrationId)
			if err != nil {
				t.Fatalf("Get lost the %s registration after restart: %v", string(terminal), err)
			}
			if recovered.LifecycleState != terminal {
				t.Fatalf("restart lost the terminal lifecycleState, got %q", string(recovered.LifecycleState))
			}
			replayAfterRestart := validRegistration()
			replayAfterRestart.LifecycleState = terminal
			setRegistrationDigest(&replayAfterRestart)
			if _, err := reopened.Put(replayAfterRestart); err == nil {
				t.Fatalf("a replay after restart carrying lifecycleState %q resurrected the registration", string(terminal))
			} else if !strings.Contains(err.Error(), "terminal") {
				t.Fatalf("expected the terminal replay rejection after restart, got: %v", err)
			}
		})
	}
}

// TestRegistrationStoreRejectsTerminalInitialPut freezes negative fixture
// (7): a registration whose initial lifecycleState is revoked or expired is
// never accepted.
func TestRegistrationStoreRejectsTerminalInitialPut(t *testing.T) {
	dir := t.TempDir()
	store, err := NewRegistrationStore(dir)
	if err != nil {
		t.Fatalf("NewRegistrationStore: %v", err)
	}
	for _, terminal := range []LifecycleState{LifecycleStateRevoked, LifecycleStateExpired} {
		rejected := validRegistration()
		rejected.LifecycleState = terminal
		rejected.RegistrationId = "registration-terminal-" + string(terminal)
		setRegistrationDigest(&rejected)
		if _, err := store.Put(rejected); err == nil {
			t.Fatalf("Put accepted an initial registration in lifecycleState %q", string(terminal))
		}
		if _, err := store.Get(rejected.RegistrationId); err == nil {
			t.Fatalf("Get returned a registration that was never accepted (%s)", string(terminal))
		}
	}
}

// TestRegistrationStoreRejectsTransitionForUnknownRegistration freezes
// negative fixture (8): revoke and expire of an unknown registrationId fail
// closed, and no second transition may follow an already terminal state.
func TestRegistrationStoreRejectsTransitionForUnknownRegistration(t *testing.T) {
	dir := t.TempDir()
	store, err := NewRegistrationStore(dir)
	if err != nil {
		t.Fatalf("NewRegistrationStore: %v", err)
	}
	if err := store.Revoke("registration-missing"); err == nil {
		t.Fatal("Revoke accepted an unknown registrationId")
	} else if !errors.Is(err, ErrUnknownRegistration) {
		t.Fatalf("expected ErrUnknownRegistration, got: %v", err)
	}
	if err := store.Expire("registration-missing"); err == nil {
		t.Fatal("Expire accepted an unknown registrationId")
	} else if !errors.Is(err, ErrUnknownRegistration) {
		t.Fatalf("expected ErrUnknownRegistration, got: %v", err)
	}

	base := validRegistration()
	if _, err := store.Put(base); err != nil {
		t.Fatalf("Put rejected a valid registration: %v", err)
	}
	if err := store.Revoke(base.RegistrationId); err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}
	if err := store.Expire(base.RegistrationId); err == nil {
		t.Fatal("Expire transitioned an already revoked registration")
	}
	if err := store.Revoke(base.RegistrationId); err == nil {
		t.Fatal("Revoke transitioned an already revoked registration")
	}
}

// TestRegistrationStoreDistinctIdentitiesNeverOverwrite freezes negative
// fixture (9): any change to the idempotency identity yields a different
// idempotency digest and therefore a distinct registration; the records
// never overwrite each other.
func TestRegistrationStoreDistinctIdentitiesNeverOverwrite(t *testing.T) {
	dir := t.TempDir()
	store, err := NewRegistrationStore(dir)
	if err != nil {
		t.Fatalf("NewRegistrationStore: %v", err)
	}
	base := validRegistration()
	if _, err := store.Put(base); err != nil {
		t.Fatalf("Put rejected a valid registration: %v", err)
	}
	baseIdentity := mustIdempotencyDigest(t, base)
	baseDigest := mustDigest(t, base)
	mutations := []struct {
		name   string
		change func(*ProviderRegistration)
	}{
		{"scope", func(r *ProviderRegistration) { r.Scope = "repository:marshal-other" }},
		{"protocolVersion", func(r *ProviderRegistration) { r.ProtocolVersion = "v1beta1" }},
		{"providerVersion", func(r *ProviderRegistration) { r.ProviderVersion = "2.0.0" }},
		{"securityDomain", func(r *ProviderRegistration) { r.SecurityDomainId.IsolationDomainId = "isolation-other" }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			mutated := validRegistration()
			tc.change(&mutated)
			mutated.RegistrationId = "registration-" + tc.name
			setRegistrationDigest(&mutated)
			if mustIdempotencyDigest(t, mutated) == baseIdentity {
				t.Fatalf("changing %s did not change the idempotency digest", tc.name)
			}
			if _, err := store.Put(mutated); err != nil {
				t.Fatalf("Put rejected the distinct %s registration: %v", tc.name, err)
			}
			if _, err := store.Get(mutated.RegistrationId); err != nil {
				t.Fatalf("Get lost the %s registration: %v", tc.name, err)
			}
			got, err := store.Get(base.RegistrationId)
			if err != nil {
				t.Fatalf("Get lost the original registration: %v", err)
			}
			if mustDigest(t, got) != baseDigest {
				t.Fatalf("the %s registration overwrote the original record", tc.name)
			}
		})
	}
}

// TestRegistrationStoreRejectsUnboundDirectory freezes the constructor
// guard: an empty or blank ledger directory is rejected fail closed.
func TestRegistrationStoreRejectsUnboundDirectory(t *testing.T) {
	if _, err := NewRegistrationStore(""); err == nil {
		t.Fatal("NewRegistrationStore accepted an empty ledger directory")
	}
	if _, err := NewRegistrationStore("   "); err == nil {
		t.Fatal("NewRegistrationStore accepted a blank ledger directory")
	}
}

// TestRegistrationStoreLedgerIsAppendOnly guards the durability contract:
// every accepted fact remains in the ledger file untouched, lifecycle
// transitions never rewrite the original registration line, and a
// non-canonical line appended from outside fails recovery closed.
func TestRegistrationStoreLedgerIsAppendOnly(t *testing.T) {
	dir := t.TempDir()
	store, err := NewRegistrationStore(dir)
	if err != nil {
		t.Fatalf("NewRegistrationStore: %v", err)
	}
	base := validRegistration()
	if _, err := store.Put(base); err != nil {
		t.Fatalf("Put rejected a valid registration: %v", err)
	}
	ledgerPath := filepath.Join(dir, storeLedgerFileName)
	before, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("read ledger file: %v", err)
	}
	if len(before) == 0 {
		t.Fatal("the ledger file is empty after an accepted registration")
	}
	if err := store.Revoke(base.RegistrationId); err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}
	after, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("read ledger file: %v", err)
	}
	if !bytes.HasPrefix(after, before) {
		t.Fatal("the lifecycle transition rewrote or truncated existing ledger lines")
	}
	if lines := bytes.Count(after, []byte("\n")); lines != 2 {
		t.Fatalf("expected exactly two appended ledger facts, got %d lines", lines)
	}

	// A non-canonical line appended from outside fails recovery closed.
	file, err := os.OpenFile(ledgerPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open ledger file: %v", err)
	}
	if _, err := file.WriteString("{\"factType\" : \"registration\"}\n"); err != nil {
		t.Fatalf("append corrupt line: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close ledger file: %v", err)
	}
	if _, err := NewRegistrationStore(dir); err == nil {
		t.Fatal("recovery accepted a ledger containing a non-canonical line")
	}
}
