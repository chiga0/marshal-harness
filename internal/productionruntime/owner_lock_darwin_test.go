//go:build darwin && arm64

package productionruntime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

func currentProcessAcquisition() resultingress.ControlOwnerAcquisition {
	value := testAcquisition()
	value.OwnerUID = uint32(os.Getuid())
	value.OwnerGID = uint32(os.Getgid())
	value.OwnerProcess.PID = os.Getpid()
	value.OwnerProcess.SessionID = os.Getpid()
	value.OwnerProcess.ProcessGroupID = os.Getpid()
	value.OwnerBinary.UID = uint32(os.Getuid())
	value.OwnerBinary.GID = uint32(os.Getgid())
	return value
}

type ownerLockFixture struct {
	base      string
	ownerPath string
	directory *os.File
}

func newOwnerLockFixture(t *testing.T) ownerLockFixture {
	t.Helper()
	// Keep Darwin fixtures below its real private temporary root. The
	// production API receives only the held descriptor; this pathname exists
	// solely to drive hostile current-name replacement tests.
	base, err := os.MkdirTemp("/private/tmp", "marshal-owner-lock-")
	if err != nil {
		t.Fatal(err)
	}
	ownerPath := filepath.Join(base, "owner")
	if err := os.Mkdir(ownerPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ownerPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(ownerPath, -1, os.Getgid()); err != nil {
		t.Fatalf("set owner directory group: %v", err)
	}
	directory, err := os.Open(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = directory.Close()
		if err := os.RemoveAll(base); err != nil {
			t.Errorf("remove owner lock fixture: %v", err)
		}
	})
	return ownerLockFixture{base: base, ownerPath: ownerPath, directory: directory}
}

func openOwnerStore(t *testing.T, fixture ownerLockFixture) *resultingress.DurableStore {
	t.Helper()
	store, err := resultingress.OpenResultIngressStore(filepath.Join(fixture.base, "result-ingress"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func openHeldOwnerStore(t *testing.T, fixture ownerLockFixture) (*resultingress.DurableStore, string) {
	t.Helper()
	return openHeldOwnerStoreWithInitialLedger(t, fixture, nil)
}

func openHeldOwnerStoreWithInitialLedger(t *testing.T, fixture ownerLockFixture, initialLedger []byte) (*resultingress.DurableStore, string) {
	t.Helper()
	path := filepath.Join(fixture.base, "held-result-ingress")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, -1, os.Getgid()); err != nil {
		t.Fatalf("set held ingress directory group: %v", err)
	}
	ledgerPath := filepath.Join(path, "result-ingress.jsonl")
	if err := os.WriteFile(ledgerPath, initialLedger, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(ledgerPath, -1, os.Getgid()); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	store, err := resultingress.OpenDarwinResultIngressStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func ownerLedgerBytes(t *testing.T, fixture ownerLockFixture) []byte {
	t.Helper()
	value, err := os.ReadFile(filepath.Join(fixture.base, "result-ingress", "result-ingress.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func renameAwayAndBackSameObject(t *testing.T, path, moved string) {
	t.Helper()
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, path); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("rename roundtrip did not preserve the exact filesystem object")
	}
}

func requirePhysicalRecheckBeforeCallback(t *testing.T, phase repositoryOwnerScopeLock) {
	t.Helper()
	concrete := phase.(*darwinRepositoryOwnerScopeLock)
	called := false
	err := concrete.physical.withHeld(context.Background(), false, func() error {
		called = true
		return nil
	})
	if !application.HasReason(err, application.ReasonOwnerNotCurrent) || called {
		t.Fatalf("mutated physical lock recheck err=%v called=%t", err, called)
	}
}

func requireOwnerTransitionFailure(t *testing.T, err error, kind repositoryOwnerTransitionFailureKind) {
	t.Helper()
	actual, ok := repositoryOwnerTransitionKind(err)
	if !ok || actual != kind || !application.HasReason(err, application.ReasonOwnerNotCurrent) || err.Error() != "application: production-owner-not-current" {
		t.Fatalf("owner transition err=%v kind=%q found=%t, want kind=%q", err, actual, ok, kind)
	}
}

func acquisitionAtEpoch(epoch uint64) resultingress.ControlOwnerAcquisition {
	value := currentProcessAcquisition()
	value.OwnerEpoch = epoch
	return value
}

func acquireAndBindOwner(t *testing.T, phase repositoryOwnerScopeLock, store *resultingress.DurableStore, acquisition resultingress.ControlOwnerAcquisition) (repositoryOwnerLock, resultingress.ControlOwnerState, resultingress.ControlOwnerAcquisition) {
	t.Helper()
	bound, state, acquired, err := phase.acquireAndBind(context.Background(), store, acquisition)
	if err != nil {
		t.Fatal(err)
	}
	if acquired.OwnerEpoch == 0 || state.Acquisition != acquired || !validCurrentOwnerReplay(state, acquired) {
		t.Fatalf("unexpected atomic owner transition: state=%#v acquisition=%#v", state, acquired)
	}
	replayed, found, err := store.OpenOwner(acquired.Scope)
	if err != nil || !found || replayed != state {
		t.Fatalf("exact owner replay failed: found=%t state=%#v err=%v", found, replayed, err)
	}
	return bound, state, acquired
}

func appendOwnerWithoutBinding(t *testing.T, phase repositoryOwnerScopeLock, store *resultingress.DurableStore, expectedEpoch uint64, expectedDigest string, acquisition resultingress.ControlOwnerAcquisition) resultingress.ControlOwnerState {
	t.Helper()
	concrete := phase.(*darwinRepositoryOwnerScopeLock)
	var result resultingress.ControlOwnerAppendResult
	err := concrete.physical.withHeld(context.Background(), false, func() error {
		verifier := &darwinProvisionalOwnerVerifier{candidate: acquisition}
		var appendErr error
		result, appendErr = store.AcquireOwner(context.Background(), verifier, expectedEpoch, expectedDigest, acquisition)
		return appendErr
	})
	if err != nil || !result.Appended || result.State.Acquisition != acquisition {
		t.Fatalf("append without bind: result=%#v err=%v", result, err)
	}
	return result.State
}

func TestRepositoryOwnerLockAtomicallyAcquiresReplaysAndBinds(t *testing.T) {
	fixture := newOwnerLockFixture(t)
	acquisition := acquisitionAtEpoch(1)
	phase, err := openRepositoryOwnerScopeLock(fixture.directory, acquisition.Scope)
	if err != nil {
		t.Fatal(err)
	}
	defer phase.Close()
	if _, implementsCurrent := any(phase).(resultingress.CurrentOwnerLockVerifier); implementsCurrent {
		t.Fatal("Phase A scope lock implements CurrentOwnerLockVerifier")
	}
	if phase.scope() != acquisition.Scope || phase.identity() == (ownerLockIdentity{}) {
		t.Fatalf("scope lock identity mismatch: scope=%#v identity=%#v", phase.scope(), phase.identity())
	}
	if _, err := openRepositoryOwnerScopeLock(fixture.directory, acquisition.Scope); !application.HasReason(err, application.ReasonOwnerUnavailable) {
		t.Fatalf("competing Phase A lock err=%v", err)
	}

	store := openOwnerStore(t, fixture)
	bound, state, acquired := acquireAndBindOwner(t, phase, store, acquisition)
	if acquired != acquisition || state.Acquisition != acquisition {
		t.Fatalf("bound acquisition mismatch: state=%#v acquisition=%#v", state, acquired)
	}
	if _, _, _, err := phase.acquireAndBind(context.Background(), store, acquisition); !application.HasReason(err, application.ReasonOwnerNotCurrent) {
		t.Fatalf("repeated transition err=%v", err)
	}
	// Ownership was transferred; closing the spent Phase A handle must not
	// release the Phase B lock.
	if err := phase.Close(); err != nil {
		t.Fatal(err)
	}
	called := false
	err = bound.WithCurrentOwnerLock(context.Background(), acquisition, func() error { called = true; return nil })
	if !application.HasReason(err, application.ReasonOwnerNotCurrent) || called {
		t.Fatalf("unclaimed bound lock err=%v called=%t", err, called)
	}
	if err := bound.claimRuntime(); err != nil {
		t.Fatal(err)
	}
	called = false
	if err := bound.WithCurrentOwnerLock(context.Background(), acquisition, func() error { called = true; return nil }); err != nil || !called {
		t.Fatalf("current callback err=%v called=%t", err, called)
	}
	if err := bound.claimRuntime(); !application.HasReason(err, application.ReasonOwnerUnavailable) {
		t.Fatalf("repeated runtime claim err=%v", err)
	}
	wrong := acquisition
	wrong.OwnerEpoch++
	called = false
	err = bound.WithCurrentOwnerLock(context.Background(), wrong, func() error { called = true; return nil })
	if !application.HasReason(err, application.ReasonOwnerNotCurrent) || called {
		t.Fatalf("wrong acquisition err=%v called=%t", err, called)
	}
	if err := bound.Close(); err != nil {
		t.Fatal(err)
	}
	called = false
	err = bound.WithCurrentOwnerLock(context.Background(), acquisition, func() error { called = true; return nil })
	if !application.HasReason(err, application.ReasonOwnerNotCurrent) || called {
		t.Fatalf("closed bound lock err=%v called=%t", err, called)
	}
}

func TestRepositoryOwnerLockTransitionIsCreationOnce(t *testing.T) {
	fixture := newOwnerLockFixture(t)
	acquisition := acquisitionAtEpoch(1)
	phase, err := openRepositoryOwnerScopeLock(fixture.directory, acquisition.Scope)
	if err != nil {
		t.Fatal(err)
	}
	defer phase.Close()
	store := openOwnerStore(t, fixture)
	bound, _, _ := acquireAndBindOwner(t, phase, store, acquisition)
	defer bound.Close()
	foreign, err := resultingress.OpenResultIngressStore(filepath.Join(fixture.base, "foreign-result-ingress"))
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if _, _, _, err := phase.acquireAndBind(context.Background(), foreign, acquisition); !application.HasReason(err, application.ReasonOwnerNotCurrent) {
		t.Fatalf("second transition with foreign store err=%v", err)
	}
}

func TestRepositoryOwnerScopeLockCloseIsTerminalAndIdempotent(t *testing.T) {
	fixture := newOwnerLockFixture(t)
	acquisition := acquisitionAtEpoch(1)
	phase, err := openRepositoryOwnerScopeLock(fixture.directory, acquisition.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := phase.Close(); err != nil {
		t.Fatal(err)
	}
	if err := phase.Close(); err != nil {
		t.Fatalf("repeated close err=%v", err)
	}
	store := openOwnerStore(t, fixture)
	if _, _, _, err := phase.acquireAndBind(context.Background(), store, acquisition); !application.HasReason(err, application.ReasonOwnerNotCurrent) {
		t.Fatalf("closed Phase A transition err=%v", err)
	}
	reopened, err := openRepositoryOwnerScopeLock(fixture.directory, acquisition.Scope)
	if err != nil {
		t.Fatalf("released lock could not be reopened: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryOwnerLockResponseLossCreatesStrictSuccessor(t *testing.T) {
	fixture := newOwnerLockFixture(t)
	store, _ := openHeldOwnerStore(t, fixture)
	firstAcquisition := acquisitionAtEpoch(1)
	first, err := openRepositoryOwnerScopeLock(fixture.directory, firstAcquisition.Scope)
	if err != nil {
		t.Fatal(err)
	}
	firstState := appendOwnerWithoutBinding(t, first, store, 0, "", firstAcquisition)
	// Simulate loss of the successful append response before Phase B bind.
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := openRepositoryOwnerScopeLock(fixture.directory, firstAcquisition.Scope)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	secondCandidate := acquisitionAtEpoch(0)
	bound, secondState, secondAcquisition := acquireAndBindOwner(t, second, store, secondCandidate)
	defer bound.Close()
	if bound.acquisition() != secondAcquisition || secondState.PreviousFactDigest != firstState.FactDigest {
		t.Fatalf("successor did not bind exact predecessor: %#v", secondState)
	}
}

func TestRepositoryOwnerScopeLockRejectsConcurrentAcquire(t *testing.T) {
	fixture := newOwnerLockFixture(t)
	store := openOwnerStore(t, fixture)
	acquisition := acquisitionAtEpoch(1)
	phase, err := openRepositoryOwnerScopeLock(fixture.directory, acquisition.Scope)
	if err != nil {
		t.Fatal(err)
	}
	defer phase.Close()
	type outcome struct {
		owner repositoryOwnerLock
		state resultingress.ControlOwnerState
		err   error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			owner, state, _, err := phase.acquireAndBind(context.Background(), store, acquisition)
			outcomes <- outcome{owner: owner, state: state, err: err}
		}()
	}
	close(start)
	workers.Wait()
	close(outcomes)
	var successes, rejections int
	for outcome := range outcomes {
		if outcome.err == nil && outcome.owner != nil && outcome.state.Acquisition == acquisition {
			successes++
			_ = outcome.owner.Close()
		} else if application.HasReason(outcome.err, application.ReasonOwnerNotCurrent) {
			rejections++
		} else {
			t.Fatalf("unexpected concurrent outcome: %#v", outcome)
		}
	}
	if successes != 1 || rejections != 1 {
		t.Fatalf("successes=%d rejections=%d", successes, rejections)
	}
}

func TestRepositoryOwnerScopeLockRejectsDirectoryAndEntryABA(t *testing.T) {
	t.Run("directory-same-object-roundtrip", func(t *testing.T) {
		fixture := newOwnerLockFixture(t)
		store := openOwnerStore(t, fixture)
		acquisition := acquisitionAtEpoch(1)
		phase, err := openRepositoryOwnerScopeLock(fixture.directory, acquisition.Scope)
		if err != nil {
			t.Fatal(err)
		}
		defer phase.Close()
		before := ownerLedgerBytes(t, fixture)
		moved := fixture.ownerPath + "-roundtrip"
		renameAwayAndBackSameObject(t, fixture.ownerPath, moved)
		requirePhysicalRecheckBeforeCallback(t, phase)
		_, _, _, transitionErr := phase.acquireAndBind(context.Background(), store, acquisition)
		requireOwnerTransitionFailure(t, transitionErr, repositoryOwnerFailureOwnerIdentityDrift)
		if after := ownerLedgerBytes(t, fixture); !bytes.Equal(before, after) {
			t.Fatalf("directory rename roundtrip wrote ledger: before=%q after=%q", before, after)
		}
		if _, found, err := store.OpenOwner(acquisition.Scope); err != nil || found {
			t.Fatalf("directory rename roundtrip mutated owner authority: found=%t err=%v", found, err)
		}
	})

	t.Run("lock-entry-same-object-roundtrip", func(t *testing.T) {
		fixture := newOwnerLockFixture(t)
		store := openOwnerStore(t, fixture)
		acquisition := acquisitionAtEpoch(1)
		phase, err := openRepositoryOwnerScopeLock(fixture.directory, acquisition.Scope)
		if err != nil {
			t.Fatal(err)
		}
		defer phase.Close()
		concrete := phase.(*darwinRepositoryOwnerScopeLock)
		path := filepath.Join(fixture.ownerPath, concrete.physical.name)
		moved := path + ".roundtrip"
		before := ownerLedgerBytes(t, fixture)
		renameAwayAndBackSameObject(t, path, moved)
		requirePhysicalRecheckBeforeCallback(t, phase)
		_, _, _, transitionErr := phase.acquireAndBind(context.Background(), store, acquisition)
		requireOwnerTransitionFailure(t, transitionErr, repositoryOwnerFailureOwnerIdentityDrift)
		if after := ownerLedgerBytes(t, fixture); !bytes.Equal(before, after) {
			t.Fatalf("entry rename roundtrip wrote ledger: before=%q after=%q", before, after)
		}
		if _, found, err := store.OpenOwner(acquisition.Scope); err != nil || found {
			t.Fatalf("entry rename roundtrip mutated owner authority: found=%t err=%v", found, err)
		}
	})

	t.Run("directory-current-name", func(t *testing.T) {
		fixture := newOwnerLockFixture(t)
		store := openOwnerStore(t, fixture)
		acquisition := acquisitionAtEpoch(1)
		phase, err := openRepositoryOwnerScopeLock(fixture.directory, acquisition.Scope)
		if err != nil {
			t.Fatal(err)
		}
		defer phase.Close()
		moved := fixture.ownerPath + "-moved"
		if err := os.Rename(fixture.ownerPath, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(fixture.ownerPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(fixture.ownerPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(fixture.ownerPath, -1, os.Getgid()); err != nil {
			t.Fatal(err)
		}
		_, _, _, transitionErr := phase.acquireAndBind(context.Background(), store, acquisition)
		requireOwnerTransitionFailure(t, transitionErr, repositoryOwnerFailureOwnerIdentityDrift)
		if _, found, err := store.OpenOwner(acquisition.Scope); err != nil || found {
			t.Fatalf("directory ABA mutated owner authority: found=%t err=%v", found, err)
		}
	})

	t.Run("lock-entry", func(t *testing.T) {
		fixture := newOwnerLockFixture(t)
		store := openOwnerStore(t, fixture)
		acquisition := acquisitionAtEpoch(1)
		phase, err := openRepositoryOwnerScopeLock(fixture.directory, acquisition.Scope)
		if err != nil {
			t.Fatal(err)
		}
		defer phase.Close()
		concrete := phase.(*darwinRepositoryOwnerScopeLock)
		name := concrete.physical.name
		if err := os.Rename(filepath.Join(fixture.ownerPath, name), filepath.Join(fixture.ownerPath, "replaced.lock")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture.ownerPath, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(filepath.Join(fixture.ownerPath, name), -1, os.Getgid()); err != nil {
			t.Fatal(err)
		}
		_, _, _, transitionErr := phase.acquireAndBind(context.Background(), store, acquisition)
		requireOwnerTransitionFailure(t, transitionErr, repositoryOwnerFailureOwnerIdentityDrift)
		if _, found, err := store.OpenOwner(acquisition.Scope); err != nil || found {
			t.Fatalf("entry ABA mutated owner authority: found=%t err=%v", found, err)
		}
	})
}

func TestRepositoryOwnerAtomicTransitionClassifiesIngressIdentityAndReplayFailure(t *testing.T) {
	t.Run("ingress-current-name-aba", func(t *testing.T) {
		fixture := newOwnerLockFixture(t)
		store, ingressPath := openHeldOwnerStore(t, fixture)
		acquisition := acquisitionAtEpoch(1)
		phase, err := openRepositoryOwnerScopeLock(fixture.directory, acquisition.Scope)
		if err != nil {
			t.Fatal(err)
		}
		defer phase.Close()

		ledgerPath := filepath.Join(ingressPath, "result-ingress.jsonl")
		movedPath := ledgerPath + ".replaced"
		if err := os.Rename(ledgerPath, movedPath); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ledgerPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(ledgerPath, -1, os.Getgid()); err != nil {
			t.Fatal(err)
		}
		_, _, _, transitionErr := phase.acquireAndBind(context.Background(), store, acquisition)
		requireOwnerTransitionFailure(t, transitionErr, repositoryOwnerFailureIngressIdentityIO)
		moved, readErr := os.ReadFile(movedPath)
		if readErr != nil || len(moved) != 0 {
			t.Fatalf("ingress ABA mutated held authority: bytes=%q err=%v", moved, readErr)
		}
	})

	t.Run("owner-ledger-corruption", func(t *testing.T) {
		fixture := newOwnerLockFixture(t)
		corrupt := []byte("{}\n")
		store, ingressPath := openHeldOwnerStoreWithInitialLedger(t, fixture, corrupt)
		acquisition := acquisitionAtEpoch(1)
		phase, err := openRepositoryOwnerScopeLock(fixture.directory, acquisition.Scope)
		if err != nil {
			t.Fatal(err)
		}
		defer phase.Close()

		ledgerPath := filepath.Join(ingressPath, "result-ingress.jsonl")
		_, _, _, transitionErr := phase.acquireAndBind(context.Background(), store, acquisition)
		requireOwnerTransitionFailure(t, transitionErr, repositoryOwnerFailureReplayConflict)
		after, readErr := os.ReadFile(ledgerPath)
		if readErr != nil || !bytes.Equal(after, corrupt) {
			t.Fatalf("corrupt replay failure appended authority: bytes=%q err=%v", after, readErr)
		}
	})
}

func TestRepositoryOwnerLockRejectsRoundtripDuringAtomicTransitionAndBoundCallback(t *testing.T) {
	t.Run("directory-before-transition", func(t *testing.T) {
		fixture := newOwnerLockFixture(t)
		store := openOwnerStore(t, fixture)
		acquisition := acquisitionAtEpoch(1)
		phase, err := openRepositoryOwnerScopeLock(fixture.directory, acquisition.Scope)
		if err != nil {
			t.Fatal(err)
		}
		defer phase.Close()
		before := ownerLedgerBytes(t, fixture)
		moved := fixture.ownerPath + "-before-transition"
		renameAwayAndBackSameObject(t, fixture.ownerPath, moved)
		_, _, _, transitionErr := phase.acquireAndBind(context.Background(), store, acquisition)
		requireOwnerTransitionFailure(t, transitionErr, repositoryOwnerFailureOwnerIdentityDrift)
		if after := ownerLedgerBytes(t, fixture); !bytes.Equal(before, after) {
			t.Fatalf("failed transition wrote ledger: before=%q after=%q", before, after)
		}
	})

	t.Run("lock-entry-after-bind", func(t *testing.T) {
		fixture := newOwnerLockFixture(t)
		store := openOwnerStore(t, fixture)
		acquisition := acquisitionAtEpoch(1)
		phase, err := openRepositoryOwnerScopeLock(fixture.directory, acquisition.Scope)
		if err != nil {
			t.Fatal(err)
		}
		bound, _, _ := acquireAndBindOwner(t, phase, store, acquisition)
		defer bound.Close()
		if err := bound.claimRuntime(); err != nil {
			t.Fatal(err)
		}
		concrete := bound.(*darwinRepositoryOwnerLock)
		path := filepath.Join(fixture.ownerPath, concrete.physical.name)
		moved := path + ".after-bind"
		renameAwayAndBackSameObject(t, path, moved)
		called := false
		err = bound.WithCurrentOwnerLock(context.Background(), acquisition, func() error { called = true; return nil })
		if !application.HasReason(err, application.ReasonOwnerNotCurrent) || called {
			t.Fatalf("lock roundtrip callback err=%v called=%t", err, called)
		}
	})
}

func TestRepositoryOwnerLockRejectsPostBindReplacement(t *testing.T) {
	fixture := newOwnerLockFixture(t)
	store := openOwnerStore(t, fixture)
	acquisition := acquisitionAtEpoch(1)
	phase, err := openRepositoryOwnerScopeLock(fixture.directory, acquisition.Scope)
	if err != nil {
		t.Fatal(err)
	}
	bound, _, _ := acquireAndBindOwner(t, phase, store, acquisition)
	defer bound.Close()
	if err := bound.claimRuntime(); err != nil {
		t.Fatal(err)
	}
	concrete := bound.(*darwinRepositoryOwnerLock)
	name := concrete.physical.name
	if err := os.Rename(filepath.Join(fixture.ownerPath, name), filepath.Join(fixture.ownerPath, "post-bind.lock")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.ownerPath, name), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(filepath.Join(fixture.ownerPath, name), -1, os.Getgid()); err != nil {
		t.Fatal(err)
	}
	called := false
	err = bound.WithCurrentOwnerLock(context.Background(), acquisition, func() error { called = true; return nil })
	if !application.HasReason(err, application.ReasonOwnerNotCurrent) || called {
		t.Fatalf("post-bind replacement err=%v called=%t", err, called)
	}
}
