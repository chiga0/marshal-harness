package review

import (
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/verification"
)

// buildRoundWith builds a Review Packet round from explicit inputs instead of
// the fixture's legacy defaults, so tests can mix legacy and candidate-mode
// rounds within one run directory.
func buildRoundWith(t *testing.T, fixture reviewFixture, round uint, report verification.Report, reportData []byte, manifest verification.ArtifactManifest, manifestData []byte) (*domain.ReviewPacket, string) {
	t.Helper()
	builder := PacketBuilder{RunDirectory: fixture.directory, Validator: fixture.validator}
	packet, digest, err := builder.Build(PacketBuildInput{Task: fixture.task, TaskData: fixture.taskData, Report: report, ReportData: reportData, Manifest: manifest, ManifestData: manifestData, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, BaseSHA: packetTestSHA("1"), ReviewRound: round, AttemptsUsed: round})
	if err != nil {
		t.Fatal(err)
	}
	return packet, digest
}

// commitReworkRoundWithFinding imports a rework decision carrying exactly one
// blocking finding for the round and persists the round records, so the next
// Build observes the finding as a PreviousFinding.
func commitReworkRoundWithFinding(t *testing.T, fixture reviewFixture, round uint, report verification.Report, reportData []byte, manifest verification.ArtifactManifest, manifestData []byte) DecisionResult {
	t.Helper()
	packet, packetDigest := buildRoundWith(t, fixture, round, report, reportData, manifest, manifestData)
	decision := validDecision(fixture, packet, packetDigest, "rework")
	decision.BlockingFindings = []domain.Finding{{ID: "F-1", Severity: "P1", Title: "并发缺陷", Description: "仍会竞态", RequiredOutcome: "新增回归证据"}}
	path := writeDecision(t, fixture.directory, decision)
	result, err := (&DecisionImporter{RunDirectory: fixture.directory, Validator: fixture.validator}).Import(DecisionInput{Path: path, Task: fixture.task, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, ReviewRound: round, AttemptsUsed: round, ReworkRoundsUsed: round - 1, Report: report, Manifest: manifest})
	if err != nil {
		t.Fatal(err)
	}
	records, err := PrepareRecords(fixture.directory, result, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := records.Commit(); err != nil {
		t.Fatal(err)
	}
	return result
}

// attemptClosure imports an accept decision that drops every previous
// blocking finding for the round built from the given inputs.
func attemptClosure(t *testing.T, fixture reviewFixture, round uint, report verification.Report, reportData []byte, manifest verification.ArtifactManifest, manifestData []byte) (*domain.ReviewPacket, error) {
	t.Helper()
	packet, packetDigest := buildRoundWith(t, fixture, round, report, reportData, manifest, manifestData)
	decision := validDecision(fixture, packet, packetDigest, "accept")
	path := writeDecision(t, fixture.directory, decision)
	_, err := (&DecisionImporter{RunDirectory: fixture.directory, Validator: fixture.validator}).Import(DecisionInput{Path: path, Task: fixture.task, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, ReviewRound: round, AttemptsUsed: round, ReworkRoundsUsed: round - 1, Report: report, Manifest: manifest})
	return packet, err
}

// TestStaleFixCandidatePathBlocksIdenticalHead proves the candidate path
// dominates the legacy comparison when both sides carry head Candidate
// identities: the worker resubmits identical bytes, re-verification
// coalesces onto the same head candidateDigest, and the finding cannot be
// closed even though the report bytes (and therefore the legacy
// verificationDigest comparison) changed. No false pass at the adoption
// boundary (§5.2, §8 R6).
func TestStaleFixCandidatePathBlocksIdenticalHead(t *testing.T) {
	fixture := newReviewFixture(t)
	workerCandidate, headCandidate := packetTestDigest("a"), packetTestDigest("b")
	report, reportData, manifest, manifestData := candidateFixtureInputs(t, fixture, workerCandidate, headCandidate, "round one")
	commitReworkRoundWithFinding(t, fixture, 1, report, reportData, manifest, manifestData)

	// Identical observed bytes and identical head Candidate, but fresh report
	// bytes: the legacy SnapshotDigest+VerificationDigest pair would call the
	// evidence changed; the candidate comparison must override it.
	report2, reportData2, manifest2, manifestData2 := candidateFixtureInputs(t, fixture, workerCandidate, headCandidate, "round two")
	if verificationDigestOf(t, reportData) == verificationDigestOf(t, reportData2) {
		t.Fatal("test precondition broken: report bytes must differ between rounds")
	}
	packet, err := attemptClosure(t, fixture, 2, report2, reportData2, manifest2, manifestData2)
	if err == nil || !strings.Contains(err.Error(), "cannot close without new evidence") {
		t.Fatalf("identical head candidate allowed a stale closure: %v", err)
	}
	if len(packet.PreviousBlockingFindings) != 1 || packet.PreviousBlockingFindings[0].CandidateDigest != report.CandidateDigest {
		t.Fatalf("previous finding must carry the round-1 head candidate: %+v", packet.PreviousBlockingFindings)
	}
}

// TestStaleFixCandidatePathAllowsClosureOnNewHead proves the other half of
// the dual path: a changed head Candidate (new observed bytes admitted as a
// new record) authorizes closing the previous finding. No false block.
func TestStaleFixCandidatePathAllowsClosureOnNewHead(t *testing.T) {
	fixture := newReviewFixture(t)
	report, reportData, manifest, manifestData := candidateFixtureInputs(t, fixture, packetTestDigest("a"), packetTestDigest("b"), "round one")
	commitReworkRoundWithFinding(t, fixture, 1, report, reportData, manifest, manifestData)

	report2, reportData2, manifest2, manifestData2 := candidateFixtureInputs(t, fixture, packetTestDigest("a"), packetTestDigest("c"), "round two fixed")
	if _, err := attemptClosure(t, fixture, 2, report2, reportData2, manifest2, manifestData2); err != nil {
		t.Fatalf("new head candidate must authorize closure: %v", err)
	}
}

// TestStaleFixCrossBoundaryLegacyToCandidate covers the adoption-boundary
// rework sequence of §5.2/§7.5: a finding raised by a legacy round carries
// no candidateDigest, so the candidate-mode next round falls back to the
// legacy SnapshotDigest+VerificationDigest comparison and closes the finding
// without a misjudgement.
func TestStaleFixCrossBoundaryLegacyToCandidate(t *testing.T) {
	fixture := newReviewFixture(t)
	commitReworkRoundWithFinding(t, fixture, 1, fixture.report, fixture.reportData, fixture.manifest, fixture.manifestData)

	report2, reportData2, manifest2, manifestData2 := candidateFixtureInputs(t, fixture, packetTestDigest("a"), packetTestDigest("b"), "candidate round")
	packet, err := attemptClosure(t, fixture, 2, report2, reportData2, manifest2, manifestData2)
	if err != nil {
		t.Fatalf("cross-boundary fallback must not block a legitimate closure: %v", err)
	}
	if len(packet.PreviousBlockingFindings) != 1 {
		t.Fatalf("previous findings = %+v", packet.PreviousBlockingFindings)
	}
	previous := packet.PreviousBlockingFindings[0]
	if previous.CandidateDigest != "" {
		t.Fatalf("legacy-round finding must keep an empty candidateDigest, got %q", previous.CandidateDigest)
	}
	if previous.SnapshotDigest == "" || previous.VerificationDigest == "" {
		t.Fatalf("legacy fallback requires the frozen digest pair: %+v", previous)
	}
}

// TestStaleFixCrossBoundaryCandidateToLegacy covers the reverse boundary:
// the finding carries a candidateDigest but the current packet is legacy, so
// the fallback comparison applies and must not block the closure.
func TestStaleFixCrossBoundaryCandidateToLegacy(t *testing.T) {
	fixture := newReviewFixture(t)
	report, reportData, manifest, manifestData := candidateFixtureInputs(t, fixture, packetTestDigest("a"), packetTestDigest("b"), "candidate round")
	result := commitReworkRoundWithFinding(t, fixture, 1, report, reportData, manifest, manifestData)
	if len(result.Packet.PreviousBlockingFindings) != 0 {
		t.Fatalf("round 1 must start without previous findings: %+v", result.Packet.PreviousBlockingFindings)
	}

	packet, err := attemptClosure(t, fixture, 2, fixture.report, fixture.reportData, fixture.manifest, fixture.manifestData)
	if err != nil {
		t.Fatalf("reverse-boundary fallback must not block a legitimate closure: %v", err)
	}
	if len(packet.PreviousBlockingFindings) != 1 || packet.PreviousBlockingFindings[0].CandidateDigest != report.CandidateDigest {
		t.Fatalf("candidate-round finding must carry its head candidate: %+v", packet.PreviousBlockingFindings)
	}
}

// TestStaleFixLegacyFallbackUnchangedWithinLegacyRounds keeps the pre-adoption
// regression pinned inside this file as well: two legacy rounds with
// byte-identical evidence cannot close a dropped finding.
func TestStaleFixLegacyFallbackUnchangedWithinLegacyRounds(t *testing.T) {
	fixture := newReviewFixture(t)
	commitReworkRoundWithFinding(t, fixture, 1, fixture.report, fixture.reportData, fixture.manifest, fixture.manifestData)
	if _, err := attemptClosure(t, fixture, 2, fixture.report, fixture.reportData, fixture.manifest, fixture.manifestData); err == nil || !strings.Contains(err.Error(), "cannot close without new evidence") {
		t.Fatalf("unchanged legacy evidence allowed a stale closure: %v", err)
	}
}

// TestStaleFixRedeclaredFindingIsNeverBlocked proves a finding the reviewer
// keeps open (re-declared in the current decision) is never subject to the
// stale-fix gate, even when the head Candidate is completely unchanged.
func TestStaleFixRedeclaredFindingIsNeverBlocked(t *testing.T) {
	fixture := newReviewFixture(t)
	workerCandidate, headCandidate := packetTestDigest("a"), packetTestDigest("b")
	report, reportData, manifest, manifestData := candidateFixtureInputs(t, fixture, workerCandidate, headCandidate, "round one")
	commitReworkRoundWithFinding(t, fixture, 1, report, reportData, manifest, manifestData)

	packet, packetDigest := buildRoundWith(t, fixture, 2, report, reportData, manifest, manifestData)
	decision := validDecision(fixture, packet, packetDigest, "rework")
	decision.BlockingFindings = []domain.Finding{{ID: "F-1", Severity: "P1", Title: "并发缺陷", Description: "仍未修复", RequiredOutcome: "新增回归证据"}}
	path := writeDecision(t, fixture.directory, decision)
	if _, err := (&DecisionImporter{RunDirectory: fixture.directory, Validator: fixture.validator}).Import(DecisionInput{Path: path, Task: fixture.task, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, ReviewRound: 2, AttemptsUsed: 2, ReworkRoundsUsed: 1, Report: report, Manifest: manifest}); err != nil {
		t.Fatalf("re-declared finding must never be blocked by the stale-fix gate: %v", err)
	}
}

// verificationDigestOf computes the canonical digest of report bytes for test
// preconditions.
func verificationDigestOf(t *testing.T, reportData []byte) string {
	t.Helper()
	digest, err := canonical.DigestJSON(reportData)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
