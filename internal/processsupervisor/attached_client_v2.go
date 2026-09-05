package processsupervisor

import (
	"context"
	"os"
	"sync"
)

// AttachOwnerVerifierV2 must hold the exact current repository owner and
// owner-bound ledger successor throughout one synchronous callback. It takes
// the full v2 authority; no generation is discarded at the producer boundary.
type AttachOwnerVerifierV2 interface {
	WithCurrentAttachOwnerV2(context.Context, AttachAuthorityV2, func() error) error
}

type AttachOptionsV2 struct {
	FixedMarshalPath string
	ControlDirectory *os.File
	Authority        AttachAuthorityV2
	OwnerVerifier    AttachOwnerVerifierV2
}

// AttachedSessionV2 is a borrowed capability, not a reconnectable ClientV2.
// Only the observation/outcome can escape; no generic command channel is
// exposed. Core persists the exact preparation after Observation and before
// calling one of the closed continuation methods.
type AttachedSessionV2 struct {
	mu                                              sync.Mutex
	active, observed, violated, attempted, executed bool
	ownerGoroutine                                  uint64
	observation                                     AttachObservationV2
	client                                          *ClientV2
	scope                                           context.Context
	command                                         CommandName
	post                                            SessionAnchorV2
}

func (s *AttachedSessionV2) Observation() (AttachObservationV2, error) {
	if s == nil {
		return AttachObservationV2{}, ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active || s.observed || s.ownerGoroutine == 0 || currentGoroutineID() != s.ownerGoroutine {
		s.violated = true
		return AttachObservationV2{}, ErrConflict
	}
	s.observed = true
	return s.observation, nil
}

func (s *AttachedSessionV2) ExecutePreparedBindAuthority(ctx context.Context, p PreparedCommandV2) (VerifiedCommandOutcomeV2, error) {
	return s.execute(ctx, p, CommandBindAuthority)
}
func (s *AttachedSessionV2) ExecutePreparedInspect(ctx context.Context, p PreparedCommandV2) (VerifiedCommandOutcomeV2, error) {
	return s.execute(ctx, p, CommandInspect)
}
func (s *AttachedSessionV2) ExecutePreparedTerminate(ctx context.Context, p PreparedCommandV2) (VerifiedCommandOutcomeV2, error) {
	return s.execute(ctx, p, CommandTerminate)
}
func (s *AttachedSessionV2) ExecutePreparedCollect(ctx context.Context, p PreparedCommandV2) (VerifiedCommandOutcomeV2, error) {
	return s.execute(ctx, p, CommandCollect)
}
func (s *AttachedSessionV2) ExecutePreparedClose(ctx context.Context, p PreparedCommandV2) (VerifiedCommandOutcomeV2, error) {
	return s.execute(ctx, p, CommandClose)
}

func (s *AttachedSessionV2) execute(ctx context.Context, p PreparedCommandV2, allowed CommandName) (VerifiedCommandOutcomeV2, error) {
	if s == nil {
		return VerifiedCommandOutcomeV2{}, ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active || !s.observed || s.attempted || s.ownerGoroutine == 0 || currentGoroutineID() != s.ownerGoroutine || s.client == nil ||
		p.evidence.Validate() != nil || p.evidence.Command != allowed || p.evidence.PreCommand != s.observation.Response.Authority.PreviousSupervisor {
		s.violated = true
		return VerifiedCommandOutcomeV2{}, ErrConflict
	}
	if allowed == CommandBindAuthority {
		var bind BindAuthorityPayload
		if strictCanonicalDecode(p.request.Payload, &bind) != nil || bind.AuthorityHead != s.observation.Response.Authority.CurrentOwnerBoundFact.AttemptHead {
			s.violated = true
			return VerifiedCommandOutcomeV2{}, ErrConflict
		}
	}
	if ctx == nil || s.scope == nil || ctx.Err() != nil || s.scope.Err() != nil {
		s.violated = true
		return VerifiedCommandOutcomeV2{}, ErrConflict
	}
	s.attempted = true
	commandContext, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.scope, cancel)
	defer func() { stop(); cancel() }()
	outcome, err := s.client.DoPrepared(commandContext, p)
	if err != nil {
		return VerifiedCommandOutcomeV2{}, err
	}
	s.executed, s.command, s.post = true, allowed, outcome.PostCommand
	return outcome, nil
}

func callAttachedBorrowerV2(s *AttachedSessionV2, fn func(*AttachedSessionV2) error) (err error) {
	if s == nil || fn == nil || s.observation.Validate() != nil {
		return ErrInvalid
	}
	s.mu.Lock()
	s.active, s.ownerGoroutine = true, currentGoroutineID()
	s.mu.Unlock()
	defer func() {
		if recover() != nil {
			err = ErrConflict
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.active = false
		s.observation, s.client = AttachObservationV2{}, nil
		if err == nil && (!s.observed || s.violated || s.attempted && !s.executed) {
			err = ErrConflict
		}
	}()
	return fn(s)
}

func withAttachOwnerV2(ctx context.Context, verifier AttachOwnerVerifierV2, authority AttachAuthorityV2, fn func() error) (err error) {
	if ctx == nil || ctx.Err() != nil || verifier == nil || authority.Validate() != nil || fn == nil {
		return ErrInvalid
	}
	var mu sync.Mutex
	called, violated, closed := false, false, false
	owner := currentGoroutineID()
	var callbackErr error
	defer func() {
		mu.Lock()
		closed = true
		mu.Unlock()
		if recover() != nil {
			err = ErrConflict
		}
	}()
	verifierErr := verifier.WithCurrentAttachOwnerV2(ctx, authority, func() error {
		mu.Lock()
		defer mu.Unlock()
		if closed || called || owner == 0 || currentGoroutineID() != owner {
			violated = true
			return ErrConflict
		}
		called = true
		callbackErr = fn()
		return callbackErr
	})
	mu.Lock()
	defer mu.Unlock()
	closed = true
	if !called || violated || verifierErr != nil {
		if callbackErr != nil {
			return callbackErr
		}
		return ErrConflict
	}
	return callbackErr
}
