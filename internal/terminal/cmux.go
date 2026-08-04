package terminal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/launcher"
)

const (
	CMUXBackendID       = "cmux-pty"
	defaultCommandLimit = 1 << 20
	defaultStartTimeout = 10 * time.Second
	inputJournalLimit   = 32 << 20
)

var workspacePattern = regexp.MustCompile(`^OK (workspace:[0-9]+)$`)

type commandRunner interface {
	Run(context.Context, ...string) (string, error)
}

type processController interface {
	Supported() bool
	GroupID(int) (int, error)
	Pause(int) ([]int, error)
	Resume(int, []int) error
	Terminate(context.Context, int, time.Duration) error
}

// CMUXBackend owns native TUI workspaces through the frozen cmux control CLI.
// It does not install hooks or modify user Agent configuration.
type CMUXBackend struct {
	path       string
	sum        string
	runner     commandRunner
	processes  processController
	startDelay time.Duration
	startLimit time.Duration
}

func NewCMUXBackend(path string) (*CMUXBackend, error) {
	if !filepath.IsAbs(path) || path != filepath.Clean(path) {
		return nil, ErrInvalidRequest
	}
	sum, err := executableDigest(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	backend := &CMUXBackend{path: path, sum: sum, processes: defaultProcessController(), startDelay: 50 * time.Millisecond, startLimit: defaultStartTimeout}
	backend.runner = &execCommandRunner{backend: backend, limit: defaultCommandLimit}
	return backend, nil
}

func (b *CMUXBackend) ID() string { return CMUXBackendID }

func (b *CMUXBackend) Probe(ctx context.Context) (ProbeResult, error) {
	if err := b.verify(); err != nil {
		return ProbeResult{BackendID: b.ID(), Diagnostic: "binary-replaced"}, err
	}
	output, err := b.runner.Run(ctx, "capabilities", "--json")
	if err != nil {
		return ProbeResult{BackendID: b.ID(), Diagnostic: "probe-failed"}, ErrUnavailable
	}
	var response struct {
		Methods []string `json:"methods"`
	}
	if json.Unmarshal([]byte(output), &response) != nil {
		return ProbeResult{BackendID: b.ID(), Diagnostic: "invalid-capabilities"}, ErrUnavailable
	}
	required := []string{"workspace.create", "surface.send_text", "surface.send_key", "surface.read_text", "workspace.close"}
	for _, method := range required {
		if !slices.Contains(response.Methods, method) {
			return ProbeResult{BackendID: b.ID(), Diagnostic: "missing-required-method"}, nil
		}
	}
	capabilities := []Capability{CapabilitySessionCreate, CapabilityPromptSend, CapabilityScreenRead, CapabilityInterruptStep}
	if b.processes.Supported() {
		capabilities = append(capabilities, CapabilityPauseResume, CapabilityTerminate)
	}
	// Complete provenance includes input typed directly into the terminal, and
	// session resume requires provider hooks/configuration. Marshal does not
	// install either silently, so those capabilities are deliberately omitted.
	return ProbeResult{BackendID: b.ID(), Available: true, Capabilities: capabilities}, nil
}

func (b *CMUXBackend) Start(ctx context.Context, request StartRequest) (Session, error) {
	if err := validateStartRequest(request); err != nil {
		return nil, err
	}
	probe, err := b.Probe(ctx)
	if err != nil || !probe.Available {
		return nil, ErrUnavailable
	}
	executableSum, err := executableDigest(request.Executable)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	if executableSum != request.ExpectedExecutableDigest {
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
	session := &cmuxSession{
		backend: b, workspace: workspace, pid: pid, pgid: pgid, state: StateRunning,
		capabilities: slices.Clone(probe.Capabilities), recordPath: recordPath,
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

func (b *CMUXBackend) waitEnvelopeConsumed(ctx context.Context, path string) error {
	deadline := time.NewTimer(b.startLimit)
	defer deadline.Stop()
	ticker := time.NewTicker(b.startDelay)
	defer ticker.Stop()
	for {
		_, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("%w: trusted launcher did not consume envelope", ErrUnavailable)
		case <-ticker.C:
		}
	}
}

func (b *CMUXBackend) waitCommandVisible(ctx context.Context, workspace string) error {
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
			return fmt.Errorf("%w: cmux start command did not become visible", ErrUnavailable)
		case <-ticker.C:
		}
	}
}

func (b *CMUXBackend) verify() error {
	sum, err := executableDigest(b.path)
	if err != nil || sum != b.sum {
		return ErrUnavailable
	}
	return nil
}

func (b *CMUXBackend) waitPID(ctx context.Context, path string) (int, error) {
	deadline := time.NewTimer(b.startLimit)
	defer deadline.Stop()
	ticker := time.NewTicker(b.startDelay)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
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
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-deadline.C:
			return 0, fmt.Errorf("%w: terminal PID handshake timed out", ErrUnavailable)
		case <-ticker.C:
		}
	}
}

type sessionRecord struct {
	BackendID                string    `json:"backendId"`
	WorkspaceRef             string    `json:"workspaceRef"`
	RunID                    string    `json:"runId"`
	AttemptID                string    `json:"attemptId"`
	PID                      int       `json:"pid"`
	ProcessGroupID           int       `json:"processGroupId,omitempty"`
	Executable               string    `json:"executable"`
	ExecutableDigest         string    `json:"executableDigest"`
	LauncherExecutable       string    `json:"launcherExecutable"`
	LauncherExecutableDigest string    `json:"launcherExecutableDigest"`
	LaunchEnvelopeDigest     string    `json:"launchEnvelopeDigest"`
	State                    State     `json:"state"`
	CreatedAt                time.Time `json:"createdAt"`
	UpdatedAt                time.Time `json:"updatedAt"`
}

type inputRecord struct {
	Sequence uint64      `json:"sequence"`
	Source   InputSource `json:"source"`
	Digest   string      `json:"digest"`
	Phase    string      `json:"phase"`
	SentAt   time.Time   `json:"sentAt"`
}

type cmuxSession struct {
	backend      *CMUXBackend
	workspace    string
	pid          int
	pgid         int
	capabilities []Capability
	recordPath   string
	record       sessionRecord
	inputCount   uint64
	state        State
	pausedGroups []int
	mu           sync.Mutex
}

func (s *cmuxSession) ID() string {
	return "cmux:" + s.record.RunID + ":" + s.record.AttemptID + ":" + s.workspace
}
func (s *cmuxSession) Identity() SessionIdentity {
	return SessionIdentity{RunID: s.record.RunID, AttemptID: s.record.AttemptID}
}
func (s *cmuxSession) State() State               { s.mu.Lock(); defer s.mu.Unlock(); return s.state }
func (s *cmuxSession) Capabilities() []Capability { return slices.Clone(s.capabilities) }

func (s *cmuxSession) Send(ctx context.Context, source InputSource, text string, now time.Time) error {
	if strings.TrimSpace(text) == "" || len(text) > 1<<20 || now.IsZero() ||
		!slices.Contains([]InputSource{InputSourceFrozenPrompt, InputSourceLeadSteering, InputSourceHumanSteering}, source) {
		return ErrInvalidRequest
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateRunning {
		return ErrSessionState
	}
	s.inputCount++
	record := inputRecord{Sequence: s.inputCount, Source: source, Digest: canonical.DigestBytes([]byte(text)), Phase: "planned", SentAt: now.UTC()}
	if err := s.appendInput(record); err != nil {
		return err
	}
	if _, err := s.backend.runner.Run(ctx, "send", "--workspace", s.workspace, text); err != nil {
		return ErrUnavailable
	}
	if _, err := s.backend.runner.Run(ctx, "send-key", "--workspace", s.workspace, "enter"); err != nil {
		return ErrUnavailable
	}
	record.Phase = "delivered"
	return s.appendInput(record)
}

func (s *cmuxSession) ReadScreen(ctx context.Context, lines int) (string, error) {
	if lines < 1 || lines > 10000 {
		return "", ErrInvalidRequest
	}
	output, err := s.backend.runner.Run(ctx, "read-screen", "--workspace", s.workspace, "--lines", strconv.Itoa(lines))
	if err != nil {
		return "", ErrUnavailable
	}
	return output, nil
}

func (s *cmuxSession) InterruptStep(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateRunning {
		return ErrSessionState
	}
	if _, err := s.backend.runner.Run(ctx, "send-key", "--workspace", s.workspace, "escape"); err != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *cmuxSession) Pause(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateRunning {
		return ErrSessionState
	}
	if !s.backend.processes.Supported() {
		return ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	groups, err := s.backend.processes.Pause(s.pid)
	if err != nil {
		return err
	}
	s.pausedGroups = groups
	return s.setState(StatePaused, time.Now().UTC())
}

func (s *cmuxSession) Resume(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StatePaused {
		return ErrSessionState
	}
	if !s.backend.processes.Supported() {
		return ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.backend.processes.Resume(s.pid, s.pausedGroups); err != nil {
		return err
	}
	s.pausedGroups = nil
	return s.setState(StateRunning, time.Now().UTC())
}

func (s *cmuxSession) Terminate(ctx context.Context, grace time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateTerminated {
		return nil
	}
	if !s.backend.processes.Supported() {
		return ErrUnsupported
	}
	if err := s.backend.processes.Terminate(ctx, s.pid, grace); err != nil {
		return err
	}
	return s.setState(StateTerminated, time.Now().UTC())
}

func (s *cmuxSession) setState(state State, now time.Time) error {
	s.state, s.record.State, s.record.UpdatedAt = state, state, now.UTC()
	return s.persist()
}

func (s *cmuxSession) persist() error {
	data, err := json.MarshalIndent(s.record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWrite(s.recordPath, data)
}

func (s *cmuxSession) appendInput(record inputRecord) error {
	directory := filepath.Dir(s.recordPath)
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
	} else if !errors.Is(statErr, os.ErrNotExist) {
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

func atomicWrite(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := secureDirectory(directory); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".terminal-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func secureDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidRequest
	}
	return os.Chmod(path, 0o700)
}

func validateStartRequest(request StartRequest) error {
	if !filepath.IsAbs(request.StateRoot) || request.StateRoot != filepath.Clean(request.StateRoot) || domain.ValidateID(request.RunID) != nil || domain.ValidateID(request.AttemptID) != nil ||
		!filepath.IsAbs(request.WorkingDirectory) || request.WorkingDirectory != filepath.Clean(request.WorkingDirectory) ||
		!filepath.IsAbs(request.LauncherExecutable) || request.LauncherExecutable != filepath.Clean(request.LauncherExecutable) ||
		!filepath.IsAbs(request.Executable) || request.Executable != filepath.Clean(request.Executable) || request.Title == "" || request.Now.IsZero() || !request.ExpiresAt.After(request.Now) ||
		strings.TrimSpace(request.InitialPrompt) == "" || len(request.InitialPrompt) > 1<<20 {
		return ErrInvalidRequest
	}
	if info, err := os.Stat(request.WorkingDirectory); err != nil || !info.IsDir() {
		return ErrInvalidRequest
	}
	for _, argument := range request.Arguments {
		if strings.ContainsRune(argument, 0) || strings.ContainsAny(argument, "\r\n") {
			return ErrInvalidRequest
		}
	}
	return nil
}

func shellCommand(arguments []string) (string, error) {
	if len(arguments) == 0 {
		return "", ErrInvalidRequest
	}
	quoted := make([]string, len(arguments))
	for index, argument := range arguments {
		if strings.ContainsRune(argument, 0) || strings.ContainsAny(argument, "\r\n") {
			return "", ErrInvalidRequest
		}
		quoted[index] = "'" + strings.ReplaceAll(argument, "'", `'"'"'`) + "'"
	}
	return "exec " + strings.Join(quoted, " "), nil
}

func shellCommandWithPID(arguments []string, pidPath string) (string, error) {
	command, err := shellCommand(arguments)
	if err != nil || !filepath.IsAbs(pidPath) || pidPath != filepath.Clean(pidPath) || strings.ContainsAny(pidPath, "\r\n\x00") {
		return "", ErrInvalidRequest
	}
	quotedPath := "'" + strings.ReplaceAll(pidPath, "'", `'"'"'`) + "'"
	// The terminal login shell may share a PGID with root-owned /usr/bin/login
	// on macOS. A monitored background job receives its own PGID; fg then gives
	// it the real PTY while Marshal retains a signalable worker-owned group.
	return ": MARSHAL_LAUNCH_READY; set -m; (" + command + ") & marshal_pid=$!; printf '%s\\n' \"$marshal_pid\" > " + quotedPath + " && fg", nil
}

func preparePIDHandshake(directory string) (string, error) {
	if err := secureDirectory(directory); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(directory, ".terminal-bootstrap-*.pid")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

func executableDigest(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", ErrUnavailable
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

type execCommandRunner struct {
	backend *CMUXBackend
	limit   int
}

func (r *execCommandRunner) Run(ctx context.Context, arguments ...string) (string, error) {
	if err := r.backend.verify(); err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, r.backend.path, arguments...)
	command.Env = terminalEnvironment()
	stdout, stderr := &boundedBuffer{limit: r.limit}, &boundedBuffer{limit: 4 << 10}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil || stdout.overflow {
		return "", ErrUnavailable
	}
	return string(bytes.TrimSpace(stdout.Bytes())), nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.overflow = true
		return original, nil
	}
	if len(data) > remaining {
		b.overflow = true
		data = data[:remaining]
	}
	_, _ = b.Buffer.Write(data)
	return original, nil
}

func terminalEnvironment() []string {
	var environment []string
	for _, key := range []string{"HOME", "TMPDIR", "CMUX_SOCKET_PASSWORD"} {
		if value, ok := os.LookupEnv(key); ok {
			environment = append(environment, key+"="+value)
		}
	}
	return environment
}
