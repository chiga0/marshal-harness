package verification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/chiga0/marshal-harness/internal/verificationbuiltin"
)

const verifierWorktreeMutatedReason = "verifier-worktree-mutated"

const (
	// A verifier command may leave a large Go test binary, race profile, or
	// compiler cache in the disposable clone.  Five seconds is routinely too
	// short on macOS under normal host contention.  A 60-second bound still
	// produced false isolation errors for race/vet clones whose removal took
	// 87–103 seconds while the host was compiling other work.  Keep cleanup
	// bounded and fail closed, but leave enough time for the largest observed
	// disposable clone to drain before the result is classified as
	// non-authoritative.
	commandIsolationCleanupTimeout  = 180 * time.Second
	commandIsolationAuditTimeout    = 10 * time.Second
	commandIsolationSnapshotTimeout = 30 * time.Second
	commandIsolationMaxEntries      = 200000
	commandIsolationMaxPathBytes    = 4096
	commandIsolationMaxTargetBytes  = 4096
	commandIsolationMaxFileBytes    = int64(256 << 20)
	commandIsolationMaxTotalBytes   = int64(1 << 30)
)

type isolatedCommandResult struct {
	Command               CommandResult
	BeforeDigest          string
	AfterDigest           string
	Mutated               bool
	Executed              bool
	BuiltinArtifactDigest string
	BuiltinDeliverableID  string
	BuiltinConsumed       bool
	BuiltinFailureReason  string
}

type commandProtectedSource struct {
	Path     string
	BaseSHA  string
	Expected Observation
}

// runCommandIsolated executes one acceptance command against a disposable,
// standalone Git clone containing the exact observed candidate bytes.  The
// managed candidate worktree is only read while the clone is constructed and
// re-observed; command writes can therefore never become candidate bytes.
func runCommandIsolated(ctx context.Context, runner Runner, source, baseSHA string, expected Observation, spec CommandSpec, additional ...commandProtectedSource) (result isolatedCommandResult, resultErr error) {
	return runCommandIsolatedPlanned(ctx, runner, source, baseSHA, expected, spec, nil, additional...)
}

func runCommandIsolatedPlanned(ctx context.Context, runner Runner, source, baseSHA string, expected Observation, spec CommandSpec, builtin *verificationbuiltin.Plan, additional ...commandProtectedSource) (result isolatedCommandResult, resultErr error) {
	return runCommandIsolatedWithPlanAndHooks(ctx, runner, source, baseSHA, expected, spec, builtin, commandIsolationHooks{}, additional...)
}

func runCommandIsolatedWithHooks(ctx context.Context, runner Runner, source, baseSHA string, expected Observation, spec CommandSpec, hooks commandIsolationHooks, additional ...commandProtectedSource) (result isolatedCommandResult, resultErr error) {
	return runCommandIsolatedWithPlanAndHooks(ctx, runner, source, baseSHA, expected, spec, nil, hooks, additional...)
}

func runCommandIsolatedWithPlanAndHooks(ctx context.Context, runner Runner, source, baseSHA string, expected Observation, spec CommandSpec, builtin *verificationbuiltin.Plan, hooks commandIsolationHooks, additional ...commandProtectedSource) (result isolatedCommandResult, resultErr error) {
	canonicalSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return isolatedCommandResult{}, errors.New("cannot resolve command source")
	}
	protected := append([]commandProtectedSource{{Path: source, BaseSHA: baseSHA, Expected: expected}}, additional...)
	protectedRoots := []string{source, canonicalSource}
	for _, item := range additional {
		canonical, resolveErr := filepath.EvalSymlinks(item.Path)
		if resolveErr != nil {
			return isolatedCommandResult{}, errors.New("cannot resolve an additional managed source")
		}
		protectedRoots = append(protectedRoots, item.Path, canonical)
		observed, observeErr := ObserveContext(ctx, item.Path, item.BaseSHA, item.Expected.DiffBytes+1<<20)
		if observeErr != nil || !sameObservationIdentity(observed, item.Expected) {
			return isolatedCommandResult{}, errors.New("additional managed source no longer matches its frozen observation")
		}
	}
	if err := rejectProtectedReferences(spec.Argv, runner.Environment, protectedRoots...); err != nil {
		return isolatedCommandResult{}, err
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
	// Cleanup precedes the final source audit on every return after creation.
	// The named return lets cleanup/audit failures overlay isolation status
	// without discarding an already executed command's real result and logs.
	defer func() {
		cleanupErr := removeAllBounded(root, commandIsolationCleanupTimeout)
		auditContext, cancelAudit := context.WithTimeout(context.Background(), commandIsolationAuditTimeout)
		defer cancelAudit()
		if cleanupErr != nil {
			if errors.Is(cleanupErr, context.DeadlineExceeded) {
				resultErr = errors.Join(resultErr, fmt.Errorf("cannot clean command isolation root within deadline: %w", cleanupErr))
			} else {
				resultErr = errors.Join(resultErr, fmt.Errorf("cannot clean command isolation root: %w", cleanupErr))
			}
		}
		for _, item := range protected {
			sourceAfter, observeErr := ObserveContext(auditContext, item.Path, item.BaseSHA, item.Expected.DiffBytes+1<<20)
			if observeErr != nil || !sameObservationIdentity(sourceAfter, item.Expected) {
				resultErr = errors.Join(resultErr, errors.New("managed candidate or baseline changed while verifier command ran"))
			}
		}
	}()

	isolate := filepath.Join(root, "worktree")
	if err := cloneCandidateWithHooks(ctx, source, isolate, hooks); err != nil {
		return isolatedCommandResult{}, err
	}
	isolateObservation, err := ObserveContext(ctx, isolate, baseSHA, expected.DiffBytes+1<<20)
	if err != nil || !sameObservationIdentity(isolateObservation, expected) {
		return isolatedCommandResult{}, errors.New("command isolate does not match the frozen candidate")
	}
	beforeContext, cancelBefore := context.WithTimeout(ctx, commandIsolationSnapshotTimeout)
	before, err := snapshotCommandTree(beforeContext, isolate)
	cancelBefore()
	if err != nil {
		return isolatedCommandResult{}, errors.New("cannot snapshot command isolate before execution")
	}

	var command CommandResult
	if builtin != nil {
		command, result.BuiltinArtifactDigest, result.BuiltinConsumed, result.BuiltinFailureReason = runTaskSpecBuiltin(ctx, isolate, spec, *builtin)
		result.BuiltinDeliverableID = builtin.DeliverableID
	} else {
		environment, environmentErr := commandIsolationEnvironment(root, runner.Environment)
		if environmentErr != nil {
			return isolatedCommandResult{}, environmentErr
		}
		runner.Environment = environment
		if err := rejectResolvedExecutable(isolate, runner, spec, protectedRoots...); err != nil {
			return isolatedCommandResult{}, err
		}
		command = runner.Run(ctx, isolate, spec)
		command.Record.Executable = stableIsolateExecutable(command.Record.Executable, isolate)
	}
	result.Command = command
	result.BeforeDigest = before
	result.Executed = true
	afterContext, cancelAfter := context.WithTimeout(context.Background(), commandIsolationSnapshotTimeout)
	after, snapshotErr := snapshotCommandTree(afterContext, isolate)
	cancelAfter()
	if snapshotErr != nil {
		return result, errors.New("cannot snapshot command isolate after execution")
	}
	result.AfterDigest = after
	result.Mutated = before != after
	return result, nil
}

func removeAllBounded(path string, timeout time.Duration) error {
	if timeout <= 0 {
		return context.DeadlineExceeded
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		lastErr = removeTreeBounded(path, deadline)
		if lastErr == nil {
			return nil
		}
		if errors.Is(lastErr, context.DeadlineExceeded) || time.Now().After(deadline) {
			return fmt.Errorf("%w: %v", context.DeadlineExceeded, lastErr)
		}
		// A command may still be closing a race profile or compiler output
		// after its process group has exited. Retry within the same bounded
		// window instead of converting one transient RemoveAll error into a
		// false verifier failure.
		retry := time.NewTimer(50 * time.Millisecond)
		<-retry.C
	}
}

// removeTreeBounded removes one disposable verifier tree while checking the
// deadline between bounded directory reads and individual removals. Unlike
// os.RemoveAll, a large or slow tree cannot spend an unbounded amount of time
// inside one recursive call before the caller gets a chance to fail closed.
func removeTreeBounded(path string, deadline time.Time) error {
	if time.Now().After(deadline) {
		return context.DeadlineExceeded
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return os.Remove(path)
	}
	if !info.IsDir() {
		// Toolchains and package managers commonly mark extracted regular
		// files read-only after writing them (for example Go's downloaded
		// toolchain LICENSE). Restore owner write permission before unlinking
		// within the verifier-owned disposable root. Symlinks are handled above
		// and never followed; failures remain fail-closed.
		if info.Mode().IsRegular() && info.Mode().Perm()&0o200 == 0 {
			if err := os.Chmod(path, info.Mode().Perm()|0o200); err != nil {
				return err
			}
		}
		return os.Remove(path)
	}
	// Toolchains and package managers commonly make extracted cache
	// directories read-only after writing them (for example Go's downloaded
	// toolchain).  The tree is disposable and lives below the verifier-owned
	// isolation root, so restore owner write/search permission before removing
	// children.  This does not touch the managed candidate or any user path;
	// failure remains fail-closed and is reported to the verifier.
	if info.Mode().Perm()&0o700 != 0o700 {
		if err := os.Chmod(path, info.Mode().Perm()|0o700); err != nil {
			return err
		}
	}

	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	for {
		if time.Now().After(deadline) {
			return context.DeadlineExceeded
		}
		names, readErr := directory.Readdirnames(128)
		for _, name := range names {
			if err := removeTreeBounded(filepath.Join(path, name), deadline); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func rejectResolvedExecutable(worktree string, runner Runner, spec CommandSpec, roots ...string) error {
	if len(spec.Argv) == 0 {
		return nil
	}
	cwd, err := secureDirectory(worktree, spec.CWD)
	if err != nil {
		return nil // Runner will preserve its existing typed command error.
	}
	executable, err := lookPath(spec.Argv[0], cwd, worktree, verifierEnvironment(runner.Environment))
	if err != nil {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return errors.New("cannot resolve command executable")
	}
	for _, root := range roots {
		canonical, canonicalErr := filepath.EvalSymlinks(root)
		if canonicalErr == nil && within(canonical, resolved) {
			return errors.New("command executable resolves inside the managed candidate")
		}
	}
	return nil
}

func rejectProtectedReferences(argv, environment []string, roots ...string) error {
	canonicalRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		absolute, err := filepath.Abs(root)
		if err != nil {
			return errors.New("cannot canonicalize managed candidate path")
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return errors.New("cannot resolve managed candidate path")
		}
		canonicalRoots = append(canonicalRoots, filepath.Clean(resolved))
	}
	check := func(value string) bool {
		for _, candidate := range pathCandidates(value) {
			resolved, err := resolveWithMissingTail(candidate)
			if err != nil {
				continue
			}
			for _, root := range canonicalRoots {
				if within(root, resolved) {
					return true
				}
			}
		}
		return false
	}
	for _, value := range argv {
		if check(value) {
			return errors.New("command argv references the managed candidate directly or through an alias")
		}
	}
	for _, value := range environment {
		if check(value) {
			return errors.New("command environment references the managed candidate directly or through an alias")
		}
	}
	return nil
}

func pathCandidates(value string) []string {
	var result []string
	for start := 0; start < len(value); start++ {
		if value[start] != filepath.Separator {
			continue
		}
		end := start + 1
		for end < len(value) && !strings.ContainsRune(" \t\r\n\"'`;|&<>(){}[],:=", rune(value[end])) {
			end++
		}
		if end-start <= commandIsolationMaxPathBytes {
			// Preserve dot and dot-dot components until after symlinks are
			// expanded. The kernel resolves an alias before a following "..";
			// cleaning here would change that traversal and could hide a path
			// that lands back inside a protected root.
			result = append(result, value[start:end])
		}
		start = end - 1
	}
	return result
}

// resolveWithMissingTail follows filesystem components in kernel traversal
// order, resolving a symlink before applying any later ".." component. Once
// an absent component is reached, the remaining (necessarily absent) tail is
// reduced lexically. Cleaning the full input before symlink expansion is
// unsafe: alias-to-candidate/subdir/../output must resolve to candidate/output,
// not to the lexical sibling of alias-to-candidate.
func resolveWithMissingTail(path string) (string, error) {
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = cwd + string(filepath.Separator) + path
	}
	volume := filepath.VolumeName(path)
	root := volume + string(filepath.Separator)
	remainder := strings.TrimPrefix(path, root)
	pending := strings.Split(remainder, string(filepath.Separator))
	current := root
	missingDepth := 0
	symlinks := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			current = filepath.Dir(current)
			if missingDepth > 0 {
				missingDepth--
			}
			continue
		}
		next := filepath.Join(current, part)
		if missingDepth > 0 {
			current = next
			missingDepth++
			continue
		}
		info, err := os.Lstat(next)
		if errors.Is(err, os.ErrNotExist) {
			missingDepth = 1
			current = next
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			current = next
			continue
		}
		symlinks++
		if symlinks > 255 {
			return "", errors.New("too many symlinks while resolving protected reference")
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			volume = filepath.VolumeName(target)
			current = volume + string(filepath.Separator)
			target = strings.TrimPrefix(target, current)
		}
		targetParts := strings.Split(target, string(filepath.Separator))
		pending = append(targetParts, pending...)
	}
	return filepath.Clean(current), nil
}

func sameObservationIdentity(left, right Observation) bool {
	return left.SnapshotDigest == right.SnapshotDigest && left.DiffDigest == right.DiffDigest
}

func cloneCandidateWithHooks(ctx context.Context, source, destination string, hooks commandIsolationHooks) error {
	canonicalSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return errors.New("cannot resolve command source")
	}
	head, err := commandOutput(ctx, source, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return errors.New("cannot resolve command source head")
	}
	// A full --no-local clone copies the entire repository history into every
	// verifier sandbox. Large/race commands then spend most of their bounded
	// cleanup budget deleting .git objects, even though the verifier only needs
	// the exact frozen HEAD and its trees. Initialize an empty repository and
	// fetch only the source HEAD; candidate files are still copied and
	// re-observed below, so shallow history does not weaken evidence binding.
	if err := initializeShallowCommandClone(ctx, canonicalSource, destination, strings.TrimSpace(string(head))); err != nil {
		return err
	}
	if err := commandRun(ctx, destination, "checkout", "--detach", "--quiet", "FETCH_HEAD"); err != nil {
		return errors.New("cannot checkout command source head")
	}
	checkedOut, err := commandOutput(ctx, destination, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || strings.TrimSpace(string(checkedOut)) != strings.TrimSpace(string(head)) {
		return errors.New("command clone head does not match the frozen source")
	}
	if err := clearCommandWorktree(destination); err != nil {
		return err
	}
	if err := copyCandidateFilesWithHooks(ctx, canonicalSource, destination, hooks); err != nil {
		return err
	}
	return nil
}

func initializeShallowCommandClone(ctx context.Context, source, destination, head string) error {
	if head == "" {
		return errors.New("frozen command source head is empty")
	}
	init := exec.CommandContext(ctx, "git", "init", "--quiet", destination)
	init.Env = isolatedGitEnvironment()
	if output, err := init.CombinedOutput(); err != nil {
		_ = output
		return errors.New("cannot initialize standalone command clone")
	}
	fetch := exec.CommandContext(ctx, "git", "-c", "protocol.file.allow=always", "-C", destination, "fetch", "--quiet", "--no-tags", "--depth=1", source, "HEAD")
	fetch.Env = isolatedGitEnvironment()
	if output, err := fetch.CombinedOutput(); err != nil {
		_ = output
		return errors.New("cannot fetch frozen command source head")
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

type commandIsolationHooks struct {
	afterLstat func(string)
	afterCopy  func(string)
}

func copyCandidateFilesWithHooks(ctx context.Context, source, destination string, hooks commandIsolationHooks) error {
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
	if len(paths) > commandIsolationMaxEntries {
		return errors.New("command isolation candidate exceeds the safe entry limit")
	}
	sort.Strings(paths)
	var symlinks []string
	var totalBytes int64
	for _, relative := range paths {
		if err := ctx.Err(); err != nil {
			return errors.New("command isolation candidate copy cancelled")
		}
		if len(relative) == 0 || len(relative) > commandIsolationMaxPathBytes {
			return errors.New("command isolation refuses an oversized candidate path")
		}
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
		if hooks.afterLstat != nil {
			hooks.afterLstat(sourcePath)
		}
		if err := validateCandidatePathComponents(source, relative); err != nil {
			return err
		}
		destinationPath := filepath.Join(destination, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(destinationPath), 0o700); err != nil {
			return errors.New("cannot create command clone directory")
		}
		if err := os.Chmod(filepath.Dir(destinationPath), 0o700); err != nil {
			return errors.New("cannot protect command clone directory")
		}
		switch {
		case info.Mode().IsRegular():
			if info.Size() < 0 || info.Size() > commandIsolationMaxFileBytes || totalBytes > commandIsolationMaxTotalBytes-info.Size() {
				return errors.New("command isolation candidate exceeds the safe byte limit")
			}
			totalBytes += info.Size()
			if err := copyRegularCandidateFile(ctx, sourcePath, destinationPath, info); err != nil {
				return err
			}
			if hooks.afterCopy != nil {
				hooks.afterCopy(sourcePath)
			}
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(sourcePath)
			if err != nil || len(target) == 0 || len(target) > commandIsolationMaxTargetBytes || filepath.IsAbs(target) {
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

func copyRegularCandidateFile(ctx context.Context, source, destination string, expected os.FileInfo) error {
	input, err := openStableRegular(source, expected)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("cannot create a command clone file")
	}
	// The isolation root is 0700, so reproducing the exact candidate mode
	// does not expose bytes to other users. Exact mode reproduction is also
	// required for the post-clone authoritative Observation identity.
	if err := output.Chmod(expected.Mode().Perm()); err != nil {
		_ = output.Close()
		return errors.New("cannot protect a command clone file")
	}
	written, copyErr := copyStreamContext(ctx, output, input)
	closeErr := output.Close()
	finalInfo, statErr := input.Stat()
	if copyErr != nil || closeErr != nil || statErr != nil || written != expected.Size() || !os.SameFile(expected, finalInfo) || finalInfo.Size() != expected.Size() {
		return errors.New("cannot copy a candidate file")
	}
	return nil
}

func snapshotCommandTree(ctx context.Context, root string) (string, error) {
	return snapshotCommandTreeWithHooks(ctx, root, commandIsolationHooks{})
}

func snapshotCommandTreeWithHooks(ctx context.Context, root string, hooks commandIsolationHooks) (string, error) {
	var records []string
	var totalBytes int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return errors.New("command isolate snapshot deadline exceeded")
		}
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
		if len(records) >= commandIsolationMaxEntries || len(relative) > commandIsolationMaxPathBytes {
			return errors.New("command isolate snapshot exceeds the safe entry or path limit")
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if hooks.afterLstat != nil {
			hooks.afterLstat(path)
		}
		name := filepath.ToSlash(relative)
		switch {
		case info.IsDir():
			records = append(records, fmt.Sprintf("d\x00%s\x00%o", name, info.Mode().Perm()))
		case info.Mode().IsRegular():
			if info.Size() > commandIsolationMaxFileBytes || totalBytes > commandIsolationMaxTotalBytes-info.Size() {
				return errors.New("command isolate snapshot exceeds the safe byte limit")
			}
			totalBytes += info.Size()
			input, err := openStableRegular(path, info)
			if err != nil {
				return err
			}
			digest := sha256.New()
			read, copyErr := copyStreamContext(ctx, digest, input)
			finalInfo, statErr := input.Stat()
			closeErr := input.Close()
			if copyErr != nil || closeErr != nil || statErr != nil || read != info.Size() || !os.SameFile(info, finalInfo) || finalInfo.Size() != info.Size() {
				return errors.New("cannot digest a command isolate file")
			}
			records = append(records, fmt.Sprintf("f\x00%s\x00%o\x00%d\x00%s", name, info.Mode().Perm(), info.Size(), hex.EncodeToString(digest.Sum(nil))))
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if len(target) == 0 || len(target) > commandIsolationMaxTargetBytes {
				return errors.New("command isolate snapshot exceeds the safe symlink target limit")
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

func openStableRegular(path string, expected os.FileInfo) (*os.File, error) {
	input, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("cannot safely open a regular verifier file")
	}
	actual, statErr := input.Stat()
	if statErr != nil || !actual.Mode().IsRegular() || !expected.Mode().IsRegular() || !os.SameFile(expected, actual) || actual.Size() != expected.Size() {
		_ = input.Close()
		return nil, errors.New("verifier file changed type or identity after inspection")
	}
	return input, nil
}

func copyStreamContext(ctx context.Context, destination io.Writer, source io.ReadCloser) (int64, error) {
	stop := context.AfterFunc(ctx, func() { _ = source.Close() })
	defer stop()
	buffer := make([]byte, 128<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			written, writeErr := destination.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			if ctx.Err() != nil {
				return total, ctx.Err()
			}
			return total, readErr
		}
	}
}

func commandIsolationEnvironment(root string, inherited []string) ([]string, error) {
	result := append([]string(nil), inherited...)
	for name, relative := range map[string]string{
		"HOME":                "home",
		"TMPDIR":              "tmp",
		"TMP":                 "tmp",
		"TEMP":                "tmp",
		"XDG_CACHE_HOME":      "cache",
		"XDG_CONFIG_HOME":     "config",
		"XDG_DATA_HOME":       "data",
		"XDG_STATE_HOME":      "state",
		"PYTHONPYCACHEPREFIX": "python-cache",
		"PIP_CACHE_DIR":       "pip-cache",
		"GOCACHE":             "go-cache",
		"GOMODCACHE":          "go-mod-cache",
		"GOTMPDIR":            "go-tmp",
		"GOPATH":              "go-path",
		"npm_config_cache":    "npm-cache",
		"CARGO_HOME":          "cargo-home",
		"RUSTUP_HOME":         "rustup-home",
	} {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, errors.New("cannot create isolated command cache")
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return nil, errors.New("cannot protect isolated command cache")
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
