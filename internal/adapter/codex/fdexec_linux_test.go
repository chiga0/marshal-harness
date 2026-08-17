//go:build linux

package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLinuxExecutableMemfdIsSealedAgainstSameUIDMutation(t *testing.T) {
	executable := fakeExecutable(t, supportedVersionOutput, "exit 0")
	file, err := sealedExecutableFD(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	want := unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
	got, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got&want != want {
		t.Fatalf("memfd seals = %#x, want %#x", got, want)
	}
	if _, err := unix.Pwrite(int(file.Fd()), []byte("attacker"), 0); err == nil {
		t.Fatal("same-UID write changed a sealed executable image")
	}
}

func TestLinuxFDExecBindsProbeAndRunToHeldInodes(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "replacement-ran")
	fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
	fixture.adapter.unsafePathExecutionForTest = false
	fixture.adapter.testHook = func(stage string) {
		if stage != "after-identity-verify" {
			return
		}
		replacement := fakeScript(supportedVersionOutput, "touch "+shellQuote(marker)+"; exit 91")
		if err := os.WriteFile(fixture.executable, []byte(replacement), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("replacement Codex pathname was executed")
	}
}

func TestLinuxFDExecProbeUsesOneHeldInodeAndCreatesNoSnapshots(t *testing.T) {
	before, err := filepath.Glob(filepath.Join(os.TempDir(), ".marshal-codex-*-*"))
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "replacement-version-ran")
	executable := fakeExecutable(t, supportedVersionOutput, "exit 0")
	adapter, err := New(executable, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	adapter.unsafePathExecutionForTest = false
	adapter.testHook = func(stage string) {
		if stage != "after-executable-digest" {
			return
		}
		replacement := "#!/bin/sh\nif [ \"${1:-}\" = \"--version\" ]; then touch " + shellQuote(marker) + "; printf '%s\\n' " + shellQuote(supportedVersionOutput) + "; exit 0; fi\nexit 9\n"
		if err := os.WriteFile(executable, []byte(replacement), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	record, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(record.Data), `"binaryVersion":"0.145.0"`) {
		t.Fatalf("probe = %s", record.Data)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("replacement version pathname was executed")
	}
	after, err := filepath.Glob(filepath.Join(os.TempDir(), ".marshal-codex-*-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("fd execution left snapshot residue: before=%d after=%d", len(before), len(after))
	}
}
