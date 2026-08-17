package qoder

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
	"golang.org/x/sys/unix"
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

func TestRunRequiresBoundConformanceIdentityBeforeLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, supportedBinary, "touch "+shellQuote(marker)+"\n"+successEvents("provider/model"))
	fixture.adapter.mu.Lock()
	fixture.adapter.conformance = nil
	fixture.adapter.mu.Unlock()
	_, err := fixture.adapter.Run(context.Background(), fixture.request)
	if !errors.Is(err, ErrConformancePending) || !port.IsPermanent(err) {
		t.Fatalf("error = %v, want permanent conformance-pending", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("worker launched without a bound conformance identity")
	}
}

func TestCallerRewrittenCapabilitySnapshotCannotAuthorizeAdapter(t *testing.T) {
	fixture := newRunFixture(t, supportedBinary, successEvents("provider/model"))
	fixture.adapter.mu.Lock()
	fixture.adapter.conformance = nil
	fixture.adapter.mu.Unlock()
	record, err := fixture.adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(record.Data, &snapshot); err != nil {
		t.Fatal(err)
	}
	// A caller can rewrite Probe JSON, but no public binding API consumes a
	// CapabilitySnapshot or caller-supplied authority store.
	snapshot["probeStatus"], snapshot["probeErrors"] = "supported", []string{}
	record.Data, err = json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(record.Data), conformanceEventContract) {
		t.Fatal("Probe unexpectedly contains an independent conformance receipt")
	}
	probe, err := fixture.adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(probe.Data), `"probeStatus":"supported"`) {
		t.Fatalf("rewritten caller snapshot authorized adapter: %s", probe.Data)
	}
}

func TestCandidateAuthorityDynamicallyRevalidatesAndRevokesFullRun(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched-after-revoke")
	fixture := newRunFixture(t, supportedBinary, "touch "+shellQuote(marker)+"\n"+successEvents("provider/model"))
	fixture.adapter.mu.Lock()
	fixture.adapter.conformance = nil
	fixture.adapter.mu.Unlock()
	identity, err := fixture.adapter.inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	store, evidenceDigest := signedTestAuthority(t, identity)
	publicKey := store.trustRoots["test-root"]
	configPath := filepath.Join(realTempDir(t), "authority.json")
	configValue := AuthorityConfig{
		EvidenceRoot: store.root, EvidenceDigest: evidenceDigest, AuthorityGeneration: 1, ProbeArtifactDigest: digest("a"), ChallengeDigest: digest("c"), RevokedEvidenceDigests: []string{},
		TrustRoots: []AuthorityTrustRoot{{KeyID: "test-root", Algorithm: "ed25519", PublicKey: base64.StdEncoding.EncodeToString(publicKey)}},
	}
	configData, err := json.Marshal(configValue)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.adapter.authorityConfigPath = configPath
	fixture.adapter.authorityFenceRoot = realPrivateTempDir(t)
	if err := fixture.adapter.refreshConfiguredConformance(context.Background()); err != nil {
		t.Fatal(err)
	}
	probe, err := fixture.adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(probe.Data), `"probeStatus":"supported"`) {
		t.Fatalf("production authority wiring did not bind support: %s", probe.Data)
	}
	for _, field := range []string{"conformanceEvidenceDigest", "conformanceTrustRootKeyId", "conformanceProbeProfileDigest", "conformanceValidUntil", "conformanceHostFingerprint", "conformanceAuthorityGeneration"} {
		if !strings.Contains(string(probe.Data), `"`+field+`"`) {
			t.Fatalf("supported snapshot omitted %s: %s", field, probe.Data)
		}
	}
	var incomplete map[string]any
	if err := json.Unmarshal(probe.Data, &incomplete); err != nil {
		t.Fatal(err)
	}
	delete(incomplete, "conformanceEvidenceDigest")
	incompleteData, err := json.Marshal(incomplete)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.validator.Validate(domain.KindCapabilitySnapshot, incompleteData); err == nil {
		t.Fatal("schema accepted supported qoder without required conformance metadata")
	}
	for name, mutate := range map[string]func(*AuthorityConfig){
		"wrong probe artifact": func(value *AuthorityConfig) { value.ProbeArtifactDigest = digest("f") },
		"wrong challenge":      func(value *AuthorityConfig) { value.ChallengeDigest = digest("f") },
	} {
		t.Run(name, func(t *testing.T) {
			broken := configValue
			mutate(&broken)
			writeAuthorityConfig(t, configPath, broken)
			brokenProbe, err := fixture.adapter.Probe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(brokenProbe.Data), `"probeStatus":"unsupported"`) {
				t.Fatalf("mismatched authority config remained supported: %s", brokenProbe.Data)
			}
			writeAuthorityConfig(t, configPath, configValue)
		})
	}
	probe, err = fixture.adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(probe.Data), `"probeStatus":"supported"`) {
		t.Fatalf("restored exact authority config did not recover: %s", probe.Data)
	}
	configValue.AuthorityGeneration = 2
	configValue.RevokedEvidenceDigests = []string{evidenceDigest}
	revokedData, err := json.Marshal(configValue)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, revokedData, 0o600); err != nil {
		t.Fatal(err)
	}
	revokedProbe, err := fixture.adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(revokedProbe.Data), `"probeStatus":"unsupported"`) {
		t.Fatalf("revoked evidence remained supported: %s", revokedProbe.Data)
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrConformancePending) || !port.IsPermanent(err) {
		t.Fatalf("revoked evidence did not fail closed for full Run: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("revoked full Run launched the worker")
	}
	configValue.AuthorityGeneration = 1
	configValue.RevokedEvidenceDigests = nil
	writeAuthorityConfig(t, configPath, configValue)
	rollbackProbe, err := fixture.adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rollbackProbe.Data), `"probeStatus":"unsupported"`) {
		t.Fatalf("revoked generation rollback revived support: %s", rollbackProbe.Data)
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrConformancePending) || !port.IsPermanent(err) {
		t.Fatalf("revoked generation rollback did not fail closed for full Run: %v", err)
	}

	badConfig := filepath.Join(realTempDir(t), "authority.json")
	if err := os.WriteFile(badConfig, configData, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAuthorityConfig(badConfig); err == nil {
		t.Fatal("world-readable authority config was accepted")
	}
	symlink := filepath.Join(realTempDir(t), "authority-link.json")
	if err := os.Symlink(configPath, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAuthorityConfig(symlink); err == nil {
		t.Fatal("symlinked authority config was accepted")
	}
	realParent := realTempDir(t)
	parentConfig := filepath.Join(realParent, "authority.json")
	if err := os.WriteFile(parentConfig, configData, 0o600); err != nil {
		t.Fatal(err)
	}
	parentLink := filepath.Join(realTempDir(t), "linked-parent")
	if err := os.Symlink(realParent, parentLink); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAuthorityConfig(filepath.Join(parentLink, "authority.json")); err == nil {
		t.Fatal("authority config beneath a symlinked parent was accepted")
	}
}

func TestProductionAuthorityConfigRemainsDisabledWhileADR0034Proposed(t *testing.T) {
	executable := fakeExecutable(t, supportedBinary, "exit 0")
	if _, err := NewFromAuthorityConfig(context.Background(), executable, newValidator(t), "/private/candidate.json"); !errors.Is(err, ErrConformancePending) || !port.IsPermanent(err) {
		t.Fatalf("production candidate activation error = %v", err)
	}
}

func TestCandidateAuthorityGenerationRollbackFailsClosed(t *testing.T) {
	executable := fakeExecutable(t, supportedBinary, "exit 0")
	adapter, err := New(executable, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := adapter.inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	store1, digest1 := signedTestAuthorityWindowGeneration(t, identity, now.Add(-time.Minute), now.Add(time.Hour), mustHostFingerprint(t), 1)
	store2, digest2 := signedTestAuthorityWindowGeneration(t, identity, now.Add(-time.Minute), now.Add(time.Hour), mustHostFingerprint(t), 2)
	configPath := filepath.Join(realTempDir(t), "authority.json")
	writeAuthorityConfig(t, configPath, authorityConfigForStore(store1, digest1, 1))
	adapter.authorityConfigPath = configPath
	adapter.authorityFenceRoot = realPrivateTempDir(t)
	if err := adapter.refreshConfiguredConformance(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeAuthorityConfig(t, configPath, authorityConfigForStore(store2, digest2, 2))
	if err := adapter.refreshConfiguredConformance(context.Background()); err != nil {
		t.Fatal(err)
	}
	storeSameGeneration, digestSameGeneration := signedTestAuthorityWindowGeneration(t, identity, now.Add(-2*time.Minute), now.Add(time.Hour), mustHostFingerprint(t), 2)
	writeAuthorityConfig(t, configPath, authorityConfigForStore(storeSameGeneration, digestSameGeneration, 2))
	if err := adapter.refreshConfiguredConformance(context.Background()); !errors.Is(err, ErrConformancePending) || !port.IsPermanent(err) {
		t.Fatalf("same-generation evidence substitution error = %v", err)
	}
	writeAuthorityConfig(t, configPath, authorityConfigForStore(store1, digest1, 1))
	if err := adapter.refreshConfiguredConformance(context.Background()); !errors.Is(err, ErrConformancePending) || !port.IsPermanent(err) {
		t.Fatalf("generation rollback error = %v", err)
	}
	if adapter.authorityGenerationHighWater != 2 {
		t.Fatalf("authority high-water = %d, want 2", adapter.authorityGenerationHighWater)
	}
}

func TestConfiguredAuthorityGenerationFenceSurvivesAdapterRestart(t *testing.T) {
	executable := fakeExecutable(t, supportedBinary, "exit 0")
	first, err := New(executable, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := first.inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	store1, digest1 := signedTestAuthorityWindowGeneration(t, identity, now.Add(-time.Minute), now.Add(time.Hour), mustHostFingerprint(t), 1)
	store2, digest2 := signedTestAuthorityWindowGeneration(t, identity, now.Add(-time.Minute), now.Add(time.Hour), mustHostFingerprint(t), 2)
	configPath := filepath.Join(realTempDir(t), "authority.json")
	fenceRoot := realPrivateTempDir(t)
	writeAuthorityConfig(t, configPath, authorityConfigForStore(store1, digest1, 1))
	first.authorityConfigPath, first.authorityFenceRoot = configPath, fenceRoot
	if err := first.refreshConfiguredConformance(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Consume generation two even though its leaf is deliberately missing.
	missingGenerationTwo := authorityConfigForStore(store2, digest("f"), 2)
	writeAuthorityConfig(t, configPath, missingGenerationTwo)
	if err := first.refreshConfiguredConformance(context.Background()); !errors.Is(err, ErrConformancePending) {
		t.Fatalf("missing generation-two evidence error = %v", err)
	}

	// A new Adapter instance has no process-local high-water. The durable
	// consumer fence must still reject both rollback and same-generation
	// identity substitution.
	restarted, err := New(executable, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	restarted.authorityConfigPath, restarted.authorityFenceRoot = configPath, fenceRoot
	writeAuthorityConfig(t, configPath, authorityConfigForStore(store1, digest1, 1))
	if err := restarted.refreshConfiguredConformance(context.Background()); !errors.Is(err, ErrConformancePending) || !port.IsPermanent(err) {
		t.Fatalf("restart rollback error = %v", err)
	}
	writeAuthorityConfig(t, configPath, authorityConfigForStore(store2, digest2, 2))
	if err := restarted.refreshConfiguredConformance(context.Background()); !errors.Is(err, ErrConformancePending) || !port.IsPermanent(err) {
		t.Fatalf("restart same-generation replacement error = %v", err)
	}
	writeAuthorityConfig(t, configPath, missingGenerationTwo)
	if err := restarted.refreshConfiguredConformance(context.Background()); !errors.Is(err, ErrConformancePending) || !port.IsPermanent(err) {
		t.Fatalf("restart exact generation-two config error = %v", err)
	}
}

func TestAuthorityGenerationFenceIsAtomicPrivateAndCrashRecoverable(t *testing.T) {
	root := realPrivateTempDir(t)
	configDigest := digest("a")
	if err := consumeAuthorityGeneration(root, 1, configDigest); err != nil {
		t.Fatal(err)
	}
	// Same-directory staging residue is not authority. A restarted consumer
	// reads only the fsync+rename committed record.
	if err := os.WriteFile(filepath.Join(root, ".generation-crash-residue.tmp"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := consumeAuthorityGeneration(root, 1, configDigest); err != nil {
		t.Fatalf("exact restart replay rejected: %v", err)
	}
	if err := consumeAuthorityGeneration(root, 0, configDigest); err == nil {
		t.Fatal("zero generation was accepted")
	}
	if err := consumeAuthorityGeneration(root, 1, digest("b")); err == nil {
		t.Fatal("same-generation identity replacement was accepted")
	}

	privateRoot := realTempDir(t)
	if err := os.Chmod(privateRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := consumeAuthorityGeneration(privateRoot, 1, configDigest); err == nil {
		t.Fatal("world-searchable fence root was accepted")
	}
	realRoot := realTempDir(t)
	linkedRoot := filepath.Join(realTempDir(t), "fence-link")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Fatal(err)
	}
	if err := consumeAuthorityGeneration(linkedRoot, 1, configDigest); err == nil {
		t.Fatal("symlinked fence root was accepted")
	}

	corruptRoot := realPrivateTempDir(t)
	if err := os.WriteFile(filepath.Join(corruptRoot, authorityGenerationFenceName), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := consumeAuthorityGeneration(corruptRoot, 2, configDigest); err == nil {
		t.Fatal("corrupt durable fence was overwritten instead of failing closed")
	}
	unknownRoot := realPrivateTempDir(t)
	unknown := []byte(`{"kind":"qoder-authority-generation-fence-v1","authorityGeneration":1,"authorityConfigDigest":"` + configDigest + `","unknown":true}`)
	if err := os.WriteFile(filepath.Join(unknownRoot, authorityGenerationFenceName), unknown, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := consumeAuthorityGeneration(unknownRoot, 2, configDigest); err == nil {
		t.Fatal("durable fence with an unknown field was accepted")
	}
	oversizedRoot := realPrivateTempDir(t)
	if err := os.WriteFile(filepath.Join(oversizedRoot, authorityGenerationFenceName), make([]byte, authorityGenerationLimit+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := consumeAuthorityGeneration(oversizedRoot, 2, configDigest); err == nil {
		t.Fatal("oversized durable fence was accepted")
	}

	abnormalRoot := realPrivateTempDir(t)
	target := filepath.Join(realTempDir(t), "target")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(abnormalRoot, authorityGenerationFenceName)); err != nil {
		t.Fatal(err)
	}
	if err := consumeAuthorityGeneration(abnormalRoot, 1, configDigest); err == nil {
		t.Fatal("symlinked durable fence was accepted")
	}
	fifoRoot := realPrivateTempDir(t)
	if err := unix.Mkfifo(filepath.Join(fifoRoot, authorityGenerationFenceName), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := consumeAuthorityGeneration(fifoRoot, 1, configDigest); err == nil {
		t.Fatal("FIFO durable fence was accepted")
	}
	modeRoot := realPrivateTempDir(t)
	if err := consumeAuthorityGeneration(modeRoot, 1, configDigest); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(modeRoot, authorityGenerationFenceName), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := consumeAuthorityGeneration(modeRoot, 2, digest("b")); err == nil {
		t.Fatal("world-readable durable fence was accepted")
	}
	privateWrongModeRoot := realPrivateTempDir(t)
	if err := consumeAuthorityGeneration(privateWrongModeRoot, 1, configDigest); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(privateWrongModeRoot, authorityGenerationFenceName), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := consumeAuthorityGeneration(privateWrongModeRoot, 2, digest("b")); err == nil {
		t.Fatal("private but non-0600 durable fence was accepted")
	}
	recordHardlinkRoot := realPrivateTempDir(t)
	if err := consumeAuthorityGeneration(recordHardlinkRoot, 1, configDigest); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(recordHardlinkRoot, authorityGenerationFenceName), filepath.Join(recordHardlinkRoot, "generation-hardlink")); err != nil {
		t.Fatal(err)
	}
	if err := consumeAuthorityGeneration(recordHardlinkRoot, 2, digest("b")); err == nil {
		t.Fatal("hard-linked durable fence was accepted")
	}
	lockRoot := realPrivateTempDir(t)
	if err := os.Symlink(target, filepath.Join(lockRoot, authorityGenerationLockName)); err != nil {
		t.Fatal(err)
	}
	if err := consumeAuthorityGeneration(lockRoot, 1, configDigest); err == nil {
		t.Fatal("symlinked durable fence lock was accepted")
	}
	lockModeRoot := realPrivateTempDir(t)
	if err := os.WriteFile(filepath.Join(lockModeRoot, authorityGenerationLockName), nil, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := consumeAuthorityGeneration(lockModeRoot, 1, configDigest); err == nil {
		t.Fatal("private but non-0600 durable fence lock was accepted")
	}
	lockFIFORoot := realPrivateTempDir(t)
	if err := unix.Mkfifo(filepath.Join(lockFIFORoot, authorityGenerationLockName), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := consumeAuthorityGeneration(lockFIFORoot, 1, configDigest); err == nil {
		t.Fatal("FIFO durable fence lock was accepted")
	}
	lockHardlinkRoot := realPrivateTempDir(t)
	lockTarget := filepath.Join(lockHardlinkRoot, "lock-target")
	if err := os.WriteFile(lockTarget, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(lockTarget, filepath.Join(lockHardlinkRoot, authorityGenerationLockName)); err != nil {
		t.Fatal(err)
	}
	if err := consumeAuthorityGeneration(lockHardlinkRoot, 1, configDigest); err == nil {
		t.Fatal("hard-linked durable fence lock was accepted")
	}
}

func TestAuthorityGenerationFenceRejectsAuthorityRootOverlapAndAliases(t *testing.T) {
	configDigest := digest("a")
	rejects := func(fenceRoot string, evidenceRoot string) bool {
		t.Helper()
		evidenceDirectory, err := openPrivateAuthorityDirectory(evidenceRoot, "test evidence root is invalid")
		if err != nil {
			t.Fatal(err)
		}
		defer evidenceDirectory.Close()
		return consumeAuthorityGenerationSeparated(fenceRoot, evidenceDirectory, 1, configDigest) != nil
	}
	sameRoot := realPrivateTempDir(t)
	if !rejects(sameRoot, sameRoot) {
		t.Fatal("same fence and evidence directory was accepted")
	}

	authorityParent := realPrivateTempDir(t)
	fenceChild := filepath.Join(authorityParent, "consumer-state")
	if err := os.Mkdir(fenceChild, 0o700); err != nil {
		t.Fatal(err)
	}
	if !rejects(fenceChild, authorityParent) {
		t.Fatal("fence nested under authority root was accepted")
	}

	fenceParent := realPrivateTempDir(t)
	authorityChild := filepath.Join(fenceParent, "authority-evidence")
	if err := os.Mkdir(authorityChild, 0o700); err != nil {
		t.Fatal(err)
	}
	if !rejects(fenceParent, authorityChild) {
		t.Fatal("authority root nested under fence was accepted")
	}

	aliasTarget := realPrivateTempDir(t)
	alias := filepath.Join(realPrivateTempDir(t), "authority-alias")
	if err := os.Symlink(aliasTarget, alias); err != nil {
		t.Fatal(err)
	}
	if !rejects(alias, aliasTarget) {
		t.Fatal("symlink path alias between fence and authority roots was accepted")
	}
	uncleanAlias := filepath.Join(aliasTarget, ".")
	if uncleanAlias == filepath.Clean(uncleanAlias) {
		uncleanAlias = aliasTarget + string(filepath.Separator) + "."
	}
	if !rejects(uncleanAlias, realPrivateTempDir(t)) {
		t.Fatal("unclean path alias for fence root was accepted")
	}
}

func TestAuthorityGenerationFenceConcurrentSameGenerationHasOneIdentity(t *testing.T) {
	root := realPrivateTempDir(t)
	start := make(chan struct{})
	type result struct {
		digest string
		err    error
	}
	results := make(chan result, 2)
	for _, candidate := range []string{digest("a"), digest("b")} {
		candidate := candidate
		go func() {
			<-start
			results <- result{digest: candidate, err: consumeAuthorityGeneration(root, 1, candidate)}
		}()
	}
	close(start)
	var winner, loser string
	for range 2 {
		outcome := <-results
		if outcome.err == nil {
			if winner != "" {
				t.Fatal("two config identities consumed the same generation")
			}
			winner = outcome.digest
		} else {
			loser = outcome.digest
		}
	}
	if winner == "" || loser == "" {
		t.Fatalf("concurrent outcomes did not select exactly one identity: winner=%q loser=%q", winner, loser)
	}
	if err := consumeAuthorityGeneration(root, 1, winner); err != nil {
		t.Fatalf("winning identity was not durable: %v", err)
	}
	if err := consumeAuthorityGeneration(root, 1, loser); err == nil {
		t.Fatal("losing same-generation identity became admissible")
	}
}

func TestAuthorityGenerationFenceCrossProcess(t *testing.T) {
	if os.Getenv("MARSHAL_QODER_FENCE_HELPER") == "1" {
		generation, err := strconv.ParseUint(os.Getenv("MARSHAL_QODER_FENCE_GENERATION"), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		if err := consumeAuthorityGeneration(os.Getenv("MARSHAL_QODER_FENCE_ROOT"), generation, os.Getenv("MARSHAL_QODER_FENCE_DIGEST")); err != nil {
			t.Fatal(err)
		}
		return
	}
	root := realPrivateTempDir(t)
	run := func(generation uint64, configDigest string) error {
		command := exec.Command(os.Args[0], "-test.run=^TestAuthorityGenerationFenceCrossProcess$")
		command.Env = append(os.Environ(),
			"MARSHAL_QODER_FENCE_HELPER=1",
			"MARSHAL_QODER_FENCE_ROOT="+root,
			"MARSHAL_QODER_FENCE_GENERATION="+strconv.FormatUint(generation, 10),
			"MARSHAL_QODER_FENCE_DIGEST="+configDigest,
		)
		return command.Run()
	}
	if err := run(1, digest("a")); err != nil {
		t.Fatal(err)
	}
	if err := run(2, digest("b")); err != nil {
		t.Fatal(err)
	}
	if err := run(1, digest("a")); err == nil {
		t.Fatal("fresh process revived an older generation")
	}
	if err := run(2, digest("c")); err == nil {
		t.Fatal("fresh process replaced same-generation identity")
	}
	if err := run(2, digest("b")); err != nil {
		t.Fatalf("fresh process rejected exact durable identity: %v", err)
	}
}

func TestCandidateAuthorityMissingEvidenceAdvancesGenerationHighWater(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched-after-missing-evidence-rollback")
	fixture := newRunFixture(t, supportedBinary, "touch "+shellQuote(marker)+"\n"+successEvents("provider/model"))
	fixture.adapter.mu.Lock()
	fixture.adapter.conformance = nil
	fixture.adapter.mu.Unlock()
	identity, err := fixture.adapter.inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	store, evidenceDigest := signedTestAuthority(t, identity)
	configPath := filepath.Join(realTempDir(t), "authority.json")
	generationOne := authorityConfigForStore(store, evidenceDigest, 1)
	writeAuthorityConfig(t, configPath, generationOne)
	fixture.adapter.authorityConfigPath = configPath
	fixture.adapter.authorityFenceRoot = realPrivateTempDir(t)
	if err := fixture.adapter.refreshConfiguredConformance(context.Background()); err != nil {
		t.Fatal(err)
	}
	missing := authorityConfigForStore(store, digest("f"), 2)
	writeAuthorityConfig(t, configPath, missing)
	probe, err := fixture.adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(probe.Data), `"probeStatus":"unsupported"`) {
		t.Fatalf("missing generation-two evidence remained supported: %s", probe.Data)
	}
	writeAuthorityConfig(t, configPath, generationOne)
	probe, err = fixture.adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(probe.Data), `"probeStatus":"unsupported"`) {
		t.Fatalf("missing-evidence generation rollback revived support: %s", probe.Data)
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrConformancePending) || !port.IsPermanent(err) {
		t.Fatalf("missing-evidence generation rollback did not fail closed for full Run: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("missing-evidence generation rollback launched the worker")
	}
}

func TestCandidateAuthorityRevocationAtFullRunLaunchBoundary(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched-after-boundary-revoke")
	fixture := newRunFixture(t, supportedBinary, "touch "+shellQuote(marker)+"\n"+successEvents("provider/model"))
	fixture.adapter.mu.Lock()
	fixture.adapter.conformance = nil
	fixture.adapter.mu.Unlock()
	identity, err := fixture.adapter.inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	store, evidenceDigest := signedTestAuthority(t, identity)
	configPath := filepath.Join(realTempDir(t), "authority.json")
	config := authorityConfigForStore(store, evidenceDigest, 1)
	writeAuthorityConfig(t, configPath, config)
	fixture.adapter.authorityConfigPath = configPath
	fixture.adapter.authorityFenceRoot = realPrivateTempDir(t)
	fixture.adapter.beforeLaunchGuard = func() {
		config.AuthorityGeneration = 2
		config.RevokedEvidenceDigests = []string{evidenceDigest}
		writeAuthorityConfig(t, configPath, config)
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrConformancePending) || !port.IsPermanent(err) {
		t.Fatalf("launch-boundary revocation error = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("worker launched after launch-boundary revocation")
	}
}

func authorityConfigForStore(store *AuthorityEvidenceStore, evidenceDigest string, generation uint64) AuthorityConfig {
	return AuthorityConfig{
		EvidenceRoot: store.root, EvidenceDigest: evidenceDigest, AuthorityGeneration: generation, ProbeArtifactDigest: digest("a"), ChallengeDigest: digest("c"), RevokedEvidenceDigests: []string{},
		TrustRoots: []AuthorityTrustRoot{{KeyID: "test-root", Algorithm: "ed25519", PublicKey: base64.StdEncoding.EncodeToString(store.trustRoots["test-root"])}},
	}
}

func writeAuthorityConfig(t *testing.T, path string, config AuthorityConfig) {
	t.Helper()
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSealConformanceEvidenceFreezesProbeProfile(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	observation := LiveConformanceObservation{
		RunnerID: "independent-qoder-verifier", RunnerVersion: "1", ObservedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour),
		AdapterVersion: adapterVersion, Executable: "/opt/qoder/qodercli", ExecutableDigest: digest("e"), BinaryVersion: supportedBinary, QoderCLIVersion: supportedBinary, HostOS: runtime.GOOS, HostArch: runtime.GOARCH, HostFingerprint: digest("d"), AuthorityGeneration: 1,
		ProbeSuiteDigest: expectedProbeSuiteDigest(), ProbeArtifactDigest: digest("a"), ChallengeDigest: digest("c"), CapabilitiesDigest: expectedCapabilitiesDigest(), ProbeProfileDigest: expectedProbeProfileDigest(), ArgvDigest: expectedProbeArgvDigest(), EnvironmentDigest: expectedProbeEnvironmentDigest(), ToolPolicyDigest: expectedProbeToolPolicyDigest(), TranscriptDigest: digest("b"), ExecutionReceiptDigest: digest("e"), ExecutionReceiptDigests: []string{digest("a"), digest("b"), digest("c"), digest("d")}, ExecutionReceipts: []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`{}`)}, EvidenceClass: candidateEvidenceClassLive, ReceiptAuthorityKeyID: "receipt-root", ReceiptAuthorityPublicKeyDigest: digest("a"), VerifierKeyID: "verifier-root", VerifierPublicKeyDigest: digest("b"), VerifierSignature: base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)),
		CredentialVerified: true, LiveProtocolVerified: true, WorkspaceWriteVerified: true, EventContract: conformanceEventContract, ProtocolVersion: qoderProtocolVersion, PermissionMode: qoderPermissionMode, TrustRootKeyID: "root-1",
	}
	document, observationDigest, err := EncodeLiveConformanceObservation(observation)
	if err != nil {
		t.Fatal(err)
	}
	if !validSHA256Digest(observationDigest) {
		t.Fatalf("observation digest = %q", observationDigest)
	}
	data, evidenceDigest, err := SignLiveConformanceObservation(document, privateKey)
	if !errors.Is(err, ErrConformancePending) || !port.IsPermanent(err) {
		t.Fatalf("exported candidate signer must remain hard-disabled: %v", err)
	}
	_, _ = data, evidenceDigest
	if err != nil {
		return
	}
	var evidence ConformanceEvidence
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.EvidenceDigest != evidenceDigest || evidence.RunnerVersion != observation.RunnerVersion || evidence.AdapterVersion != observation.AdapterVersion || evidence.BinaryVersion != observation.BinaryVersion || evidence.QoderCLIVersion != observation.QoderCLIVersion || evidence.HostOS != observation.HostOS || evidence.HostArch != observation.HostArch || evidence.CredentialVerified != observation.CredentialVerified || evidence.LiveProtocolVerified != observation.LiveProtocolVerified || evidence.WorkspaceWriteVerified != observation.WorkspaceWriteVerified {
		t.Fatalf("sealed evidence did not freeze profile: %+v", evidence)
	}
	if err := evidence.validate(now, map[string]ed25519.PublicKey{"root-1": publicKey}); err != nil {
		t.Fatal(err)
	}
	wrongProfile := evidence
	wrongProfile.ProbeProfileDigest = digest("c")
	wrongProfile.EvidenceDigest, err = wrongProfile.digest()
	if err != nil {
		t.Fatal(err)
	}
	message, err := wrongProfile.signingBytes()
	if err != nil {
		t.Fatal(err)
	}
	wrongProfile.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, message))
	if err := wrongProfile.validate(now, map[string]ed25519.PublicKey{"root-1": publicKey}); err == nil {
		t.Fatal("validly signed evidence for a different probe environment was accepted")
	}
	wrongHost := evidence
	wrongHost.HostOS = "not-" + runtime.GOOS
	wrongHost.EvidenceDigest, err = wrongHost.digest()
	if err != nil {
		t.Fatal(err)
	}
	message, err = wrongHost.signingBytes()
	if err != nil {
		t.Fatal(err)
	}
	wrongHost.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, message))
	if err := wrongHost.validate(now, map[string]ed25519.PublicKey{"root-1": publicKey}); err == nil {
		t.Fatal("validly signed evidence for a different host environment was accepted")
	}
	future := evidence
	future.ObservedAt = now.Add(time.Minute).Format(time.RFC3339Nano)
	future.ValidUntil = now.Add(time.Hour).Format(time.RFC3339Nano)
	future.EvidenceDigest, err = future.digest()
	if err != nil {
		t.Fatal(err)
	}
	message, err = future.signingBytes()
	if err != nil {
		t.Fatal(err)
	}
	future.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, message))
	if err := future.validate(now, map[string]ed25519.PublicKey{"root-1": publicKey}); err == nil {
		t.Fatal("validly signed future evidence was accepted")
	}

	for name, mutate := range map[string]func(*LiveConformanceObservation){
		"missing runner id":         func(value *LiveConformanceObservation) { value.RunnerID = "" },
		"missing runner version":    func(value *LiveConformanceObservation) { value.RunnerVersion = "" },
		"missing trust root":        func(value *LiveConformanceObservation) { value.TrustRootKeyID = "" },
		"credential not verified":   func(value *LiveConformanceObservation) { value.CredentialVerified = false },
		"protocol not verified":     func(value *LiveConformanceObservation) { value.LiveProtocolVerified = false },
		"workspace not verified":    func(value *LiveConformanceObservation) { value.WorkspaceWriteVerified = false },
		"unsupported version":       func(value *LiveConformanceObservation) { value.BinaryVersion = "1.2.0" },
		"qoder version mismatch":    func(value *LiveConformanceObservation) { value.QoderCLIVersion = "1.1.24" },
		"missing adapter version":   func(value *LiveConformanceObservation) { value.AdapterVersion = "" },
		"wrong adapter version":     func(value *LiveConformanceObservation) { value.AdapterVersion = "0.0.0" },
		"missing host os":           func(value *LiveConformanceObservation) { value.HostOS = "" },
		"missing host arch":         func(value *LiveConformanceObservation) { value.HostArch = "" },
		"missing host fingerprint":  func(value *LiveConformanceObservation) { value.HostFingerprint = "" },
		"missing executable":        func(value *LiveConformanceObservation) { value.Executable = "" },
		"missing executable digest": func(value *LiveConformanceObservation) { value.ExecutableDigest = "" },
		"missing binary version":    func(value *LiveConformanceObservation) { value.BinaryVersion = "" },
		"missing qoder version":     func(value *LiveConformanceObservation) { value.QoderCLIVersion = "" },
		"missing generation":        func(value *LiveConformanceObservation) { value.AuthorityGeneration = 0 },
		"missing probe artifact":    func(value *LiveConformanceObservation) { value.ProbeArtifactDigest = "" },
		"missing challenge":         func(value *LiveConformanceObservation) { value.ChallengeDigest = "" },
		"missing transcript digest": func(value *LiveConformanceObservation) { value.TranscriptDigest = "" },
		"missing receipt digest":    func(value *LiveConformanceObservation) { value.ExecutionReceiptDigest = "" },
		"non-live evidence class":   func(value *LiveConformanceObservation) { value.EvidenceClass = candidateEvidenceClassHermetic },
		"wrong suite":               func(value *LiveConformanceObservation) { value.ProbeSuiteDigest = digest("f") },
		"wrong capabilities":        func(value *LiveConformanceObservation) { value.CapabilitiesDigest = digest("f") },
		"wrong probe profile":       func(value *LiveConformanceObservation) { value.ProbeProfileDigest = digest("f") },
		"wrong argv":                func(value *LiveConformanceObservation) { value.ArgvDigest = digest("f") },
		"wrong environment":         func(value *LiveConformanceObservation) { value.EnvironmentDigest = digest("f") },
		"wrong tool policy":         func(value *LiveConformanceObservation) { value.ToolPolicyDigest = digest("f") },
		"wrong event contract":      func(value *LiveConformanceObservation) { value.EventContract = "wrong" },
		"wrong protocol":            func(value *LiveConformanceObservation) { value.ProtocolVersion = "wrong" },
		"wrong permission":          func(value *LiveConformanceObservation) { value.PermissionMode = "wrong" },
		"non-independent runner":    func(value *LiveConformanceObservation) { value.RunnerID = adapterID },
		"future observation": func(value *LiveConformanceObservation) {
			value.ObservedAt = time.Now().UTC().Add(time.Hour)
			value.ValidUntil = value.ObservedAt.Add(time.Hour)
		},
		"stale observation": func(value *LiveConformanceObservation) {
			value.ObservedAt = time.Now().UTC().Add(-25 * time.Hour)
			value.ValidUntil = time.Now().UTC().Add(time.Hour)
		},
		"validity window too long": func(value *LiveConformanceObservation) {
			value.ValidUntil = value.ObservedAt.Add(maxConformanceValidity + time.Second)
		},
	} {
		t.Run(name, func(t *testing.T) {
			broken := observation
			mutate(&broken)
			document, _, encodeErr := EncodeLiveConformanceObservation(broken)
			if encodeErr == nil {
				_, _, encodeErr = SignLiveConformanceObservation(document, privateKey)
			}
			if encodeErr == nil {
				t.Fatal("invalid observation was sealed")
			}
		})
	}
}

func TestAuthorityEvidenceStoreRejectsSymlinkRoot(t *testing.T) {
	realRoot := realTempDir(t)
	if err := os.Chmod(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(realTempDir(t), "authority")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Fatal(err)
	}
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewAuthorityEvidenceStore(link, map[string]ed25519.PublicKey{"root": publicKey}); err == nil {
		t.Fatal("symlinked authority root was accepted")
	}
}

func TestAuthorityConfigRejectsUnknownFieldAndFIFOWithoutBlocking(t *testing.T) {
	if _, err := loadAuthorityConfig(filepath.Join(realTempDir(t), "missing.json")); err == nil {
		t.Fatal("missing authority config was accepted")
	}
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	config := AuthorityConfig{
		EvidenceRoot: realTempDir(t), EvidenceDigest: digest("a"), AuthorityGeneration: 1, ProbeArtifactDigest: digest("b"), ChallengeDigest: digest("c"), RevokedEvidenceDigests: []string{},
		TrustRoots: []AuthorityTrustRoot{{KeyID: "root", Algorithm: "ed25519", PublicKey: base64.StdEncoding.EncodeToString(publicKey)}},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	document["unknownField"] = true
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(realTempDir(t), "authority.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAuthorityConfig(path); err == nil {
		t.Fatal("authority config with unknown field was accepted")
	}
	delete(document, "unknownField")
	delete(document, "evidenceDigest")
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAuthorityConfig(path); err == nil {
		t.Fatal("authority config with missing evidence digest was accepted")
	}
	fifo := filepath.Join(realTempDir(t), "authority.fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAuthorityConfig(fifo); err == nil {
		t.Fatal("authority config FIFO was accepted")
	}
}

func TestAuthorityEvidenceRejectsMissingUnknownRotatedAndAbnormalLeaf(t *testing.T) {
	executable := fakeExecutable(t, supportedBinary, "exit 0")
	adapter, err := New(executable, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := adapter.inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	store, evidenceDigest := signedTestAuthority(t, identity)
	now := time.Now().UTC()
	if _, err := store.resolve(context.Background(), digest("f"), now); err == nil {
		t.Fatal("missing authority evidence was accepted")
	}
	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	unknownStore, err := NewAuthorityEvidenceStore(store.root, map[string]ed25519.PublicKey{"other-root": otherPublic})
	if err != nil {
		t.Fatal(err)
	}
	defer unknownStore.Close()
	if _, err := unknownStore.resolve(context.Background(), evidenceDigest, now); err == nil {
		t.Fatal("evidence signed by an unknown key was accepted")
	}
	rotatedStore, err := NewAuthorityEvidenceStore(store.root, map[string]ed25519.PublicKey{"test-root": otherPublic})
	if err != nil {
		t.Fatal(err)
	}
	defer rotatedStore.Close()
	if _, err := rotatedStore.resolve(context.Background(), evidenceDigest, now); err == nil {
		t.Fatal("evidence signed by the pre-rotation key was accepted")
	}

	leaf := filepath.Join(store.root, strings.TrimPrefix(evidenceDigest, "sha256:")+".json")
	data, err := os.ReadFile(leaf)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(leaf, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.resolve(context.Background(), evidenceDigest, now); err == nil {
		t.Fatal("world-readable authority evidence was accepted")
	}
	if err := os.Chmod(leaf, 0o600); err != nil {
		t.Fatal(err)
	}
	document["unknownField"] = true
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leaf, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.resolve(context.Background(), evidenceDigest, now); err == nil {
		t.Fatal("authority evidence with unknown field was accepted")
	}

	if err := os.Remove(leaf); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(realTempDir(t), "target.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, leaf); err != nil {
		t.Fatal(err)
	}
	if _, err := store.resolve(context.Background(), evidenceDigest, now); err == nil {
		t.Fatal("symlinked authority evidence leaf was accepted")
	}
	if err := os.Remove(leaf); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(leaf, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.resolve(context.Background(), evidenceDigest, now); err == nil {
		t.Fatal("authority evidence FIFO was accepted")
	}
}

func TestAuthorityOwnerAndModePredicatesFailClosed(t *testing.T) {
	uid := os.Geteuid()
	regular := unix.Stat_t{Mode: unix.S_IFREG | 0o600, Uid: uint32(uid)}
	if !privateRegularFile(regular, uid) {
		t.Fatal("private owner regular file was rejected")
	}
	regular.Mode = unix.S_IFREG | 0o644
	if privateRegularFile(regular, uid) {
		t.Fatal("world-readable authority file was accepted")
	}
	regular.Mode, regular.Uid = unix.S_IFREG|0o600, uint32(uid+1)
	if privateRegularFile(regular, uid) {
		t.Fatal("wrong-owner authority file was accepted")
	}
	regular.Mode, regular.Uid, regular.Nlink = unix.S_IFREG|0o600, uint32(uid), 2
	if privateSingleLinkRegularFile(regular, uid) {
		t.Fatal("multi-link authority fence file was accepted")
	}
	regular.Nlink = 1
	if !privateSingleLinkRegularFile(regular, uid) {
		t.Fatal("private single-link authority fence file was rejected")
	}
	directory := unix.Stat_t{Mode: unix.S_IFDIR | 0o700, Uid: uint32(uid)}
	if !privateDirectory(directory, uid) {
		t.Fatal("private owner authority directory was rejected")
	}
	directory.Mode = unix.S_IFDIR | 0o755
	if privateDirectory(directory, uid) {
		t.Fatal("world-searchable authority directory was accepted")
	}
	directory.Mode, directory.Uid = unix.S_IFDIR|0o700, uint32(uid+1)
	if privateDirectory(directory, uid) {
		t.Fatal("wrong-owner authority directory was accepted")
	}
}

func TestSignedEvidenceRejectsEveryFieldSubstitution(t *testing.T) {
	executable := fakeExecutable(t, supportedBinary, "exit 0")
	adapter, err := New(executable, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := adapter.inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	store, evidenceDigest := signedTestAuthority(t, identity)
	leaf := filepath.Join(store.root, strings.TrimPrefix(evidenceDigest, "sha256:")+".json")
	original, err := os.ReadFile(leaf)
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	if err := json.Unmarshal(original, &base); err != nil {
		t.Fatal(err)
	}
	replacements := map[string]any{
		"runnerId": "replacement-runner", "runnerVersion": "replacement-version", "observedAt": "2026-01-01T00:00:00Z", "validUntil": "2026-01-01T00:00:01Z",
		"adapterVersion": "replacement-adapter", "executable": "/replacement/qodercli", "executableDigest": digest("f"), "binaryVersion": "1.1.24", "qodercliVersion": "1.1.24",
		"hostOs": "replacement-os", "hostArch": "replacement-arch", "hostFingerprint": digest("f"), "authorityGeneration": float64(2),
		"probeSuiteDigest": digest("f"), "probeArtifactDigest": digest("f"), "challengeDigest": digest("f"), "capabilitiesDigest": digest("f"), "probeProfileDigest": digest("f"),
		"argvDigest": digest("f"), "environmentDigest": digest("f"), "toolPolicyDigest": digest("f"), "transcriptDigest": digest("f"),
		"credentialVerified": false, "liveProtocolVerified": false, "workspaceWriteVerified": false,
		"eventContract": "replacement-event", "protocolVersion": "replacement-protocol", "permissionMode": "replacement-permission", "trustRootKeyId": "replacement-root",
	}
	for field, replacement := range replacements {
		t.Run(field, func(t *testing.T) {
			document := make(map[string]any, len(base))
			for key, value := range base {
				document[key] = value
			}
			document[field] = replacement
			data, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(leaf, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.resolve(context.Background(), evidenceDigest, time.Now().UTC()); err == nil {
				t.Fatalf("signed evidence substitution for %s was accepted", field)
			}
			if err := os.WriteFile(leaf, original, 0o600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCandidateAuthorityRejectsSamePlatformDifferentHost(t *testing.T) {
	executable := fakeExecutable(t, supportedBinary, "exit 0")
	validator := newValidator(t)
	adapter, err := New(executable, validator)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := adapter.inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	store, evidenceDigest := signedTestAuthorityWindowForHost(t, identity, now.Add(-time.Minute), now.Add(time.Hour), digest("f"))
	evidence, err := store.resolve(context.Background(), evidenceDigest, now)
	if err != nil {
		t.Fatal(err)
	}
	config := AuthorityConfig{EvidenceDigest: evidenceDigest, ProbeArtifactDigest: evidence.ProbeArtifactDigest, AuthorityGeneration: evidence.AuthorityGeneration}
	if err := adapter.bindVerifiedConformance(identity, evidence, config, nil); !errors.Is(err, ErrIdentityDrift) {
		t.Fatalf("same-platform evidence from another host was accepted: %v", err)
	}
}

func signedTestAuthority(t *testing.T, identity executableIdentity) (*AuthorityEvidenceStore, string) {
	t.Helper()
	now := time.Now().UTC()
	return signedTestAuthorityWindow(t, identity, now.Add(-time.Minute), now.Add(time.Hour))
}

func signedTestAuthorityWindow(t *testing.T, identity executableIdentity, observedAt, validUntil time.Time) (*AuthorityEvidenceStore, string) {
	return signedTestAuthorityWindowForHost(t, identity, observedAt, validUntil, mustHostFingerprint(t))
}

func signedTestAuthorityWindowForHost(t *testing.T, identity executableIdentity, observedAt, validUntil time.Time, hostFingerprint string) (*AuthorityEvidenceStore, string) {
	return signedTestAuthorityWindowGeneration(t, identity, observedAt, validUntil, hostFingerprint, 1)
}

func signedTestAuthorityWindowGeneration(t *testing.T, identity executableIdentity, observedAt, validUntil time.Time, hostFingerprint string, generation uint64) (*AuthorityEvidenceStore, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := realTempDir(t)
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	evidence := ConformanceEvidence{
		RunnerID: "marshal-conformance", RunnerVersion: "1", ObservedAt: observedAt.Format(time.RFC3339Nano), ValidUntil: validUntil.Format(time.RFC3339Nano),
		AdapterVersion: adapterVersion, Executable: identity.path, ExecutableDigest: identity.digest, BinaryVersion: identity.version, HostOS: runtime.GOOS, HostArch: runtime.GOARCH, HostFingerprint: hostFingerprint, AuthorityGeneration: generation,
		ProbeSuiteDigest: expectedProbeSuiteDigest(), ProbeArtifactDigest: digest("a"), ChallengeDigest: digest("c"), CapabilitiesDigest: expectedCapabilitiesDigest(), ProbeProfileDigest: expectedProbeProfileDigest(), ArgvDigest: expectedProbeArgvDigest(), EnvironmentDigest: expectedProbeEnvironmentDigest(), ToolPolicyDigest: expectedProbeToolPolicyDigest(), TranscriptDigest: digest("b"), ExecutionReceiptDigest: digest("e"), ExecutionReceiptDigests: []string{digest("a"), digest("b"), digest("c"), digest("d")}, EvidenceClass: candidateEvidenceClassLive, ReceiptAuthorityKeyID: "receipt-root", ReceiptAuthorityPublicKeyDigest: digest("a"), VerifierKeyID: "verifier-root", VerifierPublicKeyDigest: digest("b"), CredentialVerified: true, LiveProtocolVerified: true, WorkspaceWriteVerified: true,
		EventContract: conformanceEventContract, QoderCLIVersion: identity.version, ProtocolVersion: qoderProtocolVersion, PermissionMode: qoderPermissionMode,
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

func mustHostFingerprint(t *testing.T) string {
	t.Helper()
	value, err := currentHostFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func realTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func realPrivateTempDir(t *testing.T) string {
	t.Helper()
	path := realTempDir(t)
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConformanceExpiryRevokesProbeAndRunAdmission(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched-after-expiry")
	fixture := newRunFixture(t, supportedBinary, "touch "+shellQuote(marker)+"\n"+successEvents("provider/model"))
	fixture.adapter.mu.Lock()
	fixture.adapter.conformance = nil
	fixture.adapter.mu.Unlock()
	boundAt := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	current := boundAt
	fixture.adapter.now = func() time.Time { return current }
	identity, err := fixture.adapter.inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	store, evidenceDigest := signedTestAuthorityWindow(t, identity, boundAt.Add(-time.Minute), boundAt.Add(time.Minute))
	evidence, err := store.resolve(context.Background(), evidenceDigest, boundAt)
	if err != nil {
		t.Fatal(err)
	}
	config := AuthorityConfig{EvidenceDigest: evidenceDigest, ProbeArtifactDigest: evidence.ProbeArtifactDigest, AuthorityGeneration: evidence.AuthorityGeneration}
	if err := fixture.adapter.bindVerifiedConformance(identity, evidence, config, nil); err != nil {
		t.Fatal(err)
	}
	current = boundAt.Add(2 * time.Minute)
	probe, err := fixture.adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(probe.Data), `"probeStatus":"unsupported"`) {
		t.Fatalf("expired conformance probe = %s", probe.Data)
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrConformancePending) || !port.IsPermanent(err) {
		t.Fatalf("expired conformance Run error = %v, want permanent pending", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("worker launched after conformance expiry")
	}
}

func TestConformanceExpiryAtLaunchBoundaryPreventsWorkerStart(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched-after-admission-window")
	fixture := newRunFixture(t, supportedBinary, "touch "+shellQuote(marker)+"\n"+successEvents("provider/model"))
	beforeExpiry := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	afterExpiry := beforeExpiry.Add(2 * time.Minute)
	fixture.adapter.mu.Lock()
	fixture.adapter.conformance.validUntil = beforeExpiry.Add(time.Minute)
	fixture.adapter.mu.Unlock()
	var calls int
	fixture.adapter.now = func() time.Time {
		calls++
		if calls == 1 {
			return beforeExpiry
		}
		return afterExpiry
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrConformancePending) || !port.IsPermanent(err) {
		t.Fatalf("launch-boundary expiry error = %v, want permanent pending", err)
	}
	if calls < 2 {
		t.Fatalf("conformance clock calls = %d, want admission and launch-boundary checks", calls)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("worker launched after conformance expired during preparation")
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
	args := buildArgs("provider/model", "/managed/config", "/worktree", false)
	want := []string{"--print", "--output-format", "stream-json", "--permission-mode", "accept_edits", "--no-session-persistence", "--config-dir", "/managed/config", "--setting-sources", "", "--cwd", "/worktree", "--model", "provider/model"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v", args)
	}
}

func TestBuildArgsRejectsFabricatedRunSandboxArgv(t *testing.T) {
	args := buildArgs("", "/isolated/config", "/worktree", false)
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
	noModel := buildArgs("", "/isolated/config", "/worktree", false)
	if strings.Contains(strings.Join(noModel, "\x00"), "--model") {
		t.Fatalf("empty model must not emit --model: %#v", noModel)
	}
}

func TestBuildArgsDisablesAllToolsForExplicitEmptyAllowlist(t *testing.T) {
	args := buildArgs("", "/isolated/config", "/worktree", true)
	if !containsSequence(args, "--tools", "") {
		t.Fatalf("explicit empty allowlist must disable all tools: %#v", args)
	}
}

func TestExpectedProbeArgvDigestCoversEveryExecutionVariant(t *testing.T) {
	variants := expectedProbeArgvVariants()
	want := [][]string{
		buildArgs("", "$ISOLATED_CONFIG_DIR", "$ISOLATED_WORKTREE", false),
		buildArgs("$MODEL", "$ISOLATED_CONFIG_DIR", "$ISOLATED_WORKTREE", false),
		buildArgs("", "$ISOLATED_CONFIG_DIR", "$ISOLATED_WORKTREE", true),
		buildArgs("$MODEL", "$ISOLATED_CONFIG_DIR", "$ISOLATED_WORKTREE", true),
	}
	if !reflect.DeepEqual(variants, want) {
		t.Fatalf("probe argv variants = %#v, want %#v", variants, want)
	}
	if containsSequence(variants[0], "--model", "$MODEL") || containsSequence(variants[0], "--tools", "") {
		t.Fatalf("omitted model/tools variant is not exact: %#v", variants[0])
	}
	if !containsSequence(variants[1], "--model", "$MODEL") || containsSequence(variants[1], "--tools", "") {
		t.Fatalf("model-only variant is not exact: %#v", variants[1])
	}
	if containsSequence(variants[2], "--model", "$MODEL") || !containsSequence(variants[2], "--tools", "") {
		t.Fatalf("explicit-empty tools variant is not exact: %#v", variants[2])
	}
	if !containsSequence(variants[3], "--model", "$MODEL") || !containsSequence(variants[3], "--tools", "") {
		t.Fatalf("model plus explicit-empty tools variant is not exact: %#v", variants[3])
	}
	data, err := json.Marshal(variants)
	if err != nil {
		t.Fatal(err)
	}
	if got := digestBytes(data); got != expectedProbeArgvDigest() {
		t.Fatalf("probe argv digest = %s, want %s", expectedProbeArgvDigest(), got)
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
	probeRoot := t.TempDir()
	joined := strings.Join(probeEnvironment(probeRoot), "\n")
	if strings.Contains(joined, "secret-user") {
		t.Fatalf("probe environment leaked ambient home/config: %s", joined)
	}
	for _, want := range []string{"HOME=" + probeRoot, "XDG_CONFIG_HOME=" + filepath.Join(probeRoot, "xdg-config"), "XDG_CACHE_HOME=" + filepath.Join(probeRoot, "xdg-cache"), "XDG_DATA_HOME=" + filepath.Join(probeRoot, "xdg-data"), "XDG_STATE_HOME=" + filepath.Join(probeRoot, "xdg-state")} {
		if !strings.Contains(joined, want) {
			t.Fatalf("probe environment missing %s: %s", want, joined)
		}
	}
}

func TestVersionProbeUsesPrivateWritableTemporaryHome(t *testing.T) {
	ambientHome := t.TempDir()
	t.Setenv("HOME", ambientHome)
	capture := filepath.Join(t.TempDir(), "probe-home")
	script := `#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "--version" ]; then
    test -d "$HOME" && test -w "$HOME" || exit 9
    printf '%s' "$HOME" > ` + shellQuote(capture) + `
    : > "$HOME/probe-write"
    printf '%s\n' '1.1.23'
    exit 0
  fi
done
exit 2
`
	executable := filepath.Join(t.TempDir(), "qodercli")
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	version, err := readBinaryVersion(context.Background(), executable)
	if err != nil || version != supportedBinary {
		t.Fatalf("version = %q err=%v", version, err)
	}
	home, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if string(home) == ambientHome || string(home) == "/nonexistent" || !strings.Contains(string(home), "marshal-qoder-probe-") {
		t.Fatalf("probe HOME was not an isolated temporary root: %q", home)
	}
	if _, err := os.Stat(string(home)); !os.IsNotExist(err) {
		t.Fatalf("probe root was not removed after probe: %v", err)
	}
}

func TestVersionProbeBoundsOutputAndKillsProcessGroup(t *testing.T) {
	t.Run("stdout limit", func(t *testing.T) {
		executable := filepath.Join(t.TempDir(), "qodercli")
		script := `#!/bin/sh
while :; do printf '0123456789abcdef'; done
`
		if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := readBinaryVersion(context.Background(), executable)
		if err == nil || err.Error() != "probe qoder version: qoder version output exceeds byte limit" {
			t.Fatalf("error = %v, want stable output-limit error", err)
		}
	})
	t.Run("stderr limit", func(t *testing.T) {
		executable := filepath.Join(t.TempDir(), "qodercli")
		script := `#!/bin/sh
while :; do printf '0123456789abcdef' >&2; done
`
		if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := readBinaryVersion(context.Background(), executable)
		if err == nil || err.Error() != "probe qoder version: qoder version output exceeds byte limit" {
			t.Fatalf("error = %v, want stable output-limit error", err)
		}
	})

	t.Run("child holding pipe", func(t *testing.T) {
		root := t.TempDir()
		executable := filepath.Join(root, "qodercli")
		pidPath := filepath.Join(root, "child.pid")
		script := `#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "--version" ]; then
    sleep 60 &
    child=$!
    printf '%s' "$child" > ` + shellQuote(pidPath) + `
    printf '1.1.23\n'
    exit 0
  fi
done
exit 2
`
		if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		version, err := readBinaryVersion(context.Background(), executable)
		if err != nil || version != supportedBinary {
			t.Fatalf("version = %q err=%v", version, err)
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
				t.Fatalf("version probe child %d survived process-group cleanup: %v", pid, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
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
	body := successEvents("provider/model")
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
	if err != nil || !strings.Contains(string(transcript), `"session_id":"sess-1"`) {
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

func TestRunRejectsExecutableDriftAfterConformance(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, supportedBinary, "touch "+shellQuote(marker))
	if err := os.WriteFile(fixture.executable, []byte(fakeScript(supportedBinary, "touch "+shellQuote(marker)+"\n# changed")), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrIdentityDrift) {
		t.Fatalf("error = %v, want identity drift", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("drifted executable launched")
	}
}

func TestLaunchSnapshotSurvivesConfiguredExecutableReplacement(t *testing.T) {
	oldMarker, newMarker := filepath.Join(t.TempDir(), "old"), filepath.Join(t.TempDir(), "new")
	executable := fakeExecutable(t, supportedBinary, "touch "+shellQuote(oldMarker))
	identity := executableIdentity{path: executable, version: supportedBinary}
	var err error
	identity.digest, err = digestFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, cleanup, err := snapshotExecutable(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := os.WriteFile(executable, []byte(fakeScript(supportedBinary, "touch "+shellQuote(newMarker))), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(snapshot).Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldMarker); err != nil {
		t.Fatalf("immutable launch object did not run inspected bytes: %v", err)
	}
	if _, err := os.Stat(newMarker); !os.IsNotExist(err) {
		t.Fatal("replacement executable was launched")
	}
}

func TestRunKeepsEvidenceOnTrustedDirectoryHandleAfterRenameAndSymlink(t *testing.T) {
	body := successEvents("provider/model") + `
marshal_root=$(dirname "$(dirname "$HOME")")
mv "$marshal_root/output" "$marshal_root/output-held"
mkdir -p "$PWD/escaped-output"
ln -s "$PWD/escaped-output" "$marshal_root/output"`
	fixture := newRunFixture(t, supportedBinary, body)
	_, err := fixture.adapter.Run(context.Background(), fixture.request)
	if failure, ok := port.AsAdapterFailure(err); !ok || failure.Kind != port.FailureKindProtocolInvalid {
		t.Fatalf("error = %v, want typed protocol-invalid boundary failure", err)
	}
	for _, name := range []string{"qoder-transcript.jsonl", "qoder-stderr.log", "qoder-transcript-meta.json"} {
		if _, err := os.Stat(filepath.Join(fixture.controlRoot, "output-held", name)); err != nil {
			t.Fatalf("trusted evidence %s missing after directory replacement: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(fixture.worktree, "escaped-output", name)); !os.IsNotExist(err) {
			t.Fatalf("evidence %s escaped through replacement symlink", name)
		}
	}
}

func TestRunRejectsClaimedLeafUnlinkAndReplacement(t *testing.T) {
	declared, err := json.Marshal(validDeclaredResult("/worker/claim"))
	if err != nil {
		t.Fatal(err)
	}
	body := successEvents("provider/model") + `
marshal_root=$(dirname "$(dirname "$HOME")")
printf '%s' ` + shellQuote(string(declared)) + ` > "$marshal_root/output/worker-result.json"
rm "$marshal_root/output/worker-result.json"
printf '%s' 'forged replacement' > "$marshal_root/output/worker-result.json"`
	fixture := newRunFixtureWithResult(t, supportedBinary, body, nil)
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil {
		t.Fatal("Run accepted an unlinked and replaced WorkerResult leaf")
	}
	data, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "worker-result.json"))
	if err != nil || string(data) != "forged replacement" {
		t.Fatalf("replacement fixture was not established: %q, %v", data, err)
	}
}

func TestRunRejectsRealProtocolContractDrift(t *testing.T) {
	for _, drift := range []struct{ old, replacement string }{
		{`"qodercli_version":"1.1.23"`, `"qodercli_version":"1.1.24"`},
		{`"protocol_version":"1.2.0"`, `"protocol_version":"1.3.0"`},
		{`"permissionMode":"acceptEdits"`, `"permissionMode":"bypassPermissions"`},
	} {
		body := strings.Replace(successEvents("provider/model"), drift.old, drift.replacement, 1)
		fixture := newRunFixture(t, supportedBinary, body)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrProtocol) {
			t.Fatalf("drift %s error = %v, want protocol-invalid", drift.replacement, err)
		}
	}
}

func TestReadBoundedWithinRejectsSymlinkInputs(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("must-not-read"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "prompt.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedWithin(root, "prompt.md", 1024); err == nil {
		t.Fatal("fd-based input reader followed a symlink")
	}
}

func TestRunBindsReportedModelToSystemEvent(t *testing.T) {
	t.Run("requested mismatch", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, successEvents("different/model"))
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		if !errors.Is(err, ErrProtocol) {
			t.Fatalf("error = %v, want model mismatch protocol failure", err)
		}
	})
	t.Run("unspecified uses observed", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, successEvents("actual/model"))
		writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{}})
		record, err := fixture.adapter.Run(context.Background(), fixture.request)
		if err != nil {
			t.Fatal(err)
		}
		var result declaredResult
		if err := json.Unmarshal(record.Data, &result); err != nil {
			t.Fatal(err)
		}
		if result.Adapter.Model != "actual/model" {
			t.Fatalf("model = %q, want observed system model", result.Adapter.Model)
		}
	})
}

func TestRunRejectsStaleResultLeafBeforeLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, supportedBinary, "touch "+shellQuote(marker))
	writeJSON(t, filepath.Join(fixture.controlRoot, "output", "worker-result.json"), validDeclaredResult("/stale"))
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "pre-exists") {
		t.Fatalf("error = %v, want stale result rejection", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("worker launched with a stale result leaf")
	}
}

func TestRunRejectsSymlinkedOutputAncestorWithMissingSuffix(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, supportedBinary, "touch "+shellQuote(marker))
	outside := t.TempDir()
	output := filepath.Join(fixture.controlRoot, "output")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(output, "escape")); err != nil {
		t.Fatal(err)
	}
	request := fixture.requestWith(map[string]any{"resultPath": "output/escape/missing/worker-result.json"})
	if _, err := fixture.adapter.Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %v, want output ancestor symlink rejection", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "missing")); !os.IsNotExist(err) {
		t.Fatal("output directory was created outside the control root")
	}
}

func TestRunRejectsNamedWorkerToolsBeforeLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, supportedBinary, "touch "+shellQuote(marker))
	writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{"model": "provider/model", "tools": []string{"read"}}})
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrUnsupportedWorkerTools) {
		t.Fatalf("error = %v, want unsupported worker tools", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("worker launched with an unverified named tool mapping")
	}
}

func TestRunClearsWorkerClaimedModelWhenTaskSpecOmitsModel(t *testing.T) {
	claimed := validDeclaredResult("/worker/claim")
	claimed["adapter"].(map[string]any)["model"] = "worker-secret-model"
	fixture := newRunFixtureWithResult(t, supportedBinary, successEvents("provider/model"), claimed)
	writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{}})
	record, err := fixture.adapter.Run(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(record.Data), "worker-secret-model") {
		t.Fatalf("normalized result retained worker-claimed model: %s", record.Data)
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
		body := successEvents("provider/model")
		data := validDeclaredResult("/worker/claim")
		data["taskId"] = "OTHER"
		fixture := newRunFixtureWithResult(t, supportedBinary, body, data)
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
	body := successEvents("provider/model")
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
