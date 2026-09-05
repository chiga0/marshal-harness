package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// Validate the actual renderer output, not a hand-copied approximation. The
// doctor binding is test-only; this test claims Task schema compatibility only.
func TestFixedServerT2TaskRendererContract(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 is required for the T2 renderer contract test")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"marker", "order-quote"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			doctor := filepath.Join(dir, "doctor.json")
			if err := os.WriteFile(doctor, []byte(`{"policyEnvironmentBinding":{"fixture":"not-authority"}}`), 0600); err != nil {
				t.Fatal(err)
			}
			task := filepath.Join(dir, "task.json")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, python, "-I", "-B", filepath.Join(root, "scripts", "fixed-server-t2-task.py"),
				"--repository", root, "--base-ref", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"--task-id", "renderer-contract-task", "--run-id", "renderer-contract-run", "--model", "test/model",
				"--doctor", doctor, "--task-out", task, "--policy-out", filepath.Join(dir, "policy.json"), "--scenario", scenario)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("renderer: %v: %s", err, output)
			}
			data, err := os.ReadFile(task)
			if err != nil {
				t.Fatal(err)
			}
			if err := validator.Validate(domain.KindTask, data); err != nil {
				t.Fatalf("rendered %s Task: %v", scenario, err)
			}
		})
	}
}
