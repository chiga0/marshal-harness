package github

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

func testValidator(t *testing.T) *contract.Validator {
	t.Helper()
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	return validator
}

// TestBaseEnvironmentExcludesCredentialVariables covers T9: the merge
// execution environment mechanically excludes GH_TOKEN, GITHUB_TOKEN and
// every MARSHAL_GH_* variable, so a Worker/Verifier ambient credential can
// never reach the SCMMerger child process.
func TestBaseEnvironmentExcludesCredentialVariables(t *testing.T) {
	t.Setenv("GH_TOKEN", "gh-secret")
	t.Setenv("GITHUB_TOKEN", "github-secret")
	t.Setenv("MARSHAL_GH_PATH", "/secret/gh")
	t.Setenv("MARSHAL_GH_CONFIG_DIR", "/secret/config")
	t.Setenv("HOME", "/home/marshal")

	environment := baseEnvironment()
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		switch {
		case key == "GH_TOKEN", key == "GITHUB_TOKEN":
			t.Fatalf("credential variable %q leaked into the merge environment", key)
		case strings.HasPrefix(key, "MARSHAL_GH_"):
			t.Fatalf("credential variable %q leaked into the merge environment", key)
		}
	}
}

// TestNewMergerResolvesAndValidatesPaths covers the SCMMerger constructor:
// it must resolve real paths and fail closed on a missing executable, missing
// config dir or missing repository.
func TestNewMergerResolvesAndValidatesPaths(t *testing.T) {
	validator := testValidator(t)
	root := t.TempDir()
	ghPath := filepath.Join(root, "gh")
	if err := os.WriteFile(ghPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Join(root, "repo")
	if err := os.MkdirAll(repoRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	merger, err := NewMerger(ghPath, configDir, repoRoot, validator)
	if err != nil {
		t.Fatalf("NewMerger failed on valid inputs: %v", err)
	}
	if !merger.BindsExpectedHead() {
		t.Fatal("GitHub SCMMerger must report that it mechanically binds the expected head")
	}

	t.Run("nil validator rejected", func(t *testing.T) {
		if _, err := NewMerger(ghPath, configDir, repoRoot, nil); err == nil {
			t.Fatal("NewMerger accepted a nil validator")
		}
	})
	t.Run("relative gh path rejected", func(t *testing.T) {
		if _, err := NewMerger("relative/gh", configDir, repoRoot, validator); err == nil {
			t.Fatal("NewMerger accepted a relative gh path")
		}
	})
	t.Run("non-executable gh rejected", func(t *testing.T) {
		plain := filepath.Join(root, "plain")
		if err := os.WriteFile(plain, []byte("not executable"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := NewMerger(plain, configDir, repoRoot, validator); err == nil {
			t.Fatal("NewMerger accepted a non-executable gh path")
		}
	})
	t.Run("missing config dir rejected", func(t *testing.T) {
		if _, err := NewMerger(ghPath, filepath.Join(root, "missing"), repoRoot, validator); err == nil {
			t.Fatal("NewMerger accepted a missing config dir")
		}
	})
	t.Run("missing repository rejected", func(t *testing.T) {
		if _, err := NewMerger(ghPath, configDir, filepath.Join(root, "missing-repo"), validator); err == nil {
			t.Fatal("NewMerger accepted a missing repository")
		}
	})
}

func TestMergeMethodFlagClosedEnumeration(t *testing.T) {
	for _, test := range []struct {
		method string
		flag   string
	}{
		{domain.MergeMethodMerge, "merge"},
		{domain.MergeMethodSquash, "squash"},
		{domain.MergeMethodRebase, "rebase"},
	} {
		flag, err := mergeMethodFlag(test.method)
		if err != nil || flag != test.flag {
			t.Fatalf("mergeMethodFlag(%q) = %q, %v", test.method, flag, err)
		}
	}
	if _, err := mergeMethodFlag("force"); err == nil {
		t.Fatal("mergeMethodFlag accepted a value outside the closed enumeration")
	}
}

func TestCredentialIdentityDigestDeterministicAndSecretFree(t *testing.T) {
	first := credentialIdentityDigest("/gh/bin", "/gh/config", "github-login:alice")
	second := credentialIdentityDigest("/gh/bin", "/gh/config", "github-login:alice")
	if first == "" || first != second {
		t.Fatalf("credential identity digest is not deterministic: %q vs %q", first, second)
	}
	if !strings.HasPrefix(first, "sha256:") || len(first) != len("sha256:")+64 {
		t.Fatalf("credential identity digest is not a canonical sha256 digest: %q", first)
	}
	if first == credentialIdentityDigest("/gh/bin", "/gh/config", "github-login:bob") {
		t.Fatal("credential identity digest did not change when the principal changed")
	}
	if first == credentialIdentityDigest("/other/bin", "/gh/config", "github-login:alice") {
		t.Fatal("credential identity digest did not change when the gh path changed")
	}
}

func TestMergeMarkerDeterministic(t *testing.T) {
	marker := mergeMarker("TASK", "run:1")
	if marker != "<!-- marshal task=TASK run=run:1 -->" {
		t.Fatalf("mergeMarker = %q", marker)
	}
	if marker != mergeMarker("TASK", "run:1") {
		t.Fatal("mergeMarker is not deterministic")
	}
}

func TestValidateMergeTargetIntent(t *testing.T) {
	if err := validateMergeTargetIntent(domain.SCMMergeIntent{RepositoryRef: "org/repo", PRNumber: 7}); err != nil {
		t.Fatalf("bound target intent rejected: %v", err)
	}
	if err := validateMergeTargetIntent(domain.SCMMergeIntent{RepositoryRef: "", PRNumber: 7}); err == nil {
		t.Fatal("empty repository accepted")
	}
	if err := validateMergeTargetIntent(domain.SCMMergeIntent{RepositoryRef: "org/repo", PRNumber: 0}); err == nil {
		t.Fatal("zero PR number accepted")
	}
}

func TestClassifyMergeErrorTypedAndRedacted(t *testing.T) {
	for _, test := range []struct {
		name       string
		diagnostic string
		want       error
	}{
		{name: "permission", diagnostic: "HTTP 403: Resource not accessible by integration SECRET", want: port.ErrMergePermissionDenied},
		{name: "not mergeable", diagnostic: "HTTP 422: Pull Request is not mergeable SECRET", want: port.ErrMergeNotMergeable},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := classifyMergeError(&ghCommandError{operation: "api", cause: fmt.Errorf("exit status 1"), output: test.diagnostic})
			if !port.IsPermanent(err) || !errors.Is(err, test.want) {
				t.Fatalf("classification = %v, want permanent %v", err, test.want)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("provider diagnostic leaked through typed error: %v", err)
			}
		})
	}
	transient := &ghCommandError{operation: "api", cause: fmt.Errorf("exit status 1"), output: "temporary upstream reset"}
	if got := classifyMergeError(transient); got != transient || port.IsPermanent(got) {
		t.Fatalf("transient classification = %v", got)
	}
}
