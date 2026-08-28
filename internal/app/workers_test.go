package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/chiga0/marshal-harness/internal/adapter"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"github.com/chiga0/marshal-harness/internal/sandboxbridge"
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

func writeCanonicalExecutable(t *testing.T, name string) string {
	t.Helper()
	path := writeExecutable(t, name)
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
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
	if len(configurations) != 5 {
		t.Fatalf("expected 5 configurations, got %d", len(configurations))
	}
	wantOrder := []string{"opencode", "qwen", "qoder", "codex", "pi"}
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
	script := "#!/bin/sh\nfor arg in \"$@\"; do if [ \"$arg\" = \"--version\" ]; then printf '1.1.27\\n'; exit 0; fi; done\nexit 1\n"
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

func TestMacOrdinaryUserModeRegistersQoderAndCodexWithExplicitLabel(t *testing.T) {
	qoderPath := writeExecutable(t, "qodercli")
	if err := os.WriteFile(qoderPath, []byte("#!/bin/sh\nprintf '1.1.27\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	codexPath := writeExecutable(t, "codex")
	if err := os.WriteFile(codexPath, []byte("#!/bin/sh\nprintf 'codex-cli 0.145.0\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runtimeValue, err := NewWorkerRuntime(staticEnv(map[string]string{
		"MARSHAL_QODER_PATH": qoderPath, "MARSHAL_QODER_MODE": "ordinary-user",
		"MARSHAL_CODEX_PATH": codexPath, "MARSHAL_CODEX_MODE": "ordinary-user",
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"qoder", "codex"} {
		configuration := configurationByID(t, runtimeValue, id)
		if !configuration.Registered || configuration.Outcome != WorkerOutcomeRegistered || configuration.AuthorityMode != "ordinary-user" {
			t.Fatalf("%s configuration = %+v", id, configuration)
		}
		worker, err := runtimeValue.Registry().Resolve(id)
		if err != nil {
			t.Fatal(err)
		}
		record, err := worker.Probe(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var snapshot struct {
			ProbeStatus string   `json:"probeStatus"`
			ProbeErrors []string `json:"probeErrors"`
		}
		if err := json.Unmarshal(record.Data, &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.ProbeStatus != "supported" || len(snapshot.ProbeErrors) != 0 {
			t.Fatalf("%s ordinary-user snapshot = %s", id, record.Data)
		}
	}
}

func TestMacOrdinaryUserModeRegistersAndProbesCodex01491(t *testing.T) {
	codexPath := writeExecutable(t, "codex")
	if err := os.WriteFile(codexPath, []byte("#!/bin/sh\nprintf 'codex-cli 0.149.1\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runtimeValue, err := NewWorkerRuntime(staticEnv(map[string]string{
		"MARSHAL_CODEX_PATH": codexPath,
		"MARSHAL_CODEX_MODE": "ordinary-user",
	}))
	if err != nil {
		t.Fatal(err)
	}
	configuration := configurationByID(t, runtimeValue, "codex")
	if !configuration.Registered || configuration.Outcome != WorkerOutcomeRegistered || configuration.AuthorityMode != "ordinary-user" {
		t.Fatalf("codex configuration = %+v", configuration)
	}
	worker, err := runtimeValue.Registry().Resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	record, err := worker.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		BinaryVersion string `json:"binaryVersion"`
		ProbeStatus   string `json:"probeStatus"`
		AuthorityMode string `json:"authorityMode"`
	}
	if err := json.Unmarshal(record.Data, &snapshot); err != nil {
		t.Fatal(err)
	}
	wantStatus := "unsupported"
	if runtime.GOOS == "darwin" {
		wantStatus = "supported"
	}
	if snapshot.BinaryVersion != "0.149.1" || snapshot.ProbeStatus != wantStatus || snapshot.AuthorityMode != "ordinary-user" {
		t.Fatalf("codex 0.149.1 snapshot = %s", record.Data)
	}
}

func TestOrdinaryUserModeRejectsUnknownValue(t *testing.T) {
	path := writeExecutable(t, "qodercli")
	runtimeValue, err := NewWorkerRuntime(staticEnv(map[string]string{
		"MARSHAL_QODER_PATH": path, "MARSHAL_QODER_MODE": "unsafe",
	}))
	if err != nil {
		t.Fatal(err)
	}
	configuration := configurationByID(t, runtimeValue, "qoder")
	if configuration.Registered || configuration.Outcome != WorkerOutcomeInvalidConfiguration {
		t.Fatalf("unknown mode configuration = %+v", configuration)
	}
}

func TestMacAuthorityEndpointStatusIsDiagnosticOnly(t *testing.T) {
	qoderPath := filepath.Join(t.TempDir(), "qodercli")
	if err := os.WriteFile(qoderPath, []byte("#!/bin/sh\nprintf '1.1.27\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	codexPath := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(codexPath, []byte("#!/bin/sh\nprintf 'codex-cli 0.145.0\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runtimeValue, err := NewWorkerRuntime(staticEnv(map[string]string{
		"MARSHAL_QODER_PATH":            qoderPath,
		"MARSHAL_CODEX_PATH":            codexPath,
		"MARSHAL_APAP_ENDPOINT":         "/private/var/run/marshal-apap.sock",
		"MARSHAL_DARWIN_LAUNCHD_CONFIG": "/private/var/run/marshal-deployment.json",
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"qoder", "codex"} {
		status := configurationByID(t, runtimeValue, id).AuthorityEndpointStatus
		if runtime.GOOS == "darwin" {
			if status != "unavailable" {
				t.Fatalf("%s authority status = %q, want unavailable", id, status)
			}
		} else if status != "unsupported-platform" {
			t.Fatalf("%s authority status = %q, want unsupported-platform", id, status)
		}
		deployment := configurationByID(t, runtimeValue, id).AuthorityDeploymentStatus
		if runtime.GOOS == "darwin" {
			if deployment != "unsafe" {
				t.Fatalf("%s deployment status = %q, want unsafe", id, deployment)
			}
		} else if deployment != "unsupported-platform" {
			t.Fatalf("%s deployment status = %q, want unsupported-platform", id, deployment)
		}
	}
	qoder, err := runtimeValue.Registry().Resolve("qoder")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := qoder.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		ProbeStatus string `json:"probeStatus"`
	}
	if err := json.Unmarshal(snapshot.Data, &probe); err != nil {
		t.Fatal(err)
	}
	if probe.ProbeStatus != "unsupported" {
		t.Fatalf("endpoint status must not admit qoder: %q", probe.ProbeStatus)
	}
}

func TestQoderAuthorityConfigCannotActivateWhileADR0034Proposed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qodercli")
	script := "#!/bin/sh\nprintf '1.1.27\\n'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "qoder-authority.json")
	if err := os.WriteFile(configPath, []byte(`{"candidate":"must-not-activate"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewWorkerRuntime(staticEnv(map[string]string{
		"MARSHAL_QODER_PATH": path, "MARSHAL_QODER_CONFORMANCE_CONFIG": configPath,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Registry().Resolve("qoder"); err == nil {
		t.Fatal("Proposed ADR authority config activated qoder")
	}
	configuration := configurationByID(t, runtime, "qoder")
	if !configuration.Configured || configuration.Registered || configuration.Outcome != WorkerOutcomeInvalidConfiguration {
		t.Fatalf("qoder configuration = %+v", configuration)
	}
}

func TestCodexRegistrationIsObservableButCannotActivateAuthority(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf 'codex-cli 0.145.0\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewWorkerRuntime(staticEnv(map[string]string{"MARSHAL_CODEX_PATH": executable}))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := runtime.Registry().Resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	record, err := worker.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		ProbeStatus    string          `json:"probeStatus"`
		ProbeErrors    []string        `json:"probeErrors"`
		AdapterFailure json.RawMessage `json:"adapterFailure"`
	}
	if err := json.Unmarshal(record.Data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ProbeStatus != "unsupported" || len(snapshot.ProbeErrors) != 1 || len(snapshot.AdapterFailure) == 0 {
		t.Fatalf("codex fail-closed snapshot = %s", record.Data)
	}

	runtime, err = NewWorkerRuntime(staticEnv(map[string]string{
		"MARSHAL_CODEX_PATH": executable, "MARSHAL_CODEX_AUTHORITY_CONFIG": filepath.Join(t.TempDir(), "authority.json"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	configuration := configurationByID(t, runtime, "codex")
	if !configuration.Configured || configuration.Registered || configuration.Outcome != WorkerOutcomeInvalidConfiguration {
		t.Fatalf("codex authority configuration = %+v", configuration)
	}
}

func TestNewWorkerRuntimeRegistersAllThree(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"MARSHAL_OPENCODE_PATH": writeExecutable(t, "opencode"),
		"MARSHAL_QWEN_PATH":     writeExecutable(t, "qwen"),
		"MARSHAL_QODER_PATH":    writeExecutable(t, "qodercli"),
		"MARSHAL_CODEX_PATH":    writeExecutable(t, "codex"),
		"MARSHAL_PI_PATH":       writeExecutable(t, "pi"),
	}
	runtime, err := NewWorkerRuntime(staticEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	configurations := runtime.Configurations()
	if len(configurations) != 5 {
		t.Fatalf("expected 5 configurations, got %d", len(configurations))
	}
	wantOrder := []string{"opencode", "qwen", "qoder", "codex", "pi"}
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
	if len(ids) != 5 {
		t.Fatalf("expected 5 registered adapters, got %v", ids)
	}
	wantIDs := map[string]bool{"opencode": true, "qwen": true, "qoder": true, "codex": true, "pi": true}
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

func TestProductionSelectorRejectsPiWithoutExplicitNodeRuntimeBeforeProbe(t *testing.T) {
	piPath := writeExecutable(t, "pi")
	runtimeValue, err := NewWorkerRuntime(staticEnv(map[string]string{"MARSHAL_PI_PATH": piPath}))
	if err != nil {
		t.Fatal(err)
	}
	selection, err := runtimeValue.ProductionSelector().Select(context.Background(), adapter.SelectionRequest{
		PreferredAdapter: "pi",
		AllowedAdapters:  []string{"pi"},
	})
	if err == nil {
		t.Fatal("PATH-only Pi was admitted to the production selector")
	}
	if !errors.Is(err, launchidentity.ErrUnavailable) {
		t.Fatalf("selection error = %v, want typed unavailable", err)
	}
	if selection.Adapter != nil || len(selection.Attempts) != 1 || selection.Attempts[0].Outcome != adapter.OutcomeNotLaunchCapable {
		t.Fatalf("selection = %+v", selection)
	}
	// Compatibility remains explicit and available; production rejection must
	// not unregister the ordinary adapter.
	if _, err := runtimeValue.Registry().Resolve("pi"); err != nil {
		t.Fatalf("legacy Pi registration lost: %v", err)
	}
}

func TestProductionSelectorRejectsExactPiUntilAttemptRuntimeIsComposedWithoutProbe(t *testing.T) {
	dir := t.TempDir()
	probeSentinel := filepath.Join(dir, "probe-ran")
	piPath := filepath.Join(dir, "pi")
	if err := os.WriteFile(piPath, []byte("#!/bin/sh\ntouch "+probeSentinel+"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runtimeValue, err := NewWorkerRuntime(staticEnv(map[string]string{
		"MARSHAL_PI_PATH":      piPath,
		"MARSHAL_PI_NODE_PATH": writeCanonicalExecutable(t, "node"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	selection, err := runtimeValue.ProductionSelector().Select(context.Background(), adapter.SelectionRequest{
		PreferredAdapter: "pi",
		AllowedAdapters:  []string{"pi"},
	})
	if !errors.Is(err, launchidentity.ErrUnavailable) || selection.Adapter != nil {
		t.Fatalf("selection=%+v error=%v", selection, err)
	}
	if _, statErr := os.Stat(probeSentinel); !os.IsNotExist(statErr) {
		t.Fatalf("production selector executed Probe before exact Attempt runtime admission: %v", statErr)
	}
}

func TestProductionCapabilityRequiresExactPiRuntimeProfile(t *testing.T) {
	runtimeValue, err := NewWorkerRuntime(staticEnv(map[string]string{
		"MARSHAL_PI_PATH":      writeExecutable(t, "pi"),
		"MARSHAL_PI_NODE_PATH": writeCanonicalExecutable(t, "node"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := runtimeValue.Registry().Resolve("pi")
	if err != nil {
		t.Fatal(err)
	}
	capable, ok := worker.(sandboxbridge.ProductionLaunchCapable)
	if !ok {
		t.Fatalf("production capability = %T", worker)
	}
	profile := capable.ProductionLaunchProfileID()
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		if profile != "" {
			t.Fatalf("non-Darwin production profile = %q", profile)
		}
		return
	}
	if profile != launchidentity.Pi0843DarwinARM64Profile {
		t.Fatalf("production profile = %q", profile)
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
	wantOrder := []string{"opencode", "qwen", "qoder", "codex", "pi"}
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
