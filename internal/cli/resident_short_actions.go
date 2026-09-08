package cli

import (
	"context"
	"sync"
	"time"
)

// driveResidentShortActions grants one bounded turn per action in fixed round
// robin order. Buffered ticks cannot let a slow action take its sibling's turn.
// A busy public lane may still skip a turn: this grants scheduling opportunity,
// never permission to bypass a lane, retry an unknown write or advance state.
func driveResidentShortActions(ctx context.Context, ticks <-chan time.Time, actions []func(context.Context) error, report func(error)) {
	if len(actions) == 0 {
		return
	}
	next := 0
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok || ctx.Err() != nil {
				return
			}
			step, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := actions[next](step)
			cancel()
			next = (next + 1) % len(actions)
			if err != nil && ctx.Err() == nil {
				report(err)
			}
		}
	}
}

// newResidentLongAction reserves one in-flight slot. The short turn waits for
// explicit admission or completion, never merely for goroutine creation. The
// caller must signal admission only after acquiring its existing Run lane;
// that lane is retained by advance until it returns. No retry is added here.
func newResidentLongAction(parent context.Context, workers *sync.WaitGroup, timeout time.Duration, advance func(context.Context, func()) error, report func(error)) func(context.Context) error {
	busy := make(chan struct{}, 1)
	return func(turn context.Context) error {
		if err := turn.Err(); err != nil {
			return err
		}
		select {
		case busy <- struct{}{}:
		default:
			return nil
		}
		ctx, cancel := context.WithTimeout(parent, timeout)
		ready := make(chan struct{})
		var once sync.Once
		admitted := func() { once.Do(func() { close(ready) }) }
		// The scheduler remains in workers while adding this child, and it
		// stops adding children before shutdown drains the group.
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer cancel()
			defer func() { <-busy; admitted() }()
			if err := advance(ctx, admitted); err != nil && parent.Err() == nil {
				report(err)
			}
		}()
		select {
		case <-ready:
			return nil
		case <-turn.Done():
			cancel()
			return turn.Err()
		}
	}
}
