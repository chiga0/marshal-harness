package verification

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

const unformattedGoSource = "package pkg\n" +
	"\n" +
	"func add(a int,b int) int{\n" +
	"\treturn a+b\n" +
	"}\n"

const formattedGoSource = "package pkg\n" +
	"\n" +
	"func add(a int, b int) int {\n" +
	"\treturn a + b\n" +
	"}\n"

func writeFixtureGoFile(t *testing.T, worktree, relative, content string) string {
	t.Helper()
	path := filepath.Join(worktree, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNormalizeFormatRewritesNonCompliantGoFile(t *testing.T) {
	worktree := t.TempDir()
	target := writeFixtureGoFile(t, worktree, "pkg/code.go", unformattedGoSource)
	normalized, err := normalizeFormat(context.Background(), worktree, []string{"pkg/code.go"}, []string{"pkg/**"})
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized) != 1 || normalized[0] != "pkg/code.go" {
		t.Fatalf("normalized = %v", normalized)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != formattedGoSource {
		t.Fatalf("normalized bytes = %q", data)
	}
}

func TestNormalizeFormatLeavesCompliantFileUntouched(t *testing.T) {
	worktree := t.TempDir()
	target := writeFixtureGoFile(t, worktree, "pkg/code.go", formattedGoSource)
	normalized, err := normalizeFormat(context.Background(), worktree, []string{"pkg/code.go"}, []string{"pkg/**"})
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized) != 0 {
		t.Fatalf("normalized = %v", normalized)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != formattedGoSource {
		t.Fatalf("compliant file changed: %q", data)
	}
}

func TestNormalizeFormatSkipsFilesOutsideAllowPaths(t *testing.T) {
	worktree := t.TempDir()
	target := writeFixtureGoFile(t, worktree, "vendor/code.go", unformattedGoSource)
	normalized, err := normalizeFormat(context.Background(), worktree, []string{"vendor/code.go"}, []string{"pkg/**"})
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized) != 0 {
		t.Fatalf("normalized = %v", normalized)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != unformattedGoSource {
		t.Fatalf("out-of-scope file changed: %q", data)
	}
}

func TestNormalizeFormatIgnoresNonGoAndMissingFiles(t *testing.T) {
	worktree := t.TempDir()
	target := writeFixtureGoFile(t, worktree, "notes.txt", unformattedGoSource)
	normalized, err := normalizeFormat(context.Background(), worktree, []string{"notes.txt", "pkg/removed.go"}, []string{"**"})
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized) != 0 {
		t.Fatalf("normalized = %v", normalized)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != unformattedGoSource {
		t.Fatalf("non-go file changed: %q", data)
	}
}

func TestNormalizeFormatReturnsDeterministicOrder(t *testing.T) {
	worktree := t.TempDir()
	writeFixtureGoFile(t, worktree, "pkg/b.go", unformattedGoSource)
	writeFixtureGoFile(t, worktree, "pkg/a.go", unformattedGoSource)
	normalized, err := normalizeFormat(context.Background(), worktree, []string{"pkg/b.go", "pkg/a.go"}, []string{"pkg/**"})
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized) != 2 || normalized[0] != "pkg/a.go" || normalized[1] != "pkg/b.go" {
		t.Fatalf("normalized = %v", normalized)
	}
}

func TestNormalizeFormatFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, worktree string) []string
	}{
		{
			name: "unparseable go source",
			prepare: func(t *testing.T, worktree string) []string {
				writeFixtureGoFile(t, worktree, "pkg/broken.go", "package pkg\n\nfunc { oops\n")
				return []string{"pkg/broken.go"}
			},
		},
		{
			name: "gofmt missing from filtered PATH",
			prepare: func(t *testing.T, worktree string) []string {
				t.Setenv("PATH", t.TempDir())
				writeFixtureGoFile(t, worktree, "pkg/code.go", unformattedGoSource)
				return []string{"pkg/code.go"}
			},
		},
		{
			name: "gofmt present but not executable",
			prepare: func(t *testing.T, worktree string) []string {
				directory := t.TempDir()
				if err := os.WriteFile(filepath.Join(directory, "gofmt"), []byte("#!/bin/sh\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", directory)
				writeFixtureGoFile(t, worktree, "pkg/code.go", unformattedGoSource)
				return []string{"pkg/code.go"}
			},
		},
		{
			name: "absolute changed path rejected",
			prepare: func(t *testing.T, worktree string) []string {
				return []string{filepath.Join(worktree, "pkg", "code.go")}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			worktree := t.TempDir()
			changedPaths := tc.prepare(t, worktree)
			if _, err := normalizeFormat(context.Background(), worktree, changedPaths, []string{"**"}); err == nil {
				t.Fatalf("normalizeFormat accepted case %q", tc.name)
			}
		})
	}
}

func TestFormatNormalizeGateEvidenceListsNormalizedFiles(t *testing.T) {
	gate := formatNormalizeGate([]string{"pkg/a.go", "pkg/b.go"}, "")
	if gate.ID != "format:normalize" || gate.Category != "other" || gate.Status != "pass" || gate.Required {
		t.Fatalf("gate = %+v", gate)
	}
	if gate.Summary != "gofmt 归一化 2 个文件" {
		t.Fatalf("summary = %q", gate.Summary)
	}
	if len(gate.Evidence) != 2 || gate.Evidence[0] != "normalized:pkg/a.go" || gate.Evidence[1] != "normalized:pkg/b.go" {
		t.Fatalf("evidence = %+v", gate.Evidence)
	}
	empty := formatNormalizeGate(nil, "")
	if empty.ID != "format:normalize" || empty.Status != "pass" || empty.Required || len(empty.Evidence) != 0 {
		t.Fatalf("empty gate = %+v", empty)
	}
	if empty.Summary != "无需 gofmt 归一化：所有变更 .go 文件均已合规" {
		t.Fatalf("empty summary = %q", empty.Summary)
	}
}

func TestFormatNormalizeGateEvidenceCarriesCandidateReference(t *testing.T) {
	candidateDigest := candidateFixtureDigest("c")
	gate := formatNormalizeGate([]string{"pkg/a.go"}, candidateDigest)
	if len(gate.Evidence) != 2 || gate.Evidence[0] != "normalized:pkg/a.go" || gate.Evidence[1] != "candidate:"+candidateDigest {
		t.Fatalf("evidence = %+v", gate.Evidence)
	}
	// A no-op normalization still carries the head Candidate (the worker
	// Candidate in that case), keeping the gate evidence auditable.
	noop := formatNormalizeGate(nil, candidateDigest)
	if len(noop.Evidence) != 1 || noop.Evidence[0] != "candidate:"+candidateDigest {
		t.Fatalf("no-op evidence = %+v", noop.Evidence)
	}
	if noop.Status != "pass" || noop.Required {
		t.Fatalf("no-op gate = %+v", noop)
	}
}
