package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		if required == "candidateDigest" || required == "workerCandidateDigest" {
			t.Fatalf("packet-level candidate field %s must stay optional", required)
		}
	}
	for _, key := range []string{"candidateDigest", "workerCandidateDigest"} {
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
