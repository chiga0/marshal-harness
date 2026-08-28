// Package stablegotest routes Darwin Go test binaries through one stable,
// user-owned execution path. The go command's anonymous test binary is treated
// only as input bytes; Marshal never executes that pathname directly.
//
// This is an ordinary-user compatibility mechanism for macOS endpoint policy.
// It is not a hardened sandbox and does not claim protection from another
// process running as the same user.
package stablegotest

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
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// InternalCommand is deliberately not a public Marshal command. The fixed
	// Marshal image is the only executable that production GOFLAGS names.
	InternalCommand = "__go-test-exec"

	exitOK          = 0
	exitFailure     = 1
	exitUsage       = 2
	exitUnavailable = 3

	lockName     = "lock"
	incomingName = "incoming"
	currentName  = "current"

	maxTestBinaryBytes int64 = 512 << 20

	activeEnvironment = "MARSHAL_STABLE_GO_TEST_ACTIVE"
)

var (
	errUnsupportedPlatform = errors.New("stable_go_test_unsupported_platform")
	errLockCanceled        = errors.New("stable_go_test_lock_canceled")
	errLockDeadline        = errors.New("stable_go_test_lock_deadline_exceeded")
)

// MaybeRun recognizes and runs the internal stable Go test entry point.
func MaybeRun(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (bool, int) {
	if len(args) == 0 || args[0] != InternalCommand {
		return false, 0
	}
	return true, run(ctx, args[1:], stdin, stdout, stderr)
}

// WithEnvironment adds the stable Darwin -exec binding to a complete child
// environment. Package-test processes are excluded because they are not a
// production Marshal image and cannot service InternalCommand.
func WithEnvironment(environment []string) ([]string, error) {
	result := append([]string(nil), environment...)
	if runtime.GOOS != "darwin" {
		return result, nil
	}
	if environmentValue(result, activeEnvironment) == "1" {
		return result, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, errors.New("stable go test: marshal identity unavailable")
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, errors.New("stable go test: marshal identity unavailable")
	}
	if strings.HasSuffix(filepath.Base(executable), ".test") {
		return result, nil
	}
	if !isMarshalImageName(filepath.Base(executable)) {
		return nil, errors.New("stable go test: running image is not Marshal")
	}
	info, err := os.Lstat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return nil, errors.New("stable go test: Marshal image is not a regular executable")
	}
	root, err := rootForExecutable(executable)
	if err != nil {
		return nil, err
	}
	existing := environmentValue(result, "GOFLAGS")
	identityDigest, err := digestRegularExecutable(executable)
	if err != nil {
		return nil, errors.New("stable go test: Marshal image digest unavailable")
	}
	merged, err := MergeGOFLAGS(existing, executable, root, identityDigest)
	if err != nil {
		return nil, err
	}
	return replaceEnvironmentValue(result, "GOFLAGS", merged), nil
}

func rootForExecutable(executable string) (string, error) {
	if !filepath.IsAbs(executable) {
		return "", errors.New("stable go test: marshal identity unavailable")
	}
	return filepath.Join(filepath.Dir(filepath.Clean(executable)), "test"), nil
}

// MergeGOFLAGS preserves all existing flags but refuses another -exec value.
// Two quoted parsing layers are required: GOFLAGS first parses the whole
// -exec= token, then the go command parses that flag's command vector.
func MergeGOFLAGS(existing, marshalPath, slotRoot, marshalDigest string) (string, error) {
	if !filepath.IsAbs(marshalPath) || !filepath.IsAbs(slotRoot) {
		return "", errors.New("stable go test: paths must be absolute")
	}
	if hasQuoteOrControl(marshalPath) || hasQuoteOrControl(slotRoot) {
		return "", errors.New("stable go test: paths cannot be represented safely")
	}
	if !validSHA256(marshalDigest) {
		return "", errors.New("stable go test: Marshal digest is invalid")
	}
	tokens, err := splitQuoted(existing)
	if err != nil {
		return "", errors.New("stable go test: invalid existing GOFLAGS")
	}
	for _, token := range tokens {
		name := token
		if index := strings.IndexByte(name, '='); index >= 0 {
			name = name[:index]
		}
		if name == "-exec" || name == "--exec" {
			return "", errors.New("stable go test: conflicting GOFLAGS -exec")
		}
	}
	execValue := quoteInner(marshalPath) + " " + InternalCommand + " --slot-root " + quoteInner(slotRoot) + " --marshal-sha256 " + marshalDigest
	stableToken := quoteOuter("-exec=" + execValue)
	if strings.TrimSpace(existing) == "" {
		return stableToken, nil
	}
	return strings.TrimSpace(existing) + " " + stableToken, nil
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if runtime.GOOS != "darwin" {
		writeFailure(stderr, errUnsupportedPlatform)
		return exitUnavailable
	}
	if os.Getenv(activeEnvironment) == "1" {
		writeFailure(stderr, errors.New("stable_go_test_nested_invocation_rejected"))
		return exitUnavailable
	}
	root, expectedMarshalDigest, source, childArgs, err := parseArgs(args)
	if err != nil {
		writeFailure(stderr, err)
		return exitUsage
	}
	self, err := os.Executable()
	if err != nil {
		writeFailure(stderr, errors.New("stable_go_test_self_identity_invalid"))
		return exitUnavailable
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		writeFailure(stderr, errors.New("stable_go_test_self_identity_invalid"))
		return exitUnavailable
	}
	observedMarshalDigest, err := digestRegularExecutable(self)
	if err != nil || observedMarshalDigest != expectedMarshalDigest {
		writeFailure(stderr, errors.New("stable_go_test_self_identity_mismatch"))
		return exitUnavailable
	}
	directory, err := openValidatedRoot(root, self, nil)
	if err != nil {
		writeFailure(stderr, errors.New("stable_go_test_root_invalid"))
		return exitUnavailable
	}
	defer directory.Close()

	lock, err := openLock(directory)
	if err != nil {
		writeFailure(stderr, errors.New("stable_go_test_lock_invalid"))
		return exitUnavailable
	}
	defer lock.Close()
	if err := acquireLock(ctx, lock); err != nil {
		switch {
		case errors.Is(err, errLockDeadline):
			writeFailure(stderr, errLockDeadline)
		case errors.Is(err, errLockCanceled):
			writeFailure(stderr, errLockCanceled)
		default:
			writeFailure(stderr, errors.New("stable_go_test_lock_failed"))
		}
		return exitUnavailable
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN) //nolint:errcheck -- process exit also releases the lock

	digest, installed, err := install(directory, source)
	if err != nil {
		writeFailure(stderr, errors.New("stable_go_test_input_rejected"))
		return exitUnavailable
	}
	defer installed.Close()
	if err := verifyCurrent(root, installed, digest); err != nil {
		writeFailure(stderr, errors.New("stable_go_test_current_mismatch"))
		return exitUnavailable
	}

	command := exec.Command(filepath.Join(root, currentName), childArgs...)
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
	// The upstream verifier/Adapter already supplies a complete, sanitized
	// environment. Preserve it byte-for-byte so project-specific test
	// variables keep their normal semantics. The injected GOFLAGS is retained
	// so a nested go command cannot bypass the stable path; activeEnvironment
	// makes that nested fixed-Marshal invocation fail closed before taking the
	// already-held lock instead of deadlocking or executing anonymous output.
	command.Env = childEnvironment(os.Environ())
	if err := command.Start(); err != nil {
		writeFailure(stderr, errors.New("stable_go_test_start_failed"))
		return exitFailure
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	err = waitWithCancellation(ctx, wait, 2*time.Second, func(signal syscall.Signal) error {
		return command.Process.Signal(signal)
	})
	return childExitCode(command, err)
}

func parseArgs(args []string) (string, string, string, []string, error) {
	if len(args) < 5 || args[0] != "--slot-root" || args[2] != "--marshal-sha256" || !filepath.IsAbs(args[1]) || !filepath.IsAbs(args[4]) || !validSHA256(args[3]) {
		return "", "", "", nil, errors.New("stable_go_test_usage")
	}
	root := filepath.Clean(args[1])
	if root == string(filepath.Separator) || root == filepath.VolumeName(root)+string(filepath.Separator) {
		return "", "", "", nil, errors.New("stable_go_test_usage")
	}
	return root, args[3], filepath.Clean(args[4]), append([]string(nil), args[5:]...), nil
}

func validateRoot(root, self string) error {
	if !filepath.IsAbs(root) || !filepath.IsAbs(self) {
		return errors.New("root identity is not absolute")
	}
	root = filepath.Clean(root)
	expectedRoot, err := rootForExecutable(self)
	if err != nil || root != expectedRoot {
		return errors.New("root is not an allowed stable slot")
	}
	return nil
}

func openValidatedRoot(root, self string, beforeOpen func()) (*os.File, error) {
	if err := validateRoot(root, self); err != nil {
		return nil, err
	}
	return openOrCreatePrivateRoot(root, beforeOpen)
}

// openOrCreatePrivateRoot creates only the final component. The caller must
// validate the exact root against the verified Marshal image before calling
// this function. Existing directories are observed, never chmod'd.
func openOrCreatePrivateRoot(root string, beforeOpen func()) (*os.File, error) {
	parentPath, base := filepath.Dir(root), filepath.Base(root)
	parentFD, err := unix.Open(parentPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(parentFD), "stable-go-test-parent")
	defer parent.Close()
	parentInfo, err := parent.Stat()
	pathParentInfo, pathErr := os.Lstat(parentPath)
	if err != nil || pathErr != nil || !parentInfo.IsDir() || !ownedByCurrentUser(parentInfo) || !os.SameFile(parentInfo, pathParentInfo) {
		return nil, errors.New("root parent identity mismatch")
	}
	created := false
	if err := unix.Mkdirat(parentFD, base, 0o700); err == nil {
		created = true
		if err := parent.Sync(); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, unix.EEXIST) {
		return nil, err
	}
	if beforeOpen != nil {
		beforeOpen()
	}
	fd, err := unix.Openat(parentFD, base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(fd), "stable-go-test-root")
	info, err := directory.Stat()
	pathInfo, pathErr := os.Lstat(root)
	if err != nil || pathErr != nil || !info.IsDir() || !ownedByCurrentUser(info) || !os.SameFile(info, pathInfo) {
		directory.Close()
		return nil, errors.New("root identity mismatch")
	}
	if created {
		if err := directory.Chmod(0o700); err != nil {
			directory.Close()
			return nil, err
		}
		info, err = directory.Stat()
	}
	if err != nil || info.Mode().Perm() != 0o700 {
		directory.Close()
		return nil, errors.New("root ownership or mode mismatch")
	}
	return directory, nil
}

func openRoot(root string) (*os.File, error) {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "stable-go-test-root")
	info, err := file.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || !ownedByCurrentUser(info) {
		file.Close()
		return nil, errors.New("root identity mismatch")
	}
	return file, nil
}

func openLock(directory *os.File) (*os.File, error) {
	fd, err := unix.Openat(int(directory.Fd()), lockName, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "stable-go-test-lock")
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ownedByCurrentUser(info) || linkCount(info) != 1 {
		file.Close()
		return nil, errors.New("lock identity mismatch")
	}
	return file, nil
}

func acquireLock(ctx context.Context, lock *os.File) error {
	if ctx == nil {
		ctx = context.Background()
	}
	const retryInterval = 10 * time.Millisecond
	for {
		select {
		case <-ctx.Done():
			return lockContextError(ctx.Err())
		default:
		}
		err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return lockContextError(ctx.Err())
		case <-timer.C:
		}
	}
}

func lockContextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", errLockDeadline, context.DeadlineExceeded)
	}
	return fmt.Errorf("%w: %w", errLockCanceled, context.Canceled)
}

func install(directory *os.File, sourcePath string) ([sha256.Size]byte, *os.File, error) {
	var zero [sha256.Size]byte
	sourceFD, err := unix.Open(sourcePath, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return zero, nil, err
	}
	source := os.NewFile(uintptr(sourceFD), "stable-go-test-input")
	defer source.Close()
	before, err := source.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Mode()&0o111 == 0 || before.Size() <= 0 || before.Size() > maxTestBinaryBytes || !ownedByCurrentUser(before) || linkCount(before) != 1 {
		return zero, nil, errors.New("input identity invalid")
	}

	incomingFD, err := unix.Openat(int(directory.Fd()), incomingName, unix.O_WRONLY|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o700)
	if err != nil {
		return zero, nil, err
	}
	incoming := os.NewFile(uintptr(incomingFD), "stable-go-test-incoming")
	defer incoming.Close()
	incomingInfo, err := incoming.Stat()
	if err != nil || !incomingInfo.Mode().IsRegular() || !ownedByCurrentUser(incomingInfo) || linkCount(incomingInfo) != 1 {
		return zero, nil, errors.New("incoming identity invalid")
	}
	if err := incoming.Truncate(0); err != nil {
		return zero, nil, err
	}
	if err := incoming.Chmod(0o700); err != nil {
		return zero, nil, err
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(incoming, hash), source)
	if err != nil || written != before.Size() {
		return zero, nil, errors.New("input copy incomplete")
	}
	after, err := source.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return zero, nil, errors.New("input changed during copy")
	}
	if err := incoming.Sync(); err != nil {
		return zero, nil, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	if err := unix.Renameat(int(directory.Fd()), incomingName, int(directory.Fd()), currentName); err != nil {
		return zero, nil, err
	}
	if err := directory.Sync(); err != nil {
		return zero, nil, err
	}
	currentFD, err := unix.Openat(int(directory.Fd()), currentName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return zero, nil, err
	}
	return digest, os.NewFile(uintptr(currentFD), "stable-go-test-current"), nil
}

func verifyCurrent(root string, held *os.File, expected [sha256.Size]byte) error {
	heldInfo, err := held.Stat()
	if err != nil || !heldInfo.Mode().IsRegular() || heldInfo.Mode().Perm() != 0o700 || !ownedByCurrentUser(heldInfo) || linkCount(heldInfo) != 1 {
		return errors.New("held current invalid")
	}
	pathInfo, err := os.Lstat(filepath.Join(root, currentName))
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(heldInfo, pathInfo) {
		return errors.New("current path mismatch")
	}
	if _, err := held.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, held); err != nil {
		return err
	}
	var observed [sha256.Size]byte
	copy(observed[:], hash.Sum(nil))
	if observed != expected {
		return errors.New("current digest mismatch")
	}
	return nil
}

func childExitCode(command *exec.Cmd, err error) int {
	if command == nil || command.ProcessState == nil {
		return exitFailure
	}
	status, ok := command.ProcessState.Sys().(syscall.WaitStatus)
	if ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	code := command.ProcessState.ExitCode()
	if err == nil && code == 0 {
		return exitOK
	}
	if code >= 0 {
		return code
	}
	return exitFailure
}

func waitWithCancellation(ctx context.Context, wait <-chan error, grace time.Duration, signal func(syscall.Signal) error) error {
	select {
	case err := <-wait:
		return err
	case <-ctx.Done():
		_ = signal(syscall.SIGTERM)
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case err := <-wait:
		return err
	case <-timer.C:
		_ = signal(syscall.SIGKILL)
		return <-wait
	}
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

func linkCount(info os.FileInfo) uint64 {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return uint64(stat.Nlink)
}

func isMarshalImageName(name string) bool {
	return name == "marshal" || name == "marshal-server" || strings.HasPrefix(name, "marshal_")
}

func hasQuoteOrControl(value string) bool {
	return strings.ContainsAny(value, "'\"\r\n\x00")
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func digestRegularExecutable(path string) (string, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(fd), "stable-go-test-marshal-image")
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 || !ownedByCurrentUser(info) {
		return "", errors.New("invalid executable")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func quoteInner(value string) string { return "'" + value + "'" }
func quoteOuter(value string) string { return "\"" + value + "\"" }

func environmentValue(environment []string, name string) string {
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key == name {
			return value
		}
	}
	return ""
}

func childEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, ok := strings.Cut(entry, "=")
		if ok && name == activeEnvironment {
			continue
		}
		result = append(result, entry)
	}
	return replaceEnvironmentValue(result, activeEnvironment, "1")
}

func replaceEnvironmentValue(environment []string, name, value string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key != name {
			result = append(result, entry)
		}
	}
	return append(result, name+"="+value)
}

// splitQuoted mirrors the two quote forms accepted by cmd/go GOFLAGS.
func splitQuoted(value string) ([]string, error) {
	var result []string
	for len(value) > 0 {
		value = strings.TrimLeft(value, " \t\r\n")
		if value == "" {
			break
		}
		if value[0] == '\'' || value[0] == '"' {
			quote := value[0]
			end := strings.IndexByte(value[1:], quote)
			if end < 0 {
				return nil, errors.New("unterminated quote")
			}
			result = append(result, value[1:1+end])
			value = value[2+end:]
			continue
		}
		end := strings.IndexAny(value, " \t\r\n")
		if end < 0 {
			result = append(result, value)
			break
		}
		result = append(result, value[:end])
		value = value[end:]
	}
	return result, nil
}

func writeFailure(stderr io.Writer, err error) {
	_, _ = fmt.Fprintf(stderr, "marshal stable go test: %s\n", err.Error())
}
