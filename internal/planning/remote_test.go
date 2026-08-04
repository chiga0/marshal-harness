package planning

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runRemoteGit runs a git command in the fixture repository with a config-free
// environment and fails the test on error.
func runRemoteGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	)
	if err := command.Run(); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

// remoteFixture returns a repository root whose origin remote points at url.
func remoteFixture(t *testing.T, url string) string {
	t.Helper()
	root, _ := gitFixture(t)
	runRemoteGit(t, root, "remote", "add", "origin", url)
	return root
}

func TestResolveRemoteSuccess(t *testing.T) {
	url := "https://github.com/chiga0/marshal-harness.git"
	root := remoteFixture(t, url)

	got, err := ResolveRemote(context.Background(), root, "origin")
	if err != nil {
		t.Fatalf("ResolveRemote(origin) = %v", err)
	}
	if got != url {
		t.Fatalf("ResolveRemote(origin) = %q, want %q", got, url)
	}

	// URLs may carry credentials and must be returned verbatim, unmodified.
	secretURL := "https://user:s3cret-token@example.invalid/repo.git"
	runRemoteGit(t, root, "remote", "add", "cred", secretURL)
	got, err = ResolveRemote(context.Background(), root, "cred")
	if err != nil {
		t.Fatalf("ResolveRemote(cred) = %v", err)
	}
	if got != secretURL {
		t.Fatalf("ResolveRemote(cred) = %q, want %q", got, secretURL)
	}
}

func TestResolveRemoteUnknownRemoteFailsClosed(t *testing.T) {
	root := remoteFixture(t, "https://github.com/chiga0/marshal-harness.git")
	_, err := ResolveRemote(context.Background(), root, "no-such-remote")
	if err == nil || err.Error() != ErrRemoteGitFailed {
		t.Fatalf("ResolveRemote(no-such-remote) = %v, want %q", err, ErrRemoteGitFailed)
	}
}

func TestResolveRemoteRejectsOptionInjection(t *testing.T) {
	root := remoteFixture(t, "https://github.com/chiga0/marshal-harness.git")
	sentinel := filepath.Join(root, "pwned")
	for _, remote := range []string{
		"--upload-pack=" + "touch " + sentinel,
		"--push",
		"-C",
		"-c core.hooksPath=/",
	} {
		_, err := ResolveRemote(context.Background(), root, remote)
		if err == nil || err.Error() != ErrRemoteNameInvalid {
			t.Errorf("ResolveRemote(%q) = %v, want %q", remote, err, ErrRemoteNameInvalid)
		}
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("sentinel file was created: %v", err)
	}
}

func TestResolveRemoteArgvPassThrough(t *testing.T) {
	root, _ := gitFixture(t)
	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	fakeGit(t, "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> "+argvFile+"; done\nprintf 'https://example.invalid/repo.git\\n'\n")

	// Contains shell metacharacters but no leading/trailing whitespace, so it
	// is a legal name that must reach git as one intact argv element.
	hostile := "origin; touch " + filepath.Join(root, "pwned")
	got, err := ResolveRemote(context.Background(), root, hostile)
	if err != nil {
		t.Fatalf("ResolveRemote(argv) = %v", err)
	}
	if got != "https://example.invalid/repo.git" {
		t.Fatalf("ResolveRemote(argv) = %q", got)
	}

	data, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("fake git did not capture argv: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	want := []string{"-C", root, "remote", "get-url", hostile}
	if len(lines) != len(want) {
		t.Fatalf("argv = %q, want %q", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, lines[i], want[i])
		}
	}
	if _, err := os.Stat(filepath.Join(root, "pwned")); !os.IsNotExist(err) {
		t.Fatalf("sentinel file was created: %v", err)
	}
}

func TestResolveRemoteNeverUsesShell(t *testing.T) {
	root := remoteFixture(t, "https://github.com/chiga0/marshal-harness.git")
	sentinel := filepath.Join(root, "pwned")
	// If a shell interpreted this name, the file would be created. Without a
	// shell it is simply a nonexistent remote name.
	_, err := ResolveRemote(context.Background(), root, "origin; touch "+sentinel)
	if err == nil || err.Error() != ErrRemoteGitFailed {
		t.Fatalf("ResolveRemote(shell metacharacters) = %v, want %q", err, ErrRemoteGitFailed)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("shell metacharacters were executed: %v", err)
	}
}

func TestResolveRemoteInvalidRoot(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{
		"relative/path",
		"",
		"/tmp/marshal-harness-does-not-exist/../elsewhere",
		filepath.Join(t.TempDir(), "missing"),
		file,
	} {
		_, err := ResolveRemote(context.Background(), root, "origin")
		if err == nil || err.Error() != ErrRemoteRootInvalid {
			t.Errorf("ResolveRemote(root %q) = %v, want %q", root, err, ErrRemoteRootInvalid)
		}
	}
}

func TestResolveRemoteInvalidName(t *testing.T) {
	root := remoteFixture(t, "https://github.com/chiga0/marshal-harness.git")
	for _, remote := range []string{
		"",
		" ",
		" origin",
		"origin ",
		"\torigin",
		"origin\n",
		"-origin",
		"a\x00b",
		"a\rb",
		"a\nb",
	} {
		_, err := ResolveRemote(context.Background(), root, remote)
		if err == nil || err.Error() != ErrRemoteNameInvalid {
			t.Errorf("ResolveRemote(remote %q) = %v, want %q", remote, err, ErrRemoteNameInvalid)
		}
	}
}

func TestResolveRemoteCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// An invalid root proves the context is checked before anything else.
	_, err := ResolveRemote(ctx, filepath.Join(t.TempDir(), "missing"), "origin")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveRemote(canceled) = %v, want context.Canceled", err)
	}
}

func TestResolveRemoteTimeout(t *testing.T) {
	root := remoteFixture(t, "https://github.com/chiga0/marshal-harness.git")
	fakeGit(t, "#!/bin/sh\nsleep 30\n")

	previous := remoteResolveTimeout
	remoteResolveTimeout = 100 * time.Millisecond
	defer func() { remoteResolveTimeout = previous }()

	start := time.Now()
	_, err := ResolveRemote(context.Background(), root, "origin")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ResolveRemote(timeout) = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}
}

func TestResolveRemoteRejectsOversizedStdout(t *testing.T) {
	root := remoteFixture(t, "https://github.com/chiga0/marshal-harness.git")
	// Emits ~8KB of 'a' with exit status 0, beyond the 4096 byte bound.
	fakeGit(t, "#!/bin/sh\nhead -c 8192 /dev/zero | tr '\\0' 'a'\n")
	_, err := ResolveRemote(context.Background(), root, "origin")
	if err == nil || err.Error() != ErrRemoteOutputInvalid {
		t.Fatalf("ResolveRemote(oversized stdout) = %v, want %q", err, ErrRemoteOutputInvalid)
	}
}

func TestResolveRemoteRejectsMalformedOutput(t *testing.T) {
	root := remoteFixture(t, "https://github.com/chiga0/marshal-harness.git")
	secret := "https://s3cret:x-token@example.invalid/repo.git"
	for name, output := range map[string]string{
		"multi-line":          "https://a.invalid/1.git\nhttps://a.invalid/2.git\n",
		"two trailing breaks": "https://a.invalid/repo.git\n\n",
		"blank":               "\n",
		"empty":               "",
		"leading space":       " https://a.invalid/repo.git\n",
		"trailing space":      "https://a.invalid/repo.git \n",
		"trailing cr":         "https://a.invalid/repo.git\r\n",
		"embedded cr":         "https://a.invalid/re\rop.git\n",
		"embedded nul":        "https://a.invalid/re\\000po.git\n",
		"secret multi-line":   secret + "\n" + secret + "\n",
	} {
		fakeGit(t, "#!/bin/sh\nprintf '"+output+"'\n")
		_, err := ResolveRemote(context.Background(), root, "origin")
		if err == nil || err.Error() != ErrRemoteOutputInvalid {
			t.Errorf("ResolveRemote(%s output) = %v, want %q", name, err, ErrRemoteOutputInvalid)
		}
		if err != nil && (strings.Contains(err.Error(), "s3cret") ||
			strings.Contains(err.Error(), "x-token") ||
			strings.Contains(err.Error(), "a.invalid")) {
			t.Errorf("ResolveRemote(%s output) leaked git output: %v", name, err)
		}
	}
}

func TestResolveRemoteAcceptsURLWithoutTrailingNewline(t *testing.T) {
	root := remoteFixture(t, "https://github.com/chiga0/marshal-harness.git")
	url := "https://a.invalid/repo.git"
	fakeGit(t, "#!/bin/sh\nprintf '"+url+"'\n")
	got, err := ResolveRemote(context.Background(), root, "origin")
	if err != nil {
		t.Fatalf("ResolveRemote(no trailing newline) = %v", err)
	}
	if got != url {
		t.Fatalf("ResolveRemote(no trailing newline) = %q, want %q", got, url)
	}
}

func TestResolveRemoteDiscardsStderr(t *testing.T) {
	root := remoteFixture(t, "https://github.com/chiga0/marshal-harness.git")
	fakeGit(t, "#!/bin/sh\necho SECRET=topsecret >&2\nexit 1\n")
	_, err := ResolveRemote(context.Background(), root, "origin")
	if err == nil || err.Error() != ErrRemoteGitFailed {
		t.Fatalf("ResolveRemote(stderr) = %v, want %q", err, ErrRemoteGitFailed)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("stderr leaked into error: %v", err)
	}
}

func TestResolveRemoteRestrictedEnvironment(t *testing.T) {
	root := remoteFixture(t, "https://github.com/chiga0/marshal-harness.git")
	envFile := filepath.Join(t.TempDir(), "env.txt")
	fakeGit(t, "#!/bin/sh\nenv > "+envFile+"\nexit 1\n")
	t.Setenv("MARSHAL_SECRET_SHOULD_NOT_LEAK", "leak")

	_, err := ResolveRemote(context.Background(), root, "origin")
	if err == nil || err.Error() != ErrRemoteGitFailed {
		t.Fatalf("ResolveRemote(env dump) = %v, want %q", err, ErrRemoteGitFailed)
	}
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("fake git did not capture its environment: %v", err)
	}
	env := string(data)

	for _, wanted := range []string{
		"LC_ALL=C",
		"LANG=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=/dev/null",
		"PATH=",
	} {
		if !strings.Contains(env, wanted) {
			t.Errorf("git environment missing %q", wanted)
		}
	}
	if strings.Contains(env, "MARSHAL_SECRET_SHOULD_NOT_LEAK") {
		t.Error("unrelated parent environment leaked into git")
	}
}
