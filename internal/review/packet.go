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
}

const (
	codexAuthorityEvidenceArtifactID = "evidence:codex-authority-evidence"
	codexLaunchReceiptArtifactID     = "evidence:codex-launch-receipt"
	codexLaunchTopologyArtifactID    = "evidence:codex-launch-accept-topology"
)

type workerEvidenceIdentity struct {
	TaskID    string `json:"taskId"`
	RunID     string `json:"runId"`
	AttemptID string `json:"attemptId"`
	Adapter   struct {
		ID string `json:"id"`
	} `json:"adapter"`
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
	workerPaths, workerDigests, workerIdentities, err := b.collectWorkerResults(input.TaskID, input.RunID)
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
	eligibilityBinding, eligibilityBindingDigest, err := b.deriveCodexEligibilityBinding(input.Report, input.Manifest, workerIdentities, input.TaskID, input.RunID)
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

func (b *PacketBuilder) deriveCodexEligibilityBinding(report verification.Report, manifest verification.ArtifactManifest, workers []workerEvidenceIdentity, taskID, runID string) (*domain.CodexEligibilityBindingV1, string, error) {
	worker, found, err := b.currentWorkerIdentity(report, workers, taskID, runID)
	if err != nil {
		return nil, "", err
	}
	if !found {
		if manifestHasCodexEligibilityArtifacts(manifest) {
			return nil, "", errors.New("unresolved current attempt cannot carry Codex eligibility artifacts")
		}
		return nil, "", nil
	}
	if worker.Adapter.ID != "codex" {
		if manifestHasCodexEligibilityArtifacts(manifest) {
			return nil, "", errors.New("non-Codex attempt cannot carry Codex eligibility artifacts")
		}
		return nil, "", nil
	}

	stateData, err := readBounded(filepath.Join(b.RunDirectory, "state.json"), packetByteLimit)
	if err != nil {
		return nil, "", fmt.Errorf("read frozen run state for Codex eligibility: %w", err)
	}
	if err := b.Validator.Validate(domain.KindRunState, stateData); err != nil {
		return nil, "", fmt.Errorf("validate frozen run state for Codex eligibility: %w", err)
	}
	var state domain.RunState
	if err := json.Unmarshal(stateData, &state); err != nil {
		return nil, "", fmt.Errorf("decode frozen run state for Codex eligibility: %w", err)
	}
	if state.TaskID != taskID || state.RunID != runID || state.CurrentAttemptID != worker.AttemptID {
		return nil, "", errors.New("codex WorkerResult attempt does not match frozen run state")
	}

	capabilityData, err := readBounded(filepath.Join(b.RunDirectory, "capability-snapshot.json"), packetByteLimit)
	if err != nil {
		return nil, "", fmt.Errorf("read frozen Codex capability snapshot: %w", err)
	}
	if err := b.Validator.Validate(domain.KindCapabilitySnapshot, capabilityData); err != nil {
		return nil, "", fmt.Errorf("validate frozen Codex capability snapshot: %w", err)
	}
	capabilityDigest, err := canonical.DigestJSON(capabilityData)
	if err != nil {
		return nil, "", fmt.Errorf("digest frozen Codex capability snapshot: %w", err)
	}
	if capabilityDigest != state.CapabilityDigest {
		return nil, "", errors.New("codex capability snapshot digest does not match frozen run state")
	}
	var capability struct {
		AdapterID      string `json:"adapterId"`
		ProbeStatus    string `json:"probeStatus"`
		AuthorityMode  string `json:"authorityMode"`
		CodexAuthority *struct {
			EvidenceDigest       string `json:"evidenceDigest"`
			ConfigDigest         string `json:"configDigest"`
			FenceDigest          string `json:"fenceDigest"`
			HostIdentityDigest   string `json:"hostIdentityDigest"`
			BinaryIdentityDigest string `json:"binaryIdentityDigest"`
			ProfileDigest        string `json:"profileDigest"`
		} `json:"codexAuthority"`
	}
	if err := json.Unmarshal(capabilityData, &capability); err != nil {
		return nil, "", fmt.Errorf("decode frozen Codex capability snapshot: %w", err)
	}
	if capability.AdapterID != "codex" || capability.ProbeStatus != "supported" {
		return nil, "", errors.New("codex WorkerResult is not backed by a supported Codex capability authority")
	}
	// ADR 0042 explicitly permits Mac ordinary-user mode at the same
	// trust level as Qwen/OpenCode. It has no signed authority, launch
	// receipt, or eligibility artifacts, so it must not enter the Codex
	// production eligibility binding path. The capability snapshot remains
	// visibly downgraded via authorityMode=ordinary-user.
	if capability.CodexAuthority == nil {
		if capability.AuthorityMode == "ordinary-user" {
			return nil, "", nil
		}
		return nil, "", errors.New("codex WorkerResult is not backed by a supported Codex capability authority")
	}

	authorityData, err := b.readCodexManifestArtifact(manifest, codexAuthorityEvidenceArtifactID, "codex-authority-evidence.json")
	if err != nil {
		return nil, "", err
	}
	authorityPayload, authorityEvidenceDigest, err := parseSignedPayload(authorityData, "marshal.codex.production-evidence.v1")
	if err != nil {
		return nil, "", fmt.Errorf("validate frozen Codex authority evidence: %w", err)
	}
	if authorityEvidenceDigest != capability.CodexAuthority.EvidenceDigest {
		return nil, "", errors.New("codex authority evidence digest does not match capability authority")
	}
	var authorityIdentity struct {
		HostIdentityDigest   string `json:"hostIdentityDigest"`
		BinaryIdentityDigest string `json:"binaryIdentityDigest"`
		ProfileDigest        string `json:"profileDigest"`
	}
	if err := json.Unmarshal(authorityPayload, &authorityIdentity); err != nil {
		return nil, "", fmt.Errorf("decode frozen Codex authority evidence payload: %w", err)
	}
	if authorityIdentity.HostIdentityDigest != capability.CodexAuthority.HostIdentityDigest ||
		authorityIdentity.BinaryIdentityDigest != capability.CodexAuthority.BinaryIdentityDigest ||
		authorityIdentity.ProfileDigest != capability.CodexAuthority.ProfileDigest {
		return nil, "", errors.New("codex authority evidence identity digests do not match capability authority")
	}

	attemptPrefix := filepath.ToSlash(filepath.Join("attempts", worker.AttemptID))
	receiptPath := attemptPrefix + "/codex-launch-receipt.json"
	receiptData, err := b.readCodexManifestArtifact(manifest, codexLaunchReceiptArtifactID, receiptPath)
	if err != nil {
		return nil, "", err
	}
	receiptPayload, launchReceiptDigest, err := parseSignedPayload(receiptData, "marshal.codex.launch-receipt.v1")
	if err != nil {
		return nil, "", fmt.Errorf("validate frozen Codex launch receipt: %w", err)
	}
	var receipt struct {
		TaskID         string   `json:"taskId"`
		RunID          string   `json:"runId"`
		AttemptID      string   `json:"attemptId"`
		EvidenceDigest string   `json:"evidenceDigest"`
		ConfigDigest   string   `json:"configDigest"`
		FenceDigest    string   `json:"fenceDigest"`
		PhaseDigests   []string `json:"phaseDigests"`
	}
	if err := json.Unmarshal(receiptPayload, &receipt); err != nil {
		return nil, "", fmt.Errorf("decode frozen Codex launch receipt payload: %w", err)
	}
	if receipt.TaskID != taskID || receipt.RunID != runID || receipt.AttemptID != worker.AttemptID {
		return nil, "", errors.New("codex launch receipt identity does not match frozen WorkerResult")
	}
	if receipt.EvidenceDigest != authorityEvidenceDigest || receipt.ConfigDigest != capability.CodexAuthority.ConfigDigest || receipt.FenceDigest != capability.CodexAuthority.FenceDigest {
		return nil, "", errors.New("codex launch receipt authority digests do not match frozen evidence and capability")
	}
	if len(receipt.PhaseDigests) != 4 {
		return nil, "", errors.New("codex launch receipt must bind exactly T0 through T3")
	}

	topologyPath := attemptPrefix + "/codex-launch-accept-topology.json"
	topologyData, err := b.readCodexManifestArtifact(manifest, codexLaunchTopologyArtifactID, topologyPath)
	if err != nil {
		return nil, "", err
	}
	launchAcceptTopologyDigest, err := validateLaunchAcceptTopology(topologyData)
	if err != nil {
		return nil, "", fmt.Errorf("validate frozen Codex launch-accept topology: %w", err)
	}

	binding := &domain.CodexEligibilityBindingV1{
		SchemaVersion: "marshal.codex.eligibility-binding.v1", TaskID: taskID, RunID: runID, AttemptID: worker.AttemptID,
		CapabilitySnapshotDigest: capabilityDigest, AuthorityEvidenceDigest: authorityEvidenceDigest,
		ConfigDigest: capability.CodexAuthority.ConfigDigest, FenceDigest: capability.CodexAuthority.FenceDigest,
		LaunchReceiptDigest: launchReceiptDigest, LaunchAcceptTopologyDigest: launchAcceptTopologyDigest,
	}
	bindingData, err := json.Marshal(binding)
	if err != nil {
		return nil, "", fmt.Errorf("marshal derived Codex eligibility binding: %w", err)
	}
	bindingDigest, err := canonical.DigestJSON(bindingData)
	if err != nil {
		return nil, "", fmt.Errorf("digest derived Codex eligibility binding: %w", err)
	}
	return binding, bindingDigest, nil
}

func (b *PacketBuilder) currentWorkerIdentity(report verification.Report, workers []workerEvidenceIdentity, taskID, runID string) (workerEvidenceIdentity, bool, error) {
	if report.CandidateDigest == "" {
		if len(workers) == 1 {
			return workers[0], true, nil
		}
		for _, worker := range workers {
			if worker.Adapter.ID == "codex" {
				return workerEvidenceIdentity{}, false, errors.New("codex review evidence has no unambiguous current Candidate attempt")
			}
		}
		return workerEvidenceIdentity{}, false, nil
	}
	candidateData, err := readBounded(filepath.Join(b.RunDirectory, "candidates", report.CandidateDigest+".json"), packetByteLimit)
	if err != nil {
		return workerEvidenceIdentity{}, false, fmt.Errorf("read head Candidate for review attempt: %w", err)
	}
	var candidate domain.Candidate
	if err := json.Unmarshal(candidateData, &candidate); err != nil {
		return workerEvidenceIdentity{}, false, fmt.Errorf("decode head Candidate for review attempt: %w", err)
	}
	if err := candidate.Validate(); err != nil || candidate.CandidateDigest != report.CandidateDigest || candidate.TaskID != taskID || candidate.RunID != runID {
		return workerEvidenceIdentity{}, false, errors.New("head Candidate does not bind the frozen review run")
	}
	for _, worker := range workers {
		if worker.AttemptID == candidate.AttemptID {
			return worker, true, nil
		}
	}
	return workerEvidenceIdentity{}, false, errors.New("head Candidate attempt has no frozen WorkerResult")
}

func manifestHasCodexEligibilityArtifacts(manifest verification.ArtifactManifest) bool {
	for _, artifact := range manifest.Artifacts {
		switch artifact.ID {
		case codexAuthorityEvidenceArtifactID, codexLaunchReceiptArtifactID, codexLaunchTopologyArtifactID:
			return true
		}
	}
	return false
}

func (b *PacketBuilder) readCodexManifestArtifact(manifest verification.ArtifactManifest, id, relativePath string) ([]byte, error) {
	var match *verification.Artifact
	for index := range manifest.Artifacts {
		artifact := &manifest.Artifacts[index]
		if artifact.ID != id {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("codex eligibility artifact %s is duplicated", id)
		}
		match = artifact
	}
	if match == nil || match.Producer != "system" || !match.Required || match.Status != "validated" || match.PathRoot != "run" || match.RelativePath != relativePath {
		return nil, fmt.Errorf("codex eligibility artifact %s is missing or not a validated system artifact", id)
	}
	data, err := readBounded(filepath.Join(b.RunDirectory, filepath.FromSlash(relativePath)), packetByteLimit)
	if err != nil {
		return nil, fmt.Errorf("read Codex eligibility artifact %s: %w", id, err)
	}
	if match.ByteSize != int64(len(data)) || match.Digest != canonical.DigestBytes(data) {
		return nil, fmt.Errorf("codex eligibility artifact %s does not match its frozen manifest identity", id)
	}
	return data, nil
}

func parseSignedPayload(data []byte, schemaVersion string) (json.RawMessage, string, error) {
	if _, err := canonical.JSON(data); err != nil {
		return nil, "", err
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, "", err
	}
	if len(envelope) != 3 || envelope["payload"] == nil || envelope["payloadDigest"] == nil || envelope["signatures"] == nil {
		return nil, "", errors.New("signed envelope has an invalid closed shape")
	}
	var payloadDigest string
	var signatures []json.RawMessage
	if err := json.Unmarshal(envelope["payloadDigest"], &payloadDigest); err != nil {
		return nil, "", err
	}
	if err := json.Unmarshal(envelope["signatures"], &signatures); err != nil || len(signatures) != 1 {
		return nil, "", errors.New("signed envelope must contain exactly one signature")
	}
	var signature map[string]json.RawMessage
	if err := json.Unmarshal(signatures[0], &signature); err != nil || len(signature) != 3 || signature["alg"] == nil || signature["keyId"] == nil || signature["value"] == nil {
		return nil, "", errors.New("signed envelope signature has an invalid closed shape")
	}
	var alg, keyID, value string
	if json.Unmarshal(signature["alg"], &alg) != nil || json.Unmarshal(signature["keyId"], &keyID) != nil || json.Unmarshal(signature["value"], &value) != nil || alg != "Ed25519" || keyID == "" || value == "" {
		return nil, "", errors.New("signed envelope signature identity is invalid")
	}
	computed, err := canonical.DigestJSON(envelope["payload"])
	if err != nil || computed != payloadDigest {
		return nil, "", errors.New("signed envelope payloadDigest does not match its canonical payload")
	}
	var tag struct {
		SchemaVersion string `json:"schemaVersion"`
	}
	if err := json.Unmarshal(envelope["payload"], &tag); err != nil || tag.SchemaVersion != schemaVersion {
		return nil, "", errors.New("signed envelope payload schemaVersion mismatch")
	}
	return append(json.RawMessage(nil), envelope["payload"]...), payloadDigest, nil
}

func validateLaunchAcceptTopology(data []byte) (string, error) {
	digest, err := canonical.DigestJSON(data)
	if err != nil {
		return "", err
	}
	var topology map[string]json.RawMessage
	if err := json.Unmarshal(data, &topology); err != nil {
		return "", err
	}
	required := []string{"schemaVersion", "mountNamespaceDevice", "mountNamespaceInode", "phase", "fixedRoots", "executables"}
	if len(topology) != len(required) {
		return "", errors.New("launch-accept topology has an invalid closed shape")
	}
	for _, member := range required {
		if topology[member] == nil {
			return "", errors.New("launch-accept topology has an invalid closed shape")
		}
	}
	var tag struct {
		SchemaVersion string            `json:"schemaVersion"`
		Phase         string            `json:"phase"`
		FixedRoots    []json.RawMessage `json:"fixedRoots"`
		Executables   []json.RawMessage `json:"executables"`
	}
	if err := json.Unmarshal(data, &tag); err != nil || tag.SchemaVersion != "marshal.codex.topology-snapshot.v1" || tag.Phase != "consumer-receipt-accept" || len(tag.FixedRoots) != 6 || len(tag.Executables) != 3 {
		return "", errors.New("launch-accept topology must be a T4 consumer-receipt-accept record")
	}
	return digest, nil
}

func (b *PacketBuilder) collectWorkerResults(taskID, runID string) ([]string, []string, []workerEvidenceIdentity, error) {
	pattern := filepath.Join(b.RunDirectory, "attempts", "*", "worker-result.json")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, nil, nil, err
	}
	sort.Strings(paths)
	relativePaths := make([]string, 0, len(paths))
	digests := make([]string, 0, len(paths))
	identities := make([]workerEvidenceIdentity, 0, len(paths))
	for _, path := range paths {
		data, err := readBounded(path, packetByteLimit)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("read worker result: %w", err)
		}
		if err := b.Validator.Validate(domain.KindWorkerResult, data); err != nil {
			return nil, nil, nil, fmt.Errorf("validate worker result: %w", err)
		}
		var identity workerEvidenceIdentity
		if err := json.Unmarshal(data, &identity); err != nil || identity.TaskID != taskID || identity.RunID != runID {
			return nil, nil, nil, errors.New("worker result identity does not match run")
		}
		digest, err := canonical.DigestJSON(data)
		if err != nil {
			return nil, nil, nil, err
		}
		relative, err := filepath.Rel(b.RunDirectory, path)
		if err != nil {
			return nil, nil, nil, err
		}
		relativePaths = append(relativePaths, filepath.ToSlash(relative))
		digests = append(digests, digest)
		identities = append(identities, identity)
	}
	return relativePaths, digests, identities, nil
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
