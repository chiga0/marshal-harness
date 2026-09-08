//go:build darwin && arm64

package fixedcontrolplane

import (
	"context"
	"sync"

	"github.com/chiga0/marshal-harness/internal/application"
)

// Run lanes span Begin -> application -> receipt even when verification is
// executing outside the global writer lane. Entries live only while owned or
// awaited; this map is scheduling, never durable Run authority.
type runMutationLanes struct {
	mu      sync.Mutex
	entries map[string]*runMutationLane
}

type runMutationLane struct {
	gate chan struct{}
	refs int
}

func (lanes *runMutationLanes) acquire(ctx context.Context, runID string) (func(), error) {
	if ctx == nil || runID == "" {
		return nil, ErrInvalid
	}
	if ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	lanes.mu.Lock()
	if lanes.entries == nil {
		lanes.entries = make(map[string]*runMutationLane)
	}
	lane := lanes.entries[runID]
	if lane == nil {
		lane = &runMutationLane{gate: make(chan struct{}, 1)}
		lanes.entries[runID] = lane
	}
	lane.refs++
	lanes.mu.Unlock()
	drop := func() {
		lanes.mu.Lock()
		defer lanes.mu.Unlock()
		lane.refs--
		if lane.refs == 0 {
			delete(lanes.entries, runID)
		}
	}
	select {
	case lane.gate <- struct{}{}:
		if ctx.Err() != nil {
			<-lane.gate
			drop()
			return nil, ErrUnavailable
		}
		var once sync.Once
		return func() { once.Do(func() { <-lane.gate; drop() }) }, nil
	case <-ctx.Done():
		drop()
		return nil, ErrUnavailable
	}
}

// The resident writer lane covers delivery Begin and receipt reconciliation,
// plus non-verification application mutations. Run lanes protect the whole
// delivery across the verification execution gap. An application mutex cannot
// protect the Run lease acquired by the delivery store before entering it.
// This is process-local scheduling, never a replacement for durable authority.
func (router *HTTPRouter) acquireMutation(ctx context.Context) error {
	if router == nil || router.mutation == nil || ctx == nil {
		return ErrInvalid
	}
	if ctx.Err() != nil {
		return ErrUnavailable
	}
	select {
	case router.mutation <- struct{}{}:
		if ctx.Err() != nil {
			router.releaseMutation()
			return ErrUnavailable
		}
		return nil
	case <-ctx.Done():
		return ErrUnavailable
	}
}

func (router *HTTPRouter) releaseMutation() { <-router.mutation }

// WithAvailableMutation bridges another authenticated input adapter into this
// exact router's writer lane. It grants no authentication or durable authority.
func (router *HTTPRouter) WithAvailableMutation(ctx context.Context, action func(context.Context) error) error {
	called, err := router.TryBackgroundMutation(ctx, action)
	if err == nil && !called {
		return application.NewError("task-http", application.ReasonCapacityBusy)
	}
	return err
}

// TryBackgroundMutation joins the same writer lane without queuing a timer
// behind a public operation. The callback must not recursively dispatch an
// HTTP mutation. Status/Inspect bypass this lane. Existing HTTP admission
// limits bound public waiters; the callback's caller supplies its deadline.
func (router *HTTPRouter) TryBackgroundMutation(ctx context.Context, advance func(context.Context) error) (bool, error) {
	if router == nil || router.mutation == nil || ctx == nil || advance == nil {
		return false, ErrInvalid
	}
	if ctx.Err() != nil {
		return false, ErrUnavailable
	}
	select {
	case router.mutation <- struct{}{}:
		defer router.releaseMutation()
		if ctx.Err() != nil {
			return false, ErrUnavailable
		}
		return true, advance(ctx)
	default:
		return false, nil
	}
}
