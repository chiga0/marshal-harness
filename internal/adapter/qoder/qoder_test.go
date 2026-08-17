package qoder

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

func TestNewRequiresExactExecutableAndValidator(t *testing.T) {
	validator := newValidator(t)
	if _, err := New("qodercli", validator); err == nil {
		t.Fatal("relative executable accepted")
	}
	executable := fakeExecutable(t, supportedBinary, "exit 0")
	if _, err := New(executable, nil); err == nil {
		t.Fatal("nil validator accepted")
	}
	nonExecutable := filepath.Join(t.TempDir(), "qodercli")
	if err := os.WriteFile(nonExecutable, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(nonExecutable, validator); err == nil {
		t.Fatal("non-executable file accepted")
	}
}

func TestProbeIsFailClosedUntilConformance(t *testing.T) {
	for _, version := range []string{supportedBinary, "1.1.24", "1.1.22", "9.9.9"} {
		t.Run(version, func(t *testing.T) {
			adapter, err := New(fakeExecutable(t, version, "exit 0"), newValidator(t))
			if err != nil {
				t.Fatal(err)
			}
			record, err := adapter.Probe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]any
			if err := json.Unmarshal(record.Data, &raw); err != nil {
				t.Fatal(err)
			}
			status, _ := raw["probeStatus"].(string)
			binaryVersion, _ := raw["binaryVersion"].(string)
			digest, _ := raw["executableDigest"].(string)
			executable, _ := raw["executable"].(string)
			if status != "unsupported" {
				t.Fatalf("probeStatus = %q, want unsupported (fail-closed until conformance)", status)
			}
			if binaryVersion != version || !strings.HasPrefix(digest, "sha256:") || !filepath.IsAbs(executable) {
				t.Fatalf("snapshot = %s/%s/%s", binaryVersion, digest, executable)
			}
			probeErrors, _ := raw["probeErrors"].([]any)
			if len(probeErrors) == 0 {
				t.Fatal("probeErrors must never be empty while conformance is pending")
			}
			joined := ""
			for _, item := range probeErrors {
				joined += item.(string) + "\n"
			}
			if !strings.Contains(joined, conformancePendingReason) {
				t.Fatalf("probeErrors must carry the conformance-pending reason: %v", probeErrors)
			}
			if !isSupportedBinaryVersion(version) {
				found := false
				for _, item := range probeErrors {
					message := item.(string)
					if strings.Contains(message, version) && strings.Contains(message, supportedBinaryRange) {
						found = true
					}
				}
				if !found {
					t.Fatalf("probeErrors must report the actual and supported version: %v", probeErrors)
				}
			}
		})
	}
}

func TestRunRequiresBoundConformanceIdentityBeforeLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, supportedBinary, "touch "+shellQuote(marker)+"\n"+successEvents("provider/model"))
	fixture.adapter.mu.Lock()
	fixture.adapter.conformance = nil
	fixture.adapter.mu.Unlock()
	_, err := fixture.adapter.Run(context.Background(), fixture.request)
	if !errors.Is(err, ErrConformancePending) || !port.IsPermanent(err) {
		t.Fatalf("error = %v, want permanent conformance-pending", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("worker launched without a bound conformance identity")
	}
}

func TestBindConformanceRequiresIndependentExactReceipt(t *testing.T) {
	fixture := newRunFixture(t, supportedBinary, successEvents("provider/model"))
	fixture.adapter.mu.Lock()
	fixture.adapter.conformance = nil
	fixture.adapter.mu.Unlock()
	record, err := fixture.adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(record.Data, &snapshot); err != nil {
		t.Fatal(err)
	}
	// A caller can rewrite Probe JSON, but BindConformance accepts no
	// CapabilitySnapshot and therefore cannot authorize that self-claim.
	snapshot["probeStatus"], snapshot["probeErrors"] = "supported", []string{}
	record.Data, err = json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(record.Data), conformanceEventContract) {
		t.Fatal("Probe unexpectedly contains an independent conformance receipt")
	}
	identity, err := fixture.adapter.inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	receipt := validConformanceReceipt(identity)
	if err := fixture.adapter.BindConformance(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	probe, err := fixture.adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(probe.Data), `"probeStatus":"supported"`) {
		t.Fatalf("probe did not reflect exact bound conformance: %s", probe.Data)
	}
	receipt.ExecutableDigest = digest("f")
	if err := fixture.adapter.BindConformance(context.Background(), receipt); !errors.Is(err, ErrIdentityDrift) {
		t.Fatalf("stale conformance identity accepted: %v", err)
	}
	receipt = validConformanceReceipt(identity)
	receipt.CapabilitiesDigest = digest("e")
	if err := fixture.adapter.BindConformance(context.Background(), receipt); err == nil {
		t.Fatal("receipt with different capabilities was accepted")
	}
}

func validConformanceReceipt(identity executableIdentity) ConformanceReceipt {
	return ConformanceReceipt{
		RunnerID: "marshal-conformance", RunnerVersion: "1", EvidenceDigest: digest("a"), ObservedAt: classifyNow,
		AdapterVersion: adapterVersion, Executable: identity.path, ExecutableDigest: identity.digest,
		BinaryVersion: identity.version, CapabilitiesDigest: expectedCapabilitiesDigest(),
		ProbeErrors: []string{}, CredentialVerified: true, LiveProtocolVerified: true, EventContract: conformanceEventContract,
	}
}

func TestSupportedBinaryVersionAllowsCompatiblePatchOnly(t *testing.T) {
	for _, version := range []string{"1.1.23", "1.1.24", "1.1.999"} {
		if !isSupportedBinaryVersion(version) {
			t.Fatalf("compatible patch %s rejected", version)
		}
	}
	for _, version := range []string{"1.1.22", "1.0.99", "1.2.0", "2.1.23", "malformed"} {
		if isSupportedBinaryVersion(version) {
			t.Fatalf("incompatible version %s accepted", version)
		}
	}
}

func TestParseQoderVersionNormalizesBareOutput(t *testing.T) {
	for _, test := range []struct {
		name    string
		output  string
		version string
	}{
		{"real", "1.1.23\n", supportedBinary},
		{"trailing-newline", "1.1.23\n", supportedBinary},
		{"extra-whitespace", "  1.1.23  \n", supportedBinary},
		{"unsupported-patch", "1.1.24\n", "1.1.24"},
		{"unsupported-minor", "1.2.0\n", "1.2.0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			version, err := parseQoderVersion(test.output)
			if err != nil {
				t.Fatal(err)
			}
			if version != test.version {
				t.Fatalf("version = %q, want %q", version, test.version)
			}
		})
	}
}

func TestParseQoderVersionRejectsMalformedOutput(t *testing.T) {
	for _, input := range []string{
		"",
		"\n",
		"1.1\n",
		"1.1.23.0\n",
		"01.1.23\n",
		"1.1.23-rc1\n",
		"1.1.23+build\n",
		"1.1.23 extra\n",
		"qodercli 1.1.23\n",
		"qodercli\n",
		"qoder 1.1.23\n",
		"v1.1.23\n",
		"not-a-version\n",
	} {
		if _, err := parseQoderVersion(input); err == nil {
			t.Fatalf("input %q did not produce an error", input)
		}
	}
}

func TestBuildArgsFreezesRealNonInteractiveArgv(t *testing.T) {
	args := buildArgs("provider/model", "/managed/config", "/worktree", false)
	want := []string{"--print", "--output-format", "stream-json", "--permission-mode", "accept_edits", "--no-session-persistence", "--config-dir", "/managed/config", "--setting-sources", "", "--cwd", "/worktree", "--model", "provider/model"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v", args)
	}
}

func TestBuildArgsRejectsFabricatedRunSandboxArgv(t *testing.T) {
	args := buildArgs("", "/isolated/config", "/worktree", false)
	joined := strings.Join(args, "\x00")
	// The previous `run --json --non-interactive --sandbox workspace-write`
	// construct does not exist in the real help and must never reappear.
	// workspace-write is not a legal permission mode, and bypass_permissions
	// is forbidden because it removes the provider permission gate.
	for _, forbidden := range []string{"run", "--json", "--non-interactive", "--sandbox", "qodercli", "workspace-write", "bypass_permissions"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("argv leaked fabricated flag %q: %#v", forbidden, args)
		}
	}
	for _, want := range []string{"--print", "--output-format", "stream-json", "--permission-mode", "accept_edits", "--no-session-persistence", "--config-dir", "--setting-sources"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv missing hardened flag %q: %#v", want, args)
		}
	}
	// --setting-sources must carry the empty set (disable user/project/local),
	// never a fabricated managed source or a bait source.
	if !containsSequence(args, "--setting-sources", "") {
		t.Fatalf("argv missing empty setting-sources set: %#v", args)
	}
	for _, bait := range []string{"managed", "user", "project", "local"} {
		if containsSequence(args, "--setting-sources", bait) {
			t.Fatalf("argv leaked bait setting source %q: %#v", bait, args)
		}
	}
	noModel := buildArgs("", "/isolated/config", "/worktree", false)
	if strings.Contains(strings.Join(noModel, "\x00"), "--model") {
		t.Fatalf("empty model must not emit --model: %#v", noModel)
	}
}

func TestBuildArgsDisablesAllToolsForExplicitEmptyAllowlist(t *testing.T) {
	args := buildArgs("", "/isolated/config", "/worktree", true)
	if !containsSequence(args, "--tools", "") {
		t.Fatalf("explicit empty allowlist must disable all tools: %#v", args)
	}
}

func TestWorkerEnvironmentRebindsHomeToManagedConfigDir(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "publisher-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "cloud-secret")
	t.Setenv("OPENAI_API_KEY", "model-secret")
	t.Setenv("HOME", "/home/secret-user")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/ssh-secret")
	t.Setenv("XDG_CONFIG_HOME", "/home/secret-user/.config")
	configDir := filepath.Join(t.TempDir(), "managed", "config")
	environment := workerEnvironment(t.TempDir(), configDir)
	joined := strings.Join(environment, "\n")
	for _, secret := range []string{
		"GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "OPENAI_API_KEY", "SSH_AUTH_SOCK",
		"publisher-secret", "cloud-secret", "model-secret", "ssh-secret", "secret-user",
	} {
		if strings.Contains(joined, secret) {
			t.Fatalf("worker environment leaked %s", secret)
		}
	}
	// HOME must be present and rebound to the managed config dir, never empty
	// or inherited from the ambient environment.
	if !strings.Contains(joined, "HOME="+configDir+"\n") {
		t.Fatalf("missing HOME=%s rebind: %s", configDir, joined)
	}
	for _, want := range []string{"CI=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "XDG_CONFIG_HOME=" + configDir} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing isolation environment %s: %s", want, joined)
		}
	}
}

func TestProbeEnvironmentDoesNotUseAmbientHomeOrConfig(t *testing.T) {
	t.Setenv("HOME", "/home/secret-user")
	t.Setenv("XDG_CONFIG_HOME", "/home/secret-user/.config")
	probeRoot := t.TempDir()
	joined := strings.Join(probeEnvironment(probeRoot), "\n")
	if strings.Contains(joined, "secret-user") {
		t.Fatalf("probe environment leaked ambient home/config: %s", joined)
	}
	for _, want := range []string{"HOME=" + probeRoot, "XDG_CONFIG_HOME=" + filepath.Join(probeRoot, "xdg-config"), "XDG_CACHE_HOME=" + filepath.Join(probeRoot, "xdg-cache"), "XDG_DATA_HOME=" + filepath.Join(probeRoot, "xdg-data"), "XDG_STATE_HOME=" + filepath.Join(probeRoot, "xdg-state")} {
		if !strings.Contains(joined, want) {
			t.Fatalf("probe environment missing %s: %s", want, joined)
		}
	}
}

func TestVersionProbeUsesPrivateWritableTemporaryHome(t *testing.T) {
	ambientHome := t.TempDir()
	t.Setenv("HOME", ambientHome)
	capture := filepath.Join(t.TempDir(), "probe-home")
	script := `#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "--version" ]; then
    test -d "$HOME" && test -w "$HOME" || exit 9
    printf '%s' "$HOME" > ` + shellQuote(capture) + `
    : > "$HOME/probe-write"
    printf '%s\n' '1.1.23'
    exit 0
  fi
done
exit 2
`
	executable := filepath.Join(t.TempDir(), "qodercli")
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	version, err := readBinaryVersion(context.Background(), executable)
	if err != nil || version != supportedBinary {
		t.Fatalf("version = %q err=%v", version, err)
	}
	home, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if string(home) == ambientHome || string(home) == "/nonexistent" || !strings.Contains(string(home), "marshal-qoder-probe-") {
		t.Fatalf("probe HOME was not an isolated temporary root: %q", home)
	}
	if _, err := os.Stat(string(home)); !os.IsNotExist(err) {
		t.Fatalf("probe root was not removed after probe: %v", err)
	}
}

func TestManagedConfigDirBindsPrivateDir(t *testing.T) {
	controlRoot := t.TempDir()
	resolved, err := filepath.EvalSymlinks(controlRoot)
	if err != nil {
		t.Fatal(err)
	}
	configDir, err := managedConfigDir(controlRoot)
	if err != nil {
		t.Fatal(err)
	}
	if configDir != filepath.Join(resolved, "config", "qoder") {
		t.Fatalf("configDir = %q, want %q", configDir, filepath.Join(resolved, "config", "qoder"))
	}
	info, err := os.Stat(configDir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("config dir not private: %v", err)
	}
}

func TestManagedConfigDirRejectsSymlinkAndEscape(t *testing.T) {
	t.Run("target-symlink", func(t *testing.T) {
		controlRoot := t.TempDir()
		outside := t.TempDir()
		if err := os.MkdirAll(filepath.Join(controlRoot, "config"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(controlRoot, "config", "qoder")); err != nil {
			t.Fatal(err)
		}
		if _, err := managedConfigDir(controlRoot); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlink config dir must fail closed: %v", err)
		}
	})
	t.Run("parent-symlink", func(t *testing.T) {
		controlRoot := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(controlRoot, "config")); err != nil {
			t.Fatal(err)
		}
		if _, err := managedConfigDir(controlRoot); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlinked parent must fail closed before MkdirAll: %v", err)
		}
		// The uncontrolled parent symlink must not have caused creation
		// outside the control root.
		if _, statErr := os.Stat(filepath.Join(outside, "qoder")); !os.IsNotExist(statErr) {
			t.Fatal("config dir was created through the parent symlink")
		}
	})
	t.Run("world-writable-existing", func(t *testing.T) {
		controlRoot := t.TempDir()
		if err := os.MkdirAll(filepath.Join(controlRoot, "config", "qoder"), 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := managedConfigDir(controlRoot); err == nil || !strings.Contains(err.Error(), "non-private") {
			t.Fatalf("managedConfigDir error = %v, want non-private rejection", err)
		}
	})
	t.Run("world-writable-parent", func(t *testing.T) {
		controlRoot := t.TempDir()
		parent := filepath.Join(controlRoot, "config")
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := managedConfigDir(controlRoot); err == nil || !strings.Contains(err.Error(), "non-private") {
			t.Fatalf("managedConfigDir error = %v, want non-private parent rejection", err)
		}
	})
}

func TestManagedConfigDirResolvesSymlinkedControlRoot(t *testing.T) {
	realRoot := t.TempDir()
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "root")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	configDir, err := managedConfigDir(link)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(realRoot)
	if err != nil {
		t.Fatal(err)
	}
	if configDir != filepath.Join(resolved, "config", "qoder") {
		t.Fatalf("configDir = %q, want under resolved control root %q", configDir, filepath.Join(resolved, "config", "qoder"))
	}
	info, err := os.Stat(configDir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("config dir not private under symlinked control root: %v", err)
	}
}

func TestRunNormalizesResultAndPersistsBoundedTranscript(t *testing.T) {
	body := successEvents("provider/model")
	fixture := newRunFixture(t, supportedBinary, body)
	record, err := fixture.adapter.Run(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.validator.Validate(domain.KindWorkerResult, record.Data); err != nil {
		t.Fatal(err)
	}
	var result declaredResult
	if err := json.Unmarshal(record.Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.TaskID != "TASK-1" || result.Adapter.ID != adapterID || result.Adapter.Version != supportedBinary || result.Adapter.Executable != fixture.executable || result.Session == nil || result.Session.ID != "sess-1" || result.Adapter.Model != "provider/model" {
		t.Fatalf("normalized result = %+v", result)
	}
	if !result.StartedAt.Before(result.CompletedAt) && !result.StartedAt.Equal(result.CompletedAt) {
		t.Fatalf("invalid times: %s %s", result.StartedAt, result.CompletedAt)
	}
	transcript, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qoder-transcript.jsonl"))
	if err != nil || !strings.Contains(string(transcript), `"session_id":"sess-1"`) {
		t.Fatalf("transcript = %s err=%v", transcript, err)
	}
	metadata, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qoder-transcript-meta.json"))
	if err != nil || !strings.Contains(string(metadata), `"eventCount": 3`) {
		t.Fatalf("metadata = %s err=%v", metadata, err)
	}
}

func TestRunRejectsUnsupportedVersionBeforeWorkerLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, "9.9.9", "touch "+shellQuote(marker))
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unsupported worker process was launched")
	}
}

func TestRunRejectsExecutableDriftAfterConformance(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, supportedBinary, "touch "+shellQuote(marker))
	if err := os.WriteFile(fixture.executable, []byte(fakeScript(supportedBinary, "touch "+shellQuote(marker)+"\n# changed")), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrIdentityDrift) {
		t.Fatalf("error = %v, want identity drift", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("drifted executable launched")
	}
}

func TestLaunchSnapshotSurvivesConfiguredExecutableReplacement(t *testing.T) {
	oldMarker, newMarker := filepath.Join(t.TempDir(), "old"), filepath.Join(t.TempDir(), "new")
	executable := fakeExecutable(t, supportedBinary, "touch "+shellQuote(oldMarker))
	identity := executableIdentity{path: executable, version: supportedBinary}
	var err error
	identity.digest, err = digestFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, cleanup, err := snapshotExecutable(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := os.WriteFile(executable, []byte(fakeScript(supportedBinary, "touch "+shellQuote(newMarker))), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(snapshot).Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldMarker); err != nil {
		t.Fatalf("immutable launch object did not run inspected bytes: %v", err)
	}
	if _, err := os.Stat(newMarker); !os.IsNotExist(err) {
		t.Fatal("replacement executable was launched")
	}
}

func TestRunKeepsEvidenceOnTrustedDirectoryHandleAfterRenameAndSymlink(t *testing.T) {
	body := successEvents("provider/model") + `
marshal_root=$(dirname "$(dirname "$HOME")")
mv "$marshal_root/output" "$marshal_root/output-held"
mkdir -p "$PWD/escaped-output"
ln -s "$PWD/escaped-output" "$marshal_root/output"`
	fixture := newRunFixture(t, supportedBinary, body)
	_, err := fixture.adapter.Run(context.Background(), fixture.request)
	if failure, ok := port.AsAdapterFailure(err); !ok || failure.Kind != port.FailureKindProtocolInvalid {
		t.Fatalf("error = %v, want typed protocol-invalid boundary failure", err)
	}
	for _, name := range []string{"qoder-transcript.jsonl", "qoder-stderr.log", "qoder-transcript-meta.json"} {
		if _, err := os.Stat(filepath.Join(fixture.controlRoot, "output-held", name)); err != nil {
			t.Fatalf("trusted evidence %s missing after directory replacement: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(fixture.worktree, "escaped-output", name)); !os.IsNotExist(err) {
			t.Fatalf("evidence %s escaped through replacement symlink", name)
		}
	}
}

func TestRunBindsReportedModelToSystemEvent(t *testing.T) {
	t.Run("requested mismatch", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, successEvents("different/model"))
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		if !errors.Is(err, ErrProtocol) {
			t.Fatalf("error = %v, want model mismatch protocol failure", err)
		}
	})
	t.Run("unspecified uses observed", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, successEvents("actual/model"))
		writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{}})
		record, err := fixture.adapter.Run(context.Background(), fixture.request)
		if err != nil {
			t.Fatal(err)
		}
		var result declaredResult
		if err := json.Unmarshal(record.Data, &result); err != nil {
			t.Fatal(err)
		}
		if result.Adapter.Model != "actual/model" {
			t.Fatalf("model = %q, want observed system model", result.Adapter.Model)
		}
	})
}

func TestRunRejectsStaleResultLeafBeforeLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, supportedBinary, "touch "+shellQuote(marker))
	writeJSON(t, filepath.Join(fixture.controlRoot, "output", "worker-result.json"), validDeclaredResult("/stale"))
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "pre-exists") {
		t.Fatalf("error = %v, want stale result rejection", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("worker launched with a stale result leaf")
	}
}

func TestRunRejectsSymlinkedOutputAncestorWithMissingSuffix(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, supportedBinary, "touch "+shellQuote(marker))
	outside := t.TempDir()
	output := filepath.Join(fixture.controlRoot, "output")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(output, "escape")); err != nil {
		t.Fatal(err)
	}
	request := fixture.requestWith(map[string]any{"resultPath": "output/escape/missing/worker-result.json"})
	if _, err := fixture.adapter.Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %v, want output ancestor symlink rejection", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "missing")); !os.IsNotExist(err) {
		t.Fatal("output directory was created outside the control root")
	}
}

func TestRunRejectsNamedWorkerToolsBeforeLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, supportedBinary, "touch "+shellQuote(marker))
	writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{"model": "provider/model", "tools": []string{"read"}}})
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrUnsupportedWorkerTools) {
		t.Fatalf("error = %v, want unsupported worker tools", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("worker launched with an unverified named tool mapping")
	}
}

func TestRunClearsWorkerClaimedModelWhenTaskSpecOmitsModel(t *testing.T) {
	claimed := validDeclaredResult("/worker/claim")
	claimed["adapter"].(map[string]any)["model"] = "worker-secret-model"
	fixture := newRunFixtureWithResult(t, supportedBinary, successEvents("provider/model"), claimed)
	writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{}})
	record, err := fixture.adapter.Run(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(record.Data), "worker-secret-model") {
		t.Fatalf("normalized result retained worker-claimed model: %s", record.Data)
	}
}

func TestRunFailsClosedOnInvalidTaskSpecBeforeWorkerLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, supportedBinary, "touch "+shellQuote(marker))
	taskSpec := filepath.Join(fixture.controlRoot, "input", "task-spec.json")
	if err := os.WriteFile(taskSpec, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "decode TaskSpec") {
		t.Fatalf("error = %v, want typed TaskSpec decode failure", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("worker process launched after TaskSpec read failure")
	}
}

func TestRunRejectsUnsupportedProfileAndSessionPolicy(t *testing.T) {
	t.Run("profile", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		if _, err := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"executionProfile": "hardened"})); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("err = %v, want profile mismatch", err)
		}
	})
	t.Run("session-policy", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		if _, err := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"sessionPolicy": "persist"})); !errors.Is(err, ErrUnsupportedSessionPolicy) {
			t.Fatalf("err = %v, want ErrUnsupportedSessionPolicy", err)
		}
	})
}

func TestRunRejectsMalformedJSONLAndIdentityMismatch(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, `printf '%s\n' 'not-json'`)
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		failure, ok := port.AsAdapterFailure(err)
		if !errors.Is(err, ErrProtocol) || !ok || failure.Adapter != port.AdapterIDQoder || failure.Kind != port.FailureKindProtocolInvalid || failure.Disposition != port.RetryDispositionDoNotRetry {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("identity", func(t *testing.T) {
		body := successEvents("provider/model")
		data := validDeclaredResult("/worker/claim")
		data["taskId"] = "OTHER"
		fixture := newRunFixtureWithResult(t, supportedBinary, body, data)
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		failure, ok := port.AsAdapterFailure(err)
		if !ok || failure.Kind != port.FailureKindProtocolInvalid || failure.Disposition != port.RetryDispositionDoNotRetry {
			t.Fatalf("error = %v, want typed protocol-invalid/do-not-retry", err)
		}
		if !strings.Contains(err.Error(), "identity") {
			t.Fatalf("error must mention identity mismatch: %v", err)
		}
	})
}

func TestRunProcessFailureNeverLeaksStderrIntoError(t *testing.T) {
	secrets := []string{"qoder-stderr-secret-sentinel-0001", "qoder-stderr-bearer-sentinel-0002", "qoder-stderr-content-sentinel-0003"}
	body := successEvents("provider/model")
	for _, secret := range secrets {
		body += "\nprintf '%s\\n' " + shellQuote(secret) + " >&2"
	}
	body += "\nexit 7"
	fixture := newRunFixture(t, supportedBinary, body)
	_, err := fixture.adapter.Run(context.Background(), fixture.request)
	if !errors.Is(err, ErrProcessFailed) {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(err.Error(), "exit=7") {
		t.Fatalf("error must carry the exit code: %v", err)
	}
	for _, secret := range secrets {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("provider stderr leaked into error: %v", err)
		}
	}
	evidence, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qoder-stderr.log"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, secret := range secrets {
		if !strings.Contains(string(evidence), secret) {
			t.Fatalf("bounded stderr evidence file lost %q", secret)
		}
	}
}
