//go:build darwin || linux

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	detachStdoutMarker = "detach-driver-stdout-marker"
	detachStderrMarker = "detach-driver-stderr-marker"

	detachTestStageEnv        = "MARSHAL_DETACH_STAGE_HELPER"
	detachTestDriverReportEnv = "MARSHAL_DETACH_DRIVER_REPORT"
	detachTestCallerDirEnv    = "MARSHAL_DETACH_CALLER_DIR"

	detachTestStageRunFlag       = "-test.run=^TestDetachStageHelper$"
	detachTestSilentStageRunFlag = "-test.run=^TestDetachSilentStageHelper$"
	detachTestCallerRunFlag      = "-test.run=^TestDetachCallerHelper$"
	detachTestDriverRunFlag      = "-test.run=^TestDetachDriverHelper$"
)

// withDetachTestHooks redirects the detach stages into this test binary:
// the intermediate stage re-executes the test binary with the stage helper
// entry point, and the helper environment marks stage/driver processes.
func withDetachTestHooks(t *testing.T, driverReportPath string) {
	t.Helper()
	previousSelfArgv := detachSelfArgv
	previousExtraEnv := detachExtraEnv
	detachSelfArgv = func() ([]string, error) {
		return []string{os.Args[0], detachTestStageRunFlag}, nil
	}
	detachExtraEnv = []string{
		detachTestStageEnv + "=1",
		detachTestDriverReportEnv + "=" + driverReportPath,
	}
	t.Cleanup(func() {
		detachSelfArgv = previousSelfArgv
		detachExtraEnv = previousExtraEnv
	})
}

// TestDetachStageHelper is the re-exec entry point for the intermediate
// detach stage when the detach mechanism runs under the test binary.
func TestDetachStageHelper(t *testing.T) {
	if os.Getenv(detachTestStageEnv) != "1" {
		return
	}
	os.Exit(runInternalDetach(os.Stderr))
}

// TestDetachSilentStageHelper simulates an intermediate stage that dies
// before reporting the driver pid: the parent must fail closed.
func TestDetachSilentStageHelper(t *testing.T) {
	if os.Getenv(detachTestStageEnv) != "1" {
		return
	}
	os.Exit(0)
}

// TestDetachDriverHelper is the re-exec entry point for the final detached
// driver. It proves stdio redirection by writing one marker to stdout and
// one to stderr, reports its own pid, then stays alive until the test
// releases it so liveness assertions never race the driver's exit.
func TestDetachDriverHelper(t *testing.T) {
	reportPath := os.Getenv(detachTestDriverReportEnv)
	if reportPath == "" {
		return
	}
	fmt.Println(detachStdoutMarker)
	fmt.Fprintln(os.Stderr, detachStderrMarker)
	report, err := json.Marshal(struct {
		PID int `json:"pid"`
	}{PID: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, report, 0o600); err != nil {
		t.Fatal(err)
	}
	release := reportPath + ".release"
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(release); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestDetachCallerHelper is a nested "caller CLI session": it runs in its
// own process group and performs a full --detach, so the outer test can
// issue kill -- -PGID against the whole caller group and prove the driver
// survives (Issue #56).
func TestDetachCallerHelper(t *testing.T) {
	directory := os.Getenv(detachTestCallerDirEnv)
	if directory == "" {
		return
	}
	previousSelfArgv := detachSelfArgv
	detachSelfArgv = func() ([]string, error) {
		return []string{os.Args[0], detachTestStageRunFlag}, nil
	}
	defer func() { detachSelfArgv = previousSelfArgv }()
	detachExtraEnv = []string{
		detachTestStageEnv + "=1",
		detachTestDriverReportEnv + "=" + filepath.Join(directory, "driver-report.json"),
	}
	var stdout, stderr bytes.Buffer
	exit := executeDetach(&stdout, &stderr, detachRequest{
		RunID: "detach-caller-run", StateRoot: directory,
		FinalArgs:  []string{detachTestDriverRunFlag},
		LogPath:    filepath.Join(directory, "driver.log"),
		LogErrPath: filepath.Join(directory, "driver.err.log"),
	})
	state, err := json.Marshal(struct {
		Exit   int    `json:"exit"`
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
	}{Exit: exit, Stdout: stdout.String(), Stderr: stderr.String()})
	if err == nil {
		_ = os.WriteFile(filepath.Join(directory, "caller-state.json"), state, 0o600)
	}
	if exit != ExitOK {
		os.Exit(exit)
	}
	// Stay alive as the caller session until the outer test kills the group.
	time.Sleep(30 * time.Second)
}

func TestDetachLogPathResolution(t *testing.T) {
	logs, err := resolveDetachLogs(detachRequest{RunID: "run-1", StateRoot: "/tmp/detach-state"})
	if err != nil {
		t.Fatal(err)
	}
	wantDefault := "/tmp/detach-state/detached/run-1.log"
	if logs.stdoutPath != wantDefault || logs.stderrPath != wantDefault {
		t.Fatalf("default logs = %+v, want stdout=stderr=%s", logs, wantDefault)
	}

	logs, err = resolveDetachLogs(detachRequest{RunID: "run-1", StateRoot: "/tmp/detach-state", LogPath: "/tmp/detach-explicit/out.log"})
	if err != nil {
		t.Fatal(err)
	}
	if logs.stdoutPath != "/tmp/detach-explicit/out.log" || logs.stderrPath != "/tmp/detach-explicit/out.log" {
		t.Fatalf("stdout-only logs = %+v, stderr must fall back to the stdout log", logs)
	}

	logs, err = resolveDetachLogs(detachRequest{
		RunID: "run-1", StateRoot: "/tmp/detach-state",
		LogPath: "/tmp/detach-explicit/out.log", LogErrPath: "/tmp/detach-explicit/err.log",
	})
	if err != nil {
		t.Fatal(err)
	}
	if logs.stdoutPath != "/tmp/detach-explicit/out.log" || logs.stderrPath != "/tmp/detach-explicit/err.log" {
		t.Fatalf("explicit logs = %+v", logs)
	}

	logs, err = resolveDetachLogs(detachRequest{RunID: "run-1", StateRoot: "/tmp/detach-state", LogPath: "relative/driver.log"})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(logs.stdoutPath) || !strings.HasSuffix(logs.stdoutPath, filepath.Join("relative", "driver.log")) {
		t.Fatalf("relative log path was not canonicalized: %+v", logs)
	}

	if _, err := resolveDetachLogs(detachRequest{RunID: "run-1"}); err == nil {
		t.Fatal("default log path without a state root must fail closed")
	}
}

func TestDetachDriverArgBuilders(t *testing.T) {
	if got := taskRunDetachedArgs("run-1", false, false, false); !reflect.DeepEqual(got, []string{"task", "run", "--run", "run-1"}) {
		t.Fatalf("task run args = %v", got)
	}
	if got := taskRunDetachedArgs("run-1", true, false, true); !reflect.DeepEqual(got, []string{"task", "run", "--run", "run-1", "--through-verify", "--json"}) {
		t.Fatalf("task run args with flags = %v", got)
	}
	if got := taskRunDetachedArgs("run-1", true, true, true); !reflect.DeepEqual(got, []string{"task", "run", "--run", "run-1", "--through-verify", "--recover-dead-driver", "--json"}) {
		t.Fatalf("task run args with recovery = %v", got)
	}
	if got := taskPublishDetachedArgs("run-1", false); !reflect.DeepEqual(got, []string{"task", "publish", "--run", "run-1"}) {
		t.Fatalf("task publish args = %v", got)
	}
	if got := taskPublishDetachedArgs("run-1", true); !reflect.DeepEqual(got, []string{"task", "publish", "--run", "run-1", "--json"}) {
		t.Fatalf("task publish args with json = %v", got)
	}
}

func TestDetachFinalArgsSeparator(t *testing.T) {
	if got := detachFinalArgs([]string{"__detach", "--", "task", "run", "--run", "r1"}); !reflect.DeepEqual(got, []string{"task", "run", "--run", "r1"}) {
		t.Fatalf("production layout = %v", got)
	}
	if got := detachFinalArgs([]string{"-test.run=^TestX$", "__detach", "--", "marker"}); !reflect.DeepEqual(got, []string{"marker"}) {
		t.Fatalf("test layout = %v", got)
	}
	if got := detachFinalArgs([]string{"__detach"}); got != nil {
		t.Fatalf("missing separator = %v", got)
	}
}

func TestDetachFlagUsage(t *testing.T) {
	for _, args := range [][]string{
		{"task", "run", "--run", "detach-usage-run", "--log", "/tmp/detach-usage.log"},
		{"task", "run", "--run", "detach-usage-run", "--log-err", "/tmp/detach-usage.log"},
		{"task", "publish", "--run", "detach-usage-run", "--log", "/tmp/detach-usage.log"},
		{"task", "publish", "--run", "detach-usage-run", "--log-err", "/tmp/detach-usage.log"},
	} {
		var stdout, stderr bytes.Buffer
		if exit := Run(args, strings.NewReader(""), &stdout, &stderr); exit != ExitUsage {
			t.Fatalf("Run(%v) exit = %d, want %d; stderr = %q", args, exit, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "--log/--log-err") {
			t.Fatalf("Run(%v) stderr = %q, want --log/--log-err diagnostic", args, stderr.String())
		}
	}

	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"task", "run", "--detach"}, strings.NewReader(""), &stdout, &stderr); exit != ExitUsage {
		t.Fatalf("task run --detach without --run exit = %d, stderr = %q", exit, stderr.String())
	}
	if !strings.Contains(stderr.String(), "marshal task run") {
		t.Fatalf("usage diagnostic missing: %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "publish", "--detach"}, strings.NewReader(""), &stdout, &stderr); exit != ExitUsage {
		t.Fatalf("task publish --detach without --run exit = %d, stderr = %q", exit, stderr.String())
	}

	// Invalid Run ID must fail before any fork or repository access.
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "run", "--run", "../escape", "--detach"}, strings.NewReader(""), &stdout, &stderr); exit != ExitUsage {
		t.Fatalf("invalid Run ID with --detach exit = %d, stderr = %q", exit, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Run ID 无效") {
		t.Fatalf("invalid Run ID diagnostic missing: %q", stderr.String())
	}

	// The internal detach stage stays hidden and fail-closed.
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"help"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("help exit = %d", exit)
	}
	if strings.Contains(stdout.String(), "__detach") {
		t.Fatal("internal detach stage appeared in public help")
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"__detach"}, strings.NewReader(""), &stdout, &stderr); exit == ExitOK {
		t.Fatalf("bare __detach must fail closed, stderr = %q", stderr.String())
	}
}

func TestDetachFailsClosedOnUnusableLogTargets(t *testing.T) {
	stateRoot := t.TempDir()

	t.Run("missing log parent directory is never created", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "missing")
		logPath := filepath.Join(parent, "driver.log")
		var stdout, stderr bytes.Buffer
		exit := executeDetach(&stdout, &stderr, detachRequest{
			RunID: "detach-fc-run", StateRoot: stateRoot,
			FinalArgs: []string{detachTestDriverRunFlag},
			LogPath:   logPath,
		})
		if exit == ExitOK {
			t.Fatalf("exit = %d, want failure; stdout = %q", exit, stdout.String())
		}
		if strings.Contains(stdout.String(), "detached pid=") {
			t.Fatalf("failure reported a detached pid: %q", stdout.String())
		}
		if !strings.Contains(stderr.String(), "日志目录") {
			t.Fatalf("stderr = %q, want log directory diagnostic", stderr.String())
		}
		// Fail-closed ordering: the pure validation failed before any fork
		// and before any path component was created — the target and its
		// missing parent directory must both lstat as ENOENT afterwards.
		if _, err := os.Lstat(logPath); !os.IsNotExist(err) {
			t.Fatalf("log target lstat = %v, want ENOENT after fail-closed detach", err)
		}
		if _, err := os.Lstat(parent); !os.IsNotExist(err) {
			t.Fatalf("log parent directory lstat = %v, want ENOENT after fail-closed detach", err)
		}
	})

	t.Run("log parent path is a regular file", func(t *testing.T) {
		directory := t.TempDir()
		blocker := filepath.Join(directory, "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		logPath := filepath.Join(blocker, "nested", "driver.log")
		var stdout, stderr bytes.Buffer
		exit := executeDetach(&stdout, &stderr, detachRequest{
			RunID: "detach-fc-run", StateRoot: stateRoot,
			FinalArgs: []string{detachTestDriverRunFlag},
			LogPath:   logPath,
		})
		if exit == ExitOK {
			t.Fatalf("exit = %d, want failure; stdout = %q", exit, stdout.String())
		}
		if strings.Contains(stdout.String(), "detached pid=") {
			t.Fatalf("failure reported a detached pid: %q", stdout.String())
		}
		if !strings.Contains(stderr.String(), "日志目录") {
			t.Fatalf("stderr = %q, want log directory diagnostic", stderr.String())
		}
		// Fail-closed ordering: validation is pure, so the failure must not
		// have created any path component, intermediate directories
		// included. lstat through a regular file reports ENOTDIR by POSIX
		// no matter what, so absence of creation is proven by listing:
		// the directory must still hold only the original regular file.
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != "blocker" || entries[0].IsDir() {
			t.Fatalf("fail-closed detach created path components: %v", entries)
		}
	})

	t.Run("log target is a directory", func(t *testing.T) {
		directory := t.TempDir()
		var stdout, stderr bytes.Buffer
		exit := executeDetach(&stdout, &stderr, detachRequest{
			RunID: "detach-fc-run", StateRoot: stateRoot,
			FinalArgs:  []string{detachTestDriverRunFlag},
			LogPath:    directory,
			LogErrPath: directory,
		})
		if exit == ExitOK {
			t.Fatalf("exit = %d, want failure; stdout = %q", exit, stdout.String())
		}
		if !strings.Contains(stderr.String(), "日志文件") {
			t.Fatalf("stderr = %q, want log file diagnostic", stderr.String())
		}
	})
}

func TestDetachFailsClosedWhenIntermediateStageFails(t *testing.T) {
	stateRoot := t.TempDir()

	t.Run("stage executable missing", func(t *testing.T) {
		previousSelfArgv := detachSelfArgv
		detachSelfArgv = func() ([]string, error) {
			return []string{filepath.Join(t.TempDir(), "missing-detach-binary")}, nil
		}
		defer func() { detachSelfArgv = previousSelfArgv }()
		var stdout, stderr bytes.Buffer
		exit := executeDetach(&stdout, &stderr, detachRequest{
			RunID: "detach-fc-run", StateRoot: stateRoot,
			FinalArgs: []string{"task", "run", "--run", "detach-fc-run"},
		})
		if exit == ExitOK {
			t.Fatalf("exit = %d, want failure; stdout = %q", exit, stdout.String())
		}
		if !strings.Contains(stderr.String(), "中间进程") {
			t.Fatalf("stderr = %q, want intermediate stage diagnostic", stderr.String())
		}
	})

	t.Run("stage exits without reporting", func(t *testing.T) {
		previousSelfArgv := detachSelfArgv
		previousExtraEnv := detachExtraEnv
		detachSelfArgv = func() ([]string, error) {
			return []string{os.Args[0], detachTestSilentStageRunFlag}, nil
		}
		detachExtraEnv = []string{detachTestStageEnv + "=1"}
		defer func() {
			detachSelfArgv = previousSelfArgv
			detachExtraEnv = previousExtraEnv
		}()
		var stdout, stderr bytes.Buffer
		exit := executeDetach(&stdout, &stderr, detachRequest{
			RunID: "detach-fc-run", StateRoot: stateRoot,
			FinalArgs: []string{"task", "run", "--run", "detach-fc-run"},
		})
		if exit == ExitOK {
			t.Fatalf("exit = %d, want failure; stdout = %q", exit, stdout.String())
		}
		if !strings.Contains(stderr.String(), "pid") {
			t.Fatalf("stderr = %q, want missing pid diagnostic", stderr.String())
		}
	})
}

// TestDetachEndToEndDefaultLog pins the full detach contract with the test
// process as the caller: new session and process group, stdio redirected to
// the default .marshal-style log, parent returning while the driver is
// still alive, and fail-closed reporting of the driver pid.
func TestDetachEndToEndDefaultLog(t *testing.T) {
	stateRoot := t.TempDir()
	reportPath := filepath.Join(t.TempDir(), "driver-report.json")
	withDetachTestHooks(t, reportPath)
	releaseDetachDriver(t, reportPath)

	var stdout, stderr bytes.Buffer
	exit := executeDetach(&stdout, &stderr, detachRequest{
		RunID: "detach-e2e-run", StateRoot: stateRoot,
		FinalArgs: []string{detachTestDriverRunFlag},
	})
	if exit != ExitOK {
		t.Fatalf("executeDetach exit = %d, stderr = %q", exit, stderr.String())
	}
	expectedLog := filepath.Join(stateRoot, "detached", "detach-e2e-run.log")
	if !strings.Contains(stdout.String(), "log="+expectedLog) || !strings.Contains(stdout.String(), "log-err="+expectedLog) {
		t.Fatalf("parent stdout = %q, want default log %s", stdout.String(), expectedLog)
	}
	pid := parseDetachPID(t, stdout.String())

	var report struct {
		PID int `json:"pid"`
	}
	detachWaitFor(t, 15*time.Second, "driver report", func() bool {
		data, err := os.ReadFile(reportPath)
		if err != nil {
			return false
		}
		return json.Unmarshal(data, &report) == nil
	})
	if report.PID != pid {
		t.Fatalf("parent reported pid %d, driver self-reports %d", pid, report.PID)
	}
	// The parent already returned while the driver is still alive: the
	// driver only exits on the release file, so liveness here proves the
	// parent did not block on the driver.
	if !detachProcessAlive(pid) {
		t.Fatal("driver must be alive after the parent returned")
	}

	// setsid forensic, portable to darwin: setsid inside the final driver
	// makes the driver the leader of a brand-new session and a brand-new
	// process group, so its pgid equals its own pid and the caller's group
	// can never reach it. darwin ps exposes no session id ("sess" prints a
	// sanitized zero), so the session fact is proven through this same
	// forensic here and re-asserted with ps -o sid= on linux.
	myPGID, _ := detachProcessIDs(t, os.Getpid())
	driverPGID, _ := detachProcessIDs(t, pid)
	if driverPGID != pid {
		t.Fatalf("driver pgid = %d, want own pid %d: setsid must make the driver lead a new session and process group", driverPGID, pid)
	}
	if driverPGID == myPGID {
		t.Fatalf("driver process group %d must differ from caller process group %d", driverPGID, myPGID)
	}
	if runtime.GOOS == "linux" {
		mySID := detachProcessSession(t, os.Getpid())
		driverSID := detachProcessSession(t, pid)
		if driverSID != pid {
			t.Fatalf("driver session %d, want own pid %d (setsid)", driverSID, pid)
		}
		if driverSID == mySID {
			t.Fatalf("driver session %d must differ from caller session %d", driverSID, mySID)
		}
	}
	detachWaitFor(t, 15*time.Second, "driver reparented after intermediate exit", func() bool {
		_, ppid := detachProcessIDs(t, pid)
		return ppid == 1
	})

	releaseDetachDriverNow(t, reportPath)
	detachWaitFor(t, 15*time.Second, "driver exit", func() bool { return !detachProcessAlive(pid) })

	data, err := os.ReadFile(expectedLog)
	if err != nil {
		t.Fatalf("default detach log missing: %v", err)
	}
	if !strings.Contains(string(data), detachStdoutMarker) || !strings.Contains(string(data), detachStderrMarker) {
		t.Fatalf("default detach log %s missing stdio markers: %q", expectedLog, data)
	}
}

// TestDetachEndToEndJSONMode pins that --json parents report the detach as
// a JSON object carrying the driver pid instead of the text line.
func TestDetachEndToEndJSONMode(t *testing.T) {
	stateRoot := t.TempDir()
	reportPath := filepath.Join(t.TempDir(), "driver-report.json")
	withDetachTestHooks(t, reportPath)
	releaseDetachDriver(t, reportPath)

	var stdout, stderr bytes.Buffer
	exit := executeDetach(&stdout, &stderr, detachRequest{
		RunID: "detach-json-run", StateRoot: stateRoot,
		FinalArgs: []string{detachTestDriverRunFlag},
		JSON:      true,
	})
	if exit != ExitOK {
		t.Fatalf("executeDetach exit = %d, stderr = %q", exit, stderr.String())
	}
	if strings.Contains(stdout.String(), "detached pid=") {
		t.Fatalf("JSON mode must not emit the text report: %q", stdout.String())
	}
	var report detachReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("parent stdout is not JSON: %v; stdout = %q", err, stdout.String())
	}
	if !report.Detached || report.PID <= 0 {
		t.Fatalf("detach report = %+v", report)
	}
	expectedLog := filepath.Join(stateRoot, "detached", "detach-json-run.log")
	if report.Log != expectedLog || report.LogErr != expectedLog {
		t.Fatalf("detach report logs = %+v, want %s", report, expectedLog)
	}
	var driverReport struct {
		PID int `json:"pid"`
	}
	detachWaitFor(t, 15*time.Second, "driver report", func() bool {
		data, err := os.ReadFile(reportPath)
		if err != nil {
			return false
		}
		return json.Unmarshal(data, &driverReport) == nil
	})
	if driverReport.PID != report.PID {
		t.Fatalf("parent reported pid %d, driver self-reports %d", report.PID, driverReport.PID)
	}
	releaseDetachDriverNow(t, reportPath)
	detachWaitFor(t, 15*time.Second, "driver exit", func() bool { return !detachProcessAlive(report.PID) })
}

// TestDetachSurvivesCallerProcessGroupKill reproduces Issue #56: a caller
// session in its own process group starts a detached driver, then the whole
// caller group is killed (kill -- -PGID); the driver must survive in its
// own session and process group with stdio in the explicit --log/--log-err
// files.
func TestDetachSurvivesCallerProcessGroupKill(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "driver-report.json")

	caller := exec.Command(os.Args[0], detachTestCallerRunFlag)
	caller.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	caller.Env = append(append([]string{}, os.Environ()...), detachTestCallerDirEnv+"="+directory)
	if err := caller.Start(); err != nil {
		t.Fatal(err)
	}
	callerPID := caller.Process.Pid
	callerDone := make(chan struct{})
	go func() {
		_ = caller.Wait()
		close(callerDone)
	}()
	t.Cleanup(func() {
		if detachProcessAlive(callerPID) {
			_ = syscall.Kill(-callerPID, syscall.SIGKILL)
		}
		<-callerDone
	})
	releaseDetachDriver(t, reportPath)

	statePath := filepath.Join(directory, "caller-state.json")
	var state struct {
		Exit   int    `json:"exit"`
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
	}
	detachWaitFor(t, 20*time.Second, "caller detach state", func() bool {
		data, err := os.ReadFile(statePath)
		if err != nil {
			return false
		}
		return json.Unmarshal(data, &state) == nil
	})
	if state.Exit != ExitOK {
		t.Fatalf("caller executeDetach exit = %d, stderr = %q", state.Exit, state.Stderr)
	}
	pid := parseDetachPID(t, state.Stdout)
	// The caller wrote its state immediately after executeDetach returned;
	// the driver (released only at cleanup) must still be alive, proving
	// the parent returned without waiting on the driver.
	if !detachProcessAlive(pid) {
		t.Fatal("driver must be alive when the detached parent returns")
	}

	callerPGID, _ := detachProcessIDs(t, callerPID)
	if callerPGID != callerPID {
		t.Fatalf("caller pgid = %d, want own pid %d (Setpgid fixture broken)", callerPGID, callerPID)
	}
	// setsid forensic (darwin+linux): the final driver performed setsid, so
	// it leads its own session and process group — pgid equals its own pid
	// and differs from every caller-side group. The process-group kill
	// below then proves the isolation operationally.
	driverPGID, _ := detachProcessIDs(t, pid)
	if driverPGID != pid {
		t.Fatalf("driver pgid = %d, want own pid %d: setsid must make the driver lead a new session and process group", driverPGID, pid)
	}
	if driverPGID == callerPGID {
		t.Fatalf("driver process group %d must differ from caller process group %d", driverPGID, callerPGID)
	}
	myPGID, _ := detachProcessIDs(t, os.Getpid())
	if driverPGID == myPGID {
		t.Fatalf("driver process group %d must differ from the test process group %d", driverPGID, myPGID)
	}
	if runtime.GOOS == "linux" {
		callerSID := detachProcessSession(t, callerPID)
		driverSID := detachProcessSession(t, pid)
		if driverSID != pid {
			t.Fatalf("driver session %d, want own pid %d (setsid)", driverSID, pid)
		}
		if driverSID == callerSID {
			t.Fatalf("driver session %d must differ from caller session %d", driverSID, callerSID)
		}
		mySID := detachProcessSession(t, os.Getpid())
		if driverSID == mySID {
			t.Fatalf("driver session %d must differ from the test process session %d", driverSID, mySID)
		}
	}

	// Caller-side process group cleanup: kill -- -PGID.
	if err := syscall.Kill(-callerPID, syscall.SIGTERM); err != nil {
		t.Fatalf("kill caller process group: %v", err)
	}
	select {
	case <-callerDone:
	case <-time.After(15 * time.Second):
		t.Fatal("caller did not exit after process group kill")
	}
	if detachProcessAlive(callerPID) {
		t.Fatal("caller still alive after process group kill and reap")
	}
	time.Sleep(200 * time.Millisecond)
	if !detachProcessAlive(pid) {
		t.Fatal("caller process group kill reached the detached driver")
	}
	detachWaitFor(t, 15*time.Second, "driver reparented after intermediate exit", func() bool {
		_, ppid := detachProcessIDs(t, pid)
		return ppid == 1
	})

	releaseDetachDriverNow(t, reportPath)
	detachWaitFor(t, 20*time.Second, "driver exit", func() bool { return !detachProcessAlive(pid) })

	reportData, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("driver report missing: %v", err)
	}
	var report struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(reportData, &report); err != nil || report.PID != pid {
		t.Fatalf("driver report = %q, want pid %d", reportData, pid)
	}
	logData, err := os.ReadFile(filepath.Join(directory, "driver.log"))
	if err != nil || !strings.Contains(string(logData), detachStdoutMarker) {
		t.Fatalf("--log file missing stdout marker: %q, err = %v", logData, err)
	}
	errData, err := os.ReadFile(filepath.Join(directory, "driver.err.log"))
	if err != nil || !strings.Contains(string(errData), detachStderrMarker) {
		t.Fatalf("--log-err file missing stderr marker: %q, err = %v", errData, err)
	}
}

// releaseDetachDriver registers the driver release file as a cleanup so the
// detached driver never outlives the test even on failed assertions.
func releaseDetachDriver(t *testing.T, reportPath string) {
	t.Helper()
	t.Cleanup(func() { releaseDetachDriverNow(t, reportPath) })
}

func releaseDetachDriverNow(t *testing.T, reportPath string) {
	t.Helper()
	_ = os.WriteFile(reportPath+".release", []byte("release"), 0o600)
}

func parseDetachPID(t *testing.T, output string) int {
	t.Helper()
	index := strings.Index(output, "pid=")
	if index < 0 {
		t.Fatalf("no pid in detach output: %q", output)
	}
	rest := output[index+len("pid="):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		t.Fatalf("no digits after pid= in detach output: %q", output)
	}
	pid, err := strconv.Atoi(rest[:end])
	if err != nil {
		t.Fatalf("parse pid from %q: %v", output, err)
	}
	return pid
}

func detachProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// detachProcessIDs reads pgid and ppid for pid with ps semantics available
// on both darwin and linux (ps -o pgid=,ppid=).
func detachProcessIDs(t *testing.T, pid int) (int, int) {
	t.Helper()
	output, err := exec.Command("ps", "-o", "pgid=,ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps failed for pid %d: %v", pid, err)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 {
		t.Fatalf("unexpected ps output for pid %d: %q", pid, output)
	}
	pgid, pgidErr := strconv.Atoi(fields[0])
	ppid, ppidErr := strconv.Atoi(fields[1])
	if pgidErr != nil || ppidErr != nil {
		t.Fatalf("parse ps fields %q for pid %d (pgid: %v, ppid: %v)", output, pid, pgidErr, ppidErr)
	}
	return pgid, ppid
}

// detachProcessSession reads the session id of pid where ps exposes one.
// Linux procps has the sid keyword; darwin ps has no session-id keyword at
// all ("sess" prints a sanitized zero for every process), so callers on
// darwin prove the session fact through the setsid forensic instead: a
// process that performed setsid leads a brand-new session and process
// group, hence its pgid equals its own pid.
func detachProcessSession(t *testing.T, pid int) int {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Fatalf("ps exposes no session id on %s; use the pgid==pid setsid forensic", runtime.GOOS)
	}
	output, err := exec.Command("ps", "-o", "sid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps sid failed for pid %d: %v", pid, err)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 1 {
		t.Fatalf("unexpected ps sid output for pid %d: %q", pid, output)
	}
	sid, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatalf("parse ps sid %q for pid %d: %v", fields[0], pid, err)
	}
	return sid
}

func detachWaitFor(t *testing.T, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
