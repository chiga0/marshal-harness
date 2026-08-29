//go:build darwin

package provider

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type heldRegistrationTestDirectory struct {
	parentPath    string
	directoryPath string
	directory     *os.File
}

func newHeldRegistrationTestDirectory(t *testing.T) heldRegistrationTestDirectory {
	t.Helper()
	parentPath := t.TempDir()
	if err := os.Chmod(parentPath, 0o700); err != nil {
		t.Fatalf("chmod test parent: %v", err)
	}
	directoryPath := filepath.Join(parentPath, "provider-registrations")
	if err := os.Mkdir(directoryPath, 0o700); err != nil {
		t.Fatalf("mkdir held registration directory: %v", err)
	}
	directory, err := os.Open(directoryPath)
	if err != nil {
		t.Fatalf("open held registration directory: %v", err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	return heldRegistrationTestDirectory{parentPath: parentPath, directoryPath: directoryPath, directory: directory}
}

func requireHeldRegistrationUnavailable(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrHeldRegistrationUnavailable) {
		t.Fatalf("expected ErrHeldRegistrationUnavailable, got %v", err)
	}
}

func readHeldRegistrationLedger(t *testing.T, directoryPath string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(directoryPath, ledgerFileName))
	if err != nil {
		t.Fatalf("read held registration ledger: %v", err)
	}
	return content
}

func TestDarwinHeldRegistrationStoreRestartRecoversRegistrationAndLifecycle(t *testing.T) {
	fixture := newHeldRegistrationTestDirectory(t)
	store, err := OpenDarwinRegistrationStore(fixture.directory)
	if err != nil {
		t.Fatalf("OpenDarwinRegistrationStore: %v", err)
	}
	registration := storeRegistration(t, "held-restart")
	if _, err := store.Put(registration); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Revoke(registration.RegistrationId); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenDarwinRegistrationStore(fixture.directory)
	if err != nil {
		t.Fatalf("reopen held registration store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.Get(registration.RegistrationId)
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	registration.LifecycleState = LifecycleStateRevoked
	if !reflect.DeepEqual(got, registration) {
		t.Fatalf("recovered registration differs:\n got %+v\nwant %+v", got, registration)
	}
}

func TestDarwinHeldRegistrationStoreRegistrationResponseLossReplaysExactFact(t *testing.T) {
	fixture := newHeldRegistrationTestDirectory(t)
	store, err := OpenDarwinRegistrationStore(fixture.directory)
	if err != nil {
		t.Fatalf("OpenDarwinRegistrationStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	lost := errors.New("injected registration response loss")
	injected := false
	store.held.afterSync = func() error {
		if injected {
			return nil
		}
		injected = true
		return lost
	}
	registration := storeRegistration(t, "held-response-loss")
	got, err := store.Put(registration)
	if err != nil {
		t.Fatalf("Put did not converge after durable response loss: %v", err)
	}
	if !reflect.DeepEqual(got, registration) {
		t.Fatalf("Put returned a different registration: got %+v want %+v", got, registration)
	}
	content := readHeldRegistrationLedger(t, fixture.directoryPath)
	if facts := bytes.Count(content, []byte{'\n'}); facts != 1 {
		t.Fatalf("response loss left %d registration facts, want 1", facts)
	}
}

func TestDarwinHeldRegistrationStoreLifecycleResponseLossReplaysExactFact(t *testing.T) {
	fixture := newHeldRegistrationTestDirectory(t)
	store, err := OpenDarwinRegistrationStore(fixture.directory)
	if err != nil {
		t.Fatalf("OpenDarwinRegistrationStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	registration := storeRegistration(t, "held-lifecycle-loss")
	if _, err := store.Put(registration); err != nil {
		t.Fatalf("Put: %v", err)
	}
	injected := false
	store.held.afterSync = func() error {
		if injected {
			return nil
		}
		injected = true
		return errors.New("injected lifecycle response loss")
	}
	if err := store.Expire(registration.RegistrationId); err != nil {
		t.Fatalf("Expire did not converge after durable response loss: %v", err)
	}
	got, err := store.Get(registration.RegistrationId)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LifecycleState != LifecycleStateExpired {
		t.Fatalf("lifecycle state = %q, want expired", got.LifecycleState)
	}
	content := readHeldRegistrationLedger(t, fixture.directoryPath)
	if facts := bytes.Count(content, []byte{'\n'}); facts != 2 {
		t.Fatalf("response loss left %d facts, want 2", facts)
	}
}

func TestDarwinHeldRegistrationStoreRejectsPartialTailOnRestart(t *testing.T) {
	fixture := newHeldRegistrationTestDirectory(t)
	store, err := OpenDarwinRegistrationStore(fixture.directory)
	if err != nil {
		t.Fatalf("OpenDarwinRegistrationStore: %v", err)
	}
	if _, err := store.Put(storeRegistration(t, "held-partial-tail")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ledgerPath := filepath.Join(fixture.directoryPath, ledgerFileName)
	info, err := os.Stat(ledgerPath)
	if err != nil {
		t.Fatalf("stat ledger: %v", err)
	}
	if err := os.Truncate(ledgerPath, info.Size()-1); err != nil {
		t.Fatalf("truncate ledger newline: %v", err)
	}
	_, err = OpenDarwinRegistrationStore(fixture.directory)
	requireHeldRegistrationUnavailable(t, err)
}

func TestDarwinHeldRegistrationStoreRejectsDirectoryRenameReplacementAndABA(t *testing.T) {
	t.Run("replacement", func(t *testing.T) {
		fixture := newHeldRegistrationTestDirectory(t)
		store, err := OpenDarwinRegistrationStore(fixture.directory)
		if err != nil {
			t.Fatalf("OpenDarwinRegistrationStore: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		away := fixture.directoryPath + "-away"
		if err := os.Rename(fixture.directoryPath, away); err != nil {
			t.Fatalf("rename held directory away: %v", err)
		}
		if err := os.Mkdir(fixture.directoryPath, 0o700); err != nil {
			t.Fatalf("create replacement directory: %v", err)
		}
		_, err = store.Put(storeRegistration(t, "held-directory-replacement"))
		requireHeldRegistrationUnavailable(t, err)
		if content := readHeldRegistrationLedger(t, away); len(content) != 0 {
			t.Fatalf("rejected replacement mutated held ledger: %q", content)
		}
	})

	t.Run("rename-away-and-back", func(t *testing.T) {
		fixture := newHeldRegistrationTestDirectory(t)
		store, err := OpenDarwinRegistrationStore(fixture.directory)
		if err != nil {
			t.Fatalf("OpenDarwinRegistrationStore: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		away := fixture.directoryPath + "-aba"
		if err := os.Rename(fixture.directoryPath, away); err != nil {
			t.Fatalf("rename held directory away: %v", err)
		}
		if err := os.Rename(away, fixture.directoryPath); err != nil {
			t.Fatalf("restore held directory name: %v", err)
		}
		observed, err := observeHeldRegistrationDirectory(int(store.held.parent.Fd()), true)
		if err != nil {
			t.Fatalf("observe parent after ABA: %v", err)
		}
		if observed.mutation == store.held.parentID.mutation {
			t.Fatal("filesystem did not expose parent mutation for deterministic ABA fixture")
		}
		_, err = store.Put(storeRegistration(t, "held-directory-aba"))
		requireHeldRegistrationUnavailable(t, err)
	})
}

func TestDarwinHeldRegistrationStoreRejectsLedgerReplacement(t *testing.T) {
	fixture := newHeldRegistrationTestDirectory(t)
	store, err := OpenDarwinRegistrationStore(fixture.directory)
	if err != nil {
		t.Fatalf("OpenDarwinRegistrationStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ledgerPath := filepath.Join(fixture.directoryPath, ledgerFileName)
	if err := os.Rename(ledgerPath, ledgerPath+"-away"); err != nil {
		t.Fatalf("rename ledger: %v", err)
	}
	replacement, err := os.OpenFile(ledgerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create replacement ledger: %v", err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatalf("close replacement ledger: %v", err)
	}
	_, err = store.Put(storeRegistration(t, "held-ledger-replacement"))
	requireHeldRegistrationUnavailable(t, err)
}

func TestDarwinHeldRegistrationStoreRejectsHostileInitialObjects(t *testing.T) {
	t.Run("directory-mode", func(t *testing.T) {
		fixture := newHeldRegistrationTestDirectory(t)
		if err := os.Chmod(fixture.directoryPath, 0o755); err != nil {
			t.Fatalf("chmod directory: %v", err)
		}
		_, err := OpenDarwinRegistrationStore(fixture.directory)
		requireHeldRegistrationUnavailable(t, err)
	})

	t.Run("ledger-mode", func(t *testing.T) {
		fixture := newHeldRegistrationTestDirectory(t)
		ledgerPath := filepath.Join(fixture.directoryPath, ledgerFileName)
		if err := os.WriteFile(ledgerPath, nil, 0o644); err != nil {
			t.Fatalf("create wrong-mode ledger: %v", err)
		}
		_, err := OpenDarwinRegistrationStore(fixture.directory)
		requireHeldRegistrationUnavailable(t, err)
	})

	t.Run("ledger-symlink", func(t *testing.T) {
		fixture := newHeldRegistrationTestDirectory(t)
		target := filepath.Join(fixture.parentPath, "target")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatalf("create symlink target: %v", err)
		}
		if err := os.Symlink(target, filepath.Join(fixture.directoryPath, ledgerFileName)); err != nil {
			t.Fatalf("create ledger symlink: %v", err)
		}
		_, err := OpenDarwinRegistrationStore(fixture.directory)
		requireHeldRegistrationUnavailable(t, err)
	})

	t.Run("ledger-hardlink", func(t *testing.T) {
		fixture := newHeldRegistrationTestDirectory(t)
		target := filepath.Join(fixture.parentPath, "target")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatalf("create hardlink target: %v", err)
		}
		if err := os.Link(target, filepath.Join(fixture.directoryPath, ledgerFileName)); err != nil {
			t.Fatalf("create ledger hardlink: %v", err)
		}
		_, err := OpenDarwinRegistrationStore(fixture.directory)
		requireHeldRegistrationUnavailable(t, err)
	})
}

func TestDarwinHeldRegistrationStoreFirstCreationUsesOwnerOnlyLedger(t *testing.T) {
	fixture := newHeldRegistrationTestDirectory(t)
	store, err := OpenDarwinRegistrationStore(fixture.directory)
	if err != nil {
		t.Fatalf("OpenDarwinRegistrationStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	info, err := os.Stat(filepath.Join(fixture.directoryPath, ledgerFileName))
	if err != nil {
		t.Fatalf("stat registration ledger: %v", err)
	}
	if info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		t.Fatalf("ledger mode = %v, want owner-only regular", info.Mode())
	}
}
