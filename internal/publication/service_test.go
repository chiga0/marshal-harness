package publication

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/verification"
)

var controlledCommitTestSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

type controlledCommitFixture struct {
	repository   string
	worktreePath string
	runDir       string
	baseSHA      string
	state        domain.RunState
	decision     domain.ReviewDecision
	observation  verification.Observation
	title        string
}

// newControlledCommitFixture builds a temporary repository with a managed
// linked worktree that carries tracked modifications, a new executable file
// and a new symlink, mirroring the inputs controlledCommit receives during a
// real publication without touching any real .marshal state.
func newControlledCommitFixture(t *testing.T) *controlledCommitFixture {
	t.Helper()
	repository, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string { return runFixtureGit(t, repository, args...) }
	git("init", "-q")
	git("symbolic-ref", "HEAD", "refs/heads/main")
	git("config", "user.name", "Marshal Test")
	git("config", "user.email", "marshal@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("commit", "-q", "-m", "base")
	baseSHA := git("rev-parse", "HEAD")
	stateRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := gitworktree.Open(repository)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := manager.Create(stateRoot, "TASK-CTRLCOMMIT", baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	if err := worktree.Release(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command("git", "-C", repository, "worktree", "remove", "--force", worktree.Path).Run()
		_ = exec.Command("git", "-C", repository, "branch", "-D", worktree.Branch).Run()
	})
	worktreePath := worktree.Path
	writeFile := func(path, content string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(filepath.Join(worktreePath, "README.md"), "base\nfeature\n", 0o600)
	writeFile(filepath.Join(worktreePath, "src", "code.go"), "package src\n\nfunc Value() int {\n\treturn 1\n}\n", 0o600)
	writeFile(filepath.Join(worktreePath, "scripts", "build.sh"), "#!/bin/sh\necho build\n", 0o700)
	if err := os.Symlink("code.go", filepath.Join(worktreePath, "src", "current.go")); err != nil {
		t.Fatal(err)
	}
	observation, err := verification.ObserveContext(context.Background(), worktreePath, baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.ChangedFiles) == 0 {
		t.Fatal("fixture worktree reports no changed files")
	}
	now := time.Date(2026, 8, 12, 3, 0, 0, 0, time.UTC)
	state := domain.NewRunState("TASK-CTRLCOMMIT", "run:ctrlcommit", now)
	state.State = domain.StatePublishing
	state.SpecDigest = "sha256:" + strings.Repeat("a", 64)
	state.BaseSHA = baseSHA
	state.WorktreePath = worktreePath
	decision := domain.ReviewDecision{
		APIVersion:     domain.APIVersionV1Alpha1,
		Kind:           domain.KindReviewDecision,
		TaskID:         state.TaskID,
		RunID:          state.RunID,
		ReviewRound:    1,
		EvidenceDigest: "sha256:" + strings.Repeat("d", 64),
		Verdict:        "accept",
		DecidedAt:      now,
	}
	return &controlledCommitFixture{
		repository:   repository,
		worktreePath: worktreePath,
		runDir:       t.TempDir(),
		baseSHA:      baseSHA,
		state:        state,
		decision:     decision,
		observation:  observation,
		title:        "Controlled commit regression",
	}
}

func (f *controlledCommitFixture) commit(t *testing.T, parentSHA string, candidates candidateBinding) (string, error) {
	t.Helper()
	return controlledCommit(context.Background(), f.worktreePath, f.runDir, f.baseSHA, parentSHA, f.title, f.state, f.decision, f.observation, candidates)
}

func TestControlledCommitProducesValidSHA(t *testing.T) {
	fixture := newControlledCommitFixture(t)
	commitSHA, err := fixture.commit(t, fixture.baseSHA, candidateBinding{})
	if err != nil {
		t.Fatalf("controlledCommit failed: %v", err)
	}
	if !controlledCommitTestSHA.MatchString(commitSHA) {
		t.Fatalf("commit SHA = %q, want a 40 hex object id", commitSHA)
	}
	runFixtureGit(t, fixture.repository, "cat-file", "-e", commitSHA)
	if kind := runFixtureGit(t, fixture.repository, "cat-file", "-t", commitSHA); kind != "commit" {
		t.Fatalf("object type = %q, want commit", kind)
	}
	raw := runFixtureGit(t, fixture.repository, "cat-file", "-p", commitSHA)
	if !strings.Contains(raw, "parent "+fixture.baseSHA) {
		t.Fatalf("commit does not reference parent %s:\n%s", fixture.baseSHA, raw)
	}
	if !strings.Contains(raw, fixture.title) {
		t.Fatalf("commit message does not contain the sanitized subject %q:\n%s", fixture.title, raw)
	}
	for _, trailer := range []string{
		"Marshal-Task: " + fixture.state.TaskID,
		"Marshal-Run: " + fixture.state.RunID,
		"Marshal-Spec-Digest: " + fixture.state.SpecDigest,
		"Marshal-Evidence-Digest: " + fixture.decision.EvidenceDigest,
		"Marshal-Snapshot-Digest: " + fixture.observation.SnapshotDigest,
	} {
		if !strings.Contains(raw, trailer) {
			t.Fatalf("commit message missing trailer %q", trailer)
		}
	}
	leftovers, err := filepath.Glob(filepath.Join(fixture.runDir, ".publication-index-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary controlled indexes not cleaned up: %v (err=%v)", leftovers, err)
	}
	if strings.Contains(raw, "Marshal-Candidate-Digest") || strings.Contains(raw, "Marshal-Worker-Candidate-Digest") {
		t.Fatalf("legacy commit message must not carry candidate trailers:\n%s", raw)
	}
}

// TestControlledCommitAppendsCandidateTrailers pins the trailer-append rule
// (design §8 R1): candidate identities are appended as new trailers without
// disturbing the frozen legacy trailer set.
func TestControlledCommitAppendsCandidateTrailers(t *testing.T) {
	fixture := newControlledCommitFixture(t)
	candidates := candidateBinding{head: "sha256:" + strings.Repeat("a", 64), worker: "sha256:" + strings.Repeat("b", 64)}
	commitSHA, err := fixture.commit(t, fixture.baseSHA, candidates)
	if err != nil {
		t.Fatalf("controlledCommit with candidate binding failed: %v", err)
	}
	raw := runFixtureGit(t, fixture.repository, "cat-file", "-p", commitSHA)
	for _, trailer := range []string{
		"Marshal-Task: " + fixture.state.TaskID,
		"Marshal-Run: " + fixture.state.RunID,
		"Marshal-Spec-Digest: " + fixture.state.SpecDigest,
		"Marshal-Evidence-Digest: " + fixture.decision.EvidenceDigest,
		"Marshal-Snapshot-Digest: " + fixture.observation.SnapshotDigest,
		"Marshal-Candidate-Digest: " + candidates.head,
		"Marshal-Worker-Candidate-Digest: " + candidates.worker,
	} {
		if !strings.Contains(raw, trailer) {
			t.Fatalf("commit message missing trailer %q:\n%s", trailer, raw)
		}
	}
	if index := strings.Index(raw, "Marshal-Snapshot-Digest"); index >= strings.Index(raw, "Marshal-Candidate-Digest") {
		t.Fatalf("candidate trailers must be appended after the legacy trailer set:\n%s", raw)
	}
	// Determinism: repeating the identical binding reproduces the identical
	// commit object.
	again, err := fixture.commit(t, fixture.baseSHA, candidates)
	if err != nil || again != commitSHA {
		t.Fatalf("candidate-bound commit is not deterministic: %s vs %s (err=%v)", again, commitSHA, err)
	}
}

// TestCandidateBindingForPublication exercises the fail-closed cross-check
// between the verification report and the review packet.
func TestCandidateBindingForPublication(t *testing.T) {
	head := "sha256:" + strings.Repeat("a", 64)
	worker := "sha256:" + strings.Repeat("b", 64)
	foreign := "sha256:" + strings.Repeat("c", 64)
	tests := []struct {
		name    string
		report  verification.Report
		packet  domain.ReviewPacket
		want    candidateBinding
		wantErr string
	}{
		{name: "legacy evidence keeps empty binding", report: verification.Report{}, packet: domain.ReviewPacket{}, want: candidateBinding{}},
		{name: "matching candidate binding", report: verification.Report{CandidateDigest: head, WorkerCandidateDigest: worker}, packet: domain.ReviewPacket{CandidateDigest: head, WorkerCandidateDigest: worker}, want: candidateBinding{head: head, worker: worker}},
		{name: "partial report binding", report: verification.Report{CandidateDigest: head}, packet: domain.ReviewPacket{CandidateDigest: head, WorkerCandidateDigest: worker}, wantErr: "partial candidate binding"},
		{name: "partial packet binding", report: verification.Report{CandidateDigest: head, WorkerCandidateDigest: worker}, packet: domain.ReviewPacket{CandidateDigest: head}, wantErr: "partial candidate binding"},
		{name: "divergent head binding", report: verification.Report{CandidateDigest: head, WorkerCandidateDigest: worker}, packet: domain.ReviewPacket{CandidateDigest: foreign, WorkerCandidateDigest: worker}, wantErr: "differs between verification report and review packet"},
		{name: "divergent worker binding", report: verification.Report{CandidateDigest: head, WorkerCandidateDigest: worker}, packet: domain.ReviewPacket{CandidateDigest: head, WorkerCandidateDigest: foreign}, wantErr: "differs between verification report and review packet"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binding, err := candidateBindingForPublication(test.report, test.packet)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("err = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil || binding != test.want {
				t.Fatalf("binding = %+v, err = %v, want %+v", binding, err, test.want)
			}
		})
	}
}

func TestControlledCommitRejectsInvalidParent(t *testing.T) {
	fixture := newControlledCommitFixture(t)
	t.Run("missing parent object", func(t *testing.T) {
		_, err := fixture.commit(t, strings.Repeat("9", 40), candidateBinding{})
		if err == nil {
			t.Fatal("expected a missing parent to be rejected")
		}
		if strings.Contains(err.Error(), "invalid SHA") {
			t.Fatalf("error = %q, must not mask a missing parent as an invalid SHA", err)
		}
		if !strings.Contains(err.Error(), "parent") || !strings.Contains(err.Error(), strings.Repeat("9", 40)) {
			t.Fatalf("error = %q, want an explicit missing-parent diagnosis", err)
		}
	})
	t.Run("malformed parent id", func(t *testing.T) {
		_, err := fixture.commit(t, "not-an-object-id", candidateBinding{})
		if err == nil {
			t.Fatal("expected a malformed parent to be rejected")
		}
		if strings.Contains(err.Error(), "invalid SHA") {
			t.Fatalf("error = %q, must not mask a malformed parent as an invalid SHA", err)
		}
		if !strings.Contains(err.Error(), "parent") {
			t.Fatalf("error = %q, want an explicit malformed-parent diagnosis", err)
		}
	})
}

func TestControlledCommitStableUnderRepeat(t *testing.T) {
	fixture := newControlledCommitFixture(t)
	first := ""
	for iteration := 0; iteration < 5; iteration++ {
		commitSHA, err := fixture.commit(t, fixture.baseSHA, candidateBinding{})
		if err != nil {
			t.Fatalf("iteration %d failed: %v", iteration+1, err)
		}
		if !controlledCommitTestSHA.MatchString(commitSHA) {
			t.Fatalf("iteration %d returned %q, want a 40 hex object id", iteration+1, commitSHA)
		}
		runFixtureGit(t, fixture.repository, "cat-file", "-e", commitSHA)
		if first == "" {
			first = commitSHA
			continue
		}
		if commitSHA != first {
			t.Fatalf("iteration %d produced %s, want deterministic commit %s", iteration+1, commitSHA, first)
		}
	}
}

// TestControlledCommitSeparatesCommitTreeStderr reproduces the original
// failure class: git diagnostics on stderr must never pollute the commit
// object id extracted from stdout. GIT_TRACE forces git commit-tree to emit
// trace noise on stderr while still succeeding.
func TestControlledCommitSeparatesCommitTreeStderr(t *testing.T) {
	fixture := newControlledCommitFixture(t)
	tree := runFixtureGit(t, fixture.worktreePath, "rev-parse", "HEAD^{tree}")
	noisy := append(baseGitEnvironment(), "GIT_TRACE=2")
	message := "controlled commit stderr separation regression"
	stdout, stderr, err := gitExec(context.Background(), fixture.worktreePath, noisy, &message, "commit-tree", tree, "-p", fixture.baseSHA)
	if err != nil {
		t.Fatalf("git commit-tree with stderr noise failed: %v", err)
	}
	if stderr == "" {
		t.Fatal("expected GIT_TRACE to emit diagnostic noise on stderr")
	}
	commit := strings.TrimSpace(stdout)
	if !controlledCommitTestSHA.MatchString(commit) {
		t.Fatalf("commit SHA = %q, want stdout unpolluted by stderr noise", commit)
	}
	runFixtureGit(t, fixture.repository, "cat-file", "-e", commit)
}
