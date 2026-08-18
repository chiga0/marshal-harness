package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// discoveryCandidate mirrors app.DiscoveryCandidate for JSON assertions.
type discoveryCandidate struct {
	Path     string `json:"path"`
	Realpath string `json:"realpath"`
	SHA256   string `json:"sha256"`
	Version  string `json:"version"`
	Source   string `json:"source"`
}

// discoveryEntry mirrors app.Discovery for JSON assertions.
type discoveryEntry struct {
	AdapterID           string               `json:"adapterId"`
	EnvironmentVariable string               `json:"environmentVariable"`
	Candidates          []discoveryCandidate `json:"candidates"`
	SuggestedEnv        string               `json:"suggestedEnv"`
}

type doctorDiscoveryReport struct {
	Workers []struct {
		AdapterID string `json:"adapterId"`
		Outcome   string `json:"outcome"`
	} `json:"workers"`
	Discovery []discoveryEntry `json:"discovery"`
}

// fakeWorkerScript returns a shell script that accepts only the exact version
// probe argv frozen by the matching Adapter. Qoder additionally requires its
// private temporary config directory and disabled setting sources.
func fakeWorkerScript(name, version string) string {
	switch name {
	case "qodercli":
		return "#!/bin/sh\nif [ \"$#\" -eq 5 ] && [ \"$1\" = \"--config-dir\" ] && [ -d \"$2\" ] && [ \"$3\" = \"--setting-sources\" ] && [ -z \"$4\" ] && [ \"$5\" = \"--version\" ]; then printf '%s\\n' '" + version + "'; exit 0; fi\nexit 1\n"
	case "opencode", "qwen", "qwen-code", "pi":
		return "#!/bin/sh\nif [ \"$#\" -eq 1 ] && [ \"$1\" = \"--version\" ]; then printf '%s\\n' '" + version + "'; exit 0; fi\nexit 1\n"
	case "codex":
		return "#!/bin/sh\nif [ \"$#\" -eq 1 ] && [ \"$1\" = \"--version\" ]; then printf 'codex-cli %s\\n' '" + version + "'; exit 0; fi\nexit 1\n"
	default:
		return "#!/bin/sh\nexit 1\n"
	}
}

// plantBinary writes an executable fake Worker binary and returns its path
// together with the resolved realpath used by discovery.
func plantBinary(t *testing.T, dir, name, version string) (string, string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(fakeWorkerScript(name, version)), 0o700); err != nil {
		t.Fatal(err)
	}
	realpath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, realpath
}

// hermeticDiscoveryEnvironment pins PATH to binDir, isolates HOME, disables
// the default known install locations, and clears every adapter registration
// variable so discovery scans only the controlled PATH entry.
func hermeticDiscoveryEnvironment(t *testing.T, binDir string) {
	t.Helper()
	t.Setenv("PATH", binDir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MARSHAL_DISCOVERY_KNOWN_LOCATIONS", "-")
	t.Setenv("FNM_DIR", "")
	t.Setenv("MARSHAL_OPENCODE_PATH", "")
	t.Setenv("MARSHAL_QWEN_PATH", "")
	t.Setenv("MARSHAL_QODER_PATH", "")
	t.Setenv("MARSHAL_CODEX_PATH", "")
	t.Setenv("MARSHAL_PI_PATH", "")
}

func findDiscovery(entries []discoveryEntry, adapterID string) *discoveryEntry {
	for index := range entries {
		if entries[index].AdapterID == adapterID {
			return &entries[index]
		}
	}
	return nil
}

func runDoctorDiscoveryJSON(t *testing.T) doctorDiscoveryReport {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"doctor", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("doctor exit = %d, stderr = %s", exit, stderr.String())
	}
	var report doctorDiscoveryReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode doctor discovery: %v\n%s", err, stdout.String())
	}
	return report
}

// TestDoctorDiscoveryPinsCandidateAndSuggestion plants a fake binary on a
// temporary PATH and checks the structured discovery section carries the
// candidate identity and a suggested export value, without registering it.
func TestDoctorDiscoveryPinsCandidateAndSuggestion(t *testing.T) {
	binDir := t.TempDir()
	hermeticDiscoveryEnvironment(t, binDir)
	wantVersion := "0.21.5"
	path, realpath := plantBinary(t, binDir, "qwen", wantVersion)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	wantDigest := "sha256:" + hex.EncodeToString(sum[:])

	report := runDoctorDiscoveryJSON(t)
	entry := findDiscovery(report.Discovery, "qwen")
	if entry == nil {
		t.Fatalf("discovery missing qwen entry: %+v", report.Discovery)
	}
	if len(entry.Candidates) != 1 {
		t.Fatalf("qwen candidates = %+v", entry.Candidates)
	}
	candidate := entry.Candidates[0]
	if candidate.Path != path || candidate.Realpath != realpath || candidate.Version != wantVersion || candidate.SHA256 != wantDigest || candidate.Source != "path" {
		t.Fatalf("candidate = %+v, want path=%q realpath=%q version=%q digest=%q source=path", candidate, path, realpath, wantVersion, wantDigest)
	}
	if entry.SuggestedEnv != realpath {
		t.Fatalf("suggestedEnv = %q, want %q", entry.SuggestedEnv, realpath)
	}
	if entry.EnvironmentVariable != "MARSHAL_QWEN_PATH" {
		t.Fatalf("environmentVariable = %q", entry.EnvironmentVariable)
	}
	// Discovery is advisory: the worker must stay unregistered.
	for _, worker := range report.Workers {
		if worker.AdapterID == "qwen" && worker.Outcome != "not-configured" {
			t.Fatalf("qwen worker outcome = %q, want not-configured", worker.Outcome)
		}
	}
}

// TestDoctorDiscoveryCoversEveryBinding plants binaries for every known
// names and verifies each binding reports its own candidate and suggestion.
func TestDoctorDiscoveryCoversEveryBinding(t *testing.T) {
	binDir := t.TempDir()
	hermeticDiscoveryEnvironment(t, binDir)
	_, opencodeReal := plantBinary(t, binDir, "opencode", "1.18.13")
	_, qwenReal := plantBinary(t, binDir, "qwen-code", "0.21.5")
	_, qoderReal := plantBinary(t, binDir, "qodercli", "1.1.23")
	_, codexReal := plantBinary(t, binDir, "codex", "0.145.0")
	_, piReal := plantBinary(t, binDir, "pi", "0.83.0")

	report := runDoctorDiscoveryJSON(t)
	expectations := []struct {
		adapterID string
		variable  string
		realpath  string
	}{
		{"opencode", "MARSHAL_OPENCODE_PATH", opencodeReal},
		{"qwen", "MARSHAL_QWEN_PATH", qwenReal},
		{"qoder", "MARSHAL_QODER_PATH", qoderReal},
		{"codex", "MARSHAL_CODEX_PATH", codexReal},
		{"pi", "MARSHAL_PI_PATH", piReal},
	}
	for _, expectation := range expectations {
		entry := findDiscovery(report.Discovery, expectation.adapterID)
		if entry == nil {
			t.Fatalf("discovery missing %s entry: %+v", expectation.adapterID, report.Discovery)
		}
		if entry.EnvironmentVariable != expectation.variable {
			t.Fatalf("%s environmentVariable = %q, want %q", expectation.adapterID, entry.EnvironmentVariable, expectation.variable)
		}
		if entry.SuggestedEnv != expectation.realpath {
			t.Fatalf("%s suggestedEnv = %q, want %q", expectation.adapterID, entry.SuggestedEnv, expectation.realpath)
		}
		if len(entry.Candidates) != 1 || entry.Candidates[0].Realpath != expectation.realpath {
			t.Fatalf("%s candidates = %+v", expectation.adapterID, entry.Candidates)
		}
	}
}

// TestDoctorPrintEnvEmitsOnlySuggestedExports checks the --print-env flag
// prints exactly the export lines for adapters discovery can suggest.
func TestDoctorPrintEnvEmitsOnlySuggestedExports(t *testing.T) {
	binDir := t.TempDir()
	hermeticDiscoveryEnvironment(t, binDir)
	_, qwenReal := plantBinary(t, binDir, "qwen", "0.21.5")

	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"doctor", "--print-env"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("print-env exit = %d, stderr = %s", exit, stderr.String())
	}
	want := "export MARSHAL_QWEN_PATH=" + qwenReal + "\n"
	if stdout.String() != want {
		t.Fatalf("print-env output = %q, want %q", stdout.String(), want)
	}
}

// TestDoctorDiscoveryCleanWhenNoCandidates pins that with no candidates the
// report keeps every adapter not-configured and carries empty discovery
// entries without inventing suggestions.
func TestDoctorDiscoveryCleanWhenNoCandidates(t *testing.T) {
	binDir := t.TempDir()
	hermeticDiscoveryEnvironment(t, binDir)

	report := runDoctorDiscoveryJSON(t)
	for _, adapterID := range []string{"opencode", "qwen", "qoder", "codex", "pi"} {
		entry := findDiscovery(report.Discovery, adapterID)
		if entry == nil {
			t.Fatalf("discovery missing %s entry: %+v", adapterID, report.Discovery)
		}
		if len(entry.Candidates) != 0 {
			t.Fatalf("%s candidates = %+v, want none", adapterID, entry.Candidates)
		}
		if entry.SuggestedEnv != "" {
			t.Fatalf("%s suggestedEnv = %q, want empty", adapterID, entry.SuggestedEnv)
		}
	}
	for _, worker := range report.Workers {
		if worker.Outcome != "not-configured" {
			t.Fatalf("worker %s outcome = %q, want not-configured", worker.AdapterID, worker.Outcome)
		}
	}
}

// TestDoctorDiscoverySkipsFailingVersion pins that a candidate whose
// --version execution fails is silently skipped and never suggested.
func TestDoctorDiscoverySkipsFailingVersion(t *testing.T) {
	binDir := t.TempDir()
	hermeticDiscoveryEnvironment(t, binDir)
	broken := filepath.Join(binDir, "qwen")
	if err := os.WriteFile(broken, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	report := runDoctorDiscoveryJSON(t)
	entry := findDiscovery(report.Discovery, "qwen")
	if entry == nil {
		t.Fatalf("discovery missing qwen entry: %+v", report.Discovery)
	}
	if len(entry.Candidates) != 0 || entry.SuggestedEnv != "" {
		t.Fatalf("qwen entry = %+v, want no candidates or suggestion", entry)
	}
}

// TestDoctorDiscoveryIgnoresConfiguredAdapters pins that an adapter whose
// environment variable is set does not participate in discovery, so an
// operator who already registered it sees no discovery noise.
func TestDoctorDiscoveryIgnoresConfiguredAdapters(t *testing.T) {
	binDir := t.TempDir()
	hermeticDiscoveryEnvironment(t, binDir)
	configuredPath, _ := plantBinary(t, t.TempDir(), "qwen", "0.21.5")
	t.Setenv("MARSHAL_QWEN_PATH", configuredPath)
	// Plant a stray candidate on PATH; it must be ignored for qwen.
	plantBinary(t, binDir, "qwen", "0.21.5")

	report := runDoctorDiscoveryJSON(t)
	if findDiscovery(report.Discovery, "qwen") != nil {
		t.Fatalf("configured qwen must not appear in discovery: %+v", report.Discovery)
	}
	for _, worker := range report.Workers {
		if worker.AdapterID == "qwen" && worker.Outcome != "registered" {
			t.Fatalf("qwen worker outcome = %q, want registered", worker.Outcome)
		}
	}
}
