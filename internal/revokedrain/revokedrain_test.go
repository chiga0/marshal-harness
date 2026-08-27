package revokedrain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func assertError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("expected error, got nil")
		return
	}
	if !strings.HasPrefix(err.Error(), "revokedrain:") {
		t.Errorf("error %q missing revokedrain: prefix", err.Error())
	}
}

func TestNewDrainPolicy_Invalid(t *testing.T) {
	tests := []struct {
		name          string
		drainWindow   time.Duration
		maxExtensions int
	}{
		{"zero window", 0, 0},
		{"negative window", -time.Minute, 0},
		{"negative extensions", time.Minute, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewDrainPolicy(tt.drainWindow, tt.maxExtensions)
			assertError(t, err)
			if !errors.Is(err, ErrInvalidDrainPolicy) {
				t.Errorf("expected ErrInvalidDrainPolicy, got %v", err)
			}
		})
	}
}

func TestNewInstanceGuard_Invalid(t *testing.T) {
	_, err := NewInstanceGuard("", 1)
	assertError(t, err)
	if !errors.Is(err, ErrInvalidRegistration) {
		t.Errorf("expected ErrInvalidRegistration, got %v", err)
	}

	_, err = NewInstanceGuard("reg", 0)
	assertError(t, err)
	if !errors.Is(err, ErrInvalidRegistration) {
		t.Errorf("expected ErrInvalidRegistration, got %v", err)
	}
}

func TestSecurityCriticalRevoke_Immediate(t *testing.T) {
	start := time.Unix(1000, 0)
	g, err := NewInstanceGuard("reg-old", 1)
	if err != nil {
		t.Fatalf("NewInstanceGuard: %v", err)
	}

	if err := g.ApplySecurityCriticalRevoke(RevokeReasonCodeCredentialCompromise, start); err != nil {
		t.Fatalf("ApplySecurityCriticalRevoke: %v", err)
	}

	if !g.DrainDeadline().IsZero() {
		t.Errorf("expected zero drain deadline after security-critical revoke, got %v", g.DrainDeadline())
	}
	assertError(t, g.AcceptNewLease(start))
	if !errors.Is(g.AcceptNewLease(start), ErrRevoked) {
		t.Errorf("expected ErrRevoked for new lease, got %v", g.AcceptNewLease(start))
	}
	assertError(t, g.AcceptInFlightCompletion(start))
	if !errors.Is(g.AcceptInFlightCompletion(start), ErrRevoked) {
		t.Errorf("expected ErrRevoked for in-flight completion, got %v", g.AcceptInFlightCompletion(start))
	}

	ev := g.Events()
	if len(ev) != 3 {
		t.Fatalf("expected 3 events, got %d", len(ev))
	}
	wantOps := []string{"cancel", "generation-bump", "kill"}
	for i, op := range wantOps {
		if ev[i].Operation != op {
			t.Errorf("event %d operation = %q, want %q", i, ev[i].Operation, op)
		}
		if ev[i].Class != DispositionClassSecurityCritical {
			t.Errorf("event %d class = %q, want security-critical", i, ev[i].Class)
		}
		if ev[i].ReasonCode != RevokeReasonCodeCredentialCompromise {
			t.Errorf("event %d reason = %q, want credential-compromise", i, ev[i].ReasonCode)
		}
		if ev[i].Sequence != i+1 {
			t.Errorf("event %d sequence = %d, want %d", i, ev[i].Sequence, i+1)
		}
	}
	if ev[0].Generation != 1 {
		t.Errorf("cancel generation = %d, want 1", ev[0].Generation)
	}
	if ev[1].Generation != 2 || g.Generation() != 2 {
		t.Errorf("generation bump did not reach 2: event=%d guard=%d", ev[1].Generation, g.Generation())
	}
	if ev[2].Generation != 2 {
		t.Errorf("kill generation = %d, want 2", ev[2].Generation)
	}
}

func TestStartUpgrade_StopNew(t *testing.T) {
	start := time.Unix(2000, 0)
	window := 5 * time.Minute
	g, err := NewInstanceGuard("reg-old", 10)
	if err != nil {
		t.Fatalf("NewInstanceGuard: %v", err)
	}
	policy, err := NewDrainPolicy(window, 2)
	if err != nil {
		t.Fatalf("NewDrainPolicy: %v", err)
	}
	if err := g.StartUpgrade("reg-new", policy, start); err != nil {
		t.Fatalf("StartUpgrade: %v", err)
	}

	assertError(t, g.AcceptNewLease(start))
	if !errors.Is(g.AcceptNewLease(start), ErrStopNew) {
		t.Errorf("expected ErrStopNew, got %v", g.AcceptNewLease(start))
	}
}

func TestAcceptInFlightCompletion_InsideDeadline(t *testing.T) {
	start := time.Unix(3000, 0)
	window := 5 * time.Minute
	g, _ := NewInstanceGuard("reg-old", 1)
	policy, _ := NewDrainPolicy(window, 2)
	g.StartUpgrade("reg-new", policy, start)

	if err := g.AcceptInFlightCompletion(start.Add(window - time.Second)); err != nil {
		t.Errorf("expected completion inside deadline to pass, got %v", err)
	}
	if err := g.AcceptInFlightCompletion(start.Add(window)); err != nil {
		t.Errorf("expected completion exactly at deadline to pass, got %v", err)
	}
}

func TestAcceptInFlightCompletion_AfterDeadline_Fences(t *testing.T) {
	start := time.Unix(4000, 0)
	window := 5 * time.Minute
	g, _ := NewInstanceGuard("reg-old", 1)
	policy, _ := NewDrainPolicy(window, 2)
	g.StartUpgrade("reg-new", policy, start)

	firstLate := start.Add(window + 1)
	err := g.AcceptInFlightCompletion(firstLate)
	assertError(t, err)
	if !errors.Is(err, ErrDrainExpired) {
		t.Errorf("expected ErrDrainExpired, got %v", err)
	}

	// fence 后再次请求仍然失败
	err2 := g.AcceptInFlightCompletion(firstLate.Add(time.Minute))
	assertError(t, err2)
	if !errors.Is(err2, ErrFenced) {
		t.Errorf("expected ErrFenced after fence, got %v", err2)
	}

	ev := g.Events()
	if len(ev) < 3 {
		t.Fatalf("expected at least stop-new + cancel + generation-bump, got %d", len(ev))
	}
	if ev[len(ev)-2].Operation != "cancel" {
		t.Errorf("expected cancel before fence, got %q", ev[len(ev)-2].Operation)
	}
	if ev[len(ev)-1].Operation != "generation-bump" {
		t.Errorf("expected generation-bump as last event, got %q", ev[len(ev)-1].Operation)
	}
	if g.Generation() != 2 {
		t.Errorf("expected generation bumped to 2, got %d", g.Generation())
	}
}

func TestExtendDrain_Twice(t *testing.T) {
	start := time.Unix(5000, 0)
	window := 5 * time.Minute
	g, _ := NewInstanceGuard("reg-old", 1)
	policy, _ := NewDrainPolicy(window, 2)
	g.StartUpgrade("reg-new", policy, start)

	if err := g.ExtendDrain(start); err != nil {
		t.Fatalf("first extend: %v", err)
	}
	want := start.Add(2 * window)
	if !g.DrainDeadline().Equal(want) {
		t.Errorf("after one extension deadline = %v, want %v", g.DrainDeadline(), want)
	}

	if err := g.ExtendDrain(start.Add(window)); err != nil {
		t.Fatalf("second extend: %v", err)
	}
	want = start.Add(3 * window)
	if !g.DrainDeadline().Equal(want) {
		t.Errorf("after two extensions deadline = %v, want %v", g.DrainDeadline(), want)
	}
}

func TestExtendDrain_Exhausted(t *testing.T) {
	start := time.Unix(6000, 0)
	window := 5 * time.Minute
	g, _ := NewInstanceGuard("reg-old", 1)
	policy, _ := NewDrainPolicy(window, 1)
	g.StartUpgrade("reg-new", policy, start)

	if err := g.ExtendDrain(start); err != nil {
		t.Fatalf("first extend: %v", err)
	}
	assertError(t, g.ExtendDrain(start.Add(window)))
	if !errors.Is(g.ExtendDrain(start.Add(window)), ErrExtensionsExhausted) {
		t.Errorf("expected ErrExtensionsExhausted, got %v", g.ExtendDrain(start.Add(window)))
	}
	want := start.Add(2 * window)
	if !g.DrainDeadline().Equal(want) {
		t.Errorf("deadline shifted after failed extension: %v", g.DrainDeadline())
	}
}

func TestExtendDrain_AfterFence(t *testing.T) {
	start := time.Unix(7000, 0)
	window := 5 * time.Minute
	g, _ := NewInstanceGuard("reg-old", 1)
	policy, _ := NewDrainPolicy(window, 2)
	g.StartUpgrade("reg-new", policy, start)
	g.AcceptInFlightCompletion(start.Add(window + 1))

	assertError(t, g.ExtendDrain(start.Add(window+2)))
	if !errors.Is(g.ExtendDrain(start.Add(window+2)), ErrFenced) {
		t.Errorf("expected ErrFenced, got %v", g.ExtendDrain(start.Add(window+2)))
	}
}

func TestReactivate_FailsAfterFence(t *testing.T) {
	start := time.Unix(8000, 0)
	window := 5 * time.Minute
	g, _ := NewInstanceGuard("reg-old", 1)
	policy, _ := NewDrainPolicy(window, 2)
	g.StartUpgrade("reg-new", policy, start)
	g.AcceptInFlightCompletion(start.Add(window + 1))

	assertError(t, g.Reactivate("reg-old", start.Add(window+2)))
	if !errors.Is(g.Reactivate("reg-old", start.Add(window+2)), ErrReactivateDenied) {
		t.Errorf("expected ErrReactivateDenied after fence, got %v", g.Reactivate("reg-old", start.Add(window+2)))
	}
}

func TestReactivate_FailsAfterSecurityCritical(t *testing.T) {
	start := time.Unix(9000, 0)
	g, _ := NewInstanceGuard("reg-old", 1)
	g.ApplySecurityCriticalRevoke(RevokeReasonCodeProtocolViolation, start)

	assertError(t, g.Reactivate("reg-old", start))
	if !errors.Is(g.Reactivate("reg-old", start), ErrReactivateDenied) {
		t.Errorf("expected ErrReactivateDenied after revoke, got %v", g.Reactivate("reg-old", start))
	}
}

func TestSetLeaseDigest_Immutable(t *testing.T) {
	g, _ := NewInstanceGuard("reg", 1)
	if err := g.SetLeaseDigest("digest-1"); err != nil {
		t.Fatalf("first SetLeaseDigest: %v", err)
	}
	if g.LeaseDigest() != "digest-1" {
		t.Errorf("lease digest = %q, want digest-1", g.LeaseDigest())
	}
	assertError(t, g.SetLeaseDigest("digest-2"))
	if !errors.Is(g.SetLeaseDigest("digest-2"), ErrDigestImmutable) {
		t.Errorf("expected ErrDigestImmutable, got %v", g.SetLeaseDigest("digest-2"))
	}
}

func TestSecurityCriticalRevoke_PreemptsUpgradeDrain(t *testing.T) {
	start := time.Unix(12000, 0)
	window := 5 * time.Minute
	g, _ := NewInstanceGuard("reg-old", 3)
	policy, _ := NewDrainPolicy(window, 2)
	if err := g.StartUpgrade("reg-new", policy, start); err != nil {
		t.Fatalf("StartUpgrade: %v", err)
	}
	if err := g.ApplySecurityCriticalRevoke(RevokeReasonCodeProtocolViolation, start.Add(time.Minute)); err != nil {
		t.Fatalf("ApplySecurityCriticalRevoke: %v", err)
	}

	if !g.DrainDeadline().IsZero() {
		t.Errorf("security-critical revoke must zero drain window, got %v", g.DrainDeadline())
	}
	if g.Generation() != 4 {
		t.Errorf("expected generation bumped 3 -> 4, got %d", g.Generation())
	}
	if !errors.Is(g.AcceptInFlightCompletion(start.Add(time.Minute)), ErrRevoked) {
		t.Errorf("in-flight completion inside former drain window must fail closed after revoke")
	}
	ev := g.Events()
	if ev[len(ev)-1].Operation != "kill" {
		t.Errorf("last event must be kill, got %q", ev[len(ev)-1].Operation)
	}
}

func TestStartUpgrade_AfterFence(t *testing.T) {
	start := time.Unix(13000, 0)
	window := 5 * time.Minute
	g, _ := NewInstanceGuard("reg-old", 1)
	policy, _ := NewDrainPolicy(window, 0)
	g.StartUpgrade("reg-new", policy, start)
	g.AcceptInFlightCompletion(start.Add(window + 1))

	err := g.StartUpgrade("reg-newer", policy, start.Add(window+2))
	assertError(t, err)
	if !errors.Is(err, ErrFenced) {
		t.Errorf("expected ErrFenced, got %v", err)
	}
}

func TestApplySecurityCriticalRevoke_Double(t *testing.T) {
	start := time.Unix(14000, 0)
	g, _ := NewInstanceGuard("reg", 1)
	if err := g.ApplySecurityCriticalRevoke(RevokeReasonCodeCredentialCompromise, start); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	err := g.ApplySecurityCriticalRevoke(RevokeReasonCodeProtocolViolation, start)
	assertError(t, err)
	if !errors.Is(err, ErrRevoked) {
		t.Errorf("expected ErrRevoked on double revoke, got %v", err)
	}
	if len(g.Events()) != 3 {
		t.Errorf("double revoke must not append events, got %d", len(g.Events()))
	}
	if g.Generation() != 2 {
		t.Errorf("double revoke must not bump generation again, got %d", g.Generation())
	}
}

func TestAcceptNewLease_Active(t *testing.T) {
	start := time.Unix(10000, 0)
	g, _ := NewInstanceGuard("reg", 1)
	if err := g.AcceptNewLease(start); err != nil {
		t.Errorf("expected new lease to be accepted before any revoke/upgrade, got %v", err)
	}
}

func TestApplySecurityCriticalRevoke_InvalidReasonCode(t *testing.T) {
	start := time.Unix(11000, 0)
	g, _ := NewInstanceGuard("reg", 1)
	assertError(t, g.ApplySecurityCriticalRevoke("not-a-reason", start))
	if !errors.Is(g.ApplySecurityCriticalRevoke("not-a-reason", start), ErrInvalidReasonCode) {
		t.Errorf("expected ErrInvalidReasonCode, got %v", g.ApplySecurityCriticalRevoke("not-a-reason", start))
	}
}
