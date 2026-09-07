package gitworktree

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// IntegrationBase is a Git data product, not a creation/Start authorization.
// The controller must bind the original accepted inputs and current owner in
// its durable creation fact before materializing any Run from this commit.
type IntegrationBase struct {
	TreeSHA   string
	CommitSHA string
}

var integrationObjectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var integrationBindingDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// CombineAcceptedPatches applies the two captured inputs in caller-supplied
// frozen node order. Only immutable objects and an isolated temporary index
// are written; no ref, checked-out file, shared index, hook or filter is used.
// It deliberately has no retry, three-way merge or conflict resolution path.
func (r Repository) CombineAcceptedPatches(ctx context.Context, stateRoot, baseSHA, bindingDigest string, patches [][]byte) (IntegrationBase, error) {
	fail := errors.New("integration base: invalid input or conflicting patch")
	if ctx == nil || !integrationObjectID.MatchString(baseSHA) || !integrationBindingDigest.MatchString(bindingDigest) || len(patches) != 2 {
		return IntegrationBase{}, fail
	}
	for _, patch := range patches {
		if len(patch) == 0 || len(patch) > 4<<20 {
			return IntegrationBase{}, fail
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return IntegrationBase{}, err
	}
	actual, err := OpenContext(ctx, r.Root)
	if err != nil || actual != r || !filepath.IsAbs(stateRoot) {
		return IntegrationBase{}, fail
	}
	locks := filepath.Join(stateRoot, "locks")
	canonicalLocks, err := canonical(locks)
	info, statErr := os.Lstat(locks)
	if err != nil || statErr != nil || canonicalLocks != locks || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return IntegrationBase{}, fail
	}
	directory, err := os.MkdirTemp(locks, "team-integration-index-")
	if err != nil {
		return IntegrationBase{}, err
	}
	index := filepath.Join(directory, "index")
	defer func() {
		// Remove only our exact temporary artifacts, never recursive content.
		_ = os.Remove(index + ".lock")
		_ = os.Remove(index)
		_ = os.Remove(directory)
	}()
	env := append(gitEnvironment(), "GIT_INDEX_FILE="+index, "GIT_NO_REPLACE_OBJECTS=1",
		"GIT_AUTHOR_NAME=Marshal Integration", "GIT_AUTHOR_EMAIL=integration@marshal.invalid",
		"GIT_COMMITTER_NAME=Marshal Integration", "GIT_COMMITTER_EMAIL=integration@marshal.invalid",
		"GIT_AUTHOR_DATE=946684800 +0000", "GIT_COMMITTER_DATE=946684800 +0000")
	git := func(input []byte, args ...string) (string, error) {
		prefix := []string{"-C", r.Root, "-c", "core.splitIndex=false", "-c", "core.fsmonitor=false", "-c", "commit.gpgSign=false", "-c", "i18n.commitEncoding=UTF-8"}
		command := exec.CommandContext(ctx, "git", append(prefix, args...)...)
		command.Env, command.Stdin = env, bytes.NewReader(input)
		command.WaitDelay = time.Second
		stdout, stderr := &integrationOutput{}, &integrationOutput{}
		command.Stdout, command.Stderr = stdout, stderr
		if err := command.Run(); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			// Git diagnostics can contain repository data. Do not publish them.
			return "", fail
		}
		if stdout.overflow || stderr.overflow {
			return "", fail
		}
		return strings.TrimSpace(stdout.String()), nil
	}
	if kind, err := git(nil, "cat-file", "-t", baseSHA); err != nil || kind != "commit" {
		return IntegrationBase{}, fail
	}
	if _, err := git(nil, "read-tree", baseSHA); err != nil {
		return IntegrationBase{}, err
	}
	for _, patch := range patches {
		if _, err := git(patch, "apply", "--cached", "--binary", "--whitespace=nowarn", "-"); err != nil {
			return IntegrationBase{}, err
		}
	}
	tree, err := git(nil, "write-tree")
	if err != nil || !integrationObjectID.MatchString(tree) {
		return IntegrationBase{}, fail
	}
	message := []byte("Marshal bounded team integration\n\nMarshal-Integration-Inputs: " + bindingDigest + "\n")
	commit, err := git(message, "commit-tree", tree, "-p", baseSHA)
	if err != nil || !integrationObjectID.MatchString(commit) {
		return IntegrationBase{}, fail
	}
	return IntegrationBase{TreeSHA: tree, CommitSHA: commit}, nil
}

// Keep draining pipes to avoid a verbose child blocking, while bounding both
// memory and the accepted protocol output. Overflow makes the command fail.
type integrationOutput struct {
	buffer   bytes.Buffer
	overflow bool
}

func (out *integrationOutput) Len() int       { return out.buffer.Len() }
func (out *integrationOutput) String() string { return out.buffer.String() }

func (out *integrationOutput) Write(data []byte) (int, error) {
	n := len(data)
	remaining := (64 << 10) - out.Len()
	if n > remaining {
		out.overflow = true
		data = data[:remaining]
	}
	_, _ = out.buffer.Write(data)
	return n, nil
}
