package cli

import (
	"bytes"
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
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/review"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// e2eSHA assembles a 40-hex object id from a repeated nibble (helper
// construction keeps fixture literals gitleaks-safe).
func e2eSHA(fill string) string { return strings.Repeat(fill, 40) }

// e2eDigest assembles exactly 64 lowercase hex characters prefixed with
// sha256: — the schema pattern ^sha256:[0-9a-f]{64}$ rejects both longer and
// non-hex literals, so every fixture digest uses this helper.
func e2eDigest(fill string) string { return "sha256:" + strings.Repeat(fill, 64) }

const fakeReconcileGHScript = `#!/bin/sh
set -u
STATE='@STATE@'
printf '%s\n' "$*" >> "$STATE/gh.log"
case "$1" in
api)
	case "$2" in
	repos/*/commits/*) cat "$STATE/commit.json"; exit 0 ;;
	repos/*) cat "$STATE/repo.json"; exit 0 ;;
	user) cat "$STATE/user.json"; exit 0 ;;
	*) exit 1 ;;
	esac
	;;
pr)
	case "$2" in
	view) cat "$STATE/pr.json"; exit 0 ;;
	checks) cat "$STATE/checks.json"; exit 0 ;;
	*) exit 1 ;;
	esac
	;;
esac
printf 'unexpected gh invocation\n' >&2
exit 1
`

type reconcileE2ERun struct {
	taskID, runID     string
	headBranch        string
	baseSHA, headSHA  string
	mergeSHA          string
	marker            string
	publicationDigest string
	decisionDigest    string
	evidenceDigest    string
}

type reconcileE2EOptions struct {
	block bool
}

// writeReconcileE2ERun fabricates a post-publication run lineage (journal,
// snapshot and evidence files) that terminated BLOCKED (or stays CI_PENDING
// for the negative case), mirroring a run whose maintainer merged the draft
// PR outside Marshal.
func writeReconcileE2ERun(t *testing.T, stateRoot string, validator *contract.Validator, opts reconcileE2EOptions) reconcileE2ERun {
	t.Helper()
	const (
		taskID = "TASK-RECONCILE-E2E"
		runID  = "run:reconcile-e2e"
	)
	run := reconcileE2ERun{
		taskID: taskID, runID: runID,
		headBranch: "marshal/task-reconcile-e2e-a1b2c3",
		baseSHA:    e2eSHA("0"), headSHA: e2eSHA("2"), mergeSHA: e2eSHA("4"),
		marker: "<!-- marshal task=" + taskID + " run=" + runID + " -->",
	}
	now := time.Date(2026, 8, 13, 6, 0, 0, 0, time.UTC)
	runDir := filepath.Join(stateRoot, "runs", runID)
	writeFile := func(name string, data []byte) {
		t.Helper()
		path := filepath.Join(runDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Frozen TaskSpec with a required publication and one required check.
	task := readCLIFixture(t, "examples/happy-path/task-spec.json")
	task["metadata"].(map[string]any)["id"] = taskID
	repositorySection := task["repository"].(map[string]any)
	repositorySection["path"] = "/tmp/reconcile-e2e-repository"
	repositorySection["baseRef"] = run.baseSHA
	repositorySection["expectedRemoteUrl"] = "https://github.com/marshal-test/reconcile-e2e.git"
	publication := task["publication"].(map[string]any)
	publication["requiredChecks"] = []any{"ci/test"}
	taskData, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(domain.KindTask, taskData); err != nil {
		t.Fatalf("e2e TaskSpec fixture invalid: %v", err)
	}
	specDigest, err := canonical.DigestJSON(taskData)
	if err != nil {
		t.Fatal(err)
	}
	writeFile("task-spec.json", taskData)

	// PolicySnapshot: detached policyDigest stamped exactly the way planning
	// verifies it; every digest literal is exactly 64 lowercase hex chars.
	policy := readCLIFixture(t, "examples/happy-path/policy-snapshot.json")
	policy["taskId"] = taskID
	policy["runId"] = runID
	effective := policy["effective"].(map[string]any)
	effective["allowPublication"] = true
	effective["allowMerge"] = false
	sources := []any{}
	for _, source := range policy["sources"].([]any) {
		entry := source.(map[string]any)
		entry["digest"] = e2eDigest("b")
		sources = append(sources, entry)
	}
	policy["sources"] = sources
	policy["policyDigest"] = ""
	policyData, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	policyDigest, err := canonical.DigestJSON(policyData)
	if err != nil {
		t.Fatal(err)
	}
	policy["policyDigest"] = policyDigest
	policyData, err = json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(domain.KindPolicySnapshot, policyData); err != nil {
		t.Fatalf("e2e PolicySnapshot fixture invalid: %v", err)
	}
	writeFile("policy-snapshot.json", policyData)

	reportDigest := e2eDigest("1")
	manifestDigest := e2eDigest("9")
	workerDigest := e2eDigest("8")
	identity := map[string]any{
		"specDigest":               specDigest,
		"patchDigest":              e2eDigest("7"),
		"verificationDigest":       reportDigest,
		"artifactManifestDigest":   manifestDigest,
		"workerResultDigests":      []any{workerDigest},
		"previousBlockingFindings": []any{},
	}
	identityData, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	evidenceDigest, err := canonical.DigestJSON(identityData)
	if err != nil {
		t.Fatal(err)
	}
	run.evidenceDigest = evidenceDigest

	// ReviewPacket binding the (fabricated) verification evidence. All digest
	// literals are exactly 64 lowercase hex characters.
	packet := readCLIFixture(t, "examples/happy-path/review-packet.json")
	packet["taskId"] = taskID
	packet["runId"] = runID
	packet["specDigest"] = specDigest
	packet["baseSha"] = run.baseSHA
	packet["snapshotDigest"] = e2eDigest("3")
	packet["diffDigest"] = e2eDigest("5")
	packet["verificationDigest"] = reportDigest
	packet["artifactManifestDigest"] = manifestDigest
	packet["workerResultDigests"] = []any{workerDigest}
	packet["evidenceDigest"] = evidenceDigest
	packetData, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(domain.KindReviewPacket, packetData); err != nil {
		t.Fatalf("e2e ReviewPacket fixture invalid: %v", err)
	}
	packetDigest, err := canonical.DigestJSON(packetData)
	if err != nil {
		t.Fatal(err)
	}
	writeFile("review-packet.json", packetData)

	// Accepting ReviewDecision for round 1.
	decision := domain.ReviewDecision{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindReviewDecision,
		TaskID: taskID, RunID: runID, ReviewRound: 1,
		Reviewer:   domain.Reviewer{Type: "lead-agent", ID: "reconcile-e2e"},
		SpecDigest: specDigest, ReviewPacketDigest: packetDigest,
		VerificationDigest: reportDigest, ArtifactManifestDigest: manifestDigest,
		EvidenceDigest: evidenceDigest, Verdict: "accept", Summary: "accept and publish",
		BlockingFindings: []domain.Finding{}, NonBlockingFindings: []domain.Finding{},
		PublicationRecommendation: "publish", MergeRecommendation: "do-not-merge",
		DecidedAt: now,
	}
	decisionData, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(domain.KindReviewDecision, decisionData); err != nil {
		t.Fatalf("e2e ReviewDecision fixture invalid: %v", err)
	}
	decisionDigest, err := canonical.DigestJSON(decisionData)
	if err != nil {
		t.Fatal(err)
	}
	run.decisionDigest = decisionDigest
	writeFile(filepath.Join("decisions", "decision-001.json"), decisionData)

	// Frozen PublicationRecord for the draft PR that the maintainer merged.
	publicationRecord := domain.PublicationRecord{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindPublicationRecord,
		TaskID: taskID, RunID: runID, Provider: "github",
		Repository: domain.PublicationRepository{ID: "R_reconcilee2e01", NameWithOwner: "marshal-test/reconcile-e2e", URL: "https://github.com/marshal-test/reconcile-e2e"},
		Remote:     "origin", BaseBranch: "main", HeadBranch: run.headBranch, ReviewRound: 1,
		BaseSHA: run.baseSHA, HeadSHA: run.headSHA, CommitSHA: run.headSHA,
		SnapshotDigest: e2eDigest("3"), DiffDigest: e2eDigest("5"),
		SpecDigest: specDigest, PolicyDigest: policyDigest,
		EvidenceDigest: evidenceDigest, VerificationDigest: reportDigest,
		ReviewDecisionDigest: decisionDigest,
		Marker:               run.marker, Mode: "draft", MergePolicy: "never",
		Request: domain.PullRequestRecord{ID: "PR_reconcilee2e01", Number: 7, URL: "https://github.com/marshal-test/reconcile-e2e/pull/7", Draft: true, State: "OPEN"},
		Actor:   "marshal-github-publisher", PublishedAt: now, UpdatedAt: now,
	}
	publicationData, err := json.Marshal(publicationRecord)
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(domain.KindPublicationRecord, publicationData); err != nil {
		t.Fatalf("e2e PublicationRecord fixture invalid: %v", err)
	}
	publicationDigest, err := canonical.DigestJSON(publicationData)
	if err != nil {
		t.Fatal(err)
	}
	run.publicationDigest = publicationDigest
	writeFile("publication-record.json", publicationData)

	// Journal lineage.
	store := runstore.New(stateRoot)
	lease, err := store.Acquire(runID)
	if err != nil {
		t.Fatal(err)
	}
	transitions := []struct {
		from, to  domain.State
		eventType string
		attemptID string
		actor     *domain.Actor
		payload   map[string]any
	}{
		{domain.StateCreated, domain.StatePlanned, "task.planned", "", nil, map[string]any{"fixture": true}},
		{domain.StatePlanned, domain.StateReady, "task.ready", "", nil, map[string]any{"fixture": true}},
		{domain.StateReady, domain.StateRunning, "worker.started", "attempt:1", nil, map[string]any{"fixture": true}},
		{domain.StateRunning, domain.StateVerifying, "worker.completed", "", nil, map[string]any{"fixture": true}},
		{domain.StateVerifying, domain.StateReviewPending, "verification.completed", "", &domain.Actor{Type: "system", ID: "marshal-verifier"}, map[string]any{"reportDigest": reportDigest, "artifactManifestDigest": manifestDigest, "status": "pass"}},
		{domain.StateReviewPending, domain.StatePublishing, "review.accept", "", &domain.Actor{Type: "system", ID: "marshal-review"}, map[string]any{"verdict": "accept", "decisionDigest": decisionDigest, "evidenceDigest": evidenceDigest}},
		{domain.StatePublishing, domain.StatePublished, "publication.completed", "", &domain.Actor{Type: "publisher", ID: "marshal-github-publisher"}, map[string]any{
			"publicationDigest": publicationDigest, "provider": "github", "repository": "marshal-test/reconcile-e2e",
			"headBranch": run.headBranch, "baseBranch": "main", "externalId": "PR_reconcilee2e01",
			"headSha": run.headSHA, "uri": "https://github.com/marshal-test/reconcile-e2e/pull/7",
		}},
		{domain.StatePublished, domain.StateCIPending, "publication.checks-requested", "", &domain.Actor{Type: "publisher", ID: "marshal-github-publisher"}, map[string]any{"requiredChecks": []any{"ci/test"}}},
	}
	if opts.block {
		transitions = append(transitions, struct {
			from, to  domain.State
			eventType string
			attemptID string
			actor     *domain.Actor
			payload   map[string]any
		}{domain.StateCIPending, domain.StateBlocked, "publication.blocked", "", &domain.Actor{Type: "publisher", ID: "marshal-github-publisher"}, map[string]any{"error": "remote PR head or identity changed", "terminalReason": "publication safety gate failed"}})
	}
	for index, transition := range transitions {
		event := domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent,
			EventID: fmt.Sprintf("event:reconcile-e2e:%d", index+1), RunID: runID,
			Sequence: uint64(index + 1), Type: transition.eventType,
			StateFrom: transition.from, StateTo: transition.to, Timestamp: now,
			Actor: transition.actor, Payload: transition.payload,
		}
		if transition.attemptID != "" {
			event.AttemptID = transition.attemptID
		}
		if err := store.Append(lease, event, uint64(index)); err != nil {
			t.Fatal(err)
		}
	}

	// Snapshot.
	state := domain.NewRunState(taskID, runID, now)
	state.Sequence = uint64(len(transitions))
	state.SpecDigest = specDigest
	state.PolicyDigest = policyDigest
	state.BaseSHA = run.baseSHA
	state.WorktreePath = filepath.Join(stateRoot, "worktrees", "task-reconcile-e2e")
	state.AttemptsUsed, state.ReviewRound, state.CurrentAttemptID = 1, 1, "attempt:1"
	state.Publication = &domain.RunPublication{Provider: "github", Repository: "marshal-test/reconcile-e2e", HeadBranch: run.headBranch, BaseBranch: "main", ExternalID: "PR_reconcilee2e01", URI: "https://github.com/marshal-test/reconcile-e2e/pull/7", HeadSHA: run.headSHA}
	if opts.block {
		state.State = domain.StateBlocked
		state.TerminalReason = "publication safety gate failed"
	} else {
		state.State = domain.StateCIPending
	}
	state.UpdatedAt = now
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Inspect(runID); err != nil {
		t.Fatalf("fabricated e2e run is inconsistent: %v", err)
	}

	// Blocked terminal Outcome (archived by a successful reconcile).
	if opts.block {
		prepared, err := review.PrepareOutcome(runDir, review.OutcomeData{
			TaskID: taskID, RunID: runID, TerminalState: domain.StateBlocked, Verdict: "blocked",
			FinalReviewRound: 1, FinalReviewDigest: decisionDigest, FinalEvidenceDigest: evidenceDigest,
			Summary: "publication safety gate failed: remote PR head or identity changed", GeneratedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := prepared.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	return run
}

// seedReconcileGH writes the fake gh state observing a merged PR whose node
// facts stay complete even though the head branch has been deleted remotely.
func seedReconcileGH(t *testing.T, stateDir string, run reconcileE2ERun) {
	t.Helper()
	pr := map[string]any{
		"id": "PR_reconcilee2e01", "number": 7, "url": "https://github.com/marshal-test/reconcile-e2e/pull/7",
		"isDraft": false, "state": "MERGED",
		"headRefName": run.headBranch, "headRefOid": run.headSHA,
		"headRepositoryOwner": map[string]any{"login": "marshal-test"},
		"isCrossRepository":   false,
		"baseRefName":         "main", "baseRefOid": run.baseSHA,
		"mergedAt":    "2026-08-13T05:00:00Z",
		"mergedBy":    map[string]any{"login": "maintainer"},
		"mergeCommit": map[string]any{"oid": run.mergeSHA},
		"body":        "## 目标\n\ne2e fixture\n\n" + run.marker,
	}
	prData, err := json.Marshal(pr)
	if err != nil {
		t.Fatal(err)
	}
	commit := map[string]any{
		"parents": []any{map[string]any{"sha": run.baseSHA}, map[string]any{"sha": run.headSHA}},
		"tree":    map[string]any{"sha": e2eSHA("5")},
	}
	commitData, err := json.Marshal(commit)
	if err != nil {
		t.Fatal(err)
	}
	checks := `[{"name":"ci/test","bucket":"pass","link":"https://github.com/marshal-test/reconcile-e2e/actions/runs/1","state":"COMPLETED"}]`
	for name, content := range map[string]string{
		"pr.json":     string(prData),
		"commit.json": string(commitData),
		"checks.json": checks,
		"repo.json":   `{"node_id":"R_reconcilee2e01","full_name":"marshal-test/reconcile-e2e","html_url":"https://github.com/marshal-test/reconcile-e2e"}`,
		"user.json":   `{"login":"maintainer"}`,
	} {
		if err := os.WriteFile(filepath.Join(stateDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func newReconcileE2EEnvironment(t *testing.T) (repositoryRoot, ghStateDir string) {
	t.Helper()
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot = t.TempDir()
	repositoryRoot, err = filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryRoot, "init", "-q", "-b", "main")
	runGit(t, repositoryRoot, "config", "user.email", "marshal@example.invalid")
	runGit(t, repositoryRoot, "config", "user.name", "Marshal Test")
	if err := os.WriteFile(filepath.Join(repositoryRoot, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryRoot, "add", "README.md")
	runGit(t, repositoryRoot, "commit", "-q", "-m", "fixture")
	runGit(t, repositoryRoot, "remote", "add", "origin", "https://github.com/marshal-test/reconcile-e2e.git")
	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"init"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("init exit = %d, stderr = %s", exit, stderr.String())
	}

	// Fake gh binary (hermetic: no real network or credentials).
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	configDir := filepath.Join(root, "gh-config")
	ghStateDir = filepath.Join(root, "state")
	for _, dir := range []string{binDir, configDir, ghStateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ghPath := filepath.Join(binDir, "gh")
	if err := os.WriteFile(ghPath, []byte(strings.ReplaceAll(fakeReconcileGHScript, "@STATE@", ghStateDir)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARSHAL_GH_PATH", ghPath)
	t.Setenv("MARSHAL_GH_CONFIG_DIR", configDir)
	t.Setenv("MARSHAL_STATE_DIR", "")
	return repositoryRoot, ghStateDir
}

func TestTaskReconcileUsageAndGuards(t *testing.T) {
	for _, args := range [][]string{
		{"task", "reconcile"},
		{"task", "reconcile", "--run", "run-1", "extra"},
	} {
		var stdout, stderr bytes.Buffer
		if exit := Run(args, strings.NewReader(""), &stdout, &stderr); exit != ExitUsage {
			t.Fatalf("Run(%v) exit = %d, want %d; stderr = %s", args, exit, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "marshal task reconcile --run RUN_ID") {
			t.Fatalf("usage missing from stderr: %q", stderr.String())
		}
	}
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := t.TempDir()
	runGit(t, repositoryRoot, "init", "-q")
	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"init"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("init exit = %d, stderr = %s", exit, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "reconcile", "--run", "../escape"}, strings.NewReader(""), &stdout, &stderr); exit != ExitUsage || !strings.Contains(stderr.String(), "Run ID 无效") {
		t.Fatalf("invalid run id exit = %d, stderr = %q", exit, stderr.String())
	}
}

func TestTaskReconcileEndToEndMigratesBlockedRunToAccepted(t *testing.T) {
	repositoryRoot, ghStateDir := newReconcileE2EEnvironment(t)
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	run := writeReconcileE2ERun(t, filepath.Join(repositoryRoot, ".marshal"), validator, reconcileE2EOptions{block: true})
	seedReconcileGH(t, ghStateDir, run)

	var stdout, stderr bytes.Buffer
	exit := Run([]string{"task", "reconcile", "--run", run.runID, "--actor", "maintainer", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitOK {
		t.Fatalf("task reconcile exit = %d, stderr = %s", exit, stderr.String())
	}
	var result struct {
		State   domain.RunState                   `json:"state"`
		Receipt domain.SCMMergeReceipt            `json:"scmMergeReceipt"`
		Record  domain.PublicationReconcileRecord `json:"publicationReconcileRecord"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode reconcile output: %v\n%s", err, stdout.String())
	}
	if result.State.State != domain.StateAccepted || result.State.TerminalReason != "reconciled-after-merge" {
		t.Fatalf("state = %+v", result.State)
	}
	if result.Receipt.MergeCommitSha != run.mergeSHA || result.Receipt.HeadOid != run.headSHA || result.Receipt.BaseOid != run.baseSHA {
		t.Fatalf("receipt merge facts = %+v", result.Receipt)
	}
	if result.Record.ReconcileType != "accept-after-merge" || result.Record.ObservedState != domain.StateBlocked || result.Record.DecidedState != domain.StateAccepted || result.Record.ReconciledBy != "maintainer" {
		t.Fatalf("record = %+v", result.Record)
	}

	runDir := filepath.Join(repositoryRoot, ".marshal", "runs", run.runID)
	// Immutable receipt persisted under the run directory.
	receiptData, err := os.ReadFile(filepath.Join(runDir, "scm-merge-receipt.json"))
	if err != nil {
		t.Fatalf("receipt missing: %v", err)
	}
	if err := validator.Validate(domain.KindSCMMergeReceipt, receiptData); err != nil {
		t.Fatalf("receipt invalid: %v", err)
	}
	// Append-only record directory carries exactly one record.
	entries, err := os.ReadDir(filepath.Join(runDir, "publication-reconcile-records"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("reconcile records = %+v, err = %v", entries, err)
	}
	recordData, err := os.ReadFile(filepath.Join(runDir, "publication-reconcile-records", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(domain.KindPublicationReconcileRecord, recordData); err != nil {
		t.Fatalf("record invalid: %v", err)
	}
	// Blocked outcome archived, accepted outcome written.
	archives, err := filepath.Glob(filepath.Join(runDir, "outcomes", "blocked-*", "outcome.json"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("blocked outcome archive = %v (err=%v)", archives, err)
	}
	outcomeData, err := os.ReadFile(filepath.Join(runDir, "outcome.json"))
	if err != nil {
		t.Fatalf("accepted outcome missing: %v", err)
	}
	var outcome domain.OutcomeBundle
	if err := json.Unmarshal(outcomeData, &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.TerminalState != domain.StateAccepted {
		t.Fatalf("outcome = %+v", outcome)
	}
	// Journal tail is the single typed reconciliation event.
	store := runstore.New(filepath.Join(repositoryRoot, ".marshal"))
	events, _, err := store.ReadEvents(run.runID)
	if err != nil {
		t.Fatal(err)
	}
	reconcileEvents := 0
	for _, event := range events {
		if event.Type == lifecycle.PublicationReconcileEventType {
			reconcileEvents++
		}
	}
	if reconcileEvents != 1 {
		t.Fatalf("reconcile events = %d, want 1", reconcileEvents)
	}
	last := events[len(events)-1]
	if last.Type != lifecycle.PublicationReconcileEventType || last.StateFrom != domain.StateBlocked || last.StateTo != domain.StateAccepted {
		t.Fatalf("journal tail = %+v", last)
	}
	// Records and receipt never leak local paths or credential material.
	for label, document := range map[string]string{"receipt": string(receiptData), "record": string(recordData), "stdout": stdout.String()} {
		for _, forbidden := range []string{repositoryRoot, "gh-config", "token"} {
			if strings.Contains(document, forbidden) {
				t.Fatalf("%s leaks %q", label, forbidden)
			}
		}
	}

	// Idempotent replay: no second event, no second record.
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "reconcile", "--run", run.runID, "--actor", "maintainer", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("idempotent replay exit = %d, stderr = %s", exit, stderr.String())
	}
	events, _, err = store.ReadEvents(run.runID)
	if err != nil {
		t.Fatal(err)
	}
	reconcileEvents = 0
	for _, event := range events {
		if event.Type == lifecycle.PublicationReconcileEventType {
			reconcileEvents++
		}
	}
	if reconcileEvents != 1 {
		t.Fatalf("idempotent replay appended %d events", reconcileEvents-1)
	}
	entriesAfterReplay, err := os.ReadDir(filepath.Join(runDir, "publication-reconcile-records"))
	if err != nil || len(entriesAfterReplay) != 1 {
		t.Fatalf("replay duplicated records: %+v (err=%v)", entriesAfterReplay, err)
	}
}

func TestTaskReconcileRejectsNonBlockedRun(t *testing.T) {
	repositoryRoot, ghStateDir := newReconcileE2EEnvironment(t)
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	run := writeReconcileE2ERun(t, filepath.Join(repositoryRoot, ".marshal"), validator, reconcileE2EOptions{block: false})
	seedReconcileGH(t, ghStateDir, run)

	var stdout, stderr bytes.Buffer
	exit := Run([]string{"task", "reconcile", "--run", run.runID, "--actor", "maintainer", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitFailure {
		t.Fatalf("reconcile of CI_PENDING run exit = %d, want %d; stderr = %s", exit, ExitFailure, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cannot be reconciled") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	store := runstore.New(filepath.Join(repositoryRoot, ".marshal"))
	state, err := store.Inspect(run.runID)
	if err != nil || state.State != domain.StateCIPending {
		t.Fatalf("rejected reconcile mutated the run: %+v (err=%v)", state, err)
	}
	if _, statErr := os.Lstat(filepath.Join(repositoryRoot, ".marshal", "runs", run.runID, "scm-merge-receipt.json")); !os.IsNotExist(statErr) {
		t.Fatal("rejected reconcile must not persist a receipt")
	}
}
