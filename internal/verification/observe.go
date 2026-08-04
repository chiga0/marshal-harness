package verification

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

func Observe(worktree, baseSHA string, captureLimit int64) (Observation, error) {
	return ObserveContext(context.Background(), worktree, baseSHA, captureLimit)
}

func ObserveContext(ctx context.Context, worktree, baseSHA string, captureLimit int64) (Observation, error) {
	if captureLimit <= 0 {
		captureLimit = 64 << 20
	}
	nameStatus, err := gitBytesContext(ctx, worktree, "diff", "--name-status", "-z", "--find-renames", baseSHA, "--")
	if err != nil {
		return Observation{}, err
	}
	changes, err := parseNameStatus(nameStatus)
	if err != nil {
		return Observation{}, err
	}
	untrackedRaw, err := gitBytesContext(ctx, worktree, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return Observation{}, err
	}
	for _, path := range splitNUL(untrackedRaw) {
		changes = append(changes, Change{Status: "U", Path: path})
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Path == changes[j].Path {
			return changes[i].OldPath < changes[j].OldPath
		}
		return changes[i].Path < changes[j].Path
	})
	for index := range changes {
		if err := enrichChange(ctx, worktree, &changes[index]); err != nil {
			return Observation{}, err
		}
	}
	patchWriter := newDigestCapture(captureLimit)
	if err := gitToWriterContext(ctx, worktree, patchWriter, "diff", "--no-ext-diff", "--no-textconv", "--binary", "--full-index", "--find-renames", baseSHA, "--"); err != nil {
		return Observation{}, err
	}
	for _, change := range changes {
		if change.Status != "U" {
			continue
		}
		if err := gitDiffUntracked(ctx, worktree, change.Path, patchWriter); err != nil {
			return Observation{}, err
		}
	}
	snapshotJSON, err := json.Marshal(changes)
	if err != nil {
		return Observation{}, err
	}
	canonicalSnapshot, err := canonical.JSON(snapshotJSON)
	if err != nil {
		return Observation{}, err
	}
	paths := make([]string, 0, len(changes)*2)
	seen := map[string]bool{}
	hasUntracked := false
	for _, change := range changes {
		for _, path := range []string{change.OldPath, change.Path} {
			if path != "" && !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
		}
		hasUntracked = hasUntracked || change.Status == "U"
	}
	sort.Strings(paths)
	return Observation{
		SnapshotDigest: canonical.DigestBytes(canonicalSnapshot), DiffDigest: patchWriter.digest(),
		ChangedFiles: paths, ChangedFileCount: len(paths), DiffBytes: patchWriter.total,
		HasUntrackedFiles: hasUntracked, Changes: changes, Patch: patchWriter.bytes(), DiffTruncated: patchWriter.truncated(),
	}, nil
}

func parseNameStatus(data []byte) ([]Change, error) {
	fields := splitNUL(data)
	var changes []Change
	for index := 0; index < len(fields); {
		status := fields[index]
		index++
		if status == "" || index >= len(fields) {
			return nil, errors.New("malformed git name-status output")
		}
		if status[0] == 'R' || status[0] == 'C' {
			if index+1 >= len(fields) {
				return nil, errors.New("malformed rename record")
			}
			changes = append(changes, Change{Status: status, OldPath: fields[index], Path: fields[index+1]})
			index += 2
		} else {
			changes = append(changes, Change{Status: status, Path: fields[index]})
			index++
		}
	}
	return changes, nil
}

func enrichChange(ctx context.Context, root string, change *Change) error {
	if err := validateRelativePath(change.Path); err != nil {
		return err
	}
	if change.OldPath != "" {
		if err := validateRelativePath(change.OldPath); err != nil {
			return err
		}
	}
	path := filepath.Join(root, filepath.FromSlash(change.Path))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	change.Mode = uint32(info.Mode())
	change.ByteSize = info.Size()
	if info.Mode()&os.ModeSymlink != 0 {
		change.Symlink = true
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		change.Digest = canonical.DigestBytes([]byte(target))
		resolvedTarget := target
		if !filepath.IsAbs(resolvedTarget) {
			resolvedTarget = filepath.Join(filepath.Dir(path), resolvedTarget)
		}
		absoluteTarget, absoluteErr := filepath.Abs(resolvedTarget)
		canonicalRoot, rootErr := filepath.EvalSymlinks(root)
		canonicalTarget, targetErr := filepath.EvalSymlinks(absoluteTarget)
		change.SymlinkEscapes = absoluteErr != nil || rootErr != nil || targetErr != nil || !within(canonicalRoot, canonicalTarget)
		return nil
	}
	if info.Mode().IsRegular() {
		digest, err := digestFile(path)
		if err != nil {
			return err
		}
		change.Digest = digest
	}
	mode, _ := gitBytesContext(ctx, root, "ls-files", "-s", "--", change.Path)
	change.Submodule = bytes.HasPrefix(mode, []byte("160000 "))
	return nil
}

func validateRelativePath(path string) error {
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, "\\") || strings.ContainsRune(path, 0) {
		return fmt.Errorf("unsafe repository path %q", path)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if clean != path || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("unsafe repository path %q", path)
	}
	return nil
}

func gitDiffUntracked(ctx context.Context, root, path string, writer io.Writer) error {
	command := exec.CommandContext(ctx, "git", "-C", root, "diff", "--no-index", "--no-ext-diff", "--no-textconv", "--binary", "--full-index", "--", "/dev/null", path)
	environment, err := verifierGitEnvironment(ctx, root)
	if err != nil {
		return err
	}
	command.Env = environment
	command.Stdout = writer
	var stderr bytes.Buffer
	command.Stderr = &stderr
	err = command.Run()
	var exitErr *exec.ExitError
	if err == nil || (errors.As(err, &exitErr) && exitErr.ExitCode() == 1) {
		return nil
	}
	return fmt.Errorf("diff untracked %q: %w: %s", path, err, stderr.String())
}

func gitBytesContext(ctx context.Context, root string, args ...string) ([]byte, error) {
	var output bytes.Buffer
	if err := gitToWriterContext(ctx, root, &output, args...); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func gitToWriterContext(ctx context.Context, root string, writer io.Writer, args ...string) error {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	environment, err := verifierGitEnvironment(ctx, root)
	if err != nil {
		return err
	}
	command.Env = environment
	command.Stdout = writer
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

var filterConfigKey = regexp.MustCompile(`^filter\.(.+)\.(?:clean|smudge|process|required)$`)

// verifierGitEnvironment neutralizes repository-local filter drivers. Git
// diff otherwise runs a Worker-controlled clean/process command while merely
// observing the accepted worktree. The raw file digests remain authoritative.
func verifierGitEnvironment(ctx context.Context, root string) ([]string, error) {
	base := verifierEnvironment(nil)
	command := exec.CommandContext(ctx, "git", "-C", root, "config", "--local", "--null", "--name-only", "--get-regexp", `^filter\..*\.(clean|smudge|process|required)$`)
	command.Env = base
	output, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if !(errors.As(err, &exitErr) && exitErr.ExitCode() == 1) {
			return nil, fmt.Errorf("inspect repository filter config: %w", err)
		}
	}
	drivers := map[string]bool{}
	for _, line := range bytes.Split(output, []byte{0}) {
		matches := filterConfigKey.FindStringSubmatch(string(line))
		if len(matches) == 2 {
			drivers[matches[1]] = true
		}
	}
	names := make([]string, 0, len(drivers))
	for name := range drivers {
		names = append(names, name)
	}
	sort.Strings(names)
	count := 1 + len(names)*4
	base = replaceEnvironment(base, "GIT_CONFIG_COUNT", strconv.Itoa(count))
	index := 1
	for _, name := range names {
		for _, item := range [][2]string{{"clean", "cat"}, {"smudge", "cat"}, {"process", ""}, {"required", "false"}} {
			base = append(base, fmt.Sprintf("GIT_CONFIG_KEY_%d=filter.%s.%s", index, name, item[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", index, item[1]))
			index++
		}
	}
	return base, nil
}

func replaceEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	for index, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			environment[index] = prefix + value
			return environment
		}
	}
	return append(environment, prefix+value)
}

func digestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

func splitNUL(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	parts := bytes.Split(data, []byte{0})
	if len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	result := make([]string, len(parts))
	for index := range parts {
		result[index] = string(parts[index])
	}
	return result
}

type digestCapture struct {
	limit int64
	total int64
	hash  hash.Hash
	data  bytes.Buffer
}

func newDigestCapture(limit int64) *digestCapture {
	return &digestCapture{limit: limit, hash: sha256.New()}
}

func (w *digestCapture) Write(data []byte) (int, error) {
	_, _ = w.hash.Write(data)
	w.total += int64(len(data))
	remaining := w.limit - int64(w.data.Len())
	if remaining > 0 {
		if int64(len(data)) > remaining {
			_, _ = w.data.Write(data[:remaining])
		} else {
			_, _ = w.data.Write(data)
		}
	}
	return len(data), nil
}

func (w *digestCapture) digest() string {
	return "sha256:" + hex.EncodeToString(w.hash.Sum(nil))
}
func (w *digestCapture) bytes() []byte   { return bytes.Clone(w.data.Bytes()) }
func (w *digestCapture) truncated() bool { return w.total > w.limit }
