package processsupervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

func TestClientDoValidatesExactResponseAndAllowsOnlyExactPendingReplay(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	client, err := newTestClient(clientSide)
	if err != nil {
		t.Fatal(err)
	}
	server := mustClientCodec(t, serverSide)
	done := make(chan error, 1)
	go func() {
		var request Request
		if err := server.Read(&request); err != nil {
			done <- err
			return
		}
		response := clientResponse(t, request)
		done <- server.Write(response)
	}()
	options := CommandOptions{Command: CommandAbortUnbound, CommandID: "abort-1", Sequence: 1, PreviousCommandDigest: CommandGenesisDigest, CurrentAuthorityHead: digest("a"), Deadline: time.Now().UTC().Add(time.Minute)}
	payload := AbortUnboundPayload{OwnerEpoch: 1, PreviousAuthorityHead: digest("a"), AuthorityAbsenceProofDigest: digest("b")}
	response, err := client.AbortUnbound(context.Background(), options, payload)
	if err != nil || response.Status != "ok" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if client.pending != nil {
		t.Fatal("successful response left pending request")
	}
	if err := client.Disconnect(); err != nil {
		t.Fatal(err)
	}
}

func TestClientDoKeepsJournalBaseA0WhileOrdinaryCommandAdvancesToAt(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	client, err := newTestClient(clientSide)
	if err != nil {
		t.Fatal(err)
	}
	a0 := client.Anchor()
	at := digest("d")
	server := mustClientCodec(t, serverSide)
	done := make(chan error, 1)
	go func() {
		var request Request
		if err := server.Read(&request); err != nil {
			done <- err
			return
		}
		if request.CurrentAuthorityHead != at {
			done <- errors.New("request did not carry At")
			return
		}
		done <- server.Write(clientResponse(t, request))
	}()
	options := CommandOptions{Command: CommandResume, CommandID: "resume-authority-advance", Sequence: 1, PreviousCommandDigest: a0.CommandHead, CurrentAuthorityHead: at, Deadline: time.Now().UTC().Add(20 * time.Second)}
	if _, err := client.Do(context.Background(), options, ResumePayload{ProcessStartedFactDigest: digest("e")}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := client.Anchor()
	if got.CurrentAuthorityHead != at || got.JournalSequence != a0.JournalSequence+2 {
		t.Fatalf("post anchor=%+v", got)
	}
	request, err := NewRequest(a0.SessionID, options.Command, options.CommandID, options.Sequence, options.PreviousCommandDigest, at, options.Deadline, ResumePayload{ProcessStartedFactDigest: digest("e")})
	if err != nil {
		t.Fatal(err)
	}
	response := clientResponse(t, request)
	_, rightHead, err := expectedPendingJournalHeads(a0, request, &response)
	if err != nil || got.JournalHead != rightHead {
		t.Fatalf("A0 journal head=%s got=%s err=%v", rightHead, got.JournalHead, err)
	}
	forged := a0
	forged.CurrentAuthorityHead = at
	_, wrongHead, err := expectedPendingJournalHeads(forged, request, &response)
	if err != nil || wrongHead == rightHead {
		t.Fatalf("At-as-record-base was not distinguished right=%s wrong=%s err=%v", rightHead, wrongHead, err)
	}
	_ = client.Disconnect()
}

func TestClientLostResponsePinsExactRequestUntilReconnect(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	client, err := newTestClient(clientSide)
	if err != nil {
		t.Fatal(err)
	}
	server := mustClientCodec(t, serverSide)
	go func() {
		var request Request
		_ = server.Read(&request)
		_ = serverSide.Close()
	}()
	options := CommandOptions{Command: CommandAbortUnbound, CommandID: "abort-lost", Sequence: 1, PreviousCommandDigest: CommandGenesisDigest, CurrentAuthorityHead: digest("a"), Deadline: time.Now().UTC().Add(time.Minute)}
	payload := AbortUnboundPayload{OwnerEpoch: 1, PreviousAuthorityHead: digest("a"), AuthorityAbsenceProofDigest: digest("b")}
	if _, err := client.AbortUnbound(context.Background(), options, payload); !errors.Is(err, ErrIntervention) {
		t.Fatalf("lost response error=%v", err)
	}
	if _, err := client.AbortUnbound(context.Background(), options, payload); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("same-stream retry error=%v", err)
	}
	if evidence, ok := client.PendingReplayEvidence(); !ok || evidence.RequestDigest == "" || evidence.CommandID != options.CommandID {
		t.Fatalf("secret-free pending evidence=%+v ok=%v", evidence, ok)
	}
	_ = client.Disconnect()
}

func TestClientRejectsPartialOrMisbindingResponse(t *testing.T) {
	for name, serve := range map[string]func(net.Conn){
		"partial": func(connection net.Conn) {
			codec := mustClientCodec(t, connection)
			var request Request
			_ = codec.Read(&request)
			_, _ = connection.Write([]byte("00000020:{\"schemaVersion\":"))
			_ = connection.Close()
		},
		"wrong command": func(connection net.Conn) {
			codec := mustClientCodec(t, connection)
			var request Request
			_ = codec.Read(&request)
			response := clientResponse(t, request)
			response.CommandID = "other-command"
			_ = codec.Write(response)
			// A valid late frame must not be consumed as the next command's
			// response: validation ambiguity poisons and closes this stream.
			_ = codec.Write(clientResponse(t, request))
		},
	} {
		t.Run(name, func(t *testing.T) {
			clientSide, serverSide := net.Pipe()
			defer serverSide.Close()
			client, err := newTestClient(clientSide)
			if err != nil {
				t.Fatal(err)
			}
			go serve(serverSide)
			options := CommandOptions{Command: CommandAbortUnbound, CommandID: "abort-hostile", Sequence: 1, PreviousCommandDigest: CommandGenesisDigest, CurrentAuthorityHead: digest("a"), Deadline: time.Now().UTC().Add(time.Minute)}
			payload := AbortUnboundPayload{OwnerEpoch: 1, PreviousAuthorityHead: digest("a"), AuthorityAbsenceProofDigest: digest("b")}
			if _, err := client.AbortUnbound(context.Background(), options, payload); err == nil {
				t.Fatal("hostile response admitted")
			}
			if _, err := client.AbortUnbound(context.Background(), options, payload); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("poisoned stream retry error=%v", err)
			}
			_ = client.Disconnect()
		})
	}
}

func TestReconnectHandshakeReconcilesOnlyClosedPendingStates(t *testing.T) {
	bootstrap := validBootstrap()
	response := validClientHandshake()
	anchor := HandshakeAnchor{
		SessionID: response.SessionID, SessionNonceDigest: response.SessionNonceDigest, Authority: bootstrap.Authority,
		OwnerEpoch: response.OwnerEpoch, CurrentAuthorityHead: response.CurrentAuthorityHead,
		CommandSequence: response.CommandSequence, CommandHead: response.CommandHead, JournalSequence: response.JournalSequence, JournalHead: response.JournalHead,
		UID: bootstrap.Core.UID, GID: bootstrap.Core.GID, FixedBinary: bootstrap.Core.Binary, ControlSocket: response.ControlSocket,
	}
	pending, err := NewRequest(anchor.SessionID, CommandAbortUnbound, "abort-reconnect", 1, anchor.CommandHead, anchor.CurrentAuthorityHead, time.Now().UTC().Add(time.Minute), AbortUnboundPayload{OwnerEpoch: anchor.OwnerEpoch, PreviousAuthorityHead: anchor.CurrentAuthorityHead, AuthorityAbsenceProofDigest: digest("7")})
	if err != nil {
		t.Fatal(err)
	}
	reconnect := ReconnectRequest{
		SchemaVersion: ReconnectSchema, ProtocolRevision: ProtocolRevision, SessionID: anchor.SessionID,
		PreviousOwnerEpoch: anchor.OwnerEpoch, OwnerEpoch: anchor.OwnerEpoch + 1, PreviousAuthorityHead: anchor.CurrentAuthorityHead, CurrentAuthorityHead: digest("8"), ControlOwnerAcquired: digest("9"), Core: bootstrap.Core,
		LastOwnerEpoch: anchor.OwnerEpoch, LastAuthorityHead: anchor.CurrentAuthorityHead,
		LastCommandSequence: anchor.CommandSequence, LastCommandHead: anchor.CommandHead, LastJournalSequence: anchor.JournalSequence, LastJournalHead: anchor.JournalHead, PendingRequest: &pending,
	}
	evidence := pendingEvidence(pending)
	options := ReconnectOptions{Request: reconnect, Anchor: anchor, PendingEvidence: &evidence}
	observed := CoreIdentity{UID: anchor.UID, GID: anchor.GID, Process: response.SupervisorProcess, Binary: anchor.FixedBinary}
	for _, state := range []ReconciliationState{ReconciliationUnchanged, ReconciliationIntentPending, ReconciliationReceiptCommitted} {
		t.Run(string(state), func(t *testing.T) {
			handshake := response
			handshake.OwnerEpoch = reconnect.OwnerEpoch
			handshake.CurrentAuthorityHead = reconnect.CurrentAuthorityHead
			handshake.Reconciliation = state
			var wantReplay bool
			switch state {
			case ReconciliationUnchanged:
				replay := clientResponse(t, pending)
				_, receipt, err := expectedPendingJournalHeads(anchor, pending, &replay)
				if err != nil {
					t.Fatal(err)
				}
				handshake.CommandSequence = pending.Sequence
				handshake.CommandHead = replay.CommandHead
				handshake.JournalSequence += 2
				handshake.JournalHead = receipt
				handshake.ReplayedResponse = &replay
				wantReplay = true
			case ReconciliationIntentPending:
				intent, _, err := expectedPendingJournalHeads(anchor, pending, nil)
				if err != nil {
					t.Fatal(err)
				}
				handshake.JournalSequence++
				handshake.JournalHead = intent
			case ReconciliationReceiptCommitted:
				replay := clientResponse(t, pending)
				_, receipt, err := expectedPendingJournalHeads(anchor, pending, &replay)
				if err != nil {
					t.Fatal(err)
				}
				handshake.CommandSequence = pending.Sequence
				handshake.CommandHead = replay.CommandHead
				handshake.JournalSequence += 2
				handshake.JournalHead = receipt
				handshake.ReplayedResponse = &replay
				wantReplay = true
			}
			replay, _, err := validateReconnectHandshake(handshake, options, observed)
			if err != nil || (replay != nil) != wantReplay {
				t.Fatalf("state=%s replay=%+v err=%v", state, replay, err)
			}
		})
	}
	hostile := response
	hostile.OwnerEpoch = reconnect.OwnerEpoch
	hostile.CurrentAuthorityHead = reconnect.CurrentAuthorityHead
	hostile.Reconciliation = ReconciliationReceiptCommitted
	replay := clientResponse(t, pending)
	_, hostile.JournalHead, _ = expectedPendingJournalHeads(anchor, pending, &replay)
	hostile.JournalSequence += 2
	hostile.CommandSequence = pending.Sequence
	hostile.CommandHead = replay.CommandHead
	hostile.ReplayedResponse = &replay
	changed := pending
	changed.RequestDigest = digest("f")
	hostileOptions := options
	hostileOptions.Request.PendingRequest = &changed
	if _, _, err := validateReconnectHandshake(hostile, hostileOptions, observed); err == nil {
		t.Fatal("different pending digest admitted")
	}

	unchangedWithoutReplay := response
	unchangedWithoutReplay.OwnerEpoch = reconnect.OwnerEpoch
	unchangedWithoutReplay.CurrentAuthorityHead = reconnect.CurrentAuthorityHead
	unchangedWithoutReplay.Reconciliation = ReconciliationUnchanged
	if _, _, err := validateReconnectHandshake(unchangedWithoutReplay, options, observed); err == nil {
		t.Fatal("pending unchanged without exact replay admitted")
	}

	noPending := options
	noPending.Request.PendingRequest = nil
	noPending.PendingEvidence = nil
	if replay, gotAnchor, err := validateReconnectHandshake(unchangedWithoutReplay, noPending, observed); err != nil || replay != nil || gotAnchor.CommandHead != anchor.CommandHead {
		t.Fatalf("exact no-pending unchanged replay=%+v anchor=%+v err=%v", replay, gotAnchor, err)
	}
}

func TestReconnectHandshakeRejectsAtAsJournalRecordBaseAndWrongReplayBindings(t *testing.T) {
	bootstrap := validBootstrap()
	response := validClientHandshake()
	a0 := HandshakeAnchor{
		SessionID: response.SessionID, SessionNonceDigest: response.SessionNonceDigest, Authority: bootstrap.Authority,
		OwnerEpoch: response.OwnerEpoch, CurrentAuthorityHead: response.CurrentAuthorityHead,
		CommandHead: response.CommandHead, JournalSequence: response.JournalSequence, JournalHead: response.JournalHead,
		UID: bootstrap.Core.UID, GID: bootstrap.Core.GID, FixedBinary: bootstrap.Core.Binary, ControlSocket: response.ControlSocket,
	}
	at := digest("d")
	pending, err := NewRequest(a0.SessionID, CommandResume, "resume-reconnect", 1, a0.CommandHead, at, time.Now().UTC().Add(20*time.Second), ResumePayload{ProcessStartedFactDigest: digest("e")})
	if err != nil {
		t.Fatal(err)
	}
	evidence := pendingEvidence(pending)
	request := ReconnectRequest{
		SchemaVersion: ReconnectSchema, ProtocolRevision: ProtocolRevision, SessionID: a0.SessionID, SessionNonce: bootstrap.SessionNonce,
		PreviousOwnerEpoch: a0.OwnerEpoch, OwnerEpoch: a0.OwnerEpoch + 1, PreviousAuthorityHead: at, CurrentAuthorityHead: digest("8"), ControlOwnerAcquired: digest("9"), Core: bootstrap.Core,
		LastOwnerEpoch: a0.OwnerEpoch, LastAuthorityHead: a0.CurrentAuthorityHead, LastCommandSequence: a0.CommandSequence, LastCommandHead: a0.CommandHead, LastJournalSequence: a0.JournalSequence, LastJournalHead: a0.JournalHead, PendingRequest: &pending,
	}
	options := ReconnectOptions{Request: request, Anchor: a0, PendingEvidence: &evidence}
	observed := CoreIdentity{UID: a0.UID, GID: a0.GID, Process: response.SupervisorProcess, Binary: a0.FixedBinary}
	replay := clientResponse(t, pending)
	_, receiptHead, err := expectedPendingJournalHeads(a0, pending, &replay)
	if err != nil {
		t.Fatal(err)
	}
	valid := response
	valid.OwnerEpoch = request.OwnerEpoch
	valid.CurrentAuthorityHead = request.CurrentAuthorityHead
	valid.CommandSequence = pending.Sequence
	valid.CommandHead = replay.CommandHead
	valid.JournalSequence += 2
	valid.JournalHead = receiptHead
	valid.Reconciliation = ReconciliationReceiptCommitted
	valid.ReplayedResponse = &replay
	if _, _, err := validateReconnectHandshake(valid, options, observed); err != nil {
		t.Fatalf("A0!=At valid receipt rejected: %v", err)
	}

	forgedBase := a0
	forgedBase.CurrentAuthorityHead = at
	_, forgedHead, err := expectedPendingJournalHeads(forgedBase, pending, &replay)
	if err != nil {
		t.Fatal(err)
	}
	forged := valid
	forged.JournalHead = forgedHead
	if _, _, err := validateReconnectHandshake(forged, options, observed); err == nil {
		t.Fatal("At-as-record-base forgery admitted")
	}

	for name, mutate := range map[string]func(*HandshakeResponse){
		"wrong request":      func(value *HandshakeResponse) { value.ReplayedResponse.RequestDigest = digest("7") },
		"wrong receipt":      func(value *HandshakeResponse) { value.ReplayedResponse.ReceiptDigest = digest("8") },
		"wrong command head": func(value *HandshakeResponse) { value.ReplayedResponse.CommandHead = digest("9") },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cloneHandshake(valid)
			mutate(&candidate)
			if _, _, err := validateReconnectHandshake(candidate, options, observed); err == nil {
				t.Fatal("forged replay admitted")
			}
		})
	}
}

func TestCommandPostAuthorityHeadIsClosedForBindAndDurableRejection(t *testing.T) {
	a0 := digest("a")
	at := digest("b")
	next := digest("c")
	bindProjection := requestProjection{NextAuthorityHead: next}
	if got := commandPostAuthorityHead(a0, Request{Command: CommandBindAuthority}, Response{Status: "ok"}, bindProjection); got != next {
		t.Fatalf("bind ok post=%s", got)
	}
	if got := commandPostAuthorityHead(a0, Request{Command: CommandBindAuthority}, Response{Status: "rejected"}, bindProjection); got != a0 {
		t.Fatalf("bind rejected post=%s", got)
	}
	if got := commandPostAuthorityHead(a0, Request{Command: CommandResume, CurrentAuthorityHead: at}, Response{Status: "rejected"}, requestProjection{}); got != at {
		t.Fatalf("ordinary durable rejection post=%s", got)
	}
}

func TestPendingReplayEvidenceNeverContainsRawCommandMaterial(t *testing.T) {
	request, err := NewRequest("session-1", CommandSpawn, "spawn-secret", 1, CommandGenesisDigest, digest("d"), time.Now().UTC().Add(time.Minute), validSpawnPayload())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(pendingEvidence(request))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"credential-value", "stdin-secret", "/secret/runtime", "secret-argument"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("pending evidence leaked %q: %s", secret, raw)
		}
	}
}

func TestVerifiedCommandOutcomeExposesExactDigestsTypedReportAndRecovery(t *testing.T) {
	pre := validClientEvidence().Anchor
	request, err := NewRequest(pre.SessionID, CommandResume, "resume-outcome", pre.CommandSequence+1, pre.CommandHead, digest("d"), time.Now().UTC().Add(20*time.Second), ResumePayload{ProcessStartedFactDigest: digest("e")})
	if err != nil {
		t.Fatal(err)
	}
	response := clientResponse(t, request)
	_, receiptHead, err := expectedPendingJournalHeads(pre, request, &response)
	if err != nil {
		t.Fatal(err)
	}
	post := pre
	post.CommandSequence, post.CommandHead = request.Sequence, response.CommandHead
	post.JournalSequence, post.JournalHead = pre.JournalSequence+2, receiptHead
	post.CurrentAuthorityHead = request.CurrentAuthorityHead
	outcome, err := verifiedCommandOutcome(request, response, CommandRecoveryEvidence{PreCommand: pre, PostCommand: post})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.RequestDigest != request.RequestDigest || outcome.ReceiptDigest != response.ReceiptDigest || outcome.ObservationDigest != response.ObservationDigest || outcome.CommandHead != response.CommandHead || outcome.ProcessReport == nil || outcome.ProcessReport.State != "running" || outcome.Recovery.PreCommand.CurrentAuthorityHead != pre.CurrentAuthorityHead || outcome.Recovery.PostCommand.CurrentAuthorityHead != request.CurrentAuthorityHead {
		t.Fatalf("typed outcome=%+v", outcome)
	}

	var result MechanicsResult
	if strictCanonicalDecode(response.Payload, &result) != nil {
		t.Fatal("fixture mechanics result invalid")
	}
	var report ProcessReport
	if strictCanonicalDecode(result.Payload, &report) != nil {
		t.Fatal("fixture process report invalid")
	}
	report.State = "invented-state"
	result.Payload = mustCanonical(report)
	result.ObservationDigest = canonical.DigestBytes(result.Payload)
	forged := responseForResult(t, request, result)
	_, forgedReceiptHead, err := expectedPendingJournalHeads(pre, request, &forged)
	if err != nil {
		t.Fatal(err)
	}
	forgedPost := post
	forgedPost.CommandHead, forgedPost.JournalHead = forged.CommandHead, forgedReceiptHead
	if _, err := verifiedCommandOutcome(request, forged, CommandRecoveryEvidence{PreCommand: pre, PostCommand: forgedPost}); err == nil {
		t.Fatal("semantically forged typed process report admitted")
	}

	report.State = "running"
	result.Payload = mustCanonical(report)
	result.ObservationDigest = digest("f")
	forged = responseForResult(t, request, result)
	forgedPost, err = commandPostAnchor(pre, request, forged)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifiedCommandOutcome(request, forged, CommandRecoveryEvidence{PreCommand: pre, PostCommand: forgedPost}); err == nil {
		t.Fatal("process report with detached observation digest admitted")
	}

	forged = response
	forgedPost = post
	forgedPost.JournalHead = digest("f")
	if _, err := verifiedCommandOutcome(request, forged, CommandRecoveryEvidence{PreCommand: pre, PostCommand: forgedPost}); err == nil {
		t.Fatal("typed recovery with forged journal head admitted")
	}
}

func TestRunBoundedTransportFailsClosedAndJoinsDeadlineWriter(t *testing.T) {
	for name, failCall := range map[string]int{"initial deadline": 1, "deadline clear": 2} {
		t.Run(name, func(t *testing.T) {
			stream := &recordingDeadlineStream{failCall: failCall}
			err := runBoundedTransport(context.Background(), stream, time.Now().Add(time.Minute), func() error { return nil })
			if !errors.Is(err, ErrIntervention) || !stream.isClosed() {
				t.Fatalf("err=%v closed=%v", err, stream.isClosed())
			}
		})
	}
	stream := &recordingDeadlineStream{secondSet: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	err := runBoundedTransport(ctx, stream, time.Now().Add(time.Minute), func() error {
		cancel()
		<-stream.secondSet
		return ErrIntervention
	})
	if !errors.Is(err, ErrIntervention) || !stream.isClosed() {
		t.Fatalf("cancel err=%v closed=%v", err, stream.isClosed())
	}
	calls := stream.deadlines()
	if len(calls) != 3 || !calls[len(calls)-1].IsZero() {
		t.Fatalf("deadline last-writer calls=%v", calls)
	}
}

type recordingDeadlineStream struct {
	mu        sync.Mutex
	calls     []time.Time
	failCall  int
	closed    bool
	secondSet chan struct{}
}

func (stream *recordingDeadlineStream) Read([]byte) (int, error)    { return 0, errors.New("unused") }
func (stream *recordingDeadlineStream) Write(p []byte) (int, error) { return len(p), nil }
func (stream *recordingDeadlineStream) Close() error {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	stream.closed = true
	return nil
}
func (stream *recordingDeadlineStream) SetDeadline(value time.Time) error {
	stream.mu.Lock()
	stream.calls = append(stream.calls, value)
	call := len(stream.calls)
	second := stream.secondSet
	stream.mu.Unlock()
	if call == 2 && second != nil {
		close(second)
	}
	if call == stream.failCall {
		return errors.New("deadline failure")
	}
	return nil
}
func (stream *recordingDeadlineStream) isClosed() bool {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.closed
}
func (stream *recordingDeadlineStream) deadlines() []time.Time {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return append([]time.Time(nil), stream.calls...)
}

func TestAbortUnboundCannotAliasAnotherCommand(t *testing.T) {
	client := &Client{}
	if _, err := client.AbortUnbound(context.Background(), CommandOptions{Command: CommandSpawn}, AbortUnboundPayload{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("aliased abort error=%v", err)
	}
}

func TestValidateReconnectOptionsRejectsAuthorityAndIdentityDrift(t *testing.T) {
	bootstrap := validBootstrap()
	bootstrap.SessionNonce = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	directory := bootstrap.ControlDirectoryIdentity
	socket := ControlSocketIdentity{Device: 8, Inode: 9, FileType: "socket", UID: bootstrap.Core.UID, GID: bootstrap.Core.GID, Mode: 0o140600, LinkCount: 1}
	request := ReconnectRequest{
		SchemaVersion: ReconnectSchema, ProtocolRevision: ProtocolRevision, SessionID: bootstrap.SessionID, SessionNonce: bootstrap.SessionNonce,
		PreviousOwnerEpoch: 1, OwnerEpoch: 2, PreviousAuthorityHead: digest("a"), CurrentAuthorityHead: digest("b"), ControlOwnerAcquired: digest("c"), Core: bootstrap.Core,
		LastOwnerEpoch: 1, LastAuthorityHead: digest("a"),
		LastCommandHead: CommandGenesisDigest, LastJournalSequence: 1, LastJournalHead: digest("d"),
	}
	anchor := HandshakeAnchor{
		SessionID: bootstrap.SessionID, SessionNonceDigest: digestBytes(bootstrap.SessionNonce), Authority: bootstrap.Authority, OwnerEpoch: request.PreviousOwnerEpoch, CurrentAuthorityHead: request.PreviousAuthorityHead,
		CommandHead: CommandGenesisDigest, JournalSequence: 1, JournalHead: digest("d"), UID: bootstrap.Core.UID, GID: bootstrap.Core.GID, FixedBinary: bootstrap.Core.Binary, ControlSocket: socket,
	}
	file, err := os.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	base := ReconnectOptions{ControlDirectory: file, ControlDirectoryIdentity: directory, Request: request, Anchor: anchor}
	gap := base
	gap.Request.PreviousOwnerEpoch = 4
	gap.Request.OwnerEpoch = 9
	gap.Request.LastOwnerEpoch = anchor.OwnerEpoch
	if err := validateReconnectOptions(gap); err != nil {
		t.Fatalf("CAS owner epoch gap with historical anchor rejected: %v", err)
	}
	for name, mutate := range map[string]func(*ReconnectOptions){
		"raw nonce shape": func(value *ReconnectOptions) { value.Request.SessionNonce = "not-a-nonce" },
		"nonce digest":    func(value *ReconnectOptions) { value.Anchor.SessionNonceDigest = digest("f") },
		"owner epoch":     func(value *ReconnectOptions) { value.Anchor.OwnerEpoch++ },
		"authority head":  func(value *ReconnectOptions) { value.Anchor.CurrentAuthorityHead = digest("f") },
		"owner proof":     func(value *ReconnectOptions) { value.Request.ControlOwnerAcquired = "" },
		"owner replay": func(value *ReconnectOptions) {
			value.Request.OwnerEpoch = value.Request.PreviousOwnerEpoch
		},
		"historical owner ahead": func(value *ReconnectOptions) {
			value.Request.LastOwnerEpoch = value.Request.PreviousOwnerEpoch + 1
		},
		"binary source": func(value *ReconnectOptions) {
			value.Anchor.FixedBinary.SourceHead = "1111111111111111111111111111111111111111"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			if err := validateReconnectOptions(candidate); err == nil {
				t.Fatal("drift admitted")
			}
		})
	}
}

func validClientHandshake() HandshakeResponse {
	bootstrap := validBootstrap()
	socket := ControlSocketIdentity{Device: 8, Inode: 9, FileType: "socket", UID: bootstrap.Core.UID, GID: bootstrap.Core.GID, Mode: 0o140600, LinkCount: 1}
	return HandshakeResponse{
		SchemaVersion: HandshakeSchema, ProtocolRevision: ProtocolRevision, Status: "ok", ReasonCode: "process-supervisor-ready",
		SessionID: bootstrap.SessionID, SessionNonceDigest: digest("1"), OwnerEpoch: bootstrap.OwnerEpoch, CurrentAuthorityHead: bootstrap.CurrentAuthorityHead,
		CommandHead: CommandGenesisDigest, JournalSequence: 1, JournalHead: digest("2"), ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
		SupervisorProcess: bootstrap.Core.Process, SupervisorBinary: bootstrap.Core.Binary, ControlSocket: socket,
	}
}

func validClientEvidence() ConnectionEvidence {
	bootstrap := validBootstrap()
	handshake := validClientHandshake()
	anchor := HandshakeAnchor{
		SessionID: handshake.SessionID, SessionNonceDigest: handshake.SessionNonceDigest, Authority: bootstrap.Authority,
		OwnerEpoch: handshake.OwnerEpoch, CurrentAuthorityHead: handshake.CurrentAuthorityHead,
		CommandSequence: handshake.CommandSequence, CommandHead: handshake.CommandHead, JournalSequence: handshake.JournalSequence, JournalHead: handshake.JournalHead,
		UID: bootstrap.Core.UID, GID: bootstrap.Core.GID, FixedBinary: bootstrap.Core.Binary, ControlSocket: handshake.ControlSocket,
	}
	return ConnectionEvidence{Handshake: handshake, Anchor: anchor}
}

func newTestClient(stream deadlineStream) (*Client, error) {
	evidence := validClientEvidence()
	peer := CoreIdentity{UID: evidence.Anchor.UID, GID: evidence.Anchor.GID, Process: evidence.Handshake.SupervisorProcess, Binary: evidence.Handshake.SupervisorBinary}
	return newClient(stream, evidence, peer)
}

func mustClientCodec(t *testing.T, connection net.Conn) *ProtocolCodec {
	t.Helper()
	codec, err := NewProtocolCodec(connection)
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

func clientResponse(t *testing.T, request Request) Response {
	t.Helper()
	reason, observation := "unbound-aborted", ""
	payload := canonicalEmptyPayload()
	switch request.Command {
	case CommandBindAuthority:
		var value BindAuthorityPayload
		if strictCanonicalDecode(request.Payload, &value) != nil {
			t.Fatal("invalid bind fixture")
		}
		reason, observation = "authority-bound", value.SupervisorStartedFactDigest
	case CommandAbortUnbound:
		var value AbortUnboundPayload
		if strictCanonicalDecode(request.Payload, &value) != nil {
			t.Fatal("invalid abort fixture")
		}
		observation = value.AuthorityAbsenceProofDigest
	default:
		state := "running"
		switch request.Command {
		case CommandSpawn:
			state = "exec-stopped"
		case CommandTerminate, CommandCollect, CommandClose:
			state = "terminal"
		}
		reason = "process-observed"
		report := ProcessReport{State: state, ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Process: validBootstrap().Core.Process, RuntimeObjectDigest: digest("a"), WorkingObjectDigest: digest("b")}
		if request.Command == CommandCollect {
			report.StdoutDigest, report.StderrDigest = digest("c"), digest("d")
		}
		payload = mustCanonical(report)
		observation = canonical.DigestBytes(payload)
	}
	result := MechanicsResult{Disposition: "ok", ReasonCode: reason, ObservationDigest: observation, Payload: payload}
	if request.Command == CommandCollect {
		result.TranscriptDigest = observation
	}
	return responseForResult(t, request, result)
}

func responseForResult(t *testing.T, request Request, result MechanicsResult) Response {
	t.Helper()
	receipt, err := digestValue(result)
	if err != nil {
		t.Fatal(err)
	}
	head, err := digestValue(struct {
		Previous string `json:"previousCommandDigest"`
		Request  string `json:"requestDigest"`
		Receipt  string `json:"receiptDigest"`
	}{request.PreviousCommandDigest, request.RequestDigest, receipt})
	if err != nil {
		t.Fatal(err)
	}
	return Response{SchemaVersion: ResponseSchema, ProtocolRevision: ProtocolRevision, SessionID: request.SessionID, Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence, RequestDigest: request.RequestDigest, Status: "ok", ReasonCode: result.ReasonCode, ReceiptDigest: receipt, ObservationDigest: result.ObservationDigest, CommandHead: head, Payload: mustCanonical(result)}
}

func digestBytes(value string) string {
	return canonical.DigestBytes([]byte(value))
}
