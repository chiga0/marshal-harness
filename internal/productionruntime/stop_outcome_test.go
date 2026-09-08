package productionruntime

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// These fixtures test the production materialization path after its caller's
// authority checks. They deliberately do not manufacture a stopped Run or
// claim that these synthetic digests prove stop/cleanup authorization.
func stoppedOutcomeFixture(t *testing.T, reason string) (string, *runstore.Lease, domain.OutcomeBundle) {
	t.Helper()
	root := t.TempDir()
	lease, err := runstore.New(root).Acquire("run-outcome")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	evidence := canonical.DigestBytes([]byte("synthetic-stopped-event-payload"))
	return root, lease, domain.OutcomeBundle{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindOutcome,
		TaskID: "task-outcome", RunID: "run-outcome", TerminalState: domain.StateBlocked,
		Verdict: "abort", FinalReviewRound: 1, FinalReviewDigest: evidence, FinalEvidenceDigest: evidence,
		Summary: reason, RetentionPolicy: "default", GeneratedAt: time.Date(2026, 9, 6, 1, 2, 3, 4000, time.UTC),
	}
}

func TestStoppedOutcomeRepairsPartialFilesAcrossColdLeaseWithoutChangingBytes(t *testing.T) {
	for _, reason := range []string{"aborted-by-operator", "attempt-deadline-exceeded", "run-deadline-exceeded"} {
		t.Run(reason, func(t *testing.T) {
			root, lease, outcome := stoppedOutcomeFixture(t, reason)
			directory := filepath.Join(root, "runs", outcome.RunID)
			// Fail the second immutable write: Outcome is already durable but
			// result.md cannot be installed. No success digest may escape.
			summaryPath := filepath.Join(directory, "result.md")
			if err := os.Mkdir(summaryPath, 0o700); err != nil {
				t.Fatal(err)
			}
			if digest, err := materializeStoppedOutcome(lease, outcome); err == nil || digest != "" {
				t.Fatalf("partial write reported success: digest=%q err=%v", digest, err)
			}
			original, err := os.ReadFile(filepath.Join(directory, "outcome.json"))
			if err != nil {
				t.Fatal(err)
			}
			want, err := canonical.DigestJSON(original)
			if err != nil {
				t.Fatal(err)
			}
			if err := lease.Release(); err != nil {
				t.Fatal(err)
			}
			// Remove only this test-owned empty obstruction, not authority data.
			if err := os.Remove(summaryPath); err != nil {
				t.Fatal(err)
			}
			cold, err := runstore.New(root).AcquireExisting(outcome.RunID)
			if err != nil {
				t.Fatal(err)
			}
			defer cold.Release()
			for range 2 {
				if got, err := materializeStoppedOutcome(cold, outcome); err != nil || got != want {
					t.Fatalf("cold/repeated materialization drifted: digest=%q err=%v", got, err)
				}
			}
			recovered, err := os.ReadFile(filepath.Join(directory, "outcome.json"))
			if err != nil || !bytes.Equal(original, recovered) {
				t.Fatalf("Outcome changed across recovery: %v", err)
			}
			var decoded domain.OutcomeBundle
			if json.Unmarshal(recovered, &decoded) != nil || !decoded.GeneratedAt.Equal(outcome.GeneratedAt) || decoded.Summary != reason {
				t.Fatal("recovery invented a timestamp or terminal reason")
			}
			summary, err := os.ReadFile(summaryPath)
			if err != nil || !bytes.Contains(summary, []byte(reason)) || !bytes.Contains(summary, []byte(outcome.FinalEvidenceDigest)) {
				t.Fatal("missing exact stopped evidence summary")
			}
		})
	}
}

func TestStoppedOutcomeRejectsConflictAndLinkedTargetsWithoutOverwrite(t *testing.T) {
	for _, name := range []string{"outcome.json", "result.md"} {
		for _, linked := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "-conflict", true: "-symlink"}[linked], func(t *testing.T) {
				root, lease, outcome := stoppedOutcomeFixture(t, "attempt-deadline-exceeded")
				path := filepath.Join(root, "runs", outcome.RunID, name)
				protected := path
				if linked {
					protected = filepath.Join(root, "protected-fixture.txt")
					if err := os.Symlink(protected, path); err != nil {
						t.Fatal(err)
					}
				}
				before := []byte("conflicting bytes must survive")
				if err := os.WriteFile(protected, before, 0o600); err != nil {
					t.Fatal(err)
				}
				if digest, err := materializeStoppedOutcome(lease, outcome); err == nil || digest != "" {
					t.Fatalf("conflict reported success: digest=%q err=%v", digest, err)
				}
				after, err := os.ReadFile(protected)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("conflicting/linked content was overwritten")
				}
			})
		}
	}
}

func TestStoppedOutcomeRejectsClosedLease(t *testing.T) {
	_, lease, outcome := stoppedOutcomeFixture(t, "run-deadline-exceeded")
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if digest, err := materializeStoppedOutcome(lease, outcome); err == nil || digest != "" {
		t.Fatal("closed lease materialized Outcome")
	}
}
