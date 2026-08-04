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

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// gitFixture initializes a throwaway repository with a single commit and
// returns its root and the HEAD commit SHA.
func gitFixture(t *testing.T) (root, sha string) {
	t.Helper()
	requireGit(t)
	root = t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		command.Env = append(os.Environ(),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=/dev/null",
		)
		output, err := command.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(output))
	}
	run("init", "-q")
	run("config", "user.email", "fixture@example.com")
	run("config", "user.name", "Fixture")
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "file.txt")
	run("commit", "-q", "-m", "fixture commit")
	return root, run("rev-parse", "HEAD")
}

// fakeGit installs a stub git executable as the first PATH entry. The script
// is run via /bin/sh and receives whatever argv ResolveBase produced.
func fakeGit(t *testing.T, script string) {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestResolveBaseSuccess(t *testing.T) {
	root, sha := gitFixture(t)

	got, err := ResolveBase(context.Background(), root, "HEAD")
	if err != nil {
		t.Fatalf("ResolveBase(HEAD) = %v", err)
	}
	if got != sha {
		t.Fatalf("ResolveBase(HEAD) = %q, want %q", got, sha)
	}

	// A literal object ID must also resolve.
	got, err = ResolveBase(context.Background(), root, sha)
	if err != nil {
		t.Fatalf("ResolveBase(literal sha) = %v", err)
	}
	if got != sha {
		t.Fatalf("ResolveBase(literal sha) = %q, want %q", got, sha)
	}
}

func TestResolveBaseUnknownRefFailsClosed(t *testing.T) {
	root, _ := gitFixture(t)
	_, err := ResolveBase(context.Background(), root, "no-such-ref")
	if err == nil || err.Error() != ErrBaseGitFailed {
		t.Fatalf("ResolveBase(no-such-ref) = %v, want %q", err, ErrBaseGitFailed)
	}
}

func TestResolveBaseRejectsOptionInjection(t *testing.T) {
	root, _ := gitFixture(t)
	sentinel := filepath.Join(root, "pwned")
	for _, ref := range []string{
		"--upload-pack=" + "touch " + sentinel,
		"--exec=touch " + sentinel,
		"-C",
		"-c core.hooksPath=/",
	} {
		_, err := ResolveBase(context.Background(), root, ref)
		if err == nil || err.Error() != ErrBaseRefInvalid {
			t.Errorf("ResolveBase(%q) = %v, want %q", ref, err, ErrBaseRefInvalid)
		}
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("sentinel file was created: %v", err)
	}
}

func TestResolveBaseNeverUsesShell(t *testing.T) {
	root, _ := gitFixture(t)
	sentinel := filepath.Join(root, "pwned")
	// If a shell interpreted this ref, the file would be created. Without a
	// shell it is simply an unresolvable ref name.
	_, err := ResolveBase(context.Background(), root, "HEAD; touch "+sentinel)
	if err == nil || err.Error() != ErrBaseGitFailed {
		t.Fatalf("ResolveBase(shell metacharacters) = %v, want %q", err, ErrBaseGitFailed)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("shell metacharacters were executed: %v", err)
	}
}

func TestResolveBaseInvalidRoot(t *testing.T) {
	_, sha := gitFixture(t)

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
		_, err := ResolveBase(context.Background(), root, sha)
		if err == nil || err.Error() != ErrBaseRootInvalid {
			t.Errorf("ResolveBase(root %q) = %v, want %q", root, err, ErrBaseRootInvalid)
		}
	}
}

func TestResolveBaseInvalidRef(t *testing.T) {
	root, _ := gitFixture(t)
	for _, ref := range []string{
		"",
		" ",
		" main",
		"main ",
		"\tHEAD",
		"HEAD\n",
		"-ref",
		"a\x00b",
		"a\rb",
		"a\nb",
	} {
		_, err := ResolveBase(context.Background(), root, ref)
		if err == nil || err.Error() != ErrBaseRefInvalid {
			t.Errorf("ResolveBase(ref %q) = %v, want %q", ref, err, ErrBaseRefInvalid)
		}
	}
}

func TestResolveBaseCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ResolveBase(ctx, "/tmp", "HEAD")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveBase(canceled) = %v, want context.Canceled", err)
	}
}

func TestResolveBaseTimeout(t *testing.T) {
	root, _ := gitFixture(t)
	fakeGit(t, "#!/bin/sh\nsleep 30\n")

	previous := baseResolveTimeout
	baseResolveTimeout = 100 * time.Millisecond
	defer func() { baseResolveTimeout = previous }()

	start := time.Now()
	_, err := ResolveBase(context.Background(), root, "HEAD")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ResolveBase(timeout) = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}
}

func TestResolveBaseRejectsOversizedStdout(t *testing.T) {
	root, _ := gitFixture(t)
	// Emits ~128KB of lowercase hex characters with exit status 0.
	fakeGit(t, "#!/bin/sh\nhead -c 131072 /dev/zero | tr '\\0' 'a'\n")
	_, err := ResolveBase(context.Background(), root, "HEAD")
	if err == nil || err.Error() != ErrBaseOutputInvalid {
		t.Fatalf("ResolveBase(oversized stdout) = %v, want %q", err, ErrBaseOutputInvalid)
	}
}

func TestResolveBaseDiscardsStderr(t *testing.T) {
	root, _ := gitFixture(t)
	fakeGit(t, "#!/bin/sh\necho SECRET=topsecret >&2\nexit 1\n")
	_, err := ResolveBase(context.Background(), root, "HEAD")
	if err == nil || err.Error() != ErrBaseGitFailed {
		t.Fatalf("ResolveBase(stderr) = %v, want %q", err, ErrBaseGitFailed)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("stderr leaked into error: %v", err)
	}
}

func TestResolveBaseRejectsMalformedObjectID(t *testing.T) {
	root, _ := gitFixture(t)
	valid := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 40 chars
	for name, output := range map[string]string{
		"too short":        strings.Repeat("a", 39) + "\n",
		"too long":         strings.Repeat("a", 65) + "\n",
		"uppercase":        strings.ToUpper(valid) + "\n",
		"non-hex":          strings.Repeat("g", 40) + "\n",
		"trailing garbage": valid + " extra\n",
		"two lines":        valid + "\n" + valid + "\n",
		"blank":            "\n",
		"empty":            "",
	} {
		fakeGit(t, "#!/bin/sh\nprintf '"+output+"'\n")
		_, err := ResolveBase(context.Background(), root, "HEAD")
		if err == nil || err.Error() != ErrBaseOutputInvalid {
			t.Errorf("ResolveBase(%s output) = %v, want %q", name, err, ErrBaseOutputInvalid)
		}
	}
}

func TestResolveBaseAcceptsObjectIDWithoutTrailingNewline(t *testing.T) {
	root, _ := gitFixture(t)
	valid := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fakeGit(t, "#!/bin/sh\nprintf '"+valid+"'\n")
	got, err := ResolveBase(context.Background(), root, "HEAD")
	if err != nil {
		t.Fatalf("ResolveBase(no trailing newline) = %v", err)
	}
	if got != valid {
		t.Fatalf("ResolveBase(no trailing newline) = %q, want %q", got, valid)
	}
}

func TestResolveBaseRestrictedEnvironment(t *testing.T) {
	root, _ := gitFixture(t)
	envFile := filepath.Join(t.TempDir(), "env.txt")
	fakeGit(t, "#!/bin/sh\nenv > "+envFile+"\nexit 1\n")
	t.Setenv("MARSHAL_SECRET_SHOULD_NOT_LEAK", "leak")

	_, err := ResolveBase(context.Background(), root, "HEAD")
	if err == nil || err.Error() != ErrBaseGitFailed {
		t.Fatalf("ResolveBase(env dump) = %v, want %q", err, ErrBaseGitFailed)
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
