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

func TestInitRepairsExistingStateRootToOwnerOnly(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init", "-q")
	stateRoot := filepath.Join(repository, ".marshal")
	if err := os.Mkdir(stateRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(stateRoot, "operator-evidence")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := Discover(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Init(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("state root mode=%#o", info.Mode().Perm())
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "preserve" {
		t.Fatalf("existing state changed: data=%q err=%v", data, err)
	}
}

func TestInitRejectsSymlinkStateRoot(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init", "-q")
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(repository, ".marshal")); err != nil {
		t.Fatal(err)
	}
	state, err := Discover(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Init(); err == nil {
		t.Fatal("symlink state root admitted")
	}
	if _, err := os.Stat(filepath.Join(target, "repo.json")); !os.IsNotExist(err) {
		t.Fatalf("symlink target mutated: %v", err)
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
