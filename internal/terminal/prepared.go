package terminal

import (
	"context"
	"errors"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

// PreparedStartRequest contains only provider-neutral orchestration fields.
// Provider argv, environment, cwd and identity always come from the Adapter.
type PreparedStartRequest struct {
	StateRoot          string
	LauncherExecutable string
	Title              string
	Description        string
	Now                time.Time
	ExpiresAt          time.Time
}

// PreparedSession keeps the completion gate adjacent to the live session so a
// caller cannot accidentally treat terminal exit or screen text as success.
type PreparedSession struct {
	Session        port.TerminalSession
	CompletionGate port.TerminalCompletionGate
}

// StartPrepared asks the selected Adapter to freeze a native TUI launch and
// forwards it verbatim to a provider-neutral backend. Only the explicitly
// supervised completion gate is accepted until a provider lifecycle protocol
// is implemented and verified.
func StartPrepared(ctx context.Context, adapter port.TerminalLaunchAdapter, backend port.TerminalSessionBackend, record domain.Record, request PreparedStartRequest) (PreparedSession, error) {
	if adapter == nil || backend == nil {
		return PreparedSession{}, errors.New("terminal adapter and backend are required")
	}
	spec, err := adapter.PrepareTerminal(ctx, record)
	if err != nil {
		return PreparedSession{}, err
	}
	if spec.AdapterID == "" || spec.AdapterID != adapter.ID() {
		return PreparedSession{}, errors.New("terminal specification adapter identity mismatch")
	}
	if spec.CompletionGate != port.TerminalCompletionSupervisedConfirmation {
		return PreparedSession{}, errors.New("terminal completion gate is not supported")
	}
	session, err := backend.Start(ctx, port.TerminalStartRequest{
		StateRoot: request.StateRoot, RunID: spec.RunID, AttemptID: spec.AttemptID,
		WorkingDirectory: spec.WorkingDirectory, LauncherExecutable: request.LauncherExecutable,
		Executable: spec.Executable, ExpectedExecutableDigest: spec.ExecutableDigest,
		Arguments: append([]string(nil), spec.Arguments...), Environment: append([]string(nil), spec.Environment...),
		Title: request.Title, Description: request.Description, InitialPrompt: spec.InitialPrompt,
		Now: request.Now, ExpiresAt: request.ExpiresAt,
	})
	if err != nil {
		return PreparedSession{}, err
	}
	return PreparedSession{Session: session, CompletionGate: spec.CompletionGate}, nil
}
