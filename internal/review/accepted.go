package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/verification"
)

// AcceptedCandidate is an immutable-data snapshot, not an authority token.
// Only a current-owner reader holding the real Run lease may supply the state,
// terminal event and descriptor-relative reader to ReadAcceptedCandidate.
// Downstream creation must recheck that authority; JSON cannot replace it.
type AcceptedCandidate struct {
	Candidate      domain.Candidate
	Patch          []byte
	DecisionDigest string
	PacketDigest   string
	OutcomeDigest  string
}

// ReadAcceptedCandidate reuses the actual Decision validator and Outcome
// producer. It never imports authority, edits a Run or observes mutable worker
// worktrees: integration consumes only the captured, accepted patch bytes.
func ReadAcceptedCandidate(state domain.RunState, terminal domain.RunEvent, namespace string, validator *contract.Validator, read func(int64, ...string) ([]byte, error)) (AcceptedCandidate, error) {
	fail := func() (AcceptedCandidate, error) {
		return AcceptedCandidate{}, errors.New("review: accepted candidate binding conflict")
	}
	if validator == nil || read == nil || domain.ValidateID(namespace) != nil || state.State != domain.StateAccepted ||
		terminal.Type != "review.accept" || terminal.StateFrom != domain.StateReviewPending || terminal.StateTo != domain.StateAccepted ||
		terminal.RunID != state.RunID || terminal.AttemptID != state.CurrentAttemptID || terminal.Sequence != state.Sequence ||
		terminal.Payload["verdict"] != "accept" {
		return fail()
	}
	load := func(kind domain.Kind, limit int64, value any, path ...string) ([]byte, error) {
		raw, err := read(limit, path...)
		if err != nil || validator.Validate(kind, raw) != nil || json.Unmarshal(raw, value) != nil {
			return nil, errors.New("review: accepted evidence unavailable")
		}
		return raw, nil
	}
	var task domain.TaskSpec
	taskData, err := load(domain.KindTask, 2<<20, &task, "task-spec.json")
	if err != nil {
		return fail()
	}
	specDigest, err := canonical.DigestJSON(taskData)
	if err != nil || specDigest != state.SpecDigest || task.Metadata.ID != state.TaskID || task.Repository.BaseRef != state.BaseSHA || task.Publication.Required {
		return fail()
	}
	var report verification.Report
	reportData, err := load(domain.KindVerificationReport, 8<<20, &report, "verification-report.json")
	if err != nil {
		return fail()
	}
	var manifest verification.ArtifactManifest
	manifestData, err := load(domain.KindArtifactManifest, 8<<20, &manifest, "artifact-manifest.json")
	if err != nil {
		return fail()
	}
	var packet domain.ReviewPacket
	packetData, err := load(domain.KindReviewPacket, packetByteLimit, &packet, "review-packets", fmt.Sprintf("packet-%03d.json", state.ReviewRound))
	if err != nil || packet.CandidateDigest == "" || packet.CodexEligibilityBinding != nil {
		return fail()
	}
	decisionData, err := read(packetByteLimit, "decisions", fmt.Sprintf("decision-%03d.json", state.ReviewRound))
	if err != nil {
		return fail()
	}
	input := DecisionInput{Task: task, TaskID: state.TaskID, RunID: state.RunID, SpecDigest: state.SpecDigest,
		ReviewRound: state.ReviewRound, AttemptsUsed: state.AttemptsUsed, ReworkRoundsUsed: state.ReworkRoundsUsed,
		Report: report, Manifest: manifest, LocalSelfIdentityBinding: packet.LocalSelfIdentityBinding}
	imported, err := (&DecisionImporter{Validator: validator}).importBytesWithPacket(input, decisionData, packetData)
	if err != nil || imported.TargetState != domain.StateAccepted || imported.DecisionDigest != terminal.Payload["decisionDigest"] ||
		imported.Decision.EvidenceDigest != terminal.Payload["evidenceDigest"] {
		return fail()
	}
	localDigest, localPresent := terminal.Payload["localSelfIdentityBindingDigest"]
	if imported.Decision.LocalSelfIdentityBindingDigest == "" && localPresent ||
		imported.Decision.LocalSelfIdentityBindingDigest != "" && localDigest != imported.Decision.LocalSelfIdentityBindingDigest {
		return fail()
	}
	reportDigest, reportErr := canonical.DigestJSON(reportData)
	manifestDigest, manifestErr := canonical.DigestJSON(manifestData)
	if reportErr != nil || manifestErr != nil || packet.VerificationDigest != reportDigest || packet.ArtifactManifestDigest != manifestDigest ||
		report.TaskID != state.TaskID || report.RunID != state.RunID || report.SpecDigest != state.SpecDigest || report.BaseSHA != state.BaseSHA ||
		manifest.TaskID != state.TaskID || manifest.RunID != state.RunID || packet.CandidateDigest != report.CandidateDigest ||
		packet.WorkerCandidateDigest != report.WorkerCandidateDigest || packet.DiffDigest != report.Observed.DiffDigest ||
		packet.SnapshotDigest != report.Observed.SnapshotDigest || validateCandidateBinding(report, manifest) != nil {
		return fail()
	}
	patch, err := read(packetByteLimit, "observed.patch")
	if err != nil || len(patch) == 0 || canonical.DigestBytes(patch) != packet.DiffDigest || validateObservedPatch(manifest, patch) != nil {
		return fail()
	}
	// Candidate.Validate checks detached identity; the validated Schema digest
	// is also the filename guard before this descriptor-relative read.
	raw, err := read(64<<10, "candidates", packet.CandidateDigest+".json")
	var candidate domain.Candidate
	if err != nil || validator.Validate(domain.KindCandidate, raw) != nil || json.Unmarshal(raw, &candidate) != nil || candidate.Validate() != nil ||
		candidate.CandidateDigest != packet.CandidateDigest || candidate.ContentDigest != canonical.DigestBytes(patch) ||
		candidate.TaskID != state.TaskID || candidate.RunID != state.RunID || candidate.AttemptID != state.CurrentAttemptID ||
		candidate.AuthorityNamespaceID != namespace || candidate.BaseSHA != state.BaseSHA {
		return fail()
	}
	outcomeData, err := read(2<<20, "outcome.json")
	if err != nil {
		return fail()
	}
	expected, _, err := renderOutcome(*TerminalOutcome(state.TaskID, state.RunID, domain.StateAccepted, imported, terminal.Timestamp))
	actualCanonical, actualErr := canonical.JSON(outcomeData)
	expectedCanonical, expectedErr := canonical.JSON(expected)
	if err != nil || actualErr != nil || expectedErr != nil || !bytes.Equal(actualCanonical, expectedCanonical) {
		return fail()
	}
	return AcceptedCandidate{Candidate: candidate, Patch: bytes.Clone(patch), DecisionDigest: imported.DecisionDigest,
		PacketDigest: imported.Decision.ReviewPacketDigest, OutcomeDigest: canonical.DigestBytes(actualCanonical)}, nil
}
