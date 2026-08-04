package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRunner struct {
	mu             sync.Mutex
	commands       [][]string
	failSend       bool
	retainEnvelope bool
}

func (r *fakeRunner) Run(_ context.Context, arguments ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, slices.Clone(arguments))
	if len(arguments) == 0 {
		return "", ErrUnavailable
	}
	switch arguments[0] {
	case "capabilities":
		return `{"methods":["workspace.create","surface.send_text","surface.send_key","surface.read_text","system.top","workspace.close"]}`, nil
	case "workspace":
		if len(arguments) > 1 && arguments[1] == "create" {
			for index, argument := range arguments {
				if argument == "--command" && index+1 < len(arguments) {
					match := regexp.MustCompile(`> '([^']+)' && fg`).FindStringSubmatch(arguments[index+1])
					if match != nil {
						_ = os.WriteFile(match[1], []byte("4242\n"), 0o600)
					}
					envelope := regexp.MustCompile(`'__launch' '([^']+\.json)'`).FindStringSubmatch(arguments[index+1])
					if envelope != nil && !r.retainEnvelope {
						_ = os.Remove(envelope[1])
					}
				}
			}
			return "OK workspace:7", nil
		}
		return "OK", nil
	case "send":
		if r.failSend {
			return "", ErrUnavailable
		}
		return "OK", nil
	case "send-key":
		return "OK", nil
	case "read-screen":
		return "MARSHAL_LAUNCH_READY\nnative TUI output", nil
	default:
		return "", ErrUnavailable
	}
}

type fakeProcesses struct {
	paused, resumed, terminated bool
}

type unsupportedFakeProcesses struct{ fakeProcesses }

func (*unsupportedFakeProcesses) Supported() bool { return false }

func (p *fakeProcesses) Supported() bool { return true }
func (p *fakeProcesses) GroupID(pid int) (int, error) {
	if pid != 4242 {
		return 0, ErrAmbiguousProcess
	}
	return 4242, nil
}
func (p *fakeProcesses) Pause(pid int) ([]int, error) {
	p.paused = pid == 4242
	return []int{4242}, nil
}
func (p *fakeProcesses) Resume(pid int, groups []int) error {
	p.resumed = pid == 4242 && slices.Equal(groups, []int{4242})
	return nil
}
func (p *fakeProcesses) Terminate(_ context.Context, pid int, _ time.Duration) error {
	p.terminated = pid == 4242
	return nil
}

func TestCMUXSessionLifecycleAndProvenance(t *testing.T) {
	t.Parallel()
	backend, runner, processes, executable := newFakeCMUX(t)
	launcherExecutable := filepath.Join(filepath.Dir(executable), "marshal")
	if err := os.WriteFile(launcherExecutable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	session, err := backend.Start(context.Background(), StartRequest{
		StateRoot: root, RunID: "run-01", AttemptID: "attempt-01", WorkingDirectory: t.TempDir(),
		LauncherExecutable: launcherExecutable, Executable: executable, ExpectedExecutableDigest: frozenDigest(t, executable), Arguments: []string{"--model", "safe value"}, Environment: []string{"SAFE=value"}, Title: "Marshal Run", Description: "Native PTY",
		InitialPrompt: "frozen prompt with $() and `backticks`", Now: time.Unix(10, 0).UTC(), ExpiresAt: time.Unix(70, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.ID() != "cmux:run-01:attempt-01:workspace:7" || session.State() != StateRunning {
		t.Fatalf("session = %s / %s", session.ID(), session.State())
	}
	capabilities := session.Capabilities()
	if !slices.Contains(capabilities, CapabilityPauseResume) || slices.Contains(capabilities, CapabilityInputProvenance) || slices.Contains(capabilities, CapabilitySessionResume) {
		t.Fatalf("capabilities = %v", capabilities)
	}
	screen, err := session.ReadScreen(context.Background(), 100)
	if err != nil || !strings.Contains(screen, "native TUI output") {
		t.Fatalf("ReadScreen() = %q, %v", screen, err)
	}
	if err := session.InterruptStep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.Pause(context.Background()); err != nil || session.State() != StatePaused || !processes.paused {
		t.Fatalf("Pause() state=%s process=%+v error=%v", session.State(), processes, err)
	}
	if err := session.Send(context.Background(), InputSourceLeadSteering, "while paused", time.Now()); !errors.Is(err, ErrSessionState) {
		t.Fatalf("Send() while paused = %v", err)
	}
	if err := session.Resume(context.Background()); err != nil || session.State() != StateRunning || !processes.resumed {
		t.Fatalf("Resume() state=%s process=%+v error=%v", session.State(), processes, err)
	}
	if err := session.Send(context.Background(), InputSourceLeadSteering, "bounded correction", time.Unix(20, 0)); err != nil {
		t.Fatal(err)
	}
	if err := session.Terminate(context.Background(), time.Second); err != nil || session.State() != StateTerminated || !processes.terminated {
		t.Fatalf("Terminate() state=%s process=%+v error=%v", session.State(), processes, err)
	}
	if err := session.Terminate(context.Background(), time.Second); err != nil {
		t.Fatalf("idempotent Terminate() = %v", err)
	}

	recordData, err := os.ReadFile(filepath.Join(root, "runs", "run-01", "attempts", "attempt-01", "terminal-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record sessionRecord
	if json.Unmarshal(recordData, &record) != nil || record.State != StateTerminated || record.PID != 4242 || record.ProcessGroupID != 4242 || record.LauncherExecutable != launcherExecutable || record.LaunchEnvelopeDigest == "" {
		t.Fatalf("session record = %+v", record)
	}
	inputData, err := os.ReadFile(filepath.Join(root, "runs", "run-01", "attempts", "attempt-01", "terminal-inputs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(inputData), "frozen prompt") || strings.Contains(string(inputData), "bounded correction") {
		t.Fatal("input provenance log leaked prompt text")
	}
	lines := strings.Split(strings.TrimSpace(string(inputData)), "\n")
	if len(lines) != 4 {
		t.Fatalf("input provenance records = %d, want planned+delivered for two inputs", len(lines))
	}
	if len(runner.commands) == 0 {
		t.Fatal("cmux runner was not invoked")
	}
	visible := ""
	for _, command := range runner.commands {
		if len(command) > 1 && command[0] == "workspace" && command[1] == "create" {
			visible = strings.Join(command, " ")
		}
	}
	if visible == "" || !strings.Contains(visible, launcherExecutable) || strings.Contains(visible, executable) || strings.Contains(visible, "safe value") || strings.Contains(visible, "SAFE=value") {
		t.Fatalf("visible cmux launch leaked Worker details: %s", visible)
	}
}

func TestCMUXSendFailureLeavesPlannedProvenance(t *testing.T) {
	t.Parallel()
	backend, runner, _, executable := newFakeCMUX(t)
	root := t.TempDir()
	session, err := backend.Start(context.Background(), StartRequest{StateRoot: root, RunID: "run-01", AttemptID: "attempt-01", WorkingDirectory: t.TempDir(), LauncherExecutable: executable, Executable: executable, ExpectedExecutableDigest: frozenDigest(t, executable), Title: "Run", InitialPrompt: "initial", Now: time.Unix(1, 0), ExpiresAt: time.Unix(61, 0)})
	if err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	runner.failSend = true
	runner.mu.Unlock()
	if err := session.Send(context.Background(), InputSourceLeadSteering, "not delivered", time.Unix(2, 0)); err == nil {
		t.Fatal("Send() unexpectedly succeeded")
	}
	data, err := os.ReadFile(filepath.Join(root, "runs", "run-01", "attempts", "attempt-01", "terminal-inputs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 || !strings.Contains(lines[2], `"phase":"planned"`) {
		t.Fatalf("provenance after failed send = %s", data)
	}
}

func TestCMUXProbeAndParsersFailClosed(t *testing.T) {
	t.Parallel()
	backend, _, _, _ := newFakeCMUX(t)
	probe, err := backend.Probe(context.Background())
	if err != nil || !probe.Available || slices.Contains(probe.Capabilities, CapabilityInputProvenance) {
		t.Fatalf("Probe() = %+v, %v", probe, err)
	}
	command, err := shellCommand([]string{"/tmp/agent", "a b", "x'y", "$(touch /tmp/no)"})
	if err != nil || command != `exec '/tmp/agent' 'a b' 'x'"'"'y' '$(touch /tmp/no)'` {
		t.Fatalf("shellCommand() = %q, %v", command, err)
	}
}

func TestCMUXProbeDoesNotClaimUnsupportedProcessControl(t *testing.T) {
	t.Parallel()
	backend, _, _, _ := newFakeCMUX(t)
	backend.processes = &unsupportedFakeProcesses{}
	probe, err := backend.Probe(context.Background())
	if err != nil || !probe.Available {
		t.Fatalf("Probe() = %+v, %v", probe, err)
	}
	if slices.Contains(probe.Capabilities, CapabilityPauseResume) || slices.Contains(probe.Capabilities, CapabilityTerminate) {
		t.Fatalf("unsupported process capabilities claimed: %v", probe.Capabilities)
	}
}

func TestCMUXRequiresLauncherEnvelopeConsumption(t *testing.T) {
	backend, runner, _, executable := newFakeCMUX(t)
	runner.retainEnvelope = true
	backend.startLimit = 10 * time.Millisecond
	root := t.TempDir()
	_, err := backend.Start(context.Background(), StartRequest{
		StateRoot: root, RunID: "run-01", AttemptID: "attempt-01", WorkingDirectory: t.TempDir(),
		LauncherExecutable: executable, Executable: executable, ExpectedExecutableDigest: frozenDigest(t, executable), Title: "Run", InitialPrompt: "initial",
		Now: time.Unix(1, 0), ExpiresAt: time.Unix(61, 0),
	})
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "consume envelope") {
		t.Fatalf("Start() = %v", err)
	}
	matches, globErr := filepath.Glob(filepath.Join(root, "runs", "run-01", "attempts", "attempt-01", "runtime", ".launch-*.json"))
	if globErr != nil || len(matches) != 0 {
		t.Fatalf("failed launch retained envelopes: %v, %v", matches, globErr)
	}
}

func TestCMUXRejectsExecutableIdentityDriftBeforeWorkspaceCreation(t *testing.T) {
	backend, runner, _, executable := newFakeCMUX(t)
	root := t.TempDir()
	_, err := backend.Start(context.Background(), StartRequest{
		StateRoot: root, RunID: "run-01", AttemptID: "attempt-01", WorkingDirectory: t.TempDir(),
		LauncherExecutable: executable, Executable: executable,
		ExpectedExecutableDigest: "sha256:" + strings.Repeat("0", 64),
		Title:                    "Run", InitialPrompt: "initial", Now: time.Unix(1, 0), ExpiresAt: time.Unix(61, 0),
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Start() with drifted executable = %v", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, call := range runner.commands {
		if len(call) >= 2 && call[0] == "workspace" && call[1] == "create" {
			t.Fatalf("workspace created after identity drift: %v", runner.commands)
		}
	}
}

func TestCMUXInputJournalRejectsSymlink(t *testing.T) {
	t.Parallel()
	backend, _, _, executable := newFakeCMUX(t)
	root := t.TempDir()
	directory := filepath.Join(root, "runs", "run-01", "attempts", "attempt-01")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "terminal-inputs.jsonl")); err != nil {
		t.Fatal(err)
	}
	_, err := backend.Start(context.Background(), StartRequest{StateRoot: root, RunID: "run-01", AttemptID: "attempt-01", WorkingDirectory: t.TempDir(), LauncherExecutable: executable, Executable: executable, ExpectedExecutableDigest: frozenDigest(t, executable), Title: "Run", InitialPrompt: "initial", Now: time.Unix(1, 0), ExpiresAt: time.Unix(61, 0)})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Start() with symlinked journal = %v", err)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || len(data) != 0 {
		t.Fatalf("outside target changed: %q, %v", data, readErr)
	}
}

func newFakeCMUX(t *testing.T) (*CMUXBackend, *fakeRunner, *fakeProcesses, string) {
	t.Helper()
	directory := t.TempDir()
	cmuxPath := filepath.Join(directory, "cmux")
	executable := filepath.Join(directory, "agent")
	for _, path := range []string{cmuxPath, executable} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	backend, err := NewCMUXBackend(cmuxPath)
	if err != nil {
		t.Fatal(err)
	}
	runner, processes := &fakeRunner{}, &fakeProcesses{}
	backend.runner, backend.processes, backend.startDelay, backend.startLimit = runner, processes, time.Millisecond, time.Second
	return backend, runner, processes, executable
}

func frozenDigest(t *testing.T, path string) string {
	t.Helper()
	digest, err := executableDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
