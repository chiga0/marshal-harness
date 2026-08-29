//go:build darwin

package resultingress

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
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

func TestPreparedDarwinSealRequiresDescriptorBackedStore(t *testing.T) {
	pathStore, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SealPi0843DarwinPreparedExecutionStore(pathStore, "/does/not/matter", nil); !errors.Is(err, ErrPreparedExecutionUnavailable) {
		t.Fatalf("SealPi0843DarwinPreparedExecutionStore err = %v, want unavailable", err)
	}
}
