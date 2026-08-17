package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter/qoder"
)

// writeExecutable creates a regular executable file and returns its absolute
// path. Concrete adapter New only requires an absolute, clean, executable
// regular file; it never runs the binary, so no real provider is needed.
func writeExecutable(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// staticEnv builds a getenv backed by a fixed map; absent keys return "".
func staticEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func configurationByID(t *testing.T, runtime *WorkerRuntime, id string) WorkerConfiguration {
	t.Helper()
	for _, configuration := range runtime.Configurations() {
		if configuration.AdapterID == id {
			return configuration
		}
	}
	t.Fatalf("configuration for adapter %q not found", id)
	return WorkerConfiguration{}
}

func TestNewWorkerRuntimeNilGetenvFailsClosed(t *testing.T) {
	t.Parallel()
	runtime, err := NewWorkerRuntime(nil)
	if err == nil {
		t.Fatal("expected error for nil getenv, got nil")
	}
	if runtime != nil {
		t.Fatalf("expected nil runtime, got %v", runtime)
	}
}

func TestNewWorkerRuntimeAllUnconfigured(t *testing.T) {
	t.Parallel()
	runtime, err := NewWorkerRuntime(staticEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	configurations := runtime.Configurations()
	if len(configurations) != 4 {
		t.Fatalf("expected 4 configurations, got %d", len(configurations))
	}
	wantOrder := []string{"opencode", "qwen", "qoder", "pi"}
	for index, want := range wantOrder {
		if configurations[index].AdapterID != want {
			t.Fatalf("configurations[%d].AdapterID = %q, want %q", index, configurations[index].AdapterID, want)
		}
	}
	for _, configuration := range configurations {
		if configuration.Outcome != WorkerOutcomeNotConfigured {
			t.Errorf("adapter %q outcome = %q, want %q", configuration.AdapterID, configuration.Outcome, WorkerOutcomeNotConfigured)
		}
		if configuration.Configured {
			t.Errorf("adapter %q should not be configured", configuration.AdapterID)
		}
		if configuration.Registered {
			t.Errorf("adapter %q should not be registered", configuration.AdapterID)
		}
	}
	if ids := runtime.Registry().IDs(); len(ids) != 0 {
		t.Fatalf("expected empty registry, got %v", ids)
	}
}

func TestQoderRegistrationRemainsUnsupportedWithoutAuthorityEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qodercli")
	script := "#!/bin/sh\nfor arg in \"$@\"; do if [ \"$arg\" = \"--version\" ]; then printf '1.1.23\\n'; exit 0; fi; done\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewWorkerRuntime(staticEnv(map[string]string{"MARSHAL_QODER_PATH": path}))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := runtime.Registry().Resolve("qoder")
	if err != nil {
		t.Fatal(err)
	}
	record, err := worker.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		ProbeStatus string `json:"probeStatus"`
	}
	if err := json.Unmarshal(record.Data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ProbeStatus != "unsupported" {
		t.Fatalf("qoder without authority evidence = %q, want unsupported", snapshot.ProbeStatus)
	}
}

// TestQoderHermeticSignedAuthorityWiring verifies only the cryptographic and
// runtime wiring. Its fake executable, generated key and synthetic transcript
// are not credentialed live evidence and make no deployment readiness claim.
func TestQoderHermeticSignedAuthorityWiring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qodercli")
	script := "#!/bin/sh\nfor arg in \"$@\"; do if [ \"$arg\" = \"--version\" ]; then printf '1.1.23\\n'; exit 0; fi; done\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	version, executableDigest, err := qoder.Identify(path)
	if err != nil {
		t.Fatal(err)
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	contract := qoder.FrozenLiveConformanceContract()
	hostFingerprint, err := qoder.CurrentHostFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	evidence, evidenceDigest, err := qoder.SealConformanceEvidence(qoder.LiveConformanceObservation{
		RunnerID: "independent-verifier", RunnerVersion: "1", ObservedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour),
		Executable: realPath, ExecutableDigest: executableDigest, BinaryVersion: version, HostOS: runtime.GOOS, HostArch: runtime.GOARCH, HostFingerprint: hostFingerprint, AuthorityGeneration: 1,
		ProbeSuiteDigest: contract.ProbeSuiteDigest, ProbeArtifactDigest: "sha256:" + strings.Repeat("a", 64), ChallengeDigest: "sha256:" + strings.Repeat("c", 64), CapabilitiesDigest: contract.CapabilitiesDigest, ProbeProfileDigest: contract.ProbeProfileDigest, ArgvDigest: contract.ArgvDigest, EnvironmentDigest: contract.EnvironmentDigest, ToolPolicyDigest: contract.ToolPolicyDigest,
		TranscriptDigest: "sha256:" + strings.Repeat("b", 64), CredentialVerified: true, LiveProtocolVerified: true, WorkspaceWriteVerified: true, EventContract: contract.EventContract, ProtocolVersion: contract.ProtocolVersion, PermissionMode: contract.PermissionMode, TrustRootKeyID: "root-1",
	}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, strings.TrimPrefix(evidenceDigest, "sha256:")+".json"), evidence, 0o600); err != nil {
		t.Fatal(err)
	}
	configParent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configParent, "qoder-authority.json")
	config, err := json.Marshal(qoder.AuthorityConfig{
		EvidenceRoot: root, EvidenceDigest: evidenceDigest, AuthorityGeneration: 1, ProbeArtifactDigest: "sha256:" + strings.Repeat("a", 64), RevokedEvidenceDigests: []string{},
		TrustRoots: []qoder.AuthorityTrustRoot{{KeyID: "root-1", Algorithm: "ed25519", PublicKey: base64.StdEncoding.EncodeToString(publicKey)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewWorkerRuntime(staticEnv(map[string]string{
		"MARSHAL_QODER_PATH": path, "MARSHAL_QODER_CONFORMANCE_CONFIG": configPath,
	}))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := runtime.Registry().Resolve("qoder")
	if err != nil {
		t.Fatal(err)
	}
	record, err := worker.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(record.Data), `"probeStatus":"supported"`) {
		t.Fatalf("authority-wired qoder was not supported: %s", record.Data)
	}
}

func TestNewWorkerRuntimeRegistersAllThree(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"MARSHAL_OPENCODE_PATH": writeExecutable(t, "opencode"),
		"MARSHAL_QWEN_PATH":     writeExecutable(t, "qwen"),
		"MARSHAL_QODER_PATH":    writeExecutable(t, "qodercli"),
		"MARSHAL_PI_PATH":       writeExecutable(t, "pi"),
	}
	runtime, err := NewWorkerRuntime(staticEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	configurations := runtime.Configurations()
	if len(configurations) != 4 {
		t.Fatalf("expected 4 configurations, got %d", len(configurations))
	}
	wantOrder := []string{"opencode", "qwen", "qoder", "pi"}
	for index, want := range wantOrder {
		if configurations[index].AdapterID != want {
			t.Fatalf("configurations[%d].AdapterID = %q, want %q", index, configurations[index].AdapterID, want)
		}
		configuration := configurations[index]
		if !configuration.Configured || !configuration.Registered {
			t.Errorf("adapter %q configured=%v registered=%v, want both true", want, configuration.Configured, configuration.Registered)
		}
		if configuration.Outcome != WorkerOutcomeRegistered {
			t.Errorf("adapter %q outcome = %q, want %q", want, configuration.Outcome, WorkerOutcomeRegistered)
		}
	}
	// Registry.IDs returns sorted IDs; verify membership and count.
	ids := runtime.Registry().IDs()
	if len(ids) != 4 {
		t.Fatalf("expected 4 registered adapters, got %v", ids)
	}
	wantIDs := map[string]bool{"opencode": true, "qwen": true, "qoder": true, "pi": true}
	for _, id := range ids {
		if !wantIDs[id] {
			t.Errorf("unexpected registered adapter %q", id)
		}
	}
	for _, want := range wantOrder {
		if _, err := runtime.Registry().Resolve(want); err != nil {
			t.Errorf("resolve %q: %v", want, err)
		}
	}
}

func TestNewWorkerRuntimeInvalidPathDoesNotLeakAndOthersRegister(t *testing.T) {
	t.Parallel()
	invalidPath := "/nonexistent/marshal-leak-check/opencode-binary"
	env := map[string]string{
		"MARSHAL_OPENCODE_PATH": invalidPath,
		"MARSHAL_QWEN_PATH":     writeExecutable(t, "qwen"),
		"MARSHAL_PI_PATH":       writeExecutable(t, "pi"),
	}
	runtime, err := NewWorkerRuntime(staticEnv(env))
	if err != nil {
		t.Fatalf("invalid single adapter must not abort runtime: %v", err)
	}
	opencodeConfig := configurationByID(t, runtime, "opencode")
	if opencodeConfig.Outcome != WorkerOutcomeInvalidConfiguration {
		t.Fatalf("opencode outcome = %q, want %q", opencodeConfig.Outcome, WorkerOutcomeInvalidConfiguration)
	}
	if !opencodeConfig.Configured || opencodeConfig.Registered {
		t.Fatalf("opencode configured=%v registered=%v, want configured=true registered=false", opencodeConfig.Configured, opencodeConfig.Registered)
	}
	// No structured configuration field may echo the invalid path.
	for _, configuration := range runtime.Configurations() {
		value := reflect.ValueOf(configuration)
		for field := 0; field < value.NumField(); field++ {
			if fieldValue, ok := value.Field(field).Interface().(string); ok && fieldValue == invalidPath {
				t.Fatalf("invalid path leaked in configuration field %s", value.Type().Field(field).Name)
			}
		}
	}
	// The other adapters must still be registered.
	for _, id := range []string{"qwen", "pi"} {
		configuration := configurationByID(t, runtime, id)
		if !configuration.Registered || configuration.Outcome != WorkerOutcomeRegistered {
			t.Errorf("adapter %q registered=%v outcome=%q, want registered with %q", id, configuration.Registered, configuration.Outcome, WorkerOutcomeRegistered)
		}
		if _, err := runtime.Registry().Resolve(id); err != nil {
			t.Errorf("resolve %q: %v", id, err)
		}
	}
	if _, err := runtime.Registry().Resolve("opencode"); err == nil {
		t.Error("opencode must not be resolvable after invalid configuration")
	}
}

func TestNewWorkerRuntimeRelativePathIsInvalid(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"MARSHAL_PI_PATH": "bin/pi",
	}
	runtime, err := NewWorkerRuntime(staticEnv(env))
	if err != nil {
		t.Fatalf("relative adapter path must not abort runtime: %v", err)
	}
	configuration := configurationByID(t, runtime, "pi")
	if configuration.Outcome != WorkerOutcomeInvalidConfiguration {
		t.Fatalf("pi outcome = %q, want %q", configuration.Outcome, WorkerOutcomeInvalidConfiguration)
	}
	if configuration.Registered {
		t.Error("relative path must not be registered")
	}
	if _, err := runtime.Registry().Resolve("pi"); err == nil {
		t.Error("pi must not be resolvable for a relative path")
	}
}

func TestNewWorkerRuntimePaddedExecutableIsInvalidAndNotLeaked(t *testing.T) {
	t.Parallel()
	realPath := writeExecutable(t, "pi")
	// A real absolute executable padded with whitespace, plus whitespace-only
	// values: all are non-empty, so all stay configured and must be rejected
	// by concrete New as-is. None may be trimmed into a valid path.
	paddedValues := []string{
		" " + realPath,
		realPath + " ",
		"\t" + realPath,
		realPath + "\n",
		"   ",
		"\t",
	}
	for _, padded := range paddedValues {
		runtime, err := NewWorkerRuntime(staticEnv(map[string]string{
			"MARSHAL_PI_PATH": padded,
		}))
		if err != nil {
			t.Fatalf("padded adapter value must not abort runtime: %v", err)
		}
		configuration := configurationByID(t, runtime, "pi")
		if !configuration.Configured {
			t.Errorf("non-empty padded value must keep configured=true")
		}
		if configuration.Registered {
			t.Errorf("padded value must not be registered")
		}
		if configuration.Outcome != WorkerOutcomeInvalidConfiguration {
			t.Errorf("pi outcome = %q, want %q", configuration.Outcome, WorkerOutcomeInvalidConfiguration)
		}
		if _, resolveErr := runtime.Registry().Resolve("pi"); resolveErr == nil {
			t.Errorf("pi must not be resolvable for a padded value")
		}
		// No structured configuration field may echo the raw padded value.
		for _, other := range runtime.Configurations() {
			value := reflect.ValueOf(other)
			for field := 0; field < value.NumField(); field++ {
				if fieldValue, ok := value.Field(field).Interface().(string); ok && fieldValue == padded {
					t.Fatalf("raw padded value leaked in configuration field %s", value.Type().Field(field).Name)
				}
			}
		}
	}
}

func TestConfigurationsCloneIsImmutable(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"MARSHAL_QWEN_PATH": writeExecutable(t, "qwen"),
	}
	runtime, err := NewWorkerRuntime(staticEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	first := runtime.Configurations()
	// Mutate the returned clone: tamper with every field.
	for index := range first {
		first[index].Outcome = "tampered"
		first[index].Registered = !first[index].Registered
		first[index].Configured = !first[index].Configured
		first[index].AdapterID = "tampered"
		first[index].EnvironmentVariable = "tampered"
	}
	second := runtime.Configurations()
	qwenConfig := configurationByID(t, runtime, "qwen")
	if qwenConfig.Outcome != WorkerOutcomeRegistered || !qwenConfig.Registered {
		t.Fatalf("internal state was mutated through the clone: %+v", qwenConfig)
	}
	if reflect.DeepEqual(first, second) {
		t.Fatal("expected fresh clone to differ from the tampered slice")
	}
	// The clone must preserve frozen order and outcomes.
	wantOrder := []string{"opencode", "qwen", "qoder", "pi"}
	for index, want := range wantOrder {
		if second[index].AdapterID != want {
			t.Fatalf("second[%d].AdapterID = %q, want %q", index, second[index].AdapterID, want)
		}
	}
}

func TestSelectorIsConstructed(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"MARSHAL_PI_PATH": writeExecutable(t, "pi"),
	}
	runtime, err := NewWorkerRuntime(staticEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	// Real Probes are version-gated and must not be faked here; only verify
	// that the Selector is bound and non-nil.
	if runtime.Selector() == nil {
		t.Fatal("expected non-nil Selector")
	}
	if runtime.Validator() == nil {
		t.Fatal("expected non-nil Validator")
	}
	if runtime.Registry() == nil {
		t.Fatal("expected non-nil Registry")
	}
}
