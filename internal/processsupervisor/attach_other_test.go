//go:build !darwin

package processsupervisor

import (
	"context"
	"errors"
	"testing"
)

// TestWithAttachedFailsClosedOnNonDarwin enforces ADR 0067 §4: the read-only
// Attach primitive is a Darwin ordinary-user contract only. On any other
// platform it must never provide authority and must return ErrUnavailable
// before touching the verifier, control directory, or callback.
func TestWithAttachedFailsClosedOnNonDarwin(t *testing.T) {
	if err := WithAttachedV2(context.Background(), AttachOptionsV2{}, func(*AttachedSessionV2) error { t.Fatal("non-Darwin v2 callback invoked"); return nil }); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("v2 Attach on non-Darwin: %v", err)
	}
	verifier := attachVerifierFunc(func(context.Context, AttachAuthority, func() error) error {
		t.Fatal("non-Darwin WithAttached invoked the owner verifier")
		return ErrConflict
	})
	err := WithAttached(context.Background(), AttachOptions{FixedMarshalPath: "/fixed/bin/marshal", ControlDirectory: nil, Authority: validAttachAuthority(), OwnerVerifier: verifier}, func(*AttachedSession) error {
		t.Fatal("non-Darwin WithAttached invoked the borrower")
		return nil
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("non-Darwin WithAttached = %v, want ErrUnavailable", err)
	}
}
