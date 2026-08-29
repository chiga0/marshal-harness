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

func acquisitionAtEpoch(epoch uint64) resultingress.ControlOwnerAcquisition {
	value := currentProcessAcquisition()
	value.OwnerEpoch = epoch
	return value
}

func acquireAndReplay(t *testing.T, phase repositoryOwnerScopeLock, store *resultingress.DurableStore, expectedEpoch uint64, expectedDigest string, acquisition resultingress.ControlOwnerAcquisition) resultingress.ControlOwnerState {
	t.Helper()
	result, err := phase.acquireOwner(context.Background(), store, expectedEpoch, expectedDigest, acquisition)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Appended || result.State.Acquisition != acquisition {
		t.Fatalf("unexpected append result: %#v", result)
	}
	replayed, found, err := store.OpenOwner(acquisition.Scope)
	if err != nil || !found || replayed != result.State {
		t.Fatalf("exact owner replay failed: found=%t state=%#v err=%v", found, replayed, err)
	}
	return replayed
}

func TestRepositoryOwnerLockRequiresTwoPhaseExactReplay(t *testing.T) {
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
	_ = acquireAndReplay(t, phase, store, 0, "", acquisition)
	bound, err := phase.bindAcquisition(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := phase.bindAcquisition(store); !application.HasReason(err, application.ReasonOwnerNotCurrent) {
		t.Fatalf("repeated bind err=%v", err)
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

func TestRepositoryOwnerLockBindIsCreationOnceOnForeignStore(t *testing.T) {
	fixture := newOwnerLockFixture(t)
	acquisition := acquisitionAtEpoch(1)
	phase, err := openRepositoryOwnerScopeLock(fixture.directory, acquisition.Scope)
	if err != nil {
		t.Fatal(err)
	}
	defer phase.Close()
	store := openOwnerStore(t, fixture)
	_ = acquireAndReplay(t, phase, store, 0, "", acquisition)
	foreign, err := resultingress.OpenResultIngressStore(filepath.Join(fixture.base, "foreign-result-ingress"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := phase.bindAcquisition(foreign); !application.HasReason(err, application.ReasonOwnerNotCurrent) {
		t.Fatalf("foreign replay store bind err=%v", err)
	}
	if _, err := phase.bindAcquisition(store); !application.HasReason(err, application.ReasonOwnerNotCurrent) {
		t.Fatalf("bind retry after foreign store err=%v", err)
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
	if _, err := phase.acquireOwner(context.Background(), store, 0, "", acquisition); !application.HasReason(err, application.ReasonOwnerNotCurrent) {
		t.Fatalf("closed Phase A acquire err=%v", err)
	}
	if _, err := phase.bindAcquisition(store); !application.HasReason(err, application.ReasonOwnerNotCurrent) {
		t.Fatalf("closed Phase A bind err=%v", err)
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
	store := openOwnerStore(t, fixture)
	firstAcquisition := acquisitionAtEpoch(1)
	first, err := openRepositoryOwnerScopeLock(fixture.directory, firstAcquisition.Scope)
	if err != nil {
		t.Fatal(err)
	}
	firstState := acquireAndReplay(t, first, store, 0, "", firstAcquisition)
	// Simulate loss of the successful append response before Phase B bind.
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := openRepositoryOwnerScopeLock(fixture.directory, firstAcquisition.Scope)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	secondAcquisition := acquisitionAtEpoch(2)
	secondState := acquireAndReplay(t, second, store, 1, firstState.FactDigest, secondAcquisition)
	bound, err := second.bindAcquisition(store)
	if err != nil {
		t.Fatal(err)
	}
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
		result resultingress.ControlOwnerAppendResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			result, err := phase.acquireOwner(context.Background(), store, 0, "", acquisition)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)
	workers.Wait()
	close(outcomes)
	var successes, rejections int
	for outcome := range outcomes {
		if outcome.err == nil && outcome.result.Appended {
			successes++
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
		if _, err := phase.acquireOwner(context.Background(), store, 0, "", acquisition); err == nil {
			t.Fatal("same owner directory rename roundtrip admitted owner append")
		}
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
		if _, err := phase.acquireOwner(context.Background(), store, 0, "", acquisition); err == nil {
			t.Fatal("same lock entry rename roundtrip admitted owner append")
		}
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
		if _, err := phase.acquireOwner(context.Background(), store, 0, "", acquisition); err == nil {
			t.Fatal("directory current-name ABA admitted owner append")
		}
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
		if _, err := phase.acquireOwner(context.Background(), store, 0, "", acquisition); err == nil {
			t.Fatal("lock entry ABA admitted owner append")
		}
		if _, found, err := store.OpenOwner(acquisition.Scope); err != nil || found {
			t.Fatalf("entry ABA mutated owner authority: found=%t err=%v", found, err)
		}
	})
}

func TestRepositoryOwnerLockRejectsRoundtripDuringBindAndBoundCallback(t *testing.T) {
	t.Run("directory-before-bind", func(t *testing.T) {
		fixture := newOwnerLockFixture(t)
		store := openOwnerStore(t, fixture)
		acquisition := acquisitionAtEpoch(1)
		phase, err := openRepositoryOwnerScopeLock(fixture.directory, acquisition.Scope)
		if err != nil {
			t.Fatal(err)
		}
		defer phase.Close()
		_ = acquireAndReplay(t, phase, store, 0, "", acquisition)
		before := ownerLedgerBytes(t, fixture)
		moved := fixture.ownerPath + "-before-bind"
		renameAwayAndBackSameObject(t, fixture.ownerPath, moved)
		if _, err := phase.bindAcquisition(store); !application.HasReason(err, application.ReasonOwnerNotCurrent) {
			t.Fatalf("directory roundtrip bind err=%v", err)
		}
		if after := ownerLedgerBytes(t, fixture); !bytes.Equal(before, after) {
			t.Fatalf("failed bind wrote ledger: before=%q after=%q", before, after)
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
		_ = acquireAndReplay(t, phase, store, 0, "", acquisition)
		bound, err := phase.bindAcquisition(store)
		if err != nil {
			t.Fatal(err)
		}
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
	_ = acquireAndReplay(t, phase, store, 0, "", acquisition)
	bound, err := phase.bindAcquisition(store)
	if err != nil {
		t.Fatal(err)
	}
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
