package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/verification"
)

const packetByteLimit int64 = 4 << 20

type PacketBuilder struct {
	RunDirectory string
	Validator    *contract.Validator
}

type PacketBuildInput struct {
	Task         domain.TaskSpec
	TaskData     []byte
	Report       verification.Report
	ReportData   []byte
	Manifest     verification.ArtifactManifest
	ManifestData []byte
	TaskID       string
	RunID        string
	SpecDigest   string
	BaseSHA      string
	ReviewRound  uint
	AttemptsUsed uint
	// CodexEligibilityBinding is present only for a Codex Attempt admitted by
	// the ADR 0037 production authority. Legacy and non-Codex builds omit it.
	CodexEligibilityBinding *domain.CodexEligibilityBindingV1
}

type evidenceIdentity struct {
	SpecDigest             string                   `json:"specDigest"`
	PatchDigest            string                   `json:"patchDigest"`
	VerificationDigest     string                   `json:"verificationDigest"`
	ArtifactManifestDigest string                   `json:"artifactManifestDigest"`
	WorkerResultDigests    []string                 `json:"workerResultDigests"`
	PreviousFindings       []domain.PreviousFinding `json:"previousBlockingFindings"`
	// CandidateDigest and WorkerCandidateDigest are the ADR 0027 head and
	// worker Candidate record identities (§4.2 stage A). Runs verified before
	// Candidate adoption leave both empty; the omitempty tags then keep them
	// out of the canonical serialization entirely, so legacy evidenceDigest
	// recomputation stays byte-identical (§5.1/§7.5).
	CandidateDigest          string `json:"candidateDigest,omitempty"`
	WorkerCandidateDigest    string `json:"workerCandidateDigest,omitempty"`
	EligibilityBindingDigest string `json:"eligibilityBindingDigest,omitempty"`
}

func (b *PacketBuilder) Build(input PacketBuildInput) (*domain.ReviewPacket, string, error) {
	if b.Validator == nil {
		return nil, "", errors.New("contract validator is required")
	}
	if err := b.Validator.Validate(domain.KindTask, input.TaskData); err != nil {
		return nil, "", fmt.Errorf("validate task spec: %w", err)
	}
	if err := b.Validator.Validate(domain.KindVerificationReport, input.ReportData); err != nil {
		return nil, "", fmt.Errorf("validate verification report: %w", err)
	}
	if err := b.Validator.Validate(domain.KindArtifactManifest, input.ManifestData); err != nil {
		return nil, "", fmt.Errorf("validate artifact manifest: %w", err)
	}
	computedSpecDigest, err := canonical.DigestJSON(input.TaskData)
	if err != nil || computedSpecDigest != input.SpecDigest {
		return nil, "", errors.New("task spec digest does not match frozen state")
	}
	if input.Task.Metadata.ID != input.TaskID || input.Report.TaskID != input.TaskID || input.Manifest.TaskID != input.TaskID || input.Report.RunID != input.RunID || input.Manifest.RunID != input.RunID || input.Report.SpecDigest != input.SpecDigest || input.Report.BaseSHA != input.BaseSHA {
		return nil, "", errors.New("review evidence identity does not match frozen run")
	}
	patchData, err := readBounded(filepath.Join(b.RunDirectory, "observed.patch"), packetByteLimit)
	if err != nil {
		return nil, "", fmt.Errorf("read observed patch: %w", err)
	}
	if err := validateObservedPatch(input.Manifest, patchData); err != nil {
		return nil, "", err
	}
	if err := validateCandidateBinding(input.Report, input.Manifest); err != nil {
		return nil, "", err
	}
	workerPaths, workerDigests, err := b.collectWorkerResults(input.TaskID, input.RunID)
	if err != nil {
		return nil, "", err
	}
	if len(workerPaths) == 0 {
		return nil, "", errors.New("review packet requires at least one schema-valid worker result")
	}
	previous, err := b.loadPreviousFindings()
	if err != nil {
		return nil, "", err
	}
	verificationDigest, err := canonical.DigestJSON(input.ReportData)
	if err != nil {
		return nil, "", err
	}
	manifestDigest, err := canonical.DigestJSON(input.ManifestData)
	if err != nil {
		return nil, "", err
	}
	eligibilityBinding, eligibilityBindingDigest, err := validateCodexEligibilityBinding(input.CodexEligibilityBinding, input.TaskID, input.RunID)
	if err != nil {
		return nil, "", err
	}
	identity := evidenceIdentity{SpecDigest: input.SpecDigest, PatchDigest: canonical.DigestBytes(patchData), VerificationDigest: verificationDigest, ArtifactManifestDigest: manifestDigest, WorkerResultDigests: workerDigests, PreviousFindings: previous, CandidateDigest: input.Report.CandidateDigest, WorkerCandidateDigest: input.Report.WorkerCandidateDigest, EligibilityBindingDigest: eligibilityBindingDigest}
	identityData, err := json.Marshal(identity)
	if err != nil {
		return nil, "", err
	}
	evidenceDigest, err := canonical.DigestJSON(identityData)
	if err != nil {
		return nil, "", err
	}
	packet := &domain.ReviewPacket{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindReviewPacket,
		TaskID: input.TaskID, RunID: input.RunID, ReviewRound: input.ReviewRound,
		SpecDigest: input.SpecDigest, BaseSHA: input.BaseSHA,
		SnapshotDigest: input.Report.Observed.SnapshotDigest, DiffDigest: input.Report.Observed.DiffDigest,
		VerificationDigest: verificationDigest, ArtifactManifestDigest: manifestDigest,
		WorkerResultDigests: workerDigests, EvidenceDigest: evidenceDigest,
		WorkerCandidateDigest: input.Report.WorkerCandidateDigest, CandidateDigest: input.Report.CandidateDigest,
		CodexEligibilityBinding:  eligibilityBinding,
		Inputs:                   domain.PacketInputs{TaskSpec: "task-spec.json", Patch: "observed.patch", VerificationReport: "verification-report.json", ArtifactManifest: "artifact-manifest.json", WorkerResults: workerPaths},
		PreviousBlockingFindings: previous, GeneratedAt: input.Report.CompletedAt.UTC(),
	}
	packetData, err := json.MarshalIndent(packet, "", "  ")
	if err != nil {
		return nil, "", err
	}
	if int64(len(packetData)) > packetByteLimit {
		return nil, "", errors.New("review packet exceeds safe byte limit")
	}
	if err := b.Validator.Validate(domain.KindReviewPacket, packetData); err != nil {
		return nil, "", fmt.Errorf("generated review packet violates contract: %w", err)
	}
	packetDigest, err := canonical.DigestJSON(packetData)
	if err != nil {
		return nil, "", err
	}
	prompt := RenderWorkerPrompt(WorkerPromptInput{Task: input.Task, TaskID: input.TaskID, RunID: input.RunID, SpecDigest: input.SpecDigest, BaseSHA: input.BaseSHA, ReviewRound: input.ReviewRound, PreviousFindings: previous, AttemptsUsed: input.AttemptsUsed})
	if err := atomicWrite(filepath.Join(b.RunDirectory, "worker-prompt.md"), []byte(prompt), true); err != nil {
		return nil, "", fmt.Errorf("write worker prompt: %w", err)
	}
	if err := atomicWrite(filepath.Join(b.RunDirectory, "review-packet.json"), append(packetData, '\n'), true); err != nil {
		return nil, "", fmt.Errorf("write review packet: %w", err)
	}
	return packet, packetDigest, nil
}

func validateCodexEligibilityBinding(binding *domain.CodexEligibilityBindingV1, taskID, runID string) (*domain.CodexEligibilityBindingV1, string, error) {
	if binding == nil {
		return nil, "", nil
	}
	if binding.SchemaVersion != "marshal.codex.eligibility-binding.v1" {
		return nil, "", errors.New("codex eligibility binding has unsupported schema version")
	}
	if binding.TaskID != taskID || binding.RunID != runID || binding.AttemptID == "" {
		return nil, "", errors.New("codex eligibility binding identity does not match frozen run")
	}
	cloned := *binding
	data, err := json.Marshal(cloned)
	if err != nil {
		return nil, "", fmt.Errorf("marshal codex eligibility binding: %w", err)
	}
	digest, err := canonical.DigestJSON(data)
	if err != nil {
		return nil, "", fmt.Errorf("digest codex eligibility binding: %w", err)
	}
	return &cloned, digest, nil
}

func validateObservedPatch(manifest verification.ArtifactManifest, patchData []byte) error {
	for _, artifact := range manifest.Artifacts {
		if artifact.RelativePath != "observed.patch" {
			continue
		}
		if artifact.Producer != "verifier" || artifact.Status != "validated" || artifact.Digest != canonical.DigestBytes(patchData) || artifact.ByteSize != int64(len(patchData)) {
			return errors.New("observed patch does not match the frozen verifier artifact")
		}
		return nil
	}
	return errors.New("artifact manifest does not contain the observed patch")
}

// validateCandidateBinding enforces the ADR 0027 binding discipline at packet
// construction: candidate mode is all-or-nothing (the head and worker
// Candidate identities appear together or not at all), and every candidate
// binding carried by the artifact manifest must agree field by field with the
// verification report. Legacy runs without Candidate records pass unchanged;
// validateObservedPatch above keeps its independent, untouched semantics.
func validateCandidateBinding(report verification.Report, manifest verification.ArtifactManifest) error {
	if report.CandidateDigest == "" && report.WorkerCandidateDigest == "" {
		return nil
	}
	if report.CandidateDigest == "" || report.WorkerCandidateDigest == "" {
		return errors.New("verification report carries a partial candidate binding")
	}
	for _, artifact := range manifest.Artifacts {
		switch artifact.RelativePath {
		case "observed.patch":
			if artifact.CandidateDigest != report.CandidateDigest {
				return errors.New("observed patch artifact does not bind the head candidate")
			}
		case "worker.patch":
			if artifact.CandidateDigest != report.WorkerCandidateDigest {
				return errors.New("worker patch artifact does not bind the worker candidate")
			}
		}
	}
	return nil
}

func (b *PacketBuilder) collectWorkerResults(taskID, runID string) ([]string, []string, error) {
	pattern := filepath.Join(b.RunDirectory, "attempts", "*", "worker-result.json")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(paths)
	relativePaths := make([]string, 0, len(paths))
	digests := make([]string, 0, len(paths))
	for _, path := range paths {
		data, err := readBounded(path, packetByteLimit)
		if err != nil {
			return nil, nil, fmt.Errorf("read worker result: %w", err)
		}
		if err := b.Validator.Validate(domain.KindWorkerResult, data); err != nil {
			return nil, nil, fmt.Errorf("validate worker result: %w", err)
		}
		var identity struct{ TaskID, RunID string }
		if err := json.Unmarshal(data, &identity); err != nil || identity.TaskID != taskID || identity.RunID != runID {
			return nil, nil, errors.New("worker result identity does not match run")
		}
		digest, err := canonical.DigestJSON(data)
		if err != nil {
			return nil, nil, err
		}
		relative, err := filepath.Rel(b.RunDirectory, path)
		if err != nil {
			return nil, nil, err
		}
		relativePaths = append(relativePaths, filepath.ToSlash(relative))
		digests = append(digests, digest)
	}
	return relativePaths, digests, nil
}

func (b *PacketBuilder) loadPreviousFindings() ([]domain.PreviousFinding, error) {
	decisions, err := filepath.Glob(filepath.Join(b.RunDirectory, "decisions", "decision-*.json"))
	if err != nil || len(decisions) == 0 {
		return []domain.PreviousFinding{}, err
	}
	sort.Strings(decisions)
	data, err := readBounded(decisions[len(decisions)-1], packetByteLimit)
	if err != nil {
		return nil, err
	}
	if err := b.Validator.Validate(domain.KindReviewDecision, data); err != nil {
		return nil, fmt.Errorf("validate prior decision: %w", err)
	}
	var decision domain.ReviewDecision
	if err := json.Unmarshal(data, &decision); err != nil {
		return nil, err
	}
	packetPath := filepath.Join(b.RunDirectory, "review-packets", fmt.Sprintf("packet-%03d.json", decision.ReviewRound))
	packetData, err := readBounded(packetPath, packetByteLimit)
	if err != nil {
		return nil, fmt.Errorf("read prior review packet: %w", err)
	}
	if err := b.Validator.Validate(domain.KindReviewPacket, packetData); err != nil {
		return nil, fmt.Errorf("validate prior review packet: %w", err)
	}
	var packet domain.ReviewPacket
	if err := json.Unmarshal(packetData, &packet); err != nil {
		return nil, err
	}
	result := make([]domain.PreviousFinding, 0, len(decision.BlockingFindings))
	for _, finding := range decision.BlockingFindings {
		result = append(result, domain.PreviousFinding{Finding: finding, EvidenceDigest: decision.EvidenceDigest, SnapshotDigest: packet.SnapshotDigest, VerificationDigest: packet.VerificationDigest, CandidateDigest: packet.CandidateDigest})
	}
	return result, nil
}

func ValidateCurrentObservation(report verification.Report, observation verification.Observation) error {
	if report.Observed.SnapshotDigest != observation.SnapshotDigest || report.Observed.DiffDigest != observation.DiffDigest {
		return errors.New("worktree evidence changed after verification")
	}
	return nil
}
