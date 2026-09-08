//go:build darwin && arm64

package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

type teamProgressStub struct {
	run                application.RunProjection
	err                error
	collects, verifies int
	badResult          bool
}

func (s *teamProgressStub) InspectRun(context.Context, application.InspectRunRequest) (application.RunProjection, error) {
	return s.run, nil
}

func (s *teamProgressStub) CollectRunResult(_ context.Context, r application.CollectRunResultRequest) (application.CollectedRunProjection, error) {
	s.collects++
	if s.err != nil {
		return application.CollectedRunProjection{}, s.err
	}
	s.run.State = domain.StateVerifying
	s.run.Sequence++
	s.run.AuthorityHead = canonical.DigestBytes([]byte("collected"))
	if s.badResult {
		s.run.RunID = "unrelated-run"
	}
	d := canonical.DigestBytes([]byte("fixture"))
	return application.CollectedRunProjection{ProtocolRevision: application.FullLifecycleProtocolRevision, Run: s.run, AdmissionFactDigest: d, DRCDigest: d, EnvelopeDigest: d}, nil
}

func (s *teamProgressStub) VerifyRun(_ context.Context, r application.VerifyRunRequest) (application.VerificationProjection, error) {
	s.verifies++
	if s.err != nil {
		return application.VerificationProjection{}, s.err
	}
	s.run.State = domain.StateReviewPending
	s.run.Sequence++
	s.run.AuthorityHead = canonical.DigestBytes([]byte("verified"))
	if s.badResult {
		s.run.AttemptID = "unrelated-attempt"
	}
	d := canonical.DigestBytes([]byte("fixture"))
	return application.VerificationProjection{ProtocolRevision: application.FullLifecycleProtocolRevision, Run: s.run, Status: "pass", ReportDigest: d, ArtifactManifestDigest: d}, nil
}

func progressRunFixture() application.RunProjection {
	return application.RunProjection{TaskID: "task-progress", RunID: "run-progress", AttemptID: "attempt-progress", State: domain.StateRunning, Sequence: 3, AuthorityHead: canonical.DigestBytes([]byte("running"))}
}

func TestTeamProgressCollectVerifyAndStaleSelections(t *testing.T) {
	port := &teamProgressStub{run: progressRunFixture()}
	initial := port.run
	if err := advanceTeamRun(context.Background(), port, initial); err != nil {
		t.Fatal(err)
	}
	if port.run.State != domain.StateVerifying || port.collects != 1 || port.verifies != 0 {
		t.Fatal("did not perform just collection")
	}
	if err := advanceTeamRun(context.Background(), port, initial); err != nil {
		t.Fatal(err)
	}
	if port.collects != 1 {
		t.Fatal("stale collect replayed")
	}
	verifying := port.run
	if err := advanceTeamRun(context.Background(), port, verifying); err != nil {
		t.Fatal(err)
	}
	if port.run.State != domain.StateReviewPending || port.verifies != 1 {
		t.Fatal("did not stop at independent review")
	}
	if err := advanceTeamRun(context.Background(), port, verifying); err != nil {
		t.Fatal(err)
	}
	if port.verifies != 1 {
		t.Fatal("stale verification repeated")
	}
	if err := advanceTeamRun(context.Background(), port, port.run); err == nil {
		t.Fatal("review was treated as automatic acceptance")
	}
}

func TestTeamProgressFailureAndPositiveLiveness(t *testing.T) {
	for _, phase := range []domain.State{domain.StateRunning, domain.StateVerifying} {
		for _, reason := range []application.ReasonCode{application.ReasonAttemptStillRunning, application.ReasonAuthorityConflict, application.ReasonRunStopped, application.ReasonRecoveryRequired} {
			t.Run(string(phase)+"/"+string(reason), func(t *testing.T) {
				port := &teamProgressStub{run: progressRunFixture(), err: application.NewError("fixture", reason)}
				port.run.State = phase
				err := advanceTeamRun(context.Background(), port, port.run)
				waiting := phase == domain.StateRunning && reason == application.ReasonAttemptStillRunning
				if (err == nil) != waiting || port.collects+port.verifies != 1 {
					t.Fatal("error retried or hidden")
				}
			})
		}
		port := &teamProgressStub{run: progressRunFixture(), badResult: true}
		port.run.State = phase
		if err := advanceTeamRun(context.Background(), port, port.run); err == nil {
			t.Fatal("foreign result accepted")
		}
	}
	port := &teamProgressStub{run: progressRunFixture(), err: context.Canceled}
	if err := advanceTeamRun(context.Background(), port, port.run); !errors.Is(err, context.Canceled) {
		t.Fatal("lost operation cancellation")
	}
}
