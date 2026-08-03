package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	marshalSchemas "github.com/chiga0/marshal-harness/schemas"
)

func TestDoctorReportsCompiledContracts(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	exitCode := Run([]string{"doctor", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != ExitOK {
		t.Fatalf("Run() exit = %d, stderr = %s", exitCode, stderr.String())
	}
	var report struct {
		Status          string `json:"status"`
		ContractSchemas int    `json:"contractSchemas"`
		WorkerAdapters  int    `json:"workerAdapters"`
		Milestone       string `json:"milestone"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode doctor output: %v", err)
	}
	if report.Status != "ok" || report.ContractSchemas != 11 || report.WorkerAdapters != 0 || report.Milestone != "0" {
		t.Fatalf("doctor report = %+v", report)
	}
}

func TestContractValidateFromStandardInput(t *testing.T) {
	t.Parallel()

	data, err := marshalSchemas.FS.ReadFile("examples/happy-path/task-spec.json")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exitCode := Run([]string{"contract", "validate", "-"}, bytes.NewReader(data), &stdout, &stderr)
	if exitCode != ExitOK {
		t.Fatalf("Run() exit = %d, stderr = %s", exitCode, stderr.String())
	}
	if got := stdout.String(); got != "有效：Task\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestContractValidateWithExplicitSchema(t *testing.T) {
	t.Parallel()

	task, err := marshalSchemas.FS.ReadFile("examples/happy-path/task-spec.json")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exitCode := Run([]string{"contract", "validate", "--schema", "task-spec", "-"}, bytes.NewReader(task), &stdout, &stderr)
	if exitCode != ExitOK {
		t.Fatalf("valid Task exit = %d, stderr = %s", exitCode, stderr.String())
	}

	runState, err := marshalSchemas.FS.ReadFile("examples/happy-path/run-state.json")
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	exitCode = Run([]string{"contract", "validate", "--schema", "task-spec", "-"}, bytes.NewReader(runState), &stdout, &stderr)
	if exitCode != ExitFailure {
		t.Fatalf("mismatched RunState exit = %d, want %d", exitCode, ExitFailure)
	}
}

func TestReadBoundedRejectsOversizedInput(t *testing.T) {
	t.Parallel()

	if _, err := readBounded(strings.NewReader("12345"), 4); err == nil {
		t.Fatal("readBounded accepted oversized input")
	}
}

func TestTaskSkeletonHasNoFilesystemSideEffects(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	temporaryDirectory := t.TempDir()
	if err := os.Chdir(temporaryDirectory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	for _, command := range taskCommands {
		var stdout, stderr bytes.Buffer
		exitCode := Run([]string{"task", command}, strings.NewReader(""), &stdout, &stderr)
		if exitCode != ExitUnavailable {
			t.Fatalf("task %s exit = %d, want %d", command, exitCode, ExitUnavailable)
		}
	}
	if _, err := os.Stat(filepath.Join(temporaryDirectory, ".marshal")); !os.IsNotExist(err) {
		t.Fatalf("task skeleton created .marshal: %v", err)
	}
}
