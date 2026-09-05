package processsupervisor

import (
	"bytes"
	"context"
	"net"
	"reflect"
	"testing"
	"time"
)

func testAnchorV2(session *sessionV2) SessionAnchorV2 {
	b := productionTestAnchor()
	b.SessionID, b.SessionNonceDigest, b.Authority = session.core.sessionID, session.core.nonceDigest, session.core.authority
	b.OwnerEpoch, b.CurrentAuthorityHead = session.core.ownerEpoch, session.core.authorityHead
	b.CommandSequence, b.CommandHead = session.core.commandSequence, session.core.commandHead
	b.JournalSequence, b.JournalHead, _ = session.journal.checkpoint()
	return SessionAnchorV2{Generation: DormantV2ProtocolContract(), Binding: b, ControlDirectory: validBootstrapV2().ControlDirectoryIdentity}
}

func testHandshakeV2(a SessionAnchorV2) HandshakeResponseV2 {
	b := a.Binding
	return handshakeResponseV2{SchemaVersion: handshakeSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2,
		Status: "ok", ReasonCode: "process-supervisor-ready", SessionID: b.SessionID, SessionNonceDigest: b.SessionNonceDigest, OwnerEpoch: b.OwnerEpoch, CurrentAuthorityHead: b.CurrentAuthorityHead,
		CommandSequence: b.CommandSequence, CommandHead: b.CommandHead, JournalSequence: b.JournalSequence, JournalHead: b.JournalHead, ObserverIdentity: observerIdentityV2,
		ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), SupervisorProcess: validBootstrapV2().Core.Process, SupervisorBinary: b.FixedBinary, ControlSocket: b.ControlSocket, ControlFiles: b.ControlFiles}
}

func TestV2PreparedEvidenceCannotLoseAnyGenerationField(t *testing.T) {
	session, _, _ := newTestSessionV2(t)
	defer session.journal.close()
	anchor := testAnchorV2(session)
	options := CommandOptions{Command: CommandSpawn, CommandID: "prepared-v2", Sequence: 1, PreviousCommandDigest: anchor.Binding.CommandHead, CurrentAuthorityHead: anchor.Binding.CurrentAuthorityHead, Deadline: time.Now().UTC().Add(20 * time.Second)}
	payload := validSpawnPayload()
	prepared, err := PrepareCommandV2(anchor, options, payload)
	if err != nil {
		t.Fatal(err)
	}
	evidence := prepared.Evidence()
	if _, err := RebuildPreparedCommandV2(evidence, payload); err != nil {
		t.Fatal(err)
	}
	fields := reflect.TypeOf(anchor.Generation)
	for i := 0; i < fields.NumField(); i++ {
		t.Run(fields.Field(i).Name, func(t *testing.T) {
			changed := evidence
			reflect.ValueOf(&changed.PreCommand.Generation).Elem().Field(i).SetString("wrong-generation")
			changed.EvidenceDigest, _ = changed.integrityDigest()
			if changed.Validate() == nil {
				t.Fatal("generation field omitted from admission")
			}
		})
	}
	if _, err := RebuildPreparedCommandV2(evidence, validBindPayloadForAnchorV2(anchor)); err == nil {
		t.Fatal("different payload rebuilt")
	}
	changed := evidence
	changed.PreCommand.Binding.ControlFiles.Journal.Inode++
	if changed.Validate() == nil {
		t.Fatal("changed held identity kept valid evidence digest")
	}
	if prepared.Evidence() != evidence {
		t.Fatal("returned evidence aliases private preparation")
	}
	raw := mustCanonical(evidence)
	for _, secret := range []string{validBootstrapV2().SessionNonce, payload.Runtime.CanonicalPath, payload.WorkingDirectory.CanonicalPath} {
		if secret != "" && bytes.Contains(raw, []byte(secret)) {
			t.Fatal("secret/private launch path in preparation evidence")
		}
	}
}

func validBindPayloadForAnchorV2(a SessionAnchorV2) BindAuthorityPayload {
	return BindAuthorityPayload{SupervisorStartedFactDigest: digest("client-v2-started"), OwnerEpoch: a.Binding.OwnerEpoch, PreviousAuthorityHead: a.Binding.CurrentAuthorityHead, AuthorityHead: digest("client-v2-bound")}
}

func TestV2HandshakeRejectsWrongPeerAndMixedEvidence(t *testing.T) {
	session, _, _ := newTestSessionV2(t)
	defer session.journal.close()
	a := testAnchorV2(session)
	h := testHandshakeV2(a)
	peer := validBootstrapV2().Core
	if ValidateHandshakeBindingV2(h, a, peer) != nil {
		t.Fatal("valid handshake")
	}
	for name, mutate := range map[string]func(*HandshakeResponseV2){
		"observer":   func(h *HandshakeResponseV2) { h.ObserverIdentity = "darwin-fixed-process-supervisor-v1" },
		"schema":     func(h *HandshakeResponseV2) { h.SchemaVersion = HandshakeSchema },
		"journal":    func(h *HandshakeResponseV2) { h.JournalHead = digest("forged-journal") },
		"peer-birth": func(h *HandshakeResponseV2) { h.SupervisorProcess.BirthMicroseconds++ },
		"held-file":  func(h *HandshakeResponseV2) { h.ControlFiles.Journal.Inode++ },
	} {
		t.Run(name, func(t *testing.T) {
			changed := h
			mutate(&changed)
			if ValidateHandshakeBindingV2(changed, a, peer) == nil {
				t.Fatal("forged handshake accepted")
			}
		})
	}
	peer.Process.PID++
	if ValidateHandshakeBindingV2(h, a, peer) == nil {
		t.Fatal("wrong kernel peer accepted")
	}
}

func newTestClientV2(t *testing.T, session *sessionV2, mode string) (*ClientV2, <-chan struct{}) {
	t.Helper()
	core, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		codec, _ := NewProtocolCodec(server)
		for {
			var request requestV2
			if codec.Read(&request) != nil {
				return
			}
			response, err := session.handle(mustCanonical(request))
			if err != nil {
				return
			}
			if mode == "lost" {
				return
			}
			if mode == "forged" {
				response.ReceiptDigest = digest("forged-receipt")
			}
			if codec.Write(response) != nil {
				return
			}
		}
	}()
	anchor := testAnchorV2(session)
	client, err := newClientV2(core, testHandshakeV2(anchor), anchor, validBootstrapV2().Core)
	if err != nil {
		_ = core.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Disconnect()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("test transport did not close")
		}
	})
	return client, done
}

func clientOptionsV2(a SessionAnchorV2, command CommandName, id string) CommandOptions {
	return CommandOptions{Command: command, CommandID: id, Sequence: a.Binding.CommandSequence + 1, PreviousCommandDigest: a.Binding.CommandHead, CurrentAuthorityHead: a.Binding.CurrentAuthorityHead, Deadline: time.Now().UTC().Add(20 * time.Second)}
}

func TestClientV2PreparedCommandAdvancesExactAnchor(t *testing.T) {
	session, mechanics, _ := newTestSessionV2(t)
	defer session.journal.close()
	session.core.now = time.Now
	client, _ := newTestClientV2(t, session, "")
	pre := client.Anchor()
	prepared, err := client.Prepare(clientOptionsV2(pre, CommandBindAuthority, "client-bind"), validBindPayloadForAnchorV2(pre))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := client.DoPrepared(context.Background(), prepared)
	if err != nil || outcome.Status != "ok" || outcome.PostCommand != client.Anchor() || outcome.Preparation != prepared.Evidence() {
		t.Fatalf("bind outcome: %+v %v", outcome, err)
	}
	if _, err := client.DoPrepared(context.Background(), prepared); err == nil {
		t.Fatal("old anchor executed twice")
	}
	session.core.mu.Lock()
	payload := validSpawnPayload()
	payload.LaunchAuthorizedFactDigest, payload.SupervisorStartedFactDigest = session.core.launchFact, session.core.supervisorStartedFact
	session.core.mu.Unlock()
	spawn, err := client.Prepare(clientOptionsV2(client.Anchor(), CommandSpawn, "client-spawn"), payload)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err = client.DoPrepared(context.Background(), spawn)
	if err != nil || outcome.ProcessReport == nil || outcome.ProcessReport.ObserverIdentity != observerIdentityV2 {
		t.Fatalf("spawn outcome: %+v %v", outcome, err)
	}
	state := session.journal.recoverySnapshot("")
	if outcome.PostCommand.Binding.JournalHead != state.head || outcome.PostCommand.Binding.CommandHead != state.commandHead || outcome.PostCommand.Binding.JournalSequence != 5 {
		t.Fatal("client computed wrong exact post anchor")
	}
	if _, ok := client.Pending(); ok {
		t.Fatal("committed command remains pending")
	}
	_ = client.Disconnect()
	session.core.mu.Lock()
	calls := mechanics.calls
	session.core.mu.Unlock()
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestClientV2AmbiguousOrForgedResponsePreservesPendingAndStopsRetry(t *testing.T) {
	for _, mode := range []string{"lost", "forged"} {
		t.Run(mode, func(t *testing.T) {
			session, mechanics, _ := newTestSessionV2(t)
			defer session.journal.close()
			session.core.now = time.Now
			bindTestSessionV2(t, session)
			client, done := newTestClientV2(t, session, mode)
			payload := validSpawnPayload()
			payload.LaunchAuthorizedFactDigest, payload.SupervisorStartedFactDigest = session.core.launchFact, session.core.supervisorStartedFact
			prepared, err := client.Prepare(clientOptionsV2(client.Anchor(), CommandSpawn, "ambiguous-spawn"), payload)
			if err != nil {
				t.Fatal(err)
			}
			pre := client.Anchor()
			if _, err := client.DoPrepared(context.Background(), prepared); err == nil {
				t.Fatal("ambiguous outcome accepted")
			}
			pending, ok := client.Pending()
			if !ok || pending != prepared.Evidence() || client.Anchor() != pre {
				t.Fatal("ambiguous evidence lost or authority advanced")
			}
			if _, err := client.DoPrepared(context.Background(), prepared); err == nil {
				t.Fatal("poisoned connection retried")
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("transport alive")
			}
			if mechanics.calls != 1 {
				t.Fatalf("calls=%d", mechanics.calls)
			}
		})
	}
}

func TestClientV2RejectedCommandDoesNotConsumeExternalAuthority(t *testing.T) {
	session, mechanics, _ := newTestSessionV2(t)
	defer session.journal.close()
	session.core.now = time.Now
	bindTestSessionV2(t, session)
	mechanics.fail = ErrUnavailable
	client, _ := newTestClientV2(t, session, "")
	pre := client.Anchor()
	options := clientOptionsV2(pre, CommandResume, "rejected-resume")
	options.CurrentAuthorityHead = digest("not-consumed-external-authority")
	prepared, err := client.Prepare(options, ResumePayload{ProcessStartedFactDigest: digest("external-started-fact")})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := client.DoPrepared(context.Background(), prepared)
	if err != nil || outcome.Status != "rejected" || outcome.PostCommand.Binding.CurrentAuthorityHead != pre.Binding.CurrentAuthorityHead {
		t.Fatalf("rejected outcome: %+v %v", outcome, err)
	}
	state := session.journal.recoverySnapshot("")
	if state.authorityHead != pre.Binding.CurrentAuthorityHead || state.head != outcome.PostCommand.Binding.JournalHead {
		t.Fatal("journal adopted rejected authority")
	}
	session.core.mu.Lock()
	mechanics.fail = nil
	payload := validSpawnPayload()
	payload.LaunchAuthorizedFactDigest, payload.SupervisorStartedFactDigest = session.core.launchFact, session.core.supervisorStartedFact
	session.core.mu.Unlock()
	next, err := client.Prepare(clientOptionsV2(client.Anchor(), CommandSpawn, "after-rejected-resume"), payload)
	if err != nil {
		t.Fatal(err)
	}
	if outcome, err := client.DoPrepared(context.Background(), next); err != nil || outcome.Status != "ok" {
		t.Fatalf("rejected command broke next journal transition: %v", err)
	}
}
