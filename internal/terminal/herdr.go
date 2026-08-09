package terminal

// POC（实验分支 exp/herdr-terminal-backend，不进主干）。
//
// HerdrBackend 是 ADR 0008/0009 可插拔 TerminalSession 边界的第二个真实后端
// 原型：把 herdr（终端运行时，herdr.dev）作为 Marshal 的 PTY 后端。它与 cmux
// 后端同构：Probe 探测 herdr CLI 与 socket 可达性；Start 通过密封
// LaunchEnvelope 在 herdr workspace/pane 内执行可信 launcher；Send/ReadScreen/
// Interrupt 走 herdr CLI；Pause/Resume/Terminate 走进程组控制器。
//
// 设计要点（详见 docs/research/herdr-backend-poc.md）：
//   - herdr 只提供"身体/神经"（终端、注意力、喊话通信），不提供任务/证据/治理；
//     Marshal 仍是状态与策略唯一权威，herdr 的 blocked/working 信号仅作辅助。
//   - 与 cmux 一致：屏幕文本不替代 WorkerResult/Git Snapshot/Verification/Review。
//   - 不继承 herdr/terminal ambient environment；环境值不进可见 argv。
//
// 该文件是 POC：命令面以 herdr CLI 为准（workspace/pane/agent 子命令），
// 测试在无 MARSHAL_HERDR_PATH 或 herdr 缺席时跳过。

import (
	"context"
	"encoding/json"
	"os"
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
	backend.runner = &execCommandRunner{backend: &CMUXBackend{path: path, sum: sum}, limit: defaultCommandLimit}
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

// Probe checks the herdr binary identity and that its control surface answers.
func (b *HerdrBackend) Probe(ctx context.Context) (port.TerminalProbeResult, error) {
	if err := b.verify(); err != nil {
		return port.TerminalProbeResult{BackendID: b.ID(), Diagnostic: "binary-replaced"}, err
	}
	if _, err := b.runner.Run(ctx, "workspace", "list", "--json"); err != nil {
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

// Start creates a herdr workspace running the sealed launcher, then delivers the
// frozen initial prompt. Mirrors the cmux start handshake.
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
	command, err := shellCommandWithPID([]string{request.LauncherExecutable, "__launch", reference.Path}, pidPath)
	if err != nil {
		return nil, err
	}
	output, err := b.runner.Run(ctx, "workspace", "create", "--name", request.Title, "--description", request.Description,
		"--cwd", request.WorkingDirectory, "--command", command, "--focus", "false")
	if err != nil {
		return nil, ErrUnavailable
	}
	match := workspacePattern.FindStringSubmatch(strings.TrimSpace(output))
	if match == nil {
		return nil, ErrUnavailable
	}
	workspace := match[1]
	if err := b.waitCommandVisible(ctx, workspace); err != nil {
		_, _ = b.runner.Run(context.Background(), "workspace", "close", workspace)
		return nil, err
	}
	if _, err := b.runner.Run(ctx, "send-key", "--workspace", workspace, "enter"); err != nil {
		_, _ = b.runner.Run(context.Background(), "workspace", "close", workspace)
		return nil, ErrUnavailable
	}
	pid, err := b.waitPID(ctx, pidPath)
	if err != nil {
		_, _ = b.runner.Run(context.Background(), "workspace", "close", workspace)
		return nil, err
	}
	if err := b.waitEnvelopeConsumed(ctx, reference.Path); err != nil {
		_, _ = b.runner.Run(context.Background(), "workspace", "close", workspace)
		return nil, err
	}
	envelopePending = false
	pgid := 0
	if b.processes.Supported() {
		pgid, err = b.processes.GroupID(pid)
		if err != nil {
			_, _ = b.runner.Run(context.Background(), "workspace", "close", workspace)
			return nil, err
		}
	}
	recordPath := filepath.Join(attemptDirectory, "terminal-session.json")
	session := &herdrSession{
		backend: b, workspace: workspace, pid: pid, pgid: pgid, state: StateRunning,
		capabilities: probe.Capabilities, recordPath: recordPath,
		record: sessionRecord{BackendID: b.ID(), WorkspaceRef: workspace, RunID: request.RunID, AttemptID: request.AttemptID,
			PID: pid, ProcessGroupID: pgid, Executable: request.Executable, ExecutableDigest: executableSum,
			LauncherExecutable: request.LauncherExecutable, LauncherExecutableDigest: launcherSum, LaunchEnvelopeDigest: reference.Digest,
			State: StateRunning, CreatedAt: request.Now.UTC(), UpdatedAt: request.Now.UTC()},
	}
	if err := session.persist(); err != nil {
		_ = b.processes.Terminate(context.Background(), pid, time.Second)
		_, _ = b.runner.Run(context.Background(), "workspace", "close", workspace)
		return nil, err
	}
	if err := session.Send(ctx, InputSourceFrozenPrompt, request.InitialPrompt, request.Now); err != nil {
		_ = session.Terminate(context.Background(), time.Second)
		_, _ = b.runner.Run(context.Background(), "workspace", "close", workspace)
		return nil, err
	}
	return session, nil
}

func (b *HerdrBackend) waitCommandVisible(ctx context.Context, workspace string) error {
	deadline := time.NewTimer(b.startLimit)
	defer deadline.Stop()
	ticker := time.NewTicker(b.startDelay)
	defer ticker.Stop()
	for {
		output, err := b.runner.Run(ctx, "read-screen", "--workspace", workspace, "--lines", "100")
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

// herdrSession implements port.TerminalSession on top of a herdr workspace.
type herdrSession struct {
	backend      *HerdrBackend
	workspace    string
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
	if _, err := s.backend.runner.Run(ctx, "send", "--workspace", s.workspace, text); err != nil {
		return ErrUnavailable
	}
	if _, err := s.backend.runner.Run(ctx, "send-key", "--workspace", s.workspace, "enter"); err != nil {
		return ErrUnavailable
	}
	return s.appendInput(inputRecord{Sequence: s.inputCount, Source: source, Digest: digestText(text), Phase: "delivered", SentAt: now.UTC()})
}

func (s *herdrSession) ReadScreen(ctx context.Context, lines int) (string, error) {
	if lines < 1 || lines > 10000 {
		return "", ErrInvalidRequest
	}
	return s.backend.runner.Run(ctx, "read-screen", "--workspace", s.workspace, "--lines", itoa(lines))
}

func (s *herdrSession) InterruptStep(ctx context.Context) error {
	if s.state != StateRunning {
		return ErrSessionState
	}
	if _, err := s.backend.runner.Run(ctx, "send-key", "--workspace", s.workspace, "escape"); err != nil {
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
	// Reuse the cmux session input journal format (terminal-inputs.jsonl).
	return appendInputJournal(filepath.Dir(s.recordPath), record)
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

// digestText returns the canonical digest of an input string for provenance.
func digestText(text string) string { return canonical.DigestBytes([]byte(text)) }

// itoa is a small strconv wrapper to keep call sites terse.
func itoa(n int) string { return strconv.Itoa(n) }

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
