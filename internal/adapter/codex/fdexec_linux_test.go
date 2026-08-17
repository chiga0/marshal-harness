//go:build linux

package codex

import (
	"context"
	"errors"
	"os"
	"os/exec"
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

func TestLinuxRunningImageRejectsProcfsLookalike(t *testing.T) {
	lookalike := t.TempDir()
	if err := os.Symlink(".", filepath.Join(lookalike, "self")); err != nil {
		t.Fatal(err)
	}
	if _, err := openRunningExecutableFDAt(lookalike); err == nil || !strings.Contains(err.Error(), "not a procfs") {
		t.Fatalf("lookalike proc tree err = %v, want fail-closed procfs rejection", err)
	}
}

func TestLinuxNativeTargetClosesLauncherAuthorityFDs(t *testing.T) {
	helperSource := `package main
import (
	"errors"
	"fmt"
	"os"
	"golang.org/x/sys/unix"
)
func main() {
    for _, fd := range []int{3, 4} {
        if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil {
            fmt.Fprintf(os.Stderr, "evidence fd %d unavailable: %v", fd, err)
            os.Exit(21)
        }
    }
    for _, fd := range []int{5, 6, 7} {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
            fmt.Fprintf(os.Stderr, "authority fd %d remained open: %v", fd, err)
            os.Exit(22)
        }
    }
    if _, err := unix.Write(4, []byte("ok")); err != nil {
        fmt.Fprintf(os.Stderr, "write result fd: %v", err)
        os.Exit(23)
    }
}
`
	helperDir := t.TempDir()
	helperGo := filepath.Join(helperDir, "main.go")
	helper := filepath.Join(helperDir, "native-helper")
	if err := os.WriteFile(helperGo, []byte(helperSource), 0o600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", helper, helperGo)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build native helper: %v: %s", err, output)
	}
	target, err := sealedExecutableFD(helper)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	launcher, err := secureLauncherFD()
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.CreateTemp(t.TempDir(), "schema-")
	if err != nil {
		t.Fatal(err)
	}
	defer schema.Close()
	result, err := os.CreateTemp(t.TempDir(), "result-")
	if err != nil {
		t.Fatal(err)
	}
	defer result.Close()
	worktree, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer worktree.Close()

	command := exec.Command(secureFDPath(6), codexLauncherArgument, secureFDPath(7), "", "")
	command.ExtraFiles = []*os.File{schema, result, worktree, launcher, target}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native helper exec: %v: %s", err, output)
	}
	if _, err := result.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(result.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ok" {
		t.Fatalf("native helper result = %q, want ok", data)
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
