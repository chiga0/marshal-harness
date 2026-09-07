package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

func acceptedFixture(t *testing.T) (reviewFixture, domain.RunState, domain.RunEvent) {
	t.Helper()
	f := newReviewFixture(t)
	f.task.Repository.BaseRef = strings.Repeat("1", 40)
	var err error
	f.taskData, err = json.Marshal(f.task)
	if err != nil {
		t.Fatal(err)
	}
	f.specDigest, err = canonical.DigestJSON(f.taskData)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := os.ReadFile(filepath.Join(f.directory, "observed.patch"))
	if err != nil {
		t.Fatal(err)
	}
	content := canonical.DigestBytes(patch)
	head := materializeCandidateFixture(t, f, content)
	f.report, _, f.manifest, f.manifestData = candidateFixtureInputs(t, f, head, content, "accepted fixture")
	f.report.Observed.DiffDigest = content
	f.report.Observed.DiffBytes = int64(len(patch))
	f.reportData, err = json.Marshal(f.report)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"task-spec.json": f.taskData, "verification-report.json": f.reportData, "artifact-manifest.json": f.manifestData} {
		if err := os.WriteFile(filepath.Join(f.directory, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	packet, packetDigest := f.build(t, 1)
	decision := validDecision(f, packet, packetDigest, "accept")
	path := writeDecision(t, f.directory, decision)
	imported, err := (&DecisionImporter{RunDirectory: f.directory, Validator: f.validator}).Import(DecisionInput{
		Path: path, Task: f.task, TaskID: "ENG-123", RunID: "run-01", SpecDigest: f.specDigest,
		ReviewRound: 1, AttemptsUsed: 1, Report: f.report, Manifest: f.manifest})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 0, 3, 0, 0, time.UTC)
	records, err := PrepareRecords(f.directory, imported, TerminalOutcome("ENG-123", "run-01", domain.StateAccepted, imported, now))
	if err != nil {
		t.Fatal(err)
	}
	if err := records.Commit(); err != nil {
		t.Fatal(err)
	}
	state := domain.NewRunState("ENG-123", "run-01", now)
	state.State, state.Sequence, state.ReviewRound, state.AttemptsUsed = domain.StateAccepted, 4, 1, 1
	state.CurrentAttemptID, state.BaseSHA, state.SpecDigest = "attempt-01", f.report.BaseSHA, f.specDigest
	event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event-accepted",
		RunID: state.RunID, AttemptID: state.CurrentAttemptID, Sequence: state.Sequence, Type: "review.accept",
		StateFrom: domain.StateReviewPending, StateTo: domain.StateAccepted, Timestamp: now,
		Payload: map[string]any{"verdict": "accept", "decisionDigest": imported.DecisionDigest, "evidenceDigest": imported.Decision.EvidenceDigest}}
	return f, state, event
}

// Component fixture only: the production session must additionally prove
// owner/plan and hold real Run descriptors. This reader grants no authority.
func acceptedFixtureRead(f reviewFixture) func(int64, ...string) ([]byte, error) {
	return func(limit int64, parts ...string) ([]byte, error) {
		data, err := os.ReadFile(filepath.Join(append([]string{f.directory}, parts...)...))
		if err != nil || int64(len(data)) > limit {
			return nil, errors.New("fixture unavailable")
		}
		return data, nil
	}
}

func TestAcceptedCandidateUsesOriginalDecisionAndOutcomeProducer(t *testing.T) {
	f, state, event := acceptedFixture(t)
	result, err := ReadAcceptedCandidate(state, event, "authority-01", f.validator, acceptedFixtureRead(f))
	if err != nil || result.DecisionDigest != event.Payload["decisionDigest"] || result.Candidate.ContentDigest != canonical.DigestBytes(result.Patch) || result.OutcomeDigest == "" {
		t.Fatalf("accepted input: %+v %v", result, err)
	}
	expected, _ := os.ReadFile(filepath.Join(f.directory, "observed.patch"))
	result.Patch[0] ^= 1
	again, err := ReadAcceptedCandidate(state, event, "authority-01", f.validator, acceptedFixtureRead(f))
	if err != nil || !bytes.Equal(again.Patch, expected) {
		t.Fatal("caller changed captured source")
	}
}

func TestAcceptedCandidateRejectsIncompleteOrWrongCurrentAuthority(t *testing.T) {
	for _, mode := range []string{"pending", "wrong-attempt", "wrong-sequence", "wrong-event", "wrong-decision", "wrong-namespace"} {
		t.Run(mode, func(t *testing.T) {
			f, state, event := acceptedFixture(t)
			namespace := "authority-01"
			switch mode {
			case "pending":
				state.State = domain.StateReviewPending
			case "wrong-attempt":
				event.AttemptID = "other-attempt"
			case "wrong-sequence":
				event.Sequence++
			case "wrong-event":
				event.Type = "review.reject"
			case "wrong-decision":
				event.Payload["decisionDigest"] = packetTestDigest("9")
			case "wrong-namespace":
				namespace = "other-authority"
			}
			if _, err := ReadAcceptedCandidate(state, event, namespace, f.validator, acceptedFixtureRead(f)); err == nil {
				t.Fatal("unbound candidate accepted")
			}
		})
	}
}

func TestAcceptedCandidateRejectsEachChangedOrMissingCapturedInput(t *testing.T) {
	for _, name := range []string{"task-spec.json", "verification-report.json", "artifact-manifest.json", "review-packet.json", "review-decision.json", "outcome.json", "observed.patch", "candidate"} {
		for _, mode := range []string{"missing", "changed"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				f, state, event := acceptedFixture(t)
				path := name
				if name == "candidate" {
					path = filepath.Join("candidates", f.report.CandidateDigest+".json")
				}
				if mode == "missing" {
					if err := os.Remove(filepath.Join(f.directory, path)); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(filepath.Join(f.directory, path), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := ReadAcceptedCandidate(state, event, "authority-01", f.validator, acceptedFixtureRead(f)); err == nil {
					t.Fatal("changed accepted input consumed")
				}
			})
		}
	}
}

func TestAcceptedCandidateRejectsSchemaValidOutcomeAndDecisionDrift(t *testing.T) {
	for _, name := range []string{"outcome.json", "review-decision.json"} {
		t.Run(name, func(t *testing.T) {
			f, state, event := acceptedFixture(t)
			path := filepath.Join(f.directory, name)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var record map[string]any
			if err := json.Unmarshal(raw, &record); err != nil {
				t.Fatal(err)
			}
			record["summary"] = "schema-valid but not the committed producer's content"
			raw, err = json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			kind := domain.KindOutcome
			if name == "review-decision.json" {
				kind = domain.KindReviewDecision
			}
			if err := f.validator.Validate(kind, raw); err != nil {
				t.Fatal("negative fixture must remain schema-valid", err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadAcceptedCandidate(state, event, "authority-01", f.validator, acceptedFixtureRead(f)); err == nil {
				t.Fatal("valid syntax replaced exact committed evidence")
			}
		})
	}
}
