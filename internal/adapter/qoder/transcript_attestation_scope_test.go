package qoder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
)

func TestAttestObservedToolPathAllowsCanaryReadsAndScopedWrite(t *testing.T) {
	worktree := canonicalTestWorktree(t)
	mustWriteAttestationFixture(t, worktree, "README.md")
	mustWriteAttestationFixture(t, worktree, "internal/adapter/qoder/transcript_attestation.go")
	mustWriteAttestationFixture(t, worktree, "report.md")
	task := attestationScopeTask("workspace-write", []string{"report.md"}, []string{".marshal/**"})
	declared := map[string]bool{"report.md": true}

	for _, call := range []struct {
		tool string
		key  string
		path string
	}{
		{tool: "read", key: "file_path", path: filepath.Join(worktree, "README.md")},
		{tool: "read", key: "file_path", path: "internal/adapter/qoder/transcript_attestation.go"},
		{tool: "write", key: "file_path", path: filepath.Join(worktree, "report.md")},
	} {
		if err := attestObservedToolPath(worktree, task, call.tool, attestationToolInput(t, call.key, call.path), declared); err != nil {
			t.Fatalf("%s %q rejected: %v", call.tool, call.path, err)
		}
	}
}

func TestAttestObservedSearchToolPathRequiresDisjointSearchDomain(t *testing.T) {
	worktree := canonicalTestWorktree(t)
	mustWriteAttestationFixture(t, worktree, "docs/public/input.md")
	mustWriteAttestationFixture(t, worktree, "docs/hidden/secret.txt")
	mustWriteAttestationFixture(t, worktree, "private/secret.txt")
	mustWriteAttestationFixture(t, worktree, ".marshal/state.json")
	task := attestationScopeTask("workspace-write", []string{"report.md"}, []string{"private/**", "docs/hidden/**"})

	for _, tool := range []string{"grep", "glob"} {
		t.Run(tool+"-non-deny-subtree", func(t *testing.T) {
			if err := attestObservedToolPath(worktree, task, tool, attestationToolInput(t, "path", "docs/public"), nil); err != nil {
				t.Fatalf("non-deny subtree rejected: %v", err)
			}
		})
		for _, tc := range []struct {
			name string
			path string
		}{
			{name: "worktree-root", path: worktree},
			{name: "deny-parent", path: "docs"},
			{name: "deny-ancestor", path: "private"},
			{name: "marshal-root", path: ".marshal"},
		} {
			t.Run(tool+"-"+tc.name, func(t *testing.T) {
				if err := attestObservedToolPath(worktree, task, tool, attestationToolInput(t, "path", tc.path), nil); err == nil || err.Error() != "tool-path-out-of-scope" {
					t.Fatalf("search err = %v, want tool-path-out-of-scope", err)
				}
			})
		}
	}
}

func TestAttestObservedSearchToolPathRejectsUnboundedOrInvalidDenyPattern(t *testing.T) {
	worktree := canonicalTestWorktree(t)
	mustWriteAttestationFixture(t, worktree, "docs/public/input.md")
	for _, pattern := range []string{"**/*.secret", "["} {
		t.Run(pattern, func(t *testing.T) {
			task := attestationScopeTask("workspace-write", []string{"report.md"}, []string{pattern})
			if err := attestObservedToolPath(worktree, task, "grep", attestationToolInput(t, "path", "docs/public"), nil); err == nil || err.Error() != "tool-path-out-of-scope" {
				t.Fatalf("search err = %v, want tool-path-out-of-scope", err)
			}
		})
	}
}

func TestAttestObservedToolPathReadOnlyReadsWorktreeButCannotWriteOutsideAllowPaths(t *testing.T) {
	worktree := canonicalTestWorktree(t)
	mustWriteAttestationFixture(t, worktree, "sources/repository/input.md")
	task := attestationScopeTask("read-only", []string{"report.md"}, nil)
	declared := map[string]bool{"sources/repository/input.md": true}

	if err := attestObservedToolPath(worktree, task, "read", attestationToolInput(t, "file_path", "sources/repository/input.md"), declared); err != nil {
		t.Fatalf("read-only worktree read rejected: %v", err)
	}
	if err := attestObservedToolPath(worktree, task, "write", attestationToolInput(t, "file_path", "sources/repository/input.md"), declared); err == nil || err.Error() != "tool-path-out-of-scope" {
		t.Fatalf("write to read-only source err = %v, want tool-path-out-of-scope", err)
	}
}

func TestAttestObservedToolPathRejectsDenyAndEscapes(t *testing.T) {
	worktree := canonicalTestWorktree(t)
	mustWriteAttestationFixture(t, worktree, "private/secret.txt")
	outside := t.TempDir()
	mustWriteAttestationFixture(t, outside, "secret.txt")
	if err := os.Symlink(outside, filepath.Join(worktree, "linked")); err != nil {
		t.Fatal(err)
	}
	task := attestationScopeTask("workspace-write", []string{"report.md"}, []string{"private/**"})

	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "deny", path: "private/secret.txt"},
		{name: "dot-dot", path: "../secret.txt"},
		{name: "absolute-outside", path: filepath.Join(outside, "secret.txt")},
		{name: "symlink-escape", path: "linked/secret.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := attestObservedToolPath(worktree, task, "read", attestationToolInput(t, "file_path", tc.path), nil); err == nil {
				t.Fatal("read unexpectedly passed")
			}
		})
	}
}

func attestationScopeTask(profile string, allowPaths, denyPaths []string) domain.TaskSpec {
	return domain.TaskSpec{
		Scope:  domain.TaskScope{AllowPaths: allowPaths, DenyPaths: denyPaths},
		Worker: domain.TaskWorker{ExecutionProfile: profile},
	}
}

func canonicalTestWorktree(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func mustWriteAttestationFixture(t *testing.T, root, relative string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func attestationToolInput(t *testing.T, key, path string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]string{key: path})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
