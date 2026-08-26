package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/app"
	"github.com/chiga0/marshal-harness/internal/buildinfo"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
)

func TestMain(m *testing.M) {
	localDogfoodGateTestBypass = func(info buildinfo.Info) bool {
		return info.Commit == "unknown" && info.SelfProfile == "unprofiled"
	}
	os.Exit(m.Run())
}

func TestDarwinLocalDogfoodProductionEntry(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin profile gate")
	}
	originalBuild := localBuildInfo
	originalNow := localNow
	originalRuntime := newWorkerRuntime
	t.Cleanup(func() {
		localBuildInfo = originalBuild
		localNow = originalNow
		newWorkerRuntime = originalRuntime
	})

	const sourceHead = "89abcdef0123456789abcdef0123456789abcdef"
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	localBuildInfo = func() buildinfo.Info {
		return buildinfo.Info{Version: "dev", Commit: sourceHead, SelfProfile: selfidentity.LocalProfile,
			OS: runtime.GOOS, Arch: runtime.GOARCH}
	}
	localNow = func() time.Time { return now }

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })

	newWorkerRuntime = func(func(string) string) (*app.WorkerRuntime, error) {
		t.Fatal("doctor --self reached Worker Runtime discovery")
		return nil, nil
	}
	var activationOutput, stderr bytes.Buffer
	exit := RunContext(context.Background(), []string{
		"doctor", "--self", "--repository-root", root, "--activation-id", "local-production-entry",
		"--issued-at", now.Format(time.RFC3339), "--valid-until", now.Add(time.Hour).Format(time.RFC3339),
	}, strings.NewReader(""), &activationOutput, &stderr)
	if exit != ExitOK {
		t.Fatalf("doctor --self exit=%d stderr=%s", exit, stderr.String())
	}
	activationRaw := activationOutput.Bytes()
	canonicalRaw, err := canonical.JSON(activationRaw)
	if err != nil || !bytes.Equal(canonicalRaw, activationRaw) || bytes.HasSuffix(activationRaw, []byte("\n")) {
		t.Fatalf("doctor --self did not emit exact canonical activation: err=%v", err)
	}
	var bypassOutput, bypassError bytes.Buffer
	exit = RunContext(context.Background(), []string{"doctor", "--self", "--self=false", "--json"},
		strings.NewReader(""), &bypassOutput, &bypassError)
	if exit != ExitUsage || bypassOutput.Len() != 0 || !strings.Contains(bypassError.String(), "布尔参数不得重复") {
		t.Fatalf("conflicting --self exit=%d stdout=%q stderr=%q", exit, bypassOutput.String(), bypassError.String())
	}
	for _, test := range []struct {
		name   string
		args   []string
		reason string
	}{
		{"self false", []string{"doctor", "--self=false", "--json"}, selfidentity.ReasonOptInMissing},
		{"self with run", []string{"doctor", "--self", "--run", "local-run", "--json"}, selfidentity.ReasonOptInMissing},
		{"self with print env", []string{"doctor", "--self", "--print-env"}, selfidentity.ReasonOptInMissing},
		{"self with repair", []string{"doctor", "--self", "--run", "local-run", "--repair", "--json"}, selfidentity.ReasonCommandDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output, denied bytes.Buffer
			got := RunContext(context.Background(), test.args, strings.NewReader(""), &output, &denied)
			if got != ExitUnavailable || output.Len() != 0 || !strings.Contains(denied.String(), test.reason) {
				t.Fatalf("exit=%d stdout=%q stderr=%q, want reason %q", got, output.String(), denied.String(), test.reason)
			}
		})
	}
	activationPath := filepath.Join(root, "activation.json")
	if err := os.WriteFile(activationPath, activationRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(selfidentity.ActivationEnv, activationPath)

	draftRaw, err := os.ReadFile(filepath.Join(originalDirectory, "..", "..", "schemas", "examples", "happy-path", "task-spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	var draft map[string]any
	if err := json.Unmarshal(draftRaw, &draft); err != nil {
		t.Fatal(err)
	}
	worker := draft["worker"].(map[string]any)
	delete(worker, "preferredAdapter")
	delete(worker, "fallbackAdapters")
	draftRaw, err = json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	draftPath := filepath.Join(root, "draft.json")
	if err := os.WriteFile(draftPath, draftRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	var taskOutput bytes.Buffer
	stderr.Reset()
	exit = RunContext(context.Background(), []string{"task", "scaffold", "--draft", draftPath},
		strings.NewReader(""), &taskOutput, &stderr)
	if exit != ExitOK {
		t.Fatalf("gated task scaffold exit=%d stderr=%s", exit, stderr.String())
	}
	if !json.Valid(taskOutput.Bytes()) {
		t.Fatalf("task scaffold output is not JSON: %s", taskOutput.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".marshal")); !os.IsNotExist(err) {
		t.Fatalf("unexpected state root before denied repair: %v", err)
	}
	var repairOutput, repairError bytes.Buffer
	exit = RunContext(context.Background(), []string{"doctor", "--run", "local-run", "--repair", "--json"},
		strings.NewReader(""), &repairOutput, &repairError)
	if exit != ExitUnavailable || repairOutput.Len() != 0 || !strings.Contains(repairError.String(), selfidentity.ReasonCommandDenied) {
		t.Fatalf("denied repair exit=%d stdout=%q stderr=%q", exit, repairOutput.String(), repairError.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".marshal")); !os.IsNotExist(err) {
		t.Fatalf("denied repair changed state root: %v", err)
	}

	for _, test := range []struct {
		name   string
		args   []string
		reason string
	}{
		{"publication", []string{"task", "publish", "--run", "run-1"}, selfidentity.ReasonPublicationDenied},
		{"remote", []string{"serve"}, selfidentity.ReasonRemoteSurfaceDenied},
		{"internal", []string{"internal", "plan-premortem-check"}, selfidentity.ReasonCredentialedEffectDenied},
		{"unlineaged lifecycle", []string{"task", "plan"}, selfidentity.ReasonCommandDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, denied bytes.Buffer
			if got := RunContext(context.Background(), test.args, strings.NewReader(""), &stdout, &denied); got != ExitUnavailable {
				t.Fatalf("exit=%d, want %d", got, ExitUnavailable)
			}
			if !strings.Contains(denied.String(), test.reason) {
				t.Fatalf("stderr=%q, want reason %q", denied.String(), test.reason)
			}
		})
	}

	t.Setenv(selfidentity.ActivationEnv, filepath.Join(root, "missing.json"))
	var stdout, missing bytes.Buffer
	if got := RunContext(context.Background(), []string{"task", "scaffold", "--draft", draftPath}, strings.NewReader(""), &stdout, &missing); got != ExitUnavailable ||
		!strings.Contains(missing.String(), selfidentity.ReasonOptInMissing) {
		t.Fatalf("missing activation exit=%d stderr=%q", got, missing.String())
	}
}

func TestDarwinUnprofiledBuildFailsClosedAtProductionEntry(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin profile gate")
	}
	original := localBuildInfo
	t.Cleanup(func() { localBuildInfo = original })
	localBuildInfo = func() buildinfo.Info {
		return buildinfo.Info{Commit: strings.Repeat("a", 40), SelfProfile: "unprofiled"}
	}
	var stdout, stderr bytes.Buffer
	exit := RunContext(context.Background(), []string{"task", "scaffold", "--draft", "ignored"}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitUnavailable || !strings.Contains(stderr.String(), selfidentity.ReasonProfileMismatch) {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
}

func TestVersionJSONReportsExplicitDefaultSelfProfile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := runVersion([]string{"--json"}, &stdout, &stderr); exit != ExitOK {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	var info buildinfo.Info
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.SelfProfile != "unprofiled" {
		t.Fatalf("selfProfile=%q, want unprofiled", info.SelfProfile)
	}
}
