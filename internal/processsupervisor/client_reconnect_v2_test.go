package processsupervisor

import (
	"bytes"
	"context"
	"net"
	"os"
	"testing"
	"time"
)

func reconnectPlanForAnchorV2(anchor SessionAnchorV2) ReconnectPlan {
	b := anchor.Binding
	return ReconnectPlan{PreviousOwnerEpoch: b.OwnerEpoch, OwnerEpoch: b.OwnerEpoch + 1, PreviousAuthorityHead: b.CurrentAuthorityHead,
		CurrentAuthorityHead: digest("v2-new-core-head"), ControlOwnerAcquired: digest("v2-new-core-acquired")}
}

func TestClientV2ReconnectClassificationsAndImmutableRecovery(t *testing.T) {
	for _, mode := range []string{"no-pending", "no-intent", "intent-only", "committed", "committed-expired"} {
		t.Run(mode, func(t *testing.T) {
			session, mechanics, path := newTestSessionV2(t)
			defer session.journal.close()
			session.core.now = time.Now
			bindTestSessionV2(t, session)
			anchor := testAnchorV2(session)
			var pending *PreparedCommandV2
			if mode != "no-pending" {
				payload := validSpawnPayload()
				payload.LaunchAuthorizedFactDigest, payload.SupervisorStartedFactDigest = session.core.launchFact, session.core.supervisorStartedFact
				prepared, err := PrepareCommandV2(anchor, clientOptionsV2(anchor, CommandSpawn, "recovery-spawn"), payload)
				if err != nil {
					t.Fatal(err)
				}
				pending = &prepared
			}
			if mode == "intent-only" {
				projection, _, err := projectRequestV2(pending.request)
				if err != nil {
					t.Fatal(err)
				}
				record := session.journalBase()
				record.Kind, record.Request = journalCommandIntent, &projection
				if _, err := session.journal.append(record); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "committed" || mode == "committed-expired" {
				if _, err := session.handle(mustCanonical(pending.request)); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "committed-expired" {
				session.core.now = func() time.Time { return time.Now().Add(time.Hour) }
			}
			state := session.journal.recoverySnapshot("recovery-spawn")
			if err := validateReconnectJournalV2(state, anchor, pending); err != nil {
				t.Fatalf("preflight: %v", err)
			}
			wrong := anchor
			wrong.Binding.JournalHead = digest("different-A0")
			if validateReconnectJournalV2(state, wrong, pending) == nil {
				t.Fatal("wrong A0 passed journal preflight")
			}
			if _, err := prepareReconnectRequestV2(anchor, reconnectPlanForAnchorV2(anchor), pending, "wrong-nonce", validBootstrapV2().Core); err == nil {
				t.Fatal("wrong nonce admitted")
			}
			plan := reconnectPlanForAnchorV2(anchor)
			core := validBootstrapV2().Core
			request, err := prepareReconnectRequestV2(anchor, plan, pending, validBootstrapV2().SessionNonce, core)
			if err != nil {
				t.Fatal(err)
			}
			result := session.reconnectAttempt(request, core)
			if result.err != nil {
				t.Fatal(result.err)
			}
			handshake := testHandshakeV2(testAnchorV2(session))
			handshake.Reconciliation, handshake.ReplayedResponse = result.resolution.State, result.resolution.Response
			stream, other := net.Pipe()
			defer stream.Close()
			defer other.Close()
			codec, _ := NewProtocolCodec(stream)
			client, err := newReconnectedClientV2(stream, codec, handshake, anchor, plan, pending, core)
			if err != nil {
				t.Fatal(err)
			}
			recovery, ok := client.Recovery()
			if !ok || recovery.Previous != anchor || recovery.Current != client.Anchor() || recovery.Current.Binding.OwnerEpoch != plan.OwnerEpoch || recovery.Plan != plan {
				t.Fatal("lost recovery binding")
			}
			wantCalls := 1
			if mode == "intent-only" || mode == "no-pending" {
				wantCalls = 0
			}
			if mechanics.calls != wantCalls {
				t.Fatalf("calls=%d", mechanics.calls)
			}
			if mode == "intent-only" {
				if !recovery.MechanicsLocked || recovery.ReplayedOutcome != nil || recovery.Pending == nil {
					t.Fatal("pending intent treated as success")
				}
				if _, err := client.DoPrepared(context.Background(), *pending); err == nil {
					t.Fatal("intervention client executed")
				}
			} else if mode != "no-pending" {
				if recovery.ReplayedOutcome == nil || recovery.ReplayedOutcome.Preparation != pending.evidence || recovery.ReplayedOutcome.PostCommand.Binding.OwnerEpoch != anchor.Binding.OwnerEpoch {
					t.Fatal("A0 overwritten by successor owner")
				}
				// Losing this handshake again must preserve the original A0 and
				// replay exactly the same receipt without a second mechanics call.
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				secondPlan := plan
				secondPlan.PreviousOwnerEpoch, secondPlan.OwnerEpoch = plan.OwnerEpoch, plan.OwnerEpoch+1
				secondPlan.PreviousAuthorityHead, secondPlan.CurrentAuthorityHead = plan.CurrentAuthorityHead, digest("third-core-head")
				secondPlan.ControlOwnerAcquired = digest("third-core-acquired")
				secondRequest, err := prepareReconnectRequestV2(anchor, secondPlan, pending, validBootstrapV2().SessionNonce, core)
				if err != nil {
					t.Fatal(err)
				}
				second := session.reconnectAttempt(secondRequest, core)
				if second.err != nil {
					t.Fatal(second.err)
				}
				hs := testHandshakeV2(testAnchorV2(session))
				hs.Reconciliation, hs.ReplayedResponse = second.resolution.State, second.resolution.Response
				r, err := validateReconnectHandshakeV2(hs, anchor, secondPlan, pending, core)
				after, readErr := os.ReadFile(path)
				if err != nil || readErr != nil || !bytes.Equal(before, after) || mechanics.calls != 1 || r.ReplayedOutcome.ReceiptDigest != recovery.ReplayedOutcome.ReceiptDigest {
					t.Fatal("second recovery changed committed effect")
				}
			}
			if recovery.Pending != nil {
				recovery.Pending.EvidenceDigest = digest("changed")
			}
			if recovery.ReplayedOutcome != nil {
				recovery.ReplayedOutcome.ProcessReport.ObserverIdentity = "changed"
			}
			again, _ := client.Recovery()
			if again.Pending != nil && again.Pending.EvidenceDigest != pending.evidence.EvidenceDigest || again.ReplayedOutcome != nil && again.ReplayedOutcome.ProcessReport.ObserverIdentity != observerIdentityV2 {
				t.Fatal("recovery value aliases client")
			}
			for name, change := range map[string]func(*HandshakeResponseV2){
				"generation":     func(h *HandshakeResponseV2) { h.ProtocolRevision = ProtocolRevision },
				"journal":        func(h *HandshakeResponseV2) { h.JournalHead = digest("forged") },
				"owner":          func(h *HandshakeResponseV2) { h.OwnerEpoch++ },
				"peer":           func(h *HandshakeResponseV2) { h.SupervisorProcess.PID++ },
				"classification": func(h *HandshakeResponseV2) { h.Reconciliation = "invented" },
			} {
				t.Run(name, func(t *testing.T) {
					changed := handshake
					change(&changed)
					if _, err := validateReconnectHandshakeV2(changed, anchor, plan, pending, core); err == nil {
						t.Fatal("forged recovery accepted")
					}
				})
			}
		})
	}
}

func TestClientV2ReconnectCommittedBindKeepsCommandAndOwnerHeadsSeparate(t *testing.T) {
	session, _, _ := newTestSessionV2(t)
	defer session.journal.close()
	session.core.now = time.Now
	anchor := testAnchorV2(session)
	payload := validBindPayloadForAnchorV2(anchor)
	pending, err := PrepareCommandV2(anchor, clientOptionsV2(anchor, CommandBindAuthority, "lost-bind"), payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.handle(mustCanonical(pending.request)); err != nil {
		t.Fatal(err)
	}
	plan := reconnectPlanForAnchorV2(anchor)
	plan.PreviousAuthorityHead = payload.AuthorityHead
	core := validBootstrapV2().Core
	request, err := prepareReconnectRequestV2(anchor, plan, &pending, validBootstrapV2().SessionNonce, core)
	if err != nil {
		t.Fatal(err)
	}
	result := session.reconnectAttempt(request, core)
	if result.err != nil {
		t.Fatal(result.err)
	}
	hs := testHandshakeV2(testAnchorV2(session))
	hs.Reconciliation, hs.ReplayedResponse = result.resolution.State, result.resolution.Response
	recovery, err := validateReconnectHandshakeV2(hs, anchor, plan, &pending, core)
	if err != nil || recovery.ReplayedOutcome == nil || recovery.ReplayedOutcome.PostCommand.Binding.CurrentAuthorityHead != payload.AuthorityHead || recovery.Current.Binding.CurrentAuthorityHead != plan.CurrentAuthorityHead {
		t.Fatalf("bind recovery: %v", err)
	}
}
