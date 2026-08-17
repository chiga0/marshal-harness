package qoder

import (
	"context"
	"encoding/json"
	"errors"
	"os"
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
	args := buildArgs("provider/model", "/managed/config", "完成任务")
	want := []string{"--print", "--output-format", "stream-json", "--permission-mode", "accept_edits", "--no-session-persistence", "--config-dir", "/managed/config", "--setting-sources", "", "--model", "provider/model", "完成任务"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v", args)
	}
}

func TestBuildArgsRejectsFabricatedRunSandboxArgv(t *testing.T) {
	args := buildArgs("", "/isolated/config", "完成任务")
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
	noModel := buildArgs("", "/isolated/config", "完成任务")
	if strings.Contains(strings.Join(noModel, "\x00"), "--model") {
		t.Fatalf("empty model must not emit --model: %#v", noModel)
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
	joined := strings.Join(probeEnvironment(), "\n")
	if strings.Contains(joined, "secret-user") {
		t.Fatalf("probe environment leaked ambient home/config: %s", joined)
	}
	for _, want := range []string{"HOME=/nonexistent", "XDG_CONFIG_HOME=/nonexistent", "XDG_CACHE_HOME=/nonexistent", "XDG_DATA_HOME=/nonexistent", "XDG_STATE_HOME=/nonexistent"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("probe environment missing %s: %s", want, joined)
		}
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
	body := emitLines(
		`{"type":"session","id":"sess-1"}`,
		`{"type":"usage","input_tokens":10,"output_tokens":5}`,
		`{"type":"result","status":"success"}`,
	)
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
	if err != nil || !strings.Contains(string(transcript), `"id":"sess-1"`) {
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
		body := emitLines(`{"type":"session","id":"sess-1"}`, `{"type":"result","status":"success"}`)
		fixture := newRunFixture(t, supportedBinary, body)
		data := validDeclaredResult(fixture.executable)
		data["taskId"] = "OTHER"
		writeJSON(t, filepath.Join(fixture.controlRoot, "output", "worker-result.json"), data)
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
	body := emitLines(`{"type":"session","id":"sess-1"}`, `{"type":"result","status":"success"}`)
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
