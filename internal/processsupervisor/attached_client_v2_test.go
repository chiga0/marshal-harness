package processsupervisor

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type attachOwnerVerifierFuncV2 func(context.Context, AttachAuthorityV2, func() error) error

func (f attachOwnerVerifierFuncV2) WithCurrentAttachOwnerV2(ctx context.Context, a AttachAuthorityV2, fn func() error) error {
	return f(ctx, a, fn)
}

func TestAttachedV2OwnerCallbackMustBeSynchronousAndOnce(t *testing.T) {
	a := testAttachRequestV2(t).Authority
	for name, verifier := range map[string]attachOwnerVerifierFuncV2{
		"missing": func(context.Context, AttachAuthorityV2, func() error) error { return nil },
		"twice":   func(_ context.Context, _ AttachAuthorityV2, fn func() error) error { _ = fn(); _ = fn(); return nil },
		"cross-goroutine": func(_ context.Context, _ AttachAuthorityV2, fn func() error) error {
			done := make(chan error, 1)
			go func() { done <- fn() }()
			<-done
			return nil
		},
		"panic": func(context.Context, AttachAuthorityV2, func() error) error { panic("not diagnostic authority") },
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			if withAttachOwnerV2(context.Background(), verifier, a, func() error { calls++; return nil }) == nil {
				t.Fatal("invalid owner admitted")
			}
			if name != "twice" && calls != 0 {
				t.Fatal("invalid owner performed observation")
			}
		})
	}
	var saved func() error
	late := attachOwnerVerifierFuncV2(func(_ context.Context, _ AttachAuthorityV2, fn func() error) error { saved = fn; return nil })
	if withAttachOwnerV2(context.Background(), late, a, func() error { t.Fatal("late owner observed"); return nil }) == nil || saved() == nil {
		t.Fatal("escaped owner remained usable")
	}
	want := errors.New("borrower failed")
	exact := attachOwnerVerifierFuncV2(func(_ context.Context, got AttachAuthorityV2, fn func() error) error {
		if got != a {
			t.Fatal("generation dropped")
		}
		_ = fn()
		return nil
	})
	if !errors.Is(withAttachOwnerV2(context.Background(), exact, a, func() error { return want }), want) {
		t.Fatal("callback error swallowed")
	}
}

func TestAttachedV2CapabilityCannotEscapeOrGainGenericCommands(t *testing.T) {
	request := testAttachRequestV2(t)
	response, peer := testAttachResponseV2(t, request)
	observation := AttachObservationV2{Response: response, Peer: peer}
	var saved *AttachedSessionV2
	if err := callAttachedBorrowerV2(&AttachedSessionV2{observation: observation}, func(s *AttachedSessionV2) error {
		saved = s
		got, err := s.Observation()
		if err != nil || !reflect.DeepEqual(got, observation) {
			t.Fatal("observation drift")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := saved.Observation(); err == nil {
		t.Fatal("escaped observation")
	}
	for name, fn := range map[string]func(*AttachedSessionV2) error{
		"missing": func(*AttachedSessionV2) error { return nil },
		"twice":   func(s *AttachedSessionV2) error { _, _ = s.Observation(); _, _ = s.Observation(); return nil },
		"cross-goroutine": func(s *AttachedSessionV2) error {
			done := make(chan struct{})
			go func() { _, _ = s.Observation(); close(done) }()
			<-done
			return nil
		},
		"panic": func(*AttachedSessionV2) error { panic("no escape") },
		"unobserved-command": func(s *AttachedSessionV2) error {
			_, _ = s.ExecutePreparedBindAuthority(context.Background(), PreparedCommandV2{})
			return nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			if callAttachedBorrowerV2(&AttachedSessionV2{observation: observation}, fn) == nil {
				t.Fatal("invalid borrower admitted")
			}
		})
	}
	methods := reflect.TypeOf((*AttachedSessionV2)(nil))
	want := map[string]bool{"Observation": true, "ExecutePreparedBindAuthority": true, "ExecutePreparedInspect": true, "ExecutePreparedCollect": true, "ExecutePreparedClose": true}
	if methods.NumMethod() != len(want) {
		t.Fatal("unexpected exported capability")
	}
	for i := 0; i < methods.NumMethod(); i++ {
		if !want[methods.Method(i).Name] {
			t.Fatal("generic capability exposed")
		}
	}
}
