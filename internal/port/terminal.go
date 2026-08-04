package port

import (
	"context"
	"time"
)

type TerminalCapability string

const (
	TerminalSessionCreate   TerminalCapability = "session-create"
	TerminalPromptSend      TerminalCapability = "prompt-send"
	TerminalScreenRead      TerminalCapability = "screen-read"
	TerminalInterruptStep   TerminalCapability = "interrupt-step"
	TerminalPauseResume     TerminalCapability = "pause-resume"
	TerminalTerminate       TerminalCapability = "terminate"
	TerminalSessionResume   TerminalCapability = "session-resume"
	TerminalInputProvenance TerminalCapability = "input-provenance"
)

type TerminalState string

const (
	TerminalRunning    TerminalState = "running"
	TerminalPaused     TerminalState = "paused"
	TerminalTerminated TerminalState = "terminated"
)

type TerminalProbeResult struct {
	BackendID    string               `json:"backendId"`
	Available    bool                 `json:"available"`
	Capabilities []TerminalCapability `json:"capabilities"`
	Diagnostic   string               `json:"diagnostic,omitempty"`
}

type TerminalStartRequest struct {
	StateRoot          string
	RunID              string
	AttemptID          string
	WorkingDirectory   string
	LauncherExecutable string
	Executable         string
	Arguments          []string
	Environment        []string
	Title              string
	Description        string
	InitialPrompt      string
	Now                time.Time
	ExpiresAt          time.Time
}

type TerminalInputSource string

const (
	TerminalFrozenPrompt  TerminalInputSource = "frozen-prompt"
	TerminalLeadSteering  TerminalInputSource = "lead-steering"
	TerminalHumanSteering TerminalInputSource = "human-steering"
)

type TerminalSessionIdentity struct {
	RunID     string
	AttemptID string
}

type TerminalSession interface {
	ID() string
	Identity() TerminalSessionIdentity
	State() TerminalState
	Capabilities() []TerminalCapability
	Send(context.Context, TerminalInputSource, string, time.Time) error
	ReadScreen(context.Context, int) (string, error)
	InterruptStep(context.Context) error
	Pause(context.Context) error
	Resume(context.Context) error
	Terminate(context.Context, time.Duration) error
}

type TerminalSessionBackend interface {
	ID() string
	Probe(context.Context) (TerminalProbeResult, error)
	Start(context.Context, TerminalStartRequest) (TerminalSession, error)
}
