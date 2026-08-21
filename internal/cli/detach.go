//go:build darwin || linux

package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/repository"
)

// --detach (Issue #56) separates the driver session of `task run` and
// `task publish` from the calling CLI session with the classic daemon
// pattern — double fork + setsid — expressed through Go fork/exec
// primitives:
//
//  1. fork #1 + setsid: the parent starts the hidden intermediate stage
//     (__detach) with SysProcAttr{Setsid: true}, so the stage leaves the
//     caller's session and process group immediately and even the detach
//     launch window is immune to caller-side group cleanup. setsid happens
//     between fork and exec, which is the safe equivalent of
//     syscall.Setsid in a multi-threaded Go runtime.
//  2. fork #2 + setsid: the intermediate stage starts the actual driver
//     (the same command re-invoked without any detach flags) with
//     SysProcAttr{Setsid: true} and all stdio redirected, reports the
//     driver pid and exits immediately. setsid therefore happens inside
//     the second child of the double fork: the final driver leads a
//     brand-new session and a brand-new process group (pgid == driver
//     pid), never inherits the caller's terminal, and the intermediate
//     process cannot linger. The driver's stdio is /dev/null plus log
//     files, so leading a new session can never acquire a controlling
//     terminal.
//
// Caller-side process group cleanup (kill -- -PGID), session teardown or
// terminal hangups can no longer reach the driver. Every fork/setsid/
// redirection failure is fail-closed: the parent exits non-zero with a
// stderr diagnostic and never reports a detached pid.
//
// Fail-closed discipline details:
//   - Log targets are purely validated by the parent before any fork: the
//     parent path must already exist and be a directory, and the target
//     must be either absent (atomically creatable with O_EXCL) or an
//     existing regular file (reopened for append). A failed validation
//     exits non-zero and creates zero path components — intermediate
//     directories included. Only after every target validates does the
//     parent create the default detached/ directory (when in use) and
//     open the log files. The detached stages never create log targets.
//   - The intermediate stage marks every inherited fd close-on-exec before
//     starting the driver: the stdio fds are dup'd into 0/1/2 before exec,
//     while the report-pipe write end must not leak, because the parent
//     only returns once every writer of that pipe is closed.
//   - The stage ignores SIGHUP before exec; SIG_IGN dispositions survive
//     exec, so the final driver is immune to hangups emitted when the
//     caller session departs.

const (
	detachInternalCommand = "__detach"
	detachSeparator       = "--"

	// Fixed child fd layout established through cmd.ExtraFiles.
	detachReportFD = 3
	detachStdinFD  = 4
	detachLogFD    = 5
	detachLogErrFD = 6
)

// detachSelfArgv returns the argv prefix used to re-execute this process for
// the detached stages. Production re-executes the current executable; tests
// substitute the test binary with a helper entry point.
var detachSelfArgv = detachDefaultSelfArgv

// detachExtraEnv is appended to the intermediate stage environment
// (production: none; tests use it to mark helper processes).
var detachExtraEnv []string

func detachDefaultSelfArgv() ([]string, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve current executable: %w", err)
	}
	return []string{executable}, nil
}

// detachRequest describes one --detach invocation. FinalArgs is the driver
// argv (after the executable) and must never contain detach flags, so the
// driver cannot re-enter the detach path.
type detachRequest struct {
	RunID      string
	StateRoot  string
	FinalArgs  []string
	LogPath    string
	LogErrPath string
	JSON       bool
}

type detachLogs struct {
	stdoutPath string
	stderrPath string
}

type detachReport struct {
	Detached bool   `json:"detached"`
	PID      int    `json:"pid"`
	Log      string `json:"log"`
	LogErr   string `json:"logErr"`
}

func resolveDetachLogs(request detachRequest) (detachLogs, error) {
	stdoutPath, err := canonicalDetachLogPath(request.LogPath)
	if err != nil {
		return detachLogs{}, err
	}
	if stdoutPath == "" {
		if strings.TrimSpace(request.StateRoot) == "" {
			return detachLogs{}, fmt.Errorf("default detach log path requires a state root")
		}
		stdoutPath = filepath.Join(request.StateRoot, "detached", request.RunID+".log")
	}
	stderrPath, err := canonicalDetachLogPath(request.LogErrPath)
	if err != nil {
		return detachLogs{}, err
	}
	if stderrPath == "" {
		stderrPath = stdoutPath
	}
	return detachLogs{stdoutPath: stdoutPath, stderrPath: stderrPath}, nil
}

func canonicalDetachLogPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", nil
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolve detach log path %q: %w", trimmed, err)
	}
	return absolute, nil
}

// taskRunDetachedArgs rebuilds the exact driver argv for `task run` without
// any detach flags. Recovery is part of the semantic command and must be
// preserved across the double-fork boundary; dropping it would turn an
// explicitly proven dead-driver recovery into a normal stale-window run.
func taskRunDetachedArgs(runID string, throughVerify, recoverDeadDriver, jsonOutput bool) []string {
	args := []string{"task", "run", "--run", runID}
	if throughVerify {
		args = append(args, "--through-verify")
	}
	if recoverDeadDriver {
		args = append(args, "--recover-dead-driver")
	}
	if jsonOutput {
		args = append(args, "--json")
	}
	return args
}

// taskPublishDetachedArgs rebuilds the exact driver argv for `task publish`
// without any detach flags.
func taskPublishDetachedArgs(runID string, jsonOutput bool) []string {
	args := []string{"task", "publish", "--run", runID}
	if jsonOutput {
		args = append(args, "--json")
	}
	return args
}

// detachTaskCommand performs the shared --detach preflight for task run and
// task publish: Run ID validation (the ID becomes part of the default log
// file name), repository identity validation and the fail-closed detach. All
// task semantics stay in the driver, which re-invokes the identical command
// without detach flags; behaviour without --detach is untouched.
func detachTaskCommand(stdout, stderr io.Writer, request detachRequest) int {
	if err := domain.ValidateID(request.RunID); err != nil {
		fmt.Fprintln(stderr, "分离失败：Run ID 无效。")
		return ExitUsage
	}
	location, err := repository.Discover(".")
	if err != nil {
		fmt.Fprintf(stderr, "分离失败：%v\n", err)
		return ExitFailure
	}
	if err := location.ValidateIdentity(); err != nil {
		fmt.Fprintf(stderr, "分离失败：%v\n", err)
		return ExitFailure
	}
	request.StateRoot = location.StateRoot
	return executeDetach(stdout, stderr, request)
}

// executeDetach runs the parent half of the double fork: it purely
// validates every redirection target fail-closed (zero path components on
// failure), opens them, starts the intermediate stage in a new session,
// waits for the driver pid report and prints it.
func executeDetach(stdout, stderr io.Writer, request detachRequest) int {
	if len(request.FinalArgs) == 0 {
		fmt.Fprintln(stderr, "分离失败：缺少驱动命令。")
		return ExitUsage
	}
	logs, err := resolveDetachLogs(request)
	if err != nil {
		fmt.Fprintf(stderr, "分离失败：%v\n", err)
		return ExitFailure
	}
	// Fail-closed ordering: every redirection target is purely validated
	// before any fork and before any path component is created — the parent
	// path must already exist and be a directory (only the default
	// detached/ directory may still be created) and the target must be
	// absent or an existing regular file. Redirection problems must surface
	// on the caller's terminal with a non-zero exit instead of silently
	// losing a detached driver, and a failed validation must leave nothing
	// behind, intermediate directories included.
	defaultDir := ""
	if strings.TrimSpace(request.LogPath) == "" {
		defaultDir = filepath.Dir(logs.stdoutPath)
	}
	if err := validateDetachLogTarget(logs.stdoutPath, defaultDir != ""); err != nil {
		fmt.Fprintf(stderr, "分离失败：%v\n", err)
		return ExitFailure
	}
	if logs.stderrPath != logs.stdoutPath {
		if err := validateDetachLogTarget(logs.stderrPath, false); err != nil {
			fmt.Fprintf(stderr, "分离失败：%v\n", err)
			return ExitFailure
		}
	}
	if defaultDir != "" {
		if err := os.MkdirAll(defaultDir, 0o700); err != nil {
			fmt.Fprintf(stderr, "分离失败：无法创建日志目录：%v\n", err)
			return ExitFailure
		}
	}
	logOut, logErr, err := openDetachLogs(logs)
	if err != nil {
		fmt.Fprintf(stderr, "分离失败：无法打开日志文件：%v\n", err)
		return ExitFailure
	}
	defer logOut.Close()
	if logErr != logOut {
		defer logErr.Close()
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		fmt.Fprintf(stderr, "分离失败：无法打开 %s：%v\n", os.DevNull, err)
		return ExitFailure
	}
	defer devNull.Close()
	reportRead, reportWrite, err := os.Pipe()
	if err != nil {
		fmt.Fprintf(stderr, "分离失败：无法创建回报管道：%v\n", err)
		return ExitFailure
	}
	defer reportRead.Close()
	selfArgs, err := detachSelfArgv()
	if err != nil {
		fmt.Fprintf(stderr, "分离失败：%v\n", err)
		return ExitFailure
	}
	stageArgs := append(append([]string{}, selfArgs...), detachInternalCommand, detachSeparator)
	stageArgs = append(stageArgs, request.FinalArgs...)
	stage := exec.Command(stageArgs[0], stageArgs[1:]...)
	// fork #1 + setsid: the intermediate stage leaves the caller's session
	// and process group entirely.
	stage.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	stage.Stdout = stdout
	stage.Stderr = stderr
	stage.ExtraFiles = []*os.File{reportWrite, devNull, logOut, logErr}
	stage.Env = append(append([]string{}, os.Environ()...), detachExtraEnv...)
	if err := stage.Start(); err != nil {
		reportWrite.Close()
		fmt.Fprintf(stderr, "分离失败：无法启动中间进程：%v\n", err)
		return ExitFailure
	}
	// Close the parent's report-write copy so the pipe reaches EOF as soon
	// as the intermediate stage exits.
	reportWrite.Close()
	pidData, readErr := io.ReadAll(reportRead)
	waitErr := stage.Wait()
	if waitErr != nil {
		fmt.Fprintf(stderr, "分离失败：中间进程未成功启动驱动：%v\n", waitErr)
		return ExitFailure
	}
	if readErr != nil {
		fmt.Fprintf(stderr, "分离失败：读取驱动 pid 回报失败：%v\n", readErr)
		return ExitFailure
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil || pid <= 0 {
		fmt.Fprintln(stderr, "分离失败：中间进程未回报驱动 pid。")
		return ExitFailure
	}
	report := detachReport{Detached: true, PID: pid, Log: logs.stdoutPath, LogErr: logs.stderrPath}
	if request.JSON {
		if err := writeJSON(stdout, report); err != nil {
			fmt.Fprintf(stderr, "输出分离结果失败：%v\n", err)
			return ExitFailure
		}
		return ExitOK
	}
	fmt.Fprintf(stdout, "detached pid=%d log=%s log-err=%s\n", report.PID, report.Log, report.LogErr)
	return ExitOK
}

// validateDetachLogTarget purely checks one redirection target without
// creating anything: the parent path must already exist and be a
// directory, and the target must be either absent — atomically creatable
// with O_EXCL — or an existing regular file that is reopened for append.
// parentCreatable is only ever true for the default log target, whose
// detached/ directory is created after every target validates; until then
// even that directory must not appear, so the state root that would
// contain it is checked instead. Directory checks follow symlinks
// (symlinked directories stay usable, as before), while the target itself
// is inspected with lstat: a symlinked log file fails closed instead of
// being opened through the link. Any failure aborts fail-closed before
// any fork and leaves zero path components behind.
func validateDetachLogTarget(path string, parentCreatable bool) error {
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) && parentCreatable {
			parentInfo, parentErr := os.Stat(filepath.Dir(dir))
			if parentErr != nil {
				return fmt.Errorf("日志目录不可用 %s：%w", filepath.Dir(dir), parentErr)
			}
			if !parentInfo.IsDir() {
				return fmt.Errorf("日志目录不是目录：%s", filepath.Dir(dir))
			}
			return nil
		}
		return fmt.Errorf("日志目录不可用 %s：%w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("日志目录不是目录：%s", dir)
	}
	info, err = os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("无法检查日志文件 %s：%w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("日志文件是目录：%s", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("日志文件不是常规文件：%s", path)
	}
	return nil
}

// openDetachLogs opens every redirection target fail-closed and leaves no
// residue when the pair cannot be fully prepared: a file created by this
// call is removed again if the second target fails, while pre-existing
// files are only ever reopened for append.
func openDetachLogs(logs detachLogs) (*os.File, *os.File, error) {
	logOut, stdoutCreated, err := prepareDetachLogFile(logs.stdoutPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open stdout log %s: %w", logs.stdoutPath, err)
	}
	if logs.stderrPath == logs.stdoutPath {
		return logOut, logOut, nil
	}
	logErr, _, err := prepareDetachLogFile(logs.stderrPath)
	if err != nil {
		logOut.Close()
		if stdoutCreated {
			_ = os.Remove(logs.stdoutPath)
		}
		return nil, nil, fmt.Errorf("open stderr log %s: %w", logs.stderrPath, err)
	}
	return logOut, logErr, nil
}

// prepareDetachLogFile opens path for append, creating it atomically
// (O_EXCL) when it does not exist yet. The boolean result reports whether
// this call created the file, so a failed overall preparation can roll the
// target back without touching pre-existing logs.
func prepareDetachLogFile(path string) (*os.File, bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o600)
	if err == nil {
		return file, true, nil
	}
	if !os.IsExist(err) {
		return nil, false, err
	}
	file, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, false, err
	}
	if info, statErr := file.Stat(); statErr == nil && info.IsDir() {
		file.Close()
		return nil, false, &os.PathError{Op: "open", Path: path, Err: syscall.EISDIR}
	}
	return file, false, nil
}

// runInternalDetach is the hidden intermediate stage of the double fork. It
// runs in the fresh session created for it by SysProcAttr{Setsid: true},
// starts the final driver with SysProcAttr{Setsid: true} and all stdio
// redirected (so setsid happens inside the second child), reports the
// driver pid over the inherited report pipe and exits immediately: the
// driver must outlive it and must lead its own session and process group.
func runInternalDetach(stderr io.Writer) int {
	finalArgs := detachFinalArgs(os.Args[1:])
	if len(finalArgs) == 0 {
		fmt.Fprintln(stderr, "内部分离调用无效。")
		return ExitUsage
	}
	report := os.NewFile(detachReportFD, "detach-report")
	stdinFile := os.NewFile(detachStdinFD, "detach-stdin")
	stdoutFile := os.NewFile(detachLogFD, "detach-log")
	stderrFile := os.NewFile(detachLogErrFD, "detach-log-err")
	for _, file := range []*os.File{report, stdinFile, stdoutFile, stderrFile} {
		if _, err := file.Stat(); err != nil {
			fmt.Fprintf(stderr, "分离失败：继承的分离文件描述符无效：%v\n", err)
			return ExitFailure
		}
	}
	// fd hygiene before fork #2: the inherited fds must not leak into the
	// driver. The stdio fds are dup'd into 0/1/2 before exec (which clears
	// CLOEXEC on the destination), but the report-pipe write end has to be
	// closed in the driver: the parent reaches EOF on the report pipe only
	// once every writer is closed, and a leaked write end would pin the
	// caller inside executeDetach until the driver itself exits.
	for _, fd := range []int{detachReportFD, detachStdinFD, detachLogFD, detachLogErrFD} {
		syscall.CloseOnExec(fd)
	}
	// SIG_IGN dispositions survive exec: ignoring SIGHUP here makes the
	// final driver immune to hangups emitted when the caller session
	// departs, on top of already living in its own session and group.
	signal.Ignore(syscall.SIGHUP)
	selfArgs, err := detachSelfArgv()
	if err != nil {
		fmt.Fprintf(stderr, "分离失败：%v\n", err)
		return ExitFailure
	}
	driverArgs := append(append([]string{}, selfArgs...), finalArgs...)
	driver := exec.Command(driverArgs[0], driverArgs[1:]...)
	// fork #2 + setsid: setsid happens inside this second child, between
	// fork and exec, so the final driver leads a brand-new session and
	// process group (pgid == driver pid) and can never be reached through
	// the caller's session or process group.
	driver.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	driver.Stdin = stdinFile
	driver.Stdout = stdoutFile
	driver.Stderr = stderrFile
	if err := driver.Start(); err != nil {
		fmt.Fprintf(stderr, "分离失败：无法启动驱动进程：%v\n", err)
		return ExitFailure
	}
	if _, err := fmt.Fprintf(report, "%d", driver.Process.Pid); err != nil {
		fmt.Fprintf(stderr, "分离失败：无法回报驱动 pid：%v\n", err)
		return ExitFailure
	}
	if err := report.Close(); err != nil {
		fmt.Fprintf(stderr, "分离失败：无法关闭回报管道：%v\n", err)
		return ExitFailure
	}
	return ExitOK
}

// detachFinalArgs returns the driver argv following the first "--" separator
// in args, or nil when the separator is absent.
func detachFinalArgs(args []string) []string {
	for index, arg := range args {
		if arg == detachSeparator {
			return append([]string(nil), args[index+1:]...)
		}
	}
	return nil
}
