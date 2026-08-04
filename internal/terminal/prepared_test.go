package terminal

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

func TestStartPreparedForwardsFrozenSpecWithoutProviderInference(t *testing.T) {
	now := time.Unix(10, 0).UTC()
	spec := port.TerminalLaunchSpec{
		AdapterID: "worker", AdapterVersion: "1", RunID: "run-01", AttemptID: "attempt-01",
		BinaryVersion: "2", Executable: "/bin/worker", ExecutableDigest: "sha256:frozen",
		WorkingDirectory: "/worktree", Arguments: []string{"--safe", "value"},
		Environment: []string{"TERM=xterm-256color"}, InitialPrompt: "frozen prompt",
		CompletionGate: port.TerminalCompletionSupervisedConfirmation,
	}
	adapter := &preparedAdapter{spec: spec}
	backend := &preparedBackend{session: &preparedTerminalSession{}}
	result, err := StartPrepared(context.Background(), adapter, backend, domain.Record{Kind: domain.KindWorkerRequest}, PreparedStartRequest{
		StateRoot: "/state", LauncherExecutable: "/bin/marshal", Title: "title", Description: "description", Now: now, ExpiresAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := port.TerminalStartRequest{
		StateRoot: "/state", RunID: "run-01", AttemptID: "attempt-01", WorkingDirectory: "/worktree",
		LauncherExecutable: "/bin/marshal", Executable: "/bin/worker", ExpectedExecutableDigest: "sha256:frozen",
		Arguments: []string{"--safe", "value"}, Environment: []string{"TERM=xterm-256color"},
		Title: "title", Description: "description", InitialPrompt: "frozen prompt", Now: now, ExpiresAt: now.Add(time.Minute),
	}
	if !reflect.DeepEqual(backend.request, want) {
		t.Fatalf("request = %#v, want %#v", backend.request, want)
	}
	if result.Session != backend.session || result.CompletionGate != port.TerminalCompletionSupervisedConfirmation {
		t.Fatalf("result = %+v", result)
	}
	// The backend cannot mutate Adapter-owned slices through the forwarded copy.
	backend.request.Arguments[0] = "changed"
	backend.request.Environment[0] = "TERM=dumb"
	if spec.Arguments[0] != "--safe" || spec.Environment[0] != "TERM=xterm-256color" {
		t.Fatal("backend mutated frozen Adapter specification")
	}
}

func TestStartPreparedFailsClosedBeforeBackend(t *testing.T) {
	tests := []struct {
		name    string
		adapter *preparedAdapter
	}{
		{name: "adapter error", adapter: &preparedAdapter{err: errors.New("adapter failed")}},
		{name: "adapter identity mismatch", adapter: &preparedAdapter{spec: port.TerminalLaunchSpec{AdapterID: "other", CompletionGate: port.TerminalCompletionSupervisedConfirmation}}},
		{name: "unsupported gate", adapter: &preparedAdapter{spec: port.TerminalLaunchSpec{CompletionGate: "screen-idle"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := &preparedBackend{session: &preparedTerminalSession{}}
			if _, err := StartPrepared(context.Background(), test.adapter, backend, domain.Record{}, PreparedStartRequest{}); err == nil {
				t.Fatal("unsafe prepared launch accepted")
			}
			if backend.called {
				t.Fatal("backend called after preparation failure")
			}
		})
	}
}

type preparedAdapter struct {
	spec port.TerminalLaunchSpec
	err  error
}

func (*preparedAdapter) ID() string                                   { return "worker" }
func (*preparedAdapter) Probe(context.Context) (domain.Record, error) { return domain.Record{}, nil }
func (*preparedAdapter) Run(context.Context, domain.Record) (domain.Record, error) {
	return domain.Record{}, nil
}
func (a *preparedAdapter) PrepareTerminal(context.Context, domain.Record) (port.TerminalLaunchSpec, error) {
	return a.spec, a.err
}

type preparedBackend struct {
	request port.TerminalStartRequest
	session port.TerminalSession
	called  bool
}

func (*preparedBackend) ID() string { return "backend" }
func (*preparedBackend) Probe(context.Context) (port.TerminalProbeResult, error) {
	return port.TerminalProbeResult{}, nil
}
func (b *preparedBackend) Start(_ context.Context, request port.TerminalStartRequest) (port.TerminalSession, error) {
	b.called, b.request = true, request
	return b.session, nil
}

type preparedTerminalSession struct{}

func (*preparedTerminalSession) ID() string { return "session" }
func (*preparedTerminalSession) Identity() port.TerminalSessionIdentity {
	return port.TerminalSessionIdentity{}
}
func (*preparedTerminalSession) State() port.TerminalState               { return port.TerminalRunning }
func (*preparedTerminalSession) Capabilities() []port.TerminalCapability { return nil }
func (*preparedTerminalSession) Send(context.Context, port.TerminalInputSource, string, time.Time) error {
	return nil
}
func (*preparedTerminalSession) ReadScreen(context.Context, int) (string, error) { return "", nil }
func (*preparedTerminalSession) InterruptStep(context.Context) error             { return nil }
func (*preparedTerminalSession) Pause(context.Context) error                     { return nil }
func (*preparedTerminalSession) Resume(context.Context) error                    { return nil }
func (*preparedTerminalSession) Terminate(context.Context, time.Duration) error  { return nil }
