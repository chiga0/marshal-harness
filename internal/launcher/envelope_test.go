package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSealConsumeOneUseEnvelope(t *testing.T) {
	fixture := newFixture(t)
	reference, err := Seal(fixture.stateRoot, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(reference.Path)
	if err != nil || info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		t.Fatalf("envelope mode=%v err=%v", info.Mode(), err)
	}
	runtimeInfo, err := os.Lstat(filepath.Dir(reference.Path))
	if err != nil || runtimeInfo.Mode().Perm() != 0o700 || !runtimeInfo.IsDir() {
		t.Fatalf("runtime mode=%v err=%v", runtimeInfo.Mode(), err)
	}
	envelope, err := Consume(fixture.stateRoot, fixture.consume(reference, fixture.request.Now.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	if envelope.RunID != fixture.request.RunID || envelope.AttemptID != fixture.request.AttemptID || envelope.Executable.Path != fixture.executable || envelope.WorkingDirectory != fixture.worktree || len(envelope.Environment) != 2 {
		t.Fatalf("envelope = %+v", envelope)
	}
	if _, err := os.Lstat(reference.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("consumed envelope remains: %v", err)
	}
	if _, err := Consume(fixture.stateRoot, fixture.consume(reference, fixture.request.Now.Add(time.Second))); err == nil {
		t.Fatal("second consume succeeded")
	}
}

func TestSealRejectsUnsafeInputs(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*testFixture)
	}{
		{name: "expired", configure: func(f *testFixture) { f.request.ExpiresAt = f.request.Now }},
		{name: "relative executable", configure: func(f *testFixture) { f.request.Executable = "worker" }},
		{name: "missing cwd", configure: func(f *testFixture) { f.request.WorkingDirectory = filepath.Join(f.stateRoot, "missing") }},
		{name: "duplicate env", configure: func(f *testFixture) { f.request.Environment = []string{"PATH=/bin", "PATH=/usr/bin"} }},
		{name: "invalid env", configure: func(f *testFixture) { f.request.Environment = []string{"BAD"} }},
		{name: "publisher credential", configure: func(f *testFixture) { f.request.Environment = []string{"GH_TOKEN=secret"} }},
		{name: "argument NUL", configure: func(f *testFixture) { f.request.Arguments = []string{"bad\x00arg"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			test.configure(&fixture)
			if _, err := Seal(fixture.stateRoot, fixture.request); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
}

func TestSealRejectsSymlinkedOrPermissiveRuntime(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		fixture := newFixture(t)
		runtime := fixture.runtimePath()
		target := t.TempDir()
		if err := os.Symlink(target, runtime); err != nil {
			t.Fatal(err)
		}
		if _, err := Seal(fixture.stateRoot, fixture.request); err == nil {
			t.Fatal("symlinked runtime accepted")
		}
	})
	t.Run("permissive", func(t *testing.T) {
		fixture := newFixture(t)
		runtime := fixture.runtimePath()
		if err := os.Mkdir(runtime, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(runtime, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := Seal(fixture.stateRoot, fixture.request); err == nil {
			t.Fatal("permissive runtime accepted")
		}
	})
}

func TestConsumeRejectsTampering(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, f testFixture, reference Reference) ConsumeRequest
	}{
		{name: "wrong digest", mutate: func(t *testing.T, f testFixture, r Reference) ConsumeRequest {
			r.Digest = "sha256:" + strings.Repeat("0", 64)
			return f.consume(r, f.request.Now.Add(time.Second))
		}},
		{name: "wrong identity", mutate: func(t *testing.T, f testFixture, r Reference) ConsumeRequest {
			request := f.consume(r, f.request.Now.Add(time.Second))
			request.AttemptID = "attempt-other"
			return request
		}},
		{name: "expired", mutate: func(t *testing.T, f testFixture, r Reference) ConsumeRequest {
			return f.consume(r, f.request.ExpiresAt)
		}},
		{name: "permissive file", mutate: func(t *testing.T, f testFixture, r Reference) ConsumeRequest {
			if err := os.Chmod(r.Path, 0o644); err != nil {
				t.Fatal(err)
			}
			return f.consume(r, f.request.Now.Add(time.Second))
		}},
		{name: "truncated", mutate: func(t *testing.T, f testFixture, r Reference) ConsumeRequest {
			writeAndDigest(t, r.Path, []byte(`{"version":1`), &r)
			return f.consume(r, f.request.Now.Add(time.Second))
		}},
		{name: "unknown field", mutate: func(t *testing.T, f testFixture, r Reference) ConsumeRequest {
			var raw map[string]any
			data, _ := os.ReadFile(r.Path)
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}
			raw["unknown"] = true
			changed, _ := json.Marshal(raw)
			writeAndDigest(t, r.Path, append(changed, '\n'), &r)
			return f.consume(r, f.request.Now.Add(time.Second))
		}},
		{name: "envelope identity", mutate: func(t *testing.T, f testFixture, r Reference) ConsumeRequest {
			var raw map[string]any
			data, _ := os.ReadFile(r.Path)
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}
			raw["attemptId"] = "attempt-other"
			changed, _ := json.Marshal(raw)
			writeAndDigest(t, r.Path, append(changed, '\n'), &r)
			return f.consume(r, f.request.Now.Add(time.Second))
		}},
		{name: "missing working directory", mutate: func(t *testing.T, f testFixture, r Reference) ConsumeRequest {
			if err := os.Remove(f.worktree); err != nil {
				t.Fatal(err)
			}
			return f.consume(r, f.request.Now.Add(time.Second))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			reference, err := Seal(fixture.stateRoot, fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Consume(fixture.stateRoot, test.mutate(t, fixture, reference)); err == nil {
				t.Fatal("tampered envelope accepted")
			}
		})
	}
}

func TestConsumeRejectsEnvelopeSymlinkAndExecutableReplacement(t *testing.T) {
	t.Run("envelope symlink", func(t *testing.T) {
		fixture := newFixture(t)
		reference, err := Seal(fixture.stateRoot, fixture.request)
		if err != nil {
			t.Fatal(err)
		}
		copyPath := filepath.Join(t.TempDir(), "copy.json")
		data, _ := os.ReadFile(reference.Path)
		if err := os.WriteFile(copyPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(reference.Path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(copyPath, reference.Path); err != nil {
			t.Fatal(err)
		}
		if _, err := Consume(fixture.stateRoot, fixture.consume(reference, fixture.request.Now.Add(time.Second))); err == nil {
			t.Fatal("symlink envelope accepted")
		}
	})
	t.Run("executable replacement", func(t *testing.T) {
		fixture := newFixture(t)
		reference, err := Seal(fixture.stateRoot, fixture.request)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixture.executable, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Consume(fixture.stateRoot, fixture.consume(reference, fixture.request.Now.Add(time.Second))); err == nil {
			t.Fatal("replaced executable accepted")
		}
	})
}

type testFixture struct {
	stateRoot, worktree, executable string
	request                         SealRequest
}

func newFixture(t *testing.T) testFixture {
	t.Helper()
	stateRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runID, attemptID := "run-test", "attempt-test"
	attempt := filepath.Join(stateRoot, "runs", runID, "attempts", attemptID)
	if err := os.MkdirAll(attempt, 0o700); err != nil {
		t.Fatal(err)
	}
	worktree, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "worker")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	request := SealRequest{RunID: runID, AttemptID: attemptID, Executable: executable, Arguments: []string{"--safe", "值"}, WorkingDirectory: worktree, Environment: []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}, Now: now, ExpiresAt: now.Add(time.Minute)}
	return testFixture{stateRoot, worktree, executable, request}
}

func (f testFixture) runtimePath() string {
	return filepath.Join(f.stateRoot, "runs", f.request.RunID, "attempts", f.request.AttemptID, "runtime")
}

func (f testFixture) consume(reference Reference, now time.Time) ConsumeRequest {
	return ConsumeRequest{RunID: f.request.RunID, AttemptID: f.request.AttemptID, Path: reference.Path, ExpectedDigest: reference.Digest, Now: now}
}

func writeAndDigest(t *testing.T, path string, data []byte, reference *Reference) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	reference.Digest = "sha256:" + hex.EncodeToString(digest[:])
}
