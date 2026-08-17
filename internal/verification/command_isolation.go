package verification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const verifierWorktreeMutatedReason = "verifier-worktree-mutated"

type isolatedCommandResult struct {
	Command      CommandResult
	BeforeDigest string
	AfterDigest  string
	Mutated      bool
}

// runCommandIsolated executes one acceptance command against a disposable,
// standalone Git clone containing the exact observed candidate bytes.  The
// managed candidate worktree is only read while the clone is constructed and
// re-observed; command writes can therefore never become candidate bytes.
func runCommandIsolated(ctx context.Context, runner Runner, source, baseSHA string, expected Observation, spec CommandSpec) (isolatedCommandResult, error) {
	canonicalSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return isolatedCommandResult{}, errors.New("cannot resolve command source")
	}
	for _, argument := range spec.Argv {
		if strings.Contains(argument, canonicalSource) || (source != canonicalSource && strings.Contains(argument, source)) {
			return isolatedCommandResult{}, errors.New("command argv references the managed candidate directly")
		}
	}
	for _, item := range runner.Environment {
		if strings.Contains(item, canonicalSource) || (source != canonicalSource && strings.Contains(item, source)) {
			return isolatedCommandResult{}, errors.New("command environment references the managed candidate directly")
		}
	}
	observed, err := ObserveContext(ctx, source, baseSHA, expected.DiffBytes+1<<20)
	if err != nil {
		return isolatedCommandResult{}, errors.New("cannot observe command source")
	}
	if !sameObservationIdentity(observed, expected) {
		return isolatedCommandResult{}, errors.New("command source no longer matches the frozen candidate")
	}

	root, err := os.MkdirTemp("", "marshal-verifier-command-")
	if err != nil {
		return isolatedCommandResult{}, errors.New("cannot create command isolation root")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return isolatedCommandResult{}, errors.New("cannot protect command isolation root")
	}
	defer os.RemoveAll(root)

	isolate := filepath.Join(root, "worktree")
	if err := cloneCandidate(ctx, source, isolate); err != nil {
		return isolatedCommandResult{}, err
	}
	before, err := snapshotCommandTree(isolate)
	if err != nil {
		return isolatedCommandResult{}, errors.New("cannot snapshot command isolate before execution")
	}

	environment, err := commandIsolationEnvironment(root, runner.Environment)
	if err != nil {
		return isolatedCommandResult{}, err
	}
	runner.Environment = environment
	command := runner.Run(ctx, isolate, spec)
	command.Record.Executable = stableIsolateExecutable(command.Record.Executable, isolate)
	after, snapshotErr := snapshotCommandTree(isolate)
	if snapshotErr != nil {
		return isolatedCommandResult{Command: command, BeforeDigest: before}, errors.New("cannot snapshot command isolate after execution")
	}

	// Cancellation still requires a final immutable-candidate check. Use a
	// short cleanup context so the caller's cancellation cannot skip it.
	auditContext, cancelAudit := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelAudit()
	sourceAfter, observeErr := ObserveContext(auditContext, source, baseSHA, expected.DiffBytes+1<<20)
	if observeErr != nil || !sameObservationIdentity(sourceAfter, expected) {
		return isolatedCommandResult{Command: command, BeforeDigest: before, AfterDigest: after}, errors.New("managed candidate changed while verifier command ran")
	}
	return isolatedCommandResult{Command: command, BeforeDigest: before, AfterDigest: after, Mutated: before != after}, nil
}

func sameObservationIdentity(left, right Observation) bool {
	return left.SnapshotDigest == right.SnapshotDigest && left.DiffDigest == right.DiffDigest
}

func cloneCandidate(ctx context.Context, source, destination string) error {
	canonicalSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return errors.New("cannot resolve command source")
	}
	head, err := commandOutput(ctx, source, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return errors.New("cannot resolve command source head")
	}
	clone := exec.CommandContext(ctx, "git", "-c", "protocol.file.allow=always", "clone", "--no-local", "--no-checkout", "--no-tags", canonicalSource, destination)
	clone.Env = isolatedGitEnvironment()
	if output, cloneErr := clone.CombinedOutput(); cloneErr != nil {
		_ = output // Never surface provider/local-path output through stable evidence.
		return errors.New("cannot create standalone command clone")
	}
	if err := commandRun(ctx, destination, "checkout", "--detach", "--quiet", strings.TrimSpace(string(head))); err != nil {
		return errors.New("cannot checkout command source head")
	}
	if err := commandRun(ctx, destination, "remote", "remove", "origin"); err != nil {
		return errors.New("cannot detach command clone remote")
	}
	if err := clearCommandWorktree(destination); err != nil {
		return err
	}
	if err := copyCandidateFiles(ctx, canonicalSource, destination); err != nil {
		return err
	}
	return nil
}

func clearCommandWorktree(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return errors.New("cannot enumerate command clone")
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return errors.New("cannot reset command clone")
		}
	}
	return nil
}

func copyCandidateFiles(ctx context.Context, source, destination string) error {
	stages, err := commandOutput(ctx, source, "ls-files", "--stage", "-z")
	if err != nil {
		return errors.New("cannot inspect candidate index")
	}
	for _, record := range splitNUL(stages) {
		if strings.HasPrefix(record, "160000 ") {
			return errors.New("command isolation refuses submodule entries")
		}
	}
	listed, err := commandOutput(ctx, source, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return errors.New("cannot enumerate candidate files")
	}
	paths := splitNUL(listed)
	sort.Strings(paths)
	var symlinks []string
	for _, relative := range paths {
		if err := validateRelativePath(relative); err != nil {
			return errors.New("command isolation refuses an unsafe candidate path")
		}
		sourcePath := filepath.Join(source, filepath.FromSlash(relative))
		info, err := os.Lstat(sourcePath)
		if errors.Is(err, os.ErrNotExist) {
			continue // A tracked deletion is represented by absence in the clone.
		}
		if err != nil {
			return errors.New("cannot inspect a candidate file")
		}
		if err := validateCandidatePathComponents(source, relative); err != nil {
			return err
		}
		destinationPath := filepath.Join(destination, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(destinationPath), 0o700); err != nil {
			return errors.New("cannot create command clone directory")
		}
		switch {
		case info.Mode().IsRegular():
			if err := copyRegularCandidateFile(sourcePath, destinationPath, info.Mode()); err != nil {
				return err
			}
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(sourcePath)
			if err != nil || filepath.IsAbs(target) {
				return errors.New("command isolation refuses an unsafe symlink")
			}
			resolved, err := filepath.EvalSymlinks(sourcePath)
			if err != nil || !within(source, resolved) {
				return errors.New("command isolation refuses an escaping or unresolved symlink")
			}
			if err := os.Symlink(target, destinationPath); err != nil {
				return errors.New("cannot reproduce a candidate symlink")
			}
			symlinks = append(symlinks, destinationPath)
		default:
			return errors.New("command isolation refuses a non-regular candidate entry")
		}
	}
	for _, path := range symlinks {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || !within(destination, resolved) {
			return errors.New("command isolation cannot reproduce a symlink safely")
		}
	}
	return nil
}

func validateCandidatePathComponents(root, relative string) error {
	current := root
	parts := strings.Split(filepath.FromSlash(relative), string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("command isolation refuses a symlinked candidate parent")
		}
	}
	return nil
}

func copyRegularCandidateFile(source, destination string, mode os.FileMode) error {
	input, err := os.OpenFile(source, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("cannot safely open a candidate file")
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600|mode.Perm()&0o111)
	if err != nil {
		return errors.New("cannot create a command clone file")
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		return errors.New("cannot copy a candidate file")
	}
	return nil
}

func snapshotCommandTree(root string) (string, error) {
	var records []string
	var totalBytes int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == ".git" || strings.HasPrefix(relative, ".git"+string(filepath.Separator)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if relative == "." {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		switch {
		case info.IsDir():
			records = append(records, fmt.Sprintf("d\x00%s\x00%o", name, info.Mode().Perm()))
		case info.Mode().IsRegular():
			if info.Size() > 256<<20 || totalBytes > (1<<30)-info.Size() {
				return errors.New("command isolate snapshot exceeds the safe byte limit")
			}
			totalBytes += info.Size()
			input, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			if err != nil {
				return err
			}
			digest := sha256.New()
			_, copyErr := io.Copy(digest, input)
			closeErr := input.Close()
			if copyErr != nil || closeErr != nil {
				return errors.New("cannot digest a command isolate file")
			}
			records = append(records, fmt.Sprintf("f\x00%s\x00%o\x00%d\x00%s", name, info.Mode().Perm(), info.Size(), hex.EncodeToString(digest.Sum(nil))))
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			records = append(records, "l\x00"+name+"\x00"+target)
		default:
			return errors.New("command created an unsupported filesystem entry")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(records)
	digest := sha256.Sum256([]byte(strings.Join(records, "\x00")))
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func commandIsolationEnvironment(root string, inherited []string) ([]string, error) {
	result := append([]string(nil), inherited...)
	for name, relative := range map[string]string{
		"HOME":                "home",
		"TMPDIR":              "tmp",
		"XDG_CACHE_HOME":      "cache",
		"PYTHONPYCACHEPREFIX": "python-cache",
		"GOCACHE":             "go-cache",
	} {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, errors.New("cannot create isolated command cache")
		}
		result = append(result, name+"="+path)
	}
	return result, nil
}

func stableIsolateExecutable(executable, isolate string) string {
	if executable == "" {
		return executable
	}
	relative, err := filepath.Rel(isolate, executable)
	if err == nil && relative != "." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && relative != ".." {
		return "worktree://" + filepath.ToSlash(relative)
	}
	return executable
}

func isolatedGitEnvironment() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"LC_ALL=C",
		"LANG=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=/dev/null",
	}
}

func commandOutput(ctx context.Context, directory string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, args...)...)
	command.Env = isolatedGitEnvironment()
	return command.Output()
}

func commandRun(ctx context.Context, directory string, args ...string) error {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, args...)...)
	command.Env = isolatedGitEnvironment()
	return command.Run()
}

func isolationErrorCommand(spec CommandSpec) CommandResult {
	now := time.Now().UTC()
	return CommandResult{
		Status:    "error",
		StartedAt: now,
		EndedAt:   now,
		Record: CommandRecord{
			Argv:           append([]string(nil), spec.Argv...),
			CWD:            spec.CWD,
			Executable:     "unresolved",
			StartedAt:      now,
			CompletedAt:    now,
			BaselineStatus: "not-run",
		},
	}
}
