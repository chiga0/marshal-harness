//go:build linux

package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLinuxRunningImageUsesVerifiedProcfsMagicLink(t *testing.T) {
	fd, err := unix.Open("/proc/self/exe", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if fd >= 0 {
		_ = unix.Close(fd)
	}
	if !errors.Is(err, unix.ELOOP) {
		t.Fatalf("ordinary O_NOFOLLOW opener err = %v, want ELOOP", err)
	}
	running, err := openRunningExecutableFD()
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()
	opened, err := running.Stat()
	if err != nil {
		t.Fatal(err)
	}
	linked, err := os.Stat("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(opened, linked) {
		t.Fatal("verified procfs opener did not bind the running image inode")
	}
}

func TestLinuxLauncherInitializationFailureMakesAllSurfacesUnsupported(t *testing.T) {
	originalFile, originalErr := launcherFile, launcherErr
	launcherFile, launcherErr = nil, errors.New("injected launcher init failure")
	defer func() { launcherFile, launcherErr = originalFile, originalErr }()
	if secureFDExecutionAvailable() {
		t.Fatal("failed launcher initialization reported fd execution available")
	}
	executable := fakeExecutable(t, supportedVersionOutput, "exit 0")
	adapter, err := New(executable, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	adapter.unsafePathExecutionForTest = false
	record, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(record.Data), `"probeStatus":"unsupported"`) ||
		!strings.Contains(string(record.Data), "launcher initialization failed") {
		t.Fatalf("probe = %s, want auditable launcher initialization failure", record.Data)
	}
	if err := adapter.BindConformance(context.Background(), digest("a")); !errors.Is(err, ErrPlatformUnsupported) {
		t.Fatalf("BindConformance err = %v, want platform unsupported", err)
	}
	fixture := newRunFixture(t, supportedVersionOutput, "exit 0")
	fixture.adapter.unsafePathExecutionForTest = false
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrPlatformUnsupported) {
		t.Fatalf("Run err = %v, want platform unsupported", err)
	}
}

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
