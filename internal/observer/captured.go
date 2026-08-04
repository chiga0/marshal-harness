package observer

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrDetached is returned by Update on a handle that has been detached.
var ErrDetached = errors.New("observer: handle detached")

// CapturedBackend is a fully in-memory Backend used as a test double and as
// the default when no real observer is configured. It has no file, process
// or network side effects and always probes as ready.
type CapturedBackend struct {
	id string

	mu   sync.Mutex
	next int
}

// NewCapturedBackend returns a CapturedBackend with the standard ID.
func NewCapturedBackend() *CapturedBackend {
	return &CapturedBackend{id: "captured"}
}

var _ Backend = (*CapturedBackend)(nil)

// ID implements Backend.
func (b *CapturedBackend) ID() string { return b.id }

// Probe implements Backend. The captured backend is always ready.
func (b *CapturedBackend) Probe(ctx context.Context) (ProbeResult, error) {
	if err := ctx.Err(); err != nil {
		return ProbeResult{}, err
	}
	return ProbeResult{
		BackendID: b.id,
		Phase:     PhaseReady,
	}, nil
}

// Attach implements Backend. It returns an in-memory no-op Handle that is
// bound to the given run and attempt.
func (b *CapturedBackend) Attach(ctx context.Context, req AttachRequest) (Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.next++
	seq := b.next
	b.mu.Unlock()
	return &capturedHandle{
		id:        fmt.Sprintf("%s:%s:%s#%d", b.id, req.RunID, req.AttemptID, seq),
		runID:     req.RunID,
		attemptID: req.AttemptID,
	}, nil
}

type capturedHandle struct {
	id        string
	runID     string
	attemptID string

	mu       sync.Mutex
	detached bool
}

var _ Handle = (*capturedHandle)(nil)

// ID implements Handle.
func (h *capturedHandle) ID() string { return h.id }

// RunID returns the run this handle is attached to.
func (h *capturedHandle) RunID() string { return h.runID }

// AttemptID returns the attempt this handle is attached to.
func (h *capturedHandle) AttemptID() string { return h.attemptID }

// Update implements Handle. It is a no-op that honours context
// cancellation and reports ErrDetached once detached.
func (h *capturedHandle) Update(ctx context.Context, req UpdateRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.detached {
		return ErrDetached
	}
	return nil
}

// Detach implements Handle. It is idempotent and honours context
// cancellation.
func (h *capturedHandle) Detach(ctx context.Context, req DetachRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.Lock()
	h.detached = true
	h.mu.Unlock()
	return nil
}
