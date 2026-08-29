//go:build darwin

package processsupervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

type boundaryDriftMechanics struct {
	fakeMechanics
	drift func()
}

func (mechanics boundaryDriftMechanics) Spawn(context.Context, SpawnPayload) (MechanicsResult, error) {
	mechanics.drift()
	return fakeResult("fake-spawn"), nil
}

func TestHeldSessionControlFilesDescriptorRelativeIdentityAndNonce(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	nonce := strings.Repeat("0123456789abcdef", 4)
	if err := os.WriteFile(filepath.Join(root, nonceFileName), []byte(nonce), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, JournalFileName), []byte("journal"), 0o600); err != nil {
		t.Fatal(err)
	}
	nonceFile, err := openControlFileAt(directory, nonceFileName)
	if err != nil {
		t.Fatal(err)
	}
	nonceIdentity, _, err := observeControlFile(nonceFile)
	_ = nonceFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	journalFile, err := openControlFileAt(directory, JournalFileName)
	if err != nil {
		t.Fatal(err)
	}
	journalIdentity, _, err := observeControlFile(journalFile)
	_ = journalFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	expected := SessionControlFiles{Nonce: nonceIdentity, Journal: journalIdentity}
	held, err := openHeldSessionControlFiles(directory, expected)
	if err != nil {
		t.Fatal(err)
	}
	defer held.close()
	got, err := readSessionNonce(held, canonical.DigestBytes([]byte(nonce)))
	if err != nil || got != nonce {
		t.Fatalf("nonce read=%q error=%v", got, err)
	}

	if err := os.Rename(filepath.Join(root, nonceFileName), filepath.Join(root, "old.nonce")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, nonceFileName), []byte(nonce), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := revalidateHeldSessionControlFiles(directory, held, expected); !errors.Is(err, ErrConflict) {
		t.Fatalf("ABA replacement error=%v", err)
	}
}

func TestHeldSessionControlFilesRejectSymlinkHardlinkAndWeakMode(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "symlink", setup: func(t *testing.T, root string) {
			target := filepath.Join(root, "target")
			if err := os.WriteFile(target, []byte(strings.Repeat("a", nonceBytes)), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(root, nonceFileName)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hardlink", setup: func(t *testing.T, root string) {
			target := filepath.Join(root, "target")
			if err := os.WriteFile(target, []byte(strings.Repeat("a", nonceBytes)), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(target, filepath.Join(root, nonceFileName)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "weak-mode", setup: func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, nonceFileName), []byte(strings.Repeat("a", nonceBytes)), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.setup(t, root)
			directory, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			file, err := openControlFileAt(directory, nonceFileName)
			if err == nil {
				defer file.Close()
				_, _, err = observeControlFile(file)
			}
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("boundary error=%v", err)
			}
		})
	}
}

func TestCommandBoundaryRejectsPreAndPostReceiptDriftWithoutResponse(t *testing.T) {
	for _, phase := range []string{"pre-command", "post-receipt"} {
		t.Run(phase, func(t *testing.T) {
			boundary, journal, bootstrap, root := commandBoundaryFixture(t)
			mechanics := boundaryDriftMechanics{drift: func() {
				if phase == "post-receipt" {
					if err := os.Chmod(filepath.Join(root, JournalFileName), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}}
			session, err := NewSession(bootstrap, journal, mechanics, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			session.state = sessionBound
			session.supervisorStartedFact = digest("c")
			if phase == "pre-command" {
				if err := os.Link(filepath.Join(root, nonceFileName), filepath.Join(root, "nonce-link")); err != nil {
					t.Fatal(err)
				}
			}
			request := commandRequest(t, bootstrap.SessionID, CommandSpawn, "spawn-boundary", 1, CommandGenesisDigest, bootstrap.CurrentAuthorityHead, time.Now().Add(time.Minute), validSpawnPayload())
			response, err := handleSessionCommand(session, boundary, mustCanonical(request))
			if !errors.Is(err, ErrConflict) || response.SchemaVersion != "" || session.State() != string(sessionIntervention) {
				t.Fatalf("response=%+v error=%v state=%s", response, err, session.State())
			}
			sequence := journal.Snapshot().Sequence
			if phase == "pre-command" && sequence != 1 || phase == "post-receipt" && sequence != 3 {
				t.Fatalf("phase=%s journal sequence=%d", phase, sequence)
			}
		})
	}
}

func commandBoundaryFixture(t *testing.T) (sessionControlBoundary, *Journal, BootstrapRequest, string) {
	t.Helper()
	// AF_UNIX paths on Darwin are bounded. t.TempDir includes the full test and
	// subtest name, so use an explicit short root while retaining deterministic
	// cleanup instead of weakening the production socket-path boundary.
	root, err := os.MkdirTemp("/private/tmp", "marshal-ps-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove short control root: %v", err)
		}
	})
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	_, directoryIdentity, err := observeControlDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := validBootstrap()
	bootstrap.ControlDirectoryIdentity = directoryIdentity
	nonceHeld, err := writeHeldOpenatExclusive(directory, nonceFileName, []byte(bootstrap.SessionNonce), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nonceHeld.Close() })
	journalFile, err := openatExclusive(directory, JournalFileName, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Sync(); err != nil {
		_ = journalFile.Close()
		t.Fatal(err)
	}
	nonceIdentity, _, err := observeControlFile(nonceHeld)
	if err != nil {
		_ = journalFile.Close()
		t.Fatal(err)
	}
	journalIdentity, _, err := observeControlFile(journalFile)
	if err != nil {
		_ = journalFile.Close()
		t.Fatal(err)
	}
	controlFiles := SessionControlFiles{Nonce: nonceIdentity, Journal: journalIdentity}
	held := &heldSessionControlFiles{nonce: nonceHeld, journal: journalFile, identity: controlFiles}
	if err := revalidateHeldSessionControlFiles(directory, held, controlFiles); err != nil {
		_ = journalFile.Close()
		t.Fatal(err)
	}
	journal, err := OpenJournal(journalFile)
	if err != nil {
		_ = journalFile.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	listener, err := listenUnixAt(directory, controlSocket)
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	t.Cleanup(func() { _ = listener.Close() })
	if err := unix.Fchmodat(int(directory.Fd()), controlSocket, 0o600, 0); err != nil {
		t.Fatal(err)
	}
	if err := directory.Sync(); err != nil {
		t.Fatal(err)
	}
	socket, err := observeControlSocket(directory)
	if err != nil {
		t.Fatal(err)
	}
	return sessionControlBoundary{directory: directory, directoryIdentity: directoryIdentity, socket: socket, heldFiles: held, controlFiles: controlFiles}, journal, bootstrap, root
}
