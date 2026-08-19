package darwin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLauncherPolicyRequiresExactIdentity(t *testing.T) {
	identity := ExecutableIdentity{SHA256: "sha", TeamID: "team", CDHash: "cdhash", Identifier: "launcher"}
	policy := LauncherPolicy{SHA256: "sha", TeamID: "team", CDHash: "cdhash", Identifier: "launcher"}
	if err := policy.validate(identity); err != nil {
		t.Fatalf("matching policy rejected: %v", err)
	}
	for name, mutate := range map[string]func(*LauncherPolicy){
		"missing digest":   func(p *LauncherPolicy) { p.SHA256 = "" },
		"wrong team":       func(p *LauncherPolicy) { p.TeamID = "other" },
		"wrong cdhash":     func(p *LauncherPolicy) { p.CDHash = "other" },
		"wrong identifier": func(p *LauncherPolicy) { p.Identifier = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := policy
			mutate(&candidate)
			if err := candidate.validate(identity); err == nil {
				t.Fatal("mismatched policy was accepted")
			}
		})
	}
}

func TestLauncherPolicyDoesNotAuthorizeObservationAlone(t *testing.T) {
	identity := ExecutableIdentity{SHA256: "sha", TeamID: "team", CDHash: "cdhash", Identifier: "launcher"}
	if err := (LauncherPolicy{}).validate(identity); err == nil {
		t.Fatal("empty authority policy was accepted")
	}
}

func TestHeldExecutableDuplicateKeepsOriginalHeld(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "candidate")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Chmod(0o500); err != nil {
		t.Fatal(err)
	}
	held := &HeldExecutable{file: file}
	duplicate, err := held.Duplicate()
	if err != nil {
		t.Fatalf("Duplicate() error = %v", err)
	}
	defer duplicate.Close()
	originalInfo, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	duplicateInfo, err := duplicate.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(originalInfo, duplicateInfo) {
		t.Fatal("duplicate descriptor does not reference the held inode")
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := duplicate.Stat(); err != nil {
		t.Fatalf("transport duplicate became unusable after owner close: %v", err)
	}
}

func TestOpenHeldCandidateRejectsSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	_, err := OpenHeldCandidate(filepath.Join(link, "candidate"), ExecutablePolicy{
		SHA256: "sha", TeamID: "team", CDHash: "cdhash", Identifier: "candidate",
	})
	if err == nil {
		t.Fatal("symlinked parent was accepted")
	}
}
