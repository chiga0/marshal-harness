//go:build darwin && arm64

package resultingress

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

func openHeldDarwinTestStore(t *testing.T) (string, *DurableStore) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	store, err := OpenDarwinResultIngressStore(held)
	if err != nil {
		t.Fatalf("OpenDarwinResultIngressStore: %v", err)
	}
	return directory, store
}

func appendHeldDarwinTestFact(t *testing.T, store *DurableStore, suffix string) {
	t.Helper()
	if err := store.RecordQuarantined(ReasonMalformed, canonical.DigestBytes([]byte("drc-"+suffix)), canonical.DigestBytes([]byte("envelope-"+suffix)), time.Unix(1, 0)); err != nil {
		t.Fatalf("RecordQuarantined: %v", err)
	}
}

func TestHeldDarwinResultIngressRejectsDirectoryCurrentNameReplacementBeforeAppend(t *testing.T) {
	directory, store := openHeldDarwinTestStore(t)
	appendHeldDarwinTestFact(t, store, "first")

	moved := directory + ".held"
	t.Cleanup(func() { _ = os.RemoveAll(moved) })
	before, err := os.ReadFile(filepath.Join(directory, resultIngressStoreFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(directory, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	err = store.RecordQuarantined(ReasonMalformed, canonical.DigestBytes([]byte("drc-second")), canonical.DigestBytes([]byte("envelope-second")), time.Unix(2, 0))
	if !errors.Is(err, ErrPreparedExecutionUnavailable) {
		t.Fatalf("RecordQuarantined err = %v, want fail-closed current-name rejection", err)
	}
	after, err := os.ReadFile(filepath.Join(moved, resultIngressStoreFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("held ledger changed after directory current-name replacement")
	}
	if _, err := os.Stat(filepath.Join(directory, resultIngressStoreFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement directory acquired a ledger: %v", err)
	}
}

func TestHeldDarwinResultIngressRejectsLedgerAndLockEntryABA(t *testing.T) {
	for _, name := range []string{resultIngressStoreFileName, resultIngressStoreLockName} {
		t.Run(name, func(t *testing.T) {
			directory, store := openHeldDarwinTestStore(t)
			appendHeldDarwinTestFact(t, store, "first")
			ledgerBefore, err := os.ReadFile(filepath.Join(directory, resultIngressStoreFileName))
			if err != nil {
				t.Fatal(err)
			}
			entry := filepath.Join(directory, name)
			moved := entry + ".held"
			if err := os.Rename(entry, moved); err != nil {
				t.Fatal(err)
			}
			replacement := []byte("replacement-must-not-change")
			if err := os.WriteFile(entry, replacement, 0o600); err != nil {
				t.Fatal(err)
			}

			err = store.RecordQuarantined(ReasonMalformed, canonical.DigestBytes([]byte("drc-second")), canonical.DigestBytes([]byte("envelope-second")), time.Unix(2, 0))
			if !errors.Is(err, ErrPreparedExecutionUnavailable) {
				t.Fatalf("RecordQuarantined err = %v, want fail-closed entry ABA rejection", err)
			}
			ledgerPath := filepath.Join(directory, resultIngressStoreFileName)
			if name == resultIngressStoreFileName {
				ledgerPath = moved
			}
			ledgerAfter, err := os.ReadFile(ledgerPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(ledgerBefore, ledgerAfter) {
				t.Fatal("held ledger changed after authority entry ABA")
			}
			current, err := os.ReadFile(entry)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(current, replacement) {
				t.Fatal("replacement authority entry was modified")
			}
		})
	}
}

func TestHeldDarwinResultIngressRejectsNamedObjectDriftBeforeAppend(t *testing.T) {
	for _, mutate := range []struct {
		name string
		fn   func(t *testing.T, ledger string)
	}{
		{name: "mode", fn: func(t *testing.T, ledger string) {
			if err := os.Chmod(ledger, 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hardlink", fn: func(t *testing.T, ledger string) {
			if err := os.Link(ledger, ledger+".link"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", fn: func(t *testing.T, ledger string) {
			moved := ledger + ".held"
			if err := os.Rename(ledger, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, ledger); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			directory, store := openHeldDarwinTestStore(t)
			appendHeldDarwinTestFact(t, store, "first")
			ledger := filepath.Join(directory, resultIngressStoreFileName)
			before, err := os.ReadFile(ledger)
			if err != nil {
				t.Fatal(err)
			}
			mutate.fn(t, ledger)
			err = store.RecordQuarantined(ReasonMalformed, canonical.DigestBytes([]byte("drift-drc")), canonical.DigestBytes([]byte("drift-envelope")), time.Unix(2, 0))
			if !errors.Is(err, ErrPreparedExecutionUnavailable) {
				t.Fatalf("RecordQuarantined err=%v, want identity-drift rejection", err)
			}
			current := ledger
			if mutate.name == "symlink" {
				current = ledger + ".held"
			}
			after, err := os.ReadFile(current)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("held ledger changed after named-object drift")
			}
		})
	}
}

func TestPreparedDarwinSealRequiresDescriptorBackedStore(t *testing.T) {
	pathStore, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SealPi0843DarwinPreparedExecutionStore(context.Background(), pathStore, nil, CurrentOwnerBinding{}, "/does/not/matter", nil); !errors.Is(err, ErrPreparedExecutionUnavailable) {
		t.Fatalf("SealPi0843DarwinPreparedExecutionStore err = %v, want unavailable", err)
	}
}

func TestPreparedDarwinSealIsOwnerBoundSingleUseAndCloseIsIdempotent(t *testing.T) {
	_, store := openHeldDarwinTestStore(t)
	fixed, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	core, err := processsupervisor.ObserveCurrentCore(fixed)
	if err != nil {
		t.Fatalf("ObserveCurrentCore: %v", err)
	}
	id := attemptTestIdentity()
	acquisition := ControlOwnerAcquisition{
		Scope: attemptTestOwnerScope(id), OwnerEpoch: 1,
		OwnerUID: core.UID, OwnerGID: core.GID, OwnerProcess: core.Process, OwnerBinary: core.Binary,
		ObserverIdentity: "darwin-owner-observer/v1", ObservedAt: "2026-08-29T00:00:00Z",
	}
	verifier := attemptOwnerVerifier{want: acquisition}
	owner, err := store.AcquireOwner(context.Background(), verifier, 0, "", acquisition)
	if err != nil {
		t.Fatalf("AcquireOwner: %v", err)
	}
	binding := CurrentOwnerBinding{Scope: acquisition.Scope, OwnerEpoch: acquisition.OwnerEpoch, ControlOwnerAcquiredFactDigest: owner.State.FactDigest}
	controlRoot := t.TempDir()
	if err := os.Chmod(controlRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	control, err := os.Open(controlRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	sealed, err := SealPi0843DarwinPreparedExecutionStore(context.Background(), store, verifier, binding, fixed, control)
	if err != nil || sealed != store {
		t.Fatalf("SealPi0843DarwinPreparedExecutionStore store=%p sealed=%p err=%v", store, sealed, err)
	}
	if _, err := SealPi0843DarwinPreparedExecutionStore(context.Background(), store, verifier, binding, fixed, control); !errors.Is(err, ErrPreparedExecutionUnavailable) {
		t.Fatalf("second Seal err=%v, want single-use rejection", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := store.RecordQuarantined(ReasonMalformed, canonical.DigestBytes([]byte("closed-drc")), canonical.DigestBytes([]byte("closed-envelope")), time.Unix(3, 0)); !errors.Is(err, ErrResultIngressClosed) {
		t.Fatalf("closed store err=%v, want ErrResultIngressClosed", err)
	}
}

func TestHeldDarwinResultIngressUnlockMarksPostWriteDriftOutcomeUnknown(t *testing.T) {
	directory, store := openHeldDarwinTestStore(t)
	files, ok := store.heldFiles.(*heldDarwinAuthorityFiles)
	if !ok {
		t.Fatal("descriptor-backed store has unexpected backend")
	}
	unlock, err := files.lockExclusive()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.ledger.Write([]byte("durable-before-drift\n")); err != nil || files.ledger.Sync() != nil {
		t.Fatalf("write held ledger: %v", err)
	}
	files.operationWrote = true
	moved := directory + ".moved"
	t.Cleanup(func() { _ = os.RemoveAll(moved) })
	if err := os.Rename(directory, moved); err != nil {
		t.Fatal(err)
	}
	if err := unlock(); !errors.Is(err, ErrResultIngressOutcomeUnknown) || !files.poisoned {
		t.Fatalf("unlock err=%v poisoned=%v, want outcome-unknown poison", err, files.poisoned)
	}
}

func TestHeldDarwinResultIngressPoisonPreventsSiblingAppend(t *testing.T) {
	directory, store := openHeldDarwinTestStore(t)
	appendHeldDarwinTestFact(t, store, "first")
	before, err := os.ReadFile(filepath.Join(directory, resultIngressStoreFileName))
	if err != nil {
		t.Fatal(err)
	}
	files := store.heldFiles.(*heldDarwinAuthorityFiles)
	files.mu.Lock()
	files.poisoned = true
	files.mu.Unlock()
	err = store.RecordQuarantined(ReasonMalformed, canonical.DigestBytes([]byte("poison-drc")), canonical.DigestBytes([]byte("poison-envelope")), time.Unix(4, 0))
	if !errors.Is(err, ErrResultIngressOutcomeUnknown) {
		t.Fatalf("poisoned append err=%v, want outcome unknown", err)
	}
	after, err := os.ReadFile(filepath.Join(directory, resultIngressStoreFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("poisoned writer appended a sibling fact")
	}
}

func TestHeldDarwinResultIngressUnlocksAfterPanic(t *testing.T) {
	_, store := openHeldDarwinTestStore(t)
	recovered := false
	func() {
		defer func() { recovered = recover() != nil }()
		_ = store.withExclusive(func() error { panic("test panic") })
	}()
	if !recovered {
		t.Fatal("withExclusive did not propagate panic")
	}
	done := make(chan error, 1)
	go func() { done <- store.withExclusive(func() error { return nil }) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("operation after panic: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("held mutex/flock leaked after panic")
	}
}
