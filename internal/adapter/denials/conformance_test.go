package denials

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCrossAdapterGradingConformance is the shared ADR 0013 conformance
// suite: the same benign/fatal denial sequence must produce identical grades
// under every adapter's classifier configuration. Unknown denials default to
// FATAL, BENIGN never contains write or execute events, and FATAL entries are
// exactly the ones that must terminate an attempt.
func TestCrossAdapterGradingConformance(t *testing.T) {
	worktree := resolvedDir(t)
	controlRoot := resolvedDir(t)
	tempDir := resolvedDir(t)
	if err := os.WriteFile(filepath.Join(worktree, "source.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(resolvedDir(t), "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(worktree, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	hostsPath, err := filepath.EvalSymlinks("/etc/hosts")
	if err != nil {
		t.Skip("no /etc/hosts to observe an outside read")
	}
	sequence := []struct {
		event Event
		grade Grade
	}{
		{event: Event{Tool: "read", Target: filepath.Join(controlRoot, "output", "worker-result.json")}, grade: Benign},
		{event: Event{Tool: "read", Target: filepath.Join(controlRoot, "input", "task-spec.json")}, grade: Fatal},
		{event: Event{Tool: "read", Target: filepath.Join(worktree, "source.go")}, grade: Benign},
		{event: Event{Tool: "read", Target: filepath.Join(worktree, "escape.txt")}, grade: Fatal},
		{event: Event{Tool: "read", Target: hostsPath}, grade: Fatal},
		{event: Event{Tool: "read", Target: ""}, grade: Fatal},
		{event: Event{Tool: "read", Target: "relative/path.txt"}, grade: Fatal},
		{event: Event{Tool: "edit", Target: filepath.Join(worktree, "source.go")}, grade: Fatal},
		{event: Event{Tool: "bash", Target: "curl http://evil.example"}, grade: Fatal},
		{event: Event{Tool: "question", Target: filepath.Join(worktree, "source.go")}, grade: Fatal},
	}
	providers := []string{"opencode", "pi", "qwen"}
	var vectors [][]Grade
	for _, provider := range providers {
		classifier := Classifier{Provider: provider, Worktree: worktree, ControlRoot: controlRoot, TempDir: tempDir}
		grades := make([]Grade, 0, len(sequence))
		for _, step := range sequence {
			decision := classifier.Classify(step.event)
			if decision.Grade != step.grade {
				t.Fatalf("provider %s event %+v = %s (%s), want %s", provider, step.event, decision.Grade, decision.Reason, step.grade)
			}
			grades = append(grades, decision.Grade)
		}
		vectors = append(vectors, grades)
	}
	for index := 1; index < len(vectors); index++ {
		if len(vectors[index]) != len(vectors[0]) {
			t.Fatalf("provider %s graded %d events, want %d", providers[index], len(vectors[index]), len(vectors[0]))
		}
		for position := range vectors[0] {
			if vectors[index][position] != vectors[0][position] {
				t.Fatalf("providers disagree at sequence position %d: %v vs %v", position, vectors[0], vectors[index])
			}
		}
	}
}

// TestBootstrapDenialsGradePerProvider verifies each provider grades only its
// own $TMPDIR/<provider>/ bootstrap artifacts as benign; the same path is
// FATAL for every other provider.
func TestBootstrapDenialsGradePerProvider(t *testing.T) {
	worktree := resolvedDir(t)
	controlRoot := resolvedDir(t)
	tempDir := resolvedDir(t)
	for _, provider := range []string{"opencode", "pi", "qwen"} {
		classifier := Classifier{Provider: provider, Worktree: worktree, ControlRoot: controlRoot, TempDir: tempDir}
		own := filepath.Join(tempDir, provider, "work-context.txt")
		if decision := classifier.Classify(Event{Tool: "read", Target: own}); decision.Grade != Benign {
			t.Fatalf("provider %s must grade its own bootstrap read benign: %+v", provider, decision)
		}
		for _, other := range []string{"opencode", "pi", "qwen"} {
			if other == provider {
				continue
			}
			foreign := filepath.Join(tempDir, other, "work-context.txt")
			if decision := classifier.Classify(Event{Tool: "read", Target: foreign}); decision.Grade != Fatal {
				t.Fatalf("provider %s must grade %s bootstrap reads fatal: %+v", provider, other, decision)
			}
		}
	}
}

// TestBenignSequenceNeverContainsWriteOrExecute guards the invariant that no
// write or execute denial can ever reach BENIGN, regardless of target.
func TestBenignSequenceNeverContainsWriteOrExecute(t *testing.T) {
	worktree := resolvedDir(t)
	classifier := Classifier{Provider: "opencode", Worktree: worktree, ControlRoot: resolvedDir(t), TempDir: resolvedDir(t)}
	for _, tool := range []string{"edit", "write", "write_file", "bash", "shell", "run_shell_command"} {
		for _, target := range []string{filepath.Join(worktree, "any.go"), filepath.Join(classifier.TempDir, "opencode", "work-context.txt"), ""} {
			if decision := classifier.Classify(Event{Tool: tool, Target: target}); decision.Grade != Fatal {
				t.Fatalf("tool %s target %q graded %s, want FATAL", tool, target, decision.Grade)
			}
		}
	}
}

func resolvedDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}
