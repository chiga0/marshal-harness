// Package terminal implements provider-neutral native PTY TerminalSessions.
package terminal

import (
	"errors"

	"github.com/chiga0/marshal-harness/internal/port"
)

type Capability = port.TerminalCapability

const (
	CapabilitySessionCreate   = port.TerminalSessionCreate
	CapabilityPromptSend      = port.TerminalPromptSend
	CapabilityScreenRead      = port.TerminalScreenRead
	CapabilityInterruptStep   = port.TerminalInterruptStep
	CapabilityPauseResume     = port.TerminalPauseResume
	CapabilityTerminate       = port.TerminalTerminate
	CapabilitySessionResume   = port.TerminalSessionResume
	CapabilityInputProvenance = port.TerminalInputProvenance
)

type State = port.TerminalState

const (
	StateRunning    = port.TerminalRunning
	StatePaused     = port.TerminalPaused
	StateTerminated = port.TerminalTerminated
)

var (
	ErrUnavailable      = errors.New("terminal session backend unavailable")
	ErrUnsupported      = errors.New("terminal session operation unsupported")
	ErrInvalidRequest   = errors.New("invalid terminal session request")
	ErrSessionState     = errors.New("terminal session state conflict")
	ErrAmbiguousProcess = errors.New("terminal session process identity is ambiguous")
)

type ProbeResult = port.TerminalProbeResult
type StartRequest = port.TerminalStartRequest
type InputSource = port.TerminalInputSource

const (
	InputSourceFrozenPrompt  = port.TerminalFrozenPrompt
	InputSourceLeadSteering  = port.TerminalLeadSteering
	InputSourceHumanSteering = port.TerminalHumanSteering
)

type Session = port.TerminalSession
type Backend = port.TerminalSessionBackend
