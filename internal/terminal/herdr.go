package terminal

// POC（实验分支 exp/herdr-terminal-backend，不进主干）。
//
// HerdrBackend 是 ADR 0008/0009 可插拔 TerminalSession 边界的第二个真实后端
// 原型：把 herdr（终端运行时，herdr.dev，本机 0.8.0）作为 Marshal 的 PTY 后端。
// 命令面以 herdr 0.8.0 真实 CLI 校准：
//
//	workspace create --cwd --label   -> result.workspace.workspace_id + result.root_pane.pane_id
//	pane run <pane> <cmd>            -> 在 pane 内执行密封 launcher
//	pane read <pane>                 -> 读屏
//	pane send-text / send-keys       -> 注入文本/按键
//	workspace close <id>             -> 关闭
//	agent list / workspace get       -> agent_status（blocked/working/idle）辅助信号
//
// 信任边界与 cmux 一致、未放宽：密封 LaunchEnvelope 一次性、owner-only；环境值不进
// 可见 argv；ExpectedExecutableDigest 漂移即拒绝；屏幕文本不替代 WorkerResult/
// Git Snapshot/Verification/Review；herdr agent_status 仅作 CompletionGate 辅助
// 信号（见 docs/research/herdr-adr-supplement.md），不具权威性。

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/launcher"
	"github.com/chiga0/marshal-harness/internal/port"
)

const HerdrBackendID = "herdr-pty"

// HerdrBackend implements port.TerminalSessionBackend against the herdr CLI.
type HerdrBackend struct {
	path       string
	sum        string
	runner     commandRunner
	processes  processController
	startDelay time.Duration
	startLimit time.Duration
}

// NewHerdrBackend pins the herdr executable by realpath + SHA-256.
func NewHerdrBackend(path string) (*HerdrBackend, error) {
	if !filepath.IsAbs(path) || path != filepath.Clean(path) {
		return nil, ErrInvalidRequest
	}
	sum, err := executableDigest(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	backend := &HerdrBackend{path: path, sum: sum, processes: defaultProcessController(), startDelay: 50 * time.Millisecond, startLimit: defaultStartTimeout}
	backend.runner = &herdrRunner{path: path, sum: sum, limit: defaultCommandLimit}
	return backend, nil
}

func (b *HerdrBackend) ID() string { return HerdrBackendID }

func (b *HerdrBackend) verify() error {
	sum, err := executableDigest(b.path)
	if err != nil || sum != b.sum {
		return ErrUnavailable
	}
	return nil
}

// Probe checks herdr binary identity and that its control surface answers.
func (b *HerdrBackend) Probe(ctx context.Context) (port.TerminalProbeResult, error) {
	if err := b.verify(); err != nil {
		return port.TerminalProbeResult{BackendID: b.ID(), Diagnostic: "binary-replaced"}, err
	}
	output, err := b.runner.Run(ctx, "workspace", "list")
	if err != nil || !strings.Contains(output, "workspace_list") {
		return port.TerminalProbeResult{BackendID: b.ID(), Diagnostic: "herdr-control-unavailable"}, ErrUnavailable
	}
	caps := []port.TerminalCapability{
		port.TerminalSessionCreate, port.TerminalPromptSend, port.TerminalScreenRead,
		port.TerminalInterruptStep, port.TerminalTerminate,
	}
	if b.processes.Supported() {
		caps = append(caps, port.TerminalPauseResume)
	}
	return port.TerminalProbeResult{BackendID: b.ID(), Available: true, Capabilities: caps}, nil
}

// AuxiliaryStatus returns herdr's attention signal for a workspace
// (blocked/working/idle/unknown). It is advisory only (ADR supplement).
func (b *HerdrBackend) AuxiliaryStatus(ctx context.Context, workspace string) (string, error) {
	output, err := b.runner.Run(ctx, "workspace", "get", workspace)
	if err != nil {
		return "", ErrUnavailable
	}
	var parsed struct {
		Result struct {
			Workspace struct {
				AgentStatus string `json:"agent_status"`
			} `json:"workspace"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(output), &parsed) != nil {
		return "", ErrUnavailable
	}
	return parsed.Result.Workspace.AgentStatus, nil
}

type herdrCreateResult struct {
	Workspace string
	Pane      string
}

func (b *HerdrBackend) createWorkspace(ctx context.Context, request port.TerminalStartRequest) (herdrCreateResult, error) {
	output, err := b.runner.Run(ctx, "workspace", "create", "--cwd", request.WorkingDirectory, "--label", request.Title)
	if err != nil {
		return herdrCreateResult{}, ErrUnavailable
	}
	var parsed struct {
		Result struct {
			Workspace struct {
				WorkspaceID string `json:"workspace_id"`
			} `json:"workspace"`
			RootPane struct {
				PaneID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(output), &parsed) != nil || parsed.Result.Workspace.WorkspaceID == "" || parsed.Result.RootPane.PaneID == "" {
		return herdrCreateResult{}, ErrUnavailable
	}
	return herdrCreateResult{Workspace: parsed.Result.Workspace.WorkspaceID, Pane: parsed.Result.RootPane.PaneID}, nil
}

// Start creates a herdr workspace, runs the sealed launcher in its root pane,
// then delivers the frozen initial prompt. Mirrors the cmux start handshake.
func (b *HerdrBackend) Start(ctx context.Context, request port.TerminalStartRequest) (port.TerminalSession, error) {
	if err := validateStartRequest(request); err != nil {
		return nil, err
	}
	probe, err := b.Probe(ctx)
	if err != nil || !probe.Available {
		return nil, ErrUnavailable
	}
	executableSum, err := executableDigest(request.Executable)
	if err != nil || executableSum != request.ExpectedExecutableDigest {
		return nil, ErrInvalidRequest
	}
	attemptDirectory := filepath.Join(request.StateRoot, "runs", request.RunID, "attempts", request.AttemptID)
	pidPath, err := preparePIDHandshake(attemptDirectory)
	if err != nil {
		return nil, err
	}
	defer os.Remove(pidPath)
	launcherSum, err := executableDigest(request.LauncherExecutable)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	reference, err := launcher.Seal(request.StateRoot, launcher.SealRequest{
		RunID: request.RunID, AttemptID: request.AttemptID, Executable: request.Executable,
		ExpectedExecutableDigest: request.ExpectedExecutableDigest,
		Arguments:                request.Arguments, WorkingDirectory: request.WorkingDirectory,
		Environment: request.Environment, Now: request.Now, ExpiresAt: request.ExpiresAt,
	})
	if err != nil {
		return nil, ErrInvalidRequest
	}
	envelopePending := true
	defer func() {
		if envelopePending {
			_ = os.Remove(reference.Path)
		}
	}()
	created, err := b.createWorkspace(ctx, request)
	if err != nil {
		return nil, err
	}
	closeWS := func() { _, _ = b.runner.Run(context.Background(), "workspace", "close", created.Workspace) }
	command, err := shellCommandWithPID([]string{request.LauncherExecutable, "__launch", reference.Path}, pidPath)
	if err != nil {
		closeWS()
		return nil, err
	}
	if _, err := b.runner.Run(ctx, "pane", "send-text", created.Pane, command); err != nil {
		closeWS()
		return nil, err
	}
	if _, err := b.runner.Run(ctx, "pane", "send-keys", created.Pane, "enter"); err != nil {
		closeWS()
		return nil, err
	}
	if err := b.waitCommandVisible(ctx, created.Pane); err != nil {
		closeWS()
		return nil, err
	}
	pid, err := b.waitPID(ctx, pidPath)
	if err != nil {
		closeWS()
		return nil, err
	}
	if err := b.waitEnvelopeConsumed(ctx, reference.Path); err != nil {
		closeWS()
		return nil, err
	}
	envelopePending = false
	pgid := 0
	if b.processes.Supported() {
		pgid, err = b.processes.GroupID(pid)
		if err != nil {
			closeWS()
			return nil, err
		}
	}
	recordPath := filepath.Join(attemptDirectory, "terminal-session.json")
	session := &herdrSession{
		backend: b, workspace: created.Workspace, pane: created.Pane, pid: pid, pgid: pgid, state: StateRunning,
		capabilities: probe.Capabilities, recordPath: recordPath,
		record: sessionRecord{BackendID: b.ID(), WorkspaceRef: created.Workspace, RunID: request.RunID, AttemptID: request.AttemptID,
			PID: pid, ProcessGroupID: pgid, Executable: request.Executable, ExecutableDigest: executableSum,
			LauncherExecutable: request.LauncherExecutable, LauncherExecutableDigest: launcherSum, LaunchEnvelopeDigest: reference.Digest,
			State: StateRunning, CreatedAt: request.Now.UTC(), UpdatedAt: request.Now.UTC()},
	}
	if err := session.persist(); err != nil {
		_ = b.processes.Terminate(context.Background(), pid, time.Second)
		closeWS()
		return nil, err
	}
	if err := session.Send(ctx, InputSourceFrozenPrompt, request.InitialPrompt, request.Now); err != nil {
		_ = session.Terminate(context.Background(), time.Second)
		closeWS()
		return nil, err
	}
	return session, nil
}

func (b *HerdrBackend) waitCommandVisible(ctx context.Context, pane string) error {
	deadline := time.NewTimer(b.startLimit)
	defer deadline.Stop()
	ticker := time.NewTicker(b.startDelay)
	defer ticker.Stop()
	for {
		output, err := b.runner.Run(ctx, "pane", "read", pane)
		if err == nil && strings.Contains(output, "MARSHAL_LAUNCH_READY") {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return ErrUnavailable
		case <-ticker.C:
		}
	}
}

func (b *HerdrBackend) waitEnvelopeConsumed(ctx context.Context, path string) error {
	deadline := time.NewTimer(b.startLimit)
	defer deadline.Stop()
	ticker := time.NewTicker(b.startDelay)
	defer ticker.Stop()
	for {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return ErrUnavailable
		case <-ticker.C:
		}
	}
}

func (b *HerdrBackend) waitPID(ctx context.Context, path string) (int, error) {
	deadline := time.NewTimer(b.startLimit)
	defer deadline.Stop()
	ticker := time.NewTicker(b.startDelay)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return parsePIDFile(path, data)
		}
		if err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-deadline.C:
			return 0, ErrUnavailable
		case <-ticker.C:
		}
	}
}

// herdrSession implements port.TerminalSession on a herdr workspace/pane.
type herdrSession struct {
	backend      *HerdrBackend
	workspace    string
	pane         string
	pid          int
	pgid         int
	capabilities []port.TerminalCapability
	recordPath   string
	record       sessionRecord
	inputCount   uint64
	state        port.TerminalState
	pausedGroups []int
}

func (s *herdrSession) ID() string {
	return "herdr:" + s.record.RunID + ":" + s.record.AttemptID + ":" + s.workspace
}
func (s *herdrSession) Identity() port.TerminalSessionIdentity {
	return port.TerminalSessionIdentity{RunID: s.record.RunID, AttemptID: s.record.AttemptID}
}
func (s *herdrSession) State() port.TerminalState               { return s.state }
func (s *herdrSession) Capabilities() []port.TerminalCapability { return s.capabilities }

func (s *herdrSession) Send(ctx context.Context, source port.TerminalInputSource, text string, now time.Time) error {
	if strings.TrimSpace(text) == "" || len(text) > 1<<20 || now.IsZero() ||
		(source != InputSourceFrozenPrompt && source != InputSourceLeadSteering && source != InputSourceHumanSteering) {
		return ErrInvalidRequest
	}
	if s.state != StateRunning {
		return ErrSessionState
	}
	s.inputCount++
	if err := s.appendInput(inputRecord{Sequence: s.inputCount, Source: source, Digest: digestText(text), Phase: "planned", SentAt: now.UTC()}); err != nil {
		return err
	}
	if _, err := s.backend.runner.Run(ctx, "pane", "send-text", s.pane, text); err != nil {
		return ErrUnavailable
	}
	if _, err := s.backend.runner.Run(ctx, "pane", "send-keys", s.pane, "enter"); err != nil {
		return ErrUnavailable
	}
	return s.appendInput(inputRecord{Sequence: s.inputCount, Source: source, Digest: digestText(text), Phase: "delivered", SentAt: now.UTC()})
}

func (s *herdrSession) ReadScreen(ctx context.Context, lines int) (string, error) {
	if lines < 1 || lines > 10000 {
		return "", ErrInvalidRequest
	}
	return s.backend.runner.Run(ctx, "pane", "read", s.pane)
}

func (s *herdrSession) InterruptStep(ctx context.Context) error {
	if s.state != StateRunning {
		return ErrSessionState
	}
	if _, err := s.backend.runner.Run(ctx, "pane", "send-keys", s.pane, "escape"); err != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *herdrSession) Pause(ctx context.Context) error {
	if s.state != StateRunning {
		return ErrSessionState
	}
	if !s.backend.processes.Supported() {
		return ErrUnsupported
	}
	groups, err := s.backend.processes.Pause(s.pid)
	if err != nil {
		return err
	}
	s.pausedGroups = groups
	s.state = StatePaused
	return s.persist()
}

func (s *herdrSession) Resume(ctx context.Context) error {
	if s.state != StatePaused {
		return ErrSessionState
	}
	if !s.backend.processes.Supported() {
		return ErrUnsupported
	}
	if err := s.backend.processes.Resume(s.pid, s.pausedGroups); err != nil {
		return err
	}
	s.pausedGroups = nil
	s.state = StateRunning
	return s.persist()
}

func (s *herdrSession) Terminate(ctx context.Context, grace time.Duration) error {
	if s.state == StateTerminated {
		return nil
	}
	if !s.backend.processes.Supported() {
		return ErrUnsupported
	}
	if err := s.backend.processes.Terminate(ctx, s.pid, grace); err != nil {
		return err
	}
	s.state = StateTerminated
	return s.persist()
}

func (s *herdrSession) persist() error {
	data, err := json.MarshalIndent(s.record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWrite(s.recordPath, data)
}

func (s *herdrSession) appendInput(record inputRecord) error {
	return appendInputJournal(filepath.Dir(s.recordPath), record)
}

// herdrRunner shells to the pinned herdr binary with a bounded output buffer.
type herdrRunner struct {
	path  string
	sum   string
	limit int
}

func (r *herdrRunner) Run(ctx context.Context, arguments ...string) (string, error) {
	sum, err := executableDigest(r.path)
	if err != nil || sum != r.sum {
		return "", ErrUnavailable
	}
	return runCommand(ctx, r.path, r.limit, arguments...)
}

// runCommand executes a command with bounded stdout, shared with cmux runner.
func runCommand(ctx context.Context, path string, limit int, arguments ...string) (string, error) {
	return execBounded(ctx, path, limit, arguments...)
}

// execBounded runs path with arguments, capturing bounded stdout. It mirrors
// the cmux execCommandRunner behavior without depending on a backend instance.
func execBounded(ctx context.Context, path string, limit int, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, path, arguments...)
	command.Env = terminalEnvironment()
	stdout, stderr := &boundedBuffer{limit: limit}, &boundedBuffer{limit: 4 << 10}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil || stdout.overflow {
		return "", ErrUnavailable
	}
	return strings.TrimSpace(stdout.String()), nil
}

// parsePIDFile validates the PID handshake file written by the sealed launcher.
func parsePIDFile(path string, data []byte) (int, error) {
	info, statErr := os.Lstat(path)
	if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || len(data) > 32 {
		return 0, ErrAmbiguousProcess
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if parseErr != nil || pid <= 1 {
		return 0, ErrAmbiguousProcess
	}
	return pid, nil
}

func digestText(text string) string { return canonical.DigestBytes([]byte(text)) }

// appendInputJournal appends one provenance record to terminal-inputs.jsonl.
func appendInputJournal(directory string, record inputRecord) error {
	if err := secureDirectory(directory); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, "terminal-inputs.jsonl")
	if info, statErr := os.Lstat(path); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size()+int64(len(data)+1) > inputJournalLimit {
			return ErrInvalidRequest
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return file.Sync()
}
