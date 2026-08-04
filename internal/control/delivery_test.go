package control

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

type fakeTerminalSession struct {
	identity     port.TerminalSessionIdentity
	state        port.TerminalState
	sendErr      error
	terminateErr error
	sent         []string
	terminated   bool
}

func (s *fakeTerminalSession) ID() string                              { return "fake-terminal" }
func (s *fakeTerminalSession) Identity() port.TerminalSessionIdentity  { return s.identity }
func (s *fakeTerminalSession) State() port.TerminalState               { return s.state }
func (s *fakeTerminalSession) Capabilities() []port.TerminalCapability { return nil }
func (s *fakeTerminalSession) Send(_ context.Context, _ port.TerminalInputSource, text string, _ time.Time) error {
	if s.sendErr != nil {
		return s.sendErr
	}
	s.sent = append(s.sent, text)
	return nil
}
func (*fakeTerminalSession) ReadScreen(context.Context, int) (string, error) { return "", nil }
func (*fakeTerminalSession) InterruptStep(context.Context) error             { return nil }
func (s *fakeTerminalSession) Pause(context.Context) error {
	s.state = port.TerminalPaused
	return nil
}
func (s *fakeTerminalSession) Resume(context.Context) error {
	s.state = port.TerminalRunning
	return nil
}
func (s *fakeTerminalSession) Terminate(context.Context, time.Duration) error {
	if s.terminateErr != nil {
		return s.terminateErr
	}
	s.terminated, s.state = true, port.TerminalTerminated
	return nil
}

func TestApplyInterventionDeliversBeforeRecording(t *testing.T) {
	t.Parallel()
	fixture := newApprovalFixture(t, nil, false)
	advanceFixtureToRunning(t, fixture)
	session := boundFakeSession(fixture.runID)
	input := fixture.interventionInput(domain.InterventionCategoryClarification, "解释当前验证失败。")
	input.DeliveryAccepted = false
	record, err := ApplyIntervention(context.Background(), session, input, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.sent) != 1 || record.Effect != domain.InterventionEffectContinue {
		t.Fatalf("delivery=%v record=%+v", session.sent, record)
	}
	records, err := runstore.New(fixture.root).ReadControlRecords(fixture.runID, fixture.validator)
	if err != nil || len(records) != 1 || records[0].Intervention == nil {
		t.Fatalf("control records=%+v error=%v", records, err)
	}
}

func TestApplyInterventionDoesNotRecordFailedOrMismatchedDelivery(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		configure func(*fakeTerminalSession)
		want      error
	}{
		{name: "send failure", configure: func(s *fakeTerminalSession) { s.sendErr = errors.New("send failed") }, want: ErrTerminalDelivery},
		{name: "wrong attempt", configure: func(s *fakeTerminalSession) { s.identity.AttemptID = "attempt-other" }, want: ErrTerminalBinding},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApprovalFixture(t, nil, false)
			advanceFixtureToRunning(t, fixture)
			session := boundFakeSession(fixture.runID)
			test.configure(session)
			input := fixture.interventionInput(domain.InterventionCategoryImplementationCorrection, "修正实现。")
			input.DeliveryAccepted = false
			if _, err := ApplyIntervention(context.Background(), session, input, time.Second); !errors.Is(err, test.want) {
				t.Fatalf("ApplyIntervention() = %v", err)
			}
			if records, err := runstore.New(fixture.root).ReadControlRecords(fixture.runID, fixture.validator); !errors.Is(err, os.ErrNotExist) || len(records) != 0 {
				t.Fatalf("unexpected control records=%+v error=%v", records, err)
			}
		})
	}
}

func TestApplyScopeChangeTerminatesBoundSession(t *testing.T) {
	t.Parallel()
	fixture := newApprovalFixture(t, nil, false)
	advanceFixtureToRunning(t, fixture)
	session := boundFakeSession(fixture.runID)
	input := fixture.interventionInput(domain.InterventionCategoryScopeChange, "增加新的交付物。")
	record, err := ApplyIntervention(context.Background(), session, input, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !session.terminated || record.Effect != domain.InterventionEffectNewRunRequired || record.AttemptID != "attempt-01" {
		t.Fatalf("session=%+v record=%+v", session, record)
	}
	state, err := runstore.New(fixture.root).Inspect(fixture.runID)
	if err != nil || state.State != domain.StateAborted || state.TerminalReason == "" {
		t.Fatalf("scope-changed state=%+v error=%v", state, err)
	}
}

func TestApplyPauseResumeAndAbort(t *testing.T) {
	t.Parallel()
	fixture := newApprovalFixture(t, nil, false)
	advanceFixtureToRunning(t, fixture)
	session := boundFakeSession(fixture.runID)

	pause := fixture.interventionInput(domain.InterventionCategoryPause, "")
	pauseRecord, err := ApplyIntervention(context.Background(), session, pause, time.Second)
	if err != nil || session.state != port.TerminalPaused || pauseRecord.Effect != domain.InterventionEffectPaused {
		t.Fatalf("pause session=%+v record=%+v error=%v", session, pauseRecord, err)
	}
	resume := fixture.interventionInput(domain.InterventionCategoryResume, "")
	resumeRecord, err := ApplyIntervention(context.Background(), session, resume, time.Second)
	if err != nil || session.state != port.TerminalRunning || resumeRecord.Effect != domain.InterventionEffectResumed {
		t.Fatalf("resume session=%+v record=%+v error=%v", session, resumeRecord, err)
	}
	abort := fixture.interventionInput(domain.InterventionCategoryAbort, "")
	abortRecord, err := ApplyIntervention(context.Background(), session, abort, time.Second)
	if err != nil || !session.terminated || abortRecord.Effect != domain.InterventionEffectAbortRequested {
		t.Fatalf("abort session=%+v record=%+v error=%v", session, abortRecord, err)
	}
	state, err := runstore.New(fixture.root).Inspect(fixture.runID)
	if err != nil || state.State != domain.StateAborted {
		t.Fatalf("aborted state=%+v error=%v", state, err)
	}
	records, err := runstore.New(fixture.root).ReadControlRecords(fixture.runID, fixture.validator)
	if err != nil || len(records) != 3 {
		t.Fatalf("control records=%+v error=%v", records, err)
	}
}

func boundFakeSession(runID string) *fakeTerminalSession {
	return &fakeTerminalSession{identity: port.TerminalSessionIdentity{RunID: runID, AttemptID: "attempt-01"}, state: port.TerminalRunning}
}
