package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecutePathUsesExactLaunchAndDeletesEnvelope(t *testing.T) {
	if os.Getenv("MARSHAL_LAUNCH_HELPER") == "1" {
		t.Fatal("helper marker leaked into parent test")
	}
	fixture := newFixture(t)
	observed := filepath.Join(t.TempDir(), "observed.txt")
	script := `#!/bin/sh
{
  printf 'cwd=%s\n' "$PWD"
  printf 'arg=%s\n' "$1"
  printf 'safe=%s\n' "$SAFE_VALUE"
  printf 'ambient=%s\n' "${AMBIENT_SECRET-unset}"
} > "$OBSERVED_PATH"
`
	if err := os.WriteFile(fixture.executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	fixture.request.Now = now
	fixture.request.ExpiresAt = now.Add(time.Minute)
	fixture.request.Arguments = []string{"expected-argument"}
	fixture.request.Environment = []string{"SAFE_VALUE=expected-value", "OBSERVED_PATH=" + observed}
	reference, err := Seal(fixture.stateRoot, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestExecutePathHelper$", "--", reference.Path)
	command.Env = append(os.Environ(), "MARSHAL_LAUNCH_HELPER=1", "AMBIENT_SECRET=must-not-leak")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v: %s", err, output)
	}
	data, err := os.ReadFile(observed)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"cwd=" + fixture.worktree, "arg=expected-argument", "safe=expected-value", "ambient=unset"} {
		if !strings.Contains(string(data), expected+"\n") {
			t.Fatalf("observed launch missing %q: %s", expected, data)
		}
	}
	if _, err := os.Lstat(reference.Path); !os.IsNotExist(err) {
		t.Fatalf("envelope was not removed before exec: %v", err)
	}
}

func TestExecutePathHelper(t *testing.T) {
	if os.Getenv("MARSHAL_LAUNCH_HELPER") != "1" {
		return
	}
	if len(os.Args) < 2 {
		t.Fatal("missing envelope path")
	}
	if err := ExecutePath(os.Args[len(os.Args)-1], time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}
