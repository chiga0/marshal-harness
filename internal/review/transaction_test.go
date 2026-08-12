package review

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// writeDecisionWithDecidedAt submits the decision with decidedAt spelled
// exactly as given, including RFC 3339 fractional-second forms that Go's
// time.Time marshaler itself would never emit.
func writeDecisionWithDecidedAt(t *testing.T, directory string, decision domain.ReviewDecision, decidedAt string) string {
	t.Helper()
	data, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	marker := []byte(`"decidedAt":"`)
	index := bytes.Index(data, marker)
	if index < 0 {
		t.Fatal("marshaled decision has no decidedAt field")
	}
	start := index + len(marker)
	end := bytes.IndexByte(data[start:], '"')
	if end < 0 {
		t.Fatal("unterminated decidedAt value in marshaled decision")
	}
	patched := make([]byte, 0, len(data)+len(decidedAt))
	patched = append(patched, data[:start]...)
	patched = append(patched, decidedAt...)
	patched = append(patched, data[start+end:]...)
	path := filepath.Join(directory, "input-decision.json")
	if err := os.WriteFile(path, patched, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// storeRoundRecord runs the review transaction exactly as the CLI applies it
// and returns the persisted round decision bytes.
func storeRoundRecord(t *testing.T, fixture reviewFixture, result DecisionResult) []byte {
	t.Helper()
	records, err := PrepareRecords(fixture.directory, result, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := records.Commit(); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(fixture.directory, "decisions", "decision-001.json"))
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func assertStoredDigestMatchesJournal(t *testing.T, fixture reviewFixture, stored []byte, result DecisionResult) {
	t.Helper()
	if !bytes.Equal(stored, result.DecisionData) {
		t.Fatalf("stored decision bytes diverge from the bytes the digest was computed over")
	}
	storedDigest, err := canonical.DigestJSON(stored)
	if err != nil {
		t.Fatal(err)
	}
	if storedDigest != result.DecisionDigest {
		t.Fatalf("stored decision digest %s != journal decisionDigest %s", storedDigest, result.DecisionDigest)
	}
	if err := fixture.validator.Validate(domain.KindReviewDecision, stored); err != nil {
		t.Fatalf("stored decision fails contract: %v", err)
	}
}

func TestDecisionStoredBytesMatchDigestBytes(t *testing.T) {
	fixture := newReviewFixture(t)
	packet, packetDigest := fixture.build(t, 1)
	decision := validDecision(fixture, packet, packetDigest, "accept")
	decidedAt := "2026-08-12T11:10:00.000000Z"
	path := writeDecisionWithDecidedAt(t, fixture.directory, decision, decidedAt)
	submitted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(submitted, []byte(decidedAt)) {
		t.Fatalf("submitted decision lost the fractional decidedAt: %s", submitted)
	}
	result, err := (&DecisionImporter{RunDirectory: fixture.directory, Validator: fixture.validator}).Import(DecisionInput{Path: path, Task: fixture.task, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, ReviewRound: 1, AttemptsUsed: 1, Report: fixture.report, Manifest: fixture.manifest})
	if err != nil {
		t.Fatal(err)
	}
	stored := storeRoundRecord(t, fixture, result)
	if !bytes.Contains(stored, []byte(`"decidedAt": "2026-08-12T11:10:00Z"`)) {
		t.Fatalf("stored decision does not carry the normalized decidedAt: %s", stored)
	}
	assertStoredDigestMatchesJournal(t, fixture, stored, result)
}

func TestDecisionFractionalSecondTimestampRoundTrips(t *testing.T) {
	for _, decidedAt := range []string{"2026-08-12T11:10:00.000000Z", "2026-08-12T11:10:00Z"} {
		t.Run(decidedAt, func(t *testing.T) {
			fixture := newReviewFixture(t)
			packet, packetDigest := fixture.build(t, 1)
			decision := validDecision(fixture, packet, packetDigest, "rework")
			decision.BlockingFindings = []domain.Finding{{ID: "F-1", Severity: "P1", Title: "digest regression", Description: "stored decision digest must match the journal", RequiredOutcome: "fix the digest mismatch"}}
			path := writeDecisionWithDecidedAt(t, fixture.directory, decision, decidedAt)
			result, err := (&DecisionImporter{RunDirectory: fixture.directory, Validator: fixture.validator}).Import(DecisionInput{Path: path, Task: fixture.task, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, ReviewRound: 1, AttemptsUsed: 1, Report: fixture.report, Manifest: fixture.manifest})
			if err != nil {
				t.Fatal(err)
			}
			if result.TargetState != domain.StateReworkRequested {
				t.Fatalf("target state = %s, want %s", result.TargetState, domain.StateReworkRequested)
			}
			stored := storeRoundRecord(t, fixture, result)
			// loadRoundBoundDecision rejects the rework worker when this
			// recomputation diverges from the journal decisionDigest.
			assertStoredDigestMatchesJournal(t, fixture, stored, result)
			var reread domain.ReviewDecision
			if err := json.Unmarshal(stored, &reread); err != nil {
				t.Fatal(err)
			}
			if reread.Verdict != "rework" || len(reread.BlockingFindings) != 1 || reread.BlockingFindings[0].ID != "F-1" {
				t.Fatalf("reread decision = %+v", reread)
			}
			if reread.DecidedAt.IsZero() || !reread.DecidedAt.Equal(result.Decision.DecidedAt) {
				t.Fatalf("decidedAt round trip failed: stored %s, imported %s", reread.DecidedAt, result.Decision.DecidedAt)
			}
		})
	}
}

func TestDecisionNoFractionalSecondStillConsistent(t *testing.T) {
	fixture := newReviewFixture(t)
	packet, packetDigest := fixture.build(t, 1)
	decision := validDecision(fixture, packet, packetDigest, "reject")
	decision.DecidedAt = decision.DecidedAt.UTC()
	path := writeDecision(t, fixture.directory, decision)
	result, err := (&DecisionImporter{RunDirectory: fixture.directory, Validator: fixture.validator}).Import(DecisionInput{Path: path, Task: fixture.task, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, ReviewRound: 1, AttemptsUsed: 1, Report: fixture.report, Manifest: fixture.manifest})
	if err != nil {
		t.Fatal(err)
	}
	stored := storeRoundRecord(t, fixture, result)
	if !bytes.Contains(stored, []byte(`"decidedAt": "2026-08-04T00:02:00Z"`)) {
		t.Fatalf("stored decision without fractional seconds changed shape: %s", stored)
	}
	assertStoredDigestMatchesJournal(t, fixture, stored, result)
}
