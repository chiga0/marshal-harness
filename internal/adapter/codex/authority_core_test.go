//go:build linux || darwin

package codex

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

func testDigest(label string) string { return digestBytesHex([]byte(label)) }

func realTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func testNonce(fill byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

func testRoot(t *testing.T, id string) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	seed := sha256.Sum256([]byte("marshal-codex-hermetic-" + id))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	return privateKey.Public().(ed25519.PublicKey), privateKey
}

func buildTestSignedEnvelope(t *testing.T, payload any, domain string, signers map[string]ed25519.PrivateKey) SignedEnvelopeV1 {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	canonicalPayload, err := canonical.JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	digest := canonical.DigestBytes(canonicalPayload)
	var schema struct {
		SchemaVersion string `json:"schemaVersion"`
	}
	if err := json.Unmarshal(canonicalPayload, &schema); err != nil {
		t.Fatal(err)
	}
	projection := struct {
		Domain        string `json:"domain"`
		PayloadDigest string `json:"payloadDigest"`
		SchemaVersion string `json:"schemaVersion"`
	}{domain, digest, schema.SchemaVersion}
	projectionRaw, _ := json.Marshal(projection)
	message, _ := canonical.JSON(projectionRaw)
	ids := make([]string, 0, len(signers))
	for id := range signers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	signatures := make([]SignatureV1, 0, len(ids))
	for _, id := range ids {
		signatures = append(signatures, SignatureV1{"Ed25519", id, base64.StdEncoding.EncodeToString(ed25519.Sign(signers[id], message))})
	}
	return SignedEnvelopeV1{canonicalPayload, digest, signatures}
}

func testState(t *testing.T, authorityGeneration, trustGeneration uint64, keyset, config string) CodexConsumerAuthorityStateV1 {
	t.Helper()
	publicKey, _ := testRoot(t, "root")
	now := time.Date(2026, 8, 18, 1, 2, int(authorityGeneration), 0, time.UTC)
	pin := CodexActiveRootPinV1{
		SchemaVersion: activeRootPinSchema, AuthorityNamespace: "marshal.codex.production",
		BootstrapDigest: testDigest("bootstrap"), RootKeyID: "root", RootAlgorithm: "Ed25519",
		RootPublicKey: base64.StdEncoding.EncodeToString(publicKey), RootPublicKeyDigest: canonicalDigestBytes(publicKey),
		TrustRootGeneration: trustGeneration, KeysetDigest: keyset, ActivatedAt: formatAuthorityTime(now),
	}
	fence := CodexConsumerFenceV1{
		SchemaVersion: consumerFenceSchema, AuthorityNamespace: pin.AuthorityNamespace, AdapterID: adapterID,
		BootstrapDigest: pin.BootstrapDigest, HostIdentityDigest: testDigest("host"), BootstrapID: testNonce(1),
		TrustRootGeneration: trustGeneration, AuthorityGeneration: authorityGeneration, KeysetDigest: keyset,
		ConfigDigest: config, RevocationSetDigest: testDigest("revocations"), CurrentEvidenceDigest: testDigest("evidence"),
	}
	state, err := NewConsumerAuthorityState(pin, fence, now)
	if err != nil {
		t.Fatalf("new state: %v", err)
	}
	return state
}

func TestConsumerAuthorityStateAtomicCommitRecoveryAndReplay(t *testing.T) {
	stateRoot, authorityRoot := realTempDir(t), realTempDir(t)
	store, err := OpenCodexConsumerAuthorityStore(stateRoot, authorityRoot)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	first := testState(t, 1, 1, testDigest("keyset-1"), testDigest("config-1"))
	if err := store.Commit(first); err != nil {
		t.Fatalf("commit first: %v", err)
	}
	if err := store.Commit(first); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	recovered, exists, err := store.Recover(func(state CodexConsumerAuthorityStateV1) error {
		if state.Fence.ConfigDigest != first.Fence.ConfigDigest {
			return errors.New("wrong current authority")
		}
		return nil
	})
	if err != nil || !exists || !stateAuthorityIdentityEqual(first, recovered) {
		t.Fatalf("recover = %#v, %v, %v", recovered, exists, err)
	}

	rollback := testState(t, 1, 1, testDigest("keyset-1"), testDigest("config-other"))
	if err := store.Commit(rollback); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("same-generation conflict = %v", err)
	}

	second := testState(t, 2, 1, testDigest("keyset-1"), testDigest("config-2"))
	second.ActiveRootPin.ActivatedAt = first.ActiveRootPin.ActivatedAt
	crash := errors.New("simulated crash")
	if err := store.commit(second, func(phase string) error {
		if phase == "temp-synced" {
			return crash
		}
		return nil
	}, nil, time.Time{}); !errors.Is(err, crash) {
		t.Fatalf("pre-rename crash = %v", err)
	}
	current, _, err := store.Recover(func(state CodexConsumerAuthorityStateV1) error { return nil })
	if err != nil || current.Fence.AuthorityGeneration != 1 {
		t.Fatalf("pre-rename recovery = %d, %v", current.Fence.AuthorityGeneration, err)
	}

	if err := store.commit(second, func(phase string) error {
		if phase == "state-renamed" {
			return crash
		}
		return nil
	}, nil, time.Time{}); !errors.Is(err, crash) {
		t.Fatalf("post-rename crash = %v", err)
	}
	current, _, err = store.Recover(func(state CodexConsumerAuthorityStateV1) error {
		if state.Fence.ConfigDigest != second.Fence.ConfigDigest {
			return errors.New("new authority unavailable")
		}
		return nil
	})
	if err != nil || current.Fence.AuthorityGeneration != 2 {
		t.Fatalf("post-rename recovery = %d, %v", current.Fence.AuthorityGeneration, err)
	}
	if err := store.Commit(first); err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("rollback = %v", err)
	}
}

func TestConsumerAuthorityStateIgnoresTempAndRejectsUnsafeState(t *testing.T) {
	stateRoot, authorityRoot := realTempDir(t), realTempDir(t)
	if err := os.WriteFile(filepath.Join(stateRoot, "state.orphan.tmp"), []byte("incomplete"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenCodexConsumerAuthorityStore(stateRoot, authorityRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, exists, err := store.Recover(func(CodexConsumerAuthorityStateV1) error { return nil }); err != nil || exists {
		t.Fatalf("orphan temp recovery = %v, %v", exists, err)
	}
	if err := os.WriteFile(filepath.Join(stateRoot, consumerStateName), []byte(`{"schemaVersion":"bad"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Recover(func(CodexConsumerAuthorityStateV1) error { return nil }); err == nil {
		t.Fatal("corrupt state accepted")
	}
}

func TestConsumerAuthorityStoreRejectsOverlappingRootsAndBusyLock(t *testing.T) {
	root := realTempDir(t)
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{root, root}, {root, child}, {child, root}} {
		if store, err := OpenCodexConsumerAuthorityStore(pair[0], pair[1]); err == nil {
			store.Close()
			t.Fatalf("overlap accepted: %v", pair)
		}
	}
	stateRoot, authorityRoot := realTempDir(t), realTempDir(t)
	store, err := OpenCodexConsumerAuthorityStore(stateRoot, authorityRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lock, err := lockConsumerState(int(store.stateRoot.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer unlockConsumerState(lock)
	if err := store.Commit(testState(t, 1, 1, testDigest("keyset"), testDigest("config"))); err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatalf("busy lock = %v", err)
	}
}

func TestRootRotationRequiresOldAndNewSignatures(t *testing.T) {
	oldPublic, oldPrivate := testRoot(t, "old")
	newPublic, newPrivate := testRoot(t, "new")
	now := time.Date(2026, 8, 18, 2, 0, 0, 0, time.UTC)
	current := CodexActiveRootPinV1{
		SchemaVersion: activeRootPinSchema, AuthorityNamespace: "marshal.codex.production", BootstrapDigest: testDigest("bootstrap"),
		RootKeyID: "root-old", RootAlgorithm: "Ed25519", RootPublicKey: base64.StdEncoding.EncodeToString(oldPublic),
		RootPublicKeyDigest: canonicalDigestBytes(oldPublic), TrustRootGeneration: 1, KeysetDigest: testDigest("old-keyset"), ActivatedAt: formatAuthorityTime(now.Add(-time.Hour)),
	}
	previous := current.KeysetDigest
	keyset := CodexAuthorityKeysetV1{
		SchemaVersion: authorityKeysetSchema, AuthorityNamespace: current.AuthorityNamespace, TrustRootGeneration: 2,
		PreviousKeysetDigest: &previous, ValidFrom: formatAuthorityTime(now),
		Keys:          []AuthorityPublicKeyV1{{KeyID: "leaf", Usage: "evidence", Algorithm: "Ed25519", PublicKey: base64.StdEncoding.EncodeToString(newPublic), NotBefore: formatAuthorityTime(now), NotAfter: formatAuthorityTime(now.Add(time.Hour))}},
		RevokedKeyIDs: []string{}, RootRotation: &RootRotationV1{"root-new", "Ed25519", base64.StdEncoding.EncodeToString(newPublic), formatAuthorityTime(now)},
	}
	envelope := buildTestSignedEnvelope(t, keyset, authorityKeysetSchema, map[string]ed25519.PrivateKey{"root-old": oldPrivate, "root-new": newPrivate})
	next, err := VerifyRootRotation(current, envelope, now)
	if err != nil || next.RootKeyID != "root-new" || next.TrustRootGeneration != 2 {
		t.Fatalf("rotation = %#v, %v", next, err)
	}
	oneSignature := buildTestSignedEnvelope(t, keyset, authorityKeysetSchema, map[string]ed25519.PrivateKey{"root-new": newPrivate})
	if _, err := VerifyRootRotation(current, oneSignature, now); err == nil {
		t.Fatal("self-signed new root accepted")
	}
	stateRoot, authorityRoot := realTempDir(t), realTempDir(t)
	store, err := OpenCodexConsumerAuthorityStore(stateRoot, authorityRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	initialFence := CodexConsumerFenceV1{
		SchemaVersion: consumerFenceSchema, AuthorityNamespace: current.AuthorityNamespace, AdapterID: adapterID,
		BootstrapDigest: current.BootstrapDigest, HostIdentityDigest: testDigest("host"), BootstrapID: testNonce(1),
		TrustRootGeneration: 1, AuthorityGeneration: 1, KeysetDigest: current.KeysetDigest,
		ConfigDigest: testDigest("config-1"), RevocationSetDigest: testDigest("revoke-1"), CurrentEvidenceDigest: testDigest("evidence-1"),
	}
	initialState, err := NewConsumerAuthorityState(current, initialFence, now.Add(-time.Minute))
	if err != nil || store.Commit(initialState) != nil {
		t.Fatalf("initial root state: %v", err)
	}
	nextFence := initialFence
	nextFence.TrustRootGeneration, nextFence.AuthorityGeneration = 2, 2
	nextFence.KeysetDigest, nextFence.ConfigDigest = envelope.PayloadDigest, testDigest("config-2")
	nextFence.RevocationSetDigest, nextFence.CurrentEvidenceDigest = testDigest("revoke-2"), testDigest("evidence-2")
	nextState, err := NewConsumerAuthorityState(next, nextFence, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(nextState); err == nil || !strings.Contains(err.Error(), "rotation") {
		t.Fatalf("unsigned atomic rotation = %v", err)
	}
	if err := store.CommitRootRotation(nextState, envelope, now); err != nil {
		t.Fatalf("dual-signed atomic rotation: %v", err)
	}
	previous = envelope.PayloadDigest
	leafKeyset := CodexAuthorityKeysetV1{
		SchemaVersion: authorityKeysetSchema, AuthorityNamespace: current.AuthorityNamespace, TrustRootGeneration: 2,
		PreviousKeysetDigest: &previous, ValidFrom: formatAuthorityTime(now.Add(time.Second)),
		Keys:          []AuthorityPublicKeyV1{{KeyID: "leaf-next", Usage: "evidence", Algorithm: "Ed25519", PublicKey: base64.StdEncoding.EncodeToString(oldPublic), NotBefore: formatAuthorityTime(now), NotAfter: formatAuthorityTime(now.Add(time.Hour))}},
		RevokedKeyIDs: []string{}, RootRotation: nil,
	}
	leafEnvelope := buildTestSignedEnvelope(t, leafKeyset, authorityKeysetSchema, map[string]ed25519.PrivateKey{"root-new": newPrivate})
	leafFence := nextFence
	leafFence.AuthorityGeneration = 3
	leafFence.KeysetDigest, leafFence.ConfigDigest = leafEnvelope.PayloadDigest, testDigest("config-3")
	leafPin := next
	leafPin.KeysetDigest = leafEnvelope.PayloadDigest
	leafState, err := NewConsumerAuthorityState(leafPin, leafFence, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(leafState); err == nil {
		t.Fatal("unsigned keyset advance accepted")
	}
	if err := store.CommitKeysetAdvance(leafState, leafEnvelope, now.Add(time.Second)); err != nil {
		t.Fatalf("signed keyset advance: %v", err)
	}
}

type fakeHostTPM struct{}

func (fakeHostTPM) Attest(_ context.Context, identity LinuxHostIdentityV1, nonce []byte) ([]byte, error) {
	digest, _ := HostIdentityDigest(identity)
	sum := sha256.Sum256(append(append([]byte(nil), nonce...), []byte(digest)...))
	return append(append([]byte(nil), sum[:]...), sum[:]...), nil
}

func (fakeHostTPM) Verify(identity LinuxHostIdentityV1, nonce, signature []byte) error {
	want, _ := (fakeHostTPM{}).Attest(context.Background(), identity, nonce)
	if !bytes.Equal(want, signature) {
		return errors.New("signature mismatch")
	}
	return nil
}

func testHostIdentity() LinuxHostIdentityV1 {
	publicArea := []byte("hermetic TPM2B_PUBLIC fixture")
	return LinuxHostIdentityV1{
		SchemaVersion: hostIdentitySchema, OS: "linux", Arch: "amd64", MachineIDDigest: testDigest("machine"),
		TPMEKCertificateDigest: testDigest("ek"), TPMHostKeyPublic: base64.StdEncoding.EncodeToString(publicArea),
		TPMHostKeyPublicDigest: canonicalDigestBytes(publicArea), TPMHostKeyQualifiedNameDigest: testDigest("qualified"), BootstrapID: testNonce(2),
	}
}

func TestStableHostIdentityAllowsDistinctFreshAttestations(t *testing.T) {
	identity := testHostIdentity()
	digest, err := HostIdentityDigest(identity)
	if err != nil {
		t.Fatal(err)
	}
	firstRandom := bytes.NewReader(bytes.Repeat([]byte{3}, 32))
	secondRandom := bytes.NewReader(bytes.Repeat([]byte{4}, 32))
	first, err := FreshHostAttestation(context.Background(), identity, fakeHostTPM{}, fakeHostTPM{}, firstRandom)
	if err != nil {
		t.Fatal(err)
	}
	second, err := FreshHostAttestation(context.Background(), identity, fakeHostTPM{}, fakeHostTPM{}, secondRandom)
	if err != nil {
		t.Fatal(err)
	}
	if first.ChallengeNonce == second.ChallengeNonce || first.ChallengeSignature == second.ChallengeSignature {
		t.Fatal("fresh attestations reused operation identity")
	}
	if err := ValidateHostAttestation(first, digest, fakeHostTPM{}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateHostAttestation(second, digest, fakeHostTPM{}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateHostAttestation(first, testDigest("other-host"), fakeHostTPM{}); err == nil {
		t.Fatal("host mismatch accepted")
	}
}

func TestTopologySourceSealedChildTransition(t *testing.T) {
	fixed := SortedTopologyRoles(
		TopologyRoleV1{"worktree", FileIdentityV1{1, 1, 10, 10, 1, testDigest("work")}},
		TopologyRoleV1{"control", FileIdentityV1{1, 1, 11, 10, 1, testDigest("control")}},
	)
	source := TopologyRoleV1{"source", FileIdentityV1{1, 1, 20, 10, 100, testDigest("binary")}}
	sealed := TopologyRoleV1{"sealed", FileIdentityV1{2, 2, 30, 20, 100, source.Identity.SHA256}}
	child := TopologyRoleV1{"child", sealed.Identity}
	snapshots := []TopologySnapshotV1{
		{"marshal.codex.topology-snapshot.v1", "T0", "consumer", fixed, SortedTopologyRoles(source)},
		{"marshal.codex.topology-snapshot.v1", "T1", "launcher", fixed, SortedTopologyRoles(source, sealed)},
		{"marshal.codex.topology-snapshot.v1", "T2", "launcher", fixed, SortedTopologyRoles(source, sealed)},
		{"marshal.codex.topology-snapshot.v1", "T3", "launch-receipt-authority", fixed, SortedTopologyRoles(source, sealed, child)},
		{"marshal.codex.topology-snapshot.v1", "T4", "consumer", fixed, SortedTopologyRoles(source, sealed, child)},
	}
	if err := ValidateTopologyTransition(snapshots); err != nil {
		t.Fatalf("valid topology: %v", err)
	}
	bad := append([]TopologySnapshotV1(nil), snapshots...)
	bad[4] = snapshots[4]
	bad[4].Executables = append([]TopologyRoleV1(nil), snapshots[4].Executables...)
	bad[4].Executables[0].Identity.Inode++
	if err := ValidateTopologyTransition(bad); err == nil {
		t.Fatal("child identity drift accepted")
	}
	bad = append([]TopologySnapshotV1(nil), snapshots...)
	bad[1] = snapshots[1]
	bad[1].Executables = append([]TopologyRoleV1(nil), snapshots[1].Executables...)
	bad[1].Executables[0].Identity = source.Identity
	if err := ValidateTopologyTransition(bad); err == nil {
		t.Fatal("source/sealed alias accepted")
	}
}

func TestSeparatedWorkRootsRejectSameAndNested(t *testing.T) {
	worktree := realTempDir(t)
	control := realTempDir(t)
	roots, err := OpenSeparatedWorkRoots(worktree, control)
	if err != nil {
		t.Fatalf("open siblings: %v", err)
	}
	if err := roots.Verify(); err != nil {
		t.Fatal(err)
	}
	roots.Close()
	child := filepath.Join(worktree, "control")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{worktree, worktree}, {worktree, child}, {child, worktree}} {
		if roots, err := OpenSeparatedWorkRoots(pair[0], pair[1]); err == nil {
			roots.Close()
			t.Fatalf("overlap accepted: %v", pair)
		}
	}
	alias := filepath.Join(realTempDir(t), "alias")
	if err := os.Symlink(worktree, alias); err != nil {
		t.Fatal(err)
	}
	if roots, err := OpenSeparatedWorkRoots(alias, control); err == nil {
		roots.Close()
		t.Fatal("symlink root accepted")
	}
	original, replacement := realTempDir(t), realTempDir(t)
	pinned, err := OpenSeparatedWorkRoots(original, control)
	if err != nil {
		t.Fatal(err)
	}
	retained := original + "-retained"
	if err := os.Rename(original, retained); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, original); err != nil {
		t.Fatal(err)
	}
	if err := pinned.Verify(); err == nil {
		t.Fatal("worktree pathname relink accepted")
	}
	pinned.Close()
}

func TestProductionAuthorityConstructorRemainsHardDisabled(t *testing.T) {
	adapter, err := NewFromAuthorityConfig(context.Background(), "/not/used", nil, "/not/used")
	if adapter != nil || err == nil || !errors.Is(err, ErrCodexConformancePending) {
		t.Fatalf("production activation = %v, %v", adapter, err)
	}
	var failure *AuthorityFailure
	expectedCode := "codex_conformance_pending"
	if runtime.GOOS != "linux" {
		expectedCode = "codex_platform_unsupported"
	}
	if !errors.As(err, &failure) || failure.Code != expectedCode || failure.RetryClass != "permanent" {
		t.Fatalf("typed pending failure = %#v", failure)
	}
}

func TestExactChallengeRevocationAndWorkerZeroKeyProjection(t *testing.T) {
	receipt := CodexProbeExecutionReceiptV1{
		SchemaVersion: probeReceiptSchema, AuthorityNamespace: "marshal.codex.production", AuthorityGeneration: 3, TrustRootGeneration: 2,
		BootstrapID: testNonce(1), SuiteDigest: testDigest("suite"), ProbeArtifactDigest: testDigest("artifact"), VariantID: "success",
		ChallengeNonce: testNonce(5), StartedAt: formatAuthorityTime(time.Unix(1, 0)), EndedAt: formatAuthorityTime(time.Unix(2, 0)),
		HostIdentityDigest: testDigest("host"), BinaryIdentityDigest: testDigest("binary"), ArgvDigest: testDigest("argv"), EnvironmentDigest: testDigest("env"),
		TopologyDigest: testDigest("topology"), TranscriptDigest: testDigest("transcript"), MarkerDigest: testDigest("marker"),
		EventContractDigest: testDigest("event"), PermissionContractDigest: testDigest("permission"), ExitCode: 0, ReceiptKeyID: "receipt-key",
	}
	receipt.ReceiptChallengeDigest, _ = receiptChallengeDigest(receipt)
	receiptDigest := testDigest("receipt-envelope")
	aggregate, err := AggregateChallengeDigest([]CodexProbeExecutionReceiptV1{receipt}, map[string]string{"success": receiptDigest})
	if err != nil {
		t.Fatal(err)
	}
	evidenceDigest := testDigest("evidence-envelope")
	evidence := CodexProductionEvidenceV1{
		SchemaVersion: productionEvidenceSchema, AuthorityNamespace: receipt.AuthorityNamespace, AuthorityGeneration: 3, TrustRootGeneration: 2,
		BootstrapID: receipt.BootstrapID, EvidenceKeyID: "evidence-key", ObservationDigest: testDigest("observation"), ReceiptDigests: []string{receiptDigest},
		IssuedAt: formatAuthorityTime(time.Unix(4, 0)), ValidFrom: formatAuthorityTime(time.Unix(3, 0)), ValidUntil: formatAuthorityTime(time.Unix(5, 0)),
		HostIdentityDigest: receipt.HostIdentityDigest, BinaryIdentityDigest: receipt.BinaryIdentityDigest, ContractDigest: testDigest("contract"), ProfileDigest: testDigest("profile"),
		SuiteDigest: receipt.SuiteDigest, ProbeArtifactDigest: receipt.ProbeArtifactDigest, AggregateChallengeDigest: aggregate, TopologyDigest: receipt.TopologyDigest,
		VerifierKeyID: "verifier-key", ProbeReceiptKeyID: receipt.ReceiptKeyID, Verdicts: AuthorityVerdictsV1{true, true, true, true, true},
	}
	config := CodexAuthorityConfigV1{
		SchemaVersion: authorityConfigSchema, AuthorityNamespace: evidence.AuthorityNamespace, AuthorityGeneration: 3, TrustRootGeneration: 2,
		KeysetDigest: testDigest("keyset"), CurrentEvidenceDigest: evidenceDigest, RevokedEvidenceDigests: []string{}, RevokedSuiteDigests: []string{}, RevokedChallengeDigests: []string{},
		HostIdentityDigest: evidence.HostIdentityDigest, BootstrapID: evidence.BootstrapID, SuiteDigest: evidence.SuiteDigest, ProbeArtifactDigest: evidence.ProbeArtifactDigest,
		AggregateChallengeDigest: aggregate, ContractDigest: evidence.ContractDigest, ProfileDigest: evidence.ProfileDigest, ConfigKeyID: "config-key", IssuedAt: evidence.IssuedAt,
	}
	config.RevocationSetDigest, _ = RevocationSetDigest(config)
	configJSON, _ := json.Marshal(config)
	if _, err := ParseCodexAuthorityConfig(configJSON); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if _, err := ParseCodexAuthorityConfig([]byte(`{"schemaVersion":"marshal.codex.authority-config.v1","schemaVersion":"marshal.codex.authority-config.v1"}`)); err == nil {
		t.Fatal("duplicate config member accepted")
	}
	if _, err := ParseCodexAuthorityConfig([]byte(`{"unknown":true}`)); err == nil {
		t.Fatal("unknown config member accepted")
	}
	receiptJSON, _ := json.Marshal(receipt)
	if _, err := ParseCodexProbeReceipt(receiptJSON); err != nil {
		t.Fatalf("parse receipt: %v", err)
	}
	if err := ValidateAuthorityProjection(config, evidence, evidenceDigest, []CodexProbeExecutionReceiptV1{receipt}, map[string]string{"success": receiptDigest}); err != nil {
		t.Fatalf("projection: %v", err)
	}
	state := testState(t, 3, 2, config.KeysetDigest, testDigest("config-envelope"))
	state.Fence.RevocationSetDigest = config.RevocationSetDigest
	state.Fence.CurrentEvidenceDigest = evidenceDigest
	state.ActiveRootPin.KeysetDigest = config.KeysetDigest
	state.Fence.KeysetDigest = config.KeysetDigest
	metadata, err := NewAuthorityMetadata(config, evidence, state.ActiveRootPin, state.Fence, CodexContractMetadataInput{
		CodexVersion: "0.145.0", ArgvMatrixDigest: testDigest("argv-matrix"), EnvironmentDigest: receipt.EnvironmentDigest,
		EventContractDigest: receipt.EventContractDigest, PermissionContractDigest: receipt.PermissionContractDigest,
		ToolPolicyDigest: testDigest("tool-policy"), ResultContractDigest: testDigest("result"), OutputLimitDigest: testDigest("limit"),
		NativeBudgetsDigest: testDigest("budgets"), ExecutionProfiles: []string{"read-only", "workspace-write"},
	}, time.Unix(4, 0).UTC())
	if err != nil || metadata.EvidenceDigest != evidenceDigest || metadata.FenceDigest == "" || metadata.IsolationClaim == "" {
		t.Fatalf("metadata = %#v, %v", metadata, err)
	}
	revoked := config
	revoked.RevokedChallengeDigests = []string{receipt.ReceiptChallengeDigest}
	revoked.RevocationSetDigest, _ = RevocationSetDigest(revoked)
	if err := ValidateAuthorityProjection(revoked, evidence, evidenceDigest, []CodexProbeExecutionReceiptV1{receipt}, map[string]string{"success": receiptDigest}); err == nil {
		t.Fatal("component challenge revocation accepted")
	}
	worker := CodexWorkerAuthorityContextV1{workerAuthoritySchema, config.CurrentEvidenceDigest, evidenceDigest, testDigest("fence"), testDigest("launch"), 0}
	if err := worker.Validate(); err != nil {
		t.Fatal(err)
	}
	worker.AuthoritySigningPrivateKeyCount = 1
	if err := worker.Validate(); err == nil {
		t.Fatal("worker authority private key accepted")
	}
}

func TestStateLockFileIsPrivateSingleLink(t *testing.T) {
	stateRoot, authorityRoot := realTempDir(t), realTempDir(t)
	store, err := OpenCodexConsumerAuthorityStore(stateRoot, authorityRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Commit(testState(t, 1, 1, testDigest("keyset"), testDigest("config"))); err != nil {
		t.Fatal(err)
	}
	var stat unix.Stat_t
	if err := unix.Stat(filepath.Join(stateRoot, consumerLockName), &stat); err != nil {
		t.Fatal(err)
	}
	if stat.Mode&0o7777 != 0o600 || stat.Nlink != 1 {
		t.Fatalf("lock mode/link = %o/%d", stat.Mode&0o7777, stat.Nlink)
	}
}
