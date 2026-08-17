package codex

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
)

// TestFakeExecutableConformsToCodexCLI proves the fake executable helper
// conforms to the Codex CLI contract the adapter consumes: exact `--version`
// reporting and a well-formed non-interactive JSONL stream that runs
// end-to-end without any real provider or network.
func TestFakeExecutableConformsToCodexCLI(t *testing.T) {
	body := `printf '%s\n' '{"type":"thread.started","thread_id":"thread-1","thread":{"id":"thread-1"}}' '{"type":"item.completed","item":{"id":"i","type":"command","role":"assistant","status":"completed"}}' '{"type":"turn.completed","turn":{"id":"turn-1","status":"completed","usage":{"input_tokens":10,"output_tokens":5}}}'`
	executable := fakeExecutable(t, supportedBinary, body)

	version, digestValue, err := Identify(executable)
	if err != nil {
		t.Fatal(err)
	}
	if version != supportedBinary || !strings.HasPrefix(digestValue, "sha256:") {
		t.Fatalf("identify = %q %q", version, digestValue)
	}

	adapter, err := New(executable, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	probe, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(probe.Data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot["probeStatus"] != "supported" || snapshot["binaryVersion"] != supportedBinary {
		t.Fatalf("probe snapshot = %v", snapshot)
	}

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
	if result.Session == nil || result.Session.ID != "thread-1" || result.Adapter.ID != adapterID {
		t.Fatalf("result = %+v", result)
	}
}

// TestFakeExecutableReportsCodexCLIVersionLine proves the fake binary emits
// the exact official `codex-cli <semver>` contract that readBinaryVersion
// normalizes, so the end-to-end conformance test above exercises the real
// probe path rather than a simplified bare-version path.
func TestFakeExecutableReportsCodexCLIVersionLine(t *testing.T) {
	executable := fakeExecutable(t, supportedBinary, "exit 0")
	output, err := exec.Command(executable, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(output)); got != "codex-cli "+supportedBinary {
		t.Fatalf("fake --version = %q, want %q", got, "codex-cli "+supportedBinary)
	}
}

// fakeExecutable writes a fake Codex CLI binary that answers `--version`
// with the official `codex-cli <version>` line and otherwise runs body as a
// shell script.
func fakeExecutable(t *testing.T, version, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte(fakeScript(version, body)), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeScript(version, body string) string {
	return "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf 'codex-cli %s\\n' '" + version + "'; exit 0; fi\n" + body + "\n"
}
