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
	"github.com/chiga0/marshal-harness/internal/port"
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

func testBootstrap(t *testing.T) CodexConsumerBootstrapV1 {
	t.Helper()
	identity := testHostIdentity()
	hostDigest, err := HostIdentityDigest(identity)
	if err != nil {
		t.Fatal(err)
	}
	return CodexConsumerBootstrapV1{
		SchemaVersion: consumerBootstrapSchema, AuthorityNamespace: "marshal.codex.production", AdapterID: adapterID,
		BootstrapID: identity.BootstrapID, HostIdentityDigest: hostDigest, MachineIDDigest: identity.MachineIDDigest,
		TPMHostKeyPublic: identity.TPMHostKeyPublic, TPMHostKeyPublicDigest: identity.TPMHostKeyPublicDigest,
		TPMHostKeyQualifiedNameDigest: identity.TPMHostKeyQualifiedNameDigest, CreatedAt: "2026-08-18T01:00:00Z",
	}
}

func testBootstrapRoot(t *testing.T, label, keyID string) CodexBootstrapRootIdentityV1 {
	t.Helper()
	publicKey, _ := testRoot(t, label)
	return CodexBootstrapRootIdentityV1{
		AuthorityNamespace: "marshal.codex.production", RootKeyID: keyID, RootAlgorithm: "Ed25519",
		RootPublicKey: base64.StdEncoding.EncodeToString(publicKey), RootPublicKeyDigest: canonicalDigestBytes(publicKey), TrustRootGeneration: 1,
	}
}

func commitTestBootstrap(t *testing.T, store *CodexConsumerAuthorityStore, state CodexConsumerAuthorityStateV1) CodexConsumerAuthorityStateV1 {
	t.Helper()
	bootstrap := testBootstrap(t)
	bootstrapDigest, err := BootstrapDigest(bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	rootPublic, rootPrivate := testRoot(t, "root")
	configPublic, _ := testRoot(t, "bootstrap-config")
	keyset := CodexAuthorityKeysetV1{
		SchemaVersion: authorityKeysetSchema, AuthorityNamespace: state.Fence.AuthorityNamespace, TrustRootGeneration: state.Fence.TrustRootGeneration,
		PreviousKeysetDigest: nil, ValidFrom: "2026-08-18T00:00:00Z", Keys: []AuthorityPublicKeyV1{{KeyID: "config", Usage: "config", Algorithm: "Ed25519", PublicKey: base64.StdEncoding.EncodeToString(configPublic), NotBefore: "2026-08-18T00:00:00Z", NotAfter: "2026-08-19T00:00:00Z"}}, RevokedKeyIDs: []string{}, RootRotation: nil,
	}
	envelope := buildTestSignedEnvelope(t, keyset, authorityKeysetSchema, map[string]ed25519.PrivateKey{"root": rootPrivate})
	state.ActiveRootPin.BootstrapDigest, state.Fence.BootstrapDigest = bootstrapDigest, bootstrapDigest
	state.Fence.BootstrapID, state.Fence.HostIdentityDigest = bootstrap.BootstrapID, bootstrap.HostIdentityDigest
	state.ActiveRootPin.RootPublicKey = base64.StdEncoding.EncodeToString(rootPublic)
	state.ActiveRootPin.RootPublicKeyDigest = canonicalDigestBytes(rootPublic)
	state.ActiveRootPin.KeysetDigest, state.Fence.KeysetDigest = envelope.PayloadDigest, envelope.PayloadDigest
	if err := store.CommitBootstrap(bootstrap, testHostIdentity(), state, envelope, time.Date(2026, 8, 18, 1, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("commit bootstrap: %v", err)
	}
	return state
}

func TestConsumerAuthorityStateAtomicCommitRecoveryAndReplay(t *testing.T) {
	stateRoot, authorityRoot := realTempDir(t), realTempDir(t)
	store, err := OpenCodexConsumerAuthorityStore(stateRoot, authorityRoot, testBootstrapRoot(t, "root", "root"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	first := testState(t, 1, 1, testDigest("keyset-1"), testDigest("config-1"))
	if err := store.Commit(first); err == nil || !strings.Contains(err.Error(), "bootstrap") {
		t.Fatalf("unproved first commit = %v", err)
	}
	first = commitTestBootstrap(t, store, first)
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

	rollback := first
	rollback.Fence.ConfigDigest = testDigest("config-other")
	if err := store.Commit(rollback); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("same-generation conflict = %v", err)
	}

	secondFence := first.Fence
	secondFence.AuthorityGeneration = 2
	secondFence.ConfigDigest = testDigest("config-2")
	second, err := NewConsumerAuthorityState(first.ActiveRootPin, secondFence, time.Date(2026, 8, 18, 1, 3, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
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
	store, err := OpenCodexConsumerAuthorityStore(stateRoot, authorityRoot, testBootstrapRoot(t, "root", "root"))
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
		if store, err := OpenCodexConsumerAuthorityStore(pair[0], pair[1], testBootstrapRoot(t, "root", "root")); err == nil {
			store.Close()
			t.Fatalf("overlap accepted: %v", pair)
		}
	}
	stateRoot, authorityRoot := realTempDir(t), realTempDir(t)
	store, err := OpenCodexConsumerAuthorityStore(stateRoot, authorityRoot, testBootstrapRoot(t, "root", "root"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lock, err := lockConsumerState(int(store.stateRoot.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer unlockConsumerState(lock)
	if err := store.Commit(testState(t, 2, 1, testDigest("keyset"), testDigest("config"))); err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatalf("busy lock = %v", err)
	}
}

func TestRootRotationRequiresOldAndNewSignatures(t *testing.T) {
	oldPublic, oldPrivate := testRoot(t, "old")
	newPublic, newPrivate := testRoot(t, "new")
	now := time.Date(2026, 8, 18, 2, 0, 0, 0, time.UTC)
	bootstrap := testBootstrap(t)
	bootstrapDigest, _ := BootstrapDigest(bootstrap)
	initialLeafPublic, _ := testRoot(t, "initial-leaf")
	initialKeyset := CodexAuthorityKeysetV1{
		SchemaVersion: authorityKeysetSchema, AuthorityNamespace: "marshal.codex.production", TrustRootGeneration: 1,
		PreviousKeysetDigest: nil, ValidFrom: formatAuthorityTime(now.Add(-time.Hour)),
		Keys: []AuthorityPublicKeyV1{{KeyID: "leaf-initial", Usage: "evidence", Algorithm: "Ed25519", PublicKey: base64.StdEncoding.EncodeToString(initialLeafPublic), NotBefore: formatAuthorityTime(now.Add(-time.Hour)), NotAfter: formatAuthorityTime(now.Add(time.Hour))}}, RevokedKeyIDs: []string{}, RootRotation: nil,
	}
	initialEnvelope := buildTestSignedEnvelope(t, initialKeyset, authorityKeysetSchema, map[string]ed25519.PrivateKey{"root-old": oldPrivate})
	current := CodexActiveRootPinV1{
		SchemaVersion: activeRootPinSchema, AuthorityNamespace: "marshal.codex.production", BootstrapDigest: bootstrapDigest,
		RootKeyID: "root-old", RootAlgorithm: "Ed25519", RootPublicKey: base64.StdEncoding.EncodeToString(oldPublic),
		RootPublicKeyDigest: canonicalDigestBytes(oldPublic), TrustRootGeneration: 1, KeysetDigest: initialEnvelope.PayloadDigest, ActivatedAt: formatAuthorityTime(now.Add(-time.Hour)),
	}
	previous := current.KeysetDigest
	rotationLeafPublic, _ := testRoot(t, "rotation-leaf")
	keyset := CodexAuthorityKeysetV1{
		SchemaVersion: authorityKeysetSchema, AuthorityNamespace: current.AuthorityNamespace, TrustRootGeneration: 2,
		PreviousKeysetDigest: &previous, ValidFrom: formatAuthorityTime(now),
		Keys:          []AuthorityPublicKeyV1{{KeyID: "leaf", Usage: "evidence", Algorithm: "Ed25519", PublicKey: base64.StdEncoding.EncodeToString(rotationLeafPublic), NotBefore: formatAuthorityTime(now), NotAfter: formatAuthorityTime(now.Add(time.Hour))}},
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
	store, err := OpenCodexConsumerAuthorityStore(stateRoot, authorityRoot, testBootstrapRoot(t, "old", "root-old"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	initialFence := CodexConsumerFenceV1{
		SchemaVersion: consumerFenceSchema, AuthorityNamespace: current.AuthorityNamespace, AdapterID: adapterID,
		BootstrapDigest: current.BootstrapDigest, HostIdentityDigest: bootstrap.HostIdentityDigest, BootstrapID: bootstrap.BootstrapID,
		TrustRootGeneration: 1, AuthorityGeneration: 1, KeysetDigest: current.KeysetDigest,
		ConfigDigest: testDigest("config-1"), RevocationSetDigest: testDigest("revoke-1"), CurrentEvidenceDigest: testDigest("evidence-1"),
	}
	initialState, err := NewConsumerAuthorityState(current, initialFence, now.Add(-time.Minute))
	attackerPublic, attackerPrivate := testRoot(t, "attacker-root")
	attackerKeyset := initialKeyset
	attackerEnvelope := buildTestSignedEnvelope(t, attackerKeyset, authorityKeysetSchema, map[string]ed25519.PrivateKey{"root-attacker": attackerPrivate})
	attackerState := initialState
	attackerState.ActiveRootPin.RootKeyID = "root-attacker"
	attackerState.ActiveRootPin.RootPublicKey = base64.StdEncoding.EncodeToString(attackerPublic)
	attackerState.ActiveRootPin.RootPublicKeyDigest = canonicalDigestBytes(attackerPublic)
	attackerState.ActiveRootPin.KeysetDigest = attackerEnvelope.PayloadDigest
	attackerState.Fence.KeysetDigest = attackerEnvelope.PayloadDigest
	if err := store.CommitBootstrap(bootstrap, testHostIdentity(), attackerState, attackerEnvelope, now); err == nil {
		t.Fatal("self-consistent attacker root and signature replaced deployment root")
	}
	forgedInitial := buildTestSignedEnvelope(t, initialKeyset, authorityKeysetSchema, map[string]ed25519.PrivateKey{"root-old": newPrivate})
	if err := store.CommitBootstrap(bootstrap, testHostIdentity(), initialState, forgedInitial, now); err == nil {
		t.Fatal("forged initial root proof accepted")
	}
	forgedBootstrap := bootstrap
	forgedBootstrap.HostIdentityDigest = testDigest("forged-host")
	forgedBootstrapDigest, _ := BootstrapDigest(forgedBootstrap)
	forgedState := initialState
	forgedState.ActiveRootPin.BootstrapDigest, forgedState.Fence.BootstrapDigest = forgedBootstrapDigest, forgedBootstrapDigest
	forgedState.Fence.HostIdentityDigest = forgedBootstrap.HostIdentityDigest
	if err := store.CommitBootstrap(forgedBootstrap, testHostIdentity(), forgedState, initialEnvelope, now); err == nil {
		t.Fatal("forged bootstrap host binding accepted")
	}
	if err != nil || store.CommitBootstrap(bootstrap, testHostIdentity(), initialState, initialEnvelope, now) != nil {
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
	leafNextPublic, _ := testRoot(t, "leaf-next")
	leafKeyset := CodexAuthorityKeysetV1{
		SchemaVersion: authorityKeysetSchema, AuthorityNamespace: current.AuthorityNamespace, TrustRootGeneration: 2,
		PreviousKeysetDigest: &previous, ValidFrom: formatAuthorityTime(now.Add(time.Second)),
		Keys:          []AuthorityPublicKeyV1{{KeyID: "leaf-next", Usage: "evidence", Algorithm: "Ed25519", PublicKey: base64.StdEncoding.EncodeToString(leafNextPublic), NotBefore: formatAuthorityTime(now), NotAfter: formatAuthorityTime(now.Add(time.Hour))}},
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
	firstNonce := bytes.Repeat([]byte{3}, 32)
	secondNonce := bytes.Repeat([]byte{4}, 32)
	first, err := FreshHostAttestation(context.Background(), identity, firstNonce, fakeHostTPM{}, fakeHostTPM{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := FreshHostAttestation(context.Background(), identity, secondNonce, fakeHostTPM{}, fakeHostTPM{})
	if err != nil {
		t.Fatal(err)
	}
	if first.ChallengeNonce == second.ChallengeNonce || first.ChallengeSignature == second.ChallengeSignature {
		t.Fatal("fresh attestations reused operation identity")
	}
	fence := NewHostAttestationNonceFence()
	if err := ValidateHostAttestation(first, digest, firstNonce, fakeHostTPM{}, fence); err != nil {
		t.Fatal(err)
	}
	if err := ValidateHostAttestation(first, digest, firstNonce, fakeHostTPM{}, fence); err == nil {
		t.Fatal("replayed host attestation accepted")
	}
	if err := ValidateHostAttestation(second, digest, secondNonce, fakeHostTPM{}, fence); err != nil {
		t.Fatal(err)
	}
	if err := ValidateHostAttestation(first, testDigest("other-host"), firstNonce, fakeHostTPM{}, NewHostAttestationNonceFence()); err == nil {
		t.Fatal("host mismatch accepted")
	}
	if err := ValidateHostAttestation(second, digest, firstNonce, fakeHostTPM{}, NewHostAttestationNonceFence()); err == nil {
		t.Fatal("attestation-selected nonce accepted")
	}
}

func TestTopologySourceSealedChildTransition(t *testing.T) {
	object := func(role string, device, inode, mount, size uint64, digest *string) TopologyObjectV1 {
		return TopologyObjectV1{Identity: MountObjectIdentityV1{Role: role, DeviceMajor: device, Inode: inode, MountIDUnique: mount, Mode: 0o700, UID: 1, GID: 1, Size: size, SHA256: digest}, AncestorChain: []MountObjectIdentityV1{{Role: role + "Ancestor", DeviceMajor: 1, Inode: inode + 1000, MountIDUnique: 1, Mode: 0o755}}}
	}
	fixed := make([]TopologyObjectV1, len(fixedRootRoles))
	for index, role := range fixedRootRoles {
		fixed[index] = object(role, 1, uint64(index+10), 1, 0, nil)
	}
	binaryDigest := testDigest("binary")
	source := object("sourceExecutable", 1, 20, 10, 100, &binaryDigest)
	sealed := object("sealedExecutable", 2, 30, 20, 100, &binaryDigest)
	child := object("childExecutable", 2, 30, 20, 100, &binaryDigest)
	snapshots := []TopologySnapshotV1{
		{SchemaVersion: "marshal.codex.topology-snapshot.v1", MountNamespaceDevice: 1, MountNamespaceInode: 2, Phase: topologyPhases[0], FixedRoots: fixed, Executables: []TopologyObjectV1{source}},
		{SchemaVersion: "marshal.codex.topology-snapshot.v1", MountNamespaceDevice: 1, MountNamespaceInode: 2, Phase: topologyPhases[1], FixedRoots: fixed, Executables: []TopologyObjectV1{source, sealed}},
		{SchemaVersion: "marshal.codex.topology-snapshot.v1", MountNamespaceDevice: 1, MountNamespaceInode: 2, Phase: topologyPhases[2], FixedRoots: fixed, Executables: []TopologyObjectV1{source, sealed}},
		{SchemaVersion: "marshal.codex.topology-snapshot.v1", MountNamespaceDevice: 1, MountNamespaceInode: 2, Phase: topologyPhases[3], FixedRoots: fixed, Executables: []TopologyObjectV1{source, sealed, child}},
		{SchemaVersion: "marshal.codex.topology-snapshot.v1", MountNamespaceDevice: 1, MountNamespaceInode: 2, Phase: topologyPhases[4], FixedRoots: fixed, Executables: []TopologyObjectV1{source, sealed, child}},
	}
	if err := ValidateTopologyTransition(snapshots); err != nil {
		t.Fatalf("valid topology: %v", err)
	}
	bad := append([]TopologySnapshotV1(nil), snapshots...)
	bad[4] = snapshots[4]
	bad[4].Executables = append([]TopologyObjectV1(nil), snapshots[4].Executables...)
	bad[4].Executables[2].Identity.Inode++
	if err := ValidateTopologyTransition(bad); err == nil {
		t.Fatal("child identity drift accepted")
	}
	bad = append([]TopologySnapshotV1(nil), snapshots...)
	bad[1] = snapshots[1]
	bad[1].Executables = append([]TopologyObjectV1(nil), snapshots[1].Executables...)
	bad[1].Executables[1].Identity.DeviceMajor = source.Identity.DeviceMajor
	bad[1].Executables[1].Identity.Inode = source.Identity.Inode
	bad[1].Executables[1].Identity.MountIDUnique = source.Identity.MountIDUnique
	if err := ValidateTopologyTransition(bad); err == nil {
		t.Fatal("source/sealed alias accepted")
	}
}

func TestHeldLaunchRootsRejectRenameRelinkAndCoverControlIO(t *testing.T) {
	authorityRoot, fenceRoot, worktree, control := realTempDir(t), realTempDir(t), realTempDir(t), realTempDir(t)
	for _, name := range []string{"input", "output"} {
		if err := os.Mkdir(filepath.Join(control, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(control, "input"), 0o500); err != nil {
		t.Fatal(err)
	}
	roots, err := OpenHeldLaunchRoots(authorityRoot, fenceRoot, worktree, control)
	if runtime.GOOS != "linux" {
		if err == nil {
			roots.Close()
			t.Fatal("Darwin held mount identity unexpectedly enabled")
		}
		return
	}
	if err != nil {
		var failure *AuthorityFailure
		if errors.As(err, &failure) && failure.Code == "codex_mount_identity_unsupported" {
			t.Skip("kernel lacks STATX_MNT_ID_UNIQUE")
		}
		t.Fatal(err)
	}
	defer roots.Close()
	if len(roots.paths) != 6 || roots.paths[4].role != "controlInput" || roots.paths[5].role != "controlOutput" || roots.Verify() != nil {
		t.Fatal("held root set is incomplete")
	}
	replacement := realTempDir(t)
	retained := worktree + "-retained"
	if err := os.Rename(worktree, retained); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, worktree); err != nil {
		t.Fatal(err)
	}
	if err := roots.Verify(); err == nil {
		t.Fatal("worktree rename/relink accepted")
	}
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
	if !port.IsPermanent(err) || failure.Validate() != nil {
		t.Fatalf("Core permanent classification = %v, %v", port.IsPermanent(err), failure.Validate())
	}
	transient := newAuthorityFailure("probe", "codex_fence_lock_busy", "busy", AuthorityFailureDetails{}, nil, authorityNow())
	if port.IsPermanent(transient) || transient.RetryClass != "transient" || transient.Validate() != nil {
		t.Fatalf("transient classification = %#v", transient)
	}
	reconcile := newAuthorityFailure("launch", "codex_launch_outcome_ambiguous", "ambiguous", AuthorityFailureDetails{}, nil, authorityNow())
	if port.IsPermanent(reconcile) || reconcile.RetryClass != "reconcile-required" || reconcile.Validate() != nil {
		t.Fatalf("reconcile classification = %#v", reconcile)
	}
	invalid := *failure
	invalid.RetryClass = "transient"
	if invalid.Validate() == nil {
		t.Fatal("closed retry mapping accepted mismatched carrier")
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
	_, parserPrivate := testRoot(t, "parser")
	configEnvelope := buildTestSignedEnvelope(t, config, authorityConfigSchema, map[string]ed25519.PrivateKey{"config-key": parserPrivate})
	configJSON, _ := json.Marshal(configEnvelope)
	if _, _, err := ParseCodexAuthorityConfig(configJSON); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if _, _, err := ParseCodexAuthorityConfig([]byte(`{"payload":{},"payload":{},"payloadDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","signatures":[]}`)); err == nil {
		t.Fatal("duplicate config member accepted")
	}
	if _, _, err := ParseCodexAuthorityConfig([]byte(`{"unknown":true}`)); err == nil {
		t.Fatal("unknown config member accepted")
	}
	receiptEnvelope := buildTestSignedEnvelope(t, receipt, probeReceiptSchema, map[string]ed25519.PrivateKey{"receipt-key": parserPrivate})
	receiptJSON, _ := json.Marshal(receiptEnvelope)
	if _, _, err := ParseCodexProbeReceipt(receiptJSON); err != nil {
		t.Fatalf("parse receipt: %v", err)
	}
	if err := validateAuthorityProjection(config, evidence, evidenceDigest, []CodexProbeExecutionReceiptV1{receipt}, map[string]string{"success": receiptDigest}); err != nil {
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
	if err := validateAuthorityProjection(revoked, evidence, evidenceDigest, []CodexProbeExecutionReceiptV1{receipt}, map[string]string{"success": receiptDigest}); err == nil {
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

type testAuthorityBundle struct {
	now             time.Time
	pin             CodexActiveRootPinV1
	keyset          SignedEnvelopeV1
	config          SignedEnvelopeV1
	evidence        SignedEnvelopeV1
	observation     SignedEnvelopeV1
	receipts        []SignedEnvelopeV1
	hostNonce       []byte
	configPrivate   ed25519.PrivateKey
	evidencePrivate ed25519.PrivateKey
}

func newTestAuthorityBundle(t *testing.T) testAuthorityBundle {
	t.Helper()
	now := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	rootPublic, rootPrivate := testRoot(t, "bundle-root")
	configPublic, configPrivate := testRoot(t, "bundle-config")
	evidencePublic, evidencePrivate := testRoot(t, "bundle-evidence")
	receiptPublic, receiptPrivate := testRoot(t, "bundle-receipt")
	verifierPublic, verifierPrivate := testRoot(t, "bundle-verifier")
	keysetPayload := CodexAuthorityKeysetV1{
		SchemaVersion: authorityKeysetSchema, AuthorityNamespace: "marshal.codex.production", TrustRootGeneration: 1,
		PreviousKeysetDigest: nil, ValidFrom: formatAuthorityTime(now.Add(-2 * time.Hour)),
		Keys: []AuthorityPublicKeyV1{
			{KeyID: "config-key", Usage: "config", Algorithm: "Ed25519", PublicKey: base64.StdEncoding.EncodeToString(configPublic), NotBefore: formatAuthorityTime(now.Add(-2 * time.Hour)), NotAfter: formatAuthorityTime(now.Add(2 * time.Hour))},
			{KeyID: "evidence-key", Usage: "evidence", Algorithm: "Ed25519", PublicKey: base64.StdEncoding.EncodeToString(evidencePublic), NotBefore: formatAuthorityTime(now.Add(-2 * time.Hour)), NotAfter: formatAuthorityTime(now.Add(2 * time.Hour))},
			{KeyID: "receipt-key", Usage: "probe-receipt", Algorithm: "Ed25519", PublicKey: base64.StdEncoding.EncodeToString(receiptPublic), NotBefore: formatAuthorityTime(now.Add(-2 * time.Hour)), NotAfter: formatAuthorityTime(now.Add(2 * time.Hour))},
			{KeyID: "verifier-key", Usage: "verifier-attestation", Algorithm: "Ed25519", PublicKey: base64.StdEncoding.EncodeToString(verifierPublic), NotBefore: formatAuthorityTime(now.Add(-2 * time.Hour)), NotAfter: formatAuthorityTime(now.Add(2 * time.Hour))},
		}, RevokedKeyIDs: []string{}, RootRotation: nil,
	}
	keyset := buildTestSignedEnvelope(t, keysetPayload, authorityKeysetSchema, map[string]ed25519.PrivateKey{"root-key": rootPrivate})
	pin := CodexActiveRootPinV1{SchemaVersion: activeRootPinSchema, AuthorityNamespace: keysetPayload.AuthorityNamespace, BootstrapDigest: testDigest("bundle-bootstrap"), RootKeyID: "root-key", RootAlgorithm: "Ed25519", RootPublicKey: base64.StdEncoding.EncodeToString(rootPublic), RootPublicKeyDigest: canonicalDigestBytes(rootPublic), TrustRootGeneration: 1, KeysetDigest: keyset.PayloadDigest, ActivatedAt: formatAuthorityTime(now.Add(-time.Hour))}
	hostIdentity := testHostIdentity()
	hostDigest, _ := HostIdentityDigest(hostIdentity)
	hostNonce := bytes.Repeat([]byte{9}, 32)
	hostAttestation, err := FreshHostAttestation(context.Background(), hostIdentity, hostNonce, fakeHostTPM{}, fakeHostTPM{})
	if err != nil {
		t.Fatal(err)
	}
	binary := ExecutableIdentityV1{CanonicalRealpath: "/usr/bin/codex", DeviceMajor: 1, Inode: 2, MountIDUnique: 3, Size: 100, Mode: 0o755, SHA256: testDigest("binary"), Version: "1.2.3", VersionOutputDigest: testDigest("version")}
	binaryDigest, _ := canonicalDigest(binary)
	contract := CodexContractBindingV1{AdapterContractDigest: testDigest("adapter"), LauncherBuildDigest: testDigest("launcher"), ProfileDigest: testDigest("profile"), ArgvMatrixDigest: testDigest("argv-matrix"), EnvironmentDigest: testDigest("environment"), EventContractDigest: testDigest("event"), PermissionContractDigest: testDigest("permission"), ToolPolicyDigest: testDigest("tools"), ResultContractDigest: testDigest("result"), OutputLimitDigest: testDigest("output"), NativeBudgetsDigest: testDigest("budgets"), ExecutionProfiles: []string{"read-only", "workspace-write"}}
	contractDigest, _ := canonicalDigest(contract)
	receipt := CodexProbeExecutionReceiptV1{SchemaVersion: probeReceiptSchema, AuthorityNamespace: keysetPayload.AuthorityNamespace, AuthorityGeneration: 1, TrustRootGeneration: 1, BootstrapID: hostIdentity.BootstrapID, SuiteDigest: testDigest("suite"), ProbeArtifactDigest: testDigest("artifact"), VariantID: "default", ChallengeNonce: testNonce(7), StartedAt: formatAuthorityTime(now.Add(-30 * time.Minute)), EndedAt: formatAuthorityTime(now.Add(-29 * time.Minute)), HostIdentityDigest: hostDigest, BinaryIdentityDigest: binaryDigest, ArgvDigest: testDigest("argv"), EnvironmentDigest: contract.EnvironmentDigest, TopologyDigest: testDigest("topology"), TranscriptDigest: testDigest("transcript"), MarkerDigest: testDigest("marker"), EventContractDigest: contract.EventContractDigest, PermissionContractDigest: contract.PermissionContractDigest, ReceiptKeyID: "receipt-key"}
	receipt.ReceiptChallengeDigest, _ = receiptChallengeDigest(receipt)
	receiptEnvelope := buildTestSignedEnvelope(t, receipt, probeReceiptSchema, map[string]ed25519.PrivateKey{"receipt-key": receiptPrivate})
	aggregate, _ := AggregateChallengeDigest([]CodexProbeExecutionReceiptV1{receipt}, map[string]string{receipt.VariantID: receiptEnvelope.PayloadDigest})
	verdicts := AuthorityVerdictsV1{true, true, true, true, true}
	observation := CodexProbeObservationV1{SchemaVersion: "marshal.codex.probe-observation.v1", AuthorityNamespace: receipt.AuthorityNamespace, AuthorityGeneration: 1, TrustRootGeneration: 1, BootstrapID: hostIdentity.BootstrapID, ObservationNonce: testNonce(8), VerifierKeyID: "verifier-key", VerifierBuildDigest: testDigest("verifier"), ObservedAt: formatAuthorityTime(now.Add(-20 * time.Minute)), ValidUntil: formatAuthorityTime(now.Add(time.Hour)), HostAttestation: hostAttestation, BinaryIdentity: binary, Contract: contract, SuiteDigest: receipt.SuiteDigest, ProbeArtifactDigest: receipt.ProbeArtifactDigest, AggregateChallengeDigest: aggregate, TopologyDigest: receipt.TopologyDigest, ReceiptDigests: []string{receiptEnvelope.PayloadDigest}, Verdicts: verdicts}
	observationEnvelope := buildTestSignedEnvelope(t, observation, "marshal.codex.probe-observation.v1", map[string]ed25519.PrivateKey{"verifier-key": verifierPrivate})
	evidence := CodexProductionEvidenceV1{SchemaVersion: productionEvidenceSchema, AuthorityNamespace: receipt.AuthorityNamespace, AuthorityGeneration: 1, TrustRootGeneration: 1, BootstrapID: hostIdentity.BootstrapID, EvidenceKeyID: "evidence-key", ObservationDigest: observationEnvelope.PayloadDigest, ReceiptDigests: observation.ReceiptDigests, IssuedAt: formatAuthorityTime(now.Add(-15 * time.Minute)), ValidFrom: formatAuthorityTime(now.Add(-19 * time.Minute)), ValidUntil: formatAuthorityTime(now.Add(30 * time.Minute)), HostIdentityDigest: hostDigest, BinaryIdentityDigest: binaryDigest, ContractDigest: contractDigest, ProfileDigest: contract.ProfileDigest, SuiteDigest: receipt.SuiteDigest, ProbeArtifactDigest: receipt.ProbeArtifactDigest, AggregateChallengeDigest: aggregate, TopologyDigest: receipt.TopologyDigest, VerifierKeyID: observation.VerifierKeyID, ProbeReceiptKeyID: receipt.ReceiptKeyID, Verdicts: verdicts}
	evidenceEnvelope := buildTestSignedEnvelope(t, evidence, productionEvidenceSchema, map[string]ed25519.PrivateKey{"evidence-key": evidencePrivate})
	config := CodexAuthorityConfigV1{SchemaVersion: authorityConfigSchema, AuthorityNamespace: receipt.AuthorityNamespace, AuthorityGeneration: 1, TrustRootGeneration: 1, KeysetDigest: keyset.PayloadDigest, CurrentEvidenceDigest: evidenceEnvelope.PayloadDigest, RevokedEvidenceDigests: []string{}, RevokedSuiteDigests: []string{}, RevokedChallengeDigests: []string{}, HostIdentityDigest: hostDigest, BootstrapID: hostIdentity.BootstrapID, SuiteDigest: receipt.SuiteDigest, ProbeArtifactDigest: receipt.ProbeArtifactDigest, AggregateChallengeDigest: aggregate, ContractDigest: contractDigest, ProfileDigest: contract.ProfileDigest, ConfigKeyID: "config-key", IssuedAt: formatAuthorityTime(now.Add(-10 * time.Minute))}
	config.RevocationSetDigest, _ = RevocationSetDigest(config)
	configEnvelope := buildTestSignedEnvelope(t, config, authorityConfigSchema, map[string]ed25519.PrivateKey{"config-key": configPrivate})
	return testAuthorityBundle{now: now, pin: pin, keyset: keyset, config: configEnvelope, evidence: evidenceEnvelope, observation: observationEnvelope, receipts: []SignedEnvelopeV1{receiptEnvelope}, hostNonce: hostNonce, configPrivate: configPrivate, evidencePrivate: evidencePrivate}
}

func TestSignedAuthorityBundleDomainsUsageFreshnessAndReplay(t *testing.T) {
	fixture := newTestAuthorityBundle(t)
	verify := func(bundle testAuthorityBundle, fence *HostAttestationNonceFence) error {
		_, err := VerifyAuthorityBundle(bundle.now, bundle.pin, bundle.keyset, bundle.config, bundle.evidence, bundle.observation, bundle.receipts, bundle.hostNonce, fakeHostTPM{}, fence)
		return err
	}
	fence := NewHostAttestationNonceFence()
	if err := verify(fixture, fence); err != nil {
		t.Fatalf("valid signed authority bundle: %v", err)
	}
	if err := verify(fixture, fence); err == nil {
		t.Fatal("host nonce replay accepted")
	}
	unsigned := fixture
	unsigned.config.Signatures = nil
	if err := verify(unsigned, NewHostAttestationNonceFence()); err == nil {
		t.Fatal("unsigned config accepted")
	}
	unsigned = fixture
	unsigned.evidence.Signatures = nil
	if err := verify(unsigned, NewHostAttestationNonceFence()); err == nil {
		t.Fatal("unsigned evidence accepted")
	}
	unsigned = fixture
	unsigned.receipts = append([]SignedEnvelopeV1(nil), fixture.receipts...)
	unsigned.receipts[0].Signatures = nil
	if err := verify(unsigned, NewHostAttestationNonceFence()); err == nil {
		t.Fatal("unsigned receipt accepted")
	}
	unsigned = fixture
	unsigned.observation.Signatures = nil
	if err := verify(unsigned, NewHostAttestationNonceFence()); err == nil {
		t.Fatal("unsigned observation accepted")
	}
	wrongDomain := fixture
	var evidence CodexProductionEvidenceV1
	if err := json.Unmarshal(fixture.evidence.Payload, &evidence); err != nil {
		t.Fatal(err)
	}
	wrongDomain.evidence = buildTestSignedEnvelope(t, evidence, "marshal.codex.wrong-domain.v1", map[string]ed25519.PrivateKey{"evidence-key": fixture.evidencePrivate})
	if err := verify(wrongDomain, NewHostAttestationNonceFence()); err == nil {
		t.Fatal("wrong evidence domain accepted")
	}
	wrongUsage := fixture
	evidence.EvidenceKeyID = "config-key"
	wrongUsage.evidence = buildTestSignedEnvelope(t, evidence, productionEvidenceSchema, map[string]ed25519.PrivateKey{"config-key": fixture.configPrivate})
	if err := verify(wrongUsage, NewHostAttestationNonceFence()); err == nil {
		t.Fatal("wrong evidence key usage accepted")
	}
	wrongReceiptProjection := fixture
	evidence.EvidenceKeyID = "evidence-key"
	evidence.ProbeReceiptKeyID = "config-key"
	wrongReceiptProjection.evidence = buildTestSignedEnvelope(t, evidence, productionEvidenceSchema, map[string]ed25519.PrivateKey{"evidence-key": fixture.evidencePrivate})
	if err := verify(wrongReceiptProjection, NewHostAttestationNonceFence()); err == nil {
		t.Fatal("receipt key projection mismatch accepted")
	}
	stale := fixture
	stale.now = fixture.now.Add(25 * time.Hour)
	if err := verify(stale, NewHostAttestationNonceFence()); err == nil {
		t.Fatal("stale observation/evidence accepted")
	}
}

func TestStateLockFileIsPrivateSingleLink(t *testing.T) {
	stateRoot, authorityRoot := realTempDir(t), realTempDir(t)
	store, err := OpenCodexConsumerAuthorityStore(stateRoot, authorityRoot, testBootstrapRoot(t, "root", "root"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_ = commitTestBootstrap(t, store, testState(t, 1, 1, testDigest("keyset"), testDigest("config")))
	var stat unix.Stat_t
	if err := unix.Stat(filepath.Join(stateRoot, consumerLockName), &stat); err != nil {
		t.Fatal(err)
	}
	if stat.Mode&0o7777 != 0o600 || stat.Nlink != 1 {
		t.Fatalf("lock mode/link = %o/%d", stat.Mode&0o7777, stat.Nlink)
	}
}
