//go:build darwin

package verification

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/verificationbuiltin"
	"golang.org/x/sys/unix"
)

func TestVerifierTaskSpecBuiltinUsesPathlessCoreEvidence(t *testing.T) {
	fixture := newVerificationFixture(t)
	raw := taskSpecBuiltinFixture(t)
	relative := "generated/task-spec.json"
	writeBuiltinArtifact(t, fixture.worktree.Path, relative, raw)
	input := fixture.candidateInput()
	input.Scope = ScopePolicy{AllowPaths: []string{"generated/**"}, MaxChangedFiles: 3, MaxDiffBytes: 1 << 20}
	input.Deliverables = []Deliverable{{ID: "task-spec", Kind: "documentation", Required: true, PathGlob: relative, MinimumCount: 1}}
	input.Commands = []CommandSpec{{ID: "validate-task-spec", Argv: []string{verificationbuiltin.TaskSpecV1, "deliverable:task-spec"}, CWD: ".", Timeout: 10 * time.Second, Required: true, MaxLogBytes: 4096, BaselinePolicy: "none"}}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "pass" {
		t.Fatalf("report = %+v", result.Report)
	}
	var commandGate *Gate
	for index := range result.Report.Gates {
		if result.Report.Gates[index].ID == "command:validate-task-spec" {
			commandGate = &result.Report.Gates[index]
		}
	}
	if commandGate == nil || commandGate.Status != "pass" || commandGate.Command == nil {
		t.Fatalf("command gate = %+v", commandGate)
	}
	if commandGate.Command.Executable != verificationbuiltin.TaskSpecV1 || !slices.Equal(commandGate.Command.Argv, []string{verificationbuiltin.TaskSpecV1, "deliverable:task-spec"}) {
		t.Fatalf("command record is not pathless: %+v", commandGate.Command)
	}
	digest := canonical.DigestBytes(raw)
	for _, evidence := range []string{"builtin:contract-task-spec:v1", "deliverable:task-spec", "artifact-digest:" + digest} {
		if !slices.Contains(commandGate.Evidence, evidence) {
			t.Fatalf("evidence %q missing from %+v", evidence, commandGate.Evidence)
		}
	}
	if artifact := findArtifact(t, result.Manifest, "task-spec"); artifact.Digest != digest {
		t.Fatalf("manifest digest = %q, want %q", artifact.Digest, digest)
	} else if artifact.CandidateDigest != result.Report.CandidateDigest || artifact.CandidateDigest == "" {
		t.Fatalf("artifact candidate binding = %q, report = %q", artifact.CandidateDigest, result.Report.CandidateDigest)
	}
	if !slices.Contains(commandGate.Evidence, "candidate:"+result.Report.CandidateDigest) {
		t.Fatalf("candidate evidence missing from %+v", commandGate.Evidence)
	}
	stdout, err := os.ReadFile(filepath.Join(input.RunDirectory, "logs", "validate-task-spec.stdout.log"))
	if err != nil || string(stdout) != builtinTaskSpecValid {
		t.Fatalf("stdout = %q err=%v", stdout, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.worktree.Path, "generated", "unexpected")); !os.IsNotExist(err) {
		t.Fatalf("builtin mutated candidate: %v", err)
	}
}

func TestVerifierTaskSpecBuiltinClosedFailuresDoNotLeakValues(t *testing.T) {
	cases := []struct {
		name   string
		raw    []byte
		reason string
	}{
		{name: "schema", raw: []byte(`{"apiVersion":"marshal.dev/v1alpha1","kind":"Task","private-SUPERSECRET":"never"}`), reason: reasonSchemaInvalid},
		{name: "semantic", raw: taskSpecBuiltinSemanticFailure(t), reason: reasonSemanticInvalid},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newVerificationFixture(t)
			relative := "generated/SUPERSECRET-task.json"
			writeBuiltinArtifact(t, fixture.worktree.Path, relative, test.raw)
			input := fixture.candidateInput()
			input.Scope = ScopePolicy{AllowPaths: []string{"generated/**"}, MaxChangedFiles: 3, MaxDiffBytes: 1 << 20}
			input.Deliverables = []Deliverable{{ID: "task-spec", Kind: "documentation", Required: true, PathGlob: relative, MinimumCount: 1}}
			input.Commands = []CommandSpec{{ID: "validate-task-spec", Argv: []string{verificationbuiltin.TaskSpecV1, "deliverable:task-spec"}, CWD: ".", Timeout: 10 * time.Second, Required: true, MaxLogBytes: 4096, BaselinePolicy: "none"}}
			result, err := New().Verify(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if result.Report.Status != "fail" || gateStatus(result.Report.Gates, "command:validate-task-spec") != "fail" {
				t.Fatalf("report = %+v", result.Report)
			}
			stderr, err := os.ReadFile(filepath.Join(input.RunDirectory, "logs", "validate-task-spec.stderr.log"))
			if err != nil || string(stderr) != test.reason+"\n" {
				t.Fatalf("stderr = %q err=%v", stderr, err)
			}
			if strings.Contains(string(stderr), "SUPERSECRET") || strings.Contains(string(stderr), relative) {
				t.Fatalf("closed diagnostic leaked user material: %q", stderr)
			}
		})
	}
}

func TestVerifierTaskSpecBuiltinRejectsCandidateSwapAndStaleResult(t *testing.T) {
	for _, phase := range []string{"before-authority", "after-execution"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newVerificationFixture(t)
			raw := taskSpecBuiltinFixture(t)
			relative := "generated/task-spec.json"
			writeBuiltinArtifact(t, fixture.worktree.Path, relative, raw)
			input := fixture.candidateInput()
			input.Scope = ScopePolicy{AllowPaths: []string{"generated/**"}, MaxChangedFiles: 3, MaxDiffBytes: 1 << 20}
			input.Deliverables = []Deliverable{{ID: "task-spec", Kind: "documentation", Required: true, PathGlob: relative, MinimumCount: 1}}
			input.Commands = []CommandSpec{{ID: "validate-task-spec", Argv: []string{verificationbuiltin.TaskSpecV1, "deliverable:task-spec"}, CWD: ".", Timeout: 10 * time.Second, Required: true, MaxLogBytes: 4096, BaselinePolicy: "none"}}
			inject := func(store *localCandidateStore, currentInput Input, observation Observation, predecessor string) {
				t.Helper()
				payload := append(append([]byte(nil), observation.Patch...), []byte("hostile replay")...)
				record, err := buildCandidate(currentInput, domain.ProducerKindNormalizer, candidateProducerNormalizer, canonical.DigestBytes(payload), predecessor, time.Now().Add(time.Second))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.Admit(record, payload); err != nil {
					t.Fatal(err)
				}
			}
			verifier := New()
			if phase == "before-authority" {
				verifier.hooks.beforeAuthorityCheck = inject
			} else {
				verifier.hooks.afterExecution = inject
			}
			result, err := verifier.Verify(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			var commandGate Gate
			for _, gate := range result.Report.Gates {
				if gate.ID == "command:validate-task-spec" {
					commandGate = gate
				}
			}
			if commandGate.Status != "error" || commandGate.Summary != reasonArtifactDenied || commandGate.Command == nil || commandGate.Command.Executable != verificationbuiltin.TaskSpecV1 {
				t.Fatalf("command gate = %+v", commandGate)
			}
			for _, evidence := range commandGate.Evidence {
				if strings.HasPrefix(evidence, "artifact-digest:") || strings.HasPrefix(evidence, "candidate:") {
					t.Fatalf("stale/forged evidence survived: %+v", commandGate.Evidence)
				}
			}
			stderr, err := os.ReadFile(filepath.Join(input.RunDirectory, "logs", "validate-task-spec.stderr.log"))
			if err != nil || string(stderr) != reasonArtifactDenied+"\n" {
				t.Fatalf("stderr = %q err=%v", stderr, err)
			}
		})
	}
}

func TestTaskSpecBuiltinHeldReaderRejectsSizeSymlinkAndCurrentNameABA(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "out"), 0o700); err != nil {
		t.Fatal(err)
	}
	valid := taskSpecBuiltinFixture(t)
	writeBuiltinArtifact(t, root, "out/task.json", valid)
	if _, reason := readTaskSpecBuiltinArtifact(context.Background(), root, "out/task.json", builtinArtifactReadHooks{}); reason != "" {
		t.Fatalf("valid held read reason = %q", reason)
	}
	writeBuiltinArtifact(t, root, "out/large.json", make([]byte, builtinTaskSpecMaxBytes+1))
	if _, reason := readTaskSpecBuiltinArtifact(context.Background(), root, "out/large.json", builtinArtifactReadHooks{}); reason != reasonArtifactTooLarge {
		t.Fatalf("large reason = %q", reason)
	}
	if err := os.Symlink("task.json", filepath.Join(root, "out", "link.json")); err != nil {
		t.Fatal(err)
	}
	if _, reason := readTaskSpecBuiltinArtifact(context.Background(), root, "out/link.json", builtinArtifactReadHooks{}); reason != reasonArtifactDenied {
		t.Fatalf("symlink reason = %q", reason)
	}
	original := filepath.Join(root, "out", "task.json")
	old := filepath.Join(root, "out", "task.old")
	hooks := builtinArtifactReadHooks{afterLeafOpen: func() {
		if err := os.Rename(original, old); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(original, valid, 0o600); err != nil {
			t.Fatal(err)
		}
	}}
	if _, reason := readTaskSpecBuiltinArtifact(context.Background(), root, "out/task.json", hooks); reason != reasonArtifactDenied {
		t.Fatalf("leaf current-name ABA reason = %q", reason)
	}
}

func TestTaskSpecBuiltinHeldReaderHostileObjectMatrix(t *testing.T) {
	valid := taskSpecBuiltinFixture(t)
	newFixture := func(t *testing.T) (string, string) {
		t.Helper()
		// Darwin limits sockaddr_un paths to roughly 104 bytes. Go's default
		// per-test temporary path includes the full nested test name and can
		// exceed that limit before the hostile socket is even created. Keep the
		// fixture under the real short private temp root; the production reader
		// still receives the exact absolute root and exercises the same held
		// descriptor boundary.
		root, err := os.MkdirTemp("/private/tmp", "marshal-builtin-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.RemoveAll(root); err != nil {
				t.Errorf("remove builtin fixture: %v", err)
			}
		})
		relative := "out/task.json"
		writeBuiltinArtifact(t, root, relative, valid)
		return root, relative
	}
	t.Run("parent-symlink", func(t *testing.T) {
		root, _ := newFixture(t)
		if err := os.Symlink("out", filepath.Join(root, "alias")); err != nil {
			t.Fatal(err)
		}
		if _, reason := readTaskSpecBuiltinArtifact(context.Background(), root, "alias/task.json", builtinArtifactReadHooks{}); reason != reasonArtifactDenied {
			t.Fatalf("reason = %q", reason)
		}
	})
	for _, object := range []string{"directory", "fifo", "socket"} {
		t.Run(object, func(t *testing.T) {
			root, _ := newFixture(t)
			path := filepath.Join(root, "out", object)
			switch object {
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "socket":
				listener, err := net.Listen("unix", path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = listener.Close() })
			}
			if _, reason := readTaskSpecBuiltinArtifact(context.Background(), root, "out/"+object, builtinArtifactReadHooks{}); reason != reasonArtifactDenied {
				t.Fatalf("reason = %q", reason)
			}
		})
	}
	t.Run("hardlink", func(t *testing.T) {
		root, relative := newFixture(t)
		if err := os.Link(filepath.Join(root, relative), filepath.Join(root, "out", "hard.json")); err != nil {
			t.Fatal(err)
		}
		if _, reason := readTaskSpecBuiltinArtifact(context.Background(), root, relative, builtinArtifactReadHooks{}); reason != reasonArtifactDenied {
			t.Fatalf("reason = %q", reason)
		}
	})
	for _, mutation := range []string{"truncate", "growth"} {
		t.Run(mutation, func(t *testing.T) {
			root, relative := newFixture(t)
			path := filepath.Join(root, relative)
			hooks := builtinArtifactReadHooks{afterLeafOpen: func() {
				if mutation == "truncate" {
					if err := os.Truncate(path, 1); err != nil {
						t.Fatal(err)
					}
				} else {
					file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := file.WriteString("growth"); err != nil {
						_ = file.Close()
						t.Fatal(err)
					}
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
				}
			}}
			if _, reason := readTaskSpecBuiltinArtifact(context.Background(), root, relative, hooks); reason != reasonArtifactDenied {
				t.Fatalf("reason = %q", reason)
			}
		})
	}
	t.Run("parent-current-name", func(t *testing.T) {
		root, relative := newFixture(t)
		parent := filepath.Join(root, "out")
		hooks := builtinArtifactReadHooks{afterLeafOpen: func() {
			if err := os.Rename(parent, filepath.Join(root, "out-old")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			writeBuiltinArtifact(t, root, relative, valid)
		}}
		if _, reason := readTaskSpecBuiltinArtifact(context.Background(), root, relative, hooks); reason != reasonArtifactDenied {
			t.Fatalf("reason = %q", reason)
		}
	})
	t.Run("same-inode-rename-away-restore-aba", func(t *testing.T) {
		root, relative := newFixture(t)
		path := filepath.Join(root, relative)
		away := filepath.Join(root, "out", "task-away.json")
		var before unix.Stat_t
		if err := unix.Stat(path, &before); err != nil {
			t.Fatal(err)
		}
		hooks := builtinArtifactReadHooks{beforeFinalRecheck: func() {
			if err := os.Rename(path, away); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(away, path); err != nil {
				t.Fatal(err)
			}
		}}
		if _, reason := readTaskSpecBuiltinArtifact(context.Background(), root, relative, hooks); reason != reasonArtifactDenied {
			t.Fatalf("reason = %q", reason)
		}
		var after unix.Stat_t
		if err := unix.Stat(path, &after); err != nil {
			t.Fatal(err)
		}
		if before.Dev != after.Dev || before.Ino != after.Ino {
			t.Fatalf("fixture did not restore the same inode: before=(%d,%d) after=(%d,%d)", before.Dev, before.Ino, after.Dev, after.Ino)
		}
	})
	t.Run("real-device", func(t *testing.T) {
		if _, reason := readTaskSpecBuiltinArtifact(context.Background(), "/dev", "null", builtinArtifactReadHooks{}); reason != reasonArtifactDenied {
			t.Fatalf("/dev/null reason = %q", reason)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		root, relative := newFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, reason := readTaskSpecBuiltinArtifact(ctx, root, relative, builtinArtifactReadHooks{}); reason != reasonBuiltinTimeout {
			t.Fatalf("reason = %q", reason)
		}
	})
}

func taskSpecBuiltinFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "examples", "happy-path", "task-spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func taskSpecBuiltinSemanticFailure(t *testing.T) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(taskSpecBuiltinFixture(t), &document); err != nil {
		t.Fatal(err)
	}
	repository := document["repository"].(map[string]any)
	repository["path"] = "relative/SUPERSECRET"
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeBuiltinArtifact(t *testing.T, root, relative string, raw []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
