//go:build darwin && arm64

package fixedcontrolplane

import (
	"context"
	"sync"
)

// TryBackgroundRunMutation never queues a timer behind a public Run operation.
// It shares HTTP's Run lane, preventing overlap even while Verify releases the
// repository writer. The callback must recheck current authority; no transport
// receipt is fabricated for this internal controller operation.
func (router *HTTPRouter) TryBackgroundRunMutation(ctx context.Context, runID string, verification bool, advance func(context.Context) error) (bool, error) {
	if router == nil || router.mutation == nil || ctx == nil || runID == "" || advance == nil {
		return false, ErrInvalid
	}
	release, err := router.runMutations.tryAcquire(ctx, runID)
	if err != nil || release == nil {
		return false, err
	}
	defer release()
	select {
	case router.mutation <- struct{}{}:
		if ctx.Err() != nil {
			router.releaseMutation()
			return false, ErrUnavailable
		}
		if verification {
			router.releaseMutation()
		} else {
			defer router.releaseMutation()
		}
		return true, advance(ctx)
	default:
		return false, nil
	}
}

func (lanes *runMutationLanes) tryAcquire(ctx context.Context, runID string) (func(), error) {
	if ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	lanes.mu.Lock()
	defer lanes.mu.Unlock()
	if lanes.entries == nil {
		lanes.entries = make(map[string]*runMutationLane)
	}
	if lanes.entries[runID] != nil {
		return nil, nil // Existing owners and public waiters take precedence.
	}
	lane := &runMutationLane{gate: make(chan struct{}, 1), refs: 1}
	lane.gate <- struct{}{}
	lanes.entries[runID] = lane
	var once sync.Once
	return func() {
		once.Do(func() {
			lanes.mu.Lock()
			defer lanes.mu.Unlock()
			<-lane.gate
			lane.refs--
			if lane.refs == 0 {
				delete(lanes.entries, runID)
			}
		})
	}, nil
}
