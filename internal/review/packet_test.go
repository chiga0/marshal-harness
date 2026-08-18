package review

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/verification"
	marshalSchemas "github.com/chiga0/marshal-harness/schemas"
)

// packetTestDigest assembles a canonical sha256 digest literal (helper
// construction keeps fixtures gitleaks-safe).
func packetTestDigest(fill string) string { return "sha256:" + strings.Repeat(fill, 64) }

// packetTestSHA assembles a full object-id literal without embedding a
// full-length hex secret in one place.
func packetTestSHA(fill string) string { return strings.Repeat(fill, 40) }

var packetTestTime = time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)

// legacyEvidenceIdentity mirrors the pre-adoption evidence identity member
// set exactly, so tests can recompute the baseline evidenceDigest a legacy
// Run must keep reproducing (§5.1/§7.5).
type legacyEvidenceIdentity struct {
	SpecDigest             string                   `json:"specDigest"`
	PatchDigest            string                   `json:"patchDigest"`
	VerificationDigest     string                   `json:"verificationDigest"`
	ArtifactManifestDigest string                   `json:"artifactManifestDigest"`
	WorkerResultDigests    []string                 `json:"workerResultDigests"`
	PreviousFindings       []domain.PreviousFinding `json:"previousBlockingFindings"`
}

// legacyReviewPacket mirrors the pre-adoption ReviewPacket wire shape, so
// tests can prove the generated packet bytes stay byte-for-byte identical
// for legacy runs and can materialize archived documents verbatim.
type legacyReviewPacket struct {
	APIVersion               domain.APIVersion        `json:"apiVersion"`
	Kind                     domain.Kind              `json:"kind"`
	TaskID                   string                   `json:"taskId"`
	RunID                    string                   `json:"runId"`
	ReviewRound              uint                     `json:"reviewRound"`
	SpecDigest               string                   `json:"specDigest"`
	BaseSHA                  string                   `json:"baseSha"`
	SnapshotDigest           string                   `json:"snapshotDigest"`
	DiffDigest               string                   `json:"diffDigest"`
	VerificationDigest       string                   `json:"verificationDigest"`
	ArtifactManifestDigest   string                   `json:"artifactManifestDigest"`
	WorkerResultDigests      []string                 `json:"workerResultDigests"`
	EvidenceDigest           string                   `json:"evidenceDigest"`
	Inputs                   domain.PacketInputs      `json:"inputs"`
	PreviousBlockingFindings []domain.PreviousFinding `json:"previousBlockingFindings"`
	GeneratedAt              time.Time                `json:"generatedAt"`
}

// candidateReportData renders a schema-valid candidate-mode
// VerificationReport wire document. Empty candidate identities are omitted
// entirely (the schema rejects empty-string digests), which lets tests
// exercise partial-binding rejection. The summary knob varies the report
// bytes without touching the observation digests, letting tests separate
// candidate identity from report-byte identity.
func candidateReportData(specDigest, baseSHA, workerCandidate, headCandidate, summary string) []byte {
	candidateMembers := ""
	if workerCandidate != "" {
		candidateMembers += `"workerCandidateDigest":"` + workerCandidate + `",`
	}
	if headCandidate != "" {
		candidateMembers += `"candidateDigest":"` + headCandidate + `",`
	}
	return []byte(`{"apiVersion":"marshal.dev/v1alpha1","kind":"VerificationReport","taskId":"ENG-123","runId":"run-01","specDigest":"` + specDigest + `","baseSha":"` + baseSHA + `","observed":{"snapshotDigest":"sha256:` + strings.Repeat("1", 64) + `","diffDigest":"sha256:` + strings.Repeat("2", 64) + `","changedFiles":["src/code.go"],"changedFileCount":1,"diffBytes":20,"hasUntrackedFiles":true},` + candidateMembers + `"status":"pass","gates":[{"id":"scope","category":"scope","required":true,"status":"pass","summary":"` + summary + `","evidence":[]}],"startedAt":"2026-08-04T00:00:00Z","completedAt":"2026-08-04T00:01:00Z"}`)
}

// candidateManifest renders a candidate-mode ArtifactManifest whose patch
// artifacts bind the head and worker Candidate identities exactly as the
// T2 verifier emits them.
func candidateManifest(t *testing.T, patchData []byte, workerCandidate, headCandidate string) (verification.ArtifactManifest, []byte) {
	t.Helper()
	manifest := verification.ArtifactManifest{
		APIVersion: domain.APIVersionV1Alpha1,
		Kind:       domain.KindArtifactManifest,
		TaskID:     "ENG-123",
		RunID:      "run-01",
		Artifacts: []verification.Artifact{
			{ID: "code", Kind: "code", Producer: "worker", Required: true, Status: "validated", PathRoot: "repository", RelativePath: "src/code.go", ByteSize: 12, Digest: packetTestDigest("3"), CreatedAt: packetTestTime, RelatedGates: []string{"scope"}},
			{ID: "evidence:observed-patch", Kind: "patch", MediaType: "text/x-diff", Producer: "verifier", Required: true, Status: "validated", PathRoot: "run", RelativePath: "observed.patch", ByteSize: int64(len(patchData)), Digest: canonical.DigestBytes(patchData), CandidateDigest: headCandidate, CreatedAt: packetTestTime, RelatedGates: []string{"scope"}},
			{ID: "evidence:worker-patch", Kind: "patch", MediaType: "text/x-diff", Producer: "verifier", Required: true, Status: "validated", PathRoot: "run", RelativePath: "worker.patch", ByteSize: int64(len(patchData)), Digest: canonical.DigestBytes(patchData), CandidateDigest: workerCandidate, CreatedAt: packetTestTime, RelatedGates: []string{"diff:observe", "scope:changed-paths", "format:normalize"}},
		},
		GeneratedAt: packetTestTime,
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return manifest, data
}

// candidateFixtureInputs derives the candidate-mode Build inputs for an
// existing legacy fixture directory (the on-disk observed.patch and worker
// result stay untouched).
func candidateFixtureInputs(t *testing.T, fixture reviewFixture, workerCandidate, headCandidate, summary string) (verification.Report, []byte, verification.ArtifactManifest, []byte) {
	t.Helper()
	reportData := candidateReportData(fixture.specDigest, packetTestSHA("1"), workerCandidate, headCandidate, summary)
	var report verification.Report
	if err := json.Unmarshal(reportData, &report); err != nil {
		t.Fatal(err)
	}
	patchData, err := os.ReadFile(filepath.Join(fixture.directory, "observed.patch"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, manifestData := candidateManifest(t, patchData, workerCandidate, headCandidate)
	return report, reportData, manifest, manifestData
}

// TestPacketLegacyEvidenceDigestSurvivesCandidateMembers proves the §5.1/§7.5
// zero-regression assertion: for a run without candidate records the new
// evidence identity members stay out of the canonical serialization, so the
// recomputed evidenceDigest equals the pre-adoption baseline member set
// exactly, and the persisted packet bytes are byte-for-byte identical to the
// legacy wire shape.
func TestPacketLegacyEvidenceDigestSurvivesCandidateMembers(t *testing.T) {
	fixture := newReviewFixture(t)
	packet, _ := fixture.build(t, 1)
	patchData, err := os.ReadFile(filepath.Join(fixture.directory, "observed.patch"))
	if err != nil {
		t.Fatal(err)
	}
	legacyIdentity := legacyEvidenceIdentity{
		SpecDigest:             fixture.specDigest,
		PatchDigest:            canonical.DigestBytes(patchData),
		VerificationDigest:     packet.VerificationDigest,
		ArtifactManifestDigest: packet.ArtifactManifestDigest,
		WorkerResultDigests:    packet.WorkerResultDigests,
		PreviousFindings:       packet.PreviousBlockingFindings,
	}
	identityData, err := json.Marshal(legacyIdentity)
	if err != nil {
		t.Fatal(err)
	}
	baselineDigest, err := canonical.DigestJSON(identityData)
	if err != nil {
		t.Fatal(err)
	}
	if packet.EvidenceDigest != baselineDigest {
		t.Fatalf("legacy evidenceDigest diverged from the pre-adoption baseline: %s != %s", packet.EvidenceDigest, baselineDigest)
	}
	if packet.CandidateDigest != "" || packet.WorkerCandidateDigest != "" {
		t.Fatalf("legacy packet must not carry candidate bindings: %+v", packet)
	}
	storedData, err := os.ReadFile(filepath.Join(fixture.directory, "review-packet.json"))
	if err != nil {
		t.Fatal(err)
	}
	stored := strings.TrimSuffix(string(storedData), "\n")
	if strings.Contains(stored, "candidateDigest") {
		t.Fatalf("legacy packet bytes carry candidate members: %s", stored)
	}
	expected := legacyReviewPacket{
		APIVersion: packet.APIVersion, Kind: packet.Kind, TaskID: packet.TaskID, RunID: packet.RunID, ReviewRound: packet.ReviewRound,
		SpecDigest: packet.SpecDigest, BaseSHA: packet.BaseSHA, SnapshotDigest: packet.SnapshotDigest, DiffDigest: packet.DiffDigest,
		VerificationDigest: packet.VerificationDigest, ArtifactManifestDigest: packet.ArtifactManifestDigest,
		WorkerResultDigests: packet.WorkerResultDigests, EvidenceDigest: packet.EvidenceDigest, Inputs: packet.Inputs,
		PreviousBlockingFindings: packet.PreviousBlockingFindings, GeneratedAt: packet.GeneratedAt,
	}
	expectedData, err := json.MarshalIndent(expected, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if stored != string(expectedData) {
		t.Fatalf("legacy packet bytes diverge from the pre-adoption wire shape:\n%s\n---\n%s", stored, expectedData)
	}
}

// TestPacketBindsCandidateChain verifies candidate-mode construction: the
// packet carries the head and worker Candidate identities, the persisted
// document exposes them, and the evidenceDigest necessarily differs from the
// legacy member set over identical base evidence (§8 R6).
func TestPacketBindsCandidateChain(t *testing.T) {
	fixture := newReviewFixture(t)
	workerCandidate, headCandidate := packetTestDigest("a"), packetTestDigest("b")
	report, reportData, manifest, manifestData := candidateFixtureInputs(t, fixture, workerCandidate, headCandidate, "candidate round")
	builder := PacketBuilder{RunDirectory: fixture.directory, Validator: fixture.validator}
	packet, _, err := builder.Build(PacketBuildInput{Task: fixture.task, TaskData: fixture.taskData, Report: report, ReportData: reportData, Manifest: manifest, ManifestData: manifestData, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, BaseSHA: packetTestSHA("1"), ReviewRound: 1})
	if err != nil {
		t.Fatalf("candidate packet build failed: %v", err)
	}
	if packet.CandidateDigest != headCandidate || packet.WorkerCandidateDigest != workerCandidate {
		t.Fatalf("packet candidate binding = %q/%q, want %q/%q", packet.CandidateDigest, packet.WorkerCandidateDigest, headCandidate, workerCandidate)
	}
	storedData, err := os.ReadFile(filepath.Join(fixture.directory, "review-packet.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"candidateDigest": "` + headCandidate, `"workerCandidateDigest": "` + workerCandidate} {
		if !strings.Contains(string(storedData), key) {
			t.Fatalf("persisted packet misses %s", key)
		}
	}
	patchData, err := os.ReadFile(filepath.Join(fixture.directory, "observed.patch"))
	if err != nil {
		t.Fatal(err)
	}
	legacyIdentity := legacyEvidenceIdentity{
		SpecDigest: fixture.specDigest, PatchDigest: canonical.DigestBytes(patchData),
		VerificationDigest: packet.VerificationDigest, ArtifactManifestDigest: packet.ArtifactManifestDigest,
		WorkerResultDigests: packet.WorkerResultDigests, PreviousFindings: packet.PreviousBlockingFindings,
	}
	identityData, err := json.Marshal(legacyIdentity)
	if err != nil {
		t.Fatal(err)
	}
	legacyDigest, err := canonical.DigestJSON(identityData)
	if err != nil {
		t.Fatal(err)
	}
	if packet.EvidenceDigest == legacyDigest {
		t.Fatal("candidate evidenceDigest must differ from the legacy member set over identical base evidence")
	}
}

// TestPacketRejectsPartialOrDivergentCandidateBinding exercises the
// fail-closed binding discipline: a partial report binding and a manifest
// whose artifact bindings diverge from the report are both rejected, while
// validateObservedPatch keeps its own semantics untouched.
func TestPacketRejectsPartialOrDivergentCandidateBinding(t *testing.T) {
	workerCandidate, headCandidate := packetTestDigest("a"), packetTestDigest("b")
	t.Run("partial report binding", func(t *testing.T) {
		fixture := newReviewFixture(t)
		reportData := candidateReportData(fixture.specDigest, packetTestSHA("1"), "", headCandidate, "partial")
		var report verification.Report
		if err := json.Unmarshal(reportData, &report); err != nil {
			t.Fatal(err)
		}
		patchData, err := os.ReadFile(filepath.Join(fixture.directory, "observed.patch"))
		if err != nil {
			t.Fatal(err)
		}
		manifest, manifestData := candidateManifest(t, patchData, workerCandidate, headCandidate)
		builder := PacketBuilder{RunDirectory: fixture.directory, Validator: fixture.validator}
		_, _, err = builder.Build(PacketBuildInput{Task: fixture.task, TaskData: fixture.taskData, Report: report, ReportData: reportData, Manifest: manifest, ManifestData: manifestData, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, BaseSHA: packetTestSHA("1"), ReviewRound: 1})
		if err == nil || !strings.Contains(err.Error(), "partial candidate binding") {
			t.Fatalf("partial candidate binding accepted: %v", err)
		}
	})
	t.Run("divergent head binding in manifest", func(t *testing.T) {
		fixture := newReviewFixture(t)
		report, reportData, manifest, _ := candidateFixtureInputs(t, fixture, workerCandidate, headCandidate, "divergent")
		manifest.Artifacts[1].CandidateDigest = packetTestDigest("f")
		divergentData, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		builder := PacketBuilder{RunDirectory: fixture.directory, Validator: fixture.validator}
		_, _, err = builder.Build(PacketBuildInput{Task: fixture.task, TaskData: fixture.taskData, Report: report, ReportData: reportData, Manifest: manifest, ManifestData: divergentData, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, BaseSHA: packetTestSHA("1"), ReviewRound: 1})
		if err == nil || !strings.Contains(err.Error(), "does not bind the head candidate") {
			t.Fatalf("divergent head binding accepted: %v", err)
		}
	})
	t.Run("divergent worker binding in manifest", func(t *testing.T) {
		fixture := newReviewFixture(t)
		report, reportData, manifest, _ := candidateFixtureInputs(t, fixture, workerCandidate, headCandidate, "divergent-worker")
		manifest.Artifacts[2].CandidateDigest = packetTestDigest("e")
		divergentData, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		builder := PacketBuilder{RunDirectory: fixture.directory, Validator: fixture.validator}
		_, _, err = builder.Build(PacketBuildInput{Task: fixture.task, TaskData: fixture.taskData, Report: report, ReportData: reportData, Manifest: manifest, ManifestData: divergentData, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, BaseSHA: packetTestSHA("1"), ReviewRound: 1})
		if err == nil || !strings.Contains(err.Error(), "does not bind the worker candidate") {
			t.Fatalf("divergent worker binding accepted: %v", err)
		}
	})
}

// TestReviewPacketSchemaKeepsCandidateFieldsOptional asserts the schema
// change is a pure optional append: candidateDigest/workerCandidateDigest
// are declared properties but never required, at the packet top level and in
// the previousFinding definition, so archived pre-adoption packets remain
// schema-valid (§4.2, §5.1). The assertion lives here (not in the domain
// package tests) because the domain test binary cannot import contract or
// the schema fixtures without an import cycle.
func TestReviewPacketSchemaKeepsCandidateFieldsOptional(t *testing.T) {
	data, err := marshalSchemas.FS.ReadFile("review-packet.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
		Defs       map[string]struct {
			Required   []string                   `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	for _, required := range schema.Required {
		if required == "candidateDigest" || required == "workerCandidateDigest" || required == "codexEligibilityBinding" {
			t.Fatalf("packet-level candidate field %s must stay optional", required)
		}
	}
	for _, key := range []string{"candidateDigest", "workerCandidateDigest", "codexEligibilityBinding"} {
		if _, ok := schema.Properties[key]; !ok {
			t.Fatalf("packet schema misses optional property %s", key)
		}
	}
	previousFinding, ok := schema.Defs["previousFinding"]
	if !ok {
		t.Fatal("review packet schema lacks the previousFinding definition")
	}
	for _, required := range previousFinding.Required {
		if required == "candidateDigest" {
			t.Fatal("previousFinding candidateDigest must stay optional")
		}
	}
	if _, ok := previousFinding.Properties["candidateDigest"]; !ok {
		t.Fatal("previousFinding definition misses optional candidateDigest")
	}
}

func signedCodexFixture(t *testing.T, payload map[string]any) ([]byte, string) {
	t.Helper()
	payloadData, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	payloadDigest, err := canonical.DigestJSON(payloadData)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{
		"payload": payload, "payloadDigest": payloadDigest,
		"signatures": []any{map[string]any{"alg": "Ed25519", "keyId": "fixture-key", "value": "fixture-signature"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return data, payloadDigest
}

func addCodexFixtureArtifact(t *testing.T, fixture *reviewFixture, id, relativePath string, data []byte) {
	t.Helper()
	absolute := filepath.Join(fixture.directory, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, data, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.manifest.Artifacts = append(fixture.manifest.Artifacts, verification.Artifact{
		ID: id, Kind: "evidence", MediaType: "application/json", Producer: "system", Required: true,
		Status: "validated", PathRoot: "run", RelativePath: relativePath, ByteSize: int64(len(data)),
		Digest: canonical.DigestBytes(data), CreatedAt: packetTestTime, RelatedGates: []string{"codex:eligibility"},
	})
	refreshCodexFixtureManifest(t, fixture)
}

func refreshCodexFixtureManifest(t *testing.T, fixture *reviewFixture) {
	t.Helper()
	data, err := json.Marshal(fixture.manifest)
	if err != nil {
		t.Fatal(err)
	}
	fixture.manifestData = data
}

func rewriteCodexFixtureArtifact(t *testing.T, fixture *reviewFixture, id string, data []byte) {
	t.Helper()
	for index := range fixture.manifest.Artifacts {
		artifact := &fixture.manifest.Artifacts[index]
		if artifact.ID != id {
			continue
		}
		if err := os.WriteFile(filepath.Join(fixture.directory, filepath.FromSlash(artifact.RelativePath)), data, 0o600); err != nil {
			t.Fatal(err)
		}
		artifact.ByteSize = int64(len(data))
		artifact.Digest = canonical.DigestBytes(data)
		refreshCodexFixtureManifest(t, fixture)
		return
	}
	t.Fatalf("missing fixture artifact %s", id)
}

func newCodexReviewFixture(t *testing.T, adapterID string) reviewFixture {
	t.Helper()
	fixture := newReviewFixture(t)
	workerPath := filepath.Join(fixture.directory, "attempts/attempt-01/worker-result.json")
	workerData, err := os.ReadFile(workerPath)
	if err != nil {
		t.Fatal(err)
	}
	var worker map[string]any
	if err := json.Unmarshal(workerData, &worker); err != nil {
		t.Fatal(err)
	}
	worker["adapter"].(map[string]any)["id"] = adapterID
	workerData, err = json.Marshal(worker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workerPath, workerData, 0o600); err != nil {
		t.Fatal(err)
	}

	digest := packetTestDigest("a")
	configDigest, fenceDigest := packetTestDigest("b"), packetTestDigest("c")
	authorityData, authorityDigest := signedCodexFixture(t, map[string]any{
		"schemaVersion": "marshal.codex.production-evidence.v1", "hostIdentityDigest": digest,
		"binaryIdentityDigest": digest, "profileDigest": digest,
	})
	nativeBudgetsData, _ := json.Marshal([]string{"wall-time"})
	nativeBudgetsDigest, _ := canonical.DigestJSON(nativeBudgetsData)
	authority := map[string]any{
		"schemaVersion": "marshal.codex.authority-metadata.v1", "codexVersion": "1.2.3",
		"binaryIdentityDigest": digest, "hostIdentityDigest": digest, "platform": "linux",
		"launcherKind": "linux-execveat-sealed-memfd-ptrace-v1", "evidenceDigest": authorityDigest,
		"configDigest": configDigest, "keysetDigest": digest, "fenceDigest": fenceDigest, "suiteDigest": digest,
		"profileDigest": digest, "argvMatrixDigest": digest, "environmentDigest": digest,
		"eventContractDigest": digest, "permissionContractDigest": digest, "toolPolicyDigest": digest,
		"resultContractDigest": digest, "outputLimitDigest": digest, "nativeBudgetsDigest": nativeBudgetsDigest,
		"trustRootKeyId": "codex-root", "evidenceSignerKeyId": "codex-signer",
		"trustRootGeneration": 1, "authorityGeneration": 1, "revocationSetDigest": digest,
		"observedAt": "2026-08-18T00:00:00Z", "validUntil": "2026-08-19T00:00:00Z",
		"executionProfiles": []string{"read-only", "workspace-write"},
		"isolationClaim":    "cooperative-host-process-not-malicious-code-sandbox",
	}
	capabilityData, err := json.Marshal(map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "CapabilitySnapshot", "adapterId": "codex",
		"adapterVersion": "1.0.0", "executable": "/usr/bin/codex", "binaryVersion": "1.2.3",
		"probeStatus": "supported", "probeErrors": []string{}, "probedAt": "2026-08-18T00:00:00Z",
		"capabilities": map[string]any{"structuredOutput": []string{"jsonl"}, "nonInteractiveEdit": true,
			"sessionPolicies": []string{"ephemeral"}, "modelSelection": true,
			"executionProfiles": []string{"read-only", "workspace-write"}, "nativeBudgets": []string{"wall-time"}},
		"codexAuthority": authority, "conformanceEvidenceDigest": authorityDigest,
		"conformanceTrustRootKeyId": "codex-root", "conformanceProbeProfileDigest": digest,
		"conformanceValidUntil": "2026-08-19T00:00:00Z", "conformanceHostFingerprint": digest,
		"conformanceAuthorityGeneration": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	capabilityDigest, err := canonical.DigestJSON(capabilityData)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.directory, "capability-snapshot.json"), capabilityData, 0o600); err != nil {
		t.Fatal(err)
	}
	stateData, err := json.Marshal(map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "RunState", "taskId": "ENG-123", "runId": "run-01",
		"state": "REVIEW_PENDING", "sequence": 7, "specDigest": fixture.specDigest,
		"capabilityDigest": capabilityDigest, "baseSha": packetTestSHA("1"), "worktreePath": "/tmp/repo",
		"currentAttemptId": "attempt-01", "reviewRound": 1, "attemptsUsed": 1,
		"operationalRetriesUsed": 0, "reworkRoundsUsed": 0,
		"createdAt": "2026-08-18T00:00:00Z", "updatedAt": "2026-08-18T00:01:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.directory, "state.json"), stateData, 0o600); err != nil {
		t.Fatal(err)
	}
	receiptData, _ := signedCodexFixture(t, map[string]any{
		"schemaVersion": "marshal.codex.launch-receipt.v1", "taskId": "ENG-123", "runId": "run-01",
		"attemptId": "attempt-01", "evidenceDigest": authorityDigest, "configDigest": configDigest,
		"fenceDigest": fenceDigest, "phaseDigests": []string{digest, digest, digest, digest},
	})
	topologyData, err := json.Marshal(map[string]any{
		"schemaVersion": "marshal.codex.topology-snapshot.v1", "mountNamespaceDevice": uint64(1),
		"mountNamespaceInode": uint64(2), "phase": "consumer-receipt-accept",
		"fixedRoots":  []any{map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}},
		"executables": []any{map[string]any{}, map[string]any{}, map[string]any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	addCodexFixtureArtifact(t, &fixture, codexAuthorityEvidenceArtifactID, "codex-authority-evidence.json", authorityData)
	addCodexFixtureArtifact(t, &fixture, codexLaunchReceiptArtifactID, "attempts/attempt-01/codex-launch-receipt.json", receiptData)
	addCodexFixtureArtifact(t, &fixture, codexLaunchTopologyArtifactID, "attempts/attempt-01/codex-launch-accept-topology.json", topologyData)
	return fixture
}

func buildCodexFixture(fixture reviewFixture) (*domain.ReviewPacket, error) {
	builder := PacketBuilder{RunDirectory: fixture.directory, Validator: fixture.validator}
	packet, _, err := builder.Build(PacketBuildInput{Task: fixture.task, TaskData: fixture.taskData, Report: fixture.report,
		ReportData: fixture.reportData, Manifest: fixture.manifest, ManifestData: fixture.manifestData,
		TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, BaseSHA: packetTestSHA("1"), ReviewRound: 1})
	return packet, err
}

func TestPacketBindsCodexEligibilityIdentity(t *testing.T) {
	fixture := newCodexReviewFixture(t, "codex")
	packet, err := buildCodexFixture(fixture)
	if err != nil {
		t.Fatalf("derive frozen Codex eligibility binding: %v", err)
	}
	if packet.CodexEligibilityBinding == nil || packet.CodexEligibilityBinding.AttemptID != "attempt-01" {
		t.Fatalf("missing exact derived binding: %+v", packet.CodexEligibilityBinding)
	}
	bindingData, _ := json.Marshal(packet.CodexEligibilityBinding)
	bindingDigest, _ := canonical.DigestJSON(bindingData)
	patchData, _ := os.ReadFile(filepath.Join(fixture.directory, "observed.patch"))
	identityData, _ := json.Marshal(evidenceIdentity{
		SpecDigest: fixture.specDigest, PatchDigest: canonical.DigestBytes(patchData),
		VerificationDigest: packet.VerificationDigest, ArtifactManifestDigest: packet.ArtifactManifestDigest,
		WorkerResultDigests: packet.WorkerResultDigests, PreviousFindings: packet.PreviousBlockingFindings,
		EligibilityBindingDigest: bindingDigest,
	})
	wantEvidenceDigest, _ := canonical.DigestJSON(identityData)
	if packet.EvidenceDigest != wantEvidenceDigest {
		t.Fatalf("packet evidence does not bind derived eligibility: %s != %s", packet.EvidenceDigest, wantEvidenceDigest)
	}
}

func addHistoricalWorkerResult(t *testing.T, fixture *reviewFixture, attemptID, adapterID string) {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(fixture.directory, "attempts/attempt-01/worker-result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var worker map[string]any
	if err := json.Unmarshal(source, &worker); err != nil {
		t.Fatal(err)
	}
	worker["attemptId"] = attemptID
	worker["adapter"].(map[string]any)["id"] = adapterID
	data, err := json.Marshal(worker)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.directory, "attempts", attemptID, "worker-result.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func selectFrozenCandidateAttempt(t *testing.T, fixture *reviewFixture, attemptID string) {
	t.Helper()
	candidate := domain.Candidate{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindCandidate, TaskID: "ENG-123", RunID: "run-01",
		AttemptID: attemptID, AuthorityNamespaceID: "authority-01", BaseSHA: packetTestSHA("1"),
		ContentDigest: packetTestDigest("d"), ProducerKind: domain.ProducerKindWorker, Producer: "verifier",
		CreatedAt: packetTestTime,
	}
	digest, err := candidate.Digest()
	if err != nil {
		t.Fatal(err)
	}
	candidate.CandidateDigest = digest
	data, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.directory, "candidates", digest+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.report.CandidateDigest = digest
	fixture.report.WorkerCandidateDigest = digest
	fixture.reportData, err = json.Marshal(fixture.report)
	if err != nil {
		t.Fatal(err)
	}
	for index := range fixture.manifest.Artifacts {
		if fixture.manifest.Artifacts[index].RelativePath == "observed.patch" {
			fixture.manifest.Artifacts[index].CandidateDigest = digest
		}
	}
	refreshCodexFixtureManifest(t, fixture)
}

func stripCodexFixtureArtifacts(t *testing.T, fixture *reviewFixture) []verification.Artifact {
	t.Helper()
	kept := fixture.manifest.Artifacts[:0]
	var removed []verification.Artifact
	for _, artifact := range fixture.manifest.Artifacts {
		switch artifact.ID {
		case codexAuthorityEvidenceArtifactID, codexLaunchReceiptArtifactID, codexLaunchTopologyArtifactID:
			removed = append(removed, artifact)
		default:
			kept = append(kept, artifact)
		}
	}
	fixture.manifest.Artifacts = kept
	refreshCodexFixtureManifest(t, fixture)
	return removed
}

func TestHistoricalCodexWorkerDoesNotPolluteCurrentNonCodexAttempt(t *testing.T) {
	fixture := newCodexReviewFixture(t, "codex")
	addHistoricalWorkerResult(t, &fixture, "attempt-02", "qwen")
	selectFrozenCandidateAttempt(t, &fixture, "attempt-02")
	removed := stripCodexFixtureArtifacts(t, &fixture)
	packet, err := buildCodexFixture(fixture)
	if err != nil {
		t.Fatalf("current non-Codex attempt rejected because of historical Codex WorkerResult: %v", err)
	}
	if packet.CodexEligibilityBinding != nil {
		t.Fatalf("current non-Codex attempt received historical Codex binding: %+v", packet.CodexEligibilityBinding)
	}
	fixture.manifest.Artifacts = append(fixture.manifest.Artifacts, removed[0])
	refreshCodexFixtureManifest(t, &fixture)
	if _, err := buildCodexFixture(fixture); err == nil || !strings.Contains(err.Error(), "non-Codex") {
		t.Fatalf("current non-Codex manifest injection accepted: %v", err)
	}
}

func TestHistoricalNonCodexWorkerCannotRelaxCurrentCodexBinding(t *testing.T) {
	fixture := newCodexReviewFixture(t, "codex")
	addHistoricalWorkerResult(t, &fixture, "attempt-00", "qwen")
	selectFrozenCandidateAttempt(t, &fixture, "attempt-01")
	packet, err := buildCodexFixture(fixture)
	if err != nil {
		t.Fatalf("current Codex binding rejected because of historical non-Codex WorkerResult: %v", err)
	}
	if packet.CodexEligibilityBinding == nil || packet.CodexEligibilityBinding.AttemptID != "attempt-01" {
		t.Fatalf("current Codex attempt lacks exact mandatory binding: %+v", packet.CodexEligibilityBinding)
	}
	stripCodexFixtureArtifacts(t, &fixture)
	if _, err := buildCodexFixture(fixture); err == nil {
		t.Fatal("current Codex attempt accepted without mandatory binding artifacts")
	}
}

func TestPacketRejectsInvalidFrozenCodexEligibilityArtifacts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *reviewFixture)
	}{
		{"missing manifest", func(t *testing.T, f *reviewFixture) {
			f.manifest.Artifacts = f.manifest.Artifacts[:len(f.manifest.Artifacts)-1]
			refreshCodexFixtureManifest(t, f)
		}},
		{"forged path", func(t *testing.T, f *reviewFixture) {
			f.manifest.Artifacts[len(f.manifest.Artifacts)-2].RelativePath = "attempts/forged/codex-launch-receipt.json"
			refreshCodexFixtureManifest(t, f)
		}},
		{"wrong producer", func(t *testing.T, f *reviewFixture) {
			f.manifest.Artifacts[len(f.manifest.Artifacts)-3].Producer = "worker"
			refreshCodexFixtureManifest(t, f)
		}},
		{"wrong status", func(t *testing.T, f *reviewFixture) {
			f.manifest.Artifacts[len(f.manifest.Artifacts)-2].Status = "missing"
			refreshCodexFixtureManifest(t, f)
		}},
		{"wrong digest", func(t *testing.T, f *reviewFixture) {
			f.manifest.Artifacts[len(f.manifest.Artifacts)-1].Digest = packetTestDigest("f")
			refreshCodexFixtureManifest(t, f)
		}},
		{"bad envelope", func(t *testing.T, f *reviewFixture) {
			rewriteCodexFixtureArtifact(t, f, codexLaunchReceiptArtifactID, []byte(`{"payload":{},"payloadDigest":"sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff","signatures":[]}`))
		}},
		{"wrong task", func(t *testing.T, f *reviewFixture) { rewriteCodexReceipt(t, f, "taskId", "OTHER") }},
		{"wrong run", func(t *testing.T, f *reviewFixture) { rewriteCodexReceipt(t, f, "runId", "other-run") }},
		{"wrong attempt", func(t *testing.T, f *reviewFixture) { rewriteCodexReceipt(t, f, "attemptId", "attempt-02") }},
		{"wrong fence", func(t *testing.T, f *reviewFixture) { rewriteCodexReceipt(t, f, "fenceDigest", packetTestDigest("f")) }},
		{"wrong T4", func(t *testing.T, f *reviewFixture) {
			data, _ := json.Marshal(map[string]any{"schemaVersion": "marshal.codex.topology-snapshot.v1", "mountNamespaceDevice": 1, "mountNamespaceInode": 2, "phase": "pre-exec", "fixedRoots": []any{map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}}, "executables": []any{map[string]any{}, map[string]any{}, map[string]any{}}})
			rewriteCodexFixtureArtifact(t, f, codexLaunchTopologyArtifactID, data)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCodexReviewFixture(t, "codex")
			test.mutate(t, &fixture)
			if _, err := buildCodexFixture(fixture); err == nil {
				t.Fatal("invalid frozen Codex evidence accepted")
			}
		})
	}
	t.Run("nonCodex injection", func(t *testing.T) {
		fixture := newCodexReviewFixture(t, "qwen")
		if _, err := buildCodexFixture(fixture); err == nil || !strings.Contains(err.Error(), "non-Codex") {
			t.Fatalf("non-Codex attempt accepted Codex artifacts: %v", err)
		}
	})
}

func rewriteCodexReceipt(t *testing.T, fixture *reviewFixture, key string, value any) {
	t.Helper()
	path := filepath.Join(fixture.directory, "attempts/attempt-01/codex-launch-receipt.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Payload[key] = value
	data, _ = signedCodexFixture(t, envelope.Payload)
	rewriteCodexFixtureArtifact(t, fixture, codexLaunchReceiptArtifactID, data)
}

func TestCapabilitySnapshotSchemaEnforcesCodexAuthorityShape(t *testing.T) {
	fixture := newReviewFixture(t)
	digest := packetTestDigest("a")
	nativeBudgetsData, err := json.Marshal([]string{"wall-time"})
	if err != nil {
		t.Fatal(err)
	}
	nativeBudgetsDigest, err := canonical.DigestJSON(nativeBudgetsData)
	if err != nil {
		t.Fatal(err)
	}
	authority := map[string]any{
		"schemaVersion": "marshal.codex.authority-metadata.v1", "codexVersion": "1.2.3",
		"binaryIdentityDigest": digest, "hostIdentityDigest": digest, "platform": "linux",
		"launcherKind": "linux-execveat-sealed-memfd-ptrace-v1", "evidenceDigest": digest,
		"configDigest": digest, "keysetDigest": digest, "fenceDigest": digest, "suiteDigest": digest,
		"profileDigest": digest, "argvMatrixDigest": digest, "environmentDigest": digest,
		"eventContractDigest": digest, "permissionContractDigest": digest, "toolPolicyDigest": digest,
		"resultContractDigest": digest, "outputLimitDigest": digest, "nativeBudgetsDigest": nativeBudgetsDigest,
		"trustRootKeyId": "codex-root", "evidenceSignerKeyId": "codex-signer",
		"trustRootGeneration": 1, "authorityGeneration": 1, "revocationSetDigest": digest,
		"observedAt": "2026-08-18T00:00:00Z", "validUntil": "2026-08-19T00:00:00Z",
		"executionProfiles": []string{"read-only", "workspace-write"},
		"isolationClaim":    "cooperative-host-process-not-malicious-code-sandbox",
	}
	base := map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "CapabilitySnapshot", "adapterId": "codex",
		"adapterVersion": "1.0.0", "executable": "/usr/bin/codex", "binaryVersion": "1.2.3",
		"probeStatus": "supported", "probeErrors": []string{}, "probedAt": "2026-08-18T00:00:00Z",
		"capabilities": map[string]any{
			"structuredOutput": []string{"jsonl"}, "nonInteractiveEdit": true,
			"sessionPolicies": []string{"ephemeral"}, "modelSelection": true,
			"executionProfiles": []string{"read-only", "workspace-write"}, "nativeBudgets": []string{"wall-time"},
		},
		"codexAuthority": authority, "conformanceEvidenceDigest": digest,
		"conformanceTrustRootKeyId": "codex-root", "conformanceProbeProfileDigest": digest,
		"conformanceValidUntil": "2026-08-19T00:00:00Z", "conformanceHostFingerprint": digest,
		"conformanceAuthorityGeneration": 1,
	}
	validate := func(snapshot map[string]any) error {
		data, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		return fixture.validator.Validate(domain.KindCapabilitySnapshot, data)
	}
	if err := validate(base); err != nil {
		t.Fatalf("valid supported Codex authority rejected: %v", err)
	}
	authority["executionProfiles"] = []string{"workspace-write"}
	if err := validate(base); err == nil {
		t.Fatal("Codex authority/capability execution profile mismatch accepted")
	}
	authority["executionProfiles"] = []string{"read-only", "workspace-write"}
	delete(base, "codexAuthority")
	if err := validate(base); err == nil {
		t.Fatal("supported Codex snapshot without authority accepted")
	}
	base["probeStatus"] = "unsupported"
	base["probeErrors"] = []string{"Codex production authority is temporarily unavailable."}
	for _, key := range []string{"conformanceEvidenceDigest", "conformanceTrustRootKeyId", "conformanceProbeProfileDigest", "conformanceValidUntil", "conformanceHostFingerprint", "conformanceAuthorityGeneration"} {
		delete(base, key)
	}
	base["adapterFailure"] = map[string]any{
		"schemaVersion": "marshal.adapter-failure.v1", "adapterId": "codex", "operation": "probe",
		"code": "codex_fence_lock_busy", "retryClass": "permanent", "safeMessage": "Codex production authority is temporarily unavailable.",
		"observedAt": "2026-08-18T00:00:00Z", "details": map[string]any{},
	}
	if err := validate(base); err == nil {
		t.Fatal("Codex failure with invalid code/retryClass pairing accepted")
	}
	base["adapterFailure"].(map[string]any)["retryClass"] = "transient"
	if err := validate(base); err != nil {
		t.Fatalf("valid unsupported Codex failure rejected: %v", err)
	}
	base["adapterId"] = "fake"
	if err := validate(base); err == nil {
		t.Fatal("non-Codex snapshot carrying Codex adapterFailure accepted")
	}
}

// TestArchivedLegacyReviewPacketRemainsSchemaValid migrated here from the
// domain package tests, which cannot import contract without an import
// cycle: an archived pre-adoption packet (no candidate members)
// re-validates unchanged, a candidate-bound packet remains schema-valid,
// and a malformed candidateDigest is rejected, so old packets pass
// re-verification unchanged while new bindings stay fail-closed (§4.2,
// §5.1).
func TestArchivedLegacyReviewPacketRemainsSchemaValid(t *testing.T) {
	fixture := newReviewFixture(t)
	packet, _ := fixture.build(t, 1)
	legacyData, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.validator.Validate(domain.KindReviewPacket, legacyData); err != nil {
		t.Fatalf("archived legacy packet failed re-validation: %v", err)
	}
	packet.WorkerCandidateDigest = packetTestDigest("2")
	packet.CandidateDigest = packetTestDigest("3")
	candidateData, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.validator.Validate(domain.KindReviewPacket, candidateData); err != nil {
		t.Fatalf("candidate packet failed validation: %v", err)
	}
	packet.CandidateDigest = "sha256:" + strings.Repeat("z", 64)
	invalidData, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.validator.Validate(domain.KindReviewPacket, invalidData); err == nil {
		t.Fatal("malformed candidateDigest accepted by schema")
	}
}

// TestArchivedLegacyPacketRevalidatesUnderExtendedSchema materializes an
// archived pre-adoption packet verbatim (legacy wire shape, no candidate
// members) and proves it still passes schema re-verification unchanged,
// with the candidate fields decoding as empty (§5.1).
func TestArchivedLegacyPacketRevalidatesUnderExtendedSchema(t *testing.T) {
	fixture := newReviewFixture(t)
	packet, _ := fixture.build(t, 1)
	archived := legacyReviewPacket{
		APIVersion: packet.APIVersion, Kind: packet.Kind, TaskID: packet.TaskID, RunID: packet.RunID, ReviewRound: packet.ReviewRound,
		SpecDigest: packet.SpecDigest, BaseSHA: packet.BaseSHA, SnapshotDigest: packet.SnapshotDigest, DiffDigest: packet.DiffDigest,
		VerificationDigest: packet.VerificationDigest, ArtifactManifestDigest: packet.ArtifactManifestDigest,
		WorkerResultDigests: packet.WorkerResultDigests, EvidenceDigest: packet.EvidenceDigest, Inputs: packet.Inputs,
		PreviousBlockingFindings: packet.PreviousBlockingFindings, GeneratedAt: packet.GeneratedAt,
	}
	archivedData, err := json.Marshal(archived)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.validator.Validate(domain.KindReviewPacket, archivedData); err != nil {
		t.Fatalf("archived legacy packet failed schema re-verification: %v", err)
	}
	var decoded domain.ReviewPacket
	if err := json.Unmarshal(archivedData, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.CandidateDigest != "" || decoded.WorkerCandidateDigest != "" {
		t.Fatalf("archived legacy packet decoded with candidate bindings: %+v", decoded)
	}
	if decoded.EvidenceDigest != packet.EvidenceDigest {
		t.Fatalf("archived packet evidence binding diverged: %s != %s", decoded.EvidenceDigest, packet.EvidenceDigest)
	}
}

// earlyExitGitFixture prepares a minimal git repository with one base
// commit; the repository directory doubles as the verification worktree.
func earlyExitGitFixture(t *testing.T) (worktree, baseSHA string) {
	t.Helper()
	repository := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", repository}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	run("init", "-q")
	run("config", "user.name", "Marshal Test")
	run("config", "user.email", "marshal@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "base")
	output, err := exec.Command("git", "-C", repository, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v: %s", err, output)
	}
	return repository, strings.TrimSpace(string(output))
}

// TestPacketBuildsFromEarlyExitVerificationEvidence is the issue #142
// end-to-end regression: when Verification exits early with a failed or
// errored Gate (repository integrity or format:normalize), it must persist
// an observed.patch that agrees with the frozen Observation plus a validated
// observed-patch artifact with recomputable digest, byte size and candidate
// binding, so task review still builds a schema-valid ReviewPacket and the
// Lead can formally rework or reject the failed candidate instead of facing
// an unexaminable REVIEW_PENDING. The flow mirrors the review command:
// re-observe the worktree, validate the frozen identities, then build.
func TestPacketBuildsFromEarlyExitVerificationEvidence(t *testing.T) {
	cases := []struct {
		name       string
		candidate  bool
		repository bool // fail the repository Gate instead of format:normalize
	}{
		{name: "repository gate fail legacy"},
		{name: "repository gate fail candidate mode", candidate: true},
		{name: "format normalize fail legacy"},
		{name: "format normalize fail candidate mode", candidate: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newReviewFixture(t)
			worktree, baseSHA := earlyExitGitFixture(t)
			if !tc.repository {
				directory := filepath.Join(worktree, "pkg")
				if err := os.MkdirAll(directory, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, "broken.go"), []byte("package pkg\n\nfunc { oops\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			input := verification.Input{TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, BaseSHA: baseSHA, Worktree: worktree, RunDirectory: fixture.directory, Scope: verification.ScopePolicy{AllowPaths: []string{"**"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20}, PatchCaptureBytes: 1 << 20}
			if tc.repository {
				input.ExpectedCommonDir = filepath.Join(t.TempDir(), "foreign-common-dir")
			}
			if tc.candidate {
				input.AttemptID = "attempt-01"
				namespace, err := authority.AuthorityNamespaceId{TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: worktree}.Digest()
				if err != nil {
					t.Fatal(err)
				}
				input.AuthorityNamespaceID = namespace
			}
			if _, err := verification.New().Verify(context.Background(), input); err != nil {
				t.Fatalf("early exit verification must complete: %v", err)
			}
			reportData, err := os.ReadFile(filepath.Join(fixture.directory, "verification-report.json"))
			if err != nil {
				t.Fatal(err)
			}
			manifestData, err := os.ReadFile(filepath.Join(fixture.directory, "artifact-manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var report verification.Report
			if err := json.Unmarshal(reportData, &report); err != nil {
				t.Fatal(err)
			}
			var manifest verification.ArtifactManifest
			if err := json.Unmarshal(manifestData, &manifest); err != nil {
				t.Fatal(err)
			}
			if report.Status != "fail" {
				t.Fatalf("early exit report = %+v", report)
			}
			// The review command re-observes the worktree before building;
			// the frozen report identities must reproduce exactly.
			observation, err := verification.ObserveContext(context.Background(), worktree, baseSHA, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateCurrentObservation(report, observation); err != nil {
				t.Fatalf("early exit evidence must stay current for review: %v", err)
			}
			builder := PacketBuilder{RunDirectory: fixture.directory, Validator: fixture.validator}
			packet, packetDigest, err := builder.Build(PacketBuildInput{Task: fixture.task, TaskData: fixture.taskData, Report: report, ReportData: reportData, Manifest: manifest, ManifestData: manifestData, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, BaseSHA: baseSHA, ReviewRound: 1, AttemptsUsed: 1})
			if err != nil {
				t.Fatalf("review packet must build from the early exit evidence: %v", err)
			}
			if packet.CandidateDigest != "" || packet.WorkerCandidateDigest != "" {
				t.Fatalf("failed run packet must not fabricate candidate bindings: %+v", packet)
			}
			if packet.DiffDigest != report.Observed.DiffDigest || packet.SnapshotDigest != report.Observed.SnapshotDigest {
				t.Fatalf("packet observation binding diverges from the report: %+v", packet)
			}
			if tc.candidate && !tc.repository {
				// The failed candidate must stay formally reworkable by the
				// Lead over the generated packet.
				decision := validDecision(fixture, packet, packetDigest, "rework")
				path := writeDecision(t, fixture.directory, decision)
				imported, err := (&DecisionImporter{RunDirectory: fixture.directory, Validator: fixture.validator}).Import(DecisionInput{Path: path, Task: fixture.task, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, ReviewRound: 1, AttemptsUsed: 1, Report: report, Manifest: manifest})
				if err != nil || imported.TargetState != domain.StateReworkRequested {
					t.Fatalf("lead rework over the failed candidate = %+v err = %v", imported, err)
				}
			}
		})
	}
}
