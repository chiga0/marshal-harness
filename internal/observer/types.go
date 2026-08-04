// Package observer defines the integration surface for external observer
// backends (e.g. terminal multiplexers) that can surface run progress.
package observer

import (
	"context"
	"errors"
	"fmt"
	"math"
)

// Phase describes the lifecycle stage of a backend as seen by Probe.
type Phase string

const (
	PhaseNotInstalled Phase = "not-installed"
	PhaseInstalled    Phase = "installed"
	PhaseReachable    Phase = "reachable"
	PhaseAuthorized   Phase = "authorized"
	PhaseReady        Phase = "ready"
)

// Capability names a feature a backend may support.
type Capability string

const (
	CapabilityWorkspaceCreate Capability = "workspace-create"
	CapabilityPaneCreate      Capability = "pane-create"
	CapabilityScreenRead      Capability = "screen-read"
	CapabilityNotify          Capability = "notify"
	CapabilityProgress        Capability = "progress"
	CapabilityReadonlyFollow  Capability = "readonly-follow"
)

// ProbeResult reports the current state of a backend.
type ProbeResult struct {
	BackendID    string
	Phase        Phase
	Executable   string
	Version      string
	AccessMode   string
	Methods      []string
	Capabilities []Capability
	Diagnostic   string
}

// AttachRequest asks a backend to start observing a run attempt.
type AttachRequest struct {
	RunID            string
	AttemptID        string
	Title            string
	Description      string
	WorkingDirectory string
}

// Validate rejects requests without identity.
func (r AttachRequest) Validate() error {
	if r.RunID == "" {
		return errors.New("observer: attach request missing RunID")
	}
	if r.AttemptID == "" {
		return errors.New("observer: attach request missing AttemptID")
	}
	return nil
}

// UpdateRequest pushes incremental state to an attached handle.
type UpdateRequest struct {
	Status       string
	Progress     *float64
	LogLevel     string
	LogMessage   string
	Notification string
}

// Validate rejects out-of-range and non-finite progress values. NaN and
// infinities are rejected explicitly because NaN comparisons would
// otherwise pass both range checks.
func (r UpdateRequest) Validate() error {
	if r.Progress != nil {
		p := *r.Progress
		if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
			return fmt.Errorf("observer: progress %v out of range [0, 1]", p)
		}
	}
	return nil
}

// DetachRequest asks a handle to stop observing.
type DetachRequest struct {
	Reason string
}

// Validate checks the detach request.
func (r DetachRequest) Validate() error { return nil }

// Backend is an observer integration that can be probed and attached.
type Backend interface {
	ID() string
	Probe(ctx context.Context) (ProbeResult, error)
	Attach(ctx context.Context, req AttachRequest) (Handle, error)
}

// Handle is an active observation session for one run attempt.
type Handle interface {
	ID() string
	Update(ctx context.Context, req UpdateRequest) error
	Detach(ctx context.Context, req DetachRequest) error
}
