package review

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/planning"
	"github.com/chiga0/marshal-harness/internal/verification"
)

// Real original Verifier, frozen repository oracle, PacketBuilder and
// DecisionImporter; only the candidate and transcript are deterministic test
// inputs. No Worker, model, alternate verification report or gate producer.
func originalVerifierObjectiveFixture(t *testing.T, declaredTools bool) (reviewFixture, DecisionInput, domain.ReviewPacket, []byte) {
	t.Helper()
	f := newReviewFixture(t)
	repository := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", repository}, args...)...)
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("-c", "user.name=Marshal Test", "-c", "user.email=marshal@example.invalid", "commit", "--allow-empty", "-q", "-m", "base")
	base := git("rev-parse", "HEAD")
	manager, err := gitworktree.Open(repository)
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(repository, ".marshal")
	if err := os.MkdirAll(filepath.Join(stateRoot, "locks"), 0700); err != nil {
		t.Fatal(err)
	}
	worktree, err := manager.Create(stateRoot, "objective-verifier", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worktree.Release() })
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	command := planning.OrderQuoteOracleCommand(root, "client")
	f.task.Repository.Path, f.task.Repository.BaseRef = repository, base
	f.task.Acceptance = domain.TaskAcceptance{Commands: []domain.TaskCommand{command}}
	f.task.Scope.AllowPaths = []string{"quote_client.py"}
	f.task.Deliverables = []domain.TaskDeliverable{{ID: "client", Kind: "code", Required: true, PathGlob: "quote_client.py", MediaType: "text/plain", MinimumCount: 1}}
	if declaredTools {
		f.task.Worker.Tools = []string{"read"}
	}
	f.taskData, err = json.Marshal(f.task)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.validator.Validate(domain.KindTask, f.taskData); err != nil {
		t.Fatal("complete frozen Task fixture", err)
	}
	f.specDigest, err = canonical.DigestJSON(f.taskData)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"task-spec.json": f.taskData,
		"attempts/attempt-01/control/output/fixture-transcript-meta.json": []byte(`{"toolNames":["read"],"permissionDenied":false,"denialsBenign":0,"denialsFatal":0}`),
	} {
		path := filepath.Join(f.directory, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// This client must pass the repository's actual response challenges, not
	// print an expected status label. Python is the original verifier command.
	client := `import http.client, json
from urllib.parse import urlsplit
def quote_order(base_url, items):
    u = urlsplit(base_url)
    if u.scheme != 'http' or u.hostname != '127.0.0.1' or not u.port or u.username is not None or u.password is not None or u.path or u.query or u.fragment:
        raise ValueError('endpoint')
    c = http.client.HTTPConnection(u.hostname, u.port, timeout=2)
    try:
        c.request('POST', '/quote', json.dumps({'items':items}), {'Content-Type':'application/json'})
        r = c.getresponse()
        result = json.loads(r.read(65537))
        if r.status != 200 or type(result) is not dict or set(result) != {'subtotal_cents','shipping_cents','total_cents'} or any(type(x) is not int for x in result.values()):
            raise ValueError('response')
        return result
    finally:
        c.close()
`
	if err := os.WriteFile(filepath.Join(worktree.Path, "quote_client.py"), []byte(client), 0644); err != nil {
		t.Fatal(err)
	}
	scope, deliverables, commands := verification.PolicyFromTask(f.task)
	verified, err := verification.New().Verify(context.Background(), verification.Input{
		TaskID: "ENG-123", RunID: "run-01", AttemptID: "attempt-01", AuthorityNamespaceID: "authority-01",
		SpecDigest: f.specDigest, BaseSHA: base, Worktree: worktree.Path, ExpectedCommonDir: manager.CommonDir,
		RunDirectory: f.directory, Scope: scope, Deliverables: deliverables, Commands: commands,
		ToolAllowlist: verification.ToolAllowlistFromTask(f.task), PatchCaptureBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if verified.Report.Status != "pass" {
		t.Fatalf("original verifier: %+v", verified.Report.Gates)
	}
	f.report, f.manifest = verified.Report, verified.Manifest
	f.reportData, err = os.ReadFile(filepath.Join(f.directory, "verification-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.manifestData, err = os.ReadFile(filepath.Join(f.directory, "artifact-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	packet, _, err := (&PacketBuilder{RunDirectory: f.directory, Validator: f.validator}).Build(PacketBuildInput{
		Task: f.task, TaskData: f.taskData, Report: f.report, ReportData: f.reportData, Manifest: f.manifest, ManifestData: f.manifestData,
		TaskID: "ENG-123", RunID: "run-01", SpecDigest: f.specDigest, BaseSHA: base, ReviewRound: 1, AttemptsUsed: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(f.directory, "review-packet.json"))
	if err != nil {
		t.Fatal(err)
	}
	input := DecisionInput{Task: f.task, TaskID: "ENG-123", RunID: "run-01", SpecDigest: f.specDigest, ReviewRound: 1, AttemptsUsed: 1,
		Report: f.report, Manifest: f.manifest, Objective: &ObjectivePolicy{NodeID: "client", OracleDigest: "sha256:" + planning.OrderQuoteOracleDigest,
			Command: command, VerificationDigest: packet.VerificationDigest, ArtifactManifestDigest: packet.ArtifactManifestDigest, ReadEvidence: acceptedFixtureRead(f)}}
	return f, input, *packet, raw
}

func TestTaskObjectiveOriginalVerifierSafetyGatesAndImporter(t *testing.T) {
	for _, declared := range []bool{false, true} {
		name := "undeclared-tools"
		if declared {
			name = "declared-tools"
		}
		t.Run(name, func(t *testing.T) {
			f, original, packet, raw := originalVerifierObjectiveFixture(t, declared)
			decision, err := BuildObjectiveDecision(original, packet, raw, time.Now())
			if err != nil {
				t.Fatal("real Verifier cannot build objective Decision", err)
			}
			result, err := (&DecisionImporter{RunDirectory: f.directory, Validator: f.validator}).ImportBytes(original, decision)
			if err != nil || result.TargetState != domain.StateAccepted {
				t.Fatal("original importer", err)
			}
			for _, id := range []string{"denial-summary", "tool-audit", "tool-allowlist"} {
				for _, mode := range []string{"missing", "duplicate", "failed", "forged-required", "forged-status"} {
					t.Run(id+"/"+mode, func(t *testing.T) {
						input := original
						policy := *original.Objective
						input.Objective = &policy
						input.Report.Gates = slices.Clone(original.Report.Gates)
						index := slices.IndexFunc(input.Report.Gates, func(g verification.Gate) bool { return g.ID == id })
						if index < 0 {
							t.Fatal("producer omitted safety gate")
						}
						switch mode {
						case "missing":
							input.Report.Gates = slices.Delete(input.Report.Gates, index, index+1)
						case "duplicate":
							input.Report.Gates = append(input.Report.Gates, input.Report.Gates[index])
						case "failed":
							input.Report.Gates[index].Status = "fail"
						case "forged-required":
							input.Report.Gates[index].Required = !input.Report.Gates[index].Required
						case "forged-status":
							input.Report.Gates[index].Status = "skipped"
							if original.Report.Gates[index].Status == "skipped" {
								input.Report.Gates[index].Status = "pass"
							}
						}
						// Rebind both original-event and packet projections so this
						// assertion exercises gate policy, not the digest mismatch.
						reportData, err := json.Marshal(input.Report)
						if err != nil {
							t.Fatal(err)
						}
						policy.VerificationDigest, err = canonical.DigestJSON(reportData)
						if err != nil {
							t.Fatal(err)
						}
						changedPacket := packet
						changedPacket.VerificationDigest = policy.VerificationDigest
						changedRaw, err := json.Marshal(changedPacket)
						if err != nil {
							t.Fatal(err)
						}
						if _, err := BuildObjectiveDecision(input, changedPacket, changedRaw, time.Now()); err == nil {
							t.Fatal("forged safety gate accepted")
						}
					})
				}
			}
		})
	}
}
