package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/verification"
	marshalSchemas "github.com/chiga0/marshal-harness/schemas"
)

type reviewFixture struct {
	directory    string
	task         domain.TaskSpec
	taskData     []byte
	report       verification.Report
	reportData   []byte
	manifest     verification.ArtifactManifest
	manifestData []byte
	specDigest   string
	validator    *contract.Validator
}

func newReviewFixture(t *testing.T) reviewFixture {
	t.Helper()
	directory := t.TempDir()
	taskData := []byte(`{"apiVersion":"marshal.dev/v1alpha1","kind":"Task","metadata":{"id":"ENG-123","title":"review"},"repository":{"path":"/tmp/repo","baseRef":"main","remote":"origin"},"work":{"objective":"修复并发缺陷","constraints":["保持 API 兼容"],"nonGoals":["不重写模块"]},"scope":{"allowPaths":["src/**"],"denyPaths":[],"allowSubmodules":false,"maxChangedFiles":10,"maxDiffBytes":100000},"acceptance":{"commands":[],"allowNoChange":false},"deliverables":[{"id":"code","kind":"code","required":true,"pathGlob":"src/**","minimumCount":1}],"worker":{"preferredAdapter":"fake","fallbackAdapters":[],"executionProfile":"workspace-write","sessionPolicy":"ephemeral"},"budgets":{"runTimeoutSeconds":600,"attemptTimeoutSeconds":300,"maxAttempts":3,"maxOperationalRetries":0,"maxReworkRounds":2,"maxOutputBytes":1000000},"publication":{"required":false,"provider":"none","mode":"none","remote":"origin","baseBranch":"main","mergePolicy":"never","requiredChecks":[]}}`)
	specDigest, err := canonical.DigestJSON(taskData)
	if err != nil {
		t.Fatal(err)
	}
	base := strings.Repeat("1", 40)
	reportData := []byte(`{"apiVersion":"marshal.dev/v1alpha1","kind":"VerificationReport","taskId":"ENG-123","runId":"run-01","specDigest":"` + specDigest + `","baseSha":"` + base + `","observed":{"snapshotDigest":"sha256:1111111111111111111111111111111111111111111111111111111111111111","diffDigest":"sha256:2222222222222222222222222222222222222222222222222222222222222222","changedFiles":["src/code.go"],"changedFileCount":1,"diffBytes":20,"hasUntrackedFiles":true},"status":"pass","gates":[{"id":"scope","category":"scope","required":true,"status":"pass","summary":"ok","evidence":[]}],"startedAt":"2026-08-04T00:00:00Z","completedAt":"2026-08-04T00:01:00Z"}`)
	patchData := []byte("diff --git a/src/code.go\n")
	manifestData := []byte(`{"apiVersion":"marshal.dev/v1alpha1","kind":"ArtifactManifest","taskId":"ENG-123","runId":"run-01","artifacts":[{"id":"code","kind":"code","producer":"worker","required":true,"status":"validated","pathRoot":"repository","relativePath":"src/code.go","byteSize":12,"digest":"sha256:3333333333333333333333333333333333333333333333333333333333333333","createdAt":"2026-08-04T00:00:00Z","redacted":false,"truncated":false,"relatedGates":["scope"]},{"id":"evidence:observed-patch","kind":"patch","producer":"verifier","required":true,"status":"validated","pathRoot":"run","relativePath":"observed.patch","byteSize":` + fmt.Sprint(len(patchData)) + `,"digest":"` + canonical.DigestBytes(patchData) + `","createdAt":"2026-08-04T00:00:00Z","redacted":false,"truncated":false,"relatedGates":["scope"]}],"generatedAt":"2026-08-04T00:01:00Z"}`)
	workerData, err := marshalSchemas.FS.ReadFile("examples/happy-path/worker-result.json")
	if err != nil {
		t.Fatal(err)
	}
	var worker map[string]any
	if err := json.Unmarshal(workerData, &worker); err != nil {
		t.Fatal(err)
	}
	worker["taskId"], worker["runId"], worker["attemptId"] = "ENG-123", "run-01", "attempt-01"
	workerData, _ = json.Marshal(worker)
	for path, data := range map[string][]byte{"task-spec.json": taskData, "observed.patch": patchData, "verification-report.json": reportData, "artifact-manifest.json": manifestData, "attempts/attempt-01/worker-result.json": workerData} {
		absolute := filepath.Join(directory, path)
		if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	var task domain.TaskSpec
	var report verification.Report
	var manifest verification.ArtifactManifest
	if err := json.Unmarshal(taskData, &task); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(reportData, &report); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	return reviewFixture{directory, task, taskData, report, reportData, manifest, manifestData, specDigest, validator}
}

func (f reviewFixture) build(t *testing.T, round uint) (*domain.ReviewPacket, string) {
	t.Helper()
	builder := PacketBuilder{RunDirectory: f.directory, Validator: f.validator}
	packet, digest, err := builder.Build(PacketBuildInput{Task: f.task, TaskData: f.taskData, Report: f.report, ReportData: f.reportData, Manifest: f.manifest, ManifestData: f.manifestData, TaskID: "ENG-123", RunID: "run-01", SpecDigest: f.specDigest, BaseSHA: strings.Repeat("1", 40), ReviewRound: round, AttemptsUsed: round})
	if err != nil {
		t.Fatal(err)
	}
	return packet, digest
}

func TestPacketIsSchemaValidDeterministicAndWorkerPromptIsBounded(t *testing.T) {
	fixture := newReviewFixture(t)
	packet, first := fixture.build(t, 1)
	_, second := fixture.build(t, 1)
	if first != second {
		t.Fatalf("packet digest changed: %s != %s", first, second)
	}
	if packet.DiffDigest != fixture.report.Observed.DiffDigest {
		t.Fatalf("diff digest = %s", packet.DiffDigest)
	}
	prompt, err := os.ReadFile(filepath.Join(fixture.directory, "worker-prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(prompt) > PromptSizeCap || !strings.Contains(string(prompt), fixture.task.Work.Objective) || strings.Contains(string(prompt), "ReviewDecision") {
		t.Fatalf("invalid worker prompt: %s", prompt)
	}
}

func TestPacketRejectsMissingOrInvalidWorkerResult(t *testing.T) {
	fixture := newReviewFixture(t)
	if err := os.Remove(filepath.Join(fixture.directory, "attempts/attempt-01/worker-result.json")); err != nil {
		t.Fatal(err)
	}
	builder := PacketBuilder{RunDirectory: fixture.directory, Validator: fixture.validator}
	_, _, err := builder.Build(PacketBuildInput{Task: fixture.task, TaskData: fixture.taskData, Report: fixture.report, ReportData: fixture.reportData, Manifest: fixture.manifest, ManifestData: fixture.manifestData, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, BaseSHA: strings.Repeat("1", 40), ReviewRound: 1})
	if err == nil {
		t.Fatal("missing worker result accepted")
	}
}

func TestPacketRejectsTamperedOrOversizedPatch(t *testing.T) {
	fixture := newReviewFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.directory, "observed.patch"), []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	builder := PacketBuilder{RunDirectory: fixture.directory, Validator: fixture.validator}
	input := PacketBuildInput{Task: fixture.task, TaskData: fixture.taskData, Report: fixture.report, ReportData: fixture.reportData, Manifest: fixture.manifest, ManifestData: fixture.manifestData, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, BaseSHA: strings.Repeat("1", 40), ReviewRound: 1}
	if _, _, err := builder.Build(input); err == nil || !strings.Contains(err.Error(), "frozen verifier artifact") {
		t.Fatalf("tampered patch accepted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.directory, "observed.patch"), make([]byte, packetByteLimit+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := builder.Build(input); err == nil || !strings.Contains(err.Error(), "safe byte limit") {
		t.Fatalf("oversized patch accepted: %v", err)
	}
}

func TestDecisionSchemaStaleAndPublicationGuards(t *testing.T) {
	fixture := newReviewFixture(t)
	packet, packetDigest := fixture.build(t, 1)
	decision := validDecision(fixture, packet, packetDigest, "accept")
	path := writeDecision(t, fixture.directory, decision)
	importer := DecisionImporter{RunDirectory: fixture.directory, Validator: fixture.validator}
	result, err := importer.Import(DecisionInput{Path: path, Task: fixture.task, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, ReviewRound: 1, AttemptsUsed: 1, Report: fixture.report, Manifest: fixture.manifest})
	if err != nil || result.TargetState != domain.StateAccepted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	decision.Summary = ""
	path = writeDecision(t, fixture.directory, decision)
	if _, err := importer.Import(DecisionInput{Path: path, Task: fixture.task, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, ReviewRound: 1, Report: fixture.report, Manifest: fixture.manifest}); err == nil {
		t.Fatal("schema-invalid decision accepted")
	}
	decision = validDecision(fixture, packet, packetDigest, "accept")
	decision.EvidenceDigest = "sha256:" + strings.Repeat("0", 64)
	path = writeDecision(t, fixture.directory, decision)
	if _, err := importer.Import(DecisionInput{Path: path, Task: fixture.task, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, ReviewRound: 1, Report: fixture.report, Manifest: fixture.manifest}); err == nil {
		t.Fatal("stale decision accepted")
	}
	fixture.task.Publication.Required = true
	decision = validDecision(fixture, packet, packetDigest, "accept")
	decision.PublicationRecommendation = "do-not-publish"
	path = writeDecision(t, fixture.directory, decision)
	if _, err := importer.Import(DecisionInput{Path: path, Task: fixture.task, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, ReviewRound: 1, Report: fixture.report, Manifest: fixture.manifest}); err == nil {
		t.Fatal("publication policy bypass accepted")
	}
}

func TestReworkBudgetExhaustionTransitionsToRejected(t *testing.T) {
	fixture := newReviewFixture(t)
	packet, packetDigest := fixture.build(t, 1)
	decision := validDecision(fixture, packet, packetDigest, "rework")
	decision.BlockingFindings = []domain.Finding{{ID: "F-1", Severity: "P1", Title: "bug", Description: "must fix", RequiredOutcome: "fixed"}}
	path := writeDecision(t, fixture.directory, decision)
	result, err := (&DecisionImporter{RunDirectory: fixture.directory, Validator: fixture.validator}).Import(DecisionInput{Path: path, Task: fixture.task, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, ReviewRound: 1, AttemptsUsed: uint(fixture.task.Budgets.MaxAttempts), ReworkRoundsUsed: uint(fixture.task.Budgets.MaxReworkRounds), Report: fixture.report, Manifest: fixture.manifest})
	if err != nil || result.TargetState != domain.StateRejected {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestNoChangeRequiresValidatedDiagnostic(t *testing.T) {
	fixture := newReviewFixture(t)
	fixture.task.Acceptance.AllowNoChange = true
	fixture.report.Observed.ChangedFiles = []string{}
	fixture.report.Observed.ChangedFileCount = 0
	packet, packetDigest := fixture.build(t, 1)
	decision := validDecision(fixture, packet, packetDigest, "no_change")
	path := writeDecision(t, fixture.directory, decision)
	input := DecisionInput{Path: path, Task: fixture.task, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, ReviewRound: 1, Report: fixture.report, Manifest: fixture.manifest}
	if _, err := (&DecisionImporter{RunDirectory: fixture.directory, Validator: fixture.validator}).Import(input); err == nil {
		t.Fatal("no_change without diagnostic accepted")
	}
	input.Manifest.Artifacts = append(input.Manifest.Artifacts, verification.Artifact{ID: "diagnosis", Kind: "diagnostic", Status: "validated"})
	if _, err := (&DecisionImporter{RunDirectory: fixture.directory, Validator: fixture.validator}).Import(input); err != nil {
		t.Fatalf("diagnostic no_change rejected: %v", err)
	}
}

func TestCurrentObservationRejectsStaleWorktree(t *testing.T) {
	report := verification.Report{Observed: verification.Observation{SnapshotDigest: "sha256:" + strings.Repeat("1", 64), DiffDigest: "sha256:" + strings.Repeat("2", 64)}}
	observation := verification.Observation{SnapshotDigest: report.Observed.SnapshotDigest, DiffDigest: "sha256:" + strings.Repeat("3", 64)}
	if err := ValidateCurrentObservation(report, observation); err == nil {
		t.Fatal("stale worktree accepted")
	}
}

func TestPreviousBlockingFindingRequiresNewEvidenceBeforeClosure(t *testing.T) {
	fixture := newReviewFixture(t)
	packet, packetDigest := fixture.build(t, 1)
	decision := validDecision(fixture, packet, packetDigest, "rework")
	decision.BlockingFindings = []domain.Finding{{ID: "F-1", Severity: "P1", Title: "并发缺陷", Description: "仍会竞态", RequiredOutcome: "新增回归证据"}}
	path := writeDecision(t, fixture.directory, decision)
	result, err := (&DecisionImporter{RunDirectory: fixture.directory, Validator: fixture.validator}).Import(DecisionInput{Path: path, Task: fixture.task, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, ReviewRound: 1, AttemptsUsed: 1, Report: fixture.report, Manifest: fixture.manifest})
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

	packet, packetDigest = fixture.build(t, 2)
	if len(packet.PreviousBlockingFindings) != 1 || packet.PreviousBlockingFindings[0].Description != "仍会竞态" {
		t.Fatalf("previous findings = %+v", packet.PreviousBlockingFindings)
	}
	decision = validDecision(fixture, packet, packetDigest, "accept")
	path = writeDecision(t, fixture.directory, decision)
	if _, err := (&DecisionImporter{RunDirectory: fixture.directory, Validator: fixture.validator}).Import(DecisionInput{Path: path, Task: fixture.task, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, ReviewRound: 2, AttemptsUsed: 2, ReworkRoundsUsed: 1, Report: fixture.report, Manifest: fixture.manifest}); err == nil || !strings.Contains(err.Error(), "cannot close without new evidence") {
		t.Fatalf("finding closed without new evidence: %v", err)
	}
}

func TestPrepareRecordsReplacesOrphanPendingFiles(t *testing.T) {
	fixture := newReviewFixture(t)
	packet, packetDigest := fixture.build(t, 1)
	decision := validDecision(fixture, packet, packetDigest, "reject")
	path := writeDecision(t, fixture.directory, decision)
	result, err := (&DecisionImporter{RunDirectory: fixture.directory, Validator: fixture.validator}).Import(DecisionInput{Path: path, Task: fixture.task, TaskID: "ENG-123", RunID: "run-01", SpecDigest: fixture.specDigest, ReviewRound: 1, AttemptsUsed: 1, Report: fixture.report, Manifest: fixture.manifest})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"decisions/decision-001.json.pending", "review-packets/packet-001.json.pending"} {
		absolute := filepath.Join(fixture.directory, path)
		if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, []byte("crash residue"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	records, err := PrepareRecords(fixture.directory, result, nil)
	if err != nil {
		t.Fatalf("orphan pending files blocked retry: %v", err)
	}
	defer records.Abort()
	data, err := os.ReadFile(filepath.Join(fixture.directory, "decisions", "decision-001.json.pending"))
	if err != nil || !strings.Contains(string(data), `"kind": "ReviewDecision"`) {
		t.Fatalf("pending decision was not rebuilt: %v %s", err, data)
	}
}

func TestMarshalSkillHasExplicitAndImplicitTriggers(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".agents", "skills", "marshal", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, expected := range []string{"明确要求“使用 Marshal”", "主 Agent（pi、Codex 等编码 Agent）", "marshal task review", "不要绕过 Core"} {
		if !strings.Contains(content, expected) {
			t.Fatalf("skill misses trigger or boundary %q", expected)
		}
	}
}

func validDecision(f reviewFixture, packet *domain.ReviewPacket, packetDigest, verdict string) domain.ReviewDecision {
	publication := "not-applicable"
	if f.task.Publication.Required {
		publication = "publish"
	}
	return domain.ReviewDecision{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindReviewDecision, TaskID: "ENG-123", RunID: "run-01", ReviewRound: packet.ReviewRound, Reviewer: domain.Reviewer{Type: "lead-agent", ID: "codex"}, SpecDigest: f.specDigest, ReviewPacketDigest: packetDigest, VerificationDigest: packet.VerificationDigest, ArtifactManifestDigest: packet.ArtifactManifestDigest, EvidenceDigest: packet.EvidenceDigest, Verdict: verdict, Summary: "reviewed", BlockingFindings: []domain.Finding{}, NonBlockingFindings: []domain.Finding{}, PublicationRecommendation: publication, MergeRecommendation: "do-not-merge", DecidedAt: time.Date(2026, 8, 4, 0, 2, 0, 0, time.UTC)}
}

func writeDecision(t *testing.T, directory string, decision domain.ReviewDecision) string {
	t.Helper()
	data, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "input-decision.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
