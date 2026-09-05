//go:build darwin

package processsupervisor

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAttachedClientV2SameConnectionPreparedBindAndNoEscapedCapability(t *testing.T) {
	for _, name := range []string{"read-only", "prepared-bind", "wrong-successor", "wrong-method", "cross-goroutine", "canceled-command"} {
		command := name == "prepared-bind"
		t.Run(name, func(t *testing.T) {
			h, m, self, request := newAttachV2WireFixture(t)
			directory, err := os.Open(h.root)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			held, callbacks := false, 0
			verifier := attachOwnerVerifierFuncV2(func(_ context.Context, a AttachAuthorityV2, fn func() error) error {
				if a != request.Authority {
					t.Fatal("owner identity drift")
				}
				held = true
				defer func() { held = false }()
				return fn()
			})
			options := AttachOptionsV2{FixedMarshalPath: self.Binary.CanonicalPath, ControlDirectory: directory, Authority: request.Authority, OwnerVerifier: verifier}
			var saved *AttachedSessionV2
			err = withAttachedV2(context.Background(), options, func(s *AttachedSessionV2) error {
				if !held {
					t.Fatal("owner released before callback")
				}
				callbacks++
				saved = s
				observation, err := s.Observation()
				if err != nil || observation.Validate() != nil {
					t.Fatal("invalid observation")
				}
				if name == "read-only" {
					return nil
				}
				a := request.Authority.PreviousSupervisor
				h.session.core.mu.Lock()
				startedFact := h.session.core.supervisorStartedFact
				h.session.core.mu.Unlock()
				successor := request.Authority.CurrentOwnerBoundFact.AttemptHead
				if name == "wrong-successor" {
					successor = digest("not-owner-bound")
				}
				prepared, err := PrepareCommandV2(a, CommandOptions{Command: CommandBindAuthority, CommandID: "borrowed-bind-v2", Sequence: a.Binding.CommandSequence + 1,
					PreviousCommandDigest: a.Binding.CommandHead, CurrentAuthorityHead: a.Binding.CurrentAuthorityHead, Deadline: time.Now().UTC().Add(20 * time.Second)},
					BindAuthorityPayload{SupervisorStartedFactDigest: startedFact, OwnerEpoch: a.Binding.OwnerEpoch, PreviousAuthorityHead: a.Binding.CurrentAuthorityHead, AuthorityHead: successor})
				if err != nil {
					t.Fatal(err)
				}
				if name == "wrong-method" {
					_, _ = s.ExecutePreparedCollect(context.Background(), prepared)
					return nil
				}
				if name == "cross-goroutine" {
					done := make(chan struct{})
					go func() { _, _ = s.ExecutePreparedBindAuthority(context.Background(), prepared); close(done) }()
					<-done
					return nil
				}
				commandContext, cancel := context.WithCancel(context.Background())
				defer cancel()
				if name == "canceled-command" {
					cancel()
				}
				outcome, err := s.ExecutePreparedBindAuthority(commandContext, prepared)
				if name != "prepared-bind" {
					if err == nil {
						t.Fatal("invalid continuation sent")
					}
					return nil
				}
				if err != nil || outcome.Validate() != nil {
					t.Fatalf("outcome: %v", err)
				}
				return nil
			}, func(string) (CoreIdentity, error) {
				if !held {
					t.Fatal("unheld core observation")
				}
				return request.Core, nil
			},
				func(*net.UnixConn) (CoreIdentity, error) { return self, nil },
				func(ControlDirectoryIdentity) (string, error) { return filepath.Join(h.root, controlSocket), nil })
			wantFailure := name != "read-only" && name != "prepared-bind"
			if (err != nil) != wantFailure || callbacks != 1 || held {
				t.Fatalf("Attach: %v callbacks=%d", err, callbacks)
			}
			if _, err := saved.Observation(); err == nil {
				t.Fatal("saved capability survived callback")
			}
			h.session.core.mu.Lock()
			defer h.session.core.mu.Unlock()
			want := request.Authority.PreviousSupervisor.Binding.CommandSequence
			if command {
				want++
			}
			if h.session.core.commandSequence != want || m.calls != 1 {
				t.Fatal("Attach repeated child effect or command")
			}
		})
	}
}
