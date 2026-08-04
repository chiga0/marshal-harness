package observer

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
)

func ptr(f float64) *float64 { return &f }

func TestInterfaceConformance(t *testing.T) {
	var _ Backend = (*CapturedBackend)(nil)
	b := NewCapturedBackend()
	h, err := b.Attach(context.Background(), AttachRequest{RunID: "r1", AttemptID: "a1"})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	var _ Handle = h
	if got := b.ID(); got != "captured" {
		t.Errorf("backend ID = %q, want %q", got, "captured")
	}
}

func TestProbeAlwaysReady(t *testing.T) {
	res, err := NewCapturedBackend().Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.Phase != PhaseReady {
		t.Errorf("Phase = %q, want %q", res.Phase, PhaseReady)
	}
	if res.BackendID != "captured" {
		t.Errorf("BackendID = %q, want %q", res.BackendID, "captured")
	}
}

func TestAttachValidation(t *testing.T) {
	b := NewCapturedBackend()
	cases := []struct {
		name string
		req  AttachRequest
	}{
		{"missing run", AttachRequest{AttemptID: "a"}},
		{"missing attempt", AttachRequest{RunID: "r"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := b.Attach(context.Background(), tc.req); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestHandleBoundToRunAndAttempt(t *testing.T) {
	b := NewCapturedBackend()
	h, err := b.Attach(context.Background(), AttachRequest{RunID: "run-7", AttemptID: "att-3"})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	ch := h.(*capturedHandle)
	if ch.RunID() != "run-7" || ch.AttemptID() != "att-3" {
		t.Errorf("handle bound to %s/%s, want run-7/att-3", ch.RunID(), ch.AttemptID())
	}
	h2, err := b.Attach(context.Background(), AttachRequest{RunID: "run-7", AttemptID: "att-3"})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if h.ID() == h2.ID() {
		t.Errorf("two handles share ID %q", h.ID())
	}
}

func TestUpdateValidation(t *testing.T) {
	h := attach(t)
	for _, p := range []float64{-0.1, 1.5, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := h.Update(context.Background(), UpdateRequest{Progress: ptr(p)}); err == nil {
			t.Errorf("progress %v: expected error, got nil", p)
		}
	}
	// Validate directly, to pin the non-finite bypass independently of
	// the handle path.
	for _, p := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := (UpdateRequest{Progress: ptr(p)}).Validate(); err == nil {
			t.Errorf("Validate(progress %v): expected error, got nil", p)
		}
	}
	for _, p := range []float64{0, 0.5, 1} {
		if err := h.Update(context.Background(), UpdateRequest{Progress: ptr(p)}); err != nil {
			t.Errorf("progress %v: %v", p, err)
		}
	}
}

func TestContextCancellation(t *testing.T) {
	b := NewCapturedBackend()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Probe(ctx); err == nil {
		t.Error("Probe: expected error on cancelled context")
	}
	if _, err := b.Attach(ctx, AttachRequest{RunID: "r", AttemptID: "a"}); err == nil {
		t.Error("Attach: expected error on cancelled context")
	}

	h := attach(t)
	if err := h.Update(ctx, UpdateRequest{Status: "ok"}); err == nil {
		t.Error("Update: expected error on cancelled context")
	}
	if err := h.Detach(ctx, DetachRequest{}); err == nil {
		t.Error("Detach: expected error on cancelled context")
	}
}

func TestDetachIdempotentAndBlocksUpdate(t *testing.T) {
	h := attach(t)
	ctx := context.Background()
	if err := h.Detach(ctx, DetachRequest{Reason: "done"}); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	if err := h.Detach(ctx, DetachRequest{Reason: "again"}); err != nil {
		t.Fatalf("second Detach: %v", err)
	}
	if err := h.Update(ctx, UpdateRequest{Status: "late"}); !errors.Is(err, ErrDetached) {
		t.Errorf("Update after detach = %v, want ErrDetached", err)
	}
}

func TestConcurrentAccess(t *testing.T) {
	b := NewCapturedBackend()
	h, err := b.Attach(context.Background(), AttachRequest{RunID: "r", AttemptID: "a"})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = h.Update(context.Background(), UpdateRequest{Status: "run", Progress: ptr(0.5)})
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 20; j++ {
			_ = h.Detach(context.Background(), DetachRequest{Reason: "concurrent"})
		}
	}()
	wg.Wait()
	if err := h.Update(context.Background(), UpdateRequest{}); !errors.Is(err, ErrDetached) {
		t.Errorf("final Update = %v, want ErrDetached", err)
	}
}

func attach(t *testing.T) Handle {
	t.Helper()
	h, err := NewCapturedBackend().Attach(context.Background(), AttachRequest{RunID: "r", AttemptID: "a"})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return h
}
