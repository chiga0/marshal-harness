package gitworktree

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ExportTeamDelivery reconstructs the original integration base from the two
// immutable accepted patches, then applies the final incremental patch. It
// exports only caller-frozen regular files, never a mutable Worker worktree.
// The result is data, not a current-owner or publication authorization.
func (r Repository) ExportTeamDelivery(ctx context.Context, stateRoot, base, binding, expectedTree, expectedCommit string, upstreams [][]byte, finalPatch []byte, paths []string) (map[string][]byte, error) {
	fail := errors.New("team delivery: invalid or changed accepted content")
	if ctx == nil || len(finalPatch) == 0 || len(finalPatch) > 4<<20 || len(paths) < 1 || len(paths) > 16 {
		return nil, fail
	}
	seen := map[string]bool{}
	for _, path := range paths {
		if path == "" || path == "." || path == ".." || strings.ContainsAny(path, "/\\\x00\n\r\t") || seen[path] {
			return nil, fail
		}
		seen[path] = true
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	derived, err := r.CombineAcceptedPatches(ctx, stateRoot, base, binding, upstreams)
	if err != nil || derived.TreeSHA != expectedTree || derived.CommitSHA != expectedCommit {
		return nil, fail
	}
	// CombineAcceptedPatches has already validated the exact repository and
	// stateRoot/locks directory. Re-observe before opening our own index.
	actual, err := OpenContext(ctx, r.Root)
	locks := filepath.Join(stateRoot, "locks")
	canonicalLocks, canonicalErr := canonical(locks)
	if err != nil || actual != r || canonicalErr != nil || canonicalLocks != locks {
		return nil, fail
	}
	directory, err := os.MkdirTemp(locks, "team-delivery-index-")
	if err != nil {
		return nil, err
	}
	index := filepath.Join(directory, "index")
	defer func() { _ = os.Remove(index + ".lock"); _ = os.Remove(index); _ = os.Remove(directory) }()
	env := append(gitEnvironment(), "GIT_INDEX_FILE="+index, "GIT_NO_REPLACE_OBJECTS=1")
	git := func(input []byte, limit int, args ...string) ([]byte, error) {
		prefix := []string{"-C", r.Root, "-c", "core.splitIndex=false", "-c", "core.fsmonitor=false"}
		command := exec.CommandContext(ctx, "git", append(prefix, args...)...)
		command.Env, command.Stdin, command.WaitDelay = env, bytes.NewReader(input), time.Second
		stdout, stderr := &deliveryOutput{limit: limit}, &deliveryOutput{limit: 64 << 10}
		command.Stdout, command.Stderr = stdout, stderr
		if err := command.Run(); err != nil || stdout.overflow || stderr.overflow {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fail
		}
		return bytes.Clone(stdout.buffer.Bytes()), nil
	}
	if _, err := git(nil, 64<<10, "read-tree", expectedCommit); err != nil {
		return nil, err
	}
	if _, err := git(finalPatch, 64<<10, "apply", "--cached", "--binary", "--whitespace=nowarn", "-"); err != nil {
		return nil, err
	}
	treeRaw, err := git(nil, 64<<10, "write-tree")
	tree := strings.TrimSpace(string(treeRaw))
	if err != nil || !integrationObjectID.MatchString(tree) {
		return nil, fail
	}
	result := make(map[string][]byte, len(paths))
	total := 0
	for _, path := range paths {
		entry, err := git(nil, 64<<10, "ls-tree", "-z", tree, "--", path)
		if err != nil || bytes.Count(entry, []byte{0}) != 1 || len(entry) == 0 || entry[len(entry)-1] != 0 {
			return nil, fail
		}
		parts := strings.SplitN(string(entry[:len(entry)-1]), "\t", 2)
		if len(parts) != 2 || parts[1] != path {
			return nil, fail
		}
		identity := strings.Fields(parts[0])
		if len(identity) != 3 || identity[0] != "100644" || identity[1] != "blob" || !integrationObjectID.MatchString(identity[2]) {
			return nil, fail
		}
		data, err := git(nil, (8<<20)-total, "cat-file", "blob", identity[2])
		if err != nil || len(data) == 0 {
			return nil, fail
		}
		total += len(data)
		if total > 8<<20 {
			return nil, fail
		}
		result[path] = data
	}
	return result, nil
}

type deliveryOutput struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (out *deliveryOutput) Write(data []byte) (int, error) {
	n := len(data)
	remaining := out.limit - out.buffer.Len()
	if n > remaining {
		out.overflow = true
		data = data[:remaining]
	}
	_, _ = out.buffer.Write(data)
	return n, nil
}
