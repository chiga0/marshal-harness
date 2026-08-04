package observer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"
)

// CMUXBackendID is the stable identifier of the cmux observer backend.
const CMUXBackendID = "cmux"

// Fixed diagnostic categories for cmux probes. Diagnostics never carry
// process output, stderr, environment values or socket credentials.
const (
	DiagCMUXNotInstalled   = "not-installed"
	DiagCMUXBinaryReplaced = "binary-replaced"
	DiagCMUXExecFailed     = "capabilities-exec-failed"
	DiagCMUXTimeout        = "capabilities-timeout"
	DiagCMUXOutputTooLarge = "capabilities-output-too-large"
	DiagCMUXInvalidJSON    = "capabilities-invalid-json"
	DiagCMUXProtocol       = "capabilities-protocol"
	DiagCMUXUnauthorized   = "unauthorized"
)

const (
	defaultCMUXProbeTimeout = 5 * time.Second
	defaultCMUXMaxStdout    = 64 << 10
	defaultCMUXMaxStderr    = 4 << 10
)

var (
	// ErrCMUXBinaryReplaced is returned when the frozen executable no
	// longer matches its recorded identity. The backend fails closed and
	// the binary is not executed.
	ErrCMUXBinaryReplaced = errors.New("observer: cmux binary replaced since discovery")

	// ErrCMUXNotReady is returned by Attach when the probe does not reach
	// the ready phase. No workspace is created.
	ErrCMUXNotReady = errors.New("observer: cmux backend is not ready")

	// ErrCMUXWorkspaceCreateFailed is returned when the create command
	// output is not the strict success line. The error is fixed and
	// content-free: CLI output is never echoed back.
	ErrCMUXWorkspaceCreateFailed = errors.New("observer: cmux workspace create failed")

	// ErrCMUXDetached is returned by Handle.Update once the handle has
	// been detached. No child process is spawned.
	ErrCMUXDetached = errors.New("observer: cmux handle detached")

	// ErrCMUXInvalidLogLevel is returned by Handle.Update when LogLevel
	// is not one of the values accepted by the real cmux CLI. The request
	// is rejected before any command runs.
	ErrCMUXInvalidLogLevel = errors.New("observer: cmux invalid log level")

	errCMUXNotDiscovered = errors.New("observer: cmux control CLI not discovered")
)

// cmux update protocol constants, verified against real cmux 0.64.20 via
// direct argv (never through a shell).
const (
	// cmuxStatusKey is the fixed status key written by set-status.
	cmuxStatusKey = "marshal.status"
	// cmuxLogSource is the fixed --source value for log commands.
	cmuxLogSource = "marshal"
	// cmuxDefaultLogLevel is used when LogMessage is non-empty and
	// LogLevel is empty.
	cmuxDefaultLogLevel = "info"
	// cmuxDefaultProgressLabel is the set-progress label fallback when
	// Status is empty.
	cmuxDefaultProgressLabel = "Marshal"
)

// cmuxLogLevels is the complete set of log levels accepted by the real
// cmux CLI. Anything else fails closed before any command runs.
var cmuxLogLevels = map[string]bool{
	"info":     true,
	"progress": true,
	"success":  true,
	"warning":  true,
	"error":    true,
}

// cmuxWorkspaceCreatedPattern matches the strict, complete success output
// of `workspace create`: exactly one line of the form
// "OK workspace:<digits>". The capture group keeps the complete own ref
// ("workspace:<digits>"), not just the digits. Anything else fails closed.
var cmuxWorkspaceCreatedPattern = regexp.MustCompile(`^OK (workspace:[0-9]+)$`)

// cmuxMethodCapabilities is the only source of capability mapping. It maps
// the dotted method names actually reported by `cmux capabilities --json`
// (e.g. pane.create, workspace.create, surface.read_text,
// notification.create). Methods that cannot be proven from real CLI
// output are never mapped: capabilities are not guessed.
var cmuxMethodCapabilities = map[string]Capability{
	"pane.create":         CapabilityPaneCreate,
	"workspace.create":    CapabilityWorkspaceCreate,
	"surface.read_text":   CapabilityScreenRead,
	"notification.create": CapabilityNotify,
}

// defaultCMUXAppBundleCandidates lists the control CLI inside macOS app
// bundles: the system-wide installation and the user-level installation
// under the home directory. The app main executable under Contents/MacOS
// is never a candidate.
func defaultCMUXAppBundleCandidates() []string {
	paths := []string{
		"/Applications/cmux.app/Contents/Resources/bin/cmux",
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, "Applications", "cmux.app", "Contents", "Resources", "bin", "cmux"))
	}
	return paths
}

// CMUXBackend probes the cmux control CLI. It discovers the executable
// once, freezes its resolved path, SHA256 and portable file identity, and
// verifies that identity before every probe execution.
type CMUXBackend struct {
	explicitPath   string
	appBundlePaths []string
	timeout        time.Duration
	maxStdout      int
	maxStderr      int

	mu     sync.Mutex
	frozen bool
	path   string
	sum    [sha256.Size]byte
	info   os.FileInfo
}

var _ Backend = (*CMUXBackend)(nil)

// NewCMUXBackend returns a cmux backend. A non-empty explicitPath must be
// absolute and lexically clean (equal to its own filepath.Clean); it
// becomes the first discovery candidate ahead of PATH and the macOS app
// bundles. No environment variable other than PATH influences discovery;
// in particular CMUX_SOCKET_PASSWORD is never read.
func NewCMUXBackend(explicitPath string) (*CMUXBackend, error) {
	if explicitPath != "" {
		if !filepath.IsAbs(explicitPath) || explicitPath != filepath.Clean(explicitPath) {
			return nil, fmt.Errorf("observer: cmux explicit path must be absolute and lexically clean")
		}
	}
	return &CMUXBackend{
		explicitPath:   explicitPath,
		appBundlePaths: defaultCMUXAppBundleCandidates(),
		timeout:        defaultCMUXProbeTimeout,
		maxStdout:      defaultCMUXMaxStdout,
		maxStderr:      defaultCMUXMaxStderr,
	}, nil
}

// ID implements Backend.
func (b *CMUXBackend) ID() string { return CMUXBackendID }

// Probe implements Backend. It discovers and freezes the control CLI on
// first success, re-verifies the frozen identity, then runs
// `capabilities --json` directly (never through a shell) with a bounded
// timeout and bounded output capture.
func (b *CMUXBackend) Probe(ctx context.Context) (ProbeResult, error) {
	if err := ctx.Err(); err != nil {
		return ProbeResult{}, err
	}

	b.mu.Lock()
	if !b.frozen {
		if err := b.discoverLocked(); err != nil {
			b.mu.Unlock()
			return ProbeResult{
				BackendID:  CMUXBackendID,
				Phase:      PhaseNotInstalled,
				Diagnostic: DiagCMUXNotInstalled,
			}, nil
		}
	}
	path := b.path
	b.mu.Unlock()

	if !b.verifyIdentity() {
		return ProbeResult{
			BackendID:  CMUXBackendID,
			Phase:      PhaseInstalled,
			Executable: path,
			Diagnostic: DiagCMUXBinaryReplaced,
		}, ErrCMUXBinaryReplaced
	}
	return b.runCapabilities(ctx, path)
}

// Attach implements Backend. It validates the request and the working
// directory, requires a ready probe, then creates a workspace directly
// via argv — never through a shell — with a short timeout and bounded
// output. Success requires the strict single line "OK workspace:<digits>";
// anything else fails closed with a fixed, content-free error. The
// returned handle remembers only the complete workspace ref
// ("workspace:<digits>") it created itself.
func (b *CMUXBackend) Attach(ctx context.Context, req AttachRequest) (Handle, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	cwd := req.WorkingDirectory
	if !filepath.IsAbs(cwd) || cwd != filepath.Clean(cwd) {
		return nil, errors.New("observer: cmux working directory must be absolute and lexically clean")
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return nil, errors.New("observer: cmux working directory is not an existing directory")
	}

	res, err := b.Probe(ctx)
	if err != nil {
		return nil, err
	}
	if res.Phase != PhaseReady {
		return nil, ErrCMUXNotReady
	}

	out, err := b.runCMUXCommand(ctx, "workspace", "create",
		"--name", req.Title,
		"--description", req.Description,
		"--cwd", cwd,
		"--focus", "false")
	if err != nil {
		return nil, err
	}
	m := cmuxWorkspaceCreatedPattern.FindStringSubmatch(out)
	if m == nil {
		return nil, ErrCMUXWorkspaceCreateFailed
	}
	return &cmuxHandle{
		backend:   b,
		runID:     req.RunID,
		attemptID: req.AttemptID,
		ref:       m[1],
	}, nil
}

// runCMUXCommand executes the frozen control CLI directly via argv with a
// short timeout, bounded output capture and a minimal environment
// allowlist. It re-verifies the frozen binary identity before every run
// and fails closed on mismatch. Stdout is returned trimmed; stderr is
// discarded and its content never surfaces in errors.
func (b *CMUXBackend) runCMUXCommand(parent context.Context, args ...string) (string, error) {
	if err := parent.Err(); err != nil {
		return "", err
	}
	if !b.verifyIdentity() {
		return "", ErrCMUXBinaryReplaced
	}
	b.mu.Lock()
	path := b.path
	b.mu.Unlock()

	ctx, cancel := context.WithTimeout(parent, b.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = cmuxEnv()
	// WaitDelay bounds Wait even if grandchild processes keep the output
	// pipes open after the CLI itself is killed on timeout.
	cmd.WaitDelay = b.timeout
	stdout := &limitedBuffer{limit: b.maxStdout}
	cmd.Stdout = stdout
	cmd.Stderr = &boundedDiscard{limit: b.maxStderr}
	runErr := cmd.Run()

	if runErr != nil {
		if err := parent.Err(); err != nil {
			return "", err
		}
		if ctx.Err() != nil {
			return "", errors.New("observer: cmux command timed out")
		}
		return "", errors.New("observer: cmux command failed")
	}
	if stdout.overflow {
		return "", errors.New("observer: cmux command output too large")
	}
	return string(bytes.TrimSpace(stdout.Bytes())), nil
}

// cmuxEnv builds the minimal environment allowlist for direct CLI
// invocations. CMUX_SOCKET_PASSWORD is passed through when set but is
// never recorded or echoed by the backend.
func cmuxEnv() []string {
	var env []string
	for _, key := range []string{"HOME", "TMPDIR", "CMUX_SOCKET_PASSWORD"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}

// cmuxHandle tracks exactly one workspace created by Attach. It never
// lists, guesses or closes workspaces it did not create itself.
type cmuxHandle struct {
	backend   *CMUXBackend
	runID     string
	attemptID string
	// ref is the complete "workspace:<digits>" ref from the create output.
	ref string

	mu       sync.Mutex
	detached bool
}

var _ Handle = (*cmuxHandle)(nil)

// ID implements Handle. The stored ref is already the complete
// "workspace:<digits>" form.
func (h *cmuxHandle) ID() string {
	return "cmux:" + h.runID + ":" + h.attemptID + ":" + h.ref
}

// Update implements Handle. It validates the context, the request and the
// log level before any command runs, then executes only the commands for
// non-empty fields in the fixed order status -> progress -> log ->
// notification, all directly via argv and verified against real cmux
// 0.64.20:
//
//	set-status marshal.status VALUE --workspace <ref>
//	set-progress VALUE --label LABEL --workspace <ref>
//	log --level LEVEL --source marshal --workspace <ref> MESSAGE
//	notify --title TITLE --body BODY --workspace <ref>
//
// Every free-text value is passed as a single argv element; nothing is
// ever run through a shell. The whole update is serialized under the
// handle mutex together with Detach so close and update never interleave.
// An empty update succeeds with no side effects; a detached handle returns
// the fixed ErrCMUXDetached without spawning any child process; the first
// command failure aborts the remaining commands and surfaces a fixed,
// content-free error.
func (h *cmuxHandle) Update(ctx context.Context, req UpdateRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	level := req.LogLevel
	if req.LogMessage != "" && level == "" {
		level = cmuxDefaultLogLevel
	}
	if level != "" && !cmuxLogLevels[level] {
		return ErrCMUXInvalidLogLevel
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.detached {
		return ErrCMUXDetached
	}

	if req.Status != "" {
		if _, err := h.backend.runCMUXCommand(ctx,
			"set-status", cmuxStatusKey, req.Status,
			"--workspace", h.ref); err != nil {
			return err
		}
	}
	if req.Progress != nil {
		label := req.Status
		if label == "" {
			label = cmuxDefaultProgressLabel
		}
		if _, err := h.backend.runCMUXCommand(ctx,
			"set-progress", strconv.FormatFloat(*req.Progress, 'f', -1, 64),
			"--label", label,
			"--workspace", h.ref); err != nil {
			return err
		}
	}
	if req.LogMessage != "" {
		if _, err := h.backend.runCMUXCommand(ctx,
			"log", "--level", level, "--source", cmuxLogSource,
			"--workspace", h.ref,
			req.LogMessage); err != nil {
			return err
		}
	}
	if req.Notification != "" {
		if _, err := h.backend.runCMUXCommand(ctx,
			"notify", "--title", "Marshal run "+h.runID,
			"--body", req.Notification,
			"--workspace", h.ref); err != nil {
			return err
		}
	}
	return nil
}

// Detach implements Handle. It is mutex-serialized and idempotent: after
// the first successful close, further calls are no-ops. A failed close
// does not mark the handle detached, so Detach can be retried. The frozen
// binary identity is re-verified before every close attempt, and only the
// stored complete ref is ever closed via the direct argv
// `workspace close <ref>`.
func (h *cmuxHandle) Detach(ctx context.Context, req DetachRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.detached {
		return nil
	}
	if _, err := h.backend.runCMUXCommand(ctx, "workspace", "close", h.ref); err != nil {
		return err
	}
	h.detached = true
	return nil
}

// discoverLocked freezes the first valid candidate. Callers must hold b.mu.
func (b *CMUXBackend) discoverLocked() error {
	var candidates []string
	if b.explicitPath != "" {
		candidates = append(candidates, b.explicitPath)
	}
	if p, err := exec.LookPath("cmux"); err == nil {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, b.appBundlePaths...)

	for _, candidate := range candidates {
		resolved, sum, info, ok := freezeCandidate(candidate)
		if !ok {
			continue
		}
		b.frozen = true
		b.path = resolved
		b.sum = sum
		b.info = info
		return nil
	}
	return errCMUXNotDiscovered
}

// verifyIdentity re-checks the frozen executable against its recorded
// resolved path, SHA256 and file identity. Any mismatch fails closed.
func (b *CMUXBackend) verifyIdentity() bool {
	b.mu.Lock()
	path, sum, info := b.path, b.sum, b.info
	b.mu.Unlock()

	resolved, newSum, newInfo, ok := freezeCandidate(path)
	if !ok || info == nil || newInfo == nil {
		return false
	}
	return resolved == path && newSum == sum && os.SameFile(info, newInfo)
}

// freezeCandidate resolves symlinks and requires a regular executable
// file, returning its resolved path, content hash and portable file
// identity (os.FileInfo; no unix-specific syscall types are used, so this
// compiles on linux, darwin and windows).
func freezeCandidate(path string) (string, [sha256.Size]byte, os.FileInfo, bool) {
	var zero [sha256.Size]byte
	if !filepath.IsAbs(path) {
		return "", zero, nil, false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", zero, nil, false
	}
	info, err := os.Stat(resolved)
	if err != nil || info == nil || !info.Mode().IsRegular() {
		return "", zero, nil, false
	}
	if info.Mode().Perm()&0o111 == 0 {
		return "", zero, nil, false
	}
	f, err := os.Open(resolved)
	if err != nil {
		return "", zero, nil, false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", zero, nil, false
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return resolved, sum, info, true
}

// runCapabilities executes `capabilities --json` and classifies the
// outcome into probe phases.
func (b *CMUXBackend) runCapabilities(parent context.Context, path string) (ProbeResult, error) {
	ctx, cancel := context.WithTimeout(parent, b.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "capabilities", "--json")
	cmd.Env = cmuxEnv()
	// Bound Wait even if a failed control CLI leaves descendants holding the
	// capture pipes open after CommandContext terminates the direct process.
	cmd.WaitDelay = b.timeout
	stdout := &limitedBuffer{limit: b.maxStdout}
	cmd.Stdout = stdout
	// stderr is consumed by a bounded discarder: its content is never
	// recorded, reported or returned, so hostile output cannot reach
	// diagnostics or errors.
	cmd.Stderr = &boundedDiscard{limit: b.maxStderr}
	runErr := cmd.Run()

	if err := parent.Err(); err != nil {
		return ProbeResult{}, err
	}

	result := ProbeResult{
		BackendID:  CMUXBackendID,
		Phase:      PhaseInstalled,
		Executable: path,
	}

	if stdout.overflow {
		result.Diagnostic = DiagCMUXOutputTooLarge
		return result, nil
	}
	if ctx.Err() != nil {
		result.Diagnostic = DiagCMUXTimeout
		return result, nil
	}

	var caps cmuxCapabilities
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &caps); err != nil {
		if runErr != nil {
			result.Diagnostic = DiagCMUXExecFailed
		} else {
			result.Diagnostic = DiagCMUXInvalidJSON
		}
		return result, nil
	}

	result.AccessMode = caps.AccessMode
	result.Methods = append([]string(nil), caps.Methods...)
	if !cmuxAccessModeAuthorized(caps.AccessMode) {
		result.Phase = PhaseReachable
		result.Diagnostic = DiagCMUXUnauthorized
		return result, nil
	}
	// Version is decoded strictly: a non-empty JSON string or a finite
	// JSON number, normalized to a string. Any other shape yields the
	// fixed protocol diagnostic without echoing the offending value.
	version, ok := normalizeCMUXVersion(caps.Version)
	if !ok {
		result.Diagnostic = DiagCMUXProtocol
		return result, nil
	}
	result.Version = version
	// Readiness additionally requires a complete protocol envelope. The
	// socket path is validated but never surfaced in the probe result.
	if caps.Protocol == "" || caps.SocketPath == "" {
		result.Diagnostic = DiagCMUXProtocol
		return result, nil
	}
	result.Phase = PhaseReady
	result.Capabilities = mapCMUXMethods(caps.Methods)
	return result, nil
}

// cmuxCapabilities is the parsed `capabilities --json` payload. Protocol
// and socket path are parsed for validation only and never surfaced in
// probe results. Version is kept as raw JSON because real CLI releases
// report it either as a string or as a number; strict decoding happens in
// normalizeCMUXVersion.
type cmuxCapabilities struct {
	AccessMode string          `json:"access_mode"`
	Methods    []string        `json:"methods"`
	Protocol   string          `json:"protocol"`
	SocketPath string          `json:"socket_path"`
	Version    json.RawMessage `json:"version"`
}

// normalizeCMUXVersion strictly decodes the version field. Accepted forms
// are a non-empty JSON string and a finite JSON number; numbers are
// normalized to their decimal string form (2 becomes "2"). Null, objects,
// arrays, booleans, empty strings and non-finite numbers are rejected.
// The raw value is never echoed back: rejection carries no content.
func normalizeCMUXVersion(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", false
	}
	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil || s == "" {
			return "", false
		}
		return s, true
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		var n json.Number
		if err := json.Unmarshal(trimmed, &n); err != nil {
			return "", false
		}
		lit := n.String()
		if i, err := strconv.ParseInt(lit, 10, 64); err == nil {
			return strconv.FormatInt(i, 10), true
		}
		f, err := strconv.ParseFloat(lit, 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return "", false
		}
		return lit, true
	default: // null, object, array, boolean
		return "", false
	}
}

// cmuxAccessModeAuthorized fails closed: only explicitly known access
// modes count as authorized. "password" means the CLI has already
// completed access via the stored password and is therefore authorized.
func cmuxAccessModeAuthorized(mode string) bool {
	switch mode {
	case "full", "readonly", "authorized", "password":
		return true
	}
	return false
}

// mapCMUXMethods maps known dotted method names to capabilities and
// ignores everything else. The result is sorted for deterministic output.
func mapCMUXMethods(methods []string) []Capability {
	seen := make(map[Capability]bool)
	var caps []Capability
	for _, m := range methods {
		c, ok := cmuxMethodCapabilities[m]
		if !ok || seen[c] {
			continue
		}
		seen[c] = true
		caps = append(caps, c)
	}
	sort.Slice(caps, func(i, j int) bool { return caps[i] < caps[j] })
	return caps
}

// limitedBuffer keeps at most limit bytes and records overflow.
type limitedBuffer struct {
	buf      []byte
	limit    int
	overflow bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	remaining := l.limit - len(l.buf)
	if remaining > 0 {
		n := min(len(p), remaining)
		l.buf = append(l.buf, p[:n]...)
	}
	if len(p) > remaining {
		l.overflow = true
	}
	return len(p), nil
}

func (l *limitedBuffer) Bytes() []byte { return l.buf }

// boundedDiscard consumes and discards bytes, tracking only a saturating
// byte count. Content is never retained.
type boundedDiscard struct {
	written int
	limit   int
}

func (d *boundedDiscard) Write(p []byte) (int, error) {
	remaining := d.limit - d.written
	if remaining < 0 {
		remaining = 0
	}
	if len(p) > remaining {
		d.written = d.limit
	} else {
		d.written += len(p)
	}
	return len(p), nil
}
