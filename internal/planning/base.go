package planning

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Base resolution errors are fixed, categorized strings. They never carry the
// repository root, the ref, git stderr, or any other subprocess output, so
// callers can compare and log them deterministically without leaking data.
// Context cancellation and deadline errors are the only exceptions: those are
// returned as the context's own error.
const (
	ErrBaseRootInvalid     = "resolve base: repository root is invalid"
	ErrBaseRefInvalid      = "resolve base: base ref is invalid"
	ErrBaseRefNotImmutable = "resolve base: base ref must be an immutable full commit SHA"
	ErrBaseGitFailed       = "resolve base: git rev-parse failed"
	ErrBaseOutputInvalid   = "resolve base: git output is not a valid commit object ID"
)

// baseResolveTimeout bounds a single rev-parse invocation. It is a variable so
// tests can exercise the timeout path without waiting for real git.
var baseResolveTimeout = 10 * time.Second

// maxBaseOutput bounds how much stdout a rev-parse invocation may produce. A
// valid object ID plus a trailing newline is at most 65 bytes; anything larger
// is discarded and treated as invalid output.
const maxBaseOutput = 256

// ResolveBase resolves ref to a single commit object ID inside the repository
// at repositoryRoot. The ref must already be exactly one immutable full git
// object ID: floating refs such as branch names, tag names, HEAD,
// remote-tracking names, or relative spellings are rejected fail-closed
// before any git invocation, so the locked baseline can never move after
// planning. It invokes git directly via argv (never through a
// shell), with a restricted environment and a bounded timeout and output
// buffer. The command runs in its own process group and the entire group is
// terminated when the timeout expires, so grandchildren holding the output
// pipe cannot keep the invocation alive. It is fail-closed: anything other
// than a single 40–64 character lowercase hexadecimal object ID on stdout is
// rejected.
func ResolveBase(ctx context.Context, repositoryRoot, ref string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !validRepositoryRoot(repositoryRoot) {
		return "", errors.New(ErrBaseRootInvalid)
	}
	if !validBaseRef(ref) {
		return "", errors.New(ErrBaseRefInvalid)
	}
	if !validObjectID(ref) {
		return "", errors.New(ErrBaseRefNotImmutable)
	}

	ctx, cancel := context.WithTimeout(ctx, baseResolveTimeout)
	defer cancel()

	stdout := &limitedWriter{limit: maxBaseOutput}
	runErr := runDirectCommand(ctx,
		[]string{
			"git",
			"-C", repositoryRoot,
			"rev-parse", "--verify", "--end-of-options", ref + "^{commit}",
		},
		baseGitEnvironment(),
		stdout)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if runErr != nil {
		return "", errors.New(ErrBaseGitFailed)
	}
	if stdout.overflow {
		return "", errors.New(ErrBaseOutputInvalid)
	}
	sha := strings.TrimSuffix(string(stdout.buf), "\n")
	if !validObjectID(sha) {
		return "", errors.New(ErrBaseOutputInvalid)
	}
	return sha, nil
}

// validRepositoryRoot requires an absolute, lexically clean path that exists
// and is a directory. Symlink resolution is intentionally left to git itself
// via -C; no content of the directory is inspected here.
func validRepositoryRoot(root string) bool {
	if root == "" || !filepath.IsAbs(root) || root != filepath.Clean(root) {
		return false
	}
	info, err := os.Stat(root)
	return err == nil && info.IsDir()
}

// validBaseRef rejects anything that could be interpreted as a git option or
// that smuggles control characters. The ref is additionally passed after
// --end-of-options, so this is defense in depth, not the only line. The
// immutable full-object-ID shape is enforced on top by validObjectID in
// ResolveBase, which rejects every floating ref.
func validBaseRef(ref string) bool {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return false
	}
	if ref != strings.TrimSpace(ref) {
		return false
	}
	return !strings.ContainsAny(ref, "\x00\r\n")
}

// validObjectID reports whether id is exactly a 40–64 character lowercase
// hexadecimal object ID.
func validObjectID(id string) bool {
	if len(id) < 40 || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// baseGitEnvironment builds a restricted environment for git invocations:
// stable locale, no system or global config, no terminal prompts, hooks
// disabled. Only PATH, HOME and TMPDIR pass through from the current
// environment, so no credentials or harness-specific variables leak into git.
func baseGitEnvironment() []string {
	environment := []string{
		"LC_ALL=C",
		"LANG=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=/dev/null",
	}
	for _, name := range []string{"PATH", "HOME", "TMPDIR"} {
		if value := os.Getenv(name); value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

// limitedWriter keeps at most limit bytes and records whether any input beyond
// the limit was seen. It never fails a write, so the subprocess drains cleanly
// and the caller decides how to classify the overflow.
type limitedWriter struct {
	buf      []byte
	limit    int
	overflow bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if remaining := w.limit - len(w.buf); remaining > 0 {
		if len(p) <= remaining {
			w.buf = append(w.buf, p...)
		} else {
			w.buf = append(w.buf, p[:remaining]...)
			w.overflow = true
		}
	} else if len(p) > 0 {
		w.overflow = true
	}
	return len(p), nil
}
