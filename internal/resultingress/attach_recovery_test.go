//go:build darwin && arm64

package resultingress

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync/atomic"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

type fakeRebindSession struct {
	authority  processsupervisor.AttachAuthority
	executeErr error
	bind       func(processsupervisor.PreparedCommand)
	collect    func(processsupervisor.PreparedCommand) processsupervisor.VerifiedCommandOutcome
	observed   bool
	executed   bool
}

func (session *fakeRebindSession) Observation() (processsupervisor.AttachObservation, error) {
	if session.observed {
		return processsupervisor.AttachObservation{}, processsupervisor.ErrConflict
	}
	session.observed = true
	return processsupervisor.AttachObservation{
		SchemaVersion: processsupervisor.AttachObservationSchema, ProtocolRevision: processsupervisor.ProtocolRevision,
		PreviousSupervisor: session.authority.PreviousSupervisor, Supervisor: session.authority.Supervisor,
		CurrentAcquisition: session.authority.CurrentAcquisition, CurrentOwnerBoundFact: session.authority.CurrentOwnerBoundFact,
		Child: session.authority.Child, ChildObservationDigest: session.authority.ChildObservationDigest,
	}, nil
}

func (session *fakeRebindSession) ExecutePreparedBindAuthority(_ context.Context, prepared processsupervisor.PreparedCommand) (processsupervisor.VerifiedCommandOutcome, error) {
	if session.executed {
		return processsupervisor.VerifiedCommandOutcome{}, processsupervisor.ErrConflict
	}
	session.executed = true
	if session.executeErr != nil {
		return processsupervisor.VerifiedCommandOutcome{}, session.executeErr
	}
	if session.bind != nil {
		session.bind(prepared)
	}
	return fakeBindOutcome(prepared), nil
}

func (session *fakeRebindSession) ExecutePreparedCollect(_ context.Context, prepared processsupervisor.PreparedCommand) (processsupervisor.VerifiedCommandOutcome, error) {
	if session.executed || session.collect == nil {
		return processsupervisor.VerifiedCommandOutcome{}, processsupervisor.ErrConflict
	}
	session.executed = true
	if session.executeErr != nil {
		return processsupervisor.VerifiedCommandOutcome{}, session.executeErr
	}
	return session.collect(prepared), nil
}

func (session *fakeRebindSession) ExecutePreparedInspect(_ context.Context, prepared processsupervisor.PreparedCommand) (processsupervisor.VerifiedCommandOutcome, error) {
	if session.executed || session.collect == nil {
		return processsupervisor.VerifiedCommandOutcome{}, processsupervisor.ErrConflict
	}
	session.executed = true
	if session.executeErr != nil {
		return processsupervisor.VerifiedCommandOutcome{}, session.executeErr
	}
	return session.collect(prepared), nil
}

func (session *fakeRebindSession) ExecutePreparedClose(_ context.Context, prepared processsupervisor.PreparedCommand) (processsupervisor.VerifiedCommandOutcome, error) {
	if session.executed || session.collect == nil {
		return processsupervisor.VerifiedCommandOutcome{}, processsupervisor.ErrConflict
	}
	session.executed = true
	if session.executeErr != nil {
		return processsupervisor.VerifiedCommandOutcome{}, session.executeErr
	}
	return session.collect(prepared), nil
}

func fakeBindOutcome(prepared processsupervisor.PreparedCommand) processsupervisor.VerifiedCommandOutcome {
	evidence := prepared.Evidence()
	observation := evidence.Projection.SupervisorStartedFactDigest
	result := processsupervisor.MechanicsResult{Disposition: "ok", ReasonCode: "authority-bound", ObservationDigest: observation, Payload: json.RawMessage("{}")}
	receipt, _ := canonicalDigest(result)
	commandHead, _ := canonicalDigest(struct {
		Previous string `json:"previousCommandDigest"`
		Request  string `json:"requestDigest"`
		Receipt  string `json:"receiptDigest"`
	}{evidence.PreviousCommandDigest, evidence.RequestDigest, receipt})
	pre := evidence.PreCommand
	post := pre
	post.CurrentAuthorityHead = evidence.Projection.AuthorityHead
	post.CommandSequence = evidence.Sequence
	post.CommandHead = commandHead
	post.JournalSequence = pre.JournalSequence + 2
	// The mechanics journal head binds the command-specific intent/receipt
	// records, not the response payload digest alone. Consecutive successful
	// bind responses can carry the same payload but must still advance to a
	// distinct journal head.
	post.JournalHead = canonical.DigestBytes([]byte(pre.JournalHead + evidence.RequestDigest + receipt))
	return processsupervisor.VerifiedCommandOutcome{
		Command: evidence.Command, CommandID: evidence.CommandID, Sequence: evidence.Sequence,
		Status: "ok", Disposition: "ok", ReasonCode: result.ReasonCode,
		RequestDigest: evidence.RequestDigest, ReceiptDigest: receipt, ObservationDigest: observation,
		CommandHead: commandHead, Recovery: processsupervisor.CommandRecoveryEvidence{PreCommand: pre, PostCommand: post},
	}
}

func fakeRebindTransportFactory(executeErr error, calls *int32) rebindTransport {
	return func(_ context.Context, options processsupervisor.AttachOptions, rebind func(AttachedRebindSession) error) error {
		atomic.AddInt32(calls, 1)
		return rebind(&fakeRebindSession{authority: options.Authority, executeErr: executeErr})
	}
}

func rebindRecoveryStore(t *testing.T) (*DurableStore, AttemptAuthorityState, ControlOwnerAcquisition, AttemptIdentity, attemptOwnerVerifier, *os.File) {
	t.Helper()
	fixture := newPreparedExecutionFixture(t)
	state := fixture.storeStateAfterPrepared(t, fixture)
	state = advancePreparedAttemptToStarted(t, fixture, state)
	scope := state.Owner.Scope
	prior, found, err := fixture.store.OpenOwner(scope)
	if err != nil || !found {
		t.Fatalf("current owner found=%v err=%v", found, err)
	}
	successorEpoch := prior.Acquisition.OwnerEpoch + 1
	process := processsupervisor.ProcessIdentity{PID: 14000 + int(successorEpoch), BirthSeconds: 1_700_000_020, BirthMicroseconds: int64(successorEpoch), SessionID: 14000 + int(successorEpoch), ProcessGroupID: 14000 + int(successorEpoch)}
	successor := prior.Acquisition
	successor.OwnerEpoch = successorEpoch
	successor.OwnerProcess = process
	successor.ObservedAt = "2026-08-29T00:00:20Z"
	successorVerifier := attemptOwnerVerifier{want: successor}
	if _, err := fixture.store.AcquireOwner(context.Background(), successorVerifier, prior.Acquisition.OwnerEpoch, prior.FactDigest, successor); err != nil {
		t.Fatalf("successor acquire: %v", err)
	}
	root, err := os.MkdirTemp("/private/tmp", "rebind-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	opened, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close(); _ = os.RemoveAll(root) })
	return fixture.store, state, successor, state.Identity, successorVerifier, opened
}

func doRebind(t *testing.T, store *DurableStore, verifier attemptOwnerVerifier, acquisition ControlOwnerAcquisition, identity AttemptIdentity, dir *os.File, executeErr error, calls *int32) (AttemptAuthorityState, error) {
	t.Helper()
	return store.rebindOwnerSuccessorForAttachedRecoveryWithTransport(context.Background(), verifier, acquisition, identity, dir, "/fixed/marshal", fakeRebindTransportFactory(executeErr, calls))
}

func TestRebindOwnerSuccessorForAttachedRecoveryHappyPath(t *testing.T) {
	store, started, acquisition, identity, verifier, dir := rebindRecoveryStore(t)
	var calls int32
	var preparedOwnerEpoch uint64
	transport := func(_ context.Context, options processsupervisor.AttachOptions, rebind func(AttachedRebindSession) error) error {
		atomic.AddInt32(&calls, 1)
		return rebind(&fakeRebindSession{authority: options.Authority, bind: func(prepared processsupervisor.PreparedCommand) {
			preparedOwnerEpoch = prepared.Evidence().Projection.OwnerEpoch
		}})
	}
	state, err := store.rebindOwnerSuccessorForAttachedRecoveryWithTransport(context.Background(), verifier, acquisition, identity, dir, "/fixed/marshal", transport)
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("transport calls=%d want 1", calls)
	}
	if state.SupervisorBoundAuthorityHead != state.HeadDigest || state.SupervisorBoundAuthorityHead == started.HeadDigest {
		t.Fatalf("rebind did not advance bound authority head: bound=%s head=%s started=%s", state.SupervisorBoundAuthorityHead, state.HeadDigest, started.HeadDigest)
	}
	if len(state.SupervisorCommandCheckpoints) == 0 || state.SupervisorCommandCheckpoints[len(state.SupervisorCommandCheckpoints)-1].Evidence.Command != processsupervisor.CommandBindAuthority {
		t.Fatalf("rebind did not append a bind-authority outcome checkpoint")
	}
	if state.SupervisorPendingIntentDigest != "" {
		t.Fatal("rebind left a pending intent")
	}
	if preparedOwnerEpoch != started.SupervisorMechanicsAnchor.OwnerEpoch {
		t.Fatalf("rebind mechanics owner epoch=%d want predecessor=%d", preparedOwnerEpoch, started.SupervisorMechanicsAnchor.OwnerEpoch)
	}
	if preparedOwnerEpoch == acquisition.OwnerEpoch {
		t.Fatalf("rebind incorrectly used fresh repository owner epoch=%d", acquisition.OwnerEpoch)
	}
}

func TestCollectPreparedExecutionPersistsOutcomeAndReturnsDescriptorValidatedTranscript(t *testing.T) {
	store, _, acquisition, identity, verifier, dir := rebindRecoveryStore(t)
	var rebindCalls int32
	state, err := doRebind(t, store, verifier, acquisition, identity, dir, nil, &rebindCalls)
	if err != nil {
		t.Fatal(err)
	}
	var collectCalls int32
	transport := func(_ context.Context, options processsupervisor.AttachOptions, callback func(AttachedRebindSession) error) error {
		atomic.AddInt32(&collectCalls, 1)
		return callback(&fakeRebindSession{authority: options.Authority, collect: func(prepared processsupervisor.PreparedCommand) processsupervisor.VerifiedCommandOutcome {
			return fakeCollectOutcome(prepared, state)
		}})
	}
	reader := func(options processsupervisor.CollectedTranscriptReadOptions) (processsupervisor.CollectedTranscript, error) {
		if options.Outcome.Command != processsupervisor.CommandCollect || options.Outcome.ReasonCode != "transcript-collected" {
			t.Fatalf("reader received wrong outcome: %+v", options.Outcome)
		}
		return processsupervisor.CollectedTranscript{Stdout: []byte(`{"ok":true}`), Report: *options.Outcome.ProcessReport, TranscriptDigest: options.Outcome.TranscriptDigest}, nil
	}
	collected, err := store.collectPreparedExecutionWithTransport(context.Background(), verifier, acquisition, identity, dir, "/fixed/marshal", transport, reader)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if collected.Identity != identity || collected.OutcomeFactDigest == "" || string(collected.Transcript.Stdout) != `{"ok":true}` || atomic.LoadInt32(&collectCalls) != 1 {
		t.Fatalf("collected=%+v calls=%d", collected, collectCalls)
	}
	after, found, err := store.AttemptState(identity)
	if err != nil || !found || after.SupervisorPendingIntentDigest != "" {
		t.Fatalf("after found=%v err=%v state=%+v", found, err, after)
	}
	checkpoint, ok := latestSuccessfulCollect(after)
	if !ok || checkpoint.FactDigest != collected.OutcomeFactDigest {
		t.Fatalf("collect outcome not durable: %+v", after.SupervisorCommandCheckpoints)
	}

	// Lost return transport is replayed by descriptor read only. No second
	// command may be sent once the successful outcome is durable.
	replayed, err := store.collectPreparedExecutionWithTransport(context.Background(), verifier, acquisition, identity, dir, "/fixed/marshal", transport, reader)
	if err != nil || replayed.OutcomeFactDigest != collected.OutcomeFactDigest || atomic.LoadInt32(&collectCalls) != 1 {
		t.Fatalf("replay=%+v err=%v calls=%d", replayed, err, collectCalls)
	}
}

func TestCollectPreparedExecutionReplaysExactPendingIntentAfterResponseLoss(t *testing.T) {
	store, _, acquisition, identity, verifier, dir := rebindRecoveryStore(t)
	var rebindCalls int32
	state, err := doRebind(t, store, verifier, acquisition, identity, dir, nil, &rebindCalls)
	if err != nil {
		t.Fatal(err)
	}
	var calls int32
	transport := func(_ context.Context, options processsupervisor.AttachOptions, callback func(AttachedRebindSession) error) error {
		call := atomic.AddInt32(&calls, 1)
		session := &fakeRebindSession{authority: options.Authority, collect: func(prepared processsupervisor.PreparedCommand) processsupervisor.VerifiedCommandOutcome {
			return fakeCollectOutcome(prepared, state)
		}}
		if call == 1 {
			session.executeErr = errors.New("response lost")
		}
		return callback(session)
	}
	reader := func(options processsupervisor.CollectedTranscriptReadOptions) (processsupervisor.CollectedTranscript, error) {
		return processsupervisor.CollectedTranscript{Stdout: []byte(`{"ok":true}`), Report: *options.Outcome.ProcessReport, TranscriptDigest: options.Outcome.TranscriptDigest}, nil
	}
	if _, err := store.collectPreparedExecutionWithTransport(context.Background(), verifier, acquisition, identity, dir, "/fixed/marshal", transport, reader); err == nil {
		t.Fatal("first collect unexpectedly succeeded")
	}
	pending, found, err := store.AttemptState(identity)
	if err != nil || !found || pending.SupervisorPendingIntent.Command != processsupervisor.CommandCollect {
		t.Fatalf("pending collect not preserved: found=%v err=%v state=%+v", found, err, pending)
	}
	result, err := store.collectPreparedExecutionWithTransport(context.Background(), verifier, acquisition, identity, dir, "/fixed/marshal", transport, reader)
	if err != nil || result.OutcomeFactDigest == "" || atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("pending collect replay failed: result=%+v err=%v calls=%d", result, err, calls)
	}
}

// TestRebindOwnerSuccessorAfterCommittedResultColdReplay proves that a fresh
// CLI owner can recover an attached Supervisor after WorkerResult admission
// has been fsynced and the durable ledger has been reopened. The owner
// successor must extend the admission head; falling back to ProcessStarted
// would fork the Attempt authority chain and is rejected.
func TestRebindOwnerSuccessorAfterCommittedResultColdReplay(t *testing.T) {
	store, _, acquisition, identity, verifier, dir := rebindRecoveryStore(t)
	var initialRebindCalls int32
	bound, err := doRebind(t, store, verifier, acquisition, identity, dir, nil, &initialRebindCalls)
	if err != nil {
		t.Fatalf("initial rebind: %v", err)
	}
	if atomic.LoadInt32(&initialRebindCalls) != 1 {
		t.Fatalf("initial rebind calls=%d want 1", initialRebindCalls)
	}

	var collectCalls int32
	collectTransport := func(_ context.Context, options processsupervisor.AttachOptions, callback func(AttachedRebindSession) error) error {
		atomic.AddInt32(&collectCalls, 1)
		return callback(&fakeRebindSession{authority: options.Authority, collect: func(prepared processsupervisor.PreparedCommand) processsupervisor.VerifiedCommandOutcome {
			return fakeCollectOutcome(prepared, bound)
		}})
	}
	reader := func(options processsupervisor.CollectedTranscriptReadOptions) (processsupervisor.CollectedTranscript, error) {
		return processsupervisor.CollectedTranscript{Stdout: []byte(`{"ok":true}`), Report: *options.Outcome.ProcessReport, TranscriptDigest: options.Outcome.TranscriptDigest}, nil
	}
	collected, err := store.collectPreparedExecutionWithTransport(context.Background(), verifier, acquisition, identity, dir, "/fixed/marshal", collectTransport, reader)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if atomic.LoadInt32(&collectCalls) != 1 {
		t.Fatalf("collect calls=%d want 1", collectCalls)
	}
	drc, envelope := attemptTestDRCForState(bound, KindWorkerResult, 1)
	drcDigest, err := drc.Digest()
	if err != nil {
		t.Fatal(err)
	}
	// Commit through the same fsyncing durable seam used by governed admission.
	// The synthetic Pi fixture intentionally has no host files to Reopen, so the
	// public capability preflight is covered elsewhere; this test isolates the
	// crash boundary after its already-validated admission decision.
	var admission AdmissionFact
	var admittedAuthorityHead string
	projection := newAuthorityProjection()
	err = store.transact(projection, func() error {
		key, err := identity.Key()
		if err != nil {
			return err
		}
		governed, found := projection.attempts[key]
		if !found {
			return ErrAttemptAuthorityUnknown
		}
		ledgerSequence := projection.ledgerSequence + 1
		factInput, err := json.Marshal(struct {
			DRCDigest        string       `json:"drcDigest"`
			EnvelopeKind     EnvelopeKind `json:"envelopeKind"`
			EnvelopeSequence uint64       `json:"envelopeSequence"`
			EnvelopeDigest   string       `json:"envelopeDigest"`
			LedgerSequence   uint64       `json:"ledgerSequence"`
		}{drcDigest, envelope.Kind, envelope.Sequence, envelope.ResultDigest, ledgerSequence})
		if err != nil {
			return err
		}
		admission = AdmissionFact{FactDigest: canonical.DigestBytes(factInput), LedgerSequence: ledgerSequence}
		admittedAuthorityHead, err = store.recordAdmittedLocked(drc.IdempotencyKey, &governed, drcDigest, envelope, admission.FactDigest, ledgerSequence, SupervisorCommandEvidence{}, collected.OutcomeFactDigest, ResultObservationBinding{})
		return err
	})
	if err != nil {
		t.Fatalf("fsync committed result: %v", err)
	}
	admitted, found, err := store.AttemptState(identity)
	if err != nil || !found {
		t.Fatalf("admitted state found=%v err=%v", found, err)
	}
	if admitted.HeadDigest != admittedAuthorityHead || admitted.CommittedResultFactDigest != admission.FactDigest || admitted.HeadDigest == admitted.ProcessStartedDigest {
		t.Fatalf("result admission did not become current head: state=%+v admission=%+v", admitted, admission)
	}

	// Reopen after the admission append to exercise cold projection replay, as
	// a new fixed-CLI process would do before acquiring its successor epoch.
	reopened, err := OpenResultIngressStore(store.dir)
	if err != nil {
		t.Fatalf("reopen after admission: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	priorOwner, found, err := reopened.OpenOwner(acquisition.Scope)
	if err != nil || !found {
		t.Fatalf("current owner found=%v err=%v", found, err)
	}
	successor := priorOwner.Acquisition
	successor.OwnerEpoch++
	successor.OwnerProcess = processsupervisor.ProcessIdentity{PID: 16000 + int(successor.OwnerEpoch), BirthSeconds: 1_700_000_040, BirthMicroseconds: int64(successor.OwnerEpoch), SessionID: 16000 + int(successor.OwnerEpoch), ProcessGroupID: 16000 + int(successor.OwnerEpoch)}
	successor.ObservedAt = "2026-08-29T00:00:40Z"
	successorVerifier := attemptOwnerVerifier{want: successor}
	if _, err := reopened.AcquireOwner(context.Background(), successorVerifier, priorOwner.Acquisition.OwnerEpoch, priorOwner.FactDigest, successor); err != nil {
		t.Fatalf("fresh owner acquire: %v", err)
	}

	var recoveryCalls int32
	recovered, err := doRebind(t, reopened, successorVerifier, successor, identity, dir, nil, &recoveryCalls)
	if err != nil {
		t.Fatalf("fresh owner rebind after admission: %v", err)
	}
	if atomic.LoadInt32(&recoveryCalls) != 1 {
		t.Fatalf("recovery calls=%d want 1", recoveryCalls)
	}
	if recovered.Revision != admitted.Revision+1 || recovered.ControlOwnerBindingRevision != recovered.Revision || recovered.ControlOwnerBindingPreviousHead != admitted.HeadDigest {
		t.Fatalf("owner successor did not extend admitted head: admitted revision/head=%d/%s recovered binding revision/previous=%d/%s", admitted.Revision, admitted.HeadDigest, recovered.ControlOwnerBindingRevision, recovered.ControlOwnerBindingPreviousHead)
	}
	if recovered.CommittedResultFactDigest != admission.FactDigest || recovered.SupervisorBoundAuthorityHead != recovered.HeadDigest {
		t.Fatalf("recovery lost result/bind authority: %+v", recovered)
	}
}

func fakeCollectOutcome(prepared processsupervisor.PreparedCommand, state AttemptAuthorityState) processsupervisor.VerifiedCommandOutcome {
	evidence := prepared.Evidence()
	started := state.ProcessStartedEvidence.Outcome
	report := processsupervisor.ProcessReport{
		State: "terminal", ObserverIdentity: started.ObserverIdentity, ObservedAt: "2026-08-29T00:01:00Z", Process: started.Process,
		RuntimeObjectDigest: started.RuntimeObjectDigest, WorkingObjectDigest: started.WorkingObjectDigest,
		SourceGateRevision: started.SourceGateRevision, ExactSetDigest: started.ExactSetDigest,
		ExitCode: 0, StdoutDigest: attemptTestDigest("collect-stdout"), StderrDigest: attemptTestDigest("collect-stderr"), StdoutBytes: 11,
	}
	payload, _ := processsupervisor.CanonicalProtocolMessage(report)
	observation := canonical.DigestBytes(payload)
	result := processsupervisor.MechanicsResult{Disposition: "ok", ReasonCode: "transcript-collected", ObservationDigest: observation, TranscriptDigest: observation, StdoutBytes: report.StdoutBytes, Payload: payload}
	receipt, _ := canonicalDigest(result)
	commandHead, _ := canonicalDigest(struct {
		Previous string `json:"previousCommandDigest"`
		Request  string `json:"requestDigest"`
		Receipt  string `json:"receiptDigest"`
	}{evidence.PreviousCommandDigest, evidence.RequestDigest, receipt})
	post := evidence.PreCommand
	post.CommandSequence, post.CommandHead = evidence.Sequence, commandHead
	post.JournalSequence += 2
	post.JournalHead = receipt
	return processsupervisor.VerifiedCommandOutcome{
		Command: processsupervisor.CommandCollect, CommandID: evidence.CommandID, Sequence: evidence.Sequence, Status: "ok", Disposition: "ok", ReasonCode: "transcript-collected",
		RequestDigest: evidence.RequestDigest, ReceiptDigest: receipt, ObservationDigest: observation, CommandHead: commandHead, TranscriptDigest: observation,
		StdoutBytes: report.StdoutBytes, ProcessReport: &report, Recovery: processsupervisor.CommandRecoveryEvidence{PreCommand: evidence.PreCommand, PostCommand: post},
	}
}

func TestRebindOwnerSuccessorForAttachedRecoveryExactReplayIdempotent(t *testing.T) {
	store, _, acquisition, identity, verifier, dir := rebindRecoveryStore(t)
	var calls int32
	first, err := doRebind(t, store, verifier, acquisition, identity, dir, nil, &calls)
	if err != nil {
		t.Fatalf("first rebind: %v", err)
	}
	checkpoints := len(first.SupervisorCommandCheckpoints)
	second, err := doRebind(t, store, verifier, acquisition, identity, dir, nil, &calls)
	if err != nil {
		t.Fatalf("second rebind: %v", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("replay invoked the transport: calls=%d", calls)
	}
	if second.Revision != first.Revision || second.HeadDigest != first.HeadDigest || second.SupervisorBoundAuthorityHead != first.SupervisorBoundAuthorityHead || len(second.SupervisorCommandCheckpoints) != checkpoints {
		t.Fatal("replay did not return the exact durable state")
	}
}

func TestRebindOwnerSuccessorForAttachedRecoveryAcrossThreeColdOwnerEpochs(t *testing.T) {
	store, _, acquisition, identity, verifier, dir := rebindRecoveryStore(t)
	var calls int32
	current, err := doRebind(t, store, verifier, acquisition, identity, dir, nil, &calls)
	if err != nil {
		t.Fatal(err)
	}
	for generation := 0; generation < 2; generation++ {
		reopened, err := OpenResultIngressStore(store.dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		prior, found, err := reopened.OpenOwner(acquisition.Scope)
		if err != nil || !found {
			t.Fatalf("owner found=%v err=%v", found, err)
		}
		next := prior.Acquisition
		next.OwnerEpoch++
		next.OwnerProcess = processsupervisor.ProcessIdentity{PID: 17000 + int(next.OwnerEpoch), BirthSeconds: 1_700_000_050, BirthMicroseconds: int64(next.OwnerEpoch), SessionID: 17000 + int(next.OwnerEpoch), ProcessGroupID: 17000 + int(next.OwnerEpoch)}
		next.ObservedAt = "2026-08-29T00:00:50Z"
		nextVerifier := attemptOwnerVerifier{want: next}
		if _, err := reopened.AcquireOwner(context.Background(), nextVerifier, prior.Acquisition.OwnerEpoch, prior.FactDigest, next); err != nil {
			t.Fatal(err)
		}
		recovered, err := doRebind(t, reopened, nextVerifier, next, identity, dir, nil, &calls)
		if err != nil {
			t.Fatalf("cold owner generation %d: %v", generation+2, err)
		}
		if recovered.Revision != current.Revision+1 || recovered.ControlOwnerBindingPreviousHead != current.HeadDigest || recovered.SupervisorBoundAuthorityHead != recovered.HeadDigest {
			t.Fatalf("cold owner did not extend exact predecessor: before=%+v after=%+v", current, recovered)
		}
		store, acquisition, verifier, current = reopened, next, nextVerifier, recovered
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("transport calls=%d want 3", calls)
	}
}

// TestRebindOwnerSuccessorForAttachedRecoveryReplayContinuesExactSuccessor proves
// that an exact already-persisted control-owner-bound successor (from a prior
// recovery that crashed after append but before bind) can be replay-continued
// without creating a sibling successor.
func TestRebindOwnerSuccessorForAttachedRecoveryReplayContinuesExactSuccessor(t *testing.T) {
	store, started, acquisition, identity, verifier, dir := rebindRecoveryStore(t)
	successorOwner, found, err := store.OpenOwner(acquisition.Scope)
	if err != nil || !found {
		t.Fatalf("successor owner found=%v err=%v", found, err)
	}
	successorBinding := CurrentOwnerBinding{Scope: acquisition.Scope, OwnerEpoch: acquisition.OwnerEpoch, ControlOwnerAcquiredFactDigest: successorOwner.FactDigest}
	run := attemptTestRunAuthority(identity)
	bound, err := store.BindOwnerToAttempt(context.Background(), verifier, attemptRunVerifier{want: run}, started.Revision, started.HeadDigest, AttemptAuthorizationRequest{Identity: identity, CurrentRunAuthority: run}, successorBinding)
	if err != nil {
		t.Fatalf("manual successor: %v", err)
	}
	var calls int32
	state, err := doRebind(t, store, verifier, acquisition, identity, dir, nil, &calls)
	if err != nil {
		t.Fatalf("replay-continue: %v", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("transport calls=%d want 1", calls)
	}
	if state.Revision != bound.State.Revision {
		t.Fatalf("replay-continue advanced revision: got=%d want=%d", state.Revision, bound.State.Revision)
	}
	if state.SupervisorBoundAuthorityHead != state.HeadDigest {
		t.Fatal("replay-continue did not bind the successor head")
	}
}

func TestRebindOwnerSuccessorForAttachedRecoveryRejectsSiblingSuccessor(t *testing.T) {
	store, started, acquisition, identity, verifier, dir := rebindRecoveryStore(t)
	// Acquire a DIFFERENT successor epoch to simulate a sibling.
	scope := started.Owner.Scope
	prior, _, _ := store.OpenOwner(scope)
	siblingEpoch := prior.Acquisition.OwnerEpoch + 1
	siblingProcess := processsupervisor.ProcessIdentity{PID: 15000 + int(siblingEpoch), BirthSeconds: 1_700_000_030, BirthMicroseconds: int64(siblingEpoch), SessionID: 15000 + int(siblingEpoch), ProcessGroupID: 15000 + int(siblingEpoch)}
	sibling := prior.Acquisition
	sibling.OwnerEpoch = siblingEpoch
	sibling.OwnerProcess = siblingProcess
	sibling.ObservedAt = "2026-08-29T00:00:30Z"
	siblingVerifier := attemptOwnerVerifier{want: sibling}
	if _, err := store.AcquireOwner(context.Background(), siblingVerifier, prior.Acquisition.OwnerEpoch, prior.FactDigest, sibling); err != nil {
		t.Fatalf("sibling acquire: %v", err)
	}
	siblingOwner, _, _ := store.OpenOwner(scope)
	siblingBinding := CurrentOwnerBinding{Scope: scope, OwnerEpoch: siblingEpoch, ControlOwnerAcquiredFactDigest: siblingOwner.FactDigest}
	run := attemptTestRunAuthority(identity)
	bound, err := store.BindOwnerToAttempt(context.Background(), siblingVerifier, attemptRunVerifier{want: run}, started.Revision, started.HeadDigest, AttemptAuthorizationRequest{Identity: identity, CurrentRunAuthority: run}, siblingBinding)
	if err != nil {
		t.Fatalf("sibling successor: %v", err)
	}
	successorRevision, successorHead := bound.State.Revision, bound.State.HeadDigest
	var calls int32
	_, err = doRebind(t, store, verifier, acquisition, identity, dir, nil, &calls)
	if err == nil {
		t.Fatal("recovery admitted a sibling successor")
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("recovery invoked the transport on a sibling: calls=%d", calls)
	}
	after, found, err := store.AttemptState(identity)
	if err != nil || !found {
		t.Fatal(err)
	}
	if after.Revision != successorRevision || after.HeadDigest != successorHead {
		t.Fatal("recovery mutated the ledger on a rejected sibling successor")
	}
}

func TestRebindOwnerSuccessorForAttachedRecoveryRejectsStaleAcquisition(t *testing.T) {
	store, _, acquisition, identity, verifier, dir := rebindRecoveryStore(t)
	stale := acquisition
	stale.OwnerEpoch = acquisition.OwnerEpoch + 1
	var calls int32
	_, err := doRebind(t, store, verifier, stale, identity, dir, nil, &calls)
	if err == nil {
		t.Fatal("stale acquisition admitted recovery")
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("stale acquisition invoked the transport: calls=%d", calls)
	}
}

// TestRebindOwnerSuccessorForAttachedRecoverySameOwnerPendingReplay proves that
// a same-owner pending bind intent (transport loss during a prior recovery) is
// replayed through Attach and the outcome is appended, closing the pending
// intent.
func TestRebindOwnerSuccessorForAttachedRecoverySameOwnerPendingReplay(t *testing.T) {
	store, _, acquisition, identity, verifier, dir := rebindRecoveryStore(t)
	var firstCalls int32
	failing := fakeRebindTransportFactory(errors.New("transport execute lost"), &firstCalls)
	_, err := store.rebindOwnerSuccessorForAttachedRecoveryWithTransport(context.Background(), verifier, acquisition, identity, dir, "/fixed/marshal", failing)
	if err == nil {
		t.Fatal("failing first recovery did not surface an error")
	}
	if atomic.LoadInt32(&firstCalls) != 1 {
		t.Fatalf("first recovery transport calls=%d", firstCalls)
	}
	// The intent is now durably pending; a second recovery replays it.
	var secondCalls int32
	state, err := doRebind(t, store, verifier, acquisition, identity, dir, nil, &secondCalls)
	if err != nil {
		t.Fatalf("pending replay: %v", err)
	}
	if atomic.LoadInt32(&secondCalls) != 1 {
		t.Fatalf("pending replay transport calls=%d want 1", secondCalls)
	}
	if state.SupervisorBoundAuthorityHead != state.HeadDigest {
		t.Fatal("pending replay did not bind the successor head")
	}
	if state.SupervisorPendingIntentDigest != "" {
		t.Fatal("pending replay left a pending intent")
	}
}

var _ = canonical.DigestBytes
