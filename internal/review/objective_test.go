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
)

func objectiveFixture(t *testing.T) (reviewFixture, DecisionInput, domain.ReviewPacket, []byte) {
	t.Helper()
	f := newReviewFixture(t)
	command := domain.TaskCommand{ID: "quote-team-client", Argv: []string{"/usr/bin/python3", "-I", "-B", "-c", "frozen-loader", "/fixture/oracle.py", strings.Repeat("a", 64), "--client", "quote_client.py"}, CWD: ".", Required: true, BaselinePolicy: "none", MaxLogBytes: 4000, TimeoutSeconds: 30}
	var document map[string]json.RawMessage
	if json.Unmarshal(f.taskData, &document) != nil {
		t.Fatal("task fixture")
	}
	var repository map[string]json.RawMessage
	_ = json.Unmarshal(document["repository"], &repository)
	repository["baseRef"], _ = json.Marshal(strings.Repeat("1", 40))
	document["repository"], _ = json.Marshal(repository)
	document["acceptance"], _ = json.Marshal(domain.TaskAcceptance{Commands: []domain.TaskCommand{command}})
	f.taskData, _ = json.Marshal(document)
	_ = json.Unmarshal(f.taskData, &f.task)
	f.specDigest, _ = canonical.DigestJSON(f.taskData)
	patch, _ := os.ReadFile(filepath.Join(f.directory, "observed.patch"))
	digest := canonical.DigestBytes(patch)
	head := materializeCandidateFixture(t, f, digest)
	f.report, _, f.manifest, _ = candidateFixtureInputs(t, f, head, digest, "objective component fixture")
	f.report.Observed.DiffDigest = digest
	f.report.Observed.DiffBytes = int64(len(patch))
	f.report.Gates = nil
	for _, id := range []string{"repository:integrity", "diff:observe", "scope:changed-paths", "format:normalize", "artifact:code"} {
		f.report.Gates = append(f.report.Gates, verification.Gate{ID: id, Category: "other", Required: id != "format:normalize", Status: "pass", Summary: "fixture", Evidence: []string{}})
	}
	zero := 0
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	record := verification.CommandRecord{Argv: command.Argv, CWD: ".", Executable: "/usr/bin/python3.11", StartedAt: now, CompletedAt: now.Add(time.Second), ExitCode: &zero, BaselineStatus: "not-run"}
	f.report.Gates = append(f.report.Gates, verification.Gate{ID: "command:" + command.ID, Category: "command", Required: true, Status: "pass", Summary: "independent fixture", Command: &record, Evidence: []string{"artifact://log-out", "artifact://log-err"}})
	if err := os.MkdirAll(filepath.Join(f.directory, "logs"), 0700); err != nil {
		t.Fatal(err)
	}
	for stream, data := range map[string][]byte{"stdout": []byte("{\"checks\":10,\"scope\":\"client\"}\n"), "stderr": {}} {
		id := "log-out"
		if stream == "stderr" {
			id = "log-err"
		}
		name := "logs/" + command.ID + "." + stream + ".log"
		if err := os.WriteFile(filepath.Join(f.directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
		f.manifest.Artifacts = append(f.manifest.Artifacts, verification.Artifact{ID: id, Kind: "command-log", Producer: "verifier", Status: "validated", PathRoot: "run", RelativePath: name, ByteSize: int64(len(data)), Digest: canonical.DigestBytes(data), CreatedAt: now, RelatedGates: []string{"command:" + command.ID}})
	}
	f.reportData, _ = json.Marshal(f.report)
	f.manifestData, _ = json.Marshal(f.manifest)
	for name, data := range map[string][]byte{"task-spec.json": f.taskData, "verification-report.json": f.reportData, "artifact-manifest.json": f.manifestData} {
		if err := os.WriteFile(filepath.Join(f.directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	packet, _ := f.build(t, 1)
	raw, err := os.ReadFile(filepath.Join(f.directory, "review-packet.json"))
	if err != nil {
		t.Fatal(err)
	}
	input := DecisionInput{Task: f.task, TaskID: "ENG-123", RunID: "run-01", SpecDigest: f.specDigest, ReviewRound: 1, AttemptsUsed: 1, Report: f.report, Manifest: f.manifest, Objective: &ObjectivePolicy{NodeID: "client", OracleDigest: "sha256:" + command.Argv[6], Command: command, ReadEvidence: acceptedFixtureRead(f)}}
	input.Objective.VerificationDigest = packet.VerificationDigest
	input.Objective.ArtifactManifestDigest = packet.ArtifactManifestDigest
	return f, input, *packet, raw
}

func TestTaskObjectiveDecisionUsesOriginalImporterAndRejectsExternalSystem(t *testing.T) {
	f, input, packet, raw := objectiveFixture(t)
	decision, err := BuildObjectiveDecision(input, packet, raw, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	importer := DecisionImporter{RunDirectory: f.directory, Validator: f.validator}
	result, err := importer.ImportBytes(input, decision)
	if err != nil || result.TargetState != domain.StateAccepted || result.Decision.Reviewer.Type != "system" {
		t.Fatalf("objective import: %v", err)
	}
	now := time.Now().UTC()
	records, err := PrepareRecords(f.directory, result, TerminalOutcome(input.TaskID, input.RunID, domain.StateAccepted, result, now))
	if err != nil {
		t.Fatal(err)
	}
	if err := records.Commit(); err != nil {
		t.Fatal(err)
	}
	state := domain.NewRunState(input.TaskID, input.RunID, now)
	state.State = domain.StateAccepted
	state.Sequence = 4
	state.ReviewRound = 1
	state.AttemptsUsed = 1
	state.CurrentAttemptID = "attempt-01"
	state.BaseSHA = f.report.BaseSHA
	state.SpecDigest = f.specDigest
	event := domain.RunEvent{Type: "review.accept", StateFrom: domain.StateReviewPending, StateTo: domain.StateAccepted, RunID: state.RunID, AttemptID: state.CurrentAttemptID, Sequence: 4, Timestamp: now, Payload: map[string]any{"verdict": "accept", "decisionDigest": result.DecisionDigest, "evidenceDigest": result.Decision.EvidenceDigest}}
	if _, err := ReadAcceptedCandidateWithObjective(state, event, "authority-01", f.validator, acceptedFixtureRead(f), input.Objective); err != nil {
		t.Fatal("accepted Task source rejected", err)
	}
	if _, err := ReadAcceptedCandidate(state, event, "authority-01", f.validator, acceptedFixtureRead(f)); err == nil {
		t.Fatal("legacy reader inherited system authority")
	}
	input.Objective = nil
	if _, err := importer.ImportBytes(input, decision); err == nil {
		t.Fatal("system label granted external authority")
	}
	var foreign domain.ReviewDecision
	_ = json.Unmarshal(decision, &foreign)
	foreign.Reviewer.ID = "worker"
	forged, _ := json.Marshal(foreign)
	if _, err := importer.ImportBytes(input, forged); err == nil {
		t.Fatal("foreign system imported")
	}
}

func TestTaskObjectiveRejectsMissingOrForgedIndependentEvidence(t *testing.T) {
	for _, mode := range []string{"missing-policy", "missing-verifier-event", "unknown-node", "oracle-drift", "worker-command", "missing-gate", "extra-gate", "failed-required", "no-command", "wrong-argv", "no-exit", "truncated", "wrong-scope", "few-checks", "replaced-log", "wrong-packet", "missing-candidate", "previous-finding"} {
		t.Run(mode, func(t *testing.T) {
			f, input, packet, raw := objectiveFixture(t)
			command := input.Report.Gates[len(input.Report.Gates)-1].Command
			switch mode {
			case "missing-verifier-event":
				input.Objective.VerificationDigest = ""
			case "missing-policy":
				input.Objective = nil
			case "unknown-node":
				input.Objective.NodeID = "other"
			case "oracle-drift":
				input.Objective.OracleDigest = packetTestDigest("b")
			case "worker-command":
				input.Manifest.Artifacts[len(input.Manifest.Artifacts)-1].Producer = "worker"
			case "missing-gate":
				input.Report.Gates = input.Report.Gates[1:]
			case "extra-gate":
				input.Report.Gates = append(input.Report.Gates, verification.Gate{ID: "unknown", Status: "pass"})
			case "failed-required":
				input.Report.Gates[0].Status = "fail"
			case "no-command":
				input.Report.Gates[len(input.Report.Gates)-1].Command = nil
			case "wrong-argv":
				command.Argv = append([]string{"echo"}, command.Argv[1:]...)
			case "no-exit":
				command.ExitCode = nil
			case "truncated":
				command.Truncated = true
			case "wrong-scope", "few-checks", "replaced-log":
				data := []byte("{\"checks\":10,\"scope\":\"integration\"}")
				if mode == "few-checks" {
					data = []byte("{\"checks\":1,\"scope\":\"client\"}")
				}
				if err := os.WriteFile(filepath.Join(f.directory, "logs/quote-team-client.stdout.log"), data, 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong-packet":
				packet.VerificationDigest = packetTestDigest("b")
			case "missing-candidate":
				input.Report.CandidateDigest = ""
			case "previous-finding":
				packet.PreviousBlockingFindings = []domain.PreviousFinding{{}}
			}
			if _, err := BuildObjectiveDecision(input, packet, raw, time.Now()); err == nil {
				t.Fatal("unsafe evidence accepted")
			}
		})
	}
}
