package codex

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

// supportedVersionOutput 是冻结的 --version 输出行。
const supportedVersionOutput = "codex-cli " + "0.145.0"

func TestMain(m *testing.M) {
	unsafePathExecutionForTests = true
	os.Exit(m.Run())
}

func TestProductionPlatformGateIsAuditableAndLeavesNoLauncherSnapshots(t *testing.T) {
	if secureFDExecutionAvailable() {
		t.Skip("platform provides authenticated fd execution")
	}
	before, err := filepath.Glob(filepath.Join(os.TempDir(), ".marshal-codex-launcher-*"))
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "version-executed")
	executable := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\ntouch " + shellQuote(marker) + "\nprintf 'codex-cli 0.145.0\\n'\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	adapter, err := New(executable, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	adapter.unsafePathExecutionForTest = false
	record, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(record.Data), `"probeStatus":"unsupported"`) ||
		!strings.Contains(string(record.Data), "fd-exec") ||
		!strings.Contains(string(record.Data), `"binaryVersion":"unavailable"`) {
		t.Fatalf("platform probe is not auditable: %s", record.Data)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unsupported platform executed the configured Codex pathname")
	}
	if err := adapter.BindConformance(context.Background(), digest("a")); !errors.Is(err, ErrPlatformUnsupported) || !strings.Contains(err.Error(), secureFDPublicReason) || strings.Contains(err.Error(), "signed/privileged launcher ADR") {
		t.Fatalf("BindConformance err = %v, want fixed safe platform error", err)
	}
	fixture := newRunFixture(t, supportedVersionOutput, "exit 0")
	fixture.adapter.unsafePathExecutionForTest = false
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrPlatformUnsupported) || !strings.Contains(err.Error(), secureFDPublicReason) || strings.Contains(err.Error(), "signed/privileged launcher ADR") {
		t.Fatalf("Run err = %v, want fixed safe platform error on %s", err, runtime.GOOS)
	}
	after, err := filepath.Glob(filepath.Join(os.TempDir(), ".marshal-codex-launcher-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("launcher snapshot residue changed: before=%d after=%d", len(before), len(after))
	}
}

func TestNewRequiresExactExecutableAndValidator(t *testing.T) {
	validator := newValidator(t)
	if _, err := New("codex", validator); err == nil {
		t.Fatal("relative executable accepted")
	}
	if _, err := New(t.TempDir()+"/./codex", validator); err == nil {
		t.Fatal("unclean absolute executable accepted")
	}
	executable := fakeExecutable(t, supportedVersionOutput, "exit 0")
	if _, err := New(executable, nil); err == nil {
		t.Fatal("nil validator accepted")
	}
	nonExecutable := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(nonExecutable, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(nonExecutable, validator); err == nil {
		t.Fatal("non-executable file accepted")
	}
	if _, err := New(filepath.Join(t.TempDir(), "missing"), validator); err == nil {
		t.Fatal("missing executable accepted")
	}
}

func TestNewResolvesSymlinkAndPinsRealFile(t *testing.T) {
	realExecutable := fakeExecutable(t, supportedVersionOutput, "exit 0")
	link := filepath.Join(t.TempDir(), "codex-link")
	if err := os.Symlink(realExecutable, link); err != nil {
		t.Fatal(err)
	}
	adapter, err := New(link, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(realExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if adapter.executable != resolved {
		t.Fatalf("executable = %q, want pinned realpath %q", adapter.executable, resolved)
	}
}

func TestProbeFreezesSupportedAndUnsupportedBinary(t *testing.T) {
	probeSnapshot := func(t *testing.T, record domain.Record) map[string]any {
		t.Helper()
		var raw map[string]any
		if err := json.Unmarshal(record.Data, &raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	t.Run("compatible-but-untrusted", func(t *testing.T) {
		for _, version := range []string{"0.145.0", "0.145.1", "0.145.27"} {
			adapter, err := New(fakeExecutable(t, "codex-cli "+version, "exit 0"), newValidator(t))
			if err != nil {
				t.Fatal(err)
			}
			record, err := adapter.Probe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := newValidator(t).Validate(domain.KindCapabilitySnapshot, record.Data); err != nil {
				t.Fatalf("CapabilitySnapshot schema: %v", err)
			}
			raw := probeSnapshot(t, record)
			if raw["probeStatus"] != "unsupported" || raw["binaryVersion"] != version {
				t.Fatalf("snapshot status/version = %v/%v", raw["probeStatus"], raw["binaryVersion"])
			}
			digest, _ := raw["executableDigest"].(string)
			executable, _ := raw["executable"].(string)
			if !strings.HasPrefix(digest, "sha256:") || !filepath.IsAbs(executable) {
				t.Fatalf("identity = %s/%s", digest, executable)
			}
			if probeErrors, _ := raw["probeErrors"].([]any); len(probeErrors) != 1 || !strings.Contains(fmt.Sprint(probeErrors), conformancePendingReason) {
				t.Fatalf("probeErrors must report pending conformance: %v", probeErrors)
			}
			capabilities, _ := raw["capabilities"].(map[string]any)
			if capabilities == nil {
				t.Fatal("capabilities missing")
			}
			// truthful 能力声明：冻结首切片的精确面。
			wantCapabilities := map[string]any{
				"structuredOutput":        []any{"jsonl"},
				"nonInteractiveEdit":      true,
				"sessionPolicies":         []any{"ephemeral"},
				"modelSelection":          true,
				"executionProfiles":       []any{"workspace-write"},
				"nativeBudgets":           []any{},
				"processTreeCancellation": true,
			}
			for key, want := range wantCapabilities {
				if got, ok := capabilities[key]; !ok {
					t.Fatalf("capability %s missing", key)
				} else if jsonString(got) != jsonString(want) {
					t.Fatalf("capability %s = %v, want %v", key, got, want)
				}
			}
		}
	})
	t.Run("unsupported", func(t *testing.T) {
		for _, version := range []string{"0.146.0", "9.9.9"} {
			adapter, err := New(fakeExecutable(t, "codex-cli "+version, "exit 0"), newValidator(t))
			if err != nil {
				t.Fatal(err)
			}
			record, err := adapter.Probe(context.Background())
			if err != nil {
				t.Fatalf("parseable unsupported version must report, not error: %v", err)
			}
			raw := probeSnapshot(t, record)
			if raw["probeStatus"] != "unsupported" || raw["binaryVersion"] != version {
				t.Fatalf("snapshot status/version = %v/%v", raw["probeStatus"], raw["binaryVersion"])
			}
			probeErrors, _ := raw["probeErrors"].([]any)
			if len(probeErrors) != 1 {
				t.Fatalf("probeErrors = %v", probeErrors)
			}
			message := fmt.Sprint(probeErrors)
			if !strings.Contains(message, "outside the admitted compatibility line") {
				t.Fatalf("probeErrors must report the fixed contract mismatch: %v", probeErrors)
			}
		}
	})
	t.Run("unrecognized-version", func(t *testing.T) {
		for _, output := range []string{
			"codex 0.145.0", "codex-cli", "0.145.0", "codex-cli v0.145.0",
			"codex-cli 0.145.01", "codex-cli 0.145.0-beta.1", "codex-cli 0.145.0+build",
		} {
			adapter, err := New(fakeExecutable(t, output, "exit 0"), newValidator(t))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := adapter.Probe(context.Background()); !errors.Is(err, ErrVersionUnrecognized) {
				t.Fatalf("output %q: err = %v, want ErrVersionUnrecognized", output, err)
			}
		}
	})
	t.Run("version-bytes-are-exact", func(t *testing.T) {
		for _, output := range []string{
			"codex-cli 0.145.0",
			" codex-cli 0.145.0\n",
			"codex-cli 0.145.0 \n",
			"codex-cli 0.145.0\r\n",
			"codex-cli 0.145.0\n\n",
		} {
			if _, err := parseBinaryVersion(output); !errors.Is(err, ErrVersionUnrecognized) {
				t.Fatalf("parseBinaryVersion(%q) err = %v, want ErrVersionUnrecognized", output, err)
			}
		}
	})
	t.Run("version-probe-fails", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "codex")
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 3\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		adapter, err := New(path, newValidator(t))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.Probe(context.Background()); err == nil || errors.Is(err, ErrVersionUnrecognized) {
			t.Fatalf("err = %v, want execution failure distinct from unrecognized version", err)
		}
	})
	t.Run("executable-unavailable", func(t *testing.T) {
		adapter, err := New(fakeExecutable(t, supportedVersionOutput, "exit 0"), newValidator(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(adapter.executable); err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.Probe(context.Background()); err == nil || !errors.Is(err, ErrIdentityInvalid) {
			t.Fatalf("err = %v, want unavailable executable", err)
		}
	})
}

func TestOrdinaryUserProbeReportsSupportedWithoutAuthorityEvidence(t *testing.T) {
	adapter, err := NewOrdinaryUser(fakeExecutable(t, supportedVersionOutput, "exit 0"), newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	record, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		ProbeStatus  string   `json:"probeStatus"`
		ProbeErrors  []string `json:"probeErrors"`
		Capabilities struct {
			Notes []string `json:"notes"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(record.Data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ProbeStatus != "supported" || len(snapshot.ProbeErrors) != 0 {
		t.Fatalf("ordinary-user snapshot = %s", record.Data)
	}
	if !slices.Contains(snapshot.Capabilities.Notes, "当前为 ordinary-user：无签名 authority、APAP 凭据或恶意代码沙箱保证。") {
		t.Fatalf("ordinary-user capability note missing: %s", record.Data)
	}
}

func TestProbeVersionOutputHasStrictByteLimit(t *testing.T) {
	adapter, err := New(fakeExecutable(t, strings.Repeat("x", maxVersionBytes+1), "exit 0"), newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Probe(context.Background()); !errors.Is(err, ErrVersionUnrecognized) {
		t.Fatalf("err = %v, want bounded ErrVersionUnrecognized", err)
	}
}

func TestRunRequiresAuthorityBackedConformance(t *testing.T) {
	fixture := newRunFixture(t, supportedVersionOutput, "touch "+shellQuote(filepath.Join(t.TempDir(), "launched")))
	fixture.adapter.mu.Lock()
	fixture.adapter.pinned, fixture.adapter.conformance = nil, nil
	fixture.adapter.mu.Unlock()
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrConformancePending) {
		t.Fatalf("err = %v, want ErrConformancePending", err)
	}
	probe, err := fixture.adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(probe.Data), `"probeStatus":"unsupported"`) || !strings.Contains(string(probe.Data), conformancePendingReason) {
		t.Fatalf("untrusted probe became supported: %s", probe.Data)
	}
}

func TestBindConformanceRequiresSignedAuthorityEvidence(t *testing.T) {
	fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
	fixture.adapter.mu.Lock()
	fixture.adapter.pinned, fixture.adapter.conformance = nil, nil
	fixture.adapter.mu.Unlock()
	snapshot, err := fixture.adapter.inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	identity := snapshot.identity
	snapshot.close()
	if err := fixture.adapter.BindConformance(context.Background(), digest("a")); !errors.Is(err, ErrConformancePending) {
		t.Fatalf("adapter without authority accepted caller digest: %v", err)
	}
	store, evidenceDigest := signedTestAuthority(t, identity)
	fixture.adapter.authority = store
	if err := fixture.adapter.BindConformance(context.Background(), evidenceDigest); err != nil {
		t.Fatal(err)
	}
	probe, err := fixture.adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(probe.Data), `"probeStatus":"unsupported"`) || !strings.Contains(string(probe.Data), `"codex_conformance_pending"`) {
		t.Fatalf("legacy conformance promoted production authority: %s", probe.Data)
	}
	fixture.adapter.mu.Lock()
	expires := fixture.adapter.conformance.validUntil
	fixture.adapter.mu.Unlock()
	fixture.adapter.now = func() time.Time { return expires.Add(time.Second) }
	expiredProbe, err := fixture.adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(expiredProbe.Data), `"probeStatus":"unsupported"`) {
		t.Fatalf("expired conformance remained supported: %s", expiredProbe.Data)
	}
	fixture.adapter.now = time.Now
	fixture.adapter.mu.Lock()
	fixture.adapter.conformance = nil
	fixture.adapter.mu.Unlock()
	path := filepath.Join(store.root, strings.TrimPrefix(evidenceDigest, "sha256:")+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(`"credentialVerified":true`), []byte(`"credentialVerified":false`), 1)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fixture.adapter.BindConformance(context.Background(), evidenceDigest); err == nil {
		t.Fatal("tampered authority evidence was accepted")
	}
}

func TestConformanceEvidenceRejectsExcessiveAgeAndTTL(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	base := ConformanceEvidence{
		RunnerID: "marshal-conformance", RunnerVersion: "1", AdapterVersion: adapterVersion,
		Executable: "/private/codex", ExecutableDigest: digest("a"), BinaryVersion: "0.145.0",
		CapabilitiesDigest: expectedCapabilitiesDigest(), TranscriptDigest: digest("b"), CredentialVerified: true, LiveProtocolVerified: true,
		EventContract: conformanceEventContract, CodexCLIVersion: "0.145.0", ProtocolVersion: codexProtocolVersion,
		PermissionMode: codexPermissionMode, TrustRootKeyID: "root",
	}
	sign := func(evidence ConformanceEvidence) ConformanceEvidence {
		evidence.EvidenceDigest, err = evidence.digest()
		if err != nil {
			t.Fatal(err)
		}
		message, signErr := evidence.signingBytes()
		if signErr != nil {
			t.Fatal(signErr)
		}
		evidence.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, message))
		return evidence
	}
	for _, test := range []struct {
		name       string
		observedAt time.Time
		validUntil time.Time
	}{
		{"stale-observation", now.Add(-maxConformanceAge - time.Second), now.Add(time.Hour)},
		{"excessive-validity-window", now.Add(-time.Minute), now.Add(maxConformanceTTL + time.Hour)},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := base
			evidence.ObservedAt = test.observedAt.Format(time.RFC3339Nano)
			evidence.ValidUntil = test.validUntil.Format(time.RFC3339Nano)
			if err := sign(evidence).validate(now, map[string]ed25519.PublicKey{"root": publicKey}); err == nil {
				t.Fatal("out-of-policy conformance window was accepted")
			}
		})
	}
}

func TestRunRechecksConformanceAtLaunchBoundary(t *testing.T) {
	fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
	fixture.adapter.mu.Lock()
	expires := fixture.adapter.conformance.validUntil
	fixture.adapter.mu.Unlock()
	now := expires.Add(-time.Minute)
	fixture.adapter.now = func() time.Time { return now }
	fixture.adapter.testHook = func(stage string) {
		if stage == "before-command-start" {
			now = expires.Add(time.Second)
		}
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrConformancePending) {
		t.Fatalf("err = %v, want launch-adjacent ErrConformancePending", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.worktree, "capture")); !os.IsNotExist(err) {
		t.Fatal("provider launched after conformance expired during preflight")
	}
}

func TestRunFailsClosedWhenPinnedControlRootIsReplaced(t *testing.T) {
	fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
	retained := fixture.controlRoot + "-retained"
	t.Cleanup(func() { _ = os.RemoveAll(retained) })
	fixture.adapter.testHook = func(stage string) {
		if stage != "before-prompt-read" {
			return
		}
		if err := os.Rename(fixture.controlRoot, retained); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(fixture.controlRoot, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "control root changed") {
		t.Fatalf("err = %v, want pinned control-root replacement rejection", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.worktree, "capture")); !os.IsNotExist(err) {
		t.Fatal("provider launched after the control root pathname was replaced")
	}
}

func TestRunFailsClosedWhenPinnedWorktreeIsReplaced(t *testing.T) {
	fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
	retained := fixture.worktree + "-retained"
	replacement := t.TempDir()
	t.Cleanup(func() { _ = os.RemoveAll(retained) })
	fixture.adapter.testHook = func(stage string) {
		if stage != "before-command-start" {
			return
		}
		if err := os.Rename(fixture.worktree, retained); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(replacement, fixture.worktree); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "worktree changed") {
		t.Fatalf("err = %v, want pinned worktree replacement rejection", err)
	}
	if _, err := os.Stat(filepath.Join(replacement, "capture")); !os.IsNotExist(err) {
		t.Fatal("provider launched in the replacement worktree")
	}
}

func TestRunFailsClosedWhenWorktreeChangesAcrossStart(t *testing.T) {
	fixture := newRunFixture(t, supportedVersionOutput, "sleep 30")
	retained := fixture.worktree + "-retained"
	replacement := t.TempDir()
	t.Cleanup(func() { _ = os.RemoveAll(retained) })
	fixture.adapter.testHook = func(stage string) {
		if stage != "after-command-start" {
			return
		}
		if err := os.Rename(fixture.worktree, retained); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(replacement, fixture.worktree); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "worktree changed during") {
		t.Fatalf("err = %v, want post-Start worktree identity rejection", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("worktree replacement did not promptly terminate the launched process group")
	}
}

func TestRunRejectsWorktreePathChangeAfterLaunchVerification(t *testing.T) {
	fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
	original := fixture.worktree
	retained := original + "-retained"
	replacement := t.TempDir()
	t.Cleanup(func() { _ = os.RemoveAll(retained) })
	fixture.adapter.testHook = func(stage string) {
		if stage != "after-worktree-launch-verify" {
			return
		}
		if err := os.Rename(original, retained); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(replacement, original); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "worktree changed") {
		t.Fatalf("err = %v, want post-Start worktree drift rejection", err)
	}
	if _, err := os.Stat(filepath.Join(retained, "capture")); err != nil {
		t.Fatalf("provider did not stay in the already-started worktree inode: %v", err)
	}
	if _, err := os.Stat(filepath.Join(replacement, "capture")); !os.IsNotExist(err) {
		t.Fatal("provider followed the mutable worktree pathname after Start")
	}
}

func TestRunBindsWorktreeFDDespiteStartABA(t *testing.T) {
	fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
	original := fixture.worktree
	retained := original + "-retained"
	replacement := t.TempDir()
	gate := t.TempDir()
	fixture.adapter.launcherTestGate = gate
	t.Cleanup(func() { _ = os.RemoveAll(retained) })
	errCh := make(chan error, 1)
	go func() {
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		errCh <- err
	}()
	waitForFile(t, filepath.Join(gate, "ready"), 5*time.Second)
	if err := os.Rename(original, retained); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(replacement, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(retained, original); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gate, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run after pathname ABA: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not complete after launcher gate release")
	}
	if _, err := os.Stat(filepath.Join(original, "capture")); err != nil {
		t.Fatalf("provider did not execute in the pinned worktree inode: %v", err)
	}
	if _, err := os.Stat(filepath.Join(replacement, "capture")); !os.IsNotExist(err) {
		t.Fatal("provider followed the transient replacement during ABA")
	}
}

func TestRunUsesAuthenticatedLauncherWhenExecutablePathIsReplaced(t *testing.T) {
	if !secureFDExecutionAvailable() {
		t.Skip("production launcher is unsupported on this platform")
	}
	fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
	useFixtureExecutable(t, &fixture, nativeFakeExecutable(t))
	fixture.adapter.unsafePathExecutionForTest = false
	configured, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(configured)
	if err != nil {
		t.Fatal(err)
	}
	retained := real + "-marshal-retained"
	marker := filepath.Join(t.TempDir(), "replacement-launcher-ran")
	if err := os.Rename(real, retained); err != nil {
		t.Fatal(err)
	}
	restored := false
	defer func() {
		if !restored {
			_ = os.Remove(real)
			_ = os.Rename(retained, real)
		}
	}()
	replacement := "#!/bin/sh\ntouch " + shellQuote(marker) + "\nexit 99\n"
	if err := os.WriteFile(real, []byte(replacement), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
		t.Fatalf("Run through private launcher snapshot: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("replacement os.Executable pathname was launched")
	}
	if err := os.Remove(real); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(retained, real); err != nil {
		t.Fatal(err)
	}
	restored = true
}

func TestRunFailsClosedWhenLauncherCannotCloseWorktreeFD(t *testing.T) {
	secretHome := filepath.Join(t.TempDir(), "credential-home")
	t.Setenv("CODEX_HOME", secretHome)
	fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
	fixture.adapter.launcherTestCloseFailure = true
	_, err := fixture.adapter.Run(context.Background(), fixture.request)
	if err == nil {
		t.Fatal("launcher close failure unexpectedly executed Codex")
	}
	if strings.Contains(err.Error(), secretHome) {
		t.Fatalf("launcher failure leaked CODEX_HOME: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.worktree, "capture")); !os.IsNotExist(err) {
		t.Fatal("Codex executed after the launcher worktree fd close failed")
	}
	metadata, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "codex-transcript-meta.json"))
	if readErr != nil || !strings.Contains(string(metadata), `"exitCode": 126`) {
		t.Fatalf("metadata = %s err=%v, want launcher exit 126", metadata, readErr)
	}
}

func TestRunRejectsControlRootReplacementAfterLaunchVerification(t *testing.T) {
	fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
	original := fixture.controlRoot
	retained := original + "-retained"
	replacement := t.TempDir()
	t.Cleanup(func() { _ = os.RemoveAll(retained) })
	fixture.adapter.testHook = func(stage string) {
		if stage != "after-worktree-launch-verify" {
			return
		}
		if err := os.Rename(original, retained); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(replacement, original); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "control root changed") {
		t.Fatalf("err = %v, want detached control-root rejection", err)
	}
	entries, err := os.ReadDir(replacement)
	if err != nil || len(entries) != 0 {
		t.Fatalf("replacement control root received evidence: entries=%v err=%v", entries, err)
	}
}

func TestProbeTimeoutKillsForkHoldingStdout(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	executable := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nif [ \"${1:-}\" = \"--version\" ]; then (trap '' TERM; while :; do sleep 1; done) & echo $! > " + shellQuote(pidPath) + "; wait; fi\nexit 9\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	adapter, err := New(executable, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := adapter.Probe(context.Background()); err == nil || !errors.Is(err, ErrIdentityInvalid) {
		t.Fatalf("err = %v, want bounded identity probe failure", err)
	}
	if elapsed := time.Since(started); elapsed > probeTimeout+3*time.Second {
		t.Fatalf("probe exceeded watchdog bound: %s", elapsed)
	}
	pidData, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("probe child %d survived process-group timeout", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProbeExecutesSameSnapshotAcrossDigestAndVersion(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "replacement-version-ran")
	executable := fakeExecutable(t, supportedVersionOutput, "exit 0")
	adapter, err := New(executable, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	adapter.testHook = func(stage string) {
		if stage != "after-executable-digest" {
			return
		}
		replacement := "#!/bin/sh\nif [ \"${1:-}\" = \"--version\" ]; then touch " + shellQuote(marker) + "; printf '%s\\n' " + shellQuote(supportedVersionOutput) + "; exit 0; fi\nexit 9\n"
		if err := os.WriteFile(executable, []byte(replacement), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	record, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(record.Data), `"binaryVersion":"0.145.0"`) {
		t.Fatalf("probe = %s", record.Data)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("replacement path was executed between digest and --version")
	}
}

func TestBuildArgsFreezesCapturedSurface(t *testing.T) {
	schemaPath := "/control/codex-output-schema.json"
	resultPath := "/control/output/worker-result.json"
	// 精确相等断言同时冻结参数顺序并证明冻结面之外没有任何额外参数。
	want := []string{
		"--ask-for-approval", "never",
		"-c", sandboxNetworkOverride,
		"exec",
		"--color", "never",
		"--ephemeral",
		"--ignore-user-config",
		"--ignore-rules",
		"--json",
		"--sandbox", "workspace-write",
		"--output-schema", schemaPath,
		"--output-last-message", resultPath,
		"-m", "provider/model",
	}
	args := buildArgs(schemaPath, resultPath, "provider/model")
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	withoutModel := buildArgs(schemaPath, resultPath, "")
	if strings.Join(withoutModel, "\x00") != strings.Join(want[:len(want)-2], "\x00") {
		t.Fatalf("empty model args = %#v", withoutModel)
	}
	// 全局参数必须位于 exec 子命令之前，sandbox 必须是枚举值而非策略对象。
	execIndex := -1
	for index, argument := range args {
		if argument == "exec" {
			execIndex = index
			break
		}
	}
	if execIndex <= 0 {
		t.Fatalf("exec subcommand missing or first: %#v", args)
	}
	if !containsSequence(args[:execIndex], "--ask-for-approval", "never") || !containsSequence(args[:execIndex], "-c", sandboxNetworkOverride) || slices.Contains(args, "-C") || slices.Contains(args, "--cd") {
		t.Fatalf("global arguments are not frozen before the exec subcommand: %#v", args[:execIndex])
	}
	if !containsSequence(args[execIndex:], "--sandbox", "workspace-write") {
		t.Fatalf("exec sandbox must be the workspace-write enum value: %#v", args[execIndex:])
	}
	if slices.Contains(args, "完成 fixture") {
		t.Fatal("prompt must travel through stdin, not argv")
	}
}

func TestEnvironmentReplacementFailsClosed(t *testing.T) {
	secrets := map[string]string{
		"OPENAI_API_KEY": "model-" + "secret", "CODEX_API_KEY": "codex-" + "secret",
		"GITHUB_TOKEN": "publisher-" + "secret", "GH_TOKEN": "gh-" + "secret",
		"AWS_SECRET_ACCESS_KEY": "cloud-" + "secret", "GOOGLE_API_KEY": "google-" + "secret",
		"ANTHROPIC_API_KEY": "anthropic-" + "secret", "DASHSCOPE_API_KEY": "dashscope-" + "secret",
		"HTTP_PROXY": "http://proxy.example", "HTTPS_PROXY": "https://proxy.example",
		"SSH_AUTH_SOCK": "/tmp/agent.sock", "MARSHAL_TASK_ID": "marshal-state",
	}
	for key, value := range secrets {
		t.Setenv(key, value)
	}
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	joined := strings.Join(workerEnvironment(), "\n")
	for key, value := range secrets {
		if strings.Contains(joined, key) || strings.Contains(joined, value) {
			t.Fatalf("worker environment leaked %s", key)
		}
	}
	for _, required := range []string{"CI=1", "GH_PROMPT_DISABLED=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "CODEX_HOME=" + codexHome} {
		if !strings.Contains(joined, required) {
			t.Fatalf("worker environment misses %s: %s", required, joined)
		}
	}
	if strings.Contains(joined, "PWD=") {
		t.Fatalf("worker environment must not reintroduce a mutable workspace pathname: %s", joined)
	}
	probeJoined := strings.Join(probeEnvironment(), "\n")
	for key := range secrets {
		if strings.Contains(probeJoined, key) {
			t.Fatalf("probe environment leaked %s", key)
		}
	}
}

func TestRunHappyPathNormalizesIdentityAndEvidence(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "model-"+"secret")
	t.Setenv("GITHUB_TOKEN", "publisher-"+"secret")
	fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
	record, err := fixture.adapter.Run(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if record.Kind != domain.KindWorkerResult {
		t.Fatalf("kind = %s", record.Kind)
	}
	if err := fixture.validator.Validate(domain.KindWorkerResult, record.Data); err != nil {
		t.Fatal(err)
	}
	var result declaredResult
	if err := json.Unmarshal(record.Data, &result); err != nil {
		t.Fatal(err)
	}
	// 声明中的 executable/version 不作为权威：归一化覆盖为钉住身份。
	if result.Adapter.Executable != fixture.executable || result.Adapter.Version != "0.145.0" || result.Adapter.ID != adapterID || result.Adapter.Model != "provider/model" {
		t.Fatalf("normalized adapter identity = %+v", result.Adapter)
	}
	if result.Session == nil || result.Session.ID != "thread-1" || result.Session.Resumable {
		t.Fatalf("normalized session = %+v, want transcript thread bound with resumable=false", result.Session)
	}
	if result.StartedAt.IsZero() || result.CompletedAt.IsZero() || result.CompletedAt.Before(result.StartedAt) {
		t.Fatalf("invalid times: %s %s", result.StartedAt, result.CompletedAt)
	}
	// 归一化结果必须原子写回 resultPath。
	persisted, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "worker-result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(persisted), `"version":"0.145.0"`) || strings.Contains(string(persisted), "worker-claim") {
		t.Fatalf("persisted result not normalized: %s", persisted)
	}
	assertCapturedInvocation(t, fixture, true)
	assertEvidenceFiles(t, fixture)
}

func TestRunOmitsModelWhenTaskSpecDeclaresNone(t *testing.T) {
	// worker 自述的 model 不作为权威：TaskSpec 未声明时归一化必须置空。
	claimed := strings.Replace(validDeclaredResultJSON(), `"version":"worker-claim"`, `"version":"worker-claim","model":"claimed-by-worker"`, 1)
	fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(claimed))
	fixture.replaceTaskSpec(t, "", nil)
	record, err := fixture.adapter.Run(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	var result declaredResult
	if err := json.Unmarshal(record.Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Adapter.Model != "" {
		t.Fatalf("model must come only from the frozen TaskSpec: %+v", result.Adapter)
	}
	argvData, err := os.ReadFile(filepath.Join(fixture.worktree, "capture", "argv"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(argvData)), "\n") {
		if line == "-m" || line == "--model" {
			t.Fatalf("argv carries a model flag without a declared model: %s", argvData)
		}
	}
}

func assertCapturedInvocation(t *testing.T, fixture runFixture, withModel bool) {
	t.Helper()
	captureDir := filepath.Join(fixture.worktree, "capture")
	argvData, err := os.ReadFile(filepath.Join(captureDir, "argv"))
	if err != nil {
		t.Fatalf("fake did not capture argv: %v", err)
	}
	gotArgs := strings.Split(strings.TrimSpace(string(argvData)), "\n")
	wantArgs := []string{
		"--ask-for-approval", "never",
		"-c", sandboxNetworkOverride,
		"exec",
		"--color", "never",
		"--ephemeral",
		"--ignore-user-config",
		"--ignore-rules",
		"--json",
		"--sandbox", "workspace-write",
		"--output-schema", inheritedFilePath(0),
		"--output-last-message", inheritedFilePath(1),
	}
	if withModel {
		wantArgs = append(wantArgs, "-m", "provider/model")
	}
	// 精确相等既冻结 global/subcommand 排序，也证明没有任何额外参数。
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("captured argv = %#v, want %#v", gotArgs, wantArgs)
	}
	for _, argument := range gotArgs {
		if argument == "完成 fixture" {
			t.Fatal("prompt leaked into argv; it must travel through stdin")
		}
	}
	stdinData, err := os.ReadFile(filepath.Join(captureDir, "stdin"))
	if err != nil {
		t.Fatalf("fake did not capture stdin: %v", err)
	}
	if strings.TrimSpace(string(stdinData)) != "完成 fixture" {
		t.Fatalf("stdin = %q, want frozen prompt", stdinData)
	}
	envData, err := os.ReadFile(filepath.Join(captureDir, "env"))
	if err != nil {
		t.Fatalf("fake did not capture env: %v", err)
	}
	envJoined := string(envData)
	for _, secret := range []string{"OPENAI_API_KEY", "GITHUB_TOKEN", "model-secret", "publisher-secret"} {
		if strings.Contains(envJoined, secret) {
			t.Fatalf("worker environment leaked %s", secret)
		}
	}
	for _, required := range []string{"CI=1", "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0", "PWD=" + fixture.worktree} {
		if !strings.Contains(envJoined, required) {
			t.Fatalf("worker environment misses %s", required)
		}
	}
}

func assertEvidenceFiles(t *testing.T, fixture runFixture) {
	t.Helper()
	outputDir := filepath.Join(fixture.controlRoot, "output")
	transcript, err := os.ReadFile(filepath.Join(outputDir, "codex-transcript.jsonl"))
	if err != nil || !strings.Contains(string(transcript), `"thread_id":"thread-1"`) || !strings.Contains(string(transcript), `"type":"turn.completed"`) {
		t.Fatalf("transcript = %s err=%v", transcript, err)
	}
	metadata, err := os.ReadFile(filepath.Join(outputDir, "codex-transcript-meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(metadata, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["threadId"] != "thread-1" || meta["eventCount"] != float64(4) || meta["turnCount"] != float64(1) || meta["itemCount"] != float64(1) || meta["inputTokens"] != float64(11) || meta["outputTokens"] != float64(7) || meta["outputTruncated"] != false {
		t.Fatalf("metadata = %v", meta)
	}
	digestValue, _ := meta["transcriptDigest"].(string)
	if !strings.HasPrefix(digestValue, "sha256:") {
		t.Fatalf("metadata misses transcript digest: %v", meta)
	}
	// metadata 不得包含 prompt、自由文本或凭据。
	for _, forbidden := range []string{"完成 fixture", "model-" + "secret", "publisher-" + "secret"} {
		if strings.Contains(string(metadata), forbidden) {
			t.Fatalf("metadata leaked %q", forbidden)
		}
	}
	schemaTemp, err := os.ReadFile(filepath.Join(outputDir, "codex-output-schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	durableSchema, err := contract.SchemaDocument("worker-result")
	if err != nil {
		t.Fatal(err)
	}
	providerSchema, err := providerSchemaDocument(durableSchema)
	if err != nil {
		t.Fatal(err)
	}
	if string(schemaTemp) != string(providerSchema) {
		t.Fatal("schema temp is not byte-identical to the Codex provider schema")
	}
	// result 叶子是唯一真实结果来源：--output-last-message 机械指向它，
	// 旧的 codex-last-message.json 不再存在。
	if _, err := os.Lstat(filepath.Join(outputDir, "codex-last-message.json")); !os.IsNotExist(err) {
		t.Fatal("codex-last-message.json must not exist; the result leaf is the single output source")
	}
}

func TestRunRejectsProfileAndPolicyBeforeLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	body := "touch " + shellQuote(marker)
	for _, test := range []struct {
		name      string
		overrides map[string]any
		sentinel  error
		message   string
	}{
		{name: "foreign-adapter", overrides: map[string]any{"adapterId": "qwen"}, message: "does not match"},
		{name: "read-only-profile", overrides: map[string]any{"executionProfile": "read-only"}, message: "does not match"},
		{name: "hardened-profile", overrides: map[string]any{"executionProfile": "hardened"}, message: "does not match"},
		{name: "persist-session", overrides: map[string]any{"sessionPolicy": "persist"}, sentinel: ErrUnsupportedSessionPolicy},
		{name: "resume-session", overrides: map[string]any{"sessionPolicy": "resume"}, sentinel: ErrUnsupportedSessionPolicy},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunFixture(t, supportedVersionOutput, body)
			_, err := fixture.adapter.Run(context.Background(), fixture.requestWith(test.overrides))
			if test.sentinel != nil {
				if !errors.Is(err, test.sentinel) {
					t.Fatalf("err = %v, want %v", err, test.sentinel)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("err = %v, want %q", err, test.message)
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatal("worker process was launched for a rejected profile/policy")
			}
		})
	}
}

func TestRunRejectsUnsupportedVersionBeforeLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, "codex-cli 0.146.0", "touch "+shellQuote(marker))
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("err = %v, want ErrUnsupportedVersion", err)
	} else {
		assertCodexFailure(t, err, port.FailureKindProviderTerminal, port.RetryDispositionDoNotRetry)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unsupported worker process was launched")
	}
}

func TestRunReResolvesIdentityAndRejectsDriftAfterProbe(t *testing.T) {
	fixture := newRunFixture(t, supportedVersionOutput, "exit 0")
	if _, err := fixture.adapter.Probe(context.Background()); err != nil {
		t.Fatalf("probe before drift: %v", err)
	}
	// Probe 之后替换可执行文件内容（版本漂移）必须被 Run 前的重新解析拦截。
	if err := os.WriteFile(fixture.executable, []byte(fakeScript("codex-cli 0.147.0", "exit 0")), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("err = %v, want ErrUnsupportedVersion after version drift", err)
	}
}

func TestRunRejectsSameVersionContentDriftAfterProbe(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, supportedVersionOutput, "exit 0")
	if _, err := fixture.adapter.Probe(context.Background()); err != nil {
		t.Fatalf("probe before drift: %v", err)
	}
	// 版本保持 0.145.0 但内容被替换：digest 漂移必须在启动前 fail closed，
	// worker marker 绝不出现。
	if err := os.WriteFile(fixture.executable, []byte(fakeScript(supportedVersionOutput, "touch "+shellQuote(marker))), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrIdentityDrift) {
		t.Fatalf("err = %v, want ErrIdentityDrift after same-version content drift", err)
	} else {
		assertCodexFailure(t, err, port.FailureKindProtocolInvalid, port.RetryDispositionDoNotRetry)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("drifted worker process was launched")
	}
	if _, err := os.Stat(filepath.Join(fixture.worktree, "capture")); !os.IsNotExist(err) {
		t.Fatal("drifted worker captured an invocation")
	}
}

func TestRunPinsIdentityOnFirstRunAndRejectsLaterDrift(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
	// 未经 Probe 的首次 Run 当场钉住身份，并保持版本闭集校验。
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
		t.Fatalf("first run without probe: %v", err)
	}
	if err := os.WriteFile(fixture.executable, []byte(fakeScript(supportedVersionOutput, "touch "+shellQuote(marker))), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrIdentityDrift) {
		t.Fatalf("err = %v, want ErrIdentityDrift after first-run pin", err)
	} else {
		assertCodexFailure(t, err, port.FailureKindProtocolInvalid, port.RetryDispositionDoNotRetry)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("drifted worker process was launched")
	}
}

func TestRunExecutesVerifiedSnapshotWhenConfiguredPathIsReplacedBeforeStart(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "replacement-worker-ran")
	fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
	fixture.adapter.testHook = func(stage string) {
		if stage == "after-identity-verify" {
			if err := os.WriteFile(fixture.executable, []byte(fakeScript(supportedVersionOutput, "touch "+shellQuote(marker)+"; exit 9")), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("same-version replacement path was executed after identity verification")
	}
}

func TestRunRejectsDeclaredWorkerToolsBeforeLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	t.Run("declared-allowlist", func(t *testing.T) {
		fixture := newRunFixture(t, supportedVersionOutput, "touch "+shellQuote(marker))
		fixture.replaceTaskSpec(t, "provider/model", []string{"read"})
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrUnsupportedWorkerTools) {
			t.Fatalf("err = %v, want ErrUnsupportedWorkerTools", err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("worker process was launched despite a declared tool allowlist")
		}
	})
	t.Run("malformed-declaration", func(t *testing.T) {
		fixture := newRunFixture(t, supportedVersionOutput, "touch "+shellQuote(marker))
		fixture.replaceTaskSpec(t, "provider/model", "read,edit")
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "validate TaskSpec") {
			t.Fatalf("err = %v, want fail-closed TaskSpec schema rejection", err)
		}
	})
}

func TestRunTaskSpecProjectionIsSingleReadAndFailClosed(t *testing.T) {
	t.Run("digest-mismatch", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "launched")
		fixture := newRunFixture(t, supportedVersionOutput, "touch "+shellQuote(marker))
		request := fixture.requestWith(map[string]any{"specDigest": digest("a")})
		if _, err := fixture.adapter.Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), "specDigest mismatch") {
			t.Fatalf("err = %v, want frozen specDigest rejection", err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("worker launched with mismatched TaskSpec")
		}
	})
	t.Run("symlink-leaf", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "launched")
		fixture := newRunFixture(t, supportedVersionOutput, "touch "+shellQuote(marker))
		path := filepath.Join(fixture.controlRoot, "input", "task-spec.json")
		outside := filepath.Join(t.TempDir(), "task.json")
		writeValidTaskSpec(t, outside, "replacement/model", nil)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "read TaskSpec") {
			t.Fatalf("err = %v, want O_NOFOLLOW TaskSpec rejection", err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("worker launched through symlinked TaskSpec")
		}
	})
	t.Run("malformed", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "launched")
		fixture := newRunFixture(t, supportedVersionOutput, "touch "+shellQuote(marker))
		path := filepath.Join(fixture.controlRoot, "input", "task-spec.json")
		if err := os.WriteFile(path, []byte(`{"worker":`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "validate TaskSpec") {
			t.Fatalf("err = %v, want fail-closed TaskSpec validation", err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("worker launched with malformed TaskSpec")
		}
	})
	t.Run("mutation-after-projection", func(t *testing.T) {
		fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
		fixture.adapter.testHook = func(stage string) {
			if stage == "after-task-projection" {
				writeValidTaskSpec(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), "replacement/model", []string{"shell"})
			}
		}
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
			t.Fatal(err)
		}
		assertCapturedInvocation(t, fixture, true)
	})
}

func TestRunRejectsResultLeafBeforeLaunch(t *testing.T) {
	t.Run("symlink-escapes-control-root", func(t *testing.T) {
		outsideDir := t.TempDir()
		sentinel := filepath.Join(outsideDir, "sentinel.json")
		if err := os.WriteFile(sentinel, []byte("sentinel-content"), 0o600); err != nil {
			t.Fatal(err)
		}
		fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
		if err := os.MkdirAll(filepath.Join(fixture.controlRoot, "output"), 0o700); err != nil {
			t.Fatal(err)
		}
		resultPath := filepath.Join(fixture.controlRoot, "output", "worker-result.json")
		if err := os.Symlink(sentinel, resultPath); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		if err == nil || !strings.Contains(err.Error(), "result leaf") {
			t.Fatalf("err = %v, want pre-launch result leaf rejection", err)
		}
		assertCodexFailure(t, err, port.FailureKindProtocolInvalid, port.RetryDispositionDoNotRetry)
		content, readErr := os.ReadFile(sentinel)
		if readErr != nil || string(content) != "sentinel-content" {
			t.Fatalf("outside sentinel was modified: %q err=%v", content, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(fixture.worktree, "capture")); !os.IsNotExist(statErr) {
			t.Fatal("worker was launched despite the pre-existing result symlink")
		}
	})
	t.Run("pre-existing-regular-file", func(t *testing.T) {
		fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
		if err := os.MkdirAll(filepath.Join(fixture.controlRoot, "output"), 0o700); err != nil {
			t.Fatal(err)
		}
		resultPath := filepath.Join(fixture.controlRoot, "output", "worker-result.json")
		if err := os.WriteFile(resultPath, []byte("planted"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		if err == nil || !strings.Contains(err.Error(), "result leaf") {
			t.Fatalf("err = %v, want pre-launch result leaf rejection", err)
		}
		assertCodexFailure(t, err, port.FailureKindProtocolInvalid, port.RetryDispositionDoNotRetry)
		content, readErr := os.ReadFile(resultPath)
		if readErr != nil || string(content) != "planted" {
			t.Fatalf("planted node was modified before launch: %q err=%v", content, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(fixture.worktree, "capture")); !os.IsNotExist(statErr) {
			t.Fatal("worker was launched despite the pre-existing result node")
		}
	})
	t.Run("symlink-on-schema-leaf", func(t *testing.T) {
		outsideDir := t.TempDir()
		sentinel := filepath.Join(outsideDir, "schema-sentinel.json")
		if err := os.WriteFile(sentinel, []byte("sentinel-schema"), 0o600); err != nil {
			t.Fatal(err)
		}
		fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
		if err := os.MkdirAll(filepath.Join(fixture.controlRoot, "output"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(sentinel, filepath.Join(fixture.controlRoot, "output", "codex-output-schema.json")); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		if err == nil || !strings.Contains(err.Error(), "output schema leaf") {
			t.Fatalf("err = %v, want pre-launch schema leaf rejection", err)
		}
		assertCodexFailure(t, err, port.FailureKindProtocolInvalid, port.RetryDispositionDoNotRetry)
		content, readErr := os.ReadFile(sentinel)
		if readErr != nil || string(content) != "sentinel-schema" {
			t.Fatalf("outside sentinel was modified: %q err=%v", content, readErr)
		}
	})
}

func TestRunProtocolFailClosed(t *testing.T) {
	first := `{"type":"thread.started","thread_id":"thread-1"}`
	terminal := `{"type":"turn.completed","thread_id":"thread-1","usage":{"input_tokens":1,"output_tokens":1}}`
	turnStarted := `{"type":"turn.started","thread_id":"thread-1","turn_id":"turn-1"}`
	turnFailed := `{"type":"turn.failed","thread_id":"thread-1","error":"` + "secret-" + `provider-text"}`
	for _, test := range []struct {
		name     string
		lines    []string
		sentinel error
	}{
		{name: "missing-first-event", lines: nil, sentinel: ErrProtocol},
		{name: "first-event-not-thread-started", lines: []string{`{"type":"turn.started","thread_id":"thread-1"}`}, sentinel: ErrProtocol},
		{name: "first-event-missing-identity", lines: []string{`{"type":"thread.started"}`}, sentinel: ErrProtocol},
		{name: "first-event-empty-identity", lines: []string{`{"type":"thread.started","thread_id":""}`}, sentinel: ErrProtocol},
		{name: "malformed-jsonl", lines: []string{"not-json"}, sentinel: ErrProtocol},
		{name: "identity-switch", lines: []string{first, `{"type":"item.completed","thread_id":"thread-2"}`}, sentinel: ErrProtocol},
		{name: "empty-thread-id-after-binding", lines: []string{first, `{"type":"item.completed","thread_id":""}`}, sentinel: ErrProtocol},
		{name: "duplicate-thread-started", lines: []string{first, `{"type":"thread.started","thread_id":"thread-1"}`}, sentinel: ErrProtocol},
		{name: "unknown-event-type", lines: []string{first, `{"type":"weird.event","thread_id":"thread-1"}`}, sentinel: ErrProtocol},
		{name: "unknown-item-evil", lines: []string{first, `{"type":"item.evil","thread_id":"thread-1"}`}, sentinel: ErrProtocol},
		{name: "missing-terminal", lines: []string{first, `{"type":"item.completed","thread_id":"thread-1"}`}, sentinel: ErrProtocol},
		{name: "trailing-after-terminal", lines: []string{first, turnStarted, terminal, `{"type":"item.completed","thread_id":"thread-1"}`}, sentinel: ErrProtocol},
		{name: "item-started-after-terminal", lines: []string{first, turnStarted, terminal, `{"type":"item.started","thread_id":"thread-1"}`}, sentinel: ErrProtocol},
		{name: "turn-failed", lines: []string{first, turnStarted, turnFailed}, sentinel: ErrProviderFailed},
		{name: "failed-after-success-terminal", lines: []string{first, turnStarted, terminal, turnFailed}, sentinel: ErrProtocol},
		{name: "success-after-failed-terminal", lines: []string{first, turnStarted, turnFailed, terminal}, sentinel: ErrProviderFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := transcriptBody(test.lines)
			fixture := newRunFixture(t, supportedVersionOutput, body)
			_, err := fixture.adapter.Run(context.Background(), fixture.request)
			if !errors.Is(err, test.sentinel) {
				t.Fatalf("err = %v, want %v", err, test.sentinel)
			}
			if strings.Contains(err.Error(), "secret-"+"provider-text") {
				t.Fatalf("provider free text leaked into error: %v", err)
			}
			if errors.Is(err, ErrProviderFailed) {
				assertCodexFailure(t, err, port.FailureKindProviderTerminal, port.RetryDispositionDoNotRetry)
			} else {
				assertCodexFailure(t, err, port.FailureKindProtocolInvalid, port.RetryDispositionDoNotRetry)
			}
			if _, statErr := os.Stat(filepath.Join(fixture.controlRoot, "output", "codex-transcript.jsonl")); statErr != nil {
				t.Fatal("transcript evidence was not preserved on protocol failure")
			}
		})
	}
}

func TestRunResultFailClosed(t *testing.T) {
	valid := validDeclaredResultJSON()
	for _, test := range []struct {
		name        string
		resultWrite string
		message     string
	}{
		{name: "missing-result", resultWrite: "", message: "WorkerResult declaration"},
		{name: "empty-result", resultWrite: `: > "$result_path"`, message: "WorkerResult declaration"},
		{name: "malformed-json", resultWrite: resultHeredoc("{not-json"), message: "WorkerResult declaration"},
		{name: "oversize-result", resultWrite: `head -c 4194305 /dev/zero | tr '\0' 'a' > "$result_path"`, message: "WorkerResult declaration"},
		{name: "unknown-field", resultWrite: resultHeredoc(strings.Replace(valid, `"kind":"WorkerResult",`, `"kind":"WorkerResult","extraField":true,`, 1)), message: "WorkerResult declaration"},
		{name: "absolute-changed-path", resultWrite: resultHeredoc(strings.Replace(valid, `"declaredChangedFiles":["file.txt"]`, `"declaredChangedFiles":["/etc/passwd"]`, 1)), message: "WorkerResult declaration"},
		{name: "task-id-mismatch", resultWrite: resultHeredoc(strings.Replace(valid, `"taskId":"TASK-1"`, `"taskId":"OTHER"`, 1)), message: "identity"},
		{name: "run-id-mismatch", resultWrite: resultHeredoc(strings.Replace(valid, `"runId":"run-1"`, `"runId":"OTHER"`, 1)), message: "identity"},
		{name: "attempt-id-mismatch", resultWrite: resultHeredoc(strings.Replace(valid, `"attemptId":"attempt-1"`, `"attemptId":"OTHER"`, 1)), message: "identity"},
		{name: "adapter-id-mismatch", resultWrite: resultHeredoc(strings.Replace(valid, `"adapter":{"id":"codex"`, `"adapter":{"id":"qwen"`, 1)), message: "identity"},
		{name: "session-mismatch", resultWrite: resultHeredoc(strings.Replace(valid, `"session":{"id":"thread-1"`, `"session":{"id":"other-thread"`, 1)), message: "session does not match transcript"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := transcriptBody(successTranscriptLines()) + test.resultWrite
			fixture := newRunFixture(t, supportedVersionOutput, body)
			if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("err = %v, want failure containing %q", err, test.message)
			} else if strings.Contains(test.name, "mismatch") {
				assertCodexFailure(t, err, port.FailureKindProtocolInvalid, port.RetryDispositionDoNotRetry)
			} else {
				assertCodexFailure(t, err, port.FailureKindResultMissing, port.RetryDispositionRetryable)
			}
		})
	}
}

func TestRunEnforcesOutputCap(t *testing.T) {
	t.Run("large-event", func(t *testing.T) {
		large := strings.Repeat("x", 1800)
		body := transcriptBody([]string{
			`{"type":"thread.started","thread_id":"thread-1"}`,
			`{"type":"item.completed","thread_id":"thread-1","item":{"type":"agent_message","text":"` + large + `"}}`,
		})
		fixture := newRunFixture(t, supportedVersionOutput, body)
		if _, err := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"maxOutputBytes": 1024})); !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("err = %v, want ErrOutputLimit", err)
		}
	})
	t.Run("unterminated-line", func(t *testing.T) {
		fixture := newRunFixture(t, supportedVersionOutput, `yes x | tr -d '\n'`)
		started := time.Time{}
		fixture.adapter.testHook = func(stage string) {
			if stage == "after-evidence-claim" {
				started = time.Now()
			}
		}
		if _, err := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"maxOutputBytes": 1024})); !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("err = %v, want ErrOutputLimit", err)
		}
		if started.IsZero() || time.Since(started) > 5*time.Second {
			t.Fatal("unterminated line was not terminated at the byte limit promptly")
		}
	})
}

func TestRunTimeoutReturnsContextDeadlineAndPersistsEvidence(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	// body：先落启动 handshake，再输出首个事件并长睡以触发 attempt 超时。
	// handshake 使测试能确定性区分 pre-start deadline 与已启动 worker 的
	// timeout：只有确认 worker 已启动后才断言 transcript。
	body := "touch " + shellQuote(started) + "\n" +
		transcriptBody([]string{`{"type":"thread.started","thread_id":"thread-1"}`}) +
		"sleep 30\n"
	fixture := newRunFixture(t, supportedVersionOutput, body)
	preflightDone := make(chan struct{})
	fixture.adapter.testHook = func(stage string) {
		if stage == "after-evidence-claim" {
			close(preflightDone)
		}
	}
	done := make(chan error, 1)
	go func() {
		_, runErr := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"attemptTimeoutSeconds": 1}))
		done <- runErr
	}()
	select {
	case <-preflightDone:
	case <-time.After(30 * time.Second):
		t.Fatal("Run preflight did not complete")
	}
	waitForFile(t, started, 5*time.Second)
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return promptly after the attempt timeout")
	}
	// DeadlineExceeded 之后 transcript 与结构化 metadata 必须已落盘。
	transcript, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "codex-transcript.jsonl"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(transcript), `"thread_id":"thread-1"`) {
		t.Fatalf("timeout transcript lost the captured event: %s", transcript)
	}
	metadata, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "codex-transcript-meta.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(metadata), `"contextError": "context deadline exceeded"`) {
		t.Fatalf("metadata lost context classification: %s", metadata)
	}
	if !strings.Contains(string(metadata), `"threadId": "thread-1"`) || !strings.Contains(string(metadata), `"exitCode"`) {
		t.Fatalf("metadata lost structured accounting on timeout: %s", metadata)
	}
}

func TestRunPreStartDeadlineReturnsWithoutEvidence(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	body := "touch " + shellQuote(started) + "\n" +
		transcriptBody([]string{`{"type":"thread.started","thread_id":"thread-1"}`})
	fixture := newRunFixture(t, supportedVersionOutput, body)
	// 调用方 context 在 Run 入口前已过期：preflight 的 version probe 在
	// worker 启动前即失败，绝不启动 worker、绝不伪造 transcript。
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := fixture.adapter.Run(ctx, fixture.requestWith(map[string]any{"attemptTimeoutSeconds": 15})); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if _, statErr := os.Stat(started); !os.IsNotExist(statErr) {
		t.Fatal("worker was started despite an already-expired caller context")
	}
	if _, statErr := os.Stat(filepath.Join(fixture.controlRoot, "output", "codex-transcript.jsonl")); !os.IsNotExist(statErr) {
		t.Fatal("transcript evidence fabricated for a never-started worker")
	}
}

func TestRunRejectsBypassedResultNotBoundToOutputArg(t *testing.T) {
	// 声明被写入一个测试硬编码的旁路路径，而不是 argv 绑定的
	// --output-last-message 目标：Run 只信任同一叶子，旁路结果不能成功。
	bypassPath := filepath.Join(t.TempDir(), "bypass-result.json")
	body := transcriptBody(successTranscriptLines()) +
		"cat > " + shellQuote(bypassPath) + " <<'CODEX_BYPASS_EOF'\n" + validDeclaredResultJSON() + "\nCODEX_BYPASS_EOF\n"
	fixture := newRunFixture(t, supportedVersionOutput, body)
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "WorkerResult declaration") {
		t.Fatalf("err = %v, want bypassed result to fail as a missing declaration", err)
	}
	if _, err := os.ReadFile(bypassPath); err != nil {
		t.Fatalf("bypass file was not written by the fake: %v", err)
	}
}

func TestRunCancellationTerminatesProcessGroup(t *testing.T) {
	handshake := t.TempDir()
	pidFile := filepath.Join(handshake, "child.pid")
	readyFile := filepath.Join(handshake, "ready")
	body := "sleep 60 &\nchild=$!\nprintf '%s' \"$child\" > " + shellQuote(pidFile+".tmp") + " && mv " + shellQuote(pidFile+".tmp") + " " + shellQuote(pidFile) + "\n: > " + shellQuote(readyFile+".tmp") + " && mv " + shellQuote(readyFile+".tmp") + " " + shellQuote(readyFile) + "\nwait"
	fixture := newRunFixture(t, supportedVersionOutput, body)
	preflightDone := make(chan struct{})
	fixture.adapter.testHook = func(stage string) {
		if stage == "after-evidence-claim" {
			close(preflightDone)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, runErr := fixture.adapter.Run(ctx, fixture.requestWith(map[string]any{"attemptTimeoutSeconds": 15}))
		errCh <- runErr
	}()
	select {
	case <-preflightDone:
	case <-time.After(30 * time.Second):
		t.Fatal("Run preflight did not complete")
	}
	waitForFile(t, readyFile, 5*time.Second)
	pidData, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		t.Fatalf("pid file = %q: %v", pidData, err)
	}
	cancel()
	select {
	case runErr := <-errCh:
		if !errors.Is(runErr, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return promptly after cancellation")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background child %d survived process-group cancellation", pid)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunProcessFailureNeverLeaksStderrIntoError(t *testing.T) {
	secrets := []string{"sensitive-output-marker-alpha", "private-output-marker-beta", "user-output-marker-gamma"}
	// 协议完整结束后进程才以非零退出：进程失败分类优先于结果读取。
	body := transcriptBody(successTranscriptLines())
	for _, secret := range secrets {
		body += "printf '%s\\n' " + shellQuote(secret) + " >&2\n"
	}
	body += "exit 7\n"
	fixture := newRunFixture(t, supportedVersionOutput, body)
	_, err := fixture.adapter.Run(context.Background(), fixture.request)
	if !errors.Is(err, ErrProcessFailed) {
		t.Fatalf("err = %v, want ErrProcessFailed", err)
	}
	failure, ok := port.AsAdapterFailure(err)
	if !ok || failure.Adapter != port.AdapterIDCodex || failure.Kind != port.FailureKindProviderTerminal || failure.Disposition != port.RetryDispositionDoNotRetry {
		t.Fatalf("err = %v, want typed codex provider-terminal/do-not-retry", err)
	}
	for _, secret := range secrets {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("provider stderr leaked into error: %v", err)
		}
	}
	evidence, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "codex-stderr.log"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	metadata, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "codex-transcript-meta.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, secret := range secrets {
		if !strings.Contains(string(evidence), secret) {
			t.Fatalf("bounded stderr evidence file lost %q", secret)
		}
		if strings.Contains(string(metadata), secret) {
			t.Fatalf("metadata leaked stderr content %q", secret)
		}
	}
	if !strings.Contains(string(metadata), `"exitCode": 7`) || !strings.Contains(string(metadata), `"stderrBytes"`) {
		t.Fatalf("metadata lost process/stderr accounting: %s", metadata)
	}
}

func TestRunRejectsEvidenceDirectoryEscape(t *testing.T) {
	fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
	outside := t.TempDir()
	if err := os.RemoveAll(filepath.Join(fixture.controlRoot, "output")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(fixture.controlRoot, "output")); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.adapter.Run(context.Background(), fixture.request)
	if err == nil || !strings.Contains(err.Error(), "escapes the control root") {
		t.Fatalf("err = %v, want evidence directory containment failure", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("evidence was written outside the control root: %v", entries)
	}
}

func TestRunRejectsSymlinkedMissingEvidenceSuffix(t *testing.T) {
	fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
	outsideParent := t.TempDir()
	missingOutside := filepath.Join(outsideParent, "missing", "nested")
	link := filepath.Join(fixture.controlRoot, "linked-output")
	if err := os.Symlink(missingOutside, link); err != nil {
		t.Fatal(err)
	}
	request := fixture.requestWith(map[string]any{"resultPath": "linked-output/deeper/worker-result.json"})
	_, err := fixture.adapter.Run(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "evidence path") {
		t.Fatalf("err = %v, want symlinked missing suffix rejection", err)
	}
	assertCodexFailure(t, err, port.FailureKindProtocolInvalid, port.RetryDispositionDoNotRetry)
	if _, statErr := os.Stat(filepath.Join(outsideParent, "missing")); !os.IsNotExist(statErr) {
		t.Fatal("missing outside suffix was created through a symlink")
	}
}

func TestRunCreatesAndContainsMissingEvidenceSuffix(t *testing.T) {
	fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
	request := fixture.requestWith(map[string]any{"resultPath": "new/deep/output/worker-result.json"})
	if _, err := fixture.adapter.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"worker-result.json", "codex-output-schema.json", "codex-transcript.jsonl"} {
		path := filepath.Join(fixture.controlRoot, "new", "deep", "output", name)
		real, err := filepath.EvalSymlinks(path)
		if err != nil || !strings.HasPrefix(real, fixture.controlRoot+string(filepath.Separator)) {
			t.Fatalf("evidence %s escaped: real=%q err=%v", name, real, err)
		}
	}
}

func TestRunRejectsEvidenceDirectoryReplacementAfterClaim(t *testing.T) {
	fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
	outside := t.TempDir()
	output := filepath.Join(fixture.controlRoot, "output")
	retained := filepath.Join(fixture.controlRoot, "claimed-output")
	fixture.adapter.testHook = func(stage string) {
		if stage != "after-evidence-claim" {
			return
		}
		if err := os.Rename(output, retained); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, output); err != nil {
			t.Fatal(err)
		}
	}
	_, err := fixture.adapter.Run(context.Background(), fixture.request)
	if err == nil || !strings.Contains(err.Error(), "containment changed") {
		t.Fatalf("err = %v, want replaced evidence directory rejection", err)
	}
	assertCodexFailure(t, err, port.FailureKindProtocolInvalid, port.RetryDispositionDoNotRetry)
	entries, readErr := os.ReadDir(outside)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("replacement directory received evidence: entries=%v err=%v", entries, readErr)
	}
}

func TestRunRejectsResultLeafReplacementAfterClaim(t *testing.T) {
	fixture := newRunFixture(t, supportedVersionOutput, successBodyWithResult(validDeclaredResultJSON()))
	result := filepath.Join(fixture.controlRoot, "output", "worker-result.json")
	retained := filepath.Join(fixture.controlRoot, "output", "claimed-worker-result.json")
	outside := filepath.Join(t.TempDir(), "outside-result.json")
	if err := os.WriteFile(outside, []byte("outside-sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.adapter.testHook = func(stage string) {
		if stage != "after-evidence-claim" {
			return
		}
		if err := os.Rename(result, retained); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, result); err != nil {
			t.Fatal(err)
		}
	}
	_, err := fixture.adapter.Run(context.Background(), fixture.request)
	if err == nil || !strings.Contains(err.Error(), "containment changed") {
		t.Fatalf("err = %v, want replaced result leaf rejection", err)
	}
	assertCodexFailure(t, err, port.FailureKindProtocolInvalid, port.RetryDispositionDoNotRetry)
	content, readErr := os.ReadFile(outside)
	if readErr != nil || string(content) != "outside-sentinel" {
		t.Fatalf("outside result was modified: %q err=%v", content, readErr)
	}
}

// ---------- fixtures ----------

type runFixture struct {
	adapter                           *Adapter
	validator                         *contract.Validator
	executable, worktree, controlRoot string
	request                           domain.Record
}

func newRunFixture(t *testing.T, versionOutput, body string) runFixture {
	t.Helper()
	worktree := t.TempDir()
	controlRoot := t.TempDir()
	resolvedWorktree, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	resolvedControlRoot, err := filepath.EvalSymlinks(controlRoot)
	if err != nil {
		t.Fatal(err)
	}
	executable := fakeExecutable(t, versionOutput, body)
	validator := newValidator(t)
	adapter, err := New(executable, validator)
	if err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(controlRoot, "input", "task-spec.json")
	specDigest := writeValidTaskSpec(t, taskPath, "provider/model", nil)
	promptPath := filepath.Join(controlRoot, "input", "prompt.md")
	if err := os.MkdirAll(filepath.Dir(promptPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(promptPath, []byte("完成 fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	requestData := map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "WorkerRequest", "taskId": "TASK-1", "runId": "run-1", "attemptId": "attempt-1", "attemptNumber": 1,
		"specDigest": specDigest, "policyDigest": digest("b"), "capabilityDigest": digest("c"), "baseSha": strings.Repeat("1", 40),
		"worktreePath": resolvedWorktree, "controlRoot": resolvedControlRoot, "taskSpecPath": "input/task-spec.json", "promptPath": "input/prompt.md", "resultPath": "output/worker-result.json",
		"adapterId": "codex", "executionProfile": "workspace-write", "sessionPolicy": "ephemeral", "attemptTimeoutSeconds": 5, "maxOutputBytes": 65536, "reviewFindings": []any{},
	}
	requestBytes, err := json.Marshal(requestData)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := adapter.inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	identity := snapshot.identity
	snapshot.close()
	pinned := identity
	adapter.pinned = &pinned
	adapter.conformance = &boundConformance{identity: identity, validUntil: time.Now().UTC().Add(time.Hour), evidenceDigest: digest("f")}
	return runFixture{adapter, validator, adapter.executable, resolvedWorktree, resolvedControlRoot, domain.Record{Kind: domain.KindWorkerRequest, Data: requestBytes}}
}

func writeValidTaskSpec(t *testing.T, path, model string, tools any) string {
	t.Helper()
	worker := map[string]any{"preferredAdapter": "codex", "fallbackAdapters": []any{}, "executionProfile": "workspace-write", "sessionPolicy": "ephemeral"}
	if model != "" {
		worker["model"] = model
	}
	if tools != nil {
		worker["tools"] = tools
	}
	spec := map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "Task",
		"metadata":     map[string]any{"id": "TASK-1", "title": "Codex fixture"},
		"repository":   map[string]any{"path": "/tmp/repository", "baseRef": "main", "remote": "origin"},
		"work":         map[string]any{"objective": "执行 Codex fixture。"},
		"scope":        map[string]any{"allowPaths": []any{"**"}, "denyPaths": []any{}, "allowSubmodules": false},
		"acceptance":   map[string]any{"commands": []any{}, "allowNoChange": true},
		"deliverables": []any{map[string]any{"id": "result", "kind": "diagnostic", "required": true}},
		"worker":       worker,
		"budgets":      map[string]any{"runTimeoutSeconds": 60, "attemptTimeoutSeconds": 30, "maxAttempts": 1, "maxOperationalRetries": 0, "maxReworkRounds": 0, "maxOutputBytes": 65536},
		"publication":  map[string]any{"required": false, "provider": "none", "mode": "none", "remote": "origin", "baseBranch": "main", "mergePolicy": "never", "requiredChecks": []any{}},
	}
	writeJSON(t, path, spec)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := canonical.DigestJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func (f *runFixture) replaceTaskSpec(t *testing.T, model string, tools any) {
	digest := writeValidTaskSpec(t, filepath.Join(f.controlRoot, "input", "task-spec.json"), model, tools)
	f.request = f.requestWith(map[string]any{"specDigest": digest})
}

func (f runFixture) requestWith(overrides map[string]any) domain.Record {
	data := map[string]any{}
	var source map[string]any
	if err := json.Unmarshal(f.request.Data, &source); err != nil {
		panic(err)
	}
	for key, value := range source {
		data[key] = value
	}
	for key, value := range overrides {
		data[key] = value
	}
	requestBytes, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	return domain.Record{Kind: domain.KindWorkerRequest, Data: requestBytes}
}

// validDeclaredResultJSON 是 fake worker 声明的 WorkerResult：executable/
// version/session 均不可信，用于验证归一化覆盖。
func validDeclaredResultJSON() string {
	return `{"apiVersion":"marshal.dev/v1alpha1","kind":"WorkerResult","taskId":"TASK-1","runId":"run-1","attemptId":"attempt-1","adapter":{"id":"codex","executable":"claimed-by-worker","version":"worker-claim"},"session":{"id":"thread-1","resumable":true},"status":"completed","summary":"fixture completed","declaredChangedFiles":["file.txt"],"declaredArtifacts":[],"declaredCommands":[],"declaredRisks":[],"outputTruncated":false,"startedAt":"2026-08-17T00:00:00Z","completedAt":"2026-08-17T00:00:01Z"}`
}

// successTranscriptLines 是按 0.145.0 真实 JSONL smoke 冻结的成功事件
// 序列：turn.started 不携带 turn_id，终态型 agent_message 直接发出
// item.completed，最终以 turn.completed 收口。
func successTranscriptLines() []string {
	return []string{
		`{"type":"thread.started","thread_id":"thread-1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"type":"agent_message"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":11,"output_tokens":7}}`,
	}
}

// transcriptBody 生成只打印给定 JSONL 事件的 fake 主体。
func transcriptBody(lines []string) string {
	if len(lines) == 0 {
		return "exit 0"
	}
	quoted := make([]string, 0, len(lines))
	for _, line := range lines {
		quoted = append(quoted, shellQuote(line))
	}
	return "printf '%s\\n' " + strings.Join(quoted, " ") + "\n"
}

// successBodyWithResult 组装成功 attempt 的 fake 主体：打印冻结 transcript，
// 并把声明的 WorkerResult 写入 result 路径；resultJSON 为空时不写 result，
// 用于缺失 result 的失败路径。argv/stdin/环境捕获由 fakeScript 统一完成。
func successBodyWithResult(resultJSON string) string {
	body := transcriptBody(successTranscriptLines())
	if resultJSON != "" {
		body += resultHeredoc(resultJSON)
	}
	return body
}

// resultHeredoc 把声明的 WorkerResult 写入 fake 从 argv 解析出的 result_path。
func resultHeredoc(resultJSON string) string {
	return "cat > \"$result_path\" <<'CODEX_RESULT_EOF'\n" + resultJSON + "\nCODEX_RESULT_EOF\n"
}

func fakeExecutable(t *testing.T, versionOutput, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte(fakeScript(versionOutput, body)), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// nativeFakeExecutable builds a real platform-native fixture for production
// fd-exec tests. A shebang fixture cannot be executed from a CLOEXEC descriptor:
// the kernel must hand its pathname to the interpreter after the descriptor is
// closed. Production's authenticated path intentionally targets a native Codex
// image, while parser-focused tests continue to use deterministic shell fakes.
func nativeFakeExecutable(t *testing.T) string {
	t.Helper()
	source := fmt.Sprintf(`package main
import (
    "fmt"
    "os"
)
func main() {
    if len(os.Args) > 1 && os.Args[1] == "--version" {
        fmt.Println(%q)
        return
    }
    resultPath := ""
    for index, argument := range os.Args {
        if argument == "--output-last-message" && index+1 < len(os.Args) {
            resultPath = os.Args[index+1]
        }
    }
    if resultPath == "" {
        os.Exit(2)
    }
    if err := os.WriteFile("capture", []byte("native fixture"), 0600); err != nil {
        os.Exit(3)
    }
    if err := os.WriteFile(resultPath, []byte(%q), 0600); err != nil {
        os.Exit(4)
    }
    fmt.Println(%q)
    fmt.Println(%q)
    fmt.Println(%q)
    fmt.Println(%q)
}
`, supportedVersionOutput, validDeclaredResultJSON(), successTranscriptLines()[0], successTranscriptLines()[1], successTranscriptLines()[2], successTranscriptLines()[3])
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "main.go")
	executable := filepath.Join(directory, "codex-native")
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "build", "-o", executable, sourcePath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build native Codex fixture: %v: %s", err, output)
	}
	return executable
}

func useFixtureExecutable(t *testing.T, fixture *runFixture, executable string) {
	t.Helper()
	real, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	fixture.executable = real
	fixture.adapter.executable = real
	// Native fixtures exercise the same authenticated fd-exec and conformance
	// launcher path as production. The legacy authority binding is explicitly
	// test-only; production Run always consumes a fresh atomic authority state.
	fixture.adapter.unsafePathExecutionForTest = false
	fixture.adapter.legacyAuthorityForTest = true
	snapshot, err := fixture.adapter.inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	identity := snapshot.identity
	snapshot.close()
	// Changing the executable invalidates the old fixture's conformance
	// authority. Produce independently signed evidence for the exact native
	// identity and pass it through the production authority store and
	// BindConformance validation; directly constructing boundConformance would
	// bypass the security boundary this integration fixture must prove.
	store, evidenceDigest := signedTestAuthority(t, identity)
	fixture.adapter.authority = store
	if err := fixture.adapter.BindConformance(context.Background(), evidenceDigest); err != nil {
		t.Fatalf("bind native fixture conformance: %v", err)
	}
}

// fakeScript 生成 fake codex：--version 返回冻结格式版本行；其余调用先按
// 0.145.0 真实 parser 契约校验 argv（全局参数在 exec 子命令之前、
// --ask-for-approval 只接受顶层位置、exec --sandbox 只接受
// read-only/workspace-write/danger-full-access 枚举），任何错误排序或取值
// 以 exit=2 拒绝；校验通过后从 argv 的 --output-last-message/-o 解析出
// 唯一结果叶子 result_path，确定性捕获 argv/stdin/环境并执行 body。
// 全部 fixture 均由测试在 t.TempDir 内动态生成，不依赖真实 Codex、网络、
// 用户认证或用户配置。
func fakeScript(versionOutput, body string) string {
	return "#!/bin/sh\n" +
		"if [ \"${1:-}\" = \"--version\" ]; then printf '%s\\n' " + shellQuote(versionOutput) + "; exit 0; fi\n" +
		fakeParserScript() +
		fakeCaptureScript() +
		body + "\n"
}

// fakeParserScript 是冻结的 0.145.0 parser 契约：拒绝错误排序与取值时
// 输出为空且以 exit=2 终止。--output-last-message/-o 的值被解析进
// result_path，模拟真实 CLI 把最终输出写到该叶子；测试构造器不再向
// fake 注入任何 control path，杜绝绕过 argv/CLI output contract 的假绿。
func fakeParserScript() string {
	return `argv_log=$(
  for a in "$@"; do printf '%s\n' "$a"; done
)
stdin_log=$(cat)
result_path=""
worktree_path=""
saw_exec=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    exec) saw_exec=1; shift; break ;;
    --ask-for-approval|-C|--cd|-c|--config)
      flag=$1
      [ "$#" -ge 2 ] || exit 2
      value=$2
      shift 2
      case "$flag" in
        --ask-for-approval) case "$value" in never|untrusted|on-failure|on-request) ;; *) exit 2 ;; esac ;;
        -C|--cd) worktree_path=$value ;;
      esac ;;
    *) exit 2 ;;
  esac
done
[ "$saw_exec" = "1" ] || exit 2
if [ -n "$worktree_path" ]; then cd "$worktree_path" || exit 2; fi
while [ "$#" -gt 0 ]; do
  case "$1" in
    --json|--ephemeral|--ignore-user-config|--ignore-rules) shift ;;
    --color)
      [ "$#" -ge 2 ] || exit 2
      case "$2" in never|always|auto) ;; *) exit 2 ;; esac
      shift 2 ;;
    --sandbox)
      [ "$#" -ge 2 ] || exit 2
      case "$2" in read-only|workspace-write|danger-full-access) ;; *) exit 2 ;; esac
      shift 2 ;;
    --output-schema) [ "$#" -ge 2 ] || exit 2; shift 2 ;;
    --output-last-message|-o)
      [ "$#" -ge 2 ] || exit 2
      result_path=$2
      shift 2 ;;
    -m|--model) [ "$#" -ge 2 ] || exit 2; shift 2 ;;
    *) exit 2 ;;
  esac
done
`
}

// fakeCaptureScript 在 parser 契约通过后把调用证据写入 worktree/capture。
func fakeCaptureScript() string {
	return `mkdir -p capture
printf '%s\n' "$argv_log" > capture/argv
printf '%s' "$stdin_log" > capture/stdin
env | LC_ALL=C sort > capture/env
`
}

func newValidator(t *testing.T) *contract.Validator {
	t.Helper()
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	return validator
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func jsonString(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func digest(character string) string { return "sha256:" + strings.Repeat(character, 64) }

func signedTestAuthority(t *testing.T, identity executableIdentity) (*AuthorityEvidenceStore, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	evidence := ConformanceEvidence{
		RunnerID: "marshal-conformance", RunnerVersion: "1",
		ObservedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), ValidUntil: now.Add(time.Hour).Format(time.RFC3339Nano),
		AdapterVersion: adapterVersion, Executable: identity.path, ExecutableDigest: identity.digest, BinaryVersion: identity.version,
		CapabilitiesDigest: expectedCapabilitiesDigest(), TranscriptDigest: digest("b"), CredentialVerified: true, LiveProtocolVerified: true,
		EventContract: conformanceEventContract, CodexCLIVersion: identity.version, ProtocolVersion: codexProtocolVersion, PermissionMode: codexPermissionMode,
		TrustRootKeyID: "test-root",
	}
	evidence.EvidenceDigest, err = evidence.digest()
	if err != nil {
		t.Fatal(err)
	}
	message, err := evidence.signingBytes()
	if err != nil {
		t.Fatal(err)
	}
	evidence.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, message))
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, strings.TrimPrefix(evidence.EvidenceDigest, "sha256:")+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewAuthorityEvidenceStore(root, map[string]ed25519.PublicKey{"test-root": publicKey})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, evidence.EvidenceDigest
}

func assertCodexFailure(t *testing.T, err error, kind port.FailureKind, disposition port.RetryDisposition) {
	t.Helper()
	failure, ok := port.AsAdapterFailure(err)
	if !ok || failure.Adapter != port.AdapterIDCodex || failure.Kind != kind || failure.Disposition != disposition {
		t.Fatalf("err = %v, want typed codex %s/%s", err, kind, disposition)
	}
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func containsSequence(values []string, first, second string) bool {
	for index := 0; index+1 < len(values); index++ {
		if values[index] == first && values[index+1] == second {
			return true
		}
	}
	return false
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("file %s was not produced within %s", path, timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
