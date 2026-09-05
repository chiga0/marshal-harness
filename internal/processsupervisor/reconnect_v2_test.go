package processsupervisor

import (
	"bytes"
	"os"
	"testing"
	"time"
)

func reconnectForSessionV2(session *sessionV2, pending *requestV2) reconnectRequestV2 {
	sequence, head, _ := session.journal.checkpoint()
	return reconnectRequestV2{SchemaVersion: reconnectSchemaV2, ProtocolRevision: protocolRevisionV2,
		LaunchChildProtocolRevision: launchChildProtocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2,
		SessionID: session.core.sessionID, SessionNonce: validBootstrapV2().SessionNonce,
		PreviousOwnerEpoch: session.core.ownerEpoch, OwnerEpoch: session.core.ownerEpoch + 1,
		PreviousAuthorityHead: session.core.authorityHead, CurrentAuthorityHead: digest("reconnect-v2-head"),
		ControlOwnerAcquiredFactDigest: digest("reconnect-owner-fact"), Core: validBootstrapV2().Core,
		LastOwnerEpoch: session.core.ownerEpoch, LastAuthorityHead: session.core.authorityHead,
		LastCommandSequence: session.core.commandSequence, LastCommandHead: session.core.commandHead,
		LastJournalSequence: sequence, LastJournalHead: head, PendingRequest: pending}
}

func spawnRequestForSessionV2(t *testing.T, session *sessionV2) requestV2 {
	spawn := validSpawnPayload()
	spawn.LaunchAuthorizedFactDigest, spawn.SupervisorStartedFactDigest = session.core.launchFact, session.core.supervisorStartedFact
	return sessionRequestV2(t, session, CommandSpawn, "reconnect-spawn", spawn)
}

func TestReconnectV2ResponseLossAndTwoCoreRestarts(t *testing.T) {
	for _, beforeReconnect := range []bool{false, true} {
		name := "no-intent"
		if beforeReconnect {
			name = "committed-response-lost"
		}
		t.Run(name, func(t *testing.T) {
			session, mechanics, path := newTestSessionV2(t)
			defer session.journal.close()
			bindTestSessionV2(t, session)
			pending := spawnRequestForSessionV2(t, session)
			request := reconnectForSessionV2(session, &pending)
			if beforeReconnect {
				if _, err := session.handle(mustCanonical(pending)); err != nil {
					t.Fatal(err)
				}
			}
			first := session.reconnectAttempt(request, request.Core)
			wantState, wantDisposition := ReconciliationUnchanged, reconnectResolvedAfterMechanics
			if beforeReconnect {
				wantState, wantDisposition = ReconciliationReceiptCommitted, reconnectResolvedWithoutMechanics
			}
			if first.err != nil || first.resolution.State != wantState || first.disposition != wantDisposition || first.resolution.Response == nil || mechanics.calls != 1 {
				t.Fatalf("first: %+v calls=%d", first, mechanics.calls)
			}
			responseBytes := mustCanonical(*first.resolution.Response)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// The new Core lost the first handshake too. Its durable A0 remains
			// unchanged while the authenticated live owner has advanced.
			second := request
			second.PreviousOwnerEpoch, second.OwnerEpoch = request.OwnerEpoch, request.OwnerEpoch+1
			second.PreviousAuthorityHead, second.CurrentAuthorityHead = request.CurrentAuthorityHead, digest("second-reconnect-v2-head")
			second.ControlOwnerAcquiredFactDigest = digest("second-owner-fact")
			result := session.reconnectAttempt(second, second.Core)
			if result.err != nil || result.resolution.State != ReconciliationReceiptCommitted || result.disposition != reconnectResolvedWithoutMechanics ||
				result.resolution.Response == nil || !bytes.Equal(responseBytes, mustCanonical(*result.resolution.Response)) || mechanics.calls != 1 {
				t.Fatalf("second: %+v calls=%d", result, mechanics.calls)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("receipt replay mutated journal")
			}
			// A newly admitted command uses the successor owner in its journal
			// base, never the old Core's A0 or a synthesized v1 anchor.
			resume := sessionRequestV2(t, session, CommandResume, "after-reconnect", ResumePayload{
				ProcessStartedFactDigest: digest("resumed-process-fact")})
			if _, err := session.handle(mustCanonical(resume)); err != nil || mechanics.calls != 2 {
				t.Fatalf("successor command: %v calls=%d", err, mechanics.calls)
			}
			record, ok := session.journal.receipt(resume.CommandID)
			if !ok || record.OwnerEpoch != second.OwnerEpoch || record.CurrentAuthorityHead != second.CurrentAuthorityHead {
				t.Fatal("successor owner not bound to new journal command")
			}
		})
	}
}

func TestReconnectV2PendingIntentNeverReexecutes(t *testing.T) {
	session, mechanics, path := newTestSessionV2(t)
	defer session.journal.close()
	bindTestSessionV2(t, session)
	pending := spawnRequestForSessionV2(t, session)
	request := reconnectForSessionV2(session, &pending)
	projection, _, err := projectRequestV2(pending)
	if err != nil {
		t.Fatal(err)
	}
	intent := session.journalBase()
	intent.Kind, intent.Request = journalCommandIntent, &projection
	if _, err := session.journal.append(intent); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	result := session.reconnectAttempt(request, request.Core)
	if result.err != nil || result.disposition != reconnectResolvedWithoutMechanics || result.resolution.State != ReconciliationIntentPending ||
		result.resolution.Response != nil || mechanics.calls != 0 || session.core.state != sessionIntervention {
		t.Fatalf("pending: %+v calls=%d state=%v", result, mechanics.calls, session.core.state)
	}
	if _, err := session.handle(mustCanonical(pending)); err == nil || mechanics.calls != 0 {
		t.Fatal("pending intent re-executed")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("pending classification changed evidence")
	}
}

func TestReconnectV2UnchangedWithoutPendingIsReadOnly(t *testing.T) {
	session, mechanics, path := newTestSessionV2(t)
	defer session.journal.close()
	bindTestSessionV2(t, session)
	request := reconnectForSessionV2(session, nil)
	before, _ := os.ReadFile(path)
	result := session.reconnectAttempt(request, request.Core)
	after, _ := os.ReadFile(path)
	if result.err != nil || result.resolution.State != ReconciliationUnchanged || result.resolution.Response != nil ||
		result.disposition != reconnectResolvedWithoutMechanics || mechanics.calls != 0 || !bytes.Equal(before, after) {
		t.Fatalf("unchanged reconnect: %+v", result)
	}
}

func TestReconnectV2CommittedExpiryAndDifferentDigest(t *testing.T) {
	session, mechanics, path := newTestSessionV2(t)
	defer session.journal.close()
	bindTestSessionV2(t, session)
	pending := spawnRequestForSessionV2(t, session)
	request := reconnectForSessionV2(session, &pending)
	response, err := session.handle(mustCanonical(pending))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	changed := pending
	changed.Deadline = session.core.now().Add(10 * time.Second).Format(time.RFC3339Nano)
	sealRequestV2(t, &changed)
	forged := request
	forged.PendingRequest = &changed
	if result := session.reconnectAttempt(forged, forged.Core); result.err == nil || result.disposition != reconnectRejectedBeforeMechanics {
		t.Fatalf("different digest reused command ID: %+v", result)
	}
	expiredNow := session.core.now().Add(time.Hour)
	session.core.now = func() time.Time { return expiredNow }
	result := session.reconnectAttempt(request, request.Core)
	if result.err != nil || result.resolution.Response == nil || !bytes.Equal(mustCanonical(response), mustCanonical(*result.resolution.Response)) || mechanics.calls != 1 {
		t.Fatalf("expired committed receipt not replayed: %+v", result)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("expiry/digest check rewrote journal")
	}
}

func TestReconnectV2RejectsForgedAnchorsBeforeEffects(t *testing.T) {
	for name, change := range map[string]func(*reconnectRequestV2){
		"v1-schema":     func(r *reconnectRequestV2) { r.SchemaVersion = ReconnectSchema },
		"v1-protocol":   func(r *reconnectRequestV2) { r.ProtocolRevision = ProtocolRevision },
		"v1-mechanics":  func(r *reconnectRequestV2) { r.MechanicsIdentity = "darwin-ptrace-exec-stop/v1" },
		"nonce":         func(r *reconnectRequestV2) { r.SessionNonce = string(bytes.Repeat([]byte("f"), 64)) },
		"owner":         func(r *reconnectRequestV2) { r.PreviousOwnerEpoch++ },
		"previous-head": func(r *reconnectRequestV2) { r.PreviousAuthorityHead = digest("wrong") },
		"a0-journal":    func(r *reconnectRequestV2) { r.LastJournalHead = JournalGenesisDigest },
		"a0-command":    func(r *reconnectRequestV2) { r.LastCommandHead = CommandGenesisDigest },
		"a0-owner":      func(r *reconnectRequestV2) { r.LastOwnerEpoch = r.OwnerEpoch },
		"peer-pid":      func(r *reconnectRequestV2) { r.Core.Process.PID++ },
	} {
		t.Run(name, func(t *testing.T) {
			session, mechanics, path := newTestSessionV2(t)
			defer session.journal.close()
			bindTestSessionV2(t, session)
			pending := spawnRequestForSessionV2(t, session)
			request := reconnectForSessionV2(session, &pending)
			peer := request.Core
			change(&request)
			before, _ := os.ReadFile(path)
			owner, head := session.core.ownerEpoch, session.core.authorityHead
			result := session.reconnectAttempt(request, peer)
			after, _ := os.ReadFile(path)
			if result.err == nil || result.disposition != reconnectRejectedBeforeMechanics || mechanics.calls != 0 || !bytes.Equal(before, after) || session.core.ownerEpoch != owner || session.core.authorityHead != head {
				t.Fatalf("forgery admitted: %+v", result)
			}
		})
	}
}

func TestReconnectV2FailureAfterReplayNeverClaimsNoEffect(t *testing.T) {
	for _, invalidReport := range []bool{false, true} {
		name := "expired-request"
		if invalidReport {
			name = "invalid-mechanics-report"
		}
		t.Run(name, func(t *testing.T) {
			session, mechanics, _ := newTestSessionV2(t)
			defer session.journal.close()
			bindTestSessionV2(t, session)
			pending := spawnRequestForSessionV2(t, session)
			if invalidReport {
				mechanics.legacy = true
			} else {
				pending.Deadline = session.core.now().Add(-time.Second).Format(time.RFC3339Nano)
				sealRequestV2(t, &pending)
			}
			request := reconnectForSessionV2(session, &pending)
			result := session.reconnectAttempt(request, request.Core)
			if result.err == nil || result.disposition != reconnectFailedAfterMechanics || result.resolution.Response != nil || session.core.state != sessionIntervention {
				t.Fatalf("unsafe recovery result: %+v", result)
			}
			wantCalls := 0
			if invalidReport {
				wantCalls = 1
			}
			if mechanics.calls != wantCalls {
				t.Fatalf("calls=%d", mechanics.calls)
			}
			if retry := session.reconnectAttempt(request, request.Core); retry.err == nil || mechanics.calls != wantCalls {
				t.Fatal("unresolved replay retried")
			}
		})
	}
}
