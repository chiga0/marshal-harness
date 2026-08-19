package denials

import (
	"os"
	"path/filepath"
	"strings"
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
	providers := []string{"opencode", "pi", "qwen", "qoder"}
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

// TestCrossAdapterAllowlistConformance is the shared tool-allowlist
// reconciliation suite: the same compliant/violating success sequences must
// produce bit-identical verdicts under every adapter's collection
// vocabulary. Collection contributes only successful (non-denial) tool
// calls; denial events never enter the reconciliation input. Normalization
// through the frozen tool tables is what makes the verdicts comparable
// across opencode, pi and qwen vocabularies.
func TestCrossAdapterAllowlistConformance(t *testing.T) {
	declared := []string{"read", "edit", "write"}
	// providerVocabularies maps one abstract tool call to each adapter's raw
	// transcript tool name.
	providerVocabularies := map[string]map[string]string{
		"opencode": {"read": "read", "edit": "edit", "write": "write", "grep": "grep", "bash": "bash"},
		"pi":       {"read": "read", "edit": "edit", "write": "write", "grep": "grep", "bash": "bash"},
		"qwen":     {"read": "read_file", "edit": "edit", "write": "write_file", "grep": "grep", "bash": "shell"},
		"qoder":    {"read": "read", "edit": "edit", "write": "write", "grep": "grep", "bash": "bash"},
	}
	sequence := []struct {
		tool   string
		denied bool
	}{
		{tool: "read"},
		{tool: "edit"},
		{tool: "write"},
		{tool: "grep", denied: true}, // denied probe never counts as success
	}
	violationSequence := []struct {
		tool   string
		denied bool
	}{
		{tool: "read"},
		{tool: "grep"}, // successful undeclared call
	}
	executeSequence := []struct {
		tool   string
		denied bool
	}{
		{tool: "bash"}, // successful undeclared execute-class call
	}
	reconcile := func(provider string, steps []struct {
		tool   string
		denied bool
	}) ([]string, []string) {
		vocabulary := providerVocabularies[provider]
		var collected []string
		for _, step := range steps {
			if step.denied {
				continue
			}
			collected = append(collected, vocabulary[step.tool])
		}
		return SortedToolNames(collected), AllowlistViolations(collected, declared)
	}
	providers := []string{"opencode", "pi", "qwen", "qoder"}
	for _, test := range []struct {
		name  string
		steps []struct {
			tool   string
			denied bool
		}
		wantViolations []string
	}{
		{name: "compliant", steps: sequence, wantViolations: []string{}},
		{name: "undeclared-grep", steps: violationSequence, wantViolations: []string{"grep"}},
		{name: "undeclared-execute", steps: executeSequence, wantViolations: []string{"bash"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var firstProvider string
			var firstNames, firstViolations []string
			for _, provider := range providers {
				names, violations := reconcile(provider, test.steps)
				if len(violations) != len(test.wantViolations) {
					t.Fatalf("provider %s violations = %v, want %v", provider, violations, test.wantViolations)
				}
				for index := range test.wantViolations {
					if violations[index] != test.wantViolations[index] {
						t.Fatalf("provider %s violations = %v, want %v", provider, violations, test.wantViolations)
					}
				}
				if firstProvider == "" {
					firstProvider, firstNames, firstViolations = provider, names, violations
					continue
				}
				// Bit-identical verdicts across adapters: the normalized
				// toolNames list and the violation list must match exactly.
				if strings.Join(names, "\x00") != strings.Join(firstNames, "\x00") || strings.Join(violations, "\x00") != strings.Join(firstViolations, "\x00") {
					t.Fatalf("providers %s and %s disagree: names %v vs %v, violations %v vs %v", firstProvider, provider, firstNames, names, firstViolations, violations)
				}
			}
		})
	}
}

// TestAllowlistNormalizationStaysInsideVocabularyOrOriginal pins the
// normalization contract: vocabulary words are identity, table members map
// to their class representative, and off-table names survive unchanged so
// reconciliation treats them as undeclared.
func TestAllowlistNormalizationStaysInsideVocabularyOrOriginal(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"read", "read"}, {"edit", "edit"}, {"write", "write"}, {"grep", "grep"}, {"find", "find"}, {"ls", "ls"}, {"bash", "bash"},
		{"read_file", "read"}, {"glob", "read"}, {"list_directory", "read"}, {"lsp", "read"},
		{"write_file", "write"}, {"apply_patch", "write"}, {"replace", "write"},
		{"shell", "bash"}, {"run_shell_command", "bash"},
		{"mystery_tool", "mystery_tool"},
	} {
		if got := NormalizeToolName(test.input); got != test.want {
			t.Fatalf("NormalizeToolName(%q) = %q, want %q", test.input, got, test.want)
		}
	}
	if violations := AllowlistViolations([]string{"read_file", "mystery_tool"}, []string{"read"}); strings.Join(violations, ",") != "mystery_tool" {
		t.Fatalf("violations = %v, want exactly [mystery_tool]", violations)
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
