//go:build linux

package codex

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type replayingAtomicAuthoritySource struct {
	material authorityProbeMaterial
	calls    int
}

func (source *replayingAtomicAuthoritySource) LoadFreshAuthority(context.Context) (authorityProbeMaterial, error) {
	source.calls++
	return source.material, nil
}

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
	helper := buildRawFDHelper(t)
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

	// Negative control: direct execution deliberately inherits fd3..fd7. The
	// raw helper must detect the first leaked authority descriptor before any
	// language runtime or libc can allocate and reuse a descriptor number.
	leaking := exec.Command(helper)
	leaking.ExtraFiles = []*os.File{schema, result, worktree, launcher, target}
	err = leaking.Run()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 22 {
		t.Fatalf("negative control err = %v, want authority leak exit 22", err)
	}

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

// buildRawFDHelper produces a static native image with a custom _start. Its
// first operations are direct fcntl(F_GETFD) syscalls: no dynamic loader,
// libc, Go runtime, procfs traversal, or file allocation can reuse fd5..fd7
// before the inheritance assertions execute.
func buildRawFDHelper(t *testing.T) string {
	t.Helper()
	var syscallBody string
	switch runtime.GOARCH {
	case "amd64":
		syscallBody = `
#define SYS_WRITE 1
#define SYS_FCNTL 72
#define SYS_EXIT 60
static inline long syscall3(long number, long first, long second, long third) {
    long result;
    __asm__ volatile ("syscall" : "=a"(result) : "a"(number), "D"(first), "S"(second), "d"(third) : "rcx", "r11", "memory");
    return result;
}`
	case "arm64":
		syscallBody = `
#define SYS_WRITE 64
#define SYS_FCNTL 25
#define SYS_EXIT 93
static inline long syscall3(long number, long first, long second, long third) {
    register long x0 __asm__("x0") = first;
    register long x1 __asm__("x1") = second;
    register long x2 __asm__("x2") = third;
    register long x8 __asm__("x8") = number;
    __asm__ volatile ("svc 0" : "+r"(x0) : "r"(x1), "r"(x2), "r"(x8) : "memory");
    return x0;
}`
	default:
		t.Fatalf("raw fd helper is unavailable on Linux/%s", runtime.GOARCH)
	}
	source := syscallBody + `
#define F_GETFD 1
#define EBADF_RESULT -9
static __attribute__((noreturn)) void finish(long code) {
    syscall3(SYS_EXIT, code, 0, 0);
    __builtin_unreachable();
}
void _start(void) {
    if (syscall3(SYS_FCNTL, 3, F_GETFD, 0) < 0) finish(20);
    if (syscall3(SYS_FCNTL, 4, F_GETFD, 0) < 0) finish(21);
    if (syscall3(SYS_FCNTL, 5, F_GETFD, 0) != EBADF_RESULT) finish(22);
    if (syscall3(SYS_FCNTL, 6, F_GETFD, 0) != EBADF_RESULT) finish(22);
    if (syscall3(SYS_FCNTL, 7, F_GETFD, 0) != EBADF_RESULT) finish(22);
    if (syscall3(SYS_WRITE, 4, (long)"ok", 2) != 2) finish(23);
    finish(0);
}`
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "fd-helper.c")
	helper := filepath.Join(directory, "fd-helper")
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	compiler, err := exec.LookPath("cc")
	if err != nil {
		t.Fatal("Linux native fd test requires cc")
	}
	build := exec.Command(compiler, "-nostdlib", "-static", "-fno-stack-protector", "-fno-pie", "-no-pie", "-Wl,--build-id=none", "-o", helper, sourcePath)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build raw fd helper: %v: %s", err, output)
	}
	return helper
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
		!strings.Contains(string(record.Data), secureFDPublicReason) || strings.Contains(string(record.Data), "launcher initialization failed:") {
		t.Fatalf("probe = %s, want auditable launcher initialization failure", record.Data)
	}
	if err := adapter.BindConformance(context.Background(), digest("a")); !errors.Is(err, ErrPlatformUnsupported) || !strings.Contains(err.Error(), secureFDPublicReason) || strings.Contains(err.Error(), "injected launcher init failure") {
		t.Fatalf("BindConformance err = %v, want fixed safe platform error", err)
	}
	fixture := newRunFixture(t, supportedVersionOutput, "exit 0")
	fixture.adapter.unsafePathExecutionForTest = false
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrPlatformUnsupported) || !strings.Contains(err.Error(), secureFDPublicReason) || strings.Contains(err.Error(), "injected launcher init failure") {
		t.Fatalf("Run err = %v, want fixed safe platform error", err)
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

func TestLinuxAuthorityIdentityComesFromHeldExecutable(t *testing.T) {
	executable := nativeFakeExecutable(t)
	snapshot, err := snapshotExecutable(context.Background(), executable, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.close()
	identity, err := snapshot.authorityExecutableIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if identity.CanonicalRealpath != executable || identity.SHA256 != snapshot.identity.digest || identity.Version != snapshot.identity.version || identity.MountIDUnique == 0 || identity.Inode == 0 {
		t.Fatalf("held authority identity = %+v", identity)
	}
	child, err := heldExecutableStat(snapshot.file)
	if err != nil {
		t.Fatal(err)
	}
	if child.inode == identity.Inode && child.mountIDUnique == identity.MountIDUnique {
		t.Fatal("stable source identity was conflated with the per-attempt sealed child")
	}
	second, err := snapshotExecutable(context.Background(), executable, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer second.close()
	secondIdentity, err := second.authorityExecutableIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if secondIdentity != identity {
		t.Fatalf("two snapshots of the stable source differ: first=%+v second=%+v", identity, secondIdentity)
	}
	digest, err := canonicalDigest(identity)
	if err != nil || !validDigest(digest) {
		t.Fatalf("held authority identity digest = %q, err = %v", digest, err)
	}
}

func TestLinuxAuthorityIdentityRejectsSourceReplacement(t *testing.T) {
	executable := nativeFakeExecutable(t)
	first, err := snapshotExecutable(context.Background(), executable, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.close()
	firstIdentity, err := first.authorityExecutableIdentity()
	if err != nil {
		t.Fatal(err)
	}
	replacement := nativeFakeExecutable(t)
	if err := os.Rename(replacement, executable); err != nil {
		t.Fatal(err)
	}
	if heldAgain, err := first.authorityExecutableIdentity(); err != nil || heldAgain != firstIdentity {
		t.Fatalf("held source identity changed after pathname replacement: identity=%+v err=%v", heldAgain, err)
	}
	second, err := snapshotExecutable(context.Background(), executable, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer second.close()
	secondIdentity, err := second.authorityExecutableIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if secondIdentity == firstIdentity || secondIdentity.Inode == firstIdentity.Inode {
		t.Fatalf("replacement source reused authority identity: first=%+v second=%+v", firstIdentity, secondIdentity)
	}
}

func TestLinuxProbeReverifiesAtomicAuthorityWithoutAdmissionCache(t *testing.T) {
	executable := nativeFakeExecutable(t)
	adapter, err := New(executable, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	adapter.unsafePathExecutionForTest = false
	snapshot, err := adapter.inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := snapshot.authorityExecutableIdentity()
	snapshot.close()
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiledCodexContractBinding()
	if err != nil {
		t.Fatal(err)
	}
	fixture := newTestAuthorityBundleFor(t, identity, compiled)
	source := &replayingAtomicAuthoritySource{material: authorityMaterialFromFixture(t, fixture)}
	adapter.now = func() time.Time { return fixture.now }
	adapter.bindAtomicAuthoritySource(source)
	first, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first.Data), `"probeStatus":"supported"`) || !strings.Contains(string(first.Data), `"codexAuthority"`) {
		t.Fatalf("fresh atomic authority was not admitted: %s", first.Data)
	}
	second, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(second.Data), `"probeStatus":"unsupported"`) || !strings.Contains(string(second.Data), `"codex_evidence_invalid"`) {
		t.Fatalf("replayed proof reused cached admission: %s", second.Data)
	}
	if source.calls != 2 {
		t.Fatalf("authority source calls = %d, want one per Probe", source.calls)
	}
}

func TestLinuxRunReverifiesFreshAuthorityAfterSupportedProbe(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "worker-launched")
	fixture := newRunFixture(t, supportedVersionOutput, "touch "+shellQuote(marker)+"; exit 91")
	useFixtureExecutable(t, &fixture, nativeFakeExecutable(t))
	fixture.adapter.legacyAuthorityForTest = false
	snapshot, err := fixture.adapter.inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := snapshot.authorityExecutableIdentity()
	snapshot.close()
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiledCodexContractBinding()
	if err != nil {
		t.Fatal(err)
	}
	authority := newTestAuthorityBundleFor(t, identity, compiled)
	source := &replayingAtomicAuthoritySource{material: authorityMaterialFromFixture(t, authority)}
	fixture.adapter.now = func() time.Time { return authority.now }
	fixture.adapter.bindAtomicAuthoritySource(source)
	probe, err := fixture.adapter.Probe(context.Background())
	if err != nil || !strings.Contains(string(probe.Data), `"probeStatus":"supported"`) {
		t.Fatalf("Probe = %s, err = %v", probe.Data, err)
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil {
		t.Fatal("Run reused Probe admission instead of rejecting replayed host proof")
	}
	if source.calls != 2 {
		t.Fatalf("authority source calls = %d, want Probe + launch", source.calls)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("worker launched after fresh authority verification failed")
	}
}

func TestLinuxFDExecBindsProbeAndRunToHeldInodes(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "replacement-ran")
	fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
	useFixtureExecutable(t, &fixture, nativeFakeExecutable(t))
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
	executable := nativeFakeExecutable(t)
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
