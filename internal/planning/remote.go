package planning

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Remote resolution errors are fixed, categorized strings. Resolved remote
// URLs may carry credentials, so errors never carry the repository root, the
// remote name, git stdout or stderr; callers can compare and log them
// deterministically without leaking data. Context cancellation and deadline
// errors are the only exceptions: those are returned as the context's own
// error.
const (
	ErrRemoteRootInvalid   = "resolve remote: repository root is invalid"
	ErrRemoteNameInvalid   = "resolve remote: remote name is invalid"
	ErrRemoteGitFailed     = "resolve remote: git remote get-url failed"
	ErrRemoteOutputInvalid = "resolve remote: git output is not a valid remote URL"
)

// remoteResolveTimeout bounds a single remote get-url invocation. It is a
// variable so tests can exercise the timeout path without waiting for real
// git.
var remoteResolveTimeout = 10 * time.Second

// maxRemoteOutput bounds how much stdout a remote get-url invocation may
// produce. Valid output is a single URL line plus at most one trailing
// newline; anything larger is discarded and treated as invalid output.
const maxRemoteOutput = 4096

// ResolveRemote resolves the configured URL of remote inside the repository
// at repositoryRoot. It invokes git directly via argv (never through a
// shell), with a restricted environment and a bounded timeout and output
// buffer. The command runs in its own process group and the entire group is
// terminated when the timeout expires, so grandchildren holding the output
// pipe cannot keep the invocation alive. It is fail-closed: anything other
// than a single non-empty line, with at most one trailing newline, is
// rejected.
func ResolveRemote(ctx context.Context, repositoryRoot, remote string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !validRepositoryRoot(repositoryRoot) {
		return "", errors.New(ErrRemoteRootInvalid)
	}
	if !validRemoteName(remote) {
		return "", errors.New(ErrRemoteNameInvalid)
	}

	ctx, cancel := context.WithTimeout(ctx, remoteResolveTimeout)
	defer cancel()

	stdout := &limitedWriter{limit: maxRemoteOutput}
	runErr := runDirectCommand(ctx,
		[]string{
			"git",
			"-C", repositoryRoot,
			"remote", "get-url", remote,
		},
		baseGitEnvironment(),
		stdout)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if runErr != nil {
		return "", errors.New(ErrRemoteGitFailed)
	}
	if stdout.overflow {
		return "", errors.New(ErrRemoteOutputInvalid)
	}
	line := strings.TrimSuffix(string(stdout.buf), "\n")
	if !validRemoteURL(line) {
		return "", errors.New(ErrRemoteOutputInvalid)
	}
	return line, nil
}

// validRemoteName rejects anything that could be interpreted as a git option
// (such as --push) or that smuggles control characters. Names are passed to
// git as a single argv element after the subcommand, so this is defense in
// depth against option injection.
func validRemoteName(remote string) bool {
	if remote == "" || strings.HasPrefix(remote, "-") {
		return false
	}
	if remote != strings.TrimSpace(remote) {
		return false
	}
	return !strings.ContainsAny(remote, "\x00\r\n")
}

// validRemoteURL requires a single non-empty line without surrounding
// whitespace, embedded newlines or control characters. A second trailing
// newline manifests as an embedded newline after the suffix is trimmed and is
// therefore rejected here.
func validRemoteURL(url string) bool {
	if url == "" || url != strings.TrimSpace(url) {
		return false
	}
	return !strings.ContainsAny(url, "\x00\r\n")
}
