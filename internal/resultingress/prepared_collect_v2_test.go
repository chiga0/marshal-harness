//go:build darwin && arm64

package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

type fakeContinuationV2 struct {
	observation processsupervisor.AttachObservationV2
	execute     func(processsupervisor.PreparedCommandV2) (processsupervisor.VerifiedCommandOutcomeV2, error)
	inspect     func(processsupervisor.PreparedCommandV2) (processsupervisor.VerifiedCommandOutcomeV2, error)
	close       func(processsupervisor.PreparedCommandV2) (processsupervisor.VerifiedCommandOutcomeV2, error)
}

func (f fakeContinuationV2) Observation() (processsupervisor.AttachObservationV2, error) {
	return f.observation, nil
}
func (f fakeContinuationV2) ExecutePreparedCollect(_ context.Context, p processsupervisor.PreparedCommandV2) (processsupervisor.VerifiedCommandOutcomeV2, error) {
	return f.execute(p)
}
func (f fakeContinuationV2) ExecutePreparedInspect(_ context.Context, p processsupervisor.PreparedCommandV2) (processsupervisor.VerifiedCommandOutcomeV2, error) {
	if f.inspect != nil {
		return f.inspect(p)
	}
	return processsupervisor.VerifiedCommandOutcomeV2{}, ErrPreparedExecutionConflict
}
func (f fakeContinuationV2) ExecutePreparedClose(_ context.Context, p processsupervisor.PreparedCommandV2) (processsupervisor.VerifiedCommandOutcomeV2, error) {
	if f.close != nil {
		return f.close(p)
	}
	return processsupervisor.VerifiedCommandOutcomeV2{}, ErrPreparedExecutionConflict
}

func testLauncherV2Collect(t *testing.T, fixture preparedExecutionFixture, state AttemptAuthorityState, owner ControlOwnerState, verifier attemptOwnerVerifier, directory *os.File) {
	t.Helper()
	store := fixture.store
	var report processsupervisor.ProcessReport
	for _, c := range state.SupervisorCommandCheckpoints {
		v := c.Evidence.Outcome
		if v.State == SupervisorProcessRunning {
			report = processsupervisor.ProcessReport{State: "terminal", ObserverIdentity: v.ObserverIdentity, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Process: v.Process,
				RuntimeObjectDigest: v.RuntimeObjectDigest, WorkingObjectDigest: v.WorkingObjectDigest, SourceGateRevision: v.SourceGateRevision, ExactSetDigest: v.ExactSetDigest}
		}
	}
	stdout := []byte("synthetic business result")
	report.StdoutDigest, report.StderrDigest = canonical.DigestBytes(stdout), canonical.DigestBytes(nil)
	report.StdoutBytes = uint64(len(stdout))
	if processsupervisor.ValidateDormantV2ProcessReport(report) != nil {
		t.Fatal("invalid v2 terminal report fixture")
	}
	calls, executes, reads := 0, 0, 0
	var committed processsupervisor.VerifiedCommandOutcomeV2
	badPeer, lostReply, failRead := true, true, true
	transport := func(ctx context.Context, o processsupervisor.AttachOptionsV2, fn func(attachedContinuationV2) error) error {
		calls++
		observation := testRebindObservationV2(t, o.Authority)
		if badPeer {
			observation.Response.Authority.Child.PID++
		}
		if committed.Preparation.CommandID != "" && o.Authority.PreviousSupervisor != committed.PostCommand {
			t.Fatal("collect recovery did not authenticate post checkpoint")
		}
		return o.OwnerVerifier.WithCurrentAttachOwnerV2(ctx, o.Authority, func() error {
			return fn(fakeContinuationV2{observation: observation, execute: func(p processsupervisor.PreparedCommandV2) (processsupervisor.VerifiedCommandOutcomeV2, error) {
				executes++
				intent, err := NewSupervisorCommandIntentV2(p.Evidence())
				if err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(store.ledgerPath())
				if err != nil {
					t.Fatal(err)
				}
				lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
				var fact supervisorCommandFact
				if json.Unmarshal(lines[len(lines)-1], &fact) != nil || fact.Intent != intent || fact.ProtocolRevision != p.Evidence().PreCommand.Generation.CommandRecoveryRevision {
					t.Fatal("collect sent before exact intent fsync")
				}
				evidence := testCommandOutcomeV2(t, intent, &report, nil)
				committed, err = verifiedCollectOutcomeV2(evidence)
				if err != nil {
					t.Fatal(err)
				}
				if lostReply {
					return processsupervisor.VerifiedCommandOutcomeV2{}, processsupervisor.ErrIntervention
				}
				return committed, nil
			}})
		})
	}
	observer := func(_ context.Context, o processsupervisor.PreparedJournalOptionsV2) (processsupervisor.PreparedJournalObservationV2, error) {
		if o.Prepared.Evidence() != committed.Preparation {
			t.Fatal("collect retry changed original command")
		}
		return processsupervisor.PreparedJournalObservationV2{Reconciliation: processsupervisor.ReconciliationReceiptCommitted, Outcome: &committed}, nil
	}
	reader := func(o processsupervisor.CollectedTranscriptReadOptionsV2) (processsupervisor.CollectedTranscript, error) {
		reads++
		if !reflect.DeepEqual(o.Outcome, committed) || o.ControlDirectory != directory {
			t.Fatal("reader not bound to exact collected outcome")
		}
		if failRead {
			return processsupervisor.CollectedTranscript{}, processsupervisor.ErrConflict
		}
		return processsupervisor.CollectedTranscript{Stdout: append([]byte(nil), stdout...), Report: report, TranscriptDigest: committed.TranscriptDigest}, nil
	}
	run := func() (PreparedExecutionTranscript, error) {
		var result PreparedExecutionTranscript
		err := withCurrentOwnerLock(context.Background(), verifier, owner.Acquisition, func() error {
			projection := newAuthorityProjection()
			return store.transact(projection, func() error {
				key, err := state.Identity.Key()
				if err != nil {
					return err
				}
				result, err = store.collectPreparedExecutionV2Locked(context.Background(), projection, projection.attempts[key], owner, state.Identity, directory, owner.Acquisition.OwnerBinary.CanonicalPath, transport, reader, observer)
				return err
			})
		})
		return result, err
	}
	if _, err := run(); err == nil || executes != 0 {
		t.Fatal("bad peer collected output")
	}
	badPeer = false
	if _, err := run(); !errors.Is(err, processsupervisor.ErrIntervention) || executes != 1 || reads != 0 {
		t.Fatal("lost collect response lost pending semantics")
	}
	pending, found, err := store.AttemptState(state.Identity)
	if err != nil || !found || pending.SupervisorPendingIntentDigest == "" {
		t.Fatal("collect intent not durable")
	}
	if _, err := run(); !errors.Is(err, processsupervisor.ErrConflict) || executes != 1 || reads != 1 {
		t.Fatal("receipt recovery duplicated command or ignored read failure")
	}
	saved, found, err := store.AttemptState(state.Identity)
	if err != nil || !found || saved.SupervisorPendingIntentDigest != "" || saved.SupervisorCommandSequence != state.SupervisorCommandSequence+1 {
		t.Fatal("read failure erased accepted collect receipt")
	}
	failRead = false
	result, err := run()
	if err != nil || calls != 3 || executes != 1 || reads != 2 || string(result.Transcript.Stdout) != string(stdout) || result.OutcomeFactDigest == "" {
		t.Fatalf("collect reread: %v calls=%d executes=%d reads=%d", err, calls, executes, reads)
	}
	checkpoint, ok := latestSuccessfulCollect(saved)
	if !ok || result.OutcomeFactDigest != checkpoint.FactDigest {
		t.Fatal("result admission reference lost")
	}
	bytesOnDisk, err := os.ReadFile(store.ledgerPath())
	if err != nil || bytes.Contains(bytesOnDisk, stdout) {
		t.Fatal("transcript bytes entered RB1")
	}
	testLauncherV2Terminal(t, fixture, saved, owner, verifier, directory, report)
}
