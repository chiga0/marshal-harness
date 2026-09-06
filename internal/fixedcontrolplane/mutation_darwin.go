//go:build darwin && arm64

package fixedcontrolplane

import "context"

// The resident writer lane covers delivery Begin, application mutation and
// receipt reconciliation together. The application's own mutex alone cannot
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
