package stablegotest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMergeGOFLAGSPreservesExistingAndAddsOneStableExec(t *testing.T) {
	digest := strings.Repeat("a", 64)
	got, err := MergeGOFLAGS("-count=1 -p=2", "/opt/marshal/bin/marshal", "/Users/test/.marshal/test-exec", digest)
	if err != nil {
		t.Fatalf("MergeGOFLAGS: %v", err)
	}
	wantSuffix := `"-exec='/opt/marshal/bin/marshal' __go-test-exec --slot-root '/Users/test/.marshal/test-exec' --marshal-sha256 ` + digest + `"`
	if got != "-count=1 -p=2 "+wantSuffix {
		t.Fatalf("GOFLAGS = %q", got)
	}
	tokens, err := splitQuoted(got)
	if err != nil || len(tokens) != 3 || !strings.HasPrefix(tokens[2], "-exec=") {
		t.Fatalf("outer tokens = %#v err=%v", tokens, err)
	}
	execCommand, err := splitQuoted(strings.TrimPrefix(tokens[2], "-exec="))
	if err != nil {
		t.Fatalf("inner split: %v", err)
	}
	want := []string{"/opt/marshal/bin/marshal", InternalCommand, "--slot-root", "/Users/test/.marshal/test-exec", "--marshal-sha256", digest}
	if len(execCommand) != len(want) {
		t.Fatalf("exec command = %#v", execCommand)
	}
	for index := range want {
		if execCommand[index] != want[index] {
			t.Fatalf("exec command[%d] = %q, want %q", index, execCommand[index], want[index])
		}
	}
}

func TestMergeGOFLAGSRejectsConflictAndUnsafePaths(t *testing.T) {
	for _, test := range []struct {
		name       string
		existing   string
		marshal    string
		slot       string
		reasonPart string
	}{
		{name: "short exec", existing: "-exec=/tmp/other", marshal: "/bin/marshal", slot: "/tmp/slot", reasonPart: "conflicting"},
		{name: "long exec", existing: "--exec='/tmp/other helper'", marshal: "/bin/marshal", slot: "/tmp/slot", reasonPart: "conflicting"},
		{name: "relative marshal", marshal: "bin/marshal", slot: "/tmp/slot", reasonPart: "absolute"},
		{name: "quoted root", marshal: "/bin/marshal", slot: `/tmp/slot'bad`, reasonPart: "represented"},
		{name: "unterminated existing", existing: `"-count=1`, marshal: "/bin/marshal", slot: "/tmp/slot", reasonPart: "invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := MergeGOFLAGS(test.existing, test.marshal, test.slot, strings.Repeat("a", 64))
			if err == nil || !strings.Contains(err.Error(), test.reasonPart) {
				t.Fatalf("err = %v, want %q", err, test.reasonPart)
			}
		})
	}
}

func TestMergeGOFLAGSRejectsInvalidMarshalDigest(t *testing.T) {
	if _, err := MergeGOFLAGS("", "/bin/marshal", "/tmp/slot", "not-a-digest"); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("err = %v", err)
	}
}

func TestWithEnvironmentDoesNotBindPackageTestImage(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin production binding")
	}
	input := []string{"PATH=/usr/bin", "CUSTOM=value"}
	got, err := WithEnvironment(input)
	if err != nil {
		t.Fatalf("WithEnvironment: %v", err)
	}
	if len(got) != len(input) {
		t.Fatalf("environment = %#v", got)
	}
	for index := range input {
		if got[index] != input[index] {
			t.Fatalf("environment[%d] = %q", index, got[index])
		}
	}
}

func TestChildEnvironmentPreservesUpstreamAndRemovesOnlyStableExec(t *testing.T) {
	input := []string{
		"PATH=/usr/bin",
		"CUSTOM_PROJECT_FLAG=required",
		`GOFLAGS="-exec='/fixed/marshal' __go-test-exec --slot-root '/fixed/root' --marshal-sha256 ` + strings.Repeat("a", 64) + `"`,
	}
	got := childEnvironment(input)
	want := []string{input[0], input[1], input[2], activeEnvironment + "=1"}
	if len(got) != len(want) {
		t.Fatalf("environment = %#v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("environment[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}

func TestParseArgsRequiresFixedAbsoluteRootAndInput(t *testing.T) {
	digest := strings.Repeat("a", 64)
	root, gotDigest, source, child, err := parseArgs([]string{"--slot-root", "/fixed/root", "--marshal-sha256", digest, "/private/tmp/go-build/test", "-test.v"})
	if err != nil || root != "/fixed/root" || gotDigest != digest || source != "/private/tmp/go-build/test" || len(child) != 1 || child[0] != "-test.v" {
		t.Fatalf("root=%q digest=%q source=%q child=%#v err=%v", root, gotDigest, source, child, err)
	}
	for _, args := range [][]string{
		{"--slot-root", "relative", "--marshal-sha256", digest, "/tmp/test"},
		{"--slot-root", "/fixed/root", "--marshal-sha256", digest, "relative"},
		{"--slot-root", "/", "--marshal-sha256", digest, "/tmp/test"},
		{"--slot-root", "/fixed/root", "--marshal-sha256", "bad", "/tmp/test"},
	} {
		if _, _, _, _, err := parseArgs(args); err == nil {
			t.Fatalf("parseArgs(%#v) unexpectedly passed", args)
		}
	}
}

func TestInstallRejectsHardlinkWithoutTruncatingExternalFile(t *testing.T) {
	root := privateRoot(t)
	directory, err := openRoot(root)
	if err != nil {
		t.Fatalf("openRoot: %v", err)
	}
	defer directory.Close()
	external := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(external, []byte("must-survive"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(external, filepath.Join(root, incomingName)); err != nil {
		t.Fatal(err)
	}
	source := executableFixture(t, "source")
	if _, current, err := install(directory, source); err == nil {
		current.Close()
		t.Fatal("hardlinked incoming unexpectedly accepted")
	}
	data, err := os.ReadFile(external)
	if err != nil || string(data) != "must-survive" {
		t.Fatalf("external file changed: %q err=%v", data, err)
	}
}

func TestInstallRejectsSymlinkHardlinkSourceAndOversize(t *testing.T) {
	for _, test := range []struct {
		name   string
		create func(*testing.T) string
	}{
		{name: "symlink", create: func(t *testing.T) string {
			target := executableFixture(t, "target")
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			return link
		}},
		{name: "hardlink", create: func(t *testing.T) string {
			target := executableFixture(t, "target")
			link := filepath.Join(t.TempDir(), "hardlink")
			if err := os.Link(target, link); err != nil {
				t.Fatal(err)
			}
			return link
		}},
		{name: "oversize", create: func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "oversize")
			file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o700)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(maxTestBinaryBytes + 1); err != nil {
				file.Close()
				t.Fatal(err)
			}
			file.Close()
			return path
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := privateRoot(t)
			directory, err := openRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			if _, current, err := install(directory, test.create(t)); err == nil {
				current.Close()
				t.Fatal("unsafe source unexpectedly accepted")
			}
		})
	}
}

func TestVerifyCurrentRejectsPathIdentityDrift(t *testing.T) {
	root := privateRoot(t)
	directory, err := openRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	digest, held, err := install(directory, executableFixture(t, "source"))
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	defer held.Close()
	replacement := filepath.Join(root, "replacement")
	if err := os.WriteFile(replacement, []byte("replacement"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, filepath.Join(root, currentName)); err != nil {
		t.Fatal(err)
	}
	if err := verifyCurrent(root, held, digest); err == nil {
		t.Fatal("current identity drift unexpectedly accepted")
	}
}

func TestWaitWithCancellationEscalatesTERMThenKILL(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wait := make(chan error, 1)
	var signals []syscall.Signal
	wantErr := errors.New("killed")
	err := waitWithCancellation(ctx, wait, time.Millisecond, func(signal syscall.Signal) error {
		signals = append(signals, signal)
		if signal == syscall.SIGKILL {
			wait <- wantErr
		}
		return nil
	})
	if !errors.Is(err, wantErr) || len(signals) != 2 || signals[0] != syscall.SIGTERM || signals[1] != syscall.SIGKILL {
		t.Fatalf("err=%v signals=%v", err, signals)
	}
}

func TestWaitWithCancellationStopsAfterTERMExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wait := make(chan error, 1)
	var signals []syscall.Signal
	err := waitWithCancellation(ctx, wait, time.Second, func(signal syscall.Signal) error {
		signals = append(signals, signal)
		wait <- nil
		return nil
	})
	if err != nil || len(signals) != 1 || signals[0] != syscall.SIGTERM {
		t.Fatalf("err=%v signals=%v", err, signals)
	}
}

func privateRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func executableFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
