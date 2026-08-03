package repository

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitIsIdempotentAndMarshalStateIsIgnored(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init", "-q")
	state, err := Discover(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Init(); err != nil {
		t.Fatal(err)
	}
	if err := state.Init(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state.StateRoot, "secret.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	status := runGit(t, repository, "status", "--short", "--untracked-files=all")
	if strings.Contains(status, ".marshal") {
		t.Fatalf("Marshal state leaked into Git status: %q", status)
	}
	exclude := runGit(t, repository, "check-ignore", "-v", ".marshal/secret.json")
	if !strings.Contains(exclude, "/.marshal/") {
		t.Fatalf("unexpected ignore result: %q", exclude)
	}
	runGit(t, repository, "add", "-A")
	if tracked := runGit(t, repository, "ls-files"); strings.Contains(tracked, ".marshal") {
		t.Fatalf("Marshal state entered commit tree: %q", tracked)
	}
}

func TestStateOverrideRejectsDifferentRepository(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	runGit(t, first, "init", "-q")
	runGit(t, second, "init", "-q")
	override := filepath.Join(t.TempDir(), "shared-state")
	t.Setenv("MARSHAL_STATE_DIR", override)
	firstState, err := Discover(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstState.Init(); err != nil {
		t.Fatal(err)
	}
	secondState, err := Discover(second)
	if err != nil {
		t.Fatal(err)
	}
	if err := secondState.Init(); err == nil {
		t.Fatal("shared writable state directory accepted for a different repository")
	}
}

func TestStateOverrideRejectsNonDefaultDirectoryInsideRepository(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init", "-q")
	t.Setenv("MARSHAL_STATE_DIR", filepath.Join(repository, "state"))
	if _, err := Discover(repository); err == nil {
		t.Fatal("non-default in-repository state directory accepted")
	}
}

func TestDiscoverAcceptsAbsoluteStateOverride(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init", "-q")
	override := filepath.Join(t.TempDir(), "state")
	t.Setenv("MARSHAL_STATE_DIR", override)
	state, err := Discover(repository)
	expectedParent, resolveErr := filepath.EvalSymlinks(filepath.Dir(override))
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	expected := filepath.Join(expectedParent, filepath.Base(override))
	if err != nil || state.StateRoot != expected {
		t.Fatalf("Discover = %+v, %v", state, err)
	}
	if err := state.Init(); err != nil {
		t.Fatal(err)
	}
	excludePath := filepath.Join(repository, ".git", "info", "exclude")
	data, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "/.marshal/") {
		t.Fatal("external state override changed repository-local ignore rules")
	}
}

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}
